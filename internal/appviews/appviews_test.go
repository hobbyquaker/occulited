package appviews

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func reg(views ...View) Registration {
	return Registration{Group: map[string]string{"de": "History", "en": "History"}, Views: views}
}

func view(id, path string) View {
	return View{ID: id, Title: map[string]string{"de": "Tafel " + id, "en": "Board " + id}, Path: path, Icon: "chart"}
}

// The body history sends (ccu-addon-history internal/appviews), the checks of a registration, and
// the store's life: replace, unchanged, cleared, kept across a restart, dropped with the addon.
func TestStore(t *testing.T) {
	dir := t.TempDir()
	installed := map[string]bool{"history": true, "red": true}
	changes := 0
	newStore := func() *Store {
		return &Store{File: filepath.Join(dir, "addon-views.json"), Installed: func(id string) bool { return installed[id] },
			Segments: func(id string) []string {
				if id == "red" {
					return []string{"redmatic"}
				}
				return nil
			}, Changed: func() { changes++ }}
	}
	s := newStore()
	r1 := reg(view("lab1", "/addons/history/embed/dashboard/lab1"), view("energy", "/addons/history/embed/dashboard/energy"))
	if changed, err := s.Put("history", r1); err != nil || !changed {
		t.Fatalf("put: %v %v", changed, err)
	}
	if changed, err := s.Put("history", r1); err != nil || changed {
		t.Fatalf("the same again: %v %v", changed, err)
	}
	if changes != 1 {
		t.Errorf("changes %d, want 1", changes)
	}
	// a segment of the addon's drop-in is its own
	if _, err := s.Put("red", reg(view("flows", "/addons/redmatic/ui/"))); err != nil {
		t.Fatalf("drop-in segment: %v", err)
	}
	if got := strings.Join(s.PublicSegments(), ","); got != "history,redmatic" {
		t.Errorf("segments %q", got)
	}
	// kept across a restart
	s2 := newStore()
	if e, ok := s2.Get("history"); !ok || len(e.Views) != 2 || e.Views[1].ID != "energy" || e.Group["en"] != "History" {
		t.Fatalf("after a restart: %+v %v", e, ok)
	}
	// a PUT without views and a DELETE clear it, the second time changes nothing
	if changed, err := s2.Put("red", reg()); err != nil || !changed {
		t.Fatalf("empty put: %v %v", changed, err)
	}
	if changed, err := s2.Delete("red"); err != nil || changed {
		t.Fatalf("delete again: %v %v", changed, err)
	}
	// a gone addon's views are dropped
	delete(installed, "history")
	if all := s2.All(); len(all) != 0 {
		t.Fatalf("after the uninstall: %v", all)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "addon-views.json")); strings.Contains(string(b), "lab1") {
		t.Errorf("the file keeps a gone addon: %s", b)
	}
}

func TestValidate(t *testing.T) {
	s := &Store{}
	many := reg()
	for i := 0; i <= MaxViews; i++ {
		many.Views = append(many.Views, view("v"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26)), "/addons/history/x"))
	}
	for name, r := range map[string]Registration{
		"no group":                {Views: []View{view("a", "/addons/history/a")}},
		"group in one language":   {Group: map[string]string{"en": "History"}, Views: []View{view("a", "/addons/history/a")}},
		"group too long":          {Group: map[string]string{"de": strings.Repeat("x", 41), "en": "x"}, Views: []View{view("a", "/addons/history/a")}},
		"another addon's segment": reg(view("a", "/addons/hmm/")),
		"a look-alike segment":    reg(view("a", "/addons/history2/x")),
		"outside /addons/":        reg(view("a", "/api/system/v1/x")),
		"a full URL":              reg(view("a", "https://evil.example/addons/history/")),
		"a query":                 reg(view("a", "/addons/history/x?y=1")),
		"a dot segment":           reg(view("a", "/addons/history/../hmm/")),
		"an encoded slash":        reg(view("a", "/addons/history/%2e%2e%2fhmm")),
		"a quote":                 reg(view("a", `/addons/history/"x`)),
		"a bad id":                reg(view("a b", "/addons/history/a")),
		"a duplicate id":          reg(view("a", "/addons/history/a"), view("a", "/addons/history/b")),
		"a bad icon":              reg(View{ID: "a", Title: map[string]string{"de": "a", "en": "a"}, Path: "/addons/history/a", Icon: "<svg>"}),
		"a title with a newline":  reg(View{ID: "a", Title: map[string]string{"de": "a\nb", "en": "a"}, Path: "/addons/history/a"}),
		"too many":                many,
	} {
		if _, err := s.Put("history", r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Put("../x", reg(view("a", "/addons/history/a"))); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad addon id: %v", err)
	}
	if all := s.All(); len(all) != 0 {
		t.Errorf("a refusal stored something: %v", all)
	}
}

// The gate's mirror: one empty file per segment while the public mode is on, none while off;
// stale ones go, a shape the gate would not look up is never written.
func TestSyncPublicMirror(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// the daemon runs with UMask=0077: the file must still be 0644 for lighttpd
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	if err := os.WriteFile(filepath.Join(dir, "redmatic"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncPublicMirror(dir, true, []string{"history", "Bad/../x", "redmatic"}); err != nil {
		t.Fatal(err)
	}
	names := func() string {
		es, _ := os.ReadDir(dir)
		var n []string
		for _, e := range es {
			n = append(n, e.Name())
		}
		return strings.Join(n, ",")
	}
	if got := names(); got != "history,redmatic" {
		t.Fatalf("on: %q", got)
	}
	for _, n := range []string{"history", "redmatic"} {
		if st, _ := os.Stat(filepath.Join(dir, n)); st.Mode().Perm() != 0o644 || st.Size() != 0 {
			t.Errorf("%s: %v %d", n, st.Mode(), st.Size())
		}
	}
	if err := SyncPublicMirror(dir, false, []string{"history"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); got != "" {
		t.Fatalf("off: %q", got)
	}
	if err := SyncPublicMirror("", true, []string{"x"}); err != nil {
		t.Fatal(err)
	}
}
