package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/rpieeprom/eepromtest"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

type bootloaderRig struct {
	root   system.Root
	m      *powerManager
	staged int
	err    error
	role   auth.Role
	api    *SystemAPI
	srv    *httptest.Server
}

// newBootloaderRig: a Pi 4 whose bootloader runs current and whose image carries latest.
func newBootloaderRig(t *testing.T, revision string, current, latest int64) *bootloaderRig {
	t.Helper()
	rig := &bootloaderRig{root: fakeRoot(t), m: &powerManager{}, role: auth.RoleAdmin}
	eepromtest.Tree(t, string(rig.root), revision, current, latest)
	bl := &system.Bootloader{Root: rig.root, Stage: func(context.Context) error {
		rig.staged++
		return rig.err
	}}
	rig.api = &SystemAPI{Root: rig.root, Manager: rig.m, Bootloader: bl}
	mux := http.NewServeMux()
	rig.api.Register(mux)
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: rig.role, Scopes: auth.RoleScopes(rig.role)})))
	}))
	t.Cleanup(rig.srv.Close)
	return rig
}

func (rig *bootloaderRig) req(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, rig.srv.URL+"/api/system/v1"+path, strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return res.StatusCode, m
}

// openccu-lite task 366: GET says what runs and what the image carries; nothing is offered when
// they are the same.
func TestBootloaderGet(t *testing.T) {
	rig := newBootloaderRig(t, eepromtest.Pi4Rev11, 1792057420, 1792057420)
	st, out := rig.req(t, "GET", "/hardware/bootloader", "")
	if st != 200 || out["available"] != true || out["chip"] != "2711" || out["current_date"] != "2026-10-15" || out["latest_date"] != "2026-10-15" || out["update"] != false || out["vl805"] != true {
		t.Fatalf("get: %d %v", st, out)
	}
	rig = newBootloaderRig(t, eepromtest.Pi5, 1792057420, 1792057420+86400*30)
	if _, out := rig.req(t, "GET", "/hardware/bootloader", ""); out["update"] != true || out["chip"] != "2712" || out["latest_date"] != "2026-11-14" {
		t.Fatalf("a newer image: %v", out)
	}
	rig = newBootloaderRig(t, eepromtest.Pi3, 0, 0)
	if _, out := rig.req(t, "GET", "/hardware/bootloader", ""); out["available"] != false || out["reason"] != "no-eeprom" {
		t.Fatalf("a Pi 3: %v", out)
	}
	none := &SystemAPI{Root: fakeRoot(t)}
	if v := none.bootloaderView(); v.Available || v.Reason != "no-eeprom" {
		t.Fatalf("no section: %+v", v)
	}
}

// POST /hardware/bootloader/update stages and reboots - only a newer image, or the same one with
// reinstall; never an older one, never with a system update set to install, never without
// confirm or for a user.
func TestBootloaderUpdate(t *testing.T) {
	const cur = 1792057420
	for _, c := range []struct {
		name   string
		role   auth.Role
		latest int64
		body   string
		armed  bool
		status int
		code   string
	}{
		{"newer", auth.RoleAdmin, cur + 1, `{"confirm":true}`, false, 200, ""},
		{"the same, reinstall", auth.RoleAdmin, cur, `{"confirm":true,"reinstall":true}`, false, 200, ""},
		{"the same", auth.RoleAdmin, cur, `{"confirm":true}`, false, 409, "up-to-date"},
		{"older", auth.RoleAdmin, cur - 1, `{"confirm":true,"reinstall":true}`, false, 409, "older"},
		{"no image", auth.RoleAdmin, 0, `{"confirm":true}`, false, 409, "no-image"},
		{"an update set to install", auth.RoleAdmin, cur + 1, `{"confirm":true}`, true, 409, "update-staged"},
		{"no confirm", auth.RoleAdmin, cur + 1, `{}`, false, 400, "confirm"},
		{"a user", auth.RoleUser, cur + 1, `{"confirm":true}`, false, 403, "forbidden"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newBootloaderRig(t, eepromtest.Pi4Rev11, cur, c.latest)
			rig.role = c.role
			if c.armed {
				root := string(rig.root)
				_ = os.MkdirAll(root+"/usr/local/tmp", 0o755)
				_ = os.WriteFile(root+"/usr/local/tmp/openccu-lite-aarch64-rpi4-1.0.0.zip", []byte("zip"), 0o644)
				if err := os.Symlink("/usr/local/tmp/openccu-lite-aarch64-rpi4-1.0.0.zip", root+"/usr/local/.firmwareUpdate"); err != nil {
					t.Fatal(err)
				}
				_ = os.WriteFile(root+"/usr/local/.recoveryMode", nil, 0o644)
			}
			st, out := rig.req(t, "POST", "/hardware/bootloader/update", c.body)
			if st != c.status || (c.code != "" && out["error"] != c.code) {
				t.Fatalf("got %d %v, want %d %q", st, out, c.status, c.code)
			}
			want := 0
			if c.status == 200 {
				want = 1
			}
			if rig.staged != want || rig.m.Reboots() != want {
				t.Errorf("staged %d, reboots %d, want %d", rig.staged, rig.m.Reboots(), want)
			}
		})
	}
	// a staging that fails does not reboot; its error is in the view
	rig := newBootloaderRig(t, eepromtest.Pi4Rev11, cur, cur+1)
	rig.err = priv.ErrBootloaderOlder
	if st, out := rig.req(t, "POST", "/hardware/bootloader/update", `{"confirm":true}`); st != 409 || rig.m.Reboots() != 0 {
		t.Fatalf("the helper's refusal: %d %v", st, out)
	}
	rig.err = io.ErrUnexpectedEOF
	if st, _ := rig.req(t, "POST", "/hardware/bootloader/update", `{"confirm":true}`); st != 500 || rig.m.Reboots() != 0 {
		t.Fatalf("a failed staging: %d", st)
	}
	if _, out := rig.req(t, "GET", "/hardware/bootloader", ""); out["error"] == nil {
		t.Errorf("the failure is not in the view: %v", out)
	}
	// no bootloader section at all
	plain := &SystemAPI{Root: fakeRoot(t), Manager: &powerManager{}}
	mux := http.NewServeMux()
	plain.Register(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/system/v1/hardware/bootloader/update", strings.NewReader(`{"confirm":true}`))
	mux.ServeHTTP(rec, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{Role: auth.RoleAdmin, Scopes: auth.RoleScopes(auth.RoleAdmin)})))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no section: %d", rec.Code)
	}
}

// The Status page's notice: info, only while the image's bootloader is newer than the running one.
func TestBootloaderWarning(t *testing.T) {
	const cur = 1792057420
	for _, c := range []struct {
		name    string
		rev     string
		latest  int64
		active  bool
		variant string
	}{
		{"newer", eepromtest.Pi4Rev11, cur + 86400, true, "2026-10-16"},
		{"the same", eepromtest.Pi4Rev11, cur, false, ""},
		{"older", eepromtest.Pi4Rev11, cur - 86400, false, ""},
		{"no image", eepromtest.Pi4Rev11, 0, false, ""},
		{"a Pi 5, newer", eepromtest.Pi5, cur + 86400, true, "2026-10-16"},
		{"a Pi 3", eepromtest.Pi3, 0, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newBootloaderRig(t, c.rev, cur, c.latest)
			list, ok := rig.api.bootloaderWarning(context.Background())
			if !ok || (len(list) == 1) != c.active {
				t.Fatalf("%v %v", list, ok)
			}
			if c.active {
				w := list[0]
				if w.ID != "bootloader-update" || w.Severity != warnings.SeverityInfo || w.Variant != c.variant || w.Href != "/system/hardware#bootloader" || w.Params["running"] != "2026-10-15" {
					t.Errorf("warning %+v", w)
				}
			}
		})
	}
	if list, ok := (&SystemAPI{}).bootloaderWarning(context.Background()); !ok || list != nil {
		t.Errorf("no section: %v %v", list, ok)
	}
}
