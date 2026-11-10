package priv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// occulited B-136: the helper removes an addon's directory with everything in it - the uninstall of
// a disabled addon whose own script failed - and nothing else: only AddonHomeDir + <id> itself,
// only once the addon's rc.d entry and the script beside the wrapper are gone, never through a
// link, never a config directory, never the NEO Server's (which has its own operation).
func TestAddonHomeRemoveAll(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for _, d := range []string{"usr/local/addons/watchdog/bin", "usr/local/addons/other/bin", "usr/local/addons/mediola/bin", "usr/local/addons/wrapped",
		"usr/local/etc/config/rc.d", "usr/local/etc/config/addons/watchdog", "usr/local/etc/config/addons/www/watchdog", "usr/local/data", "etc/config"} {
		if err := os.MkdirAll(at(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"usr/local/addons/watchdog/bin/daemon", "usr/local/addons/other/bin/daemon", "usr/local/addons/mediola/bin/x", "usr/local/addons/wrapped/x",
		"usr/local/etc/config/rc.d/watchdog", "usr/local/etc/config/rc.d/other", "usr/local/etc/config/rc.d/wrapped.script", "usr/local/etc/config/addons/watchdog/settings", "usr/local/data/keep"} {
		if err := os.WriteFile(at(f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite")}
	ctx := context.Background()
	refused := func(rel string) {
		t.Helper()
		if r := srv.do(ctx, request{Op: opAddonHomeRemoveAll, Path: at(rel)}); !strings.HasPrefix(r.Error, "refused") {
			t.Errorf("%s: %+v", rel, r)
		}
	}
	gone := func(rel string) bool { _, err := os.Lstat(at(rel)); return os.IsNotExist(err) }

	// still installed: its rc.d entry is there, or only the script beside a wrapper
	refused("usr/local/addons/watchdog")
	refused("usr/local/addons/wrapped")
	if err := os.Remove(at("usr/local/etc/config/rc.d/watchdog")); err != nil {
		t.Fatal(err)
	}
	// nothing but the addon's own directory
	for _, rel := range []string{"usr/local/addons", "usr/local/addons/", "usr/local/addons/watchdog/bin", "usr/local/addons/watchdog/../other", "usr/local/addons/../data",
		"usr/local/etc/config/addons/watchdog", "usr/local/etc/config/addons/www/watchdog", "etc/config/addons/watchdog", "usr/local/data", "usr/local/addons/mediola",
		"usr/local/addons/.hidden", "usr/local/etc/config/rc.d"} {
		refused(rel)
	}
	if gone("usr/local/addons/watchdog/bin/daemon") || gone("usr/local/addons/other/bin/daemon") || gone("usr/local/data/keep") || gone("usr/local/etc/config/addons/watchdog/settings") {
		t.Fatal("a refused request removed something")
	}
	if r := srv.do(ctx, request{Op: opAddonHomeRemoveAll, Path: at("usr/local/addons/watchdog")}); r.Error != "" {
		t.Fatalf("the uninstalled addon's directory: %s", r.Error)
	}
	if !gone("usr/local/addons/watchdog") || gone("usr/local/addons/other/bin/daemon") || gone("usr/local/etc/config/addons/watchdog/settings") {
		t.Error("the directory stayed, or more than it went")
	}
	// a missing one is done
	if r := srv.do(ctx, request{Op: opAddonHomeRemoveAll, Path: at("usr/local/addons/watchdog")}); r.Error != "" {
		t.Errorf("a missing directory: %s", r.Error)
	}
	// a link as the addon's directory: the link goes, never what it leads to; links inside are
	// removed, not followed
	if err := os.Symlink(at("usr/local/data"), at("usr/local/addons/linked")); err != nil {
		t.Fatal(err)
	}
	if r := srv.do(ctx, request{Op: opAddonHomeRemoveAll, Path: at("usr/local/addons/linked")}); r.Error != "" {
		t.Errorf("a link: %s", r.Error)
	}
	if err := os.MkdirAll(at("usr/local/addons/inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(at("usr/local/data"), at("usr/local/addons/inner/data")); err != nil {
		t.Fatal(err)
	}
	if r := srv.do(ctx, request{Op: opAddonHomeRemoveAll, Path: at("usr/local/addons/inner")}); r.Error != "" {
		t.Errorf("a directory with a link in it: %s", r.Error)
	}
	if !gone("usr/local/addons/linked") || !gone("usr/local/addons/inner") || gone("usr/local/data/keep") {
		t.Error("a link was followed, or stayed")
	}
	// the addons' tree reached through a link out of it is not the addons' tree
	if err := os.MkdirAll(at("usr/local/elsewhere/victim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(at("usr/local/addons"), at("usr/local/addons.real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(at("usr/local/elsewhere"), at("usr/local/addons")); err != nil {
		t.Fatal(err)
	}
	refused("usr/local/addons/victim")
	if gone("usr/local/elsewhere/victim") {
		t.Error("removed through a link out of the addons' tree")
	}
}
