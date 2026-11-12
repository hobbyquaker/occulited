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
}

func (b *waitBox) detector(root string) Detector {
	return Detector{Root: root, Now: func() time.Time { return b.now }, Sleep: func(t time.Duration) { b.now = b.now.Add(t) },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "systemctl" {
				return nil, fmt.Errorf("unexpected %s", name)
			}
			b.systemctl++
			unit := strings.TrimSuffix(args[len(args)-1], ".service")
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

func TestBidCosPreStart(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	rfd := Plan{RFD: Daemon{Run: true}, HmIPServer: Daemon{Run: true}}
	both := Plan{RFD: Daemon{Run: true}, HS485D: Daemon{Run: true}, HmIPServer: Daemon{Run: true}, HmIPServerHmIP: true}
	cases := []struct {
		name     string
		plan     Plan
		answerAt map[int]int
		states   map[string]string
		held     string // "" = no hold; else the waited-for names in the error
		waited   time.Duration
		logs     int
		hmip     bool
	}{
		{name: "no BidCos process in the plan: no wait, no systemctl", plan: Plan{HmIPServer: Daemon{Run: true}}},
		{name: "rfd answers at once", plan: rfd, answerAt: map[int]int{32001: 1}},
		{name: "rfd answers while its unit starts", plan: rfd, answerAt: map[int]int{32001: 6}, states: map[string]string{"rfd": "activating start-post"}, waited: 5 * time.Second, logs: 2},
		{name: "rfd answers late, its start queued (inactive) inside the grace", plan: rfd, answerAt: map[int]int{32001: 15}, waited: 14 * time.Second, logs: 2},
		{name: "rfd stopped: held after the grace", plan: rfd, held: "rfd", waited: 20 * time.Second, logs: 1},
		{name: "rfd between restarts (auto-restart): held at once, no start job beside rfd's", plan: rfd, states: map[string]string{"rfd": "activating auto-restart"}, held: "rfd"},
		{name: "rfd failed: held at once", plan: rfd, states: map[string]string{"rfd": "failed failed"}, held: "rfd"},
		{name: "one process starting, the other between restarts: waited while one starts, then held for the other", plan: both, answerAt: map[int]int{32001: 8}, states: map[string]string{"rfd": "activating start", "hs485d": "activating auto-restart"}, held: "hs485d", waited: 7 * time.Second, logs: 1, hmip: true},
		{name: "rfd starting for ever (absent LAN gateway): held at the limit", plan: rfd, states: map[string]string{"rfd": "activating start-post"}, held: "rfd", waited: 90 * time.Second, logs: 1},
		{name: "rfd up but its port not open yet counts as starting", plan: rfd, answerAt: map[int]int{32001: 31}, states: map[string]string{"rfd": "active running"}, waited: 30 * time.Second, logs: 2},
		{name: "hs485d down beside an answering rfd: held for hs485d alone", plan: both, answerAt: map[int]int{32001: 1}, held: "hs485d", waited: 20 * time.Second, logs: 1, hmip: true},
		{name: "both answer, hs485d later", plan: both, answerAt: map[int]int{32001: 1, 32000: 4}, states: map[string]string{"hs485d": "activating start"}, waited: 3 * time.Second, logs: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			b := &waitBox{now: t0, answerAt: c.answerAt, polls: map[int]int{}, states: c.states}
			old := bidcosDial
			bidcosDial = b.dial
			t.Cleanup(func() { bidcosDial = old })
			var lines []string
			logf := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
			// a marker of an earlier hold goes once the processes answer
			marker := shadowPath(root, BidCosHoldFile)
			_ = os.MkdirAll(filepath.Dir(marker), 0o755)
			_ = os.WriteFile(marker, []byte(`{"since":"2026-11-12T12:00:00Z","wait":[{"daemon":"rfd","port":32001}]}`), 0o644)
			err := bidcosPreStart(context.Background(), b.detector(root), c.plan, logf)
			if got := b.now.Sub(t0); got != c.waited {
				t.Errorf("waited %s, want %s", got, c.waited)
			}
			if len(lines) != c.logs {
				t.Errorf("%d log lines, want %d: %q", len(lines), c.logs, lines)
			}
			h := ReadBidCosHold(root)
			if c.held == "" {
				if err != nil || h != nil {
					t.Fatalf("no hold expected: err %v, marker %+v", err, h)
				}
				if len(bidcosWaits(c.plan)) == 0 && b.systemctl != 0 {
					t.Errorf("systemctl asked %d times without a process to wait for", b.systemctl)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "hmipserver is held back: "+c.held+" does not answer") {
				t.Fatalf("error %v", err)
			}
			want := "so VirtualDevices stay down"
			if c.hmip {
				want = "so HmIP-RF and VirtualDevices stay down"
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not say %q: %v", want, err)
			}
			if h == nil || len(h.Wait) != 1 || h.Wait[0].Daemon != c.held || h.HmIP != c.hmip || h.Wait[0].State == "" {
				t.Fatalf("marker %+v", h)
			}
			// the hold keeps its first time across the retries
			if !h.Since.Equal(time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)) {
				t.Errorf("since %s, want the earlier marker's", h.Since)
			}
			if st, err := os.Stat(marker); err != nil || st.Mode().Perm() != 0o644 {
				t.Errorf("marker mode: %v %v", err, st)
			}
		})
	}
}

func TestBidCosHoldFirstSince(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	b := &waitBox{now: t0, polls: map[int]int{}}
	old := bidcosDial
	bidcosDial = b.dial
	t.Cleanup(func() { bidcosDial = old })
	if err := bidcosPreStart(context.Background(), b.detector(root), Plan{RFD: Daemon{Run: true}}, func(string, ...any) {}); err == nil {
		t.Fatal("no hold")
	}
	if h := ReadBidCosHold(root); h == nil || !h.Since.Equal(t0.Add(BidCosWaitGrace)) {
		t.Fatalf("marker %+v", h)
	}
}

func TestBidCosUnitStateUnreadable(t *testing.T) {
	d := Detector{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("no bus") }}
	if s, ph := unitState(context.Background(), d, "rfd"); ph != unitQueued || s != "unknown" {
		t.Fatalf("%s %v", s, ph)
	}
}
