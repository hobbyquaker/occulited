// Package appviews keeps the addons' views in the Control app (occulited task 125): an addon - its
// daemon, with its control token - registers a group of read-only pages (history's dashboards),
// and the App's drawer shows them after the rooms and the functions, one entry per addon labelled
// with the group's name; a single view is opened by the entry itself, several are its children.
// Each view is an iframe of its path, which lies under the addon's own pages (/addons/<id>/…).
//
// A registration replaces the addon's whole list. It is kept across restarts of occulited (the
// file), dropped when the addon is gone (Installed), and cleared by the addon (DELETE, or a PUT
// with no view). While the App's public mode is on, the URL segments of the addons with views are
// mirrored for lighttpd's gate (PublicMirror), which then lets an anonymous GET of those pages
// reach the addon marked X-Occulite-Auth: public - the addon decides what such a request may see.
package appviews

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The limits of one addon's registration.
const (
	MaxViews     = 50
	MaxGroupName = 40  // runes, each language
	MaxTitle     = 80  // runes, each language
	MaxPath      = 300 // bytes
)

var (
	addonRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	viewRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	iconRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	// a path under /addons/<segment>/, without a query, a fragment, a quote or a space
	pathRe = regexp.MustCompile(`^/addons/([A-Za-z0-9][A-Za-z0-9_.-]*)(/[A-Za-z0-9._~%!$&'()*+,;=:@-]*)*$`)
	// SegmentRe is what the public mirror holds as a file name (the gate checks the same shape)
	SegmentRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
)

// ErrInvalid is a registration the store refuses; the message says why.
var ErrInvalid = errors.New("invalid registration")

func invalid(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(f, a...)) }

// View is one page of an addon's group.
type View struct {
	ID    string            `json:"id"`
	Title map[string]string `json:"title"`
	Path  string            `json:"path"`
	Icon  string            `json:"icon,omitempty"`
}

// Registration is what an addon PUTs: its group's name and its views, in its order.
type Registration struct {
	Group map[string]string `json:"group"`
	Views []View            `json:"views"`
}

// Entry is one addon's registration as it is kept.
type Entry struct {
	Registration
	Updated time.Time `json:"updated"`
}

// words checks a de/en map: both, non-empty, within max runes, no control character, nothing else.
func words(field string, m map[string]string, max int) error {
	if len(m) != 2 {
		return invalid("%s needs de and en, nothing else", field)
	}
	for _, l := range []string{"de", "en"} {
		v, ok := m[l]
		if !ok || strings.TrimSpace(v) == "" {
			return invalid("%s.%s is empty", field, l)
		}
		if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max {
			return invalid("%s.%s: at most %d characters", field, l, max)
		}
		for _, r := range v {
			if r < 0x20 || r == 0x7f {
				return invalid("%s.%s carries a control character", field, l)
			}
		}
	}
	return nil
}

// validate checks a registration of addon whose own URL segments are segs.
func (reg Registration) validate(segs []string) error {
	if err := words("group", reg.Group, MaxGroupName); err != nil {
		return err
	}
	if len(reg.Views) > MaxViews {
		return invalid("at most %d views", MaxViews)
	}
	seen := map[string]bool{}
	for i, v := range reg.Views {
		if !viewRe.MatchString(v.ID) {
			return invalid("views[%d].id %q: 1 to 64 letters, digits, dots, underscores or dashes", i, v.ID)
		}
		if seen[v.ID] {
			return invalid("views[%d].id %q named twice", i, v.ID)
		}
		seen[v.ID] = true
		if err := words(fmt.Sprintf("views[%d].title", i), v.Title, MaxTitle); err != nil {
			return err
		}
		m := pathRe.FindStringSubmatch(v.Path)
		if len(v.Path) > MaxPath || m == nil {
			return invalid("views[%d].path must be a page under /addons/<id>/ without a query", i)
		}
		for _, seg := range strings.Split(v.Path, "/") {
			if seg == "." || seg == ".." || strings.Contains(seg, "%2") || strings.Contains(seg, "%5") {
				return invalid("views[%d].path: no dot segment and no encoded slash", i)
			}
		}
		own := false
		for _, s := range segs {
			own = own || s == m[1]
		}
		if !own {
			return invalid("views[%d].path: /addons/%s/ is not this addon's", i, m[1])
		}
		if v.Icon != "" && !iconRe.MatchString(v.Icon) {
			return invalid("views[%d].icon %q: a lower-case icon name", i, v.Icon)
		}
	}
	return nil
}

// Store keeps the registrations.
type Store struct {
	// File keeps them across restarts, <state>/addon-views.json; "" = memory only.
	File string
	// Installed says whether an addon is still there; nil = every addon. A gone addon's views
	// are dropped at the next read.
	Installed func(id string) bool
	// Segments answers an addon's URL segments under /addons/ besides its id (its lighttpd
	// drop-in's); a view's path must lie under one of them or the id. nil = the id alone.
	Segments func(id string) []string
	// Changed is called after every change, outside the lock (the public mirror follows it).
	Changed func()
	// Now is the clock; nil = time.Now.
	Now func() time.Time

	mu      sync.Mutex
	loaded  bool
	byAddon map[string]Entry
}

type file struct {
	Addons map[string]Entry `json:"addons"`
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// load reads the file once; the caller holds mu.
func (s *Store) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.byAddon = map[string]Entry{}
	if s.File == "" {
		return
	}
	b, err := os.ReadFile(s.File)
	if err != nil {
		return
	}
	var f file
	if json.Unmarshal(b, &f) != nil {
		return
	}
	for id, e := range f.Addons {
		if addonRe.MatchString(id) && len(e.Views) > 0 && e.validate(s.segments(id)) == nil {
			s.byAddon[id] = e
		}
	}
}

// save writes the file atomically, 0600; the caller holds mu.
func (s *Store) save() error {
	if s.File == "" {
		return nil
	}
	b, err := json.MarshalIndent(file{Addons: s.byAddon}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.File)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.File)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.File)
}

// segments is the addon's id and its drop-in's segments.
func (s *Store) segments(id string) []string {
	out := []string{id}
	if s.Segments != nil {
		out = append(out, s.Segments(id)...)
	}
	return out
}

// prune drops the gone addons' entries; the caller holds mu. True when one went.
func (s *Store) prune() bool {
	if s.Installed == nil {
		return false
	}
	gone := false
	for id := range s.byAddon {
		if !s.Installed(id) {
			delete(s.byAddon, id)
			gone = true
		}
	}
	return gone
}

func (s *Store) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

// Put replaces the addon's registration; a registration without views clears it. changed is
// false when the stored one was the same.
func (s *Store) Put(addon string, reg Registration) (changed bool, err error) {
	if !addonRe.MatchString(addon) {
		return false, invalid("addon id %q", addon)
	}
	if len(reg.Views) == 0 {
		return s.Delete(addon)
	}
	if err := reg.validate(s.segments(addon)); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.load()
	pruned := s.prune()
	old, had := s.byAddon[addon]
	same := had && sameRegistration(old.Registration, reg)
	if !same {
		s.byAddon[addon] = Entry{Registration: reg, Updated: s.now().UTC()}
	}
	if !same || pruned {
		if err := s.save(); err != nil {
			if !same {
				if had {
					s.byAddon[addon] = old
				} else {
					delete(s.byAddon, addon)
				}
			}
			s.mu.Unlock()
			return false, err
		}
	}
	s.mu.Unlock()
	if !same || pruned {
		s.changed()
	}
	return !same, nil
}

// Delete clears the addon's registration; changed is false when it had none.
func (s *Store) Delete(addon string) (changed bool, err error) {
	s.mu.Lock()
	s.load()
	old, had := s.byAddon[addon]
	if !had {
		s.mu.Unlock()
		return false, nil
	}
	delete(s.byAddon, addon)
	if err := s.save(); err != nil {
		s.byAddon[addon] = old
		s.mu.Unlock()
		return false, err
	}
	s.mu.Unlock()
	s.changed()
	return true, nil
}

// Get answers the addon's registration.
func (s *Store) Get(addon string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.Installed != nil && !s.Installed(addon) {
		return Entry{}, false
	}
	e, ok := s.byAddon[addon]
	return e, ok
}

// All answers the installed addons' registrations by addon id.
func (s *Store) All() map[string]Entry {
	s.mu.Lock()
	s.load()
	pruned := s.prune()
	if pruned {
		_ = s.save()
	}
	out := make(map[string]Entry, len(s.byAddon))
	for id, e := range s.byAddon {
		out[id] = e
	}
	s.mu.Unlock()
	if pruned {
		s.changed()
	}
	return out
}

// PublicSegments answers the sorted URL segments the registered views lie under, for the public
// mirror.
func (s *Store) PublicSegments() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range s.All() {
		for _, v := range e.Views {
			if m := pathRe.FindStringSubmatch(v.Path); m != nil && SegmentRe.MatchString(m[1]) && !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	sort.Strings(out)
	return out
}

func sameRegistration(a, b Registration) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// SyncPublicMirror makes dir hold exactly one empty file per segment while on, none while off:
// the gate lets an anonymous GET or HEAD under /addons/<segment>/ through when the file is
// there. Stale files go, so a segment is open no longer than its views and the public mode last.
func SyncPublicMirror(dir string, on bool, segs []string) error {
	if dir == "" {
		return nil
	}
	want := map[string]bool{}
	if on {
		for _, s := range segs {
			if SegmentRe.MatchString(s) {
				want[s] = true
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var first error
	for _, e := range entries {
		if !want[e.Name()] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && first == nil {
				first = err
			}
		}
	}
	for s := range want {
		p := filepath.Join(dir, s)
		if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm() == 0o644 {
			continue
		}
		// lighttpd, another user, opens the file: 0644 whatever the daemon's umask (the unit's
		// UMask=0077 made it 0600 and the gate found nothing)
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := os.Chmod(p, 0o644); err != nil && first == nil {
			first = err
		}
	}
	return first
}
