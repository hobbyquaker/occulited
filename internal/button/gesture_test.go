package button

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// step is one input to the Detector: p press, r release, t tick, at ms after the start.
type step struct {
	op string
	ms int
}

func run(t *testing.T, tm Timing, steps []step) []string {
	t.Helper()
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	d := &Detector{T: tm}
	var kinds []string
	for _, s := range steps {
		at := t0.Add(time.Duration(s.ms) * time.Millisecond)
		var ev []Event
		switch s.op {
		case "p":
			ev = d.Press(at)
		case "r":
			ev = d.Release(at)
		case "t":
			ev = d.Tick(at)
		}
		for _, e := range ev {
			if e.Kind != EventPress && e.Kind != EventRelease {
				kinds = append(kinds, e.Kind)
			}
		}
	}
	return kinds
}

func TestDetector(t *testing.T) {
	tm := DefaultTiming(0)
	for _, c := range []struct {
		name  string
		steps []step
		want  []string
	}{
		{"single: short, then the window passes", []step{{"p", 0}, {"r", 150}, {"t", 400}, {"t", 600}}, []string{GestureSingle}},
		{"single waits for the window", []step{{"p", 0}, {"r", 150}, {"t", 500}}, nil},
		{"double: a second short press within the window", []step{{"p", 0}, {"r", 120}, {"p", 400}, {"r", 520}, {"t", 2000}}, []string{GestureDouble}},
		{"two singles: the second press after the window", []step{{"p", 0}, {"r", 120}, {"p", 700}, {"r", 800}, {"t", 1300}}, []string{GestureSingle, GestureSingle}},
		{"long: held past the threshold, released before cancel", []step{{"p", 0}, {"t", 4999}, {"t", 5000}, {"r", 6000}}, []string{EventLongReached, GestureLong}},
		{"long without ticks: reached and long at the release", []step{{"p", 0}, {"r", 7000}}, []string{EventLongReached, GestureLong}},
		{"in between: 1 s to the threshold is nothing", []step{{"p", 0}, {"r", 2500}, {"t", 5000}}, nil},
		{"cancel: held past 15 s", []step{{"p", 0}, {"t", 5000}, {"t", 15000}, {"r", 20000}}, []string{EventLongReached, EventCancel}},
		{"cancel without ticks: no release-now first", []step{{"p", 0}, {"r", 16000}}, []string{EventCancel}},
		{"a release without a press", []step{{"r", 10}, {"t", 1000}}, nil},
		{"a second press while down is the same press", []step{{"p", 0}, {"p", 50}, {"r", 150}, {"t", 1000}}, []string{GestureSingle}},
		{"short, then long within the window: the long only", []step{{"p", 0}, {"r", 100}, {"p", 300}, {"r", 6000}}, []string{EventLongReached, GestureLong}},
	} {
		if got := run(t, tm, c.steps); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// a configured threshold: 3 s
	if got := run(t, DefaultTiming(3*time.Second), []step{{"p", 0}, {"r", 3200}}); !slices.Equal(got, []string{EventLongReached, GestureLong}) {
		t.Errorf("3 s threshold: %v", got)
	}
}

func TestDetectorNextAndEvents(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	d := &Detector{T: DefaultTiming(0)}
	if !d.Next().IsZero() {
		t.Error("idle has a deadline")
	}
	ev := d.Press(t0)
	if len(ev) != 1 || ev[0].Kind != EventPress || !d.Pressed() {
		t.Fatalf("press: %+v", ev)
	}
	if n := d.Next(); !n.Equal(t0.Add(LongDefault)) {
		t.Errorf("next while down: %v", n)
	}
	ev = d.Release(t0.Add(200 * time.Millisecond))
	if len(ev) != 1 || ev[0].Kind != EventRelease || ev[0].HeldMS != 200 || d.Pressed() {
		t.Errorf("release: %+v", ev)
	}
	if n := d.Next(); !n.After(t0.Add(600 * time.Millisecond)) {
		t.Errorf("next with a pending single: %v", n)
	}
	ev = d.Tick(d.Next())
	if len(ev) != 1 || ev[0].Kind != GestureSingle || !strings.HasPrefix(ev[0].At.Format(time.RFC3339Nano), "2026-10-15T12:00:00.6") {
		t.Errorf("single: %+v", ev)
	}
}
