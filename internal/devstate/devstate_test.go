package devstate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/store"
)

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

// ts moves with every report, lc only with a change; the change keeps the previous value and how
// long it stood; a datapoint outside the chosen set is not kept but is seen by OnObserve.
func TestObserveTimestampsAndPrevious(t *testing.T) {
	var seen []string
	s := &Store{OnObserve: func(iface, address, dp string, v any, _ time.Time) { seen = append(seen, dp) }}
	o := s.observe("HmIP-RF", "A:1", "MOTION", false, at(0), SourceEvent)
	if !o.tracked || !o.changed || !o.e.Confirmed || o.e.Source != SourceEvent || !o.e.TS.Equal(at(0)) || !o.e.LC.Equal(at(0)) || o.e.PreviousForS != nil {
		t.Fatalf("new: %+v", o)
	}
	o = s.observe("HmIP-RF", "A:1", "MOTION", false, at(10), SourceEvent)
	if o.changed || !o.e.TS.Equal(at(10)) || !o.e.LC.Equal(at(0)) {
		t.Fatalf("re-report: %+v", o)
	}
	o = s.observe("HmIP-RF", "A:1", "MOTION", true, at(30), SourceEvent)
	if !o.changed || !o.e.LC.Equal(at(30)) || o.e.Previous != false || o.e.PreviousForS == nil || *o.e.PreviousForS != 30 {
		t.Fatalf("change: %+v", o)
	}
	if o := s.observe("HmIP-RF", "A:1", "RSSI_DEVICE", -60, at(31), SourceEvent); o.tracked {
		t.Errorf("RSSI_DEVICE kept")
	}
	if s.Count() != 1 || fmt.Sprint(seen) != "[MOTION MOTION MOTION RSSI_DEVICE]" {
		t.Errorf("count %d, seen %v", s.Count(), seen)
	}
	// an int64 and an int are the same value
	s.observe("HmIP-RF", "A:1", "LEVEL", int64(1), at(40), SourceEvent)
	if o := s.observe("HmIP-RF", "A:1", "LEVEL", 1, at(41), SourceEvent); o.changed {
		t.Errorf("int64 vs int counted as a change")
	}
}

func TestTrackedKeys(t *testing.T) {
	for _, k := range []string{"STATE", "LEVEL", "ACTUAL_TEMPERATURE", "UNREACH", "LOW_BAT", "ERROR_OVERHEAT", "PRESS_SHORT", "SET_POINT_TEMPERATURE"} {
		if !Tracked(k) {
			t.Errorf("%s not tracked", k)
		}
	}
	for _, k := range []string{"RSSI_DEVICE", "WORKING", "PROCESS", "STOP", "INSTALL_TEST", "OPERATING_VOLTAGE_STATUS"} {
		if Tracked(k) {
			t.Errorf("%s tracked", k)
		}
	}
	if l := Keys(); l[len(l)-1] != "ERROR_*" || len(l) < 50 {
		t.Errorf("keys %v", l)
	}
}

// The bus hook: an event of a kept datapoint carries its lc and the previous duration.
func TestEnrich(t *testing.T) {
	s := &Store{}
	m := rpcsub.Message{Type: "event", Interface: "HmIP-RF", Address: "A:1", Key: "STATE", Value: false}
	s.Enrich(&m, at(0))
	m2 := rpcsub.Message{Type: "event", Interface: "HmIP-RF", Address: "A:1", Key: "STATE", Value: true}
	s.Enrich(&m2, at(5))
	if m.LC != at(0).Format(time.RFC3339Nano) || m2.LC != at(5).Format(time.RFC3339Nano) || m2.PreviousForS == nil || *m2.PreviousForS != 5 {
		t.Errorf("enriched %+v / %+v", m, m2)
	}
	other := rpcsub.Message{Type: "event", Interface: "HmIP-RF", Address: "A:1", Key: "RSSI_PEER", Value: 1}
	s.Enrich(&other, at(6))
	iface := rpcsub.Message{Type: "interface", Interface: "HmIP-RF", State: "up"}
	s.Enrich(&iface, at(6))
	if other.LC != "" || s.Count() != 1 {
		t.Errorf("untracked enriched: %+v", other)
	}
}

func TestCodecRoundTrip(t *testing.T) {
	for _, v := range []any{nil, true, false, 0, -7, 1 << 30, 21.5, "", "Ümlaut", map[string]any{"a": 1.0}, []any{"x"}} {
		en := &entry{value: v, ts: at(1), lc: at(0), prev: "before", hasPrev: true, prevFor: 1500 * time.Millisecond}
		got, ok := decode(encode(en))
		if !ok || !reflect.DeepEqual(got.value, v) || got.prev != "before" || !got.hasPrev || got.prevFor != 1500*time.Millisecond || !got.ts.Equal(at(1)) || !got.lc.Equal(at(0)) {
			t.Errorf("%#v: %+v %v", v, got, ok)
		}
	}
	plain, ok := decode(encode(&entry{value: 3, ts: at(1), lc: at(1)}))
	if !ok || plain.hasPrev || plain.value != 3 {
		t.Errorf("no previous: %+v", plain)
	}
	for _, bad := range [][]byte{nil, {9}, append(encode(&entry{value: "x"})[:26], tagString, 50, 'a'), append(encode(&entry{value: 1})[:26], 77)} {
		if _, ok := decode(bad); ok {
			t.Errorf("decoded %v", bad)
		}
	}
}

// What comes back from the file is not confirmed until a report; memory wins over the file.
func TestRestoreAndConfirm(t *testing.T) {
	src := &Store{}
	src.observe("HmIP-RF", "A:1", "STATE", true, at(0), SourceEvent)
	src.observe("HmIP-RF", "A:1", "STATE", false, at(60), SourceEvent)
	src.observe("BidCos-RF", "B:0", "UNREACH", false, at(1), SourceSweep)
	ents, _ := src.PendingEntries(true, false)
	groups := map[string]map[string][]byte{}
	for _, e := range ents {
		if groups[e.Group] == nil {
			groups[e.Group] = map[string][]byte{}
		}
		groups[e.Group][e.Key] = e.Value
	}
	groups["HmIP-RF"]["broken"] = []byte{1}
	groups["HmIP-RF"]["C:1\x00STATE"] = []byte{42}
	s := &Store{}
	s.observe("BidCos-RF", "B:0", "UNREACH", true, at(100), SourceEvent) // memory's, newer
	s.RestoreEntries(groups)
	e, ok := s.Get("HmIP-RF", "A:1", "STATE")
	if !ok || e.Confirmed || e.Source != SourceRestored || e.Value != false || !e.LC.Equal(at(60)) || e.ConfirmedAt != nil || e.PreviousForS == nil || *e.PreviousForS != 60 {
		t.Fatalf("restored %+v", e)
	}
	if e, _ := s.Get("BidCos-RF", "B:0", "UNREACH"); e.Value != true || !e.Confirmed {
		t.Errorf("memory lost to the file: %+v", e)
	}
	if p := s.Read(Filter{}, "", 0); p.Total != 2 || p.Unconfirmed != 1 {
		t.Errorf("page %+v", p)
	}
	// the same value reported: confirmed, lc stays, ts moves
	o := s.observe("HmIP-RF", "A:1", "STATE", false, at(500), SourceSweep)
	if !o.changed || !o.e.Confirmed || o.e.Source != SourceSweep || !o.e.LC.Equal(at(60)) || !o.e.TS.Equal(at(500)) || o.e.ConfirmedAt == nil {
		t.Errorf("confirmed %+v", o.e)
	}
}

// Pending: after an open everything; then only what changed; urgent only the value changes. A
// change between Pending and its done keeps the entry dirty.
func TestPendingEntries(t *testing.T) {
	kicks := 0
	s := &Store{Kick: func() { kicks++ }}
	s.observe("HmIP-RF", "A:1", "STATE", true, at(0), SourceEvent)
	s.observe("HmIP-RF", "A:2", "STATE", true, at(0), SourceEvent)
	_, done := s.PendingEntries(false, false)
	done()
	if kicks != 2 {
		t.Errorf("kicks %d", kicks)
	}
	s.observe("HmIP-RF", "A:1", "STATE", true, at(10), SourceEvent)  // ts only
	s.observe("HmIP-RF", "A:2", "STATE", false, at(10), SourceEvent) // a change
	if kicks != 3 {
		t.Errorf("a ts-only report kicked: %d", kicks)
	}
	urgent, done := s.PendingEntries(false, true)
	if len(urgent) != 1 || urgent[0].Key != "A:2\x00STATE" {
		t.Fatalf("urgent %+v", urgent)
	}
	s.observe("HmIP-RF", "A:2", "STATE", false, at(11), SourceEvent) // before the commit is marked
	done()
	rest, done := s.PendingEntries(false, false)
	if len(rest) != 2 {
		t.Fatalf("after the urgent commit: %+v", rest)
	}
	done()
	if again, _ := s.PendingEntries(false, false); len(again) != 0 {
		t.Errorf("still pending: %+v", again)
	}
	if all, _ := s.PendingEntries(true, false); len(all) != 2 {
		t.Errorf("all: %d", len(all))
	}
	// a deleted device: a deletion, urgent
	s.Devices("HmIP-RF", "deleted", []string{"A"})
	del, done := s.PendingEntries(false, true)
	if len(del) != 2 || del[0].Value != nil || s.Count() != 0 {
		t.Errorf("deletions %+v", del)
	}
	done()
	if again, _ := s.PendingEntries(false, false); len(again) != 0 {
		t.Errorf("deletions pending: %+v", again)
	}
}

func TestReadFilterAndPages(t *testing.T) {
	s := &Store{}
	for _, a := range []string{"A:0", "A:1", "B:1", "AB:1"} {
		s.observe("HmIP-RF", a, "STATE", true, at(0), SourceEvent)
		s.observe("HmIP-RF", a, "LOW_BAT", false, at(0), SourceEvent)
	}
	s.observe("BidCos-RF", "C:1", "STATE", true, at(0), SourceEvent)
	if p := s.Read(Filter{Addresses: []string{"A"}}, "", 0); p.Total != 4 {
		t.Errorf("device A: %+v", p)
	}
	if p := s.Read(Filter{Interfaces: []string{"HmIP-RF"}, Datapoints: []string{"STATE"}}, "", 0); p.Total != 4 {
		t.Errorf("HmIP-RF STATE: %+v", p)
	}
	var got []string
	after := ""
	for i := 0; i < 10; i++ {
		p := s.Read(Filter{}, after, 4)
		for _, e := range p.Entries {
			got = append(got, Cursor(e))
		}
		if p.Next == "" {
			break
		}
		after = p.Next
	}
	if len(got) != 9 || got[0] != "BidCos-RF/C:1/STATE" || got[8] != "HmIP-RF/B:1/STATE" {
		t.Errorf("pages %v", got)
	}
}

type fakeSource struct {
	mu       sync.Mutex
	channels map[string][]string
	values   map[string]map[string]any
	reads    []string
	fail     map[string]bool
	onRead   func(channel string)
}

func (f *fakeSource) Channels(_ context.Context, iface string) ([]string, error) {
	if iface == "down" {
		return nil, errors.New("no answer")
	}
	return f.channels[iface], nil
}

func (f *fakeSource) Values(_ context.Context, _ string, channel string) (map[string]any, error) {
	f.mu.Lock()
	f.reads = append(f.reads, channel)
	cb := f.onRead
	f.mu.Unlock()
	if cb != nil {
		cb(channel)
	}
	if f.fail[channel] {
		return nil, errors.New("fault")
	}
	return f.values[channel], nil
}

// The sweep: unconfirmed channels first, then the unknown ones; confirmed ones are not read; a
// channel without anything of the set is not read again; a state message per report that changed
// something; an event newer than the read wins.
func TestSweep(t *testing.T) {
	s := &Store{Now: func() time.Time { return at(1000) }}
	// A:1 restored (unconfirmed), A:2 confirmed by an event, A:3 and A:0 never read
	s.RestoreEntries(map[string]map[string][]byte{"HmIP-RF": {"A:1\x00STATE": encode(&entry{value: false, ts: at(0), lc: at(0)})}})
	s.observe("HmIP-RF", "A:2", "LEVEL", 0.5, at(900), SourceEvent)
	src := &fakeSource{
		channels: map[string][]string{"HmIP-RF": {"A:0", "A:1", "A:2", "A:3", "A:4"}},
		values: map[string]map[string]any{
			"A:0": {"UNREACH": false, "RSSI_DEVICE": -50},
			"A:1": {"STATE": false},
			"A:3": {"INSTALL_TEST": true},
		},
		fail: map[string]bool{"A:4": true},
	}
	var msgs []rpcsub.Message
	s.sweepOne(context.Background(), src, "HmIP-RF", func(m rpcsub.Message) { msgs = append(msgs, m) }, time.Millisecond)
	if fmt.Sprint(src.reads) != "[A:1 A:0 A:3 A:4]" {
		t.Errorf("reads %v", src.reads)
	}
	if len(msgs) != 2 || msgs[0].Type != "state" || msgs[0].Address != "A:1" || *msgs[0].Confirmed != true || msgs[0].Source != SourceSweep || msgs[1].Key != "UNREACH" {
		t.Errorf("messages %+v", msgs)
	}
	if e, _ := s.Get("HmIP-RF", "A:1", "STATE"); !e.Confirmed || !e.LC.Equal(at(0)) {
		t.Errorf("A:1 %+v", e)
	}
	st := s.Sweeps()["HmIP-RF"]
	if st.Channels != 4 || st.Read != 3 || st.Failed != 1 {
		t.Errorf("status %+v", st)
	}
	// again: A:3 had nothing, A:0 and A:1 are confirmed now - only the failed A:4
	src.reads = nil
	s.sweepOne(context.Background(), src, "HmIP-RF", nil, time.Millisecond)
	if fmt.Sprint(src.reads) != "[A:4]" {
		t.Errorf("second sweep read %v", src.reads)
	}
	s.sweepOne(context.Background(), src, "down", nil, time.Millisecond)
	if s.Sweeps()["down"].Error == "" {
		t.Errorf("no error for a down interface")
	}
	// an event that came after the read was sent is the newer report
	s2 := &Store{}
	s2.RestoreEntries(map[string]map[string][]byte{"HmIP-RF": {"A:1\x00STATE": encode(&entry{value: false, ts: at(0), lc: at(0)})}})
	src2 := &fakeSource{channels: map[string][]string{"HmIP-RF": {"A:1"}}, values: map[string]map[string]any{"A:1": {"STATE": false}}}
	now := at(100)
	s2.Now = func() time.Time { return now }
	src2.onRead = func(string) { s2.observe("HmIP-RF", "A:1", "STATE", true, at(101), SourceEvent) }
	s2.sweepOne(context.Background(), src2, "HmIP-RF", nil, time.Millisecond)
	if e, _ := s2.Get("HmIP-RF", "A:1", "STATE"); e.Value != true || e.Source != SourceEvent {
		t.Errorf("the sweep overwrote a newer event: %+v", e)
	}
}

// RunSweeps takes what SweepSoon queued - an interface's up - and the bus handlers queue it.
func TestRunSweepsOnInterfaceUp(t *testing.T) {
	s := &Store{}
	src := &fakeSource{channels: map[string][]string{"HmIP-RF": {"A:1"}}, values: map[string]map[string]any{"A:1": {"STATE": true}}}
	s.Interface("HmIP-RF", "up", time.Now()) // before the loop runs: kept
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.RunSweeps(ctx, src, nil, time.Millisecond) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.Count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Count() != 1 {
		t.Fatal("not swept")
	}
	s.Interface("HmIP-RF", "removed", time.Now())
	if s.Count() != 0 {
		t.Error("removed interface kept its entries")
	}
	cancel()
	<-done
}

// With the database manager: persistent writes the value change at the kick and the
// timestamp-only refresh at the interval; a restart restores both, not confirmed.
func TestWithTheDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DirName, store.FileName)
	s := &Store{}
	m := &store.Manager{Path: path, Platform: "ova", KickDelay: time.Millisecond}
	m.RegisterEntries(s)
	s.Kick = m.Kick
	m.Start("", "")
	_ = m.Flush()
	ctx, cancel := context.WithCancel(context.Background())
	go m.Run(ctx)
	s.observe("HmIP-RF", "A:1", "STATE", true, at(0), SourceEvent)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p, _ := s.PendingEntries(false, false); len(p) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.observe("HmIP-RF", "A:1", "STATE", true, at(50), SourceEvent) // ts only: waits for the interval
	time.Sleep(30 * time.Millisecond)
	if p, _ := s.PendingEntries(false, false); len(p) != 1 {
		t.Errorf("the ts-only refresh was written between intervals: %d pending", len(p))
	}
	if st := m.Status("", ""); st.Kept[Bucket] != 1 {
		t.Errorf("kept %v", st.Kept)
	}
	cancel()
	<-m.Done() // the stop writes it
	s2 := &Store{}
	m2 := &store.Manager{Path: path, Platform: "ova"}
	m2.RegisterEntries(s2)
	m2.Start("", "")
	e, ok := s2.Get("HmIP-RF", "A:1", "STATE")
	if !ok || e.Confirmed || !e.TS.Equal(at(50)) || !e.LC.Equal(at(0)) {
		t.Errorf("after the restart %+v", e)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go m2.Run(ctx2)
	cancel2()
	<-m2.Done()
}

// B-46: what another reader of the interface found on a channel is taken where it differs from
// the store - rfd's acknowledged STICKY_UNREACH comes without an event - and goes on the bus as a
// sweep's report; the same value is left alone, and an event after the read began stays.
func TestReconcile(t *testing.T) {
	var recorded []string
	s := &Store{OnObserve: func(_, _, dp string, _ any, _ time.Time) { recorded = append(recorded, dp) }}
	s.observe("BidCos-RF", "D:0", "STICKY_UNREACH", true, at(0), SourceEvent)
	s.observe("BidCos-RF", "D:0", "UNREACH", false, at(0), SourceEvent)
	recorded = nil
	var pub []rpcsub.Message
	publish := func(m rpcsub.Message) { pub = append(pub, m) }
	n := s.Reconcile("BidCos-RF", "D:0", map[string]any{"STICKY_UNREACH": false, "UNREACH": false, "LOWBAT": false, "RSSI_DEVICE": -60}, at(60), publish)
	if n != 2 || len(pub) != 2 || pub[0].Key != "LOWBAT" || pub[1].Key != "STICKY_UNREACH" {
		t.Fatalf("changed %d, published %+v", n, pub)
	}
	m := pub[1]
	if m.Type != "state" || m.Interface != "BidCos-RF" || m.Address != "D:0" || m.Value != false || m.Source != SourceSweep || m.TS != at(60).Format(time.RFC3339Nano) || m.PreviousForS == nil || *m.PreviousForS != 60 {
		t.Fatalf("the state message: %+v", m)
	}
	if e, _ := s.Get("BidCos-RF", "D:0", "STICKY_UNREACH"); e.Value != false || !e.LC.Equal(at(60)) || e.Source != SourceSweep {
		t.Fatalf("the entry: %+v", e)
	}
	// the value that was right keeps its report, and only what differed reached the history
	if e, _ := s.Get("BidCos-RF", "D:0", "UNREACH"); !e.TS.Equal(at(0)) || e.Source != SourceEvent {
		t.Fatalf("an equal value was touched: %+v", e)
	}
	if fmt.Sprint(recorded) != "[LOWBAT STICKY_UNREACH]" {
		t.Fatalf("observed %v", recorded)
	}
	// the same again: nothing
	if n := s.Reconcile("BidCos-RF", "D:0", map[string]any{"STICKY_UNREACH": false, "LOWBAT": false}, at(120), publish); n != 0 || len(pub) != 2 {
		t.Fatalf("a second pass changed %d", n)
	}
	// an event that came in after the read began is the newer report
	s.observe("BidCos-RF", "D:0", "STICKY_UNREACH", true, at(200), SourceEvent)
	if n := s.Reconcile("BidCos-RF", "D:0", map[string]any{"STICKY_UNREACH": false}, at(190), publish); n != 0 {
		t.Fatal("a read that began before the event overrode it")
	}
	if e, _ := s.Get("BidCos-RF", "D:0", "STICKY_UNREACH"); e.Value != true {
		t.Fatalf("the entry after the late read: %+v", e)
	}
	// an entry restored from the file is confirmed by an equal value too
	r := &Store{}
	r.RestoreEntries(map[string]map[string][]byte{"BidCos-RF": {"D:0\x00LOWBAT": encode(&entry{value: false, ts: at(0), lc: at(0)})}})
	if n := r.Reconcile("BidCos-RF", "D:0", map[string]any{"LOWBAT": false}, at(10), nil); n != 1 {
		t.Fatalf("an unconfirmed entry was not confirmed: %d", n)
	}
	if e, _ := r.Get("BidCos-RF", "D:0", "LOWBAT"); !e.Confirmed {
		t.Fatalf("%+v", e)
	}
}

// occulited task 38 (the maintainer, 2026-10-14): the sweep reads HmIP-RF alone - a BidCos read
// is a status request on air - so BidCos-RF, BidCos-Wired and VirtualDevices coming up or getting
// devices read nothing, and their restored entries stay unconfirmed until an event
func TestSweepHmIPOnly(t *testing.T) {
	s := &Store{}
	s.RestoreEntries(map[string]map[string][]byte{
		"BidCos-RF": {"B:1\x00STATE": encode(&entry{value: true, ts: at(0), lc: at(0)})},
		"HmIP-RF":   {"A:1\x00STATE": encode(&entry{value: false, ts: at(0), lc: at(0)})},
	})
	src := &fakeSource{
		channels: map[string][]string{"HmIP-RF": {"A:1"}, "BidCos-RF": {"B:1", "B:2"}, "BidCos-Wired": {"W:1"}, "VirtualDevices": {"INT0000001:1"}},
		values:   map[string]map[string]any{"A:1": {"STATE": true}, "B:1": {"STATE": false}, "B:2": {"STATE": true}, "W:1": {"STATE": true}, "INT0000001:1": {"LEVEL": 0.5}},
	}
	for _, i := range []string{"BidCos-RF", "BidCos-Wired", "VirtualDevices"} {
		if Swept(i) {
			t.Errorf("%s is swept", i)
		}
		s.Interface(i, "up", time.Now())
		s.Devices(i, "new", []string{"X"})
	}
	if !Swept("HmIP-RF") {
		t.Fatal("HmIP-RF is not swept")
	}
	s.Interface("HmIP-RF", "up", time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.RunSweeps(ctx, src, nil, time.Millisecond) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if e, _ := s.Get("HmIP-RF", "A:1", "STATE"); e.Confirmed || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// a second queued round for the others would come a second later: give it the time
	time.Sleep(1200 * time.Millisecond)
	cancel()
	<-done
	src.mu.Lock()
	reads := fmt.Sprint(src.reads)
	src.mu.Unlock()
	if reads != "[A:1]" {
		t.Errorf("reads %s, want the HmIP channel alone", reads)
	}
	if e, _ := s.Get("BidCos-RF", "B:1", "STATE"); e.Confirmed || e.Source != SourceRestored || e.Value != true {
		t.Errorf("the BidCos entry: %+v", e)
	}
	if _, ok := s.Sweeps()["BidCos-RF"]; ok {
		t.Error("a sweep status for BidCos-RF")
	}
}
