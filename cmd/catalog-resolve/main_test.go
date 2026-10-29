package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/resolved"
)

// The Action's command: a file a system accepts, written whole; a run that resolves nothing (no
// token, no network) writes nothing and fails, so the Action keeps the last good file.
func TestRun(t *testing.T) {
	broken := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken && strings.HasPrefix(r.URL.Path, "/repos/") {
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/repos/o/a/releases":
			_, _ = w.Write([]byte(`[{"tag_name":"v1.0.0","assets":[]}]`))
		case "/repos/o/a", "/repos/o/lite":
			_, _ = w.Write([]byte(`{"stargazers_count": 3}`))
		case "/repos/o/lite/releases":
			_, _ = w.Write([]byte(`[]`))
		case "/o/a/v1.0.0/openccu-lite.json":
			_, _ = w.Write([]byte(`{"format": 1, "id": "a", "name": "A", "release": {"github": "o/a", "asset": "a-{version}.tgz"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	cat := filepath.Join(dir, "catalog.json")
	_ = os.WriteFile(cat, []byte(`{"format":1,"addons":[{"git":"https://github.com/o/a","manifest":"openccu-lite.json"}]}`), 0o644)
	out := filepath.Join(dir, "catalog-resolved.json")
	args := []string{"--catalog", "file://" + cat, "--out", out, "--feed-repo", "o/lite", "--api", srv.URL, "--raw", srv.URL}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	f, err := resolved.Parse(b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rels, ok := f.Releases("o/a", 10); !ok || len(rels) != 1 {
		t.Errorf("o/a: %v %v", rels, ok)
	}
	if rels, ok := f.Releases("o/lite", 100); !ok || len(rels) != 0 {
		t.Errorf("o/lite: %v %v", rels, ok)
	}
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["generated_at"] == nil || raw["catalog"] != "file://"+cat {
		t.Errorf("%v", raw)
	}
	broken = true
	_ = os.Remove(out)
	if err := run(args); err == nil {
		t.Error("a run that resolved nothing succeeded")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a file was written")
	}
}

// occulited task 130: an image the repository lacks (ccu-addon-mui's dist/icon.svg) is read from the
// aarch64 package as a system's check reads it (task 128), so the file says "ok" for it; an image the
// package lacks too stays "not fetched", and no other architecture's package is downloaded.
func TestRunImageFromThePackage(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range [][2]string{{"./openccu-lite.json", "{}"}, {"./dist/icon.svg", svg}} {
		_ = tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(f[1]))
	}
	_ = tw.Close()
	_ = gz.Close()
	pkg := buf.Bytes()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/ft/mui/releases":
			_, _ = w.Write([]byte(`[{"tag_name": "v1.0.25", "assets": [
				{"name": "mui-1.0.25-aarch64-lite.tar.gz", "size": 100, "browser_download_url": "` + srv.URL + `/dl/aarch64"},
				{"name": "mui-1.0.25-x86_64-lite.tar.gz", "size": 100, "browser_download_url": "` + srv.URL + `/dl/x86_64"}]}]`))
		case "/repos/ft/mui", "/repos/o/lite":
			_, _ = w.Write([]byte(`{"stargazers_count": 1}`))
		case "/repos/o/lite/releases":
			_, _ = w.Write([]byte(`[]`))
		case "/ft/mui/v1.0.25/addon_installer/openccu-lite.json":
			_, _ = w.Write([]byte(`{"format": 1, "id": "mui", "name": "MUI", "release": {"github": "ft/mui", "asset": "mui-{version}-{arch}-lite.tar.gz"},
				"ui": {"icon": "dist/icon.svg", "logo": "dist/logo.svg"}}`))
		case "/dl/aarch64":
			_, _ = w.Write(pkg)
		case "/dl/x86_64":
			t.Error("the package of another architecture was downloaded")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	cat := filepath.Join(dir, "catalog.json")
	_ = os.WriteFile(cat, []byte(`{"format":1,"addons":[{"git":"https://github.com/ft/mui","manifest":"addon_installer/openccu-lite.json"}]}`), 0o644)
	out := filepath.Join(dir, "catalog-resolved.json")
	if err := run([]string{"--catalog", "file://" + cat, "--out", out, "--feed-repo", "o/lite", "--api", srv.URL, "--raw", srv.URL}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var f resolved.File
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Fatalf("entries: %+v", f.Entries)
	}
	im := f.Entries[0].Images
	if im["icon"] != "ok" {
		t.Errorf("icon: %q", im["icon"])
	}
	if im["logo"] == "ok" || im["logo"] == "" {
		t.Errorf("logo: %q", im["logo"])
	}
}
