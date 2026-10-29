package priv

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"golang.org/x/sys/unix"
)

// The addons' own trees are outside the generic operations (openccu-lite B-294).
//
// Until B-294 /usr/local/addons/ was a write prefix (Paths). B-293 closed what rc.d and the web
// trees lead into, but the rest of a root addon's directory - its bin/, its node_modules, whatever
// its rc.d script starts - stayed writable through the generic write, rename, symlink and
// removeall, and root runs it. A confined addon's directory is its user's (OwnTree), and the
// addon writes it itself; the daemon writes nothing there. What the daemon does need under
// /usr/local/addons/ are three cases, each a named operation of exactly one shape:
//   - RemoveAddonHome: the uninstall's rmdir of an addon's emptied directory - and since B-295 of
//     its emptied config directory, and the first boot's of the CCU's empty addons/mh (a confined addon's
//     uninstall runs as its user, which cannot remove the directory in root's /usr/local/addons) -
//     a link, a file or an empty directory, never anything with content, and only once the addon's
//     rc.d entry is gone;
//   - MarkNeoServerDisabled: mediola's NEO Server's own switch, NeoServerHome/Disabled - an empty
//     file, created in the existing directory and never through a link;
//   - RemoveNeoServerHome: the NEO Server leftover's directory (and since B-295 its config directory)
//     with everything in it, once its rc.d
//     entry and its web entry are gone (RemoveAddonEntry first).

const (
	opAddonHomeRemove     = "addon-home-remove"
	opNeoServerDisabled   = "neoserver-disabled"
	opNeoServerHomeRemove = "neoserver-home-remove"
)

const (
	// NeoServerHome is mediola's NEO Server's directory, which OpenCCU unpacks onto the userfs (task 37).
	NeoServerHome = AddonHomeDir + "mediola"
	// NeoServerRCEntry is its rc.d entry's name; the web entry is "mediola".
	NeoServerRCEntry = "97NeoServer"
	// NeoServerConfig is its config directory; a root addon's config directory is named-only
	// (B-295), so the leftover's removal takes it as well.
	NeoServerConfig = "/usr/local/etc/config/addons/mediola"
	// NeoServerDisabledMarker is the vendor's switch: present, the rc.d script starts nothing.
	NeoServerDisabledMarker = NeoServerHome + "/Disabled"
)

// RemoveAddonHome asks the helper to remove an addon's emptied directory.
func (c Client) RemoveAddonHome(path string) error {
	return c.fileOp(request{Op: opAddonHomeRemove, Path: path})
}

// MarkNeoServerDisabled asks the helper to create the NEO Server's Disabled marker.
func (c Client) MarkNeoServerDisabled(path string) error {
	return c.fileOp(request{Op: opNeoServerDisabled, Path: path})
}

// RemoveNeoServerHome asks the helper to remove the NEO Server's directory.
func (c Client) RemoveNeoServerHome(path string) error {
	return c.fileOp(request{Op: opNeoServerHomeRemove, Path: path})
}

// RemoveAddonHome removes path when it is a link, a file or an empty directory; a directory with
// anything in it stays (the error says so), a missing one is not an error. occulited B-118: a
// directory that holds only empty directories counts as emptied - an uninstall left longpressdim's
// etc/ and var/, which the install had made for it - so they go first, deepest first; a directory
// with a file or a link anywhere below stays with everything above it. Nothing below is followed.
func (l Local) RemoveAddonHome(path string) error {
	if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
		l.pruneEmptyDirs(path)
	}
	return l.Remove(path)
}

// pruneEmptyDirs removes the empty directories below dir, deepest first, through descriptors opened
// without following a link (dir itself as Local follows paths: resolved by the Server first).
func (l Local) pruneEmptyDirs(dir string) {
	var fd int
	var err error
	if l.noFollow {
		parent, base := splitPath(dir)
		pfd, perr := openDir(parent, false, 0)
		if perr != nil {
			return
		}
		fd, err = unix.Openat(pfd, base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(pfd)
	} else {
		fd, err = unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return
	}
	pruneEmptyAt(fd, 0)
}

// pruneEmptyAt removes the empty directories below the directory fd (which it closes) and answers
// whether it is empty after that. A depth of 32 is as deep as it goes.
func pruneEmptyAt(fd, depth int) bool {
	f := os.NewFile(uintptr(fd), "")
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return false
	}
	empty := true
	for _, e := range entries {
		if !e.IsDir() || depth >= 32 { // a file, a link or anything else: kept
			empty = false
			continue
		}
		sub, err := unix.Openat(fd, e.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil || !pruneEmptyAt(sub, depth+1) || unix.Unlinkat(fd, e.Name(), unix.AT_REMOVEDIR) != nil {
			empty = false
		}
	}
	return empty
}

// MarkNeoServerDisabled creates the empty marker 0644 in its existing directory; an existing marker
// stays as it is.
func (l Local) MarkNeoServerDisabled(path string) error {
	fi, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: not a directory", filepath.Dir(path))
	}
	return l.Touch(path, 0o644)
}

// RemoveNeoServerHome removes the directory with everything in it (a link: the link).
func (l Local) RemoveNeoServerHome(path string) error { return l.RemoveAll(path) }

// addonHomeID is the addon id of an addon's directory - AddonHomeDir + <id>, or since B-295 its
// config directory <config>/addons/<id> in either spelling (not the web trees' "www") - "" when
// path is none.
func (p Policy) addonHomeID(path string) string {
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") {
		return ""
	}
	if id, found := strings.CutPrefix(rel, AddonHomeDir); found && addonunit.IDRe.MatchString(id) {
		return id
	}
	if id, ok := addonConfigID(rel); ok && !strings.Contains(id, "/") {
		return id
	}
	return ""
}

// addonConfigID is the first component below <config>/addons/ (either spelling) when rel is an
// addon's config directory or lies below one; the web trees' directory "www" is none (B-293 closes
// it as a whole).
func addonConfigID(rel string) (string, bool) {
	for _, d := range configDirSpellings {
		rest, found := strings.CutPrefix(rel, d+"addons/")
		if !found {
			continue
		}
		id, _, _ := strings.Cut(rest, "/")
		if id == "" || id == "www" {
			return "", false
		}
		return rest, true
	}
	return "", false
}

// addonConfinedUID is the uid the helper's own drop-in confines the addon to (openccu-lite B-295):
// <AddonPolicyDir>/<id>.conf as AddonPolicyFile rendered it - a regular file of the helper's user,
// not a link - read by addonunit.ConfinedUID. ok is false for a root addon, an addon without a
// drop-in, and anything else.
func (p Policy) addonConfinedUID(id string) (int, bool) {
	if p.AddonPolicyDir == "" || !addonunit.IDRe.MatchString(id) {
		return 0, false
	}
	path := filepath.Join(p.Root, p.AddonPolicyDir, id+addonunit.KindDropIn)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Geteuid()) {
		return 0, false
	}
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return 0, false
	}
	return addonunit.ConfinedUID(id, string(b))
}

// addonConfigClosed: rel is an addon's config directory, or lies in one, and the addon is not
// confined (B-295). A root addon's script may source or run what is there (the Email addon's
// userscript.tcl, a settings file read with "."), and an id without a drop-in is root's as soon as
// an installer makes it one - or, like the CCU's addons/mh, nobody's. A confined addon's directory
// is its own user's, and the daemon only makes it (SetPolicy, after the drop-in).
func (p Policy) addonConfigClosed(rel string) bool {
	rest, ok := addonConfigID(rel)
	if !ok {
		return false
	}
	id, _, _ := strings.Cut(rest, "/")
	_, confined := p.addonConfinedUID(id)
	return !confined
}

// entryGone: neither spelling of the config directory has rel (an rc.d or web entry) any more.
func (p Policy) entryGone(rel string) bool {
	for _, r := range []string{rel, otherSpelling(rel)} {
		if r == "" {
			continue
		}
		full := r
		if filepath.Clean(p.Root) != "/" {
			full = filepath.Join(p.Root, r)
		}
		if _, err := os.Lstat(full); err == nil || !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// rcEntryGone: the addon's rc.d entry and the script beside the wrapper are gone.
func (p Policy) rcEntryGone(name string) bool {
	if p.AddonRCDir == "" {
		return false
	}
	e := strings.TrimSuffix(p.AddonRCDir, "/") + "/" + name
	return p.entryGone(e) && p.entryGone(e+".script")
}

// addonHomeRemovable: an addon's directory whose rc.d entry is gone.
func (p Policy) addonHomeRemovable(path string) bool {
	id := p.addonHomeID(path)
	return id != "" && p.rcEntryGone(id)
}

// isRel: path is exactly rel under the root.
func (p Policy) isRel(path, rel string) bool {
	r, ok := p.rel(path)
	return ok && !strings.Contains(r, "..") && r == rel
}

// neoServerHomeRemovable: the NEO Server's directory, its rc.d entry and its web entry gone.
func (p Policy) neoServerHomeRemovable(path string) bool {
	if !(p.isRel(path, NeoServerHome) || p.isRel(path, NeoServerConfig) || p.isRel(path, otherSpelling(NeoServerConfig))) || !p.rcEntryGone(NeoServerRCEntry) {
		return false
	}
	for _, r := range p.CGIRoots {
		if strings.HasPrefix(r, "/usr/local/etc/config/") && !p.entryGone(strings.TrimSuffix(r, "/")+"/mediola") {
			return false
		}
	}
	return true
}

// doAddonHome serves the three operations; handled is false for any other.
func (s *Server) doAddonHome(req request) (res response, handled bool) {
	ops := s.ops()
	fail := func(err error) response {
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true}
	}
	switch req.Op {
	case opAddonHomeRemove:
		// the entry itself, never followed: a link goes, not what it leads to
		path, err := s.resolved(req.Path, false, s.Policy.addonHomeRemovable)
		if err != nil {
			s.log("helper: refused removing the addon directory %s: %v", req.Path, err)
			return refuse("addon directory " + req.Path), true
		}
		return fail(ops.RemoveAddonHome(path)), true
	case opNeoServerDisabled:
		marker := func(p string) bool { return s.Policy.isRel(p, NeoServerDisabledMarker) }
		path, err := s.resolved(req.Path, false, marker)
		if err != nil {
			s.log("helper: refused the NEO Server marker %s: %v", req.Path, err)
			return refuse("NEO Server marker " + req.Path), true
		}
		return fail(ops.MarkNeoServerDisabled(path)), true
	case opNeoServerHomeRemove:
		path, err := s.resolved(req.Path, false, s.Policy.neoServerHomeRemovable)
		if err != nil {
			s.log("helper: refused removing the NEO Server directory %s: %v", req.Path, err)
			return refuse("NEO Server directory " + req.Path), true
		}
		return fail(ops.RemoveNeoServerHome(path)), true
	}
	return response{}, false
}
