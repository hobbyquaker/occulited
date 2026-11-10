package system

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hobbyquaker/occulited/internal/addonunit"
)

// runtime.bin (occulited task 136): an addon's bin directory at the end of the PATH of interactive
// login shells. The image's /etc/profile sources /usr/local/etc/profile.d/*.sh; an addon whose stored
// manifest declares runtime.bin, and that is installed and switched on, has addon-<id>.sh there
// (addonunit.ProfileSnippet: appended, interactive shells only, only while the directory is root's).
// The snippet is the helper's to write (root's login shell runs it): the text is rendered from the id
// and the checked path, never taken from the addon, and the helper writes it only once it has given
// the directory to root and found it clean (priv.RootBinClean).
//
// One sync decides it all, from the stored manifests, the rc.d entries' executable bits (Disable on
// the Addons page) and the Services page's switch (a masked addon unit, B-131): after an install or
// update, an uninstall, a switch either way, and at every start - which also removes the snippet of
// an addon that went while this binary did not run.

// ProfileResult is what the sync did for one addon.
type ProfileResult struct {
	ID string
	// Action: written (new or changed), kept (unchanged; the helper checked bin again), removed,
	// error (the helper refused or failed: a snippet there was removed).
	Action string
	Reason string
}

// addonProfileWanted is id -> bin for the installed addons whose stored manifest declares
// runtime.bin and that are switched on: their rc.d entry is executable and off(id) is false.
func (r Root) addonProfileWanted(off func(id string) bool) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(r.join(AddonPolicyDir))
	if err != nil {
		return out
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), AddonManifestSuffix)
		if !ok || !addonIDRe.MatchString(id) {
			continue
		}
		m := r.ReadAddonManifest(id)
		if m == nil || m.Runtime == nil || m.Runtime.Bin == "" {
			continue
		}
		if !r.AddonEnabled(id) || (off != nil && off(id)) {
			continue
		}
		out[id] = m.Runtime.Bin
	}
	return out
}

// syncAddonProfiles writes the snippet of every addon addonProfileWanted names and removes every
// other addon-<id>.sh in the profile directory. Results by id, sorted.
//
// adopt names the addons just installed or updated: their bin's entries of another owner are given to
// root (the package's files). For every other addon such an entry was made by its user after the
// install, and the directory is not clean: no snippet.
func syncAddonProfiles(root Root, off func(id string) bool, adopt ...string) []ProfileResult {
	want := root.addonProfileWanted(off)
	dir := root.join(addonunit.ProfileDir)
	have := map[string]bool{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if id, ok := addonunit.ProfileID(e.Name()); ok {
				have[id] = true
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return []ProfileResult{{Action: "error", Reason: err.Error()}}
	}
	ids := make([]string, 0, len(want)+len(have))
	for id := range want {
		ids = append(ids, id)
	}
	for id := range have {
		if _, ok := want[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var results []ProfileResult
	for _, id := range ids {
		path := filepath.Join(dir, addonunit.ProfileName(id))
		home := root.join(addonunit.AddonsDir + "/" + id)
		bin, wanted := want[id]
		before, _ := os.ReadFile(path)
		if err := Priv.AddonProfile(path, home, bin, slices.Contains(adopt, id)); err != nil {
			results = append(results, ProfileResult{ID: id, Action: "error", Reason: err.Error()})
			continue
		}
		action := "removed"
		if wanted {
			action = "written"
			if after, err := os.ReadFile(path); err == nil && before != nil && string(after) == string(before) {
				action = "kept"
			}
		}
		results = append(results, ProfileResult{ID: id, Action: action})
	}
	return results
}

// SyncAddonProfiles brings the profile snippets in line with the addons (occulited task 136), with
// the Services page's switch counted as off: after a switch there. It logs what it changed or could
// not, and answers what it changed, as "<id> written" or "<id> removed".
func (s SystemdServices) SyncAddonProfiles() []string {
	return logProfiles(syncAddonProfiles(s.Root, s.addonSwitchedOff))
}

// SyncAddonProfiles is the same for the addon manager: at start and after Enable and Disable.
func (a *SystemdAddons) SyncAddonProfiles() []string {
	return logProfiles(syncAddonProfiles(a.Scripts.Root, a.Systemd.addonSwitchedOff))
}

// addonSwitchedOff says whether an addon's unit is switched off on the Services page (B-131).
func (s SystemdServices) addonSwitchedOff(id string) bool {
	return s.switchedOff("addon-" + id + ".service")
}

// logProfiles logs the sync's changes and refusals and answers the changes.
func logProfiles(results []ProfileResult) []string {
	var changed []string
	for _, r := range results {
		switch r.Action {
		case "error":
			slog.Warn("addons: runtime.bin: the profile snippet", "addon", r.ID, "err", r.Reason)
		case "written", "removed":
			slog.Info("addons: runtime.bin: the profile snippet "+r.Action, "addon", r.ID)
			changed = append(changed, r.ID+" "+r.Action)
		}
	}
	return changed
}

// profileOutput is the lines an install or uninstall adds to its output for the sync's results:
// what changed and what was refused, nothing for a kept snippet.
func profileOutput(results []ProfileResult) string {
	var b strings.Builder
	for _, r := range results {
		switch r.Action {
		case "written":
			b.WriteString("\n[system] runtime.bin: " + filepath.Join(addonunit.AddonsDir, r.ID) + "/… is root's and at the end of the PATH of new interactive login shells (" + filepath.Join(addonunit.ProfileDir, addonunit.ProfileName(r.ID)) + ")")
		case "removed":
			b.WriteString("\n[system] runtime.bin: " + filepath.Join(addonunit.ProfileDir, addonunit.ProfileName(r.ID)) + " removed")
		case "error":
			b.WriteString("\n[system] runtime.bin of " + r.ID + " not put on the PATH: " + r.Reason)
		}
	}
	return b.String()
}

// syncProfilesAfterChange is the sync at the end of an install, an update or an uninstall: logged,
// and its lines for the run's output.
func (a *SystemdAddons) syncProfilesAfterChange(adopt ...string) string {
	results := syncAddonProfiles(a.Scripts.Root, a.Systemd.addonSwitchedOff, adopt...)
	logProfiles(results)
	return profileOutput(results)
}
