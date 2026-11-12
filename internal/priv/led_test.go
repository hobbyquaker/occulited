package priv

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidLEDFrame(t *testing.T) {
	ok := []LEDWrite{{LED: "rpi_rf_mod:red", Trigger: "timer", DelayOn: 100, DelayOff: 100}, {LED: "rpi_rf_mod:green", Trigger: "default-on"}}
	if err := ValidLEDFrame(ok); err != nil {
		t.Fatalf("valid frame refused: %v", err)
	}
	bad := map[string][]LEDWrite{
		"empty":             {},
		"five":              {{LED: "a", Trigger: "none"}, {LED: "b", Trigger: "none"}, {LED: "c", Trigger: "none"}, {LED: "d", Trigger: "none"}, {LED: "e", Trigger: "none"}},
		"slash":             {{LED: "../etc", Trigger: "none"}},
		"dotdot":            {{LED: "..", Trigger: "none"}},
		"twice":             {{LED: "a", Trigger: "none"}, {LED: "a", Trigger: "none"}},
		"trigger":           {{LED: "a", Trigger: "oneshot"}},
		"timer short":       {{LED: "a", Trigger: "timer", DelayOn: 1, DelayOff: 100}},
		"timer long":        {{LED: "a", Trigger: "timer", DelayOn: 100, DelayOff: 20000}},
		"delay on none":     {{LED: "a", Trigger: "none", DelayOn: 100}},
		"pattern syntax":    {{LED: "a", Trigger: "pattern", Pattern: "1 150; reboot"}},
		"pattern on timer":  {{LED: "a", Trigger: "timer", DelayOn: 100, DelayOff: 100, Pattern: "1 150 0 150"}},
		"start after":       {{LED: "a", Trigger: "none", StartAfterMS: 1001}},
		"start after total": {{LED: "a", Trigger: "none", StartAfterMS: 600}, {LED: "b", Trigger: "none", StartAfterMS: 600}},
		"negative":          {{LED: "a", Trigger: "none", StartAfterMS: -1}},
	}
	for name, f := range bad {
		if err := ValidLEDFrame(f); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fakeLEDs builds /sys/devices/platform/leds/<name> with the attributes a gpio LED has, and the
// /sys/class/leds/<name> symlinks into it, as the kernel lays them out.
func fakeLEDs(t *testing.T, root string, names ...string) string {
	t.Helper()
	class := filepath.Join(root, "sys/class/leds")
	if err := os.MkdirAll(class, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		dev := filepath.Join(root, "sys/devices/platform/leds", n)
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		for attr, v := range map[string]string{"trigger": "[none] default-on timer heartbeat\n", "brightness": "1\n", "delay_on": "", "delay_off": "", "pattern": ""} {
			if err := os.WriteFile(filepath.Join(dev, attr), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(filepath.Join("../../devices/platform/leds", n), filepath.Join(class, n)); err != nil {
			t.Fatal(err)
		}
	}
	return class
}

func readAttr(t *testing.T, class, led, attr string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(class, led, attr))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLocalWriteLEDs(t *testing.T) {
	class := fakeLEDs(t, t.TempDir(), "rpi_rf_mod:red", "rpi_rf_mod:green", "rpi_rf_mod:blue")
	frame := []LEDWrite{
		{LED: "rpi_rf_mod:red", Trigger: "timer", DelayOn: 100, DelayOff: 1900},
		{LED: "rpi_rf_mod:green", Trigger: "pattern", Pattern: "1 150 1 0 0 150 0 0"},
		{LED: "rpi_rf_mod:blue", Trigger: "none"},
	}
	if err := (Local{}).WriteLEDs(class, frame); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ led, attr, want string }{
		{"rpi_rf_mod:red", "trigger", "timer"},
		{"rpi_rf_mod:red", "delay_on", "100"},
		{"rpi_rf_mod:red", "delay_off", "1900"},
		{"rpi_rf_mod:green", "trigger", "pattern"},
		{"rpi_rf_mod:green", "pattern", "1 150 1 0 0 150 0 0"},
		{"rpi_rf_mod:blue", "trigger", "none"},
		{"rpi_rf_mod:blue", "brightness", "0"},
	} {
		if got := readAttr(t, class, c.led, c.attr); got != c.want {
			t.Errorf("%s/%s = %q, want %q", c.led, c.attr, got, c.want)
		}
	}
	// an attribute is written in place, never created: an LED without delay_on fails
	if err := os.Remove(filepath.Join(class, "rpi_rf_mod:red", "delay_on")); err != nil {
		t.Fatal(err)
	}
	if err := (Local{}).WriteLEDs(class, frame[:1]); err == nil {
		t.Error("a missing attribute was created")
	}
	if _, err := os.Stat(filepath.Join(class, "rpi_rf_mod:red", "delay_on")); err == nil {
		t.Error("delay_on was created")
	}
}

func TestLocalWriteLEDsPatternModule(t *testing.T) {
	class := fakeLEDs(t, t.TempDir(), "rpi_rf_mod:green")
	frame := []LEDWrite{{LED: "rpi_rf_mod:green", Trigger: "pattern", Pattern: "1 500 1 0 0 500 0 0"}}
	oldWrite, oldLoad := writeLEDAttr, modprobe
	defer func() { writeLEDAttr, modprobe = oldWrite, oldLoad }()
	// the kernel refuses a trigger it does not know until the module is loaded
	loaded, calls := false, 0
	writeLEDAttr = func(path, value string) error {
		if filepath.Base(path) == "trigger" && value == "pattern" && !loaded {
			return errors.New("invalid argument")
		}
		return oldWrite(path, value)
	}
	modprobe = func(string) error { calls++; loaded = true; return nil }
	if err := (Local{}).WriteLEDs(class, frame); err != nil {
		t.Fatalf("after loading the module: %v", err)
	}
	if calls != 1 || readAttr(t, class, "rpi_rf_mod:green", "trigger") != "pattern" {
		t.Errorf("module loads %d, trigger %q", calls, readAttr(t, class, "rpi_rf_mod:green", "trigger"))
	}
	// a second frame finds the trigger and loads nothing
	if err := (Local{}).WriteLEDs(class, frame); err != nil || calls != 1 {
		t.Errorf("second frame: %v, module loads %d", err, calls)
	}
	// no module to load: ErrLEDPattern, which the controller answers with the timer trigger
	loaded = false
	modprobe = func(string) error { calls++; return errors.New("module not found") }
	if err := (Local{}).WriteLEDs(class, frame); !errors.Is(err, ErrLEDPattern) {
		t.Errorf("without the module: %v", err)
	}
}

func TestServerLEDOperation(t *testing.T) {
	root := t.TempDir()
	class := fakeLEDs(t, root, "rpi_rf_mod:red", "rpi_rf_mod:green", "rpi_rf_mod:blue", "ACT")
	// an entry that leads out of /sys
	outside := filepath.Join(t.TempDir(), "evil")
	_ = os.MkdirAll(outside, 0o755)
	_ = os.WriteFile(filepath.Join(outside, "trigger"), []byte("none"), 0o644)
	_ = os.Remove(filepath.Join(class, "rpi_rf_mod:blue"))
	if err := os.Symlink(outside, filepath.Join(class, "rpi_rf_mod:blue")); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logged []string
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Log: func(f string, a ...any) { logged = append(logged, f) }}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}

	if err := c.WriteLEDs(class, []LEDWrite{{LED: "rpi_rf_mod:red", Trigger: "default-on"}, {LED: "rpi_rf_mod:green", Trigger: "timer", DelayOn: 500, DelayOff: 500}}); err != nil {
		t.Fatalf("a valid frame: %v", err)
	}
	if got := readAttr(t, class, "rpi_rf_mod:red", "trigger"); got != "default-on" {
		t.Errorf("red trigger %q", got)
	}
	refused := map[string]struct {
		dir   string
		frame []LEDWrite
	}{
		"not on the list":    {class, []LEDWrite{{LED: "mmc0::", Trigger: "none"}}},
		"another directory":  {filepath.Join(root, "sys/devices/platform/leds"), []LEDWrite{{LED: "rpi_rf_mod:red", Trigger: "none"}}},
		"outside /sys":       {class, []LEDWrite{{LED: "rpi_rf_mod:blue", Trigger: "none"}}},
		"bad trigger":        {class, []LEDWrite{{LED: "rpi_rf_mod:red", Trigger: "oneshot"}}},
		"not under the root": {filepath.Join(t.TempDir(), "sys/class/leds"), []LEDWrite{{LED: "rpi_rf_mod:red", Trigger: "none"}}},
	}
	for name, r := range refused {
		if err := c.WriteLEDs(r.dir, r.frame); err == nil || !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "trigger")); string(b) != "none" {
		t.Errorf("the file outside /sys was written: %q", b)
	}
	if len(logged) == 0 || !strings.Contains(strings.Join(logged, "\n"), "LED frame") {
		t.Errorf("refusals not logged: %v", logged)
	}
}

// openccu-lite B-299: the daemon cannot see /lib/modules (ProtectKernelModules=), so the helper
// answers whether the pattern trigger's module can be had by loading it - one of LEDModules, with
// nothing else in the request.
func TestLEDModuleOperation(t *testing.T) {
	old := modprobe
	defer func() { modprobe = old }()
	var loaded []string
	modprobe = func(name string) error {
		loaded = append(loaded, name)
		if name != "ledtrig-pattern" {
			return errors.New("not found")
		}
		return nil
	}
	if err := (Local{}).LoadLEDModule(context.Background(), "ledtrig-pattern"); err != nil {
		t.Fatalf("local: %v", err)
	}
	if err := (Local{}).LoadLEDModule(context.Background(), "pwm-gpio"); err == nil || len(loaded) != 1 {
		t.Errorf("a name off the list reached modprobe: %v %v", err, loaded)
	}

	root := t.TempDir()
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logged []string
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Log: func(f string, a ...any) { logged = append(logged, f) }}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}
	if err := c.LoadLEDModule(ctx, "ledtrig-pattern"); err != nil {
		t.Fatalf("through the helper: %v", err)
	}
	if len(loaded) != 2 || loaded[1] != "ledtrig-pattern" {
		t.Errorf("modprobe calls %v", loaded)
	}
	if err := c.LoadLEDModule(ctx, "ledtrig-netdev"); err == nil || !errors.Is(err, ErrRefused) {
		t.Errorf("a module off the list: %v", err)
	}
	if _, err := c.call(ctx, request{Op: opLEDModule, Name: "ledtrig-pattern", Path: "/x"}); err == nil || !errors.Is(err, ErrRefused) {
		t.Errorf("a request with more than the name: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("a refused request reached modprobe: %v", loaded)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "LED module") {
		t.Errorf("refusals not logged: %v", logged)
	}
}

// Task 315: a level with none and timer, a one-shot fade before the frame, hr_pattern where the
// kernel has it, and no reset of an LED that already runs the pattern trigger.
func TestLocalWriteLEDsLevelsAndFades(t *testing.T) {
	class := fakeLEDs(t, t.TempDir(), "rpi_rf_mod:red", "rpi_rf_mod:green")
	for _, led := range []string{"rpi_rf_mod:red", "rpi_rf_mod:green"} {
		for _, a := range []string{"hr_pattern", "repeat"} {
			if err := os.WriteFile(filepath.Join(class, led, a), []byte("-1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	oldWrite, oldSleep := writeLEDAttr, ledSleep
	defer func() { writeLEDAttr, ledSleep = oldWrite, oldSleep }()
	var log []string
	var slept []time.Duration
	// the fake kernel: the trigger file lists the triggers with the active one in brackets
	writeLEDAttr = func(path, value string) error {
		log = append(log, filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path)+"="+value)
		if filepath.Base(path) == "trigger" {
			return oldWrite(path, "none default-on timer pattern "+strings.NewReplacer("none ", "[none] ", "pattern ", "[pattern] ", "timer ", "[timer] ").Replace(value+" "))
		}
		return oldWrite(path, value)
	}
	ledSleep = func(d time.Duration) { slept = append(slept, d) }

	// a level with none, a level the blink lights to with timer
	frame := []LEDWrite{{LED: "rpi_rf_mod:red", Trigger: "none", Brightness: 56}, {LED: "rpi_rf_mod:green", Trigger: "timer", DelayOn: 500, DelayOff: 500, Brightness: 128}}
	if err := (Local{}).WriteLEDs(class, frame); err != nil {
		t.Fatal(err)
	}
	if readAttr(t, class, "rpi_rf_mod:red", "brightness") != "56" || readAttr(t, class, "rpi_rf_mod:green", "brightness") != "128" || readAttr(t, class, "rpi_rf_mod:green", "delay_on") != "500" {
		t.Errorf("levels: red %s, green %s", readAttr(t, class, "rpi_rf_mod:red", "brightness"), readAttr(t, class, "rpi_rf_mod:green", "brightness"))
	}
	if len(slept) != 0 {
		t.Errorf("slept without a fade: %v", slept)
	}

	// a fade into a pattern: the one-shot first, waited for, then the pattern - on hr_pattern; the
	// pattern trigger replaces the timer directly, and is never reset while the LED stays on it
	log, slept = nil, nil
	frame = []LEDWrite{{LED: "rpi_rf_mod:green", Trigger: "pattern", Pattern: "255 500 255 0 0 500 0 0", Fade: "128 300 255 0"}}
	if err := (Local{}).WriteLEDs(class, frame); err != nil {
		t.Fatal(err)
	}
	want := []string{"rpi_rf_mod:green/trigger=pattern", "rpi_rf_mod:green/repeat=1", "rpi_rf_mod:green/hr_pattern=128 300 255 0", "rpi_rf_mod:green/repeat=-1", "rpi_rf_mod:green/hr_pattern=255 500 255 0 0 500 0 0"}
	if !slices.Equal(log, want) {
		t.Errorf("fade into a pattern:\n got %q\nwant %q", log, want)
	}
	if len(slept) != 1 || slept[0] < 300*time.Millisecond || slept[0] > 400*time.Millisecond {
		t.Errorf("waited %v for a 300 ms fade", slept)
	}
	if readAttr(t, class, "rpi_rf_mod:green", "pattern") != "" {
		t.Error("the jiffies pattern attribute was written although hr_pattern exists")
	}
	// the next pattern replaces the running one without a reset and without repeat
	log, slept = nil, nil
	frame = []LEDWrite{{LED: "rpi_rf_mod:green", Trigger: "pattern", Pattern: "255 100 255 0 0 100 0 0"}}
	if err := (Local{}).WriteLEDs(class, frame); err != nil {
		t.Fatal(err)
	}
	if want := []string{"rpi_rf_mod:green/hr_pattern=255 100 255 0 0 100 0 0"}; !slices.Equal(log, want) {
		t.Errorf("pattern to pattern:\n got %q\nwant %q", log, want)
	}
	// a fade into a steady level: the one-shot on the pattern trigger, then none and the brightness
	log, slept = nil, nil
	frame = []LEDWrite{{LED: "rpi_rf_mod:green", Trigger: "none", Brightness: 18, Fade: "255 300 18 0"}}
	if err := (Local{}).WriteLEDs(class, frame); err != nil {
		t.Fatal(err)
	}
	want = []string{"rpi_rf_mod:green/repeat=1", "rpi_rf_mod:green/hr_pattern=255 300 18 0", "rpi_rf_mod:green/trigger=none", "rpi_rf_mod:green/brightness=18"}
	if !slices.Equal(log, want) {
		t.Errorf("fade into a level:\n got %q\nwant %q", log, want)
	}
	// without hr_pattern the pattern attribute takes the strings
	_ = os.Remove(filepath.Join(class, "rpi_rf_mod:red", "hr_pattern"))
	log = nil
	if err := (Local{}).WriteLEDs(class, []LEDWrite{{LED: "rpi_rf_mod:red", Trigger: "pattern", Pattern: "255 500 255 0 0 500 0 0"}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(log, "rpi_rf_mod:red/pattern=255 500 255 0 0 500 0 0") {
		t.Errorf("older kernel: %q", log)
	}

	// the frame's bounds
	for name, f := range map[string][]LEDWrite{
		"brightness over 255":        {{LED: "rpi_rf_mod:red", Trigger: "none", Brightness: 256}},
		"brightness with default-on": {{LED: "rpi_rf_mod:red", Trigger: "default-on", Brightness: 10}},
		"brightness with pattern":    {{LED: "rpi_rf_mod:red", Trigger: "pattern", Pattern: "1 500 1 0 0 500 0 0", Brightness: 10}},
		"fade malformed":             {{LED: "rpi_rf_mod:red", Trigger: "none", Fade: "255 300 x"}},
		"fade too long":              {{LED: "rpi_rf_mod:red", Trigger: "none", Fade: "255 3000 0 0"}},
	} {
		if err := ValidLEDFrame(f); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := ValidLEDFrame([]LEDWrite{{LED: "rpi_rf_mod:red", Trigger: "none", Brightness: 255, Fade: "0 300 255 0"}, {LED: "rpi_rf_mod:green", Trigger: "timer", DelayOn: 100, DelayOff: 100, Brightness: 8}}); err != nil {
		t.Errorf("a valid frame refused: %v", err)
	}
	if ms := patternMS("0 250 37 250 128 250"); ms != 750 {
		t.Errorf("patternMS %d", ms)
	}
}

// occulited B-139: the board LEDs /var/hm_mode names, with the triggers S01InitHost gives them,
// are the led operation's to write - the Settings page's onboard-LED switch goes through it now,
// not through a shell redirect.
func TestLEDPolicyBoardLEDs(t *testing.T) {
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	for _, c := range []struct {
		led, trigger string
		ok           bool
	}{
		{"ACT", "heartbeat", true}, {"ACT", "mmc0", true}, {"PWR", "mmc1", true}, {"PWR", "none", true},
		{"blue:status", "heartbeat", true}, {"green:", "heartbeat", true}, {"red:", "none", true},
		{"yellow:", "default-on", true}, {"ACT", "mmc3", true},
		{"ACT", "oneshot", false}, {"ACT", "mmc4", false}, {"mmc0::", "none", false}, {"eth0", "none", false},
	} {
		err := p.ledAllowed("/sys/class/leds", []LEDWrite{{LED: c.led, Trigger: c.trigger}})
		if (err == nil) != c.ok {
			t.Errorf("%s=%s: %v", c.led, c.trigger, err)
		}
	}
}
