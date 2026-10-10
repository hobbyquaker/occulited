package led

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

func TestValidate(t *testing.T) {
	d, err := Validate(Defaults())
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if b1, b2 := mustJSON(t, d), mustJSON(t, Defaults()); b1 != b2 {
		t.Errorf("the defaults changed on validation:\n%s\n%s", b1, b2)
	}
	bad := map[string]func(c *Config){
		"unknown state": func(c *Config) {
			c.States = append(c.States, StateConfig{ID: "alarm", Enabled: true, Color: Red, Pattern: Solid})
		},
		"twice":             func(c *Config) { c.States = append(c.States, c.States[0]) },
		"fixed state":       func(c *Config) { c.States = append([]StateConfig{{ID: StateBooting, Enabled: true}}, c.States...) },
		"colour":            func(c *Config) { c.States[0].Color = "orange" },
		"pattern":           func(c *Config) { c.States[0].Pattern = "pulse" },
		"hex colour short":  func(c *Config) { c.States[0].Color = "#12345" },
		"brightness low":    func(c *Config) { c.Brightness = 5 },
		"brightness high":   func(c *Config) { c.Brightness = 101 },
		"night dim":         func(c *Config) { c.Night.Dim = 150 },
		"alternate overlap": func(c *Config) { c.Normal = Look{Color: Blue, Pattern: Alternate, Color2: Cyan} },
		"alternate alone":   func(c *Config) { c.Normal = Look{Color: Blue, Pattern: Alternate} },
		"night time":        func(c *Config) { c.Night.From = "25:00" },
		"night show":        func(c *Config) { c.Night.Show = "dim" },
		"locate dark":       func(c *Config) { c.Locate.Color = Off },
		"locate long":       func(c *Config) { c.Locate.DurationS = 7200 },
		"external short":    func(c *Config) { c.External.MaxDurationS = 5 },
		"warning id":        func(c *Config) { c.WarningsOff = []string{"Bad Id"} },
	}
	for name, mutate := range bad {
		c := Defaults()
		mutate(&c)
		if _, err := Validate(c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a row the list lacks goes to its default place; off is solid; a second colour only for alternate
	c := Defaults()
	c.States = slices.DeleteFunc(slices.Clone(c.States), func(s StateConfig) bool { return s.ID == StateStatusWarning || s.ID == StateRadioDown })
	c.States[0], c.States[1] = c.States[1], c.States[0] // no-network below service-failed
	c.States = append(c.States, StateConfig{ID: StateExternal})
	c.States = slices.DeleteFunc(c.States, func(s StateConfig) bool { return s.ID == StateExternal && s.Enabled })
	c.Normal = Look{Color: Off, Pattern: Fast, Color2: Red}
	c.States[0].Color2 = Blue
	v, err := Validate(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{StateRadioDown, StateServiceFailed, StateNoNetwork, StateStorageReplace, StateInterfacesStarting, StateRadioDutyCycle, StateRadioCarrierSense, StateSystemUpdate, StateAddonUpdate, StateNoInternet, StateExternal, StateStatusWarning}
	if got := statesOrder(v); !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
	if v.Normal != LookDark || v.States[1].Color2 != "" {
		t.Errorf("normalised: %+v %+v", v.Normal, v.States[1])
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func at(hhmm string) time.Time {
	tm, _ := time.ParseInLocation("2006-01-02 15:04", "2026-09-12 "+hhmm, time.UTC)
	return tm
}

func TestNightActive(t *testing.T) {
	across := Night{Enabled: true, From: "22:00", To: "06:30"}
	day := Night{Enabled: true, From: "12:00", To: "14:00"}
	for _, c := range []struct {
		n    Night
		at   string
		want bool
	}{
		{across, "21:59", false}, {across, "22:00", true}, {across, "03:00", true}, {across, "06:29", true}, {across, "06:30", false},
		{day, "11:59", false}, {day, "12:00", true}, {day, "14:00", false},
		{Night{Enabled: false, From: "00:00", To: "23:59"}, "12:00", false},
		{Night{Enabled: true, From: "08:00", To: "08:00"}, "08:00", false},
	} {
		if got := NightActive(c.n, at(c.at), time.UTC); got != c.want {
			t.Errorf("%+v at %s: %v", c.n, c.at, got)
		}
	}
	// the window is the box's time zone: 23:00 in Berlin is 21:00 UTC in summer
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no zoneinfo")
	}
	if !NightActive(across, at("21:00"), berlin) || NightActive(across, at("21:00"), time.UTC) {
		t.Error("the night window ignores the time zone")
	}
	// DST: on 2026-10-25 Berlin goes back an hour; 05:30 local is still night, 06:30 local is not
	autumn := time.Date(2026, 10, 25, 5, 30, 0, 0, berlin)
	if !NightActive(across, autumn, berlin) || NightActive(across, autumn.Add(time.Hour), berlin) {
		t.Error("the DST change moved the window")
	}
}

func TestResolve(t *testing.T) {
	now := at("12:00")
	cfg := Defaults()
	base := Inputs{Now: now, Location: time.UTC, Config: cfg, Active: map[string]Activity{}}
	with := func(mod func(in *Inputs)) Shown {
		in := base
		in.Active = map[string]Activity{}
		mod(&in)
		return Resolve(in)
	}
	until := now.Add(time.Minute)
	if s := with(func(in *Inputs) {}); s.Source != SourceNormal || s.Look != cfg.Normal {
		t.Errorf("nothing active: %+v", s)
	}
	if s := with(func(in *Inputs) { in.Shutdown, in.Booting = true, true }); s.ID != StateShutdown || s.Look != LookShutdown {
		t.Errorf("shutdown: %+v", s)
	}
	if s := with(func(in *Inputs) {
		in.Booting = true
		in.Active[StateRadioDown] = Activity{}
	}); s.ID != StateBooting || s.Look != LookBooting {
		t.Errorf("booting beats every state: %+v", s)
	}
	if s := with(func(in *Inputs) {
		in.Preview = &Timed{Look: Look{Color: Green, Pattern: Fast}, Until: until}
		in.Locate = &Timed{Look: Look{Color: White, Pattern: Fast}, Until: until}
	}); s.Source != SourcePreview {
		t.Errorf("preview before locate: %+v", s)
	}
	if s := with(func(in *Inputs) {
		in.Locate = &Timed{Look: Look{Color: White, Pattern: Fast}, Until: until}
		in.Active[StateRadioDown] = Activity{}
	}); s.Source != SourceLocate {
		t.Errorf("locate before radio-down: %+v", s)
	}
	if s := with(func(in *Inputs) { in.Locate = &Timed{Look: Look{Color: White, Pattern: Fast}, Until: now} }); s.Source != SourceNormal {
		t.Errorf("an expired locate: %+v", s)
	}
	if s := with(func(in *Inputs) {
		in.Active[StateStatusWarning] = Activity{Detail: "certificate"}
		in.Active[StateNoNetwork] = Activity{}
	}); s.ID != StateNoNetwork || s.Look != (Look{Color: Yellow, Pattern: Fast}) {
		t.Errorf("the first active row wins: %+v", s)
	}
	if s := with(func(in *Inputs) { in.Active[StateNoInternet] = Activity{} }); s.Source != SourceNormal {
		t.Errorf("a disabled row is skipped: %+v", s)
	}
	if s := with(func(in *Inputs) {
		in.Config.Enabled = false
		in.Active[StateRadioDown] = Activity{}
	}); s.Source != SourceOff || s.Look != LookDark {
		t.Errorf("switched off: %+v", s)
	}
	if s := with(func(in *Inputs) {
		in.Config.Enabled = false
		in.Locate = &Timed{Look: Look{Color: White, Pattern: Fast}, Until: until}
	}); s.Source != SourceLocate {
		t.Errorf("locate works on a switched-off LED: %+v", s)
	}
	// overrides at the external row: below storage-replace, above status-warning by default
	green := Override{ID: "flow", Color: Green, Pattern: Solid, Priority: "normal", Night: "respect", Since: now}
	if s := with(func(in *Inputs) {
		in.Overrides = []Override{green}
		in.Active[StateStatusWarning] = Activity{}
	}); s.Source != SourceOverride || s.ID != "flow" {
		t.Errorf("override above status-warning: %+v", s)
	}
	if s := with(func(in *Inputs) {
		in.Overrides = []Override{green}
		in.Active[StateServiceFailed] = Activity{}
	}); s.ID != StateServiceFailed {
		t.Errorf("service-failed above the override: %+v", s)
	}
	// D-65: the row's position is configurable - above the errors too
	if s := with(func(in *Inputs) {
		in.Config.States = append([]StateConfig{{ID: StateExternal, Enabled: true}}, slices.DeleteFunc(slices.Clone(in.Config.States), func(s StateConfig) bool { return s.ID == StateExternal })...)
		in.Overrides = []Override{green}
		in.Active[StateRadioDown] = Activity{}
	}); s.Source != SourceOverride {
		t.Errorf("an external row moved to the top: %+v", s)
	}
}

func TestResolveNightAndOverrides(t *testing.T) {
	night := at("23:00")
	cfg := Defaults()
	cfg.Night.Enabled = true
	in := Inputs{Now: night, Location: time.UTC, Config: cfg, Active: map[string]Activity{StateStatusWarning: {}}}
	if s := Resolve(in); s.Source != SourceNight || s.Look != LookDark {
		t.Errorf("a warning at night: %+v", s)
	}
	in.Active[StateServiceFailed] = Activity{}
	if s := Resolve(in); s.ID != StateServiceFailed {
		t.Errorf("an error at night, show errors: %+v", s)
	}
	in.Config.Night.Show = "off"
	if s := Resolve(in); s.Source != SourceNight {
		t.Errorf("an error at night, show off: %+v", s)
	}
	in.Active = map[string]Activity{}
	later := night.Add(time.Hour)
	in.Overrides = []Override{
		{ID: "a", Color: Green, Pattern: Solid, Priority: "normal", Night: "respect", Since: night},
		{ID: "b", Color: Red, Pattern: Solid, Priority: "low", Night: "ignore", Since: night, Until: &later},
	}
	if s := Resolve(in); s.ID != "b" {
		t.Errorf("only the override that ignores the night: %+v", s)
	}
	in.Config.Night.Enabled = false
	if s := Resolve(in); s.ID != "a" {
		t.Errorf("normal beats low: %+v", s)
	}
	in.Overrides = append(in.Overrides, Override{ID: "c", Color: Blue, Pattern: Fast, Priority: "normal", Since: night.Add(time.Second)})
	if s := Resolve(in); s.ID != "c" {
		t.Errorf("the newest within a level: %+v", s)
	}
	in.Now = later
	in.Overrides = in.Overrides[1:2]
	if s := Resolve(in); s.Source != SourceNormal {
		t.Errorf("an expired override: %+v", s)
	}
}

// the on/off LED's double strings, as before task 315
var doublePattern, doubleCounter = doubleOf(1), doubleCounterOf(1)

func TestFrame(t *testing.T) {
	r, g, b := ChannelLEDs[0], ChannelLEDs[1], ChannelLEDs[2]
	for _, c := range []struct {
		name    string
		look    Look
		pattern bool
		want    []priv.LEDWrite
	}{
		{"booting", LookBooting, true, []priv.LEDWrite{{LED: r, Trigger: "default-on"}, {LED: g, Trigger: "default-on"}, {LED: b, Trigger: "none"}}},
		{"dark", LookDark, true, []priv.LEDWrite{{LED: r, Trigger: "none"}, {LED: g, Trigger: "none"}, {LED: b, Trigger: "none"}}},
		{"red fast", Look{Color: Red, Pattern: Fast}, true, []priv.LEDWrite{{LED: r, Trigger: "timer", DelayOn: 100, DelayOff: 100}, {LED: g, Trigger: "none"}, {LED: b, Trigger: "none"}}},
		{"white slow", Look{Color: White, Pattern: Slow}, true, []priv.LEDWrite{{LED: r, Trigger: "timer", DelayOn: 500, DelayOff: 500}, {LED: g, Trigger: "timer", DelayOn: 500, DelayOff: 500}, {LED: b, Trigger: "timer", DelayOn: 500, DelayOff: 500}}},
		{"cyan flash", Look{Color: Cyan, Pattern: Flash}, true, []priv.LEDWrite{{LED: g, Trigger: "timer", DelayOn: 100, DelayOff: 1900}, {LED: b, Trigger: "timer", DelayOn: 100, DelayOff: 1900}, {LED: r, Trigger: "none"}}},
		{"red double", Look{Color: Red, Pattern: Double}, true, []priv.LEDWrite{{LED: r, Trigger: "pattern", Pattern: doublePattern}, {LED: g, Trigger: "none"}, {LED: b, Trigger: "none"}}},
		{"red double, no pattern trigger", Look{Color: Red, Pattern: Double}, false, []priv.LEDWrite{{LED: r, Trigger: "timer", DelayOn: 100, DelayOff: 1900}, {LED: g, Trigger: "none"}, {LED: b, Trigger: "none"}}},
		{"yellow and blue", Look{Color: Yellow, Pattern: Alternate, Color2: Blue}, true, []priv.LEDWrite{{LED: r, Trigger: "timer", DelayOn: 500, DelayOff: 500}, {LED: g, Trigger: "timer", DelayOn: 500, DelayOff: 500}, {LED: b, Trigger: "timer", DelayOn: 500, DelayOff: 500, StartAfterMS: 500}}},
		{"blue and red", Look{Color: Blue, Pattern: Alternate, Color2: Red}, true, []priv.LEDWrite{{LED: b, Trigger: "timer", DelayOn: 500, DelayOff: 500}, {LED: r, Trigger: "timer", DelayOn: 500, DelayOff: 500, StartAfterMS: 500}, {LED: g, Trigger: "none"}}},
	} {
		got := Frame(c.look, "", c.pattern)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
		if err := priv.ValidLEDFrame(got); err != nil {
			t.Errorf("%s: the helper would refuse it: %v", c.name, err)
		}
	}
	// every look the configuration can hold is a frame the helper accepts
	for _, col := range Colors {
		for _, p := range Patterns {
			l, err := NormalizeLook(Look{Color: col, Pattern: p, Color2: map[bool]string{true: Blue, false: Red}[col == Red || col == Yellow || col == Magenta || col == White]})
			if err != nil {
				continue
			}
			if err := priv.ValidLEDFrame(Frame(l, "", true)); err != nil {
				t.Errorf("%+v: %v", l, err)
			}
		}
	}
}

// box writes a fake root: /var/hm_mode, the LED devices and whatever else a test names.
type box map[string]string

func (b box) root(t *testing.T) system.Root {
	t.Helper()
	dir := t.TempDir()
	for p, v := range b {
		full := filepath.Join(dir, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		// "-> target": a symbolic link, as sysfs has them (an LED's device, a device's driver)
		if target, ok := strings.CutPrefix(v, "-> "); ok {
			if err := os.Symlink(target, full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return system.Root(dir)
}

// ledDevice gives the three channel LEDs a parent device bound by driver (openccu-lite task 326):
// leds_pwm's rpi_rf_mod_leds on the header since task 315, leds-gpio's leds before. Without it the
// LEDs are the adapter's (rpi_rf_mod_led registers them without a parent).
func ledDevice(b box, driver, device string) box {
	b["sys/bus/platform/drivers/"+driver+"/"] = ""
	b["sys/devices/platform/"+device+"/driver"] = "-> ../../../bus/platform/drivers/" + driver
	for _, l := range ChannelLEDs {
		b["sys/class/leds/"+l+"/device"] = "-> ../../../devices/platform/" + device
	}
	return b
}

func withLEDs(b box, names ...string) box {
	for _, n := range names {
		for _, a := range []string{"trigger", "brightness", "delay_on", "delay_off", "pattern"} {
			v := ""
			if a == "trigger" {
				v = "[none] default-on timer heartbeat"
			}
			b["sys/class/leds/"+n+"/"+a] = v
		}
	}
	return b
}

func charly() box {
	return ledDevice(withLEDs(box{
		"var/hm_mode":               "HM_HOST='rpi3'\nHM_MODE='NORMAL'\nHM_HMIP_DEV='RPI-RF-MOD'\nHM_HMIP_DEVTYPE='GPIO@3f201000'\nHM_HMRF_DEV='RPI-RF-MOD'\nHM_LED_RED='/sys/class/leds/PWR'\nHM_LED_RED_MODE2='mmc0'\n",
		"proc/sys/kernel/osrelease": "6.18.34\n",
		"lib/modules/6.18.34/kernel/drivers/leds/trigger/ledtrig-pattern.ko.xz": "",
		"var/status/hasLink": "",
		"var/status/hasIP":   "",
	}, ChannelLEDs[0], ChannelLEDs[1], ChannelLEDs[2], PWRLED), "leds-gpio", "leds")
}

func pi4() box {
	return withLEDs(box{
		"var/hm_mode":        "HM_HOST='rpi4'\nHM_MODE='NORMAL'\nHM_HMIP_DEV='HMIP-RFUSB'\nHM_HMRF_DEV='HMIP-RFUSB'\nHM_LED_RED='/sys/class/leds/PWR'\nHM_LED_RED_MODE2='default-on'\n",
		"var/status/hasLink": "",
		"var/status/hasIP":   "",
	}, ChannelLEDs[0], ChannelLEDs[1], ChannelLEDs[2], PWRLED, "ACT")
}

func TestProbe(t *testing.T) {
	hw := Probe(charly().root(t), nil)
	if !hw.Available || hw.Kind != "rpi-rf-mod" || !hw.PatternTrigger || !hw.Final || hw.PWR || !slices.Contains(hw.Patterns, Double) {
		t.Errorf("charly: %+v", hw)
	}
	noModule := charly()
	delete(noModule, "lib/modules/6.18.34/kernel/drivers/leds/trigger/ledtrig-pattern.ko.xz")
	if hw := Probe(noModule.root(t), nil); !hw.Available || hw.PatternTrigger || slices.Contains(hw.Patterns, Double) {
		t.Errorf("without the pattern trigger: %+v", hw)
	}
	// openccu-lite B-299: the daemon's unit hides /lib/modules (ProtectKernelModules=), so the
	// module file is not the probe's to find - the helper's loader decides
	if os.Geteuid() != 0 {
		hidden := charly()
		delete(hidden, "lib/modules/6.18.34/kernel/drivers/leds/trigger/ledtrig-pattern.ko.xz")
		root := hidden.root(t)
		modules := filepath.Join(string(root), "lib/modules")
		if err := os.MkdirAll(modules, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(modules, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(modules, 0o755) })
		var asked []string
		load := func(name string) error { asked = append(asked, name); return nil }
		if hw := Probe(root, load); !hw.Available || !hw.PatternTrigger || !slices.Contains(hw.Patterns, Double) || !slices.Equal(asked, []string{PatternModule}) {
			t.Errorf("with the helper loading the module: %+v asked %v", hw, asked)
		}
		failing := func(string) error { return errors.New("modprobe: not found") }
		if hw := Probe(root, failing); !hw.Available || hw.PatternTrigger || slices.Contains(hw.Patterns, Double) {
			t.Errorf("with a module that cannot be loaded: %+v", hw)
		}
		if hw := Probe(root, nil); hw.PatternTrigger {
			t.Errorf("without a loader the hidden module directory says no: %+v", hw)
		}
		// loaded already: /sys/module says so, nothing is asked
		loadedBox := charly()
		delete(loadedBox, "lib/modules/6.18.34/kernel/drivers/leds/trigger/ledtrig-pattern.ko.xz")
		loadedBox["sys/module/ledtrig_pattern/refcnt"] = "0\n"
		asked = nil
		if hw := Probe(loadedBox.root(t), load); !hw.PatternTrigger || len(asked) != 0 {
			t.Errorf("module loaded already: %+v asked %v", hw, asked)
		}
	}
	// the phantom rpi_rf_mod devices of the overlay, an HmIP-RFUSB as the radio
	if hw := Probe(pi4().root(t), nil); hw.Available || hw.Reason != ReasonNoModule || !hw.PWR || hw.PWRNormal != "default-on" {
		t.Errorf("pi 4: %+v", hw)
	}
	if hw := Probe(box{"var/hm_mode": "HM_HOST='ova'\nHM_MODE='NORMAL'\nHM_HMIP_DEV='HMIP-RFUSB'\n"}.root(t), nil); hw.Available || hw.Reason != ReasonVirtual || hw.PWR {
		t.Errorf("ova: %+v", hw)
	}
	lgw := charly()
	lgw["var/hm_mode"] = strings.Replace(lgw["var/hm_mode"], "NORMAL", "HM-LGW", 1)
	if hw := Probe(lgw.root(t), nil); hw.Available || !hw.StandDown || hw.Reason != ReasonHMLGW {
		t.Errorf("hm-lgw: %+v", hw)
	}
	early := charly()
	early["var/hm_mode"] = "HM_HOST='rpi3'\nHM_RTC='rx8130'\n"
	if hw := Probe(early.root(t), nil); hw.Available || hw.Final || hw.Reason != ReasonDetecting {
		t.Errorf("before the detection: %+v", hw)
	}
	lxc := charly()
	lxc["run/systemd/container"] = "lxc\n"
	if hw := Probe(lxc.root(t), nil); hw.Available || hw.Reason != ReasonContainer {
		t.Errorf("container: %+v", hw)
	}
}

// rig is a controller on a fake root, with a clock the test moves and the helper's own write into
// the fake tree.
type rig struct {
	t      *testing.T
	root   system.Root
	c      *Controller
	clock  time.Time
	mu     sync.Mutex
	units  []system.UnitRow
	frames [][]priv.LEDWrite
	fail   error
	warns  []warnings.Warning
}

func newRig(t *testing.T, b box) *rig {
	r := &rig{t: t, root: b.root(t), clock: at("12:00")}
	r.units = []system.UnitRow{
		{Unit: "multimacd.service", Load: "loaded", Active: "active", Sub: "running"},
		{Unit: "rfd.service", Load: "loaded", Active: "active", Sub: "running"},
		{Unit: "hmipserver.service", Load: "loaded", Active: "active", Sub: "running"},
		{Unit: "lighttpd.service", Load: "loaded", Active: "active", Sub: "running"},
	}
	r.c = &Controller{
		Root: r.root,
		File: filepath.Join(t.TempDir(), "led.json"),
		Now:  func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.clock },
		// the helper's module loading (openccu-lite B-299): the rig's kernel has the pattern trigger
		LoadModule: func(string) error { return nil },
		Write: func(frame []priv.LEDWrite) error {
			r.mu.Lock()
			r.frames = append(r.frames, frame)
			fail := r.fail
			r.mu.Unlock()
			if fail != nil {
				return fail
			}
			return priv.Local{}.WriteLEDs(filepath.Join(string(r.root), "sys/class/leds"), frame)
		},
		Src: Sources{
			Units: func(context.Context) ([]system.UnitRow, error) {
				r.mu.Lock()
				defer r.mu.Unlock()
				return slices.Clone(r.units), nil
			},
			Warnings: func() []warnings.Warning { r.mu.Lock(); defer r.mu.Unlock(); return slices.Clone(r.warns) },
		},
	}
	return r
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.clock = r.clock.Add(d)
	r.mu.Unlock()
}

// steps moves the clock in ten-second steps and steps the controller after each.
func (r *rig) steps(n int) {
	for range n {
		r.advance(10 * time.Second)
		r.c.step(context.Background())
	}
}

func (r *rig) lastFrame() []priv.LEDWrite {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.frames) == 0 {
		return nil
	}
	return r.frames[len(r.frames)-1]
}

func (r *rig) frameCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

func (r *rig) setUnit(unit, active, sub string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.units {
		if r.units[i].Unit == unit {
			r.units[i].Active, r.units[i].Sub = active, sub
			return
		}
	}
	r.units = append(r.units, system.UnitRow{Unit: unit, Load: "loaded", Active: active, Sub: sub})
}

func (r *rig) file(p string, present bool) {
	full := filepath.Join(string(r.root), p)
	if present {
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			r.t.Fatal(err)
		}
		return
	}
	_ = os.Remove(full)
}

func (r *rig) expectShown(look Look, id string) {
	r.t.Helper()
	r.expectShownOver(look, "", id)
}

// expectShownOver is expectShown for a look that blinks over a background (task 134).
func (r *rig) expectShownOver(look Look, background, id string) {
	r.t.Helper()
	if want := Frame(look, background, true); !slices.Equal(r.lastFrame(), want) {
		r.t.Fatalf("frame %+v, want %+v (%s)", r.lastFrame(), want, id)
	}
	if s := r.c.State(); s.Shown.ID != id || s.Shown.Background != background {
		r.t.Fatalf("shown %+v, want %s over %q", s.Shown, id, background)
	}
}

// task 95, decided in D-67: the no-internet check connects to hosts the box talks to anyway - the
// addon catalogue index's, and the ACME directory's when ACME is configured - not to google.com.
func TestInternetTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		urls []string
		want []string
	}{
		{"the catalogue index, the bundled copy left out", []string{"https://catalogue.example.org/addons/raw/index.json", "file:///etc/occulite/catalog.json"}, []string{"catalogue.example.org:443"}},
		{"a port and plain http", []string{"http://10.0.0.5:3000/index.json"}, []string{"10.0.0.5:3000"}},
		{"http without a port", []string{"http://mirror.example.net/index.json"}, []string{"mirror.example.net:80"}},
		{"the ACME directory after the catalogue", []string{"https://catalogue.example.org/index.json", "https://acme-v02.api.letsencrypt.org/directory"}, []string{"catalogue.example.org:443", "acme-v02.api.letsencrypt.org:443"}},
		{"one host once", []string{"https://catalogue.example.org/a.json", "HTTPS://Catalogue.Example.org/b.json"}, []string{"catalogue.example.org:443"}},
		{"an IPv6 address", []string{"https://[2001:db8::1]:8443/index.json"}, []string{"[2001:db8::1]:8443"}},
		{"nothing to connect to", []string{"file:///etc/occulite/catalog.json", "", "not a url", "ftp://files.example.org/x", "https:///index.json"}, nil},
		{"none at all", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := InternetTargets(tc.urls...); !slices.Equal(got, tc.want) {
				t.Errorf("%v, want %v", got, tc.want)
			}
		})
	}
}

func TestControllerInternetCheck(t *testing.T) {
	both := []string{"catalogue.example.org:443", "acme.example.org:443"}
	for _, tc := range []struct {
		name     string
		targets  []string
		reach    string
		disabled bool
		want     bool
		wantDial []string
	}{
		{name: "the catalogue answers", targets: both, reach: "catalogue.example.org:443", want: true, wantDial: both[:1]},
		{name: "only the ACME directory answers", targets: both, reach: "acme.example.org:443", want: true, wantDial: both},
		{name: "neither answers", targets: both, want: false, wantDial: both},
		{name: "no host to check is not no internet", want: true},
		{name: "switched off by the file", targets: both, disabled: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, charly())
			var mu sync.Mutex
			var dials []string
			r.c.Dial = func(_ context.Context, network, addr string) (net.Conn, error) {
				mu.Lock()
				dials = append(dials, addr)
				mu.Unlock()
				if network != "tcp" {
					t.Errorf("network %q", network)
				}
				if addr == tc.reach {
					a, b := net.Pipe()
					b.Close()
					return a, nil
				}
				return nil, errors.New("connection refused")
			}
			targets := tc.targets
			r.c.Src.InternetTargets = func() []string { return targets }
			r.file("etc/config/internetCheckDisabled", tc.disabled)
			r.c.checkInternet(context.Background())
			r.c.mu.Lock()
			got, checkedAt := r.c.internet, r.c.internetAt
			r.c.mu.Unlock()
			if got != tc.want || checkedAt.IsZero() {
				t.Errorf("internet %v at %v, want %v", got, checkedAt, tc.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(dials, tc.wantDial) {
				t.Errorf("dialled %v, want %v", dials, tc.wantDial)
			}
		})
	}
	// the page names the hosts the check uses
	r := newRig(t, charly())
	r.c.Src.InternetTargets = func() []string { return []string{"catalogue.example.org:443", "[2001:db8::1]:8443"} }
	if got := r.c.View().InternetHosts; !slices.Equal(got, []string{"catalogue.example.org", "2001:db8::1"}) {
		t.Errorf("hosts %v", got)
	}
	r.c.Src.InternetTargets = nil
	if got := r.c.View().InternetHosts; got == nil || len(got) != 0 {
		t.Errorf("no hosts: %#v", got)
	}
	if b, _ := json.Marshal(r.c.View()); !strings.Contains(string(b), `"internet_hosts":[]`) {
		t.Errorf("the view: %s", b)
	}
}

// startingLook is interfaces-starting's default: a double magenta blink over the normal colour.
var startingLook = Look{Color: Magenta, Pattern: Double, OverNormal: true}

// coldRig is a box early in the boot: the interface units not started yet, the radio run not
// finished.
func coldRig(t *testing.T) *rig {
	r := newRig(t, charly())
	for _, u := range []string{"multimacd", "rfd", "hmipserver"} {
		r.setUnit(u+".service", "inactive", "dead")
	}
	return r
}

// radioRun is the radio run finishing with these daemons in its plan.
func (r *rig) radioRun(units ...string) {
	for _, u := range units {
		r.file("run/occulite/radio/"+u+".enabled", true)
	}
	r.file("run/occulite/radio/render.json", true)
}

func TestControllerBootStatesShutdown(t *testing.T) {
	r := coldRig(t)
	r.c.step(context.Background())
	r.expectShown(LookBooting, StateBooting)
	// the run's lock makes the directory before the detection: still booting
	r.file("run/occulite/radio/.lock", true)
	r.steps(3)
	r.expectShown(LookBooting, StateBooting)
	// task 158: the radio is known - booting ends without startupFinished, and the interfaces
	// starting show as a double magenta blink over blue
	r.radioRun("multimacd", "rfd", "hmipserver")
	r.steps(1)
	r.expectShownOver(startingLook, Blue, StateInterfacesStarting)
	// the units start one after another; one that waits for its turn is starting, not down,
	// however long it waits
	r.setUnit("multimacd.service", "active", "running")
	r.setUnit("rfd.service", "activating", "start")
	r.steps(3)
	r.expectShownOver(startingLook, Blue, StateInterfacesStarting)
	if st := r.c.State(); st.Shown.Detail != "rfd, hmipserver" {
		t.Errorf("detail %q", st.Shown.Detail)
	}
	r.setUnit("hmipserver.service", "activating", "start")
	// hmipserver takes a minute: past the error hold it is still starting, never red
	r.setUnit("rfd.service", "active", "running")
	r.steps(6)
	r.expectShownOver(startingLook, Blue, StateInterfacesStarting)
	if st := r.c.State(); st.Shown.Detail != "hmipserver" {
		t.Errorf("detail %q", st.Shown.Detail)
	}
	r.setUnit("hmipserver.service", "active", "running")
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// the addons are still starting: no shutdown before startupFinished was ever there
	r.steps(3)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	r.file("var/status/startupFinished", true)
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)

	// a failed rfd shows after the hold, not at once
	r.setUnit("rfd.service", "failed", "failed")
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	r.steps(2)
	r.expectShown(Look{Color: Red, Pattern: Solid}, StateRadioDown)
	st := r.c.State()
	if len(st.Active) != 1 || st.Active[0].Detail != "rfd" {
		t.Errorf("active: %+v", st.Active)
	}
	// a restart shorter than the hold never shows
	r.setUnit("rfd.service", "active", "running")
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	r.setUnit("rfd.service", "activating", "start")
	r.steps(1)
	r.setUnit("rfd.service", "active", "running")
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// a masked unit is switched off, not down; a failed lighttpd is service-failed; addons only on request
	r.mu.Lock()
	r.units[1].Load = "masked"
	r.units[1].Active = "inactive"
	r.mu.Unlock()
	r.setUnit("lighttpd.service", "failed", "failed")
	r.setUnit("addon-redmatic.service", "failed", "failed")
	r.steps(3)
	r.expectShown(Look{Color: Red, Pattern: Slow}, StateServiceFailed)
	if st := r.c.State(); st.Shown.Detail != "lighttpd" {
		t.Errorf("service-failed detail %q", st.Shown.Detail)
	}
	r.setUnit("lighttpd.service", "active", "running")
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)

	// openccu-lite B-276: a failed transient unit - systemd-run's run-<id> ones, and a named one
	// whose unit file is in /run/systemd/transient - is not service-failed; a real one still is
	r.setUnit("run-p2492-i2493.service", "failed", "failed")
	r.setUnit("run-r0123.scope", "failed", "failed")
	r.setUnit("b276-test.service", "failed", "failed")
	r.file("run/systemd/transient/b276-test.service", true)
	r.steps(3)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	r.setUnit("runner.service", "failed", "failed")
	r.steps(3)
	r.expectShown(Look{Color: Red, Pattern: Slow}, StateServiceFailed)
	if st := r.c.State(); st.Shown.Detail != "runner" {
		t.Errorf("service-failed detail with transient units %q", st.Shown.Detail)
	}
	r.setUnit("runner.service", "active", "running")
	r.file("run/systemd/transient/b276-test.service", false)
	r.steps(3)
	r.expectShown(Look{Color: Red, Pattern: Slow}, StateServiceFailed)
	if st := r.c.State(); st.Shown.Detail != "b276-test" {
		t.Errorf("a named unit without its transient file %q", st.Shown.Detail)
	}
	r.mu.Lock()
	r.units = slices.DeleteFunc(r.units, func(u system.UnitRow) bool {
		return u.Unit == "run-p2492-i2493.service" || u.Unit == "run-r0123.scope" || u.Unit == "b276-test.service" || u.Unit == "runner.service"
	})
	r.mu.Unlock()
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)

	// openccu-lite task 283: units in a crash loop are service-failed, beside a failed one, and
	// not a status warning; the state goes with the warning
	r.mu.Lock()
	r.warns = []warnings.Warning{{ID: "crash-loop", Variant: "addon-mosquitto,sshd", Severity: warnings.SeverityError}}
	r.mu.Unlock()
	r.setUnit("lighttpd.service", "failed", "failed")
	r.steps(3)
	r.expectShown(Look{Color: Red, Pattern: Slow}, StateServiceFailed)
	if st := r.c.State(); st.Shown.Detail != "addon-mosquitto, lighttpd, sshd" {
		t.Errorf("crash-loop detail %q", st.Shown.Detail)
	}
	r.setUnit("lighttpd.service", "active", "running")
	r.steps(1)
	if st := r.c.State(); st.Shown.ID != StateServiceFailed || st.Shown.Detail != "addon-mosquitto, sshd" {
		t.Errorf("the loop alone: %+v", st.Shown)
	}
	r.mu.Lock()
	r.warns = nil
	r.mu.Unlock()
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)

	// no network: hasIP gone
	r.file("var/status/hasIP", false)
	r.steps(3)
	r.expectShown(Look{Color: Yellow, Pattern: Fast}, StateNoNetwork)
	r.file("var/status/hasIP", true)
	r.steps(1)

	// the Status page's warnings: unsilenced ones, a storage replace is its own state
	r.mu.Lock()
	r.warns = []warnings.Warning{{ID: "certificate", Variant: "expiring"}, {ID: "storage", Variant: "replace"}}
	r.mu.Unlock()
	r.steps(1)
	r.expectShown(Look{Color: Red, Pattern: Double}, StateStorageReplace)
	r.mu.Lock()
	r.warns = r.warns[:1]
	r.mu.Unlock()
	r.steps(1)
	r.expectShown(Look{Color: Yellow, Pattern: Slow}, StateStatusWarning)
	cfg := r.c.View().Config
	cfg.WarningsOff = []string{"certificate"}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)

	// the shutdown: S99SetupLEDs' stop removes startupFinished
	r.file("var/status/startupFinished", false)
	r.c.step(context.Background())
	r.expectShown(LookShutdown, StateShutdown)
	// and back (a restart of that unit by hand)
	r.file("var/status/startupFinished", true)
	r.advance(time.Second)
	r.c.step(context.Background())
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// occulited stopping
	r.c.Stop()
	r.expectShown(LookShutdown, StateShutdown)
}

func upRig(t *testing.T, b box) *rig {
	r := newRig(t, b)
	r.file("var/status/startupFinished", true)
	r.c.step(context.Background())
	return r
}

func dur(n int) *int { return &n }

func TestControllerOverrides(t *testing.T) {
	r := upRig(t, charly())
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	o, err := r.c.SetOverride("token:flow", false, OverrideRequest{Color: Green})
	if err != nil || o.ID != "token:flow" || o.Pattern != Solid || o.Until == nil || o.Until.Sub(r.clock) != 5*time.Minute {
		t.Fatalf("override: %+v %v", o, err)
	}
	r.c.step(context.Background())
	r.expectShown(Look{Color: Green, Pattern: Solid}, "token:flow")
	// two requests per second per caller
	if _, err := r.c.SetOverride("token:flow", false, OverrideRequest{Color: Red}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.SetOverride("token:flow", false, OverrideRequest{Color: Red}); !errors.Is(err, ErrRateLimited) {
		t.Errorf("third request in a second: %v", err)
	}
	r.advance(time.Second)
	// another caller cannot take or clear the id; an administrator can
	if _, err := r.c.SetOverride("token:other", false, OverrideRequest{ID: "token:flow", Color: Blue}); !errors.Is(err, ErrForbidden) {
		t.Errorf("another caller's id: %v", err)
	}
	if err := r.c.ClearOverride("token:other", "token:flow", false); !errors.Is(err, ErrForbidden) {
		t.Errorf("another caller's clear: %v", err)
	}
	r.advance(time.Second) // refusals count against the rate as well
	if err := r.c.ClearOverride("token:other", "", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("clearing nothing: %v", err)
	}
	// capped, until cleared, and its switch
	if o, err := r.c.SetOverride("admin", true, OverrideRequest{Color: Magenta, DurationS: dur(99999)}); err != nil || o.Until.Sub(r.clock) != time.Hour {
		t.Errorf("capped: %+v %v", o, err)
	}
	if o, err := r.c.SetOverride("token:node", false, OverrideRequest{Color: Red, DurationS: dur(0), Priority: "high"}); err != nil || o.Until != nil {
		t.Errorf("until cleared: %+v %v", o, err)
	}
	r.c.step(context.Background())
	r.expectShown(Look{Color: Red, Pattern: Solid}, "token:node")
	cfg := r.c.View().Config
	cfg.External.AllowUntilCleared = false
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.SetOverride("token:x", false, OverrideRequest{Color: Red, DurationS: dur(0)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("until cleared switched off: %v", err)
	}
	for _, bad := range []OverrideRequest{{Color: "pink"}, {Color: Red, Priority: "urgent"}, {Color: Red, Night: "maybe"}, {Color: Red, DurationS: dur(-1)}, {ID: "../x", Color: Red}} {
		r.advance(time.Second)
		if _, err := r.c.SetOverride("token:bad", false, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// at most 16
	for i := 0; ; i++ {
		r.advance(time.Second)
		_, err := r.c.SetOverride("admin", true, OverrideRequest{ID: "o" + strings.Repeat("x", i), Color: Blue})
		if errors.Is(err, ErrLimit) {
			if n := len(r.c.State().Overrides); n != maxOverrides {
				t.Errorf("limit at %d overrides", n)
			}
			break
		}
		if err != nil || i > maxOverrides {
			t.Fatalf("override %d: %v", i, err)
		}
	}
	if n := r.c.ClearOverrides(); n != maxOverrides {
		t.Errorf("cleared %d", n)
	}
	// expiry
	r.advance(time.Second)
	if _, err := r.c.SetOverride("token:short", false, OverrideRequest{Color: Green, DurationS: dur(20)}); err != nil {
		t.Fatal(err)
	}
	r.c.step(context.Background())
	r.expectShown(Look{Color: Green, Pattern: Solid}, "token:short")
	r.steps(3)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// locate and the preview
	if _, err := r.c.StartLocate(0); err != nil {
		t.Fatal(err)
	}
	r.c.step(context.Background())
	r.expectShown(Look{Color: White, Pattern: Fast}, StateLocate)
	if _, err := r.c.StartPreview(Look{Color: Yellow, Pattern: Alternate, Color2: Blue}); err != nil {
		t.Fatal(err)
	}
	r.c.step(context.Background())
	r.expectShown(Look{Color: Yellow, Pattern: Alternate, Color2: Blue}, StatePreview)
	r.steps(1)
	r.expectShown(Look{Color: White, Pattern: Fast}, StateLocate)
	if err := r.c.StopLocate(); err != nil {
		t.Fatal(err)
	}
	r.c.step(context.Background())
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	if _, err := r.c.StartLocate(4000); !errors.Is(err, ErrInvalid) {
		t.Errorf("a locate of 4000 s: %v", err)
	}
}

func TestControllerFramesPerSecond(t *testing.T) {
	r := upRig(t, charly())
	r.advance(time.Second) // the start's own frame is not in the second measured
	before := r.frameCount()
	for i := range 10 {
		if _, err := r.c.SetOverride("admin", true, OverrideRequest{ID: "flap", Color: []string{Red, Green}[i%2]}); err != nil && !errors.Is(err, ErrRateLimited) {
			t.Fatal(err)
		}
		r.c.mu.Lock()
		r.c.requests = map[string][]time.Time{} // the per-caller limit is not what this measures
		r.c.mu.Unlock()
		r.c.step(context.Background())
	}
	if n := r.frameCount() - before; n != framesPerSec {
		t.Errorf("%d frames in one second, want %d", n, framesPerSec)
	}
	r.advance(time.Second)
	r.c.step(context.Background())
	r.expectShown(Look{Color: Green, Pattern: Solid}, "flap")
}

func TestControllerReadBack(t *testing.T) {
	r := upRig(t, charly())
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	trigger := filepath.Join(string(r.root), "sys/class/leds", ChannelLEDs[0], "trigger")
	meddle := func() {
		if err := os.WriteFile(trigger, []byte("none [default-on] timer"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meddle()
	n := r.frameCount()
	r.steps(1) // read back: a difference
	r.c.step(context.Background())
	if r.frameCount() != n+1 || !r.c.State().Conflict {
		t.Fatalf("first difference: frames %d → %d, conflict %v", n, r.frameCount(), r.c.State().Conflict)
	}
	meddle()
	r.steps(1)
	r.c.step(context.Background())
	meddle()
	r.steps(1)
	r.c.step(context.Background())
	if r.frameCount() != n+2 || !r.c.State().Conflict {
		t.Fatalf("third difference within a minute: frames %d → %d", n, r.frameCount())
	}
	r.steps(3)
	if r.frameCount() != n+2 {
		t.Errorf("rewritten while held: %d", r.frameCount()-n)
	}
	// the controller's own change writes, and lets go of the hold
	if _, err := r.c.StartLocate(30); err != nil {
		t.Fatal(err)
	}
	r.c.step(context.Background())
	if r.frameCount() != n+3 || r.c.State().Conflict {
		t.Errorf("own change: frames %d, conflict %v", r.frameCount()-n, r.c.State().Conflict)
	}
}

func TestControllerPatternFallbackAndErrors(t *testing.T) {
	r := upRig(t, charly())
	cfg := r.c.View().Config
	cfg.Normal = Look{Color: Green, Pattern: Double}
	r.mu.Lock()
	r.fail = priv.ErrLEDPattern
	r.mu.Unlock()
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.c.step(context.Background())
	r.mu.Lock()
	r.fail = nil
	r.mu.Unlock()
	r.advance(time.Second)
	r.c.step(context.Background())
	if want := Frame(Look{Color: Green, Pattern: Flash}, "", true); !slices.Equal(r.lastFrame(), want) {
		t.Errorf("double without the trigger: %+v", r.lastFrame())
	}
	// a failing helper is retried after a while, not every second
	r.mu.Lock()
	r.fail = errors.New("privilege helper: connection refused")
	r.mu.Unlock()
	cfg.Normal = Look{Color: Blue, Pattern: Solid}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.advance(time.Second)
	r.c.step(context.Background())
	n := r.frameCount()
	for range 5 {
		r.advance(time.Second)
		r.c.step(context.Background())
	}
	if r.frameCount() != n || r.c.State().WriteError == "" {
		t.Errorf("retried every second: %d, error %q", r.frameCount()-n, r.c.State().WriteError)
	}
	r.mu.Lock()
	r.fail = nil
	r.mu.Unlock()
	r.steps(3)
	if r.c.State().WriteError != "" {
		t.Error("the error stays after a successful write")
	}
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
}

func TestControllerPWRErrorLight(t *testing.T) {
	r := upRig(t, pi4())
	if r.frameCount() != 0 {
		t.Fatalf("a box without an RGB LED was written: %+v", r.frames)
	}
	if _, err := r.c.SetOverride("admin", true, OverrideRequest{Color: Red}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("override without a LED: %v", err)
	}
	r.setUnit("rfd.service", "failed", "failed")
	r.steps(3)
	if r.frameCount() != 0 {
		t.Errorf("the power LED lit without its switch: %+v", r.frames)
	}
	cfg := r.c.View().Config
	cfg.PWRErrorLight = true
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	if want := []priv.LEDWrite{{LED: PWRLED, Trigger: "timer", DelayOn: 100, DelayOff: 100}}; !slices.Equal(r.lastFrame(), want) {
		t.Fatalf("error light: %+v", r.lastFrame())
	}
	if st := r.c.State(); st.PWR == nil || !st.PWR.Error || st.Available {
		t.Errorf("state: %+v", st)
	}
	r.setUnit("rfd.service", "active", "running")
	r.steps(1)
	if want := []priv.LEDWrite{{LED: PWRLED, Trigger: "default-on"}}; !slices.Equal(r.lastFrame(), want) {
		t.Errorf("back to normal: %+v", r.lastFrame())
	}
	for _, f := range r.frames {
		for _, w := range f {
			if strings.HasPrefix(w.LED, "rpi_rf_mod") {
				t.Errorf("a phantom LED was written: %+v", w)
			}
		}
	}
}

func TestControllerConfigFile(t *testing.T) {
	b := charly()
	b["etc/config/disableLED"] = ""
	r := upRig(t, b)
	if v := r.c.View(); v.Config.Enabled {
		t.Error("disableLED did not switch the LED off")
	}
	r.expectShown(LookDark, SourceOff)
	cfg := r.c.View().Config
	cfg.Enabled = true
	cfg.Normal = Look{Color: Green, Pattern: Flash}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(r.c.File); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("led.json: %v %v", st, err)
	}
	again := &Controller{Root: r.root, File: r.c.File}
	if v := again.View(); !v.Config.Enabled || v.Config.Normal != cfg.Normal {
		t.Errorf("read back: %+v", v.Config)
	}
	if _, err := r.c.SetConfig(Config{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty configuration: %v", err)
	}
	// a broken file is the defaults, logged
	if err := os.WriteFile(r.c.File, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := &Controller{Root: r.root, File: r.c.File}
	if v := broken.View(); !v.Config.Enabled || v.Config.Normal != Defaults().Normal {
		t.Errorf("broken file: %+v", v.Config)
	}
}

func TestControllerStandsDown(t *testing.T) {
	b := charly()
	b["var/hm_mode"] = strings.Replace(b["var/hm_mode"], "NORMAL", "HM-LGW", 1)
	r := upRig(t, b)
	r.steps(2)
	if r.frameCount() != 0 {
		t.Errorf("written in HM-LGW mode: %+v", r.frames)
	}
	r.c.Stop()
	if r.frameCount() != 0 {
		t.Errorf("shutdown written in HM-LGW mode: %+v", r.frames)
	}
}

// task 134: over_normal is kept for the four blinking patterns and dropped everywhere else; a
// led.json written before it loads unchanged.
func TestValidateOverNormal(t *testing.T) {
	for _, c := range []struct {
		look Look
		want bool
	}{
		{Look{Color: Yellow, Pattern: Slow, OverNormal: true}, true},
		{Look{Color: Yellow, Pattern: Fast, OverNormal: true}, true},
		{Look{Color: Cyan, Pattern: Flash, OverNormal: true}, true},
		{Look{Color: Red, Pattern: Double, OverNormal: true}, true},
		{Look{Color: Blue, Pattern: Slow, OverNormal: true}, true}, // the same colour as normal: kept, the resolver ignores it
		{Look{Color: Red, Pattern: Solid, OverNormal: true}, false},
		{Look{Color: Yellow, Pattern: Alternate, Color2: Blue, OverNormal: true}, false},
		{Look{Color: Off, Pattern: Slow, OverNormal: true}, false},
		{Look{Color: Yellow, Pattern: Slow}, false},
	} {
		l, err := NormalizeLook(c.look)
		if err != nil || l.OverNormal != c.want {
			t.Errorf("%+v: %+v %v", c.look, l, err)
		}
		cfg := Defaults()
		cfg.States[0].Color, cfg.States[0].Pattern, cfg.States[0].Color2, cfg.States[0].OverNormal = c.look.Color, c.look.Pattern, c.look.Color2, c.look.OverNormal
		v, err := Validate(cfg)
		if err != nil || v.States[0].OverNormal != c.want || v.States[0].Look().OverNormal != c.want {
			t.Errorf("row %+v: %+v %v", c.look, v.States[0], err)
		}
	}
	// normal is the background itself; the external row has no look; locate never blinks over it
	cfg := Defaults()
	cfg.Normal = Look{Color: Green, Pattern: Slow, OverNormal: true}
	i := slices.IndexFunc(cfg.States, func(s StateConfig) bool { return s.ID == StateExternal })
	cfg.States[i].OverNormal = true
	v, err := Validate(cfg)
	if err != nil || v.Normal.OverNormal || v.States[i].OverNormal {
		t.Errorf("normal and external: %+v %+v %v", v.Normal, v.States[i], err)
	}
	// the defaults use it for interfaces-starting alone (task 158), and the field is not written
	// when it is off
	for _, s := range Defaults().States {
		if s.OverNormal != (s.ID == StateInterfacesStarting) {
			t.Errorf("default %s over normal: %v", s.ID, s.OverNormal)
		}
	}
	if b := mustJSON(t, Defaults().States); strings.Count(b, "over_normal") != 1 {
		t.Errorf("over_normal written while off: %s", b)
	}
	// a led.json of task 95, before the field existed
	old := `{"enabled":true,"normal":{"color":"blue","pattern":"solid"},"night":{"enabled":false,"from":"22:00","to":"06:30","show":"errors"},
"states":[{"id":"radio-down","enabled":true,"color":"red","pattern":"solid"},{"id":"status-warning","enabled":true,"color":"yellow","pattern":"slow"}],
"warnings_off":[],"addon_units":false,"locate":{"color":"white","pattern":"fast","duration_s":300},"external":{"max_duration_s":3600,"allow_until_cleared":true}}`
	stored := Defaults()
	if err := json.Unmarshal([]byte(old), &stored); err != nil {
		t.Fatal(err)
	}
	v, err = Validate(stored)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.States {
		if s.OverNormal != (s.ID == StateInterfacesStarting) {
			t.Errorf("an old led.json: %s over normal: %v", s.ID, s.OverNormal)
		}
	}
	if w, _ := v.State(StateStatusWarning); w.Look() != (Look{Color: Yellow, Pattern: Slow}) {
		t.Errorf("an old led.json: %+v", w)
	}
}

func TestResolveOverNormal(t *testing.T) {
	now := at("12:00")
	cfg := Defaults()
	for i := range cfg.States {
		if cfg.States[i].ID == StateStatusWarning || cfg.States[i].ID == StateServiceFailed {
			cfg.States[i].OverNormal = true
		}
	}
	base := Inputs{Now: now, Location: time.UTC, Config: cfg}
	with := func(mod func(in *Inputs)) Shown {
		in := base
		in.Active = map[string]Activity{StateStatusWarning: {}}
		in.Config.States = slices.Clone(cfg.States)
		mod(&in)
		return Resolve(in)
	}
	until := now.Add(time.Minute)
	for _, c := range []struct {
		name string
		mod  func(in *Inputs)
		id   string
		bg   string
	}{
		{"yellow slow over blue", func(in *Inputs) {}, StateStatusWarning, Blue},
		{"a changed normal colour", func(in *Inputs) { in.Config.Normal = Look{Color: Green, Pattern: Solid} }, StateStatusWarning, Green},
		{"a blinking normal: its colour", func(in *Inputs) { in.Config.Normal = Look{Color: Magenta, Pattern: Flash} }, StateStatusWarning, Magenta},
		{"normal off: over dark", func(in *Inputs) { in.Config.Normal = LookDark }, StateStatusWarning, ""},
		{"the same colour: over dark", func(in *Inputs) { in.Config.Normal = Look{Color: Yellow, Pattern: Solid} }, StateStatusWarning, ""},
		{"a row without the option", func(in *Inputs) { in.Active = map[string]Activity{StateNoNetwork: {}} }, StateNoNetwork, ""},
		{"night: an error over dark", func(in *Inputs) {
			in.Config.Night = Night{Enabled: true, From: "11:00", To: "13:00", Show: "errors"}
			in.Active[StateServiceFailed] = Activity{}
		}, StateServiceFailed, ""},
		{"day: the same error over blue", func(in *Inputs) { in.Active[StateServiceFailed] = Activity{} }, StateServiceFailed, Blue},
		{"a preview over blue", func(in *Inputs) {
			in.Preview = &Timed{Look: Look{Color: Cyan, Pattern: Flash, OverNormal: true}, Until: until}
		}, StatePreview, Blue},
		{"a preview on a switched-off LED: over dark", func(in *Inputs) {
			in.Config.Enabled = false
			in.Preview = &Timed{Look: Look{Color: Cyan, Pattern: Flash, OverNormal: true}, Until: until}
		}, StatePreview, ""},
		{"a preview without the option", func(in *Inputs) { in.Preview = &Timed{Look: Look{Color: Cyan, Pattern: Flash}, Until: until} }, StatePreview, ""},
		{"an override over blue", func(in *Inputs) {
			in.Active = nil
			in.Overrides = []Override{{ID: "flow", Color: Red, Pattern: Fast, OverNormal: true, Priority: "normal", Since: now}}
		}, "flow", Blue},
		{"a solid look ignores a stray flag", func(in *Inputs) {
			in.Active = nil
			in.Overrides = []Override{{ID: "flow", Color: Red, Pattern: Solid, OverNormal: true, Priority: "normal", Since: now}}
		}, "flow", ""},
		{"locate: over dark", func(in *Inputs) { in.Locate = &Timed{Look: Look{Color: White, Pattern: Fast}, Until: until} }, StateLocate, ""},
		{"normal itself", func(in *Inputs) { in.Active = nil }, StateNormal, ""},
	} {
		if s := with(c.mod); s.ID != c.id || s.Background != c.bg {
			t.Errorf("%s: %+v", c.name, s)
		}
	}
}

func TestFrameOverNormal(t *testing.T) {
	r, g, b := ChannelLEDs[0], ChannelLEDs[1], ChannelLEDs[2]
	yellowSlow := Look{Color: Yellow, Pattern: Slow, OverNormal: true}
	for _, c := range []struct {
		name    string
		look    Look
		bg      string
		pattern bool
		want    []priv.LEDWrite
	}{
		// disjoint: yellow's channels in phase, blue's in the opposite phase from the end of the first on phase
		{"yellow slow over blue", yellowSlow, Blue, true, []priv.LEDWrite{
			{LED: r, Trigger: "timer", DelayOn: 500, DelayOff: 500}, {LED: g, Trigger: "timer", DelayOn: 500, DelayOff: 500},
			{LED: b, Trigger: "timer", DelayOn: 500, DelayOff: 500, StartAfterMS: 500}}},
		{"red fast over green", Look{Color: Red, Pattern: Fast, OverNormal: true}, Green, true, []priv.LEDWrite{
			{LED: r, Trigger: "timer", DelayOn: 100, DelayOff: 100}, {LED: g, Trigger: "timer", DelayOn: 100, DelayOff: 100, StartAfterMS: 100}, {LED: b, Trigger: "none"}}},
		// overlapping: blue stays on, green flashes
		{"cyan flash over blue", Look{Color: Cyan, Pattern: Flash, OverNormal: true}, Blue, true, []priv.LEDWrite{
			{LED: b, Trigger: "default-on"}, {LED: g, Trigger: "timer", DelayOn: 100, DelayOff: 1900}, {LED: r, Trigger: "none"}}},
		// the blink colour inside the background: the background's extra channel is dark during the flash
		{"blue flash over cyan", Look{Color: Blue, Pattern: Flash, OverNormal: true}, Cyan, true, []priv.LEDWrite{
			{LED: b, Trigger: "default-on"}, {LED: g, Trigger: "timer", DelayOn: 1900, DelayOff: 100, StartAfterMS: 100}, {LED: r, Trigger: "none"}}},
		{"white slow over blue", Look{Color: White, Pattern: Slow, OverNormal: true}, Blue, true, []priv.LEDWrite{
			{LED: b, Trigger: "default-on"}, {LED: r, Trigger: "timer", DelayOn: 500, DelayOff: 500}, {LED: g, Trigger: "timer", DelayOn: 500, DelayOff: 500}}},
		{"green flash over magenta", Look{Color: Green, Pattern: Flash, OverNormal: true}, Magenta, true, []priv.LEDWrite{
			{LED: g, Trigger: "timer", DelayOn: 100, DelayOff: 1900},
			{LED: r, Trigger: "timer", DelayOn: 1900, DelayOff: 100, StartAfterMS: 100}, {LED: b, Trigger: "timer", DelayOn: 1900, DelayOff: 100}}},
		// double: the complementary pattern string, together; without the trigger the flash timing
		{"red double over blue", Look{Color: Red, Pattern: Double, OverNormal: true}, Blue, true, []priv.LEDWrite{
			{LED: r, Trigger: "pattern", Pattern: doublePattern}, {LED: b, Trigger: "pattern", Pattern: doubleCounter}, {LED: g, Trigger: "none"}}},
		{"red double over blue, no pattern trigger", Look{Color: Red, Pattern: Double, OverNormal: true}, Blue, false, []priv.LEDWrite{
			{LED: r, Trigger: "timer", DelayOn: 100, DelayOff: 1900}, {LED: b, Trigger: "timer", DelayOn: 1900, DelayOff: 100, StartAfterMS: 100}, {LED: g, Trigger: "none"}}},
		{"cyan double over blue", Look{Color: Cyan, Pattern: Double, OverNormal: true}, Blue, true, []priv.LEDWrite{
			{LED: b, Trigger: "default-on"}, {LED: g, Trigger: "pattern", Pattern: doublePattern}, {LED: r, Trigger: "none"}}},
		// no background: exactly the frame of task 95
		{"yellow slow, no background", yellowSlow, "", true, Frame(Look{Color: Yellow, Pattern: Slow}, "", true)},
		{"yellow slow over dark", yellowSlow, Off, true, Frame(Look{Color: Yellow, Pattern: Slow}, "", true)},
		// the same colour would hide the blink: the resolver never passes it, and the frame blinks anyway
		{"blue slow over blue", Look{Color: Blue, Pattern: Slow, OverNormal: true}, Blue, true, Frame(Look{Color: Blue, Pattern: Slow}, "", true)},
		{"solid ignores a background", Look{Color: Red, Pattern: Solid}, Blue, true, Frame(Look{Color: Red, Pattern: Solid}, "", true)},
		{"alternate ignores a background", Look{Color: Yellow, Pattern: Alternate, Color2: Blue}, Green, true, Frame(Look{Color: Yellow, Pattern: Alternate, Color2: Blue}, "", true)},
	} {
		got := Frame(c.look, c.bg, c.pattern)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
		if err := priv.ValidLEDFrame(got); err != nil {
			t.Errorf("%s: the helper would refuse it: %v", c.name, err)
		}
	}
	// the two double strings are each other's complement, step by step
	on, off := strings.Fields(doublePattern), strings.Fields(doubleCounter)
	if len(on) != len(off) {
		t.Fatal("the double patterns differ in length")
	}
	for i := range on {
		if i%2 == 1 && on[i] != off[i] || i%2 == 0 && on[i] == off[i] {
			t.Errorf("step %d: %s and %s", i/2, on[i], off[i])
		}
	}
	// every pair of colours and every pattern is a frame the helper accepts
	for _, col := range Colors {
		for _, bg := range Colors {
			for _, p := range OverNormalPatterns {
				for _, pt := range []bool{true, false} {
					l, err := NormalizeLook(Look{Color: col, Pattern: p, OverNormal: true})
					if err != nil {
						t.Fatal(err)
					}
					if err := priv.ValidLEDFrame(Frame(l, bg, pt)); err != nil {
						t.Errorf("%+v over %s: %v", l, bg, err)
					}
				}
			}
		}
	}
}

func TestControllerOverNormal(t *testing.T) {
	r := upRig(t, charly())
	cfg := r.c.View().Config
	for i := range cfg.States {
		if cfg.States[i].ID == StateStatusWarning {
			cfg.States[i].OverNormal = true
		}
	}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(r.c.File); err != nil || !strings.Contains(string(b), `"over_normal": true`) {
		t.Fatalf("led.json: %s %v", b, err)
	}
	if again := (&Controller{Root: r.root, File: r.c.File}).View(); !slices.Equal(again.Config.States, r.c.View().Config.States) {
		t.Errorf("read back: %+v", again.Config.States)
	}
	r.mu.Lock()
	r.warns = []warnings.Warning{{ID: "certificate", Variant: "expiring"}}
	r.mu.Unlock()
	r.steps(1)
	yellow := Look{Color: Yellow, Pattern: Slow, OverNormal: true}
	r.expectShownOver(yellow, Blue, StateStatusWarning)
	// normal switched to dark: the same state blinks over dark
	cfg.Normal = LookDark
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	r.expectShownOver(yellow, "", StateStatusWarning)
	// an override may ask for it; without it an override blinks over dark as before
	cfg.Normal = Look{Color: Blue, Pattern: Solid}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.warns = nil
	r.mu.Unlock()
	o, err := r.c.SetOverride("token:flow", false, OverrideRequest{Color: Cyan, Pattern: Flash, OverNormal: true})
	if err != nil || !o.OverNormal {
		t.Fatalf("override: %+v %v", o, err)
	}
	r.steps(1)
	r.expectShownOver(Look{Color: Cyan, Pattern: Flash, OverNormal: true}, Blue, "token:flow")
	if f := r.lastFrame(); f[0] != (priv.LEDWrite{LED: ChannelLEDs[2], Trigger: "default-on"}) {
		t.Errorf("cyan over blue keeps blue on: %+v", f)
	}
	r.advance(time.Second)
	if o, err := r.c.SetOverride("token:flow", false, OverrideRequest{Color: Cyan, Pattern: Solid, OverNormal: true}); err != nil || o.OverNormal {
		t.Errorf("a solid override keeps the flag: %+v %v", o, err)
	}
	r.advance(time.Second)
	if _, err := r.c.SetOverride("token:flow", false, OverrideRequest{Color: Red, Pattern: Fast}); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	r.expectShownOver(Look{Color: Red, Pattern: Fast}, "", "token:flow")
	r.c.ClearOverrides()
	// the preview plays it
	if _, err := r.c.StartPreview(Look{Color: Red, Pattern: Double, OverNormal: true}); err != nil {
		t.Fatal(err)
	}
	r.c.step(context.Background())
	r.expectShownOver(Look{Color: Red, Pattern: Double, OverNormal: true}, Blue, StatePreview)
	if st := r.c.State(); st.Preview == nil || !st.Preview.OverNormal || st.Shown.Background != Blue {
		t.Errorf("state: %+v", st)
	}
	// written through the helper as the kernel sees it: blue's pattern is the complement
	pat, err := os.ReadFile(filepath.Join(string(r.root), "sys/class/leds", ChannelLEDs[2], "pattern"))
	if err != nil || string(pat) != doubleCounter {
		t.Errorf("blue's pattern: %q %v", pat, err)
	}
}

// B-149: a radio unit the radio configuration does not need (no activation marker from the radio
// run) is not down; one it needs stays red when it stops, and without the run's directory every
// loaded unit counts as before.
func TestControllerRadioUnitNotNeeded(t *testing.T) {
	r := upRig(t, charly())
	r.file("run/occulite/radio/rfd.enabled", true)
	r.file("run/occulite/radio/hmipserver.enabled", true)
	r.setUnit("multimacd.service", "inactive", "dead")
	r.steps(3)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// a needed unit that stops is down, after the hold
	r.setUnit("rfd.service", "failed", "failed")
	r.steps(3)
	r.expectShown(Look{Color: Red, Pattern: Solid}, StateRadioDown)
	if st := r.c.State(); st.Shown.Detail != "rfd" {
		t.Errorf("detail %q, want rfd alone", st.Shown.Detail)
	}
	r.setUnit("rfd.service", "active", "running")
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// a needed multimacd that is inactive is down
	r.file("run/occulite/radio/multimacd.enabled", true)
	r.steps(3)
	r.expectShown(Look{Color: Red, Pattern: Solid}, StateRadioDown)
	// no run directory: every loaded unit counts, as before
	r.file("run/occulite/radio/multimacd.enabled", false)
	r.file("run/occulite/radio/rfd.enabled", false)
	r.file("run/occulite/radio/hmipserver.enabled", false)
	if err := os.Remove(filepath.Join(string(r.root), "run/occulite/radio")); err != nil {
		t.Fatal(err)
	}
	r.steps(3)
	r.expectShown(Look{Color: Red, Pattern: Solid}, StateRadioDown)
}

// task 158's follow-up: hs485d is a radio unit where the radio plan runs it (a wired gateway is
// configured) - failed or stopped it is radio-down and not service-failed; where the plan does not
// run it, a failed hs485d stays service-failed.
func TestControllerWiredRadioDown(t *testing.T) {
	blue := Look{Color: Blue, Pattern: Solid}
	radioDown := Look{Color: Red, Pattern: Solid}
	active := func(r *rig) map[string]string {
		m := map[string]string{}
		for _, a := range r.c.State().Active {
			m[a.ID] = a.Detail
		}
		return m
	}
	for _, tc := range []struct {
		name string
		plan []string
	}{
		{"wired gateway in the plan", []string{"multimacd", "rfd", "hmipserver", "hs485d"}},
		{"no wired gateway", []string{"multimacd", "rfd", "hmipserver"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := upRig(t, charly())
			r.radioRun(tc.plan...)
			r.steps(1)
			r.expectShown(blue, StateNormal)
			wired := slices.Contains(tc.plan, "hs485d")
			r.setUnit("hs485d.service", "failed", "failed")
			r.steps(3)
			a := active(r)
			if wired {
				r.expectShown(radioDown, StateRadioDown)
				if d, ok := a[StateRadioDown]; !ok || d != "hs485d" {
					t.Errorf("radio-down detail %q (%v), want hs485d", d, ok)
				}
				if _, ok := a[StateServiceFailed]; ok {
					t.Errorf("hs485d is service-failed as well: %v", a)
				}
			} else {
				r.expectShown(Look{Color: Red, Pattern: Slow}, StateServiceFailed)
				if d := a[StateServiceFailed]; d != "hs485d" {
					t.Errorf("service-failed detail %q, want hs485d", d)
				}
				if _, ok := a[StateRadioDown]; ok {
					t.Errorf("radio-down without a wired gateway: %v", a)
				}
			}
			r.setUnit("hs485d.service", "active", "running")
			r.steps(1)
			r.expectShown(blue, StateNormal)
			// stopped by hand (systemctl stop): down where the plan runs it, nothing otherwise
			r.setUnit("hs485d.service", "inactive", "dead")
			r.steps(3)
			if wired {
				r.expectShown(radioDown, StateRadioDown)
			} else {
				r.expectShown(blue, StateNormal)
			}
			// with the other radio units down too, the detail names them in the boot order
			if wired {
				r.setUnit("rfd.service", "failed", "failed")
				r.steps(3)
				if d := active(r)[StateRadioDown]; d != "rfd, hs485d" {
					t.Errorf("radio-down detail %q, want \"rfd, hs485d\"", d)
				}
				r.setUnit("rfd.service", "active", "running")
			}
			r.setUnit("hs485d.service", "active", "running")
			r.steps(1)
			r.expectShown(blue, StateNormal)
		})
	}
}

// task 158: interfaces-starting - which units, which not, its place in the list, the night.
func TestControllerInterfacesStarting(t *testing.T) {
	blue := Look{Color: Blue, Pattern: Solid}
	r := upRig(t, charly())
	r.expectShown(blue, StateNormal)
	// a restart on the Services page, after the boot: hs485d counts too
	r.setUnit("hs485d.service", "activating", "start")
	r.steps(1)
	r.expectShownOver(startingLook, Blue, StateInterfacesStarting)
	if st := r.c.State(); st.Shown.Detail != "hs485d" {
		t.Errorf("detail %q", st.Shown.Detail)
	}
	r.setUnit("hs485d.service", "active", "running")
	r.steps(1)
	r.expectShown(blue, StateNormal)
	// waiting for a restart is not starting (it is radio-down after the hold)
	r.setUnit("rfd.service", "activating", "auto-restart")
	r.steps(1)
	r.expectShown(blue, StateNormal)
	r.steps(2)
	r.expectShown(Look{Color: Red, Pattern: Solid}, StateRadioDown)
	// an error beats a start: rfd down, hmipserver starting
	r.setUnit("hmipserver.service", "activating", "start")
	r.steps(1)
	r.expectShown(Look{Color: Red, Pattern: Solid}, StateRadioDown)
	r.setUnit("rfd.service", "active", "running")
	r.steps(1)
	r.expectShownOver(startingLook, Blue, StateInterfacesStarting)
	// ... and a start beats a warning
	r.mu.Lock()
	r.warns = []warnings.Warning{{ID: "certificate", Variant: "expiring"}}
	r.mu.Unlock()
	r.steps(1)
	r.expectShownOver(startingLook, Blue, StateInterfacesStarting)
	r.mu.Lock()
	r.warns = nil
	r.mu.Unlock()
	// a hs485d failing without a wired gateway in the radio plan (no marker) is service-failed,
	// not radio-down (TestControllerWiredRadioDown has the other case)
	r.setUnit("hmipserver.service", "active", "running")
	r.setUnit("hs485d.service", "failed", "failed")
	r.steps(3)
	r.expectShown(Look{Color: Red, Pattern: Slow}, StateServiceFailed)
	r.setUnit("hs485d.service", "active", "running")
	r.steps(1)
	r.expectShown(blue, StateNormal)
	// a masked unit is not starting, nor one the radio configuration does not need (B-149)
	r.mu.Lock()
	r.units[1].Load, r.units[1].Active, r.units[1].Sub = "masked", "activating", "start"
	r.mu.Unlock()
	r.steps(1)
	r.expectShown(blue, StateNormal)
	r.radioRun("rfd", "hmipserver")
	r.setUnit("multimacd.service", "activating", "start")
	r.steps(1)
	r.expectShown(blue, StateNormal)
	// green as the normal colour: the blink runs over green; at night it is dark (not an error)
	r.setUnit("hmipserver.service", "activating", "start")
	cfg := r.c.View().Config
	cfg.Normal = Look{Color: Green, Pattern: Solid}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	r.expectShownOver(startingLook, Green, StateInterfacesStarting)
	cfg.Night = Night{Enabled: true, From: "00:00", To: "23:59", Show: "errors"}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	r.expectShown(LookDark, SourceNight)
	// switched off on the page: normal again
	cfg.Night.Enabled = false
	for i := range cfg.States {
		if cfg.States[i].ID == StateInterfacesStarting {
			cfg.States[i].Enabled = false
		}
	}
	if _, err := r.c.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	r.expectShown(Look{Color: Green, Pattern: Solid}, StateNormal)
}

// task 158: without the radio run's result - an older image - booting ends when one of the
// interface units leaves inactive, and at the latest after bootLimit.
func TestControllerBootFallback(t *testing.T) {
	r := coldRig(t)
	r.steps(2)
	r.expectShown(LookBooting, StateBooting)
	r.setUnit("rfd.service", "activating", "start")
	r.steps(3)
	r.expectShownOver(startingLook, Blue, StateInterfacesStarting)
	// multimacd and hmipserver still inactive: without a plan they are neither starting nor down
	// before the boot has finished ...
	r.setUnit("rfd.service", "active", "running")
	r.steps(3)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
	// ... and down after it, as before
	r.file("var/status/startupFinished", true)
	r.steps(4)
	r.expectShown(Look{Color: Red, Pattern: Solid}, StateRadioDown)
	if st := r.c.State(); st.Shown.Detail != "multimacd, hmipserver" {
		t.Errorf("detail %q", st.Shown.Detail)
	}

	// no units at all (no systemd): the uptime
	r = coldRig(t)
	r.c.Src.Units = nil
	r.steps(1)
	r.expectShown(LookBooting, StateBooting)
	r.file("proc/uptime", false)
	full := filepath.Join(string(r.root), "proc/uptime")
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte("901.00 100.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.steps(1)
	r.expectShown(Look{Color: Blue, Pattern: Solid}, StateNormal)
}

// task 34: while the system goes down (startupFinished gone after it was seen) another writer -
// upstream's S99SetupLEDs stop - is not fought: our shutdown frame is written once, a change by the
// other process stays, no conflict is recorded.
func TestControllerNoReadBackAtShutdown(t *testing.T) {
	r := upRig(t, charly())
	_ = os.Remove(filepath.Join(string(r.root), "var/status/startupFinished"))
	r.c.step(context.Background())
	n := r.frameCount()
	trigger := filepath.Join(string(r.root), "sys/class/leds", ChannelLEDs[0], "trigger")
	if err := os.WriteFile(trigger, []byte("none [timer] heartbeat"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.steps(3)
	r.c.step(context.Background())
	if r.frameCount() != n || r.c.State().Conflict {
		t.Fatalf("fought at shutdown: frames %d → %d, conflict %v", n, r.frameCount(), r.c.State().Conflict)
	}
}
