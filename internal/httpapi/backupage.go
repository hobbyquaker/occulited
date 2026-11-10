package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// backup-age (occulited task 140, the maintainer 2026-11-10): no backup has left the system for
// more than three days (warning) or seven (error) - by any way: a target run a target verified, a
// download in the browser, a program's fetch, or an entry from before the way was recorded
// (backupcrypt.Store.Latest). A failed delivery is no backup; backup-delivery says that for itself.
const (
	backupAgeWarn  = 3 * 24 * time.Hour
	backupAgeError = 7 * 24 * time.Hour
)

// backupAgeRule is the rule without the system: latest is Store.Latest, since is when the count
// began for a system that never made a backup (Store.AgeSince; read only then). The variant is the
// severity step, so a silenced warning is spent when it turns into the error and the error is
// shown; a backup clears it, and the next time the age passes three days it is new again.
func backupAgeRule(latest map[string]backupcrypt.Created, since func() (time.Time, error), now time.Time, targetName func(string) string) ([]warnings.Warning, bool) {
	var newest backupcrypt.Created
	way := ""
	for w, c := range latest {
		if way == "" || c.Time.After(newest.Time) {
			newest, way = c, w
		}
	}
	from := newest.Time
	if way == "" {
		t, err := since()
		if err != nil {
			slog.Warn("warnings: the start of the backup age could not be kept", "err", err)
		}
		if t.IsZero() {
			return nil, false
		}
		from = t
	}
	age := now.Sub(from)
	if age <= backupAgeWarn {
		return nil, true
	}
	sev := warnings.SeverityWarning
	if age > backupAgeError {
		sev = warnings.SeverityError
	}
	params := map[string]any{"days": int(age / (24 * time.Hour)), "since": from.UTC().Format(time.RFC3339)}
	if way != "" {
		params["way"] = way
		if newest.By != "" {
			params["by"] = newest.By
		}
		if len(newest.Targets) > 0 {
			names := make([]string, len(newest.Targets))
			for i, id := range newest.Targets {
				names[i] = targetName(id)
			}
			params["targets"] = strings.Join(names, ", ")
		}
	}
	return []warnings.Warning{{ID: "backup-age", Variant: sev, Severity: sev, Href: "/backup", Params: params}}, true
}

// backupAgeWarning evaluates backup-age. Not while the boot settles or the clock is not trusted:
// a system without a real-time clock starts in the image's past until a time server answers, and
// would warn wrongly - or start the count of a system that never made a backup at a wrong time.
// Unknown then, so a silence of it is kept.
func (a *SystemAPI) backupAgeWarning(ctx context.Context) ([]warnings.Warning, bool) {
	if a.BackupCrypt == nil {
		return nil, true
	}
	if !a.bootSettled() || !a.clockTrusted(ctx) {
		return nil, false
	}
	now := time.Now()
	if a.warn.now != nil {
		now = a.warn.now()
	}
	since := func() (time.Time, error) { return a.BackupCrypt.AgeSince(now) }
	return backupAgeRule(a.BackupCrypt.Latest(), since, now, a.backupTargetName)
}

// clockTrusted: the boot's clock gate found a real-time clock, a time server or a time set by
// hand, or chrony has synchronised since its timeout (system.ClockCheck). No gate (an image or a
// container without it, the tests) is trusted.
func (a *SystemAPI) clockTrusted(ctx context.Context) bool {
	if a.Clock == nil {
		return true
	}
	st := a.Clock.Status(ctx)
	return st == nil || st.Synchronised
}

// backupTargetName is a target's name for a sentence; its id when it is gone.
func (a *SystemAPI) backupTargetName(id string) string {
	if a.BackupTargets != nil {
		if t, ok, err := a.BackupTargets.Store.Get(id); err == nil && ok && t.Name != "" {
			return t.Name
		}
	}
	return id
}
