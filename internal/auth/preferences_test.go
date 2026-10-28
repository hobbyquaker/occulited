package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreferences(t *testing.T) {
	s, dir := open(t)
	if err := s.Setup("admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("bob", "secret123", RoleUser, false); err != nil {
		t.Fatal(err)
	}

	// nothing yet: the empty set, never a nil list
	p, ok := s.Preferences("bob")
	if !ok || p.Addons == nil || len(p.Addons) != 0 {
		t.Fatalf("fresh account: %+v %v", p, ok)
	}
	// no such account: the empty set and false
	if p, ok := s.Preferences("anonymous"); ok || len(p.Addons) != 0 {
		t.Fatalf("no account: %+v %v", p, ok)
	}
	if err := s.SetPreferences("anonymous", Preferences{}); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("no account: %v", err)
	}

	// occulited task 24: the whole-window flag is kept per entry, with or without the pin
	want := Preferences{Addons: []AddonPreference{{ID: "redmatic", Pinned: true, Fullscreen: true}, {ID: "mh", Fullscreen: true}, {ID: "jp-hb-devices-addon", Pinned: true}}}
	if err := s.SetPreferences("bob", want); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Preferences("bob")
	if !ok || len(got.Addons) != 3 || got.Addons[0] != want.Addons[0] || got.Addons[1] != want.Addons[1] || got.Addons[2] != want.Addons[2] {
		t.Fatalf("stored: %+v", got)
	}
	// the other account is untouched
	if p, _ := s.Preferences("admin"); len(p.Addons) != 0 {
		t.Fatalf("admin: %+v", p)
	}

	// refused: a malformed id, a duplicate, too many
	for _, bad := range []Preferences{
		{Addons: []AddonPreference{{ID: ""}}},
		{Addons: []AddonPreference{{ID: "../x"}}},
		{Addons: []AddonPreference{{ID: "a b"}}},
		{Addons: []AddonPreference{{ID: "mh"}, {ID: "mh", Pinned: true}}},
		{Addons: make([]AddonPreference, maxPreferenceAddons+1)},
	} {
		if err := s.SetPreferences("bob", bad); !errors.Is(err, ErrBadPreferences) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	// a refusal leaves the stored set as it was
	if p, _ := s.Preferences("bob"); len(p.Addons) != 3 {
		t.Fatalf("after refusals: %+v", p)
	}

	// kept in users.json with the account, and read back by a fresh store
	b, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"preferences"`) || !strings.Contains(string(b), `"redmatic"`) {
		t.Fatalf("users.json: %s", b)
	}
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := s2.Preferences("bob"); len(p.Addons) != 3 || !p.Addons[0].Pinned || p.Addons[1].Pinned || !p.Addons[1].Fullscreen || p.Addons[2].Fullscreen {
		t.Fatalf("reopened: %+v", p)
	}
	// the account list never carries them
	for _, u := range s2.Users() {
		if u.Preferences != nil {
			t.Fatalf("Users() carries preferences: %+v", u)
		}
	}
	// the password survives beside them, and they survive a password change
	if err := s2.SetPassword("bob", "another123", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Login("bob", "another123", "127.0.0.1", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := s2.Preferences("bob"); len(p.Addons) != 3 {
		t.Fatalf("after a password change: %+v", p)
	}

	// task 306: Control's tab hidden is a preference of its own; alone it is kept, and the
	// start page beside it is kept as it was (the shell falls back to Status while it is hidden)
	if err := s2.SetPreferences("bob", Preferences{AppHidden: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s2.Preferences("bob"); !p.AppHidden || len(p.Addons) != 0 || p.StartPage != "" {
		t.Fatalf("hidden alone: %+v", p)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "users.json")); !strings.Contains(string(b), `"app_hidden": true`) {
		t.Fatalf("hidden not in users.json: %s", b)
	}
	if err := s2.SetPreferences("bob", Preferences{StartPage: "app", AppHidden: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s2.Preferences("bob"); !p.AppHidden || p.StartPage != "app" {
		t.Fatalf("hidden with the start page: %+v", p)
	}

	// occulited task 101: the full names in Control, alone, is kept as well
	if err := s2.SetPreferences("bob", Preferences{AppFullNames: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s2.Preferences("bob"); !p.AppFullNames || p.AppHidden || p.StartPage != "" {
		t.Fatalf("full names alone: %+v", p)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "users.json")); !strings.Contains(string(b), `"app_full_names": true`) {
		t.Fatalf("full names not in users.json: %s", b)
	}

	// an empty list clears the field, so the account reads as it did before
	if err := s2.SetPreferences("bob", Preferences{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "users.json")); strings.Contains(string(b), `"preferences"`) {
		t.Fatalf("cleared set still in users.json: %s", b)
	}
}

// occulited task 125: the addons whose views an account hides in Control's drawer - kept, each id
// once, refused when malformed; alone they are a set worth keeping.
func TestPreferencesAppViewsHidden(t *testing.T) {
	s, _ := open(t)
	if err := s.Setup("admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPreferences("admin", Preferences{AppViewsHidden: []string{"history", "battery", "history"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Preferences("admin"); len(p.AppViewsHidden) != 2 || p.AppViewsHidden[0] != "history" || p.AppViewsHidden[1] != "battery" {
		t.Fatalf("stored: %+v", p)
	}
	for _, bad := range [][]string{{"../x"}, {""}, make([]string, maxPreferenceAddons+1)} {
		if err := s.SetPreferences("admin", Preferences{AppViewsHidden: bad}); !errors.Is(err, ErrBadPreferences) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := s.SetPreferences("admin", Preferences{}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Preferences("admin"); len(p.AppViewsHidden) != 0 {
		t.Fatalf("cleared: %+v", p)
	}
}
