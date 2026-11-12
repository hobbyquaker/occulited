package system

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// openccu-lite task 227: IPv6 per interface - each mode's commands, the rollback, the store.

type cmdLog struct {
	mu   sync.Mutex
	cmds []string
}

func (l *cmdLog) run(_ context.Context, name string, args ...string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cmds = append(l.cmds, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	return nil, nil
}

// conf records an IPv6 sysctl write (the helper's ipv6-conf, B-139) in the same log, so the
// order against the commands stays visible.
func (l *cmdLog) conf(_ context.Context, iface, key string, v int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cmds = append(l.cmds, conf(iface, key, v))
	return nil
}

func (l *cmdLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.cmds
	l.cmds = nil
	return c
}

func conf(iface, key string, v int) string {
	return "ipv6-conf " + iface + " " + key + "=" + string(rune('0'+v))
}

func TestIPv6Validate(t *testing.T) {
	cases := []struct {
		s    IPv6Settings
		ok   bool
		want IPv6Settings
	}{
		{IPv6Settings{Mode: "off", Address: "x"}, true, IPv6Settings{Mode: "off"}},
		{IPv6Settings{Mode: "slaac"}, true, IPv6Settings{Mode: "slaac"}},
		{IPv6Settings{Mode: "dhcpv6"}, true, IPv6Settings{Mode: "dhcpv6"}},
		{IPv6Settings{Mode: "static", Address: "2001:DB8::10", Gateway: "FE80::1", DNS: []string{"2001:db8::53"}}, true, IPv6Settings{Mode: "static", Address: "2001:db8::10", Prefix: 64, Gateway: "fe80::1", DNS: []string{"2001:db8::53"}}},
		{IPv6Settings{Mode: "static", Address: "fd00::5", Prefix: 48}, true, IPv6Settings{Mode: "static", Address: "fd00::5", Prefix: 48}},
		{IPv6Settings{Mode: "static", Address: "fe80::5"}, false, IPv6Settings{}},
		{IPv6Settings{Mode: "static", Address: "192.0.2.1"}, false, IPv6Settings{}},
		{IPv6Settings{Mode: "static", Address: "2001:db8::1", Prefix: 129}, false, IPv6Settings{}},
		{IPv6Settings{Mode: "static", Address: "2001:db8::1", Gateway: "192.0.2.1"}, false, IPv6Settings{}},
		{IPv6Settings{Mode: "static", Address: "2001:db8::1", DNS: []string{"1.1.1.1"}}, false, IPv6Settings{}},
		{IPv6Settings{Mode: "static", Address: "2001:db8::1", DNS: []string{"::1", "::2", "::3", "::4"}}, false, IPv6Settings{}},
		{IPv6Settings{Mode: "dhcp"}, false, IPv6Settings{}},
	}
	for _, c := range cases {
		s := c.s
		err := s.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.s, err)
			continue
		}
		if c.ok && !s.same(c.want) {
			t.Errorf("%+v: normalised to %+v, want %+v", c.s, s, c.want)
		}
	}
	for _, n := range []string{"eth0", "wlan0", "eth0.10", "br-lan"} {
		if !ValidIface(n) {
			t.Errorf("%s refused", n)
		}
	}
	for _, n := range []string{"all", "default", "lo", "", "Eth0", "eth0/..", "averyveryverylongname"} {
		if ValidIface(n) {
			t.Errorf("%s accepted", n)
		}
	}
}

// Each mode's commands, IPv4 never among them.
func TestIPv6Apply(t *testing.T) {
	root := Root(t.TempDir())
	l := &cmdLog{}
	a := IPv6Applier{Root: root, Run: l.run, Conf: l.conf}
	ctx := context.Background()
	slaac, off, dhcp := IPv6Settings{Mode: "slaac"}, IPv6Settings{Mode: "off"}, IPv6Settings{Mode: "dhcpv6"}
	static := IPv6Settings{Mode: "static", Address: "2001:db8::10", Prefix: 64, Gateway: "fe80::1", DNS: []string{"2001:db8::53"}}

	must := func(prev, next IPv6Settings, want []string) {
		t.Helper()
		if err := a.Apply(ctx, "eth0", prev, next); err != nil {
			t.Fatal(err)
		}
		got := l.take()
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s -> %s:\n got  %q\n want %q", prev.Mode, next.Mode, got, want)
		}
		for _, c := range got {
			if strings.Contains(c, "-4") || strings.Contains(c, "ipv4") || strings.Contains(c, "udhcpc ") || strings.Contains(c, "ifconfig") {
				t.Errorf("an IPv4 command: %s", c)
			}
		}
	}
	must(slaac, off, []string{conf("eth0", "disable_ipv6", 1)})
	must(off, slaac, []string{conf("eth0", "accept_ra", 1), conf("eth0", "autoconf", 1), conf("eth0", "disable_ipv6", 0)})
	must(slaac, static, []string{
		conf("eth0", "accept_ra", 0), conf("eth0", "autoconf", 0), conf("eth0", "disable_ipv6", 1), conf("eth0", "disable_ipv6", 0),
		"/sbin/ip -6 addr replace 2001:db8::10/64 dev eth0",
		"/sbin/ip -6 route replace default via fe80::1 dev eth0",
		"/sbin/resolvconf -a eth0.ipv6 <nameserver 2001:db8::53",
	})
	// a static setup that only changes its address keeps the link up
	moved := static
	moved.Address, moved.Gateway, moved.DNS = "2001:db8::11", "", nil
	must(static, moved, []string{
		"/sbin/ip -6 route del default via fe80::1 dev eth0",
		"/sbin/ip -6 addr del 2001:db8::10/64 dev eth0",
		"/sbin/resolvconf -d eth0.ipv6",
		conf("eth0", "accept_ra", 0), conf("eth0", "autoconf", 0), conf("eth0", "disable_ipv6", 0),
		"/sbin/ip -6 addr replace 2001:db8::11/64 dev eth0",
	})
	// DHCPv6: the image's script, a pid file per interface
	must(slaac, dhcp, []string{
		conf("eth0", "accept_ra", 1), conf("eth0", "autoconf", 0), conf("eth0", "disable_ipv6", 1), conf("eth0", "disable_ipv6", 0),
		"/sbin/resolvconf -d eth0.dhcp6",
		"/sbin/udhcpc6 -b -S -t 5 -T 3 -O dns -O search -i eth0 -s /usr/libexec/occu/lite-dhcp6 -p /var/run/udhcpc6_eth0.pid",
	})
	// on systemd the client is a transient unit, stopped as one
	a.Systemd = true
	must(off, dhcp, []string{
		conf("eth0", "accept_ra", 1), conf("eth0", "autoconf", 0), conf("eth0", "disable_ipv6", 0),
		"systemctl stop occu-dhcp6-eth0.service", "/sbin/resolvconf -d eth0.dhcp6",
		"systemd-run --unit=occu-dhcp6-eth0.service --collect --quiet /sbin/udhcpc6 -f -S -t 5 -T 3 -O dns -O search -i eth0 -s /usr/libexec/occu/lite-dhcp6 -p /var/run/udhcpc6_eth0.pid",
	})
	a.Systemd = false
	// leaving DHCPv6: the client, the address its script noted, its nameservers
	_ = os.MkdirAll(filepath.Join(string(root), "var/run"), 0o755)
	_ = os.MkdirAll(filepath.Join(string(root), "run/occulite"), 0o755)
	_ = os.WriteFile(filepath.Join(string(root), "var/run/udhcpc6_eth0.pid"), []byte("4242\n"), 0o644)
	_ = os.WriteFile(filepath.Join(string(root), "run/occulite/dhcp6-eth0"), []byte("2001:db8::77/128\n"), 0o644)
	must(dhcp, slaac, []string{
		"kill 4242", "/sbin/ip -6 addr del 2001:db8::77/128 dev eth0", "/sbin/resolvconf -d eth0.dhcp6",
		conf("eth0", "accept_ra", 1), conf("eth0", "autoconf", 1), conf("eth0", "disable_ipv6", 1), conf("eth0", "disable_ipv6", 0),
	})
	if err := a.Apply(ctx, "all", slaac, off); err == nil {
		t.Error("the interface all was accepted")
	}
	if err := a.Apply(ctx, "eth0", slaac, IPv6Settings{Mode: "static", Address: "fe80::1"}); err == nil || len(l.take()) != 0 {
		t.Errorf("an invalid setting ran commands: %v", err)
	}
}

func TestIPv6Live(t *testing.T) {
	root := t.TempDir()
	a := IPv6Applier{Root: Root(root)}
	set := func(dis, ra, auto string) {
		d := filepath.Join(root, "proc/sys/net/ipv6/conf/eth0")
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "disable_ipv6"), []byte(dis+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "accept_ra"), []byte(ra+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "autoconf"), []byte(auto+"\n"), 0o644)
	}
	set("0", "1", "1")
	if a.Live("eth0").Mode != "slaac" {
		t.Error("slaac")
	}
	set("1", "1", "1")
	if a.Live("eth0").Mode != "off" {
		t.Error("off")
	}
	set("0", "1", "0")
	_ = os.MkdirAll(filepath.Join(root, "var/run"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "var/run/udhcpc6_eth0.pid"), []byte("1\n"), 0o644)
	if a.Live("eth0").Mode != "dhcpv6" {
		t.Error("dhcpv6")
	}
}

func newTx(t *testing.T, window time.Duration) (*IPv6Tx, *cmdLog, string) {
	dir := t.TempDir()
	l := &cmdLog{}
	tx := &IPv6Tx{Applier: IPv6Applier{Root: Root(dir), Run: l.run, Conf: l.conf}, Store: IPv6Store{Path: filepath.Join(dir, "ipv6.json")}, PendingPath: filepath.Join(dir, "ipv6.pending.json"), Window: window, Exists: func(n string) bool { return n == "eth0" }}
	return tx, l, dir
}

// Begin, then Confirm: stored, applied at the next start. Begin, then no confirmation: back.
func TestIPv6TxConfirmAndRollback(t *testing.T) {
	ctx := context.Background()
	tx, l, _ := newTx(t, 150*time.Millisecond)
	if _, err := tx.Begin(ctx, "eth9", IPv6Settings{Mode: "off"}); err == nil {
		t.Error("an interface that is not there")
	}
	if p, err := tx.Begin(ctx, "eth0", IPv6Settings{Mode: "slaac"}); p != nil || err != nil {
		t.Errorf("no change: %v %v", p, err)
	}
	p, err := tx.Begin(ctx, "eth0", IPv6Settings{Mode: "off"})
	if err != nil || p == nil || p.Previous.Mode != "slaac" || p.Deadline.Sub(p.Started) != 150*time.Millisecond {
		t.Fatalf("begin: %+v %v", p, err)
	}
	if _, err := tx.Begin(ctx, "eth0", IPv6Settings{Mode: "dhcpv6"}); err != ErrIPv6Pending {
		t.Errorf("a second change: %v", err)
	}
	if err := tx.Confirm("wrong"); err != ErrNoIPv6Tx {
		t.Errorf("a wrong token: %v", err)
	}
	if err := tx.Confirm(p.Token); err != nil {
		t.Fatal(err)
	}
	if tx.Pending() != nil {
		t.Error("still pending")
	}
	if m, _ := tx.Store.Load(); m["eth0"].Mode != "off" {
		t.Errorf("stored: %+v", m)
	}
	if tx.Current("eth0").Mode != "off" {
		t.Error("current")
	}
	l.take()
	time.Sleep(250 * time.Millisecond)
	if c := l.take(); len(c) != 0 {
		t.Errorf("a confirmed change was rolled back: %q", c)
	}
	// no confirmation: rolled back to off, nothing stored
	p, err = tx.Begin(ctx, "eth0", IPv6Settings{Mode: "static", Address: "2001:db8::10"})
	if err != nil {
		t.Fatal(err)
	}
	l.take()
	deadline := time.Now().Add(2 * time.Second)
	for tx.Pending() != nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := strings.Join(l.take(), "\n")
	if tx.Pending() != nil || !strings.Contains(got, "/sbin/ip -6 addr del 2001:db8::10/64 dev eth0") || !strings.HasSuffix(got, conf("eth0", "disable_ipv6", 1)) {
		t.Errorf("rollback: %q", got)
	}
	if m, _ := tx.Store.Load(); m["eth0"].Mode != "off" {
		t.Errorf("stored after the rollback: %+v", m)
	}
	if err := tx.Confirm(p.Token); err != ErrNoIPv6Tx {
		t.Errorf("a confirmation after the rollback: %v", err)
	}
}

func TestIPv6TxRevertAndRestart(t *testing.T) {
	ctx := context.Background()
	tx, l, dir := newTx(t, time.Minute)
	p, err := tx.Begin(ctx, "eth0", IPv6Settings{Mode: "dhcpv6"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tx.PendingPath); err != nil {
		t.Fatal("the pending change is not noted")
	}
	l.take()
	if err := tx.Revert(ctx, p.Token); err != nil {
		t.Fatal(err)
	}
	if c := strings.Join(l.take(), "\n"); !strings.HasSuffix(c, conf("eth0", "disable_ipv6", 0)) || !strings.Contains(c, conf("eth0", "autoconf", 1)) {
		t.Errorf("revert: %q", c)
	}
	if _, err := os.Stat(tx.PendingPath); err == nil {
		t.Error("the note stayed after the revert")
	}
	// occulited stops inside the window: the next start rolls the change back
	if _, err := tx.Begin(ctx, "eth0", IPv6Settings{Mode: "off"}); err != nil {
		t.Fatal(err)
	}
	l.take()
	next := &IPv6Tx{Applier: IPv6Applier{Root: Root(dir), Run: l.run, Conf: l.conf}, Store: tx.Store, PendingPath: tx.PendingPath, Exists: tx.Exists}
	next.RecoverPending(ctx)
	if c := strings.Join(l.take(), "\n"); !strings.Contains(c, conf("eth0", "autoconf", 1)) || !strings.HasSuffix(c, conf("eth0", "disable_ipv6", 0)) {
		t.Errorf("recovered: %q", c)
	}
	if _, err := os.Stat(tx.PendingPath); err == nil {
		t.Error("the note stayed after the recovery")
	}
	next.RecoverPending(ctx)
	if c := l.take(); len(c) != 0 {
		t.Errorf("a second recovery: %q", c)
	}
}

func TestIPv6ApplyStored(t *testing.T) {
	ctx := context.Background()
	tx, l, _ := newTx(t, time.Minute)
	if err := tx.Store.Save(map[string]IPv6Settings{"eth0": {Mode: "static", Address: "fd00::5", Prefix: 64}, "wlan0": {Mode: "off"}, "all": {Mode: "off"}}); err != nil {
		t.Fatal(err)
	}
	tx.ApplyStored(ctx)
	got := strings.Join(l.take(), "\n")
	if !strings.Contains(got, "/sbin/ip -6 addr replace fd00::5/64 dev eth0") || strings.Contains(got, "wlan0") || strings.Contains(got, "/all/") {
		t.Errorf("applied: %q", got)
	}
	s := tx.Settings([]string{"eth0", "wlan0", "lo"})
	if s["eth0"].Mode != "static" || s["wlan0"].Mode != "off" || len(s) != 2 {
		t.Errorf("settings: %+v", s)
	}
	// a broken store: the kernel's default stays, nothing runs
	_ = os.WriteFile(tx.Store.Path, []byte("{"), 0o640)
	tx.ApplyStored(ctx)
	if c := l.take(); len(c) != 0 {
		t.Errorf("a broken store ran %q", c)
	}
}
