package system

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"github.com/hobbyquaker/occulited/internal/ownwalk"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// profileBox is a root with the policy directory, rc.d and an addon tree per id; with Priv the
// local operations on it.
func profileBox(t *testing.T) Root {
	t.Helper()
	root := Root(t.TempDir())
	old, oldWalk := Priv, priv.RootBinWalk
	Priv = priv.Local{}
	// RootBin needs root; the tests are not: a clean directory, as the helper would leave it
	priv.RootBinWalk = func(string, string, ownwalk.Options) ownwalk.Result { return ownwalk.Result{} }
	t.Cleanup(func() { Priv, priv.RootBinWalk = old, oldWalk })
	for _, d := range []string{AddonPolicyDir, "/usr/local/etc/config/rc.d"} {
		if err := os.MkdirAll(root.join(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// installProfileAddon is an installed addon: its tree with a bin directory, an executable rc.d
// entry and the stored manifest, with runtime.bin when bin is set.
func installProfileAddon(t *testing.T, root Root, id, bin string) {
	t.Helper()
	if err := os.MkdirAll(root.join("/usr/local/addons/"+id+"/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.join("/usr/local/etc/config/rc.d/"+id), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := map[string]any{"daemon": true}
	if bin != "" {
		rt["bin"] = bin
	}
	m, _ := json.Marshal(map[string]any{"format": 1, "id": id, "name": id, "runtime": rt})
	if err := os.WriteFile(root.join(AddonPolicyDir+"/"+id+AddonManifestSuffix), m, 0o644); err != nil {
		t.Fatal(err)
	}
}

func snippetOf(root Root, id string) (string, bool) {
	b, err := os.ReadFile(root.join(addonunit.ProfileDir + "/" + addonunit.ProfileName(id)))
	return string(b), err == nil
}

func actions(results []ProfileResult) string {
	var parts []string
	for _, r := range results {
		parts = append(parts, r.ID+":"+r.Action)
	}
	return strings.Join(parts, " ")
}

// occulited task 136: the snippet follows the addon through install, disable, enable, the
// Services page's switch, an update that drops the declaration, and the uninstall; an addon
// without the declaration gets none, and what is not a snippet of the system stays.
func TestSyncAddonProfilesLifecycle(t *testing.T) {
	root := profileBox(t)
	off := map[string]bool{}
	sync := func() string { return actions(syncAddonProfiles(root, func(id string) bool { return off[id] })) }

	// nothing installed, no directory: nothing to do, and the directory is not made
	if got := sync(); got != "" {
		t.Errorf("empty: %s", got)
	}
	if _, err := os.Stat(root.join(addonunit.ProfileDir)); !os.IsNotExist(err) {
		t.Error("the profile directory was made for nothing")
	}

	installProfileAddon(t, root, "tools", "bin")
	installProfileAddon(t, root, "plain", "")
	if got := sync(); got != "tools:written" {
		t.Errorf("install: %s", got)
	}
	s, ok := snippetOf(root, "tools")
	if !ok || !strings.Contains(s, `export PATH="${PATH:+${PATH}:}/usr/local/addons/tools/bin"`) {
		t.Fatalf("snippet %q", s)
	}
	if _, ok := snippetOf(root, "plain"); ok {
		t.Error("an addon without runtime.bin has a snippet")
	}
	st, _ := os.Stat(root.join(addonunit.ProfileDir))
	if st.Mode().Perm() != 0o755 {
		t.Errorf("directory mode %v", st.Mode().Perm())
	}
	fi, _ := os.Stat(root.join(addonunit.ProfileDir + "/addon-tools.sh"))
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("snippet mode %v", fi.Mode().Perm())
	}
	if got := sync(); got != "tools:kept" {
		t.Errorf("again: %s", got)
	}

	// Disable on the Addons page: the rc.d entry loses its executable bits
	rcd := root.join("/usr/local/etc/config/rc.d/tools")
	if err := os.Chmod(rcd, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sync(); got != "tools:removed" {
		t.Errorf("disable: %s", got)
	}
	if _, ok := snippetOf(root, "tools"); ok {
		t.Error("the snippet stayed after Disable")
	}
	if err := os.Chmod(rcd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := sync(); got != "tools:written" {
		t.Errorf("enable: %s", got)
	}

	// switched off on the Services page, and on again
	off["tools"] = true
	if got := sync(); got != "tools:removed" {
		t.Errorf("switch off: %s", got)
	}
	off["tools"] = false
	if got := sync(); got != "tools:written" {
		t.Errorf("switch on: %s", got)
	}

	// an update that moves the directory, then one that drops the declaration
	installProfileAddon(t, root, "tools", "bin")
	if err := os.MkdirAll(root.join("/usr/local/addons/tools/sbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	installProfileAddon(t, root, "tools", "sbin")
	if got := sync(); got != "tools:written" {
		t.Errorf("update: %s", got)
	}
	if s, _ := snippetOf(root, "tools"); !strings.Contains(s, "/usr/local/addons/tools/sbin\"\n") {
		t.Errorf("updated snippet %q", s)
	}
	installProfileAddon(t, root, "tools", "")
	if got := sync(); got != "tools:removed" {
		t.Errorf("declaration dropped: %s", got)
	}
	installProfileAddon(t, root, "tools", "bin")
	if got := sync(); got != "tools:written" {
		t.Errorf("declared again: %s", got)
	}

	// the uninstall: the rc.d entry and the policy files go
	if err := os.Remove(rcd); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root.join(AddonPolicyDir + "/tools" + AddonManifestSuffix)); err != nil {
		t.Fatal(err)
	}
	if got := sync(); got != "tools:removed" {
		t.Errorf("uninstall: %s", got)
	}
	if _, ok := snippetOf(root, "tools"); ok {
		t.Error("the snippet stayed after the uninstall")
	}
}

// At start: a snippet of an addon that went while the system did not run, or whose bin left the
// tree, is removed; a file in the directory that is no snippet of the system stays.
func TestSyncAddonProfilesReconciles(t *testing.T) {
	root := profileBox(t)
	dir := root.join(addonunit.ProfileDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"addon-gone.sh":  "export PATH=\"/usr/local/addons/gone/bin${PATH:+:${PATH}}\"\n",
		"local.sh":       "# the administrator's own\n",
		"addon-gone.txt": "x\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	installProfileAddon(t, root, "tools", "bin")
	installProfileAddon(t, root, "escaper", "bin")
	// escaper's bin became a link out of its tree
	if err := os.Remove(root.join("/usr/local/addons/escaper/bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/sbin", root.join("/usr/local/addons/escaper/bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "addon-escaper.sh"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := syncAddonProfiles(root, nil)
	if got := actions(results); got != "escaper:error gone:removed tools:written" {
		t.Errorf("reconcile: %s", got)
	}
	for _, name := range []string{"addon-gone.sh", "addon-escaper.sh"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s stayed", name)
		}
	}
	for _, name := range []string{"local.sh", "addon-gone.txt", "addon-tools.sh"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	out := profileOutput(results)
	for _, want := range []string{"runtime.bin of escaper not put on the PATH", "/usr/local/etc/profile.d/addon-gone.sh removed", "/usr/local/addons/tools/… is root's and at the end of the PATH"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The Services page's switch is read from the stored set: a masked addon unit counts as off.
func TestSyncAddonProfilesSwitch(t *testing.T) {
	root := profileBox(t)
	installProfileAddon(t, root, "tools", "bin")
	s := SystemdServices{Root: root, SwitchFile: filepath.Join(string(root), "unit-switch.json")}
	if got := s.SyncAddonProfiles(); len(got) != 1 || got[0] != "tools written" {
		t.Errorf("on: %v", got)
	}
	if err := s.writeSwitch(unitSwitch{Masked: []string{"addon-tools.service"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.SyncAddonProfiles(); len(got) != 1 || got[0] != "tools removed" {
		t.Errorf("switched off: %v", got)
	}
	a := &SystemdAddons{Systemd: s}
	a.Scripts.Root = root
	if err := s.writeSwitch(unitSwitch{}); err != nil {
		t.Fatal(err)
	}
	if got := a.SyncAddonProfiles(); len(got) != 1 || got[0] != "tools written" {
		t.Errorf("switched on: %v", got)
	}
	if got := a.SyncAddonProfiles(); len(got) != 0 {
		t.Errorf("unchanged: %v", got)
	}
}

// Only the addons just installed or updated adopt their bin's entries of another owner; the sync at
// any other time does not.
func TestSyncAddonProfilesAdoptsOnlyTheInstalled(t *testing.T) {
	root := profileBox(t)
	installProfileAddon(t, root, "tools", "bin")
	installProfileAddon(t, root, "other", "bin")
	adopted := map[string]bool{}
	priv.RootBinWalk = func(home, bin string, opt ownwalk.Options) ownwalk.Result {
		if opt.Adopt {
			adopted[filepath.Base(home)] = true
		}
		return ownwalk.Result{}
	}
	syncAddonProfiles(root, nil, "tools")
	if !adopted["tools"] || adopted["other"] {
		t.Errorf("adopted %v", adopted)
	}
	adopted = map[string]bool{}
	syncAddonProfiles(root, nil)
	if len(adopted) != 0 {
		t.Errorf("a sync without an install adopted %v", adopted)
	}
}
