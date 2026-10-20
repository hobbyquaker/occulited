package system

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/runlog"
)

// task 41: the coprocessor flash against a fake box - the lab Pi's /var/hm_mode, the files it
// ships, a fake helper that answers detect_radio_module and records the flash, a fake service
// manager whose multimacd creates and removes the /dev/mmd_* nodes as the real one does.

type fakeCopro struct {
	priv.Local
	mu        sync.Mutex
	version   string // what detect_radio_module reports
	flashExit int
	flashSets string // the version the coprocessor reports after a zero exit
	flashErr  error
	onFlash   func() // what the box does while the flasher runs (a stick that re-enumerates)
	calls     []string
}

func (f *fakeCopro) Run(_ context.Context, name string, args []string, _ []byte) (priv.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "run "+filepath.Base(name)+" "+strings.Join(args, " "))
	switch filepath.Base(name) {
	case "detect_radio_module":
		return priv.Result{Stdout: []byte("HMIP-RFUSB 0000000A01 3014F711A000040000000A01 0xFF0A09 0xBD0A07 " + f.version + "\n")}, nil
	case "eq3configcmd":
		return priv.Result{Stdout: []byte("Bootloader and Application Version: " + f.version + "\n")}, nil
	}
	return priv.Result{}, nil
}

func (f *fakeCopro) FlashCoprocessor(_ context.Context, family, devnode, file, version string) (priv.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "flash "+family+" "+devnode+" "+filepath.Base(file)+" "+version)
	if f.flashErr != nil {
		return priv.Result{}, f.flashErr
	}
	if f.flashExit == 0 && f.flashSets != "" {
		f.version = f.flashSets
	}
	if f.onFlash != nil {
		f.onFlash()
	}
	return priv.Result{Stdout: []byte("hmip-copro-update: writing 104616 bytes\ndone\n"), Exit: f.flashExit}, nil
}

func (f *fakeCopro) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

type fakeRadioSvc struct {
	root    string
	mu      sync.Mutex
	running map[string]bool
	calls   []string
	failing string // a unit whose control fails
}

func (f *fakeRadioSvc) List() ([]Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Service{}
	for _, u := range []string{"multimacd", "rfd", "hmipserver", "hs485d", "lighttpd"} {
		out = append(out, Service{ID: u, Kind: "system", Running: f.running[u]})
	}
	return out, nil
}

func (f *fakeRadioSvc) Control(_ context.Context, id, action string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id+" "+action)
	if id == f.failing {
		return "", errors.New(id + " refused")
	}
	f.running[id] = action == "start"
	if id == "multimacd" {
		for _, n := range []string{"dev/mmd_bidcos", "dev/mmd_hmip"} {
			if action == "start" {
				_ = os.WriteFile(filepath.Join(f.root, n), nil, 0o600)
			} else {
				_ = os.Remove(filepath.Join(f.root, n))
			}
		}
		count := "0\n"
		if action == "start" {
			count = "2\n"
		}
		_ = os.WriteFile(filepath.Join(f.root, "sys/class/raw-uart/raw-uart1/open_count"), []byte(count), 0o644)
	}
	return "", nil
}

func (f *fakeRadioSvc) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

// piRoot is the lab Pi as the tests see it: the RFUSB on raw-uart1 serving both stacks, the
// shipped 4.4.18, multimacd up with its two loop nodes.
func piRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("var/hm_mode", "HM_HMIP_DEV='HMIP-RFUSB'\nHM_HMIP_DEVNODE='/dev/raw-uart1'\nHM_HMIP_DEVTYPE='eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3'\nHM_HMIP_VERSION='4.4.18'\nHM_HMRF_DEV='HMIP-RFUSB'\nHM_HMRF_DEVNODE='/dev/raw-uart1'\nHM_HMRF_DEVTYPE='eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3'\nHM_HMRF_VERSION='4.4.18'\nHM_MODE='NORMAL'\n")
	w("firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3", strings.Repeat("A", 1000))
	w("firmware/HmIP-RFUSB/hmip_coprocessor_update-2.8.6.eq3", "not a dualcopro file")
	w("firmware/HmIP-RFUSB/fwmap", "#Type Filename Version\nCCU2\tdualcopro_update_blhmip-4.4.18.eq3\t4.4.18\t# Dual CoProzessor HmIP-RFUSB\n")
	w("sys/class/raw-uart/raw-uart1/device_type", "eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3\n")
	w("sys/class/raw-uart/raw-uart1/open_count", "2\n")
	w("sys/class/raw-uart/raw-uart/device_type", "GPIO@fe201000.serial\n")
	w("dev/mmd_bidcos", "")
	w("dev/mmd_hmip", "")
	w("usr/local/etc/occulite/staging/.keep", "")
	return root
}

func newRadioFW(t *testing.T, root string, fp *fakeCopro) (*RadioFirmware, *fakeRadioSvc) {
	t.Helper()
	old := Priv
	Priv = fp
	t.Cleanup(func() { Priv = old })
	svc := &fakeRadioSvc{root: root, running: map[string]bool{"multimacd": true, "rfd": true, "hmipserver": true, "lighttpd": true}}
	s := &RadioFirmware{Root: Root(root), Services: svc, Systemd: true, StateDir: filepath.Join(root, "state/radio-firmware"), Poll: time.Millisecond, Wait: 200 * time.Millisecond}
	return s, svc
}

func waitDone(t *testing.T, s *RadioFirmware) RadioFirmwareStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := s.Status()
		if st.Running == nil {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("the flash did not finish: %+v", st.Running)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRadioFirmwareStatus(t *testing.T) {
	root := piRoot(t)
	s, _ := newRadioFW(t, root, &fakeCopro{version: "4.4.18"})
	// an uploaded newer file and an older one
	up := filepath.Join(root, "usr/local/etc/config/radio-firmware/HmIP-RFUSB")
	_ = os.MkdirAll(up, 0o755)
	_ = os.WriteFile(filepath.Join(up, "dualcopro_update_blhmip-4.4.22.eq3"), []byte("newer"), 0o644)
	_ = os.WriteFile(filepath.Join(up, "dualcopro_update_blhmip-4.2.14.eq3"), []byte("older"), 0o644)
	st := s.Status()
	if len(st.Modules) != 1 {
		t.Fatalf("modules: %+v", st.Modules)
	}
	m := st.Modules[0]
	if !reflect.DeepEqual(m.Protocols, []string{"BidCos-RF", "HmIP-RF"}) || m.Device != "HMIP-RFUSB" || m.DeviceNode != "/dev/raw-uart1" || m.RunningVersion != "4.4.18" || m.Family != "hmip" || m.Dir != "HmIP-RFUSB" || !m.Flashable {
		t.Fatalf("module: %+v", m)
	}
	if m.Verdict != "newer-available" || m.Newest != "dualcopro_update_blhmip-4.4.22.eq3" {
		t.Fatalf("verdict: %s newest %s", m.Verdict, m.Newest)
	}
	names := []string{}
	for _, f := range m.Files {
		names = append(names, f.Name+":"+f.Source+":"+f.Version+":"+f.Direction)
	}
	want := []string{"dualcopro_update_blhmip-4.4.18.eq3:shipped:4.4.18:same", "dualcopro_update_blhmip-4.4.22.eq3:uploaded:4.4.22:upgrade", "dualcopro_update_blhmip-4.2.14.eq3:uploaded:4.2.14:downgrade"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("files: %v", names)
	}
	if m.Files[0].Size != 1000 || len(m.Files[0].SHA256) != 64 || m.Files[0].Path != "/firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3" {
		t.Fatalf("shipped file: %+v", m.Files[0])
	}
	if st.ForceNoUpdate || st.ForcedVersion != "" || st.FirmwareStaged || st.Running != nil || st.Last != nil || st.UploadDir != RadioFirmwareUploadDir {
		t.Fatalf("status: %+v", st)
	}
	// the kill switches and a staged update are surfaced
	_ = os.MkdirAll(filepath.Join(root, "etc/config"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "etc/config/force-no-coprocessor-update"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(root, "etc/config/forced_coprocessor_version"), []byte("4.4.18\n"), 0o644)
	_ = os.Symlink("/usr/local/tmp/x.zip", filepath.Join(root, "usr/local/.firmwareUpdate"))
	st = s.Status()
	if !st.ForceNoUpdate || st.ForcedVersion != "4.4.18" || !st.FirmwareStaged {
		t.Fatalf("switches: %+v", st)
	}
	if err := s.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("flash with the kill switch: %v", err)
	}
	_ = os.Remove(filepath.Join(root, "etc/config/force-no-coprocessor-update"))
	if err := s.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); err == nil || !strings.Contains(err.Error(), "staged") {
		t.Fatalf("flash with a staged update: %v", err)
	}
}

func TestRadioFirmwareModulesVariants(t *testing.T) {
	root := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(c), 0o644)
	}
	// a CCU3-like box: the legacy module, its unversioned shipped file named in the fwmap,
	// plus a Telekom stick for HmIP
	w("var/hm_mode", "HM_HMRF_DEV='HM-MOD-RPI-PCB'\nHM_HMRF_DEVNODE='/dev/raw-uart'\nHM_HMRF_VERSION='2.8.6'\nHM_HMIP_DEV='HMIP-RFUSB-TK'\nHM_HMIP_DEVNODE='/dev/raw-uart1'\nHM_HMIP_VERSION='4.0.5'\n")
	w("firmware/HM-MOD-UART/dualcopro_si1002_update_blhm.eq3", "x")
	w("firmware/HM-MOD-UART/dualcopro_si1002_update_blhm-2.2.1.eq3", "y")
	w("firmware/HM-MOD-UART/coprocessor_update.eq3", "z")
	w("firmware/HM-MOD-UART/fwmap", "CCU2\tdualcopro_si1002_update_blhm.eq3\t2.8.6\n")
	s, _ := newRadioFW(t, root, &fakeCopro{version: "2.8.6"})
	st := s.Status()
	if len(st.Modules) != 2 {
		t.Fatalf("modules: %+v", st.Modules)
	}
	legacy, tk := st.Modules[0], st.Modules[1]
	if legacy.Device != "HM-MOD-RPI-PCB" || legacy.Family != "legacy" || legacy.Verdict != "up-to-date" || legacy.Newest != "dualcopro_si1002_update_blhm.eq3" || len(legacy.Files) != 2 {
		t.Fatalf("legacy: %+v", legacy)
	}
	if legacy.Files[0].Version != "2.8.6" || legacy.Files[0].Direction != "same" || legacy.Files[1].Version != "2.2.1" || legacy.Files[1].Direction != "downgrade" {
		t.Fatalf("legacy files: %+v", legacy.Files)
	}
	if tk.Device != "HMIP-RFUSB-TK" || tk.Verdict != "skipped" || tk.Flashable || tk.Note == "" || tk.NoteCode != "no-eq3-firmware" || tk.RunningVersion != "4.0.5" {
		t.Fatalf("tk: %+v", tk)
	}
	if err := s.Flash("HMIP-RFUSB-TK", "x.eq3"); err == nil {
		t.Fatal("a skipped module must not flash")
	}
	// no files at all
	_ = os.RemoveAll(filepath.Join(root, "firmware"))
	if st := s.Status(); st.Modules[0].Verdict != "no-file" || len(st.Modules[0].Files) != 0 {
		t.Fatalf("no-file: %+v", st.Modules[0])
	}
}

func TestRadioFirmwareUpload(t *testing.T) {
	root := piRoot(t)
	s, _ := newRadioFW(t, root, &fakeCopro{version: "4.4.18"})
	f, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3", bytes.NewReader([]byte("firmware bytes")))
	if err != nil {
		t.Fatal(err)
	}
	if f.Source != "uploaded" || f.Version != "4.4.22" || f.Size != 14 || len(f.SHA256) != 64 || f.Path != "/usr/local/etc/config/radio-firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.22.eq3" {
		t.Fatalf("uploaded: %+v", f)
	}
	dir := filepath.Join(root, "usr/local/etc/config/radio-firmware/HmIP-RFUSB")
	if b, err := os.ReadFile(filepath.Join(dir, f.Name)); err != nil || string(b) != "firmware bytes" {
		t.Fatalf("landed: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dir, ".nobackup")); err != nil {
		t.Fatal(".nobackup missing: the nightly backup would tar the images")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "usr/local/etc/occulite/staging")); len(entries) != 1 {
		t.Fatalf("staging not cleaned: %v", entries)
	}
	if st := s.Status(); st.Modules[0].Verdict != "newer-available" || st.Modules[0].Newest != f.Name {
		t.Fatalf("after upload: %+v", st.Modules[0])
	}
	refused := []struct{ name, msg string }{
		{"firmware.zip", ".eq3"},
		{"dualcopro_update_blhmip-4.4.22.eq3/../x.eq3", "fit"},
		{"coprocessor_update.eq3", "fit"},
		{"dualcopro_si1002_update_blhm-2.2.1.eq3", "fit"},
		{"dualcopro_update_blhmip-4.4.22.eq3 ", ".eq3"},
	}
	for _, c := range refused {
		if _, err := s.Upload("HMIP-RFUSB", c.name, bytes.NewReader([]byte("x"))); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%q: %v", c.name, err)
		}
	}
	if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.23.eq3", bytes.NewReader(make([]byte, radioFirmwareMaxSize+1))); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("oversized: %v", err)
	}
	if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.23.eq3", bytes.NewReader(nil)); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty: %v", err)
	}
	if _, err := s.Upload("RPI-RF-MOD", "dualcopro_update_blhmip-4.4.22.eq3", bytes.NewReader([]byte("x"))); err == nil || !strings.Contains(err.Error(), "not detected") {
		t.Errorf("a module the box does not have: %v", err)
	}
	if _, err := s.Upload("HMIP-RFUSB-TK", "dualcopro_update_blhmip-4.4.22.eq3", bytes.NewReader([]byte("x"))); err == nil {
		t.Error("the skipped module took an upload")
	}
	// the cap: ten per module, then a delete makes room
	for i := 0; i < radioFirmwareMaxFiles-1; i++ {
		if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-5.0."+string(rune('0'+i))+".eq3", bytes.NewReader([]byte("x"))); err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
	}
	if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-6.0.0.eq3", bytes.NewReader([]byte("x"))); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("over the cap: %v", err)
	}
	// replacing an existing name is not a new file
	if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3", bytes.NewReader([]byte("again"))); err != nil {
		t.Errorf("replace: %v", err)
	}
	if err := s.Delete("HMIP-RFUSB", "dualcopro_update_blhmip-5.0.0.eq3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-6.0.0.eq3", bytes.NewReader([]byte("x"))); err != nil {
		t.Errorf("after a delete: %v", err)
	}
	// a shipped file is never deleted
	if err := s.Delete("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.18.eq3"); err == nil {
		t.Error("a shipped file was deleted")
	}
	if _, err := os.Stat(filepath.Join(root, "firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3")); err != nil {
		t.Error("the shipped file is gone")
	}
	if err := s.Delete("HMIP-RFUSB", "../../../../firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3"); err == nil {
		t.Error("a path walked out of the upload directory")
	}
}

func TestRadioFirmwareFlash(t *testing.T) {
	root := piRoot(t)
	fp := &fakeCopro{version: "4.4.18", flashSets: "4.4.22"}
	s, svc := newRadioFW(t, root, fp)
	j := &testJournal{}
	s.Journal = j
	if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3", bytes.NewReader([]byte("new"))); err != nil {
		t.Fatal(err)
	}
	if err := s.Flash("HMIP-RFUSB", "nope.eq3"); err == nil {
		t.Fatal("an unknown file flashed")
	}
	if err := s.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); !errors.Is(err, ErrFlashRunning) {
		t.Fatalf("second flash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.StateDir, "running.json")); err != nil {
		t.Fatal("running.json not written")
	}
	st := waitDone(t, s)
	a := st.Last
	if a == nil || !a.OK || a.Error != "" || a.Before != "4.4.18" || a.After != "4.4.22" || a.Exit != 0 || a.Finished == nil {
		t.Fatalf("attempt: %+v", a)
	}
	// the order of 41.3: stop in reverse, read, flash, read, redetect, start in boot order
	wantSvc := []string{"hmipserver stop", "rfd stop", "multimacd stop", "multimacd start", "rfd start", "hmipserver start"}
	if got := svc.snapshot(); !reflect.DeepEqual(got, wantSvc) {
		t.Fatalf("service calls: %v", got)
	}
	wantPriv := []string{
		"run detect_radio_module /dev/raw-uart1",
		"flash hmip /dev/raw-uart1 dualcopro_update_blhmip-4.4.22.eq3 4.4.22",
		"run detect_radio_module /dev/raw-uart1",
		"run systemctl restart --no-pager -- occu-init-rf-hardware.service",
	}
	if got := fp.snapshot(); !reflect.DeepEqual(got, wantPriv) {
		t.Fatalf("helper calls: %v", got)
	}
	// task 102: the phases are the run's journal entries, one per line, in order
	phases := []string{"hmipserver stopped", "rfd stopped", "multimacd stopped", "/dev/raw-uart1 is free", "coprocessor reports 4.4.18", "flashing dualcopro_update_blhmip-4.4.22.eq3 (4.4.22, hmip)", "hmip-copro-update: writing 104616 bytes", "flasher exited 0", "coprocessor reports 4.4.22", "re-running the radio detection", "multimacd started", "rfd started", "hmipserver started", "/dev/mmd_bidcos and /dev/mmd_hmip are back", "done: the coprocessor runs 4.4.22"}
	if !runlog.ValidID(a.RunID) {
		t.Fatalf("run id %q", a.RunID)
	}
	joined := strings.Join(j.messages(a.RunID), "\n")
	last := -1
	for _, p := range phases {
		i := strings.Index(joined, p)
		if i < 0 || i < last {
			t.Fatalf("phase %q missing or out of order in\n%s", p, joined)
		}
		last = i
	}
	for _, e := range j.ofRun(a.RunID) {
		if rjField(e, "OCCULITE_RUN") != "radio-firmware" || rjField(e, "SYSLOG_IDENTIFIER") != "radio-firmware" || rjField(e, "OCCULITE_RUN_MODULE") != "HMIP-RFUSB" || rjField(e, "PRIORITY") != "6" {
			t.Errorf("entry %v", e)
		}
		// the flasher's own output is marked as the flasher's, the phases are not
		isOutput := strings.HasPrefix(rjField(e, "MESSAGE"), "hmip-copro-update") || rjField(e, "MESSAGE") == "done"
		if (rjField(e, "OCCULITE_RUN_SOURCE") == "flasher") != isOutput {
			t.Errorf("source of %v", e)
		}
	}
	// the state keeps the result and the run id, not the lines
	b, _ := os.ReadFile(filepath.Join(s.StateDir, "last.json"))
	if strings.Contains(string(b), `"lines"`) || !strings.Contains(string(b), `"run_id": "`+a.RunID+`"`) {
		t.Errorf("last.json:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(s.StateDir, "running.json")); err == nil {
		t.Fatal("running.json left behind")
	}
	if _, err := os.Stat(filepath.Join(s.StateDir, "last.json")); err != nil {
		t.Fatal("last.json not written")
	}
	// /var/hm_mode still says 4.4.18 (the fake detection does not rewrite it): the page shows
	// the version read after the flash, and says where it came from
	if m := st.Modules[0]; m.RunningVersion != "4.4.22" || m.Verdict != "up-to-date" || m.Note == "" {
		t.Fatalf("module after the flash: %+v", m)
	}
	// a new service instance reads the attempt back
	s2 := &RadioFirmware{Root: Root(root), Services: svc, StateDir: s.StateDir}
	s2.Load(context.Background())
	if l := s2.Status().Last; l == nil || l.After != "4.4.22" {
		t.Fatalf("reloaded: %+v", l)
	}
}

func TestRadioFirmwareFlashFailures(t *testing.T) {
	// the flasher exits non-zero: failed, the daemons up again, the version unchanged
	root := piRoot(t)
	fp := &fakeCopro{version: "4.4.18", flashExit: 1}
	s, svc := newRadioFW(t, root, fp)
	if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3", bytes.NewReader([]byte("truncated"))); err != nil {
		t.Fatal(err)
	}
	if err := s.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); err != nil {
		t.Fatal(err)
	}
	st := waitDone(t, s)
	if a := st.Last; a.OK || a.Exit != 1 || !strings.Contains(a.Error, "exited 1") || a.After != "4.4.18" {
		t.Fatalf("attempt: %+v", a)
	}
	if got := svc.snapshot(); !reflect.DeepEqual(got, []string{"hmipserver stop", "rfd stop", "multimacd stop", "multimacd start", "rfd start", "hmipserver start"}) {
		t.Fatalf("daemons after a failed flash: %v", got)
	}
	if !svc.running["multimacd"] || !svc.running["rfd"] || !svc.running["hmipserver"] {
		t.Fatal("the stack is not up after the failure")
	}
	if st.Modules[0].RunningVersion != "4.4.18" {
		t.Fatalf("version after a failed flash: %s", st.Modules[0].RunningVersion)
	}

	// a zero exit with the version unchanged is a failure too
	fp2 := &fakeCopro{version: "4.4.18"}
	s2, svc2 := newRadioFW(t, root, fp2)
	if err := s2.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); err != nil {
		t.Fatal(err)
	}
	st = waitDone(t, s2)
	if a := st.Last; a.OK || !strings.Contains(a.Error, "still reports 4.4.18") {
		t.Fatalf("unchanged: %+v", a)
	}
	if !svc2.running["hmipserver"] {
		t.Fatal("hmipserver down after the unchanged-version failure")
	}

	// a stop that fails: nothing flashed, what was stopped is started again
	fp3 := &fakeCopro{version: "4.4.18", flashSets: "4.4.22"}
	s3, svc3 := newRadioFW(t, root, fp3)
	svc3.failing = "rfd"
	if err := s3.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); err != nil {
		t.Fatal(err)
	}
	st = waitDone(t, s3)
	if a := st.Last; a.OK || !strings.Contains(a.Error, "stopping rfd") {
		t.Fatalf("stop failure: %+v", a)
	}
	if got := svc3.snapshot(); !reflect.DeepEqual(got, []string{"hmipserver stop", "rfd stop", "hmipserver start"}) {
		t.Fatalf("calls after a failed stop: %v", got)
	}
	for _, c := range fp3.snapshot() {
		if strings.HasPrefix(c, "flash") {
			t.Fatal("flashed although the stack was not down")
		}
	}

	// the radio is busy: refused before anything is stopped
	s4, svc4 := newRadioFW(t, root, &fakeCopro{version: "4.4.18"})
	s4.Health = busyChecker{busy: true, which: "HmIP-RF 0xBD0A07"}
	if err := s4.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("busy: %v", err)
	}
	if len(svc4.snapshot()) != 0 {
		t.Fatal("a refused flash touched the daemons")
	}
}

type busyChecker struct {
	busy  bool
	which string
}

func (b busyChecker) Busy(int) (bool, string) { return b.busy, b.which }

func TestRadioFirmwareNodeGone(t *testing.T) {
	// the stick re-enumerates after the flash: raw-uart1 is gone, the module comes back on
	// raw-uart2, found by its product string
	root := piRoot(t)
	fp := &fakeCopro{version: "4.4.18", flashSets: "4.4.22"}
	// the flash moves the class device
	fp.onFlash = func() {
		_ = os.RemoveAll(filepath.Join(root, "sys/class/raw-uart/raw-uart1"))
		_ = os.MkdirAll(filepath.Join(root, "sys/class/raw-uart/raw-uart2"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "sys/class/raw-uart/raw-uart2/device_type"), []byte("eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3\n"), 0o644)
	}
	s, _ := newRadioFW(t, root, fp)
	j := &testJournal{}
	s.Journal = j
	if _, err := s.Upload("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3", bytes.NewReader([]byte("new"))); err != nil {
		t.Fatal(err)
	}
	if err := s.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.22.eq3"); err != nil {
		t.Fatal(err)
	}
	st := waitDone(t, s)
	a := st.Last
	if !a.OK || a.After != "4.4.22" {
		t.Fatalf("attempt: %+v", a)
	}
	if lines := j.messages(a.RunID); !strings.Contains(strings.Join(lines, "\n"), "the module is on /dev/raw-uart2 now") {
		t.Fatalf("lines: %v", lines)
	}
	calls := fp.snapshot()
	if calls[len(calls)-2] != "run detect_radio_module /dev/raw-uart2" {
		t.Fatalf("the version was not read from the new node: %v", calls)
	}
}

func TestRadioFirmwareLoadRecovers(t *testing.T) {
	root := piRoot(t)
	fp := &fakeCopro{version: "4.4.18"}
	s, svc := newRadioFW(t, root, fp)
	j := &testJournal{}
	s.Journal = j
	svc.running = map[string]bool{}
	// occulited died mid-flash: running.json is there, nothing else - written by a binary from
	// before task 102, with the lines of the run so far in it
	started := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	stale := map[string]any{"module": "HMIP-RFUSB", "device_node": "/dev/raw-uart1", "file": "/firmware/x.eq3", "started": started, "lines": []string{"12:00:00 hmipserver stopped", "12:00:01 rfd stopped"}}
	if err := writeJSONFile(filepath.Join(s.StateDir, "running.json"), stale); err != nil {
		t.Fatal(err)
	}
	s.Load(context.Background())
	st := s.Status()
	if st.Running != nil || st.Last == nil || st.Last.OK || !strings.Contains(st.Last.Error, "restarted") || st.Last.Finished == nil {
		t.Fatalf("recovered: %+v", st.Last)
	}
	// the kept lines and the closing one are in the journal under a run id of the start
	if id := st.Last.RunID; !strings.HasPrefix(id, "20260912T120000-") || strings.Join(j.messages(id), " | ") != "12:00:00 hmipserver stopped | 12:00:01 rfd stopped | occulited restarted: the attempt is closed as failed, the radio daemons are started" {
		t.Fatalf("run %q: %v", id, j.messages(id))
	}
	if e := j.ofRun(st.Last.RunID); rjField(e[2], "PRIORITY") != "3" {
		t.Errorf("the closing line: %v", e[2])
	}
	if b, _ := os.ReadFile(filepath.Join(s.StateDir, "last.json")); strings.Contains(string(b), `"lines"`) {
		t.Errorf("last.json kept lines:\n%s", b)
	}
	if got := svc.snapshot(); !reflect.DeepEqual(got, []string{"multimacd start", "rfd start", "hmipserver start"}) {
		t.Fatalf("the daemons were not started: %v", got)
	}
	if _, err := os.Stat(filepath.Join(s.StateDir, "running.json")); err == nil {
		t.Fatal("running.json left behind")
	}
	// a clean start changes nothing
	svc.calls = nil
	s.Load(context.Background())
	if len(svc.snapshot()) != 0 {
		t.Fatal("a clean load touched the daemons")
	}
}

// task 102: a last.json from before the lines went to the journal is read, its lines are written
// into the journal at the priority they say, and the file is written again without them - once.
func TestRadioFirmwareLastMigrated(t *testing.T) {
	root := piRoot(t)
	s, svc := newRadioFW(t, root, &fakeCopro{version: "4.4.18"})
	j := &testJournal{}
	s.Journal = j
	old := map[string]any{"module": "HMIP-RFUSB", "device_node": "/dev/raw-uart1", "file": "/firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3", "file_version": "4.4.18",
		"started": time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC), "finished": time.Date(2026, 9, 10, 1, 2, 0, 0, time.UTC), "ok": false, "error": "the flasher exited 1", "exit": 1,
		"lines": []string{"01:00:00 hmipserver stopped", "01:01:00   | writing", "01:02:00 failed: the flasher exited 1"}}
	if err := writeJSONFile(filepath.Join(s.StateDir, "last.json"), old); err != nil {
		t.Fatal(err)
	}
	s.Load(context.Background())
	last := s.Status().Last
	if last == nil || !strings.HasPrefix(last.RunID, "20260910T010000-") || last.Error != "the flasher exited 1" {
		t.Fatalf("%+v", last)
	}
	entries := j.ofRun(last.RunID)
	if len(entries) != 3 || rjField(entries[0], "PRIORITY") != "6" || rjField(entries[2], "PRIORITY") != "3" || rjField(entries[1], "MESSAGE") != "01:01:00   | writing" {
		t.Fatalf("%v", entries)
	}
	b, _ := os.ReadFile(filepath.Join(s.StateDir, "last.json"))
	if strings.Contains(string(b), `"lines"`) || !strings.Contains(string(b), last.RunID) {
		t.Fatalf("last.json:\n%s", b)
	}
	// a second start finds nothing to move, and a clean load touches no daemon
	s2 := &RadioFirmware{Root: Root(root), Services: svc, StateDir: s.StateDir, Journal: j}
	s2.Load(context.Background())
	if len(j.ofRun(last.RunID)) != 3 || s2.Status().Last.RunID != last.RunID || len(svc.snapshot()) != 0 {
		t.Fatalf("second load: %d entries, %+v", len(j.ofRun(last.RunID)), s2.Status().Last)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"4.4.18", "4.4.22", -1}, {"4.4.22", "4.4.18", 1}, {"4.4.18", "4.4.18", 0},
		{"4.10.0", "4.9.9", 1}, {"4.4", "4.4.0", -1}, {"", "1", -1}, {"1", "", 1}, {"", "", 0},
		{"2.8.6", "2.2.1", 1}, {"1.0.a", "1.0.b", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("%q vs %q: %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if direction("4.4.22", "4.4.18") != "upgrade" || direction("4.4.18", "4.4.22") != "downgrade" || direction("1", "1") != "same" || direction("", "1") != "unknown" {
		t.Error("direction")
	}
	fm := parseFwmap("#Type Filename Version\n\nCCU2\tdualcopro_si1002_update_blhm.eq3\t2.8.6\t# x\n#CCU2 other.eq3 1.4.1\nbad line\n")
	if !reflect.DeepEqual(fm, map[string]string{"dualcopro_si1002_update_blhm.eq3": "2.8.6"}) {
		t.Errorf("fwmap: %v", fm)
	}
}

// task 147: the HM-CFG-USB-2 - listed from sysfs by its serial with the version from bcdDevice,
// an uploaded hmusbif.<hex>.enc with its version from the hex, the flash through the helper and
// the version read again once the adapter is back
func TestRadioFirmwareHMCFGUSB(t *testing.T) {
	root := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(c), 0o644)
	}
	w("var/hm_mode", "HM_HMRF_DEV='HM-CFG-USB-2'\nHM_HMRF_DEVNODE=''\nHM_HMIP_DEV='HM-MOD-RPI-PCB'\nHM_HMIP_DEVNODE='/dev/raw-uart1'\nHM_HMIP_VERSION='2.8.6'\n")
	usb := "sys/bus/usb/devices/1-1.4/"
	w(usb+"idVendor", "1b1f\n")
	w(usb+"idProduct", "c00f\n")
	w(usb+"serial", "JEQ9000002\n")
	w(usb+"bcdDevice", "0956\n")
	fp := &fakeCopro{version: "2.8.6"}
	fp.onFlash = func() { w(usb+"bcdDevice", "0967\n") }
	s, svc := newRadioFW(t, root, fp)
	st := s.Status()
	var ad *FirmwareModule
	for i := range st.Modules {
		if st.Modules[i].Device == "HM-CFG-USB-2" {
			ad = &st.Modules[i]
		}
	}
	if ad == nil || ad.DeviceNode != "usb:JEQ9000002" || ad.RunningVersion != "0.956" || ad.Family != "hmcfgusb" || ad.Verdict != "no-file" || !ad.Flashable || len(ad.Protocols) != 1 {
		t.Fatalf("adapter: %+v (%d modules)", ad, len(st.Modules))
	}
	if _, err := s.Upload("HM-CFG-USB-2", "dualcopro_update_blhmip-4.4.22.eq3", bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("an .eq3 accepted for the adapter")
	}
	f, err := s.Upload("HM-CFG-USB-2", "hmusbif.03c7.enc", bytes.NewReader([]byte("enc")))
	if err != nil || f.Version != "0.967" {
		t.Fatalf("upload: %v %+v", err, f)
	}
	for _, m := range s.Status().Modules {
		if m.Device == "HM-CFG-USB-2" && (m.Verdict != "newer-available" || m.Files[0].Direction != "upgrade") {
			t.Fatalf("after the upload: %+v", m)
		}
	}
	if err := s.Flash("HM-CFG-USB-2", "hmusbif.03c7.enc"); err != nil {
		t.Fatal(err)
	}
	a := waitDone(t, s).Last
	if a == nil || !a.OK || a.Before != "0.956" || a.After != "0.967" {
		t.Fatalf("attempt: %+v", a)
	}
	if !strings.Contains(strings.Join(fp.calls, "\n"), "flash hmcfgusb usb:JEQ9000002 hmusbif.03c7.enc 0.967") {
		t.Fatalf("calls: %v", fp.calls)
	}
	// rfd alone stops and starts, and the detection does not run again
	if got := svc.snapshot(); !reflect.DeepEqual(got, []string{"rfd stop", "rfd start"}) {
		t.Fatalf("units: %v", got)
	}
}

// task 137: an HmIP-RFUSB the radio detection found but could not read is in no role - /var/hm_mode
// does not name it - and is listed as unusable with the newest file, flashable; the flash does not
// stop at the version it cannot read before. A stick that answered and is merely not chosen (.116's
// RFUSB beside the HB-RF-ETH) is not unusable.
func TestRadioFirmwareUnusableStick(t *testing.T) {
	root := piRoot(t)
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("var/hm_mode", "HM_HMIP_DEV=''\nHM_HMIP_DEVNODE=''\nHM_HMRF_DEV=''\nHM_HMRF_DEVNODE=''\nHM_MODE='NORMAL'\n")
	w("sys/class/raw-uart/raw-uart1/open_count", "0\n")
	det := `{"modules":[{"name":"raw-uart","node":"/dev/raw-uart","device_type":"GPIO@fe201000.serial","gpio":true,"probe":"none"},` +
		`{"name":"raw-uart1","node":"/dev/raw-uart1","device_type":"eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3","probe":"timeout","detail":"no answer within 20s"}],"log":[]}`
	w("run/occulite/radio/modules.json", det)
	fp := &fakeCopro{version: "", flashSets: "4.4.18"}
	s, _ := newRadioFW(t, root, fp)
	st := s.Status()
	if len(st.Modules) != 1 {
		t.Fatalf("modules %+v", st.Modules)
	}
	m := st.Modules[0]
	if m.Device != "HMIP-RFUSB" || m.DeviceNode != "/dev/raw-uart1" || m.Verdict != "unusable" || !m.Flashable || m.RunningVersion != "" || m.Newest != "dualcopro_update_blhmip-4.4.18.eq3" || m.Note == "" {
		t.Fatalf("the unusable stick %+v", m)
	}
	// the flash goes on past the unreadable version and records that it was an unusable stick
	if err := s.Flash("HMIP-RFUSB", "dualcopro_update_blhmip-4.4.18.eq3"); err != nil {
		t.Fatal(err)
	}
	a := waitDone(t, s).Last
	if a == nil || !a.OK || !a.Unusable || a.Before != "" || a.After != "4.4.18" {
		t.Fatalf("attempt %+v", a)
	}
	// until the detection runs again the stick is not called unusable after that flash
	if m := s.Status().Modules[0]; m.Verdict == "unusable" || m.RunningVersion != "4.4.18" {
		t.Fatalf("after the flash %+v", m)
	}

	// a stick that answered but is in no role is no unusable one
	w("run/occulite/radio/modules.json", strings.Replace(det, `"probe":"timeout"`, `"probe":"ok","hardware":"HMIP-RFUSB","version":"4.4.18"`, 1))
	s2, _ := newRadioFW(t, root, &fakeCopro{version: "4.4.18"})
	if mods := s2.Status().Modules; len(mods) != 0 {
		t.Fatalf("an answering stick listed: %+v", mods)
	}
	// nor the Telekom stick, which eQ-3 ships no firmware for, nor a node a role uses
	w("run/occulite/radio/modules.json", strings.Replace(det, "eQ-3 HmIP-RFUSB@", "eQ-3 HmIP-RFUSB-TK@", 1))
	if mods := s2.Status().Modules; len(mods) != 0 {
		t.Fatalf("the TK listed: %+v", mods)
	}
	w("var/hm_mode", "HM_HMIP_DEV='HMIP-RFUSB'\nHM_HMIP_DEVNODE='/dev/raw-uart1'\nHM_HMIP_VERSION='4.4.18'\n")
	w("run/occulite/radio/modules.json", det)
	if mods := s2.Status().Modules; len(mods) != 1 || mods[0].Verdict == "unusable" {
		t.Fatalf("a node in a role: %+v", mods)
	}
}

func TestRadioModuleType(t *testing.T) {
	for typ, want := range map[string]bool{"RPI-RF-MOD": true, "rpi-rf-mod": true, "HmIP-RFUSB": true, "HmIP-RFUSB-TK": true, "HM-MOD-RPI-PCB": true, "HM-CFG-USB-2": true, " RPI-RF-MOD ": true, "HmIP-HAP": false, "HmIPW-DRAP": false, "HmIP-PDT": false, "": false} {
		if got := RadioModuleType(typ); got != want {
			t.Errorf("%q: %v, want %v", typ, got, want)
		}
	}
}
