package system

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/hobbyquaker/occulited/internal/ownwalk"
	"github.com/hobbyquaker/occulited/internal/priv"
	"golang.org/x/sys/unix"
)

// Task 107 (RedMatic bug 10): a confined addon's files are given back to its user before every start
// of its unit, not only after an install through occulited. RedMatic's update script copies the new
// package as root with `cp -af` and then starts the unit itself; var/ is root's at that moment, the
// start as addon-redmatic fails on its start lock, and B-92's repair comes only after the installer.
// The fork's occu-addons generator gives every confined addon unit
//
//	ExecStartPre=+-/usr/libexec/occu/lite-addon-own <id>
//
// and that wrapper runs `occulited -addon-own <id>` as root (the "+"), which walks exactly the
// directories below (AddonOwnPlan) with ownwalk. Whoever starts the unit - an update script, the
// wrapper, occulited, the Services page, the boot - the files are the addon's first.
//
// Task 110 (the maintainer, 2026-09-13: "Full walk after install only"): root writes into a confined
// addon's tree when the addon is installed, updated or switched to its own user, and the whole walk
// cost RedMatic about 11 s at every Charly boot. So the whole tree is walked only after such a change,
// which leaves the marker below: at the next start of the unit, and in B-92's look while the marker is
// there. Every other start and look takes the quick check (ownwalk.QuickDepth: the top directories
// and their direct entries), and a wrong owner it finds brings the whole walk after all. Fix
// ownership always walks the whole tree.

// FullWalkSuffix names the marker "full walk needed", <id>.fullwalk beside the addon's policy. The
// directory is root's and the daemon writes into it through the helper: the addon's user can neither
// create nor remove the marker, and no walk and no data directory ever gives that directory to an
// addon. What it holds (the reason) is for a reader on the box; that it is there is what counts.
const FullWalkSuffix = ".fullwalk"

// FullWalkMarker is the path of the addon's marker on the box.
func (r Root) FullWalkMarker(id string) string {
	return r.join(AddonPolicyDir + "/" + id + FullWalkSuffix)
}

// FullWalkNeeded says whether the addon's next walk is the whole tree. Whatever has the marker's name
// counts, a link is not followed, and a look that fails counts too: taking something for the marker
// costs a walk, never a repair.
func (r Root) FullWalkNeeded(id string) bool {
	_, err := os.Lstat(r.FullWalkMarker(id))
	return !errors.Is(err, fs.ErrNotExist)
}

// markFullWalk writes the marker of each addon (why is its content) and answers the ids whose marker
// was not there before. One that cannot be written is logged: that addon's quick check still walks
// the whole tree when it meets a wrong owner at the top.
func (a *SystemdAddons) markFullWalk(why string, ids ...string) []string {
	root := a.Scripts.Root
	var created []string
	for _, id := range ids {
		if !addonIDRe.MatchString(id) || root.FullWalkNeeded(id) {
			continue
		}
		if err := Priv.WriteFile(root.FullWalkMarker(id), []byte(why+"\n"), 0o644); err != nil {
			slog.Warn("addons: the next ownership walk could not be marked as the whole tree", "id", id, "err", err)
			continue
		}
		created = append(created, id)
	}
	return created
}

// clearFullWalk removes the addon's marker: a whole walk found its files right, and nothing of root's
// writes into them any more.
func (a *SystemdAddons) clearFullWalk(id string) {
	if !addonIDRe.MatchString(id) {
		return
	}
	if err := Priv.Remove(a.Scripts.Root.FullWalkMarker(id)); err != nil {
		slog.Warn("addons: the marker of the whole ownership walk could not be removed", "id", id, "err", err)
	}
}

// confinedIDs are the addons whose stored policy is confined, sorted.
func (a *SystemdAddons) confinedIDs() []string {
	var ids []string
	for id, p := range a.Scripts.Root.AddonPolicies() {
		if p.Mode == "confined" && p.UID > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// standardAddonDirs are the three directories every addon is installed into.
func standardAddonDirs(id string) []string {
	return []string{"/usr/local/addons/" + id, "/usr/local/etc/config/addons/" + id, AddonWWW + "/" + id}
}

// OwnPlan is what `occulited -addon-own <id>` walks.
type OwnPlan struct {
	UID  int
	User string
	// Dirs are on the box: the three standard directories and the data directories of the stored
	// policy that still pass the guard rails.
	Dirs []string
	// Public are the resolved directories whose files stay world-readable when the tree is closed to
	// other users (B-252): the addon's www, served to the browser. Resolved because www is a symlink
	// on the image; the walk reaches its files through the addon's own directory.
	Public []string
	// Refused are data directories of the policy the guard rails refuse now, with the reason.
	Refused []string
}

// AddonOwnPlan reads the addon's stored policy: nil for an addon that runs as root or has no policy
// (nothing to do), an error for an id or a policy that is not usable. The data directories are the
// ones the take-over recorded (D-52), each checked again against the guard rails - never another
// addon's directory, a prefix of one or a path inside one, never a link.
func AddonOwnPlan(root Root, id string) (*OwnPlan, error) {
	if !addonIDRe.MatchString(id) {
		return nil, errors.New("not an addon id")
	}
	p := root.ReadAddonPolicy(id)
	if p == nil || p.Mode != "confined" {
		return nil, nil
	}
	if p.UID < AddonUIDBase || p.User != "addon-"+id {
		return nil, fmt.Errorf("the policy names the user %q (%d), not the addon's", p.User, p.UID)
	}
	plan := &OwnPlan{UID: p.UID, User: p.User}
	for _, d := range standardAddonDirs(id) {
		plan.Dirs = append(plan.Dirs, root.join(d))
	}
	plan.Public = addonPublicWWW(root, id)
	a := NewSystemdAddons(root, SystemdServices{Root: root})
	others := a.otherAddonDirs(id)
	for _, d := range p.DataDirs {
		clean, why := a.dataDirAllowed(id, d, others)
		switch why {
		case "":
			plan.Dirs = append(plan.Dirs, root.join(clean))
		case "does not exist":
		default:
			plan.Refused = append(plan.Refused, d+": "+why)
		}
	}
	return plan, nil
}

// addonPublicWWW are the addon's web trees, resolved, which stay world-readable: they are served to
// the browser (occulited, and lighttpd's own error page for an addon it proxies). www is a symlink on
// the image, so the resolved directory is what the walk meets while it descends the addon's own
// directory. The first is AddonWWW/<id>, wherever it leads (an addon in /opt/<id> too). B-115: an
// addon may serve a page under a www name other than its id - ccu-addon-battery's settings page is
// AddonWWW/battery-config -> /usr/local/addons/battery/www, RedMatic's pattern - so every other entry
// of AddonWWW that is a link to a directory strictly inside the addon's own /usr/local/addons/<id>
// counts as well. Only a link resolving into that directory: an entry can publish nothing of another
// addon's, and never the addon's whole tree (only its id's www could, as before). AddonWWW is root's,
// written by installers; a link the addon plants inside its own tree is followed to where it ends,
// and that must still lie in the tree.
func addonPublicWWW(root Root, id string) []string {
	var out []string
	add := func(p string) {
		for _, q := range out {
			if q == p {
				return
			}
		}
		out = append(out, p)
	}
	if real, err := filepath.EvalSymlinks(root.join(AddonWWW + "/" + id)); err == nil {
		add(real)
	}
	own, err := filepath.EvalSymlinks(root.join("/usr/local/addons/" + id))
	if err != nil {
		return out
	}
	dir := root.join(AddonWWW)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.Name() == id || e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
		if err != nil || real == own || !underDir(real, own) {
			continue
		}
		if st, err := os.Stat(real); err != nil || !st.IsDir() {
			continue
		}
		add(real)
	}
	return out
}

// ownershipWalk is a dry run of the ownership walk over the addon's directories - the three standard
// ones and its data directories - through the helper, which reads what the daemon may not and follows
// no link. quick is task 110's quick check: the top directories and their direct entries.
func (a *SystemdAddons) ownershipWalk(p *AddonPolicy, quick bool) (ownwalk.Result, error) {
	root := a.Scripts.Root
	var dirs []string
	for _, d := range ownershipDirs(p) {
		dirs = append(dirs, root.join(d))
	}
	return Priv.OwnTree(p.ID, dirs, p.UID, priv.OwnTreeOptions{DryRun: true, Quick: quick})
}

// filesAreItsOwn is the install flow's look after an update: the dry run of the whole walk. why says
// what is not right.
func (a *SystemdAddons) filesAreItsOwn(p *AddonPolicy) (ok bool, why string) {
	res, err := a.ownershipWalk(p, false)
	switch {
	case err != nil:
		return false, err.Error()
	case res.Problem() != "":
		return false, res.Problem()
	case res.Wrong > 0:
		return false, fmt.Sprintf("%d entries with another owner, first %s", res.Wrong, res.FirstWrong)
	}
	return true, ""
}

// startedByTheInstall (task 107) says whether an updated addon that runs may stay as it is instead of
// B-106's stop and start: it is confined, its unit is active and left the inactive state after the
// installer began (its update script started it - RedMatic's `systemctl start` after its copy), no
// process of the addon runs outside the unit (not in the install scope, not anywhere), and none of
// its files has another owner now. A unit start runs the ownership step before the addon, so what the
// update copied was the addon's at its start; what the script wrote afterwards is what the look is
// for. Anything else - a leftover in the scope, a root-owned file, an answer systemd did not give -
// goes the way it went before.
func (a *SystemdAddons) startedByTheInstall(props map[string]string, since uint64, id string) bool {
	p := a.confinedPolicy(id)
	if p == nil || props["ActiveState"] != "active" {
		return false
	}
	started, err := strconv.ParseUint(props["InactiveExitTimestampMonotonic"], 10, 64)
	if err != nil || started == 0 || started <= since {
		return false
	}
	if len(a.Systemd.addonLeftovers(id, "")) > 0 {
		return false
	}
	ok, why := a.filesAreItsOwn(p)
	if !ok {
		slog.Info("addons: an addon its update started in its unit is restarted there", "id", id, "why", why)
	}
	return ok
}

// monotonic is CLOCK_MONOTONIC in microseconds, the clock of systemd's *TimestampMonotonic
// properties; tests set monoNow.
func (a *SystemdAddons) monotonic() uint64 {
	if a.monoNow != nil {
		return a.monoNow()
	}
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return ^uint64(0) // no clock: nothing counts as started after it
	}
	return uint64(ts.Sec)*1_000_000 + uint64(ts.Nsec)/1_000
}
