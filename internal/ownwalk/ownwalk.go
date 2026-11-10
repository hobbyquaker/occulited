// Package ownwalk gives directory trees to an owner without ever following a symbolic link (task
// 107, and B-92's repair). It is what root runs over the directories of a confined addon, which that
// addon's own user controls: the user can plant a link to /etc/config/shadow, swap a directory for a
// link while the walk is inside it, or hard-link a file of root's into its tree. None of that may
// make root change an owner outside the tree.
//
// How it holds:
//
//   - **No path is resolved twice.** Every entry is opened relative to the descriptor of the
//     directory it was read from (openat with O_PATH|O_NOFOLLOW), checked through that descriptor
//     (statx with AT_EMPTY_PATH), and changed through it (fchownat with AT_EMPTY_PATH and
//     AT_SYMLINK_NOFOLLOW). A directory is read through a descriptor opened as "." below the
//     entry's own descriptor, never by name. Swapping a name after it was opened changes nothing:
//     the walk holds the inode it checked, and a link is never entered.
//   - **A symbolic link is not followed and not changed.** Its owner grants nothing.
//   - **One file system.** An entry whose device (or, where statx reports it, mount id) differs
//     from its top directory's is neither changed nor entered: a mount below an addon's directory
//     is somebody else's.
//   - **The top directories are reached safely.** Each one is resolved from "/" one component at a
//     time with O_NOFOLLOW; every directory a name is looked up in must be one only a trusted user
//     can change (owned by root or the walk's own user, and not writable by anyone else, except a
//     sticky directory whose entry belongs to a trusted user). A link on the way is followed only
//     when such a directory holds it and a trusted user owns it (/etc/config on the image); a link
//     anywhere else, or as the top directory itself, is refused or skipped.
//   - **Hard links.** A file with more than one link and another owner is changed only when the
//     kernel's fs.protected_hardlinks is on: then the addon's user cannot have linked a file it
//     could not already write. Without it such a file is left alone and counted.
//   - **Device nodes** are never given away: owning /dev/sda's node is owning the disk.
//   - **Only wrong owners change.** An entry that already has the owner and group is not touched.
//
// The walk is bounded (MaxEntries, MaxDepth); what it did not finish is said in the result.
package ownwalk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Options says what to give to whom.
type Options struct {
	// UID and GID are the owner every entry should have.
	UID, GID int
	// DryRun only counts what is wrong and changes nothing.
	DryRun bool
	// MaxEntries is how many entries the walk looks at before it stops; 0 = 2,000,000.
	MaxEntries int
	// MaxDepth is how deep below a top directory the walk goes; 0 = 256. Every level holds one
	// open descriptor. QuickDepth is the quick check.
	MaxDepth int
	// TightenModes, when set, also takes the world-read and world-write bits off the tree, and the
	// group-write bit, so a confined addon's tree is the addon's alone (openccu-lite B-252): every
	// directory becomes at most 0751 (kept traversable so a web path through it still works, but not
	// listable), every regular file at most 0640 - with the group the addon's own (UID == GID), that
	// is 0600 for anyone else. Only bits are cleared, never added; a link and a special file are
	// left. Regular files under a PublicDir keep their mode: an addon's web content is served to the
	// browser (occulited, and lighttpd's own error pages for a proxied addon), so it stays
	// world-readable. The mode is a reason to change an entry as much as the owner is, so a tree
	// installed 0755 is tightened at the next start, whether the quick check or the whole walk meets it.
	TightenModes bool
	// PublicDirs are resolved absolute directories whose regular files keep their mode under
	// TightenModes (the addon's www). A file exactly under one, at any depth.
	PublicDirs []string
	// Exclude are absolute directories, spelled as the walk reaches them (a top directory, then
	// "/" and each name), that are neither changed nor entered: an addon's runtime.bin, which is
	// root's (occulited task 136, RootBin) and never given to the addon's user.
	Exclude []string
	// Adopt is RootBin's: an entry of another owner is given to root. Only the install and an
	// update adopt - the package's files, just written by root's update_script; at any other time
	// such an entry was made by the addon's user after the install, and it is an error, left as it
	// is (the directory is not clean, so it is not put on the PATH).
	Adopt bool

	// rootBin is RootBin's walk: the wanted modes are exact (directories 0755, regular files 0755
	// when any execute bit is set, else 0644), every other kind of entry is an error, and a link is
	// an error unless its target is relative without ".." (it then stays below the directory).
	rootBin bool

	hooks *hooks // tests only
}

// QuickDepth is MaxDepth for the quick check (task 110): a top directory and its direct entries are
// looked at - with every rule of the walk - and no directory below it is entered. A confined addon's
// start and B-92's hourly look take it; the whole tree is walked after an install or update of the
// addon, and by Fix ownership.
const QuickDepth = 1

// hooks are the tests' seams: a fake ownership layer (owner, chown), a point to race the walk
// (event), and the sysctl.
type hooks struct {
	owner              func(st Stat) (uid, gid uint32)
	chown              func(fd, uid, gid int) error
	chmod              func(fd int, mode uint32) error
	event              func(what, path string)
	protectedHardlinks func() bool
	// trusted is one more uid to trust: in a user namespace the host's root shows as the overflow
	// uid, and "/" belongs to it
	trusted *uint32
}

// Result is what a walk found and did. The counts are entries; a path is on the box.
type Result struct {
	// Checked is every entry looked at, the top directories included.
	Checked int `json:"checked"`
	// Wrong is every entry (links aside) whose owner or group was not the wanted one.
	Wrong int `json:"wrong"`
	// Fixed is how many of those were changed.
	Fixed int `json:"fixed"`
	// ModeTightened is how many entries had world (or group-write) bits taken off (B-252).
	ModeTightened int `json:"mode_tightened,omitempty"`
	// FirstWrong is the first wrong entry met, for a message.
	FirstWrong string `json:"first_wrong,omitempty"`
	// RootOwned counts the wrong entries whose owner is root, FirstRootOwned is the first: B-92's
	// warning names such a file, and a dry run is how it looks where only root can (B-123).
	RootOwned      int    `json:"root_owned,omitempty"`
	FirstRootOwned string `json:"first_root_owned,omitempty"`
	// Symlinks were seen and neither followed nor changed.
	Symlinks int `json:"symlinks,omitempty"`
	// Mounts are entries on another file system than their top directory: not changed, not entered.
	Mounts int `json:"mounts,omitempty"`
	// Devices are device nodes with another owner: left as they are.
	Devices int `json:"devices,omitempty"`
	// HardLinks are files with more than one link and another owner, left as they are because
	// fs.protected_hardlinks is off.
	HardLinks int `json:"hard_links,omitempty"`
	// TooDeep are directories below MaxDepth that were not entered.
	TooDeep int `json:"too_deep,omitempty"`
	// Excluded are entries under Options.Exclude that were left as they are.
	Excluded int `json:"excluded,omitempty"`
	// Skipped are top directories that were not walked and need no word: missing, or a link (an
	// addon's www directory is a link into its own directory on the image).
	Skipped []string `json:"skipped,omitempty"`
	// Refused are top directories that were not walked because reaching them was not safe, with
	// the reason.
	Refused []string `json:"refused,omitempty"`
	// Errors are the first errors met (at most maxErrors); ErrorCount counts all of them.
	Errors     []string `json:"errors,omitempty"`
	ErrorCount int      `json:"error_count,omitempty"`
	// Incomplete: MaxEntries was reached.
	Incomplete bool `json:"incomplete,omitempty"`
	// Duration of the walk.
	Duration time.Duration `json:"duration"`
}

// Left is how many wrong entries were not put right - neither given the owner nor tightened.
func (r Result) Left() int { return r.Wrong - r.Fixed - r.ModeTightened }

// Problem is a one-line account of what went wrong, "" when nothing did: a refused directory, an
// error, an unfinished walk. Entries left alone on purpose (devices, hard links) are not a problem
// of the walk; Left counts them.
func (r Result) Problem() string {
	var parts []string
	if len(r.Refused) > 0 {
		parts = append(parts, "refused "+strings.Join(r.Refused, "; "))
	}
	if r.ErrorCount > 0 {
		parts = append(parts, fmt.Sprintf("%d error(s), first: %s", r.ErrorCount, r.Errors[0]))
	}
	if r.Incomplete {
		parts = append(parts, fmt.Sprintf("stopped after %d entries", r.Checked))
	}
	return strings.Join(parts, "; ")
}

const (
	defaultMaxEntries = 2_000_000
	defaultMaxDepth   = 256
	maxErrors         = 10
	maxLinkHops       = 40
)

// Stat is what the walk reads about an entry.
type Stat struct {
	Mode  uint32 // type and permission bits, as st_mode
	UID   uint32
	GID   uint32
	Nlink uint64
	Dev   uint64
	Ino   uint64
	MntID uint64
	// HasMntID: the kernel reported the mount id (statx, Linux 5.8)
	HasMntID bool
}

func (s Stat) typ() uint32 { return s.Mode & unix.S_IFMT }

// Own walks each directory and gives every entry below it, and the directory itself, to
// opt.UID:opt.GID. The directories are absolute, clean paths; a directory given twice (or reached
// twice) is walked once.
func Own(dirs []string, opt Options) Result {
	start := time.Now()
	w := &walker{opt: opt, seen: map[[2]uint64]bool{}}
	if w.opt.MaxEntries <= 0 {
		w.opt.MaxEntries = defaultMaxEntries
	}
	if w.opt.MaxDepth <= 0 {
		w.opt.MaxDepth = defaultMaxDepth
	}
	if w.opt.hooks == nil {
		w.opt.hooks = &hooks{}
	}
	w.protected = w.readProtectedHardlinks()
	for _, d := range dirs {
		if w.res.Incomplete {
			break
		}
		w.top(d)
	}
	w.res.Duration = time.Since(start)
	return w.res
}

type walker struct {
	opt       Options
	res       Result
	protected bool
	seen      map[[2]uint64]bool
}

func (w *walker) readProtectedHardlinks() bool {
	if w.opt.hooks.protectedHardlinks != nil {
		return w.opt.hooks.protectedHardlinks()
	}
	b, err := os.ReadFile("/proc/sys/fs/protected_hardlinks")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

func (w *walker) event(what, path string) {
	if w.opt.hooks.event != nil {
		w.opt.hooks.event(what, path)
	}
}

func (w *walker) fail(path string, err error) {
	w.res.ErrorCount++
	if len(w.res.Errors) < maxErrors {
		w.res.Errors = append(w.res.Errors, path+": "+err.Error())
	}
}

// trustedUID: root and the user the walk runs as, who could change anything the walk changes itself.
func (w *walker) trustedUID(uid uint32) bool {
	return uid == 0 || uid == uint32(os.Geteuid()) || (w.opt.hooks.trusted != nil && uid == *w.opt.hooks.trusted)
}

func (w *walker) trustedGID(gid uint32) bool {
	return gid == 0 || gid == uint32(os.Getegid())
}

// lookupSafe says whether the name of child in dir can only have been put there by a trusted user:
// dir belongs to one, and nobody else may write to it - except in a sticky directory, where only
// the owner of an entry may rename or remove it, so a child of a trusted user stays put.
func (w *walker) lookupSafe(dir, child Stat) bool {
	if !w.trustedUID(dir.UID) {
		return false
	}
	sticky := dir.Mode&unix.S_ISVTX != 0 && w.trustedUID(child.UID)
	if dir.Mode&0o002 != 0 && !sticky {
		return false
	}
	if dir.Mode&0o020 != 0 && !w.trustedGID(dir.GID) && !sticky {
		return false
	}
	return true
}

// top resolves one top directory and walks it.
func (w *walker) top(path string) {
	if w.excluded(path) {
		w.res.Excluded++
		return
	}
	fd, st, why, skip := w.openTop(path)
	switch {
	case skip:
		w.res.Skipped = append(w.res.Skipped, path+": "+why)
		return
	case why != "":
		w.res.Refused = append(w.res.Refused, path+": "+why)
		return
	}
	key := [2]uint64{st.Dev, st.Ino}
	if w.seen[key] {
		unix.Close(fd) // reached before, below another top directory
		return
	}
	w.seen[key] = true
	w.res.Checked++
	w.fix(fd, st, path)
	dfd, err := w.openDirBelow(fd, st)
	unix.Close(fd)
	if err != nil {
		w.fail(path, err)
		return
	}
	w.walk(dfd, path, 1, st)
	unix.Close(dfd)
}

// dirRef is a directory the resolution of a top directory went through.
type dirRef struct {
	fd   int
	st   Stat
	path string
}

// openTop resolves path from "/" without following a link that an untrusted user could have put
// there. It answers an O_PATH descriptor of the directory, or why not: skip marks a top directory
// that is missing or is itself a link, which is no problem.
//
// The directories on the way are kept open, "/" first. A ".." in a trusted link's target
// (/etc/config -> ../usr/local/etc/config on the image) goes back to the one before - a directory
// that was checked on the way down - and never asks the kernel what ".." is.
func (w *walker) openTop(path string) (fd int, st Stat, why string, skip bool) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return -1, Stat{}, "not a clean absolute path below /", false
	}
	var stack []dirRef
	closeAll := func() {
		for _, d := range stack {
			unix.Close(d.fd)
		}
		stack = nil
	}
	pushRoot := func() string {
		rfd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return "/: " + err.Error()
		}
		rst, err := statFD(rfd)
		if err != nil {
			unix.Close(rfd)
			return "/: " + err.Error()
		}
		stack = append(stack, dirRef{fd: rfd, st: rst, path: "/"})
		return ""
	}
	if why := pushRoot(); why != "" {
		return -1, Stat{}, why, false
	}
	pending := strings.Split(strings.TrimPrefix(path, "/"), "/")
	hops := 0
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		last := len(pending) == 0
		cur := stack[len(stack)-1]
		switch name {
		case "", ".":
			// only a link's target has these, and a link is never the last component
			continue
		case "..":
			if len(stack) > 1 {
				unix.Close(cur.fd)
				stack = stack[:len(stack)-1]
			}
			continue
		}
		here := filepath.Join(cur.path, name)
		child, err := unix.Openat(cur.fd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			closeAll()
			if errors.Is(err, unix.ENOENT) {
				return -1, Stat{}, "does not exist", true
			}
			return -1, Stat{}, here + ": " + err.Error(), false
		}
		cst, err := statFD(child)
		if err != nil {
			unix.Close(child)
			closeAll()
			return -1, Stat{}, here + ": " + err.Error(), false
		}
		if !w.lookupSafe(cur.st, cst) {
			unix.Close(child)
			closeAll()
			return -1, Stat{}, fmt.Sprintf("%s can be changed by a user other than root (owner %d, mode %o)", cur.path, cur.st.UID, cur.st.Mode&0o7777), false
		}
		switch cst.typ() {
		case unix.S_IFLNK:
			if last {
				unix.Close(child)
				closeAll()
				return -1, Stat{}, "a symbolic link, not followed", true
			}
			if !w.trustedUID(cst.UID) {
				unix.Close(child)
				closeAll()
				return -1, Stat{}, fmt.Sprintf("%s is a link of user %d", here, cst.UID), false
			}
			target, err := readlinkFD(child)
			unix.Close(child)
			hops++
			if err != nil || target == "" || hops > maxLinkHops {
				closeAll()
				return -1, Stat{}, here + ": a link that cannot be resolved", false
			}
			if strings.HasPrefix(target, "/") {
				closeAll()
				if why := pushRoot(); why != "" {
					return -1, Stat{}, why, false
				}
			}
			pending = append(strings.Split(target, "/"), pending...)
		case unix.S_IFDIR:
			if last {
				closeAll()
				return child, cst, "", false
			}
			stack = append(stack, dirRef{fd: child, st: cst, path: here})
		default:
			unix.Close(child)
			closeAll()
			return -1, Stat{}, here + " is not a directory", false
		}
	}
	// unreachable: the last component of a clean path is a name, and it returns above
	closeAll()
	return -1, Stat{}, "resolves to no directory", false
}

// openDirBelow opens the directory an O_PATH descriptor names for reading, through "." below that
// very descriptor, and makes sure it is the same inode.
func (w *walker) openDirBelow(fd int, st Stat) (int, error) {
	dfd, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	dst, err := statFD(dfd)
	if err != nil {
		unix.Close(dfd)
		return -1, err
	}
	if dst.Dev != st.Dev || dst.Ino != st.Ino {
		unix.Close(dfd)
		return -1, errors.New("the directory changed while it was opened")
	}
	return dfd, nil
}

// sameMount: st is on the file system (and the mount) of the top directory.
func sameMount(top, st Stat) bool {
	if st.Dev != top.Dev {
		return false
	}
	return !top.HasMntID || !st.HasMntID || st.MntID == top.MntID
}

// walk reads the directory dfd and handles every entry in it; path is only for messages.
func (w *walker) walk(dfd int, path string, depth int, top Stat) {
	entries, err := readEntries(dfd)
	if err != nil {
		w.fail(path, err)
		// what was read is still handled
	}
	w.event("listed", path)
	for _, name := range entries {
		if w.res.Checked >= w.opt.MaxEntries {
			w.res.Incomplete = true
			return
		}
		// the name is the addon user's: a newline in it must not become a line of the journal
		p := path + "/" + printable(name)
		w.event("open", p)
		fd, err := unix.Openat(dfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if !errors.Is(err, unix.ENOENT) { // gone since it was listed: nothing to do
				w.fail(p, err)
			}
			continue
		}
		w.entry(fd, p, depth, top)
		if w.res.Incomplete {
			return
		}
	}
}

// printable escapes control characters.
func printable(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			fmt.Fprintf(&b, `\x%02x`, c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// entry handles one opened entry and closes fd, an O_PATH descriptor, before it goes deeper: every
// level of the walk holds one descriptor.
func (w *walker) entry(fd int, path string, depth int, top Stat) {
	closed := false
	defer func() {
		if !closed {
			unix.Close(fd)
		}
	}()
	if w.excluded(path) {
		w.res.Excluded++
		return
	}
	st, err := statFD(fd)
	if err != nil {
		w.fail(path, err)
		return
	}
	w.res.Checked++
	w.event("stat", path)
	if st.typ() == unix.S_IFLNK {
		w.res.Symlinks++
		if w.opt.rootBin {
			w.checkBinLink(fd, path)
		}
		return
	}
	if !sameMount(top, st) {
		w.res.Mounts++
		return
	}
	switch st.typ() {
	case unix.S_IFDIR:
		w.fix(fd, st, path)
		if depth >= w.opt.MaxDepth {
			w.res.TooDeep++
			return
		}
		key := [2]uint64{st.Dev, st.Ino}
		if w.seen[key] {
			return // a top directory walked before
		}
		w.seen[key] = true
		dfd, err := w.openDirBelow(fd, st)
		unix.Close(fd)
		closed = true
		if err != nil {
			w.fail(path, err)
			return
		}
		w.walk(dfd, path, depth+1, top)
		unix.Close(dfd)
	case unix.S_IFCHR, unix.S_IFBLK:
		if w.opt.rootBin {
			w.fail(path, errors.New("a device node in the bin directory"))
			return
		}
		if w.wrong(st, path) {
			w.res.Devices++
		}
	default: // regular files, fifos, sockets
		if w.opt.rootBin && st.typ() != unix.S_IFREG {
			w.fail(path, errors.New("neither a file nor a directory in the bin directory"))
			return
		}
		if st.Nlink > 1 && !w.protected {
			if w.opt.rootBin {
				// without fs.protected_hardlinks the addon's user may have linked any file of
				// root's here; its mode is not changed, and the directory is not clean
				w.fail(path, errors.New("a file with more than one link, and fs.protected_hardlinks is off"))
				return
			}
			if w.wrong(st, path) {
				w.res.HardLinks++
			}
			return
		}
		w.fix(fd, st, path)
	}
}

// ownerWrong reports whether an entry's owner or group is not the wanted one, without counting it.
func (w *walker) ownerWrong(st Stat) bool {
	uid, gid := st.UID, st.GID
	if w.opt.hooks.owner != nil {
		uid, gid = w.opt.hooks.owner(st)
	}
	return uid != uint32(w.opt.UID) || gid != uint32(w.opt.GID)
}

// ownerUID is st's owner as the walk sees it (the fake layer in tests).
func (w *walker) ownerUID(st Stat) uint32 {
	if w.opt.hooks.owner != nil {
		uid, _ := w.opt.hooks.owner(st)
		return uid
	}
	return st.UID
}

// wrong counts an entry whose owner is not the wanted one: devices and hard links, which keep their
// owner in the walk, use its count and its return.
func (w *walker) wrong(st Stat, path string) bool {
	if !w.ownerWrong(st) {
		return false
	}
	w.res.Wrong++
	if w.res.FirstWrong == "" {
		w.res.FirstWrong = path
	}
	if w.ownerUID(st) == 0 {
		w.res.RootOwned++
		if w.res.FirstRootOwned == "" {
			w.res.FirstRootOwned = path
		}
	}
	return true
}

// tightenMask is the permission bits TightenModes takes off: group-write and every world bit but
// execute, so a directory ends at most 0751 (still traversable, so a web path through it works, but
// not listable) and a regular file at most 0640. Only bits are cleared; a tighter mode is left.
const tightenMask = 0o026

// modeWant is the wanted permission of a directory or regular file under TightenModes, and whether it
// differs from the current one. A regular file, or a directory, in a PublicDir (the addon's web tree)
// keeps its mode, and so does the directory that holds a PublicDir - the web server opens that
// directory to serve the tree below it, and opening a directory needs read, not only search. Nothing
// to do for another kind, or when TightenModes is off.
func (w *walker) modeWant(st Stat, path string) (os.FileMode, bool) {
	if w.opt.rootBin {
		cur := os.FileMode(st.Mode & 0o7777)
		want := os.FileMode(0o644)
		if st.typ() == unix.S_IFDIR || cur&0o111 != 0 {
			want = 0o755
		}
		return want, want != cur
	}
	if !w.opt.TightenModes {
		return 0, false
	}
	typ := st.typ()
	switch typ {
	case unix.S_IFDIR:
		if w.publicDir(path) {
			return 0, false
		}
	case unix.S_IFREG:
		if w.publicFile(path) {
			return 0, false
		}
	default:
		return 0, false
	}
	cur := os.FileMode(st.Mode & 0o7777)
	want := cur &^ tightenMask
	return want, want != cur
}

// publicFile reports whether a regular file at path lies in one of the addon's public directories
// (its www): served to the browser, so it stays world-readable.
func (w *walker) publicFile(path string) bool {
	clean := filepath.Clean(path)
	for _, d := range w.opt.PublicDirs {
		d = filepath.Clean(d)
		if clean == d || strings.HasPrefix(clean, d+"/") {
			return true
		}
	}
	return false
}

// publicDir reports whether a directory at path is a public one, is inside one, or holds one: the www
// tree stays world-readable, and so does the directory the web server opens to reach it (the addon's
// own directory, which holds www). Everything else is closed.
func (w *walker) publicDir(path string) bool {
	if w.publicFile(path) {
		return true
	}
	clean := filepath.Clean(path)
	for _, d := range w.opt.PublicDirs {
		if clean == filepath.Dir(filepath.Clean(d)) {
			return true
		}
	}
	return false
}

// fix gives the entry behind fd to the wanted owner, and under TightenModes takes the world and
// group-write bits off a directory or a non-public regular file. An entry that needs either is
// counted wrong once; a dry run counts and changes nothing.
func (w *walker) fix(fd int, st Stat, path string) {
	ownerBad := w.ownerWrong(st)
	modeWant, modeBad := w.modeWant(st, path)
	if ownerBad || modeBad {
		w.res.Wrong++
		if w.res.FirstWrong == "" {
			w.res.FirstWrong = path
		}
		if ownerBad && w.ownerUID(st) == 0 {
			w.res.RootOwned++
			if w.res.FirstRootOwned == "" {
				w.res.FirstRootOwned = path
			}
		}
	}
	if w.opt.rootBin && ownerBad && !w.opt.Adopt {
		w.fail(path, fmt.Errorf("owned by %d, not root: made after the install", w.ownerUID(st)))
		return
	}
	if w.opt.DryRun {
		return
	}
	if ownerBad {
		chown := w.opt.hooks.chown
		if chown == nil {
			chown = fchownFD
		}
		if err := chown(fd, w.opt.UID, w.opt.GID); err != nil {
			w.fail(path, err)
			return
		}
		w.res.Fixed++
	}
	if modeBad {
		chmod := w.opt.hooks.chmod
		if chmod == nil {
			chmod = fchmodFD
		}
		if err := chmod(fd, uint32(modeWant)); err != nil {
			w.fail(path, err)
			return
		}
		w.res.ModeTightened++
	}
}

// fchownFD changes the owner of exactly the inode fd refers to: an empty path relative to the
// descriptor, and no link followed even if the descriptor were one.
func fchownFD(fd, uid, gid int) error {
	return unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
}

// fchmodFD sets the mode of exactly the inode an O_PATH descriptor refers to. fchmod and fchmodat
// with AT_EMPTY_PATH do not work on an O_PATH descriptor, and the descriptor was opened O_NOFOLLOW on
// an inode the walk already checked, so the magic /proc/self/fd link is safe: it names that very
// inode, and a directory or a regular file (the only kinds fix chmods) is not a link.
func fchmodFD(fd int, mode uint32) error {
	return os.Chmod("/proc/self/fd/"+strconv.Itoa(fd), os.FileMode(mode))
}

const statxMask = unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_NLINK | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO | unix.STATX_MNT_ID

// statFD reads the inode behind fd: statx where the kernel has it, fstat otherwise.
func statFD(fd int) (Stat, error) {
	var sx unix.Statx_t
	err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, statxMask, &sx)
	if err == nil {
		const need = unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_INO | unix.STATX_NLINK
		if sx.Mask&need != need {
			return Stat{}, errors.New("statx did not report the owner")
		}
		return Stat{
			Mode: uint32(sx.Mode), UID: sx.Uid, GID: sx.Gid, Nlink: uint64(sx.Nlink),
			Dev: unix.Mkdev(sx.Dev_major, sx.Dev_minor), Ino: sx.Ino,
			MntID: sx.Mnt_id, HasMntID: sx.Mask&unix.STATX_MNT_ID != 0,
		}, nil
	}
	if !errors.Is(err, unix.ENOSYS) {
		return Stat{}, err
	}
	var s unix.Stat_t
	if err := unix.Fstat(fd, &s); err != nil {
		return Stat{}, err
	}
	return Stat{Mode: s.Mode, UID: s.Uid, GID: s.Gid, Nlink: uint64(s.Nlink), Dev: uint64(s.Dev), Ino: s.Ino}, nil
}

// readlinkFD reads the link an O_PATH|O_NOFOLLOW descriptor refers to.
func readlinkFD(fd int) (string, error) {
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(fd, "", buf)
	if err != nil {
		return "", err
	}
	if n >= len(buf) {
		return "", errors.New("link too long")
	}
	return string(buf[:n]), nil
}

// readEntries lists a directory through its descriptor ("." and ".." left out): getdents64, whose
// records are struct linux_dirent64 { u64 d_ino; s64 d_off; u16 d_reclen; u8 d_type; char d_name[]; }.
func readEntries(fd int) ([]string, error) {
	buf := make([]byte, 32*1024)
	var out []string
	for {
		n, err := unix.Getdents(fd, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return out, err
		}
		if n <= 0 {
			return out, nil
		}
		for off := 0; off < n; {
			if off+19 > n {
				return out, errors.New("a short directory record")
			}
			ino := binary.NativeEndian.Uint64(buf[off:])
			reclen := int(binary.NativeEndian.Uint16(buf[off+16:]))
			if reclen < 19 || off+reclen > n {
				return out, errors.New("a bad directory record")
			}
			name := buf[off+19 : off+reclen]
			if i := bytes.IndexByte(name, 0); i >= 0 {
				name = name[:i]
			}
			off += reclen
			if ino == 0 || string(name) == "." || string(name) == ".." {
				continue
			}
			out = append(out, string(name))
		}
	}
}

// excluded reports whether path is one of Options.Exclude or below one.
func (w *walker) excluded(path string) bool {
	for _, d := range w.opt.Exclude {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
}

// checkBinLink is RootBin's rule for a link in the bin directory: its target is relative and has no
// ".." component, so it names an entry below the link's own directory - root's like everything the
// walk leaves there, or another link held to the same rule. Anything else may lead to a file the
// addon's user writes, and is an error: the directory is not clean.
func (w *walker) checkBinLink(fd int, path string) {
	target, err := readlinkFD(fd)
	if err != nil {
		w.fail(path, err)
		return
	}
	if target == "" || strings.HasPrefix(target, "/") || slices.Contains(strings.Split(target, "/"), "..") {
		w.fail(path, fmt.Errorf("a link to %q, which may lead out of the bin directory", printable(target)))
	}
}

// RootBin gives an addon's runtime.bin to root (occulited task 136): home is the addon's directory
// (/usr/local/addons/<id>, resolved like any top directory: every directory above it root's), bin
// the relative path below it. Each component of bin is opened with O_NOFOLLOW from home's
// descriptor - a link on the way is refused, as is anything on another file system - and the
// directory reached is walked with every rule of Own, giving every entry to 0:0 with an exact mode:
// directories 0755, regular files 0755 when they had any execute bit, else 0644 (no setuid, no group
// or world write). An entry of another owner is given to root only with Adopt (the install and an
// update); without it it is an error and left as it is. A device node, a fifo or a socket, a file with more than one link while
// fs.protected_hardlinks is off, and a link whose target is absolute or holds ".." are errors and
// left as they are. With DryRun it only counts: Wrong 0 and no Problem is a clean directory.
//
// The directories above bin inside home stay the addon user's, so the user can move bin aside (a
// rename in the same directory) and make a new one; the login shell's snippet therefore checks
// again that the directory is root's and not writable by others before it puts it on the PATH.
func RootBin(home, bin string, opt Options) Result {
	start := time.Now()
	opt.UID, opt.GID, opt.rootBin, opt.TightenModes, opt.PublicDirs, opt.Exclude = 0, 0, true, false, nil, nil
	w := &walker{opt: opt, seen: map[[2]uint64]bool{}}
	if w.opt.MaxEntries <= 0 {
		w.opt.MaxEntries = defaultMaxEntries
	}
	if w.opt.MaxDepth <= 0 {
		w.opt.MaxDepth = defaultMaxDepth
	}
	if w.opt.hooks == nil {
		w.opt.hooks = &hooks{}
	}
	w.protected = w.readProtectedHardlinks()
	defer func() { w.res.Duration = time.Since(start) }()
	path := filepath.Join(home, bin)
	if filepath.IsAbs(bin) || filepath.Clean(bin) != bin || bin == "." || strings.HasPrefix(bin, "..") || slices.Contains(strings.Split(bin, "/"), "..") {
		w.res.Refused = append(w.res.Refused, path+": not a path below the addon's directory")
		return w.res
	}
	hfd, hst, why, skip := w.openTop(home)
	if skip || why != "" {
		w.res.Refused = append(w.res.Refused, home+": "+why)
		return w.res
	}
	fd, st := hfd, hst
	here := home
	for _, name := range strings.Split(bin, "/") {
		here += "/" + name
		next, err := unix.Openat(fd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			w.res.Refused = append(w.res.Refused, here+": "+err.Error())
			return w.res
		}
		fd = next
		if st, err = statFD(fd); err != nil {
			unix.Close(fd)
			w.res.Refused = append(w.res.Refused, here+": "+err.Error())
			return w.res
		}
		if st.typ() != unix.S_IFDIR {
			unix.Close(fd)
			w.res.Refused = append(w.res.Refused, here+": not a directory (a link is not followed)")
			return w.res
		}
		if !sameMount(hst, st) {
			unix.Close(fd)
			w.res.Refused = append(w.res.Refused, here+": on another file system")
			return w.res
		}
	}
	w.seen[[2]uint64{st.Dev, st.Ino}] = true
	w.res.Checked++
	w.fix(fd, st, path)
	dfd, err := w.openDirBelow(fd, st)
	unix.Close(fd)
	if err != nil {
		w.fail(path, err)
		return w.res
	}
	w.walk(dfd, path, 1, hst)
	unix.Close(dfd)
	return w.res
}
