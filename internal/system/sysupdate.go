package system

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// The system firmware update, staged the way the WebUI's cp_maintenance.cgi and
// checkFirmwareUpdate.sh did it: the file lands in /usr/local/tmp, is checked by type
// (zip with EULA.en/EULA.de and an image, tar with EULA.*, a three-partition disk image, an
// ext4 rootfs, a vfat bootfs), /usr/local/.firmwareUpdate links to it, and "install" touches
// /usr/local/.recoveryMode before rebooting - the bootloader then starts the recovery system,
// whose fwinstall writes bootfs and rootfs and keeps the user partition. That is also the way
// back from openccu-lite to OpenCCU: upload OpenCCU's release zip.

const (
	updateDir     = "/usr/local/tmp"
	updateLink    = "/usr/local/.firmwareUpdate"
	recoveryFlag  = "/usr/local/.recoveryMode"
	updateDefault = "firmwareUpdateFile"
)

// MaxSystemUpdate is the ceiling for a staged system-update upload (B-256): the largest release
// image (the OVA zip is ~700 MB) plus headroom. It caps the copy so a lying or chunked
// Content-Length cannot run past it. A var so a test can lower it without writing 2 GiB.
var MaxSystemUpdate int64 = 2 << 30

// StagedUpdate describes the file /usr/local/.firmwareUpdate points to.
type StagedUpdate struct {
	File     string    `json:"file"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Kind     string    `json:"kind"`              // zip, tar, image, rootfs, bootfs, unknown
	Version  string    `json:"version,omitempty"` // from an OpenCCU-<version>-<board> file name
	Board    string    `json:"board,omitempty"`
	Warning  string    `json:"warning,omitempty"`
	// RecoveryArmed: .recoveryMode exists, the next boot installs.
	RecoveryArmed bool `json:"recovery_armed"`
	// WayBack: the file name is not an openccu-lite release (D-44's name, or OpenCCU's with a
	// "-lite." release), so it may well be the way back to OpenCCU. Staging one clears HSTS first
	// (task 96): OpenCCU sends no HSTS and later serves a self-signed certificate. A renamed file
	// counts as a way back - clearing HSTS costs little, a lockout a lot.
	WayBack bool `json:"way_back"`
	// Foreign: the file name names another board than this system's; the recovery refuses it
	// (the Warning says so). occulited task 22: the command line stops there.
	Foreign bool `json:"foreign,omitempty"`
	// SHA256: the checksum a download was verified against (occulited task 22); empty for an
	// upload and for a release that publishes none.
	SHA256 string `json:"sha256,omitempty"`
}

// isWayBack: not recognisably an openccu-lite release by its name.
func isWayBack(name string) bool {
	if liteReleaseRe.MatchString(strings.Replace(name, "-ccu3.tgz", ".tgz", 1)) {
		return false
	}
	m := releaseRe.FindStringSubmatch(name)
	return m == nil || !strings.Contains(m[1], "-lite.")
}

var (
	updateNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	// OpenCCU-<base>[-lite.<release>]-<board>.<ext>; "-lite.<digits>" is the release (D-37),
	// "-lite" without digits belongs to the board name
	releaseRe = regexp.MustCompile(`^OpenCCU-(\d+\.\d+\.\d+\.\d+(?:-lite\.[0-9A-Za-z][0-9A-Za-z.]*(?:-[a-z]+\.[0-9A-Za-z.]+)?)?)-([A-Za-z0-9_-]+)\.(zip|tgz|tar\.gz|img)$`)
	// D-44: openccu-lite-<product>-<semver>[-ccu3].<ext>; the product list is explicit because
	// both the product and the version carry dashes
	liteReleaseRe = regexp.MustCompile(`^openccu-lite-(x86_64-ova|aarch64-rpi3|aarch64-rpi4)-(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\.(zip|tgz|tar\.gz|img)$`)
)

// parseReleaseName reads version and board out of a release file name, the D-44 shape first.
func parseReleaseName(name string) (version, board string, ok bool) {
	// the CCU3's in-place package carries "-ccu3" before its extension; the version may carry
	// dashes of its own (1.0.0-alpha.0-snapshot.abc), so that suffix is taken off first
	if m := liteReleaseRe.FindStringSubmatch(strings.Replace(name, "-ccu3.tgz", ".tgz", 1)); m != nil {
		return m[2], m[1], true
	}
	if m := releaseRe.FindStringSubmatch(name); m != nil {
		return m[1], m[2], true
	}
	return "", "", false
}

// StagedSystemUpdate reports the staged update, nil when there is none.
func (r Root) StagedSystemUpdate() *StagedUpdate {
	target, err := os.Readlink(r.join(updateLink))
	if err != nil {
		return nil
	}
	path := target
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(r.join(updateLink)), target)
	} else {
		path = r.join(path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return &StagedUpdate{File: filepath.Base(target), Kind: "missing", Warning: "the linked file is gone"}
	}
	u := &StagedUpdate{File: filepath.Base(target), Size: st.Size(), Modified: st.ModTime()}
	u.Kind, _ = detectUpdateKind(path)
	u.Version, u.Board, u.Warning = r.describeRelease(u.File)
	u.Foreign = u.Warning != ""
	u.WayBack = isWayBack(u.File)
	_, err = os.Stat(r.join(recoveryFlag))
	u.RecoveryArmed = err == nil
	return u
}

// StageSystemUpdate stores the upload under /usr/local/tmp/<name>, checks it and links it as
// the pending update. size may be 0 (unknown); otherwise the free space is checked first.
func (r Root) StageSystemUpdate(ctx context.Context, name string, size int64, src io.Reader) (*StagedUpdate, error) {
	name = updateNameRe.ReplaceAllString(filepath.Base(strings.TrimSpace(name)), "_")
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		name = updateDefault
	}
	dir := r.join(updateDir)
	if err := Priv.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// B-256: size is the client's Content-Length claim, -1 for a chunked body and the whole body
	// for a multipart one. It may not be trusted as the only guard - a chunked upload (size <= 0)
	// skipped the space check, and the copy below ran until EOF with no cap, so one request could
	// fill the userfs (where occulited's own state, the backups and the journal live). A claim over
	// the ceiling is refused before anything is written; when the claim is a plausible size the free
	// space is checked against it as before; and the copy itself is capped, so a lying or chunked
	// length cannot run past the ceiling whatever the claim said - the copy fails at ENOSPC and
	// removes its file if the disk fills first, and the final size is the bytes actually written.
	if size > MaxSystemUpdate {
		return nil, fmt.Errorf("the update is larger than %d MB", MaxSystemUpdate>>20)
	}
	if size > 0 {
		if err := r.checkDownloadSpace(dir, name, size); err != nil {
			return nil, err
		}
	}
	r.DiscardSystemUpdate()
	path := filepath.Join(dir, name)
	// written into occulited's staging directory (same filesystem), checked, then moved
	tmp, n, err := stageFile(r, name+".part", io.LimitReader(readerCtx{ctx, src}, MaxSystemUpdate+1))
	if err != nil {
		return nil, err
	}
	if n > MaxSystemUpdate {
		os.Remove(tmp)
		return nil, fmt.Errorf("the update is larger than %d MB", MaxSystemUpdate>>20)
	}
	kind, err := detectUpdateKind(tmp)
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := Priv.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	// B-247: the recovery unpacks the file beside itself; refuse now what it would refuse then
	if err := checkUpdateSpace(path, kind); err != nil {
		_ = remove(path)
		return nil, err
	}
	// the WebUI linked the absolute path; the recovery reads the link on the mounted userfs
	if err := Priv.Symlink(filepath.Join(updateDir, name), r.join(updateLink)); err != nil {
		_ = remove(path)
		return nil, err
	}
	u := &StagedUpdate{File: name, Size: n, Modified: time.Now(), Kind: kind, WayBack: isWayBack(name)}
	u.Version, u.Board, u.Warning = r.describeRelease(name)
	u.Foreign = u.Warning != ""
	return u, nil
}

// ArmSystemUpdate touches /usr/local/.recoveryMode: the bootloader starts the recovery system
// at the next boot, which installs the staged file. The caller reboots.
func (r Root) ArmSystemUpdate() error {
	u := r.StagedSystemUpdate()
	if u == nil {
		return errors.New("no update staged")
	}
	// B-247: the space may have gone since the file was staged (a nightly backup)
	if u.Kind != "missing" {
		if err := checkUpdateSpace(filepath.Join(r.join(updateDir), u.File), u.Kind); err != nil {
			return err
		}
	}
	return touch(r.join(recoveryFlag), 0o644)
}

// UpdateSpaceError: the staged update would not fit where the recovery unpacks it (B-247), or,
// with Download, the file to be staged would not fit together with that room (B-99).
type UpdateSpaceError struct {
	Free, Required int64
	// Download: refused before the file was written - Required is the file and its unpacking.
	Download bool
}

func (e *UpdateSpaceError) Error() string {
	what := "to unpack it"
	if e.Download {
		what = "to store and unpack it"
	}
	return fmt.Sprintf("not enough space for the update on the system's own storage: %d MB free, %d MB needed %s - remove old backups (Backup page) or move them to a USB stick or a share", e.Free>>20, e.Required>>20, what)
}

// checkDownloadSpace is the check before a file of size bytes is written (B-99): on ova-1 a 1.3 GB
// image passed the old one (the file plus 16 MB), filled the user partition while the nightly
// backup was writing there, and the backup's list was lost. An archive needs its own size again
// to be unpacked - a lower bound (the unpacked size is known, and checked exactly, once the file
// is there); a disk image, a rootfs or a bootfs is written as it is. A staged update counts as
// free: it is discarded before the copy. The margin is the recovery check's.
func (r Root) checkDownloadSpace(dir, name string, size int64) error {
	free, err := updateFree(dir)
	if err != nil {
		return nil // the copy fails at ENOSPC and removes its file
	}
	if u := r.StagedSystemUpdate(); u != nil && u.Kind != "missing" {
		free += u.Size
	}
	need := size + updateSpaceMargin
	if unpacks(name) {
		need += size
	}
	if need >= free {
		return &UpdateSpaceError{Free: free, Required: need, Download: true}
	}
	return nil
}

// unpacks: the recovery unpacks the file by its name - a zip, a tar or a tar.gz. A name it cannot
// tell counts as one, the larger need.
func unpacks(name string) bool {
	n := strings.ToLower(name)
	for _, ext := range []string{".img", ".ext4", ".vfat", ".bin"} {
		if strings.HasSuffix(n, ext) {
			return false
		}
	}
	return true
}

// updateSpaceMargin is kept free beyond the recovery's own rule (it refuses when the unpacked size
// is not below the free space): the journal and the state go on writing until the reboot.
const updateSpaceMargin = 64 << 20

// updateFree is the space free beside the staged file (a test swaps it).
var updateFree = func(dir string) (int64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return 0, err
	}
	return int64(fs.Bavail) * int64(fs.Bsize), nil
}

// checkUpdateSpace applies the recovery's check (fwinstall.sh, "[5/7] Preparing uploaded data"):
// the archive's unpacked size must be below the free space of the directory it lies in, with the
// file itself still there. A disk image, a rootfs or a bootfs is written as it is, not unpacked.
func checkUpdateSpace(path, kind string) error {
	need, err := unpackedSize(path, kind)
	if err != nil || need == 0 {
		return err
	}
	free, err := updateFree(filepath.Dir(path))
	if err != nil {
		return nil // the recovery checks again
	}
	if need+updateSpaceMargin >= free {
		return &UpdateSpaceError{Free: free, Required: need + updateSpaceMargin}
	}
	return nil
}

// unpackedSize is what the recovery unpacks: a zip's entries (unzip -v's total), a tar's files
// (tar -tv's sizes); 0 for the kinds it does not unpack.
func unpackedSize(path, kind string) (int64, error) {
	switch kind {
	case "zip":
		zr, err := zip.OpenReader(path)
		if err != nil {
			return 0, fmt.Errorf("zip: %w", err)
		}
		defer zr.Close()
		var n int64
		for _, e := range zr.File {
			n += int64(e.UncompressedSize64)
		}
		return n, nil
	case "tar":
		f, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		head := make([]byte, 2)
		if _, err := io.ReadFull(f, head); err != nil {
			return 0, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		var rd io.Reader = f
		if head[0] == 0x1f && head[1] == 0x8b {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return 0, fmt.Errorf("gzip: %w", err)
			}
			defer gz.Close()
			rd = gz
		}
		tr := tar.NewReader(rd)
		var n int64
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return n, nil
			}
			if err != nil {
				return 0, fmt.Errorf("tar: %w", err)
			}
			n += h.Size
		}
	}
	return 0, nil
}

// DiscardSystemUpdate removes the link, the file it points to and the recovery marker.
func (r Root) DiscardSystemUpdate() {
	if u := r.StagedSystemUpdate(); u != nil && u.Kind != "missing" {
		_ = remove(filepath.Join(r.join(updateDir), u.File))
	}
	_ = remove(r.join(updateLink))
	_ = remove(r.join(recoveryFlag))
}

// describeRelease reads version and board out of an OpenCCU release file name and warns when
// the board does not match the running product (the recovery refuses a different PLATFORM;
// "-lite" boards are the same platform, D-31).
func (r Root) describeRelease(name string) (version, board, warning string) {
	version, board, ok := parseReleaseName(name)
	if !ok {
		return "", "", ""
	}
	running := r.ReadVersion()
	if running.Product != "" && !boardMatches(board, running.Product, running.Platform) {
		warning = fmt.Sprintf("the file is for %q, this system runs %q: the recovery will refuse it", board, running.Product)
	}
	return
}

// boardAliases maps a release file's board to the upstream product it is built from, for the two
// places where the file name and the image's own /VERSION disagree on purpose:
//
//   - upstream packs the CCU3's in-place update out of the rpi3 build and calls it "ccu3"
//     (board/rpi3/post-release.sh), while the image inside says PRODUCT=rpi3 - so the most
//     important update path of the target hardware warned that the recovery would refuse it;
//
// The D-39/D-43 names need no alias: they carry the board as a dash-separated part
// ("aarch64-rpi3", "x86_64-ova"), which boardMatches finds on its own.
//
// The alias applies only to a board that *is* the key, never to one that merely contains it:
// upstream's own "generic-aarch64" must keep warning on an rpi4 box.
var boardAliases = map[string][]string{
	"ccu3": {"rpi3"},
}

// boardMatches decides whether a release file's board names this box. The recovery compares the
// PLATFORM in the image, and a lite image keeps upstream's PRODUCT/PLATFORM (D-31), so the file
// name carries the lite product beside it: "x86_64-ova", "aarch64-rpi3", "aarch64-rpi4". A suffix
// check was not enough - it only stripped "-lite", so the systemd zip warned that a box running
// "ova" would have its own image refused (B-18). Any dash-separated part naming the product or
// the platform is a match, so "aarch64-rpi3" matches an rpi3 box and correctly does not match an
// rpi4 one; the rest is the alias table above.
func boardMatches(board, product, platform string) bool {
	for _, part := range strings.Split(board, "-") {
		if part == product || (platform != "" && part == platform) {
			return true
		}
	}
	for _, alias := range boardAliases[board] {
		if alias == product || (platform != "" && alias == platform) {
			return true
		}
	}
	return false
}

// detectUpdateKind applies the WebUI's checks: zip with EULA.en+EULA.de, tar(.gz) with EULA.*,
// MBR image with three partitions (the third ext4), an ext4 rootfs, a vfat bootfs.
func detectUpdateKind(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if n < 512 {
		return "", errors.New("not a firmware file (too small)")
	}
	switch {
	case head[0] == 'P' && head[1] == 'K':
		zr, err := zip.OpenReader(path)
		if err != nil {
			return "", fmt.Errorf("zip: %w", err)
		}
		defer zr.Close()
		have := map[string]bool{}
		img := false
		for _, e := range zr.File {
			b := filepath.Base(e.Name)
			have[b] = true
			img = img || strings.HasSuffix(b, ".img")
		}
		if !have["EULA.en"] || !have["EULA.de"] {
			return "", errors.New("zip: not a firmware update (EULA.en and EULA.de missing)")
		}
		if !img {
			return "", errors.New("zip: no *.img inside")
		}
		return "zip", nil
	case head[0] == 0x1f && head[1] == 0x8b, string(head[257:262]) == "ustar":
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		var rd io.Reader = f
		if head[0] == 0x1f {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return "", fmt.Errorf("gzip: %w", err)
			}
			defer gz.Close()
			rd = gz
		}
		tr := tar.NewReader(rd)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return "", errors.New("tar: not a firmware update (no EULA inside)")
			}
			if err != nil {
				return "", fmt.Errorf("tar: %w", err)
			}
			if strings.HasPrefix(filepath.Base(h.Name), "EULA.") {
				return "tar", nil
			}
		}
	case head[510] == 0x55 && head[511] == 0xAA:
		if strings.Contains(string(head[:512]), "bootfs") {
			return "bootfs", nil
		}
		parts := 0
		third := byte(0)
		for i := 0; i < 4; i++ {
			e := head[446+16*i : 446+16*i+16]
			if e[4] != 0 && binary.LittleEndian.Uint32(e[12:16]) > 0 {
				parts++
				if i == 2 {
					third = e[4]
				}
			}
		}
		if parts == 3 && third == 0x83 {
			return "image", nil
		}
		return "", fmt.Errorf("disk image: expected three partitions with an ext4 third, found %d", parts)
	case n >= 1024+0x80 && binary.LittleEndian.Uint16(head[1024+0x38:1024+0x3a]) == 0xEF53:
		label := strings.TrimRight(string(head[1024+0x78:1024+0x88]), "\x00")
		if label == "rootfs" {
			return "rootfs", nil
		}
		return "", fmt.Errorf("ext4 image labelled %q, not rootfs", label)
	}
	return "", errors.New("not a firmware file (zip, tgz, disk image, rootfs or bootfs expected)")
}

type readerCtx struct {
	ctx context.Context
	r   io.Reader
}

func (c readerCtx) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// ReleaseFileVersion reads the version out of a release file's name (D-44's
// openccu-lite-<product>-<version>.<ext>, or OpenCCU's): what `occulited update install --file`
// compares with the running one before it uploads.
func ReleaseFileVersion(name string) (version string, ok bool) {
	version, _, ok = parseReleaseName(filepath.Base(name))
	return version, ok
}
