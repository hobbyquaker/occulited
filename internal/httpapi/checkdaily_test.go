package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hobbyquaker/occulited/internal/catalog"
	"github.com/hobbyquaker/occulited/internal/sysupdate"
)

// openccu-lite task 244: *Check daily* beside the release check and the catalogue's check - the
// setting read with the page, switched and persisted through the callbacks.
func TestCheckDailySettings(t *testing.T) {
	r := fakeRoot(t)
	feed := sysupdate.New(r, "http://127.0.0.1:1/latest", true, nil)
	var fed []bool
	var channels []string
	daily := true
	var saved []bool
	api := &SystemAPI{Root: r, Feed: feed, OnSystemUpdateToggle: func(on bool) error { fed = append(fed, on); return nil },
		OnSystemUpdateChannel: func(c string) error { channels = append(channels, c); return nil },
		Catalog:               &fakeCatalog{view: &catalog.View{}}, Log: testLog,
		CatalogDaily: func() bool { return daily }, OnCatalogDaily: func(on bool) error { daily = on; saved = append(saved, on); return nil }}
	svc := scriptBox{}
	api.Services, api.Addons, api.Manager, api.Nav = svc, svc, svc, svc
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if _, out, _ := do(t, srv, "GET", "/api/system/v1/system-update", "", nil); out["feed"].(map[string]any)["enabled"] != true {
		t.Fatalf("feed: %v", out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/system-update/settings", `{"enabled":false}`, nil); st != 200 || out["enabled"] != false {
		t.Fatalf("%d %v", st, out)
	}
	if _, out, _ := do(t, srv, "GET", "/api/system/v1/system-update", "", nil); out["feed"].(map[string]any)["enabled"] != false || len(fed) != 1 || fed[0] {
		t.Fatalf("feed after: %v %v", out, fed)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/system-update/settings", `nope`, nil); st != 400 && st != 422 {
		t.Fatalf("bad body: %d", st)
	}
	// task 118: the channel - stable without one stored, set and persisted alone, the daily switch
	// left as it is; an unknown channel or an empty body is refused
	if _, out, _ := do(t, srv, "GET", "/api/system/v1/system-update", "", nil); out["feed"].(map[string]any)["channel"] != "stable" {
		t.Fatalf("channel: %v", out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/system-update/settings", `{"channel":"beta"}`, nil); st != 200 || out["channel"] != "beta" || out["enabled"] != false {
		t.Fatalf("%d %v", st, out)
	}
	if _, out, _ := do(t, srv, "GET", "/api/system/v1/system-update", "", nil); out["feed"].(map[string]any)["channel"] != "beta" || len(fed) != 1 || len(channels) != 1 || channels[0] != "beta" {
		t.Fatalf("after: %v %v %v", out, fed, channels)
	}
	for _, body := range []string{`{"channel":"nightly"}`, `{"channel":"pre"}`, `{}`} {
		if st, _, _ := do(t, srv, "PUT", "/api/system/v1/system-update/settings", body, nil); st != 400 {
			t.Errorf("%s: %d", body, st)
		}
	}
	if len(channels) != 1 || feed.State().Channel != "beta" {
		t.Errorf("a refused body changed the channel: %v %v", channels, feed.State().Channel)
	}

	if _, out, _ := do(t, srv, "GET", "/api/system/v1/catalog", "", nil); out["daily"] != true {
		t.Fatalf("catalog: %v", out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/catalog/settings", `{"daily":false}`, nil); st != 200 || out["daily"] != false {
		t.Fatalf("%d %v", st, out)
	}
	if _, out, _ := do(t, srv, "GET", "/api/system/v1/catalog", "", nil); out["daily"] != false || len(saved) != 1 {
		t.Fatalf("catalog after: %v %v", out, saved)
	}
	// without the switch wired: 501, and the catalogue says daily
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: r, Catalog: &fakeCatalog{view: &catalog.View{}}, Services: svc, Addons: svc, Manager: svc, Nav: svc, Log: testLog}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	if st, _, _ := do(t, srv2, "PUT", "/api/system/v1/catalog/settings", `{"daily":true}`, nil); st != 501 {
		t.Fatalf("unwired: %d", st)
	}
	if st, _, _ := do(t, srv2, "PUT", "/api/system/v1/system-update/settings", `{"enabled":true}`, nil); st != 501 {
		t.Fatalf("no feed: %d", st)
	}
}
