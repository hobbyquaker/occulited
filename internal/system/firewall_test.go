package system

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hobbyquaker/occulited/internal/manifest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/firewall"
)

// the catalogue's shape: ports stay plain numbers, port_info is the sibling map (D-47)
func TestAddonRuntimeDeclaredPorts(t *testing.T) {
	var rt AddonRuntime
	src := `{"ports": [1883, 8883, 8883, 70000], "port_info": {"8883": {"proto": "tcp", "tls": true, "label": {"de": "MQTT über TLS", "en": "MQTT over TLS"}}}}`
	if err := json.Unmarshal([]byte(src), &rt); err != nil {
		t.Fatal(err)
	}
	got := rt.DeclaredPorts()
	// each once, in declared order, out-of-range dropped; 1883 has no entry and no label
	if len(got) != 2 || got[0].Port != 1883 || got[0].TLS || got[0].Label != nil || got[0].Proto != "" {
		t.Fatalf("%+v", got)
	}
	if got[1].Port != 8883 || !got[1].TLS || got[1].Proto != "tcp" || got[1].Label["en"] != "MQTT over TLS" {
		t.Fatalf("%+v", got[1])
	}
	if !rt.Declares(8883) || rt.Declares(9999) || (*AddonRuntime)(nil).Declares(1883) || (*AddonRuntime)(nil).DeclaredPorts() != nil {
		t.Error("Declares")
	}
	// a stored policy without port_info (written before D-47) still reads
	var old AddonRuntime
	if err := json.Unmarshal([]byte(`{"ports": [1883]}`), &old); err != nil || len(old.DeclaredPorts()) != 1 {
		t.Fatalf("%v %+v", err, old)
	}
	// validate: the range, and port_info may only name declared ports
	if err := (&AddonRuntime{Ports: []int{70000}}).validate(); err == nil {
		t.Error("port 70000 accepted")
	}
	if err := (&AddonRuntime{Ports: []int{1883}, PortInfo: map[string]PortInfo{"8883": {}}}).validate(); err == nil {
		t.Error("port_info for an undeclared port accepted")
	}
	if err := rt.validate(); err == nil {
		t.Error("70000 in the declared list accepted")
	}
	rt.Ports = []int{1883, 8883}
	if err := rt.validate(); err != nil {
		t.Error(err)
	}
	// openccu-lite task 105: a range, port_info[port].to - one declaration keyed by its first port
	var rng AddonRuntime
	if err := json.Unmarshal([]byte(`{"ports": [51800, 5540], "port_info": {"51800": {"to": 51899, "proto": "tcp"}, "5540": {"proto": "udp"}}}`), &rng); err != nil {
		t.Fatal(err)
	}
	if got := rng.DeclaredPorts(); len(got) != 2 || got[0].Port != 51800 || got[0].PortTo != 51899 || got[1].PortTo != 0 || !rng.Declares(51800) || rng.Declares(51850) {
		t.Fatalf("%+v", got)
	}
	if err := rng.validate(); err != nil {
		t.Error(err)
	}
	rng.PortInfo["51800"] = PortInfo{To: 51800}
	if err := rng.validate(); err == nil {
		t.Error("to at the port accepted")
	}
	rng.PortInfo["51800"] = PortInfo{To: 70000}
	if err := rng.validate(); err == nil {
		t.Error("to above 65535 accepted")
	}
	// a socket anywhere inside the range is the addon's listening
	d := Port{Port: 51800, PortTo: 51899, Proto: "tcp"}
	in := addonPortState("redmatic", d, []Listener{{Proto: "tcp6", Port: 51891, Unit: "addon-redmatic.service"}})
	out := addonPortState("redmatic", d, []Listener{{Proto: "tcp", Port: 51900, Unit: "addon-redmatic.service"}})
	if !in.Listening || out.Listening || in.PortTo != 51899 {
		t.Errorf("range listening: %+v %+v", in, out)
	}
}

func writePolicy(t *testing.T, r Root, p AddonPolicy) {
	t.Helper()
	if err := os.MkdirAll(r.join(AddonPolicyDir), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if err := os.WriteFile(r.join(AddonPolicyDir+"/"+p.ID+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureFirewall records the calls of FirewallChanged for one test.
func captureFirewall(t *testing.T) *int {
	t.Helper()
	n := new(int)
	old := FirewallChanged
	FirewallChanged = func(context.Context) { *n++ }
	t.Cleanup(func() { FirewallChanged = old })
	return n
}

// task 157: an addon's switch stores the port in its policy and lets the firewall follow; the owners
// are the installed addons' opened, declared ports with their protocol
func TestFirewallManagerAddonOwners(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/etc/config/rc.d/mosq":  "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/other": "#!/bin/sh\n",
	})
	writePolicy(t, r, AddonPolicy{ID: "mosq", Mode: "confined", Runtime: &AddonRuntime{Ports: []int{1883, 8883, 5353}, PortInfo: map[string]PortInfo{"5353": {Proto: "udp"}}}})
	writePolicy(t, r, AddonPolicy{ID: "gone", Mode: "root", Runtime: &AddonRuntime{Ports: []int{4000}}, OpenPorts: []int{4000}}) // no rc.d entry: uninstalled
	writePolicy(t, r, AddonPolicy{ID: "other", Mode: "root"})                                                                    // no runtime block: nothing to offer
	calls := captureFirewall(t)
	m := FirewallManager{Root: r}
	if err := m.SetAddonPorts(t.Context(), "mosq", []int{8883, 5353}); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Errorf("the firewall was told %d times", *calls)
	}
	owners := m.AddonOwners()
	want := []firewall.PortSpec{{Port: 5353, Proto: "udp", Comment: "mosq"}, {Port: 8883, Proto: "tcp", Comment: "mosq"}}
	if len(owners) != 1 || !slices.Equal(owners["addon:mosq"], want) {
		t.Fatalf("owners: %+v", owners)
	}
	if err := m.SetAddonPorts(t.Context(), "mosq", []int{9999}); err == nil {
		t.Error("undeclared port opened")
	}
	if err := m.SetAddonPorts(t.Context(), "nope", []int{1}); err == nil {
		t.Error("addon without a policy")
	}
	view := m.Addons(map[string]string{"mosq": "Mosquitto"}, []Listener{{Port: 1883}})
	if len(view) != 1 || view[0].Name != "Mosquitto" || len(view[0].Ports) != 3 || !view[0].Ports[0].Listening || view[0].Ports[0].Open || !view[0].Ports[1].Open {
		t.Fatalf("%+v", view)
	}
	// uninstalled: the rc.d entry goes, the owner with it
	if err := os.Remove(r.join("/usr/local/etc/config/rc.d/mosq")); err != nil {
		t.Fatal(err)
	}
	if len(m.AddonOwners()) != 0 || len(m.Addons(nil, nil)) != 0 {
		t.Error("an uninstalled addon is still an owner")
	}
}

// SetPolicy with a new runtime block prunes what it no longer declares, and an uninstall on the
// systemd manager lets the firewall follow at once
func TestPolicyChangeAndUninstallCloseAddonPorts(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/etc/config/rc.d/mosq": "#!/bin/sh\necho uninstalled\nexit 0\n",
	})
	if err := os.Chmod(r.join("/usr/local/etc/config/rc.d/mosq"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePolicy(t, r, AddonPolicy{ID: "mosq", Mode: "root", Runtime: &AddonRuntime{Ports: []int{1883, 8883}}, OpenPorts: []int{1883, 8883}})
	var calls []string
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: fakeSystemctl(t, &calls)})
	a.Firewall = &FirewallManager{Root: r}
	told := captureFirewall(t)
	// the catalogue update dropped the plaintext port
	if _, err := a.SetPolicy(t.Context(), "mosq", "root", "catalog", &AddonRuntime{Ports: []int{8883}}); err != nil {
		t.Fatal(err)
	}
	if p := r.ReadAddonPolicy("mosq"); len(p.OpenPorts) != 1 || p.OpenPorts[0] != 8883 {
		t.Errorf("pruned: %+v", p.OpenPorts)
	}
	if got := a.Firewall.AddonOwners()["addon:mosq"]; len(got) != 1 || got[0].Port != 8883 {
		t.Errorf("owners after the prune: %+v", got)
	}
	if _, err := a.Uninstall(t.Context(), "mosq"); err != nil {
		t.Fatal(err)
	}
	// B-283 (maintainer, 2026-09-30): the policy goes with the addon, and with it its opened ports
	if p := r.ReadAddonPolicy("mosq"); p != nil {
		t.Errorf("the policy outlived the addon: %+v", p)
	}
	if got := a.Firewall.AddonOwners()["addon:mosq"]; len(got) != 0 {
		t.Errorf("owned rules after the uninstall: %+v", got)
	}
	if *told < 2 {
		t.Errorf("the firewall was told %d times, want the prune and the uninstall", *told)
	}
}

// an addon installed before its manifest declared any ports has a policy with no runtime block;
// the declaration is the policy's block merged with the stored manifest's
func TestDeclarationMergesTheManifest(t *testing.T) {
	// the merge itself: union by port, the manifest's port_info wins, grants stay the policy's
	policy := &AddonRuntime{Groups: []string{"dialout"}, Ports: []int{1883}, PortInfo: map[string]PortInfo{"1883": {Label: map[string]string{"en": "old"}}}}
	cat := &AddonRuntime{Ports: []int{8883, 1883}, PortInfo: map[string]PortInfo{"1883": {Label: map[string]string{"en": "MQTT, plain"}}, "8883": {TLS: true}}}
	m := MergeRuntime(policy, cat)
	if len(m.Ports) != 2 || m.Ports[0] != 1883 || m.Ports[1] != 8883 || m.PortInfo["1883"].Label["en"] != "MQTT, plain" || !m.PortInfo["8883"].TLS || len(m.Groups) != 1 {
		t.Fatalf("%+v", m)
	}
	if MergeRuntime(nil, cat) != cat || MergeRuntime(policy, nil) != policy || MergeRuntime(nil, nil) != nil {
		t.Error("one side nil is the other side")
	}
	if len(policy.Ports) != 1 || len(cat.Ports) != 2 {
		t.Error("the inputs were changed")
	}

	r := rootWith(t, map[string]string{
		"usr/local/etc/config/rc.d/mosq": "#!/bin/sh\n",
	})
	writePolicy(t, r, AddonPolicy{ID: "mosq", Mode: "confined", Source: "catalog"}) // no runtime block at all
	writeManifest(t, r, "mosq", &AddonRuntime{Ports: []int{1883, 8883}, PortInfo: map[string]PortInfo{"8883": {TLS: true}}})
	captureFirewall(t)
	fm := FirewallManager{Root: r, Declared: r.DeclaredRuntime}
	view := fm.Addons(nil, nil)
	if len(view) != 1 || len(view[0].Ports) != 2 || !view[0].Ports[1].TLS {
		t.Fatalf("the manifest's ports are offered: %+v", view)
	}
	if err := fm.SetAddonPorts(t.Context(), "mosq", []int{8883}); err != nil {
		t.Fatal(err)
	}
	if ports := fm.AddonPorts(); ports[8883] != "mosq" {
		t.Errorf("%v", ports)
	}
	// without the manifest lookup the same policy declares nothing
	if len((FirewallManager{Root: r}).Addons(nil, nil)) != 0 {
		t.Error("a policy without a block declares nothing on its own")
	}
	// SetPolicy prunes against the merged declaration: the manifest still declares 8883, so a
	// runtime block that says nothing about ports does not close it
	var calls []string
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: fakeSystemctl(t, &calls)})
	if _, err := a.SetPolicy(t.Context(), "mosq", "root", "catalog", &AddonRuntime{Groups: []string{"dialout"}}); err != nil {
		t.Fatal(err)
	}
	if p := r.ReadAddonPolicy("mosq"); len(p.OpenPorts) != 1 || p.OpenPorts[0] != 8883 {
		t.Errorf("pruned against the policy alone: %+v", p.OpenPorts)
	}
	// and a manifest that dropped the port closes it
	writeManifest(t, r, "mosq", &AddonRuntime{Ports: []int{1883}})
	if _, err := a.SetPolicy(t.Context(), "mosq", "root", "catalog", &AddonRuntime{}); err != nil {
		t.Fatal(err)
	}
	if p := r.ReadAddonPolicy("mosq"); len(p.OpenPorts) != 0 {
		t.Errorf("%+v", p.OpenPorts)
	}
}

// at start every policy's runtime block follows the addon's stored manifest, whatever its source
// (the facts follow the package; the mode is never touched here), an installed addon without a
// stored manifest adopts the catalogue's adapter when there is one, and the drop-in is rendered
// from the refreshed block
func TestRefreshManifestRuntimes(t *testing.T) {
	r := rootWith(t, map[string]string{"etc/group": "certs:x:8101:\ndialout:x:20:\n", "etc/passwd": "root:x:0:0::/:/bin/sh\n"})
	var calls []string
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: fakeSystemctl(t, &calls)})
	if _, err := a.SetPolicy(t.Context(), "mosq", "confined", "catalog", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetPolicy(t.Context(), "mine", "root", "user", &AddonRuntime{Ports: []int{4000}}); err != nil {
		t.Fatal(err)
	}
	writePolicy(t, r, AddonPolicy{ID: "stale", Mode: "root", Source: "catalog", Runtime: &AddonRuntime{Ports: []int{1, 2}}, OpenPorts: []int{2}})
	writePolicy(t, r, AddonPolicy{ID: "legacy", Mode: "root", Source: "migrated"})
	if ids := a.RefreshManifestRuntimes(); ids != nil {
		t.Errorf("no manifests: %v", ids)
	}
	writeManifest(t, r, "mosq", &AddonRuntime{Groups: []string{"dialout"}, Ports: []int{1883, 8883}})
	writeManifest(t, r, "mine", &AddonRuntime{Ports: []int{9999}})
	writeManifest(t, r, "stale", &AddonRuntime{Ports: []int{1}})
	// the catalogue's adapter for an addon that was installed without one
	a.FallbackManifest = func(id string) (*manifest.Manifest, string) {
		if id == "legacy" {
			return manifestFor("legacy", &AddonRuntime{Root: true, Needs: &[]string{}}), ""
		}
		return nil, ""
	}
	ids := a.RefreshManifestRuntimes()
	if strings.Join(ids, ",") != "legacy,mine,mosq,stale" {
		t.Fatalf("%v", ids)
	}
	if p := r.ReadAddonPolicy("mosq"); p.Runtime == nil || len(p.Runtime.Ports) != 2 || p.Mode != "confined" || p.Source != "catalog" {
		t.Errorf("%+v", p)
	}
	// the user's mode stands, the block follows the package
	if p := r.ReadAddonPolicy("mine"); len(p.Runtime.Ports) != 1 || p.Runtime.Ports[0] != 9999 || p.Mode != "root" || p.Source != "user" {
		t.Errorf("the user's policy: %+v", p)
	}
	// a port the manifest dropped is closed
	if p := r.ReadAddonPolicy("stale"); len(p.Runtime.Ports) != 1 || len(p.OpenPorts) != 0 {
		t.Errorf("stale: %+v", p)
	}
	// the adapter is stored and its block applied; the migrated root mode is not touched by a boot
	if m := r.ReadAddonManifest("legacy"); m == nil || !m.Runtime.Root {
		t.Errorf("the adapter was not adopted: %+v", m)
	}
	if p := r.ReadAddonPolicy("legacy"); p.Mode != "root" || p.Source != "migrated" || p.Runtime == nil || !p.Runtime.Root {
		t.Errorf("legacy: %+v", p)
	}
	if ids := a.RefreshManifestRuntimes(); ids != nil {
		t.Errorf("a second run changes nothing: %v", ids)
	}
	if ids := a.RefreshPolicyDropIns(t.Context()); !slices.Contains(ids, "mosq") {
		t.Errorf("drop-ins rendered from the refreshed blocks: %v", ids)
	}
	if conf := readFile(r.join(AddonPolicyDir + "/mosq.conf")); !strings.Contains(conf, "SupplementaryGroups=dialout certs") {
		t.Errorf("drop-in:\n%s", conf)
	}
}

// occulited B-31: a declared port is the addon's listening port only when a process of its own
// unit holds a socket on it, of its protocol; another holder is named, an unknown owner said. On
// rpi3-1 openccu-loom's 5540/udp showed as listening while RedMatic's node-red held it.
func TestAddonPortListeningIsTheAddonsOwn(t *testing.T) {
	udp := Port{Port: 5540, Proto: "udp"}
	tcp := Port{Port: 8080, Proto: "tcp"}
	anyProto := Port{Port: 9000}
	loom := "addon-openccu-loom.service"
	red := Listener{Proto: "udp", Port: 5540, Process: "node-red", Unit: "addon-redmatic.service"}
	for _, c := range []struct {
		name    string
		d       Port
		sockets []Listener
		want    FirewallAddonPort
	}{
		{"nobody", udp, nil, FirewallAddonPort{}},
		{"its own unit", udp, []Listener{{Proto: "udp6", Port: 5540, Process: "loom", Unit: loom}}, FirewallAddonPort{Listening: true}},
		{"another addon", udp, []Listener{red}, FirewallAddonPort{HeldBy: &PortHolder{Process: "node-red", Unit: "addon-redmatic.service"}}},
		{"another and its own", udp, []Listener{red, {Proto: "udp", Port: 5540, Process: "loom", Unit: loom}}, FirewallAddonPort{Listening: true}},
		{"tcp on a udp port", udp, []Listener{{Proto: "tcp", Port: 5540, Process: "loom", Unit: loom}}, FirewallAddonPort{}},
		{"udp on a tcp port", tcp, []Listener{{Proto: "udp", Port: 8080, Process: "x", Unit: "x.service"}}, FirewallAddonPort{}},
		{"tcp6 on a tcp port", tcp, []Listener{{Proto: "tcp6", Port: 8080, Process: "loom", Unit: loom}}, FirewallAddonPort{Listening: true}},
		{"owner unknown", udp, []Listener{{Proto: "udp", Port: 5540}}, FirewallAddonPort{Listening: true, OwnerUnknown: true}},
		{"no protocol declared", anyProto, []Listener{{Proto: "udp", Port: 9000, Process: "sshd", Unit: "sshd.service"}}, FirewallAddonPort{HeldBy: &PortHolder{Process: "sshd", Unit: "sshd.service"}}},
		{"another port", udp, []Listener{{Proto: "udp", Port: 5541, Process: "loom", Unit: loom}}, FirewallAddonPort{}},
	} {
		got := addonPortState("openccu-loom", c.d, c.sockets)
		if got.Listening != c.want.Listening || got.OwnerUnknown != c.want.OwnerUnknown || fmt.Sprint(got.HeldBy) != fmt.Sprint(c.want.HeldBy) {
			t.Errorf("%s: %+v (held by %v)", c.name, got, got.HeldBy)
		}
	}
}
