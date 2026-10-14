package servicemsg

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// a fake interface process for the sweep: two devices, channel 0's description with the
// service flag on UNREACH, LOWBAT and CONFIG_PENDING but not on RSSI_DEVICE, and values a test
// sets
type fakeIface struct {
	mu     sync.Mutex
	values map[string]map[string]any // channel -> key -> value
	read   map[string]int            // channel -> how often its VALUES were read
	// hold, when set, makes the next read of a channel answer with the values it found on
	// entry only after the test closes the channel it was handed on held (B-49)
	hold chan chan struct{}
	// fail makes a channel's VALUES answer a fault (a device that does not answer)
	fail map[string]bool
	srv  *httptest.Server
}

func newFakeIface(t *testing.T) *fakeIface {
	f := &fakeIface{values: map[string]map[string]any{}, read: map[string]int{}}
	d := &xmlrpc.BasicDispatcher{}
	desc := func(addr, typ, parent string, ch int) *xmlrpc.Value {
		m := map[string]interface{}{"ADDRESS": addr, "TYPE": typ, "PARENT": parent, "CHILDREN": []interface{}{}, "FIRMWARE": "1.0", "AVAILABLE_FIRMWARE": ""}
		if parent == "" {
			m["CHILDREN"] = []interface{}{addr + ":0", addr + ":1"}
		}
		v, _ := xmlrpc.NewMap(m)
		return v
	}
	d.HandleFunc("listDevices", func(*xmlrpc.Value) (*xmlrpc.Value, error) {
		return &xmlrpc.Value{Array: &xmlrpc.Array{Data: []*xmlrpc.Value{
			desc("ABC0000001", "HmIP-WRC2", "", 0), desc("ABC0000001:0", "MAINTENANCE", "ABC0000001", 0), desc("ABC0000001:1", "KEY", "ABC0000001", 1),
			desc("ABC0000002", "HmIP-PDT", "", 0), desc("ABC0000002:0", "MAINTENANCE", "ABC0000002", 0),
		}}}, nil
	})
	d.HandleFunc("getParamsetDescription", func(*xmlrpc.Value) (*xmlrpc.Value, error) {
		p := func(flags int) map[string]interface{} { return map[string]interface{}{"FLAGS": flags, "TYPE": "BOOL"} }
		// STICKY_UNREACH as rfd describes it: visible, service and sticky (FLAGS 25)
		v, _ := xmlrpc.NewMap(map[string]interface{}{"UNREACH": p(9), "STICKY_UNREACH": p(25), "LOWBAT": p(9), "CONFIG_PENDING": p(9), "RSSI_DEVICE": p(1)})
		return v, nil
	})
	d.HandleFunc("getParamset", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		ch := xmlrpc.Q(args).Idx(0).String()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.read[ch]++
		if f.fail[ch] {
			return nil, &xmlrpc.MethodError{Code: -1, Message: "Failure"}
		}
		vals := map[string]interface{}{}
		for k, v := range f.values[ch] {
			vals[k] = v
		}
		hold := f.hold
		f.hold = nil
		f.mu.Unlock()
		if hold != nil {
			release := make(chan struct{})
			hold <- release
			<-release
		}
		f.mu.Lock()
		v, _ := xmlrpc.NewMap(vals)
		return v, nil
	})
	f.srv = httptest.NewServer(&xmlrpc.Handler{Dispatcher: d})
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIface) set(ch string, vals map[string]any) {
	f.mu.Lock()
	f.values[ch] = vals
	f.mu.Unlock()
}

func (f *fakeIface) reads(ch string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.read[ch]
}

func (f *fakeIface) interfaces() []interfaces.Interface {
	return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(f.srv.URL, "http://")}})
}

func keys(s Snapshot) string {
	var out []string
	for _, m := range s.Messages {
		out = append(out, m.Address+":"+m.Channel+":"+m.Key+"="+m.Seen)
	}
	return strings.Join(out, " ")
}

func TestSweepAndTransitions(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "LOWBAT": true, "CONFIG_PENDING": false, "RSSI_DEVICE": -60})
	fi.set("ABC0000002:0", map[string]any{"UNREACH": true, "LOWBAT": false, "CONFIG_PENDING": false})
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	changes := 0
	st := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, Now: func() time.Time { return now }, OnChange: func() { changes++ }}
	st.Sweep(context.Background())
	// what a sweep finds is "seen at start"; RSSI_DEVICE has no service flag
	if got := keys(st.List()); got != "ABC0000001:0:LOWBAT=start ABC0000002:0:UNREACH=start" {
		t.Fatalf("after the sweep: %q", got)
	}
	if changes != 1 || st.List().Messages[0].Type != "HmIP-WRC2" || !st.List().Messages[0].Since.Equal(now) {
		t.Fatalf("changes %d, first %+v", changes, st.List().Messages[0])
	}
	// an event makes one active (seen: event), the same value again changes nothing, and false clears it
	later := now.Add(time.Minute)
	st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000001:0", Key: "UNREACH", Value: true, Time: later})
	if got := keys(st.List()); got != "ABC0000001:0:LOWBAT=start ABC0000002:0:UNREACH=start ABC0000001:0:UNREACH=event" || changes != 2 {
		t.Fatalf("after the event: %q (%d)", got, changes)
	}
	st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000001:0", Key: "UNREACH", Value: true, Time: later.Add(time.Second)})
	if changes != 2 {
		t.Fatal("the same value again counted as a change")
	}
	st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000001:0", Key: "UNREACH", Value: false, Time: later.Add(2 * time.Second)})
	if got := keys(st.List()); got != "ABC0000001:0:LOWBAT=start ABC0000002:0:UNREACH=start" || changes != 3 {
		t.Fatalf("after the clear: %q", got)
	}
	// a datapoint without the flag is not a message, whatever its value
	st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000001:0", Key: "RSSI_DEVICE", Value: -50, Time: later})
	if st.List().Count != 2 {
		t.Fatal("RSSI_DEVICE became a message")
	}
	// the next sweep: the stored LOWBAT keeps its since, a cleared UNREACH goes, a new fault code is a new message
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "LOWBAT": true, "CONFIG_PENDING": true})
	fi.set("ABC0000002:0", map[string]any{"UNREACH": false, "LOWBAT": false, "CONFIG_PENDING": false})
	now = now.Add(15 * time.Minute)
	st.Sweep(context.Background())
	got := st.List()
	if keys(got) != "ABC0000001:0:LOWBAT=start ABC0000001:0:CONFIG_PENDING=start" {
		t.Fatalf("after the second sweep: %q", keys(got))
	}
	if !got.Messages[0].Since.Equal(now.Add(-15*time.Minute)) || !got.Messages[1].Since.Equal(now) {
		t.Fatalf("since kept / new: %v %v", got.Messages[0].Since, got.Messages[1].Since)
	}
}

func TestActiveValues(t *testing.T) {
	for v, want := range map[any]bool{true: true, false: false, 0: false, 4: true, 0.0: false, "": false, "0": false, "x": true} {
		if Active(v) != want {
			t.Errorf("Active(%v) = %v", v, !want)
		}
	}
	// an enum fault code changing is a new message with a new since
	st := &Store{Now: time.Now}
	st.active = map[string]Message{}
	st.Event(rpcsub.Event{Interface: "BidCos-RF", Address: "JEQ9000001:0", Key: "FAULT_REPORTING", Value: 4, Time: time.Unix(100, 0)})
	st.Event(rpcsub.Event{Interface: "BidCos-RF", Address: "JEQ9000001:0", Key: "FAULT_REPORTING", Value: 6, Time: time.Unix(200, 0)})
	l := st.List()
	if l.Count != 1 || l.Messages[0].Value != 6 || !l.Messages[0].Since.Equal(time.Unix(200, 0)) {
		t.Fatalf("%+v", l.Messages)
	}
}

func TestSweepKeepsEntriesOfAnInterfaceThatDoesNotAnswer(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"UNREACH": true})
	st := &Store{Interfaces: fi.interfaces, Timeout: time.Second}
	st.Sweep(context.Background())
	if st.List().Count != 1 {
		t.Fatal("no message after the sweep")
	}
	fi.srv.Close()
	st.Sweep(context.Background())
	l := st.List()
	if l.Count != 1 || l.Errors["HmIP-RF"] == "" {
		t.Fatalf("a dead interface took its messages with it: %+v", l)
	}
}

// run starts the store's loop and stops it with the test.
func run(t *testing.T, st *Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); st.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("it never happened: %s", what)
		}
	}
}

// B-46: rfd resets STICKY_UNREACH when it is acknowledged and sends no event for it. While a
// sticky message stands, its device's channel 0 is read again every Recheck - and only that
// device's; what the reads find goes to Report.
func TestAStickyMessageIsReadAgainUntilItIsAcknowledged(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": true, "LOWBAT": false})
	fi.set("ABC0000002:0", map[string]any{"UNREACH": false, "LOWBAT": true})
	var mu sync.Mutex
	reported := map[string][]any{}
	changes := 0
	st := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, Every: time.Hour, Recheck: 20 * time.Millisecond,
		OnChange: func() { mu.Lock(); changes++; mu.Unlock() },
		Report: func(iface, channel string, vals map[string]any, _ time.Time) {
			mu.Lock()
			reported[iface+" "+channel] = append(reported[iface+" "+channel], vals["STICKY_UNREACH"])
			mu.Unlock()
		}}
	run(t, st)
	eventually(t, "the sweep", func() bool { return st.List().Count == 2 })
	eventually(t, "three reads of the device with the sticky message", func() bool { return fi.reads("ABC0000001:0") >= 4 })
	if n := fi.reads("ABC0000002:0"); n != 1 {
		t.Fatalf("the device without a sticky message was read %d times, the sweep reads it once", n)
	}
	mu.Lock()
	c := changes
	mu.Unlock()
	if c != 1 {
		t.Fatalf("reads that found nothing new counted as %d changes", c-1)
	}
	// the acknowledgement: the value goes, no event comes
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": false, "LOWBAT": false})
	eventually(t, "the acknowledged message goes", func() bool { return keys(st.List()) == "ABC0000002:0:LOWBAT=start" })
	// the read tells Report and OnChange after it let go of the store's lock, a moment after the
	// list shows the change
	eventually(t, "the change is told", func() bool { mu.Lock(); defer mu.Unlock(); return changes == 2 })
	mu.Lock()
	got := append([]any{}, reported["HmIP-RF ABC0000001:0"]...)
	sweep := len(reported["HmIP-RF ABC0000002:0"])
	c = changes
	mu.Unlock()
	if len(got) < 2 || got[0] != true || got[len(got)-1] != false || sweep != 1 || c != 2 {
		t.Fatalf("reported %v, the sweep's %d, changes %d", got, sweep, c)
	}
	// nothing sticky stands: the reading ends (one read may have been on its way)
	n := fi.reads("ABC0000001:0")
	time.Sleep(150 * time.Millisecond)
	if after := fi.reads("ABC0000001:0"); after > n+1 {
		t.Fatalf("still reading after the acknowledgement: %d -> %d", n, after)
	}
}

// B-46: UNREACH=false is the one event the device's return sends; the acknowledgement that
// follows sends none, so the event plans a read a moment later.
func TestAnEventThatClearsUnreachReadsTheDeviceSoon(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"UNREACH": true, "STICKY_UNREACH": true})
	st := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, Every: time.Hour, Recheck: time.Hour, RecheckAfter: 20 * time.Millisecond}
	run(t, st)
	eventually(t, "the sweep", func() bool { return st.List().Count == 2 })
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": false})
	st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000001:0", Key: "UNREACH", Value: false, Time: time.Now()})
	eventually(t, "the sticky message goes without an event of its own", func() bool { return st.List().Count == 0 })
	if n := fi.reads("ABC0000001:0"); n != 2 {
		t.Fatalf("%d reads: the sweep's and one after the event are two", n)
	}
	// an event that clears a message of a device without a sticky one plans nothing
	fi.set("ABC0000002:0", map[string]any{"LOWBAT": true})
	st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000002:0", Key: "LOWBAT", Value: true, Time: time.Now()})
	st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000002:0", Key: "LOWBAT", Value: false, Time: time.Now()})
	time.Sleep(100 * time.Millisecond)
	if n := fi.reads("ABC0000002:0"); n != 1 {
		t.Fatalf("a device without a sticky message was read again: %d", n)
	}
}

// B-46: a write to a maintenance channel that passed through occulited is read back; the read
// reconciles the whole channel by the sweep's rules and keeps the entries when it fails.
func TestRecheckSoonReadsOneDevice(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"LOWBAT": true, "CONFIG_PENDING": false})
	fi.set("ABC0000002:0", map[string]any{"UNREACH": true})
	st := &Store{Interfaces: fi.interfaces, Timeout: time.Second, Every: time.Hour, Recheck: time.Hour}
	run(t, st)
	eventually(t, "the sweep", func() bool { return st.List().Count == 2 })
	fi.set("ABC0000001:0", map[string]any{"LOWBAT": false, "CONFIG_PENDING": true})
	fi.set("ABC0000002:0", map[string]any{"UNREACH": false}) // not asked for: stays until its own event or the sweep
	st.RecheckSoon("HmIP-RF", "ABC0000001", 0)
	eventually(t, "the read", func() bool {
		return keys(st.List()) == "ABC0000002:0:UNREACH=start ABC0000001:0:CONFIG_PENDING=start"
	})
	// an interface that does not answer keeps the device's messages
	fi.srv.Close()
	st.RecheckSoon("HmIP-RF", "ABC0000001", 0)
	time.Sleep(100 * time.Millisecond)
	if st.List().Count != 2 {
		t.Fatalf("a failed read took messages with it: %q", keys(st.List()))
	}
}

// Without a description the name decides what is sticky, as it decides what is a service message.
func TestStickyByNameBeforeTheFirstSweep(t *testing.T) {
	st := &Store{}
	st.Event(rpcsub.Event{Interface: "BidCos-RF", Address: "JEQ9000001:0", Key: "STICKY_UNREACH", Value: true, Time: time.Now()})
	st.Event(rpcsub.Event{Interface: "BidCos-RF", Address: "JEQ9000002:0", Key: "LOWBAT", Value: true, Time: time.Now()})
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.due["BidCos-RF|JEQ9000001"]; !ok || len(st.due) != 1 {
		t.Fatalf("planned reads: %v", st.due)
	}
}

// A read asked for before any sweep got through (the interface was down at the start) works on
// an empty store.
func TestRecheckSoonBeforeTheFirstSweepGotThrough(t *testing.T) {
	fi, down := newFakeIface(t), newFakeIface(t)
	down.srv.Close()
	up := false
	var mu sync.Mutex
	st := &Store{Timeout: time.Second, Every: time.Hour, Recheck: time.Hour, Interfaces: func() []interfaces.Interface {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			return down.interfaces()
		}
		return fi.interfaces()
	}}
	run(t, st)
	eventually(t, "the failed sweep", func() bool { return st.List().Errors["HmIP-RF"] != "" })
	fi.set("ABC0000001:0", map[string]any{"UNREACH": true})
	mu.Lock()
	up = true
	mu.Unlock()
	st.RecheckSoon("HmIP-RF", "ABC0000001", 0)
	eventually(t, "the read", func() bool { return keys(st.List()) == "ABC0000001:0:UNREACH=start" })
}

// B-49: a read that began before an event answers with the value the event replaced; the
// event's value stands until a read that began after it.
func TestAReadOlderThanAnEventDoesNotUndoIt(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"UNREACH": true, "STICKY_UNREACH": false})
	fi.set("ABC0000002:0", map[string]any{"UNREACH": false})
	clock := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	var cmu sync.Mutex
	now := func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock }
	tick := func() { cmu.Lock(); clock = clock.Add(time.Second); cmu.Unlock() }
	st := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, Now: now}
	st.Sweep(context.Background())
	if got := keys(st.List()); got != "ABC0000001:0:UNREACH=start" {
		t.Fatalf("after the sweep: %q", got)
	}
	for _, c := range []struct {
		name, device string
		event        bool // the event's value; the read answers its opposite
		want         string
	}{
		{"an event clears the message while the read is on its way", "ABC0000001", false, ""},
		{"an event raises a message while the read is on its way", "ABC0000002", true, "ABC0000002:0:UNREACH=event"},
	} {
		held := make(chan chan struct{})
		fi.mu.Lock()
		fi.hold = held
		fi.mu.Unlock()
		done := make(chan struct{})
		go func() { defer close(done); st.read(context.Background(), "HmIP-RF", c.device) }()
		release := <-held // the read has its (old) answer
		tick()
		fi.set(c.device+":0", map[string]any{"UNREACH": c.event})
		st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: c.device + ":0", Key: "UNREACH", Value: c.event, Time: now()})
		close(release)
		<-done
		if got := keys(st.List()); got != c.want {
			t.Fatalf("%s: %q, want %q", c.name, got, c.want)
		}
		// a read that begins after the event is believed again
		tick()
		st.read(context.Background(), "HmIP-RF", c.device)
		if got := keys(st.List()); got != c.want {
			t.Fatalf("%s, the next read: %q", c.name, got)
		}
	}
	// the sweep has the same guard
	held := make(chan chan struct{})
	fi.mu.Lock()
	fi.hold = held
	fi.mu.Unlock()
	done := make(chan struct{})
	tick()
	go func() { defer close(done); st.Sweep(context.Background()) }()
	release := <-held // the first device's read: still UNREACH false
	tick()
	st.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000001:0", Key: "UNREACH", Value: true, Time: now()})
	fi.set("ABC0000001:0", map[string]any{"UNREACH": true})
	close(release)
	<-done
	if got := keys(st.List()); got != "ABC0000002:0:UNREACH=event ABC0000001:0:UNREACH=event" {
		t.Fatalf("after the sweep that was overtaken: %q", got)
	}
	st.mu.Lock()
	n := len(st.evented)
	st.mu.Unlock()
	if n != 1 {
		t.Fatalf("event times kept: %d, only the one no read has overtaken yet", n)
	}
}
