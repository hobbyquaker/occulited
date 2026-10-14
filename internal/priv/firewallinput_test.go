package priv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// B-152: the INPUT read is one narrow operation - a family, the command line built by the helper -
// and every other way of asking is refused before anything runs

type recordFirewall struct {
	Local
	families []string
}

func (r *recordFirewall) FirewallInput(_ context.Context, family string) (Result, error) {
	r.families = append(r.families, family)
	return Result{Stdout: []byte("-P INPUT DROP\n")}, nil
}

func firewallHelper(t *testing.T) (Client, *recordFirewall, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ops := &recordFirewall{}
	srv := &Server{Policy: DefaultPolicy("/", "/usr/local/etc/occulite"), Ops: ops}
	go func() { _ = srv.Serve(ctx, l) }()
	return Client{Socket: sock}, ops, sock
}

func TestFirewallInputFamilies(t *testing.T) {
	c, ops, _ := firewallHelper(t)
	for _, f := range []string{"ipv4", "ipv6"} {
		r, err := c.FirewallInput(context.Background(), f)
		if err != nil || !strings.Contains(string(r.Stdout), "-P INPUT DROP") {
			t.Errorf("%s: %v %+v", f, err, r)
		}
	}
	for _, f := range []string{"", "IPv4", "inet", "ipv4 ", "ipv4\n", "-F", "/usr/bin/iptables", "ipv4 -F"} {
		if _, err := c.FirewallInput(context.Background(), f); !errors.Is(err, ErrRefused) {
			t.Errorf("family %q: %v", f, err)
		}
	}
	if !reflect.DeepEqual(ops.families, []string{"ipv4", "ipv6"}) {
		t.Fatalf("a refused read reached the operation: %q", ops.families)
	}
}

// the Client sends only the family; a hand-made request that carries anything else is refused
func TestFirewallInputRefusesExtraFields(t *testing.T) {
	_, ops, sock := firewallHelper(t)
	raw := func(req request) response {
		t.Helper()
		conn, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := json.NewEncoder(conn).Encode(req); err != nil {
			t.Fatal(err)
		}
		var res response
		if err := json.NewDecoder(conn).Decode(&res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := raw(request{Op: opFirewallInput, Name: "ipv4"}); !res.OK {
		t.Fatalf("the plain request: %+v", res)
	}
	extras := map[string]request{
		"arguments":      {Op: opFirewallInput, Name: "ipv4", Args: []string{"-F"}},
		"a path":         {Op: opFirewallInput, Name: "ipv4", Path: "/usr/sbin/iptables"},
		"stdin":          {Op: opFirewallInput, Name: "ipv4", Stdin: []byte("*filter\n")},
		"an environment": {Op: opFirewallInput, Name: "ipv4", Env: []string{"XTABLES_LIBDIR=/tmp"}},
		"a directory":    {Op: opFirewallInput, Name: "ipv4", Dir: "/tmp"},
		"data":           {Op: opFirewallInput, Name: "ipv4", Data: []byte("x")},
		"a uid":          {Op: opFirewallInput, Name: "ipv4", UID: 1000},
		"a version":      {Op: opFirewallInput, Name: "ipv4", Version: "1"},
	}
	for name, req := range extras {
		if res := raw(req); res.OK || !strings.HasPrefix(res.Error, "refused:") {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if len(ops.families) != 1 {
		t.Fatalf("a refused request reached the operation: %q", ops.families)
	}
	// and iptables is not a program the generic run operation would start
	pol := DefaultPolicy("/", "/usr/local/etc/occulite")
	for _, name := range []string{"iptables", "ip6tables", "/usr/bin/iptables", "/usr/bin/ip6tables", "/usr/sbin/iptables"} {
		if pol.programAllowed(name, []string{"-F"}) {
			t.Errorf("%s is on the program list", name)
		}
	}
}

// Local runs exactly FirewallInputArgs with the family's program, and a box without the program
// answers "not available"
func TestLocalFirewallInput(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "iptables")
	script := "#!/bin/sh\necho \"$0 $@\" > " + log + "\necho '-P INPUT ACCEPT'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := FirewallFamilies
	FirewallFamilies = map[string]string{"ipv4": fake, "ipv6": filepath.Join(dir, "ip6tables")}
	t.Cleanup(func() { FirewallFamilies = old })

	r, err := (Local{}).FirewallInput(context.Background(), "ipv4")
	if err != nil || !strings.Contains(string(r.Stdout), "-P INPUT ACCEPT") {
		t.Fatalf("run: %v %+v", err, r)
	}
	argv, _ := os.ReadFile(log)
	if got, want := strings.Fields(string(argv)), append([]string{fake}, FirewallInputArgs()...); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv %q, want %q", got, want)
	}
	if !reflect.DeepEqual(FirewallInputArgs(), []string{"-w", "5", "-S", "INPUT"}) {
		t.Fatalf("the argument list changed: %q", FirewallInputArgs())
	}
	if _, err := (Local{}).FirewallInput(context.Background(), "ipv6"); !errors.Is(err, ErrNotAvailable) {
		t.Errorf("no ip6tables: %v", err)
	}
	if _, err := (Local{}).FirewallInput(context.Background(), "inet"); err == nil || errors.Is(err, ErrNotAvailable) {
		t.Errorf("an unknown family: %v", err)
	}
	if old["ipv4"] != "/usr/bin/iptables" || old["ipv6"] != "/usr/bin/ip6tables" {
		t.Errorf("the programs moved: %q", old)
	}
}

// openccu-lite task 346: a firewall the helper loaded or reverted as asked is logged at info, not
// at warning - "firewall loaded" was the one occulited line on every boot's warning list

type loadFirewall struct{ Local }

func (loadFirewall) FirewallLoad(context.Context, []byte, []byte, time.Duration) error { return nil }
func (loadFirewall) FirewallRevert(context.Context) error                              { return nil }

type lines struct {
	mu  sync.Mutex
	got []string
}

func (l *lines) add(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, fmt.Sprintf(f, a...))
}

func (l *lines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

func TestFirewallLoadIsInfo(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var warn, info lines
	srv := &Server{Policy: DefaultPolicy("/", "/usr/local/etc/occulite"), Ops: loadFirewall{}, Log: warn.add, Info: info.add}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}
	if err := c.FirewallLoad(ctx, []byte(goodText), []byte(goodText), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := c.FirewallRevert(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"helper: firewall loaded (confirm window 60000 ms)", "helper: firewall reverted on request"}
	if got := info.all(); !reflect.DeepEqual(got, want) {
		t.Errorf("info: %q", got)
	}
	if got := warn.all(); len(got) != 0 {
		t.Errorf("warnings: %q", got)
	}
	// without an Info hook the line goes to Log, as before
	var only lines
	(&Server{Log: only.add}).info("x %d", 1)
	if got := only.all(); !reflect.DeepEqual(got, []string{"x 1"}) {
		t.Errorf("fallback: %q", got)
	}
}
