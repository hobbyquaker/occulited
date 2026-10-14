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
)

// Transfer is one device's background transfer as the journal told it.
type Transfer struct {
	State string `json:"state"`
	// Percent is Bytes of Size, whole numbers, -1 while no progress line was seen yet
	Percent int   `json:"percent"`
	Bytes   int64 `json:"bytes,omitempty"`
	Size    int64 `json:"size,omitempty"`
	// Since is the first line seen for this transfer, Updated the last
	Since   time.Time `json:"since"`
	Updated time.Time `json:"updated"`
	// Reason: why it waits or was interrupted, when the journal said - "unreachable"
	Reason string `json:"reason,omitempty"`
}

// TransferEvent is what one journal line means.
type TransferEvent struct {
	// Kind: started, continues, aborted, progress, unreachable
	Kind string
	// Addresses are the devices' RPC addresses (the SGTINs' last 14 characters)
	Addresses []string
	// progress: the next byte expected and the image size (0 when the line does not say)
	Next, Size int64
}

var (
	// the logger name and the thread hmipserver puts before the text: "<logger> [<thread>] "
	journalPrefixRe = regexp.MustCompile(`^(\S+) \[[^\]]*\] `)
	handlerRe       = regexp.MustCompile(`Handler \S+ (started|continues|aborted) for device\(s\) \[([^\]]*)\]`)
	progressRe      = regexp.MustCompile(`Device (\S+) (?:acknowledged|missed) [^.]*\. Current (\d+) - next Expected (\d+)(?: of image size (\d+))?`)
	unreachableRe   = regexp.MustCompile(`Device (\S+) is unreachable`)
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
	if m := handlerRe.FindStringSubmatch(text); m != nil {
		var addrs []string
		for _, id := range strings.Split(m[2], ",") {
			if a := DeviceAddress(strings.TrimSpace(id)); a != "" {
				addrs = append(addrs, a)
			}
		}
		if len(addrs) == 0 {
			return TransferEvent{}, false
		}
		return TransferEvent{Kind: m[1], Addresses: addrs}, true
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
		return TransferEvent{Kind: "progress", Addresses: []string{a}, Next: next, Size: size}, true
	}
	if m := unreachableRe.FindStringSubmatch(text); m != nil {
		if a := DeviceAddress(m[1]); a != "" {
			return TransferEvent{Kind: "unreachable", Addresses: []string{a}}, true
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
type Transfers map[string]Transfer

// Apply folds one event in at time at. It returns the addresses whose transfer changed.
func (t Transfers) Apply(ev TransferEvent, at time.Time) (changed []string) {
	for _, a := range ev.Addresses {
		cur, had := t[a]
		next := cur
		if !had {
			next = Transfer{State: TransferRunning, Percent: -1, Since: at}
		}
		switch ev.Kind {
		case "started":
			// a start after a transfer that was done or dropped is a new one
			if !had || cur.State == TransferDone {
				next = Transfer{State: TransferRunning, Percent: -1, Since: at}
			}
			next.State, next.Reason = TransferRunning, ""
		case "continues":
			next.State, next.Reason = TransferRunning, ""
		case "aborted":
			next.State = TransferInterrupted
		case "unreachable":
			next.Reason = "unreachable"
		case "progress":
			if ev.Size > 0 {
				next.Size = ev.Size
			}
			next.Bytes = ev.Next
			if next.Size > 0 {
				p := int(next.Bytes * 100 / next.Size)
				if p > 100 {
					p = 100
				}
				next.Percent = p
			}
			if next.Size > 0 && next.Bytes >= next.Size {
				next.State = TransferDone
			} else {
				next.State = TransferRunning
			}
			next.Reason = ""
		default:
			continue
		}
		next.Updated = at
		if had && next == cur {
			continue
		}
		t[a] = next
		changed = append(changed, a)
	}
	sort.Strings(changed)
	return changed
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
	// Running: devices whose firmware is being transferred (the journal's running entry, or the
	// interface's DELIVER_FIRMWARE_IMAGE without one)
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
		case d.Transfer != nil && d.Transfer.State == TransferRunning, d.UpdateState == "DELIVER_FIRMWARE_IMAGE":
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
