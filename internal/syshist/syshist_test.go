package syshist

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// occulited task 37: one sample a poll, the ring cut to Rows, a failed read taking none.
func TestPollKeepsARing(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	load, ok := 0.0, true
	polled := 0
	s := &Sampler{Rows: 3, Now: c.now, Polled: func() { polled++ }, Read: func() (Reading, bool) { load += 0.257; return Reading{Load: load, Mem: 40}, ok }}
	for range 4 {
		s.Poll()
		c.add(time.Minute)
	}
	ok = false
	s.Poll()
	h := s.History()
	if h.IntervalS != 60 || len(h.Samples) != 3 || polled != 4 {
		t.Fatalf("%+v, polled %d", h, polled)
	}
	if h.Samples[0].Load != 0.51 || h.Samples[2].Load != 1.03 || h.Samples[2].Mem != 40 || !h.Samples[2].Time.Equal(c.now().Add(-time.Minute)) {
		t.Fatalf("samples %+v", h.Samples)
	}
	(&Sampler{}).Poll() // no reader: nothing, no panic
}

// The database: the rows round-trip, Pending gives what the file lacks, Restore merges the file's
// older rows in front of memory's.
func TestPersistence(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	s := &Sampler{Now: c.now, Read: func() (Reading, bool) { return Reading{Load: 3.5, Mem: -1}, true }}
	if got, _ := s.Pending(false); got != nil {
		t.Fatalf("pending before a sample: %v", got)
	}
	s.Poll()
	c.add(time.Minute)
	s.Poll()
	got, done := s.Pending(false)
	if len(got) != 1 || got[0].Bucket != "system" || got[0].Name != "load" || len(got[0].Rows) != 2 || got[0].Replace {
		t.Fatalf("pending %+v", got)
	}
	done()
	if again, _ := s.Pending(false); again != nil {
		t.Fatalf("written rows pending again: %+v", again)
	}
	if all, _ := s.Pending(true); len(all) != 1 || len(all[0].Rows) != 2 || !all[0].Replace {
		t.Fatalf("all after an open: %+v", all)
	}
	// a restart: the file's rows come back in a new sampler, a reboot's hole stays a hole
	file := got[0].Rows
	file = append(file, store.Row{At: c.now(), Value: []byte{9, 9}}) // a row of another version: skipped
	n := &Sampler{Now: c.now}
	n.Restore(map[string][]store.Row{"load": file, "other": nil})
	h := n.History().Samples
	if len(h) != 2 || h[0].Load != 3.5 || h[0].Mem != -1 || !h[1].Time.Equal(c.now()) {
		t.Fatalf("restored %+v", h)
	}
	if p, _ := n.Pending(false); p != nil {
		t.Fatalf("restored rows pending: %+v", p)
	}
	// memory first (a switch out of ram), the file's older rows in front, its newer ones dropped
	c.add(time.Hour)
	m := &Sampler{Now: c.now, Read: func() (Reading, bool) { return Reading{Load: 0.1, Mem: 20}, true }}
	m.Poll()
	m.Restore(map[string][]store.Row{"load": append(file[:2:2], encode(Sample{Time: c.now().Add(time.Second), Load: 9}))})
	h = m.History().Samples
	if len(h) != 3 || h[2].Load != 0.1 {
		t.Fatalf("merged %+v", h)
	}
	if p, _ := m.Pending(false); len(p) != 1 || len(p[0].Rows) != 1 {
		t.Fatalf("memory's own row pending: %+v", p)
	}
	m.Restore(nil) // nothing of ours in the file
	if len(m.History().Samples) != 3 {
		t.Fatal("an empty restore changed the ring")
	}
}

func TestEncodeClamps(t *testing.T) {
	for _, c := range []struct {
		in   Sample
		load float64
		mem  int
	}{
		{Sample{Load: -1, Mem: 150}, 0, 100},
		{Sample{Load: 1e12, Mem: -5}, 42949672.95, -1},
		{Sample{Load: 0.42, Mem: 63}, 0.42, 63},
	} {
		got, ok := decode(encode(c.in))
		if !ok || got.Load != c.load || got.Mem != c.mem {
			t.Errorf("%+v: %+v", c.in, got)
		}
	}
}

func TestRunSamplesAtOnceAndStops(t *testing.T) {
	var mu sync.Mutex
	n := 0
	s := &Sampler{Interval: 10 * time.Millisecond, Read: func() (Reading, bool) { mu.Lock(); n++; mu.Unlock(); return Reading{Load: 1}, true }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		got := n
		mu.Unlock()
		if got >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no samples")
		}
	}
	cancel()
	<-done
}

// occulited B-75: with a trusted clock a poll drops the samples stamped in the future (a boot with
// the clock in 2067) and the next write replaces the series in the file; an untrusted clock keeps them.
func TestPollDropsFutureSamples(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 15, 21, 39, 0, 0, time.UTC)}
	wrong := time.Date(2067, 7, 4, 18, 0, 0, 0, time.UTC)
	for _, trusted := range []bool{false, true} {
		s := &Sampler{Rows: 10, Now: c.now, Trusted: func() bool { return trusted }, Read: func() (Reading, bool) { return Reading{Load: 0.1, Mem: 30}, true }}
		s.Restore(map[string][]store.Row{series: {
			encode(Sample{Time: c.now().Add(-2 * time.Minute), Load: 0.1, Mem: 30}),
			encode(Sample{Time: wrong, Load: 0.6, Mem: 14}),
			encode(Sample{Time: wrong.Add(5 * time.Minute), Load: 0.6, Mem: 30}),
		}})
		s.Poll()
		h := s.History().Samples
		if !trusted {
			if len(h) != 4 {
				t.Errorf("untrusted: %d samples, want 4", len(h))
			}
			if series, _ := s.Pending(false); len(series) != 1 || series[0].Replace || len(series[0].Rows) != 1 {
				t.Errorf("untrusted: pending %+v", series)
			}
			continue
		}
		if len(h) != 2 || h[0].Time.Year() != 2026 || h[1].Time.Year() != 2026 {
			t.Fatalf("trusted: %+v", h)
		}
		series, done := s.Pending(false)
		if len(series) != 1 || !series[0].Replace || len(series[0].Rows) != 2 {
			t.Fatalf("trusted: pending %+v", series)
		}
		done()
		if series, _ := s.Pending(false); len(series) != 0 {
			t.Errorf("pending after done: %+v", series)
		}
	}
}
