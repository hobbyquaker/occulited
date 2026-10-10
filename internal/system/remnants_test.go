package system

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// task 29: the explicit list against a fixture userfs - an orphaned addon config directory, the
// ReGa crash dump, an empty eQ-3-Backup - removed once and logged with path and size; an installed
// addon's directory, one an rc.d script names, www, a non-empty eQ-3-Backup and the other CCU
// files stay.
func TestRemoveCCURemnantsOnce(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/etc/config/addons/mh/client.conf":       "remote vpn.example.org\n",
		"usr/local/etc/config/addons/mh/sub/addcron.sh":    "#!/bin/sh\n",
		"usr/local/etc/config/addons/hmm/hmm.env":          "x",
		"usr/local/addons/hmm/bin/node":                    "x",
		"usr/local/etc/config/rc.d/hmm":                    "#!/bin/sh\n",
		"usr/local/etc/config/addons/cuxd/cuxd.ini":        "x",
		"usr/local/etc/config/rc.d/cuxdaemon":              "#!/bin/sh\nCFG=/usr/local/etc/config/addons/cuxd\n",
		"usr/local/etc/config/addons/www/hmm/index.html":   "x",
		"usr/local/etc/config/homematic.regadom.err":       "0123456789",
		"usr/local/eQ-3-Backup/last":                       "",
		"usr/local/etc/config/shadow":                      "root:x\n",
		"usr/local/etc/config/addons/hdmi-wlan/config.txt": "x", // its rc.d entry is the .script twin
		"usr/local/etc/config/rc.d/hdmi-wlan.script":       "#!/bin/sh\n",
	})
	got := r.CCURemnants()
	var paths []string
	for _, it := range got {
		paths = append(paths, it.Path)
	}
	want := "/usr/local/eQ-3-Backup /usr/local/etc/config/addons/mh /usr/local/etc/config/homematic.regadom.err"
	if strings.Join(paths, " ") != want {
		t.Fatalf("remnants %v", paths)
	}
	state := t.TempDir()
	if _, ran, err := r.RemoveCCURemnantsOnce(state, false, time.Now(), func(string, ...any) {}); ran || err == nil {
		t.Fatal("ran before the import settled")
	}
	var lines []string
	logf := func(msg string, args ...any) { lines = append(lines, msg) }
	run, ran, err := r.RemoveCCURemnantsOnce(state, true, time.Now(), logf)
	if err != nil || !ran || len(run.Removed) != 3 || len(lines) != 3 {
		t.Fatalf("%v %v %+v %v", err, ran, run, lines)
	}
	for _, p := range strings.Split(want, " ") {
		if _, err := os.Lstat(r.join(p)); !os.IsNotExist(err) {
			t.Errorf("%s kept", p)
		}
	}
	for _, p := range []string{"/usr/local/etc/config/addons/hmm/hmm.env", "/usr/local/etc/config/addons/cuxd/cuxd.ini", "/usr/local/etc/config/addons/www/hmm/index.html", "/usr/local/etc/config/shadow", "/usr/local/etc/config/addons/hdmi-wlan/config.txt"} {
		if _, err := os.Stat(r.join(p)); err != nil {
			t.Errorf("%s removed", p)
		}
	}
	b, _ := os.ReadFile(filepath.Join(state, RemnantsMarker))
	var m RemnantRun
	if json.Unmarshal(b, &m) != nil || len(m.Removed) != 3 {
		t.Fatalf("marker %s", b)
	}
	for _, it := range m.Removed {
		if it.Path == "/usr/local/etc/config/homematic.regadom.err" && it.Bytes != 10 {
			t.Errorf("size %+v", it)
		}
		if it.Path == "/usr/local/etc/config/addons/mh" && it.Files != 2 {
			t.Errorf("files %+v", it)
		}
	}
	// once
	_ = os.MkdirAll(r.join("/usr/local/etc/config/addons/mh"), 0o755)
	if _, ran, _ := r.RemoveCCURemnantsOnce(state, true, time.Now(), logf); ran {
		t.Fatal("ran twice")
	}
	// a non-empty eQ-3-Backup stays
	r2 := rootWith(t, map[string]string{"usr/local/eQ-3-Backup/last/backup.sbk": "data"})
	if len(r2.CCURemnants()) != 0 {
		t.Fatal("a backup with data offered")
	}
}

func TestSummarizeHardened(t *testing.T) {
	got := SummarizeHardened([]string{"addons/mh (o-w, now 0775)", "addons/mh/a.sh (o-w, now 0775)", "addons/mh/sub/b (o-w, now 0664)", "nut (o-w, now 0755)"})
	if len(got) != 2 || got[0] != (HardenedDir{"addons/mh", 3}) || got[1] != (HardenedDir{"nut", 1}) {
		t.Fatalf("%+v", got)
	}
}
