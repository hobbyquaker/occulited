package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/sysupdate"
)

// rebootOK is a box whose reboot "starts": the install then keeps the update slot (B-111).
type rebootOK struct{ scriptBox }

func (rebootOK) Reboot(context.Context) error { return nil }

// occulited B-111: one system update at a time. While a download runs, a second download, an
// upload and the install answer 409 busy, GET /system-update names the phase, and DELETE
// /system-update cancels the download; a failed download is remembered as feed.download_error
// until the next one or a discard; the install keeps the slot once the reboot has started.
func TestSystemUpdateBusy(t *testing.T) {
	r := fakeRoot(t)
	_ = os.WriteFile(filepath.Join(string(r), "VERSION"), []byte("VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0-dev.41\n"), 0o644)
	body := firmwareZip(t)
	sum := sha256.Sum256(body)
	// the asset's body is held back until the test lets it go, or the request is cancelled
	var mu sync.Mutex
	hold := make(chan struct{})
	fail := false
	var feedSrv *httptest.Server
	feedSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		switch {
		case q.URL.Path == "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v1.0.0-dev.42","prerelease":true,"assets":[{"name":"openccu-lite-x86_64-ova-1.0.0-dev.42.zip","browser_download_url":"%s/f/42","size":%d},{"name":"openccu-lite-x86_64-ova-1.0.0-dev.42.zip.sha256","browser_download_url":"%s/s/42","size":1}]}]`, feedSrv.URL, len(body), feedSrv.URL)
		case strings.HasPrefix(q.URL.Path, "/s/"):
			fmt.Fprintf(w, "%s  x\n", hex.EncodeToString(sum[:]))
		default:
			mu.Lock()
			f := fail
			mu.Unlock()
			if f {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-hold:
				_, _ = w.Write(body)
			case <-q.Context().Done():
			}
		}
	}))
	t.Cleanup(feedSrv.Close)
	api := &SystemAPI{Root: r, Feed: sysupdate.New(r, feedSrv.URL+"/releases", true, nil), Log: testLog}
	svc := scriptBox{}
	api.Services, api.Addons, api.Manager, api.Nav = svc, svc, svc, svc
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if st, _, _ := do(t, srv, "POST", "/api/system/v1/system-update/check", "", nil); st != 200 {
		t.Fatalf("check: %d", st)
	}
	busyOf := func() map[string]any {
		t.Helper()
		_, out, _ := do(t, srv, "GET", "/api/system/v1/system-update", "", nil)
		b, _ := out["busy"].(map[string]any)
		return b
	}
	waitBusy := func(phase string) {
		t.Helper()
		for i := 0; i < 200; i++ {
			if b := busyOf(); (phase == "" && b == nil) || (b != nil && b["phase"] == phase) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("busy never became %q: %v", phase, busyOf())
	}

	// the download runs; its body is held
	type answer struct {
		st  int
		out map[string]any
	}
	done := make(chan answer, 1)
	go func() {
		st, out, _ := do(t, srv, "POST", "/api/system/v1/system-update/download", "", nil)
		done <- answer{st, out}
	}()
	waitBusy("download")
	if b := busyOf(); b["file"] != "openccu-lite-x86_64-ova-1.0.0-dev.42.zip" {
		t.Errorf("busy file: %v", b)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/system/v1/system-update/download", ""},
		{"POST", "/api/system/v1/system-update/download", `{"version":"1.0.0-dev.42"}`},
		{"POST", "/api/system/v1/system-update/upload?name=x.zip", "zzz"},
		{"POST", "/api/system/v1/system-update/install", "{}"},
	} {
		st, out, _ := do(t, srv, c.method, c.path, c.body, nil)
		d, _ := out["detail"].(map[string]any)
		if st != 409 || out["error"] != "busy" || d["phase"] != "download" {
			t.Errorf("%s %s while downloading: %d %v", c.method, c.path, st, out)
		}
	}
	// DELETE cancels it: the download's own request answers download-cancelled, nothing is staged,
	// the slot is free and no failure is remembered
	st, out, _ := do(t, srv, "DELETE", "/api/system/v1/system-update", "", nil)
	if st != 200 || out["cancelled"] != true {
		t.Fatalf("cancel: %d %v", st, out)
	}
	select {
	case a := <-done:
		if a.st != 409 || a.out["error"] != "download-cancelled" {
			t.Fatalf("cancelled download answered %d %v", a.st, a.out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled download did not answer")
	}
	waitBusy("")
	_, out, _ = do(t, srv, "GET", "/api/system/v1/system-update", "", nil)
	feed := out["feed"].(map[string]any)
	if out["staged"] != nil || feed["downloading"] != nil || feed["download_error"] != nil {
		t.Errorf("after the cancel: %v", out)
	}
	if parts, _ := filepath.Glob(filepath.Join(string(r), "usr/local/tmp", "*.part")); len(parts) != 0 {
		t.Errorf("part file left: %v", parts)
	}

	// a failed download is remembered until a discard
	mu.Lock()
	fail = true
	mu.Unlock()
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/system-update/download", "", nil); st != 502 || out["error"] != "download-failed" {
		t.Fatalf("failed download: %d %v", st, out)
	}
	_, out, _ = do(t, srv, "GET", "/api/system/v1/system-update", "", nil)
	if e := out["feed"].(map[string]any)["download_error"]; e != "download: HTTP 500" {
		t.Errorf("download_error: %v", e)
	}
	if st, _, _ := do(t, srv, "DELETE", "/api/system/v1/system-update", "", nil); st != 200 {
		t.Fatalf("discard: %d", st)
	}
	_, out, _ = do(t, srv, "GET", "/api/system/v1/system-update", "", nil)
	if e := out["feed"].(map[string]any)["download_error"]; e != nil {
		t.Errorf("download_error after the discard: %v", e)
	}

	// let go: a download completes and is staged
	mu.Lock()
	fail = false
	mu.Unlock()
	close(hold)
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/system-update/download", "", nil); st != 200 || out["version"] != "1.0.0-dev.42" {
		t.Fatalf("download after the cancel: %d %v", st, out)
	}
	if busyOf() != nil {
		t.Errorf("slot not free after the download: %v", busyOf())
	}

	// a slow upload holds the slot as "upload"; the download then answers 409 busy
	pr, pw := io.Pipe()
	upDone := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("POST", srv.URL+"/api/system/v1/system-update/upload?name=slow.zip", pr)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			upDone <- -1
			return
		}
		res.Body.Close()
		upDone <- res.StatusCode
	}()
	_, _ = pw.Write([]byte("PK"))
	waitBusy("upload")
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/system-update/download", "", nil); st != 409 || out["detail"].(map[string]any)["phase"] != "upload" {
		t.Errorf("download while uploading: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "DELETE", "/api/system/v1/system-update", "", nil); st != 409 || out["error"] != "busy" {
		t.Errorf("discard while uploading: %d %v", st, out)
	}
	_, _ = pw.Write(body[2:])
	pw.Close()
	if st := <-upDone; st != 200 {
		t.Fatalf("slow upload: %d", st)
	}
	waitBusy("")

	// the install takes the slot and, once the reboot has started, keeps it: everything else is
	// 409 busy from then on - the process is going down
	api.Manager = rebootOK{svc}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/system-update/install", "{}", nil); st != 200 || out["rebooting"] != true {
		t.Fatalf("install: %d %v", st, out)
	}
	if b := busyOf(); b == nil || b["phase"] != "install" || b["file"] != "slow.zip" {
		t.Errorf("busy after the install: %v", b)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/system/v1/system-update/install", "{}"},
		{"POST", "/api/system/v1/system-update/download", ""},
		{"POST", "/api/system/v1/system-update/upload?name=x.zip", "zzz"},
		{"DELETE", "/api/system/v1/system-update", ""},
	} {
		if st, out, _ := do(t, srv, c.method, c.path, c.body, nil); st != 409 || out["error"] != "busy" || out["detail"].(map[string]any)["phase"] != "install" {
			t.Errorf("%s %s while rebooting: %d %v", c.method, c.path, st, out)
		}
	}
}

// the install that could not arm anything, or whose reboot did not start, gives the slot back
func TestSystemUpdateInstallReleasesTheSlot(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(filepath.Join(string(r), "usr/local/tmp"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "VERSION"), []byte("VERSION=3.89.8.20260719\nPRODUCT=ova\nPLATFORM=ova\n"), 0o644)
	api := &SystemAPI{Root: r}
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/system-update/install", "{}", nil); st != 409 {
		t.Fatalf("nothing staged: %d", st)
	}
	if b := api.upd.view(); b != nil {
		t.Errorf("slot kept after nothing-staged: %+v", b)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/system/v1/system-update/upload?name=OpenCCU-3.89.8.20260719-ova.zip", bytes.NewReader(firmwareZip(t)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var up map[string]any
	_ = json.NewDecoder(res.Body).Decode(&up)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("upload: %d %v", res.StatusCode, up)
	}
	// no Manager: armed, no reboot - the slot is free again, a discard un-arms it
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/system-update/install", "{}", nil); st != 200 || out["rebooting"] != false {
		t.Fatalf("install: %d %v", st, out)
	}
	if b := api.upd.view(); b != nil {
		t.Errorf("slot kept without a reboot: %+v", b)
	}
	if st, _, _ := do(t, srv, "DELETE", "/api/system/v1/system-update", "", nil); st != 200 {
		t.Errorf("discard: %d", st)
	}
}
