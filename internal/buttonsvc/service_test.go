package buttonsvc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/button"
	"github.com/hobbyquaker/occulited/internal/led"
	"github.com/hobbyquaker/occulited/internal/priv"
)

type fakeLED struct {
	mu       sync.Mutex
	muted    time.Time
	locate   bool
	feedback []led.Look
	muteErr  error
}

func (f *fakeLED) Mute(d time.Duration) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.muteErr != nil {
		return time.Time{}, f.muteErr
	}
	f.muted = time.Now().Add(d)
	return f.muted, nil
}
func (f *fakeLED) Unmute() { f.mu.Lock(); f.muted = time.Time{}; f.mu.Unlock() }
func (f *fakeLED) Muted() (bool, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.muted.IsZero(), f.muted
}
func (f *fakeLED) Feedback(l led.Look, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d > 0 {
		f.feedback = append(f.feedback, l)
	}
}
func (f *fakeLED) StartLocate(int) (led.Timed, error) {
	f.mu.Lock()
	f.locate = true
	f.mu.Unlock()
	return led.Timed{}, nil
}
func (f *fakeLED) StopLocate() error { f.mu.Lock(); f.locate = false; f.mu.Unlock(); return nil }
func (f *fakeLED) State() led.StateView {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locate {
		return led.StateView{Locate: &led.Timed{}}
	}
	return led.StateView{}
}

// helper plays the helper's button-wait: one batch per call from a queue, then waits.
type helper struct {
	mu      sync.Mutex
	batches []priv.ButtonBatch
	acked   []uint64
}

func (h *helper) wait(ctx context.Context, since uint64) (priv.ButtonBatch, error) {
	h.mu.Lock()
	if len(h.batches) > 0 {
		b := h.batches[0]
		h.batches = h.batches[1:]
		h.mu.Unlock()
		return b, nil
	}
	h.mu.Unlock()
	<-ctx.Done()
	return priv.ButtonBatch{}, ctx.Err()
}

func (h *helper) ack(_ context.Context, seq uint64) error {
	h.mu.Lock()
	h.acked = append(h.acked, seq)
	h.mu.Unlock()
	return nil
}

func ev(seq uint64, kind string) priv.ButtonEvent {
	return priv.ButtonEvent{Seq: seq, Event: button.Event{Kind: kind, At: time.Now()}}
}

func runService(t *testing.T, s *Service, h *helper) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.Wait, s.Ack = h.wait, h.ack
	s.Pause = time.Millisecond
	s.Run(ctx)
}

func TestServiceActions(t *testing.T) {
	l := &fakeLED{}
	var recov, reboot int
	set := button.Settings{Single: button.ActionMute, Double: button.ActionLocate, Long: button.ActionRecovery}
	s := &Service{LED: l, Settings: func() button.Settings { return set }, Recovery: func() error { recov++; return nil }, Reboot: func() error { reboot++; return nil }}
	h := &helper{batches: []priv.ButtonBatch{
		{Available: true, Seq: 2, Events: []priv.ButtonEvent{ev(1, button.EventPress), ev(2, button.GestureSingle)}},
		{Available: true, Seq: 3, Events: []priv.ButtonEvent{ev(3, button.GestureDouble)}},
		{Available: true, Seq: 4, Events: []priv.ButtonEvent{ev(4, button.GestureSingle)}},
		{Available: true, Seq: 6, Events: []priv.ButtonEvent{ev(5, button.EventLongReached), ev(6, button.GestureLong)}},
	}}
	runService(t, s, h)
	if m, _ := l.Muted(); m {
		t.Error("the second single did not un-mute")
	}
	if !l.locate {
		t.Error("the double did not start locate")
	}
	if recov != 1 || reboot != 0 {
		t.Errorf("recovery %d reboot %d", recov, reboot)
	}
	if len(h.acked) != 1 || h.acked[0] != 6 {
		t.Errorf("acked %v, want the long press", h.acked)
	}
	if st := s.State(); !st.Available || st.Last == nil || st.Last.Kind != button.GestureLong || st.Last.Action != button.ActionRecovery {
		t.Errorf("state %+v", st)
	}
	if len(l.feedback) == 0 {
		t.Error("no feedback on the LED")
	}
}

func TestServiceDefaultsAndFailures(t *testing.T) {
	l := &fakeLED{}
	s := &Service{LED: l} // no settings: the defaults - single mutes, long nothing
	h := &helper{batches: []priv.ButtonBatch{
		{Available: true, Seq: 2, Events: []priv.ButtonEvent{ev(1, button.GestureSingle), ev(2, button.GestureLong)}},
	}}
	runService(t, s, h)
	if m, _ := l.Muted(); !m {
		t.Error("the default single did not mute")
	}
	if len(h.acked) != 0 {
		t.Errorf("a long press without a reboot action was acknowledged: %v", h.acked)
	}
	// a failing action: the error in the state, red on the LED
	l2 := &fakeLED{muteErr: errors.New("no")}
	s2 := &Service{LED: l2}
	runService(t, s2, &helper{batches: []priv.ButtonBatch{{Available: true, Seq: 1, Events: []priv.ButtonEvent{ev(1, button.GestureSingle)}}}})
	if st := s2.State(); st.Last == nil || st.Last.Error == "" {
		t.Errorf("the failure is not in the state: %+v", st.Last)
	}
	// events from before the daemon started are history
	l3 := &fakeLED{}
	s3 := &Service{LED: l3}
	old := priv.ButtonEvent{Seq: 1, Event: button.Event{Kind: button.GestureSingle, At: time.Now().Add(-time.Minute)}}
	runService(t, s3, &helper{batches: []priv.ButtonBatch{{Available: true, Seq: 1, Events: []priv.ButtonEvent{old}}}})
	if m, _ := l3.Muted(); m {
		t.Error("an old gesture acted")
	}
	// unavailable: the reason in the state
	s4 := &Service{}
	runService(t, s4, &helper{batches: []priv.ButtonBatch{{Reason: priv.ButtonAdapter}}})
	if st := s4.State(); st.Available || st.Reason != priv.ButtonAdapter {
		t.Errorf("unavailable: %+v", st)
	}
	if st := (&Service{}).State(); st.Reason != priv.ButtonDetecting {
		t.Errorf("before the first answer: %+v", st)
	}
}

func TestSettings(t *testing.T) {
	s, err := button.Settings{}.Normalize()
	if err != nil || s != button.DefaultSettings() {
		t.Fatalf("empty: %+v %v", s, err)
	}
	if s.Single != button.ActionMute || s.Double != button.ActionNone || s.Long != button.ActionNone || s.LongS != 5 || s.MuteMinutes != 60 {
		t.Errorf("defaults %+v", s)
	}
	for _, bad := range []button.Settings{
		{Single: button.ActionRecovery}, {Double: button.ActionReboot}, {Single: button.ActionEvent}, {Long: "shutdown"},
		{LongS: 2}, {LongS: 11}, {MuteMinutes: 4}, {MuteMinutes: 1441},
	} {
		if _, err := bad.Normalize(); !errors.Is(err, button.ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if s := (button.Settings{Long: button.ActionRecovery, LongS: 3}); s.For(button.GestureLong) != button.ActionRecovery || s.For("x") != button.ActionNone {
		t.Error("For")
	}
}
