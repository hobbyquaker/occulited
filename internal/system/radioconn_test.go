package system

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/radio"
)

const connRFDConf = "# TCP Port for XmlRpc connections\nListen IP = 127.0.0.1\nListen Port = 32001\n\nLog Destination = Syslog\nKey File = /etc/config/keys\nImproved Coprocessor Initialization = true\n\n[Interface 0]\nType = CCU2\nComPortFile = /dev/mmd_bidcos\nAccessFile = /dev/null\nResetFile = /dev/null\n"

// charlyRoot is the Charly as the service sees it after a boot: the RPI-RF-MOD on the header
// serving both stacks, the run's modules.json and plan.json under /run/occulite/radio.
func charlyRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mod := radio.Module{Name: "raw-uart", Node: "/dev/raw-uart", DeviceType: "GPIO@3f201000.serial", GPIO: true, Hardware: "RPI-RF-MOD", Serial: "0000000A03", SGTIN: "3014F711A0001F0000000A03", HmRFAddress: "0x1F0A14", HmIPAddress: "0x3F0A15", Version: "4.4.22", Probe: "ok"}
	det := radio.Detection{Modules: []radio.Module{mod}}
	w("var/hm_mode", "HM_HOST='rpi3'\nHM_MODE='NORMAL'\n")
	w("etc/config/rfd.conf", connRFDConf)
	w("etc/config_templates/rfd.conf", connRFDConf)
	w("etc/config_templates/InterfacesList.xml", "<interfaces><ipc><name>BidCos-RF</name><url>xmlrpc_bin://127.0.0.1:32001</url><info>BidCos-RF</info></ipc><ipc><name>VirtualDevices</name><url>xmlrpc://127.0.0.1:39292/groups</url><info>Virtual Devices</info></ipc><ipc><name>HmIP-RF</name><url>xmlrpc://127.0.0.1:32010</url><info>HmIP-RF</info></ipc></interfaces>")
	b, _ := json.Marshal(det)
	w("run/occulite/radio/modules.json", string(b))
	in := radio.Load(context.Background(), root, fakeRunner, det)
	b, _ = json.Marshal(radio.MakePlan(in))
	w("run/occulite/radio/plan.json", string(b))
	return root
}

func fakeRunner(_ context.Context, name string, _ ...string) ([]byte, error) {
	if name == "uname" {
		return []byte("aarch64\n"), nil
	}
	return []byte("ccu\n"), nil
}

// connSvc records the unit calls; the detection unit's restart re-plans the sandbox as the real
// `occulited radio run` would, from the files the change wrote.
type connSvc struct {
	fakeRadioSvc
	onDetect func()
}

func (c *connSvc) Control(ctx context.Context, id, action string) (string, error) {
	if id == RadioDetectionUnit && action == "restart" && c.onDetect != nil {
		c.onDetect()
	}
	return c.fakeRadioSvc.Control(ctx, id, action)
}

func newConn(t *testing.T, root string, devices []BidCosDevice) (*RadioConnections, *connSvc) {
	t.Helper()
	svc := &connSvc{fakeRadioSvc: fakeRadioSvc{root: root, running: map[string]bool{"multimacd": true, "rfd": true, "hmipserver": true}}}
	s := &RadioConnections{Root: Root(root), Services: svc, Systemd: true, Run: fakeRunner, StateDir: filepath.Join(root, "state/radio-connections"), Timeout: 5 * time.Second}
	s.BidCosDevices = func(context.Context) ([]BidCosDevice, error) { return devices, nil }
	svc.onDetect = func() {
		det, _ := s.detection()
		in := radio.Load(context.Background(), root, fakeRunner, det)
		b, _ := json.Marshal(radio.MakePlan(in))
		_ = os.WriteFile(filepath.Join(root, "run/occulite/radio/plan.json"), b, 0o644)
	}
	return s, svc
}

func waitApply(t *testing.T, s *RadioConnections) *ConnApply {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := s.Status(); st.Running == nil && st.Last != nil {
			return st.Last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the change did not finish")
	return nil
}

func TestRadioConnStatus(t *testing.T) {
	root := charlyRoot(t)
	s, _ := newConn(t, root, nil)
	st := s.Status()
	if !st.Available || st.Choices.Explicit() || st.Plan == nil || !st.Plan.Multimacd.Run || st.Plan.HmIPServer.Node != "/dev/mmd_hmip" || !st.Plan.HmIPAdvanced {
		t.Fatalf("status: %+v %+v", st, st.Plan)
	}
	if len(st.Options.HmIP) != 1 || len(st.Options.BidCos) != 1 || st.Options.HmIP[0].ID != "0000000A03" {
		t.Fatalf("options: %+v", st.Options)
	}
	// no plan: not available, nothing to choose
	_ = os.Remove(filepath.Join(root, "run/occulite/radio/plan.json"))
	if st = s.Status(); st.Available || st.Plan != nil {
		t.Fatalf("without a plan: %+v", st)
	}
}

func TestRadioConnPreviewAndValidate(t *testing.T) {
	root := charlyRoot(t)
	devs := []BidCosDevice{{Address: "JEQ9000001", Type: "HM-CC-TC"}}
	s, _ := newConn(t, root, devs)
	ctx := context.Background()
	pv, err := s.Preview(ctx, radio.Choices{BidCos: radio.BidCosNone})
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Changed || !pv.BidCosLost || !reflect.DeepEqual(pv.Devices, devs) || pv.Plan.RFD.Run || pv.Plan.Multimacd.Run || pv.Plan.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("HmIP-direct preview: %+v", pv)
	}
	if !reflect.DeepEqual(pv.Restarts, []string{"multimacd", "rfd", "hmipserver"}) {
		t.Fatalf("restarts: %v", pv.Restarts)
	}
	if pv, err = s.Preview(ctx, radio.Choices{}); err != nil || pv.Changed || pv.BidCosLost {
		t.Fatalf("auto on an auto box changes nothing: %+v %v", pv, err)
	}
	// pinning the module auto chose anyway: the choice changes, the stack does not
	if pv, err = s.Preview(ctx, radio.Choices{HmIP: "0000000A03"}); err != nil || !pv.Changed || pv.Plan.HmIPServer.Node != "/dev/mmd_hmip" {
		t.Fatalf("pinned to the same module: %+v %v", pv, err)
	}
	for _, c := range []radio.Choices{{HmIP: radio.BidCosNone}, {HmIP: "0000000000"}, {BidCos: "../etc"}, {BidCos: "KEQ0000000"}} {
		if _, err := s.Preview(ctx, c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

func TestRadioConnApplyHmIPDirectAndBack(t *testing.T) {
	root := charlyRoot(t)
	devs := []BidCosDevice{{Address: "JEQ9000001", Type: "HM-CC-TC"}}
	s, svc := newConn(t, root, devs)
	ctx := context.Background()
	// paired devices: refused without confirm, nothing written
	_, err := s.Apply(ctx, radio.Choices{BidCos: radio.BidCosNone}, false)
	var cr *ConfirmRequired
	if !errors.As(err, &cr) || len(cr.Devices) != 1 {
		t.Fatalf("confirm required: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/config/rfd.conf")); string(b) != connRFDConf {
		t.Fatalf("rfd.conf written without confirm:\n%s", b)
	}
	if _, err := s.Apply(ctx, radio.Choices{BidCos: radio.BidCosNone}, true); err != nil {
		t.Fatal(err)
	}
	a := waitApply(t, s)
	if !a.OK {
		t.Fatalf("the change failed: %+v", a)
	}
	b, _ := os.ReadFile(filepath.Join(root, "etc/config/rfd.conf"))
	if !strings.HasPrefix(string(b), "# occulite.bidcos.module=none\n") {
		t.Fatalf("rfd.conf:\n%s", b)
	}
	want := []string{"hmipserver stop", "rfd stop", "multimacd stop", RadioDetectionUnit + " restart", "multimacd start", "rfd start", "hmipserver start"}
	if got := svc.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("units:\n%v\nwant\n%v", got, want)
	}
	st := s.Status()
	if st.Choices.BidCos != radio.BidCosNone || st.Plan.RFD.Run || st.Plan.HmIPServer.Node != "/dev/raw-uart" {
		t.Fatalf("after: %+v %+v", st.Choices, st.Plan)
	}
	if !strings.Contains(strings.Join(a.Lines, "\n"), "now: multimacd off, rfd off, hmipserver on /dev/raw-uart") {
		t.Fatalf("lines: %v", a.Lines)
	}
	if _, err := os.Stat(filepath.Join(s.StateDir, "running.json")); err == nil {
		t.Fatal("running.json left behind")
	}
	// back to auto: no BidCos-RF is lost, no confirm needed
	if _, err := s.Apply(ctx, radio.Choices{}, false); err != nil {
		t.Fatal(err)
	}
	if a = waitApply(t, s); !a.OK {
		t.Fatalf("back: %+v", a)
	}
	b, _ = os.ReadFile(filepath.Join(root, "etc/config/rfd.conf"))
	if strings.Contains(string(b), "occulite.bidcos.module") {
		t.Fatalf("the marker stays:\n%s", b)
	}
	if st = s.Status(); !st.Plan.Multimacd.Run || st.Plan.HmIPServer.Node != "/dev/mmd_hmip" {
		t.Fatalf("dual stack again: %+v", st.Plan)
	}
}

func TestRadioConnApplyHmIPChoiceFile(t *testing.T) {
	root := charlyRoot(t)
	s, _ := newConn(t, root, nil)
	hu := filepath.Join(root, "etc/config/crRFD/hmip_user.conf")
	_ = os.MkdirAll(filepath.Dir(hu), 0o755)
	_ = os.WriteFile(hu, []byte("Legacy.VirtualRemoteControl.Enabled=false\n"), 0o644)
	if _, err := s.Apply(context.Background(), radio.Choices{HmIP: "3014F711A0001F0000000A03"}, false); err != nil {
		t.Fatal(err)
	}
	if a := waitApply(t, s); !a.OK {
		t.Fatalf("%+v", a)
	}
	b, _ := os.ReadFile(hu)
	if string(b) != "Legacy.VirtualRemoteControl.Enabled=false\noccu"+"lite.hmip.adapter=3014F711A0001F0000000A03\n" {
		t.Fatalf("hmip_user.conf: %q", b)
	}
}

func TestRadioConnApplyFailureStartsTheStack(t *testing.T) {
	root := charlyRoot(t)
	s, svc := newConn(t, root, nil)
	svc.failing = RadioDetectionUnit
	if _, err := s.Apply(context.Background(), radio.Choices{HmIP: "0000000A03"}, false); err != nil {
		t.Fatal(err)
	}
	a := waitApply(t, s)
	if a.OK || !strings.Contains(a.Error, "the detection and plan did not run") {
		t.Fatalf("%+v", a)
	}
	calls := svc.snapshot()
	if calls[len(calls)-1] != "hmipserver start" || calls[len(calls)-3] != "multimacd start" {
		t.Fatalf("the stack is started after a failed run: %v", calls)
	}
}

func TestRadioConnBusyAndLoad(t *testing.T) {
	root := charlyRoot(t)
	s, svc := newConn(t, root, nil)
	fw := &RadioFirmware{Root: Root(root), StateDir: filepath.Join(root, "state/radio-firmware")}
	fw.running = &FlashAttempt{Module: "RPI-RF-MOD"}
	s.Firmware = fw
	if _, err := s.Apply(context.Background(), radio.Choices{HmIP: "0000000A03"}, false); !errors.Is(err, ErrConnApplyRunning) {
		t.Fatalf("a change during a flash: %v", err)
	}
	fw.running = nil
	fw.ConnBusy = func() bool { return true }
	if err := fw.Flash("RPI-RF-MOD", "x.eq3"); !errors.Is(err, ErrConnApplyRunning) {
		t.Fatalf("a flash during a change: %v", err)
	}
	// a change cut off by a restart of occulited: closed as failed, the stack started
	_ = writeJSONFile(filepath.Join(s.StateDir, "running.json"), &ConnApply{Choices: radio.Choices{BidCos: radio.BidCosNone}, Started: time.Now()})
	s.Load(context.Background())
	st := s.Status()
	if st.Last == nil || st.Last.OK || !strings.Contains(st.Last.Error, "restarted") {
		t.Fatalf("last: %+v", st.Last)
	}
	want := []string{RadioDetectionUnit + " start", "multimacd start", "rfd start", "hmipserver start"}
	if got := svc.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("units: %v", got)
	}
}

func TestRadioConnServiceNotes(t *testing.T) {
	root := charlyRoot(t)
	s, _ := newConn(t, root, nil)
	list := []Service{{ID: "hs485d", Skipped: true}, {ID: "rfd", Running: true}, {ID: "lighttpd", Skipped: true}}
	list = s.OverlayServiceNotes(list)
	if list[0].Note != "disabled: no wired interface configured" || list[1].Note != "" || list[2].Note != "" {
		t.Fatalf("notes: %+v", list)
	}
	var none *RadioConnections
	if got := none.OverlayServiceNotes(list); len(got) != 3 {
		t.Fatal("a box without the service keeps its list")
	}
}

// closedDirPriv is the privilege helper for a directory closed to the daemon, as hmipserver's
// crRFD is since B-253 (0750 hmipserver): the daemon's own reads fail with EACCES there, the
// helper (root) reads and writes. The test opens the directory for the helper's call only.
type closedDirPriv struct {
	priv.Local
	dir      string
	failRead bool
}

func (p closedDirPriv) open(fn func() error) error {
	if err := os.Chmod(p.dir, 0o755); err != nil {
		return err
	}
	defer os.Chmod(p.dir, 0) //nolint:errcheck
	return fn()
}

func (p closedDirPriv) ReadFile(path string) (b []byte, err error) {
	if p.failRead {
		return nil, errors.New("privilege helper: connection refused")
	}
	err = p.open(func() error { b, err = os.ReadFile(path); return err })
	return b, err
}

func (p closedDirPriv) WriteFile(path string, data []byte, mode os.FileMode) error {
	return p.open(func() error { return p.Local.WriteFile(path, data, mode) })
}

// rfusbRoot is an x86_64 system with one HmIP-RFUSB and nothing else, hmip_user.conf in a
// crRFD directory closed to the daemon, carrying the local key's lines.
func rfusbRoot(t *testing.T) (root string, p closedDirPriv) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a closed directory")
	}
	root = t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mod := radio.Module{Name: "raw-uart", Node: "/dev/raw-uart", DeviceType: "eQ-3 HmIP-RFUSB@usb-0000:00:14.0-1", Hardware: "HMIP-RFUSB", Serial: "0000000A01", SGTIN: "3014F711A000040000000A01", HmRFAddress: "0xFF0001", HmIPAddress: "0xB00001", Version: "4.4.18", Probe: "ok"}
	det := radio.Detection{Modules: []radio.Module{mod}}
	w("var/hm_mode", "HM_HOST='ova'\nHM_MODE='NORMAL'\n")
	w("etc/config/rfd.conf", connRFDConf)
	w("etc/config_templates/rfd.conf", connRFDConf)
	w("etc/config_templates/InterfacesList.xml", "<interfaces><ipc><name>BidCos-RF</name><url>xmlrpc_bin://127.0.0.1:32001</url><info>BidCos-RF</info></ipc><ipc><name>VirtualDevices</name><url>xmlrpc://127.0.0.1:39292/groups</url><info>Virtual Devices</info></ipc><ipc><name>HmIP-RF</name><url>xmlrpc://127.0.0.1:32010</url><info>HmIP-RF</info></ipc></interfaces>")
	w("etc/config/crRFD/hmip_user.conf", rfusbUserConf)
	b, _ := json.Marshal(det)
	w("run/occulite/radio/modules.json", string(b))
	in := radio.Load(context.Background(), root, fakeRunner, det)
	b, _ = json.Marshal(radio.MakePlan(in))
	w("run/occulite/radio/plan.json", string(b))
	p = closedDirPriv{dir: filepath.Join(root, "etc/config/crRFD")}
	if err := os.Chmod(p.dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p.dir, 0o755) })
	return root, p
}

// the local key's lines (example keys) and the device key map, which a choice must keep
const rfusbUserConf = "SGTIN.LocalKey.MappingFile=/etc/config/crRFD/sgtin.map\nNetwork.Key=00112233445566778899AABBCCDDEEFF\nKeyServer.Mode=LOCAL\n"

// withPriv swaps the helper for the test.
func withPriv(t *testing.T, p priv.Ops) {
	t.Helper()
	old := Priv
	Priv = p
	t.Cleanup(func() { Priv = old })
}

// B-271: the HmIP-RFUSB chosen through multimacd survives the Apply, the re-detection and a
// restart of occulited (a reboot's fresh service), with hmip_user.conf in a directory closed to
// the daemon - and the file's other lines stay.
func TestRadioConnChoiceRoundTripClosedDir(t *testing.T) {
	root, p := rfusbRoot(t)
	withPriv(t, p)
	s, svc := newConn(t, root, nil)
	// the run is root's: it reads the closed directory
	svc.onDetect = func() {
		_ = p.open(func() error {
			det, _ := s.detection()
			in := radio.Load(context.Background(), root, fakeRunner, det)
			b, _ := json.Marshal(radio.MakePlan(in))
			return os.WriteFile(filepath.Join(root, "run/occulite/radio/plan.json"), b, 0o644)
		})
	}
	// automatic runs the dual stick through multimacd already: the pin is the same plan, and it
	// must stay a pin all the same
	if st := s.Status(); st.Choices.Explicit() || st.Plan.HmIPServer.Node != "/dev/mmd_hmip" {
		t.Fatalf("before: automatic: %+v %+v", st.Choices, st.Plan.HmIPServer)
	}
	want := radio.Choices{HmIP: "0000000A01", HmIPPath: radio.PathMultimacd, BidCos: "0000000A01"}
	if _, err := s.Apply(context.Background(), want, false); err != nil {
		t.Fatal(err)
	}
	if a := waitApply(t, s); !a.OK {
		t.Fatalf("the change failed: %+v", a)
	}
	check := func(what string, s *RadioConnections) {
		t.Helper()
		st := s.Status()
		if st.Choices != want {
			t.Fatalf("%s: the choice reads %+v, want %+v", what, st.Choices, want)
		}
		if !st.Plan.Multimacd.Run || st.Plan.Multimacd.Node != "/dev/raw-uart" || st.Plan.HmIPServer.Node != "/dev/mmd_hmip" || st.Plan.RFD.Node != "/dev/mmd_bidcos" {
			t.Fatalf("%s: hmipserver over multimacd on the stick: %+v", what, st.Plan)
		}
	}
	check("after the Apply and the re-detection", s)
	// a reboot: a new service reads the files, the boot's run plans from them
	svc.onDetect()
	s2, _ := newConn(t, root, nil)
	s2.Load(context.Background())
	check("after a restart", s2)
	// an unchanged choice is no change
	if pv, err := s2.Preview(context.Background(), want); err != nil || pv.Changed {
		t.Fatalf("the saved choice again: changed=%v err=%v", pv.Changed, err)
	}
	var b []byte
	_ = p.open(func() (err error) { b, err = os.ReadFile(filepath.Join(p.dir, "hmip_user.conf")); return })
	if string(b) != rfusbUserConf+"occu"+"lite.hmip.adapter=0000000A01\noccu"+"lite.hmip.path=multimacd\n" {
		t.Fatalf("hmip_user.conf keeps the key lines:\n%s", b)
	}
	// back to automatic: the key lines still stay
	if _, err := s2.Apply(context.Background(), radio.Choices{}, false); err != nil {
		t.Fatal(err)
	}
	if a := waitApply(t, s2); !a.OK {
		t.Fatalf("back: %+v", a)
	}
	_ = p.open(func() (err error) { b, err = os.ReadFile(filepath.Join(p.dir, "hmip_user.conf")); return })
	if string(b) != rfusbUserConf || s2.Status().Choices.Explicit() {
		t.Fatalf("automatic again:\n%s", b)
	}
}

// B-271: a hmip_user.conf the helper could not read is not rewritten from nothing.
func TestRadioConnUnreadableUserConfNotRewritten(t *testing.T) {
	root, p := rfusbRoot(t)
	p.failRead = true
	withPriv(t, p)
	s, _ := newConn(t, root, nil)
	_, err := s.Apply(context.Background(), radio.Choices{HmIP: "0000000A01", HmIPPath: radio.PathMultimacd}, false)
	if err == nil {
		err = errors.New(waitApply(t, s).Error)
	}
	if !strings.Contains(err.Error(), "reading /etc/config/crRFD/hmip_user.conf") {
		t.Fatalf("refused: %v", err)
	}
	var b []byte
	_ = p.open(func() (err error) { b, err = os.ReadFile(filepath.Join(p.dir, "hmip_user.conf")); return })
	if string(b) != rfusbUserConf {
		t.Fatalf("hmip_user.conf rewritten:\n%s", b)
	}
}

// pinnedStickRoot is a lab system as B-272 found it: an HmIP-RFUSB pinned for HmIP-RF (through
// multimacd) and BidCos-RF, and an HB-RF-ETH configured under LAN devices. With board, the radio
// hotplug has attached it and its HM-MOD-RPI-PCB is in the detection; without, the board is
// configured and not connected yet (the address just set, or the board not answering).
func pinnedStickRoot(t *testing.T, board bool) string {
	t.Helper()
	root := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stick := radio.Module{Name: "raw-uart", Node: "/dev/raw-uart", DeviceType: "eQ-3 HmIP-RFUSB@usb-0000:02:1b.0-1", Hardware: "HMIP-RFUSB", Serial: "0000000A01", SGTIN: "3014F711A000040000000A01", HmRFAddress: "0xFF0001", HmIPAddress: "0xB00001", Version: "4.4.18", Probe: "ok"}
	det := radio.Detection{Modules: []radio.Module{stick}, HBRFETH: "192.0.2.50", HBRFETHConnected: board}
	if board {
		det.Modules = append(det.Modules, radio.Module{Name: "raw-uart1", Node: "/dev/raw-uart1", DeviceType: "HB-RF-ETH@192.0.2.50", Hardware: "HM-MOD-RPI-PCB", Serial: "MEQ9000005", SGTIN: "3014F711A061A70000000A05", HmRFAddress: "0x3D0A01", HmIPAddress: "0xB0A001", Version: "2.8.6", Probe: "ok"})
		w("sys/class/hb-rf-eth/hb-rf-eth/is_connected", "1\n")
	} else {
		w("sys/class/hb-rf-eth/hb-rf-eth/is_connected", "0\n")
	}
	w("etc/config/hb_rf_eth", "192.0.2.50\n")
	w("var/hm_mode", "HM_HOST='ova'\nHM_MODE='NORMAL'\n")
	w("etc/config/rfd.conf", "# occulite.bidcos.module=0000000A01\n"+connRFDConf)
	w("etc/config_templates/rfd.conf", connRFDConf)
	w("etc/config_templates/InterfacesList.xml", "<interfaces><ipc><name>BidCos-RF</name><url>xmlrpc_bin://127.0.0.1:32001</url><info>BidCos-RF</info></ipc><ipc><name>VirtualDevices</name><url>xmlrpc://127.0.0.1:39292/groups</url><info>Virtual Devices</info></ipc><ipc><name>HmIP-RF</name><url>xmlrpc://127.0.0.1:32010</url><info>HmIP-RF</info></ipc></interfaces>")
	w("etc/config/crRFD/hmip_user.conf", rfusbUserConf+"occulite.hmip.adapter=0000000A01\nocculite.hmip.path=multimacd\n")
	b, _ := json.Marshal(det)
	w("run/occulite/radio/modules.json", string(b))
	in := radio.Load(context.Background(), root, fakeRunner, det)
	b, _ = json.Marshal(radio.MakePlan(in))
	w("run/occulite/radio/plan.json", string(b))
	return root
}

// openccu-lite B-272: an HB-RF-ETH added under LAN devices while both processes are pinned to
// the stick. Its module holds no role, so the plan's files never name it - the status lists it all
// the same, with no role, offers it for both processes, and says the board is detected; before the
// hotplug attached it, the board is configured and not detected, and the list is the stick alone.
func TestRadioConnStatusPinnedStickAndLANDevice(t *testing.T) {
	root := pinnedStickRoot(t, true)
	s, _ := newConn(t, root, nil)
	st := s.Status()
	if !st.Available || st.Choices.HmIP != "0000000A01" || st.Choices.BidCos != "0000000A01" || st.Plan == nil || st.Plan.HmIP == nil || st.Plan.HmIP.Serial != "0000000A01" || st.Plan.HmRF == nil || st.Plan.HmRF.Serial != "0000000A01" {
		t.Fatalf("the pin: %+v %+v", st.Choices, st.Plan)
	}
	ids := func(list []radio.Option) (out []string) {
		for _, o := range list {
			out = append(out, o.ID)
		}
		return
	}
	if !reflect.DeepEqual(ids(st.Options.HmIP), []string{"0000000A01", "MEQ9000005"}) || !reflect.DeepEqual(ids(st.Options.BidCos), []string{"0000000A01", "MEQ9000005"}) {
		t.Fatalf("the board's module is not offered: %+v", st.Options)
	}
	want := []ConnModule{
		{Serial: "0000000A01", Hardware: "HMIP-RFUSB", Node: "/dev/raw-uart", DeviceType: "eQ-3 HmIP-RFUSB@usb-0000:02:1b.0-1", SGTIN: "3014F711A000040000000A01", Version: "4.4.18", Probe: "ok", Roles: []string{"BidCos-RF", "HmIP-RF"}},
		{Serial: "MEQ9000005", Hardware: "HM-MOD-RPI-PCB", Node: "/dev/raw-uart1", DeviceType: "HB-RF-ETH@192.0.2.50", SGTIN: "3014F711A061A70000000A05", Version: "2.8.6", Probe: "ok", Roles: []string{}},
	}
	if !reflect.DeepEqual(st.Modules, want) {
		t.Fatalf("modules:\n%+v\nwant\n%+v", st.Modules, want)
	}
	if !reflect.DeepEqual(st.HBRFETH, &ConnHBRFETH{Address: "192.0.2.50", Connected: true, Detected: true, Serial: "MEQ9000005"}) {
		t.Fatalf("the board: %+v", st.HBRFETH)
	}
	// choosing the board's module works: HmIP through multimacd (the PCB's only path) and BidCos-RF
	pv, err := s.Preview(context.Background(), radio.Choices{HmIP: "MEQ9000005", HmIPPath: radio.PathMultimacd, BidCos: "MEQ9000005"})
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Changed || pv.Plan.HmIP == nil || pv.Plan.HmIP.Serial != "MEQ9000005" || pv.Plan.HmRF == nil || pv.Plan.HmRF.Node != "/dev/raw-uart1" || pv.Plan.Multimacd.Node != "/dev/raw-uart1" {
		t.Fatalf("the board's module chosen: %+v", pv.Plan)
	}

	// the board just added: configured, not connected, its module not in the detection
	root = pinnedStickRoot(t, false)
	s, _ = newConn(t, root, nil)
	st = s.Status()
	if len(st.Modules) != 1 || st.Modules[0].Serial != "0000000A01" || len(st.Options.HmIP) != 1 {
		t.Fatalf("before the hotplug: %+v %+v", st.Modules, st.Options)
	}
	if !reflect.DeepEqual(st.HBRFETH, &ConnHBRFETH{Address: "192.0.2.50"}) {
		t.Fatalf("the board pending: %+v", st.HBRFETH)
	}
	// no board configured: nothing about one
	if err := os.Remove(filepath.Join(root, "etc/config/hb_rf_eth")); err != nil {
		t.Fatal(err)
	}
	if st = s.Status(); st.HBRFETH != nil {
		t.Fatalf("no board: %+v", st.HBRFETH)
	}
}

// B-272: the roles of the module list - the HM-CFG-USB-2 (no node) by serial, a module that
// failed its probe in no role.
func TestConnModulesRoles(t *testing.T) {
	adapter := radio.Module{USBAdapter: true, Serial: "JEQ9000002", Probe: "ok"}
	dead := radio.Module{Name: "raw-uart1", Node: "/dev/raw-uart1", DeviceType: "HB-RF-ETH@192.0.2.50", Probe: "none", Detail: "nothing answered"}
	det := radio.Detection{Modules: []radio.Module{adapter, dead}}
	p := radio.Plan{HmRF: &radio.Role{Serial: "JEQ9000002"}}
	got := connModules(det, p)
	want := []ConnModule{
		{Serial: "JEQ9000002", Hardware: "HM-CFG-USB-2", DeviceType: "USB", Probe: "ok", Roles: []string{"BidCos-RF"}},
		{Node: "/dev/raw-uart1", DeviceType: "HB-RF-ETH@192.0.2.50", Probe: "none", Detail: "nothing answered", Roles: []string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	if got := connModules(radio.Detection{}, radio.Plan{}); len(got) != 0 || got == nil {
		t.Fatalf("empty: %#v", got)
	}
}

// openccu-lite B-300: the GPIO header's UART that nothing answered on has no card - a Pi with
// only USB sticks (the header probed under the short limit) and one with nothing at all (the
// second pass's full probe found no module); a header that answered wrongly keeps its card, as do
// a silent USB or HB-RF node; the hint only where a chosen module is missing.
func TestConnModulesEmptyHeader(t *testing.T) {
	gpio := func(probe, detail string) radio.Module {
		return radio.Module{Name: "raw-uart", Node: "/dev/raw-uart", DeviceType: "GPIO@fe201000.serial", GPIO: true, Probe: probe, Detail: detail}
	}
	stick := radio.Module{Name: "raw-uart1", Node: "/dev/raw-uart1", DeviceType: "eQ-3 HmIP-RFUSB@usb-1.3", Hardware: "HMIP-RFUSB", Serial: "0000000A07", SGTIN: "3014F711A000040000000A07", HmRFAddress: "0x3D0A07", HmIPAddress: "0xBC0A07", Version: "4.4.18", Probe: "ok"}
	tty := radio.Module{Name: "ttyAMA0", Node: "/dev/ttyAMA0", Probe: "none", Detail: "no module"}
	deadUSB := radio.Module{Name: "raw-uart2", Node: "/dev/raw-uart2", DeviceType: "eQ-3 HmIP-RFUSB@usb-1.2", Probe: "timeout", Detail: "no answer within 45s"}
	nodes := func(ms []ConnModule) []string {
		out := []string{}
		for _, m := range ms {
			out = append(out, m.Node+":"+m.Probe)
		}
		return out
	}
	p := radio.Plan{HmIP: &radio.Role{Node: "/dev/raw-uart1"}, HmIPServerHmIP: true}
	for _, c := range []struct {
		name string
		mods []radio.Module
		want []string
	}{
		{"sticks only, the header cut at 6s", []radio.Module{gpio("timeout", "no answer within 6s"), stick}, []string{"/dev/raw-uart1:ok"}},
		{"nothing at all, the second pass", []radio.Module{gpio("none", "no module")}, []string{}},
		{"the ttyAMA0 fallback", []radio.Module{tty}, []string{}},
		{"a module on the header that answered wrongly", []radio.Module{gpio("error", "unexpected answer: x"), stick}, []string{"/dev/raw-uart:error", "/dev/raw-uart1:ok"}},
		{"a stick that does not answer", []radio.Module{deadUSB, stick}, []string{"/dev/raw-uart2:timeout", "/dev/raw-uart1:ok"}},
	} {
		if got := nodes(connModules(radio.Detection{Modules: c.mods}, p)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	empty := radio.Detection{Modules: []radio.Module{gpio("timeout", "no answer within 6s"), stick}}
	for _, c := range []struct {
		name string
		det  radio.Detection
		ch   radio.Choices
		want *ConnHeaderSilent
	}{
		{"auto", empty, radio.Choices{}, nil},
		{"the chosen stick is there (serial)", empty, radio.Choices{HmIP: "0000000a07"}, nil},
		{"the chosen stick is there (SGTIN), BidCos-RF none", empty, radio.Choices{HmIP: "3014F711A000040000000A07", BidCos: radio.BidCosNone}, nil},
		{"the chosen module is missing", empty, radio.Choices{HmIP: "0000000A03", BidCos: "0000000A03"}, &ConnHeaderSilent{Node: "/dev/raw-uart", Probe: "timeout", Detail: "no answer within 6s", Missing: []string{"0000000A03"}}},
		{"missing, but the header answered wrongly", radio.Detection{Modules: []radio.Module{gpio("error", "x"), stick}}, radio.Choices{HmIP: "0000000A03"}, nil},
		{"missing, no header", radio.Detection{Modules: []radio.Module{stick}}, radio.Choices{BidCos: "0000000A03"}, nil},
	} {
		if got := headerSilent(c.det, c.ch); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// twoHmIPRoot is a system with two modules that can carry HmIP-RF: the RPI-RF-MOD on the header
// (HmIP-RF runs on it, its identity, hmip_address.conf and two device files are on the system) and
// an HmIP-RFUSB.
func twoHmIPRoot(t *testing.T) string {
	t.Helper()
	return twoHmIPRootStick(t, "4.4.18")
}

// twoHmIPRootStick is twoHmIPRoot with the stick's application firmware.
func twoHmIPRootStick(t *testing.T, stickVersion string) string {
	t.Helper()
	root := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mod := radio.Module{Name: "raw-uart", Node: "/dev/raw-uart", DeviceType: "GPIO@3f201000.serial", GPIO: true, Hardware: "RPI-RF-MOD", Serial: "0000000A03", SGTIN: "3014F711A0001F0000000A03", HmRFAddress: "0x1F0A14", HmIPAddress: "0x3F0A15", Version: "4.4.22", Probe: "ok"}
	stick := radio.Module{Name: "raw-uart1", Node: "/dev/raw-uart1", DeviceType: "eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3", Hardware: "HMIP-RFUSB", Serial: "0000000A01", SGTIN: "3014F711A000040000000A01", HmRFAddress: "0xFF0001", HmIPAddress: "0xB00001", Version: stickVersion, Probe: "ok"}
	det := radio.Detection{Modules: []radio.Module{mod, stick}}
	w("var/hm_mode", "HM_HOST='rpi3'\nHM_MODE='NORMAL'\n")
	w("etc/config/rfd.conf", connRFDConf)
	w("etc/config_templates/rfd.conf", connRFDConf)
	w("etc/config_templates/InterfacesList.xml", "<interfaces><ipc><name>BidCos-RF</name><url>xmlrpc_bin://127.0.0.1:32001</url><info>BidCos-RF</info></ipc><ipc><name>VirtualDevices</name><url>xmlrpc://127.0.0.1:39292/groups</url><info>VirtualDevices</info></ipc></interfaces>")
	w("etc/config/crRFD/hmip_user.conf", "occulite.hmip.path=\n")
	for _, ext := range []string{".ap", ".apkx", ".bbkx"} {
		w("etc/config/crRFD/data/3014F711A0001F0000000A03"+ext, "MOD-"+ext)
	}
	w("etc/config/crRFD/data/3014F711A0000000000000B1.dev", "DEV-B1-before")
	w("etc/config/crRFD/data/3014F711A0000000000000B2.dev", "DEV-B2-before")
	w("etc/config/crRFD/data/linkData.conf", "links")
	w("etc/config/hmip_address.conf", "Adapter.1.Address=3F0A15\n")
	w("etc/config/netconfig", "HOSTNAME=lite-test\n")
	b, _ := json.Marshal(det)
	w("run/occulite/radio/modules.json", string(b))
	in := radio.Load(context.Background(), root, fakeRunner, det)
	b, _ = json.Marshal(radio.MakePlan(in))
	w("run/occulite/radio/plan.json", string(b))
	return root
}

// openccu-lite B-285: a change that moves HmIP-RF to another module is named in the preview, takes
// a module-move snapshot of the module it leaves (identity, hmip_address.conf, the device files,
// the choices from before) before anything is written, and the way back restores it all while
// the daemons are stopped and puts the choices back - no key-server exchange
func TestHmIPModuleMoveSnapshotAndTheWayBack(t *testing.T) {
	root := twoHmIPRoot(t)
	s, svc := newConn(t, root, nil)
	k := &HmIPLocalKey{Root: Root(root), Services: svc, StateDir: filepath.Join(root, "state/hmip-local-key"), Plan: s.BootPlan, Busy: s.Busy}
	s.BeforeHmIPMove, s.MoveBackOffer, k.Conn = k.SnapshotBeforeMove, k.MoveBackOffer, s.ApplyRestore
	const mod, stick = "3014F711A0001F0000000A03", "3014F711A000040000000A01"
	if st := s.Status(); st.Plan.HmIP == nil || st.Plan.HmIP.SGTIN != mod || st.HmIPMoveBack != nil {
		t.Fatalf("before: %+v %+v", st.Plan.HmIP, st.HmIPMoveBack)
	}
	// the preview names the move; a change of the BidCos side alone does not
	toStick := radio.Choices{HmIP: "0000000A01", HmIPPath: radio.PathDirect}
	pv, err := s.Preview(context.Background(), toStick)
	if err != nil {
		t.Fatal(err)
	}
	if pv.HmIPMove == nil || pv.HmIPMove.From != mod || pv.HmIPMove.To != stick || pv.HmIPMove.LocalKey || !pv.HmIPMove.Snapshot {
		t.Fatalf("preview: %+v", pv.HmIPMove)
	}
	if pv, err := s.Preview(context.Background(), radio.Choices{BidCos: radio.BidCosNone}); err != nil || pv.HmIPMove != nil {
		t.Fatalf("a BidCos-only change names a move: %+v %v", pv.HmIPMove, err)
	}
	// openccu-lite task 317 (D-120): not without the user's word - nothing is taken or written
	var cr *ConfirmRequired
	if _, err := s.Apply(context.Background(), toStick, false); !errors.As(err, &cr) || cr.HmIPMove == nil || cr.HmIPMove.From != mod || len(cr.Devices) != 0 {
		t.Fatalf("a move without confirm: %v", err)
	}
	if snaps := k.Snapshots(); len(snaps) != 0 {
		t.Fatalf("a refused move took a snapshot: %+v", snaps)
	}
	if got := radio.ReadChoices(readFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf")), ""); got.HmIP != "" {
		t.Fatalf("a refused move wrote its choice: %+v", got)
	}
	// the move: the snapshot first, then the change
	if _, err := s.Apply(context.Background(), toStick, true); err != nil {
		t.Fatal(err)
	}
	a := waitApply(t, s)
	if !a.OK || !strings.Contains(strings.Join(a.Lines, "\n"), "kept in a snapshot") {
		t.Fatalf("the move: %+v", a)
	}
	snaps := k.Snapshots()
	if len(snaps) != 1 || snaps[0].SGTIN != mod || snaps[0].Kind != SnapshotModuleMove || snaps[0].Choices == nil || snaps[0].Choices.HmIP != "" {
		t.Fatalf("snapshot: %+v", snaps)
	}
	if got := strings.Join(snaps[0].Files, " "); got != mod+".ap "+mod+".apkx "+mod+".bbkx hmip_address.conf 3014F711A0000000000000B1.dev 3014F711A0000000000000B2.dev" {
		t.Fatalf("snapshot files: %s", got)
	}
	dir := k.snapshotDir(mod)
	if readFile(filepath.Join(dir, "3014F711A0000000000000B1.dev")) != "DEV-B1-before" || readFile(filepath.Join(dir, "hmip_address.conf")) != "Adapter.1.Address=3F0A15\n" || readFile(filepath.Join(dir, "hmip_user.conf")) == "" {
		t.Fatal("the snapshot's copies")
	}
	// nothing was removed from the data directory by the snapshot
	if readFile(filepath.Join(root, "etc/config/crRFD/data", mod+".ap")) != "MOD-.ap" {
		t.Fatal("the identity was taken from the data directory")
	}
	st := s.Status()
	if st.Plan.HmIP == nil || st.Plan.HmIP.SGTIN != stick || st.HmIPMoveBack == nil || st.HmIPMoveBack.Previous != mod || st.HmIPMoveBack.Devices != 2 || st.HmIPMoveBack.Choices.HmIP != "" {
		t.Fatalf("after the move: %+v %+v", st.Plan.HmIP, st.HmIPMoveBack)
	}
	// hmipserver did the exchange onto the stick: its identity, the device files rewritten for the
	// new access point, the address file rewritten - and then the exchange back was refused
	data := filepath.Join(root, "etc/config/crRFD/data")
	_ = os.WriteFile(filepath.Join(data, stick+".ap"), []byte("STICK-.ap"), 0o644)
	_ = os.Remove(filepath.Join(data, mod+".ap"))
	_ = os.Remove(filepath.Join(data, mod+".apkx"))
	_ = os.Remove(filepath.Join(data, mod+".bbkx"))
	_ = os.WriteFile(filepath.Join(data, "3014F711A0000000000000B1.dev"), []byte("DEV-B1-after"), 0o644)
	_ = os.WriteFile(filepath.Join(data, "3014F711A0000000000000B2.dev"), []byte("DEV-B2-after"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "etc/config/hmip_address.conf"), []byte("Adapter.1.Address=B00001\n"), 0o644)
	marker := lkFatal(t, root, radio.CauseRefused)
	// the way back
	svc.calls = nil
	if err := k.MoveBack(); err != nil {
		t.Fatal(err)
	}
	if lst := lkWait(t, k); lst.Error != "" {
		t.Fatalf("the way back: %+v", lst)
	}
	a = waitApply(t, s)
	if !a.OK || a.Choices.HmIP != "" || !strings.Contains(strings.Join(a.Lines, "\n"), "identity and device files are back") {
		t.Fatalf("the change back: %+v", a)
	}
	for _, ext := range []string{".ap", ".apkx", ".bbkx"} {
		if b := readFile(filepath.Join(data, mod+ext)); b != "MOD-"+ext {
			t.Errorf("restored %s: %q", ext, b)
		}
	}
	// task 317 (D-120): the identity files as they were before the way back, copied first
	if got := identityBackup(t, k, "move-back"); len(got) == 0 {
		t.Error("no copy of the identity files before the way back")
	}
	if readFile(filepath.Join(data, "3014F711A0000000000000B1.dev")) != "DEV-B1-before" || readFile(filepath.Join(data, "3014F711A0000000000000B2.dev")) != "DEV-B2-before" {
		t.Error("the device files were not restored")
	}
	if readFile(filepath.Join(root, "etc/config/hmip_address.conf")) != "Adapter.1.Address=3F0A15\n" {
		t.Error("hmip_address.conf was not restored")
	}
	if _, err := os.Stat(filepath.Join(data, stick+".ap")); !os.IsNotExist(err) {
		t.Error("the stick's identity is still in the data directory")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the marker of the rejected exchange stayed")
	}
	// the stick's identity is a fresh-start snapshot of its own; the move snapshot is consumed
	snaps = k.Snapshots()
	if len(snaps) != 1 || snaps[0].SGTIN != stick || snaps[0].Kind != SnapshotFreshStart {
		t.Fatalf("snapshots after the way back: %+v", snaps)
	}
	st = s.Status()
	if st.Plan.HmIP == nil || st.Plan.HmIP.SGTIN != mod || st.HmIPMoveBack != nil {
		t.Fatalf("after the way back: %+v %+v", st.Plan.HmIP, st.HmIPMoveBack)
	}
	// the restore ran while the daemons were stopped: after the stops, before the detection
	joined := strings.Join(svc.calls, "\n")
	if !strings.Contains(joined, "hmipserver stop") || !strings.Contains(joined, RadioDetectionUnit+" restart") {
		t.Fatalf("calls: %s", joined)
	}
	if strings.Index(joined, "hmipserver stop") > strings.Index(joined, RadioDetectionUnit+" restart") {
		t.Fatalf("the detection ran before the stop: %s", joined)
	}
	// a second way back has nothing to go back to
	if err := k.MoveBack(); err == nil || !strings.Contains(err.Error(), "no snapshot") {
		t.Fatalf("second way back: %v", err)
	}
}

// with local key mode on no key server is involved: the preview says so, no snapshot is taken
// and nothing is offered
func TestHmIPModuleMoveWithLocalKeyOnTakesNoSnapshot(t *testing.T) {
	root := twoHmIPRoot(t)
	_ = os.WriteFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"), []byte("occulite.hmip.path=\nKeyServer.Mode=LOCAL\nNetwork.Key=00112233445566778899AABBCCDDEEFF\n"), 0o644)
	s, svc := newConn(t, root, nil)
	k := &HmIPLocalKey{Root: Root(root), Services: svc, StateDir: filepath.Join(root, "state/hmip-local-key"), Plan: s.BootPlan, Busy: s.Busy}
	s.BeforeHmIPMove, s.MoveBackOffer, k.Conn = k.SnapshotBeforeMove, k.MoveBackOffer, s.ApplyRestore
	toStick := radio.Choices{HmIP: "0000000A01", HmIPPath: radio.PathDirect}
	pv, err := s.Preview(context.Background(), toStick)
	if err != nil || pv.HmIPMove == nil || !pv.HmIPMove.LocalKey || pv.HmIPMove.Snapshot {
		t.Fatalf("preview: %+v %v", pv.HmIPMove, err)
	}
	if _, err := s.Apply(context.Background(), toStick, true); err != nil {
		t.Fatal(err)
	}
	if a := waitApply(t, s); !a.OK || strings.Contains(strings.Join(a.Lines, "\n"), "snapshot") {
		t.Fatalf("%+v", a)
	}
	if len(k.Snapshots()) != 0 || s.Status().HmIPMoveBack != nil {
		t.Fatalf("a snapshot with local key mode on: %+v", k.Snapshots())
	}
	if err := k.MoveBack(); err == nil {
		t.Fatal("a way back without a snapshot")
	}
}

// openccu-lite B-289: HmIP-RF does not move onto a module whose application firmware is below
// 2.8.0 while local key mode is off - the preview names the refusal, the change is refused before
// anything is written or kept; with local key mode on the move is offline and goes ahead
func TestHmIPModuleMoveRefusedBelowFirmware280(t *testing.T) {
	root := twoHmIPRootStick(t, "1.8.3")
	s, svc := newConn(t, root, nil)
	k := &HmIPLocalKey{Root: Root(root), Services: svc, StateDir: filepath.Join(root, "state/hmip-local-key"), Plan: s.BootPlan, Busy: s.Busy}
	s.BeforeHmIPMove, s.MoveBackOffer, k.Conn = k.SnapshotBeforeMove, k.MoveBackOffer, s.ApplyRestore
	const stick = "3014F711A000040000000A01"
	toStick := radio.Choices{HmIP: "0000000A01", HmIPPath: radio.PathDirect}
	pv, err := s.Preview(context.Background(), toStick)
	if err != nil || pv.HmIPMove == nil || pv.HmIPMove.ToVersion != "1.8.3" {
		t.Fatalf("preview: %+v %v", pv.HmIPMove, err)
	}
	want := HmIPFirmwareRefused{Module: stick, Version: "1.8.3", Minimum: "2.8.0"}
	if r := pv.HmIPMove.Refused; r == nil || *r != want {
		t.Fatalf("refusal: %+v", r)
	}
	before, _ := os.ReadFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"))
	_, err = s.Apply(context.Background(), toStick, true)
	var refused *HmIPFirmwareRefused
	if !errors.As(err, &refused) || *refused != want || !strings.Contains(err.Error(), "1.8.3") || !strings.Contains(err.Error(), "update the module's firmware") {
		t.Fatalf("apply: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"))
	if string(after) != string(before) || len(k.Snapshots()) != 0 || s.Status().Running != nil || s.Status().Last != nil {
		t.Fatalf("a refused change wrote or kept something: %q %+v", after, k.Snapshots())
	}
	// the other module's side is untouched: a BidCos-only change goes ahead
	if pv, err := s.Preview(context.Background(), radio.Choices{BidCos: radio.BidCosNone}); err != nil || pv.HmIPMove != nil {
		t.Fatalf("BidCos-only: %+v %v", pv.HmIPMove, err)
	}
	// local key mode on: offline, allowed
	_ = os.WriteFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"), []byte("occulite.hmip.path=\nKeyServer.Mode=LOCAL\nNetwork.Key=00112233445566778899AABBCCDDEEFF\n"), 0o644)
	if pv, err := s.Preview(context.Background(), toStick); err != nil || pv.HmIPMove == nil || pv.HmIPMove.Refused != nil || !pv.HmIPMove.LocalKey {
		t.Fatalf("local key mode: %+v %v", pv.HmIPMove, err)
	}
	if _, err := s.Apply(context.Background(), toStick, true); err != nil {
		t.Fatal(err)
	}
	if a := waitApply(t, s); !a.OK {
		t.Fatalf("%+v", a)
	}
}

func TestHmIPFirmwareRefusal(t *testing.T) {
	if HmIPFirmwareRefusal("x", "1.8.3", true) != nil || HmIPFirmwareRefusal("x", "2.8.0", false) != nil || HmIPFirmwareRefusal("x", "", false) != nil {
		t.Fatal("refused what may go")
	}
	if r := HmIPFirmwareRefusal("3014f711a000040000000a01", "2.6.1", false); r == nil || r.Module != "3014F711A000040000000A01" || r.Minimum != radio.MinHmIPKeyExchangeVersion {
		t.Fatalf("%+v", r)
	}
}

// openccu-lite task 318 (D-120): on Automatic HmIP-RF is kept on the module holding the network;
// with that module missing the status names it, Automatic stays without HmIP, and a choice of
// another module is a move from it - confirmed, with the snapshot of its identity where the files
// are on the system.
func TestHmIPKeptOnItsMissingModule(t *testing.T) {
	root := twoHmIPRoot(t)
	const mod, stick, mac = "3014F711A0001F0000000A03", "3014F711A000040000000A01", "aa:aa:aa:aa:aa:01"
	var det radio.Detection
	_ = json.Unmarshal([]byte(readFile(filepath.Join(root, "run/occulite/radio/modules.json"))), &det)
	det.Modules, det.BoardMAC = det.Modules[1:], mac // the RPI-RF-MOD is gone
	b, _ := json.Marshal(det)
	_ = os.WriteFile(filepath.Join(root, "run/occulite/radio/modules.json"), b, 0o644)
	_ = os.MkdirAll(filepath.Dir(filepath.Join(root, radio.HmIPPinFile)), 0o755)
	_ = os.WriteFile(filepath.Join(root, radio.HmIPPinFile), []byte(`{"sgtin":"`+mod+`","board_mac":"`+mac+`"}`), 0o644)
	b, _ = json.Marshal(radio.MakePlan(radio.Load(context.Background(), root, fakeRunner, det)))
	_ = os.WriteFile(filepath.Join(root, "run/occulite/radio/plan.json"), b, 0o644)

	s, svc := newConn(t, root, nil)
	k := &HmIPLocalKey{Root: Root(root), Services: svc, StateDir: filepath.Join(root, "state/hmip-local-key"), Plan: s.BootPlan, Busy: s.Busy}
	s.BeforeHmIPMove, s.HmIPIdentity = k.SnapshotBeforeMove, k.HasIdentity
	st := s.Status()
	if st.Plan.HmIP != nil || st.Plan.MissingHmIP != mod || st.Plan.HmIPPin != mod || len(st.Options.HmIP) != 1 {
		t.Fatalf("status: %+v", st.Plan)
	}
	// Automatic again: no move, still the VirtualDevices half
	if pv, err := s.Preview(context.Background(), radio.Choices{}); err != nil || pv.HmIPMove != nil || pv.Plan.HmIP != nil {
		t.Fatalf("automatic: %+v %v", pv.HmIPMove, err)
	}
	toStick := radio.Choices{HmIP: "0000000A01"}
	pv, err := s.Preview(context.Background(), toStick)
	if err != nil || pv.HmIPMove == nil || pv.HmIPMove.From != mod || pv.HmIPMove.To != stick || !pv.HmIPMove.Snapshot {
		t.Fatalf("the move from the missing module: %+v %v", pv.HmIPMove, err)
	}
	var cr *ConfirmRequired
	if _, err := s.Apply(context.Background(), toStick, false); !errors.As(err, &cr) || cr.HmIPMove == nil {
		t.Fatalf("not without confirm: %v", err)
	}
	// its identity files are not on the system: nothing to keep, the move goes without a snapshot
	s.HmIPIdentity = func(string) bool { return false }
	if pv, _ := s.Preview(context.Background(), toStick); pv.HmIPMove == nil || pv.HmIPMove.Snapshot {
		t.Fatalf("no identity files: %+v", pv.HmIPMove)
	}
	if !k.HasIdentity(strings.ToLower(mod)) || k.HasIdentity(stick) {
		t.Error("HasIdentity")
	}
}

// openccu-lite B-301 (maintainer: the refusal stays): a snapshot of the module in use from a
// switch to local key mode, kept after the switch back, blocks the module-move snapshot. The
// preview says so beforehand, the change is refused before anything is written, and after the
// discard the move goes through.
func TestHmIPMoveBlockedByALocalKeySnapshot(t *testing.T) {
	root := twoHmIPRoot(t)
	s, svc := newConn(t, root, nil)
	k := &HmIPLocalKey{Root: Root(root), Services: svc, StateDir: filepath.Join(root, "state/hmip-local-key"), Plan: s.BootPlan, Busy: s.Busy}
	s.BeforeHmIPMove, s.LocalKeySnapshot = k.SnapshotBeforeMove, k.LocalKeySnapshotKept
	const mod = "3014F711A0001F0000000A03"
	if err := k.snapshot(mod, readFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"))); err != nil {
		t.Fatal(err)
	}
	if !k.LocalKeySnapshotKept(strings.ToLower(mod)) || k.LocalKeySnapshotKept("3014F711A000040000000A01") {
		t.Fatal("LocalKeySnapshotKept")
	}
	toStick := radio.Choices{HmIP: "0000000A01", HmIPPath: radio.PathDirect}
	pv, err := s.Preview(context.Background(), toStick)
	if err != nil || pv.HmIPMove == nil || !pv.HmIPMove.Snapshot || !pv.HmIPMove.SnapshotBlocked {
		t.Fatalf("preview: %+v %v", pv.HmIPMove, err)
	}
	var sb *SnapshotBlocked
	if _, err := s.Apply(context.Background(), toStick, true); !errors.As(err, &sb) || sb.SGTIN != mod {
		t.Fatalf("not refused: %v", err)
	}
	if st := s.Status(); st.Running != nil || st.Choices.HmIP != "" {
		t.Fatalf("a refused move started or wrote: %+v %+v", st.Running, st.Choices)
	}
	if err := k.DiscardSnapshot(mod); err != nil {
		t.Fatal(err)
	}
	if pv, _ := s.Preview(context.Background(), toStick); pv.HmIPMove == nil || pv.HmIPMove.SnapshotBlocked {
		t.Fatalf("after the discard: %+v", pv.HmIPMove)
	}
	if _, err := s.Apply(context.Background(), toStick, true); err != nil {
		t.Fatal(err)
	}
	if a := waitApply(t, s); !a.OK {
		t.Fatalf("the move: %+v", a)
	}
}
