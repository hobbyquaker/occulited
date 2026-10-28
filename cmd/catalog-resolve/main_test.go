package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/resolved"
)

// The Action's command: a file a system accepts, written whole; a run that resolves nothing (no
// token, no network) writes nothing and fails, so the Action keeps the last good file.
func TestRun(t *testing.T) {
	broken := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken && strings.HasPrefix(r.URL.Path, "/repos/") {
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/repos/o/a/releases":
			_, _ = w.Write([]byte(`[{"tag_name":"v1.0.0","assets":[]}]`))
		case "/repos/o/a", "/repos/o/lite":
			_, _ = w.Write([]byte(`{"stargazers_count": 3}`))
		case "/repos/o/lite/releases":
			_, _ = w.Write([]byte(`[]`))
		case "/o/a/v1.0.0/openccu-lite.json":
			_, _ = w.Write([]byte(`{"format": 1, "id": "a", "name": "A", "release": {"github": "o/a", "asset": "a-{version}.tgz"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	cat := filepath.Join(dir, "catalog.json")
	_ = os.WriteFile(cat, []byte(`{"format":1,"addons":[{"git":"https://github.com/o/a","manifest":"openccu-lite.json"}]}`), 0o644)
	out := filepath.Join(dir, "catalog-resolved.json")
	args := []string{"--catalog", "file://" + cat, "--out", out, "--feed-repo", "o/lite", "--api", srv.URL, "--raw", srv.URL}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	f, err := resolved.Parse(b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rels, ok := f.Releases("o/a", 10); !ok || len(rels) != 1 {
		t.Errorf("o/a: %v %v", rels, ok)
	}
	if rels, ok := f.Releases("o/lite", 100); !ok || len(rels) != 0 {
		t.Errorf("o/lite: %v %v", rels, ok)
	}
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["generated_at"] == nil || raw["catalog"] != "file://"+cat {
		t.Errorf("%v", raw)
	}
	broken = true
	_ = os.Remove(out)
	if err := run(args); err == nil {
		t.Error("a run that resolved nothing succeeded")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a file was written")
	}
}
