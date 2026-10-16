package led

import (
	"errors"
	"testing"
	"time"
)

// openccu-lite task 164: a mute skips every configurable state but radio and network down (and
// interfaces-starting), the overrides too; the button's feedback is above everything but shutdown
// and booting.
func TestResolveMuteAndFeedback(t *testing.T) {
	cfg := Defaults()
	now := at("12:00")
	until := now.Add(time.Minute)
	green := Override{ID: "flow", Color: Green, Pattern: Solid, Priority: "normal", Night: "respect", Since: now}
	for _, c := range []struct {
		name   string
		active []string
		over   bool
		id     string
	}{
		{"status-warning muted", []string{StateStatusWarning}, false, StateNormal},
		{"service-failed muted", []string{StateServiceFailed}, false, StateNormal},
		{"storage-replace muted", []string{StateStorageReplace}, false, StateNormal},
		{"system-update muted", []string{StateSystemUpdate}, false, StateNormal},
		{"an override muted", nil, true, StateNormal},
		{"radio-down shows", []string{StateRadioDown, StateStatusWarning}, false, StateRadioDown},
		{"no-network shows", []string{StateNoNetwork}, false, StateNoNetwork},
		{"interfaces-starting shows", []string{StateInterfacesStarting}, false, StateInterfacesStarting},
	} {
		in := Inputs{Now: now, Config: cfg, Active: map[string]Activity{}, Muted: true}
		for _, id := range c.active {
			in.Active[id] = Activity{}
		}
		if c.over {
			in.Overrides = []Override{green}
		}
		if s := Resolve(in); s.ID != c.id {
			t.Errorf("%s: %+v", c.name, s)
		}
	}
	fb := &Timed{Look: Look{Color: White, Pattern: Flash}, Until: until}
	in := Inputs{Now: now, Config: cfg, Active: map[string]Activity{StateRadioDown: {}}, Feedback: fb, Locate: &Timed{Look: Look{Color: White, Pattern: Fast}, Until: until}}
	if s := Resolve(in); s.Source != SourceFeedback || s.Look != fb.Look {
		t.Errorf("feedback above locate and radio-down: %+v", s)
	}
	in.Booting = true
	if s := Resolve(in); s.ID != StateBooting {
		t.Errorf("booting above the feedback: %+v", s)
	}
	in = Inputs{Now: until, Config: cfg, Active: map[string]Activity{}, Feedback: fb}
	if s := Resolve(in); s.Source != SourceNormal {
		t.Errorf("an expired feedback: %+v", s)
	}
}

func TestControllerMute(t *testing.T) {
	r := newRig(t, charly())
	r.file("run/occulite/radio/render.json", true)
	r.file("var/status/startupFinished", true)
	r.steps(2)
	if _, err := r.c.Mute(time.Minute); !errors.Is(err, ErrInvalid) {
		t.Errorf("a minute is below the bound: %v", err)
	}
	until, err := r.c.Mute(0)
	if err != nil || !until.Equal(r.clock.Add(MuteDefault)) {
		t.Fatalf("default mute: %v %v", until, err)
	}
	if st := r.c.State(); st.MutedUntil == nil || !st.MutedUntil.Equal(until) {
		t.Errorf("state: %+v", st.MutedUntil)
	}
	if v := r.c.View(); v.MutedUntil == nil || len(v.NeverMuted) != 3 {
		t.Errorf("view: %+v %v", v.MutedUntil, v.NeverMuted)
	}
	if m, _ := r.c.Muted(); !m {
		t.Error("not muted")
	}
	r.c.Unmute()
	if m, _ := r.c.Muted(); m || r.c.State().MutedUntil != nil {
		t.Error("still muted after Unmute")
	}
	// it runs out on its own
	if _, err := r.c.Mute(MuteMin); err != nil {
		t.Fatal(err)
	}
	r.advance(MuteMin)
	r.steps(1)
	if m, _ := r.c.Muted(); m {
		t.Error("the mute did not run out")
	}
	// the feedback shows and ends
	r.c.Feedback(Look{Color: White, Pattern: Flash}, 15*time.Second)
	r.steps(1)
	if st := r.c.State(); st.Shown.Source != SourceFeedback {
		t.Errorf("feedback not shown: %+v", st.Shown)
	}
	r.steps(2)
	if st := r.c.State(); st.Shown.Source == SourceFeedback {
		t.Errorf("feedback did not end: %+v", st.Shown)
	}
}
