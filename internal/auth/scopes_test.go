package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// asRole is what the older tests meant by a token's role: the scopes the role stands for.
func asRole(r Role) TokenOptions { return TokenOptions{Scopes: RoleScopes(r)} }

// roleOf names a scope list the way the older tests compared tokens: by the role it came from.
func roleOf(s Scopes) string {
	switch {
	case s.Full():
		return "admin"
	case s.Has(ScopeLogsRead):
		return "user"
	case s.Has(ScopeLED):
		return "led"
	}
	return strings.Join(s.Strings(), "+")
}

// A write includes its read, a tier the ones before it, Full access everything - a scope that
// does not exist yet included - and nothing else leaks across areas.
func TestScopesCover(t *testing.T) {
	for _, tc := range []struct {
		have Scopes
		want Scope
		ok   bool
	}{
		{Scopes{ScopeMetaWrite}, ScopeMetaRead, true},
		{Scopes{ScopeMetaRead}, ScopeMetaWrite, false},
		{Scopes{ScopeSystemWrite}, ScopeSystemRead, true},
		{Scopes{ScopeSystemWrite}, ScopeLED, true},
		{Scopes{ScopeSystemWrite}, ScopeLogsRead, false},
		{Scopes{ScopeSystemRead}, ScopeLogsRead, false},
		{Scopes{ScopeAddonsWrite}, ScopeSystemRead, true},
		{Scopes{ScopeAddonsWrite}, ScopeSystemWrite, false},
		{Scopes{ScopeLogsRead}, ScopeSystemRead, false},
		{Scopes{ScopeRPCAdmin}, ScopeRPCRead, true},
		{Scopes{ScopeRPCConfigure}, ScopeRPCOperate, true},
		{Scopes{ScopeRPCOperate}, ScopeRPCConfigure, false},
		{Scopes{ScopeAuthAdmin}, ScopeSelf, true},
		{Scopes{ScopeMetaRead, ScopeSystemRead, ScopeLogsRead}, ScopeSelf, false},
		{Scopes{ScopePower}, ScopeBackup, false},
		// task 154 (D-104): only Full access includes the device keys
		{Scopes{ScopeSystemWrite, ScopeAuthAdmin, ScopeBackup}, ScopeRadioKeys, false},
		{Scopes{ScopeAll}, ScopeRadioKeys, true},
		{Scopes{ScopeRadioKeys}, ScopeSystemRead, false},
		{Scopes{ScopeAll}, ScopeRPCAdmin, true},
		{Scopes{ScopeAll}, Scope("future:scope"), true}, // the wildcard includes what is added later
		{Scopes{ScopeAll}, ScopeSelf, true},
		{nil, ScopeMetaRead, false},
	} {
		if got := tc.have.Has(tc.want); got != tc.ok {
			t.Errorf("%v has %s: %v, want %v", tc.have, tc.want, got, tc.ok)
		}
	}
}

func TestScopesNormalizeAndParse(t *testing.T) {
	if got := (Scopes{ScopeMetaRead, ScopeMetaWrite, ScopeMetaRead, ScopeSelf}).Normalize(); len(got) != 1 || got[0] != ScopeMetaWrite {
		t.Errorf("normalize: %v", got)
	}
	if got := (Scopes{ScopeLED, ScopeAll, ScopeMetaRead}).Normalize(); len(got) != 1 || got[0] != ScopeAll {
		t.Errorf("full: %v", got)
	}
	if got, err := ParseScopes([]string{" system:read ", "meta:read", "system:write"}); err != nil || strings.Join(got.Strings(), ",") != "meta:read,system:write" {
		t.Errorf("parse: %v %v", got, err)
	}
	for _, bad := range [][]string{{}, {""}, {"self"}, {"root"}, {"meta:read", "meta"}} {
		if _, err := ParseScopes(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if (Scopes{ScopeAll}).RPCTier() != "admin" || (Scopes{ScopeRPCConfigure}).RPCTier() != "configure" || (Scopes{ScopeMetaRead}).RPCTier() != "" {
		t.Error("rpc tier")
	}
	for _, s := range Grantable {
		if !Known(s) || s == ScopeAll || s == ScopeSelf {
			t.Errorf("grantable %s", s)
		}
	}
}

// The role mapping (D-85): admin is Full access, user the three reads, led the scope led.
func TestRoleScopes(t *testing.T) {
	if !RoleScopes(RoleAdmin).Full() {
		t.Error("admin")
	}
	if u := RoleScopes(RoleUser); strings.Join(u.Strings(), ",") != "logs:read,meta:read,system:read" || u.Has(ScopeMetaWrite) || u.Has(ScopeLED) || u.Has(ScopeAuthAdmin) {
		t.Errorf("user: %v", u)
	}
	if l := RoleScopes(RoleLED); strings.Join(l.Strings(), ",") != "led" || l.Has(ScopeSystemRead) {
		t.Errorf("led: %v", l)
	}
	if RoleScopes(Role("root")) != nil {
		t.Error("unknown role")
	}
}

// An addon never gets Full access, auth:admin, power or backup, nor a name that is no scope;
// the rest is granted, normalized.
func TestAddonScopes(t *testing.T) {
	granted, refused := AddonScopes([]string{"*", "auth:admin", "power", "backup", "radio:keys", "meta:write", "meta:read", "rpc:operate", "bogus", ""})
	if strings.Join(granted.Strings(), ",") != "meta:write,rpc:operate" {
		t.Errorf("granted %v", granted)
	}
	// task 154 (D-104): an addon never reads the HmIP device keys
	if strings.Join(refused, ",") != "*,auth:admin,power,backup,radio:keys,bogus," {
		t.Errorf("refused %q", refused)
	}
	if g, r := AddonScopes(nil); g != nil || r != nil {
		t.Errorf("nothing declared: %v %v", g, r)
	}
}

// openccu-lite task 307: the addon ingress scope addon:<id> is known when its id has the manifest's
// shape, is covered by Full access and by nothing else, survives Normalize beside the fixed scopes,
// and is never granted to an addon's own token.
func TestAddonIngressScope(t *testing.T) {
	for _, tc := range []struct {
		scope Scope
		id    string
		ok    bool
	}{
		{"addon:openccu-loom", "openccu-loom", true},
		{"addon:hmm", "hmm", true},
		{"addon:jp-hb-devices-addon", "jp-hb-devices-addon", true},
		{"addon:", "", false},
		{"addon:Hmm", "", false},
		{"addon:a/b", "", false},
		{"addon:-x", "", false},
		{Scope("addon:" + strings.Repeat("a", 33)), "", false},
		{"addons:write", "", false},
		{"meta:read", "", false},
	} {
		id, ok := AddonOf(tc.scope)
		if ok != tc.ok || id != tc.id {
			t.Errorf("AddonOf(%s) = %q %v, want %q %v", tc.scope, id, ok, tc.id, tc.ok)
		}
		if Known(tc.scope) != (tc.ok || tc.scope == ScopeAddonsWrite || tc.scope == ScopeMetaRead) {
			t.Errorf("Known(%s) = %v", tc.scope, Known(tc.scope))
		}
	}
	if AddonScope("hmm") != "addon:hmm" {
		t.Error("AddonScope")
	}
	loom := AddonScope("openccu-loom")
	if !(Scopes{ScopeAll}).Has(loom) {
		t.Error("Full access does not cover the ingress scope")
	}
	for _, have := range []Scopes{{ScopeAddonsWrite}, {ScopeSystemWrite, ScopeAuthAdmin, ScopeRPCAdmin}, {AddonScope("hmm")}} {
		if have.Has(loom) {
			t.Errorf("%v covers %s", have, loom)
		}
	}
	if !(Scopes{loom}).Has(loom) || (Scopes{loom}).Has(ScopeSystemRead) || (Scopes{loom}).Has(ScopeMetaRead) {
		t.Error("the ingress scope stands for itself and nothing else")
	}
	got, err := ParseScopes([]string{"addon:hmm", "meta:read", "addon:hmm", "addon:openccu-loom"})
	if err != nil || strings.Join(got.Strings(), ",") != "addon:hmm,addon:openccu-loom,meta:read" {
		t.Errorf("parse: %v %v", got, err)
	}
	if _, err := ParseScopes([]string{"addon:Nope"}); err == nil {
		t.Error("a malformed ingress scope parsed")
	}
	// an addon's catalogue declaration cannot ask for another addon's ingress
	granted, refused := AddonScopes([]string{"addon:hmm", "meta:read"})
	if strings.Join(granted.Strings(), ",") != "meta:read" || strings.Join(refused, ",") != "addon:hmm" {
		t.Errorf("addon token: granted %v refused %v", granted, refused)
	}
}

// A users.json from before task 66 loads with each token's role turned into its scopes, and the
// next save writes scopes and no role.
func TestTokenMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	old := `{"users":[{"name":"admin","role":"admin","hash":"argon2id$x$y","created":"2026-09-01T00:00:00Z"}],
	"tokens":[{"name":"ci","role":"admin","hash":"h1","prefix":"aaaaaaaa","created":"2026-09-01T00:00:00Z"},
	{"name":"local","role":"user","hash":"h2","prefix":"bbbbbbbb","created":"2026-09-01T00:00:00Z"},
	{"name":"hass","role":"led","hash":"h3","prefix":"cccccccc","created":"2026-09-01T00:00:00Z"},
	{"name":"new","scopes":["meta:read"],"hash":"h4","prefix":"dddddddd","created":"2026-09-01T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ci": "*", "local": "logs:read,meta:read,system:read", "hass": "led", "new": "meta:read"}
	for _, tk := range s.Tokens() {
		if got := strings.Join(tk.Scopes.Strings(), ","); got != want[tk.Name] || tk.Role != "" {
			t.Errorf("%s: %q role %q, want %q", tk.Name, got, tk.Role, want[tk.Name])
		}
	}
	// not rewritten by the load alone
	if b, _ := os.ReadFile(path); string(b) != old {
		t.Error("the load rewrote users.json")
	}
	// the next save writes scopes and no role, and a fresh store reads the same
	if _, err := s.CreateToken("more", TokenOptions{Scopes: Scopes{ScopeBackup}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"role":"admin","hash":"h1"`) || strings.Contains(string(b), `"role": "led"`) || !strings.Contains(string(b), `"scopes"`) {
		t.Errorf("saved file:\n%s", b)
	}
	var doc usersDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tk := range doc.Tokens {
		if tk.Role != "" {
			t.Errorf("%s still carries a role", tk.Name)
		}
	}
	fresh, _ := Open(dir, Options{})
	want["more"] = "backup"
	for _, tk := range fresh.Tokens() {
		if got := strings.Join(tk.Scopes.Strings(), ","); got != want[tk.Name] {
			t.Errorf("fresh %s: %q", tk.Name, got)
		}
	}
}

// A token with scopes, an expiry and allowed ranges: accepted from inside the ranges before the
// expiry, refused outside, refused without a known address, refused afterwards.
func TestTokenExpiryAndRanges(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	s, err := Open(dir, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Setup("admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	past := now.Add(-time.Hour)
	if _, err := s.CreateToken("late", TokenOptions{Scopes: Scopes{ScopeMetaRead}, Expires: &past}); err != ErrTokenExpiry {
		t.Errorf("past expiry: %v", err)
	}
	if _, err := s.CreateToken("bad", TokenOptions{Scopes: Scopes{ScopeMetaRead}, IPs: []string{"nope"}}); err == nil {
		t.Error("bad range accepted")
	}
	if _, err := s.CreateToken("none", TokenOptions{}); err == nil {
		t.Error("a token without scopes")
	}
	exp := now.Add(time.Hour)
	secret, err := s.CreateToken("lan", TokenOptions{Scopes: Scopes{ScopeMetaRead, ScopeLogsRead}, Expires: &exp, IPs: []string{"192.168.1.0/24", "10.0.0.5", " fd00::/8 "}})
	if err != nil {
		t.Fatal(err)
	}
	tk := s.Tokens()[0]
	if strings.Join(tk.IPs, " ") != "192.168.1.0/24 10.0.0.5/32 fd00::/8" || tk.Expires == nil || !tk.Expires.Equal(exp) {
		t.Errorf("stored: %+v", tk)
	}
	if s.ValidateFrom(secret, "192.168.1.20") == nil || s.ValidateFrom(secret, "10.0.0.5") == nil || s.ValidateFrom(secret, "fd00::1") == nil {
		t.Error("refused from inside the ranges")
	}
	if s.ValidateFrom(secret, "192.168.2.20") != nil || s.ValidateFrom(secret, "") != nil || s.Validate(secret) != nil || s.ValidateFrom(secret, "garbage") != nil {
		t.Error("accepted from outside the ranges")
	}
	now = exp
	if s.ValidateFrom(secret, "192.168.1.20") != nil {
		t.Error("accepted at the expiry")
	}
	// a token without ranges is accepted from anywhere, Validate included
	now = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	open, _ := s.CreateToken("open", TokenOptions{Scopes: Scopes{ScopeMetaRead}})
	if s.Validate(open) == nil || s.ValidateFrom(open, "8.8.8.8") == nil {
		t.Error("open token refused")
	}
	// the fresh store reads the same
	fresh, _ := Open(dir, Options{Now: func() time.Time { return now }})
	if fresh.ValidateFrom(secret, "192.168.1.20") == nil || fresh.ValidateFrom(secret, "1.2.3.4") != nil {
		t.Error("fresh store")
	}
}

// Ephemeral tokens: in memory, never in users.json, an addon's filtered by AddonScopes, dropped
// on request or when nothing stands.
func TestEphemeralTokens(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Setup("admin", "secret123"); err != nil {
		t.Fatal(err)
	}
	secret, granted, refused, err := s.MintAddonToken("hmm", []string{"meta:write", "power", "*"})
	if err != nil || strings.Join(granted, ",") != "meta:write" || strings.Join(refused, ",") != "power,*" || !IsToken(secret) {
		t.Fatalf("mint: %q %v %v %v", secret, granted, refused, err)
	}
	sess := s.Validate(secret)
	if sess == nil || sess.User != "token:addon:hmm" || !sess.IsToken() || !sess.Has(ScopeMetaRead) || !sess.Has(ScopeMetaWrite) || sess.Has(ScopeSystemRead) || sess.Has(ScopeSelf) {
		t.Fatalf("session: %+v", sess)
	}
	if got := s.EphemeralScopes(AddonTokenName("hmm")); strings.Join(got.Strings(), ",") != "meta:write" {
		t.Errorf("scopes: %v", got)
	}
	if n := len(s.Tokens()); n != 0 {
		t.Errorf("listed among the stored tokens: %d", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "users.json")); strings.Contains(string(b), "addon:hmm") {
		t.Error("in users.json")
	}
	// minted again: the old secret is gone, the new one stands
	again, _, _, _ := s.MintAddonToken("hmm", []string{"meta:read"})
	if s.Validate(secret) != nil || s.Validate(again) == nil {
		t.Error("re-mint")
	}
	// nothing granted: nothing minted, the earlier token gone
	if sec, granted, refused, err := s.MintAddonToken("hmm", []string{"power"}); sec != "" || granted != nil || len(refused) != 1 || err != nil {
		t.Errorf("nothing stands: %q %v %v %v", sec, granted, refused, err)
	}
	if s.Validate(again) != nil || s.EphemeralScopes(AddonTokenName("hmm")) != nil {
		t.Error("dropped")
	}
	own, err := s.MintEphemeral("occulited:update-check", Scopes{ScopeSystemRead})
	if err != nil || s.Validate(own) == nil {
		t.Fatalf("own: %v", err)
	}
	s.DropEphemeral("occulited:update-check")
	if s.Validate(own) != nil {
		t.Error("not dropped")
	}
	if _, err := s.MintEphemeral("x", nil); err == nil {
		t.Error("no scopes")
	}
}

// The local token from before task 66 keeps its secret and loses every scope but meta:read.
func TestLocalTokenNarrowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local-token")
	s, _ := Open(dir, Options{})
	secret, err := s.CreateToken("local", asRole(RoleUser))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLocalToken(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != secret {
		t.Error("the secret changed")
	}
	sess := s.Validate(secret)
	if sess == nil || !sess.Has(ScopeMetaRead) || sess.Has(ScopeSystemRead) || sess.Has(ScopeLogsRead) {
		t.Errorf("scopes: %+v", sess)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode %o", st.Mode().Perm())
	}
	fresh, _ := Open(dir, Options{})
	if tk := fresh.Tokens()[0]; strings.Join(tk.Scopes.Strings(), ",") != "meta:read" {
		t.Errorf("stored: %v", tk.Scopes)
	}
}

// occulited task 132: power:halt is an addon's to hold, power brings it along, and it brings nothing back.
func TestPowerHalt(t *testing.T) {
	if slices.Contains(AddonNever, ScopePowerHalt) || !slices.Contains(AddonNever, ScopePower) {
		t.Fatal("AddonNever")
	}
	if !Known(ScopePowerHalt) {
		t.Fatal("not known")
	}
	if !(&Session{Scopes: []Scope{ScopePower}}).Has(ScopePowerHalt) {
		t.Fatal("power includes power:halt")
	}
	if (&Session{Scopes: []Scope{ScopePowerHalt}}).Has(ScopePower) {
		t.Fatal("power:halt must not grant power")
	}
}
