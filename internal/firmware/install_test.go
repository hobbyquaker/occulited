package firmware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

// a fake HmIP-RF that answers installFirmware with the given body and records the calls
type installRPC struct {
	mu    sync.Mutex
	calls []string
	srv   *httptest.Server
}

func newInstallRPC(t *testing.T, answer string) *installRPC {
	t.Helper()
	f := &installRPC{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(raw), "installFirmware") {
			f.mu.Lock()
			f.calls = append(f.calls, string(raw))
			f.mu.Unlock()
			_, _ = w.Write([]byte(answer))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><array><data></data></array></value></param></params></methodResponse>`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *installRPC) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

const (
	answerTrue    = `<?xml version="1.0"?><methodResponse><params><param><value><boolean>1</boolean></value></param></params></methodResponse>`
	answerFalse   = `<?xml version="1.0"?><methodResponse><params><param><value><boolean>0</boolean></value></param></params></methodResponse>`
	answerPending = `<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><i4>-10</i4></value></member><member><name>faultString</name><value><string>Transmission Pending</string></value></member></struct></value></fault></methodResponse>`
	answerFault   = `<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><i4>-2</i4></value></member><member><name>faultString</name><value><string>Unknown instance</string></value></member></struct></value></fault></methodResponse>`
)

func installService(t *testing.T, url, state string) *Service {
	t.Helper()
	s := NewService(New("http://127.0.0.1:1"), t.TempDir(), func() []interfaces.Interface { return ifs(url) }, nil)
	s.state.Devices = []DeviceStatus{
		{Device: interfaces.Device{Interface: "HmIP-RF", Address: "0001D0000000B1", Type: "HmIP-PDT", Firmware: "2.2.4", AvailableFirmware: "2.2.6", UpdateState: state}},
		{Device: interfaces.Device{Interface: "BidCos-RF", Address: "JEQ9000001", Type: "HM-CC-TC", Firmware: "2.1", UpdateState: "READY_FOR_UPDATE"}},
	}
	return s
}

func TestInstall(t *testing.T) {
	f := newInstallRPC(t, answerTrue)
	s := installService(t, f.srv.URL, "READY_FOR_UPDATE")
	res, err := s.Install(context.Background(), "0001D0000000B1")
	if err != nil || res.Pending || res.Type != "HmIP-PDT" || res.From != "2.2.4" || res.To != "2.2.6" {
		t.Fatalf("install: %+v %v", res, err)
	}
	if f.count() != 1 || !strings.Contains(f.calls[0], "<value>0001D0000000B1</value>") {
		t.Errorf("calls: %v", f.calls)
	}
	// the live reads go to the fast rate
	s.mu.Lock()
	fast := s.fastUntil.After(s.now())
	s.mu.Unlock()
	if !fast {
		t.Error("no fast rate after the install")
	}
}

func TestInstallRefusals(t *testing.T) {
	for _, c := range []struct {
		name, answer, state, address string
		want                         error
		calls                        int
	}{
		{"not ready: nothing is called", answerTrue, "DELIVER_FIRMWARE_IMAGE", "0001D0000000B1", ErrNotReady, 0},
		{"up to date", answerTrue, "UP_TO_DATE", "0001D0000000B1", ErrNotReady, 0},
		{"unknown device", answerTrue, "READY_FOR_UPDATE", "0001D0000000B9", ErrNoDevice, 0},
		{"a BidCos device is not HmIP-RF", answerTrue, "READY_FOR_UPDATE", "JEQ9000001", ErrNoDevice, 0},
		{"hmipserver answers false", answerFalse, "READY_FOR_UPDATE", "0001D0000000B1", ErrInstallRefused, 1},
	} {
		f := newInstallRPC(t, c.answer)
		s := installService(t, f.srv.URL, c.state)
		if _, err := s.Install(context.Background(), c.address); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
		if f.count() != c.calls {
			t.Errorf("%s: %d calls", c.name, f.count())
		}
	}
	// a fault other than "transmission pending" is the interface's error
	f := newInstallRPC(t, answerFault)
	if _, err := installService(t, f.srv.URL, "READY_FOR_UPDATE").Install(context.Background(), "0001D0000000B1"); err == nil || errors.Is(err, ErrInstallRefused) {
		t.Errorf("fault: %v", err)
	}
	// no HmIP-RF interface
	s := installService(t, f.srv.URL, "READY_FOR_UPDATE")
	s.Interfaces = func() []interfaces.Interface { return nil }
	if _, err := s.Install(context.Background(), "0001D0000000B1"); err == nil {
		t.Error("no interface: no error")
	}
}

// a sleeping device: hmipserver's "transmission pending" is an install that waits for the device
func TestInstallPending(t *testing.T) {
	f := newInstallRPC(t, answerPending)
	res, err := installService(t, f.srv.URL, "READY_FOR_UPDATE").Install(context.Background(), "0001D0000000B1")
	if err != nil || !res.Pending {
		t.Fatalf("pending: %+v %v", res, err)
	}
}
