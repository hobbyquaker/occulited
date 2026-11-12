package httpapi

import (
	"encoding/json"
	"fmt"
	"github.com/hobbyquaker/occulited/internal/oidc"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/meta"
)

func authServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := meta.New(nil, nil)
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store}
	a.Register(mux)
	(&MetaAPI{Store: ms}).Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv
}

func cookieOf(t *testing.T, srv *httptest.Server, body string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/v1/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Name == CookieName && c.Value != "" { // not the deletion of the pre-259 cookie at Path=/
			return c.Value
		}
	}
	t.Fatalf("no cookie, status %d", res.StatusCode)
	return ""
}

func TestAuthFlow(t *testing.T) {
	srv := authServer(t)
	// open endpoints work without a session; everything else does not
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/version", "", nil); st != 200 {
		t.Fatalf("version open: %d", st)
	}
	if st, out, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", nil); st != 401 || out["error"] != "unauthenticated" {
		t.Fatalf("snapshot must need a session: %d %v", st, out)
	}
	st, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", nil)
	if st != 200 || out["setup_required"] != true {
		t.Fatalf("state: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"admin","password":"secret123"}`, nil); st != 428 || out["error"] != "setup-required" {
		t.Fatalf("login before setup: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil)
	if st != 200 || out["role"] != "admin" || !auth.IsSessionID(out["sid"].(string)) {
		t.Fatalf("setup: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"x","password":"secret123"}`, nil); st != 409 {
		t.Fatalf("second setup: %d", st)
	}
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"admin","password":"wrong-pw"}`, nil); st != 401 || out["error"] != "invalid-credentials" {
		t.Fatalf("wrong password: %d %v", st, out)
	}
	sid := cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)
	hdr := map[string]string{"Cookie": CookieName + "=" + sid}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", hdr); st != 200 {
		t.Fatalf("snapshot with cookie: %d", st)
	}
	// the session in the URL, wrapped the CCU's way: not a credential on the API (openccu-lite
	// task 260), and the refusal says where it belongs
	if st, out, _ := do(t, srv, "GET", "/api/meta/v1/snapshot?sid=@"+sid+"@", "", nil); st != 401 || out["message"] != queryCredentialMessage {
		t.Fatalf("snapshot with ?sid=: %d %v", st, out)
	}
	// a plain user may read but not mutate
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/users", `{"username":"bob","password":"bobsecret1","role":"user"}`, hdr); st != 201 {
		t.Fatalf("create user: %d", st)
	}
	bob := cookieOf(t, srv, `{"username":"bob","password":"bobsecret1"}`)
	bobHdr := map[string]string{"Cookie": CookieName + "=" + bob}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", bobHdr); st != 200 {
		t.Fatalf("user read: %d", st)
	}
	if st, out, _ := do(t, srv, "PATCH", "/api/meta/v1/objects/BidCos-RF.A%3A1", `{"name":"x"}`, bobHdr); st != 403 || out["error"] != "forbidden" {
		t.Fatalf("user mutate: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/users", "", bobHdr); st != 403 {
		t.Fatalf("user lists users: %d", st)
	}
	// but may change their own password with the current one
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/password", `{"current":"bobsecret1","password":"bobsecret2"}`, bobHdr); st != 200 {
		t.Fatalf("own password: %d", st)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/password", `{"password":"bobsecret3"}`, bobHdr); st != 422 {
		t.Fatalf("own password without current: %d", st)
	}
	// admin resets without the current password
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/password", `{"user":"bob","password":"bobsecret4"}`, hdr); st != 200 {
		t.Fatalf("admin reset: %d", st)
	}
	// sessions: list, sign out everywhere
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/sessions", "", hdr)
	if st != 200 || len(out["sessions"].([]any)) < 1 {
		t.Fatalf("sessions: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/logout", "", hdr); st != 200 {
		t.Fatalf("logout: %d", st)
	}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", hdr); st != 401 {
		t.Fatalf("after logout: %d", st)
	}
	// the last administrator cannot delete themselves or be demoted
	sid = cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)
	hdr = map[string]string{"Cookie": CookieName + "=" + sid}
	if st, _, _ := do(t, srv, "DELETE", "/api/auth/v1/users/admin", "", hdr); st != 409 {
		t.Fatalf("delete self: %d", st)
	}
	if st, _, _ := do(t, srv, "PATCH", "/api/auth/v1/users/admin", `{"role":"user"}`, hdr); st != 409 {
		t.Fatalf("demote last admin: %d", st)
	}
	if st, _, _ := do(t, srv, "DELETE", "/api/auth/v1/users/bob", "", hdr); st != 200 {
		t.Fatalf("delete bob: %d", st)
	}
}

func TestAPITokens(t *testing.T) {
	srv := authServer(t)
	st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"test-passw0rd-1"}`, nil)
	if st != 200 {
		t.Fatalf("setup: %d", st)
	}
	cookie := cookieOf(t, srv, `{"username":"admin","password":"test-passw0rd-1"}`)
	hdr := map[string]string{"Cookie": CookieName + "=" + cookie}
	st, out, _ := do(t, srv, "POST", "/api/auth/v1/tokens", `{"name":"hm2mqtt","role":"user"}`, hdr)
	if st != 201 || !strings.HasPrefix(out["token"].(string), "olt_") {
		t.Fatalf("create: %d %v", st, out)
	}
	secret := out["token"].(string)
	st, out, _ = do(t, srv, "POST", "/api/auth/v1/tokens", `{"name":"hm2mqtt","scopes":["led"]}`, hdr)
	if st != 409 {
		t.Fatalf("duplicate: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/tokens", "", hdr)
	if st != 200 || len(out["tokens"].([]any)) != 1 || out["tokens"].([]any)[0].(map[string]any)["hash"] != nil {
		t.Fatalf("list: %d %v", st, out)
	}
	// the token works as a bearer credential, not as ?sid= (openccu-lite task 260)
	bearer := map[string]string{"Authorization": "Bearer " + secret}
	st, out, _ = do(t, srv, "GET", "/api/meta/v1/snapshot", "", bearer)
	if st != 200 {
		t.Fatalf("bearer read: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/meta/v1/snapshot?sid="+secret, "", nil)
	if st != 401 || out["error"] != "unauthenticated" || out["message"] != queryCredentialMessage {
		t.Fatalf("query read: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/state", "", bearer)
	if st != 200 || out["user"] != "token:hm2mqtt" || out["role"] != nil || fmt.Sprint(out["scopes"]) != "[logs:read meta:read system:read]" {
		t.Fatalf("state: %d %v", st, out)
	}
	// the role alias mapped to the read scopes: no mutations, no user management, and the 403
	// names the scope that is missing
	st, out, _ = do(t, srv, "PUT", "/api/meta/v1/objects/BidCos-RF.ABC0000001:1", `{"name":"x"}`, bearer)
	if st != 403 || out["scope"] != "meta:write" {
		t.Fatalf("user token mutation: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/tokens", "", bearer)
	if st != 403 || out["scope"] != "auth:admin" {
		t.Fatalf("user token lists tokens: %d %v", st, out)
	}
	// explicit scopes, an expiry and ranges; the list carries them and the grantable scopes
	st, out, _ = do(t, srv, "POST", "/api/auth/v1/tokens", `{"name":"lan","scopes":["meta:write","meta:read"],"expires":"2099-01-01T00:00:00Z","ips":["127.0.0.1","10.0.0.0/8"]}`, hdr)
	if st != 201 || fmt.Sprint(out["scopes"]) != "[meta:write]" || out["expires"] != "2099-01-01T00:00:00Z" {
		t.Fatalf("create with scopes: %d %v", st, out)
	}
	lan := map[string]string{"Authorization": "Bearer " + out["token"].(string)}
	if st, _, _ := do(t, srv, "PUT", "/api/meta/v1/objects/BidCos-RF.ABC0000001:1", `{"name":"x"}`, lan); st == 401 || st == 403 {
		t.Fatalf("write with meta:write from 127.0.0.1: %d", st)
	}
	if st, out, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", map[string]string{"Authorization": lan["Authorization"], "X-Forwarded-For": "192.168.7.7"}); st != 401 {
		t.Fatalf("outside the ranges: %d %v", st, out)
	}
	// B-129: a ranged token says what it is to the system itself (an addon's session check after the
	// gate), from nowhere else, and that opens nothing: everything else still holds the loopback to the ranges
	st, out, _ = do(t, srv, "POST", "/api/auth/v1/tokens", `{"name":"far","scopes":["meta:read"],"ips":["192.0.2.0/24"]}`, hdr)
	if st != 201 {
		t.Fatalf("create far: %d %v", st, out)
	}
	farToken := "Bearer " + out["token"].(string)
	for _, c := range []struct {
		name, forwarded string
		want            bool
	}{
		{"the loopback, as a program on the system", "", true},
		{"the loopback, through lighttpd", "127.0.0.1", true},
		{"in range", "192.0.2.7", true},
		{"outside the range", "198.51.100.7", false},
	} {
		h := map[string]string{"Authorization": farToken}
		if c.forwarded != "" {
			h["X-Forwarded-For"] = c.forwarded
		}
		if st, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", h); st != 200 || out["authenticated"] != c.want {
			t.Fatalf("state from %s: %d %v", c.name, st, out)
		}
	}
	if st, out, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", map[string]string{"Authorization": farToken}); st != 401 {
		t.Fatalf("the loopback is outside the range everywhere else: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "DELETE", "/api/auth/v1/tokens/far", "", hdr); st >= 300 {
		t.Fatalf("delete far: %d %v", st, out)
	}
	// B-230: a client-sent element in range before lighttpd's does not open the token
	if st, out, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", map[string]string{"Authorization": lan["Authorization"], "X-Forwarded-For": "10.1.2.3, 192.168.7.7"}); st != 401 {
		t.Fatalf("a forged in-range element: %d %v", st, out)
	}
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/tokens", "", hdr)
	if st != 200 || len(out["tokens"].([]any)) != 2 || len(out["scopes"].([]any)) != 16 {
		t.Fatalf("list: %d %v", st, out)
	}
	if tk := out["tokens"].([]any)[1].(map[string]any); tk["name"] != "lan" || fmt.Sprint(tk["ips"]) != "[127.0.0.1/32 10.0.0.0/8]" || tk["expires"] == nil || fmt.Sprint(tk["scopes"]) != "[meta:write]" {
		t.Fatalf("listed: %v", tk)
	}
	for _, bad := range []string{`{"name":"x","scopes":["root"]}`, `{"name":"x","scopes":[]}`, `{"name":"x"}`, `{"name":"x","role":"root"}`, `{"name":"x","scopes":["self"]}`, `{"name":"x","scopes":["meta:read"],"ips":["nope"]}`, `{"name":"x","scopes":["meta:read"],"expires":"2000-01-01T00:00:00Z"}`} {
		if st, _, _ := do(t, srv, "POST", "/api/auth/v1/tokens", bad, hdr); st != 422 {
			t.Errorf("%s: %d", bad, st)
		}
	}
	if st, _, _ := do(t, srv, "DELETE", "/api/auth/v1/tokens/lan", "", hdr); st != 200 {
		t.Fatalf("delete lan: %d", st)
	}
	// revoke
	st, _, _ = do(t, srv, "DELETE", "/api/auth/v1/tokens/hm2mqtt", "", hdr)
	if st != 200 {
		t.Fatalf("delete: %d", st)
	}
	st, _, _ = do(t, srv, "GET", "/api/meta/v1/snapshot", "", bearer)
	if st != 401 {
		t.Fatalf("revoked token still works: %d", st)
	}
	st, _, _ = do(t, srv, "DELETE", "/api/auth/v1/tokens/hm2mqtt", "", hdr)
	if st != 404 {
		t.Fatalf("delete twice: %d", st)
	}
}

// fakeProvider sends the browser straight back with a code and answers userinfo with the user
// name it is given (a pointer, so a test can change who signs in).
func fakeProvider(t *testing.T, username *string) *httptest.Server {
	t.Helper()
	var prov *httptest.Server
	prov = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Write([]byte(`{"issuer":"` + prov.URL + `","authorization_endpoint":"` + prov.URL + `/authorize","token_endpoint":"` + prov.URL + `/token","userinfo_endpoint":"` + prov.URL + `/userinfo"}`))
		case "/authorize":
			http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?code=c&state="+r.URL.Query().Get("state"), http.StatusFound)
		case "/token":
			w.Write([]byte(`{"access_token":"at","token_type":"Bearer"}`))
		case "/userinfo":
			b, _ := json.Marshal(map[string]any{"sub": "u-1", "preferred_username": *username, "groups": []string{"admins"}})
			w.Write(b)
		}
	}))
	t.Cleanup(prov.Close)
	return prov
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// providerLogin runs the browser's part of the flow and returns where the callback sent it and the
// session cookie it set, if any.
func providerLogin(t *testing.T, srv *httptest.Server, returnTo string) (location, cookie string) {
	t.Helper()
	res, err := noRedirect.Get(srv.URL + "/api/auth/v1/oidc/start?return=" + returnTo)
	if err != nil || res.StatusCode != 302 {
		t.Fatalf("start: %v %d", err, res.StatusCode)
	}
	res2, err := noRedirect.Get(res.Header.Get("Location")) // the provider
	if err != nil || res2.StatusCode != 302 {
		t.Fatalf("provider: %v %d", err, res2.StatusCode)
	}
	cb := res2.Header.Get("Location")
	if !strings.HasPrefix(cb, srv.URL+"/api/auth/v1/oidc/callback?") {
		t.Fatalf("callback url: %s", cb)
	}
	res3, err := noRedirect.Get(cb)
	if err != nil || res3.StatusCode != 302 {
		t.Fatalf("callback: %v %d", err, res3.StatusCode)
	}
	for _, c := range res3.Cookies() {
		if c.Name == CookieName {
			cookie = c.Value
		}
	}
	return res3.Header.Get("Location"), cookie
}

// task 19 (D-53, D-54): a provider login needs an account of exactly the provider's user name and
// takes that account's role; an unknown name is refused, logged and creates nothing; an account
// created without a password signs in through the provider only.
func TestOIDCLogin(t *testing.T) {
	username := "basti"
	prov := fakeProvider(t, &username)
	dir := t.TempDir()
	store, _ := auth.Open(dir, auth.Options{})
	_ = store.Setup("admin", "test-passw0rd-1")
	var logs strings.Builder
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store, OIDC: oidc.New(oidc.Config{Issuer: prov.URL, ClientID: "x"}), OIDCName: "authentik", Log: slog.New(slog.NewTextHandler(&logs, nil))}
	a.Register(mux)
	(&MetaAPI{Store: mustMeta(t)}).Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	defer srv.Close()
	admin := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"admin","password":"test-passw0rd-1"}`)}

	st, out, _ := do(t, srv, "GET", "/api/auth/v1/oidc", "", nil)
	if st != 200 || out["enabled"] != true || out["name"] != "authentik" || out["password_login"] != true {
		t.Fatalf("info: %d %v", st, out)
	}
	// no account "basti": refused, the login page told, nothing created, the name in the log
	loc, cookie := providerLogin(t, srv, "/addons/hmm/")
	if loc != "/login?error=no-account&user=basti" || cookie != "" {
		t.Fatalf("unknown name: %q cookie %q", loc, cookie)
	}
	if !strings.Contains(logs.String(), "reason=\"unknown user (nothing is created)\"") || !strings.Contains(logs.String(), "method=oidc") || !strings.Contains(logs.String(), "user=basti") || !strings.Contains(logs.String(), "remote=127.0.0.1") {
		t.Errorf("log: %s", logs.String())
	}
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/users", "", admin)
	if st != 200 || len(out["users"].([]any)) != 1 {
		t.Fatalf("something was created: %d %v", st, out)
	}
	// the near misses: the match is the exact string
	for _, name := range []string{"Basti", "basti ", "admin@example.org", "Admin"} {
		username = name
		if loc, _ := providerLogin(t, srv, "/"); !strings.HasPrefix(loc, "/login?error=no-account&user=") {
			t.Errorf("%q signed in: %s", name, loc)
		}
	}
	username = "basti"
	// the account, created without a password while the provider is configured, role user
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/users", `{"username":"basti","role":"user"}`, admin); st != 201 {
		t.Fatalf("create without a password: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"basti","password":""}`, nil); st != 401 {
		t.Fatalf("password login of a provider-only account: %d", st)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"basti","password":"whatever1"}`, nil); st != 401 {
		t.Fatalf("password login of a provider-only account: %d", st)
	}
	loc, cookie = providerLogin(t, srv, "/addons/hmm/&x=1")
	if loc != "/addons/hmm/" || cookie == "" { // the cookie is the session; no ?sid= in the URL (task 125)
		t.Fatalf("callback: %s cookie %q", loc, cookie)
	}
	basti := map[string]string{"Cookie": CookieName + "=" + cookie}
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/state", "", basti)
	// role user: the account's, not the provider's "admins" group
	if st != 200 || out["user"] != "basti" || out["role"] != "user" || out["method"] != "oidc" || out["must_change_password"] != false {
		t.Fatalf("state: %d %v", st, out)
	}
	// the list says how the account signs in and when the provider last did
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/users", "", admin)
	if st != 200 {
		t.Fatalf("users: %d %v", st, out)
	}
	listed := map[string]map[string]any{}
	for _, u := range out["users"].([]any) {
		m := u.(map[string]any)
		listed[m["name"].(string)] = m
	}
	if b := listed["basti"]; b == nil || b["password_set"] != false || b["last_provider_login"] == nil || b["external"] != nil {
		t.Errorf("basti listed as %v", listed["basti"])
	}
	if ad := listed["admin"]; ad == nil || ad["password_set"] != true || ad["last_provider_login"] != nil {
		t.Errorf("admin listed as %v", listed["admin"])
	}
	// she cannot change a password she has not got; an administrator gives her one, then both work
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/password", `{"current":"","password":"bastisecret1"}`, basti); st != 409 || out["error"] != "no-password" {
		t.Errorf("own change without a password: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/password", `{"user":"basti","password":"bastisecret1"}`, admin); st != 200 {
		t.Errorf("reset: %d", st)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"basti","password":"bastisecret1"}`, nil); st != 200 {
		t.Errorf("password login after the reset: %d", st)
	}
	// an administrator's account signs in through the provider as an administrator (its role)
	username = "admin"
	loc, cookie = providerLogin(t, srv, "/")
	st, out, _ = do(t, srv, "GET", "/api/auth/v1/state", "", map[string]string{"Cookie": CookieName + "=" + cookie})
	if st != 200 || out["user"] != "admin" || out["role"] != "admin" || out["method"] != "oidc" {
		t.Fatalf("admin via the provider: %s %d %v", loc, st, out)
	}
	// must_change_password is a password's flag: an account created with it is sent to change it
	// after a password login, not after a provider login
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/users", `{"username":"dora","password":"dorasecret1","role":"user","must_change_password":true}`, admin); st != 201 {
		t.Fatalf("create dora: %d", st)
	}
	username = "dora"
	_, cookie = providerLogin(t, srv, "/")
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", map[string]string{"Cookie": CookieName + "=" + cookie}); out["must_change_password"] != false {
		t.Errorf("must_change_password on a provider session: %v", out)
	}
	pwCookie := cookieOf(t, srv, `{"username":"dora","password":"dorasecret1"}`)
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", map[string]string{"Cookie": CookieName + "=" + pwCookie}); out["must_change_password"] != true {
		t.Errorf("must_change_password on a password session: %v", out)
	}
	// without a provider an account needs a password
	plain := authServer(t)
	_, _, _ = do(t, plain, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"test-passw0rd-1"}`, nil)
	hdr := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, plain, `{"username":"admin","password":"test-passw0rd-1"}`)}
	if st, out, _ := do(t, plain, "POST", "/api/auth/v1/users", `{"username":"carol","role":"user"}`, hdr); st != 422 || out["error"] != "invalid-body" {
		t.Errorf("no password without a provider: %d %v", st, out)
	}
	if st, out, _ := do(t, plain, "GET", "/api/auth/v1/oidc", "", nil); st != 200 || out["enabled"] != false || out["password_login"] != true {
		t.Errorf("info without a provider: %d %v", st, out)
	}
}

// task 19: the password-login switch. Off only in mode oidc and only from a provider session;
// then POST /login and POST /password answer 403 for every account, the setup is not affected,
// and the console's write of the file turns it back on without a restart.
func TestPasswordLoginSwitch(t *testing.T) {
	username := "admin"
	prov := fakeProvider(t, &username)
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "occulited.json")
	cfg := config.Default()
	cfg.Auth.Mode, cfg.Auth.OIDC.Enabled, cfg.Auth.OIDC.Issuer, cfg.Auth.OIDC.ClientID, cfg.Auth.OIDC.Name = "oidc", true, prov.URL, "x", "authentik"
	if err := config.Save(cfgFile, cfg); err != nil {
		t.Fatal(err)
	}
	store, _ := auth.Open(dir, auth.Options{})
	_ = store.Setup("admin", "test-passw0rd-1")
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store, OIDC: oidc.New(oidc.Config{Issuer: prov.URL, ClientID: "x"}), OIDCName: "authentik", ConfigFile: cfgFile}
	a.Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	defer srv.Close()
	byPassword := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"admin","password":"test-passw0rd-1"}`)}
	_, c := providerLogin(t, srv, "/")
	byProvider := map[string]string{"Cookie": CookieName + "=" + c}
	body := func(on bool) string {
		return fmt.Sprintf(`{"mode":"oidc","name":"authentik","issuer":%q,"client_id":"x","password_login":%v}`, prov.URL, on)
	}
	// off from a password session: refused, nothing written
	if st, out, _ := do(t, srv, "PUT", "/api/auth/v1/config", body(false), byPassword); st != 403 || out["error"] != "provider_session_required" {
		t.Fatalf("off from a password session: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "GET", "/api/auth/v1/config", "", byPassword); st != 200 || out["password_login"] != true {
		t.Fatalf("after the refusal: %d %v", st, out)
	}
	// off in another mode is written as on
	if st, out, _ := do(t, srv, "PUT", "/api/auth/v1/config", `{"mode":"local","password_login":false}`, byProvider); st != 200 || out["password_login"] != true {
		t.Fatalf("off in mode local: %d %v", st, out)
	}
	// off from a provider session, in mode oidc
	st, out, _ := do(t, srv, "PUT", "/api/auth/v1/config", body(false), byProvider)
	if st != 200 || out["password_login"] != false || out["restart_required"] != false {
		t.Fatalf("off: %d %v", st, out)
	}
	if _, ok := out["admin_groups"]; ok {
		t.Error("admin_groups is still in the view")
	}
	if st, out, _ := do(t, srv, "GET", "/api/auth/v1/oidc", "", nil); st != 200 || out["password_login"] != false || out["enabled"] != true {
		t.Errorf("info: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"admin","password":"test-passw0rd-1"}`, nil); st != 403 || out["error"] != "password_login_disabled" {
		t.Errorf("login with the switch off: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"nobody","password":"x"}`, nil); st != 403 || out["error"] != "password_login_disabled" {
		t.Errorf("login of an unknown name with the switch off: %d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/password", `{"user":"admin","password":"another-pw-1"}`, byProvider); st != 403 || out["error"] != "password_login_disabled" {
		t.Errorf("password reset with the switch off: %d %v", st, out)
	}
	// the sessions that exist stay, whichever way they were opened; the provider still signs in
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/state", "", byPassword); st != 200 {
		t.Errorf("the password session: %d", st)
	}
	if loc, c := providerLogin(t, srv, "/"); loc != "/" || c == "" {
		t.Errorf("provider login with the switch off: %s", loc)
	}
	// a further save from a password session that leaves the switch off is fine (not a switch-off)
	if st, out, _ := do(t, srv, "PUT", "/api/auth/v1/config", body(false), byPassword); st != 200 || out["password_login"] != false {
		t.Errorf("save with the switch already off: %d %v", st, out)
	}
	// the console's break-glass: the file is written, the daemon follows without a restart
	if _, err := config.SetPasswordLogin(cfgFile, true); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"admin","password":"test-passw0rd-1"}`, nil); st != 200 {
		t.Errorf("login after the console turned the switch on: %d", st)
	}
	if st, out, _ := do(t, srv, "GET", "/api/auth/v1/config", "", byPassword); st != 200 || out["password_login"] != true {
		t.Errorf("config after the console: %d %v", st, out)
	}
	// and back off, from the provider session, with the file written by the console too
	if _, err := config.SetPasswordLogin(cfgFile, false); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"admin","password":"test-passw0rd-1"}`, nil); st != 403 {
		t.Errorf("login after the console turned the switch off: %d", st)
	}
	// the first-boot setup takes a password whatever the switch says: a fresh store, the same switch
	fresh, _ := auth.Open(t.TempDir(), auth.Options{})
	mux2 := http.NewServeMux()
	a2 := &AuthAPI{Store: fresh, OIDC: oidc.New(oidc.Config{Issuer: prov.URL, ClientID: "x"}), ConfigFile: cfgFile}
	a2.Register(mux2)
	srv2 := httptest.NewServer(a2.Middleware(mux2))
	defer srv2.Close()
	if st, out, _ := do(t, srv2, "POST", "/api/auth/v1/setup", `{"username":"root","password":"first-passw0rd"}`, nil); st != 200 || out["role"] != "admin" {
		t.Errorf("setup with the switch off: %d %v", st, out)
	}
	if st, _, _ := do(t, srv2, "POST", "/api/auth/v1/login", `{"username":"root","password":"first-passw0rd"}`, nil); st != 403 {
		t.Errorf("login after the setup with the switch off: %d", st)
	}
}

func mustMeta(t *testing.T) *meta.Store {
	t.Helper()
	ms, err := meta.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{"/addons/x/": "/addons/x/", "": "", "//evil.example": "", "https://evil.example/": "", "/a?b=c": "/a?b=c", "/x\r\nSet-Cookie: y": ""} {
		if got := localPath(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// ---- task 29: the authentication mode ------------------------------------------------------

func TestAuthConfigRoutes(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "occulited.json")
	if err := config.Save(cfgFile, config.Default()); err != nil {
		t.Fatal(err)
	}
	store, err := auth.Open(dir, auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Setup("admin", "correct horse battery")
	sess, _ := store.Login("admin", "correct horse battery", "127.0.0.1", "test")
	api := &AuthAPI{Store: store, ConfigFile: cfgFile}
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(api.Middleware(mux))
	t.Cleanup(srv.Close)
	hdr := map[string]string{"Authorization": "Bearer " + sess.ID}

	st, out, _ := do(t, srv, "GET", "/api/auth/v1/config", "", hdr)
	if st != 200 || out["mode"] != "local" || out["running"] != "local" || out["restart_required"] != false {
		t.Fatalf("get: %d %v", st, out)
	}
	// oidc without an issuer is refused; with one it is written, the secret never read back
	if st, _, _ := do(t, srv, "PUT", "/api/auth/v1/config", `{"mode":"oidc","client_id":"x"}`, hdr); st != 422 {
		t.Errorf("oidc without issuer: %d", st)
	}
	st, out, _ = do(t, srv, "PUT", "/api/auth/v1/config", `{"mode":"oidc","name":"authentik","issuer":"https://auth.example.org/application/o/lite/","client_id":"abc","client_secret":"s3cret"}`, hdr)
	if st != 200 || out["mode"] != "oidc" || out["client_secret_set"] != true || out["restart_required"] != true || out["password_login"] != true {
		t.Fatalf("put oidc: %d %v", st, out)
	}
	if _, ok := out["admin_groups"]; ok {
		t.Error("task 19 (D-54): admin_groups is gone from the view")
	}
	if _, ok := out["client_secret"]; ok {
		t.Error("the secret came back")
	}
	cfg, _ := config.Load(cfgFile)
	if cfg.Auth.Mode != "oidc" || !cfg.Auth.OIDC.Enabled || cfg.Auth.OIDC.ClientSecret != "s3cret" {
		t.Errorf("file: %+v", cfg.Auth)
	}
	// an empty secret keeps the stored one; off is a mode
	st, out, _ = do(t, srv, "PUT", "/api/auth/v1/config", `{"mode":"off","issuer":"https://auth.example.org/application/o/lite/","client_id":"abc"}`, hdr)
	if st != 200 || out["mode"] != "off" || out["client_secret_set"] != true {
		t.Fatalf("put off: %d %v", st, out)
	}
	cfg, _ = config.Load(cfgFile)
	if cfg.Auth.EffectiveMode() != "off" || cfg.Auth.OIDC.Enabled || cfg.Auth.OIDC.ClientSecret != "s3cret" {
		t.Errorf("file after off: %+v", cfg.Auth)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/auth/v1/config", `{"mode":"maybe"}`, hdr); st != 422 {
		t.Errorf("bad mode: %d", st)
	}
	// openccu-lite B-206: GET's read-only fields sent back are refused - strictness caught the UI
	// that did it - with a message that names the field and the fields PUT takes; nothing written
	for _, f := range []string{`"modes":["local","oidc","off"]`, `"client_secret_set":true`, `"restart_required":false`, `"running":"local"`} {
		st, out, _ := do(t, srv, "PUT", "/api/auth/v1/config", `{"mode":"local",`+f+`}`, hdr)
		msg, _ := out["message"].(string)
		name := f[1 : strings.Index(f[1:], `"`)+1]
		if st != 422 || !strings.Contains(msg, `"`+name+`" is not a field PUT /config takes`) || !strings.Contains(msg, "send only mode, name, issuer, client_id, client_secret, username_claim, scopes, password_login") {
			t.Errorf("%s: %d %q", name, st, msg)
		}
	}
	if cfg, _ := config.Load(cfgFile); cfg.Auth.EffectiveMode() != "off" {
		t.Errorf("a refused body changed the mode: %s", cfg.Auth.EffectiveMode())
	}
	// a plain user may read, not write
	_ = store.CreateUser("bob", "bobs password 123", auth.RoleUser, false)
	bob, _ := store.Login("bob", "bobs password 123", "127.0.0.1", "test")
	if st, _, _ := do(t, srv, "PUT", "/api/auth/v1/config", `{"mode":"local"}`, map[string]string{"Authorization": "Bearer " + bob.ID}); st != 403 {
		t.Errorf("user writing the mode: %d", st)
	}
}

// Auth off: no session needed anywhere, /state hands out the anonymous administrator and its
// cookie, and the gate's mirror file exists.
func TestAuthOff(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	_ = os.MkdirAll(sessions, 0o755)
	store, err := auth.Open(dir, auth.Options{SessionDir: sessions})
	if err != nil {
		t.Fatal(err)
	}
	api := &AuthAPI{Store: store, Off: true}
	mux := http.NewServeMux()
	api.Register(mux)
	route(mux, auth.ScopeSystemWrite, "PUT /api/x", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"user": SessionFrom(r).User})
	})
	srv := httptest.NewServer(api.Middleware(mux))
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", nil)
	if st != 200 || out["authenticated"] != true || out["role"] != "admin" || out["auth_off"] != true || out["setup_required"] != false || out["sid"] != auth.AnonymousSID {
		t.Fatalf("state: %d %v", st, out)
	}
	resp, err := http.Get(srv.URL + "/api/auth/v1/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if c := strings.Join(resp.Header.Values("Set-Cookie"), "\n"); !strings.Contains(c, CookieName+"="+auth.AnonymousSID) || !strings.Contains(c, GateCookieName+"="+auth.AnonymousSID) {
		t.Errorf("no cookies for the gate: %q", c)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/x", `{}`, nil); st != 200 || out["user"] != "anonymous" {
		t.Errorf("a mutation without a session: %d %v", st, out)
	}
	if _, err := os.Stat(filepath.Join(sessions, gateName(auth.AnonymousSID))); err != nil {
		t.Errorf("gate mirror file: %v", err)
	}
	// over HTTPS the anonymous session goes into the HTTPS cookies - the session one at /api and
	// the gate one at /addons/ (task 259) - on every state call, whatever the browser already
	// sends: the route cannot see a gate cookie, and the id is fixed
	for _, cookie := range []string{"", CookieName + "=" + auth.AnonymousSID, SecureCookieName + "=" + auth.AnonymousSID} {
		hdr := map[string]string{"X-Forwarded-Proto": "https"}
		if cookie != "" {
			hdr["Cookie"] = cookie
		}
		_, cookies := cookieExchange(t, srv, "GET", "/api/auth/v1/state", "", hdr)
		c, g := cookies[SecureCookieName], cookies[SecureGateCookieName]
		if c == nil || c.Value != auth.AnonymousSID || !c.Secure || c.Path != CookiePath || g == nil || g.Value != auth.AnonymousSID || !g.Secure || g.Path != GateCookiePath || cookies[CookieName] != nil {
			t.Errorf("cookie %q: HTTPS cookies %v", cookie, cookies)
		}
	}
}

// cookieExchange sends a request and returns the status and the cookies the response sets, by
// name.
func cookieExchange(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) (int, map[string]*http.Cookie) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	shellHeader(req, hdr)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	out := map[string]*http.Cookie{}
	for _, c := range res.Cookies() {
		out[c.Name] = c
	}
	return res.StatusCode, out
}

// One session cookie per scheme: a login over HTTPS must not keep a later one over HTTP from
// setting its cookie, and every reader accepts both.
func TestSessionCookiePerScheme(t *testing.T) {
	srv := authServer(t)
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	creds := `{"username":"admin","password":"secret123"}`
	https := func(hdr map[string]string) map[string]string {
		hdr["X-Forwarded-Proto"] = "https"
		return hdr
	}

	// HTTPS first: the prefixed cookie, Secure, and nothing under the HTTP name
	st, cookies := cookieExchange(t, srv, "POST", "/api/auth/v1/login", creds, https(map[string]string{}))
	secure := cookies[SecureCookieName]
	if st != 200 || secure == nil || !secure.Secure || !secure.HttpOnly || secure.MaxAge <= 0 || secure.Path != CookiePath || cookies[CookieName] != nil {
		t.Fatalf("HTTPS login: %d %v", st, cookies)
	}
	// task 259: the gate cookie beside it, the same id at /addons/, and nothing under the HTTP names
	if g := cookies[SecureGateCookieName]; g == nil || g.Value != secure.Value || !g.Secure || !g.HttpOnly || g.Path != GateCookiePath || g.MaxAge != secure.MaxAge || cookies[GateCookieName] != nil {
		t.Fatalf("HTTPS login, gate cookie: %v", cookies)
	}
	// then HTTP, in the same browser (it does not send the Secure cookie over HTTP): the plain
	// cookie without Secure, and the HTTPS one is not touched
	st, cookies = cookieExchange(t, srv, "POST", "/api/auth/v1/login", creds, nil)
	plain := cookies[CookieName]
	if st != 200 || plain == nil || plain.Secure || !plain.HttpOnly || plain.MaxAge <= 0 || plain.Path != CookiePath || cookies[SecureCookieName] != nil {
		t.Fatalf("HTTP login: %d %v", st, cookies)
	}
	if g := cookies[GateCookieName]; g == nil || g.Value != plain.Value || g.Secure || g.Path != GateCookiePath || cookies[SecureGateCookieName] != nil {
		t.Fatalf("HTTP login, gate cookie: %v", cookies)
	}
	if plain.Value == secure.Value {
		t.Fatal("both logins got the same session")
	}

	for _, tc := range []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"the HTTPS cookie over HTTPS", https(map[string]string{"Cookie": SecureCookieName + "=" + secure.Value}), 200},
		{"the HTTP cookie over HTTP", map[string]string{"Cookie": CookieName + "=" + plain.Value}, 200},
		{"the HTTP cookie over HTTPS", https(map[string]string{"Cookie": CookieName + "=" + plain.Value}), 200},
		{"both cookies", https(map[string]string{"Cookie": CookieName + "=" + plain.Value + "; " + SecureCookieName + "=" + secure.Value}), 200},
		{"@-wrapped", map[string]string{"Cookie": SecureCookieName + "=@" + secure.Value + "@"}, 200},
		{"a stale HTTP cookie before a live HTTPS one", https(map[string]string{"Cookie": CookieName + "=STALE00000; " + SecureCookieName + "=" + secure.Value}), 200},
		{"a stale HTTPS cookie before a live HTTP one", https(map[string]string{"Cookie": SecureCookieName + "=STALE00000; " + CookieName + "=" + plain.Value}), 200},
		{"one name twice, the first stale", map[string]string{"Cookie": CookieName + "=STALE00000; " + CookieName + "=" + plain.Value}, 200},
		{"a stale cookie and a live bearer", map[string]string{"Cookie": CookieName + "=STALE00000", "Authorization": "Bearer " + secure.Value}, 200},
		{"only stale cookies", https(map[string]string{"Cookie": CookieName + "=STALE00000; " + SecureCookieName + "=STALE11111"}), 401},
		{"a cookie merely ending in the name", map[string]string{"Cookie": "x_" + CookieName + "=" + plain.Value}, 401},
		{"a look-alike of the HTTPS name", map[string]string{"Cookie": "__Secure_occulite_session=" + secure.Value}, 401},
		// task 259: the gate cookie opens addon pages, never the API
		{"the gate cookie on the API", map[string]string{"Cookie": GateCookieName + "=" + plain.Value}, 401},
		{"the HTTPS gate cookie on the API", https(map[string]string{"Cookie": SecureGateCookieName + "=" + secure.Value}), 401},
	} {
		if st, out, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", tc.hdr); st != tc.want {
			t.Errorf("%s: %d %v, want %d", tc.name, st, out, tc.want)
		}
	}

	// state answers for either cookie
	for name, c := range map[string]*http.Cookie{SecureCookieName: secure, CookieName: plain} {
		st, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", https(map[string]string{"Cookie": name + "=" + c.Value}))
		if st != 200 || out["authenticated"] != true || out["sid"] != c.Value {
			t.Errorf("state with %s: %d %v", name, st, out)
		}
	}
}

// RequireSession guards the addon CGIs, whose browser requests carry nothing but the cookie.
func TestRequireSessionCookies(t *testing.T) {
	store, err := auth.Open(t.TempDir(), auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Setup("admin", "secret123")
	sess, _ := store.Login("admin", "secret123", "127.0.0.1", "test")
	a := &AuthAPI{Store: store}
	srv := httptest.NewServer(a.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(SessionFrom(r).User))
	})))
	t.Cleanup(srv.Close)
	for _, tc := range []struct {
		name, cookie string
		want         int
	}{
		{"HTTP cookie", CookieName + "=" + sess.ID, 200},
		{"HTTPS cookie", SecureCookieName + "=" + sess.ID, 200},
		{"a stale HTTP cookie and a live HTTPS one", CookieName + "=STALE00000; " + SecureCookieName + "=" + sess.ID, 200},
		// task 259: what a browser carries to /addons/ is the gate cookie
		{"HTTP gate cookie", GateCookieName + "=" + sess.ID, 200},
		{"HTTPS gate cookie, @-wrapped", SecureGateCookieName + "=@" + sess.ID + "@", 200},
		{"a stale HTTPS gate cookie and a live HTTP one", SecureGateCookieName + "=STALE00000; " + GateCookieName + "=" + sess.ID, 200},
		{"no cookie", "", 401},
		{"a stale cookie", SecureCookieName + "=STALE00000", 401},
		{"a stale gate cookie", GateCookieName + "=STALE00000", 401},
		{"a look-alike of the gate name", "x_" + GateCookieName + "=" + sess.ID, 401},
	} {
		hdr := map[string]string{}
		if tc.cookie != "" {
			hdr["Cookie"] = tc.cookie
		}
		if st, _ := cookieExchange(t, srv, "GET", "/addons/x/index.cgi", "", hdr); st != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, st, tc.want)
		}
	}
}

// Logout deletes both cookies and ends every session the request carries; a stale cookie is
// deleted where the response may do so, a live one never.
func TestLogoutAndStaleCookies(t *testing.T) {
	srv := authServer(t)
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	creds := `{"username":"admin","password":"secret123"}`
	httpsHdr := func(cookie string) map[string]string {
		return map[string]string{"X-Forwarded-Proto": "https", "Cookie": cookie}
	}
	login := func(hdr map[string]string) map[string]*http.Cookie {
		t.Helper()
		st, cookies := cookieExchange(t, srv, "POST", "/api/auth/v1/login", creds, hdr)
		if st != 200 {
			t.Fatalf("login: %d", st)
		}
		return cookies
	}
	alive := func(name, sid string) bool {
		st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", map[string]string{"Cookie": name + "=" + sid})
		return st == 200
	}
	deleted := func(c *http.Cookie) bool { return c != nil && c.MaxAge < 0 && c.Value == "" }

	// over HTTPS the browser carries both cookies: both sessions end, both cookies are deleted
	s1 := login(map[string]string{"X-Forwarded-Proto": "https"})[SecureCookieName].Value
	s2 := login(nil)[CookieName].Value
	st, cookies := cookieExchange(t, srv, "POST", "/api/auth/v1/logout", "", httpsHdr(SecureCookieName+"="+s1+"; "+CookieName+"="+s2))
	if st != 200 || !deleted(cookies[SecureCookieName]) || !cookies[SecureCookieName].Secure || !deleted(cookies[CookieName]) {
		t.Fatalf("HTTPS logout: %d %v", st, cookies)
	}
	// task 259: the gate cookies go with them, and the pre-259 cookie at Path=/ as well
	if !deleted(cookies[SecureGateCookieName]) || cookies[SecureGateCookieName].Path != GateCookiePath || !deleted(cookies[GateCookieName]) {
		t.Fatalf("HTTPS logout, gate cookies: %v", cookies)
	}
	if paths := setCookiePaths(t, srv, "POST", "/api/auth/v1/logout", "", httpsHdr(SecureCookieName+"="+login(map[string]string{"X-Forwarded-Proto": "https"})[SecureCookieName].Value), SecureCookieName); paths != "/,/api" {
		t.Fatalf("HTTPS logout deletes %s at %q, want / and /api", SecureCookieName, paths)
	}
	if alive(SecureCookieName, s1) || alive(CookieName, s2) {
		t.Fatal("a session outlived the HTTPS logout")
	}

	// over HTTP only the HTTP cookie arrives: its session ends, both deletions are sent (the
	// browser drops the HTTPS one), and the HTTPS session, which the request did not carry, stays
	s1 = login(map[string]string{"X-Forwarded-Proto": "https"})[SecureCookieName].Value
	s2 = login(nil)[CookieName].Value
	st, cookies = cookieExchange(t, srv, "POST", "/api/auth/v1/logout", "", map[string]string{"Cookie": CookieName + "=" + s2})
	if st != 200 || !deleted(cookies[CookieName]) || cookies[CookieName].Secure || !deleted(cookies[SecureCookieName]) {
		t.Fatalf("HTTP logout: %d %v", st, cookies)
	}
	if alive(CookieName, s2) || !alive(SecureCookieName, s1) {
		t.Fatal("HTTP logout: wrong sessions ended")
	}

	// a stale HTTP cookie - above all one set Secure before the two names existed - is deleted
	// over HTTPS, on the shell's first state request and on a login; a live one stays
	for _, tc := range []struct {
		name, path, body string
		hdr              map[string]string
		wantDeleted      bool
		wantSecureSet    bool
	}{
		{"state over HTTPS, stale HTTP cookie", "/api/auth/v1/state", "", httpsHdr(CookieName + "=STALE00000"), true, false},
		// (task 259: a live cookie session gets its cookies set again on every state call, so the
		// Secure one is set here too)
		{"state over HTTPS, live HTTP cookie", "/api/auth/v1/state", "", httpsHdr(CookieName + "=" + s1), false, true},
		{"login over HTTPS, stale HTTP cookie", "/api/auth/v1/login", creds, httpsHdr(CookieName + "=STALE00000"), true, true},
		{"login over HTTPS, live HTTP cookie", "/api/auth/v1/login", creds, httpsHdr(CookieName + "=" + s1), false, true},
		{"state over HTTP, stale HTTP cookie", "/api/auth/v1/state", "", map[string]string{"Cookie": CookieName + "=STALE00000"}, true, false},
	} {
		method := "GET"
		if tc.body != "" {
			method = "POST"
		}
		st, cookies := cookieExchange(t, srv, method, tc.path, tc.body, tc.hdr)
		c := cookies[CookieName]
		if st != 200 || deleted(c) != tc.wantDeleted || (c != nil && !deleted(c)) {
			t.Errorf("%s: %d, HTTP cookie %v", tc.name, st, c)
		}
		if set := cookies[SecureCookieName] != nil && cookies[SecureCookieName].Value != ""; set != tc.wantSecureSet {
			t.Errorf("%s: HTTPS cookie %v", tc.name, cookies[SecureCookieName])
		}
	}
	// over HTTP nothing is said about the HTTPS cookie except on logout
	if _, cookies := cookieExchange(t, srv, "GET", "/api/auth/v1/state", "", map[string]string{"Cookie": SecureCookieName + "=STALE00000"}); cookies[SecureCookieName] != nil {
		t.Errorf("state over HTTP touched the HTTPS cookie: %v", cookies)
	}
}

// setCookiePaths lists, sorted, the paths at which a response sets or deletes the cookie called name.
func setCookiePaths(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string, name string) string {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	shellHeader(req, hdr)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var paths []string
	for _, c := range res.Cookies() {
		if c.Name == name {
			paths = append(paths, c.Path)
		}
	}
	sort.Strings(paths)
	return strings.Join(paths, ",")
}

// openccu-lite task 259: a browser from before the two cookies existed holds the session's name
// at Path=/. The shell's first call, GET /state, moves it: the answer deletes that cookie and
// sets the session cookie at /api and the gate cookie at /addons/ - and so does a login. A
// session that came as a Bearer gets no cookie from /state.
func TestStateMovesTheCookie(t *testing.T) {
	srv := authServer(t)
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	sid := cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)
	// the login itself: the session cookie set at /api and deleted at /, the gate cookie at /addons/
	if p := setCookiePaths(t, srv, "POST", "/api/auth/v1/login", `{"username":"admin","password":"secret123"}`, nil, CookieName); p != "/,/api" {
		t.Errorf("login sets %s at %q, want / (deleted) and /api", CookieName, p)
	}
	if p := setCookiePaths(t, srv, "POST", "/api/auth/v1/login", `{"username":"admin","password":"secret123"}`, nil, GateCookieName); p != GateCookiePath {
		t.Errorf("login sets %s at %q, want %s", GateCookieName, p, GateCookiePath)
	}
	// a state call on the cookie: the same three
	hdr := map[string]string{"Cookie": CookieName + "=" + sid}
	st, cookies := cookieExchange(t, srv, "GET", "/api/auth/v1/state", "", hdr)
	if st != 200 || cookies[CookieName] == nil || cookies[CookieName].Value != sid || cookies[CookieName].Path != CookiePath || cookies[GateCookieName] == nil || cookies[GateCookieName].Value != sid {
		t.Fatalf("state on the cookie: %d %v", st, cookies)
	}
	if p := setCookiePaths(t, srv, "GET", "/api/auth/v1/state", "", hdr, CookieName); p != "/,/api" {
		t.Errorf("state sets %s at %q, want / (deleted) and /api", CookieName, p)
	}
	// over HTTPS the deletion of the HTTPS name at / is Secure, as the cookie was
	st, cookies = cookieExchange(t, srv, "GET", "/api/auth/v1/state", "", map[string]string{"Cookie": SecureCookieName + "=" + sid, "X-Forwarded-Proto": "https"})
	if st != 200 || cookies[SecureCookieName] == nil || !cookies[SecureCookieName].Secure || cookies[SecureGateCookieName] == nil || !cookies[SecureGateCookieName].Secure {
		t.Fatalf("state over HTTPS: %d %v", st, cookies)
	}
	// a Bearer session gets nothing set
	st, cookies = cookieExchange(t, srv, "GET", "/api/auth/v1/state", "", map[string]string{"Authorization": "Bearer " + sid})
	if st != 200 || len(cookies) != 0 {
		t.Errorf("state on a Bearer: %d %v", st, cookies)
	}
	// nor does an unauthenticated call
	st, cookies = cookieExchange(t, srv, "GET", "/api/auth/v1/state", "", nil)
	if st != 200 || len(cookies) != 0 {
		t.Errorf("state without a session: %d %v", st, cookies)
	}
}

// openccu-lite task 259 (D-78): a state-changing API call whose only credential is a cookie must
// carry X-Occulite-Request; a safe method, a Bearer, a token and a call with the header pass.
func TestRequestHeader(t *testing.T) {
	srv := authServer(t)
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	sid := cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)
	_, tok, _ := do(t, srv, "POST", "/api/auth/v1/tokens", `{"name":"t259","role":"admin"}`, map[string]string{"Cookie": CookieName + "=" + sid})
	token, _ := tok["token"].(string)
	if token == "" {
		t.Fatalf("no token: %v", tok)
	}
	cookie := CookieName + "=" + sid
	body := `{"id":"t259","name":{"en":"x"}}`
	for _, tc := range []struct {
		name   string
		method string
		path   string
		hdr    map[string]string
		want   int
		code   string
	}{
		{"cookie alone, no header", "POST", "/api/meta/v1/enums", map[string]string{"Cookie": cookie, RequestHeader: ""}, 403, "request-header"},
		{"cookie and the header", "POST", "/api/meta/v1/enums", map[string]string{"Cookie": cookie, RequestHeader: "1"}, 201, ""},
		{"cookie, the header with another value", "PATCH", "/api/meta/v1/enums/t259", map[string]string{"Cookie": cookie, RequestHeader: "shell"}, 200, ""},
		{"cookie alone, a safe method", "GET", "/api/meta/v1/snapshot", map[string]string{"Cookie": cookie, RequestHeader: ""}, 200, ""},
		{"cookie and a Bearer of the same session", "PATCH", "/api/meta/v1/enums/t259", map[string]string{"Cookie": cookie, "Authorization": "Bearer " + sid, RequestHeader: ""}, 200, ""},
		{"a Bearer alone", "PATCH", "/api/meta/v1/enums/t259", map[string]string{"Authorization": "Bearer " + sid}, 200, ""},
		{"a token", "PATCH", "/api/meta/v1/enums/t259", map[string]string{"Authorization": "Bearer " + token}, 200, ""},
		{"a token and a stale cookie", "PATCH", "/api/meta/v1/enums/t259", map[string]string{"Cookie": CookieName + "=STALE00000", "Authorization": "Bearer " + token, RequestHeader: ""}, 200, ""},
		{"?sid= is no credential (openccu-lite task 260)", "PATCH", "/api/meta/v1/enums/t259?sid=" + sid, map[string]string{RequestHeader: ""}, 401, "unauthenticated"},
		{"cookie alone, the logout", "POST", "/api/auth/v1/logout", map[string]string{"Cookie": cookie, RequestHeader: ""}, 403, "request-header"},
		{"cookie alone, the login is open", "POST", "/api/auth/v1/login", map[string]string{"Cookie": cookie, RequestHeader: ""}, 200, ""},
	} {
		b := body
		if tc.method == "GET" {
			b = ""
		}
		if tc.path == "/api/auth/v1/login" {
			b = `{"username":"admin","password":"secret123"}`
		}
		if tc.method == "PATCH" {
			b = `{"name":{"en":` + strconv.Quote(tc.name) + `}}` // a new name each time: an unchanged one answers 304
		}
		st, out, _ := do(t, srv, tc.method, tc.path, b, tc.hdr)
		if st != tc.want || (tc.code != "" && out["error"] != tc.code) {
			t.Errorf("%s: %d %v, want %d %s", tc.name, st, out, tc.want, tc.code)
		}
	}
}
