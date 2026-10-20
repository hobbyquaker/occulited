package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/system"
)

// occulited task 68: POST /firmware/devices/{address}/install

type installFirmware struct {
	apFirmware
	asked []string
}

func (f *installFirmware) Install(_ context.Context, address string) (firmware.InstallResult, error) {
	f.asked = append(f.asked, address)
	switch address {
	case "0001D0000000B1":
		return firmware.InstallResult{Address: address, Type: "HmIP-PDT", From: "2.2.4", To: "2.2.6"}, nil
	case "0001D0000000B2":
		return firmware.InstallResult{Address: address, Type: "HmIP-PDT", Pending: true}, nil
	case "00010000000A10":
		return firmware.InstallResult{}, firmware.ErrNotReady
	case "00010000000A11":
		return firmware.InstallResult{}, firmware.ErrInstallRefused
	case "00010000000B01":
		return firmware.InstallResult{}, errors.New("no answer within 30s")
	}
	return firmware.InstallResult{}, firmware.ErrNoDevice
}

func TestFirmwareInstallRoute(t *testing.T) {
	fw := &installFirmware{}
	mux := http.NewServeMux()
	(&SystemAPI{Root: system.Root(t.TempDir()), Firmware: fw}).Register(mux)
	call := func(address string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/v1/firmware/devices/"+address+"/install", nil))
		return rec
	}
	for _, c := range []struct {
		address, want string
		code          int
	}{
		{"0001D0000000B1", `"install":{"address":"0001D0000000B1","type":"HmIP-PDT","from":"2.2.4","to":"2.2.6","pending":false}`, 200},
		{"0001D0000000B2", `"pending":true`, 200},
		{"00010000000A10", `"error":"not-ready"`, 409},
		{"00010000000A11", `"error":"install-refused"`, 409},
		{"00010000000B01", `"error":"interface-failed"`, 502},
		{"0001D0000000B9", `"error":"not-found"`, 404},
	} {
		rec := call(c.address)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.address, rec.Code, rec.Body.String())
		}
	}
	if len(fw.asked) != 6 {
		t.Errorf("asked %v", fw.asked)
	}
	// a firmware service without the install
	mux = http.NewServeMux()
	(&SystemAPI{Root: system.Root(t.TempDir()), Firmware: apFirmware{}}).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/v1/firmware/devices/0001D0000000B1/install", nil))
	if rec.Code != 501 {
		t.Errorf("without install: %d", rec.Code)
	}
}
