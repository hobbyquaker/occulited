package radio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain: every other test that runs hmipserver's prep step sees rfd and hs485d answering, as
// on a box whose interface processes are up; the wait's own tests below replace the dial.
func TestMain(m *testing.M) {
	bidcosDial = func(int) bool { return true }
	bidcosRFDReported = func(context.Context, Detector) bool { return true }
	os.Exit(m.Run())
}

// waitBox is a fake clock that the step's sleeps advance, a dial that answers from a port table
// after a number of polls, and a systemctl that answers each unit's state.
type waitBox struct {
	now       time.Time
	answerAt  map[int]int // port -> the poll from which it answers; missing: never
	polls     map[int]int
	states    map[string]string // unit -> "ActiveState SubState"
	systemctl int
	rfdPID    string // rfd.service's MainPID
}

func (b *waitBox) detector(root string) Detector {
	return Detector{Root: root, Now: func() time.Time { return b.now }, Sleep: func(t time.Duration) { b.now = b.now.Add(t) },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "systemctl" {
				return nil, fmt.Errorf("unexpected %s", name)
			}
			b.systemctl++
			unit := strings.TrimSuffix(args[len(args)-1], ".service")
			if args[2] == "MainPID" {
				return []byte("MainPID=" + b.rfdPID + "\n"), nil
			}
			s := strings.Fields(b.states[unit])
			if len(s) != 2 {
				return []byte("ActiveState=inactive\nSubState=dead\n"), nil
			}
			return []byte("ActiveState=" + s[0] + "\nSubState=" + s[1] + "\n"), nil
		}}
}

func (b *waitBox) dial(port int) bool {
	b.polls[port]++
	at, ok := b.answerAt[port]
	return ok && b.polls[port] >= at
}

// tmplIfaces is the image's InterfacesList.xml template, byte for byte.
const tmplIfaces = "<?xml version=\"1.0\" encoding=\"utf-8\" ?> \n<interfaces v=\"1.0\">\n\t<ipc>\n\t \t<name>BidCos-RF</name>\n\t \t<url>xmlrpc_bin://127.0.0.1:32001</url> \n\t \t<info>BidCos-RF</info> \n\t</ipc>\n\t<ipc>\n\t \t<name>VirtualDevices</name>\n\t \t<url>xmlrpc://127.0.0.1:39292/groups</url> \n\t \t<info>Virtual Devices</info> \n\t</ipc>\n\t<ipc>\n\t \t<name>HmIP-RF</name>\n\t \t<url>xmlrpc://127.0.0.1:32010</url>\n\t \t<info>HmIP-RF</info>\n\t</ipc>\n</interfaces>\n"

// waitRoot is a box root with the template and the list the radio run wrote for the plan.
func waitRoot(t *testing.T, p Plan) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc/config", "etc/config_templates"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(root, "etc/config_templates/InterfacesList.xml"), []byte(tmplIfaces), 0o644)
	_ = os.WriteFile(filepath.Join(root, "etc/config/InterfacesList.xml"), []byte(RenderInterfaces(tmplIfaces, p.Interfaces)), 0o644)
	return root
}

func listNames(t *testing.T, root string) string {
	t.Helper()
	var n []string
	for _, i := range ParseInterfaces(readFile(filepath.Join(root, "etc/config/InterfacesList.xml"))) {
		n = append(n, i.Name)
	}
	return strings.Join(n, ",")
}

// planned plans as the radio run does for a module-less system with a LAN gateway (rfd), and a
// system with an HmIP module, rfd and a wired gateway (both).
func plannedWait() (rfd, both Plan) {
	all := TemplateInterfaces(tmplIfaces)
	rfd = Plan{RFD: Daemon{Run: true}, HmIPServer: Daemon{Run: true}, Interfaces: removeInterface(all, "HmIP-RF")}
	both = Plan{RFD: Daemon{Run: true}, HS485D: Daemon{Run: true}, HmIPServer: Daemon{Run: true}, HmIPServerHmIP: true, Interfaces: append(all, BidCosWired)}
	return
}

func TestBidCosPreStart(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	rfd, both := plannedWait()
	cases := []struct {
		name     string
		plan     Plan
		answerAt map[int]int
		states   map[string]string
		missing  string // "" = everything answers; else the left-out processes, comma separated
		list     string // the entries InterfacesList.xml has afterwards
		waited   time.Duration
		logs     int
		status   string // /var/status/rfd.status; "" = the running rfd's (1234)
	}{
		{name: "no BidCos process in the plan: no wait, no systemctl", plan: Plan{HmIPServer: Daemon{Run: true}, Interfaces: []Interface{{Name: "VirtualDevices"}}}, list: "BidCos-RF,VirtualDevices,HmIP-RF"},
		{name: "rfd answers at once", plan: rfd, answerAt: map[int]int{32001: 1}, list: "BidCos-RF,VirtualDevices"},
		{name: "rfd answers while its unit starts", plan: rfd, answerAt: map[int]int{32001: 6}, states: map[string]string{"rfd": "activating start-post"}, list: "BidCos-RF,VirtualDevices", waited: 5 * time.Second, logs: 2},
		{name: "rfd answers late, its start queued (inactive) inside the grace", plan: rfd, answerAt: map[int]int{32001: 15}, list: "BidCos-RF,VirtualDevices", waited: 14 * time.Second, logs: 2},
		{name: "rfd stopped: left out after the grace", plan: rfd, missing: "rfd", list: "VirtualDevices", waited: 20 * time.Second, logs: 2},
		{name: "rfd between restarts (auto-restart): left out at once", plan: rfd, states: map[string]string{"rfd": "activating auto-restart"}, missing: "rfd", list: "VirtualDevices", logs: 1},
		{name: "rfd failed: left out at once", plan: rfd, states: map[string]string{"rfd": "failed failed"}, missing: "rfd", list: "VirtualDevices", logs: 1},
		{name: "one process starting, the other between restarts: waited while one starts, then the other left out", plan: both, answerAt: map[int]int{32001: 8}, states: map[string]string{"rfd": "activating start", "hs485d": "activating auto-restart"}, missing: "hs485d", list: "BidCos-RF,VirtualDevices,HmIP-RF", waited: 7 * time.Second, logs: 2},
		{name: "rfd starting for ever (absent LAN gateway): left out at the limit", plan: rfd, states: map[string]string{"rfd": "activating start-post"}, missing: "rfd", list: "VirtualDevices", waited: 90 * time.Second, logs: 2},
		{name: "rfd up but its port not open yet counts as starting", plan: rfd, answerAt: map[int]int{32001: 31}, states: map[string]string{"rfd": "active running"}, list: "BidCos-RF,VirtualDevices", waited: 30 * time.Second, logs: 2},
		{name: "hs485d down beside an answering rfd: BidCos-Wired alone left out", plan: both, answerAt: map[int]int{32001: 1}, missing: "hs485d", list: "BidCos-RF,VirtualDevices,HmIP-RF", waited: 20 * time.Second, logs: 2},
		{name: "both down: both left out, HmIP-RF and VirtualDevices stay", plan: both, states: map[string]string{"rfd": "failed failed", "hs485d": "failed failed"}, missing: "rfd,hs485d", list: "VirtualDevices,HmIP-RF", logs: 1},
		{name: "rfd's port open but rfd never reports itself started (a silent LAN gateway): left out at the limit", plan: rfd, answerAt: map[int]int{32001: 1}, states: map[string]string{"rfd": "activating start-post"}, status: "none", missing: "rfd", list: "VirtualDevices", waited: 90 * time.Second, logs: 2},
		{name: "rfd's status file names an earlier process: not started", plan: rfd, answerAt: map[int]int{32001: 1}, status: "999", missing: "rfd", list: "VirtualDevices", waited: 20 * time.Second, logs: 2},
		{name: "both answer, hs485d later", plan: both, answerAt: map[int]int{32001: 1, 32000: 4}, states: map[string]string{"hs485d": "activating start"}, list: "BidCos-RF,VirtualDevices,HmIP-RF,BidCos-Wired", waited: 3 * time.Second, logs: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := waitRoot(t, c.plan)
			if c.name == "no BidCos process in the plan: no wait, no systemctl" {
				_ = os.WriteFile(filepath.Join(root, "etc/config/InterfacesList.xml"), []byte(tmplIfaces), 0o644)
			}
			before := readFile(filepath.Join(root, "etc/config/InterfacesList.xml"))
			b := &waitBox{now: t0, answerAt: c.answerAt, polls: map[int]int{}, states: c.states}
			old, oldR := bidcosDial, bidcosRFDReported
			bidcosDial, bidcosRFDReported = b.dial, rfdReported
			t.Cleanup(func() { bidcosDial, bidcosRFDReported = old, oldR })
			b.rfdPID = "1234"
			switch c.status {
			case "":
				_ = os.MkdirAll(filepath.Join(root, "var/status"), 0o755)
				_ = os.WriteFile(filepath.Join(root, "var/status/rfd.status"), []byte("1234\n"), 0o664)
			case "none":
			default:
				_ = os.MkdirAll(filepath.Join(root, "var/status"), 0o755)
				_ = os.WriteFile(filepath.Join(root, "var/status/rfd.status"), []byte(c.status+"\n"), 0o664)
			}
			var lines []string
			logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
			bidcosPreStart(context.Background(), b.detector(root), c.plan, logf)
			if got := b.now.Sub(t0); got != c.waited {
				t.Errorf("waited %s, want %s", got, c.waited)
			}
			if len(lines) != c.logs {
				t.Errorf("%d log lines, want %d: %q", len(lines), c.logs, lines)
			}
			if got := listNames(t, root); got != c.list {
				t.Errorf("InterfacesList.xml lists %s, want %s", got, c.list)
			}
			m := ReadBidCosDegraded(root)
			if c.missing == "" {
				if m != nil {
					t.Fatalf("no marker expected: %+v", m)
				}
				if after := readFile(filepath.Join(root, "etc/config/InterfacesList.xml")); after != before {
					t.Errorf("the list changed although everything answers:\n%s", after)
				}
				if len(bidcosWaits(c.plan)) == 0 && b.systemctl != 0 {
					t.Errorf("systemctl asked %d times without a process to wait for", b.systemctl)
				}
				return
			}
			var names []string
			for _, w := range m.Missing {
				names = append(names, w.Daemon)
				if w.State == "" || w.Interface == "" {
					t.Errorf("missing entry without state or interface: %+v", w)
				}
			}
			if strings.Join(names, ",") != c.missing || len(m.Removed) != len(m.Missing) || m.HmIP != c.plan.HmIPServerHmIP || !m.Since.Equal(b.now) || !m.At.Equal(b.now) {
				t.Fatalf("marker %+v", m)
			}
			last := lines[len(lines)-1]
			want := "so VirtualDevices come up without the BidCos devices"
			if c.plan.HmIPServerHmIP {
				want = "so HmIP-RF and VirtualDevices come up without the BidCos devices"
			}
			if !strings.Contains(last, strings.ReplaceAll(c.missing, ",", " and ")+" does not answer") || !strings.Contains(last, want) || !strings.Contains(last, "left out of InterfacesList.xml") {
				t.Errorf("line %q", last)
			}
			if st, err := os.Stat(shadowPath(root, BidCosDegradedFile)); err != nil || st.Mode().Perm() != 0o644 {
				t.Errorf("marker mode: %v %v", err, st)
			}
			if st, err := os.Stat(filepath.Join(root, "etc/config/InterfacesList.xml")); err != nil || st.Mode().Perm() != 0o644 {
				t.Errorf("list mode: %v %v", err, st)
			}
		})
	}
}

// The episode: left out at one start, still out at the next (the first time kept), and back -
// byte-identical with the radio run's list - at the start that finds rfd answering.
func TestBidCosPreStartEpisode(t *testing.T) {
	_, both := plannedWait()
	root := waitRoot(t, both)
	listPath := filepath.Join(root, "etc/config/InterfacesList.xml")
	full := readFile(listPath)
	fi0, _ := os.Stat(listPath)
	t0 := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	b := &waitBox{now: t0, answerAt: map[int]int{32000: 1}, polls: map[int]int{}, states: map[string]string{"rfd": "failed failed"}, rfdPID: "1234"}
	old := bidcosDial
	bidcosDial = b.dial
	t.Cleanup(func() { bidcosDial = old })
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	bidcosPreStart(context.Background(), b.detector(root), both, logf)
	if got := listNames(t, root); got != "VirtualDevices,HmIP-RF,BidCos-Wired" {
		t.Fatalf("first start: %s", got)
	}
	// written into its own inode (the unit binds the file)
	if fi1, _ := os.Stat(listPath); !os.SameFile(fi0, fi1) {
		t.Error("the list was replaced, not written in place")
	}
	b.now = t0.Add(5 * time.Minute)
	bidcosPreStart(context.Background(), b.detector(root), both, logf)
	m := ReadBidCosDegraded(root)
	if m == nil || !m.Since.Equal(t0) || !m.At.Equal(t0.Add(5*time.Minute)) || len(m.Removed) != 1 || m.Removed[0].Name != "BidCos-RF" {
		t.Fatalf("second start: %+v", m)
	}
	b.now = t0.Add(10 * time.Minute)
	b.answerAt[32001] = 1
	b.states = nil
	_ = os.MkdirAll(filepath.Join(root, "var/status"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "var/status/rfd.status"), []byte("1234\n"), 0o664)
	lines = nil
	bidcosPreStart(context.Background(), b.detector(root), both, logf)
	if got := readFile(listPath); got != full {
		t.Fatalf("not back as the radio run wrote it:\n%s", got)
	}
	if ReadBidCosDegraded(root) != nil {
		t.Fatal("the marker outlived the answer")
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "rfd answers again: BidCos-RF back in InterfacesList.xml") {
		t.Fatalf("lines %q", lines)
	}
	// and the next start writes nothing
	fi2, _ := os.Stat(listPath)
	bidcosPreStart(context.Background(), b.detector(root), both, logf)
	if fi3, _ := os.Stat(listPath); !fi3.ModTime().Equal(fi2.ModTime()) || len(lines) != 1 {
		t.Fatal("a start with everything answering touched the list")
	}
}

// A connection change dropped rfd while it was left out (BidCos-RF "none"): nothing comes back,
// the marker goes; and a list the radio run re-wrote with the entry is not doubled.
func TestBidCosPreStartPlanChanged(t *testing.T) {
	rfd, _ := plannedWait()
	none := Plan{HmIPServer: Daemon{Run: true}, Interfaces: removeInterface(rfd.Interfaces, "BidCos-RF")}
	root := waitRoot(t, none)
	marker := shadowPath(root, BidCosDegradedFile)
	_ = os.MkdirAll(filepath.Dir(marker), 0o755)
	_ = os.WriteFile(marker, []byte(`{"since":"2026-11-12T12:00:00Z","missing":[{"daemon":"rfd","port":32001,"interface":"BidCos-RF"}],"removed":[{"name":"BidCos-RF","url":"xmlrpc_bin://127.0.0.1:32001","info":"BidCos-RF"}]}`), 0o644)
	b := &waitBox{now: time.Now(), polls: map[int]int{}}
	bidcosPreStart(context.Background(), b.detector(root), none, func(string, ...any) {})
	if got := listNames(t, root); got != "VirtualDevices" || ReadBidCosDegraded(root) != nil {
		t.Fatalf("list %s, marker %+v", got, ReadBidCosDegraded(root))
	}
	root = waitRoot(t, rfd)
	marker = shadowPath(root, BidCosDegradedFile)
	_ = os.MkdirAll(filepath.Dir(marker), 0o755)
	_ = os.WriteFile(marker, []byte(`{"since":"2026-11-12T12:00:00Z","missing":[{"daemon":"rfd","port":32001,"interface":"BidCos-RF"}],"removed":[{"name":"BidCos-RF","url":"xmlrpc_bin://127.0.0.1:32001","info":"BidCos-RF"}]}`), 0o644)
	bidcosPreStart(context.Background(), b.detector(root), rfd, func(string, ...any) {})
	if got := listNames(t, root); got != "BidCos-RF,VirtualDevices" || ReadBidCosDegraded(root) != nil {
		t.Fatalf("re-written list: %s", got)
	}
}

// A list the step cannot write (an image whose unit does not bind it writable) stays as it is,
// with no marker - the start goes on as before.
func TestBidCosPreStartUnwritable(t *testing.T) {
	rfd, _ := plannedWait()
	root := waitRoot(t, rfd)
	before := readFile(filepath.Join(root, "etc/config/InterfacesList.xml"))
	b := &waitBox{now: time.Now(), polls: map[int]int{}, states: map[string]string{"rfd": "failed failed"}}
	old, oldW := bidcosDial, bidcosWriteList
	bidcosDial = b.dial
	bidcosWriteList = func(string, []byte) error { return fmt.Errorf("read-only file system") }
	t.Cleanup(func() { bidcosDial, bidcosWriteList = old, oldW })
	var lines []string
	bidcosPreStart(context.Background(), b.detector(root), rfd, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	if ReadBidCosDegraded(root) != nil || len(lines) != 1 || !strings.Contains(lines[0], "could not be written") || readFile(filepath.Join(root, "etc/config/InterfacesList.xml")) != before {
		t.Fatalf("lines %q", lines)
	}
}

func TestRFDReported(t *testing.T) {
	root := t.TempDir()
	pid := "4321"
	d := Detector{Root: root, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte("MainPID=" + pid + "\n"), nil
	}}
	if rfdReported(context.Background(), d) {
		t.Fatal("no status file: reported")
	}
	_ = os.MkdirAll(filepath.Join(root, "var/status"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "var/status/rfd.status"), []byte("4321\n"), 0o664)
	if !rfdReported(context.Background(), d) {
		t.Fatal("the running rfd's status: not reported")
	}
	pid = "0"
	_ = os.WriteFile(filepath.Join(root, "var/status/rfd.status"), []byte("0\n"), 0o664)
	if rfdReported(context.Background(), d) {
		t.Fatal("no main process: reported")
	}
	d.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("no bus") }
	if rfdReported(context.Background(), d) {
		t.Fatal("systemctl failed: reported")
	}
}

func TestBidCosUnitStateUnreadable(t *testing.T) {
	d := Detector{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("no bus") }}
	if s, ph := unitState(context.Background(), d.run(), "rfd"); ph != unitQueued || s != "unknown" {
		t.Fatalf("%s %v", s, ph)
	}
}
