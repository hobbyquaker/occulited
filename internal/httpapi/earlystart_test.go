package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/system"
)

// Task 119: the early start's switches - GET/PUT /early-start, PUT /addons/{id}/early-start, and
// start_early in the addon's policy and in GET /addons - and the .start files they keep.

// memEarly is EarlyStarts in memory.
type memEarly struct {
	on     bool
	off    []string
	delays map[string]int // task 369
}

func (m *memEarly) EarlyStart() (bool, []string) { return m.on, m.off }
func (m *memEarly) SetEarlyStart(on bool, off []string) error {
	m.on, m.off = on, off
	return nil
}
func (m *memEarly) fn(id string) bool { return m.on && !slices.Contains(m.off, id) }

// StartDelays and SetStartDelay make memEarly the start delays too (openccu-lite task 369).
func (m *memEarly) StartDelays() map[string]int { return m.delays }
func (m *memEarly) SetStartDelay(id string, seconds int) error {
	if m.delays == nil {
		m.delays = map[string]int{}
	}
	if seconds == 0 {
		delete(m.delays, id)
	} else {
		m.delays[id] = seconds
	}
	return nil
}
func (m *memEarly) delay(id string) int { return m.delays[id] }

// earlyBox is a systemd box with hmm (its manifest declares start: early) and mosquitto (does not),
// an admin and a user logged in.
func earlyBox(t *testing.T) (*httptest.Server, system.Root, *memEarly, map[string]string, map[string]string) {
	t.Helper()
	root := fakeRoot(t)
	w := func(p, c string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(string(root), p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), mode); err != nil {
			t.Fatal(err)
		}
	}
	w("usr/local/etc/config/rc.d/hmm", "#!/bin/sh\ncase $1 in info) printf 'Name: Homematic Manager\\nVersion: 3.0.0-beta.18\\n';; esac\n", 0o755)
	w("usr/local/etc/config/addon-policy/hmm.json", `{"id":"hmm","mode":"confined","source":"catalog"}`, 0o644)
	w("usr/local/etc/config/addon-policy/mosquitto.json", `{"id":"mosquitto","mode":"confined","source":"catalog","runtime":{"needs":[]}}`, 0o644)
	run := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	w("usr/local/etc/config/addon-policy/hmm.manifest.json", `{"format":1,"id":"hmm","name":"Homematic Manager","runtime":{"start":"early","needs":["rfd","hmipserver"]}}`, 0o644)
	sa := system.NewSystemdAddons(root, system.SystemdServices{Root: root, Run: run})
	sw := &memEarly{on: true}
	sa.EarlyStart = sw.fn
	sa.StartDelay = sw.delay
	sa.RefreshAddonStart()

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
	a := &SystemAPI{Root: root, Services: scriptBox{system.AddonScripts{Root: root}}, Addons: sa, Manager: sa, Nav: scriptBox{system.AddonScripts{Root: root}}, Early: sw, Delays: sw}
	a.Register(mux)
	(&AuthAPI{Store: store}).Register(mux)
	srv := httptest.NewServer((&AuthAPI{Store: store}).Middleware(mux))
	t.Cleanup(srv.Close)
	admin := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)}
	user := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"bob","password":"bobsecret1"}`)}
	return srv, root, sw, admin, user
}

func startFileOf(root system.Root, id string) bool {
	_, err := os.Stat(filepath.Join(string(root), "usr/local/etc/config/addon-policy", id+".start"))
	return err == nil
}

func TestEarlyStartSwitches(t *testing.T) {
	srv, root, sw, admin, user := earlyBox(t)
	if !startFileOf(root, "hmm") || startFileOf(root, "mosquitto") {
		t.Fatal("the start's refresh: hmm.start only")
	}

	// the global switch: readable by any session, on by default, with the next-boot note
	st, out, raw := do(t, srv, "GET", "/api/system/v1/early-start", "", user)
	if st != 200 || out["enabled"] != true || out["next_boot"] != true || len(out["off"].([]any)) != 0 {
		t.Fatalf("GET: %d %s", st, raw)
	}
	// changing it needs addons:write
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/early-start", `{"enabled":false}`, user); st != http.StatusForbidden {
		t.Errorf("a user switched it: %d", st)
	}
	st, out, raw = do(t, srv, "PUT", "/api/system/v1/early-start", `{"enabled":false}`, admin)
	if st != 200 || out["enabled"] != false || sw.on {
		t.Fatalf("off: %d %s", st, raw)
	}
	if startFileOf(root, "hmm") {
		t.Error("globally off, hmm.start stayed")
	}
	if st, out, _ = do(t, srv, "PUT", "/api/system/v1/early-start", `{"enabled":true}`, admin); st != 200 || !startFileOf(root, "hmm") {
		t.Errorf("on again: %d %v", st, out)
	}

	// per addon
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addons/hmm/early-start", `{"enabled":false}`, user); st != http.StatusForbidden {
		t.Errorf("a user switched hmm: %d", st)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addons/hmm/early-start", `{}`, admin); st != http.StatusUnprocessableEntity {
		t.Errorf("no enabled: %d", st)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addons/..%2Fx/early-start", `{"enabled":false}`, admin); st != http.StatusUnprocessableEntity && st != http.StatusNotFound {
		t.Errorf("a bad id: %d", st)
	}
	st, out, raw = do(t, srv, "PUT", "/api/system/v1/addons/hmm/early-start", `{"enabled":false}`, admin)
	if st != 200 || out["addon_enabled"] != false || out["start_early_declared"] != true || out["start_early"] != false || out["enabled"] != true {
		t.Fatalf("hmm off: %d %s", st, raw)
	}
	if startFileOf(root, "hmm") || !slices.Equal(sw.off, []string{"hmm"}) {
		t.Errorf("hmm off: file %v, off %v", startFileOf(root, "hmm"), sw.off)
	}

	// the policy and the addon list say the same
	st, out, raw = do(t, srv, "GET", "/api/system/v1/addons/hmm/policy", "", user)
	if st != 200 || out["start_early_declared"] != true || out["start_early"] != false {
		t.Errorf("policy: %d %s", st, raw)
	}
	listed := func() map[string]map[string]any {
		t.Helper()
		_, out, _ := do(t, srv, "GET", "/api/system/v1/addons", "", user)
		m := map[string]map[string]any{}
		for _, x := range out["addons"].([]any) {
			e := x.(map[string]any)
			m[e["id"].(string)] = e
		}
		return m
	}
	if l := listed(); l["hmm"]["start_early_declared"] != true || l["hmm"]["start_early"] != nil || l["mosquitto"]["start_early_declared"] != nil {
		t.Errorf("list: %v / %v", l["hmm"], l["mosquitto"])
	}

	// the policy's own field switches it back, restarts nothing and leaves the mode alone
	st, out, raw = do(t, srv, "PUT", "/api/system/v1/addons/hmm/policy", `{"start_early":true}`, admin)
	if st != 200 || out["start_early"] != true || out["restarted"] != false || out["next_boot"] != true {
		t.Fatalf("policy put: %d %s", st, raw)
	}
	if !startFileOf(root, "hmm") || len(sw.off) != 0 {
		t.Errorf("hmm on: file %v, off %v", startFileOf(root, "hmm"), sw.off)
	}
	if p := root.ReadAddonPolicy("hmm"); p == nil || p.Mode != "confined" {
		t.Errorf("the switch changed the policy: %+v", p)
	}
	if l := listed(); l["hmm"]["start_early"] != true {
		t.Errorf("list after on: %v", l["hmm"])
	}
	// the whole list in one PUT
	st, out, raw = do(t, srv, "PUT", "/api/system/v1/early-start", `{"off":["mosquitto","hmm","bad/id","hmm"]}`, admin)
	if st != 200 {
		t.Fatalf("off list: %d %s", st, raw)
	}
	if b, _ := json.Marshal(out["off"]); string(b) != `["hmm","mosquitto"]` || startFileOf(root, "hmm") {
		t.Errorf("off list: %s, file %v", b, startFileOf(root, "hmm"))
	}
}

// openccu-lite task 369: PUT /addons/{id}/start-delay keeps the delay, writes <id>.delay, and the
// early-start view and the addon list show it; 0 removes it, out of range is refused.
func TestStartDelays(t *testing.T) {
	srv, root, sw, admin, user := earlyBox(t)
	delayFile := func(id string) string {
		b, err := os.ReadFile(filepath.Join(string(root), "usr/local/etc/config/addon-policy", id+".delay"))
		if err != nil {
			return ""
		}
		return string(b)
	}
	if delayFile("hmm") != "" {
		t.Fatal("a delay file before any delay")
	}
	st, out, raw := do(t, srv, "GET", "/api/system/v1/early-start", "", user)
	if st != 200 || out["max_delay"] != float64(600) || len(out["delays"].(map[string]any)) != 0 {
		t.Fatalf("GET: %d %s", st, raw)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addons/hmm/start-delay", `{"seconds":30}`, user); st != http.StatusForbidden {
		t.Errorf("a user set a delay: %d", st)
	}
	for _, body := range []string{`{}`, `{"seconds":-1}`, `{"seconds":601}`, `{"seconds":"30"}`} {
		if st, _, raw := do(t, srv, "PUT", "/api/system/v1/addons/hmm/start-delay", body, admin); st != http.StatusUnprocessableEntity && st != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, st, raw)
		}
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/addons/..%2Fx/start-delay", `{"seconds":30}`, admin); st != http.StatusUnprocessableEntity && st != http.StatusNotFound {
		t.Errorf("a bad id: %d", st)
	}
	st, out, raw = do(t, srv, "PUT", "/api/system/v1/addons/hmm/start-delay", `{"seconds":30}`, admin)
	if st != 200 || out["delays"].(map[string]any)["hmm"] != float64(30) || out["next_boot"] != true {
		t.Fatalf("hmm 30: %d %s", st, raw)
	}
	if delayFile("hmm") != "30\n" || sw.delays["hmm"] != 30 {
		t.Errorf("hmm 30: file %q, kept %v", delayFile("hmm"), sw.delays)
	}
	// the early start is a separate switch: hmm.start stays
	if !startFileOf(root, "hmm") {
		t.Error("the delay removed hmm.start")
	}
	_, out, _ = do(t, srv, "GET", "/api/system/v1/addons", "", user)
	found := false
	for _, x := range out["addons"].([]any) {
		e := x.(map[string]any)
		if e["id"] == "hmm" {
			found = e["start_delay"] == float64(30)
		} else if e["start_delay"] != nil {
			t.Errorf("%v has a delay", e["id"])
		}
	}
	if !found {
		t.Errorf("the list does not show hmm's delay: %v", out["addons"])
	}
	if st, _, _ = do(t, srv, "PUT", "/api/system/v1/addons/hmm/start-delay", `{"seconds":600}`, admin); st != 200 || delayFile("hmm") != "600\n" {
		t.Errorf("600: %d %q", st, delayFile("hmm"))
	}
	st, out, raw = do(t, srv, "PUT", "/api/system/v1/addons/hmm/start-delay", `{"seconds":0}`, admin)
	if st != 200 || len(out["delays"].(map[string]any)) != 0 || delayFile("hmm") != "" || len(sw.delays) != 0 {
		t.Errorf("0: %d %s, file %q", st, raw, delayFile("hmm"))
	}
}

// a daemon without a configuration file has no switches: the routes say so
func TestEarlyStartWithoutConfig(t *testing.T) {
	r := fakeRoot(t)
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: scriptBox{system.AddonScripts{Root: r}}, Addons: scriptBox{system.AddonScripts{Root: r}}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/system/v1/early-start", ""},
		{"PUT", "/api/system/v1/early-start", `{"enabled":false}`},
		{"PUT", "/api/system/v1/addons/hmm/early-start", `{"enabled":false}`},
		{"PUT", "/api/system/v1/addons/hmm/start-delay", `{"seconds":30}`},
	} {
		if st, _, raw := do(t, srv, c.method, c.path, c.body, nil); st != http.StatusNotImplemented {
			t.Errorf("%s %s: %d %s", c.method, c.path, st, raw)
		}
	}
}
