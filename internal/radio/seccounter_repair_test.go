package radio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// repairRoot is a root with the anonymised lab access point file, its offset replaced.
func repairRoot(t *testing.T, offset uint64) (root, apPath string) {
	t.Helper()
	root = t.TempDir()
	fixture, err := os.ReadFile("testdata/accesspoint.ap")
	if err != nil {
		t.Fatal(err)
	}
	apPath = filepath.Join(root, "etc/config/crRFD/data/3014F711A000040000000A01.ap")
	if err := os.MkdirAll(filepath.Dir(apPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apPath, withOffset(fixture, offset), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, apPath
}

func readAP(t *testing.T, p string) AccessPoint {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	ap, err := ParseAccessPoint(b)
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

// openccu-lite task 349: the plan - the target a margin above the highest known value, refused when
// the detector finds nothing (unless forced), when the target comes too close to 2^32, and when a
// start computes more already.
func TestPlanCounterRepair(t *testing.T) {
	now := time.Date(2026, 9, 30, 9, 5, 8, 0, time.UTC)
	// a wrapped start in the history: the module held 1024874459, the start computed 5319842009
	root, _ := repairRoot(t, 4031374848)
	if _, err := RecordCounterLines(root, "3014F711A000040000000A01", "Current Security Counter: 1024874459\nUpdate security counter to calculation: 5319842009\n", now.Add(-time.Hour), "ntp"); err != nil {
		t.Fatal(err)
	}
	plans, err := PlanCounterRepair(root, "", 0, false, now)
	if err != nil || len(plans) != 1 {
		t.Fatalf("%v %+v", err, plans)
	}
	p := plans[0]
	if p.Verdict != CounterWrapped || p.Known != 1024874713 || p.FromOffset || p.Target != 1024874713+CounterRepairMargin || p.Refused != "" || p.CalcNow < CounterNear {
		t.Fatalf("wrapped: %+v", p)
	}
	// the new offset gives the target at this clock
	if c, _ := (AccessPoint{FirstConnect: time.Date(2026, 9, 3, 18, 52, 45, 412e6, time.UTC), Offset: p.NewOffset}).Calc(now); c != p.Target {
		t.Fatalf("calc with the new offset %d, target %d", c, p.Target)
	}
	// by SGTIN, any case; an unknown one is an error
	if pl, err := PlanCounterRepair(root, "3014f711a000040000000a01", 5, false, now); err != nil || len(pl) != 1 || pl[0].Target != 1024874713+5 {
		t.Fatalf("by sgtin: %v %+v", err, pl)
	}
	if _, err := PlanCounterRepair(root, "3014F711A000040000000A02", 0, false, now); err == nil {
		t.Fatal("an unknown SGTIN planned")
	}
	// fine: refused unless forced; with no start recorded the offset stands in
	fine, _ := repairRoot(t, 2829)
	pl, _ := PlanCounterRepair(fine, "", 0, false, now)
	if len(pl) != 1 || pl[0].Verdict != CounterFine || !strings.Contains(pl[0].Refused, "nothing to repair") || !pl[0].FromOffset || pl[0].Known != 2829 {
		t.Fatalf("fine: %+v", pl)
	}
	// forced: a start computes 7.6 M at this clock, more than 2829 + 10 M? no - so it is raised
	pl, _ = PlanCounterRepair(fine, "", 0, true, now)
	if pl[0].Refused != "" || pl[0].Target != 2829+CounterRepairMargin {
		t.Fatalf("forced: %+v", pl)
	}
	// forced with a small margin: the start computes more already
	pl, _ = PlanCounterRepair(fine, "", 1000, true, now)
	if !strings.Contains(pl[0].Refused, "nothing to raise") {
		t.Fatalf("already above: %+v", pl)
	}
	// a known value too close to 2^32 for a margin above it
	high, _ := repairRoot(t, 2829)
	if _, err := RecordCounterLines(high, "3014F711A000040000000A01", "Current Security Counter: 4294000000\n", now, "ntp"); err != nil {
		t.Fatal(err)
	}
	pl, _ = PlanCounterRepair(high, "", 0, true, now)
	if !strings.Contains(pl[0].Refused, "too close to 2^32") {
		t.Fatalf("too high: %+v", pl)
	}
}

// The request is applied by the next prep with a trusted clock: the offset rewritten (the original
// kept once as .bak, the mode kept), the repair in the history, the request gone; an untrusted clock
// keeps it for a later start.
func TestCounterRepairAppliedAtTheNextStart(t *testing.T) {
	now := time.Date(2026, 9, 30, 9, 5, 8, 0, time.UTC)
	root, apPath := repairRoot(t, 4031374848)
	if _, err := RecordCounterLines(root, "3014F711A000040000000A01", "Current Security Counter: 1024874459\nUpdate security counter to calculation: 5319842009\n", now.Add(-time.Hour), "ntp"); err != nil {
		t.Fatal(err)
	}
	plans, _ := PlanCounterRepair(root, "", 0, false, now)
	if n, err := RequestCounterRepair(root, plans, now); n != 1 || err != nil {
		t.Fatalf("request: %d %v", n, err)
	}
	if r := ReadCounterRepair(root); r == nil || r.Targets["3014F711A000040000000A01"] != plans[0].Target {
		t.Fatalf("request file: %+v", r)
	}
	if st, err := os.Stat(filepath.Join(root, CounterRepairFile)); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("request file mode: %v %v", err, st)
	}
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, strings.TrimSpace(f)) }
	start := now.Add(10 * time.Minute)
	d := Detector{Root: root, Now: func() time.Time { return start }}
	// the clock is not trusted: nothing changes, the request stays
	_ = os.MkdirAll(filepath.Join(root, "run/occulite"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "run/occulite/clock-state"), []byte("timeout\n"), 0o644)
	applyCounterRepair(d, logf)
	if readAP(t, apPath).Offset != 4031374848 || ReadCounterRepair(root) == nil || !strings.Contains(strings.Join(lines, "\n"), "waits for a trusted clock") {
		t.Fatalf("applied with an untrusted clock: %q", lines)
	}
	// trusted: applied
	_ = os.WriteFile(filepath.Join(root, "run/occulite/clock-state"), []byte("ntp\n"), 0o644)
	applyCounterRepair(d, logf)
	ap := readAP(t, apPath)
	if c, _ := ap.Calc(start); c != plans[0].Target || ap.SGTIN != "3014F711A000040000000A01" || ap.State != "BACKUP_DONE" {
		t.Fatalf("after the repair: %+v computes %d, target %d", ap, c, plans[0].Target)
	}
	if bak := readAP(t, apPath+".bak"); bak.Offset != 4031374848 {
		t.Fatalf("backup offset %d", bak.Offset)
	}
	if st, _ := os.Stat(apPath); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if ReadCounterRepair(root) != nil {
		t.Fatal("the request stayed")
	}
	st := ReadCounterState(root).AccessPoints["3014F711A000040000000A01"]
	last := st.Starts[len(st.Starts)-1]
	if last.Source != "repair" || last.Calc != plans[0].Target || last.Verdict != CounterFine {
		t.Fatalf("history: %+v", last)
	}
	// the prep's own computation after it: fine, no hold
	if err := counterPreStart(d, Plan{}, logf); err != nil {
		t.Fatalf("prep after the repair: %v", err)
	}
	if v := ReadCounterState(root).AccessPoints["3014F711A000040000000A01"].Starts; v[len(v)-1].Verdict != CounterFine {
		t.Fatalf("computed after the repair: %+v", v[len(v)-1])
	}
	// a second repair keeps the first backup
	if _, err := RequestCounterRepair(root, []CounterRepairPlan{{SGTIN: "3014F711A000040000000A01", Target: plans[0].Target + 5}}, now); err != nil {
		t.Fatal(err)
	}
	applyCounterRepair(d, logf)
	if bak := readAP(t, apPath+".bak"); bak.Offset != 4031374848 {
		t.Fatalf("the backup was overwritten: %d", bak.Offset)
	}
}

// A request for an access point that is gone is dropped; a file that cannot be written keeps it.
func TestCounterRepairEdges(t *testing.T) {
	now := time.Date(2026, 9, 30, 9, 5, 8, 0, time.UTC)
	root, apPath := repairRoot(t, 2829)
	d := Detector{Root: root, Now: func() time.Time { return now }}
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, f) }
	if n, _ := RequestCounterRepair(root, []CounterRepairPlan{{SGTIN: "x", Refused: "no"}}, now); n != 0 || ReadCounterRepair(root) != nil {
		t.Fatal("a refused plan was requested")
	}
	_, _ = RequestCounterRepair(root, []CounterRepairPlan{{SGTIN: "3014F711A000040000000A02", Target: 1}}, now)
	applyCounterRepair(d, logf)
	if ReadCounterRepair(root) != nil || !strings.Contains(strings.Join(lines, "\n"), "dropped") {
		t.Fatalf("a request for a missing access point stayed: %q", lines)
	}
	// the place of the offset unknown: refused, the file untouched
	b, _ := os.ReadFile(apPath)
	if err := writeAccessPointOffset(apPath, b, AccessPoint{}, 1); err == nil {
		t.Fatal("written without the offset's place")
	}
	// a directory that refuses the write keeps the request (not as root: root writes anyway)
	if os.Geteuid() != 0 {
		_, _ = RequestCounterRepair(root, []CounterRepairPlan{{SGTIN: "3014F711A000040000000A01", Target: 20_000_000}}, now)
		dir := filepath.Dir(apPath)
		_ = os.Chmod(dir, 0o555)
		applyCounterRepair(d, logf)
		_ = os.Chmod(dir, 0o755)
		if ReadCounterRepair(root) == nil || readAP(t, apPath).Offset != 2829 {
			t.Fatal("a failed write lost the request or changed the file")
		}
	}
	if had, err := CancelCounterRepair(root); !had || err != nil {
		t.Fatalf("cancel: %v %v", had, err)
	}
	if had, err := CancelCounterRepair(root); had || err != nil {
		t.Fatalf("cancel again: %v %v", had, err)
	}
}
