package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonupdates"
	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/catalog"
	"github.com/hobbyquaker/occulited/internal/manifest"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
)

func fakeRoot(t *testing.T) system.Root {
	t.Helper()
	dir := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(c), 0o755)
	}
	w("VERSION", "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\n")
	w("etc/passwd", "root:x:0:0::/:/bin/sh\n")
	w("etc/group", "root:x:0:\n")
	w("var/hm_mode", "HM_MODE='NORMAL'\nHM_HMRF_DEV='HMIP-RFUSB'\n")
	w("etc/init.d/S61rfd", "#!/bin/sh\necho rfd $1\n")
	w("var/run/rfd.pid", "1\n")
	w("proc/1/status", "Name: init\n")
	w("usr/local/etc/config/rc.d/mosquitto", "#!/bin/sh\ncase $1 in info) echo 'Name: Mosquitto';; *) echo mosquitto $1;; esac\n")
	return system.Root(dir)
}

func systemServer(t *testing.T) *httptest.Server {
	r := fakeRoot(t)
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	_ = os.MkdirAll(filepath.Join(string(r), "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "bin/install_addon"), []byte("#!/bin/sh\n[ -f "+filepath.Join(string(r), "usr/local/tmp/new_addon.tar.gz")+" ] || exit 101\nrm -f "+filepath.Join(string(r), "usr/local/tmp/new_addon.tar.gz")+"\nexit 10\n"), 0o755)
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSystemRoutes(t *testing.T) {
	srv := systemServer(t)
	st, out, _ := do(t, srv, "GET", "/api/system/v1/status", "", nil)
	if st != 200 || out["hm_mode"] != "NORMAL" || out["version"].(map[string]any)["variant"] != "lite" {
		t.Fatalf("status: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/radio", "", nil)
	if st != 200 || len(out["modules"].([]any)) != 1 {
		t.Fatalf("radio: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/services", "", nil)
	if st != 200 || len(out["services"].([]any)) != 2 {
		t.Fatalf("services: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/services/rfd/restart", "", nil)
	if st != 200 || out["output"] != "rfd restart\n" {
		t.Fatalf("control: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/services/rfd/format", "", nil)
	if st != 422 {
		t.Fatalf("bad action: %d %v", st, out)
	}
	// openccu-lite task 243: the units the web interface runs through are not stopped or disabled
	// from it; a restart passes
	for _, c := range []string{"lighttpd/stop", "lighttpd/disable", "occulited/stop", "occulited-helper.service/disable"} {
		if st, out, _ := do(t, srv, "POST", "/api/system/v1/services/"+c, "", nil); st != 409 || out["error"] != "cuts-ui" {
			t.Errorf("%s: %d %v", c, st, out)
		}
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/addons", "", nil)
	if st != 200 || out["addons"].([]any)[0].(map[string]any)["name"] != "Mosquitto" {
		t.Fatalf("addons: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/log?severity=err", "", nil)
	if st != 200 || len(out["lines"].([]any)) != 1 {
		t.Fatalf("log: %d %v", st, out)
	}
}

func TestInstallUninstallRoutes(t *testing.T) {
	srv := systemServer(t)
	// B-15: before the first install the newest job is none - 204, no body; an unknown id stays 404
	if st, _, raw := do(t, srv, "GET", "/api/system/v1/addons/install", "", nil); st != 204 || raw != "" {
		t.Fatalf("no job yet: %d %q", st, raw)
	}
	if st, out, _ := do(t, srv, "GET", "/api/system/v1/addons/install?job=nope", "", nil); st != 404 || out["error"] != "unknown-job" {
		t.Fatalf("an unknown job before any: %d %v", st, out)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "addon.tar.gz")
	_, _ = fw.Write(bytes.Repeat([]byte("x"), 200)) // the fake installer does not unpack it
	_ = mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/system/v1/addons/install", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 202 || out["id"] == "" || out["bytes"] != float64(200) {
		t.Fatalf("install: %d %v", res.StatusCode, out)
	}
	// B-4: the install is a job the client polls
	job := waitInstallJob(t, srv, out["id"].(string))
	if res := job["result"].(map[string]any); job["state"] != "done" || res["exit"] != float64(10) || res["reboot_required"] != true {
		t.Fatalf("install job: %v", job)
	}
	if st, latest, _ := do(t, srv, "GET", "/api/system/v1/addons/install", "", nil); st != 200 || latest["id"] != out["id"] {
		t.Fatalf("the newest job: %d %v", st, latest)
	}
	if st, _, _ := do(t, srv, "GET", "/api/system/v1/addons/install?job=nope", "", nil); st != 404 {
		t.Fatalf("an unknown job: %d", st)
	}
	// ?wait=true answers the finished job
	st, waited, _ := do(t, srv, "POST", "/api/system/v1/addons/install?wait=true", strings.Repeat("y", 100), nil)
	if st != 200 || waited["state"] != "done" || waited["result"].(map[string]any)["exit"] != float64(10) {
		t.Fatalf("install ?wait=true: %d %v", st, waited)
	}
	// an empty archive is refused before any job
	if st, bad, _ := do(t, srv, "POST", "/api/system/v1/addons/install", "tiny", nil); st != 422 || bad["error"] != "install-failed" {
		t.Fatalf("empty: %d %v", st, bad)
	}
	st, out2, _ := do(t, srv, "POST", "/api/system/v1/addons/mosquitto/uninstall", "", nil)
	if st != 200 || out2["ok"] != true || out2["output"] != "mosquitto uninstall" {
		t.Fatalf("uninstall: %d %v", st, out2)
	}
	// B-283: the answer names what the system removed after the script - what was there
	if removed, _ := out2["system_removed"].([]any); len(removed) != 1 || removed[0] != "/usr/local/etc/config/rc.d/mosquitto" {
		t.Fatalf("system_removed: %v", out2["system_removed"])
	}
	st, _, _ = do(t, srv, "POST", "/api/system/v1/addons/mosquitto/uninstall", "", nil)
	if st != 422 {
		t.Fatalf("uninstall twice: %d", st)
	}
	st, out3, _ := do(t, srv, "GET", "/api/system/v1/addons/nope/update", "", nil)
	if st != 404 || out3["error"] != "unknown-addon" {
		t.Fatalf("update unknown: %d %v", st, out3)
	}
}

func TestNavAndNetworkRoutes(t *testing.T) {
	srv := systemServer(t)
	st, out, _ := do(t, srv, "GET", "/api/system/v1/nav", "", nil)
	if st != 200 || len(out["entries"].([]any)) != 0 {
		t.Fatalf("nav (mosquitto has no Config-Url in this fixture): %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/network", "", nil)
	if st != 200 || out["network"].(map[string]any)["mode"] != "dhcp" || out["writable"] != false {
		t.Fatalf("network: %d %v", st, out)
	}
	st, _, _ = do(t, srv, "POST", "/api/system/v1/network", `{"hostname":"x","mode":"dhcp"}`, nil)
	if st != 501 {
		t.Fatalf("network write without a transaction: %d", st)
	}
	// task 157: without the firewall service the rule routes answer 501
	st, out, _ = do(t, srv, "GET", "/api/system/v1/firewall", "", nil)
	if st != 501 {
		t.Fatalf("firewall: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/time", "", nil)
	if st != 200 {
		t.Fatalf("time: %d %v", st, out)
	}
}

func TestNetworkTransactionRoutes(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(filepath.Join(string(r), "etc/config"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "etc/config/netconfig"), []byte("HOSTNAME=openccu\nMODE=DHCP\nCURRENT_IP=192.0.2.119\nCURRENT_NETMASK=255.255.255.0\nCURRENT_GATEWAY=192.0.2.1\nCURRENT_NAMESERVER1=192.0.2.1\nIP=0.0.0.0\nNETMASK=0.0.0.0\nGATEWAY=0.0.0.0\n"), 0o644)
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		return nil, nil
	}
	tx := &system.NetTx{Root: r, Applier: system.NetApplier{Root: r, Iface: "eth0", Run: run}, Window: time.Minute}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, NetTx: tx, Run: run}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "POST", "/api/system/v1/network", `{"hostname":"bad name","mode":"dhcp"}`, nil)
	if st != 400 {
		t.Fatalf("invalid: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/network", `{"hostname":"openccu","mode":"static","address":"192.0.2.200","netmask":"255.255.255.0","gateway":"192.0.2.1","dns":["192.0.2.1"]}`, nil)
	if st != 200 || out["applied"] != false {
		t.Fatalf("begin: %d %v", st, out)
	}
	token := out["pending"].(map[string]any)["token"].(string)
	st, out, _ = do(t, srv, "GET", "/api/system/v1/network", "", nil)
	if st != 200 || out["pending"] == nil || out["writable"] != true {
		t.Fatalf("pending missing: %d %v", st, out)
	}
	st, _, _ = do(t, srv, "POST", "/api/system/v1/network", `{"hostname":"openccu","mode":"dhcp"}`, nil)
	if st != 409 {
		t.Fatalf("second begin: %d", st)
	}
	st, _, _ = do(t, srv, "POST", "/api/system/v1/network/confirm", `{"token":"nope"}`, nil)
	if st != 404 {
		t.Fatalf("bad token: %d", st)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/network/confirm", `{"token":"`+token+`"}`, nil)
	if st != 200 || out["confirmed"] != true {
		t.Fatalf("confirm: %d %v", st, out)
	}
	if b, _ := os.ReadFile(filepath.Join(string(r), "etc/config/netconfig")); !strings.Contains(string(b), "MODE=MANUAL\n") {
		t.Fatalf("not persisted: %s", b)
	}
	if len(calls) == 0 {
		t.Fatal("nothing ran")
	}

	// time writes through the same dry runner
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/time", `{"ntp_servers":["ptbtime1.ptb.de"]}`, nil)
	if st != 200 || out["ntp_servers"].([]any)[0] != "ptbtime1.ptb.de" {
		t.Fatalf("time put: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/time", `{"zone":"Mars/Olympus"}`, nil)
	if st != 400 {
		t.Fatalf("bad zone: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/leds", "", nil)
	if st != 200 || out["disabled"] != false {
		t.Fatalf("leds: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/leds", `{"disabled":true}`, nil)
	if st != 200 || out["disabled"] != true {
		t.Fatalf("leds put: %d %v", st, out)
	}
}

func TestLANGatewayRoutes(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(filepath.Join(string(r), "etc/config"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "etc/config/rfd.conf"), []byte("Listen Port = 32001\n\n[Interface 0]\nType = CCU2\nComPortFile = /dev/mmd_bidcos\n\n"), 0o644)
	svc := &fakeServices{}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "PUT", "/api/system/v1/radio/lan-gateways", `{"class":"rf","gateways":[{"type":"HMLGW2","name":"Garage","serial":"neq1234567","key":"secret1","ip":"lgw.lan"}],"restart":true}`, nil)
	if st != 200 || out["restarted"] != true || out["service"] != "rfd" {
		t.Fatalf("put: %d %v", st, out)
	}
	gws := out["gateways"].([]any)
	g := gws[0].(map[string]any)
	if len(gws) != 1 || g["serial"] != "NEQ1234567" || g["address"] != "lgw.lan" || g["has_key"] != true || g["raw"] != nil {
		t.Fatalf("%v", gws)
	}
	if len(svc.calls) != 1 || svc.calls[0] != "rfd restart" {
		t.Errorf("restart calls: %v", svc.calls)
	}
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/radio/lan-gateways", `{"class":"rf","gateways":[{"type":"Foo","serial":"X"}]}`, nil)
	if st != 422 {
		t.Fatalf("invalid type: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/radio", "", nil)
	if st != 200 || len(out["lan_gateways"].([]any)) != 1 || len(out["wired_gateways"].([]any)) != 0 {
		t.Fatalf("radio: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/radio/lan-gateways/key", `{"class":"rf","serial":"NEQ1234567","key":"newkey","current_key":"secret1"}`, nil)
	if st != 200 || out["queued"] != true {
		t.Fatalf("key: %d %v", st, out)
	}
	if b, _ := os.ReadFile(filepath.Join(string(r), "etc/config/NEQ1234567.keychange")); !strings.Contains(string(b), "KEY=newkey\n") {
		t.Errorf("keychange: %q", b)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/radio/lan-gateways", "", nil)
	if st != 200 || len(out["pending_key_changes"].([]any)) != 1 {
		t.Fatalf("pending: %d %v", st, out)
	}
	// no crypttool in the fake root: a clear error, not a crash
	st, out, _ = do(t, srv, "POST", "/api/system/v1/radio/security-key", `{"key":"abc"}`, nil)
	if st != 422 {
		t.Fatalf("short key: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/radio/security-key", `{"key":"abcde"}`, nil)
	if st != 422 || !strings.Contains(out["message"].(string), "crypttool") {
		t.Fatalf("no crypttool: %d %v", st, out)
	}
}

// fakeServices records control calls.
type fakeServices struct{ calls []string }

func (f *fakeServices) List() ([]system.Service, error) { return nil, nil }
func (f *fakeServices) Control(_ context.Context, id, action string) (string, error) {
	f.calls = append(f.calls, id+" "+action)
	return "ok", nil
}

func TestSystemUpdateRoutes(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(filepath.Join(string(r), "usr/local/tmp"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "VERSION"), []byte("VERSION=3.89.8.20260719\nPRODUCT=ova\nPLATFORM=ova\n"), 0o644)
	mux := http.NewServeMux()
	(&SystemAPI{Root: r}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/system-update", "", nil)
	if st != 200 || out["staged"] != nil {
		t.Fatalf("empty: %d %v", st, out)
	}
	// a zip with the EULAs and an image, as multipart
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for _, n := range []string{"OpenCCU-3.89.8.20260719-ova.img", "EULA.en", "EULA.de"} {
		f, _ := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Store})
		_, _ = f.Write(bytes.Repeat([]byte("z"), 300))
	}
	_ = zw.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "OpenCCU-3.89.8.20260719-ova.zip")
	_, _ = fw.Write(zb.Bytes())
	_ = mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/system/v1/system-update/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var up map[string]any
	_ = json.NewDecoder(res.Body).Decode(&up)
	res.Body.Close()
	if res.StatusCode != 200 || up["kind"] != "zip" || up["board"] != "ova" || up["warning"] != nil {
		t.Fatalf("upload: %d %v", res.StatusCode, up)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/system-update/install", "{}", nil)
	if st != 200 || out["armed"] != true || out["rebooting"] != false {
		t.Fatalf("install: %d %v", st, out)
	}
	if _, err := os.Stat(filepath.Join(string(r), "usr/local/.recoveryMode")); err != nil {
		t.Error("marker missing")
	}
	st, out, _ = do(t, srv, "DELETE", "/api/system/v1/system-update", "", nil)
	if st != 200 || out["staged"] != nil {
		t.Fatalf("discard: %d %v", st, out)
	}
	if _, err := os.Stat(filepath.Join(string(r), "usr/local/.recoveryMode")); !os.IsNotExist(err) {
		t.Error("marker kept")
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/system-update/install", "{}", nil)
	if st != 409 {
		t.Fatalf("install without file: %d %v", st, out)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/api/system/v1/system-update/upload?name=x.bin", bytes.NewReader(bytes.Repeat([]byte("no"), 600)))
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 422 {
		t.Errorf("garbage accepted: %d", res.StatusCode)
	}
}

// fakeCatalog is a catalogue whose view is already known, the state the box is in after the
// user's check. Its installs hold the one slot as the real one does, until release is closed
// (nil: they end at once).
type fakeCatalog struct {
	view      *catalog.View
	images    map[string][]byte // "<id>/<kind>" → the image (openccu-lite task 100)
	refreshes int
	release   chan struct{}

	mu      sync.Mutex
	running *catalog.Progress
	started []string
}

func (f *fakeCatalog) Fetch(context.Context, bool) (*catalog.View, error) { return f.view, nil }
func (f *fakeCatalog) Refresh(context.Context) error                      { f.refreshes++; return nil }
func (f *fakeCatalog) Start(_ context.Context, id string) (<-chan error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running != nil && f.running.Finished == nil {
		return nil, catalog.ErrInstallRunning
	}
	p := &catalog.Progress{AddonID: id, Phase: "resolving"}
	f.running, f.started = p, append(f.started, id)
	done := make(chan error, 1)
	go func() {
		if f.release != nil {
			<-f.release
		}
		f.mu.Lock()
		now := time.Now()
		p.Phase, p.Finished = "done", &now
		f.mu.Unlock()
		done <- nil
	}()
	return done, nil
}
func (f *fakeCatalog) Image(id, kind string) ([]byte, error) {
	if f.images != nil {
		if b, ok := f.images[id+"/"+kind]; ok {
			return b, nil
		}
	}
	return nil, fs.ErrNotExist
}
func (f *fakeCatalog) Progress() *catalog.Progress {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running == nil {
		return nil
	}
	p := *f.running
	return &p
}
func (f *fakeCatalog) Started() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...)
}

// item is a catalogue item whose manifest names id.
func item(id string, latest *catalog.Latest) catalog.Item {
	return catalog.Item{Git: "https://github.com/o/" + id, Manifest: &manifest.Manifest{Format: 1, ID: id, Name: manifest.Text{"en": id}}, Latest: latest}
}

// updatesBox is the addon list and the addons' own update checks as the check service sees them.
type updatesBox struct {
	mu        sync.Mutex
	addons    []system.Addon
	available map[string]bool
	checks    int
}

func (b *updatesBox) ListAddons(context.Context) ([]system.Addon, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]system.Addon(nil), b.addons...), nil
}

func (b *updatesBox) CheckUpdate(_ context.Context, a system.Addon, _ string) system.UpdateInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checks++
	return system.UpdateInfo{Installed: a.Version, UpdateAvailable: b.available[a.ID]}
}

func (b *updatesBox) set(id string, available bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.available[id] = available
}

func (b *updatesBox) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checks
}

// waitInstallJob polls an upload install's job until it is no longer running.
func waitInstallJob(t *testing.T, srv *httptest.Server, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, job, raw := do(t, srv, "GET", "/api/system/v1/addons/install?job="+id, "", nil)
		if st != 200 {
			t.Fatalf("job %s: %d %s", id, st, raw)
		}
		if job["state"] != "running" {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still running", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// blockingManager installs when told to, and records whether the install's context was ended.
type blockingManager struct {
	updatesManager
	release chan struct{}
	got     chan string // the archive's content and the context's error, when the install ends
}

func (m blockingManager) Install(ctx context.Context, r io.Reader) (*system.InstallResult, error) {
	b, _ := io.ReadAll(r)
	<-m.release
	time.Sleep(20 * time.Millisecond) // time for a cancellation to arrive
	m.got <- fmt.Sprintf("%s|%v|%T", b, ctx.Err(), r)
	return &system.InstallResult{Exit: 0, Meaning: "installed"}, nil
}

// B-25: three catalogue installs started in the same moment answered 202 each, and only the
// first ran - the others were refused inside the detached run, silently. Now the slot is taken
// before the answer: one 202, the rest 409 install-running, an upload beside it 409 too, and the
// next start after the first has ended is accepted again.
func TestCatalogInstallOneAtATime(t *testing.T) {
	r := fakeRoot(t)
	m := blockingManager{release: make(chan struct{}), got: make(chan string, 1)}
	cat := &fakeCatalog{view: &catalog.View{}, release: make(chan struct{})}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: scriptBox{system.AddonScripts{Root: r}}, Log: testLog, Addons: scriptBox{system.AddonScripts{Root: r}}, Manager: m, Catalog: cat}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ids := []string{"hmm", "mosquitto", "redmatic"}
	codes := make(chan int, len(ids))
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, _, _ := do(t, srv, "POST", "/api/system/v1/catalog/"+id+"/install", "", nil)
			codes <- st
		}()
	}
	wg.Wait()
	close(codes)
	count := map[int]int{}
	for c := range codes {
		count[c]++
	}
	if count[202] != 1 || count[409] != 2 {
		t.Fatalf("three starts at once: %v, want one 202 and two 409", count)
	}
	if got := cat.Started(); len(got) != 1 {
		t.Fatalf("installs started: %v", got)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/catalog/hmm/install", "", nil); st != 409 || out["error"] != "install-running" {
		t.Errorf("a start while one runs: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/install", strings.Repeat("a", 128), nil); st != 409 || out["error"] != "install-running" {
		t.Errorf("an upload while a catalogue install runs: %d %v", st, out)
	}
	close(cat.release)
	for deadline := time.Now().Add(5 * time.Second); ; {
		if p := cat.Progress(); p != nil && p.Finished != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first install did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/catalog/redmatic/install", "", nil); st != 202 {
		t.Errorf("the next start after the first ended: %d", st)
	}
	if got := cat.Started(); len(got) != 2 {
		t.Errorf("installs started: %v", got)
	}
}

// slotManager is blockingManager with an uninstall that waits to be released (B-113).
type slotManager struct {
	blockingManager
	uninstallRelease chan struct{}
}

func (m slotManager) Uninstall(context.Context, string) (system.UninstallResult, error) {
	<-m.uninstallRelease
	return system.UninstallResult{Output: "removed"}, nil
}

// B-113: GET /catalog/progress says in `busy` what the one install slot holds - a catalogue run,
// an upload's job or an uninstall - so the Addons page shows one state in every tab; and the
// uninstall is part of the slot: refused while an install runs, the installs refused while it runs.
func TestCatalogProgressBusy(t *testing.T) {
	r := fakeRoot(t)
	m := slotManager{blockingManager: blockingManager{release: make(chan struct{}), got: make(chan string, 1)}, uninstallRelease: make(chan struct{})}
	cat := &fakeCatalog{view: &catalog.View{}, release: make(chan struct{})}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: scriptBox{system.AddonScripts{Root: r}}, Log: testLog, Addons: scriptBox{system.AddonScripts{Root: r}}, Manager: m, Catalog: cat}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	busy := func() map[string]any {
		st, out, raw := do(t, srv, "GET", "/api/system/v1/catalog/progress", "", nil)
		if st != 200 {
			t.Fatalf("progress: %d %s", st, raw)
		}
		b, _ := out["busy"].(map[string]any)
		return b
	}
	waitBusy := func(kind string) map[string]any {
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			b := busy()
			if (kind == "" && b == nil) || (kind != "" && b != nil && b["kind"] == kind) {
				return b
			}
			if time.Now().After(deadline) {
				t.Fatalf("busy did not become %q: %v", kind, b)
			}
		}
	}
	if b := busy(); b != nil {
		t.Fatalf("busy while nothing runs: %v", b)
	}

	// an upload's job holds the slot: busy names it, and the uninstall is refused
	archive := strings.Repeat("a", 128)
	st, job, raw := do(t, srv, "POST", "/api/system/v1/addons/install", archive, nil)
	if st != 202 {
		t.Fatalf("upload: %d %s", st, raw)
	}
	if b := busy(); b == nil || b["kind"] != "upload" || b["job"] != job["id"] || b["bytes"] != float64(128) {
		t.Fatalf("busy during the upload's job: %v (job %v)", b, job["id"])
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/mosquitto/uninstall", "", nil); st != 409 || out["error"] != "install-running" || out["message"] != errInstallRunning.Error() {
		t.Errorf("an uninstall during an upload's install: %d %v", st, out)
	}
	close(m.release)
	<-m.got
	waitInstallJob(t, srv, job["id"].(string))
	waitBusy("")

	// a catalogue run holds it
	if st, _, raw := do(t, srv, "POST", "/api/system/v1/catalog/hmm/install", "", nil); st != 202 {
		t.Fatalf("catalogue install: %d %s", st, raw)
	}
	if b := busy(); b == nil || b["kind"] != "catalog" || b["addon_id"] != "hmm" {
		t.Fatalf("busy during a catalogue run: %v", b)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/mosquitto/uninstall", "", nil); st != 409 || out["error"] != "install-running" {
		t.Errorf("an uninstall during a catalogue run: %d %v", st, out)
	}
	close(cat.release)
	waitBusy("")

	// an uninstall holds it: busy names the addon, and both installs are refused with the reason
	type answer struct {
		st  int
		out map[string]any
	}
	done := make(chan answer, 1)
	go func() {
		st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/mosquitto/uninstall", "", nil)
		done <- answer{st, out}
	}()
	if b := waitBusy("uninstall"); b["addon_id"] != "mosquitto" {
		t.Fatalf("busy during the uninstall: %v", b)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/install", archive, nil); st != 409 || out["error"] != "install-running" || out["message"] != errUninstallRunning.Error() {
		t.Errorf("an upload during an uninstall: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/catalog/hmm/install", "", nil); st != 409 || out["error"] != "install-running" || out["message"] != errUninstallRunning.Error() {
		t.Errorf("a catalogue install during an uninstall: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/hmm/uninstall", "", nil); st != 409 || out["error"] != "install-running" {
		t.Errorf("a second uninstall: %d %v", st, out)
	}
	if got := cat.Started(); len(got) != 1 {
		t.Errorf("catalogue installs started: %v", got)
	}
	close(m.uninstallRelease)
	if a := <-done; a.st != 200 || a.out["output"] != "removed" {
		t.Fatalf("the uninstall: %d %v", a.st, a.out)
	}
	waitBusy("")
	// the slot is free again
	if st, _, raw := do(t, srv, "POST", "/api/system/v1/catalog/hmm/install", "", nil); st != 202 {
		t.Errorf("a catalogue install after the uninstall: %d %s", st, raw)
	}
}

// B-4: lighttpd's graceful reload during an install ends the proxied request; the install -
// its tail, the unit generation and the start - must not end with it. The request only stores
// the archive; the job runs on a context of its own and the client polls it. A second install
// while one runs is refused, an upload and a catalogue install alike.
func TestInstallJobOutlivesItsRequest(t *testing.T) {
	r := fakeRoot(t)
	m := blockingManager{release: make(chan struct{}), got: make(chan string, 1)}
	cat := &fakeCatalog{view: &catalog.View{}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: scriptBox{system.AddonScripts{Root: r}}, Log: testLog, Addons: scriptBox{system.AddonScripts{Root: r}}, Manager: m, Catalog: cat}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	archive := strings.Repeat("a", 128)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/system/v1/addons/install?wait=true", strings.NewReader(archive))
	errc := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
		}
		errc <- err
	}()
	// the job is there while the request waits
	var id string
	for deadline := time.Now().Add(5 * time.Second); id == ""; {
		if st, job, _ := do(t, srv, "GET", "/api/system/v1/addons/install", "", nil); st == 200 && job["state"] == "running" {
			id = job["id"].(string)
		} else if time.Now().After(deadline) {
			t.Fatal("no running job")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/addons/install", archive, nil); st != 409 || out["error"] != "install-running" {
		t.Fatalf("a second upload: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/catalog/mosquitto/install", "", nil); st != 409 || out["error"] != "install-running" {
		t.Fatalf("a catalogue install beside it: %d %v", st, out)
	}
	cancel() // the reload cuts the request
	if err := <-errc; err == nil {
		t.Fatal("the request was not cut")
	}
	close(m.release)
	if got := <-m.got; got != archive+"|<nil>|*system.StagedArchive" {
		t.Fatalf("the install saw %q", got)
	}
	job := waitInstallJob(t, srv, id)
	if job["state"] != "done" || job["result"].(map[string]any)["meaning"] != "installed" {
		t.Fatalf("job: %v", job)
	}
	// the staged upload is gone
	left, _ := filepath.Glob(filepath.Join(string(r), system.StagingDir, "upload-*"))
	if len(left) != 0 {
		t.Fatalf("staged upload left behind: %v", left)
	}
}

// updatesManager installs and uninstalls without touching anything.
type updatesManager struct{ box *updatesBox }

func (m updatesManager) Install(context.Context, io.Reader) (*system.InstallResult, error) {
	return &system.InstallResult{Exit: 0, Meaning: "installed"}, nil
}
func (m updatesManager) Uninstall(context.Context, string) (system.UninstallResult, error) {
	return system.UninstallResult{Output: "removed", SystemRemoved: []string{"/usr/local/etc/config/rc.d/mosquitto", "/usr/local/etc/config/addon-policy/mosquitto.json"}}, nil
}
func (m updatesManager) CheckUpdate(ctx context.Context, a system.Addon, base string) system.UpdateInfo {
	return m.box.CheckUpdate(ctx, a, base)
}
func (m updatesManager) Reboot(context.Context) error { return nil }

// B-82: the Status page counted the updates of the daily check that ran before they were
// installed. An install from the catalogue asks that addon's check again, an uninstall drops its
// result, and an uploaded archive - which names no addon - runs the whole check again.
func TestInstallsMoveTheAddonUpdateCheck(t *testing.T) {
	resultOf := func(s *addonupdates.Service, id string) (found, available bool) {
		for _, r := range s.State().Results {
			if r.ID == id {
				return true, r.Info.UpdateAvailable
			}
		}
		return false, false
	}
	tests := []struct {
		name   string
		path   string
		change func(*updatesBox) // what the install or the uninstall did to the box
		done   func(s *addonupdates.Service, b *updatesBox, checksBefore int) bool
	}{
		{
			name:   "an install from the catalogue asks that addon's check again",
			path:   "/api/system/v1/catalog/redmatic/install",
			change: func(b *updatesBox) { b.set("redmatic", false) },
			done: func(s *addonupdates.Service, _ *updatesBox, _ int) bool {
				found, available := resultOf(s, "redmatic")
				return found && !available && s.State().Available == 1
			},
		},
		{
			name:   "an uninstall drops the addon's result",
			path:   "/api/system/v1/addons/redmatic/uninstall",
			change: func(*updatesBox) {},
			done: func(s *addonupdates.Service, _ *updatesBox, _ int) bool {
				found, _ := resultOf(s, "redmatic")
				return !found && s.State().Available == 1
			},
		},
		{
			name:   "an uploaded archive runs the whole check again",
			path:   "/api/system/v1/addons/install",
			change: func(b *updatesBox) { b.set("redmatic", false); b.set("hm2mqtt", false) },
			done: func(s *addonupdates.Service, b *updatesBox, before int) bool {
				return b.count() >= before+2 && s.State().Available == 0
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fakeRoot(t)
			box := &updatesBox{
				addons:    []system.Addon{{ID: "hm2mqtt", Version: "3.6.0", Update: "/addons/hm2mqtt/update.cgi"}, {ID: "redmatic", Version: "9.4.0", Update: "/addons/redmatic/update.cgi"}},
				available: map[string]bool{"hm2mqtt": true, "redmatic": true},
			}
			upd := &addonupdates.Service{Lister: box, Checker: box}
			upd.Check(context.Background())
			if st := upd.State(); st.Available != 2 {
				t.Fatalf("before: %+v", st)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go upd.Run(ctx) // the loop serves the trigger of an uploaded archive
			svc := scriptBox{system.AddonScripts{Root: r}}
			mux := http.NewServeMux()
			(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: updatesManager{box}, Nav: svc, Catalog: &fakeCatalog{view: &catalog.View{}}, Updates: upd}).Register(mux)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			before := box.count()
			tt.change(box)
			// an uploaded archive is stored before its job runs (B-4), and an empty one is refused
			if st, _, raw := do(t, srv, "POST", tt.path, strings.Repeat("x", 100), nil); st >= 300 {
				t.Fatalf("POST %s: %d %s", tt.path, st, raw)
			}
			deadline := time.Now().Add(5 * time.Second)
			for !tt.done(upd, box, before) {
				if time.Now().After(deadline) {
					t.Fatalf("the check did not follow: %+v", upd.State())
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestCatalogLatestAndUpdateAvailable(t *testing.T) {
	r := fakeRoot(t)
	// the installed version comes from the rc.d info output
	_ = os.WriteFile(filepath.Join(string(r), "usr/local/etc/config/rc.d/mosquitto"),
		[]byte("#!/bin/sh\ncase $1 in info) echo 'Name: Mosquitto'; echo 'Version: 2.1.0';; esac\n"), 0o755)
	svc := scriptBox{system.AddonScripts{Root: r}}
	cat := &fakeCatalog{view: &catalog.View{Format: 1, Addons: []catalog.Item{
		item("mosquitto", &catalog.Latest{Version: "2.1.2", Asset: "mosquitto-x86_64-2.1.2.tar.gz"}),
		item("ccu-jack", &catalog.Latest{Version: "2.13.0", Asset: "ccu-jack-2.13.0.tar.gz"}),
		item("hm2mqtt", nil), // not resolved yet: no latest, no claim
		{Git: "https://github.com/o/unfetched", Untested: true}, // no check yet: the entry alone
	}}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc, Catalog: cat}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	st, out, raw := do(t, srv, "GET", "/api/system/v1/catalog", "", nil)
	if st != 200 {
		t.Fatalf("catalog: %d %s", st, raw)
	}
	if out["installed"].(map[string]any)["mosquitto"] != "2.1.0" {
		t.Fatalf("installed: %v", out["installed"])
	}
	addons := out["catalog"].(map[string]any)["addons"].([]any)
	got := map[string]map[string]any{}
	unfetched, untested := 0, false
	for _, a := range addons {
		e := a.(map[string]any)
		id, ok := e["id"].(string)
		if !ok {
			untested = e["untested"] == true
			unfetched++
			continue
		}
		got[id] = e
	}
	if unfetched != 1 || len(got) != 3 || !untested {
		t.Fatalf("entries: %d unfetched, %v", unfetched, got)
	}
	if _, ok := got["mosquitto"]["untested"]; ok || got["mosquitto"]["verified"] != nil || got["mosquitto"]["name"].(map[string]any)["en"] != "mosquitto" {
		t.Fatalf("the manifest's fields are flattened into the item: %v", got["mosquitto"])
	}
	if l, ok := got["mosquitto"]["latest"].(map[string]any); !ok || l["version"] != "2.1.2" || l["asset"] != "mosquitto-x86_64-2.1.2.tar.gz" {
		t.Fatalf("latest: %v", got["mosquitto"]["latest"])
	}
	if got["mosquitto"]["update_available"] != true {
		t.Fatalf("installed 2.1.0 against 2.1.2 must offer an update: %v", got["mosquitto"])
	}
	if _, ok := got["ccu-jack"]["update_available"]; ok {
		t.Fatalf("not installed: no update flag: %v", got["ccu-jack"])
	}
	if _, ok := got["hm2mqtt"]["latest"]; ok {
		t.Fatalf("unresolved entry must carry no latest: %v", got["hm2mqtt"])
	}
	if cat.refreshes != 0 {
		t.Fatalf("a plain page load must not fetch anything: %d", cat.refreshes)
	}
	if st, _, raw := do(t, srv, "GET", "/api/system/v1/catalog?refresh=1", "", nil); st != 200 || cat.refreshes != 1 {
		t.Fatalf("explicit refresh: %d refreshes=%d %s", st, cat.refreshes, raw)
	}
	if st, _, raw := do(t, srv, "POST", "/api/system/v1/catalog/refresh", "", nil); st != 200 || cat.refreshes != 2 {
		t.Fatalf("POST refresh: %d refreshes=%d %s", st, cat.refreshes, raw)
	}
}

// D-36: confined is the default and root is the opt-out, so the API does not hand out root to a
// caller who left a field out - "unsafe": true is the deliberate act, and the UI's button says so.
func TestAddonPolicyUnsafeOptOut(t *testing.T) {
	r := fakeRoot(t)
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte("ok"), nil
	}
	useOwnNoted(t, func(line string) { calls = append(calls, line) })
	sa := system.NewSystemdAddons(r, system.SystemdServices{Root: r, Run: run})
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: scriptBox{system.AddonScripts{Root: r}}, Addons: sa, Manager: sa, Nav: scriptBox{system.AddonScripts{Root: r}}, Timers: nil}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// confine it: no acknowledgement needed, that is the default direction
	st, out, _ := do(t, srv, "PUT", "/api/system/v1/addons/mosquitto/policy", `{"mode":"confined","restart":false}`, nil)
	if st != 200 || out["policy"].(map[string]any)["mode"] != "confined" {
		t.Fatalf("confine: %d %v", st, out)
	}
	// root without the flag: refused, and the addon stays confined
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/addons/mosquitto/policy", `{"mode":"root","restart":false}`, nil)
	if st != 422 || out["error"] != "unsafe_not_confirmed" {
		t.Fatalf("root without unsafe: %d %v", st, out)
	}
	if p := r.ReadAddonPolicy("mosquitto"); p == nil || p.Mode != "confined" {
		t.Fatalf("the refusal changed the policy: %+v", p)
	}
	// root with it: taken, and recorded as the user's own choice
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/addons/mosquitto/policy", `{"mode":"root","unsafe":true,"restart":false}`, nil)
	if st != 200 || out["policy"].(map[string]any)["mode"] != "root" || out["policy"].(map[string]any)["source"] != "user" {
		t.Fatalf("root with unsafe: %d %v", st, out)
	}
	// and what the pages read: the default of this box, and the addon shown as undeclared
	st, out, _ = do(t, srv, "GET", "/api/system/v1/addons/mosquitto/policy", "", nil)
	if st != 200 || out["default_mode"] != "confined" || out["mode"] != "root" || out["source"] != "user" || out["undeclared"] != true {
		t.Fatalf("policy: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/addons", "", nil)
	list := out["addons"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["policy_mode"] != "root" || list[0].(map[string]any)["undeclared"] != true {
		t.Fatalf("addons: %d %v", st, out)
	}
}

// B-106: the switch with its restart stops the addon before its files change hands and starts it
// after, as the new user; a refused mode stops nothing.
func TestAddonPolicySwitchRestarts(t *testing.T) {
	r := fakeRoot(t)
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte("ok"), nil
	}
	useOwnNoted(t, func(line string) { calls = append(calls, line) })
	sa := system.NewSystemdAddons(r, system.SystemdServices{Root: r, Run: run, StopWait: 10 * time.Millisecond})
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: scriptBox{system.AddonScripts{Root: r}}, Addons: sa, Manager: sa}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "PUT", "/api/system/v1/addons/mosquitto/policy", `{"mode":"confined"}`, nil)
	if st != 200 || out["restarted"] != true || out["restart_error"] != nil || out["policy"].(map[string]any)["mode"] != "confined" {
		t.Fatalf("confine: %d %v", st, out)
	}
	joined := strings.Join(calls, "\n")
	stop := strings.Index(joined, "systemctl stop --no-pager -- addon-mosquitto.service")
	chown := strings.Index(joined, "chown -R ")
	start := strings.Index(joined, "systemctl start --no-pager -- addon-mosquitto.service")
	if stop < 0 || chown < stop || start < chown || strings.Contains(joined, "systemctl restart") {
		t.Errorf("want the stop, the chown and the start, in that order:\n%s", joined)
	}
	calls = nil
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/addons/mosquitto/policy", `{"mode":"nobody"}`, nil); st != 422 || len(calls) != 0 {
		t.Errorf("a refused mode: %d %v\n%s", st, out, strings.Join(calls, "\n"))
	}
}

// ---- task 27.8 / task 23 / task 27.4: the Log page's panels and the unit editor -------------

// fakeEditor is a service manager that can also edit units (systemd's shape, task 27.4).
type fakeEditor struct {
	fakeServices
	override string
}

func (f *fakeEditor) ReadUnitOverride(_ context.Context, id string) (system.UnitOverride, error) {
	return system.UnitOverride{Unit: id + ".service", Effective: "[Service]\nExecStart=/bin/" + id + "\n", Override: f.override, Path: "/run/systemd/system/" + id + ".service.d/50-occulite.conf"}, nil
}
func (f *fakeEditor) SetUnitOverride(ctx context.Context, id, override string) (system.UnitOverride, error) {
	if override != "" && !strings.Contains(override, "[") {
		return system.UnitOverride{}, errors.New("an override needs a [Section] header")
	}
	f.override = override
	return f.ReadUnitOverride(ctx, id)
}

func TestLogLevelsRoutes(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(filepath.Join(string(r), "etc/config"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "etc/config/syslog"), []byte("LOGLEVEL_RFD=5\nLOGLEVEL_HS485D=5\nLOGLEVEL_REGA=2\nLOGLEVEL_HMIP=ERROR\n"), 0o644)
	var live []string
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, SetLogLevel: func(_ context.Context, iface string, level int) error {
		live = append(live, fmt.Sprintf("%s=%d", iface, level))
		if iface == "BidCos-Wired" {
			return errors.New("connection refused")
		}
		return nil
	}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/loglevels", "", nil)
	if st != 200 || out["rfd"] != 5.0 || out["hmip"] != "ERROR" || out["loghost"] != "" {
		t.Fatalf("get: %d %v", st, out)
	}
	// rfd goes live and leaves multimacd's own level alone (task 297); hs485d's live call fails and
	// lands in restart; hmip and LOGHOST mean hmipserver
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/loglevels", `{"rfd":2,"hs485d":4,"hmip":"info","loghost":"log.example.net","lighttpd":{"request_handling":true}}`, nil)
	if st != 200 {
		t.Fatalf("put: %d %v", st, out)
	}
	if fmt.Sprint(live) != "[BidCos-RF=2 BidCos-Wired=4]" {
		t.Errorf("live calls %v", live)
	}
	if fmt.Sprint(out["applied"]) != "[rfd]" || fmt.Sprint(out["restart"]) != "[hmipserver occu-syslog-forward lighttpd hs485d]" {
		t.Errorf("applied %v restart %v", out["applied"], out["restart"])
	}
	if out["errors"].(map[string]any)["hs485d"] != "connection refused" {
		t.Errorf("errors %v", out["errors"])
	}
	b, _ := os.ReadFile(filepath.Join(string(r), "etc/config/syslog"))
	if string(b) != "LOGLEVEL_RFD=2\nLOGLEVEL_HS485D=4\nLOGLEVEL_REGA=2\nLOGLEVEL_HMIP=INFO\nLOGHOST=log.example.net\nLOGLEVEL_MULTIMACD=2\n" {
		t.Errorf("file:\n%s", b)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/loglevels", `{"rfd":3,"hs485d":5,"hmip":"ERROR"}`, nil); st != 422 || out["error"] != "invalid" {
		t.Errorf("a level off the scale: %d %v", st, out)
	}
}

func TestJournalRoutes(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(filepath.Join(string(r), "etc/config"), 0o755)
	svc := &fakeServices{}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte("Archived and active journals take up 32.4M in the file system.\n"), nil
	}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Journal: &system.JournalLog{Run: run}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// fakeRoot is ova: persistent by default; nothing mounted, no journal files, /usr/local there
	st, out, _ := do(t, srv, "GET", "/api/system/v1/journal", "", nil)
	if st != 200 || out["storage"] != "" || out["default_storage"] != "persistent" || out["target"] != "userfs" || out["runtime_max_use"] != "" ||
		out["persist"] != "" || out["default_persistent"] != true || out["persistent"] != false || out["reboot_pending"] != false ||
		out["ram_usage"] != 0.0 || out["target_usage"] != 0.0 || out["target_ok"] != true || !strings.Contains(out["usage"].(string), "32.4M") {
		t.Fatalf("get: %d %v", st, out)
	}
	if free, ok := out["target_free"].(float64); !ok || free <= 0 {
		t.Errorf("target_free %v", out["target_free"])
	}

	conf := filepath.Join(string(r), "etc/config/journal")
	for _, tc := range []struct {
		name, body       string
		status           int
		storage, persist string // the answer's, for a 200
		file             string // the file after a 200; a refused PUT writes none
		msg              string // a part of the message of a refusal
	}{
		{
			name:    "storage, target and the sizes",
			body:    `{"storage":"persistent","target":"userfs","runtime_max_use":"8m","system_max_use":"16M","system_max_file":"","rate_limit_burst":"500"}`,
			status:  200,
			storage: "persistent", persist: "1",
			file: "PERSIST=1\nRATE_LIMIT_BURST=500\nRUNTIME_MAX_USE=8M\nSTORAGE=persistent\nSYSTEM_MAX_USE=16M\nTARGET=userfs\n",
		},
		{
			name:    "the GET view sent back as it is",
			body:    `{"storage":"ram","target":"userfs","persist":"1","platform":"ova","default_storage":"persistent","ram_usage":0,"target_free":null,"target_ok":true}`,
			status:  200,
			storage: "ram", persist: "0",
			file: "PERSIST=0\nSTORAGE=ram\nTARGET=userfs\n",
		},
		{name: "a legacy client: persist 0 and no storage key", body: `{"persist":"0","system_max_use":"16M"}`, status: 200, storage: "ram", persist: "0", file: "PERSIST=0\nSTORAGE=ram\nSYSTEM_MAX_USE=16M\n"},
		{name: "a legacy client: persist 1", body: `{"persist":"1"}`, status: 200, storage: "persistent", persist: "1", file: "PERSIST=1\nSTORAGE=persistent\n"},
		{name: "a legacy client: persist empty is the default", body: `{"persist":""}`, status: 200, file: ""},
		{name: "storage wins over persist", body: `{"storage":"ram","persist":"1"}`, status: 200, storage: "ram", persist: "0", file: "PERSIST=0\nSTORAGE=ram\n"},
		{name: "an empty storage key wins over persist too", body: `{"storage":"","persist":"1"}`, status: 200, file: ""},
		{name: "a null storage is a present key", body: `{"storage":null,"persist":"0"}`, status: 200, file: ""},
		{name: "a bad persist beside storage is not looked at", body: `{"storage":"ram","persist":"maybe"}`, status: 200, storage: "ram", persist: "0", file: "PERSIST=0\nSTORAGE=ram\n"},
		{name: "a bad legacy persist", body: `{"persist":"maybe"}`, status: 422, msg: "persist is 1, 0"},
		{
			name:    "ram-sync with its interval and the copies' limits",
			body:    `{"storage":"ram-sync","target":"userfs","sync_interval":"1h","target_max_use":"128m","target_max_age":"30d"}`,
			status:  200,
			storage: "ram-sync", persist: "0",
			file: "PERSIST=0\nSTORAGE=ram-sync\nSYNC_INTERVAL=1h\nTARGET=userfs\nTARGET_MAX_AGE=30d\nTARGET_MAX_USE=128M\n",
		},
		{name: "an interval below 15 minutes", body: `{"storage":"ram-sync","sync_interval":"5min"}`, status: 422, msg: "15min to 7d"},
		{name: "a plain path is no target", body: `{"storage":"ram-sync","target":"/media/usb0/journal"}`, status: 422, msg: "neither userfs nor a USB stick"},
		// task 216: a USB stick by its label, for ram-sync only
		{name: "ram-sync to a USB stick", body: `{"storage":"ram-sync","target":"usb:LOGSTICK/journal"}`, status: 200, storage: "ram-sync", persist: "0", file: "PERSIST=0\nSTORAGE=ram-sync\nTARGET=usb:LOGSTICK/journal\n"},
		{name: "persistent on a USB stick", body: `{"storage":"persistent","target":"usb:LOGSTICK/journal"}`, status: 422, msg: "persistent on a USB stick is not possible"},
		{name: "the VM's persistent default on a USB stick", body: `{"storage":"","target":"usb:LOGSTICK/journal"}`, status: 422, msg: "default is persistent"},
		{name: "a stick target that climbs out", body: `{"storage":"ram-sync","target":"usb:LOGSTICK/../etc"}`, status: 422, msg: "neither userfs nor a USB stick"},
		{name: "an unknown mode", body: `{"storage":"tape"}`, status: 422, msg: "ram, ram-sync, persistent or empty"},
		{name: "a bad RAM limit", body: `{"storage":"ram","runtime_max_use":"lots"}`, status: 422, msg: "a size"},
		{name: "not an object", body: `["ram"]`, status: 400},
		{name: "not JSON", body: `{`, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(conf)
			svc.calls = nil
			st, out, _ := do(t, srv, "PUT", "/api/system/v1/journal", tc.body, nil)
			if st != tc.status {
				t.Fatalf("status %d, want %d: %v", st, tc.status, out)
			}
			b, err := os.ReadFile(conf)
			if tc.status != 200 {
				if err == nil || len(svc.calls) != 0 {
					t.Errorf("a refused PUT wrote %q and ran %v", b, svc.calls)
				}
				if tc.status == 422 && (out["error"] != "invalid" || !strings.Contains(fmt.Sprint(out["message"]), tc.msg)) {
					t.Errorf("refusal %v, want %q in the message", out, tc.msg)
				}
				return
			}
			if string(b) != tc.file {
				t.Errorf("file:\n%s\nwant:\n%s", b, tc.file)
			}
			if fmt.Sprint(svc.calls) != "[occu-persist restart]" {
				t.Errorf("the script was not re-run: %v", svc.calls)
			}
			target := "userfs"
			if strings.Contains(tc.body, `"usb:`) {
				target = "usb:LOGSTICK/journal"
			}
			if out["storage"] != tc.storage || out["persist"] != tc.persist || out["target"] != target || out["default_storage"] != "persistent" {
				t.Errorf("answer %v", out)
			}
		})
	}
	// no journald: 501, and nothing written
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	if st, out, _ := do(t, srv2, "GET", "/api/system/v1/journal", "", nil); st != 501 || out["error"] != "not-systemd" {
		t.Errorf("busybox: %d %v", st, out)
	}
}

func TestUnitOverrideRoutes(t *testing.T) {
	r := fakeRoot(t)
	ed := &fakeEditor{}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: ed}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/services/rfd/unit", "", nil)
	if st != 200 || out["unit"] != "rfd.service" || out["override"] != "" || !strings.Contains(out["effective"].(string), "ExecStart") {
		t.Fatalf("get: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/services/rfd/unit", `{"override":"[Service]\nEnvironment=X=1\n"}`, nil)
	if st != 200 || out["override"] != "[Service]\nEnvironment=X=1\n" || out["path"] != "/run/systemd/system/rfd.service.d/50-occulite.conf" {
		t.Fatalf("put: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/services/rfd/unit", `{"override":"Environment=X=1\n"}`, nil); st != 422 || out["error"] != "invalid" {
		t.Errorf("no section: %d %v", st, out)
	}
	// a manager that cannot edit: 501
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: r, Services: &fakeServices{}}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	if st, out, _ := do(t, srv2, "GET", "/api/system/v1/services/rfd/unit", "", nil); st != 501 || out["error"] != "not-systemd" {
		t.Errorf("busybox: %d %v", st, out)
	}
}

// ---- task 28.8: the addon control route --------------------------------------------------

type fakeAddonCtl map[string]string

func (f fakeAddonCtl) Lookup(tok string) (string, bool) { id, ok := f[tok]; return id, ok }

func TestAddonCtlRoute(t *testing.T) {
	r := fakeRoot(t)
	svc := &fakeServices{}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, AddonCtl: fakeAddonCtl{"tok-redmatic": "redmatic"}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// the token is the authority: its addon's unit, the three actions, nothing else
	st, out, _ := do(t, srv, "POST", "/api/system/v1/addonctl", `{"action":"restart"}`, map[string]string{"Authorization": "Bearer tok-redmatic"})
	if st != 200 || out["unit"] != "addon-redmatic.service" {
		t.Fatalf("restart: %d %v", st, out)
	}
	if fmt.Sprint(svc.calls) != "[addon-redmatic restart]" {
		t.Errorf("calls %v", svc.calls)
	}
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/addonctl", `{"action":"enable"}`, map[string]string{"Authorization": "Bearer tok-redmatic"}); st != 422 {
		t.Errorf("enable must be refused: %d", st)
	}
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/addonctl", `{"action":"stop"}`, map[string]string{"Authorization": "Bearer nope"}); st != 401 {
		t.Errorf("unknown token: %d", st)
	}
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/addonctl", `{"action":"stop"}`, nil); st != 401 {
		t.Errorf("no token: %d", st)
	}
	// off systemd: 501
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	if st, _, _ := do(t, srv2, "POST", "/api/system/v1/addonctl", `{"action":"stop"}`, map[string]string{"Authorization": "Bearer x"}); st != 501 {
		t.Errorf("busybox: %d", st)
	}
}

// task 34: on a container the host owns the network, the clock and the rootfs - the routes that
// would change them answer 501 host-managed, the reads say so, and the release check still runs.
func TestContainerRoutes(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(filepath.Join(string(r), "etc/config"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "etc/config/netconfig"), []byte("HOSTNAME=openccu\nMODE=DHCP\nCURRENT_IP=192.0.2.119\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(string(r), "run/systemd"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "run/systemd/container"), []byte("lxc\n"), 0o644)
	_ = os.WriteFile(filepath.Join(string(r), "VERSION"), []byte("VERSION=3.89.8.20260719\nPRODUCT=lxc_amd64\nPLATFORM=lxc\nVARIANT=lite\nLITE=1.0.0-alpha.0\n"), 0o644)
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		return nil, nil
	}
	tx := &system.NetTx{Root: r, Applier: system.NetApplier{Root: r, Iface: "eth0", Run: run}, Window: time.Minute}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, NetTx: tx, Run: run}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/status", "", nil)
	if st != 200 || out["container"] != "lxc" {
		t.Fatalf("status: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/network", "", nil)
	if st != 200 || out["writable"] != false || out["host_managed"] != true || out["current"].(map[string]any)["address"] != "192.0.2.119" {
		t.Fatalf("network: %d %v", st, out)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/system/v1/network", `{"hostname":"openccu","mode":"dhcp"}`},
		{"POST", "/api/system/v1/network/confirm", `{"token":"x"}`},
		{"POST", "/api/system/v1/network/revert", `{"token":"x"}`},
		{"POST", "/api/system/v1/system-update/upload?name=x.zip", "zzz"},
		{"POST", "/api/system/v1/system-update/install", ""},
		{"POST", "/api/system/v1/system-update/download", ""},
		{"POST", "/api/system/v1/time/clock", `{"time":"2026-09-09T00:00:00Z"}`},
		{"PUT", "/api/system/v1/time", `{"ntp_servers":["ptbtime1.ptb.de"]}`},
	} {
		st, out, _ := do(t, srv, c.method, c.path, c.body, nil)
		if st != 501 || out["error"] != "host-managed" {
			t.Fatalf("%s %s: %d %v", c.method, c.path, st, out)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("something ran on a container: %v", calls)
	}
	if _, err := os.Stat(filepath.Join(string(r), "usr/local/.firmwareUpdate")); err == nil {
		t.Fatal("an update was staged on a container")
	}
	// the zone is a file on the userfs and stays settable (hwclock's failure inside a
	// container is ignored by pushClock, as it always was)
	_ = os.MkdirAll(filepath.Join(string(r), "usr/share/zoneinfo/Europe"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "usr/share/zoneinfo/Europe/Berlin"), []byte("TZif2"), 0o644)
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/time", `{"zone":"Europe/Berlin"}`, nil)
	if st != 200 {
		t.Fatalf("zone on a container: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/system/v1/system-update", "", nil)
	if st != 200 || out["container"] != "lxc" || out["staged"] != nil {
		t.Fatalf("system-update: %d %v", st, out)
	}
}

// fwPriv records the firewall loads for the API tests; everything else is the local helper.
type fwPriv struct {
	priv.Local
	loads        []time.Duration
	confirms     int
	last4, last6 string
}

func (p *fwPriv) FirewallLoad(_ context.Context, v4, v6 []byte, window time.Duration) error {
	if err := priv.ValidFirewallText(v4); err != nil {
		return err
	}
	if err := priv.ValidFirewallText(v6); err != nil {
		return err
	}
	p.loads = append(p.loads, window)
	p.last4, p.last6 = string(v4), string(v6)
	return nil
}
func (p *fwPriv) FirewallConfirm(context.Context) error { p.confirms++; return nil }

// FirewallCounters answers what the last load wrote, with every line counted once (task 167).
func (p *fwPriv) FirewallCounters(context.Context) (map[string][]byte, error) {
	out := map[string][]byte{}
	for f, text := range map[string]string{"ipv4": p.last4, "ipv6": p.last6} {
		var b strings.Builder
		for _, l := range strings.Split(text, "\n") {
			switch {
			case strings.HasPrefix(l, ":INPUT "):
				b.WriteString(strings.Replace(l, "[0:0]", "[5:500]", 1) + "\n")
			case strings.HasPrefix(l, "-A lite-input "):
				b.WriteString("[1:100] " + l + "\n")
			}
		}
		out[f] = []byte(b.String())
	}
	return out, nil
}
func (p *fwPriv) FirewallRevert(context.Context) error { return nil }
func (p *fwPriv) SocketOwners(context.Context) ([]priv.SocketOwner, error) {
	return []priv.SocketOwner{{Inode: 12345, PID: 412, Comm: "lighttpd", Unit: "lighttpd.service"}}, nil
}

// task 157: the rule list over the API - the conversion at the start, a draft, Apply with the
// window, Confirm writing the file, the listeners with their owner and coverage
func TestFirewallRuleRoutes(t *testing.T) {
	r := fakeRoot(t)
	w := func(p, c string) {
		full := filepath.Join(string(r), p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(c), 0o644)
	}
	w("etc/config/firewall.conf", "MODE = RESTRICTIVE\nIPs = \nUSERPORTS = 5000\n")
	w("proc/net/tcp", "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
		"   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0\n"+
		"   1: 00000000:075B 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0\n")
	fp := &fwPriv{}
	old := system.Priv
	system.Priv = fp
	t.Cleanup(func() { system.Priv = old })
	fr := &system.FirewallRules{Root: r, StateDir: t.TempDir(), Window: time.Minute}
	if err := fr.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Addons: svc, Manager: svc, Nav: svc, FirewallRules: fr}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/firewall", "", nil)
	cfg := out["config"].(map[string]any)
	rules := cfg["rules"].([]any)
	if st != 200 || cfg["policy"].(map[string]any)["ipv4"] != "DROP" || len(rules) != 14 || cfg["migration"] == nil || out["draft"] != nil {
		t.Fatalf("firewall: %d %v", st, out)
	}
	if fr := out["frame"].(map[string]any); len(fr["ipv4"].([]any)) == 0 || out["owners"].(map[string]any)["web"] != "Web server" {
		t.Fatalf("frame/owners: %v", out)
	}
	// a draft: the rules as they are plus one; a bad one is refused
	body, _ := json.Marshal(map[string]any{"policy": cfg["policy"], "rules": append(rules, map[string]any{"port": 1883, "proto": "tcp", "source": "0/0", "family": "both", "target": "ACCEPT"})})
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/firewall", string(body), nil)
	if st != 200 || out["draft"] == nil {
		t.Fatalf("draft: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/firewall", `{"policy":{"ipv4":"DROP","ipv6":"DROP"},"rules":[{"id":"x","port":70000,"proto":"tcp","source":"0/0","family":"both","target":"ACCEPT"}]}`, nil)
	if st != 422 {
		t.Fatalf("bad draft: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/firewall/apply", "", nil)
	if st != 200 || out["pending"] == nil || fp.loads[len(fp.loads)-1] != time.Minute {
		t.Fatalf("apply: %d %v %v", st, out, fp.loads)
	}
	st, _, _ = do(t, srv, "PUT", "/api/system/v1/firewall", string(body), nil)
	if st != 409 {
		t.Fatalf("a draft while pending: %d", st)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/firewall/confirm", "", nil)
	if st != 200 || out["pending"] != nil || out["draft"] != nil || len(out["config"].(map[string]any)["rules"].([]any)) != 15 || fp.confirms != 1 {
		t.Fatalf("confirm: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/system/v1/firewall/migration/dismiss", "", nil)
	if st != 200 || out["config"].(map[string]any)["migration"].(map[string]any)["dismissed"] != true {
		t.Fatalf("dismiss: %d %v", st, out)
	}
	// the listeners: lighttpd's 80 by its owned rule, 1883 by the new rule
	st, out, _ = do(t, srv, "GET", "/api/system/v1/firewall/listeners", "", nil)
	ls := out["listeners"].([]any)
	if st != 200 || len(ls) != 2 {
		t.Fatalf("listeners: %d %v", st, out)
	}
	l80 := ls[0].(map[string]any)
	if l80["process"] != "lighttpd" || l80["unit"] != "lighttpd.service" || l80["cover"].(map[string]any)["ipv4"].(map[string]any)["owner"] != "web" {
		t.Fatalf("80: %v", l80)
	}
	if c := ls[1].(map[string]any)["cover"].(map[string]any)["ipv4"].(map[string]any); c["target"] != "ACCEPT" || c["rule"] == nil {
		t.Fatalf("1883: %v", c)
	}
}

// D-47, task 157: the addons' declared ports and their switches; an opened port is an owned rule
func TestFirewallAddonPorts(t *testing.T) {
	r := fakeRoot(t)
	w := func(p, c string) {
		full := filepath.Join(string(r), p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(c), 0o644)
	}
	// mosquitto (in rc.d of the fixture) declares two ports, the TLS one described
	policy, _ := json.Marshal(system.AddonPolicy{ID: "mosquitto", Mode: "confined", Runtime: &system.AddonRuntime{Ports: []int{1883, 8883},
		PortInfo: map[string]system.PortInfo{"8883": {Proto: "tcp", TLS: true, Label: map[string]string{"de": "MQTT über TLS", "en": "MQTT over TLS"}}}}})
	w("usr/local/etc/config/addon-policy/mosquitto.json", string(policy))
	told := 0
	old := system.FirewallChanged
	system.FirewallChanged = func(context.Context) { told++ }
	t.Cleanup(func() { system.FirewallChanged = old })
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Addons: svc, Manager: svc, Nav: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ports := func(list []any) []map[string]any {
		t.Helper()
		if len(list) != 1 || list[0].(map[string]any)["id"] != "mosquitto" || list[0].(map[string]any)["name"] != "Mosquitto" || list[0].(map[string]any)["mode"] != "confined" {
			t.Fatalf("addons: %v", list)
		}
		var ps []map[string]any
		for _, p := range list[0].(map[string]any)["ports"].([]any) {
			ps = append(ps, p.(map[string]any))
		}
		return ps
	}
	st, list, _ := doList(t, srv, "GET", "/api/system/v1/firewall/addons", "")
	if st != 200 {
		t.Fatalf("addons: %d %v", st, list)
	}
	ps := ports(list)
	if len(ps) != 2 || ps[0]["port"] != float64(1883) || ps[0]["open"] != false || ps[1]["tls"] != true || ps[1]["label"].(map[string]any)["en"] != "MQTT over TLS" {
		t.Fatalf("ports: %v", ps)
	}
	st, list, _ = doList(t, srv, "PUT", "/api/system/v1/firewall/addons/mosquitto/ports", `{"open":[8883]}`)
	if st != 200 || ports(list)[1]["open"] != true || told != 1 {
		t.Fatalf("open: %d %v %d", st, list, told)
	}
	if o := (system.FirewallManager{Root: r}).AddonOwners(); len(o["addon:mosquitto"]) != 1 || o["addon:mosquitto"][0].Port != 8883 {
		t.Fatalf("owners: %v", o)
	}
	st, out, _ := do(t, srv, "PUT", "/api/system/v1/firewall/addons/mosquitto/ports", `{"open":[9999]}`, nil)
	if st != 422 || out["error"] != "invalid" {
		t.Fatalf("undeclared: %d %v", st, out)
	}
	st, _, _ = do(t, srv, "PUT", "/api/system/v1/firewall/addons/nope/ports", `{"open":[]}`, nil)
	if st != 422 {
		t.Fatalf("no policy: %d", st)
	}
}

// D-47 on the first container: mosquitto's policy was written before its entry declared any
// ports, so the declaration has to come from the addon's stored manifest (D-119)
func TestFirewallAddonPortsFromTheManifest(t *testing.T) {
	r := fakeRoot(t)
	w := func(p, c string) {
		full := filepath.Join(string(r), p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(c), 0o644)
	}
	policy, _ := json.Marshal(system.AddonPolicy{ID: "mosquitto", Mode: "confined", Source: "catalog"})
	w("usr/local/etc/config/addon-policy/mosquitto.json", string(policy))
	w("usr/local/etc/config/addon-policy/mosquitto.manifest.json", `{"format": 1, "id": "mosquitto", "name": "Mosquitto",
		"runtime": {"ports": [1883, 8883], "port_info": {"8883": {"proto": "tcp", "tls": true, "label": {"en": "MQTT over TLS"}}}}}`)
	old := system.FirewallChanged
	system.FirewallChanged = func(context.Context) {}
	t.Cleanup(func() { system.FirewallChanged = old })
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Addons: svc, Manager: svc, Nav: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	st, list, _ := doList(t, srv, "GET", "/api/system/v1/firewall/addons", "")
	if st != 200 || len(list) != 1 || len(list[0].(map[string]any)["ports"].([]any)) != 2 {
		t.Fatalf("firewall: %d %v", st, list)
	}
	st, list, _ = doList(t, srv, "PUT", "/api/system/v1/firewall/addons/mosquitto/ports", `{"open":[8883]}`)
	if st != 200 {
		t.Fatalf("open: %d %v", st, list)
	}
	p := list[0].(map[string]any)["ports"].([]any)[1].(map[string]any)
	if p["open"] != true || p["tls"] != true {
		t.Fatalf("%v", p)
	}
	// the policy itself still has no block: the switch does not copy the manifest into it
	if pol := r.ReadAddonPolicy("mosquitto"); pol.Runtime != nil || len(pol.OpenPorts) != 1 {
		t.Fatalf("%+v", pol)
	}
}

// task 42: the USB list on a root without a controller is an empty list, not an error
func TestUSBRoute(t *testing.T) {
	srv := systemServer(t)
	st, out, _ := do(t, srv, "GET", "/api/system/v1/usb", "", nil)
	if st != 200 {
		t.Fatalf("usb: %d %v", st, out)
	}
	if devs, ok := out["devices"].([]any); !ok || len(devs) != 0 {
		t.Fatalf("usb devices: %v", out["devices"])
	}
}

// task 41: the radio firmware routes - admin-only, the list, an upload through the multipart
// form, a refused one, the delete, and a flash that is refused with the reason
func TestRadioFirmwareRoutes(t *testing.T) {
	r := fakeRoot(t)
	root := string(r)
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(c), 0o644)
	}
	w("var/hm_mode", "HM_HMIP_DEV='HMIP-RFUSB'\nHM_HMIP_DEVNODE='/dev/raw-uart1'\nHM_HMIP_VERSION='4.4.18'\nHM_HMRF_DEV='HMIP-RFUSB'\nHM_HMRF_DEVNODE='/dev/raw-uart1'\nHM_HMRF_VERSION='4.4.18'\n")
	w("firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3", "shipped")
	w("etc/config/force-no-coprocessor-update", "")
	svc := scriptBox{system.AddonScripts{Root: r}}
	fw := &system.RadioFirmware{Root: r, Services: svc, StateDir: filepath.Join(root, "state/radio-firmware")}
	api := &SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc, RadioFirmware: fw}
	mux := http.NewServeMux()
	api.Register(mux)
	role := auth.RoleAdmin
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: role, Scopes: auth.RoleScopes(role)})))
	}))
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/radio/firmware", "", nil)
	if st != 200 || out["force_no_update"] != true {
		t.Fatalf("status: %d %v", st, out)
	}
	mods := out["modules"].([]any)
	if len(mods) != 1 || mods[0].(map[string]any)["verdict"] != "up-to-date" || mods[0].(map[string]any)["running_version"] != "4.4.18" {
		t.Fatalf("modules: %v", mods)
	}
	// an upload through the form
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "dualcopro_update_blhmip-4.4.22.eq3")
	_, _ = part.Write([]byte("uploaded bytes"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/system/v1/radio/firmware/upload?module=HMIP-RFUSB", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	_ = json.NewDecoder(res.Body).Decode(&f)
	res.Body.Close()
	if res.StatusCode != 200 || f["version"] != "4.4.22" || f["source"] != "uploaded" || f["size"] != float64(14) {
		t.Fatalf("upload: %d %v", res.StatusCode, f)
	}
	if _, err := os.Stat(filepath.Join(root, "usr/local/etc/config/radio-firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.22.eq3")); err != nil {
		t.Fatal("the upload did not land")
	}
	// a raw body with ?name=, refused: a zip
	st, out, _ = do(t, srv, "POST", "/api/system/v1/radio/firmware/upload?module=HMIP-RFUSB&name=firmware.zip", "zip", map[string]string{"Content-Type": "application/octet-stream"})
	if st != 422 || out["error"] != "firmware-rejected" {
		t.Fatalf("zip: %d %v", st, out)
	}
	// the flash is refused by the kill switch, with the reason
	st, out, _ = do(t, srv, "POST", "/api/system/v1/radio/firmware/flash", `{"module":"HMIP-RFUSB","file":"dualcopro_update_blhmip-4.4.22.eq3"}`, nil)
	if st != 422 || !strings.Contains(out["message"].(string), "switched off") {
		t.Fatalf("flash: %d %v", st, out)
	}
	// delete: the uploaded one goes, the shipped one is refused
	st, _, _ = do(t, srv, "DELETE", "/api/system/v1/radio/firmware/HMIP-RFUSB/dualcopro_update_blhmip-4.4.22.eq3", "", nil)
	if st != 200 {
		t.Fatalf("delete: %d", st)
	}
	st, _, _ = do(t, srv, "DELETE", "/api/system/v1/radio/firmware/HMIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3", "", nil)
	if st != 422 {
		t.Fatalf("delete shipped: %d", st)
	}
	// a user role sees nothing of it
	role = auth.RoleUser
	if st, _, _ = do(t, srv, "GET", "/api/system/v1/radio/firmware", "", nil); st != 403 {
		t.Fatalf("user GET: %d", st)
	}
	// without the service: 501
	api.RadioFirmware = nil
	role = auth.RoleAdmin
	if st, _, _ = do(t, srv, "GET", "/api/system/v1/radio/firmware", "", nil); st != 501 {
		t.Fatalf("no service: %d", st)
	}
}

// doList is do for an answer that is a JSON array.
func doList(t *testing.T, srv *httptest.Server, method, path, body string) (int, []any, string) {
	t.Helper()
	st, _, raw := do(t, srv, method, path, body, nil)
	var out []any
	_ = json.Unmarshal([]byte(raw), &out)
	return st, out, raw
}

// task 167: the counters laid onto the loaded rules, summed over both families and a rule's
// several lines; the reset loads again
func TestFirewallCountersRoute(t *testing.T) {
	r := fakeRoot(t)
	fp := &fwPriv{}
	old := system.Priv
	system.Priv = fp
	t.Cleanup(func() { system.Priv = old })
	fr := &system.FirewallRules{Root: r, StateDir: t.TempDir(), Window: time.Minute}
	if err := fr.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Addons: svc, Manager: svc, Nav: svc, FirewallRules: fr}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	st, out, _ := do(t, srv, "GET", "/api/system/v1/firewall/counters", "", nil)
	if st != 200 || out["known"] != true || out["policy"].(map[string]any)["ipv4"].(map[string]any)["packets"] != float64(5) {
		t.Fatalf("counters: %d %v", st, out)
	}
	c, _ := r.ReadRules()
	var web string
	for _, x := range c.Rules {
		if x.Port == 80 {
			web = x.ID
		}
	}
	// the web rule is one line per local network in each family, each counted once
	l := fr.LocalNetworks()
	want := float64(len(l.V4) + len(l.V6))
	if got := out["rules"].(map[string]any)[web].(map[string]any)["packets"]; got != want {
		t.Fatalf("web: %v, want %v", got, want)
	}
	loads := len(fp.loads)
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/firewall/counters/reset", "", nil); st != 200 || len(fp.loads) != loads+1 {
		t.Fatalf("reset: %d, loads %d", st, len(fp.loads))
	}
}

// openccu-lite B-274: /bin/install_addon outside occulited hands its archive to POST
// /addons/install/local with the install token - the same job as an upload, answered for a shell:
// the output as text, the installer's exit code in a header. Without the token, with another one,
// or from another system (lighttpd's forwarded address) the route refuses.
func TestInstallLocalRoute(t *testing.T) {
	r := fakeRoot(t)
	svc := scriptBox{system.AddonScripts{Root: r}}
	_ = os.MkdirAll(filepath.Join(string(r), "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "bin/install_addon"), []byte("#!/bin/sh\n[ -f "+filepath.Join(string(r), "usr/local/tmp/new_addon.tar.gz")+" ] || exit 101\nrm -f "+filepath.Join(string(r), "usr/local/tmp/new_addon.tar.gz")+"\necho installed\nexit 10\n"), 0o755)
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc, InstallToken: "the-token"}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	archive := strings.Repeat("z", 300)
	post := func(hdr map[string]string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/system/v1/addons/install/local", strings.NewReader(archive))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	for name, hdr := range map[string]map[string]string{
		"no token":            nil,
		"another token":       {"Authorization": "Bearer nope"},
		"from another system": {"Authorization": "Bearer the-token", "X-Forwarded-For": "192.0.2.7"},
	} {
		if res, _ := post(hdr); res.StatusCode != 401 {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	res, body := post(map[string]string{"Authorization": "Bearer the-token"})
	if res.StatusCode != 200 || res.Header.Get(InstallExitHeader) != "10" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") || body != "installed\n" {
		t.Fatalf("install: %d exit %q %q", res.StatusCode, res.Header.Get(InstallExitHeader), body)
	}
	// the same job list as the uploads
	if st, job, _ := do(t, srv, "GET", "/api/system/v1/addons/install", "", nil); st != 200 || job["state"] != "done" {
		t.Fatalf("the job: %d %v", st, job)
	}
	// an installer that fails is its exit code, with a 422
	_ = os.WriteFile(filepath.Join(string(r), "bin/install_addon"), []byte("#!/bin/sh\necho broken\nexit 104\n"), 0o755)
	res, body = post(map[string]string{"Authorization": "Bearer the-token"})
	if res.StatusCode != 422 || res.Header.Get(InstallExitHeader) != "104" || body != "broken\n" {
		t.Fatalf("failed install: %d exit %q %q", res.StatusCode, res.Header.Get(InstallExitHeader), body)
	}
	// no token configured: the route is closed
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	req, _ := http.NewRequest("POST", srv2.URL+"/api/system/v1/addons/install/local", strings.NewReader(archive))
	req.Header.Set("Authorization", "Bearer ")
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 401 {
		t.Fatalf("without a token configured: %v %v", err, res)
	} else {
		res.Body.Close()
	}
}
