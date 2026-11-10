package firewall

import "testing"

// TestAdoptOwned: an owner's rules replaced by rules made elsewhere (the import of a CCU backup's
// classic RPC, occulited task 142), and Reconcile leaves them alone afterwards.
func TestAdoptOwned(t *testing.T) {
	specs := []PortSpec{{Port: 2001, Proto: ProtoTCP}, {Port: 2010, Proto: ProtoTCP}, {Port: 2000, Proto: ProtoTCP}}
	c := Reconcile(Default(), AlwaysOwners())
	c.Rules = append(c.Rules, Rule{ID: "user", Port: 1883, Proto: ProtoTCP, Source: LocalNetworks, Family: FamilyBoth, Target: Accept})
	// a stale rule and a stale key of the owner go
	c.Rules = append(c.Rules, OwnedRule(OwnerRPC, PortSpec{Port: 9292, Proto: ProtoTCP}))
	c.Active = append(c.Active, activeKey(OwnerRPC, PortSpec{Port: 9292, Proto: ProtoTCP}))
	before := len(c.Rules) - 1
	conv := []Rule{
		{ID: "a", Port: 2001, Proto: ProtoTCP, Source: "192.168.1.20", Family: FamilyV4, Target: Accept, Owner: OwnerRPC},
		{ID: "b", Port: 2001, Proto: ProtoTCP, Source: "192.168.1.21", Family: FamilyV4, Target: Accept, Owner: OwnerRPC},
		{ID: "c", Port: 2010, Proto: ProtoTCP, Source: "192.168.1.20", Family: FamilyV4, Target: Accept, Owner: OwnerRPC},
		{ID: "x", Port: 22, Proto: ProtoTCP, Source: AnySource, Family: FamilyBoth, Target: Accept}, // not the owner's: not taken
	}
	out := AdoptOwned(c, OwnerRPC, specs, conv)
	owned := map[int][]string{}
	for _, r := range out.Rules {
		if r.Owner == OwnerRPC {
			owned[r.Port] = append(owned[r.Port], r.Source)
		}
		if r.ID == "x" {
			t.Error("a rule without the owner was adopted")
		}
	}
	if len(owned[2001]) != 2 || len(owned[2010]) != 1 || len(owned[9292]) != 0 {
		t.Errorf("owned rules: %v", owned)
	}
	// 2000 was not converted: the owner's default rule
	if len(owned[2000]) != 1 || owned[2000][0] != OwnedRule(OwnerRPC, specs[2]).Source {
		t.Errorf("the uncovered port: %v", owned[2000])
	}
	if len(out.Rules) != before+4 || len(c.Rules) != before+1 {
		t.Errorf("%d rules, want %d; the input has %d", len(out.Rules), before+4, len(c.Rules))
	}
	if err := out.Validate(); err != nil {
		t.Error(err)
	}
	want := AlwaysOwners()
	want[OwnerRPC] = specs[:2] // hs485d does not run here
	next := Reconcile(out, want)
	n := 0
	for _, r := range next.Rules {
		if r.Owner == OwnerRPC {
			n++
			if r.Port == 2000 || r.Source == LocalNetworks {
				t.Errorf("after the sync: %+v", r)
			}
		}
	}
	if n != 3 {
		t.Errorf("%d owned rules after the sync, want the 3 converted ones", n)
	}
}
