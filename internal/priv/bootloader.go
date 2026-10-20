package priv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hobbyquaker/occulited/internal/rpieeprom"
)

// The Raspberry Pi 4/5 bootloader EEPROM update (openccu-lite task 366). The image carries the
// update outside the live /boot (rpieeprom.ImageDir), so the boot ROM never flashes it by itself;
// an administrator asks for it on the Hardware page, and the helper stages it: /boot remounted rw,
// the files copied in, synced, /boot back to ro. The reboot that follows is the daemon's (the
// power route's), and the ROM's recovery.bin flashes. Once it has, the daemon asks for the clean,
// which removes the staged names again.
//
// Two operations that carry nothing: the helper finds the board, the files and the boot partition
// itself, from fixed places. It refuses an image older than the running bootloader (no downgrade
// through this path, whatever the daemon asks), a file that does not match its .sig, and a /boot
// that is not a mount of its own.

const (
	opBootloaderStage = "bootloader-stage"
	opBootloaderClean = "bootloader-clean"
)

var (
	// eepromRoot is the root the fixed paths are under; a test points it at a tree of its own.
	eepromRoot = "/"
	// mountsFile says whether /boot is mounted and how; remount is the remount itself.
	mountsFile = "/proc/self/mounts"
	remount    = func(ctx context.Context, dir string, rw bool) error {
		opt := "remount,ro"
		if rw {
			opt = "remount,rw"
		}
		out, err := exec.CommandContext(ctx, "/bin/mount", "-o", opt, dir).CombinedOutput()
		if err != nil {
			return fmt.Errorf("mount -o %s %s: %v: %s", opt, dir, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

// ErrBootloaderOlder is the refusal of an image older than the running bootloader.
var ErrBootloaderOlder = errors.New("the image's bootloader is older than the running one")

// bootMount answers whether dir is a mount point and whether it is mounted read-only.
func bootMount(dir string) (mounted, ro bool, err error) {
	f, err := os.Open(mountsFile)
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[1] != dir {
			continue
		}
		// the last line for a mount point is the one that counts (a mount over another)
		mounted = true
		ro = false
		for _, o := range strings.Split(fields[3], ",") {
			ro = ro || o == "ro"
		}
	}
	return mounted, ro, sc.Err()
}

// withBootRW runs fn with /boot writable and puts it back to read-only afterwards, when it was.
func withBootRW(ctx context.Context, fn func(dir string) error) error {
	dir := filepath.Join(eepromRoot, rpieeprom.BootDir)
	mounted, ro, err := bootMount(dir)
	if err != nil {
		return err
	}
	if !mounted {
		return fmt.Errorf("%s is not a mount of its own", dir)
	}
	if ro {
		if err := remount(ctx, dir, true); err != nil {
			return err
		}
	}
	ferr := fn(dir)
	syncDir(dir)
	if ro {
		if err := remount(context.WithoutCancel(ctx), dir, false); err != nil && ferr == nil {
			ferr = err
		}
	}
	return ferr
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// stagedFile is one file to copy: its name and its bound, and the signature that checks it.
type stagedFile struct {
	name string
	max  int64
	sig  string
}

// writeSynced writes data to dir/name through a temporary name, synced before the rename.
func writeSynced(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, filepath.Join(dir, name))
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// StageBootloader copies the image's bootloader update into /boot for the boot ROM: pieeprom.upd
// and its .sig, vl805.bin and its .sig on a Pi 4 whose USB controller has an EEPROM of its own,
// and recovery.bin last - without it the ROM flashes nothing, so a staging cut short leaves nothing
// that runs. A recovery.000 of an earlier flash goes first.
func (Local) StageBootloader(ctx context.Context) error {
	board, err := rpieeprom.ReadBoard(eepromRoot)
	if err != nil {
		return fmt.Errorf("bootloader: %w", err)
	}
	if board.Chip == "" {
		return ErrNotAvailable
	}
	src := rpieeprom.ImageFiles(eepromRoot, board.Chip)
	latest, err := rpieeprom.ImageVersion(filepath.Join(src, rpieeprom.Update))
	if err != nil {
		return fmt.Errorf("bootloader: the image's update: %w", err)
	}
	current, err := rpieeprom.Current(eepromRoot)
	if err != nil {
		return fmt.Errorf("bootloader: the running version: %w", err)
	}
	if latest < current {
		return ErrBootloaderOlder
	}
	files := []stagedFile{
		{rpieeprom.UpdateSig, rpieeprom.MaxSig, ""},
		{rpieeprom.Update, rpieeprom.MaxImage, rpieeprom.UpdateSig},
	}
	if board.VL805EEPROM {
		if _, err := os.Lstat(filepath.Join(src, rpieeprom.VL805)); err == nil {
			files = append(files,
				stagedFile{rpieeprom.VL805Sig, rpieeprom.MaxSig, ""},
				stagedFile{rpieeprom.VL805, rpieeprom.MaxVL805, rpieeprom.VL805Sig},
				stagedFile{rpieeprom.VL805Ver, rpieeprom.MaxSig, ""})
		}
	}
	files = append(files, stagedFile{rpieeprom.Recovery, rpieeprom.MaxRecovery, ""})
	// read and check everything before /boot is touched
	data := map[string][]byte{}
	for _, f := range files {
		b, err := rpieeprom.ReadBounded(filepath.Join(src, f.name), f.max)
		if err != nil {
			return fmt.Errorf("bootloader: %w", err)
		}
		data[f.name] = b
	}
	for _, f := range files {
		if f.sig != "" {
			if err := rpieeprom.CheckSig(data[f.name], data[f.sig]); err != nil {
				return fmt.Errorf("bootloader: %s: %w", f.name, err)
			}
		}
	}
	return withBootRW(ctx, func(dir string) error {
		_ = os.Remove(filepath.Join(dir, rpieeprom.RecoveryOld))
		for _, f := range files {
			if err := writeSynced(dir, f.name, data[f.name]); err != nil {
				// recovery.bin is written last: whatever went in before it does not run
				return fmt.Errorf("bootloader: %s: %w", f.name, err)
			}
		}
		return nil
	})
}

// CleanBootloader removes the staged names from /boot once the flash has happened: recovery.bin is
// gone (the ROM renamed it to recovery.000) and the staged image is not newer than the running
// bootloader. Anything else - a staging waiting for its reboot, a newer image not yet flashed -
// stays. Nothing to remove is no error and no remount.
func (Local) CleanBootloader(ctx context.Context) error {
	board, err := rpieeprom.ReadBoard(eepromRoot)
	if err != nil || board.Chip == "" {
		return ErrNotAvailable
	}
	if rpieeprom.Exists(eepromRoot, rpieeprom.Recovery) {
		return nil
	}
	if !rpieeprom.Exists(eepromRoot, rpieeprom.Update) && !rpieeprom.Exists(eepromRoot, rpieeprom.RecoveryOld) {
		return nil
	}
	current, err := rpieeprom.Current(eepromRoot)
	if err != nil {
		return fmt.Errorf("bootloader: the running version: %w", err)
	}
	if rpieeprom.Exists(eepromRoot, rpieeprom.Update) {
		staged, err := rpieeprom.ImageVersion(filepath.Join(eepromRoot, rpieeprom.BootDir, rpieeprom.Update))
		if err == nil && staged > current {
			return nil
		}
	}
	return withBootRW(ctx, func(dir string) error {
		var failed error
		for _, n := range rpieeprom.StagedNames {
			p := filepath.Join(dir, n)
			if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if err := os.Remove(p); err != nil && failed == nil {
				failed = err
			}
		}
		return failed
	})
}

// StageBootloader asks the helper to stage the image's bootloader update into /boot.
func (c Client) StageBootloader(ctx context.Context) error {
	_, err := c.call(ctx, request{Op: opBootloaderStage})
	return bootloaderErr(err)
}

// CleanBootloader asks the helper to remove a flashed update's files from /boot.
func (c Client) CleanBootloader(ctx context.Context) error {
	_, err := c.call(ctx, request{Op: opBootloaderClean})
	return bootloaderErr(err)
}

// bootloaderErr gives the refusal of an older image its sentinel back (ErrNotAvailable comes
// back through the answer's not-available form).
func bootloaderErr(err error) error {
	if err != nil && !errors.Is(err, ErrNotAvailable) && strings.Contains(err.Error(), ErrBootloaderOlder.Error()) {
		return ErrBootloaderOlder
	}
	return err
}
