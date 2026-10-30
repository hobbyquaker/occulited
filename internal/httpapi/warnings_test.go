package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/bootchart"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/radio"
	"github.com/hobbyquaker/occulited/internal/servicemsg"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

func warnRig(t *testing.T) (*http.ServeMux, system.Root, string) {
	t.Helper()
	r := fakeRoot(t)
	svc := scriptBox{system.AddonScripts{Root: r}}
	a := &SystemAPI{Root: r, Services: svc, Addons: svc, Manager: svc, MetaRecovered: true}
	file := filepath.Join(t.TempDir(), "warnings.json")
	a.WarningTracker(file, nil)
	mux := http.NewServeMux()
	a.Register(mux)
	return mux, r, file
}

func warnCall(t *testing.T, mux *http.ServeMux, method, path, body, user string, role auth.Role) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s-" + user, User: user, Role: role, Scopes: auth.RoleScopes(role)}))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func warningByID(out map[string]any, id string) map[string]any {
	list, _ := out["warnings"].([]any)
	for _, x := range list {
		if w, ok := x.(map[string]any); ok && w["id"] == id {
			return w
		}
	}
	return nil
}

const user = auth.Role("user")

// The unclean-shutdown warning opens the log of the boot before this one, which ends where the box
// went down (task 93), where the journal holds that boot; in RAM, without a journal or when
// journalctl fails it opens this boot's log from an hour before the marker.
func TestUncleanWarningLink(t *testing.T) {
	at := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	since := "/log?since=@" + strconv.FormatInt(at.Unix()-3600, 10)
	const this = `{"index":0,"boot_id":"4c1d2a6b0e8f4a2b9c3d5e7f8a1b2c3d","first_entry":1757660460000000,"last_entry":1757692800000000}`
	for _, tc := range []struct {
		name    string
		journal bool
		answer  string
		err     error
		want    string
	}{
		{"a persistent journal with the previous boot", true, `[{"index":-1,"boot_id":"7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d","first_entry":1757533290000000,"last_entry":1757660400000000},` + this + `]`, nil, "/log?boot=-1"},
		{"a journal in RAM holds this boot alone", true, `[` + this + `]`, nil, since},
		{"journalctl fails", true, "", os.ErrNotExist, since},
		{"no journal", false, "", nil, since},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fakeRoot(t)
			marker := filepath.Join(string(r), "var/status/uncleanShutdown")
			if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(marker, at, at); err != nil {
				t.Fatal(err)
			}
			a := &SystemAPI{Root: r}
			if tc.journal {
				a.Journal = &system.JournalLog{Run: func(context.Context, string, ...string) ([]byte, error) {
					return []byte(tc.answer), tc.err
				}}
			}
			ws, ok := a.uncleanWarning(context.Background())
			if !ok || len(ws) != 1 || ws[0].ID != "unclean" || ws[0].Href != tc.want || ws[0].Variant != strconv.FormatInt(at.Unix(), 10) {
				t.Errorf("%+v %v, want the href %s", ws, ok, tc.want)
			}
		})
	}
}

// B-114: on a box without a real-time clock the marker is written before the clock is set and
// carries the image's date (the Pi 4's notice named 13 March). The notice and the RAM link take this
// boot's start instead; the variant stays the marker's own time, so a silence still holds. A marker
// written after the boot began keeps its time.
func TestUncleanWarningBeforeTheClock(t *testing.T) {
	r := fakeRoot(t)
	writeRootFile(t, r, "proc/uptime", "600.00 1000.00\n")
	writeRootFile(t, r, "var/status/uncleanShutdown", "")
	marker := filepath.Join(string(r), "var/status/uncleanShutdown")
	a := &SystemAPI{Root: r}
	near := func(what string, got, want time.Time) {
		t.Helper()
		if d := got.Sub(want); d < -5*time.Second || d > 5*time.Second {
			t.Errorf("%s: %s, want about %s", what, got, want)
		}
	}
	image := time.Date(2026, 3, 13, 17, 8, 31, 0, time.UTC)
	recent := time.Now().Add(-300 * time.Second).Truncate(time.Second)
	for _, tc := range []struct {
		name   string
		marker time.Time
		want   time.Time
	}{
		{"written before the clock was set", image, time.Now().Add(-600 * time.Second)},
		{"written after the boot began", recent, recent},
	} {
		if err := os.Chtimes(marker, tc.marker, tc.marker); err != nil {
			t.Fatal(err)
		}
		ws, ok := a.uncleanWarning(context.Background())
		if !ok || len(ws) != 1 || ws[0].Variant != strconv.FormatInt(tc.marker.Unix(), 10) {
			t.Fatalf("%s: %+v", tc.name, ws)
		}
		since, err := strconv.ParseInt(strings.TrimPrefix(ws[0].Href, "/log?since=@"), 10, 64)
		if err != nil {
			t.Fatalf("%s: %s", tc.name, ws[0].Href)
		}
		near(tc.name+", the link", time.Unix(since, 0), tc.want.Add(-time.Hour))
		at, err := time.Parse(time.RFC3339, ws[0].Params["at"].(string))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, ws[0].Params)
		}
		near(tc.name+", the notice", at, tc.want)
	}
}

func TestWarningsRoutes(t *testing.T) {
	mux, r, file := warnRig(t)
	marker := filepath.Join(string(r), "var/status/uncleanShutdown")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	if err := os.Chtimes(marker, at, at); err != nil {
		t.Fatal(err)
	}

	st, out := warnCall(t, mux, "GET", "/api/system/v1/warnings", "", "admin", auth.RoleAdmin)
	if st != 200 {
		t.Fatalf("list: %d %v", st, out)
	}
	if p, _ := out["periods"].([]any); len(p) != 3 || p[0] != 1.0 || p[1] != 7.0 || p[2] != 90.0 {
		t.Errorf("periods: %v", out["periods"])
	}
	bt := warningByID(out, "backup-target")
	if bt == nil || bt["variant"] != "/media/usb0/backup" || bt["href"] != "/backup" || bt["severity"] != "error" || bt["params"].(map[string]any)["path"] != "/media/usb0/backup" {
		t.Fatalf("backup-target: %v", out)
	}
	if m := warningByID(out, "meta"); m == nil || m["variant"] != strconv.FormatInt(processStart.Unix(), 10) || m["href"] != "/app" {
		t.Errorf("meta: %v", m)
	}
	u := warningByID(out, "unclean")
	if u == nil || u["variant"] != strconv.FormatInt(at.Unix(), 10) || u["href"] != "/log?since=@"+strconv.FormatInt(at.Unix()-3600, 10) || u["params"].(map[string]any)["at"] != "2026-09-12T03:00:00Z" {
		t.Errorf("unclean: %v", u)
	}

	// silencing is an administrator's (D-64), with one of the three periods (D-65)
	body := `{"id":"backup-target","variant":"/media/usb0/backup","days":7}`
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/warnings/silence", body, "monitor", user); st != 403 {
		t.Errorf("a user silences: %d", st)
	}
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/warnings/silence", strings.Replace(body, "7", "30", 1), "admin", auth.RoleAdmin); st != 400 || out["error"] != "invalid-period" {
		t.Errorf("30 days: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/warnings/silence", `{"id":"certificate","variant":"expiring","days":7}`, "admin", auth.RoleAdmin); st != 404 || out["error"] != "not-active" {
		t.Errorf("an inactive warning: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/warnings/silence", "{nope", "admin", auth.RoleAdmin); st < 400 || st > 499 {
		t.Errorf("a broken body: %d %v", st, out)
	}
	st, out = warnCall(t, mux, "POST", "/api/system/v1/warnings/silence", body, "admin", auth.RoleAdmin)
	if st != 200 || out["silence"].(map[string]any)["by"] != "admin" {
		t.Fatalf("silence: %d %v", st, out)
	}
	if s, _ := warningByID(out, "backup-target")["silenced"].(map[string]any); s == nil || s["by"] != "admin" {
		t.Errorf("the answer carries the silence: %v", out)
	}
	if b, err := os.ReadFile(file); err != nil || !strings.Contains(string(b), `"by": "admin"`) {
		t.Errorf("the file: %s %v", b, err)
	}
	// per user: another administrator and a user still see it
	_, out = warnCall(t, mux, "GET", "/api/system/v1/warnings", "", "sebastian", auth.RoleAdmin)
	if w := warningByID(out, "backup-target"); w == nil || w["silenced"] != nil {
		t.Errorf("another admin: %v", w)
	}
	_, out = warnCall(t, mux, "GET", "/api/system/v1/warnings", "", "monitor", user)
	if w := warningByID(out, "backup-target"); w == nil || w["silenced"] != nil || out["periods"] != nil {
		t.Errorf("a user: %v", out)
	}

	// lifting it: the variant is a path, sent escaped
	del := "/api/system/v1/warnings/silence/backup-target/" + strings.ReplaceAll("/media/usb0/backup", "/", "%2F")
	if st, _ := warnCall(t, mux, "DELETE", del, "", "monitor", user); st != 403 {
		t.Errorf("a user lifts: %d", st)
	}
	if st, out := warnCall(t, mux, "DELETE", del, "", "admin", auth.RoleAdmin); st != 200 || warningByID(out, "backup-target")["silenced"] != nil {
		t.Fatalf("unsilence: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "DELETE", del, "", "admin", auth.RoleAdmin); st != 404 || out["error"] != "not-silenced" {
		t.Errorf("again: %d %v", st, out)
	}

	// the clearing: silenced, then the target appears - the silence is spent at the next read
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/warnings/silence", body, "admin", auth.RoleAdmin); st != 200 {
		t.Fatal(st)
	}
	if err := os.MkdirAll(filepath.Join(string(r), "media/usb0/backup"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, out = warnCall(t, mux, "GET", "/api/system/v1/warnings", "", "admin", auth.RoleAdmin)
	if warningByID(out, "backup-target") != nil {
		t.Errorf("the target exists: %v", out)
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "backup-target") {
		t.Errorf("the silence is spent:\n%s", b)
	}
}

func TestWarningsWithoutTracker(t *testing.T) {
	srv := systemServer(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/system/v1/warnings"},
		{"POST", "/api/system/v1/warnings/silence"},
		{"DELETE", "/api/system/v1/warnings/silence/a/b"},
	} {
		if st, out, _ := do(t, srv, c.method, c.path, "{}", nil); st != 501 {
			t.Errorf("%s %s: %d %v", c.method, c.path, st, out)
		}
	}
}

func TestSecurityKeyRule(t *testing.T) {
	type key struct{ set, known, ok bool }
	up := bidcosState{detected: true, module: true, rfd: rfdUp, paired: 1}
	cases := []struct {
		name   string
		st     bidcosState
		key    key
		active bool
		ok     bool
	}{
		{"BidCos-RF with the default key", up, key{false, true, true}, true, true},
		{"BidCos-RF with an own key", up, key{true, true, true}, false, true},
		{"rfd answers BidCos-RF without a module in hm_mode", bidcosState{detected: true, rfd: rfdUp, paired: 3}, key{false, true, true}, true, true},
		// occulited task 89: HmIP only on a radio that carries both - rfd runs, no BidCos device paired
		{"module, rfd up, no BidCos device paired", bidcosState{detected: true, module: true, rfd: rfdUp, paired: 0}, key{false, true, true}, false, true},
		{"module, rfd up, one BidCos device paired", bidcosState{detected: true, module: true, rfd: rfdUp, paired: 1}, key{false, true, true}, true, true},
		{"module, rfd up, the devices not known yet", bidcosState{detected: true, module: true, rfd: rfdUp, paired: -1}, key{false, true, true}, false, false},
		{"no BidCos device paired, an own key", bidcosState{detected: true, module: true, rfd: rfdUp, paired: 0}, key{true, true, true}, false, true},
		{"HmIP only: no module, rfd asked after the detection", bidcosState{detected: true, rfd: rfdDown}, key{false, true, true}, false, true},
		{"no module, rfd not asked since the detection", bidcosState{detected: true, rfd: rfdUnknown}, key{false, true, true}, false, false},
		// B-107: occulited's evaluation at start on the Pi 4, 20 s before the detection wrote hm_mode
		{"the detection has not finished, no module yet", bidcosState{rfd: rfdDown}, key{false, true, true}, false, false},
		{"the detection has not finished, whatever the rest says", bidcosState{module: true, rfd: rfdUp}, key{false, true, true}, false, false},
		{"rfd down", bidcosState{detected: true, module: true, rfd: rfdDown}, key{false, true, true}, false, false},
		{"rfd not asked yet", bidcosState{detected: true, module: true, rfd: rfdUnknown}, key{false, true, true}, false, false},
		{"crypttool failed", up, key{false, true, false}, false, false},
		{"no crypttool", up, key{false, false, true}, false, true},
	}
	for _, c := range cases {
		asked := false
		list, ok := securityKeyRule(c.st, func() (bool, bool, bool) { asked = true; return c.key.set, c.key.known, c.key.ok })
		if ok != c.ok || (len(list) == 1) != c.active {
			t.Errorf("%s: %v %v", c.name, list, ok)
		}
		if c.active && (list[0].ID != "security-key" || list[0].Variant != "default" || list[0].Href != "/system/keys#security-key" || list[0].Severity != warnings.SeverityWarning) {
			t.Errorf("%s: %+v", c.name, list[0])
		}
		if (!c.st.detected || c.st.rfd != rfdUp || c.st.paired <= 0) && asked {
			t.Errorf("%s: crypttool is not run before the detection has finished, rfd is up and a BidCos device is paired", c.name)
		}
	}
}

// B-107 as the Pi 4 showed it: the security-key warning silenced, a reboot, and occulited's evaluation
// at start 20 s before the radio detection wrote /var/hm_mode. The silence is kept through the
// detection and a poll from before it; after rfd's first poll the warning is back and still
// silenced; an HmIP-only box after the detection still spends the silence.
func TestSecurityKeySilenceSurvivesABoot(t *testing.T) {
	r := fakeRoot(t)
	file := filepath.Join(t.TempDir(), "warnings.json")
	const withBidCos = "HM_MODE='NORMAL'\nHM_HMRF_DEV='HMIP-RFUSB'\nHM_HMIP_DEV='HMIP-RFUSB'\n"
	bidcos := []interfaces.RadioInterface{{Interface: "HmIP-RF"}, {Interface: "BidCos-RF"}}
	hmip := []interfaces.RadioInterface{{Interface: "HmIP-RF"}}

	type boot struct {
		a      *SystemAPI
		svc    *unitsServices
		polled time.Time
		list   []interfaces.RadioInterface
	}
	start := func() *boot {
		b := &boot{svc: &unitsServices{}}
		b.a = &SystemAPI{Root: r, Services: b.svc}
		b.a.warn.keyState = func(context.Context) (bool, bool, error) { return false, true, nil } // the default key
		b.a.warn.rfdPoll = func() (time.Time, []interfaces.RadioInterface) { return b.polled, b.list }
		b.a.warn.paired = func() (int, bool) { return 2, true } // two BidCos devices (occulited task 89)
		b.a.WarningTracker(file, nil)
		return b
	}
	detection := func(b *boot, state string, activeFor time.Duration) {
		b.svc.units = []system.InterfaceUnit{
			{Unit: system.RadioDetectionUnit, Interfaces: []string{}, ActiveState: state, ActiveFor: activeFor},
			{Unit: "rfd", Interfaces: []string{"BidCos-RF"}, ActiveState: "active"},
		}
		b.a.units.at = time.Time{} // past the cache's three seconds
	}
	shown := func(b *boot) (active, silenced bool) {
		t.Helper()
		for _, w := range b.a.Warnings.List(context.Background(), "admin", true) {
			if w.ID == "security-key" {
				return true, w.Silenced != nil
			}
		}
		return false, false
	}
	kept := func() bool {
		b, _ := os.ReadFile(file)
		return strings.Contains(string(b), `"security-key"`)
	}

	// the boot before: detected, rfd answers BidCos-RF, the default key; silenced for 7 days
	putRootFile(t, r, "var/hm_mode", withBidCos)
	one := start()
	detection(one, "active", time.Hour)
	one.polled, one.list = time.Now(), bidcos
	if _, err := one.a.Warnings.Silence(context.Background(), "admin", "security-key", "default", 7); err != nil {
		t.Fatal(err)
	}

	// the reboot: /var/hm_mode as it is before the detection, its unit still starting, rfd not up
	putRootFile(t, r, "var/hm_mode", "HM_MODE='NORMAL'\n")
	two := start()
	detection(two, "activating", 0)
	two.polled, two.list = time.Now(), hmip
	if active, _ := shown(two); active || !kept() {
		t.Fatalf("at start: shown %v, the silence kept %v", active, kept())
	}
	// the detection has finished; the sampler's last poll is from before it
	putRootFile(t, r, "var/hm_mode", withBidCos)
	detection(two, "active", 2*time.Second)
	two.polled = time.Now().Add(-10 * time.Second)
	if active, _ := shown(two); active || !kept() {
		t.Fatalf("after the detection, before rfd's poll: shown %v, the silence kept %v", active, kept())
	}
	// rfd's first poll after the detection: the warning is back, still silenced
	two.polled, two.list = time.Now(), bidcos
	if active, silenced := shown(two); !active || !silenced {
		t.Fatalf("after rfd's first poll: shown %v, silenced %v", active, silenced)
	}
	// an HmIP-only box after the detection: cleared, and the silence is spent
	putRootFile(t, r, "var/hm_mode", "HM_MODE='NORMAL'\nHM_HMIP_DEV='HMIP-RFUSB'\n")
	two.polled, two.list = time.Now(), hmip
	if active, _ := shown(two); active || kept() {
		t.Errorf("HmIP only: shown %v, the silence kept %v", active, kept())
	}
	// without systemd there is no detection to wait for
	three := &SystemAPI{Root: r}
	three.warn.rfdPoll = func() (time.Time, []interfaces.RadioInterface) { return time.Now(), hmip }
	if st := three.bidcosState(context.Background()); !st.detected || st.module || st.rfd != rfdDown || st.paired != -1 {
		t.Errorf("busybox: %+v", st)
	}
	// occulited task 89: the paired BidCos devices come from the service-message sweep
	three.warn.paired = func() (int, bool) { return 0, true }
	if st := three.bidcosState(context.Background()); st.paired != 0 {
		t.Errorf("no BidCos device: %+v", st)
	}
	three.warn.paired = nil
	three.ServiceMessages = &servicemsg.Store{}
	if st := three.bidcosState(context.Background()); st.paired != -1 {
		t.Errorf("before rfd answered a sweep: %+v", st)
	}
	// a systemd that does not answer: not known to have finished
	four := &SystemAPI{Root: r, Services: &unitsServices{}}
	if st := four.bidcosState(context.Background()); st.detected {
		t.Errorf("no answer from systemd: %+v", st)
	}
}

// The boot gate (B-107): until systemd's manager has finished starting, a warning that reads as clear
// keeps its silences - here the backup target on the box itself, whose directory is not there yet at
// start. Once settled a warning that is really gone spends its silence; the uptime cap settles a boot
// that never finishes, and a box without systemd is settled.
func TestBootGateKeepsSilencesWhileTheBoxStarts(t *testing.T) {
	r := fakeRoot(t)
	file := filepath.Join(t.TempDir(), "warnings.json")
	target := filepath.Join(string(r), "media/usb0/backup")
	finish, asked := "0", 0
	chart := &bootchart.Reader{Systemctl: func(_ context.Context, args ...string) ([]byte, error) {
		asked++
		if got := strings.Join(args, " "); got != "show -p FinishTimestampMonotonic" {
			t.Errorf("systemctl %s", got)
		}
		return []byte("FinishTimestampMonotonic=" + finish + "\n"), nil
	}}
	api := func() *SystemAPI {
		a := &SystemAPI{Root: r, BootChart: chart}
		a.WarningTracker(file, nil)
		return a
	}
	list := func(a *SystemAPI) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, w := range a.Warnings.List(context.Background(), "admin", true) {
			out[w.ID] = w.Silenced != nil
		}
		return out
	}
	kept := func() bool {
		b, _ := os.ReadFile(file)
		return strings.Contains(string(b), `"backup-userfs"`)
	}

	// the boot before, finished: the backup writes onto the box itself, silenced
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	finish = "41000000"
	one := api()
	if _, err := one.Warnings.Silence(context.Background(), "admin", "backup-userfs", "/media/usb0/backup", 90); err != nil {
		t.Fatal(err)
	}

	// the reboot, not finished: the directory is not there yet
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	finish = "0"
	two := api()
	got := list(two)
	_, missing := got["backup-target"]
	_, userfs := got["backup-userfs"]
	if !missing || userfs || !kept() {
		t.Fatalf("at start: %v (the missing target is shown, the userfs one is not), the silence kept %v", got, kept())
	}
	// there again: still silenced
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := list(two); !got["backup-userfs"] {
		t.Fatalf("back: %v", got)
	}
	// asked once per boot while it starts, at most every bootRecheck
	if asked != 2 {
		t.Errorf("systemctl asked %d times", asked)
	}
	// the boot has finished, and the backup is switched off: the silence is spent
	finish, two.warn.bootAskedAt = "93000000", time.Time{}
	putRootFile(t, r, "etc/config/NoCronBackup", "")
	got = list(two)
	_, missing = got["backup-target"]
	_, userfs = got["backup-userfs"]
	if missing || userfs || kept() {
		t.Errorf("settled, backup off: %v, the silence kept %v", got, kept())
	}

	// a boot that never finishes counts as settled after bootSettleCap of uptime
	finish = "0"
	three := &SystemAPI{Root: r, BootChart: chart}
	if three.bootSettled() {
		t.Error("not finished, no uptime: settled")
	}
	putRootFile(t, r, "proc/uptime", "901.50 1000.00\n")
	if !three.bootSettled() {
		t.Error("15 minutes up: not settled")
	}
	if !(&SystemAPI{Root: r}).bootSettled() {
		t.Error("without systemd: not settled")
	}
}

// storage: a kernel log that could not be read leaves a verdict below replace unknown (B-107)
func TestStorageWarningWithoutTheKernelLog(t *testing.T) {
	r := fakeRoot(t)
	a := &SystemAPI{Root: r, Storage: &system.Storage{Root: r}} // a test root: the kernel log is not read
	if list, ok := a.storageWarning(context.Background()); ok || len(list) != 0 {
		t.Errorf("no kernel log: %v %v", list, ok)
	}
	a.Storage = &system.Storage{Root: r, Kernel: func(context.Context) ([]system.LogLine, error) { return nil, nil }}
	if list, ok := a.storageWarning(context.Background()); !ok || len(list) != 0 {
		t.Errorf("a good verdict with the kernel log: %v %v", list, ok)
	}
}

// B-106: an install or a policy switch that started a confined addon has B-92's check look at it again
// a moment later and the warnings evaluated then - seen here through the tracker's last evaluation.
func TestOwnershipIsCheckedAgainAfterAStart(t *testing.T) {
	old := ownershipRecheck
	ownershipRecheck = 0
	t.Cleanup(func() { ownershipRecheck = old })
	r := fakeRoot(t)
	quiet := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	sa := system.NewSystemdAddons(r, system.SystemdServices{Root: r, Run: quiet})
	a := &SystemAPI{Root: r, Manager: sa}
	a.WarningTracker(filepath.Join(t.TempDir(), "warnings.json"), nil)
	if sa.AfterStart == nil {
		t.Fatal("the addon manager has no hook")
	}
	if n := len(a.Warnings.Unsilenced()); n != 0 {
		t.Fatalf("evaluated before the start: %d", n)
	}
	sa.AfterStart([]string{"mosquitto"})
	deadline := time.Now().Add(5 * time.Second)
	for len(a.Warnings.Unsilenced()) == 0 { // the fake root's missing backup target
		if time.Now().After(deadline) {
			t.Fatal("no evaluation after the start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRfdOf(t *testing.T) {
	now := time.Now()
	if rfdOf(time.Time{}, nil) != rfdUnknown {
		t.Error("not polled yet")
	}
	if rfdOf(now, []interfaces.RadioInterface{{Interface: "HmIP-RF"}}) != rfdDown {
		t.Error("no BidCos-RF answer")
	}
	if rfdOf(now, []interfaces.RadioInterface{{Interface: "HmIP-RF"}, {Interface: "BidCos-RF"}}) != rfdUp {
		t.Error("BidCos-RF answered")
	}
}

func TestCertificateVariant(t *testing.T) {
	for _, c := range []struct {
		warning string
		days    int
		want    string
	}{
		{"", 200, ""},
		{"self-signed", 3000, ""},
		{"last-attempt-failed", 60, "renewal-failed"},
		{"last-attempt-failed", -2, "renewal-failed"},
		{"", -1, "expired"},
		{"expiring", 9, "expiring"},
		{"", 13, "expiring"},
		{"", 14, ""},
	} {
		if got := certificateVariant(c.warning, c.days); got != c.want {
			t.Errorf("%q %d: %q, want %q", c.warning, c.days, got, c.want)
		}
	}
}

func TestAddonListVariant(t *testing.T) {
	w := addonListWarning("rega", warnings.SeverityError, "/addons", []warnAddon{{ID: "hm-print"}, {ID: "email", Enabled: true}})
	if w.Variant != "email,hm-print" {
		t.Errorf("variant %q", w.Variant)
	}
	if list := w.Params["addons"].([]warnAddon); list[0].ID != "email" {
		t.Errorf("sorted: %+v", list)
	}
}

func TestAddonOwnershipRoute(t *testing.T) {
	r := fakeRoot(t)
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Manager: svc}).Register(mux)
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/addons/hmm/ownership", "", "monitor", user); st != 403 {
		t.Errorf("a user: %d", st)
	}
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/addons/hmm/ownership", "", "admin", auth.RoleAdmin); st != 501 {
		t.Errorf("without systemd: %d %v", st, out)
	}
	quiet := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	mux = http.NewServeMux()
	(&SystemAPI{Root: r, Manager: system.NewSystemdAddons(r, system.SystemdServices{Root: r, Run: quiet})}).Register(mux)
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/addons/mosquitto/ownership", "", "admin", auth.RoleAdmin); st != 409 || out["error"] != "not-confined" {
		t.Errorf("an addon without a confined policy: %d %v", st, out)
	}
}

// B-152, task 157: a family without the jump to lite-input is the warning, naming the families;
// a box without iptables has nothing to warn about, and a failed read is unknown
func TestFirewallRule(t *testing.T) {
	list, ok := firewallRule(map[string]bool{"ipv4": false, "ipv6": false}, true, true)
	if !ok || len(list) != 1 || list[0].ID != "firewall" || list[0].Variant != "ipv4,ipv6" || list[0].Href != "/system/firewall" {
		t.Fatalf("not loaded: %v %+v", ok, list)
	}
	if list, ok := firewallRule(map[string]bool{"ipv4": true, "ipv6": false}, true, true); !ok || len(list) != 1 || list[0].Variant != "ipv6" {
		t.Fatalf("ipv6 only: %v %+v", ok, list)
	}
	if list, ok := firewallRule(map[string]bool{"ipv4": true, "ipv6": true}, true, true); !ok || len(list) != 0 {
		t.Fatalf("loaded: %v %+v", ok, list)
	}
	if list, ok := firewallRule(nil, false, true); !ok || len(list) != 0 {
		t.Fatalf("no iptables: %v %+v", ok, list)
	}
	if _, ok := firewallRule(nil, true, false); ok {
		t.Fatal("a failed read counted as known")
	}
}

// the policies are read once per keyStateTTL, a failed read is not cached, and an applied firewall
// is read again at once
func TestFirewallPoliciesCached(t *testing.T) {
	var s warningsState
	reads := 0
	fail := true
	s.fwRead = func(context.Context) (map[string]bool, bool, error) {
		reads++
		if fail {
			return nil, true, errors.New("helper down")
		}
		return map[string]bool{"ipv4": false, "ipv6": true}, true, nil
	}
	if _, _, ok := s.firewall(context.Background()); ok {
		t.Fatal("a failed read answered ok")
	}
	fail = false
	for range 3 {
		if p, avail, ok := s.firewall(context.Background()); !ok || !avail || p["ipv4"] || !p["ipv6"] {
			t.Fatalf("read: %v %v %v", p, avail, ok)
		}
	}
	if reads != 2 {
		t.Fatalf("reads %d, want 2 (the failed one and one cached)", reads)
	}
	s.forgetFirewall()
	s.firewall(context.Background())
	if reads != 3 {
		t.Fatalf("reads %d after forgetFirewall, want 3", reads)
	}
}

// task 173: classic RPC on without a login warns, with what is open as the variant; a login or
// both switches off is quiet
func TestClassicRPCWarning(t *testing.T) {
	for _, c := range []struct {
		in      system.ClassicRPC
		variant string
	}{
		{system.ClassicRPC{Auth: "none"}, ""},
		{system.ClassicRPC{Plain: true, Auth: "none"}, "plain"},
		{system.ClassicRPC{TLS: true, Auth: "none"}, "tls"},
		{system.ClassicRPC{Plain: true, TLS: true, Auth: "none"}, "plain,tls"},
		{system.ClassicRPC{Plain: true, TLS: true, Auth: "password", PasswordSet: true}, ""},
	} {
		got := classicRPCRule(c.in)
		if c.variant == "" {
			if len(got) != 0 {
				t.Errorf("%+v: %+v", c.in, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Variant != c.variant || got[0].Severity != warnings.SeverityWarning || got[0].Href != "/system/remote-access" {
			t.Errorf("%+v: %+v", c.in, got)
		}
	}
}

// B-89: hmip-port-open while hmipserver's HTTP port listens beyond the loopback, and nothing
// while the shim holds it there.
func TestHmIPPortWarning(t *testing.T) {
	root := t.TempDir()
	head := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	set := func(line string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, "proc/net"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "proc/net/tcp6"), []byte(head+line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := &SystemAPI{Root: system.Root(root)}
	set("   0: 0000000000000000FFFF00000100007F:997C 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  8113        0 1 1\n")
	if w, ok := a.hmipPortWarning(context.Background()); !ok || len(w) != 0 {
		t.Fatalf("on the loopback: %v %v", w, ok)
	}
	set("   0: 00000000000000000000000000000000:997C 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  8113        0 1 1\n")
	w, ok := a.hmipPortWarning(context.Background())
	if !ok || len(w) != 1 || w[0].ID != "hmip-port-open" || w[0].Variant != "39292" || w[0].Params["addresses"] != "::" {
		t.Fatalf("open: %+v %v", w, ok)
	}
}

// Task 201: hmip-key-declined while hmipserver has turned an inclusion down for a wrong key in the
// map, and nothing once that device is paired. The journal is read through the JournalLog's own
// runner seam, so the test sees the arguments journalctl would get as well.
func TestHmIPKeyDeclinedWarning(t *testing.T) {
	const sgtin = "3014F711A0001F0000000A04"
	entry := func(us int64, msg string) string {
		b, err := json.Marshal(map[string]any{"__REALTIME_TIMESTAMP": strconv.FormatInt(us, 10), "MESSAGE": msg, "_SYSTEMD_UNIT": "hmipserver.service"})
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	decline := func(s string) string {
		return "AP 3014F711A000040000000A01: The inclusion for device " + s + " is declined, the local key is wrong"
	}

	var gotArgs []string
	var out string
	var runErr error
	a := &SystemAPI{Journal: &system.JournalLog{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "journalctl" {
			t.Errorf("ran %q", name)
		}
		gotArgs = args
		return []byte(out), runErr
	}}}

	// nothing in the log: no warning, and the read is the cheap one - this boot, the unit, and
	// journalctl's own grep on the fixed part of the line
	if w, ok := a.hmipKeyDeclinedWarning(context.Background()); !ok || len(w) != 0 {
		t.Fatalf("quiet: %+v %v", w, ok)
	}
	args := strings.Join(gotArgs, " ")
	for _, want := range []string{"-u hmipserver.service", "--boot=0", "-g " + radio.InclusionDeclinedMatch} {
		if !strings.Contains(args, want) {
			t.Errorf("journalctl args %q miss %q", args, want)
		}
	}

	// a decline, and no device listing available: the warning names the device and its address
	out = entry(1_700_000_000_000_000, decline(sgtin))
	w, ok := a.hmipKeyDeclinedWarning(context.Background())
	if !ok || len(w) != 1 {
		t.Fatalf("declined: %+v %v", w, ok)
	}
	if w[0].ID != "hmip-key-declined" || w[0].Severity != warnings.SeverityWarning || w[0].Href != "/system/keys#device-keys" {
		t.Errorf("warning: %+v", w[0])
	}
	if w[0].Params["sgtin"] != sgtin || w[0].Params["address"] != "001F0000000A04" {
		t.Errorf("params: %+v", w[0].Params)
	}
	at, _ := w[0].Params["at"].(string)
	if at == "" || w[0].Variant != sgtin+"@"+at {
		t.Errorf("variant %q, at %q", w[0].Variant, at)
	}

	// a second, newer decline of the same device is the same warning with a new variant, so a
	// silence of the first does not swallow it
	first := w[0].Variant
	out += entry(1_700_000_060_000_000, decline(sgtin))
	w, _ = a.hmipKeyDeclinedWarning(context.Background())
	if len(w) != 1 || w[0].Variant == first {
		t.Errorf("second decline: %+v (was %q)", w, first)
	}

	// two devices, one warning each, in the order they were declined
	const other = "3014F711A000040000000A01"
	out = entry(1_700_000_000_000_000, decline(sgtin)) + entry(1_700_000_010_000_000, decline(other))
	w, _ = a.hmipKeyDeclinedWarning(context.Background())
	if len(w) != 2 || w[0].Params["sgtin"] != sgtin || w[1].Params["sgtin"] != other {
		t.Fatalf("two devices: %+v", w)
	}

	// a line about the same map that is not a decline raises nothing
	out = entry(1_700_000_000_000_000, "AP 30: Add local key of device "+sgtin+" for inclusion from map to whitelist")
	if w, ok := a.hmipKeyDeclinedWarning(context.Background()); !ok || len(w) != 0 {
		t.Errorf("not a decline: %+v %v", w, ok)
	}

	// journalctl failed: unknown, so the silences are kept (ok false)
	out, runErr = "", errors.New("no journal")
	if w, ok := a.hmipKeyDeclinedWarning(context.Background()); ok || len(w) != 0 {
		t.Errorf("failed read: %+v %v", w, ok)
	}
	runErr = nil

	// without a journal (a busybox box) the source is simply quiet
	if w, ok := (&SystemAPI{}).hmipKeyDeclinedWarning(context.Background()); !ok || len(w) != 0 {
		t.Errorf("no journal: %+v %v", w, ok)
	}
}

// Task 201: the warning goes once the device is paired - the only way it can go, because the line
// that raised it stays in the journal. A listing that fails says nothing instead (ok false).
func TestHmIPKeyDeclinedGoesWhenTheDeviceIsIn(t *testing.T) {
	const sgtin = "3014F711A0001F0000000A04" // its address is the SGTIN's last 14 digits
	hmip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "listDevices") {
			http.Error(w, "unexpected call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>` +
			`<value><struct><member><name>ADDRESS</name><value><string>001F0000000A04</string></value></member>` +
			`<member><name>TYPE</name><value><string>HmIP-PDT</string></value></member>` +
			`<member><name>PARENT</name><value><string></string></value></member></struct></value>` +
			`</data></array></value></param></params></methodResponse>`))
	}))
	defer hmip.Close()

	line, err := json.Marshal(map[string]any{
		"__REALTIME_TIMESTAMP": "1700000000000000",
		"MESSAGE":              "AP 30: The inclusion for device " + sgtin + " is declined, the local key is wrong",
		"_SYSTEMD_UNIT":        "hmipserver.service",
	})
	if err != nil {
		t.Fatal(err)
	}
	reachable := true
	a := &SystemAPI{
		Journal: &system.JournalLog{Run: func(context.Context, string, ...string) ([]byte, error) { return append(line, '\n'), nil }},
		HmIPDeviceKeys: &system.HmIPDeviceKeys{Root: system.Root(t.TempDir()), HmIP: func() (interfaces.Interface, bool) {
			if !reachable {
				return interfaces.Interface{}, false
			}
			return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(hmip.URL, "http://")}})[0], true
		}},
	}
	if w, ok := a.hmipKeyDeclinedWarning(context.Background()); !ok || len(w) != 0 {
		t.Fatalf("the device is paired, the warning must be gone: %+v %v", w, ok)
	}
	// hmipserver silent: unknown, so nothing is shown and the silences are kept
	reachable = false
	if w, ok := a.hmipKeyDeclinedWarning(context.Background()); ok || len(w) != 0 {
		t.Fatalf("hmipserver silent: %+v %v", w, ok)
	}
}

// openccu-lite task 137: a newer coprocessor firmware on the system is a notice with the versions,
// an HmIP-RFUSB the detection could not read a warning with its node - both lead to the radio
// firmware section, neither flashes anything.
func TestRadioFirmwareWarnings(t *testing.T) {
	root := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("var/hm_mode", "HM_HMIP_DEV='RPI-RF-MOD'\nHM_HMIP_DEVNODE='/dev/raw-uart'\nHM_HMIP_VERSION='4.2.14'\nHM_HMRF_DEV='RPI-RF-MOD'\nHM_HMRF_DEVNODE='/dev/raw-uart'\nHM_HMRF_VERSION='4.2.14'\n")
	w("firmware/RPI-RF-MOD/dualcopro_update_blhmip-4.4.22.eq3", "x")
	w("firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3", "x")
	a := &SystemAPI{RadioFirmware: &system.RadioFirmware{Root: system.Root(root)}}
	ws, ok := a.radioFirmwareWarnings(context.Background())
	if !ok || len(ws) != 1 {
		t.Fatalf("%+v %v", ws, ok)
	}
	if w0 := ws[0]; w0.ID != "radio-firmware" || w0.Variant != "RPI-RF-MOD@4.4.22" || w0.Severity != warnings.SeverityWarning || w0.Href != "/system/updates#radio-firmware" || w0.Params["running"] != "4.2.14" || w0.Params["newest"] != "4.4.22" || w0.Params["drops_off"] != nil {
		t.Errorf("newer: %+v", w0)
	}
	// up to date: nothing
	w("var/hm_mode", "HM_HMIP_DEV='RPI-RF-MOD'\nHM_HMIP_DEVNODE='/dev/raw-uart'\nHM_HMIP_VERSION='4.4.22'\n")
	if ws, _ := a.radioFirmwareWarnings(context.Background()); len(ws) != 0 {
		t.Errorf("up to date: %+v", ws)
	}
	// an HmIP-RFUSB the detection found and could not read
	w("run/occulite/radio/modules.json", `{"modules":[{"name":"raw-uart1","node":"/dev/raw-uart1","device_type":"eQ-3 HmIP-RFUSB@usb-0000:01:00.0-1.3","probe":"timeout"}],"log":[]}`)
	ws, _ = a.radioFirmwareWarnings(context.Background())
	if len(ws) != 1 || ws[0].ID != "radio-module-unusable" || ws[0].Variant != "/dev/raw-uart1" || ws[0].Params["device"] != "HMIP-RFUSB" || ws[0].Params["newest"] != "4.4.18" {
		t.Errorf("unusable: %+v", ws)
	}
	// without the service: quiet
	if ws, ok := (&SystemAPI{}).radioFirmwareWarnings(context.Background()); !ok || len(ws) != 0 {
		t.Errorf("no service: %+v", ws)
	}
}

// openccu-lite task 299: the security counter's state file and the hold marker become warnings.
func TestHmIPCounterWarnings(t *testing.T) {
	mux, r, _ := warnRig(t)
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(string(r), rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, out := warnCall(t, mux, "GET", "/api/system/v1/warnings", "", "admin", auth.RoleAdmin)
	if st != 200 || warningByID(out, "hmip-security-counter") != nil || warningByID(out, "hmip-clock-hold") != nil {
		t.Fatalf("nothing recorded: %d %v", st, out)
	}
	write(radio.CounterStateFile, `{"access_points":{"3014F711A000040000000A02":{"sgtin":"3014F711A000040000000A02","offset":4031374848,"wraps_at":"2026-10-01T00:00:00Z","seen":4031374849,"verdict":"backwards","starts":[{"at":"2026-09-30T09:00:00Z","current":1024874459,"calc":5319842009,"written":1024874713,"source":"journal","verdict":"backwards"}]},"3014F711A000040000000A01":{"sgtin":"3014F711A000040000000A01","offset":2829,"verdict":"fine","starts":[{"at":"2026-09-30T09:00:00Z","calc":7661307,"source":"computed","verdict":"fine"}]}}}`)
	write("run/occulite/radio/hmipserver.clock-hold.json", `{"since":"2026-09-30T09:10:00Z","sgtin":"3014F711A000040000000A02","calc":5319842009,"clock_state":"timeout","reason":"the computed security counter has passed 2^32"}`)
	_, out = warnCall(t, mux, "GET", "/api/system/v1/warnings", "", "admin", auth.RoleAdmin)
	w := warningByID(out, "hmip-security-counter")
	if w == nil || w["variant"] != "backwards" || w["severity"] != "error" || w["href"] != "/system/interfaces#connections" {
		t.Fatalf("counter: %v", w)
	}
	if p := w["params"].(map[string]any); p["sgtin"] != "3014F711A000040000000A02" || p["written"] != 1024874713.0 || p["calc"] != 5319842009.0 || p["current"] != 1024874459.0 || p["wraps_at"] != "2026-10-01T00:00:00Z" {
		t.Fatalf("params: %v", p)
	}
	h := warningByID(out, "hmip-clock-hold")
	if h == nil || h["variant"] != "timeout" || h["severity"] != "error" || h["href"] != "/system/network" || h["params"].(map[string]any)["since"] != "2026-09-30T09:10:00Z" {
		t.Fatalf("hold: %v", h)
	}
	// the fine access point raises nothing: one counter warning
	n := 0
	for _, x := range out["warnings"].([]any) {
		if x.(map[string]any)["id"] == "hmip-security-counter" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d counter warnings", n)
	}
}

// openccu-lite task 316: the radio load's warning - a carrier sense above 10 % in the sampler's last
// poll, with hysteresis, the interface of the highest value named; occulited task 61: the duty cycle
// from 25 %, from 75 % while a device firmware transfer runs on that interface.
func TestRadioLoadWarning(t *testing.T) {
	a := &SystemAPI{}
	var polled time.Time
	var list []interfaces.RadioInterface
	transfers := map[string]bool{}
	a.warn.rfdPoll = func() (time.Time, []interfaces.RadioInterface) { return polled, list }
	a.warn.transfers = func() map[string]bool { return transfers }
	cs := func(n int) *int { return &n }
	eval := func() (string, map[string]any, bool) {
		ws, ok := a.radioLoadWarning(context.Background())
		if len(ws) == 0 {
			return "", nil, ok
		}
		return ws[0].Variant, ws[0].Params, ok
	}
	if _, _, ok := eval(); ok {
		t.Error("before the first poll the source is not ready")
	}
	polled = time.Now()
	list = []interfaces.RadioInterface{{Interface: "BidCos-RF", DutyCycle: 12}, {Interface: "HmIP-RF", DutyCycle: 24, CarrierSense: cs(9)}}
	// the duty cycle's thresholds, in sequence (the hysteresis carries over from step to step)
	for _, c := range []struct {
		name     string
		dc       int
		transfer bool
		want     string
	}{
		{"no transfer, 24 %", 24, false, ""},
		{"no transfer, 25 %: from 25 on", 25, false, "dc"},
		{"no transfer, 26 %", 26, false, "dc"},
		{"hysteresis: 21 % holds", 21, false, "dc"},
		{"hysteresis: 20 % clears", 20, false, ""},
		{"a transfer starts, 50 %", 50, true, ""},
		{"transfer, 74 %", 74, true, ""},
		{"transfer, 76 %", 76, true, "dc"},
		{"transfer, 71 % holds", 71, true, "dc"},
		{"transfer, 70 % clears", 70, true, ""},
		{"the transfer ends at 40 %: the warning now", 40, false, "dc"},
		{"no transfer, 10 %", 10, false, ""},
	} {
		list[1].DutyCycle = c.dc
		transfers = map[string]bool{"HmIP-RF": c.transfer}
		v, p, _ := eval()
		if v != c.want {
			t.Errorf("%s: %q", c.name, v)
			continue
		}
		if v == "dc" && (p["interface"] != "HmIP-RF" || p["duty_cycle"] != c.dc || (p["transfer"] == true) != c.transfer) {
			t.Errorf("%s: params %v", c.name, p)
		}
	}
	// a transfer on HmIP-RF leaves BidCos-RF at 25 %, and the warning names BidCos-RF with its own value
	list[0].DutyCycle, list[1].DutyCycle = 30, 60
	transfers = map[string]bool{"HmIP-RF": true}
	if v, p, _ := eval(); v != "dc" || p["interface"] != "BidCos-RF" || p["duty_cycle"] != 30 || p["transfer"] != nil {
		t.Errorf("BidCos beside a transfer: %q %v", v, p)
	}
	list[0].DutyCycle, list[1].DutyCycle = 12, 10
	_, _, _ = eval()
	// occulited B-83: one module carries both stacks (multimacd) - the transfer on HmIP-RF counts
	// for BidCos-RF's reading of the same radio, so 43 % on both is no warning, 80 % is
	rfusb := &radio.Role{Hardware: "RPI-RF-MOD", Serial: "0000000A04", SGTIN: "3014F711A0001F0000000A04"}
	a.warn.plan = func() (radio.Plan, bool) { return radio.Plan{HmRF: rfusb, HmIP: rfusb, RFDLocal: true}, true }
	shared := []interfaces.RadioInterface{{Interface: "BidCos-RF", Address: "OEQ9000007", Type: "CCU2", DutyCycle: 43}, {Interface: "HmIP-RF", Address: "3014F711A0001F0000000A04", Type: "HMIP_CCU2", DutyCycle: 43}}
	list = shared
	if v, p, _ := eval(); v != "" {
		t.Errorf("shared radio, transfer, 43 %%: %q %v", v, p)
	}
	transfers = map[string]bool{}
	if v, p, _ := eval(); v != "dc" || p["transfer"] != nil {
		t.Errorf("shared radio, no transfer, 43 %%: %q %v", v, p)
	}
	transfers = map[string]bool{"HmIP-RF": true}
	list[0].DutyCycle, list[1].DutyCycle = 80, 80
	if v, p, _ := eval(); v != "dc" || p["transfer"] != true {
		t.Errorf("shared radio, transfer, 80 %%: %q %v", v, p)
	}
	list[0].DutyCycle, list[1].DutyCycle = 10, 10
	_, _, _ = eval()
	a.warn.plan = nil
	// both kinds; the carrier sense alone on BidCos-RF
	list = []interfaces.RadioInterface{{Interface: "BidCos-RF", DutyCycle: 60, CarrierSense: cs(2)}, {Interface: "HmIP-RF", DutyCycle: 10, CarrierSense: cs(11)}}
	if v, p, _ := eval(); v != "dc,cs" || p["interface"] != "BidCos-RF" || p["carrier_sense"] != 11 {
		t.Errorf("both: %q %v", v, p)
	}
	list[0].DutyCycle = 10
	if v, p, _ := eval(); v != "cs" || p["interface"] != "HmIP-RF" {
		t.Errorf("carrier sense alone: %q %v", v, p)
	}
	*list[1].CarrierSense = 6
	if v, _, _ := eval(); v != "cs" {
		t.Errorf("6 %% holds: %q", v)
	}
	*list[1].CarrierSense = 5
	if v, _, _ := eval(); v != "" {
		t.Errorf("5 %% clears: %q", v)
	}
	// the source is on the list
	found := false
	for _, s := range a.warningSources() {
		found = found || slices.Contains(s.IDs, "radio-load")
	}
	if !found {
		t.Error("radio-load is not a warning source")
	}
}

// occulited B-83: a transfer counts for every entry of its radio, and only for those
func TestTransferEntries(t *testing.T) {
	mod := &radio.Role{Hardware: "HMIP-RFUSB", Serial: "0000000A02", SGTIN: "3014F711A000040000000A02"}
	shared := radio.Plan{HmRF: mod, HmIP: mod, RFDLocal: true}
	list := []interfaces.RadioInterface{
		{Interface: "BidCos-RF", Address: "OEQ9000007", Type: "CCU2"},
		{Interface: "BidCos-RF", Address: "NEQ1234567", Type: "HMLGW2"},
		{Interface: "HmIP-RF", Address: "3014F711A000040000000A02", Type: "HMIP_CCU2"},
	}
	hm := map[string]bool{"HmIP-RF": true}
	for _, c := range []struct {
		name      string
		plan      radio.Plan
		havePlan  bool
		transfers map[string]bool
		want      []bool
	}{
		{"no transfer", shared, true, nil, []bool{false, false, false}},
		{"shared module: BidCos-RF's local entry too, not the LAN gateway", shared, true, hm, []bool{true, false, true}},
		{"no plan: only the interface itself", radio.Plan{}, false, hm, []bool{false, false, true}},
		{"a BidCos transfer on the gateway's interface: every BidCos entry, and the module's HmIP", shared, true, map[string]bool{"BidCos-RF": true}, []bool{true, true, true}},
	} {
		if got := TransferEntries(c.plan, c.havePlan, list, c.transfers); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// occulited task 79: the threshold the charts draw is the warning's, per entry
func TestMarkThresholds(t *testing.T) {
	list := []interfaces.RadioInterface{{Interface: "BidCos-RF"}, {Interface: "HmIP-RF"}}
	markThresholds(list, []bool{false, true})
	if list[0].DCThreshold != 25 || list[0].Transfer || list[1].DCThreshold != 75 || !list[1].Transfer {
		t.Fatalf("%+v", list)
	}
}

// B-99: backup-userfs says how much the backups on the system itself take; the variant stays the path.
func TestBackupUserfsSize(t *testing.T) {
	r := fakeRoot(t)
	target := filepath.Join(string(r), "media/usb0/backup")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a.sbk": 3000, "b.sbk": 2000, "notes.txt": 99} {
		if err := os.WriteFile(filepath.Join(target, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := &SystemAPI{Root: r}
	ws, ok := a.backupWarnings(context.Background())
	if !ok || len(ws) != 1 || ws[0].ID != "backup-userfs" || ws[0].Variant != "/media/usb0/backup" {
		t.Fatalf("%v %+v", ok, ws)
	}
	if p := ws[0].Params; p["count"] != 2 || p["bytes"] != int64(5000) || p["path"] != "/media/usb0/backup" {
		t.Fatalf("params %+v", p)
	}
}
