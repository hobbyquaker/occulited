package system

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// occulited task 28 (maintainer, Q&A 2026-10-10): a migrated rc.d script stays an addon and is
// started - a CCU3 user's own hack among them (the maintainer's hdmi-wlan-disable, a link to
// /usr/local/bin/hdmi-wlan-disable.sh written for the CCU3's kernel, failing here). The Addons page
// says "came from the CCU" with the failed unit's last line, and offers one click to remove the
// rc.d entry: the entry and its .script twin through the helper's RemoveAddonEntry, the file a
// link pointed to outside /usr/local/addons/ only when the user ticks it, then the generator
// reloaded and the failed state forgotten.

// rcTargetDirs is where a link's target may be removed with the entry: the userfs, which is the
// user's - never the addons' tree (the uninstall's), the configuration, occulited's state.
var rcTargetDeny = []string{"/usr/local/addons/", "/usr/local/etc/", "/usr/local/var/"}

// RCTarget is where the addon's rc.d entry - or the .script twin behind the wrapper, which is the
// entry that was there before - leads when it is a link to a regular file under /usr/local/
// outside the addons' tree; "" otherwise.
func (r Root) RCTarget(id string) string {
	if !addonIDRe.MatchString(id) {
		return ""
	}
	for _, name := range []string{id + ".script", id} {
		entry := "/usr/local/etc/config/rc.d/" + name
		dest, err := os.Readlink(r.join(entry))
		if err != nil {
			continue
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(entry), dest)
		}
		dest = filepath.Clean(dest)
		if RCTargetRemovable(dest) {
			if st, err := os.Lstat(r.join(dest)); err == nil && st.Mode().IsRegular() {
				return dest
			}
		}
		return ""
	}
	return ""
}

// RCTargetRemovable: dest (as the box spells it) is under /usr/local/ and outside the trees an
// entry's target is never removed from.
func RCTargetRemovable(dest string) bool {
	if dest != filepath.Clean(dest) || !strings.HasPrefix(dest, "/usr/local/") || strings.Contains(dest, "..") {
		return false
	}
	for _, d := range rcTargetDeny {
		if strings.HasPrefix(dest, d) {
			return false
		}
	}
	return true
}

// RemoveRCEntryResult is what "remove the rc.d entry" removed.
type RemoveRCEntryResult struct {
	Removed []string `json:"removed"`
	Target  string   `json:"target,omitempty"` // the link's target, when there was one to name
}

// RemoveRCEntry removes the addon's rc.d entry and its .script twin (and nothing of an addon
// directory: an addon with one is uninstalled, not this), and with withTarget the regular file the
// entry led to outside /usr/local/addons/. The caller stops the unit before and reloads after.
func (r Root) RemoveRCEntry(id string, withTarget bool) (RemoveRCEntryResult, error) {
	var res RemoveRCEntryResult
	if !addonIDRe.MatchString(id) {
		return res, fmt.Errorf("invalid addon id")
	}
	if _, err := os.Lstat(r.join("/usr/local/addons/" + id)); err == nil {
		return res, fmt.Errorf("%s has an addon directory: uninstall it instead", id)
	}
	rcd := r.join("/usr/local/etc/config/rc.d/" + id)
	if _, err := os.Lstat(rcd); err != nil {
		return res, fmt.Errorf("unknown addon")
	}
	res.Target = r.RCTarget(id)
	removed, err := Priv.RemoveAddonEntry(rcd, r.join(AddonWWW+"/"+id), false)
	for _, p := range removed {
		res.Removed = append(res.Removed, r.unjoin(p))
	}
	if err != nil {
		return res, err
	}
	if withTarget && res.Target != "" {
		if err := Priv.RemoveRCTarget(r.join(res.Target)); err != nil {
			return res, fmt.Errorf("%s: %w", res.Target, err)
		}
		res.Removed = append(res.Removed, res.Target)
	}
	ForgetAddonScans(id)
	return res, nil
}

// unjoin is p as the box spells it.
func (r Root) unjoin(p string) string {
	root := filepath.Clean(string(r))
	if root == "/" || root == "." || root == "" {
		return p
	}
	return "/" + strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
}
