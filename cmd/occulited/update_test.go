package main

import (
	"bufio"
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

// occulited task 22: `occulited update` - the arguments, and the flows against a fake occulited:
// the channel, the downgrade's question, the backup before the install, the sha256 checks, the
// exit codes.

func TestParseUpdateArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		want updateOpts
		err  string
	}{
		{[]string{"check"}, updateOpts{cmd: "check"}, ""},
		{[]string{"check", "--pre", "--json"}, updateOpts{cmd: "check", channel: "pre", json: true}, ""},
		{[]string{"install"}, updateOpts{cmd: "install"}, ""},
		{[]string{"install", "latest", "--stable"}, updateOpts{cmd: "install", channel: "stable"}, ""},
		{[]string{"install", "--yes", "v1.0.0-dev.41", "--no-backup"}, updateOpts{cmd: "install", version: "1.0.0-dev.41", yes: true, noBackup: true}, ""},
		{[]string{"install", "-y", "--file", "/tmp/x.zip"}, updateOpts{cmd: "install", file: "/tmp/x.zip", yes: true}, ""},
		{[]string{"install", "--file=/tmp/x.zip"}, updateOpts{cmd: "install", file: "/tmp/x.zip"}, ""},
		{[]string{"status", "--config", "/c.json"}, updateOpts{cmd: "status", config: "/c.json"}, ""},
		{[]string{"discard", "--json"}, updateOpts{cmd: "discard", json: true}, ""},
		{nil, updateOpts{}, "which command"},
		{[]string{"frobnicate"}, updateOpts{}, "unknown command"},
		{[]string{"check", "--yes"}, updateOpts{}, "check takes only"},
		{[]string{"check", "1.0.0"}, updateOpts{}, "check takes only"},
		{[]string{"status", "--pre"}, updateOpts{}, "status takes only"},
		{[]string{"install", "--pre", "--stable"}, updateOpts{}, "exclude each other"},
		{[]string{"install", "1", "2"}, updateOpts{}, "one version at a time"},
		{[]string{"install", "1.0.0", "--file", "x"}, updateOpts{}, "--file installs that file"},
		{[]string{"install", "1.0.0", "--pre"}, updateOpts{}, "either channel"},
		{[]string{"install", "--file"}, updateOpts{}, "needs a value"},
		{[]string{"install", "--yes=1"}, updateOpts{}, "takes no value"},
		{[]string{"install", "--force"}, updateOpts{}, "unknown option"},
	} {
		got, err := parseUpdateArgs(c.args)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%v: %v, want %q", c.args, err, c.err)
			}
			continue
		}
		if c.want.config == "" {
			c.want.config = "/usr/local/etc/occulite/occulited.json"
		}
		if err != nil || got != c.want {
			t.Errorf("%v: %+v %v, want %+v", c.args, got, err, c.want)
		}
	}
}

// fakeOcculited answers the routes `occulited update` calls, as the daemon does.
type fakeOcculited struct {
	mu        sync.Mutex
	running   string
	releases  []sysupdate.Release
	targets   []map[string]any // id, name, enabled
	backupOK  bool
	backupRan int
	now       func() time.Time
	staged    map[string]any
	installed bool
	uploaded  []byte
	calls     []string
	noSHA     bool // the download comes back unverified
}

func (f *fakeOcculited) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer olt_console" {
			w.WriteHeader(401)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/api/system/v1")
		f.calls = append(f.calls, r.Method+" "+p)
		reply := func(st int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(st)
			_ = json.NewEncoder(w).Encode(v)
		}
		switch r.Method + " " + p {
		case "GET /system-update":
			reply(200, map[string]any{"running": map[string]any{"version": "3.89.11", "product": "ova", "platform": "ova", "variant": "lite", "lite": f.running}, "staged": f.staged, "feed": map[string]any{"enabled": true, "checked": "2026-10-07T08:00:00Z", "available": map[string]any{"version": "1.0.0-dev.43", "newer": sysupdate.Direction(f.running, "1.0.0-dev.43") == "upgrade"}, "installed_newer": sysupdate.Direction(f.running, "1.0.0-dev.43") == "downgrade"}, "container": ""})
		case "GET /system-update/releases":
			ch := r.URL.Query().Get("channel")
			out := sysupdate.ReleaseList{Running: f.running, Channel: ch, Default: "pre", Releases: []sysupdate.Release{}}
			if ch == "" {
				out.Channel = "pre"
			}
			for _, x := range f.releases {
				if ch == "stable" && x.Prerelease {
					continue
				}
				x.Direction = sysupdate.Direction(f.running, x.Version)
				out.Releases = append(out.Releases, x)
			}
			reply(200, out)
		case "POST /system-update/download":
			var b struct{ Version string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.staged = map[string]any{"file": "openccu-lite-x86_64-ova-" + b.Version + ".zip", "size": 1, "kind": "zip", "version": b.Version, "sha256": "ab"}
			if f.noSHA {
				delete(f.staged, "sha256")
			}
			reply(200, f.staged)
		case "POST /system-update/upload":
			f.uploaded, _ = io.ReadAll(r.Body)
			name := r.URL.Query().Get("name")
			f.staged = map[string]any{"file": name, "size": len(f.uploaded), "kind": "zip"}
			if strings.Contains(name, "rpi4") {
				f.staged["foreign"] = true
				f.staged["warning"] = `the file is for "aarch64-rpi4", this system runs "ova": the recovery will refuse it`
			}
			reply(200, f.staged)
		case "DELETE /system-update":
			f.staged = nil
			reply(200, map[string]any{"staged": nil})
		case "POST /system-update/install":
			if f.staged == nil {
				reply(409, map[string]any{"error": "nothing-staged", "message": "no update staged"})
				return
			}
			f.installed = true
			reply(200, map[string]any{"armed": true, "rebooting": true})
		case "GET /backup/targets":
			reply(200, map[string]any{"targets": f.targets})
		case "POST /backup/run":
			f.backupRan++
			for _, tg := range f.targets {
				if tg["enabled"] == true {
					tg["last_backup"] = map[string]any{"at": f.now(), "ok": f.backupOK, "name": "box-1.sbk.age", "size": 3 << 20, "error": map[bool]string{false: "the target is full"}[f.backupOK]}
				}
			}
			reply(202, map[string]any{"started": true})
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	})
}

func newFakeOcculited(t *testing.T) (*fakeOcculited, *httptest.Server, *time.Time) {
	clock := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	f := &fakeOcculited{running: "1.0.0-dev.42", backupOK: true, now: func() time.Time { return clock }}
	rel := func(v string, pre bool) sysupdate.Release {
		return sysupdate.Release{Available: sysupdate.Available{Version: v, Name: "openccu-lite-x86_64-ova-" + v + ".zip", Size: 700 << 20, SHA256URL: "https://example.org/" + v + ".sha256", Published: "2026-10-06T10:00:00Z"}, Prerelease: pre}
	}
	f.releases = []sysupdate.Release{rel("1.0.0-dev.43", true), rel("1.0.0-dev.42", true), rel("1.0.0-dev.41", true), rel("0.9.0", false)}
	f.targets = []map[string]any{{"id": "directory", "name": "USB", "enabled": true, "state": map[string]any{"state": "idle"}}, {"id": "nas", "name": "NAS", "enabled": false, "state": map[string]any{"state": "idle"}}}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return f, srv, &clock
}

// runUpdate runs one command line against the fake; stdin is the terminal's answer ("" = no
// terminal).
func runUpdate(t *testing.T, srv *httptest.Server, clock *time.Time, stdin string, args ...string) (int, string, string) {
	t.Helper()
	o, err := parseUpdateArgs(args)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	var out, errOut bytes.Buffer
	e := &updateEnv{
		api: &updateAPI{base: srv.URL, token: "olt_console", http: srv.Client()},
		in:  bufio.NewReader(strings.NewReader(stdin)), out: &out, errOut: &errOut, tty: stdin != "",
		now:   func() time.Time { return *clock },
		sleep: func(d time.Duration) { *clock = clock.Add(d) },
	}
	code := e.run(context.Background(), o)
	return code, out.String(), errOut.String()
}

func TestUpdateCheck(t *testing.T) {
	f, srv, clock := newFakeOcculited(t)
	code, out, _ := runUpdate(t, srv, clock, "", "check")
	if code != updateExitAvailable || !strings.Contains(out, "newest:    1.0.0-dev.43 - an update") || !strings.Contains(out, "as the Updates page") {
		t.Errorf("%d %q", code, out)
	}
	code, out, _ = runUpdate(t, srv, clock, "", "check", "--stable", "--json")
	var j map[string]any
	if err := json.Unmarshal([]byte(out), &j); err != nil || code != updateExitOK || j["update_available"] != false || j["channel"] != "stable" {
		t.Errorf("%d %q %v", code, out, err)
	}
	f.running = "1.0.0-dev.43"
	if code, out, _ := runUpdate(t, srv, clock, "", "check"); code != updateExitOK || !strings.Contains(out, "up to date") {
		t.Errorf("%d %q", code, out)
	}
	// B-57: an installed version newer than the newest published one says so, and exits 0
	f.running = "1.0.0-dev.44"
	if code, out, _ := runUpdate(t, srv, clock, "", "check"); code != updateExitOK || !strings.Contains(out, "newest:    1.0.0-dev.43 published - the installed 1.0.0-dev.44 is newer, nothing to install") {
		t.Errorf("%d %q", code, out)
	}
	// the credential refused: a hint, exit 1
	bad := &updateEnv{api: &updateAPI{base: srv.URL, token: "olt_other", http: srv.Client()}, out: io.Discard, errOut: &bytes.Buffer{}, now: time.Now, sleep: func(time.Duration) {}}
	if code := bad.run(context.Background(), updateOpts{cmd: "check"}); code != updateExitError || !strings.Contains(bad.errOut.(*bytes.Buffer).String(), "console credential") {
		t.Errorf("%d %s", code, bad.errOut)
	}
}

func TestUpdateStatusAndDiscard(t *testing.T) {
	f, srv, clock := newFakeOcculited(t)
	code, out, _ := runUpdate(t, srv, clock, "", "status")
	if code != 0 || !strings.Contains(out, "installed:   openccu-lite 1.0.0-dev.42") || !strings.Contains(out, "staged:      none") || !strings.Contains(out, "1.0.0-dev.43 (an update)") {
		t.Errorf("%q", out)
	}
	f.staged = map[string]any{"file": "openccu-lite-x86_64-ova-1.0.0-dev.43.zip", "size": 700 << 20, "version": "1.0.0-dev.43", "recovery_armed": false}
	if _, out, _ := runUpdate(t, srv, clock, "", "status"); !strings.Contains(out, "openccu-lite-x86_64-ova-1.0.0-dev.43.zip (1.0.0-dev.43, 700 MB) - staged, not yet scheduled") {
		t.Errorf("%q", out)
	}
	if code, out, _ := runUpdate(t, srv, clock, "", "status", "--json"); code != 0 || !json.Valid([]byte(out)) {
		t.Errorf("%q", out)
	}
	if code, _, _ := runUpdate(t, srv, clock, "", "discard"); code != 0 || f.staged != nil {
		t.Errorf("%d %v", code, f.staged)
	}
	// B-57: the newest published one is what runs
	f.running = "1.0.0-dev.43"
	if _, out, _ := runUpdate(t, srv, clock, "", "status"); !strings.Contains(out, "last check:  2026-10-07T08:00:00Z: 1.0.0-dev.43 (installed)") {
		t.Errorf("%q", out)
	}
	// B-57: a round not yet published runs - it is newer, not "installed" with the feed's version
	f.running = "1.0.0-dev.44"
	if _, out, _ := runUpdate(t, srv, clock, "", "status"); !strings.Contains(out, "newest published 1.0.0-dev.43; the installed 1.0.0-dev.44 is newer") || strings.Contains(out, "(installed)") {
		t.Errorf("%q", out)
	}
}

func TestUpdateInstallLatest(t *testing.T) {
	f, srv, clock := newFakeOcculited(t)
	// no terminal and no --yes: nothing happens, exit 3
	if code, _, errOut := runUpdate(t, srv, clock, "", "install"); code != updateExitDeclined || !strings.Contains(errOut, "--yes") || f.backupRan != 0 || f.staged != nil {
		t.Fatalf("%d %q", code, errOut)
	}
	// answered no on the terminal
	if code, _, _ := runUpdate(t, srv, clock, "n\n", "install"); code != updateExitDeclined || f.backupRan != 0 {
		t.Fatalf("%d", code)
	}
	// answered yes: the backup to the enabled target, the download, the install
	code, out, errOut := runUpdate(t, srv, clock, "y\n", "install")
	if code != 0 || !f.installed || f.backupRan != 1 || f.staged["version"] != "1.0.0-dev.43" {
		t.Fatalf("%d %q %q %v", code, out, errOut, f.staged)
	}
	for _, want := range []string{"backup: to USB…", "backup: USB: box-1.sbk.age (3 MB)", "sha256 verified", "rebooting into the recovery system"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q not in %q", want, out)
		}
	}
	if !strings.Contains(errOut, "Install 1.0.0-dev.43 (now 1.0.0-dev.42) and reboot?") {
		t.Errorf("question: %q", errOut)
	}
	// the order: the backup before the download, the download before the install
	calls := strings.Join(f.calls, ",")
	if b, d, i := strings.Index(calls, "POST /backup/run"), strings.Index(calls, "POST /system-update/download"), strings.Index(calls, "POST /system-update/install"); b < 0 || b > d || d > i {
		t.Errorf("%s", calls)
	}
	// up to date: nothing to do, exit 0
	f.running, f.installed, f.backupRan = "1.0.0-dev.43", false, 0
	if code, out, _ := runUpdate(t, srv, clock, "", "install", "--yes"); code != 0 || f.installed || f.backupRan != 0 || !strings.Contains(out, "up to date") {
		t.Errorf("%d %q", code, out)
	}
	// --stable on a dev system: the newest release is older - up to date as well
	if code, out, _ := runUpdate(t, srv, clock, "", "install", "--stable", "--yes"); code != 0 || f.installed || !strings.Contains(out, "0.9.0") {
		t.Errorf("%d %q", code, out)
	}
}

func TestUpdateInstallVersion(t *testing.T) {
	f, srv, clock := newFakeOcculited(t)
	// a downgrade warns, and needs the answer
	code, _, errOut := runUpdate(t, srv, clock, "", "install", "1.0.0-dev.41")
	if code != updateExitDeclined || !strings.Contains(errOut, "WARNING: 1.0.0-dev.41 is older than the running 1.0.0-dev.42") {
		t.Fatalf("%d %q", code, errOut)
	}
	code, out, errOut := runUpdate(t, srv, clock, "", "install", "1.0.0-dev.41", "--yes", "--json")
	if code != 0 || !f.installed || f.staged["version"] != "1.0.0-dev.41" || !strings.Contains(errOut, "WARNING") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	var j map[string]any
	if err := json.Unmarshal([]byte(out), &j); err != nil || j["direction"] != "downgrade" || j["rebooting"] != true || len(j["backup"].([]any)) != 1 {
		t.Errorf("%q %v", out, err)
	}
	// the running version again: a reinstall, asked as one; --no-backup skips the backup
	f.installed, f.backupRan = false, 0
	code, out, errOut = runUpdate(t, srv, clock, "yes\n", "install", "v1.0.0-dev.42", "--no-backup")
	if code != 0 || !f.installed || f.backupRan != 0 || !strings.Contains(errOut, "Reinstall 1.0.0-dev.42") || !strings.Contains(out, "backup: skipped") {
		t.Errorf("%d %q %q", code, out, errOut)
	}
	// a version the feed does not list
	if code, _, errOut := runUpdate(t, srv, clock, "", "install", "1.0.0-dev.7", "--yes"); code != 1 || !strings.Contains(errOut, "no 1.0.0-dev.7") {
		t.Errorf("%d %q", code, errOut)
	}
	// a download that came back unverified is discarded, nothing installed
	f.installed, f.noSHA = false, true
	if code, _, errOut := runUpdate(t, srv, clock, "", "install", "1.0.0-dev.43", "--yes", "--no-backup"); code != 1 || f.installed || f.staged != nil || !strings.Contains(errOut, "without its sha256 check") {
		t.Errorf("%d %q %v", code, errOut, f.staged)
	}
	// a release without a published .sha256 is refused before anything is fetched
	f.noSHA = false
	f.releases[0].SHA256URL = ""
	if code, _, errOut := runUpdate(t, srv, clock, "", "install", "--yes"); code != 1 || f.installed || !strings.Contains(errOut, "publishes no .sha256") {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestUpdateInstallBackup(t *testing.T) {
	f, srv, clock := newFakeOcculited(t)
	// no enabled target: refused, with the way out
	f.targets[0]["enabled"] = false
	if code, _, errOut := runUpdate(t, srv, clock, "", "install", "--yes"); code != 1 || f.installed || !strings.Contains(errOut, "no backup target is enabled") || !strings.Contains(errOut, "--no-backup") {
		t.Fatalf("%d %q", code, errOut)
	}
	// the backup failed on every target: nothing installed
	f.targets[0]["enabled"] = true
	f.backupOK = false
	code, out, errOut := runUpdate(t, srv, clock, "", "install", "--yes")
	if code != 1 || f.installed || f.staged != nil || !strings.Contains(out, "USB: failed: the target is full") || !strings.Contains(errOut, "no target holds the new backup") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	// a backup already running
	f.targets[0]["state"] = map[string]any{"state": "running"}
	if code, _, errOut := runUpdate(t, srv, clock, "", "install", "--yes"); code != 1 || !strings.Contains(errOut, "is running") {
		t.Errorf("%d %q", code, errOut)
	}
	// a run that never ends: the wait has its bound
	f.targets[0]["state"] = map[string]any{"state": "idle"}
	f.targets[0]["last_backup"] = nil
	f.backupOK = true
	o, _ := parseUpdateArgs([]string{"install", "--yes"})
	var errOut2 bytes.Buffer
	e := &updateEnv{api: &updateAPI{base: srv.URL, token: "olt_console", http: srv.Client()}, out: io.Discard, errOut: &errOut2,
		now: func() time.Time { return *clock }, sleep: func(d time.Duration) {
			*clock = clock.Add(d)
			f.mu.Lock()
			f.targets[0]["state"] = map[string]any{"state": "running"}
			f.mu.Unlock()
		}, backupWait: time.Minute}
	if code := e.run(context.Background(), o); code != 1 || !strings.Contains(errOut2.String(), "not done after 1m0s") {
		t.Errorf("%d %q", code, errOut2.String())
	}
}

func TestUpdateInstallFile(t *testing.T) {
	f, srv, clock := newFakeOcculited(t)
	dir := t.TempDir()
	body := []byte("a release zip")
	sum := sha256.Sum256(body)
	file := filepath.Join(dir, "openccu-lite-x86_64-ova-1.0.0-dev.41.zip")
	_ = os.WriteFile(file, body, 0o644)
	// a .sha256 beside it that does not match: refused before anything else
	_ = os.WriteFile(file+".sha256", []byte(strings.Repeat("0", 64)+"  x\n"), 0o644)
	if code, _, errOut := runUpdate(t, srv, clock, "", "install", "--file", file, "--yes"); code != 1 || !strings.Contains(errOut, "sha256 mismatch") || f.uploaded != nil {
		t.Fatalf("%d %q", code, errOut)
	}
	_ = os.WriteFile(file+".sha256", []byte(hex.EncodeToString(sum[:])+"  x\n"), 0o644)
	code, out, errOut := runUpdate(t, srv, clock, "", "install", "--file", file, "--yes", "--no-backup")
	if code != 0 || !f.installed || string(f.uploaded) != string(body) || !strings.Contains(out, "sha256 verified against") || !strings.Contains(errOut, "WARNING: 1.0.0-dev.41 is older") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	// without a .sha256: said, and installed
	other := filepath.Join(dir, "my-update.zip")
	_ = os.WriteFile(other, body, 0o644)
	f.installed = false
	code, out, errOut = runUpdate(t, srv, clock, "", "install", "--file", other, "--yes", "--no-backup")
	if code != 0 || !f.installed || !strings.Contains(out, "not verified") || !strings.Contains(errOut, "cannot be compared") {
		t.Errorf("%d %q %q", code, out, errOut)
	}
	// a file for another board: discarded again
	foreign := filepath.Join(dir, "openccu-lite-aarch64-rpi4-1.0.0-dev.43.zip")
	_ = os.WriteFile(foreign, body, 0o644)
	f.installed = false
	if code, _, errOut := runUpdate(t, srv, clock, "", "install", "--file", foreign, "--yes", "--no-backup"); code != 1 || f.installed || f.staged != nil || !strings.Contains(errOut, "recovery will refuse it") {
		t.Errorf("%d %q", code, errOut)
	}
	if code, _, errOut := runUpdate(t, srv, clock, "", "install", "--file", filepath.Join(dir, "missing.zip"), "--yes"); code != 1 || !strings.Contains(errOut, "no such file") {
		t.Errorf("%d %q", code, errOut)
	}
}

// the entry: root only, the usage for a wrong shape, the credential's file
func TestUpdateCmdEntry(t *testing.T) {
	oldRoot, oldTok := updateIsRoot, updateTokenFile
	t.Cleanup(func() { updateIsRoot, updateTokenFile = oldRoot, oldTok })
	updateIsRoot = func() bool { return false }
	if code := updateCmd([]string{"frobnicate"}); code != updateExitUsage {
		t.Errorf("usage: %d", code)
	}
	if code := updateCmd([]string{"status"}); code != updateExitError {
		t.Errorf("not root: %d", code)
	}
	updateIsRoot = func() bool { return true }
	updateTokenFile = filepath.Join(t.TempDir(), "console-token")
	gone := httptest.NewServer(http.NotFoundHandler())
	addr := gone.Listener.Addr().String()
	gone.Close()
	cfg := filepath.Join(t.TempDir(), "occulited.json")
	_ = os.WriteFile(cfg, []byte(`{"listen":"`+addr+`"}`), 0o644)
	if code := updateCmd([]string{"status", "--config", cfg}); code != updateExitError {
		t.Errorf("no credential: %d", code)
	}
	_ = os.WriteFile(updateTokenFile, []byte("olt_x\n"), 0o600)
	if code := updateCmd([]string{"status", "--config", cfg}); code != updateExitError {
		t.Errorf("no daemon: %d", code)
	}
	if !strings.Contains(fmt.Sprint(subcommands), "update") {
		t.Error("update is not a subcommand")
	}
}

// /dev/null is a character device, not a terminal: a script's `install </dev/null` is not asked
func TestIsTerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("/dev/null counted as a terminal")
	}
	g, _ := os.CreateTemp(t.TempDir(), "x")
	defer g.Close()
	if isTerminal(g) {
		t.Error("a file counted as a terminal")
	}
}
