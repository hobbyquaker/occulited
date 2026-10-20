package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// occulited task 73: any role keeps display names of its own, and only its own; the metadata is
// not touched.
func TestDisplayNamesRoutes(t *testing.T) {
	srv := authServer(t)
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	admin := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/users", `{"username":"bob","password":"secret123","role":"user"}`, admin); st != 201 {
		t.Fatalf("create bob: %d", st)
	}
	bob := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"bob","password":"secret123"}`)}
	const base = "/api/auth/v1/me/display-names"
	const ch = "/HmIP-RF.0000000000AAAA%3A3"

	if st, _, _ := do(t, srv, "GET", base, "", nil); st != 401 {
		t.Fatalf("no session: %d", st)
	}
	if st, _, raw := do(t, srv, "GET", base, "", bob); st != 200 || raw != "{\"display_names\":{}}\n" {
		t.Fatalf("fresh: %d %q", st, raw)
	}
	if st, _, raw := do(t, srv, "PUT", base+ch, `{"name":"Leselampe"}`, bob); st != 200 || raw != "{\"display_names\":{\"HmIP-RF.0000000000AAAA:3\":\"Leselampe\"}}\n" {
		t.Fatalf("put: %d %q", st, raw)
	}
	if st, _, _ := do(t, srv, "PUT", base+ch, `{"name":"Lampe links"}`, admin); st != 200 {
		t.Fatalf("admin's put: %d", st)
	}
	// each sees their own
	if _, _, raw := do(t, srv, "GET", base, "", bob); !strings.Contains(raw, "Leselampe") || strings.Contains(raw, "links") {
		t.Fatalf("bob's: %s", raw)
	}
	if _, _, raw := do(t, srv, "GET", base, "", admin); !strings.Contains(raw, "Lampe links") {
		t.Fatalf("admin's: %s", raw)
	}
	// refused bodies and keys
	for _, bad := range [][2]string{{base + ch, `{"name":"a","x":1}`}, {base + ch, `{"name":"a\u0007"}`}, {base + "/a%20b", `{"name":"a"}`}} {
		if st, out, _ := do(t, srv, "PUT", bad[0], bad[1], bob); st != 422 || out["error"] != "invalid-body" {
			t.Fatalf("%v: %d %v", bad, st, out)
		}
	}
	// an empty name clears, as DELETE does
	if st, _, raw := do(t, srv, "PUT", base+ch, `{"name":""}`, bob); st != 200 || raw != "{\"display_names\":{}}\n" {
		t.Fatalf("clear: %d %q", st, raw)
	}
	if st, _, raw := do(t, srv, "DELETE", base+ch, "", admin); st != 200 || raw != "{\"display_names\":{}}\n" {
		t.Fatalf("delete: %d %q", st, raw)
	}
	// the account list does not carry them
	if _, _, raw := do(t, srv, "GET", "/api/auth/v1/users", "", admin); strings.Contains(raw, "display_names") {
		t.Fatalf("users list: %s", raw)
	}
}

// Auth mode off: no account to keep them with - GET answers the empty map, a write says so.
func TestDisplayNamesAuthOff(t *testing.T) {
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
	if st, _, raw := do(t, srv, "GET", "/api/auth/v1/me/display-names", "", nil); st != 200 || raw != "{\"display_names\":{}}\n" {
		t.Fatalf("anonymous get: %d %q", st, raw)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/auth/v1/me/display-names/x", `{"name":"y"}`, nil); st != 409 || out["error"] != "no-account" {
		t.Fatalf("anonymous put: %d %v", st, out)
	}
}
