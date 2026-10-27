package warnings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// occulited task 119: the addons' own notices. An addon - its daemon or a CGI, with its control
// token - posts a notice (a warning, an error or a quiet info) with its own words in German and
// English and a link into its page, and clears it when the matter is over; the notices stand on
// the Status page with the system's warnings, as the warning addon-notice with the variant
// "<addon>/<notice id>", so they are silenced, counted in the System menu's dot and shown on the
// status LED exactly as the system's own (an info never on the LED, no dot). The words are the
// addon's, never the UI's catalogue: the params carry them.
//
// A notice is kept across restarts of occulited (the file below), until the addon clears it, its
// expiry passes, or the addon is gone. Posting the same id again replaces the notice and keeps a
// silence of it - a matter that changed so much that it deserves to be seen again is a new id.

// AddonNoticeID is the warning id of every addon notice.
const AddonNoticeID = "addon-notice"

// The limits of one addon's notices.
const (
	MaxAddonNotices    = 20
	MaxNoticeTitle     = 160  // runes, each language
	MaxNoticeText      = 1000 // runes, each language
	MaxNoticeLink      = 300  // bytes
	MaxNoticeTTL       = 366 * 24 * time.Hour
	noticeLanguagesMin = 2 // de and en (D-12)
)

var (
	noticeIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	// noticeAddonRe is the addon id rule (internal/manifest's idRe)
	noticeAddonRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	// noticeLinkRe: an in-app path of the shell, without a scheme, a host or a quote
	noticeLinkRe = regexp.MustCompile(`^/[A-Za-z0-9/._~%!$&'()*+,;=:@?#-]*$`)
)

// ErrTooManyNotices, ErrNoSuchNotice and ErrNoticeInvalid are the store's refusals.
var (
	ErrTooManyNotices = fmt.Errorf("an addon may keep at most %d notices", MaxAddonNotices)
	ErrNoSuchNotice   = errors.New("no such notice")
	ErrNoticeInvalid  = errors.New("invalid notice")
)

// AddonNotice is one addon's notice as it is posted and kept.
type AddonNotice struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	// Title and Text are the addon's words by language: de and en, both (D-12).
	Title map[string]string `json:"title"`
	Text  map[string]string `json:"text,omitempty"`
	// Link is where the matter is handled, an in-app path of the shell; empty is the addon's
	// settings page, /addon-settings/<addon>.
	Link string `json:"link,omitempty"`
	// Expires ends the notice by itself; zero = until the addon clears it.
	Expires time.Time `json:"expires,omitzero"`
	Posted  time.Time `json:"posted"`
	Updated time.Time `json:"updated"`
}

// AddonNoticeInput is what an addon posts: the notice without its id and times, and TTL instead
// of an expiry (seconds, 0 = none).
type AddonNoticeInput struct {
	Severity string            `json:"severity"`
	Title    map[string]string `json:"title"`
	Text     map[string]string `json:"text,omitempty"`
	Link     string            `json:"link,omitempty"`
	TTL      int64             `json:"ttl,omitempty"`
}

// noticeError is a refusal that names the field.
func noticeError(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNoticeInvalid, fmt.Sprintf(f, a...))
}

// checkWords checks one language map: de and en, each non-empty (unless optional and absent) and
// within max runes, nothing else.
func checkWords(field string, m map[string]string, max int, optional bool) error {
	if len(m) == 0 && optional {
		return nil
	}
	if len(m) != noticeLanguagesMin {
		return noticeError("%s needs de and en, nothing else", field)
	}
	for _, lang := range []string{"de", "en"} {
		v, ok := m[lang]
		if !ok || strings.TrimSpace(v) == "" {
			return noticeError("%s.%s is empty", field, lang)
		}
		if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max {
			return noticeError("%s.%s: at most %d characters", field, lang, max)
		}
		if strings.ContainsAny(v, "\x00\r") {
			return noticeError("%s.%s carries a control character", field, lang)
		}
	}
	return nil
}

// validate checks what an addon posts.
func (in AddonNoticeInput) validate() error {
	switch in.Severity {
	case SeverityError, SeverityWarning, SeverityInfo:
	default:
		return noticeError("severity must be error, warning or info")
	}
	if err := checkWords("title", in.Title, MaxNoticeTitle, false); err != nil {
		return err
	}
	if err := checkWords("text", in.Text, MaxNoticeText, true); err != nil {
		return err
	}
	if in.Link != "" && (len(in.Link) > MaxNoticeLink || !noticeLinkRe.MatchString(in.Link) || strings.HasPrefix(in.Link, "//") || strings.Contains(in.Link, "/../")) {
		return noticeError("link must be an in-app path such as /addon-settings/<id>")
	}
	if in.TTL < 0 || time.Duration(in.TTL)*time.Second > MaxNoticeTTL {
		return noticeError("ttl: 0 to %d seconds", int64(MaxNoticeTTL/time.Second))
	}
	return nil
}

// AddonNotices keeps the addons' notices.
type AddonNotices struct {
	// File keeps them across restarts, <state>/addon-notices.json; "" = memory only.
	File string
	// Now is the clock; nil = time.Now.
	Now func() time.Time
	// Installed says whether an addon is still there; nil = every addon. A gone addon's notices
	// are dropped at the next read, so a reinstall does not bring them back.
	Installed func(id string) bool

	mu     sync.Mutex
	loaded bool
	byID   map[string][]AddonNotice // addon id -> its notices, in the order they were first posted
}

type noticesFile struct {
	Addons map[string][]AddonNotice `json:"addons"`
}

func (s *AddonNotices) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// load reads the file once; the caller holds mu.
func (s *AddonNotices) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.byID = map[string][]AddonNotice{}
	if s.File == "" {
		return
	}
	b, err := os.ReadFile(s.File)
	if err != nil {
		return
	}
	var f noticesFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	for id, list := range f.Addons {
		if noticeAddonRe.MatchString(id) && len(list) > 0 {
			s.byID[id] = list
		}
	}
}

// save writes the file atomically, 0600; the caller holds mu.
func (s *AddonNotices) save() error {
	if s.File == "" {
		return nil
	}
	b, err := json.MarshalIndent(noticesFile{Addons: s.byID}, "", "  ")
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
	name := tmp.Name()
	_, werr := tmp.Write(append(b, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, s.File)
	}
	if werr != nil {
		_ = os.Remove(name)
	}
	return werr
}

// prune drops expired notices and the notices of addons that are gone; the caller holds mu.
// It answers whether anything was dropped.
func (s *AddonNotices) prune() bool {
	now := s.now()
	changed := false
	for id, list := range s.byID {
		if s.Installed != nil && !s.Installed(id) {
			delete(s.byID, id)
			changed = true
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(list), func(n AddonNotice) bool { return !n.Expires.IsZero() && !now.Before(n.Expires) })
		if len(kept) != len(list) {
			changed = true
			if len(kept) == 0 {
				delete(s.byID, id)
			} else {
				s.byID[id] = kept
			}
		}
	}
	return changed
}

// prepare loads and prunes, saving what the prune dropped; the caller holds mu.
func (s *AddonNotices) prepare() {
	s.load()
	if s.prune() {
		_ = s.save()
	}
}

// Put posts or replaces the addon's notice id; created says it was new.
func (s *AddonNotices) Put(addon, id string, in AddonNoticeInput) (AddonNotice, bool, error) {
	if !noticeAddonRe.MatchString(addon) {
		return AddonNotice{}, false, noticeError("not an addon id")
	}
	if !noticeIDRe.MatchString(id) {
		return AddonNotice{}, false, noticeError("the notice id: a-z, 0-9, '.', '_', '-', at most 64, starting with a letter or digit")
	}
	if err := in.validate(); err != nil {
		return AddonNotice{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepare()
	now := s.now().UTC().Truncate(time.Second)
	n := AddonNotice{ID: id, Severity: in.Severity, Title: in.Title, Text: in.Text, Link: in.Link, Posted: now, Updated: now}
	if len(n.Text) == 0 {
		n.Text = nil
	}
	if in.TTL > 0 {
		n.Expires = now.Add(time.Duration(in.TTL) * time.Second)
	}
	list := s.byID[addon]
	i := slices.IndexFunc(list, func(o AddonNotice) bool { return o.ID == id })
	created := i < 0
	before := slices.Clone(list)
	if created {
		if len(list) >= MaxAddonNotices {
			return AddonNotice{}, false, ErrTooManyNotices
		}
		list = append(list, n)
	} else {
		n.Posted = list[i].Posted
		list = slices.Clone(list)
		list[i] = n
	}
	s.byID[addon] = list
	if err := s.save(); err != nil {
		s.byID[addon] = before
		return AddonNotice{}, false, err
	}
	return n, created, nil
}

// Delete clears one of the addon's notices.
func (s *AddonNotices) Delete(addon, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepare()
	list := s.byID[addon]
	i := slices.IndexFunc(list, func(o AddonNotice) bool { return o.ID == id })
	if i < 0 {
		return ErrNoSuchNotice
	}
	kept := slices.Delete(slices.Clone(list), i, i+1)
	if len(kept) == 0 {
		delete(s.byID, addon)
	} else {
		s.byID[addon] = kept
	}
	if err := s.save(); err != nil {
		s.byID[addon] = list
		return err
	}
	return nil
}

// Clear removes every notice of the addon and answers how many there were.
func (s *AddonNotices) Clear(addon string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepare()
	list := s.byID[addon]
	if len(list) == 0 {
		return 0, nil
	}
	delete(s.byID, addon)
	if err := s.save(); err != nil {
		s.byID[addon] = list
		return 0, err
	}
	return len(list), nil
}

// List is the addon's notices, in the order they were first posted.
func (s *AddonNotices) List(addon string) []AddonNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepare()
	return slices.Clone(s.byID[addon])
}

// Warnings are every addon's notices as the Status page's warnings: id addon-notice, the variant
// "<addon>/<notice id>", the words in the params, the link as the href. A source's Eval.
func (s *AddonNotices) Warnings() []Warning {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepare()
	addons := make([]string, 0, len(s.byID))
	for id := range s.byID {
		addons = append(addons, id)
	}
	sort.Strings(addons)
	var out []Warning
	for _, addon := range addons {
		for _, n := range s.byID[addon] {
			href := n.Link
			if href == "" {
				href = "/addon-settings/" + addon
			}
			params := map[string]any{"addon": addon, "title": n.Title, "posted": n.Posted.Format(time.RFC3339)}
			if len(n.Text) > 0 {
				params["text"] = n.Text
			}
			if !n.Expires.IsZero() {
				params["expires"] = n.Expires.Format(time.RFC3339)
			}
			out = append(out, Warning{ID: AddonNoticeID, Variant: addon + "/" + n.ID, Severity: n.Severity, Href: href, Params: params})
		}
	}
	return out
}
