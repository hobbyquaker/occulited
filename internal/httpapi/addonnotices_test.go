package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hobbyquaker/occulited/internal/warnings"
)

// occulited task 119: an addon's notices through the API - its control token reaches its own
// notices and nothing else, the notices stand in GET /warnings with the system's, and an
// administrator silences them there.
func TestAddonNoticesAPI(t *testing.T) {
	r := fakeRoot(t)
	for _, id := range []string{"battery", "history"} {
		if err := os.MkdirAll(filepath.Join(string(r), "usr/local/etc/config/rc.d"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(string(r), "usr/local/etc/config/rc.d", id), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	notices := &warnings.AddonNotices{File: filepath.Join(t.TempDir(), "addon-notices.json"), Installed: func(id string) bool {
		_, err := os.Lstat(filepath.Join(string(r), "usr/local/etc/config/rc.d", id))
		return err == nil
	}}
	api := &SystemAPI{Root: r, AddonCtl: fakeAddonCtl{"tok-battery": "battery", "tok-history": "history"}, AddonNotices: notices, Log: testLog}
	api.Warnings = &warnings.Tracker{Sources: api.warningSources()}
	svc := scriptBox{}
	api.Services, api.Addons, api.Manager, api.Nav = svc, svc, svc, svc
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	bat := map[string]string{"Authorization": "Bearer tok-battery"}
	hist := map[string]string{"Authorization": "Bearer tok-history"}
	const low = `{"severity":"warning","title":{"de":"Batterie schwach","en":"Battery low"},"text":{"de":"3 Geräte","en":"3 devices"},"link":"/addon-settings/battery#/due"}`

	// the credential: the control token, nothing else
	for _, h := range []map[string]string{nil, {"Authorization": "Bearer nope"}, {"Authorization": "Basic eDp5"}} {
		if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addon-notices/low", low, h); st != 401 {
			t.Errorf("%v: %d", h, st)
		}
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/addon-notices/low", low, bat); st != 201 || out["id"] != "low" || out["severity"] != "warning" {
		t.Fatalf("post: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/addon-notices/low", low, bat); st != 200 || out["posted"] == nil {
		t.Fatalf("replace: %d %v", st, out)
	}
	for body, want := range map[string]int{
		`{"severity":"warning","title":{"en":"x"}}`:                           422,
		`{"severity":"fatal","title":{"de":"x","en":"x"}}`:                    422,
		`{"severity":"info","title":{"de":"x","en":"x"},"link":"https://x/"}`: 422,
		`{"severity":"info","title":{"de":"x","en":"x"},"addon":"history"}`:   400, // no field of its own choosing
		`nope`: 400,
	} {
		if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addon-notices/x", body, bat); st != want {
			t.Errorf("%s: %d, want %d", body, st, want)
		}
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/addon-notices/full", `{"severity":"info","title":{"de":"Speicher voll","en":"Storage full"},"ttl":3600}`, hist); st != 201 || out["expires"] == nil {
		t.Fatalf("history: %d %v", st, out)
	}
	// each addon sees its own
	if st, out, _ := do(t, srv, "GET", "/api/system/v1/addon-notices", "", bat); st != 200 || out["addon"] != "battery" || len(out["notices"].([]any)) != 1 {
		t.Fatalf("list: %d %v", st, out)
	}
	// and clears its own only: history's token does not reach battery's notice
	if st, _, _ := do(t, srv, "DELETE", "/api/system/v1/addon-notices/low", "", hist); st != 404 {
		t.Errorf("another addon's: %d", st)
	}

	// on the Status page, with the system's warnings, silenced by an administrator
	_, out, _ := do(t, srv, "GET", "/api/system/v1/warnings", "", nil)
	var got []map[string]any
	for _, w := range out["warnings"].([]any) {
		if m := w.(map[string]any); m["id"] == warnings.AddonNoticeID {
			got = append(got, m)
		}
	}
	if len(got) != 2 || got[0]["variant"] != "battery/low" || got[0]["href"] != "/addon-settings/battery#/due" || got[1]["variant"] != "history/full" || got[1]["severity"] != "info" {
		t.Fatalf("warnings: %v", got)
	}
	if p := got[0]["params"].(map[string]any); p["addon"] != "battery" || p["title"].(map[string]any)["de"] != "Batterie schwach" || p["text"].(map[string]any)["en"] != "3 devices" {
		t.Errorf("params %v", p)
	}
	if _, err := api.Warnings.Silence(t.Context(), "admin", warnings.AddonNoticeID, "battery/low", 1); err != nil {
		t.Fatalf("silence: %v", err)
	}

	// cleared: one, then all
	if st, _, _ := do(t, srv, "DELETE", "/api/system/v1/addon-notices/low", "", bat); st != 200 {
		t.Errorf("delete: %d", st)
	}
	if st, _, _ := do(t, srv, "DELETE", "/api/system/v1/addon-notices/low", "", bat); st != 404 {
		t.Errorf("delete again: %d", st)
	}
	if st, out, _ := do(t, srv, "DELETE", "/api/system/v1/addon-notices", "", hist); st != 200 || out["cleared"] != float64(1) {
		t.Errorf("clear: %d %v", st, out)
	}
	_, out, _ = do(t, srv, "GET", "/api/system/v1/warnings", "", nil)
	for _, w := range out["warnings"].([]any) {
		if w.(map[string]any)["id"] == warnings.AddonNoticeID {
			t.Errorf("still shown: %v", w)
		}
	}

	// the routes need no session (the token is the credential); without the store: 501
	for _, p := range []string{"/api/system/v1/addon-notices", "/api/system/v1/addon-notices/low"} {
		if !open(p) {
			t.Errorf("%s is not open", p)
		}
	}
	if open("/api/system/v1/addon-noticesx") {
		t.Error("a longer path is open")
	}
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Addons: svc, Manager: svc, Nav: svc, Log: testLog}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	if st, _, _ := do(t, srv2, "GET", "/api/system/v1/addon-notices", "", bat); st != 501 {
		t.Errorf("unwired: %d", st)
	}
}
