package priv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodText = `*filter
:INPUT DROP [0:0]
:lite-input - [0:0]
-F INPUT
-F lite-input
-A INPUT -i lo -j ACCEPT
-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A INPUT -j lite-input
-A INPUT -m limit --limit 10/min -j LOG --log-prefix "fw policy "
-A lite-input -p tcp -s 10.0.0.0/8 -m tcp --dport 80 -j ACCEPT
-A lite-input -p tcp -m tcp --dport 8080 -m limit --limit 10/min -j LOG --log-prefix "fw 0a1b2c3d "
-A lite-input -p udp -m udp --dport 5353 -j REJECT
-A lite-input -p tcp -m tcp --syn --dport 80 -j REJECT --reject-with tcp-reset
COMMIT
`

// task 157: the helper loads what the rendering writes and nothing that reaches past INPUT and
// lite-input
func TestValidFirewallText(t *testing.T) {
	if err := ValidFirewallText([]byte(goodText)); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"nat table":      strings.Replace(goodText, "*filter", "*nat", 1),
		"another chain":  strings.Replace(goodText, "-A lite-input -p udp", "-A FORWARD -p udp", 1),
		"delete chain":   strings.Replace(goodText, "-F lite-input", "-X lite-input", 1),
		"policy line":    strings.Replace(goodText, "-F INPUT", "-P FORWARD ACCEPT", 1),
		"insert":         strings.Replace(goodText, "-A INPUT -i lo", "-I INPUT -i lo", 1),
		"jump elsewhere": strings.Replace(goodText, "-j REJECT\n", "-j addon-chain\n", 1),
		"another reject": strings.Replace(goodText, "--reject-with tcp-reset", "--reject-with icmp6-adm-prohibited", 1),
		"a quote":        strings.Replace(goodText, "--dport 80", "--dport 80 -m comment --comment \"x\"", 1),
		"no lite-input":  strings.Replace(goodText, ":lite-input - [0:0]\n", "", 1),
		"no commit":      strings.TrimSuffix(goodText, "COMMIT\n"),
		"forward policy": strings.Replace(goodText, ":INPUT DROP [0:0]", ":FORWARD DROP [0:0]", 1),
	} {
		if ValidFirewallText([]byte(bad)) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// fakeTools points FirewallTools at scripts that log their arguments and stdin; failV6 makes
// ip6tables-restore's real load (not the -t test) fail.
func fakeTools(t *testing.T, failV6 bool) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	mk := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	restore := `echo "$0 $*" >> ` + log + `; cat > /dev/null; exit 0`
	restore6 := restore
	if failV6 {
		restore6 = `echo "$0 $*" >> ` + log + `; cat > /dev/null; case "$*" in *-t*) exit 0;; esac; echo boom >&2; exit 2`
	}
	old := FirewallTools
	FirewallTools = map[string][2]string{
		"ipv4": {mk("iptables-restore", restore), mk("iptables-save", `echo "$0 $*" >> `+log+`; echo "SAVED4"`)},
		"ipv6": {mk("ip6tables-restore", restore6), mk("ip6tables-save", `echo "$0 $*" >> `+log+`; echo "SAVED6"`)},
	}
	t.Cleanup(func() { FirewallTools = old; fwState.saved, fwState.timer = nil, nil })
	return log
}

func calls(t *testing.T, log string) []string {
	b, _ := os.ReadFile(log)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			f := strings.Fields(l)
			out = append(out, filepath.Base(f[0])+" "+strings.Join(f[1:], " "))
		}
	}
	return out
}

func TestFirewallLoadConfirmRevert(t *testing.T) {
	log := fakeTools(t, false)
	l := Local{}
	ctx := context.Background()
	// no window: test both, load both, nothing kept
	if err := l.FirewallLoad(ctx, []byte(goodText), []byte(goodText), 0); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"iptables-restore -w 5 --noflush -t", "ip6tables-restore -w 5 --noflush -t",
		"iptables-save -t filter", "ip6tables-save -t filter",
		"iptables-restore -w 5 --noflush", "ip6tables-restore -w 5 --noflush",
	}
	if got := calls(t, log); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls:\n%s", strings.Join(got, "\n"))
	}
	if l.FirewallConfirm(ctx) == nil {
		t.Error("a confirm with nothing open")
	}
	// a window: confirm closes it
	_ = os.Remove(log)
	if err := l.FirewallLoad(ctx, []byte(goodText), []byte(goodText), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := l.FirewallConfirm(ctx); err != nil {
		t.Fatal(err)
	}
	// a window that runs out puts the saved tables back, without --noflush
	_ = os.Remove(log)
	if err := l.FirewallLoad(ctx, []byte(goodText), []byte(goodText), 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	got := calls(t, log)
	if n := len(got); n < 2 || got[n-2] != "iptables-restore -w 5" || got[n-1] != "ip6tables-restore -w 5" {
		t.Fatalf("no revert after the window:\n%s", strings.Join(got, "\n"))
	}
	fwState.mu.Lock()
	open := fwState.saved != nil
	fwState.mu.Unlock()
	if open {
		t.Fatal("the window stays open after the revert")
	}
	// a revert on request
	if err := l.FirewallLoad(ctx, []byte(goodText), []byte(goodText), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := l.FirewallRevert(ctx); err != nil {
		t.Fatal(err)
	}
	// a bad text never reaches iptables
	_ = os.Remove(log)
	if err := l.FirewallLoad(ctx, []byte(strings.Replace(goodText, "*filter", "*nat", 1)), []byte(goodText), 0); err == nil || len(calls(t, log)) != 0 {
		t.Fatalf("bad text: %v %v", err, calls(t, log))
	}
}

// the second family fails: the first is put back
func TestFirewallLoadRollsBack(t *testing.T) {
	log := fakeTools(t, true)
	err := Local{}.FirewallLoad(context.Background(), []byte(goodText), []byte(goodText), 0)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	got := calls(t, log)
	if got[len(got)-1] != "iptables-restore -w 5" {
		t.Fatalf("no rollback of ipv4:\n%s", strings.Join(got, "\n"))
	}
}

func TestSocketOwners(t *testing.T) {
	dir := t.TempDir()
	old := procDir
	procDir = dir
	t.Cleanup(func() { procDir = old })
	mk := func(pid, comm, cgroup string, links map[string]string) {
		base := filepath.Join(dir, pid)
		_ = os.MkdirAll(filepath.Join(base, "fd"), 0o755)
		_ = os.WriteFile(filepath.Join(base, "comm"), []byte(comm+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(base, "cgroup"), []byte(cgroup), 0o644)
		for fd, target := range links {
			_ = os.Symlink(target, filepath.Join(base, "fd", fd))
		}
	}
	mk("412", "lighttpd", "0::/system.slice/lighttpd.service\n", map[string]string{"3": "socket:[1001]", "4": "/dev/null", "5": "socket:[1002]"})
	mk("977", "java", "0::/system.slice/hmipserver.service\n", map[string]string{"7": "socket:[2001]"})
	mk("self", "x", "", nil)
	owners, err := Local{}.SocketOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byInode := map[uint64]SocketOwner{}
	for _, o := range owners {
		byInode[o.Inode] = o
	}
	if len(owners) != 3 || byInode[1002] != (SocketOwner{Inode: 1002, PID: 412, Comm: "lighttpd", Unit: "lighttpd.service"}) || byInode[2001].Unit != "hmipserver.service" {
		t.Fatalf("%+v", owners)
	}
}

// task 167: the counters are both saves with -c, read only
func TestFirewallCounters(t *testing.T) {
	log := fakeTools(t, false)
	got, err := Local{}.FirewallCounters(context.Background())
	if err != nil || string(got["ipv4"]) != "SAVED4\n" || string(got["ipv6"]) != "SAVED6\n" {
		t.Fatalf("%q %v", got, err)
	}
	if c := calls(t, log); strings.Join(c, "|") != "iptables-save -c -t filter|ip6tables-save -c -t filter" {
		t.Fatalf("calls %v", c)
	}
}
