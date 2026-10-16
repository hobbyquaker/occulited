package led

import (
	"slices"
	"testing"
)

// occulited task 57: the two firmware rows - off by default, below every fault and warning (and
// the radio load's rows), a transfer above a firmware waiting for its install.
func TestFirmwareStatesDefaults(t *testing.T) {
	d := Defaults()
	order := statesOrder(d)
	idx := func(id string) int { return slices.Index(order, id) }
	tr, rd := idx(StateFirmwareTransfer), idx(StateFirmwareReady)
	if tr < 0 || rd != tr+1 {
		t.Fatalf("order %v", order)
	}
	for _, above := range []string{StateRadioDown, StateNoNetwork, StateServiceFailed, StateStorageReplace, StateInterfacesStarting, StateRadioDutyCycle, StateRadioCarrierSense, StateStatusWarning} {
		if idx(above) > tr {
			t.Errorf("%s is below the firmware rows: %v", above, order)
		}
	}
	for _, id := range []string{StateFirmwareTransfer, StateFirmwareReady} {
		s, ok := d.State(id)
		if !ok || s.Enabled {
			t.Errorf("%s: on by default or missing: %+v", id, s)
		}
	}
	if s, _ := d.State(StateFirmwareTransfer); s.Look() != (Look{Color: Blue, Pattern: Breathe}) {
		t.Errorf("transfer look %+v", s.Look())
	}
	if s, _ := d.State(StateFirmwareReady); s.Look() != (Look{Color: Green, Pattern: Double}) {
		t.Errorf("ready look %+v", s.Look())
	}
	// a stored list from before task 57 gets both rows at their default places
	old := Defaults()
	old.States = slices.DeleteFunc(slices.Clone(old.States), func(s StateConfig) bool {
		return s.ID == StateFirmwareTransfer || s.ID == StateFirmwareReady
	})
	v, err := Validate(old)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(statesOrder(v), order) {
		t.Errorf("an old list: %v, want %v", statesOrder(v), order)
	}
}

func TestResolveFirmware(t *testing.T) {
	cfg := Defaults()
	for i := range cfg.States {
		if cfg.States[i].ID == StateFirmwareTransfer || cfg.States[i].ID == StateFirmwareReady {
			cfg.States[i].Enabled = true
		}
	}
	transfer := Look{Color: Blue, Pattern: Breathe}
	ready := Look{Color: Green, Pattern: Double}
	for _, c := range []struct {
		name   string
		active []string
		id     string
		look   Look
	}{
		{"transfer alone", []string{StateFirmwareTransfer}, StateFirmwareTransfer, transfer},
		{"ready alone", []string{StateFirmwareReady}, StateFirmwareReady, ready},
		{"transfer above ready", []string{StateFirmwareReady, StateFirmwareTransfer}, StateFirmwareTransfer, transfer},
		{"a warning above both", []string{StateFirmwareTransfer, StateFirmwareReady, StateStatusWarning}, StateStatusWarning, Look{Color: Yellow, Pattern: Slow}},
		{"radio-down above both", []string{StateFirmwareTransfer, StateRadioDown}, StateRadioDown, Look{Color: Red, Pattern: Solid}},
		{"both above a system update", []string{StateSystemUpdate, StateFirmwareReady}, StateFirmwareReady, ready},
	} {
		in := Inputs{Now: at("12:00"), Location: nil, Config: cfg, Active: map[string]Activity{}}
		for _, id := range c.active {
			in.Active[id] = Activity{Detail: "1"}
		}
		if s := Resolve(in); s.ID != c.id || s.Look != c.look {
			t.Errorf("%s: %+v", c.name, s)
		}
	}
	// switched off (the default): nothing of them shows
	in := Inputs{Now: at("12:00"), Config: Defaults(), Active: map[string]Activity{StateFirmwareTransfer: {}, StateFirmwareReady: {}}}
	if s := Resolve(in); s.Source != SourceNormal {
		t.Errorf("off by default: %+v", s)
	}
}

// The controller reads the firmware service's counts: the detail is the number of devices, and the
// rows clear when the counts are zero again.
func TestControllerFirmwareStates(t *testing.T) {
	r := newRig(t, charly())
	var tr, rd int
	r.c.Src.Firmware = func() (int, int) { r.mu.Lock(); defer r.mu.Unlock(); return tr, rd }
	set := func(a, b int) { r.mu.Lock(); tr, rd = a, b; r.mu.Unlock() }
	cfg := Defaults()
	for i := range cfg.States {
		if cfg.States[i].ID == StateFirmwareTransfer || cfg.States[i].ID == StateFirmwareReady {
			cfg.States[i].Enabled = true
		}
	}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.file("run/occulite/radio/render.json", true)
	r.file("var/status/startupFinished", true)
	r.steps(2)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	set(2, 1)
	r.advance(refreshEvery)
	r.steps(2)
	r.expectShown(Look{Color: Blue, Pattern: Breathe}, StateFirmwareTransfer)
	if st := r.c.State(); st.Shown.Detail != "2" {
		t.Errorf("detail %+v", st.Shown)
	}
	set(0, 3)
	r.advance(refreshEvery)
	r.steps(2)
	r.expectShown(Look{Color: Green, Pattern: Double}, StateFirmwareReady)
	if st := r.c.State(); st.Shown.Detail != "3" {
		t.Errorf("detail %+v", st.Shown)
	}
	set(0, 0)
	r.advance(refreshEvery)
	r.steps(2)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
}
