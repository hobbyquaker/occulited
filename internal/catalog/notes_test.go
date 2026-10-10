package catalog

import (
	"testing"

	"github.com/hobbyquaker/occulited/internal/manifest"
)

// task 26: the release notes beside an offered update - the manifest's changelog first, then the
// release's page, and nothing (never a dead link) without either.
func TestNotesURL(t *testing.T) {
	m := &manifest.Manifest{ID: "x"}
	cases := []struct {
		name string
		it   Item
		want string
	}{
		{"nothing known", Item{}, ""},
		{"release page", Item{Manifest: m, Latest: &Latest{Version: "1.0.0", Notes: "https://github.com/o/x/releases/tag/v1.0.0"}}, "https://github.com/o/x/releases/tag/v1.0.0"},
		{"changelog wins", Item{Manifest: &manifest.Manifest{ID: "x", Changelog: "https://example.org/CHANGELOG.md#100"}, Latest: &Latest{Notes: "https://github.com/o/x/releases/tag/v1.0.0"}}, "https://example.org/CHANGELOG.md#100"},
		{"changelog without a release", Item{Manifest: &manifest.Manifest{ID: "x", Changelog: "https://example.org/c"}}, "https://example.org/c"},
		{"not a web URL", Item{Manifest: &manifest.Manifest{ID: "x", Changelog: "javascript:alert(1)"}, Latest: &Latest{Notes: "ftp://x"}}, ""},
		{"no manifest yet", Item{Latest: &Latest{Notes: "https://github.com/o/x/releases/tag/v1"}}, "https://github.com/o/x/releases/tag/v1"},
	}
	for _, c := range cases {
		if got := c.it.NotesURL(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
