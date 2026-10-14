package firmware

import (
	"context"
	"encoding/json"
	"fmt"
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
)

func dev(iface, addr, state string) interfaces.Device {
	return interfaces.Device{Interface: iface, Address: addr, Type: "HmIP-BBL", Firmware: "1.0.2", AvailableFirmware: "1.10.16", UpdateState: state}
}

// liveInterface is a fake HmIP-RF whose listDevices answer the test sets: a device per entry,
// with FIRMWARE, AVAILABLE_FIRMWARE and FIRMWARE_UPDATE_STATE.
type liveInterface struct {
	mu      sync.Mutex
	devices []interfaces.Device
	calls   int
	srv     *httptest.Server
}

func newLiveInterface(t *testing.T, devs ...interfaces.Device) *liveInterface {
	t.Helper()
	li := &liveInterface{devices: devs}
	li.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if !strings.Contains(string(raw), "listDevices") {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><string></string></value></param></params></methodResponse>`))
			return
		}
		li.mu.Lock()
		li.calls++
		devs := append([]interfaces.Device(nil), li.devices...)
		li.mu.Unlock()
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>`)
		for _, d := range devs {
			fmt.Fprintf(&b, `<value><struct><member><name>ADDRESS</name><value><string>%s</string></value></member><member><name>TYPE</name><value><string>%s</string></value></member><member><name>FIRMWARE</name><value><string>%s</string></value></member><member><name>AVAILABLE_FIRMWARE</name><value><string>%s</string></value></member><member><name>FIRMWARE_UPDATE_STATE</name><value><string>%s</string></value></member><member><name>UPDATABLE</name><value><boolean>1</boolean></value></member><member><name>PARENT</name><value><string></string></value></member></struct></value>`, d.Address, d.Type, d.Firmware, d.AvailableFirmware, d.UpdateState)
		}
		b.WriteString(`</data></array></value></param></params></methodResponse>`)
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(li.srv.Close)
	return li
}

func (li *liveInterface) set(devs ...interfaces.Device) {
	li.mu.Lock()
	li.devices = devs
	li.mu.Unlock()
}

func (li *liveInterface) count() int {
	li.mu.Lock()
	defer li.mu.Unlock()
	return li.calls
}

func (li *liveInterface) list() []interfaces.Interface {
	return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(li.srv.URL, "http://")}})
}

func liveService(t *testing.T, li *liveInterface) *Service {
	t.Helper()
	s := NewService(New("http://127.0.0.1:1"), t.TempDir(), li.list, nil)
	s.Open(filepath.Join(t.TempDir(), "state.json"))
	return s
}

// The live read: the interface's states and firmware replace the check's snapshot for the
// devices it lists, a device paired since takes eQ-3's version from its type, a deleted one goes.
func TestReadLiveFoldsTheInterfaceIntoTheState(t *testing.T) {
	li := newLiveInterface(t, dev("HmIP-RF", "00010000000A10", "DELIVER_FIRMWARE_IMAGE"), dev("HmIP-RF", "00010000000A11", "NEW_FIRMWARE_AVAILABLE"))
	s := liveService(t, li)
	// the check's snapshot: A10 queued, with eQ-3's version; A12 since deleted on the interface
	s.mu.Lock()
	s.state.Devices = []DeviceStatus{
		{Device: dev("HmIP-RF", "00010000000A10", "NEW_FIRMWARE_AVAILABLE"), Latest: "1.10.16", UpdateAvailable: true},
		{Device: dev("HmIP-RF", "0001D0000000B2", "UP_TO_DATE"), Latest: "1.10.16"},
		{Device: interfaces.Device{Interface: "BidCos-RF", Address: "JEQ9000001", Type: "HM-CC-TC", Firmware: "2.1"}, NotListed: true},
	}
	s.mu.Unlock()
	var got []Change
	var mu sync.Mutex
	s.Changed = func(c Change) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	}
	s.ReadLive(context.Background(), []string{"HmIP-RF"})
	st := s.Status()
	if len(st.Devices) != 3 {
		t.Fatalf("devices after the read: %+v", st.Devices)
	}
	byAddr := map[string]DeviceStatus{}
	for _, d := range st.Devices {
		byAddr[d.Address] = d
	}
	if d := byAddr["00010000000A10"]; d.UpdateState != "DELIVER_FIRMWARE_IMAGE" || d.Latest != "1.10.16" || !d.UpdateAvailable {
		t.Errorf("A10: %+v", d)
	}
	if d := byAddr["00010000000A11"]; d.UpdateState != "NEW_FIRMWARE_AVAILABLE" || d.Latest != "1.10.16" || !d.UpdateAvailable {
		t.Errorf("A11 (new, eQ-3's version from its type): %+v", d)
	}
	if _, ok := byAddr["0001D0000000B2"]; ok {
		t.Error("the deleted device is still listed")
	}
	if d, ok := byAddr["JEQ9000001"]; !ok || !d.NotListed {
		t.Errorf("the other interface's device was touched: %+v", d)
	}
	if st.LiveAt == nil {
		t.Error("live_at not set")
	}
	if st.Transfers != (TransferCounts{Running: 1, Queued: 1}) {
		t.Errorf("counts: %+v", st.Transfers)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Address != "00010000000A10" || got[0].UpdateState != "DELIVER_FIRMWARE_IMAGE" || got[1].Address != "00010000000A11" {
		t.Errorf("changes: %+v", got)
	}
	// the same answer again changes nothing and tells nobody
	s.ReadLive(context.Background(), nil)
	if len(got) != 2 {
		t.Errorf("a read without change notified: %+v", got)
	}
	// the install done: the firmware is the new one, the update no longer available
	li.set(interfaces.Device{Interface: "HmIP-RF", Address: "00010000000A10", Type: "HmIP-BBL", Firmware: "1.10.16", AvailableFirmware: "1.10.16", UpdateState: "UP_TO_DATE"}, dev("HmIP-RF", "00010000000A11", "NEW_FIRMWARE_AVAILABLE"))
	s.ReadLive(context.Background(), nil)
	st = s.Status()
	for _, d := range st.Devices {
		if d.Address == "00010000000A10" && (d.Firmware != "1.10.16" || d.UpdateAvailable || d.UpdateState != "UP_TO_DATE") {
			t.Errorf("after the install: %+v", d)
		}
	}
	// the state survives a restart
	s2 := NewService(New("http://127.0.0.1:1"), s.Dir, li.list, nil)
	s2.Open(s.statePath)
	if got := s2.Status(); len(got.Devices) != 3 || got.Devices[1].UpdateState != "UP_TO_DATE" && got.Devices[2].UpdateState != "UP_TO_DATE" {
		t.Errorf("reopened: %+v", got.Devices)
	}
}

// The journal's transfer rides on the device while the interface says it is transferring, is
// dropped once it says READY_FOR_UPDATE, and comes back from the state file after a restart.
func TestStatusAttachesTheJournalTransfer(t *testing.T) {
	li := newLiveInterface(t, dev("HmIP-RF", "00010000000A10", "DELIVER_FIRMWARE_IMAGE"))
	s := liveService(t, li)
	now := time.Date(2026, 10, 14, 13, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	s.ReadLive(context.Background(), nil)
	s.NotifyEvery = 100 * time.Millisecond
	var mu sync.Mutex
	var changes []Change
	s.Changed = func(c Change) {
		mu.Lock()
		changes = append(changes, c)
		mu.Unlock()
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(changes)
	}
	s.Line(now, lineContinues)
	s.Line(now.Add(time.Minute), lineInit)
	s.Line(now.Add(2*time.Minute), lineFrames)
	st := s.Status()
	if tr := st.Devices[0].Transfer; tr == nil || tr.State != TransferRunning || tr.Percent != 0 || tr.Bytes != 30 || tr.Size != 99540 {
		t.Fatalf("transfer: %+v", st.Devices[0].Transfer)
	}
	if st.Transfers.Running != 1 {
		t.Errorf("counts: %+v", st.Transfers)
	}
	// the notifier: the first line sent at once, the two inside the window held - and the held
	// one that goes out when the window ends is the last
	if count() != 1 {
		t.Errorf("changes right after three lines: %+v", changes)
	}
	deadline := time.Now().Add(3 * time.Second)
	for count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if len(changes) != 2 || changes[0].Transfer == nil || changes[0].Transfer.State != TransferRunning || changes[1].Transfer == nil || changes[1].Transfer.Bytes != 30 {
		t.Errorf("changes after the window: %+v", changes)
	}
	mu.Unlock()
	now = now.Add(time.Hour)
	s.Line(now, lineContinue)
	mu.Lock()
	if len(changes) != 3 || changes[2].Transfer.Bytes != 420 {
		t.Errorf("a line an hour later is sent at once: %+v", changes)
	}
	mu.Unlock()
	// a restart keeps it
	s2 := NewService(New("http://127.0.0.1:1"), s.Dir, li.list, nil)
	s2.Now = s.Now
	s2.Open(s.statePath)
	if tr := s2.Status().Devices[0].Transfer; tr == nil || tr.Bytes != 420 {
		t.Errorf("after a restart: %+v", tr)
	}
	// the follower starts after the newest entry it knows
	if since := s2.journalSince(); !since.Equal(now) {
		t.Errorf("journal since %v, want %v", since, now)
	}
	// transferred: the interface says READY_FOR_UPDATE, the journal's entry is dropped
	li.set(dev("HmIP-RF", "00010000000A10", "READY_FOR_UPDATE"))
	s.ReadLive(context.Background(), nil)
	st = s.Status()
	if st.Devices[0].Transfer != nil || st.Transfers.Ready != 1 {
		t.Errorf("ready: %+v %+v", st.Devices[0].Transfer, st.Transfers)
	}
	s.mu.Lock()
	_, kept := s.transfers["00010000000A10"]
	s.mu.Unlock()
	if kept {
		t.Error("the journal entry was kept past READY_FOR_UPDATE")
	}
	// an entry nobody confirms for two days goes too
	s.Line(now, lineMissed)
	now = now.Add(transferKeep + time.Hour)
	if tr := s.Status(); len(tr.Devices) != 1 {
		t.Errorf("devices: %+v", tr.Devices)
	}
	s.mu.Lock()
	s.pruneTransfers(now)
	_, kept = s.transfers["00010000000A11"]
	s.mu.Unlock()
	if kept {
		t.Error("a stale journal entry was kept")
	}
}

// The poll rule: nothing to poll for READY_FOR_UPDATE or UP_TO_DATE alone, the slow rate for a
// transfer, the fast one for an install and for a while after an UPDATE_PENDING event.
func TestLiveWait(t *testing.T) {
	s := NewService(New("http://127.0.0.1:1"), t.TempDir(), func() []interfaces.Interface { return nil }, nil)
	now := time.Now()
	s.Now = func() time.Time { return now }
	set := func(states ...string) {
		s.mu.Lock()
		s.state.Devices = nil
		for i, st := range states {
			s.state.Devices = append(s.state.Devices, DeviceStatus{Device: dev("HmIP-RF", fmt.Sprintf("00010000000A1%d", i), st)})
		}
		s.mu.Unlock()
	}
	for _, c := range []struct {
		states []string
		want   time.Duration
	}{
		{nil, 0},
		{[]string{"UP_TO_DATE", ""}, 0},
		{[]string{"READY_FOR_UPDATE"}, 0},
		{[]string{"READY_FOR_UPDATE", "NEW_FIRMWARE_AVAILABLE"}, LiveSlow},
		{[]string{"DELIVER_FIRMWARE_IMAGE"}, LiveSlow},
		{[]string{"DELIVER_FIRMWARE_IMAGE", "DO_UPDATE_PENDING"}, LiveFast},
		{[]string{"PERFORMING_UPDATE"}, LiveFast},
		{[]string{"LIVE_DELIVER_FIRMWARE_IMAGE"}, LiveFast},
	} {
		set(c.states...)
		s.mu.Lock()
		got := s.liveWait()
		s.mu.Unlock()
		if got != c.want {
			t.Errorf("%v: %v, want %v", c.states, got, c.want)
		}
	}
	set("READY_FOR_UPDATE")
	s.InstallSeen("HmIP-RF")
	s.mu.Lock()
	got := s.liveWait()
	s.mu.Unlock()
	if got != LiveFast {
		t.Errorf("after an install event: %v, want the fast rate", got)
	}
	now = now.Add(liveFastFor + time.Second)
	s.mu.Lock()
	got = s.liveWait()
	s.mu.Unlock()
	if got != 0 {
		t.Errorf("three minutes after the install event: %v, want none", got)
	}
}

// RunLive reads on the bus's asks, debounced, and on the timer while a transfer runs.
func TestRunLiveReadsOnAsksAndOnTheTimer(t *testing.T) {
	li := newLiveInterface(t, dev("HmIP-RF", "00010000000A10", "DELIVER_FIRMWARE_IMAGE"))
	s := liveService(t, li)
	s.LiveSlow = 150 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RunLive(ctx)
	// nothing known yet: no timer; three asks within the debounce are one read
	s.Refresh("HmIP-RF")
	s.Devices("HmIP-RF", "updated")
	s.Event("HmIP-RF", "00010000000A10:0", "RSSI_DEVICE")
	deadline := time.Now().Add(5 * time.Second)
	for li.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if li.count() != 1 {
		t.Fatalf("reads after the asks: %d", li.count())
	}
	// now a transfer is known: the timer reads at the slow rate
	time.Sleep(400 * time.Millisecond)
	if n := li.count(); n < 2 {
		t.Errorf("reads after 400 ms at a 150 ms rate: %d", n)
	}
}

// The follower reads journalctl's JSON lines from a fake and feeds the transfer lines in; a line
// of another kind or a message journald stored as bytes is skipped.
func TestRunJournalFollowsTheFake(t *testing.T) {
	li := newLiveInterface(t, dev("HmIP-RF", "00010000000A10", "DELIVER_FIRMWARE_IMAGE"))
	s := liveService(t, li)
	s.ReadLive(context.Background(), nil)
	argsCh := make(chan []string, 1)
	pr, pw := io.Pipe()
	s.Journal = func(ctx context.Context, name string, args ...string) (io.ReadCloser, error) {
		argsCh <- append([]string{name}, args...)
		go func() {
			<-ctx.Done()
			_ = pw.Close()
		}()
		return pr, nil
	}
	changed := make(chan Change, 8)
	s.Changed = func(c Change) { changed <- c }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RunJournal(ctx)
	line := func(msg any, us int64) string {
		b, _ := json.Marshal(map[string]any{"MESSAGE": msg, "__REALTIME_TIMESTAMP": fmt.Sprint(us), "PRIORITY": "7", "_SYSTEMD_UNIT": "hmipserver.service"})
		return string(b) + "\n"
	}
	at := time.Date(2026, 10, 14, 13, 19, 5, 0, time.UTC)
	go func() {
		_, _ = io.WriteString(pw, line(lineQueue, at.UnixMicro()))
		_, _ = io.WriteString(pw, line([]int{1, 2, 3}, at.UnixMicro()))
		_, _ = io.WriteString(pw, "not json\n")
		_, _ = io.WriteString(pw, line(lineContinue, at.UnixMicro()))
	}()
	select {
	case c := <-changed:
		if c.Address != "00010000000A10" || c.Transfer == nil || c.Transfer.Bytes != 420 || !c.Transfer.Updated.Equal(at) {
			t.Errorf("change: %+v %+v", c, c.Transfer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change from the follower")
	}
	argsSeen := <-argsCh
	if len(argsSeen) < 8 || argsSeen[0] != "journalctl" || argsSeen[6] != "hmipserver.service" || !strings.HasPrefix(argsSeen[len(argsSeen)-1], "@") {
		t.Errorf("journalctl args: %v", argsSeen)
	}
}

// Without a journal on the system the follower does nothing; without HmIP-RF it waits.
func TestRunJournalWithoutJournalOrInterface(t *testing.T) {
	s := NewService(New("http://127.0.0.1:1"), t.TempDir(), func() []interfaces.Interface { return nil }, nil)
	done := make(chan struct{})
	go func() {
		s.RunJournal(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunJournal without Journal did not return")
	}
	called := false
	s.Journal = func(ctx context.Context, name string, args ...string) (io.ReadCloser, error) {
		called = true
		return io.NopCloser(strings.NewReader("")), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s.RunJournal(ctx)
	if called {
		t.Error("journalctl started without an HmIP-RF interface")
	}
}

func TestJournalEntry(t *testing.T) {
	fallback := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stamp := time.Date(2026, 10, 14, 11, 19, 5, 0, time.UTC)
	at, msg, ok := journalEntry([]byte(`{"MESSAGE":"x","__REALTIME_TIMESTAMP":"`+fmt.Sprint(stamp.UnixMicro())+`"}`), fallback)
	if !ok || msg != "x" || !at.Equal(stamp) {
		t.Errorf("%v %q %v", at, msg, ok)
	}
	if at, _, ok := journalEntry([]byte(`{"MESSAGE":"y"}`), fallback); !ok || !at.Equal(fallback) {
		t.Errorf("without a stamp: %v %v", at, ok)
	}
	for _, b := range []string{`{"MESSAGE":[1,2]}`, `{"MESSAGE":""}`, `{}`, `nope`} {
		if _, _, ok := journalEntry([]byte(b), fallback); ok {
			t.Errorf("%s taken", b)
		}
	}
}

// The state file holds the transfers beside the last run.
func TestStateFileKeepsTransfers(t *testing.T) {
	s := NewService(New("http://127.0.0.1:1"), t.TempDir(), func() []interfaces.Interface { return nil }, nil)
	path := filepath.Join(t.TempDir(), "state.json")
	s.Open(path)
	s.Line(time.Now(), lineMissed)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if tr, ok := p.Transfers["00010000000A11"]; !ok || tr.Percent != 25 {
		t.Errorf("state file: %s", raw)
	}
}
