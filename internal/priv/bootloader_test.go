package priv

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/rpieeprom"
	"github.com/hobbyquaker/occulited/internal/rpieeprom/eepromtest"
)

// eepromHelperTree points the helper's fixed places at a tree: the board, the running bootloader,
// the image's update, a /boot mounted read-only, and a remount that records what it was asked.
func eepromHelperTree(t *testing.T, revision string, current, latest int64) (root string, remounts *[]string) {
	t.Helper()
	root = t.TempDir()
	eepromtest.Tree(t, root, revision, current, latest)
	mounts := filepath.Join(root, "mounts")
	boot := filepath.Join(root, "boot")
	eepromtest.Write(t, mounts, []byte("/dev/root / ext4 ro,noatime 0 0\n/dev/mmcblk0p1 "+boot+" vfat ro,relatime,fmask=0022 0 0\n"))
	var calls []string
	oldRoot, oldMounts, oldRemount := eepromRoot, mountsFile, remount
	eepromRoot, mountsFile = root, mounts
	remount = func(_ context.Context, dir string, rw bool) error {
		if dir != boot {
			t.Errorf("remount of %s", dir)
		}
		if rw {
			calls = append(calls, "rw")
		} else {
			calls = append(calls, "ro")
		}
		return nil
	}
	t.Cleanup(func() { eepromRoot, mountsFile, remount = oldRoot, oldMounts, oldRemount })
	return root, &calls
}

func bootNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "boot"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// A Pi 4 1.1 gets the update, its .sig, the VL805's three files and recovery.bin, with /boot
// remounted rw and back to ro; a recovery.000 of an earlier flash goes.
func TestStageBootloaderPi4(t *testing.T) {
	root, calls := eepromHelperTree(t, eepromtest.Pi4Rev11, 1792057420, 1792057420)
	eepromtest.Write(t, filepath.Join(root, "boot", rpieeprom.RecoveryOld), []byte("old"))
	eepromtest.Write(t, filepath.Join(root, "boot", "config.txt"), []byte("keep"))
	if err := (Local{}).StageBootloader(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := bootNames(t, root)
	want := []string{"config.txt", "pieeprom.sig", "pieeprom.upd", "recovery.bin", "vl805.bin", "vl805.sig", "vl805.ver"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("/boot holds %v, want %v", got, want)
	}
	src, _ := os.ReadFile(filepath.Join(rpieeprom.ImageFiles(root, rpieeprom.Chip2711), rpieeprom.Update))
	if b, _ := os.ReadFile(filepath.Join(root, "boot", rpieeprom.Update)); string(b) != string(src) {
		t.Error("the update was not copied as it is")
	}
	if strings.Join(*calls, ",") != "rw,ro" {
		t.Errorf("remounts %v", *calls)
	}
}

// A Pi 4 1.5 and a Pi 5: no VL805 files (the Pi 5's image directory has none at all).
func TestStageBootloaderNoVL805(t *testing.T) {
	for _, rev := range []string{eepromtest.Pi4Rev15, eepromtest.Pi5} {
		root, _ := eepromHelperTree(t, rev, 100, 200)
		if err := (Local{}).StageBootloader(context.Background()); err != nil {
			t.Fatalf("%s: %v", rev, err)
		}
		if got := strings.Join(bootNames(t, root), ","); got != "pieeprom.sig,pieeprom.upd,recovery.bin" {
			t.Errorf("%s: /boot holds %s", rev, got)
		}
	}
}

// Refusals: nothing is staged, /boot is not even remounted.
func TestStageBootloaderRefusals(t *testing.T) {
	cases := map[string]func(t *testing.T, root string){
		"older":  func(*testing.T, string) {},
		"a Pi 3": func(*testing.T, string) {},
		"a broken signature": func(t *testing.T, root string) {
			eepromtest.Write(t, filepath.Join(rpieeprom.ImageFiles(root, rpieeprom.Chip2711), rpieeprom.UpdateSig), eepromtest.Sig([]byte("another")))
		},
		"a broken VL805 signature": func(t *testing.T, root string) {
			eepromtest.Write(t, filepath.Join(rpieeprom.ImageFiles(root, rpieeprom.Chip2711), rpieeprom.VL805Sig), eepromtest.Sig([]byte("another")))
		},
		"no recovery.bin": func(t *testing.T, root string) {
			_ = os.Remove(filepath.Join(rpieeprom.ImageFiles(root, rpieeprom.Chip2711), rpieeprom.Recovery))
		},
		"a link as recovery.bin": func(t *testing.T, root string) {
			p := filepath.Join(rpieeprom.ImageFiles(root, rpieeprom.Chip2711), rpieeprom.Recovery)
			_ = os.Remove(p)
			if err := os.Symlink("/etc/shadow", p); err != nil {
				t.Fatal(err)
			}
		},
		"no image": func(t *testing.T, root string) {
			_ = os.RemoveAll(rpieeprom.ImageFiles(root, rpieeprom.Chip2711))
		},
		"no running version": func(t *testing.T, root string) {
			_ = os.Remove(filepath.Join(root, rpieeprom.BuildTimestampFile))
		},
		"/boot not mounted": func(t *testing.T, root string) {
			eepromtest.Write(t, mountsFile, []byte("/dev/root / ext4 ro 0 0\n"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			rev, current := eepromtest.Pi4Rev11, int64(100)
			switch name {
			case "older":
				current = 300
			case "a Pi 3":
				rev = eepromtest.Pi3
			}
			root, calls := eepromHelperTree(t, rev, current, 200)
			setup(t, root)
			err := (Local{}).StageBootloader(context.Background())
			if err == nil {
				t.Fatal("staged")
			}
			if name == "older" && !errors.Is(err, ErrBootloaderOlder) {
				t.Errorf("older: %v", err)
			}
			if name == "a Pi 3" && !errors.Is(err, ErrNotAvailable) {
				t.Errorf("a Pi 3: %v", err)
			}
			if got := bootNames(t, root); len(got) != 0 {
				t.Errorf("/boot holds %v", got)
			}
			if len(*calls) != 0 {
				t.Errorf("remounted: %v", *calls)
			}
		})
	}
}

// A write that fails still puts /boot back to read-only; a remount that fails is the answer.
func TestStageBootloaderFailures(t *testing.T) {
	root, calls := eepromHelperTree(t, eepromtest.Pi5, 100, 200)
	// a directory where the update goes: the rename fails
	if err := os.MkdirAll(filepath.Join(root, "boot", rpieeprom.Update, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (Local{}).StageBootloader(context.Background()); err == nil {
		t.Error("a failed write is no error")
	}
	if strings.Join(*calls, ",") != "rw,ro" {
		t.Errorf("remounts %v", *calls)
	}
	if slices.Contains(bootNames(t, root), rpieeprom.Recovery) {
		t.Error("recovery.bin staged after a failure")
	}
	remount = func(context.Context, string, bool) error { return errors.New("busy") }
	if err := (Local{}).StageBootloader(context.Background()); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("a failed remount: %v", err)
	}
}

// A /boot that is already rw is written and left rw.
func TestStageBootloaderRW(t *testing.T) {
	root, calls := eepromHelperTree(t, eepromtest.Pi5, 100, 200)
	eepromtest.Write(t, mountsFile, []byte("/dev/mmcblk0p1 "+filepath.Join(root, "boot")+" vfat rw,relatime 0 0\n"))
	if err := (Local{}).StageBootloader(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("remounts %v", *calls)
	}
}

func TestCleanBootloader(t *testing.T) {
	stage := func(t *testing.T, root string, ts int64, recoveryBin bool) {
		boot := filepath.Join(root, "boot")
		eepromtest.Write(t, filepath.Join(boot, rpieeprom.Update), eepromtest.Image(ts))
		eepromtest.Write(t, filepath.Join(boot, rpieeprom.UpdateSig), []byte("sig"))
		eepromtest.Write(t, filepath.Join(boot, rpieeprom.VL805), []byte("vl"))
		eepromtest.Write(t, filepath.Join(boot, "config.txt"), []byte("keep"))
		if recoveryBin {
			eepromtest.Write(t, filepath.Join(boot, rpieeprom.Recovery), []byte("r"))
		} else {
			eepromtest.Write(t, filepath.Join(boot, rpieeprom.RecoveryOld), []byte("r"))
		}
	}
	for _, c := range []struct {
		name     string
		staged   int64
		recovery bool
		removed  bool
	}{
		{"flashed", 200, false, true},
		{"flashed an older one (beta.9's /boot after a newer EEPROM)", 150, false, true},
		{"waiting for its reboot", 200, true, false},
		{"a newer one, not flashed", 300, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, calls := eepromHelperTree(t, eepromtest.Pi4Rev11, 200, 200)
			stage(t, root, c.staged, c.recovery)
			if err := (Local{}).CleanBootloader(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := strings.Join(bootNames(t, root), ",")
			if c.removed && (got != "config.txt" || strings.Join(*calls, ",") != "rw,ro") {
				t.Errorf("left %s, remounts %v", got, *calls)
			}
			if !c.removed && (got == "config.txt" || len(*calls) != 0) {
				t.Errorf("removed: %s, remounts %v", got, *calls)
			}
		})
	}
	// nothing staged: no remount
	_, calls := eepromHelperTree(t, eepromtest.Pi4Rev11, 200, 200)
	if err := (Local{}).CleanBootloader(context.Background()); err != nil || len(*calls) != 0 {
		t.Errorf("nothing staged: %v %v", err, *calls)
	}
	eepromHelperTree(t, eepromtest.Pi3, 0, 0)
	if err := (Local{}).CleanBootloader(context.Background()); !errors.Is(err, ErrNotAvailable) {
		t.Errorf("a Pi 3: %v", err)
	}
}

// Through the helper: the two operations carry nothing, anything more is refused; the older
// image's refusal and the missing EEPROM come back as their sentinels.
func TestBootloaderOperations(t *testing.T) {
	root, _ := eepromHelperTree(t, eepromtest.Pi4Rev15, 100, 200)
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logged []string
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Log: func(f string, a ...any) { logged = append(logged, f) }}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}
	if err := c.StageBootloader(ctx); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !rpieeprom.Exists(root, rpieeprom.Recovery) {
		t.Error("not staged")
	}
	if err := c.CleanBootloader(ctx); err != nil {
		t.Errorf("clean: %v", err)
	}
	for _, req := range []request{{Op: opBootloaderStage, Path: "/tmp/x"}, {Op: opBootloaderClean, Src: "/etc"}, {Op: opBootloaderStage, Args: []string{"-f"}}} {
		if _, err := c.call(ctx, req); !errors.Is(err, ErrRefused) {
			t.Errorf("%+v: %v", req, err)
		}
	}
	if !strings.Contains(strings.Join(logged, "\n"), "bootloader request") {
		t.Errorf("the refusal was not logged: %v", logged)
	}
	eepromHelperTree(t, eepromtest.Pi4Rev15, 300, 200)
	if err := c.StageBootloader(ctx); !errors.Is(err, ErrBootloaderOlder) {
		t.Errorf("older: %v", err)
	}
	eepromHelperTree(t, eepromtest.Pi3, 0, 0)
	if err := c.StageBootloader(ctx); !errors.Is(err, ErrNotAvailable) {
		t.Errorf("a Pi 3: %v", err)
	}
}
