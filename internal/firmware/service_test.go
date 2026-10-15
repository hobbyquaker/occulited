package firmware

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

func bundleBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestServiceTriggerRunsACheck(t *testing.T) {
	// a fake interface: one HmIP-PDT on 2.2.4
	itf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(raw), "listDevices") {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data><value><struct><member><name>ADDRESS</name><value><string>A</string></value></member><member><name>TYPE</name><value><string>HmIP-PDT</string></value></member><member><name>FIRMWARE</name><value><string>2.2.4</string></value></member></struct></value></data></array></value></param></params></methodResponse>`))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><boolean>1</boolean></value></param></params></methodResponse>`))
	}))
	defer itf.Close()
	// a fake eQ-3: index says 2.2.6 for HmIP-PDT, download serves a bundle
	padBytes := make([]byte, 8192)
	_, _ = rand.Read(padBytes) // incompressible, so the archive passes the size check
	pad := string(padBytes)
	bundle := bundleBytes(t, map[string]string{"info": "TypeCode=310\nName=HmIP-PDT\nFirmwareVersion=2.2.6\n", "HmIP-PDT_update_V2_2_6.efw": pad, "changelog.txt": "x"})
	eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/DEVICE"):
			_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-PDT","version":"2.2.6"},{"type":"HmIP-BBL","version":"1.0.0"}])`))
		case r.URL.Path == "/firmware/download":
			w.Header().Set("Content-Disposition", `attachment; filename=HmIP-PDT_update_V2_2_6_240101.tgz`)
			_, _ = w.Write(bundle)
		default:
			http.NotFound(w, r)
		}
	}))
	defer eq3.Close()
	dir := t.TempDir()
	ifs := interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(itf.URL, "http://")}})
	svc := NewService(New(eq3.URL), dir, func() []interfaces.Interface { return ifs }, nil)
	svc.SetEnabled(false) // scheduled runs off; a trigger must still run
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)
	svc.Trigger()
	deadline := time.Now().Add(10 * time.Second)
	var st State
	for time.Now().Before(deadline) {
		st = svc.Status()
		if st.LastRun != nil && !st.Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.LastRun == nil {
		t.Fatal("trigger did not run a check")
	}
	if st.LastError != "" || st.IndexSize != 2 || len(st.Devices) != 1 {
		t.Fatalf("state: %+v", st)
	}
	if len(st.LastResult) != 1 || st.LastResult[0].Type != "HmIP-PDT" || st.LastResult[0].Action != "downloaded" || st.LastResult[0].Version != "2.2.6" {
		t.Fatalf("result: %+v (BBL is not paired and must not appear)", st.LastResult)
	}
	if len(st.Deployed) != 1 || st.Deployed[0].TypeCode != "310" || st.Deployed[0].Version != "2.2.6" {
		t.Fatalf("deployed: %+v", st.Deployed)
	}
	// a second run finds everything current
	svc.Trigger()
	time.Sleep(300 * time.Millisecond)
	for time.Now().Before(deadline) && svc.Status().Running {
		time.Sleep(50 * time.Millisecond)
	}
	st = svc.Status()
	if len(st.LastResult) != 1 || st.LastResult[0].Action != "deployed" {
		t.Fatalf("second run must not download again: %+v", st.LastResult)
	}
}

// fakeInterface answers listDevices with one device and everything else with true.
func fakeInterface(t *testing.T, typ, fw string) []interfaces.Interface {
	t.Helper()
	itf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(raw), "listDevices") {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data><value><struct><member><name>ADDRESS</name><value><string>00010000000A10</string></value></member><member><name>TYPE</name><value><string>` + typ + `</string></value></member><member><name>FIRMWARE</name><value><string>` + fw + `</string></value></member><member><name>AVAILABLE_FIRMWARE</name><value><string>0.0.0</string></value></member></struct></value></data></array></value></param></params></methodResponse>`))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><boolean>1</boolean></value></param></params></methodResponse>`))
	}))
	t.Cleanup(itf.Close)
	return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(itf.URL, "http://")}})
}

func waitRun(t *testing.T, svc *Service, after *time.Time) State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.Status()
		if st.LastRun != nil && !st.Running && (after == nil || st.LastRun.After(*after)) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no run")
	return State{}
}

// B-195: hmipserver reports HMIP-WRC2, eQ-3's index says HmIP-WRC2 - the bundle is fetched with the
// index's spelling and deployed, the device shows eQ-3's version, and the run survives a restart.
func TestServiceMatchesUpperCaseTypesAndKeepsTheLastRun(t *testing.T) {
	ifs := fakeInterface(t, "HMIP-WRC2", "1.0.3")
	padBytes := make([]byte, 8192)
	_, _ = rand.Read(padBytes)
	bundle := bundleBytes(t, map[string]string{"info": "TypeCode=261\nName=HmIP-WRC2\nFirmwareVersion=1.18.2\nCCU3FirmwareVersionMin=3.43.15\n", "HmIP-WRC2_update_V1_18_2_230207.efw": string(padBytes)})
	var indexQuery, product string
	eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/DEVICE"):
			indexQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-WRC2","version":"1.18.2"},{"type":"HmIP-WRC2-2","version":"2.8.8"}])`))
		case r.URL.Path == "/firmware/download":
			product = r.URL.Query().Get("product")
			w.Header().Set("Content-Disposition", `attachment; filename=HmIP-WRC2_update_V1_18_2_230207.tgz`)
			_, _ = w.Write(bundle)
		default:
			http.NotFound(w, r)
		}
	}))
	defer eq3.Close()
	dir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "firmware-state.json")
	c := New(eq3.URL)
	c.SystemVersion = "3.89.9.20260914"
	svc := NewService(c, dir, func() []interfaces.Interface { return ifs }, nil)
	svc.SystemVersion = c.SystemVersion
	svc.Open(statePath)
	svc.SetAutoDeploy(true) // task 48: off by default
	svc.Check(t.Context())
	st := svc.Status()
	if indexQuery != "product=HM-CCU3&version=3.89.9.20260914" || product != "HmIP-WRC2" {
		t.Fatalf("index query %q, download product %q", indexQuery, product)
	}
	if st.LastError != "" || len(st.LastResult) != 1 || st.LastResult[0].Type != "HmIP-WRC2" || st.LastResult[0].Action != "downloaded" {
		t.Fatalf("result: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "261", "HmIP-WRC2_update_V1_18_2_230207.efw")); err != nil {
		t.Fatal("the bundle must be in 261/")
	}
	if len(st.Devices) != 1 || st.Devices[0].Latest != "1.18.2" || !st.Devices[0].UpdateAvailable || st.Devices[0].NotListed {
		t.Fatalf("device: %+v", st.Devices)
	}
	// a second run: the bundle is there, nothing is downloaded again
	product = ""
	svc.Check(t.Context())
	if st := svc.Status(); product != "" || len(st.LastResult) != 1 || st.LastResult[0].Action != "deployed" {
		t.Fatalf("second run: product %q, %+v", product, st.LastResult)
	}
	// a restart: the state file brings the last run back
	again := NewService(c, dir, func() []interfaces.Interface { return ifs }, nil)
	again.Open(statePath)
	if st2 := again.Status(); st2.LastRun == nil || len(st2.Devices) != 1 || st2.Devices[0].Latest != "1.18.2" || len(st2.LastResult) != 1 {
		t.Fatalf("after a restart: %+v", st2)
	}
}

func TestServiceMarksUnlistedTypes(t *testing.T) {
	ifs := fakeInterface(t, "HM-CC-TC", "2.1")
	eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-PDT","version":"2.2.4"}])`))
	}))
	defer eq3.Close()
	svc := NewService(New(eq3.URL), t.TempDir(), func() []interfaces.Interface { return ifs }, nil)
	svc.Check(t.Context())
	st := svc.Status()
	if len(st.Devices) != 1 || !st.Devices[0].NotListed || st.Devices[0].Latest != "" || st.Devices[0].UpdateAvailable || len(st.LastResult) != 0 {
		t.Fatalf("%+v", st)
	}
}

// B-195: switched on, a run that is due - none yet, or the last older than the interval - comes
// StartDelay after the start, not a day later; a recent one waits for its interval.
func TestServiceRunsADueCheckAfterStart(t *testing.T) {
	ifs := fakeInterface(t, "HmIP-PDT", "2.2.4")
	eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-PDT","version":"2.2.4"}])`))
	}))
	defer eq3.Close()
	statePath := filepath.Join(t.TempDir(), "firmware-state.json")
	old := time.Now().Add(-25 * time.Hour)
	_ = os.WriteFile(statePath, []byte(`{"last_run":"`+old.Format(time.RFC3339Nano)+`","last_result":[],"devices":[],"index_size":1}`), 0o600)
	svc := NewService(New(eq3.URL), t.TempDir(), func() []interfaces.Interface { return ifs }, nil)
	svc.StartDelay = 50 * time.Millisecond
	svc.Open(statePath)
	svc.SetEnabled(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go svc.Run(ctx)
	st := waitRun(t, svc, &old)
	if st.LastError != "" || len(st.LastResult) != 1 || st.LastResult[0].Action != "current" {
		t.Fatalf("%+v", st)
	}
	// the run just now: the next one is a day away
	time.Sleep(50 * time.Millisecond)
	if n := svc.Status().NextRun; n == nil || time.Until(*n) < 23*time.Hour {
		t.Fatalf("next run %v", n)
	}
}

// B-204: an access point - the HmIP-HAP, and the HmIP-HAP-B1 that takes its firmware - is
// fetched and deployed under 270 with its .zip, as any other bundle.
func TestServiceDeploysAnAccessPointBundle(t *testing.T) {
	for _, typ := range []string{"HmIP-HAP", "HmIP-HAP-B1"} {
		t.Run(typ, func(t *testing.T) {
			ifs := fakeInterface(t, typ, "2.2.18")
			pad := make([]byte, 8192) // a real bundle is 290 KB; the download refuses a tiny answer
			_, _ = rand.Read(pad)
			pack := map[string]string{"HMIP-HAP-application_update.eq3": string(pad)}
			for k, v := range hapPack {
				if _, ok := pack[k]; !ok {
					pack[k] = v
				}
			}
			bundle := bundleBytes(t, map[string]string{"info": hapInfo, "changelog.txt": "x", "HMIP_HAP_update_V3_0_18_2023_09_29.zip": zipBytes(t, pack)})
			var product string
			eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/search/DEVICE"):
					_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-HAP","version":"3.0.18"},{"type":"HmIPW-DRAP","version":"3.0.20"}])`))
				case r.URL.Path == "/firmware/download":
					product = r.URL.Query().Get("product")
					w.Header().Set("Content-Disposition", `attachment; filename=HMIP-HAP_3_0_18_230929.tgz`)
					_, _ = w.Write(bundle)
				default:
					http.NotFound(w, r)
				}
			}))
			defer eq3.Close()
			dir := t.TempDir()
			c := New(eq3.URL)
			c.SystemVersion = "3.89.9.20260914"
			svc := NewService(c, dir, func() []interfaces.Interface { return ifs }, nil)
			svc.SystemVersion = c.SystemVersion
			svc.Open(filepath.Join(t.TempDir(), "firmware-state.json"))
			svc.SetAutoDeploy(true) // task 48: off by default
			svc.Check(t.Context())
			st := svc.Status()
			if product != "HmIP-HAP" || st.LastError != "" || len(st.LastResult) != 1 || st.LastResult[0].Action != "downloaded" {
				t.Fatalf("product %q, %+v", product, st)
			}
			if _, err := os.Stat(filepath.Join(dir, "270", "HMIP_HAP_update_V3_0_18_2023_09_29.zip")); err != nil {
				t.Fatal("the access point bundle must be in 270/")
			}
		})
	}
}
