package paramsets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// A fake interface process: a wall thermostat (HmIP-WTH-2) and a valve (HmIP-eTRV-2) that share
// the channel type HEATING_CLIMATECONTROL_TRANSCEIVER and differ in their VALUES paramset, as
// hmipserver describes them.
type fakeProcess struct {
	mu      sync.Mutex
	devices map[string]map[string]any // address → device description (JSON form)
	descs   map[string]map[string]any // address|paramset → description
	calls   []string
	fail    bool
}

func newFake() *fakeProcess {
	f := &fakeProcess{devices: map[string]map[string]any{}, descs: map[string]map[string]any{}}
	f.addDevice("000A1", "HmIP-WTH-2", "1.0.14", 1, []string{"000A1:0", "000A1:1"})
	f.addChannel("000A1:1", "HEATING_CLIMATECONTROL_TRANSCEIVER", "000A1", "HmIP-WTH-2",
		map[string]any{"SET_POINT_TEMPERATURE": param("FLOAT", 7, 4.5, 30.5), "HUMIDITY": param("INTEGER", 5, 0, 100)})
	f.addDevice("000B1", "HmIP-eTRV-2", "2.0.2", 1, []string{"000B1:0", "000B1:1"})
	f.addChannel("000B1:1", "HEATING_CLIMATECONTROL_TRANSCEIVER", "000B1", "HmIP-eTRV-2",
		map[string]any{"SET_POINT_TEMPERATURE": param("FLOAT", 7, 4.5, 30.5), "LEVEL": param("FLOAT", 5, 0, 1), "VALVE_STATE": param("INTEGER", 5, 0, 4)})
	// a second valve of the same model and firmware: the same key
	f.addDevice("000B2", "HmIP-eTRV-2", "2.0.2", 1, []string{"000B2:0", "000B2:1"})
	f.addChannel("000B2:1", "HEATING_CLIMATECONTROL_TRANSCEIVER", "000B2", "HmIP-eTRV-2", f.descs["000B1:1|VALUES"])
	return f
}

func param(typ string, ops int, min, max float64) map[string]any {
	return map[string]any{"TYPE": typ, "OPERATIONS": ops, "FLAGS": 1, "MIN": min, "MAX": max, "DEFAULT": min, "ID": "x", "UNIT": ""}
}

func (f *fakeProcess) addDevice(addr, typ, fw string, ver int, children []string) {
	ch := make([]any, len(children))
	for i, c := range children {
		ch[i] = c
	}
	f.devices[addr] = map[string]any{"ADDRESS": addr, "TYPE": typ, "FIRMWARE": fw, "VERSION": ver, "CHILDREN": ch, "PARAMSETS": []any{"MASTER"}}
	f.devices[addr+":0"] = map[string]any{"ADDRESS": addr + ":0", "TYPE": "MAINTENANCE", "PARENT": addr, "PARENT_TYPE": typ, "PARAMSETS": []any{"MASTER", "VALUES"}}
	f.descs[addr+":0|VALUES"] = map[string]any{"UNREACH": param("BOOL", 5, 0, 1)}
}

func (f *fakeProcess) addChannel(addr, typ, parent, parentType string, values map[string]any) {
	f.devices[addr] = map[string]any{"ADDRESS": addr, "TYPE": typ, "PARENT": parent, "PARENT_TYPE": parentType, "PARAMSETS": []any{"MASTER", "VALUES", "LINK"}}
	f.descs[addr+"|VALUES"] = values
	f.descs[addr+"|MASTER"] = map[string]any{"TEMPERATURE_OFFSET": param("FLOAT", 3, -3.5, 3.5)}
}

func (f *fakeProcess) forward(_ context.Context, iface, method string, params ...*xmlrpc.Value) (*xmlrpc.Value, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	if f.fail {
		return nil, errors.New("down")
	}
	switch method {
	case "getDeviceDescription":
		d, ok := f.devices[xmlrpc.Q(params[0]).String()]
		if !ok {
			return nil, &xmlrpc.MethodError{Code: -2, Message: "Unknown instance"}
		}
		return literpc.FromJSON(d)
	case "getParamsetDescription":
		d, ok := f.descs[xmlrpc.Q(params[0]).String()+"|"+xmlrpc.Q(params[1]).String()]
		if !ok {
			return nil, &xmlrpc.MethodError{Code: -2, Message: "Unknown instance"}
		}
		return literpc.FromJSON(d)
	case "listDevices":
		var out []any
		for _, d := range f.devices {
			out = append(out, d)
		}
		return literpc.FromJSON(out)
	}
	return nil, errors.New("unknown method " + method)
}

func (f *fakeProcess) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}

func TestKey(t *testing.T) {
	p := Device{Type: "HmIP-eTRV-2", Firmware: "2.0.2", Version: 1}
	for _, c := range []struct{ ch, ps, want string }{
		{"HEATING_CLIMATECONTROL_TRANSCEIVER", "VALUES", "HmIP-RF/HmIP-eTRV-2/2.0.2/1/HEATING_CLIMATECONTROL_TRANSCEIVER/VALUES"},
		{"", "MASTER", "HmIP-RF/HmIP-eTRV-2/2.0.2/1//MASTER"},
		{"HEATING_CLIMATECONTROL_TRANSCEIVER", "000ABC:1", "HmIP-RF/HmIP-eTRV-2/2.0.2/1/HEATING_CLIMATECONTROL_TRANSCEIVER/LINK"},
	} {
		if got := Key("HmIP-RF", p, c.ch, c.ps); got != c.want {
			t.Errorf("Key(%q, %q) = %q, want %q", c.ch, c.ps, got, c.want)
		}
	}
	// nrcc's own keys pass the shape check
	for _, k := range []string{"BidCos-RF/HM-CC-RT-DN-BoM/1.1/4//MASTER", "BidCos-RF/HM-CC-RT-DN-BoM/1.1/4/CLIMATECONTROL_RECEIVER/VALUES"} {
		if !validKey(k) {
			t.Errorf("%q not a valid key", k)
		}
	}
	if validKey("x") || validKey("a/b/c/d/e/") {
		t.Error("a malformed key passed")
	}
}

// Two models sharing a channel type name get their own description; the same model again is
// served from the cache without a call; the device descriptions are asked once each.
func TestDescribeByModel(t *testing.T) {
	f := newFake()
	c := &Cache{Forward: f.forward}
	ctx := context.Background()
	wk, wth, err := c.Describe(ctx, "HmIP-RF", "000A1:1", "VALUES")
	if err != nil {
		t.Fatal(err)
	}
	ek, etrv, err := c.Describe(ctx, "HmIP-RF", "000B1:1", "VALUES")
	if err != nil {
		t.Fatal(err)
	}
	if wk == ek || wk != "HmIP-RF/HmIP-WTH-2/1.0.14/1/HEATING_CLIMATECONTROL_TRANSCEIVER/VALUES" {
		t.Fatalf("keys: %q %q", wk, ek)
	}
	if _, ok := wth["HUMIDITY"]; !ok {
		t.Error("the wall thermostat lost HUMIDITY")
	}
	if _, ok := etrv["LEVEL"]; !ok {
		t.Error("the valve lost LEVEL")
	}
	if _, ok := wth["LEVEL"]; ok {
		t.Error("the wall thermostat got the valve's LEVEL")
	}
	// the second valve: the same key, no call
	before := f.count("getParamsetDescription")
	k2, _, err := c.Describe(ctx, "HmIP-RF", "000B2:1", "VALUES")
	if err != nil || k2 != ek || f.count("getParamsetDescription") != before {
		t.Fatalf("the second valve: %q %v, calls %d → %d", k2, err, before, f.count("getParamsetDescription"))
	}
	if c.Filled != 2 {
		t.Errorf("filled %d, want 2", c.Filled)
	}
	// the XML-RPC answer: a FLOAT's bounds are doubles whatever they look like
	v, err := c.Description(ctx, "HmIP-RF", "000B1:1", "VALUES")
	if err != nil {
		t.Fatal(err)
	}
	level := xmlrpc.Q(v).Key("LEVEL")
	if level.Key("MIN").Float64() != 0 || level.Key("MAX").Float64() != 1 || level.Err() != nil {
		t.Errorf("LEVEL bounds: %v", level.Err())
	}
	if level.Key("MIN").Value().Double == "" {
		t.Error("a FLOAT's MIN 0 came back as an integer")
	}
	if xmlrpc.Q(v).Key("VALVE_STATE").Key("MIN").Int() != 0 || xmlrpc.Q(v).Key("VALVE_STATE").Key("MAX").Int() != 4 {
		t.Error("an INTEGER's bounds")
	}
}

// The file: written after the delay in nrcc's schema, read back at the next start, and nothing
// is asked for a key the file holds. A firmware change makes a new key, which is filled.
func TestPersistAndFirmwareChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, FileName)
	f := newFake()
	var fired func()
	c := &Cache{File: file, Forward: f.forward, AfterFunc: func(_ time.Duration, fn func()) *time.Timer { fired = fn; return time.NewTimer(time.Hour) }}
	if err := c.Load(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := c.Describe(ctx, "HmIP-RF", "000A1:1", "VALUES"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("written before the delay")
	}
	fired()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"HmIP-RF/HmIP-WTH-2/1.0.14/1/HEATING_CLIMATECONTROL_TRANSCEIVER/VALUES": {`; !contains(string(b), want) {
		t.Fatalf("the file:\n%s", b)
	}
	// the next start: loaded, served without a call
	f2 := newFake()
	c2 := &Cache{File: file, Forward: f2.forward}
	if err := c2.Load(); err != nil {
		t.Fatal(err)
	}
	if c2.Len() != 1 {
		t.Fatalf("loaded %d", c2.Len())
	}
	if _, _, err := c2.Describe(ctx, "HmIP-RF", "000A1:1", "VALUES"); err != nil || f2.count("getParamsetDescription") != 0 {
		t.Fatalf("served from the file: %v, calls %d", err, f2.count("getParamsetDescription"))
	}
	// the wall thermostat gets a firmware update: updateDevice forgets it, the new key is asked
	f2.mu.Lock()
	f2.devices["000A1"]["FIRMWARE"] = "1.0.20"
	f2.mu.Unlock()
	c2.Forget("HmIP-RF", []string{"000A1"})
	k, _, err := c2.Describe(ctx, "HmIP-RF", "000A1:1", "VALUES")
	if err != nil || k != "HmIP-RF/HmIP-WTH-2/1.0.20/1/HEATING_CLIMATECONTROL_TRANSCEIVER/VALUES" || f2.count("getParamsetDescription") != 1 {
		t.Fatalf("after the firmware change: %q %v, calls %d", k, err, f2.count("getParamsetDescription"))
	}
	if c2.Len() != 2 {
		t.Errorf("the old key is kept beside the new: %d", c2.Len())
	}
	// Save now (the shutdown's): both keys in the file
	if err := c2.Save(); err != nil {
		t.Fatal(err)
	}
	c3 := &Cache{File: file}
	if err := c3.Load(); err != nil || c3.Len() != 2 {
		t.Fatalf("reloaded: %v %d", err, c3.Len())
	}
	// an unreadable file is an error of Load, and the cache still works
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	c4 := &Cache{File: file, Forward: f2.forward}
	if err := c4.Load(); err == nil {
		t.Error("a broken file loaded without a word")
	}
	if _, _, err := c4.Describe(ctx, "HmIP-RF", "000B1:1", "VALUES"); err != nil {
		t.Fatal(err)
	}
}

// Many callers asking the same new key at once make one process call.
func TestSingleFlight(t *testing.T) {
	f := newFake()
	c := &Cache{Forward: f.forward}
	var wg sync.WaitGroup
	var errs atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := c.Describe(context.Background(), "HmIP-RF", "000B1:1", "VALUES"); err != nil {
				errs.Add(1)
			}
		}()
	}
	wg.Wait()
	if errs.Load() != 0 || f.count("getParamsetDescription") != 1 {
		t.Fatalf("errors %d, calls %d", errs.Load(), f.count("getParamsetDescription"))
	}
	// a failing process: every caller gets the error, nothing is cached, and the next try asks again
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	if _, _, err := c.Describe(context.Background(), "HmIP-RF", "000A1:1", "VALUES"); err == nil {
		t.Fatal("no error from a process that is down")
	}
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	if _, _, err := c.Describe(context.Background(), "HmIP-RF", "000A1:1", "VALUES"); err != nil {
		t.Fatal(err)
	}
}

// The sweep: one listDevices, every device remembered, one description per VALUES key the cache
// lacks (channels of the same model and firmware once), nothing for the devices themselves or
// for MASTER.
func TestSweep(t *testing.T) {
	f := newFake()
	c := &Cache{Forward: f.forward}
	ctx := context.Background()
	v, err := f.forward(ctx, "HmIP-RF", "listDevices")
	if err != nil {
		t.Fatal(err)
	}
	asked, err := c.Sweep(ctx, "HmIP-RF", v)
	if err != nil {
		t.Fatal(err)
	}
	// keys: WTH :0 (MAINTENANCE), WTH :1, eTRV :0, eTRV :1 - the second valve shares the eTRV keys
	if asked != 4 || c.Len() != 4 || f.count("getParamsetDescription") != 4 || f.count("getDeviceDescription") != 0 {
		t.Fatalf("asked %d held %d, descriptions %d, device lookups %d", asked, c.Len(), f.count("getParamsetDescription"), f.count("getDeviceDescription"))
	}
	// everything the sweep saw is served without a call afterwards
	if _, _, err := c.Describe(ctx, "HmIP-RF", "000B2:1", "VALUES"); err != nil || f.count("getParamsetDescription") != 4 || f.count("getDeviceDescription") != 0 {
		t.Fatalf("after the sweep: %v, %d %d", err, f.count("getParamsetDescription"), f.count("getDeviceDescription"))
	}
	// a second sweep asks nothing
	if asked, err := c.Sweep(ctx, "HmIP-RF", v); err != nil || asked != 0 {
		t.Fatalf("the second sweep asked %d, %v", asked, err)
	}
}

// The handler on the bus: an interface up sweeps; a new device is described; an updated one is
// forgotten and described again.
func TestHandler(t *testing.T) {
	f := newFake()
	c := &Cache{Forward: f.forward}
	done := make(chan struct{}, 10)
	h := &Handler{Cache: c, Timeout: 5 * time.Second, Done: func(string, int, error) { done <- struct{}{} }}
	h.Interface("HmIP-RF", "down", time.Now())
	h.Interface("HmIP-RF", "up", time.Now())
	<-done
	if c.Len() != 4 {
		t.Fatalf("after the up: %d", c.Len())
	}
	// a new device of a model not seen: described
	f.mu.Lock()
	f.addDevice("000C1", "HmIP-PSM", "2.6.2", 1, []string{"000C1:0", "000C1:3"})
	f.addChannel("000C1:3", "SWITCH_VIRTUAL_RECEIVER", "000C1", "HmIP-PSM", map[string]any{"STATE": param("BOOL", 7, 0, 1)})
	f.mu.Unlock()
	h.Devices("HmIP-RF", "new", []string{"000C1"})
	<-done
	if _, ok := c.Get("HmIP-RF/HmIP-PSM/2.6.2/1/SWITCH_VIRTUAL_RECEIVER/VALUES"); !ok {
		t.Fatalf("the new device's channel is not described: %v", c.Keys())
	}
	// its firmware changes: updateDevice → the new key
	f.mu.Lock()
	f.devices["000C1"]["FIRMWARE"] = "2.6.4"
	f.mu.Unlock()
	h.Devices("HmIP-RF", "updated", []string{"000C1"})
	<-done
	if _, ok := c.Get("HmIP-RF/HmIP-PSM/2.6.4/1/SWITCH_VIRTUAL_RECEIVER/VALUES"); !ok {
		t.Fatalf("the updated device's new key is not described: %v", c.Keys())
	}
	h.Devices("HmIP-RF", "deleted", []string{"000C1"})
	h.Event(rpcsub.Event{})
	var _ rpcsub.Handler = h
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
