package priv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/button"
)

// The RPI-RF-MOD's button (openccu-lite task 164). The helper reads it, not the daemon: the long
// press's reboot into the recovery system must work while occulited hangs or is stopped (the
// maintainer, 2026-10-15), and the GPIO character device is root's (0600). The helper runs the
// gesture Detector on the line's edges and keeps the last events; the daemon fetches them with
// button-wait (a long poll, one request per batch as every helper operation) and runs the
// configured actions, and says with button-ack that it took a long press. A long press set to
// "recovery" that the daemon has not acknowledged within a second the helper executes itself:
// /usr/local/.recoveryMode, then systemctl reboot, reboot -f when that does not answer.

const (
	opButtonWait = "button-wait"
	opButtonAck  = "button-ack"
)

// ButtonEvent is one event of the button with its sequence number.
type ButtonEvent struct {
	Seq uint64 `json:"seq"`
	button.Event
}

// ButtonBatch is button-wait's answer: whether the button can be read (and why not), whether it
// is held now, and the events after the asked sequence number.
type ButtonBatch struct {
	Available bool          `json:"available"`
	Reason    string        `json:"reason,omitempty"`
	Pressed   bool          `json:"pressed"`
	Seq       uint64        `json:"seq"`
	Events    []ButtonEvent `json:"events"`
	// Ignored counts the gestures dropped since the helper started: before the boot finished, or
	// over the rate limit
	Ignored int `json:"ignored,omitempty"`
}

// The reasons the button cannot be read.
const (
	ButtonDetecting = "detecting"  // /var/hm_mode not written yet
	ButtonNoModule  = "no-module"  // no RPI-RF-MOD
	ButtonAdapter   = "adapter"    // the RPI-RF-MOD on a radio adapter (HB-RF-USB(-2), HB-RF-ETH)
	ButtonContainer = "container"  // a container: the GPIO is the host's
	ButtonNoLine    = "no-line"    // no GPIO line GPIO12
	ButtonLineError = "line-error" // the line could not be requested
)

// ButtonConfig is what the helper itself needs of occulited.json's hardware.button: the long
// threshold and whether the long press reboots into recovery.
type ButtonConfig struct {
	LongS int
	Long  string
}

// ButtonWatcher reads the button in the helper. Start it once; Server.Button serves it.
type ButtonWatcher struct {
	// Root is the filesystem root (/var/hm_mode, /var/status, /usr/local, /dev).
	Root string
	// Config reads the button's settings, at every press; nil = the defaults.
	Config func() ButtonConfig
	// Log, Info: the helper's.
	Log, Info func(string, ...any)
	// Recover is the fallback's reboot into the recovery system; nil = the real one.
	Recover func() error
	// AckWait is how long a long press set to recovery waits for the daemon; 0 = 1 s.
	AckWait time.Duration
	// Poll is how often the watcher looks again at /var/hm_mode while it decides; 0 = 10 s.
	Poll time.Duration

	mu        sync.Mutex
	changed   chan struct{} // closed and replaced at every change
	available bool
	reason    string
	pressed   bool
	seq       uint64
	events    []ButtonEvent // the last buttonKeep
	acked     map[uint64]bool
	ignored   int
	recent    []time.Time // the gestures of the last minute, for the rate limit
}

const (
	buttonKeep      = 64
	buttonRateLimit = 10 // gestures a minute
	buttonDebounce  = 20 * time.Millisecond
	buttonLine      = "GPIO12"
)

func (w *ButtonWatcher) logf(f string, a ...any) {
	if w.Log != nil {
		w.Log(f, a...)
	}
}

func (w *ButtonWatcher) infof(f string, a ...any) {
	if w.Info != nil {
		w.Info(f, a...)
	} else {
		w.logf(f, a...)
	}
}

func (w *ButtonWatcher) path(p string) string { return filepath.Join(w.Root, p) }

// buttonPlace decides from /var/hm_mode whether the button is the header's: "" when it is, else
// the reason. The RPI-RF-MOD on the header is a GPIO@… uart; on a radio adapter it is not.
func buttonPlace(hmMode string) string {
	if hmMode == "" {
		return ButtonDetecting
	}
	kv := map[string]string{}
	for _, l := range strings.Split(hmMode, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok {
			kv[k] = strings.Trim(v, `'"`)
		}
	}
	for _, p := range []string{"HM_HMIP", "HM_HMRF"} {
		if kv[p+"_DEV"] == "RPI-RF-MOD" {
			if strings.HasPrefix(kv[p+"_DEVTYPE"], "GPIO@") {
				return ""
			}
			return ButtonAdapter
		}
	}
	return ButtonNoModule
}

func (w *ButtonWatcher) set(f func()) {
	w.mu.Lock()
	f()
	if w.changed != nil {
		close(w.changed)
	}
	w.changed = make(chan struct{})
	w.mu.Unlock()
}

// Start decides whether the button is the header's - waiting for /var/hm_mode while the radio is
// detected - and then reads it until ctx ends.
func (w *ButtonWatcher) Start(ctx context.Context) {
	w.mu.Lock()
	if w.changed == nil {
		w.changed = make(chan struct{})
	}
	w.reason = ButtonDetecting
	// the count starts at 1: a daemon's first call (since 0) is answered at once, its next ones
	// (since 1 or more) wait for a change
	if w.seq == 0 {
		w.seq = 1
	}
	w.mu.Unlock()
	go w.run(ctx)
}

func inContainer(root string) bool {
	if _, err := os.Stat(filepath.Join(root, ".dockerenv")); err == nil {
		return true
	}
	b, _ := os.ReadFile(filepath.Join(root, "proc/1/environ"))
	return strings.Contains(string(b), "container=")
}

func (w *ButtonWatcher) run(ctx context.Context) {
	poll := w.Poll
	if poll <= 0 {
		poll = 10 * time.Second
	}
	// /var/hm_mode is written in steps while the radio is detected, and the radio can change
	// (an adapter exchange): every answer but the container's is looked at again
	last := ""
	for {
		reason := ButtonContainer
		if !inContainer(w.Root) {
			b, _ := os.ReadFile(w.path("var/hm_mode"))
			reason = buttonPlace(string(b))
		}
		if reason == "" {
			break
		}
		w.set(func() { w.reason = reason })
		if reason != last {
			w.infof("button: not read (%s)", reason)
			last = reason
		}
		if reason == ButtonContainer {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
	chip, off, err := button.FindLine(w.path("dev"), buttonLine)
	if err != nil {
		w.set(func() { w.reason = ButtonNoLine })
		w.logf("button: %s not found: %v", buttonLine, err)
		return
	}
	line, err := button.OpenLine(chip, off, "occulited-button", buttonDebounce)
	if err != nil {
		w.set(func() { w.reason = ButtonLineError })
		w.logf("button: %v", err)
		return
	}
	go func() {
		<-ctx.Done()
		line.Close()
	}()
	high, err := line.High()
	if err != nil {
		w.logf("button: reading %s: %v", buttonLine, err)
	}
	w.set(func() { w.available, w.reason, w.pressed = true, "", err == nil && !high })
	w.infof("button: reading %s on %s line %d (pressed: %v)", buttonLine, chip, off, err == nil && !high)
	w.loop(ctx, line)
}

// buttonTiming reads the long threshold from the settings, within its bounds.
func (w *ButtonWatcher) config() ButtonConfig {
	c := ButtonConfig{}
	if w.Config != nil {
		c = w.Config()
	}
	return c
}

func timingOf(c ButtonConfig) button.Timing {
	long := time.Duration(c.LongS) * time.Second
	if long < button.LongMin || long > button.LongMax {
		long = button.LongDefault
	}
	return button.DefaultTiming(long)
}

// edgeSource is what loop reads: a Line, or a test's channel.
type edgeSource interface {
	Read() (button.Edge, error)
}

func (w *ButtonWatcher) loop(ctx context.Context, line edgeSource) {
	edges := make(chan button.Edge, 16)
	go func() {
		defer close(edges)
		for {
			e, err := line.Read()
			if err != nil {
				if ctx.Err() == nil {
					w.logf("button: reading the line ended: %v", err)
				}
				return
			}
			edges <- e
		}
	}()
	d := &button.Detector{T: timingOf(w.config())}
	// the line's level when the watcher started: a press under way counts from now
	w.mu.Lock()
	if w.pressed {
		d.Press(time.Now())
	}
	w.mu.Unlock()
	for {
		var timer <-chan time.Time
		if n := d.Next(); !n.IsZero() {
			timer = time.After(time.Until(n))
		}
		var ev []button.Event
		select {
		case <-ctx.Done():
			return
		case e, ok := <-edges:
			if !ok {
				w.set(func() { w.available, w.reason = false, ButtonLineError })
				return
			}
			if e.Low {
				if !d.Pressed() {
					d.T = timingOf(w.config()) // a changed threshold takes effect at the next press
				}
				ev = d.Press(e.At)
			} else {
				ev = d.Release(e.At)
			}
		case <-timer:
			ev = d.Tick(time.Now())
		}
		if len(ev) > 0 {
			w.publish(ctx, d.Pressed(), ev)
		}
	}
}

func isGesture(kind string) bool {
	return kind == button.GestureSingle || kind == button.GestureDouble || kind == button.GestureLong
}

// publish adds events: gestures before the boot finished, and over the rate limit, are dropped
// (counted, logged); a long press set to recovery gets its fallback.
func (w *ButtonWatcher) publish(ctx context.Context, pressed bool, ev []button.Event) {
	_, err := os.Stat(w.path("var/status/startupFinished"))
	booted := err == nil
	var fallback []uint64
	w.set(func() {
		w.pressed = pressed
		now := time.Now()
		w.recent = filterRecent(w.recent, now)
		for _, e := range ev {
			if isGesture(e.Kind) {
				if !booted || len(w.recent) >= buttonRateLimit {
					w.ignored++
					w.infof("button: %s ignored (%s)", e.Kind, map[bool]string{true: "rate limit", false: "the boot has not finished"}[booted])
					continue
				}
				w.recent = append(w.recent, now)
				if e.HeldMS > 0 {
					w.infof("button: %s (held %d ms)", e.Kind, e.HeldMS)
				} else {
					w.infof("button: %s", e.Kind)
				}
			}
			w.seq++
			w.events = append(w.events, ButtonEvent{Seq: w.seq, Event: e})
			if e.Kind == button.GestureLong && w.config().Long == "recovery" {
				fallback = append(fallback, w.seq)
			}
		}
		if len(w.events) > buttonKeep {
			w.events = append([]ButtonEvent(nil), w.events[len(w.events)-buttonKeep:]...)
		}
	})
	for _, seq := range fallback {
		go w.recoverUnlessAcked(ctx, seq)
	}
}

func filterRecent(t []time.Time, now time.Time) []time.Time {
	out := t[:0]
	for _, x := range t {
		if now.Sub(x) < time.Minute {
			out = append(out, x)
		}
	}
	return out
}

func (w *ButtonWatcher) recoverUnlessAcked(ctx context.Context, seq uint64) {
	wait := w.AckWait
	if wait <= 0 {
		wait = time.Second
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(wait):
	}
	w.mu.Lock()
	acked := w.acked[seq]
	w.mu.Unlock()
	if acked {
		return
	}
	w.logf("button: the long press is set to recovery and occulited did not take it within %s; the helper reboots into the recovery system", wait)
	recov := w.Recover
	if recov == nil {
		recov = w.recoverReboot
	}
	if err := recov(); err != nil {
		w.logf("button: reboot into recovery: %v", err)
	}
}

// recoverReboot is what POST /reboot/recovery does, without the daemon: the marker (refused while
// an update is staged, which the recovery would install), then the reboot.
func (w *ButtonWatcher) recoverReboot() error {
	if _, err := os.Lstat(w.path("usr/local/.firmwareUpdate")); err == nil {
		return errors.New("an update is staged; the recovery system would install it - not rebooting")
	}
	f, err := os.OpenFile(w.path("usr/local/.recoveryMode"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		w.logf("button: the recovery marker: %v (rebooting anyway; hold the button at power-on)", err)
	} else {
		f.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "reboot").CombinedOutput()
	if err == nil {
		return nil
	}
	w.logf("button: systemctl reboot: %v: %s; reboot -f", err, strings.TrimSpace(string(out)))
	return exec.Command("reboot", "-f").Run()
}

// Wait answers the events after since, waiting up to max for one when there is none.
func (w *ButtonWatcher) Wait(ctx context.Context, since uint64, max time.Duration) ButtonBatch {
	deadline := time.After(max)
	for {
		w.mu.Lock()
		b := ButtonBatch{Available: w.available, Reason: w.reason, Pressed: w.pressed, Seq: w.seq, Events: []ButtonEvent{}, Ignored: w.ignored}
		for _, e := range w.events {
			if e.Seq > since {
				b.Events = append(b.Events, e)
			}
		}
		ch := w.changed
		w.mu.Unlock()
		// since 0 is the daemon's first call: the state at once, so the page has it; since beyond
		// the helper's count: the helper restarted, everything it has is new
		if len(b.Events) > 0 || since == 0 || since > b.Seq || ch == nil {
			if since > b.Seq {
				b.Events = append(b.Events[:0], w.snapshot()...)
			}
			return b
		}
		select {
		case <-ctx.Done():
			return b
		case <-deadline:
			return b
		case <-ch:
		}
	}
}

func (w *ButtonWatcher) snapshot() []ButtonEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]ButtonEvent(nil), w.events...)
}

// Ack records that the daemon took the event seq (a long press it acts on itself).
func (w *ButtonWatcher) Ack(seq uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.acked == nil {
		w.acked = map[uint64]bool{}
	}
	w.acked[seq] = true
	for s := range w.acked {
		if s+buttonKeep < w.seq {
			delete(w.acked, s)
		}
	}
}

// buttonWaitMax is how long one button-wait waits for an event.
const buttonWaitMax = 25 * time.Second

func (s *Server) buttonOp(ctx context.Context, req request) response {
	if s.Button == nil {
		if req.Op == opButtonAck {
			return response{OK: true}
		}
		b, _ := json.Marshal(ButtonBatch{Reason: ButtonNoModule, Events: []ButtonEvent{}})
		return response{OK: true, Stdout: b}
	}
	if req.Op == opButtonAck {
		s.Button.Ack(req.Seq)
		return response{OK: true}
	}
	b, err := json.Marshal(s.Button.Wait(ctx, req.Seq, buttonWaitMax))
	if err != nil {
		return response{Error: err.Error()}
	}
	return response{OK: true, Stdout: b}
}

// ButtonWait asks the helper for the button's events after since; it waits up to 25 s for one.
func (c Client) ButtonWait(ctx context.Context, since uint64) (ButtonBatch, error) {
	ctx, cancel := context.WithTimeout(ctx, buttonWaitMax+10*time.Second)
	defer cancel()
	res, err := c.call(ctx, request{Op: opButtonWait, Seq: since})
	if err != nil {
		return ButtonBatch{}, err
	}
	var b ButtonBatch
	if err := json.Unmarshal(res.Stdout, &b); err != nil {
		return ButtonBatch{}, fmt.Errorf("button-wait: %w", err)
	}
	return b, nil
}

// ButtonAck tells the helper the daemon takes the event seq.
func (c Client) ButtonAck(ctx context.Context, seq uint64) error {
	_, err := c.call(ctx, request{Op: opButtonAck, Seq: seq})
	return err
}
