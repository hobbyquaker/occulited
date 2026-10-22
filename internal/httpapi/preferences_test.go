package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// The caller's shell preferences: any role reads and writes its own, and only its own.
func TestPreferencesRoutes(t *testing.T) {
	srv := authServer(t)
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	admin := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/users", `{"username":"bob","password":"secret123","role":"user"}`, admin); st != 201 {
		t.Fatalf("create bob: %d", st)
	}
	bob := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"bob","password":"secret123"}`)}

	// nobody: 401
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/me/preferences", "", nil); st != 401 {
		t.Fatalf("no session: %d", st)
	}
	// a fresh account: the empty list, as a list
	if st, _, raw := do(t, srv, "GET", "/api/auth/v1/me/preferences", "", bob); st != 200 || raw != "{\"addons\":[]}\n" {
		t.Fatalf("fresh: %d %q", st, raw)
	}
	// a plain user writes their own: PUT is self-service, not an administrator's mutation
	body := `{"addons":[{"id":"redmatic","pinned":true},{"id":"mh"}]}`
	if st, out, raw := do(t, srv, "PUT", "/api/auth/v1/me/preferences", body, bob); st != 200 || out["addons"] == nil || raw != body+"\n" {
		t.Fatalf("put: %d %s", st, raw)
	}
	if _, _, raw := do(t, srv, "GET", "/api/auth/v1/me/preferences", "", bob); raw != body+"\n" {
		t.Fatalf("get after put: %s", raw)
	}
	// task 306: Control's tab hidden, with the App's other choices; occulited task 101: the full
	// names in Control; read back as written
	app := `{"addons":[],"start_page":"app","app_fullscreen":true,"app_hidden":true,"app_full_names":true}`
	if st, _, raw := do(t, srv, "PUT", "/api/auth/v1/me/preferences", app, bob); st != 200 || raw != app+"\n" {
		t.Fatalf("put app choices: %d %s", st, raw)
	}
	if _, _, raw := do(t, srv, "GET", "/api/auth/v1/me/preferences", "", bob); raw != app+"\n" {
		t.Fatalf("get app choices: %s", raw)
	}
	// occulited task 24: an addon shown as the whole window is a flag on its entry; read back as
	// written, beside the pin, and left out when it is off
	full := `{"addons":[{"id":"redmatic","pinned":true,"fullscreen":true},{"id":"mh","fullscreen":true}]}`
	if st, _, raw := do(t, srv, "PUT", "/api/auth/v1/me/preferences", full, bob); st != 200 || raw != full+"\n" {
		t.Fatalf("put fullscreen: %d %s", st, raw)
	}
	if _, _, raw := do(t, srv, "GET", "/api/auth/v1/me/preferences", "", bob); raw != full+"\n" {
		t.Fatalf("get fullscreen: %s", raw)
	}
	if st, _, raw := do(t, srv, "PUT", "/api/auth/v1/me/preferences", body, bob); st != 200 || raw != body+"\n" {
		t.Fatalf("put back: %d %s", st, raw)
	}
	// the administrator's own set is another one
	if _, _, raw := do(t, srv, "GET", "/api/auth/v1/me/preferences", "", admin); raw != "{\"addons\":[]}\n" {
		t.Fatalf("admin's set: %s", raw)
	}
	// refused bodies: an unknown field, a bad id, a duplicate
	for _, bad := range []string{`{"addons":[{"id":"x y"}]}`, `{"addons":[{"id":"mh"},{"id":"mh"}]}`, `{"addons":[],"theme":"dark"}`, `[]`} {
		if st, out, _ := do(t, srv, "PUT", "/api/auth/v1/me/preferences", bad, bob); st != 422 || out["error"] != "invalid-body" {
			t.Fatalf("%s: %d %v", bad, st, out)
		}
	}
	// the account list does not carry them
	if _, out, raw := do(t, srv, "GET", "/api/auth/v1/users", "", admin); out["users"] == nil || strings.Contains(raw, "preferences") {
		t.Fatalf("users list: %s", raw)
	}
}

// Auth mode off: the anonymous administrator has no account, so there is nothing to keep the
// preferences with - GET answers the empty set, PUT says so.
func TestPreferencesAuthOff(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store, Off: true}
	a.Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	if st, _, raw := do(t, srv, "GET", "/api/auth/v1/me/preferences", "", nil); st != 200 || raw != "{\"addons\":[]}\n" {
		t.Fatalf("anonymous get: %d %q", st, raw)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/auth/v1/me/preferences", `{"addons":[{"id":"mh","pinned":true}]}`, nil); st != 409 || out["error"] != "no-account" {
		t.Fatalf("anonymous put: %d %v", st, out)
	}
}
