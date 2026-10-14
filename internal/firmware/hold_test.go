package firmware

import (
	"context"
	"crypto/rand"
	"encoding/json"
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

// ifs is one HmIP-RF interface at a test server's URL.
func ifs(url string) []interfaces.Interface {
	return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(url, "http://")}})
}

// a bundle directory as Deploy leaves it: info + a firmware file
func writeHeldTestBundle(t *testing.T, base, dirName, name, typeCode, version string) {
	t.Helper()
	d := filepath.Join(base, dirName)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "info"), []byte("TypeCode="+typeCode+"\nName="+name+"\nFirmwareVersion="+version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name+"_update_V1.efw"), []byte("fw"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// refreshCounter is a fake HmIP-RF that counts refreshDeployedDeviceFirmwareList and answers
// listDevices with nothing.
type refreshCounter struct {
	mu       sync.Mutex
	refreshs int
	srv      *httptest.Server
}

func newRefreshCounter(t *testing.T) *refreshCounter {
	t.Helper()
	rc := &refreshCounter{}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(raw), "refreshDeployedDeviceFirmwareList") {
			rc.mu.Lock()
			rc.refreshs++
			rc.mu.Unlock()
		}
		if strings.Contains(string(raw), "listDevices") {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data></data></array></value></param></params></methodResponse>`))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><string></string></value></param></params></methodResponse>`))
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *refreshCounter) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.refreshs
}

func TestHeldDir(t *testing.T) {
	if got := HeldDir("/etc/config/firmware"); got != "/etc/config/firmware-held" {
		t.Errorf("HeldDir: %s", got)
	}
	if got := HeldDir("/etc/config/firmware/"); got != "/etc/config/firmware-held" {
		t.Errorf("HeldDir with a slash: %s", got)
	}
}

// The per-type switch: off moves the bundle into the held directory and marks the type, the
// interfaces re-read the directory, GET /firmware lists the bundle as held; on moves it back.
func TestSetTransferMovesTheBundleAndMarksTheType(t *testing.T) {
	rc := newRefreshCounter(t)
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	writeHeldTestBundle(t, dir, "332", "HmIP-BBL", "332", "1.10.16")
	writeHeldTestBundle(t, dir, "261", "HmIP-WRC2", "261", "1.18.2")
	s := NewService(New("http://127.0.0.1:1"), dir, func() []interfaces.Interface { return ifs(rc.srv.URL) }, nil)
	s.Open(filepath.Join(root, "state.json"))
	if st := s.Status(); !st.AutoDeploy || len(st.HeldTypes) != 0 || len(st.Deployed) != 2 {
		t.Fatalf("fresh: %+v", st)
	}
	b, err := s.SetTransfer("332", false)
	if err != nil || b == nil || !b.Held || b.Name != "HmIP-BBL" {
		t.Fatalf("hold: %+v %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, HeldDirName, "332", "info")); err != nil {
		t.Errorf("the bundle is not in the held directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "332")); !os.IsNotExist(err) {
		t.Errorf("the bundle is still offered: %v", err)
	}
	if rc.count() != 1 {
		t.Errorf("refreshes after the hold: %d", rc.count())
	}
	st := s.Status()
	if len(st.HeldTypes) != 1 || st.HeldTypes[0] != "hmip-bbl" {
		t.Errorf("held types: %v", st.HeldTypes)
	}
	var held, offered int
	for _, d := range st.Deployed {
		if d.Held {
			held++
			if d.Dir != "332" || d.Name != "HmIP-BBL" {
				t.Errorf("held bundle: %+v", d)
			}
		} else {
			offered++
		}
	}
	if held != 1 || offered != 1 {
		t.Errorf("deployed: %d held, %d offered", held, offered)
	}
	// the mark survives a restart
	s2 := NewService(New("http://127.0.0.1:1"), dir, func() []interfaces.Interface { return ifs(rc.srv.URL) }, nil)
	s2.Open(filepath.Join(root, "state.json"))
	if st := s2.Status(); len(st.HeldTypes) != 1 {
		t.Errorf("after a restart: %v", st.HeldTypes)
	}
	// holding it again changes nothing but says so
	if b, err := s.SetTransfer("332", false); err != nil || !b.Held {
		t.Errorf("hold again: %+v %v", b, err)
	}
	// offered again
	b, err = s.SetTransfer("332", true)
	if err != nil || b.Held {
		t.Fatalf("offer: %+v %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "332", "info")); err != nil {
		t.Errorf("the bundle is not offered: %v", err)
	}
	if st := s.Status(); len(st.HeldTypes) != 0 {
		t.Errorf("held types after the offer: %v", st.HeldTypes)
	}
	if rc.count() != 3 {
		t.Errorf("refreshes: %d", rc.count())
	}
	// no such bundle, and no path games
	for _, d := range []string{"999", "../x", "", ".held"} {
		if _, err := s.SetTransfer(d, false); err == nil {
			t.Errorf("%q held", d)
		}
	}
}

// The global switch: off, the check's downloads land in the held directory; on again, the
// check offers them (unless the type is held by its own switch).
func TestCheckRespectsTheSwitches(t *testing.T) {
	var mu sync.Mutex
	downloads := 0
	eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/search/DEVICE"):
			_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-BBL","version":"1.10.16"},{"type":"HmIP-WRC2","version":"1.18.2"}])`))
		case strings.Contains(r.URL.Path, "/download"):
			mu.Lock()
			downloads++
			mu.Unlock()
			typ := r.URL.Query().Get("product")
			code, ver := "332", "1.10.16"
			if typ == "HmIP-WRC2" {
				code, ver = "261", "1.18.2"
			}
			w.Header().Set("Content-Disposition", `attachment; filename="`+typ+`_update_V`+strings.ReplaceAll(ver, ".", "_")+`.tgz"`)
			// the firmware file must not compress below the download's 1 KB floor
			_, _ = w.Write(bundleBytes(t, map[string]string{"info": "TypeCode=" + code + "\nName=" + typ + "\nFirmwareVersion=" + ver + "\n", typ + "_update.efw": randomBytes(t, 4096)}))
		}
	}))
	defer eq3.Close()
	itf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(raw), "listDevices") {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>` +
				device("00010000000A10", "HmIP-BBL", "1.0.2") + device("00010000000A11", "HmIP-WRC2", "1.10.0") +
				`</data></array></value></param></params></methodResponse>`))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><string></string></value></param></params></methodResponse>`))
	}))
	defer itf.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	s := NewService(New(eq3.URL), dir, func() []interfaces.Interface { return ifs(itf.URL) }, nil)
	s.Open(filepath.Join(root, "state.json"))
	s.SetAutoDeploy(false)
	s.Check(context.Background())
	st := s.Status()
	if st.LastError != "" {
		t.Fatalf("check: %s", st.LastError)
	}
	for _, d := range []string{"332", "261"} {
		if _, err := os.Stat(filepath.Join(root, HeldDirName, d, "info")); err != nil {
			t.Errorf("%s not held with the global switch off: %v", d, err)
		}
		if _, err := os.Stat(filepath.Join(dir, d)); !os.IsNotExist(err) {
			t.Errorf("%s offered with the global switch off", d)
		}
	}
	if len(st.Deployed) != 2 || !st.Deployed[0].Held || !st.Deployed[1].Held || st.AutoDeploy {
		t.Errorf("status: %+v", st)
	}
	for _, r := range st.LastResult {
		if r.Action != "downloaded" || !r.Held {
			t.Errorf("result: %+v", r)
		}
	}
	// the BBL held by its own switch, the global switch on again: the check offers the WRC2's
	// bundle without a new download and leaves the BBL's where it is
	if _, err := s.SetTransfer("332", false); err != nil {
		t.Fatal(err)
	}
	s.SetAutoDeploy(true)
	before := downloads
	s.Check(context.Background())
	if downloads != before {
		t.Errorf("a bundle on the box was downloaded again")
	}
	if _, err := os.Stat(filepath.Join(dir, "261", "info")); err != nil {
		t.Errorf("the WRC2's bundle was not offered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, HeldDirName, "332", "info")); err != nil {
		t.Errorf("the held BBL bundle was moved: %v", err)
	}
	st = s.Status()
	for _, r := range st.LastResult {
		switch r.Type {
		case "HmIP-WRC2":
			if r.Action != "deployed" || r.Held {
				t.Errorf("WRC2: %+v", r)
			}
		case "HmIP-BBL":
			if r.Action != "deployed" || !r.Held {
				t.Errorf("BBL: %+v", r)
			}
		}
	}
	if len(st.HeldTypes) != 1 || st.HeldTypes[0] != "hmip-bbl" {
		t.Errorf("held types: %v", st.HeldTypes)
	}
	// the state file keeps both switches
	raw, _ := os.ReadFile(filepath.Join(root, "state.json"))
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil || !p.Held["hmip-bbl"] || p.AutoDeployOff == nil || *p.AutoDeployOff {
		t.Errorf("state file: %s %v", raw, err)
	}
}

func randomBytes(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func device(addr, typ, fw string) string {
	return `<value><struct><member><name>ADDRESS</name><value><string>` + addr + `</string></value></member><member><name>TYPE</name><value><string>` + typ + `</string></value></member><member><name>FIRMWARE</name><value><string>` + fw + `</string></value></member><member><name>PARENT</name><value><string></string></value></member></struct></value>`
}
