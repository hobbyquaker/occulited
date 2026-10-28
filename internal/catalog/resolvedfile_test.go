package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/resolved"
)

// a fake GitHub: the catalogue file, one addon whose manifest is at its release tag with one image
// that answers and one that does not, the release lists and star counts, and a second repository
// for the system update feed. api counts the api.github.com calls; auth records their bearer.
type fakeGitHub struct {
	srv  *httptest.Server
	api  atomic.Int32
	mu   sync.Mutex
	auth map[string]string // path → Authorization of the last call
	file []byte            // the resolved catalogue served at /resolved.json
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{auth: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth[r.URL.Path] = r.Header.Get("Authorization")
		file := f.file
		f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			f.api.Add(1)
		}
		switch r.URL.Path {
		case "/catalog/catalog.json":
			_, _ = w.Write([]byte(`{"format": 1, "addons": [{"git": "https://github.com/hc/mosq", "manifest": "addon_files/openccu-lite.json"}]}`))
		case "/repos/hc/mosq/releases":
			if r.URL.Query().Get("per_page") != "10" {
				t.Errorf("an addon's list asked with %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"tag_name": "v2.1.2", "name": "2.1.2", "published_at": "2026-10-01T00:00:00Z", "html_url": "https://github.com/hc/mosq/releases/tag/v2.1.2", "assets": [{"name": "mosquitto-x86_64-2.1.2.tar.gz", "size": 10, "browser_download_url": "` + f.srv.URL + `/pkg"}]}]`))
		case "/repos/hc/mosq":
			_, _ = w.Write([]byte(`{"stargazers_count": 34}`))
		case "/repos/o/lite/releases":
			if r.URL.Query().Get("per_page") != "100" {
				t.Errorf("the feed asked with %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"tag_name": "v1.0.1", "assets": [{"name": "openccu-lite-x86_64-ova-1.0.1.zip", "size": 1, "browser_download_url": "x"}]}]`))
		case "/repos/o/lite":
			_, _ = w.Write([]byte(`{"stargazers_count": 5}`))
		case "/hc/mosq/v2.1.2/addon_files/openccu-lite.json":
			_, _ = w.Write([]byte(`{"format": 1, "id": "mosquitto", "name": "Mosquitto", "release": {"github": "hc/mosq", "asset": "mosquitto-{arch}-{version}.tar.gz"}, "ui": {"icon": "www/icon.svg", "logo": "www/logo.png"}}`))
		case "/hc/mosq/v2.1.2/addon_files/www/icon.svg":
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"></svg>`))
		case "/resolved.json":
			if file == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(file)
		default:
			http.NotFound(w, r) // the logo
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) service(resolvedURL string) *Service {
	s := New([]string{f.srv.URL + "/catalog/catalog.json"}, "x86_64", nil)
	s.GitHubAPI, s.RawGitHub, s.ResolvedURL = f.srv.URL, f.srv.URL, resolvedURL
	return s
}

func (f *fakeGitHub) serve(t *testing.T, file *resolved.File) {
	t.Helper()
	b, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.file = b
	f.mu.Unlock()
}

// occulited task 124: the resolver runs the catalogue's own check against the API with its token
// and writes down the answers; the token goes to the API alone, never to the raw host.
func TestBuildResolved(t *testing.T) {
	gh := newFakeGitHub(t)
	s := gh.service("")
	s.Token = "secret-token"
	f, err := BuildResolved(t.Context(), s, []string{"o/lite"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if f.Format != resolved.Format || f.Valid(time.Now()) != nil || f.Catalog != gh.srv.URL+"/catalog/catalog.json" {
		t.Fatalf("%+v", f)
	}
	mosq, lite := f.Repos["hc/mosq"], f.Repos["o/lite"]
	if len(mosq.Releases) != 1 || mosq.Releases[0].TagName != "v2.1.2" || mosq.Releases[0].PublishedAt == "" || mosq.Stars == nil || *mosq.Stars != 34 || mosq.PerPage != 10 {
		t.Errorf("hc/mosq: %+v", mosq)
	}
	if len(lite.Releases) != 1 || lite.PerPage != 100 || lite.Stars == nil || *lite.Stars != 5 {
		t.Errorf("o/lite: %+v", lite)
	}
	if len(f.Entries) != 1 {
		t.Fatalf("%+v", f.Entries)
	}
	e := f.Entries[0]
	if e.ID != "mosquitto" || e.Tag != "v2.1.2" || e.ReleaseRepo != "hc/mosq" || e.Error != "" || e.Images["icon"] != "ok" || !strings.HasPrefix(e.Images["logo"], "not fetched") {
		t.Errorf("entry: %+v", e)
	}
	gh.mu.Lock()
	defer gh.mu.Unlock()
	for p, a := range gh.auth {
		if strings.HasPrefix(p, "/repos/") != (a == "Bearer secret-token") {
			t.Errorf("%s: Authorization %q", p, a)
		}
	}
	// a resolver reads the API, never an older file
	if _, err := BuildResolved(t.Context(), gh.service(gh.srv.URL+"/resolved.json"), nil, 100); err == nil {
		t.Error("built from a resolved file")
	}
}

// A system's check with the resolved file: no API call at all, the same answers as the API path;
// a stale file, one of another format, a missing file or one without the repository leaves it to
// the API; a system sends no token.
func TestRefreshFromResolved(t *testing.T) {
	gh := newFakeGitHub(t)
	built, err := BuildResolved(t.Context(), gh.service(""), []string{"o/lite"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, file *resolved.File, wantAPI bool) {
		t.Helper()
		if file != nil {
			gh.serve(t, file)
		} else {
			gh.mu.Lock()
			gh.file = nil
			gh.mu.Unlock()
		}
		gh.api.Store(0)
		s := gh.service(gh.srv.URL + "/resolved.json")
		if err := s.Refresh(t.Context()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		v, _ := s.Fetch(t.Context(), false)
		if len(v.Addons) != 1 {
			t.Fatalf("%s: %+v", name, v.Addons)
		}
		it := v.Addons[0]
		if it.Manifest == nil || it.Tag != "v2.1.2" || it.Stars != 34 || it.Latest == nil || it.Latest.Version != "2.1.2" {
			t.Errorf("%s: %+v latest=%+v", name, it, it.Latest)
		}
		if got := gh.api.Load() > 0; got != wantAPI {
			t.Errorf("%s: %d API calls", name, gh.api.Load())
		}
		if s.ResolvedInUse() == wantAPI && name != "without the repository" && name != "a repository the build could not read" {
			t.Errorf("%s: in use %v", name, s.ResolvedInUse())
		}
		// an install resolves from the same list
		if r, err := s.Resolve(t.Context(), it.Release); err != nil || r.Version != "2.1.2" {
			t.Errorf("%s: resolve %+v %v", name, r, err)
		}
		gh.mu.Lock()
		for p, a := range gh.auth {
			if a != "" && !strings.HasPrefix(name, "built") {
				t.Errorf("%s: %s sent %q", name, p, a)
			}
		}
		gh.mu.Unlock()
	}
	check("fresh", built, false)
	stale := *built
	stale.GeneratedAt = time.Now().Add(-resolved.MaxAge - time.Minute)
	check("stale", &stale, true)
	other := *built
	other.Format = 2
	check("another format", &other, true)
	check("missing", nil, true)
	without := *built
	without.Repos = map[string]resolved.Repo{"o/lite": built.Repos["o/lite"]}
	check("without the repository", &without, true)
	failed := *built
	failed.Repos = map[string]resolved.Repo{"hc/mosq": {PerPage: 10, Error: "HTTP 502"}}
	check("a repository the build could not read", &failed, true)
}
