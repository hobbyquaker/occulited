package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// Legacy leftovers (roadmap task 21): what an update from OpenCCU leaves on the userfs that
// openccu-lite never reads - the ReGa database above all. Removing them frees space and ends the
// confusion of two sources of names. Since task 250 they go by themselves after the first start
// (RemoveLeftoversOnce); the Backup page's panel is gone.
//
// The way back to OpenCCU is a backup taken before the migration in every case (D-35, revised
// 2026-09-08) - openccu-lite does not claim you can flash OpenCCU over lite and find your box as
// you left it. Removing these makes that worse rather than newly true: without them you cannot
// even recover the ReGa database as it stood on the day of the switch. The page says so.

// LegacyItem is one removable leftover.
type LegacyItem struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Present bool   `json:"present"`
	Why     string `json:"why"`
	// WayBack: this is ReGa state. A backup from before the migration is the way back either
	// way; removing this takes away even the frozen copy the box still carries.
	WayBack bool `json:"way_back"`
	// Also are further paths that go with Path (an addon's config directory, web tree and rc.d
	// entry beside its directory): counted in Bytes, removed together, and Present when any of
	// them is - a half-removed addon is still a leftover.
	Also []string `json:"also,omitempty"`
	// Unit is the systemd unit to stop before the paths go (an addon that may still be running).
	Unit string `json:"-"`
	// RCEntry and WWWEntry name an addon among the paths by its rc.d id and its web entry: the rc.d
	// entry with its script and the web entry go first, through the helper's own operation
	// (openccu-lite B-293) - no generic one reaches rc.d or the web trees, nor a directory a web
	// link leads into while the link is there.
	RCEntry, WWWEntry string `json:"-"`
	// after runs once the paths are gone: what the leftover's own uninstall would do beyond them.
	after func(Root) error
}

var legacyItems = []LegacyItem{
	{ID: "regadom", Path: "/etc/config/homematic.regadom", Why: "the ReGa database: programs, system variables, and the names openccu-lite imported at its first boot", WayBack: true},
	{ID: "regadom-bak", Path: "/etc/config/homematic.regadom.bak", Why: "the ReGa's previous database", WayBack: true},
	{ID: "measurement", Path: "/etc/config/measurement", Why: "the WebUI's diagram data"},
	{ID: "userprofiles", Path: "/etc/config/userprofiles", Why: "WebUI user profiles (favourites, page settings)"},
	{ID: "rega-tmp", Path: "/etc/config/rega", Why: "ReGa working files"},
	// task 37: OpenCCU's NEO Server, which cannot work without the ReGa; the removal is what the
	// addon's own uninstall does - its four paths and the neoDisabled marker - plus the wrapper's
	// twin, its hm_addons.cfg entry and the watchdog line it left in root's crontab
	{ID: "neoserver", Path: NeoServerDir, Also: neoServerPaths[1:], Unit: "addon-" + NeoServerID + ".service", RCEntry: NeoServerID, WWWEntry: "mediola",
		Why:   "mediola's NEO Server, unpacked by OpenCCU: it posts to /tclrega.exe (the ReGa) and /api/homematic.cgi (the WebUI's CGI stack), neither of which openccu-lite has",
		after: Root.removeNeoServerRemains},
}

// LegacyLeftovers lists the items with their presence and size.
func (r Root) LegacyLeftovers() []LegacyItem {
	out := make([]LegacyItem, 0, len(legacyItems))
	for _, it := range legacyItems {
		for _, path := range append([]string{it.Path}, it.Also...) {
			p := r.join(path)
			st, err := os.Lstat(p)
			if err != nil {
				continue
			}
			it.Present = true
			if st.IsDir() {
				_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						if i, err := d.Info(); err == nil {
							it.Bytes += i.Size()
						}
					}
					return nil
				})
			} else {
				it.Bytes += st.Size()
			}
		}
		out = append(out, it)
	}
	return out
}

// RemoveLegacy deletes the named items (all present ones when ids is empty) through the
// privilege boundary. Unknown ids are an error before anything is touched.
func (r Root) RemoveLegacy(ids []string) ([]string, error) {
	return r.RemoveLegacyWith(ids, nil, nil)
}

// RemoveLegacyWith is RemoveLegacy with the box's hooks: stop is called with an item's unit
// before its paths go, reload once at the end when an item had a unit (the generator's unit
// disappears with the rc.d entry only at the next daemon-reload). Either may be nil.
func (r Root) RemoveLegacyWith(ids []string, stop func(unit string), reload func()) ([]string, error) {
	known := map[string]LegacyItem{}
	for _, it := range legacyItems {
		known[it.ID] = it
	}
	if len(ids) == 0 {
		for _, it := range r.LegacyLeftovers() {
			if it.Present {
				ids = append(ids, it.ID)
			}
		}
	}
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			return nil, fmt.Errorf("unknown item %q", id)
		}
	}
	sort.Strings(ids)
	var removed []string
	stopped := false
	for _, id := range ids {
		it := known[id]
		paths := append([]string{it.Path}, it.Also...)
		present := false
		for _, path := range paths {
			if _, err := os.Lstat(r.join(path)); !errors.Is(err, os.ErrNotExist) {
				present = true
			}
		}
		if !present {
			continue
		}
		if it.Unit != "" && stop != nil {
			stop(it.Unit)
			stopped = true
		}
		named := map[string]bool{}
		if it.RCEntry != "" || it.WWWEntry != "" {
			var rcd, www string
			if it.RCEntry != "" {
				rcd = "/usr/local/etc/config/rc.d/" + it.RCEntry
				named[rcd], named[rcd+".script"] = true, true
				rcd = r.join(rcd)
			}
			if it.WWWEntry != "" {
				www = AddonWWW + "/" + it.WWWEntry
				named[www] = true
				www = r.join(www)
			}
			if _, err := Priv.RemoveAddonEntry(rcd, www, true); err != nil {
				return removed, fmt.Errorf("%s: %w", id, err)
			}
		}
		for _, path := range paths {
			p := r.join(path)
			if named[path] {
				continue
			}
			if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
				continue
			}
			// RemoveAll on a symlink removes the link, never what it points to. An addon's
			// directory is no generic write's (openccu-lite B-294): the NEO Server's goes through
			// the helper's own operation, after its rc.d and web entries above
			del := Priv.RemoveAll
			if path == NeoServerDir || path == priv.NeoServerConfig {
				del = Priv.RemoveNeoServerHome
			}
			if err := del(p); err != nil {
				return removed, fmt.Errorf("%s: %w", path, err)
			}
		}
		if it.after != nil {
			if err := it.after(r); err != nil {
				return removed, fmt.Errorf("%s: %w", id, err)
			}
		}
		removed = append(removed, id)
	}
	if stopped && reload != nil {
		reload()
	}
	return removed, nil
}

// Task 250 (the maintainer: "remove the 'leftovers from ccu' panel from system-backup. instead just
// kill these files after first boot of openccu-lite, there is no way back except they have a backup
// from (open)ccu before migration to lite"): the leftovers go once, by themselves, after the first
// start on openccu-lite has done what it needs of them - the names imported from the ReGa database
// (or that import settled: given up, or not needed). The radio identity, the keys and the interface
// configuration are not among them and are not touched. The way back is the (Open)CCU's own backup
// from before the switch.

// LeftoversMarker is the file (in the state directory) that says the removal ran.
const LeftoversMarker = "ccu-leftovers-removed.json"

// LeftoverRun is what one run did, as the marker keeps it.
type LeftoverRun struct {
	At      string   `json:"at"`
	Removed []string `json:"removed"`
	Paths   []string `json:"paths,omitempty"`
	Freed   int64    `json:"freed_bytes"`
	// Hardened lists the world-writable directories under /usr/local/etc/config whose mode was
	// fixed at first boot, and the removed empty addons/mh leftover (B-257). Markers written
	// between B-257 and B-264 carry it; since B-264 the hardening keeps a marker of its own
	// (HardenConfigDirsOnce) and this field stays empty.
	Hardened []string `json:"hardened,omitempty"`
}

// HardenMarker is the file (in the state directory) that says the config-dir hardening ran
// (openccu-lite B-264).
const HardenMarker = "harden-config-dirs.done"

// HardenVersion is the hardening's current pass (openccu-lite task 312). A marker with a lower
// version - 0 for one written before the field existed - runs the hardening once more: what a new
// pass adds reaches the systems the earlier one already ran on. Raise it when the pass grows.
//   - 1: B-257/B-264, world-writable directories under config, the empty addons/mh removed;
//   - 2: task 312, the files under the CCU's addons/mh lose their world-writable bit as well.
const HardenVersion = 2

// HardenRun is what the hardening did, as its marker keeps it.
type HardenRun struct {
	At       string   `json:"at"`
	Version  int      `json:"version,omitempty"`
	Hardened []string `json:"hardened"`
}

// HardenConfigDirsOnce runs hardenConfigDirs unless its own marker says the current pass
// (HardenVersion) ran (openccu-lite B-264, task 312). B-257 put the hardening into the leftovers pass, whose marker every system that ran an
// image with task 250 already had (dev.24 to dev.28, the first public release among them): there
// the pass never ran again and addons/mh stayed 0777. A marker of its own makes it run once on every
// system, those included, whatever the leftovers pass did. Not while the ReGa runs (the CCU's own
// processes may still need the directories as they are). ran is false when the marker was there.
func (r Root) HardenConfigDirsOnce(stateDir string, now time.Time) (run HardenRun, ran bool, err error) {
	marker := filepath.Join(stateDir, HardenMarker)
	if b, err := os.ReadFile(marker); err == nil {
		// an unreadable marker counts as an old one: the pass only ever takes bits away
		var prev HardenRun
		if json.Unmarshal(b, &prev) == nil && prev.Version >= HardenVersion {
			return HardenRun{}, false, nil
		}
	}
	if r.HasReGa() {
		return HardenRun{}, false, fmt.Errorf("%w: the ReGa runs on this system", ErrMigrationIncomplete)
	}
	run = HardenRun{At: now.UTC().Format(time.RFC3339), Version: HardenVersion, Hardened: r.hardenConfigDirs()}
	if run.Hardened == nil {
		run.Hardened = []string{}
	}
	b, _ := json.Marshal(run)
	if werr := os.WriteFile(marker, append(b, '\n'), 0o600); werr != nil {
		return run, true, fmt.Errorf("the marker: %w", werr)
	}
	return run, true, nil
}

// hardenConfigDirs (B-257) is the first-boot cleanup of the CCU's world-writable leftover
// directories under /usr/local/etc/config. The base firmware creates /usr/local/etc/config/addons/mh
// (the WebUI's mediola/cloudmatic directory) mode 0777 so the ReGa's processes could write it; on
// lite nothing reads it, so it is the one directory under config any local user may write - a place
// to drop a file, and a hazard if a later feature ever walks addons/* as root. This removes it when
// it is empty, and takes the world-writable bit off every directory under config otherwise (a sweep
// for any 0777 directory, not mh alone). Inside addons/mh the files lose the bit as well (task 312:
// the CCU leaves its CloudMatic scripts there 0777, root's, and any local user could rewrite them);
// nothing in it is removed but the empty directory, so what a user may still want stays. Files
// elsewhere under config keep their modes: they belong to the daemons and the addons. It runs as
// occulited (occulite) and fixes the mode through the privilege helper, which takes bits away there
// and never adds one (B-295); a directory occulite cannot enter (a daemon's 0700 tree) is not
// world-writable and is skipped. Returns the paths (relative to config) it changed.
func (r Root) hardenConfigDirs() []string {
	base := r.join("/usr/local/etc/config")
	var fixed []string
	// the empty CCU leftover goes; a non-empty one only loses its world-writable bit below, so no
	// file a user may still want is deleted behind their back
	mh := filepath.Join(base, "addons", "mh")
	if fi, err := os.Lstat(mh); err == nil && fi.IsDir() {
		if ents, _ := os.ReadDir(mh); len(ents) == 0 {
			// an addon config directory nobody is confined to is no generic write's (openccu-lite
			// B-295): the helper's own operation removes it, and only empty
			if err := Priv.RemoveAddonHome(mh); err == nil {
				fixed = append(fixed, "addons/mh (removed, empty)")
				slog.Info("ccu leftovers: removed the empty world-writable directory", "path", "addons/mh")
			} else {
				slog.Warn("ccu leftovers: could not remove addons/mh", "err", err)
			}
		}
	}
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // a directory occulite cannot enter is not world-writable; skip it
		}
		// a regular file under addons/mh: a link is never followed, and its own mode means nothing
		if !d.IsDir() && (!d.Type().IsRegular() || !strings.HasPrefix(p, mh+string(filepath.Separator))) {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.Mode().Perm()&0o002 == 0 {
			return nil
		}
		mode := fi.Mode().Perm() &^ 0o002 // take the world-writable bit off, keep the rest
		if err := Priv.Chmod(p, mode); err != nil {
			slog.Warn("ccu leftovers: could not take the world-writable bit off", "path", p, "err", err)
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, base), "/")
		fixed = append(fixed, fmt.Sprintf("%s (o-w, now %04o)", rel, mode))
		// a directory a line of its own; mh's files (some 70 on a switched CCU) only at debug: the
		// caller's summary names them all
		lvl := slog.LevelInfo
		if !d.IsDir() {
			lvl = slog.LevelDebug
		}
		slog.Log(context.Background(), lvl, "ccu leftovers: took the world-writable bit off", "path", rel, "mode", fmt.Sprintf("%04o", mode))
		return nil
	})
	return fixed
}

// HardenedDir is one line of the hardening's summary (task 29): a top directory under config
// (addons/mh) and how many of its entries lost the world-writable bit.
type HardenedDir struct {
	Dir     string
	Entries int
}

// SummarizeHardened groups hardenConfigDirs' entries ("addons/mh/x (o-w, now 0775)") by their
// directory - two components under addons/, one elsewhere - in the order first seen.
func SummarizeHardened(entries []string) []HardenedDir {
	var out []HardenedDir
	idx := map[string]int{}
	for _, e := range entries {
		p, _, _ := strings.Cut(e, " (")
		parts := strings.Split(p, "/")
		n := 1
		if parts[0] == "addons" && len(parts) > 1 {
			n = 2
		}
		if len(parts) < n {
			n = len(parts)
		}
		dir := strings.Join(parts[:n], "/")
		if i, ok := idx[dir]; ok {
			out[i].Entries++
			continue
		}
		idx[dir] = len(out)
		out = append(out, HardenedDir{Dir: dir, Entries: 1})
	}
	return out
}

// ErrMigrationIncomplete: the first start's own migration has not finished; nothing is removed and
// the next start tries again.
var ErrMigrationIncomplete = errors.New("the switch from the CCU is not complete")

// RemoveLeftoversOnce removes the CCU's leftovers (legacyItems, nothing else) unless the marker
// says it ran. importSettled is the first-boot name import's state: false while the ReGa database
// still has to be read (ErrMigrationIncomplete then). A system with nothing left over records an
// empty run, so a later restore of a CCU backup is not swept behind the user's back. ran is false
// when the marker was there already.
func (r Root) RemoveLeftoversOnce(stateDir string, importSettled bool, now time.Time, stop func(unit string), reload func()) (run LeftoverRun, ran bool, err error) {
	marker := filepath.Join(stateDir, LeftoversMarker)
	if _, err := os.Stat(marker); err == nil {
		return LeftoverRun{}, false, nil
	}
	if r.HasReGa() {
		return LeftoverRun{}, false, fmt.Errorf("%w: the ReGa runs on this system", ErrMigrationIncomplete)
	}
	if !importSettled {
		return LeftoverRun{}, false, fmt.Errorf("%w: the names from the ReGa database are not imported yet", ErrMigrationIncomplete)
	}
	sizes := map[string]int64{}
	paths := map[string][]string{}
	for _, it := range r.LegacyLeftovers() {
		if it.Present {
			sizes[it.ID] = it.Bytes
			for _, p := range append([]string{it.Path}, it.Also...) {
				if _, err := os.Lstat(r.join(p)); err == nil {
					paths[it.ID] = append(paths[it.ID], p)
				}
			}
		}
	}
	removed, err := r.RemoveLegacyWith(nil, stop, reload)
	run = LeftoverRun{At: now.UTC().Format(time.RFC3339), Removed: removed}
	for _, id := range removed {
		run.Freed += sizes[id]
		run.Paths = append(run.Paths, paths[id]...)
	}
	if run.Removed == nil {
		run.Removed = []string{}
	}
	if err != nil {
		return run, true, err // no marker: what is left is tried again at the next start
	}
	b, _ := json.Marshal(run)
	if werr := os.WriteFile(marker, append(b, '\n'), 0o600); werr != nil {
		return run, true, fmt.Errorf("the marker: %w", werr)
	}
	return run, true, nil
}
