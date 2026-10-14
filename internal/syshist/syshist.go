// Package syshist samples the system itself - the load average and the memory in use - once a
// minute and keeps their history for the Status page's graphs (occulited task 37), the way
// internal/health keeps the radio's duty cycle and carrier sense: a ring of Rows samples (one N
// for every series of occulited's database, 500, about 8 h), in memory and, unless the storage
// mode is ram, in occulited's database file (bucket "system"), so a restart or a deploy keeps the
// curve. A reboot is a hole in time, and the graph breaks the line there.
//
// The SoC temperature and the undervoltage flag were left out on purpose: where they come from
// differs per board (a thermal zone that may be missing, the Pi firmware's flag behind vcgencmd
// or a hwmon file), so they are not the cheap, clean addition the load and the memory are.
package syshist

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/store"
)

// Sample is one minute's reading.
type Sample struct {
	Time time.Time `json:"t"`
	// Load is the 1-minute load average
	Load float64 `json:"load"`
	// Mem is the memory in use (MemTotal - MemAvailable), in per cent of MemTotal; -1 unknown
	Mem int `json:"mem"`
}

// Reading is what Read returns: the 1-minute load average and the memory in use in per cent
// (-1 unknown); ok is false when nothing could be read.
type Reading struct {
	Load float64
	Mem  int
}

// Sampler reads every Interval and keeps Rows samples.
type Sampler struct {
	// Read takes one reading; false when /proc could not be read
	Read     func() (Reading, bool)
	Interval time.Duration // default 60 s
	Rows     int           // default store.DefaultRows
	Now      func() time.Time
	// Polled is called after every sample, outside the lock: the store writes the round at once in
	// persistent mode (store.Manager.Kick)
	Polled func()

	mu       sync.Mutex
	history  []Sample
	appended uint64 // samples ever appended
	synced   uint64 // of those, the ones the database has
}

const series = "load"

func (s *Sampler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sampler) rows() int {
	if s.Rows > 0 {
		return s.Rows
	}
	return store.DefaultRows
}

// Interval in force.
func (s *Sampler) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return time.Minute
}

// Run samples until ctx ends; the first sample is taken at once.
func (s *Sampler) Run(ctx context.Context) {
	s.Poll()
	t := time.NewTicker(s.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Poll()
		}
	}
}

// Poll takes one sample.
func (s *Sampler) Poll() {
	if s.Read == nil {
		return
	}
	r, ok := s.Read()
	if !ok {
		return
	}
	s.mu.Lock()
	s.history = append(s.history, Sample{Time: s.now(), Load: math.Round(r.Load*100) / 100, Mem: r.Mem})
	if n := s.rows(); len(s.history) > n {
		s.history = append([]Sample(nil), s.history[len(s.history)-n:]...)
	}
	s.appended++
	s.mu.Unlock()
	if s.Polled != nil {
		s.Polled()
	}
}

// History is what the API returns.
type History struct {
	IntervalS int      `json:"interval_s"`
	Samples   []Sample `json:"samples"`
}

// History returns a copy of the ring, oldest first.
func (s *Sampler) History() History {
	s.mu.Lock()
	defer s.mu.Unlock()
	return History{IntervalS: int(s.interval() / time.Second), Samples: append([]Sample{}, s.history...)}
}

// The sampler is the store's Keeper for its bucket.

// Bucket is the system history's bucket in occulited's database.
func (s *Sampler) Bucket() string { return "system" }

// Restore takes the file's rows older than memory's oldest sample, then memory's, cut to the ring.
func (s *Sampler) Restore(all map[string][]store.Row) {
	rows, ok := all[series]
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mem := s.history
	var merged []Sample
	for _, r := range rows {
		sm, ok := decode(r)
		if !ok || (len(mem) > 0 && !sm.Time.Before(mem[0].Time)) {
			continue
		}
		merged = append(merged, sm)
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].Time.Before(merged[j].Time) })
	merged = append(merged, mem...)
	if n := s.rows(); len(merged) > n {
		merged = merged[len(merged)-n:]
	}
	s.history = merged
	fromFile := len(merged) - len(mem)
	s.appended += uint64(max(fromFile, 0))
	s.synced = s.appended - uint64(len(mem))
}

// Pending returns the samples the file lacks - all of them, as a Replace series, after an open -
// and the function that marks them written.
func (s *Sampler) Pending(all bool) ([]store.Series, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.history)
	if !all {
		if d := s.appended - s.synced; d < uint64(n) {
			n = int(d)
		}
	}
	upTo := s.appended
	if n == 0 {
		return nil, func() {}
	}
	rows := make([]store.Row, 0, n)
	for _, sm := range s.history[len(s.history)-n:] {
		rows = append(rows, encode(sm))
	}
	return []store.Series{{Bucket: s.Bucket(), Name: series, Rows: rows, Replace: all}}, func() {
		s.mu.Lock()
		s.synced = upTo
		s.mu.Unlock()
	}
}

// A sample in the database: the time is the key; the value is a version byte, the load in
// hundredths as a big-endian uint32 and the memory in per cent as an int8 (-1 unknown).
const sampleV1 = 1

func encode(sm Sample) store.Row {
	v := make([]byte, 6)
	v[0] = sampleV1
	load := math.Round(sm.Load * 100)
	switch {
	case load < 0:
		load = 0
	case load > math.MaxUint32:
		load = math.MaxUint32
	}
	binary.BigEndian.PutUint32(v[1:], uint32(load))
	mem := max(min(sm.Mem, 100), -1)
	v[5] = byte(int8(mem))
	return store.Row{At: sm.Time, Value: v}
}

func decode(r store.Row) (Sample, bool) {
	v := r.Value
	if len(v) < 6 || v[0] != sampleV1 {
		return Sample{}, false
	}
	return Sample{Time: r.At, Load: float64(binary.BigEndian.Uint32(v[1:])) / 100, Mem: int(int8(v[5]))}, true
}
