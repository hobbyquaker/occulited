package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/manifest"
)

type fakeInstaller struct {
	got []byte
	id  string // AddonID of the context the install was handed
}

func (f *fakeInstaller) Install(ctx context.Context, r io.Reader) (any, error) {
	b, _ := io.ReadAll(r)
	f.got = b
	f.id = AddonID(ctx)
	return map[string]any{"exit": 0}, nil
}

func TestPatternRe(t *testing.T) {
	re := patternRe("mosquitto-{arch}-{version}.tar.gz", "x86_64")
	if !re.MatchString("mosquitto-x86_64-2.1.2+1.tar.gz") || re.MatchString("mosquitto-aarch64-2.1.2+1.tar.gz") || re.MatchString("mosquitto-x86_64-2.1.2+1.tar.gz.sha256") {
		t.Fatal("pattern")
	}
	if !patternRe("hm2mqtt-ccu-{arch}-{version}.tar.gz", "armv7l").MatchString("hm2mqtt-ccu-armv7l-3.5.2-beta.tar.gz") {
		t.Fatal("suffix version")
	}
}

// The repository's own catalogue file parses and names the adapter manifests beside it.
func TestRepositoryCatalogue(t *testing.T) {
	root := filepath.Join("..", "..", "catalog")
	s := New([]string{"file://" + filepath.Join(root, "catalog.json")}, "x86_64", nil)
	s.BundledManifests = filepath.Join(root, "manifests")
	v, err := s.Fetch(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Addons) < 8 {
		t.Fatalf("%d entries", len(v.Addons))
	}
	// the maintainer's word (2026-09-25): these are marked untested, the rest carry no label
	untested := map[string]bool{
		"https://github.com/jp112sdl/JP-HB-Devices-addon": true, "https://github.com/mdzio/ccu-jack": true,
		"https://github.com/bloop16/homekit-ccu": true,
	}
	adapters := 0
	for _, it := range v.Addons {
		if it.Untested != untested[it.Git] {
			t.Errorf("%s: untested %v", it.Git, it.Untested)
		}
		if !it.Adapter {
			if it.Manifest != nil {
				t.Errorf("%s: a manifest before any check", it.Git)
			}
			continue
		}
		adapters++
		if it.Manifest == nil || it.ID != adapterID(it.ManifestPath) || it.Release == nil {
			t.Errorf("%s: the bundled adapter is not known: %+v", it.Git, it.Manifest)
		}
	}
	if adapters != 3 {
		t.Errorf("%d adapters", adapters)
	}
	// homekit-ccu declares requires.rega: it is no exception to the ReGa scan
	if ids := RegaFreeAdapterIDs(s.BundledManifests); strings.Join(ids, ",") != "ccu-jack,jp-hb-devices-addon" {
		t.Errorf("%v", ids)
	}
	if s.Manifest("jp-hb-devices-addon") == nil || s.Manifest("nope") != nil {
		t.Error("Manifest by id")
	}
}

// writeJSON writes a file and answers its file:// URL.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return "file://" + p
}

// The catalogue files merge with the first entry per repository winning, unusable entries are
// skipped, an unreadable file lets the next stand in, and a wrong format is refused.
func TestLoadCatalog(t *testing.T) {
	dir := t.TempDir()
	published := writeFile(t, dir, "pub/catalog.json", `{"format": 1, "addons": [
		{"git": "https://github.com/o/a", "manifest": "openccu-lite.json", "verified": true},
		{"git": "https://github.com/o/b.git", "manifest": "x/openccu-lite.json"},
		{"git": "not a url", "manifest": "openccu-lite.json"},
		{"git": "https://github.com/o/c", "manifest": "../etc/passwd"},
		{"git": "https://github.com/o/d", "manifest": ""}]}`)
	bundled := writeFile(t, dir, "etc/catalog.json", `{"format": 1, "addons": [
		{"git": "https://github.com/O/A/", "manifest": "other.json", "untested": true},
		{"git": "https://github.com/o/e", "manifest": "catalog/manifests/e.json", "untested": true}]}`)
	writeFile(t, dir, "etc/manifests/e.json", `{"format": 1, "id": "e", "name": "E", "release": {"github": "o/e", "asset": "e.tgz"}}`)
	old := writeFile(t, dir, "old/index.json", `{"format": 2, "addons": []}`)
	missing := "file://" + filepath.Join(dir, "missing.json")

	s := New([]string{published, bundled}, "x86_64", nil)
	s.BundledManifests = filepath.Join(dir, "etc/manifests")
	v, err := s.Fetch(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	var gits []string
	for _, it := range v.Addons {
		gits = append(gits, it.Git)
	}
	// the untested last, each group by name: the manifest's where one is known ("E"), else the
	// repository's; the old `verified` is ignored
	if strings.Join(gits, " ") != "https://github.com/o/a https://github.com/o/b https://github.com/o/e" {
		t.Fatalf("%v", gits)
	}
	if v.Addons[0].Untested || v.Addons[0].ManifestPath != "openccu-lite.json" {
		t.Errorf("the published entry wins, flag and all: %+v", v.Addons[0])
	}
	if e := v.Addons[2]; !e.Adapter || e.Manifest == nil || e.ID != "e" || !e.Untested {
		t.Errorf("the bundled adapter is known without a check: %+v", e)
	}
	if v.Checked != nil {
		t.Error("no check yet")
	}
	// the published file unreachable: the bundled copy stands in
	s = New([]string{missing, bundled}, "x86_64", nil)
	if v, err := s.Fetch(t.Context(), false); err != nil || len(v.Addons) != 2 {
		t.Fatalf("bundled only: %v %+v", err, v)
	}
	// nothing loads: the error names the first file
	if _, err := New([]string{missing}, "x86_64", nil).Fetch(t.Context(), false); err == nil || !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("%v", err)
	}
	// the old index's format is refused
	if _, err := New([]string{old}, "x86_64", nil).Fetch(t.Context(), false); err == nil || !strings.Contains(err.Error(), "format 2") {
		t.Fatalf("%v", err)
	}
	if RepoName("https://github.com/o/a") != "o/a" {
		t.Error("RepoName")
	}
}

// The user's check: the manifest at the latest release tag from GitHub's raw host, an adapter from
// beside the catalogue file, the star counts, the latest releases - all cached to disk and answered
// from there afterwards; a failing entry keeps its last manifest and records the error.
func TestRefreshAndCache(t *testing.T) {
	var ghCalls, rawCalls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/catalog.json":
			_, _ = w.Write([]byte(`{"format": 1, "addons": [
				{"git": "https://github.com/hc/mosq", "manifest": "addon_files/openccu-lite.json"},
				{"git": "https://github.com/x/loom", "manifest": "catalog/manifests/openccu-loom.json"},
				{"git": "https://github.com/x/broken", "manifest": "openccu-lite.json"},
				{"git": "https://github.com/x/pre", "manifest": "openccu-lite.json"}]}`))
		case "/catalog/manifests/openccu-loom.json":
			_, _ = w.Write([]byte(`{"format": 1, "id": "openccu-loom", "name": "Loom", "release": {"github": "x/loom", "asset": "loom-{version}.tar.gz"}}`))
		case "/repos/hc/mosq/releases":
			ghCalls.Add(1)
			_, _ = w.Write([]byte(`[{"tag_name": "3.0.0-rc1", "prerelease": true, "assets": []}, {"tag_name": "v2.1.2", "assets": [{"name": "mosquitto-x86_64-2.1.2.tar.gz", "size": 10, "browser_download_url": "` + srv.URL + `/pkg"}]}]`))
		case "/repos/hc/mosq":
			w.Header().Set("ETag", `"s1"`)
			_, _ = w.Write([]byte(`{"stargazers_count": 34}`))
		case "/repos/x/loom/releases", "/repos/x/broken/releases":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/x/pre/releases":
			// a release from before the manifest: the file is not at its tag, the default branch has it
			_, _ = w.Write([]byte(`[{"tag_name": "v0.9.0", "assets": []}]`))
		case "/repos/x/pre":
			_, _ = w.Write([]byte(`{"stargazers_count": 1}`))
		case "/x/pre/HEAD/openccu-lite.json":
			_, _ = w.Write([]byte(`{"format": 1, "id": "pre", "name": "Pre", "release": {"github": "x/pre", "asset": "pre-{version}.tar.gz"}}`))
		case "/repos/x/loom":
			_, _ = w.Write([]byte(`{"stargazers_count": 5}`))
		case "/hc/mosq/v2.1.2/addon_files/openccu-lite.json":
			rawCalls.Add(1)
			w.Header().Set("ETag", `"m1"`)
			if r.Header.Get("If-None-Match") == `"m1"` {
				w.WriteHeader(304)
				return
			}
			_, _ = w.Write([]byte(`{"format": 1, "id": "mosquitto", "name": {"de": "Mosquitto", "en": "Mosquitto"}, "release": {"github": "hc/mosq", "asset": "mosquitto-{arch}-{version}.tar.gz"}, "runtime": {"ports": [1883]}}`))
		case "/x/broken/HEAD/openccu-lite.json":
			_, _ = w.Write([]byte(`{"format": 1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	newService := func() *Service {
		s := New([]string{srv.URL + "/catalog/catalog.json"}, "x86_64", nil)
		s.GitHubAPI, s.RawGitHub = srv.URL, srv.URL
		s.CacheFile = filepath.Join(dir, "catalog-cache.json")
		return s
	}
	s := newService()
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Fetch(t.Context(), false)
	byGit := map[string]Item{}
	for _, it := range v.Addons {
		byGit[it.Git] = it
	}
	mosq := byGit["https://github.com/hc/mosq"]
	if mosq.Manifest == nil || mosq.ID != "mosquitto" || mosq.Tag != "v2.1.2" || mosq.Stars != 34 || mosq.Latest == nil || mosq.Latest.Version != "2.1.2" || mosq.Error != "" || mosq.Fetched == nil {
		t.Fatalf("mosq: %+v latest=%+v", mosq, mosq.Latest)
	}
	if loom := byGit["https://github.com/x/loom"]; loom.Manifest == nil || loom.ID != "openccu-loom" || !loom.Adapter || loom.Stars != 5 || loom.Tag != "" {
		t.Fatalf("loom: %+v", loom)
	}
	if broken := byGit["https://github.com/x/broken"]; broken.Manifest != nil || broken.Error == "" {
		t.Fatalf("broken: %+v", broken)
	}
	// the manifest missing at the latest release tag (404): read from the default branch, no tag recorded
	if pre := byGit["https://github.com/x/pre"]; pre.Manifest == nil || pre.ID != "pre" || pre.Tag != "" || pre.Error != "" {
		t.Fatalf("pre: %+v", pre)
	}
	if v.Checked == nil {
		t.Error("checked")
	}
	if s.Item(t.Context(), "mosquitto") == nil || s.Item(t.Context(), "nope") != nil || s.Manifest("openccu-loom") == nil {
		t.Error("Item and Manifest by id")
	}
	// B-27: the tag a fetched manifest was read at goes with it; an adapter and one read at the
	// default branch speak for any version
	if m, tag := s.ManifestAt("mosquitto"); m == nil || tag != "v2.1.2" {
		t.Errorf("ManifestAt(mosquitto) = %v, %q", m, tag)
	}
	if m, tag := s.ManifestAt("openccu-loom"); m == nil || tag != "" {
		t.Errorf("ManifestAt(openccu-loom) = %v, %q", m, tag)
	}
	if m, tag := s.ManifestAt("pre"); m == nil || tag != "" {
		t.Errorf("ManifestAt(pre) = %v, %q", m, tag)
	}
	// a new process answers from the cache without any fetch
	gh, raw := ghCalls.Load(), rawCalls.Load()
	s2 := newService()
	v2, err := s2.Fetch(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range v2.Addons {
		if it.Git == "https://github.com/hc/mosq" && (it.Manifest == nil || it.Stars != 34 || it.Latest == nil) {
			t.Fatalf("not from the cache: %+v", it)
		}
	}
	if ghCalls.Load() != gh || rawCalls.Load() != raw {
		t.Error("a page load fetched something")
	}
	// the second check is conditional: the manifest's 304 keeps it
	if err := s2.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rawCalls.Load() != raw+1 {
		t.Errorf("raw calls %d, want %d", rawCalls.Load(), raw+1)
	}
	if it := s2.Item(t.Context(), "mosquitto"); it == nil || it.Error != "" || it.Runtime == nil || it.Runtime.Ports[0] != 1883 {
		t.Fatalf("after the 304: %+v", it)
	}
	// the cache file is JSON with the entries by repository
	b, err := os.ReadFile(s.CacheFile)
	if err != nil {
		t.Fatal(err)
	}
	var c cacheFile
	if json.Unmarshal(b, &c) != nil || c.Entries["https://github.com/hc/mosq"].Manifest == nil || c.Stars["hc/mosq"] != 34 || c.Latest["mosquitto"].Version != "2.1.2" {
		t.Fatalf("cache: %s", b)
	}
}

func TestRawURLsAndAdapterURL(t *testing.T) {
	if got := rawURLs("https://raw.githubusercontent.com", "https://github.com/o/r", "a/openccu-lite.json", "v1.2"); len(got) != 1 || got[0] != "https://raw.githubusercontent.com/o/r/v1.2/a/openccu-lite.json" {
		t.Errorf("%v", got)
	}
	if got := rawURLs("https://raw.githubusercontent.com", "https://github.com/o/r", "openccu-lite.json", ""); got[0] != "https://raw.githubusercontent.com/o/r/HEAD/openccu-lite.json" {
		t.Errorf("%v", got)
	}
	if got := rawURLs("", "https://git.example.org/o/r", "openccu-lite.json", ""); len(got) != 2 || got[0] != "https://git.example.org/o/r/raw/branch/main/openccu-lite.json" || got[1] != "https://git.example.org/o/r/raw/branch/master/openccu-lite.json" {
		t.Errorf("%v", got)
	}
	if got := adapterURL("https://git.example.org/o/occulited/raw/branch/master/catalog/catalog.json", "catalog/manifests/ccu-jack.json"); got != "https://git.example.org/o/occulited/raw/branch/master/catalog/manifests/ccu-jack.json" {
		t.Errorf("%s", got)
	}
	if got := adapterURL("file:///etc/occulite/catalog.json", "catalog/manifests/ccu-jack.json"); got != "file:///etc/occulite/manifests/ccu-jack.json" {
		t.Errorf("%s", got)
	}
}

// Install resolves the release from the item's manifest, skips prereleases, verifies the sha256
// sidecar and hands the archive to the installer with the addon's id; unknown ids and a foreign
// architecture are refused before any download.
func TestResolveInstall(t *testing.T) {
	pkg := []byte(strings.Repeat("addon-bytes", 100))
	sum := sha256.Sum256(pkg)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/homematic-community/ccu-addon-mosquitto/releases":
			_, _ = w.Write([]byte(`[{"tag_name":"3.0.0-rc1","draft":false,"prerelease":true,"assets":[{"name":"mosquitto-x86_64-3.0.0-rc1.tar.gz","size":1,"browser_download_url":"` + srv.URL + `/rc"}]},
			 {"tag_name":"2.1.2+1","draft":false,"prerelease":false,"html_url":"` + srv.URL + `/rel","assets":[{"name":"mosquitto-x86_64-2.1.2+1.tar.gz","size":1100,"browser_download_url":"` + srv.URL + `/pkg"},{"name":"mosquitto-x86_64-2.1.2+1.tar.gz.sha256","size":80,"browser_download_url":"` + srv.URL + `/pkg.sha256"}]}]`))
		case "/pkg":
			_, _ = w.Write(pkg)
		case "/pkg.sha256":
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  mosquitto-x86_64-2.1.2+1.tar.gz\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	cat := writeFile(t, dir, "catalog.json", `{"format": 1, "addons": [{"git": "https://github.com/homematic-community/ccu-addon-mosquitto", "manifest": "catalog/manifests/mosquitto.json"}]}`)
	writeFile(t, dir, "manifests/mosquitto.json", `{"format": 1, "id": "mosquitto", "name": "Mosquitto", "requires": {"architectures": ["x86_64", "aarch64", "armv7l"]},
		"release": {"github": "homematic-community/ccu-addon-mosquitto", "asset": "mosquitto-{arch}-{version}.tar.gz", "fallback_asset": "mosquitto-{version}.tar.gz"}}`)
	inst := &fakeInstaller{}
	s := New([]string{cat}, "x86_64", inst)
	s.GitHubAPI = srv.URL
	s.BundledManifests = filepath.Join(dir, "manifests")
	it := s.Item(t.Context(), "mosquitto")
	if it == nil {
		t.Fatal("the bundled adapter is an item")
	}
	r, err := s.Resolve(t.Context(), it.Release)
	if err != nil || r.Tag != "2.1.2+1" || r.Asset != "mosquitto-x86_64-2.1.2+1.tar.gz" || r.SHA256 == "" {
		t.Fatalf("resolve must skip the prerelease and find the sha256 sidecar: %v %+v", err, r)
	}
	p, err := s.Install(t.Context(), "mosquitto")
	if err != nil || p.Phase != "done" || string(inst.got) != string(pkg) {
		t.Fatalf("install: %v %+v", err, p)
	}
	if inst.id != "mosquitto" {
		t.Errorf("AddonID in the install's context: %q", inst.id)
	}
	if AddonID(t.Context()) != "" {
		t.Error("an id outside a catalogue install")
	}
	if _, err := s.Install(t.Context(), "nope"); err == nil {
		t.Fatal("unknown addon must fail")
	}
	s2 := New([]string{cat}, "mips", inst)
	s2.GitHubAPI = srv.URL
	s2.BundledManifests = s.BundledManifests
	if _, err := s2.Install(t.Context(), "mosquitto"); err == nil || !strings.Contains(err.Error(), "mips") {
		t.Fatalf("arch check: %v", err)
	}
	if _, err := s.Resolve(t.Context(), nil); err == nil {
		t.Error("no release source")
	}
}

func TestResolveUsesETag(t *testing.T) {
	calls := 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(304)
			return
		}
		if calls == 3 {
			w.WriteHeader(403) // rate limited on a cache miss
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`[{"tag_name":"v1.2.0","html_url":"","assets":[{"name":"x-armv7l-1.2.0.tar.gz","browser_download_url":"http://x","size":1}]}]`))
	}))
	defer gh.Close()
	s := New(nil, "armv7l", &fakeInstaller{})
	s.GitHubAPI = gh.URL
	rel := &manifest.Release{GitHub: "o/x", Asset: "x-{arch}-{version}.tar.gz"}
	r1, err := s.Resolve(t.Context(), rel)
	if err != nil || r1.Version != "1.2.0" {
		t.Fatalf("%v %+v", err, r1)
	}
	r2, err := s.Resolve(t.Context(), rel)
	if err != nil || r2.Asset != r1.Asset || calls != 2 {
		t.Fatalf("second: %v %+v calls=%d", err, r2, calls)
	}
	// B-21: a refused answer is an error, never the cached list in its place
	s.releases["o/x"] = releaseCache{etag: "", rels: s.releases["o/x"].rels}
	if r3, err := s.Resolve(t.Context(), rel); err == nil {
		t.Fatalf("a 403 must not answer the cached releases: %+v", r3)
	} else if re, ok := asReleasesError(err); !ok || re.Status != 403 {
		t.Fatalf("403: %v", err)
	}
	// the latest tag: the first stable release, else the first prerelease
	if tag, err := s.latestTag(t.Context(), "https://github.com/o/x"); err != nil || tag != "v1.2.0" {
		t.Errorf("latest tag: %q %v", tag, err)
	}
	if tag, _ := s.latestTag(t.Context(), "https://git.example.org/o/x"); tag != "" {
		t.Errorf("another host has no tag lookup: %q", tag)
	}
}

func TestProgressPercent(t *testing.T) {
	s := &Service{}
	t0 := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	s.progress = &Progress{AddonID: "x", Phase: "downloading", Bytes: 0, Total: 100e6}
	s.phaseAt = t0
	if got := s.percentLocked(t0); got != pctDownloadFrom {
		t.Errorf("download start: %d", got)
	}
	s.progress.Bytes = 50e6
	if got := s.percentLocked(t0); got != (pctDownloadFrom+pctDownloadTo)/2 {
		t.Errorf("download half: %d", got)
	}
	s.progress.Phase = "installing" // 100 MB: 17 s expected
	exp := s.expectedInstallLocked("x", 100e6)
	if exp < 16 || exp > 18 {
		t.Errorf("estimate for 100 MB: %.1f s", exp)
	}
	at0 := s.percentLocked(t0)
	atExp := s.percentLocked(t0.Add(time.Duration(exp * float64(time.Second))))
	at3 := s.percentLocked(t0.Add(time.Duration(3 * exp * float64(time.Second))))
	if at0 != pctInstallFrom || atExp <= at0 || at3 <= atExp || at3 > pctInstallCap {
		t.Errorf("install pacing: %d %d %d (cap %d)", at0, atExp, at3, pctInstallCap)
	}
	s.rememberInstallLocked("x", 4)
	if got := s.expectedInstallLocked("x", 100e6); got != 4 {
		t.Errorf("remembered: %.1f", got)
	}
	s.rememberInstallLocked("x", 8)
	if got := s.expectedInstallLocked("x", 100e6); got != 6 {
		t.Errorf("mean with the previous: %.1f", got)
	}
	s.progress.Phase = "done"
	if got := s.percentLocked(t0); got != 100 {
		t.Errorf("done: %d", got)
	}
}

// B-25: Start takes the install slot before it returns, so a second start while the first
// downloads is refused at once - never accepted and dropped - and the slot is free again once
// the first has ended.
func TestStartOneAtATime(t *testing.T) {
	pkg := []byte(strings.Repeat("addon-bytes", 100))
	release := make(chan struct{})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/homematic-community/ccu-addon-mosquitto/releases":
			_, _ = w.Write([]byte(`[{"tag_name":"2.1.2","draft":false,"prerelease":false,"assets":[{"name":"mosquitto-x86_64-2.1.2.tar.gz","size":1100,"browser_download_url":"` + srv.URL + `/pkg"}]}]`))
		case "/pkg":
			<-release
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	cat := writeFile(t, dir, "catalog.json", `{"format": 1, "addons": [{"git": "https://github.com/homematic-community/ccu-addon-mosquitto", "manifest": "catalog/manifests/mosquitto.json"}]}`)
	writeFile(t, dir, "manifests/mosquitto.json", `{"format": 1, "id": "mosquitto", "name": "Mosquitto", "requires": {"architectures": ["x86_64"]},
		"release": {"github": "homematic-community/ccu-addon-mosquitto", "asset": "mosquitto-{arch}-{version}.tar.gz"}}`)
	inst := &fakeInstaller{}
	s := New([]string{cat}, "x86_64", inst)
	s.GitHubAPI = srv.URL
	s.BundledManifests = filepath.Join(dir, "manifests")

	done, err := s.Start(t.Context(), "mosquitto")
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Progress(); p == nil || p.AddonID != "mosquitto" || p.Finished != nil {
		t.Fatalf("the slot is taken when Start returns: %+v", p)
	}
	for _, id := range []string{"hmm", "redmatic", "mosquitto"} {
		if d, err := s.Start(t.Context(), id); !errors.Is(err, ErrInstallRunning) || d != nil {
			t.Errorf("a second start (%s) while one runs: %v", id, err)
		}
	}
	if _, err := s.Install(t.Context(), "hmm"); !errors.Is(err, ErrInstallRunning) {
		t.Errorf("Install beside a Start: %v", err)
	}
	if p := s.Progress(); p.AddonID != "mosquitto" {
		t.Errorf("a refused start must not touch the running one's progress: %+v", p)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the first install: %v", err)
	}
	if p := s.Progress(); p.Phase != "done" || string(inst.got) != string(pkg) {
		t.Fatalf("after the first: %+v", p)
	}
	// the slot is free again; a failing run reports its error on done
	done, err = s.Start(t.Context(), "nope")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "unknown addon") {
		t.Errorf("the failed run's error: %v", err)
	}
}
