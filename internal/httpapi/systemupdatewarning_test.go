package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hobbyquaker/occulited/internal/sysupdate"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// occulited task 123: system-update-available - while the feed's last check offers a release newer
// than the running one in the system's channel; from the remembered answer only, per version
// silenced, gone when installed, when the channel changes, or with the daily check off.
func TestSystemUpdateWarning(t *testing.T) {
	r := fakeRoot(t)
	setVersion := func(v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(string(r), "VERSION"), []byte("VERSION=3.89.11\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE="+v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setVersion("1.0.0")
	var mu sync.Mutex
	stable, requests := "1.0.1", 0
	var feedSrv *httptest.Server
	rel := func(v string, pre bool) string {
		return fmt.Sprintf(`{"tag_name":"v%s","html_url":"https://example.invalid/releases/v%s","prerelease":%t,"assets":[{"name":"openccu-lite-x86_64-ova-%s.zip","browser_download_url":"%s/f/%s","size":1}]}`, v, v, pre, v, feedSrv.URL, v)
	}
	feedSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		fmt.Fprint(w, "["+rel("1.1.0-beta.1", true)+","+rel(stable, false)+","+rel("1.0.0", false)+"]")
	}))
	t.Cleanup(feedSrv.Close)
	feed := sysupdate.New(r, feedSrv.URL+"/releases", true, nil)
	a := &SystemAPI{Root: r, Feed: feed}
	tr := &warnings.Tracker{Sources: []warnings.Source{{IDs: []string{warnings.SystemUpdateID}, Eval: a.systemUpdateWarning}}}
	ctx := context.Background()
	active := func() []warnings.Warning {
		t.Helper()
		var out []warnings.Warning
		for _, w := range tr.List(ctx, "admin", true) {
			if w.Silenced == nil {
				out = append(out, w)
			}
		}
		return out
	}

	// before any check: nothing, and nothing fetched for the evaluation (D-90)
	if ws := active(); len(ws) != 0 {
		t.Fatalf("before a check: %+v", ws)
	}
	check := func() {
		t.Helper()
		if err := feed.Check(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check()
	mu.Lock()
	n := requests
	mu.Unlock()
	ws := active()
	if len(ws) != 1 {
		t.Fatalf("after the check: %+v", ws)
	}
	w := ws[0]
	if w.ID != "system-update-available" || w.Variant != "1.0.1" || w.Severity != "warning" || w.Href != "/system/updates#system-update" ||
		w.Params["version"] != "1.0.1" || w.Params["channel"] != "stable" || w.Params["running"] != "1.0.0" || w.Params["url"] != "https://example.invalid/releases/v1.0.1" {
		t.Fatalf("%+v", w)
	}
	for range 3 {
		active()
	}
	mu.Lock()
	if requests != n {
		t.Errorf("the warning fetched the feed: %d requests, want %d", requests, n)
	}
	mu.Unlock()

	// silenced for 1.0.1; 1.0.2 is a new warning
	if _, err := tr.Silence(ctx, "admin", w.ID, w.Variant, 7); err != nil {
		t.Fatal(err)
	}
	if ws := active(); len(ws) != 0 {
		t.Fatalf("silenced: %+v", ws)
	}
	mu.Lock()
	stable = "1.0.2"
	mu.Unlock()
	check()
	if ws := active(); len(ws) != 1 || ws[0].Variant != "1.0.2" {
		t.Fatalf("1.0.2 after a silence of 1.0.1: %+v", ws)
	}

	// the beta channel: the answer is forgotten until its check, then the beta is offered
	feed.SetChannel(sysupdate.ChannelBeta)
	if ws := active(); len(ws) != 0 {
		t.Fatalf("channel changed, not checked yet: %+v", ws)
	}
	check()
	if ws := active(); len(ws) != 1 || ws[0].Variant != "1.1.0-beta.1" || ws[0].Params["channel"] != "beta" {
		t.Fatalf("beta: %+v", ws)
	}
	feed.SetChannel(sysupdate.ChannelStable)
	check()

	// the daily check switched off: not shown
	feed.SetEnabled(false)
	if ws := active(); len(ws) != 0 {
		t.Fatalf("daily check off: %+v", ws)
	}
	feed.SetEnabled(true)

	// installed: gone; a running version newer than the feed's (a local build): none either
	for _, v := range []string{"1.0.2", "1.0.5"} {
		setVersion(v)
		check()
		if ws := active(); len(ws) != 0 {
			t.Fatalf("running %s: %+v", v, ws)
		}
	}

	// no feed configured
	if ws, ok := (&SystemAPI{Root: r}).systemUpdateWarning(ctx); !ok || len(ws) != 0 {
		t.Fatalf("%v %+v", ok, ws)
	}
}

// Upgrade never names an older release, also where the feed's Newer means "differs" (OpenCCU).
func TestUpgradeNeverADowngrade(t *testing.T) {
	r := fakeRoot(t)
	_ = os.WriteFile(filepath.Join(string(r), "VERSION"), []byte("VERSION=3.89.11\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.3\n"), 0o644)
	feedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v1.0.1","assets":[{"name":"openccu-lite-x86_64-ova-1.0.1.zip","browser_download_url":"http://x/f","size":1}]}]`)
	}))
	t.Cleanup(feedSrv.Close)
	feed := sysupdate.New(r, feedSrv.URL, true, nil)
	if err := feed.Check(context.Background()); err != nil && !strings.Contains(err.Error(), "none") {
		t.Fatal(err)
	}
	if av := feed.Upgrade(); av != nil {
		t.Errorf("a downgrade offered: %+v", av)
	}
}
