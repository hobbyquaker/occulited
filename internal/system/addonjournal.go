package system

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hobbyquaker/occulited/internal/journald"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// ---- the installer's output in the journal ----------------------------------------------------
//
// The journal is the one log of the box (D-59). An install's output was only in the result the
// page showed, and gone with the page - a failed install could not be read afterwards. Now every
// install and uninstall also writes its output into the journal: an entry per line, identifier
// addon-install, the addon as ADDON_ID where it is known, and a closing summary with the exit
// code as ADDON_EXIT, priority info on success and err on failure.
//
// The script runs through the privilege helper, which hands its output back in one piece when it
// has ended; the entries are written then, in occulited, which is the process that can reach the
// journal's socket. That is also when an uploaded archive's addon id is known: the rc.d entry the
// installer created or changed. An upload whose installer changed no rc.d entry (a failed install,
// most often) has no id to give, and its entries carry no ADDON_ID.
//
// Best effort: written in the background once the result is there, an error only logged.

const (
	addonInstallIdent = "addon-install"
	// a longer line is split into entries of this size, as journald splits a long line on a
	// unit's stdout
	journalLineMax = 16 << 10
	// the most output lines one run writes: the last ones, where an install fails - npm's output
	// could otherwise take the rate limit of occulited's unit for everything else it logs
	journalLinesMax = 2000
)

type addonIDKey struct{}

// WithAddonID tells Install which addon the archive is, when the caller knows it: the catalogue
// does, an upload does not.
func WithAddonID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, addonIDKey{}, id)
}

func addonIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(addonIDKey{}).(string)
	return id
}

// installedID is the addon an install was about: the caller's word, or else the one addon whose
// rc.d entry the installer created or changed. None or several: not known.
func installedID(ctx context.Context, fresh, touched []string) string {
	if id := addonIDFrom(ctx); id != "" {
		return id
	}
	switch {
	case len(fresh) == 1 && len(touched) == 0:
		return fresh[0]
	case len(fresh) == 0 && len(touched) == 1:
		return touched[0]
	}
	return ""
}

// addonRun is one run of an installer or an uninstall script as the journal gets it.
type addonRun struct {
	action  string // "install" or "uninstall"
	id      string // "" = not known
	output  string
	ran     bool   // the script ran to an exit code; false: err says what stopped it
	exit    int    // the exit code, when ran
	meaning string // what the exit code means, when known
	err     error
	// removed is what the system removed after an uninstall script (UninstallResult.SystemRemoved),
	// asUser the user a confined addon's script ran as (occulited B-26): the journal says it after
	// the script's lines, so the script's refused rm lines read as handled
	removed []string
	asUser  string
	// forced (occulited B-136): a disabled addon's uninstall script failed and the system removed
	// its rc.d entry and its directory anyway - one more line, a warning, before the summary
	forced bool
}

func (r addonRun) failed() bool {
	if r.action == "install" {
		return r.exit != 0 && r.exit != 10 // 10 is installed, reboot required
	}
	return r.exit != 0
}

// entries are the run's journal entries: the output lines, then the summary.
func (r addonRun) entries() [][]journald.Field {
	var out [][]journald.Field
	entry := func(msg string, priority int) []journald.Field {
		f := []journald.Field{
			{Key: "MESSAGE", Value: msg},
			{Key: "PRIORITY", Value: strconv.Itoa(priority)},
			{Key: "SYSLOG_IDENTIFIER", Value: addonInstallIdent},
		}
		if r.id != "" {
			f = append(f, journald.Field{Key: "ADDON_ID", Value: r.id})
		}
		return append(f, journald.Field{Key: "ADDON_ACTION", Value: r.action})
	}
	for _, line := range journalLines(r.output) {
		out = append(out, entry(line, journald.PriorityInfo))
	}
	prefix := ""
	if r.id != "" {
		prefix = r.id + ": "
	}
	if note := r.removedNote(); note != "" {
		out = append(out, entry(prefix+note, journald.PriorityInfo))
	}
	if r.forced {
		out = append(out, entry(prefix+"the addon was disabled: its rc.d entry and its directory were removed although its uninstall script failed", journald.PriorityWarning))
	}
	if !r.ran {
		return append(out, entry(fmt.Sprintf("%s%s failed: %v", prefix, r.action, r.err), journald.PriorityErr))
	}
	msg := fmt.Sprintf("%s%s finished, exit %d", prefix, r.action, r.exit)
	if r.meaning != "" {
		msg += " (" + r.meaning + ")"
	}
	priority := journald.PriorityInfo
	if r.failed() {
		priority = journald.PriorityErr
	}
	return append(out, append(entry(msg, priority), journald.Field{Key: "ADDON_EXIT", Value: strconv.Itoa(r.exit)}))
}

// removedNote is the journal's line after a confined or any uninstall script: as whom it ran, and
// what the system removed afterwards; "" when there is nothing to say.
func (r addonRun) removedNote() string {
	if r.action != "uninstall" || (len(r.removed) == 0 && r.asUser == "") {
		return ""
	}
	note := ""
	if r.asUser != "" {
		note = "the uninstall ran as the addon's user " + r.asUser + ", as a confined addon's does"
	}
	if len(r.removed) > 0 {
		if note != "" {
			note += "; "
		}
		note += "the system then removed: " + strings.Join(r.removed, ", ")
	}
	return note
}

// journalLines is the output as the journal's lines: blank lines left out, a line redrawn with
// carriage returns (a progress bar) as the text a terminal ends up showing, a very long line split,
// and at most journalLinesMax lines - the last ones, after a line that says how many are left out.
func journalLines(s string) []string { return journalLinesN(s, journalLinesMax) }

// journalLinesN is journalLines with the caller's cap (the recovery log keeps more lines).
func journalLinesN(s string, max int) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if i := strings.LastIndex(l, "\r"); i >= 0 {
			l = l[i+1:]
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		for len(l) > journalLineMax {
			n := journalLineMax
			for n > 0 && !utf8.RuneStart(l[n]) {
				n--
			}
			if n == 0 {
				n = journalLineMax // not UTF-8 at all: cut where the size says
			}
			out = append(out, l[:n])
			l = l[n:]
		}
		out = append(out, l)
	}
	if len(out) > max {
		left := len(out) - max
		out = append([]string{fmt.Sprintf("(the first %d lines of the output are left out here)", left)}, out[left:]...)
	}
	return out
}

// journalAddonRun writes a run's entries in the background: the caller's answer never waits for
// the journal, and an error is only logged.
func (a *SystemdAddons) journalAddonRun(r addonRun) {
	if a.Journal == nil {
		return
	}
	w, entries := a.Journal, r.entries()
	go func() {
		if err := w.Send(entries...); err != nil {
			slog.Warn("journal: the addon "+r.action+" output could not be written", "addon", r.id, "err", err)
		}
	}()
}

// journalUninstall journals an uninstall: its script's output and how it ended. A refusal before
// any script ran (an unknown addon) is the request's answer and not a run, so it is left out.
func (a *SystemdAddons) journalUninstall(id string, res UninstallResult, asUser string, err error) {
	r := addonRun{action: "uninstall", id: id, output: res.Output, ran: true, meaning: "uninstalled", removed: res.SystemRemoved, asUser: asUser, forced: res.Forced}
	if res.Forced {
		err = res.scriptErr // B-136: the uninstall succeeded, the script did not
	}
	var pe *priv.ExitError
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(err, errInvalidAddonID), errors.Is(err, errUnknownAddon):
		return
	case errors.As(err, &pe):
		r.exit, r.meaning = pe.ExitCode(), "uninstall script failed"
	case errors.As(err, &ee):
		r.exit, r.meaning = ee.ExitCode(), "uninstall script failed"
	default:
		r.ran, r.err = false, err
	}
	a.journalAddonRun(r)
}
