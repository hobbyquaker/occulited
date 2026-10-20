// Package rpieeprom reads what the Hardware page's bootloader section shows (openccu-lite task
// 366): the Raspberry Pi 4/5 bootloader EEPROM's running version and the update image the system
// image carries. Pure: files under a root, no helper, no commands - the daemon and the privilege
// helper both read through it.
//
// The image never stages the update into the live /boot (the boot ROM would flash it at the next
// start); it keeps the files under ImageDir/firmware-<chip>/, and the helper copies them into /boot
// when an administrator asks for the update. The boot ROM runs recovery.bin, which flashes
// pieeprom.upd (and vl805.bin on a Pi 4 with a VL805 EEPROM of its own) and renames itself to
// recovery.000 - the same files and names upstream's image puts into /boot.
package rpieeprom

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The two chips with a bootloader EEPROM, as rpi-eeprom names its directories (firmware-2711,
// firmware-2712). The Pi 3 and older, and the CCU3, have none.
const (
	Chip2711 = "2711" // Raspberry Pi 4, 400, CM4
	Chip2712 = "2712" // Raspberry Pi 5, 500, CM5
)

// The fixed places, relative to the root.
const (
	// ImageDir holds the image's update files, one directory per chip: firmware-2711/,
	// firmware-2712/ (the fork's rpi-eeprom package, BR2_PACKAGE_RPI_EEPROM_OFFER).
	ImageDir = "/usr/share/rpi-eeprom"
	// BootDir is the boot partition, where the boot ROM looks for recovery.bin.
	BootDir = "/boot"
	// RevisionFile and CPUInfo name the board (rpi-eeprom-info reads the same two).
	RevisionFile = "/sys/firmware/devicetree/base/system/linux,revision"
	CPUInfo      = "/proc/cpuinfo"
	// BuildTimestampFile is the running bootloader's build time, a 32-bit big-endian number of
	// seconds, which the firmware passes in the device tree (also through U-Boot).
	BuildTimestampFile = "/proc/device-tree/chosen/bootloader/build-timestamp"
)

// The file names, in /boot and in the image's directory.
const (
	Update      = "pieeprom.upd"
	UpdateSig   = "pieeprom.sig"
	Recovery    = "recovery.bin"
	RecoveryOld = "recovery.000" // what recovery.bin is renamed to once it has flashed
	VL805       = "vl805.bin"
	VL805Sig    = "vl805.sig"
	VL805Ver    = "vl805.ver"
)

// StagedNames are every name a staging writes into /boot and a clean removes - nothing else in /boot
// is ever touched.
var StagedNames = []string{Update, UpdateSig, Recovery, RecoveryOld, VL805, VL805Sig, VL805Ver}

// Bounds: the Pi 4's image is 512 KiB, the Pi 5's 2 MiB, recovery.bin about 100 KiB.
const (
	MaxImage    = 4 << 20
	MaxRecovery = 1 << 20
	MaxSig      = 4 << 10
	MaxVL805    = 1 << 20
)

// Board is what the revision code says.
type Board struct {
	// Chip is Chip2711, Chip2712 or "" (no EEPROM).
	Chip string
	// VL805EEPROM: a Pi 4 Model B before revision 1.4, whose USB controller has an EEPROM of its own
	// that recovery.bin flashes from vl805.bin (later boards keep its firmware in the bootloader's).
	VL805EEPROM bool
	// Revision is the code as read, hex.
	Revision string
}

func join(root, p string) string { return filepath.Join(root, p) }

// ParseRevision decodes a new-style revision code: the processor in bits 12-15 (3 = BCM2711,
// 4 = BCM2712), the board type in bits 4-11 (0x11 = 4 Model B) and its revision in bits 0-3.
func ParseRevision(hexCode string) (Board, error) {
	code, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(hexCode), "0x"), 16, 32)
	if err != nil {
		return Board{}, fmt.Errorf("revision %q: %w", hexCode, err)
	}
	b := Board{Revision: fmt.Sprintf("%x", code)}
	if code&(1<<23) == 0 {
		// an old-style code: a Pi 1 or the like
		return b, nil
	}
	switch (code >> 12) & 0xf {
	case 3:
		b.Chip = Chip2711
	case 4:
		b.Chip = Chip2712
	}
	b.VL805EEPROM = b.Chip == Chip2711 && (code>>4)&0xff == 0x11 && code&0xf < 4
	return b, nil
}

// ReadBoard reads the revision code from the device tree, else from /proc/cpuinfo.
func ReadBoard(root string) (Board, error) {
	if raw, err := os.ReadFile(join(root, RevisionFile)); err == nil && len(raw) == 4 {
		return ParseRevision(fmt.Sprintf("%08x", binary.BigEndian.Uint32(raw)))
	}
	f, err := os.Open(join(root, CPUInfo))
	if err != nil {
		return Board{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.TrimSpace(k) == "Revision" {
			return ParseRevision(v)
		}
	}
	return Board{}, errors.New("no revision code")
}

// Current reads the running bootloader's build time (seconds since 1970).
func Current(root string) (int64, error) {
	raw, err := os.ReadFile(join(root, BuildTimestampFile))
	if err != nil {
		return 0, err
	}
	if len(raw) != 4 {
		return 0, fmt.Errorf("%s: %d bytes, not 4", BuildTimestampFile, len(raw))
	}
	ts := int64(binary.BigEndian.Uint32(raw))
	if ts == 0 {
		return 0, errors.New("the bootloader names no build time")
	}
	return ts, nil
}

// ImageFiles is the image's directory for a chip.
func ImageFiles(root, chip string) string {
	return join(root, filepath.Join(ImageDir, "firmware-"+chip))
}

var buildTS = []byte("BUILD_TIMESTAMP=")

// ImageVersion reads the build time an EEPROM image carries: the BUILD_TIMESTAMP=<seconds> string
// rpi-eeprom-info's LATEST is read from.
func ImageVersion(path string) (int64, error) {
	b, err := readBounded(path, MaxImage)
	if err != nil {
		return 0, err
	}
	i := bytes.Index(b, buildTS)
	if i < 0 {
		return 0, fmt.Errorf("%s: no %s", filepath.Base(path), buildTS)
	}
	rest := b[i+len(buildTS):]
	n := 0
	for n < len(rest) && n < 12 && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	ts, err := strconv.ParseInt(string(rest[:n]), 10, 64)
	if err != nil || ts <= 0 {
		return 0, fmt.Errorf("%s: %s is not a time", filepath.Base(path), buildTS)
	}
	return ts, nil
}

// readBounded reads a regular file (no link followed) of at most max bytes.
func readBounded(path string, max int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s: %d bytes, more than %d", path, fi.Size(), max)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max+1))
}

// ReadBounded is readBounded for the helper's copy.
func ReadBounded(path string, max int64) ([]byte, error) { return readBounded(path, max) }

// CheckSig checks a file against its .sig: rpi-eeprom-digest's first line is the SHA-256 in hex
// (a "ts: <seconds>" line, and an "rsa2048: …" line for a signed image, may follow). recovery.bin
// checks it again before it flashes; the helper checks first so that nothing broken is staged.
func CheckSig(data, sig []byte) error {
	first, _, _ := strings.Cut(string(sig), "\n")
	want := strings.ToLower(strings.TrimSpace(first))
	if len(want) != 64 {
		return errors.New("the signature file has no SHA-256")
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return errors.New("the file does not match its signature file")
	}
	return nil
}

// Exists says whether /boot holds name as a regular file.
func Exists(root, name string) bool {
	fi, err := os.Lstat(join(root, filepath.Join(BootDir, name)))
	return err == nil && fi.Mode().IsRegular()
}
