package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/system"
)

// The Firmware page's viewer: a bundle's changelog and info come back as UTF-8 text, everything
// else is refused with the status that says why.
func TestFirmwareBundleFileRoute(t *testing.T) {
	dir := t.TempDir()
	b := filepath.Join(dir, "4107")
	_ = os.MkdirAll(b, 0o755)
	w := func(name, data string) {
		if err := os.WriteFile(filepath.Join(b, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("info", "TypeCode=4107\nName=HmIPW-DRS8\nFirmwareVersion=0.0.0\n")
	w("changelog.txt", "Version 1.2.6\n")
	w("latin1.txt", "f\xfcr\n")
	w("HmIPW-DRS8_update_V1_2_6_220928.efw", "\x00\x01")
	w("big.log", strings.Repeat("x", firmware.MaxViewSize+10))
	_ = os.WriteFile(filepath.Join(dir, "secret"), []byte("root only"), 0o644)
	_ = os.Symlink(filepath.Join(dir, "secret"), filepath.Join(b, "notes.txt"))

	mux := http.NewServeMux()
	(&SystemAPI{Root: system.Root(t.TempDir()), Firmware: firmware.NewService(nil, dir, nil, nil)}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cases := []struct {
		name   string
		path   string
		status int
		body   string // the text of a 200, the error code otherwise
	}{
		{"the changelog", "4107/files/changelog.txt", 200, "Version 1.2.6\n"},
		{"the info file", "4107/files/info", 200, "TypeCode=4107\nName=HmIPW-DRS8\nFirmwareVersion=0.0.0\n"},
		{"a Latin-1 file as UTF-8", "4107/files/latin1.txt", 200, "für\n"},
		{"a name not in the bundle", "4107/files/readme.txt", 404, "not-found"},
		{"a symlink", "4107/files/notes.txt", 404, "not-found"},
		{"an escaped slash", "4107/files/..%2Fsecret", 404, ""},
		{"a firmware image", "4107/files/HmIPW-DRS8_update_V1_2_6_220928.efw", 415, "not-viewable"},
		{"over the cap", "4107/files/big.log", 413, "too-large"},
		{"an unknown bundle", "310/files/info", 404, "not-found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := http.Get(srv.URL + "/api/system/v1/firmware/bundles/" + c.path)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != c.status {
				t.Fatalf("status %d, want %d: %s", res.StatusCode, c.status, raw)
			}
			if strings.Contains(string(raw), "root only") {
				t.Fatal("the file outside the bundle was served")
			}
			if c.status == 200 {
				if ct := res.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
					t.Errorf("content type %q", ct)
				}
				if res.Header.Get("X-Content-Type-Options") != "nosniff" {
					t.Error("no nosniff")
				}
				if string(raw) != c.body {
					t.Errorf("body %q, want %q", raw, c.body)
				}
				return
			}
			if c.body == "" {
				return
			}
			var e apiError
			if err := json.Unmarshal(raw, &e); err != nil || e.Error != c.body {
				t.Fatalf("error %q (%v), want %q: %s", e.Error, err, c.body, raw)
			}
			if c.status == 413 && (e.Detail["size"] != float64(firmware.MaxViewSize+10) || e.Detail["limit"] != float64(firmware.MaxViewSize)) {
				t.Errorf("the size is not in the answer: %v", e.Detail)
			}
		})
	}

	// without a firmware service the route says so
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: system.Root(t.TempDir())}).Register(mux2)
	rec := httptest.NewRecorder()
	mux2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/system/v1/firmware/bundles/4107/files/info", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no service: %d", rec.Code)
	}
}

// Task 40: GET /firmware names each paired device from the metadata store, so the Firmware page
// lists and finds a type's devices by name; a device the store does not know has no name, and the
// service's own state keeps none.
func TestFirmwareStatusNamesDevices(t *testing.T) {
	devs := []firmware.DeviceStatus{
		{Device: interfaces.Device{Interface: "HmIP-RF", Address: "0001D0000000B1", Type: "HmIP-PDT", Firmware: "2.2.4"}, Latest: "2.2.4"},
		{Device: interfaces.Device{Interface: "BidCos-RF", Address: "JEQ9000001", Type: "HM-CC-TC", Firmware: "2.1"}, NotListed: true},
	}
	asked := []string{}
	mux := http.NewServeMux()
	(&SystemAPI{Root: system.Root(t.TempDir()), Firmware: apFirmware{devices: devs}, Names: func(ref string) (string, []string, bool) {
		asked = append(asked, ref)
		if ref == "HmIP-RF.0001D0000000B1" {
			return "Plug kitchen", nil, true
		}
		return "", nil, false
	}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/api/system/v1/firmware")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var got struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Devices) != 2 {
		t.Fatalf("devices %v", got.Devices)
	}
	if got.Devices[0]["name"] != "Plug kitchen" || got.Devices[0]["type"] != "HmIP-PDT" || got.Devices[0]["latest"] != "2.2.4" {
		t.Errorf("first device %v", got.Devices[0])
	}
	if _, ok := got.Devices[1]["name"]; ok || got.Devices[1]["not_listed"] != true {
		t.Errorf("second device %v", got.Devices[1])
	}
	if strings.Join(asked, " ") != "HmIP-RF.0001D0000000B1 BidCos-RF.JEQ9000001" {
		t.Errorf("asked %v", asked)
	}
	if devs[0].Interface != "HmIP-RF" {
		t.Error("the state was changed")
	}
}

// Without a metadata store the devices come out as the service has them, never null.
func TestFirmwareViewWithoutNames(t *testing.T) {
	v := firmwareView(firmware.State{}, nil)
	if v.Devices == nil || len(v.Devices) != 0 {
		t.Fatalf("devices %#v", v.Devices)
	}
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `"devices":[]`) {
		t.Errorf("json %s", raw)
	}
}

// occulited task 65: the radio modules hmipserver and rfd list as devices are marked, so the device
// firmware list leaves them out; an access point is a device and is not
func TestFirmwareViewMarksRadioModules(t *testing.T) {
	st := firmware.State{Devices: []firmware.DeviceStatus{
		{Device: interfaces.Device{Interface: "HmIP-RF", Address: "001F0000000A03", Type: "RPI-RF-MOD", Firmware: "4.4.22"}},
		{Device: interfaces.Device{Interface: "HmIP-RF", Address: "001F0000000A04", Type: "HmIP-RFUSB", Firmware: "4.4.18"}},
		{Device: interfaces.Device{Interface: "HmIP-RF", Address: "00040000000A01", Type: "HmIP-HAP", Firmware: "2.4.6"}},
		{Device: interfaces.Device{Interface: "HmIP-RF", Address: "0001D0000000B1", Type: "HmIP-PDT", Firmware: "2.2.4"}},
	}}
	v := firmwareView(st, nil)
	got := []bool{}
	for _, d := range v.Devices {
		got = append(got, d.RadioModule)
	}
	if fmt.Sprint(got) != "[true true false false]" {
		t.Errorf("radio_module %v", got)
	}
	raw, _ := json.Marshal(v.Devices[0])
	if !strings.Contains(string(raw), `"radio_module":true`) {
		t.Errorf("json %s", raw)
	}
	raw, _ = json.Marshal(v.Devices[2])
	if strings.Contains(string(raw), `radio_module`) {
		t.Errorf("an access point is marked: %s", raw)
	}
}
