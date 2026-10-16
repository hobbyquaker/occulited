package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/system"
)

// occulited task 45: the per-type switch and the global one through the API.

// switchFirmware records what the two switches were set to.
type switchFirmware struct {
	apFirmware
	transfers  map[string]bool
	autoDeploy bool
	enabled    bool
}

func (f *switchFirmware) Status() firmware.State {
	return firmware.State{Enabled: f.enabled, AutoDeploy: f.autoDeploy, Devices: []firmware.DeviceStatus{}, Deployed: []firmware.Bundle{}, LastResult: []firmware.TypeResult{}}
}
func (f *switchFirmware) SetEnabled(on bool)    { f.enabled = on }
func (f *switchFirmware) SetAutoDeploy(on bool) { f.autoDeploy = on }
func (f *switchFirmware) SetTransfer(dir string, on bool) (*firmware.Bundle, error) {
	if dir == "95" && !on {
		return nil, firmware.ErrBidCosNoHold
	}
	if dir != "332" {
		return nil, firmware.ErrNoBundle
	}
	f.transfers[dir] = on
	return &firmware.Bundle{TypeCode: "332", Dir: "332", Name: "HmIP-BBL", Version: "1.10.16", Held: !on}, nil
}

func TestFirmwareTransferRoute(t *testing.T) {
	fw := &switchFirmware{transfers: map[string]bool{}, autoDeploy: true, enabled: true}
	mux := http.NewServeMux()
	(&SystemAPI{Root: system.Root(t.TempDir()), Firmware: fw}).Register(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := call(http.MethodPut, "/api/system/v1/firmware/bundles/332/transfer", `{"transfer": false}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"held":true`) || fw.transfers["332"] != false {
		t.Errorf("hold: %d %s %v", rec.Code, rec.Body.String(), fw.transfers)
	}
	rec = call(http.MethodPut, "/api/system/v1/firmware/bundles/332/transfer", `{"transfer": true}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"held":true`) || !fw.transfers["332"] {
		t.Errorf("offer: %d %s", rec.Code, rec.Body.String())
	}
	if rec = call(http.MethodPut, "/api/system/v1/firmware/bundles/999/transfer", `{"transfer": false}`); rec.Code != 404 {
		t.Errorf("unknown bundle: %d %s", rec.Code, rec.Body.String())
	}
	// occulited B-78: a hold on a BidCos bundle is refused
	if rec = call(http.MethodPut, "/api/system/v1/firmware/bundles/95/transfer", `{"transfer": false}`); rec.Code != 422 || !strings.Contains(rec.Body.String(), `"error":"bidcos-not-held"`) {
		t.Errorf("hold on BidCos: %d %s", rec.Code, rec.Body.String())
	}
	if rec = call(http.MethodPut, "/api/system/v1/firmware/bundles/332/transfer", `{}`); rec.Code != 422 {
		t.Errorf("without transfer: %d %s", rec.Code, rec.Body.String())
	}
	// the settings: each switch alone, the answer says both
	rec = call(http.MethodPut, "/api/system/v1/firmware/settings", `{"auto_deploy": false}`)
	if rec.Code != 200 || fw.autoDeploy || !fw.enabled || !strings.Contains(rec.Body.String(), `"auto_deploy":false`) || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Errorf("auto_deploy off: %d %s %+v", rec.Code, rec.Body.String(), fw)
	}
	rec = call(http.MethodPut, "/api/system/v1/firmware/settings", `{"enabled": false}`)
	if rec.Code != 200 || fw.enabled || fw.autoDeploy {
		t.Errorf("enabled off: %d %s %+v", rec.Code, rec.Body.String(), fw)
	}
	if rec = call(http.MethodPut, "/api/system/v1/firmware/settings", `{}`); rec.Code != 422 {
		t.Errorf("empty settings: %d %s", rec.Code, rec.Body.String())
	}
}
