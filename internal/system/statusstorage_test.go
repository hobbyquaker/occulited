package system

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// occulited task 78: the Status page's storage panels - one per disk with a mounted filesystem,
// its partitions as rings, network shares and tmpfs none of them

const pi4MMC = "platform/emmc2bus/fe340000.mmc/mmc_host/mmc0/mmc0:aaaa"

func (f sysTree) partSize(devpath, disk, part string, sectors int64) {
	f.file("sys/devices/"+devpath+"/block/"+disk+"/"+part+"/size", strconv.FormatInt(sectors, 10))
}

// statfsOf answers statusStatfs from a table of mount point -> {total, free}
func statfsOf(t *testing.T, table map[string][2]int64) {
	old := statusStatfs
	statusStatfs = func(_ Root, point string) ([2]int64, bool) {
		v, ok := table[point]
		return v, ok
	}
	t.Cleanup(func() { statusStatfs = old })
}

func labelsOf(t *testing.T, labels map[string]string) {
	old := USBStickProps
	USBStickProps = func(_ Root, dev string) map[string]string {
		if l, ok := labels[strings.TrimPrefix(dev, "/dev/")]; ok {
			return map[string]string{"ID_FS_LABEL": l, "ID_FS_LABEL_ENC": strings.ReplaceAll(l, " ", `\x20`)}
		}
		return map[string]string{}
	}
	t.Cleanup(func() { USBStickProps = old })
}

func TestStatusStorage(t *testing.T) {
	type part struct {
		name, mount, role, label string
		ro                       bool
		size, total, used        int64
	}
	type dev struct {
		name, kind, vendor, model string
		size, unallocated         int64
		parts                     []part
	}
	cases := []struct {
		name   string
		build  func(t *testing.T) sysTree
		statfs map[string][2]int64
		want   []dev
	}{
		{"the Pi 4's card: boot, root and userfs in one panel, the userfs outermost", func(t *testing.T) sysTree {
			f := pi4Tree(t)
			f.partSize(pi4MMC, "mmcblk0", "mmcblk0p1", 524288)
			f.partSize(pi4MMC, "mmcblk0", "mmcblk0p2", 4194304)
			f.partSize(pi4MMC, "mmcblk0", "mmcblk0p3", 120016895)
			return f
		}, map[string][2]int64{"/": {2000e6, 500e6}, "/boot": {268e6, 200e6}, "/usr/local": {60e9, 50e9}}, []dev{{
			name: "mmcblk0", kind: "sd", vendor: "SanDisk", model: "SN64G", size: 124735488 * 512, unallocated: (124735488 - 524288 - 4194304 - 120016895) * 512,
			parts: []part{
				{name: "mmcblk0p3", mount: "/usr/local", role: "userfs", label: "userfs", size: 120016895 * 512, total: 60e9, used: 10e9},
				{name: "mmcblk0p2", mount: "/", role: "root", label: "rootfs", ro: true, size: 4194304 * 512, total: 2000e6, used: 1500e6},
				{name: "mmcblk0p1", mount: "/boot", role: "boot", label: "bootfs", ro: true, size: 524288 * 512, total: 268e6, used: 68e6},
			},
		}}},
		{"an SD card and a USB stick with two partitions, one mounted; an NFS share and a tmpfs are no disks", func(t *testing.T) sysTree {
			f := newSysTree(t)
			f.noise()
			const mmc = "platform/soc/3f202000.mmc/mmc_host/mmc0/mmc0:0001"
			const usb = "platform/soc/3f980000.usb/usb1/1-1/1-1.3/1-1.3:1.0/host0/target0:0:0/0:0:0:0"
			f.disk("mmcblk0", mmc, 31116288, "179:0", "1 0 8 0 2 0 16 0 0 0 0", map[string]string{"type": "SD", "name": "SC16G", "manfid": "0x000003"},
				map[string]string{"mmcblk0p2": "179:2", "mmcblk0p3": "179:3"})
			f.partSize(mmc, "mmcblk0", "mmcblk0p2", 4194304)
			f.partSize(mmc, "mmcblk0", "mmcblk0p3", 26918912)
			f.disk("sda", usb, 60062500, "8:0", "1 0 8 0 2 0 16 0 0 0 0", map[string]string{"vendor": "SanDisk ", "model": "Ultra Fit       "},
				map[string]string{"sda1": "8:1", "sda2": "8:2"})
			f.partSize(usb, "sda", "sda1", 30000000)
			f.partSize(usb, "sda", "sda2", 30000000)
			f.file("proc/self/mountinfo", strings.Join([]string{
				"22 1 179:2 / / ro - ext4 /dev/root ro",
				// occulited's namespace: the userfs read-only by ProtectSystem, the filesystem not
				"30 22 179:3 / /usr/local ro,nosuid - ext4 /dev/mmcblk0p3 rw",
				"31 22 8:1 / /media/usb1 rw,nosuid - vfat /dev/sda1 ro",
				"32 22 0:53 / /media/backup ro - nfs4 nas:/backup rw",
				"33 22 0:25 / /tmp rw - tmpfs tmpfs rw",
			}, "\n"))
			return f
		}, map[string][2]int64{"/": {2e9, 1e9}, "/usr/local": {13e9, 12e9}, "/media/usb1": {15e9, 5e9}, "/media/backup": {1e12, 1e11}}, []dev{
			{name: "mmcblk0", kind: "sd", vendor: "SanDisk", model: "SC16G", size: 31116288 * 512, unallocated: (31116288 - 4194304 - 26918912) * 512,
				parts: []part{
					{name: "mmcblk0p3", mount: "/usr/local", role: "userfs", label: "userfs", size: 26918912 * 512, total: 13e9, used: 1e9},
					{name: "mmcblk0p2", mount: "/", role: "root", label: "rootfs", ro: true, size: 4194304 * 512, total: 2e9, used: 1e9},
				}},
			{name: "sda", kind: "usb", vendor: "SanDisk", model: "Ultra Fit", size: 60062500 * 512, unallocated: 62500 * 512,
				parts: []part{{name: "sda1", mount: "/media/usb1", role: "other", label: "BACKUP STICK", ro: true, size: 30000000 * 512, total: 15e9, used: 10e9}}},
		}},
		{"a stick without a partition table, and a disk with nothing mounted is left out", func(t *testing.T) sysTree {
			f := newSysTree(t)
			const usb = "pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host1/target1:0:0/1:0:0:0"
			f.disk("sdb", usb, 15625000, "8:16", "1 0 8 0 2 0 16 0 0 0 0", map[string]string{"vendor": "Intenso ", "model": "Rainbow Line"}, nil)
			f.disk("sdc", "pci0000:00/0000:00:1f.2/ata1/host0/target0:0:0/0:0:0:0", 468862128, "8:32", "1 0 8 0 2 0 16 0 0 0 0", map[string]string{"model": "KINGSTON SA400S3"}, map[string]string{"sdc1": "8:33"})
			f.file("proc/self/mountinfo", "31 22 8:16 / /media/usb0 rw - exfat /dev/sdb rw")
			return f
		}, map[string][2]int64{"/media/usb0": {7999e6, 7000e6}}, []dev{
			{name: "sdb", kind: "usb", vendor: "Intenso", model: "Rainbow Line", size: 15625000 * 512,
				parts: []part{{name: "sdb", mount: "/media/usb0", role: "other", size: 15625000 * 512, total: 7999e6, used: 999e6}}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.build(t)
			statfsOf(t, c.statfs)
			labelsOf(t, map[string]string{"mmcblk0p1": "bootfs", "mmcblk0p2": "rootfs", "mmcblk0p3": "userfs", "sda1": "BACKUP STICK"})
			got := []dev{}
			for _, d := range Root(f.root).StatusStorage(storageNow) {
				g := dev{name: d.Name, kind: d.Kind, vendor: d.Vendor, model: d.Model, size: d.SizeBytes, unallocated: d.UnallocatedBytes}
				for _, p := range d.Partitions {
					g.parts = append(g.parts, part{name: p.Name, mount: p.Mount, role: p.Role, label: p.Label, ro: p.ReadOnly, size: p.SizeBytes, total: p.TotalBytes, used: p.UsedBytes})
				}
				got = append(got, g)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("\n got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestPartitionRole(t *testing.T) {
	for mount, want := range map[string]string{"/": "root", "/boot": "boot", "/boot/firmware": "boot", "/usr/local": "userfs", "/media/usb0": "other", "/usr/local/x": "other"} {
		if got := partitionRole(mount); got != want {
			t.Errorf("%s: %s, want %s", mount, got, want)
		}
	}
}
