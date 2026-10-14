package radio

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The HmIP security counter (openccu-lite task 299; eq-3/occu#134, OpenCCU/OpenCCU#4274).
//
// Every HmIP frame carries a counter the devices only accept going up. At each start hmipserver
// reads the radio module's counter ("Current Security Counter: N" in its journal) and computes a
// value from the wall clock and two numbers kept in the access point's file crRFD/data/<SGTIN>.ap -
// the time the access point was first connected and an offset - and writes it to the module when
// it is higher ("Update security counter to calculation: M"). As observed in the public issue, the
// comparison is done on the full value while the module takes its lower 32 bits: a computed value
// at or above 2^32 passes the check and lands in the module truncated - possibly below what the
// devices have seen, after which they drop every frame of the system as a replay until they are
// power-cycled or paired again. The same happens when a start with a clock far behind writes a
// small value first and the next start with the right clock a wrapped one.
//
// This file reads the two lines, reads the access point file's two numbers (as observed in the
// files: the layout is described at ParseAccessPoint), computes what the next start would write
// from the running clock, keeps a short history per access point on the userfs, and says what
// state the system is in: fine, near (the computed value has passed 2^31), wrapped (at or above
// 2^32: the check protects nothing any more) or backwards (the module's counter, or what the
// devices saw at an earlier start, is above what this start wrote). The prep step holds hmipserver
// on a wrapped or at-risk system while the clock is not trusted (the fork's clock gate timed out).

// The counter's bounds and hmipserver's tick, as the public issue describes them.
const (
	CounterWrap = uint64(1) << 32
	CounterNear = uint64(1) << 31
	counterTick = 300 * time.Millisecond
)

// The verdicts.
const (
	CounterFine      = "fine"
	CounterNearWrap  = "near"
	CounterWrapped   = "wrapped"
	CounterBackwards = "backwards"
)

// CounterStateFile keeps the history per access point on the userfs: written by the radio steps
// as root, read by the daemon for the Status page.
const CounterStateFile = "/usr/local/var/lib/occulite/hmip-security-counter.json"

// CounterHoldFile is the marker the prep step leaves while it holds hmipserver back for a
// trusted clock (under the radio run directory).
const CounterHoldFile = "hmipserver.clock-hold.json"

// ClockStateFile is what the fork's clock gate writes: rtc, ntp, or a word for an untrusted clock
// (timeout, rtc-implausible, ntp-implausible). The system package has the same constant.
const clockStateFile = "/run/occulite/clock-state"

// counterHistory is how many starts the state keeps per access point.
const counterHistory = 12

var (
	counterCurrentRe = regexp.MustCompile(`Current Security Counter:\s*([0-9]+)`)
	counterCalcRe    = regexp.MustCompile(`Update security counter to calculation:\s*([0-9]+)`)
)

// ParseSecurityCounterLines finds the two lines of the latest start in a daemon's output: the
// module's counter and the computed value. A start that computed nothing lower than the module's
// counter logs no update line, so okCalc may be false while okCurrent is true.
func ParseSecurityCounterLines(text string) (current, calc uint64, okCurrent, okCalc bool) {
	if m := counterCurrentRe.FindAllStringSubmatch(text, -1); len(m) > 0 {
		if n, err := strconv.ParseUint(m[len(m)-1][1], 10, 64); err == nil {
			current, okCurrent = n, true
		}
	}
	if m := counterCalcRe.FindAllStringSubmatch(text, -1); len(m) > 0 {
		if n, err := strconv.ParseUint(m[len(m)-1][1], 10, 64); err == nil {
			calc, okCalc = n, true
		}
	}
	return
}

// CounterWritten is what the module holds after a start with these two numbers: the computed
// value's lower 32 bits when it is above the module's counter (the update happens), else the
// module's own counter.
func CounterWritten(current, calc uint64, okCalc bool) uint64 {
	if okCalc && calc > current {
		return calc % CounterWrap
	}
	return current
}

// CounterVerdict judges one start from its two numbers and the highest value the devices saw
// before it (0 when unknown): backwards when the module's counter or the earlier value is above
// what this start wrote, wrapped when the computed value reached 2^32, near when it reached 2^31.
func CounterVerdict(current, calc uint64, okCalc bool, seenBefore uint64) string {
	written := CounterWritten(current, calc, okCalc)
	switch {
	case okCalc && calc > current && written < current, written < seenBefore:
		return CounterBackwards
	case okCalc && calc >= CounterWrap:
		return CounterWrapped
	case okCalc && calc >= CounterNear, current >= CounterNear:
		return CounterNearWrap
	}
	return CounterFine
}

// AccessPoint is what the access point file carries that matters here.
type AccessPoint struct {
	SGTIN   string `json:"sgtin"`
	Address string `json:"address,omitempty"`
	// FirstConnect is when the access point was first connected; Offset is added to the ticks
	// since then.
	FirstConnect time.Time `json:"first_connect"`
	Offset       uint64    `json:"offset"`
	State        string    `json:"state,omitempty"`
	// offsetAt is where the offset's 8 bytes are in the file (openccu-lite task 349's repair)
	offsetAt int
}

// Calc is what hmipserver would compute at now: the 300 ms ticks since the first connection,
// plus one, plus the offset. behind: now is before the first connection, so the time term is
// nothing and the value is the offset alone (a clock behind real time).
func (ap AccessPoint) Calc(now time.Time) (calc uint64, behind bool) {
	if now.Before(ap.FirstConnect) {
		return ap.Offset + 1, true
	}
	return uint64(now.Sub(ap.FirstConnect)/counterTick) + 1 + ap.Offset, false
}

// ReachesAt is the first time the computed value is at least v (openccu-lite B-302): the first
// connection when the offset alone reaches it.
func (ap AccessPoint) ReachesAt(v uint64) time.Time {
	if ap.Offset+1 >= v {
		return ap.FirstConnect
	}
	return ap.FirstConnect.Add(time.Duration(v-ap.Offset-1) * counterTick)
}

// WrapsAt is the time the computed value reaches 2^32 with this offset.
func (ap AccessPoint) WrapsAt() time.Time {
	if ap.Offset+1 >= CounterWrap {
		return ap.FirstConnect
	}
	return ap.FirstConnect.Add(time.Duration(CounterWrap-ap.Offset-1) * counterTick)
}

var errNotAccessPoint = errors.New("not an HmIP access point file")

// ParseAccessPoint reads the file as observed on the lab systems (165-189 bytes): a serialisation
// header and the class name (ending in ShareableHMIPAccessPoint), the SGTIN and the radio
// address as ASCII strings whose last character carries the high bit, the first connection as
// an 8-byte big-endian millisecond timestamp, a state word in the same string form (BACKUP_DONE),
// then the offset as an 8-byte big-endian integer, then the device map. Anything else - a
// truncated file, a foreign file, a timestamp outside 2010-2100 - is refused.
func ParseAccessPoint(b []byte) (AccessPoint, error) {
	var ap AccessPoint
	i := strings.Index(string(b), "ShareableHMIPAccessPoint")
	if i < 0 {
		return ap, errNotAccessPoint
	}
	rest := b[i+len("ShareableHMIPAccessPoint"):]
	sgtin, n := findASCIIRun(rest, 24, isHexASCII)
	if n < 0 {
		return ap, fmt.Errorf("%w: no SGTIN", errNotAccessPoint)
	}
	ap.SGTIN = sgtin
	rest = rest[n:]
	addr, n := readASCII(rest, 8, isHexASCII)
	if n < 0 {
		return ap, fmt.Errorf("%w: no radio address", errNotAccessPoint)
	}
	ap.Address = addr
	rest = rest[n:]
	if len(rest) < 8 {
		return ap, fmt.Errorf("%w: truncated before the first connection", errNotAccessPoint)
	}
	ms := binary.BigEndian.Uint64(rest[:8])
	rest = rest[8:]
	ap.FirstConnect = time.UnixMilli(int64(ms)).UTC()
	if ms > uint64(1<<62) || ap.FirstConnect.Year() < 2010 || ap.FirstConnect.Year() > 2100 {
		return ap, fmt.Errorf("%w: the first connection %d ms is not a plausible time", errNotAccessPoint, ms)
	}
	state, n := readASCII(rest, 32, func(c byte) bool { return c >= 'A' && c <= 'Z' || c == '_' })
	if n < 0 {
		return ap, fmt.Errorf("%w: no state word", errNotAccessPoint)
	}
	ap.State = state
	rest = rest[n:]
	if len(rest) < 8 {
		return ap, fmt.Errorf("%w: truncated before the offset", errNotAccessPoint)
	}
	ap.Offset = binary.BigEndian.Uint64(rest[:8])
	ap.offsetAt = len(b) - len(rest)
	return ap, nil
}

func isHexASCII(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'F' || c >= 'a' && c <= 'f'
}

// readASCII reads a string in the serialisation's short form at the start of b: characters of
// the class, the last one with the high bit set. Answers the string and the bytes consumed, or -1.
func readASCII(b []byte, maxLen int, class func(byte) bool) (string, int) {
	var out []byte
	for i := 0; i < len(b) && i < maxLen; i++ {
		c := b[i]
		if c&0x80 != 0 {
			c &^= 0x80
			if !class(c) {
				return "", -1
			}
			return string(append(out, c)), i + 1
		}
		if !class(c) {
			return "", -1
		}
		out = append(out, c)
	}
	return "", -1
}

// findASCIIRun finds the first string of exactly n characters of the class in the short form;
// answers it and the offset just past it, or -1.
func findASCIIRun(b []byte, n int, class func(byte) bool) (string, int) {
	for i := 0; i+n <= len(b); i++ {
		s, used := readASCII(b[i:], n, class)
		if used == n {
			return s, i + used
		}
	}
	return "", -1
}

// CounterStart is one start of hmipserver as its journal lines told it, or as computed before a
// start from the access point file and the clock.
type CounterStart struct {
	At time.Time `json:"at"`
	// Current is the module's counter (journal); Calc the computed value; Written what the module
	// holds afterwards; Behind: the clock was before the first connection (computed only).
	Current uint64 `json:"current,omitempty"`
	Calc    uint64 `json:"calc"`
	Written uint64 `json:"written,omitempty"`
	Behind  bool   `json:"behind,omitempty"`
	// Source is journal or computed; ClockState the gate's word at the time.
	Source     string `json:"source"`
	ClockState string `json:"clock_state,omitempty"`
	Verdict    string `json:"verdict"`
}

// CounterAccessPoint is the state of one access point.
type CounterAccessPoint struct {
	SGTIN        string    `json:"sgtin"`
	FirstConnect time.Time `json:"first_connect,omitempty"`
	Offset       uint64    `json:"offset"`
	// WrapsAt is when the computed value reaches 2^32 with this offset.
	WrapsAt time.Time `json:"wraps_at,omitempty"`
	// Starts is the short history, oldest first; journal readings and computations both.
	Starts []CounterStart `json:"starts"`
	// Verdict is the worst of the latest journal reading and the latest computation.
	Verdict string `json:"verdict"`
	// Seen is the highest value the devices are known to have seen (the highest written value).
	Seen uint64 `json:"seen"`
}

// CounterState is the file.
type CounterState struct {
	AccessPoints map[string]*CounterAccessPoint `json:"access_points"`
}

// ReadCounterState reads the state; an empty one without the file.
func ReadCounterState(root string) CounterState {
	var st CounterState
	_ = readJSONFile(filepath.Join(root, CounterStateFile), &st)
	if st.AccessPoints == nil {
		st.AccessPoints = map[string]*CounterAccessPoint{}
	}
	return st
}

// writeCounterState writes the file world-readable: the radio steps run inside hmipserver's unit
// with its UMask=0077, and the daemon reads the file as its own user for the Status page - so the
// modes are set explicitly, not left to the umask.
func writeCounterState(root string, st CounterState) error {
	p := filepath.Join(root, CounterStateFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o644)
	return os.Rename(tmp, p)
}

// Worst answers the graver of two verdicts.
func WorstCounterVerdict(a, b string) string {
	rank := map[string]int{CounterFine: 0, CounterNearWrap: 1, CounterWrapped: 2, CounterBackwards: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// record adds a start to the access point's history and re-judges it.
func (st *CounterState) record(sgtin string, s CounterStart) *CounterAccessPoint {
	sgtin = strings.ToUpper(sgtin)
	ap := st.AccessPoints[sgtin]
	if ap == nil {
		ap = &CounterAccessPoint{SGTIN: sgtin}
		st.AccessPoints[sgtin] = ap
	}
	if s.Source == "journal" {
		s.Written = CounterWritten(s.Current, s.Calc, s.Calc != 0)
		s.Verdict = CounterVerdict(s.Current, s.Calc, s.Calc != 0, ap.Seen)
		if s.Written > ap.Seen {
			ap.Seen = s.Written
		}
	} else {
		// computed before a start: the module's counter is not known, so only the wrap and the
		// approach to it can be judged, and a clock behind the first connection is at risk
		s.Verdict = CounterVerdict(0, s.Calc, true, 0)
		if s.Calc%CounterWrap < ap.Seen && s.Calc > ap.Seen {
			s.Verdict = CounterBackwards
		}
	}
	ap.Starts = append(ap.Starts, s)
	if len(ap.Starts) > counterHistory {
		ap.Starts = ap.Starts[len(ap.Starts)-counterHistory:]
	}
	ap.Verdict = CounterFine
	var lastJournal, lastComputed *CounterStart
	for i := range ap.Starts {
		x := &ap.Starts[i]
		if x.Source == "journal" {
			lastJournal = x
		} else {
			lastComputed = x
		}
	}
	if lastJournal != nil {
		ap.Verdict = WorstCounterVerdict(ap.Verdict, lastJournal.Verdict)
	}
	if lastComputed != nil {
		ap.Verdict = WorstCounterVerdict(ap.Verdict, lastComputed.Verdict)
	}
	return ap
}

// RecordCounterLines takes a start's journal lines into the state file and answers the access
// point's state; nil when the lines carry no counter.
func RecordCounterLines(root, sgtin string, text string, now time.Time, clockState string) (*CounterAccessPoint, error) {
	current, calc, okCurrent, okCalc := ParseSecurityCounterLines(text)
	if !okCurrent && !okCalc {
		return nil, nil
	}
	st := ReadCounterState(root)
	s := CounterStart{At: now.UTC(), Current: current, Calc: calc, Source: "journal", ClockState: clockState}
	if !okCalc {
		s.Calc = 0
	}
	ap := st.record(sgtin, s)
	return ap, writeCounterState(root, st)
}

// AccessPointFiles reads every access point file in the data directory.
func AccessPointFiles(dir string) ([]AccessPoint, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.ap"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []AccessPoint
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			continue
		}
		ap, err := ParseAccessPoint(b)
		if err != nil {
			continue
		}
		out = append(out, ap)
	}
	return out, nil
}

// CounterHold is why the prep step holds hmipserver back.
type CounterHold struct {
	Since      time.Time `json:"since"`
	SGTIN      string    `json:"sgtin"`
	Calc       uint64    `json:"calc"`
	Behind     bool      `json:"behind,omitempty"`
	ClockState string    `json:"clock_state"`
	Reason     string    `json:"reason"`
}

// ReadCounterHold answers the hold marker, nil without one.
func ReadCounterHold(root string) *CounterHold {
	var h CounterHold
	if err := readJSONFile(shadowPath(root, CounterHoldFile), &h); err != nil {
		return nil
	}
	return &h
}

// ClockTrusted: the gate's word says the clock came from a plausible real-time clock or NTP, or a
// manual setting; no file (an image without the gate, a test root) counts as trusted, as before.
func ClockTrusted(state string) bool {
	switch strings.TrimSpace(state) {
	case "", "rtc", "ntp", "manual":
		return true
	}
	return false
}

// counterPreStart is the prep step's half: every access point's next value is computed from the
// clock and recorded, and on a wrapped or at-risk access point with an untrusted clock the start
// is held (the error fails the step; the unit retries with its backoff, and the marker tells the
// pages). Never fatal otherwise: a file that cannot be read or written is logged.
func counterPreStart(d Detector, p Plan, logf func(string, ...any)) error {
	clockState := strings.TrimSpace(readFile(d.path(clockStateFile)))
	// openccu-lite task 349: a repair requested with `occulited radio repair-counter` is applied
	// before this start computes anything
	applyCounterRepair(d, logf)
	aps, _ := AccessPointFiles(d.path("/etc/config/crRFD/data"))
	if len(aps) == 0 {
		_ = os.Remove(shadowPath(d.Root, CounterHoldFile))
		return nil
	}
	now := d.now()
	st := ReadCounterState(d.Root)
	var hold *CounterHold
	for _, ap := range aps {
		calc, behind := ap.Calc(now)
		s := CounterStart{At: now.UTC(), Calc: calc, Behind: behind, Source: "computed", ClockState: clockState}
		rec := st.record(ap.SGTIN, s)
		rec.FirstConnect, rec.Offset, rec.WrapsAt = ap.FirstConnect, ap.Offset, ap.WrapsAt()
		verdict := rec.Starts[len(rec.Starts)-1].Verdict
		if verdict != CounterFine || behind {
			logf("prep hmipserver: HmIP access point %s: the security counter would be set to %d at this clock (%s; first connected %s, offset %d): %s", ap.SGTIN, calc, now.UTC().Format(time.RFC3339), ap.FirstConnect.Format(time.RFC3339), ap.Offset, verdict)
		}
		// at risk: the value would wrap now, the clock is behind the first connection, or the
		// last start hmipserver logged was already wrapped or backwards
		atRisk := calc >= CounterWrap || behind
		for _, x := range rec.Starts {
			if x.Source == "journal" {
				atRisk = atRisk || x.Verdict == CounterWrapped || x.Verdict == CounterBackwards
			}
		}
		if hold == nil && atRisk && !ClockTrusted(clockState) {
			why := "the computed security counter has passed 2^32"
			if behind {
				why = "the clock is before the access point's first connection"
			}
			hold = &CounterHold{Since: now.UTC(), SGTIN: ap.SGTIN, Calc: calc, Behind: behind, ClockState: clockState, Reason: why}
		}
	}
	if err := writeCounterState(d.Root, st); err != nil {
		logf("prep hmipserver: the security counter state was not written: %v", err)
	}
	if hold == nil {
		_ = os.Remove(shadowPath(d.Root, CounterHoldFile))
		return nil
	}
	if prev := ReadCounterHold(d.Root); prev != nil && prev.SGTIN == hold.SGTIN {
		hold.Since = prev.Since
	}
	if b, err := json.MarshalIndent(hold, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(shadowPath(d.Root, CounterHoldFile)), 0o755)
		_ = os.WriteFile(shadowPath(d.Root, CounterHoldFile), b, 0o644)
		_ = os.Chmod(shadowPath(d.Root, CounterHoldFile), 0o644) // the unit's UMask=0077 (see writeCounterState)
	}
	return fmt.Errorf("hmipserver is held back: %s (access point %s, clock state %q) and the clock is not trusted - a start now could set the HmIP security counter below what the devices have seen and lock every HmIP device out; waiting for a synchronised clock, or for the time set by hand on the Time page", hold.Reason, hold.SGTIN, hold.ClockState)
}

// counterAfterStart is the ready step's half: the two lines of this start, read from the daemon's
// output, go into the state file; the verdict is logged.
func counterAfterStart(text string, d Detector, p Plan, logf func(string, ...any)) {
	sgtin := ""
	if p.HmIP != nil {
		sgtin = p.HmIP.SGTIN
	}
	if sgtin == "" {
		if aps, _ := AccessPointFiles(d.path("/etc/config/crRFD/data")); len(aps) == 1 {
			sgtin = aps[0].SGTIN
		}
	}
	if sgtin == "" {
		return
	}
	clockState := strings.TrimSpace(readFile(d.path(clockStateFile)))
	ap, err := RecordCounterLines(d.Root, sgtin, text, d.now(), clockState)
	if err != nil {
		logf("ready hmipserver: the security counter state was not written: %v", err)
	}
	if ap == nil {
		logf("ready hmipserver: no security counter lines in this start's output")
		return
	}
	last := ap.Starts[len(ap.Starts)-1]
	switch last.Verdict {
	case CounterFine:
		logf("ready hmipserver: HmIP security counter %d, computed %d (%s)", last.Current, last.Calc, last.Verdict)
	default:
		logf("<4>ready hmipserver: HmIP security counter %d, computed %d, the module now holds %d: %s (eq-3/occu#134)", last.Current, last.Calc, last.Written, last.Verdict)
	}
}
