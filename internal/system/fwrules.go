package system

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/firewall"
)

// ---- the firewall (task 157, D-105) ----------------------------------------------------------
//
// occulited owns the firewall: one ordered list of INPUT rules in /etc/config/firewall-rules.json,
// rendered per family and loaded through the privilege helper. The page edits a draft; Apply loads
// it with a confirm window the helper owns, and only Confirm writes the file. Automatic changes -
// an owner switched on or off (SSH, the HmIP access points, an addon's opened port), the local
// networks changing - load at once, without a window: they do not come from the page's connection.

// FirewallWindow is the confirm window of an Apply.
const FirewallWindow = 60 * time.Second

// FirewallRules is the service. Owners answers the owners switched on now; Interfaces the
// interfaces' addresses (nil: net.InterfaceAddrs).
type FirewallRules struct {
	Root     Root
	StateDir string
	Owners   func() map[string][]firewall.PortSpec
	// Unknown answers the owners whose state is not known yet at this moment - the HmIP access
	// points before the radio detection wrote its plan - so their rules stay as the file has them
	// instead of being removed at the start and added again half a minute later (B-181).
	Unknown    func() []string
	Interfaces func() []*net.IPNet
	Log        *slog.Logger
	Window     time.Duration

	mu      sync.Mutex
	pending *FirewallPending
	local   firewall.Local
	loaded  bool
	// loadedAt is when the rules were last loaded - where the counters start (task 167)
	loadedAt time.Time
	lastErr  string
}

// FirewallPending is an Apply waiting for its Confirm.
type FirewallPending struct {
	Started  time.Time `json:"started"`
	Deadline time.Time `json:"deadline"`
}

// FirewallView is GET /firewall.
type FirewallView struct {
	Config  firewall.Config     `json:"config"`
	Draft   *firewall.Config    `json:"draft"`
	Pending *FirewallPending    `json:"pending"`
	Frame   map[string][]string `json:"frame"`
	Local   firewall.Local      `json:"local_networks"`
	Owners  map[string]string   `json:"owners"`
	Error   string              `json:"error,omitempty"`
}

func (f *FirewallRules) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

func (f *FirewallRules) window() time.Duration {
	if f.Window > 0 {
		return f.Window
	}
	return FirewallWindow
}

func (f *FirewallRules) draftPath() string { return filepath.Join(f.StateDir, "firewall-draft.json") }

// ReadRules is the confirmed rule file; ok false when there is none (a box before task 157).
func (r Root) ReadRules() (firewall.Config, bool) {
	var c firewall.Config
	b, err := os.ReadFile(r.join(firewall.RulesPath))
	if err != nil || json.Unmarshal(b, &c) != nil {
		return firewall.Config{}, false
	}
	if c.Rules == nil {
		c.Rules = []firewall.Rule{}
	}
	if c.Active == nil {
		c.Active = []string{}
	}
	return c, true
}

// WriteRules writes the rule file atomically (through the helper: /etc/config is root's).
func (r Root) WriteRules(c firewall.Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return writeFileAtomic(r.join(firewall.RulesPath), append(b, '\n'), 0o644)
}

// oldFirewall is firewall.conf as the conversion needs it.
func (r Root) oldFirewall() (firewall.Old, bool) {
	if _, err := os.Stat(r.join("/etc/config/firewall.conf")); err != nil {
		return firewall.Old{}, false
	}
	fw := r.ReadFirewall()
	old := firewall.Old{Mode: fw.Mode, IPs: fw.IPs, UserPorts: fw.UserPorts}
	for _, s := range fw.Services {
		old.Services = append(old.Services, firewall.OldService{ID: s.ID, Ports: s.Ports, Access: s.Access})
	}
	if _, err := os.Stat(r.join("/etc/config/AllowExternalAccess")); err == nil {
		old.AllowExternal = true
	}
	return old, true
}

// FirewallDefaultMarker is B-43's marker in the state directory: D-29's RESTRICTIVE default was
// applied to firewall.conf once. A box without it (switched from OpenCCU, never run by an occulited
// with B-43) still carries OpenCCU's MOST_OPEN, which D-29 overrules - the conversion then takes
// policy DROP, as B-43 would have.
const FirewallDefaultMarker = "firewall-default-applied"

// EnsureRules is the rule file, made once when there is none: converted from firewall.conf when
// that exists (renamed .migrated afterwards), a fresh box's defaults otherwise. owners are the
// owners switched on now (nil: none known yet, the web server only); stateDir is where B-43's
// marker is.
func (r Root) EnsureRules(owners map[string][]firewall.PortSpec, stateDir string) (firewall.Config, error) {
	if c, ok := r.ReadRules(); ok {
		r.removeLibfirewallLeftovers(stateDir)
		return c, nil
	}
	var c firewall.Config
	if old, ok := r.oldFirewall(); ok {
		marker := filepath.Join(stateDir, FirewallDefaultMarker)
		forced := false
		if _, err := os.Stat(marker); err != nil && stateDir != "" && old.Mode == "MOST_OPEN" {
			old.Mode, forced = "RESTRICTIVE", true
		}
		// task 143 (the maintainer, 2026-09-18): a CCU whose XMLRPC ports were open keeps its
		// clients - classic RPC is switched on, plain and TLS as OpenCCU served both, and its rules
		// are the converted ones. Not when OpenCCU asked the ReGa accounts for a login
		// (authEnabled): lite has no ReGa, and the ports would be open without one.
		all := map[string][]firewall.PortSpec{}
		for o, p := range owners {
			all[o] = p
		}
		authNote := false
		if xmlrpcOpen(old) {
			if _, err := os.Stat(r.join(AuthEnabledMarker)); err == nil {
				authNote = true
			} else if err := r.switchClassicRPCOn(); err == nil {
				// every port, 2000/42000 too: where hs485d does not run the owner drops them at
				// its first sync
				all[firewall.OwnerRPC] = classicSpecs(ClassicRPCPorts(true, true, true))
			}
		}
		c = firewall.Migrate(firewall.MigrateInputs{Old: old, Owners: all}, time.Now())
		if authNote && c.Migration != nil {
			c.Migration.Notes = append(c.Migration.Notes, firewall.NewNote("OpenCCU asked for a login on the XMLRPC ports (authEnabled, the ReGa accounts), which this system cannot carry over: classic RPC stays off. Switch it on with a user name and password on System → Remote access."))
		}
		if forced && c.Migration != nil {
			c.Migration.Notes = append([]firewall.Note{firewall.NewNote("firewall.conf said MOST_OPEN, which OpenCCU ships: this system's default is DROP, as it would have become at the first start of this firmware.")}, c.Migration.Notes...)
		}
		if stateDir != "" {
			_ = os.WriteFile(marker, []byte("{\"converted\":true}\n"), 0o600)
		}
	} else {
		c = firewall.Default()
		if owners != nil {
			all := firewall.AlwaysOwners()
			for o, p := range owners {
				all[o] = p
			}
			c = firewall.Reconcile(c, all)
		}
	}
	if err := r.WriteRules(c); err != nil {
		return c, err
	}
	if _, err := os.Stat(r.join("/etc/config/firewall.conf")); err == nil {
		b := []byte(readFile(r.join("/etc/config/firewall.conf")))
		if err := writeFileAtomic(r.join("/etc/config/firewall.conf.migrated"), b, 0o644); err == nil {
			_ = Priv.Remove(r.join("/etc/config/firewall.conf"))
		}
	}
	r.removeLibfirewallLeftovers(stateDir)
	return c, nil
}

// removeLibfirewallLeftovers takes out what the old firewall kept beside firewall.conf (task 172):
// libfirewall's "the page was seen" marker and occulited's own list of manual user ports (D-47),
// which nothing reads once the rule file exists. firewall.conf.migrated stays, for the record.
func (r Root) removeLibfirewallLeftovers(stateDir string) {
	if _, err := os.Stat(r.join("/etc/config/firewallConfigured")); err == nil {
		_ = Priv.Remove(r.join("/etc/config/firewallConfigured"))
	}
	if stateDir != "" {
		_ = os.Remove(filepath.Join(stateDir, "firewall.json"))
	}
}

// xmlrpcOpen: firewall.conf let clients reach the XMLRPC ports (full or restricted access).
func xmlrpcOpen(old firewall.Old) bool {
	for _, s := range old.Services {
		if s.ID == "XMLRPC" && (s.Access == "full" || s.Access == "restricted") {
			return true
		}
	}
	return false
}

// switchClassicRPCOn writes both markers; lighttpd reads them at its start, which comes after the
// boot's conversion.
func (r Root) switchClassicRPCOn() error {
	for _, m := range []string{ClassicRPCPlainMarker, ClassicRPCTLSMarker} {
		if err := writeFileAtomic(r.join(m), nil, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// SSHOwner is the SSH owner's ports while the marker says SSH is on.
func (r Root) SSHOwner() []firewall.PortSpec {
	if _, err := os.Stat(r.join("/etc/config/sshEnabled")); err == nil {
		return firewall.SSHPorts
	}
	return nil
}

// LocalNetworks is what "local networks" stands for now.
func (f *FirewallRules) LocalNetworks() firewall.Local {
	var nets []*net.IPNet
	if f.Interfaces != nil {
		nets = f.Interfaces()
	} else if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				nets = append(nets, n)
			}
		}
	}
	return firewall.LocalFor(nets)
}

func renderBoth(c firewall.Config, l firewall.Local) ([]byte, []byte, error) {
	v4, err := firewall.Render(c, firewall.FamilyV4, l)
	if err != nil {
		return nil, nil, err
	}
	v6, err := firewall.Render(c, firewall.FamilyV6, l)
	if err != nil {
		return nil, nil, err
	}
	return []byte(v4), []byte(v6), nil
}

// LoadRules renders a configuration and loads it with no window.
func LoadRules(ctx context.Context, c firewall.Config, l firewall.Local) error {
	v4, v6, err := renderBoth(c, l)
	if err != nil {
		return err
	}
	return Priv.FirewallLoad(ctx, v4, v6, 0)
}

// desired is the owners' ports as of now; an owner Unknown names and Owners does not keeps the
// ports c is active for, so nothing changes for it until its state is known (B-181).
func (f *FirewallRules) desired(c firewall.Config) map[string][]firewall.PortSpec {
	want := firewall.AlwaysOwners()
	if f.Owners != nil {
		for o, p := range f.Owners() {
			if len(p) > 0 {
				want[o] = p
			}
		}
	}
	if f.Unknown != nil {
		active := c.ActiveOwners()
		for _, o := range f.Unknown() {
			if _, ok := want[o]; !ok && len(active[o]) > 0 {
				want[o] = active[o]
			}
		}
	}
	return want
}

// Start is occulited's start: the rule file made if needed (the conversion), the owners brought in
// line, the rules loaded - the boot unit loaded them already, but the owners and the local networks
// are known only now.
func (f *FirewallRules) Start(ctx context.Context) error {
	c, _ := f.Root.ReadRules() // empty without a file: nothing an unknown owner could keep
	if _, err := f.Root.EnsureRules(f.desired(c), f.StateDir); err != nil {
		return err
	}
	return f.Sync(ctx, true)
}

// Sync brings the owned rules in line with the owners and reloads when they or the local networks
// changed (force: always). It waits while an Apply is open: the confirmed set is not the loaded one
// then, and Confirm or the revert syncs afterwards.
func (f *FirewallRules) Sync(ctx context.Context, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending != nil {
		return nil
	}
	c, ok := f.Root.ReadRules()
	if !ok {
		return errors.New("no firewall rules file")
	}
	want := f.desired(c)
	next := firewall.Reconcile(c, want)
	changed := !reflect.DeepEqual(next.Active, c.Active) || !reflect.DeepEqual(next.Rules, c.Rules)
	if changed {
		if err := f.Root.WriteRules(next); err != nil {
			return err
		}
		f.log().Info("firewall: the owners' rules follow", "active", next.Active)
		if d, ok := f.readDraft(); ok {
			nd := firewall.Reconcile(d, want)
			f.writeDraft(&nd)
		}
	}
	l := f.LocalNetworks()
	if !force && !changed && f.loaded && reflect.DeepEqual(l, f.local) {
		return nil
	}
	if err := LoadRules(ctx, next, l); err != nil {
		f.lastErr = err.Error()
		f.log().Error("firewall: loading the rules failed", "err", err)
		return err
	}
	f.local, f.loaded, f.lastErr, f.loadedAt = l, true, "", time.Now()
	return nil
}

// FirewallCounters is GET /firewall/counters (task 167): per rule the packets and bytes both
// families counted since the last load, and what reached the policy. Known is false when the
// tables do not hold the loaded lines.
type FirewallCounters struct {
	At     time.Time                 `json:"at"`
	Since  time.Time                 `json:"since,omitzero"`
	Known  bool                      `json:"known"`
	Rules  map[string]firewall.Count `json:"rules"`
	Policy map[string]firewall.Count `json:"policy"`
}

// Counters reads the tables' counters through the helper and lays them onto the loaded rules: the
// draft while a confirm window is open, the file otherwise.
func (f *FirewallRules) Counters(ctx context.Context) (FirewallCounters, error) {
	f.mu.Lock()
	c, ok := f.Root.ReadRules()
	if f.pending != nil {
		if d, dok := f.readDraft(); dok {
			c, ok = d, true
		}
	}
	since := f.loadedAt
	f.mu.Unlock()
	out := FirewallCounters{At: time.Now(), Since: since, Rules: map[string]firewall.Count{}, Policy: map[string]firewall.Count{}}
	if !ok {
		return out, nil
	}
	tables, err := Priv.FirewallCounters(ctx)
	if err != nil {
		return out, err
	}
	l := f.LocalNetworks()
	out.Known = true
	for _, fam := range firewall.Families {
		rules, pol, fok := firewall.Counts(tables[fam], firewall.Lines(c, fam, l))
		if !fok {
			out.Known = false
			out.Rules = map[string]firewall.Count{}
			break
		}
		out.Policy[fam] = pol
		for id, n := range rules {
			sum := out.Rules[id]
			sum.Packets += n.Packets
			sum.Bytes += n.Bytes
			out.Rules[id] = sum
		}
	}
	return out, nil
}

// ResetCounters loads the rules again, which starts every counter at zero (INPUT is declared with
// [0:0], the user chain flushed and refilled). Not while a confirm window is open.
func (f *FirewallRules) ResetCounters(ctx context.Context) error {
	f.mu.Lock()
	pending := f.pending != nil
	f.mu.Unlock()
	if pending {
		return ErrFirewallPending
	}
	return f.Sync(ctx, true)
}

// Watch syncs every interval (the owners and the local networks) until ctx ends.
func (f *FirewallRules) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = f.Sync(ctx, false)
		}
	}
}

func (f *FirewallRules) readDraft() (firewall.Config, bool) {
	var c firewall.Config
	b, err := os.ReadFile(f.draftPath())
	if err != nil || json.Unmarshal(b, &c) != nil {
		return c, false
	}
	return c, true
}

func (f *FirewallRules) writeDraft(c *firewall.Config) {
	if c == nil {
		_ = os.Remove(f.draftPath())
		return
	}
	_ = os.MkdirAll(f.StateDir, 0o750)
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(f.draftPath(), b, 0o640)
}

// View is GET /firewall.
func (f *FirewallRules) View() FirewallView {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, _ := f.Root.ReadRules()
	v := FirewallView{Config: c, Frame: map[string][]string{}, Local: f.LocalNetworks(), Owners: map[string]string{}, Error: f.lastErr}
	if d, ok := f.readDraft(); ok {
		v.Draft = &d
	}
	if f.pending != nil {
		p := *f.pending
		v.Pending = &p
	}
	for _, fam := range firewall.Families {
		v.Frame[fam] = firewall.Frame(fam)
	}
	for _, r := range append(append([]firewall.Rule{}, c.Rules...), draftRules(v.Draft)...) {
		if r.Owner != "" {
			v.Owners[r.Owner] = firewall.OwnerLabel(r.Owner)
		}
	}
	return v
}

func draftRules(d *firewall.Config) []firewall.Rule {
	if d == nil {
		return nil
	}
	return d.Rules
}

// ErrFirewallPending is a change while an Apply waits for its Confirm.
var ErrFirewallPending = errors.New("a firewall change is waiting for its confirmation")

// SetDraft stores the page's draft: the policy, the rules and the IPv6 local networks (B-339). An
// owned rule keeps its owner only for a port its owner has (a duplicate keeps it, a made-up one
// does not); the owners' bookkeeping is the file's, never the page's.
func (f *FirewallRules) SetDraft(policy firewall.Policy, rules []firewall.Rule, localV6 []string) (firewall.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending != nil {
		return firewall.Config{}, ErrFirewallPending
	}
	c, _ := f.Root.ReadRules()
	d := c.Clone()
	d.Policy = policy
	d.LocalV6 = nil
	if len(localV6) > 0 {
		d.LocalV6 = append([]string{}, localV6...)
	}
	d.Rules = []firewall.Rule{}
	owned := map[string]bool{}
	for _, k := range c.Active {
		owned[k] = true
	}
	for _, r := range rules {
		if r.ID == "" {
			r.ID = firewall.NewID()
		}
		// the key carries the range (task 105): a rule edited into another range is the user's
		if r.Owner != "" && !owned[firewall.RuleKey(r)] {
			r.Owner = ""
		}
		d.Rules = append(d.Rules, r)
	}
	if err := d.Validate(); err != nil {
		return firewall.Config{}, err
	}
	f.writeDraft(&d)
	return d, nil
}

// DiscardDraft drops the draft.
func (f *FirewallRules) DiscardDraft() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending != nil {
		return ErrFirewallPending
	}
	f.writeDraft(nil)
	return nil
}

// Apply loads the draft with the confirm window. Nothing is written until Confirm; when the window
// runs out the helper puts the old rules back and the draft stays for another try.
func (f *FirewallRules) Apply(ctx context.Context) (*FirewallPending, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending != nil {
		return nil, ErrFirewallPending
	}
	d, ok := f.readDraft()
	if !ok {
		return nil, errors.New("there is no draft to apply")
	}
	v4, v6, err := renderBoth(d, f.LocalNetworks())
	if err != nil {
		return nil, err
	}
	w := f.window()
	if err := Priv.FirewallLoad(ctx, v4, v6, w); err != nil {
		return nil, err
	}
	now := time.Now()
	f.loadedAt = now
	p := &FirewallPending{Started: now, Deadline: now.Add(w)}
	f.pending = p
	f.log().Warn("firewall: the draft is loaded, waiting for its confirmation", "window", w)
	time.AfterFunc(w+2*time.Second, func() {
		f.mu.Lock()
		if f.pending == p {
			f.pending = nil
			f.lastErr = "the change was not confirmed in time; the rules before it are back"
			f.log().Warn("firewall: not confirmed in time, the helper put the rules back")
		}
		f.mu.Unlock()
	})
	out := *p
	return &out, nil
}

// Confirm keeps the applied draft: the helper's window closes and the draft becomes the file.
func (f *FirewallRules) Confirm(ctx context.Context) error {
	f.mu.Lock()
	if f.pending == nil {
		f.mu.Unlock()
		return errors.New("no firewall change is waiting for its confirmation")
	}
	d, ok := f.readDraft()
	if err := Priv.FirewallConfirm(ctx); err != nil {
		f.mu.Unlock()
		return err
	}
	f.pending, f.lastErr = nil, ""
	if ok {
		if err := f.Root.WriteRules(d); err != nil {
			f.mu.Unlock()
			return err
		}
		f.writeDraft(nil)
	}
	f.mu.Unlock()
	f.log().Info("firewall: the change is confirmed")
	return f.Sync(ctx, false)
}

// Revert ends an open window now: the rules before it are back, the draft stays.
func (f *FirewallRules) Revert(ctx context.Context) error {
	f.mu.Lock()
	if f.pending == nil {
		f.mu.Unlock()
		return errors.New("no firewall change is waiting for its confirmation")
	}
	err := Priv.FirewallRevert(ctx)
	f.pending = nil
	f.mu.Unlock()
	return err
}

// DismissMigration hides the conversion's notice.
func (f *FirewallRules) DismissMigration() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.Root.ReadRules()
	if !ok || c.Migration == nil {
		return nil
	}
	c.Migration.Dismissed = true
	return f.Root.WriteRules(c)
}
