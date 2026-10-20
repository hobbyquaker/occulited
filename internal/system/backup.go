package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// Backup and restore use the firmware's own scripts (task 11): createBackup.sh produces a CCU
// compatible .sbk (tar of usr_local.tar.gz + signature + key index + firmware version),
// restoreBackup.sh checks and applies one. occulited adds the API, the temporary files and the
// honesty about what a backup from a system with ReGa contains that this one cannot use.

// BackupDir is where the firmware keeps temporary backups; /usr/local/tmp is excluded from the
// archive itself and survives nothing, which is exactly right for a download staging area.
const BackupDir = "/usr/local/tmp"

// CreateBackup writes a fresh .sbk into BackupDir and returns its path; the caller streams and
// removes it. A key-protected system signs the archive with its key, as the WebUI's backup does.
//
// The script always runs to its end (occulited B-91): the privilege helper never stopped it when
// the request went away, and a stop half-way - a SIGKILL from exec.CommandContext where occulited
// runs it itself - skips its `trap "rm -rf ${TMPDIR}" EXIT`, leaving a temporary directory the size
// of /usr/local behind. So ctx is not handed to the script; when it has ended by the time the script
// is done, the .sbk is removed here and ctx's error returned, and nothing is left behind either way.
func (r Root) CreateBackup(ctx context.Context, run Runner) (string, error) {
	if run == nil {
		run = ExecRunner
	}
	ver := r.ReadVersion().Version
	if ver == "" {
		ver = "unknown"
	}
	host := strings.TrimSpace(readFile(r.join("/proc/sys/kernel/hostname")))
	if host == "" {
		host = "openccu-lite"
	}
	name := fmt.Sprintf("%s-%s-%s.sbk", host, ver, time.Now().Format("2006-01-02-1504"))
	path := filepath.Join(r.join(BackupDir), name)
	out, err := run(context.WithoutCancel(ctx), r.join("/bin/createBackup.sh"), path)
	if ctx.Err() != nil {
		if _, serr := os.Lstat(path); serr == nil {
			if rerr := r.RemoveBackupFile(path); rerr != nil {
				slog.Warn("backup: the file of a cancelled backup was not removed", "path", path, "err", rerr)
			}
		}
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("createBackup.sh: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		return "", errors.New("createBackup.sh produced no file")
	}
	return path, nil
}

// backupTempRe is the name mktemp -d gives createBackup.sh's working directory in BackupDir
// (tmp.XXXXXX; busybox and coreutils alike).
var backupTempRe = regexp.MustCompile(`^tmp\.[A-Za-z0-9]{6,}$`)

// BackupTempMaxAge is how old (its last change) a working directory of createBackup.sh in
// BackupDir must be before SweepBackupTemp takes it for a leftover. The script adds an entry to it
// at every step, and the longest step - the tar of /usr/local - takes minutes, not hours.
const BackupTempMaxAge = 12 * time.Hour

// SweepBackupTemp removes the working directories createBackup.sh left in BackupDir (occulited
// B-91): its EXIT trap removes them, but not when the script was killed - the helper restarted, the
// power gone - and each holds a copy of /usr/local. Only directories named as mktemp names them and
// unchanged for longer than maxAge go (they are root's 0700, so their content is not looked at);
// anything else in BackupDir - an uploaded backup waiting for its restore - is never touched.
// Returns the names removed.
func (r Root) SweepBackupTemp(now time.Time, maxAge time.Duration) []string {
	dir := r.join(BackupDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() || !backupTempRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if err := Priv.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			slog.Warn("backup: a leftover working directory was not removed", "dir", e.Name(), "err", err)
			continue
		}
		removed = append(removed, e.Name())
	}
	if len(removed) > 0 {
		slog.Info("backup: leftover working directories of createBackup.sh removed", "dirs", strings.Join(removed, ", "))
	}
	return removed
}

// RestoreCheck is the parsed result of restoreBackup.sh -c.
type RestoreCheck struct {
	OK             bool   `json:"ok"`
	Output         string `json:"output"`
	BackupVersion  string `json:"backup_version,omitempty"`
	RunningVersion string `json:"running_version,omitempty"`
	NeedsKey       bool   `json:"needs_key"` // the backup or the system is protected by a security key
	HasRega        bool   `json:"has_rega"`  // the archive carries a ReGa database this system cannot use
	// BackupKey and SystemKey (openccu-lite task 296) say which side has a BidCos security key
	// other than the factory key, as the script tells them apart; KeyIndex is the backup's
	// key_index (the API fills it in).
	BackupKey bool `json:"backup_key"`
	SystemKey bool `json:"system_key"`
	KeyIndex  int  `json:"key_index"`
}

var restoreVersionRe = regexp.MustCompile(`generated on ([^,\s]+), applying to ([^,\s]+)`)

// SaveUpload stores an uploaded plain .sbk under BackupDir with a safe name and returns the path;
// SaveUploadSniffed (backupcrypt.go) is what the API uses since task 91, this is its plain case.
func (r Root) SaveUpload(src io.Reader, name string) (string, error) {
	return r.storeUpload(io.LimitReader(src, 2<<30), strings.TrimSuffix(safeUploadName(name), ".age"))
}

// RemoveBackupFile deletes a file under BackupDir through the privilege boundary: the API layer
// removes the .sbk it has just streamed, and that directory is root's (B-20).
func (r Root) RemoveBackupFile(path string) error {
	if filepath.Dir(path) != r.join(BackupDir) {
		return fmt.Errorf("not a file under %s", BackupDir)
	}
	return Priv.Remove(path)
}

// CheckBackup runs restoreBackup.sh -c on path and looks inside for a ReGa database. The script
// asks for the security key on its standard input when the backup or the system has one; the check
// answers with an empty line and -f, so that it goes on past the missing key to the end and says
// which side is protected (openccu-lite task 296: before, it met EOF at the prompt, or stopped at
// the first mismatch, and every backup with a key was "rejected"). The passphrase itself is checked
// by the API (BackupSignature, SystemKeyMatches), never through this output.
func (r Root) CheckBackup(ctx context.Context, run StdinRunner, path string) RestoreCheck {
	if run == nil {
		run = ExecStdinRunner
	}
	out, err := run(ctx, []byte("\n"), r.join("/bin/restoreBackup.sh"), "-c", "-f", path)
	c := RestoreCheck{OK: err == nil, Output: checkKeyNoteRe.ReplaceAllString(strings.TrimSpace(string(out)), "the passphrase is asked for below.")}
	if m := restoreVersionRe.FindStringSubmatch(c.Output); m != nil {
		c.BackupVersion, c.RunningVersion = m[1], m[2]
	}
	c.NeedsKey = strings.Contains(c.Output, "backup and/or system protected by security key")
	c.BackupKey = strings.Contains(c.Output, "backup protected with a key")
	c.SystemKey = strings.Contains(c.Output, "system protected with a key")
	// tar -tzf on the inner archive is cheap enough to answer the ReGa question
	if list, err := runOutput(ctx, "sh", "-c", "tar -xOf "+shellQuote(path)+" usr_local.tar.gz 2>/dev/null | tar -tzf - 2>/dev/null | grep -c 'etc/config/homematic.regadom$'"); err == nil && strings.TrimSpace(string(list)) != "0" {
		c.HasRega = true
	}
	return c
}

// checkKeyNoteRe is the script's verdict on the empty key the check gives it - "does NOT match",
// "forced restore" - which would read as a failed check; the page asks for the passphrase instead.
var checkKeyNoteRe = regexp.MustCompile(`WARNING: security key does NOT match (?:system key|key in backup), forced restore\.`)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// StdinRunner executes one external command with a standard input. restoreBackup.sh reads the
// security key from it, and a Runner has none: with the API's Runner the script met EOF at its
// prompt and stopped (B-144), on every box or backup with a key.
type StdinRunner func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)

// ExecStdinRunner runs the command for real, through the helper, with stdin.
func ExecStdinRunner(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return priv.AsExitError(Priv.Run(ctx, name, args, stdin))
}

// DryStdinRunner logs instead of executing, as DryRunner does; the input itself (a key) is not
// logged.
func DryStdinRunner(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	slog.Info("dry run", "cmd", name, "args", args, "stdin_bytes", len(stdin))
	return nil, nil
}

// RestoreBackup stages path with restoreBackup.sh: the script checks the archive, sets the
// security key when the backup brings one, moves the tree to /usr/local/tmp and marks
// /usr/local/.doBackupRestore for the next boot. It does not reboot (no -r): the caller does,
// once its answer is on the way - with -r the script rebooted under the open request, the
// request's context died with the daemon and the route answered 422 although the restore was
// under way (openccu-lite B-193). key answers the script's prompt when the backup or the system
// is protected - one line on stdin, empty when there is no key; force skips the key check.
// run nil = exec for real.
func (r Root) RestoreBackup(ctx context.Context, run StdinRunner, path, key string, force bool) (string, error) {
	if run == nil {
		run = ExecStdinRunner
	}
	var args []string
	if force {
		args = append(args, "-f")
	}
	args = append(args, path)
	out, err := run(ctx, []byte(key+"\n"), r.join("/bin/restoreBackup.sh"), args...)
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("restoreBackup.sh: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// BootMarkerCarryFile is where the reboot countdown's marker of a restore waits across the restore
// at boot, beside the system's identity (CarryFile, task 91): S05CheckBackupRestore deletes
// everything under /usr/local except tmp - the state directory with the marker included - before
// it unpacks the backup, so a restore's reboot was never recorded (openccu-lite B-193). A dotfile
// in /usr/local/tmp is in no backup and outlives both the restore and S06InitSystem's emptying.
const BootMarkerCarryFile = BackupDir + "/.occulite-boot-timing"

// CarryBootMarker puts the marker into BootMarkerCarryFile: staged as occulited (so the daemon
// reads it back at the next start) and moved into root's /usr/local/tmp by the helper.
func (r Root) CarryBootMarker(marker []byte) error {
	tmp, _, err := stageFile(r, ".occulite-boot-timing.part", strings.NewReader(string(marker)))
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := Priv.Rename(tmp, r.join(BootMarkerCarryFile)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// RemoveCarriedBootMarker takes the carried marker away again (the helper; a missing file is fine).
func (r Root) RemoveCarriedBootMarker() error {
	return Priv.Remove(r.join(BootMarkerCarryFile))
}
