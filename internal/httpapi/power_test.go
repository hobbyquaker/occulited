package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/system"
)

// powerManager counts the reboots the routes ask for; a box would be gone after the first. Its
// fields are read and changed under its mutex (B-208): a handler whose client gave up still calls
// Reboot on its own goroutine while the test polls the count.
type powerManager struct {
	mu      sync.Mutex
	reboots int
	err     error
}

// Reboots is the count so far.
func (m *powerManager) Reboots() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reboots
}

// setErr is what the next reboots answer.
func (m *powerManager) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *powerManager) Install(context.Context, io.Reader) (*system.InstallResult, error) {
	return nil, errors.New("not here")
}
func (m *powerManager) Uninstall(context.Context, string) (system.UninstallResult, error) {
	return system.UninstallResult{}, nil
}
func (m *powerManager) CheckUpdate(context.Context, system.Addon, string) system.UpdateInfo {
	return system.UpdateInfo{}
}
func (m *powerManager) Reboot(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reboots++
	return m.err
}

type powerRig struct {
	root system.Root
	srv  *httptest.Server
	role auth.Role
	// scopes replaces the role's when set
	scopes []auth.Scope
	m      *powerManager
	ran    [][]string
	power  *system.Power
}

func newPowerRig(t *testing.T, systemd bool) *powerRig {
	t.Helper()
	rig := &powerRig{root: fakeRoot(t), role: auth.RoleAdmin, m: &powerManager{}}
	rig.power = &system.Power{Root: rig.root, Systemd: systemd,
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			rig.ran = append(rig.ran, append([]string{name}, args...))
			return nil, nil
		},
		Later: func(f func()) { f() },
	}
	api := &SystemAPI{Root: rig.root, Manager: rig.m, Power: rig.power}
	mux := http.NewServeMux()
	api.Register(mux)
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		scopes := rig.scopes
		if scopes == nil {
			scopes = auth.RoleScopes(rig.role)
		}
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: rig.role, Scopes: scopes})))
	}))
	t.Cleanup(rig.srv.Close)
	return rig
}

func (rig *powerRig) post(t *testing.T, path, body string) (int, apiError, map[string]any) {
	t.Helper()
	res, err := http.Post(rig.srv.URL+"/api/system/v1"+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var e apiError
	var m map[string]any
	_ = json.Unmarshal(raw, &e)
	_ = json.Unmarshal(raw, &m)
	return res.StatusCode, e, m
}

func (rig *powerRig) armed() bool {
	_, err := os.Stat(string(rig.root) + "/usr/local/.recoveryMode")
	return err == nil
}

// The three routes refuse a user session and a request without {"confirm": true}, and nothing
// happens when they refuse.
func TestPowerRoutesRefuse(t *testing.T) {
	cases := []struct {
		name   string
		role   auth.Role
		body   string
		status int
		code   string
	}{
		{"a user session", auth.RoleUser, `{"confirm":true}`, 403, "forbidden"},
		{"no body", auth.RoleAdmin, ``, 400, "confirm"},
		{"an empty object", auth.RoleAdmin, `{}`, 400, "confirm"},
		{"confirm false", auth.RoleAdmin, `{"confirm":false}`, 400, "confirm"},
		{"not JSON", auth.RoleAdmin, `yes please`, 422, "invalid-body"},
	}
	for _, path := range []string{"/reboot", "/reboot/recovery", "/halt"} {
		for _, c := range cases {
			t.Run(path+" "+c.name, func(t *testing.T) {
				rig := newPowerRig(t, true)
				rig.role = c.role
				st, e, _ := rig.post(t, path, c.body)
				if st != c.status || (c.code != "" && e.Error != c.code) {
					t.Fatalf("got %d %q, want %d %q", st, e.Error, c.status, c.code)
				}
				if rig.m.Reboots() != 0 || rig.ran != nil || rig.armed() {
					t.Fatalf("something happened: reboots %d, ran %v, armed %v", rig.m.Reboots(), rig.ran, rig.armed())
				}
			})
		}
	}
}

func TestRebootRoute(t *testing.T) {
	rig := newPowerRig(t, true)
	st, _, out := rig.post(t, "/reboot", `{"confirm":true}`)
	if st != 200 || out["ok"] != true || rig.m.Reboots() != 1 || rig.armed() {
		t.Fatalf("reboot: %d %v, reboots %d, armed %v", st, out, rig.m.Reboots(), rig.armed())
	}
}

func TestHaltRoute(t *testing.T) {
	for _, c := range []struct {
		name    string
		systemd bool
		want    func(root string) []string
	}{
		{"systemd", true, func(string) []string { return []string{"systemctl", "poweroff"} }},
		{"busybox", false, func(root string) []string { return []string{root + "/sbin/poweroff"} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newPowerRig(t, c.systemd)
			_ = os.MkdirAll(string(rig.root)+"/sbin", 0o755)
			_ = os.WriteFile(string(rig.root)+"/sbin/poweroff", []byte("#!/bin/sh\n"), 0o755)
			st, _, out := rig.post(t, "/halt", `{"confirm":true}`)
			if st != 200 || out["halting"] != true {
				t.Fatalf("halt: %d %v", st, out)
			}
			if len(rig.ran) != 1 || !reflect.DeepEqual(rig.ran[0], c.want(string(rig.root))) {
				t.Fatalf("ran %v", rig.ran)
			}
			if rig.m.Reboots() != 0 {
				t.Error("a halt is not a reboot")
			}
		})
	}
	// a busybox box without poweroff says so instead of answering ok
	rig := newPowerRig(t, false)
	if st, _, _ := rig.post(t, "/halt", `{"confirm":true}`); st == 200 || rig.ran != nil {
		t.Fatalf("no poweroff: %d, ran %v", st, rig.ran)
	}
	// and a daemon without power control
	rig = newPowerRig(t, true)
	api := &SystemAPI{Root: rig.root}
	mux := http.NewServeMux()
	api.Register(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/system/v1/halt", strings.NewReader(`{"confirm":true}`))
	mux.ServeHTTP(rec, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{Role: auth.RoleAdmin, Scopes: auth.RoleScopes(auth.RoleAdmin)})))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no power control: %d", rec.Code)
	}
}

func TestRebootRecoveryRoute(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(rig *powerRig)
		status  int
		code    string
		armed   bool
		reboots int
	}{
		{"armed, then the reboot", func(*powerRig) {}, 200, "", true, 1},
		{"an update is staged", func(rig *powerRig) {
			root := string(rig.root)
			_ = os.MkdirAll(root+"/usr/local/tmp", 0o755)
			_ = os.WriteFile(root+"/usr/local/tmp/openccu-lite-x86_64-ova-1.0.0.zip", []byte("zip"), 0o644)
			_ = os.Symlink(root+"/usr/local/tmp/openccu-lite-x86_64-ova-1.0.0.zip", root+"/usr/local/.firmwareUpdate")
		}, 409, "update-staged", false, 0},
		{"a container has none", func(rig *powerRig) {
			_ = os.MkdirAll(string(rig.root)+"/run/systemd", 0o755)
			_ = os.WriteFile(string(rig.root)+"/run/systemd/container", []byte("lxc\n"), 0o644)
		}, 501, "not-available", false, 0},
		{"the reboot fails: the marker goes again", func(rig *powerRig) { rig.m.setErr(errors.New("no reboot command")) }, 500, "", false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newPowerRig(t, true)
			c.setup(rig)
			st, e, out := rig.post(t, "/reboot/recovery", `{"confirm":true}`)
			if st != c.status || (c.code != "" && e.Error != c.code) {
				t.Fatalf("got %d %q %v, want %d %q", st, e.Error, out, c.status, c.code)
			}
			if rig.armed() != c.armed || rig.m.Reboots() != c.reboots {
				t.Fatalf("armed %v, reboots %d; want %v, %d", rig.armed(), rig.m.Reboots(), c.armed, c.reboots)
			}
			if c.status == 200 && (out["recovery"] != true || out["rebooting"] != true) {
				t.Fatalf("answer %v", out)
			}
			if c.code == "update-staged" && e.Detail["file"] != "openccu-lite-x86_64-ova-1.0.0.zip" {
				t.Errorf("the staged file is not named: %v", e.Detail)
			}
		})
	}
}

// occulited task 132: power:halt powers off and does nothing else power does.
func TestPowerHaltScope(t *testing.T) {
	rig := newPowerRig(t, true)
	rig.scopes = []auth.Scope{auth.ScopePowerHalt}
	if st, _, _ := rig.post(t, "/reboot", `{"confirm":true}`); st != 403 || rig.m.Reboots() != 0 {
		t.Fatalf("reboot: %d", st)
	}
	if st, _, _ := rig.post(t, "/reboot/recovery", `{"confirm":true}`); st != 403 {
		t.Fatalf("recovery: %d", st)
	}
	if st, e, _ := rig.post(t, "/halt", `{"confirm":true}`); st != 200 {
		t.Fatalf("halt: %d %v", st, e)
	}
}
