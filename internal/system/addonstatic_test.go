package system

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hobbyquaker/occulited/internal/ownwalk"
)

// B-120: the static files of an addon through os.Root - a link inside the addon's tree is fine, a
// link out of it is 404, so is a dotfile, a CGI and a directory without index.html.
func TestAddonStatic(t *testing.T) {
	dir := t.TempDir()
	root := Root(dir)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := root.join("/usr/local/addons/x")
	must(os.MkdirAll(filepath.Join(tree, "www", "sub"), 0o755))
	must(os.MkdirAll(filepath.Join(tree, "lib"), 0o755))
	must(os.MkdirAll(root.join(AddonWWW), 0o755))
	must(os.Symlink(filepath.Join(tree, "www"), root.join(filepath.Join(AddonWWW, "x"))))
	must(os.WriteFile(filepath.Join(tree, "www", "index.html"), []byte("<p>hi</p>"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "www", "app.js"), []byte("1"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "www", "sub", "index.html"), []byte("sub"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "www", ".secret"), []byte("s"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "www", "run.cgi"), []byte("puts x"), 0o755))
	must(os.WriteFile(filepath.Join(tree, "lib", "inside.txt"), []byte("in"), 0o644))
	must(os.MkdirAll(root.join("/etc"), 0o755))
	must(os.WriteFile(root.join("/etc/secret"), []byte("root-only"), 0o644))
	must(os.Symlink(filepath.Join(tree, "lib", "inside.txt"), filepath.Join(tree, "www", "inside.txt")))
	must(os.Symlink(root.join("/etc/secret"), filepath.Join(tree, "www", "outside.txt")))
	must(os.Symlink("../../../../../etc/secret", filepath.Join(tree, "www", "relout.txt")))
	srv := httptest.NewServer(CGIRunner{Root: root, Static: AddonStatic{Root: root}})
	t.Cleanup(srv.Close)
	cases := []struct {
		path string
		want int
		body string
		ct   string
	}{
		{"/addons/x/", 200, "<p>hi</p>", "text/html; charset=utf-8"},
		{"/addons/x/index.html", 200, "<p>hi</p>", ""},
		{"/addons/x/app.js", 200, "1", "text/javascript; charset=utf-8"},
		{"/addons/x/sub", 301, "", ""},
		{"/addons/x/sub/", 200, "sub", ""},
		{"/addons/x/inside.txt", 200, "in", ""},
		{"/addons/x/outside.txt", 404, "", ""},
		{"/addons/x/relout.txt", 404, "", ""},
		{"/addons/x/.secret", 404, "", ""},
		{"/addons/x/missing.js", 404, "", ""},
		{"/addons/x/../../etc/secret", 404, "", ""},
		{"/addons/x/run.cgi", 502, "", ""}, // the CGI path, which has no tclsh in this root - never the file itself
		{"/addons/y/index.html", 404, "", ""},
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, c := range cases {
		res, err := client.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 64)
		n, _ := res.Body.Read(b)
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.path, res.StatusCode, c.want)
			continue
		}
		if c.body != "" && string(b[:n]) != c.body {
			t.Errorf("%s: body %q", c.path, b[:n])
		}
		if c.ct != "" && res.Header.Get("Content-Type") != c.ct {
			t.Errorf("%s: content type %q", c.path, res.Header.Get("Content-Type"))
		}
	}
}

// B-115, end to end: a confined addon whose www is named other than its id - ccu-addon-battery's
// settings page, AddonWWW/battery-config -> /usr/local/addons/battery/www - keeps that www and the
// addon's own directory world-readable through addon-own's walk (the real ownwalk, closing the rest
// of the tree), and AddonStatic serves the page. A link into another addon's www or to the addon's
// whole tree publishes nothing, and a link out of the www still answers 404.
func TestAddonStaticConfinedWWWNamedOtherThanID(t *testing.T) {
	dir := t.TempDir()
	root := Root(dir)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := root.join("/usr/local/addons/battery")
	other := root.join("/usr/local/addons/other")
	for _, d := range []string{"www/assets", "lib", "etc"} {
		must(os.MkdirAll(filepath.Join(tree, d), 0o755))
	}
	must(os.MkdirAll(filepath.Join(other, "www"), 0o755))
	must(os.MkdirAll(root.join(AddonWWW), 0o755))
	must(os.MkdirAll(root.join(AddonPolicyDir), 0o755))
	must(os.WriteFile(root.join(AddonPolicyDir+"/battery.json"),
		[]byte(`{"id":"battery","mode":"confined","uid":30011,"user":"addon-battery","source":"catalog"}`), 0o644))
	must(os.WriteFile(filepath.Join(tree, "www", "index.html"), []byte("<p>battery</p>"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "www", "assets", "index-04T9OWL1.js"), []byte("js"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "www", "assets", "index.css"), []byte("css"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "etc", "settings.json"), []byte("{}"), 0o644))
	must(os.WriteFile(filepath.Join(tree, "lib", "inside.txt"), []byte("in"), 0o644))
	must(os.Symlink(filepath.Join(tree, "lib", "inside.txt"), filepath.Join(tree, "www", "inside.txt")))
	must(os.Symlink(filepath.Join(tree, "etc", "settings.json"), filepath.Join(tree, "www", "conf.json")))
	must(os.Symlink(filepath.Join(tree, "www"), root.join(AddonWWW+"/battery-config")))
	must(os.Symlink(tree, root.join(AddonWWW+"/battery-all")))                  // the whole tree: not public
	must(os.Symlink(filepath.Join(other, "www"), root.join(AddonWWW+"/other"))) // another addon's
	must(os.WriteFile(root.join(AddonWWW+"/plain.txt"), []byte("x"), 0o644))

	plan, err := AddonOwnPlan(root, "battery")
	if err != nil || plan == nil {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if want := []string{filepath.Join(tree, "www")}; !reflect.DeepEqual(plan.Public, want) {
		t.Fatalf("public %v, want %v", plan.Public, want)
	}
	// the real walk, as the user the test runs as (the owner is right, so only the modes change)
	res := ownwalk.Own(plan.Dirs, ownwalk.Options{UID: os.Geteuid(), GID: os.Getegid(), TightenModes: true, PublicDirs: plan.Public})
	if res.Problem() != "" {
		t.Fatalf("walk: %+v", res)
	}
	perm := func(p string) os.FileMode {
		t.Helper()
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return st.Mode().Perm()
	}
	for p, want := range map[string]os.FileMode{
		tree:                                        0o755, // the tree root AddonStatic opens
		filepath.Join(tree, "www"):                  0o755,
		filepath.Join(tree, "www/assets"):           0o755,
		filepath.Join(tree, "www/index.html"):       0o644,
		filepath.Join(tree, "www/assets/index.css"): 0o644,
		filepath.Join(tree, "etc"):                  0o751,
		filepath.Join(tree, "etc/settings.json"):    0o640,
		filepath.Join(tree, "lib/inside.txt"):       0o640,
	} {
		if got := perm(p); got != want {
			t.Errorf("%s is %o, want %o", p, got, want)
		}
	}

	srv := httptest.NewServer(CGIRunner{Root: root, Static: AddonStatic{Root: root}})
	t.Cleanup(srv.Close)
	get := func(path string) (int, string, string) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Content-Type")
	}
	check := func(label string, cases map[string]int) {
		t.Helper()
		for path, want := range cases {
			if code, _, _ := get(path); code != want {
				t.Errorf("%s: %s: %d, want %d", label, path, code, want)
			}
		}
	}
	if code, body, ct := get("/addons/battery-config/"); code != 200 || body != "<p>battery</p>" || ct != "text/html; charset=utf-8" {
		t.Errorf("the settings page: %d %q %q", code, body, ct)
	}
	if code, _, ct := get("/addons/battery-config/assets/index.css"); code != 200 || ct != "text/css; charset=utf-8" {
		t.Errorf("the css: %d %q", code, ct)
	}
	check("served from the tree", map[string]int{
		"/addons/battery-config/assets/index-04T9OWL1.js": 200,
		"/addons/battery-config/inside.txt":               200, // a link into the addon's own lib
		"/addons/battery-config/../etc/settings.json":     404,
		"/addons/battery-all/etc/settings.json":           200, // the link exists, the file is the test user's: os.Root
	})

	// a box where the tree root was closed before the fix (0751, which occulite cannot read): the
	// www is the tree then - its files are served, a link out of it into the addon's lib is 404. The
	// test owns the tree, so 0311 takes the read bit off for it as 0751 does for occulite.
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	must(os.Chmod(tree, 0o311))
	t.Cleanup(func() { _ = os.Chmod(tree, 0o755) })
	check("the tree root closed", map[string]int{
		"/addons/battery-config/":                         200,
		"/addons/battery-config/assets/index-04T9OWL1.js": 200,
		"/addons/battery-config/inside.txt":               404,
		"/addons/battery-config/conf.json":                404,
	})
}
