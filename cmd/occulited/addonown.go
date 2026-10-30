package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/hobbyquaker/occulited/internal/ownwalk"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
)

// addonOwnFlag is `occulited -addon-own <id>` (task 107): a confined addon's directories given to its
// user before its unit starts. The fork's generated addon units run it through
// /usr/libexec/occu/lite-addon-own as root (ExecStartPre=+-).
//
// It is spelled as a flag on purpose. The fork's half may reach a box before an occulited that has
// the subcommand. An older occulited handed an unknown word would start a second daemon - as root,
// inside the addon's start; handed an unknown flag, Go's flag package refuses it and exits 2 before
// anything else happens, and the wrapper takes exactly that answer as "not supported yet".
const addonOwnFlag = "-addon-own"

// legacyOwnedSuffix is task 107's digest marker, <id>.owned beside the policy: a pass over the addon's
// directories skipped the walk when none of them had changed since a walk found every entry right.
// RedMatic changes its directories while it runs, so a Charly boot walked its whole tree anyway, about
// 11 s. Task 110 replaced it with the marker "full walk needed" (system.FullWalkSuffix); one left on a
// box is removed.
const legacyOwnedSuffix = ".owned"

// addonOwnEnv is what the subcommand works with; tests give their own.
type addonOwnEnv struct {
	root           system.Root
	walk           func(dirs []string, opt ownwalk.Options) ownwalk.Result
	policy         priv.Policy
	stdout, stderr io.Writer
	// ingressDir makes the addon's web ingress socket directory (openccu-lite task 406);
	// priv.Local's on the box, a fake in the tests. nil: nothing is made.
	ingressDir func(id string) error
}

// addonOwnMain runs the subcommand as root on the box and answers the exit code.
func addonOwnMain(args []string) int {
	if !priv.IsRoot() {
		fmt.Fprintln(os.Stderr, "occulited -addon-own: must run as root")
		return 1
	}
	return addonOwn(addonOwnEnv{root: "/", walk: ownwalk.Own, policy: priv.DefaultPolicy("/", ""), stdout: os.Stdout, stderr: os.Stderr, ingressDir: priv.Local{}.IngressDir}, args)
}

// addonOwn is the subcommand: the stored policy's directories (system.AddonOwnPlan), the helper's own
// boundary applied (priv.Policy.OwnTreeAllowed), then the walk (task 110):
//   - with the marker "full walk needed" - an install, an update or a switch to the addon's own user
//     since the last start - the whole tree, and the marker removed once that walk finished without a
//     problem;
//   - without it the quick check, a dry run over the top directories and their direct entries. An
//     entry there that the walk would give to the addon's user, or a problem, and the whole tree is
//     walked for this start after all.
//
// What it did goes to stdout, which is the unit's journal: a line with the count when it changed
// something, a line when the whole tree was walked and why, a line for what it left or could not do,
// nothing at all when the quick check found everything right. Exit 1 on a problem - the unit's "-"
// ignores it, the addon starts anyway and B-92's warning names what is still wrong.
func addonOwn(env addonOwnEnv, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.stderr, "usage: occulited -addon-own <addon id>")
		return 1
	}
	id := args[0]
	// openccu-lite task 406: an addon with a web ingress gets its socket directory's group now that
	// the group exists - at boot the group may be recreated after lighttpd's own preparation ran
	if env.ingressDir != nil && env.root.IngressRanges(id) != nil {
		if err := env.ingressDir(id); err != nil {
			fmt.Fprintf(env.stdout, "addon-own: %s: the ingress socket directory: %v\n", id, err)
		}
	}
	plan, err := system.AddonOwnPlan(env.root, id)
	if err != nil {
		fmt.Fprintf(env.stdout, "addon-own: %q: %v\n", id, err)
		return 1
	}
	if plan == nil {
		return 0 // runs as root, or has no policy: nothing is the addon user's to have
	}
	_ = os.Remove(filepath.Join(string(env.root), system.AddonPolicyDir, id+legacyOwnedSuffix))
	for _, r := range plan.Refused {
		fmt.Fprintf(env.stdout, "addon-own: %s: a data directory is not walked: %s\n", id, r)
	}
	if err := env.policy.OwnTreeAllowed(id, plan.Dirs, plan.UID); err != nil {
		fmt.Fprintf(env.stdout, "addon-own: %s: refused: %v\n", id, err)
		return 1
	}
	// TightenModes closes the tree to other users (openccu-lite B-252): the addon's www keeps its
	// mode (it is served to the browser), everything else becomes at most 0751/0640.
	opt := ownwalk.Options{UID: plan.UID, GID: plan.UID, TightenModes: true, PublicDirs: plan.Public}
	marked := env.root.FullWalkNeeded(id)
	if !marked {
		quick := opt
		quick.DryRun, quick.MaxDepth = true, ownwalk.QuickDepth
		q := env.walk(plan.Dirs, quick)
		// device nodes and files with more than one link keep their owner in the whole walk too
		switch changes := q.Wrong - q.Devices - q.HardLinks; {
		case q.Problem() != "":
			fmt.Fprintf(env.stdout, "addon-own: %s: the quick check did not look everywhere (%s): the whole tree is walked\n", id, q.Problem())
		case changes > 0:
			fmt.Fprintf(env.stdout, "addon-own: %s: the quick check found %d entries to put right at the top, first %s: the whole tree is walked\n", id, changes, q.FirstWrong)
		default:
			return 0
		}
	}
	res := env.walk(plan.Dirs, opt)
	problem := res.Problem()
	switch {
	case res.Fixed > 0:
		fmt.Fprintf(env.stdout, "addon-own: %s: %d of %d entries given to %s (%d) in %.2f s\n", id, res.Fixed, res.Checked, plan.User, plan.UID, res.Duration.Seconds())
	case marked && res.Wrong == 0 && problem == "":
		fmt.Fprintf(env.stdout, "addon-own: %s: the whole tree after an install or a policy change: %d entries, all %s's, in %.2f s\n", id, res.Checked, plan.User, res.Duration.Seconds())
	}
	if res.ModeTightened > 0 {
		fmt.Fprintf(env.stdout, "addon-own: %s: %d entries closed to other users (directories 0751, files 0640; the www tree left) in %.2f s\n", id, res.ModeTightened, res.Duration.Seconds())
	}
	if n := res.Devices + res.HardLinks; n > 0 {
		fmt.Fprintf(env.stdout, "addon-own: %s: %d entries keep another owner (%d device nodes, %d files with more than one link), first %s\n", id, n, res.Devices, res.HardLinks, res.FirstWrong)
	}
	if problem != "" {
		fmt.Fprintf(env.stdout, "addon-own: %s: %s\n", id, problem)
		return 1 // a marker stays: the next start walks the whole tree again
	}
	if marked {
		if err := os.Remove(env.root.FullWalkMarker(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(env.stdout, "addon-own: %s: the marker of the whole walk stays: %v\n", id, err)
		}
	}
	return 0
}
