package led

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// Activity is one active state: since when, and what the page names (the failed units, the version).
type Activity struct {
	Since  time.Time `json:"since"`
	Detail string    `json:"detail,omitempty"`
	// Level is a radio state's active level (task 316), 1-based.
	Level int `json:"level,omitempty"`
}

// Override is an external caller's look at its priority level (POST /led/override).
type Override struct {
	ID      string `json:"id"`
	Color   string `json:"color"`
	Pattern string `json:"pattern"`
	Color2  string `json:"color2,omitempty"`
	// OverNormal blinks over the normal colour instead of over dark (task 134).
	OverNormal bool `json:"over_normal,omitempty"`
	// Priority orders the overrides among themselves: high, normal, low; the newest within a level.
	Priority string `json:"priority"`
	// Night is respect (hidden in the night window) or ignore.
	Night string `json:"night"`
	// Until is when it ends; nil = until it is cleared.
	Until *time.Time `json:"until,omitempty"`
	By    string     `json:"by"`
	Since time.Time  `json:"since"`
}

// Look is the override's look.
func (o Override) Look() Look {
	return Look{Color: o.Color, Pattern: o.Pattern, Color2: o.Color2, OverNormal: o.OverNormal}
}

// Timed is a look that ends by itself: locate and the page's Test.
type Timed struct {
	Look
	Until time.Time `json:"until"`
}

// Inputs are everything the resolver decides on.
type Inputs struct {
	Now      time.Time
	Location *time.Location
	Config   Config
	Shutdown bool
	Booting  bool
	Preview  *Timed
	Locate   *Timed
	// Active are the states whose condition holds, by id.
	Active    map[string]Activity
	Overrides []Override
	// Muted: the warning states are silenced (openccu-lite task 164, the button's mute or
	// POST /led/mute) - every configurable state but NeverMuted, and the overrides.
	Muted bool
	// Feedback is the button's own short light (task 164): the press, the long threshold
	// reached, an action's outcome; above everything but shutdown and booting.
	Feedback *Timed
}

// NeverMuted are the states a mute leaves showing (the maintainer, Q&A 2026-10-15): radio and
// network down - and interfaces-starting, which is no warning. The fixed states are above the
// list anyway.
var NeverMuted = []string{StateRadioDown, StateNoNetwork, StateInterfacesStarting}

// The sources of what is shown.
const (
	SourceFixed    = "fixed"
	SourcePreview  = "preview"
	SourceLocate   = "locate"
	SourceState    = "state"
	SourceOverride = "override"
	SourceNormal   = "normal"
	SourceNight    = "night"
	SourceOff      = "off"
	// SourceFeedback is the button's own light (task 164)
	SourceFeedback = "feedback"
)

// Shown is what the LED shows and why.
type Shown struct {
	Look
	// Background is the colour a blink's off phase shows (task 134): the normal state's colour when
	// the look blinks over it and that is visible - empty for dark.
	Background string `json:"background,omitempty"`
	// Dim is the night's level in percent (task 315) when what is shown is dimmed; 0 = full.
	Dim int `json:"dim,omitempty"`
	// Level is the radio state's level shown (task 316), 1-based.
	Level  int    `json:"level,omitempty"`
	Source string `json:"source"`
	ID     string `json:"id"`
	Detail string `json:"detail,omitempty"`
}

// NightActive reports whether now is inside the night window, in loc.
func NightActive(n Night, now time.Time, loc *time.Location) bool {
	if !n.Enabled || !hhmmRe.MatchString(n.From) || !hhmmRe.MatchString(n.To) {
		return false
	}
	if loc != nil {
		now = now.In(loc)
	}
	m := now.Hour()*60 + now.Minute()
	from, to := minutesOf(n.From), minutesOf(n.To)
	switch {
	case from == to:
		return false
	case from < to:
		return m >= from && m < to
	default: // across midnight
		return m >= from || m < to
	}
}

func priorityRank(p string) int {
	switch p {
	case "high":
		return 2
	case "low":
		return 0
	}
	return 1
}

// bestOverride is the override shown at the external row: live, not hidden by the night, the
// highest level, the newest within it.
func bestOverride(list []Override, now time.Time, night bool) *Override {
	var live []Override
	for _, o := range list {
		if o.Until != nil && !now.Before(*o.Until) {
			continue
		}
		if night && o.Night != "ignore" {
			continue
		}
		live = append(live, o)
	}
	if len(live) == 0 {
		return nil
	}
	sort.SliceStable(live, func(i, j int) bool {
		if a, b := priorityRank(live[i].Priority), priorityRank(live[j].Priority); a != b {
			return a > b
		}
		return live[i].Since.After(live[j].Since)
	})
	return &live[0]
}

// Resolve is the whole decision, a pure function: the fixed states, then the configured list in its
// order (the first active and enabled row wins, the external row standing for the best override),
// then normal - or dark, at night or when the LED is switched off.
//
// The night (task 315): with show "errors" or "off" it hides as before; with "dimmed" it hides
// nothing. What it still shows - a state, an override, normal - is dimmed to Night.Dim; the fixed
// states, locate and the test are not.
func Resolve(in Inputs) Shown {
	night := NightActive(in.Config.Night, in.Now, in.Location)
	hide := night && in.Config.Night.Show != "dimmed"
	dim := func(s Shown) Shown {
		if night && in.Config.Night.Dim < 100 {
			s.Dim = in.Config.Night.Dim
		}
		return s
	}
	switch {
	case in.Shutdown:
		return Shown{Look: LookShutdown, Source: SourceFixed, ID: StateShutdown}
	case in.Booting:
		return Shown{Look: LookBooting, Source: SourceFixed, ID: StateBooting}
	case in.Feedback != nil && in.Now.Before(in.Feedback.Until):
		return Shown{Look: in.Feedback.Look, Source: SourceFeedback, ID: SourceFeedback}
	case in.Preview != nil && in.Now.Before(in.Preview.Until):
		return over(Shown{Look: in.Preview.Look, Source: SourcePreview, ID: StatePreview}, in.Config, hide)
	case in.Locate != nil && in.Now.Before(in.Locate.Until):
		return Shown{Look: in.Locate.Look, Source: SourceLocate, ID: StateLocate}
	case !in.Config.Enabled:
		return Shown{Look: LookDark, Source: SourceOff, ID: SourceOff}
	}
	for _, st := range in.Config.States {
		if !st.Enabled {
			continue
		}
		if in.Muted && !slices.Contains(NeverMuted, st.ID) {
			continue
		}
		if st.ID == StateExternal {
			if o := bestOverride(in.Overrides, in.Now, hide); o != nil {
				return dim(over(Shown{Look: o.Look(), Source: SourceOverride, ID: o.ID}, in.Config, hide))
			}
			continue
		}
		a, ok := in.Active[st.ID]
		if !ok {
			continue
		}
		if hide && (in.Config.Night.Show == "off" || !slices.Contains(ErrorStates, st.ID)) {
			continue
		}
		look := st.Look()
		if IsRadioState(st.ID) {
			look = in.Config.Radio.LevelLook(st.ID, a.Level) // task 316: the active level's look
		}
		return dim(over(Shown{Look: look, Source: SourceState, ID: st.ID, Detail: a.Detail, Level: a.Level}, in.Config, hide))
	}
	if hide {
		return Shown{Look: LookDark, Source: SourceNight, ID: SourceNight}
	}
	return dim(Shown{Look: in.Config.Normal, Source: SourceNormal, ID: StateNormal})
}

// over sets the background of a look that blinks over the normal colour (task 134), where that is
// visible: the LED is on, it is not night, normal lights, and in another colour than the blink -
// otherwise the look blinks over dark as without the option, so a state never becomes invisible.
func over(s Shown, cfg Config, night bool) Shown {
	bg := cfg.Normal.Color
	if !s.OverNormal || !slices.Contains(OverNormalPatterns, s.Pattern) || s.Color == Off ||
		!cfg.Enabled || night || bg == "" || bg == Off || bg == s.Color {
		return s
	}
	s.Background = bg
	return s
}

// The RPI-RF-MOD's LEDs under /sys/class/leds, in channel order.
var ChannelLEDs = [3]string{"rpi_rf_mod:red", "rpi_rf_mod:green", "rpi_rf_mod:blue"}

// PWRLED is the Pi's red power LED, the error light of a box without an RGB LED.
const PWRLED = "PWR"

// Render is what the renderer knows about the LED and the levels (task 315).
type Render struct {
	// PatternTrigger: the kernel's pattern trigger can be had.
	PatternTrigger bool
	// MaxBrightness is the LED's max_brightness: 1 on an on/off LED, 255 over PWM.
	MaxBrightness int
	// Brightness is the configured level in percent (10-100); 0 reads as 100.
	Brightness int
	// Dim is the night's level in percent of that (Shown.Dim); 0 = none.
	Dim int
}

// dimmable: the LED has levels, so colours are mixed and dimmed.
func (r Render) dimmable() bool { return r.MaxBrightness > 1 }

// animated: every blink and fade runs as one hr_pattern per channel in the kernel.
func (r Render) animated() bool { return r.dimmable() && r.PatternTrigger }

// The curve from a colour's value (0-255, perceptual) to a duty cycle: the eye is logarithmic, and
// 2.2 is the usual exponent - a value of 128 is about 22 % duty and looks half as bright.
const gamma = 2.2

// minLevel is the lowest level a lit channel is driven at over PWM: 3 % of the maximum. Below it the
// hrtimer's jitter on a non-RT kernel (tens of µs against a 4 ms period) shows as shimmer.
const minLevelPercent = 3

// levels is a colour's per-channel brightness for the LED: on an on/off LED 1 where the value is
// 128 or more; over PWM the gamma curve of the value, the configured brightness and the night's
// dimming, floored at minLevelPercent for a lit channel.
func levels(color string, r Render) [3]int {
	rgb, _ := RGB(color)
	var out [3]int
	if !r.dimmable() {
		for i, v := range rgb {
			if v >= 128 {
				out[i] = 1
			}
		}
		return out
	}
	scale := 1.0
	if r.Brightness > 0 && r.Brightness < 100 {
		scale *= math.Pow(float64(r.Brightness)/100, gamma)
	}
	if r.Dim > 0 && r.Dim < 100 {
		scale *= math.Pow(float64(r.Dim)/100, gamma)
	}
	floor := max(1, int(math.Round(float64(r.MaxBrightness)*minLevelPercent/100)))
	for i, v := range rgb {
		if v == 0 {
			continue
		}
		l := int(math.Round(math.Pow(float64(v)/255, gamma) * scale * float64(r.MaxBrightness)))
		out[i] = max(l, floor)
	}
	return out
}

// doubleOf is two 150 ms flashes every 2 s at level l in the pattern trigger's syntax: a hard step
// is a repeated brightness with a duration of 0.
func doubleOf(l int) string {
	return fmt.Sprintf("%d 150 %d 0 0 150 0 0 %d 150 %d 0 0 1550 0 0", l, l, l, l)
}

// doubleCounterOf is doubleOf's complement: lit at l while that is dark (task 134).
func doubleCounterOf(l int) string {
	return fmt.Sprintf("0 150 0 0 %d 150 %d 0 0 150 0 0 %d 1550 %d 0", l, l, l, l)
}

// timerOf is the timer trigger's delays of a blinking pattern.
func timerOf(pattern string) (on, off int) {
	switch pattern {
	case Fast:
		return 100, 100
	case Flash, Double:
		return 100, 1900
	}
	return 500, 500
}

// Frame turns a look into the helper's frame for an on/off LED (max_brightness 1), with or without
// the pattern trigger: Render{PatternTrigger: patternTrigger, MaxBrightness: 1}. The tests and the
// power LED's path use it; the controller calls FrameFor.
func Frame(l Look, background string, patternTrigger bool) []priv.LEDWrite {
	return FrameFor(l, background, Render{PatternTrigger: patternTrigger, MaxBrightness: 1})
}

// FrameFor turns a look into the helper's frame: all three channels, the ones the colour does not
// light at none.
//
// Over PWM with the pattern trigger (task 315) every channel whose level changes over time runs
// one hr_pattern - the same timings on every channel, so the channels stay in step and a yellow
// blink has no red or green fringes - and a steady channel is its brightness; see FramePWM.
//
// Otherwise the kernel's timer trigger: without the pattern trigger, double is shown as flash.
// Alternate is the timer trigger on both colours with the second one started half a period later,
// as hss_led does. A background (task 134, Shown.Background) is the colour a blink's off phase
// shows: the blink colour's own channels blink in phase, the background's own channels in the
// opposite phase (the timer trigger with on and off swapped, started when the blink's first on
// phase ends, or the complementary pattern string), and the channels both light stay on. Over PWM
// without the pattern trigger the levels still dim: a steady channel below the maximum is its
// brightness, a blinking one blinks between dark and its level (the timer trigger keeps the
// brightness it is given).
func FrameFor(l Look, background string, r Render) []priv.LEDWrite {
	if r.animated() {
		return FramePWM(l, background, r)
	}
	if r.MaxBrightness < 1 {
		r.MaxBrightness = 1
	}
	pattern := l.Pattern
	if pattern == Breathe {
		pattern = Slow // no levels to pulse through
	}
	first := levels(l.Color, r)
	var second, under [3]int
	if pattern == Alternate {
		second = levels(l.Color2, r)
	} else if pattern != Solid && background != l.Color {
		under = levels(background, r)
	}
	level := func(i int) int {
		switch {
		case first[i] > 0:
			return first[i]
		case second[i] > 0:
			return second[i]
		}
		return under[i]
	}
	steadyAt := func(i, lv int) priv.LEDWrite {
		if lv >= r.MaxBrightness {
			return priv.LEDWrite{LED: ChannelLEDs[i], Trigger: "default-on"}
		}
		return priv.LEDWrite{LED: ChannelLEDs[i], Trigger: "none", Brightness: lv}
	}
	blink := func(i int, counter, delayed bool) priv.LEDWrite {
		w := priv.LEDWrite{LED: ChannelLEDs[i]}
		lv := level(i)
		switch pattern {
		case Solid:
			return steadyAt(i, lv)
		case Double:
			if r.PatternTrigger {
				w.Trigger, w.Pattern = "pattern", doubleOf(lv)
				if counter {
					w.Pattern = doubleCounterOf(lv)
				}
				return w
			}
		}
		w.Trigger = "timer"
		w.DelayOn, w.DelayOff = timerOf(pattern)
		if lv < r.MaxBrightness {
			w.Brightness = lv
		}
		if counter {
			w.DelayOn, w.DelayOff = w.DelayOff, w.DelayOn
		}
		if delayed {
			w.StartAfterMS = w.DelayOff // when the first on phase ends
			if !counter {
				w.StartAfterMS = w.DelayOn
			}
		}
		return w
	}
	var steady, frame, later, dark []priv.LEDWrite
	for i := range ChannelLEDs {
		switch {
		case first[i] > 0 && under[i] > 0:
			steady = append(steady, steadyAt(i, max(first[i], under[i])))
		case first[i] > 0:
			frame = append(frame, blink(i, false, false))
		case second[i] > 0:
			later = append(later, blink(i, false, len(later) == 0))
		case under[i] > 0:
			later = append(later, blink(i, true, len(later) == 0 && !(pattern == Double && r.PatternTrigger)))
		default:
			dark = append(dark, priv.LEDWrite{LED: ChannelLEDs[i], Trigger: "none"})
		}
	}
	return slices.Concat(steady, frame, later, dark)
}

// A phase of an animation: the three channels' levels, how long, and whether the levels glide to
// the next phase's over that time (the pattern trigger dims gradually between tuples of different
// brightness, in 50 ms steps) or hold and step.
type phase struct {
	levels [3]int
	ms     int
	glide  bool
}

// breatheSteps is how many phases a breath has: a cosine from the background to the colour and
// back, 2 s in all.
const breatheSteps = 8

// phases is a look as a cycle of phases (task 315). The dark phases show the background's levels
// (zero without one), so a blink over the normal colour is the same cycle with another floor.
func phases(l Look, background string, r Render) []phase {
	c := levels(l.Color, r)
	var b [3]int
	if l.Pattern != Solid && l.Pattern != Alternate && background != l.Color {
		b = levels(background, r)
	}
	switch l.Pattern {
	case Slow:
		return []phase{{c, 500, false}, {b, 500, false}}
	case Fast:
		return []phase{{c, 100, false}, {b, 100, false}}
	case Flash:
		return []phase{{c, 100, false}, {b, 1900, false}}
	case Double:
		return []phase{{c, 150, false}, {b, 150, false}, {c, 150, false}, {b, 1550, false}}
	case Alternate:
		return []phase{{c, 500, false}, {levels(l.Color2, r), 500, false}}
	case Breathe:
		out := make([]phase, breatheSteps)
		for k := range breatheSteps {
			// rounded to 1/10000 first: cos(π/2) is not quite 0 in floating point, and the two
			// halves of the breath must mirror each other exactly
			f := math.Round((1-math.Cos(2*math.Pi*float64(k)/breatheSteps))/2*1e4) / 1e4
			var lv [3]int
			for i := range 3 {
				lv[i] = int(math.Round(float64(b[i]) + f*float64(c[i]-b[i])))
			}
			out[k] = phase{lv, 2000 / breatheSteps, true}
		}
		return out
	}
	return []phase{{c, 0, false}}
}

// FramePWM renders a look over PWM with the pattern trigger: a channel whose level is the same in
// every phase is its brightness (trigger none), every other channel one hr_pattern string of the
// cycle - identical durations on all of them, written back to back, so they stay in step.
func FramePWM(l Look, background string, r Render) []priv.LEDWrite {
	ph := phases(l, background, r)
	var steady, moving []priv.LEDWrite
	for i := range ChannelLEDs {
		same := true
		for _, p := range ph {
			same = same && p.levels[i] == ph[0].levels[i]
		}
		if same {
			steady = append(steady, priv.LEDWrite{LED: ChannelLEDs[i], Trigger: "none", Brightness: ph[0].levels[i]})
			continue
		}
		var sb strings.Builder
		for k, p := range ph {
			if k > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(&sb, "%d %d", p.levels[i], p.ms)
			if !p.glide {
				fmt.Fprintf(&sb, " %d 0", p.levels[i])
			}
		}
		moving = append(moving, priv.LEDWrite{LED: ChannelLEDs[i], Trigger: "pattern", Pattern: sb.String()})
	}
	return slices.Concat(moving, steady)
}

// restingLevel is the level a channel shows first in a frame entry: its brightness, the maximum
// for default-on, the first value of a pattern.
func restingLevel(w priv.LEDWrite, maxBrightness int) int {
	switch w.Trigger {
	case "default-on":
		return maxBrightness
	case "timer":
		if w.Brightness > 0 {
			return w.Brightness
		}
		return maxBrightness
	case "pattern":
		n, _ := strconv.Atoi(strings.Fields(w.Pattern)[0])
		return n
	}
	return w.Brightness
}

// FadeMS is how long a cross-fade between two looks takes.
const FadeMS = 300

// WithFade gives each entry of frame whose first level differs from what its LED shows now
// (previous) a one-shot fade from that level to the new one (task 315): the helper runs it before
// the frame. Only over PWM with the pattern trigger; a channel the LED did not have before fades
// from dark.
func WithFade(frame, previous []priv.LEDWrite, r Render) []priv.LEDWrite {
	if !r.animated() || len(previous) == 0 {
		return frame
	}
	out := slices.Clone(frame)
	for i, w := range out {
		from := 0
		for _, p := range previous {
			if p.LED == w.LED {
				from = restingLevel(p, r.MaxBrightness)
			}
		}
		if to := restingLevel(w, r.MaxBrightness); from != to {
			out[i].Fade = fmt.Sprintf("%d %d %d 0", from, FadeMS, to)
		}
	}
	return out
}
