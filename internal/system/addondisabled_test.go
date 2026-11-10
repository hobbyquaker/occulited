package system

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/journald"
)

// occulited B-136 (hobbyquaker/openccu-lite#15): a disabled addon - its rc.d entry without the
// executable bits, as the first boot leaves an addon with foreign binaries a CCU backup brought
// along, and as "Disable at boot" leaves any - is listed with its name and version and can be
// uninstalled: its script runs, and when that fails the entry and the directory go anyway.

// disabledRig is a root with addons in rc.d: name → {script, mode}, each with a directory that
// holds a binary and a VERSION file.
func disabledRig(t *testing.T, addons map[string]struct {
	script string
	mode   os.FileMode
}) Root {
	t.Helper()
	r := rootWith(t, map[string]string{"usr/local/etc/config/hm_addons.cfg": ""})
	for id, a := range addons {
		for p, c := range map[string]string{
			"/usr/local/etc/config/rc.d/" + id:        a.script,
			"/usr/local/addons/" + id + "/bin/daemon": "\x7fELF another machine's",
			"/usr/local/addons/" + id + "/VERSION":    "0.9\n",
		} {
			if err := os.MkdirAll(filepath.Dir(r.join(p)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(r.join(p), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(r.join("/usr/local/etc/config/rc.d/"+id), a.mode); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

type rigAddon = struct {
	script string
	mode   os.FileMode
}

func exists(r Root, p string) bool { _, err := os.Lstat(r.join(p)); return err == nil }

func TestListAddonsDescribesADisabledAddon(t *testing.T) {
	r := disabledRig(t, map[string]rigAddon{
		// its info must not run: it would say another version
		"hm-watchdog": {"#!/bin/sh\necho 'Name: from info'; echo 'Version: 9.9'; echo 'Operations: restart'\n", 0o644},
		"declared":    {"#!/bin/sh\nexit 0\n", 0o644},
		"linked":      {"#!/bin/sh\nexit 0\n", 0o644},
		"wordy":       {"#!/bin/sh\nexit 0\n", 0o644},
		"on":          {"#!/bin/sh\necho 'Name: On'; echo 'Version: 1.2'; echo 'Operations: restart uninstall'\n", 0o755},
	})
	// a CCU user's own script: an rc.d entry and nothing else
	if err := os.WriteFile(r.join("/usr/local/etc/config/rc.d/hack"), []byte("#!/bin/sh\nrmmod x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.join("/usr/local/etc/config/hm_addons.cfg"), []byte("hm-watchdog {CONFIG_URL /addons/hm-watchdog/ CONFIG_DESCRIPTION {} ID hm-watchdog CONFIG_NAME {HM Watchdog}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a stored manifest names the addon and its version, before the VERSION file
	if err := os.MkdirAll(r.join(AddonPolicyDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.join(AddonPolicyDir+"/declared"+AddonManifestSuffix), []byte(`{"format": 1, "id": "declared", "version": "2.0.1", "name": {"de": "Erklärt", "en": "Declared"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// a VERSION that is a link out of the addon's tree is not read; one that is no version is not shown
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("4.5.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(r.join("/usr/local/addons/linked/VERSION"))
	if err := os.Symlink(secret, r.join("/usr/local/addons/linked/VERSION")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.join("/usr/local/addons/wordy/VERSION"), []byte("the version is 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := AddonScripts{Root: r}.ListAddons(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Addon{}
	for _, a := range list {
		got[a.ID] = a
	}
	for id, want := range map[string]Addon{
		"hm-watchdog": {Name: "HM Watchdog", Version: "0.9", Operations: []string{"uninstall"}, Directory: true},
		"declared":    {Name: "Declared", Version: "2.0.1", Operations: []string{"uninstall"}, Directory: true},
		"linked":      {Operations: []string{"uninstall"}, Directory: true},
		"wordy":       {Operations: []string{"uninstall"}, Directory: true},
		"hack":        {Operations: []string{}},
		"on":          {Name: "On", Version: "1.2", Operations: []string{"restart", "uninstall"}, Directory: true, Enabled: true},
	} {
		a := got[id]
		if a.Name != want.Name || a.Version != want.Version || !slices.Equal(a.Operations, want.Operations) || a.Directory != want.Directory || a.Enabled != want.Enabled {
			t.Errorf("%s: name %q version %q operations %v directory %v enabled %v", id, a.Name, a.Version, a.Operations, a.Directory, a.Enabled)
		}
	}
}

func TestUninstallDisabledAddon(t *testing.T) {
	ok := "#!/bin/sh\n[ \"$1\" = uninstall ] || exit 9\necho ran $1\nrm -rf \"$(dirname \"$0\")/../../../addons/$(basename \"$0\")\"\n"
	r := disabledRig(t, map[string]rigAddon{
		"works":   {ok, 0o644},
		"leaves":  {"#!/bin/sh\necho kept my files\n", 0o644},
		"fails":   {"#!/bin/sh\necho cannot\nexit 3\n", 0o644},
		"foreign": {"\x7fELF not a script here", 0o644},
		"enabled": {"#!/bin/sh\necho cannot\nexit 3\n", 0o755},
	})
	for _, p := range []string{AddonWWW + "/fails/js", "/usr/local/etc/config/addons/fails"} {
		if err := os.MkdirAll(r.join(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{AddonWWW + "/fails/js/app.js", "/usr/local/etc/config/addons/fails/settings.json"} {
		if err := os.WriteFile(r.join(p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := AddonScripts{Root: r}
	ctx := context.Background()

	// its script runs although the entry was not executable, and did the removal itself
	res, err := b.Uninstall(ctx, "works")
	if err != nil || res.Output != "ran uninstall" || res.Forced || exists(r, "/usr/local/etc/config/rc.d/works") || exists(r, "/usr/local/addons/works") {
		t.Errorf("works: %+v %v", res, err)
	}
	// a script that succeeds and keeps files keeps them, as an enabled addon's does
	res, err = b.Uninstall(ctx, "leaves")
	if err != nil || res.Forced || exists(r, "/usr/local/etc/config/rc.d/leaves") || !exists(r, "/usr/local/addons/leaves/VERSION") {
		t.Errorf("leaves: %+v %v", res, err)
	}
	// a failing script: the entry, the directory and the web tree go anyway; the settings stay
	res, err = b.Uninstall(ctx, "fails")
	if err != nil || !res.Forced || res.Output != "cannot" || !strings.Contains(res.ScriptError, "3") {
		t.Errorf("fails: %+v %v", res, err)
	}
	for _, p := range []string{"/usr/local/etc/config/rc.d/fails", "/usr/local/addons/fails", AddonWWW + "/fails"} {
		if exists(r, p) {
			t.Errorf("fails: %s is still there", p)
		}
		if !slices.Contains(res.SystemRemoved, p) {
			t.Errorf("fails: system_removed lacks %s: %v", p, res.SystemRemoved)
		}
	}
	if !exists(r, "/usr/local/etc/config/addons/fails/settings.json") {
		t.Error("fails: the settings went")
	}
	// a script that cannot run at all
	res, err = b.Uninstall(ctx, "foreign")
	if err != nil || !res.Forced || res.ScriptError == "" || exists(r, "/usr/local/etc/config/rc.d/foreign") || exists(r, "/usr/local/addons/foreign") {
		t.Errorf("foreign: %+v %v", res, err)
	}
	// an enabled addon's failed uninstall is what it was: an error, the entry gone (as the WebUI
	// does it), the directory with its files kept
	res, err = b.Uninstall(ctx, "enabled")
	if err == nil || res.Forced || exists(r, "/usr/local/etc/config/rc.d/enabled") || !exists(r, "/usr/local/addons/enabled/bin/daemon") {
		t.Errorf("enabled: %+v %v", res, err)
	}
}

// Switched off on the Services page is disabled too (B-132): its bits are set, its script runs as
// it is, and a failure removes it all the same. The journal says what happened.
func TestSystemdUninstallDisabledAddon(t *testing.T) {
	r := disabledRig(t, map[string]rigAddon{
		"off":    {"#!/bin/sh\necho cannot\nexit 4\n", 0o755},
		"broken": {"#!/bin/sh\necho no\nexit 5\n", 0o644},
		"on":     {"#!/bin/sh\necho cannot\nexit 4\n", 0o755},
	})
	run := func(_ context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	sd := SystemdServices{Root: r, Run: run, SwitchFile: filepath.Join(t.TempDir(), "unit-switch.json")}
	a := NewSystemdAddons(r, sd)
	j := newFakeJournal(t)
	a.Journal = &journald.Writer{Socket: j.sock}
	if err := sd.writeSwitch(unitSwitch{Masked: []string{"addon-off.service"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id     string
		forced bool
		exit   string
	}{{"off", true, "4"}, {"broken", true, "5"}, {"on", false, "4"}} {
		res, err := a.Uninstall(context.Background(), c.id)
		if res.Forced != c.forced || (err == nil) != c.forced || exists(r, "/usr/local/addons/"+c.id) == c.forced {
			t.Errorf("%s: %+v %v", c.id, res, err)
		}
		got := j.run()
		var lines []string
		for _, e := range got {
			lines = append(lines, e["PRIORITY"]+" "+e["MESSAGE"])
		}
		note := "4 " + c.id + ": the addon was disabled: its rc.d entry and its directory were removed although its uninstall script failed"
		if slices.Contains(lines, note) != c.forced {
			t.Errorf("%s: the journal: %q", c.id, lines)
		}
		last := got[len(got)-1]
		if last["MESSAGE"] != fmt.Sprintf("%s: uninstall finished, exit %s (uninstall script failed)", c.id, c.exit) || last["PRIORITY"] != "3" {
			t.Errorf("%s: the summary: %v", c.id, last)
		}
	}
}
