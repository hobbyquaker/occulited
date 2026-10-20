package system

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/rpieeprom"
)

// Bootloader is the Hardware page's bootloader section (openccu-lite task 366): the Raspberry Pi
// 4/5 bootloader EEPROM's running version, the version of the update the image carries, and the
// update on request. The image never puts the update into the live /boot, so nothing is flashed
// by itself; Stage has the helper copy it there, and the caller reboots. Once the boot ROM has
// flashed, Clean has the helper remove the files again.
type Bootloader struct {
	Root Root
	// Stage and Clean are the helper's; nil = Priv's.
	Stage func(ctx context.Context) error
	Clean func(ctx context.Context) error

	mu      sync.Mutex
	lastErr string
}

// BootloaderView is GET /hardware/bootloader.
type BootloaderView struct {
	// Available: a Raspberry Pi 4 or 5 whose running bootloader names its version. Reason says why
	// not: no-eeprom (another board, the Pi 3, the CCU3, a VM, a container), unreadable.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Chip is 2711 (Pi 4) or 2712 (Pi 5).
	Chip string `json:"chip,omitempty"`
	// Current and Latest are build times in seconds since 1970: the running bootloader's and the
	// image's update's (0 = the image carries none). CurrentDate and LatestDate are their UTC days,
	// the names rpi-eeprom's images carry.
	Current     int64  `json:"current,omitempty"`
	CurrentDate string `json:"current_date,omitempty"`
	Latest      int64  `json:"latest,omitempty"`
	LatestDate  string `json:"latest_date,omitempty"`
	// Update: the image's is newer than the running one. Newer: the running one is newer than the
	// image's (flashed by other means); nothing is offered then.
	Update bool `json:"update"`
	Newer  bool `json:"newer,omitempty"`
	// Staged: recovery.bin is in /boot - the next start flashes.
	Staged bool `json:"staged,omitempty"`
	// VL805: the Pi 4's USB controller has an EEPROM of its own, which the update flashes as well.
	VL805 bool `json:"vl805,omitempty"`
	// ImageError says why the image's update could not be read; Error is the last staging's.
	ImageError string `json:"image_error,omitempty"`
	Error      string `json:"error,omitempty"`
}

// The refusals of Update.
var (
	ErrBootloaderUnavailable = errors.New("this system has no bootloader EEPROM to update")
	ErrBootloaderUpToDate    = errors.New("the bootloader is up to date")
	ErrBootloaderNoImage     = errors.New("the system image carries no bootloader update")
)

func day(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02") }

// View reads the board, the running version, the image's update and /boot.
func (b *Bootloader) View() BootloaderView {
	root := string(b.Root)
	if root == "" {
		root = "/"
	}
	v := BootloaderView{}
	board, err := rpieeprom.ReadBoard(root)
	if err != nil || board.Chip == "" {
		v.Reason = "no-eeprom"
		return v
	}
	v.Chip, v.VL805 = board.Chip, board.VL805EEPROM
	cur, err := rpieeprom.Current(root)
	if err != nil {
		v.Reason = "unreadable"
		v.ImageError = err.Error()
		return v
	}
	v.Available, v.Current, v.CurrentDate = true, cur, day(cur)
	latest, err := rpieeprom.ImageVersion(filepath.Join(rpieeprom.ImageFiles(root, board.Chip), rpieeprom.Update))
	switch {
	case errors.Is(err, os.ErrNotExist):
		// an image from before task 366, or one built without the update
	case err != nil:
		v.ImageError = err.Error()
	default:
		v.Latest, v.LatestDate = latest, day(latest)
		v.Update, v.Newer = latest > cur, latest < cur
	}
	v.Staged = rpieeprom.Exists(root, rpieeprom.Recovery)
	b.mu.Lock()
	v.Error = b.lastErr
	b.mu.Unlock()
	return v
}

// Prepare stages the image's update into /boot, the step before the reboot. reinstall stages an
// image of the running version as well (for a check of the path); an older one is never staged.
func (b *Bootloader) Prepare(ctx context.Context, reinstall bool) error {
	v := b.View()
	switch {
	case !v.Available:
		return ErrBootloaderUnavailable
	case v.Latest == 0:
		return ErrBootloaderNoImage
	case v.Newer:
		return priv.ErrBootloaderOlder
	case !v.Update && !reinstall:
		return ErrBootloaderUpToDate
	}
	stage := b.Stage
	if stage == nil {
		stage = Priv.StageBootloader
	}
	err := stage(ctx)
	b.mu.Lock()
	b.lastErr = ""
	if err != nil {
		b.lastErr = err.Error()
	}
	b.mu.Unlock()
	return err
}

// CleanAtStart removes a flashed update's files from /boot (the helper decides whether the flash
// happened). Run once at occulited's start; quiet where there is nothing to do.
func (b *Bootloader) CleanAtStart(ctx context.Context) {
	v := b.View()
	if !v.Available || v.Staged {
		return
	}
	clean := b.Clean
	if clean == nil {
		clean = Priv.CleanBootloader
	}
	root := string(b.Root)
	// recovery.000 is the boot ROM's rename of recovery.bin once it has flashed
	flashed := rpieeprom.Exists(root, rpieeprom.RecoveryOld)
	if err := clean(ctx); err != nil {
		if !errors.Is(err, priv.ErrNotAvailable) {
			slog.Warn("bootloader: the flashed update's files could not be removed from /boot", "err", err)
		}
		return
	}
	if flashed && !rpieeprom.Exists(root, rpieeprom.RecoveryOld) {
		slog.Info("bootloader: the boot ROM has flashed the staged update (recovery.000); its files are removed from /boot", "current", v.CurrentDate)
	}
}
