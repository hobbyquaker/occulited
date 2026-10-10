package system

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/hobbyquaker/occulited/internal/addonimage"
	"github.com/hobbyquaker/occulited/internal/journald"
	"github.com/hobbyquaker/occulited/internal/manifest"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// SystemdAddons installs and removes addons on a systemd box (task 20, D-36). The scripts are
// the firmware's own - install_addon runs the archive's update_script, the rc.d script gets
// `uninstall` - but they run in a transient scope instead of occulited's own cgroup, so a daemon
// an update_script starts does not end up as occulited's child; afterwards the generator is
// reloaded and the addon's unit (addon-<name>.service) takes the daemon over: restart runs the
// rc.d stop/start inside the unit's cgroup, and the scope's leftovers are stopped.
type SystemdAddons struct {
	Scripts AddonScripts
	Systemd SystemdServices
	Log     io.Writer // nil = quiet
	// DefaultMode is how a newly installed addon runs: "confined" (D-36's default since
	// 2026-09-07 - its own user addon-<id> with what its manifest declares) or "root", the
	// whole-box opt-out. Empty means AddonDefaultMode. Config: addons.default_mode.
	DefaultMode string
	// Tokens are the addon control tokens (28.8) and the addons' API tokens (task 66); nil until
	// RefreshAddonTokens ran.
	Tokens *AddonTokens
	// APITokens mints the addons' API tokens (task 66): the auth store; nil = none are minted.
	APITokens APITokenMinter
	// Firewall regenerates firewall.conf when a policy change or an uninstall moves an addon's
	// opened ports (D-47); nil = the file is left alone.
	Firewall *FirewallManager
	// FallbackManifest is the catalogue's word for an addon whose package carries no manifest of
	// its own (D-119): its adapter manifest, or the manifest fetched for the Addons page, with the
	// release tag it was read at ("" for an adapter or the default branch: any version; B-27); nil
	// for none, and nil without a catalogue.
	FallbackManifest func(id string) (*manifest.Manifest, string)
	// EarlyStart is the user's early-start switch for an addon (task 119): the global one and the
	// addon's own, both on by default. nil = on for every addon.
	EarlyStart func(id string) bool
	// Journal also gets the output of every install and uninstall (addonjournal.go); nil = the
	// output is only in the result the caller shows.
	Journal *journald.Writer
	// AfterStart is told the confined addons an install or a policy switch has just started in their
	// units (B-106), so B-92's ownership check can look at their files once more; nil = nothing.
	AfterStart func(ids []string)

	// Daemons is what was learned about the addons that keep a process (addondaemon.go, B-158);
	// nil = nothing is learned, only runtime.daemon counts.
	Daemons *AddonDaemons

	// Journalctl runs journalctl for the refused-remount scan (addonremount.go) and an ended
	// addon's last lines (addondaemon.go); nil = the box's journalctl. Tests hand in the JSON lines.
	Journalctl func(ctx context.Context, args ...string) ([]byte, error)

	// jobs counts the installs and uninstalls running now (occulited B-30): the addon supervisor
	// leaves every unit alone while one runs (Busy).
	jobs atomic.Int32

	scopeFn func() string // nil = a random occulite-addon-<hex>.scope
	monoNow func() uint64 // nil = CLOCK_MONOTONIC in microseconds (task 107)
	remount remountScan   // the last refused-remount scan (D-66)
}

// AddonDefaultMode is what an addon gets when nothing else says otherwise (D-36, decided by the
// maintainer 2026-09-07): confined. Root is an explicit choice from then on - the catalogue entry
// saying runtime.root, the user pressing the unsafe button, or a box-wide addons.default_mode.
const AddonDefaultMode = "confined"

// NewSystemdAddons wires the rc.d layer (AddonScripts) to run its scripts through systemd-run.
// Busy says whether an install or an uninstall runs now (occulited B-30). The addon supervisor
// (CrashLoops.Paused) restarts nothing meanwhile: the job stops, starts and settles the units
// itself, and an update script's stop leaves a daemon's unit empty for a moment on purpose.
// Which addon an archive holds is known only after its installer, and an install touches other
// addons' units too, so the pause is for all of them; it lasts seconds to a minute.
func (a *SystemdAddons) Busy() bool { return a.jobs.Load() > 0 }

func NewSystemdAddons(root Root, sd SystemdServices) *SystemdAddons {
	a := &SystemdAddons{Systemd: sd}
	a.Scripts = AddonScripts{Root: root, Exec: a.scoped, Credential: a.Credential}
	return a
}

// Credential is who a confined addon's own code runs as outside its unit (B-119): its rc.d script's
// info, init and uninstall, and its CGIs. nil = root: a root policy, or none. The group list is
// explicit - the addon's own group - so the helper's setgroups runs and nothing of the caller's
// groups is inherited (systemd-run --scope --uid keeps them; RunAs without a list keeps them too).
func (a *SystemdAddons) Credential(id string) *priv.Credential {
	p := a.Scripts.Root.ReadAddonPolicy(id)
	if p == nil || p.Mode != "confined" || p.UID <= 0 {
		return nil
	}
	return &priv.Credential{UID: p.UID, GID: p.UID, Groups: []int{p.UID}}
}

func (a *SystemdAddons) scopeName() string {
	if a.scopeFn != nil {
		return a.scopeFn() // tests: a fake /proc names the scope before the install runs
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "occulite-addon-" + hex.EncodeToString(b) + ".scope"
}

// scoped runs name args... inside a transient scope named by the caller through the context.
func (a *SystemdAddons) scoped(ctx context.Context, name string, args ...string) ([]byte, error) {
	scope, _ := ctx.Value(scopeKey{}).(string)
	if scope == "" || a.Systemd.Run != nil {
		// tests (recorder Runner) and a missing scope: run plainly
		return run(ctx, name, args...)
	}
	full := append([]string{"--scope", "--quiet", "--unit=" + scope, "--", name}, args...)
	return run(ctx, "systemd-run", full...)
}

type scopeKey struct{}

func (a *SystemdAddons) rcd() map[string]bool {
	out := map[string]bool{}
	for _, id := range rcdAddonIDs(a.Scripts.Root) {
		out[id] = true
	}
	return out
}

// rcdEntry is what an install may change about an rc.d entry (task 48): the entry itself (an
// update script's `ln -sfn` puts the addon's own script over the wrapper - a symlink where the
// wrapper was a regular file, or a new mtime), and whether it still is the wrapper, read off
// its text when the script is readable (rc.d scripts may be root-only, then unknown).
type rcdEntry struct {
	sig     string
	wrapper int // 1 the wrapper, -1 a script of its own, 0 unreadable
}

// addonRCMark is the line lite-addon-rc looks for in a wrapper (the fork's MARK).
const addonRCMark = "openccu-lite addon-rc wrapper"

func (a *SystemdAddons) rcdSnapshot() map[string]rcdEntry {
	out := map[string]rcdEntry{}
	for _, id := range rcdAddonIDs(a.Scripts.Root) {
		path := a.Scripts.Root.join("/usr/local/etc/config/rc.d/" + id)
		e := rcdEntry{}
		if st, err := os.Lstat(path); err == nil {
			e.sig = fmt.Sprintf("%v %d %d", st.Mode()&os.ModeSymlink != 0, st.Size(), st.ModTime().UnixNano())
		}
		if f, err := os.Open(path); err == nil {
			head := make([]byte, 4096)
			n, _ := io.ReadFull(f, head)
			f.Close()
			if strings.Contains(string(head[:n]), addonRCMark) {
				e.wrapper = 1
			} else {
				e.wrapper = -1
			}
		}
		out[id] = e
	}
	return out
}

// Install reads the package's manifest, runs the installer, applies the manifest to the addon it
// names (D-119), then settles every addon it touched in its unit (task 48): adopts the rc.d
// scripts again, gives a confined one its directories back (B-92), reloads the generator, and
// restarts the units of the addons that run - and starts the ones that ran before the installer
// and are stopped now (B-98) - unless the installer asked for a reboot. A new addon is started in
// its unit after any installer that succeeded, one asking for a reboot too (B-186).
func (a *SystemdAddons) Install(ctx context.Context, archive io.Reader) (*InstallResult, error) {
	a.jobs.Add(1) // B-30: the supervisor waits until the units are settled
	defer a.jobs.Add(-1)
	// the archive is staged first, so that openccu-lite.json can be read out of it in Go before
	// any code of the package runs (never a helper tar -xOf, B-39); an archive the API staged
	// already is read where it is
	staged, ok := archive.(*StagedArchive)
	if !ok {
		var err error
		if staged, err = StageAddonArchive(a.Scripts.Root, "new_addon.tar.gz", archive); err != nil {
			a.journalAddonRun(addonRun{action: "install", id: addonIDFrom(ctx), err: err})
			return nil, err
		}
	}
	m, merr := manifest.FromArchiveFile(staged.Path)
	if merr != nil && !errors.Is(merr, manifest.ErrNoManifest) {
		slog.Warn("addon manifest: the package's manifest is not usable", "addon", addonIDFrom(ctx), "err", merr)
	}
	// occulited task 11: the images the manifest declares, out of the same archive and before any
	// of the package's code runs
	var imgs PackageImages
	if m != nil {
		imgs.Images, imgs.Missing, imgs.Err = addonimage.FromArchiveFile(staged.Path, m.UI)
	}
	archive = staged
	before := a.rcdSnapshot()
	// B-98: what ran before the installer did. An update script stops its addon, and when its own
	// start fails afterwards the addon would stay stopped although it was running.
	ranBefore := a.runningAddons(ctx, before)
	// task 107: a unit that left the inactive state after this was started during the install
	installBegan := a.monotonic()
	// task 110: an update script may start its addon's unit before the installer ends - RedMatic's
	// `systemctl start` right after its `cp -af` as root - and that start walks the whole tree only
	// when the marker says so. Which addon the archive holds is known only afterwards: every confined
	// addon is marked now, and the marks of the addons the install did not touch are taken back below.
	premarked := a.markFullWalk("install", a.confinedIDs()...)
	scope := a.scopeName()
	res, err := a.Scripts.Install(context.WithValue(ctx, scopeKey{}, scope), archive)
	if err != nil {
		// the installer did not run
		for _, id := range premarked {
			a.clearFullWalk(id)
		}
		a.journalAddonRun(addonRun{action: "install", id: addonIDFrom(ctx), err: err})
		return nil, err
	}
	after := a.rcdSnapshot()
	// Fresh ids were not in rc.d before. Touched ids were, and the installer changed their
	// entry, or it was the wrapper and is not any more, or a process of theirs runs in the
	// install scope - an update script that stopped the daemon and started it again where it
	// ran (hmm, RedMatic, Mosquitto and nearly every OpenCCU addon do exactly that, and the box
	// has to handle it: it cannot be fixed in the addons). An entry that never was the wrapper
	// is not touched by being what it always was: on an image without lite-addon-rc every
	// addon would otherwise be restarted by every install of any other.
	var fresh, touched []string
	for id, now := range after {
		was, existed := before[id]
		switch {
		case !existed:
			fresh = append(fresh, id)
		case was.sig != now.sig, was.wrapper == 1 && now.wrapper == -1, len(a.Systemd.addonLeftoversDeep(id, scope)) > 0:
			touched = append(touched, id)
		}
	}
	sort.Strings(fresh)
	sort.Strings(touched)
	// 28.8: the wrapper in front of the new scripts before the units are generated - and in
	// front of the ones an update replaced (task 48)
	if len(fresh)+len(touched) > 0 {
		a.adoptRC(ctx, append(append([]string{}, fresh...), touched...)...)
	}
	// B-158: what was learned about an addon's daemon is about the version before; the new one is
	// learned again once its unit holds a process (its manifest's runtime.daemon covers the gap)
	for _, id := range append(append([]string{}, fresh...), touched...) {
		a.Daemons.Forget(id)
	}
	// D-119: the package's declaration becomes the policy of the addon it names, before the unit
	// is generated; an addon without one takes the catalogue's word when there is one
	if merr != nil && !errors.Is(merr, manifest.ErrNoManifest) {
		res.Output += fmt.Sprintf("\n[manifest] the package's %s is not usable and was ignored: %v", manifest.FileName, merr)
	}
	a.applyInstalledManifest(ctx, m, imgs, fresh, touched, res)
	start := !res.RebootRequired && res.Exit == 0
	// B-186 (maintainer, 2026-09-23): a new addon is started after an installer that succeeded,
	// also one that asks for a reboot - a CCU starts it at that reboot, and nearly every CCU addon's
	// first install ends with exit 10 for no other reason. An addon that was there before keeps
	// D-67's rule (the `start` above).
	startFresh := res.Exit == 0 || res.RebootRequired
	// B-106: a new addon's install script may have started its daemon as root in the scope - its
	// own `rc.d/<id> start` ran the script itself, there was no wrapper yet. It is stopped whatever
	// the installer's exit (B-119): before the unit's start below, so that the policy gives the
	// addon's files to its user after the daemon wrote what it writes on its way out; and when
	// nothing is started - the installer failed - because a root daemon outside any unit would
	// otherwise run until the reboot and hold the port its unit needs.
	for _, id := range fresh {
		if left := a.Systemd.addonLeftoversDeep(id, scope); len(left) > 0 {
			stopped := a.Systemd.stopByPID(ctx, left)
			if len(stopped) > 0 {
				res.Output += fmt.Sprintf("\n[systemd] %s: %d process(es) its installer started outside a unit were stopped", id, len(stopped))
			}
		}
	}
	// every new addon without a declaration gets a policy before its unit is generated (D-36):
	// the box's default, which the Services page may change right after
	for _, id := range fresh {
		if a.Scripts.Root.ReadAddonPolicy(id) == nil {
			if _, err := a.SetPolicy(ctx, id, a.defaultMode(), "default", nil); err != nil {
				res.Output += fmt.Sprintf("\n[systemd] policy for %s: %v", id, err)
				// Confining needs a user, and creating one needs a writable /etc (B-28). Where
				// that fails the addon still has to run: it gets an explicit root policy marked
				// "fallback", so the Services page shows what happened instead of the addon
				// having no policy at all and nobody knowing why it is root.
				if a.defaultMode() != "root" {
					if _, ferr := a.SetPolicy(ctx, id, "root", "fallback", nil); ferr != nil {
						res.Output += fmt.Sprintf("\n[systemd] root fallback for %s: %v", id, ferr)
					} else {
						res.Output += fmt.Sprintf("\n[systemd] %s runs as root: it could not be confined", id)
					}
				}
			}
		}
	}
	// task 110: every confined addon the install created or touched walks its whole tree at its next
	// start - marked again, since a start during the install may have taken the first mark, and a new
	// addon had none - and the marks made above for the addons it did not touch are taken back
	installed := append(append([]string{}, fresh...), touched...)
	for _, id := range installed {
		if a.confinedPolicy(id) != nil {
			a.markFullWalk("install", id)
		}
	}
	for _, id := range premarked {
		if !slices.Contains(installed, id) {
			a.clearFullWalk(id)
		}
	}
	// An updated addon is put back into its unit when it runs - the unit is active, or a process of
	// the addon is alive somewhere. One that is not running after the install is started only when
	// it ran before (B-98): its update script stopped it, and the script's own start failed -
	// RedMatic 9.7.1's died as root on a file of its confined user. One that was stopped before
	// stays stopped, as the user left it.
	// Task 107: one that its update script started in its unit, with nothing of it left elsewhere and
	// its files its user's, is left running (startedByTheInstall) - B-106's stop and start would only
	// start it a second time.
	running, restart, leave := map[string]bool{}, map[string]bool{}, map[string]bool{}
	if start && len(touched) > 0 {
		units := make([]string, len(touched))
		for i, id := range touched {
			units[i] = "addon-" + id + ".service"
		}
		states := a.Systemd.showAll(ctx, units, "ActiveState", "InactiveExitTimestampMonotonic")
		for _, id := range touched {
			running[id] = a.addonRunning(states, id)
			restart[id] = running[id] || ranBefore[id]
			leave[id] = running[id] && a.startedByTheInstall(states["addon-"+id+".service"], installBegan, id)
		}
	}
	// B-92: an updated confined addon gets its directories back before anything starts it - what
	// its update script wrote as root (var/, a pid file directory) is otherwise root's, and the
	// start in its unit as addon-<id> fails while its rc.d script may still say OK. The stored
	// policy is left as it is (the catalogue install rewrites it afterwards, the start-time refresh
	// renders a data directory that is new); a failure is said and the start still happens, the
	// addon may run anyway. One that is put back into its unit gets them there, once its old
	// processes are gone (B-106, settleUpdated); one that is not started now - not running, or
	// waiting for a reboot - here, for its next start.
	for _, id := range touched {
		if !restart[id] {
			a.ownUpdatedAddon(ctx, id, res)
		}
	}
	_, _ = a.Systemd.run(ctx, "daemon-reload")
	a.RefreshAddonTokens(ctx)
	var started, confined []string
	if startFresh {
		// a new addon is started in its unit; what its install script may have started in the
		// scope is stopped on the way (the same sequence as for an update, below). The install
		// ends when the unit is active, or says why it is not (B-186).
		for _, id := range fresh {
			if _, err := a.Systemd.resettleAddon(ctx, id, scope); err != nil {
				res.Output += fmt.Sprintf("\n[systemd] addon-%s.service: %v", id, err)
				slog.Warn("addons: a new addon's unit could not be started", "id", id, "err", err)
				continue
			}
			switch state, result := a.Systemd.unitSettled(ctx, id); state {
			case "active", "":
				started = append(started, id)
				if a.confinedPolicy(id) != nil {
					confined = append(confined, id)
				}
			default:
				why := state
				if result != "" && result != "success" {
					why += " (" + result + ")"
				}
				res.Output += fmt.Sprintf("\n[systemd] %s was started in its unit and is %s now: its log says why", id, why)
				slog.Warn("addons: a new addon's unit is not active after its start", "id", id, "state", state, "result", result)
			}
		}
		if res.RebootRequired && len(started) > 0 {
			res.Output += "\n[systemd] the installer asks for a reboot; the new addon was started without waiting for it"
		}
	}
	if start {
		for _, id := range touched {
			if !restart[id] {
				res.Output += fmt.Sprintf("\n[systemd] %s is not running and was not started", id)
				continue
			}
			if leave[id] {
				res.Output += fmt.Sprintf("\n[systemd] %s was started in its unit by its update and its files are its user's: left running", id)
				slog.Info("addons: an addon its update started in its unit was left running", "id", id)
				// task 110: the whole dry run behind that found its tree right, with nothing of the
				// install left running
				a.clearFullWalk(id)
				confined = append(confined, id)
				continue
			}
			stopped, err := a.settleUpdated(ctx, id, scope, res)
			if err == nil && a.confinedPolicy(id) != nil {
				confined = append(confined, id)
			}
			switch {
			case !running[id] && err != nil:
				res.Output += fmt.Sprintf("\n[systemd] %s ran before the update and is stopped now; starting it in its unit failed: %v", id, err)
				slog.Warn("addons: an addon that ran before its update could not be started again", "id", id, "err", err)
			case !running[id]:
				res.Output += fmt.Sprintf("\n[systemd] %s ran before the update and its update script left it stopped: started in its unit", id)
				slog.Info("addons: an addon that ran before its update was started again", "id", id)
			case err != nil:
				res.Output += fmt.Sprintf("\n[systemd] addon-%s.service: %v", id, err)
				res.Output += strayWarning(a.Systemd.addonLeftoversDeep(id, ""), id)
			case len(stopped) > 0:
				res.Output += fmt.Sprintf("\n[systemd] %s restarted in its unit (%d process(es) left in the install scope stopped)", id, len(stopped))
			default:
				res.Output += fmt.Sprintf("\n[systemd] %s restarted in its unit", id)
			}
		}
	}
	// B-106: one more look at the files of what now runs as its own user, a moment after its start
	if len(confined) > 0 && a.AfterStart != nil {
		a.AfterStart(confined)
	}
	// The scope is left alone (B-3): an installer that restarted lighttpd or another system
	// daemon through its init script leaves that daemon in the scope, and stopping the scope
	// killed it - the box lost its web server. Only the addon's own processes are stopped by
	// pid above; anything else stays in the abandoned scope, visible in the unit list, and
	// ends when its process ends.
	if len(started) > 0 {
		res.Output += "\n[systemd] started " + strings.Join(started, ", ") + " in their units"
	}
	// B-98, decided in D-67: an installer that failed or asks for a reboot starts nothing, so an addon
	// its script stopped stays stopped; the result names it and the page asks whether to start it again
	if !start {
		why := "the installer failed"
		if res.RebootRequired {
			why = "the installer asks for a reboot"
		}
		res.StoppedAddons = a.stoppedAddons(ctx, ranBefore)
		for _, s := range res.StoppedAddons {
			res.Output += fmt.Sprintf("\n[systemd] %s was running before the install and is stopped now; it was not started: %s", s.ID, why)
		}
	}
	a.journalAddonRun(addonRun{action: "install", id: installedID(ctx, fresh, touched), output: res.Output, ran: true, exit: res.Exit, meaning: res.Meaning})
	return res, nil
}

// addonRunning says whether an addon runs, from its unit's ActiveState in states (active, starting
// or reloading) or, outside its unit, from a process of the addon that is alive.
func (a *SystemdAddons) addonRunning(states map[string]map[string]string, id string) bool {
	switch states["addon-"+id+".service"]["ActiveState"] {
	case "active", "activating", "reloading":
		return true
	}
	return len(addonProcesses(a.Scripts.Root, id)) > 0
}

// strayWarning says that the addon still runs outside its unit after its unit failed to start
// (B-59): not started by the system, not confined, and the Restart button is the way back.
func strayWarning(left []proc, id string) string {
	if len(left) == 0 {
		return ""
	}
	pids := make([]string, len(left))
	for i, p := range left {
		pids[i] = strconv.Itoa(p.PID)
	}
	slog.Warn("addons: a process of the addon runs outside its unit after the install", "id", id, "pids", pids)
	return fmt.Sprintf("\n[systemd] warning: %s still runs outside its unit (pid %s) - not under the system's control and not confined; Restart on the Services page or a reboot puts it back", id, strings.Join(pids, ", "))
}

// settleUpdated puts an updated addon back into its unit (B-106): its unit stopped until nothing of
// it is left, and its processes in the install scope stopped and gone, then a confined addon's files
// given to its user (B-92) - what those processes wrote on their way out included - then the unit
// started. A stop that fails still gives the files over, for the addon's next start.
func (a *SystemdAddons) settleUpdated(ctx context.Context, id, scope string, res *InstallResult) ([]int, error) {
	stopped, err := a.Systemd.quietAddon(ctx, id, func() []proc { return a.Systemd.addonLeftoversDeep(id, scope) })
	// task 110: what the stopped processes wrote on their way out may be root's anywhere in the tree;
	// the mark is made again in case a start of the unit since the install's took it
	if a.confinedPolicy(id) != nil {
		a.markFullWalk("install", id)
	}
	a.ownUpdatedAddon(ctx, id, res)
	if err != nil {
		return stopped, err
	}
	return stopped, a.Systemd.startAddonUnit(ctx, id)
}

// ownUpdatedAddon is B-92's step for an updated addon whose stored policy is confined; a failure is
// said in the install's output and stops nothing.
func (a *SystemdAddons) ownUpdatedAddon(ctx context.Context, id string, res *InstallResult) {
	p := a.confinedPolicy(id)
	if p == nil {
		return
	}
	if err := a.ownAddonDirs(ctx, p); err != nil {
		res.Output += fmt.Sprintf("\n[systemd] %s: its files could not be given to addon-%s, it may fail to start: %v", id, id, err)
	}
}

// confinedPolicy is the addon's stored policy when it runs as its own user, nil otherwise.
func (a *SystemdAddons) confinedPolicy(id string) *AddonPolicy {
	p := a.Scripts.Root.ReadAddonPolicy(id)
	if p == nil || p.Mode != "confined" || p.UID <= 0 {
		return nil
	}
	return p
}

// runningAddons is the set of the given rc.d ids that run right now (B-98), with one systemctl show
// for all of their units.
func (a *SystemdAddons) runningAddons(ctx context.Context, entries map[string]rcdEntry) map[string]bool {
	units := make([]string, 0, len(entries))
	for id := range entries {
		units = append(units, "addon-"+id+".service")
	}
	sort.Strings(units)
	states := a.Systemd.showAll(ctx, units, "ActiveState")
	out := map[string]bool{}
	for id := range entries {
		if a.addonRunning(states, id) {
			out[id] = true
		}
	}
	return out
}

// stoppedAddons lists the addons that ran before the installer and do not run now (B-98, D-67), with
// whether the next boot starts them anyway: the occu-addons generator gives every executable rc.d
// script a unit in addons.target, which occu-addons.service starts at boot; in safe mode it generates
// none, and a unit switched off on the Services page is masked again at start (unitswitch.go). A unit
// systemctl does not answer for is left out - unknown is not stopped - and so is an addon whose rc.d
// entry the installer removed.
func (a *SystemdAddons) stoppedAddons(ctx context.Context, ranBefore map[string]bool) []StoppedAddon {
	ids := make([]string, 0, len(ranBefore))
	for id, ran := range ranBefore {
		if ran {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	units := make([]string, len(ids))
	for i, id := range ids {
		units[i] = "addon-" + id + ".service"
	}
	states := a.Systemd.showAll(ctx, units, "ActiveState")
	root := a.Scripts.Root
	_, err := os.Stat(root.join("/usr/local/etc/config/safemode"))
	safe := err == nil
	masked := map[string]bool{}
	for _, u := range a.Systemd.readSwitch().Masked {
		masked[u] = true
	}
	var out []StoppedAddon
	for i, id := range ids {
		if states[units[i]] == nil || a.addonRunning(states, id) {
			continue
		}
		st, err := os.Stat(root.join("/usr/local/etc/config/rc.d/" + id))
		if err != nil {
			continue
		}
		boot := !safe && st.Mode().IsRegular() && st.Mode()&0o111 != 0 && !masked[units[i]]
		out = append(out, StoppedAddon{ID: id, StartsAtBoot: boot})
	}
	return out
}

// Uninstall stops the addon's unit (the whole cgroup), runs the rc.d uninstall, removes the
// entry and reloads the generator so the unit disappears, and removes the addon's policy files.
func (a *SystemdAddons) Uninstall(ctx context.Context, id string) (UninstallResult, error) {
	a.jobs.Add(1) // B-30
	defer a.jobs.Add(-1)
	if strings.ContainsAny(id, "/\\ ") || id == "" {
		return UninstallResult{}, fmt.Errorf("invalid addon id")
	}
	unit := "addon-" + id + ".service"
	if sout, serr := a.Systemd.run(ctx, "stop", "--no-pager", "--", unit); serr != nil {
		// not fatal: the addon goes anyway (a stop script that finds its daemon gone and exits 1,
		// RedMatic's, leaves the unit failed - the reset below clears that)
		slog.Warn("addons: the unit's stop failed before the uninstall; the uninstall goes on", "id", id, "err", serr, "output", strings.TrimSpace(string(sout)))
	}
	scope := a.scopeName()
	// occulited B-26: as whom the script runs, for the journal's line after it (the policy goes below)
	asUser := ""
	if a.Credential(id) != nil {
		asUser = "addon-" + id
	}
	out, err := a.Scripts.Uninstall(context.WithValue(ctx, scopeKey{}, scope), id)
	// the scope is not stopped (B-3, see Install)
	_, _ = a.Systemd.run(ctx, "daemon-reload")
	// B-236: a unit that ended failed (its stop exited non-zero) stays listed after its file is
	// gone - "not-found", "failed" on the Services page and in systemctl --failed - until systemd
	// is told to forget the failure. Harmless when the unit is not failed or already gone.
	_, _ = a.Systemd.run(ctx, "reset-failed", "--no-pager", "--", unit)
	// openccu-lite B-283 (maintainer, 2026-09-30): every addon-policy/<id>.* goes with the addon -
	// the policy, its drop-in, the start order, the early start and the stored manifest - so a
	// reinstall starts clean from its new manifest (D-119), with every port closed (D-29, D-47). Its
	// uid stays reserved in the uid registry (B-288: addon-uids.json is not one of these files), so
	// a reinstall runs as the same uid and no other addon gets it. The firewall follows: the opened
	// ports were the policy's.
	out.SystemRemoved = append(out.SystemRemoved, a.Scripts.Root.removeAddonPolicyFiles(id)...)
	// the script's output, then what the system removed after it (B-26: the refused rm lines of a
	// confined script read as handled), then how it ended
	a.journalUninstall(id, out, asUser, err)
	a.regenerateFirewall(ctx)
	// its tokens go with it (28.8, task 66), and what was learned about its daemon (B-158)
	a.Tokens.forget(a.Scripts.Root, id)
	a.Daemons.Forget(id)
	return out, err
}

// regenerateFirewall lets the firewall follow when the addons' opened ports changed (D-47, task
// 157): an uninstalled addon's owned rules go.
func (a *SystemdAddons) regenerateFirewall(ctx context.Context) {
	notifyFirewall(ctx)
}

// ListAddons is the rc.d layer's list with the running state taken from the addon's unit (B-29).
//
// The busybox lister read /var/run/<id>.pid, which is what the CCU's own addons do - but most of
// the ported ones do not write a pid file at all (RedMatic starts node-red through a loader,
// hm2mqtt likewise), and the file is root-only when they do, while occulited is not root since
// task 17. On the systemd product the addon's daemons live in the generated unit's cgroup, and the
// unit is the honest answer: the Addons page said "not running" for three of four addons that were
// running.
func (a *SystemdAddons) ListAddons(ctx context.Context) ([]Addon, error) {
	list, err := a.Scripts.ListAddons(ctx)
	if err != nil {
		return list, err
	}
	units, uerr := a.Systemd.List()
	if uerr != nil {
		return a.overlayAddonPolicy(list), nil // no unit states: the rc.d info is all there is
	}
	return a.overlayAddonPolicy(overlayUnitState(list, a.markEnded(ctx, units))), nil
}

// overlayAddonPolicy writes each addon's confinement state into the list the Addons page reads
// (D-36): the mode it actually runs in, where that came from, and whether it ever declared a
// runtime block. Without this an addon is either root or not and the page cannot say why.
func (a *SystemdAddons) overlayAddonPolicy(list []Addon) []Addon {
	refused := a.RemountRefused(context.Background())
	for i := range list {
		v := a.PolicyView(list[i].ID)
		list[i].PolicyMode, list[i].PolicySource, list[i].Undeclared = v.Mode, v.Source, v.Undeclared
		list[i].MayMount, list[i].RemountRefused = v.MayMount, refused[list[i].ID]
		list[i].APIScopes = a.Tokens.APIScopes(list[i].ID)                        // task 66: what its own API token holds
		list[i].StartEarlyDeclared, list[i].StartEarly = a.StartEarly(list[i].ID) // task 119
		if v.Source == "migrated" {                                               // task 28: from the CCU
			list[i].FromCCU = true
			list[i].RCTarget = a.Scripts.Root.RCTarget(list[i].ID)
			if list[i].Failed {
				if _, lines := a.lastLines(context.Background(), "addon-"+list[i].ID+".service"); len(lines) > 0 {
					list[i].FailedLog = lines[len(lines)-1]
				}
			}
		}
	}
	return list
}

// OverlayServicePolicy does the same for the Services page's list, whose entries come from
// systemctl and are named addon-<id>.
func (a *SystemdAddons) OverlayServicePolicy(list []Service) []Service {
	refused := a.RemountRefused(context.Background())
	list = a.markEnded(context.Background(), list) // B-158
	for i := range list {
		if list[i].Kind != "addon" || !strings.HasPrefix(list[i].ID, "addon-") {
			continue // a unit an addon shipped itself is not one of the generated ones
		}
		id := strings.TrimPrefix(list[i].ID, "addon-")
		v := a.PolicyView(id)
		list[i].PolicyMode, list[i].PolicySource, list[i].Undeclared = v.Mode, v.Source, v.Undeclared
		list[i].MayMount, list[i].RemountRefused = v.MayMount, refused[id]
	}
	return list
}

// overlayUnitState replaces each addon's running state with its addon-<id>.service unit's.
func overlayUnitState(list []Addon, units []Service) []Addon {
	byID := map[string]Service{}
	for _, u := range units {
		byID[u.ID] = u
	}
	for i := range list {
		if u, ok := byID["addon-"+list[i].ID]; ok {
			list[i].Running, list[i].PID = u.Running, u.PID
			list[i].OneShot, list[i].Result, list[i].Stray = u.OneShot, u.Result, u.Stray
			list[i].Ended, list[i].EndedAt, list[i].EndedLog = u.Ended, u.EndedAt, u.EndedLog
			list[i].Failed, list[i].Skipped = u.Failed, u.Skipped
		}
	}
	return list
}

// CheckUpdate and Reboot are the rc.d layer's (an update check asks the addon's CGI; reboot is reboot).
func (a *SystemdAddons) CheckUpdate(ctx context.Context, ad Addon, base string) UpdateInfo {
	return a.Scripts.CheckUpdate(ctx, ad, base)
}

func (a *SystemdAddons) Reboot(ctx context.Context) error { return a.Scripts.Reboot(ctx) }

// RemoveRCEntry is task 28's "remove the rc.d entry" of a migrated script: the unit stopped, the
// entry and its .script twin removed (with withTarget also the file a link led to outside the
// addons' tree), the addon's policy files with them as an uninstall removes them, the generator
// reloaded and the failed state forgotten (B-58) - no ghost unit stays.
func (a *SystemdAddons) RemoveRCEntry(ctx context.Context, id string, withTarget bool) (RemoveRCEntryResult, error) {
	a.jobs.Add(1)
	defer a.jobs.Add(-1)
	if !addonIDRe.MatchString(id) {
		return RemoveRCEntryResult{}, fmt.Errorf("invalid addon id")
	}
	unit := "addon-" + id + ".service"
	if out, err := a.Systemd.run(ctx, "stop", "--no-pager", "--", unit); err != nil {
		slog.Warn("addons: the unit's stop failed before removing the rc.d entry; going on", "id", id, "err", err, "output", strings.TrimSpace(string(out)))
	}
	res, err := a.Scripts.Root.RemoveRCEntry(id, withTarget)
	_, _ = a.Systemd.run(ctx, "daemon-reload")
	_, _ = a.Systemd.run(ctx, "reset-failed", "--no-pager", "--", unit)
	if err != nil {
		return res, err
	}
	res.Removed = append(res.Removed, a.Scripts.Root.removeAddonPolicyFiles(id)...)
	a.regenerateFirewall(ctx)
	a.Tokens.forget(a.Scripts.Root, id)
	a.Daemons.Forget(id)
	slog.Info("addons: the rc.d entry of a script from the CCU removed", "id", id, "removed", strings.Join(res.Removed, " "))
	return res, nil
}

// SettleDisabled finishes disabling addons whose rc.d scripts just lost their executable bit
// (occulited B-58): each unit is stopped - which also cancels a start job addons.target queued at
// boot before a first-boot scan disabled the addon - then the generator is reloaded so the units
// disappear, and the failed state systemd keeps for a unit whose file is gone is cleared. Without
// the stop, RedMatic's unit, generated while the script was still executable, started after the
// ReGa scan and failed 126 ("Permission denied"); without the reset, a disabled addon's unit stayed
// "not-found failed" and the system "degraded". The stop's error is returned per id.
func (a *SystemdAddons) SettleDisabled(ctx context.Context, ids ...string) map[string]string {
	errs := map[string]string{}
	if len(ids) == 0 {
		return errs
	}
	for _, id := range ids {
		if out, err := a.Systemd.run(ctx, "stop", "--no-pager", "--", "addon-"+id+".service"); err != nil {
			errs[id] = strings.TrimSpace(err.Error() + ": " + strings.TrimSpace(string(out)))
		}
	}
	_, _ = a.Systemd.run(ctx, "daemon-reload")
	for _, id := range ids {
		_, _ = a.Systemd.run(ctx, "reset-failed", "--no-pager", "--", "addon-"+id+".service")
	}
	return errs
}

// ClearDisabledGhosts clears the failed state of every disabled addon's unit (B-58), at start: a
// box that disabled an addon with an earlier binary keeps its "not-found failed" unit - and the
// system "degraded" - until something says reset-failed. A disabled addon has no unit, so any
// failure on record for it is stale. Returns the ids it asked about.
func (a *SystemdAddons) ClearDisabledGhosts(ctx context.Context) []string {
	var ids []string
	for _, id := range rcdAddonIDs(a.Scripts.Root) {
		if addonIDRe.MatchString(id) && !a.Scripts.Root.AddonEnabled(id) {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if out, err := a.Systemd.run(ctx, "show", "-p", "LoadState,ActiveState", "--", "addon-"+id+".service"); err == nil &&
			strings.Contains(string(out), "LoadState=not-found") && strings.Contains(string(out), "ActiveState=failed") {
			_, _ = a.Systemd.run(ctx, "reset-failed", "--no-pager", "--", "addon-"+id+".service")
		}
	}
	return ids
}

// Reload reruns the generators (an rc.d script appeared, vanished or changed its mode).
func (a *SystemdAddons) Reload(ctx context.Context) { _, _ = a.Systemd.run(ctx, "daemon-reload") }

// defaultMode is the box's default. Anything but an explicit "root" is AddonDefaultMode: an
// unreadable or misspelt addons.default_mode confines rather than opening the box, which is the
// direction a default should fail in.
func (a *SystemdAddons) defaultMode() string {
	if a.DefaultMode == "root" {
		return "root"
	}
	return AddonDefaultMode
}

// Policy returns the stored policy of an addon (nil when it runs with the default and none
// was written yet - only addons installed before the systemd product).
func (a *SystemdAddons) Policy(id string) *AddonPolicy { return a.Scripts.Root.ReadAddonPolicy(id) }

// AddonPolicyView is what a page needs to say about one addon's confinement (D-36).
type AddonPolicyView struct {
	// Mode is what the addon really runs as: the stored policy's mode, or "root" when it has
	// no policy - the generator installs a drop-in only for a stored confined policy, so an
	// addon without one is root whatever the box's default says.
	Mode string
	// Source is where the mode came from: "default" (the box's), "manifest" (the package's own
	// openccu-lite.json, D-119), "catalog" (the catalogue's manifest standing in for a package
	// without one), "user" (the unsafe opt-out or the switch back), "migrated" (an addon that was
	// already installed when the default flipped) or "fallback" (confining failed, B-28), and
	// empty for an addon with no policy at all.
	Source string
	// Undeclared: no runtime block was ever applied to this addon, so nobody has said what it
	// needs - it is confined on the box's default and may want more than that. The maintainer's
	// requirement is that the UI says so rather than leaving the user to read the log.
	Undeclared bool
	// MayMount: a root addon whose declaration (the stored block or its manifest's)
	// names CAP_SYS_ADMIN, so its unit keeps the right to mount that every other root addon lost
	// (D-66). The pages mark it.
	MayMount bool
}

// EffectiveDefaultMode is the mode a newly installed addon gets on this box.
func (a *SystemdAddons) EffectiveDefaultMode() string { return a.defaultMode() }

// PolicyView is the addon's confinement as the pages show it.
func (a *SystemdAddons) PolicyView(id string) AddonPolicyView {
	p := a.Scripts.Root.ReadAddonPolicy(id)
	if p == nil {
		return AddonPolicyView{Mode: "root", Undeclared: true}
	}
	v := AddonPolicyView{Mode: p.Mode, Source: p.Source, Undeclared: !p.Runtime.declaresConfinement()}
	// the drop-in is rendered from the stored block; the view says what the declaration is today
	v.MayMount = p.Mode == "root" && (p.Runtime.DeclaresCapability(CapSysAdmin) || a.declared(id).DeclaresCapability(CapSysAdmin))
	return v
}

// AdoptInstalledAddons gives every installed addon that has no policy the mode it is running in
// today - root - and marks it "migrated". It is the answer to what D-36's flipped default means
// for a box that is already working (B-43's shape: a once-per-box step and a release note).
//
// An addon that came across on /usr/local from OpenCCU, or was installed before the systemd
// product, has no policy file, and the generator therefore installs no drop-in for it: it runs as
// root. Confining it because a firmware update changed a default would take away write access
// from a working addon at a reboot the user did not connect to it - a broken setup with no
// message. So the flip applies to what is installed *from now on*, and what is already there is
// pinned to what it has: visible on the Services page as root, undeclared, with the switch to
// confine it one press away.
//
// The caller runs this once per box behind a marker. It returns the ids it pinned; an error means
// nothing should be marked done, so the next boot tries again (B-33).
func (a *SystemdAddons) AdoptInstalledAddons(ctx context.Context) ([]string, error) {
	var ids []string
	for id := range a.rcd() {
		if a.Scripts.Root.ReadAddonPolicy(id) == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := a.SetPolicy(ctx, id, "root", "migrated", nil); err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
	}
	return ids, nil
}
