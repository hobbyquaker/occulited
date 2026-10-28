package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/meta"
)

// task 193: the Control app's public mode - a request without a session on the App's paths is
// the named principal at operate, everything else stays behind the login, and the switch is in
// force at once and written to the configuration file.
func TestPublicMode(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup("admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "occulited.json")
	if err := config.Save(cfgPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	ms, _ := meta.New(nil, nil)
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store, ConfigFile: cfgPath}
	a.Register(mux)
	(&MetaAPI{Store: ms}).Register(mux)
	sys := &SystemAPI{Root: fakeRoot(t), Public: a}
	sys.WarningTracker(filepath.Join(dir, "warnings.json"), nil)
	sys.Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)

	// off: the App's paths need a login like everything else, and state says so
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", nil); st != 401 {
		t.Fatalf("snapshot with public off: %d", st)
	}
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", nil); out["authenticated"] != false {
		t.Fatalf("state with public off: %v", out)
	}

	// on: the metadata's reads, the state as the guest, and nothing of the administration
	if err := a.SetPublic(true, "guest"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", nil); st != 200 {
		t.Fatalf("snapshot with public on: %d", st)
	}
	_, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", nil)
	if out["authenticated"] != true || out["user"] != "guest" || out["public"] != true || out["level"] != "operate" || out["sid"] != nil {
		t.Fatalf("state with public on: %v", out)
	}
	if st, _, _ := do(t, srv, "PATCH", "/api/meta/v1/objects/x", `{"name":"y"}`, nil); st != 401 {
		t.Fatalf("a write to the metadata without a session: %d", st)
	}
	if st, _, _ := do(t, srv, "GET", "/api/system/v1/status", "", nil); st != 401 {
		t.Fatalf("the status without a session: %d", st)
	}
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/me/preferences", "", nil); st != 200 {
		t.Fatalf("the guest's preferences: %d", st)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/auth/v1/me/preferences", `{"start_page":"app"}`, nil); st != 401 {
		t.Fatalf("a write to the guest's preferences: %d", st)
	}
	// occulited task 73: the guest's display names are read like its preferences, never written
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/me/display-names", "", nil); st != 200 {
		t.Fatalf("the guest's display names: %d", st)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/auth/v1/me/display-names/x", `{"name":"y"}`, nil); st != 401 {
		t.Fatalf("a write to the guest's display names: %d", st)
	}
	// occulited task 125: the addons' views are listed for the public App too
	if st, out, _ := do(t, srv, "GET", "/api/system/v1/app-views", "", nil); st != 200 || out["groups"] == nil {
		t.Fatalf("the App's views for the guest: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/users", "", nil); st != 401 {
		t.Fatalf("the users without a session: %d", st)
	}
	// the warning, and the switch in the file
	if _, out, _ := do(t, srv, "GET", "/api/system/v1/warnings", "", map[string]string{"Authorization": "Bearer " + cookieOf(t, srv, `{"username":"admin","password":"correct horse battery"}`)}); warningByID(out, "app-public") == nil {
		t.Fatalf("no app-public warning: %v", out)
	}
	b, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(b), `"enabled": true`) || !strings.Contains(string(b), `"account": "guest"`) {
		t.Fatalf("the file: %s", b)
	}
	// an administrator as the public account is refused; off again is off at once
	if err := a.SetPublic(true, "admin"); err == nil {
		t.Fatal("an administrator became the public account")
	}
	if err := a.SetPublic(false, "guest"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := do(t, srv, "GET", "/api/meta/v1/snapshot", "", nil); st != 401 {
		t.Fatalf("snapshot after off: %d", st)
	}
}

// occulited B-123: in the public mode a request that presents an API token which does not
// validate - an addon's token from before occulited minted them anew - is answered 401 on every
// public path, never taken for the guest (who got 403 on the event stream, so the addon kept its
// stale token). A valid token is itself, a request without a credential and one with a stale
// session id (the shell's Bearer after the session's end) stay the guest.
func TestPublicModeStaleToken(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup("admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "occulited.json")
	if err := config.Save(cfgPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	ms, _ := meta.New(nil, nil)
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store, ConfigFile: cfgPath}
	a.Register(mux)
	(&MetaAPI{Store: ms}).Register(mux)
	// the rpc routes are another package's: a stand-in that answers who the caller is
	route(mux, auth.ScopeRPCRead, "GET /api/rpc/v1/events", func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFrom(r)
		if sess == nil {
			w.WriteHeader(500)
			return
		}
		writeJSON(w, 200, map[string]any{"user": sess.User, "method": sess.Method})
	})
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	if err := a.SetPublic(true, "guest"); err != nil {
		t.Fatal(err)
	}
	valid, err := store.CreateToken("history", auth.TokenOptions{Scopes: auth.Scopes{auth.ScopeMetaRead, auth.ScopeRPCRead}})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.CreateToken("old", auth.TokenOptions{Scopes: auth.Scopes{auth.ScopeMetaRead, auth.ScopeRPCRead}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteToken("old"); err != nil {
		t.Fatal(err)
	}
	staleSID := cookieOf(t, srv, `{"username":"admin","password":"correct horse battery"}`)
	if st, _, raw := do(t, srv, "POST", "/api/auth/v1/logout", "{}", map[string]string{"Authorization": "Bearer " + staleSID}); st >= 300 {
		t.Fatalf("logout: %d %s", st, raw)
	}
	unknown := "olt_" + strings.Repeat("0", 32)
	cases := []struct {
		name, path string
		hdr        map[string]string
		want       int
		user       string
	}{
		{"no credential, rpc", "/api/rpc/v1/events", nil, 200, "guest"},
		{"no credential, meta", "/api/meta/v1/snapshot", nil, 200, ""},
		{"valid token", "/api/rpc/v1/events", map[string]string{"Authorization": "Bearer " + valid}, 200, "token:history"},
		{"stale token as Bearer, rpc", "/api/rpc/v1/events", map[string]string{"Authorization": "Bearer " + stale}, 401, ""},
		{"stale token as Bearer, meta", "/api/meta/v1/snapshot", map[string]string{"Authorization": "Bearer " + stale}, 401, ""},
		{"unknown token as Bearer", "/api/rpc/v1/events", map[string]string{"Authorization": "Bearer " + unknown}, 401, ""},
		{"stale token as the basic password", "/api/rpc/v1/events", map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+stale))}, 401, ""},
		{"stale token as ?sid=", "/api/rpc/v1/events?sid=" + stale, nil, 401, ""},
		{"stale session id as Bearer stays the guest", "/api/rpc/v1/events", map[string]string{"Authorization": "Bearer " + staleSID}, 200, "guest"},
		{"stale session id as ?sid= stays the guest", "/api/rpc/v1/events?sid=@" + staleSID + "@", nil, 200, "guest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, out, raw := do(t, srv, "GET", c.path, "", c.hdr)
			if st != c.want {
				t.Fatalf("%s: %d %s", c.path, st, raw)
			}
			if c.user != "" && out["user"] != c.user {
				t.Fatalf("%s: user %v, want %s", c.path, out["user"], c.user)
			}
		})
	}
	// the state is open: a stale token there still sees the public mode's guest
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/state", "", map[string]string{"Authorization": "Bearer " + stale}); out["public"] != true {
		t.Fatalf("state with a stale token: %v", out)
	}
}
