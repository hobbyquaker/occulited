package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// task 19: the password-login switch defaults to on, holds only in mode oidc, and a file of an
// earlier version with the group mapping loads with those keys reported and dropped at the next save.
func TestPasswordLoginSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "occulited.json")
	c, err := Load(path)
	if err != nil || !c.Auth.OIDC.PasswordLogin || !c.Auth.PasswordLoginOn() {
		t.Fatalf("no file: %v %+v", err, c.Auth)
	}
	old := `{"auth": {"mode": "oidc", "oidc": {"enabled": true, "issuer": "https://auth.example.org/o/lite/", "client_id": "x", "groups_claim": "groups", "admin_groups": ["ccu-admins"]}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Auth.OIDC.PasswordLogin || !c.Auth.PasswordLoginOn() {
		t.Errorf("a file without the key: password login must be on: %+v", c.Auth)
	}
	if strings.Join(c.Stale, ",") != "auth.oidc.groups_claim,auth.oidc.admin_groups" {
		t.Errorf("stale keys: %v", c.Stale)
	}
	c.Auth.OIDC.PasswordLogin = false
	if c.Auth.PasswordLoginOn() {
		t.Error("off in mode oidc must hold")
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "admin_groups") || strings.Contains(string(b), "groups_claim") {
		t.Errorf("the stale keys were written back:\n%s", b)
	}
	if !strings.Contains(string(b), `"password_login": false`) {
		t.Errorf("the switch was not written:\n%s", b)
	}
	c, err = Load(path)
	if err != nil || c.Auth.OIDC.PasswordLogin || c.Auth.PasswordLoginOn() || len(c.Stale) != 0 {
		t.Errorf("after save: %v %+v stale=%v", err, c.Auth, c.Stale)
	}
	// the switch holds in mode oidc only: local and off log in with a password (or not at all)
	for _, mode := range []string{"local", "off", ""} {
		c.Auth.Mode, c.Auth.OIDC.Enabled = mode, false
		if !c.Auth.PasswordLoginOn() {
			t.Errorf("mode %q with the switch off: password login must be on", mode)
		}
	}
	// an enabled provider without a mode is mode oidc (EffectiveMode), so the switch holds there too
	c.Auth.Mode, c.Auth.OIDC.Enabled = "", true
	if c.Auth.PasswordLoginOn() {
		t.Error("enabled provider without a mode: the switch must hold")
	}
	// the console command: on always writes, off only in mode oidc
	c.Auth.Mode, c.Auth.OIDC.Enabled, c.Auth.OIDC.PasswordLogin = "oidc", true, false
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if c, err := SetPasswordLogin(path, true); err != nil || !c.Auth.OIDC.PasswordLogin {
		t.Errorf("console on: %v %+v", err, c.Auth.OIDC)
	}
	if c, _ := Load(path); !c.Auth.PasswordLoginOn() {
		t.Error("console on was not written")
	}
	if _, err := SetPasswordLogin(path, false); err != nil {
		t.Errorf("console off in mode oidc: %v", err)
	}
	if c, _ := Load(path); c.Auth.PasswordLoginOn() {
		t.Error("console off was not written")
	}
	c.Auth.Mode = "local"
	_ = Save(path, c)
	if _, err := SetPasswordLogin(path, false); err == nil {
		t.Error("console off in mode local must be refused")
	}
	if c, _ := Load(path); !c.Auth.PasswordLoginOn() {
		t.Error("the refused off was written")
	}
	// `"admin_groups": null`, as occulited wrote it for a while, is not stale
	if err := os.WriteFile(path, []byte(`{"auth": {"oidc": {"admin_groups": null, "groups_claim": ""}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(path); err != nil || len(c.Stale) != 0 {
		t.Errorf("null groups: %v %v", err, c.Stale)
	}
}

// D-119 and the publication on GitHub: a stored catalogue list that still names an old default -
// the openccu-lite-addons index or this repository's file on the private forge - follows the new
// default; anything else is the user's choice and stays, the bundled copy included.
func TestOldCatalogDefaultsFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "occulited.json")
	file := `{"catalog": {"enabled": true, "urls": [
		"https://git.example.org/hobbyquaker/openccu-lite-addons/raw/branch/master/index.json",
		"https://git.example.org/hobbyquaker/occulited/raw/branch/master/catalog/catalog.json",
		"https://git.example.org/hobbyquaker/occulited/raw/branch/main/catalog/catalog.json",
		"http://git.example.org/hobbyquaker/occulited/raw/branch/master/catalog/catalog.json",
		"https://raw.githubusercontent.com/hobbyquaker/occulited/raw/branch/master/catalog/catalog.json",
		"https://git.example.org/hobbyquaker/occulited/raw/branch/master/catalog/catalog.json?x=1",
		"file:///etc/occulite/catalog.json"]}}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		DefaultCatalogURL,
		DefaultCatalogURL,
		"https://git.example.org/hobbyquaker/occulited/raw/branch/main/catalog/catalog.json",
		"http://git.example.org/hobbyquaker/occulited/raw/branch/master/catalog/catalog.json",
		"https://raw.githubusercontent.com/hobbyquaker/occulited/raw/branch/master/catalog/catalog.json",
		"https://git.example.org/hobbyquaker/occulited/raw/branch/master/catalog/catalog.json?x=1",
		BundledCatalog,
	}
	if strings.Join(c.Catalog.URLs, "\n") != strings.Join(want, "\n") {
		t.Errorf("urls:\n%s", strings.Join(c.Catalog.URLs, "\n"))
	}
	if !strings.HasPrefix(DefaultCatalogURL, "https://raw.githubusercontent.com/hobbyquaker/occulited/") {
		t.Errorf("the default is not the published file: %s", DefaultCatalogURL)
	}
	if d := Default(); len(d.Catalog.URLs) != 2 || d.Catalog.URLs[0] != DefaultCatalogURL || d.Catalog.URLs[1] != BundledCatalog {
		t.Errorf("defaults: %v", d.Catalog.URLs)
	}
}

// task 258: the release feed moved from releases/latest (which never returns a prerelease) to the
// release list; a stored file with the old default follows, a feed of one's own stays.
func TestOldSystemUpdateFeedFollows(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ stored, want string }{
		{"https://api.github.com/repos/hobbyquaker/openccu-lite/releases/latest", DefaultSystemUpdateFeed},
		{"https://api.github.com/repos/hobbyquaker/openccu-lite/releases?per_page=20", DefaultSystemUpdateFeed},
		{"https://api.github.com/repos/someone/fork/releases/latest", "https://api.github.com/repos/someone/fork/releases/latest"},
		{"https://example.org/feed.json", "https://example.org/feed.json"},
	} {
		path := filepath.Join(dir, "occulited.json")
		if err := os.WriteFile(path, []byte(`{"system_update": {"enabled": true, "feed": "`+c.stored+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.SystemUpdate.Feed != c.want {
			t.Errorf("%s: %s", c.stored, got.SystemUpdate.Feed)
		}
	}
	if d := Default().SystemUpdate.Feed; !strings.HasPrefix(d, "https://api.github.com/repos/hobbyquaker/openccu-lite/releases?") {
		t.Errorf("default feed: %s", d)
	}
}

// openccu-lite B-241 (D-90): the outbound switches are off on a fresh system; a file that lacks
// them is settled once - on for a system whose setup is done (what it ran with before), off for
// a fresh one - and then carries all three explicitly, so a later start decides nothing again.
func TestOutboundSwitchesDefaultOffAndSettleOnce(t *testing.T) {
	d := Default()
	if d.Firmware.Enabled || d.SystemUpdate.Enabled || d.Catalog.DailyOn() {
		t.Fatalf("a fresh system calls nothing daily: %+v %+v %v", d.Firmware, d.SystemUpdate, d.Catalog.Daily)
	}
	if (CatalogConfig{}).DailyOn() {
		t.Fatal("catalog.daily absent must read as off")
	}
	dir := t.TempDir()

	// no file, setup not done: the fresh defaults, written explicitly
	path := filepath.Join(dir, "fresh.json")
	c, err := Load(path)
	if err != nil || strings.Join(c.OutboundUnset, ",") != "firmware.enabled,system_update.enabled,catalog.daily" {
		t.Fatalf("no file: %v unset=%v", err, c.OutboundUnset)
	}
	wrote, err := SettleOutbound(path, &c, false)
	if err != nil || len(wrote) != 3 || c.Firmware.Enabled || c.SystemUpdate.Enabled || c.Catalog.DailyOn() || len(c.OutboundUnset) != 0 {
		t.Fatalf("fresh: %v wrote=%v %+v %+v daily=%v unset=%v", err, wrote, c.Firmware, c.SystemUpdate, c.Catalog.DailyOn(), c.OutboundUnset)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{`"firmware": {
    "enabled": false`, `"daily": false`, `"system_update": {
    "enabled": false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the fresh file must carry %q:\n%s", want, b)
		}
	}
	// the setup done afterwards: nothing flips, the file is not written again
	c, _ = Load(path)
	if len(c.OutboundUnset) != 0 {
		t.Fatalf("after the settle every switch is explicit: %v", c.OutboundUnset)
	}
	before, _ := os.Stat(path)
	if wrote, err := SettleOutbound(path, &c, true); err != nil || wrote != nil || c.Firmware.Enabled || c.SystemUpdate.Enabled || c.Catalog.DailyOn() {
		t.Fatalf("settled file: %v wrote=%v %+v", err, wrote, c.Firmware)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("a settled file is left alone")
	}

	// a file the daemon saved before B-241 on a system in use: the booleans present (on), daily
	// absent - the daily check it ran with is written as on; the present values stay
	path = filepath.Join(dir, "existing.json")
	if err := os.WriteFile(path, []byte(`{"firmware": {"enabled": true, "dir": "/etc/config/firmware"}, "catalog": {"enabled": true, "urls": ["file:///etc/occulite/catalog.json"]}, "system_update": {"enabled": false, "feed": "https://example.org/feed"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil || strings.Join(c.OutboundUnset, ",") != "catalog.daily" || !c.Firmware.Enabled || c.SystemUpdate.Enabled {
		t.Fatalf("existing: %v unset=%v %+v %+v", err, c.OutboundUnset, c.Firmware, c.SystemUpdate)
	}
	if wrote, err := SettleOutbound(path, &c, true); err != nil || strings.Join(wrote, ",") != "catalog.daily" || !c.Catalog.DailyOn() || !c.Firmware.Enabled || c.SystemUpdate.Enabled {
		t.Fatalf("existing settled: %v wrote=%v daily=%v %+v %+v", err, wrote, c.Catalog.DailyOn(), c.Firmware, c.SystemUpdate)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"daily": true`) || !strings.Contains(string(b), `"feed": "https://example.org/feed"`) {
		t.Errorf("existing file after: %s", b)
	}
	// a hand-written file without any of the three on a system in use: all three on, as it ran
	path = filepath.Join(dir, "hand.json")
	if err := os.WriteFile(path, []byte(`{"listen": "127.0.0.1:8183"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ = Load(path)
	if wrote, err := SettleOutbound(path, &c, true); err != nil || len(wrote) != 3 || !c.Firmware.Enabled || !c.SystemUpdate.Enabled || !c.Catalog.DailyOn() {
		t.Fatalf("hand-written on a system in use: %v wrote=%v %+v %+v %v", err, wrote, c.Firmware, c.SystemUpdate, c.Catalog.DailyOn())
	}
	// a file the user already switched is never touched: daily off stays off, whatever the setup
	path = filepath.Join(dir, "chosen.json")
	if err := os.WriteFile(path, []byte(`{"firmware": {"enabled": false}, "system_update": {"enabled": true}, "catalog": {"daily": false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ = Load(path)
	if wrote, err := SettleOutbound(path, &c, true); err != nil || wrote != nil || c.Firmware.Enabled || !c.SystemUpdate.Enabled || c.Catalog.DailyOn() {
		t.Fatalf("chosen: %v wrote=%v %+v %+v %v", err, wrote, c.Firmware, c.SystemUpdate, c.Catalog.DailyOn())
	}
	// an unwritable path: the values hold in memory, the error is reported, the keys stay listed
	path = filepath.Join(dir, "missing-dir", "occulited.json")
	c, _ = Load(path)
	if _, err := SettleOutbound(path, &c, true); err == nil || len(c.OutboundUnset) != 3 || !c.Firmware.Enabled {
		t.Fatalf("unwritable: err=%v unset=%v %+v", err, c.OutboundUnset, c.Firmware)
	}
}

// occulited task 19: lite-rpc's streams per token or session - absent is 3, a value in 1-16 is
// taken as stored, and one out of bounds gives the default with false (main logs it); the file
// keeps the key through a load and a save
func TestStreamsPerSession(t *testing.T) {
	for _, c := range []struct {
		stored, want int
		ok           bool
	}{{0, 3, true}, {1, 1, true}, {2, 2, true}, {16, 16, true}, {17, 3, false}, {-1, 3, false}} {
		n, ok := RPCConfig{StreamsPerSession: c.stored}.StreamsPerSessionLimit()
		if n != c.want || ok != c.ok {
			t.Errorf("%d: %d %v, want %d %v", c.stored, n, ok, c.want, c.ok)
		}
	}
	path := filepath.Join(t.TempDir(), "occulited.json")
	if err := os.WriteFile(path, []byte(`{"rpc": {"streams_per_session": 4}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := c.RPC.StreamsPerSessionLimit(); n != 4 || !ok {
		t.Fatalf("loaded: %d %v", n, ok)
	}
	if n, _ := Default().RPC.StreamsPerSessionLimit(); n != DefaultStreamsPerSession {
		t.Fatalf("default: %d", n)
	}
}

// occulited task 124: the pre-resolved catalogue is on by default for both checks, "off" switches
// it off, any other value is the URL.
func TestResolvedURL(t *testing.T) {
	for _, tc := range []struct{ v, want string }{
		{"", DefaultResolvedURL},
		{"off", ""},
		{"file:///etc/occulite/catalog-resolved.json", "file:///etc/occulite/catalog-resolved.json"},
	} {
		if got := (CatalogConfig{Resolved: tc.v}).ResolvedURL(); got != tc.want {
			t.Errorf("catalog %q: %q", tc.v, got)
		}
		if got := (SystemUpdateConfig{Resolved: tc.v}).ResolvedURL(); got != tc.want {
			t.Errorf("system update %q: %q", tc.v, got)
		}
	}
}
