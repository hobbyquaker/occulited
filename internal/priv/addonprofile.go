package priv

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"github.com/hobbyquaker/occulited/internal/manifest"
	"github.com/hobbyquaker/occulited/internal/ownwalk"
)

// An addon's profile snippet (occulited task 136): runtime.bin appends a directory of the addon's
// tree to the PATH of interactive login shells through /usr/local/etc/profile.d/addon-<id>.sh, which
// root's login shell sources. Like the policy files root obeys (B-293) it is a named operation: the
// daemon names the snippet, the addon's directory and the relative bin; the helper checks both
// paths' shapes, checks that every component of bin is a directory and no link, gives bin to root
// with exact modes and checks it is clean (ownwalk.RootBin), renders the text itself
// (addonunit.ProfileSnippet) and writes it 0644 into a root-owned 0755 directory. An empty bin
// removes the snippet. Nothing generic reaches /usr/local/etc/profile.d.

const opAddonProfile = "addon-profile"

// AddonProfile asks the helper to write (bin set) or remove (bin "") an addon's profile snippet;
// adopt gives entries of another owner in bin to root (the install and an update only).
func (c Client) AddonProfile(path, home, bin string, adopt bool) error {
	return c.fileOp(request{Op: opAddonProfile, Path: path, Dir: home, Target: bin, Recursive: adopt})
}

// AddonProfile writes the snippet of the addon its name gives, or removes it when bin is "". A
// bin that is no directory inside home, or leaves it through a link, is refused - and a snippet
// already there for it is removed, so PATH never names what no longer passes. An unchanged snippet
// is not rewritten.
func (l Local) AddonProfile(path, home, bin string, adopt bool) error {
	id, ok := addonunit.ProfileID(filepath.Base(path))
	if !ok {
		return fmt.Errorf("%s: not an addon profile snippet", path)
	}
	if bin == "" {
		return l.Remove(path)
	}
	text, err := addonunit.ProfileSnippet(id, bin)
	if err != nil {
		return err
	}
	if filepath.Base(home) != id {
		return fmt.Errorf("%s is not the directory of the addon %s", home, id)
	}
	refuse := func(err error) error {
		if rerr := l.Remove(path); rerr != nil {
			return errors.Join(err, rerr)
		}
		return err
	}
	if err := BinInside(home, bin); err != nil {
		return refuse(err)
	}
	// the directory and everything below it root's, with modes nobody else writes, then checked
	// again: a snippet only for a clean directory
	if err := RootBinClean(home, bin, adopt); err != nil {
		return refuse(err)
	}
	dir := filepath.Dir(path)
	if err := l.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := l.Chown(dir, 0, 0, false); err != nil {
			return err
		}
	}
	if err := l.Chmod(dir, 0o755); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm() == 0o644 {
		if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, []byte(text)) {
			return nil
		}
	}
	return l.WriteFile(path, []byte(text), 0o644)
}

// BinInside checks that bin names a directory inside home: home and every component of bin a
// directory, none of them a link (the hand-back to the addon's user leaves bin out by its path, and
// a link would make that path another directory than the one root owns).
func BinInside(home, bin string) error {
	fi, err := os.Lstat(home)
	if err != nil {
		return fmt.Errorf("the addon's directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("the addon's directory %s is not a directory (a link is not followed)", home)
	}
	here := home
	for _, name := range strings.Split(bin, "/") {
		here = filepath.Join(here, name)
		fi, err := os.Lstat(here)
		if err != nil {
			return fmt.Errorf("runtime.bin %q: %w", bin, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("runtime.bin %q: %s is not a directory (a link is not followed)", bin, here)
		}
	}
	return nil
}

// RootBinWalk is ownwalk.RootBin; tests, which cannot give a file to root, put a fake here.
var RootBinWalk = ownwalk.RootBin

// RootBinClean gives the addon's bin directory to root (ownwalk.RootBin) and checks with a dry run
// that it is clean: every entry root's, modes 0755/0644, no link that may lead out, nothing else.
// Only with adopt are entries of another owner given to root - the install's and an update's files;
// at any other time such an entry is the addon user's own doing and the directory is not clean.
func RootBinClean(home, bin string, adopt bool) error {
	res := RootBinWalk(home, bin, ownwalk.Options{Adopt: adopt})
	if p := res.Problem(); p != "" {
		return fmt.Errorf("runtime.bin %q is not made root's: %s", bin, p)
	}
	check := RootBinWalk(home, bin, ownwalk.Options{DryRun: true})
	if p := check.Problem(); p != "" || check.Wrong > 0 {
		return fmt.Errorf("runtime.bin %q is not root's alone: %d entries wrong, first %s %s", bin, check.Wrong, check.FirstWrong, p)
	}
	return nil
}

// AddonBinDir is the absolute runtime.bin directory of an installed addon below root, from its
// stored manifest; "" for an addon without one, or a manifest that does not parse or is another
// addon's.
func AddonBinDir(root, id string) string {
	if !addonunit.IDRe.MatchString(id) {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, AddonPolicyDir, id+AddonManifestSuffix))
	if err != nil {
		return ""
	}
	m, err := manifest.Parse(b)
	if err != nil || m.ID != id || m.Runtime == nil || m.Runtime.Bin == "" || manifest.ValidBin(m.Runtime.Bin) != nil {
		return ""
	}
	return filepath.Join(root, addonunit.AddonsDir, id, m.Runtime.Bin)
}

// addonProfileAllowed: path is <ProfileDir>/addon-<id>.sh under the root, and - when a snippet is
// written - home is <AddonsDir>/<id> of the same id. The id it names, "" when refused.
func (p Policy) addonProfileAllowed(path, home string, write bool) string {
	rel, ok := p.rel(path)
	if !ok || filepath.Dir(rel) != addonunit.ProfileDir {
		return ""
	}
	id, ok := addonunit.ProfileID(filepath.Base(rel))
	if !ok || rel != addonunit.ProfileDir+"/"+addonunit.ProfileName(id) {
		return ""
	}
	if write {
		h, ok := p.rel(home)
		if !ok || h != addonunit.AddonsDir+"/"+id || filepath.Clean(home) != home {
			return ""
		}
	}
	return id
}

// doAddonProfile serves the operation; handled is false for any other.
func (s *Server) doAddonProfile(req request) (res response, handled bool) {
	if req.Op != opAddonProfile {
		return response{}, false
	}
	write := req.Target != ""
	if s.Policy.addonProfileAllowed(req.Path, req.Dir, write) == "" {
		s.log("helper: refused the addon profile snippet %s (%s)", req.Path, req.Dir)
		return refuse("addon profile snippet " + req.Path), true
	}
	id, _ := addonunit.ProfileID(filepath.Base(req.Path))
	if write {
		// checked here as well as in the operation: this is the boundary
		if _, err := addonunit.ProfileSnippet(id, req.Target); err != nil {
			s.log("helper: refused the addon profile snippet %s: %v", req.Path, err)
			return refuse("addon profile snippet " + req.Path + ": " + err.Error()), true
		}
	}
	if err := s.ops().AddonProfile(filepath.Clean(req.Path), req.Dir, req.Target, req.Recursive); err != nil {
		return response{Error: err.Error()}, true
	}
	return response{OK: true}, true
}
