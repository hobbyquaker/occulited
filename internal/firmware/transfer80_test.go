package firmware

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// occulited B-80 and task 72: the multicast handler's lines (several devices of one type at once),
// the duty-cycle pause, and the time-left estimate. The lines are normalised from a production
// journal of 2026-10-15/16, the identifiers invented (the access point …0A02, the devices …0C01 and
// …0C02: addresses 00160000000C01, 00160000000C02).
const (
	mcStarted   = "de.eq3.cbcs.server.core.otau.DeviceBackgroundUpdateSubsystem [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU RF Handler 1b2c3d4e-0000-4000-8000-00000000c0de started for device(s) [3014F711A000160000000C01, 3014F711A000160000000C02]"
	mcInit      = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerMulticastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU Transaction: Device 3014F711A000160000000C01 acknowledged init. Current 0 - Expected 0 of image size 101588"
	mcContinue1 = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerMulticastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU Transaction: Device 3014F711A000160000000C01 acknowledged continue. Current 45120 - Expected 45120 of image size 101588"
	mcContinue2 = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerMulticastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU Transaction: Device 3014F711A000160000000C02 acknowledged continue. Current 44880 - Expected 44880 of image size 101588"
	mcSent      = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerMulticastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: Backend Notification: Sent 12 of image size 50"
	mcNotify    = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerMulticastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: Backend Notification: Got unhandled Notification: DEVICE_UPDATE_STATE_CHANGED / RESPONSE_NAK"
	mcThreshold = "de.eq3.cbcs.server.core.otau.handler.AbstractUpdateHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: AccessPoint duty cycle exceeds threshold of 50.0 will suspend handler"
	mcSuspended = "de.eq3.cbcs.server.core.otau.DeviceBackgroundUpdateSubsystem [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU RF Handler 1b2c3d4e-0000-4000-8000-00000000c0de suspended for device(s) [3014F711A000160000000C01, 3014F711A000160000000C02]"
	mcContinues = "de.eq3.cbcs.server.core.otau.DeviceBackgroundUpdateSubsystem [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU RF Handler 1b2c3d4e-0000-4000-8000-00000000c0de continues for device(s) [3014F711A000160000000C01, 3014F711A000160000000C02]"
)

const (
	devC1 = "00160000000C01"
	devC2 = "00160000000C02"
)

func TestParseMulticastLines(t *testing.T) {
	for _, c := range []struct {
		line string
		want TransferEvent
		ok   bool
	}{
		{mcStarted, TransferEvent{Kind: "started", Addresses: []string{devC1, devC2}, AP: "3014F711A000040000000A02", Handler: "1b2c3d4e-0000-4000-8000-00000000c0de"}, true},
		{mcInit, TransferEvent{Kind: "progress", Addresses: []string{devC1}, AP: "3014F711A000040000000A02", Size: 101588}, true},
		{mcContinue1, TransferEvent{Kind: "progress", Addresses: []string{devC1}, AP: "3014F711A000040000000A02", Next: 45120, Size: 101588}, true},
		{mcSent, TransferEvent{Kind: "sent", AP: "3014F711A000040000000A02", Next: 12, Size: 50}, true},
		{mcSuspended, TransferEvent{Kind: "suspended", Addresses: []string{devC1, devC2}, AP: "3014F711A000040000000A02", Handler: "1b2c3d4e-0000-4000-8000-00000000c0de"}, true},
		{mcContinues, TransferEvent{Kind: "continues", Addresses: []string{devC1, devC2}, AP: "3014F711A000040000000A02", Handler: "1b2c3d4e-0000-4000-8000-00000000c0de"}, true},
		// the lines that say nothing a device's transfer needs
		{mcNotify, TransferEvent{}, false},
		{mcThreshold, TransferEvent{}, false},
		{"Backend Notification: Sent 3 of image size 0", TransferEvent{}, false},
		// the AP is optional
		{"Backend Notification: Sent 3 of image size 50", TransferEvent{Kind: "sent", Next: 3, Size: 50}, true},
	} {
		ev, ok := ParseTransferLine(c.line)
		if ok != c.ok {
			t.Errorf("%q: ok %v, want %v", c.line, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if ev.Kind != c.want.Kind || ev.Next != c.want.Next || ev.Size != c.want.Size || ev.AP != c.want.AP || ev.Handler != c.want.Handler || strings.Join(ev.Addresses, ",") != strings.Join(c.want.Addresses, ",") {
			t.Errorf("%q: %+v, want %+v", c.line, ev, c.want)
		}
	}
}

func applyLines(t *testing.T, tr Transfers, at time.Time, lines ...string) {
	t.Helper()
	for _, l := range lines {
		ev, ok := ParseTransferLine(l)
		if !ok {
			t.Fatalf("not a transfer line: %q", l)
		}
		tr.Apply(ev, at)
	}
}

// Two devices of one type: each device's own bytes; the handler's block count only where a device
// has none; a pause and its end.
func TestMulticastTransfer(t *testing.T) {
	tr := Transfers{}
	t0 := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	applyLines(t, tr, t0, mcStarted)
	if !tr[devC1].Multicast || !tr[devC2].Multicast || tr[devC1].AP != "3014F711A000040000000A02" {
		t.Fatalf("started: %+v", tr[devC1])
	}
	// the handler's blocks: both devices, no byte line yet - blocks, never done from a send
	applyLines(t, tr, t0.Add(time.Minute), mcSent)
	for _, a := range []string{devC1, devC2} {
		if x := tr[a]; x.Unit != UnitBlocks || x.Bytes != 12 || x.Size != 50 || x.Percent != 24 || x.State != TransferRunning {
			t.Errorf("%s after sent: %+v", a, x.Transfer)
		}
	}
	tr.Apply(TransferEvent{Kind: "sent", AP: "3014F711A000040000000A02", Next: 50, Size: 50}, t0.Add(2*time.Minute))
	if x := tr[devC1]; x.Percent != 99 || x.State != TransferRunning {
		t.Errorf("all blocks sent: %+v", x.Transfer)
	}
	// a sent line of another access point does not touch them
	if ch := tr.Apply(TransferEvent{Kind: "sent", AP: "3014F711A000040000000FFF", Next: 1, Size: 50}, t0.Add(3*time.Minute)); len(ch) != 0 {
		t.Errorf("another AP's sent changed %v", ch)
	}
	// the device's own bytes win, from then on the blocks are ignored for it
	applyLines(t, tr, t0.Add(10*time.Minute), mcContinue1)
	if x := tr[devC1]; x.Unit != "" || x.Bytes != 45120 || x.Size != 101588 || x.Percent != 44 {
		t.Errorf("own bytes: %+v", x.Transfer)
	}
	ch := tr.Apply(TransferEvent{Kind: "sent", AP: "3014F711A000040000000A02", Next: 30, Size: 50}, t0.Add(11*time.Minute))
	if len(ch) != 1 || ch[0] != devC2 {
		t.Errorf("sent with one device's bytes known changed %v", ch)
	}
	applyLines(t, tr, t0.Add(12*time.Minute), mcContinue2)
	if x := tr[devC2]; x.Bytes != 44880 || x.Percent != 44 || x.Unit != "" {
		t.Errorf("second device: %+v", x.Transfer)
	}
	// the duty-cycle pause: both devices, since when, why; the threshold line itself is no event
	pausedAt := t0.Add(30 * time.Minute)
	applyLines(t, tr, pausedAt, mcSuspended)
	for _, a := range []string{devC1, devC2} {
		if x := tr[a]; x.State != TransferPaused || x.Reason != "duty-cycle" || !x.PausedSince.Equal(pausedAt) || x.Percent != 44 {
			t.Errorf("%s paused: %+v", a, x.Transfer)
		}
	}
	// a second suspended line keeps the first time
	applyLines(t, tr, pausedAt.Add(time.Minute), mcSuspended)
	if x := tr[devC1]; !x.PausedSince.Equal(pausedAt) {
		t.Errorf("repeated suspend moved the time: %+v", x.Transfer)
	}
	// counted as running, for the LED and the summary
	if c := countTransfers([]DeviceStatus{{Device: dev("HmIP-RF", devC1, "DELIVER_FIRMWARE_IMAGE"), Transfer: &Transfer{State: TransferPaused}}}); c.Running != 1 {
		t.Errorf("paused counts: %+v", c)
	}
	if !transferring(DeviceStatus{Device: dev("HmIP-RF", devC1, "UP_TO_DATE"), Transfer: &Transfer{State: TransferPaused}}) {
		t.Error("a paused transfer is on its way")
	}
	resumedAt := pausedAt.Add(25 * time.Minute)
	applyLines(t, tr, resumedAt, mcContinues)
	for _, a := range []string{devC1, devC2} {
		if x := tr[a]; x.State != TransferRunning || x.Reason != "" || !x.PausedSince.IsZero() || x.Idle < 25*time.Minute {
			t.Errorf("%s continued: %+v idle %v", a, x.Transfer, x.Idle)
		}
	}
	// a progress line also ends a pause
	applyLines(t, tr, resumedAt.Add(time.Hour), mcSuspended)
	tr.Apply(TransferEvent{Kind: "progress", Addresses: []string{devC1}, Next: 60000, Size: 101588}, resumedAt.Add(time.Hour+10*time.Minute))
	if x := tr[devC1]; x.State != TransferRunning || x.Reason != "" || x.Idle < 35*time.Minute {
		t.Errorf("progress after a pause: %+v idle %v", x.Transfer, x.Idle)
	}
	// an abort during a pause: interrupted, the pause's reason gone
	applyLines(t, tr, resumedAt.Add(2*time.Hour), mcSuspended)
	tr.Apply(TransferEvent{Kind: "aborted", Addresses: []string{devC2}}, resumedAt.Add(2*time.Hour+time.Minute))
	if x := tr[devC2]; x.State != TransferInterrupted || x.Reason != "" || !x.PausedSince.IsZero() {
		t.Errorf("aborted while paused: %+v", x.Transfer)
	}
}

func eta(e transferEntry) int64 {
	if p := e.eta(); p != nil {
		return *p
	}
	return -1
}

// The estimate: the rate over the last hour of progress lines, the pause left out.
func TestTransferETA(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	prog := func(tr Transfers, n int64, at time.Time) {
		tr.Apply(TransferEvent{Kind: "progress", Addresses: []string{devC1}, Next: n, Size: 99540}, at)
	}
	pause := func(tr Transfers, kind string, at time.Time) {
		tr.Apply(TransferEvent{Kind: kind, Addresses: []string{devC1}}, at)
	}

	tr := Transfers{}
	pause(tr, "started", t0)
	if eta(tr[devC1]) != -1 {
		t.Error("no progress line: unknown")
	}
	prog(tr, 0, t0)
	if eta(tr[devC1]) != -1 {
		t.Error("one progress line: unknown")
	}
	prog(tr, 3000, t0.Add(30*time.Minute))
	// 3000 bytes in 30 min = 100 B/min; 96540 left = 965.4 min
	if got := eta(tr[devC1]); got != 57924 {
		t.Errorf("two lines: %d", got)
	}
	// a suspension of 25 minutes, then 3000 more bytes 50 minutes after the last line: the
	// window (an hour before the newest) holds the 30-min line and the newest; 25 minutes of
	// running for 3000 bytes = 120 B/min, 93540 left = 779.5 min
	pause(tr, "suspended", t0.Add(40*time.Minute))
	if eta(tr[devC1]) != -1 {
		t.Error("paused: unknown")
	}
	pause(tr, "continues", t0.Add(65*time.Minute))
	prog(tr, 6000, t0.Add(80*time.Minute))
	if got := eta(tr[devC1]); got != 46770 {
		t.Errorf("with a pause: %d (the pause counted?)", got)
	}
	// the API carries it; null when unknown
	if p := tr[devC1].public(); p.ETASeconds == nil || *p.ETASeconds != 46770 {
		t.Errorf("public: %+v", p)
	}
	b, _ := json.Marshal(tr[devC1].public())
	if !strings.Contains(string(b), `"eta_seconds":46770`) || strings.Contains(string(b), "samples") || strings.Contains(string(b), "idle") {
		t.Errorf("API JSON: %s", b)
	}
	pause(tr, "aborted", t0.Add(90*time.Minute))
	b, _ = json.Marshal(tr[devC1].public())
	if !strings.Contains(string(b), `"eta_seconds":null`) {
		t.Errorf("interrupted: %s", b)
	}

	// an interruption is left out like a pause
	tr = Transfers{}
	prog(tr, 0, t0)
	pause(tr, "aborted", t0.Add(10*time.Minute))
	pause(tr, "continues", t0.Add(40*time.Minute))
	prog(tr, 3000, t0.Add(60*time.Minute))
	if got := eta(tr[devC1]); got != 57924 {
		t.Errorf("with an interruption: %d", got)
	}

	// a position before the last starts the rate over
	prog(tr, 1000, t0.Add(70*time.Minute))
	if eta(tr[devC1]) != -1 {
		t.Error("after a restart from an earlier position: unknown")
	}
	// no movement: unknown
	prog(tr, 1000, t0.Add(80*time.Minute))
	if eta(tr[devC1]) != -1 {
		t.Error("no movement: unknown")
	}

	// the window: lines older than an hour before the newest are not read when two newer exist
	tr = Transfers{}
	prog(tr, 0, t0)                         // slow start: 0 → 600 in 2 h
	prog(tr, 600, t0.Add(2*time.Hour))      // …
	prog(tr, 3600, t0.Add(150*time.Minute)) // 3000 in 30 min
	prog(tr, 6600, t0.Add(3*time.Hour))     // 3000 in 30 min
	// 6000 bytes in 60 min = 100 B/min; 92940 left
	if got := eta(tr[devC1]); got != 55764 {
		t.Errorf("window: %d", got)
	}
	// done: unknown
	prog(tr, 99540, t0.Add(4*time.Hour))
	if x := tr[devC1]; x.State != TransferDone || eta(x) != -1 {
		t.Errorf("done: %+v", x.Transfer)
	}
}

// The samples and the handler are kept in the state file; an entry of a file from before B-80
// loads as it was.
func TestTransferEntryPersisted(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	tr := Transfers{}
	applyLines(t, tr, t0, mcStarted, mcInit)
	applyLines(t, tr, t0.Add(30*time.Minute), mcContinue1)
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	var back Transfers
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back[devC1].same(tr[devC1]) || eta(back[devC1]) != eta(tr[devC1]) || eta(back[devC1]) <= 0 {
		t.Errorf("round trip: %+v\nwant %+v", back[devC1], tr[devC1])
	}
	var old Transfers
	if err := json.Unmarshal([]byte(`{"00010000000A10":{"state":"running","percent":42,"bytes":41806,"size":99540,"since":"2026-10-14T13:15:35Z","updated":"2026-10-14T16:15:35Z"}}`), &old); err != nil {
		t.Fatal(err)
	}
	if x := old["00010000000A10"]; x.Percent != 42 || x.State != TransferRunning || x.Multicast {
		t.Errorf("old entry: %+v", x)
	}
	// a sent line reaches an old multicast entry without an AP
	old2 := Transfers{devC1: transferEntry{Transfer: Transfer{State: TransferRunning, Percent: -1}, Multicast: true}}
	if ch := old2.Apply(TransferEvent{Kind: "sent", AP: "3014F711A000040000000A02", Next: 5, Size: 50}, t0); len(ch) != 1 {
		t.Errorf("old multicast entry: %v", ch)
	}
}
