// Package sysupdate is the release feed check for the system firmware (task 16): what
// checkFirmwareUpdate.sh did for OpenCCU - ask the releases API once a day, pick the asset for
// this product, compare versions - plus a download that stages the asset through the same path
// an upload takes, checked against the published sha256.
package sysupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/httpwait"
	"github.com/hobbyquaker/occulited/internal/prerelease"
	"github.com/hobbyquaker/occulited/internal/resolved"
	"github.com/hobbyquaker/occulited/internal/system"
)

// feedErrorText is what an error answer of the feed says, for the Status page (B-115): GitHub's
// JSON carries a "message" beside a documentation URL, and the raw body was one run of text wider
// than a phone ("API rate limit exceeded for …"). Another body stays as it came, trimmed and cut at
// feedErrorMax characters.
func feedErrorText(body []byte) string {
	var j struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &j) == nil && strings.TrimSpace(j.Message) != "" {
		body = []byte(j.Message)
	}
	s := strings.TrimSpace(string(body))
	if r := []rune(s); len(r) > feedErrorMax {
		s = string(r[:feedErrorMax]) + "…"
	}
	return s
}

const feedErrorMax = 300

// Available is the newest release the feed offers for this product.
type Available struct {
	Version   string `json:"version"`
	Tag       string `json:"tag"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	SHA256URL string `json:"sha256_url,omitempty"`
	Published string `json:"published,omitempty"`
	Notes     string `json:"notes_url,omitempty"`
	// Newer: on openccu-lite a semantically newer version; on OpenCCU one that differs (a downgrade
	// counts, like the script).
	Newer bool `json:"newer"`
}

// State is what the API reports.
type State struct {
	Enabled bool `json:"enabled"`
	// Channel is the channel the check follows (occulited task 118): stable, beta or dev.
	Channel string `json:"channel"`
	FeedURL string `json:"feed_url"`
	Checked string `json:"checked,omitempty"`
	Error   string `json:"error,omitempty"`
	// ErrorHost and ErrorTimeout are set when the last check failed because the feed's host did
	// not answer within ErrorTimeout seconds (B-56): the page says so in the user's language.
	ErrorHost    string     `json:"error_host,omitempty"`
	ErrorTimeout int        `json:"error_timeout,omitempty"`
	Available    *Available `json:"available"`
	// NoneInChannel: the feed has releases for this system, none in its channel yet (stable before
	// the first release, task 118) - not a failed check.
	NoneInChannel bool `json:"none_in_channel,omitempty"`
	// InstalledNewer: the system runs a newer version than the newest the feed offers (a
	// prerelease round not yet published, a local build; B-57) - not "the installed one".
	InstalledNewer bool `json:"installed_newer,omitempty"`
	// Downloading is set while a release is being fetched and staged.
	Downloading string `json:"downloading,omitempty"`
	// DownloadError is why the last download failed (occulited B-111), kept until the next
	// download starts or the staged update is discarded - so a page that did not start the
	// download (a reload, a second tab) can say why the controls are back. A cancelled download
	// leaves none.
	DownloadError string `json:"download_error,omitempty"`
}

// Service checks the feed and downloads releases.
type Service struct {
	Root    system.Root
	FeedURL string       // a GitHub release list URL, or a "releases/latest" one (or any JSON of those shapes)
	HTTP    *http.Client // its timeout is the download's; a feed request has FeedTimeout
	Log     *slog.Logger
	Enabled bool
	// Channel is the stored channel (task 118): stable, beta or dev; anything else is stable.
	Channel string
	// FeedTimeout bounds one request for the feed's metadata (the release list, a .sha256) as a
	// whole, and HeaderWait a download's wait for its response header (B-56); 0 is
	// httpwait.Meta and httpwait.HeaderWait.
	FeedTimeout time.Duration
	HeaderWait  time.Duration
	// ResolvedURL is the pre-resolved catalogue (occulited task 124), which carries the feed
	// repository's release list as the Action read it with a token: asked first, FeedURL (the API)
	// only when it is missing, stale, of another format or without the list. "" asks FeedURL alone.
	ResolvedURL string

	mu          sync.Mutex
	etag        string
	checked     time.Time
	err         string
	errNoAnswer *httpwait.NoAnswerError
	available   *Available
	none        bool // the last check found releases for this system, none in its channel
	downloading string
	// cancel ends the running download (B-111: DELETE /system-update while one runs); nil
	// when none runs. downloadErr is the last download's failure, see State.DownloadError.
	cancel      context.CancelFunc
	downloadErr string
}

// New returns a service for the feed; enabled decides whether the daily check runs.
func New(root system.Root, feedURL string, enabled bool, log *slog.Logger) *Service {
	return &Service{Root: root, FeedURL: feedURL, HTTP: &http.Client{Timeout: 30 * time.Minute}, Log: log, Enabled: enabled}
}

// State reports the last check.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Enabled: s.Enabled, Channel: NormalChannel(s.Channel), FeedURL: s.FeedURL, Error: s.err, Available: s.available, NoneInChannel: s.none, Downloading: s.downloading, DownloadError: s.downloadErr}
	if av := s.available; av != nil && !av.Newer && s.Root != "" {
		if v := s.Root.ReadVersion(); v.Variant == "lite" {
			st.InstalledNewer = semverNewer(v.Full(), av.Version)
		}
	}
	if na := s.errNoAnswer; na != nil && s.err != "" {
		st.ErrorHost, st.ErrorTimeout = na.Host, na.Seconds()
	}
	if !s.checked.IsZero() {
		st.Checked = s.checked.Format(time.RFC3339)
	}
	return st
}

// Upgrade is the release the system-update-available warning names (occulited task 123): the newer
// release the last check found in the system's channel, or nil - nothing found, the daily check
// switched off, or not newer than the running version (never a downgrade, also on a feed whose
// Newer means "differs"). It reads the remembered answer only: nothing is fetched for it (D-90).
func (s *Service) Upgrade() *Available {
	st := s.State()
	av := st.Available
	if av == nil || !av.Newer || !st.Enabled {
		return nil
	}
	if s.Root != "" {
		if running := s.Root.ReadVersion().Full(); running != "" && !semverNewer(av.Version, running) {
			return nil
		}
	}
	c := *av
	return &c
}

// liteProducts maps the upstream PRODUCT that D-31 keeps in /VERSION to the openccu-lite product
// name the release files carry (D-39, D-43). It is the inverse of the case in the fork's
// board/lite/post-build.sh and its twin in the recovery system's, and the three must be changed
// together: a box whose PRODUCT is missing here cannot find its own image in the feed.
var liteProducts = map[string]string{
	"ova":  "x86_64-ova",
	"rpi3": "aarch64-rpi3",
	"rpi4": "aarch64-rpi4",
	"rpi5": "aarch64-rpi5",
	// task 34: the Proxmox CT templates, named after the board (lxc-amd64), not the product
	// (lxc-lite_amd64), as board/lxc-lite/post-release.sh writes them; a .tar.xz, and the check
	// only ever *names* the newer template - it is swapped on the host, never staged from inside
	"lxc_amd64": "lxc-amd64",
	"lxc_arm64": "lxc-arm64",
}

// assetPattern is what the feed must carry for this box. On openccu-lite (D-44) that is
// openccu-lite-<product>-<version>.zip with the lite product looked up from the upstream PRODUCT
// that D-31 keeps in /VERSION; on OpenCCU it is OpenCCU-<version>-<PRODUCT>.<ext>, the extension
// following the platform as in checkFirmwareUpdate.sh.
func assetPattern(v system.Version) (prefix, suffix string, err error) {
	if v.Product == "" {
		return "", "", errors.New("/VERSION has no PRODUCT")
	}
	ext := "zip"
	switch v.Platform {
	case "oci":
		return "", "", errors.New("a container is updated by pulling the new image, not from inside")
	case "lxc":
		ext = "tar.xz"
	}
	if v.Variant == "lite" {
		name, ok := liteProducts[v.Product]
		if !ok {
			return "", "", fmt.Errorf("no openccu-lite image for PRODUCT=%s", v.Product)
		}
		return "openccu-lite-" + name + "-", "." + ext, nil
	}
	return "OpenCCU-", "-" + v.Product + "." + ext, nil
}

// semverNewer says a is a newer version than b, both semantic versions with an optional
// prerelease (1.0.0-dev.31 < 1.0.0-alpha.0 < 1.0.0-beta.0 < 1.0.0-beta.10 < 1.0.0-rc.1 < 1.0.0 <
// 1.0.1). The prerelease tags rank dev < alpha < beta < rc, not in SemVer's ASCII order, which
// would put dev after beta (package prerelease).
// Anything that is not a semantic version falls back to "differs", which is what the old
// script did.
func semverNewer(a, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return a != b
	}
	for i := 0; i < 3; i++ {
		if pa.num[i] != pb.num[i] {
			return pa.num[i] > pb.num[i]
		}
	}
	// a release outranks any prerelease of the same numbers
	if pa.pre == "" || pb.pre == "" {
		return pa.pre == "" && pb.pre != ""
	}
	return prerelease.Compare(pa.pre, pb.pre) > 0
}

type semver struct {
	num [3]int
	pre string
}

var semverRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

func parseSemver(s string) (semver, bool) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return semver{}, false
	}
	var v semver
	for i := 0; i < 3; i++ {
		v.num[i], _ = strconv.Atoi(m[i+1])
	}
	v.pre = m[4]
	return v, true
}

// release is the part of a GitHub release the check reads.
type release struct {
	Tag         string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// decodeReleases reads a feed in either of GitHub's shapes: the release list
// (…/releases?per_page=N, the default since the first public release, task 258) or one release
// (…/releases/latest, the default before, and what a hand-made feed may still serve).
func decodeReleases(body []byte) ([]release, error) {
	t := bytes.TrimSpace(body)
	if len(t) > 0 && t[0] == '[' {
		var list []release
		if err := json.Unmarshal(t, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var one release
	if err := json.Unmarshal(t, &one); err != nil {
		return nil, err
	}
	return []release{one}, nil
}

// isPrerelease says a version carries a prerelease part (1.0.0-dev.26, 1.0.0-beta.1).
func isPrerelease(version string) bool {
	p, ok := parseSemver(version)
	return ok && p.pre != ""
}

// pick chooses the release to offer from the feed (task 258): the newest in the system's channel
// (task 118, inChannel), read from the release list, as releases/latest never returns a
// prerelease. It may be older than the running version - Newer then says false: a system on stable
// that runs a newer prerelease is offered nothing until a newer release exists (no downgrade,
// D-122). Drafts are skipped. On openccu-lite the newest version by
// semver wins, whatever order the list is in; on OpenCCU, whose versions are not semver, the
// first one in the list (GitHub lists the newest first).
func pick(rels []release, v system.Version, prefix, suffix, channel string) *Available {
	running := v.Full()
	if v.Variant != "lite" {
		channel = ChannelStable // OpenCCU's feed: releases only, as before
	}
	var best *Available
	for _, rel := range rels {
		if rel.Draft {
			continue
		}
		for _, a := range rel.Assets {
			if !strings.HasSuffix(a.Name, suffix) || !strings.HasPrefix(a.Name, prefix) || len(a.Name) <= len(prefix)+len(suffix) {
				continue
			}
			version := strings.TrimSuffix(strings.TrimPrefix(a.Name, prefix), suffix)
			if !inChannel(version, rel.Prerelease, channel) {
				break
			}
			if best != nil && (v.Variant != "lite" || !semverNewer(version, best.Version)) {
				break
			}
			newer := version != running
			if v.Variant == "lite" {
				newer = semverNewer(version, running)
			}
			av := &Available{Version: version, Tag: rel.Tag, Name: a.Name, URL: a.URL, Size: a.Size, Published: rel.PublishedAt, Notes: rel.HTMLURL, Newer: newer}
			for _, b := range rel.Assets {
				if b.Name == a.Name+".sha256" {
					av.SHA256URL = b.URL
				}
			}
			best = av
			break
		}
	}
	return best
}

// Check fetches the feed once (ETag-cached) and remembers the result.
func (s *Service) Check(ctx context.Context) error {
	v := s.Root.ReadVersion()
	prefix, suffix, err := assetPattern(v)
	if err != nil {
		s.remember(nil, err)
		return err
	}
	if rels, ok := s.resolvedReleases(ctx); ok {
		return s.take(rels, v, prefix, suffix, "")
	}
	req, explain, cancel, err := s.feedRequest(ctx, s.FeedURL)
	if err != nil {
		s.remember(nil, err)
		return err
	}
	defer cancel()
	s.mu.Lock()
	if s.etag != "" && s.available != nil {
		req.Header.Set("If-None-Match", s.etag)
	}
	s.mu.Unlock()
	res, err := s.HTTP.Do(req)
	if err != nil {
		err = explain(err)
		s.remember(nil, err)
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		s.mu.Lock()
		s.checked, s.err = time.Now(), ""
		s.mu.Unlock()
		return nil
	}
	if res.StatusCode == http.StatusNotFound {
		// GitHub answers 404 on releases/latest when a repository has no published release, which
		// is every box before release day (D-24) - and "HTTP 404 Not Found" on the Status page
		// reads like a broken box rather than "there is nothing yet".
		err := errors.New("no release published yet on the update feed")
		s.remember(nil, err)
		return err
	}
	if res.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		err := fmt.Errorf("feed: HTTP %d %s", res.StatusCode, feedErrorText(msg))
		s.remember(nil, err)
		return err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		err = fmt.Errorf("feed: %w", explain(err))
		s.remember(nil, err)
		return err
	}
	rels, err := decodeReleases(body)
	if err != nil {
		err = fmt.Errorf("feed: %w", err)
		s.remember(nil, err)
		return err
	}
	return s.take(rels, v, prefix, suffix, res.Header.Get("ETag"))
}

// take is a check's answer from a release list, the API's or the pre-resolved catalogue's: the
// newest release for this system in its channel, or why there is none. etag is the API answer's.
func (s *Service) take(rels []release, v system.Version, prefix, suffix, etag string) error {
	if len(rels) == 0 {
		// the release list of a repository with nothing published yet is an empty array
		err := errors.New("no release published yet on the update feed")
		s.remember(nil, err)
		return err
	}
	channel := s.channel()
	av := pick(rels, v, prefix, suffix, channel)
	if av == nil && pick(rels, v, prefix, suffix, ChannelAll) != nil {
		s.rememberNone() // releases for this system, none in its channel yet (stable before 1.0.0)
		return nil
	}
	if av == nil {
		err := fmt.Errorf("feed: release %s carries no %s*%s", rels[0].Tag, prefix, suffix)
		if len(rels) > 1 {
			err = fmt.Errorf("feed: no release carries %s*%s", prefix, suffix)
		}
		s.remember(nil, err)
		return err
	}
	s.mu.Lock()
	s.etag = etag
	s.mu.Unlock()
	s.remember(av, nil)
	return nil
}

// feedRepoRe is the repository of a GitHub release-list URL: …/repos/<owner>/<repo>/releases….
var (
	feedRepoRe = regexp.MustCompile(`/repos/([\w.-]+/[\w.-]+)/releases`)
	perPageRe  = regexp.MustCompile(`[?&]per_page=(\d+)`)
)

// resolvedReleases is the feed's release list from the pre-resolved catalogue (task 124), ok false
// when it cannot stand in for FeedURL: none configured, not readable, stale or of another format,
// or without the feed repository's list. Then the API answers, as before.
func (s *Service) resolvedReleases(ctx context.Context) ([]release, bool) {
	if s.ResolvedURL == "" {
		return nil, false
	}
	m := feedRepoRe.FindStringSubmatch(s.FeedURL)
	if m == nil {
		return nil, false
	}
	perPage := 1
	if pp := perPageRe.FindStringSubmatch(s.FeedURL); pp != nil {
		perPage, _ = strconv.Atoi(pp[1])
	}
	note := func(err error) ([]release, bool) {
		if s.Log != nil {
			s.Log.Info("system update: the resolved catalogue is not used; the GitHub API answers instead", "url", s.ResolvedURL, "err", err)
		}
		return nil, false
	}
	var body []byte
	if p, ok := strings.CutPrefix(s.ResolvedURL, "file://"); ok {
		b, err := os.ReadFile(p)
		if err != nil {
			return note(err)
		}
		body = b
	} else {
		req, explain, cancel, err := s.feedRequest(ctx, s.ResolvedURL)
		if err != nil {
			return note(err)
		}
		defer cancel()
		req.Header.Del("Accept")
		res, err := s.HTTP.Do(req)
		if err != nil {
			return note(explain(err))
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return note(fmt.Errorf("HTTP %d", res.StatusCode))
		}
		if body, err = io.ReadAll(io.LimitReader(res.Body, resolved.MaxSize+1)); err != nil {
			return note(explain(err))
		}
	}
	if len(body) > resolved.MaxSize {
		return note(errors.New("too large"))
	}
	f, err := resolved.Parse(body, time.Now())
	if err != nil {
		return note(err)
	}
	list, ok := f.Releases(m[1], perPage)
	if !ok {
		return note(fmt.Errorf("it holds no release list of %s", m[1]))
	}
	b, err := json.Marshal(list)
	if err != nil {
		return note(err)
	}
	rels, err := decodeReleases(b)
	if err != nil {
		return note(err)
	}
	return rels, true
}

func (s *Service) remember(av *Available, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checked = time.Now()
	s.errNoAnswer, s.none = nil, false
	if err != nil {
		s.err = err.Error()
		s.errNoAnswer, _ = httpwait.As(err)
		return
	}
	s.err, s.available, s.none = "", av, false
}

// rememberNone is a check whose feed has releases for this system, none of them in its channel.
func (s *Service) rememberNone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checked, s.errNoAnswer, s.err, s.available, s.none = time.Now(), nil, "", nil, true
}

// feedRequest is a GET for the feed's metadata, bounded by FeedTimeout (B-56); explain names a
// host that did not answer in time, cancel releases the bound once the body is read.
func (s *Service) feedRequest(ctx context.Context, u string) (*http.Request, func(error) error, context.CancelFunc, error) {
	host := u
	if req, err := http.NewRequest(http.MethodGet, u, nil); err == nil {
		host = req.URL.Host
	}
	bounded, explain, cancel := httpwait.Bound(ctx, s.FeedTimeout, host)
	req, err := http.NewRequestWithContext(bounded, http.MethodGet, u, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	return req, explain, cancel, nil
}

// SetChannel stores the channel the check follows (task 118); anything but beta or dev is stable.
func (s *Service) SetChannel(c string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c = NormalChannel(c)
	if c == NormalChannel(s.Channel) {
		return
	}
	// the remembered answer was the old channel's, and a 304 would keep it: the next check asks anew
	s.Channel, s.available, s.none, s.etag, s.checked, s.err, s.errNoAnswer = c, nil, false, "", time.Time{}, "", nil
}

// channel is the stored channel, normalised.
func (s *Service) channel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return NormalChannel(s.Channel)
}

// SetEnabled switches the daily check (task 244: the page's *Check daily*); the button's check
// runs either way.
func (s *Service) SetEnabled(on bool) {
	s.mu.Lock()
	s.Enabled = on
	s.mu.Unlock()
}

func (s *Service) enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Enabled
}

// Run checks shortly after start and then once a day with jitter - each time only when the daily
// check is on then (task 244: it can be switched while the system runs).
func (s *Service) Run(ctx context.Context) {
	first := time.After(2*time.Minute + time.Duration(rand.Int64N(int64(5*time.Minute))))
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
			if s.enabled() {
				_ = s.Check(ctx)
			}
		case <-time.After(24*time.Hour + time.Duration(rand.Int64N(int64(2*time.Hour)))):
			if s.enabled() {
				_ = s.Check(ctx)
			}
		}
	}
}

// Download fetches the available release, verifies its sha256 against the published file when
// there is one, and stages it as an upload would be. One download at a time.
func (s *Service) Download(ctx context.Context) (*system.StagedUpdate, error) {
	s.mu.Lock()
	av := s.available
	s.mu.Unlock()
	if av == nil {
		return nil, errors.New("no release known - check first")
	}
	return s.download(ctx, av)
}

// DownloadVersion fetches and stages one published version of this product's release (occulited
// task 22: `occulited update install <version>`, a downgrade among them), from either channel;
// "" is the newest of the default channel. The version may carry the tag's "v".
func (s *Service) DownloadVersion(ctx context.Context, version string) (*system.StagedUpdate, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	channel := ChannelAll
	if version == "" {
		channel = ""
	}
	list, err := s.Releases(ctx, channel)
	if err != nil {
		return nil, err
	}
	for _, av := range list.Releases {
		if version == "" || av.Version == version {
			return s.download(ctx, &av.Available)
		}
	}
	if version == "" {
		return nil, fmt.Errorf("no release in the %s channel", list.Channel)
	}
	return nil, fmt.Errorf("%w: %s", ErrNoSuchVersion, version)
}

// ErrNoSuchVersion: the feed lists no release of that version for this product.
var ErrNoSuchVersion = errors.New("the release feed has no such version for this system")

// ErrDownloadRunning: a download is running; one at a time (occulited B-111: the API answers 409).
var ErrDownloadRunning = errors.New("a download is running")

// ErrDownloadCancelled: the running download was ended by CancelDownload (B-111); nothing is
// staged, and the part file is gone.
var ErrDownloadCancelled = errors.New("the download was cancelled")

// Downloading names the release being fetched, "" when none is.
func (s *Service) Downloading() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.downloading
}

// CancelDownload ends the running download (B-111: DELETE /system-update while one runs); its
// request answers ErrDownloadCancelled. False when none runs.
func (s *Service) CancelDownload() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil {
		return false
	}
	s.cancel()
	return true
}

// ForgetDownloadError drops the remembered failure of the last download (a discard, B-111).
func (s *Service) ForgetDownloadError() {
	s.mu.Lock()
	s.downloadErr = ""
	s.mu.Unlock()
}

func (s *Service) download(ctx context.Context, av *Available) (staged *system.StagedUpdate, err error) {
	s.mu.Lock()
	if s.downloading != "" {
		s.mu.Unlock()
		return nil, ErrDownloadRunning
	}
	ctx, cancel := context.WithCancel(ctx)
	s.downloading, s.cancel, s.downloadErr = av.Name, cancel, ""
	s.mu.Unlock()
	defer func() {
		// a cancelled download is no failure to remember (B-111); any other error is, until the
		// next download starts. ctx.Err() is read before cancel() below sets it.
		if err != nil && ctx.Err() != nil {
			err = ErrDownloadCancelled
		}
		cancel()
		s.mu.Lock()
		s.downloading, s.cancel = "", nil
		if err != nil && !errors.Is(err, ErrDownloadCancelled) {
			s.downloadErr = err.Error()
		}
		s.mu.Unlock()
	}()
	want := ""
	if av.SHA256URL != "" {
		// occulited task 22: a published checksum that cannot be read is a failed download, not
		// an unverified one - the file would be staged without the check it is published for
		if want, err = s.fetchSHA256(ctx, av.SHA256URL); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, av.URL, nil)
	if err != nil {
		return nil, err
	}
	res, err := httpwait.Do(s.HTTP, req, s.HeaderWait)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("download: HTTP %d", res.StatusCode)
	}
	h := sha256.New()
	staged, err = s.Root.StageSystemUpdate(ctx, path.Base(av.Name), av.Size, io.TeeReader(res.Body, h))
	if err != nil {
		return nil, err
	}
	if want != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			s.Root.DiscardSystemUpdate()
			return nil, fmt.Errorf("sha256 mismatch: published %s, downloaded %s", want, got)
		}
		staged.Warning = strings.TrimSpace(staged.Warning + " sha256 verified")
		staged.SHA256 = want
	} else if staged.Warning == "" {
		staged.Warning = "no .sha256 published for this asset; not verified"
	}
	if s.Log != nil {
		s.Log.Info("system update staged from the feed", "file", staged.File, "size", staged.Size)
	}
	return staged, nil
}

// fetchSHA256 reads the first word of a published .sha256 file: 64 hex characters.
func (s *Service) fetchSHA256(ctx context.Context, url string) (string, error) {
	req, explain, cancel, err := s.feedRequest(ctx, url)
	if err != nil {
		return "", err
	}
	defer cancel()
	req.Header.Del("Accept")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("sha256: %w", explain(err))
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("sha256: HTTP %d", res.StatusCode)
	}
	line, _ := bufio.NewReader(io.LimitReader(res.Body, 4096)).ReadString('\n')
	want := strings.ToLower(strings.TrimSpace(strings.SplitN(strings.TrimSpace(line), " ", 2)[0]))
	if len(want) != 64 || strings.Trim(want, "0123456789abcdef") != "" {
		return "", errors.New("sha256: the published file holds no checksum")
	}
	return want, nil
}

// The channels (occulited task 118, D-122). The stored one decides what the daily check and the
// Updates page offer; the release list (task 22) may be asked for any of them.
const (
	ChannelStable = "stable" // releases only: 1.0.0, 1.1.0, 1.1.1
	ChannelBeta   = "beta"   // releases and the alpha, beta and rc prereleases
	ChannelDev    = "dev"    // everything, the -dev.N builds too
	ChannelAll    = "all"    // the release list only: every version, for an install of a named one
	// ChannelPre is the release list's name for dev before task 118 (`occulited update --pre`).
	ChannelPre = "pre"
)

// Channels are the channels a system can follow, in the order the pages offer them.
var Channels = []string{ChannelStable, ChannelBeta, ChannelDev}

// ValidChannel says c is a channel a system can follow.
func ValidChannel(c string) bool { return c == ChannelStable || c == ChannelBeta || c == ChannelDev }

// NormalChannel is the channel a stored value stands for: stable unless it names beta or dev. A
// system without one - every system before 1.0.0 - is on stable (D-122).
func NormalChannel(c string) string {
	if ValidChannel(c) {
		return c
	}
	return ChannelStable
}

// listChannel resolves a release list's channel: "" is the stored one, pre is dev.
func listChannel(c, stored string) (string, bool) {
	switch c {
	case "":
		return NormalChannel(stored), true
	case ChannelPre:
		return ChannelDev, true
	case ChannelAll:
		return ChannelAll, true
	}
	return c, ValidChannel(c)
}

// inChannel says a release of version (GitHub's pre-release flag: flagged) belongs to the channel.
// The prerelease part's first identifier decides: dev is the dev channel's alone, alpha, beta and rc
// (and a flagged release without a prerelease part, or with another tag) are beta's too.
func inChannel(version string, flagged bool, channel string) bool {
	switch channel {
	case ChannelDev, ChannelAll:
		return true
	}
	p, ok := parseSemver(version)
	pre := ok && p.pre != ""
	if !pre && !flagged {
		return true // a release: every channel
	}
	if channel != ChannelBeta {
		return false
	}
	tag, _, _ := strings.Cut(p.pre, ".")
	return tag != "dev"
}

// Direction says what installing target means on a system that runs running: "upgrade", "same",
// "downgrade", or "other" where either is not a semantic version (OpenCCU's, a renamed file).
func Direction(running, target string) string {
	_, okr := parseSemver(running)
	_, okt := parseSemver(target)
	switch {
	case !okr || !okt:
		if running == target && running != "" {
			return "same"
		}
		return "other"
	case semverNewer(target, running):
		return "upgrade"
	case semverNewer(running, target):
		return "downgrade"
	}
	return "same"
}

// Release is one entry of the release list: what the feed offers, and what installing it means.
type Release struct {
	Available
	Prerelease bool `json:"prerelease"`
	// Direction: upgrade, same, downgrade or other (Direction).
	Direction string `json:"direction"`
}

// ReleaseList is GET /system-update/releases; Default is the stored channel.
type ReleaseList struct {
	Running  string    `json:"running"`
	Channel  string    `json:"channel"`
	Default  string    `json:"default_channel"`
	Releases []Release `json:"releases"`
}

// Releases asks the feed now and lists this product's releases in the channel, newest first
// ("" is the stored channel, pre is dev). Asked for the default channel, the newest one is also what the check
// remembers, so the Updates page and the status LED see the same answer as the command line.
func (s *Service) Releases(ctx context.Context, channel string) (ReleaseList, error) {
	v := s.Root.ReadVersion()
	def := s.channel()
	ch, ok := listChannel(channel, def)
	out := ReleaseList{Running: v.Full(), Channel: ch, Default: def, Releases: []Release{}}
	if !ok {
		return out, fmt.Errorf("channel %q: stable, beta, dev or all", channel)
	}
	channel = ch
	prefix, suffix, err := assetPattern(v)
	if err != nil {
		return out, err
	}
	rels, ok := s.resolvedReleases(ctx)
	if !ok {
		var err error
		if rels, err = s.apiReleases(ctx, channel == def); err != nil {
			return out, err
		}
	}
	out.Releases = listReleases(rels, v, prefix, suffix, channel)
	if channel == def {
		if len(out.Releases) > 0 {
			av := out.Releases[0].Available
			s.remember(&av, nil)
		} else if len(listReleases(rels, v, prefix, suffix, ChannelAll)) > 0 {
			s.rememberNone()
		} else {
			s.remember(nil, fmt.Errorf("feed: no release carries %s*%s", prefix, suffix))
		}
	}
	return out, nil
}

// apiReleases is the feed's release list from FeedURL, for Releases; remember says a host that
// did not answer is the stored channel's failed check too.
func (s *Service) apiReleases(ctx context.Context, remember bool) ([]release, error) {
	req, explain, cancel, err := s.feedRequest(ctx, s.FeedURL)
	if err != nil {
		return nil, err
	}
	defer cancel()
	res, err := s.HTTP.Do(req)
	if err != nil {
		err = explain(err)
		if _, ok := httpwait.As(err); ok && remember {
			s.remember(nil, err) // the Updates page and `update status` see the failed check too
		}
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errors.New("no release published yet on the update feed")
	}
	if res.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("feed: HTTP %d %s", res.StatusCode, feedErrorText(msg))
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("feed: %w", explain(err))
	}
	rels, err := decodeReleases(body)
	if err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	return rels, nil
}

// listReleases is every release of the feed with an asset for this product, in the channel; on
// openccu-lite sorted newest first by semver, on OpenCCU in the feed's order.
func listReleases(rels []release, v system.Version, prefix, suffix, channel string) []Release {
	running := v.Full()
	effective := channel
	if v.Variant != "lite" && channel != ChannelAll {
		effective = ChannelStable // OpenCCU's feed: releases only, as before
	}
	out := []Release{}
	seen := map[string]bool{}
	for _, rel := range rels {
		if rel.Draft {
			continue
		}
		for _, a := range rel.Assets {
			if !strings.HasSuffix(a.Name, suffix) || !strings.HasPrefix(a.Name, prefix) || len(a.Name) <= len(prefix)+len(suffix) {
				continue
			}
			version := strings.TrimSuffix(strings.TrimPrefix(a.Name, prefix), suffix)
			pre := rel.Prerelease || isPrerelease(version)
			if seen[version] || !inChannel(version, rel.Prerelease, effective) {
				break
			}
			seen[version] = true
			dir := Direction(running, version)
			if v.Variant != "lite" && dir == "other" {
				// OpenCCU's versions are no semver: the old script's "differs"
				dir = "upgrade"
			}
			r := Release{Available: Available{Version: version, Tag: rel.Tag, Name: a.Name, URL: a.URL, Size: a.Size, Published: rel.PublishedAt, Notes: rel.HTMLURL, Newer: dir == "upgrade"}, Prerelease: pre, Direction: dir}
			for _, b := range rel.Assets {
				if b.Name == a.Name+".sha256" {
					r.SHA256URL = b.URL
				}
			}
			out = append(out, r)
			break
		}
	}
	if v.Variant == "lite" {
		sort.SliceStable(out, func(i, j int) bool { return semverNewer(out[i].Version, out[j].Version) })
	}
	return out
}
