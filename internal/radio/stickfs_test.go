package radio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B-62: on a FAT stick (usbmount's vfat, /media/usb0 a link to /media/usb1) the diagram data is
// copied without owners, groups and xattrs, which FAT cannot keep - -aogX ended every stop with
// exit 23 and a cp of the whole database. An ext4 stick keeps the mirror as before.
func TestStickRsyncArgs(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "media/usb1/measurement"), 0o755)
	_ = os.Symlink("usb1", filepath.Join(root, "media/usb0"))
	_ = os.MkdirAll(filepath.Join(root, "proc"), 0o755)
	mounts := func(s string) { _ = os.WriteFile(filepath.Join(root, "proc/mounts"), []byte(s), 0o644) }
	d := Detector{Root: root}
	stick, measure := d.path("/media/usb0/measurement"), d.path(DiagramPath)

	mounts("/dev/root / ext4 ro 0 0\n/dev/mmcblk0p3 /usr/local ext4 rw 0 0\n/dev/sda1 /media/usb1 vfat rw,gid=8102,fmask=0007 0 0\n")
	if got := strings.Join(stickRsyncArgs(d, stick, measure), " "); !strings.HasPrefix(got, "-rt --modify-window=2 --delete-after") || strings.Contains(got, "-aogX") {
		t.Errorf("vfat: %s", got)
	}
	mounts("/dev/root / ext4 ro 0 0\n/dev/sda1 /media/usb1 ext4 rw 0 0\n")
	if got := strings.Join(stickRsyncArgs(d, stick, measure), " "); !strings.HasPrefix(got, "-aogX ") {
		t.Errorf("ext4: %s", got)
	}
	// a mount point with a space, escaped as /proc/mounts does it; a prefix that is not a parent
	mounts("/dev/root / ext4 ro 0 0\n/dev/sdb1 /media/usb1x vfat rw 0 0\n")
	if got := mountFSType(d, stick); got != "ext4" {
		t.Errorf("a sibling mount point matched: %q", got)
	}
	_ = os.MkdirAll(filepath.Join(root, "media/my stick"), 0o755)
	mounts("/dev/root / ext4 ro 0 0\n/dev/sdb1 /media/my\\040stick exfat rw 0 0\n")
	if got := mountFSType(d, d.path("/media/my stick")); got != "exfat" {
		t.Errorf("escaped mount point: %q", got)
	}
	// no /proc/mounts: unknown, the mirror as before
	_ = os.Remove(filepath.Join(root, "proc/mounts"))
	if got := stickRsyncArgs(d, stick, measure)[0]; got != "-aogX" {
		t.Errorf("unknown: %s", got)
	}
}
