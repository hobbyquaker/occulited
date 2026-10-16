package led

import (
	"slices"
	"strings"
	"testing"
)

// openccu-lite task 316: the radio load's levels - thresholds with hysteresis, the highest wins,
// the active level's look on the row, the switches and the defaults.
func TestRadioLevelOf(t *testing.T) {
	levels := defaultRadio().DutyCycle
	for i := range levels {
		levels[i].Enabled = true
	}
	for _, c := range []struct{ value, current, want int }{
		{10, 0, 0}, {25, 0, 0}, {26, 0, 1}, {51, 0, 2}, {90, 0, 3}, {90, 1, 3},
		// the hysteresis: a level holds until the value is 5 points below its threshold
		{24, 1, 1}, {21, 1, 1}, {20, 1, 0}, {48, 2, 2}, {45, 2, 1}, {44, 2, 1}, {20, 2, 0}, {72, 3, 3}, {70, 3, 2}, {10, 3, 0},
	} {
		if got := RadioLevelOf(levels, c.value, c.current); got != c.want {
			t.Errorf("value %d at level %d: %d, want %d", c.value, c.current, got, c.want)
		}
	}
	// a level switched off is skipped on the way up and does not hold
	off := slices.Clone(levels)
	off[1].Enabled = false
	if got := RadioLevelOf(off, 60, 0); got != 1 {
		t.Errorf("level 2 off, 60 %%: %d, want 1", got)
	}
	if got := RadioLevelOf(off, 60, 2); got != 1 {
		t.Errorf("level 2 off while at 2: %d, want 1", got)
	}
	if got := RadioLevelOf(off, 80, 1); got != 3 {
		t.Errorf("level 2 off, 80 %%: %d, want 3", got)
	}
	none := slices.Clone(levels)
	for i := range none {
		none[i].Enabled = false
	}
	if got := RadioLevelOf(none, 99, 0); got != 0 {
		t.Errorf("all off: %d", got)
	}
}

func TestRadioConfigValidate(t *testing.T) {
	c := Defaults()
	if dc, cs := c.Radio.DutyCycle, c.Radio.CarrierSense; len(dc) != 3 || len(cs) != 3 || dc[0].Threshold != 25 || cs[2].Threshold != 20 || dc[0].Enabled || !dc[0].OverNormal || dc[2].OverNormal {
		t.Errorf("defaults: %+v %+v", dc, cs)
	}
	for _, id := range RadioStates {
		st, ok := c.State(id)
		if !ok || st.Enabled || st.Color != "" {
			t.Errorf("default row %s: %+v %v", id, st, ok)
		}
	}
	v, err := Validate(c)
	if err != nil {
		t.Fatal(err)
	}
	// the rows carry the switch and the place only
	v.States[slices.IndexFunc(v.States, func(s StateConfig) bool { return s.ID == StateRadioDutyCycle })].Color = Red
	v, err = Validate(v)
	if err != nil || v.States[slices.IndexFunc(v.States, func(s StateConfig) bool { return s.ID == StateRadioDutyCycle })].Color != "" {
		t.Errorf("a radio row's own colour is dropped: %v", err)
	}
	// a file from before the levels: the defaults
	old := Defaults()
	old.Radio = RadioConfig{}
	if v, err := Validate(old); err != nil || len(v.Radio.DutyCycle) != 3 || v.Radio.CarrierSense[0].Threshold != 5 {
		t.Errorf("no radio block: %+v %v", v.Radio, err)
	}
	bad := map[string]func(c *Config){
		"two levels":         func(c *Config) { c.Radio.DutyCycle = c.Radio.DutyCycle[:2] },
		"thresholds falling": func(c *Config) { c.Radio.CarrierSense[1].Threshold = 3 },
		"threshold 100":      func(c *Config) { c.Radio.DutyCycle[2].Threshold = 100 },
		"a dark level":       func(c *Config) { c.Radio.DutyCycle[0].Color = Off },
		"a bad colour":       func(c *Config) { c.Radio.CarrierSense[0].Color = "amber" },
	}
	for name, mutate := range bad {
		c := Defaults()
		mutate(&c)
		if _, err := Validate(c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// the levels' looks are normalised: a hex that is a name's value becomes the name
	c = Defaults()
	c.Radio.DutyCycle[0].Color, c.Radio.DutyCycle[0].Enabled = "#FFFF00", true
	v, _ = Validate(c)
	if v.Radio.DutyCycle[0].Color != Yellow || !v.Radio.DutyCycle[0].Enabled {
		t.Errorf("normalised level: %+v", v.Radio.DutyCycle[0])
	}
	if l := v.Radio.LevelLook(StateRadioCarrierSense, 3); l.Color != Red || l.Pattern != Fast {
		t.Errorf("level look: %+v", l)
	}
	if l := v.Radio.LevelLook(StateRadioDutyCycle, 0); l != LookDark {
		t.Errorf("no level: %+v", l)
	}
}

// The controller: the radio rows switched on show the active level's look, the detail names the
// interface and the value, and the level follows the sampler with hysteresis.
func TestControllerRadioLevels(t *testing.T) {
	r := newRig(t, charly())
	var loads []RadioLoad
	r.c.Src.Radio = func() []RadioLoad { r.mu.Lock(); defer r.mu.Unlock(); return slices.Clone(loads) }
	set := func(dc, cs int) {
		r.mu.Lock()
		defer r.mu.Unlock()
		cs1 := 1
		loads = []RadioLoad{{Interface: "BidCos-RF", DutyCycle: dc / 2}, {Interface: "HmIP-RF", DutyCycle: dc, CarrierSense: cs, HasCS: true}, {Interface: "BidCos-RF", DutyCycle: 0, CarrierSense: cs1, HasCS: true}}
	}
	cfg := Defaults()
	for i := range cfg.Radio.DutyCycle {
		cfg.Radio.DutyCycle[i].Enabled = true
		cfg.Radio.CarrierSense[i].Enabled = true
	}
	for i := range cfg.States {
		if IsRadioState(cfg.States[i].ID) {
			cfg.States[i].Enabled = true
		}
	}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.file("run/occulite/radio/render.json", true)
	r.file("var/status/startupFinished", true)
	set(10, 2)
	r.steps(2)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// 63 % duty cycle: level 2, orange-red slow over blue; the detail names HmIP-RF
	set(63, 2)
	r.advance(refreshEvery)
	r.steps(2)
	r.expectShownOver(Look{Color: "#ff4000", Pattern: Slow, OverNormal: true}, Blue, StateRadioDutyCycle)
	st := r.c.State()
	if st.Shown.Level != 2 || !strings.Contains(st.Shown.Detail, "HmIP-RF 63 %") {
		t.Errorf("shown: %+v", st.Shown)
	}
	if a := st.Active; len(a) != 1 || a[0].ID != StateRadioDutyCycle || a[0].Level != 2 {
		t.Errorf("active: %+v", a)
	}
	// 47 %: still level 2 (the hysteresis holds to 45); 44 %: level 1; 20 %: none
	set(47, 2)
	r.advance(refreshEvery)
	r.steps(2)
	r.expectShownOver(Look{Color: "#ff4000", Pattern: Slow, OverNormal: true}, Blue, StateRadioDutyCycle)
	set(44, 2)
	r.advance(refreshEvery)
	r.steps(2)
	r.expectShownOver(Look{Color: Yellow, Pattern: Breathe, OverNormal: true}, Blue, StateRadioDutyCycle)
	set(20, 2)
	r.advance(refreshEvery)
	r.steps(2)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// the carrier sense at 21 %: level 3, red fast instead of normal; a failed radio stays above it
	set(20, 21)
	r.advance(refreshEvery)
	r.steps(2)
	r.expectShown(Look{Color: Red, Pattern: Fast}, StateRadioCarrierSense)
	// the row switched off: nothing of it shows
	cfg.States[slices.IndexFunc(cfg.States, func(s StateConfig) bool { return s.ID == StateRadioCarrierSense })].Enabled = false
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.steps(2)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
}

// occulited task 61: while a device firmware transfer runs on an interface, its duty cycle counts
// only for the levels from 75 % up - the default levels 25 and 50 % stay dark at 60 %, 76 % is
// level 3; without the transfer 60 % is level 2 again.
func TestControllerRadioLevelsDuringATransfer(t *testing.T) {
	r := newRig(t, charly())
	var loads []RadioLoad
	r.c.Src.Radio = func() []RadioLoad { r.mu.Lock(); defer r.mu.Unlock(); return slices.Clone(loads) }
	set := func(dc int, transfer bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		loads = []RadioLoad{{Interface: "BidCos-RF", DutyCycle: 3}, {Interface: "HmIP-RF", DutyCycle: dc, Transfer: transfer}}
	}
	cfg := Defaults()
	for i := range cfg.Radio.DutyCycle {
		cfg.Radio.DutyCycle[i].Enabled = true
	}
	for i := range cfg.States {
		if cfg.States[i].ID == StateRadioDutyCycle {
			cfg.States[i].Enabled = true
		}
	}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.file("run/occulite/radio/render.json", true)
	r.file("var/status/startupFinished", true)
	level := func() int {
		for _, a := range r.c.State().Active {
			if a.ID == StateRadioDutyCycle {
				return a.Level
			}
		}
		return 0
	}
	for _, c := range []struct {
		dc       int
		transfer bool
		want     int
	}{
		{60, true, 0},
		{76, true, 3},
		{60, true, 0}, // 60 is under 75 less the hysteresis
		{60, false, 2},
		{30, false, 1},
	} {
		set(c.dc, c.transfer)
		r.advance(refreshEvery)
		r.steps(2)
		if got := level(); got != c.want {
			t.Errorf("%d %% transfer %v: level %d, want %d", c.dc, c.transfer, got, c.want)
		}
	}
	if l := transferLevels(cfg.Radio.DutyCycle); l[0].Enabled || l[1].Enabled || !l[2].Enabled || !cfg.Radio.DutyCycle[0].Enabled {
		t.Errorf("transferLevels: %+v (the config itself untouched: %v)", l, cfg.Radio.DutyCycle[0].Enabled)
	}
}
