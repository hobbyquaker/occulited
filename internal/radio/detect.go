// Package radio is openccu-lite's radio stack: what the box has (Detect), what runs with which
// connection (Plan), and the files and command lines that follow from it (Render). It replaces
// upstream's S47InitRFHardware, S49hs485d, S60multimacd, S61rfd, S62HMServer, S60hs485d and
// S61hmlangw chain (D-83): the same decisions, taken once, in one place, with timeouts, and
// every deviation from upstream's scripts written down where it is taken.
//
// The package is stdlib only and reads a filesystem root (a box, or a sandbox in the tests): it
// runs as root at boot from the occulited binary (`occulited radio …`), independent of the
// occulited service. The fork's differential harness (scripts/testcases/lite-radio-oracle-test.sh)
// runs upstream's scripts over the hardware matrix and compares this package's output with theirs.
package radio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Module is one radio module as the probe reported it - a raw UART node the kernel offers, or the
// HM-CFG-USB-2 adapter, which has no node (libusb). Nothing here is a role decision.
type Module struct {
	// Name is the node's name (raw-uart1, ttyAMA0); Node its path (/dev/raw-uart1). Both are
	// empty for the USB adapter.
	Name string `json:"name,omitempty"`
	Node string `json:"node,omitempty"`
	// DeviceType is the raw-uart class's device_type (GPIO@fe201000.serial, eQ-3 HmIP-RFUSB@usb-…,
	// HB-RF-USB-2@usb-…, HB-RF-ETH@<address>); "USB" for the adapter, "" for the ttyAMA0 fallback.
	DeviceType string `json:"device_type,omitempty"`
	// GPIO: the node is the GPIO header's UART, which the Pi images create whether or not a module
	// is fitted (task 138).
	GPIO bool `json:"gpio,omitempty"`
	// USBAdapter: the HM-CFG-USB-2.
	USBAdapter bool `json:"usb_adapter,omitempty"`
	// The probe's six fields, as detect_radio_module prints them (the hardware upper-cased).
	Hardware    string `json:"hardware,omitempty"`
	Serial      string `json:"serial,omitempty"`
	SGTIN       string `json:"sgtin,omitempty"`
	HmRFAddress string `json:"hmrf_address,omitempty"`
	HmIPAddress string `json:"hmip_address,omitempty"`
	Version     string `json:"version,omitempty"`
	// Application is the coprocessor's firmware line, as detect_radio_module tells the lines apart
	// (openccu-lite B-282): DualCoPro_App - BidCos-RF and HmIP (an RPI-RF-MOD, an HmIP-RFUSB with
	// firmware 4.x); HMIP_TRX_App - HmIP only (an HmIP-RFUSB below firmware 4, e.g. 1.8.3, for which
	// the tool reports no BidCos address); Co_CPU_App - the HM-MOD-RPI-PCB's legacy coprocessor.
	// The tool prints the name of no line, so it is derived from the hardware and the version by
	// the tool's own rule; empty where the line is not known (the Telekom stick, a module without
	// a serial).
	Application string `json:"application,omitempty"`
	// Probe is ok, none (nothing answered), timeout (the limit hit) or error (the probe failed
	// otherwise); Detail carries the probe's message when it is not ok.
	Probe  string `json:"probe"`
	Detail string `json:"detail,omitempty"`
	// LEDPins are the HB-RF adapter's LED GPIO pins (red, green, blue) when the node has them.
	LEDPins []string `json:"led_pins,omitempty"`
	// SerialFromSGTIN: the module reported no serial; Serial is the SGTIN's tail
	// and the module is HmIP-only (deviation 16).
	SerialFromSGTIN bool `json:"serial_from_sgtin,omitempty"`
	// FactoryReset: the module was factory-reset because /usr/local/.doCoproFactoryReset was there.
	FactoryReset bool `json:"factory_reset,omitempty"`
}

// OK: the probe answered.
func (m Module) OK() bool { return m.Probe == "ok" }

// Header: the node is the board's own UART for a module on the GPIO header - the raw-uart node the
// Pi images create whether or not a module is fitted (task 138), or the ttyAMA0 fallback.
func (m Module) Header() bool {
	return m.GPIO || (m.Name == "ttyAMA0" && m.DeviceType == "" && !m.USBAdapter)
}

// EmptyHeader: the header's UART, and nothing answered on it - the probe hit its limit (D-92: an
// empty header keeps detect_radio_module waiting) or the tool found no module. On the UART that is
// what a header without a module looks like, so it is no module (openccu-lite B-300): the
// Interfaces page shows no card for it, the detection keeps it for the journal. A module that
// answers wrongly is "error" and stays one; a USB or HB-RF node exists only with its device.
func (m Module) EmptyHeader() bool {
	return m.Header() && (m.Probe == "timeout" || m.Probe == "none")
}

// Detection is what the box has.
type Detection struct {
	Modules []Module `json:"modules"`
	// HBRFETH is the address of /etc/config/hb_rf_eth when one is configured, and whether the
	// kernel module took it.
	HBRFETH          string `json:"hb_rf_eth,omitempty"`
	HBRFETHConnected bool   `json:"hb_rf_eth_connected,omitempty"`
	// HBRFETHFresh: this rescan connected a board afresh that had been connected before, at the
	// watch's request (B-218) - after a power cycle of the board, whose module was reset; not kept
	HBRFETHFresh bool `json:"-"`
	// TTYFallback: no raw-uart node existed and the board's own serial port stood in (rpi only).
	TTYFallback bool `json:"tty_fallback,omitempty"`
	// SecondPass: nothing was found in the first pass and the nodes were probed again (D-92).
	SecondPass bool `json:"second_pass,omitempty"`
	// Log is what happened, one line per step, for the journal.
	Log      []string      `json:"log"`
	Duration time.Duration `json:"duration"`
	// BoardMAC is the fallback board serial's source: eth0's MAC, or the first physical
	// interface's.
	BoardMAC string `json:"board_mac,omitempty"`
}

// HasModule: a module that answered carries the identity (its serial or its SGTIN, case ignored).
func (d Detection) HasModule(id string) bool {
	for _, m := range d.Modules {
		if m.OK() && m.matches(id) {
			return true
		}
	}
	return false
}

// Found: the scan found a module worth a role, or the adapter - S47's condition for skipping the
// second pass (a module with a serial and a BidCos address or a real HmIP address).
func (d Detection) Found() bool {
	for _, m := range d.Modules {
		if m.USBAdapter {
			return true
		}
		if !m.OK() || m.Serial == "" {
			continue
		}
		if m.HmRFAddress != "" || (m.HmIPAddress != "" && m.HmIPAddress != "0x000000") {
			return true
		}
	}
	return false
}

// Runner executes one external command with a context; the default is exec. Never a shell line.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs the command for real (stdout and stderr together).
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Detector runs the detection against a root.
type Detector struct {
	// Root is the filesystem root ("/" on a box; a sandbox in a test).
	Root string
	// Host is HM_HOST (rpi3, rpi4, ova-…), for the ttyAMA0 fallback.
	Host string
	// Run executes the probes and the module tools; ExecRunner when nil.
	Run Runner
	// GPIOLimit is the first pass's limit on a GPIO node's probe (D-92: an empty header UART keeps
	// detect_radio_module waiting about 18 s, a fitted module answers within about 3 s); 0 means
	// no limit and no second pass, as upstream without RF_GPIO_PROBE_TIMEOUT.
	GPIOLimit time.Duration
	// ProbeLimit bounds every other probe (upstream has none): a probe that hangs must not hang
	// the boot. Default 45 s.
	ProbeLimit time.Duration
	// ResetWait is the pause after a module reset (upstream: a fixed 2 s). Default 2 s.
	ResetWait time.Duration
	// RouteWait is how long a configured HB-RF-ETH's route is waited for. Default 60 s.
	RouteWait time.Duration
	// Sleep pauses; time.Sleep when nil (a test substitutes a counter).
	Sleep func(time.Duration)
	// Now is the clock for Duration; time.Now when nil.
	Now func() time.Time
	// Diagrams is occulited.json's hmipserver.diagrams (occulited task 33): hmipserver's diagram
	// data is carried between its tmpfs and the stick at start and stop. Off by default - openccu-lite
	// has no WebUI to configure a diagram - and then a migrated system's diagram data is removed once.
	Diagrams bool
}

func (d Detector) run() Runner {
	if d.Run != nil {
		return d.Run
	}
	return ExecRunner
}

func (d Detector) sleep(t time.Duration) {
	if d.Sleep != nil {
		d.Sleep(t)
		return
	}
	time.Sleep(t)
}

func (d Detector) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Detector) probeLimit() time.Duration {
	if d.ProbeLimit > 0 {
		return d.ProbeLimit
	}
	return 45 * time.Second
}

func (d Detector) resetWait() time.Duration {
	if d.ResetWait > 0 {
		return d.ResetWait
	}
	return 2 * time.Second
}

func (d Detector) routeWait() time.Duration {
	if d.RouteWait > 0 {
		return d.RouteWait
	}
	return 60 * time.Second
}

// path joins a box path onto the root.
func (d Detector) path(p string) string {
	if d.Root == "" || d.Root == "/" {
		return p
	}
	return filepath.Join(d.Root, p)
}

// tool finds a program: under the root first (a sandbox's stand-in), else by name on PATH.
func (d Detector) tool(p string) string {
	if d.Root != "" && d.Root != "/" {
		if st, err := os.Stat(filepath.Join(d.Root, p)); err == nil && !st.IsDir() {
			return filepath.Join(d.Root, p)
		}
		return filepath.Base(p)
	}
	return p
}

// FactoryResetMarker is upstream's: the module is factory-reset once at the next boot.
const FactoryResetMarker = "/usr/local/.doCoproFactoryReset"

var (
	usbSerialRe = regexp.MustCompile(`^[A-Z]{3}[0-9]{7}`)
	ipRe        = regexp.MustCompile(`^[0-9.]+$|:`)
)

// Detect enumerates the radio hardware and probes it, as S47InitRFHardware did: the HB-RF-ETH
// first, the raw UART nodes (or the board's own port), the USB adapter, a reset, then the probes -
// GPIO nodes with the short limit, and a second pass when nothing was found (D-92).
func (d Detector) Detect(ctx context.Context) Detection {
	start := d.now()
	var det Detection
	logf := func(f string, a ...any) { det.Log = append(det.Log, fmt.Sprintf(f, a...)) }

	d.hbRFETH(ctx, &det, logf)
	d.enumerate(ctx, &det, logf)

	// the reset, and the pause upstream gives the modules after it
	d.reset(det.Modules)
	if n := d.nodeCount(det.Modules); n > 0 {
		d.sleep(d.resetWait())
	}

	// pass one
	var timedOut, retry []int
	for i := range det.Modules {
		m := &det.Modules[i]
		if m.USBAdapter {
			continue
		}
		limit := d.probeLimit()
		if d.GPIOLimit > 0 && m.GPIO {
			limit = d.GPIOLimit
		} else if d.GPIOLimit > 0 && m.DeviceType != "" {
			retry = append(retry, i)
		}
		d.probe(ctx, m, limit, logf)
		if m.Probe == "timeout" && d.GPIOLimit > 0 && m.GPIO {
			timedOut = append(timedOut, i)
		}
	}
	// pass two: nothing found at all - the other nodes first (a stick probed right after a cut
	// probe can answer wrongly once), then the cut GPIO nodes without the limit, each only while
	// nothing was found
	if d.GPIOLimit > 0 && !det.Found() && (len(retry) > 0 || len(timedOut) > 0) {
		det.SecondPass = true
		again := append(append([]int{}, retry...), timedOut...)
		var mods []Module
		for _, i := range again {
			mods = append(mods, det.Modules[i])
		}
		d.reset(mods)
		d.sleep(d.resetWait())
		logf("no module found in the first pass: probing again")
		for _, i := range again {
			m := &det.Modules[i]
			if m.GPIO && det.Found() {
				continue
			}
			d.probe(ctx, m, d.probeLimit(), logf)
		}
	}
	d.factoryReset(ctx, &det, logf)
	det.BoardMAC = d.boardMAC()
	det.Duration = d.now().Sub(start)
	return det
}

// HBRFETHFile is where the HB-RF-ETH's address is configured (OpenCCU's file, its first line).
const HBRFETHFile = "/etc/config/hb_rf_eth"

// HBRFETHConnectedFile is the kernel module's connection state: 1 while the board is connected.
const HBRFETHConnectedFile = "/sys/class/hb-rf-eth/hb-rf-eth/is_connected"

// HBRFETHAddress is the configured address under root, "" when none is.
func HBRFETHAddress(root string) string {
	b := readFile(Detector{Root: root}.path(HBRFETHFile))
	if b == "" {
		return ""
	}
	return strings.Join(strings.Fields(strings.SplitN(b, "\n", 2)[0]), "")
}

// HBRFETHConnected reads the module's state: connected, and whether the module is loaded at all.
func HBRFETHConnected(root string) (connected, loaded bool) {
	b, err := os.ReadFile(Detector{Root: root}.path(HBRFETHConnectedFile))
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(string(b)) == "1", true
}

// hbRFETH loads the HB-RF-ETH module for a configured address, waits for the route and connects.
func (d Detector) hbRFETH(ctx context.Context, det *Detection, logf func(string, ...any)) {
	addr := HBRFETHAddress(d.Root)
	if addr == "" {
		return
	}
	d.hbRFETHConnect(ctx, det, addr, d.routeWait(), 30, logf)
}

// hbRFETHConnect loads the module, waits at most routeWait for a route to the board and tries
// the connect tries times, a second apart.
func (d Detector) hbRFETHConnect(ctx context.Context, det *Detection, addr string, routeWait time.Duration, tries int, logf func(string, ...any)) {
	det.HBRFETH = addr
	param := d.path("/sys/module/hb_rf_eth/parameters/connect")
	if !exists(param) {
		if !d.moduleLoaded("hb_rf_eth") {
			_, _ = d.run()(ctx, "modprobe", "-q", "hb_rf_eth")
		}
		for i := 0; i < 30 && !exists(param); i++ {
			d.sleep(time.Second)
		}
	}
	// the HB-RF-ETH is reached over the network: wait for a route to it (an address), or for a
	// default route (a name), at most routeWait
	polls := int(routeWait / (200 * time.Millisecond))
	for i := 0; i < polls; i++ {
		if ipRe.MatchString(addr) {
			if _, err := d.run()(ctx, "ip", "route", "get", addr); err == nil {
				break
			}
		} else if out, err := d.run()(ctx, "ip", "route", "show", "default"); err == nil && len(bytes.TrimSpace(out)) > 0 {
			break
		}
		d.sleep(200 * time.Millisecond)
	}
	for i := 0; i < tries; i++ {
		if i > 0 {
			d.sleep(time.Second)
		}
		target := param
		if class := d.path("/sys/class/hb-rf-eth/hb-rf-eth/connect"); exists(class) {
			target = class
		}
		if os.WriteFile(target, []byte(addr), 0) == nil {
			det.HBRFETHConnected = true
			logf("HB-RF-ETH %s connected", addr)
			return
		}
	}
	det.HBRFETHConnected = false
	logf("HB-RF-ETH %s: the kernel module did not take the address", addr)
}

// hbRFETHRelease disconnects the board and unloads the module, as the stop does.
func (d Detector) hbRFETHRelease(ctx context.Context, logf func(string, ...any)) {
	if p := d.path("/sys/module/hb_rf_eth/parameters/connect"); exists(p) {
		_ = os.WriteFile(p, []byte("-"), 0)
		_, _ = d.run()(ctx, d.tool("/sbin/rmmod"), "hb-rf-eth")
		logf("HB-RF-ETH disconnected")
	}
}

// hbRFETHRescan is a hotplug's HB-RF-ETH step (task 218): a configured board that is not
// connected - it was unreachable at boot, or its address was set since - is tried again (no route
// wait, three tries), one whose address changed is let go first, and one no longer configured is
// let go. The radio module then appears or goes as a raw-uart node, which the rescan lists.
func (d Detector) hbRFETHRescan(ctx context.Context, prev Detection, det *Detection, logf func(string, ...any)) {
	addr := HBRFETHAddress(d.Root)
	connected, _ := HBRFETHConnected(d.Root)
	forced := d.hbRFETHForced(addr) // read and taken away whatever the case below
	switch {
	case addr == "" && prev.HBRFETH != "":
		d.hbRFETHRelease(ctx, logf)
		det.HBRFETH, det.HBRFETHConnected = "", false
	case addr != "" && addr == prev.HBRFETH && prev.HBRFETHConnected && !connected && d.hbRFETHReconnecting(addr) && !forced:
		// openccu-lite B-218: the board was connected and its link dropped; the kernel's
		// autoreconnect is on it. Writing the address again would restart that as a first connect
		// and the node would be taken for gone: the board is left to the kernel, its node kept.
		logf("HB-RF-ETH %s: the link is lost; the kernel reconnects it", addr)
		det.HBRFETH, det.HBRFETHConnected = addr, false
	case addr != "" && (addr != prev.HBRFETH || !connected):
		if prev.HBRFETH != "" && addr != prev.HBRFETH {
			d.hbRFETHRelease(ctx, logf)
		}
		d.hbRFETHConnect(ctx, det, addr, 0, 3, logf)
		det.HBRFETHFresh = forced && det.HBRFETHConnected && addr == prev.HBRFETH && prev.HBRFETHConnected
		if det.HBRFETHConnected {
			d.sleep(2 * time.Second) // the module's raw-uart node appears after the connect
		}
	default:
		det.HBRFETH, det.HBRFETHConnected = addr, connected && addr != ""
	}
}

// hbRFETHForced says whether the watch asked for a fresh connect of the board at addr (the kernel's
// reconnect did not take it back within its grace), and takes the note away.
func (d Detector) hbRFETHForced(addr string) bool {
	p := d.path(HBRFETHReconnectFile)
	b := strings.TrimSpace(readFile(p))
	if b == "" {
		return false
	}
	_ = os.Remove(p)
	return addr != "" && b == addr
}

// hbRFETHReconnecting says whether the kernel is reconnecting the board at addr: autoreconnect on,
// and the board's raw-uart node still there, for that address.
func (d Detector) hbRFETHReconnecting(addr string) bool {
	if strings.TrimSpace(readFile(d.path("/sys/module/hb_rf_eth/parameters/autoreconnect"))) != "1" {
		return false
	}
	return d.hbRFETHNode(addr) != ""
}

// hbRFETHNode is the raw-uart node the HB-RF-ETH at addr drives, "" when there is none.
func (d Detector) hbRFETHNode(addr string) string {
	for _, name := range d.rawUARTNames() {
		if strings.TrimSpace(readFile(d.path("/sys/class/raw-uart/"+name+"/device_type"))) == "HB-RF-ETH@"+addr {
			return name
		}
	}
	return ""
}

func (d Detector) moduleLoaded(name string) bool {
	for _, l := range strings.Split(readFile(d.path("/proc/modules")), "\n") {
		if strings.HasPrefix(l, name+" ") {
			return true
		}
	}
	return false
}

func (d Detector) rawUARTNames() []string {
	entries, err := os.ReadDir(d.path("/sys/class/raw-uart"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func (d Detector) nodeCount(mods []Module) int {
	n := 0
	for _, m := range mods {
		if m.Node != "" {
			n++
		}
	}
	return n
}

// enumerate lists the nodes and the adapter without touching them: the raw UART nodes (unless a
// "hmul" USB device is there), the board's own port as the fallback, the HM-CFG-USB-2.
func (d Detector) enumerate(ctx context.Context, det *Detection, logf func(string, ...any)) {
	// the raw UART nodes, unless a "hmul" USB device is there (upstream's exclusion, kept)
	if !d.usbProductContains("hmul") {
		for _, name := range d.rawUARTNames() {
			if !exists(d.path("/dev/" + name)) {
				continue
			}
			// a USB stick pulled while a daemon held its node: the driver keeps the node until it
			// is closed, with connection_state 0 (the lab box .170, an HB-RF-USB swapped for a TK)
			if strings.TrimSpace(readFile(d.path("/sys/class/raw-uart/"+name+"/connection_state"))) == "0" {
				// B-218: the HB-RF-ETH's node while the kernel reconnects the board is not a device
				// that is gone - it comes back on its own, and a re-plan without it tore the
				// daemons down for a drop of seconds
				if addr := HBRFETHAddress(d.Root); addr == "" || d.hbRFETHNode(addr) != name || !d.hbRFETHReconnecting(addr) {
					logf("/dev/%s: the device behind it is gone (connection_state 0)", name)
					continue
				}
				logf("/dev/%s: the HB-RF-ETH's link is lost; the node is kept while the kernel reconnects it", name)
			}
			m := Module{Name: name, Node: "/dev/" + name, DeviceType: strings.TrimSpace(readFile(d.path("/sys/class/raw-uart/" + name + "/device_type")))}
			m.GPIO = strings.HasPrefix(m.DeviceType, "GPIO@")
			for _, c := range []string{"red", "green", "blue"} {
				if pin := strings.TrimSpace(readFile(d.path("/sys/class/raw-uart/" + name + "/" + c + "_gpio_pin"))); pin != "" {
					m.LEDPins = append(m.LEDPins, pin)
				}
			}
			// a node without an HB-RF adapter shows the pin files with 0 (the HmIP-RFUSB on .119)
			if len(m.LEDPins) != 3 || (m.LEDPins[0] == "0" && m.LEDPins[1] == "0" && m.LEDPins[2] == "0") {
				m.LEDPins = nil
			}
			det.Modules = append(det.Modules, m)
		}
	}
	if len(det.Modules) == 0 && strings.HasPrefix(d.Host, "rpi") && exists(d.path("/dev/ttyAMA0")) {
		det.TTYFallback = true
		det.Modules = append(det.Modules, Module{Name: "ttyAMA0", Node: "/dev/ttyAMA0"})
		_, _ = d.run()(ctx, d.tool("/bin/setserial"), d.path("/dev/ttyAMA0"), "low_latency")
		logf("no raw UART node: the board's own serial port /dev/ttyAMA0 stands in")
	}
	// the HM-CFG-USB-2 (1b1f:c00f) opens through libusb and has no node
	if d.usbID("1b1f", "c00f") {
		serial := ""
		for _, s := range d.usbSerials() {
			if usbSerialRe.MatchString(s) {
				serial = s
				break
			}
		}
		det.Modules = append(det.Modules, Module{USBAdapter: true, DeviceType: "USB", Hardware: "HM-CFG-USB-2", Serial: serial, Probe: "ok"})
		logf("an HM-CFG-USB-2 is connected (serial %s)", serial)
	}
}

// reset writes 1 to each node's reset_radio_module where the class offers it.
func (d Detector) reset(mods []Module) {
	for _, m := range mods {
		if m.Name == "" {
			continue
		}
		p := d.path("/sys/class/raw-uart/" + m.Name + "/reset_radio_module")
		if exists(p) {
			_ = os.WriteFile(p, []byte("1"), 0)
		}
	}
}

// probe runs detect_radio_module on the node under the limit and fills the module in.
func (d Detector) probe(ctx context.Context, m *Module, limit time.Duration, logf func(string, ...any)) {
	pctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	start := d.now()
	out, err := d.run()(pctx, d.tool("/bin/detect_radio_module"), d.path(m.Node))
	took := d.now().Sub(start).Round(10 * time.Millisecond)
	m.Hardware, m.Serial, m.SGTIN, m.HmRFAddress, m.HmIPAddress, m.Version, m.Application = "", "", "", "", "", "", ""
	text := strings.TrimSpace(string(out))
	switch {
	case pctx.Err() != nil && errors.Is(pctx.Err(), context.DeadlineExceeded):
		m.Probe, m.Detail = "timeout", fmt.Sprintf("no answer within %s", limit)
		logf("%s: no answer within %s (probed %s)", m.Node, limit, took)
		return
	case err != nil:
		m.Probe, m.Detail = "none", text
		logf("%s: no module (%s, %s)", m.Node, firstLine(text), took)
		return
	}
	// the fields as S47 cuts them (cut -d' '): an empty field stays empty - a stick that answers
	// without a serial, "HM-MOD-RPI-PCB  <SGTIN> 0x000000 <HmIP address> <version>"
	f := strings.Split(strings.TrimSpace(firstLine(text)), " ")
	if len(f) < 6 || f[0] == "" {
		m.Probe, m.Detail = "error", "unexpected answer: "+text
		logf("%s: unexpected answer %q", m.Node, text)
		return
	}
	m.Probe, m.Detail = "ok", ""
	m.Hardware = strings.ToUpper(f[0])
	m.Serial, m.SGTIN, m.HmRFAddress, m.HmIPAddress, m.Version = f[1], f[2], f[3], f[4], f[5]
	// deviation 16 (maintainer, 2026-09-18): a module without a serial takes the SGTIN's last ten
	// characters, the serial every other module reports as that tail (the RPI-RF-MOD's
	// ...0000000A03, the TK's ...0000000A09); upstream uses such a module for nothing. It is an
	// HmIP-only stick, like the TK
	if m.Serial == "" && len(m.SGTIN) >= 10 {
		m.Serial, m.SerialFromSGTIN = m.SGTIN[len(m.SGTIN)-10:], true
		logf("%s: no serial reported; %s from the SGTIN (an HmIP-only stick)", m.Node, m.Serial)
	}
	m.Application = applicationOf(m.Hardware, m.Version, m.SGTIN, m.SerialFromSGTIN)
	if m.Application == AppHmIPOnly {
		logf("%s: %s serial %s firmware %s, %s: HmIP only (%s)", m.Node, m.Hardware, m.Serial, m.Version, m.Application, took)
		return
	}
	logf("%s: %s serial %s firmware %s (%s)", m.Node, m.Hardware, m.Serial, m.Version, took)
}

// The coprocessor firmware lines (Module.Application).
const (
	AppDualCoPro = "DualCoPro_App"
	AppHmIPOnly  = "HMIP_TRX_App"
	AppLegacy    = "Co_CPU_App"
)

// applicationOf derives the firmware line from the probe's fields, by detect_radio_module's own
// rule: an HmIP-RFUSB below firmware 4 runs the HmIP-only line and reports no BidCos address, one
// at 4.x the dual line; an RPI-RF-MOD is always dual; an HM-MOD-RPI-PCB that reports no SGTIN is
// the legacy coprocessor. Unknown (empty) for the Telekom stick and a module without a serial.
func applicationOf(hardware, version, sgtin string, serialFromSGTIN bool) string {
	switch hardware {
	case "HMIP-RFUSB":
		if major, _, ok := strings.Cut(version, "."); ok {
			if n, err := strconv.Atoi(major); err == nil && n < 4 {
				return AppHmIPOnly
			}
		}
		return AppDualCoPro
	case "RPI-RF-MOD":
		return AppDualCoPro
	case "HM-MOD-RPI-PCB":
		if !serialFromSGTIN && (sgtin == "" || sgtin == "n/a" || sgtin == "-") {
			return AppLegacy
		}
	}
	return ""
}

// factoryReset resets the HmIP modules once when the marker is there, as S47 did, then removes
// the marker.
func (d Detector) factoryReset(ctx context.Context, det *Detection, logf func(string, ...any)) {
	marker := d.path(FactoryResetMarker)
	if !exists(marker) {
		return
	}
	done := false
	for i := range det.Modules {
		m := &det.Modules[i]
		if !m.OK() || m.Node == "" {
			continue
		}
		switch m.Hardware {
		case "RPI-RF-MOD", "HMIP-RFUSB", "HMIP-RFUSB-TK":
			jctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, _ = d.run()(jctx, d.tool("/opt/java/bin/java"), "-Dos.arch="+d.arch(ctx), "-Dgnu.io.rxtx.SerialPorts="+d.path(m.Node), "-jar", d.path("/opt/HmIP/hmip-copro-update.jar"), "-p", d.path(m.Node), "-r")
			cancel()
			d.sleep(2 * time.Second)
			d.probe(ctx, m, d.probeLimit(), logf)
			m.FactoryReset = true
			done = true
			logf("%s: factory reset performed", m.Node)
		case "HM-MOD-RPI-PCB":
			m.FactoryReset = true
			done = true
		}
	}
	if done {
		_ = os.Remove(marker)
	}
}

func (d Detector) arch(ctx context.Context) string {
	out, err := d.run()(ctx, "uname", "-m")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// usbProductContains: any /sys/bus/usb/devices/*/product carries the word (case-insensitive).
func (d Detector) usbProductContains(word string) bool {
	for _, p := range d.usbFiles("product") {
		if strings.Contains(strings.ToLower(p), strings.ToLower(word)) {
			return true
		}
	}
	return false
}

// usbID: a USB device with the vendor and product id is present.
func (d Detector) usbID(vendor, product string) bool {
	entries, err := os.ReadDir(d.path("/sys/bus/usb/devices"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		dir := d.path("/sys/bus/usb/devices/" + e.Name())
		if strings.TrimSpace(readFile(dir+"/idVendor")) == vendor && strings.TrimSpace(readFile(dir+"/idProduct")) == product {
			return true
		}
	}
	return false
}

func (d Detector) usbSerials() []string { return d.usbFiles("serial") }

func (d Detector) usbFiles(name string) []string {
	entries, err := os.ReadDir(d.path("/sys/bus/usb/devices"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if v := strings.TrimSpace(readFile(d.path("/sys/bus/usb/devices/" + e.Name() + "/" + name))); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// boardMAC is the fallback board serial's source: eth0's MAC, else the first interface with a
// hardware device, type 1 and a non-zero address (S47's board_mac).
func (d Detector) boardMAC() string {
	net := d.path("/sys/class/net")
	entries, err := os.ReadDir(net)
	if err != nil {
		return ""
	}
	names := []string{"eth0"}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, n := range names {
		dir := filepath.Join(net, n)
		if !exists(dir + "/address") {
			continue
		}
		if n != "eth0" && !exists(dir+"/device") {
			continue
		}
		if strings.TrimSpace(readFile(dir+"/type")) != "1" {
			continue
		}
		mac := strings.TrimSpace(readFile(dir + "/address"))
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		return mac
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
