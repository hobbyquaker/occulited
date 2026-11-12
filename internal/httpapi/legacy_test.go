package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// Task 125 (D-77): the session id has 130 bits; the CCU's ten-character ?sid=@..@ is an alias a
// session gets on request, which the API never accepts - not as a cookie, not as Bearer, not as
// ?sid= - and the addon CGIs' guard takes from ?sid= alone. Downloads and the hand-over of a
// session to another host go by one-time tickets instead of the session in the URL. Since
// openccu-lite task 260 the API reads no credential from ?sid= at all - the session id and a
// token are refused there with 401 and a message that names the header; the addon CGIs' guard
// keeps the gate's rule (the session id or the alias from ?sid=, never a token).

// legacyServer is an API with the auth routes, the meta API (a read to guard) and a CGI stand-in
// under /addons/ behind RequireSession, which answers the session's user.
func legacyServer(t *testing.T) (*httptest.Server, *auth.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "run", "sessions")})
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
	a := &AuthAPI{Store: store}
	a.Register(mux)
	(&MetaAPI{Store: mustMeta(t)}).Register(mux)
	mux.Handle("/addons/", a.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("cgi:" + SessionFrom(r).User))
	})))
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv, store, dir
}

func TestLegacyAliasOverHTTP(t *testing.T) {
	srv, store, dir := legacyServer(t)
	sid := cookieOf(t, srv, `{"username":"bob","password":"bobsecret1"}`)
	cookie := map[string]string{"Cookie": CookieName + "=" + sid}
	if !auth.IsSessionID(sid) {
		t.Fatalf("the login's id: %q", sid)
	}
	// no alias until asked for
	st, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", cookie)
	if st != 200 || out["sid"] != sid || out["legacy_sid"] != nil {
		t.Fatalf("state before an alias: %d %v", st, out)
	}
	// a plain user gets one (a self-service mutation), and the same one again
	st, out, _ = do(t, srv, "POST", "/api/auth/v1/legacy-sid", "", cookie)
	alias, _ := out["legacy_sid"].(string)
	if st != 200 || !auth.IsLegacyID(alias) {
		t.Fatalf("legacy-sid: %d %v", st, out)
	}
	if _, out, _ := do(t, srv, "POST", "/api/auth/v1/legacy-sid", "", cookie); out["legacy_sid"] != alias {
		t.Errorf("a second ask: %v", out)
	}
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", cookie); out["legacy_sid"] != alias || out["sid"] != sid {
		t.Errorf("state with the alias: %v", out)
	}
	// mirrored by hash, with the user and the session's hash
	body, err := os.ReadFile(filepath.Join(dir, "run", "legacy-sessions", gateName(alias)))
	if err != nil || string(body) != "bob\n"+gateName(sid)+"\n" {
		t.Errorf("alias mirror: %q %v", body, err)
	}

	// the API refuses the alias everywhere, wrapped or bare, with the plain 401 (it never was a
	// credential there); the session id it takes from the cookie and the header alone
	for name, c := range map[string]struct {
		path string
		hdr  map[string]string
	}{
		"as the HTTP cookie":       {"/api/meta/v1/snapshot", map[string]string{"Cookie": CookieName + "=" + alias}},
		"as the HTTPS cookie":      {"/api/meta/v1/snapshot", map[string]string{"Cookie": SecureCookieName + "=@" + alias + "@", "X-Forwarded-Proto": "https"}},
		"as Bearer":                {"/api/meta/v1/snapshot", map[string]string{"Authorization": "Bearer " + alias}},
		"as ?sid=@..@":             {"/api/meta/v1/snapshot?sid=@" + alias + "@", nil},
		"as bare ?sid=":            {"/api/meta/v1/snapshot?sid=" + alias, nil},
		"as ?sid= on the auth API": {"/api/auth/v1/sessions?sid=@" + alias + "@", nil},
		"as ?ticket=":              {"/api/meta/v1/snapshot?ticket=" + alias, nil},
	} {
		if st, out, _ := do(t, srv, "GET", c.path, "", c.hdr); st != 401 || out["message"] != "login required" {
			t.Errorf("the alias %s: %d %v", name, st, out)
		}
	}
	if st, out, _ := do(t, srv, "GET", "/api/auth/v1/state?sid=@"+alias+"@", "", nil); st != 200 || out["authenticated"] != false {
		t.Errorf("state with the alias as ?sid=: %d %v", st, out)
	}
	for name, c := range map[string]struct {
		path string
		hdr  map[string]string
	}{
		"the session as the cookie": {"/api/meta/v1/snapshot", cookie},
		"the session as Bearer":     {"/api/meta/v1/snapshot", map[string]string{"Authorization": "Bearer " + sid}},
	} {
		if st, out, _ := do(t, srv, "GET", c.path, "", c.hdr); st != 200 {
			t.Errorf("%s: %d %v", name, st, out)
		}
	}
	// openccu-lite task 260: the session id in the URL is refused with the message that names the header
	for name, path := range map[string]string{
		"the session as ?sid=@..@":             "/api/meta/v1/snapshot?sid=@" + sid + "@",
		"the session as bare ?sid=":            "/api/meta/v1/snapshot?sid=" + sid,
		"the session as ?sid= on the auth API": "/api/auth/v1/sessions?sid=" + sid,
	} {
		if st, out, _ := do(t, srv, "GET", path, "", nil); st != 401 || out["error"] != "unauthenticated" || out["message"] != queryCredentialMessage {
			t.Errorf("%s: %d %v", name, st, out)
		}
	}
	// a ?sid= beside a real credential is ignored, not refused
	if st, out, _ := do(t, srv, "GET", "/api/meta/v1/snapshot?sid=@"+alias+"@", "", cookie); st != 200 {
		t.Errorf("the cookie beside a ?sid=: %d %v", st, out)
	}

	// the addon CGIs' guard: the cookie, or the session id or the alias from ?sid= - not the alias
	// as a cookie, not a token in ?sid= (the gate's rule)
	get := func(path string, hdr map[string]string) (int, string) {
		res, body := fetch(t, srv, path, hdr)
		return res.StatusCode, body
	}
	if st, body := get("/addons/hmm/x.cgi", cookie); st != 200 || body != "cgi:bob" {
		t.Errorf("CGI with the cookie: %d %q", st, body)
	}
	if st, body := get("/addons/hmm/x.cgi?sid="+sid, nil); st != 200 || body != "cgi:bob" {
		t.Errorf("CGI with the session id as ?sid=: %d %q", st, body)
	}
	if st, body := get("/addons/hmm/x.cgi?sid=@"+sid+"@", nil); st != 200 || body != "cgi:bob" {
		t.Errorf("CGI with the wrapped session id as ?sid=: %d %q", st, body)
	}
	if st, _ := get("/addons/hmm/x.cgi?sid=olt_"+strings.Repeat("0", 32), nil); st != 401 {
		t.Errorf("CGI with a token as ?sid=: %d", st)
	}
	if st, body := get("/addons/hmm/x.cgi?sid=@"+alias+"@", nil); st != 200 || body != "cgi:bob" {
		t.Errorf("CGI with the alias as ?sid=: %d %q", st, body)
	}
	if st, body := get("/addons/hmm/x.cgi?sid="+alias, nil); st != 200 || body != "cgi:bob" {
		t.Errorf("CGI with the bare alias: %d %q", st, body)
	}
	if st, _ := get("/addons/hmm/x.cgi", map[string]string{"Cookie": CookieName + "=" + alias}); st != 401 {
		t.Errorf("CGI with the alias as the cookie: %d", st)
	}
	if st, _ := get("/addons/hmm/x.cgi?sid=@nobody0000@", nil); st != 401 {
		t.Errorf("CGI with an alias nobody has: %d", st)
	}

	// the alias ends with the session: a logout, and a saved URL then lands nowhere
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/logout", "", cookie); st != 200 {
		t.Fatal("logout")
	}
	if st, _ := get("/addons/hmm/x.cgi?sid=@"+alias+"@", nil); st != 401 {
		t.Errorf("CGI with the alias after the logout: %d", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "run", "legacy-sessions", gateName(alias))); err == nil {
		t.Error("the alias mirror file outlived the logout")
	}
	if store.ValidateLegacy(alias) != nil {
		t.Error("the store still knows the alias")
	}

	// a token has no alias and gets no ticket
	tok, err := store.CreateToken("ci", auth.TokenOptions{Scopes: auth.Scopes{auth.ScopeAll}})
	if err != nil {
		t.Fatal(err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + tok}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/legacy-sid", "", bearer); st != 409 {
		t.Errorf("legacy-sid for a token: %d", st)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/ticket", `{"path":"/api/meta/v1/snapshot"}`, bearer); st != 409 {
		t.Errorf("a ticket for a token: %d", st)
	}
}

// A ticket stands in for the session on one GET of one path, once; a session ticket opens a
// session on a host that has no cookie.
func TestTicketsOverHTTP(t *testing.T) {
	srv, _, _ := legacyServer(t)
	sid := cookieOf(t, srv, `{"username":"bob","password":"bobsecret1"}`)
	cookie := map[string]string{"Cookie": CookieName + "=" + sid}
	for _, bad := range []string{`{}`, `{"path":"snapshot"}`, `{"path":"/api/meta/v1/snapshot?x=1"}`, `{"path":"/"}`} {
		if st, out, _ := do(t, srv, "POST", "/api/auth/v1/ticket", bad, cookie); st != 422 {
			t.Errorf("ticket for %s: %d %v", bad, st, out)
		}
	}
	st, out, _ := do(t, srv, "POST", "/api/auth/v1/ticket", `{"path":"/api/meta/v1/snapshot"}`, cookie)
	tk, _ := out["ticket"].(string)
	if st != 200 || len(tk) != 26 || out["expires_in"] != float64(60) {
		t.Fatalf("ticket: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/version?ticket="+tk, "", nil); st != 200 {
		t.Error("an open path with a ticket must still answer")
	}
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/sessions?ticket="+tk, "", nil); st != 401 {
		t.Error("the ticket opened another path")
	}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot?ticket="+tk, "", nil); st != 401 {
		t.Error("a ticket tried on another path was not spent")
	}
	_, out, _ = do(t, srv, "POST", "/api/auth/v1/ticket", `{"path":"/api/meta/v1/snapshot"}`, cookie)
	tk = out["ticket"].(string)
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot?ticket="+tk, "", nil); st != 200 {
		t.Error("the ticket did not open its path")
	}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot?ticket="+tk, "", nil); st != 401 {
		t.Error("the ticket opened its path twice")
	}
	// the ticket is no session: the state does not know it, and it is not a Bearer
	if st, out, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", map[string]string{"Authorization": "Bearer " + tk}); st != 401 {
		t.Errorf("a ticket as Bearer: %d %v", st, out)
	}

	// the session ticket: redeemed on "another host" (no cookie), it opens a session of its own
	// for the same account and sets the cookie; the original stays
	_, out, _ = do(t, srv, "POST", "/api/auth/v1/ticket", `{"path":"session"}`, cookie)
	tk = out["ticket"].(string)
	if out["expires_in"] != float64(90) { // D-84: as long as the confirm window
		t.Errorf("a session ticket lives 90 s: %v", out)
	}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot?ticket="+tk, "", nil); st != 401 {
		t.Error("a session ticket opened a path")
	}
	_, out, _ = do(t, srv, "POST", "/api/auth/v1/ticket", `{"path":"session"}`, cookie)
	tk = out["ticket"].(string)
	if out["expires_in"] != float64(90) { // D-84: as long as the confirm window
		t.Errorf("a session ticket lives 90 s: %v", out)
	}
	st, cookies := cookieExchange(t, srv, "POST", "/api/auth/v1/ticket/redeem", `{"ticket":"`+tk+`"}`, nil)
	c := cookies[CookieName]
	if st != 200 || c == nil || !auth.IsSessionID(c.Value) || c.Value == sid {
		t.Fatalf("redeem: %d %v", st, cookies)
	}
	if st, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", map[string]string{"Cookie": CookieName + "=" + c.Value}); st != 200 || out["user"] != "bob" || out["role"] != "user" || out["sid"] != c.Value {
		t.Errorf("the handed-over session: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", cookie); st != 200 || out["authenticated"] != true {
		t.Errorf("the original session: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/ticket/redeem", `{"ticket":"`+tk+`"}`, nil); st != 401 || out["error"] != "invalid-ticket" {
		t.Errorf("redeemed twice: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/ticket/redeem", `{"ticket":"`+sid+`"}`, nil); st != 401 {
		t.Error("the session id redeemed as a ticket")
	}
	// the sessions list shows both, by handle, and no alias or ticket anywhere in it
	_, out, body := do(t, srv, "GET", "/api/auth/v1/sessions", "", cookie)
	if list, _ := out["sessions"].([]any); len(list) != 2 || strings.Contains(body, sid) || strings.Contains(body, tk) {
		t.Errorf("sessions: %s", body)
	}
}

// Auth off: the fixed session and its fixed alias, and the CGI guard takes both.
func TestLegacyAliasAuthOff(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "sessions")})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store, Off: true}
	a.Register(mux)
	mux.Handle("/addons/", a.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("cgi:" + SessionFrom(r).User))
	})))
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	st, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", nil)
	if st != 200 || out["sid"] != auth.AnonymousSID || out["legacy_sid"] != auth.AnonymousLegacySID {
		t.Fatalf("state: %d %v", st, out)
	}
	if _, out, _ := do(t, srv, "POST", "/api/auth/v1/legacy-sid", "", nil); out["legacy_sid"] != auth.AnonymousLegacySID {
		t.Errorf("legacy-sid: %v", out)
	}
	for _, name := range []string{gateName(auth.AnonymousSID), filepath.Join("..", "legacy-sessions", gateName(auth.AnonymousLegacySID))} {
		if _, err := os.Stat(filepath.Join(dir, "sessions", name)); err != nil {
			t.Errorf("mirror: %v", err)
		}
	}
	for _, path := range []string{"/addons/hmm/x.cgi", "/addons/hmm/x.cgi?sid=@" + auth.AnonymousLegacySID + "@"} {
		if res, body := fetch(t, srv, path, nil); res.StatusCode != 200 || body != "cgi:anonymous" {
			t.Errorf("%s: %d %q", path, res.StatusCode, body)
		}
	}
}
