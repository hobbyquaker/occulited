package firmware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

// occulited task 63 (the maintainer, 2026-10-16: "Deliver at once"): BidCos-RF firmware goes to
// the firmware directory straight after the download, whatever Deploy automatically says - rfd
// transfers nothing by itself, a bundle there only lets the frontends offer the update. HmIP keeps
// task 48's held/deliver flow. Held BidCos bundles are moved over once.

func TestCheckDeliversBidCosAtOnce(t *testing.T) {
	codes := map[string][2]string{"HmIP-BBL": {"332", "1.10.16"}, "HM-CC-RT-DN": {"95", "1.5"}, "ZEL STG RM FWT": {"F8", "2.4"}}
	eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/search/DEVICE"):
			_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-BBL","version":"1.10.16"},{"type":"HM-CC-RT-DN","version":"1.5"},{"type":"ZEL STG RM FWT","version":"2.4"}])`))
		case strings.Contains(r.URL.Path, "/download"):
			typ := r.URL.Query().Get("product")
			c := codes[typ]
			w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(typ, " ", "_")+`_update_V`+strings.ReplaceAll(c[1], ".", "_")+`.tgz"`)
			_, _ = w.Write(bundleBytes(t, map[string]string{"info": "TypeCode=" + c[0] + "\nName=" + typ + "\nFirmwareVersion=" + c[1] + "\n", "fw_update.eq3": randomBytes(t, 4096)}))
		}
	}))
	defer eq3.Close()
	itf := func(devs string) (*httptest.Server, *int) {
		refreshs := new(int)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/xml")
			switch {
			case strings.Contains(string(raw), "listDevices"):
				_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>` + devs + `</data></array></value></param></params></methodResponse>`))
				return
			case strings.Contains(string(raw), "refreshDeployedDeviceFirmwareList"):
				*refreshs++
			}
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><string></string></value></param></params></methodResponse>`))
		}))
		t.Cleanup(srv.Close)
		return srv, refreshs
	}
	hmip, _ := itf(device("00010000000A10", "HmIP-BBL", "1.0.2"))
	// a BidCos type eQ-3 does not name HM-…: BidCos by its interface
	bidcos, bidcosRefreshs := itf(device("NEQ0000001", "HM-CC-RT-DN", "1.4") + device("NEQ0000002", "ZEL STG RM FWT", "2.3"))
	list := interfaces.FromList([]struct{ Name, URL string }{
		{"BidCos-RF", "xmlrpc://" + strings.TrimPrefix(bidcos.URL, "http://")},
		{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(hmip.URL, "http://")},
	})
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	s := NewService(New(eq3.URL), dir, func() []interfaces.Interface { return list }, nil)
	s.Open(filepath.Join(root, "state.json"))
	s.SetAutoDeploy(false)
	s.Check(context.Background())
	st := s.Status()
	if st.LastError != "" {
		t.Fatalf("check: %s", st.LastError)
	}
	for _, code := range []string{"95", "F8"} {
		if _, err := os.Stat(filepath.Join(dir, code, "info")); err != nil {
			t.Errorf("BidCos bundle %s not offered with the switch off: %v", code, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, HeldDirName, "332-HmIP-BBL-1.10.16", "info")); err != nil {
		t.Errorf("the HmIP bundle is not held: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "332")); !os.IsNotExist(err) {
		t.Errorf("the HmIP bundle was offered with the switch off")
	}
	for _, r := range st.LastResult {
		if r.Action != "downloaded" || r.Held != (r.Type == "HmIP-BBL") {
			t.Errorf("result: %+v", r)
		}
	}
	if *bidcosRefreshs != 1 {
		t.Errorf("rfd refreshed %d times", *bidcosRefreshs)
	}
	// a type held by its own switch before task 63 (the API still takes it): BidCos is offered
	// all the same
	s.mu.Lock()
	s.held = map[string]bool{"hm-cc-rt-dn": true}
	s.mu.Unlock()
	if dir, held := s.target("hm-cc-rt-dn", true); held || dir != s.Dir {
		t.Errorf("held BidCos type: %s %v", dir, held)
	}
}

func TestDeliverHeldBidCosOnce(t *testing.T) {
	rc := newRefreshCounter(t)
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	held := filepath.Join(root, HeldDirName)
	// the radiator thermostat: 1.4 offered, 1.5 and an older 1.3 held
	writeHeldTestBundle(t, dir, "95", "HM-CC-RT-DN", "95", "1.4")
	writeHeldTestBundle(t, held, "95-HM-CC-RT-DN-1.5", "HM-CC-RT-DN", "95", "1.5")
	writeHeldTestBundle(t, held, "95-HM-CC-RT-DN-1.3", "HM-CC-RT-DN", "95", "1.3")
	// the wall thermostat: nothing offered, held before task 48 under its TypeCode
	writeHeldTestBundle(t, held, "AD", "HM-TC-IT-WM-W-EU", "AD", "1.6")
	// a switch actuator: the held one is older than the offered one
	writeHeldTestBundle(t, dir, "6A", "HM-LC-Sw1-FM", "6A", "2.9")
	writeHeldTestBundle(t, held, "6A-HM-LC-Sw1-FM-2.8", "HM-LC-Sw1-FM", "6A", "2.8")
	// HmIP stays held
	writeHeldTestBundle(t, held, "332-HmIP-BBL-1.10.16", "HmIP-BBL", "332", "1.10.16")
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte(`{"held": {"hm-cc-rt-dn": true, "hmip-bbl": true}, "pinned": {"hm-lc-sw1-fm": true}, "previous": {"hm-cc-rt-dn": "1.3"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewService(New("http://127.0.0.1:1"), dir, func() []interfaces.Interface { return ifs(rc.srv.URL) }, nil)
	s.Open(path)
	s.DeliverHeldBidCos()
	for d, want := range map[string]string{"95": "1.5", "AD": "1.6", "6A": "2.9"} {
		if got := bundleVersionAt(t, filepath.Join(dir, d)); got != want {
			t.Errorf("offered %s: %s, want %s", d, got, want)
		}
	}
	rest, err := Held(held)
	if err != nil || len(rest) != 1 || rest[0].Name != "HmIP-BBL" {
		t.Errorf("held after the move: %+v %v", rest, err)
	}
	if rc.count() != 1 {
		t.Errorf("refreshes: %d", rc.count())
	}
	st := s.Status()
	if len(st.HeldTypes) != 1 || st.HeldTypes[0] != "hmip-bbl" || len(st.PinnedTypes) != 0 {
		t.Errorf("marks: held %v pinned %v", st.HeldTypes, st.PinnedTypes)
	}
	raw, _ := os.ReadFile(path)
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil || !p.BidCosDelivered || p.Previous["hm-cc-rt-dn"] != "" {
		t.Errorf("state file: %s %v", raw, err)
	}
	// once: a BidCos bundle held later (through the API) stays where it is, after a restart too
	writeHeldTestBundle(t, held, "95-HM-CC-RT-DN-1.6", "HM-CC-RT-DN", "95", "1.6")
	s2 := NewService(New("http://127.0.0.1:1"), dir, func() []interfaces.Interface { return ifs(rc.srv.URL) }, nil)
	s2.Open(path)
	s2.DeliverHeldBidCos()
	if _, err := os.Stat(filepath.Join(held, "95-HM-CC-RT-DN-1.6", "info")); err != nil {
		t.Errorf("the second start moved it: %v", err)
	}
	if rc.count() != 1 {
		t.Errorf("refreshes after the second start: %d", rc.count())
	}
}

func TestBidCosType(t *testing.T) {
	for typ, want := range map[string]bool{"HM-CC-RT-DN": true, "hm-lc-sw1-fm": true, "HMW-IO-12-Sw7-DR": false, "HmIP-BBL": false, "HMIP-WRC2": false, "HmIPW-DRAP": false, "": false} {
		if got := BidCosType(typ); got != want {
			t.Errorf("%q: %v", typ, got)
		}
	}
}
