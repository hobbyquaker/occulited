package button

import (
	"errors"
	"fmt"
	"slices"
)

// The actions a gesture can have (the maintainer, 2026-10-15).
const (
	ActionNone     = "none"
	ActionMute     = "mute"     // the LED's warning states muted for MuteMinutes, a second press un-mutes
	ActionLocate   = "locate"   // the LED's locate blink, toggled
	ActionRecovery = "recovery" // reboot into the recovery system - the long press only
	ActionReboot   = "reboot"   // reboot - the long press only
	ActionEvent    = "event"    // an event for RedMatic / Node-RED: task 166, not offered yet
)

// Actions are the actions single and double may have; LongActions the long press's.
var (
	Actions     = []string{ActionNone, ActionMute, ActionLocate}
	LongActions = []string{ActionNone, ActionMute, ActionLocate, ActionRecovery, ActionReboot}
)

// Settings is occulited.json's hardware.button.
type Settings struct {
	Single string `json:"single"`
	Double string `json:"double"`
	Long   string `json:"long"`
	// LongS is the long press's threshold in seconds (3-10, 5 by default)
	LongS int `json:"long_s"`
	// MuteMinutes is how long a mute lasts (5-1440, 60 by default)
	MuteMinutes int `json:"mute_minutes"`
}

// DefaultSettings: a single press mutes the warnings, double and long do nothing (the maintainer,
// Q&A 2026-10-15).
func DefaultSettings() Settings {
	return Settings{Single: ActionMute, Double: ActionNone, Long: ActionNone, LongS: int(LongDefault.Seconds()), MuteMinutes: 60}
}

// ErrInvalid wraps every refusal of the settings.
var ErrInvalid = errors.New("invalid")

// Normalize fills what a file from before leaves out and checks the rest.
func (s Settings) Normalize() (Settings, error) {
	d := DefaultSettings()
	if s.Single == "" {
		s.Single = d.Single
	}
	if s.Double == "" {
		s.Double = d.Double
	}
	if s.Long == "" {
		s.Long = d.Long
	}
	if s.LongS == 0 {
		s.LongS = d.LongS
	}
	if s.MuteMinutes == 0 {
		s.MuteMinutes = d.MuteMinutes
	}
	if !slices.Contains(Actions, s.Single) {
		return s, fmt.Errorf("%w: single: %q is not one of %v", ErrInvalid, s.Single, Actions)
	}
	if !slices.Contains(Actions, s.Double) {
		return s, fmt.Errorf("%w: double: %q is not one of %v", ErrInvalid, s.Double, Actions)
	}
	if !slices.Contains(LongActions, s.Long) {
		return s, fmt.Errorf("%w: long: %q is not one of %v", ErrInvalid, s.Long, LongActions)
	}
	if s.LongS < int(LongMin.Seconds()) || s.LongS > int(LongMax.Seconds()) {
		return s, fmt.Errorf("%w: long_s is %d to %d", ErrInvalid, int(LongMin.Seconds()), int(LongMax.Seconds()))
	}
	if s.MuteMinutes < 5 || s.MuteMinutes > 1440 {
		return s, fmt.Errorf("%w: mute_minutes is 5 to 1440", ErrInvalid)
	}
	return s, nil
}

// For is a gesture's action.
func (s Settings) For(gesture string) string {
	switch gesture {
	case GestureSingle:
		return s.Single
	case GestureDouble:
		return s.Double
	case GestureLong:
		return s.Long
	}
	return ActionNone
}
