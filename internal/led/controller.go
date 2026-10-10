package led

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// The controller's timing.
const (
	tickEvery      = time.Second
	refreshEvery   = 10 * time.Second // units, files, warnings, updates
	probeEvery     = time.Minute      // the hardware, once the detection has run
	readBackEvery  = 10 * time.Second
	errorHold      = 15 * time.Second // an error state must hold this long: a unit restarted by hand is not red
	bootLimit      = 15 * time.Minute // uptime after which the box counts as up whatever the files say
	framesPerSec   = 5
	requestsPerSec = 2
	maxOverrides   = 16
	previewFor     = 10 * time.Second
	internetEvery  = 5 * time.Minute
	writeRetry     = 30 * time.Second
)

// radioUnits are the units whose failure is radio-down; the order is the boot order. hs485d joins
// them where the radio plan runs it (its activation marker: a wired gateway is configured), see
// radioDownUnits; elsewhere a failed hs485d is service-failed.
var radioUnits = []string{"multimacd", "rfd", "hmipserver"}

// wiredUnit is the BidCos-Wired daemon, radio-down only where the plan runs it.
const wiredUnit = "hs485d"

// interfaceUnits are the units whose start is interfaces-starting (task 158): the radio's and
// hs485d, in the boot order.
var interfaceUnits = []string{"multimacd", "rfd", "hmipserver", "hs485d"}

// radioKnown is the file the radio run writes last before the units' activation markers: once it
// is there the radio is known, and the controller leaves booting (task 158). The directory alone
// says nothing - the run's lock creates it before the detection.
const radioKnown = "render.json"

// radioRunDir holds the radio run's activation markers (internal/radio's RunDir): <unit>.enabled
// is there when the plan runs the daemon, and it is the unit's own ConditionPathExists=. With the
// directory there and a unit's marker absent, the radio configuration does not need that unit
// (B-149: multimacd off with the modules taken directly), and it is not down. Without the
// directory (the run has not finished, or an image without it) every loaded unit counts.
const radioRunDir = "run/occulite/radio"

// radioDownUnits are the units whose failure is radio-down on this box now: radioUnits, and hs485d
// when the radio plan runs it - a wired gateway is configured, so BidCos-Wired is part of the radio
// the user expects to work. Without its marker (no wired gateway, or no plan yet) it is not.
func (c *Controller) radioDownUnits() []string {
	if c.exists(radioRunDir, wiredUnit+".enabled") {
		return append(slices.Clone(radioUnits), wiredUnit)
	}
	return radioUnits
}

// transientDir is where systemd keeps the unit files of transient units: the ones systemd-run
// makes, under their own --unit= name as well as its run-<id> ones.
const transientDir = "run/systemd/transient"

// transient says whether a unit is a transient one that systemd-run made (openccu-lite B-276): a
// person or a script at the shell started it, not the system, so its failure is not the system's
// error. Without --collect a failed one stays failed until reset-failed, and it held the LED red
// for an hour after a lab check that was meant to fail. run-<id>.service and .scope are
// systemd-run's own names; a named one is known by its unit file in transientDir.
func (c *Controller) transient(unit string) bool {
	if strings.HasPrefix(unit, "run-") && (strings.HasSuffix(unit, ".service") || strings.HasSuffix(unit, ".scope")) {
		return true
	}
	return unit != "" && !strings.Contains(unit, "/") && c.exists(transientDir, unit)
}

// heldStates are the error states that must hold for errorHold before they show.
var heldStates = []string{StateRadioDown, StateServiceFailed, StateNoNetwork}

// The refusals of the API's writes.
var (
	ErrUnavailable = errors.New("this system has no status LED")
	ErrRateLimited = errors.New("too many requests, at most 2 per second")
	ErrLimit       = errors.New("too many overrides, at most 16")
	ErrForbidden   = errors.New("the override is another caller's")
	ErrNotFound    = errors.New("no such override")
)

// Sources are what the states read besides the files under the root. A nil function is a source
// this box does not have, and its states stay off.
type Sources struct {
	// Units lists the service units (systemctl list-units); nil off systemd.
	Units func(ctx context.Context) ([]system.UnitRow, error)
	// Warnings are the Status page's warnings nobody silenced.
	Warnings func() []warnings.Warning
	// SystemUpdate is the newer release on offer, "" for none.
	SystemUpdate func() string
	// AddonUpdates is how many addons have an update.
	AddonUpdates func() int
	// Internet answers whether the internet is reachable; nil = checkInternet's way, a TCP
	// connection to InternetTargets (skipped with /etc/config/internetCheckDisabled).
	Internet func(ctx context.Context) bool
	// InternetTargets are the host:port the check connects to, in order - hosts the box talks to
	// anyway (task 95, D-67): the addon catalogue index's, then the ACME directory's when ACME is
	// configured. nil or none = nothing to check against, which counts as reachable.
	InternetTargets func() []string
	// WarningIDs are the warning ids the page offers to leave off the LED.
	WarningIDs []string
	// Radio is the radio interfaces' load as the radio sampler last saw it (task 316): the Status
	// page's and the sparklines' sampling, nothing of the LED's own. nil = no radio levels.
	Radio func() []RadioLoad
}

// RadioLoad is one interface's load (task 316): the duty cycle, and the carrier sense where the
// interface reports one.
type RadioLoad struct {
	Interface    string
	DutyCycle    int
	CarrierSense int
	HasCS        bool
}

// Controller is the status LED's owner.
type Controller struct {
	Root system.Root
	// File is led.json in occulited's state directory.
	File string
	Src  Sources
	Log  *slog.Logger
	// Write applies a frame; nil = the privilege helper under Root's /sys/class/leds.
	Write func(frame []priv.LEDWrite) error
	// LoadModule loads a kernel module the LED needs (the pattern trigger, openccu-lite B-299);
	// nil = the privilege helper's LoadLEDModule.
	LoadModule func(name string) error
	// Now is the clock; nil = time.Now.
	Now func() time.Time
	// Dial connects the internet check; nil = a net.Dialer with internetTimeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	stepMu sync.Mutex // one step at a time: the loop and Stop
	once   sync.Once
	kick   chan struct{}

	mu          sync.Mutex
	loaded      bool
	cfg         Config
	hw          Hardware
	probedAt    time.Time
	refreshedAt time.Time
	loc         *time.Location
	active      map[string]Activity
	pending     map[string]time.Time
	// radioLevels is each radio state's level now (task 316), for the hysteresis
	radioLevels map[string]int
	overrides   map[string]Override
	requests    map[string][]time.Time
	locate      *Timed
	preview     *Timed

	sawFinished, booted, stopping, shutdown, booting bool
	// unitsStirred: one of interfaceUnits has left inactive - the fallback for leaving booting on
	// a box whose radio run leaves no radioKnown (task 158)
	unitsStirred bool

	shown      Shown
	shownSince time.Time
	applied    []priv.LEDWrite // what is on the LED, as far as the controller knows
	lastOwn    []priv.LEDWrite // the frame the controller last wanted
	writes     []time.Time
	retryAt    time.Time
	writeErr   string
	noPattern  bool
	moduleErr  string

	conflict, conflictLogged, hold bool
	diffs                          []time.Time
	readAt                         time.Time

	pwrOn bool

	internet     bool
	internetAt   time.Time
	internetBusy bool
}

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Controller) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Controller) join(p ...string) string {
	return filepath.Join(append([]string{string(c.Root)}, p...)...)
}

func (c *Controller) exists(p ...string) bool {
	_, err := os.Stat(c.join(p...))
	return err == nil
}

func (c *Controller) kickCh() chan struct{} {
	c.once.Do(func() { c.kick = make(chan struct{}, 1) })
	return c.kick
}

// poke has the loop step now rather than at its next tick.
func (c *Controller) poke() {
	select {
	case c.kickCh() <- struct{}{}:
	default:
	}
}

// load reads led.json once; the caller holds mu. A missing file is the defaults - and switched
// off when hss_led's /etc/config/disableLED is there, which is what that box's owner chose.
func (c *Controller) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.active, c.pending, c.overrides, c.requests = map[string]Activity{}, map[string]time.Time{}, map[string]Override{}, map[string][]time.Time{}
	c.cfg = Defaults()
	b, err := os.ReadFile(c.File)
	switch {
	case err == nil:
		stored := Defaults()
		if jerr := json.Unmarshal(b, &stored); jerr != nil {
			c.log().Warn("status LED: led.json is not valid JSON, the defaults apply", "file", c.File, "err", jerr)
		} else if v, verr := Validate(stored); verr != nil {
			c.log().Warn("status LED: led.json is not a valid configuration, the defaults apply", "file", c.File, "err", verr)
		} else {
			c.cfg = v
		}
	case errors.Is(err, os.ErrNotExist):
		if c.exists("etc/config/disableLED") {
			c.cfg.Enabled = false
			c.log().Info("status LED: /etc/config/disableLED is there, the LED starts switched off")
		}
	default:
		c.log().Warn("status LED: led.json cannot be read, the defaults apply", "file", c.File, "err", err)
	}
}

// save writes led.json atomically, 0600, in occulited's own state directory.
func (c *Controller) save(cfg Config) error {
	if c.File == "" {
		return nil
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.File)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(c.File)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(append(b, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, c.File)
	}
	if werr != nil {
		_ = os.Remove(name)
	}
	return werr
}

// Run steps every second, and at once when something changed, until ctx ends.
func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		c.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.kickCh():
		}
	}
}

// Stop shows the shutdown pattern and waits until it is written: occulited is stopping, and a box
// going down never looks fine.
func (c *Controller) Stop() {
	c.mu.Lock()
	c.stopping = true
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.step(ctx)
}

// uptime is the time since boot; 0 when unknown.
func (c *Controller) uptime() time.Duration {
	b, err := os.ReadFile(c.join("proc/uptime"))
	if err != nil {
		return 0
	}
	f, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	s, err := strconv.ParseFloat(f, 64)
	if err != nil {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}

// location is the box's time zone, for the night window: the zone name updateTZ.sh keeps.
func (c *Controller) location() *time.Location {
	b, _ := os.ReadFile(c.join("etc/config/timezone"))
	if name := strings.TrimSpace(string(b)); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

// step is one round: read what is due, decide, write what changed, read back.
func (c *Controller) step(ctx context.Context) {
	c.stepMu.Lock()
	defer c.stepMu.Unlock()
	now := c.now()

	c.mu.Lock()
	c.load()
	refresh := c.refreshedAt.IsZero() || now.Sub(c.refreshedAt) >= refreshEvery
	probe := c.probedAt.IsZero() || now.Sub(c.probedAt) >= probeEvery || (!c.hw.Final && now.Sub(c.probedAt) >= refreshEvery)
	c.mu.Unlock()

	// the slow reads happen outside the lock: systemctl goes through the helper
	var hw *Hardware
	if probe {
		h := Probe(c.Root, c.loadModule)
		hw = &h
	}
	var (
		rows    []system.UnitRow
		unitsOK bool
		warns   []warnings.Warning
		update  string
		addons  int
		radio   []RadioLoad
		loc     *time.Location
	)
	if refresh {
		if c.Src.Units != nil {
			var err error
			rows, err = c.Src.Units(ctx)
			unitsOK = err == nil
		}
		if c.Src.Warnings != nil {
			warns = c.Src.Warnings()
		}
		if c.Src.SystemUpdate != nil {
			update = c.Src.SystemUpdate()
		}
		if c.Src.AddonUpdates != nil {
			addons = c.Src.AddonUpdates()
		}
		if c.Src.Radio != nil {
			radio = c.Src.Radio()
		}
		loc = c.location()
	}

	c.mu.Lock()
	if hw != nil {
		if hw.Available != c.hw.Available || hw.Reason != c.hw.Reason {
			c.log().Info("status LED: hardware", "available", hw.Available, "reason", hw.Reason, "kind", hw.Kind, "pattern_trigger", hw.PatternTrigger, "max_brightness", hw.MaxBrightness)
		}
		c.hw, c.probedAt = *hw, now
	}
	if refresh {
		c.refreshedAt, c.loc = now, loc
		c.evaluate(now, rows, unitsOK, warns, update, addons, radio)
	}
	finished := c.exists("var/status/startupFinished")
	if finished {
		c.sawFinished = true
	}
	c.shutdown = c.stopping || (c.sawFinished && !finished)
	// task 158: booting ends when the radio is known, not when the boot has finished - from then
	// on the states show, interfaces-starting first. A finished boot (occulited restarted later, a
	// box without units to read) and the uptime limit are the backstops.
	if !c.booted && !c.shutdown && (c.exists(radioRunDir, radioKnown) || c.unitsStirred || finished || c.uptime() >= bootLimit) {
		c.booted = true
		c.log().Info("status LED: the radio is known, the states take over from booting")
	}
	c.booting = !c.booted && !c.shutdown
	c.expire(now)
	in := c.inputs(now)
	shown := Resolve(in)
	if shown != c.shown {
		c.shown, c.shownSince = shown, now
	}
	render := c.render(shown)
	frame := FrameFor(shown.Look, shown.Background, render)
	var write, written []priv.LEDWrite
	if c.hw.Available && !c.hw.StandDown && !slices.Equal(frame, c.applied) && (c.stopping || (now.After(c.retryAt) && c.rateOK(now))) {
		written = frame
		write = frame
		// task 315: a cross-fade from what the LED shows now, unless the box is going down
		if c.cfg.Fades() && !c.stopping {
			write = WithFade(frame, c.applied, render)
		}
	}
	// task 34: no read-back while the system goes down - upstream's S99SetupLEDs stop writes its
	// shutdown pattern then, and setting ours again over it was a fight and a warning at every
	// shutdown (openccu-lite task 345, row 7); our own shutdown frame is written once, as any change
	readBack := write == nil && c.hw.Available && c.applied != nil && !c.hold && !c.shutdown && now.Sub(c.readAt) >= readBackEvery
	pwr, pwrOn := c.pwrFrame(in)
	checkInternet := c.wantInternet(now)
	c.mu.Unlock()

	if write != nil {
		err := c.write(write)
		c.mu.Lock()
		c.written(written, err, now)
		c.mu.Unlock()
	}
	if readBack {
		c.readBack(now)
	}
	if pwr != nil {
		err := c.write(pwr)
		c.mu.Lock()
		if err == nil {
			c.pwrOn = pwrOn
		} else {
			c.writeFailed(err, now)
		}
		c.mu.Unlock()
	}
	if checkInternet {
		go c.checkInternet(ctx)
	}
}

// render is what the renderer needs for a look (task 315): the hardware's levels and the pattern
// trigger, the configured brightness, the night's dimming of what is shown. The caller holds mu.
func (c *Controller) render(shown Shown) Render {
	return Render{PatternTrigger: c.hw.PatternTrigger && !c.noPattern, MaxBrightness: max(c.hw.MaxBrightness, 1), Brightness: c.cfg.Brightness, Dim: shown.Dim}
}

// loadModule is Probe's loader: the hook, or the helper. A failure is logged once per message -
// a kernel without the module answers the same every minute.
func (c *Controller) loadModule(name string) error {
	var err error
	if c.LoadModule != nil {
		err = c.LoadModule(name)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err = system.Priv.LoadLEDModule(ctx, name)
	}
	if err != nil {
		c.mu.Lock()
		logged := c.moduleErr == err.Error()
		c.moduleErr = err.Error()
		c.mu.Unlock()
		if !logged {
			c.log().Warn("status LED: the kernel module cannot be loaded, double is shown as flash", "module", name, "err", err)
		}
	}
	return err
}

func (c *Controller) write(frame []priv.LEDWrite) error {
	if c.Write != nil {
		return c.Write(frame)
	}
	return system.Priv.WriteLEDs(c.join("sys/class/leds"), frame)
}

// rateOK: at most framesPerSec frames in the last second; the rest waits for the next tick, where the
// newest decision is written (a flapping automation does not become a strobe).
func (c *Controller) rateOK(now time.Time) bool {
	c.writes = slices.DeleteFunc(c.writes, func(t time.Time) bool { return now.Sub(t) >= time.Second })
	return len(c.writes) < framesPerSec
}

// written records a frame's write; the caller holds mu.
func (c *Controller) written(frame []priv.LEDWrite, err error, now time.Time) {
	c.writes = append(c.writes, now)
	if err != nil {
		if errors.Is(err, priv.ErrLEDPattern) && !c.noPattern {
			c.noPattern = true
			c.log().Warn("status LED: the kernel's pattern trigger is not available, double is shown as flash", "err", err)
			return
		}
		c.writeFailed(err, now)
		return
	}
	if c.writeErr != "" {
		c.log().Info("status LED: written again after a failure")
	}
	c.writeErr, c.retryAt = "", time.Time{}
	if !slices.Equal(frame, c.lastOwn) {
		// a change of the controller's own: whoever kept changing the LED gets another chance
		c.hold, c.conflict, c.diffs = false, false, nil
	}
	c.applied, c.lastOwn = frame, frame
}

// writeFailed logs a failure once per message and waits before the next attempt; the caller holds mu.
func (c *Controller) writeFailed(err error, now time.Time) {
	if msg := err.Error(); msg != c.writeErr {
		c.writeErr = msg
		c.log().Warn("status LED: the LED could not be written", "err", err)
	}
	c.retryAt = now.Add(writeRetry)
}

var activeTriggerRe = regexp.MustCompile(`\[([^\]]+)\]`)

// activeTrigger is the bracketed entry of a trigger attribute ("none [timer] heartbeat").
func activeTrigger(s string) string {
	if m := activeTriggerRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return strings.TrimSpace(s)
}

// readBack compares the LED with the frame, as hss_led did: a difference is written again once and
// logged once per start; the third within a minute stops the rewriting until the controller's own
// next change - another writer gets a message on the page, not a strobe.
func (c *Controller) readBack(now time.Time) {
	c.mu.Lock()
	frame := c.applied
	c.readAt = now
	c.mu.Unlock()
	diff := ""
	for _, w := range frame {
		b, err := os.ReadFile(c.join("sys/class/leds", w.LED, "trigger"))
		if err != nil {
			continue
		}
		if got := activeTrigger(string(b)); got != w.Trigger {
			diff = w.LED + ": trigger " + got
			break
		}
		if w.Trigger == "none" {
			if br, err := os.ReadFile(c.join("sys/class/leds", w.LED, "brightness")); err == nil {
				if v := strings.TrimSpace(string(br)); v != "" && v != strconv.Itoa(w.Brightness) {
					diff = w.LED + ": brightness " + v
					break
				}
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !slices.Equal(frame, c.applied) {
		return // written meanwhile
	}
	if diff == "" {
		c.conflict = false
		return
	}
	c.conflict = true
	c.diffs = append(slices.DeleteFunc(c.diffs, func(t time.Time) bool { return now.Sub(t) > time.Minute }), now)
	if !c.conflictLogged {
		c.conflictLogged = true
		c.log().Warn("status LED: changed by another process, set again", "change", diff)
	}
	if len(c.diffs) >= 3 {
		c.hold = true
		c.log().Warn("status LED: another process keeps changing it; it is not set again until the controller changes it itself", "change", diff)
		return
	}
	c.applied = nil
}

// evaluate turns this refresh's readings into the active states; the caller holds mu. A source that
// did not answer keeps what its states were (unknown is not fine).
func (c *Controller) evaluate(now time.Time, rows []system.UnitRow, unitsOK bool, warns []warnings.Warning, update string, addons int, radio []RadioLoad) {
	cond := map[string]string{}
	known := map[string]bool{}
	if c.exists("var/status") {
		known[StateNoNetwork] = true
		if !c.exists("var/status/hasLink") || !c.exists("var/status/hasIP") {
			cond[StateNoNetwork] = ""
		}
		if st, ok := c.cfg.State(StateNoInternet); ok && st.Enabled && c.exists("var/status/hasIP") && !c.internetAt.IsZero() {
			known[StateNoInternet] = true
			if !c.internet {
				cond[StateNoInternet] = ""
			}
		} else {
			known[StateNoInternet] = true
		}
	}
	if unitsOK {
		known[StateRadioDown], known[StateServiceFailed], known[StateInterfacesStarting] = true, true, true
		byUnit := map[string]system.UnitRow{}
		for _, r := range rows {
			byUnit[r.Unit] = r
		}
		var down, starting []string
		planned := c.exists(radioRunDir)
		radio := c.radioDownUnits()
		// until the boot has finished, a unit that has not started yet waits for its turn (rfd
		// after multimacd, hmipserver after rfd): one the plan runs is starting, any other is left
		// alone - it is not down before its start was even tried (task 158)
		early := !c.sawFinished && c.uptime() < bootLimit
		for _, u := range interfaceUnits {
			r, ok := byUnit[u+".service"]
			if !ok || r.Load != "loaded" {
				continue // not on this box, or switched off on the Services page (masked)
			}
			if r.Active != "inactive" {
				c.unitsStirred = true
			}
			if planned && !c.exists(radioRunDir, u+".enabled") {
				continue // not needed by the radio configuration (B-149)
			}
			switch r.Active {
			case "active", "reloading":
			case "activating":
				// a unit waiting to be restarted is down; one that is starting is interfaces-starting,
				// at the boot and after (a restart on the Services page, a changed configuration)
				if strings.HasPrefix(r.Sub, "auto-restart") {
					down = append(down, u)
				} else {
					starting = append(starting, u)
				}
			case "inactive":
				switch {
				case early && planned:
					starting = append(starting, u)
				case !early:
					down = append(down, u)
				}
			default:
				down = append(down, u)
			}
		}
		down = slices.DeleteFunc(down, func(u string) bool { return !slices.Contains(radio, u) })
		if len(down) > 0 {
			cond[StateRadioDown] = strings.Join(down, ", ")
		}
		if len(starting) > 0 {
			cond[StateInterfacesStarting] = strings.Join(starting, ", ")
		}
		var failed []string
		for _, r := range rows {
			id := strings.TrimSuffix(r.Unit, ".service")
			if r.Active != "failed" || slices.Contains(radio, id) || (strings.HasPrefix(id, "addon-") && !c.cfg.AddonUnits) || c.transient(r.Unit) {
				continue
			}
			failed = append(failed, id)
		}
		sort.Strings(failed)
		if len(failed) > 0 {
			cond[StateServiceFailed] = strings.Join(failed, ", ")
		}
	}
	if c.Src.Warnings != nil {
		known[StateStatusWarning], known[StateStorageReplace] = true, true
		var ids, loops []string
		for _, w := range warns {
			if w.ID == "storage" && w.Variant == system.StorageReplace {
				cond[StateStorageReplace] = ""
				continue
			}
			if w.ID == "crash-loop" {
				// openccu-lite task 283: a unit in a crash loop is an error, as a failed one is -
				// systemd's backoff never lets it reach the failed state
				for _, u := range strings.Split(w.Variant, ",") {
					if u != "" && !slices.Contains(loops, u) {
						loops = append(loops, u)
					}
				}
				continue
			}
			if !slices.Contains(c.cfg.WarningsOff, w.ID) && !slices.Contains(ids, w.ID) {
				ids = append(ids, w.ID)
			}
		}
		if len(ids) > 0 {
			cond[StateStatusWarning] = strings.Join(ids, ", ")
		}
		if len(loops) > 0 {
			known[StateServiceFailed] = true
			all := loops
			if prev, ok := cond[StateServiceFailed]; ok && prev != "" {
				all = append(strings.Split(prev, ", "), loops...)
			}
			slices.Sort(all)
			cond[StateServiceFailed] = strings.Join(slices.Compact(all), ", ")
		}
	}
	levels := map[string]int{}
	if c.Src.Radio != nil {
		// task 316: the radio load's levels, with hysteresis, the highest wins; the detail names
		// the interface with the highest value
		known[StateRadioDutyCycle], known[StateRadioCarrierSense] = true, true
		if c.radioLevels == nil {
			c.radioLevels = map[string]int{}
		}
		for _, id := range RadioStates {
			value, iface, have := -1, "", false
			for _, l := range radio {
				v, ok := l.DutyCycle, true
				if id == StateRadioCarrierSense {
					v, ok = l.CarrierSense, l.HasCS
				}
				if ok && (!have || v > value) {
					value, iface, have = v, l.Interface, true
				}
			}
			lv := 0
			if have {
				lv = RadioLevelOf(c.cfg.Radio.Levels(id), value, c.radioLevels[id])
			}
			c.radioLevels[id] = lv
			if lv > 0 {
				cond[id] = fmt.Sprintf("%s %d %%", iface, value)
				levels[id] = lv
			}
		}
	}
	if c.Src.SystemUpdate != nil {
		known[StateSystemUpdate] = true
		if update != "" {
			cond[StateSystemUpdate] = update
		}
	}
	if c.Src.AddonUpdates != nil {
		known[StateAddonUpdate] = true
		if addons > 0 {
			cond[StateAddonUpdate] = strconv.Itoa(addons)
		}
	}
	for id := range known {
		detail, on := cond[id]
		if !on {
			if _, was := c.active[id]; was && slices.Contains(ErrorStates, id) {
				c.log().Info("status LED: cleared", "state", id)
			}
			delete(c.active, id)
			delete(c.pending, id)
			continue
		}
		if a, ok := c.active[id]; ok {
			a.Detail, a.Level = detail, levels[id]
			c.active[id] = a
			continue
		}
		since := now
		if slices.Contains(heldStates, id) {
			first, ok := c.pending[id]
			if !ok {
				c.pending[id] = now
				continue
			}
			if now.Sub(first) < errorHold {
				continue
			}
			since = first
			delete(c.pending, id)
		}
		c.active[id] = Activity{Since: since, Detail: detail, Level: levels[id]}
		if slices.Contains(ErrorStates, id) {
			c.log().Warn("status LED: "+id, "detail", detail)
		}
	}
}

// expire drops what has run out; the caller holds mu.
func (c *Controller) expire(now time.Time) {
	for id, o := range c.overrides {
		if o.Until != nil && !now.Before(*o.Until) {
			delete(c.overrides, id)
		}
	}
	if c.locate != nil && !now.Before(c.locate.Until) {
		c.locate = nil
	}
	if c.preview != nil && !now.Before(c.preview.Until) {
		c.preview = nil
	}
}

// inputs are the resolver's; the caller holds mu.
func (c *Controller) inputs(now time.Time) Inputs {
	list := make([]Override, 0, len(c.overrides))
	for _, o := range c.overrides {
		list = append(list, o)
	}
	active := make(map[string]Activity, len(c.active))
	if c.booted {
		// nothing but booting before the box is up: a normal start is never red
		for id, a := range c.active {
			active[id] = a
		}
	}
	return Inputs{Now: now, Location: c.loc, Config: c.cfg, Shutdown: c.shutdown, Booting: c.booting, Preview: c.preview, Locate: c.locate, Active: active, Overrides: list}
}

// pwrFrame is what the red power LED needs, on a box without an RGB LED whose owner made it the
// error light: blinking fast while an enabled error state shows, its normal trigger again after.
// nil when nothing changes; the caller holds mu.
func (c *Controller) pwrFrame(in Inputs) ([]priv.LEDWrite, bool) {
	if !c.hw.PWR || c.hw.Available || c.hw.StandDown || !c.hw.Final || in.Now.Before(c.retryAt) {
		return nil, c.pwrOn
	}
	want := false
	if c.cfg.PWRErrorLight && c.booted && !c.shutdown {
		night := NightActive(c.cfg.Night, in.Now, in.Location)
		for _, id := range ErrorStates {
			st, ok := c.cfg.State(id)
			if _, active := in.Active[id]; ok && st.Enabled && active && !(night && c.cfg.Night.Show == "off") {
				want = true
			}
		}
	}
	switch {
	case want && !c.pwrOn:
		return []priv.LEDWrite{{LED: PWRLED, Trigger: "timer", DelayOn: 100, DelayOff: 100}}, true
	case !want && c.pwrOn:
		normal := c.hw.PWRNormal
		if c.exists("etc/config/disableOnboardLED") || normal == "" {
			normal = "none"
		}
		if !slices.Contains(priv.LEDTriggers, normal) || normal == "timer" || normal == "pattern" {
			normal = "default-on"
		}
		return []priv.LEDWrite{{LED: PWRLED, Trigger: normal}}, false
	}
	return nil, c.pwrOn
}

// wantInternet: the no-internet state is switched on, the box has an address, and the last check is
// old; the caller holds mu.
func (c *Controller) wantInternet(now time.Time) bool {
	st, ok := c.cfg.State(StateNoInternet)
	if !ok || !st.Enabled || !c.booted || c.internetBusy || !c.exists("var/status/hasIP") {
		return false
	}
	if !c.internetAt.IsZero() && now.Sub(c.internetAt) < internetEvery {
		return false
	}
	c.internetBusy = true
	return true
}

// internetTimeout is how long the check waits for one host.
const internetTimeout = 3 * time.Second

// checkInternet connects to the hosts the box talks to anyway, one after another; the first that
// answers is enough. With no such host - the catalogue switched off or local, no ACME - there is
// nothing to tell "no internet" by, and the state stays off, as with internetCheckDisabled.
func (c *Controller) checkInternet(ctx context.Context) {
	ok := true
	switch {
	case c.Src.Internet != nil:
		ok = c.Src.Internet(ctx)
	case c.exists("etc/config/internetCheckDisabled"):
	default:
		var targets []string
		if c.Src.InternetTargets != nil {
			targets = c.Src.InternetTargets()
		}
		dial := c.Dial
		if dial == nil {
			d := net.Dialer{Timeout: internetTimeout}
			dial = d.DialContext
		}
		for i, target := range targets {
			if i == 0 {
				ok = false
			}
			dctx, cancel := context.WithTimeout(ctx, internetTimeout)
			conn, err := dial(dctx, "tcp", target)
			cancel()
			if conn != nil {
				conn.Close()
			}
			if err == nil {
				ok = true
				break
			}
		}
	}
	c.mu.Lock()
	c.internet, c.internetAt, c.internetBusy = ok, c.now(), false
	c.mu.Unlock()
	c.poke()
}

// InternetTargets turns URLs into the host:port the internet check connects to: the http and https
// ones with a host (a file:// catalogue copy has none), the scheme's port when the URL names none,
// each host once, in the order given.
func InternetTargets(urls ...string) []string {
	var out []string
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Hostname() == "" {
			continue
		}
		port := u.Port()
		switch strings.ToLower(u.Scheme) {
		case "https":
			if port == "" {
				port = "443"
			}
		case "http":
			if port == "" {
				port = "80"
			}
		default:
			continue
		}
		target := net.JoinHostPort(strings.ToLower(u.Hostname()), port)
		if !slices.Contains(out, target) {
			out = append(out, target)
		}
	}
	return out
}

// ---- the API's side ---------------------------------------------------------------------------

// View is GET /led: the hardware, the configuration and what the page needs to edit it.
type View struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Hardware  Hardware `json:"hardware"`
	// PWR: the red power LED can be the error light (a box without an RGB LED).
	PWR         bool     `json:"pwr"`
	Config      Config   `json:"config"`
	Defaults    Config   `json:"defaults"`
	Fixed       []string `json:"fixed"`
	ErrorStates []string `json:"error_states"`
	WarningIDs  []string `json:"warning_ids"`
	// InternetHosts are the hosts the no-internet check connects to (task 95, D-67), [] for none.
	InternetHosts []string `json:"internet_hosts"`
}

// ShownView is what is shown, with since when.
type ShownView struct {
	Shown
	Since time.Time `json:"since"`
}

// ActiveView is one active state.
type ActiveView struct {
	ID string `json:"id"`
	Activity
}

// PWRView is the red power LED as the error light.
type PWRView struct {
	Enabled bool `json:"enabled"`
	Error   bool `json:"error"`
}

// Capabilities are what the hardware can show: the preset colours, the patterns, and whether the
// LED has levels (then any #rrggbb is mixed and the brightness settings apply).
type Capabilities struct {
	Colors        []string `json:"colors"`
	Patterns      []string `json:"patterns"`
	Brightness    bool     `json:"brightness"`
	MaxBrightness int      `json:"max_brightness,omitempty"`
}

// StateView is GET /led/state: what the LED shows now and why, and what else is active below it.
type StateView struct {
	Available    bool         `json:"available"`
	Reason       string       `json:"reason,omitempty"`
	Shown        ShownView    `json:"shown"`
	Active       []ActiveView `json:"active"`
	Overrides    []Override   `json:"overrides"`
	Locate       *Timed       `json:"locate,omitempty"`
	Preview      *Timed       `json:"preview,omitempty"`
	Night        bool         `json:"night"`
	Booting      bool         `json:"booting"`
	Conflict     bool         `json:"conflict"`
	WriteError   string       `json:"write_error,omitempty"`
	PWR          *PWRView     `json:"pwr,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
}

// View answers GET /led.
func (c *Controller) View() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	ids := c.Src.WarningIDs
	if ids == nil {
		ids = []string{}
	}
	hosts := []string{}
	if c.Src.InternetTargets != nil {
		for _, target := range c.Src.InternetTargets() {
			if h, _, err := net.SplitHostPort(target); err == nil {
				hosts = append(hosts, h)
			}
		}
	}
	return View{Available: c.hw.Available, Reason: c.hw.Reason, Hardware: c.hw, PWR: c.hw.PWR && !c.hw.Available, Config: c.cfg, Defaults: Defaults(),
		Fixed: FixedStates, ErrorStates: ErrorStates, WarningIDs: ids, InternetHosts: hosts}
}

// State answers GET /led/state.
func (c *Controller) State() StateView {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	now := c.now()
	c.expire(now)
	in := c.inputs(now)
	shown := Resolve(in)
	since := c.shownSince
	if shown != c.shown || since.IsZero() {
		since = now
	}
	if a, ok := in.Active[shown.ID]; ok && shown.Source == SourceState {
		since = a.Since
	}
	if o, ok := c.overrides[shown.ID]; ok && shown.Source == SourceOverride {
		since = o.Since
	}
	v := StateView{Available: c.hw.Available, Reason: c.hw.Reason, Shown: ShownView{Shown: shown, Since: since}, Active: []ActiveView{}, Overrides: in.Overrides,
		Locate: c.locate, Preview: c.preview, Night: NightActive(c.cfg.Night, now, c.loc), Booting: c.booting, Conflict: c.conflict, WriteError: c.writeErr,
		Capabilities: Capabilities{Colors: c.hw.Colors, Patterns: c.hw.Patterns, Brightness: c.hw.Brightness, MaxBrightness: c.hw.MaxBrightness}}
	if v.Capabilities.Colors == nil {
		v.Capabilities = Capabilities{Colors: Colors, Patterns: Patterns}
	}
	for _, id := range append(slices.Clone(FixedStates), statesOrder(c.cfg)...) {
		if a, ok := in.Active[id]; ok {
			v.Active = append(v.Active, ActiveView{ID: id, Activity: a})
		}
	}
	sort.SliceStable(v.Overrides, func(i, j int) bool {
		if a, b := priorityRank(v.Overrides[i].Priority), priorityRank(v.Overrides[j].Priority); a != b {
			return a > b
		}
		return v.Overrides[i].Since.After(v.Overrides[j].Since)
	})
	if c.hw.PWR && !c.hw.Available {
		v.PWR = &PWRView{Enabled: c.cfg.PWRErrorLight, Error: c.pwrOn}
	}
	return v
}

func statesOrder(cfg Config) []string {
	ids := make([]string, len(cfg.States))
	for i, s := range cfg.States {
		ids[i] = s.ID
	}
	return ids
}

// SetConfig validates, stores and applies a configuration.
func (c *Controller) SetConfig(cfg Config) (View, error) {
	v, err := Validate(cfg)
	if err != nil {
		return View{}, err
	}
	c.mu.Lock()
	c.load()
	if err := c.save(v); err != nil {
		c.mu.Unlock()
		return View{}, err
	}
	c.cfg = v
	c.mu.Unlock()
	c.poke()
	return c.View(), nil
}

// OverrideRequest is POST /led/override's body.
type OverrideRequest struct {
	ID      string `json:"id"`
	Color   string `json:"color"`
	Color2  string `json:"color2"`
	Pattern string `json:"pattern"`
	// OverNormal blinks over the normal colour instead of over dark (task 134); optional.
	OverNormal bool   `json:"over_normal"`
	DurationS  *int   `json:"duration_s"`
	Priority   string `json:"priority"`
	Night      string `json:"night"`
}

var overrideIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,63}$`)

// limited counts a caller's request and says whether it is over the rate; the caller holds mu.
func (c *Controller) limited(by string, now time.Time) bool {
	list := slices.DeleteFunc(c.requests[by], func(t time.Time) bool { return now.Sub(t) >= time.Second })
	if len(list) >= requestsPerSec {
		c.requests[by] = list
		return true
	}
	c.requests[by] = append(list, now)
	for k, l := range c.requests {
		if len(l) == 0 || now.Sub(l[len(l)-1]) > time.Minute {
			delete(c.requests, k)
		}
	}
	return false
}

// SetOverride sets the caller's override (by is the session's user, "token:<name>" for a token).
// One per id; an id another caller set is refused unless the caller is an administrator.
func (c *Controller) SetOverride(by string, admin bool, r OverrideRequest) (Override, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	now := c.now()
	if !c.hw.Available {
		return Override{}, ErrUnavailable
	}
	if c.limited(by, now) {
		return Override{}, ErrRateLimited
	}
	id := r.ID
	if id == "" {
		id = by
	}
	if !overrideIDRe.MatchString(id) {
		return Override{}, invalid("id %q", id)
	}
	look, err := NormalizeLook(Look{Color: r.Color, Pattern: r.Pattern, Color2: r.Color2, OverNormal: r.OverNormal})
	if err != nil {
		return Override{}, err
	}
	o := Override{ID: id, Color: look.Color, Pattern: look.Pattern, Color2: look.Color2, OverNormal: look.OverNormal, Priority: r.Priority, Night: r.Night, By: by, Since: now}
	if o.Priority == "" {
		o.Priority = "normal"
	}
	if o.Priority != "low" && o.Priority != "normal" && o.Priority != "high" {
		return Override{}, invalid("priority is low, normal or high")
	}
	if o.Night == "" {
		o.Night = "respect"
	}
	if o.Night != "respect" && o.Night != "ignore" {
		return Override{}, invalid("night is respect or ignore")
	}
	d := 300
	if r.DurationS != nil {
		d = *r.DurationS
	}
	switch {
	case d < 0:
		return Override{}, invalid("duration_s is 0 (until cleared) or more")
	case d == 0 && !c.cfg.External.AllowUntilCleared:
		return Override{}, invalid("an override until cleared is switched off on this system")
	case d == 0:
	default:
		if d > c.cfg.External.MaxDurationS {
			d = c.cfg.External.MaxDurationS
		}
		until := now.Add(time.Duration(d) * time.Second)
		o.Until = &until
	}
	c.expire(now)
	if old, ok := c.overrides[id]; ok {
		if old.By != by && !admin {
			return Override{}, ErrForbidden
		}
	} else if len(c.overrides) >= maxOverrides {
		return Override{}, ErrLimit
	}
	c.overrides[id] = o
	c.poke()
	return o, nil
}

// ClearOverride ends an override: the caller's own id when id is empty; another caller's only for
// an administrator.
func (c *Controller) ClearOverride(by, id string, admin bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	now := c.now()
	if !c.hw.Available {
		return ErrUnavailable
	}
	if c.limited(by, now) {
		return ErrRateLimited
	}
	if id == "" {
		id = by
	}
	c.expire(now)
	o, ok := c.overrides[id]
	if !ok {
		return ErrNotFound
	}
	if o.By != by && !admin {
		return ErrForbidden
	}
	delete(c.overrides, id)
	c.poke()
	return nil
}

// ClearOverrides ends every override (the page's Clear, an administrator's); the number ended.
func (c *Controller) ClearOverrides() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	n := len(c.overrides)
	c.overrides = map[string]Override{}
	c.poke()
	return n
}

// StartLocate blinks the locate look for durationS seconds (0 = the configured duration).
func (c *Controller) StartLocate(durationS int) (Timed, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	if !c.hw.Available {
		return Timed{}, ErrUnavailable
	}
	if durationS == 0 {
		durationS = c.cfg.Locate.DurationS
	}
	if durationS < 1 || durationS > 3600 {
		return Timed{}, invalid("duration_s is 1 to 3600")
	}
	t := Timed{Look: Look{Color: c.cfg.Locate.Color, Pattern: c.cfg.Locate.Pattern}, Until: c.now().Add(time.Duration(durationS) * time.Second)}
	c.locate = &t
	c.poke()
	return t, nil
}

// StopLocate ends locate.
func (c *Controller) StopLocate() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hw.Available {
		return ErrUnavailable
	}
	c.locate = nil
	c.poke()
	return nil
}

// StartPreview plays a look for ten seconds (the page's Test).
func (c *Controller) StartPreview(l Look) (Timed, error) {
	look, err := NormalizeLook(l)
	if err != nil {
		return Timed{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	if !c.hw.Available {
		return Timed{}, ErrUnavailable
	}
	t := Timed{Look: look, Until: c.now().Add(previewFor)}
	c.preview = &t
	c.poke()
	return t, nil
}

// StopPreview ends the preview.
func (c *Controller) StopPreview() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hw.Available {
		return ErrUnavailable
	}
	c.preview = nil
	c.poke()
	return nil
}
