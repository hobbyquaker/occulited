package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hobbyquaker/occulited/internal/appviews"
	"github.com/hobbyquaker/occulited/internal/system"
)

// runningBox answers the addon units' state as the services listing does.
type runningBox struct {
	scriptBox
	running map[string]bool
}

func (b runningBox) List() ([]system.Service, error) {
	var out []system.Service
	for id, on := range b.running {
		out = append(out, system.Service{ID: "addon-" + id, Kind: "addon", Running: on})
	}
	return out, nil
}

// occulited task 125: an addon's views through the API - its control token reaches its own
// registration alone; the App's list carries the running addons' groups by the addon's name.
func TestAddonViewsAPI(t *testing.T) {
	r := fakeRoot(t)
	rcd := filepath.Join(string(r), "usr/local/etc/config/rc.d")
	if err := os.MkdirAll(rcd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"battery", "history", "zeta"} {
		if err := os.WriteFile(filepath.Join(rcd, id), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	views := &appviews.Store{File: filepath.Join(t.TempDir(), "addon-views.json"), Installed: func(id string) bool {
		_, err := os.Lstat(filepath.Join(rcd, id))
		return err == nil
	}}
	api := &SystemAPI{Root: r, AddonCtl: fakeAddonCtl{"tok-battery": "battery", "tok-history": "history", "tok-zeta": "zeta"}, AddonViews: views, Log: testLog}
	box := runningBox{running: map[string]bool{"history": true, "zeta": true, "battery": false}}
	api.Services, api.Addons, api.Manager, api.Nav = box, box, box, box
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hist := map[string]string{"Authorization": "Bearer tok-history"}
	// the body ccu-addon-history sends
	const two = `{"group":{"de":"History","en":"History"},"views":[{"id":"lab1","title":{"de":"Labor","en":"Lab"},"path":"/addons/history/embed/dashboard/lab1","icon":"chart"},{"id":"energy","title":{"de":"Energie","en":"Energy"},"path":"/addons/history/embed/dashboard/energy","icon":"chart"}]}`

	for _, h := range []map[string]string{nil, {"Authorization": "Bearer nope"}} {
		if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addon-views", two, h); st != 401 {
			t.Errorf("%v: %d", h, st)
		}
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/addon-views", two, hist); st != 200 || out["changed"] != true || len(out["views"].([]any)) != 2 {
		t.Fatalf("put: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/addon-views", two, hist); st != 200 || out["changed"] != false {
		t.Fatalf("the same again: %d %v", st, out)
	}
	for body, want := range map[string]int{
		`{"group":{"de":"x","en":"x"},"views":[{"id":"a","title":{"de":"a","en":"a"},"path":"/addons/hmm/"}]}`: 422, // another addon's pages
		`{"group":{"de":"x","en":"x"},"views":[],"extra":1}`:                                                   400,
		`nope`: 400,
	} {
		if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addon-views", body, hist); st != want {
			t.Errorf("%s: %d, want %d", body, st, want)
		}
	}
	if st, out, _ := do(t, srv, "GET", "/api/system/v1/addon-views", "", hist); st != 200 || out["addon"] != "history" || len(out["views"].([]any)) != 2 {
		t.Fatalf("get: %d %v", st, out)
	}
	// battery's views exist but its unit does not run; zeta's runs
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addon-views", `{"group":{"de":"Batterien","en":"Batteries"},"views":[{"id":"due","title":{"de":"Fällig","en":"Due"},"path":"/addons/battery/due"}]}`, map[string]string{"Authorization": "Bearer tok-battery"}); st != 200 {
		t.Fatalf("battery: %d", st)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addon-views", `{"group":{"de":"Zeta","en":"Zeta"},"views":[{"id":"z","title":{"de":"Z","en":"Z"},"path":"/addons/zeta/z"}]}`, map[string]string{"Authorization": "Bearer tok-zeta"}); st != 200 {
		t.Fatalf("zeta: %d", st)
	}
	st, out, _ := do(t, srv, "GET", "/api/system/v1/app-views", "", nil)
	groups, _ := out["groups"].([]any)
	if st != 200 || len(groups) != 2 {
		t.Fatalf("app views: %d %v", st, out)
	}
	if g := groups[0].(map[string]any); g["addon"] != "history" || len(g["views"].([]any)) != 2 || g["name"].(map[string]any)["de"] != "History" {
		t.Errorf("first group: %v", g)
	}
	if g := groups[1].(map[string]any); g["addon"] != "zeta" {
		t.Errorf("second group: %v", g)
	}
	// DELETE clears history's, and only history's
	if st, out, _ := do(t, srv, "DELETE", "/api/system/v1/addon-views", "", hist); st != 200 || out["changed"] != true {
		t.Fatalf("delete: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "DELETE", "/api/system/v1/addon-views", "", hist); st != 200 || out["changed"] != false {
		t.Fatalf("delete again: %d %v", st, out)
	}
	api.addonsRunning = runningCache{} // the cache's ten seconds
	_, out, _ = do(t, srv, "GET", "/api/system/v1/app-views", "", nil)
	if groups, _ := out["groups"].([]any); len(groups) != 1 || groups[0].(map[string]any)["addon"] != "zeta" {
		t.Fatalf("after the delete: %v", out)
	}
	// without the store: 501 for the addon, an empty list for the App
	bare := &SystemAPI{Root: r, AddonCtl: api.AddonCtl, Log: testLog}
	bare.Services, bare.Addons, bare.Manager, bare.Nav = box, box, box, box
	m2 := http.NewServeMux()
	bare.Register(m2)
	s2 := httptest.NewServer(m2)
	t.Cleanup(s2.Close)
	if st, _, _ := do(t, s2, "PUT", "/api/system/v1/addon-views", two, hist); st != 501 {
		t.Errorf("no store: %d", st)
	}
	if st, out, _ := do(t, s2, "GET", "/api/system/v1/app-views", "", nil); st != 200 || len(out["groups"].([]any)) != 0 {
		t.Errorf("no store, the App: %d %v", st, out)
	}
}
