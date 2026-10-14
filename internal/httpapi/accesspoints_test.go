package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/system"
)

type apFirmware struct{ devices []firmware.DeviceStatus }

func (f apFirmware) Status() firmware.State                      { return firmware.State{Devices: f.devices} }
func (apFirmware) Trigger()                                      {}
func (apFirmware) SetEnabled(bool)                               {}
func (apFirmware) DeployUpload(string) (*firmware.Bundle, error) { return nil, nil }
func (apFirmware) BundleFile(string, string) ([]byte, error)     { return nil, nil }
func (apFirmware) SetTransfer(string, bool) (*firmware.Bundle, error) {
	return nil, nil
}
func (apFirmware) SetAutoDeploy(bool) {}

// apHmIP answers listDevices with a HAP-B1 and a PDT, or with the PDT alone when paired is false.
func apHmIP(t *testing.T, paired *atomic.Bool) *httptest.Server {
	t.Helper()
	dev := func(addr, typ, fw string) string {
		return `<value><struct><member><name>ADDRESS</name><value>` + addr + `</value></member><member><name>TYPE</name><value>` + typ +
			`</value></member><member><name>FIRMWARE</name><value>` + fw + `</value></member><member><name>PARENT</name><value></value></member></struct></value>`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		w.Header().Set("Content-Type", "text/xml")
		head, tail := `<?xml version="1.0"?><methodResponse><params><param><value>`, `</value></param></params></methodResponse>`
		switch {
		case strings.Contains(body, "listDevices"):
			list := dev("000A1B2C3D4E5F", "HmIP-PDT", "2.2.4")
			if paired.Load() {
				list += dev("00030000000A13", "HmIP-HAP-B1", "2.2.18")
			}
			_, _ = w.Write([]byte(head + `<array><data>` + list + `</data></array>` + tail))
		case strings.Contains(body, "getParamset"):
			_, _ = w.Write([]byte(head + `<struct><member><name>UNREACH</name><value><boolean>0</boolean></value></member><member><name>IP_ADDRESS</name><value>192.0.2.155</value></member></struct>` + tail))
		default:
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><int>-1</int></value></member><member><name>faultString</name><value>unknown</value></member></struct></value></fault></methodResponse>`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// openccu-lite task 217: the paired access points with SGTIN (from hmipserver's .dev files, TARGA's
// prefix), name, reachability, B-195's firmware data, and the firewall hint's four cases.
func TestAccessPoints(t *testing.T) {
	root := fakeRoot(t)
	data := filepath.Join(string(root), "etc/config/crRFD/data")
	_ = os.MkdirAll(data, 0o755)
	for _, f := range []string{"30150377DC00030000000A13.dev", "3014F711A0000A1B2C3D4E5F.dev", "3014F711A000040000000A01.ap", "linkData.conf"} {
		_ = os.WriteFile(filepath.Join(data, f), []byte("x"), 0o644)
	}
	var paired atomic.Bool
	paired.Store(true)
	hmip := apHmIP(t, &paired)
	var ifs []interfaces.Interface
	api := &SystemAPI{Root: root,
		RadioInterfaces: func() []interfaces.Interface { return ifs },
		Names: func(ref string) (string, []string, bool) {
			if ref == "HmIP-RF.00030000000A13" {
				return "Access point cellar", nil, true
			}
			return "", nil, false
		},
		Firmware: apFirmware{devices: []firmware.DeviceStatus{
			{Device: interfaces.Device{Interface: "HmIP-RF", Address: "00030000000A13", Type: "HmIP-HAP-B1", Firmware: "2.2.18"}, Latest: "2.2.20", UpdateAvailable: true},
		}},
	}
	mux := http.NewServeMux()
	api.Register(mux)
	role := auth.RoleUser
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: role, Scopes: auth.RoleScopes(role)})))
	}))
	t.Cleanup(srv.Close)
	get := func() accessPointsView {
		t.Helper()
		res, err := http.Get(srv.URL + "/api/system/v1/radio/hmip/access-points")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("status %d", res.StatusCode)
		}
		var v accessPointsView
		if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	writeRules := func(target string) {
		c := firewall.Config{Policy: firewall.Policy{V4: "DROP", V6: "DROP"}}
		for _, p := range firewall.HmIPAPPorts {
			r := firewall.OwnedRule(firewall.OwnerHmIPAP, p)
			if p.Port == 9294 {
				r.Target = target
			}
			c.Rules = append(c.Rules, r)
		}
		if err := root.WriteRules(c); err != nil {
			t.Fatal(err)
		}
	}

	// no HmIP-RF in the list, no rule file: nothing to say
	v := get()
	if v.Available || v.Firewall != nil || len(v.AccessPoints) != 0 || v.Interface != "HmIP-RF" {
		t.Errorf("without HmIP-RF: %+v", v)
	}
	ifs = interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(hmip.URL, "http://")}})
	writeRules("ACCEPT")
	v = get()
	if !v.Available || v.Error != "" || len(v.AccessPoints) != 1 {
		t.Fatalf("%+v", v)
	}
	ap := v.AccessPoints[0]
	if ap.SGTIN != "30150377DC00030000000A13" || ap.Name != "Access point cellar" || ap.Latest != "2.2.20" || !ap.UpdateAvailable ||
		ap.Reachable == nil || !*ap.Reachable || ap.IPAddress != "192.0.2.155" || ap.Type != "HmIP-HAP-B1" {
		t.Errorf("the HAP: %+v", ap)
	}
	if v.Firewall == nil || v.Firewall.Hint != "keep" || len(v.Firewall.Ports) != 3 || v.Firewall.Ports[1].Target != "ACCEPT" || v.Firewall.Ports[1].Owner != firewall.OwnerHmIPAP {
		t.Errorf("paired, open: %+v", v.Firewall)
	}
	writeRules("REJECT")
	if v = get(); v.Firewall.Hint != "blocked" || v.Firewall.Ports[1].Target != "REJECT" {
		t.Errorf("paired, one port rejected: %+v", v.Firewall)
	}
	paired.Store(false)
	if v = get(); v.Firewall.Hint != "reopen" || len(v.AccessPoints) != 0 {
		t.Errorf("none paired, one port rejected: %+v", v)
	}
	writeRules("ACCEPT")
	if v = get(); v.Firewall.Hint != "unused" {
		t.Errorf("none paired, open: %+v", v.Firewall)
	}
	// HmIP-RF silent: the generic hint, the error named
	hmip.Close()
	if v = get(); v.Firewall.Hint != "unknown" || v.Error == "" || !v.Available {
		t.Errorf("HmIP-RF silent: %+v", v)
	}
}

func TestHmIPDeviceSGTINs(t *testing.T) {
	root := system.Root(t.TempDir())
	if m := root.HmIPDeviceSGTINs(); len(m) != 0 {
		t.Errorf("no directory: %v", m)
	}
	data := filepath.Join(string(root), "etc/config/crRFD/data")
	_ = os.MkdirAll(data, 0o755)
	for _, f := range []string{"30150377dc00030000000a13.dev", "3014F711A000170000000A08.dev", "3014F711A000040000000A01.ap", "NOTANSGTIN000000000000000.dev", "metaData.conf"} {
		_ = os.WriteFile(filepath.Join(data, f), []byte("x"), 0o644)
	}
	m := root.HmIPDeviceSGTINs()
	if len(m) != 2 || m["00030000000A13"] != "30150377DC00030000000A13" || m["00170000000A08"] != "3014F711A000170000000A08" {
		t.Errorf("%v", m)
	}
}
