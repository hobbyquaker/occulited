package priv

// openccu-lite task 228 (phase 1, the maintainer: "users should be able to erase/format a usb stick
// filesystem should be chosable, i would suggest offering ext4 and exfat"): formatting a USB stick
// and removing it safely, as root. The daemon names a disk, a filesystem and a label; the helper
// decides whether that disk may be touched at all - a whole disk on the USB bus, holding no
// filesystem the system runs from - and runs the fixed steps with argument slices.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const (
	opUSBFormat = "usbformat"
	opUSBEject  = "usbeject"
)

// USBFormatSpec is a format request: the disk's kernel name (sda), exfat or ext4, the label.
type USBFormatSpec struct {
	Device string `json:"device"`
	FS     string `json:"fs"`
	Label  string `json:"label"`
}

var (
	usbDiskRe  = regexp.MustCompile(`^sd[a-z]{1,2}$`)
	loopDiskRe = regexp.MustCompile(`^loop[0-9]{1,3}$`)
	// a label every filesystem, udev's ID_FS_LABEL and the journal's usb:<label> target take as
	// it is: letters, digits, - and _
	usbLabelRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// USBTestLoopMarker, when it exists, admits loop devices as "sticks" - the lab's stand-in (task
// 228's lab check), never present on a product image.
var USBTestLoopMarker = "/usr/local/etc/occulite/storage-test-loop"

// ValidUSBLabel checks a label for fs: exFAT takes at most 15 characters, ext4 16.
func ValidUSBLabel(fs, label string) error {
	max := 0
	switch fs {
	case "exfat":
		max = 15
	case "ext4":
		max = 16
	default:
		return errors.New("filesystem: exfat or ext4")
	}
	if label == "" || len(label) > max || !usbLabelRe.MatchString(label) {
		return fmt.Errorf("label: 1 to %d letters, digits, - or _", max)
	}
	return nil
}

func (p Policy) sysPath(path string) string { return filepath.Join(p.Root, path) }

// usbDiskOK is the boundary: dev is a whole disk on the USB bus (or, with the lab marker, a loop
// device), and no filesystem the system runs from lives on it.
func (p Policy) usbDiskOK(dev string) error {
	loop := loopDiskRe.MatchString(dev)
	if !usbDiskRe.MatchString(dev) && !loop {
		return fmt.Errorf("%q is not a USB disk's name", dev)
	}
	if loop {
		if _, err := os.Stat(p.sysPath(USBTestLoopMarker)); err != nil {
			return fmt.Errorf("%s: loop devices are admitted only on a lab system", dev)
		}
	} else {
		link, err := filepath.EvalSymlinks(p.sysPath("/sys/block/" + dev))
		if err != nil {
			return fmt.Errorf("%s: no such disk", dev)
		}
		if !strings.Contains(link, "/usb") {
			return fmt.Errorf("%s is not on the USB bus", dev)
		}
	}
	// no system filesystem on it: every mount outside /media, and swap
	b, _ := os.ReadFile(p.sysPath("/proc/mounts"))
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !onDisk(f[0], dev) {
			continue
		}
		if !strings.HasPrefix(f[1], "/media/") {
			return fmt.Errorf("%s holds %s, a filesystem the system runs from", dev, f[1])
		}
	}
	sw, _ := os.ReadFile(p.sysPath("/proc/swaps"))
	for _, line := range strings.Split(string(sw), "\n") {
		if f := strings.Fields(line); len(f) > 0 && onDisk(f[0], dev) {
			return fmt.Errorf("%s holds swap", dev)
		}
	}
	return nil
}

// onDisk says whether the device path src is dev or one of its partitions (sda, sda1, loop0p1).
func onDisk(src, dev string) bool {
	name := strings.TrimPrefix(src, "/dev/")
	if name == src || !strings.HasPrefix(name, dev) {
		return false
	}
	rest := strings.TrimPrefix(name, dev)
	if rest == "" {
		return true
	}
	if strings.HasPrefix(dev, "loop") {
		// loop1's partitions are loop1p1, ...; loop10 is another device
		if rest[0] != 'p' {
			return false
		}
		rest = rest[1:]
	}
	return rest != "" && strings.Trim(rest, "0123456789") == ""
}

// usbPartitions is dev's partitions as sysfs lists them.
func (p Policy) usbPartitions(dev string) []string {
	var out []string
	ents, _ := os.ReadDir(p.sysPath("/sys/block/" + dev))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), dev) && e.Name() != dev {
			out = append(out, e.Name())
		}
	}
	return out
}

// Client side.

// USBFormat erases the disk and makes one partition with fs and label.
func (c Client) USBFormat(ctx context.Context, s USBFormatSpec) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, request{Op: opUSBFormat, Data: b})
	return err
}

// USBEject stops the disk's mount units (the journal's last copy, the unmount) and unmounts what
// is left of it.
func (c Client) USBEject(ctx context.Context, dev string) error {
	_, err := c.call(ctx, request{Op: opUSBEject, Name: dev})
	return err
}

// Server side.

// usbStop releases every partition of dev: its occu-usb-mount@ unit first (whose stop makes the
// journal's last copy and runs usbmount's hooks), then a plain unmount of anything still mounted.
func (s *Server) usbStop(ctx context.Context, dev string) error {
	ops := s.ops()
	for _, part := range append([]string{dev}, s.Policy.usbPartitions(dev)...) {
		_, _ = ops.Run(ctx, "systemctl", []string{"stop", "occu-usb-mount@" + part + ".service"}, nil)
	}
	b, _ := os.ReadFile(s.Policy.sysPath("/proc/mounts"))
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !onDisk(f[0], dev) {
			continue
		}
		mp := strings.ReplaceAll(f[1], `\040`, " ")
		if r, err := ops.Run(ctx, "umount", []string{mp}, nil); err != nil || r.Exit != 0 {
			return fmt.Errorf("%s is busy and was not unmounted: %s", mp, strings.TrimSpace(string(r.Combined())))
		}
	}
	return nil
}

func (s *Server) usbDisk(ctx context.Context, req request) response {
	ops := s.ops()
	switch req.Op {
	case opUSBEject:
		if !reflect.DeepEqual(req, request{Op: req.Op, Name: req.Name}) {
			return refuse("usbeject takes a disk and nothing else")
		}
		if err := s.Policy.usbDiskOK(req.Name); err != nil {
			s.log("helper: refused to eject: %v", err)
			return refuse("usbeject: " + err.Error())
		}
		if err := s.usbStop(ctx, req.Name); err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true}
	case opUSBFormat:
		if !reflect.DeepEqual(req, request{Op: req.Op, Data: req.Data}) {
			return refuse("usbformat takes a disk, a filesystem and a label, nothing else")
		}
		var spec USBFormatSpec
		dec := json.NewDecoder(strings.NewReader(string(req.Data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&spec); err != nil {
			return refuse("usbformat: " + err.Error())
		}
		if err := ValidUSBLabel(spec.FS, spec.Label); err != nil {
			return refuse("usbformat: " + err.Error())
		}
		if err := s.Policy.usbDiskOK(spec.Device); err != nil {
			s.log("helper: refused to format: %v", err)
			return refuse("usbformat: " + err.Error())
		}
		if err := s.usbStop(ctx, spec.Device); err != nil {
			return response{Error: err.Error()}
		}
		disk := "/dev/" + spec.Device
		part := disk + "1"
		ptype := "7" // exFAT: MBR type 07 (HPFS/NTFS/exFAT), what a PC expects on a stick
		if spec.FS == "ext4" {
			ptype = "83"
		}
		if loopDiskRe.MatchString(spec.Device) {
			part = disk + "p1"
		}
		steps := []struct {
			name  string
			args  []string
			stdin []byte
		}{
			// one MBR partition over the whole stick, the old signatures wiped (the image has no
			// wipefs; sfdisk's --wipe does the same for the disk and the new partition)
			{"sfdisk", []string{"--wipe", "always", "--wipe-partitions", "always", disk}, []byte("label: dos\n,," + ptype + "\n")},
			{"udevadm", []string{"settle", "--timeout=30"}, nil},
		}
		// the image's mkfs.exfat is exfat-utils' (mkexfatfs): the label is -n
		if spec.FS == "exfat" {
			steps = append(steps, struct {
				name  string
				args  []string
				stdin []byte
			}{"mkfs.exfat", []string{"-n", spec.Label, part}, nil})
		} else {
			steps = append(steps, struct {
				name  string
				args  []string
				stdin []byte
			}{"mkfs.ext4", []string{"-F", "-q", "-m", "0", "-L", spec.Label, part}, nil})
		}
		steps = append(steps, struct {
			name  string
			args  []string
			stdin []byte
		}{"udevadm", []string{"settle", "--timeout=30"}, nil})
		for _, st := range steps {
			if st.name == "mkfs.exfat" || st.name == "mkfs.ext4" {
				// the partition's node appears with udev's event; wait for it
				for i := 0; i < 50; i++ {
					if _, err := os.Stat(s.Policy.sysPath(part)); err == nil {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			r, err := ops.Run(ctx, st.name, st.args, st.stdin)
			if err != nil {
				return response{Error: fmt.Sprintf("%s: %v", st.name, err)}
			}
			if r.Exit != 0 {
				return response{Error: fmt.Sprintf("%s failed (exit %d): %s", st.name, r.Exit, strings.TrimSpace(string(r.Combined())))}
			}
		}
		// the stick mounts again through its unit, as when it was plugged in; a loop device has none
		if !loopDiskRe.MatchString(spec.Device) {
			_, _ = ops.Run(ctx, "systemctl", []string{"start", "occu-usb-mount@" + spec.Device + "1.service"}, nil)
		}
		s.info("helper: formatted %s as %s, label %s", disk, spec.FS, spec.Label)
		return response{OK: true}
	}
	return refuse("op " + req.Op)
}
