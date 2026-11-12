package system

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// fwPriv is the helper for the firewall tests: writes go to disk as the local one does, loads are
// recorded.
type fwPriv struct {
	priv.Local
	mu       sync.Mutex
	loads    []string // "window:<d> <v4 first rule line>"
	confirms int
	reverts  int
	fail     error
}

func (p *fwPriv) FirewallLoad(_ context.Context, v4, v6 []byte, window time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	if err := priv.ValidFirewallText(v4); err != nil {
		return err
	}
	if err := priv.ValidFirewallText(v6); err != nil {
		return err
	}
	p.loads = append(p.loads, window.String()+"\n"+string(v4))
	return nil
}

func (p *fwPriv) FirewallConfirm(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.confirms++
	return nil
}

func (p *fwPriv) FirewallRevert(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reverts++
	return nil
}

func fwRig(t *testing.T, files map[string]string) (*FirewallRules, *fwPriv, *map[string][]firewall.PortSpec) {
	t.Helper()
	r := rootWith(t, files)
	p := &fwPriv{}
	old := Priv
	Priv = p
	t.Cleanup(func() { Priv = old })
	owners := &map[string][]firewall.PortSpec{}
	_, g, _ := net.ParseCIDR("203.0.113.0/24")
	g.IP = net.ParseIP("203.0.113.9")
	f := &FirewallRules{Root: r, StateDir: filepath.Join(t.TempDir(), "state"), Owners: func() map[string][]firewall.PortSpec { return *owners },
		Interfaces: func() []*net.IPNet { return []*net.IPNet{g} }, Window: 100 * time.Millisecond}
	return f, p, owners
}

// a box with firewall.conf: the conversion, once, the file renamed; the owners switched on join
func TestFirewallRulesMigrateAndStart(t *testing.T) {
	f, p, _ := fwRig(t, map[string]string{
		"etc/config/firewall.conf":      "MODE = RESTRICTIVE\nIPs = 10.0.0.0/8\nUSERPORTS = 8181\n\n[SERVICE XMLRPC]\nId = XMLRPC\nPorts = 2001\nAccess = full\n",
		"etc/config/sshEnabled":         "",
		"etc/config/firewallConfigured": "",
	})
	_ = os.MkdirAll(f.StateDir, 0o755)
	_ = os.WriteFile(filepath.Join(f.StateDir, "firewall.json"), []byte(`{"user_ports":[]}`), 0o644)
	// as main's owners do: classic RPC's from its markers, which the conversion writes
	f.Owners = func() map[string][]firewall.PortSpec {
		o := map[string][]firewall.PortSpec{firewall.OwnerSSH: f.Root.SSHOwner()}
		if p := f.Root.ClassicRPCOwner(); p != nil {
			o[firewall.OwnerRPC] = p
		}
		return o
	}
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, ok := f.Root.ReadRules()
	if !ok || c.Migration == nil || c.Policy.V4 != firewall.Drop {
		t.Fatalf("rules: %+v", c)
	}
	for _, want := range []struct {
		port  int
		owner string
	}{{80, firewall.OwnerWeb}, {443, firewall.OwnerWeb}, {22, firewall.OwnerSSH}, {2001, firewall.OwnerRPC}, {42001, firewall.OwnerRPC}, {8181, ""}} {
		if !slices.ContainsFunc(c.Rules, func(r firewall.Rule) bool { return r.Port == want.port && r.Owner == want.owner }) {
			t.Errorf("no rule for %d (%s): %+v", want.port, want.owner, c.Rules)
		}
	}
	// task 143: the open XMLRPC switched classic RPC on; 2000 went with the first sync (no hs485d)
	if cr := f.Root.ReadClassicRPC(); !cr.Plain || !cr.TLS || cr.Auth != "none" {
		t.Errorf("classic RPC: %+v", cr)
	}
	if slices.ContainsFunc(c.Rules, func(r firewall.Rule) bool { return r.Port == 2000 && r.Owner == firewall.OwnerRPC }) {
		t.Errorf("2000 owned without hs485d: %+v", c.Rules)
	}
	if _, err := os.Stat(f.Root.join("/etc/config/firewall.conf")); err == nil {
		t.Error("firewall.conf is still there")
	}
	// task 172: libfirewall's marker and the old manual-port list go with the conversion
	if _, err := os.Stat(f.Root.join("/etc/config/firewallConfigured")); err == nil {
		t.Error("firewallConfigured is still there")
	}
	if _, err := os.Stat(filepath.Join(f.StateDir, "firewall.json")); err == nil {
		t.Error("the old firewall.json is still there")
	}
	if _, err := os.Stat(f.Root.join("/etc/config/firewall.conf.migrated")); err != nil {
		t.Error("no firewall.conf.migrated")
	}
	if len(p.loads) != 1 || !strings.HasPrefix(p.loads[0], "0s\n") || !strings.Contains(p.loads[0], "-s 203.0.113.0/24 -m tcp --dport 80 -j ACCEPT") {
		t.Fatalf("loads: %v", p.loads)
	}
	// a second start converts nothing again and loads once more
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c2, _ := f.Root.ReadRules(); len(c2.Rules) != len(c.Rules) {
		t.Errorf("converted twice: %d rules, was %d", len(c2.Rules), len(c.Rules))
	}
	// a fresh box: the defaults
	g, _, _ := fwRig(t, nil)
	if err := g.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c, _ := g.Root.ReadRules(); len(c.Rules) != 2+len(firewall.DiscoveryPorts) || c.Migration != nil {
		t.Fatalf("fresh: %+v", c)
	}
}

// task 143: OpenCCU asked the ReGa accounts for a login (authEnabled) - classic RPC stays off, the
// XMLRPC rules stay the user's, and the notice says why
func TestFirewallRulesMigrateAuthEnabled(t *testing.T) {
	f, _, _ := fwRig(t, map[string]string{
		"etc/config/firewall.conf": "MODE = RESTRICTIVE\nIPs = 10.0.0.0/8\n\n[SERVICE XMLRPC]\nId = XMLRPC\nPorts = 2001\nAccess = full\n",
		"etc/config/authEnabled":   "",
	})
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, _ := f.Root.ReadRules()
	if cr := f.Root.ReadClassicRPC(); cr.Plain || cr.TLS {
		t.Errorf("switched on: %+v", cr)
	}
	if !slices.ContainsFunc(c.Rules, func(r firewall.Rule) bool { return r.Port == 2001 && r.Owner == "" }) {
		t.Errorf("no hand-made 2001: %+v", c.Rules)
	}
	if !slices.ContainsFunc(c.Migration.Notes, func(n firewall.Note) bool { return strings.Contains(n.Text, "authEnabled") }) {
		t.Errorf("notes: %+v", c.Migration.Notes)
	}
}

// the owners: SSH switched off drops its rules at once, with no window; a sync with nothing changed
// does not load again; a change of the local networks does
func TestFirewallRulesSync(t *testing.T) {
	f, p, owners := fwRig(t, nil)
	*owners = map[string][]firewall.PortSpec{firewall.OwnerSSH: firewall.SSHPorts}
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, _ := f.Root.ReadRules()
	if !slices.ContainsFunc(c.Rules, func(r firewall.Rule) bool { return r.Owner == firewall.OwnerSSH }) {
		t.Fatalf("no ssh rule: %+v", c.Rules)
	}
	n := len(p.loads)
	if err := f.Sync(t.Context(), false); err != nil || len(p.loads) != n {
		t.Fatalf("a sync without a change loaded: %v", err)
	}
	*owners = map[string][]firewall.PortSpec{}
	if err := f.Sync(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	c, _ = f.Root.ReadRules()
	if slices.ContainsFunc(c.Rules, func(r firewall.Rule) bool { return r.Owner == firewall.OwnerSSH }) || len(p.loads) != n+1 {
		t.Fatalf("ssh off: %+v, loads %d", c.Rules, len(p.loads))
	}
	_, g2, _ := net.ParseCIDR("198.51.100.0/24")
	g2.IP = net.ParseIP("198.51.100.4")
	f.Interfaces = func() []*net.IPNet { return []*net.IPNet{g2} }
	if err := f.Sync(t.Context(), false); err != nil || len(p.loads) != n+2 || !strings.Contains(p.loads[n+1], "198.51.100.0/24") {
		t.Fatalf("new prefix: %v %d", err, len(p.loads))
	}
}

// the draft: validated, owners only where they own the port; Apply loads it with the window and
// writes nothing; Confirm writes it; a window that runs out leaves the draft and the old file
func TestFirewallRulesDraftApplyConfirm(t *testing.T) {
	f, p, owners := fwRig(t, nil)
	*owners = map[string][]firewall.PortSpec{}
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, _ := f.Root.ReadRules()
	rules := append([]firewall.Rule{}, before.Rules...)
	dup := rules[0]
	dup.ID, dup.Source = "", "192.168.1.0/24"
	dup.Family = firewall.FamilyV4
	rules = append(rules, dup,
		firewall.Rule{Port: 1883, Proto: "tcp", Source: firewall.AnySource, Family: firewall.FamilyBoth, Target: firewall.Accept, Owner: "addon:fake"})
	d, err := f.SetDraft(firewall.Policy{V4: firewall.Drop, V6: firewall.Drop}, rules, []string{"2001:db8:1::/56"})
	if err != nil {
		t.Fatal(err)
	}
	n := len(before.Rules)
	if d.Rules[n].ID == "" || d.Rules[n].Owner != firewall.OwnerWeb || d.Rules[n+1].Owner != "" {
		t.Fatalf("draft owners: %+v", d.Rules)
	}
	if _, err := f.SetDraft(firewall.Policy{V4: "MAYBE", V6: firewall.Drop}, rules, nil); err == nil {
		t.Fatal("a bad policy")
	}
	if _, err := f.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	if last := p.loads[len(p.loads)-1]; !strings.HasPrefix(last, "100ms\n") || !strings.Contains(last, "--dport 1883") {
		t.Fatalf("apply: %q", last)
	}
	if now, _ := f.Root.ReadRules(); len(now.Rules) != len(before.Rules) {
		t.Fatal("the file changed before the confirm")
	}
	if _, err := f.SetDraft(d.Policy, rules, nil); !errors.Is(err, ErrFirewallPending) {
		t.Fatalf("a draft while pending: %v", err)
	}
	if v := f.View(); v.Pending == nil || v.Draft == nil {
		t.Fatalf("view: %+v", v)
	}
	if err := f.Confirm(t.Context()); err != nil {
		t.Fatal(err)
	}
	if now, _ := f.Root.ReadRules(); len(now.Rules) != n+2 || p.confirms != 1 || f.View().Draft != nil {
		t.Fatalf("confirmed: %+v", now.Rules)
	}
	// once more, and let the window run out
	if _, err := f.SetDraft(firewall.Policy{V4: firewall.Accept, V6: firewall.Drop}, rules[:1], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2300 * time.Millisecond)
	v := f.View()
	if v.Pending != nil || v.Draft == nil || !strings.Contains(v.Error, "not confirmed in time") {
		t.Fatalf("after the window: %+v", v)
	}
	if now, _ := f.Root.ReadRules(); now.Policy.V4 != firewall.Drop {
		t.Fatal("the file follows an unconfirmed apply")
	}
	// the revert on request
	if _, err := f.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.Revert(t.Context()); err != nil || p.reverts != 1 || f.View().Pending != nil {
		t.Fatalf("revert: %v", err)
	}
	if err := f.DiscardDraft(); err != nil || f.View().Draft != nil {
		t.Fatal("discard")
	}
}

// openccu-lite B-188: a fresh userfs - /etc/config a link to ../usr/local/etc/config, which does
// not exist yet - gets its rule file and the directory from EnsureRules instead of an error.
func TestEnsureRulesFreshUserfs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../usr/local/etc/config", filepath.Join(root, "etc/config")); err != nil {
		t.Fatal(err)
	}
	c, err := Root(root).EnsureRules(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) == 0 || c.Migration != nil {
		t.Fatalf("not the defaults: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(root, "usr/local/etc/config/firewall-rules.json")); err != nil {
		t.Fatal(err)
	}
	if got, ok := Root(root).ReadRules(); !ok || len(got.Rules) != len(c.Rules) {
		t.Fatalf("read back: %v %+v", ok, got)
	}
}

// openccu-lite B-181: a start before the radio detection has written its plan says nothing about
// the HmIP access points - their rules stay, and the file and an existing draft are not rewritten
// (before, the four rules were removed at the start and added again with new ids half a minute
// later, at every boot); once the plan is there the owner is followed as before
func TestFirewallRulesSyncUnknownOwnerKeepsTheFile(t *testing.T) {
	f, p, owners := fwRig(t, nil)
	unknown := []string{}
	f.Unknown = func() []string { return unknown }
	*owners = map[string][]firewall.PortSpec{firewall.OwnerSSH: firewall.SSHPorts, firewall.OwnerHmIPAP: firewall.HmIPAPPorts}
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := f.Root.join(firewall.RulesPath)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := f.Root.ReadRules()
	var apIDs []string
	for _, r := range c.Rules {
		if r.Owner == firewall.OwnerHmIPAP {
			apIDs = append(apIDs, r.ID)
		}
	}
	if len(apIDs) != len(firewall.HmIPAPPorts) || apIDs[0] != firewall.OwnedID(firewall.OwnerHmIPAP, firewall.HmIPAPPorts[0]) {
		t.Fatalf("hmip-ap rules: %v", apIDs)
	}
	// a stray draft, as .119 had
	draft := c.Clone()
	draft.Rules = append(draft.Rules, firewall.Rule{ID: "deadbeef", Port: 8080, Proto: firewall.ProtoTCP, Source: firewall.AnySource, Family: firewall.FamilyBoth, Target: firewall.Accept})
	f.writeDraft(&draft)
	draftBefore, _ := os.ReadFile(f.draftPath())

	// the next boot: occulited starts before the plan exists - hmip-ap is neither wanted nor known
	g := &FirewallRules{Root: f.Root, StateDir: f.StateDir, Interfaces: f.Interfaces, Window: f.Window}
	unknown = []string{firewall.OwnerHmIPAP}
	g.Unknown = func() []string { return unknown }
	*owners = map[string][]firewall.PortSpec{firewall.OwnerSSH: firewall.SSHPorts}
	g.Owners = func() map[string][]firewall.PortSpec { return *owners }
	loads := len(p.loads)
	if err := g.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("the file changed at a start with the owner unknown:\n%s", after)
	}
	if d, _ := os.ReadFile(g.draftPath()); string(d) != string(draftBefore) {
		t.Fatal("the draft was rewritten")
	}
	if len(p.loads) != loads+1 || !strings.Contains(p.loads[loads], "43438") {
		t.Fatalf("the start loads the rules as the file has them, hmip-ap included: %d loads", len(p.loads))
	}
	if err := g.Sync(t.Context(), false); err != nil || len(p.loads) != loads+1 {
		t.Fatalf("a watch tick with the owner still unknown: %v, %d loads", err, len(p.loads))
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("the file changed at a watch tick")
	}
	// the plan is there and says no HmIP: the rules go; then HmIP is back: the same rules, the same ids
	unknown = nil
	if err := g.Sync(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	c, _ = g.Root.ReadRules()
	if slices.ContainsFunc(c.Rules, func(r firewall.Rule) bool { return r.Owner == firewall.OwnerHmIPAP }) {
		t.Fatalf("hmip-ap rules with the plan saying no HmIP: %+v", c.Rules)
	}
	*owners = map[string][]firewall.PortSpec{firewall.OwnerSSH: firewall.SSHPorts, firewall.OwnerHmIPAP: firewall.HmIPAPPorts}
	if err := g.Sync(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	c, _ = g.Root.ReadRules()
	var again []string
	for _, r := range c.Rules {
		if r.Owner == firewall.OwnerHmIPAP {
			again = append(again, r.ID)
		}
	}
	if !slices.Equal(again, apIDs) {
		t.Fatalf("ids after the re-add %v, before %v", again, apIDs)
	}
	// the draft followed the owner both times, its own rule kept
	if d, ok := g.readDraft(); !ok || !slices.ContainsFunc(d.Rules, func(r firewall.Rule) bool { return r.ID == "deadbeef" }) {
		t.Fatalf("draft: %v %+v", ok, d.Rules)
	}
}
