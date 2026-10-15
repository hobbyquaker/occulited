package firmware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/outboundpin"
)

// occulited task 49: older bundles from the OpenCCU firmware archive, several versions per type,
// the user's choice of the delivered one.

const archiveTree = `{"sha":"x","truncated":false,"tree":[
{"path":"HmIP","type":"tree"},
{"path":"HmIP/HmIP-PDT_update_V1_4_8_170725.tgz","type":"blob","size":100801},
{"path":"HmIP/HmIP-PDT_update_V2_2_4_231123.tgz","type":"blob","size":98883},
{"path":"HmIP/HmIP-PDT-UK_update_V1_4_8_170804.tgz","type":"blob","size":100784},
{"path":"HmIP/HMIP-HAP_3_0_18_230929.tgz","type":"blob","size":5000},
{"path":"HmIP/HmIP-BBL_update_V1_8_10_190815.tgz","type":"blob","size":80000},
{"path":"HmIPW/HmIPW-DRAP_2_4_14_210816.tgz","type":"blob","size":5000},
{"path":"HmIPW/HmIPW-DRS8_update_V1_2_6_220928.tgz","type":"blob","size":70000},
{"path":"ELV/ELV-SH-CTH_update_V1_0_10_240807.tgz","type":"blob","size":60000},
{"path":"HM/HM-LC-Sw1-Pl-2_update_V2_8_0.tgz","type":"blob","size":40000},
{"path":"HmIP/HmIP-WRC2_update_V1_18_0_220214.tgz","type":"blob","size":58747},
{"path":"HmIP/HmIP-WRC2_update_V1_18_2_230207.tgz","type":"blob","size":61168},
{"path":"HmIP/HmIP-WRC2_update_V1_18_2_240101.tgz","type":"blob","size":61168},
{"path":"docs/index.md","type":"blob","size":10},
{"path":"HmIP/../x_update_V1_0_0.tgz","type":"blob","size":10}
]}`

// fakeArchive serves the tree and the files; files maps an archive path to a bundle's bytes.
type fakeArchive struct {
	mu    sync.Mutex
	trees int
	files map[string][]byte
	srv   *httptest.Server
	log   *outboundpin.Log
}

func newFakeArchive(t *testing.T, files map[string][]byte) *fakeArchive {
	t.Helper()
	fa := &fakeArchive{files: files}
	h, log := outboundpin.Recorder(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tree" {
			fa.mu.Lock()
			fa.trees++
			fa.mu.Unlock()
			_, _ = io.WriteString(w, archiveTree)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/raw/")
		if b, ok := fa.files[p]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	fa.log = log
	fa.srv = httptest.NewServer(h)
	t.Cleanup(fa.srv.Close)
	return fa
}

func (fa *fakeArchive) archive() *Archive {
	a := NewArchive()
	a.TreeURL, a.RawBase = fa.srv.URL+"/tree", fa.srv.URL+"/raw/"
	return a
}

func (fa *fakeArchive) treeCalls() int {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return fa.trees
}

func pdtBundle(t *testing.T, name, version, min string) []byte {
	t.Helper()
	info := "TypeCode=310\nName=" + name + "\nFirmwareVersion=" + version + "\n"
	if min != "" {
		info += "CCU3FirmwareVersionMin=" + min + "\n"
	}
	return bundleBytes(t, map[string]string{"info": info, name + "_update_V" + strings.ReplaceAll(version, ".", "_") + ".efw": randomBytes(t, 4096), "changelog.txt": "v" + version})
}

func TestArchivable(t *testing.T) {
	for typ, want := range map[string]bool{
		"HmIP-PDT": true, "HMIP-WRC2": true, "HmIPW-DRS8": true, "ELV-SH-CTH": true,
		"HM-LC-Sw1-Pl-2": false, "HMW-IO-12-Sw7-DR": false, "HmIP-HAP": false, "HmIP-HAP-B1": false, "HMIP-HAP JS1": false, "HmIPW-DRAP": false,
		"": false, "../x": false,
	} {
		if got := Archivable(typ); got != want {
			t.Errorf("Archivable(%q) = %v", typ, got)
		}
	}
}

// The list: HmIP device bundles only (no access point, no BidCos, nothing outside the three
// directories, no path game), per type newest first, a version listed twice newer build first;
// the tree is read once an hour.
func TestArchiveListAndFor(t *testing.T) {
	fa := newFakeArchive(t, nil)
	a := fa.archive()
	now := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	all, err := a.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range all {
		paths = append(paths, e.Path)
	}
	if got := strings.Join(paths, " "); got != "HmIP/HmIP-PDT_update_V1_4_8_170725.tgz HmIP/HmIP-PDT_update_V2_2_4_231123.tgz HmIP/HmIP-PDT-UK_update_V1_4_8_170804.tgz HmIP/HmIP-BBL_update_V1_8_10_190815.tgz HmIPW/HmIPW-DRS8_update_V1_2_6_220928.tgz ELV/ELV-SH-CTH_update_V1_0_10_240807.tgz HmIP/HmIP-WRC2_update_V1_18_0_220214.tgz HmIP/HmIP-WRC2_update_V1_18_2_230207.tgz HmIP/HmIP-WRC2_update_V1_18_2_240101.tgz" {
		t.Errorf("list: %s", got)
	}
	pdt, err := a.For(t.Context(), "HMIP-PDT")
	if err != nil || len(pdt) != 2 || pdt[0].Version != "2.2.4" || pdt[0].Date != "2023-11-23" || pdt[1].Version != "1.4.8" || pdt[0].Type != "HmIP-PDT" || pdt[0].Size != 98883 {
		t.Errorf("PDT: %+v %v", pdt, err)
	}
	wrc, _ := a.For(t.Context(), "HmIP-WRC2")
	if len(wrc) != 3 || wrc[0].Path != "HmIP/HmIP-WRC2_update_V1_18_2_240101.tgz" || wrc[2].Version != "1.18.0" {
		t.Errorf("WRC2: %+v", wrc)
	}
	if _, err := a.For(t.Context(), "HmIP-HAP"); !errors.Is(err, ErrNotArchivable) {
		t.Errorf("access point: %v", err)
	}
	if _, err := a.For(t.Context(), "HM-LC-Sw1-Pl-2"); !errors.Is(err, ErrNotArchivable) {
		t.Errorf("BidCos: %v", err)
	}
	if fa.treeCalls() != 1 {
		t.Errorf("tree read %d times within the hour", fa.treeCalls())
	}
	now = now.Add(61 * time.Minute)
	if _, err := a.List(t.Context()); err != nil || fa.treeCalls() != 2 {
		t.Errorf("after an hour: %d %v", fa.treeCalls(), err)
	}
}

// What occulited sends to GitHub for the archive (the fork's docs/privacy.md): the tree call
// and the one file - nothing of the system. A change here is a change to the privacy statement.
func TestArchiveRequestsArePinned(t *testing.T) {
	fa := newFakeArchive(t, map[string][]byte{"HmIP/HmIP-PDT_update_V1_4_8_170725.tgz": pdtBundle(t, "HmIP-PDT", "1.4.8", "")})
	a := fa.archive()
	if _, _, err := a.Download(t.Context(), "HmIP/HmIP-PDT_update_V1_4_8_170725.tgz", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /tree\nAccept: application/vnd.github+json\nAccept-Encoding: gzip\nUser-Agent: Go-http-client/1.1",
		"GET /raw/HmIP/HmIP-PDT_update_V1_4_8_170725.tgz\nAccept-Encoding: gzip\nUser-Agent: Go-http-client/1.1",
	}
	if got := fa.log.Requests(); strings.Join(got, "\n\n") != strings.Join(want, "\n\n") {
		t.Errorf("the requests changed - update docs/privacy.md in the fork with this test:\n%s", strings.Join(got, "\n\n"))
	}
}

// A fetch names a listed path only; the bundle goes into the held directory under its type and
// version, checked like a download from eQ-3; another type's bundle behind a name is refused and
// leaves nothing behind.
func TestFetchArchived(t *testing.T) {
	fa := newFakeArchive(t, map[string][]byte{
		"HmIP/HmIP-PDT_update_V1_4_8_170725.tgz":   pdtBundle(t, "HmIP-PDT", "1.4.8", "3.0.0"),
		"HmIP/HmIP-BBL_update_V1_8_10_190815.tgz":  pdtBundle(t, "HmIP-PDT", "1.8.10", ""),
		"HmIP/HmIP-WRC2_update_V1_18_0_220214.tgz": pdtBundle(t, "HmIP-WRC2", "1.18.0", "9.0.0"),
	})
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	writeHeldTestBundle(t, dir, "310", "HmIP-PDT", "310", "2.2.4")
	s := NewService(New("http://127.0.0.1:1"), dir, func() []interfaces.Interface { return nil }, nil)
	s.Archive = fa.archive()
	s.SystemVersion = "3.89.11.20260919"
	vs, err := s.ArchiveFor(t.Context(), "HmIP-PDT")
	if err != nil || len(vs) != 2 || !vs[0].OnSystem || vs[1].OnSystem {
		t.Fatalf("versions: %+v %v", vs, err)
	}
	b, err := s.FetchArchived(t.Context(), "HmIP/HmIP-PDT_update_V1_4_8_170725.tgz")
	if err != nil || !b.Held || b.Dir != "310-HmIP-PDT-1.4.8" || b.Version != "1.4.8" {
		t.Fatalf("fetch: %+v %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, HeldDirName, "310-HmIP-PDT-1.4.8", "info")); err != nil {
		t.Errorf("not held: %v", err)
	}
	if vs, _ := s.ArchiveFor(t.Context(), "HmIP-PDT"); !vs[1].OnSystem {
		t.Errorf("1.4.8 not marked on the system: %+v", vs)
	}
	for p, want := range map[string]error{
		"https://example.org/x.tgz":                ErrNotInArchive,
		"HM/HM-LC-Sw1-Pl-2_update_V2_8_0.tgz":      ErrNotInArchive,
		"HmIP/../x_update_V1_0_0.tgz":              ErrNotInArchive,
		"HmIP/HmIP-BBL_update_V1_8_10_190815.tgz":  ErrWrongBundle,
		"HmIP/HmIP-WRC2_update_V1_18_0_220214.tgz": ErrNeedsNewerSystem,
	} {
		if _, err := s.FetchArchived(t.Context(), p); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", p, err, want)
		}
	}
	held, _ := Held(filepath.Join(root, HeldDirName))
	if len(held) != 1 {
		t.Errorf("a refused fetch left a bundle: %+v", held)
	}
}

// The user's choice: an older held version delivered replaces the offered one, which stays held
// as the previous version; the type is pinned, so the check keeps a newer download held even with
// Deploy automatically on; the held directory keeps the newest and the previous version; the
// newest delivered again clears the pin.
func TestDeliverAnOlderVersionPinsTheType(t *testing.T) {
	var mu sync.Mutex
	latest := "2.2.4"
	eq3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		v := latest
		mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/search/DEVICE"):
			_, _ = w.Write([]byte(`homematic.com.setDeviceFirmwareVersions([{"type":"HmIP-PDT","version":"` + v + `"}])`))
		case strings.Contains(r.URL.Path, "/download"):
			w.Header().Set("Content-Disposition", `attachment; filename="HmIP-PDT_update_V`+strings.ReplaceAll(v, ".", "_")+`.tgz"`)
			_, _ = w.Write(pdtBundle(t, "HmIP-PDT", v, ""))
		}
	}))
	defer eq3.Close()
	itf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(raw), "listDevices") {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>` + device("00010000000A10", "HmIP-PDT", "2.2.0") + `</data></array></value></param></params></methodResponse>`))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><string></string></value></param></params></methodResponse>`))
	}))
	defer itf.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	held := filepath.Join(root, HeldDirName)
	writeHeldTestBundle(t, dir, "310", "HmIP-PDT", "310", "2.2.4")
	writeHeldTestBundle(t, held, "310-HmIP-PDT-1.4.8", "HmIP-PDT", "310", "1.4.8")
	s := NewService(New(eq3.URL), dir, func() []interfaces.Interface { return ifs(itf.URL) }, nil)
	s.Open(filepath.Join(root, "state.json"))
	s.SetAutoDeploy(true)
	b, err := s.SetTransfer("310-HmIP-PDT-1.4.8", true)
	if err != nil || b.Dir != "310" || b.Version != "1.4.8" {
		t.Fatalf("deliver 1.4.8: %+v %v", b, err)
	}
	if got := bundleVersionAt(t, filepath.Join(held, "310-HmIP-PDT-2.2.4")); got != "2.2.4" {
		t.Errorf("2.2.4 is not held as the previous version: %q", got)
	}
	st := s.Status()
	if len(st.PinnedTypes) != 1 || st.PinnedTypes[0] != "hmip-pdt" {
		t.Errorf("pinned: %v", st.PinnedTypes)
	}
	// eQ-3 publishes 2.2.6: the check downloads it into the held directory, 1.4.8 stays delivered,
	// the held directory keeps 2.2.6 (newest) and 2.2.4 (previous)
	mu.Lock()
	latest = "2.2.6"
	mu.Unlock()
	s.Check(context.Background())
	if st := s.Status(); st.LastError != "" {
		t.Fatalf("check: %s", st.LastError)
	}
	if got := bundleVersionAt(t, filepath.Join(dir, "310")); got != "1.4.8" {
		t.Errorf("the chosen version was replaced: %s", got)
	}
	hs, _ := Held(held)
	var vs []string
	for _, h := range hs {
		vs = append(vs, h.Dir)
	}
	if strings.Join(vs, " ") != "310-HmIP-PDT-2.2.4 310-HmIP-PDT-2.2.6" {
		t.Errorf("held: %v", vs)
	}
	// the newest delivered: the pin is gone, 1.4.8 is the previous version and stays held beside
	// 2.2.4, now the newest held
	if _, err := s.SetTransfer("310-HmIP-PDT-2.2.6", true); err != nil {
		t.Fatal(err)
	}
	st = s.Status()
	if len(st.PinnedTypes) != 0 {
		t.Errorf("still pinned: %v", st.PinnedTypes)
	}
	hs, _ = Held(held)
	vs = nil
	for _, h := range hs {
		vs = append(vs, h.Dir)
	}
	if strings.Join(vs, " ") != "310-HmIP-PDT-1.4.8 310-HmIP-PDT-2.2.4" {
		t.Errorf("held after delivering the newest: %v", vs)
	}
	// a restart keeps the previous version
	s2 := NewService(New(eq3.URL), dir, func() []interfaces.Interface { return ifs(itf.URL) }, nil)
	s2.Open(filepath.Join(root, "state.json"))
	s2.mu.Lock()
	prev := s2.prev["hmip-pdt"]
	s2.mu.Unlock()
	if prev != "1.4.8" {
		t.Errorf("previous after a restart: %q", prev)
	}
}
