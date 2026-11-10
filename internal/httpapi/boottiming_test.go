package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/bootexpect"
	"github.com/hobbyquaker/occulited/internal/system"
)

type bootRig struct {
	root  system.Root
	state string
	m     *powerManager
	api   *SystemAPI
	srv   *httptest.Server
	// restoreErr makes the firmware's restore script fail
	restoreErr error
}

func newBootRig(t *testing.T, withRecorder bool) *bootRig {
	t.Helper()
	g := &bootRig{root: fakeRoot(t), state: t.TempDir(), m: &powerManager{}}
	_ = os.MkdirAll(string(g.root)+"/proc/sys/kernel/random", 0o755)
	_ = os.WriteFile(string(g.root)+"/proc/sys/kernel/random/boot_id", []byte("boot-now\n"), 0o644)
	g.api = &SystemAPI{Root: g.root, Manager: g.m, Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("restored"), g.restoreErr },
		RunStdin: func(context.Context, []byte, string, ...string) ([]byte, error) {
			return []byte("restored"), g.restoreErr
		}}
	if withRecorder {
		g.api.BootTiming = &bootexpect.Recorder{StateDir: g.state, Root: string(g.root), Product: "ova",
			Carry: &bootexpect.Carry{Path: string(g.root) + system.BootMarkerCarryFile, Place: g.root.CarryBootMarker, Remove: g.root.RemoveCarriedBootMarker}}
	}
	mux := http.NewServeMux()
	g.api.Register(mux)
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: auth.RoleAdmin, Scopes: auth.RoleScopes(auth.RoleAdmin)})))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *bootRig) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, g.srv.URL+"/api/system/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return res.StatusCode, m
}

func (g *bootRig) marker(t *testing.T) *bootexpect.Pending {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(g.state, "boot-timing", "pending.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var p bootexpect.Pending
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func (g *bootRig) stage(t *testing.T, armed bool) {
	t.Helper()
	root := string(g.root)
	_ = os.MkdirAll(root+"/usr/local/tmp", 0o755)
	_ = os.WriteFile(root+"/usr/local/tmp/openccu-lite-x86_64-ova-1.0.0.zip", []byte("zip"), 0o644)
	// the link points where it points on a box; the root is joined in front of it
	if err := os.Symlink("/usr/local/tmp/openccu-lite-x86_64-ova-1.0.0.zip", root+"/usr/local/.firmwareUpdate"); err != nil {
		t.Fatal(err)
	}
	if armed {
		_ = os.WriteFile(root+"/usr/local/.recoveryMode", nil, 0o644)
	}
}

// Every route that reboots marks the reboot first, with its kind, and takes the marker back when
// the reboot did not start.
func TestBootMarkers(t *testing.T) {
	restoreFile := func(t *testing.T, g *bootRig) {
		dir := filepath.Join(string(g.root), system.BackupDir)
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, "restore-1.sbk"), liteSBK(t), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		setup  func(t *testing.T, g *bootRig)
		path   string
		body   string
		status int
		kind   string // "" = no marker afterwards
	}{
		{"a reboot", nil, "/reboot", `{"confirm":true}`, 200, bootexpect.KindReboot},
		{"a reboot with an update set to install is the install", func(t *testing.T, g *bootRig) { g.stage(t, true) }, "/reboot", `{"confirm":true}`, 200, bootexpect.KindUpdate},
		{"a reboot with an update staged but not set", func(t *testing.T, g *bootRig) { g.stage(t, false) }, "/reboot", `{"confirm":true}`, 200, bootexpect.KindReboot},
		{"a reboot that fails", func(_ *testing.T, g *bootRig) { g.m.setErr(errors.New("no reboot")) }, "/reboot", `{"confirm":true}`, 500, ""},
		{"a refused reboot marks nothing", nil, "/reboot", `{}`, 400, ""},
		{"into the recovery system", nil, "/reboot/recovery", `{"confirm":true}`, 200, bootexpect.KindRecovery},
		{"into the recovery system, failing", func(_ *testing.T, g *bootRig) { g.m.setErr(errors.New("no reboot")) }, "/reboot/recovery", `{"confirm":true}`, 500, ""},
		{"the system update's install", func(t *testing.T, g *bootRig) { g.stage(t, false) }, "/system-update/install", `{}`, 200, bootexpect.KindUpdate},
		{"the install whose reboot fails", func(t *testing.T, g *bootRig) { g.stage(t, false); g.m.setErr(errors.New("no reboot")) }, "/system-update/install", `{}`, 200, ""},
		{"a restore", restoreFile, "/restore/apply", `{"confirm":true,"file":"restore-1.sbk"}`, 200, bootexpect.KindRestore},
		{"a restore that fails", func(t *testing.T, g *bootRig) { restoreFile(t, g); g.restoreErr = errors.New("exit status 1") }, "/restore/apply", `{"confirm":true,"file":"restore-1.sbk"}`, 422, ""},
		{"a restore whose reboot fails", func(t *testing.T, g *bootRig) { restoreFile(t, g); g.m.setErr(errors.New("no reboot")) }, "/restore/apply", `{"confirm":true,"file":"restore-1.sbk"}`, 200, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newBootRig(t, true)
			if c.setup != nil {
				c.setup(t, g)
			}
			st, out := g.do(t, "POST", c.path, c.body)
			if st != c.status {
				t.Fatalf("status %d %v, want %d", st, out, c.status)
			}
			p := g.marker(t)
			switch {
			case c.kind == "" && p != nil:
				t.Fatalf("a marker stayed: %+v", p)
			case c.kind != "" && (p == nil || p.Kind != c.kind || p.BootID != "boot-now" || p.Product != "ova" || p.RequestWallMS <= 0):
				t.Fatalf("marker %+v, want kind %s", p, c.kind)
			}
			// openccu-lite B-193: a restore's marker has its copy in /usr/local/tmp, which the
			// restore at boot keeps; nothing else has one, and a refused reboot leaves none
			_, err := os.Lstat(filepath.Join(string(g.root), system.BootMarkerCarryFile))
			if carried := err == nil; carried != (c.kind == bootexpect.KindRestore) {
				t.Fatalf("carried copy %v for kind %q", carried, c.kind)
			}
		})
	}
	// without a recorder every route works as before
	g := newBootRig(t, false)
	if st, _ := g.do(t, "POST", "/reboot", `{"confirm":true}`); st != 200 || g.m.Reboots() != 1 {
		t.Fatalf("reboot without a recorder: %d", st)
	}
}

func TestBootExpectRoute(t *testing.T) {
	type answer struct {
		Kind     string              `json:"kind"`
		Product  string              `json:"product"`
		Expect   bootexpect.Expect   `json:"expect"`
		Measured bootexpect.Measured `json:"measured"`
	}
	for _, withRecorder := range []bool{false, true} {
		g := newBootRig(t, withRecorder)
		for _, c := range []struct {
			query  string
			status int
			kind   string
		}{
			{"", 200, "reboot"},
			{"?kind=update", 200, "update"},
			{"?kind=halt", 200, "halt"},
			{"?kind=sideways", 400, ""},
		} {
			res, err := http.Get(g.srv.URL + "/api/system/v1/boot-expect" + c.query)
			if err != nil {
				t.Fatal(err)
			}
			var a answer
			_ = json.NewDecoder(res.Body).Decode(&a)
			res.Body.Close()
			if res.StatusCode != c.status {
				t.Fatalf("recorder %v %s: %d", withRecorder, c.query, res.StatusCode)
			}
			if c.status != 200 {
				continue
			}
			// the fake root is the ova
			if a.Kind != c.kind || a.Product != "ova" || a.Expect != bootexpect.Default("ova", c.kind) {
				t.Fatalf("recorder %v %s: %+v", withRecorder, c.query, a)
			}
		}
	}
}

func TestBootTimingRoute(t *testing.T) {
	const s = 1_800_000_000_000
	body := func(kind string, down, http, ui int64) string {
		b, _ := json.Marshal(map[string]any{"kind": kind, "started": s, "down": down, "http": http, "ui": ui})
		return string(b)
	}
	t.Run("without a recorder", func(t *testing.T) {
		g := newBootRig(t, false)
		if st, _ := g.do(t, "POST", "/boot-timing", body("reboot", s+5000, s+30000, s+32000)); st != 501 {
			t.Fatalf("got %d", st)
		}
	})
	g := newBootRig(t, true)
	// a record finished in this boot, as occulited left it before a restart
	rec := map[string]any{"records": map[string]any{"reboot": []any{map[string]any{"kind": "reboot", "product": "ova", "boot_id": "boot-now", "request_wall_ms": s + 20, "kernel_start_wall_ms": s + 12000, "to_kernel_s": 12, "http_mono_s": 20, "ui_mono_s": 21, "phases": map[string]any{}}}}}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(g.state, "boot-timings.json"), raw, 0o640); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"not JSON", `nope`, 422, "invalid-body"},
		{"an unknown kind", body("sideways", s+5000, s+30000, s+32000), 400, "invalid"},
		{"checkpoints out of order", body("reboot", s+40000, s+30000, s+32000), 400, "invalid"},
		{"nothing recorded of that kind", body("update", s+5000, s+30000, s+32000), 409, "no-record"},
		{"the record of this boot", body("reboot", s+5000, s+30000, s+32000), 200, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, out := g.do(t, "POST", "/boot-timing", c.body)
			if st != c.status || (c.code != "" && out["error"] != c.code) {
				t.Fatalf("got %d %v, want %d %s", st, out, c.status, c.code)
			}
			if st == 200 && out["attached"] != "record" {
				t.Fatalf("answer %v", out)
			}
		})
	}
	// the browser's down now splits the box's 32 s into the shutdown and the boot
	res, err := http.Get(g.srv.URL + "/api/system/v1/boot-expect?kind=reboot")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var a struct {
		Expect   bootexpect.Expect   `json:"expect"`
		Measured bootexpect.Measured `json:"measured"`
	}
	_ = json.NewDecoder(res.Body).Decode(&a)
	if a.Expect.Down != 5 || a.Expect.HTTP != 27 || a.Expect.UI != 1 || a.Measured.Down != 1 {
		t.Fatalf("expect %+v", a)
	}
}

// B-144: the restore hands the key to the script's standard input; the API's plain Runner, which
// has none, is not used for it.
func TestRestoreApplyPassesKeyOnStdin(t *testing.T) {
	g := newBootRig(t, false)
	_ = os.MkdirAll(string(g.root)+system.BackupDir, 0o755)
	_ = os.WriteFile(string(g.root)+system.BackupDir+"/restore-1.sbk", liteSBK(t), 0o644)
	g.api.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("the restore ran through the Runner, which has no stdin: %s %v", name, args)
		return nil, errors.New("no stdin")
	}
	var stdin []string
	var calls []string
	g.api.RunStdin = func(_ context.Context, in []byte, name string, args ...string) ([]byte, error) {
		stdin = append(stdin, string(in))
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte("restored"), nil
	}
	if st, out := g.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-1.sbk","key":"s3cret","force":true}`); st != 200 || out["rebooting"] != true {
		t.Fatalf("%d %v", st, out)
	}
	if st, out := g.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-1.sbk"}`); st != 200 || out["rebooting"] != true {
		t.Fatalf("%d %v", st, out)
	}
	if len(stdin) != 2 || stdin[0] != "s3cret\n" || stdin[1] != "\n" {
		t.Errorf("stdin %q", stdin)
	}
	// openccu-lite B-193: no -r - the script stages, the route reboots through the manager
	if len(calls) != 2 || !strings.HasSuffix(calls[0], "/bin/restoreBackup.sh -f "+string(g.root)+system.BackupDir+"/restore-1.sbk") || !strings.HasSuffix(calls[1], "/bin/restoreBackup.sh "+string(g.root)+system.BackupDir+"/restore-1.sbk") {
		t.Errorf("calls %q", calls)
	}
	if g.m.Reboots() != 2 {
		t.Errorf("reboots %d, want 2", g.m.Reboots())
	}
}

// openccu-lite B-193: the restore's answer is written before the reboot, on the script's own
// context. A client that gives up while the script runs does not cut the staging short; a reboot
// that does not start leaves the restore staged and says so, with no boot marker.
func TestRestoreApplyAnswersBeforeReboot(t *testing.T) {
	g := newBootRig(t, true)
	_ = os.MkdirAll(string(g.root)+system.BackupDir, 0o755)
	_ = os.WriteFile(string(g.root)+system.BackupDir+"/restore-1.sbk", liteSBK(t), 0o644)
	// the client gives up while the script runs: the request's context dies, the script's must not
	clientCtx, giveUp := context.WithCancel(context.Background())
	scriptCtxErr := make(chan error, 1)
	g.api.RunStdin = func(ctx context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the script runs without a deadline")
		}
		giveUp()
		time.Sleep(100 * time.Millisecond)
		scriptCtxErr <- ctx.Err()
		return []byte("4) Scheduling backup restore for next boot cycle, OK"), nil
	}
	req, _ := http.NewRequestWithContext(clientCtx, "POST", g.srv.URL+"/api/system/v1/restore/apply", strings.NewReader(`{"confirm":true,"file":"restore-1.sbk"}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("the client's request was not cut")
	}
	select {
	case err := <-scriptCtxErr:
		if err != nil {
			t.Errorf("the script's context was %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the script did not run")
	}
	// the handler goes on after the script: the marker, then the reboot
	for deadline := time.Now().Add(5 * time.Second); g.m.Reboots() != 1 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if g.m.Reboots() != 1 {
		t.Errorf("reboots %d, want 1", g.m.Reboots())
	}
	// a live request whose reboot the manager refuses
	g.api.RunStdin = func(context.Context, []byte, string, ...string) ([]byte, error) { return []byte("staged"), nil }
	g.m.setErr(errors.New("no reboot"))
	st, out := g.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-1.sbk"}`)
	if st != 200 || out["ok"] != true || out["rebooting"] != false || !strings.Contains(out["message"].(string), "no reboot") {
		t.Fatalf("%d %v", st, out)
	}
	if p := g.marker(t); p != nil {
		t.Errorf("a boot marker stayed although the reboot did not start: %+v", p)
	}
	// no manager at all (a dry run): staged, not rebooting
	g.api.Manager = nil
	if st, out := g.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-1.sbk"}`); st != 200 || out["rebooting"] != false {
		t.Fatalf("%d %v", st, out)
	}
}
