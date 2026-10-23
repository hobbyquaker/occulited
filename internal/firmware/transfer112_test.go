package firmware

import (
	"testing"
	"time"
)

// occulited B-112: the time left rests on enough progress or on nothing. The series are the
// lab's (2026-10-14, a 99,540-byte image at 30-byte frames, the regular line every 3,000 bytes
// ~22 min apart) and the maintainer's report (30 of 87,352 bytes, "15 min left").

// progressAt applies one progress line of the unicast shape to the first device.
func progressAt(tr Transfers, next, size int64, at time.Time) {
	tr.Apply(TransferEvent{Kind: "progress", Addresses: []string{devC1}, Next: next, Size: size}, at)
}

func handlerAt(tr Transfers, kind string, at time.Time) {
	tr.Apply(TransferEvent{Kind: kind, Addresses: []string{devC1}, AP: "3014F711A000040000000A02", Handler: "1b2c3d4e-0000-4000-8000-00000000c0de"}, at)
}

// hours checks the estimate lies in [lo, hi] hours.
func hours(t *testing.T, what string, got int64, lo, hi float64) {
	t.Helper()
	h := float64(got) / 3600
	if got < 0 || h < lo || h > hi {
		t.Errorf("%s: %d s = %.1f h, want %.1f-%.1f h", what, got, h, lo, hi)
	}
}

// The report: init 0 and the first frame's 30 bytes come within a second; the estimate waits for
// the first regular line, then says hours.
func TestETAStartBurst(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 19, 0, 0, 0, time.UTC)
	const size = 87352
	tr := Transfers{}
	handlerAt(tr, "started", t0)
	progressAt(tr, 0, size, t0)                            // acknowledged init. Current 0 - next Expected 0
	progressAt(tr, 30, size, t0.Add(300*time.Millisecond)) // acknowledged update frame(s). Current 0 - next Expected 30
	if got := eta(tr[devC1]); got != -1 {
		t.Errorf("init and the first frame within a second: %d s, want unknown (was 15 min)", got)
	}
	if p := tr[devC1].public(); p.ETASeconds != nil || p.Percent != 0 || p.Bytes != 30 {
		t.Errorf("public: %+v", p)
	}
	// a watchdog's continue 3.5 min later at 390 bytes (the lab's 13:19:05): the rate would be right,
	// but it rests on 13 frames - still unknown
	progressAt(tr, 390, size, t0.Add(210*time.Second))
	if got := eta(tr[devC1]); got != -1 {
		t.Errorf("390 bytes in 3.5 min: %d s, want unknown", got)
	}
	// the first regular line, 3,000 bytes after 25 min: ~115 B/min, 84,352 left = ~12.2 h
	progressAt(tr, 3030, size, t0.Add(25*time.Minute+10*time.Second))
	hours(t, "first regular line", eta(tr[devC1]), 11.5, 13)
}

// The regular cadence: every 3,000 bytes, 22 min apart, gives ~136 B/min; a cluster of a missed
// and an acknowledged line 14 s apart inside the series does not move the estimate.
func TestETARegularCadenceAndCluster(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 17, 4, 50, 0, time.UTC)
	const size = 99540
	tr := Transfers{}
	var at time.Time
	for i := int64(0); i <= 4; i++ {
		at = t0.Add(time.Duration(i) * 22 * time.Minute)
		progressAt(tr, 21000+i*3000, size, at)
	}
	// 33,000 of 99,540 at 136.4 B/min: 66,540 left = 8.1 h
	before := eta(tr[devC1])
	hours(t, "regular cadence", before, 7.8, 8.4)
	// the lab's 21:48:01/21:48:15 pair: missed … next Expected 56970, then the ACK at 57030
	progressAt(tr, 33000+60, size, at.Add(14*time.Second))
	after := eta(tr[devC1])
	hours(t, "after the cluster", after, 7.8, 8.4)
	// 60 bytes more and a rate a shade higher: a few minutes less, never hours
	if d := before - after; d < 0 || d > 300 {
		t.Errorf("the cluster moved the estimate by %d s (before %d, after %d)", d, before, after)
	}
}

// A duty-cycle pause: the first line after the resume rests on too little within the hour, so the
// window takes older samples in; the estimate stays instead of going unknown for 20 minutes.
func TestETAWindowGrowsOverPause(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 17, 4, 50, 0, time.UTC)
	const size = 99540
	tr := Transfers{}
	progressAt(tr, 21000, size, t0)
	progressAt(tr, 24000, size, t0.Add(22*time.Minute))
	progressAt(tr, 27000, size, t0.Add(44*time.Minute))
	// the lab's 17:59 continue at 28,500, the suspend at 18:07, the resume at 18:27 with a missed line at 29,640
	progressAt(tr, 28500, size, t0.Add(55*time.Minute))
	handlerAt(tr, "suspended", t0.Add(63*time.Minute))
	if got := eta(tr[devC1]); got != -1 {
		t.Errorf("paused: %d, want unknown", got)
	}
	handlerAt(tr, "continues", t0.Add(83*time.Minute))
	progressAt(tr, 29640, size, t0.Add(83*time.Minute))
	// within the hour before 18:27: the 55-min and the 44-min lines (1,140 and 2,640 bytes, 8 and
	// 19 active minutes) - not enough; with the 22-min line it is 5,640 bytes over 41 active minutes
	got := eta(tr[devC1])
	hours(t, "after the resume", got, 8, 9.5)
	// a watchdog's continue a second after the missed line changes nothing
	progressAt(tr, 29670, size, t0.Add(83*time.Minute+time.Second))
	hours(t, "watchdog cluster after the resume", eta(tr[devC1]), 8, 9.5)
	// when even the oldest sample is too close, it stays unknown
	short := Transfers{}
	progressAt(short, 28500, size, t0)
	handlerAt(short, "suspended", t0.Add(8*time.Minute))
	handlerAt(short, "continues", t0.Add(28*time.Minute))
	progressAt(short, 29640, size, t0.Add(28*time.Minute))
	if got := eta(short[devC1]); got != -1 {
		t.Errorf("only 1,140 bytes over 8 active minutes: %d, want unknown", got)
	}
}

// The ceiling: a series that passes the minimums at an impossible rate reads at 250 B/min, never
// as minutes.
func TestETACeiling(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 19, 0, 0, 0, time.UTC)
	const size = 87352
	tr := Transfers{}
	progressAt(tr, 0, size, t0)
	// 45,000 bytes in 11 minutes (4,090 B/min)
	progressAt(tr, 45000, size, t0.Add(11*time.Minute))
	// 42,352 left at 250 B/min = 169.4 min
	if got := eta(tr[devC1]); got != 10164 {
		t.Errorf("ceiling: %d s, want 10164", got)
	}
	// a real fast device, 3,000 bytes in 15 min = 200 B/min, is not slowed down: 84,352 left = 421.8 min
	fast := Transfers{}
	progressAt(fast, 0, size, t0)
	progressAt(fast, 3000, size, t0.Add(15*time.Minute))
	if got := eta(fast[devC1]); got != 25305 {
		t.Errorf("200 B/min: %d s, want 25305", got)
	}
	// too fast for the active minimum: 3,000 bytes in 9 minutes is a cluster, not a rate
	cluster := Transfers{}
	progressAt(cluster, 0, size, t0)
	progressAt(cluster, 3000, size, t0.Add(9*time.Minute))
	if got := eta(cluster[devC1]); got != -1 {
		t.Errorf("3,000 bytes in 9 min: %d, want unknown", got)
	}
}

// The multicast handler's blocks: one block over ten active minutes is enough, and no byte ceiling
// applies to them.
func TestETABlocks(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	tr := Transfers{}
	applyLines(t, tr, t0, mcStarted)
	sent := func(n int64, at time.Time) {
		tr.Apply(TransferEvent{Kind: "sent", AP: "3014F711A000040000000A02", Next: n, Size: 50}, at)
	}
	sent(1, t0)
	sent(2, t0.Add(5*time.Minute))
	if got := eta(tr[devC1]); got != -1 {
		t.Errorf("one block in 5 min: %d, want unknown", got)
	}
	sent(3, t0.Add(40*time.Minute))
	// 2 blocks in 40 min, 47 left = 940 min
	if x := tr[devC1]; x.Unit != UnitBlocks || eta(x) != 56400 {
		t.Errorf("blocks: unit %q eta %d, want 56400", x.Unit, eta(x))
	}
	// the devices of the handler share it
	if eta(tr[devC2]) != 56400 {
		t.Errorf("second device: %d", eta(tr[devC2]))
	}
	// a device's own bytes start the samples over: unknown until they span an interval
	progressAt(tr, 6000, 101588, t0.Add(41*time.Minute))
	if got := eta(tr[devC1]); got != -1 {
		t.Errorf("first byte line after blocks: %d, want unknown", got)
	}
	progressAt(tr, 9000, 101588, t0.Add(63*time.Minute))
	hours(t, "bytes after blocks", eta(tr[devC1]), 11, 12)
	if eta(tr[devC2]) != 56400 {
		t.Errorf("the other device keeps the blocks: %d", eta(tr[devC2]))
	}
}
