package priv

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hobbyquaker/occulited/internal/addonunit"
)

// occulited task 29 (maintainer, Q&A 2026-10-10): the known-useless CCU remnants go once at the
// first start after a switch. The daemon may not remove them with the generic operations (an
// addon's config directory is named-only since B-295, the userfs root is no write prefix), so this
// operation takes exactly the explicit list and checks each condition itself:
//   - <config>/addons/<id> (either spelling, never "www") when the addon is gone: no rc.d entry
//     (nor its .script twin) and no /usr/local/addons/<id> - the configuration of an addon removed
//     long ago, such as meine-homematic.de's addons/mh with its old VPN credentials;
//   - /usr/local/etc/config/homematic.regadom.err, the ReGa's crash dump (there is no ReGa);
//   - /usr/local/eQ-3-Backup when it holds no byte (the CCU3's empty backup folder).
// Nothing else; a link is removed as a link, never followed.

const opRemnantRemove = "ccu-remnant-remove"

// Remnant paths as the box spells them.
const (
	RemnantRegadomErr = "/usr/local/etc/config/homematic.regadom.err"
	RemnantEQ3Backup  = "/usr/local/eQ-3-Backup"
)

// RemoveCCURemnant asks the helper to remove one of task 29's remnants.
func (c Client) RemoveCCURemnant(path string) error {
	return c.fileOp(request{Op: opRemnantRemove, Path: path})
}

// RemoveCCURemnant removes path: a link or a file itself, a directory with what is in it.
func (Local) RemoveCCURemnant(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

// remnantRemovable is the explicit list and its conditions.
func (p Policy) remnantRemovable(path string) bool {
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") || rel != filepath.Clean(rel) {
		return false
	}
	switch rel {
	case RemnantRegadomErr, otherSpelling(RemnantRegadomErr):
		return true
	case RemnantEQ3Backup:
		return p.holdsNoBytes(path)
	}
	id, ok := addonConfigID(rel)
	if !ok || strings.Contains(id, "/") || !addonunit.IDRe.MatchString(id) || id == "www" {
		return false
	}
	return p.rcEntryGone(id) && p.entryGone(AddonHomeDir+id)
}

// holdsNoBytes: the tree under path has no regular file with content and no link (a link could be
// anything).
func (p Policy) holdsNoBytes(path string) bool {
	empty := true
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			empty = false
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if fi, ierr := d.Info(); ierr != nil || fi.Size() > 0 {
				empty = false
				return filepath.SkipAll
			}
		}
		return nil
	})
	return err == nil && empty
}

// remnant is the operation at the boundary.
func (s *Server) remnant(req request) response {
	if req.Name != "" || len(req.Args) > 0 || len(req.Data) > 0 || req.Dir != "" || req.Src != "" || req.Dst != "" || req.Target != "" {
		return refuse("ccu-remnant-remove takes a path and nothing else")
	}
	path, err := s.resolved(req.Path, false, s.Policy.remnantRemovable)
	if err != nil {
		s.log("helper: refused removing the CCU remnant %s: %v", req.Path, err)
		return refuse("CCU remnant " + req.Path)
	}
	if err := s.ops().RemoveCCURemnant(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return response{Error: err.Error()}
	}
	return response{OK: true}
}
