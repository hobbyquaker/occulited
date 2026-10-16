// Package led is the status LED controller (task 95): occulited is the only writer of the
// RPI-RF-MOD's RGB LED after the boot. States of the box are a priority list with CCU-compatible
// defaults, the first active and enabled one wins, and what it shows is one frame of kernel
// triggers, written through the privilege helper only when it changes - a blink keeps running in
// the kernel through a pause or a restart of the daemon.
//
// The boot (S02InitRTC's yellow, eQ3StartNetwork's blue blink) and the recovery system's magenta
// stay the shell scripts'; the controller starts in booting (the same yellow) and takes over once
// the radio is known (task 158), showing interfaces-starting while the interface daemons start.
package led

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The colours: seven names - RedMatic-LED's payloads, which users already know from their flows -
// and, since task 315, any "#rrggbb". On an RPI-RF-MOD on the Pi's header driven over PWM (leds_pwm,
// max_brightness 255) a colour is mixed from its three channels through a gamma curve; where the LED
// is on or off per channel (an older image's gpio-leds, or the radio adapter's own LED - HB-RF-USB,
// HB-RF-USB-2, HB-RF-ETH: rpi_rf_mod_led, which reports max_brightness 255 too, the LED class
// default, but lights any level above 0 fully; the probe treats it as 1, openccu-lite task 326) a
// channel at 128 or more lights, so the names and their shades come out as themselves and a free
// colour as the nearest of the seven.
const (
	Off     = "off"
	Red     = "red"
	Green   = "green"
	Blue    = "blue"
	Yellow  = "yellow"
	Cyan    = "cyan"
	Magenta = "magenta"
	White   = "white"
)

// Colors lists the names in the order the page offers them - the palette's presets.
var Colors = []string{Red, Green, Blue, Yellow, Cyan, Magenta, White, Off}

// named is each name's full channels.
var named = map[string][3]uint8{
	Off:     {},
	Red:     {255, 0, 0},
	Green:   {0, 255, 0},
	Blue:    {0, 0, 255},
	Yellow:  {255, 255, 0},
	Cyan:    {0, 255, 255},
	Magenta: {255, 0, 255},
	White:   {255, 255, 255},
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// RGB is a colour's red, green and blue, 0-255: a name's full channels or the hex value; ok is
// false for anything else.
func RGB(c string) (rgb [3]uint8, ok bool) {
	if v, ok := named[c]; ok {
		return v, true
	}
	if !hexColorRe.MatchString(c) {
		return rgb, false
	}
	for i := range 3 {
		n, _ := strconv.ParseUint(c[1+2*i:3+2*i], 16, 8)
		rgb[i] = uint8(n)
	}
	return rgb, true
}

// Hex is a colour as "#rrggbb" (a name becomes its value); "" for an unknown one.
func Hex(c string) string {
	rgb, ok := RGB(c)
	if !ok {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])
}

// normalizeColor is a colour in its one spelling: a name stays a name, a hex value is lower-cased,
// and one that is exactly a name's value (or black) becomes the name - so a saved "#0000ff" is
// "blue" and compares equal to it wherever the controller compares colours.
func normalizeColor(c string) (string, bool) {
	rgb, ok := RGB(c)
	if !ok {
		return c, false
	}
	for _, n := range Colors {
		if named[n] == rgb {
			return n, true
		}
	}
	return strings.ToLower(c), true
}

// lit is which channels a colour lights at all.
func lit(c string) [3]bool {
	rgb, _ := RGB(c)
	return [3]bool{rgb[0] > 0, rgb[1] > 0, rgb[2] > 0}
}

// The patterns. Severity is in the pattern, the category in the colour: solid and fast are more
// urgent than slow, flash, double and alternate.
const (
	Solid     = "solid"
	Slow      = "slow"      // 500/500 ms, upstream's 499
	Fast      = "fast"      // 100/100 ms
	Flash     = "flash"     // 100/1900 ms, a sign of life
	Double    = "double"    // two 150 ms flashes every 2 s (the pattern trigger)
	Breathe   = "breathe"   // a 2 s pulse from dark to the colour and back (PWM; slow where the LED is on/off)
	Alternate = "alternate" // with a second colour on other channels, the CCU's service message look
)

// Patterns lists them in the order the page offers them.
var Patterns = []string{Solid, Slow, Fast, Flash, Double, Breathe, Alternate}

// OverNormalPatterns are the patterns that can blink over the normal colour (task 134): the ones
// with an off phase and one colour - breathe then pulses between the normal colour and its own.
var OverNormalPatterns = []string{Slow, Fast, Flash, Double, Breathe}

// Look is what the LED shows: a colour, a pattern, and for alternate the second colour.
// OverNormal (task 134, opt-in) makes a blink's off phase show the normal state's colour instead of
// dark; the resolver decides whether it takes effect (Shown.Background).
type Look struct {
	Color      string `json:"color"`
	Pattern    string `json:"pattern"`
	Color2     string `json:"color2,omitempty"`
	OverNormal bool   `json:"over_normal,omitempty"`
}

// The state ids. The fixed ones are above every configurable one and cannot be moved.
const (
	StateShutdown       = "shutdown"
	StateBooting        = "booting"
	StatePreview        = "preview"
	StateLocate         = "locate"
	StateRadioDown      = "radio-down"
	StateNoNetwork      = "no-network"
	StateServiceFailed  = "service-failed"
	StateStorageReplace = "storage-replace"
	// StateInterfacesStarting: one of the interface daemons is starting (task 158)
	StateInterfacesStarting = "interfaces-starting"
	// StateRadioDutyCycle and StateRadioCarrierSense: the radio's load above a configured level
	// (openccu-lite task 316); their look is the active level's (Config.Radio), not the row's
	StateRadioDutyCycle    = "radio-duty-cycle"
	StateRadioCarrierSense = "radio-carrier-sense"
	StateExternal          = "external"
	StateStatusWarning     = "status-warning"
	// StateFirmwareTransfer and StateFirmwareReady (occulited task 57): a device's firmware is being
	// transferred over the air, or has been and waits for its installation - from the firmware
	// service's device states (task 44), no radio traffic of the LED's own
	StateFirmwareTransfer = "firmware-transfer"
	StateFirmwareReady    = "firmware-ready"
	StateSystemUpdate     = "system-update"
	StateAddonUpdate      = "addon-update"
	StateNoInternet       = "no-internet"
	StateNormal           = "normal"
)

// FixedStates are above the list, in this order.
var FixedStates = []string{StateShutdown, StateBooting, StatePreview, StateLocate}

// RadioStates are the rows whose look comes from Config.Radio's levels (task 316): the row carries
// its place and its switch, each level its threshold and look.
var RadioStates = []string{StateRadioDutyCycle, StateRadioCarrierSense}

// IsRadioState reports whether a row is one of RadioStates.
func IsRadioState(id string) bool { return slices.Contains(RadioStates, id) }

// RadioLevel is one threshold of the radio load (task 316): active while the value is above
// Threshold (percent), until it falls to Threshold less RadioHysteresis; shown with its own look
// - over the normal colour (OverNormal, "overlay") or instead of it.
type RadioLevel struct {
	Enabled   bool `json:"enabled"`
	Threshold int  `json:"threshold"`
	Look
}

// RadioConfig is the radio load's levels: three for the duty cycle, three for the carrier
// sense, each list in rising order of its thresholds. The highest active level wins.
type RadioConfig struct {
	DutyCycle    []RadioLevel `json:"duty_cycle"`
	CarrierSense []RadioLevel `json:"carrier_sense"`
}

// RadioHysteresis is how many points a value must fall below a level's threshold before the
// level clears, so the LED does not flap around a threshold.
const RadioHysteresis = 5

// RadioLevelCount is how many levels each kind has.
const RadioLevelCount = 3

// Levels is a radio state's levels.
func (r RadioConfig) Levels(id string) []RadioLevel {
	if id == StateCarrierSenseID() {
		return r.CarrierSense
	}
	return r.DutyCycle
}

// StateCarrierSenseID is StateRadioCarrierSense; a function so Levels reads as a lookup.
func StateCarrierSenseID() string { return StateRadioCarrierSense }

// LevelLook is the look of a radio state's level n (1-based); the row's dark look for none.
func (r RadioConfig) LevelLook(id string, n int) Look {
	levels := r.Levels(id)
	if n < 1 || n > len(levels) {
		return LookDark
	}
	return levels[n-1].Look
}

// RadioLevelOf is the level a value puts a kind at, given the level it is at now (task 316): a
// higher level whose threshold the value exceeds is taken at once; the current level holds until
// the value falls to its threshold less RadioHysteresis, and then the highest level the value
// still exceeds is taken. Levels switched off do not count. 0 = none.
func RadioLevelOf(levels []RadioLevel, value, current int) int {
	highest := func(v int) int {
		l := 0
		for i, lv := range levels {
			if lv.Enabled && v > lv.Threshold {
				l = i + 1
			}
		}
		return l
	}
	if h := highest(value); h > current {
		return h
	}
	if current >= 1 && current <= len(levels) && levels[current-1].Enabled && value > levels[current-1].Threshold-RadioHysteresis {
		return current
	}
	return highest(value)
}

// defaultRadio is the radio load's default levels: off, in amber, orange-red and red - looks an
// on/off LED tells apart too (yellow slow, red slow, red fast), the first two over the normal
// colour, the top one instead of it.
func defaultRadio() RadioConfig {
	levels := func(t1, t2, t3 int) []RadioLevel {
		return []RadioLevel{
			{Threshold: t1, Look: Look{Color: Yellow, Pattern: Breathe, OverNormal: true}},
			{Threshold: t2, Look: Look{Color: "#ff4000", Pattern: Slow, OverNormal: true}},
			{Threshold: t3, Look: Look{Color: Red, Pattern: Fast}},
		}
	}
	return RadioConfig{DutyCycle: levels(25, 50, 75), CarrierSense: levels(5, 10, 20)}
}

// ErrorStates are what night mode's "only errors" still shows, and what lights the red power LED
// on a box without an RGB LED.
var ErrorStates = []string{StateRadioDown, StateServiceFailed, StateStorageReplace}

// The fixed looks: the CCU3 manual's "booting" and a shutdown that never looks fine.
var (
	LookBooting  = Look{Color: Yellow, Pattern: Solid}
	LookShutdown = Look{Color: Yellow, Pattern: Fast}
	LookDark     = Look{Color: Off, Pattern: Solid}
)

// StateConfig is one configurable row: whether it shows, and how. The external row has no look of
// its own - each override brings one - and its position is where overrides rank.
type StateConfig struct {
	ID         string `json:"id"`
	Enabled    bool   `json:"enabled"`
	Color      string `json:"color,omitempty"`
	Pattern    string `json:"pattern,omitempty"`
	Color2     string `json:"color2,omitempty"`
	OverNormal bool   `json:"over_normal,omitempty"`
}

// Look is the row's look.
func (s StateConfig) Look() Look {
	return Look{Color: s.Color, Pattern: s.Pattern, Color2: s.Color2, OverNormal: s.OverNormal}
}

// Night is the night window, in the box's time zone; To before From crosses midnight.
type Night struct {
	Enabled bool   `json:"enabled"`
	From    string `json:"from"`
	To      string `json:"to"`
	// Show is errors (only the error states and the fixed ones), off (only the fixed ones) or
	// dimmed (everything, at Dim - task 315, PWM).
	Show string `json:"show"`
	// Dim is the level, in percent of Brightness, of what the night still shows (task 315; the
	// fixed states, locate and the test are never dimmed). 100 = not dimmed. Ignored on an
	// on/off LED.
	Dim int `json:"dim"`
}

// Locate is the locate action's look and default duration.
type Locate struct {
	Color     string `json:"color"`
	Pattern   string `json:"pattern"`
	DurationS int    `json:"duration_s"`
}

// External bounds the overrides.
type External struct {
	MaxDurationS int `json:"max_duration_s"`
	// AllowUntilCleared lets an override with duration 0 stay until it is cleared.
	AllowUntilCleared bool `json:"allow_until_cleared"`
}

// Config is led.json.
type Config struct {
	Enabled bool `json:"enabled"`
	// Brightness is the LED's level in percent, 10-100 (task 315; PWM only - an on/off LED is
	// always full). The colours' own shades sit below it.
	Brightness int `json:"brightness"`
	// Fade: a look changes with a 300 ms cross-fade instead of a hard switch (task 315; PWM only).
	// nil reads as on.
	Fade   *bool         `json:"fade,omitempty"`
	Normal Look          `json:"normal"`
	Night  Night         `json:"night"`
	States []StateConfig `json:"states"`
	// WarningsOff are the Status page warning ids the status-warning state ignores.
	WarningsOff []string `json:"warnings_off"`
	// AddonUnits: a failed addon unit counts as service-failed too.
	AddonUnits bool     `json:"addon_units"`
	Locate     Locate   `json:"locate"`
	External   External `json:"external"`
	// PWRErrorLight: on a box without an RGB LED, the red power LED blinks while an error state is
	// active.
	PWRErrorLight bool `json:"pwr_error_light"`
	// Radio is the radio load's levels (task 316), shown by the radio-duty-cycle and
	// radio-carrier-sense rows while those are switched on.
	Radio RadioConfig `json:"radio"`
}

// Defaults is the configuration of a box that never saved one: the CCU's meanings where the CCU
// has one.
func Defaults() Config {
	on := true
	return Config{
		Enabled:    true,
		Brightness: 100,
		Fade:       &on,
		Normal:     Look{Color: Blue, Pattern: Solid},
		Night:      Night{From: "22:00", To: "06:30", Show: "errors", Dim: DefaultNightDim},
		States: []StateConfig{
			{ID: StateRadioDown, Enabled: true, Color: Red, Pattern: Solid},
			{ID: StateNoNetwork, Enabled: true, Color: Yellow, Pattern: Fast},
			{ID: StateServiceFailed, Enabled: true, Color: Red, Pattern: Slow},
			{ID: StateStorageReplace, Enabled: true, Color: Red, Pattern: Double},
			// task 158 (maintainer, 2026-09-18): a double magenta blink over the normal colour - not the
			// recovery system's magenta, which is slow, solid or fast over dark; below the errors
			{ID: StateInterfacesStarting, Enabled: true, Color: Magenta, Pattern: Double, OverNormal: true},
			// task 316: the radio load, off by default, below the failures and above the overrides
			{ID: StateRadioDutyCycle, Enabled: false},
			{ID: StateRadioCarrierSense, Enabled: false},
			{ID: StateExternal, Enabled: true},
			{ID: StateStatusWarning, Enabled: true, Color: Yellow, Pattern: Slow},
			// occulited task 57: below every fault and warning, off by default as task 316's optional
			// rows; a transfer above a firmware waiting for its install
			{ID: StateFirmwareTransfer, Enabled: false, Color: Blue, Pattern: Breathe},
			{ID: StateFirmwareReady, Enabled: false, Color: Green, Pattern: Double},
			{ID: StateSystemUpdate, Enabled: true, Color: Cyan, Pattern: Slow},
			{ID: StateAddonUpdate, Enabled: false, Color: Cyan, Pattern: Double},
			{ID: StateNoInternet, Enabled: false, Color: Blue, Pattern: Fast},
		},
		WarningsOff: []string{},
		Locate:      Locate{Color: White, Pattern: Fast, DurationS: 300},
		External:    External{MaxDurationS: 3600, AllowUntilCleared: true},
		Radio:       defaultRadio(),
	}
}

// DefaultNightDim is the night's level in percent when a file or a request does not say.
const DefaultNightDim = 30

// Fades reports whether looks cross-fade (Fade nil = on).
func (c Config) Fades() bool { return c.Fade == nil || *c.Fade }

// ErrInvalid wraps every refusal of a configuration or a look; the API answers 422.
var ErrInvalid = errors.New("invalid")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Disjoint reports whether two colours share no channel - the pairs alternate can blink on an
// on/off LED, where the two colours' timers run in opposite phases.
func Disjoint(a, b string) bool {
	ca, cb := lit(a), lit(b)
	for i := range ca {
		if ca[i] && cb[i] {
			return false
		}
	}
	return true
}

// NormalizeLook checks a look and returns it in its one spelling: off is always solid, a second
// colour only with alternate, alternate only with two disjoint colours that both light, and
// over_normal only with one of OverNormalPatterns (dropped, not refused, elsewhere - an older page
// or a flow that keeps the flag while changing the pattern is not an error).
func NormalizeLook(l Look) (Look, error) {
	c, ok := normalizeColor(l.Color)
	if !ok {
		return l, invalid("colour %q", l.Color)
	}
	l.Color = c
	if l.Pattern == "" {
		l.Pattern = Solid
	}
	if !slices.Contains(Patterns, l.Pattern) {
		return l, invalid("pattern %q", l.Pattern)
	}
	if l.Color == Off {
		return LookDark, nil
	}
	if !slices.Contains(OverNormalPatterns, l.Pattern) {
		l.OverNormal = false
	}
	if l.Pattern != Alternate {
		l.Color2 = ""
		return l, nil
	}
	c2, ok := normalizeColor(l.Color2)
	if !ok || c2 == Off {
		return l, invalid("alternate needs a second colour")
	}
	l.Color2 = c2
	if !Disjoint(l.Color, l.Color2) {
		return l, invalid("%s and %s share a channel and cannot alternate", l.Color, l.Color2)
	}
	return l, nil
}

var (
	hhmmRe      = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	warningIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
)

// minutesOf is an HH:MM in minutes after midnight.
func minutesOf(hhmm string) int {
	h, _ := strconv.Atoi(hhmm[:2])
	m, _ := strconv.Atoi(hhmm[3:])
	return h*60 + m
}

// Validate checks a configuration and returns it normalised. A row the stored or sent list lacks -
// a state a later version added - is put in at its default position, after the default row before
// it; an unknown or repeated id is refused.
func Validate(c Config) (Config, error) {
	var err error
	if c.Normal, err = NormalizeLook(c.Normal); err != nil {
		return c, fmt.Errorf("normal: %w", err)
	}
	c.Normal.OverNormal = false // normal is the background itself
	if c.Brightness == 0 {
		c.Brightness = 100 // a file from before task 315
	}
	if c.Brightness < 10 || c.Brightness > 100 {
		return c, invalid("brightness is 10 to 100")
	}
	if c.Fade == nil {
		on := true
		c.Fade = &on
	}
	if !hhmmRe.MatchString(c.Night.From) || !hhmmRe.MatchString(c.Night.To) {
		return c, invalid("night: from and to are HH:MM")
	}
	if c.Night.Show != "errors" && c.Night.Show != "off" && c.Night.Show != "dimmed" {
		return c, invalid("night: show is errors, off or dimmed")
	}
	if c.Night.Dim == 0 {
		c.Night.Dim = DefaultNightDim
	}
	if c.Night.Dim < 1 || c.Night.Dim > 100 {
		return c, invalid("night: dim is 1 to 100")
	}
	if l, err := NormalizeLook(Look{Color: c.Locate.Color, Pattern: c.Locate.Pattern}); err != nil {
		return c, fmt.Errorf("locate: %w", err)
	} else if l.Color == Off || l.Pattern == Alternate {
		return c, invalid("locate: a colour that lights and a pattern of one colour")
	} else {
		c.Locate.Color, c.Locate.Pattern = l.Color, l.Pattern
	}
	if c.Locate.DurationS < 10 || c.Locate.DurationS > 3600 {
		return c, invalid("locate: duration_s is 10 to 3600")
	}
	if c.External.MaxDurationS < 60 || c.External.MaxDurationS > 86400 {
		return c, invalid("external: max_duration_s is 60 to 86400")
	}
	defaults := Defaults().States
	known := map[string]StateConfig{}
	for _, d := range defaults {
		known[d.ID] = d
	}
	seen := map[string]bool{}
	states := make([]StateConfig, 0, len(defaults))
	for _, s := range c.States {
		d, ok := known[s.ID]
		if !ok {
			return c, invalid("unknown state %q", s.ID)
		}
		if seen[s.ID] {
			return c, invalid("state %q twice", s.ID)
		}
		seen[s.ID] = true
		if s.ID == StateExternal || IsRadioState(s.ID) {
			states = append(states, StateConfig{ID: s.ID, Enabled: s.Enabled})
			continue
		}
		if s.Color == "" {
			s.Color, s.Pattern, s.Color2, s.OverNormal = d.Color, d.Pattern, d.Color2, d.OverNormal
		}
		l, err := NormalizeLook(s.Look())
		if err != nil {
			return c, fmt.Errorf("%s: %w", s.ID, err)
		}
		s.Color, s.Pattern, s.Color2, s.OverNormal = l.Color, l.Pattern, l.Color2, l.OverNormal
		states = append(states, s)
	}
	for i, d := range defaults {
		if seen[d.ID] {
			continue
		}
		at := 0
		for j := i - 1; j >= 0; j-- {
			if k := slices.IndexFunc(states, func(s StateConfig) bool { return s.ID == defaults[j].ID }); k >= 0 {
				at = k + 1
				break
			}
		}
		states = slices.Insert(states, at, d)
		seen[d.ID] = true
	}
	c.States = states
	// task 316: three levels per kind, thresholds rising within 1-99, each look lighting; a file
	// from before the levels (none at all) gets the defaults
	d := defaultRadio()
	if len(c.Radio.DutyCycle) == 0 && len(c.Radio.CarrierSense) == 0 {
		c.Radio = d
	}
	for _, kind := range []struct {
		name   string
		levels *[]RadioLevel
	}{{"duty_cycle", &c.Radio.DutyCycle}, {"carrier_sense", &c.Radio.CarrierSense}} {
		if len(*kind.levels) != RadioLevelCount {
			return c, invalid("radio.%s: %d levels", kind.name, RadioLevelCount)
		}
		last := 0
		for i := range *kind.levels {
			lv := &(*kind.levels)[i]
			if lv.Threshold <= last || lv.Threshold > 99 {
				return c, invalid("radio.%s: thresholds rise within 1-99", kind.name)
			}
			last = lv.Threshold
			l, err := NormalizeLook(lv.Look)
			if err != nil {
				return c, fmt.Errorf("radio.%s level %d: %w", kind.name, i+1, err)
			}
			if l.Color == Off {
				return c, invalid("radio.%s level %d: a colour that lights", kind.name, i+1)
			}
			lv.Look = l
		}
	}
	off := []string{}
	for _, id := range c.WarningsOff {
		id = strings.TrimSpace(id)
		if !warningIDRe.MatchString(id) {
			return c, invalid("warning id %q", id)
		}
		if !slices.Contains(off, id) {
			off = append(off, id)
		}
	}
	c.WarningsOff = off
	return c, nil
}

// State returns the row of an id.
func (c Config) State(id string) (StateConfig, bool) {
	for _, s := range c.States {
		if s.ID == id {
			return s, true
		}
	}
	return StateConfig{}, false
}
