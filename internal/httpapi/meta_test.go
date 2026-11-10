package httpapi

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/meta"
	"github.com/hobbyquaker/occulited/internal/radio"
)

// zeroReader yields an endless stream of NUL bytes, for a large upload body a test streams rather
// than allocates.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// B-255: the regadom import stages an upload on the userfs with a cap, refuses one over the cap
// with 413, and leaves no staged file behind on any path.
func TestImportRegadomStaging(t *testing.T) {
	root := t.TempDir()
	s, err := meta.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&MetaAPI{Store: s, Root: root}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	staging := filepath.Join(root, "usr/local/etc/occulite/staging")

	post := func(filename string, size int64) int {
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			fw, _ := mw.CreateFormFile("file", filename)
			_, _ = io.CopyN(fw, zeroReader{}, size)
			_ = mw.Close()
			_ = pw.Close()
		}()
		req, _ := http.NewRequest("POST", srv.URL+"/api/meta/v1/import/regadom?dry_run=true", pr)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	stagedFiles := func() int {
		e, _ := os.ReadDir(staging)
		return len(e)
	}

	// over the 64 MiB regadom cap: 413, nothing left staged
	if code := post("big.regadom", 65<<20); code != http.StatusRequestEntityTooLarge {
		t.Errorf("65 MiB upload: got %d, want 413", code)
	}
	if n := stagedFiles(); n != 0 {
		t.Errorf("a staged file was left after the refused upload: %d", n)
	}
	// a small body that is not a regadom: 422 from the parser, still nothing left staged
	if code := post("small.regadom", 1024); code != http.StatusUnprocessableEntity {
		t.Errorf("small non-regadom upload: got %d, want 422", code)
	}
	if n := stagedFiles(); n != 0 {
		t.Errorf("a staged file was left after the parse failure: %d", n)
	}
}

func newServer(t *testing.T) (*httptest.Server, *meta.Store) {
	t.Helper()
	s, err := meta.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&MetaAPI{Store: s}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, s
}

// shellHeader adds what the shell sends with every call (task 259): the header credential, when
// the request rides on a cookie without an Authorization header. A test that wants the header left
// out names RequestHeader in hdr with an empty value; one that sets it keeps its own.
func shellHeader(req *http.Request, hdr map[string]string) {
	if _, named := hdr[RequestHeader]; named {
		return
	}
	if req.Header.Get("Cookie") != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set(RequestHeader, "1")
	}
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) (int, map[string]any, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	shellHeader(req, hdr)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var raw strings.Builder
	buf := make([]byte, 64*1024)
	for {
		n, err := res.Body.Read(buf)
		raw.Write(buf[:n])
		if err != nil {
			break
		}
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(raw.String()), &out)
	return res.StatusCode, out, raw.String()
}

func TestVersionAndSnapshot(t *testing.T) {
	srv, _ := newServer(t)
	st, out, _ := do(t, srv, "GET", "/api/meta/v1/version", "", nil)
	if st != 200 || out["api"] != "meta" || out["format"] != float64(1) {
		t.Fatalf("version: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/meta/v1/snapshot", "", nil)
	if st != 200 || out["revision"] != float64(0) || len(out["enums"].(map[string]any)) != 2 {
		t.Fatalf("snapshot: %d %v", st, out)
	}
}

// /version names the implementation with occulited's version - the commit (occulited task 16) - and
// the bare hash beside it; a build without a commit leaves the field out
func TestVersionImplementationAndCommit(t *testing.T) {
	impl, commit := Implementation, Commit
	t.Cleanup(func() { Implementation, Commit = impl, commit })
	for _, c := range []struct{ impl, commit string }{
		{"occulited fa42dfde1e31fb074df53220dd573ceb92642ff0", "fa42dfde1e31fb074df53220dd573ceb92642ff0"},
		{"occulited fa42dfde1e31fb074df53220dd573ceb92642ff0-dirty", "fa42dfde1e31fb074df53220dd573ceb92642ff0"},
		{"occulited dev", ""},
	} {
		Implementation, Commit = c.impl, c.commit
		srv, _ := newServer(t)
		st, out, _ := do(t, srv, "GET", "/api/meta/v1/version", "", nil)
		got, present := out["commit"]
		if st != 200 || out["implementation"] != c.impl || (c.commit == "" && present) || (c.commit != "" && got != c.commit) {
			t.Fatalf("%q/%q: status %d, %v", c.impl, c.commit, st, out)
		}
	}
}

// task 192: /version carries the pairing fact when the daemon has one, and nothing when it has
// not - an older system's answer has no `hmip` at all, which is how a client tells them apart
func TestVersionHmIPPairing(t *testing.T) {
	srv, _ := newServer(t)
	if _, out, _ := do(t, srv, "GET", "/api/meta/v1/version", "", nil); out["hmip"] != nil {
		t.Fatalf("no fact wired, yet answered: %v", out)
	}
	s, _ := meta.New(nil, nil)
	mux := http.NewServeMux()
	fact := radio.Pairing{KeyServerMode: radio.KeyServerLocal, DeviceKeys: 3}
	(&MetaAPI{Store: s, HmIPPairing: func() radio.Pairing { return fact }}).Register(mux)
	with := httptest.NewServer(mux)
	t.Cleanup(with.Close)
	st, out, raw := do(t, with, "GET", "/api/meta/v1/version", "", nil)
	hmip, _ := out["hmip"].(map[string]any)
	if st != 200 || out["api"] != "meta" || hmip["keyserver_mode"] != "LOCAL" || hmip["device_keys"] != float64(3) || hmip["offline_pairing"] != false {
		t.Fatalf("version with the fact: %d %s", st, raw)
	}
	// the count is a count and the mode a word: nothing else leaves the system this way
	if len(hmip) != 3 || strings.Contains(raw, "sgtin") {
		t.Fatalf("more than the three fields: %s", raw)
	}
	fact = radio.Pairing{KeyServerMode: radio.KeyServerLocalFallback, OfflinePairing: true}
	_, out, raw = do(t, with, "GET", "/api/meta/v1/version", "", nil)
	if hmip, _ := out["hmip"].(map[string]any); hmip["keyserver_mode"] != "KEYSERVER_LOCAL" || hmip["device_keys"] != float64(0) || hmip["offline_pairing"] != true {
		t.Fatalf("read live, not once: %s", raw)
	}
}

// the fact goes with /version's openness: no credential, a user's session and a read token all
// get the same answer, and none of them needs a system or radio scope (D-104 keeps the keys
// themselves behind radio:keys)
func TestVersionHmIPPairingOpen(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := meta.New(nil, nil)
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store}
	a.Register(mux)
	(&MetaAPI{Store: ms, HmIPPairing: func() radio.Pairing { return radio.Pairing{KeyServerMode: radio.KeyServerLocal, DeviceKeys: 1} }}).Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	admin := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/users", `{"username":"bob","password":"bobsecret1","role":"user"}`, admin); st != 201 {
		t.Fatalf("create user: %d", st)
	}
	_, tok, _ := do(t, srv, "POST", "/api/auth/v1/tokens", `{"name":"pairing","role":"user"}`, admin)
	for name, hdr := range map[string]map[string]string{
		"open":    nil,
		"user":    {"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"bob","password":"bobsecret1"}`)},
		"token":   {"Authorization": "Bearer " + tok["token"].(string)},
		"a token": {"Authorization": "Bearer olt_" + strings.Repeat("0", 32)},
	} {
		st, out, raw := do(t, srv, "GET", "/api/meta/v1/version", "", hdr)
		hmip, _ := out["hmip"].(map[string]any)
		if st != 200 || hmip["keyserver_mode"] != "LOCAL" || hmip["device_keys"] != float64(1) || hmip["offline_pairing"] != false {
			t.Fatalf("%s: %d %s", name, st, raw)
		}
	}
}

// occulited task 137: /version names the interfaces lite-rpc offers, read live, as a list of names
// and nothing else - for a client that looks for this system before it holds a credential. No
// source wired: no `interfaces` at all (an older system); a source with none: an empty list.
func TestVersionInterfaces(t *testing.T) {
	srv, _ := newServer(t)
	if _, out, raw := do(t, srv, "GET", "/api/meta/v1/version", "", nil); out["interfaces"] != nil || strings.Contains(raw, `"interfaces"`) {
		t.Fatalf("no source wired, yet answered: %s", raw)
	}
	s, _ := meta.New(nil, nil)
	mux := http.NewServeMux()
	names := []string{"BidCos-RF", "HmIP-RF", "VirtualDevices"}
	(&MetaAPI{Store: s, RPCInterfaces: func() []string { return names }}).Register(mux)
	with := httptest.NewServer(mux)
	t.Cleanup(with.Close)
	st, out, raw := do(t, with, "GET", "/api/meta/v1/version", "", nil)
	got, _ := out["interfaces"].([]any)
	if st != 200 || len(got) != 3 || got[0] != "BidCos-RF" || got[1] != "HmIP-RF" || got[2] != "VirtualDevices" {
		t.Fatalf("version with the interfaces: %d %s", st, raw)
	}
	names = nil
	_, out, raw = do(t, with, "GET", "/api/meta/v1/version", "", nil)
	if got, ok := out["interfaces"].([]any); !ok || len(got) != 0 {
		t.Fatalf("none offered must be an empty list, read live: %s", raw)
	}
}

// the names go with /version's openness: no credential, a session or a token, the same answer
func TestVersionInterfacesOpen(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := meta.New(nil, nil)
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store}
	a.Register(mux)
	(&MetaAPI{Store: ms, RPCInterfaces: func() []string { return []string{"HmIP-RF"} }}).Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	for name, hdr := range map[string]map[string]string{
		"open":          nil,
		"a stale token": {"Authorization": "Bearer olt_" + strings.Repeat("0", 32)},
	} {
		st, out, raw := do(t, srv, "GET", "/api/meta/v1/version", "", hdr)
		got, _ := out["interfaces"].([]any)
		if st != 200 || len(got) != 1 || got[0] != "HmIP-RF" {
			t.Fatalf("%s: %d %s", name, st, raw)
		}
	}
}

func TestObjectLifecycle(t *testing.T) {
	srv, _ := newServer(t)
	ref := "/api/meta/v1/objects/BidCos-RF.JEQ9000001%3A1"
	st, out, _ := do(t, srv, "PATCH", ref, `{"name":"Heizung Bad"}`, nil)
	if st != 200 || out["revision"] != float64(1) {
		t.Fatalf("patch: %d %v", st, out)
	}
	st, _, _ = do(t, srv, "PATCH", ref, `{"name":"Heizung Bad"}`, nil)
	if st != 304 {
		t.Fatalf("no-op patch should be 304, got %d", st)
	}
	st, out, _ = do(t, srv, "PATCH", ref, `{"orphaned":true}`, nil)
	if st != 403 || out["error"] != "forbidden" {
		t.Fatalf("orphaned from a client must be forbidden: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "PATCH", ref, `{"name":"X"}`, map[string]string{"If-Match": "7"})
	if st != 409 || out["error"] != "revision-conflict" {
		t.Fatalf("if-match: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", ref, "", nil)
	if st != 200 || out["object"].(map[string]any)["name"] != "Heizung Bad" {
		t.Fatalf("get: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "PATCH", ref, `{"enums":["room/nowhere"]}`, nil)
	if st != 422 || out["error"] != "unknown-path" {
		t.Fatalf("unknown path: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "PATCH", ref, `{"bogus":1}`, nil)
	if st != 422 || out["error"] != "invalid-body" {
		t.Fatalf("unknown field: %d %v", st, out)
	}
	st, _, _ = do(t, srv, "DELETE", ref, "", nil)
	if st != 200 {
		t.Fatalf("delete: %d", st)
	}
	st, out, _ = do(t, srv, "GET", ref, "", nil)
	if st != 404 || out["error"] != "unknown-object" {
		t.Fatalf("after delete: %d %v", st, out)
	}
}

func TestTreeAndQuery(t *testing.T) {
	srv, _ := newServer(t)
	st, _, _ := do(t, srv, "POST", "/api/meta/v1/enums/room/nodes", `{"parent":null,"id":"eg","name":"Erdgeschoss"}`, nil)
	if st != 201 {
		t.Fatalf("create root node: %d", st)
	}
	st, _, _ = do(t, srv, "POST", "/api/meta/v1/enums/room/nodes", `{"parent":"room/eg","id":"bad","name":"Bad","icon":"bath"}`, nil)
	if st != 201 {
		t.Fatalf("create child: %d", st)
	}
	st, out, _ := do(t, srv, "POST", "/api/meta/v1/enums/room/nodes", `{"parent":"room/eg","id":"bad","name":"Again"}`, nil)
	if st != 409 || out["error"] != "duplicate-id" {
		t.Fatalf("duplicate: %d %v", st, out)
	}
	do(t, srv, "PATCH", "/api/meta/v1/objects/HmIP-RF.A%3A1", `{"name":"Lampe","enums":["room/eg/bad"]}`, nil)
	st, out, _ = do(t, srv, "GET", "/api/meta/v1/objects?enum=room/eg", "", nil)
	if st != 200 || len(out["objects"].(map[string]any)) != 1 {
		t.Fatalf("subtree query: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "DELETE", "/api/meta/v1/enums/room/nodes/eg", "", nil)
	if st != 409 || out["error"] != "has-members" {
		t.Fatalf("delete with members: %d %v", st, out)
	}
	st, _, _ = do(t, srv, "PATCH", "/api/meta/v1/enums/room/nodes/eg/bad", `{"parent":null}`, nil)
	if st != 200 {
		t.Fatalf("move to root: %d", st)
	}
	st, out, _ = do(t, srv, "GET", "/api/meta/v1/objects/HmIP-RF.A%3A1", "", nil)
	enums := out["object"].(map[string]any)["enums"].([]any)
	if st != 200 || enums[0] != "room/bad" {
		t.Fatalf("path rewritten on move: %d %v", st, enums)
	}
	st, out, _ = do(t, srv, "GET", "/api/meta/v1/enums/room/tree", "", nil)
	if st != 200 || len(out["tree"].([]any)) != 2 {
		t.Fatalf("tree: %d %v", st, out)
	}
}

func TestExportImportYAML(t *testing.T) {
	srv, _ := newServer(t)
	do(t, srv, "PATCH", "/api/meta/v1/objects/HmIP-RF.A%3A1", `{"name":"Lampe","meta":{"ccu-jack":{"hidden":true}}}`, nil)
	st, _, y := do(t, srv, "GET", "/api/meta/v1/export?format=yaml", "", nil)
	if st != 200 || !strings.Contains(y, "Lampe") || !strings.Contains(y, "hidden: true") {
		t.Fatalf("yaml export: %d %s", st, y)
	}
	srv2, s2 := newServer(t)
	req, _ := http.NewRequest("PUT", srv2.URL+"/api/meta/v1/import", strings.NewReader(y))
	req.Header.Set("Content-Type", "application/yaml")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("yaml import: %v %d", err, res.StatusCode)
	}
	o, err := s2.GetObject("HmIP-RF.A:1")
	if err != nil || o.Name != "Lampe" || string(o.Meta["ccu-jack"]) != `{"hidden":true}` {
		t.Fatalf("imported: %v %+v", err, o)
	}
	st, out, _ := do(t, srv2, "PUT", "/api/meta/v1/import", `{"format":2,"revision":0,"objects":{},"enums":{}}`, nil)
	if st != 422 || out["error"] != "format-unsupported" {
		t.Fatalf("format 2: %d %v", st, out)
	}
}

func TestSSEReplayAndLive(t *testing.T) {
	srv, s := newServer(t)
	_, _, _ = s.SetObject(nil, "HmIP-RF.A:1", meta.ObjectPatch{Name: strp("Lampe")})
	req, _ := http.NewRequest("GET", srv.URL+"/api/meta/v1/events/sse?since=0", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 4096)
	first := sseData(t, res.Body, buf)
	if !strings.Contains(first, `"kind":"object.updated"`) || !strings.Contains(first, `"revision":1`) {
		t.Fatalf("replay: %q", first)
	}
	_, _, _ = s.SetObject(nil, "HmIP-RF.A:1", meta.ObjectPatch{Name: strp("Deckenlampe")})
	if got := sseData(t, res.Body, buf); !strings.Contains(got, `"revision":2`) {
		t.Fatalf("live: %q", got)
	}
	req2, _ := http.NewRequest("GET", srv.URL+"/api/meta/v1/events/sse?since=-1", nil)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if got := sseData(t, res2.Body, buf); !strings.Contains(got, `"kind":"resync"`) {
		t.Fatalf("resync: %q", got)
	}
}

func strp(s string) *string { return &s }

// sseData reads until a data frame arrives, skipping the ": connected" comment the stream
// opens with (it may share a read with the first event or not).
func sseData(t *testing.T, r io.Reader, buf []byte) string {
	t.Helper()
	var got string
	for i := 0; i < 5; i++ {
		n, err := r.Read(buf)
		got += string(buf[:n])
		if strings.Contains(got, "data:") {
			return got
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("no data frame: %q", got)
	return got
}
