// Package catalog is the addon catalogue (task 10, D-119): a JSON file that says where the addons'
// manifests are - the repository, the manifest's path in it, an untested flag - and nothing else
// (docs/catalog-format.md). Everything an addon says about itself is in its manifest
// (docs/manifest-format.md, internal/manifest): the page shows the manifest fetched from the
// repository at its latest release tag, the install applies the one inside the package.
//
// Nothing here is fetched in the background: the catalogue file, the manifests, the star counts
// and the latest releases are loaded when the user runs a check (Refresh) and cached on disk
// (D-90); a page load, the start of the service and an install answer from the cache - the last
// fetched copy of each published catalogue file is part of it (B-240) - and from the bundled copy
// the image carries, whose adapter manifests beside it are known without any fetch. The one
// scheduled fetch, Run's daily refresh, goes out only while the user's *Check daily* is on.
package catalog

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/manifest"
)

// Format is the catalogue file's format this binary reads.
const Format = 1

// AdapterPrefix marks a manifest path that names an adapter manifest in the catalogue's own
// repository (catalog/manifests/<id>.json), read beside the catalogue file rather than from the
// addon's repository.
const AdapterPrefix = "catalog/manifests/"

// Catalog is the catalogue file.
type Catalog struct {
	Format int     `json:"format"`
	Addons []Entry `json:"addons"`
}

// Entry is one addon in the catalogue file: exactly three fields (D-119, revised 2026-09-25).
// A catalogue written before then carries `verified` instead of `untested`; the field is
// ignored like any other unknown key.
type Entry struct {
	Git      string `json:"git"`                // the addon's repository, https://
	Manifest string `json:"manifest"`           // the manifest's path in it, or an adapter under AdapterPrefix
	Untested bool   `json:"untested,omitempty"` // not tried on openccu-lite yet; a label, grants and refuses nothing
}

// Adapter says whether the entry's manifest is an adapter in the catalogue's repository.
func (e Entry) Adapter() bool { return strings.HasPrefix(e.Manifest, AdapterPrefix) }

// Item is one addon as the page sees it: the entry, and what the cache knows of its manifest.
type Item struct {
	Git          string `json:"git"`
	ManifestPath string `json:"manifest_path"`
	Untested     bool   `json:"untested,omitempty"`
	Adapter      bool   `json:"adapter,omitempty"`
	// Manifest is the fetched (or bundled adapter) manifest, flattened into the item: id, name,
	// description, homepage, release, requires, ui, runtime. Nil until the first check.
	*manifest.Manifest
	// Tag is the release tag the manifest was read at ("" for the default branch or an adapter),
	// Fetched when; Error is why the last fetch failed, with the previous manifest kept.
	Tag     string     `json:"tag,omitempty"`
	Fetched *time.Time `json:"fetched,omitempty"`
	Error   string     `json:"error,omitempty"`
	// Stars is the repository's GitHub star count from the last check, 0 for unknown.
	Stars int `json:"stars,omitempty"`
	// Latest is the release the resolver would pick for this box, absent while unknown.
	Latest *Latest `json:"latest,omitempty"`
	// UpdateAvailable is set by the API when the addon is installed and Latest is newer.
	UpdateAvailable bool `json:"update_available,omitempty"`
	// ReleaseNotes is set by the API (task 26): the manifest's changelog, else the latest
	// release's page; absent when neither is known, so the page never shows a dead link.
	ReleaseNotes string `json:"release_notes,omitempty"`
	// ImageHashes are the images the check fetched with the manifest (openccu-lite task 100):
	// kind → the content's sha256, the name of the file in ImagesDir. Images is set by the API:
	// kind → the URL the shell loads it from.
	ImageHashes map[string]string `json:"-"`
	Images      map[string]string `json:"images,omitempty"`
}

// NotesURL is where the release notes of the offered version are (task 26): the manifest's
// changelog, else the latest release's page; "" when neither is an https or http URL.
func (it Item) NotesURL() string {
	if it.Manifest != nil && webURL(it.Changelog) {
		return it.Changelog
	}
	if it.Latest != nil && webURL(it.Latest.Notes) {
		return it.Latest.Notes
	}
	return ""
}

func webURL(u string) bool {
	return (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) && !strings.ContainsAny(u, " \t\n\"'<>")
}

// View is what Fetch answers: the catalogue joined with the cache.
type View struct {
	Format int    `json:"format"`
	Addons []Item `json:"addons"`
	// Checked is when the user last ran a check that loaded everything; nil before the first.
	Checked *time.Time `json:"checked,omitempty"`
	// ReleasesError is why the last check could not read a release list (B-21): the versions and
	// update hints shown are the ones from before it. Nil when the last check read them all.
	ReleasesError *CheckNotice `json:"releases_error,omitempty"`
	// Source says where the entries come from (B-52): "published" when a published catalogue
	// file - fetched by a check, or the copy the last one kept - is part of the list, "bundled"
	// when the list is the image's own copy alone (no check has reached the published file yet),
	// "" when there are no entries.
	Source string `json:"source,omitempty"`
	// BundledDate is the date of the image's copy (its file's time), for "the built-in list of".
	BundledDate *time.Time `json:"bundled_date,omitempty"`
	// CheckError is why the last check failed (B-52): the published catalogue file could not be
	// fetched, no addon's manifest could be read, or nothing loaded at all. Nil after a check that
	// worked. Checked stays the time of the last check that worked.
	CheckError *CheckFailure `json:"check_error,omitempty"`
}

// CheckFailure is a check of the catalogue that failed (B-52): the user's or the daily one.
type CheckFailure struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`        // what failed, naming the host
	Host    string    `json:"host,omitempty"` // the host that could not be reached, when it is one
	// Failures counts the checks in a row that failed, Since is when the first of them did.
	Failures int       `json:"failures"`
	Since    time.Time `json:"since"`
}

// Persistent says whether the check keeps failing (B-52): three checks in a row, or failures
// over a day - two daily runs. The Status page warns then; one failed check is the Addons
// page's alone.
func (f *CheckFailure) Persistent() bool {
	return f != nil && (f.Failures >= 3 || f.At.Sub(f.Since) >= 24*time.Hour)
}

// CheckNotice is a release list the last check could not read (B-21).
type CheckNotice struct {
	Code    string     `json:"code"` // CodeRateLimit or CodeUnreachable
	Repo    string     `json:"repo"`
	Message string     `json:"message"`
	At      time.Time  `json:"at"`
	RetryAt *time.Time `json:"retry_at,omitempty"`
	// RetryMinutes is counted from the moment the view is answered; 0 = unknown or already past.
	RetryMinutes int `json:"retry_minutes,omitempty"`
}

// Latest is the newest release the resolver picked for this architecture.
type Latest struct {
	Version string `json:"version"`
	Asset   string `json:"asset"`
	// Notes is the release's page (GitHub's html_url) - the release notes of this version (task 26)
	Notes string `json:"notes_url,omitempty"`
}

// Resolved is one release asset, ready to download.
type Resolved struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Asset   string `json:"asset"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256_url,omitempty"`
	Release string `json:"release_url,omitempty"`
}

// Progress is the state of one install.
type Progress struct {
	AddonID  string     `json:"addon_id"`
	Phase    string     `json:"phase"` // resolving, downloading, verifying, installing, done, failed
	Message  string     `json:"message,omitempty"`
	Bytes    int64      `json:"bytes,omitempty"`
	Total    int64      `json:"total,omitempty"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Result   any        `json:"result,omitempty"`
	// Error is set on a failed run whose release list could not be read (B-21): CodeRateLimit or
	// CodeUnreachable; RetryMinutes is GitHub's wait, in whole minutes (0 = unknown). Nothing was
	// installed then.
	Error        string `json:"error,omitempty"`
	RetryMinutes int    `json:"retry_minutes,omitempty"`
	// Percent (30.2) is one bar from 0 to 100 over the whole run: the download by bytes against
	// the content length, the install by elapsed time against a duration learned from the last
	// install of the same addon (or estimated from the archive size), never reaching 100 before
	// the installer has returned, never sitting still while something is happening.
	Percent int `json:"percent"`
}

// Installer is the piece that takes the archive: system.SystemdAddons.Install. The context
// names the addon the archive is (AddonID).
type Installer interface {
	Install(ctx context.Context, archive io.Reader) (any, error)
}

type addonIDKey struct{}

// AddonID is the catalogue id of the addon an Installer is handed, "" outside a catalogue install.
func AddonID(ctx context.Context) string {
	id, _ := ctx.Value(addonIDKey{}).(string)
	return id
}

// cached is what the disk cache keeps per catalogue entry, keyed by the repository URL.
type cached struct {
	Manifest *manifest.Manifest `json:"manifest,omitempty"`
	Tag      string             `json:"tag,omitempty"`
	ETag     string             `json:"etag,omitempty"`
	Fetched  time.Time          `json:"fetched"`
	Error    string             `json:"error,omitempty"`
	// Images are the manifest's declared images as fetched at Tag (openccu-lite task 100): kind →
	// the content's sha256, which names the file in Service.ImagesDir. A kind the fetch could not
	// take (not there, too large, no image) is left out.
	Images map[string]string `json:"images,omitempty"`
}

// cacheFile is the disk cache's shape.
type cacheFile struct {
	Entries   map[string]cached `json:"entries"`
	Stars     map[string]int    `json:"stars,omitempty"`
	StarsETag map[string]string `json:"stars_etag,omitempty"`
	Latest    map[string]Latest `json:"latest,omitempty"`
	Checked   *time.Time        `json:"checked,omitempty"`
	// Catalogs is the last fetched copy of each published catalogue file, by URL (B-240): what a
	// page load and the start of the service read instead of the network. file:// URLs are read
	// from disk every time and are not kept here.
	Catalogs map[string]*Catalog `json:"catalogs,omitempty"`
	// ReleasesError is the last check's unread release list (B-21), kept across a restart so that
	// the page still says why its versions are old.
	ReleasesError *CheckNotice `json:"releases_error,omitempty"`
	// CheckError is the last check's failure (B-52), kept across a restart like ReleasesError.
	CheckError *CheckFailure `json:"check_error,omitempty"`
}

// Service loads the catalogue, fetches manifests and installs from them.
type Service struct {
	URLs []string // the catalogue file's URLs: the published one first, the bundled copy last
	Arch string   // uname -m
	HTTP *http.Client
	// HeaderWait bounds each request's wait for its response header (B-56); 0 is
	// httpwait.HeaderWait.
	HeaderWait time.Duration
	GitHubAPI  string // https://api.github.com, overridable for tests
	RawGitHub  string // https://raw.githubusercontent.com, overridable for tests
	Installer  Installer
	// BundledManifests is the directory of adapter manifests the image carries beside the bundled
	// catalogue (/etc/occulite/manifests); "" for none.
	BundledManifests string
	// Daily says whether Run's daily release refresh goes out (task 244); nil = always.
	Daily func() bool
	// ImagesDir keeps the images fetched with the manifests (openccu-lite task 100), one file per
	// content hash; "" fetches none.
	ImagesDir string
	// CacheFile keeps the fetched manifests, the star counts and the latest releases across
	// restarts; "" = this process only.
	CacheFile string
	// TimingsFile keeps what the last install of each addon took (30.2), so the bar's install
	// half moves at the right speed the next time; "" = remembered for this process only.
	TimingsFile string

	refreshing sync.Mutex // one refresh at a time

	mu       sync.Mutex
	catalog  *Catalog // the merged catalogue file, with the source URL per entry
	sources  map[string]string
	cache    cacheFile
	loaded   bool
	adapters map[string]*manifest.Manifest // the bundled adapter manifests by id
	bundled  *time.Time                    // the time of the image's copy of the catalogue file
	releases map[string]releaseCache
	timings  map[string]float64 // addon id -> install seconds
	phaseAt  time.Time          // when the current phase began
	progress *Progress
	now      func() time.Time // the clock for rate-limit waits; nil = time.Now
}

// New returns a service for the catalogue URLs.
func New(urls []string, arch string, inst Installer) *Service {
	return &Service{URLs: urls, Arch: arch, HTTP: &http.Client{Timeout: 10 * time.Minute}, GitHubAPI: "https://api.github.com", RawGitHub: "https://raw.githubusercontent.com", Installer: inst}
}

var gitRe = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(?::\d+)?/[\w.-]+/[\w.-]+$`)

// RegaFreeAdapterIDs reads the ids of the adapter manifests the image carries (no network) that
// do not declare requires.rega: the addons whose authors ship no manifest and that the ReGa scan
// must not flag. An adapter listed only as untested that does need the ReGa (homekit-ccu) is left
// out, so its own manifest's verdict stands. An unreadable directory yields none.
func RegaFreeAdapterIDs(dir string) []string {
	var ids []string
	for id, m := range loadAdapters(dir) {
		if !m.NeedsRega() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func loadAdapters(dir string) map[string]*manifest.Manifest {
	out := map[string]*manifest.Manifest{}
	if dir == "" {
		return out
	}
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, p := range names {
		m, err := manifest.ParseFile(p)
		if err != nil {
			slog.Warn("catalog: a bundled adapter manifest does not parse", "file", p, "err", err)
			continue
		}
		if m.ID != strings.TrimSuffix(filepath.Base(p), ".json") {
			continue
		}
		out[m.ID] = m
	}
	return out
}

// loadLocked reads the disk cache and the bundled adapters once. s.mu held.
func (s *Service) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.adapters = loadAdapters(s.BundledManifests)
	for _, u := range s.URLs {
		if p, ok := strings.CutPrefix(u, "file://"); ok {
			if fi, err := os.Stat(p); err == nil {
				t := fi.ModTime()
				s.bundled = &t
			}
			break
		}
	}
	s.cache = cacheFile{Entries: map[string]cached{}}
	if s.CacheFile == "" {
		return
	}
	b, err := os.ReadFile(s.CacheFile)
	if err != nil {
		return
	}
	var c cacheFile
	if json.Unmarshal(b, &c) != nil {
		return
	}
	if c.Entries == nil {
		c.Entries = map[string]cached{}
	}
	s.cache = c
}

// saveLocked writes the disk cache. s.mu held.
func (s *Service) saveLocked() {
	if s.CacheFile == "" {
		return
	}
	b, err := json.MarshalIndent(s.cache, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.CacheFile, b, 0o644); err != nil {
		slog.Warn("catalog: the cache could not be written", "file", s.CacheFile, "err", err)
	}
	s.pruneImagesLocked()
}

// Fetch merges the configured catalogue files and answers the view: the entries joined with the
// cached manifests, the bundled adapters, the star counts and the latest releases. Without force
// nothing goes out (B-240, D-90): a published file is taken from the copy the last check left in
// the cache (none = the file is skipped), a file:// URL is read from disk, and a catalogue already
// in memory is answered as it is. With force - the user's check, Run's daily refresh - every
// published file is fetched again and the copy kept. It fetches no manifest: that is Refresh's.
// An error only when no catalogue file could be loaded.
func (s *Service) Fetch(ctx context.Context, force bool) (*View, error) {
	if _, err := s.loadFiles(ctx, force); err != nil {
		return nil, err
	}
	return s.view(), nil
}

// loadFiles is Fetch without the view: err when no catalogue file loaded, pubErr when a published
// file could not be fetched (force only) while others - its kept copy, the bundled one - stood in.
func (s *Service) loadFiles(ctx context.Context, force bool) (pubErr, err error) {
	s.mu.Lock()
	s.loadLocked()
	have := s.catalog != nil
	s.mu.Unlock()
	if force || !have {
		cat, sources, pubErr, err := s.loadCatalog(ctx, force)
		if err != nil {
			return pubErr, err
		}
		s.mu.Lock()
		s.catalog, s.sources = cat, sources
		if force {
			s.saveLocked() // the fetched copies, for the next start
		}
		s.mu.Unlock()
		return pubErr, nil
	}
	return nil, nil
}

// sourceError is a published catalogue file that could not be fetched, by the host that failed
// (B-52): "raw.githubusercontent.com: dial tcp: lookup …: no such host", not Go's quoted URL.
type sourceError struct {
	host string
	err  error
}

func (e *sourceError) Error() string { return e.host + ": " + e.err.Error() }
func (e *sourceError) Unwrap() error { return e.err }

// newSourceError names the host of u and the cause of err without the URL Go's client puts in.
func newSourceError(u string, err error) *sourceError {
	host := u
	if p, perr := url.Parse(u); perr == nil && p.Host != "" {
		host = p.Host
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &sourceError{host: host, err: err}
}

// noteCheckLocked records a check's outcome (B-52): nil clears the failure, an error counts it.
// A cancelled check (the user left the page) is no outcome. s.mu held.
func (s *Service) noteCheckLocked(err error, now time.Time) {
	if err == nil {
		s.cache.CheckError = nil
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	f := &CheckFailure{At: now, Message: err.Error(), Failures: 1, Since: now}
	if errors.Is(err, context.DeadlineExceeded) {
		f.Message = "the check did not finish in time"
	}
	var se *sourceError
	if errors.As(err, &se) {
		f.Host = se.host
	}
	if prev := s.cache.CheckError; prev != nil {
		f.Failures, f.Since = prev.Failures+1, prev.Since
	}
	s.cache.CheckError = f
	slog.Warn("catalog: the check failed", "err", f.Message, "failures", f.Failures, "since", f.Since)
}

// CheckError is the last check's failure (B-52) or nil, from what the system holds; it never
// goes out (the Status warning reads it).
func (s *Service) CheckError() *CheckFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if s.cache.CheckError == nil {
		return nil
	}
	c := *s.cache.CheckError
	return &c
}

// Cached is the view from what the system holds already - the catalogue file of the last Fetch and
// the cached manifests and releases - or nil before any Fetch. It never goes out (task 248: the
// Status warning and the menu dot of an addon update read it once a minute).
func (s *Service) Cached() *View {
	s.mu.Lock()
	s.loadLocked()
	have := s.catalog != nil
	s.mu.Unlock()
	if !have {
		return nil
	}
	return s.view()
}

// loadCatalog merges every configured URL, the first entry per repository winning. A file:// URL
// is read from disk; a published one is fetched only with network and its copy kept in the cache,
// else the kept copy stands in - also when the fetch fails - and a URL without one is skipped.
func (s *Service) loadCatalog(ctx context.Context, network bool) (*Catalog, map[string]string, error, error) {
	merged := &Catalog{Format: Format}
	sources := map[string]string{}
	seen := map[string]bool{}
	var firstErr, pubErr error
	loaded := 0
	for _, u := range s.URLs {
		cat, err := s.loadOne(ctx, u, network)
		if err != nil && !strings.HasPrefix(u, "file://") {
			// B-52: a published file the check could not fetch is the check's failure, whatever
			// stands in for it - and it is logged even when nothing earlier was kept
			se := newSourceError(u, err)
			if pubErr == nil {
				pubErr = se
			}
			if cat != nil {
				slog.Warn("catalog: the catalogue file could not be fetched, the last copy stands in", "catalog", u, "err", se)
			} else {
				slog.Warn("catalog: the catalogue file could not be fetched and no copy of it is kept", "catalog", u, "err", se)
			}
		}
		if cat == nil && err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", u, err)
			}
			continue
		}
		if cat == nil {
			continue // a published file no check has fetched yet
		}
		loaded++
		for _, e := range cat.Addons {
			e.Git = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(e.Git), "/"), ".git")
			key := entryKey(e.Git)
			if !gitRe.MatchString(e.Git) || e.Manifest == "" || strings.Contains(e.Manifest, "..") || strings.HasPrefix(e.Manifest, "/") {
				slog.Warn("catalog: an entry is not usable and was skipped", "git", e.Git, "manifest", e.Manifest, "catalog", u)
				continue
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			merged.Addons = append(merged.Addons, e)
			sources[key] = u
		}
	}
	if loaded == 0 && firstErr != nil {
		return nil, nil, pubErr, firstErr
	}
	return merged, sources, pubErr, nil
}

// loadOne is one catalogue URL under loadCatalog's rule; nil without an error is a published file
// without a kept copy. A published file whose fetch failed answers its kept copy with the error.
func (s *Service) loadOne(ctx context.Context, u string, network bool) (*Catalog, error) {
	if strings.HasPrefix(u, "file://") {
		return s.fetchCatalog(ctx, u)
	}
	s.mu.Lock()
	kept := s.cache.Catalogs[u]
	s.mu.Unlock()
	if !network {
		return kept, nil
	}
	cat, err := s.fetchCatalog(ctx, u)
	if err != nil {
		return kept, err
	}
	s.mu.Lock()
	if s.cache.Catalogs == nil {
		s.cache.Catalogs = map[string]*Catalog{}
	}
	s.cache.Catalogs[u] = cat
	s.mu.Unlock()
	return cat, nil
}

// entryKey is how two catalogue files name the same repository: the URL lower-cased, without a
// trailing slash or .git.
func entryKey(git string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(git), "/"), ".git"))
}

func (s *Service) fetchCatalog(ctx context.Context, u string) (*Catalog, error) {
	var b []byte
	var err error
	if strings.HasPrefix(u, "file://") {
		b, err = os.ReadFile(strings.TrimPrefix(u, "file://"))
	} else {
		b, _, err = s.getETag(ctx, u, "", 1<<20)
	}
	if err != nil {
		return nil, err
	}
	var cat Catalog
	if err := json.Unmarshal(b, &cat); err != nil {
		return nil, err
	}
	if cat.Format != Format {
		return nil, fmt.Errorf("catalogue format %d is not %d", cat.Format, Format)
	}
	return &cat, nil
}

var errNotModified = errors.New("not modified")

// httpStatusError is an answer other than 200: the status, so a caller can tell a 404 (the file is
// not at that ref) from a failing host.
type httpStatusError int

func (e httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", int(e)) }

// isNotFound says whether err is a 404.
func isNotFound(err error) bool {
	var se httpStatusError
	return errors.As(err, &se) && int(se) == http.StatusNotFound
}

// getETag is one GET with a byte cap; etag adds If-None-Match, and a 304 answers errNotModified.
func (s *Service) getETag(ctx context.Context, u, etag string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := s.do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified && etag != "" {
		return nil, etag, errNotModified
	}
	if res.StatusCode != 200 {
		return nil, "", httpStatusError(res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(b)) > limit {
		return nil, "", fmt.Errorf("larger than %d bytes", limit)
	}
	return b, res.Header.Get("ETag"), nil
}

// view joins the catalogue with the cache. It never fetches.
func (s *Service) view() *View {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := &View{Format: Format, Checked: s.cache.Checked, Addons: []Item{}}
	if n := s.cache.ReleasesError; n != nil {
		c := *n
		if c.RetryAt != nil {
			re := &ReleasesError{Repo: c.Repo, RateLimited: c.Code == CodeRateLimit, RetryAt: *c.RetryAt}
			c.RetryMinutes = re.RetryMinutes(s.clock())
			if re.RateLimited {
				c.Message = re.message(s.clock())
			}
		}
		v.ReleasesError = &c
	}
	if f := s.cache.CheckError; f != nil {
		c := *f
		v.CheckError = &c
	}
	v.BundledDate = s.bundled
	if s.catalog == nil {
		return v
	}
	for _, e := range s.catalog.Addons {
		if v.Source != "published" {
			v.Source = "bundled"
			if !strings.HasPrefix(s.sources[entryKey(e.Git)], "file://") {
				v.Source = "published"
			}
		}
		it := Item{Git: e.Git, ManifestPath: e.Manifest, Untested: e.Untested, Adapter: e.Adapter()}
		if c, ok := s.cache.Entries[entryKey(e.Git)]; ok {
			it.Manifest, it.Tag, it.Error = c.Manifest, c.Tag, c.Error
			if len(c.Images) > 0 {
				it.ImageHashes = maps.Clone(c.Images)
			}
			if !c.Fetched.IsZero() {
				f := c.Fetched
				it.Fetched = &f
			}
		}
		// the bundled adapter is known without a fetch; the fetched copy of it wins when there is one
		if it.Manifest == nil && e.Adapter() {
			it.Manifest = s.adapters[adapterID(e.Manifest)]
		}
		if it.Manifest != nil {
			if it.Release != nil {
				it.Stars = s.cache.Stars[it.Release.GitHub]
			}
			if l, ok := s.cache.Latest[it.ID]; ok {
				cp := l
				it.Latest = &cp
			}
		}
		v.Addons = append(v.Addons, it)
	}
	sort.SliceStable(v.Addons, func(i, j int) bool {
		a, b := v.Addons[i], v.Addons[j]
		if a.Untested != b.Untested {
			return b.Untested
		}
		return strings.ToLower(itemName(a)) < strings.ToLower(itemName(b))
	})
	return v
}

// adapterID is the id an adapter manifest path names: its file name.
func adapterID(manifestPath string) string {
	return strings.TrimSuffix(path.Base(manifestPath), ".json")
}

// itemName is what an item is called before a manifest is known: the repository's name.
func itemName(it Item) string {
	if it.Manifest != nil {
		return it.Name.In("en")
	}
	return RepoName(it.Git)
}

// RepoName is "owner/repo" of a repository URL, for an entry whose manifest is not fetched yet.
func RepoName(git string) string {
	u, err := url.Parse(git)
	if err != nil {
		return git
	}
	return strings.Trim(u.Path, "/")
}

// Refresh is the user's check (D-90): the catalogue files again, every entry's manifest at its
// latest release tag (or the adapter beside the catalogue), the star counts, the latest releases;
// all of it cached. An entry whose fetch fails keeps its last manifest and records the error;
// the call fails only when no catalogue file loads. One refresh at a time; a second caller waits.
//
// B-52: a check that fails says so - the published catalogue file not fetched (the kept or the
// bundled copy stands in), no addon's manifest read, nothing loaded at all, or the check cut off -
// in the view's CheckError, kept across a restart; checked stays the last check that worked.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshing.Lock()
	defer s.refreshing.Unlock()
	pubErr, err := s.loadFiles(ctx, true)
	if err != nil {
		s.noteCheck(err)
		return err
	}
	s.mu.Lock()
	cat, sources := s.catalog, s.sources
	s.cache.ReleasesError = nil // this check says anew what it could not read
	s.mu.Unlock()
	tried, failed := 0, 0
	var firstFailed string
	for _, e := range cat.Addons {
		if ctx.Err() != nil {
			s.noteCheck(ctx.Err())
			return ctx.Err()
		}
		src := sources[entryKey(e.Git)]
		c := s.fetchManifest(ctx, e, src)
		s.mu.Lock()
		s.cache.Entries[entryKey(e.Git)] = c
		s.mu.Unlock()
		// the addons' own repositories: an adapter is read beside the catalogue file, so it says
		// nothing about whether GitHub answers for them (a rate limit leaves the adapters readable)
		if !e.Adapter() {
			tried++
			if c.Error != "" {
				failed++
				if firstFailed == "" {
					firstFailed = RepoName(e.Git) + ": " + c.Error
				}
			}
		}
		if c.Manifest != nil && c.Manifest.Release != nil {
			s.refreshStars(ctx, c.Manifest.Release.GitHub)
		}
	}
	s.RefreshReleases(ctx)
	failure := pubErr
	if failure == nil && tried > 0 && failed == tried {
		failure = fmt.Errorf("no addon's manifest could be read (%s)", firstFailed)
	}
	now := time.Now()
	s.mu.Lock()
	if failure == nil {
		s.cache.Checked = &now
	}
	s.noteCheckLocked(failure, now)
	s.saveLocked()
	s.mu.Unlock()
	return nil
}

// noteCheck records a check's outcome and writes the cache.
func (s *Service) noteCheck(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noteCheckLocked(err, time.Now())
	s.saveLocked()
}

// fetchManifest reads one entry's manifest: an adapter from beside the catalogue file it came
// from, else from the repository at its latest release tag (GitHub), or its default branch - also
// when the file is not at the tag yet.
func (s *Service) fetchManifest(ctx context.Context, e Entry, source string) cached {
	s.mu.Lock()
	prev := s.cache.Entries[entryKey(e.Git)]
	s.mu.Unlock()
	out := cached{Manifest: prev.Manifest, Tag: prev.Tag, ETag: prev.ETag, Fetched: time.Now()}
	fail := func(err error) cached {
		out.Error = err.Error()
		slog.Warn("catalog: an entry's manifest could not be fetched", "git", e.Git, "manifest", e.Manifest, "err", err)
		return out
	}
	var b []byte
	var etag string
	var err error
	if e.Adapter() {
		u := adapterURL(source, e.Manifest)
		if strings.HasPrefix(u, "file://") {
			b, err = os.ReadFile(strings.TrimPrefix(u, "file://"))
		} else {
			b, etag, err = s.getETag(ctx, u, prev.ETag, manifest.MaxSize)
		}
		out.Tag = ""
	} else {
		var tag string
		if tag, err = s.latestTag(ctx, e.Git); err != nil {
			s.noteReleasesError(err)
			if re, ok := asReleasesError(err); ok && re.RateLimited && prev.Manifest != nil {
				// the manifest of the last check stays, with its tag; the page's notice names the
				// limit once instead of every card (B-21)
				out.Error = prev.Error
				return out
			}
			return fail(err)
		}
		etagIn := ""
		if tag == prev.Tag {
			etagIn = prev.ETag
		}
		for _, u := range rawURLs(s.RawGitHub, e.Git, e.Manifest, tag) {
			b, etag, err = s.getETag(ctx, u, etagIn, manifest.MaxSize)
			if err == nil || errors.Is(err, errNotModified) {
				break
			}
		}
		if tag != "" && isNotFound(err) {
			// the latest release predates the addon's manifest (the four first-class addons
			// between their manifest commit and their next release): the default branch
			// describes the addon until a release carries the file - the rule for a repository
			// without releases, applied to one whose release lacks the file. Recorded as no tag.
			for _, u := range rawURLs(s.RawGitHub, e.Git, e.Manifest, "") {
				b, etag, err = s.getETag(ctx, u, "", manifest.MaxSize)
				if err == nil {
					break
				}
			}
			tag = ""
		}
		out.Tag = tag
	}
	switch {
	case errors.Is(err, errNotModified):
		out.Error = ""
		out.Images = s.fetchImages(ctx, e, out, prev) // the manifest we have is the current one; its images may be missing
		return out
	case err != nil:
		return fail(err)
	}
	m, err := manifest.Parse(b)
	if err != nil {
		return fail(err)
	}
	if e.Adapter() && m.ID != adapterID(e.Manifest) {
		return fail(fmt.Errorf("the adapter manifest names %s, not its file", m.ID))
	}
	out.Manifest, out.ETag, out.Error = m, etag, ""
	out.Images = s.fetchImages(ctx, e, out, prev)
	return out
}

// adapterURL is where an adapter manifest lives beside the catalogue file it came from: the
// file's directory plus manifests/<name>. The bundled copy lives the same way in /etc/occulite.
func adapterURL(catalogURL, manifestPath string) string {
	dir := catalogURL[:strings.LastIndex(catalogURL, "/")+1]
	return dir + "manifests/" + path.Base(manifestPath)
}

// rawURLs are where the raw file is on the repository's host: GitHub's raw host at the tag (or
// HEAD without one), else the Gitea shape at the tag, or at the default branches this binary knows.
func rawURLs(rawGitHub, git, file, tag string) []string {
	u, err := url.Parse(git)
	if err != nil {
		return nil
	}
	if strings.EqualFold(u.Host, "github.com") {
		ref := tag
		if ref == "" {
			ref = "HEAD"
		}
		return []string{rawGitHub + "/" + strings.Trim(u.Path, "/") + "/" + ref + "/" + file}
	}
	base := strings.TrimSuffix(git, "/")
	if tag != "" {
		return []string{base + "/raw/tag/" + tag + "/" + file}
	}
	return []string{base + "/raw/branch/main/" + file, base + "/raw/branch/master/" + file}
}

// latestTag is the repository's latest release tag: for GitHub from the releases list (the first
// stable release, else the first prerelease; conditional, so a repeat is free of the rate
// limit), "" for a repository without releases and for every other host.
func (s *Service) latestTag(ctx context.Context, git string) (string, error) {
	u, err := url.Parse(git)
	if err != nil || !strings.EqualFold(u.Host, "github.com") {
		return "", nil
	}
	rels, err := s.releasesOf(ctx, strings.Trim(u.Path, "/"))
	if err != nil {
		return "", err
	}
	for _, r := range rels {
		if !r.Draft && !r.Prerelease {
			return r.TagName, nil
		}
	}
	for _, r := range rels {
		if !r.Draft {
			return r.TagName, nil
		}
	}
	return "", nil
}

// refreshStars asks GitHub for a repository's star count once per check (ETag-cached) and
// remembers it; a failing call keeps the last value.
func (s *Service) refreshStars(ctx context.Context, repo string) {
	if !repoRe.MatchString(repo) {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.GitHubAPI+"/repos/"+repo, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	s.mu.Lock()
	if etag := s.cache.StarsETag[repo]; etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	s.mu.Unlock()
	res, err := s.do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return
	}
	var body struct {
		Stars int `json:"stargazers_count"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body) != nil {
		return
	}
	s.mu.Lock()
	if s.cache.Stars == nil {
		s.cache.Stars, s.cache.StarsETag = map[string]int{}, map[string]string{}
	}
	s.cache.Stars[repo], s.cache.StarsETag[repo] = body.Stars, res.Header.Get("ETag")
	s.mu.Unlock()
}

// RefreshReleases resolves every known manifest's newest release once and remembers what it
// picked, so that the page can show a version and an update hint. Resolve is conditional (ETag);
// a list it cannot read (the shared address rate-limited, no network) keeps the version the page
// had and is named in the view's ReleasesError (B-21), never answered from an older list.
func (s *Service) RefreshReleases(ctx context.Context) {
	v := s.view()
	changed := false
	for _, it := range v.Addons {
		if ctx.Err() != nil {
			break
		}
		if it.Manifest == nil || it.Release == nil || !it.SupportsArch(s.Arch) {
			continue
		}
		r, err := s.Resolve(ctx, it.Release)
		if err != nil {
			s.noteReleasesError(err)
			continue // no package for this box, or GitHub said no: keep whatever we had
		}
		s.mu.Lock()
		if s.cache.Latest == nil {
			s.cache.Latest = map[string]Latest{}
		}
		if cur, ok := s.cache.Latest[it.ID]; !ok || cur.Version != r.Version || cur.Asset != r.Asset || cur.Notes != r.Release {
			s.cache.Latest[it.ID], changed = Latest{Version: r.Version, Asset: r.Asset, Notes: r.Release}, true
		}
		s.mu.Unlock()
	}
	if changed {
		s.mu.Lock()
		s.saveLocked()
		s.mu.Unlock()
	}
}

// noteReleasesError keeps the first release list a check could not read for the page (B-21); a
// rate limit wins over a network error, since it says when to try again. Other errors (no package
// for this architecture, a cancelled check) are not about reading the list.
func (s *Service) noteReleasesError(err error) {
	re, ok := asReleasesError(err)
	if !ok {
		return
	}
	now := s.clock()
	n := &CheckNotice{Code: re.Code(), Repo: re.Repo, Message: re.message(now), At: now}
	if !re.RetryAt.IsZero() {
		at := re.RetryAt
		n.RetryAt = &at
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.cache.ReleasesError; cur == nil || (cur.Code != CodeRateLimit && n.Code == CodeRateLimit) {
		s.cache.ReleasesError = n
	}
}

// Run refreshes the catalogue files and the latest releases of the manifests the box already
// knows shortly after start and then once a day, with jitter: conditional GitHub calls, no
// manifest and no star fetch - those are the user's check (D-90).
//
// Task 244: only while Daily says so (the Addons page's *Check daily*); nil = always. Nothing
// else in this package goes out on its own (B-240).
func (s *Service) Run(ctx context.Context) {
	daily := func() bool { return s.Daily == nil || s.Daily() }
	first := time.After(3*time.Minute + time.Duration(rand.Int64N(int64(5*time.Minute))))
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
			if daily() {
				s.daily(ctx)
			}
		case <-time.After(24*time.Hour + time.Duration(rand.Int64N(int64(2*time.Hour)))):
			if daily() {
				s.daily(ctx)
			}
		}
	}
}

// daily is one of Run's refreshes: the catalogue files, then the releases. Whether the published
// file could be fetched is the check's outcome (B-52): a failure counts towards the Status warning,
// a fetch that works ends the run of failures (checked stays the user's last full check).
func (s *Service) daily(ctx context.Context) {
	pubErr, err := s.loadFiles(ctx, true)
	if err != nil {
		s.noteCheck(err)
		return
	}
	s.noteCheck(pubErr)
	s.dailyReleases(ctx)
}

// dailyReleases is Run's release refresh: it says anew what it could not read (B-21), and the
// cache is written either way, so the notice survives a restart.
func (s *Service) dailyReleases(ctx context.Context) {
	s.mu.Lock()
	s.cache.ReleasesError = nil
	s.mu.Unlock()
	s.RefreshReleases(ctx)
	s.mu.Lock()
	s.saveLocked()
	s.mu.Unlock()
}

// Item finds an addon by its manifest id in the view - what the system holds, nothing fetched;
// nil when none is known.
func (s *Service) Item(ctx context.Context, id string) *Item {
	v, err := s.Fetch(ctx, false)
	if err != nil {
		return nil
	}
	for i := range v.Addons {
		if v.Addons[i].Manifest != nil && v.Addons[i].ID == id {
			return &v.Addons[i]
		}
	}
	return nil
}

// Manifest is the catalogue's manifest for an addon id - the fetched one, or the bundled adapter -
// or nil: what stands in for a package without a manifest (system.SystemdAddons.FallbackManifest).
// Nothing is fetched (B-240): the catalogue comes from the cache and the bundled copy, so the
// start of the service and a policy write never wait on the network.
func (s *Service) Manifest(id string) *manifest.Manifest {
	m, _ := s.ManifestAt(id)
	return m
}

// ManifestAt is Manifest with the release tag the fetched manifest was read at: "" for an adapter
// and for one read at the default branch (a repository without releases, or a latest release that
// lacks the file), which speak for whatever version is installed. A tag says the manifest describes
// that release only (occulited B-27): a package without a manifest of another version - an older
// one installed from a file - is not described by it.
func (s *Service) ManifestAt(id string) (*manifest.Manifest, string) {
	if it := s.Item(context.Background(), id); it != nil {
		if it.Adapter {
			return it.Manifest, ""
		}
		return it.Manifest, it.Tag
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.adapters[id], ""
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	HTMLURL    string `json:"html_url"`
	Assets     []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// releaseCache is the last releases answer per repository with its ETag.
type releaseCache struct {
	etag string
	rels []ghRelease
}

var repoRe = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

// releasesOf lists a repository's releases: conditional requests are free of GitHub's 60/h
// budget, which this box shares with every addon CGI that asks the same API and with everything
// else behind the same public address. A list it cannot read is a *ReleasesError, never the last
// answer in its place (B-21).
func (s *Service) releasesOf(ctx context.Context, repo string) ([]ghRelease, error) {
	if !repoRe.MatchString(repo) {
		return nil, fmt.Errorf("%q is not owner/repo", repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.GitHubAPI+"/repos/"+repo+"/releases?per_page=10", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	s.mu.Lock()
	cached, ok := s.releases[repo]
	s.mu.Unlock()
	if ok && cached.etag != "" {
		req.Header.Set("If-None-Match", cached.etag)
	}
	res, err := s.do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err // cancelled by the caller, not a failing GitHub
		}
		return nil, &ReleasesError{Repo: repo, Err: err}
	}
	defer res.Body.Close()
	var rels []ghRelease
	switch {
	case res.StatusCode == http.StatusNotModified && ok:
		rels = cached.rels
	case res.StatusCode == 200:
		if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&rels); err != nil {
			return nil, &ReleasesError{Repo: repo, Status: res.StatusCode, Err: err}
		}
		s.mu.Lock()
		if s.releases == nil {
			s.releases = map[string]releaseCache{}
		}
		s.releases[repo] = releaseCache{etag: res.Header.Get("ETag"), rels: rels}
		s.mu.Unlock()
	default:
		// B-21: no fallback to the last answer. A rate-limited 403 used to answer the cached list,
		// and an install then resolved the release it held - an older one than asked for, installed
		// with exit 0 and no word. The caller says it instead: an install refuses, a check keeps
		// what it showed and names the limit.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		limited, retry := rateLimited(res, body, s.clock())
		return nil, &ReleasesError{Repo: repo, RateLimited: limited, RetryAt: retry, Status: res.StatusCode}
	}
	return rels, nil
}

// clock is the time rate-limit waits are counted from; a test sets now.
func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Resolve picks the release and the asset for this architecture from a manifest's release source.
func (s *Service) Resolve(ctx context.Context, rel *manifest.Release) (*Resolved, error) {
	if rel == nil || !repoRe.MatchString(rel.GitHub) {
		return nil, errors.New("the manifest has no usable release source")
	}
	rels, err := s.releasesOf(ctx, rel.GitHub)
	if err != nil {
		return nil, err
	}
	for _, r := range rels {
		if r.Draft || (r.Prerelease && !rel.Prerelease) {
			continue
		}
		version := strings.TrimPrefix(r.TagName, "v")
		for _, pattern := range []string{rel.Assets[s.Arch], rel.Asset, rel.Fallback} {
			if pattern == "" {
				continue
			}
			re := patternRe(pattern, s.Arch)
			for _, a := range r.Assets {
				if re.MatchString(a.Name) {
					out := &Resolved{Tag: r.TagName, Version: version, Asset: a.Name, URL: a.URL, Size: a.Size, Release: r.HTMLURL}
					for _, b := range r.Assets {
						if b.Name == a.Name+".sha256" {
							out.SHA256 = b.URL
						}
					}
					return out, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("no release of %s has a package for %s", rel.GitHub, s.Arch)
}

// patternRe turns "mosquitto-{arch}-{version}.tar.gz" into a regexp; {version} matches anything
// without a slash so tags with suffixes (3.5.2-beta) still resolve.
func patternRe(pattern, arch string) *regexp.Regexp {
	q := regexp.QuoteMeta(pattern)
	q = strings.ReplaceAll(q, regexp.QuoteMeta("{arch}"), regexp.QuoteMeta(arch))
	q = strings.ReplaceAll(q, regexp.QuoteMeta("{version}"), `[^/]+`)
	return regexp.MustCompile("^" + q + "$")
}

// Progress returns the current or last install progress.
func (s *Service) Progress() *Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress == nil {
		return nil
	}
	p := *s.progress
	p.Percent = s.percentLocked(time.Now())
	return &p
}

// The bar's budget per phase. Measured on the lab box 2026-09-08: RedMatic 94.3 MB installed
// in 17.7 s, homematic-manager 45.9 MB in 5.0 s - so the install is a good half of the whole
// on a LAN download; the download half is real (bytes), the install half is paced.
const (
	pctDownloadFrom = 4
	pctDownloadTo   = 52
	pctVerify       = 54
	pctInstallFrom  = 56
	pctInstallCap   = 97 // the installer has not returned: the bar creeps up to here and waits
)

// percentLocked is Percent for the current phase at time now; s.mu held.
func (s *Service) percentLocked(now time.Time) int {
	p := s.progress
	switch p.Phase {
	case "resolving":
		return 2
	case "downloading":
		if p.Total > 0 {
			return pctDownloadFrom + int(float64(pctDownloadTo-pctDownloadFrom)*float64(p.Bytes)/float64(p.Total))
		}
		// no content length: pace by the expected download time of a typical archive at 5 MB/s
		return paced(pctDownloadFrom, pctDownloadTo-2, now.Sub(s.phaseAt).Seconds(), 20)
	case "verifying":
		return pctVerify
	case "installing":
		return paced(pctInstallFrom, pctInstallCap, now.Sub(s.phaseAt).Seconds(), s.expectedInstallLocked(p.AddonID, p.Total))
	case "done":
		return 100
	case "failed":
		return p.Percent
	}
	return 0
}

// paced moves from lo towards hi with elapsed/expected, decelerating past the expected time so
// a slow install still shows movement without ever reaching hi: 63 % of the way at the expected
// time, 86 % at twice it.
func paced(lo, hi int, elapsed, expected float64) int {
	if expected <= 0 {
		expected = 1
	}
	f := 1 - math.Exp(-elapsed/expected*1.0)
	return lo + int(float64(hi-lo)*f)
}

// expectedInstallLocked is the install duration to pace against: what this addon took last
// time, or an estimate from the archive size (0.16 s per MB plus a second, the lab box's
// numbers). s.mu held.
func (s *Service) expectedInstallLocked(id string, bytes int64) float64 {
	if s.timings == nil {
		s.loadTimingsLocked()
	}
	if v, ok := s.timings[id]; ok && v > 0 {
		return v
	}
	return 1 + 0.16*float64(bytes)/1e6
}

func (s *Service) loadTimingsLocked() {
	s.timings = map[string]float64{}
	if s.TimingsFile == "" {
		return
	}
	if b, err := os.ReadFile(s.TimingsFile); err == nil {
		_ = json.Unmarshal(b, &s.timings)
	}
}

// rememberInstallLocked keeps the measured install time of an addon (a mean with the previous
// one, so one slow run does not set the pace forever). s.mu held.
func (s *Service) rememberInstallLocked(id string, seconds float64) {
	if s.timings == nil {
		s.loadTimingsLocked()
	}
	if prev, ok := s.timings[id]; ok && prev > 0 {
		seconds = (prev + seconds) / 2
	}
	s.timings[id] = seconds
	if s.TimingsFile != "" {
		if b, err := json.MarshalIndent(s.timings, "", "  "); err == nil {
			_ = os.WriteFile(s.TimingsFile, b, 0o644)
		}
	}
}

func (s *Service) setPhase(phase, msg string) {
	s.mu.Lock()
	if s.progress != nil {
		s.progress.Phase, s.progress.Message = phase, msg
		s.phaseAt = time.Now()
	}
	s.mu.Unlock()
}

// ErrInstallRunning refuses a second catalogue install while one runs.
var ErrInstallRunning = errors.New("an install is already running")

// Install resolves, downloads, verifies and installs one catalogue addon from its manifest's
// release source; one at a time. The installer applies the package's own manifest; the
// catalogue's stands in for a package without one (system.SystemdAddons.FallbackManifest).
func (s *Service) Install(ctx context.Context, id string) (*Progress, error) {
	if err := s.begin(id); err != nil {
		return nil, err
	}
	return s.run(ctx, id)
}

// Start is Install in the background: the one install slot is taken for id before it returns -
// ErrInstallRunning when an install runs - and done receives the install's error once it ends
// (nil on success). A caller that answers "started" does so only for an install that runs
// (B-25: three starts in the same second each answered 202, and two of them did nothing).
func (s *Service) Start(ctx context.Context, id string) (<-chan error, error) {
	if err := s.begin(id); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.run(ctx, id)
		done <- err
	}()
	return done, nil
}

// begin takes the install slot for id: the progress of a new run, from now on what Progress
// answers and what a second begin is refused by.
func (s *Service) begin(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress != nil && s.progress.Finished == nil {
		return ErrInstallRunning
	}
	s.progress = &Progress{AddonID: id, Phase: "resolving", Started: time.Now()}
	s.phaseAt = time.Now()
	return nil
}

// run is the install whose slot begin took.
func (s *Service) run(ctx context.Context, id string) (*Progress, error) {
	fail := func(err error) (*Progress, error) {
		now := time.Now()
		s.mu.Lock()
		s.progress.Phase, s.progress.Message, s.progress.Finished = "failed", err.Error(), &now
		// B-37: a failed install is said in the journal too, not only in the progress
		slog.Warn("catalog: install failed", "addon", id, "err", err)
		if re, ok := asReleasesError(err); ok {
			// B-21: the list could not be read, so nothing was resolved and nothing installed -
			// said with a code the page translates and GitHub's wait
			s.progress.Error, s.progress.RetryMinutes = re.Code(), re.RetryMinutes(s.clock())
			s.progress.Message = re.message(s.clock()) + "; nothing was installed"
		}
		p := *s.progress
		s.mu.Unlock()
		return &p, err
	}
	it := s.Item(ctx, id)
	if it == nil {
		return fail(errors.New("unknown addon: run a check first, so that its manifest is known"))
	}
	if !it.SupportsArch(s.Arch) {
		return fail(fmt.Errorf("%s is not available for %s", it.Name.In("en"), s.Arch))
	}
	// the release list is read now, never taken from an earlier answer (B-21): an install that
	// cannot read it refuses instead of installing whatever release the last list named
	r, err := s.Resolve(ctx, it.Release)
	if err != nil {
		if _, ok := asReleasesError(err); ok {
			slog.Warn("catalog: install refused, the release list could not be read", "addon", id, "err", err)
		}
		return fail(err)
	}
	s.setPhase("downloading", r.Asset)
	tmp, err := os.CreateTemp("", "occulite-catalog-*.tar.gz")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(tmp.Name())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return fail(err)
	}
	res, err := s.do(req)
	if err != nil {
		return fail(err)
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return fail(statusError("download", res))
	}
	h := sha256.New()
	counter := &countingWriter{w: io.MultiWriter(tmp, h), s: s, total: res.ContentLength}
	_, err = io.Copy(counter, io.LimitReader(res.Body, 400<<20))
	res.Body.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	s.mu.Lock()
	s.progress.Bytes, s.progress.Total = counter.n, max(counter.total, counter.n) // the final count, whatever the last tick said
	s.mu.Unlock()
	if r.SHA256 != "" {
		s.setPhase("verifying", "sha256")
		want, err := s.fetchSHA(ctx, r.SHA256)
		if err != nil {
			return fail(fmt.Errorf("checksum: %w", err))
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return fail(fmt.Errorf("checksum mismatch: got %s, release says %s", got, want))
		}
	}
	s.setPhase("installing", r.Asset)
	slog.Info("catalog: installing", "addon", id, "asset", r.Asset, "version", r.Version, "bytes", counter.n)
	f, err := os.Open(tmp.Name())
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	result, err := s.Installer.Install(context.WithValue(ctx, addonIDKey{}, id), f)
	if err != nil {
		return fail(err)
	}
	now := time.Now()
	s.mu.Lock()
	s.rememberInstallLocked(id, now.Sub(s.phaseAt).Seconds())
	s.progress.Phase, s.progress.Message, s.progress.Finished, s.progress.Result = "done", r.Asset, &now, result
	s.progress.Percent = 100
	p := *s.progress
	s.mu.Unlock()
	slog.Info("catalog: installed", "addon", id, "version", r.Version, "result", result)
	return &p, nil
}

func (s *Service) fetchSHA(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	res, err := s.do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", statusError("the checksum file", res)
	}
	sc := bufio.NewScanner(io.LimitReader(res.Body, 4096))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 0 && regexp.MustCompile(`^[0-9a-fA-F]{64}$`).MatchString(f[0]) {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", errors.New("no sha256 in the checksum file")
}

type countingWriter struct {
	w     io.Writer
	s     *Service
	n     int64
	total int64
	last  time.Time
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if time.Since(c.last) > 300*time.Millisecond {
		c.last = time.Now()
		c.s.mu.Lock()
		if c.s.progress != nil {
			c.s.progress.Bytes, c.s.progress.Total = c.n, c.total
		}
		c.s.mu.Unlock()
	}
	return n, err
}
