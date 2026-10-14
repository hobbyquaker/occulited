package radio

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// systemctlCalls are the stop and start calls a hotplug made, in order.
func systemctlCalls(rec *recorder) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []string
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "systemctl stop") || strings.HasPrefix(c, "systemctl start") {
			out = append(out, c)
		}
	}
	return out
}

func resetCalls(rec *recorder) {
	rec.mu.Lock()
	rec.calls = nil
	rec.mu.Unlock()
}

func plug(t *testing.T, root, name, typ string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Join(root, "sys/class/raw-uart", name), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sys/class/raw-uart", name, "device_type"), []byte(typ+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys/class/raw-uart", name, "reset_radio_module"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(root, "dev", name), nil, 0o644)
}

func unplug(root, name string) {
	_ = os.RemoveAll(filepath.Join(root, "sys/class/raw-uart", name))
	_ = os.Remove(filepath.Join(root, "dev", name))
}

func TestHotplugTKStickPulledAndBack(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart1": "eQ-3 HmIP-RFUSB-TK@usb-1"})
	rec.probe.answers = map[string]string{"raw-uart1": "HMIP-RFUSB-TK 0001TK0001 3014F711A0000000001TK0001 0x000000 0x7F0A17 2.8.6"}
	d := Detector{Root: root, Run: rec.run, Sleep: func(time.Duration) {}}
	logf := func(string, ...any) {}
	ctx := context.Background()
	if _, err := Run(ctx, root, d, logf); err != nil {
		t.Fatal(err)
	}
	// nothing changed: nothing restarted, the stick not probed again (a daemon holds it)
	resetCalls(rec)
	rep, err := Hotplug(ctx, root, d, 0, logf)
	if err != nil || rep.Changed || len(systemctlCalls(rec)) != 0 || rec.probe.seen["raw-uart1"] != 1 {
		t.Fatalf("unchanged: %+v %v %v probes %d", rep, err, systemctlCalls(rec), rec.probe.seen["raw-uart1"])
	}
	// pulled: hmipserver alone restarts, on its VirtualDevices half
	unplug(root, "raw-uart1")
	rep, err = Hotplug(ctx, root, d, 0, logf)
	if err != nil || !rep.Changed || !reflect.DeepEqual(rep.Restarted, []string{"hmipserver"}) {
		t.Fatalf("pulled: %+v %v", rep, err)
	}
	if got := systemctlCalls(rec); !reflect.DeepEqual(got, []string{"systemctl stop -- hmipserver.service", "systemctl start -- hmipserver.service"}) {
		t.Fatalf("units: %v", got)
	}
	env := readFile(filepath.Join(root, "run/occulite/radio/hmipserver.env"))
	if !strings.Contains(env, "HMIP_CLASS=de.eq3.ccu.server.HMServer") {
		t.Fatalf("hmipserver without the stick:\n%s", env)
	}
	// back: probed once more (a new node), hmipserver restarts on it
	resetCalls(rec)
	plug(t, root, "raw-uart1", "eQ-3 HmIP-RFUSB-TK@usb-1")
	rep, err = Hotplug(ctx, root, d, 0, logf)
	if err != nil || !reflect.DeepEqual(rep.Restarted, []string{"hmipserver"}) || rec.probe.seen["raw-uart1"] != 2 {
		t.Fatalf("back: %+v %v probes %d", rep, err, rec.probe.seen["raw-uart1"])
	}
	if env = readFile(filepath.Join(root, "run/occulite/radio/hmipserver.env")); !strings.Contains(env, "HMIP_DEVNODE=/dev/raw-uart1") {
		t.Fatalf("hmipserver on the stick again:\n%s", env)
	}
}

func TestHotplugSecondStickAndAdapter(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart": "GPIO@3f201000.serial"})
	d := Detector{Root: root, Run: rec.run, Sleep: func(time.Duration) {}}
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, f) }
	ctx := context.Background()
	if _, err := Run(ctx, root, d, logf); err != nil {
		t.Fatal(err)
	}
	// a second stick nothing uses (auto keeps both stacks on the RPI-RF-MOD): the results change,
	// no daemon restarts, and the page offers the stick
	resetCalls(rec)
	rec.probe.answers["raw-uart1"] = "HMIP-RFUSB 0000000A01 3014F711A000040000000A01 0x000000 0x7F0A16 4.4.18"
	plug(t, root, "raw-uart1", "eQ-3 HmIP-RFUSB@usb-1")
	rep, err := Hotplug(ctx, root, d, 0, logf)
	if err != nil || !rep.Changed || len(rep.Restarted) != 0 || len(systemctlCalls(rec)) != 0 {
		t.Fatalf("second stick: %+v %v %v", rep, err, systemctlCalls(rec))
	}
	var det Detection
	if err := readJSONFile(filepath.Join(root, "run/occulite/radio/modules.json"), &det); err != nil || len(det.Modules) != 2 || rec.probe.seen["raw-uart"] != 1 {
		t.Fatalf("modules.json: %+v %v probes of the held module %d", det.Modules, err, rec.probe.seen["raw-uart"])
	}
	// an HM-CFG-USB-2 takes BidCos-RF (auto): multimacd goes, rfd moves to it, hmipserver opens the
	// module directly - all three restart
	usb := filepath.Join(root, "sys/bus/usb/devices/1-1.4")
	_ = os.MkdirAll(usb, 0o755)
	_ = os.WriteFile(filepath.Join(usb, "idVendor"), []byte("1b1f\n"), 0o644)
	_ = os.WriteFile(filepath.Join(usb, "idProduct"), []byte("c00f\n"), 0o644)
	_ = os.WriteFile(filepath.Join(usb, "serial"), []byte("KEQ0123456\n"), 0o644)
	rep, err = Hotplug(ctx, root, d, 0, logf)
	if err != nil || !reflect.DeepEqual(rep.Restarted, []string{"multimacd", "rfd", "hmipserver"}) {
		t.Fatalf("adapter: %+v %v", rep, err)
	}
	want := []string{"systemctl stop -- hmipserver.service", "systemctl stop -- rfd.service", "systemctl stop -- multimacd.service", "systemctl start -- multimacd.service", "systemctl start -- rfd.service", "systemctl start -- hmipserver.service"}
	if got := systemctlCalls(rec); !reflect.DeepEqual(got, want) {
		t.Fatalf("units: %v", got)
	}
	if exists(filepath.Join(root, "run/occulite/radio/multimacd.enabled")) || !strings.Contains(readFile(filepath.Join(root, "etc/config/rfd.conf")), "Type = USB Interface") {
		t.Fatal("multimacd still enabled, or rfd.conf without the adapter")
	}
}

func TestAffected(t *testing.T) {
	dual := MakePlan(inputs(rpiRFMod("/dev/raw-uart")))
	direct := func() Plan {
		in := inputs(rpiRFMod("/dev/raw-uart"))
		in.RFDConf, in.RFDConfExists = SetBidCosChoice(rfdTemplate, BidCosNone), true
		return MakePlan(in)
	}()
	if got := Affected(dual, dual); len(got) != 0 {
		t.Fatalf("same plan: %v", got)
	}
	if got := Affected(dual, direct); !reflect.DeepEqual(got, []string{"multimacd", "rfd", "hmipserver"}) {
		t.Fatalf("dual to direct: %v", got)
	}
	if !SameModules([]Module{rpiRFMod("/dev/raw-uart"), rfusb("/dev/raw-uart1")}, []Module{rfusb("/dev/raw-uart1"), rpiRFMod("/dev/raw-uart")}) {
		t.Fatal("the order does not matter")
	}
}

func TestHotplugStandsAsideDuringAFlash(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart1": "eQ-3 HmIP-RFUSB-TK@usb-1"})
	rec.probe.answers = map[string]string{"raw-uart1": "HMIP-RFUSB-TK 0001TK0001 3014F711A0000000001TK0001 0x000000 0x7F0A17 2.8.6"}
	d := Detector{Root: root, Run: rec.run, Sleep: func(time.Duration) {}}
	logf := func(string, ...any) {}
	if _, err := Run(context.Background(), root, d, logf); err != nil {
		t.Fatal(err)
	}
	busy := filepath.Join(root, BusyFiles[0])
	_ = os.MkdirAll(filepath.Dir(busy), 0o755)
	_ = os.WriteFile(busy, []byte("{}"), 0o600)
	resetCalls(rec)
	unplug(root, "raw-uart1")
	if rep, err := Hotplug(context.Background(), root, d, 0, logf); err != nil || rep.Changed || len(systemctlCalls(rec)) != 0 {
		t.Fatalf("during a flash: %+v %v %v", rep, err, systemctlCalls(rec))
	}
}

// a stick pulled while multimacd held its node: the node stays with connection_state 0 and counts
// as gone - .170's HB-RF-USB with the PCB swapped for a TK, where the rescan kept the PCB
func TestHotplugHeldNodeOfAPulledStick(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart1": "HB-RF-USB@usb-0000:01:00.0-1.3"})
	rec.probe.answers = map[string]string{
		"raw-uart1": "HM-MOD-RPI-PCB MEQ9000005 3014F711A061A70000000A06 0x3D0A01 0x1E0A02 2.8.6",
		"raw-uart2": "HMIP-RFUSB-TK 0000000A09 3014F5AC9400040000000A09 0x000000 0x7F0A17 4.2.14",
	}
	d := Detector{Root: root, Run: rec.run, Sleep: func(time.Duration) {}}
	logf := func(string, ...any) {}
	ctx := context.Background()
	if _, err := Run(ctx, root, d, logf); err != nil {
		t.Fatal(err)
	}
	resetCalls(rec)
	_ = os.WriteFile(filepath.Join(root, "sys/class/raw-uart/raw-uart1/connection_state"), []byte("0\n"), 0o644)
	plug(t, root, "raw-uart2", "eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3")
	rep, err := Hotplug(ctx, root, d, 0, logf)
	if err != nil || !reflect.DeepEqual(rep.Restarted, []string{"multimacd", "rfd", "hmipserver"}) {
		t.Fatalf("%+v %v", rep, err)
	}
	// openccu-lite task 318 (D-120): on Automatic HmIP-RF stays with the PCB, which holds the HmIP
	// network - the TK in its place is not taken unasked (hmipserver would attempt the adapter
	// exchange): the VirtualDevices half alone, the PCB named as missing
	env := readFile(filepath.Join(root, "run/occulite/radio/hmipserver.env"))
	p, _ := LoadPlan(root)
	if !strings.Contains(env, "HMIP_DEVNODE=\n") || p.HmIP != nil || p.MissingHmIP != "3014F711A061A70000000A06" || p.HmIPPin != p.MissingHmIP {
		t.Fatalf("HmIP-RF moved to the TK unasked:\n%s\n%+v", env, p)
	}
	// the move as the user's choice: hmipserver on the TK directly, no multimacd, and the record
	// follows it
	_ = os.MkdirAll(filepath.Join(root, "etc/config/crRFD"), 0o755)
	if err := os.WriteFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"), []byte(SetHmIPChoice("", "0000000A09")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, root, d, logf); err != nil {
		t.Fatal(err)
	}
	env = readFile(filepath.Join(root, "run/occulite/radio/hmipserver.env"))
	if !strings.Contains(env, "HMIP_DEVNODE=/dev/raw-uart2") || exists(filepath.Join(root, "run/occulite/radio/multimacd.enabled")) {
		p, _ = LoadPlan(root)
		t.Fatalf("hmipserver on the TK directly, no multimacd:\n%s\n%v", env, p.Notes)
	}
	if pin := ReadHmIPPin(root); pin == nil || pin.SGTIN != "3014F5AC9400040000000A09" {
		t.Fatalf("the record: %+v", pin)
	}
}

// D-102: hmipserver's ready step fails at once on a known fatal line in its own output and leaves
// the marker that keeps the unit from restarting; the next run clears it
func TestReadyHmIPServerFailsFastOnAKnownError(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart": "GPIO@3f201000.serial"})
	d := Detector{Root: root, Run: rec.run, Sleep: func(time.Duration) {}}
	logf := func(string, ...any) {}
	if _, err := Run(context.Background(), root, d, logf); err != nil {
		t.Fatal(err)
	}
	// openccu-lite B-284: the ready step runs inside hmipserver's unit with its UMask=0077, and
	// the marker came out 0600 root - unreadable for the daemon, which then saw no rejection
	oldUmask := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(oldUmask) })
	rec.answers["journalctl"] = "Init Hardware Info\nde.eq3.cbcs.server.local.base.internal.HMIPTRXInitialResponseListener [vert.x-eventloop-thread-0] Adapter exchange was rejected by key server.\n"
	err := Ready(context.Background(), d, "hmipserver", 4242, logf)
	if err == nil || !strings.Contains(err.Error(), "adapter-exchange-rejected") {
		t.Fatalf("ready: %v", err)
	}
	if !rec.called("journalctl _PID=4242") {
		t.Fatalf("the main process's output was not read: %v", rec.calls)
	}
	f := ReadHmIPFatal(root)
	if f == nil || f.Code != "adapter-exchange-rejected" || f.Adapter != "3014F711A0001F0000000A03" || !strings.Contains(f.Line, "rejected by key server") || f.Cause != CauseRefused {
		t.Fatalf("marker: %+v", f)
	}
	// the daemon reads the marker as its own user: world-readable whatever the unit's umask
	if st, err := os.Stat(FatalPath(root)); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("marker mode: %v %v", err, st)
	}
	// a marker the daemon cannot read is no marker to it - the shape of the bug
	if err := os.Chmod(FatalPath(root), 0); err == nil && os.Getuid() != 0 {
		if ReadHmIPFatal(root) != nil {
			t.Fatal("a closed marker was read")
		}
		_ = os.Chmod(FatalPath(root), 0o644)
	}
	// the next run of the stack clears it
	rec.answers["systemctl"] = "inactive\ninactive\ninactive\ninactive\ninactive\n"
	if _, err := Run(context.Background(), root, d, logf); err != nil {
		t.Fatal(err)
	}
	if ReadHmIPFatal(root) != nil {
		t.Fatal("the marker survived a new run")
	}
	if code, _, _ := hmipFatal("Init Hardware Info\nstarted\n"); code != "" {
		t.Fatalf("a normal start is fatal: %s", code)
	}
}

// B-218: after a power cycle of the board the kernel's reconnect never took it back (the lab,
// 14:43-14:56); the watch's hotplug connects it afresh and restarts the daemons on it, whose module
// was reset under them. The same loss without the watch's note (a udev hotplug) is the kernel's.
func TestHotplugBoardPowerCycle(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart1": "HB-RF-ETH@192.0.2.10"})
	rec.probe.answers = map[string]string{"raw-uart1": "HM-MOD-RPI-PCB MEQ9000005 3014F711A061A70000000A06 0x3D0A01 0xB40A03 2.8.6"}
	for p, c := range map[string]string{
		"etc/config/hb_rf_eth":                          "192.0.2.10\n",
		"sys/module/hb_rf_eth/parameters/connect":       "",
		"sys/module/hb_rf_eth/parameters/autoreconnect": "1\n",
		"sys/class/hb-rf-eth/hb-rf-eth/connect":         "",
		HBRFETHConnectedFile[1:]:                        "1\n",
		"usr/local/etc/occulite/.keep":                  "",
	} {
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)
	}
	d := Detector{Root: root, Run: rec.run, Sleep: func(time.Duration) {}}
	logf := func(string, ...any) {}
	ctx := context.Background()
	if _, err := Run(ctx, root, d, logf); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPlan(root)
	if _, _, daemons := p.OnHBRFETH(); err != nil || len(daemons) == 0 {
		t.Fatalf("no daemons on the board in the plan: %v %+v", err, p)
	}
	_, _, want := p.OnHBRFETH()
	// the board is off: the kernel reconnects, the node is held with connection_state 0
	_ = os.WriteFile(filepath.Join(root, HBRFETHConnectedFile), []byte("0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys/class/raw-uart/raw-uart1/connection_state"), []byte("0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sys/class/hb-rf-eth/hb-rf-eth/connect"), nil, 0o644)
	resetCalls(rec)
	rep, err := Hotplug(ctx, root, d, 0, logf)
	if err != nil || len(rep.Restarted) != 0 {
		t.Fatalf("a udev hotplug during the kernel's reconnect: %+v %v", rep, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "sys/class/hb-rf-eth/hb-rf-eth/connect")); len(b) != 0 {
		t.Fatalf("the address was written: %q", b)
	}
	// the kernel did not get it back: the watch's note, a fresh connect, the daemons restarted
	_ = os.WriteFile(filepath.Join(root, HBRFETHReconnectFile), []byte("192.0.2.10\n"), 0o644)
	resetCalls(rec)
	rep, err = Hotplug(ctx, root, d, 0, logf)
	if err != nil || !reflect.DeepEqual(rep.Restarted, want) {
		t.Fatalf("after the power cycle: %+v %v, want %v", rep, err, want)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "sys/class/hb-rf-eth/hb-rf-eth/connect")); string(b) != "192.0.2.10" {
		t.Fatalf("not connected afresh: %q", b)
	}
	calls := strings.Join(systemctlCalls(rec), ";")
	if !strings.HasPrefix(calls, "systemctl stop -- "+want[len(want)-1]+".service") || !strings.HasSuffix(calls, "systemctl start -- "+want[len(want)-1]+".service") {
		t.Errorf("order: %s", calls)
	}
}

// openccu-lite B-272: both processes pinned to an HmIP-RFUSB, then an HB-RF-ETH added under LAN
// devices (the address written, the watch's hotplug). The hotplug attaches the board and probes its
// module; the plan stays on the stick and no daemon restarts, but the detection lists the board's
// module and offers it for both processes.
func TestHotplugBoardAddedWhilePinned(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart": "eQ-3 HmIP-RFUSB@usb-1"})
	rec.probe.answers = map[string]string{"raw-uart": "HMIP-RFUSB 0000000A01 3014F711A000040000000A01 0xFF0001 0xB00001 4.4.18"}
	for p, c := range map[string]string{
		"etc/config/rfd.conf":                     "# occulite.bidcos.module=0000000A01\n" + rfdTemplate,
		"etc/config/crRFD/hmip_user.conf":         "occulite.hmip.adapter=0000000A01\nocculite.hmip.path=multimacd\n",
		"sys/module/hb_rf_eth/parameters/connect": "",
		"sys/class/hb-rf-eth/hb-rf-eth/connect":   "",
		HBRFETHConnectedFile[1:]:                  "0\n",
	} {
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)
	}
	d := Detector{Root: root, Run: rec.run, Sleep: func(time.Duration) {}}
	logf := func(string, ...any) {}
	ctx := context.Background()
	if _, err := Run(ctx, root, d, logf); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPlan(root)
	if err != nil || p.HmRF == nil || p.HmRF.Serial != "0000000A01" || p.HmIP == nil || p.HmIP.Serial != "0000000A01" || p.Multimacd.Node != "/dev/raw-uart" {
		t.Fatalf("the pin: %v %+v", err, p)
	}
	// the board added: its address configured, the kernel's node appears once connected
	_ = os.WriteFile(filepath.Join(root, "etc/config/hb_rf_eth"), []byte("192.0.2.50\n"), 0o644)
	rec.probe.answers["raw-uart1"] = "HM-MOD-RPI-PCB MEQ9000005 3014F711A061A70000000A05 0x3D0A01 0xB40A03 2.8.6"
	plug(t, root, "raw-uart1", "HB-RF-ETH@192.0.2.50")
	resetCalls(rec)
	rep, err := Hotplug(ctx, root, d, 0, logf)
	if err != nil || !rep.Changed || len(rep.Restarted) != 0 || len(systemctlCalls(rec)) != 0 {
		t.Fatalf("the board added: %+v %v %v", rep, err, systemctlCalls(rec))
	}
	if b, _ := os.ReadFile(filepath.Join(root, "sys/class/hb-rf-eth/hb-rf-eth/connect")); string(b) != "192.0.2.50" {
		t.Fatalf("the board was not connected: %q", b)
	}
	var det Detection
	if err := readJSONFile(filepath.Join(root, "run/occulite/radio/modules.json"), &det); err != nil || len(det.Modules) != 2 || det.HBRFETH != "192.0.2.50" || !det.HBRFETHConnected {
		t.Fatalf("modules.json: %+v %v", det, err)
	}
	if det.Modules[1].Serial != "MEQ9000005" || det.Modules[1].DeviceType != "HB-RF-ETH@192.0.2.50" || !det.Modules[1].OK() || rec.probe.seen["raw-uart"] != 1 {
		t.Fatalf("the board's module: %+v (probes of the held stick %d)", det.Modules[1], rec.probe.seen["raw-uart"])
	}
	if p, err = LoadPlan(root); err != nil || p.HmRF.Serial != "0000000A01" || p.HmIP.Serial != "0000000A01" || p.Multimacd.Node != "/dev/raw-uart" {
		t.Fatalf("the plan left the stick: %v %+v", err, p)
	}
	o := ChoiceOptions(det)
	if len(o.HmIP) != 2 || o.HmIP[1].ID != "MEQ9000005" || !reflect.DeepEqual(o.HmIP[1].Paths, []string{PathMultimacd}) || len(o.BidCos) != 2 || o.BidCos[1].ID != "MEQ9000005" {
		t.Fatalf("the board's module is not offered: %+v", o)
	}
}
