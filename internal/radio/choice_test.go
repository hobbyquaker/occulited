package radio

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const lgwSection = "\n[Interface 1]\nType = HMLGW2\nName = Garage\nSerial Number = KEQ0987654\nEncryption Key = k\n"

func TestReadAndSetChoices(t *testing.T) {
	if c := ReadChoices("", rfdTemplate); c.Explicit() {
		t.Fatalf("nothing written is auto: %+v", c)
	}
	hu := SetHmIPChoice("Legacy.VirtualRemoteControl.Enabled=false\n", "0000000A01")
	if hu != "Legacy.VirtualRemoteControl.Enabled=false\noccu"+"lite.hmip.adapter=0000000A01\n" {
		t.Fatalf("hmip_user.conf keeps the user's key and adds lite's:\n%q", hu)
	}
	rc := SetBidCosChoice(rfdTemplate, BidCosNone)
	if !strings.HasPrefix(rc, "# occulite.bidcos.module=none\n# TCP Port") {
		t.Fatalf("the marker is the first line:\n%s", rc)
	}
	c := ReadChoices(hu, rc)
	if c.HmIP != "0000000A01" || c.BidCos != BidCosNone || !c.Explicit() {
		t.Fatalf("read back: %+v", c)
	}
	// changed, then back to auto: the files are as before
	rc = SetBidCosChoice(rc, "0000000A03")
	if strings.Count(rc, "occulite.bidcos.module") != 1 || ReadChoices("", rc).BidCos != "0000000A03" {
		t.Fatalf("one marker line:\n%s", rc)
	}
	if SetBidCosChoice(rc, ChoiceAuto) != rfdTemplate {
		t.Fatalf("auto removes the marker:\n%s", SetBidCosChoice(rc, ChoiceAuto))
	}
	if SetHmIPChoice(hu, ChoiceAuto) != "Legacy.VirtualRemoteControl.Enabled=false\n" {
		t.Fatalf("auto removes the key: %q", SetHmIPChoice(hu, ChoiceAuto))
	}
	if SetHmIPChoice("", ChoiceAuto) != "" || ReadChoices("occulite.hmip.adapter=auto\n", "").HmIP != ChoiceAuto {
		t.Fatal("auto spelled out is auto")
	}
	for id, ok := range map[string]bool{"0000000A01": true, "3014F711A000040000000A01": true, "": false, "a b": false, "../x": false} {
		if ValidIdentity(id) != ok {
			t.Errorf("ValidIdentity(%q) != %v", id, ok)
		}
	}
}

func TestChoiceOptions(t *testing.T) {
	tk := rfusb("/dev/raw-uart2")
	tk.Hardware, tk.Serial = "HMIP-RFUSB-TK", "0001TK0001"
	adapter := Module{USBAdapter: true, DeviceType: "USB", Hardware: "HM-CFG-USB-2", Serial: "KEQ0123456", Probe: "ok"}
	empty := Module{Name: "raw-uart", Node: "/dev/raw-uart", GPIO: true, Probe: "none"}
	o := ChoiceOptions(Detection{Modules: []Module{empty, rpiRFMod("/dev/raw-uart3"), rfusb("/dev/raw-uart1"), tk, adapter}})
	ids := func(list []Option) string {
		var s []string
		for _, x := range list {
			s = append(s, x.Hardware+":"+x.ID)
		}
		return strings.Join(s, ",")
	}
	if ids(o.HmIP) != "RPI-RF-MOD:0000000A03,HMIP-RFUSB:0000000A01,HMIP-RFUSB-TK:0001TK0001" {
		t.Fatalf("hmip options: %s", ids(o.HmIP))
	}
	if ids(o.BidCos) != "RPI-RF-MOD:0000000A03,HMIP-RFUSB:0000000A01,HM-CFG-USB-2:KEQ0123456" {
		t.Fatalf("bidcos options (no TK, the adapter too): %s", ids(o.BidCos))
	}
}

func TestPlanHmIPDirectByChoice(t *testing.T) {
	// the Charly: one RPI-RF-MOD, BidCos-RF off by choice - hmipserver on the UART directly
	in := inputs(rpiRFMod("/dev/raw-uart"))
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, BidCosNone), true
	p := MakePlan(in)
	if p.HmRF != nil || p.HmIP == nil || p.Multimacd.Run || p.RFD.Run || p.HmIPServer.Node != "/dev/raw-uart" || !p.HmIPServerAdvanced {
		t.Fatalf("HmIP-direct: %+v", p)
	}
	if names(p.Interfaces) != "VirtualDevices,HmIP-RF" {
		t.Fatalf("interfaces: %s", names(p.Interfaces))
	}
	if !strings.HasPrefix(p.RFDConf, "# occulite.bidcos.module=none\n") || !strings.Contains(p.RFDConf, "#[Interface 0]\n") {
		t.Fatalf("rfd.conf keeps the marker, the local section off:\n%s", p.RFDConf)
	}
	// with a LAN gateway: rfd on the gateway alone, BidCos-RF stays in the list
	in.RFDConf = SetBidCosChoice(rfdTemplate+lgwSection, BidCosNone)
	p = MakePlan(in)
	if !p.RFD.Run || p.RFDLocal || !p.RFDLANGateway || p.Multimacd.Run || p.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("gateways only: %+v", p)
	}
	if names(p.Interfaces) != "BidCos-RF,VirtualDevices,HmIP-RF" {
		t.Fatalf("interfaces: %s", names(p.Interfaces))
	}
	// back to auto: the local section is switched on again
	in.RFDConf = SetBidCosChoice(p.RFDConf, ChoiceAuto)
	p = MakePlan(in)
	if !p.Multimacd.Run || !p.RFDLocal || p.HmIPServer.Node != "/dev/mmd_hmip" || !strings.Contains(p.RFDConf, "\n[Interface 0]\n") {
		t.Fatalf("dual stack again: %+v\n%s", p, p.RFDConf)
	}
}

func TestPlanTwoModulesByChoice(t *testing.T) {
	// an RPI-RF-MOD on the header and an HmIP-RFUSB: auto puts both roles on the RPI-RF-MOD
	mods := []Module{rpiRFMod("/dev/raw-uart"), rfusb("/dev/raw-uart1")}
	p := MakePlan(inputs(mods...))
	if p.HmIP.Node != "/dev/raw-uart" || p.HmRF.Node != "/dev/raw-uart" || p.HmIPServer.Node != "/dev/mmd_hmip" {
		t.Fatalf("auto: %+v %+v", p.HmRF, p.HmIP)
	}
	// HmIP pinned to the stick: hmipserver on it directly, BidCos-RF (auto) keeps the module
	// through multimacd, which now serves rfd alone
	in := inputs(mods...)
	in.HmIPUserConf = SetHmIPChoice("", "3014F711A000040000000A01") // the SGTIN works too
	p = MakePlan(in)
	if p.HmIP.Node != "/dev/raw-uart1" || p.HmIPServer.Node != "/dev/raw-uart1" || p.HmRF.Node != "/dev/raw-uart" || !p.Multimacd.Run || p.Multimacd.Node != "/dev/raw-uart" || !p.RFDLocal {
		t.Fatalf("pinned: %+v %+v %+v", p.HmIP, p.HmIPServer, p.Multimacd)
	}
	if p.BoardSerial != "0000000A01" {
		t.Fatalf("the board serial follows the HmIP module, as upstream: %s", p.BoardSerial)
	}
	// BidCos-RF pinned to the stick, HmIP to the module: the other way round
	in = inputs(mods...)
	in.HmIPUserConf = SetHmIPChoice("", "0000000A03")
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, "0000000A01"), true
	p = MakePlan(in)
	if p.HmRF.Node != "/dev/raw-uart1" || p.HmRF.Address != "0xFF1234" || p.Multimacd.Node != "/dev/raw-uart1" || p.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("crossed: %+v %+v %+v", p.HmRF, p.Multimacd, p.HmIPServer)
	}
}

func TestPlanMissingChosenModule(t *testing.T) {
	// the stick hmipserver is pinned to is unplugged: no fallback to the RPI-RF-MOD
	in := inputs(rpiRFMod("/dev/raw-uart"))
	in.HmIPUserConf = SetHmIPChoice("", "0000000A01")
	p := MakePlan(in)
	if p.HmIP != nil || p.MissingHmIP != "0000000A01" || p.HmIPServerHmIP || !p.HmIPServer.Run {
		t.Fatalf("missing HmIP module: %+v", p)
	}
	// rfd keeps the module through multimacd though HmIP is not on it
	if p.HmRF == nil || !p.Multimacd.Run || !p.RFDLocal {
		t.Fatalf("rfd on the module: %+v %+v", p.HmRF, p.Multimacd)
	}
	if names(p.Interfaces) != "BidCos-RF,VirtualDevices" {
		t.Fatalf("interfaces: %s", names(p.Interfaces))
	}
	// rfd pinned to an HM-CFG-USB-2 that is not there, with a gateway: rfd on the gateway alone
	in = inputs(rpiRFMod("/dev/raw-uart"))
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate+lgwSection, "KEQ0123456"), true
	p = MakePlan(in)
	if p.HmRF != nil || p.MissingBidCos != "KEQ0123456" || !p.RFD.Run || p.RFDLocal || p.Multimacd.Run || p.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("missing BidCos module: %+v", p)
	}
	// a pinned module that cannot do the job (the Telekom stick for BidCos-RF) counts as missing
	tk := rfusb("/dev/raw-uart1")
	tk.Hardware = "HMIP-RFUSB-TK"
	in = inputs(tk)
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, tk.Serial), true
	if p = MakePlan(in); p.HmRF != nil || p.MissingBidCos != tk.Serial {
		t.Fatalf("TK for BidCos-RF: %+v", p)
	}
}

func TestPlanUSBAdapterByChoice(t *testing.T) {
	adapter := Module{USBAdapter: true, DeviceType: "USB", Hardware: "HM-CFG-USB-2", Serial: "KEQ0123456", Probe: "ok"}
	// rfd off the adapter: its section is switched off, hmipserver on the module directly
	in := inputs(adapter, rpiRFMod("/dev/raw-uart"))
	in.RFDConf, in.RFDConfExists = rfdTemplate, true
	p := MakePlan(in)
	if p.HmRF == nil || p.HmRF.Hardware != "HM-CFG-USB-2" || !p.RFDUSBAdapter {
		t.Fatalf("auto takes the adapter: %+v", p.HmRF)
	}
	in.RFDConf = SetBidCosChoice(p.RFDConf, "0000000A03")
	p = MakePlan(in)
	if p.HmRF.Hardware != "RPI-RF-MOD" || p.RFDUSBAdapter || !p.RFDLocal || !p.Multimacd.Run || p.HmIPServer.Node != "/dev/mmd_hmip" {
		t.Fatalf("the module instead of the adapter: %+v %+v", p.HmRF, p.Multimacd)
	}
	if !strings.Contains(p.RFDConf, "#Name = HM-CFG-USB\n") {
		t.Fatalf("the adapter's section switched off:\n%s", p.RFDConf)
	}
	// back to the adapter: its section is switched on again, not added a second time
	in.RFDConf = SetBidCosChoice(p.RFDConf, "KEQ0123456")
	p = MakePlan(in)
	if !p.RFDUSBAdapter || strings.Count(p.RFDConf, "Name = HM-CFG-USB") != 1 || strings.Contains(p.RFDConf, "#Name = HM-CFG-USB") {
		t.Fatalf("one active adapter section:\n%s", p.RFDConf)
	}
}

func TestReactivateUSBSectionRenumbers(t *testing.T) {
	// the adapter's section 1 was switched off, a LAN gateway took number 1 since
	conf := rfdTemplate + "\n#[Interface 1]\n#Type = USB Interface\n#Name = HM-CFG-USB\n#Serial Number = KEQ0123456\n#Encryption Key =\n#\n" + lgwSection
	lines := strings.Split(conf, "\n")
	if !reactivateUSBSection(lines, "KEQ0123456", 2) {
		t.Fatal("not found")
	}
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "\n[Interface 2]\nType = USB Interface\nName = HM-CFG-USB\nSerial Number = KEQ0123456\nEncryption Key =\n\n") {
		t.Fatalf("renumbered:\n%s", out)
	}
	if reactivateUSBSection(strings.Split(conf, "\n"), "KEQ9999999", 2) {
		t.Fatal("another adapter's section is not taken")
	}
}

func pcb(node string) Module {
	return Module{Name: strings.TrimPrefix(node, "/dev/"), Node: node, DeviceType: "HB-RF-USB@usb-0000:01:00.0-1.3", Hardware: "HM-MOD-RPI-PCB", Serial: "MEQ9000005", SGTIN: "3014F711A061A70000000A06", HmRFAddress: "0x3D0A01", HmIPAddress: "0x1E0A02", Version: "2.8.6", Probe: "ok"}
}

// deviation 15: an HM-MOD-RPI-PCB carries HmIP only through multimacd - .170's PCB beside an
// HM-CFG-USB-2, where upstream (and auto before) opened it directly and hmipserver hung
func TestPlanPCBHmIPThroughTheMultiplexer(t *testing.T) {
	adapter := Module{USBAdapter: true, DeviceType: "USB", Hardware: "HM-CFG-USB-2", Serial: "JEQ9000002", Probe: "ok"}
	p := MakePlan(inputs(adapter, pcb("/dev/raw-uart1")))
	if p.HmRF == nil || p.HmRF.Hardware != "HM-CFG-USB-2" || !p.RFDUSBAdapter || p.RFDLocal {
		t.Fatalf("rfd on the adapter: %+v", p.HmRF)
	}
	if !p.Multimacd.Run || p.Multimacd.Node != "/dev/raw-uart1" || p.HmIPServer.Node != "/dev/mmd_hmip" || p.Conflict != "" {
		t.Fatalf("the PCB's HmIP through multimacd: %+v %+v", p.Multimacd, p.HmIPServer)
	}
	// BidCos-RF off the PCB by choice (HmIP-direct would be the RPI-RF-MOD's way): multimacd for HmIP alone
	in := inputs(pcb("/dev/raw-uart1"))
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, BidCosNone), true
	if p = MakePlan(in); !p.Multimacd.Run || p.HmIPServer.Node != "/dev/mmd_hmip" || p.RFD.Run {
		t.Fatalf("PCB, BidCos-RF off: %+v %+v", p.Multimacd, p.HmIPServer)
	}
	// HmIP on the PCB, BidCos-RF on another module: one multiplexer cannot serve both
	in = inputs(pcb("/dev/raw-uart1"), rfusb("/dev/raw-uart"))
	in.HmIPUserConf = SetHmIPChoice("", "MEQ9000005")
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, "0000000A01"), true
	if p = MakePlan(in); p.Conflict == "" || p.Multimacd.Node != "/dev/raw-uart" {
		t.Fatalf("conflict: %q %+v", p.Conflict, p.Multimacd)
	}
	// the RPI-RF-MOD stays direct beside the adapter
	if p = MakePlan(inputs(adapter, rpiRFMod("/dev/raw-uart"))); p.Multimacd.Run || p.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("RPI-RF-MOD: %+v %+v", p.Multimacd, p.HmIPServer)
	}
}

// deviation 16: a stick that answers without a serial - parsed as S47 cuts it, the
// serial from the SGTIN's tail, HmIP only; beside the HM-CFG-USB-2 of .170
func TestModuleWithoutASerial(t *testing.T) {
	root := sandbox(t, map[string]string{"raw-uart1": "HmIP USB Stick@usb-0000:01:00.0-1.3"})
	fp := &fakeProbe{answers: map[string]string{"raw-uart1": "HM-MOD-RPI-PCB  3014F711A0000000AB12CD34 0x000000 0x780a04 2.8.6"}}
	det := Detector{Root: root, Run: fp.run, Sleep: func(time.Duration) {}}.Detect(context.Background())
	var m Module
	for _, x := range det.Modules {
		if x.Name == "raw-uart1" {
			m = x
		}
	}
	if !m.OK() || m.Serial != "00AB12CD34" || !m.SerialFromSGTIN || m.SGTIN != "3014F711A0000000AB12CD34" || m.HmIPAddress != "0x780a04" {
		t.Fatalf("module: %+v", m)
	}
	if !m.HmIPCapable() || m.BidCosCapable() {
		t.Fatalf("HmIP only: %v %v", m.HmIPCapable(), m.BidCosCapable())
	}
	// alone: HmIP on it, no BidCos-RF role (upstream would make one up with a random address), and
	// directly - multimacd refuses its HmIP-only coprocessor, so deviation 15 is not for it
	p := MakePlan(inputs(m))
	if p.HmIP == nil || p.HmIP.Serial != "00AB12CD34" || p.HmRF != nil || p.RFD.Run || p.Multimacd.Run || p.HmIPServer.Node != m.Node {
		t.Fatalf("plan: %+v %+v %+v %+v", p.HmIP, p.HmRF, p.Multimacd, p.HmIPServer)
	}
	// a module that reports its serial keeps it
	fp.answers["raw-uart1"] = "RPI-RF-MOD 0000000A03 3014F711A0001F0000000A03 0x1F0A14 0x3F0A15 4.4.22"
	det = Detector{Root: root, Run: fp.run, Sleep: func(time.Duration) {}}.Detect(context.Background())
	for _, x := range det.Modules {
		if x.Name == "raw-uart1" && (x.Serial != "0000000A03" || x.SerialFromSGTIN) {
			t.Fatalf("a reported serial: %+v", x)
		}
	}
}

// task 150: the HmIP path by choice - through the multiplexer or directly, where the module and
// the BidCos-RF choice allow it
func TestPlanHmIPPathByChoice(t *testing.T) {
	path := func(in Inputs, v string) Inputs {
		in.HmIPUserConf = SetHmIPPath(in.HmIPUserConf, v)
		return in
	}
	// the RPI-RF-MOD, BidCos-RF off: HmIP through multimacd by choice
	in := inputs(rpiRFMod("/dev/raw-uart"))
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, BidCosNone), true
	if p := MakePlan(in); p.Multimacd.Run || p.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("auto, BidCos-RF off: direct: %+v %+v", p.Multimacd, p.HmIPServer)
	}
	if p := MakePlan(path(in, PathMultimacd)); !p.Multimacd.Run || p.Multimacd.Node != "/dev/raw-uart" || p.HmIPServer.Node != "/dev/mmd_hmip" || p.RFD.Run || p.Conflict != "" {
		t.Fatalf("multimacd by choice: %+v %+v %q", p.Multimacd, p.HmIPServer, p.Conflict)
	}
	// direct while BidCos-RF uses the same module through multimacd: a conflict
	if p := MakePlan(path(inputs(rpiRFMod("/dev/raw-uart")), PathDirect)); p.Conflict == "" {
		t.Fatal("direct beside BidCos-RF on the same module")
	}
	// multimacd while it serves another module for BidCos-RF: a conflict
	in = path(inputs(rpiRFMod("/dev/raw-uart"), rfusb("/dev/raw-uart1")), PathMultimacd)
	in.HmIPUserConf = SetHmIPChoice(in.HmIPUserConf, "0000000A01")
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, "0000000A03"), true
	if p := MakePlan(in); p.Conflict == "" {
		t.Fatalf("multimacd busy: %+v", p.Multimacd)
	}
	// the TK is reached directly only, the dual-protocol PCB through multimacd only
	tk := rfusb("/dev/raw-uart")
	tk.Hardware = "HMIP-RFUSB-TK"
	if p := MakePlan(path(inputs(tk), PathMultimacd)); p.Conflict == "" || p.Multimacd.Run {
		t.Fatalf("TK through multimacd: %+v", p.Multimacd)
	}
	if p := MakePlan(path(inputs(tk), PathDirect)); p.Conflict != "" || p.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("TK direct: %q", p.Conflict)
	}
	adapter := Module{USBAdapter: true, DeviceType: "USB", Hardware: "HM-CFG-USB-2", Serial: "JEQ9000002", Probe: "ok"}
	if p := MakePlan(path(inputs(adapter, pcb("/dev/raw-uart1")), PathDirect)); p.Conflict == "" {
		t.Fatal("the dual PCB directly")
	}
	if got := fmt.Sprint(rpiRFMod("").HmIPPaths(), tk.HmIPPaths(), pcb("").HmIPPaths()); got != "[direct multimacd] [direct] [multimacd]" {
		t.Fatalf("paths: %s", got)
	}
	// the key round-trips and goes away for automatic
	c := SetHmIPPath(SetHmIPPath("Adapter.1.Port=/dev/raw-uart\n", PathDirect), PathMultimacd)
	if ch := ReadChoices(c, ""); ch.HmIPPath != PathMultimacd || strings.Count(c, HmIPPathKey) != 1 {
		t.Fatalf("round trip: %q", c)
	}
	if c = SetHmIPPath(c, ChoiceAuto); strings.Contains(c, HmIPPathKey) || ReadChoices(c, "").HmIPPath != ChoiceAuto {
		t.Fatalf("auto: %q", c)
	}
}

// openccu-lite B-282: an HmIP-RFUSB on the HmIP-only firmware line (HMIP_TRX_App, firmware
// below 4 - detect_radio_module reports no BidCos address for it) carries HmIP alone, directly:
// not offered for BidCos-RF, no multimacd path, no routing flags.
func TestHmIPOnlyRFUSB(t *testing.T) {
	for _, tc := range []struct {
		hardware, version, sgtin string
		fromSGTIN                bool
		want                     string
	}{
		{"RPI-RF-MOD", "4.4.22", "3014F711A0001F0000000A03", false, AppDualCoPro},
		{"HMIP-RFUSB", "4.4.18", "3014F711A000040000000A01", false, AppDualCoPro},
		{"HMIP-RFUSB", "2.8.6", "3014F711A000040000000A01", false, AppHmIPOnly},
		{"HMIP-RFUSB", "1.8.3", "3014F711A000040000000A07", false, AppHmIPOnly},
		{"HMIP-RFUSB", "", "", false, AppDualCoPro},
		{"HMIP-RFUSB-TK", "4.2.14", "3014F5AC940004000000TK01", false, ""},
		{"HM-MOD-RPI-PCB", "2.8.6", "n/a", false, AppLegacy},
		{"HM-MOD-RPI-PCB", "2.8.6", "-", false, AppLegacy},
		{"HM-MOD-RPI-PCB", "2.8.6", "3014F711A0000000AB12CD34", true, ""},
	} {
		if got := applicationOf(tc.hardware, tc.version, tc.sgtin, tc.fromSGTIN); got != tc.want {
			t.Errorf("applicationOf(%s %s %s %v) = %q, want %q", tc.hardware, tc.version, tc.sgtin, tc.fromSGTIN, got, tc.want)
		}
	}
	root := sandbox(t, map[string]string{"raw-uart1": "eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3"})
	fp := &fakeProbe{answers: map[string]string{"raw-uart1": "HMIP-RFUSB 0000000A07 3014F711A000040000000A07 0x000000 0x7F0A18 1.8.3"}}
	det := Detector{Root: root, Run: fp.run, Sleep: func(time.Duration) {}}.Detect(context.Background())
	var m Module
	for _, x := range det.Modules {
		if x.Name == "raw-uart1" {
			m = x
		}
	}
	if !m.OK() || m.Serial != "0000000A07" || m.Version != "1.8.3" || m.Application != AppHmIPOnly || !m.HmIPOnly() {
		t.Fatalf("module: %+v", m)
	}
	if !m.HmIPCapable() || m.BidCosCapable() || fmt.Sprint(m.HmIPPaths()) != "[direct]" {
		t.Fatalf("HmIP only: %v %v %v", m.HmIPCapable(), m.BidCosCapable(), m.HmIPPaths())
	}
	if !strings.Contains(strings.Join(det.Log, "\n"), "HMIP_TRX_App: HmIP only") {
		t.Fatalf("log: %q", det.Log)
	}
	dual := rfusb("/dev/raw-uart2")
	dual.Application = AppDualCoPro
	o := ChoiceOptions(Detection{Modules: []Module{m, dual}})
	if len(o.HmIP) != 2 || !o.HmIP[0].HmIPOnly || o.HmIP[0].Application != AppHmIPOnly || fmt.Sprint(o.HmIP[0].Paths) != "[direct]" || o.HmIP[1].HmIPOnly || fmt.Sprint(o.HmIP[1].Paths) != "[direct multimacd]" {
		t.Fatalf("hmip options: %+v", o.HmIP)
	}
	if len(o.BidCos) != 1 || o.BidCos[0].ID != dual.Serial || o.BidCos[0].HmIPOnly {
		t.Fatalf("bidcos options (the dual stick alone): %+v", o.BidCos)
	}
	// alone, automatic: HmIP on it directly, no BidCos-RF role, no multimacd, no routing flags
	p := MakePlan(inputs(m))
	if p.HmIP == nil || p.HmIP.Serial != "0000000A07" || p.HmRF != nil || p.RFD.Run || p.Multimacd.Run || p.HmIPServer.Node != m.Node || p.HmIPServerAdvanced {
		t.Fatalf("plan: %+v %+v %+v %+v advanced=%v", p.HmIP, p.HmRF, p.Multimacd, p.HmIPServer, p.HmIPServerAdvanced)
	}
	if !strings.Contains(strings.Join(p.Notes, "\n"), "deviation 17") {
		t.Fatalf("notes: %q", p.Notes)
	}
	// the dual stick keeps its routing flags
	if p := MakePlan(inputs(dual)); !p.HmIPServerAdvanced {
		t.Fatal("the dual stick: routing flags")
	}
	// pinned for BidCos-RF it is missing; through multimacd it is a conflict; directly it is fine
	path := func(in Inputs, v string) Inputs {
		in.HmIPUserConf = SetHmIPPath(in.HmIPUserConf, v)
		return in
	}
	in := inputs(m)
	in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, "0000000A07"), true
	if p := MakePlan(in); p.HmRF != nil || p.MissingBidCos != "0000000A07" {
		t.Fatalf("pinned for BidCos-RF: %+v missing=%q", p.HmRF, p.MissingBidCos)
	}
	if p := MakePlan(path(inputs(m), PathMultimacd)); p.Conflict == "" || p.Multimacd.Run {
		t.Fatalf("through multimacd: %q %+v", p.Conflict, p.Multimacd)
	}
	if p := MakePlan(path(inputs(m), PathDirect)); p.Conflict != "" || p.HmIPServer.Node != m.Node {
		t.Fatalf("direct: %q", p.Conflict)
	}
}

// openccu-lite B-300: which nodes are the header, and when the header is empty.
func TestEmptyHeader(t *testing.T) {
	for _, c := range []struct {
		name           string
		m              Module
		header, silent bool
	}{
		{"GPIO, cut at the limit", Module{Name: "raw-uart", GPIO: true, DeviceType: "GPIO@fe201000.serial", Probe: "timeout"}, true, true},
		{"GPIO, no module", Module{Name: "raw-uart", GPIO: true, Probe: "none"}, true, true},
		{"GPIO, a wrong answer", Module{Name: "raw-uart", GPIO: true, Probe: "error"}, true, false},
		{"GPIO, a module", Module{Name: "raw-uart", GPIO: true, Probe: "ok"}, true, false},
		{"the ttyAMA0 fallback", Module{Name: "ttyAMA0", Node: "/dev/ttyAMA0", Probe: "timeout"}, true, true},
		{"a stick", Module{Name: "raw-uart1", DeviceType: "eQ-3 HmIP-RFUSB@usb-1.3", Probe: "timeout"}, false, false},
		{"an HB-RF-ETH", Module{Name: "raw-uart1", DeviceType: "HB-RF-ETH@192.0.2.50", Probe: "none"}, false, false},
		{"the HM-CFG-USB-2", Module{USBAdapter: true, DeviceType: "USB", Probe: "ok"}, false, false},
	} {
		if c.m.Header() != c.header || c.m.EmptyHeader() != c.silent {
			t.Errorf("%s: header %v empty %v, want %v %v", c.name, c.m.Header(), c.m.EmptyHeader(), c.header, c.silent)
		}
	}
	det := Detection{Modules: []Module{{Serial: "0000000A07", SGTIN: "3014F711A000040000000A07", Probe: "ok"}, {Serial: "0000000A03", Probe: "error"}}}
	for id, want := range map[string]bool{"0000000a07": true, "3014F711A000040000000A07": true, "0000000A03": false, "": false} {
		if det.HasModule(id) != want {
			t.Errorf("HasModule(%q) = %v", id, !want)
		}
	}
}
