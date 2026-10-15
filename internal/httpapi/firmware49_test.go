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

// occulited task 49: the OpenCCU firmware archive through the API.

type archiveFirmware struct {
	apFirmware
	fetched []string
	listErr error
}

func (f *archiveFirmware) ArchiveFor(_ context.Context, typ string) ([]firmware.ArchivedVersion, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if !firmware.Archivable(typ) {
		return nil, firmware.ErrNotArchivable
	}
	return []firmware.ArchivedVersion{{ArchiveEntry: firmware.ArchiveEntry{Type: "HmIP-PDT", Version: "2.2.4", Path: "HmIP/HmIP-PDT_update_V2_2_4_231123.tgz"}, OnSystem: true}, {ArchiveEntry: firmware.ArchiveEntry{Type: "HmIP-PDT", Version: "1.4.8", Path: "HmIP/HmIP-PDT_update_V1_4_8_170725.tgz"}}}, nil
}

func (f *archiveFirmware) FetchArchived(_ context.Context, p string) (*firmware.Bundle, error) {
	switch p {
	case "HmIP/HmIP-PDT_update_V1_4_8_170725.tgz":
		f.fetched = append(f.fetched, p)
		return &firmware.Bundle{TypeCode: "310", Dir: "310-HmIP-PDT-1.4.8", Name: "HmIP-PDT", Version: "1.4.8", Held: true}, nil
	case "HmIP/wrong.tgz":
		return nil, firmware.ErrWrongBundle
	}
	return nil, firmware.ErrNotInArchive
}

func TestFirmwareArchiveRoutes(t *testing.T) {
	fw := &archiveFirmware{}
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
	rec := call(http.MethodGet, "/api/system/v1/firmware/archive?type=HmIP-PDT", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"1.4.8"`) || !strings.Contains(rec.Body.String(), `"on_system":true`) || !strings.Contains(rec.Body.String(), `"source":"https://github.com/OpenCCU/HMDeviceFirmware"`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec = call(http.MethodGet, "/api/system/v1/firmware/archive?type=HM-LC-Sw1-Pl-2", ""); rec.Code != 422 || !strings.Contains(rec.Body.String(), "not-archivable") {
		t.Errorf("BidCos: %d %s", rec.Code, rec.Body.String())
	}
	fw.listErr = errors.New("firmware archive: HTTP 403")
	if rec = call(http.MethodGet, "/api/system/v1/firmware/archive?type=HmIP-PDT", ""); rec.Code != 502 || !strings.Contains(rec.Body.String(), "HTTP 403") {
		t.Errorf("GitHub down: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(http.MethodPost, "/api/system/v1/firmware/archive/fetch", `{"path": "HmIP/HmIP-PDT_update_V1_4_8_170725.tgz"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"dir":"310-HmIP-PDT-1.4.8"`) || !strings.Contains(rec.Body.String(), `"held":true`) || len(fw.fetched) != 1 {
		t.Errorf("fetch: %d %s", rec.Code, rec.Body.String())
	}
	for body, code := range map[string]int{`{"path": "https://example.org/x.tgz"}`: 404, `{"path": "HmIP/wrong.tgz"}`: 422, `{}`: 422} {
		if rec = call(http.MethodPost, "/api/system/v1/firmware/archive/fetch", body); rec.Code != code {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}
