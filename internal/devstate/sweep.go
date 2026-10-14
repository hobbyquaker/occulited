package devstate

import (
	"context"
	"sort"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpcsub"
)

// The sweep (task 194, decided 2026-09-24; HmIP only since occulited task 38, the maintainer
// 2026-10-14): whenever HmIP-RF comes up - at occulited's start right after the subscriber
// registered - its channels whose entries are not confirmed (restored from the file), and the
// channels the store has never read, are read with getParamset(VALUES), one at a time with a pause
// between two, so a big installation's ~3,000 channels take ten minutes at the default pace.
//
// Only HmIP-RF, because only there a read is free on air (openccu-lite task 287, measured;
// docs/system-api.md, "Seeding values"): hmipserver answers every HmIP channel from its cache, no
// frame. rfd asks an always-listening BidCos actuator for its status on every read of a value
// channel (one frame pair), and a device that is away costs rfd's retries and comes back UNREACH
// (occulited B-46's boot sweep on rpi3-2) - a boot is no time for that much airtime. So
// BidCos-RF and BidCos-Wired are never swept: their entries stay as restored (confirmed false,
// source restored, since kept) until an event confirms them. Left out as well: VirtualDevices (its
// BidCos heating groups not measured) and HmIP's wired devices (HmIPW-*, not measured; the
// Source leaves their channels out). A datapoint the answer does not mention stays unconfirmed
// (PRESS_SHORT is only ever an event): the next event confirms it.

// Swept says whether the sweep reads an interface: HmIP-RF alone (above).
func Swept(iface string) bool { return iface == "HmIP-RF" }

// DefaultPace is the pause between two reads of the sweep.
const DefaultPace = 200 * time.Millisecond

// Source is what the sweep reads through: the interface process's channel list (without the
// channels of devices whose read is not known to be free on air: HmIPW-*) and one channel's VALUES.
type Source interface {
	Channels(ctx context.Context, iface string) ([]string, error)
	Values(ctx context.Context, iface, channel string) (map[string]any, error)
}

type sweepState struct {
	wake    chan struct{}
	queued  map[string]bool
	nothing map[string]bool // iface\x00channel: read, and nothing of the chosen set in it
	last    map[string]SweepStatus
}

// SweepStatus is one interface's last sweep, for the API.
type SweepStatus struct {
	At       time.Time `json:"at"`
	Channels int       `json:"channels"`
	Read     int       `json:"read"`
	Failed   int       `json:"failed"`
	Error    string    `json:"error,omitempty"`
	Seconds  float64   `json:"seconds"`
}

// SweepSoon queues a sweep of one interface; one the sweep does not read (Swept) is left alone.
func (s *Store) SweepSoon(iface string) {
	if !Swept(iface) {
		return
	}
	s.mu.Lock()
	if s.sweep.queued == nil {
		s.sweep.queued = map[string]bool{}
	}
	s.sweep.queued[iface] = true
	w := s.sweep.wake
	s.mu.Unlock()
	if w != nil {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

// Sweeps is every interface's last sweep.
func (s *Store) Sweeps() map[string]SweepStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]SweepStatus{}
	for k, v := range s.sweep.last {
		out[k] = v
	}
	return out
}

// RunSweeps sweeps what SweepSoon queued until ctx ends. publish puts a state message on the bus
// for every entry a sweep created, changed or confirmed (nil: none); pace is the pause between
// two reads (DefaultPace when 0).
func (s *Store) RunSweeps(ctx context.Context, src Source, publish func(rpcsub.Message), pace time.Duration) {
	if pace <= 0 {
		pace = DefaultPace
	}
	s.mu.Lock()
	s.sweep.wake = make(chan struct{}, 1)
	w := s.sweep.wake
	pending := len(s.sweep.queued) > 0
	s.mu.Unlock()
	if pending {
		select {
		case w <- struct{}{}:
		default:
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-w:
		}
		// an interface's "up" comes with its restart's "devices": gather them for a second
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		s.mu.Lock()
		var ifaces []string
		for i := range s.sweep.queued {
			ifaces = append(ifaces, i)
		}
		s.sweep.queued = map[string]bool{}
		s.mu.Unlock()
		sort.Strings(ifaces)
		for _, i := range ifaces {
			s.sweepOne(ctx, src, i, publish, pace)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// todo is the sweep's list for one interface: the channels with an unconfirmed entry first, then
// the ones never read; a channel whose entries are all confirmed is not read.
func (s *Store) todo(iface string, channels []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	known := map[string]bool{}
	unconfirmed := map[string]bool{}
	for k, en := range s.m {
		i, address, _ := splitKey(k)
		if i != iface {
			continue
		}
		known[address] = true
		if !en.confirmed {
			unconfirmed[address] = true
		}
	}
	var first, then []string
	for _, c := range channels {
		switch {
		case unconfirmed[c]:
			first = append(first, c)
		case !known[c] && !s.sweep.nothing[iface+"\x00"+c]:
			then = append(then, c)
		}
	}
	return append(first, then...)
}

func (s *Store) sweepOne(ctx context.Context, src Source, iface string, publish func(rpcsub.Message), pace time.Duration) {
	started := s.now()
	st := SweepStatus{At: started}
	channels, err := src.Channels(ctx, iface)
	if err != nil {
		st.Error = err.Error()
		s.setSweep(iface, st)
		s.log().Warn("state: the sweep could not list the channels", "interface", iface, "err", err)
		return
	}
	todo := s.todo(iface, channels)
	st.Channels = len(todo)
	confirmed := 0
	for n, c := range todo {
		if n > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pace):
			}
		}
		at := s.now()
		vals, err := src.Values(ctx, iface, c)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			st.Failed++
			s.log().Debug("state: the sweep could not read a channel", "interface", iface, "channel", c, "err", err)
			continue
		}
		st.Read++
		kept := false
		dps := make([]string, 0, len(vals))
		for dp := range vals {
			dps = append(dps, dp)
		}
		sort.Strings(dps)
		for _, dp := range dps {
			o := s.observe(iface, c, dp, vals[dp], at, SourceSweep)
			if !o.tracked {
				continue
			}
			kept = true
			if o.changed {
				confirmed++
				if publish != nil {
					publish(stateMessage(o.e, at))
				}
			}
		}
		if !kept {
			s.mu.Lock()
			if s.sweep.nothing == nil {
				s.sweep.nothing = map[string]bool{}
			}
			s.sweep.nothing[iface+"\x00"+c] = true
			s.mu.Unlock()
		}
	}
	st.Seconds = seconds(s.now().Sub(started))
	s.setSweep(iface, st)
	if len(todo) > 0 {
		s.log().Info("state: swept", "interface", iface, "channels", len(todo), "read", st.Read, "failed", st.Failed, "reported", confirmed, "seconds", st.Seconds)
	}
}

// Reconcile takes one channel's VALUES as another reader of the interface got them - the
// service-message store's sweep of the maintenance channels and its re-read of a sticky message
// (occulited B-46: rfd resets STICKY_UNREACH on its acknowledgement without an event, so the
// entry here said true until the interface restarted). Only what differs from the store is taken:
// a kept datapoint with another value, one the store has not confirmed or does not know. Each
// becomes a sweep's report - on the bus through publish (nil: none), in the history - and at is
// when the read began, so an event that came in after it stays the newer report. It returns how
// many entries it changed.
func (s *Store) Reconcile(iface, channel string, vals map[string]any, at time.Time, publish func(rpcsub.Message)) int {
	dps := make([]string, 0, len(vals))
	for dp := range vals {
		if s.tracked(dp) {
			dps = append(dps, dp)
		}
	}
	sort.Strings(dps)
	n := 0
	for _, dp := range dps {
		s.mu.Lock()
		en := s.m[key(iface, channel, dp)]
		same := en != nil && en.confirmed && equal(en.value, Normalize(vals[dp]))
		s.mu.Unlock()
		if same {
			continue
		}
		o := s.observe(iface, channel, dp, vals[dp], at, SourceSweep)
		if !o.changed {
			continue
		}
		n++
		if publish != nil {
			publish(stateMessage(o.e, at))
		}
	}
	return n
}

func (s *Store) setSweep(iface string, st SweepStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sweep.last == nil {
		s.sweep.last = map[string]SweepStatus{}
	}
	s.sweep.last[iface] = st
}

// stateMessage is a sweep's report as the bus carries it: the entry's shape, the datapoint in Key,
// the read's time as TS.
func stateMessage(e Entry, at time.Time) rpcsub.Message {
	c := e.Confirmed
	return rpcsub.Message{Type: "state", Interface: e.Interface, Address: e.Address, Key: e.Datapoint, Value: e.Value,
		TS: at.UTC().Format(time.RFC3339Nano), LC: e.LC.UTC().Format(time.RFC3339Nano), PreviousForS: e.PreviousForS,
		Confirmed: &c, Source: e.Source}
}
