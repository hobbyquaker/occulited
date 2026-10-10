package radio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// task 33: a migrated system's diagram data - the stick's measurement/ and the WebUI's copy in the
// config directory - is removed once with hmipserver.diagrams off, logged with path and size; the
// stick's other files stay; the marker waits for the stick's place to exist; with the setting on
// nothing is removed.
func TestRemoveMigratedDiagrams(t *testing.T) {
	root := t.TempDir()
	w := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	w("usr/local/etc/config/measurement/config.xml", "12345")
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, f) }
	d := Detector{Root: root}

	// no stick yet: the config directory's copy goes, no marker - the stick is looked for again
	removeMigratedDiagrams(d, logf)
	if exists(filepath.Join(root, "usr/local/etc/config/measurement")) || exists(filepath.Join(root, MeasurementRemovedFile)) || len(lines) != 1 {
		t.Fatalf("no stick: %v", lines)
	}
	// the stick (mounted later): its measurement/ goes, the backup and the journal beside it stay
	w("media/usb1/measurement/db/a.rrd", "0123456789")
	w("media/usb1/measurement/b.rrd", "01234")
	w("media/usb1/backup/x.sbk", "keep")
	_ = os.Symlink("usb1", filepath.Join(root, "media/usb0"))
	removeMigratedDiagrams(d, logf)
	if exists(filepath.Join(root, "media/usb1/measurement")) || !exists(filepath.Join(root, "media/usb1/backup/x.sbk")) {
		t.Fatal("the stick")
	}
	b, err := os.ReadFile(filepath.Join(root, MeasurementRemovedFile))
	var m struct {
		Removed []struct {
			Path  string
			Bytes int64
		}
	}
	if err != nil || json.Unmarshal(b, &m) != nil || len(m.Removed) != 1 || m.Removed[0].Path != "/media/usb0/measurement" || m.Removed[0].Bytes != 15 {
		t.Fatalf("marker %s %v", b, err)
	}
	if !strings.Contains(lines[len(lines)-1], "removed") {
		t.Fatalf("log %v", lines)
	}
	// once: a directory put back later stays
	w("media/usb1/measurement/c.rrd", "x")
	removeMigratedDiagrams(d, logf)
	if !exists(filepath.Join(root, "media/usb1/measurement/c.rrd")) {
		t.Fatal("removed twice")
	}
	// the setting on: nothing removed, even without a marker
	_ = os.Remove(filepath.Join(root, MeasurementRemovedFile))
	removeMigratedDiagrams(Detector{Root: root, Diagrams: true}, logf)
	if !exists(filepath.Join(root, "media/usb1/measurement/c.rrd")) {
		t.Fatal("removed with diagrams on")
	}
}

// task 33: the start copies the stick's diagram data in only with hmipserver.diagrams on.
func TestStorageCopiesDiagramsOnlyWhenOn(t *testing.T) {
	fakeUsers(t)
	for _, on := range []bool{false, true} {
		root := t.TempDir()
		_ = os.MkdirAll(filepath.Join(root, "media/usb0/measurement"), 0o755)
		rec := &recorder{}
		w := &writer{d: Detector{Root: root, Run: rec.run, Diagrams: on}, report: &RunReport{}}
		if err := w.storage(Plan{}, Inputs{}); err != nil {
			t.Fatal(err)
		}
		if rec.called("cp -a") != on {
			t.Errorf("diagrams %v: %v", on, rec.calls)
		}
	}
}
