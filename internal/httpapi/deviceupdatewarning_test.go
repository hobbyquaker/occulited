package httpapi

import (
	"context"
	"sync"
	"testing"

	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// readyFirmware is a firmware service whose devices the test changes between evaluations.
type readyFirmware struct {
	apFirmware
	mu      sync.Mutex
	devices []firmware.DeviceStatus
}

func (f *readyFirmware) Status() firmware.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return firmware.State{Devices: append([]firmware.DeviceStatus(nil), f.devices...)}
}

func (f *readyFirmware) set(d ...firmware.DeviceStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devices = d
}

func fwDevice(iface, addr, typ, state, available string) firmware.DeviceStatus {
	return firmware.DeviceStatus{Device: interfaces.Device{Interface: iface, Address: addr, Type: typ, Firmware: "1.2.0", AvailableFirmware: available, UpdateState: state}}
}

// occulited task 126: device-update-ready - one warning for the HmIP devices whose firmware is
// transferred and waits for the install; named from the metadata, linked to their rows on the
// Updates page; a silence per device and version; gone once installed, no longer ready, or deleted.
func TestDeviceUpdateReadyWarning(t *testing.T) {
	const (
		living = "00010000000A10"
		bath   = "00010000000A11"
		hall   = "00030000000A13"
	)
	fw := &readyFirmware{}
	names := map[string]string{"HmIP-RF." + living: "Wohnzimmer Thermostat", "HmIP-RF." + bath: "Bad Thermostat"}
	a := &SystemAPI{Firmware: fw, Names: func(ref string) (string, []string, bool) {
		n, ok := names[ref]
		return n, nil, ok
	}}
	tr := &warnings.Tracker{Sources: []warnings.Source{{IDs: []string{DeviceUpdateReadyID}, Lists: true, Eval: a.deviceUpdateReadyWarning}}}
	ctx := context.Background()
	active := func() []warnings.Warning {
		t.Helper()
		var out []warnings.Warning
		for _, w := range tr.List(ctx, "admin", true) {
			if w.Silenced == nil {
				out = append(out, w)
			}
		}
		return out
	}

	// nothing ready: transferring, queued, up to date, installing, a BidCos device with a bundle, a radio module
	fw.set(fwDevice("HmIP-RF", living, "HmIP-eTRV-2", "DELIVER_FIRMWARE_IMAGE", "1.4.8"),
		fwDevice("HmIP-RF", bath, "HmIP-eTRV-2", "NEW_FIRMWARE_AVAILABLE", "1.4.8"),
		fwDevice("HmIP-RF", hall, "HmIP-WTH-2", "PERFORMING_UPDATE", "1.6.2"),
		fwDevice("BidCos-RF", "JEQ0000001", "HM-CC-RT-DN", "", "1.5"),
		fwDevice("HmIP-RF", "3014F711A00000000000000A", "RPI-RF-MOD", "READY_FOR_UPDATE", "4.4.18"))
	if ws := active(); len(ws) != 0 {
		t.Fatalf("nothing ready: %+v", ws)
	}

	// one ready
	fw.set(fwDevice("HmIP-RF", living, "HmIP-eTRV-2", "READY_FOR_UPDATE", "1.4.8"),
		fwDevice("HmIP-RF", bath, "HmIP-eTRV-2", "DELIVER_FIRMWARE_IMAGE", "1.4.8"))
	ws := active()
	if len(ws) != 1 {
		t.Fatalf("one ready: %+v", ws)
	}
	w := ws[0]
	if w.ID != "device-update-ready" || w.Variant != living+"@1.4.8" || w.Severity != warnings.SeverityWarning || w.Href != "/system/updates?device="+living+"#device-firmware" || w.Params["count"] != 1 {
		t.Fatalf("%+v", w)
	}
	list, _ := w.Params["updates"].([]map[string]any)
	if len(list) != 1 || list[0]["address"] != living || list[0]["name"] != "Wohnzimmer Thermostat" || list[0]["type"] != "HmIP-eTRV-2" || list[0]["version"] != "1.4.8" {
		t.Fatalf("params: %+v", w.Params)
	}

	// silenced for 1.4.8
	if _, err := tr.Silence(ctx, "admin", w.ID, w.Variant, 7); err != nil {
		t.Fatal(err)
	}
	if ws := active(); len(ws) != 0 {
		t.Fatalf("silenced: %+v", ws)
	}

	// a second device ready: a new set, shown with both - named ones first by name, the unnamed last
	fw.set(fwDevice("HmIP-RF", living, "HmIP-eTRV-2", "READY_FOR_UPDATE", "1.4.8"),
		fwDevice("HmIP-RF", bath, "HmIP-eTRV-2", "READY_FOR_UPDATE", "1.4.8"),
		fwDevice("HmIP-RF", hall, "HmIP-WTH-2", "READY_FOR_UPDATE", "0.0.0"))
	ws = active()
	// the variant sorted by address, the list by name
	if len(ws) != 1 || ws[0].Variant != living+"@1.4.8,"+bath+"@1.4.8,"+hall+"@" {
		t.Fatalf("three ready: %+v", ws)
	}
	w = ws[0]
	list, _ = w.Params["updates"].([]map[string]any)
	if len(list) != 3 || list[0]["name"] != "Bad Thermostat" || list[1]["name"] != "Wohnzimmer Thermostat" || list[2]["address"] != hall || list[2]["name"] != "" || list[2]["version"] != "" {
		t.Fatalf("order and names: %+v", list)
	}
	if w.Href != "/system/updates?device="+bath+","+living+","+hall+"#device-firmware" || w.Params["count"] != 3 {
		t.Fatalf("href: %+v", w)
	}
	if _, err := tr.Silence(ctx, "admin", w.ID, w.Variant, 7); err != nil {
		t.Fatal(err)
	}

	// two of them installed: the rest stays silenced (a subset of the silenced set)
	fw.set(fwDevice("HmIP-RF", living, "HmIP-eTRV-2", "UP_TO_DATE", ""),
		fwDevice("HmIP-RF", bath, "HmIP-eTRV-2", "DO_UPDATE_PENDING", "1.4.8"),
		fwDevice("HmIP-RF", hall, "HmIP-WTH-2", "READY_FOR_UPDATE", "0.0.0"))
	if all := tr.List(ctx, "admin", true); len(all) != 1 || all[0].Silenced == nil || all[0].Variant != hall+"@" {
		t.Fatalf("a subset after a silence: %+v", all)
	}
	// a newer version for the living room's device later: new
	fw.set(fwDevice("HmIP-RF", living, "HmIP-eTRV-2", "READY_FOR_UPDATE", "1.4.9"),
		fwDevice("HmIP-RF", hall, "HmIP-WTH-2", "READY_FOR_UPDATE", "0.0.0"))
	if ws := active(); len(ws) != 1 || ws[0].Variant != living+"@1.4.9,"+hall+"@" {
		t.Fatalf("1.4.9 after a silence of 1.4.8: %+v", ws)
	}

	// deleted (no longer listed): gone
	fw.set()
	if all := tr.List(ctx, "admin", true); len(all) != 0 {
		t.Fatalf("deleted: %+v", all)
	}

	// no firmware service
	if ws, ok := (&SystemAPI{}).deviceUpdateReadyWarning(ctx); !ok || len(ws) != 0 {
		t.Fatalf("%v %+v", ok, ws)
	}
}
