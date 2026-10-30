package system

import (
	"log/slog"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The start order of an addon (task 94, section 6, optimisation 1). An addon's manifest
// may say which interface processes it talks to, runtime.needs; occulited writes that as one line
// to /usr/local/etc/config/addon-policy/<id>.needs, and the fork's occu-addons generator reads
// the file at the next boot and orders addon-<id>.service after exactly those units:
//
//	(no file)             undeclared - after rfd and hmipserver, as every addon started before
//	none                  needs no interface - right after the network (a broker, a web page)
//	rfd hmipserver        after the named units only
//
// The file is independent of the confinement mode: a root addon is ordered like a confined one.
// Undeclared stays the safe default, and so does anything occulited cannot vouch for: an id it
// does not know makes the whole declaration unusable (logged), because ordering an addon after
// less than it needs brings back the RPC errors at boot the default exists to avoid.

// AddonNeedsIDs are the interface processes an addon can declare, in the order the file lists
// them: BidCos-RF, HmIP (and the VirtualDevices behind it), BidCos-Wired.
var AddonNeedsIDs = []string{"rfd", "hmipserver", "hs485d"}

// needsLine is the .needs file's line for a declaration: "none" for an empty list, else the
// declared ids once each in AddonNeedsIDs' order. ok is false when there is nothing usable -
// no block, no needs, or an id outside AddonNeedsIDs, which is returned in unknown.
func needsLine(rt *AddonRuntime) (line string, ok bool, unknown []string) {
	if rt == nil || rt.Needs == nil {
		return "", false, nil
	}
	for _, id := range *rt.Needs {
		if !slices.Contains(AddonNeedsIDs, id) {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return "", false, unknown
	}
	if len(*rt.Needs) == 0 {
		return "none", true, nil
	}
	var ids []string
	for _, id := range AddonNeedsIDs {
		if slices.Contains(*rt.Needs, id) {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, " "), true, nil
}

// declaresConfinement says whether a stored runtime block says how the addon runs - what the
// Addons and Services pages' "undeclared" marking is about. A block counts as before, whatever it
// holds ({} included), except one that holds nothing but needs, start and session: the start order,
// the early start (task 119) and how the addon reads the session are no statement of what it needs
// to run, and docs/manifest-format.md says declaring them alone does not remove the marking. A key added to AddonRuntime later belongs
// in this list: daemon (B-158) and api_scopes (task 66) are statements about how the addon runs, so
// {"daemon": true, "needs": [...], "start": "early"} is as declared as {"daemon": true} alone
// (occulited B-20) - an addon that needs nothing beyond its own directories can say so and still
// declare its start order.
func (rt *AddonRuntime) declaresConfinement() bool {
	if rt == nil {
		return false
	}
	if rt.Needs == nil && rt.Session == nil && rt.Start == "" {
		return true
	}
	return rt.Root || len(rt.Capabilities) > 0 || len(rt.Groups) > 0 || len(rt.Paths) > 0 || len(rt.DataDirs) > 0 || len(rt.Ports) > 0 || len(rt.PortInfo) > 0 ||
		rt.Daemon || len(rt.APIScopes) > 0 || rt.WebIngress != nil
}

// NeedsLine is the start order an addon's declaration gives, as the .needs file spells it; ""
// for an undeclared or unusable one.
func (rt *AddonRuntime) NeedsLine() string {
	line, _, _ := needsLine(rt)
	return line
}

// writeAddonNeeds brings <id>.needs in line with the declaration: written when it says something
// usable, removed when it does not. It reports whether the file changed.
func (r Root) writeAddonNeeds(id string, rt *AddonRuntime) (bool, error) {
	path := filepath.Join(r.join(AddonPolicyDir), id+".needs")
	line, ok, unknown := needsLine(rt)
	if len(unknown) > 0 {
		slog.Warn("addon policy: runtime.needs names an interface this box does not know; the addon keeps the default start order", "addon", id, "unknown", unknown, "known", AddonNeedsIDs)
	}
	if !ok {
		if _, err := os.Lstat(path); err != nil {
			return false, nil
		}
		if err := Priv.AddonPolicyFile(path, addonunit.File{Remove: true}); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}
	want := line + "\n"
	if readFile(path) == want {
		return false, nil
	}
	if err := Priv.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	// the helper renders the line itself from the checked list (B-293); "none" is the empty one
	needs := []string{}
	if line != "none" {
		needs = strings.Fields(line)
	}
	return true, Priv.AddonPolicyFile(path, addonunit.File{Needs: &needs})
}

// RefreshAddonNeeds writes every stored policy's .needs file from what the addon declares now:
// the stored manifest when it has a needs, else the policy's stored block
// (MergeRuntime). For every source - catalog, user, migrated - because the start order is a fact
// about the addon, not a grant. Run at start after RefreshManifestRuntimes; returns the ids whose
// file changed.
func (a *SystemdAddons) RefreshAddonNeeds() []string {
	root := a.Scripts.Root
	var changed []string
	for id, p := range root.AddonPolicies() {
		if did, err := root.writeAddonNeeds(id, MergeRuntime(p.Runtime, a.declared(id))); err != nil {
			slog.Warn("addon policy: the start order file could not be written", "addon", id, "err", err)
		} else if did {
			changed = append(changed, id)
		}
	}
	sort.Strings(changed)
	return changed
}
