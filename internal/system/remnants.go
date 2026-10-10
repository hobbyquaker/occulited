package system

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// occulited task 29 (maintainer, Q&A 2026-10-10): "remove known-useless CCU3 remnants
// automatically on migration" - once (RemnantsMarker), each removal logged with its path and size,
// an explicit list and never a guess, never anything an installed addon or occulited uses. The
// way back is the CCU backup made before the switch (docs/switching.md). The list:
//   - <config>/addons/<id> with no rc.d entry, no /usr/local/addons/<id>, and no rc.d script that
//     names it (an addon whose config directory carries another name than its id keeps it) - on
//     the maintainer's CCU meine-homematic.de's addons/mh, 71 files with its old VPN credentials;
//   - /usr/local/etc/config/homematic.regadom.err, the ReGa's crash dump (9.6 MB there);
//   - /usr/local/eQ-3-Backup when it holds no byte.
// The helper checks the same conditions itself (priv.RemoveCCURemnant). Other remnants seen there
// (etc/config/shadow, snmp/, log4j*.xml, test) stay: see the item.

// RemnantsMarker is the record of the one pass in occulited's state directory.
const RemnantsMarker = "ccu-remnants-removed.json"

// Remnant is one removal.
type Remnant struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
	Error string `json:"error,omitempty"`
}

// RemnantRun is the marker's content.
type RemnantRun struct {
	At      string    `json:"at"`
	Removed []Remnant `json:"removed"`
}

// CCURemnants lists what the pass would remove now, with sizes.
func (r Root) CCURemnants() []Remnant {
	var out []Remnant
	add := func(p string) {
		bytes, files := treeBytes(r.join(p))
		out = append(out, Remnant{Path: p, Bytes: bytes, Files: files})
	}
	cfg := "/usr/local/etc/config/addons"
	entries, _ := os.ReadDir(r.join(cfg))
	rcScripts := r.rcScriptTexts()
	for _, e := range entries {
		id := e.Name()
		if id == "www" || !addonIDRe.MatchString(id) {
			continue
		}
		if r.exists("/usr/local/etc/config/rc.d/"+id) || r.exists("/usr/local/etc/config/rc.d/"+id+".script") || r.exists("/usr/local/addons/"+id) {
			continue
		}
		if regexp.MustCompile(`config/addons/` + regexp.QuoteMeta(id) + `([^A-Za-z0-9_.-]|$)`).MatchString(rcScripts) {
			continue // an installed addon names it in its script
		}
		add(cfg + "/" + id)
	}
	if st, err := os.Lstat(r.join(priv.RemnantRegadomErr)); err == nil && st.Mode().IsRegular() {
		add(priv.RemnantRegadomErr)
	}
	if st, err := os.Lstat(r.join(priv.RemnantEQ3Backup)); err == nil && st.IsDir() {
		if bytes, _ := treeBytes(r.join(priv.RemnantEQ3Backup)); bytes == 0 && !hasLink(r.join(priv.RemnantEQ3Backup)) {
			add(priv.RemnantEQ3Backup)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// RemoveCCURemnantsOnce runs the pass unless the marker says it ran: after the first start's name
// import settled (importSettled), on a system without the ReGa. A failed removal writes no marker:
// the next start tries again.
func (r Root) RemoveCCURemnantsOnce(stateDir string, importSettled bool, now time.Time, logf func(msg string, args ...any)) (RemnantRun, bool, error) {
	marker := filepath.Join(stateDir, RemnantsMarker)
	if _, err := os.Stat(marker); err == nil {
		return RemnantRun{}, false, nil
	}
	if r.HasReGa() {
		return RemnantRun{}, false, fmt.Errorf("%w: the ReGa runs on this system", ErrMigrationIncomplete)
	}
	if !importSettled {
		return RemnantRun{}, false, fmt.Errorf("%w: the names from the ReGa database are not imported yet", ErrMigrationIncomplete)
	}
	run := RemnantRun{At: now.UTC().Format(time.RFC3339), Removed: []Remnant{}}
	failed := false
	for _, it := range r.CCURemnants() {
		if err := Priv.RemoveCCURemnant(r.join(it.Path)); err != nil {
			it.Error, failed = err.Error(), true
			logf("ccu remnants: could not remove", "path", it.Path, "bytes", it.Bytes, "err", err)
		} else {
			logf("ccu remnants: removed - the CCU backup from before the switch keeps it", "path", it.Path, "bytes", it.Bytes, "files", it.Files)
		}
		run.Removed = append(run.Removed, it)
	}
	if failed {
		return run, true, fmt.Errorf("some remnants could not be removed")
	}
	b, _ := json.Marshal(run)
	if err := os.WriteFile(marker, append(b, '\n'), 0o600); err != nil {
		return run, true, fmt.Errorf("the marker: %w", err)
	}
	return run, true, nil
}

func (r Root) exists(p string) bool {
	_, err := os.Lstat(r.join(p))
	return err == nil
}

// rcScriptTexts is every rc.d script's text, for "does an installed addon name this directory".
func (r Root) rcScriptTexts() string {
	var b strings.Builder
	entries, _ := os.ReadDir(r.join("/usr/local/etc/config/rc.d"))
	for _, e := range entries {
		b.WriteString(readFile(r.join("/usr/local/etc/config/rc.d/" + e.Name())))
		b.WriteString("\n")
	}
	return b.String()
}

// treeBytes is the bytes and the number of regular files under p (p itself when a file).
func treeBytes(p string) (int64, int) {
	var n int64
	files := 0
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, ierr := d.Info(); ierr == nil {
				n += fi.Size()
				files++
			}
		}
		return nil
	})
	return n, files
}

func hasLink(p string) bool {
	found := false
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type()&os.ModeSymlink != 0 {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
