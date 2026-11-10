package firewall

import (
	"reflect"
	"testing"
)

// openccu-lite task 217: what the confirmed rules do with the access point ports - the first
// enabled rule for the port, passing over rules narrowed by a source port or a destination, or the
// policy.
func TestVerdict(t *testing.T) {
	c := Config{Policy: Policy{V4: "DROP", V6: "DROP"}, Rules: []Rule{
		{ID: "d1", Proto: "udp", SPort: 43439, Source: AnySource, Family: FamilyBoth, Target: "ACCEPT"},
		{ID: "m1", Proto: "udp", Dest: "224.0.0.0/24", Source: AnySource, Family: FamilyV4, Target: "ACCEPT"},
		{ID: "v6", Port: 9293, Proto: "tcp", Source: AnySource, Family: FamilyV6, Target: "ACCEPT"},
		{ID: "off", Port: 9293, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: "ACCEPT", Disabled: true},
		{ID: "r1", Port: 9293, Proto: "tcp", Source: LocalNetworks, Family: FamilyBoth, Target: "REJECT", Owner: OwnerHmIPAP},
		{ID: "r2", Port: 9293, Proto: "tcp", Source: AnySource, Family: FamilyBoth, Target: "ACCEPT"},
		{ID: "rg", Port: 9000, PortTo: 9300, Proto: "tcp", Source: AnySource, Family: FamilyV4, Target: "ACCEPT"},
		{ID: "all", Proto: "udp", Source: "192.0.2.0/24", Family: FamilyV4, Target: "ACCEPT"},
	}}
	for _, tc := range []struct {
		proto string
		port  int
		want  PortVerdict
	}{
		{"tcp", 9293, PortVerdict{Port: 9293, Proto: "tcp", Target: "REJECT", Rule: "r1", Source: LocalNetworks, Owner: OwnerHmIPAP}},
		{"tcp", 9294, PortVerdict{Port: 9294, Proto: "tcp", Target: "ACCEPT", Rule: "rg", Source: AnySource, Sources: []string{AnySource}}},
		{"udp", 43438, PortVerdict{Port: 43438, Proto: "udp", Target: "ACCEPT", Rule: "all", Source: "192.0.2.0/24", Sources: []string{"192.0.2.0/24"}}},
		{"tcp", 9400, PortVerdict{Port: 9400, Proto: "tcp", Target: "DROP"}},
	} {
		if got := Verdict(c, tc.proto, tc.port); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s/%d: %+v, want %+v", tc.proto, tc.port, got, tc.want)
		}
	}
	v := HmIPAPVerdicts(c)
	if len(v) != 3 || v[0].Port != 9293 || v[1].Port != 9294 || v[2].Port != 43438 {
		t.Errorf("the access point ports without the discovery: %+v", v)
	}
}

// B-134: an accepted port names every source it is accepted from. A firewall converted from a
// restricted OpenCCU one has a rule per allowed address (hobbyquaker/openccu-lite#17: 192.168.0.1
// and 192.168.0.0/16 for each classic RPC port), and the first alone made the access look narrower.
func TestVerdictSources(t *testing.T) {
	rule := func(id, source, target string) Rule {
		return Rule{ID: id, Port: 2001, Proto: "tcp", Source: source, Family: FamilyBoth, Target: target, Owner: OwnerRPC}
	}
	with := func(r Rule, change func(*Rule)) Rule { change(&r); return r }
	for _, tc := range []struct {
		name    string
		rules   []Rule
		target  string
		rule    string
		sources []string
	}{
		{"the converted pair", []Rule{rule("a", "192.168.0.1", Accept), rule("b", "192.168.0.0/16", Accept)}, Accept, "a", []string{"192.168.0.1", "192.168.0.0/16"}},
		{"a source named twice is listed once", []Rule{rule("a", LocalNetworks, Accept), rule("b", LocalNetworks, Accept)}, Accept, "a", []string{LocalNetworks}},
		{"a disabled rule, another port, another protocol and an IPv6 rule do not count", []Rule{
			rule("a", "192.168.0.1", Accept),
			with(rule("off", "10.0.0.0/8", Accept), func(r *Rule) { r.Disabled = true }),
			with(rule("other", "10.1.0.0/16", Accept), func(r *Rule) { r.Port = 2010 }),
			with(rule("udp", "10.2.0.0/16", Accept), func(r *Rule) { r.Proto = "udp" }),
			with(rule("v6", "fd00::/8", Accept), func(r *Rule) { r.Family = FamilyV6 }),
			with(rule("range", "172.16.0.0/12", Accept), func(r *Rule) { r.Port, r.PortTo = 2000, 2010 }),
		}, Accept, "a", []string{"192.168.0.1", "172.16.0.0/12"}},
		{"nothing behind a rule from anywhere is reached", []Rule{rule("a", "192.168.0.1", Accept), rule("any", AnySource, Accept), rule("late", "10.0.0.0/8", Accept)}, Accept, "a", []string{"192.168.0.1", AnySource}},
		{"nor behind a DROP from anywhere", []Rule{rule("a", "192.168.0.1", Accept), rule("drop", AnySource, "DROP"), rule("late", "10.0.0.0/8", Accept)}, Accept, "a", []string{"192.168.0.1"}},
		{"a DROP for one source between them is passed over", []Rule{rule("a", "192.168.0.1", Accept), rule("drop", "192.168.5.5", "DROP"), rule("b", "192.168.0.0/16", Accept)}, Accept, "a", []string{"192.168.0.1", "192.168.0.0/16"}},
		{"a port that is not accepted names no sources", []Rule{rule("r", LocalNetworks, "REJECT"), rule("b", "192.168.0.0/16", Accept)}, "REJECT", "r", nil},
		{"the policy decides: none", nil, "DROP", "", nil},
	} {
		got := Verdict(Config{Policy: Policy{V4: "DROP", V6: "DROP"}, Rules: tc.rules}, "tcp", 2001)
		if got.Target != tc.target || got.Rule != tc.rule || !reflect.DeepEqual(got.Sources, tc.sources) {
			t.Errorf("%s: target %s, rule %q, sources %v; want %s, %q, %v", tc.name, got.Target, got.Rule, got.Sources, tc.target, tc.rule, tc.sources)
		}
		if len(got.Sources) > 0 && got.Sources[0] != got.Source {
			t.Errorf("%s: the deciding rule's source %q is not the first of %v", tc.name, got.Source, got.Sources)
		}
	}
}
