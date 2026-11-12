package radio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// recoverRig is a watch on a fake clock with a marker it can write, processes that answer when
// told, and a restart that is counted.
type recoverRig struct {
	root     string
	now      time.Time
	answer   bool
	serverUp bool
	restarts int
	fail     error
	r        *BidCosRecovery
}

func newRecoverRig(t *testing.T) *recoverRig {
	g := &recoverRig{root: t.TempDir(), now: time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC), serverUp: true}
	g.r = &BidCosRecovery{Root: g.root, Now: func() time.Time { return g.now },
		Answering: func(context.Context, BidCosWait) bool { return g.answer },
		ServerUp:  func(context.Context) bool { return g.serverUp },
		Restart: func(context.Context) error {
			if g.fail != nil {
				return g.fail
			}
			g.restarts++
			return nil
		}}
	return g
}

func (g *recoverRig) mark(t *testing.T, since, at time.Time) {
	t.Helper()
	b, _ := json.Marshal(BidCosDegraded{Since: since, At: at, Missing: []BidCosWait{{Daemon: "rfd", Port: 32001, Interface: "BidCos-RF"}}})
	p := shadowPath(g.root, BidCosDegradedFile)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (g *recoverRig) step(d time.Duration) {
	g.now = g.now.Add(d)
	g.r.Tick(context.Background())
}

func TestBidCosRecoveryNoMarker(t *testing.T) {
	g := newRecoverRig(t)
	g.answer = true
	g.r.Answering = func(context.Context, BidCosWait) bool { t.Fatal("asked without a marker"); return false }
	g.step(time.Minute)
	if g.restarts != 0 || g.r.Status() != (BidCosRecoveryStatus{}) {
		t.Fatalf("%d %+v", g.restarts, g.r.Status())
	}
}

// rfd answers again: after Stable of answering, one restart; the restart's start finds it and the
// marker goes - the state resets.
func TestBidCosRecoveryRestartsOnceStable(t *testing.T) {
	g := newRecoverRig(t)
	since := g.now
	g.mark(t, since, since)
	g.step(10 * time.Second) // not answering
	g.answer = true
	g.step(10 * time.Second) // answering from now
	g.step(10 * time.Second)
	g.step(10 * time.Second)
	if g.restarts != 0 {
		t.Fatal("restarted before Stable")
	}
	g.answer = false // a break starts the time again
	g.step(10 * time.Second)
	g.answer = true
	g.step(10 * time.Second)
	g.step(20 * time.Second)
	if g.restarts != 0 {
		t.Fatal("the break did not restart the time")
	}
	g.step(10 * time.Second)
	if g.restarts != 1 || g.r.Status().Restarts != 1 {
		t.Fatalf("restarts %d", g.restarts)
	}
	// the restart's prep step has not run: nothing more
	g.step(time.Minute)
	if g.restarts != 1 {
		t.Fatal("restarted again while the first was pending")
	}
	_ = os.Remove(shadowPath(g.root, BidCosDegradedFile))
	g.step(10 * time.Second)
	if g.r.Status() != (BidCosRecoveryStatus{}) {
		t.Fatalf("state after the marker went: %+v", g.r.Status())
	}
}

// A process that answers, then not at the restart: the next restart waits twice as long, and
// after MaxRestarts the watch gives up for this marker; a new marker starts afresh.
func TestBidCosRecoveryBacksOffAndGivesUp(t *testing.T) {
	g := newRecoverRig(t)
	g.r.MaxRestarts = 3
	since := g.now
	g.mark(t, since, since)
	g.answer = true
	need := 30 * time.Second
	for i := 1; i <= 3; i++ {
		g.step(10 * time.Second) // starts the time
		for e := 10 * time.Second; e < need; e += 10 * time.Second {
			g.step(10 * time.Second)
			if g.restarts != i-1 {
				t.Fatalf("round %d: restarted after %s, want %s", i, e, need)
			}
		}
		g.step(10 * time.Second)
		if g.restarts != i {
			t.Fatalf("round %d: %d restarts", i, g.restarts)
		}
		// the restart came back without rfd: a newer marker of the same episode
		g.mark(t, since, g.now.Add(time.Second))
		need *= 2
	}
	g.step(10 * time.Second)
	for e := time.Duration(0); e <= need; e += 10 * time.Second {
		g.step(10 * time.Second)
	}
	if g.restarts != 3 || !g.r.Status().GaveUp {
		t.Fatalf("restarts %d, %+v", g.restarts, g.r.Status())
	}
	g.step(10 * time.Minute)
	if g.restarts != 3 {
		t.Fatal("restarted after giving up")
	}
	g.mark(t, g.now, g.now) // a new episode (a restart by hand, a reboot)
	g.step(10 * time.Second)
	if st := g.r.Status(); st.GaveUp || st.Restarts != 0 {
		t.Fatalf("new marker: %+v", st)
	}
}

// hmipserver still starting: no restart until it is up; a restart that fails is not counted; a
// restart whose start never writes a marker again counts as come back after 10 minutes.
func TestBidCosRecoveryWaitsForTheServer(t *testing.T) {
	g := newRecoverRig(t)
	g.mark(t, g.now, g.now)
	g.answer, g.serverUp = true, false
	g.step(10 * time.Second)
	g.step(40 * time.Second)
	if g.restarts != 0 {
		t.Fatal("restarted a starting server")
	}
	g.serverUp = true
	g.fail = fmt.Errorf("no bus")
	g.step(10 * time.Second)
	if g.restarts != 0 || g.r.Status().Restarts != 0 {
		t.Fatal("a failed restart counted")
	}
	g.fail = nil
	g.step(10 * time.Second)
	if g.restarts != 1 {
		t.Fatalf("restarts %d", g.restarts)
	}
	g.step(9 * time.Minute)
	if g.restarts != 1 {
		t.Fatal("restarted while the first was pending")
	}
	g.step(2 * time.Minute) // pending for 11 minutes: taken as back without rfd; the time starts again
	g.step(50 * time.Second)
	if g.restarts != 1 {
		t.Fatal("restarted without waiting twice the time")
	}
	g.step(20 * time.Second)
	if g.restarts != 2 {
		t.Fatalf("restarts %d", g.restarts)
	}
}

// The default checks: rfd's unit active/running and its port open; hmipserver's unit active.
func TestBidCosRecoveryDefaultChecks(t *testing.T) {
	states := map[string]string{"rfd": "active running", "hmipserver": "activating start"}
	r := &BidCosRecovery{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		unit := args[len(args)-1]
		unit = unit[:len(unit)-len(".service")]
		var a, s string
		_, _ = fmt.Sscan(states[unit], &a, &s)
		return []byte("ActiveState=" + a + "\nSubState=" + s + "\n"), nil
	}}
	w := BidCosWait{Daemon: "rfd", Port: 32001}
	old := bidcosDial
	t.Cleanup(func() { bidcosDial = old })
	open := false
	bidcosDial = func(int) bool { return open }
	if r.answering(context.Background(), w) {
		t.Fatal("port closed: answering")
	}
	open = true
	if !r.answering(context.Background(), w) {
		t.Fatal("active and open: not answering")
	}
	states["rfd"] = "activating start-post" // a silent LAN gateway: the port open, rfd not started
	if r.answering(context.Background(), w) {
		t.Fatal("starting: answering")
	}
	if r.serverUp(context.Background()) {
		t.Fatal("hmipserver starting: up")
	}
	states["hmipserver"] = "active running"
	if !r.serverUp(context.Background()) {
		t.Fatal("hmipserver active: not up")
	}
}

// The last LAN gateway removed (or BidCos switched off): rfd is no longer planned, so it counts as
// answered - one restart, whose start removes the marker - instead of a warning for ever.
func TestBidCosRecoveryNotPlannedAnyMore(t *testing.T) {
	g := newRecoverRig(t)
	g.mark(t, g.now, g.now)
	_ = os.WriteFile(shadowPath(g.root, "plan.json"), []byte(`{"rfd":{"run":true}}`), 0o644)
	g.step(10 * time.Second)
	g.step(40 * time.Second)
	if g.restarts != 0 {
		t.Fatal("restarted while rfd is planned and silent")
	}
	_ = os.WriteFile(shadowPath(g.root, "plan.json"), []byte(`{"rfd":{"run":false}}`), 0o644)
	g.step(10 * time.Second)
	g.step(30 * time.Second)
	if g.restarts != 1 {
		t.Fatalf("restarts %d", g.restarts)
	}
	// no plan at all: taken as planned
	_ = os.Remove(shadowPath(g.root, "plan.json"))
	if !g.r.planned("rfd") {
		t.Fatal("no plan: not planned")
	}
}

func TestBidCosRecoveryRunEnds(t *testing.T) {
	g := newRecoverRig(t)
	g.r.Every = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.r.Run(ctx); close(done) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	<-done
}
