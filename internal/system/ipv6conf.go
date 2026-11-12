package system

// openccu-lite task 227 (the maintainer: "for IPv6: shouldnt the panel also offer a selection
// static, dhcp, slaac ?"): IPv6 per interface - off, SLAAC, DHCPv6 or static - applied live with a
// rollback: after Begin the page has 60 s to confirm over the new configuration, else the previous
// one comes back by itself. IPv4 is never touched.
//
// What each mode is, on the kernel and the image:
//   - off:    disable_ipv6=1 (every IPv6 address of the interface goes, link-local included)
//   - slaac:  disable_ipv6=0, accept_ra=1, autoconf=1 - the kernel's default
//   - dhcpv6: disable_ipv6=0, accept_ra=1 (the default route comes from the router's
//     announcements), autoconf=0, and busybox's udhcpc6 with the image's script
//     /usr/libexec/occu/lite-dhcp6, which sets the leased address and its nameservers and leaves
//     IPv4 alone (busybox's default script would flush the IPv4 address)
//   - static: disable_ipv6=0, accept_ra=0, autoconf=0, the address, a default route when a
//     gateway is given, and the nameservers as a resolvconf record <interface>.ipv6
//
// The confirmed configuration is kept in the state directory (ipv6.json) and applied at every
// start of occulited; an interface it does not name keeps the kernel's default (SLAAC). A pending
// change is noted beside it (ipv6.pending.json) until it is confirmed or rolled back: occulited
// restarting inside the window finds the note at its start and rolls the change back then.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IPv6 modes.
const (
	IPv6Off    = "off"
	IPv6SLAAC  = "slaac"
	IPv6DHCPv6 = "dhcpv6"
	IPv6Static = "static"
)

// DHCP6Script is the image's udhcpc6 script (the fork's overlay).
const DHCP6Script = "/usr/libexec/occu/lite-dhcp6"

// IPv6Window is how long a change waits for its confirmation.
const IPv6Window = 60 * time.Second

// IPv6Settings is one interface's IPv6 configuration.
type IPv6Settings struct {
	Mode string `json:"mode"`
	// static only
	Address string   `json:"address,omitempty"`
	Prefix  int      `json:"prefix,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

var ifaceNameRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,14}$`)

// ValidIface is a kernel interface name the settings may name - never "all", "default" or "lo".
func ValidIface(name string) bool {
	return ifaceNameRe.MatchString(name) && name != "all" && name != "default" && name != "lo"
}

// Validate checks s; a static address must be a global or unique-local unicast address, the
// gateway an IPv6 address (link-local is the usual router), the servers IPv6 addresses.
func (s *IPv6Settings) Validate() error {
	switch s.Mode {
	case IPv6Off, IPv6SLAAC, IPv6DHCPv6:
		s.Address, s.Prefix, s.Gateway, s.DNS = "", 0, "", nil
		return nil
	case IPv6Static:
	default:
		return errors.New("mode: off, slaac, dhcpv6 or static")
	}
	ip := net.ParseIP(strings.TrimSpace(s.Address))
	if ip == nil || ip.To4() != nil {
		return errors.New("address: not an IPv6 address")
	}
	if !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
		return errors.New("address: a global or unique-local unicast address")
	}
	s.Address = ip.String()
	if s.Prefix == 0 {
		s.Prefix = 64
	}
	if s.Prefix < 1 || s.Prefix > 128 {
		return errors.New("prefix: 1 to 128")
	}
	if g := strings.TrimSpace(s.Gateway); g != "" {
		gw := net.ParseIP(g)
		if gw == nil || gw.To4() != nil {
			return errors.New("gateway: not an IPv6 address")
		}
		s.Gateway = gw.String()
	}
	if len(s.DNS) > 3 {
		return errors.New("dns: at most three servers")
	}
	for i, d := range s.DNS {
		ip := net.ParseIP(strings.TrimSpace(d))
		if ip == nil || ip.To4() != nil {
			return fmt.Errorf("dns: %q is not an IPv6 address", d)
		}
		s.DNS[i] = ip.String()
	}
	return nil
}

func (s IPv6Settings) same(o IPv6Settings) bool {
	return s.Mode == o.Mode && s.Address == o.Address && s.Prefix == o.Prefix && s.Gateway == o.Gateway && strings.Join(s.DNS, " ") == strings.Join(o.DNS, " ")
}

// IPv6Applier changes one interface's live IPv6 configuration.
type IPv6Applier struct {
	Root Root
	Run  Runner
	// Conf writes one of an interface's IPv6 sysctls; nil = the helper's (Priv.SetIPv6Conf).
	Conf func(ctx context.Context, iface, key string, v int) error
	// Systemd: the DHCPv6 client runs as a transient unit (occu-dhcp6-<interface>.service)
	Systemd bool
}

func dhcp6Unit(iface string) string { return "occu-dhcp6-" + iface + ".service" }

func (a IPv6Applier) run() Runner {
	if a.Run == nil {
		return ExecRunner
	}
	return a.Run
}

// setConf writes one of the interface's IPv6 sysctls. A procfs file cannot be written by
// rename, so it is the helper's ipv6-conf operation (occulited B-139: before, its one fixed shell
// shape), or Conf where a test or a development root sets one.
func (a IPv6Applier) setConf(ctx context.Context, iface, key string, v int) error {
	set := a.Conf
	if set == nil {
		set = Priv.SetIPv6Conf
	}
	if err := set(ctx, iface, key, v); err != nil {
		return fmt.Errorf("%s %s=%d: %w", iface, key, v, err)
	}
	return nil
}

// DryIPv6Conf logs instead of writing, as DryRunner does: an IPv6Applier's Conf under --root.
func DryIPv6Conf(_ context.Context, iface, key string, v int) error {
	slog.Info("dry run", "ipv6-conf", iface, "key", key, "value", v)
	return nil
}

func dhcp6PidFile(iface string) string { return "/var/run/udhcpc6_" + iface + ".pid" }

// dhcp6LeaseFile is where the image's script notes the address it set.
func dhcp6LeaseFile(iface string) string { return "/run/occulite/dhcp6-" + iface }

// undo removes what prev added on its own: a static address, its route and nameservers; a DHCPv6
// client, its address and nameservers. Errors are logged, not returned: the new mode is applied
// either way.
func (a IPv6Applier) undo(ctx context.Context, iface string, prev IPv6Settings) {
	run := a.run()
	switch prev.Mode {
	case IPv6Static:
		if prev.Gateway != "" {
			_, _ = run(ctx, "/sbin/ip", "-6", "route", "del", "default", "via", prev.Gateway, "dev", iface)
		}
		if prev.Address != "" {
			_, _ = run(ctx, "/sbin/ip", "-6", "addr", "del", prev.Address+"/"+strconv.Itoa(prev.Prefix), "dev", iface)
		}
		_, _ = run(ctx, "/sbin/resolvconf", "-d", iface+".ipv6")
	case IPv6DHCPv6:
		a.stopDHCP6(ctx, iface)
	}
}

func (a IPv6Applier) stopDHCP6(ctx context.Context, iface string) {
	run := a.run()
	if a.Systemd {
		_, _ = run(ctx, "systemctl", "stop", dhcp6Unit(iface))
	}
	if pid := pidFromFile(a.Root.join(dhcp6PidFile(iface))); pid > 0 {
		_, _ = run(ctx, "kill", strconv.Itoa(pid))
	}
	if addr := strings.TrimSpace(readFile(a.Root.join(dhcp6LeaseFile(iface)))); addr != "" {
		_, _ = run(ctx, "/sbin/ip", "-6", "addr", "del", addr, "dev", iface)
	}
	_, _ = run(ctx, "/sbin/resolvconf", "-d", iface+".dhcp6")
}

// Apply takes iface from prev to next.
func (a IPv6Applier) Apply(ctx context.Context, iface string, prev, next IPv6Settings) error {
	if !ValidIface(iface) {
		return fmt.Errorf("interface %q", iface)
	}
	if err := next.Validate(); err != nil {
		return err
	}
	a.undo(ctx, iface, prev)
	if next.Mode == IPv6Off {
		return a.setConf(ctx, iface, "disable_ipv6", 1)
	}
	ra, auto := 1, 1
	switch next.Mode {
	case IPv6DHCPv6:
		auto = 0
	case IPv6Static:
		ra, auto = 0, 0
	}
	if err := a.setConf(ctx, iface, "accept_ra", ra); err != nil {
		return err
	}
	if err := a.setConf(ctx, iface, "autoconf", auto); err != nil {
		return err
	}
	// another mode starts clean: switching IPv6 off and on drops what the old one left - the
	// addresses SLAAC took and the routes the announcements set - and brings a fresh link-local
	// address. A static setup that only changes its address keeps the link up.
	if prev.Mode != next.Mode && prev.Mode != IPv6Off {
		if err := a.setConf(ctx, iface, "disable_ipv6", 1); err != nil {
			return err
		}
	}
	if err := a.setConf(ctx, iface, "disable_ipv6", 0); err != nil {
		return err
	}
	run := a.run()
	switch next.Mode {
	case IPv6DHCPv6:
		// one client per interface: a leftover of an earlier start goes first
		a.stopDHCP6(ctx, iface)
		// the client runs on in a transient unit of its own: started directly, it keeps the
		// helper's output pipes open, and against a server that only answers "no addresses
		// available" it never goes to the background at all (seen on the lab's router)
		args := []string{"-f", "-S", "-t", "5", "-T", "3", "-O", "dns", "-O", "search", "-i", iface, "-s", DHCP6Script, "-p", dhcp6PidFile(iface)}
		name, cmd := "/sbin/udhcpc6", args
		if a.Systemd {
			name, cmd = "systemd-run", append([]string{"--unit=" + dhcp6Unit(iface), "--collect", "--quiet", "/sbin/udhcpc6"}, args...)
		} else {
			cmd = append([]string{"-b"}, args[1:]...)
		}
		if out, err := run(ctx, name, cmd...); err != nil {
			return fmt.Errorf("udhcpc6: %w: %s", err, strings.TrimSpace(string(out)))
		}
	case IPv6Static:
		if out, err := run(ctx, "/sbin/ip", "-6", "addr", "replace", next.Address+"/"+strconv.Itoa(next.Prefix), "dev", iface); err != nil {
			return fmt.Errorf("address: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if next.Gateway != "" {
			if out, err := run(ctx, "/sbin/ip", "-6", "route", "replace", "default", "via", next.Gateway, "dev", iface); err != nil {
				return fmt.Errorf("default route: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
		if len(next.DNS) > 0 {
			var rec strings.Builder
			for _, d := range next.DNS {
				rec.WriteString("nameserver " + d + "\n")
			}
			if err := (NetApplier{Root: a.Root, Run: a.Run}).resolvconfAdd(ctx, []string{"-a", iface + ".ipv6"}, rec.String()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Live is what the kernel says of iface when nothing is stored for it: off when IPv6 is
// disabled, SLAAC when it takes addresses from announcements, else static without a known
// address (set by hand outside occulited).
func (a IPv6Applier) Live(iface string) IPv6Settings {
	conf := func(k string) string {
		return strings.TrimSpace(readFile(a.Root.join("/proc/sys/net/ipv6/conf/" + iface + "/" + k)))
	}
	switch {
	case conf("disable_ipv6") == "1":
		return IPv6Settings{Mode: IPv6Off}
	case conf("autoconf") == "0" && conf("accept_ra") != "0":
		if _, err := os.Stat(a.Root.join(dhcp6PidFile(iface))); err == nil {
			return IPv6Settings{Mode: IPv6DHCPv6}
		}
	}
	return IPv6Settings{Mode: IPv6SLAAC}
}

// IPv6Store is the confirmed configuration: interface → settings.
type IPv6Store struct{ Path string }

// Load reads the store; a missing file is an empty configuration.
func (s IPv6Store) Load() (map[string]IPv6Settings, error) {
	out := map[string]IPv6Settings{}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]IPv6Settings{}, fmt.Errorf("%s: %w", s.Path, err)
	}
	return out, nil
}

// Save writes the store atomically.
func (s IPv6Store) Save(m map[string]IPv6Settings) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o750); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// IPv6Pending is the open change.
type IPv6Pending struct {
	Token     string       `json:"token"`
	Interface string       `json:"interface"`
	Settings  IPv6Settings `json:"settings"`
	Previous  IPv6Settings `json:"previous"`
	Started   time.Time    `json:"started"`
	Deadline  time.Time    `json:"deadline"`
}

// IPv6Tx is the confirm-or-rollback transaction, one at a time.
type IPv6Tx struct {
	Applier IPv6Applier
	Store   IPv6Store
	// PendingPath notes the open change across a restart; "" = not noted.
	PendingPath string
	Window      time.Duration // default IPv6Window
	// Exists says whether an interface is there; nil = /sys/class/net/<name> under the root.
	Exists func(string) bool
	Log    *slog.Logger

	mu      sync.Mutex
	pending *IPv6Pending
	timer   *time.Timer
}

// ErrIPv6Pending is Begin while a change waits.
var ErrIPv6Pending = errors.New("an IPv6 change is already waiting for confirmation")

// ErrNoIPv6Tx is Confirm or Revert with no or another token.
var ErrNoIPv6Tx = errors.New("no such pending IPv6 change")

func (t *IPv6Tx) log() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

func (t *IPv6Tx) exists(iface string) bool {
	if t.Exists != nil {
		return t.Exists(iface)
	}
	_, err := os.Stat(t.Applier.Root.join("/sys/class/net/" + iface))
	return err == nil
}

// Current is iface's configuration: the stored one, else what the kernel shows.
func (t *IPv6Tx) Current(iface string) IPv6Settings {
	if m, err := t.Store.Load(); err == nil {
		if s, ok := m[iface]; ok {
			return s
		}
	}
	return t.Applier.Live(iface)
}

// Settings is every interface's configuration the store names, and whether it came from there.
func (t *IPv6Tx) Settings(ifaces []string) map[string]IPv6Settings {
	out := map[string]IPv6Settings{}
	for _, i := range ifaces {
		if ValidIface(i) {
			out[i] = t.Current(i)
		}
	}
	return out
}

// Begin applies s to iface live and arms the rollback.
func (t *IPv6Tx) Begin(ctx context.Context, iface string, s IPv6Settings) (*IPv6Pending, error) {
	if !ValidIface(iface) || !t.exists(iface) {
		return nil, fmt.Errorf("interface: no interface %q", iface)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending != nil {
		return nil, ErrIPv6Pending
	}
	prev := t.Current(iface)
	if prev.same(s) {
		return nil, nil
	}
	if err := t.Applier.Apply(ctx, iface, prev, s); err != nil {
		_ = t.Applier.Apply(context.Background(), iface, s, prev)
		return nil, err
	}
	w := t.Window
	if w == 0 {
		w = IPv6Window
	}
	now := time.Now()
	p := &IPv6Pending{Token: randomToken(), Interface: iface, Settings: s, Previous: prev, Started: now, Deadline: now.Add(w)}
	t.pending = p
	t.notePending(p)
	t.timer = time.AfterFunc(w, func() { t.expire(p.Token) })
	t.log().Warn("network: IPv6 applied, waiting for confirmation", "interface", iface, "mode", s.Mode, "from", prev.Mode, "window", w)
	return p, nil
}

// Pending is the open change, if any.
func (t *IPv6Tx) Pending() *IPv6Pending {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		return nil
	}
	p := *t.pending
	return &p
}

func (t *IPv6Tx) take(token string) *IPv6Pending {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.pending
	if p == nil || p.Token != token {
		return nil
	}
	if t.timer != nil {
		t.timer.Stop()
	}
	t.pending, t.timer = nil, nil
	t.notePending(nil)
	return p
}

func (t *IPv6Tx) notePending(p *IPv6Pending) {
	if t.PendingPath == "" {
		return
	}
	if p == nil {
		_ = os.Remove(t.PendingPath)
		return
	}
	b, _ := json.Marshal(p)
	tmp := t.PendingPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err == nil {
		_ = os.Rename(tmp, t.PendingPath)
	}
}

// RecoverPending rolls back a change that was still waiting when occulited stopped: at start,
// before ApplyStored.
func (t *IPv6Tx) RecoverPending(ctx context.Context) {
	if t.PendingPath == "" {
		return
	}
	b, err := os.ReadFile(t.PendingPath)
	if err != nil {
		return
	}
	_ = os.Remove(t.PendingPath)
	var p IPv6Pending
	if json.Unmarshal(b, &p) != nil || !ValidIface(p.Interface) {
		return
	}
	t.log().Warn("network: an IPv6 change was still waiting for confirmation at the last stop; rolled back", "interface", p.Interface, "to", p.Previous.Mode)
	if err := t.Applier.Apply(ctx, p.Interface, p.Settings, p.Previous); err != nil {
		t.log().Error("network: the IPv6 rollback failed", "interface", p.Interface, "err", err)
	}
}

func (t *IPv6Tx) expire(token string) {
	p := t.take(token)
	if p == nil {
		return
	}
	t.log().Warn("network: IPv6 not confirmed in time, rolled back", "interface", p.Interface, "to", p.Previous.Mode)
	if err := t.Applier.Apply(context.Background(), p.Interface, p.Settings, p.Previous); err != nil {
		t.log().Error("network: the IPv6 rollback failed", "interface", p.Interface, "err", err)
	}
}

// Confirm keeps the pending change: it is stored and applied at every start.
func (t *IPv6Tx) Confirm(token string) error {
	p := t.take(token)
	if p == nil {
		return ErrNoIPv6Tx
	}
	m, err := t.Store.Load()
	if err != nil {
		t.log().Warn("network: the stored IPv6 configuration was unreadable and is written anew", "err", err)
		m = map[string]IPv6Settings{}
	}
	m[p.Interface] = p.Settings
	if err := t.Store.Save(m); err != nil {
		return err
	}
	t.log().Info("network: IPv6 confirmed", "interface", p.Interface, "mode", p.Settings.Mode)
	return nil
}

// Revert rolls the pending change back now.
func (t *IPv6Tx) Revert(ctx context.Context, token string) error {
	p := t.take(token)
	if p == nil {
		return ErrNoIPv6Tx
	}
	t.log().Info("network: IPv6 change reverted by the user", "interface", p.Interface, "to", p.Previous.Mode)
	return t.Applier.Apply(ctx, p.Interface, p.Settings, p.Previous)
}

// ApplyStored applies the confirmed configuration of every interface that is there: at start.
func (t *IPv6Tx) ApplyStored(ctx context.Context) {
	m, err := t.Store.Load()
	if err != nil {
		t.log().Warn("network: the stored IPv6 configuration is unreadable; the kernel's default stays", "err", err)
		return
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, iface := range names {
		s := m[iface]
		if !ValidIface(iface) || !t.exists(iface) {
			continue
		}
		if err := t.Applier.Apply(ctx, iface, t.Applier.Live(iface), s); err != nil {
			t.log().Warn("network: the stored IPv6 configuration could not be applied", "interface", iface, "mode", s.Mode, "err", err)
			continue
		}
		t.log().Info("network: IPv6 configured", "interface", iface, "mode", s.Mode)
	}
}
