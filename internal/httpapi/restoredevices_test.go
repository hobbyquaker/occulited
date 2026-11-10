package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/radio"
	"github.com/hobbyquaker/occulited/internal/system"
)

// writeSBK puts a made-up CCU backup under the rig's /usr/local/tmp as /restore/check would.
func writeSBK(t *testing.T, root system.Root, name string, files map[string]string) {
	t.Helper()
	var inner bytes.Buffer
	gz := gzip.NewWriter(&inner)
	tw := tar.NewWriter(gz)
	for n, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	var outer bytes.Buffer
	ow := tar.NewWriter(&outer)
	for n, body := range map[string]string{"usr_local.tar.gz": inner.String(), "signature": "sig", "key_index": "0\n", "firmware_version": "VERSION=3.89.11\n"} {
		_ = ow.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = ow.Write([]byte(body))
	}
	_ = ow.Close()
	dir := filepath.Join(string(root), system.BackupDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), outer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

var ccuBackup = map[string]string{
	"usr/local/etc/config/ids":                                     "BidCoS-Address=0xFF1234\nSerialNumber=1709ADFA00\n",
	"usr/local/etc/config/rfd/JEQ9000001.dev":                      "device",
	"usr/local/etc/config/crRFD/data/3014F711A0001F0000000A03.ap":  "ap",
	"usr/local/etc/config/crRFD/data/3014F711A000010000000A10.dev": "hmip",
	"usr/local/etc/config/hmip_address.conf":                       "Adapter.1.Address=BC0A08\n",
	"usr/local/etc/config/netconfig":                               "HOSTNAME=old\n",
}

// liteOf is files as an openccu-lite backup holds them: with occulited's accounts, which is what
// tells it from an (Open)CCU backup (occulited task 142).
func liteOf(files map[string]string) map[string]string {
	out := map[string]string{"usr/local/etc/occulite/users.json": `{"users":[{"name":"admin"}],"tokens":[{"name":"t"}]}`}
	for k, v := range files {
		out[k] = v
	}
	return out
}

// liteSBK is a minimal openccu-lite backup's bytes, for the tests that only need a file the
// restore accepts.
func liteSBK(t *testing.T) []byte {
	t.Helper()
	root := system.Root(t.TempDir())
	writeSBK(t, root, "restore-x.sbk", liteOf(nil))
	b, err := os.ReadFile(filepath.Join(string(root), system.BackupDir, "restore-x.sbk"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRestoreDevices: the view of a checked backup and this system, the refusals, the import with
// the reboot.
func TestRestoreDevices(t *testing.T) {
	rig := newPowerRig(t, true)
	root := string(rig.root)
	_ = os.MkdirAll(root+"/etc/config", 0o755)
	_ = os.WriteFile(root+"/etc/config/ids", []byte("BidCoS-Address=0xFF9999\nSerialNumber=0000000A01\n"), 0o644)
	writeSBK(t, rig.root, "restore-ccu.sbk", ccuBackup)
	writeSBK(t, rig.root, "restore-empty.sbk", map[string]string{"usr/local/etc/config/netconfig": "x"})
	old := system.PairedDevicesTimeout
	system.PairedDevicesTimeout = 2e9
	t.Cleanup(func() { system.PairedDevicesTimeout = old })
	rfd := listDevicesStub(t, "HM-RCV-50 BidCoS-RF", "HM-CC-TC")
	hmipFree := listDevicesStub(t, "HmIP-RFUSB")
	paired := true
	// this system's modules (task 275): HmIP-RF on an RPI-RF-MOD whose SGTIN is not the backup's
	plan := radio.Plan{HmIP: &radio.Role{Hardware: "RPI-RF-MOD", Serial: "0000000A01", SGTIN: "3014F711A000040000000A01"}, HmRF: &radio.Role{Hardware: "RPI-RF-MOD", Serial: "0000000A01"}}
	record := &system.ImportRecord{Path: filepath.Join(root, "state/devices-import.json"), Root: rig.root, Plan: func() (radio.Plan, bool) { return plan, true }, Journal: func(context.Context, time.Time) []string { return nil }}
	// task 281: the names of the same file are imported before the devices; the fake records the
	// order and the path, and fails when told to
	var namesCalls []string
	namesImport := func(path string) NamesImportResult {
		namesCalls = append(namesCalls, path)
		if _, err := os.Stat(path); err != nil {
			return NamesImportResult{Error: "the file is gone: " + err.Error()}
		}
		return NamesImportResult{OK: true, Objects: 109, Rooms: 11, Functions: 10, Changed: true}
	}
	api := &SystemAPI{Root: rig.root, Manager: rig.m, Power: rig.power, ImportRecord: record, NamesImport: namesImport, RadioInterfaces: func() []interfaces.Interface {
		list := []struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(hmipFree.URL, "http://")}}
		if paired {
			list = append(list, struct{ Name, URL string }{"BidCos-RF", "xmlrpc://" + strings.TrimPrefix(rfd.URL, "http://")})
		}
		return interfaces.FromList(list)
	}}
	mux := http.NewServeMux()
	api.Register(mux)
	rig.srv.Close()
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: rig.role, Scopes: auth.RoleScopes(rig.role)})))
	}))
	t.Cleanup(rig.srv.Close)

	// the view: the backup's counts and this system's pairing (one BidCos device here)
	st, out := rig.get(t, "/restore/devices?file=restore-ccu.sbk")
	if st != 200 {
		t.Fatalf("view: %d %v", st, out)
	}
	b := out["backup"].(map[string]any)
	if b["bidcos_rf"].(map[string]any)["devices"] != 1.0 || b["bidcos_rf"].(map[string]any)["address"] != "0xFF1234" || b["hmip"].(map[string]any)["devices"] != 1.0 || b["hmip"].(map[string]any)["identity_sgtin"] != "3014F711A0001F0000000A03" {
		t.Errorf("backup: %v", b)
	}
	tg := out["target"].(map[string]any)
	if tg["devices"] != 1.0 || tg["importable"] != false || tg["paired"].(map[string]any)["BidCos-RF"].(map[string]any)["devices"] != 1.0 {
		t.Errorf("target: %v", tg)
	}
	// task 275: this system's modules, and that the backup's identity belongs to another one;
	// task 278: the backup's key store is the factory one here
	if tg["hmip_module"].(map[string]any)["sgtin"] != "3014F711A000040000000A01" || tg["bidcos_module"].(map[string]any)["hardware"] != "RPI-RF-MOD" || out["module_changed"] != true || out["non_default_key"] != false {
		t.Errorf("modules: %v %v %v", tg["hmip_module"], out["module_changed"], out["non_default_key"])
	}
	if st, out := rig.get(t, "/radio/import"); st != 200 || out["imported"] != false {
		t.Errorf("no import yet: %d %v", st, out)
	}
	if st, _, _ := rig.do(t, "POST", "/radio/import/retry", `{}`); st != 404 {
		t.Errorf("retry without a record: %d", st)
	}
	// openccu-lite task 317 (D-120): the import writes the backup's HmIP identity - not without confirm
	if st, e, _ := rig.do(t, "POST", "/restore/import-devices", `{"file":"restore-ccu.sbk"}`); st != 400 || e.Error != "confirm" {
		t.Errorf("no confirm: %d %+v", st, e)
	}
	// the refusals: a paired system, a bad name, a missing upload, nothing to import
	if st, e, _ := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-ccu.sbk"}`); st != 409 || e.Error != "paired" {
		t.Errorf("paired: %d %+v", st, e)
	}
	if st, e, _ := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"../etc/passwd"}`); st != 400 || e.Error != "invalid" {
		t.Errorf("bad name: %d %+v", st, e)
	}
	if st, _, _ := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-nope.sbk"}`); st != 404 {
		t.Errorf("missing: %d", st)
	}
	if st, _ := rig.get(t, "/restore/devices?file=x.sbk"); st != 400 {
		t.Errorf("view bad name: %d", st)
	}
	paired = false
	// the daemons down but their device files on the userfs (task 275's lab run): still paired
	_ = os.MkdirAll(root+"/etc/config/rfd", 0o755)
	_ = os.WriteFile(root+"/etc/config/rfd/KEQ0000001.dev", []byte("x"), 0o644)
	_ = os.MkdirAll(root+"/etc/config/crRFD/data", 0o755)
	_ = os.WriteFile(root+"/etc/config/crRFD/data/3014F711A000010000000A55.dev", []byte("x"), 0o600)
	_ = os.WriteFile(root+"/etc/config/crRFD/data/linkData.conf", []byte("x"), 0o600)
	st, out = rig.get(t, "/restore/devices?file=restore-ccu.sbk")
	tg = out["target"].(map[string]any)
	if st != 200 || tg["importable"] != false || tg["devices"] != 2.0 || tg["paired"].(map[string]any)["BidCos-RF"].(map[string]any)["devices"] != 1.0 || tg["paired"].(map[string]any)["HmIP-RF"].(map[string]any)["devices"] != 1.0 {
		t.Errorf("paired from the files: %d %v", st, tg)
	}
	if st, e, _ := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-ccu.sbk"}`); st != 409 || e.Error != "paired" {
		t.Errorf("paired from the files: %d %+v", st, e)
	}
	_ = os.Remove(root + "/etc/config/rfd/KEQ0000001.dev")
	_ = os.Remove(root + "/etc/config/crRFD/data/3014F711A000010000000A55.dev")
	if st, e, _ := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-empty.sbk"}`); st != 422 || e.Error != "nothing_to_import" {
		t.Errorf("empty: %d %+v", st, e)
	}
	if len(namesCalls) != 0 {
		t.Errorf("the names were imported for a refused import: %v", namesCalls)
	}
	// a user may not
	rig.role = auth.RoleUser
	if st, _ := rig.get(t, "/restore/devices?file=restore-ccu.sbk"); st != 403 {
		t.Errorf("a user's view: %d", st)
	}
	rig.role = auth.RoleAdmin
	// the import onto the free system: the files land, this system's own go aside, the reboot
	st, e, out := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-ccu.sbk"}`)
	if st != 200 || out["ok"] != true || out["rebooting"] != true {
		t.Fatalf("import: %d %+v %v", st, e, out)
	}
	imp := out["imported"].(map[string]any)
	if imp["non_default_key"] != false || imp["target_key_replaced"] != false || len(imp["written"].([]any)) != 5 || !strings.HasPrefix(imp["aside"].(string), "/etc/config/.import-devices-aside/") {
		t.Errorf("imported: %v", imp)
	}
	// task 281: the names of the same file went first, from the checked upload, and are reported
	names := out["names"].(map[string]any)
	if names["ok"] != true || names["objects"] != 109.0 || names["rooms"] != 11.0 || names["functions"] != 10.0 {
		t.Errorf("names: %v", names)
	}
	if len(namesCalls) != 1 || !strings.HasSuffix(namesCalls[0], "/usr/local/tmp/restore-ccu.sbk") {
		t.Errorf("names calls: %v", namesCalls)
	}
	// the record (task 275): the identity came from the backup's module onto this one
	recOut := out["record"].(map[string]any)["hmip"].(map[string]any)
	if recOut["module_changed"] != true || recOut["from_sgtin"] != "3014F711A0001F0000000A03" || recOut["to_sgtin"] != "3014F711A000040000000A01" {
		t.Errorf("record: %v", recOut)
	}
	if record.Read() == nil {
		t.Fatal("the record was not written")
	}
	if got, _ := os.ReadFile(root + "/etc/config/ids"); string(got) != "BidCoS-Address=0xFF1234\nSerialNumber=1709ADFA00\n" {
		t.Errorf("ids: %q", got)
	}
	if got, _ := os.ReadFile(root + "/etc/config/rfd/JEQ9000001.dev"); string(got) != "device" {
		t.Errorf("device file: %q", got)
	}
	if _, err := os.Stat(root + "/etc/config/netconfig"); !errors.Is(err, os.ErrNotExist) {
		t.Error("netconfig came out of the backup")
	}
	if got, _ := os.ReadFile(root + imp["aside"].(string) + "/ids"); string(got) != "BidCoS-Address=0xFF9999\nSerialNumber=0000000A01\n" {
		t.Errorf("the old ids aside: %q", got)
	}
	if rig.m.Reboots() == 0 {
		t.Error("no reboot")
	}
	// after the reboot: the view says the move is pending (the previous module's identity file
	// is what the import wrote; hmipserver has not replaced it), the warning says so, the retry
	// has no local key service here (501), and the dismissal removes the record
	st, out = rig.get(t, "/radio/import")
	if st != 200 || out["imported"] != true || out["outcome"].(map[string]any)["hmip"].(map[string]any)["state"] != system.ImportPending {
		t.Fatalf("import view: %d %v", st, out)
	}
	if st, _, _ := rig.do(t, "POST", "/radio/import/retry", `{}`); st != 501 {
		t.Errorf("retry without the local key service: %d", st)
	}
	if ws, _ := api.devicesImportWarning(context.Background()); len(ws) != 1 || ws[0].ID != "devices-import" || ws[0].Variant != system.ImportPending || ws[0].Params["from"] != "3014F711A0001F0000000A03" {
		t.Errorf("warning: %+v", ws)
	}
	// hmipserver took it over: this module's identity file there, the previous one's gone
	_ = os.Remove(root + "/etc/config/crRFD/data/3014F711A0001F0000000A03.ap")
	_ = os.WriteFile(root+"/etc/config/crRFD/data/3014F711A000040000000A01.ap", []byte("new"), 0o600)
	if _, out := rig.get(t, "/radio/import"); out["outcome"].(map[string]any)["hmip"].(map[string]any)["state"] != system.ImportDone {
		t.Errorf("done: %v", out["outcome"])
	}
	if ws, _ := api.devicesImportWarning(context.Background()); len(ws) != 0 {
		t.Errorf("warning after the move: %+v", ws)
	}
	rig.role = auth.RoleUser
	if st, _, _ := rig.do(t, "DELETE", "/radio/import", ""); st != 403 {
		t.Errorf("a user's dismissal: %d", st)
	}
	rig.role = auth.RoleAdmin
	if st, _, _ := rig.do(t, "DELETE", "/radio/import", ""); st != 204 || record.Read() != nil {
		t.Errorf("dismiss: %d", st)
	}
}

// task 278 (option B): a backup with a non-default key store onto a system that has a key store of
// its own - the import asks for the word to replace it and takes no passphrase; the result says the
// key came along
func TestRestoreDevicesKeyReplace(t *testing.T) {
	rig := newPowerRig(t, true)
	root := string(rig.root)
	_ = os.MkdirAll(root+"/etc/config", 0o755)
	_ = os.WriteFile(root+"/etc/config/keys", []byte("the target's own key store"), 0o600)
	files := map[string]string{}
	for k, v := range ccuBackup {
		files[k] = v
	}
	files["usr/local/etc/config/keys"] = "the backup's key store"
	files["usr/local/etc/config/crypttool.cfg"] = "1 00000000000000000000000000000000\n"
	writeSBK(t, rig.root, "restore-key.sbk", files)
	old := system.PairedDevicesTimeout
	system.PairedDevicesTimeout = 2e9
	t.Cleanup(func() { system.PairedDevicesTimeout = old })
	hmipFree := listDevicesStub(t, "HmIP-RFUSB")
	record := &system.ImportRecord{Path: filepath.Join(root, "state/devices-import.json"), Root: rig.root, Plan: func() (radio.Plan, bool) { return radio.Plan{}, false }}
	// task 281: a backup without a ReGa database - the names fail, the devices come all the same
	namesImport := func(string) NamesImportResult { return NamesImportResult{Error: "no ReGa database in the backup"} }
	api := &SystemAPI{Root: rig.root, Manager: rig.m, Power: rig.power, ImportRecord: record, NamesImport: namesImport, RadioInterfaces: func() []interfaces.Interface {
		return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(hmipFree.URL, "http://")}})
	}}
	mux := http.NewServeMux()
	api.Register(mux)
	rig.srv.Close()
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: rig.role, Scopes: auth.RoleScopes(rig.role)})))
	}))
	t.Cleanup(rig.srv.Close)
	st, out := rig.get(t, "/restore/devices?file=restore-key.sbk")
	if st != 200 || out["non_default_key"] != true || out["target"].(map[string]any)["user_key"] != true || out["module_changed"] != false {
		t.Fatalf("view: %d %v", st, out)
	}
	// no passphrase: the word to replace this system's key store is what is needed
	if st, e, _ := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-key.sbk"}`); st != 422 || e.Error != "key_replace" {
		t.Fatalf("without replace_key: %d %+v", st, e)
	}
	if b, _ := os.ReadFile(root + "/etc/config/keys"); string(b) != "the target's own key store" {
		t.Fatal("the key store moved on a refusal")
	}
	st, e, out := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-key.sbk","replace_key":true}`)
	if st != 200 {
		t.Fatalf("import: %d %+v", st, e)
	}
	imp := out["imported"].(map[string]any)
	if imp["non_default_key"] != true || imp["target_key_replaced"] != true {
		t.Errorf("imported: %v", imp)
	}
	if names := out["names"].(map[string]any); names["ok"] != false || names["error"] != "no ReGa database in the backup" || out["rebooting"] != true {
		t.Errorf("a names failure stopped the import or is not reported: %v %v", names, out["rebooting"])
	}
	if b, _ := os.ReadFile(root + "/etc/config/keys"); string(b) != "the backup's key store" {
		t.Errorf("keys: %q", b)
	}
	if b, _ := os.ReadFile(root + imp["aside"].(string) + "/keys"); string(b) != "the target's own key store" {
		t.Errorf("the target's key store aside: %q", b)
	}
	rec := record.Read()
	if rec == nil || !rec.BidCosRF.NonDefaultKey || !rec.BidCosRF.TargetKeyReplaced || rec.HmIP.ModuleChanged {
		t.Errorf("record: %+v", rec)
	}
}

// openccu-lite B-289: a backup whose HmIP identity would move onto this system's module, whose
// application firmware is below 2.8.0, with local key mode off: the view names the refusal, the
// device import and the restore are refused (422 hmip-firmware) before anything happens; a backup
// in local key mode, or a module on 2.8.0 or later, goes ahead.
func TestRestoreHmIPFirmwareRefused(t *testing.T) {
	rig := newPowerRig(t, true)
	root := string(rig.root)
	writeSBK(t, rig.root, "restore-ccu.sbk", ccuBackup)
	local := map[string]string{}
	for k, v := range ccuBackup {
		local[k] = v
	}
	local["usr/local/etc/config/crRFD/hmip_user.conf"] = "KeyServer.Mode=LOCAL\nNetwork.Key=00112233445566778899AABBCCDDEEFF\n"
	writeSBK(t, rig.root, "restore-local.sbk", local)
	plan := radio.Plan{HmIP: &radio.Role{Hardware: "HMIP-RFUSB", Serial: "0000000A01", SGTIN: "3014F711A000040000000A01", Version: "1.8.3"}}
	record := &system.ImportRecord{Path: filepath.Join(root, "state/devices-import.json"), Root: rig.root, Plan: func() (radio.Plan, bool) { return plan, true }, Journal: func(context.Context, time.Time) []string { return nil }}
	api := &SystemAPI{Root: rig.root, Manager: rig.m, Power: rig.power, ImportRecord: record}
	mux := http.NewServeMux()
	api.Register(mux)
	rig.srv.Close()
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: rig.role, Scopes: auth.RoleScopes(rig.role)})))
	}))
	t.Cleanup(rig.srv.Close)

	st, out := rig.get(t, "/restore/devices?file=restore-ccu.sbk")
	fw, _ := out["hmip_firmware"].(map[string]any)
	if st != 200 || out["module_changed"] != true || fw == nil || fw["module"] != "3014F711A000040000000A01" || fw["version"] != "1.8.3" || fw["minimum"] != "2.8.0" {
		t.Fatalf("view: %d %v", st, out)
	}
	if v := out["target"].(map[string]any)["hmip_module"].(map[string]any)["version"]; v != "1.8.3" {
		t.Errorf("the module's version: %v", v)
	}
	st, e, _ := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-ccu.sbk"}`)
	if st != 422 || e.Error != "hmip-firmware" || e.Detail["version"] != "1.8.3" || e.Detail["minimum"] != "2.8.0" || !strings.Contains(e.Message, "update the module's firmware") {
		t.Errorf("import: %d %+v", st, e)
	}
	// the restore: an (Open)CCU backup is refused as such (D-125); an openccu-lite backup with the
	// same radio side is refused for the module's firmware
	if st, e, _ := rig.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-ccu.sbk"}`); st != 422 || e.Error != "not_openccu_lite" {
		t.Errorf("restore of a CCU backup: %d %+v", st, e)
	}
	liteFiles := liteOf(nil)
	for k, v := range ccuBackup {
		liteFiles[k] = v
	}
	liteFiles["usr/local/etc/config/crRFD/data/3014F711A0001F0000000A03.ap"] = "ap"
	writeSBK(t, rig.root, "restore-lite.sbk", liteFiles)
	if st, e, _ := rig.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-lite.sbk"}`); st != 422 || e.Error != "hmip-firmware" || e.Detail["module"] != "3014F711A000040000000A01" {
		t.Errorf("restore: %d %+v", st, e)
	}
	if record.Read() != nil || rig.m.Reboots() != 0 {
		t.Errorf("a refused import or restore did something: %v %v", record.Read(), rig.m.Reboots())
	}
	// local key mode in the backup: offline, no refusal
	if st, out := rig.get(t, "/restore/devices?file=restore-local.sbk"); st != 200 || out["hmip_firmware"] != nil || out["module_changed"] != true {
		t.Errorf("local key mode: %d %v", st, out)
	}
	// a module that can take the key
	plan.HmIP.Version = "4.4.18"
	if st, out := rig.get(t, "/restore/devices?file=restore-ccu.sbk"); st != 200 || out["hmip_firmware"] != nil {
		t.Errorf("4.4.18: %d %v", st, out)
	}
	if refused := api.restoreHmIPRefusal(filepath.Join(root, system.BackupDir, "restore-ccu.sbk")); refused != nil {
		t.Errorf("restore at 4.4.18: %+v", refused)
	}
	// the identity of this module itself: no move, no refusal
	plan.HmIP.Version, plan.HmIP.SGTIN = "1.8.3", "3014F711A0001F0000000A03"
	if st, out := rig.get(t, "/restore/devices?file=restore-ccu.sbk"); st != 200 || out["hmip_firmware"] != nil || out["module_changed"] != false {
		t.Errorf("same module: %d %v", st, out)
	}
}
