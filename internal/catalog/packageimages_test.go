package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// tarGz packs files (name → content; a name ending in / is a directory) in the given order.
func tarGz(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		h := &tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg}
		if strings.HasSuffix(f[0], "/") {
			h.Typeflag, h.Size, h.Mode = tar.TypeDir, 0, 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(f[1])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// occulited task 128: ccu-addon-mui's manifest (addon_installer/openccu-lite.json) declares
// dist/icon.svg, which is in the package but not in the repository. The check reads an image the
// repository answers 404 for from the release asset this system would install - once per asset: a
// second check downloads nothing, an image the package lacks too is not downloaded for again, and a
// new release is read again.
func TestImagesFromThePackage(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`
	svg2 := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="5"/></svg>`
	var downloads atomic.Int32
	var mu sync.Mutex
	tag, icon := "v1.0.25", svg
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tg, ic := tag, icon
		mu.Unlock()
		v := strings.TrimPrefix(tg, "v")
		switch r.URL.Path {
		case "/catalog/catalog.json":
			_, _ = w.Write([]byte(`{"format": 1, "addons": [{"git": "https://github.com/ft/mui", "manifest": "addon_installer/openccu-lite.json"}]}`))
		case "/repos/ft/mui/releases":
			_, _ = w.Write([]byte(`[{"tag_name": "` + tg + `", "assets": [
				{"name": "mui-` + v + `-aarch64-lite.tar.gz", "size": 100, "browser_download_url": "` + srv.URL + `/dl/aarch64"},
				{"name": "mui-` + v + `-x86_64-lite.tar.gz", "size": 100, "browser_download_url": "` + srv.URL + `/dl/x86_64/` + v + `"}]}]`))
		case "/repos/ft/mui":
			_, _ = w.Write([]byte(`{"stargazers_count": 1}`))
		case "/ft/mui/" + tg + "/addon_installer/openccu-lite.json":
			_, _ = w.Write([]byte(`{"format": 1, "id": "mui", "name": "MUI", "release": {"github": "ft/mui", "asset": "mui-{version}-{arch}-lite.tar.gz"},
				"ui": {"icon": "dist/icon.svg", "logo": "dist/logo.svg"}}`))
		case "/dl/x86_64/" + v:
			downloads.Add(1)
			_, _ = w.Write(tarGz(t, [2]string{"./", ""}, [2]string{"./openccu-lite.json", "{}"}, [2]string{"./dist/", ""}, [2]string{"./dist/icon.svg", ic}, [2]string{"./dist/big.bin", strings.Repeat("x", 4096)}))
		case "/dl/aarch64":
			t.Error("the package of another architecture was downloaded")
		default:
			http.NotFound(w, r) // the repository has no dist/: addon_installer/dist/icon.svg is 404
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	s := New([]string{srv.URL + "/catalog/catalog.json"}, "x86_64", nil)
	s.GitHubAPI, s.RawGitHub = srv.URL, srv.URL
	s.CacheFile = filepath.Join(dir, "catalog-cache.json")
	s.ImagesDir = filepath.Join(dir, "images")
	hashOf := func(b string) string {
		sum := sha256.Sum256([]byte(b))
		return hex.EncodeToString(sum[:])
	}
	check := func(want string, downloadsWant int32) {
		t.Helper()
		if err := s.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if b, err := s.Image("mui", "icon"); err != nil || string(b) != want {
			t.Fatalf("icon: %q %v", b, err)
		}
		if _, err := s.Image("mui", "logo"); err == nil {
			t.Error("a logo the package does not have")
		}
		v, _ := s.Fetch(t.Context(), false)
		if len(v.Addons) != 1 || v.Addons[0].ImageHashes["icon"] != hashOf(want) {
			t.Fatalf("view: %+v", v.Addons)
		}
		if n := downloads.Load(); n != downloadsWant {
			t.Errorf("package downloads: %d, want %d", n, downloadsWant)
		}
	}
	check(svg, 1)
	// the same release: the icon is kept, and the logo it lacks is not downloaded for again
	check(svg, 1)
	// a new release with a new icon: read again
	mu.Lock()
	tag, icon = "v1.0.26", svg2
	mu.Unlock()
	check(svg2, 2)
	check(svg2, 2)
}

// The package reader: paths with and without ./, only regular files, at most MaxSize each, and it
// stops at the last wanted file.
func TestImagesInPackage(t *testing.T) {
	big := strings.Repeat("x", 300<<10)
	pkg := tarGz(t, [2]string{"./www/", ""}, [2]string{"./www/icon.svg", "a"}, [2]string{"www/logo.png", "b"}, [2]string{"./www/huge.png", big})
	got, err := imagesInPackage(bytes.NewReader(pkg), map[string]bool{"www/icon.svg": true, "www/logo.png": true, "www/huge.png": true, "www/none.svg": true, "www": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["www/icon.svg"]) != "a" || string(got["www/logo.png"]) != "b" {
		t.Fatalf("%v", got)
	}
	if _, err := imagesInPackage(strings.NewReader("not gzip"), map[string]bool{"x": true}); err == nil {
		t.Error("not a gzip: no error")
	}
}
