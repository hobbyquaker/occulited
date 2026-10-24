package radio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fork's differential harness (scripts/testcases/lite-radio-oracle-test.sh) checks the plan
// against upstream's scripts over the whole hardware matrix; these tests cover what the harness
// cannot: the detection's timeouts and second pass against a fake probe, and the edits and
// decisions on their own, as table tests.

const rfdTemplate = "# TCP Port for XmlRpc connections\nListen IP = 127.0.0.1\nListen Port = 32001\n\nLog Destination = Syslog\nKey File = /etc/config/keys\nImproved Coprocessor Initialization = true\n\n[Interface 0]\nType = CCU2\nComPortFile = /dev/mmd_bidcos\n#AccessFile = /dev/null\n#ResetFile = /dev/null\n"

func rpiRFMod(node string) Module {
	return Module{Name: strings.TrimPrefix(node, "/dev/"), Node: node, DeviceType: "GPIO@3f201000.serial", GPIO: true, Hardware: "RPI-RF-MOD", Serial: "0000000A03", SGTIN: "3014F711A0001F0000000A03", HmRFAddress: "0x1F0A14", HmIPAddress: "0x3F0A15", Version: "4.4.22", Probe: "ok"}
}

func rfusb(node string) Module {
	return Module{Name: strings.TrimPrefix(node, "/dev/"), Node: node, DeviceType: "eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3", Hardware: "HMIP-RFUSB", Serial: "0000000A01", SGTIN: "3014F711A000040000000A01", HmRFAddress: "0x000000", HmIPAddress: "0x7F0A16", Version: "4.4.18", Probe: "ok"}
}

func inputs(mods ...Module) Inputs {
	return Inputs{
		Detection: Detection{Modules: mods}, Env: map[string]string{"HM_HOST": "rpi3", "HM_MODE": "NORMAL", "HM_RTC": "rx8130"},
		Host: "rpi3", Mode: "NORMAL", TemplateRFDConf: rfdTemplate, TemplateMultimacdConf: "Coprocessor Device Path = /dev/ttyXXX\nLoop Master Device = /dev/eq3loop\n",
		TemplateCrRFDConf:      "Adapter.1.Port=/dev/mmd_hmip\nAdapter.Local.Device.Enabled=true\nLan.Routing.Enabled=true\nLegacy.BindAddress=0.0.0.0\n",
		TemplateInterfacesList: "<interfaces><ipc><name>BidCos-RF</name><url>xmlrpc_bin://127.0.0.1:32001</url><info>BidCos-RF</info></ipc><ipc><name>VirtualDevices</name><url>xmlrpc://127.0.0.1:39292/groups</url><info>Virtual Devices</info></ipc><ipc><name>HmIP-RF</name><url>xmlrpc://127.0.0.1:32010</url><info>HmIP-RF</info></ipc></interfaces>",
		HMServerConf:           "hmServerPort=39292\ndiagramDatabasePath=/media/usb0/measurement\n", HmIPServerDefaults: map[string]string{"HMIP_BIND_ADDRESS": "127.0.0.1"},
		Arch: "aarch64", MemTotalKB: 946000, UserfsOnMMC: true, RandomAddress: "0xFF1234",
	}
}

func names(list []Interface) string {
	var n []string
	for _, i := range list {
		n = append(n, i.Name)
	}
	return strings.Join(n, ",")
}

func TestPlanDualStackModule(t *testing.T) {
	p := MakePlan(inputs(rpiRFMod("/dev/raw-uart")))
	if p.HmRF == nil || p.HmIP == nil || p.HmRF.Node != "/dev/raw-uart" || p.HmIP.Node != "/dev/raw-uart" {
		t.Fatalf("roles: %+v %+v", p.HmRF, p.HmIP)
	}
	if !p.Multimacd.Run || p.Multimacd.Node != "/dev/raw-uart" || !p.RFD.Run || !p.RFDLocal || p.HmIPServer.Node != "/dev/mmd_hmip" || !p.HmIPServerAdvanced {
		t.Fatalf("daemons: %+v %+v %+v", p.Multimacd, p.RFD, p.HmIPServer)
	}
	if p.HmRFAddressActive != "0x1F0A14" || p.HmIPAddressActive != "0x3F0A15" || p.BoardSerial != "0000000A03" || p.BoardSGTIN != "3014F711A0001F0000000A03" {
		t.Fatalf("addresses: %+v", p)
	}
	if names(p.Interfaces) != "BidCos-RF,VirtualDevices,HmIP-RF" {
		t.Fatalf("interfaces: %s", names(p.Interfaces))
	}
	if p.HeapMB != 128 {
		t.Fatalf("heap: %d", p.HeapMB)
	}
	f := Render(inputs(rpiRFMod("/dev/raw-uart")), p)
	if !strings.Contains(f.HMMode, "HM_RTC='rx8130'\n") || !strings.Contains(f.HMMode, "HM_HMIP_DEV='RPI-RF-MOD'\n") {
		t.Fatalf("hm_mode keeps the environment and adds the roles:\n%s", f.HMMode)
	}
	if !strings.Contains(f.Commands["hmipserver"], "-Dgnu.io.rxtx.SerialPorts=/dev/mmd_hmip -Xmx128m") || !strings.HasSuffix(f.Commands["hmipserver"], "de.eq3.ccu.server.ip.HMIPServer /var/etc/crRFD.conf /var/etc/HMServer.conf") {
		t.Fatalf("hmipserver command: %s", f.Commands["hmipserver"])
	}
	if !strings.Contains(f.CrRFDConf, "Legacy.BindAddress=127.0.0.1\n") || strings.Contains(f.CrRFDConf, "0.0.0.0") {
		t.Fatalf("crRFD.conf bind: %s", f.CrRFDConf)
	}
	if !strings.Contains(f.HMServerConf, "diagramDatabasePath="+DiagramPath) {
		t.Fatalf("HMServer.conf: %s", f.HMServerConf)
	}
}

func TestPlanStickRandomAddressAndIDs(t *testing.T) {
	in := inputs(Module{Name: "raw-uart", Node: "/dev/raw-uart", DeviceType: "GPIO@fe201000.serial", GPIO: true, Probe: "none"}, rfusb("/dev/raw-uart1"))
	in.Host, in.Env["HM_HOST"] = "rpi4", "rpi4"
	p := MakePlan(in)
	if p.HmRF == nil || p.HmRF.Address != "0xFF1234" || p.HmRFAddressActive != "0xFF1234" {
		t.Fatalf("a stick without a BidCos address gets the random one: %+v", p.HmRF)
	}
	in.IDs, in.IDsExists = "BidCoS-Address=0xFE0A19\nSerialNumber=0000000A01\n", true
	in.HmIPAddressConf, in.HmIPAddressOK = "#Create random address\nAdapter.1.Address=7F0A16\n", true
	p = MakePlan(in)
	if p.HmRFAddressActive != "0xFE0A19" || p.HmIPAddressActive != "0x7F0A16" || p.IDsInvalid {
		t.Fatalf("the box's own addresses win: %+v", p)
	}
	in.IDs = "BidCoS-Address = 0x000000\n"
	p = MakePlan(in)
	if !p.IDsInvalid || p.HmRFAddressActive != "0xFF1234" {
		t.Fatalf("an ids file without a usable address is moved aside: %+v", p)
	}
	// rfd writes the address it takes in decimal (openccu-lite B-266): 16585268 is 0xFD1234
	for ids, want := range map[string]string{
		"BidCoS-Address = 16585268\n":               "0xFD1234",
		"BidCoS-Address=0xFE0A19\nSerialNumber=X\n": "0xFE0A19",
		"BidCoS-Address=0x000000\n":                 "0x000000",
		"BidCoS-Address=0\n":                        "0",
		"SerialNumber=X\n":                          "",
		"BidCoS-Address = 0123\n":                   "0123",
		"BidCoS-Address = 99999999\n":               "99999999",
	} {
		if got := IDsAddress(ids); got != want {
			t.Errorf("IDsAddress(%q) = %q, want %q", ids, got, want)
		}
	}
	in.IDs = "BidCoS-Address = 16585268\n"
	if p = MakePlan(in); p.IDsInvalid || p.HmRFAddressActive != "0xFD1234" {
		t.Fatalf("rfd's decimal address: invalid %v, active %q", p.IDsInvalid, p.HmRFAddressActive)
	}
	// openccu-lite B-266: the prep step makes both files empty for the daemons to fill; an empty
	// (or blank) file is no address at all - nothing to move aside, and never upstream's "0x"
	for _, blank := range []string{"", "\n", "  \n\n"} {
		in.IDs, in.HmIPAddressConf = blank, blank
		p = MakePlan(in)
		if p.IDsInvalid || p.HmRFAddressActive != "0xFF1234" || p.HmIPAddressActive != p.HmIP.Address || p.HmIPAddressActive == "0x" {
			t.Fatalf("empty ids/hmip_address.conf %q: invalid %v, active %q/%q", blank, p.IDsInvalid, p.HmRFAddressActive, p.HmIPAddressActive)
		}
	}
}

func TestPlanTKStickHmIPOnly(t *testing.T) {
	m := rfusb("/dev/raw-uart1")
	m.Hardware, m.DeviceType = "HMIP-RFUSB-TK", "eQ-3 HmIP-RFUSB-TK@usb-1"
	p := MakePlan(inputs(m))
	if p.HmRF != nil || p.HmIP == nil || p.Multimacd.Run || p.RFD.Run || p.HmIPServer.Node != "/dev/raw-uart1" || p.HmIPServerAdvanced {
		t.Fatalf("TK: %+v", p)
	}
	if names(p.Interfaces) != "VirtualDevices,HmIP-RF" {
		t.Fatalf("interfaces: %s", names(p.Interfaces))
	}
}

func TestPlanUSBAdapter(t *testing.T) {
	adapter := Module{USBAdapter: true, DeviceType: "USB", Hardware: "HM-CFG-USB-2", Serial: "KEQ0123456", Probe: "ok"}
	// alone: rfd on the adapter, no HmIP
	p := MakePlan(inputs(adapter))
	if p.HmRF == nil || p.HmRF.Node != "" || p.Multimacd.Run || !p.RFD.Run || p.RFDLocal || !p.RFDUSBAdapter || p.HmIPServerHmIP {
		t.Fatalf("adapter alone: %+v", p)
	}
	if !strings.Contains(p.RFDConf, "[Interface 1]\nType = USB Interface\nName = HM-CFG-USB\nSerial Number = KEQ0123456\nEncryption Key =\n") || !strings.Contains(p.RFDConf, "#[Interface 0]\n") {
		t.Fatalf("rfd.conf:\n%s", p.RFDConf)
	}
	if names(p.Interfaces) != "BidCos-RF,VirtualDevices" {
		t.Fatalf("interfaces: %s", names(p.Interfaces))
	}
	// beside an RPI-RF-MOD on the header: the module keeps HmIP only, hmipserver on it directly, no
	// multimacd (deviation 9: upstream started one with an empty device path)
	p = MakePlan(inputs(adapter, rpiRFMod("/dev/raw-uart")))
	if p.HmRF.Hardware != "HM-CFG-USB-2" || p.HmIP == nil || p.Multimacd.Run || p.HmIPServer.Node != "/dev/raw-uart" || !p.RFD.Run {
		t.Fatalf("adapter beside the module: %+v %+v %+v", p.HmRF, p.Multimacd, p.HmIPServer)
	}
	// beside a stick: multimacd not required, the stick directly
	p = MakePlan(inputs(adapter, rfusb("/dev/raw-uart1")))
	if p.Multimacd.Run || p.HmIPServer.Node != "/dev/raw-uart1" || !p.RFD.Run || !p.RFDUSBAdapter {
		t.Fatalf("adapter beside the stick: %+v", p)
	}
}

func TestPlanTwoModules(t *testing.T) {
	pcb := Module{Name: "raw-uart1", Node: "/dev/raw-uart1", DeviceType: "HB-RF-USB-2@usb-1", Hardware: "HM-MOD-RPI-PCB", Serial: "MEQ0123456", SGTIN: "-", HmRFAddress: "0xABC123", HmIPAddress: "0x000000", Version: "2.8.6", Probe: "ok"}
	p := MakePlan(inputs(rpiRFMod("/dev/raw-uart"), pcb))
	if p.HmRF.Hardware != "HM-MOD-RPI-PCB" || p.HmIP.Hardware != "RPI-RF-MOD" {
		t.Fatalf("preferences: %+v %+v", p.HmRF, p.HmIP)
	}
	if !p.Multimacd.Run || p.Multimacd.Node != "/dev/raw-uart1" || p.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("multimacd on the PCB's node for rfd, hmipserver direct: %+v %+v", p.Multimacd, p.HmIPServer)
	}
	// the same two the other way round
	pcb.Node, pcb.Name, pcb.DeviceType, pcb.GPIO = "/dev/raw-uart", "raw-uart", "GPIO@3f201000.serial", true
	m := rpiRFMod("/dev/raw-uart1")
	m.DeviceType, m.GPIO, m.LEDPins = "HB-RF-USB-2@usb-1", false, []string{"22", "23", "24"}
	p = MakePlan(inputs(pcb, m))
	if p.HmRF.Node != "/dev/raw-uart" || p.HmIP.Node != "/dev/raw-uart1" || p.HBRFLED == nil || p.HBRFLED.Red != "22" {
		t.Fatalf("reversed: %+v %+v %+v", p.HmRF, p.HmIP, p.HBRFLED)
	}
}

func TestPlanNoRadioAndLANGateway(t *testing.T) {
	in := inputs(Module{Name: "raw-uart", Node: "/dev/raw-uart", DeviceType: "GPIO@fe201000.serial", GPIO: true, Probe: "none"})
	in.Detection.BoardMAC = "dc:a6:32:0f:00:01"
	p := MakePlan(in)
	if p.HmRF != nil || p.HmIP != nil || p.RFD.Run || p.Multimacd.Run || p.HmIPServerHmIP || !p.HmIPServer.Run || p.BoardSerial != "6320f0001" {
		t.Fatalf("no radio: %+v", p)
	}
	if names(p.Interfaces) != "VirtualDevices" {
		t.Fatalf("interfaces: %s", names(p.Interfaces))
	}
	in.RFDConf, in.RFDConfExists = rfdTemplate+"\n[Interface 1]\nType = HMLGW2\nName = Garage\nSerial Number = KEQ0987654\nEncryption Key = k\n", true
	p = MakePlan(in)
	if !p.RFD.Run || p.RFDLocal || !p.RFDLANGateway || names(p.Interfaces) != "BidCos-RF,VirtualDevices" {
		t.Fatalf("LAN gateway only: %+v %s", p.RFD, names(p.Interfaces))
	}
	if !strings.Contains(p.RFDConf, "#[Interface 0]\n") || !strings.Contains(p.RFDConf, "\n[Interface 1]\nType = HMLGW2\n") {
		t.Fatalf("rfd.conf:\n%s", p.RFDConf)
	}
}

func TestPlanWiredAndLGWMode(t *testing.T) {
	in := inputs(rpiRFMod("/dev/raw-uart"))
	in.HS485DConf, in.HS485DConfExists = "Listen Port = 2000\n\n[Interface 0]\nType = HMWLGW\nSerial Number = LEQ0123456\n", true
	p := MakePlan(in)
	if !p.HS485D.Run || names(p.Interfaces) != "BidCos-RF,VirtualDevices,HmIP-RF,BidCos-Wired" {
		t.Fatalf("wired: %+v %s", p.HS485D, names(p.Interfaces))
	}
	f := Render(in, p)
	// B-164: the port is forced and the daemon put on the loopback, as rfd's template does it -
	// hs485d listened on every interface before (D-29)
	if f.VarHS485DConf != "Listen IP = 127.0.0.1\nListen Port = 32000\n\n[Interface 0]\nType = HMWLGW\nSerial Number = LEQ0123456\n" || f.Commands["hs485d"] != "/bin/hs485dLoader -l 5 -dw /var/etc/hs485d.conf" {
		t.Fatalf("hs485d: %q %q", f.VarHS485DConf, f.Commands["hs485d"])
	}
	// without the wired interface the entry goes again (D-96)
	in.InterfacesList, in.InterfacesListOK = RenderInterfaces(in.TemplateInterfacesList, p.Interfaces), true
	in.HS485DConf, in.HS485DConfExists = "", false
	if p = MakePlan(in); p.HS485D.Run || names(p.Interfaces) != "BidCos-RF,VirtualDevices,HmIP-RF" {
		t.Fatalf("wired gone: %s", names(p.Interfaces))
	}
	in.HS485DConf, in.HS485DConfExists = "Listen Port = 2000\n\n[Interface 0]\nType = HMWLGW\nSerial Number = LEQ0123456\n", true
	in.Mode, in.HMLGW = "HM-LGW", true
	p = MakePlan(in)
	if !p.Multimacd.Run || !p.Hmlangw.Run || p.RFD.Run || p.HmIPServer.Run || p.HS485D.Run {
		t.Fatalf("LAN-gateway mode: %+v", p)
	}
	if f := Render(in, p); f.Commands["hmlangw"] != "exec /bin/hmlangw -b -n 0000000A03 -s /dev/mmd_bidcos -r -1 >/var/log/hmlangw.log 2>&1" || f.RFDConf != "" {
		t.Fatalf("hmlangw: %+v", f.Commands)
	}
}

func TestLogLevels(t *testing.T) {
	l := logLevels(map[string]string{"LOGLEVEL_RFD": "2", "LOGLEVEL_HMIP": "debug"})
	if l.RFD != "2" || l.Multimacd != "2" || l.HS485D != "5" || l.HmIP != "DEBUG" {
		t.Fatalf("%+v", l)
	}
	// a stale 3 reads as info, 1 stays debug (task 297; TestMultimacdLevel has the rest)
	l = logLevels(map[string]string{"LOGLEVEL_MULTIMACD": "3", "LOGLEVEL_RFD": "x"})
	if l.RFD != "5" || l.Multimacd != "2" {
		t.Fatalf("%+v", l)
	}
	l = logLevels(map[string]string{"LOGLEVEL_MULTIMACD": "1", "LOGLEVEL_RFD": "x"})
	if l.RFD != "5" || l.Multimacd != "1" {
		t.Fatalf("%+v", l)
	}
	if heapMB(3884000, 0) != 896 || heapMB(946000, 0) != 128 || heapMB(0, 2*1024*1024*1024) != 512 {
		t.Fatalf("heap: %d %d %d", heapMB(3884000, 0), heapMB(946000, 0), heapMB(0, 2*1024*1024*1024))
	}
}

func TestEditRFDConf(t *testing.T) {
	mod := &Role{Hardware: "RPI-RF-MOD", Node: "/dev/raw-uart"}
	// a CCU3's file: no loopback line, the section active, a LAN gateway after it
	ccu3 := "Listen Port = 32001\n\nKey File = /etc/config/keys\n\n[Interface 0]\nType = CCU2\nComPortFile = /dev/mmd_bidcos\nAccessFile = /dev/null\nResetFile = /dev/null\n\n[Interface 1]\nType = HMLGW2\nName = Garage\nSerial Number = KEQ0987654\nEncryption Key = k\n\n"
	out, local, usb := EditRFDConf(ccu3, true, rfdTemplate, mod, true)
	if !local || usb || !strings.HasPrefix(out, "Listen IP = 127.0.0.1\nListen Port = 32001\n") || !strings.Contains(out, "Improved Coprocessor Initialization = true\n\n[Interface 0]\n") || !strings.Contains(out, "[Interface 1]\nType = HMLGW2\n") {
		t.Fatalf("ccu3:\n%s", out)
	}
	// the section commented by an earlier boot without the module: uncommented again
	commented := "Listen IP = 127.0.0.1\nListen Port = 32001\n\nImproved Coprocessor Initialization = true\n\n#[Interface 0]\n#Type = CCU2\n#ComPortFile = /dev/mmd_bidcos\n#AccessFile = /dev/null\n#ResetFile = /dev/null\n#\n[Interface 1]\nType = HMLGW2\n"
	out, local, _ = EditRFDConf(commented, true, rfdTemplate, mod, true)
	if !local || !strings.Contains(out, "\n[Interface 0]\nType = CCU2\nComPortFile = /dev/mmd_bidcos\nAccessFile = /dev/null\nResetFile = /dev/null\n\n[Interface 1]\n") {
		t.Fatalf("uncommented:\n%s", out)
	}
	// no module: the section switched off, up to and including the blank line
	out, local, _ = EditRFDConf(ccu3, true, rfdTemplate, nil, false)
	if local || !strings.Contains(out, "#[Interface 0]\n#Type = CCU2\n#ComPortFile = /dev/mmd_bidcos\n#AccessFile = /dev/null\n#ResetFile = /dev/null\n#\n[Interface 1]\n") {
		t.Fatalf("commented:\n%s", out)
	}
	// the key-file repair keeps the gateway (deviation 7)
	out, _, _ = EditRFDConf(strings.Replace(ccu3, "/etc/config/keys", "/etc/config/rfd/keys", 1), true, rfdTemplate, mod, true)
	if !strings.Contains(out, "Key File = /etc/config/keys\n") || !strings.Contains(out, "[Interface 1]\nType = HMLGW2\n") {
		t.Fatalf("keys repaired:\n%s", out)
	}
	// the adapter's section after a LAN gateway takes the next number (deviation 4)
	adapter := &Role{Hardware: "HM-CFG-USB-2", Serial: "KEQ0123456"}
	out, local, usb = EditRFDConf(ccu3, true, rfdTemplate, adapter, false)
	// (the file ended with a blank line, and upstream's echo "" adds one more: kept as it is)
	if local || !usb || !strings.HasSuffix(out, "Encryption Key = k\n\n\n[Interface 2]\nType = USB Interface\nName = HM-CFG-USB\nSerial Number = KEQ0123456\nEncryption Key =\n") {
		t.Fatalf("adapter added:\n%s", out)
	}
	// the adapter gone: its own section switched off, the gateway after it kept (deviation 5)
	gone := out
	out, _, usb = EditRFDConf(gone, true, rfdTemplate, mod, true)
	if usb || !strings.Contains(out, "\n#[Interface 2]\n#Type = USB Interface\n#Name = HM-CFG-USB\n") || !strings.Contains(out, "\n[Interface 1]\nType = HMLGW2\n") {
		t.Fatalf("adapter gone:\n%s", out)
	}
	// a missing file: the template
	out, local, _ = EditRFDConf("", false, rfdTemplate, mod, true)
	if !local || !strings.HasSuffix(out, "[Interface 0]\nType = CCU2\nComPortFile = /dev/mmd_bidcos\nAccessFile = /dev/null\nResetFile = /dev/null\n") {
		t.Fatalf("template:\n%s", out)
	}
	if v := VarRFDConf("Listen Port = 2001\n"); v != "Listen IP = 127.0.0.1\nListen Port = 32001\n" {
		t.Fatalf("var: %q", v)
	}
	// B-164: a wired file that already names another address is corrected, and one that has the
	// loopback line is left as it is
	for _, c := range []struct{ in, want string }{
		{"Listen IP = 0.0.0.0\nListen Port = 2000\n", "Listen IP = 127.0.0.1\nListen Port = 32000\n"},
		{"Listen IP = 127.0.0.1\nListen Port = 32000\n", "Listen IP = 127.0.0.1\nListen Port = 32000\n"},
		{"# generated\nListen Port = 32000\n", "# generated\nListen IP = 127.0.0.1\nListen Port = 32000\n"},
	} {
		if got := VarHS485DConf(c.in); got != c.want {
			t.Errorf("VarHS485DConf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoopbackListen(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Listen IP = 127.0.0.1\nListen Port = 32001\n", "Listen IP = 127.0.0.1\nListen Port = 32001\n"},
		{"Listen IP = 0.0.0.0\nListen Port = 32001\nListen IP = 1.2.3.4\n", "Listen IP = 127.0.0.1\nListen Port = 32001\n"},
		{"# x\nListen Port = 32001\n", "# x\nListen IP = 127.0.0.1\nListen Port = 32001\n"},
		{"Log Level = 1\n", "Listen IP = 127.0.0.1\nLog Level = 1\n"},
	} {
		if got := LoopbackListen(c.in); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTemplateInterfaces(t *testing.T) {
	tmpl := inputs().TemplateInterfacesList
	got := TemplateInterfaces(tmpl)
	if names(got) != "BidCos-RF,VirtualDevices,HmIP-RF" || got[0].URL != "xmlrpc_bin://127.0.0.1:32001" {
		t.Fatalf("%+v", got)
	}
	// D-97: a foreign entry in the box's file is not carried over - the plan starts from the template
	in := inputs(rpiRFMod("/dev/raw-uart"))
	in.InterfacesList, in.InterfacesListOK = "<interfaces><ipc><name>CCU-Jack</name><url>xmlrpc://127.0.0.1:2121/RPC3</url><info>CCU-Jack</info></ipc></interfaces>", true
	if names(MakePlan(in).Interfaces) != "BidCos-RF,VirtualDevices,HmIP-RF" {
		t.Fatalf("plan interfaces: %s", names(MakePlan(in).Interfaces))
	}
	// the render is the template's own bytes when nothing is cut (D-97), the cut entries' blocks
	// removed when it is, and the wired entry appended in the template's layout
	realTmpl := "<?xml version=\"1.0\" encoding=\"utf-8\" ?> \n<interfaces v=\"1.0\">\n\t<ipc>\n\t \t<name>BidCos-RF</name>\n\t \t<url>xmlrpc_bin://127.0.0.1:32001</url> \n\t \t<info>BidCos-RF</info> \n\t</ipc>\n\t<ipc>\n\t \t<name>VirtualDevices</name>\n\t \t<url>xmlrpc://127.0.0.1:39292/groups</url> \n\t \t<info>Virtual Devices</info> \n\t</ipc>\n\t<ipc>\n\t \t<name>HmIP-RF</name>\n\t \t<url>xmlrpc://127.0.0.1:32010</url>\n\t \t<info>HmIP-RF</info>\n\t</ipc>\n</interfaces>\n"
	if x := RenderInterfaces(realTmpl, TemplateInterfaces(realTmpl)); x != realTmpl {
		t.Fatalf("the full list must be the template's bytes:\n%s", x)
	}
	cut := RenderInterfaces(realTmpl, removeInterface(TemplateInterfaces(realTmpl), "HmIP-RF"))
	if names(ParseInterfaces(cut)) != "BidCos-RF,VirtualDevices" || strings.Contains(cut, "HmIP-RF") || !strings.HasSuffix(cut, "\t</ipc>\n</interfaces>\n") {
		t.Fatalf("cut:\n%s", cut)
	}
	wired := RenderInterfaces(realTmpl, append(TemplateInterfaces(realTmpl), Interface{Name: "BidCos-Wired", URL: "xmlrpc_bin://127.0.0.1:32000", Info: "BidCos-Wired"}))
	if names(ParseInterfaces(wired)) != "BidCos-RF,VirtualDevices,HmIP-RF,BidCos-Wired" || !strings.HasPrefix(wired, realTmpl[:len(realTmpl)-len("</interfaces>\n")]) {
		t.Fatalf("wired:\n%s", wired)
	}
}

// fakeProbe answers detect_radio_module by node: a delay (a real sleep, the limits in the tests
// are milliseconds), then an answer, an error, or an error once (flaky).
type fakeProbe struct {
	mu      sync.Mutex
	answers map[string]string // node name -> answer; "none" = fails; "flaky:..." = fails once
	delays  map[string]time.Duration
	calls   []string
	seen    map[string]int
}

func (f *fakeProbe) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch filepath.Base(name) {
	case "detect_radio_module":
		n := filepath.Base(args[0])
		f.calls = append(f.calls, n)
		if f.seen == nil {
			f.seen = map[string]int{}
		}
		f.seen[n]++
		if d := f.delays[n]; d > 0 {
			f.mu.Unlock()
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
			f.mu.Lock()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		a := f.answers[n]
		// "empty:": a coprocessor not yet back from its bootloader answers once without an
		// identity (openccu-lite B-331, the HmIP-RFUSB-TK on rpi4-2)
		if strings.HasPrefix(a, "empty:") {
			if f.seen[n] == 1 {
				return []byte("HMIP-RFUSB     0.0.0\n"), nil
			}
			a = strings.TrimPrefix(a, "empty:")
		}
		if strings.HasPrefix(a, "flaky:") {
			if f.seen[n] == 1 {
				return []byte("Error: Radio module was found, but did not respond correctly"), errors.New("exit 255")
			}
			a = strings.TrimPrefix(a, "flaky:")
		}
		if a == "" || a == "none" {
			return []byte("Error: No radio module found"), errors.New("exit 1")
		}
		return []byte(a + "\n"), nil
	case "uname":
		return []byte("aarch64\n"), nil
	}
	return nil, nil
}

func sandbox(t *testing.T, nodes map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"sys/class/raw-uart", "dev", "etc/config", "var", "proc", "sys/bus/usb/devices", "sys/class/net/eth0", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, typ := range nodes {
		if err := os.MkdirAll(filepath.Join(root, "sys/class/raw-uart", name), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(root, "sys/class/raw-uart", name, "device_type"), []byte(typ+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(root, "sys/class/raw-uart", name, "reset_radio_module"), nil, 0o644)
		_ = os.WriteFile(filepath.Join(root, "dev", name), nil, 0o644)
	}
	_ = os.WriteFile(filepath.Join(root, "sys/class/net/eth0/type"), []byte("1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys/class/net/eth0/address"), []byte("dc:a6:32:0f:00:01\n"), 0o644)
	return root
}

func TestDetectPi4TwoPasses(t *testing.T) {
	root := sandbox(t, map[string]string{"raw-uart": "GPIO@fe201000.serial", "raw-uart1": "eQ-3 HmIP-RFUSB@usb-1"})
	stick := "HMIP-RFUSB 0000000A01 3014F711A000040000000A01 0x000000 0x7F0A16 4.4.18"
	sleeps := 0
	// the empty header: the probe takes longer than the limit; the stick answers wrongly once
	// right after the cut probe (the Pi 4 on dev.2, task 138)
	fp := &fakeProbe{answers: map[string]string{"raw-uart": "none", "raw-uart1": "flaky:" + stick}, delays: map[string]time.Duration{"raw-uart": 200 * time.Millisecond}}
	d := Detector{Root: root, Host: "rpi4", Run: fp.run, GPIOLimit: 30 * time.Millisecond, ProbeLimit: time.Second, Sleep: func(time.Duration) { sleeps++ }}
	det := d.Detect(context.Background())
	if strings.Join(fp.calls, ",") != "raw-uart,raw-uart1,raw-uart1" {
		t.Fatalf("probes: %v", fp.calls)
	}
	if !det.SecondPass || sleeps != 2 || det.Modules[0].Probe != "timeout" || det.Modules[1].Probe != "ok" || det.Modules[1].Hardware != "HMIP-RFUSB" || !det.Found() {
		t.Fatalf("%+v sleeps %d", det, sleeps)
	}
	// a slow module alone on the header: found by the second probe without the limit
	fp = &fakeProbe{answers: map[string]string{"raw-uart": "RPI-RF-MOD 0000000A03 3014F711A0001F0000000A03 0x1F0A14 0x3F0A15 4.4.22", "raw-uart1": "none"}, delays: map[string]time.Duration{"raw-uart": 60 * time.Millisecond}}
	d.Run = fp.run
	det = d.Detect(context.Background())
	// (the stick's node that never answers is given up after its own retries, B-331)
	if strings.Join(fp.calls, ",") != "raw-uart,raw-uart1,raw-uart1,raw-uart,raw-uart1,raw-uart1" || det.Modules[0].Probe != "ok" || det.Modules[0].Hardware != "RPI-RF-MOD" {
		t.Fatalf("slow header module: %v %+v", fp.calls, det.Modules[0])
	}
	// without the limit: one pass, as upstream - the stick's own retry (B-331) finds it all the same
	fp = &fakeProbe{answers: map[string]string{"raw-uart": "none", "raw-uart1": "flaky:" + stick}}
	d.Run, d.GPIOLimit = fp.run, 0
	det = d.Detect(context.Background())
	if strings.Join(fp.calls, ",") != "raw-uart,raw-uart1,raw-uart1" || !det.Found() || det.SecondPass {
		t.Fatalf("no limit: %v found %v", fp.calls, det.Found())
	}
	// every probe is bounded: a hanging probe ends with a timeout, not a hung boot
	fp = &fakeProbe{answers: map[string]string{"raw-uart": "none", "raw-uart1": stick}, delays: map[string]time.Duration{"raw-uart1": time.Second}}
	d.Run, d.GPIOLimit, d.ProbeLimit = fp.run, 30*time.Millisecond, 50*time.Millisecond
	det = d.Detect(context.Background())
	if det.Modules[1].Probe != "timeout" {
		t.Fatalf("bounded: %+v", det.Modules[1])
	}
	if det.BoardMAC != "dc:a6:32:0f:00:01" {
		t.Fatalf("mac: %q", det.BoardMAC)
	}
}

// openccu-lite B-331: a stick that answers the first probe without an identity - empty, or "did
// not respond correctly" - is reset and probed again even when another module was found, so the
// second pass of D-92 does not run; the HM-CFG-USB-2 and the header's UART are not retried, a
// node that never answers is given up after StickRetries, and a timeout is not retried
func TestDetectStickRetry(t *testing.T) {
	root := sandbox(t, map[string]string{"raw-uart": "GPIO@fe201000.serial", "raw-uart1": "eQ-3 HmIP-RFUSB@usb-1.2", "raw-uart2": "eQ-3 HmIP-RFUSB@usb-1.3"})
	_ = os.MkdirAll(filepath.Join(root, "sys/bus/usb/devices/1-1.4"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sys/bus/usb/devices/1-1.4/idVendor"), []byte("1b1f\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys/bus/usb/devices/1-1.4/idProduct"), []byte("c00f\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys/bus/usb/devices/1-1.4/serial"), []byte("KEQ0123456\n"), 0o644)
	rfusb := "HMIP-RFUSB 0000000A01 3014F711A000040000000A01 0x7F0A16 0x3F0A15 4.4.18"
	tk := "HMIP-RFUSB-TK 0000000A02 3014F711A000040000000A02 0x7F0A17 0x3F0A15 4.2.14"
	for _, c := range []struct {
		name, tk string
		calls    string
		probe    string
		hw       string
		sleeps   int
	}{
		{"empty answer", "empty:" + tk, "raw-uart,raw-uart1,raw-uart2,raw-uart2", "ok", "HMIP-RFUSB-TK", 2},
		{"did not respond correctly", "flaky:" + tk, "raw-uart,raw-uart1,raw-uart2,raw-uart2", "ok", "HMIP-RFUSB-TK", 2},
		{"answers at once", tk, "raw-uart,raw-uart1,raw-uart2", "ok", "HMIP-RFUSB-TK", 1},
		{"never answers", "none", "raw-uart,raw-uart1,raw-uart2,raw-uart2,raw-uart2", "none", "", 3},
	} {
		fp := &fakeProbe{answers: map[string]string{"raw-uart": "none", "raw-uart1": rfusb, "raw-uart2": c.tk}, delays: map[string]time.Duration{"raw-uart": 100 * time.Millisecond}}
		sleeps := 0
		d := Detector{Root: root, Host: "rpi4", Run: fp.run, GPIOLimit: 20 * time.Millisecond, ProbeLimit: time.Second, Sleep: func(time.Duration) { sleeps++ }}
		det := d.Detect(context.Background())
		tkm := det.Modules[2]
		if strings.Join(fp.calls, ",") != c.calls || det.SecondPass || sleeps != c.sleeps || tkm.Probe != c.probe || tkm.Hardware != c.hw {
			t.Fatalf("%s: calls %v sleeps %d second pass %v %+v", c.name, fp.calls, sleeps, det.SecondPass, tkm)
		}
		if c.probe == "ok" && (!det.HasModule("0000000A02") || tkm.Serial != "0000000A02" || tkm.Version != "4.2.14") {
			t.Fatalf("%s: %+v", c.name, tkm)
		}
		if c.sleeps > 1 && !strings.Contains(strings.Join(det.Log, "\n"), "/dev/raw-uart2: no identity in the answer: reset and probing again (1 of 2)") {
			t.Fatalf("%s: log %v", c.name, det.Log)
		}
	}
	// a stick whose probe timed out is not retried: it waited its whole limit already
	fp := &fakeProbe{answers: map[string]string{"raw-uart": "none", "raw-uart1": rfusb, "raw-uart2": tk}, delays: map[string]time.Duration{"raw-uart2": time.Second}}
	d := Detector{Root: root, Host: "rpi4", Run: fp.run, GPIOLimit: 20 * time.Millisecond, ProbeLimit: 50 * time.Millisecond, Sleep: func(time.Duration) {}}
	det := d.Detect(context.Background())
	if strings.Join(fp.calls, ",") != "raw-uart,raw-uart1,raw-uart2" || det.Modules[2].Probe != "timeout" {
		t.Fatalf("timeout: %v %+v", fp.calls, det.Modules[2])
	}
}

func TestUnidentified(t *testing.T) {
	for _, c := range []struct {
		m    Module
		want bool
	}{
		{Module{Probe: "none"}, true},
		{Module{Probe: "error"}, true},
		{Module{Probe: "timeout"}, false},
		{Module{Probe: "ok", Hardware: "HMIP-RFUSB", Version: "0.0.0"}, true},
		{Module{Probe: "ok", Hardware: "HMIP-RFUSB", Serial: "0000000A01", Version: "0.0.0"}, true},
		{Module{Probe: "ok", Hardware: "HMIP-RFUSB", Serial: "0000000A01", Version: "4.4.18"}, false},
		{Module{Probe: "ok", Hardware: "HM-MOD-RPI-PCB", Serial: "0000000A02", SerialFromSGTIN: true, Version: "2.8.6"}, false},
	} {
		if got := c.m.unidentified(); got != c.want {
			t.Errorf("%+v: %v, want %v", c.m, got, c.want)
		}
	}
}

func TestDetectUSBAdapterAndFallback(t *testing.T) {
	root := sandbox(t, nil)
	_ = os.MkdirAll(filepath.Join(root, "sys/bus/usb/devices/1-1.2"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sys/bus/usb/devices/1-1.2/idVendor"), []byte("1b1f\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys/bus/usb/devices/1-1.2/idProduct"), []byte("c00f\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys/bus/usb/devices/1-1.2/serial"), []byte("KEQ0123456\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "dev/ttyAMA0"), nil, 0o644)
	fp := &fakeProbe{answers: map[string]string{"ttyAMA0": "none"}}
	d := Detector{Root: root, Host: "rpi3", Run: fp.run, GPIOLimit: 30 * time.Millisecond, Sleep: func(time.Duration) {}}
	det := d.Detect(context.Background())
	if !det.TTYFallback || len(det.Modules) != 2 || det.Modules[0].Node != "/dev/ttyAMA0" || !det.Modules[1].USBAdapter || det.Modules[1].Serial != "KEQ0123456" || !det.Found() {
		t.Fatalf("%+v", det)
	}
	in := Load(context.Background(), root, fp.run, det)
	if in.Host != "" || in.Mode != "NORMAL" || in.RFDConfExists || in.Arch != "aarch64" {
		t.Fatalf("%+v", in)
	}
}

// occulited B-69: with diagrams off the measurement service's configuration and template paths
// are the empty DiagramsOffPath - their defaults do not exist on lite and each logged an ERR line
// at every start; with diagrams on the image's file stands, the database path aside
func TestHMServerConfDiagramDirs(t *testing.T) {
	base := "hmServerPort=39292\nregaPort=31999\ndiagramDatabasePath=/tmp/measurement\ndiagramConfigPath=/etc/config/measurement"
	off := HMServerConf(base, false)
	want := "hmServerPort=39292\nregaPort=31999\ndiagramDatabasePath=" + DiagramPath + "\ndiagramConfigPath=" + DiagramsOffPath + "\ndiagramTemplatePath=" + DiagramsOffPath + "\n"
	if off != want {
		t.Fatalf("off:\n%s\nwant:\n%s", off, want)
	}
	if on := HMServerConf(base, true); on != "hmServerPort=39292\nregaPort=31999\ndiagramDatabasePath="+DiagramPath+"\ndiagramConfigPath=/etc/config/measurement" {
		t.Fatalf("on: %q", on)
	}
	if HMServerConf("", false) != "diagramConfigPath="+DiagramsOffPath+"\ndiagramTemplatePath="+DiagramsOffPath+"\n" {
		t.Fatalf("empty base: %q", HMServerConf("", false))
	}
	// Render follows the Detector's setting through the inputs
	in := inputs(rpiRFMod("/dev/raw-uart"))
	p := MakePlan(in)
	if f := Render(in, p); !strings.Contains(f.HMServerConf, "diagramTemplatePath="+DiagramsOffPath) {
		t.Fatalf("render off: %s", f.HMServerConf)
	}
	in.Diagrams = true
	if f := Render(in, p); strings.Contains(f.HMServerConf, DiagramsOffPath) {
		t.Fatalf("render on: %s", f.HMServerConf)
	}
}
