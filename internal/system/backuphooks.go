package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/manifest"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// The addons' part in the system backup (openccu-lite task 399, the maintainer's Q&A of
// 2026-10-28): an addon declares in its manifest's backup block a hook that writes a consistent
// snapshot of its data into a staging directory inside its tree. Before every backup - the one
// downloaded in the browser, Back up now, the nightly run, every target - occulited runs the hooks
// of the addons switched on, as each addon's own user through the privilege helper
// (priv.BackupHook), createBackup.sh's tar takes the staging like any other content of /usr/local,
// and the staging is emptied afterwards. A hook that fails or runs past its timeout does not stop
// the backup: the backup goes on without that addon's snapshot, the result is kept for the Backup
// page and the Status warning, and the log says so.
//
// The switch per addon: on by default for a snapshot up to manifest.BackupLargeMB, off above it,
// the user's choice either way, kept in the state file beside the last result of each addon.

// BackupHookRunner runs one step of an addon's hook: priv.Client through the helper (the daemon),
// priv.Local as root (`occulited -backup create`).
type BackupHookRunner interface {
	BackupHook(ctx context.Context, id, step string) (priv.BackupHookResult, error)
}

// BackupHooks is the daemon's (or the backup unit's) view of the declared hooks.
type BackupHooks struct {
	Root Root
	// Runner is nil where the hooks cannot run (no systemd); Declared still lists the addons.
	Runner BackupHookRunner
	// StateFile keeps the switches and the last results: <state-dir>/backup-addons.json.
	StateFile string
	// UID and GID are the daemon's user: a file written as root (the backup unit) is given to it,
	// so the daemon can write it next. -1 leaves the owner alone.
	UID, GID int
	Log      *slog.Logger
	Now      func() time.Time
	mu       sync.Mutex
}

// BackupHookResult is what the last run of an addon's hook did, as the API shows it.
type BackupHookResult struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
	// OK: the hook ran and exited 0 within its timeout.
	OK bool `json:"ok"`
	// Exit is the hook's exit status, Error why it did not run or what failed.
	Exit  int    `json:"exit,omitempty"`
	Error string `json:"error,omitempty"`
	// Output is the hook's own output (the last part when long).
	Output   string `json:"output,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
	// Bytes and Files: the snapshot the hook left in the staging.
	Bytes      int64 `json:"bytes"`
	Files      int   `json:"files"`
	DurationMS int64 `json:"duration_ms"`
}

// BackupHookAddon is one addon that declares a backup block, with its switch and its last result.
type BackupHookAddon struct {
	ID      string        `json:"id"`
	Name    manifest.Text `json:"name"`
	Hook    string        `json:"hook"`
	Staging string        `json:"staging"`
	Restore string        `json:"restore,omitempty"`
	// Timeout is the hook's limit in seconds, the default filled in.
	Timeout int `json:"timeout"`
	// SizeHintMB is the manifest's estimate; Large says it is above manifest.BackupLargeMB, so the
	// snapshot is off by default.
	SizeHintMB int  `json:"size_hint_mb"`
	Large      bool `json:"large"`
	// Enabled is whether the hook runs: the user's choice (Chosen) when there is one, else !Large.
	Enabled bool  `json:"enabled"`
	Chosen  *bool `json:"chosen,omitempty"`
	// Last is the last run's result, absent before the first.
	Last *BackupHookResult `json:"last,omitempty"`
}

// backupHookState is the state file.
type backupHookState struct {
	On   []string                    `json:"on,omitempty"`
	Off  []string                    `json:"off,omitempty"`
	Last map[string]BackupHookResult `json:"last,omitempty"`
}

func (h *BackupHooks) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func (h *BackupHooks) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Supported says whether hooks can run here: a systemd system with a runner.
func (h *BackupHooks) Supported() bool { return h != nil && h.Runner != nil }

func (h *BackupHooks) readState() backupHookState {
	var s backupHookState
	if h.StateFile == "" {
		return s
	}
	b, err := os.ReadFile(h.StateFile)
	if err != nil {
		return s
	}
	if json.Unmarshal(b, &s) != nil {
		return backupHookState{}
	}
	return s
}

func (h *BackupHooks) writeState(s backupHookState) error {
	if h.StateFile == "" {
		return errors.New("no state file")
	}
	sort.Strings(s.On)
	sort.Strings(s.Off)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// the daemon's own file in its own state directory: written directly, not through the helper
	// (whose write list does not and need not know it); the backup unit writes it as root and
	// gives it to the daemon's user
	tmp, err := os.CreateTemp(filepath.Dir(h.StateFile), ".backup-addons-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if os.Geteuid() == 0 && h.UID >= 0 {
		_ = tmp.Chown(h.UID, h.GID)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), h.StateFile); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Declared lists the installed addons whose stored manifest declares a backup block, by id.
func (h *BackupHooks) Declared() []BackupHookAddon {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.declared(h.readState())
}

func (h *BackupHooks) declared(s backupHookState) []BackupHookAddon {
	var out []BackupHookAddon
	for id := range h.Root.AddonPolicies() {
		m := h.Root.ReadAddonManifest(id)
		if m == nil || m.Backup == nil {
			continue
		}
		a := BackupHookAddon{ID: id, Name: m.Name, Hook: m.Backup.Hook, Staging: m.Backup.Staging, Restore: m.Backup.Restore,
			Timeout: m.Backup.TimeoutSeconds(), SizeHintMB: m.Backup.SizeHintMB, Large: m.Backup.Large()}
		a.Enabled = !a.Large
		switch {
		case slices.Contains(s.On, id):
			on := true
			a.Chosen, a.Enabled = &on, true
		case slices.Contains(s.Off, id):
			off := false
			a.Chosen, a.Enabled = &off, false
		}
		if last, ok := s.Last[id]; ok {
			last.ID = id
			a.Last = &last
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ErrNoBackupBlock: the addon declares no backup block (or is not installed).
var ErrNoBackupBlock = errors.New("the addon declares no backup block")

// SetEnabled is the switch of one declared addon; the choice stays until changed, whatever the
// manifest's size hint says.
func (h *BackupHooks) SetEnabled(id string, on bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.readState()
	if !slices.ContainsFunc(h.declared(s), func(a BackupHookAddon) bool { return a.ID == id }) {
		return ErrNoBackupBlock
	}
	s.On = slices.DeleteFunc(s.On, func(x string) bool { return x == id })
	s.Off = slices.DeleteFunc(s.Off, func(x string) bool { return x == id })
	if on {
		s.On = append(s.On, id)
	} else {
		s.Off = append(s.Off, id)
	}
	return h.writeState(s)
}

// Run runs the hook of every declared addon that is switched on, one after another, and keeps each
// result; the backup's tar follows. A failure is logged and kept, never returned: the backup goes
// on. Nothing runs where hooks are not supported.
func (h *BackupHooks) Run(ctx context.Context) []BackupHookResult {
	if !h.Supported() {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.readState()
	var out []BackupHookResult
	for _, a := range h.declared(s) {
		if !a.Enabled {
			continue
		}
		r := BackupHookResult{ID: a.ID, At: h.now()}
		res, err := h.Runner.BackupHook(ctx, a.ID, priv.BackupHookSnapshot)
		r.Exit, r.Output, r.TimedOut, r.Bytes, r.Files, r.DurationMS = res.Exit, res.Output, res.TimedOut, res.Bytes, res.Files, res.DurationMS
		switch {
		case err != nil:
			r.Error = err.Error()
		case res.TimedOut:
			r.Error = fmt.Sprintf("the hook ran past its %d s and was stopped", a.Timeout)
		case res.Exit != 0:
			r.Error = fmt.Sprintf("the hook exited %d", res.Exit)
		default:
			r.OK = true
		}
		if r.OK {
			h.log().Info("backup: addon snapshot written", "addon", a.ID, "bytes", r.Bytes, "files", r.Files, "ms", r.DurationMS)
		} else {
			h.log().Warn("backup: an addon's snapshot hook failed; the backup goes on without it", "addon", a.ID, "err", r.Error, "output", strings.TrimSpace(r.Output))
		}
		if s.Last == nil {
			s.Last = map[string]BackupHookResult{}
		}
		s.Last[a.ID] = r
		out = append(out, r)
	}
	if len(out) > 0 {
		if err := h.writeState(s); err != nil {
			h.log().Warn("backup: the addon hooks' results could not be kept", "err", err)
		}
	}
	return out
}

// Clean empties the staging of every addon whose hook Run ran (failed ones too: a half-written
// snapshot is not kept), after the tar. Errors are logged.
func (h *BackupHooks) Clean(ctx context.Context, ran []BackupHookResult) {
	if !h.Supported() {
		return
	}
	for _, r := range ran {
		if _, err := h.Runner.BackupHook(ctx, r.ID, priv.BackupHookClean); err != nil {
			h.log().Warn("backup: an addon's staging could not be emptied", "addon", r.ID, "err", err)
		}
	}
}

// Failed lists the declared addons that are switched on and whose last run failed: the Status
// warning backup-addon.
func (h *BackupHooks) Failed() []BackupHookAddon {
	var out []BackupHookAddon
	for _, a := range h.Declared() {
		if a.Enabled && a.Last != nil && !a.Last.OK {
			out = append(out, a)
		}
	}
	return out
}

// CreateBackupWithHooks is CreateBackup with the addons' hooks around it: the snapshots first, the
// script, then the stagings emptied. The hooks' results come back beside the file; hooks is nil
// where there are none.
func (r Root) CreateBackupWithHooks(ctx context.Context, run Runner, hooks *BackupHooks) (string, []BackupHookResult, error) {
	var ran []BackupHookResult
	if hooks != nil {
		ran = hooks.Run(ctx)
	}
	path, err := r.CreateBackup(ctx, run)
	if hooks != nil {
		hooks.Clean(context.WithoutCancel(ctx), ran)
	}
	return path, ran, err
}
