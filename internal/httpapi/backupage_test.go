package httpapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/bootchart"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// backup-age (occulited task 140): the age of the newest backup of any way, three days a warning,
// seven an error; a system that never made one counts from AgeSince.
func TestBackupAgeRule(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	const day = 24 * time.Hour
	name := func(id string) string {
		if id == "usb" {
			return "USB stick"
		}
		return id
	}
	noSince := func() (time.Time, error) { t.Fatal("AgeSince read although a backup exists"); return time.Time{}, nil }
	for _, c := range []struct {
		name    string
		latest  map[string]backupcrypt.Created
		since   func() (time.Time, error)
		sev     string // "" = no warning
		days    int
		params  map[string]any
		unknown bool
	}{
		{name: "download an hour ago", latest: map[string]backupcrypt.Created{"download": {Time: ago(time.Hour), By: "admin"}}, since: noSince},
		{name: "exactly three days", latest: map[string]backupcrypt.Created{"client": {Time: ago(3 * day), By: "occulite-backup on nas"}}, since: noSince},
		{name: "download four days ago", latest: map[string]backupcrypt.Created{"download": {Time: ago(4 * day), By: "admin"}}, since: noSince,
			sev: warnings.SeverityWarning, days: 4, params: map[string]any{"way": "download", "by": "admin"}},
		{name: "client exactly seven days", latest: map[string]backupcrypt.Created{"client": {Time: ago(7 * day), By: "occulite-backup on nas"}}, since: noSince,
			sev: warnings.SeverityWarning, days: 7, params: map[string]any{"way": "client", "by": "occulite-backup on nas"}},
		{name: "automatic eight days ago", latest: map[string]backupcrypt.Created{"automatic": {Time: ago(8*day + time.Hour), Instance: "nightly", Targets: []string{"usb", "t1234567"}}}, since: noSince,
			sev: warnings.SeverityError, days: 8, params: map[string]any{"way": "automatic", "targets": "USB stick, t1234567"}},
		{name: "the newest way counts", latest: map[string]backupcrypt.Created{
			"automatic": {Time: ago(30 * day), Targets: []string{"usb"}},
			"run":       {Time: ago(5 * day), Targets: []string{"usb"}},
			"earlier":   {Time: ago(40 * day)},
		}, since: noSince, sev: warnings.SeverityWarning, days: 5, params: map[string]any{"way": "run", "targets": "USB stick"}},
		{name: "an earlier entry counts", latest: map[string]backupcrypt.Created{"earlier": {Time: ago(2 * day)}}, since: noSince},
		{name: "an earlier entry, old", latest: map[string]backupcrypt.Created{"earlier": {Time: ago(10 * day)}}, since: noSince,
			sev: warnings.SeverityError, days: 10, params: map[string]any{"way": "earlier"}},
		{name: "a backup in the future", latest: map[string]backupcrypt.Created{"download": {Time: now.Add(day)}}, since: noSince},
		{name: "never, first minute", since: func() (time.Time, error) { return ago(time.Minute), nil }},
		{name: "never, four days", since: func() (time.Time, error) { return ago(4 * day), nil },
			sev: warnings.SeverityWarning, days: 4, params: map[string]any{}},
		{name: "never, a week and a day", since: func() (time.Time, error) { return ago(8 * day), nil },
			sev: warnings.SeverityError, days: 8, params: map[string]any{}},
		{name: "never, the start not kept", since: func() (time.Time, error) { return now, errors.New("read-only") }},
		{name: "never, no start at all", since: func() (time.Time, error) { return time.Time{}, errors.New("broken") }, unknown: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws, ok := backupAgeRule(c.latest, c.since, now, name)
			if ok == c.unknown {
				t.Fatalf("ok %v", ok)
			}
			if c.sev == "" {
				if len(ws) != 0 {
					t.Fatalf("a warning: %+v", ws)
				}
				return
			}
			if len(ws) != 1 {
				t.Fatalf("%+v", ws)
			}
			w := ws[0]
			if w.ID != "backup-age" || w.Variant != c.sev || w.Severity != c.sev || w.Href != "/backup" {
				t.Errorf("%+v", w)
			}
			if w.Params["days"] != c.days {
				t.Errorf("days %v, want %d", w.Params["days"], c.days)
			}
			for _, k := range []string{"way", "by", "targets"} {
				if w.Params[k] != c.params[k] {
					t.Errorf("%s %v, want %v", k, w.Params[k], c.params[k])
				}
			}
			if _, err := time.Parse(time.RFC3339, w.Params["since"].(string)); err != nil {
				t.Errorf("since %v", w.Params["since"])
			}
		})
	}
}

// The source: the store's record and the start of the count, not while the clock is untrusted or
// the boot settles, and through the tracker a silenced warning returns as the error.
func TestBackupAgeWarning(t *testing.T) {
	r := fakeRoot(t)
	state := t.TempDir()
	store := &backupcrypt.Store{Dir: state}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	a := &SystemAPI{Root: r, BackupCrypt: store}
	a.warn.now = func() time.Time { return now }
	eval := func() ([]warnings.Warning, bool) { t.Helper(); return a.backupAgeWarning(context.Background()) }

	// first evaluation: no backup, the count starts now
	if ws, ok := eval(); len(ws) != 0 || !ok {
		t.Fatalf("fresh: %v %v", ws, ok)
	}
	if _, err := os.Stat(filepath.Join(state, backupcrypt.KeyDir, backupcrypt.AgeSinceFile)); err != nil {
		t.Fatalf("the start is not kept: %v", err)
	}
	now = now.Add(4 * 24 * time.Hour)
	if ws, _ := eval(); len(ws) != 1 || ws[0].Variant != "warning" || ws[0].Params["way"] != nil {
		t.Fatalf("never, four days: %+v", ws)
	}
	now = now.Add(4 * 24 * time.Hour)
	if ws, _ := eval(); len(ws) != 1 || ws[0].Variant != "error" {
		t.Fatalf("never, eight days: %+v", ws)
	}

	// a target run no target verified is no backup; a download is
	if err := store.RecordCreated(backupcrypt.Created{SHA256: "f", Time: now.Add(-time.Hour), Via: backupcrypt.ViaTarget, Instance: "nightly"}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := eval(); len(ws) != 1 || ws[0].Variant != "error" {
		t.Fatalf("a failed delivery cleared it: %+v", ws)
	}
	if err := store.RecordCreated(backupcrypt.Created{SHA256: "d", Time: now.Add(-time.Hour), Via: backupcrypt.ViaDownload, By: "admin"}); err != nil {
		t.Fatal(err)
	}
	if ws, ok := eval(); len(ws) != 0 || !ok {
		t.Fatalf("after a download: %v %v", ws, ok)
	}

	// the clock: the gate's timeout and chrony not synchronised is unknown, not clear
	now = now.Add(5 * 24 * time.Hour)
	_ = os.MkdirAll(filepath.Join(string(r), "run/occulite"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), system.ClockStateFile), []byte("timeout\n"), 0o644)
	a.Clock = &system.ClockCheck{Root: r, Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Leap status     : Not synchronised\n"), nil
	}}
	if ws, ok := eval(); len(ws) != 0 || ok {
		t.Fatalf("untrusted clock: %v %v", ws, ok)
	}
	_ = os.WriteFile(filepath.Join(string(r), system.ClockStateFile), []byte("ntp\n"), 0o644)
	if ws, ok := eval(); len(ws) != 1 || !ok || ws[0].Params["way"] != "download" || ws[0].Params["by"] != "admin" || ws[0].Params["days"] != 5 {
		t.Fatalf("trusted clock: %+v %v", ws, ok)
	}

	// the boot still starting: unknown
	b := &SystemAPI{Root: r, BackupCrypt: store, BootChart: &bootchart.Reader{Systemctl: func(context.Context, ...string) ([]byte, error) {
		return []byte("FinishTimestampMonotonic=0\n"), nil
	}}}
	b.warn.now = a.warn.now
	if ws, ok := b.backupAgeWarning(context.Background()); len(ws) != 0 || ok {
		t.Fatalf("boot not settled: %v %v", ws, ok)
	}

	// without the store: nothing to say
	if ws, ok := (&SystemAPI{Root: r}).backupAgeWarning(context.Background()); len(ws) != 0 || !ok {
		t.Fatalf("no store: %v %v", ws, ok)
	}
}

// A silenced warning is spent when the age turns it into the error, which is then shown.
func TestBackupAgeSilenceReturnsAsError(t *testing.T) {
	r := fakeRoot(t)
	store := &backupcrypt.Store{Dir: t.TempDir()}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := store.RecordCreated(backupcrypt.Created{SHA256: "d", Time: now.Add(-4 * 24 * time.Hour), Via: backupcrypt.ViaDownload, By: "admin"}); err != nil {
		t.Fatal(err)
	}
	a := &SystemAPI{Root: r, BackupCrypt: store}
	a.warn.now = func() time.Time { return now }
	a.WarningTracker(filepath.Join(t.TempDir(), "warnings.json"), nil)
	a.Warnings.Now = a.warn.now
	find := func() *warnings.Warning {
		for _, w := range a.Warnings.List(context.Background(), "admin", true) {
			if w.ID == "backup-age" {
				return &w
			}
		}
		return nil
	}
	if w := find(); w == nil || w.Variant != "warning" || w.Silenced != nil {
		t.Fatalf("yellow: %+v", w)
	}
	if _, err := a.Warnings.Silence(context.Background(), "admin", "backup-age", "warning", 90); err != nil {
		t.Fatal(err)
	}
	if w := find(); w == nil || w.Silenced == nil {
		t.Fatalf("silenced: %+v", w)
	}
	now = now.Add(4 * 24 * time.Hour)
	if w := find(); w == nil || w.Variant != "error" || w.Severity != "error" || w.Silenced != nil {
		t.Fatalf("red: %+v", w)
	}
}
