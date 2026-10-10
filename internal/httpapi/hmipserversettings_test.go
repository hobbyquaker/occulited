package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// task 33: hmipserver.diagrams through the system API - read, switched and persisted by the
// callback, a body without the key refused, 501 without the callbacks.
func TestHmIPServerSettings(t *testing.T) {
	on := false
	mux := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t), HmIPServerDiagrams: func() bool { return on }, OnHmIPServerDiagrams: func(v bool) error { on = v; return nil }}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if st, out, _ := do(t, srv, "GET", "/api/system/v1/hmipserver/settings", "", nil); st != 200 || out["diagrams"] != false {
		t.Fatalf("get: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/system/v1/hmipserver/settings", `{"diagrams":true}`, nil); st != 200 || out["diagrams"] != true || !on || out["applies"] != "next-start" {
		t.Fatalf("put: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/hmipserver/settings", `{}`, nil); st != 400 {
		t.Fatalf("empty body: %d", st)
	}
	none := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t)}).Register(none)
	s2 := httptest.NewServer(none)
	t.Cleanup(s2.Close)
	if st, _, _ := do(t, s2, "GET", "/api/system/v1/hmipserver/settings", "", nil); st != 501 {
		t.Fatalf("no callbacks: %d", st)
	}
}
