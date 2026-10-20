package system

import (
	"log/slog"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"os"
	"path/filepath"
	"sort"
)

// The early start of an addon (task 119, D-75). An addon's manifest may declare
// runtime.start "early": it promises to cope with interface processes that do not answer yet - it
// retries within seconds and logs no errors while it waits. occulited writes that as one line to
// /usr/local/etc/config/addon-policy/<id>.start, and the fork's occu-addons generator reads the
// file at the next boot: such an addon's unit is ordered after the network, lighttpd and
// occulited only, not on its interface units, so it starts before they are ready. It does not
// start them either (openccu-lite B-308: no addon unit has Wants= on an interface unit).
//
//	(no file)   the default ordering, after the addon's needs
//	early       started early
//
// The file is written only when the addon declares it and the user has not switched the early
// start off - globally or for this addon, on the Addons page (EarlyStart). It is independent of
// the confinement mode, like <id>.needs, and takes effect at the next boot.

// AddonStartEarly is the one value of runtime.start that means something.
const AddonStartEarly = "early"

// DeclaresEarlyStart says whether the runtime block declares the early start.
func (rt *AddonRuntime) DeclaresEarlyStart() bool {
	return rt != nil && rt.Start == AddonStartEarly
}

// earlyStartOn is the user's switch for an addon: on unless EarlyStart says otherwise.
func (a *SystemdAddons) earlyStartOn(id string) bool {
	return a.EarlyStart == nil || a.EarlyStart(id)
}

// StartEarly is what the Addons page shows for an addon: whether it declares the early start
// (what its manifest or its stored block says now) and whether it will start early at the next
// boot (declared, and not switched off).
func (a *SystemdAddons) StartEarly(id string) (declared, early bool) {
	var stored *AddonRuntime
	if p := a.Scripts.Root.ReadAddonPolicy(id); p != nil {
		stored = p.Runtime
	}
	declared = MergeRuntime(stored, a.declared(id)).DeclaresEarlyStart()
	return declared, declared && a.earlyStartOn(id)
}

// writeAddonStart brings <id>.start in line with the declaration and the switches: "early" when
// the addon declares it and the user did not switch it off, removed otherwise. It reports whether
// the file changed.
func (a *SystemdAddons) writeAddonStart(id string, rt *AddonRuntime) (bool, error) {
	path := filepath.Join(a.Scripts.Root.join(AddonPolicyDir), id+".start")
	if rt != nil && rt.Start != "" && rt.Start != AddonStartEarly {
		slog.Warn("addon policy: runtime.start is not \"early\"; the addon keeps the default start order", "addon", id, "start", rt.Start)
	}
	if !rt.DeclaresEarlyStart() || !a.earlyStartOn(id) {
		if _, err := os.Lstat(path); err != nil {
			return false, nil
		}
		if err := Priv.AddonPolicyFile(path, addonunit.File{Remove: true}); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}
	want := AddonStartEarly + "\n"
	if readFile(path) == want {
		return false, nil
	}
	if err := Priv.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, Priv.AddonPolicyFile(path, addonunit.File{Early: true}) // the helper's text (B-293)
}

// The start delay (openccu-lite task 369). An addon may wait at boot, after its radio interfaces
// answer, for as many seconds as the user set on the Addons page: at a boot every client hits
// hmipserver in the same second its port opens, and on a large network their inits and device
// reads block its event loop for seconds. occulited writes the seconds as one line to
// addon-policy/<id>.delay; the occu-addons generator reads it at the next boot, orders such an
// addon after its interface units even if it could start early, and lets its unit wait the delay
// before the start - at boot only, never on a start or restart later on.
//
//	(no file)   no delay
//	30          thirty seconds after the interfaces

// startDelay is the user's delay for an addon in seconds, 0 for none or one out of range.
func (a *SystemdAddons) startDelay(id string) int {
	if a.StartDelay == nil {
		return 0
	}
	s := a.StartDelay(id)
	if addonunit.ValidateDelay(s) != nil {
		return 0
	}
	return s
}

// writeAddonDelay brings <id>.delay in line with the user's delay: the seconds, or no file. It
// reports whether the file changed.
func (a *SystemdAddons) writeAddonDelay(id string) (bool, error) {
	path := filepath.Join(a.Scripts.Root.join(AddonPolicyDir), id+addonunit.KindDelay)
	s := a.startDelay(id)
	if s == 0 {
		if _, err := os.Lstat(path); err != nil {
			return false, nil
		}
		if err := Priv.AddonPolicyFile(path, addonunit.File{Remove: true}); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}
	if readFile(path) == addonunit.RenderDelay(s) {
		return false, nil
	}
	if err := Priv.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, Priv.AddonPolicyFile(path, addonunit.File{Delay: &s}) // the helper's text (B-293)
}

// RefreshAddonStart writes every stored policy's .start file from what the addon declares now
// (the stored manifest when it says anything, else the policy's stored block) and from
// the switches, and its .delay file from the user's delay (task 369). Run at start after RefreshManifestRuntimes and after the user changed a switch;
// returns the ids whose file changed.
func (a *SystemdAddons) RefreshAddonStart() []string {
	root := a.Scripts.Root
	var changed []string
	for id, p := range root.AddonPolicies() {
		did, err := a.writeAddonStart(id, MergeRuntime(p.Runtime, a.declared(id)))
		if err != nil {
			slog.Warn("addon policy: the early start file could not be written", "addon", id, "err", err)
		}
		didDelay, err := a.writeAddonDelay(id)
		if err != nil {
			slog.Warn("addon policy: the start delay file could not be written", "addon", id, "err", err)
		}
		if did || didDelay {
			changed = append(changed, id)
		}
	}
	sort.Strings(changed)
	return changed
}
