package firmware

import (
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

// B-225: two device types with one TypeCode (HmIP-WRC2 and HM-LC-Dim1T-DR, 261) keep two bundle
// directories - the second <TypeCode>-<Name> - so neither overwrites the other; a newer bundle of a
// type replaces its own, wherever it lies.
func TestDeploySharedTypeCode(t *testing.T) {
	dir := t.TempDir()
	deploy := func(name, version, file string) *Bundle {
		t.Helper()
		arch := filepath.Join(t.TempDir(), "b.tgz")
		writeBundle(t, arch, map[string]string{"info": "TypeCode=261\nName=" + name + "\nFirmwareVersion=" + version + "\n", file: "fw", "changelog.txt": name})
		b, err := Deploy(arch, dir)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if b := deploy("HmIP-WRC2", "1.18.2", "HmIP-WRC2_update_V1_18_2.efw"); b.Dir != "261" {
		t.Fatalf("first: %+v", b)
	}
	if b := deploy("HM-LC-Dim1T-DR", "1.1.0", "HM-LC-Dim1T-DR_update_V1_1_0.eq3"); b.Dir != "261-HM-LC-Dim1T-DR" || b.TypeCode != "261" {
		t.Fatalf("second: %+v", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "261", "HmIP-WRC2_update_V1_18_2.efw")); err != nil {
		t.Fatal("the WRC2's bundle was overwritten")
	}
	// a newer one of either replaces its own
	if b := deploy("HM-LC-Dim1T-DR", "1.2.0", "HM-LC-Dim1T-DR_update_V1_2_0.eq3"); b.Dir != "261-HM-LC-Dim1T-DR" {
		t.Fatalf("newer dimmer: %+v", b)
	}
	if b := deploy("HmIP-WRC2", "1.20.0", "HmIP-WRC2_update_V1_20_0.efw"); b.Dir != "261" {
		t.Fatalf("newer WRC2: %+v", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "261-HM-LC-Dim1T-DR", "HM-LC-Dim1T-DR_update_V1_1_0.eq3")); err == nil {
		t.Fatal("the dimmer's old file is still there")
	}
	dep, _ := Deployed(dir)
	got := map[string]string{}
	for _, b := range dep {
		got[b.Dir] = b.TypeCode + " " + b.Name + " " + b.Version
	}
	if len(dep) != 2 || got["261"] != "261 HmIP-WRC2 1.20.0" || got["261-HM-LC-Dim1T-DR"] != "261 HM-LC-Dim1T-DR 1.2.0" {
		t.Fatalf("deployed: %v", got)
	}
	// the page reads a file by the directory's name
	if b, err := ReadBundleFile(dir, "261-HM-LC-Dim1T-DR", "changelog.txt"); err != nil || string(b) != "HM-LC-Dim1T-DR" {
		t.Fatalf("%q %v", b, err)
	}
	// the dimmer first on a fresh system: it takes 261, the WRC2 the second directory
	dir2 := t.TempDir()
	dir = dir2
	if b := deploy("HM-LC-Dim1T-DR", "1.1.0", "d.eq3"); b.Dir != "261" {
		t.Fatalf("%+v", b)
	}
	if b := deploy("HmIP-WRC2", "1.18.2", "w.efw"); b.Dir != "261-HmIP-WRC2" {
		t.Fatalf("%+v", b)
	}
	// the same type spelled otherwise (hmipserver's HMIP-WRC2) is the same type
	if b := deploy("HMIP-WRC2", "1.18.3", "w2.efw"); b.Dir != "261-HmIP-WRC2" {
		t.Fatalf("%+v", b)
	}
	// a name that cannot name a directory: refused, nothing overwritten
	arch := filepath.Join(t.TempDir(), "b.tgz")
	writeBundle(t, arch, map[string]string{"info": "TypeCode=261\nName=" + strings.Repeat("X", 40) + "\nFirmwareVersion=1.0.0\n", "x.eq3": "fw"})
	if _, err := Deploy(arch, dir); err == nil || !strings.Contains(err.Error(), "held by HM-LC-Dim1T-DR") {
		t.Fatalf("%v", err)
	}
}

// fakeInterfaces is one interface per name, each answering listDevices with its devices (TYPE,
// FIRMWARE, UPDATABLE as the given raw XML value) and recording every other method called.
func fakeInterfaces(t *testing.T, devices map[string][][3]string) ([]interfaces.Interface, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	var entries []struct{ Name, URL string }
	for name, devs := range devices {
		var sb strings.Builder
		for i, d := range devs {
			sb.WriteString(`<value><struct><member><name>ADDRESS</name><value><string>` + name + string(rune('A'+i)) + `</string></value></member><member><name>TYPE</name><value><string>` + d[0] + `</string></value></member><member><name>FIRMWARE</name><value><string>` + d[1] + `</string></value></member>`)
			if d[2] != "" {
				sb.WriteString(`<member><name>UPDATABLE</name><value>` + d[2] + `</value></member>`)
			}
			sb.WriteString(`</struct></value>`)
		}
		list := `<?xml version="1.0"?><methodResponse><params><param><value><array><data>` + sb.String() + `</data></array></value></param></params></methodResponse>`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/xml")
			if strings.Contains(string(raw), "listDevices") {
				_, _ = w.Write([]byte(list))
				return
			}
			mu.Lock()
			calls = append(calls, name)
			mu.Unlock()
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><boolean>1</boolean></value></param></params></methodResponse>`))
		}))
		t.Cleanup(srv.Close)
		entries = append(entries, struct{ Name, URL string }{name, "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")})
	}
	return interfaces.FromList(entries), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// B-225: a WRC2 and a Dim1T-DR, both older than eQ-3's version - two runs keep both bundles and the
// second downloads nothing; only BidCos-RF and HmIP-RF are refreshed; a device that says
// UPDATABLE 0 is not planned and not "not in eQ-3's list".
func TestServiceSharedTypeCodeAndRefresh(t *testing.T) {
	ifs, calls := fakeInterfaces(t, map[string][][3]string{
		"HmIP-RF":        {{"HMIP-WRC2", "1.0.3", "<boolean>1</boolean>"}},
		"BidCos-RF":      {{"HM-LC-Dim1T-DR", "1.0.0", "<i4>1</i4>"}, {"HM-CC-TC", "2.1", "<i4>0</i4>"}},
		"BidCos-Wired":   {{"HMW-IO-12-Sw7-DR", "1.0", ""}},
		"VirtualDevices": {{"HM-RCV-50", "1.0", ""}},
	})
	pad := make([]byte, 8192)
	_, _ = rand.Read(pad)
	bundles := map[string][]byte{
		"HmIP-WRC2":      bundleBytes(t, map[string]string{"info": "TypeCode=261\nName=HmIP-WRC2\nFirmwareVersion=1.18.2\n", "HmIP-WRC2_update_V1_18_2.efw": string(pad)}),
		"HM-LC-Dim1T-DR": bundleBytes(t, map[string]string{"info": "TypeCode=261\nName=HM-LC-Dim1T-DR\nFirmwareVersion=1.1.0\n", "HM-LC-Dim1T-DR_update_V1_1_0.eq3": string(pad)}),
	}
	var downloads []string
	eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/search/DEVICE"):
			_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-WRC2","version":"1.18.2"},{"type":"HM-LC-Dim1T-DR","version":"1.1.0"},{"type":"HM-CC-TC","version":"2.9"}])`))
		case r.URL.Path == "/firmware/download":
			p := r.URL.Query().Get("product")
			downloads = append(downloads, p)
			w.Header().Set("Content-Disposition", `attachment; filename=`+p+`_update.tgz`)
			_, _ = w.Write(bundles[p])
		default:
			http.NotFound(w, r)
		}
	}))
	defer eq3.Close()
	dir := t.TempDir()
	svc := NewService(New(eq3.URL), dir, func() []interfaces.Interface { return ifs }, nil)
	svc.SetAutoDeploy(true) // task 48: off by default
	svc.Check(t.Context())
	st := svc.Status()
	if st.LastError != "" || strings.Join(downloads, ",") != "HM-LC-Dim1T-DR,HmIP-WRC2" {
		t.Fatalf("first run: %q %+v", downloads, st)
	}
	for _, sub := range []string{"261", "261-HmIP-WRC2"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Fatalf("%s missing", sub)
		}
	}
	got := calls()
	for _, c := range got {
		if c != "HmIP-RF" && c != "BidCos-RF" {
			t.Fatalf("refreshed %q: %q", c, got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("refresh calls %q", got)
	}
	var tc DeviceStatus
	for _, d := range st.Devices {
		if d.Type == "HM-CC-TC" {
			tc = d
		}
	}
	if tc.Updatable == nil || *tc.Updatable || tc.NotListed || tc.UpdateAvailable || tc.Latest != "" {
		t.Fatalf("HM-CC-TC: %+v", tc)
	}
	// the second run: both are there, nothing is downloaded
	downloads = nil
	svc.Check(t.Context())
	if len(downloads) != 0 {
		t.Fatalf("second run downloaded %q", downloads)
	}
	for _, r := range svc.Status().LastResult {
		if r.Type == "HM-CC-TC" {
			t.Fatalf("a device that cannot be updated is in the run: %+v", r)
		}
		if r.Action != "deployed" {
			t.Fatalf("second run: %+v", r)
		}
	}
	// a manual upload refreshes BidCos-RF and HmIP-RF only
	before := len(calls())
	arch := filepath.Join(t.TempDir(), "up.tgz")
	writeBundle(t, arch, map[string]string{"info": "TypeCode=310\nName=HmIP-PDT\nFirmwareVersion=2.2.6\n", "x.efw": "fw"})
	if _, err := svc.DeployUpload(arch); err != nil {
		t.Fatal(err)
	}
	after := calls()[before:]
	if len(after) != 2 || strings.Contains(strings.Join(after, ","), "Wired") || strings.Contains(strings.Join(after, ","), "Virtual") {
		t.Fatalf("upload refreshed %q", after)
	}
}

func TestPlanSkipsNotUpdatable(t *testing.T) {
	index := []Entry{{Type: "HM-CC-TC", Version: "2.9"}, {Type: "HmIP-PDT", Version: "2.2.6"}}
	plan := Plan(index, []Device{{Type: "HM-CC-TC", Firmware: "2.1", NotUpdatable: true}, {Type: "HmIP-PDT", Firmware: "2.2.4"}})
	if len(plan) != 1 || plan[0].Type != "HmIP-PDT" {
		t.Fatalf("%+v", plan)
	}
}
