// Package addonimport puts the addons of an (Open)CCU backup onto this system (occulited task 142,
// D-125): the privilege helper's side of the import's "addons" group.
//
// The archive is untrusted. What comes out of it is exactly four shapes per addon id the caller
// named, and nothing else:
//
//	usr/local/addons/<id>/**                  the addon's directory
//	usr/local/etc/config/rc.d/<id>            its rc.d entry: a file, or a link into its directory
//	usr/local/etc/config/addons/www/<id>      its web entry: a tree, or a link into its directory
//	usr/local/etc/config/addons/<id>/**       its config directory
//
// Every tree is written through an os.Root, so a link the archive planted cannot lead a later
// member out of it. Regular files, directories and symbolic links only; setuid, setgid and sticky
// bits are dropped. An id that has any of the four here already is refused whole: the import adds
// addons, it never changes one. The rc.d entry always ends without its executable bits - the
// caller enables an addon once its own checks (ReGa, binaries) passed.
package addonimport

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/hobbyquaker/occulited/internal/addonunit"
)

// Dest is where the four shapes go: the directories that hold one entry per addon.
type Dest struct {
	Home   string // /usr/local/addons
	RCDir  string // /usr/local/etc/config/rc.d
	WWWDir string // /usr/local/etc/config/addons/www
	Config string // /usr/local/etc/config/addons
	// HomeRef is how an archive's links name Home: /usr/local/addons, whatever root Home is under
	HomeRef string
}

// DestUnder is the destination below a filesystem root.
func DestUnder(root string) Dest {
	return Dest{
		Home:    filepath.Join(root, "/usr/local/addons"),
		RCDir:   filepath.Join(root, "/usr/local/etc/config/rc.d"),
		WWWDir:  filepath.Join(root, "/usr/local/etc/config/addons/www"),
		Config:  filepath.Join(root, "/usr/local/etc/config/addons"),
		HomeRef: "/usr/local/addons",
	}
}

// Result is what happened to one id.
type Result struct {
	ID       string `json:"id"`
	Imported bool   `json:"imported"`
	// Error says why not: "exists" (something of that id is here already), "no-entry" (the backup
	// has no rc.d entry of that name), "entry" (its rc.d entry is neither a file nor a link into
	// the addon's directory), or the failure
	Error string `json:"error,omitempty"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	// WasEnabled: the rc.d script was executable in the backup
	WasEnabled bool `json:"was_enabled"`
	// Skipped counts the members of this addon that were not taken (a hard link, a device, a web
	// or config link that leads elsewhere)
	Skipped int `json:"skipped"`
}

// reserved are the names in the destination directories that are no addon's.
var reserved = map[string]bool{"www": true, "mediola": true, "97NeoServer": true}

// ValidID says whether id can be imported at all.
func ValidID(id string) bool {
	return addonunit.IDRe.MatchString(id) && !reserved[id] && !strings.HasSuffix(id, ".script") && id != "." && id != ".."
}

var (
	syscallENOTDIR error = syscall.ENOTDIR
	syscallELOOP   error = syscall.ELOOP
)

// maxRCScript bounds an rc.d script.
const maxRCScript = 1 << 20

const prefix = "usr/local/"

type target struct {
	kind string // home, rc, www, config
	id   string
	rel  string // below the kind's directory, starting with the id
}

// place says where an archive member belongs, ok false when nowhere.
func place(name string) (target, bool) {
	clean := path.Clean(name)
	if clean != strings.TrimSuffix(name, "/") || strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") {
		return target{}, false
	}
	rel, ok := strings.CutPrefix(clean, prefix)
	if !ok {
		return target{}, false
	}
	for _, k := range []struct{ kind, dir string }{
		{"home", "addons/"}, {"rc", "etc/config/rc.d/"}, {"www", "etc/config/addons/www/"}, {"config", "etc/config/addons/"},
	} {
		rest, ok := strings.CutPrefix(rel, k.dir)
		if !ok || rest == "" {
			continue
		}
		id, below, _ := strings.Cut(rest, "/")
		if k.kind == "rc" && below != "" {
			return target{}, false
		}
		if k.kind == "config" && id == "www" {
			return target{}, false
		}
		return target{kind: k.kind, id: id, rel: rest}, true
	}
	return target{}, false
}

// intoHome says whether a link target is the addon's own directory or lies below it.
func intoHome(link, homeRef, id string) (string, bool) {
	if !path.IsAbs(link) || path.Clean(link) != link {
		return "", false
	}
	home := path.Join(homeRef, id)
	if link == home {
		return id, true
	}
	if rest, ok := strings.CutPrefix(link, home+"/"); ok {
		return id + "/" + rest, true
	}
	return "", false
}

// Extract reads a backup (.sbk) from r and imports the addons ids. It answers one Result per id,
// in the order given. An error is a failure of the archive or the disk: everything this call wrote
// is then removed again.
func Extract(r io.Reader, ids []string, d Dest) ([]Result, error) {
	x := &extraction{d: d, res: map[string]*Result{}, want: map[string]bool{}, roots: map[string]*os.Root{}, entry: map[string]string{}, enabled: map[string]bool{}}
	for _, id := range ids {
		if _, dup := x.res[id]; dup {
			continue
		}
		x.order = append(x.order, id)
		res := &Result{ID: id}
		x.res[id] = res
		switch {
		case !ValidID(id):
			res.Error = "invalid"
		case exists(d, id):
			res.Error = "exists"
		default:
			x.want[id] = true
		}
	}
	if len(x.want) == 0 {
		return x.answer(), nil
	}
	for kind, dir := range map[string]string{"home": d.Home, "rc": d.RCDir, "www": d.WWWDir, "config": d.Config} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return x.answer(), err
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return x.answer(), err
		}
		defer root.Close()
		x.roots[kind] = root
	}
	err := walk(r, func(h *tar.Header, body io.Reader) error {
		t, ok := place(h.Name)
		if !ok || !x.want[t.id] {
			return nil
		}
		if err := x.member(h, body, t); skippable(err) {
			x.res[t.id].Skipped++
		} else if err != nil {
			return fmt.Errorf("%s: %w", h.Name, err)
		}
		return nil
	})
	if err != nil {
		for id := range x.want {
			x.remove(id)
			*x.res[id] = Result{ID: id, Error: "failed"}
		}
		return x.answer(), err
	}
	x.finish()
	return x.answer(), nil
}

type extraction struct {
	d     Dest
	order []string
	res   map[string]*Result
	want  map[string]bool
	roots map[string]*os.Root
	// entry is the rc.d entry of each id: "file", "refused", or a link's target below Home
	entry   map[string]string
	enabled map[string]bool
}

func (x *extraction) answer() []Result {
	out := make([]Result, 0, len(x.order))
	for _, id := range x.order {
		out = append(out, *x.res[id])
	}
	return out
}

// remove takes everything of id away again; all of it was made by this call (exists refused the
// id otherwise).
func (x *extraction) remove(id string) {
	for _, r := range x.roots {
		_ = r.RemoveAll(id)
	}
}

// member writes one archive member.
func (x *extraction) member(h *tar.Header, body io.Reader, t target) error {
	res := x.res[t.id]
	root := x.roots[t.kind]
	top := t.rel == t.id
	mode := os.FileMode(h.Mode) & 0o777
	parent := func() error {
		if dir := path.Dir(t.rel); dir != "." {
			return root.MkdirAll(dir, 0o755)
		}
		return nil
	}
	switch h.Typeflag {
	case tar.TypeDir:
		if t.kind == "rc" {
			res.Skipped++
			return nil
		}
		if err := root.MkdirAll(t.rel, 0o755); err != nil {
			return err
		}
		return root.Chmod(t.rel, mode|0o700)
	case tar.TypeReg:
		if top && t.kind != "rc" {
			res.Skipped++ // a file where an addon's directory belongs
			return nil
		}
		if t.kind == "rc" {
			if h.Size > maxRCScript {
				return fmt.Errorf("an rc.d script of %d bytes", h.Size)
			}
			x.entry[t.id], x.enabled[t.id] = "file", mode&0o111 != 0
			mode &^= 0o111
		}
		if err := parent(); err != nil {
			return err
		}
		f, err := root.OpenFile(t.rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if errors.Is(err, os.ErrExist) {
			res.Skipped++ // a second member of the same name
			return nil
		}
		if err != nil {
			return err
		}
		n, err := io.Copy(f, body)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if err := root.Chmod(t.rel, mode); err != nil { // the umask took bits
			return err
		}
		if t.kind == "home" {
			res.Files++
			res.Bytes += n
		}
		return nil
	case tar.TypeSymlink:
		if top || t.kind == "rc" {
			// an entry that is a link: only into the addon's own directory; the directory itself
			// is never a link
			below, ok := intoHome(h.Linkname, x.d.HomeRef, t.id)
			if !ok || t.kind == "home" {
				res.Skipped++
				if t.kind == "rc" {
					x.entry[t.id] = "refused"
				}
				return nil
			}
			if t.kind == "rc" {
				x.entry[t.id] = below
			}
		}
		if err := parent(); err != nil {
			return err
		}
		if err := root.Symlink(h.Linkname, t.rel); errors.Is(err, os.ErrExist) {
			res.Skipped++
		} else if err != nil {
			return err
		}
		return nil
	}
	res.Skipped++
	return nil
}

// finish settles each id by its rc.d entry and takes the executable bits off the script.
func (x *extraction) finish() {
	names := make([]string, 0, len(x.want))
	for id := range x.want {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		res := x.res[id]
		switch e := x.entry[id]; e {
		case "":
			res.Error = "no-entry"
		case "refused":
			res.Error = "entry"
		case "file":
			res.Imported, res.WasEnabled = true, x.enabled[id]
		default:
			// a link into the addon's directory: the script's bits are the target's. The stat and
			// the chmod go through the root, which follows no link out of the addons' tree.
			res.Imported = true
			if st, err := x.roots["home"].Stat(e); err == nil && st.Mode().IsRegular() {
				res.WasEnabled = st.Mode()&0o111 != 0
				if err := x.roots["home"].Chmod(e, st.Mode().Perm()&^0o111); err != nil {
					res.Imported, res.Error = false, err.Error()
				}
			} else if err == nil {
				res.Imported, res.Error = false, "entry" // the link leads to something that is no file
			}
		}
		if !res.Imported {
			x.remove(id)
			res.Files, res.Bytes = 0, 0
		}
	}
}

// skippable says whether a failed write is the archive's own shape and not the disk's: the path
// leads through a link out of the tree (os.Root refuses it), or through something that is no
// directory. Such a member is left out; the import goes on.
func skippable(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "path escapes") || errors.Is(err, syscallENOTDIR) || errors.Is(err, syscallELOOP))
}

// exists says whether anything of that id is on the system already.
func exists(d Dest, id string) bool {
	for _, p := range []string{filepath.Join(d.Home, id), filepath.Join(d.RCDir, id), filepath.Join(d.RCDir, id+".script"), filepath.Join(d.WWWDir, id), filepath.Join(d.Config, id)} {
		if _, err := os.Lstat(p); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

// ErrNotSBK says the stream is no CCU backup.
var ErrNotSBK = errors.New("not a CCU backup: no usr_local.tar.gz in the archive")

// walk hands every member of the backup's inner usr_local.tar.gz to fn.
func walk(r io.Reader, fn func(h *tar.Header, body io.Reader) error) error {
	outer := tar.NewReader(r)
	for {
		h, err := outer.Next()
		if err == io.EOF {
			return ErrNotSBK
		}
		if err != nil {
			return fmt.Errorf("the backup archive: %w", err)
		}
		if path.Base(h.Name) != "usr_local.tar.gz" {
			continue
		}
		gz, err := gzip.NewReader(outer)
		if err != nil {
			return fmt.Errorf("usr_local.tar.gz: %w", err)
		}
		inner := tar.NewReader(gz)
		for {
			ih, err := inner.Next()
			if err == io.EOF {
				// to the end of the gzip stream: its checksum says whether the archive is whole
				if _, err := io.Copy(io.Discard, gz); err != nil {
					return fmt.Errorf("usr_local.tar.gz: %w", err)
				}
				return nil
			}
			if err != nil {
				return fmt.Errorf("usr_local.tar.gz: %w", err)
			}
			if err := fn(ih, inner); err != nil {
				return err
			}
		}
	}
}
