package firmware

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The background firmware transfer to HmIP devices, as hmipserver reports it in its journal
// (occulited task 44). hmipserver's XML-RPC gives the transfer's state (FIRMWARE_UPDATE_STATE:
// NEW_FIRMWARE_AVAILABLE queued, DELIVER_FIRMWARE_IMAGE running or interrupted, READY_FOR_UPDATE
// transferred) but never a percent; the percent is in the lines its transfer loggers write, which
// openccu-lite's log4j2 template keeps at their levels. The lines are not an API: the parser
// matches them loosely and ignores whatever it does not know, so a newer hmipserver degrades the
// page to the states alone, never to an error. Shapes seen in the journal (the device id is the
// SGTIN; its last 14 characters are the device's RPC address):
//
//	AP <ap>: OTAU RF Handler <uuid> started for device(s) [<sgtin>, <sgtin>]
//	AP <ap>: OTAU RF Handler <uuid> continues for device(s) [<sgtin>]
//	AP <ap>: OTAU RF Handler <uuid> aborted for device(s) [<sgtin>]
//	AP <ap>: OTAU Transaction: Device <sgtin> acknowledged init. Current 0 - next Expected 0 of image size 99540
//	AP <ap>: Event: Device <sgtin> acknowledged update frame(s). Current 0 - next Expected 30 of image size 99540
//	AP <ap>: OTAU Transaction: Device <sgtin> acknowledged continue. Current 390 - next Expected 420 of image size 99540
//	AP <ap>: OTAU Transaction: Device <sgtin> missed update frame(s). Current 0 - next Expected 14310 of image size 55600
//	AP <ap>: Backend Notification: Device <sgtin> is unreachable
//
// "next Expected" is the byte the device expects next, "image size" the image's size: their
// ratio is the progress. The frame line comes every hundred frames (a frame was 30 bytes on the
// lab), the others at every start, continuation and wake-up of the device.
//
// Several devices of one type get one multicast handler (PermanentListenerMulticastHandler,
// occulited B-80), which words its lines differently - no "next" - and counts what it sent for all
// of its devices together, in blocks:
//
//	AP <ap>: OTAU Transaction: Device <sgtin> acknowledged continue. Current 1200 - Expected 1200 of image size 99540
//	AP <ap>: Backend Notification: Sent 12 of image size 50
//
// and the subsystem suspends a handler while the access point's duty cycle is above its threshold
// (50 %), continuing it 20-30 minutes later (occulited task 72):
//
//	AP <ap>: AccessPoint duty cycle exceeds threshold of 50.0 will suspend handler
//	AP <ap>: OTAU RF Handler <uuid> suspended for device(s) [<sgtin>, <sgtin>]
//	AP <ap>: OTAU RF Handler <uuid> continues for device(s) [<sgtin>, <sgtin>]

// Transfer states.
const (
	// TransferRunning: hmipserver's handler is transferring to the device (or waiting for a
	// battery device to wake up; Updated says when the last frame went).
	TransferRunning = "running"
	// TransferInterrupted: the handler gave up for now (the device did not answer, hmipserver
	// restarted); it continues by itself later, from where it stopped.
	TransferInterrupted = "interrupted"
	// TransferDone: the last byte was acknowledged; the interface's state moves to
	// READY_FOR_UPDATE.
	TransferDone = "done"
	// TransferPaused: hmipserver suspended the handler at the access point's duty-cycle threshold
	// (occulited task 72; Reason "duty-cycle"); it continues by itself.
	TransferPaused = "paused"
)

// UnitBlocks marks a progress counted in the multicast handler's blocks, not bytes (occulited B-80).
const UnitBlocks = "blocks"

// etaWindow is how far back the time-left estimate reads the progress (B-80: not from the start -
// the duty-cycle pauses and the watchdog's restarts make the rate jumpy).
const etaWindow = 60 * time.Minute

// The estimate rests on enough progress or on nothing (occulited B-112: the first two lines of a
// transfer - "acknowledged init … Expected 0" and the first frame's "Expected 30" - come within a
// second, and the burst read as "15 min" for 87 kB). Measured on the lab (2026-10-14): the regular
// progress line comes every 100 frames of 30 bytes = 3,000 bytes, ~22 min apart at ~136 bytes a
// minute; every other line (init, continue after a watchdog, missed after a resume) comes in
// clusters seconds apart. So the oldest and the newest sample the rate rests on must be one
// regular interval apart in bytes (one block in the multicast unit) and 10 minutes of active time
// - less than one interval takes even at the ceiling's rate, so it rejects only the clusters.
const (
	etaMinBytes  int64 = 3000
	etaMinBlocks int64 = 1
	etaMinActive       = 10 * time.Minute
	// etaMaxRate bounds the rate in bytes per minute: about twice the measured 110-136, and above
	// the ~207 that eQ-3's "8 to 42 hours" allows for the largest image seen (99,540 bytes over 8 h)
	// - a real fast device is never slowed down, a burst can never say minutes. The blocks unit has
	// no ceiling (a block's size in bytes is unknown; the minimums bound it to a block per 10 min).
	etaMaxRate = 250.0
)

// sampleKeep is how long progress samples are kept for the estimate.
const sampleKeep = 3 * time.Hour

// Transfer is one device's background transfer as the journal told it.
type Transfer struct {
	State string `json:"state"`
	// Percent is Bytes of Size, whole numbers, -1 while no progress line was seen yet
	Percent int   `json:"percent"`
	Bytes   int64 `json:"bytes,omitempty"`
	Size    int64 `json:"size,omitempty"`
	// Unit is "blocks" when Bytes and Size are the multicast handler's block count (B-80), "" for bytes
	Unit string `json:"unit,omitempty"`
	// Since is the first line seen for this transfer, Updated the last
	Since   time.Time `json:"since"`
	Updated time.Time `json:"updated"`
	// ProgressAt is the last progress line's time (B-80)
	ProgressAt time.Time `json:"progress_at,omitzero"`
	// PausedSince is when hmipserver suspended the handler, while State is paused (task 72)
	PausedSince time.Time `json:"paused_since,omitzero"`
	// Reason: why it waits or was interrupted, when the journal said - "unreachable", "duty-cycle"
	Reason string `json:"reason,omitempty"`
	// ETASeconds is the estimated time left (B-80): from the progress over the last hour, not
	// counting pauses and interruptions, resting on at least one regular progress interval and
	// bounded by what a transfer can do (B-112); null while it is not known (too little progress,
	// paused, interrupted, done)
	ETASeconds *int64 `json:"eta_seconds"`
}

// progressSample is one progress line, for the time-left estimate; Idle is the time the transfer
// had spent paused or interrupted up to it.
type progressSample struct {
	At    time.Time     `json:"at"`
	Bytes int64         `json:"bytes"`
	Idle  time.Duration `json:"idle,omitempty"`
}

// transferEntry is what the service keeps per device: the Transfer the API shows, the handler it
// belongs to, and the progress samples - kept in the state file, never in the API.
type transferEntry struct {
	Transfer
	AP        string `json:"ap,omitempty"`
	Handler   string `json:"handler,omitempty"`
	Multicast bool   `json:"multicast,omitempty"`
	// Idle is the time spent paused or interrupted so far, IdleFrom when the current pause began
	Idle     time.Duration    `json:"idle,omitempty"`
	IdleFrom time.Time        `json:"idle_from,omitzero"`
	Samples  []progressSample `json:"samples,omitempty"`
}

// TransferEvent is what one journal line means.
type TransferEvent struct {
	// Kind: started, continues, suspended, aborted, progress, unreachable, sent
	Kind string
	// Addresses are the devices' RPC addresses (the SGTINs' last 14 characters); none for sent,
	// which is the multicast handler's on the access point AP
	Addresses []string
	// AP is the access point the line names ("AP <id>: "), Handler the handler's id
	AP, Handler string
	// progress and sent: the next byte (block) expected or sent and the image size (0 when the
	// line does not say)
	Next, Size int64
}

var (
	// the logger name and the thread hmipserver puts before the text: "<logger> [<thread>] "
	journalPrefixRe = regexp.MustCompile(`^(\S+) \[[^\]]*\] `)
	apRe            = regexp.MustCompile(`\bAP ([^\s:]+):`)
	handlerRe       = regexp.MustCompile(`Handler (\S+) (started|continues|suspended|aborted) for device\(s\) \[([^\]]*)\]`)
	// the unicast handlers say "next Expected", the multicast one "Expected" (B-80)
	progressRe    = regexp.MustCompile(`Device (\S+) (?:acknowledged|missed) [^.]*\. Current (\d+) - (?:next )?Expected (\d+)(?: of image size (\d+))?`)
	sentRe        = regexp.MustCompile(`Backend Notification: Sent (\d+) of image size (\d+)`)
	unreachableRe = regexp.MustCompile(`Device (\S+) is unreachable`)
)

// ParseTransferLine reads one journal MESSAGE of hmipserver; ok is false for every line that is
// not one of the transfer's. A line of a logger outside the transfer's package is never taken,
// however much it looks like one.
func ParseTransferLine(msg string) (ev TransferEvent, ok bool) {
	text := msg
	if m := journalPrefixRe.FindStringSubmatch(msg); m != nil {
		if !strings.Contains(strings.ToLower(m[1]), "otau") {
			return TransferEvent{}, false
		}
		text = msg[len(m[0]):]
	}
	var ap string
	if m := apRe.FindStringSubmatch(text); m != nil {
		ap = m[1]
	}
	if m := handlerRe.FindStringSubmatch(text); m != nil {
		var addrs []string
		for _, id := range strings.Split(m[3], ",") {
			if a := DeviceAddress(strings.TrimSpace(id)); a != "" {
				addrs = append(addrs, a)
			}
		}
		if len(addrs) == 0 {
			return TransferEvent{}, false
		}
		return TransferEvent{Kind: m[2], Addresses: addrs, AP: ap, Handler: m[1]}, true
	}
	if m := progressRe.FindStringSubmatch(text); m != nil {
		a := DeviceAddress(m[1])
		if a == "" {
			return TransferEvent{}, false
		}
		next, _ := strconv.ParseInt(m[3], 10, 64)
		var size int64
		if m[4] != "" {
			size, _ = strconv.ParseInt(m[4], 10, 64)
		}
		return TransferEvent{Kind: "progress", Addresses: []string{a}, AP: ap, Next: next, Size: size}, true
	}
	if m := sentRe.FindStringSubmatch(text); m != nil {
		next, _ := strconv.ParseInt(m[1], 10, 64)
		size, _ := strconv.ParseInt(m[2], 10, 64)
		if size <= 0 {
			return TransferEvent{}, false
		}
		return TransferEvent{Kind: "sent", AP: ap, Next: next, Size: size}, true
	}
	if m := unreachableRe.FindStringSubmatch(text); m != nil {
		if a := DeviceAddress(m[1]); a != "" {
			return TransferEvent{Kind: "unreachable", Addresses: []string{a}, AP: ap}, true
		}
	}
	return TransferEvent{}, false
}

// DeviceAddress is the RPC address of the device id a transfer line names: the SGTIN's last 14
// characters (3014F711A0 + the address, as seen), a shorter id as it is; "" for nothing usable.
func DeviceAddress(id string) string {
	id = strings.TrimSpace(id)
	if len(id) == 24 {
		id = id[10:]
	}
	if id == "" || strings.ContainsAny(id, " []:") {
		return ""
	}
	return id
}

// Transfers keeps the last known transfer per device address. Not safe for concurrent use; the
// service holds it under its own mutex.
type Transfers map[string]transferEntry

// Apply folds one event in at time at. It returns the addresses whose transfer changed.
func (t Transfers) Apply(ev TransferEvent, at time.Time) (changed []string) {
	addrs := ev.Addresses
	if ev.Kind == "sent" {
		addrs = t.multicastOn(ev.AP)
	}
	for _, a := range addrs {
		cur, had := t[a]
		next := cur
		next.Samples = append([]progressSample(nil), cur.Samples...)
		if !had {
			next = transferEntry{Transfer: Transfer{State: TransferRunning, Percent: -1, Since: at}}
		}
		switch ev.Kind {
		case "started":
			// a start after a transfer that was done or dropped is a new one
			if !had || cur.State == TransferDone {
				next = transferEntry{Transfer: Transfer{State: TransferRunning, Percent: -1, Since: at}}
			}
			next.resume(at)
			next.handler(ev)
		case "continues":
			next.resume(at)
			next.handler(ev)
		case "suspended":
			next.handler(ev)
			if next.State != TransferPaused {
				next.idle(at)
				next.State, next.Reason, next.PausedSince = TransferPaused, "duty-cycle", at
			}
		case "aborted":
			next.idle(at)
			next.State, next.PausedSince = TransferInterrupted, time.Time{}
			if next.Reason == "duty-cycle" {
				next.Reason = ""
			}
		case "unreachable":
			next.Reason = "unreachable"
		case "progress":
			if next.Unit == UnitBlocks {
				// the device's own bytes win over the handler's blocks from now on
				next.Unit, next.Size, next.Bytes, next.Samples = "", 0, 0, nil
			}
			next.progress(ev.Next, ev.Size, at)
		case "sent":
			if next.Unit != UnitBlocks && next.Size > 0 {
				continue // the device's own bytes are known
			}
			if next.Unit != UnitBlocks {
				next.Samples = nil
			}
			next.Unit = UnitBlocks
			next.progress(ev.Next, ev.Size, at)
		default:
			continue
		}
		next.Updated = at
		if had && next.same(cur) {
			continue
		}
		t[a] = next
		changed = append(changed, a)
	}
	sort.Strings(changed)
	return changed
}

// multicastOn are the devices of the multicast handler on the access point (any access point when
// the line names none, or the entry was kept from before B-80) whose transfer is on its way.
func (t Transfers) multicastOn(ap string) []string {
	var out []string
	for a, e := range t {
		if !e.Multicast || e.State == TransferDone {
			continue
		}
		if ap != "" && e.AP != "" && e.AP != ap {
			continue
		}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func (e *transferEntry) handler(ev TransferEvent) {
	if ev.AP != "" {
		e.AP = ev.AP
	}
	if ev.Handler != "" {
		e.Handler = ev.Handler
	}
	if ev.Kind == "started" || ev.Kind == "continues" || ev.Kind == "suspended" {
		e.Multicast = len(ev.Addresses) > 1
	}
}

// idle marks the start of a pause or interruption (once).
func (e *transferEntry) idle(at time.Time) {
	if e.IdleFrom.IsZero() {
		e.IdleFrom = at
	}
}

// resume ends a pause or interruption: the transfer runs again, the time it stood counted.
func (e *transferEntry) resume(at time.Time) {
	if !e.IdleFrom.IsZero() {
		if d := at.Sub(e.IdleFrom); d > 0 {
			e.Idle += d
		}
		e.IdleFrom = time.Time{}
	}
	e.State, e.Reason, e.PausedSince = TransferRunning, "", time.Time{}
}

// progress takes a progress line: next of size (bytes, or blocks).
func (e *transferEntry) progress(next, size int64, at time.Time) {
	e.resume(at)
	if size > 0 {
		if e.Size > 0 && size != e.Size {
			e.Samples = nil
		}
		e.Size = size
	}
	e.Bytes = next
	if e.Size > 0 {
		p := int(e.Bytes * 100 / e.Size)
		if p > 100 {
			p = 100
		}
		if e.Unit == UnitBlocks && p > 99 {
			// a block sent is not a block acknowledged: done comes from the interface's state
			p = 99
		}
		e.Percent = p
	}
	if e.Unit != UnitBlocks && e.Size > 0 && e.Bytes >= e.Size {
		e.State = TransferDone
	}
	e.ProgressAt = at
	// a position before the last one is a new start: the rate starts over
	if n := len(e.Samples); n > 0 && (next < e.Samples[n-1].Bytes || at.Before(e.Samples[n-1].At)) {
		e.Samples = nil
	}
	e.Samples = append(e.Samples, progressSample{At: at, Bytes: next, Idle: e.Idle})
	keep := e.Samples[:0]
	for _, s := range e.Samples {
		if at.Sub(s.At) <= sampleKeep {
			keep = append(keep, s)
		}
	}
	if len(keep) > 64 {
		keep = keep[len(keep)-64:]
	}
	e.Samples = keep
}

// same reports whether nothing the API or the estimate reads differs.
func (e transferEntry) same(o transferEntry) bool {
	if e.Transfer != o.Transfer || e.AP != o.AP || e.Handler != o.Handler || e.Multicast != o.Multicast || e.Idle != o.Idle || !e.IdleFrom.Equal(o.IdleFrom) || len(e.Samples) != len(o.Samples) {
		return false
	}
	for i := range e.Samples {
		if e.Samples[i] != o.Samples[i] {
			return false
		}
	}
	return true
}

// eta is the estimated time left (B-80): the rate over the progress lines of the last hour before
// the newest, with the time the transfer stood paused or interrupted left out, applied to what is
// left after the newest line. The samples it rests on span at least one regular progress interval
// and ten active minutes (B-112): when the last hour does not, older samples are taken in, as far
// as they go - the estimate does not vanish for the twenty minutes after a pause - and the rate is
// bounded by what a transfer can do. Nil while it is not known: too little progress, a position
// that went back, no movement, paused, interrupted, done.
func (e transferEntry) eta() *int64 {
	if e.State != TransferRunning || e.Size <= 0 || len(e.Samples) < 2 {
		return nil
	}
	minMoved := etaMinBytes
	if e.Unit == UnitBlocks {
		minMoved = etaMinBlocks
	}
	last := e.Samples[len(e.Samples)-1]
	first := len(e.Samples) - 2
	span := func(i int) (active time.Duration, moved int64) {
		f := e.Samples[i]
		return last.At.Sub(f.At) - (last.Idle - f.Idle), last.Bytes - f.Bytes
	}
	// the last hour, then further back until the span is enough
	for first > 0 && last.At.Sub(e.Samples[first-1].At) <= etaWindow {
		first--
	}
	for active, moved := span(first); first > 0 && (active < etaMinActive || moved < minMoved); active, moved = span(first) {
		first--
	}
	active, moved := span(first)
	if active < etaMinActive || moved < minMoved {
		return nil
	}
	rate := float64(moved) / active.Minutes()
	if e.Unit != UnitBlocks && rate > etaMaxRate {
		rate = etaMaxRate
	}
	left := e.Size - last.Bytes
	if left < 0 {
		left = 0
	}
	secs := int64(float64(left) / rate * 60)
	return &secs
}

// public is the API's view of the entry.
func (e transferEntry) public() Transfer {
	t := e.Transfer
	t.ETASeconds = e.eta()
	return t
}

// Clone copies the map.
func (t Transfers) Clone() Transfers {
	out := make(Transfers, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}

// transferStates are the interface's states during which the journal's transfer entry is
// shown; in any other state (READY_FOR_UPDATE, UP_TO_DATE, an install) it is dropped.
func transferState(s string) bool {
	return s == "NEW_FIRMWARE_AVAILABLE" || s == "DELIVER_FIRMWARE_IMAGE"
}

// TransferCounts is GET /firmware's transfers summary.
type TransferCounts struct {
	// Running: devices whose firmware is being transferred (the journal's running or paused entry,
	// or the interface's DELIVER_FIRMWARE_IMAGE without one)
	Running int `json:"running"`
	// Queued: NEW_FIRMWARE_AVAILABLE - a deployed bundle waits for its turn
	Queued int `json:"queued"`
	// Interrupted: the journal said the handler gave up for now
	Interrupted int `json:"interrupted"`
	// Ready: READY_FOR_UPDATE - transferred, waiting for the install in the frontend
	Ready int `json:"ready"`
	// Installing: DO_UPDATE_PENDING or PERFORMING_UPDATE
	Installing int `json:"installing"`
}

func countTransfers(devs []DeviceStatus) TransferCounts {
	var c TransferCounts
	for _, d := range devs {
		switch {
		case d.Transfer != nil && d.Transfer.State == TransferInterrupted:
			c.Interrupted++
		case d.Transfer != nil && (d.Transfer.State == TransferRunning || d.Transfer.State == TransferPaused), d.UpdateState == "DELIVER_FIRMWARE_IMAGE":
			c.Running++
		case d.UpdateState == "NEW_FIRMWARE_AVAILABLE":
			c.Queued++
		case d.UpdateState == "READY_FOR_UPDATE":
			c.Ready++
		case d.UpdateState == "DO_UPDATE_PENDING", d.UpdateState == "PERFORMING_UPDATE", d.UpdateState == "LIVE_DELIVER_FIRMWARE_IMAGE":
			c.Installing++
		}
	}
	return c
}

// transferring is a device whose firmware is on its way: the journal's running, paused (hmipserver
// suspends it at its duty-cycle threshold) or interrupted transfer, or the interface's
// DELIVER_FIRMWARE_IMAGE.
func transferring(d DeviceStatus) bool {
	return d.Transfer != nil && (d.Transfer.State == TransferRunning || d.Transfer.State == TransferPaused || d.Transfer.State == TransferInterrupted) || d.UpdateState == "DELIVER_FIRMWARE_IMAGE"
}

// TransferringInterfaces are the interfaces on which a device's firmware is on its way (occulited
// task 61): the radio-load warning and the LED's duty-cycle levels take the higher threshold there.
func TransferringInterfaces(devs []DeviceStatus) map[string]bool {
	out := map[string]bool{}
	for _, d := range devs {
		if transferring(d) {
			out[d.Interface] = true
		}
	}
	return out
}
