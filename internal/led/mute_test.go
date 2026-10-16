package led

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
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

// occulited B-77: the mute outlives a restart of occulited - led-mute.json beside led.json - and
// is dropped when it ran out meanwhile or ends further ahead than a mute can last.
func TestControllerMuteKeptAcrossRestart(t *testing.T) {
	r := upRig(t, charly())
	until, err := r.c.Mute(30 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(filepath.Dir(r.c.File), "led-mute.json")
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("led-mute.json: %v %v", st, err)
	}
	restart := func() *Controller {
		return &Controller{Root: r.root, File: r.c.File, Now: r.c.Now, Write: r.c.Write, LoadModule: r.c.LoadModule, Src: r.c.Src}
	}
	again := restart()
	if m, u := again.Muted(); !m || !u.Equal(until) {
		t.Fatalf("not restored: %v %v", m, u)
	}
	if v := again.View(); v.MutedUntil == nil || !v.MutedUntil.Equal(until) {
		t.Errorf("view after the restart: %+v", v.MutedUntil)
	}
	// un-muting removes the file
	again.Unmute()
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file stays after Unmute: %v", err)
	}
	if m, _ := restart().Muted(); m {
		t.Error("muted again after Unmute and a restart")
	}
	// one that ran out while occulited was stopped
	if _, err := again.Mute(MuteMin); err != nil {
		t.Fatal(err)
	}
	r.advance(MuteMin)
	if m, _ := restart().Muted(); m {
		t.Error("a mute that ran out was restored")
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the run-out mute's file stays: %v", err)
	}
	// running out while running removes the file too
	c3 := restart()
	if _, err := c3.Mute(MuteMin); err != nil {
		t.Fatal(err)
	}
	r.advance(MuteMin)
	c3.mu.Lock()
	c3.expire(c3.now())
	c3.mu.Unlock()
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file stays after the mute ran out: %v", err)
	}
	// further ahead than a mute lasts (the clock went back), and a broken file
	for name, body := range map[string]string{
		"too far": `{"muted_until":"` + r.clock.Add(MuteMax+time.Hour).Format(time.RFC3339) + `"}`,
		"broken":  `{`,
		"zero":    `{}`,
	} {
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if m, _ := restart().Muted(); m {
			t.Errorf("%s: restored", name)
		}
		if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: the file stays: %v", name, err)
		}
	}
	// no state file at all: in memory only
	mem := &Controller{Root: r.root, Now: r.c.Now}
	if _, err := mem.Mute(0); err != nil {
		t.Fatal(err)
	}
	if m, _ := mem.Muted(); !m {
		t.Error("a controller without a file does not mute")
	}
}

// occulited B-77: the red power LED (the error light without an RGB LED) follows the mute's rule:
// service-failed and storage-replace are silenced, radio-down still blinks.
func TestControllerPWRErrorLightMuted(t *testing.T) {
	r := upRig(t, pi4())
	cfg := r.c.View().Config
	cfg.PWRErrorLight = true
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	blink := []priv.LEDWrite{{LED: PWRLED, Trigger: "timer", DelayOn: 100, DelayOff: 100}}
	normal := []priv.LEDWrite{{LED: PWRLED, Trigger: "default-on"}}
	r.setUnit("lighttpd.service", "failed", "failed")
	r.steps(3)
	if !slices.Equal(r.lastFrame(), blink) {
		t.Fatalf("service-failed: %+v", r.lastFrame())
	}
	if _, err := r.c.Mute(0); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	if !slices.Equal(r.lastFrame(), normal) {
		t.Errorf("muted service-failed still blinks: %+v", r.lastFrame())
	}
	if st := r.c.State(); st.PWR == nil || st.PWR.Error {
		t.Errorf("state while muted: %+v", st.PWR)
	}
	r.c.Unmute()
	r.steps(1)
	if !slices.Equal(r.lastFrame(), blink) {
		t.Errorf("after the un-mute: %+v", r.lastFrame())
	}
	// radio-down is never muted
	r.setUnit("lighttpd.service", "active", "running")
	r.steps(1)
	if !slices.Equal(r.lastFrame(), normal) {
		t.Fatalf("back to normal: %+v", r.lastFrame())
	}
	if _, err := r.c.Mute(0); err != nil {
		t.Fatal(err)
	}
	r.setUnit("rfd.service", "failed", "failed")
	r.steps(3)
	r.c.mu.Lock()
	_, active := r.c.inputs(r.clock).Active[StateRadioDown]
	r.c.mu.Unlock()
	if !active {
		t.Fatal("the rig's rfd failure is not radio-down")
	}
	if !slices.Equal(r.lastFrame(), blink) {
		t.Errorf("muted radio-down does not blink: %+v", r.lastFrame())
	}
}
