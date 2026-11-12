package firewall

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func sample() Config {
	return Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}, Active: []string{}, Rules: []Rule{
		{ID: "web80", Port: 80, Proto: "tcp", Source: LocalNetworks, Family: FamilyBoth, Target: Accept, Owner: OwnerWeb},
		{ID: "v4only", Port: 8080, Proto: "tcp", Source: "192.168.1.0/24", Family: FamilyV4, Target: Accept, Log: true},
		{ID: "v6drop", Port: 5353, Proto: "udp", Source: "fd00::/8", Family: FamilyV6, Target: Drop},
		{ID: "any", Port: 1883, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Reject},
	}}
}

var sampleLocal = Local{V4: []string{"10.0.0.0/8", "192.168.0.0/16"}, V6: []string{"fe80::/10"}}

// task 157: the rendering, family by family - the frame, the jump, the rules in order, a source
// that is another family's left out, the local networks expanded, the log rule in front
func TestRender(t *testing.T) {
	v4, err := Render(sample(), FamilyV4, sampleLocal)
	if err != nil {
		t.Fatal(err)
	}
	want4 := `*filter
:INPUT DROP [0:0]
:lite-input - [0:0]
-F INPUT
-F lite-input
-A INPUT -i lo -j ACCEPT
-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A INPUT -p icmp -m icmp --icmp-type 8 -j ACCEPT
-A INPUT -p icmp -m icmp --icmp-type 3 -j ACCEPT
-A INPUT -p icmp -m icmp --icmp-type 11 -j ACCEPT
-A INPUT -p udp -m udp --sport 67 --dport 68 -j ACCEPT
-A INPUT -j lite-input
-A lite-input -p tcp -s 10.0.0.0/8 -m tcp --dport 80 -j ACCEPT
-A lite-input -p tcp -s 192.168.0.0/16 -m tcp --dport 80 -j ACCEPT
-A lite-input -p tcp -s 192.168.1.0/24 -m tcp --dport 8080 -m limit --limit 10/min -j LOG --log-prefix "fw v4only "
-A lite-input -p tcp -s 192.168.1.0/24 -m tcp --dport 8080 -j ACCEPT
-A lite-input -p tcp -m tcp --dport 1883 -j REJECT
COMMIT
`
	if v4 != want4 {
		t.Errorf("ipv4:\n%s\nwant:\n%s", v4, want4)
	}
	c := sample()
	c.Policy = Policy{V4: Drop, V6: Accept, Log: true}
	v6, err := Render(c, FamilyV6, sampleLocal)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		":INPUT ACCEPT [0:0]",
		"-A INPUT -p ipv6-icmp -m icmp6 --icmpv6-type 135 -j ACCEPT",
		"-A INPUT -p udp -m udp --sport 547 --dport 546 -j ACCEPT",
		"-A INPUT -j lite-input\n-A INPUT -m limit --limit 10/min -j LOG --log-prefix \"fw policy \"\n",
		"-A lite-input -p tcp -s fe80::/10 -m tcp --dport 80 -j ACCEPT",
		"-A lite-input -p udp -s fd00::/8 -m udp --dport 5353 -j DROP",
		"-A lite-input -p tcp -m tcp --dport 1883 -j REJECT",
	} {
		if !strings.Contains(v6, want) {
			t.Errorf("ipv6 lacks %q:\n%s", want, v6)
		}
	}
	if strings.Contains(v6, "8080") || strings.Contains(v6, "192.168") {
		t.Errorf("an IPv4 rule in ipv6:\n%s", v6)
	}
	// a bad configuration renders nothing
	c.Rules[0].Target = "MAYBE"
	if _, err := Render(c, FamilyV4, sampleLocal); err == nil {
		t.Error("rendered a bad rule")
	}
}

// the discovery rules: a destination, a source port, igmp without ports, every port - each in
// the families it names
func TestRenderDiscovery(t *testing.T) {
	c := Reconcile(Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}}, map[string][]PortSpec{OwnerDiscovery: DiscoveryPorts})
	v4, err := Render(c, FamilyV4, sampleLocal)
	if err != nil {
		t.Fatal(err)
	}
	v6, err := Render(c, FamilyV6, sampleLocal)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"-A lite-input -p udp -d 224.0.0.0/24 -j ACCEPT\n",
		"-A lite-input -p igmp -d 224.0.0.0/24 -j ACCEPT\n",
		"-A lite-input -p udp -m udp --dport 1900 -j ACCEPT\n",
		"-A lite-input -p udp -m udp --sport 43439 -j ACCEPT\n",
		"-A lite-input -p udp -m udp --sport 23272 -j ACCEPT\n",
		"-A lite-input -p udp -m udp --dport 23272 -j ACCEPT\n",
		// B-221: the eQ-3 discovery from anywhere, on every system; B-219: mDNS from the local networks
		"-A lite-input -p udp -m udp --dport 43439 -j ACCEPT\n",
		"-A lite-input -p udp -s 192.168.0.0/16 -m udp --sport 5353 -j ACCEPT\n",
		"-A lite-input -p udp -s 10.0.0.0/8 -m udp --dport 5353 -j ACCEPT\n",
	} {
		if !strings.Contains(v4, want) {
			t.Errorf("ipv4 lacks %q:\n%s", want, v4)
		}
	}
	if !strings.Contains(v6, "-A lite-input -p udp -s fe80::/10 -m udp --sport 5353 -j ACCEPT\n") || !strings.Contains(v6, "-A lite-input -p udp -s fe80::/10 -m udp --dport 5353 -j ACCEPT\n") || strings.Contains(v6, "--sport 5353 -j") && strings.Contains(v6, "-A lite-input -p udp -m udp --sport 5353") {
		t.Errorf("ipv6 mDNS from the local networks only:\n%s", v6)
	}
	if strings.Contains(v4, "ff02") || !strings.Contains(v6, "-A lite-input -p udp -d ff02::/16 -j ACCEPT\n") || strings.Contains(v6, "224.0.0.0") || strings.Contains(v6, "igmp") {
		t.Errorf("families:\n%s\n%s", v4, v6)
	}
	// the keys round-trip, so a later reconcile keeps them
	back := c.ActiveOwners()[OwnerDiscovery]
	if len(back) != len(DiscoveryPorts) {
		t.Fatalf("active: %v -> %+v", c.Active, back)
	}
	for _, p := range DiscoveryPorts {
		p.Comment, p.Local = "", false
		if !slices.Contains(back, p) {
			t.Errorf("%+v lost in %v", p, c.Active)
		}
	}
	if again := Reconcile(c, map[string][]PortSpec{OwnerDiscovery: DiscoveryPorts}); len(again.Rules) != len(c.Rules) {
		t.Errorf("added twice: %d", len(again.Rules))
	}
}

func TestValidate(t *testing.T) {
	ok := Rule{ID: "a", Port: 22, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Rule){
		"port":       func(r *Rule) { r.Port = 70000 },
		"sport":      func(r *Rule) { r.SPort = -1 },
		"igmp port":  func(r *Rule) { r.Proto = ProtoIGMP },
		"dest":       func(r *Rule) { r.Dest = LocalNetworks },
		"dest v6":    func(r *Rule) { r.Dest = "ff02::/16" },
		"src ≠ dest": func(r *Rule) { r.Source, r.Dest, r.Family = "10.0.0.1", "ff02::1", FamilyV4 },
		"proto":      func(r *Rule) { r.Proto = "sctp" },
		"source":     func(r *Rule) { r.Source = "the internet" },
		"family":     func(r *Rule) { r.Family = "ipx" },
		"v4 on both": func(r *Rule) { r.Source = "10.0.0.1" },
		"v6 on v4":   func(r *Rule) { r.Source, r.Family = "fe80::1", FamilyV4 },
		"target":     func(r *Rule) { r.Target = "LOG" },
		"comment":    func(r *Rule) { r.Comment = "a \"quote\"" },
		"id":         func(r *Rule) { r.ID = "" },
	} {
		r := ok
		mut(&r)
		if r.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c := Config{Policy: Policy{V4: Drop, V6: "MAYBE"}}
	if c.Validate() == nil {
		t.Error("a bad policy")
	}
	c = Config{Policy: Policy{V4: Drop, V6: Drop}, Rules: []Rule{ok, ok}}
	if c.Validate() == nil {
		t.Error("an id twice")
	}
}

// the owned rules: added once when the owner switches on, gone with every duplicate and edit when it
// switches off, a deleted one stays deleted while the owner stays on
func TestReconcile(t *testing.T) {
	d := Default()
	if len(d.Rules) != 2+len(DiscoveryPorts) || d.Rules[0].Port != 80 || d.Rules[0].Source != LocalNetworks || d.Rules[1].Port != 443 || d.Rules[2].Owner != OwnerDiscovery || d.Policy.V4 != Drop {
		t.Fatalf("default: %+v", d)
	}
	c := Reconcile(Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}}, map[string][]PortSpec{OwnerWeb: WebPorts})
	on := map[string][]PortSpec{OwnerWeb: WebPorts, OwnerSSH: SSHPorts}
	c = Reconcile(c, on)
	if len(c.Rules) != 3 || c.Rules[2].Owner != OwnerSSH || c.Rules[2].Source != LocalNetworks {
		t.Fatalf("ssh on: %+v", c.Rules)
	}
	// the user edits the SSH rule, duplicates it, and deletes the web server's 443
	c.Rules[2].Source = "192.168.1.5"
	c.Rules[2].Family = FamilyV4
	dup := c.Rules[2]
	dup.ID, dup.Source = NewID(), "192.168.1.6"
	c.Rules = append(c.Rules, dup)
	c.Rules = slices.DeleteFunc(c.Rules, func(r Rule) bool { return r.Port == 443 })
	again := Reconcile(c, on)
	if len(again.Rules) != len(c.Rules) {
		t.Fatalf("a reconcile with nothing switched re-added: %+v", again.Rules)
	}
	// SSH off: both of its rules go; the hand-made rule and the web server's stay
	c.Rules = append(c.Rules, Rule{ID: "mine", Port: 1883, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept})
	off := Reconcile(c, map[string][]PortSpec{OwnerWeb: WebPorts})
	for _, r := range off.Rules {
		if r.Owner == OwnerSSH {
			t.Fatalf("ssh rule left: %+v", off.Rules)
		}
	}
	if len(off.Rules) != 2 || !slices.ContainsFunc(off.Rules, func(r Rule) bool { return r.ID == "mine" }) {
		t.Fatalf("off: %+v", off.Rules)
	}
	// on again: the defaults again
	back := Reconcile(off, on)
	if n := len(back.Rules); n != 3 || back.Rules[n-1].Source != LocalNetworks {
		t.Fatalf("on again: %+v", back.Rules)
	}
	// an addon's port: one rule per opened port, gone with the port
	a := OwnerAddonPrefix + "mosquitto"
	withAddon := Reconcile(back, map[string][]PortSpec{OwnerWeb: WebPorts, OwnerSSH: SSHPorts, a: {{Port: 8883, Proto: "tcp"}, {Port: 1883, Proto: "tcp"}}})
	if n := len(withAddon.Rules); n != 5 || withAddon.Rules[n-1].Owner != a || withAddon.Rules[n-1].Source != AnySource {
		t.Fatalf("addon: %+v", withAddon.Rules)
	}
	oneLess := Reconcile(withAddon, map[string][]PortSpec{OwnerWeb: WebPorts, OwnerSSH: SSHPorts, a: {{Port: 8883, Proto: "tcp"}}})
	if slices.ContainsFunc(oneLess.Rules, func(r Rule) bool { return r.Owner == a && r.Port == 1883 }) || !slices.ContainsFunc(oneLess.Rules, func(r Rule) bool { return r.Owner == a && r.Port == 8883 }) {
		t.Fatalf("one port closed: %+v", oneLess.Rules)
	}
	if got := oneLess.ActiveOwners()[a]; len(got) != 1 || got[0] != (PortSpec{Port: 8883, Proto: "tcp"}) {
		t.Fatalf("active: %v", oneLess.Active)
	}
}

// .138's firewall.conf as libfirewall left it: RESTRICTIVE, XMLRPC full, SNMP none, one allowed
// network list, a user port and a broken one
func TestMigrate(t *testing.T) {
	old := Old{
		Mode:      "RESTRICTIVE",
		IPs:       []string{"192.168.0.0/16", "fe80::/10"},
		UserPorts: []string{"8181", "5353/udp", "1883"},
		Services: []OldService{
			{ID: "SNMP", Ports: []int{161}, Access: "none"},
			{ID: "XMLRPC", Ports: []int{2001, 2010}, Access: "full"},
			{ID: "OTHER", Ports: []int{8088}, Access: "restricted"},
			{ID: "NEOSERVER", Ports: []int{1901, 9099}, Access: "full"},
			{ID: "REGA", Ports: []int{1999}, Access: "none"},
		},
	}
	c := Migrate(MigrateInputs{Old: old, Owners: map[string][]PortSpec{OwnerSSH: SSHPorts, OwnerAddonPrefix + "mosquitto": {{Port: 1883, Proto: "tcp"}}}}, time.Unix(0, 0))
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	has := func(port int, proto, src, fam, owner string) bool {
		return slices.ContainsFunc(c.Rules, func(r Rule) bool {
			return r.Port == port && r.Proto == proto && r.Source == src && r.Family == fam && r.Owner == owner
		})
	}
	for _, x := range []struct {
		port            int
		proto, src, fam string
		owner           string
	}{
		{80, "tcp", LocalNetworks, FamilyBoth, OwnerWeb},
		{443, "tcp", LocalNetworks, FamilyBoth, OwnerWeb},
		{22, "tcp", LocalNetworks, FamilyBoth, OwnerSSH},
		{1883, "tcp", LocalNetworks, FamilyBoth, OwnerAddonPrefix + "mosquitto"},
		{2001, "tcp", LocalNetworks, FamilyBoth, ""},
		{2010, "tcp", LocalNetworks, FamilyBoth, ""},
		{8088, "tcp", "192.168.0.0/16", FamilyV4, ""},
		{8088, "tcp", "fe80::/10", FamilyV6, ""},
		{8181, "tcp", LocalNetworks, FamilyBoth, ""},
		{8181, "udp", LocalNetworks, FamilyBoth, ""},
	} {
		if !has(x.port, x.proto, x.src, x.fam, x.owner) {
			t.Errorf("missing %+v in %+v", x, c.Rules)
		}
	}
	if has(161, "tcp", AnySource, FamilyBoth, "") || has(1883, "tcp", AnySource, FamilyBoth, "") || has(1883, "udp", AnySource, FamilyBoth, "") {
		t.Errorf("SNMP (none) or the addon's port as a user port: %+v", c.Rules)
	}
	if has(1901, "tcp", AnySource, FamilyBoth, "") || has(9099, "tcp", AnySource, FamilyBoth, "") {
		t.Errorf("a rule for the NEO server, which the system does not run: %+v", c.Rules)
	}
	if c.Policy != (Policy{V4: Drop, V6: Drop}) || c.Migration == nil || len(c.Migration.Notes) < 5 {
		t.Fatalf("policy %+v, notice %+v", c.Policy, c.Migration)
	}
	// the notes filled in, as the page shows them in English
	var filled []string
	for _, n := range c.Migration.Notes {
		s := n.Text
		for k, v := range n.Args {
			s = strings.ReplaceAll(s, "{"+k+"}", v)
		}
		filled = append(filled, s)
	}
	joined := strings.Join(filled, "\n")
	for _, want := range []string{"Mode RESTRICTIVE", "XMLRPC (full access): 2 rule(s) from the local networks", "\"5353/udp\" was not a port number", "User ports 8181: a rule each for tcp and udp from the local networks", "opened ports (1883/tcp) stay limited", "Automatic rules: addon:mosquitto discovery ssh web.", "NEOSERVER (full access): not converted", "ports 1901 9099 stay closed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notice lacks %q:\n%s", want, joined)
		}
	}
	// MOST_OPEN and AllowExternalAccess
	open := Migrate(MigrateInputs{Old: Old{Mode: "MOST_OPEN", AllowExternal: true}}, time.Unix(0, 0))
	if open.Policy.V4 != Accept || open.Policy.V6 != Accept || open.Rules[0].Source != AnySource {
		t.Fatalf("most open: %+v", open)
	}
	// AllowExternalAccess opened the local-only chain to everyone, the user ports with it
	ext := Migrate(MigrateInputs{Old: Old{Mode: "RESTRICTIVE", AllowExternal: true, UserPorts: []string{"9000"}}}, time.Unix(0, 0))
	if !slices.ContainsFunc(ext.Rules, func(r Rule) bool { return r.Port == 9000 && r.Source == AnySource }) {
		t.Fatalf("external user port: %+v", ext.Rules)
	}
}

func TestLocalFor(t *testing.T) {
	_, g4, _ := net.ParseCIDR("203.0.113.7/24")
	_, g6, _ := net.ParseCIDR("2001:db8:1:2::5/64")
	g4.IP, g6.IP = net.ParseIP("203.0.113.7"), net.ParseIP("2001:db8:1:2::5")
	_, p4, _ := net.ParseCIDR("192.168.1.10/24")
	l := LocalFor([]*net.IPNet{g4, g6, p4, g4})
	if !slices.Contains(l.V4, "203.0.113.0/24") || !slices.Contains(l.V6, "2001:db8:1:2::/64") || slices.Contains(l.V4, "192.168.1.0/24") {
		t.Fatalf("%+v", l)
	}
	if n := len(l.V4); n != len(localStaticV4)+1 {
		t.Fatalf("twice: %v", l.V4)
	}
}

// task 143: the conversion switched classic RPC on - a restricted XMLRPC's rules become classic
// RPC's, one per allowed address; a port the service did not list keeps the owner's default
func TestMigrateClassicRPC(t *testing.T) {
	old := Old{Mode: "RESTRICTIVE", IPs: []string{"192.168.0.0/16", "fe80::/10"}, Services: []OldService{
		{ID: "XMLRPC", Ports: []int{2001, 42001}, Access: "restricted"},
	}}
	rpc := []PortSpec{{Port: 2001, Proto: "tcp"}, {Port: 42001, Proto: "tcp"}, {Port: 2010, Proto: "tcp", Comment: "XMLRPC hmipserver (HmIP-RF)"}}
	c := Migrate(MigrateInputs{Old: old, Owners: map[string][]PortSpec{OwnerRPC: rpc}}, time.Unix(0, 0))
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range c.Rules {
		if r.Owner == OwnerRPC {
			got = append(got, fmt.Sprintf("%d %s %s", r.Port, r.Source, r.Family))
		}
	}
	want := []string{"2010 local networks both", "2001 192.168.0.0/16 ipv4", "2001 fe80::/10 ipv6", "42001 192.168.0.0/16 ipv4", "42001 fe80::/10 ipv6"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rpc rules:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if slices.ContainsFunc(c.Rules, func(r Rule) bool { return r.Owner == "" && (r.Port == 2001 || r.Port == 42001) }) {
		t.Error("an XMLRPC rule stayed hand-made")
	}
	if !slices.ContainsFunc(c.Migration.Notes, func(n Note) bool { return strings.HasPrefix(n.Text, "Classic RPC is switched on") }) {
		t.Errorf("notes: %+v", c.Migration.Notes)
	}
	// a later reconcile with the same owner adds nothing back
	all := AlwaysOwners()
	all[OwnerRPC] = rpc
	if again := Reconcile(c, all); len(again.Rules) != len(c.Rules) {
		t.Errorf("reconcile added %d", len(again.Rules)-len(c.Rules))
	}
}

// task 172: a network another entry contains is not listed twice - a privacy /128 beside its /64,
// a global /24 inside nothing, a private one inside the static range
func TestLocalForContained(t *testing.T) {
	mk := func(cidr, ip string) *net.IPNet {
		_, n, _ := net.ParseCIDR(cidr)
		n.IP = net.ParseIP(ip)
		return n
	}
	l := LocalFor([]*net.IPNet{
		mk("2003:ce:871c:8bf1::/64", "2003:ce:871c:8bf1:dea6:32ff:fe03:a9fa"),
		mk("2003:ce:871c:8bf1:794f:68fa:21e5:29bf/128", "2003:ce:871c:8bf1:794f:68fa:21e5:29bf"),
		mk("203.0.113.0/24", "203.0.113.7"),
	})
	if !slices.Contains(l.V6, "2003:ce:871c:8bf1::/64") || slices.ContainsFunc(l.V6, func(s string) bool { return strings.HasSuffix(s, "/128") && s != "::1/128" }) {
		t.Fatalf("v6: %v", l.V6)
	}
	if !slices.Contains(l.V4, "203.0.113.0/24") || len(l.V4) != 7 {
		t.Fatalf("v4: %v", l.V4)
	}
	if got := withoutContained([]string{"10.0.0.0/8", "10.1.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}); strings.Join(got, " ") != "10.0.0.0/8 172.16.0.0/12" {
		t.Fatalf("contained: %v", got)
	}
}

// task 171: a port range - rendered a:b, validated, covering the ports inside
func TestPortRange(t *testing.T) {
	r := Rule{ID: "rg", Port: 1880, PortTo: 1890, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	c := Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}, Rules: []Rule{r}}
	v4, err := Render(c, FamilyV4, sampleLocal)
	if err != nil || !strings.Contains(v4, "-A lite-input -p tcp -m tcp --dport 1880:1890 -j ACCEPT\n") {
		t.Fatalf("%v\n%s", err, v4)
	}
	if !r.HasPort(1880) || !r.HasPort(1885) || !r.HasPort(1890) || r.HasPort(1891) || r.HasPort(1879) {
		t.Error("HasPort")
	}
	for name, bad := range map[string]Rule{
		"reversed": {ID: "a", Port: 1890, PortTo: 1880, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept},
		"equal":    {ID: "a", Port: 1880, PortTo: 1880, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept},
		"no start": {ID: "a", Port: 0, PortTo: 1880, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept},
		"too high": {ID: "a", Port: 1880, PortTo: 70000, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept},
		"igmp":     {ID: "a", Port: 0, PortTo: 5, Proto: "igmp", Source: AnySource, Family: FamilyBoth, Target: Accept},
	} {
		if bad.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// task 170: a switched-off rule stays in the list and is not loaded; an owner switching off still
// takes it, and keeping the owner leaves it off
func TestDisabledRule(t *testing.T) {
	c := Reconcile(Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}}, map[string][]PortSpec{OwnerWeb: WebPorts, OwnerSSH: SSHPorts})
	for i := range c.Rules {
		if c.Rules[i].Owner == OwnerSSH {
			c.Rules[i].Disabled = true
		}
	}
	v4, err := Render(c, FamilyV4, sampleLocal)
	if err != nil || strings.Contains(v4, "--dport 22") || !strings.Contains(v4, "--dport 80") {
		t.Fatalf("%v\n%s", err, v4)
	}
	kept := Reconcile(c, map[string][]PortSpec{OwnerWeb: WebPorts, OwnerSSH: SSHPorts})
	if !slices.ContainsFunc(kept.Rules, func(r Rule) bool { return r.Owner == OwnerSSH && r.Disabled }) {
		t.Fatalf("the switched-off rule went: %+v", kept.Rules)
	}
	if gone := Reconcile(c, map[string][]PortSpec{OwnerWeb: WebPorts}); slices.ContainsFunc(gone.Rules, func(r Rule) bool { return r.Owner == OwnerSSH }) {
		t.Fatal("SSH off kept its rule")
	}
}

// task 167: the counters of iptables-save -c laid onto the loaded lines - the local networks'
// several lines summed into their rule, a LOG line left out, the policy from the chain head; a
// table that does not hold these lines is unknown
func TestCounts(t *testing.T) {
	c := Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}, Rules: []Rule{
		{ID: "web", Port: 80, Proto: "tcp", Source: LocalNetworks, Family: FamilyBoth, Target: Accept},
		{ID: "mq", Port: 1883, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept, Log: true},
		{ID: "off", Port: 22, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept, Disabled: true},
	}}
	lines := Lines(c, FamilyV4, sampleLocal)
	save := `# Generated by iptables-save
*filter
:INPUT DROP [7:1500]
:FORWARD ACCEPT [0:0]
:lite-input - [0:0]
[3:180] -A INPUT -i lo -j ACCEPT
[40:4960] -A INPUT -j lite-input
[2:120] -A lite-input -s 10.0.0.0/8 -p tcp -m tcp --dport 80 -j ACCEPT
[5:300] -A lite-input -s 192.168.0.0/16 -p tcp -m tcp --dport 80 -j ACCEPT
[4:240] -A lite-input -p tcp -m tcp --dport 1883 -m limit --limit 10/min -j LOG --log-prefix "fw mq "
[9:540] -A lite-input -p tcp -m tcp --dport 1883 -j ACCEPT
COMMIT
`
	rules, policy, ok := Counts([]byte(save), lines)
	if !ok || rules["web"] != (Count{7, 420}) || rules["mq"] != (Count{9, 540}) || policy != (Count{7, 1500}) {
		t.Fatalf("%v %+v %+v", ok, rules, policy)
	}
	if _, ok := rules["off"]; ok {
		t.Error("a switched-off rule counted")
	}
	// one line missing: not these rules
	short := strings.Replace(save, "[9:540] -A lite-input -p tcp -m tcp --dport 1883 -j ACCEPT\n", "", 1)
	if _, _, ok := Counts([]byte(short), lines); ok {
		t.Error("a table without the lines mapped")
	}
}

// task 175: the ACME owner's rule - port 80 from anywhere, with its comment and label
func TestACMEOwner(t *testing.T) {
	c := Reconcile(Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}}, map[string][]PortSpec{OwnerWeb: WebPorts, OwnerACME: ACMEPorts})
	i := slices.IndexFunc(c.Rules, func(r Rule) bool { return r.Owner == OwnerACME })
	if i < 0 || c.Rules[i].Port != 80 || c.Rules[i].Source != AnySource || !strings.HasPrefix(c.Rules[i].Comment, "ACME HTTP-01") || OwnerLabel(OwnerACME) != "ACME HTTP-01" {
		t.Fatalf("%+v", c.Rules)
	}
	// the web server's own rule for 80 stays beside it, from the local networks
	if !slices.ContainsFunc(c.Rules, func(r Rule) bool { return r.Owner == OwnerWeb && r.Port == 80 && r.Source == LocalNetworks }) {
		t.Fatal("the web rule went")
	}
}

// openccu-lite B-181: an owned rule's id comes from the owner and the port, so a remove and re-add
// (the HmIP access points at every boot) gives the same rule again; different specs differ
func TestOwnedRuleStableID(t *testing.T) {
	a := OwnedRule(OwnerHmIPAP, HmIPAPPorts[0])
	b := OwnedRule(OwnerHmIPAP, HmIPAPPorts[0])
	if a.ID != b.ID || len(a.ID) != 8 || a.ID != OwnedID(OwnerHmIPAP, HmIPAPPorts[0]) {
		t.Fatalf("ids %q %q", a.ID, b.ID)
	}
	seen := map[string]bool{a.ID: true}
	for _, p := range HmIPAPPorts[1:] {
		if id := OwnedID(OwnerHmIPAP, p); seen[id] {
			t.Fatalf("duplicate id %s", id)
		} else {
			seen[id] = true
		}
	}
	if OwnedID(OwnerWeb, WebPorts[0]) == OwnedID(OwnerACME, ACMEPorts[0]) {
		t.Fatal("web/80 and acme/80 share an id")
	}
	// Reconcile re-adds a removed owner's rules with the same ids
	c := Reconcile(Default(), map[string][]PortSpec{OwnerHmIPAP: HmIPAPPorts})
	ids := map[string]bool{}
	for _, r := range c.Rules {
		if r.Owner == OwnerHmIPAP {
			ids[r.ID] = true
		}
	}
	gone := Reconcile(c, map[string][]PortSpec{})
	back := Reconcile(gone, map[string][]PortSpec{OwnerHmIPAP: HmIPAPPorts})
	n := 0
	for _, r := range back.Rules {
		if r.Owner == OwnerHmIPAP {
			if !ids[r.ID] {
				t.Fatalf("a new id %s after the re-add", r.ID)
			}
			n++
		}
	}
	if n != len(HmIPAPPorts) {
		t.Fatalf("%d hmip-ap rules back", n)
	}
}

// openccu-lite B-221: the eQ-3 discovery on udp 43439 moved from the HmIP access point ports to the
// network discovery. An existing system keeps its rule where it was, with the user's target and
// source, under the new owner - one rule, not a removed one and a fresh one at the end.
func TestDiscoveryMovedFromHmIPAP(t *testing.T) {
	oldAP := append(slices.Clone(HmIPAPPorts), PortSpec{Port: 43439, Proto: ProtoUDP, Comment: "device discovery"})
	var oldDiscovery []PortSpec
	for _, p := range DiscoveryPorts {
		if p.Port != 43439 && p.Port != 5353 && p.SPort != 5353 {
			oldDiscovery = append(oldDiscovery, p)
		}
	}
	before := Reconcile(Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}}, map[string][]PortSpec{OwnerWeb: WebPorts, OwnerHmIPAP: oldAP, OwnerDiscovery: oldDiscovery})
	at := slices.IndexFunc(before.Rules, func(r Rule) bool { return r.Owner == OwnerHmIPAP && r.Port == 43439 })
	if at < 0 {
		t.Fatal("no old rule")
	}
	before.Rules[at].Target, before.Rules[at].Source = Drop, "192.0.2.0/24" // the user's choice
	after := Reconcile(before, map[string][]PortSpec{OwnerWeb: WebPorts, OwnerHmIPAP: HmIPAPPorts, OwnerDiscovery: DiscoveryPorts})
	var hits []int
	for i, r := range after.Rules {
		if r.Port == 43439 && r.Proto == ProtoUDP {
			hits = append(hits, i)
		}
	}
	if len(hits) != 1 || hits[0] != at {
		t.Fatalf("43439 rules at %v, the old one was at %d", hits, at)
	}
	r := after.Rules[at]
	if r.Owner != OwnerDiscovery || r.ID != OwnedID(OwnerDiscovery, PortSpec{Port: 43439, Proto: ProtoUDP}) || r.Target != Drop || r.Source != "192.0.2.0/24" || r.Comment != "eQ-3 discovery: apps find the system" {
		t.Errorf("moved rule %+v", r)
	}
	if slices.Contains(after.Active, "hmip-ap/43439/udp") || !slices.Contains(after.Active, "discovery/43439/udp") {
		t.Errorf("active %v", after.Active)
	}
	// the mDNS rules are new at the end; a second reconcile changes nothing
	if n := len(after.Rules) - len(before.Rules); n != 2 {
		t.Errorf("%d rules added", n)
	}
	if again := Reconcile(after, map[string][]PortSpec{OwnerWeb: WebPorts, OwnerHmIPAP: HmIPAPPorts, OwnerDiscovery: DiscoveryPorts}); !slices.EqualFunc(again.Rules, after.Rules, func(a, b Rule) bool { return a == b }) {
		t.Errorf("not stable:\n%+v\n%+v", again.Rules, after.Rules)
	}
	// a system without HmIP-RF has the discovery all the same, from anywhere
	bidcos := Default()
	v4, err := Render(bidcos, FamilyV4, sampleLocal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v4, "-A lite-input -p udp -m udp --dport 43439 -j ACCEPT\n") {
		t.Errorf("no discovery on a system without HmIP:\n%s", v4)
	}
	for _, v := range HmIPAPVerdicts(bidcos) {
		if v.Port == 43439 {
			t.Errorf("43439 listed among the access point ports: %+v", v)
		}
	}
}

// openccu-lite B-339: the IPv6 local networks are the automatic ones plus the configuration's
// list, and a TCP SYN a "local networks" rule refuses is answered with a reset behind the rules
func TestLocalV6AndReset(t *testing.T) {
	c := Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}, LocalV6: []string{"2001:db8:1::/56", "fe80::/10", "2001:db8:1:2::/64"}, Rules: []Rule{
		{ID: "web", Port: 443, Proto: "tcp", Source: LocalNetworks, Family: FamilyBoth, Target: Accept},
		{ID: "web2", Port: 443, Proto: "tcp", Source: LocalNetworks, Family: FamilyBoth, Target: Accept},
		{ID: "ssh", Port: 22, Proto: "tcp", Source: LocalNetworks, Family: FamilyV6, Target: Accept, Dest: "2001:db8::1"},
		{ID: "range", Port: 1880, PortTo: 1890, Proto: "tcp", Source: LocalNetworks, Family: FamilyBoth, Target: Accept, Log: true},
		{ID: "mdns", Port: 5353, Proto: "udp", Source: LocalNetworks, Family: FamilyBoth, Target: Accept},
		{ID: "any", Port: 1883, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: Accept},
		{ID: "v4", Port: 8080, Proto: "tcp", Source: LocalNetworks, Family: FamilyV4, Target: Accept},
		{ID: "off", Port: 8443, Proto: "tcp", Source: LocalNetworks, Family: FamilyBoth, Target: Accept, Disabled: true},
		{ID: "drop", Port: 9000, Proto: "tcp", Source: LocalNetworks, Family: FamilyBoth, Target: Drop},
	}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	v6, err := Render(c, FamilyV6, sampleLocal)
	if err != nil {
		t.Fatal(err)
	}
	// the user's networks after the automatic ones, each once, a contained one dropped
	i := strings.Index(v6, "-A lite-input -p tcp -s fe80::/10 -m tcp --dport 443 -j ACCEPT\n-A lite-input -p tcp -s 2001:db8:1::/56 -m tcp --dport 443 -j ACCEPT\n-A lite-input -p tcp -s fe80::/10 -m tcp --dport 443 -j ACCEPT")
	if i < 0 || strings.Contains(v6, "2001:db8:1:2::/64") {
		t.Errorf("the local networks:\n%s", v6)
	}
	// the resets: after every rule, --syn, one per port spec, a destination kept, a range kept, no
	// udp, no 0/0, no IPv4-only rule, no switched-off rule, no DROP rule
	tail := v6[strings.LastIndex(v6, "-j DROP\n")+len("-j DROP\n"):]
	want := `-A lite-input -p tcp -m tcp --syn --dport 443 -j REJECT --reject-with tcp-reset
-A lite-input -p tcp -d 2001:db8::1 -m tcp --syn --dport 22 -j REJECT --reject-with tcp-reset
-A lite-input -p tcp -m tcp --syn --dport 1880:1890 -j REJECT --reject-with tcp-reset
COMMIT
`
	if tail != want {
		t.Errorf("the tail:\n%s\nwant:\n%s", tail, want)
	}
	// IPv4 unchanged: no reset, no IPv6 network
	v4, _ := Render(c, FamilyV4, sampleLocal)
	if strings.Contains(v4, "tcp-reset") || strings.Contains(v4, "2001:db8") {
		t.Errorf("ipv4:\n%s", v4)
	}
	// policy ACCEPT: nothing to reset; the policy logged: the reset logged as the policy's
	c.Policy.V6 = Accept
	if v6, _ = Render(c, FamilyV6, sampleLocal); strings.Contains(v6, "tcp-reset") {
		t.Errorf("a reset under policy ACCEPT:\n%s", v6)
	}
	c.Policy.V6, c.Policy.Log = Drop, true
	v6, _ = Render(c, FamilyV6, sampleLocal)
	if !strings.Contains(v6, "-A lite-input -p tcp -m tcp --syn --dport 443 -m limit --limit 10/min -j LOG --log-prefix \"fw policy \"\n-A lite-input -p tcp -m tcp --syn --dport 443 -j REJECT --reject-with tcp-reset\n") {
		t.Errorf("the logged reset:\n%s", v6)
	}
	// the counters: a reset line's packets are the policy's, the LOG line left out
	c.Policy.Log = false
	c.Rules = c.Rules[:1]
	c.LocalV6 = nil
	lines := Lines(c, FamilyV6, sampleLocal)
	save := `*filter
:INPUT DROP [7:1500]
:lite-input - [0:0]
[2:120] -A lite-input -s fe80::/10 -p tcp -m tcp --dport 443 -j ACCEPT
[3:180] -A lite-input -p tcp -m tcp --syn --dport 443 -j REJECT --reject-with tcp-reset
COMMIT
`
	rules, policy, ok := Counts([]byte(save), lines)
	if !ok || rules["web"] != (Count{2, 120}) || policy != (Count{10, 1680}) || len(rules) != 1 {
		t.Fatalf("%v %+v %+v", ok, rules, policy)
	}
	// the list is validated: IPv6 networks, each once
	for name, bad := range map[string][]string{
		"v4":    {"10.0.0.0/8"},
		"word":  {"the internet"},
		"any":   {AnySource},
		"local": {LocalNetworks},
		"twice": {"2001:db8::/32", "2001:db8::/32"},
	} {
		c.LocalV6 = bad
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c.LocalV6 = []string{"2001:db8::/32", "2001:db8::1"}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
	// Clone copies the list
	d := c.Clone()
	d.LocalV6[0] = "x"
	if c.LocalV6[0] != "2001:db8::/32" {
		t.Error("clone shares the list")
	}
	// WithV6 with nothing to add is the same list
	if l := sampleLocal.WithV6(nil); !slices.Equal(l.V6, sampleLocal.V6) {
		t.Errorf("%v", l)
	}
}

// openccu-lite task 105: an addon's declared range is one owned rule with port_to; its key in
// Active carries the range, so a reconcile keeps it and a user's edit into another range is theirs
func TestOwnedRange(t *testing.T) {
	spec := PortSpec{Port: 51800, PortTo: 51899, Proto: ProtoTCP, Comment: "redmatic: HomeKit bridges"}
	k := activeKey("addon:redmatic", spec)
	if k != "addon:redmatic/51800-51899/tcp" {
		t.Fatalf("key %q", k)
	}
	o, p, ok := splitKey(k)
	if !ok || o != "addon:redmatic" || p.Port != 51800 || p.PortTo != 51899 || p.Proto != ProtoTCP {
		t.Fatalf("split: %v %q %+v", ok, o, p)
	}
	if _, p2, ok := splitKey("web/443/tcp"); !ok || p2.PortTo != 0 || p2.Port != 443 {
		t.Fatalf("plain key: %v %+v", ok, p2)
	}
	r := OwnedRule("addon:redmatic", spec)
	if r.Port != 51800 || r.PortTo != 51899 || r.Source != AnySource || RuleKey(r) != k {
		t.Fatalf("rule %+v", r)
	}
	c := Reconcile(Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}}, map[string][]PortSpec{"addon:redmatic": {spec}})
	if !slices.Contains(c.Active, k) || len(c.Rules) != 1 || c.Rules[0].PortTo != 51899 {
		t.Fatalf("reconciled: %+v %v", c.Rules, c.Active)
	}
	v4, err := Render(c, FamilyV4, sampleLocal)
	if err != nil || !strings.Contains(v4, "-A lite-input -p tcp -m tcp --dport 51800:51899 -j ACCEPT\n") {
		t.Fatalf("%v\n%s", err, v4)
	}
	// still there after a second reconcile, gone when the owner stops wanting it
	again := Reconcile(c, map[string][]PortSpec{"addon:redmatic": {spec}})
	if len(again.Rules) != 1 || again.Rules[0].ID != c.Rules[0].ID {
		t.Fatalf("not stable: %+v", again.Rules)
	}
	if off := Reconcile(c, map[string][]PortSpec{}); len(off.Rules) != 0 {
		t.Fatalf("not removed: %+v", off.Rules)
	}
}
