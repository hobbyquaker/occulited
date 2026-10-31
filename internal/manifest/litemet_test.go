package manifest

import (
	"errors"
	"strings"
	"testing"
)

// occulited task 134: strict semver, an addon names the first build that has what it needs.
func TestLiteMet(t *testing.T) {
	for _, c := range []struct {
		min, running string
		want         bool
	}{
		{"", "1.0.0", true},
		{"1.1.0-dev.6", "", true},
		{"1.1.0-dev.6", "1.1.0-dev.6", true},
		{"1.1.0-dev.6", "1.1.0-dev.7", true},
		{"1.1.0-dev.6", "1.1.0-dev.5", false},
		{"1.1.0-dev.6", "1.1.0", true},
		{"1.1.0-dev.6", "1.1.0-beta.0", true}, // dev < alpha < beta < rc, not ASCII: lite-addon-wireguard and -tailscale name this minimum
		{"1.1.0-beta.0", "1.1.0-dev.9", false},
		{"1.1.0-beta.0", "1.1.0-beta.0", true},
		{"1.1.0-dev.6", "1.0.0", false},
		{"1.1.0", "1.1.0-dev.6", false}, // the decision: a pre-release is older than its release
		{"1.1.0", "1.1.0-rc.1", false},
		{"1.1.0", "1.1.0", true},
		{"1.1", "1.1.0", true},
		{"1.1", "1.0.9", false},
		{"1", "0.9.0", false},
		{"1.1.0+2", "1.1.0", true},
		{"1.1.0-dev.6", "0-beta.2", true}, // no semantic version: nothing to refuse on
		{"not.a.version", "1.0.0", true},
	} {
		m := &Manifest{Requires: Requires{Lite: c.min}}
		if got := m.LiteMet(c.running); got != c.want {
			t.Errorf("minimum %q on %q: %v, want %v", c.min, c.running, got, c.want)
		}
	}
	var none *Manifest
	if !none.LiteMet("1.0.0") || none.CheckLite("1.0.0") != nil {
		t.Error("no manifest refuses nothing")
	}
}

func TestCheckLite(t *testing.T) {
	m := &Manifest{ID: "nut", Requires: Requires{Lite: "1.1.0-dev.6"}}
	err := m.CheckLite("1.0.0")
	var e *LiteTooOldError
	if !errors.As(err, &e) || e.Required != "1.1.0-dev.6" || !strings.Contains(err.Error(), "requires openccu-lite 1.1.0-dev.6 or later") {
		t.Fatalf("%v", err)
	}
}
