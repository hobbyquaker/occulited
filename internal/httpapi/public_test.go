package httpapi

import (
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
