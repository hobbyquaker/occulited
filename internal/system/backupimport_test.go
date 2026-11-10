package system

import (
	"context"
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// importRoot is a system below a temporary directory with the files the import's groups read.
func importRoot(t *testing.T) Root {
	t.Helper()
	old := Priv
	Priv = priv.Local{}
	t.Cleanup(func() { Priv = old; ForgetAddonScans("") })
	dir := t.TempDir()
	for _, d := range []string{"etc/config", "usr/local/etc/config/rc.d", "usr/local/addons", "run/occulite/radio"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return Root(dir)
}

func groupsByID(gs []ImportGroup) map[string]ImportGroup {
	out := map[string]ImportGroup{}
	for _, g := range gs {
		out[g.ID] = g
	}
	return out
}

func TestImportGroupsAndTheirDefaults(t *testing.T) {
	r := importRoot(t)
	b, err := InspectBackup(backupOf(t, "VERSION=3.89.11.20260919\n", ccuMembers()...))
	if err != nil {
		t.Fatal(err)
	}
	gs := r.ImportGroups(b)
	var order []string
	for _, g := range gs {
		order = append(order, g.ID)
	}
	if strings.Join(order, " ") != "addons hb_rf_eth hostname loghost classic_rpc" {
		t.Fatalf("groups: %v", order)
	}
	g := groupsByID(gs)
	// everything ticked except classic RPC; the host name because this system has none of its own
	for id, def := range map[string]bool{GroupAddons: true, GroupHBRFETH: true, GroupHostname: true, GroupLogHost: true, GroupClassicRPC: false} {
		if g[id].Default != def || g[id].Unavailable != "" {
			t.Errorf("%s: default %v unavailable %q", id, g[id].Default, g[id].Unavailable)
		}
	}
	if g[GroupHostname].Value != "old-ccu" || g[GroupLogHost].Value != "loghost.lan:5514" || len(g[GroupAddons].Addons) != 3 || len(g[GroupClassicRPC].Sources) != 2 {
		t.Errorf("values: %+v", g)
	}
	if err := r.CheckImportGroups(b, []string{GroupAddons, GroupHostname, GroupClassicRPC}); err != nil {
		t.Errorf("an offered selection: %v", err)
	}
	for _, bad := range [][]string{{"users"}, {GroupHostname, GroupHostname}, {""}} {
		if err := r.CheckImportGroups(b, bad); !errors.Is(err, ErrGroupUnavailable) {
			t.Errorf("%v: %v", bad, err)
		}
	}

	// this system has a name of its own, a syslog server, classic RPC on, and one of the addons
	_ = os.WriteFile(r.join("/etc/config/netconfig"), []byte("HOSTNAME=mine\nMODE=DHCP\n"), 0o644)
	_ = os.WriteFile(r.join("/etc/config/syslog"), []byte("LOGHOST=here.lan\n"), 0o644)
	_ = os.WriteFile(r.join(ClassicRPCPlainMarker), nil, 0o644)
	_ = os.MkdirAll(r.join("/usr/local/addons/wd"), 0o755)
	g = groupsByID(r.ImportGroups(b))
	if g[GroupHostname].Default || g[GroupHostname].Current != "mine" {
		t.Errorf("the host name of a system that has one is ticked: %+v", g[GroupHostname])
	}
	if g[GroupLogHost].Current != "here.lan" || !g[GroupLogHost].Default {
		t.Errorf("loghost: %+v", g[GroupLogHost])
	}
	if g[GroupClassicRPC].Unavailable != "on" {
		t.Errorf("classic RPC on a system that has it on: %+v", g[GroupClassicRPC])
	}
	if a := g[GroupAddons]; a.Unavailable != "" || !a.Addons[2].Here || a.Addons[0].Here {
		t.Errorf("addons: %+v", a)
	}
	if err := r.CheckImportGroups(b, []string{GroupClassicRPC}); !errors.Is(err, ErrGroupUnavailable) {
		t.Errorf("classic RPC although it is on: %v", err)
	}

	// values the setters refuse, the old system's login: shown, not selectable
	bad, _ := InspectBackup(backupOf(t, "VERSION=3.89.11\n",
		bm{name: cfgDir + "netconfig", body: "HOSTNAME=bad_name\n"},
		bm{name: cfgDir + "firewall.conf", body: "[SERVICE XMLRPC]\nId = XMLRPC\nPorts = 2001\nAccess = full\n"},
		bm{name: cfgDir + "authEnabled"},
		bm{name: cfgDir + "rc.d/wd", body: "x", mode: 0o755},
	))
	_ = os.Remove(r.join(ClassicRPCPlainMarker))
	g = groupsByID(r.ImportGroups(bad))
	if g[GroupHostname].Unavailable != "invalid" || g[GroupHostname].Default || g[GroupClassicRPC].Unavailable != "auth" || g[GroupAddons].Unavailable != "here" || g[GroupAddons].Default {
		t.Errorf("unavailable groups: %+v", g)
	}
	// an openccu-lite backup has no groups: that is the restore
	lite, _ := InspectBackup(backupOf(t, "VERSION=3.89.11.20260919\n", append(ccuMembers(), bm{name: "usr/local/etc/occulite/users.json", body: "{}", at: day2.AddDate(0, 0, 1)})...))
	if lite.Kind != BackupKindLite || len(r.ImportGroups(lite)) != 0 {
		t.Errorf("an openccu-lite backup offers groups: %v", r.ImportGroups(lite))
	}
	if err := r.CheckImportGroups(lite, []string{GroupAddons}); !errors.Is(err, ErrGroupUnavailable) {
		t.Errorf("a group of an openccu-lite backup: %v", err)
	}
}

func TestImportSettings(t *testing.T) {
	r := importRoot(t)
	_ = os.WriteFile(r.join("/etc/config/netconfig"), []byte("HOSTNAME=\nMODE=MANUAL\nIP=192.0.2.5\nNETMASK=255.255.255.0\nGATEWAY=192.0.2.1\nNAMESERVER1=192.0.2.1\n"), 0o644)
	if err := r.ImportHostname("old-ccu"); err != nil {
		t.Fatal(err)
	}
	nc := readFile(r.join("/etc/config/netconfig"))
	if !strings.Contains(nc, "HOSTNAME=old-ccu") || !strings.Contains(nc, "IP=192.0.2.5") || !strings.Contains(nc, "MODE=MANUAL") {
		t.Errorf("netconfig after the host name: %q", nc)
	}
	for _, bad := range []string{"", "a b", "x;reboot", "-x", strings.Repeat("a", 64), "$(id)"} {
		if err := r.ImportHostname(bad); err == nil {
			t.Errorf("host name %q was taken", bad)
		}
	}

	_ = os.WriteFile(r.join("/etc/config/syslog"), []byte("LOGLEVEL_RFD=2\nLOGLEVEL_HS485D=2\nLOGLEVEL_HMIP=WARN\n"), 0o644)
	if err := r.ImportLogHost(" loghost.lan:5514 "); err != nil {
		t.Fatal(err)
	}
	sl := readFile(r.join("/etc/config/syslog"))
	if !strings.Contains(sl, "LOGHOST=loghost.lan:5514") || !strings.Contains(sl, "LOGLEVEL_RFD=2") || !strings.Contains(sl, "LOGLEVEL_HMIP=WARN") {
		t.Errorf("syslog after the import: %q", sl)
	}
	for _, bad := range []string{"", "a b", "`id`", "$(reboot)", "host:99999", "h;x"} {
		if err := r.ImportLogHost(bad); err == nil {
			t.Errorf("LOGHOST %q was taken", bad)
		}
	}

	if err := r.ImportHBRFETH(" 192.168.1.50\n"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(r.join("/etc/config/hb_rf_eth")); got != "192.168.1.50\n" {
		t.Errorf("hb_rf_eth: %q", got)
	}
	for _, bad := range []string{"", "board.lan", "fd00::1", "127.0.0.1", "0.0.0.0", "1.2.3.4; reboot"} {
		if err := r.ImportHBRFETH(bad); err == nil {
			t.Errorf("address %q was taken", bad)
		}
	}
}

func TestImportClassicRPC(t *testing.T) {
	r := importRoot(t)
	b, _ := InspectBackup(backupOf(t, "VERSION=3.89.11.20260919\n", ccuMembers()...))
	if _, err := r.ImportClassicRPC(b.ClassicRPC); err == nil {
		t.Fatal("converted without a rule file")
	}
	// this system: its defaults, a rule of the user's
	c := firewall.Reconcile(firewall.Default(), firewall.AlwaysOwners())
	c.Rules = append(c.Rules, firewall.Rule{ID: firewall.NewID(), Port: 1883, Proto: firewall.ProtoTCP, Source: firewall.LocalNetworks, Family: firewall.FamilyBoth, Target: firewall.Accept, Comment: "mine"})
	if err := r.WriteRules(c); err != nil {
		t.Fatal(err)
	}
	n, err := r.ImportClassicRPC(b.ClassicRPC)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.ReadRules()
	rpc, sources := 0, map[string]bool{}
	for _, x := range got.Rules {
		if x.Owner == firewall.OwnerRPC {
			rpc++
			sources[x.Source] = true
		}
	}
	// restricted to two addresses: every classic port from each, and from nowhere else
	if n != rpc || rpc != 16 || len(sources) != 2 || !sources["192.168.1.20"] || !sources["fd00::20"] {
		t.Errorf("%d rules (%d owned), sources %v", n, rpc, sources)
	}
	if len(got.Rules) != len(c.Rules)+16 || got.Policy != c.Policy || got.Migration != nil {
		t.Errorf("the rest of the rule list changed: %d rules, policy %+v", len(got.Rules), got.Policy)
	}
	if cr := r.ReadClassicRPC(); !cr.Plain || !cr.TLS || cr.Auth != "none" {
		t.Errorf("classic RPC: %+v", cr)
	}
	// the owner's next sync keeps the converted rules and drops only what it does not serve
	want := firewall.AlwaysOwners()
	want[firewall.OwnerRPC] = classicSpecs(ClassicRPCPorts(true, true, false))
	next := firewall.Reconcile(got, want)
	kept := 0
	for _, x := range next.Rules {
		if x.Owner == firewall.OwnerRPC {
			kept++
			if x.Source == firewall.LocalNetworks || x.Port == 2000 || x.Port == 42000 {
				t.Errorf("after the sync: %+v", x)
			}
		}
	}
	if kept != 12 {
		t.Errorf("%d classic RPC rules after the sync, want 12", kept)
	}
	// the old system's login cannot come along; a closed service is nothing to convert
	auth := *b.ClassicRPC
	auth.Auth = true
	if _, err := r.ImportClassicRPC(&auth); err == nil {
		t.Error("converted although the old system asked for a login")
	}
	if _, err := r.ImportClassicRPC(nil); err == nil {
		t.Error("converted without a firewall.conf")
	}
}

func TestImportAddons(t *testing.T) {
	r := importRoot(t)
	withArch(t, "amd64")
	elfPath := filepath.Join(t.TempDir(), "daemon")
	fakeELF(t, elfPath, elf.ELFCLASS32, elf.EM_ARM, "/lib/ld-linux-armhf.so.3")
	armBytes, _ := os.ReadFile(elfPath)
	armELF := string(armBytes)
	sbk := backupOf(t, "VERSION=3.89.11.20260919\n",
		bm{name: cfgDir + "hm_addons.cfg", body: "fine {CONFIG_URL /addons/fine/ CONFIG_NAME {Fine Addon} CONFIG_DESCRIPTION {de x en y}}\n"},
		// runs here
		bm{name: cfgDir + "rc.d/fine", body: "#!/bin/sh\n", mode: 0o755},
		bm{name: "usr/local/addons/fine/run.sh", body: "#!/bin/sh\necho hi\n", mode: 0o755},
		bm{name: cfgDir + "addons/fine/settings", body: "a=1"},
		// a program for another architecture
		bm{name: cfgDir + "rc.d/arm", body: "#!/bin/sh\n", mode: 0o755},
		bm{name: "usr/local/addons/arm/bin/daemon", body: armELF, mode: 0o755},
		// needs the ReGa
		bm{name: cfgDir + "rc.d/rega", body: "#!/bin/sh\n", mode: 0o755},
		bm{name: "usr/local/addons/rega/x.tcl", body: "load tclrega.so\nrega_script {var x = dom.GetObject(1);}\n", mode: 0o644},
		// switched off on the old system
		bm{name: cfgDir + "rc.d/off", body: "#!/bin/sh\n", mode: 0o644},
		bm{name: "usr/local/addons/off/run.sh", body: "#!/bin/sh\n", mode: 0o755},
		// its program was not in the backup
		bm{name: cfgDir + "rc.d/noprog", body: "#!/bin/sh\n", mode: 0o755},
		bm{name: cfgDir + "addons/noprog/settings", body: "a=1"},
		// here already
		bm{name: cfgDir + "rc.d/here", body: "#!/bin/sh\nnew\n", mode: 0o755},
		bm{name: "usr/local/addons/here/new", body: "x"},
		// a user's hack: an entry that leads out of the addons' tree
		bm{name: cfgDir + "rc.d/hack", link: "/usr/local/bin/hack.sh"},
	)
	_ = os.MkdirAll(r.join("/usr/local/addons/here"), 0o755)
	_ = os.WriteFile(r.join("/usr/local/addons/here/mine"), []byte("kept"), 0o644)
	_ = os.WriteFile(r.join("/usr/local/etc/config/hm_addons.cfg"), []byte("here {CONFIG_URL /addons/here/ CONFIG_NAME Here CONFIG_DESCRIPTION {de x en y}}\n"), 0o664)
	b, err := InspectBackup(sbk)
	if err != nil {
		t.Fatal(err)
	}
	var pinned []string
	res, err := r.ImportAddons(context.Background(), sbk, b, func(_ context.Context, id string) error {
		pinned = append(pinned, id)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ImportedAddon{}
	for _, a := range res {
		got[a.ID] = a
	}
	for id, want := range map[string]struct {
		imported, enabled bool
		disabled, err     string
	}{
		"fine":   {true, true, "", ""},
		"arm":    {true, false, "binary", ""},
		"rega":   {true, false, "rega", ""},
		"off":    {true, false, "backup", ""},
		"noprog": {true, false, "program", ""},
		"here":   {false, false, "", "exists"},
		"hack":   {false, false, "", "entry"},
	} {
		a := got[id]
		if a.Imported != want.imported || a.Enabled != want.enabled || a.Disabled != want.disabled || a.Error != want.err {
			t.Errorf("%s: %+v", id, a)
		}
		// what is on the disk agrees: enabled is the executable bit
		if want.imported && r.AddonEnabled(id) != (want.enabled || want.disabled == "program") {
			t.Errorf("%s: executable %v", id, r.AddonEnabled(id))
		}
	}
	if got["fine"].Name != "Fine Addon" || strings.Join(pinned, " ") != "arm fine noprog off rega" {
		t.Errorf("names and pins: %q %v", got["fine"].Name, pinned)
	}
	// the installed addon is untouched, the hack was not written
	if b, _ := os.ReadFile(r.join("/usr/local/addons/here/mine")); string(b) != "kept" {
		t.Error("the installed addon's directory changed")
	}
	if _, err := os.Lstat(r.join("/usr/local/addons/here/new")); err == nil {
		t.Error("the backup's files were written into an installed addon")
	}
	if _, err := os.Lstat(r.join("/usr/local/etc/config/rc.d/hack")); err == nil {
		t.Error("an rc.d link out of the addons' tree was written")
	}
	entries := ParseHMAddonsCfg(readFile(r.join("/usr/local/etc/config/hm_addons.cfg")))
	if entries["fine"].Name != "Fine Addon" || entries["here"].Name != "Here" || len(entries) != 2 {
		t.Errorf("hm_addons.cfg: %+v", entries)
	}
	if s := readFile(r.join("/usr/local/etc/config/addons/fine/settings")); s != "a=1" {
		t.Errorf("the settings: %q", s)
	}
	// a second import of the same backup changes nothing: everything is here now
	res, err = r.ImportAddons(context.Background(), sbk, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res {
		if a.Imported || (a.Error != "exists" && a.ID != "hack") {
			t.Errorf("second import, %s: %+v", a.ID, a)
		}
	}
	if !r.AddonEnabled("fine") {
		t.Error("the second import disabled an addon")
	}
}
