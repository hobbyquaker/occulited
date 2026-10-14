package radio

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The repair of a wrapped or backwards HmIP security counter (openccu-lite task 349; the maintainer,
// 2026-10-12: a command line tool only, no UI). `occulited radio repair-counter` computes a target
// above the highest value the devices are known to have seen - the detector's history - and leaves
// a request; the next `radio prep hmipserver` rewrites the access point file's offset so that this
// start computes the target, and hmipserver sets the module's counter to it (it writes a computed
// value that is above the module's counter, see seccounter.go). The devices then accept the
// system's frames again without being power-cycled or paired again. The rule follows OpenCCU's
// public checkHmIPsecurityCounter.sh `repair` (OpenCCU #4317): the target is a margin above the
// last known counter, and never closer to 2^32 than CounterRepairLimit.

// CounterRepairFile is the request, kept on the userfs until a start applied it.
const CounterRepairFile = "/usr/local/var/lib/occulite/hmip-security-counter-repair.json"

// CounterRepairMargin is how far above the highest known value the target lies by default - the
// frames a system may have sent since that value was recorded (OpenCCU's script uses the same).
const CounterRepairMargin = uint64(10_000_000)

// CounterRepairLimit is the highest target: 2^32 less about eight hours of the time term, so the
// next starts do not wrap at once.
const CounterRepairLimit = CounterWrap - 100_000

// CounterRepairPlan is what the repair would do for one access point.
type CounterRepairPlan struct {
	SGTIN string `json:"sgtin"`
	// Verdict is the detector's, made worse by what this clock would compute now
	Verdict string `json:"verdict"`
	// Known is the highest value the devices are known to have seen (the detector's seen, or the
	// module's counter at a start); FromOffset says the history had none and the stored offset stood in
	Known      uint64 `json:"known"`
	FromOffset bool   `json:"from_offset,omitempty"`
	Offset     uint64 `json:"offset"`
	// CalcNow is what a start would compute at this clock with the stored offset
	CalcNow uint64 `json:"calc_now"`
	// Target is what the repaired start computes; NewOffset the offset that gives it at this clock
	// (the start recomputes it at its own time)
	Target    uint64 `json:"target"`
	NewOffset uint64 `json:"new_offset"`
	// Refused says why this access point is not repaired ("" = it is)
	Refused string `json:"refused,omitempty"`
}

// CounterRepairRequest is the file.
type CounterRepairRequest struct {
	RequestedAt time.Time `json:"requested_at"`
	// Targets by SGTIN
	Targets map[string]uint64 `json:"targets"`
}

// PlanCounterRepair works out the repair for every access point, or for the one sgtin names.
// force repairs an access point the detector calls fine as well.
func PlanCounterRepair(root, sgtin string, margin uint64, force bool, now time.Time) ([]CounterRepairPlan, error) {
	if margin == 0 {
		margin = CounterRepairMargin
	}
	aps, err := AccessPointFiles(filepath.Join(root, "/etc/config/crRFD/data"))
	if err != nil {
		return nil, err
	}
	st := ReadCounterState(root)
	var out []CounterRepairPlan
	for _, ap := range aps {
		if sgtin != "" && !strings.EqualFold(ap.SGTIN, sgtin) {
			continue
		}
		pl := CounterRepairPlan{SGTIN: ap.SGTIN, Offset: ap.Offset, Verdict: CounterFine}
		calc, _ := ap.Calc(now)
		pl.CalcNow = calc
		pl.Verdict = WorstCounterVerdict(pl.Verdict, CounterVerdict(0, calc, true, 0))
		if rec := st.AccessPoints[strings.ToUpper(ap.SGTIN)]; rec != nil {
			pl.Verdict = WorstCounterVerdict(pl.Verdict, rec.Verdict)
			pl.Known = rec.Seen
			for _, x := range rec.Starts {
				if x.Source == "journal" && x.Current > pl.Known {
					pl.Known = x.Current
				}
			}
		}
		if pl.Known == 0 {
			// no start recorded: the stored offset stands in, as in OpenCCU's script - after a
			// wrap hmipserver keeps about the module's counter there
			pl.Known, pl.FromOffset = ap.Offset, true
		}
		pl.Target = pl.Known + margin
		switch {
		case pl.Verdict == CounterFine && !force:
			pl.Refused = "the detector finds nothing to repair (fine); --force repairs all the same"
		case pl.Target > CounterRepairLimit:
			pl.Refused = fmt.Sprintf("the target %d would be too close to 2^32: the devices saw too high a value; power-cycle them or pair them again", pl.Target)
		case calc >= pl.Target && calc < CounterNear:
			// a start writes more than the target anyway, far from the wrap; a computed value
			// past 2^31 is brought down to the target instead, away from the wrap
			pl.Refused = fmt.Sprintf("a start computes %d already, above the target %d and far from 2^32: nothing to raise", calc, pl.Target)
		}
		pl.NewOffset = repairOffset(ap, pl.Target, now)
		out = append(out, pl)
	}
	if sgtin != "" && len(out) == 0 {
		return nil, fmt.Errorf("no HmIP access point %s in /etc/config/crRFD/data", sgtin)
	}
	return out, nil
}

// repairOffset is the offset with which a start at now computes target: the target less the time
// term, never below 0 (a time term above the target already gives more).
func repairOffset(ap AccessPoint, target uint64, now time.Time) uint64 {
	calc0, _ := AccessPoint{FirstConnect: ap.FirstConnect}.Calc(now)
	if calc0 >= target {
		return 0
	}
	return target - calc0
}

// RequestCounterRepair writes the request for the plans that are not refused; it answers how many.
func RequestCounterRepair(root string, plans []CounterRepairPlan, now time.Time) (int, error) {
	req := CounterRepairRequest{RequestedAt: now.UTC(), Targets: map[string]uint64{}}
	for _, p := range plans {
		if p.Refused == "" {
			req.Targets[strings.ToUpper(p.SGTIN)] = p.Target
		}
	}
	if len(req.Targets) == 0 {
		return 0, nil
	}
	return len(req.Targets), writeJSON0644(filepath.Join(root, CounterRepairFile), req)
}

// ReadCounterRepair answers the pending request, nil without one.
func ReadCounterRepair(root string) *CounterRepairRequest {
	var r CounterRepairRequest
	if err := readJSONFile(filepath.Join(root, CounterRepairFile), &r); err != nil || len(r.Targets) == 0 {
		return nil
	}
	return &r
}

// CancelCounterRepair removes a pending request; false when there was none.
func CancelCounterRepair(root string) (bool, error) {
	err := os.Remove(filepath.Join(root, CounterRepairFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// applyCounterRepair is the prep step's half: a pending request rewrites each named access point's
// offset for this start - only with a trusted clock, as the offset is computed from it - keeps the
// original as <file>.bak (the first one only, as OpenCCU's script), records the repair in the
// detector's history and clears the request. A failure keeps the request for the next start and
// never fails the start.
func applyCounterRepair(d Detector, logf func(string, ...any)) {
	req := ReadCounterRepair(d.Root)
	if req == nil {
		return
	}
	clockState := strings.TrimSpace(readFile(d.path(clockStateFile)))
	if !ClockTrusted(clockState) {
		logf("<4>prep hmipserver: the HmIP security counter repair waits for a trusted clock (clock state %q); it is applied at a later start", clockState)
		return
	}
	dir := d.path("/etc/config/crRFD/data")
	names, _ := filepath.Glob(filepath.Join(dir, "*.ap"))
	sort.Strings(names)
	now := d.now()
	st := ReadCounterState(d.Root)
	left := map[string]uint64{}
	for k, v := range req.Targets {
		left[k] = v
	}
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			continue
		}
		ap, err := ParseAccessPoint(b)
		if err != nil {
			continue
		}
		target, ok := left[strings.ToUpper(ap.SGTIN)]
		if !ok {
			continue
		}
		off := repairOffset(ap, target, now)
		if err := writeAccessPointOffset(n, b, ap, off); err != nil {
			logf("<3>prep hmipserver: the HmIP security counter repair of %s failed, kept for the next start: %v", ap.SGTIN, err)
			continue
		}
		delete(left, strings.ToUpper(ap.SGTIN))
		calc, _ := AccessPoint{FirstConnect: ap.FirstConnect, Offset: off}.Calc(now)
		st.record(ap.SGTIN, CounterStart{At: now.UTC(), Calc: calc, Source: "repair", ClockState: clockState})
		logf("<4>prep hmipserver: HmIP security counter repair of %s: the offset changed from %d to %d, this start computes %d (the target %d, requested %s); the original is %s.bak", ap.SGTIN, ap.Offset, off, calc, target, req.RequestedAt.Format(time.RFC3339), filepath.Base(n))
	}
	if err := writeCounterState(d.Root, st); err != nil {
		logf("prep hmipserver: the security counter state was not written: %v", err)
	}
	for k := range left {
		if _, err := os.Stat(filepath.Join(dir, k+".ap")); errors.Is(err, os.ErrNotExist) {
			// the access point is gone (exchanged, paired anew): nothing left to repair
			logf("<4>prep hmipserver: the HmIP security counter repair of %s is dropped: no such access point file any more", k)
			delete(left, k)
		}
	}
	if len(left) == 0 {
		_ = os.Remove(filepath.Join(d.Root, CounterRepairFile))
		return
	}
	req.Targets = left
	_ = writeJSON0644(filepath.Join(d.Root, CounterRepairFile), req)
}

// writeAccessPointOffset writes the file with another offset: a .bak of the original first (only
// when there is none), the new content through a temporary file with the original's mode and
// owner, read back and parsed before it replaces the file.
func writeAccessPointOffset(path string, b []byte, ap AccessPoint, offset uint64) error {
	if ap.offsetAt <= 0 || ap.offsetAt+8 > len(b) {
		return fmt.Errorf("the offset's place in the file is unknown")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path + ".bak"); errors.Is(err, os.ErrNotExist) {
		if err := writeLike(path+".bak", b, fi); err != nil {
			return fmt.Errorf("the backup: %w", err)
		}
	}
	out := append([]byte{}, b...)
	binary.BigEndian.PutUint64(out[ap.offsetAt:], offset)
	tmp := path + ".new"
	if err := writeLike(tmp, out, fi); err != nil {
		return err
	}
	check, err := os.ReadFile(tmp)
	if err == nil {
		var got AccessPoint
		if got, err = ParseAccessPoint(check); err == nil && (got.Offset != offset || got.SGTIN != ap.SGTIN) {
			err = fmt.Errorf("the written file reads back as offset %d", got.Offset)
		}
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// writeLike writes a file with the mode and, where the system tells it, the owner of fi.
func writeLike(path string, b []byte, fi os.FileInfo) error {
	if err := os.WriteFile(path, b, fi.Mode().Perm()); err != nil {
		return err
	}
	_ = os.Chmod(path, fi.Mode().Perm())
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(path, int(sys.Uid), int(sys.Gid))
	}
	return nil
}

// writeJSON0644 writes a JSON file the daemon can read whatever the umask.
func writeJSON0644(p string, v any) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o644)
	return os.Rename(tmp, p)
}
