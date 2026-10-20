// Package health samples the radio interfaces (duty cycle, link) over their RPC and keeps their
// history: enough for "was the radio busy at 3 o'clock" on the Interfaces page, and for task 11's
// rule that a firmware update does not start while the duty cycle is high.
//
// The history is a ring of Rows samples per interface (openccu-lite task 214: 500, one N for
// every series of occulited's database - about 8 h at a sample a minute). It lives in memory and,
// unless the storage mode is ram, in occulited's database file (internal/store, bucket "health"),
// so a restart or a deploy of occulited no longer empties the curves: the Sampler is the store's
// Keeper, and the store decides when the new rows are written.
package health

import (
	"context"
	"encoding/binary"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/store"
)

// Sample is one poll of one radio interface.
type Sample struct {
	Time      time.Time `json:"t"`
	DutyCycle int       `json:"dc"`
	// CarrierSense is the level at this poll (task 151), nil when the interface reported none -
	// which is not 0 %
	CarrierSense *int `json:"cs,omitempty"`
	Connected    bool `json:"up"`
}

// Sampler polls every Interval and keeps Rows samples of history per interface address.
type Sampler struct {
	Interfaces func() []interfaces.Interface
	Interval   time.Duration // default 60 s
	Rows       int           // the ring per interface address, default store.DefaultRows (500)
	Timeout    time.Duration // per interface, default 10 s
	// MinRateSpan is the shortest interval an event rate is taken over, default minRateSpan
	// (occulited B-79); a test that polls twice in a row sets it to a nanosecond
	MinRateSpan time.Duration
	// Polled is called after every poll, outside the lock: the store writes the round at once in
	// persistent mode (store.Manager.Kick)
	Polled func()
	// Trusted says whether the clock can be believed (occulited B-75): only then are samples
	// stamped in the future dropped. nil = always.
	Trusted func() bool

	// levels reads the carrier sense from the radio module's device where the interface list has
	// none, and remembers which devices do not have it (task 68)
	levels interfaces.DeviceLevels

	mu        sync.Mutex
	current   []interfaces.RadioInterface
	answering []string // the processes that answered without a radio list
	errs      map[string]string
	polled    time.Time
	history   map[string][]Sample // key: interface + "/" + address
	// per series: how many samples were ever appended, and how many of those the database has -
	// the difference (at most the ring's length) is what the store writes next (task 214)
	appended map[string]uint64
	synced   map[string]uint64
	// replace counts, per series, the prunes the database has not taken yet (B-75): the next
	// write of that series is a whole one
	replace map[string]uint64
	// recent is the module's level as the subscriber last reported it, by interface and the
	// module's channel 0 (task 75); the poll takes it while it is younger than eventFresh
	recent map[string]levelEvent
	// PollSoon's guard: a poll it started is still running, and when it last started one
	soon   bool
	soonAt time.Time

	// occulited task 13: the event rates. Feed is the subscriber's view of every interface
	// process (its telegram counts), read at every poll; the difference to the last poll is a
	// sample of the interface's rate, kept as a ring of Rows like the duty cycle's
	Feed   func() []rpcsub.IfaceStatus
	rates  map[string][]Rate // key: the interface name
	counts map[string]countMark
}

// Rate is one poll's event rate of an interface process (occulited task 13): In the calls with a
// device's events it delivered to occulited's subscriber, per minute (occulited task 17) - a
// system.multicall is one, as it is one radio telegram. Up is false when occulited's subscriber
// was not registered at the process: the line breaks there. The outgoing rate of task 13 is gone
// (task 17): it only ever saw what passed through occulited.
type Rate struct {
	Time time.Time `json:"t"`
	In   float64   `json:"in"`
	Up   bool      `json:"up"`
}

// countMark is an interface's count at the last poll that started a rate's interval, and the
// interface's last registration (rpcsub's LastInit) as that poll saw it (occulited B-79).
type countMark struct {
	in   uint64
	at   time.Time
	init string
	// settled: the mark was taken rateSettle or more after that registration, past the burst
	settled bool
}

// minRateSpan is the shortest interval a rate is taken over (occulited B-79): a poll sooner than
// that after the mark (PollSoon's, 15 s apart at the least) leaves the mark where it is, and the
// next poll's interval covers both. A handful of events over three seconds is not a rate of
// hundreds a minute.
const minRateSpan = 30 * time.Second

// rateSettle is how long after a (re)registration an interface's events are the burst that
// follows its init, not traffic (occulited B-79): the daemon replays values (UNREACH and the
// like) and sends its device lists. An interval that holds a registration, or begins less than
// this after one, gives no rate - its poll only sets the mark.
const rateSettle = 30 * time.Second

// ratePrefix names the rate series in the health bucket beside the duty cycle's
// "<interface>/<address>" series.
const ratePrefix = "rate:"

// pollSoonGap is how often PollSoon may start a poll of its own.
const pollSoonGap = 15 * time.Second

// eventFresh is how long a level the RPC process reported counts as current (task 75): the HmIP
// module reports every 5 minutes, plus on a change, and a getValue in between reads the same
// cached value - so while an event is younger than this the poll makes none.
const eventFresh = 10 * time.Minute

type levelEvent struct {
	carrierSense *int
	dutyCycle    *int
	at           time.Time
}

// Observe takes a datapoint event of a radio module's channel 0 from the subscriber (task
// 75): CARRIER_SENSE_LEVEL and DUTY_CYCLE_LEVEL become a sample with the event's own time, so
// a level the module reported between two polls is in the history with the moment it was
// reported, and the next poll takes the level instead of asking for it. Other keys and other
// channels are ignored.
func (s *Sampler) Observe(iface, channel, key string, value any, at time.Time) {
	if key != "CARRIER_SENSE_LEVEL" && key != "DUTY_CYCLE_LEVEL" {
		return
	}
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case int:
		n = float64(v)
	default:
		return
	}
	pct := int(n + 0.5)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recent == nil {
		s.recent = map[string]levelEvent{}
	}
	k := iface + "/" + channel
	ev := s.recent[k]
	ev.at = at
	if key == "CARRIER_SENSE_LEVEL" {
		ev.carrierSense = &pct
	} else {
		ev.dutyCycle = &pct
	}
	s.recent[k] = ev
	// the sample: the interface entry whose module this is, as the last poll saw it
	for n := range s.current {
		ri := &s.current[n]
		if ri.Interface != iface || interfaces.ModuleChannel(ri.Address) != channel {
			continue
		}
		if ev.carrierSense != nil {
			ri.CarrierSense, ri.CarrierSenseSource = ev.carrierSense, "event"
		}
		if key == "DUTY_CYCLE_LEVEL" {
			ri.DutyCycle = pct
		}
		s.appendLocked(ri.Interface+"/"+ri.Address, Sample{Time: at, DutyCycle: ri.DutyCycle, CarrierSense: ri.CarrierSense, Connected: ri.Connected})
	}
}

// The sampler as a handler of the subscriber's bus (rpcsub.Handler): only the events matter to
// it, and only the module's own levels.
func (s *Sampler) Event(e rpcsub.Event) {
	s.Observe(e.Interface, e.Address, e.Key, e.Value, e.Time)
}

// Interface is the bus's: an interface that came back is polled soon, so its state is current.
func (s *Sampler) Interface(_ string, state string, _ time.Time) {
	if state == "up" || state == "restarted" {
		s.PollSoon()
	}
}

// Devices is the bus's: nothing here depends on the device list.
func (s *Sampler) Devices(string, string, []string) {}

// recentLevel is DeviceLevels.Recent: the event's carrier sense while it is fresh.
func (s *Sampler) recentLevel(iface, channel string) (*int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, ok := s.recent[iface+"/"+channel]
	if !ok || ev.carrierSense == nil || time.Since(ev.at) > eventFresh {
		return nil, false
	}
	return ev.carrierSense, true
}

// PollSoon starts a poll in the background unless one of its own is running or started less than
// pollSoonGap ago, and says whether it did. The API asks for it when an interface process's unit
// became active after the last poll (task 94): hmipserver is ready some 40 s after the web UI at
// boot, and the minute's interval would otherwise keep its start-time error on the pages for up to
// a minute after it answers.
func (s *Sampler) PollSoon() bool {
	s.mu.Lock()
	if s.soon || (!s.soonAt.IsZero() && time.Since(s.soonAt) < pollSoonGap) || s.Interfaces == nil {
		s.mu.Unlock()
		return false
	}
	s.soon, s.soonAt = true, time.Now()
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.soon = false
			s.mu.Unlock()
		}()
		s.Poll(context.Background())
	}()
	return true
}

// Status is what the API returns.
type Status struct {
	Polled     time.Time                   `json:"polled"`
	Interfaces []interfaces.RadioInterface `json:"interfaces"`
	// Answering are the interface processes that answered without a radio list (hs485d, CUxD):
	// up, with nothing to sample
	Answering []string            `json:"answering"`
	Errors    map[string]string   `json:"errors"`
	History   map[string][]Sample `json:"history"`
	// Rates are the telegram rates per interface process (occulited task 13)
	Rates map[string][]Rate `json:"rates"`
}

// Run polls until ctx ends; the first poll happens at once.
func (s *Sampler) Run(ctx context.Context) {
	if s.Interval == 0 {
		s.Interval = time.Minute
	}
	s.Poll(ctx)
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Poll(ctx)
		}
	}
}

// Poll samples once.
func (s *Sampler) Poll(ctx context.Context) {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ifs := s.Interfaces()
	list, answered, errs := interfaces.SampleInterfaces(ctx, ifs, timeout)
	s.levels.Recent = s.recentLevel
	s.levels.Fill(ctx, ifs, list, timeout)
	now := time.Now()
	// the counts are read outside the lock: the subscriber and the proxy take their own
	var feed []rpcsub.IfaceStatus
	if s.Feed != nil {
		feed = s.Feed()
	}
	trusted := s.Trusted == nil || s.Trusted()
	s.mu.Lock()
	if trusted {
		s.pruneFutureLocked(now)
	}
	s.rateLocked(feed, now)
	defer func() {
		s.mu.Unlock()
		if s.Polled != nil {
			s.Polled()
		}
	}()
	s.current, s.answering, s.polled = list, answered, now
	s.errs = map[string]string{}
	for k, e := range errs {
		s.errs[k] = e.Error()
	}
	for _, ri := range list {
		s.appendLocked(ri.Interface+"/"+ri.Address, Sample{Time: now, DutyCycle: ri.DutyCycle, CarrierSense: ri.CarrierSense, Connected: ri.Connected})
		if ri.DutyCycle >= 80 {
			slog.Warn("radio: duty cycle high", "interface", ri.Interface, "address", ri.Address, "duty_cycle", ri.DutyCycle)
		}
	}
}

// rateLocked takes one poll's event rate (occulited task 13): per interface process the count's
// difference to the last poll over the time between, per minute (task 17). The first poll of an
// interface, and one whose count went back (the subscriber dropped the interface and took it up
// again), only set the mark.
//
// occulited B-79: neither is the burst after a registration a rate - an interval that holds a
// (re)registration of the interface, or starts within rateSettle after one, only sets the mark -
// nor is an interval shorter than minRateSpan: that poll leaves the mark in place. At every start
// of occulited the first poll marked an unregistered interface, the registration's PollSoon polled
// again seconds later, and the events the daemon sent after the new init came out as hundreds a
// minute.
func (s *Sampler) rateLocked(feed []rpcsub.IfaceStatus, now time.Time) {
	if s.counts == nil {
		s.counts = map[string]countMark{}
	}
	span := s.MinRateSpan
	if span == 0 {
		span = minRateSpan
	}
	for _, f := range feed {
		in := f.Telegrams
		last, ok := s.counts[f.Name]
		secs := now.Sub(last.at).Seconds()
		up := f.Registered && f.State != "down"
		if ok && in >= last.in && up && f.LastInit == last.init && secs > 0 && secs < span.Seconds() {
			continue // too short an interval: the next poll takes it with this one
		}
		mark := countMark{in: in, at: now, init: f.LastInit, settled: settledAt(f.LastInit, now)}
		s.counts[f.Name] = mark
		if !ok || in < last.in || secs <= 0 {
			continue
		}
		r := Rate{Time: now, Up: up}
		if up {
			if f.LastInit != last.init || !last.settled {
				continue // the interval holds the registration's burst
			}
			r.In = perMinute(in-last.in, secs)
		}
		s.appendRateLocked(f.Name, r)
	}
}

// settledAt says whether a mark taken at now is past the burst of the registration at init
// (rpcsub's LastInit; empty or unreadable: no registration to wait for).
func settledAt(init string, now time.Time) bool {
	at, err := time.Parse(time.RFC3339Nano, init)
	return err != nil || !now.Before(at.Add(rateSettle))
}

// perMinute is n over secs, per minute, to a thousandth.
func perMinute(n uint64, secs float64) float64 {
	return math.Round(float64(n)/secs*60*1000) / 1000
}

// appendRateLocked adds a rate sample and drops the oldest past the ring's length.
func (s *Sampler) appendRateLocked(name string, r Rate) {
	s.initLocked()
	h := append(s.rates[name], r)
	if n := s.rows(); len(h) > n {
		h = append([]Rate(nil), h[len(h)-n:]...)
	}
	s.rates[name] = h
	s.appended[ratePrefix+name]++
}

// pruneFutureLocked drops the samples stamped after now from every series (occulited B-75) and has
// the next write replace those series in the database, where they would outlive every real row:
// the rows are keyed by time and the ring keeps the newest.
func (s *Sampler) pruneFutureLocked(now time.Time) {
	s.initLocked()
	dropped := 0
	for key, h := range s.history {
		if kept := dropFuture(h, func(sm Sample) time.Time { return sm.Time }, now); len(kept) != len(h) {
			dropped += len(h) - len(kept)
			s.history[key] = kept
			s.replace[key]++
		}
	}
	for name, h := range s.rates {
		if kept := dropFuture(h, func(r Rate) time.Time { return r.Time }, now); len(kept) != len(h) {
			dropped += len(h) - len(kept)
			s.rates[name] = kept
			s.replace[ratePrefix+name]++
		}
	}
	if dropped > 0 {
		slog.Info("radio history: samples stamped after the current time dropped", "dropped", dropped)
	}
}

// dropFuture is a series without the samples stamped after now (store.Future).
func dropFuture[T any](h []T, at func(T) time.Time, now time.Time) []T {
	kept := h[:0:0]
	for _, v := range h {
		if !store.Future(at(v), now) {
			kept = append(kept, v)
		}
	}
	return kept
}

// initLocked makes the maps a first append or restore needs.
func (s *Sampler) initLocked() {
	if s.history == nil {
		s.history = map[string][]Sample{}
	}
	if s.rates == nil {
		s.rates = map[string][]Rate{}
	}
	if s.appended == nil {
		s.appended = map[string]uint64{}
		s.synced = map[string]uint64{}
	}
	if s.replace == nil {
		s.replace = map[string]uint64{}
	}
}

// Status returns the current state and history (copies).
func (s *Sampler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Polled: s.polled, Interfaces: append([]interfaces.RadioInterface{}, s.current...), Answering: append([]string{}, s.answering...), Errors: map[string]string{}, History: map[string][]Sample{}, Rates: map[string][]Rate{}}
	for k, v := range s.errs {
		st.Errors[k] = v
	}
	for k, v := range s.history {
		st.History[k] = append([]Sample{}, v...)
	}
	for k, v := range s.rates {
		st.Rates[k] = append([]Rate{}, v...)
	}
	if st.Interfaces == nil {
		st.Interfaces = []interfaces.RadioInterface{}
	}
	return st
}

// Busy reports whether any interface is above the threshold (for "do not start a firmware
// update now"). Unknown (no poll yet, or -1) counts as not busy.
func (s *Sampler) Busy(threshold int) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ri := range s.current {
		if ri.DutyCycle >= threshold {
			return true, ri.Interface + " " + ri.Address
		}
	}
	return false, ""
}

func (s *Sampler) rows() int {
	if s.Rows > 0 {
		return s.Rows
	}
	return store.DefaultRows
}

// appendLocked adds a sample to a series and drops the oldest past the ring's length.
func (s *Sampler) appendLocked(key string, sm Sample) {
	s.initLocked()
	h := append(s.history[key], sm)
	if n := s.rows(); len(h) > n {
		h = append([]Sample(nil), h[len(h)-n:]...)
	}
	s.history[key] = h
	s.appended[key]++
}

// The sampler is the store's Keeper for the health history (task 214).

// Bucket is the health history's bucket in occulited's database.
func (s *Sampler) Bucket() string { return "health" }

// Restore takes the rows the database holds: each series is the file's rows older than the
// oldest sample in memory, then memory's, cut to the ring. At a start memory is empty and the
// file is the history; after a switch out of ram both are kept.
func (s *Sampler) Restore(series map[string][]store.Row) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()
	for key, rows := range series {
		var fromFile, inMem int
		if name, ok := strings.CutPrefix(key, ratePrefix); ok {
			mem := s.rates[name]
			kept, spikes := dropBurstRows(rows)
			if spikes > 0 {
				// the file still has them: the next write of the series is a whole one
				s.replace[key]++
				slog.Info("radio history: registration bursts dropped from the event rates", "interface", name, "dropped", spikes)
			}
			merged := restoreRing(kept, mem, decodeRate, func(r Rate) time.Time { return r.Time }, s.rows())
			s.rates[name] = merged
			fromFile, inMem = len(merged)-len(mem), len(mem)
		} else {
			mem := s.history[key]
			merged := restoreRing(rows, mem, decodeSample, func(sm Sample) time.Time { return sm.Time }, s.rows())
			s.history[key] = merged
			fromFile, inMem = len(merged)-len(mem), len(mem)
		}
		// what came from the file is in it; memory's own rows are written by the whole write
		// that follows an open
		s.appended[key] += uint64(fromFile)
		s.synced[key] = s.appended[key] - uint64(inMem)
	}
}

// restoreRing is a series after an open: the file's rows older than memory's oldest, then
// memory's, cut to the ring.
func restoreRing[T any](rows []store.Row, mem []T, decode func(store.Row) (T, bool), at func(T) time.Time, n int) []T {
	var merged []T
	for _, r := range rows {
		v, ok := decode(r)
		if !ok || (len(mem) > 0 && !at(v).Before(at(mem[0]))) {
			continue
		}
		merged = append(merged, v)
	}
	sort.SliceStable(merged, func(i, j int) bool { return at(merged[i]).Before(at(merged[j])) })
	merged = append(merged, mem...)
	if len(merged) > n {
		merged = merged[len(merged)-n:]
	}
	return merged
}

// Pending returns the samples the database does not have yet, per series, or all of them (as
// Replace series) after an open. The returned function marks them written.
func (s *Sampler) Pending(all bool) ([]store.Series, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Series
	upTo := map[string]uint64{}
	pruned := map[string]uint64{}
	// the rows of a series the file lacks: all of them after an open or a prune (B-75), else the
	// ones appended since the last write (at most the ring)
	full := func(key string) bool { return all || s.replace[key] > 0 }
	pending := func(key string, total int) int {
		n := total
		if !full(key) {
			if d := s.appended[key] - s.synced[key]; d < uint64(n) {
				n = int(d)
			}
		}
		return n
	}
	add := func(key string, rows []store.Row) {
		out = append(out, store.Series{Bucket: s.Bucket(), Name: key, Rows: rows, Replace: full(key)})
		upTo[key] = s.appended[key]
		pruned[key] = s.replace[key]
	}
	for key, h := range s.history {
		n := pending(key, len(h))
		if n == 0 && !full(key) {
			continue
		}
		rows := make([]store.Row, 0, n)
		for _, sm := range h[len(h)-n:] {
			rows = append(rows, encodeSample(sm))
		}
		add(key, rows)
	}
	for name, h := range s.rates {
		key := ratePrefix + name
		n := pending(key, len(h))
		if n == 0 && !full(key) {
			continue
		}
		rows := make([]store.Row, 0, n)
		for _, r := range h[len(h)-n:] {
			rows = append(rows, encodeRate(r))
		}
		add(key, rows)
	}
	return out, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for k, v := range upTo {
			s.synced[k] = v
		}
		for k, v := range pruned {
			s.replace[k] -= min(s.replace[k], v)
		}
	}
}

// A sample in the database: the time is the key; the value is a version byte, a flags byte
// (1 = up, 2 = a carrier sense is known) and the duty cycle and the carrier sense as two
// big-endian int16.
const sampleV1 = 1

func encodeSample(sm Sample) store.Row {
	v := make([]byte, 6)
	v[0] = sampleV1
	if sm.Connected {
		v[1] |= 1
	}
	binary.BigEndian.PutUint16(v[2:], uint16(int16(sm.DutyCycle)))
	if sm.CarrierSense != nil {
		v[1] |= 2
		binary.BigEndian.PutUint16(v[4:], uint16(int16(*sm.CarrierSense)))
	}
	return store.Row{At: sm.Time, Value: v}
}

func decodeSample(r store.Row) (Sample, bool) {
	v := r.Value
	if len(v) < 6 || v[0] != sampleV1 {
		return Sample{}, false
	}
	sm := Sample{Time: r.At, Connected: v[1]&1 != 0, DutyCycle: int(int16(binary.BigEndian.Uint16(v[2:])))}
	if v[1]&2 != 0 {
		cs := int(int16(binary.BigEndian.Uint16(v[4:])))
		sm.CarrierSense = &cs
	}
	return sm, true
}

// A rate sample in the database (occulited task 13): a version byte of its own - an occulited from
// before the rates skips the rows instead of reading them as duty cycles - a flags byte (1 = up,
// 2 = rateChecked, B-79) and the rate as a big-endian uint32 in thousandths. Version 0x82 (task 17)
// holds the events per minute alone; 0x81 (task 13's hot builds) held per second in and out, and is read as in × 60.
const (
	rateV1 = 0x81
	rateV2 = 0x82
)

// rateChecked is the flag (2) of a row written since occulited B-79: its rate was taken past the
// burst of a registration and over an interval of minRateSpan at least, or it was read back and
// kept by dropBurstRows. An occulited from before reads the flags' first bit only.
const rateChecked = 2

// A row without rateChecked is a registration burst (occulited B-79) when it is a lone spike: up,
// at least burstMin a minute, and more than burstFactor times the higher of its neighbours in the
// series. The production read had about 2 a minute either side of 500.
const (
	burstMin    = 60
	burstFactor = 10
)

// dropBurstRows is a rate series from the database without the registration bursts an occulited
// from before B-79 wrote into it, and how many it dropped. The rows are taken in time order; rows
// it cannot read stay for restoreRing to skip.
func dropBurstRows(rows []store.Row) ([]store.Row, int) {
	type row struct {
		r       Rate
		checked bool
	}
	var rates []row
	var idx []int // rates[k] is rows[idx[k]]
	for i, r := range rows {
		if v, ok := decodeRate(r); ok {
			rates = append(rates, row{v, len(r.Value) > 1 && r.Value[1]&rateChecked != 0})
			idx = append(idx, i)
		}
	}
	order := make([]int, len(rates))
	for k := range order {
		order[k] = k
	}
	sort.SliceStable(order, func(a, b int) bool { return rates[order[a]].r.Time.Before(rates[order[b]].r.Time) })
	drop := map[int]bool{}
	for p, k := range order {
		c := rates[k]
		if c.checked || !c.r.Up || c.r.In < burstMin {
			continue
		}
		var near []float64
		if p > 0 {
			near = append(near, rates[order[p-1]].r.In)
		}
		if p < len(order)-1 {
			near = append(near, rates[order[p+1]].r.In)
		}
		if len(near) == 0 {
			continue
		}
		top := near[0]
		for _, v := range near[1:] {
			top = math.Max(top, v)
		}
		if c.r.In > burstFactor*top {
			drop[idx[k]] = true
		}
	}
	if len(drop) == 0 {
		return rows, 0
	}
	kept := make([]store.Row, 0, len(rows)-len(drop))
	for i, r := range rows {
		if !drop[i] {
			kept = append(kept, r)
		}
	}
	return kept, len(drop)
}

func encodeRate(r Rate) store.Row {
	v := make([]byte, 6)
	v[0] = rateV2
	v[1] = rateChecked
	if r.Up {
		v[1] |= 1
	}
	binary.BigEndian.PutUint32(v[2:], milli(r.In))
	return store.Row{At: r.Time, Value: v}
}

func decodeRate(row store.Row) (Rate, bool) {
	v := row.Value
	switch {
	case len(v) >= 6 && v[0] == rateV2:
		return Rate{Time: row.At, Up: v[1]&1 != 0, In: float64(binary.BigEndian.Uint32(v[2:])) / 1000}, true
	case len(v) >= 10 && v[0] == rateV1:
		perSec := float64(binary.BigEndian.Uint32(v[2:])) / 1000
		return Rate{Time: row.At, Up: v[1]&1 != 0, In: math.Round(perSec*60*1000) / 1000}, true
	}
	return Rate{}, false
}

// milli is a rate in thousandths, held to what a uint32 takes.
func milli(f float64) uint32 {
	switch m := math.Round(f * 1000); {
	case m <= 0:
		return 0
	case m >= math.MaxUint32:
		return math.MaxUint32
	default:
		return uint32(m)
	}
}
