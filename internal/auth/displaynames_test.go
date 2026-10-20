package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// occulited task 73: each account keeps its own display names; a blank name removes one.
func TestDisplayNames(t *testing.T) {
	s, dir := open(t)
	if err := s.Setup("admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("bob", "secret123", RoleUser, false); err != nil {
		t.Fatal(err)
	}
	if m, ok := s.DisplayNames("bob"); !ok || m == nil || len(m) != 0 {
		t.Fatalf("fresh account: %v %v", m, ok)
	}
	if m, ok := s.DisplayNames("anonymous"); ok || m == nil || len(m) != 0 {
		t.Fatalf("no account: %v %v", m, ok)
	}
	if err := s.SetDisplayName("anonymous", "BidCos-RF.ABC0000001:1", "x"); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("no account: %v", err)
	}

	const ch = "BidCos-RF.ABC0000001:1"
	if err := s.SetDisplayName("bob", ch, "  Leselampe  "); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisplayName("admin", ch, "Lampe links"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.DisplayNames("bob"); m[ch] != "Leselampe" || len(m) != 1 {
		t.Fatalf("bob: %v", m)
	}
	if m, _ := s.DisplayNames("admin"); m[ch] != "Lampe links" {
		t.Fatalf("admin: %v", m)
	}
	// the copy is a copy
	m, _ := s.DisplayNames("bob")
	m[ch] = "changed"
	if m2, _ := s.DisplayNames("bob"); m2[ch] != "Leselampe" {
		t.Fatalf("the map leaked: %v", m2)
	}
	// kept in users.json, across a reopen
	raw, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil || !strings.Contains(string(raw), `"display_names"`) {
		t.Fatalf("users.json: %v %s", err, raw)
	}
	s2, err := Open(dir, Options{SessionDir: filepath.Join(dir, "sessions2")})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := s2.DisplayNames("bob"); m[ch] != "Leselampe" {
		t.Fatalf("reopened: %v", m)
	}
	// the same name again changes nothing; refused keys and names
	if err := s.SetDisplayName("bob", ch, "Leselampe"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{{"", "x"}, {"a b", "x"}, {"a/b", "x"}, {strings.Repeat("a", maxDisplayKeyLen+1), "x"}, {ch, strings.Repeat("ä", maxDisplayNameLen+1)}, {ch, "a\nb"}} {
		if err := s.SetDisplayName("bob", bad[0], bad[1]); !errors.Is(err, ErrBadDisplayName) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	// clearing: blank removes it, the last one removes the field; clearing a missing one is fine
	if err := s.SetDisplayName("bob", ch, "   "); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisplayName("bob", "BidCos-RF.ABC0000002:1", ""); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.DisplayNames("bob"); len(m) != 0 {
		t.Fatalf("cleared: %v", m)
	}
	if u := s.users["bob"]; u.DisplayNames != nil {
		t.Fatalf("an empty map is kept: %v", u.DisplayNames)
	}
	// the list of accounts does not carry them
	for _, a := range s.Users() {
		if a.DisplayNames != nil {
			t.Fatalf("%s listed with display names", a.Name)
		}
	}
	// bounded
	s.users["bob"].DisplayNames = map[string]string{}
	for i := 0; i < maxDisplayNames; i++ {
		s.users["bob"].DisplayNames[strings.Repeat("k", 1+i%50)+string(rune('A'+i/50))] = "n"
	}
	if len(s.users["bob"].DisplayNames) != maxDisplayNames {
		t.Fatalf("setup: %d", len(s.users["bob"].DisplayNames))
	}
	if err := s.SetDisplayName("bob", "one-more", "n"); !errors.Is(err, ErrBadDisplayName) {
		t.Fatalf("over the bound: %v", err)
	}
}
