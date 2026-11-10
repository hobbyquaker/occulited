package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/system"
)

// occulited task 139: every backup handed out is recorded with the way it left - a download in
// the browser with the account, a token's (occulite-backup's) with the program's name - and GET
// /backup/last answers the newest per way.
func TestBackupLastRecordsTheWay(t *testing.T) {
	root := fakeRoot(t)
	_ = os.MkdirAll(string(root)+system.BackupDir, 0o755)
	store := &backupcrypt.Store{Dir: t.TempDir()}
	api := &SystemAPI{Root: root, BackupCrypt: store,
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if strings.HasSuffix(name, "/bin/createBackup.sh") {
				return nil, os.WriteFile(args[0], []byte("the backup"), 0o644)
			}
			return nil, nil
		},
		TokenClient: func(name string) string {
			if name == "nas-paired" {
				return "occulite-backup on nas"
			}
			return ""
		}}
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := &auth.Session{ID: "s", User: "admin", Role: auth.RoleAdmin, Scopes: auth.RoleScopes(auth.RoleAdmin)}
		if tok := r.Header.Get("X-Test-Token"); tok != "" {
			s = &auth.Session{ID: auth.TokenSessionPrefix + tok, User: auth.TokenSessionPrefix + tok, Scopes: auth.Scopes{auth.ScopeBackup, auth.ScopeSystemRead}}
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, s)))
	}))
	t.Cleanup(srv.Close)
	get := func(path, token string) (int, []byte) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/system/v1"+path, nil)
		if token != "" {
			req.Header.Set("X-Test-Token", token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	last := func() map[string]LastBackup {
		st, b := get("/backup/last", "")
		if st != 200 {
			t.Fatalf("last: %d %s", st, b)
		}
		var m map[string]LastBackup
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := last(); len(m) != 0 {
		t.Fatalf("nothing handed out yet: %v", m)
	}
	// a download in the browser, unencrypted (encryption off): recorded all the same
	if st, b := get("/backup", ""); st != 200 || string(b) != "the backup" {
		t.Fatalf("download: %d %q", st, b)
	}
	m := last()
	if d, ok := m["download"]; !ok || d.By != "admin" || d.Encrypted || d.Size != int64(len("the backup")) || !strings.HasSuffix(d.Name, ".sbk") {
		t.Fatalf("download: %+v", m)
	}
	if _, ok := m["client"]; ok {
		t.Fatal("a browser download counted as a client's")
	}
	// a paired client: its label; a console token: its name
	if st, _ := get("/backup", "nas-paired"); st != 200 {
		t.Fatalf("client download: %d", st)
	}
	if c := last()["client"]; c.By != "occulite-backup on nas" {
		t.Fatalf("paired client: %+v", c)
	}
	if st, _ := get("/backup", "cron"); st != 200 {
		t.Fatalf("token download: %d", st)
	}
	m = last()
	if c := m["client"]; c.By != "cron" {
		t.Fatalf("console token: %+v", c)
	}
	if m["download"].By != "admin" {
		t.Fatalf("the browser's entry changed: %+v", m["download"])
	}
	// the store not wired: 501, as the encryption routes
	api.BackupCrypt = nil
	if st, _ := get("/backup/last", ""); st != http.StatusNotImplemented {
		t.Fatalf("without a store: %d", st)
	}
}
