package interfaces

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// a minimal XML-RPC interface: listDevices with one device and one channel, refresh returns true
func fakeInterface(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		w.Header().Set("Content-Type", "text/xml")
		switch {
		case strings.Contains(body, "<methodName>listDevices</methodName>"):
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>
<value><struct>
<member><name>ADDRESS</name><value><string>000A1B2C3D4E5F</string></value></member>
<member><name>TYPE</name><value><string>HmIP-PDT</string></value></member>
<member><name>FIRMWARE</name><value><string>2.2.4</string></value></member>
<member><name>AVAILABLE_FIRMWARE</name><value><string>2.2.6</string></value></member>
<member><name>FIRMWARE_UPDATE_STATE</name><value><string>DELIVER_FIRMWARE_IMAGE</string></value></member>
<member><name>PARENT</name><value><string></string></value></member>
<member><name>CHILDREN</name><value><array><data><value><string>000A1B2C3D4E5F:0</string></value></data></array></value></member>
</struct></value>
<value><struct>
<member><name>ADDRESS</name><value><string>000A1B2C3D4E5F:0</string></value></member>
<member><name>TYPE</name><value><string>MAINTENANCE</string></value></member>
<member><name>PARENT</name><value><string>000A1B2C3D4E5F</string></value></member>
</struct></value>
</data></array></value></param></params></methodResponse>`))
		case strings.Contains(body, "<methodName>getDeviceDescription</methodName>") && strings.Contains(body, "INT0000001"):
			// task 42: a heating group device on VirtualDevices
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><struct>
<member><name>ADDRESS</name><value><string>INT0000001</string></value></member>
<member><name>TYPE</name><value><string>HmIP-HEATING</string></value></member>
<member><name>CHILDREN</name><value><array><data><value><string>INT0000001:0</string></value><value><string>INT0000001:1</string></value></data></array></value></member>
</struct></value></param></params></methodResponse>`))
		case strings.Contains(body, "<methodName>getDeviceDescription</methodName>") && strings.Contains(body, "INT0000002"):
			// VirtualDevices' empty value for a device without channels
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><struct>
<member><name>ADDRESS</name><value><string>INT0000002</string></value></member>
<member><name>CHILDREN</name><value></value></member>
</struct></value></param></params></methodResponse>`))
		case strings.Contains(body, "<methodName>listBidcosInterfaces</methodName>"):
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data><value><struct>
<member><name>ADDRESS</name><value>3014F711A000040000000A02</value></member>
<member><name>DESCRIPTION</name><value>HMIP_CCU2 3014F711A000040000000A02</value></member>
<member><name>CONNECTED</name><value><boolean>1</boolean></value></member>
<member><name>DEFAULT</name><value><boolean>1</boolean></value></member>
<member><name>TYPE</name><value>HMIP_CCU2</value></member>
<member><name>FIRMWARE_VERSION</name><value>4.4.18</value></member>
<member><name>DUTY_CYCLE</name><value><i4>3</i4></value></member>
</struct></value></data></array></value></param></params></methodResponse>`))
		case strings.Contains(body, "<methodName>logLevel</methodName>"):
			// rfd's own method (task 27.8): answers the level in force; refuses anything but 2 here
			if !strings.Contains(body, "<i4>2</i4>") && !strings.Contains(body, "<int>2</int>") {
				_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><int>-2</int></value></member><member><name>faultString</name><value><string>Invalid level</string></value></member></struct></value></fault></methodResponse>`))
				return
			}
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><i4>2</i4></value></param></params></methodResponse>`))
		case strings.Contains(body, "<methodName>getValue</methodName>") && strings.Contains(body, "UNREACH"):
			// task 149: the one device of this interface went silent
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><boolean>1</boolean></value></param></params></methodResponse>`))
		case strings.Contains(body, "refreshDeployedDeviceFirmwareList"):
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><boolean>1</boolean></value></param></params></methodResponse>`))
		default:
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><int>-1</int></value></member><member><name>faultString</name><value><string>unknown</string></value></member></struct></value></fault></methodResponse>`))
		}
	}))
}

func TestDevicesAndRefresh(t *testing.T) {
	srv := fakeInterface(t)
	defer srv.Close()
	ifs := FromList([]struct{ Name, URL string }{
		{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")},
		{"BidCos-RF", "xmlrpc_bin://127.0.0.1:1"}, // nothing listens: must be reported, not fatal
		{"Weird", "gopher://x"},                   // skipped
	})
	if len(ifs) != 2 {
		t.Fatalf("clients: %d", len(ifs))
	}
	devs, errs := Devices(t.Context(), ifs, 3*time.Second)
	if len(devs) != 1 || devs[0].Type != "HmIP-PDT" || devs[0].Firmware != "2.2.4" || devs[0].AvailableFirmware != "2.2.6" || devs[0].Interface != "HmIP-RF" || devs[0].UpdateState != "DELIVER_FIRMWARE_IMAGE" {
		t.Fatalf("devices: %+v errs: %v", devs, errs)
	}
	if errs["BidCos-RF"] == nil {
		t.Fatal("the dead interface must be reported")
	}
	rerrs := RefreshFirmware(ifs[:1])
	if len(rerrs) != 0 {
		t.Fatalf("refresh: %v", rerrs)
	}
}

func TestListInterfaces(t *testing.T) {
	srv := fakeInterface(t)
	defer srv.Close()
	ifs := FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")}, {"VirtualDevices", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://") + "/groups"}})
	list, errs := ListInterfaces(context.Background(), ifs, 5*time.Second)
	if len(errs) != 0 || len(list) != 1 {
		t.Fatalf("%v %+v", errs, list)
	}
	ri := list[0]
	if ri.Interface != "HmIP-RF" || ri.Address != "3014F711A000040000000A02" || !ri.Connected || !ri.Default || ri.DutyCycle != 3 || ri.Firmware != "4.4.18" || ri.Type != "HMIP_CCU2" {
		t.Errorf("%+v", ri)
	}
}

// hs485d (BidCos-Wired) has no listBidcosInterfaces and answers it with a fault: it is up, and
// must be neither an error ("not answering") nor a radio entry; a process that does not answer
// at all still is an error.
func TestSampleInterfacesFaultIsAnAnswer(t *testing.T) {
	wired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><i4>-1</i4></value></member><member><name>faultString</name><value>listBidcosInterfaces: unknown method name</value></member></struct></value></fault></methodResponse>`))
	}))
	defer wired.Close()
	rf := fakeInterface(t)
	defer rf.Close()
	ifs := FromList([]struct{ Name, URL string }{
		{"BidCos-Wired", "xmlrpc://" + strings.TrimPrefix(wired.URL, "http://")},
		{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(rf.URL, "http://")},
		{"BidCos-RF", "xmlrpc://127.0.0.1:1"}, // nothing listens
	})
	list, answered, errs := SampleInterfaces(t.Context(), ifs, 3*time.Second)
	if len(list) != 1 || list[0].Interface != "HmIP-RF" {
		t.Errorf("list: %+v", list)
	}
	if len(answered) != 1 || answered[0] != "BidCos-Wired" {
		t.Errorf("answered: %v", answered)
	}
	if len(errs) != 1 || errs["BidCos-RF"] == nil {
		t.Errorf("errs: %v", errs)
	}
	if _, errs := ListInterfaces(t.Context(), ifs, 3*time.Second); errs["BidCos-Wired"] != nil {
		t.Errorf("ListInterfaces reports the answering process as an error: %v", errs)
	}
}

// levelServer is a fake hmipserver for the carrier sense (task 68): its listBidcosInterfaces entry
// may carry CARRIER_SENSE_LEVEL itself, and channel 0 of the module's device answers the paramset
// description and getValue the way it is told to. It counts the calls by method.
type levelServer struct {
	listLevel string // the CARRIER_SENSE_LEVEL member's value in the list entry, "" for none
	desc      string // "has", "none" (a description without the datapoint), "fault", "down" (HTTP 500)
	value     string // the getValue answer: a <value> body, or "fault"
	mu        sync.Mutex
	calls     map[string]int
	addresses []string
}

func (l *levelServer) count(method string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[method]
}

func (l *levelServer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	const fault = `<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><i4>%s</i4></value></member><member><name>faultString</name><value>%s</value></member></struct></value></fault></methodResponse>`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		method := body[strings.Index(body, "<methodName>")+len("<methodName>") : strings.Index(body, "</methodName>")]
		l.mu.Lock()
		if l.calls == nil {
			l.calls = map[string]int{}
		}
		l.calls[method]++
		if method != "listBidcosInterfaces" {
			l.addresses = append(l.addresses, body)
		}
		l.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		switch method {
		case "listBidcosInterfaces":
			member := ""
			if l.listLevel != "" {
				member = `<member><name>CARRIER_SENSE_LEVEL</name><value>` + l.listLevel + `</value></member>`
			}
			fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><array><data><value><struct>
<member><name>ADDRESS</name><value>3014F711A000040000000A02</value></member>
<member><name>CONNECTED</name><value><boolean>1</boolean></value></member>
<member><name>TYPE</name><value>HMIP_CCU2</value></member>
<member><name>DUTY_CYCLE</name><value><i4>1</i4></value></member>%s
</struct></value></data></array></value></param></params></methodResponse>`, member)
		case "getParamsetDescription":
			switch l.desc {
			case "has":
				_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><struct>
<member><name>CARRIER_SENSE_LEVEL</name><value><struct><member><name>TYPE</name><value>FLOAT</value></member><member><name>UNIT</name><value>%</value></member></struct></value></member>
<member><name>DUTY_CYCLE_LEVEL</name><value><struct><member><name>TYPE</name><value>FLOAT</value></member></struct></value></member>
</struct></value></param></params></methodResponse>`))
			case "none":
				_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><struct>
<member><name>UNREACH</name><value><struct><member><name>TYPE</name><value>BOOL</value></member></struct></value></member>
</struct></value></param></params></methodResponse>`))
			case "fault":
				fmt.Fprintf(w, fault, "-2", "Invalid device")
			default:
				http.Error(w, "down", http.StatusInternalServerError)
			}
		case "getValue":
			if l.value == "fault" {
				fmt.Fprintf(w, fault, "-5", "Unknown Parameter value for value key")
				return
			}
			fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value>%s</value></param></params></methodResponse>`, l.value)
		default:
			fmt.Fprintf(w, fault, "-1", "unknown")
		}
	}))
}

// Task 68: the carrier sense of an HmIP radio whose list entry has none comes from channel 0 of
// the module's device - the SGTIN without its prefix - and a value in the list always wins.
func TestCarrierSenseFromDevice(t *testing.T) {
	cases := []struct {
		name                  string
		srv                   *levelServer
		want                  *int // after the polls
		source                string
		descCalls, valueCalls int // over three polls
	}{
		{"the device reports it", &levelServer{desc: "has", value: "<double>2.5</double>"}, ptr(3), "device", 1, 3},
		{"0 % is a value", &levelServer{desc: "has", value: "<double>0.0</double>"}, ptr(0), "device", 1, 3},
		{"the list reports it: no device call", &levelServer{listLevel: "<i4>7</i4>", desc: "has", value: "<double>2.5</double>"}, ptr(7), "interface", 0, 0},
		{"the list reports it as a double", &levelServer{listLevel: "<double>4.4</double>"}, ptr(4), "interface", 0, 0},
		{"not reported yet: a fault from getValue", &levelServer{desc: "has", value: "fault"}, nil, "device", 1, 3},
		{"a LAN gateway: asked once, then remembered", &levelServer{desc: "fault"}, nil, "", 1, 0},
		{"a device without the datapoint: asked once", &levelServer{desc: "none"}, nil, "", 1, 0},
		{"no answer: asked again next time", &levelServer{desc: "down"}, nil, "", 3, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := c.srv.serve(t)
			defer srv.Close()
			ifs := FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")}})
			var levels DeviceLevels
			var list []RadioInterface
			for range 3 {
				var errs map[string]error
				list, errs = ListInterfaces(t.Context(), ifs, 5*time.Second)
				if len(errs) != 0 || len(list) != 1 {
					t.Fatalf("%v %+v", errs, list)
				}
				levels.Fill(t.Context(), ifs, list, 5*time.Second)
			}
			ri := list[0]
			if (ri.CarrierSense == nil) != (c.want == nil) || (c.want != nil && *ri.CarrierSense != *c.want) || ri.CarrierSenseSource != c.source {
				t.Errorf("carrier sense %v source %q, want %v %q", deref(ri.CarrierSense), ri.CarrierSenseSource, deref(c.want), c.source)
			}
			if got := c.srv.count("getParamsetDescription"); got != c.descCalls {
				t.Errorf("getParamsetDescription calls: %d, want %d", got, c.descCalls)
			}
			if got := c.srv.count("getValue"); got != c.valueCalls {
				t.Errorf("getValue calls: %d, want %d", got, c.valueCalls)
			}
			// the device, not the SGTIN of the list entry
			for _, body := range c.srv.addresses {
				if !strings.Contains(body, ">00040000000A02:0<") {
					t.Errorf("asked for another address: %s", body)
				}
			}
			// the JSON keeps a 0 % and says nothing where nothing is known
			raw, _ := json.Marshal(ri)
			if strings.Contains(string(raw), `"carrier_sense":`) != (c.want != nil) || strings.Contains(string(raw), `"carrier_sense_source"`) != (c.source != "") {
				t.Errorf("json: %s", raw)
			}
		})
	}
}

// A device that had no datapoint is asked again once RetryAfter has passed: an hmipserver that
// answered "Invalid device" while it was still starting does not keep the figure away for good.
func TestCarrierSenseRetry(t *testing.T) {
	srv := (&levelServer{desc: "fault"})
	s := srv.serve(t)
	defer s.Close()
	ifs := FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(s.URL, "http://")}})
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	levels := DeviceLevels{RetryAfter: 10 * time.Minute, Now: func() time.Time { return now }}
	poll := func() {
		list, _ := ListInterfaces(t.Context(), ifs, 5*time.Second)
		levels.Fill(t.Context(), ifs, list, 5*time.Second)
	}
	poll()
	now = now.Add(9 * time.Minute)
	poll()
	if n := srv.count("getParamsetDescription"); n != 1 {
		t.Fatalf("within the retry time: %d calls", n)
	}
	now = now.Add(time.Minute)
	poll()
	if n := srv.count("getParamsetDescription"); n != 2 {
		t.Errorf("after the retry time: %d calls", n)
	}
}

// only HmIP entries are asked: rfd's BidCos-RF has no such datapoint
func TestCarrierSenseOnlyHmIP(t *testing.T) {
	srv := &levelServer{desc: "has", value: "<double>2.0</double>"}
	s := srv.serve(t)
	defer s.Close()
	ifs := FromList([]struct{ Name, URL string }{{"BidCos-RF", "xmlrpc://" + strings.TrimPrefix(s.URL, "http://")}})
	list, _ := ListInterfaces(t.Context(), ifs, 5*time.Second)
	var levels DeviceLevels
	levels.Fill(t.Context(), ifs, list, 5*time.Second)
	if srv.count("getParamsetDescription")+srv.count("getValue") != 0 || list[0].CarrierSense != nil {
		t.Errorf("BidCos-RF was asked: %+v", list[0])
	}
}

func TestDeviceAddress(t *testing.T) {
	for in, want := range map[string]string{
		"3014F711A000040000000A01": "00040000000A01",
		"3014f711a0001F0000000A03": "001F0000000A03",
		"NEQ1234567":               "NEQ1234567",
		"3014F711A0":               "3014F711A0",
		// task 217: another company prefix (the SilverCrest HmIP-HAP-B1, TARGA's)
		"30150377DC00030000000A13": "00030000000A13",
		"30150377dc00030000000a13": "00030000000A13",
		"NEQ12345670123456789ABCD": "NEQ12345670123456789ABCD",
		"3015XXXXXXXXXXXXXXXXXXXX": "3015XXXXXXXXXXXXXXXXXXXX",
	} {
		if got := deviceAddress(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func ptr(n int) *int { return &n }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// The level goes to the one interface named, as an int, and the answer decides: a fault is an
// error, a name that is not in the list is an error, the right call is not.
func TestSetLogLevel(t *testing.T) {
	srv := fakeInterface(t)
	defer srv.Close()
	ifs := FromList([]struct{ Name, URL string }{
		{"BidCos-RF", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")},
	})
	if err := SetLogLevel(t.Context(), ifs, "BidCos-RF", 2); err != nil {
		t.Fatalf("level 2: %v", err)
	}
	if err := SetLogLevel(t.Context(), ifs, "BidCos-RF", 5); err == nil {
		t.Error("the interface's fault must come back as an error")
	}
	if err := SetLogLevel(t.Context(), ifs, "BidCos-Wired", 2); err == nil {
		t.Error("an interface that is not in the list must be an error")
	}
}

// task 76: init goes to the one interface named with the url and the id as two strings - an empty
// id is the deregistration - and a fault or a name that is not in the list is an error.
func TestInit(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(raw), "fault.example") {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><int>-1</int></value></member><member><name>faultString</name><value><string>refused</string></value></member></struct></value></fault></methodResponse>`))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value></value></param></params></methodResponse>`))
	}))
	defer srv.Close()
	ifs := FromList([]struct{ Name, URL string }{
		{"VirtualDevices", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://") + "/groups"},
	})
	tests := []struct {
		name, iface, url string
		wantErr          bool
		wantBody         []string
	}{
		{"deregistration", "VirtualDevices", "http://127.0.0.1:59999", false, []string{"<methodName>init</methodName>", "<param><value>http://127.0.0.1:59999</value></param><param><value></value></param>"}},
		{"a fault", "VirtualDevices", "http://fault.example:1", true, nil},
		{"not in the list", "HmIP-RF", "http://127.0.0.1:59999", true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			bodies = nil
			mu.Unlock()
			err := Init(t.Context(), ifs, tc.iface, tc.url, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, want := range tc.wantBody {
				if len(bodies) != 1 || !strings.Contains(bodies[0], want) {
					t.Errorf("request %q does not carry %q", bodies, want)
				}
			}
		})
	}
}

// task 149: the check after a key change counts the devices and names the unreachable ones
func TestReachability(t *testing.T) {
	srv := fakeInterface(t)
	defer srv.Close()
	ifs := FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")}})
	states, err := Reachability(t.Context(), ifs[0], 3*time.Second)
	if err != nil || len(states) != 1 || states["000A1B2C3D4E5F"] != (DeviceState{State: "unreach"}) {
		t.Fatalf("%v %v", states, err)
	}
	dead := FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://127.0.0.1:1"}})
	if _, err := Reachability(t.Context(), dead[0], time.Second); err == nil {
		t.Fatal("a silent interface is an error")
	}
}

// task 149: the module, HAPs and DRAPs are access points - a key check does not watch them
func TestAccessPoint(t *testing.T) {
	for typ, want := range map[string]bool{"HmIPW-DRAP": true, "HmIP-HAP": true, "RPI-RF-MOD": true, "HmIP-RFUSB": true, "HmIPW-DRS8": false, "HMIP-WRC2": false, "HmIP-PDT": false, "HmIP-eTRV-2": false} {
		if accessPoint(typ) != want {
			t.Errorf("%s: %v", typ, !want)
		}
	}
}

// task 42: a device's channels from its getDeviceDescription
func TestChildren(t *testing.T) {
	srv := fakeInterface(t)
	defer srv.Close()
	ifs := FromList([]struct{ Name, URL string }{{"VirtualDevices", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")}})
	got, err := Children(t.Context(), ifs[0], "INT0000001", 3*time.Second)
	if err != nil || strings.Join(got, ",") != "INT0000001:0,INT0000001:1" {
		t.Fatalf("children: %v %v", got, err)
	}
	if got, err := Children(t.Context(), ifs[0], "INT0000002", 3*time.Second); err != nil || len(got) != 0 {
		t.Fatalf("no children: %v %v", got, err)
	}
	if _, err := Children(t.Context(), ifs[0], "INT0000009", 3*time.Second); err == nil {
		t.Fatal("an unknown device (the interface's fault) is an error")
	}
	dead := FromList([]struct{ Name, URL string }{{"VirtualDevices", "xmlrpc://127.0.0.1:1"}})
	if _, err := Children(t.Context(), dead[0], "INT0000001", time.Second); err == nil {
		t.Fatal("a silent interface is an error")
	}
}

// occulited task 38: the sweep's channels - HmIP radio devices' channels with VALUES; the CCU's
// receiver and the wired HmIP devices (not measured free on the bus) are left out
func TestChannelsLeaveWiredOut(t *testing.T) {
	ch := func(addr, parentType string, paramsets ...string) string {
		ps := ""
		for _, p := range paramsets {
			ps += "<value><string>" + p + "</string></value>"
		}
		return `<value><struct><member><name>ADDRESS</name><value><string>` + addr + `</string></value></member>
<member><name>PARENT</name><value><string>` + strings.Split(addr, ":")[0] + `</string></value></member>
<member><name>PARENT_TYPE</name><value><string>` + parentType + `</string></value></member>
<member><name>PARAMSETS</name><value><array><data>` + ps + `</data></array></value></member></struct></value>`
	}
	dev := func(addr, typ string) string {
		return `<value><struct><member><name>ADDRESS</name><value><string>` + addr + `</string></value></member>
<member><name>TYPE</name><value><string>` + typ + `</string></value></member></struct></value>`
	}
	body := `<?xml version="1.0"?><methodResponse><params><param><value><array><data>` +
		dev("0001", "HmIP-PDT") + ch("0001:0", "HmIP-PDT", "MASTER", "VALUES") + ch("0001:3", "HmIP-PDT", "MASTER", "VALUES") + ch("0001:9", "HmIP-PDT", "MASTER") +
		dev("0002", "HmIPW-DRAP") + ch("0002:0", "HmIPW-DRAP", "MASTER", "VALUES") +
		dev("0003", "HmIPW-DRS8") + ch("0003:1", "HmIPW-DRS8", "MASTER", "VALUES") +
		dev("0004", "HmIP-RCV-50") + ch("0004:1", "HmIP-RCV-50", "MASTER", "VALUES") +
		`</data></array></value></param></params></methodResponse>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	ifs := FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")}})
	got, err := Channels(t.Context(), ifs[0], 3*time.Second)
	if err != nil || strings.Join(got, ",") != "0001:0,0001:3" {
		t.Fatalf("channels: %v %v", got, err)
	}
}
