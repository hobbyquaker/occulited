package priv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// occulited task 28: "remove the rc.d entry" of a migrated script that is no addon - a CCU3 user's
// hack such as a link rc.d/hdmi-wlan-disable → /usr/local/bin/hdmi-wlan-disable.sh. The entry and
// its .script twin go through RemoveAddonEntry; the file the link led to goes through this
// operation, and only when the user ticked it: one regular file under /usr/local/, never in the
// addons' tree (the uninstall's), the configuration or the state directories, never a directory or
// a link, never followed.

// opRCTargetRemove removes the regular file an rc.d entry led to.
const opRCTargetRemove = "rctargetremove"

// rcTargetDeny are the trees no rc.d target is removed from.
var rcTargetDeny = []string{"/usr/local/addons/", "/usr/local/etc/", "/usr/local/var/"}

// RemoveRCTarget asks the helper to remove the file an rc.d entry led to.
func (c Client) RemoveRCTarget(path string) error {
	return c.fileOp(request{Op: opRCTargetRemove, Path: path})
}

// RemoveRCTarget removes path when it is a regular file; anything else is refused.
func (Local) RemoveRCTarget(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return os.Remove(path)
}

// rcTargetRemovable: path (relative to the policy's root) is below /usr/local/ and outside the
// denied trees.
func (p Policy) rcTargetRemovable(path string) bool {
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") || !strings.HasPrefix(rel, "/usr/local/") || rel != filepath.Clean(rel) {
		return false
	}
	for _, d := range rcTargetDeny {
		if strings.HasPrefix(rel, d) {
			return false
		}
	}
	return true
}

// rcTarget is the operation at the boundary.
func (s *Server) rcTarget(req request) response {
	if req.Name != "" || len(req.Args) > 0 || len(req.Data) > 0 || req.Dir != "" || req.Src != "" || req.Dst != "" || req.Target != "" {
		return refuse("rctargetremove takes a path and nothing else")
	}
	path, err := s.resolved(req.Path, false, s.Policy.rcTargetRemovable)
	if err != nil {
		s.log("helper: refused removing the rc.d target %s: %v", req.Path, err)
		return refuse("rc.d target " + req.Path)
	}
	if err := s.ops().RemoveRCTarget(path); err != nil {
		return response{Error: err.Error()}
	}
	return response{OK: true}
}
