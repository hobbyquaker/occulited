package priv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"github.com/hobbyquaker/occulited/internal/manifest"
)

// An addon's backup hook (openccu-lite task 399): before every backup's tar the system runs the
// program the addon's manifest names in its backup block, as the addon's own user and in its
// confinement, so that it writes a consistent snapshot of its data (a database on a USB stick,
// say) into the staging directory inside its tree, which the tar then takes; after the tar the
// staging is emptied again.
//
// The operation is the helper's because the daemon runs as occulite and neither starts a transient
// unit as another user nor empties a directory the addon's user owns. It takes an addon id and a
// step and nothing else: the program, the staging directory, the user, the groups and the writable
// paths all come from the stored manifest and policy, root's files beside each other in
// AddonPolicyDir, never from the request - so a daemon that is lied to can make the helper run an
// addon's declared hook as that addon, which is what the addon's unit does at every start anyway,
// and nothing else.
const opAddonBackupHook = "addon-backup-hook"

// The steps.
const (
	BackupHookSnapshot = "snapshot" // run the hook
	BackupHookClean    = "clean"    // empty the staging directory
)

// BackupHookResult is what one step did.
type BackupHookResult struct {
	Step string `json:"step"`
	// Exit is the hook's exit status (snapshot), 0 for a clean step.
	Exit int `json:"exit"`
	// Output is the hook's stdout and stderr, the last part when it was long.
	Output string `json:"output,omitempty"`
	// TimedOut: the hook ran past its timeout and was killed.
	TimedOut bool `json:"timed_out,omitempty"`
	// Bytes and Files are what the staging directory holds after the step (regular files, no
	// link followed).
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
	// DurationMS is the hook's run time.
	DurationMS int64 `json:"duration_ms"`
}

// The pieces a test replaces.
var (
	backupSystemdRun = "systemd-run"
	backupSystemdDir = "/run/systemd/system" // there when systemd is PID 1
	backupAddonsDir  = "/usr/local/addons"
	backupPolicyDir  = AddonPolicyDir
	backupHasGroup   = func(name string) bool { _, err := user.LookupGroup(name); return err == nil }
	// backupHookGrace is what the kill of a hook past its timeout gets before the helper gives up.
	backupHookGrace = 15 * time.Second
)

// backupHookOutputMax bounds the output kept of a hook.
const backupHookOutputMax = 4096

// BackupHook is the client side.
func (c Client) BackupHook(ctx context.Context, id, step string) (BackupHookResult, error) {
	var out BackupHookResult
	res, err := c.call(ctx, request{Op: opAddonBackupHook, Name: id, Target: step})
	if len(res.Stdout) > 0 {
		if jerr := json.Unmarshal(res.Stdout, &out); jerr != nil && err == nil {
			err = fmt.Errorf("privilege helper: %w", jerr)
		}
	}
	return out, err
}

// backupPolicy is the part of a stored policy the hook needs: the mode and the user, the groups and
// the writable paths the unit has (addonunit.DropIn renders the same).
type backupPolicy struct {
	ID      string `json:"id"`
	Mode    string `json:"mode"`
	UID     int    `json:"uid"`
	Runtime *struct {
		Groups []string `json:"groups"`
		Paths  []string `json:"paths"`
	} `json:"runtime"`
	DataDirs []string `json:"data_dirs"`
}

// BackupHook runs one step for an addon: snapshot runs the manifest's hook as the addon's user in a
// transient unit with the addon unit's confinement and the manifest's timeout; clean empties the
// staging directory. Both answer the staging's size afterwards.
func (Local) BackupHook(ctx context.Context, id, step string) (BackupHookResult, error) {
	var out BackupHookResult
	out.Step = step
	if !addonunit.IDRe.MatchString(id) {
		return out, fmt.Errorf("addon id %q", id)
	}
	if step != BackupHookSnapshot && step != BackupHookClean {
		return out, fmt.Errorf("step %q", step)
	}
	m, err := manifest.ParseFile(filepath.Join(backupPolicyDir, id+AddonManifestSuffix))
	if err != nil {
		return out, fmt.Errorf("the stored manifest of %s: %w", id, err)
	}
	if m.ID != id || m.Backup == nil {
		return out, fmt.Errorf("%s declares no backup block", id)
	}
	// the policy says who the addon is; without one (a busybox product, an addon the system has not
	// adopted) nothing runs
	pb, err := os.ReadFile(filepath.Join(backupPolicyDir, id+".json"))
	if err != nil {
		return out, fmt.Errorf("the policy of %s: %w", id, err)
	}
	var p backupPolicy
	if json.Unmarshal(pb, &p) != nil || p.ID != id {
		return out, fmt.Errorf("the policy of %s does not parse", id)
	}
	confined := p.Mode == "confined"
	if confined && (p.UID < addonunit.MinUID || p.UID > addonunit.MaxUID) {
		return out, fmt.Errorf("the policy of %s names uid %d, not an addon user's", id, p.UID)
	}
	dir := filepath.Join(backupAddonsDir, id)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return out, fmt.Errorf("the directory of %s: %w", id, err)
	}
	staging := filepath.Join(dir, filepath.FromSlash(m.Backup.Staging))
	// inside says whether a path of the addon's tree, through whatever links the addon's user put
	// there, still lies inside the tree: root removes and runs nothing elsewhere
	inside := func(path string) (string, error) {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		if real != realDir && !strings.HasPrefix(real, realDir+"/") {
			return "", fmt.Errorf("%s leads out of the addon's directory", path)
		}
		return real, nil
	}
	switch step {
	case BackupHookSnapshot:
		hook := filepath.Join(dir, filepath.FromSlash(m.Backup.Hook))
		realHook, err := inside(hook)
		if err != nil {
			return out, fmt.Errorf("the hook of %s: %w", id, err)
		}
		if st, err := os.Stat(realHook); err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
			return out, fmt.Errorf("the hook of %s, %s, is not an executable file", id, m.Backup.Hook)
		}
		if st, err := os.Stat(backupSystemdDir); err != nil || !st.IsDir() {
			return out, errors.New("the backup hooks need systemd")
		}
		timeout := time.Duration(m.Backup.TimeoutSeconds()) * time.Second
		args := []string{"--wait", "--pipe", "--collect", "--quiet", "--unit=occulite-backup-hook-" + id,
			"-p", "RuntimeMaxSec=" + strconv.Itoa(int(timeout/time.Second)),
			"-p", "NoNewPrivileges=yes", "-p", "PrivateTmp=yes", "-p", "ProtectHome=yes",
			"-p", "ProtectKernelTunables=yes", "-p", "ProtectKernelModules=yes", "-p", "ProtectControlGroups=yes",
			"-p", "WorkingDirectory=" + dir}
		if confined {
			userName := "addon-" + id
			args = append(args, "-p", "User="+userName, "-p", "Group="+userName)
			var groups []string
			if p.Runtime != nil {
				for _, g := range p.Runtime.Groups {
					if !manifest.DeniedConfinedGroup(g) && backupHasGroup(g) {
						groups = append(groups, g)
					}
				}
			}
			if backupHasGroup("certs") {
				groups = append(groups, "certs")
			}
			if len(groups) > 0 {
				args = append(args, "-p", "SupplementaryGroups="+strings.Join(groups, " "))
			}
			// the addon unit's writable paths (addonunit.DropIn.Render), "-" so a missing one
			// does not fail the unit
			paths := []string{"/usr/local/addons/" + id, "/usr/local/etc/config/addons/" + id, "/usr/local/etc/config/rc.d", "/run", "/var/log", "/tmp", "/var/tmp"}
			paths = append(paths, p.DataDirs...)
			if p.Runtime != nil {
				paths = append(paths, p.Runtime.Paths...)
			}
			for i := range paths {
				paths[i] = "-" + paths[i]
			}
			args = append(args, "-p", "CapabilityBoundingSet=", "-p", "ProtectSystem=strict", "-p", "ReadWritePaths="+strings.Join(paths, " "))
		}
		args = append(args, "--", hook)
		cctx, cancel := context.WithTimeout(ctx, timeout+backupHookGrace)
		defer cancel()
		started := time.Now()
		cmd := exec.CommandContext(cctx, backupSystemdRun, args...)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		runErr := cmd.Run()
		out.DurationMS = time.Since(started).Milliseconds()
		if buf.Len() > backupHookOutputMax {
			out.Output = "…" + buf.String()[buf.Len()-backupHookOutputMax:]
		} else {
			out.Output = buf.String()
		}
		var ee *exec.ExitError
		switch {
		case runErr == nil:
		case errors.As(runErr, &ee):
			out.Exit = ee.ExitCode()
			if out.Exit < 0 {
				out.Exit = 255
			}
		default:
			return out, fmt.Errorf("running the hook of %s: %w", id, runErr)
		}
		// systemd-run --wait answers the unit's result; one killed at RuntimeMaxSec= ends
		// non-zero after the timeout
		if out.Exit != 0 && (cctx.Err() != nil || time.Since(started) >= timeout) {
			out.TimedOut = true
		}
	case BackupHookClean:
		real, err := inside(staging)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return out, nil // nothing was written: nothing to empty
			}
			return out, fmt.Errorf("the staging of %s: %w", id, err)
		}
		if st, err := os.Lstat(real); err != nil || !st.IsDir() {
			return out, fmt.Errorf("the staging of %s, %s, is not a directory", id, m.Backup.Staging)
		}
		entries, err := os.ReadDir(real)
		if err != nil {
			return out, fmt.Errorf("the staging of %s: %w", id, err)
		}
		for _, e := range entries {
			// RemoveAll follows no link: a link in the staging goes, its target stays
			if err := os.RemoveAll(filepath.Join(real, e.Name())); err != nil {
				return out, fmt.Errorf("emptying the staging of %s: %w", id, err)
			}
		}
	}
	out.Bytes, out.Files = stagingSize(staging)
	return out, nil
}

// stagingSize sums the regular files below dir, following no link; 0 when it is not there.
func stagingSize(dir string) (bytes int64, files int) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.Type().IsRegular() {
			if st, err := d.Info(); err == nil {
				bytes += st.Size()
				files++
			}
		}
		return nil
	})
	return bytes, files
}

// backupHook is the operation at the boundary: an id and a step, nothing else; the work is Local's.
func (s *Server) backupHook(req request) response {
	if !reflect.DeepEqual(req, request{Op: req.Op, Name: req.Name, Target: req.Target}) {
		return refuse("addon-backup-hook takes an addon id and a step and nothing else")
	}
	if !addonunit.IDRe.MatchString(req.Name) {
		s.log("helper: refused the backup hook of %q: not an addon id", req.Name)
		return refuse("addon id " + req.Name)
	}
	if req.Target != BackupHookSnapshot && req.Target != BackupHookClean {
		return refuse("step " + req.Target)
	}
	// Local's operation, whatever wraps s.Ops: it reads root's own files and follows no path the
	// daemon named, so the nofollow policy of the file operations has nothing to add here
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(manifest.BackupHookTimeoutMax)*time.Second+2*backupHookGrace)
	defer cancel()
	res, err := Local{}.BackupHook(ctx, req.Name, req.Target)
	if err != nil {
		s.log("helper: the backup hook of %s (%s) failed: %v", req.Name, req.Target, err)
		return response{Error: err.Error()}
	}
	b, err := json.Marshal(res)
	if err != nil {
		return response{Error: err.Error()}
	}
	if req.Target == BackupHookSnapshot {
		s.info("helper: backup hook of %s ran: exit %d, %d bytes in the staging", req.Name, res.Exit, res.Bytes)
	}
	return response{OK: true, Stdout: b}
}
