package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hobbyquaker/occulited/internal/button"
	"github.com/hobbyquaker/occulited/internal/buttonsvc"
	"github.com/hobbyquaker/occulited/internal/system"
)

// openccu-lite task 340: GET /hardware/hdmi says whether the section exists and the setting;
// PUT switches, saves and answers the view; a system without HDMI answers available false and 501.
func TestHardwareHDMI(t *testing.T) {
	root := t.TempDir()
	for _, f := range []struct{ path, body string }{
		{"VERSION", "PRODUCT=rpi4\nPLATFORM=rpi4\n"},
		{"sys/class/drm/card0-HDMI-A-1/status", "connected\n"},
		{"sys/class/drm/card0-HDMI-A-1/enabled", "enabled\n"},
		{"sys/class/drm/card0-HDMI-A-1/dpms", "On\n"},
		{"var/status/startupFinished", ""},
	} {
		p := filepath.Join(root, f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	on := false
	var writes []bool
	h := &system.HDMI{Root: system.Root(root), Get: func() bool { return on }, Save: func(v bool) error { on = v; return nil },
		Set: func(_ context.Context, v bool) error { writes = append(writes, v); return nil }}
	h.ApplyAtBoot(context.Background())
	mux := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t), HDMI: h}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if st, out, _ := do(t, srv, "GET", "/api/system/v1/hardware/hdmi", "", nil); st != 200 || out["available"] != true || out["enabled"] != false {
		t.Fatalf("get: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/hardware/hdmi", `{"enabled":true}`, nil); st != 200 || out["enabled"] != true || !on {
		t.Fatalf("put: %d %v", st, out)
	}
	if len(writes) != 2 || writes[0] || !writes[1] {
		t.Errorf("writes %v, want off (boot) and on", writes)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/hardware/hdmi", `{}`, nil); st != 400 {
		t.Fatalf("empty body: %d", st)
	}

	none := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t)}).Register(none)
	s2 := httptest.NewServer(none)
	t.Cleanup(s2.Close)
	if st, out, _ := do(t, s2, "GET", "/api/system/v1/hardware/hdmi", "", nil); st != 200 || out["available"] != false {
		t.Fatalf("no HDMI: %d %v", st, out)
	}
	if st, _, _ := do(t, s2, "PUT", "/api/system/v1/hardware/hdmi", `{"enabled":true}`, nil); st != 501 {
		t.Fatalf("no HDMI, put: %d", st)
	}
}

type fakeButton struct{ st buttonsvc.State }

func (f fakeButton) State() buttonsvc.State { return f.st }

// openccu-lite task 164: GET /hardware/button answers the state, the settings (defaults when none
// are saved) and the choices; PUT checks and saves them.
func TestHardwareButton(t *testing.T) {
	var saved *button.Settings
	cur := button.Settings{}
	mux := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t), Button: fakeButton{st: buttonsvc.State{Available: true, Pressed: true}},
		ButtonSettings:   func() button.Settings { return cur },
		OnButtonSettings: func(s button.Settings) error { saved, cur = &s, s; return nil }}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	st, out, _ := do(t, srv, "GET", "/api/system/v1/hardware/button", "", nil)
	set, _ := out["settings"].(map[string]any)
	if st != 200 || out["available"] != true || out["pressed"] != true || set["single"] != "mute" || set["long"] != "none" || set["long_s"] != float64(5) {
		t.Fatalf("get: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/hardware/button", `{"single":"locate","double":"mute","long":"recovery","long_s":3,"mute_minutes":30}`, nil); st != 200 || saved == nil || saved.Long != "recovery" || saved.LongS != 3 {
		t.Fatalf("put: %d %v %+v", st, out, saved)
	}
	for _, bad := range []string{`{"single":"recovery"}`, `{"long":"shutdown"}`, `{"long_s":20}`, `{"mute_minutes":1}`} {
		if st, _, _ := do(t, srv, "PUT", "/api/system/v1/hardware/button", bad, nil); st != 422 {
			t.Errorf("%s: %d", bad, st)
		}
	}
	none := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t)}).Register(none)
	s2 := httptest.NewServer(none)
	t.Cleanup(s2.Close)
	if st, out, _ := do(t, s2, "GET", "/api/system/v1/hardware/button", "", nil); st != 200 || out["available"] != false || out["reason"] != "no-module" {
		t.Fatalf("no button: %d %v", st, out)
	}
	if st, _, _ := do(t, s2, "PUT", "/api/system/v1/hardware/button", `{}`, nil); st != 501 {
		t.Fatalf("no button, put: %d", st)
	}
}
