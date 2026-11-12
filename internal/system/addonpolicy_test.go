package system

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestAddonPolicy(t *testing.T) {
	r := rootWith(t, map[string]string{
		"etc/passwd":                                 "root:x:0:0::/:/bin/sh\n",
		"etc/group":                                  "root:x:0:\ndialout:x:20:\n",
		"usr/local/addons/mosq/.keep":                "",
		"usr/local/etc/config/rc.d/mosq":             "#!/bin/sh\n",
		"usr/local/etc/config/addon-policy/old.json": `{"id":"old","mode":"confined","uid":30003,"user":"addon-old"}`,
	})
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	// the ownership walk (task 107) noted as the chown -R it replaced
	old := Priv
	Priv = ownersPriv{o: &owners{m: map[string]int{}}, note: func(line string) { calls = append(calls, line) }}
	t.Cleanup(func() { Priv = old })
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
	p, err := a.SetPolicy(context.Background(), "mosq", "confined", "catalog", &AddonRuntime{Capabilities: []string{"CAP_NET_BIND_SERVICE"}, Groups: []string{"dialout"}, Paths: []string{"/etc/config/mosquitto"}, Ports: []int{1883}})
	if err != nil {
		t.Fatal(err)
	}
	if p.UID != 30004 || p.User != "addon-mosq" || p.Mode != "confined" {
		t.Fatalf("%+v", p)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"chown -R 30004:30004 " + r.join("/usr/local/addons/mosq"), "systemctl daemon-reload"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	// B-54: the user and its group are appended in place by the helper's operation; busybox
	// adduser/addgroup (a temporary file plus rename) no longer run at all
	if strings.Contains(joined, "adduser") || strings.Contains(joined, "addgroup") {
		t.Errorf("busybox account tools ran:\n%s", joined)
	}
	if b, _ := os.ReadFile(r.join("/etc/passwd")); string(b) != "root:x:0:0::/:/bin/sh\naddon-mosq:x:30004:30004::/usr/local/addons/mosq:/bin/false\n" {
		t.Errorf("passwd:\n%s", b)
	}
	if b, _ := os.ReadFile(r.join("/etc/group")); string(b) != "root:x:0:\ndialout:x:20:\naddon-mosq:x:30004:\n" {
		t.Errorf("group:\n%s", b)
	}
	if !strings.Contains(joined, "chown -R 30004:30004 "+r.join("/usr/local/etc/config/addons/mosq")) {
		t.Error("the addon's config directory is created and chowned")
	}
	conf, _ := os.ReadFile(r.join(AddonPolicyDir + "/mosq.conf"))
	for _, want := range []string{"[Service]\nUser=addon-mosq\nGroup=addon-mosq\n", "SupplementaryGroups=dialout\n", "AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n", "ProtectSystem=strict\n", "RuntimeDirectory=addon-mosq\n", "ReadWritePaths=-/usr/local/addons/mosq -/usr/local/etc/config/addons/mosq -/usr/local/etc/config/rc.d -/run -/var/log -/tmp -/var/tmp -/etc/config/mosquitto\n"} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("drop-in lacks %q:\n%s", want, conf)
		}
	}
	if got := r.ReadAddonPolicy("mosq"); got == nil || got.Runtime == nil || got.Runtime.Ports[0] != 1883 || got.Source != "catalog" {
		t.Errorf("%+v", got)
	}
	// back to root: the runtime block is kept, the uid stays; the drop-in is root without the
	// right to mount (D-66) - a [Service] section with the bounding set alone, no User=
	calls = nil
	p, err = a.SetPolicy(context.Background(), "mosq", "root", "user", nil)
	if err != nil || p.UID != 30004 || p.Runtime == nil || p.Source != "user" {
		t.Fatalf("%v %+v", err, p)
	}
	conf, _ = os.ReadFile(r.join(AddonPolicyDir + "/mosq.conf"))
	if !strings.Contains(string(conf), "mode=root\n[Service]\nCapabilityBoundingSet=~CAP_SYS_ADMIN\n") || strings.Contains(string(conf), "User=") || strings.Contains(string(conf), "ProtectSystem") {
		t.Errorf("root drop-in:\n%s", conf)
	}
	// confined again: the user is there with the stored uid -> the operation is idempotent, the
	// files keep their one line each and the uid is reused
	calls = nil
	p, err = a.SetPolicy(context.Background(), "mosq", "confined", "user", nil)
	if err != nil || p.UID != 30004 {
		t.Errorf("%v uid %d calls %v", err, p.UID, calls)
	}
	if b, _ := os.ReadFile(r.join("/etc/passwd")); strings.Count(string(b), "addon-mosq:") != 1 {
		t.Errorf("passwd after confining again:\n%s", b)
	}
	if b, _ := os.ReadFile(r.join("/etc/group")); strings.Count(string(b), "addon-mosq:") != 1 {
		t.Errorf("group after confining again:\n%s", b)
	}
	// the stored uid taken by another account: refused, the addon is not confined onto it
	_ = os.WriteFile(r.join("/etc/passwd"), []byte("root:x:0:0::/:/bin/sh\naddon-other:x:30004:30004::/usr/local/addons/other:/bin/false\n"), 0o644)
	if _, err := a.SetPolicy(context.Background(), "mosq", "confined", "user", nil); err == nil || !strings.Contains(err.Error(), "belongs to addon-other") {
		t.Errorf("uid conflict: %v", err)
	}
	_ = os.WriteFile(r.join("/etc/passwd"), []byte("root:x:0:0::/:/bin/sh\naddon-mosq:x:30004:30004::/usr/local/addons/mosq:/bin/false\n"), 0o644)
	// validation
	if _, err := a.SetPolicy(context.Background(), "../x", "root", "user", nil); err == nil {
		t.Error("bad id")
	}
	if _, err := a.SetPolicy(context.Background(), "mosq", "confined", "user", &AddonRuntime{Capabilities: []string{"cap_sys_admin"}}); err == nil {
		t.Error("bad capability")
	}
	if _, err := a.SetPolicy(context.Background(), "mosq", "confined", "user", &AddonRuntime{Paths: []string{"/usr/../etc"}}); err == nil {
		t.Error("bad path")
	}
	if ids := r.PolicyIDs(); len(ids) != 2 || ids[0] != "mosq" {
		t.Errorf("%v", ids)
	}
}

// D-46: a confined addon joins the certs group when the box has it, and only then
func TestDropInRootMayMount(t *testing.T) {
	p := &AddonPolicy{ID: "mounter", Mode: "root", Runtime: &AddonRuntime{Root: true, Capabilities: []string{CapSysAdmin}}}
	got := renderDropIn(p, allGroups, "")
	if strings.Contains(got, "[Service]") || !strings.Contains(got, "# mode=root\n# may mount") {
		t.Errorf("an entry declaring CAP_SYS_ADMIN keeps the full set:\n%s", got)
	}
	// without the declaration, and with no block at all: the bounding set loses it
	for _, p := range []*AddonPolicy{{ID: "x", Mode: "root"}, {ID: "x", Mode: "root", Runtime: &AddonRuntime{Capabilities: []string{"CAP_NET_ADMIN"}}}} {
		if got := renderDropIn(p, allGroups, ""); !strings.HasSuffix(got, "# mode=root\n[Service]\nCapabilityBoundingSet=~CAP_SYS_ADMIN\n") {
			t.Errorf("root drop-in:\n%s", got)
		}
	}
	// a confined addon's set is what it declares - among the capabilities that are not
	// root-equivalent (B-251)
	if got := renderDropIn(&AddonPolicy{ID: "x", Mode: "confined", UID: 30005, User: "addon-x", Runtime: &AddonRuntime{Capabilities: []string{"CAP_NET_BIND_SERVICE"}}}, noCertsGroup, ""); !strings.Contains(got, "AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n") {
		t.Errorf("confined:\n%s", got)
	}
}

// TestDropInDenylist is B-251's second guard: renderDropIn never renders a root-equivalent
// capability or group for a confined addon, even when the stored policy carries one, and the
// harmless declarations beside them survive.
func TestDropInDenylist(t *testing.T) {
	p := &AddonPolicy{ID: "x", Mode: "confined", UID: 30005, User: "addon-x", Runtime: &AddonRuntime{
		Capabilities: []string{"CAP_SYS_ADMIN", "CAP_NET_BIND_SERVICE", "CAP_DAC_READ_SEARCH"},
		Groups:       []string{"occulite", "dialout", "root"},
	}}
	got := renderDropIn(p, noCertsGroup, "")
	// the denied names must not appear in the rendered directives (the header comment mentions
	// "occulited", so check the lines, not the whole string)
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "AmbientCapabilities=") && !strings.HasPrefix(line, "CapabilityBoundingSet=") && !strings.HasPrefix(line, "SupplementaryGroups=") {
			continue
		}
		for _, denied := range []string{"CAP_SYS_ADMIN", "CAP_DAC_READ_SEARCH", "occulite", "root"} {
			if strings.Contains(line, denied) {
				t.Errorf("denied token %q rendered in %q:\n%s", denied, line, got)
			}
		}
	}
	if !strings.Contains(got, "AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n") {
		t.Errorf("the harmless capability was lost:\n%s", got)
	}
	if !strings.Contains(got, "SupplementaryGroups=dialout\n") {
		t.Errorf("the harmless group was lost:\n%s", got)
	}
	// a confined addon that declares only denied capabilities ends with an empty bounding set
	only := renderDropIn(&AddonPolicy{ID: "x", Mode: "confined", UID: 30006, User: "addon-x", Runtime: &AddonRuntime{Capabilities: []string{"CAP_SYS_MODULE"}}}, noCertsGroup, "")
	if !strings.Contains(only, "CapabilityBoundingSet=\n") || strings.Contains(only, "AmbientCapabilities=") {
		t.Errorf("a confined addon with only denied caps must get an empty set:\n%s", only)
	}
}

// allGroups and noCertsGroup stand in for Root.HasGroup: a system that knows every group, and one
// that knows every group but certs.
func allGroups(string) bool      { return true }
func noCertsGroup(g string) bool { return g != CertsGroup }

// B-259: a declared group the system does not know is left out of the drop-in (systemd would
// refuse the whole unit), the known ones stay; usbstorage is rendered only where the image has it.
func TestDropInUnknownGroup(t *testing.T) {
	p := &AddonPolicy{ID: "mosq", Mode: "confined", UID: 30004, User: "addon-mosq", Runtime: &AddonRuntime{Groups: []string{USBStorageGroup, "dialout"}, Paths: []string{"/media"}}}
	if got := renderDropIn(p, allGroups, ""); !strings.Contains(got, "SupplementaryGroups=usbstorage dialout certs\n") || !strings.Contains(got, " -/media\n") {
		t.Errorf("a system with the group:\n%s", got)
	}
	older := func(g string) bool { return g == "dialout" || g == CertsGroup }
	got := renderDropIn(p, older, "")
	if !strings.Contains(got, "SupplementaryGroups=dialout certs\n") || strings.Contains(got, USBStorageGroup) {
		t.Errorf("a system without usbstorage:\n%s", got)
	}
	if got := renderDropIn(p, func(string) bool { return false }, ""); strings.Contains(got, "SupplementaryGroups=") {
		t.Errorf("a system without any of the groups:\n%s", got)
	}
	// a root addon's drop-in names no group, known or not
	if got := renderDropIn(&AddonPolicy{ID: "x", Mode: "root", Runtime: &AddonRuntime{Groups: []string{"nosuchgroup"}}}, allGroups, ""); strings.Contains(got, "SupplementaryGroups") {
		t.Errorf("root:\n%s", got)
	}
}

func TestDropInCertsGroup(t *testing.T) {
	p := &AddonPolicy{ID: "mosq", Mode: "confined", UID: 30004, User: "addon-mosq", Runtime: &AddonRuntime{Groups: []string{"dialout"}}}
	if got := renderDropIn(p, allGroups, ""); !strings.Contains(got, "SupplementaryGroups=dialout certs\n") {
		t.Errorf("with the group:\n%s", got)
	}
	if got := renderDropIn(p, noCertsGroup, ""); !strings.Contains(got, "SupplementaryGroups=dialout\n") || strings.Contains(got, "certs") {
		t.Errorf("without the group:\n%s", got)
	}
	if got := renderDropIn(&AddonPolicy{ID: "x", Mode: "confined", UID: 30005, User: "addon-x"}, allGroups, ""); !strings.Contains(got, "SupplementaryGroups=certs\n") {
		t.Errorf("no runtime block:\n%s", got)
	}
	r := fakeRoot(t)
	if r.HasGroup("certs") {
		t.Error("fake root has no certs group")
	}
	if err := os.MkdirAll(r.join("etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.join("etc/group"), []byte("root:x:0:\ncerts:x:8101:\naddon-mosq:x:30004:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !r.HasGroup("certs") || r.HasGroup("cert") {
		t.Error("HasGroup reads /etc/group by exact name")
	}
}

// a stored policy's drop-in is re-rendered at start: the certs group arrives on an addon that
// was confined before the box had the group
func TestRefreshPolicyDropIns(t *testing.T) {
	r := fakeRoot(t)
	var calls []string
	a := &SystemdAddons{Scripts: AddonScripts{Root: r}, Systemd: SystemdServices{Root: r, Run: fakeSystemctl(t, &calls)}}
	if _, err := a.SetPolicy(t.Context(), "mosq", "confined", "catalog", nil); err != nil {
		t.Fatal(err)
	}
	conf := func() string { b, _ := os.ReadFile(r.join(AddonPolicyDir + "/mosq.conf")); return string(b) }
	if strings.Contains(conf(), "certs") {
		t.Fatal("no certs group yet")
	}
	if ids := a.RefreshPolicyDropIns(t.Context()); len(ids) != 0 {
		t.Errorf("nothing changed, got %v", ids)
	}
	if err := os.WriteFile(r.join("etc/group"), []byte("certs:x:8101:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ids := a.RefreshPolicyDropIns(t.Context()); len(ids) != 1 || ids[0] != "mosq" || !strings.Contains(conf(), "SupplementaryGroups=certs") {
		t.Errorf("ids %v conf:\n%s", ids, conf())
	}
	if !strings.Contains(strings.Join(calls, "\n"), "daemon-reload") {
		t.Error("systemd reloaded after a change")
	}
}
