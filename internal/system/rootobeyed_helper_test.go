package system

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// withHelper runs the privilege helper with the box's policy over r and makes it Priv, as on a
// box where occulited runs as occulite: what the daemon asks for has to pass the boundary.
func withHelper(t *testing.T, r Root) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := &priv.Server{Policy: priv.DefaultPolicy(string(r), "/usr/local/etc/occulite")}
	go func() { _ = srv.Serve(ctx, l) }()
	old := Priv
	Priv = priv.Client{Socket: sock}
	t.Cleanup(func() { Priv = old; cancel() })
}

// openccu-lite B-293: the daemon's own writes of what root runs or obeys - the policy drop-in, the
// start order, the early start, an addon's enabled bit, the uninstall's removal, the NEO Server's
// leftovers - pass the helper through its named operations, and the generic ones are refused.
func TestRootObeyedWritesThroughTheHelper(t *testing.T) {
	r := fakeRoot(t)
	withHelper(t, r)
	conf := r.join(AddonPolicyDir + "/mosquitto.conf")

	p := &AddonPolicy{ID: "mosquitto", Mode: "confined", UID: 30001, User: "addon-mosquitto", Runtime: &AddonRuntime{Capabilities: []string{"CAP_NET_BIND_SERVICE"}}}
	if err := r.writePolicyDropIn(p); err != nil {
		t.Fatal(err)
	}
	if got := readFile(conf); got != renderDropIn(p, r.HasGroup, "") || !strings.Contains(got, "User=addon-mosquitto\n") {
		t.Errorf("drop-in:\n%s", got)
	}
	if err := Priv.WriteFile(conf, []byte("[Service]\nUser=root\n"), 0o644); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("generic write of the drop-in: %v", err)
	}
	if err := Priv.Remove(conf); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("generic remove of the drop-in: %v", err)
	}
	// a stored policy that names a uid of the system's is not rendered at all
	if err := r.writePolicyDropIn(&AddonPolicy{ID: "mosquitto", Mode: "confined", UID: 0, User: "addon-mosquitto"}); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("uid 0: %v", err)
	}
	if !strings.Contains(readFile(conf), "uid=30001") {
		t.Errorf("the drop-in changed:\n%s", readFile(conf))
	}

	if did, err := r.writeAddonNeeds("mosquitto", &AddonRuntime{Needs: &[]string{"hmipserver", "rfd"}}); !did || err != nil {
		t.Fatal(did, err)
	}
	if got := readFile(r.join(AddonPolicyDir + "/mosquitto.needs")); got != "rfd hmipserver\n" {
		t.Errorf("needs %q", got)
	}
	if did, err := r.writeAddonNeeds("mosquitto", &AddonRuntime{Needs: &[]string{}}); !did || err != nil || readFile(r.join(AddonPolicyDir+"/mosquitto.needs")) != "none\n" {
		t.Errorf("none: %v %v", did, err)
	}
	a := NewSystemdAddons(r, SystemdServices{Root: r})
	if did, err := a.writeAddonStart("mosquitto", &AddonRuntime{Start: AddonStartEarly}); !did || err != nil || readFile(r.join(AddonPolicyDir+"/mosquitto.start")) != "early\n" {
		t.Errorf("start: %v %v", did, err)
	}
	if err := r.writeAddonPolicy(p); err != nil { // the stored policy stays the daemon's own file
		t.Fatal(err)
	}
	removed := r.removeAddonPolicyFiles("mosquitto")
	if len(removed) != 4 {
		t.Errorf("removed %v", removed)
	}

	// enabled and disabled through the helper; a generic chmod of rc.d is refused
	if err := r.SetAddonEnabled("mosquitto", false); err != nil || r.AddonEnabled("mosquitto") {
		t.Fatalf("disable: %v", err)
	}
	if err := r.SetAddonEnabled("mosquitto", true); err != nil || !r.AddonEnabled("mosquitto") {
		t.Fatalf("enable: %v", err)
	}
	rcd := r.join("/usr/local/etc/config/rc.d/mosquitto")
	if err := Priv.Chmod(rcd, 0o755); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("generic chmod of rc.d: %v", err)
	}
	if err := Priv.WriteFile(rcd, []byte("#!/bin/sh\nid\n"), 0o755); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("generic write of rc.d: %v", err)
	}

	// the uninstall: the script through the helper's program list, the entry and the web link
	// through its own operation
	if err := os.MkdirAll(r.join("/usr/local/addons/mosquitto/www"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.join(AddonWWW), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.join("/usr/local/addons/mosquitto/www"), r.join(AddonWWW+"/mosquitto")); err != nil {
		t.Fatal(err)
	}
	if err := Priv.WriteFile(r.join("/usr/local/addons/mosquitto/www/index.cgi"), []byte("x"), 0o755); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("generic write of a CGI behind the web link: %v", err)
	}
	res, err := AddonScripts{Root: r}.Uninstall(t.Context(), "mosquitto")
	if err != nil || res.Output != "mosquitto uninstall" {
		t.Fatalf("uninstall: %+v %v", res, err)
	}
	if !slices.Contains(res.SystemRemoved, "/usr/local/etc/config/rc.d/mosquitto") || !slices.Contains(res.SystemRemoved, AddonWWW+"/mosquitto") {
		t.Errorf("system_removed %v", res.SystemRemoved)
	}
	for _, p := range []string{"/usr/local/etc/config/rc.d/mosquitto", AddonWWW + "/mosquitto"} {
		if _, err := os.Lstat(r.join(p)); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
}

// The NEO Server's leftovers go through the helper: its rc.d entry and its web link (which have
// different names) by the named operation first, then its directories.
func TestNeoServerLeftoverThroughTheHelper(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/addons/mediola/VERSION":          "2.10.1\n",
		"usr/local/addons/mediola/rc.d/97NeoServer": "#!/bin/sh\n",
		"usr/local/addons/mediola/www/index.html":   "<html>",
		"usr/local/etc/config/addons/mediola/.keep": "",
		"usr/local/etc/config/rc.d/97NeoServer":     "#!/bin/sh\n# wrapper\n",
		"usr/local/crontabs/root":                   "*/5 * * * * /usr/local/addons/mediola/bin/watchdog\n",
		"usr/local/etc/config/hm_addons.cfg":        "",
		"etc/config/.keep":                          "",
	})
	if err := os.MkdirAll(r.join(AddonWWW), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.join(NeoServerDir+"/www"), r.join(AddonWWW+"/mediola")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.join(NeoServerDir+"/rc.d/97NeoServer"), r.join("/usr/local/etc/config/rc.d/97NeoServer.script")); err != nil {
		t.Fatal(err)
	}
	withHelper(t, r)
	removed, err := r.RemoveLegacyWith([]string{"neoserver"}, nil, nil)
	if err != nil || !slices.Equal(removed, []string{"neoserver"}) {
		t.Fatalf("%v %v", removed, err)
	}
	for _, p := range neoServerPaths {
		if _, err := os.Lstat(r.join(p)); err == nil {
			t.Errorf("%s still there", p)
		}
	}
}

// openccu-lite B-294: /usr/local/addons/ is no write prefix any more. The daemon's three cases there
// pass the helper through their named operations - the uninstall's rmdir of the emptied directory,
// the NEO Server's Disabled marker (and its leftover's removal, above) - and a generic write into
// an addon's directory is refused.
func TestAddonHomeThroughTheHelper(t *testing.T) {
	r := fakeRoot(t)
	if err := os.MkdirAll(r.join("/usr/local/addons/mosquitto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.join(NeoServerDir+"/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.join("/usr/local/etc/config/rc.d/"+NeoServerID), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withHelper(t, r)
	for _, p := range []string{"/usr/local/addons/mosquitto/bin/mosquitto", NeoServerDir + "/bin/watchdog", "/usr/local/addons/new"} {
		if err := Priv.WriteFile(r.join(p), []byte("#!/bin/sh\nid\n"), 0o755); !errors.Is(err, priv.ErrRefused) {
			t.Errorf("generic write of %s: %v", p, err)
		}
	}
	if err := Priv.RemoveAll(r.join(NeoServerDir)); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("generic removeall of the NEO Server: %v", err)
	}
	if err := Priv.Remove(r.join("/usr/local/addons/mosquitto")); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("generic remove of an addon's directory: %v", err)
	}

	done, err := r.DisableNeoServer()
	if err != nil || len(done) != 2 || !r.NeoServerDisabled() || r.AddonEnabled(NeoServerID) {
		t.Fatalf("NEO Server: %v %v", done, err)
	}

	res, err := AddonScripts{Root: r}.Uninstall(t.Context(), "mosquitto")
	if err != nil {
		t.Fatalf("uninstall: %+v %v", res, err)
	}
	if !slices.Contains(res.SystemRemoved, "/usr/local/addons/mosquitto") {
		t.Errorf("system_removed %v", res.SystemRemoved)
	}
	if _, err := os.Lstat(r.join("/usr/local/addons/mosquitto")); err == nil {
		t.Error("the emptied directory is still there")
	}
}

// openccu-lite B-295: the helper gives an addon its tree, and opens its config directory, only when
// its own drop-in confines the addon. SetPolicy writes the confined drop-in first, then makes the
// config directory and has the tree walked; a root addon's config directory stays closed.
func TestConfinedPolicyThroughTheHelper(t *testing.T) {
	r := rootWith(t, map[string]string{
		"etc/passwd":                    "root:x:0:0::/:/bin/sh\n",
		"etc/group":                     "root:x:0:\n",
		"usr/local/addons/hmm/.keep":    "",
		"usr/local/etc/config/rc.d/hmm": "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/jp":  "#!/bin/sh\n",
		"usr/local/addons/jp/bin/x":     "#!/bin/sh\n",
	})
	withHelper(t, r)
	run := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
	if _, err := a.SetPolicy(t.Context(), "jp", "root", "catalog", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.join("/usr/local/etc/config/addons/jp"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/usr/local/etc/config/addons/jp/settings.conf", "/etc/config/addons/jp/userscript.tcl", "/usr/local/etc/config/addons/new/x"} {
		if err := Priv.WriteFile(r.join(p), []byte("id\n"), 0o755); !errors.Is(err, priv.ErrRefused) {
			t.Errorf("generic write of %s: %v", p, err)
		}
	}
	if _, err := Priv.OwnTree("jp", []string{r.join("/usr/local/addons/jp")}, 30000, priv.OwnTreeOptions{}); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("a root addon's tree to a uid: %v", err)
	}

	// confined: the drop-in first, so the helper makes the config directory and walks the tree. The
	// test's user cannot chown, so the walk itself fails (not a refusal) and the drop-in goes back
	// to what it was - none
	if os.Geteuid() == 0 {
		t.Skip("the walk's failure needs a user that cannot chown")
	}
	_, err := a.SetPolicy(t.Context(), "hmm", "confined", "user", nil)
	if err == nil || errors.Is(err, priv.ErrRefused) || !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("confined: %v", err)
	}
	if st, err := os.Stat(r.join("/usr/local/etc/config/addons/hmm")); err != nil || !st.IsDir() {
		t.Errorf("the config directory was not made: %v", err)
	}
	if _, err := os.Lstat(r.join(AddonPolicyDir + "/hmm.conf")); err == nil {
		t.Errorf("the drop-in stayed:\n%s", readFile(r.join(AddonPolicyDir+"/hmm.conf")))
	}
	if err := Priv.WriteFile(r.join("/usr/local/etc/config/addons/hmm/x"), []byte("y"), 0o755); !errors.Is(err, priv.ErrRefused) {
		t.Errorf("an addon's config directory without its drop-in: %v", err)
	}
}
