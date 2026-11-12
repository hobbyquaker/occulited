package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/backuptarget"
	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/logctl"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/shares"
	"github.com/hobbyquaker/occulited/internal/system"
)

// backupFlag is `occulited -backup create|deliver <instance>` (openccu-lite task 86): the halves of
// the nightly run and of Back up now, run by the fork's occu-backup-create@.service (root) and
// occu-backup-deliver@.service (occulite). A flag, not a word: an older occulited handed an unknown
// word would start a second daemon - as root, from a timer at night; handed an unknown flag it exits
// 2 before anything else happens (the addonOwnFlag lesson).
const backupFlag = "-backup"

const backupUsage = "usage: occulited -backup create|deliver <nightly|all|target-id> [--state-dir DIR]"

// backupArgs parses the command line: the step, the instance, the state directory.
func backupArgs(args []string) (step, instance, stateDir string, err error) {
	stateDir = "/usr/local/etc/occulite"
	var rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--state-dir" || a == "-state-dir":
			if i+1 >= len(args) {
				return "", "", "", errors.New(backupUsage)
			}
			stateDir = args[i+1]
			i++
		case strings.HasPrefix(a, "-"):
			return "", "", "", fmt.Errorf("unknown option %s; %s", a, backupUsage)
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) != 2 || (rest[0] != "create" && rest[0] != "deliver") || !backuptarget.ValidInstance(rest[1]) {
		return "", "", "", errors.New(backupUsage)
	}
	if !strings.HasPrefix(stateDir, "/") {
		return "", "", "", errors.New("--state-dir: an absolute path")
	}
	return rest[0], rest[1], stateDir, nil
}

// backupTargets is the daemon's manager of the targets: the markers under /etc/config and the
// mounts through the helper, systemctl's show and the journal as the daemon itself.
func backupTargets(root system.Root, stateDir string, crypt *backupcrypt.Store) *backuptarget.Manager {
	st := &backuptarget.Store{Dir: stateDir, Root: string(root),
		WriteMarker:  func(path string, data []byte) error { return system.Priv.WriteFile(path, data, 0o644) },
		RemoveMarker: func(path string) error { return system.Priv.Remove(path) },
		USBMount:     usbMount(root),
		ShareInfo:    shareInfo(stateDir),
	}
	return &backuptarget.Manager{
		Store:  st,
		Crypt:  crypt,
		Root:   string(root),
		Helper: func() backuptarget.Helper { return system.Priv },
		Hostname: func() string {
			h, _ := os.Hostname()
			return h
		},
		Container: root.Container,
		Systemctl: func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "systemctl", args...).Output()
		},
		Journal: unitJournal,
	}
}

// usbMount answers where the USB stick with a label is mounted now (a directory target chosen as
// usb:<label>/<folder>, task 228).
func usbMount(root system.Root) func(string) (string, bool) {
	return func(label string) (string, bool) {
		s, ok := root.USBStickByLabel(label)
		return s.Mount, ok
	}
}

// shareInfo answers whether a share exists and is read-only (a backup target of kind share).
func shareInfo(stateDir string) func(string) (bool, bool) {
	st := &shares.Store{Dir: stateDir}
	return func(id string) (bool, bool) {
		s, ok, _ := st.Get(id)
		return ok, s.ReadOnly
	}
}

// migrateShares is task 228's migration at start: task 86's NFS and SMB targets become shares of
// System → Storage and targets of kind share; a target whose share got another name loses its
// own units. Nothing happens once there are none.
func migrateShares(ctx context.Context, bt *backuptarget.Manager, sh *shares.Manager, log *slog.Logger) {
	done, err := bt.Store.MigrateMounts(sh.Store)
	for _, m := range done {
		log.Info("backup targets: an NFS/SMB target is a share of System → Storage now", "target", m.Target, "name", m.Name, "share", m.Share, "folder", m.Folder)
		if m.OldMount != "" {
			if err := sh.Helper().NetMountRemove(ctx, m.OldMount); err != nil {
				log.Warn("backup targets: the old mount units were not removed", "target", m.Target, "err", err)
			}
		}
	}
	if err != nil {
		log.Error("backup targets: the move to shares stopped", "err", err)
	}
}

// unitJournal answers a unit's last lines of the last 15 minutes (a mount unit's: why it failed).
func unitJournal(unit string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", "8", "-o", "cat", "--no-pager", "--since", "-15min").Output()
	return string(out)
}

// shareManager is the daemon's manager of the shares (task 228): the mounts through the helper, the
// journal as the daemon itself. A share's name may not be a task-86 mount target's id: both are
// mounted under /media/net.
func shareManager(root system.Root, stateDir string, bt *backuptarget.Manager) *shares.Manager {
	st := &shares.Store{Dir: stateDir, Taken: func(id string) bool {
		if bt == nil {
			return false
		}
		t, ok, _ := bt.Store.Get(id)
		return ok && (t.Kind == backuptarget.KindNFS || t.Kind == backuptarget.KindCIFS)
	}}
	return &shares.Manager{
		Store:     st,
		Root:      string(root),
		Helper:    func() shares.Helper { return system.Priv },
		Container: root.Container,
		Journal:   unitJournal,
	}
}

// backupMain runs one half and answers the exit code.
func backupMain(args []string) int {
	step, instance, stateDir, err := backupArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "occulited -backup:", err)
		return 2
	}
	// create is root's (createBackup.sh, the copies into the mounts); deliver is occulite's - as
	// root it would leave root's files in the state directory
	if step == "create" && !priv.IsRoot() {
		fmt.Fprintln(os.Stderr, "occulited -backup create: must run as root")
		return 1
	}
	if step == "deliver" && priv.IsRoot() {
		fmt.Fprintln(os.Stderr, "occulited -backup deliver: runs as occulite, not as root")
		return 1
	}
	var level slog.LevelVar
	if cfg, err := config.Load(stateDir + "/occulited.json"); err == nil {
		level.Set(logctl.SlogLevel(cfg.LogLevel))
	}
	h, err := logHandler("auto", "occulited-backup", &level)
	if err != nil {
		fmt.Fprintln(os.Stderr, "occulited -backup:", err)
		return 1
	}
	log := slog.New(h)
	uid, gid := -1, -1
	if u, err := user.Lookup("occulite"); err == nil {
		uid, _ = strconv.Atoi(u.Uid)
		gid, _ = strconv.Atoi(u.Gid)
	}
	host, _ := os.Hostname()
	p := &backuptarget.Pipeline{
		Store:    &backuptarget.Store{Dir: stateDir, Root: "/", USBMount: usbMount(system.Root("/"))},
		Crypt:    &backupcrypt.Store{Dir: stateDir},
		Staging:  backuptarget.StagingDir,
		Hostname: host,
		Version:  system.Root("/").ReadVersion().Version,
		UID:      uid, GID: gid,
		Log: log,
		Create: func(ctx context.Context, path string) error {
			// openccu-lite task 399: the addons' snapshot hooks first (as root here, priv.Local
			// runs each as its addon), the stagings emptied after the tar
			hooks := &system.BackupHooks{Root: system.Root("/"), Runner: priv.Local{}, StateFile: stateDir + "/backup-addons.json", UID: uid, GID: gid, Log: log}
			ran := hooks.Run(ctx)
			out, err := exec.CommandContext(ctx, "/bin/createBackup.sh", path).CombinedOutput()
			hooks.Clean(context.WithoutCancel(ctx), ran)
			if err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
	backuptarget.MountLog = func(unit string) string {
		out, _ := exec.Command("journalctl", "-u", unit, "-n", "8", "-o", "cat", "--no-pager").Output()
		return string(out)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if step == "create" {
		err = p.CreateRun(ctx, instance)
	} else {
		err = p.DeliverRun(ctx, instance)
	}
	if err != nil {
		log.Error("backup: "+step+" failed", "instance", instance, "err", err)
		return 1
	}
	return 0
}
