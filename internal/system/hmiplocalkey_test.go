package system

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/radio"
)

const lkSGTIN = "3014F711A0001F0000000A03" // the Charly's RPI-RF-MOD in charlyRoot

// fakeHmIPServer answers listDevices with two devices, their UNREACH as set ("ok", "unreach", or
// "unknown": fault -5, not heard from), and getInstallMode. after is the state from hmipserver's
// next restart on (a wrong key: the devices go silent).
type fakeHmIPServer struct {
	mu      sync.Mutex
	state   string
	after   string
	install int
}

func (f *fakeHmIPServer) restarted() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.after != "" {
		f.state = f.after
	}
}

// restartHook tells the fake hmipserver about its restart.
type restartHook struct {
	*connSvc
	fake *fakeHmIPServer
}

func (r restartHook) Control(ctx context.Context, id, action string) (string, error) {
	if id == "hmipserver" && action == "restart" {
		r.fake.restarted()
	}
	return r.connSvc.Control(ctx, id, action)
}

func (f *fakeHmIPServer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		f.mu.Lock()
		state, install := f.state, f.install
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		switch {
		case strings.Contains(body, "<methodName>listDevices</methodName>"):
			dev := func(a, typ string) string {
				return `<value><struct><member><name>ADDRESS</name><value><string>` + a + `</string></value></member><member><name>TYPE</name><value><string>` + typ + `</string></value></member><member><name>PARENT</name><value><string></string></value></member></struct></value>`
			}
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>` + dev("000A1B2C3D4E5F", "HmIP-PDT") + dev("000B1B2C3D4E5F", "HmIP-eTRV-2") + dev("000D1B2C3D4E5F", "HmIPW-DRAP") + dev("0000000000000A", "HmIP-RCV-1") + `</data></array></value></param></params></methodResponse>`))
		case strings.Contains(body, "UNREACH") && strings.Contains(body, "000D1B2C3D4E5F"):
			// the DRAP answers over the LAN whatever the radio key (the Charly, 2026-09-18)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><boolean>0</boolean></value></param></params></methodResponse>`))
		case strings.Contains(body, "UNREACH") && state == "unknown":
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><i4>-5</i4></value></member><member><name>faultString</name><value>Unknown Parameter value for value key: UNREACH</value></member></struct></value></fault></methodResponse>`))
		case strings.Contains(body, "UNREACH"):
			v := "0"
			if state == "unreach" {
				v = "1"
			}
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><boolean>` + v + `</boolean></value></param></params></methodResponse>`))
		case strings.Contains(body, "getInstallMode"):
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><i4>` + strconv.Itoa(install) + `</i4></value></param></params></methodResponse>`))
		default:
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><int>-1</int></value></member><member><name>faultString</name><value><string>unknown</string></value></member></struct></value></fault></methodResponse>`))
		}
	}))
	t.Cleanup(srv.Close)
	fakes.Store(srv, f)
	return srv
}

var fakes sync.Map // *httptest.Server -> *fakeHmIPServer

func fakeFor(srv *httptest.Server) *fakeHmIPServer {
	if srv == nil {
		return &fakeHmIPServer{}
	}
	f, _ := fakes.Load(srv)
	return f.(*fakeHmIPServer)
}

func lkRig(t *testing.T, hmip *httptest.Server) (*HmIPLocalKey, *connSvc, string) {
	t.Helper()
	root := charlyRoot(t)
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o664); err != nil {
			t.Fatal(err)
		}
	}
	for _, ext := range []string{".ap", ".apkx", ".bbkx"} {
		w("etc/config/crRFD/data/"+lkSGTIN+ext, "EQ3-"+ext)
	}
	w("etc/config/crRFD/hmip_user.conf", "occulite.hmip.path=direct\n")
	w("etc/config/hmip_address.conf", "adapter.address=0x3F0A15\n")
	svc := &connSvc{fakeRadioSvc: fakeRadioSvc{root: root, running: map[string]bool{"hmipserver": true}}}
	k := &HmIPLocalKey{
		Root: Root(root), Services: restartHook{svc, fakeFor(hmip)}, StateDir: filepath.Join(root, "state/hmip-local-key"),
		Plan: func() (radio.Plan, bool) {
			var p radio.Plan
			b, err := os.ReadFile(filepath.Join(root, "run/occulite/radio/plan.json"))
			return p, err == nil && json.Unmarshal(b, &p) == nil
		},
		HmIP: func() (interfaces.Interface, bool) {
			if hmip == nil {
				return interfaces.Interface{}, false
			}
			ifs := interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(hmip.URL, "http://")}})
			return ifs[0], true
		},
		CheckWait: 200 * time.Millisecond, CheckFor: 150 * time.Millisecond, CheckEvery: 20 * time.Millisecond,
	}
	return k, svc, root
}

func lkWait(t *testing.T, k *HmIPLocalKey) LocalKeyStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := k.Status(); st.Switching == "" {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the switch did not finish")
	return LocalKeyStatus{}
}

func lkCheck(t *testing.T, k *HmIPLocalKey, want string) *LocalKeyCheck {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c := k.Status().Check; c != nil && c.State == want {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("check: %+v, want %s", k.Status().Check, want)
	return nil
}

// lkDone waits for the check after a switch to reach its final state: until then its goroutine
// writes state.json, and a test that ends earlier races its TempDir's cleanup
func lkDone(t *testing.T, k *HmIPLocalKey) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c := k.Status().Check; c != nil && !checkOpen(c.State) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the check did not end: %+v", k.Status().Check)
}

// task 149: generate, snapshot, write, restart; the override; the revert from the snapshot
func TestLocalKeyGenerateOverrideRevert(t *testing.T) {
	fake := &fakeHmIPServer{state: "ok"}
	k, svc, root := lkRig(t, fake.serve(t))
	st := k.Status()
	if !st.Available || st.Enabled || st.SGTIN != lkSGTIN || st.KeyServerMode != radio.KeyServerLocalFallback || st.RevertBlocked == "" {
		t.Fatalf("before: %+v", st)
	}
	if err := k.Enable("generate", "", ""); err != nil {
		t.Fatal(err)
	}
	st = lkWait(t, k)
	conf := readFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"))
	key := radio.ReadLocalKey(conf)
	if st.Error != "" || !st.Enabled || st.Source != "generated" || len(key.NetworkKey) != 32 || len(key.BackboneKey) != 32 || key.NetworkKey == key.BackboneKey || key.KeyServerMode != radio.KeyServerLocal {
		t.Fatalf("on: %+v\n%s", st, conf)
	}
	if !strings.Contains(conf, "occulite.hmip.path=direct") {
		t.Fatalf("the choice went:\n%s", conf)
	}
	if fi, _ := os.Stat(filepath.Join(root, "etc/config/crRFD/hmip_user.conf")); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640 with a key in it", fi.Mode().Perm())
	}
	if !strings.Contains(strings.Join(svc.calls, " "), "hmipserver restart") {
		t.Errorf("no restart: %v", svc.calls)
	}
	if len(st.Snapshots) != 1 || st.Snapshots[0].SGTIN != lkSGTIN || len(st.Snapshots[0].Files) != 3 || st.RevertBlocked != "" {
		t.Fatalf("snapshot: %+v blocked %q", st.Snapshots, st.RevertBlocked)
	}
	if fi, _ := os.Stat(filepath.Join(k.StateDir, "snapshots", lkSGTIN, lkSGTIN+".apkx")); fi.Mode().Perm() != 0o600 {
		t.Errorf("snapshot file mode %v", fi.Mode().Perm())
	}
	// the devices answer: the check ends ok
	if c := lkCheck(t, k, "ok"); c.Before != 3 || c.Total != 2 || c.Heard != 2 {
		t.Errorf("check: %+v", c)
	}
	if err := k.Enable("generate", "", ""); err == nil {
		t.Error("on twice")
	}

	// the override for one pairing: KEYSERVER_LOCAL, and LOCAL again when it is switched off
	if err := k.Override(true); err != nil {
		t.Fatal(err)
	}
	if st = lkWait(t, k); !st.OverrideActive || radio.ReadLocalKey(readFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"))).KeyServerMode != radio.KeyServerLocalFallback {
		t.Fatalf("override on: %+v", st)
	}
	if err := k.Override(false); err != nil {
		t.Fatal(err)
	}
	if st = lkWait(t, k); st.OverrideActive || radio.ReadLocalKey(readFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"))).KeyServerMode != radio.KeyServerLocal {
		t.Fatalf("override off: %+v", st)
	}

	// hmipserver rewrote the access point since; a choice was changed since - the revert puts the
	// identity back and takes the key lines out, the choice stays
	_ = os.WriteFile(filepath.Join(root, "etc/config/crRFD/data/"+lkSGTIN+".ap"), []byte("CHANGED"), 0o664)
	conf = strings.Replace(readFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf")), "path=direct", "path=multimacd", 1)
	_ = os.WriteFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"), []byte(conf), 0o640)
	if err := k.Disable(); err != nil {
		t.Fatal(err)
	}
	st = lkWait(t, k)
	conf = readFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"))
	if st.Error != "" || st.Enabled || st.Check != nil || radio.ReadLocalKey(conf).KeyServerMode != "" || !strings.Contains(conf, "path=multimacd") {
		t.Fatalf("off: %+v\n%s", st, conf)
	}
	if b := readFile(filepath.Join(root, "etc/config/crRFD/data/"+lkSGTIN+".ap")); b != "EQ3-.ap" {
		t.Errorf("the .ap is %q", b)
	}
	// task 317 (D-120): the files the revert replaced were copied first
	if got := identityBackup(t, k, "local-key-off"); got[lkSGTIN+".ap"] != "CHANGED" || len(got) != 3 {
		t.Errorf("the copy before the revert: %v", got)
	}
	if fi, _ := os.Stat(filepath.Join(root, "etc/config/crRFD/hmip_user.conf")); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v after the revert", fi.Mode().Perm())
	}
	// the snapshot is kept until it is discarded
	if len(st.Snapshots) != 1 {
		t.Fatalf("snapshots after the revert: %+v", st.Snapshots)
	}
	if err := k.DiscardSnapshot(lkSGTIN); err != nil || len(k.Status().Snapshots) != 0 {
		t.Fatalf("discard: %v", err)
	}
}

// a known key: validated, the backbone key left out when not given; a wrong one goes silent and
// the check says so; another module blocks the revert
func TestLocalKeyKnownCheckAndOtherModule(t *testing.T) {
	fake := &fakeHmIPServer{state: "ok", after: "unreach"}
	k, _, root := lkRig(t, fake.serve(t))
	var e ErrLocalKey
	if err := k.Enable("known", "0102030405060708090A0B0C0D0E0F10", ""); !errors.As(err, &e) {
		t.Fatalf("the demo key: %v", err)
	}
	if err := k.Enable("known", "0011", ""); !errors.As(err, &e) {
		t.Fatalf("a short key: %v", err)
	}
	// the entered key is not the network's: after the restart the devices do not answer
	if err := k.Enable("known", "00112233445566778899aabbccddeeff", ""); err != nil {
		t.Fatal(err)
	}
	st := lkWait(t, k)
	key := radio.ReadLocalKey(readFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf")))
	if st.Source != "entered" || key.NetworkKey != "00112233445566778899AABBCCDDEEFF" || key.BackboneKey != "" {
		t.Fatalf("known: %+v %+v", st, key)
	}
	if c := lkCheck(t, k, "failed"); len(c.Unreachable) != 2 || k.CheckFailed() == nil {
		t.Fatalf("check: %+v", c)
	}
	// the radio was swapped since: the snapshot is another module's, the revert is blocked
	plan := filepath.Join(root, "run/occulite/radio/plan.json")
	b, _ := os.ReadFile(plan)
	_ = os.WriteFile(plan, []byte(strings.ReplaceAll(string(b), lkSGTIN, "3014F711A000040000000A02")), 0o644)
	if st = k.Status(); !strings.Contains(st.RevertBlocked, "another radio module") || k.Disable() == nil {
		t.Fatalf("blocked: %q", st.RevertBlocked)
	}
}

// a key set by hand is shown as such; no revert without a snapshot; accesspoint.exchange.id and a
// box without an HmIP module refuse the switch; no device before the switch skips the check
func TestLocalKeyManualAndRefusals(t *testing.T) {
	k, _, root := lkRig(t, nil)
	_ = os.WriteFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"), []byte("Network.Key=00112233445566778899AABBCCDDEEFF\n"), 0o644)
	st := k.Status()
	if !st.Enabled || st.Source != "manual" || !strings.Contains(st.RevertBlocked, "by hand") || st.Snapshots == nil {
		t.Fatalf("manual: %+v", st)
	}
	if err := k.Enable("generate", "", ""); err == nil {
		t.Fatal("on over a hand-set key")
	}
	_ = os.WriteFile(filepath.Join(root, "etc/config/crRFD/hmip_user.conf"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(root, "etc/config/hmip_address.conf"), []byte("accesspoint.exchange.id=1\n"), 0o644)
	if err := k.Enable("generate", "", ""); err == nil || !strings.Contains(err.Error(), "exchange") {
		t.Fatalf("exchange id: %v", err)
	}
	_ = os.WriteFile(filepath.Join(root, "etc/config/hmip_address.conf"), []byte(""), 0o644)
	// no hmipserver to ask: the device count is unknown, the check waits and gives up
	if err := k.Enable("generate", "", ""); err != nil {
		t.Fatal(err)
	}
	lkWait(t, k)
	if c := lkCheck(t, k, "error"); c.Before != -1 {
		t.Fatalf("check: %+v", c)
	}
	k.Plan = func() (radio.Plan, bool) { return radio.Plan{}, false }
	if st := k.Status(); st.Available {
		t.Fatal("available without a module")
	}
}

// a wrong key leaves the devices unheard rather than unreachable: none heard from again fails the
// check, although the DRAP keeps answering (it is not watched); devices that never answered before
// the switch (a drawer full of them) are not watched
func TestLocalKeyCheckQuietAndUnheard(t *testing.T) {
	fake := &fakeHmIPServer{state: "ok", after: "unknown"}
	k, _, _ := lkRig(t, fake.serve(t))
	if err := k.Enable("generate", "", ""); err != nil {
		t.Fatal(err)
	}
	lkWait(t, k)
	if c := lkCheck(t, k, "failed"); c.Total != 2 || c.Heard != 0 || len(c.Quiet) != 2 || len(c.Unreachable) != 0 {
		t.Fatalf("quiet: %+v", c)
	}

	idle := &fakeHmIPServer{state: "unknown"}
	k2, _, _ := lkRig(t, idle.serve(t))
	if n := k2.DeviceCount(t.Context()); n != 3 {
		t.Fatalf("device count %d", n)
	}
	if err := k2.Enable("generate", "", ""); err != nil {
		t.Fatal(err)
	}
	lkWait(t, k2)
	if c := lkCheck(t, k2, "skipped"); c.Before != 3 || c.Total != 0 {
		t.Fatalf("unheard before: %+v", c)
	}
}

// task 192: the pairing fact from the two files as they are on disk - no files is the shipped
// template's mode with no keys; LOCAL with a map counts the keys and rules the key server out
func TestHmIPPairing(t *testing.T) {
	root := t.TempDir()
	if got := HmIPPairing(Root(root)); got != (radio.Pairing{KeyServerMode: radio.KeyServerLocalFallback, OfflinePairing: true}) {
		t.Fatalf("bare root: %+v", got)
	}
	w := func(p, c string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o664); err != nil {
			t.Fatal(err)
		}
	}
	w("etc/config/crRFD/hmip_user.conf", radio.SetLocalKey("occulite.hmip.path=direct\n", "00112233445566778899AABBCCDDEEFF", ""))
	w("etc/config/crRFD/sgtin.map", radio.FormatDeviceKeyMap([]radio.DeviceKey{{SGTIN: "3014F711A0000B00000000DF", Key: "00112233445566778899AABBCCDDEEFF"}, {SGTIN: "3014F711A0000B00000000E0", Key: "FFEEDDCCBBAA99887766554433221100"}}))
	if got := HmIPPairing(Root(root)); got != (radio.Pairing{KeyServerMode: radio.KeyServerLocal, DeviceKeys: 2}) {
		t.Fatalf("LOCAL with two keys: %+v", got)
	}
	w("etc/config/crRFD/hmip_user.conf", "KeyServer.Mode=KEYSERVER\n")
	if got := HmIPPairing(Root(root)); got != (radio.Pairing{KeyServerMode: radio.KeyServerKeyServerOnly, DeviceKeys: 2, OfflinePairing: true}) {
		t.Fatalf("KEYSERVER: %+v", got)
	}
}

// gateSvc holds every hmipserver restart until the test lets one through (a token on gate), so
// two callers can be started while one restart is in flight.
type gateSvc struct {
	ServiceManager
	gate chan struct{}
}

func (g gateSvc) Control(ctx context.Context, id, action string) (string, error) {
	if id == "hmipserver" && action == "restart" {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return g.ServiceManager.Control(ctx, id, action)
}

func dkWait(t *testing.T, d *HmIPDeviceKeys) DeviceKeysView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v := d.View(context.Background()); !v.Applying {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the apply did not finish")
	return DeviceKeysView{}
}

// B-198: local key mode and the device keys' apply share one restart lock, so whichever rewrites
// hmipserver's configuration and restarts it first refuses the other (with its name), in both
// orders; the check after a switch holds nothing
func TestLocalKeyAndDeviceKeysApplyExclusive(t *testing.T) {
	fake := &fakeHmIPServer{state: "ok"}
	k, svc, root := lkRig(t, fake.serve(t))
	lock := &HmIPRestartLock{}
	gate := make(chan struct{})
	k.Services, k.Restarts = gateSvc{k.Services, gate}, lock
	d := &HmIPDeviceKeys{Root: Root(root), Services: gateSvc{svc, gate}, StateDir: filepath.Join(root, "state/hmip-device-keys"), Restarts: lock}
	var lk ErrLocalKey
	var dk ErrDeviceKeys

	// the apply first: hmipserver is restarting for it, and the switch is refused naming it
	if err := d.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := k.Enable("generate", "", ""); !errors.As(err, &lk) || !strings.Contains(err.Error(), "device keys' apply is running") {
		t.Fatalf("switch during the apply: %v", err)
	}
	if err := d.Apply(); !errors.As(err, &dk) {
		t.Fatalf("apply twice: %v", err)
	}
	if lock.Holder() != "the device keys' apply" {
		t.Fatalf("holder %q", lock.Holder())
	}
	gate <- struct{}{}
	if v := dkWait(t, d); v.Error != "" {
		t.Fatalf("apply: %+v", v)
	}
	if lock.Holder() != "" {
		t.Fatalf("held after the apply: %q", lock.Holder())
	}

	// the switch first: the apply is refused naming it, until the restart is through
	if err := k.Enable("generate", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(); !errors.As(err, &dk) || !strings.Contains(err.Error(), "local key switch is running") {
		t.Fatalf("apply during the switch: %v", err)
	}
	if err := k.Override(true); !errors.As(err, &lk) {
		t.Fatalf("override during the switch: %v", err)
	}
	gate <- struct{}{}
	if st := lkWait(t, k); st.Error != "" || !st.Enabled {
		t.Fatalf("on: %+v", st)
	}
	// the check after the switch runs without the lock: the apply may restart hmipserver now
	if err := d.Apply(); err != nil {
		t.Fatalf("apply after the switch: %v", err)
	}
	gate <- struct{}{}
	dkWait(t, d)
	if n := strings.Count(strings.Join(svc.calls, " "), "hmipserver restart"); n != 3 {
		t.Fatalf("%d restarts: %v", n, svc.calls)
	}
	lkDone(t, k)
}

// B-197: the override or a switch during the check ends it with a final state at once, and the
// cut-off check writes nothing more; the state survives occulited's next start
func TestLocalKeyCheckSuperseded(t *testing.T) {
	fake := &fakeHmIPServer{state: "ok"}
	k, _, _ := lkRig(t, fake.serve(t))
	k.CheckFor = 10 * time.Second // the check outlasts the test
	if err := k.Enable("generate", "", ""); err != nil {
		t.Fatal(err)
	}
	lkWait(t, k)
	lkCheck(t, k, "running")
	if err := k.Override(true); err != nil {
		t.Fatal(err)
	}
	// final before the override's answer, not at the next start
	if c := k.Status().Check; c == nil || c.State != "superseded" || c.Finished.IsZero() {
		t.Fatalf("check right after the override: %+v", c)
	}
	st := lkWait(t, k)
	time.Sleep(60 * time.Millisecond) // the cut-off check's ticks
	if c := k.Status().Check; c.State != "superseded" || !st.OverrideActive {
		t.Fatalf("check after the override: %+v (override %v)", c, st.OverrideActive)
	}
	k.Load()
	if c := k.Status().Check; c.State != "superseded" {
		t.Fatalf("after Load: %+v", c)
	}

	// the revert during the check, failing before it clears the state: the check is superseded,
	// not left running
	k2, _, _ := lkRig(t, fake.serve(t))
	k2.CheckFor = 10 * time.Second
	if err := k2.Enable("generate", "", ""); err != nil {
		t.Fatal(err)
	}
	lkWait(t, k2)
	lkCheck(t, k2, "running")
	if err := os.Remove(filepath.Join(k2.StateDir, "snapshots", lkSGTIN, lkSGTIN+".ap")); err != nil {
		t.Fatal(err)
	}
	if err := k2.Disable(); err != nil {
		t.Fatal(err)
	}
	if st := lkWait(t, k2); !strings.Contains(st.Error, "reading the snapshot") || st.Check == nil || st.Check.State != "superseded" {
		t.Fatalf("revert during the check: %+v", st)
	}

	// a check cut off by occulited's restart is still interrupted
	k3, _, _ := lkRig(t, fake.serve(t))
	k3.update(func(st *localKeyState) { st.Check = &LocalKeyCheck{Started: k3.now(), State: "running"} })
	k3.Load()
	if c := k3.Status().Check; c.State != "interrupted" || c.Finished.IsZero() {
		t.Fatalf("after a restart: %+v", c)
	}
}

// openccu-lite B-253: hmipserver's data directory is 0700 and its files 0600 - the daemon lists it
// and reads the module's identity files through the helper, and the snapshot, the exchange view and
// the revert work as before; what the revert puts back is 0600.
func TestLocalKeyThroughClosedDataDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a closed directory cannot be produced")
	}
	fake := &fakeHmIPServer{state: "ok"}
	k, _, root := lkRig(t, fake.serve(t))
	data := filepath.Join(root, "etc/config/crRFD/data")
	for _, ext := range []string{".ap", ".apkx", ".bbkx"} {
		if err := os.Chmod(filepath.Join(data, lkSGTIN+ext), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(data, exPrevSGTIN+".ap"), []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(data, 0o755) })
	old := Priv
	Priv = rootReader{}
	t.Cleanup(func() { Priv = old })
	if v := k.Exchange(); len(v.Previous) != 1 || v.Previous[0] != exPrevSGTIN {
		t.Fatalf("the previous identity through the helper: %+v", v)
	}
	if err := k.Enable("generate", "", ""); err != nil {
		t.Fatal(err)
	}
	st := lkWait(t, k)
	if st.Error != "" || !st.Enabled || len(st.Snapshots) != 1 || len(st.Snapshots[0].Files) != 3 {
		t.Fatalf("on through a closed directory: %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(k.StateDir, "snapshots", lkSGTIN, lkSGTIN+".apkx")); string(b) != "EQ3-.apkx" {
		t.Errorf("the snapshot's copy: %q", b)
	}
	lkDone(t, k)
	// the revert writes as root through the helper (the fake cannot): the directory open again
	if err := os.Chmod(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(data, lkSGTIN+".ap")); err != nil {
		t.Fatal(err)
	}
	if err := k.Disable(); err != nil {
		t.Fatal(err)
	}
	if st = lkWait(t, k); st.Error != "" || st.Enabled {
		t.Fatalf("off: %+v", st)
	}
	if fi, err := os.Stat(filepath.Join(data, lkSGTIN+".ap")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the restored identity file: %v %v, want 0600", err, fi)
	}
}

// identityBackup reads the newest copy of the identity files the reason made (task 317).
func identityBackup(t *testing.T, k *HmIPLocalKey, reason string) map[string]string {
	t.Helper()
	base := filepath.Join(k.StateDir, identityBackupDir)
	entries, _ := os.ReadDir(base)
	for i := len(entries) - 1; i >= 0; i-- {
		if !strings.HasSuffix(entries[i].Name(), "-"+reason) {
			continue
		}
		if fi, _ := os.Stat(filepath.Join(base, entries[i].Name())); fi.Mode().Perm() != 0o700 {
			t.Errorf("copy directory mode %v", fi.Mode().Perm())
		}
		out := map[string]string{}
		files, _ := os.ReadDir(filepath.Join(base, entries[i].Name()))
		for _, f := range files {
			out[f.Name()] = readFile(filepath.Join(base, entries[i].Name(), f.Name()))
		}
		return out
	}
	return nil
}

// task 317 (D-120): the copy takes every module's identity files and nothing else, changes
// nothing in the data directory, keeps the newest IdentityBackupsKept, and refuses an unreadable
// file; without identity files there is nothing to copy.
func TestBackupIdentity(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "etc/config/crRFD/data")
	_ = os.MkdirAll(data, 0o755)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	k := &HmIPLocalKey{Root: Root(root), StateDir: filepath.Join(root, "state"), Now: func() time.Time { now = now.Add(time.Second); return now }}
	if dir, err := k.backupIdentity("empty"); err != nil || dir != "" {
		t.Fatalf("nothing to copy: %q %v", dir, err)
	}
	for name, body := range map[string]string{"3014F711A0001F0000000A03.ap": "A", "3014F711A0001F0000000A03.apkx": "K", "3014F711A0001F0000000A03.bbkx": "B", "3014F711A000040000000A01.ap": "S", "3014F711A0000000000000B1.dev": "D", "metaData.conf": "M"} {
		_ = os.WriteFile(filepath.Join(data, name), []byte(body), 0o600)
	}
	dir, err := k.backupIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	got := identityBackup(t, k, "test")
	if len(got) != 4 || got["3014F711A0001F0000000A03.ap"] != "A" || got["3014F711A000040000000A01.ap"] != "S" || got["3014F711A0001F0000000A03.bbkx"] != "B" {
		t.Fatalf("copied %v into %s", got, dir)
	}
	if readFile(filepath.Join(data, "3014F711A0001F0000000A03.ap")) != "A" {
		t.Error("the data directory changed")
	}
	for i := 0; i < IdentityBackupsKept+3; i++ {
		if _, err := k.backupIdentity("again"); err != nil {
			t.Fatal(err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(k.StateDir, identityBackupDir)); len(entries) != IdentityBackupsKept || strings.HasSuffix(entries[0].Name(), "-test") {
		t.Errorf("kept %d, the oldest %v", len(entries), entries[0].Name())
	}
	_ = os.WriteFile(filepath.Join(data, "3014F711A000040000000A01.ap"), nil, 0o600)
	if _, err := k.backupIdentity("broken"); err == nil || identityBackup(t, k, "broken") != nil {
		t.Errorf("an empty file: %v", err)
	}
}
