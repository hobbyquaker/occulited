package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// recorder is the Runner the tests substitute for exec. It is locked because NetTx's expiry
// timer calls it from its own goroutine while the test reads what was called - `go test -race`
// reported that as a data race in TestNetTxConfirmAndRevert, and a flaky test is a bug of its
// own even when the production Runner (ExecRunner) holds no state.
type recorder struct {
	mu    sync.Mutex
	calls []string
	// answers by program name, and the programs that fail (task 62's tests)
	answers map[string]string
	fails   map[string]bool
}

func (r *recorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	if r.fails[name] {
		return []byte("failed"), errors.New(name + ": exit 1")
	}
	if a, ok := r.answers[name]; ok {
		return []byte(a), nil
	}
	return nil, nil
}

// Calls returns a copy; every assertion goes through it.
func (r *recorder) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// reset clears the recording.
func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

func rootWith(t *testing.T, files map[string]string) Root {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Root(dir)
}

const netconfigDHCP = "HOSTNAME=openccu\nMODE=DHCP\nCURRENT_IP=192.0.2.119\nCURRENT_NETMASK=255.255.255.0\nCURRENT_GATEWAY=192.0.2.1\nCURRENT_NAMESERVER1=192.0.2.1\nCURRENT_NAMESERVER2=192.0.2.225\nIP=192.168.1.225\nNETMASK=255.255.255.0\nGATEWAY=192.168.1.1\nNAMESERVER1=192.168.1.1\nNAMESERVER2=0.0.0.0\nCRYPT=0\n"

func TestNetworkValidate(t *testing.T) {
	bad := []NetworkSettings{
		{Hostname: "a b", Mode: "dhcp"},
		{Hostname: "x", Mode: "bridge"},
		{Hostname: "x", Mode: "static", Address: "10.0.0.5"},
		{Hostname: "x", Mode: "static", Address: "10.0.0.5", Netmask: "255.255.255.255"},
		{Hostname: "x", Mode: "static", Address: "10.0.0.5", Netmask: "255.255.255.0", Gateway: "10.0.1.1"},
		{Hostname: "x", Mode: "dhcp", DNS: []string{"a", "b"}},
		{Hostname: "x", Mode: "dhcp", DNS: []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}},
	}
	for i, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
	good := NetworkSettings{Hostname: "ccu-lite", Mode: "static", Address: "10.0.0.5", Netmask: "255.255.255.0", Gateway: "10.0.0.1", DNS: []string{"10.0.0.1"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteNetconfigPreservesOtherKeys(t *testing.T) {
	r := rootWith(t, map[string]string{"etc/config/netconfig": netconfigDHCP})
	err := r.WriteNetconfig(NetworkSettings{Hostname: "lite", Mode: "static", Address: "10.0.0.5", Netmask: "255.255.255.0", Gateway: "10.0.0.1", DNS: []string{"10.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(r.join("/etc/config/netconfig"))
	for _, want := range []string{"HOSTNAME=lite\n", "MODE=MANUAL\n", "IP=10.0.0.5\n", "NAMESERVER1=10.0.0.1\n", "NAMESERVER2=0.0.0.0\n", "CRYPT=0\n", "CURRENT_IP=192.0.2.119\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Count(got, "MODE=") != 1 {
		t.Errorf("MODE duplicated:\n%s", got)
	}
	n := r.ReadNetwork()
	if n.Mode != "static" || n.Address != "10.0.0.5" {
		t.Errorf("readback: %+v", n)
	}
	if err := r.WriteNetconfig(NetworkSettings{Hostname: "lite", Mode: "dhcp"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(r.join("/etc/config/netconfig")); !strings.Contains(got, "MODE=DHCP\n") || !strings.Contains(got, "IP=10.0.0.5\n") {
		t.Errorf("dhcp keeps the static values for a later switch back:\n%s", got)
	}
}

func TestSetProperty(t *testing.T) {
	if got := setProperty("", "A", "1"); got != "A=1\n" {
		t.Errorf("%q", got)
	}
	if got := setProperty("A=0\nB=2", "A", "1"); got != "A=1\nB=2" {
		t.Errorf("%q", got)
	}
	if got := setProperty("B=2", "A", "1"); got != "B=2\nA=1\n" {
		t.Errorf("%q", got)
	}
}

func TestNetTxConfirmAndRevert(t *testing.T) {
	r := rootWith(t, map[string]string{"etc/config/netconfig": netconfigDHCP, "var/run/udhcpc_eth0.pid": "1233\n"})
	rec := &recorder{}
	tx := &NetTx{Root: r, Applier: NetApplier{Root: r, Iface: "eth0", Run: rec.run}, Window: 50 * time.Millisecond}
	static := NetworkSettings{Hostname: "openccu", Mode: "static", Address: "192.0.2.200", Netmask: "255.255.255.0", Gateway: "192.0.2.1", DNS: []string{"192.0.2.1"}}
	p, _, err := tx.Begin(context.Background(), static)
	if err != nil || p == nil {
		t.Fatalf("begin: %v %v", err, p)
	}
	if p.Previous.Mode != "dhcp" || p.Previous.Address != "192.0.2.119" {
		t.Errorf("previous should be the live DHCP state: %+v", p.Previous)
	}
	joined := strings.Join(rec.Calls(), "\n")
	for _, want := range []string{"kill 1233", "/sbin/ifconfig eth0 192.0.2.200 netmask 255.255.255.0", "/sbin/ip route add default via 192.0.2.1", "/sbin/resolvconf -a eth0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if _, _, err := tx.Begin(context.Background(), static); err != ErrTxPending {
		t.Errorf("second begin: %v", err)
	}
	if err := tx.Confirm(context.Background(), "wrong"); err != ErrNoTx {
		t.Errorf("wrong token: %v", err)
	}
	if err := tx.Confirm(context.Background(), p.Token); err != nil {
		t.Fatal(err)
	}
	if got := readFile(r.join("/etc/config/netconfig")); !strings.Contains(got, "MODE=MANUAL\n") || !strings.Contains(got, "IP=192.0.2.200\n") {
		t.Errorf("confirm did not persist:\n%s", got)
	}
	if tx.Pending() != nil {
		t.Error("still pending after confirm")
	}

	// back to DHCP, then let the window expire: the static setup must be re-applied
	rec.reset()
	p, _, err = tx.Begin(context.Background(), NetworkSettings{Hostname: "openccu", Mode: "dhcp"})
	if err != nil || p == nil {
		t.Fatalf("begin dhcp: %v", err)
	}
	if !strings.Contains(strings.Join(rec.Calls(), "\n"), "/sbin/udhcpc -b -t 20 -T 3 -S -x hostname:openccu -i eth0 -F openccu") {
		t.Errorf("udhcpc not started:\n%s", strings.Join(rec.Calls(), "\n"))
	}
	time.Sleep(150 * time.Millisecond)
	if tx.Pending() != nil {
		t.Fatal("should have expired")
	}
	if got := strings.Join(rec.Calls(), "\n"); !strings.Contains(got, "/sbin/ifconfig eth0 192.0.2.200 netmask 255.255.255.0") {
		t.Errorf("expiry did not revert to the confirmed static setup:\n%s", got)
	}
	if got := readFile(r.join("/etc/config/netconfig")); !strings.Contains(got, "MODE=MANUAL\n") {
		t.Errorf("expiry must not persist anything:\n%s", got)
	}

	// hostname only: immediate, no window; a static setup has no DHCP server to tell (task 62)
	rec.reset()
	p, ren, err := tx.Begin(context.Background(), NetworkSettings{Hostname: "cupboard", Mode: "static", Address: "192.0.2.200", Netmask: "255.255.255.0", Gateway: "192.0.2.1", DNS: []string{"192.0.2.1"}})
	if err != nil || p != nil || ren == nil || ren.Hostname != "cupboard" || ren.Previous != "openccu" || !ren.Lease.Static || ren.Lease.Renewed {
		t.Fatalf("hostname only: %v %v %+v", err, p, ren)
	}
	calls := strings.Join(rec.Calls(), "\n")
	if !strings.Contains(calls, "hostname cupboard") || readFile(r.join("/etc/hostname")) != "cupboard\n" || strings.Contains(calls, "udhcpc") || strings.Contains(calls, "reload") {
		t.Errorf("hostname not applied, or the lease touched on a static setup: %v", rec.Calls())
	}
	// the same name again: nothing to do
	if p, ren, err := tx.Begin(context.Background(), NetworkSettings{Hostname: "cupboard", Mode: "static", Address: "192.0.2.200", Netmask: "255.255.255.0", Gateway: "192.0.2.1", DNS: []string{"192.0.2.1"}}); err != nil || p != nil || ren != nil {
		t.Errorf("unchanged: %v %v %v", err, p, ren)
	}
}

// TestVendorClass (openccu-lite task 327): a DHCP client the daemon starts sends the vendor class
// of /etc/dhcp-vendor-class, as eQ3StartNetwork and lite-network-reload do - openccu-lite on the
// images - and a CCU3's without the file or with one that is not a single word.
func TestVendorClass(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{"# the class\nDHCP_VENDOR_ID=openccu-lite\n", "openccu-lite"},
		{"DHCP_VENDOR_ID=\"my-ccu.v2\"\n", "my-ccu.v2"},
		{"DHCP_VENDOR_ID=a b\n", "eQ3-CCU3"},
		{"DHCP_VENDOR_ID=-x\n", "eQ3-CCU3"},
		{"", "eQ3-CCU3"},
		{"-", "eQ3-CCU3"}, // no file
	} {
		files := map[string]string{"etc/config/netconfig": netconfigDHCP, "var/run/udhcpc_eth0.pid": "1233\n"}
		if c.file != "-" {
			files["etc/dhcp-vendor-class"] = c.file
		}
		r := rootWith(t, files)
		if got := (NetApplier{Root: r}).vendorClass(); got != c.want {
			t.Errorf("%q: %q, want %q", c.file, got, c.want)
		}
		// both the rename's restart (no systemd) and an address change start the client with it
		rec := &recorder{}
		a := NetApplier{Root: r, Iface: "eth0", Run: rec.run}
		a.RenewLease(context.Background(), "attic")
		if err := a.Apply(context.Background(), NetworkSettings{Hostname: "attic", Mode: "dhcp", DNS: []string{}}); err != nil {
			t.Fatal(err)
		}
		want := "-F attic -V " + c.want + " -s /bin/dhcp.script"
		if n := strings.Count(strings.Join(rec.Calls(), "\n"), want); n != 2 {
			t.Errorf("%q: %d clients with %q in\n%s", c.file, n, want, strings.Join(rec.Calls(), "\n"))
		}
	}
}

// TestRenameDHCP (openccu-lite task 62): a hostname-only change on a DHCP setup asks the server
// for a lease under the new name - through the unit's reload with systemd, by restarting udhcpc
// without - and /etc/hosts keeps the lines that are not the host's own.
func TestRenameDHCP(t *testing.T) {
	hosts := "127.0.0.1 localhost\n127.0.1.1 openccu\n# an addon's entry\n192.0.2.9 nas nas.home.arpa\n::1 ip6-localhost ip6-loopback\n"
	for _, systemd := range []bool{true, false} {
		r := rootWith(t, map[string]string{"etc/config/netconfig": netconfigDHCP, "var/run/udhcpc_eth0.pid": "1233\n", "etc/hosts": hosts})
		rec := &recorder{answers: map[string]string{"/sbin/ip": "2: eth0    inet 192.0.2.119/24 brd 192.0.2.255 scope global dynamic eth0\n"}}
		tx := &NetTx{Root: r, Applier: NetApplier{Root: r, Iface: "eth0", Run: rec.run, Systemd: systemd}, Window: 50 * time.Millisecond}
		p, ren, err := tx.Begin(context.Background(), NetworkSettings{Hostname: "attic", Mode: "dhcp", DNS: []string{}})
		if err != nil || p != nil || ren == nil || !ren.Lease.Renewed || ren.Lease.Static || ren.Lease.Address != "192.0.2.119" {
			t.Fatalf("systemd=%v: %v %v %+v", systemd, err, p, ren)
		}
		calls := strings.Join(rec.Calls(), "\n")
		if systemd {
			if !strings.Contains(calls, "systemctl reload occu-network.service") || strings.Contains(calls, "udhcpc") {
				t.Errorf("systemd: the unit's reload, not a client of our own:\n%s", calls)
			}
		} else if !strings.Contains(calls, "kill 1233") || !strings.Contains(calls, "/sbin/udhcpc -b -t 20 -T 3 -S -x hostname:attic -i eth0 -F attic") {
			t.Errorf("busybox: kill and restart with the new name:\n%s", calls)
		}
		got := readFile(r.join("/etc/hosts"))
		if got != "127.0.0.1 localhost\n127.0.1.1 attic\n# an addon's entry\n192.0.2.9 nas nas.home.arpa\n::1 ip6-localhost ip6-loopback\n" {
			t.Errorf("hosts:\n%s", got)
		}
		if !strings.Contains(readFile(r.join("/etc/config/netconfig")), "HOSTNAME=attic\n") {
			t.Error("netconfig not written")
		}
	}
	// a reload that fails is reported, not fatal: the name is set, the lease stays the old one
	r := rootWith(t, map[string]string{"etc/config/netconfig": netconfigDHCP, "var/run/udhcpc_eth0.pid": "1233\n"})
	rec := &recorder{fails: map[string]bool{"systemctl": true}}
	tx := &NetTx{Root: r, Applier: NetApplier{Root: r, Iface: "eth0", Run: rec.run, Systemd: true}, Window: 50 * time.Millisecond}
	_, ren, err := tx.Begin(context.Background(), NetworkSettings{Hostname: "attic", Mode: "dhcp", DNS: []string{}})
	if err != nil || ren == nil || ren.Lease.Renewed || ren.Lease.Error == "" {
		t.Fatalf("failed reload: %v %+v", err, ren)
	}
}

func TestHostsWithName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "127.0.0.1 localhost\n127.0.1.1 x\n::1 ip6-localhost ip6-loopback\nff02::1 ip6-allnodes\nff02::2 ip6-allrouters\n"},
		{"127.0.0.1 localhost\n127.0.1.1 old\n", "127.0.0.1 localhost\n127.0.1.1 x\n"},
		{"127.0.0.1 localhost\n192.0.2.9 nas\n", "127.0.0.1 localhost\n127.0.1.1 x\n192.0.2.9 nas\n"},
		{"192.0.2.9 nas\n", "127.0.1.1 x\n192.0.2.9 nas\n"},
		{"127.0.1.1 a\n127.0.1.1 b\n10.0.0.1 c\n", "127.0.1.1 x\n10.0.0.1 c\n"},
	}
	for _, c := range cases {
		if got := HostsWithName(c.in, "x"); got != c.want {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}

const firewallConf = "# Firewall Configuration file\n#   created Sat Sep 05 04:22:13 CEST 2026\n\nMODE = MOST_OPEN\n\nIPs = 192.168.0.1 192.168.0.0/16 fc00::/7 fe80::/10\n\n\n\n[SERVICE NEOSERVER]\nId = NEOSERVER\nPorts = 1901 1902 5987 8088 9099 10000 48899 49880\nAccess = full\n\n[SERVICE SNMP]\nId = SNMP\nPorts = 161\nAccess = none\n\n[SERVICE XMLRPC]\nId = XMLRPC\nPorts = 2000 2001 2002 2010 9292 42000 42001 42010 49292\nAccess = full\n\n[SERVICE REGA]\nId = REGA\nPorts = 1999 8181 41999 48181\nAccess = restricted\n\n"

func TestTimeReadZonesAndSet(t *testing.T) {
	r := rootWith(t, map[string]string{
		"etc/config/TZ":                    "CET-1CEST-2,M3.5.0/02:00:00,M10.5.0/03:00:00\n",
		"etc/config/timezone":              "Europe/Berlin\n",
		"etc/config/ntpclient":             "NTPSERVERS='0.de.pool.ntp.org 1.de.pool.ntp.org'\n",
		"etc/config/time.conf":             "COUNTRY=Germany\nCITY=Berlin\nLATITUDE=52.5\nLONGITUDE=13.4\nTIMEZONE=Europe/Berlin\n",
		"var/status/hasNTP":                "",
		"usr/share/zoneinfo/zone.tab":      "# comment\nDE\t+5230+01322\tEurope/Berlin\tmost of Germany\nAT\t+4813+01620\tEurope/Vienna\n",
		"usr/share/zoneinfo/iso3166.tab":   "# codes\nAT\tAustria\nDE\tGermany\n",
		"usr/share/zoneinfo/Europe/Vienna": "TZif",
		"etc/eq3services.ports.tcl":        "set EQ3_SERVICE_RFD_PORT 32001\n",
		"bin/updateTZ.sh":                  "#!/bin/sh\n",
	})
	tc := r.ReadTime()
	if tc.Zone != "Europe/Berlin" || len(tc.NTPServers) != 2 || !tc.HasNTP || !strings.HasPrefix(tc.TZ, "CET-1CEST") {
		t.Fatalf("%+v", tc)
	}
	zones := r.Zones()
	if len(zones) != 2 || zones[0].Name != "Europe/Berlin" || zones[0].Country != "Germany" || zones[0].Comment != "most of Germany" || zones[1].Country != "Austria" {
		t.Fatalf("%+v", zones)
	}
	rec := &recorder{}
	if err := r.SetTimezone(context.Background(), rec.run, "../../etc/passwd"); err == nil {
		t.Error("traversal accepted")
	}
	if err := r.SetTimezone(context.Background(), rec.run, "Europe/Rome"); err == nil {
		t.Error("missing zone accepted")
	}
	if err := r.SetTimezone(context.Background(), rec.run, "Europe/Vienna"); err != nil {
		t.Fatal(err)
	}
	if readFile(r.join("/etc/config/TZ")) != "Europe/Vienna\n" {
		t.Errorf("TZ: %q", readFile(r.join("/etc/config/TZ")))
	}
	if conf := readFile(r.join("/etc/config/time.conf")); !strings.Contains(conf, "TIMEZONE=Europe/Vienna\n") || !strings.Contains(conf, "CITY=Berlin\n") {
		t.Errorf("time.conf: %q", conf)
	}
	joined := strings.Join(rec.Calls(), "\n")
	for _, want := range []string{"/bin/updateTZ.sh", "/sbin/hwclock -wu", "/bin/SetInterfaceClock 127.0.0.1:32001"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	rec.reset()
	if err := r.SetNTP(context.Background(), rec.run, nil, "", []string{"bad host"}); err == nil {
		t.Error("bad host accepted")
	}
	if err := r.SetNTP(context.Background(), rec.run, nil, "", []string{"ptbtime1.ptb.de", "192.168.1.1"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(r.join("/etc/config/ntpclient")); got != "NTPSERVERS='ptbtime1.ptb.de 192.168.1.1'\n" {
		t.Errorf("%q", got)
	}
	if !strings.HasSuffix(rec.Calls()[0], "/etc/init.d/S46chronyd restart") {
		t.Errorf("%v", rec.Calls())
	}
	// with a service manager the restart is its job, not the init script's (task 115)
	rec.reset()
	fake := &fakeServices{}
	if err := r.SetNTP(context.Background(), rec.run, fake, NTPSourceManual, []string{"192.168.1.2"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("the init script was called although a service manager is there: %v", rec.Calls())
	}
	if got := strings.Join(fake.actions, ","); got != "chrony restart" {
		t.Errorf("service manager asked for %q, want \"chrony restart\"", got)
	}
	rec.reset()
	if err := r.SetClock(context.Background(), rec.run, time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if rec.Calls()[0] != "date -u -s 2026-09-06 12:00:00" {
		t.Errorf("%v", rec.Calls())
	}
}

// occulited task 138: the NTP source - noDHCPNTP written with "manual" and removed with "dhcp",
// the template's servers back as the fallback, the source read back, and a system with servers
// of its own from before the source shown as manual without the file being created.
func TestNTPSource(t *testing.T) {
	r := rootWith(t, map[string]string{
		"etc/config_templates/ntpclient": "NTPSERVERS='0.de.pool.ntp.org 1.de.pool.ntp.org 2.de.pool.ntp.org 3.de.pool.ntp.org'\n",
		"etc/config/ntpclient":           "NTPSERVERS='0.de.pool.ntp.org 1.de.pool.ntp.org 2.de.pool.ntp.org 3.de.pool.ntp.org'\n",
		"var/etc/chrony.conf":            "driftfile /var/lib/chrony/drift\nmakestep 1 -1\n# the servers, written by lite-chrony at start\nserver 192.168.1.1 iburst\nserver 0.de.pool.ntp.org iburst\n",
	})
	noDHCP := r.join(NoDHCPNTPFile)
	exists := func() bool { _, err := os.Stat(noDHCP); return err == nil }

	tc := r.ReadTime()
	if tc.NTPSource != NTPSourceDHCP || strings.Join(tc.NTPServersActive, " ") != "192.168.1.1 0.de.pool.ntp.org" {
		t.Fatalf("fresh system: %+v", tc)
	}

	fake := &fakeServices{}
	if err := r.SetNTP(context.Background(), nil, fake, NTPSourceManual, nil); err == nil {
		t.Error("manual without a server accepted")
	}
	if err := r.SetNTP(context.Background(), nil, fake, "lease", nil); err == nil {
		t.Error("an unknown source accepted")
	}
	if err := r.SetNTP(context.Background(), nil, fake, NTPSourceDHCP, []string{"ptbtime1.ptb.de"}); err == nil {
		t.Error("dhcp with servers of its own accepted")
	}
	if exists() || len(fake.actions) != 0 {
		t.Fatalf("a refused request changed something: file %v, %v", exists(), fake.actions)
	}

	if err := r.SetNTP(context.Background(), nil, fake, NTPSourceManual, []string{"ptbtime1.ptb.de"}); err != nil {
		t.Fatal(err)
	}
	if !exists() {
		t.Fatal("manual: no noDHCPNTP")
	}
	if got := readFile(r.join("/etc/config/ntpclient")); got != "NTPSERVERS='ptbtime1.ptb.de'\n" {
		t.Errorf("manual: %q", got)
	}
	if tc := r.ReadTime(); tc.NTPSource != NTPSourceManual {
		t.Errorf("manual read back as %q", tc.NTPSource)
	}
	// manual with the template's servers stays manual: the file says so
	if err := r.SetNTP(context.Background(), nil, fake, NTPSourceManual, []string{"0.de.pool.ntp.org", "1.de.pool.ntp.org", "2.de.pool.ntp.org", "3.de.pool.ntp.org"}); err != nil {
		t.Fatal(err)
	}
	if tc := r.ReadTime(); tc.NTPSource != NTPSourceManual {
		t.Errorf("manual with the pool read back as %q", tc.NTPSource)
	}

	if err := r.SetNTP(context.Background(), nil, fake, NTPSourceDHCP, nil); err != nil {
		t.Fatal(err)
	}
	if exists() {
		t.Fatal("dhcp: noDHCPNTP left")
	}
	if got := readFile(r.join("/etc/config/ntpclient")); got != "NTPSERVERS='0.de.pool.ntp.org 1.de.pool.ntp.org 2.de.pool.ntp.org 3.de.pool.ntp.org'\n" {
		t.Errorf("dhcp: the template's servers not back: %q", got)
	}
	if tc := r.ReadTime(); tc.NTPSource != NTPSourceDHCP {
		t.Errorf("dhcp read back as %q", tc.NTPSource)
	}
	// dhcp twice: the missing file is no error
	if err := r.SetNTP(context.Background(), nil, fake, NTPSourceDHCP, []string{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.actions, ","); got != "chrony restart,chrony restart,chrony restart,chrony restart" {
		t.Errorf("restarts: %q", got)
	}

	// decision 2: servers of its own from before the source read as manual, the file not created
	for _, c := range []struct {
		ntpclient, want string
	}{
		{"NTPSERVERS='192.168.1.1'\n", NTPSourceManual},
		{"NTPSERVERS='3.de.pool.ntp.org 2.de.pool.ntp.org 1.de.pool.ntp.org 0.de.pool.ntp.org'\n", NTPSourceDHCP},
		{"NTPSERVERS='0.de.pool.ntp.org'\n", NTPSourceManual},
		{"NTPSERVERS=''\n", NTPSourceDHCP},
		{"NTPSERVERS='ntp.homematic.com'\n", NTPSourceDHCP},
	} {
		if err := os.WriteFile(r.join("/etc/config/ntpclient"), []byte(c.ntpclient), 0o644); err != nil {
			t.Fatal(err)
		}
		if tc := r.ReadTime(); tc.NTPSource != c.want {
			t.Errorf("%q: %q, want %q", c.ntpclient, tc.NTPSource, c.want)
		}
		if exists() {
			t.Fatalf("%q: reading created noDHCPNTP", c.ntpclient)
		}
	}
}

// ledHelper records the led operation's frames and refuses those for one LED, as the helper does
// for an LED off its list.
type ledHelper struct {
	priv.Ops
	frames []string
	refuse string
}

func (h *ledHelper) WriteLEDs(dir string, frame []priv.LEDWrite) error {
	if err := priv.ValidLEDFrame(frame); err != nil {
		return err
	}
	for _, w := range frame {
		if w.LED == h.refuse {
			return priv.ErrRefused
		}
		h.frames = append(h.frames, dir+" "+w.LED+"="+w.Trigger)
	}
	return nil
}

func (h *ledHelper) SetIPv6Conf(_ context.Context, iface, key string, v int) error {
	h.frames = append(h.frames, fmt.Sprintf("ipv6-conf %s %s=%d", iface, key, v))
	return nil
}

func TestLEDs(t *testing.T) {
	r := rootWith(t, map[string]string{
		"var/hm_mode": "HM_LED_GREEN='/sys/class/leds/rpi_rf_mod:green'\nHM_LED_GREEN_MODE2='default-on'\nHM_LED_RED='/sys/class/leds/rpi_rf_mod:red'\nHM_LED_RED_MODE2='none'\n",
		"sys/class/leds/rpi_rf_mod:green/trigger": "none timer [default-on]\n",
		"sys/class/leds/rpi_rf_mod:red/trigger":   "[none] timer default-on\n",
	})
	l := r.ReadLEDs()
	if l.Disabled || len(l.LEDs) != 2 || l.LEDs[0].Trigger != "default-on" || l.LEDs[0].Normal != "default-on" || l.LEDs[1].Trigger != "none" {
		t.Fatalf("%+v", l)
	}
	h := &ledHelper{Ops: priv.Local{}} // the marker through the real operations, the frames recorded
	old := Priv
	Priv = h
	t.Cleanup(func() { Priv = old })
	class := r.join("/sys/class/leds")
	if err := r.SetLEDsDisabled(true); err != nil {
		t.Fatal(err)
	}
	if !r.ReadLEDs().Disabled {
		t.Error("marker missing")
	}
	if got, want := strings.Join(h.frames, "|"), class+" rpi_rf_mod:green=none|"+class+" rpi_rf_mod:red=none"; got != want {
		t.Errorf("off: %q, want %q", got, want)
	}
	h.frames = nil
	if err := r.SetLEDsDisabled(false); err != nil {
		t.Fatal(err)
	}
	if r.ReadLEDs().Disabled {
		t.Error("marker still there")
	}
	if got, want := strings.Join(h.frames, "|"), class+" rpi_rf_mod:green=default-on|"+class+" rpi_rf_mod:red=none"; got != want {
		t.Errorf("on: %q, want %q", got, want)
	}
}

// occulited B-139: the onboard-LED switch sets each LED's trigger through the helper's led
// operation - one frame per LED, the LED by its name under /sys/class/leds - never a shell line.
func TestSetLEDsDisabledFrames(t *testing.T) {
	class := filepath.Join("sys", "class", "leds")
	cases := []struct {
		name     string
		hmMode   string
		leds     []string // the LEDs whose trigger file exists
		disabled bool
		refuse   string
		want     []string
		err      string
	}{
		{"a Pi, on", "HM_LED_GREEN='/sys/class/leds/ACT'\nHM_LED_GREEN_MODE2='heartbeat'\nHM_LED_RED='/sys/class/leds/PWR'\nHM_LED_RED_MODE2='mmc0'\n",
			[]string{"ACT", "PWR"}, false, "", []string{"ACT=heartbeat", "PWR=mmc0"}, ""},
		{"a Pi, off", "HM_LED_GREEN='/sys/class/leds/ACT'\nHM_LED_GREEN_MODE2='heartbeat'\nHM_LED_RED='/sys/class/leds/PWR'\nHM_LED_RED_MODE2='mmc0'\n",
			[]string{"ACT", "PWR"}, true, "", []string{"ACT=none", "PWR=none"}, ""},
		{"no normal trigger means none", "HM_LED_GREEN='/sys/class/leds/blue:status'\n",
			[]string{"blue:status"}, false, "", []string{"blue:status=none"}, ""},
		{"three LEDs", "HM_LED_GREEN='/sys/class/leds/green:'\nHM_LED_GREEN_MODE2='heartbeat'\nHM_LED_RED='/sys/class/leds/red:'\nHM_LED_RED_MODE2='none'\nHM_LED_YELLOW='/sys/class/leds/yellow:'\nHM_LED_YELLOW_MODE2='default-on'\n",
			[]string{"green:", "red:", "yellow:"}, false, "", []string{"green:=heartbeat", "red:=none", "yellow:=default-on"}, ""},
		{"an LED missing from sysfs is reported, the others still set", "HM_LED_GREEN='/sys/class/leds/ACT'\nHM_LED_GREEN_MODE2='heartbeat'\nHM_LED_RED='/sys/class/leds/PWR'\n",
			[]string{"ACT"}, false, "PWR", []string{"ACT=heartbeat"}, "led red"},
		{"a refused LED is reported, the others still set", "HM_LED_GREEN='/sys/class/leds/ACT'\nHM_LED_GREEN_MODE2='heartbeat'\nHM_LED_RED='/sys/class/leds/PWR'\nHM_LED_RED_MODE2='mmc0'\n",
			[]string{"ACT", "PWR"}, false, "ACT", []string{"PWR=mmc0"}, "led green"},
		{"a trigger off the helper's list is reported", "HM_LED_GREEN='/sys/class/leds/ACT'\nHM_LED_GREEN_MODE2='oneshot'\n",
			[]string{"ACT"}, false, "", nil, "led green"},
		{"an LED outside the class directory is not written", "HM_LED_GREEN='/sys/devices/platform/leds/ACT'\nHM_LED_GREEN_MODE2='heartbeat'\n",
			nil, false, "", nil, "led green"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"var/hm_mode": c.hmMode}
			for _, n := range c.leds {
				files[filepath.Join(class, n, "trigger")] = "[none] heartbeat\n"
			}
			if strings.Contains(c.hmMode, "/sys/devices/") {
				files["sys/devices/platform/leds/ACT/trigger"] = "[none]\n"
			}
			r := rootWith(t, files)
			h := &ledHelper{Ops: priv.Local{}, refuse: c.refuse}
			old := Priv
			Priv = h
			t.Cleanup(func() { Priv = old })
			err := r.SetLEDsDisabled(c.disabled)
			if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
				t.Fatalf("error %v, want %q", err, c.err)
			}
			var want []string
			for _, w := range c.want {
				want = append(want, r.join("/sys/class/leds")+" "+w)
			}
			if strings.Join(h.frames, "\n") != strings.Join(want, "\n") {
				t.Errorf("frames:\n got  %q\n want %q", h.frames, want)
			}
		})
	}
}

// occulited B-139: without a Conf of its own, an IPv6Applier writes the sysctls through the
// helper's ipv6-conf operation, never a shell line through its Runner.
func TestIPv6ConfThroughHelper(t *testing.T) {
	h := &ledHelper{}
	old := Priv
	Priv = h
	t.Cleanup(func() { Priv = old })
	l := &cmdLog{}
	a := IPv6Applier{Root: Root(t.TempDir()), Run: l.run}
	if err := a.Apply(context.Background(), "eth0", IPv6Settings{Mode: "slaac"}, IPv6Settings{Mode: "off"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.frames, "|"); got != "ipv6-conf eth0 disable_ipv6=1" {
		t.Errorf("helper calls %q", got)
	}
	for _, c := range l.take() {
		if strings.HasPrefix(c, "sh ") {
			t.Errorf("a shell line: %q", c)
		}
	}
}

func TestSSH(t *testing.T) {
	r := rootWith(t, map[string]string{"etc/config/shadow": "root:*:19000:0:99999:7:::\nhm:*:19000:0:99999:7:::\n", "etc/init.d/S50sshd": ""})
	told := captureFirewall(t)
	if s := r.ReadSSH(nil); s.Enabled || s.Running {
		t.Fatalf("%+v", s)
	}
	rec := &recorder{}
	if err := r.SetSSH(context.Background(), rec.run, nil, true); err != nil {
		t.Fatal(err)
	}
	// task 157: the firewall follows through FirewallChanged, no script
	if !r.ReadSSH(nil).Enabled || *told != 1 || len(rec.Calls()) != 1 || !strings.HasSuffix(rec.Calls()[0], "/etc/init.d/S50sshd restart") {
		t.Errorf("%v %+v", rec.Calls(), r.ReadSSH(nil))
	}
	rec.reset()
	if err := r.SetSSH(context.Background(), rec.run, nil, false); err != nil {
		t.Fatal(err)
	}
	if r.ReadSSH(nil).Enabled || *told != 2 || !strings.HasSuffix(rec.Calls()[0], "/etc/init.d/S50sshd stop") {
		t.Errorf("%v", rec.Calls())
	}
	// with a service manager the daemon state comes from it, and start/stop go through it
	// instead of the init script (B-10)
	fake := &fakeServices{list: []Service{{ID: "sshd", Running: true}}}
	if s := r.ReadSSH(fake); s.Enabled || !s.Running {
		t.Errorf("service manager state ignored: %+v", s)
	}
	rec.reset()
	if err := r.SetSSH(context.Background(), rec.run, fake, true); err != nil {
		t.Fatal(err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("init script called behind the service manager: %v", rec.Calls())
	}
	if fake.actions[0] != "sshd restart" {
		t.Errorf("%v", fake.actions)
	}
	if err := r.SetSSH(context.Background(), rec.run, fake, false); err != nil {
		t.Fatal(err)
	}
	if fake.actions[1] != "sshd stop" {
		t.Errorf("%v", fake.actions)
	}

	// the hasher is handed the password itself (openccu-lite task 302: through the Runner it
	// never was), and a short one is refused before any hashing
	var hashed []string
	hashing := func(_ context.Context, password string) (string, error) {
		hashed = append(hashed, password)
		return "$6$salt$hash\n", nil
	}
	if err := r.SetRootPassword(context.Background(), hashing, "short"); err == nil || len(hashed) != 0 {
		t.Errorf("short accepted: %v %v", err, hashed)
	}
	if err := r.SetRootPassword(context.Background(), hashing, "longenough1"); err != nil {
		t.Fatal(err)
	}
	if len(hashed) != 1 || hashed[0] != "longenough1" {
		t.Errorf("the hasher was handed %q, not the password", hashed)
	}
	if got := readFile(r.join("/etc/config/shadow")); !strings.HasPrefix(got, "root:$6$salt$hash:19000:") || !strings.Contains(got, "\nhm:*:") {
		t.Errorf("%q", got)
	}
	// B-15: the line is replaced by the privilege helper's own operation, which will not write
	// anything that is not a crypt hash - occulited never reads or rewrites the file itself
	notAHash := func(_ context.Context, _ string) (string, error) {
		return "plaintext\n", nil
	}
	if err := r.SetRootPassword(context.Background(), notAHash, "longenough1"); err == nil {
		t.Error("a value that is not a crypt hash was written into shadow")
	}
	if got := readFile(r.join("/etc/config/shadow")); !strings.HasPrefix(got, "root:$6$salt$hash:19000:") {
		t.Errorf("the refused write changed the file: %q", got)
	}
}

func TestRFDInterfaces(t *testing.T) {
	r := rootWith(t, map[string]string{"etc/config/rfd.conf": "Listen Port = 32001\n\n[Interface 0]\nType = CCU2\nComPortFile = /dev/mmd_bidcos\n\n[Interface 1]\nType = Lan Interface\nSerial Number = KEQ0123456\nAddress = 192.168.1.50\nEncryption Key = secret\n"})
	l := r.ReadRFDInterfaces()
	if len(l) != 2 || l[0].Type != "CCU2" || l[1].Type != "Lan Interface" || l[1].Serial != "KEQ0123456" || l[1].Address != "192.168.1.50" || l[1].Index != 1 {
		t.Fatalf("%+v", l)
	}
	if set, known := r.SecurityKeySet(context.Background()); set || known {
		t.Error("fake root must not run crypttool")
	}
}

// fakeServices is a ServiceManager that records what it was asked to do.
type fakeServices struct {
	list    []Service
	actions []string
}

func (f *fakeServices) List() ([]Service, error) { return f.list, nil }

func (f *fakeServices) Control(_ context.Context, id, action string) (string, error) {
	f.actions = append(f.actions, id+" "+action)
	return "", nil
}

// B-43: D-29's default was never implemented - occulited reported MOST_OPEN where libfirewall.tcl
// itself falls back to RESTRICTIVE, so a freshly flashed box enforced one mode and displayed the
// other, and the first save on the Network page made the display true.
func TestFirewallDefaultIsRestrictive(t *testing.T) {
	r := rootWith(t, map[string]string{"bin/setfirewall.tcl": "#!/bin/sh\n"})
	if got := r.ReadFirewall().Mode; got != "RESTRICTIVE" {
		t.Errorf("no firewall.conf: mode = %q, want RESTRICTIVE (D-29, and libfirewall.tcl's own default)", got)
	}
	// a file with every other key but no MODE line is the same case
	r2 := rootWith(t, map[string]string{
		"etc/config/firewall.conf": "# no MODE here\n\nIPs = 10.0.0.0/8\n\n",
		"bin/setfirewall.tcl":      "#!/bin/sh\n",
	})
	if got := r2.ReadFirewall().Mode; got != "RESTRICTIVE" {
		t.Errorf("no MODE line: mode = %q, want RESTRICTIVE", got)
	}
}

// Every section is read, the dead ones too (task 157): the conversion leaves them out and says so
// when they were open.
func TestFirewallReadsEveryService(t *testing.T) {
	r := rootWith(t, map[string]string{"etc/config/firewall.conf": firewallConf, "bin/setfirewall.tcl": "#!/bin/sh\n"})
	fw := r.ReadFirewall()
	if len(fw.Services) != 4 {
		t.Fatalf("want NEOSERVER, SNMP, XMLRPC and REGA, got %d: %+v", len(fw.Services), fw.Services)
	}
	byID := map[string][]int{}
	for _, svc := range fw.Services {
		byID[svc.ID] = svc.Ports
	}
	if len(byID["XMLRPC"]) != 9 || len(byID["NEOSERVER"]) != 8 {
		t.Errorf("ports: %v", byID)
	}
	if len(byID["SNMP"]) != 1 || byID["SNMP"][0] != 161 {
		t.Errorf("SNMP: %v", byID["SNMP"])
	}
}

// openccu-lite task 299: a time set by hand is accepted only between the image's build and 15 years
// after it; without a /VERSION the old bound (2020-2100) stands; the state file says manual.
func TestSetClockWindow(t *testing.T) {
	r := rootWith(t, map[string]string{"VERSION": "VERSION=3.89.11.20260919\n", "run/occulite/clock-state": "timeout\n"})
	built := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(r.join("/VERSION"), built, built); err != nil {
		t.Fatal(err)
	}
	if lo, hi := r.ClockWindow(); !lo.Equal(built) || !hi.Equal(built.AddDate(15, 0, 0)) {
		t.Fatalf("window %s - %s", lo, hi)
	}
	rec := &recorder{}
	for _, tc := range []struct {
		at time.Time
		ok bool
	}{
		{built.Add(time.Hour), true},
		{built.AddDate(14, 11, 0), true},
		{built.Add(-time.Hour), false},
		{built.AddDate(15, 0, 1), false},
		{time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), false},
	} {
		rec.reset()
		err := r.SetClock(context.Background(), rec.run, tc.at)
		if (err == nil) != tc.ok {
			t.Errorf("SetClock(%s): %v, want ok=%v", tc.at, err, tc.ok)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), "implausible") || len(rec.Calls()) != 0) {
			t.Errorf("SetClock(%s): %v %v", tc.at, err, rec.Calls())
		}
	}
	if got := strings.TrimSpace(readFile(r.join("/run/occulite/clock-state"))); got != "manual" {
		t.Fatalf("clock state %q after a manual set", got)
	}
	// without /VERSION: the old bound
	r2 := rootWith(t, nil)
	if lo, hi := r2.ClockWindow(); lo.Year() != 2020 || hi.Year() != 2100 {
		t.Fatalf("fallback window %s - %s", lo, hi)
	}
	if err := r2.SetClock(context.Background(), rec.run, time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := r2.SetClock(context.Background(), rec.run, time.Date(2101, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("2101 accepted")
	}
}
