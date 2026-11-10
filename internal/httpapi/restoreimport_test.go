package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
)

// occulited task 142 (D-125): the import of an (Open)CCU backup with its selectable groups, and
// the restore that takes an openccu-lite backup alone.

// movingBackup is an OpenCCU's backup as a user who moves brings it.
func movingBackup() map[string]string {
	out := map[string]string{}
	for k, v := range ccuBackup {
		out[k] = v
	}
	for k, v := range map[string]string{
		"usr/local/etc/config/homematic.regadom":  "<dom/>",
		"usr/local/etc/config/netconfig":          "HOSTNAME=old-ccu\nMODE=DHCP\n",
		"usr/local/etc/config/hb_rf_eth":          "192.168.1.50\n",
		"usr/local/etc/config/syslog":             "LOGHOST=loghost.lan\n",
		"usr/local/etc/config/firewall.conf":      "MODE = RESTRICTIVE\n[SERVICE XMLRPC]\nId = XMLRPC\nPorts = 2000 2001 2010 9292 42000 42001 42010 49292\nAccess = full\n",
		"usr/local/etc/config/hm_addons.cfg":      "wd {CONFIG_URL /addons/wd/ CONFIG_NAME {HM Watchdog} CONFIG_DESCRIPTION {de x en y}}\n",
		"usr/local/etc/config/rc.d/wd":            "#!/bin/sh\n",
		"usr/local/addons/wd/run.sh":              "#!/bin/sh\n",
		"usr/local/etc/config/addons/wd/settings": "a=1\n",
	} {
		out[k] = v
	}
	return out
}

func importRig(t *testing.T) (*powerRig, *SystemAPI) {
	t.Helper()
	old := system.Priv
	system.Priv = priv.Local{}
	t.Cleanup(func() { system.Priv = old; system.ForgetAddonScans("") })
	rig := newPowerRig(t, true)
	root := string(rig.root)
	for _, d := range []string{"/etc/config", "/usr/local/etc/config/rc.d", "/usr/local/addons", "/run/occulite/radio"} {
		_ = os.MkdirAll(root+d, 0o755)
	}
	oldTimeout := system.PairedDevicesTimeout
	system.PairedDevicesTimeout = 2e9
	t.Cleanup(func() { system.PairedDevicesTimeout = oldTimeout })
	free := listDevicesStub(t, "HmIP-RFUSB")
	record := &system.ImportRecord{Path: filepath.Join(root, "state/devices-import.json"), Root: rig.root}
	api := &SystemAPI{Root: rig.root, Manager: rig.m, Power: rig.power, ImportRecord: record,
		NamesImport: func(string) NamesImportResult { return NamesImportResult{OK: true, Objects: 3} },
		RadioInterfaces: func() []interfaces.Interface {
			return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(free.URL, "http://")}})
		}}
	mux := http.NewServeMux()
	api.Register(mux)
	rig.srv.Close()
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: rig.role, Scopes: auth.RoleScopes(rig.role)})))
	}))
	t.Cleanup(rig.srv.Close)
	return rig, api
}

func TestRestoreImportGroups(t *testing.T) {
	rig, _ := importRig(t)
	root := string(rig.root)
	writeSBK(t, rig.root, "restore-move.sbk", movingBackup())
	writeSBK(t, rig.root, "restore-lite.sbk", liteOf(movingBackup()))
	if err := rig.root.WriteRules(firewall.Reconcile(firewall.Default(), firewall.AlwaysOwners())); err != nil {
		t.Fatal(err)
	}

	// the view: the kind, the groups with what they are here and their defaults, what stays behind
	st, out := rig.get(t, "/restore/devices?file=restore-move.sbk")
	if st != 200 || out["kind"] != "ccu" {
		t.Fatalf("view: %d %v", st, out)
	}
	groups := map[string]map[string]any{}
	var order []string
	for _, g := range out["groups"].([]any) {
		m := g.(map[string]any)
		groups[m["id"].(string)] = m
		order = append(order, m["id"].(string))
	}
	if strings.Join(order, " ") != "addons hb_rf_eth hostname loghost classic_rpc" {
		t.Fatalf("groups: %v", order)
	}
	if groups["classic_rpc"]["default"] != false || groups["addons"]["default"] != true || groups["hostname"]["value"] != "old-ccu" || groups["hb_rf_eth"]["value"] != "192.168.1.50" {
		t.Errorf("groups: %v", groups)
	}
	if a := groups["addons"]["addons"].([]any)[0].(map[string]any); a["id"] != "wd" || a["name"] != "HM Watchdog" || a["program"] != true {
		t.Errorf("the addon: %v", a)
	}
	left, _ := out["left_behind"].([]any)
	if len(left) < 4 {
		t.Errorf("left behind: %v", left)
	}
	// an openccu-lite backup: no groups
	if st, out := rig.get(t, "/restore/devices?file=restore-lite.sbk"); st != 200 || out["kind"] != "openccu-lite" || len(out["groups"].([]any)) != 0 {
		t.Errorf("view of an openccu-lite backup: %d %v %v", st, out["kind"], out["groups"])
	}

	// refusals before anything is done: a group that is none, one of an openccu-lite backup
	for name, body := range map[string]string{
		"an unknown group":           `{"confirm":true,"file":"restore-move.sbk","groups":["users"]}`,
		"a group named twice":        `{"confirm":true,"file":"restore-move.sbk","groups":["hostname","hostname"]}`,
		"groups of an openccu-lite":  `{"confirm":true,"file":"restore-lite.sbk","groups":["addons"]}`,
		"classic RPC of an openccu-": `{"confirm":true,"file":"restore-lite.sbk","groups":["classic_rpc"]}`,
	} {
		if st, e, _ := rig.do(t, "POST", "/restore/import-devices", body); st != 422 || e.Error != "group_unavailable" {
			t.Errorf("%s: %d %+v", name, st, e)
		}
	}
	if rig.m.Reboots() != 0 {
		t.Fatal("a refused import rebooted")
	}
	if _, err := os.Stat(root + "/etc/config/rfd/JEQ9000001.dev"); err == nil {
		t.Fatal("a refused import wrote the radio files")
	}

	// the restore of an (Open)CCU backup is refused by the API as well
	if st, e, _ := rig.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-move.sbk"}`); st != 422 || e.Error != "not_openccu_lite" || e.Detail["system"] != "CCU3" || !strings.Contains(e.Message, "imported") {
		t.Errorf("restore of a CCU backup: %d %+v", st, e)
	}
	_ = os.WriteFile(filepath.Join(root, system.BackupDir, "restore-junk.sbk"), []byte("junk"), 0o600)
	if st, e, _ := rig.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-junk.sbk"}`); st != 422 || e.Error != "not_openccu_lite" {
		t.Errorf("restore of a file that is no backup: %d %+v", st, e)
	}

	// the import with a selection: addons and LOGHOST and classic RPC, not the host name, not the board
	st, e, out := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-move.sbk","groups":["addons","loghost","classic_rpc"]}`)
	if st != 200 || out["rebooting"] != true {
		t.Fatalf("import: %d %+v %v", st, e, out)
	}
	res := out["groups"].(map[string]any)
	if len(res) != 3 {
		t.Fatalf("group results: %v", res)
	}
	for id, r := range res {
		if r.(map[string]any)["ok"] != true {
			t.Errorf("%s: %v", id, r)
		}
	}
	ad := res["addons"].(map[string]any)["addons"].([]any)[0].(map[string]any)
	// the fixture's script has no executable bit: it comes disabled, as it was
	if ad["id"] != "wd" || ad["imported"] != true || ad["enabled"] != false || ad["disabled"] != "backup" {
		t.Errorf("the addon: %v", ad)
	}
	if res["classic_rpc"].(map[string]any)["rules"].(float64) != 8 || res["loghost"].(map[string]any)["value"] != "loghost.lan" {
		t.Errorf("results: %v", res)
	}
	read := func(p string) string { b, _ := os.ReadFile(root + p); return string(b) }
	if !strings.Contains(read("/etc/config/syslog"), "LOGHOST=loghost.lan") {
		t.Errorf("syslog: %q", read("/etc/config/syslog"))
	}
	if read("/usr/local/addons/wd/run.sh") == "" || read("/usr/local/etc/config/addons/wd/settings") != "a=1\n" || !strings.Contains(read("/usr/local/etc/config/hm_addons.cfg"), "HM Watchdog") {
		t.Error("the addon's files or its hm_addons.cfg entry are missing")
	}
	if cr := rig.root.ReadClassicRPC(); !cr.Plain || !cr.TLS {
		t.Errorf("classic RPC: %+v", cr)
	}
	// not chosen: the host name and the board's address stay this system's
	if strings.Contains(read("/etc/config/netconfig"), "old-ccu") || read("/etc/config/hb_rf_eth") != "" {
		t.Errorf("a group that was not chosen came along: %q %q", read("/etc/config/netconfig"), read("/etc/config/hb_rf_eth"))
	}
	if read("/etc/config/rfd/JEQ9000001.dev") == "" || rig.m.Reboots() != 1 {
		t.Errorf("the devices or the reboot: %d reboots", rig.m.Reboots())
	}
}

// TestRestoreImportSettingsAlone: the two settings the first test left out, and an import without
// groups, which is the devices alone as before task 142.
func TestRestoreImportSettingsAlone(t *testing.T) {
	rig, _ := importRig(t)
	root := string(rig.root)
	writeSBK(t, rig.root, "restore-move.sbk", movingBackup())
	st, _, out := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-move.sbk","groups":["hostname","hb_rf_eth"]}`)
	if st != 200 {
		t.Fatalf("import: %d %v", st, out)
	}
	res := out["groups"].(map[string]any)
	if res["hostname"].(map[string]any)["ok"] != true || res["hb_rf_eth"].(map[string]any)["value"] != "192.168.1.50" {
		t.Errorf("results: %v", res)
	}
	read := func(p string) string { b, _ := os.ReadFile(root + p); return string(b) }
	if !strings.Contains(read("/etc/config/netconfig"), "HOSTNAME=old-ccu") || read("/etc/config/hb_rf_eth") != "192.168.1.50\n" {
		t.Errorf("netconfig %q, hb_rf_eth %q", read("/etc/config/netconfig"), read("/etc/config/hb_rf_eth"))
	}
	if _, err := os.Stat(root + "/usr/local/addons/wd"); err == nil {
		t.Error("the addons came along although they were not chosen")
	}

	rig2, _ := importRig(t)
	writeSBK(t, rig2.root, "restore-move.sbk", movingBackup())
	st, _, out = rig2.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-move.sbk"}`)
	if st != 200 || len(out["groups"].(map[string]any)) != 0 {
		t.Fatalf("import without groups: %d %v", st, out["groups"])
	}
	for _, p := range []string{"/usr/local/addons/wd", "/etc/config/hb_rf_eth", "/etc/config/classic-rpc-plain"} {
		if _, err := os.Stat(string(rig2.root) + p); err == nil {
			t.Errorf("%s exists after an import without groups", p)
		}
	}
}

// TestRestoreCheckReportsTheBackup: the check answers what the file is and what this system is.
func TestRestoreCheckReportsTheBackup(t *testing.T) {
	g := newCryptRig(t)
	scratch := system.Root(t.TempDir())
	writeSBK(t, scratch, "restore-a.sbk", movingBackup())
	body, _ := os.ReadFile(filepath.Join(string(scratch), system.BackupDir, "restore-a.sbk"))
	st, out := g.upload(t, "old-ccu.sbk", body)
	if st != 200 {
		t.Fatalf("check: %d %v", st, out)
	}
	b, _ := out["backup"].(map[string]any)
	if b == nil || b["kind"] != "ccu" || b["system"] != "CCU3" || b["version"] != "3.89.11" || b["hostname"] != "old-ccu" || b["rega"] != true {
		t.Fatalf("backup: %v", b)
	}
	if out["check"].(map[string]any)["has_rega"] != true {
		t.Errorf("has_rega is no longer set: %v", out["check"])
	}
	if s, _ := out["system"].(map[string]any); s == nil || s["name"] != "openccu-lite" {
		t.Errorf("this system: %v", out["system"])
	}
	writeSBK(t, scratch, "restore-b.sbk", liteOf(nil))
	body, _ = os.ReadFile(filepath.Join(string(scratch), system.BackupDir, "restore-b.sbk"))
	st, out = g.upload(t, "mine.sbk", body)
	if b, _ := out["backup"].(map[string]any); st != 200 || b["kind"] != "openccu-lite" || b["users"].(float64) != 1 || b["tokens"].(float64) != 1 {
		t.Errorf("an openccu-lite backup: %d %v", st, out["backup"])
	}
	// what is no archive has no report; the check's own verdict stands
	st, out = g.upload(t, "junk.sbk", []byte("junk"))
	if st != 200 || out["backup"] != nil {
		t.Errorf("junk: %d %v", st, out["backup"])
	}
}
