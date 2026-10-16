package priv

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/button"
)

func TestButtonPlace(t *testing.T) {
	for _, c := range []struct{ mode, want string }{
		{"", ButtonDetecting},
		{"HM_HMIP_DEV='RPI-RF-MOD'\nHM_HMIP_DEVTYPE='GPIO@3f201000.serial'\n", ""},
		{"HM_HMRF_DEV='RPI-RF-MOD'\nHM_HMRF_DEVTYPE='GPIO@fe201000.serial'\n", ""},
		{"HM_HMIP_DEV='RPI-RF-MOD'\nHM_HMIP_DEVTYPE='HB-RF-USB-2@usb-0000:01:00.0-1.3'\n", ButtonAdapter},
		{"HM_HMIP_DEV='HMIP-RFUSB'\nHM_HMIP_DEVTYPE='eQ-3 HmIP-RFUSB@usb-0000:02:1b.0-1'\n", ButtonNoModule},
		{"HM_MODE='NORMAL'\n", ButtonNoModule},
	} {
		if got := buttonPlace(c.mode); got != c.want {
			t.Errorf("%q: %q, want %q", c.mode, got, c.want)
		}
	}
}

// fakeLine is an edge source a test writes to.
type fakeLine struct{ ch chan button.Edge }

func (f fakeLine) Read() (button.Edge, error) {
	e, ok := <-f.ch
	if !ok {
		return button.Edge{}, errors.New("closed")
	}
	return e, nil
}

func buttonRoot(t *testing.T, booted bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/status"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "usr/local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if booted {
		if err := os.WriteFile(filepath.Join(root, "var/status/startupFinished"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func startLoop(t *testing.T, w *ButtonWatcher) (fakeLine, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	line := fakeLine{ch: make(chan button.Edge, 8)}
	w.set(func() { w.available = true })
	go w.loop(ctx, line)
	t.Cleanup(cancel)
	return line, cancel
}

func press(line fakeLine, at time.Time, held time.Duration) {
	line.ch <- button.Edge{Low: true, At: at}
	line.ch <- button.Edge{Low: false, At: at.Add(held)}
}

func waitKinds(t *testing.T, w *ButtonWatcher, since uint64, want ...string) []ButtonEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got []ButtonEvent
	for time.Now().Before(deadline) {
		b := w.Wait(context.Background(), since, 200*time.Millisecond)
		got = b.Events
		var kinds []string
		for _, e := range got {
			if isGesture(e.Kind) || e.Kind == button.EventLongReached || e.Kind == button.EventCancel {
				kinds = append(kinds, e.Kind)
			}
		}
		if len(kinds) == len(want) {
			for i := range want {
				if kinds[i] != want[i] {
					t.Fatalf("kinds %v, want %v", kinds, want)
				}
			}
			return got
		}
	}
	t.Fatalf("events %+v, want gestures %v", got, want)
	return nil
}

// The watcher: a short press becomes a single after the window, a long press set to recovery is
// executed by the helper when the daemon does not take it, and not when it does; gestures before
// the boot finished are dropped.
func TestButtonWatcher(t *testing.T) {
	var recovered atomic.Int32
	w := &ButtonWatcher{Root: buttonRoot(t, true), AckWait: 100 * time.Millisecond, Config: func() ButtonConfig { return ButtonConfig{LongS: 3, Long: "recovery"} },
		Recover: func() error { recovered.Add(1); return nil }}
	line, _ := startLoop(t, w)
	now := time.Now()
	press(line, now, 100*time.Millisecond)
	ev := waitKinds(t, w, 0, button.GestureSingle)
	last := ev[len(ev)-1].Seq
	// long, not acknowledged: the helper reboots into recovery
	press(line, time.Now(), 3500*time.Millisecond)
	ev = waitKinds(t, w, last, button.EventLongReached, button.GestureLong)
	last = ev[len(ev)-1].Seq
	time.Sleep(300 * time.Millisecond)
	if recovered.Load() != 1 {
		t.Fatalf("recovered %d times, want 1", recovered.Load())
	}
	// long, acknowledged at once: the daemon does it, the helper does not
	press(line, time.Now(), 3500*time.Millisecond)
	ev = waitKinds(t, w, last, button.EventLongReached, button.GestureLong)
	w.Ack(ev[len(ev)-1].Seq)
	time.Sleep(300 * time.Millisecond)
	if recovered.Load() != 1 {
		t.Errorf("an acknowledged long press recovered too: %d", recovered.Load())
	}
	// a helper restarted (the daemon's since beyond its count): everything it has
	if b := w.Wait(context.Background(), 10_000, time.Millisecond); len(b.Events) == 0 || !b.Available {
		t.Errorf("since beyond the count: %+v", b)
	}
}

func TestButtonWatcherBeforeBoot(t *testing.T) {
	w := &ButtonWatcher{Root: buttonRoot(t, false), Config: func() ButtonConfig { return ButtonConfig{} }}
	line, _ := startLoop(t, w)
	press(line, time.Now(), 100*time.Millisecond)
	time.Sleep(700 * time.Millisecond)
	b := w.Wait(context.Background(), 0, time.Millisecond)
	for _, e := range b.Events {
		if isGesture(e.Kind) {
			t.Errorf("a gesture before the boot finished: %+v", e)
		}
	}
	if b.Ignored != 1 {
		t.Errorf("ignored %d", b.Ignored)
	}
}

func TestButtonRecoverReboot(t *testing.T) {
	root := buttonRoot(t, true)
	if err := os.Symlink("/x", filepath.Join(root, "usr/local/.firmwareUpdate")); err != nil {
		t.Fatal(err)
	}
	w := &ButtonWatcher{Root: root}
	if err := w.recoverReboot(); err == nil {
		t.Error("an update staged is no refusal")
	}
	if _, err := os.Stat(filepath.Join(root, "usr/local/.recoveryMode")); err == nil {
		t.Error("the marker was written with an update staged")
	}
}

// Through the helper: button-wait answers the batch, button-ack is taken; anything more in the
// request is refused; without a watcher the answer says no-module.
func TestButtonOperations(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Log: func(string, ...any) {}}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}
	b, err := c.ButtonWait(ctx, 0)
	if err != nil || b.Available || b.Reason != ButtonNoModule {
		t.Fatalf("no watcher: %+v %v", b, err)
	}
	if err := c.ButtonAck(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(ctx, request{Op: opButtonWait, Seq: 1, Path: "/dev/gpiochip0"}); !errors.Is(err, ErrRefused) {
		t.Errorf("a request with a path: %v", err)
	}

	w := &ButtonWatcher{Root: buttonRoot(t, true), Config: func() ButtonConfig { return ButtonConfig{} }}
	srv.Button = w
	line, _ := startLoop(t, w)
	press(line, time.Now(), 50*time.Millisecond)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err = c.ButtonWait(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Events) > 0 && b.Events[len(b.Events)-1].Kind == button.GestureSingle {
			break
		}
	}
	if !b.Available || len(b.Events) == 0 || b.Events[len(b.Events)-1].Kind != button.GestureSingle {
		t.Fatalf("through the helper: %+v", b)
	}
	if err := c.ButtonAck(ctx, b.Seq); err != nil {
		t.Fatal(err)
	}
}

// /var/hm_mode is written in steps at the boot: a first answer without the module is looked at
// again, and the module on the header then goes on to the GPIO line (none in the test: no-line).
func TestButtonWatcherWaitsForTheModule(t *testing.T) {
	root := buttonRoot(t, true)
	mode := filepath.Join(root, "var/hm_mode")
	if err := os.WriteFile(mode, []byte("HM_MODE='NORMAL'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &ButtonWatcher{Root: root, Poll: 10 * time.Millisecond}
	w.Start(ctx)
	reason := func() string { return w.Wait(ctx, 0, time.Millisecond).Reason }
	deadline := time.Now().Add(2 * time.Second)
	for reason() != ButtonNoModule && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r := reason(); r != ButtonNoModule {
		t.Fatalf("first: %q", r)
	}
	if err := os.WriteFile(mode, []byte("HM_HMIP_DEV='RPI-RF-MOD'\nHM_HMIP_DEVTYPE='GPIO@3f201000.serial'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for reason() != ButtonNoLine && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r := reason(); r != ButtonNoLine {
		t.Errorf("after the module showed: %q", r)
	}
}

// The daemon's first call is answered at once; the next one waits for a change.
func TestButtonWaitFirstCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &ButtonWatcher{Root: buttonRoot(t, true), Poll: time.Hour}
	w.Start(ctx)
	start := time.Now()
	b := w.Wait(ctx, 0, 2*time.Second)
	if time.Since(start) > time.Second || b.Seq != 1 {
		t.Fatalf("first call: %v %+v", time.Since(start), b)
	}
	start = time.Now()
	w.Wait(ctx, b.Seq, 200*time.Millisecond)
	if time.Since(start) < 150*time.Millisecond {
		t.Errorf("the next call did not wait: %v", time.Since(start))
	}
}
