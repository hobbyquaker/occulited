package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/rpieeprom"
	"github.com/hobbyquaker/occulited/internal/rpieeprom/eepromtest"
)

// openccu-lite task 366: the view's states, the refusals before the helper is asked, and the clean
// at the start only where nothing waits for its reboot.
func TestBootloaderView(t *testing.T) {
	const cur = 1792057420
	for _, c := range []struct {
		name                    string
		rev                     string
		current, latest         int64
		available, update, newr bool
		reason                  string
	}{
		{"the same", eepromtest.Pi4Rev11, cur, cur, true, false, false, ""},
		{"newer", eepromtest.Pi4Rev15, cur, cur + 1, true, true, false, ""},
		{"older", eepromtest.Pi5, cur, cur - 1, true, false, true, ""},
		{"no image", eepromtest.Pi5, cur, 0, true, false, false, ""},
		{"no build time", eepromtest.Pi4Rev11, 0, cur, false, false, false, "unreadable"},
		{"a Pi 3", eepromtest.Pi3, 0, 0, false, false, false, "no-eeprom"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			eepromtest.Tree(t, root, c.rev, c.current, c.latest)
			v := (&Bootloader{Root: Root(root)}).View()
			if v.Available != c.available || v.Update != c.update || v.Newer != c.newr || v.Reason != c.reason {
				t.Errorf("%+v", v)
			}
		})
	}
	// an image file that cannot be read says so
	root := t.TempDir()
	eepromtest.Tree(t, root, eepromtest.Pi5, cur, cur)
	eepromtest.Write(t, filepath.Join(rpieeprom.ImageFiles(root, rpieeprom.Chip2712), rpieeprom.Update), []byte("garbage"))
	if v := (&Bootloader{Root: Root(root)}).View(); v.ImageError == "" || v.Latest != 0 {
		t.Errorf("a broken image: %+v", v)
	}
}

func TestBootloaderPrepareAndClean(t *testing.T) {
	const cur = 1792057420
	root := t.TempDir()
	eepromtest.Tree(t, root, eepromtest.Pi4Rev11, cur, cur)
	staged, cleaned := 0, 0
	b := &Bootloader{Root: Root(root),
		Stage: func(context.Context) error { staged++; return nil },
		Clean: func(context.Context) error { cleaned++; return nil }}
	if err := b.Prepare(context.Background(), false); !errors.Is(err, ErrBootloaderUpToDate) || staged != 0 {
		t.Errorf("up to date: %v, staged %d", err, staged)
	}
	if err := b.Prepare(context.Background(), true); err != nil || staged != 1 {
		t.Errorf("reinstall: %v, staged %d", err, staged)
	}
	// a flashed update: recovery.000 is there, the clean removes it (and says so)
	old0 := filepath.Join(root, "boot", rpieeprom.RecoveryOld)
	eepromtest.Write(t, old0, []byte("r"))
	b.Clean = func(context.Context) error { cleaned++; return os.Remove(old0) }
	b.CleanAtStart(context.Background())
	if cleaned != 1 || rpieeprom.Exists(root, rpieeprom.RecoveryOld) {
		t.Errorf("clean: %d", cleaned)
	}
	// recovery.bin in /boot: the flash is still to come, nothing is cleaned
	eepromtest.Write(t, filepath.Join(root, "boot", rpieeprom.Recovery), []byte("r"))
	b.CleanAtStart(context.Background())
	if cleaned != 1 || !b.View().Staged {
		t.Errorf("staged: cleaned %d, view %+v", cleaned, b.View())
	}
	// older: never, whatever reinstall says
	old := t.TempDir()
	eepromtest.Tree(t, old, eepromtest.Pi4Rev11, cur, cur-1)
	if err := (&Bootloader{Root: Root(old), Stage: b.Stage}).Prepare(context.Background(), true); !errors.Is(err, priv.ErrBootloaderOlder) {
		t.Errorf("older: %v", err)
	}
	none := t.TempDir()
	eepromtest.Tree(t, none, eepromtest.Pi4Rev11, cur, 0)
	if err := (&Bootloader{Root: Root(none)}).Prepare(context.Background(), true); !errors.Is(err, ErrBootloaderNoImage) {
		t.Errorf("no image: %v", err)
	}
	pi3 := t.TempDir()
	eepromtest.Tree(t, pi3, eepromtest.Pi3, 0, 0)
	if err := (&Bootloader{Root: Root(pi3)}).Prepare(context.Background(), true); !errors.Is(err, ErrBootloaderUnavailable) {
		t.Errorf("a Pi 3: %v", err)
	}
	(&Bootloader{Root: Root(pi3), Clean: func(context.Context) error { t.Error("cleaned a Pi 3"); return nil }}).CleanAtStart(context.Background())
	// a failing staging is the view's error
	fail := &Bootloader{Root: Root(root), Stage: func(context.Context) error { return errors.New("no space") }}
	if err := fail.Prepare(context.Background(), true); err == nil || fail.View().Error != "no space" {
		t.Errorf("failing: %v %+v", err, fail.View())
	}
	(&Bootloader{Root: Root(none), Clean: func(context.Context) error { return errors.New("busy") }}).CleanAtStart(context.Background())
}
