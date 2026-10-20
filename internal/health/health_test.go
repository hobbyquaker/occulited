package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/store"
)

// task 94: the sampler asks for the interface list at every poll, so an InterfacesList.xml that
// occu-init-hs485d rewrites after occulited started - an interface added, one gone - is what the
// next poll samples, not the list the daemon saw at its own start.
func TestSamplerReadsTheListAtEveryPoll(t *testing.T) {
	// a port nobody listens on: every call is refused at once and lands in the errors
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()

	list := []struct{ Name, URL string }{{"BidCos-RF", "xmlrpc_bin://" + closed}}
	asked := 0
	s := &Sampler{Timeout: 2 * time.Second, Interfaces: func() []interfaces.Interface {
		asked++
		return interfaces.FromList(list)
	}}
	s.Poll(context.Background())
	if st := s.Status(); len(st.Errors) != 1 || st.Errors["BidCos-RF"] == "" {
		t.Fatalf("first poll: %+v", st.Errors)
	}

	list = append(list, struct{ Name, URL string }{"HmIP-RF", "xmlrpc://" + closed})
	s.Poll(context.Background())
	st := s.Status()
	if asked != 2 {
		t.Errorf("the list was asked for %d times in two polls", asked)
	}
	if len(st.Errors) != 2 || st.Errors["HmIP-RF"] == "" {
		t.Errorf("second poll: %+v", st.Errors)
	}

	list = list[1:]
	s.Poll(context.Background())
	if st := s.Status(); len(st.Errors) != 1 || st.Errors["BidCos-RF"] != "" {
		t.Errorf("an interface that left the list is not sampled: %+v", st.Errors)
	}
}

// task 94: PollSoon polls once in the background, and not again while that runs or within the gap
func TestPollSoon(t *testing.T) {
	if (&Sampler{}).PollSoon() {
		t.Error("a sampler without an interface list polled")
	}
	polls := make(chan struct{}, 8)
	s := &Sampler{Timeout: time.Second, Interfaces: func() []interfaces.Interface {
		polls <- struct{}{}
		return nil
	}}
	idle := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			busy := s.soon
			s.mu.Unlock()
			if !busy {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("the background poll did not end")
	}
	if !s.PollSoon() {
		t.Fatal("the first PollSoon did not poll")
	}
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("no poll")
	}
	idle()
	if s.Status().Polled.IsZero() {
		t.Error("the background poll recorded nothing")
	}
	if s.PollSoon() {
		t.Error("a second PollSoon within the gap polled again")
	}
	// the gap is over: once more
	s.mu.Lock()
	s.soonAt = time.Now().Add(-pollSoonGap)
	s.mu.Unlock()
	if !s.PollSoon() {
		t.Error("PollSoon after the gap did not poll")
	}
	idle()
	if len(polls) != 1 {
		t.Errorf("%d more polls, want 1", len(polls))
	}
}

// task 151: a sample carries the carrier sense when the interface reported one - 0 % included -
// and leaves the field out when it reported none, which the Status page draws as a gap
func TestSampleCarrierSense(t *testing.T) {
	zero, four := 0, 4
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		cs   *int
		want string
	}{
		{nil, `{"t":"2026-09-18T12:00:00Z","dc":3,"up":true}`},
		{&zero, `{"t":"2026-09-18T12:00:00Z","dc":3,"cs":0,"up":true}`},
		{&four, `{"t":"2026-09-18T12:00:00Z","dc":3,"cs":4,"up":true}`},
	} {
		b, err := json.Marshal(Sample{Time: at, DutyCycle: 3, CarrierSense: c.cs, Connected: true})
		if err != nil || string(b) != c.want {
			t.Errorf("%s, want %s (%v)", b, c.want, err)
		}
	}
}

// hs485d answers listBidcosInterfaces with a fault: the Status page shows it up, not "not
// answering" - it is in answering, not in the errors.
func TestSamplerAFaultIsAnswering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><i4>-1</i4></value></member><member><name>faultString</name><value>listBidcosInterfaces: unknown method name</value></member></struct></value></fault></methodResponse>`))
	}))
	defer srv.Close()
	s := &Sampler{Timeout: 2 * time.Second, Interfaces: func() []interfaces.Interface {
		return interfaces.FromList([]struct{ Name, URL string }{{"BidCos-Wired", "xmlrpc://" + strings.TrimPrefix(srv.URL, "http://")}})
	}}
	s.Poll(t.Context())
	st := s.Status()
	if len(st.Errors) != 0 || len(st.Answering) != 1 || st.Answering[0] != "BidCos-Wired" || len(st.Interfaces) != 0 {
		t.Fatalf("%+v", st)
	}
	b, _ := json.Marshal(st)
	if !strings.Contains(string(b), `"answering":["BidCos-Wired"]`) {
		t.Errorf("json: %s", b)
	}
}

// task 214: the ring is Rows samples per series, and the sampler is the store's keeper - what it
// wrote comes back after a restart in order, with the carrier sense and the link.
func TestHistorySurvivesARestartThroughTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DirName, store.FileName)
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	cs := 7
	s := &Sampler{Rows: 4}
	m := &store.Manager{Path: path, Platform: "ova", Rows: 4}
	m.Register(s)
	m.Start("", "")
	s.mu.Lock()
	for i := 0; i < 6; i++ {
		sm := Sample{Time: t0.Add(time.Duration(i) * time.Minute), DutyCycle: i, Connected: i != 2}
		if i == 5 {
			sm.CarrierSense = &cs
		}
		s.appendLocked("HmIP-RF/3014F711A0", sm)
	}
	s.mu.Unlock()
	if h := s.Status().History["HmIP-RF/3014F711A0"]; len(h) != 4 || h[0].DutyCycle != 2 {
		t.Fatalf("the ring in memory: %+v", h)
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	// nothing new: nothing pending
	if series, _ := s.Pending(false); len(series) != 0 {
		t.Errorf("pending after a write: %+v", series)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go m.Run(ctx)
	cancel()
	<-m.Done()

	s2 := &Sampler{Rows: 4}
	m2 := &store.Manager{Path: path, Platform: "ova", Rows: 4}
	m2.Register(s2)
	m2.Start("", "")
	h := s2.Status().History["HmIP-RF/3014F711A0"]
	if len(h) != 4 {
		t.Fatalf("restored %d samples", len(h))
	}
	for i, sm := range h {
		if !sm.Time.Equal(t0.Add(time.Duration(i+2)*time.Minute)) || sm.DutyCycle != i+2 || sm.Connected != (i+2 != 2) {
			t.Errorf("sample %d: %+v", i, sm)
		}
	}
	if h[3].CarrierSense == nil || *h[3].CarrierSense != 7 || h[2].CarrierSense != nil {
		t.Errorf("the carrier sense did not come back: %v %v", h[3].CarrierSense, h[2].CarrierSense)
	}
	// a restored history is written, nothing is pending but the whole write after the open
	if series, _ := s2.Pending(false); len(series) != 0 {
		t.Errorf("restored rows are pending again: %+v", series)
	}
	// a new sample: pending is exactly that one
	s2.mu.Lock()
	s2.appendLocked("HmIP-RF/3014F711A0", Sample{Time: t0.Add(10 * time.Minute), DutyCycle: 9, Connected: true})
	s2.mu.Unlock()
	series, done := s2.Pending(false)
	if len(series) != 1 || len(series[0].Rows) != 1 || series[0].Replace {
		t.Fatalf("pending: %+v", series)
	}
	done()
	if series, _ := s2.Pending(false); len(series) != 0 {
		t.Errorf("pending after done: %+v", series)
	}
	if all, _ := s2.Pending(true); len(all) != 1 || len(all[0].Rows) != 4 || !all[0].Replace {
		t.Errorf("a whole write: %+v", all)
	}
	_ = m2.Flush()
}

// After a switch out of ram, memory's samples stay and the file's older ones come in front.
func TestRestoreMergesWithMemory(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s := &Sampler{Rows: 5}
	s.mu.Lock()
	s.appendLocked("a", Sample{Time: t0.Add(3 * time.Minute), DutyCycle: 3})
	s.appendLocked("a", Sample{Time: t0.Add(4 * time.Minute), DutyCycle: 4})
	s.mu.Unlock()
	var file []store.Row
	for i := 0; i < 5; i++ { // 3 and 4 overlap memory and are left out
		file = append(file, encodeSample(Sample{Time: t0.Add(time.Duration(i) * time.Minute), DutyCycle: i}))
	}
	file = append(file, store.Row{At: t0, Value: []byte{9}}) // an unknown encoding is skipped
	s.Restore(map[string][]store.Row{"a": file})
	h := s.Status().History["a"]
	var got []int
	for _, sm := range h {
		got = append(got, sm.DutyCycle)
	}
	if fmt.Sprint(got) != "[0 1 2 3 4]" {
		t.Errorf("merged %v", got)
	}
	// memory's two are pending, the file's three are not
	if series, _ := s.Pending(false); len(series) != 1 || len(series[0].Rows) != 2 {
		t.Errorf("pending %+v", series)
	}
}

// occulited task 13: a poll's event rate is the count's difference to the last poll over the
// time between, per minute (task 17); the first poll only marks, a count that went back marks again, an interface whose
// registration is gone is a gap; the rings go into the database beside the duty cycle's and come
// back from it, and an old occulited's decoder skips their rows.
func TestRates(t *testing.T) {
	t0 := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s := &Sampler{Rows: 3}
	feed := func(in uint64, registered bool) []rpcsub.IfaceStatus {
		state := "up"
		if !registered {
			state = "down"
		}
		return []rpcsub.IfaceStatus{{Name: "HmIP-RF", Telegrams: in, Registered: registered, State: state}}
	}
	poll := func(at time.Time, in uint64, registered bool) {
		s.mu.Lock()
		s.rateLocked(feed(in, registered), at)
		s.mu.Unlock()
	}
	poll(t0, 100, true)
	if r := s.Status().Rates; len(r["HmIP-RF"]) != 0 {
		t.Fatalf("the first poll is a mark only: %+v", r)
	}
	poll(t0.Add(time.Minute), 130, true)                // 30 telegrams in 60 s
	poll(t0.Add(2*time.Minute), 130, false)             // the subscriber lost its registration
	poll(t0.Add(3*time.Minute), 5, true)                // the interface came back with fresh counts: a mark
	poll(t0.Add(4*time.Minute), 6, true)                // one in a minute
	poll(t0.Add(4*time.Minute+30*time.Second), 9, true) // three in half a minute
	r := s.Status().Rates["HmIP-RF"]
	if len(r) != 3 { // the ring of 3: the first of the four samples is gone
		t.Fatalf("ring %+v", r)
	}
	if r[0].Up || r[0].In != 0 || !r[1].Up || r[1].In != 1 || !r[2].Up || r[2].In != 6 {
		t.Errorf("rates %+v", r)
	}
	if got := perMinute(1, 7); got != 8.571 {
		t.Errorf("1 in 7 s: %v", got)
	}

	// the database: the series rate:<interface>, its own row version
	all, done := s.Pending(true)
	if len(all) != 1 || all[0].Name != "rate:HmIP-RF" || all[0].Bucket != "health" || len(all[0].Rows) != 3 || !all[0].Replace {
		t.Fatalf("pending %+v", all)
	}
	done()
	if _, ok := decodeSample(all[0].Rows[1]); ok {
		t.Error("a rate row reads as a duty cycle sample")
	}
	s2 := &Sampler{Rows: 3}
	s2.Restore(map[string][]store.Row{"rate:HmIP-RF": append(all[0].Rows, store.Row{At: t0, Value: []byte{rateV2, 1}})})
	back := s2.Status().Rates["HmIP-RF"]
	if fmt.Sprint(back) != fmt.Sprint(r) {
		t.Errorf("restored %+v, want %+v", back, r)
	}
	if series, _ := s2.Pending(false); len(series) != 0 {
		t.Errorf("restored rows pending again: %+v", series)
	}
	// task 13's rows (0x81: in and out per second) read as in per minute, out dropped
	old := []byte{rateV1, 1, 0, 0, 0x01, 0xf4, 0, 0, 0, 7} // in 0.5/s
	if got, ok := decodeRate(store.Row{At: t0, Value: old}); !ok || !got.Up || got.In != 30 {
		t.Errorf("an 0x81 row: %+v %v", got, ok)
	}
	if v := all[0].Rows[1].Value; len(v) != 6 || v[0] != rateV2 {
		t.Errorf("a new row %x", v)
	}
	// a rate beyond what a uint32 of thousandths takes is held to it
	if milli(5e6) != 4294967295 || milli(-1) != 0 {
		t.Errorf("milli %d %d", milli(5e6), milli(-1))
	}
}

// occulited B-79: the events an interface sends after occulited's (re)registration - its replay,
// its device lists - are no rate, and neither is a poll seconds after the last. At a start the
// first poll marked an unregistered interface, the registration's PollSoon came three seconds
// later, and 25 events in them made 500 a minute.
func TestRatesSkipTheRegistrationBurst(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 15, 50, 6, 0, time.UTC)
	s := &Sampler{Rows: 20}
	init1 := t0.Add(3 * time.Second).Format(time.RFC3339Nano)
	poll := func(at time.Duration, in uint64, init string) {
		f := rpcsub.IfaceStatus{Name: "BidCos-RF", Telegrams: in, Registered: init != "", State: "up", LastInit: init}
		if init == "" {
			f.State = ""
		}
		s.mu.Lock()
		s.rateLocked([]rpcsub.IfaceStatus{f}, t0.Add(at))
		s.mu.Unlock()
	}
	rates := func() []Rate { return s.Status().Rates["BidCos-RF"] }

	poll(0, 0, "")                   // occulited's first poll: not registered yet, a mark
	poll(3*time.Second, 25, init1)   // the registration's PollSoon: the burst is in the count
	poll(4*time.Second, 27, init1)   // and the API's: too soon
	poll(63*time.Second, 30, init1)  // a minute on, but the mark is in the burst's window
	poll(123*time.Second, 32, init1) // the first rate: 2 in a minute
	poll(133*time.Second, 40, init1) // ten seconds on: the mark stays
	poll(183*time.Second, 42, init1) // 10 in the minute since the mark
	init2 := t0.Add(200 * time.Second).Format(time.RFC3339Nano)
	poll(243*time.Second, 70, init2) // re-registered in between (hmipserver's reinit): no rate
	poll(303*time.Second, 71, init2) // the mark was 43 s past the init: settled, 1 a minute
	r := rates()
	var got []float64
	for _, x := range r {
		if !x.Up {
			t.Errorf("a gap: %+v", x)
		}
		got = append(got, x.In)
	}
	if fmt.Sprint(got) != "[2 10 1]" {
		t.Errorf("rates %v, want [2 10 1] (%+v)", got, r)
	}
	for _, x := range r {
		if x.In > 60 {
			t.Errorf("a spike: %+v", x)
		}
	}

	// the registration gone: a gap at once, whatever the interval
	poll(310*time.Second, 71, "")
	if r := rates(); r[len(r)-1].Up {
		t.Errorf("no gap after the registration was lost: %+v", r[len(r)-1])
	}
	if !settledAt("", t0) || !settledAt("bad", t0) || settledAt(init1, t0.Add(32*time.Second)) || !settledAt(init1, t0.Add(33*time.Second)) {
		t.Error("settledAt")
	}
}

// occulited B-79: the bursts an occulited from before the fix wrote are dropped when the series is
// read back - a lone spike in an unchecked row - and the series is written whole next; a checked
// row, a spike beside busy neighbours and a quiet row stay.
func TestRestoreDropsOldBurstRows(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 15, 45, 0, 0, time.UTC)
	old := func(i int, in float64) store.Row {
		r := encodeRate(Rate{Time: t0.Add(time.Duration(i) * time.Minute), In: in, Up: true})
		r.Value[1] &^= rateChecked // as an occulited before B-79 wrote it
		return r
	}
	rows := []store.Row{old(0, 2), old(1, 3), old(5, 2), old(2, 500), old(3, 2), old(4, 20), old(6, 150), old(7, 30)}
	checked := encodeRate(Rate{Time: t0.Add(8 * time.Minute), In: 900, Up: true})
	rows = append(rows, checked, old(9, 1))
	kept, n := dropBurstRows(rows)
	if n != 1 || len(kept) != len(rows)-1 {
		t.Fatalf("dropped %d, kept %d", n, len(kept))
	}
	s := &Sampler{Rows: 20}
	s.Restore(map[string][]store.Row{"rate:BidCos-RF": rows})
	var got []float64
	for _, x := range s.Status().Rates["BidCos-RF"] {
		got = append(got, x.In)
	}
	if fmt.Sprint(got) != "[2 3 2 20 2 150 30 900 1]" {
		t.Errorf("restored %v", got)
	}
	series, done := s.Pending(false)
	if len(series) != 1 || !series[0].Replace || len(series[0].Rows) != 9 {
		t.Fatalf("pending %+v", series)
	}
	done()
	for _, r := range series[0].Rows {
		if r.Value[1]&rateChecked == 0 {
			t.Errorf("a rewritten row without the flag: %x", r.Value)
		}
	}
	// nothing to drop: the rows as they are, nothing to rewrite
	s2 := &Sampler{Rows: 20}
	s2.Restore(map[string][]store.Row{"rate:BidCos-RF": {old(0, 2), old(1, 3)}})
	if series, _ := s2.Pending(false); len(series) != 0 {
		t.Errorf("pending without a burst: %+v", series)
	}
	if kept, n := dropBurstRows([]store.Row{old(0, 500)}); n != 0 || len(kept) != 1 {
		t.Error("a lone row has no neighbours to be a spike against")
	}
}

// occulited B-75: samples stamped in the future - a boot with the clock in 2067 - are dropped once
// the clock is trusted, from memory and from the database, where the ring's trim (newest keys
// first) would otherwise keep them for good and push the real rows out.
func TestFutureSamplesAreDroppedAndReplacedInTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DirName, store.FileName)
	now := time.Date(2026, 10, 15, 21, 40, 0, 0, time.UTC)
	wrong := time.Date(2067, 7, 4, 18, 0, 0, 0, time.UTC)
	s := &Sampler{Rows: 10}
	m := &store.Manager{Path: path, Platform: "ova", Rows: 10}
	m.Register(s)
	m.Start("", "")
	s.mu.Lock()
	for i, at := range []time.Time{now.Add(-3 * time.Minute), now.Add(-2 * time.Minute), wrong, wrong.Add(time.Minute), now.Add(-time.Minute)} {
		s.appendLocked("HmIP-RF/3014F711A0", Sample{Time: at, DutyCycle: i, Connected: true})
		s.appendRateLocked("HmIP-RF", Rate{Time: at, In: float64(i), Up: true})
	}
	s.mu.Unlock()
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go m.Run(ctx)
	cancel()
	<-m.Done()

	// the restart restores them - sorted, the 2067 rows last - and the trusted clock drops them
	s2 := &Sampler{Rows: 10}
	m2 := &store.Manager{Path: path, Platform: "ova", Rows: 10}
	m2.Register(s2)
	m2.Start("", "")
	if h := s2.Status().History["HmIP-RF/3014F711A0"]; len(h) != 5 || !h[4].Time.Equal(wrong.Add(time.Minute)) {
		t.Fatalf("restored: %+v", h)
	}
	s2.mu.Lock()
	s2.pruneFutureLocked(now)
	s2.mu.Unlock()
	if h := s2.Status().History["HmIP-RF/3014F711A0"]; len(h) != 3 {
		t.Fatalf("after the prune: %+v", h)
	}
	// the next write replaces both series as a whole
	series, _ := s2.Pending(false)
	if len(series) != 2 {
		t.Fatalf("pending after the prune: %+v", series)
	}
	for _, sr := range series {
		if !sr.Replace || len(sr.Rows) != 3 {
			t.Errorf("%s: replace %v, %d rows", sr.Name, sr.Replace, len(sr.Rows))
		}
	}
	if err := m2.Flush(); err != nil {
		t.Fatal(err)
	}
	if series, _ := s2.Pending(false); len(series) != 0 {
		t.Errorf("pending after the write: %+v", series)
	}
	s2.mu.Lock()
	s2.pruneFutureLocked(now) // nothing left to drop: nothing to write
	s2.mu.Unlock()
	if series, _ := s2.Pending(false); len(series) != 0 {
		t.Errorf("a prune that dropped nothing is pending: %+v", series)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go m2.Run(ctx2)
	cancel2()
	<-m2.Done()

	s3 := &Sampler{Rows: 10}
	m3 := &store.Manager{Path: path, Platform: "ova", Rows: 10}
	m3.Register(s3)
	m3.Start("", "")
	st := s3.Status()
	for _, sm := range st.History["HmIP-RF/3014F711A0"] {
		if sm.Time.Year() == 2067 {
			t.Errorf("a 2067 sample came back from the file: %+v", sm)
		}
	}
	if n := len(st.History["HmIP-RF/3014F711A0"]); n != 3 {
		t.Errorf("%d samples in the file, want 3", n)
	}
	if n := len(st.Rates["HmIP-RF"]); n != 3 {
		t.Errorf("%d rate samples in the file, want 3", n)
	}
	_ = m3.Flush()
}

// B-75: the predicate - the slack covers a clock chrony steps back by a little
func TestFuture(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{now.Add(-time.Hour), false},
		{now, false},
		{now.Add(store.FutureSlack), false},
		{now.Add(store.FutureSlack + time.Second), true},
		{time.Date(2067, 7, 4, 18, 0, 0, 0, time.UTC), true},
	} {
		if got := store.Future(c.at, now); got != c.want {
			t.Errorf("Future(%s) = %v", c.at, got)
		}
	}
}

// B-75: the poll drops them only while the clock is trusted - an untrusted clock may itself be the
// one that is behind, and the samples it called future are then the real ones
func TestPollPrunesOnlyWithATrustedClock(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for _, trusted := range []bool{false, true} {
		s := &Sampler{Rows: 10, Interfaces: func() []interfaces.Interface { return nil }, Trusted: func() bool { return trusted }}
		s.mu.Lock()
		s.appendLocked("a", Sample{Time: future, DutyCycle: 1})
		s.appendRateLocked("r", Rate{Time: future, In: 1})
		s.mu.Unlock()
		s.Poll(context.Background())
		st := s.Status()
		if kept := len(st.History["a"]) + len(st.Rates["r"]); (kept == 2) != !trusted {
			t.Errorf("trusted %v: %d future samples kept", trusted, kept)
		}
	}
}
