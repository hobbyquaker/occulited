package radio

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Role is a module in a role: the HmRF (BidCos-RF) or the HmIP side, as S47InitRFHardware wrote
// it into /var/hm_mode (HM_HMRF_* and HM_HMIP_*).
type Role struct {
	Hardware   string `json:"hardware"`
	Node       string `json:"node,omitempty"` // "" for the HM-CFG-USB-2
	DeviceType string `json:"device_type"`
	Address    string `json:"address"`
	Serial     string `json:"serial"`
	SGTIN      string `json:"sgtin,omitempty"`
	Version    string `json:"version"`
	// Module is the index into Inputs.Detection.Modules.
	Module int `json:"module"`
}

// Daemon is one interface process's plan: whether it runs, on which connection, and why.
type Daemon struct {
	Run    bool   `json:"run"`
	Reason string `json:"reason"`
	// Node is the device the daemon opens (multimacd: the module's raw UART; hmipserver: the raw
	// UART directly, or /dev/mmd_hmip through multimacd).
	Node string `json:"node,omitempty"`
}

// Plan is what runs with which connection - a pure function of Inputs (auto: what upstream's
// chain would decide, the deviations below excepted).
type Plan struct {
	Mode string `json:"mode"`
	// Choices are the user's pins (D-98); both empty is auto, upstream's pick.
	Choices Choices `json:"choices"`
	// MissingHmIP and MissingBidCos name a pinned module that is not there: the process runs
	// without it, never on another module (D-98).
	MissingHmIP   string `json:"missing_hmip,omitempty"`
	MissingBidCos string `json:"missing_bidcos,omitempty"`
	// HmIPPin is the module HmIP-RF is kept on while its choice is Automatic (openccu-lite task
	// 318): the one it last ran on, on this board. MissingHmIP names it when it is not there.
	HmIPPin string `json:"hmip_pin,omitempty"`
	HmRF    *Role  `json:"hmrf,omitempty"`
	// Conflict says why the choices cannot be served as they are, and the connection API refuses
	// them: an HM-MOD-RPI-PCB carrying HmIP needs the multiplexer that serves another module for
	// BidCos-RF (deviation 15), or a chosen HmIP path that the module or the BidCos-RF choice
	// rules out (task 150). Empty when they can.
	Conflict string `json:"conflict,omitempty"`
	HmIP     *Role  `json:"hmip,omitempty"`
	// The active addresses: the box's own from /etc/config/ids and hmip_address.conf, else the
	// modules'. IDsInvalid: the ids file carried no usable address and is moved aside.
	HmRFAddressActive string `json:"hmrf_address_active"`
	HmIPAddressActive string `json:"hmip_address_active"`
	IDsInvalid        bool   `json:"ids_invalid,omitempty"`
	BoardSerial       string `json:"board_serial"`
	BoardSGTIN        string `json:"board_sgtin,omitempty"`

	Multimacd  Daemon `json:"multimacd"`
	RFD        Daemon `json:"rfd"`
	HmIPServer Daemon `json:"hmipserver"`
	HS485D     Daemon `json:"hs485d"`
	Hmlangw    Daemon `json:"hmlangw"`

	// HmIPServerJar is the server hmipserver runs (occulited task 147): JarHMIPServer unless HmIP
	// is set to none, JarHMServer (the virtual devices alone) only then - never because no
	// module was found. HmIPServerHmIP: HMIPServer.jar has its module (the node it opens).
	// Advanced: duty cycle, carrier sense and LAN routing (RPI-RF-MOD, HmIP-RFUSB).
	HmIPServerJar      string `json:"hmipserver_jar"`
	HmIPServerHmIP     bool   `json:"hmipserver_hmip"`
	HmIPServerAdvanced bool   `json:"hmipserver_advanced"`
	HmIPServerBind     string `json:"hmipserver_bind,omitempty"`
	HeapMB             int    `json:"heap_mb"`
	// RFDLocal: rfd.conf's local module section ([Interface 0], CCU2) is active; RFDUSBAdapter:
	// an HM-CFG-USB-2 section is there; RFDLANGateway: a LAN gateway section is there.
	RFDLocal      bool `json:"rfd_local"`
	RFDUSBAdapter bool `json:"rfd_usb_adapter"`
	RFDLANGateway bool `json:"rfd_lan_gateway"`
	// RFDConf is /etc/config/rfd.conf as it will be after the render's edits.
	RFDConf string `json:"-"`
	// Interfaces is InterfacesList.xml as it will be.
	Interfaces []Interface `json:"interfaces"`
	// HBRFLED: an RPI-RF-MOD on an HB-RF adapter whose LED driver is loaded with these pins.
	HBRFLED *HBRFLED `json:"hb_rf_led,omitempty"`
	// StoragePath is the diagram and backup storage (/usr/local/sdcard on a non-SD userfs, or
	// the custom path); "" when the WebUI would ask for a USB stick.
	StoragePath string `json:"storage_path,omitempty"`
	// Notes say why each decision fell as it did, for the journal and the Interfaces page.
	Notes []string `json:"notes"`
	// LogLevels: the levels the daemons start with (/etc/config/syslog).
	LogLevels LogLevels `json:"log_levels"`
}

// HBRFLED is the LED driver's load for an RPI-RF-MOD on an HB-RF-USB/-ETH.
type HBRFLED struct {
	Node  string `json:"node"`
	Red   string `json:"red,omitempty"`
	Green string `json:"green,omitempty"`
	Blue  string `json:"blue,omitempty"`
}

// The servers hmipserver runs (occulited task 147).
const (
	JarHMIPServer = "HMIPServer.jar"
	JarHMServer   = "HMServer.jar"
)

// HmIPOff: HmIP is set to none, hmipserver runs HMServer.jar.
func (p Plan) HmIPOff() bool { return p.HmIPServerJar == JarHMServer }

// NoHmIPModule: hmipserver is to run HMIPServer.jar (HmIP auto or a pinned module) and there is no
// module for it (task 147) - it cannot start then.
func (p Plan) NoHmIPModule() bool {
	return p.Mode != "HM-LGW" && p.HmIPServer.Run && !p.HmIPOff() && p.HmIP == nil
}

// LogLevels are the daemons' log levels from /etc/config/syslog: rfd's and hs485d's numbers,
// multimacd's own - 1 or 2, never rfd's (task 297; held by MultimacdLevel as well) - and
// hmipserver's log4j2 name.
type LogLevels struct {
	RFD       string `json:"rfd"`
	Multimacd string `json:"multimacd"`
	HS485D    string `json:"hs485d"`
	HmIP      string `json:"hmip"`
}

var (
	digitsRe  = regexp.MustCompile(`^[0-9]+$`)
	gatewayRe = regexp.MustCompile(`(?m)^Type = (HMLGW2|Lan Interface)`)
	// deviation 6: any section number counts (upstream's ^\[Interface .\] matched one character)
	sectionRe = regexp.MustCompile(`(?m)^\[Interface [0-9]+\]`)
)

// randomAddress draws S47's random BidCos address for a module answering 0x000000: between
// 0xFF0000 and 0xFFFFFE.
func randomAddress() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := binary.LittleEndian.Uint32(b[:]) % (0xFFFFFE - 0xFF0000)
	return fmt.Sprintf("0x%X", 0xFF0000+n)
}

// MakePlan decides. Every upstream decision is taken here explicitly; the deviations are marked
// "deviation N" and listed in the roadmap item (task 129).
func MakePlan(in Inputs) Plan {
	p := Plan{Mode: in.Mode, Notes: []string{}}
	note := func(f string, a ...any) { p.Notes = append(p.Notes, fmt.Sprintf(f, a...)) }
	p.LogLevels = logLevels(in.Syslog)
	rfdConf := ""
	if in.RFDConfExists {
		rfdConf = in.RFDConf
	}
	ch := ReadChoices(in.HmIPUserConf, rfdConf)
	p.Choices = ch

	// --- the roles (S47's role pick; a pinned process takes its module instead, D-98) ------------
	hmrfRole := func(i int, m Module) *Role {
		if m.USBAdapter {
			return &Role{Hardware: "HM-CFG-USB-2", DeviceType: "USB", Serial: m.Serial, Module: i}
		}
		addr := m.HmRFAddress
		if addr == "0x000000" {
			// the module has no BidCos address of its own: a random one between 0xFF0000 and
			// 0xFFFFFE, as S47 makes it up
			addr = in.RandomAddress
			note("BidCos-RF: %s reports no address of its own, using the random %s", m.Hardware, addr)
		}
		return &Role{Hardware: m.Hardware, Node: m.Node, DeviceType: devTypeOf(m), Address: addr, Serial: m.Serial, Version: m.Version, Module: i}
	}
	hmipRole := func(i int, m Module) *Role {
		return &Role{Hardware: m.Hardware, Node: m.Node, DeviceType: devTypeOf(m), Address: m.HmIPAddress, Serial: m.Serial, SGTIN: m.SGTIN, Version: m.Version, Module: i}
	}
	if ch.BidCos == ChoiceAuto {
		for i, m := range in.Detection.Modules {
			if m.USBAdapter {
				// the adapter takes HmRF before any node is probed
				p.HmRF = hmrfRole(i, m)
				note("BidCos-RF: an HM-CFG-USB-2 (serial %s) is connected", m.Serial)
			}
		}
	}
	for i, m := range in.Detection.Modules {
		if !m.OK() || m.USBAdapter {
			continue
		}
		// HmRF: a module with a BidCos address and a serial, never the Telekom stick; a
		// HM-MOD-RPI-PCB is preferred over an RPI-RF-MOD already chosen
		if ch.BidCos == ChoiceAuto && m.BidCosCapable() {
			if p.HmRF == nil || (m.Hardware == "HM-MOD-RPI-PCB" && p.HmRF.Hardware == "RPI-RF-MOD") {
				p.HmRF = hmrfRole(i, m)
			}
		}
		// HmIP: a module with a real HmIP address and a serial; an RPI-RF-MOD is preferred over a
		// HM-MOD-RPI-PCB already chosen
		if ch.HmIP == ChoiceAuto && m.HmIPCapable() {
			if p.HmIP == nil || (m.Hardware == "RPI-RF-MOD" && p.HmIP.Hardware == "HM-MOD-RPI-PCB") {
				p.HmIP = hmipRole(i, m)
			}
		}
	}
	switch ch.BidCos {
	case ChoiceAuto:
	case BidCosNone:
		note("BidCos-RF: no local radio by choice (LAN gateways only)")
	default:
		for i, m := range in.Detection.Modules {
			if m.matches(ch.BidCos) && m.BidCosCapable() {
				p.HmRF = hmrfRole(i, m)
				note("BidCos-RF: the chosen module %s", ch.BidCos)
				break
			}
		}
		if p.HmRF == nil {
			p.MissingBidCos = ch.BidCos
			note("BidCos-RF: the chosen module %s is missing; rfd runs without a local radio", ch.BidCos)
		}
	}
	switch ch.HmIP {
	case ChoiceAuto:
	case HmIPNone:
		note("HmIP: off by choice; hmipserver runs HMServer.jar (virtual devices only)")
	default:
		for i, m := range in.Detection.Modules {
			if m.matches(ch.HmIP) && m.HmIPCapable() {
				p.HmIP = hmipRole(i, m)
				note("HmIP: the chosen module %s", ch.HmIP)
				break
			}
		}
		if p.HmIP == nil {
			p.MissingHmIP = ch.HmIP
			note("HmIP: the chosen module %s is missing; hmipserver runs HMIPServer.jar without a module", ch.HmIP)
		}
	}
	// openccu-lite task 318 (D-120): on Automatic, HmIP-RF stays on the module it last ran on - its
	// identity is bound to it, and another module would make hmipserver attempt the adapter
	// exchange unasked. Missing, it is not replaced by another: HMIPServer.jar without a module.
	if ch.HmIP == ChoiceAuto && in.HmIPPin.holds(in.Detection.BoardMAC) {
		pin := strings.ToUpper(in.HmIPPin.SGTIN)
		p.HmIPPin, p.HmIP = pin, nil
		for i, m := range in.Detection.Modules {
			if m.SGTIN != "" && strings.EqualFold(m.SGTIN, pin) && m.HmIPCapable() {
				p.HmIP = hmipRole(i, m)
				note("HmIP: kept on module %s, which holds the HmIP network", pin)
				break
			}
		}
		if p.HmIP == nil {
			p.MissingHmIP = pin
			note("HmIP: the module %s, which holds the HmIP network, is missing; hmipserver runs HMIPServer.jar without a module (moving HmIP-RF to another module is a confirmed connection change)", pin)
		}
	}
	if p.HmRF != nil {
		note("BidCos-RF module: %s on %s (%s)", p.HmRF.Hardware, orNone(p.HmRF.Node), p.HmRF.DeviceType)
	} else {
		note("BidCos-RF: no module")
	}
	switch {
	case p.HmIP != nil:
		note("HmIP module: %s on %s (%s)", p.HmIP.Hardware, p.HmIP.Node, p.HmIP.DeviceType)
	case ch.HmIP != HmIPNone:
		note("HmIP: no module")
	}

	// an RPI-RF-MOD on an HB-RF-USB/-ETH: the adapter's LED pins drive the module's LED
	for _, r := range []*Role{p.HmRF, p.HmIP} {
		if r != nil && r.Hardware == "RPI-RF-MOD" && strings.Contains(strings.ToLower(r.DeviceType), "hb-rf-") && p.HBRFLED == nil {
			m := in.Detection.Modules[r.Module]
			p.HBRFLED = &HBRFLED{Node: r.Node}
			if len(m.LEDPins) == 3 {
				p.HBRFLED.Red, p.HBRFLED.Green, p.HBRFLED.Blue = m.LEDPins[0], m.LEDPins[1], m.LEDPins[2]
			}
		}
	}

	// --- the active addresses and the board serial ----------------------------------------------
	hmrfAddr := ""
	if p.HmRF != nil {
		hmrfAddr = p.HmRF.Address
	}
	if in.hasIDs() {
		v := IDsAddress(in.IDs)
		if v == "" || v == "0" || v == "0x000000" {
			p.HmRFAddressActive = hmrfAddr
			p.IDsInvalid = true
			note("BidCos-RF: /etc/config/ids carries no usable address, moved aside; the module's %s is used", hmrfAddr)
		} else {
			p.HmRFAddressActive = v
		}
	} else {
		p.HmRFAddressActive = hmrfAddr
	}
	if ch.HmIP == HmIPNone {
		// HmIP off (task 147): the HmIP half's settings are not written
	} else if in.hasHmIPAddress() {
		v := ""
		for _, l := range strings.Split(in.HmIPAddressConf, "\n") {
			if strings.Contains(strings.ToLower(l), "adapter.1.address") {
				v = strings.Join(strings.Fields(l), "")
				if _, after, ok := strings.Cut(v, "="); ok {
					v = after
				} else {
					v = ""
				}
				break
			}
		}
		// upstream prefixes 0x whatever the file says (an empty value gives "0x")
		p.HmIPAddressActive = "0x" + v
	} else if p.HmIP != nil {
		p.HmIPAddressActive = p.HmIP.Address
	}
	switch {
	case p.HmIP != nil && p.HmIP.Serial != "":
		p.BoardSerial = p.HmIP.Serial
	case p.HmRF != nil && p.HmRF.Serial != "":
		p.BoardSerial = p.HmRF.Serial
	case in.Host == "oci":
		p.BoardSerial = strings.ReplaceAll(in.Hostname, "-openccu", "")
	default:
		// the last nine hex digits of the MAC (upstream's tail -c 10 on an echoed line)
		mac := strings.ReplaceAll(in.Detection.BoardMAC, ":", "")
		if len(mac) > 9 {
			mac = mac[len(mac)-9:]
		}
		p.BoardSerial = mac
		if mac != "" {
			note("board serial from the MAC address: %s", mac)
		}
	}
	if p.HmIP != nil {
		p.BoardSGTIN = p.HmIP.SGTIN
	}

	// --- the daemons ---------------------------------------------------------------------------
	// multimacd (S60): both roles set, and not the HmIP-RFUSB beside an HM-CFG-USB-2 - and only
	// on a node that exists (deviation 9: upstream starts it with an empty device path when the
	// adapter holds HmRF and a header module holds HmIP)
	switch {
	case ch.Explicit():
		// a pinned plan (D-98): rfd reaches a module on a UART only through the multiplexer, so
		// multimacd runs whenever BidCos-RF has one, shared with HmIP or not
		switch {
		case p.HmRF == nil:
			p.Multimacd = Daemon{Reason: "not required: BidCos-RF has no local module"}
		case p.HmRF.Node == "":
			p.Multimacd = Daemon{Reason: "not required: BidCos-RF is on the HM-CFG-USB-2, which has no UART"}
		case p.HmIP != nil && p.HmIP.Node == p.HmRF.Node:
			p.Multimacd = Daemon{Run: true, Node: p.HmRF.Node, Reason: "HmIP and BidCos-RF share the module on " + p.HmRF.Node}
		default:
			p.Multimacd = Daemon{Run: true, Node: p.HmRF.Node, Reason: "BidCos-RF on " + p.HmRF.Node + " through the multiplexer"}
		}
	case p.HmIP == nil || p.HmRF == nil:
		p.Multimacd = Daemon{Reason: "not required: HmIP and BidCos-RF do not share a module"}
	case strings.Contains(p.HmIP.Hardware, "HMIP-RFUSB") && p.HmRF.Hardware == "HM-CFG-USB-2":
		p.Multimacd = Daemon{Reason: "not required: the HmIP-RFUSB beside an HM-CFG-USB-2"}
	case p.HmRF.Node == "":
		p.Multimacd = Daemon{Reason: "not required: BidCos-RF is on the HM-CFG-USB-2, which has no UART (deviation 9)"}
	default:
		p.Multimacd = Daemon{Run: true, Node: p.HmRF.Node, Reason: "HmIP and BidCos-RF share the module on " + p.HmRF.Node}
		if p.HmIP.Node != p.HmRF.Node {
			p.Multimacd.Reason = "BidCos-RF on " + p.HmRF.Node + " through the multiplexer; HmIP uses " + p.HmIP.Node + " directly"
		}
	}
	// deviation 15 (maintainer, 2026-09-18): an HM-MOD-RPI-PCB carries HmIP only through the
	// multiplexer - hmipserver on its UART directly hung after every reset of the module on .170
	// (upstream opens it directly when BidCos-RF is on an HM-CFG-USB-2 or elsewhere). multimacd
	// serves one module: when it already runs for another, the PCB's HmIP cannot be served
	// - the dual-protocol PCB only (a BidCos address of its own): multimacd refuses an HmIP-only
	// coprocessor ("Please install DualCoPro Firmware", an HmIP-only stick), which hmipserver
	// opens directly like the TK
	if pm := in.pcbModule(p.HmIP); pm != nil && pm.HmRFAddress != "" && pm.HmRFAddress != "0x000000" && !pm.SerialFromSGTIN {
		switch {
		case !p.Multimacd.Run:
			p.Multimacd = Daemon{Run: true, Node: p.HmIP.Node, Reason: "HmIP on the HM-MOD-RPI-PCB on " + p.HmIP.Node + " goes through the multiplexer (deviation 15)"}
		case p.Multimacd.Node != p.HmIP.Node:
			p.Conflict = "HmIP on the HM-MOD-RPI-PCB needs the multiplexer, which would serve the BidCos-RF module: put BidCos-RF on the PCB too, on the HM-CFG-USB-2 or on LAN gateways"
			note("HmIP: the HM-MOD-RPI-PCB on %s needs the multiplexer, which serves the BidCos-RF module on %s", p.HmIP.Node, p.Multimacd.Node)
		}
	}
	// task 150: the chosen path of hmipserver to its module, where the hardware and the BidCos-RF
	// choice allow it
	if ch.HmIPPath != ChoiceAuto && p.HmIP != nil && p.Conflict == "" {
		allowed := in.Detection.Modules[p.HmIP.Module].HmIPPaths()
		ok := false
		for _, a := range allowed {
			ok = ok || a == ch.HmIPPath
		}
		shared := p.Multimacd.Run && p.Multimacd.Node == p.HmIP.Node
		switch {
		case !ok:
			p.Conflict = fmt.Sprintf("the %s is reached %s only", p.HmIP.Hardware, strings.Join(allowed, " or "))
		case ch.HmIPPath == PathDirect && shared:
			p.Conflict = "HmIP cannot open the module directly while BidCos-RF uses it through the multiplexer"
		case ch.HmIPPath == PathMultimacd && p.Multimacd.Run && !shared:
			p.Conflict = "the multiplexer already serves the BidCos-RF module on " + p.Multimacd.Node
		case ch.HmIPPath == PathMultimacd && !p.Multimacd.Run:
			p.Multimacd = Daemon{Run: true, Node: p.HmIP.Node, Reason: "HmIP on " + p.HmIP.Node + " through the multiplexer by choice"}
			note("HmIP: through the multiplexer by choice")
		}
	}
	if p.Mode == "HM-LGW" {
		// the LAN-gateway mode: multimacd and hmlangw only
		p.RFD = Daemon{Reason: "LAN-gateway mode"}
		p.HmIPServer = Daemon{Reason: "LAN-gateway mode"}
		p.HS485D = Daemon{Reason: "LAN-gateway mode"}
		if p.HmRF != nil && p.HmRF.Node != "" {
			p.Hmlangw = Daemon{Run: true, Node: "/dev/mmd_bidcos", Reason: "LAN-gateway mode on " + p.HmRF.Node}
		} else {
			p.Hmlangw = Daemon{Reason: "no BidCos-RF hardware found"}
		}
		p.Interfaces = nil
		return p
	}
	p.Hmlangw = Daemon{Reason: "not in LAN-gateway mode"}

	// rfd (S61): rfd.conf edited for the local module and the USB adapter, then any interface
	// section left means rfd runs
	conf, local, usb := EditRFDConf(in.RFDConf, in.RFDConfExists, in.TemplateRFDConf, p.HmRF, p.Multimacd.Run)
	p.RFDConf, p.RFDLocal, p.RFDUSBAdapter = conf, local, usb
	p.RFDLANGateway = gatewayRe.MatchString(conf)
	if sectionRe.MatchString(conf) {
		why := []string{}
		if local {
			why = append(why, "the module through /dev/mmd_bidcos")
		}
		if usb {
			why = append(why, "the HM-CFG-USB-2")
		}
		if p.RFDLANGateway {
			why = append(why, "a LAN gateway")
		}
		p.RFD = Daemon{Run: true, Reason: "BidCos-RF: " + strings.Join(why, ", ")}
		if local {
			p.RFD.Node = "/dev/mmd_bidcos"
		}
	} else {
		switch {
		case ch.BidCos == BidCosNone:
			p.RFD = Daemon{Reason: "off by choice: no local radio, and no LAN gateway is configured"}
		case p.MissingBidCos != "":
			p.RFD = Daemon{Reason: "the chosen module " + p.MissingBidCos + " is missing, and no LAN gateway is configured"}
		default:
			p.RFD = Daemon{Reason: "no BidCos-RF hardware found"}
		}
	}

	// hmipserver (S62): always. HMIPServer.jar unless HmIP is set to none (occulited task 147,
	// deviation 18: upstream runs HMServer.jar whenever no HmIP module is found - here a missing
	// module never changes the server, only the user's none does); with an HmIP module on the
	// module directly unless multimacd multiplexes that very node (deviation 10: upstream's
	// substring test on "raw-uart" put multimacd and hmipserver on the same UART for the ttyAMA0
	// fallback)
	p.HmIPServer = Daemon{Run: true, Reason: "VirtualDevices"}
	p.HmIPServerJar = JarHMIPServer
	if ch.HmIP == HmIPNone {
		p.HmIPServerJar = JarHMServer
	}
	if p.HmIP != nil {
		p.HmIPServerHmIP = true
		// deviation 17 (openccu-lite B-282): an HmIP-RFUSB on the HmIP-only firmware line takes no
		// routing flags - hmipserver logs "Routing was enabled in configuration but cannot be
		// initialized" on firmware 1.8.3 (upstream sets them for every RFUSB)
		p.HmIPServerAdvanced = p.HmIP.Hardware == "RPI-RF-MOD" || (p.HmIP.Hardware == "HMIP-RFUSB" && in.Detection.Modules[p.HmIP.Module].Application != AppHmIPOnly)
		if p.HmIP.Hardware == "HMIP-RFUSB" && !p.HmIPServerAdvanced {
			note("HmIP: the HmIP-RFUSB runs the HmIP-only firmware line (%s); no routing flags (deviation 17)", in.Detection.Modules[p.HmIP.Module].Version)
		}
		if p.Multimacd.Run && p.Multimacd.Node == p.HmIP.Node {
			p.HmIPServer.Node = "/dev/mmd_hmip"
			p.HmIPServer.Reason = "HmIP on " + p.HmIP.Node + " through the multiplexer"
		} else {
			p.HmIPServer.Node = p.HmIP.Node
			p.HmIPServer.Reason = "HmIP on " + p.HmIP.Node + " directly"
		}
	} else if p.HmIPOff() {
		p.HmIPServer.Reason = "HmIP off by choice: HMServer.jar, the virtual devices alone"
	} else {
		p.HmIPServer.Reason = "no HmIP module: HMIPServer.jar without a module"
	}
	p.HmIPServerBind = in.HmIPServerDefaults["HMIP_BIND_ADDRESS"]
	p.HeapMB = heapMB(in.MemTotalKB, in.CgroupMemMax)
	if in.Host == "oci" || in.Host == "lxc" || !in.UserfsOnMMC {
		p.StoragePath = "/usr/local/sdcard"
	}
	if in.CustomStorage != "" {
		p.StoragePath = in.CustomStorage
	}

	// hs485d (S49, S60hs485d): a wired interface section in hs485d.conf
	if in.HS485DConfExists && sectionRe.MatchString(in.HS485DConf) {
		p.HS485D = Daemon{Run: true, Reason: "BidCos-Wired: an interface in hs485d.conf"}
	} else {
		p.HS485D = Daemon{Reason: "disabled: no wired interface configured"}
	}

	// InterfacesList.xml (S49): the template's entries (re-copied, so foreign entries are lost as
	// on OpenCCU, D-97), HmIP-RF without an HmIP module and BidCos-RF without a BidCos module or
	// LAN gateway removed; a BidCos-Wired entry exactly when hs485d runs (deviation 12, D-96:
	// upstream on lite never wrote one, the CCU WebUI's wired page did)
	p.Interfaces = TemplateInterfaces(in.TemplateInterfacesList)
	if p.HmIP == nil {
		p.Interfaces = removeInterface(p.Interfaces, "HmIP-RF")
	}
	if p.HmRF == nil && !(in.RFDConfExists && gatewayRe.MatchString(in.RFDConf)) {
		p.Interfaces = removeInterface(p.Interfaces, "BidCos-RF")
	}
	switch {
	case p.HS485D.Run && !hasInterface(p.Interfaces, "BidCos-Wired"):
		p.Interfaces = append(p.Interfaces, BidCosWired)
	case !p.HS485D.Run:
		p.Interfaces = removeInterface(p.Interfaces, "BidCos-Wired")
	}
	return p
}

// pcbModule is the detected HM-MOD-RPI-PCB behind an HmIP role on a node, or nil.
func (in Inputs) pcbModule(r *Role) *Module {
	if r == nil || r.Hardware != "HM-MOD-RPI-PCB" || r.Node == "" || r.Module < 0 || r.Module >= len(in.Detection.Modules) {
		return nil
	}
	return &in.Detection.Modules[r.Module]
}

// devTypeOf is the role's DEVTYPE: the raw-uart class's device_type, GPIO for the ttyAMA0
// fallback that has none.
func devTypeOf(m Module) string {
	if m.DeviceType == "" {
		return "GPIO"
	}
	return m.DeviceType
}

func orNone(s string) string {
	if s == "" {
		return "no UART"
	}
	return s
}

// logLevels are the scripts' readings of /etc/config/syslog.
func logLevels(syslog map[string]string) LogLevels {
	l := LogLevels{RFD: "5", HS485D: "5", HmIP: "WARN"}
	if v := syslog["LOGLEVEL_RFD"]; digitsRe.MatchString(v) {
		l.RFD = v
	}
	if v := syslog["LOGLEVEL_HS485D"]; digitsRe.MatchString(v) {
		l.HS485D = v
	}
	// multimacd's own level, never rfd's (openccu-lite task 297): 1 debug, anything else - no key,
	// or a 3-7 an older system stored - info. MultimacdLevel stays as the guard.
	l.Multimacd = MultimacdMaxLevel
	if syslog["LOGLEVEL_MULTIMACD"] == "1" {
		l.Multimacd = "1"
	}
	l.Multimacd = MultimacdLevel(l.Multimacd)
	if v := syslog["LOGLEVEL_HMIP"]; v != "" {
		l.HmIP = strings.ToUpper(v)
	}
	return l
}

// heapMB is S62's -Xmx: a quarter of the memory in multiples of 128 MB, at least 128.
func heapMB(memTotalKB, cgroupMax int64) int {
	var heap int
	if cgroupMax > 0 {
		heap = int(float64(cgroupMax)/1024/1024*0.25/128) * 128
	} else {
		heap = int(float64(memTotalKB)/1024*0.25/128) * 128
	}
	if heap <= 128 {
		return 128
	}
	return heap
}

// IDsAddress is the BidCos address an ids file carries, as the first BidCoS-Address line gives it
// (spaces dropped, upstream's reading). rfd writes the address it uses into a file that has none
// in decimal ("BidCoS-Address = 16585268" for 0xFD1234; measured for openccu-lite B-266), the
// radio run and eq3configd in hex; a decimal one is given in the hex form everything else
// compares with.
func IDsAddress(ids string) string {
	v := ""
	for _, l := range strings.Split(ids, "\n") {
		if strings.Contains(strings.ToLower(l), "bidcos-address") {
			v = strings.Join(strings.Fields(l), "")
			if _, after, ok := strings.Cut(v, "="); ok {
				v = after
			} else {
				v = ""
			}
			break
		}
	}
	if n, err := strconv.ParseUint(v, 10, 24); err == nil && n > 0 && !strings.HasPrefix(v, "0") {
		return fmt.Sprintf("0x%06X", n)
	}
	return v
}
