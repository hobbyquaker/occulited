package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/sysupdate"
)

// occulited task 22: GET /system-update/releases and POST /system-update/download {version} -
// what `occulited update` uses to find a version and stage it, a downgrade among them.
func TestSystemUpdateReleases(t *testing.T) {
	r := fakeRoot(t)
	_ = os.WriteFile(filepath.Join(string(r), "VERSION"), []byte("VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0-dev.42\n"), 0o644)
	body := firmwareZip(t)
	sum := sha256.Sum256(body)
	var feedSrv *httptest.Server
	rel := func(v string) string {
		return fmt.Sprintf(`{"tag_name":"v%s","prerelease":true,"assets":[{"name":"openccu-lite-x86_64-ova-%s.zip","browser_download_url":"%s/f/%s","size":%d},{"name":"openccu-lite-x86_64-ova-%s.zip.sha256","browser_download_url":"%s/s/%s","size":1}]}`, v, v, feedSrv.URL, v, len(body), v, feedSrv.URL, v)
	}
	feedSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		switch {
		case q.URL.Path == "/releases":
			fmt.Fprint(w, "["+rel("1.0.0-dev.42")+","+rel("1.0.0-dev.41")+"]")
		case strings.HasPrefix(q.URL.Path, "/s/"):
			fmt.Fprintf(w, "%s  x\n", hex.EncodeToString(sum[:]))
		default:
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(feedSrv.Close)
	feed := sysupdate.New(r, feedSrv.URL+"/releases", true, nil)
	feed.Channel = sysupdate.ChannelDev // the feed holds dev builds only (task 118)
	api := &SystemAPI{Root: r, Feed: feed, Log: testLog}
	svc := scriptBox{}
	api.Services, api.Addons, api.Manager, api.Nav = svc, svc, svc, svc
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/system-update/releases", "", nil)
	if st != 200 || out["channel"] != "dev" || out["running"] != "1.0.0-dev.42" {
		t.Fatalf("%d %v", st, out)
	}
	rels := out["releases"].([]any)
	if len(rels) != 2 || rels[0].(map[string]any)["direction"] != "same" || rels[1].(map[string]any)["direction"] != "downgrade" {
		t.Fatalf("%v", rels)
	}
	if st, _, _ := do(t, srv, "GET", "/api/system/v1/system-update/releases?channel=nightly", "", nil); st != 400 {
		t.Errorf("unknown channel: %d", st)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/system-update/download", `{"version":"1.0.0-dev.7"}`, nil); st != 404 || out["error"] != "not_found" {
		t.Errorf("unknown version: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/system-update/download", `{"version":"1.0.0-dev.41"}`, nil)
	if st != 200 || out["version"] != "1.0.0-dev.41" || out["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("%d %v", st, out)
	}
	if s := r.StagedSystemUpdate(); s == nil || s.Version != "1.0.0-dev.41" {
		t.Errorf("%+v", s)
	}
	// "latest" is the newest of the default channel; the body-less call keeps the available one
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/system-update/download", `{"version":"latest"}`, nil); st != 200 || out["version"] != "1.0.0-dev.42" {
		t.Errorf("latest: %d %v", st, out)
	}
	// without a feed
	bare := &SystemAPI{Root: r, Log: testLog}
	bare.Services, bare.Addons, bare.Manager, bare.Nav = svc, svc, svc, svc
	m2 := http.NewServeMux()
	bare.Register(m2)
	s2 := httptest.NewServer(m2)
	t.Cleanup(s2.Close)
	if st, _, _ := do(t, s2, "GET", "/api/system/v1/system-update/releases", "", nil); st != 501 {
		t.Errorf("no feed: %d", st)
	}
}
