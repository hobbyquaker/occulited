// Package devstate is the state store (openccu-lite task 194): the last value of every datapoint
// in the chosen set (keys.go), with when it was last reported (ts), when it last became a
// different value (lc) and whether that change was seen as it happened (changed_at, task 95), how
// long the value before it stood (previous_for_s), and whether it has
// been confirmed since occulited started (confirmed). It is what lets a client - the App page,
// hm2mqtt.js, node-red-contrib-ccu, homematic-manager - draw every value the second it connects,
// with one request instead of a getParamset storm and without waiting for the first event.
//
// Fed by the bus (task 75): Enrich sees every event as the bus publishes it, in order and before
// the stream does, so the stream's event carries the same lc the bulk read answers. A sweep
// (sweep.go) reads the channels whose entries are not confirmed, paced, whenever an interface
// comes up.
//
// Kept in occulited's database file (internal/store, D-113), bucket "state": one bucket per
// interface, the key <address>\x00<datapoint>. In the file's modes: ram keeps nothing across a
// restart; ram-sync writes everything at the interval and at the stop; persistent writes a value
// change at once (after the store's short gathering delay) and the timestamp-only refreshes -
// 97 % of the events on the Charly (task 194's measurement) - at the interval, so after a power
// cut ts may be up to one interval old while the value is right. What comes back from the file is
// confirmed=false, source "restored", until an event or the sweep reports the datapoint.
package devstate

import (
	"log/slog"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/store"
)

// Bucket is the store's top-level bucket in the database file.
const Bucket = "state"

// The sources of an entry's last report.
const (
	SourceEvent    = "event"
	SourceSweep    = "sweep"
	SourceRestored = "restored"
)

// Entry is one datapoint as the API answers it.
type Entry struct {
	Interface string `json:"interface"`
	Address   string `json:"address"` // the channel: 00010000000A10:1
	Datapoint string `json:"datapoint"`
	Value     any    `json:"value"`
	// TS is when the last report arrived, the same value or not; LC when the value last became
	// a different one - "since when" is LC
	TS          time.Time  `json:"ts"`
	LC          time.Time  `json:"lc"`
	Confirmed   bool       `json:"confirmed"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	Source      string     `json:"source"`
	// Previous is the value before LC, PreviousForS how long it stood (absent before the first
	// change the store saw)
	Previous     any      `json:"previous,omitempty"`
	PreviousForS *float64 `json:"previous_for_s,omitempty"`
	// ChangedAt is LC when the store saw the change happen (occulited task 95): an event that
	// changed a value the store had confirmed live. Absent when the moment is not known - an entry
	// seen for the first time, a change a sweep or a restored entry's first report found (it
	// happened at some time before) - and never guessed. Control's "for 3 h" is this.
	ChangedAt *time.Time `json:"changed_at,omitempty"`
}

type entry struct {
	value, prev   any
	hasPrev       bool
	seen          bool // lc is a change the store saw happen (ChangedAt)
	prevFor       time.Duration
	ts, lc        time.Time
	confirmed     bool
	confirmedAt   time.Time
	source        string
	gen, valueGen uint64 // bumped at every change / at every value change
	dirty         bool   // the file lacks this version
	urgent        bool   // ... and the value changed: persistent writes it at once
}

// Store is the state store.
type Store struct {
	// Tracked says which datapoints are kept; nil = Tracked (keys.go).
	Tracked func(datapoint string) bool
	// Kick is the database manager's: called after a value change, outside the lock.
	Kick func()
	// OnObserve sees every report the store gets, kept or not (task 195's history).
	OnObserve func(iface, address, datapoint string, value any, at time.Time)
	Log       *slog.Logger
	Now       func() time.Time

	mu      sync.Mutex
	m       map[string]*entry
	deleted map[string]bool // keys whose deletion the file lacks
	gen     uint64
	sweep   sweepState
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Store) tracked(dp string) bool {
	if s.Tracked != nil {
		return s.Tracked(dp)
	}
	return Tracked(dp)
}

// key is the map's key: interface, address and datapoint, NUL-separated (none of them holds one).
func key(iface, address, dp string) string { return iface + "\x00" + address + "\x00" + dp }

func splitKey(k string) (iface, address, dp string) {
	a, rest, _ := strings.Cut(k, "\x00")
	b, c, _ := strings.Cut(rest, "\x00")
	return a, b, c
}

// Normalize makes a reported value comparable across its sources: every integer an int, every
// float a float64.
func Normalize(v any) any {
	switch x := v.(type) {
	case int8:
		return int(x)
	case int16:
		return int(x)
	case int32:
		return int(x)
	case int64:
		return int(x)
	case uint8:
		return int(x)
	case uint16:
		return int(x)
	case uint32:
		return int(x)
	case float32:
		return float64(x)
	}
	return v
}

// Same says whether two reported values are the same value.
func Same(a, b any) bool { return equal(Normalize(a), Normalize(b)) }

func equal(a, b any) bool {
	switch a.(type) {
	case nil, bool, int, float64, string:
		return a == b
	}
	return reflect.DeepEqual(a, b)
}

// observed is what an observation did.
type observed struct {
	e       Entry
	tracked bool
	// changed: the entry is new, its value changed, or it was confirmed by this report - what the
	// stream announces as a state message after a sweep
	changed bool
}

// observe applies one report: an event, or a sweep's read (at = when the read was sent).
func (s *Store) observe(iface, address, dp string, v any, at time.Time, source string) observed {
	v = Normalize(v)
	if s.OnObserve != nil {
		s.OnObserve(iface, address, dp, v, at)
	}
	if !s.tracked(dp) {
		return observed{}
	}
	k := key(iface, address, dp)
	s.mu.Lock()
	if s.m == nil {
		s.m = map[string]*entry{}
	}
	en := s.m[k]
	urgent, changed := false, false
	switch {
	case en == nil:
		en = &entry{value: v, ts: at, lc: at, confirmed: true, confirmedAt: at, source: source}
		s.m[k] = en
		delete(s.deleted, k)
		urgent, changed = true, true
	case source == SourceSweep && en.ts.After(at):
		// an event came in after the sweep's read was sent: it is the newer report
		out := observed{e: en.export(iface, address, dp), tracked: true}
		s.mu.Unlock()
		return out
	default:
		if !equal(en.value, v) {
			en.prev, en.hasPrev, en.prevFor = en.value, true, at.Sub(en.lc)
			en.value, en.lc = v, at
			// task 95: the moment is known only for an event that changed a value confirmed live
			// before it; a sweep, or the first report of a restored entry, finds a change that
			// happened at some time before
			en.seen = source == SourceEvent && en.confirmed
			urgent, changed = true, true
		}
		if at.After(en.ts) {
			en.ts = at
		}
		if !en.confirmed {
			en.confirmed, en.confirmedAt, changed = true, at, true
		}
		en.source = source
	}
	s.gen++
	en.gen, en.dirty = s.gen, true
	if urgent {
		en.valueGen, en.urgent = s.gen, true
	}
	out := observed{e: en.export(iface, address, dp), tracked: true, changed: changed}
	s.mu.Unlock()
	if urgent && s.Kick != nil {
		s.Kick()
	}
	return out
}

func (en *entry) export(iface, address, dp string) Entry {
	e := Entry{Interface: iface, Address: address, Datapoint: dp, Value: en.value, TS: en.ts, LC: en.lc, Confirmed: en.confirmed, Source: en.source}
	if en.confirmed && !en.confirmedAt.IsZero() {
		t := en.confirmedAt
		e.ConfirmedAt = &t
	}
	if en.hasPrev {
		e.Previous = en.prev
		f := seconds(en.prevFor)
		e.PreviousForS = &f
	}
	if en.seen {
		t := en.lc
		e.ChangedAt = &t
	}
	return e
}

// changedAt is the stream's form of ChangedAt ("" when unknown).
func changedAt(e Entry) string {
	if e.ChangedAt == nil {
		return ""
	}
	return e.ChangedAt.UTC().Format(time.RFC3339Nano)
}

// seconds is a duration in seconds with milliseconds.
func seconds(d time.Duration) float64 { return math.Round(float64(d.Milliseconds())) / 1000 }

// Enrich is the bus's hook (rpcsub.Config.Enrich): every event goes into the store, and an event
// of a kept datapoint carries its last change and the previous value's duration on to the stream.
func (s *Store) Enrich(m *rpcsub.Message, at time.Time) {
	if m.Type != "event" {
		return
	}
	o := s.observe(m.Interface, m.Address, m.Key, m.Value, at, SourceEvent)
	if !o.tracked {
		return
	}
	m.LC = o.e.LC.UTC().Format(time.RFC3339Nano)
	m.PreviousForS = o.e.PreviousForS
	m.ChangedAt = changedAt(o.e)
}

// Event is the bus handler's (rpcsub.Handler): events arrive through Enrich instead.
func (s *Store) Event(rpcsub.Event) {}

// Interface is the bus handler's: an interface that came up or back is swept for what is not
// confirmed; one that left the list takes its entries with it.
func (s *Store) Interface(name, state string, _ time.Time) {
	switch state {
	case "up", "restarted":
		s.SweepSoon(name)
	case "removed":
		s.drop(func(iface, _ string) bool { return iface == name })
	}
}

// Devices is the bus handler's: deleted devices take their entries with them, new ones are swept.
func (s *Store) Devices(iface, op string, addresses []string) {
	switch op {
	case "deleted":
		s.drop(func(i, address string) bool {
			if i != iface {
				return false
			}
			for _, a := range addresses {
				if address == a || strings.HasPrefix(address, a+":") {
					return true
				}
			}
			return false
		})
	default:
		s.SweepSoon(iface)
	}
}

func (s *Store) drop(match func(iface, address string) bool) {
	s.mu.Lock()
	n := 0
	for k := range s.m {
		iface, address, _ := splitKey(k)
		if match(iface, address) {
			delete(s.m, k)
			if s.deleted == nil {
				s.deleted = map[string]bool{}
			}
			s.deleted[k] = true
			n++
		}
	}
	s.mu.Unlock()
	if n > 0 {
		s.log().Info("state: entries dropped", "count", n)
		if s.Kick != nil {
			s.Kick()
		}
	}
}

// Count is how many datapoints the store keeps (store.Counter).
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// Filter is the bulk read's: AND across kinds, OR within one; an address matches a device and
// its channels.
type Filter struct {
	Interfaces []string
	Addresses  []string
	Datapoints []string
}

func (f Filter) match(iface, address, dp string) bool {
	if len(f.Interfaces) > 0 && !has(f.Interfaces, iface) {
		return false
	}
	if len(f.Datapoints) > 0 && !has(f.Datapoints, dp) {
		return false
	}
	if len(f.Addresses) > 0 {
		for _, a := range f.Addresses {
			if address == a || strings.HasPrefix(address, a+":") {
				return true
			}
		}
		return false
	}
	return true
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Page is one answer of the bulk read.
type Page struct {
	Entries []Entry `json:"entries"`
	// Total is how many entries match the filter; Unconfirmed how many of those are not
	// confirmed yet
	Total       int `json:"total"`
	Unconfirmed int `json:"unconfirmed"`
	// Next is the cursor of the next page ("" = this was the last): pass it as after=
	Next string `json:"next,omitempty"`
}

// Cursor is an entry's place in the order (interface, address, datapoint).
func Cursor(e Entry) string { return e.Interface + "/" + e.Address + "/" + e.Datapoint }

// Read answers the bulk read: the matching entries in the order interface, address, datapoint,
// from after the cursor, at most limit of them.
func (s *Store) Read(f Filter, after string, limit int) Page {
	s.mu.Lock()
	type kv struct {
		k  string
		en Entry
	}
	var all []kv
	unconfirmed := 0
	for k, en := range s.m {
		iface, address, dp := splitKey(k)
		if !f.match(iface, address, dp) {
			continue
		}
		if !en.confirmed {
			unconfirmed++
		}
		all = append(all, kv{k, en.export(iface, address, dp)})
	}
	s.mu.Unlock()
	sort.Slice(all, func(a, b int) bool { return all[a].k < all[b].k })
	p := Page{Entries: []Entry{}, Total: len(all), Unconfirmed: unconfirmed}
	start := 0
	if after != "" {
		start = sort.Search(len(all), func(i int) bool { return Cursor(all[i].en) > after })
	}
	for i := start; i < len(all); i++ {
		if limit > 0 && len(p.Entries) == limit {
			p.Next = Cursor(p.Entries[len(p.Entries)-1])
			break
		}
		p.Entries = append(p.Entries, all[i].en)
	}
	return p
}

// Get is one entry.
func (s *Store) Get(iface, address, dp string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	en := s.m[key(iface, address, dp)]
	if en == nil {
		return Entry{}, false
	}
	return en.export(iface, address, dp), true
}

// ---- the database file (store.EntryKeeper) ---------------------------------------------------

// Bucket is the keeper's top-level bucket.
func (s *Store) Bucket() string { return Bucket }

// RestoreEntries takes what the file holds: every entry not confirmed, source restored, until a
// report confirms it. An entry memory already has (a switch out of ram) is memory's.
func (s *Store) RestoreEntries(groups map[string]map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]*entry{}
	}
	n, bad := 0, 0
	for iface, g := range groups {
		for fk, v := range g {
			address, dp, ok := strings.Cut(fk, "\x00")
			if !ok {
				bad++
				continue
			}
			k := key(iface, address, dp)
			if _, has := s.m[k]; has {
				continue
			}
			en, ok := decode(v)
			if !ok {
				bad++
				continue
			}
			en.source, en.confirmed = SourceRestored, false
			s.m[k] = en
			n++
		}
	}
	if bad > 0 {
		s.log().Warn("state: entries in the database file could not be read and were skipped", "count", bad)
	}
	s.log().Info("state: restored from the database file, not confirmed until reported", "entries", n)
}

// PendingEntries returns what the file lacks (store.EntryKeeper): every entry after an open;
// otherwise the changed ones, and between two intervals of persistent mode (urgent) only those
// whose value changed and the deletions.
func (s *Store) PendingEntries(all, urgent bool) ([]store.Entry, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Entry
	written := map[string]uint64{}
	for k, en := range s.m {
		if !all && (!en.dirty || urgent && !en.urgent) {
			continue
		}
		iface, address, dp := splitKey(k)
		out = append(out, store.Entry{Bucket: Bucket, Group: iface, Key: address + "\x00" + dp, Value: encode(en)})
		written[k] = en.gen
	}
	var gone []string
	for k := range s.deleted {
		iface, address, dp := splitKey(k)
		out = append(out, store.Entry{Bucket: Bucket, Group: iface, Key: address + "\x00" + dp})
		gone = append(gone, k)
	}
	return out, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for k, g := range written {
			en := s.m[k]
			if en == nil {
				continue
			}
			if en.valueGen <= g {
				en.urgent = false
			}
			if en.gen == g {
				en.dirty = false
			}
		}
		for _, k := range gone {
			if _, back := s.m[k]; !back {
				delete(s.deleted, k)
			}
		}
	}
}
