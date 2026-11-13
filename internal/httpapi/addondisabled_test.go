package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hobbyquaker/occulited/internal/system"
)

// occulited B-136 (openccu-lite/openccu-lite#15): GET /addons lists a disabled addon with its
// version, `directory` and `uninstall` as its operation; its uninstall answers 200 with `forced`
// when the script failed and the system removed the addon anyway.
func TestDisabledAddonListedAndUninstalled(t *testing.T) {
	r := fakeRoot(t)
	root := string(r)
	for p, c := range map[string]string{
		"usr/local/etc/config/rc.d/hm-watchdog":   "#!/bin/sh\necho cannot\nexit 3\n",
		"usr/local/addons/hm-watchdog/VERSION":    "0.9\n",
		"usr/local/addons/hm-watchdog/bin/daemon": "\x7fELF",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/addons", "", nil)
	if st != 200 {
		t.Fatalf("addons: %d %v", st, out)
	}
	var found map[string]any
	for _, a := range out["addons"].([]any) {
		if m := a.(map[string]any); m["id"] == "hm-watchdog" {
			found = m
		}
	}
	if ops, _ := found["operations"].([]any); found == nil || found["enabled"] != false || found["directory"] != true || found["version"] != "0.9" || len(ops) != 1 || ops[0] != "uninstall" {
		t.Fatalf("the disabled addon: %v", found)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/addons/hm-watchdog/uninstall", "", nil)
	if st != 200 || out["ok"] != true || out["forced"] != true || out["script_error"] == "" || out["output"] != "cannot" {
		t.Fatalf("uninstall: %d %v", st, out)
	}
	for _, p := range []string{"usr/local/etc/config/rc.d/hm-watchdog", "usr/local/addons/hm-watchdog"} {
		if _, err := os.Lstat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", p)
		}
	}
	// an addon that was not disabled answers without the field
	st, out, _ = do(t, srv, "POST", "/api/system/v1/addons/mosquitto/uninstall", "", nil)
	if _, has := out["forced"]; st != 200 || has {
		t.Fatalf("an enabled addon's uninstall: %d %v", st, out)
	}
}
