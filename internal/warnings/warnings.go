// Package warnings is the Status page's warnings (task 81): which are active, evaluated here
// rather than in the browser, and the silences administrators set on them.
//
// A silence belongs to one user and one warning - an id plus a variant, so a change that matters
// (another certificate problem, a new unclean shutdown, one more addon on a list) is a new
// warning. It ends at the earlier of two events: the period the administrator picked (1, 7 or
// 90 days) runs out, or the warning's condition clears. The second has to be noticed even when
// nobody opens the page in between - a warning gone on day 30 and back on day 60 shows again on
// day 60 - so the Tracker evaluates every source on its own every few minutes, and on every
// read, and deletes a silence whose warning it finds inactive. A browser-only silence could not
// see that gap.
package warnings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// The severities, most severe first.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	// SeverityInfo is a quiet notice (openccu-lite task 366): listed after the warnings, never on
	// the status LED, no dot in the System menu.
	SeverityInfo = "info"
)

// The radio's duty cycle that raises the radio-load warning (occulited task 61, the maintainer
// 2026-10-15): from 25 % normally, from 75 % while a device firmware transfer runs on that
// interface - the HmIP access point suspends its background transfer at its own 50 % and resumes
// later, so about 50 % is the normal ceiling then. The LED's duty-cycle levels follow the same
// rule: during a transfer only those from 75 % up count.
const (
	DutyCycleThreshold         = 25
	DutyCycleThresholdTransfer = 75
)

// SystemUpdateID is the available system update's warning (occulited task 123). The status LED
// leaves it out of status-warning: its own system-update state shows a newer release already
// (cyan), so the warning does not turn the LED yellow on top of it.
const SystemUpdateID = "system-update-available"

// Periods are the silence lengths an administrator may pick, in days (D-65).
var Periods = []int{1, 7, 90}

// ErrPeriod, ErrNotActive and ErrNotSilenced are the refusals of Silence and Unsilence.
var (
	ErrPeriod      = errors.New("the period must be 1, 7 or 90 days")
	ErrNotActive   = errors.New("no such warning is active")
	ErrNotSilenced = errors.New("no such silence")
)

// Warning is one active warning. The sentence is the UI's, keyed by id and variant, with Params.
type Warning struct {
	ID       string         `json:"id"`
	Variant  string         `json:"variant"`
	Severity string         `json:"severity"`
	Params   map[string]any `json:"params,omitempty"`
	// Href is where the matter is handled: an in-app path, or a #fragment of the Status page
	Href string `json:"href,omitempty"`
	// Silenced is the caller's own silence of this warning; set by List only
	Silenced *Silence `json:"silenced,omitempty"`
}

// Silence is one user's silence of one warning.
type Silence struct {
	ID      string    `json:"id"`
	Variant string    `json:"variant"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Until   time.Time `json:"until"`
}

// Source evaluates the warnings of its IDs. ok false means the condition could not be read this
// time (rfd restarting, an addon listing that failed): its warnings are not shown, and its
// silences are kept - unknown is not cleared.
type Source struct {
	IDs []string
	// Lists: the variants of these ids are comma-separated sets (the addons a warning names). A
	// silence then covers a warning whose set is part of the silenced one, so an addon removed
	// from the list is still silenced and one added is new.
	Lists bool
	Eval  func(ctx context.Context) (list []Warning, ok bool)
}

// Tracker evaluates the sources and keeps the silences.
type Tracker struct {
	Sources []Source
	// File keeps the silences across restarts, <state>/warnings.json; "" = memory only.
	File string
	// Now is the clock; nil = time.Now. The tests move it by days.
	Now func() time.Time
	// Interval is how often Run evaluates; 0 = 5 minutes.
	Interval time.Duration
	Log      *slog.Logger
	// Settled says whether the box has finished starting; nil = always. Until it has, a warning
	// found inactive keeps its silences (B-107): at boot a condition that reads as clear may only
	// not be set up yet - the backup target's USB stick not mounted, the journal's boot script not
	// run - and spending the silence then made every silence end at every boot. A silence still
	// runs out at its end, and an active warning is shown as usual.
	Settled func() bool

	// eval serialises evaluations, so two readers do not run the sources twice at once
	eval     sync.Mutex
	mu       sync.Mutex
	loaded   bool
	silences []Silence
	active   []Warning
}

type fileFormat struct {
	Silences []Silence `json:"silences"`
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tracker) log() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

// load reads the file once; the caller holds mu.
func (t *Tracker) load() {
	if t.loaded {
		return
	}
	t.loaded = true
	if t.File == "" {
		return
	}
	b, err := os.ReadFile(t.File)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.log().Warn("warnings: the silences could not be read", "file", t.File, "err", err)
		}
		return
	}
	var f fileFormat
	if err := json.Unmarshal(b, &f); err != nil {
		t.log().Warn("warnings: the silences file is not valid JSON, starting without silences", "file", t.File, "err", err)
		return
	}
	for _, s := range f.Silences {
		if s.ID != "" && s.By != "" {
			t.silences = append(t.silences, s)
		}
	}
}

// save writes the silences atomically; the caller holds mu.
func (t *Tracker) save() error {
	if t.File == "" {
		return nil
	}
	list := t.silences
	if list == nil {
		list = []Silence{}
	}
	b, err := json.MarshalIndent(fileFormat{Silences: list}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(t.File)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(t.File)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, t.File)
	}
	if werr != nil {
		_ = os.Remove(name)
	}
	return werr
}

// source returns the source that owns id, or nil.
func (t *Tracker) source(id string) *Source {
	for i := range t.Sources {
		if slices.Contains(t.Sources[i].IDs, id) {
			return &t.Sources[i]
		}
	}
	return nil
}

// covers says whether a silence hides a warning.
func (t *Tracker) covers(s Silence, w Warning) bool {
	if s.ID != w.ID {
		return false
	}
	if s.Variant == w.Variant {
		return true
	}
	src := t.source(w.ID)
	if src == nil || !src.Lists {
		return false
	}
	have := strings.Split(s.Variant, ",")
	for _, item := range strings.Split(w.Variant, ",") {
		if !slices.Contains(have, item) {
			return false
		}
	}
	return true
}

// Evaluate runs every source and applies the two ends of a silence: the period, and the
// condition clearing. It returns the active warnings, most severe first.
func (t *Tracker) Evaluate(ctx context.Context) []Warning {
	t.eval.Lock()
	defer t.eval.Unlock()
	var active []Warning
	unknown := map[string]bool{}
	for _, src := range t.Sources {
		list, ok := src.Eval(ctx)
		if !ok {
			for _, id := range src.IDs {
				unknown[id] = true
			}
			continue
		}
		for _, w := range list {
			if slices.Contains(src.IDs, w.ID) {
				active = append(active, w)
			}
		}
	}
	sortWarnings(active, t.Sources)
	settled := t.Settled == nil || t.Settled()

	t.mu.Lock()
	defer t.mu.Unlock()
	t.load()
	now := t.now()
	kept := t.silences[:0:0]
	changed := false
	for _, s := range t.silences {
		switch {
		case !now.Before(s.Until):
			t.log().Info("warnings: a silence ran out", "id", s.ID, "variant", s.Variant, "by", s.By)
			changed = true
		case unknown[s.ID]:
			kept = append(kept, s)
		case t.source(s.ID) == nil:
			// a warning this daemon no longer knows
			changed = true
		case !slices.ContainsFunc(active, func(w Warning) bool { return t.covers(s, w) }) && !settled:
			// the box is still starting: not yet known is not cleared (B-107)
			kept = append(kept, s)
		case !slices.ContainsFunc(active, func(w Warning) bool { return t.covers(s, w) }):
			t.log().Info("warnings: the warning cleared, its silence is spent", "id", s.ID, "variant", s.Variant, "by", s.By)
			changed = true
		default:
			kept = append(kept, s)
		}
	}
	t.silences = kept
	if changed {
		if err := t.save(); err != nil {
			t.log().Warn("warnings: the silences could not be saved", "file", t.File, "err", err)
		}
	}
	t.active = active
	return slices.Clone(active)
}

// List evaluates and answers the active warnings as user sees them: with the silence that user
// set on each, for an administrator; a user of another role sees every warning and no silence.
func (t *Tracker) List(ctx context.Context, user string, admin bool) []Warning {
	active := t.Evaluate(ctx)
	out := make([]Warning, 0, len(active))
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, w := range active {
		if admin {
			for _, s := range t.silences {
				if s.By == user && t.covers(s, w) {
					c := s
					w.Silenced = &c
					break
				}
			}
		}
		out = append(out, w)
	}
	return out
}

// Unsilenced is the last evaluation's warnings that no administrator has silenced: what the status
// LED shows (task 95). It does not evaluate - Run does that every few minutes, and every read of
// the page - so it is cheap enough for the LED's ten-second loop.
func (t *Tracker) Unsilenced() []Warning {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.load()
	now := t.now()
	out := []Warning{}
	for _, w := range t.active {
		if !slices.ContainsFunc(t.silences, func(s Silence) bool { return now.Before(s.Until) && t.covers(s, w) }) {
			out = append(out, w)
		}
	}
	return out
}

// Silence hides an active warning from user for days (one of Periods).
func (t *Tracker) Silence(ctx context.Context, user, id, variant string, days int) (Silence, error) {
	if !slices.Contains(Periods, days) {
		return Silence{}, ErrPeriod
	}
	active := t.Evaluate(ctx)
	if !slices.ContainsFunc(active, func(w Warning) bool { return w.ID == id && w.Variant == variant }) {
		return Silence{}, ErrNotActive
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	s := Silence{ID: id, Variant: variant, By: user, At: now.UTC(), Until: now.Add(time.Duration(days) * 24 * time.Hour).UTC()}
	before := slices.Clone(t.silences)
	t.silences = slices.DeleteFunc(t.silences, func(o Silence) bool { return o.By == user && o.ID == id && o.Variant == variant })
	t.silences = append(t.silences, s)
	if err := t.save(); err != nil {
		t.silences = before
		return Silence{}, err
	}
	return s, nil
}

// Unsilence lifts user's silence of one warning.
func (t *Tracker) Unsilence(user, id, variant string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.load()
	before := slices.Clone(t.silences)
	t.silences = slices.DeleteFunc(t.silences, func(o Silence) bool { return o.By == user && o.ID == id && o.Variant == variant })
	if len(t.silences) == len(before) {
		return ErrNotSilenced
	}
	if err := t.save(); err != nil {
		t.silences = before
		return err
	}
	return nil
}

// Run evaluates at once and then every Interval until ctx ends, so a warning that clears while
// nobody looks spends its silences.
func (t *Tracker) Run(ctx context.Context) {
	every := t.Interval
	if every <= 0 {
		every = 5 * time.Minute
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		t.Evaluate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// sortWarnings orders errors before warnings and warnings before notices, then by the order the
// sources declare the ids, then by variant.
func sortWarnings(list []Warning, sources []Source) {
	order := map[string]int{}
	for _, src := range sources {
		for _, id := range src.IDs {
			if _, ok := order[id]; !ok {
				order[id] = len(order)
			}
		}
	}
	rank := func(s string) int {
		switch s {
		case SeverityError:
			return 0
		case SeverityInfo:
			return 2
		}
		return 1
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if rank(a.Severity) != rank(b.Severity) {
			return rank(a.Severity) < rank(b.Severity)
		}
		if order[a.ID] != order[b.ID] {
			return order[a.ID] < order[b.ID]
		}
		return a.Variant < b.Variant
	})
}
