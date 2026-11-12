package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// gateName is the name of a session's file in the gate's mirror: the sha256 of its id (B-102).
func gateName(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:])
}

// sessionServer is occulited's auth API over a state directory with the session store, and its
// mirror in mirror - one process; a second call over the same dir is the next.
func sessionServer(t *testing.T, dir, mirror string) (*httptest.Server, *auth.Store) {
	t.Helper()
	store, err := auth.Open(dir, auth.Options{SessionDir: mirror, SessionFile: filepath.Join(dir, "sessions", "sessions.json"), RestoreMethods: []string{auth.MethodPassword}})
	if err != nil {
		t.Fatal(err)
	}
	a := &AuthAPI{Store: store}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv, store
}

func TestSessionSurvivesRestartOverHTTP(t *testing.T) {
	dir := t.TempDir()
	srv1, store1 := sessionServer(t, dir, filepath.Join(dir, "run1"))
	if err := store1.Setup("admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	sid := cookieOf(t, srv1, `{"username":"admin","password":"secret123"}`)
	store1.Close()
	srv1.Close()

	// a reboot: a new process, an empty tmpfs; the cookie and a bearer header still work, and ?sid=
	// is refused as before the restart (openccu-lite task 260)
	srv2, _ := sessionServer(t, dir, filepath.Join(dir, "run2"))
	for name, hdr := range map[string]map[string]string{
		"cookie":        {"Cookie": CookieName + "=" + sid},
		"HTTPS cookie":  {"Cookie": SecureCookieName + "=" + sid, "X-Forwarded-Proto": "https"},
		"bearer header": {"Authorization": "Bearer " + sid},
	} {
		st, out, _ := do(t, srv2, "GET", "/api/auth/v1/state", "", hdr)
		if st != 200 || out["authenticated"] != true || out["user"] != "admin" || out["sid"] != sid {
			t.Errorf("%s after the restart: %d %v", name, st, out)
		}
	}
	if st, out, _ := do(t, srv2, "GET", "/api/auth/v1/sessions?sid=@"+sid+"@", "", nil); st != 401 || out["message"] != queryCredentialMessage {
		t.Errorf("?sid= after the restart: %d %v", st, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "run2", gateName(sid))); err != nil {
		t.Errorf("the gate's file after the restart: %v", err)
	}

	// a logout is written: the next process does not know the session
	if st, _, _ := do(t, srv2, "POST", "/api/auth/v1/logout", "", map[string]string{"Cookie": CookieName + "=" + sid}); st != 200 {
		t.Fatalf("logout: %d", st)
	}
	srv3, _ := sessionServer(t, dir, filepath.Join(dir, "run3"))
	if st, out, _ := do(t, srv3, "GET", "/api/auth/v1/state", "", map[string]string{"Cookie": CookieName + "=" + sid}); st != 200 || out["authenticated"] != false {
		t.Errorf("after logout and restart: %d %v", st, out)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "run3")); len(entries) != 0 {
		t.Errorf("mirror after logout and restart: %v", entries)
	}
}

// The session list names sessions by handle, never by id, and ends one by its handle.
func TestSessionListHandles(t *testing.T) {
	dir := t.TempDir()
	srv, store := sessionServer(t, dir, filepath.Join(dir, "run"))
	_ = store.Setup("admin", "secret123")
	_ = store.CreateUser("bob", "bobsecret1", auth.RoleUser, false)
	a := cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)
	a2 := cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)
	b := cookieOf(t, srv, `{"username":"bob","password":"bobsecret1"}`)
	as := map[string]string{"Cookie": CookieName + "=" + a}
	bs := map[string]string{"Cookie": CookieName + "=" + b}
	handle := regexp.MustCompile(`^[0-9a-f]{6}……$`)

	st, out, raw := do(t, srv, "GET", "/api/auth/v1/sessions", "", as)
	if st != 200 || out["current"] != auth.SessionHandle(a) || len(out["sessions"].([]any)) != 2 {
		t.Fatalf("own sessions: %d %s", st, raw)
	}
	for _, id := range []string{a, a2, b, gateName(a), gateName(a2)} {
		if strings.Contains(raw, id) {
			t.Errorf("the list hands out %s: %s", id, raw)
		}
	}
	for _, s := range out["sessions"].([]any) {
		if m := s.(map[string]any); !handle.MatchString(m["id"].(string)) || m["method"] != auth.MethodPassword {
			t.Errorf("listed %v", m)
		}
	}
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/sessions?all=true", "", as); len(out["sessions"].([]any)) != 3 {
		t.Errorf("all sessions: %v", out)
	}

	end := func(h string, hdr map[string]string) (int, any) {
		st, out, _ := do(t, srv, "DELETE", "/api/auth/v1/sessions/"+url.PathEscape(h), "", hdr)
		return st, out["ended"]
	}
	// task 66: ending a listed session is self-service - a user's own sessions only, so
	// another's handle ends nothing
	if st, n := end(auth.SessionHandle(a2), bs); st != 200 || n != float64(0) {
		t.Errorf("a user ending another's session: %d %v", st, n)
	}
	// none of these may name a session other than the caller's own. The upper-case probe needs a
	// handle with a letter in it: one of only digits (about one in 17) is its own upper case and
	// does name a session (B-154, occulited B-1)
	notHandles := []string{auth.SessionHandle(a), "……", auth.SessionHandle(b)[:4], a2, gateName(b)}
	for _, h := range []string{auth.SessionHandle(b), auth.SessionHandle(a2)} {
		if up := strings.ToUpper(h); up != h {
			notHandles = append(notHandles, up)
			break
		}
	}
	for _, h := range notHandles {
		if st, n := end(h, as); st != 200 || n != float64(0) {
			t.Errorf("DELETE %q ended %v (%d)", h, n, st)
		}
	}
	if st, n := end(auth.SessionHandle(b), as); st != 200 || n != float64(1) {
		t.Fatalf("an administrator ending bob's session: %v (%d)", n, st)
	}
	if st, n := end(strings.TrimSuffix(auth.SessionHandle(a2), "……"), as); st != 200 || n != float64(1) {
		t.Fatalf("a handle without its dots: %v (%d)", n, st)
	}
	srv2, _ := sessionServer(t, dir, filepath.Join(dir, "run2"))
	for id, want := range map[string]bool{a: true, a2: false, b: false} {
		if _, out, _ := do(t, srv2, "GET", "/api/auth/v1/state", "", map[string]string{"Cookie": CookieName + "=" + id}); out["authenticated"] != want {
			t.Errorf("after the restart %s authenticated %v, want %v", id, out["authenticated"], want)
		}
	}
}
