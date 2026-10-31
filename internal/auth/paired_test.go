package auth

import (
	"errors"
	"testing"
	"time"
)

// task 219: a paired token - the name from the program, -2 on a collision, the client record,
// never a scope pairing does not grant; the label and narrowing; rotation with a grace minute;
// the last address.
func TestPairedTokens(t *testing.T) {
	now := time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC)
	s, err := Open(t.TempDir(), Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	c := TokenClient{App: "hm2mqtt.js", Instance: "nas", Label: "hm2mqtt.js on nas", PairedAt: now, PairedBy: "admin", Address: "192.168.1.50"}
	name, secret, err := s.CreatePairedToken("hm2mqtt-js-nas", Scopes{ScopeRPCOperate, ScopeMetaRead}, nil, c)
	if err != nil || name != "hm2mqtt-js-nas" {
		t.Fatalf("%v %s", err, name)
	}
	name2, _, err := s.CreatePairedToken("hm2mqtt-js-nas", Scopes{ScopeMetaRead}, nil, c)
	if err != nil || name2 != "hm2mqtt-js-nas-2" {
		t.Fatalf("collision: %v %s", err, name2)
	}
	if _, _, err := s.CreatePairedToken("x-power", Scopes{ScopePower}, nil, c); !errors.Is(err, ErrBadScope) {
		t.Fatalf("power granted: %v", err)
	}
	sess := s.ValidateFrom(secret, "192.168.1.77")
	if sess == nil || !sess.Has(ScopeRPCOperate) {
		t.Fatal("the paired token does not validate")
	}
	var tok Token
	for _, x := range s.Tokens() {
		if x.Name == name {
			tok = x
		}
	}
	if tok.Client == nil || tok.Client.LastAddress != "192.168.1.77" || tok.Client.PairedBy != "admin" || tok.Hash != "" {
		t.Fatalf("record %+v", tok)
	}
	label := "  MQTT bridge  "
	up, err := s.UpdateToken(name, &label, []string{"meta:read"})
	if err != nil || up.Client.Label != "MQTT bridge" || len(up.Scopes) != 1 {
		t.Fatalf("update %v %+v", err, up)
	}
	if _, err := s.UpdateToken(name, nil, []string{"rpc:admin"}); !errors.Is(err, ErrWiden) {
		t.Fatalf("widened: %v", err)
	}
	if _, err := s.UpdateToken("nope", &label, nil); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("unknown: %v", err)
	}
	fresh, err := s.RotateToken(name, time.Minute)
	if err != nil || fresh == secret {
		t.Fatalf("rotate %v", err)
	}
	if s.ValidateFrom(fresh, "") == nil || s.ValidateFrom(secret, "") == nil {
		t.Fatal("both secrets valid within the grace minute")
	}
	now = now.Add(2 * time.Minute)
	if s.ValidateFrom(secret, "") != nil || s.ValidateFrom(fresh, "") == nil {
		t.Fatal("the old secret after the grace minute")
	}
	for _, x := range s.Tokens() {
		if x.PrevHash != "" || x.PrevUntil != nil {
			t.Fatalf("the list carries the previous hash: %+v", x)
		}
	}
}

// occulited task 135: pairing grants backup only limited to an address, and never what stays out.
func TestPairedTokenBackup(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := TokenClient{App: "occulite-backup", Label: "occulite-backup", Address: "192.168.1.50"}
	if _, _, err := s.CreatePairedToken("no-range", Scopes{ScopeBackup}, nil, c); !errors.Is(err, ErrBadScope) {
		t.Fatalf("backup without a range: %v", err)
	}
	if _, _, err := s.CreatePairedToken("bad-range", Scopes{ScopeBackup}, []string{"nope"}, c); !errors.Is(err, ErrBadIPRange) {
		t.Fatalf("a bad range: %v", err)
	}
	for _, never := range []Scope{ScopeAll, ScopeAuthAdmin, ScopeRadioKeys, ScopePower} {
		if _, _, err := s.CreatePairedToken("never", Scopes{never}, []string{"192.168.1.50/32"}, c); !errors.Is(err, ErrBadScope) {
			t.Fatalf("%s granted: %v", never, err)
		}
	}
	_, secret, err := s.CreatePairedToken("occulite-backup", Scopes{ScopeBackup}, []string{"192.168.1.50/32"}, c)
	if err != nil {
		t.Fatal(err)
	}
	if sess := s.ValidateFrom(secret, "192.168.1.50"); sess == nil || !sess.Has(ScopeBackup) {
		t.Fatal("the token from its own address")
	}
	if s.ValidateFrom(secret, "192.168.1.51") != nil {
		t.Fatal("the token from another address")
	}
}
