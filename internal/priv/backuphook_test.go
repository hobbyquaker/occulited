package priv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openccu-lite task 399: the backup hook runs the manifest's program as the addon in a transient
// unit with the addon unit's confinement, measures the staging, and the clean step empties the
// staging without following a link out of the tree.
func TestLocalBackupHook(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	fakeRun := filepath.Join(dir, "systemd-run")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + log + "\nwhile [ \"$1\" != -- ]; do shift; done; shift\nexec \"$@\"\n"
	if err := os.WriteFile(fakeRun, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	addons := filepath.Join(dir, "addons")
	policy := filepath.Join(dir, "policy")
	for _, d := range []string{filepath.Join(addons, "battery", "bin"), filepath.Join(addons, "battery", "var"), policy, filepath.Join(dir, "systemd")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hook := filepath.Join(addons, "battery", "bin", "backup-snapshot")
	// the stand-in runs the hook without the unit's WorkingDirectory, so the hook names its staging
	staging := filepath.Join(addons, "battery", "var", "backup")
	hookScript := "#!/bin/sh\nmkdir -p " + staging + " && printf 'snapshot' > " + staging + "/battery.db && echo written\nexit 3\n"
	if err := os.WriteFile(hook, []byte(hookScript), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"format": 1, "id": "battery", "name": "Battery", "runtime": {"groups": ["usbstorage", "nogroup-x"], "paths": ["/media"]},
	  "backup": {"hook": "bin/backup-snapshot", "staging": "var/backup", "timeout": 30}}`
	if err := os.WriteFile(filepath.Join(policy, "battery"+AddonManifestSuffix), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policy, "battery.json"), []byte(`{"id": "battery", "mode": "confined", "uid": 30007, "runtime": {"groups": ["usbstorage", "nogroup-x"], "paths": ["/media"]}, "data_dirs": ["/usr/local/battery"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := []any{backupSystemdRun, backupSystemdDir, backupAddonsDir, backupPolicyDir, backupHasGroup}
	backupSystemdRun, backupSystemdDir, backupAddonsDir, backupPolicyDir = fakeRun, filepath.Join(dir, "systemd"), addons, policy
	backupHasGroup = func(g string) bool { return g == "usbstorage" || g == "certs" }
	t.Cleanup(func() {
		backupSystemdRun, backupSystemdDir, backupAddonsDir, backupPolicyDir = old[0].(string), old[1].(string), old[2].(string), old[3].(string)
		backupHasGroup = old[4].(func(string) bool)
	})

	var local Local
	ctx := context.Background()
	r, err := local.BackupHook(ctx, "battery", BackupHookSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if r.Step != BackupHookSnapshot || r.Exit != 3 || !strings.Contains(r.Output, "written") || r.Bytes != 8 || r.Files != 1 || r.TimedOut {
		t.Fatalf("snapshot result: %+v", r)
	}
	argv, _ := os.ReadFile(log)
	got := string(argv)
	for _, want := range []string{"--wait\n--pipe\n--collect\n--quiet\n--unit=occulite-backup-hook-battery\n", "RuntimeMaxSec=30\n", "User=addon-battery\n", "Group=addon-battery\n",
		"SupplementaryGroups=usbstorage certs\n", "ProtectSystem=strict\n", "ReadWritePaths=-/usr/local/addons/battery -/usr/local/etc/config/addons/battery -/usr/local/etc/config/rc.d -/run -/var/log -/tmp -/var/tmp -/usr/local/battery -/media\n",
		"WorkingDirectory=" + filepath.Join(addons, "battery") + "\n", "CapabilityBoundingSet=\n", "--\n" + hook + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("argv lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "nogroup-x") {
		t.Errorf("an unknown group rendered:\n%s", got)
	}

	// clean: the staging is emptied, the directory stays
	r, err = local.BackupHook(ctx, "battery", BackupHookClean)
	if err != nil || r.Bytes != 0 || r.Files != 0 {
		t.Fatalf("clean: %+v %v", r, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(addons, "battery", "var", "backup")); len(entries) != 0 {
		t.Fatalf("the staging still holds %d entries", len(entries))
	}
	// clean of a staging that is not there: nothing, no error
	if err := os.RemoveAll(filepath.Join(addons, "battery", "var", "backup")); err != nil {
		t.Fatal(err)
	}
	if r, err := local.BackupHook(ctx, "battery", BackupHookClean); err != nil || r.Files != 0 {
		t.Fatalf("clean without a staging: %+v %v", r, err)
	}
	// a staging that is a link out of the tree is refused, and nothing outside goes
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(addons, "battery", "var", "backup")); err != nil {
		t.Fatal(err)
	}
	if _, err := local.BackupHook(ctx, "battery", BackupHookClean); err == nil || !strings.Contains(err.Error(), "leads out") {
		t.Fatalf("a linked staging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("the link's target was emptied")
	}
	_ = os.Remove(filepath.Join(addons, "battery", "var", "backup"))

	// a hook that is a link out of the tree, or not executable, does not run
	if err := os.Chmod(hook, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := local.BackupHook(ctx, "battery", BackupHookSnapshot); err == nil || !strings.Contains(err.Error(), "not an executable file") {
		t.Fatalf("a hook without the execute bit: %v", err)
	}
	_ = os.Remove(hook)
	if err := os.Symlink(fakeRun, hook); err != nil {
		t.Fatal(err)
	}
	if _, err := local.BackupHook(ctx, "battery", BackupHookSnapshot); err == nil || !strings.Contains(err.Error(), "leads out") {
		t.Fatalf("a linked hook: %v", err)
	}

	// refusals of the shape: the id, the step, an addon without the block
	if _, err := local.BackupHook(ctx, "../x", BackupHookSnapshot); err == nil {
		t.Error("a bad id accepted")
	}
	if _, err := local.BackupHook(ctx, "battery", "run"); err == nil {
		t.Error("a bad step accepted")
	}
	if err := os.WriteFile(filepath.Join(policy, "plain"+AddonManifestSuffix), []byte(`{"format": 1, "id": "plain", "name": "Plain"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := local.BackupHook(ctx, "plain", BackupHookSnapshot); err == nil || !strings.Contains(err.Error(), "no backup block") {
		t.Fatalf("an addon without the block: %v", err)
	}

	// the boundary: an id and a step, nothing else
	s := &Server{Ops: local, Log: func(string, ...any) {}}
	if res := s.backupHook(request{Op: opAddonBackupHook, Name: "battery", Target: BackupHookClean, Path: "/etc"}); res.OK || !strings.Contains(res.Error, "nothing else") {
		t.Errorf("a request with a path: %+v", res)
	}
	if res := s.backupHook(request{Op: opAddonBackupHook, Name: "battery", Target: "run"}); res.OK {
		t.Errorf("a bad step through the server: %+v", res)
	}
	if res := s.backupHook(request{Op: opAddonBackupHook, Name: "battery", Target: BackupHookClean}); !res.OK || !strings.Contains(string(res.Stdout), `"step":"clean"`) {
		t.Errorf("clean through the server: %+v", res)
	}
}
