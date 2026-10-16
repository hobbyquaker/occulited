package firmware

import (
	"testing"
	"time"
)

// The journal lines as hmipserver wrote them on the lab (occulited task 44), the identifiers
// invented: the access point 3014F711A000040000000A02, the devices 3014F711A000010000000A10 and
// …A11 (addresses 00010000000A10, 00010000000A11).
const (
	lineStarted   = "de.eq3.cbcs.server.core.otau.DeviceBackgroundUpdateSubsystem [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU RF Handler 84de0cdb-bb8e-4bff-91f9-e03fe3e2df2d started for device(s) [3014F711A000010000000A10, 3014F711A000010000000A11]"
	lineContinues = "de.eq3.cbcs.server.core.otau.DeviceBackgroundUpdateSubsystem [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU RF Handler 84de0cdb-bb8e-4bff-91f9-e03fe3e2df2d continues for device(s) [3014F711A000010000000A10]"
	lineAborted   = "de.eq3.cbcs.server.core.otau.DeviceBackgroundUpdateSubsystem [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU RF Handler 84de0cdb-bb8e-4bff-91f9-e03fe3e2df2d aborted for device(s) [3014F711A000010000000A10]"
	lineInit      = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerUnicastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU Transaction: Device 3014F711A000010000000A10 acknowledged init. Current 0 - next Expected 0 of image size 99540"
	lineFrames    = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerUnicastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: Event: Device 3014F711A000010000000A10 acknowledged update frame(s). Current 0 - next Expected 30 of image size 99540"
	lineContinue  = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerUnicastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU Transaction: Device 3014F711A000010000000A10 acknowledged continue. Current 390 - next Expected 420 of image size 99540"
	lineMissed    = "de.eq3.cbcs.server.core.otau.handler.EventListenerUnicastHandler [vert.x-eventloop-thread-3] AP 3014F711A000040000000A02: OTAU Transaction: Device 3014F711A000010000000A11 missed update frame(s). Current 0 - next Expected 14310 of image size 55600"
	lineUnreach   = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerUnicastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: Backend Notification: Device 3014F711A000010000000A10 is unreachable"
	lineCannot    = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerUnicastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: OTAU Transaction: Device 3014F711A000010000000A10 cannot continue update. Expected 0"
	lineTimeout   = "de.eq3.cbcs.server.core.otau.handler.PermanentListenerUnicastHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: Backend Notification: TIMEOUT. Caller otau.interval.response Request -1"
	lineWatchdog  = "de.eq3.cbcs.server.core.otau.handler.AbstractUpdateHandler [vert.x-eventloop-thread-4] AP 3014F711A000040000000A02: Watchdog detected missing progress, moving handler to state unknown - errorcount 1"
	lineQueue     = "de.eq3.cbcs.server.core.otau.DeviceBackgroundUpdateSubsystem [vert.x-eventloop-thread-4] SYSTEM: 1 Accesspoints in RF Queue"
	lineScheduler = "de.eq3.cbcs.server.core.otau.DeviceBackgroundUpdateSubsystem [vert.x-eventloop-thread-3] AP 3014F711A000040000000A02: Device 3014F711A000010000000A10 is not reachable. Skip it for updates."
	lineLoader    = "de.eq3.cbcs.server.core.otau.util.FirmwareLoaderFileSystem [vert.x-worker-thread-3] SYSTEM: Firmware update directory is set to /etc/config/firmware"
	lineOther     = "de.eq3.cbcs.server.core.internal.InclusionHandling [vert.x-eventloop-thread-2] Device 3014F711A000010000000A10 acknowledged init. Current 0 - next Expected 0 of image size 1"
)

func TestParseTransferLine(t *testing.T) {
	for _, c := range []struct {
		line string
		want TransferEvent
		ok   bool
	}{
		{lineStarted, TransferEvent{Kind: "started", Addresses: []string{"00010000000A10", "00010000000A11"}}, true},
		{lineContinues, TransferEvent{Kind: "continues", Addresses: []string{"00010000000A10"}}, true},
		{lineAborted, TransferEvent{Kind: "aborted", Addresses: []string{"00010000000A10"}}, true},
		{lineInit, TransferEvent{Kind: "progress", Addresses: []string{"00010000000A10"}, Next: 0, Size: 99540}, true},
		{lineFrames, TransferEvent{Kind: "progress", Addresses: []string{"00010000000A10"}, Next: 30, Size: 99540}, true},
		{lineContinue, TransferEvent{Kind: "progress", Addresses: []string{"00010000000A10"}, Next: 420, Size: 99540}, true},
		{lineMissed, TransferEvent{Kind: "progress", Addresses: []string{"00010000000A11"}, Next: 14310, Size: 55600}, true},
		{lineUnreach, TransferEvent{Kind: "unreachable", Addresses: []string{"00010000000A10"}}, true},
		// the lines that say nothing about the progress
		{lineCannot, TransferEvent{}, false},
		{lineTimeout, TransferEvent{}, false},
		{lineWatchdog, TransferEvent{}, false},
		{lineQueue, TransferEvent{}, false},
		// a user's LOGLEVEL_HMIP=debug adds the scheduler's rounds
		{lineScheduler, TransferEvent{}, false},
		{lineLoader, TransferEvent{}, false},
		// a logger outside the transfer's package, whatever it says
		{lineOther, TransferEvent{}, false},
		{"", TransferEvent{}, false},
		{"Current 0 - next Expected 0 of image size 1", TransferEvent{}, false},
		// a line without hmipserver's prefix is read by its text alone (tolerance)
		{"Device 3014F711A000010000000A11 acknowledged update frame(s). Current 2970 - next Expected 3000 of image size 55600", TransferEvent{Kind: "progress", Addresses: []string{"00010000000A11"}, Next: 3000, Size: 55600}, true},
		// a progress line without the size (a shape that may come) still names the byte
		{"Device 3014F711A000010000000A11 acknowledged end. Current 55570 - next Expected 55600", TransferEvent{Kind: "progress", Addresses: []string{"00010000000A11"}, Next: 55600}, true},
	} {
		ev, ok := ParseTransferLine(c.line)
		if ok != c.ok {
			t.Errorf("%q: ok %v, want %v", c.line, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if ev.Kind != c.want.Kind || ev.Next != c.want.Next || ev.Size != c.want.Size || len(ev.Addresses) != len(c.want.Addresses) {
			t.Errorf("%q: %+v, want %+v", c.line, ev, c.want)
			continue
		}
		for i := range ev.Addresses {
			if ev.Addresses[i] != c.want.Addresses[i] {
				t.Errorf("%q: address %d %s, want %s", c.line, i, ev.Addresses[i], c.want.Addresses[i])
			}
		}
	}
}

func TestDeviceAddress(t *testing.T) {
	for in, want := range map[string]string{
		"3014F711A000010000000A10":   "00010000000A10",
		"00010000000A10":             "00010000000A10",
		" 3014F711A000010000000A10 ": "00010000000A10",
		"":                           "",
		"[]":                         "",
		"00010000000A10:0":           "",
	} {
		if got := DeviceAddress(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A transfer as the lab showed it: started, the init and frame lines, the device silent, the
// handler aborted, continued later from where it stood, and the last byte acknowledged.
func TestTransfersApply(t *testing.T) {
	tr := Transfers{}
	t0 := time.Date(2026, 10, 14, 13, 15, 35, 0, time.UTC)
	apply := func(line string, at time.Time) []string {
		t.Helper()
		ev, ok := ParseTransferLine(line)
		if !ok {
			t.Fatalf("not a transfer line: %q", line)
		}
		return tr.Apply(ev, at)
	}
	if ch := apply(lineStarted, t0); len(ch) != 2 {
		t.Fatalf("started: changed %v", ch)
	}
	for _, a := range []string{"00010000000A10", "00010000000A11"} {
		if x := tr[a]; x.State != TransferRunning || x.Percent != -1 || !x.Since.Equal(t0) {
			t.Errorf("%s after start: %+v", a, x)
		}
	}
	apply(lineInit, t0)
	if x := tr["00010000000A10"]; x.Percent != 0 || x.Size != 99540 || x.Bytes != 0 {
		t.Errorf("after init: %+v", x)
	}
	apply(lineFrames, t0.Add(time.Second))
	apply(lineContinue, t0.Add(4*time.Minute))
	if x := tr["00010000000A10"]; x.Percent != 0 || x.Bytes != 420 || !x.Updated.Equal(t0.Add(4*time.Minute)) || !x.Since.Equal(t0) {
		t.Errorf("after 420 bytes: %+v", x)
	}
	// half way: the percent is the whole number
	ev := TransferEvent{Kind: "progress", Addresses: []string{"00010000000A10"}, Next: 49800, Size: 99540}
	tr.Apply(ev, t0.Add(7*time.Hour))
	if x := tr["00010000000A10"]; x.Percent != 50 || x.State != TransferRunning {
		t.Errorf("half way: %+v", x)
	}
	// the device goes silent: the reason, then the abort; the percent stays
	apply(lineUnreach, t0.Add(8*time.Hour))
	if x := tr["00010000000A10"]; x.Reason != "unreachable" || x.State != TransferRunning {
		t.Errorf("unreachable: %+v", x)
	}
	apply(lineAborted, t0.Add(8*time.Hour+time.Minute))
	if x := tr["00010000000A10"]; x.State != TransferInterrupted || x.Percent != 50 || x.Reason != "unreachable" {
		t.Errorf("aborted: %+v", x)
	}
	// the same line again changes nothing
	if ch := apply(lineAborted, t0.Add(8*time.Hour+time.Minute)); len(ch) != 0 {
		t.Errorf("a repeated abort changed %v", ch)
	}
	// continued: running again, the reason gone, the percent kept until the next line
	apply(lineContinues, t0.Add(9*time.Hour))
	if x := tr["00010000000A10"]; x.State != TransferRunning || x.Percent != 50 || x.Reason != "" || !x.Since.Equal(t0) {
		t.Errorf("continues: %+v", x)
	}
	// the end
	tr.Apply(TransferEvent{Kind: "progress", Addresses: []string{"00010000000A10"}, Next: 99540, Size: 99540}, t0.Add(15*time.Hour))
	if x := tr["00010000000A10"]; x.State != TransferDone || x.Percent != 100 {
		t.Errorf("done: %+v", x)
	}
	// a new start after a done transfer is a new one
	apply(lineStarted, t0.Add(20*time.Hour))
	if x := tr["00010000000A10"]; x.State != TransferRunning || x.Percent != -1 || !x.Since.Equal(t0.Add(20*time.Hour)) {
		t.Errorf("restarted: %+v", x)
	}
	// the other device moved by its own line only
	if x := tr["00010000000A11"]; x.Percent != -1 {
		t.Errorf("the other device: %+v", x)
	}
	apply(lineMissed, t0.Add(3*time.Hour))
	if x := tr["00010000000A11"]; x.Percent != 25 || x.Bytes != 14310 || x.Size != 55600 {
		t.Errorf("missed frames carry the position: %+v", x)
	}
	// a percent never above 100, and a size the line does not say keeps the one known
	tr.Apply(TransferEvent{Kind: "progress", Addresses: []string{"00010000000A11"}, Next: 60000}, t0.Add(4*time.Hour))
	if x := tr["00010000000A11"]; x.Percent != 100 || x.Size != 55600 || x.State != TransferDone {
		t.Errorf("over the end: %+v", x)
	}
}

func TestCountTransfers(t *testing.T) {
	devs := []DeviceStatus{
		{Device: dev("HmIP-RF", "00010000000A10", "DELIVER_FIRMWARE_IMAGE"), Transfer: &Transfer{State: TransferRunning, Percent: 42}},
		{Device: dev("HmIP-RF", "00010000000A11", "DELIVER_FIRMWARE_IMAGE")},
		{Device: dev("HmIP-RF", "0001D0000000B1", "DELIVER_FIRMWARE_IMAGE"), Transfer: &Transfer{State: TransferInterrupted, Percent: 10}},
		{Device: dev("HmIP-RF", "0001D0000000B2", "NEW_FIRMWARE_AVAILABLE")},
		{Device: dev("HmIP-RF", "00120000000D01", "READY_FOR_UPDATE")},
		{Device: dev("HmIP-RF", "00120000000D02", "PERFORMING_UPDATE")},
		{Device: dev("HmIP-RF", "00160000000C01", "UP_TO_DATE")},
		{Device: dev("BidCos-RF", "JEQ9000001", "")},
	}
	got := countTransfers(devs)
	want := TransferCounts{Running: 2, Queued: 1, Interrupted: 1, Ready: 1, Installing: 1}
	if got != want {
		t.Errorf("%+v, want %+v", got, want)
	}
}

// occulited task 61: the interfaces with a firmware on its way - running, interrupted (hmipserver
// suspended it at its duty-cycle threshold) or being delivered; a queued or ready one is not
func TestTransferringInterfaces(t *testing.T) {
	devs := []DeviceStatus{
		{Device: dev("HmIP-RF", "00010000000A10", "UP_TO_DATE"), Transfer: &Transfer{State: TransferInterrupted, Percent: 10}},
		{Device: dev("BidCos-RF", "JEQ9000001", "READY_FOR_UPDATE")},
		{Device: dev("HmIPW", "00160000000C01", "NEW_FIRMWARE_AVAILABLE")},
	}
	if got := TransferringInterfaces(devs); len(got) != 1 || !got["HmIP-RF"] {
		t.Errorf("interrupted: %v", got)
	}
	devs[0] = DeviceStatus{Device: dev("HmIP-RF", "00010000000A10", "DELIVER_FIRMWARE_IMAGE")}
	if got := TransferringInterfaces(devs); !got["HmIP-RF"] || got["BidCos-RF"] || got["HmIPW"] {
		t.Errorf("delivering: %v", got)
	}
	devs[0] = DeviceStatus{Device: dev("HmIP-RF", "00010000000A10", "UP_TO_DATE")}
	if got := TransferringInterfaces(devs); len(got) != 0 {
		t.Errorf("none: %v", got)
	}
}
