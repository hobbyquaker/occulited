package rpieeprom_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/rpieeprom"
	"github.com/hobbyquaker/occulited/internal/rpieeprom/eepromtest"
)

func TestParseRevision(t *testing.T) {
	for _, c := range []struct {
		code  string
		chip  string
		vl805 bool
		bad   bool
	}{
		{"c03111", rpieeprom.Chip2711, true, false},         // Pi 4 B 1.1
		{"b03114", rpieeprom.Chip2711, false, false},        // Pi 4 B 1.4
		{" 0x00c03115\n", rpieeprom.Chip2711, false, false}, // with 0x, as some tools print it
		{"c03130", rpieeprom.Chip2711, false, false},        // Pi 400
		{"b03140", rpieeprom.Chip2711, false, false},        // CM4
		{"d04170", rpieeprom.Chip2712, false, false},        // Pi 5
		{"c04190", rpieeprom.Chip2712, false, false},        // Pi 500
		{"a02082", "", false, false},                        // Pi 3 B
		{"a020d3", "", false, false},                        // Pi 3 B+
		{"0013", "", false, false},                          // an old-style code
		{"zz", "", false, true},
	} {
		b, err := rpieeprom.ParseRevision(c.code)
		if (err != nil) != c.bad {
			t.Errorf("%q: err %v", c.code, err)
			continue
		}
		if b.Chip != c.chip || b.VL805EEPROM != c.vl805 {
			t.Errorf("%q: %+v, want chip %q vl805 %v", c.code, b, c.chip, c.vl805)
		}
	}
}

func TestReadBoardAndCurrent(t *testing.T) {
	root := t.TempDir()
	eepromtest.Tree(t, root, eepromtest.Pi5, 1792057420, 0)
	b, err := rpieeprom.ReadBoard(root)
	if err != nil || b.Chip != rpieeprom.Chip2712 {
		t.Fatalf("board %+v %v", b, err)
	}
	if ts, err := rpieeprom.Current(root); err != nil || ts != 1792057420 {
		t.Errorf("current %d %v", ts, err)
	}
	// no device tree entry: /proc/cpuinfo's Revision line
	cpu := t.TempDir()
	eepromtest.Write(t, filepath.Join(cpu, rpieeprom.CPUInfo), []byte("processor\t: 0\nHardware\t: BCM2835\nRevision\t: c03111\nSerial\t\t: 1\n"))
	if b, err := rpieeprom.ReadBoard(cpu); err != nil || b.Chip != rpieeprom.Chip2711 || !b.VL805EEPROM {
		t.Errorf("cpuinfo board %+v %v", b, err)
	}
	// a VM: no revision anywhere
	vm := t.TempDir()
	eepromtest.Write(t, filepath.Join(vm, rpieeprom.CPUInfo), []byte("processor\t: 0\nvendor_id\t: GenuineIntel\n"))
	if _, err := rpieeprom.ReadBoard(vm); err == nil {
		t.Error("a VM has a board")
	}
	if _, err := rpieeprom.ReadBoard(t.TempDir()); err == nil {
		t.Error("nothing to read is a board")
	}
	if _, err := rpieeprom.Current(vm); err == nil {
		t.Error("a VM has a bootloader")
	}
	for _, raw := range [][]byte{{1, 2, 3}, {0, 0, 0, 0}} {
		eepromtest.Write(t, filepath.Join(vm, rpieeprom.BuildTimestampFile), raw)
		if _, err := rpieeprom.Current(vm); err == nil {
			t.Errorf("%v is a build time", raw)
		}
	}
}

func TestImageVersion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pieeprom.upd")
	eepromtest.Write(t, p, eepromtest.Image(1792057420))
	if ts, err := rpieeprom.ImageVersion(p); err != nil || ts != 1792057420 {
		t.Errorf("version %d %v", ts, err)
	}
	for name, data := range map[string][]byte{
		"none":  make([]byte, 100),
		"empty": []byte("BUILD_TIMESTAMP=\x00"),
		"zero":  []byte("BUILD_TIMESTAMP=0"),
	} {
		eepromtest.Write(t, p, data)
		if _, err := rpieeprom.ImageVersion(p); err == nil {
			t.Errorf("%s: a version", name)
		}
	}
	// too big, a link, missing
	eepromtest.Write(t, p, make([]byte, rpieeprom.MaxImage+1))
	if _, err := rpieeprom.ImageVersion(p); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too big: %v", err)
	}
	eepromtest.Write(t, filepath.Join(dir, "real"), eepromtest.Image(5))
	l := filepath.Join(dir, "link")
	if err := os.Symlink("real", l); err != nil {
		t.Fatal(err)
	}
	if _, err := rpieeprom.ImageVersion(l); err == nil {
		t.Error("a link is read")
	}
	if _, err := rpieeprom.ImageVersion(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("missing: %v", err)
	}
}

func TestCheckSig(t *testing.T) {
	data := []byte("image")
	if err := rpieeprom.CheckSig(data, eepromtest.Sig(data)); err != nil {
		t.Error(err)
	}
	if err := rpieeprom.CheckSig(data, []byte(strings.ToUpper(string(eepromtest.Sig(data)[:64])))); err != nil {
		t.Errorf("upper case: %v", err)
	}
	if err := rpieeprom.CheckSig([]byte("other"), eepromtest.Sig(data)); err == nil {
		t.Error("another file matches")
	}
	if err := rpieeprom.CheckSig(data, []byte("ts: 1\n")); err == nil {
		t.Error("no hash matches")
	}
}

func TestExists(t *testing.T) {
	root := t.TempDir()
	eepromtest.Write(t, filepath.Join(root, "boot", "recovery.bin"), []byte("x"))
	if !rpieeprom.Exists(root, rpieeprom.Recovery) || rpieeprom.Exists(root, rpieeprom.Update) {
		t.Error("exists")
	}
}
