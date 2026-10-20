package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/eq3disc"
	"github.com/hobbyquaker/occulited/internal/meta"
	"github.com/hobbyquaker/occulited/internal/system"
)

// fullAPI is every route of the three APIs on one mux behind the middleware, with a store that
// has an administrator; the system half runs its commands dry.
func fullAPI(t *testing.T) (*httptest.Server, *auth.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup("admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	ms, err := meta.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := logRoot(t)
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store}
	a.Register(mux)
	(&MetaAPI{Store: ms, Root: string(r)}).Register(mux)
	// occulited B-87: no real eQ-3 discovery broadcast - each search took seconds under the
	// handler's lock, and a loaded runner ran past the client's deadline
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc, Run: system.DryRunner, LAN: noLAN{}}).Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv, store
}

// fillPattern turns a mux pattern's path into a path a request can carry.
func fillPattern(path string) string {
	return regexp.MustCompile(`\{[a-z_]+(\.\.\.)?\}`).ReplaceAllStringFunc(path, func(m string) string {
		switch strings.Trim(m, "{}") {
		case "ref":
			return "BidCos-RF.X%3A1"
		case "enum":
			return "room"
		case "path...":
			return "eg/bad"
		case "sid":
			return "abcdef"
		case "action":
			return "start"
		}
		return "x"
	})
}

// call sends one request with the credential and answers the status and the body's scope field;
// a stream (SSE) is left as soon as its status is in.
func callScoped(t *testing.T, srv *httptest.Server, method, path, bearer string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body := ""
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodDelete {
		body = "{}"
	}
	req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return res.StatusCode, ""
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var out struct {
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out.Scope
}

// The route table (task 66): every registered route is in it (route() is the only way to
// register one), every entry that is not open is reached with a token of exactly its scope and
// refused with a 403 naming it for a token without, an open one needs no session, and Full
// access reaches everything. The default is deny: a pattern the table does not know answers 403.
func TestRouteTable(t *testing.T) {
	srv, store := fullAPI(t)
	table := RouteScopes()
	if len(table) < 170 {
		t.Fatalf("the table holds %d routes", len(table))
	}
	tokens := map[auth.Scope]string{}
	for _, s := range append([]auth.Scope{auth.ScopeAll}, auth.Grantable...) {
		secret, err := store.CreateToken("t-"+strings.NewReplacer(":", "-", "*", "all").Replace(string(s)), auth.TokenOptions{Scopes: auth.Scopes{s}})
		if err != nil {
			t.Fatal(err)
		}
		tokens[s] = secret
	}
	var patterns []string
	for p := range table {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	seen := map[auth.Scope]bool{}
	for _, pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(path, "/api/auth/v1/") && !strings.HasPrefix(path, "/api/meta/v1/") && !strings.HasPrefix(path, "/api/system/v1/") {
			continue // another test's fixture
		}
		scopes := table[pattern]
		path = fillPattern(path)
		if scopes[0] == scopeOpen {
			if !open(path) {
				t.Errorf("%s: open in the table, not in open()", pattern)
			}
			continue // the middleware passes it by open(); the handler's own answer is its business
		}
		if open(path) {
			t.Errorf("%s: in open(), scoped in the table", pattern)
		}
		if st, _ := callScoped(t, srv, method, path, ""); st != 401 {
			t.Errorf("%s: without a session %d, want 401", pattern, st)
		}
		// past the middleware means anything but its 401 and 403; a handler's own answer to the
		// placeholder path (a 404 for an object that is not there, a 501) is fine
		if st, _ := callScoped(t, srv, method, path, tokens[auth.ScopeAll]); st == 401 || st == 403 {
			t.Errorf("%s: Full access answered %d", pattern, st)
		}
		for _, s := range scopes {
			seen[s] = true
			if s == auth.ScopeSelf {
				continue // no token carries it; auth:admin and Full access include it
			}
			if st, _ := callScoped(t, srv, method, path, tokens[s]); st == 401 || st == 403 {
				t.Errorf("%s: a token with exactly %s answered %d", pattern, s, st)
			}
		}
		// a token that covers none of the route's scopes is refused, and the answer names the
		// route's own scope
		var other auth.Scope
		for _, cand := range auth.Grantable {
			covers := false
			for _, s := range scopes {
				if auth.Covers(cand, s) {
					covers = true
				}
			}
			if !covers {
				other = cand
				break
			}
		}
		if st, scope := callScoped(t, srv, method, path, tokens[other]); st != 403 || scope != string(scopes[0]) {
			t.Errorf("%s: a token with %s answered %d scope %q, want 403 %s", pattern, other, st, scope, scopes[0])
		}
	}
	for _, s := range []auth.Scope{auth.ScopeMetaRead, auth.ScopeMetaWrite, auth.ScopeSystemRead, auth.ScopeLogsRead, auth.ScopeSystemWrite, auth.ScopeAddonsWrite, auth.ScopePower, auth.ScopeBackup, auth.ScopeLED, auth.ScopeRadioKeys, auth.ScopeAuthAdmin, auth.ScopeSelf} {
		if !seen[s] {
			t.Errorf("no route carries %s", s)
		}
	}
	for _, s := range []auth.Scope{auth.ScopeRPCRead, auth.ScopeRPCOperate, auth.ScopeRPCConfigure, auth.ScopeRPCAdmin} {
		if seen[s] {
			t.Errorf("%s has routes before task 77", s)
		}
	}
	// a pattern the table does not know is refused for everybody, Full access included
	if st, _ := callScoped(t, srv, "GET", "/api/system/v1/no-such-route", tokens[auth.ScopeAll]); st != 404 {
		t.Errorf("an unregistered path: %d, want the mux's 404", st)
	}
}

// No route of the package is registered past the table: every registration in the non-test
// sources goes through route().
func TestNoRouteOutsideTheTable(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "scopes.go" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "mux.HandleFunc(") || strings.Contains(string(b), "mux.Handle(") {
			t.Errorf("%s registers a route past the table", name)
		}
	}
}

// The local token, narrowed to meta:read: the snapshot and the stream answer, the journal and
// the network page refuse with the scope named. An admin token (Full access) reaches everything
// a session does, and a user session keeps its self-service writes.
func TestLocalTokenAndRoles(t *testing.T) {
	srv, store := fullAPI(t)
	dir := t.TempDir()
	if err := store.EnsureLocalToken(filepath.Join(dir, "local-token")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "local-token"))
	local := strings.TrimSpace(string(b))
	for _, c := range []struct {
		method, path string
		want         int
		scope        string
	}{
		{"GET", "/api/meta/v1/snapshot", 200, ""},
		{"GET", "/api/meta/v1/events/sse", 200, ""},
		{"GET", "/api/meta/v1/enums", 200, ""},
		{"GET", "/api/system/v1/log", 403, "logs:read"},
		{"GET", "/api/system/v1/log/download", 403, "logs:read"},
		{"GET", "/api/system/v1/network", 403, "system:read"},
		{"GET", "/api/system/v1/status", 403, "system:read"},
		{"GET", "/api/system/v1/addons", 403, "system:read"},
		{"PATCH", "/api/meta/v1/objects/BidCos-RF.X%3A1", 403, "meta:write"},
		{"GET", "/api/auth/v1/sessions", 403, "self"},
		{"GET", "/api/auth/v1/state", 200, ""},
	} {
		if st, scope := callScoped(t, srv, c.method, c.path, local); st != c.want || scope != c.scope {
			t.Errorf("local token %s %s: %d %q, want %d %q", c.method, c.path, st, scope, c.want, c.scope)
		}
	}
	admin, err := store.CreateToken("ci", auth.TokenOptions{Scopes: auth.RoleScopes(auth.RoleAdmin)})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/system/v1/log", "/api/system/v1/network", "/api/auth/v1/users", "/api/auth/v1/tokens", "/api/system/v1/backup/schedule", "/api/system/v1/led/state"} {
		if st, _ := callScoped(t, srv, "GET", path, admin); st == 401 || st == 403 {
			t.Errorf("admin token GET %s: %d", path, st)
		}
	}
	// a user account: the three reads and its own account, nothing of the administrator's
	if err := store.CreateUser("bob", "bobsecret1", auth.RoleUser, false); err != nil {
		t.Fatal(err)
	}
	bob, err := store.Login("bob", "bobsecret1", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, path string
		forbidden    bool
	}{
		{"GET", "/api/system/v1/log", false},
		{"GET", "/api/system/v1/status", false},
		{"GET", "/api/system/v1/led/state", false},
		{"GET", "/api/meta/v1/snapshot", false},
		{"GET", "/api/auth/v1/sessions", false},
		{"PUT", "/api/auth/v1/me/preferences", false},
		{"POST", "/api/auth/v1/ticket", false},
		{"GET", "/api/auth/v1/sessions?all=true", true},
		{"GET", "/api/auth/v1/users", true},
		{"GET", "/api/auth/v1/config", true},
		{"PUT", "/api/system/v1/time", true},
		{"POST", "/api/system/v1/reboot", true},
		{"GET", "/api/system/v1/backup", true},
		{"POST", "/api/system/v1/led/override", true},
		{"PATCH", "/api/meta/v1/objects/BidCos-RF.X%3A1", true},
	} {
		st, _ := callScoped(t, srv, c.method, c.path, bob.ID)
		if c.forbidden && st != 403 {
			t.Errorf("user session %s %s: %d, want 403", c.method, c.path, st)
		}
		if !c.forbidden && (st == 401 || st == 403) {
			t.Errorf("user session %s %s: %d", c.method, c.path, st)
		}
	}
	// the state carries the scopes
	req, _ := http.NewRequest("GET", srv.URL+"/api/auth/v1/state", nil)
	req.Header.Set("Authorization", "Bearer "+bob.ID)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Role   string   `json:"role"`
		Scopes []string `json:"scopes"`
	}
	_ = json.NewDecoder(res.Body).Decode(&state)
	res.Body.Close()
	// task 78 (D-116): a user is operate now - the reads and rpc:operate
	if state.Role != "user" || strings.Join(state.Scopes, ",") != "logs:read,meta:read,rpc:operate,system:read" {
		t.Errorf("state: %+v", state)
	}
}

// noLAN is a LAN that answers nothing, at once (occulited B-87): the route table only asks
// whether a route lets a scope through, never what the discovery finds.
type noLAN struct{}

func (noLAN) Scan(context.Context) ([]eq3disc.Found, error) { return nil, nil }
func (noLAN) Lookup(context.Context, string) (*eq3disc.Found, error) {
	return nil, eq3disc.ErrNoAnswer
}
func (noLAN) Configure(context.Context, netip.Addr, string, string, eq3disc.Config, string) (bool, error) {
	return false, eq3disc.ErrNoAnswer
}
