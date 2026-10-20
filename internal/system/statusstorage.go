package system

// occulited task 78 (the maintainer, 2026-10-16): the Status page's storage panels - one per local
// disk with a mounted filesystem (the SD card, the eMMC, the VM's disk, every USB stick or disk),
// each partition's use as a ring. The disks come from /sys/block as the Storage page reads them
// (storageDevices), so network shares, tmpfs, zram and the overlays are none of them; the mounts
// are this process's, as the Storage page's are. Bytes throughout: the page decides the units.

import (
	"os"
	"sort"
	"strings"
	"time"
)

// blockNode is a disk or one of its partitions as sysfs lists it.
type blockNode struct {
	name  string // mmcblk0p3
	dev   string // 179:3
	bytes int64  // its size in sysfs
}

// StatusDevice is one disk of the Status page's storage panels.
type StatusDevice struct {
	Name string `json:"name"` // mmcblk0, sda
	// Kind, Vendor, Model and Virtual as the Storage page has them (StorageDevice)
	Kind    string `json:"kind"`
	Vendor  string `json:"vendor,omitempty"`
	Model   string `json:"model,omitempty"`
	Virtual bool   `json:"virtual,omitempty"`
	// SizeBytes is the block device's own size (/sys/block/<dev>/size × 512), not the sum of the
	// partitions: space no partition holds counts, so a 16 GB card says 16 GB.
	SizeBytes int64 `json:"size_bytes"`
	// UnallocatedBytes is what no partition holds (the size less the partitions'), 0 for a disk
	// without a partition table.
	UnallocatedBytes int64             `json:"unallocated_bytes"`
	Partitions       []StatusPartition `json:"partitions"`
}

// StatusPartition is one mounted filesystem of a disk.
type StatusPartition struct {
	Name  string `json:"name"`  // mmcblk0p3, or the disk's name for a filesystem on the whole disk
	Mount string `json:"mount"` // the first mount point of the filesystem's root
	// Role is root (/), boot (/boot), userfs (/usr/local) or other - the ring's place and colour
	Role  string `json:"role"`
	Label string `json:"label,omitempty"` // the filesystem's label, from udev
	// ReadOnly: the filesystem is mounted read-only (its superblock's options - occulited's own
	// namespace sees more read-only than there is)
	ReadOnly bool `json:"read_only,omitempty"`
	// SizeBytes is the partition's size in sysfs; TotalBytes and UsedBytes are the filesystem's
	// (statfs: the blocks, and the blocks less those an unprivileged writer has left - ext4's
	// reserved blocks count as used, as the mount gauges counted them)
	SizeBytes  int64 `json:"size_bytes"`
	TotalBytes int64 `json:"total_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
}

// statusStatfs is a mount point's size and free space; the tests answer it themselves.
var statusStatfs = func(r Root, point string) ([2]int64, bool) { return r.statfs(point) }

// the rings' order, outermost first (the maintainer: the userfs, the system, the boot partition)
var partitionRoleOrder = map[string]int{"userfs": 0, "root": 1, "boot": 2, "other": 3}

func partitionRole(mount string) string {
	switch mount {
	case "/":
		return "root"
	case "/boot", "/boot/firmware":
		return "boot"
	case "/usr/local":
		return "userfs"
	}
	return "other"
}

// mountPoint is one mount of a filesystem's root in this process's mountinfo.
type mountPoint struct {
	point string
	ro    bool
}

// rootMounts maps "major:minor" to the first mount of the filesystem's root, in mountinfo's order.
func (r Root) rootMounts() map[string]mountPoint {
	out := map[string]mountPoint{}
	b, err := os.ReadFile(r.join("/proc/self/mountinfo"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || f[3] != "/" {
			continue
		}
		if _, seen := out[f[2]]; seen {
			continue
		}
		// read-only by the superblock's options (after the "-"), not the mount's: occulited's unit
		// (ProtectSystem=strict) sees the userfs read-only in its own namespace, which the
		// filesystem is not (measured on rpi4-1, 2026-10-20)
		opts := f[5]
		for i := 6; i+3 < len(f); i++ {
			if f[i] == "-" {
				opts = f[i+3]
				break
			}
		}
		ro := false
		for _, o := range strings.Split(opts, ",") {
			ro = ro || o == "ro"
		}
		out[f[2]] = mountPoint{point: unescapeMount(f[4]), ro: ro}
	}
	return out
}

// StatusStorage lists the disks with a mounted filesystem: the one holding / first, then by name.
func (r Root) StatusStorage(now time.Time) []StatusDevice {
	mounts := r.rootMounts()
	out := []StatusDevice{}
	for _, d := range r.storageDevices(now) {
		sd := StatusDevice{Name: d.Name, Kind: d.Kind, Vendor: d.Vendor, Model: d.Model, Virtual: d.Virtual, SizeBytes: d.CapacityBytes, Partitions: []StatusPartition{}}
		var held int64
		for _, b := range d.blocks {
			if b.name != d.Name {
				held += b.bytes
			}
			m, ok := mounts[b.dev]
			if !ok {
				continue
			}
			p := StatusPartition{Name: b.name, Mount: m.point, Role: partitionRole(m.point), ReadOnly: m.ro, SizeBytes: b.bytes}
			if b.name == d.Name {
				p.SizeBytes = d.CapacityBytes
			}
			props := USBStickProps(r, "/dev/"+b.name)
			if p.Label = unescapeUdev(props["ID_FS_LABEL_ENC"]); p.Label == "" {
				p.Label = props["ID_FS_LABEL"]
			}
			if st, ok := statusStatfs(r, m.point); ok {
				p.TotalBytes, p.UsedBytes = st[0], st[0]-st[1]
			}
			sd.Partitions = append(sd.Partitions, p)
		}
		if len(sd.Partitions) == 0 {
			continue
		}
		if held > 0 && held < sd.SizeBytes {
			sd.UnallocatedBytes = sd.SizeBytes - held
		}
		sort.SliceStable(sd.Partitions, func(i, j int) bool {
			a, b := sd.Partitions[i], sd.Partitions[j]
			if partitionRoleOrder[a.Role] != partitionRoleOrder[b.Role] {
				return partitionRoleOrder[a.Role] < partitionRoleOrder[b.Role]
			}
			return a.Mount < b.Mount
		})
		out = append(out, sd)
	}
	holdsRoot := func(d StatusDevice) bool {
		for _, p := range d.Partitions {
			if p.Role == "root" {
				return true
			}
		}
		return false
	}
	sort.SliceStable(out, func(i, j int) bool {
		if holdsRoot(out[i]) != holdsRoot(out[j]) {
			return holdsRoot(out[i])
		}
		return out[i].Name < out[j].Name
	})
	return out
}
