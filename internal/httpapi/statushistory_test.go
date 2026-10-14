package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hobbyquaker/occulited/internal/syshist"
)

// occulited task 37: the system's load and memory history, 501 without a sampler.
func TestStatusHistoryRoute(t *testing.T) {
	mux := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t)}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if st, _, _ := do(t, srv, "GET", "/api/system/v1/status/history", "", nil); st != 501 {
		t.Fatalf("without a sampler: %d", st)
	}
	s := &syshist.Sampler{Read: func() (syshist.Reading, bool) { return syshist.Reading{Load: 1.25, Mem: 37}, true }}
	s.Poll()
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t), SysHist: s}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	st, out, _ := do(t, srv2, "GET", "/api/system/v1/status/history", "", nil)
	samples, _ := out["samples"].([]any)
	if st != 200 || out["interval_s"] != 60.0 || len(samples) != 1 {
		t.Fatalf("%d %v", st, out)
	}
	if m := samples[0].(map[string]any); m["load"] != 1.25 || m["mem"] != 37.0 || m["t"] == "" {
		t.Fatalf("sample %v", m)
	}
}
