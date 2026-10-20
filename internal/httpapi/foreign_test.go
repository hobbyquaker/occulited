package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hobbyquaker/occulited/internal/meta"
)

// occulited B-84 (the maintainer, 2026-10-20: "never automatic"): GET /foreign lists per interface
// what POST /foreign:remove would take; the removal takes exactly the named interfaces, and refuses
// one that is listed now, one with nothing to remove and a missing interface list.
func TestForeignObjectsAPI(t *testing.T) {
	doc := meta.NewDocument()
	for _, ref := range []string{"CUxD.CUX2801001", "CUxD.CUX2801001:1", "CUxD.CUX2801001:2", "HmIP-RF.0000000000AAAA:1", "BidCos-RF.ABC0000001:1"} {
		doc.Objects[ref] = &meta.Object{Name: ref, Enums: []string{}}
	}
	s, err := meta.New(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := []string{"BidCos-RF"}
	mux := http.NewServeMux()
	(&MetaAPI{Store: s, Interfaces: func() []string { return listed }}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, out, raw := do(t, srv, "GET", "/api/meta/v1/foreign", "", nil)
	if code != 200 || out["list_read"] != true {
		t.Fatalf("GET: %d %s", code, raw)
	}
	ifs, _ := out["interfaces"].([]any)
	if len(ifs) != 2 {
		t.Fatalf("interfaces: %s", raw)
	}
	cux := ifs[0].(map[string]any)
	if cux["interface"] != "CUxD" || cux["objects"] != 3.0 || cux["devices"] != 1.0 || cux["channels"] != 2.0 {
		t.Errorf("CUxD: %v", cux)
	}
	if ifs[1].(map[string]any)["interface"] != "HmIP-RF" {
		t.Errorf("order: %s", raw)
	}
	rev := s.Revision()

	// refused: nothing named, a listed interface, an unknown one - and nothing changed
	for _, body := range []string{`{"interfaces":[]}`, `{}`} {
		if code, _, raw := do(t, srv, "POST", "/api/meta/v1/foreign:remove", body, nil); code != 422 {
			t.Errorf("%s: %d %s", body, code, raw)
		}
	}
	for _, body := range []string{`{"interfaces":["BidCos-RF"]}`, `{"interfaces":["CUxD","VirtualDevices"]}`} {
		if code, out, raw := do(t, srv, "POST", "/api/meta/v1/foreign:remove", body, nil); code != 409 || out["error"] != "not-foreign" {
			t.Errorf("%s: %d %s", body, code, raw)
		}
	}
	if s.Revision() != rev {
		t.Fatalf("a refused removal changed the store")
	}
	// a stale If-Match is refused
	if code, _, raw := do(t, srv, "POST", "/api/meta/v1/foreign:remove", `{"interfaces":["CUxD"]}`, map[string]string{"If-Match": "999"}); code != 409 {
		t.Errorf("stale If-Match: %d %s", code, raw)
	}

	// CUxD goes, in one revision; HmIP-RF and the listed interface stay
	code, out, raw = do(t, srv, "POST", "/api/meta/v1/foreign:remove", `{"interfaces":["CUxD","CUxD"]}`, nil)
	if code != 200 || out["removed"] != 3.0 || out["changed"] != true {
		t.Fatalf("remove: %d %s", code, raw)
	}
	if s.Revision() != rev+1 {
		t.Errorf("revision %d, want %d", s.Revision(), rev+1)
	}
	snap := s.Snapshot()
	if len(snap.Objects) != 2 || snap.Objects["HmIP-RF.0000000000AAAA:1"] == nil {
		t.Errorf("left: %v", snap.Objects)
	}

	// HmIP-RF's module came back: its names are no longer offered, and a removal is refused
	listed = []string{"BidCos-RF", "HmIP-RF"}
	if code, out, raw := do(t, srv, "GET", "/api/meta/v1/foreign", "", nil); code != 200 || len(out["interfaces"].([]any)) != 0 {
		t.Errorf("after the module came back: %d %s", code, raw)
	}
	if code, _, raw := do(t, srv, "POST", "/api/meta/v1/foreign:remove", `{"interfaces":["HmIP-RF"]}`, nil); code != 409 {
		t.Errorf("listed again: %d %s", code, raw)
	}

	// no interface list read: nothing offered, nothing removed
	listed = nil
	if code, out, raw := do(t, srv, "GET", "/api/meta/v1/foreign", "", nil); code != 200 || out["list_read"] != false || len(out["interfaces"].([]any)) != 0 {
		t.Errorf("no list: %d %s", code, raw)
	}
	if code, out, raw := do(t, srv, "POST", "/api/meta/v1/foreign:remove", `{"interfaces":["HmIP-RF"]}`, nil); code != 409 || out["error"] != "no-interface-list" {
		t.Errorf("no list remove: %d %s", code, raw)
	}
	// a server without the hook behaves as with no list
	mux2 := http.NewServeMux()
	(&MetaAPI{Store: s}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	if code, out, raw := do(t, srv2, "GET", "/api/meta/v1/foreign", "", nil); code != 200 || out["list_read"] != false {
		t.Errorf("no hook: %d %s", code, raw)
	}
}
