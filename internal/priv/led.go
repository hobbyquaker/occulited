package priv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The status LED (task 95). occulited's LED controller is the only writer of the RPI-RF-MOD's RGB
// LED after the boot; what it writes is a frame - one entry per LED - and the helper applies it as
// root. One typed operation rather than a sysfs path on Paths or a shell redirect: the LED names
// are a closed list, the triggers a closed set, the numbers bounded, and a sysfs attribute cannot
// be written with WriteFile's temporary file plus rename anyway.
//
// The order is part of the operation: every LED of the frame goes to trigger "none" first, then
// the triggers are set back to back, so the channels of a mixed colour (yellow = red + green) start
// their timers together and blink without colour fringes. StartAfterMS delays one entry, which is
// how two colours alternate on the timer trigger (hss_led's own way, led.cpp:75-76).

// opLED is the operation's name on the wire.
const opLED = "led"

// LEDWrite is one LED's part of a frame.
type LEDWrite struct {
	// LED is the name under /sys/class/leds (rpi_rf_mod:red, PWR).
	LED string `json:"led"`
	// Trigger is one of LEDTriggers.
	Trigger string `json:"trigger"`
	// DelayOn and DelayOff are the timer trigger's milliseconds (LEDDelayMin to LEDDelayMax);
	// zero for every other trigger.
	DelayOn  int `json:"delay_on,omitempty"`
	DelayOff int `json:"delay_off,omitempty"`
	// Pattern is the pattern trigger's "brightness duration" pairs; empty for every other trigger.
	Pattern string `json:"pattern,omitempty"`
	// StartAfterMS waits this long before the entry's trigger is set (0 to LEDStartAfterMax).
	StartAfterMS int `json:"start_after_ms,omitempty"`
	// Brightness is the level written after the trigger (task 315, an LED with max_brightness
	// above 1): with none the LED's steady level (0 = dark, as before), with timer the level the
	// blink lights to; 0 to LEDBrightnessMax, nothing written for 0 but with none.
	Brightness int `json:"brightness,omitempty"`
	// Fade is a one-shot pattern ("from 300 to 0") the helper runs on the LED before the frame
	// (task 315): the pattern trigger with repeat 1, waited for, then the entry's own trigger.
	Fade string `json:"fade,omitempty"`
}

// LEDTriggers are the triggers a frame may set: the controller's own (none, default-on, timer,
// pattern) and the board LEDs' normal ones from /var/hm_mode, which the controller writes back
// after it used the red power LED as an error light, and which the Settings page's onboard-LED
// switch writes back (occulited B-139; S01InitHost's mmc<n> is the root file system's card).
var LEDTriggers = []string{"none", "default-on", "timer", "pattern", "heartbeat", "mmc0", "mmc1", "mmc2", "mmc3", "input", "actpwr"}

// The bounds of a frame.
const (
	LEDMaxFrame      = 4
	LEDDelayMin      = 10
	LEDDelayMax      = 10000
	LEDStartAfterMax = 1000
	LEDBrightnessMax = 255
	LEDFadeMaxMS     = 2000 // a fade's whole length; the helper waits for it
)

var (
	ledNameRe = regexp.MustCompile(`^[A-Za-z0-9_:.-]{1,32}$`)
	// ledPatternRe is the kernel's pattern syntax, bounded: up to 32 pairs of a brightness and a
	// duration. A malformed string would silently deactivate the trigger, so it never gets there.
	ledPatternRe = regexp.MustCompile(`^[0-9]{1,3} [0-9]{1,5}( [0-9]{1,3} [0-9]{1,5}){0,31}$`)
)

// ValidLEDFrame checks a frame's shape: the helper checks it at its boundary, the operation again.
func ValidLEDFrame(frame []LEDWrite) error {
	if len(frame) == 0 || len(frame) > LEDMaxFrame {
		return fmt.Errorf("a frame has 1 to %d entries", LEDMaxFrame)
	}
	seen := map[string]bool{}
	wait := 0
	for _, w := range frame {
		if !ledNameRe.MatchString(w.LED) || w.LED == "." || w.LED == ".." {
			return fmt.Errorf("led name %q", w.LED)
		}
		if seen[w.LED] {
			return fmt.Errorf("led %s twice in one frame", w.LED)
		}
		seen[w.LED] = true
		if !slices.Contains(LEDTriggers, w.Trigger) {
			return fmt.Errorf("led %s: trigger %q", w.LED, w.Trigger)
		}
		if w.Trigger == "timer" {
			if w.DelayOn < LEDDelayMin || w.DelayOn > LEDDelayMax || w.DelayOff < LEDDelayMin || w.DelayOff > LEDDelayMax {
				return fmt.Errorf("led %s: timer delays must be %d to %d ms", w.LED, LEDDelayMin, LEDDelayMax)
			}
		} else if w.DelayOn != 0 || w.DelayOff != 0 {
			return fmt.Errorf("led %s: delays belong to the timer trigger", w.LED)
		}
		if w.Trigger == "pattern" {
			if !ledPatternRe.MatchString(w.Pattern) {
				return fmt.Errorf("led %s: pattern %q", w.LED, w.Pattern)
			}
		} else if w.Pattern != "" {
			return fmt.Errorf("led %s: a pattern belongs to the pattern trigger", w.LED)
		}
		if w.StartAfterMS < 0 || w.StartAfterMS > LEDStartAfterMax {
			return fmt.Errorf("led %s: start_after_ms must be 0 to %d", w.LED, LEDStartAfterMax)
		}
		if w.Brightness < 0 || w.Brightness > LEDBrightnessMax {
			return fmt.Errorf("led %s: brightness must be 0 to %d", w.LED, LEDBrightnessMax)
		}
		if w.Brightness != 0 && w.Trigger != "none" && w.Trigger != "timer" {
			return fmt.Errorf("led %s: a brightness belongs to the none or timer trigger", w.LED)
		}
		if w.Fade != "" {
			if !ledPatternRe.MatchString(w.Fade) {
				return fmt.Errorf("led %s: fade %q", w.LED, w.Fade)
			}
			if ms := patternMS(w.Fade); ms > LEDFadeMaxMS {
				return fmt.Errorf("led %s: a fade lasts %d ms at most", w.LED, LEDFadeMaxMS)
			}
		}
		wait += w.StartAfterMS
	}
	if wait > LEDStartAfterMax {
		return fmt.Errorf("a frame waits %d ms at most", LEDStartAfterMax)
	}
	return nil
}

// patternMS is how long one run of a pattern string takes: the sum of its durations.
func patternMS(pattern string) int {
	f := strings.Fields(pattern)
	ms := 0
	for i := 1; i < len(f); i += 2 {
		n, _ := strconv.Atoi(f[i])
		ms += n
	}
	return ms
}

// ledAllowed: the directory is exactly the policy's LED class directory and every LED of a valid
// frame is on the policy's list.
func (p Policy) ledAllowed(dir string, frame []LEDWrite) error {
	rel, ok := p.rel(dir)
	if !ok || p.LEDDir == "" || rel != filepath.Clean(p.LEDDir) {
		return fmt.Errorf("led directory %s", dir)
	}
	if err := ValidLEDFrame(frame); err != nil {
		return err
	}
	for _, w := range frame {
		if !slices.Contains(p.LEDNames, w.LED) {
			return fmt.Errorf("led %s is not on the list", w.LED)
		}
	}
	return nil
}

// ledUnderSys: the entries of /sys/class/leds are symlinks into /sys/devices, so a symlink is not
// the refusal - an LED directory that resolves anywhere outside the root's /sys is.
func (p Policy) ledUnderSys(dir string, frame []LEDWrite) error {
	sys, err := filepath.EvalSymlinks(filepath.Join(p.Root, "sys"))
	if err != nil {
		return err
	}
	for _, w := range frame {
		real, err := filepath.EvalSymlinks(filepath.Join(dir, w.LED))
		if err != nil {
			return err
		}
		if !strings.HasPrefix(real, filepath.Clean(sys)+"/") {
			return fmt.Errorf("led %s resolves to %s, outside /sys", w.LED, real)
		}
	}
	return nil
}

// LEDModules are the kernel modules the status LED may ask for (openccu-lite B-299): the pattern
// trigger, which the older images build as a module nothing loads at boot. A closed list.
var LEDModules = []string{"ledtrig-pattern"}

// opLEDModule loads one of LEDModules (openccu-lite B-299). The daemon's unit has
// ProtectKernelModules=, so neither /lib/modules nor modprobe is its to see or run: whether the
// pattern trigger can be had is answered on this side, by loading it.
const opLEDModule = "led-module"

// modprobe loads a kernel module by name. A variable so a test can count the calls.
var modprobe = func(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prog := "/sbin/modprobe"
	if _, err := os.Stat(prog); err != nil {
		if p, lerr := exec.LookPath("modprobe"); lerr == nil {
			prog = p
		}
	}
	out, err := exec.CommandContext(ctx, prog, name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("modprobe %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// LoadLEDModule loads one of LEDModules; any other name is refused before anything runs.
func (Local) LoadLEDModule(_ context.Context, name string) error {
	if !slices.Contains(LEDModules, name) {
		return fmt.Errorf("module %q is not on the LED list", name)
	}
	return modprobe(name)
}

// LoadLEDModule asks the helper to load one of LEDModules.
func (c Client) LoadLEDModule(ctx context.Context, name string) error {
	_, err := c.call(ctx, request{Op: opLEDModule, Name: name})
	return err
}

// ErrLEDPattern is WriteLEDs' answer when the pattern trigger cannot be had; the controller then
// renders its patterns with the timer trigger instead.
var ErrLEDPattern = errors.New("the pattern trigger is not available")

// writeLEDAttr writes one sysfs attribute in place (O_TRUNC, never created). A variable so a test
// can play a kernel that refuses a trigger.
var writeLEDAttr = func(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(value)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// activeLEDTrigger is the bracketed one of a trigger attribute's text.
func activeLEDTrigger(list string) string {
	if a := strings.Index(list, "["); a >= 0 {
		if b := strings.Index(list[a:], "]"); b > 0 {
			return list[a+1 : a+b]
		}
	}
	return ""
}

// ledSleep waits inside a frame; a variable so a test does not.
var ledSleep = time.Sleep

// WriteLEDs applies a frame under dir, the LED class directory: every entry's trigger to none, then
// each entry's trigger and its attributes.
//
// Task 315: an entry whose trigger is pattern and whose LED already runs the pattern trigger is
// not reset - its new string replaces the running one at once, with no dark instant between; the
// string goes to hr_pattern (the hrtimer, exact milliseconds and no drift between channels) where
// the kernel has it, else to pattern. A Fade runs first on every entry that has one, as a one-shot
// (repeat 1), and the frame follows once the longest fade is over.
func (Local) WriteLEDs(dir string, frame []LEDWrite) error {
	if err := ValidLEDFrame(frame); err != nil {
		return err
	}
	write := func(led, attr, value string) error {
		if err := writeLEDAttr(filepath.Join(dir, led, attr), value); err != nil {
			return fmt.Errorf("%s/%s: %w", led, attr, err)
		}
		return nil
	}
	current := func(led string) string {
		b, _ := os.ReadFile(filepath.Join(dir, led, "trigger"))
		return activeLEDTrigger(string(b))
	}
	// the pattern trigger: set it, loading the module once when the kernel does not know it
	toPattern := func(led string) error {
		if current(led) == "pattern" {
			return nil
		}
		if err := write(led, "trigger", "pattern"); err != nil {
			if lerr := modprobe("ledtrig-pattern"); lerr != nil {
				return fmt.Errorf("%w: %v", ErrLEDPattern, lerr)
			}
			if err := write(led, "trigger", "pattern"); err != nil {
				return fmt.Errorf("%w: %v", ErrLEDPattern, err)
			}
		}
		return nil
	}
	patternAttr := func(led string) string {
		if _, err := os.Stat(filepath.Join(dir, led, "hr_pattern")); err == nil {
			return "hr_pattern"
		}
		return "pattern"
	}
	faded := false
	wait := 0
	for _, w := range frame {
		if w.Fade == "" {
			continue
		}
		if err := toPattern(w.LED); err != nil {
			return err
		}
		if err := write(w.LED, "repeat", "1"); err != nil {
			return err
		}
		if err := write(w.LED, patternAttr(w.LED), w.Fade); err != nil {
			return err
		}
		faded = true
		wait = max(wait, patternMS(w.Fade))
	}
	if faded {
		ledSleep(time.Duration(wait+50) * time.Millisecond)
	}
	for _, w := range frame {
		if w.Trigger == "pattern" && current(w.LED) == "pattern" {
			continue
		}
		if err := write(w.LED, "trigger", "none"); err != nil {
			return err
		}
	}
	for _, w := range frame {
		if w.StartAfterMS > 0 {
			ledSleep(time.Duration(w.StartAfterMS) * time.Millisecond)
		}
		switch w.Trigger {
		case "none":
			if err := write(w.LED, "brightness", strconv.Itoa(w.Brightness)); err != nil {
				return err
			}
		case "timer":
			if err := write(w.LED, "trigger", "timer"); err != nil {
				return err
			}
			if err := write(w.LED, "delay_on", strconv.Itoa(w.DelayOn)); err != nil {
				return err
			}
			if err := write(w.LED, "delay_off", strconv.Itoa(w.DelayOff)); err != nil {
				return err
			}
			if w.Brightness > 0 {
				if err := write(w.LED, "brightness", strconv.Itoa(w.Brightness)); err != nil {
					return err
				}
			}
		case "pattern":
			if err := toPattern(w.LED); err != nil {
				return err
			}
			if w.Fade != "" {
				if err := write(w.LED, "repeat", "-1"); err != nil {
					return err
				}
			}
			if err := write(w.LED, patternAttr(w.LED), w.Pattern); err != nil {
				return err
			}
		default:
			if err := write(w.LED, "trigger", w.Trigger); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteLEDs asks the helper to apply a frame. A frame that alternates waits up to a second inside
// the helper, so the deadline is generous.
func (c Client) WriteLEDs(dir string, frame []LEDWrite) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := c.call(ctx, request{Op: opLED, Path: dir, LEDs: frame})
	if err != nil && strings.Contains(err.Error(), ErrLEDPattern.Error()) && !errors.Is(err, ErrLEDPattern) {
		return fmt.Errorf("%w: %v", ErrLEDPattern, err)
	}
	return err
}
