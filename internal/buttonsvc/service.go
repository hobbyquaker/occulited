// Package buttonsvc is the daemon's half of the RPI-RF-MOD's button (openccu-lite task 164): it
// takes the gestures from the privileged helper (internal/priv's button-wait) and runs the action
// the Hardware page set for each - mute the LED's warnings, toggle locate, reboot, reboot into the
// recovery system - with the LED's own feedback. The helper reads the line and runs the Detector
// (internal/button); a long press set to recovery that this side does not take within a second the
// helper executes itself, so the button works while occulited does not.
package buttonsvc

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/button"
	"github.com/hobbyquaker/occulited/internal/led"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// LED is what the service needs of the status LED's controller.
type LED interface {
	Mute(d time.Duration) (time.Time, error)
	Unmute()
	Muted() (bool, time.Time)
	Feedback(l led.Look, d time.Duration)
	StartLocate(durationS int) (led.Timed, error)
	StopLocate() error
	State() led.StateView
}

// Service runs the button's actions.
type Service struct {
	// Wait and Ack are the helper's button-wait and button-ack.
	Wait func(ctx context.Context, since uint64) (priv.ButtonBatch, error)
	Ack  func(ctx context.Context, seq uint64) error
	// Settings reads the gestures' actions (occulited.json hardware.button).
	Settings func() button.Settings
	LED      LED
	// Recovery and Reboot are the power menu's (the API's own steps: the marker, the boot-timing
	// mark, the reboot); nil = not offered.
	Recovery func() error
	Reboot   func() error
	Log      *slog.Logger
	// Pause is how long the service waits after a failed call or while the button cannot be read;
	// 0 = 30 s.
	Pause time.Duration

	mu   sync.Mutex
	st   State
	seen bool
}

// State is the button's state for GET /hardware/button.
type State struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Pressed   bool   `json:"pressed"`
	// Last is the last gesture and what was done.
	Last *Gesture `json:"last,omitempty"`
	// Ignored counts the gestures the helper dropped (before the boot finished, the rate limit).
	Ignored int `json:"ignored,omitempty"`
}

// Gesture is one gesture and its action.
type Gesture struct {
	Kind   string    `json:"kind"`
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Error  string    `json:"error,omitempty"`
}

// The feedback looks: a short white flash at the press, a fast white blink from the long
// threshold on ("release now"), green for a done action, red for a failed one.
var (
	lookPress   = led.Look{Color: led.White, Pattern: led.Solid}
	lookRelease = led.Look{Color: led.White, Pattern: led.Fast}
	lookDone    = led.Look{Color: led.Green, Pattern: led.Flash}
	lookUndone  = led.Look{Color: led.Blue, Pattern: led.Flash}
	lookFailed  = led.Look{Color: led.Red, Pattern: led.Fast}
)

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// State answers the button's state.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	if !s.seen {
		st.Reason = priv.ButtonDetecting
	}
	return st
}

func (s *Service) settings() button.Settings {
	if s.Settings == nil {
		return button.DefaultSettings()
	}
	v, err := s.Settings().Normalize()
	if err != nil {
		return button.DefaultSettings()
	}
	return v
}

// Run fetches the helper's events until ctx ends.
func (s *Service) Run(ctx context.Context) {
	pause := s.Pause
	if pause <= 0 {
		pause = 30 * time.Second
	}
	var since uint64
	// what the helper kept from before this daemon started is history, not a press now
	started := time.Now().Add(-2 * time.Second)
	backoff := time.Second
	for ctx.Err() == nil {
		b, err := s.Wait(ctx, since)
		if err != nil {
			// the helper restarting (it starts before occulited, but a restart of both races):
			// soon again, so a long press set to recovery is not left to the helper's fallback
			if ctx.Err() == nil {
				s.log().Debug("button: the helper did not answer", "err", err)
			}
			sleep(ctx, min(backoff, pause))
			backoff = min(2*backoff, pause)
			continue
		}
		backoff = time.Second
		s.mu.Lock()
		s.seen = true
		s.st.Available, s.st.Reason, s.st.Pressed, s.st.Ignored = b.Available, b.Reason, b.Pressed, b.Ignored
		s.mu.Unlock()
		for _, e := range b.Events {
			if e.At.Before(started) {
				continue
			}
			s.handle(ctx, e)
		}
		since = b.Seq
		// button-wait waits for a change itself; a helper without the button answers at once
		if !b.Available && b.Reason != priv.ButtonDetecting && len(b.Events) == 0 {
			sleep(ctx, pause)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (s *Service) handle(ctx context.Context, e priv.ButtonEvent) {
	switch e.Kind {
	case button.EventPress:
		s.feedback(lookPress, time.Second)
		return
	case button.EventLongReached:
		s.feedback(lookRelease, 11*time.Second) // until the release, or the cancel
		return
	case button.EventCancel:
		s.feedback(led.Look{}, 0)
		s.log().Info("button: held too long, nothing done")
		return
	case button.EventRelease:
		return
	}
	set := s.settings()
	action := set.For(e.Kind)
	if e.Kind == button.GestureLong && (action == button.ActionRecovery || action == button.ActionReboot) {
		// the helper's fallback waits for this
		if s.Ack != nil {
			if err := s.Ack(ctx, e.Seq); err != nil {
				s.log().Warn("button: the helper did not take the acknowledgement", "err", err)
			}
		}
	}
	g := &Gesture{Kind: e.Kind, At: e.At, Action: action}
	err := s.act(action, set)
	if err != nil {
		g.Error = err.Error()
		s.feedback(lookFailed, 3*time.Second)
		s.log().Warn("button: "+e.Kind+" press: "+action+" failed", "err", err)
	} else {
		s.log().Info("button: " + e.Kind + " press: " + action)
	}
	s.mu.Lock()
	s.st.Last = g
	s.mu.Unlock()
}

func (s *Service) feedback(l led.Look, d time.Duration) {
	if s.LED != nil {
		s.LED.Feedback(l, d)
	}
}

func (s *Service) act(action string, set button.Settings) error {
	switch action {
	case button.ActionNone, "":
		s.feedback(led.Look{}, 0)
		return nil
	case button.ActionMute:
		if s.LED == nil {
			return errNoLED
		}
		if muted, _ := s.LED.Muted(); muted {
			s.LED.Unmute()
			s.feedback(lookUndone, 2*time.Second)
			return nil
		}
		if _, err := s.LED.Mute(time.Duration(set.MuteMinutes) * time.Minute); err != nil {
			return err
		}
		s.feedback(lookDone, 2*time.Second)
		return nil
	case button.ActionLocate:
		if s.LED == nil {
			return errNoLED
		}
		s.feedback(led.Look{}, 0)
		if s.LED.State().Locate != nil {
			return s.LED.StopLocate()
		}
		_, err := s.LED.StartLocate(0)
		return err
	case button.ActionRecovery:
		if s.Recovery == nil {
			return errNotOffered
		}
		s.feedback(lookDone, 3*time.Second)
		return s.Recovery()
	case button.ActionReboot:
		if s.Reboot == nil {
			return errNotOffered
		}
		s.feedback(lookDone, 3*time.Second)
		return s.Reboot()
	}
	return errNotOffered
}

type svcError string

func (e svcError) Error() string { return string(e) }

const (
	errNoLED      svcError = "no status LED controller"
	errNotOffered svcError = "the action is not offered on this system"
)
