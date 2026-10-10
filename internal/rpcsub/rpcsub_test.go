package rpcsub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// A fake interface process: an XML-RPC server that takes init and ping the way rfd does -
// init calls system.listMethods and listDevices on the callback before it returns, ping sends
// a PONG event to every registered callback - and that a test can make send events, a
// multicall, a newDevices, or drop its registrations as a restart would.
type fakeDaemon struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex
	// registrations by callback URL -> id; an init with an empty id removes
	regs  map[string]string
	inits []string // every init's id, in order ("" for a removal)
	// keep says whether a restart keeps the registrations (hmipserver does). A kept entry is
	// mute until a fresh init: hmipserver calls listDevices on it and PONGs it, but delivers no
	// events to it (openccu-lite B-286, measured)
	keep bool
	mute map[string]bool
	// stuck: init and ping answer, and nothing is delivered - no callback in init, no PONG
	// (hmipserver held by a listener that does not answer, B-201)
	stuck bool
	// hang, when set, holds every init and ping until it is closed (rfd held the same way)
	hang chan struct{}
	// slow: a registering init takes the registration and its callbacks at once, then answers only
	// after this long (hmipserver's VirtualDevices without HmIP radio, B-270)
	slow time.Duration
	// regAt is when each callback's entry was written, as the handlers file's time would say
	regAt map[string]time.Time
}

// taken is what the handlers file would say: the entry is there, written at or after since.
func (f *fakeDaemon) taken(id, callback string, since time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.regs[callback] == id && !f.regAt[callback].Before(since)
}

func (f *fakeDaemon) initCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.inits {
		if x == id {
			n++
		}
	}
	return n
}

func (f *fakeDaemon) setStuck(v bool) {
	f.mu.Lock()
	f.stuck = v
	f.mu.Unlock()
}

func (f *fakeDaemon) held() (stuck bool, hang chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stuck, f.hang
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	f := &fakeDaemon{t: t, regs: map[string]string{}, regAt: map[string]time.Time{}, mute: map[string]bool{}}
	d := &xmlrpc.BasicDispatcher{}
	d.AddSystemMethods()
	d.HandleFunc("init", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		q := xmlrpc.Q(args)
		url, id := q.Idx(0).String(), q.Idx(1).String()
		stuck, hang := f.held()
		if hang != nil {
			<-hang
		}
		f.mu.Lock()
		f.inits = append(f.inits, id)
		if id == "" {
			delete(f.regs, url)
		} else {
			f.regs[url] = id
			f.regAt[url] = time.Now()
			delete(f.mute, url)
		}
		slow := f.slow
		f.mu.Unlock()
		if id != "" && !stuck {
			// what rfd does before init returns
			c := &xmlrpc.Client{Addr: strings.TrimPrefix(url, "http://")}
			if _, err := c.Call("system.listMethods", nil); err != nil {
				t.Errorf("fake: listMethods on %s: %v", url, err)
			}
			if v, err := c.Call("listDevices", xmlrpc.Values{xmlrpc.NewString(id)}); err != nil || v == nil || v.Array == nil || len(v.Array.Data) != 0 {
				t.Errorf("fake: listDevices on %s: %v %v", url, v, err)
			}
		}
		if id != "" && slow > 0 {
			time.Sleep(slow)
		}
		return xmlrpc.NewString(""), nil
	})
	d.HandleFunc("ping", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		caller := xmlrpc.Q(args).Idx(0).String()
		stuck, hang := f.held()
		if hang != nil {
			<-hang
		}
		if stuck {
			return xmlrpc.NewBool(true), nil
		}
		f.mu.Lock()
		regs := map[string]string{}
		for u, id := range f.regs {
			regs[u] = id
		}
		f.mu.Unlock()
		for u, id := range regs {
			c := &xmlrpc.Client{Addr: strings.TrimPrefix(u, "http://")}
			go c.Call("event", xmlrpc.Values{xmlrpc.NewString(id), xmlrpc.NewString("CENTRAL"), xmlrpc.NewString("PONG"), xmlrpc.NewString(caller)})
		}
		return xmlrpc.NewBool(true), nil
	})
	f.srv = httptest.NewServer(&xmlrpc.Handler{Dispatcher: d})
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDaemon) url() string { return "xmlrpc://" + strings.TrimPrefix(f.srv.URL, "http://") }

func (f *fakeDaemon) registrations() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.regs {
		out[k] = v
	}
	return out
}

// send delivers to every registered callback: one event, or a multicall of several. A kept
// entry that was not inited afresh gets nothing, as hmipserver's do.
func (f *fakeDaemon) send(events [][3]string, multicall bool) {
	for u, id := range f.registrations() {
		f.mu.Lock()
		mute := f.mute[u]
		f.mu.Unlock()
		if mute {
			continue
		}
		c := &xmlrpc.Client{Addr: strings.TrimPrefix(u, "http://")}
		if !multicall {
			for _, e := range events {
				if _, err := c.Call("event", xmlrpc.Values{xmlrpc.NewString(id), xmlrpc.NewString(e[0]), xmlrpc.NewString(e[1]), xmlrpc.NewString(e[2])}); err != nil {
					f.t.Errorf("fake: event: %v", err)
				}
			}
			continue
		}
		var calls []*xmlrpc.Value
		for _, e := range events {
			params := &xmlrpc.Value{Array: &xmlrpc.Array{Data: []*xmlrpc.Value{xmlrpc.NewString(id), xmlrpc.NewString(e[0]), xmlrpc.NewString(e[1]), xmlrpc.NewFloat64(2.5)}}}
			calls = append(calls, &xmlrpc.Value{Struct: &xmlrpc.Struct{Members: []*xmlrpc.Member{{Name: "methodName", Value: xmlrpc.NewString("event")}, {Name: "params", Value: params}}}})
		}
		v, err := c.Call("system.multicall", xmlrpc.Values{{Array: &xmlrpc.Array{Data: calls}}})
		if err != nil {
			f.t.Errorf("fake: multicall: %v", err)
		}
		// the answer is one [""] per call, which is what rfd expects
		if v == nil || v.Array == nil || len(v.Array.Data) != len(events) {
			f.t.Errorf("fake: multicall answer %v", v)
		}
	}
}

// call makes one call with the given arguments after the registration's id on every registered
// callback, as the daemon calls updateDevice or replaceDevice.
func (f *fakeDaemon) call(method string, args ...*xmlrpc.Value) {
	for u, id := range f.registrations() {
		c := &xmlrpc.Client{Addr: strings.TrimPrefix(u, "http://")}
		if _, err := c.Call(method, append(xmlrpc.Values{xmlrpc.NewString(id)}, args...)); err != nil {
			f.t.Errorf("fake: %s: %v", method, err)
		}
	}
}

// restart drops the registrations (rfd's way) or keeps them and calls listDevices on each
// (hmipserver's way).
func (f *fakeDaemon) restart() {
	f.mu.Lock()
	regs := f.regs
	if !f.keep {
		f.regs = map[string]string{}
	}
	for u := range regs {
		f.mute[u] = f.keep
	}
	f.mu.Unlock()
	if f.keep {
		for u, id := range regs {
			c := &xmlrpc.Client{Addr: strings.TrimPrefix(u, "http://")}
			_, _ = c.Call("listDevices", xmlrpc.Values{xmlrpc.NewString(id)})
		}
	}
}

// a handler that records what the bus hands it, and a wait for the next message of a type
type recorder struct {
	mu   sync.Mutex
	msgs []Message
	ch   chan Message
}

func newRecorder() *recorder { return &recorder{ch: make(chan Message, 100)} }

func (r *recorder) push(m Message) {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
	r.ch <- m
}
func (r *recorder) Event(e Event) {
	r.push(Message{Type: "event", Interface: e.Interface, Address: e.Address, Key: e.Key, Value: e.Value, Batch: e.Batch})
}
func (r *recorder) Interface(name, state string, _ time.Time) {
	r.push(Message{Type: "interface", Interface: name, State: state})
}
func (r *recorder) Devices(iface, op string, addrs []string) {
	r.push(Message{Type: "devices", Interface: iface, Op: op, Addresses: addrs})
}

// testWait bounds every wait for something that is going to happen: the waits end on the event
// itself, and the bound only says when to give up. It is generous on purpose - a CI runner beside
// an image build (load 14-19, the race detector) took more than 5 s for one callback (occulited
// B-40); a test that fails only takes this long when it fails.
const testWait = 30 * time.Second

func (r *recorder) next(t *testing.T, typ string) Message {
	t.Helper()
	deadline := time.After(testWait)
	for {
		select {
		case m := <-r.ch:
			if m.Type == typ {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s message within %s", typ, testWait)
		}
	}
}

func startSub(t *testing.T, cfg Config) (*Subscriber, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	if cfg.Interfaces == "" {
		cfg.Interfaces = filepath.Join(dir, "InterfacesList.xml") // absent: Set() drives the tests
	}
	cfg.Listen = "127.0.0.1:0"
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	if cfg.Watch == 0 {
		cfg.Watch = 50 * time.Millisecond
	}
	if cfg.InitTimeout == 0 {
		cfg.InitTimeout = testWait // a test that wants an init to fail sets its own bound
	}
	if cfg.InitGrace == 0 {
		cfg.InitGrace = time.Millisecond // the fakes call back inside init; anything later is a restart
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	for end := time.Now().Add(testWait); s.Base() == "" && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Base() == "" {
		cancel()
		<-done
		t.Fatalf("the listener did not start within %s", testWait)
	}
	t.Cleanup(func() { cancel(); <-done })
	return s, func() { cancel(); <-done }
}

// waitRegistered waits until the subscriber itself has the interface up - the fakes note a
// registration inside their init handler, before init returns and the subscriber says "up"
func waitRegistered(t *testing.T, s *Subscriber, name string) {
	t.Helper()
	waitFor(t, "the registration of "+name, func() bool {
		for _, i := range s.Status() {
			if i.Name == name && i.Registered && i.State == "up" {
				return true
			}
		}
		return false
	})
}

// pastInitGrace waits until the interface's last init lies further back than startSub's
// InitGrace, so that a call the daemon makes now is its own and not one of the init's - on a fast
// machine the fake's restart could otherwise come within the grace and go unnoticed.
func pastInitGrace(t *testing.T, s *Subscriber, name string) {
	t.Helper()
	waitFor(t, "the init grace of "+name, func() bool {
		for _, i := range s.Status() {
			if i.Name == name {
				at, err := time.Parse(time.RFC3339Nano, i.LastInit)
				return err == nil && time.Since(at) > 10*time.Millisecond
			}
		}
		return false
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(testWait); time.Now().Before(end); {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	if cond() {
		return
	}
	t.Fatalf("waiting for %s: not within %s", what, testWait)
}

func TestRegistersAnswersAndForwards(t *testing.T) {
	rfd := newFakeDaemon(t)
	s, _ := startSub(t, Config{})
	r := newRecorder()
	s.Attach(r)
	s.Set([]Entry{{Name: "BidCos-RF", URL: rfd.url()}, {Name: "CUxD", URL: "xmlrpc_bin://127.0.0.1:8701"}})
	// registered with the fixed id and the callback under the listener's real port
	waitRegistered(t, s, "BidCos-RF")
	for u, id := range rfd.registrations() {
		if id != "occulited_BidCos-RF" || !strings.HasPrefix(u, s.Base()+"/cb/BidCos-RF") {
			t.Fatalf("registration %s -> %s", u, id)
		}
	}
	// CUxD is BIN-RPC only and is not subscribed to
	if st := s.Status(); len(st) != 1 || st[0].Name != "BidCos-RF" || !st[0].Registered || st[0].State != "up" {
		t.Fatalf("status %+v", st)
	}
	if m := r.next(t, "interface"); m.State != "added" || m.Interface != "BidCos-RF" {
		t.Fatalf("added %+v", m)
	}
	if m := r.next(t, "interface"); m.State != "up" {
		t.Fatalf("up %+v", m)
	}
	// a single event, and a multicall whose events share the batch mark
	rfd.send([][3]string{{"JEQ9000001:1", "TEMPERATURE", "21.5"}}, false)
	e := r.next(t, "event")
	if e.Interface != "BidCos-RF" || e.Address != "JEQ9000001:1" || e.Key != "TEMPERATURE" || e.Value != "21.5" || e.Batch != 0 {
		t.Fatalf("event %+v", e)
	}
	rfd.send([][3]string{{"JEQ9000001:0", "UNREACH", ""}, {"JEQ9000001:0", "LOWBAT", ""}}, true)
	e1, e2 := r.next(t, "event"), r.next(t, "event")
	if e1.Batch == 0 || e1.Batch != e2.Batch || e1.Value != 2.5 {
		t.Fatalf("batch %+v %+v", e1, e2)
	}
	// the ring carries the same, with the batch mark equal to its first event's seq
	ring, ok := s.bus.Replay(0)
	var events []Message
	for _, m := range ring {
		if m.Type == "event" {
			events = append(events, m)
		}
	}
	if !ok || len(events) != 3 || events[1].Batch != events[1].Seq || events[2].Batch != events[1].Seq {
		t.Fatalf("ring %+v", events)
	}
	// the daemon's own PONG broadcast counts as activity
	if st := s.Status(); st[0].Events != 3 || st[0].LastActivity == "" {
		t.Fatalf("counters %+v", st)
	}
	// occulited task 13: a telegram is a call with a device's events - the single event and the
	// multicall are two, its two events one; a ping's PONG, alone or in a multicall, is none
	if st := s.Status(); st[0].Telegrams != 2 {
		t.Fatalf("telegrams after an event and a multicall: %+v", st[0])
	}
	rfd.send([][3]string{{"CENTRAL", "PONG", "x"}}, false)
	rfd.send([][3]string{{"CENTRAL", "PONG", "x"}}, true)
	r.next(t, "event")
	r.next(t, "event")
	if st := s.Status(); st[0].Telegrams != 2 || st[0].Events != 5 {
		t.Fatalf("a PONG counted as a telegram: %+v", st[0])
	}
	rfd.send([][3]string{{"CENTRAL", "PONG", "x"}, {"JEQ9000001:1", "STATE", "1"}}, true)
	r.next(t, "event")
	r.next(t, "event")
	if st := s.Status(); st[0].Telegrams != 3 {
		t.Fatalf("a multicall with a device's event beside a PONG: %+v", st[0])
	}
}

// occulited task 5: updateDevice's hint and replaceDevice's old and new device go onto the bus -
// its ring, which the remote stream reads - as the daemon sent them; a hint the call left out is
// 0, and still sent. The in-process handlers keep their address-only Devices call.
func TestDeviceCallsCarryHintAndOrder(t *testing.T) {
	rfd := newFakeDaemon(t)
	s, _ := startSub(t, Config{})
	s.Set([]Entry{{Name: "BidCos-RF", URL: rfd.url()}})
	waitRegistered(t, s, "BidCos-RF")
	rfd.call("updateDevice", xmlrpc.NewString("JEQ9000001"), &xmlrpc.Value{Int: "2"})
	rfd.call("updateDevice", xmlrpc.NewString("JEQ9000001"), &xmlrpc.Value{I4: "0"})
	rfd.call("updateDevice", xmlrpc.NewString("JEQ9000002"))
	rfd.call("replaceDevice", xmlrpc.NewString("JEQ0000OLD"), xmlrpc.NewString("JEQ0000NEW"))
	var dev []Message
	waitFor(t, "the device messages", func() bool {
		ring, _ := s.bus.Replay(0)
		dev = dev[:0]
		for _, m := range ring {
			if m.Type == "devices" {
				dev = append(dev, m)
			}
		}
		return len(dev) == 4
	})
	if m := dev[0]; m.Op != "updated" || m.Interface != "BidCos-RF" || len(m.Addresses) != 1 || m.Addresses[0] != "JEQ9000001" || m.Hint == nil || *m.Hint != 2 {
		t.Fatalf("updateDevice with hint 2: %+v", m)
	}
	if m := dev[1]; m.Hint == nil || *m.Hint != 0 {
		t.Fatalf("updateDevice with hint 0: %+v", m)
	}
	if m := dev[2]; m.Addresses[0] != "JEQ9000002" || m.Hint == nil || *m.Hint != 0 {
		t.Fatalf("updateDevice without a hint: %+v", m)
	}
	if m := dev[3]; m.Op != "replaced" || m.Old != "JEQ0000OLD" || m.New != "JEQ0000NEW" || len(m.Addresses) != 2 || m.Addresses[0] != "JEQ0000OLD" || m.Hint != nil {
		t.Fatalf("replaceDevice: %+v", m)
	}
	// on the wire: hint 0 is written
	if b, _ := json.Marshal(dev[1]); !strings.Contains(string(b), `"hint":0`) {
		t.Fatalf("hint 0 in JSON: %s", b)
	}
}

func TestReplayFromTheRing(t *testing.T) {
	rfd := newFakeDaemon(t)
	s, _ := startSub(t, Config{})
	s.Set([]Entry{{Name: "BidCos-RF", URL: rfd.url()}})
	waitRegistered(t, s, "BidCos-RF")
	rfd.send([][3]string{{"A:1", "K", "1"}, {"A:1", "K", "2"}}, false)
	waitFor(t, "the events", func() bool { return s.Seq() >= 4 })
	// a reader that saw seq 2 gets what came after it (the remote stream's Last-Event-ID)
	backlog, ok := s.bus.Replay(2)
	got := []string{}
	for _, m := range backlog {
		if m.Type == "event" {
			got = append(got, m.Value.(string))
		}
	}
	if !ok || strings.Join(got, ",") != "1,2" {
		t.Fatalf("replay %v %v", ok, got)
	}
	// and a raw reader gets the live messages from now on
	ch, _, stop := s.Messages()
	defer stop()
	rfd.send([][3]string{{"A:1", "K", "3"}}, false)
	select {
	case m := <-ch:
		if m.Type != "event" || m.Value != "3" {
			t.Fatalf("live %+v", m)
		}
	case <-time.After(testWait):
		t.Fatal("no live message")
	}
}

func TestDeregistersOnStopAndAfterARestartThatKeptTheEntry(t *testing.T) {
	hmip := newFakeDaemon(t)
	hmip.keep = true
	s, stop := startSub(t, Config{})
	s.Set([]Entry{{Name: "HmIP-RF", URL: hmip.url()}})
	waitRegistered(t, s, "HmIP-RF")
	r := newRecorder()
	s.Attach(r)
	pastInitGrace(t, s, "HmIP-RF")
	// the daemon restarts and calls listDevices on its own: restored, the reader is told, and
	// the entry is registered afresh (B-286)
	hmip.restart()
	if m := r.next(t, "interface"); m.State != "restarted" {
		t.Fatalf("restarted %+v", m)
	}
	waitFor(t, "the fresh init", func() bool { return hmip.initCount("occulited_HmIP-RF") == 2 })
	stop()
	// the removal came after a fresh init (measured: the plain removal did nothing then)
	hmip.mu.Lock()
	inits := hmip.inits
	hmip.mu.Unlock()
	if n := len(inits); n < 3 || inits[n-1] != "" || inits[n-2] != "occulited_HmIP-RF" {
		t.Fatalf("inits %v", inits)
	}
	if len(hmip.registrations()) != 0 {
		t.Fatalf("still registered: %v", hmip.registrations())
	}
}

// openccu-lite B-286: hmipserver keeps a subscriber's entry over its restart and calls
// listDevices on it, but delivers no events to it until a fresh init - on a lab system the feed
// counted nothing for five minutes after a restart, and events came within seconds of an init.
// The subscriber registers a kept entry afresh, and the events flow; the stop's re-init before
// the removal is then the fresh one.
func TestReinitsAKeptEntryAfterTheDaemonsRestart(t *testing.T) {
	hmip := newFakeDaemon(t)
	hmip.keep = true
	s, _ := startSub(t, Config{})
	s.Set([]Entry{{Name: "HmIP-RF", URL: hmip.url()}})
	waitRegistered(t, s, "HmIP-RF")
	r := newRecorder()
	s.Attach(r)
	pastInitGrace(t, s, "HmIP-RF")
	hmip.restart()
	if m := r.next(t, "interface"); m.State != "restarted" {
		t.Fatalf("restarted %+v", m)
	}
	// until the fresh init the kept entry is mute: what the daemon sends now is lost to it
	hmip.send([][3]string{{"0000000000000A:1", "PRESS_SHORT", "1"}}, false)
	waitFor(t, "the fresh init", func() bool { return hmip.initCount("occulited_HmIP-RF") == 2 })
	waitFor(t, "the entry inited afresh", func() bool {
		st := s.Status()[0]
		return st.Registered && !st.Restored && st.State == "up"
	})
	hmip.send([][3]string{{"0000000000000A:1", "PRESS_SHORT", "2"}}, false)
	if e := r.next(t, "event"); e.Interface != "HmIP-RF" || e.Key != "PRESS_SHORT" || e.Value != "2" {
		t.Fatalf("event after the fresh init %+v", e)
	}
	if st := s.Status()[0]; st.Events != 1 {
		t.Fatalf("the mute entry's event was counted: %+v", st)
	}
	// one restart, one fresh init: the daemon's newDevices after our init is not a second restart
	time.Sleep(200 * time.Millisecond)
	if n := hmip.initCount("occulited_HmIP-RF"); n != 2 {
		t.Fatalf("%d inits", n)
	}
}

func TestPingsAfterSilenceAndRegistersAgainWhenForgotten(t *testing.T) {
	rfd := newFakeDaemon(t)
	// the PONG must come back within PingTimeout or the answered ping counts as unanswered: a
	// second, not 200 ms, for a loaded runner (B-40)
	const pingTimeout = time.Second
	s, _ := startSub(t, Config{PingAfter: 300 * time.Millisecond, PingTimeout: pingTimeout})
	s.Set([]Entry{{Name: "BidCos-RF", URL: rfd.url()}})
	waitRegistered(t, s, "BidCos-RF")
	// silence: a ping goes out, the PONG comes back, the state stays up and nothing re-inits
	time.Sleep(600 * time.Millisecond)
	rfd.mu.Lock()
	inits := len(rfd.inits)
	rfd.mu.Unlock()
	if inits != 1 {
		t.Fatalf("re-init after an answered ping: %d inits", inits)
	}
	if st := s.Status()[0]; st.State != "up" {
		t.Fatalf("state after the ping %+v", st)
	}
	// the daemon restarts rfd's way and forgets us: the next ping reaches nobody, and the
	// subscriber registers again
	rfd.restart()
	waitFor(t, "the re-registration", func() bool { return len(rfd.registrations()) == 1 })
	waitRegistered(t, s, "BidCos-RF")
	if v := s.View(); !v.Connected || len(v.Interfaces) != 1 || v.Interfaces[0].State != "up" {
		t.Fatalf("view %+v", v)
	}
	// a daemon that forgot us and calls back on the new registration is not stuck (B-201): the
	// check runs once PingTimeout has passed since the re-registration
	time.Sleep(pingTimeout + 300*time.Millisecond)
	if st := s.Stalls(); len(st) != 0 {
		t.Fatalf("a restart taken for a stall: %+v", st)
	}
}

// B-201: the daemon answers init and ping but delivers nothing. The unanswered ping leads to a
// fresh registration; no callback after that either is a delivery stall. A callback ends it.
func TestDeliveryStall(t *testing.T) {
	hm := newFakeDaemon(t)
	s, _ := startSub(t, Config{PingAfter: 200 * time.Millisecond, PingTimeout: 150 * time.Millisecond})
	s.Set([]Entry{{Name: "HmIP-RF", URL: hm.url()}})
	waitRegistered(t, s, "HmIP-RF")
	hm.setStuck(true)
	waitFor(t, "the delivery stall", func() bool { return len(s.Stalls()) == 1 })
	st := s.Stalls()[0]
	if st.Interface != "HmIP-RF" || st.Kind != StallDelivery || st.Since.IsZero() || st.URL != hm.url() {
		t.Fatalf("stall %+v", st)
	}
	if v := s.Status()[0]; v.StallKind != StallDelivery || v.Stalled == "" || !v.Registered {
		t.Fatalf("status %+v", v)
	}
	// the listener that held it is gone: the next callback ends the stall
	hm.setStuck(false)
	hm.send([][3]string{{"0001:1", "STATE", "1"}}, false)
	waitFor(t, "the end of the stall", func() bool { return len(s.Stalls()) == 0 })
	if v := s.Status()[0]; v.Stalled != "" || v.StallKind != "" {
		t.Fatalf("status after %+v", v)
	}
}

// B-201: the daemon's calls do not answer at all (rfd held in an init). An answered call ends it.
func TestCallStall(t *testing.T) {
	rfd := newFakeDaemon(t)
	// InitTimeout bounds the held calls; a second rather than 200 ms, so that the re-init after the
	// hold ends is not taken for another stall on a loaded runner (B-40)
	s, _ := startSub(t, Config{PingAfter: 200 * time.Millisecond, PingTimeout: 150 * time.Millisecond, InitTimeout: time.Second})
	s.Set([]Entry{{Name: "BidCos-RF", URL: rfd.url()}})
	waitRegistered(t, s, "BidCos-RF")
	hang := make(chan struct{})
	rfd.mu.Lock()
	rfd.hang = hang
	rfd.mu.Unlock()
	waitFor(t, "the call stall", func() bool { return len(s.Stalls()) == 1 && s.Stalls()[0].Kind == StallCalls })
	rfd.mu.Lock()
	rfd.hang = nil
	rfd.mu.Unlock()
	close(hang)
	waitFor(t, "the end of the call stall", func() bool { return len(s.Stalls()) == 0 })
	waitRegistered(t, s, "BidCos-RF")
}

func TestDaemonDownThenBack(t *testing.T) {
	// the call to a port nobody listens on can end only at the init's bound, so the test keeps a
	// short one of its own rather than startSub's generous default
	s, _ := startSub(t, Config{InitTimeout: 3 * time.Second})
	r := newRecorder()
	s.Attach(r)
	// a port nobody listens on: down, retried with backoff, and never fatal
	s.Set([]Entry{{Name: "BidCos-RF", URL: "xmlrpc_bin://127.0.0.1:1"}})
	r.next(t, "interface") // added
	if m := r.next(t, "interface"); m.State != "down" {
		t.Fatalf("down %+v", m)
	}
	if st := s.Status()[0]; st.State != "down" || st.LastError == "" {
		t.Fatalf("status %+v", st)
	}
}

func TestParseInterfacesAndSpeaks(t *testing.T) {
	list, err := ParseInterfaces([]byte(`<?xml version="1.0" encoding="utf-8" ?>
<interfaces v="1.0">
  <ipc><name>BidCos-RF</name><url>xmlrpc_bin://127.0.0.1:32001</url><info>BidCos-RF</info></ipc>
  <ipc><name>VirtualDevices</name><url>xmlrpc://127.0.0.1:39292/groups</url><info>Virtual Devices</info></ipc>
  <ipc><name>HmIP-RF</name><url>xmlrpc://127.0.0.1:32010</url><info>HmIP-RF</info></ipc>
  <ipc><name>BidCos-Wired</name><url>xmlrpc_bin://127.0.0.1:32000</url><info>BidCoS-Wired</info></ipc>
  <ipc><name>CUxD</name><url>xmlrpc_bin://127.0.0.1:8701</url><info>CUxD</info></ipc>
  <ipc><name>CCU-Jack</name><url>xmlrpc://127.0.0.1:2121/RPC3</url><info>CCU-Jack</info></ipc>
</interfaces>`))
	if err != nil {
		t.Fatal(err)
	}
	var yes []string
	for _, e := range list {
		if Speaks(e) {
			yes = append(yes, e.Name)
		}
	}
	if strings.Join(yes, ",") != "BidCos-RF,VirtualDevices,HmIP-RF,BidCos-Wired,CCU-Jack" {
		t.Fatalf("speaks %v", yes)
	}
	if a, _ := callerAddr("xmlrpc://127.0.0.1:39292/groups"); a != "127.0.0.1:39292/groups" {
		t.Fatalf("addr %q", a)
	}
}

func TestStatusEndpoint(t *testing.T) {
	s, _ := startSub(t, Config{})
	resp, err := http.Get(s.Base() + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		BootID     string        `json:"boot_id"`
		Interfaces []IfaceStatus `json:"interfaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.BootID != s.BootID() {
		t.Fatalf("%v %+v", err, body)
	}
}

// task 79: a daemon's calls to the listener as trace lines - the process as the source, the
// method and its parameters, an answer that says something.
type traceRec struct {
	mu    sync.Mutex
	lines []string
}

func (r *traceRec) On() bool { return true }
func (r *traceRec) Line(l string) {
	r.mu.Lock()
	r.lines = append(r.lines, l)
	r.mu.Unlock()
}

func TestCallbackTrace(t *testing.T) {
	rfd := newFakeDaemon(t)
	tr := &traceRec{}
	s, cancel := startSub(t, Config{Trace: tr})
	defer cancel()
	s.Set([]Entry{{Name: "BidCos-RF", URL: rfd.url()}})
	waitRegistered(t, s, "BidCos-RF")
	rfd.send([][3]string{{"A:1", "K", "1"}}, false)
	rfd.send([][3]string{{"A:1", "K", "2"}, {"A:2", "K", "3"}}, true)
	deadline := time.Now().Add(testWait)
	for {
		tr.mu.Lock()
		n := len(tr.lines)
		tr.mu.Unlock()
		if n >= 5 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	joined := strings.Join(tr.lines, "\n")
	// the init's listMethods and listDevices (with the empty list answered), then the event and the multicall
	for _, want := range []string{"xmlrpc BidCos-RF → occulited system.listMethods ", "← occulited BidCos-RF system.listMethods [", "xmlrpc BidCos-RF → occulited listDevices ", "← occulited BidCos-RF listDevices []", `xmlrpc BidCos-RF → occulited event ["occulited_BidCos-RF","A:1","K","1"]`, `xmlrpc BidCos-RF → occulited system.multicall [[{"methodName":"event","params":["occulited_BidCos-RF","A:1","K",`, `← occulited BidCos-RF system.multicall [[""],[""]]`} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	// an event's "" answer is not a line
	if strings.Contains(joined, "← occulited BidCos-RF event") {
		t.Error("an event's empty answer was traced")
	}
}

// B-270: an init the daemon took but answered only after the bound counts as registered, once its
// handlers file shows the entry written since the init began - no retry, no stall of the calls.
func TestLateInitTakenCountsAsRegistered(t *testing.T) {
	hm := newFakeDaemon(t)
	hm.slow = 400 * time.Millisecond
	var asked sync.WaitGroup
	asked.Add(1)
	var once sync.Once
	s, _ := startSub(t, Config{InitTimeout: 150 * time.Millisecond, InitTimeouts: map[string]time.Duration{}, PingAfter: time.Hour,
		Taken: func(iface, id, callback string, since time.Time) bool {
			once.Do(asked.Done)
			return iface == "VirtualDevices" && hm.taken(id, callback, since)
		}})
	s.Set([]Entry{{Name: "VirtualDevices", URL: hm.url()}})
	waitRegistered(t, s, "VirtualDevices")
	asked.Wait()
	time.Sleep(time.Second) // 20 watch ticks: a failed init would have been tried again after 5 s at the earliest, a stall shows at once
	if n := hm.initCount("occulited_VirtualDevices"); n != 1 {
		t.Fatalf("inits %d, want 1", n)
	}
	if st := s.Stalls(); len(st) != 0 {
		t.Fatalf("stalls %+v", st)
	}
	if v := s.Status()[0]; !v.Registered || v.State != "up" || v.LastError != "" {
		t.Fatalf("status %+v", v)
	}
}

// B-270: the same late init without the handlers entry is today's failure - down, a calls stall,
// tried again.
func TestLateInitNotTakenFails(t *testing.T) {
	hm := newFakeDaemon(t)
	hm.slow = 400 * time.Millisecond
	s, _ := startSub(t, Config{InitTimeout: 150 * time.Millisecond, InitTimeouts: map[string]time.Duration{}, PingAfter: time.Hour,
		Taken: func(iface, id, callback string, since time.Time) bool { return false }})
	s.Set([]Entry{{Name: "VirtualDevices", URL: hm.url()}})
	waitFor(t, "the calls stall", func() bool { st := s.Stalls(); return len(st) == 1 && st[0].Kind == StallCalls })
	if v := s.Status()[0]; v.Registered || v.State != "down" || !strings.Contains(v.LastError, "no answer") {
		t.Fatalf("status %+v", v)
	}
}

// B-270: an entry written before the init began (a dead run's, which the daemon keeps) is no proof
// that this init was taken.
func TestLateInitStaleEntryIsNotTaken(t *testing.T) {
	hm := newFakeDaemon(t)
	hm.slow = 400 * time.Millisecond
	s, _ := startSub(t, Config{InitTimeout: 150 * time.Millisecond, InitTimeouts: map[string]time.Duration{}, PingAfter: time.Hour,
		Taken: func(iface, id, callback string, since time.Time) bool {
			return hm.taken(id, callback, since.Add(time.Hour)) // as if the file were older than the init
		}})
	s.Set([]Entry{{Name: "VirtualDevices", URL: hm.url()}})
	waitFor(t, "the calls stall", func() bool { return len(s.Stalls()) == 1 })
	if v := s.Status()[0]; v.Registered {
		t.Fatalf("status %+v", v)
	}
}

// B-270: VirtualDevices' init has a bound of its own; an init that answers within it is an
// ordinary registration, while the same wait on another interface is a failure.
func TestInitTimeoutPerInterface(t *testing.T) {
	vd, rfd := newFakeDaemon(t), newFakeDaemon(t)
	vd.slow, rfd.slow = 300*time.Millisecond, 300*time.Millisecond
	s, _ := startSub(t, Config{InitTimeout: 150 * time.Millisecond, InitTimeouts: map[string]time.Duration{"VirtualDevices": testWait}, PingAfter: time.Hour})
	s.Set([]Entry{{Name: "VirtualDevices", URL: vd.url()}, {Name: "BidCos-RF", URL: rfd.url()}})
	waitRegistered(t, s, "VirtualDevices")
	waitFor(t, "BidCos-RF down", func() bool {
		for _, v := range s.Status() {
			if v.Name == "BidCos-RF" && v.State == "down" && strings.Contains(v.LastError, "within 150ms") {
				return true
			}
		}
		return false
	})
	if d := New(Config{}).initTimeout("VirtualDevices"); d != 30*time.Second {
		t.Fatalf("default VirtualDevices bound %s", d)
	}
	if d := New(Config{}).initTimeout("HmIP-RF"); d != 10*time.Second {
		t.Fatalf("default HmIP-RF bound %s", d)
	}
}

// task 34: at a shutdown the interface daemons stop before occulited; a deregistration that finds
// the daemon's port closed is no failure - nothing is left to take out - and no warning.
func TestNoDeregistrationWarningForAStoppedDaemon(t *testing.T) {
	hmip := newFakeDaemon(t)
	var mu sync.Mutex
	var logged strings.Builder
	w := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return logged.Write(p) })
	s, stop := startSub(t, Config{Log: slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	s.Set([]Entry{{Name: "HmIP-RF", URL: hmip.url()}})
	waitRegistered(t, s, "HmIP-RF")
	hmip.srv.Close() // the daemon stopped first
	stop()
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(logged.String(), "deregistration failed") {
		t.Fatalf("warned: %s", logged.String())
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
