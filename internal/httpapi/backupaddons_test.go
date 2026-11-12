package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
)

type fakeBackupHookRunner struct{ calls []string }

func (f *fakeBackupHookRunner) BackupHook(_ context.Context, id, step string) (priv.BackupHookResult, error) {
	f.calls = append(f.calls, id+":"+step)
	if id == "history" {
		return priv.BackupHookResult{Step: step, Exit: 2, Output: "no stick"}, nil
	}
	return priv.BackupHookResult{Step: step, Bytes: 2048, Files: 1}, nil
}

// openccu-lite task 399: GET /backup/addons lists the declared addons with their switches, PUT
// switches one (system:write), a backup runs the hooks of the switched-on ones and keeps their
// results, a failed one is the warning backup-addon.
func TestBackupAddonsRoutes(t *testing.T) {
	root := fakeRoot(t)
	w := func(p, c string) {
		t.Helper()
		full := filepath.Join(string(root), p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("usr/local/etc/config/addon-policy/battery.json", `{"id":"battery","mode":"confined","uid":30007}`)
	w("usr/local/etc/config/addon-policy/battery.manifest.json", `{"format":1,"id":"battery","name":{"de":"Batterien","en":"Batteries"},"backup":{"hook":"bin/backup-snapshot","staging":"var/backup","size_hint_mb":20}}`)
	w("usr/local/etc/config/addon-policy/history.json", `{"id":"history","mode":"confined","uid":30008}`)
	w("usr/local/etc/config/addon-policy/history.manifest.json", `{"format":1,"id":"history","name":"History","backup":{"hook":"bin/snap","staging":"var/backup","size_hint_mb":500}}`)
	w("usr/local/etc/config/addon-policy/mosquitto.json", `{"id":"mosquitto","mode":"confined","uid":30009}`)
	runner := &fakeBackupHookRunner{}
	hooks := &system.BackupHooks{Root: root, Runner: runner, StateFile: filepath.Join(t.TempDir(), "backup-addons.json"), UID: -1, GID: -1}

	store, err := auth.Open(t.TempDir(), auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup("admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("bob", "bobsecret1", auth.RoleUser, false); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc := scriptBox{system.AddonScripts{Root: root}}
	a := &SystemAPI{Root: root, Services: svc, Addons: svc, Manager: svc, Nav: svc, BackupHooks: hooks}
	a.Register(mux)
	(&AuthAPI{Store: store}).Register(mux)
	srv := httptest.NewServer((&AuthAPI{Store: store}).Middleware(mux))
	t.Cleanup(srv.Close)
	admin := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)}
	user := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"bob","password":"bobsecret1"}`)}

	st, out, raw := do(t, srv, "GET", "/api/system/v1/backup/addons", "", user)
	if st != 200 || out["supported"] != true || out["large_mb"] != float64(100) {
		t.Fatalf("GET: %d %s", st, raw)
	}
	list := out["addons"].([]any)
	if len(list) != 2 {
		t.Fatalf("addons: %s", raw)
	}
	battery, history := list[0].(map[string]any), list[1].(map[string]any)
	if battery["id"] != "battery" || battery["enabled"] != true || battery["large"] != false || battery["timeout"] != float64(120) || battery["name"].(map[string]any)["de"] != "Batterien" {
		t.Errorf("battery: %v", battery)
	}
	if history["id"] != "history" || history["enabled"] != false || history["large"] != true {
		t.Errorf("history: %v", history)
	}

	// the switch needs system:write
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/backup/addons/history", `{"enabled":true}`, user); st != http.StatusForbidden {
		t.Errorf("a user switched: %d", st)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/backup/addons/history", `{}`, admin); st != http.StatusUnprocessableEntity {
		t.Errorf("no enabled: %d", st)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/backup/addons/mosquitto", `{"enabled":true}`, admin); st != http.StatusNotFound || out["error"] != "not_found" {
		t.Errorf("an addon without the block: %d %v", st, out)
	}
	st, out, raw = do(t, srv, "PUT", "/api/system/v1/backup/addons/history", `{"enabled":true}`, admin)
	if st != 200 {
		t.Fatalf("PUT: %d %s", st, raw)
	}
	if h := out["addons"].([]any)[1].(map[string]any); h["enabled"] != true || h["chosen"] != true {
		t.Errorf("history on: %v", h)
	}

	// a run: both hooks, history's fails; the results and the warning
	ran := hooks.Run(context.Background())
	hooks.Clean(context.Background(), ran)
	if strings.Join(runner.calls, ",") != "battery:snapshot,history:snapshot,battery:clean,history:clean" {
		t.Fatalf("calls: %v", runner.calls)
	}
	st, out, raw = do(t, srv, "GET", "/api/system/v1/backup/addons", "", admin)
	if st != 200 {
		t.Fatalf("GET after the run: %d %s", st, raw)
	}
	list = out["addons"].([]any)
	if last := list[0].(map[string]any)["last"].(map[string]any); last["ok"] != true || last["bytes"] != float64(2048) {
		t.Errorf("battery's last: %v", last)
	}
	if last := list[1].(map[string]any)["last"].(map[string]any); last["ok"] != false || last["exit"] != float64(2) || !strings.Contains(last["error"].(string), "exited 2") || last["output"] != "no stick" {
		t.Errorf("history's last: %v", last)
	}
	ws, _ := a.backupAddonWarning(context.Background())
	if len(ws) != 1 || ws[0].ID != "backup-addon" || ws[0].Variant != "history" || ws[0].Href != "/backup#addons" {
		t.Fatalf("warning: %+v", ws)
	}
	// switched off, the failure is no warning
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/backup/addons/history", `{"enabled":false}`, admin); st != 200 {
		t.Fatal("off")
	}
	if ws, _ := a.backupAddonWarning(context.Background()); len(ws) != 0 {
		t.Fatalf("warning while off: %+v", ws)
	}

	// without hooks: unsupported, an empty list
	none := &SystemAPI{Root: root, Services: svc, Addons: svc, Manager: svc, Nav: svc}
	mux2 := http.NewServeMux()
	none.Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	if st, out, _ := do(t, srv2, "GET", "/api/system/v1/backup/addons", "", nil); st != 200 || out["supported"] != false || len(out["addons"].([]any)) != 0 {
		t.Errorf("without hooks: %d %v", st, out)
	}
	if st, _, _ := do(t, srv2, "PUT", "/api/system/v1/backup/addons/battery", `{"enabled":true}`, nil); st != http.StatusNotImplemented {
		t.Errorf("PUT without hooks: %d", st)
	}
}
