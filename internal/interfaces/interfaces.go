// Package interfaces talks to the interface processes — rfd, hs485d, hmipserver — over their own
// RPC, as the frontend does, for the little occulited needs: the device list with its firmware
// versions (D-27) and the refresh call after a firmware bundle was deployed. go-hmccu (GPL-3.0,
// D-15) does the wire protocols; the URLs come from /etc/config/InterfacesList.xml.
package interfaces

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mdzio/go-hmccu/v2/itf"
	"github.com/mdzio/go-hmccu/v2/itf/binrpc"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// Interface is one entry of InterfacesList.xml with a ready client.
type Interface struct {
	Name   string
	URL    string
	caller xmlrpc.Caller
}

// Device is a device (not a channel) as an interface reports it.
type Device struct {
	Interface         string `json:"interface"`
	Address           string `json:"address"`
	Type              string `json:"type"`
	Firmware          string `json:"firmware"`
	AvailableFirmware string `json:"available_firmware,omitempty"`
	// Updatable is the interface's UPDATABLE (B-225): false for a device that cannot be updated
	// over the air at all (rfd says so for an HM-CC-TC, an HM-Sec-SC); absent when the interface
	// does not say.
	Updatable *bool `json:"updatable,omitempty"`
	// UpdateState is the interface's FIRMWARE_UPDATE_STATE as it was last read (occulited task
	// 44): hmipserver's background transfer states (NEW_FIRMWARE_AVAILABLE, DELIVER_FIRMWARE_IMAGE,
	// READY_FOR_UPDATE, DO_UPDATE_PENDING, PERFORMING_UPDATE, UP_TO_DATE, the LIVE_… states of the
	// access points); empty when the interface does not say.
	UpdateState string `json:"update_state,omitempty"`
}

// updatable reads UPDATABLE as rfd (an integer or a boolean) and hmipserver (a boolean) send it;
// nil when it is missing or neither. Read from the raw value: Query.Bool would mark the whole
// answer invalid for an integer.
func updatable(q *xmlrpc.Query) *bool {
	v := q.TryKey("UPDATABLE").Value()
	if v == nil {
		return nil
	}
	var raw string
	switch {
	case v.Boolean != "":
		raw = v.Boolean
	case v.I4 != "":
		raw = v.I4
	case v.Int != "":
		raw = v.Int
	default:
		return nil
	}
	b := strings.TrimSpace(raw) != "0"
	return &b
}

// FromList builds clients for the given name/url pairs. Unknown schemes are skipped; the caller
// decides whether an empty result is an error.
func FromList(entries []struct{ Name, URL string }) []Interface {
	var out []Interface
	for _, e := range entries {
		c, err := callerFor(e.URL)
		if err != nil {
			continue
		}
		out = append(out, Interface{Name: e.Name, URL: e.URL, caller: c})
	}
	return out
}

func callerFor(raw string) (xmlrpc.Caller, error) {
	// "xmlrpc_bin" is not a URL scheme net/url accepts (the underscore), so split by hand
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return nil, fmt.Errorf("unsupported interface URL %q", raw)
	}
	switch scheme {
	case "xmlrpc", "http":
		// go-hmccu's client prepends "http://" itself: Addr is host[:port][/path]
		if _, err := url.Parse("http://" + rest); err != nil {
			return nil, err
		}
		return &xmlrpc.Client{Addr: rest}, nil
	case "xmlrpc_bin":
		host := rest
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		return &binrpc.Client{Addr: host}, nil
	}
	return nil, fmt.Errorf("unsupported interface URL %q", raw)
}

// Devices lists the devices (channels folded away) of every interface, in parallel with a
// timeout; an interface that does not answer is reported in errs and the rest is returned.
func Devices(ctx context.Context, ifs []Interface, timeout time.Duration) (devices []Device, errs map[string]error) {
	errs = map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, i := range ifs {
		wg.Add(1)
		go func(i Interface) {
			defer wg.Done()
			type listed struct {
				d     *itf.DeviceDescription
				upd   *bool
				state string
			}
			ch := make(chan struct {
				d   []listed
				err error
			}, 1)
			go func() {
				// the raw answer, as go-hmccu's ListDevices reads it, plus UPDATABLE, which its
				// DeviceDescription does not carry (B-225)
				var out []listed
				v, err := i.caller.Call("listDevices", []*xmlrpc.Value{})
				if err == nil {
					e := xmlrpc.Q(v)
					for _, av := range e.Slice() {
						d := &itf.DeviceDescription{}
						d.ReadFrom(av)
						out = append(out, listed{d, updatable(av), strings.TrimSpace(av.TryKey("FIRMWARE_UPDATE_STATE").String())})
					}
					if e.Err() != nil {
						err = fmt.Errorf("invalid answer to listDevices: %w", e.Err())
					}
				}
				ch <- struct {
					d   []listed
					err error
				}{out, err}
			}()
			select {
			case <-ctx.Done():
				mu.Lock()
				errs[i.Name] = ctx.Err()
				mu.Unlock()
			case <-time.After(timeout):
				mu.Lock()
				errs[i.Name] = fmt.Errorf("no answer within %s", timeout)
				mu.Unlock()
			case r := <-ch:
				mu.Lock()
				if r.err != nil {
					errs[i.Name] = r.err
				} else {
					for _, l := range r.d {
						d := l.d
						if d.Parent != "" || strings.Contains(d.Address, ":") {
							continue // a channel
						}
						if strings.HasSuffix(d.Type, "-RCV-50") {
							continue // the CCU's own virtual receiver; it has no firmware to fetch
						}
						devices = append(devices, Device{Interface: i.Name, Address: d.Address, Type: d.Type, Firmware: d.Firmware, AvailableFirmware: d.AvailableFirmware, Updatable: l.upd, UpdateState: l.state})
					}
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	sort.Slice(devices, func(a, b int) bool {
		if devices[a].Interface != devices[b].Interface {
			return devices[a].Interface < devices[b].Interface
		}
		return devices[a].Address < devices[b].Address
	})
	return devices, errs
}

// FirmwareInterfaces are the interfaces that know refreshDeployedDeviceFirmwareList and read the
// user firmware directory: rfd's BidCos-RF and hmipserver's HmIP-RF - the two the WebUI refreshes
// (B-225). hs485d (BidCos-Wired) has neither, VirtualDevices neither.
var FirmwareInterfaces = map[string]bool{"BidCos-RF": true, "HmIP-RF": true}

// RefreshFirmware asks BidCos-RF and HmIP-RF among ifs to re-read /etc/config/firmware, as the
// WebUI's Interface.refreshDeployedDeviceFirmwareList does; the others are left alone. Failures
// are per interface.
func RefreshFirmware(ifs []Interface) map[string]error {
	errs := map[string]error{}
	for _, i := range ifs {
		if !FirmwareInterfaces[i.Name] {
			continue
		}
		if _, err := i.caller.Call("refreshDeployedDeviceFirmwareList", nil); err != nil {
			errs[i.Name] = err
		}
	}
	return errs
}

// Addresses returns every address (devices and channels) each interface reports, keyed by
// interface name — what the metadata store needs to decide which refs are orphaned.
func Addresses(ctx context.Context, ifs []Interface, timeout time.Duration) (map[string][]string, map[string]error) {
	out := map[string][]string{}
	errs := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, i := range ifs {
		wg.Add(1)
		go func(i Interface) {
			defer wg.Done()
			type res struct {
				d   []*itf.DeviceDescription
				err error
			}
			ch := make(chan res, 1)
			go func() {
				c := itf.DeviceLayerClient{Name: i.Name, Caller: i.caller}
				d, err := c.ListDevices()
				ch <- res{d, err}
			}()
			select {
			case <-ctx.Done():
				mu.Lock()
				errs[i.Name] = ctx.Err()
				mu.Unlock()
			case <-time.After(timeout):
				mu.Lock()
				errs[i.Name] = fmt.Errorf("no answer within %s", timeout)
				mu.Unlock()
			case r := <-ch:
				mu.Lock()
				if r.err != nil {
					errs[i.Name] = r.err
				} else {
					addrs := make([]string, 0, len(r.d))
					for _, d := range r.d {
						addrs = append(addrs, d.Address)
					}
					sort.Strings(addrs)
					out[i.Name] = addrs
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	return out, errs
}

// RadioInterface is one entry of listBidcosInterfaces: rfd and hmipserver both answer it, for
// the radio modules and LAN gateways they drive. DutyCycle is the percentage of the hourly
// airtime budget used (-1 when the interface does not report one).
type RadioInterface struct {
	Interface   string `json:"interface"` // BidCos-RF, HmIP-RF
	Address     string `json:"address"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Connected   bool   `json:"connected"`
	Default     bool   `json:"default"`
	Firmware    string `json:"firmware,omitempty"`
	DutyCycle   int    `json:"duty_cycle"`
	// Radio and RadioName: which physical transmitter the entry is (task 156, radio.Plan.Transmitter),
	// set by the API from the radio plan; entries with one key are one radio
	Radio     string `json:"radio,omitempty"`
	RadioName string `json:"radio_name,omitempty"`
	// Module, Adapter and Path: the radio as the Status page's interface card names it (occulited
	// task 13, radio.Plan.Link) - the module or gateway, the HB-RF board it sits on, and how the
	// process reaches it (multimacd, direct, usb, lan); set by the API, absent where the plan does
	// not know the entry
	Module  string `json:"module,omitempty"`
	Adapter string `json:"adapter,omitempty"`
	Path    string `json:"path,omitempty"`
	// CarrierSense is CARRIER_SENSE_LEVEL, rounded to a whole percent; nil when nobody reported it.
	// A pointer because 0 % is a value: the RPI-RF-MOD on the Charly reads 0.0, and an omitempty int
	// dropped it as "not reported".
	CarrierSense *int `json:"carrier_sense,omitempty"`
	// CarrierSenseSource says where the level comes from (task 68): "interface" when the
	// listBidcosInterfaces entry carries it, "device" when it is read from channel 0 of the radio
	// module's own device, "event" when the module reported it to the RPC process lately (task
	// 75). "device" without a value means the device has the datapoint but the module has not
	// reported it yet (a few minutes after the radio stack starts).
	CarrierSenseSource string `json:"carrier_sense_source,omitempty"`
}

// number reads an XML-RPC int or double; ok is false for anything else or an empty value.
func number(q *xmlrpc.Query) (float64, bool) {
	if !q.IsNotEmpty() {
		return 0, false
	}
	var f float64
	ok := true
	switch v := q.Any().(type) {
	case int:
		f = float64(v)
	case float64:
		f = v
	default:
		ok = false
	}
	if q.Err() != nil {
		ok = false
	}
	q.SetErr(nil)
	return f, ok
}

func percent(f float64) *int {
	n := int(math.Round(f))
	return &n
}

// ListInterfaces asks every interface process for its radio interfaces (duty cycle, link).
// Interfaces that do not answer are reported in errs; one that answers the call with a fault has
// no radio list (see SampleInterfaces) and is left out of both.
func ListInterfaces(ctx context.Context, ifs []Interface, timeout time.Duration) (out []RadioInterface, errs map[string]error) {
	out, _, errs = SampleInterfaces(ctx, ifs, timeout)
	return out, errs
}

// SampleInterfaces is ListInterfaces with the processes that answered but have no radio list:
// hs485d (BidCos-Wired) and a user interface such as CUxD do not implement listBidcosInterfaces
// and answer it with a fault ("unknown method name"). A fault is an answer - the process is up -
// so such an interface is in answered, not in errs. VirtualDevices is not asked.
func SampleInterfaces(ctx context.Context, ifs []Interface, timeout time.Duration) (out []RadioInterface, answered []string, errs map[string]error) {
	errs = map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, i := range ifs {
		if i.Name == "VirtualDevices" || strings.Contains(i.URL, "/groups") {
			continue
		}
		wg.Add(1)
		go func(i Interface) {
			defer wg.Done()
			type res struct {
				v   *xmlrpc.Value
				err error
			}
			ch := make(chan res, 1)
			go func() {
				v, err := i.caller.Call("listBidcosInterfaces", nil)
				ch <- res{v, err}
			}()
			var r res
			select {
			case <-ctx.Done():
				r.err = ctx.Err()
			case <-time.After(timeout):
				r.err = fmt.Errorf("no answer within %s", timeout)
			case r = <-ch:
			}
			mu.Lock()
			defer mu.Unlock()
			var fault *xmlrpc.MethodError
			if errors.As(r.err, &fault) {
				answered = append(answered, i.Name)
				return
			}
			if r.err != nil {
				errs[i.Name] = r.err
				return
			}
			q := xmlrpc.Q(r.v)
			for _, e := range q.Slice() {
				ri := RadioInterface{Interface: i.Name, DutyCycle: -1}
				ri.Address = e.TryKey("ADDRESS").String()
				ri.Type = e.TryKey("TYPE").String()
				ri.Description = e.TryKey("DESCRIPTION").String()
				ri.Connected = e.TryKey("CONNECTED").Bool()
				ri.Default = e.TryKey("DEFAULT").Bool()
				ri.Firmware = e.TryKey("FIRMWARE_VERSION").String()
				if dc := e.TryKey("DUTY_CYCLE"); dc.IsNotEmpty() {
					ri.DutyCycle = dc.Int()
				}
				if cs, ok := number(e.TryKey("CARRIER_SENSE_LEVEL")); ok {
					ri.CarrierSense, ri.CarrierSenseSource = percent(cs), "interface"
				}
				e.SetErr(nil)
				out = append(out, ri)
			}
		}(i)
	}
	wg.Wait()
	sort.Slice(out, func(a, b int) bool {
		if out[a].Interface != out[b].Interface {
			return out[a].Interface < out[b].Interface
		}
		return out[a].Address < out[b].Address
	})
	sort.Strings(answered)
	return out, answered, errs
}

// A listBidcosInterfaces ADDRESS is the module's SGTIN (3014F711A000040000000A01), its device in
// listDevices the SGTIN's last 14 digits, 00040000000A01 (SUBTYPE CCU) - measured on the
// HmIP-RFUSB and the RPI-RF-MOD. The first ten digits are the SGTIN-96 header, filter, partition
// and the GS1 company prefix, which is not always eQ-3's 3014F711A0: the SilverCrest HmIP-HAP-B1
// is 30150377DC... (TARGA's prefix, openccu-lite task 217), so the rule is the SGTIN-96 shape,
// not eQ-3's prefix.
const sgtinDeviceDigits = 14

// ModuleChannel is channel 0 of the radio module's own device for a listBidcosInterfaces
// ADDRESS (the SGTIN without its prefix): where hmipserver reports the module's carrier sense
// and duty cycle, as datapoints and as events (task 75).
func ModuleChannel(addr string) string { return deviceAddress(addr) + ":0" }

// deviceAddress is the module's device address for a listBidcosInterfaces ADDRESS: the SGTIN
// without its prefix, or the address as it is when it is not a 24-digit SGTIN.
func deviceAddress(addr string) string {
	if IsSGTIN(addr) {
		return strings.ToUpper(addr[len(addr)-sgtinDeviceDigits:])
	}
	return addr
}

// IsSGTIN: 24 hex digits with the SGTIN-96 header 0x30, whatever the company prefix.
func IsSGTIN(s string) bool {
	if len(s) != 24 || s[:2] != "30" {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// SGTINDevice is the device address hmipserver gives the device with that SGTIN: its last 14
// digits. "" for something that is not an SGTIN.
func SGTINDevice(sgtin string) string {
	if !IsSGTIN(sgtin) {
		return ""
	}
	return deviceAddress(sgtin)
}

// DeviceLevels fills the carrier sense of an HmIP radio whose listBidcosInterfaces entry does not
// carry one (task 68): neither the HmIP-RFUSB nor the RPI-RF-MOD puts CARRIER_SENSE_LEVEL there,
// but both have it in channel 0 of the module's own device. Whether a device has the datapoint is
// asked once (getParamsetDescription); a device that does not - a LAN gateway, an address
// hmipserver does not know - is left alone for RetryAfter. The value itself is read with getValue
// at every poll; a fault there is "not reported yet", which is what hmipserver answers until the
// module has sent it, a few minutes after the radio stack started. The zero value is ready to use.
type DeviceLevels struct {
	// Recent, when set, answers with a carrier sense the module reported as an event lately
	// (task 75): the poll takes it and makes no getValue. nil, or ok false, is the poll as before.
	Recent func(iface, channel string) (level *int, ok bool)

	RetryAfter time.Duration    // a device without the datapoint is asked again after this; 0 = 15 minutes
	Now        func() time.Time // nil = time.Now

	mu    sync.Mutex
	known map[string]levelProbe // interface name + "/" + device address
}

type levelProbe struct {
	has bool
	at  time.Time
}

func (d *DeviceLevels) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// probe returns what is known about the device: has the datapoint, and whether that is known.
func (d *DeviceLevels) probe(key string) (has, known bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.known[key]
	if !ok {
		return false, false
	}
	retry := d.RetryAfter
	if retry == 0 {
		retry = 15 * time.Minute
	}
	if !p.has && d.now().Sub(p.at) >= retry {
		delete(d.known, key)
		return false, false
	}
	return p.has, true
}

func (d *DeviceLevels) remember(key string, has bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.known == nil {
		d.known = map[string]levelProbe{}
	}
	d.known[key] = levelProbe{has: has, at: d.now()}
}

// Fill sets CarrierSense and CarrierSenseSource on the HmIP entries of list that have no level of
// their own, asking the interface each entry came from. Nothing here is an error for the caller:
// an interface that does not answer leaves the entry as it was and is asked again next time.
func (d *DeviceLevels) Fill(ctx context.Context, ifs []Interface, list []RadioInterface, timeout time.Duration) {
	for n := range list {
		ri := &list[n]
		if ri.CarrierSense != nil || !strings.HasPrefix(ri.Interface, "HmIP") || ri.Address == "" {
			continue
		}
		var caller xmlrpc.Caller
		for _, i := range ifs {
			if i.Name == ri.Interface {
				caller = i.caller
				break
			}
		}
		if caller == nil {
			continue
		}
		channel := deviceAddress(ri.Address) + ":0"
		if d.Recent != nil {
			if cs, ok := d.Recent(ri.Interface, channel); ok {
				ri.CarrierSense, ri.CarrierSenseSource = cs, "event"
				continue
			}
		}
		key := ri.Interface + "/" + channel
		has, known := d.probe(key)
		if !known {
			v, err := callWithin(ctx, caller, timeout, "getParamsetDescription", xmlrpc.NewString(channel), xmlrpc.NewString("VALUES"))
			var fault *xmlrpc.MethodError
			switch {
			case errors.As(err, &fault):
				// "Invalid device" (-2) and the like: hmipserver answered, and the answer is no
				d.remember(key, false)
				continue
			case err != nil:
				continue // no answer: nothing is learnt, the next poll asks again
			}
			has = xmlrpc.Q(v).TryKey("CARRIER_SENSE_LEVEL").IsNotEmpty()
			d.remember(key, has)
		}
		if !has {
			continue
		}
		ri.CarrierSenseSource = "device"
		v, err := callWithin(ctx, caller, timeout, "getValue", xmlrpc.NewString(channel), xmlrpc.NewString("CARRIER_SENSE_LEVEL"))
		if err != nil {
			continue // not reported yet (fault -5 until the module sent it) or no answer
		}
		if cs, ok := number(xmlrpc.Q(v)); ok {
			ri.CarrierSense = percent(cs)
		}
	}
}

// callWithin makes one call and gives up after timeout or when ctx ends.
func callWithin(ctx context.Context, c xmlrpc.Caller, timeout time.Duration, method string, params ...*xmlrpc.Value) (*xmlrpc.Value, error) {
	type res struct {
		v   *xmlrpc.Value
		err error
	}
	ch := make(chan res, 1)
	go func() {
		v, err := c.Call(method, params)
		ch <- res{v, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("no answer within %s", timeout)
	case r := <-ch:
		return r.v, r.err
	}
}

// ChangeKey sets the system security key on the BidCos-RF interface (rfd's changeKey: rfd
// stores it and re-keys every AES-capable device). It is what cp_security.cgi's set_key called
// before crypttool; without a BidCos-RF interface there is nothing to call.
func ChangeKey(ctx context.Context, ifs []Interface, key string) error {
	for _, i := range ifs {
		if i.Name != "BidCos-RF" {
			continue
		}
		done := make(chan error, 1)
		go func() {
			_, err := i.caller.Call("changeKey", xmlrpc.Values{xmlrpc.NewString(key)})
			done <- err
		}()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New("no BidCos-RF interface in InterfacesList.xml")
}

// Init makes the XML-RPC init(url, id) call on one interface process, named as in
// InterfacesList.xml. With an empty id it is the deregistration every client uses: the process
// drops the callback url from its handlers file (task 76, measured on rfd, hmipserver's legacy
// face and the VirtualDevices process, 2026-09-12). The process matches the url exactly, scheme
// included: a binrpc:// registration is not removed by an http:// url with the same host and port.
func Init(ctx context.Context, ifs []Interface, name, url, id string) error {
	for _, i := range ifs {
		if i.Name != name {
			continue
		}
		done := make(chan error, 1)
		go func() {
			_, err := i.caller.Call("init", xmlrpc.Values{xmlrpc.NewString(url), xmlrpc.NewString(id)})
			done <- err
		}()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("no %s interface in InterfacesList.xml", name)
}

// SetLogLevel sends the numeric level (1 debug, 2 info, 4 warning, 5 error) to one interface
// process over its own XML-RPC method, the way the WebUI's dialog does, so rfd and hs485d take it
// without a restart (task 27.8). The name is the InterfacesList.xml one: BidCos-RF for rfd,
// BidCos-Wired for hs485d.
func SetLogLevel(ctx context.Context, ifs []Interface, name string, level int) error {
	for _, i := range ifs {
		if i.Name != name {
			continue
		}
		done := make(chan error, 1)
		go func() {
			_, err := i.caller.Call("logLevel", xmlrpc.Values{xmlrpc.NewInt(level)})
			done <- err
		}()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("no %s interface in InterfacesList.xml", name)
}

// DeviceState is one device's UNREACH as Reachability reads it.
type DeviceState struct {
	// State: "ok", "unreach", or "unknown" when the interface has no value yet - hmipserver
	// answers fault -5 for a device it has not heard from since its start
	State string `json:"state"`
	// AccessPoint: the radio module itself, an HmIP-HAP or a DRAP - they answer over the LAN or
	// the serial line whatever the radio key, so a key check does not count them
	AccessPoint bool `json:"access_point,omitempty"`
}

// accessPoint: the device types that are access points rather than radio devices (task 149).
func accessPoint(typ string) bool {
	t := strings.ToUpper(typ)
	for _, s := range []string{"DRAP", "HAP", "RPI-RF-MOD", "RFUSB", "HMIP-CCU", "HM-MOD"} {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// Reachability lists the devices of one interface with each one's UNREACH (task 149: the check
// after a network key change, where a wrong key shows only as devices going silent). The CCU's
// own receivers are left out.
func Reachability(ctx context.Context, i Interface, timeout time.Duration) (map[string]DeviceState, error) {
	devices, errs := Devices(ctx, []Interface{i}, timeout)
	if e := errs[i.Name]; e != nil {
		return nil, e
	}
	out := map[string]DeviceState{}
	for _, d := range devices {
		if strings.Contains(strings.ToUpper(d.Type), "RCV") {
			continue
		}
		st := DeviceState{State: "ok", AccessPoint: accessPoint(d.Type)}
		v, err := callWithin(ctx, i.caller, timeout, "getValue", xmlrpc.NewString(d.Address+":0"), xmlrpc.NewString("UNREACH"))
		switch {
		case err != nil:
			st.State = "unknown"
		case xmlrpc.Q(v).Bool():
			st.State = "unreach"
		}
		out[d.Address] = st
	}
	return out, nil
}

// serviceFlag is the bit in a parameter description's FLAGS that marks a service message: the
// CCU's own rule for what its service message list shows (task 82; UNREACH, LOWBAT,
// CONFIG_PENDING, UPDATE_PENDING, the ERROR codes, SABOTAGE, FAULT_REPORTING all carry it).
const serviceFlag = 8

// stickyFlag marks a datapoint that keeps its value until someone acknowledges it (STICKY_UNREACH,
// STICKY_SABOTAGE): the interface process resets it on a setValue and sends no event for that
// (occulited B-46, measured on rfd), so only a read finds it acknowledged.
const stickyFlag = 0x10

// MaintenanceValues reads every device's channel 0 of one interface for the service-message
// sweep (task 75): the devices, the service datapoints per device type (from the paramset
// description of channel 0, once per type) and which of them are sticky, and channel 0's VALUES
// per device. Under a second per box measured; a device whose channel 0 does not answer is left
// out, an interface that does not answer is the error. The CCU's own receivers (RCV) are skipped:
// they have no maintenance.
func MaintenanceValues(ctx context.Context, i Interface, timeout time.Duration) (devices []Device, flags, sticky map[string]map[string]bool, values map[string]map[string]any, err error) {
	devices, errs := Devices(ctx, []Interface{i}, timeout)
	if e := errs[i.Name]; e != nil {
		return nil, nil, nil, nil, e
	}
	flags = map[string]map[string]bool{}
	sticky = map[string]map[string]bool{}
	values = map[string]map[string]any{}
	for _, d := range devices {
		if strings.Contains(strings.ToUpper(d.Type), "RCV") {
			continue
		}
		channel := d.Address + ":0"
		if _, ok := flags[d.Type]; !ok {
			v, err := callWithin(ctx, i.caller, timeout, "getParamsetDescription", xmlrpc.NewString(channel), xmlrpc.NewString("VALUES"))
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, nil, nil, ctx.Err()
				}
				continue // no description: the well-known names decide for this type
			}
			f, st := map[string]bool{}, map[string]bool{}
			for name, p := range xmlrpc.Q(v).Map() {
				fl := p.TryKey("FLAGS").Int()
				if fl&serviceFlag != 0 {
					f[name] = true
				}
				if fl&stickyFlag != 0 {
					st[name] = true
				}
			}
			flags[d.Type], sticky[d.Type] = f, st
		}
		v, err := callWithin(ctx, i.caller, timeout, "getParamset", xmlrpc.NewString(channel), xmlrpc.NewString("VALUES"))
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, nil, nil, ctx.Err()
			}
			continue
		}
		vals := map[string]any{}
		for name, q := range xmlrpc.Q(v).Map() {
			vals[name] = q.Any()
		}
		values[channel] = vals
	}
	return devices, flags, sticky, values, nil
}

// InstallMode is the seconds the interface's install mode has left, 0 when it is off (task 149:
// the key-server override lasts for one pairing).
func InstallMode(ctx context.Context, i Interface, timeout time.Duration) (int, error) {
	v, err := callWithin(ctx, i.caller, timeout, "getInstallMode")
	if err != nil {
		return 0, err
	}
	n, _ := number(xmlrpc.Q(v))
	return int(n), nil
}

// Channels lists the channels of one interface that carry a VALUES paramset, for the state
// store's sweep (openccu-lite task 194): every device's channels, the maintenance channel :0
// included; the CCU's own virtual receivers (HM-RCV-50, HmIP-RCV-*, the BidCoS-RF:n and
// BidCoS-Wir:n keys) are left out - nothing reports them - and so are HmIP's wired devices
// (HmIPW-*, the DRAP and what hangs on it): whether hmipserver answers their reads from its cache
// without a bus frame, as it does for HmIP radio devices (task 287), is not measured (occulited
// task 38).
func Channels(ctx context.Context, i Interface, timeout time.Duration) ([]string, error) {
	type res struct {
		d   []*itf.DeviceDescription
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c := itf.DeviceLayerClient{Name: i.Name, Caller: i.caller}
		d, err := c.ListDevices()
		ch <- res{d, err}
	}()
	var r res
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("no answer within %s", timeout)
	case r = <-ch:
	}
	if r.err != nil {
		return nil, r.err
	}
	var out []string
	for _, d := range r.d {
		if d.Parent == "" || strings.Contains(strings.ToUpper(d.ParentType), "RCV") ||
			strings.HasPrefix(d.Address, "BidCoS-RF:") || strings.HasPrefix(d.Address, "BidCoS-Wir:") ||
			strings.HasPrefix(strings.ToUpper(d.ParentType), "HMIPW-") {
			continue
		}
		values := len(d.Paramsets) == 0 // an interface that does not say: try it
		for _, p := range d.Paramsets {
			if p == "VALUES" {
				values = true
			}
		}
		if values {
			out = append(out, d.Address)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Children is the CHILDREN of one device's getDeviceDescription: its channel addresses as the
// interface lists them, the maintenance channel :0 included (task 42: the heating group device's
// channels to name). A device without channels is an empty list; VirtualDevices sends an empty
// value for it instead of an empty array.
func Children(ctx context.Context, i Interface, device string, timeout time.Duration) ([]string, error) {
	v, err := callWithin(ctx, i.caller, timeout, "getDeviceDescription", xmlrpc.NewString(device))
	if err != nil {
		return nil, err
	}
	q := xmlrpc.Q(v)
	d := &itf.DeviceDescription{}
	d.ReadFrom(q)
	if q.Err() != nil {
		return nil, fmt.Errorf("invalid answer to getDeviceDescription: %w", q.Err())
	}
	if d.Address != "" && d.Address != device {
		return nil, fmt.Errorf("getDeviceDescription(%s) answered %s", device, d.Address)
	}
	return d.Children, nil
}

// ChannelValues is getParamset(<channel>, VALUES) of one channel, the values as JSON carries
// them (bool, int, float64, string).
func ChannelValues(ctx context.Context, i Interface, channel string, timeout time.Duration) (map[string]any, error) {
	v, err := callWithin(ctx, i.caller, timeout, "getParamset", xmlrpc.NewString(channel), xmlrpc.NewString("VALUES"))
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for name, q := range xmlrpc.Q(v).Map() {
		out[name] = q.Any()
	}
	return out, nil
}
