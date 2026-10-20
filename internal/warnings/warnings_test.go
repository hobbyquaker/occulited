package warnings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// a fake condition: the warnings a test has switched on, and whether the source can read them
type cond struct {
	on      map[string]Warning
	unknown bool
}

func (c *cond) set(w Warning) { c.on[w.ID+":"+w.Variant] = w }
func (c *cond) clear(id, variant string) {
	delete(c.on, id+":"+variant)
}

func rig(t *testing.T, file string) (*Tracker, *cond, *time.Time) {
	t.Helper()
	c := &cond{on: map[string]Warning{}}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	tr := &Tracker{
		File: file,
		Now:  func() time.Time { return now },
		Sources: []Source{
			{IDs: []string{"backup-target", "certificate", "security-key"}, Eval: func(context.Context) ([]Warning, bool) {
				if c.unknown {
					return nil, false
				}
				var out []Warning
				for _, w := range c.on {
					if w.ID != "rega" {
						out = append(out, w)
					}
				}
				return out, true
			}},
			{IDs: []string{"rega"}, Lists: true, Eval: func(context.Context) ([]Warning, bool) {
				var out []Warning
				for _, w := range c.on {
					if w.ID == "rega" {
						out = append(out, w)
					}
				}
				return out, true
			}},
		},
	}
	return tr, c, &now
}

func silencedFor(tr *Tracker, user string, admin bool, id string) *Silence {
	for _, w := range tr.List(context.Background(), user, admin) {
		if w.ID == id {
			return w.Silenced
		}
	}
	return nil
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestSilenceHidesForItsAdminOnly(t *testing.T) {
	tr, c, now := rig(t, "")
	c.set(Warning{ID: "backup-target", Variant: "/media/usb0/backup", Severity: SeverityError})
	s, err := tr.Silence(context.Background(), "admin", "backup-target", "/media/usb0/backup", 90)
	if err != nil {
		t.Fatal(err)
	}
	if !s.At.Equal(*now) || !s.Until.Equal(now.Add(days(90))) || s.By != "admin" {
		t.Errorf("silence %+v", s)
	}
	if got := silencedFor(tr, "admin", true, "backup-target"); got == nil || got.By != "admin" {
		t.Errorf("the admin who silenced it: %+v", got)
	}
	// per user (D-64): another administrator still sees it, and a user never sees a silence
	if got := silencedFor(tr, "sebastian", true, "backup-target"); got != nil {
		t.Errorf("another admin: %+v", got)
	}
	if got := silencedFor(tr, "admin", false, "backup-target"); got != nil {
		t.Errorf("without the admin role: %+v", got)
	}
	// the warning itself is in every list
	if n := len(tr.List(context.Background(), "monitor", false)); n != 1 {
		t.Errorf("a user's list has %d warnings", n)
	}
}

func TestPeriodsAndRefusals(t *testing.T) {
	tr, c, now := rig(t, "")
	c.set(Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning})
	for _, d := range []int{1, 7, 90} {
		s, err := tr.Silence(context.Background(), "admin", "security-key", "default", d)
		if err != nil || !s.Until.Equal(now.Add(days(d))) {
			t.Errorf("%d days: %+v %v", d, s, err)
		}
	}
	for _, d := range []int{0, 2, 30, 91, -1} {
		if _, err := tr.Silence(context.Background(), "admin", "security-key", "default", d); !errors.Is(err, ErrPeriod) {
			t.Errorf("%d days: %v", d, err)
		}
	}
	if _, err := tr.Silence(context.Background(), "admin", "security-key", "other", 1); !errors.Is(err, ErrNotActive) {
		t.Errorf("another variant: %v", err)
	}
	if _, err := tr.Silence(context.Background(), "admin", "certificate", "expiring", 1); !errors.Is(err, ErrNotActive) {
		t.Errorf("an inactive warning: %v", err)
	}
	// silencing again replaces, it does not stack
	if n := len(tr.silences); n != 1 {
		t.Errorf("%d silences", n)
	}
	if err := tr.Unsilence("sebastian", "security-key", "default"); !errors.Is(err, ErrNotSilenced) {
		t.Errorf("someone else's silence: %v", err)
	}
	if err := tr.Unsilence("admin", "security-key", "default"); err != nil {
		t.Fatal(err)
	}
	if got := silencedFor(tr, "admin", true, "security-key"); got != nil {
		t.Errorf("unsilenced: %+v", got)
	}
}

func TestTheSilenceEndsAfterItsPeriod(t *testing.T) {
	tr, c, now := rig(t, "")
	c.set(Warning{ID: "certificate", Variant: "expiring", Severity: SeverityError})
	if _, err := tr.Silence(context.Background(), "admin", "certificate", "expiring", 90); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(days(89))
	if silencedFor(tr, "admin", true, "certificate") == nil {
		t.Fatal("day 89: still silenced")
	}
	*now = now.Add(days(1))
	if got := silencedFor(tr, "admin", true, "certificate"); got != nil {
		t.Fatalf("day 90: shown again, got %+v", got)
	}
	if len(tr.silences) != 0 {
		t.Errorf("the spent silence is deleted: %+v", tr.silences)
	}
}

// the maintainer's rule: gone on day 30, back on day 60 - shown on day 60
func TestAClearedWarningSpendsItsSilence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "warnings.json")
	tr, c, now := rig(t, file)
	c.set(Warning{ID: "backup-target", Variant: "/media/usb0/backup", Severity: SeverityError})
	if _, err := tr.Silence(context.Background(), "admin", "backup-target", "/media/usb0/backup", 90); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(days(30))
	c.clear("backup-target", "/media/usb0/backup")
	// nobody reads the list: the periodic evaluation notices
	tr.Evaluate(context.Background())
	if len(tr.silences) != 0 {
		t.Fatalf("day 30: the silence is spent, got %+v", tr.silences)
	}
	b, _ := os.ReadFile(file)
	if string(b) != "{\n  \"silences\": []\n}\n" {
		t.Errorf("the file follows:\n%s", b)
	}
	*now = now.Add(days(30))
	c.set(Warning{ID: "backup-target", Variant: "/media/usb0/backup", Severity: SeverityError})
	if got := silencedFor(tr, "admin", true, "backup-target"); got != nil {
		t.Errorf("day 60: shown, got %+v", got)
	}
}

// Run evaluates on its own, without a reader
func TestRunNoticesTheClearing(t *testing.T) {
	tr, c, _ := rig(t, "")
	tr.Interval = 5 * time.Millisecond
	c.set(Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning})
	if _, err := tr.Silence(context.Background(), "admin", "security-key", "default", 7); err != nil {
		t.Fatal(err)
	}
	tr.eval.Lock() // the test's writes to c happen between evaluations
	c.clear("security-key", "default")
	tr.eval.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tr.mu.Lock()
		n := len(tr.silences)
		tr.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not spend the silence")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestUnknownKeepsTheSilence(t *testing.T) {
	tr, c, _ := rig(t, "")
	c.set(Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning})
	if _, err := tr.Silence(context.Background(), "admin", "security-key", "default", 90); err != nil {
		t.Fatal(err)
	}
	// rfd restarting: the source cannot tell, which is not "cleared"
	c.unknown = true
	if n := len(tr.List(context.Background(), "admin", true)); n != 0 {
		t.Errorf("an unknown source shows nothing, got %d", n)
	}
	c.unknown = false
	if silencedFor(tr, "admin", true, "security-key") == nil {
		t.Error("the silence survived the unknown evaluation")
	}
}

// B-107, a boot: the silences come back from the file, and while the box is still starting a
// warning that reads as clear is not yet known - the backup target's stick is not mounted, and the
// security-key source cannot tell before the radio detection. Both silences are kept; active again
// they are still silenced; once the boot is settled a warning that is really gone spends its
// silence, and one that is still there keeps it.
func TestABootKeepsSilencesUntilItIsSettled(t *testing.T) {
	file := filepath.Join(t.TempDir(), "warnings.json")
	backup := Warning{ID: "backup-target", Variant: "/media/usb0/backup", Severity: SeverityError}
	key := Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning}
	now := time.Date(2026, 9, 12, 20, 20, 0, 0, time.UTC)
	backupOn, keyOn, keyKnown, settled := true, true, true, true
	newTracker := func() *Tracker {
		return &Tracker{
			File:    file,
			Now:     func() time.Time { return now },
			Settled: func() bool { return settled },
			Sources: []Source{
				{IDs: []string{"backup-target"}, Eval: func(context.Context) ([]Warning, bool) {
					if backupOn {
						return []Warning{backup}, true
					}
					return nil, true
				}},
				{IDs: []string{"security-key"}, Eval: func(context.Context) ([]Warning, bool) {
					if !keyKnown {
						return nil, false
					}
					if keyOn {
						return []Warning{key}, true
					}
					return nil, true
				}},
			},
		}
	}
	tr := newTracker()
	for _, w := range []Warning{backup, key} {
		if _, err := tr.Silence(context.Background(), "admin", w.ID, w.Variant, 7); err != nil {
			t.Fatal(err)
		}
	}
	saved, _ := os.ReadFile(file)
	silenced := func(tr *Tracker, id string) bool {
		t.Helper()
		for _, w := range tr.List(context.Background(), "admin", true) {
			if w.ID == id {
				return w.Silenced != nil
			}
		}
		t.Fatalf("%s is not active", id)
		return false
	}

	// the reboot: a new tracker on the file, 20 s into the boot
	now = now.Add(6 * time.Minute)
	settled, backupOn, keyKnown = false, false, false
	boot := newTracker()
	if list := boot.Evaluate(context.Background()); len(list) != 0 {
		t.Fatalf("at start: %+v", list)
	}
	if b, _ := os.ReadFile(file); string(b) != string(saved) {
		t.Fatalf("an evaluation at start spent silences:\n%s\nwas\n%s", b, saved)
	}
	// the stick is mounted and rfd answers: both are back, and still silenced
	backupOn, keyKnown = true, true
	if !silenced(boot, "backup-target") || !silenced(boot, "security-key") {
		t.Fatal("the silences did not survive the boot")
	}
	// the stick is gone again while the box still starts: kept
	backupOn = false
	boot.Evaluate(context.Background())
	if n := len(boot.silences); n != 2 {
		t.Fatalf("before the boot is settled: %d silences", n)
	}
	// settled: the target is really gone now, its silence is spent; the key is still there
	settled = true
	boot.Evaluate(context.Background())
	if len(boot.silences) != 1 || boot.silences[0].ID != "security-key" || !silenced(boot, "security-key") {
		t.Fatalf("after the boot: %+v", boot.silences)
	}
	// and a key that is set spends the last one
	keyOn = false
	boot.Evaluate(context.Background())
	if b, _ := os.ReadFile(file); string(b) != "{\n  \"silences\": []\n}\n" {
		t.Errorf("the file:\n%s", b)
	}
	// a silence still runs out while the box starts
	keyOn, settled = true, false
	if _, err := boot.Silence(context.Background(), "admin", "security-key", "default", 1); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	boot.Evaluate(context.Background())
	if len(boot.silences) != 0 {
		t.Errorf("a silence past its end: %+v", boot.silences)
	}
}

func TestANewVariantIsNotHidden(t *testing.T) {
	tr, c, _ := rig(t, "")
	c.set(Warning{ID: "certificate", Variant: "expiring", Severity: SeverityError})
	c.set(Warning{ID: "rega", Variant: "email,hm-print", Severity: SeverityError})
	if _, err := tr.Silence(context.Background(), "admin", "certificate", "expiring", 90); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Silence(context.Background(), "admin", "rega", "email,hm-print", 90); err != nil {
		t.Fatal(err)
	}
	// the renewal failed now: another warning, and the expiring one cleared
	c.clear("certificate", "expiring")
	c.set(Warning{ID: "certificate", Variant: "renewal-failed", Severity: SeverityError})
	if got := silencedFor(tr, "admin", true, "certificate"); got != nil {
		t.Errorf("renewal-failed is not hidden by expiring: %+v", got)
	}
	// an addon less on a list stays silenced
	c.clear("rega", "email,hm-print")
	c.set(Warning{ID: "rega", Variant: "email", Severity: SeverityError})
	if silencedFor(tr, "admin", true, "rega") == nil {
		t.Error("a subset of the silenced list stays silenced")
	}
	// one more addon is a new warning
	c.clear("rega", "email")
	c.set(Warning{ID: "rega", Variant: "cuxd,email", Severity: SeverityError})
	if got := silencedFor(tr, "admin", true, "rega"); got != nil {
		t.Errorf("an added addon is new: %+v", got)
	}
}

func TestARestartKeepsSilences(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "warnings.json")
	tr, c, now := rig(t, file)
	c.set(Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning})
	if _, err := tr.Silence(context.Background(), "admin", "security-key", "default", 7); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", st, err)
	}
	again, c2, now2 := rig(t, file)
	*now2 = now.Add(days(3))
	c2.set(Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning})
	got := silencedFor(again, "admin", true, "security-key")
	if got == nil || !got.Until.Equal(now.Add(days(7))) {
		t.Errorf("after the restart: %+v", got)
	}
	_ = c
}

func TestABrokenFileStartsEmpty(t *testing.T) {
	file := filepath.Join(t.TempDir(), "warnings.json")
	if err := os.WriteFile(file, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr, c, _ := rig(t, file)
	c.set(Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning})
	if n := len(tr.List(context.Background(), "admin", true)); n != 1 {
		t.Errorf("%d warnings", n)
	}
	if _, err := tr.Silence(context.Background(), "admin", "security-key", "default", 1); err != nil {
		t.Errorf("silencing over a broken file: %v", err)
	}
}

func TestAFailedSaveKeepsTheOldState(t *testing.T) {
	dir := t.TempDir()
	// the file's directory is a file: nothing can be written
	blocker := filepath.Join(dir, "state")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tr, c, _ := rig(t, filepath.Join(blocker, "warnings.json"))
	c.set(Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning})
	if _, err := tr.Silence(context.Background(), "admin", "security-key", "default", 1); err == nil {
		t.Fatal("the save failed and said nothing")
	}
	if silencedFor(tr, "admin", true, "security-key") != nil {
		t.Error("a silence that could not be saved is not in force")
	}
}

func TestErrorsComeFirst(t *testing.T) {
	tr, c, _ := rig(t, "")
	c.set(Warning{ID: "security-key", Variant: "default", Severity: SeverityWarning})
	c.set(Warning{ID: "certificate", Variant: "expiring", Severity: SeverityError})
	c.set(Warning{ID: "backup-target", Variant: "/b", Severity: SeverityError})
	c.set(Warning{ID: "rega", Variant: "email", Severity: SeverityError})
	var got []string
	for _, w := range tr.List(context.Background(), "admin", true) {
		got = append(got, w.ID)
	}
	want := []string{"backup-target", "certificate", "rega", "security-key"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// a silence of a warning this daemon does not know any more is dropped
func TestAnUnknownIDIsDropped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "warnings.json")
	if err := os.WriteFile(file, []byte(`{"silences":[{"id":"gone","variant":"x","by":"admin","at":"2026-09-01T00:00:00Z","until":"2027-01-01T00:00:00Z"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tr, _, _ := rig(t, file)
	tr.Evaluate(context.Background())
	if len(tr.silences) != 0 {
		t.Errorf("%+v", tr.silences)
	}
}

// openccu-lite task 366: notices (info) come after the warnings, whatever the sources' order.
func TestSortPutsNoticesLast(t *testing.T) {
	sources := []Source{{IDs: []string{"a", "b", "c"}}}
	list := []Warning{{ID: "a", Severity: SeverityInfo}, {ID: "c", Severity: SeverityWarning}, {ID: "b", Severity: SeverityError}, {ID: "b", Severity: SeverityWarning}}
	sortWarnings(list, sources)
	got := ""
	for _, w := range list {
		got += w.ID + ":" + w.Severity + " "
	}
	if got != "b:error b:warning c:warning a:info " {
		t.Errorf("order %s", got)
	}
}
