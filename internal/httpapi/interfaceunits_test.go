package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/health"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/system"
)

// unitsServices is a service manager that answers for the radio stack's units, as SystemdServices does.
type unitsServices struct {
	scriptBox
	calls int
	ifs   []system.Interface
	units []system.InterfaceUnit
}

func (u *unitsServices) InterfaceUnits(_ context.Context, ifs []system.Interface) []system.InterfaceUnit {
	u.calls++
	u.ifs = ifs
	return u.units
}

// task 94: GET /radio/health carries the units; hmipserver starting is there with its age, and the
// list of interfaces it was read against is InterfacesList.xml at the request
func TestRadioHealthUnits(t *testing.T) {
	r := fakeRoot(t)
	list := filepath.Join(string(r), "etc/config/InterfacesList.xml")
	if err := os.MkdirAll(filepath.Dir(list), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(list, []byte("<interfaces><ipc><name>HmIP-RF</name><url>xmlrpc://127.0.0.1:32010</url><info>HmIP-RF</info></ipc></interfaces>"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &unitsServices{units: []system.InterfaceUnit{
		{Unit: "multimacd", Interfaces: []string{}, ActiveState: "active", SubState: "running"},
		{Unit: "hmipserver", Interfaces: []string{"HmIP-RF", "VirtualDevices"}, ActiveState: "activating", SubState: "start", Starting: true, StartingS: 23},
	}}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Health: &health.Sampler{}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/radio/health", "", nil)
	if st != 200 {
		t.Fatalf("%d %v", st, out)
	}
	units, _ := out["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("units %v", out["units"])
	}
	hmip := units[1].(map[string]any)
	if hmip["unit"] != "hmipserver" || hmip["starting"] != true || hmip["starting_s"] != 23.0 || hmip["active_state"] != "activating" {
		t.Errorf("hmipserver %v", hmip)
	}
	if len(svc.ifs) != 1 || svc.ifs[0].Name != "HmIP-RF" {
		t.Errorf("read against %+v", svc.ifs)
	}
	// a second request within the cache's time does not ask systemd again
	do(t, srv, "GET", "/api/system/v1/radio/health", "", nil)
	if svc.calls != 1 {
		t.Errorf("%d readings for two requests", svc.calls)
	}

	// a busybox box: no units at all
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: r, Services: scriptBox{system.AddonScripts{Root: r}}, Health: &health.Sampler{}}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	if _, out, _ := do(t, srv2, "GET", "/api/system/v1/radio/health", "", nil); out["units"] != nil {
		t.Errorf("busybox: %v", out["units"])
	}
}

// the cache hands out a reading with the starting ages moved on, so a counter never runs backwards
func TestRadioUnitsCacheAges(t *testing.T) {
	svc := &unitsServices{units: []system.InterfaceUnit{
		{Unit: "hmipserver", Starting: true, StartingS: 10},
		{Unit: "rfd", Starting: true, Queued: true},
		{Unit: "multimacd", ActiveState: "active", ActiveFor: time.Second},
	}}
	a := &SystemAPI{Root: fakeRoot(t), Services: svc}
	if u := a.radioUnits(context.Background()); u[0].StartingS != 10 {
		t.Fatalf("%+v", u)
	}
	a.units.mu.Lock()
	a.units.at = a.units.at.Add(-2 * time.Second)
	a.units.mu.Unlock()
	u := a.radioUnits(context.Background())
	if svc.calls != 1 {
		t.Errorf("%d readings", svc.calls)
	}
	if u[0].StartingS != 12 || u[1].StartingS != 0 || u[2].ActiveFor < 3*time.Second {
		t.Errorf("aged %+v", u)
	}
	// the cached copy itself is not changed
	if svc.units[0].StartingS != 10 || a.units.units[0].StartingS != 10 {
		t.Errorf("the reading was aged in place: %+v", a.units.units)
	}
	// past the cache's time: read again
	a.units.mu.Lock()
	a.units.at = a.units.at.Add(-unitsTTL)
	a.units.mu.Unlock()
	a.radioUnits(context.Background())
	if svc.calls != 2 {
		t.Errorf("%d readings after the cache ran out", svc.calls)
	}
}

// an error recorded before the interface's unit became active is the start, not a failure
func TestStaleErrors(t *testing.T) {
	st := &health.Status{Polled: time.Now().Add(-30 * time.Second), Errors: map[string]string{"HmIP-RF": "connection refused", "BidCos-RF": "connection refused", "BidCos-Wired": "no answer"}}
	units := []system.InterfaceUnit{
		// active 10 s, polled 30 s ago: the error is from its start
		{Unit: "hmipserver", Interfaces: []string{"HmIP-RF", "VirtualDevices"}, ActiveState: "active", ActiveFor: 10 * time.Second},
		// active for a minute, and still refused at the poll: a real error
		{Unit: "rfd", Interfaces: []string{"BidCos-RF"}, ActiveState: "active", ActiveFor: time.Minute},
		// starting: the page says so, the error stays for it to replace
		{Unit: "hs485d", Interfaces: []string{"BidCos-Wired"}, ActiveState: "activating", Starting: true},
	}
	if !staleErrors(st, units) {
		t.Error("nothing dropped")
	}
	if _, ok := st.Errors["HmIP-RF"]; ok || st.Errors["BidCos-RF"] == "" || st.Errors["BidCos-Wired"] == "" {
		t.Errorf("errors %v", st.Errors)
	}
	if staleErrors(st, units) {
		t.Error("dropped twice")
	}
	if staleErrors(&health.Status{Errors: map[string]string{"HmIP-RF": "x"}}, units) {
		t.Error("no poll yet, nothing is stale")
	}
}

// occulited task 13: GET /radio/health carries the event rate of every interface process the
// sampler counts (per minute, task 17), and the subscriber summary of every interface it knows that
// has a handlers file - with lite-rpc's open event streams counted in (B-45)
func TestRadioHealthRatesAndSubscribers(t *testing.T) {
	r := fakeRoot(t)
	if err := os.MkdirAll(filepath.Join(string(r), "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(string(r), "var/LegacyService.handlers"), []byte("HmIP-RF_java=http\\://127.0.0.1\\:39292/bidcos\nmb_HmIP_RF=http\\://198.51.100.9\\:2049\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	var in uint64 = 10
	s := &health.Sampler{Timeout: 2 * time.Second,
		Interfaces: func() []interfaces.Interface {
			return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + closed}, {"CUxD", "xmlrpc_bin://" + closed}})
		},
		Feed: func() []rpcsub.IfaceStatus {
			return []rpcsub.IfaceStatus{{Name: "HmIP-RF", State: "up", Registered: true, Telegrams: in}}
		},
		// the two polls are a moment apart (B-79: a rate needs 30 s by default)
		MinRateSpan: time.Nanosecond,
	}
	s.Poll(context.Background())
	in = 20
	s.Poll(context.Background())
	mux := http.NewServeMux()
	// B-45: an addon's stream on the loopback for every interface, a LAN client's for HmIP-RF, and
	// one for BidCos-RF alone, which is no subscriber of HmIP-RF
	lite := literpc.New(literpc.Config{})
	for _, o := range []struct {
		subj      literpc.Subject
		remote    string
		f         literpc.Filter
		transport string
	}{
		{literpc.Subject{Kind: "token", Name: "addon:openccu-loom"}, "127.0.0.1", literpc.Filter{}, "sse"},
		{literpc.Subject{Kind: "session", Name: "admin"}, "198.51.100.20", literpc.Filter{Interfaces: []string{"HmIP-RF"}, Types: []string{"devices"}}, "websocket"},
		{literpc.Subject{Kind: "token", Name: "ha"}, "198.51.100.21", literpc.Filter{Interfaces: []string{"BidCos-RF"}}, "sse"},
	} {
		if _, err := lite.Open(o.subj, o.transport, o.remote, o.f); err != nil {
			t.Fatal(err)
		}
	}
	(&SystemAPI{Root: r, Services: scriptBox{system.AddonScripts{Root: r}}, Health: s, LiteRPC: lite}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	st, out, _ := do(t, srv, "GET", "/api/system/v1/radio/health", "", nil)
	if st != 200 {
		t.Fatalf("%d %v", st, out)
	}
	rates, _ := out["rates"].(map[string]any)
	hmip, _ := rates["HmIP-RF"].([]any)
	if len(hmip) != 1 {
		t.Fatalf("rates %v", out["rates"])
	}
	if s0 := hmip[0].(map[string]any); s0["up"] != true || s0["in"].(float64) <= 0 {
		t.Errorf("rate sample %v", s0)
	} else if _, ok := s0["out"]; ok {
		t.Errorf("task 17: no outgoing rate any more: %v", s0)
	}
	subs, _ := out["subscribers"].(map[string]any)
	h, _ := subs["HmIP-RF"].(map[string]any)
	// two callbacks in the handlers file (one internal, one external) and two streams (the addon's
	// internal, the LAN client's external)
	if h == nil || h["total"] != 4.0 || h["internal"] != 2.0 || h["external"] != 2.0 || len(h["clients"].([]any)) != 4 {
		t.Fatalf("subscribers %v", out["subscribers"])
	}
	cs := h["clients"].([]any)
	loom, _ := cs[2].(map[string]any)
	sm, _ := loom["stream"].(map[string]any)
	if loom["id"] != "addon:openccu-loom" || loom["internal"] != true || sm["kind"] != "token" || sm["transport"] != "sse" || sm["remote"] != "127.0.0.1" || sm["id"] == "" {
		t.Errorf("the addon's stream %v", loom)
	}
	if lan, _ := cs[3].(map[string]any); lan["id"] != "admin" || lan["internal"] != false {
		t.Errorf("the LAN client's stream %v", lan)
	}
	if _, ok := cs[0].(map[string]any)["stream"]; ok {
		t.Errorf("a handlers entry carries a stream: %v", cs[0])
	}
	// CUxD answered nothing and has no handlers file: no count
	if _, ok := subs["CUxD"]; ok {
		t.Errorf("CUxD has a subscriber count: %v", subs)
	}
}
