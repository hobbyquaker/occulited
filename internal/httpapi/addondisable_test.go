package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hobbyquaker/occulited/internal/system"
)

// B-58: disabling an addon on a systemd box stops its unit, reloads the generator (the unit
// disappears) and resets the failed state of the unit whose file is gone - no "not-found failed"
// ghost, no "degraded" system. Enabling reloads and starts as before.
func TestAddonDisableLeavesNoGhost(t *testing.T) {
	r := fakeRoot(t)
	_ = os.Chmod(filepath.Join(string(r), "usr/local/etc/config/rc.d/mosquitto"), 0o755)
	var mu sync.Mutex
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if name == "systemctl" && (args[0] == "show" || args[0] == "list-units") {
			return []byte(""), nil
		}
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	sd := system.SystemdServices{Root: r, Run: run}
	sa := system.NewSystemdAddons(r, sd)
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: sd, Addons: sa, Manager: sa, Nav: scriptBox{system.AddonScripts{Root: r}}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/mosquitto/disable", "", nil)
	if st != 200 || out["enabled"] != false || out["control_error"] != nil {
		t.Fatalf("disable: %d %v", st, out)
	}
	if r.AddonEnabled("mosquitto") {
		t.Fatal("still executable")
	}
	want := "systemctl stop --no-pager -- addon-mosquitto.service\nsystemctl daemon-reload\nsystemctl reset-failed --no-pager -- addon-mosquitto.service"
	mu.Lock()
	got := strings.Join(calls, "\n")
	calls = nil
	mu.Unlock()
	if got != want {
		t.Fatalf("calls:\n%s", got)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/addons/mosquitto/enable", "", nil)
	if st != 200 || out["enabled"] != true || !r.AddonEnabled("mosquitto") {
		t.Fatalf("enable: %d %v", st, out)
	}
	mu.Lock()
	got = strings.Join(calls, "\n")
	mu.Unlock()
	if !strings.HasPrefix(got, "systemctl daemon-reload\n") || !strings.Contains(got, "start") {
		t.Fatalf("enable calls:\n%s", got)
	}
}

// occulited B-132: Enable on the Addons page ends a switch-off from the Services page: the entry goes
// before the reload, and the start is not refused.
func TestAddonEnableEndsTheSwitchOff(t *testing.T) {
	r := fakeRoot(t)
	_ = os.Chmod(filepath.Join(string(r), "usr/local/etc/config/rc.d/mosquitto"), 0o755)
	sw := filepath.Join(t.TempDir(), "unit-switch.json")
	if err := os.WriteFile(sw, []byte(`{"masked":["addon-mosquitto.service"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if name == "systemctl" && (args[0] == "show" || args[0] == "list-units") {
			return []byte(""), nil
		}
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	sd := system.SystemdServices{Root: r, Run: run, SwitchFile: sw}
	sa := system.NewSystemdAddons(r, sd)
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: sd, Addons: sa, Manager: sa, Nav: scriptBox{system.AddonScripts{Root: r}}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/mosquitto/enable", "", nil)
	if st != 200 || out["enabled"] != true || out["control_error"] != nil {
		t.Fatalf("enable: %d %v", st, out)
	}
	if b, _ := os.ReadFile(sw); strings.Contains(string(b), "addon-mosquitto") {
		t.Fatalf("still in the file: %s", b)
	}
	mu.Lock()
	got := strings.Join(calls, "\n")
	mu.Unlock()
	un, re, start := strings.Index(got, "unmask"), strings.Index(got, "daemon-reload"), strings.Index(got, "start")
	if un < 0 || re < un || start < re {
		t.Fatalf("calls:\n%s", got)
	}
}
