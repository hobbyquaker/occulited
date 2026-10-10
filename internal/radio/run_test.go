package radio

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder wraps the fake probe: every other command (systemctl, modprobe, eq3configcmd, rsync,
// cp) is recorded and answered as the test says.
type recorder struct {
	mu      sync.Mutex
	probe   *fakeProbe
	answers map[string]string // by program base name
	fails   map[string]bool   // by program base name: the command answers an error
	calls   []string
}

func (r *recorder) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	base := filepath.Base(name)
	switch base {
	case "detect_radio_module", "uname":
		return r.probe.run(ctx, name, args...)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, base+" "+strings.Join(args, " "))
	if r.fails[base] || r.fails[base+" "+strings.Join(args, " ")] {
		return nil, errors.New(base + ": not found")
	}
	if a, ok := r.answers[base]; ok {
		return []byte(a), nil
	}
	return nil, nil
}

func (r *recorder) called(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// fakeUsers stands in for the image's users table.
func fakeUsers(t *testing.T) {
	t.Helper()
	ids := map[string]string{"root": "0", "rfd": "8110", "hmipserver": "8111", "multimacd": "8112", "hs485d": "8113", "hmlangw": "8114", "raw-uart": "8120", "eq3loop": "8121", "mmd-bidcos": "8122", "mmd-hmip": "8123", "lock": "54"}
	oldU, oldG, oldID := lookupUser, lookupGroup, lookupUserID
	lookupUser = func(n string) (*user.User, error) {
		if id, ok := ids[n]; ok {
			return &user.User{Uid: id, Gid: id, Username: n}, nil
		}
		return nil, errors.New("unknown user " + n)
	}
	lookupGroup = func(n string) (*user.Group, error) {
		if id, ok := ids[n]; ok {
			return &user.Group{Gid: id, Name: n}, nil
		}
		return nil, errors.New("unknown group " + n)
	}
	lookupUserID = func(id string) (*user.User, error) {
		for n, i := range ids {
			if i == id {
				return &user.User{Uid: id, Gid: id, Username: n}, nil
			}
		}
		return nil, errors.New("unknown id " + id)
	}
	t.Cleanup(func() { lookupUser, lookupGroup, lookupUserID = oldU, oldG, oldID })
}

// recordOwnership replaces chown and chmod with recorders (a test host cannot chown to the
// daemons' ids).
func recordOwnership(t *testing.T, root string) *[]string {
	t.Helper()
	var calls []string
	var mu sync.Mutex
	oldChown, oldChmod := chownFn, chmodFn
	chownFn = func(p string, uid, gid int) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, "chown "+strings.TrimSuffix(strings.TrimPrefix(p, root), ".tmp")+" "+itoa(uid)+":"+itoa(gid))
		return nil
	}
	chmodFn = func(p string, m os.FileMode) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, "chmod "+strings.TrimPrefix(p, root)+" "+m.String())
		return os.Chmod(p, m)
	}
	t.Cleanup(func() { chownFn, chmodFn = oldChown, oldChmod })
	return &calls
}

func itoa(i int) string { return strconv.Itoa(i) }

func has(calls []string, s string) bool {
	for _, c := range calls {
		if strings.Contains(c, s) {
			return true
		}
	}
	return false
}

// boxRoot is a sandbox with the image's templates and a dual-stack module on the header.
func boxRoot(t *testing.T, nodes map[string]string) (string, *recorder) {
	t.Helper()
	root := sandbox(t, nodes)
	for _, d := range []string{"var/etc", "var/status", "etc/config_templates", "run/occulite/radio", "run/lock", "media", "usr/local"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	in := inputs()
	write("var/hm_mode", "HM_HOST='rpi3'\nHM_MODE='NORMAL'\nHM_RTC='rx8130'\nHM_LED_GREEN='/sys/class/leds/ACT'\n")
	write("etc/config_templates/rfd.conf", rfdTemplate)
	write("etc/config_templates/multimacd.conf", in.TemplateMultimacdConf)
	write("etc/config_templates/crRFD.conf", in.TemplateCrRFDConf)
	write("etc/config_templates/InterfacesList.xml", in.TemplateInterfacesList)
	write("etc/config_templates/hmip_networkkey.conf", "Network.Key=x\n")
	write("etc/config_templates/log4j2.xml", "<Configuration>\n  <Appenders><Syslog name=\"SYSLOG\" host=\"127.0.0.1\"/></Appenders>\n  <Loggers>\n    <Root level=\"warn\">\n      <AppenderRef ref=\"File\"/>\n    </Root>\n  </Loggers>\n</Configuration>\n")
	write("etc/HMServer.conf", in.HMServerConf)
	write("etc/hmipserver.default", "HMIP_BIND_ADDRESS=127.0.0.1\n")
	write("etc/config/syslog", "LOGLEVEL_RFD=4\nLOGLEVEL_HMIP=INFO\n")
	write("etc/config/ids", "BidCoS-Address=0x000000\n")
	write("proc/meminfo", "MemTotal:        946000 kB\n")
	write("proc/mounts", "/dev/mmcblk0p3 /usr/local ext4 rw 0 0\n")
	write("dev/eq3loop", "")
	fp := &fakeProbe{answers: map[string]string{"raw-uart": "RPI-RF-MOD 0000000A03 3014F711A0001F0000000A03 0x1F6C2E 0x3FAE2C 4.4.22"}}
	return root, &recorder{probe: fp, answers: map[string]string{"systemctl": "inactive\ninactive\ninactive\ninactive\ninactive\n"}}
}

func TestRunWritesTheBox(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart": "GPIO@3f201000.serial"})
	calls := recordOwnership(t, root)
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, f) }
	d := Detector{Root: root, Run: rec.run, GPIOLimit: 0, Sleep: func(time.Duration) {}}
	r, err := Run(context.Background(), root, d, logf)
	if err != nil {
		t.Fatal(err)
	}
	p := r.Render.Plan
	if !p.Multimacd.Run || !p.RFD.Run || p.HmIPServer.Node != "/dev/mmd_hmip" || p.HS485D.Run {
		t.Fatalf("plan: %+v", p)
	}
	read := func(rel string) string { return readFile(filepath.Join(root, rel)) }
	// the files the old chain wrote
	hm := ParseKV(read("var/hm_mode"))
	if hm["HM_HOST"] != "rpi3" || hm["HM_LED_GREEN"] != "/sys/class/leds/ACT" || hm["HM_HMRF_DEV"] != "RPI-RF-MOD" || hm["HM_HMIP_DEVNODE"] != "/dev/raw-uart" {
		t.Fatalf("hm_mode: %v", hm)
	}
	if read("var/board_serial") != "0000000A03" || read("var/rf_address") != "0x1F6C2E" || read("var/hmip_board_sgtin") != "3014F711A0001F0000000A03" {
		t.Fatalf("var files: %q %q", read("var/board_serial"), read("var/rf_address"))
	}
	if !strings.Contains(read("etc/config/rfd.conf"), "[Interface 0]") || !strings.Contains(read("etc/config/rfd.conf"), "Listen IP = 127.0.0.1") {
		t.Fatalf("rfd.conf:\n%s", read("etc/config/rfd.conf"))
	}
	if !strings.Contains(read("var/etc/rfd.conf"), "Listen Port = 32001") {
		t.Fatalf("var rfd.conf:\n%s", read("var/etc/rfd.conf"))
	}
	if !strings.Contains(read("var/etc/multimacd.conf"), "Coprocessor Device Path = /dev/raw-uart") {
		t.Fatalf("multimacd.conf: %s", read("var/etc/multimacd.conf"))
	}
	if !strings.Contains(read("var/etc/crRFD.conf"), "Adapter.1.Port=/dev/mmd_hmip") || !strings.Contains(read("var/etc/crRFD.conf"), "Legacy.BindAddress=127.0.0.1") {
		t.Fatalf("crRFD.conf: %s", read("var/etc/crRFD.conf"))
	}
	if !strings.Contains(read("var/etc/HMServer.conf"), "diagramDatabasePath="+DiagramPath) {
		t.Fatalf("HMServer.conf: %s", read("var/etc/HMServer.conf"))
	}
	if names(ParseInterfaces(read("etc/config/InterfacesList.xml"))) != "BidCos-RF,VirtualDevices,HmIP-RF" {
		t.Fatalf("interfaces: %s", read("etc/config/InterfacesList.xml"))
	}
	if !strings.Contains(read("var/etc/log4j2.xml"), `level="info"`) {
		t.Fatalf("log4j2.xml: %s", read("var/etc/log4j2.xml"))
	}
	if read("etc/config/hmip_networkkey.conf") != "Network.Key=x\n" {
		t.Fatal("the network key template was not copied")
	}
	// the invalid ids file moved aside
	if exists(filepath.Join(root, "etc/config/ids")) {
		t.Fatal("an ids file with 0x000000 must be moved aside")
	}
	if old, _ := filepath.Glob(filepath.Join(root, "etc/config/ids_old-*")); len(old) != 1 {
		t.Fatalf("ids_old: %v", old)
	}
	if hm["HM_HMRF_ADDRESS_ACTIVE"] != "0x1F6C2E" {
		t.Fatalf("active address: %s", hm["HM_HMRF_ADDRESS_ACTIVE"])
	}
	// the markers, the environment files, the results
	for _, n := range []string{"multimacd", "rfd", "hmipserver"} {
		if !exists(filepath.Join(root, "run/occulite/radio", n+".enabled")) {
			t.Fatalf("%s.enabled missing", n)
		}
	}
	for _, n := range []string{"hs485d", "hmlangw"} {
		if exists(filepath.Join(root, "run/occulite/radio", n+".enabled")) {
			t.Fatalf("%s.enabled must not exist", n)
		}
	}
	if strings.Join(r.Enabled, " ") != "hmipserver multimacd rfd" {
		t.Fatalf("enabled: %v", r.Enabled)
	}
	// multimacd takes rfd's level, held at MultimacdMaxLevel (openccu-lite B-275)
	if read("run/occulite/radio/rfd.env") != "LOGLEVEL_RFD=4\n" || read("run/occulite/radio/multimacd.env") != "MULTIMACD_LOGLEVEL=2\n" {
		t.Fatalf("env: %q %q", read("run/occulite/radio/rfd.env"), read("run/occulite/radio/multimacd.env"))
	}
	env := ParseKV(read("run/occulite/radio/hmipserver.env"))
	if !strings.HasPrefix(env["HMIP_JAVA_OPTS"], "-Dos.arch=aarch64 -Dgnu.io.rxtx.SerialPorts=/dev/mmd_hmip -Xmx128m") || env["HMIP_CLASS"] != "de.eq3.ccu.server.ip.HMIPServer" || env["HMIP_DEVNODE"] != "/dev/mmd_hmip" {
		t.Fatalf("hmipserver env: %v", env)
	}
	for _, n := range []string{"modules.json", "plan.json", "render.json"} {
		if !exists(filepath.Join(root, "run/occulite/radio", n)) {
			t.Fatalf("%s not written", n)
		}
	}
	// the storage of a box with the userfs on the SD card: none; the diagram directory exists and is hmipserver's
	if exists(filepath.Join(root, "media/usb0")) {
		t.Fatal("no /media/usb0 link on an SD userfs")
	}
	if !isDir(filepath.Join(root, DiagramPath)) || !has(*calls, "chown /var/hmipserver 8111:8111") {
		t.Fatalf("diagram directory: %v", *calls)
	}
	if !has(*calls, "chown /etc/config/rfd.conf 0:8110") || !has(*calls, "chown /etc/config/hmip_networkkey.conf 8111:8111") {
		t.Fatalf("ownership: %v", *calls)
	}
	// the loop device was there, no modprobe; the run said what it enabled
	if rec.called("modprobe") {
		t.Fatalf("modprobe called: %v", rec.calls)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "run: detection") {
		t.Fatalf("log: %v", lines)
	}

	// a second run leaves the same content and the check matches
	if _, err := Run(context.Background(), root, d, logf); err != nil {
		t.Fatal(err)
	}
	// the plan is loadable for prep
	if lp, err := LoadPlan(root); err != nil || lp.Multimacd.Node != "/dev/raw-uart" {
		t.Fatalf("LoadPlan: %v %+v", err, lp)
	}
}

func TestRunRefusesWithDaemonsRunning(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart": "GPIO@3f201000.serial"})
	rec.answers["systemctl"] = "active\nactive\nactivating\ninactive\ninactive\n"
	d := Detector{Root: root, Run: rec.run, GPIOLimit: 0, Sleep: func(time.Duration) {}}
	_, err := Run(context.Background(), root, d, func(string, ...any) {})
	if !errors.Is(err, ErrDaemonsRunning) || !strings.Contains(err.Error(), "multimacd, rfd, hmipserver") {
		t.Fatalf("expected the refusal: %v", err)
	}
	if rec.probe.calls != nil {
		t.Fatal("the module must not be probed while the daemons run")
	}
}

func TestRunLoadsTheLoopModuleAndTheHBRFLED(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart": "HB-RF-USB-2@usb-1"})
	_ = os.Remove(filepath.Join(root, "dev/eq3loop"))
	for _, pin := range []string{"red", "green", "blue"} {
		_ = os.WriteFile(filepath.Join(root, "sys/class/raw-uart/raw-uart", pin+"_gpio_pin"), []byte("1\n"), 0o644)
	}
	d := Detector{Root: root, Run: rec.run, GPIOLimit: 0, Sleep: func(time.Duration) {}, Now: func() time.Time { return time.Now() }}
	r, err := Run(context.Background(), root, d, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if r.Render.Plan.HBRFLED == nil {
		t.Fatalf("an RPI-RF-MOD on an HB-RF adapter drives the LED: %+v", r.Render.Plan)
	}
	if !rec.called("modprobe eq3_char_loop") || !rec.called("modprobe -q rpi_rf_mod_led red_gpio_pin=1") {
		t.Fatalf("modprobes: %v", rec.calls)
	}
}

func TestPrepReadyStopped(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart": "GPIO@3f201000.serial"})
	calls := recordOwnership(t, root)
	d := Detector{Root: root, Run: rec.run, GPIOLimit: 0, Sleep: func(time.Duration) {}}
	r, err := Run(context.Background(), root, d, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	p := r.Render.Plan
	logf := func(string, ...any) {}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// multimacd: the loop device and the module's node get their groups
	*calls = nil
	if err := Prep(context.Background(), d, "multimacd", p, logf); err != nil {
		t.Fatal(err)
	}
	if !has(*calls, "chown /dev/raw-uart 0:8120") || !has(*calls, "chown /dev/eq3loop 0:8121") {
		t.Fatalf("multimacd prep: %v", *calls)
	}
	// rfd before multimacd made the endpoint: a failure (the unit retries), not a skip
	if err := Prep(context.Background(), d, "rfd", p, logf); err == nil || !strings.Contains(err.Error(), "/dev/mmd_bidcos is missing") {
		t.Fatalf("rfd prep without the endpoint: %v", err)
	}
	write("dev/mmd_bidcos", "")
	write("dev/mmd_hmip", "")
	*calls = nil
	if err := Prep(context.Background(), d, "rfd", p, logf); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(root, "etc/config/keys")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("keys: %v %v", err, st)
	}
	if !has(*calls, "chown /etc/config/keys 8110:8110") || !has(*calls, "chown /dev/mmd_bidcos 0:8122") || !has(*calls, "chown /var/RFD.handlers 8110:8110") {
		t.Fatalf("rfd prep: %v", *calls)
	}
	// hmipserver: a stale lock of another user goes, the handler files exist
	write("run/lock/LCK..mmd_hmip", "1")
	// openccu-lite B-253: the vendor's process wrote its store 0775/0664 (an older image, a
	// restored backup); the prep makes it the server's alone, links and owners aside
	data := filepath.Join(root, "etc/config/crRFD/data")
	if err := os.MkdirAll(filepath.Join(data, "old_20260101"), 0o775); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"3014F711A000040000000A01.dev", "3014F711A000040000000A01.apkx", "old_20260101/x.dev"} {
		if err := os.WriteFile(filepath.Join(data, f), []byte("x"), 0o664); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(data, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "etc/config/crRFD"), 0o775); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := Prep(context.Background(), d, "hmipserver", p, logf); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "run/lock/LCK..mmd_hmip")) {
		t.Fatal("a lock file of another user must be removed")
	}
	if !has(*calls, "chown /dev/mmd_hmip 0:8123") || !has(*calls, "chown /var/HMSERVER.handlers 8111:8111") || !has(*calls, "chown /etc/config/groups.gson 8111:8111") || !has(*calls, "chown /run/lock 0:54") {
		t.Fatalf("hmipserver prep: %v", *calls)
	}
	for path, want := range map[string]os.FileMode{
		"etc/config/crRFD": 0o750, "etc/config/crRFD/data": 0o700, "etc/config/crRFD/data/old_20260101": 0o700,
		"etc/config/crRFD/data/3014F711A000040000000A01.dev": 0o600, "etc/config/crRFD/data/3014F711A000040000000A01.apkx": 0o600,
		"etc/config/crRFD/data/old_20260101/x.dev": 0o600,
	} {
		if st, err := os.Lstat(filepath.Join(root, path)); err != nil || st.Mode().Perm() != want {
			t.Errorf("%s after prep: %v %v, want %o", path, err, st, want)
		}
	}
	if has(*calls, "chmod /etc/config/crRFD/data/link") || has(*calls, "chmod /etc/passwd") {
		t.Errorf("a link in the store was changed: %v", *calls)
	}
	// a second prep changes no mode (nothing to repair)
	*calls = nil
	if err := Prep(context.Background(), d, "hmipserver", p, logf); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "chmod /etc/config/crRFD") {
			t.Errorf("a mode set twice: %s", c)
		}
	}
	// task 180: the group store starts as the no-groups object (an empty file kills the server's
	// start thread), and one with groups is left alone
	if got, _ := os.ReadFile(filepath.Join(root, "etc/config/groups.gson")); string(got) != `{"groups":[]}` {
		t.Fatalf("groups.gson after prep: %q", got)
	}
	write("etc/config/groups.gson", `{"groups":[{"id":1}]}`)
	if err := Prep(context.Background(), d, "hmipserver", p, logf); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "etc/config/groups.gson")); string(got) != `{"groups":[{"id":1}]}` {
		t.Fatalf("a store with groups must be kept: %q", got)
	}
	// B-161: the level the Log page wrote into /etc/config/syslog is in the file the server reads,
	// without a reboot - the boot render is not the only writer any more
	write("etc/config/syslog", "LOGLEVEL_HMIP=DEBUG\n")
	if err := Prep(context.Background(), d, "hmipserver", p, logf); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "var/etc/log4j2.xml")); err != nil || !strings.Contains(string(b), `level="debug"`) {
		t.Fatalf("log4j2 after a level change: %v %q", err, firstLine(string(b)))
	}
	// hs485d and hmlangw
	write("etc/config/hs485d.conf", "Listen Port = 2000\n\n[Interface 0]\nType = HMWLGW\nSerial Number = LEQ9000004\n")
	if err := Prep(context.Background(), d, "hs485d", p, logf); err != nil {
		t.Fatal(err)
	}
	if !isDir(filepath.Join(root, "run/hs485d/run")) || !isDir(filepath.Join(root, "run/hs485d/log")) {
		t.Fatal("hs485d's private /var sources")
	}
	// B-165: the file the daemon writes its registered callbacks into has to exist on the real
	// /var, or the unit's bind has no source and the list lands in the private tmpfs
	if st, err := os.Stat(filepath.Join(root, "var/HS485D.handlers")); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("HS485D.handlers: %v %v", err, st)
	}
	if !has(*calls, "chown /var/HS485D.handlers 8113:8113") {
		t.Fatalf("HS485D.handlers ownership: %v", *calls)
	}
	// B-163: prep renders what the daemon starts from, from the file the page writes - the boot
	// run had none here (no wired interface when it planned), the port is forced, and the key the
	// section carries makes the file root:hs485d 0640
	varConf := filepath.Join(root, "var/etc/hs485d.conf")
	b, err := os.ReadFile(varConf)
	if err != nil || !strings.Contains(string(b), "Listen Port = 32000") || !strings.Contains(string(b), "LEQ9000004") {
		t.Fatalf("var hs485d.conf: %v %q", err, b)
	}
	if st, _ := os.Stat(varConf); st.Mode().Perm() != 0o640 || !has(*calls, "chown /var/etc/hs485d.conf 0:8113") {
		t.Fatalf("var hs485d.conf ownership: %v %v", st.Mode(), *calls)
	}
	// and the same for rfd: a gateway added to /etc/config/rfd.conf while the system runs is in
	// the file rfd starts from after the next prep (B-161)
	write("etc/config/rfd.conf", readFile(filepath.Join(root, "etc/config/rfd.conf"))+"\n[Interface 9]\nType = HMLGW2\nSerial Number = KEQ9000006\n")
	if err := Prep(context.Background(), d, "rfd", p, logf); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "var/etc/rfd.conf")); err != nil || !strings.Contains(string(b), "KEQ9000006") || !strings.Contains(string(b), "Listen Port = 32001") {
		t.Fatalf("var rfd.conf: %v %q", err, b)
	}
	if err := Prep(context.Background(), d, "hmlangw", p, logf); err != nil {
		t.Fatal(err)
	}
	if err := Prep(context.Background(), d, "nothing", p, logf); err == nil {
		t.Fatal("an unknown daemon is an error")
	}

	// ready: the status file with the main pid; without it, the time runs out
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })
	now := time.Now()
	d.Now = func() time.Time { now = now.Add(500 * time.Millisecond); return now }
	write("var/status/multimacd.status", "4242\n")
	if err := Ready(context.Background(), d, "multimacd", 4242, logf); err != nil {
		t.Fatal(err)
	}
	if err := Ready(context.Background(), d, "rfd", 4243, logf); err == nil || !strings.Contains(err.Error(), "did not report") {
		t.Fatalf("rfd ready without the status file: %v", err)
	}
	write("var/status/rfd.status", "4243")
	if err := Ready(context.Background(), d, "rfd", 4243, logf); err != nil {
		t.Fatal(err)
	}
	write("var/status/HMServerStarted", "")
	if err := Ready(context.Background(), d, "hmipserver", 1, logf); err != nil {
		t.Fatal(err)
	}
	// stopped: the marker goes, the diagram data is copied to a stick when there is one
	if err := Stopped(context.Background(), d, "hmipserver", logf); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "var/status/HMServerStarted")) || rec.called("rsync") {
		t.Fatal("stopped without a stick")
	}
	_ = os.MkdirAll(filepath.Join(root, "media/usb0/measurement"), 0o755)
	// task 33: with hmipserver.diagrams off (the default) no copy - the migrated data goes
	if err := Stopped(context.Background(), d, "hmipserver", logf); err != nil || rec.called("rsync") || exists(filepath.Join(root, "media/usb0/measurement")) {
		t.Fatalf("stopped with diagrams off: %v %v", err, rec.calls)
	}
	_ = os.MkdirAll(filepath.Join(root, "media/usb0/measurement"), 0o755)
	d.Diagrams = true
	if err := Stopped(context.Background(), d, "hmipserver", logf); err != nil || !rec.called("rsync -aogX") {
		t.Fatalf("stopped with a stick: %v %v", err, rec.calls)
	}
	if rec.called("cp -a") {
		t.Fatalf("cp ran although rsync worked: %v", rec.calls)
	}
	// B-146: an image without rsync must not fail the stop - and with it the unit. The stick's
	// copy is then replaced with cp, so a file the server deleted goes too.
	rec.fails = map[string]bool{"rsync": true}
	rec.calls = nil
	if err := Stopped(context.Background(), d, "hmipserver", logf); err != nil || !rec.called("cp -a") {
		t.Fatalf("stopped without rsync: %v %v", err, rec.calls)
	}
	if exists(filepath.Join(root, "media/usb0/measurement")) {
		t.Fatal("the stick's old copy was not removed before cp wrote the new one")
	}
}

func TestStop(t *testing.T) {
	root, rec := boxRoot(t, map[string]string{"raw-uart": "GPIO@3f201000.serial"})
	_ = os.WriteFile(filepath.Join(root, "var/hm_mode"), []byte("HM_HMRF_DEV='RPI-RF-MOD'\nHM_HMRF_DEVNODE='/dev/raw-uart'\nHM_HMRF_DEVTYPE='HB-RF-USB-2'\nHM_HMIP_DEVNODE='/dev/raw-uart'\n"), 0o644)
	d := Detector{Root: root, Run: rec.run}
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, f) }
	// no staged update: no handover
	Stop(context.Background(), d, logf)
	if rec.called("eq3configcmd") {
		t.Fatalf("no handover without a staged update: %v", rec.calls)
	}
	// a staged update (the recovery marker): one handover per node, the LED driver unloaded
	_ = os.WriteFile(filepath.Join(root, "usr/local/.recoveryMode"), nil, 0o644)
	_ = os.MkdirAll(filepath.Join(root, "proc"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "proc/modules"), []byte("rpi_rf_mod_led 16384 0 - Live 0x0\n"), 0o644)
	Stop(context.Background(), d, logf)
	if !rec.called("eq3configcmd update-coprocessor -p "+root+"/dev/raw-uart -bl -l 1") || !rec.called("rmmod rpi_rf_mod_led") {
		t.Fatalf("handover: %v", rec.calls)
	}
	n := 0
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "eq3configcmd") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("one handover for one shared node, got %d", n)
	}
}

func TestLog4j2ConfigAndEnvFiles(t *testing.T) {
	tmpl := "<Syslog host=\"127.0.0.1\"/>\n<Root level=\"warn\">\n  <AppenderRef ref=\"File\"/>\n</Root>\n<Logger level=\"error\" additivity=\"false\">\n"
	got := Log4j2Config(tmpl, map[string]string{"LOGHOST": "10.0.0.5"}, map[string]string{}, "DEBUG")
	if !strings.Contains(got, `host="10.0.0.5"`) || !strings.Contains(got, "<AppenderRef ref=\"File\"/>\n    <AppenderRef ref=\"SYSLOG\"/>\n") {
		t.Fatalf("loghost:\n%s", got)
	}
	// sed's greedy level=".*": the additivity attribute falls to it, as upstream
	if !strings.Contains(got, "<Root level=\"debug\">") || !strings.Contains(got, "<Logger level=\"debug\">\n") {
		t.Fatalf("levels:\n%s", got)
	}
	// openccu-lite task 299: a marked logger keeps its level whatever LOGLEVEL_HMIP says
	fixed := tmpl + "<Logger name=\"de.eq3.cbcs.server.local.base.internal.HMIPTRXInitialResponseListener\" level=\"info\"/><!-- occulite:fixed-level -->\n"
	got = Log4j2Config(fixed, map[string]string{}, map[string]string{"HMIP_LOG_STDOUT": "1"}, "WARN")
	if !strings.Contains(got, "HMIPTRXInitialResponseListener\" level=\"info\"/>") || !strings.Contains(got, "<Root level=\"warn\">") || strings.Count(got, `level="warn"`) != 2 {
		t.Fatalf("fixed level:\n%s", got)
	}
	// stdout logging: no syslog appender
	got = Log4j2Config(tmpl, map[string]string{"LOGHOST": "10.0.0.5"}, map[string]string{"HMIP_LOG_STDOUT": "1"}, "WARN")
	if strings.Contains(got, "SYSLOG\"/>\n") && strings.Count(got, "AppenderRef") != 1 {
		t.Fatalf("stdout:\n%s", got)
	}
	p := MakePlan(inputs(rpiRFMod("/dev/raw-uart")))
	env := EnvFiles(p, "aarch64")
	if env["rfd"] != "LOGLEVEL_RFD=5\n" || env["hs485d"] != "LOGLEVEL_HS485D=5\n" || !strings.Contains(env["hmipserver"], "HMIP_ARGS=/var/etc/crRFD.conf /var/etc/HMServer.conf\n") {
		t.Fatalf("env: %v", env)
	}
	p = MakePlan(inputs())
	if env := EnvFiles(p, "x86_64"); !strings.Contains(env["hmipserver"], "HMIP_CLASS=de.eq3.ccu.server.HMServer\n") || !strings.Contains(env["hmipserver"], "HMIP_DEVNODE=\n") {
		t.Fatalf("HMServer-only env: %v", env)
	}
}

// openccu-lite task 326: the LED names are freed from whichever driver owns them - leds_pwm's
// rpi_rf_mod_leds since task 315, leds-gpio's leds before - unbound before rpi_rf_mod_led loads and
// bound again after it, and only that driver is touched.
func TestHBRFLEDFreesTheNamesFromTheirDriver(t *testing.T) {
	for _, tc := range []struct{ name, driver, device string }{
		{"the header's PWM LEDs (task 315)", "leds_pwm", "rpi_rf_mod_leds"},
		{"gpio-leds (before task 315)", "leds-gpio", "leds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mk := func(p string) string {
				full := filepath.Join(root, p)
				if err := os.MkdirAll(full, 0o755); err != nil {
					t.Fatal(err)
				}
				return full
			}
			drivers := mk("sys/bus/platform/drivers")
			for _, drv := range []string{"leds_pwm", "leds-gpio"} {
				mk("sys/bus/platform/drivers/" + drv)
				for _, f := range []string{"bind", "unbind"} {
					_ = os.WriteFile(filepath.Join(drivers, drv, f), nil, 0o644)
				}
			}
			dev := mk("sys/devices/platform/" + tc.device)
			if err := os.Symlink("../../../bus/platform/drivers/"+tc.driver, filepath.Join(dev, "driver")); err != nil {
				t.Fatal(err)
			}
			// the bound device as the driver directory lists it
			_ = os.Symlink("../../../../devices/platform/"+tc.device, filepath.Join(drivers, tc.driver, tc.device))
			blue := mk("sys/class/leds/rpi_rf_mod:blue")
			if err := os.Symlink("../../../devices/platform/"+tc.device, filepath.Join(blue, "device")); err != nil {
				t.Fatal(err)
			}
			read := func(drv, f string) string {
				b, _ := os.ReadFile(filepath.Join(drivers, drv, f))
				return string(b)
			}
			var order []string
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				// the module loads while the names are free: unbound already, not bound yet
				order = append(order, filepath.Base(name)+" unbind="+read(tc.driver, "unbind")+" bind="+read(tc.driver, "bind"))
				// the driver's unbind removes the device from its directory
				_ = os.Remove(filepath.Join(drivers, tc.driver, tc.device))
				return nil, nil
			}
			w := &writer{d: Detector{Root: root, Run: run}}
			w.hbrfLED(context.Background(), &HBRFLED{Node: "/dev/raw-uart", Red: "2", Green: "1", Blue: "0"}, func(string, ...any) {})
			if len(order) != 1 || order[0] != "modprobe unbind="+tc.device+" bind=" {
				t.Fatalf("modprobe saw %v", order)
			}
			if read(tc.driver, "bind") != tc.device {
				t.Errorf("%s bound again with %q", tc.driver, read(tc.driver, "bind"))
			}
			other := map[string]string{"leds_pwm": "leds-gpio", "leds-gpio": "leds_pwm"}[tc.driver]
			if read(other, "unbind") != "" || read(other, "bind") != "" {
				t.Errorf("%s touched", other)
			}
		})
	}
	// the adapter's own LEDs (no parent device), or none at all: nothing to unbind, the module loads
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "sys/class/leds/rpi_rf_mod:blue"), 0o755)
	var calls []string
	w := &writer{d: Detector{Root: root, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, filepath.Base(name))
		return nil, nil
	}}}
	w.hbrfLED(context.Background(), &HBRFLED{Node: "/dev/raw-uart", Red: "2", Green: "1", Blue: "0"}, func(string, ...any) {})
	if len(calls) != 1 || calls[0] != "modprobe" {
		t.Errorf("calls %v", calls)
	}
}
