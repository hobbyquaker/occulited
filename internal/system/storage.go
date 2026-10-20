package system

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// task 69: the Status page's storage health panel. Every disk the box has - the SD card or eMMC
// behind / and /usr/local, a USB or SATA SSD, an NVMe, a VM's virtual disk - with what can be
// known about its health without writing to it: the identity and manufacturing date from sysfs,
// eMMC's own wear estimates, SMART through the privilege helper's one read, the ext4 error
// counters, the kernel's storage errors of this boot, and the writes, with a daily sample in the
// state directory that turns them into writes per day. From those a verdict - good, watch or
// replace - with the reasons that decided it.
//
// Everything under /sys and /proc goes through Root, so a fixture tree tests it; SMART and the
// kernel log are functions a test replaces.

// The verdicts.
const (
	StorageGood    = "good"
	StorageWatch   = "watch"
	StorageReplace = "replace"
)

// The health sources (task 111): who watches the disks' health.
const (
	// HealthDevice: the box reads it itself - SMART, an eMMC's estimates, an SD card's facts.
	HealthDevice = "device"
	// HealthHost: the host watches the disks - a container, the VM product, a box whose disks are
	// all a hypervisor's, or one without smartctl. No SMART is read, and a missing SMART is no error.
	HealthHost = "host"
)

// The thresholds. docs/system-api.md (Storage health) documents each one and why.
const (
	// EMMCLifeWatch and EMMCLifeExceeded are values of the eMMC's DEVICE_LIFE_TIME_EST_TYP_A/B
	// (the kernel's life_time attribute): 0x01 to 0x0A are 0-10 % to 90-100 % of the rated
	// lifetime used, 0x0B is past it. 0x09 is 80-90 %, the same 80 % as the SSD rule below.
	EMMCLifeWatch    = 0x09
	EMMCLifeExceeded = 0x0B
	// EMMCPreEOLWarning and EMMCPreEOLUrgent are PRE_EOL_INFO (pre_eol_info): 0x01 normal, 0x02
	// warning (80 % of the reserved blocks consumed), 0x03 urgent.
	EMMCPreEOLWarning = 0x02
	EMMCPreEOLUrgent  = 0x03
	// WearWatchPercent and WearReplacePercent are the used share of the rated lifetime an SSD
	// reports: NVMe's percentage used, or 100 minus a SATA SSD's remaining-life attribute. 100 %
	// is the SSD twin of eMMC's "exceeded".
	WearWatchPercent   = 80
	WearReplacePercent = 100
	// SmartSectorsWatch: this many reallocated or pending sectors, or NVMe media errors, is a
	// drive that has started to lose blocks.
	SmartSectorsWatch = 1
	// IOErrorsReplace: this many storage errors in the kernel log of this boot.
	IOErrorsReplace = 1
	// Ext4ErrorsWatch: this many errors an ext4 filesystem has ever recorded (errors_count
	// survives reboots and fsck).
	Ext4ErrorsWatch = 1
	// SDAgeWatchYears and SDHeavyWritesPerDay: an SD card at least this old that receives at least
	// this much a day. A box in normal use writes 4 to 17 MB a day (docs/base-scope.md); 500 MB is
	// something else writing - a persistent debug journal, a database addon - and on an old card
	// that is the combination worth watching. SD cards report no wear, so age and load are all
	// there is.
	SDAgeWatchYears     = 5
	SDHeavyWritesPerDay = 500 * 1000 * 1000
	// WriteRateMinDays: the writes-per-day figure takes part in a verdict only once it spans this
	// many days; a figure from the first hours after a boot is mostly the boot.
	WriteRateMinDays = 3
)

const (
	smartReadEvery   = time.Hour       // the SMART read, at most once an hour per device
	kernelScanEvery  = 5 * time.Minute // the kernel log scan
	writeSampleEvery = 23 * time.Hour  // "daily", with an hour's slack for the hourly tick
	writeSampleKeep  = 35 * 24 * time.Hour
	writeRateWindow  = 28 * 24 * time.Hour
	smartTimeout     = 90 * time.Second
	kernelTimeout    = 20 * time.Second
)

// StorageReport is GET /api/system/v1/storage.
type StorageReport struct {
	Verdict string          `json:"verdict"`
	Reasons []StorageReason `json:"reasons"`
	Devices []StorageDevice `json:"devices"`
	Checked time.Time       `json:"checked"`
	// KernelLog: the kernel's messages of this boot could be read. Without them io_errors is
	// unknown, not zero.
	KernelLog bool `json:"kernel_log"`
	// HealthSource is who watches the disks' health (task 111): HealthDevice or HealthHost, where the
	// panel says in one line that the host monitors the disks. It takes no part in the verdict.
	HealthSource string `json:"health_source"`
	// LogFiles are the files written beside the journal (task 84), the largest first, at most 20;
	// LogFilesMore counts the rest. A hint: they take no part in the verdict.
	LogFiles     []LogFile `json:"log_files"`
	LogFilesMore int       `json:"log_files_more,omitempty"`
}

// StorageReason is one fact that decided a verdict. Code names it for the UI's sentence; the
// other fields are that sentence's values.
type StorageReason struct {
	Level       string `json:"level"`
	Code        string `json:"code"`
	Device      string `json:"device"`
	Count       int64  `json:"count,omitempty"`
	Percent     int    `json:"percent,omitempty"`
	Filesystem  string `json:"filesystem,omitempty"`
	Years       int    `json:"years,omitempty"`
	BytesPerDay int64  `json:"bytes_per_day,omitempty"`
}

// The reason codes.
const (
	ReasonSmartFailed   = "smart-failed"
	ReasonEMMCEOLUrgent = "emmc-eol-urgent"
	ReasonEMMCEOLWarn   = "emmc-eol-warning"
	ReasonEMMCLife      = "emmc-life"
	ReasonEMMCLifeOver  = "emmc-life-exceeded"
	ReasonWear          = "wear"
	ReasonWearOver      = "wear-exceeded"
	ReasonIOErrors      = "io-errors"
	ReasonReallocated   = "reallocated"
	ReasonPending       = "pending"
	ReasonMediaErrors   = "media-errors"
	ReasonExt4Errors    = "ext4-errors"
	ReasonSDAgeWrites   = "sd-age-writes"
)

// StorageDevice is one disk.
type StorageDevice struct {
	Name string `json:"name"` // mmcblk0, sda, nvme0n1
	// Kind is sd, emmc, usb, sata, nvme, virtio or other.
	Kind string `json:"kind"`
	// Virtual: a hypervisor's disk (virtio, or a QEMU, VirtualBox, VMware or Hyper-V model on
	// another bus) - its health is the host's, and no SMART is read.
	Virtual       bool   `json:"virtual,omitempty"`
	Vendor        string `json:"vendor,omitempty"`
	VendorID      string `json:"vendor_id,omitempty"` // an SD card's or eMMC's manufacturer id, 0x000003
	Model         string `json:"model,omitempty"`
	CapacityBytes int64  `json:"capacity_bytes"`
	// Manufactured is an SD card's or eMMC's date, YYYY-MM; AgeMonths counts from it.
	Manufactured string             `json:"manufactured,omitempty"`
	AgeMonths    *int               `json:"age_months,omitempty"`
	Mounts       []string           `json:"mounts"`
	EMMC         *EMMCHealth        `json:"emmc,omitempty"`
	Smart        *SmartHealth       `json:"smart,omitempty"`
	Filesystems  []FilesystemHealth `json:"filesystems"`
	// IOErrors counts the kernel's storage error messages of this boot for this disk.
	IOErrors      int64           `json:"io_errors"`
	LastIOError   string          `json:"last_io_error,omitempty"`
	LastIOErrorAt string          `json:"last_io_error_at,omitempty"`
	Writes        StorageWrites   `json:"writes"`
	Verdict       string          `json:"verdict"`
	Reasons       []StorageReason `json:"reasons"`

	partitions []string    // the partitions' names
	blocks     []blockNode // the disk itself and its partitions, for the Status page's panels (task 78)
	mmcHost    string      // mmc0: the host an mmcblk hangs on, which the kernel's host messages name
	serial     string      // part of the identity the write samples are kept under
	lifetimeKB int64       // the sum of the ext4 filesystems' lifetime writes
}

// EMMCHealth is the eMMC's own wear estimate (EXT_CSD). LifeTimeA and LifeTimeB are the raw
// values, 0 when the device does not define them; PreEOL is normal, warning, urgent or "".
type EMMCHealth struct {
	LifeTimeA int    `json:"life_time_a"`
	LifeTimeB int    `json:"life_time_b"`
	PreEOL    string `json:"pre_eol,omitempty"`
}

// SmartHealth is what smartctl -j -H -A -i said.
type SmartHealth struct {
	Available bool  `json:"available"`
	Passed    *bool `json:"passed,omitempty"`
	// Message is smartctl's own complaint when it could not read the device ("Unable to detect
	// device type", a USB bridge without passthrough).
	Message      string    `json:"message,omitempty"`
	Model        string    `json:"model,omitempty"`
	PowerOnHours int64     `json:"power_on_hours,omitempty"`
	WearPercent  *int      `json:"wear_percent,omitempty"`
	Reallocated  *int64    `json:"reallocated_sectors,omitempty"`
	Pending      *int64    `json:"pending_sectors,omitempty"`
	MediaErrors  *int64    `json:"media_errors,omitempty"`
	TemperatureC int       `json:"temperature_c,omitempty"`
	Read         time.Time `json:"read"`
}

// FilesystemHealth is one mounted ext4 filesystem of the disk.
type FilesystemHealth struct {
	Name               string     `json:"name"` // mmcblk0p3
	Mounts             []string   `json:"mounts"`
	Errors             int64      `json:"errors"`
	FirstError         *time.Time `json:"first_error,omitempty"`
	LastError          *time.Time `json:"last_error,omitempty"`
	LifetimeWriteBytes int64      `json:"lifetime_write_bytes"`
}

// StorageWrites: the bytes written since this boot, and per day - over the daily samples
// (basis "samples", PerDayDays the span) or, before there are a day's worth, since the boot
// (basis "boot").
type StorageWrites struct {
	SinceBootBytes int64   `json:"since_boot_bytes"`
	PerDayBytes    int64   `json:"per_day_bytes,omitempty"`
	PerDayDays     float64 `json:"per_day_days,omitempty"`
	PerDayBasis    string  `json:"per_day_basis,omitempty"`
}

// Storage serves the report, keeps the SMART and kernel log reads cached and the write samples
// on disk.
type Storage struct {
	Root Root
	// StateFile keeps the daily write samples, <state>/storage-writes.json; "" = memory only.
	StateFile string
	// Systemd: the kernel log is read from the journal (otherwise dmesg).
	Systemd bool
	// Smartctl returns one device's smartctl JSON; nil = through the privilege helper on a real
	// box (Root "/"), and no SMART anywhere else.
	Smartctl func(ctx context.Context, device string) ([]byte, error)
	// Kernel returns the kernel's error messages of this boot; nil = journalctl or dmesg on a
	// real box, and none anywhere else.
	Kernel func(ctx context.Context) ([]LogLine, error)
	// ListLogs lists the log files of a directory the daemon cannot open, for the hint about log
	// files beside the journal (B-113); nil = through the privilege helper on a real box (Root
	// "/"), and none anywhere else.
	ListLogs func(dir string, allFiles bool) ([]priv.LogFileInfo, error)
	Now      func() time.Time
	Log      *slog.Logger

	mu    sync.Mutex
	smart map[string]*SmartHealth
	// noSmartctl: a SMART read answered that smartctl is not installed (priv.ErrNotAvailable); an
	// image does not grow one while the daemon runs, so it is not asked again
	noSmartctl bool
	kernel     []LogLine
	kernelOK   bool
	kernelAt   time.Time
	samplesMu  sync.Mutex

	// task 84: the log files beside the journal, scanned at most every logScanEvery
	logMu    sync.Mutex
	logAt    time.Time
	logList  []LogFile
	logMore  int
	logSizes map[string]int64
}

func (s *Storage) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Storage) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Report reads everything, applies the verdicts and answers.
func (s *Storage) Report(ctx context.Context) StorageReport {
	// the reads are cached for everyone: a browser that goes away mid-read must not leave an
	// hour of "could not be read" behind
	ctx = context.WithoutCancel(ctx)
	now := s.now()
	devs := s.Root.storageDevices(now)
	lines, ok := s.kernelLines(ctx, now)
	attributeKernelErrors(devs, lines)
	host := s.hostMonitored(devs)
	for i := range devs {
		if !host && smartEligible(devs[i]) {
			devs[i].Smart = s.smartOf(ctx, "/dev/"+devs[i].Name, now)
			if devs[i].Model == "" && devs[i].Smart != nil {
				devs[i].Model = devs[i].Smart.Model
			}
		}
	}
	s.applyWrites(devs, now)
	// a read of this very report may have been the one that found no smartctl
	source := HealthDevice
	if host || s.smartctlMissing() {
		source = HealthHost
	}
	rep := StorageReport{Verdict: StorageGood, Reasons: []StorageReason{}, Devices: devs, Checked: now, KernelLog: ok, HealthSource: source}
	for i := range devs {
		judgeDevice(&devs[i])
		if rank(devs[i].Verdict) > rank(rep.Verdict) {
			rep.Verdict = devs[i].Verdict
		}
	}
	for _, d := range devs {
		if d.Verdict == rep.Verdict {
			rep.Reasons = append(rep.Reasons, d.Reasons...)
		}
	}
	// task 84: a hint beside the verdict, never part of it
	rep.LogFiles, rep.LogFilesMore = s.logFiles(now)
	return rep
}

// Start takes the daily write sample - at once, then every hour it is due - until ctx ends, so
// writes per day build up whether or not anybody opens the Status page.
func (s *Storage) Start(ctx context.Context) {
	go func() {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			now := s.now()
			s.applyWrites(s.Root.storageDevices(now), now)
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

func rank(v string) int {
	switch v {
	case StorageReplace:
		return 2
	case StorageWatch:
		return 1
	}
	return 0
}

// --- sysfs ---

// storageNameRe: the whole disks the panel lists. Partitions, an eMMC's boot areas and RPMB,
// loop, ram, zram, optical drives and device-mapper nodes are not disks of their own.
var storageNameRe = regexp.MustCompile(`^(mmcblk[0-9]+|sd[a-z]{1,2}|nvme[0-9]+n[0-9]+|vd[a-z]{1,2})$`)

var virtualModelRe = regexp.MustCompile(`(?i)\b(QEMU|VBOX|VMware|Virtual disk|Msft Virtual)\b`)

var mmcHostRe = regexp.MustCompile(`/mmc_host/(mmc[0-9]+)/`)

// sysRead is a trimmed sysfs attribute, "" when absent. No helper fallback: every attribute the
// panel reads is world-readable (measured on the rpi3, rpi4 and ova products).
func sysRead(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func sysInt(path string) int64 {
	n, _ := strconv.ParseInt(sysRead(path), 10, 64)
	return n
}

// storageDevices reads the disks from /sys/block, their mounts from this process's mountinfo and
// the ext4 counters, sorted with the mounted disks first.
func (r Root) storageDevices(now time.Time) []StorageDevice {
	entries, err := os.ReadDir(r.join("/sys/block"))
	if err != nil {
		return []StorageDevice{}
	}
	mounts := r.mountsByDev()
	out := []StorageDevice{}
	for _, e := range entries {
		name := e.Name()
		if !storageNameRe.MatchString(name) {
			continue
		}
		base := r.join("/sys/block/" + name)
		size := sysInt(filepath.Join(base, "size"))
		if size <= 0 {
			continue // an empty card reader
		}
		real, err := filepath.EvalSymlinks(base)
		if err != nil {
			real = base
		}
		d := StorageDevice{Name: name, CapacityBytes: size * 512, Mounts: []string{}, Filesystems: []FilesystemHealth{}, Reasons: []StorageReason{}, Verdict: StorageGood}
		dev := filepath.Join(base, "device")
		switch {
		case strings.HasPrefix(name, "mmcblk"):
			switch sysRead(filepath.Join(dev, "type")) {
			case "SD":
				d.Kind = "sd"
			case "MMC":
				d.Kind = "emmc"
			default:
				d.Kind = "other"
			}
			d.Model = sysRead(filepath.Join(dev, "name"))
			d.VendorID = sysRead(filepath.Join(dev, "manfid"))
			d.Vendor = mmcVendor(d.Kind, d.VendorID)
			d.serial = sysRead(filepath.Join(dev, "serial"))
			if m := mmcHostRe.FindStringSubmatch(filepath.ToSlash(real) + "/"); m != nil {
				d.mmcHost = m[1]
			}
			if y, mo, ok := parseMMCDate(sysRead(filepath.Join(dev, "date"))); ok {
				d.Manufactured = fmt.Sprintf("%04d-%02d", y, mo)
				age := (now.Year()*12 + int(now.Month())) - (y*12 + mo)
				if age < 0 {
					age = 0
				}
				d.AgeMonths = &age
			}
			if d.Kind == "emmc" {
				d.EMMC = readEMMC(dev)
			}
		case strings.HasPrefix(name, "nvme"):
			d.Kind = "nvme"
			d.Model = sysRead(filepath.Join(dev, "model"))
			d.serial = sysRead(filepath.Join(dev, "serial"))
		case strings.HasPrefix(name, "vd"):
			d.Kind = "virtio"
		default:
			path := filepath.ToSlash(real)
			switch {
			case strings.Contains(path, "/virtio"):
				d.Kind = "virtio"
			case strings.Contains(path, "/usb"):
				d.Kind = "usb"
			case strings.Contains(path, "/ata"):
				d.Kind = "sata"
			default:
				d.Kind = "other"
			}
			if v := sysRead(filepath.Join(dev, "vendor")); v != "ATA" { // libata's placeholder
				d.Vendor = v
			}
			d.Model = sysRead(filepath.Join(dev, "model"))
		}
		d.Virtual = d.Kind == "virtio" || virtualModelRe.MatchString(d.Vendor+" "+d.Model)
		// the disk itself and each partition: mounts, and ext4's counters for a mounted ext4
		parts := []string{name}
		if ents, err := os.ReadDir(real); err == nil {
			for _, p := range ents {
				if _, err := os.Stat(filepath.Join(real, p.Name(), "partition")); err == nil {
					parts = append(parts, p.Name())
					d.partitions = append(d.partitions, p.Name())
				}
			}
		}
		for _, p := range parts {
			dir := base
			if p != name {
				dir = filepath.Join(real, p)
			}
			devNo := sysRead(filepath.Join(dir, "dev"))
			d.blocks = append(d.blocks, blockNode{name: p, dev: devNo, bytes: sysInt(filepath.Join(dir, "size")) * 512})
			pm := mounts[devNo]
			for _, m := range pm {
				d.Mounts = appendUnique(d.Mounts, m)
			}
			if fs, ok := r.readExt4(p, pm); ok {
				d.Filesystems = append(d.Filesystems, fs)
				d.lifetimeKB += fs.LifetimeWriteBytes / 1024
			}
		}
		sort.Strings(d.Mounts) // "/" before "/boot" before "/usr/local", whatever the partition order
		// field 7 of stat is sectors written, in 512-byte sectors whatever the device's own size
		if f := strings.Fields(sysRead(filepath.Join(base, "stat"))); len(f) > 6 {
			n, _ := strconv.ParseInt(f[6], 10, 64)
			d.Writes.SinceBootBytes = n * 512
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (len(out[i].Mounts) > 0) != (len(out[j].Mounts) > 0) {
			return len(out[i].Mounts) > 0
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// mountsByDev maps "major:minor" to the mount points of this process's mount namespace. Only a
// mount of the filesystem's root counts: the bind mounts systemd adds for the unit's
// ReadWritePaths= and the journal's /var/log/journal are directories of a filesystem that is
// listed already.
func (r Root) mountsByDev() map[string][]string {
	out := map[string][]string{}
	b, err := os.ReadFile(r.join("/proc/self/mountinfo"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[3] != "/" {
			continue
		}
		out[f[2]] = appendUnique(out[f[2]], unescapeMount(f[4]))
	}
	return out
}

// unescapeMount undoes mountinfo's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// readExt4 reads /sys/fs/ext4/<name>, which exists for a mounted ext4 filesystem.
func (r Root) readExt4(name string, mounts []string) (FilesystemHealth, bool) {
	dir := r.join("/sys/fs/ext4/" + name)
	if _, err := os.Stat(filepath.Join(dir, "errors_count")); err != nil {
		return FilesystemHealth{}, false
	}
	fs := FilesystemHealth{Name: name, Mounts: append([]string{}, mounts...), Errors: sysInt(filepath.Join(dir, "errors_count")), LifetimeWriteBytes: sysInt(filepath.Join(dir, "lifetime_write_kbytes")) * 1024}
	if fs.Mounts == nil {
		fs.Mounts = []string{}
	}
	for _, t := range []struct {
		file string
		to   **time.Time
	}{{"first_error_time", &fs.FirstError}, {"last_error_time", &fs.LastError}} {
		if sec := sysInt(filepath.Join(dir, t.file)); sec > 0 {
			at := time.Unix(sec, 0).UTC()
			*t.to = &at
		}
	}
	return fs, true
}

// parseMMCDate reads the CID's date as the kernel prints it, "06/2024".
func parseMMCDate(s string) (year, month int, ok bool) {
	m, y, found := strings.Cut(s, "/")
	if !found {
		return 0, 0, false
	}
	mo, err1 := strconv.Atoi(m)
	yr, err2 := strconv.Atoi(y)
	if err1 != nil || err2 != nil || mo < 1 || mo > 12 || yr < 1990 {
		return 0, 0, false
	}
	return yr, mo, true
}

// readEMMC reads life_time ("0x02 0x01": type A, type B) and pre_eol_info ("0x02"). A plain SD
// card has neither file.
func readEMMC(dev string) *EMMCHealth {
	life := strings.Fields(sysRead(filepath.Join(dev, "life_time")))
	eol := sysRead(filepath.Join(dev, "pre_eol_info"))
	if len(life) == 0 && eol == "" {
		return nil
	}
	h := &EMMCHealth{}
	if len(life) > 0 {
		h.LifeTimeA = parseHexByte(life[0])
	}
	if len(life) > 1 {
		h.LifeTimeB = parseHexByte(life[1])
	}
	switch parseHexByte(eol) {
	case 0x01:
		h.PreEOL = "normal"
	case EMMCPreEOLWarning:
		h.PreEOL = "warning"
	case EMMCPreEOLUrgent:
		h.PreEOL = "urgent"
	}
	return h
}

func parseHexByte(s string) int {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 32)
	if err != nil {
		return 0
	}
	return int(n)
}

// The manufacturer ids of the CID. SD cards (SD Association) and eMMC (JEDEC) number them
// differently. Only the ones widely documented; for an unknown id vendor stays empty and the
// answer's vendor_id carries the id.
var sdVendors = map[int]string{
	0x01: "Panasonic", 0x02: "Toshiba", 0x03: "SanDisk", 0x1b: "Samsung", 0x1d: "ADATA", 0x27: "Phison",
	0x28: "Lexar", 0x31: "Silicon Power", 0x41: "Kingston", 0x74: "Transcend", 0x76: "Patriot", 0x82: "Sony",
}

var emmcVendors = map[int]string{
	0x11: "Toshiba", 0x13: "Micron", 0x15: "Samsung", 0x45: "SanDisk", 0x70: "Kingston", 0x90: "SK hynix", 0xfe: "Micron",
}

func mmcVendor(kind, id string) string {
	n := parseHexByte(id)
	if id == "" {
		return ""
	}
	if kind == "emmc" {
		return emmcVendors[n]
	}
	return sdVendors[n]
}

// --- SMART ---

// hostMonitored says whether the disks' health is the host's to watch (task 111): a container, the
// VM product (PLATFORM=ova in /VERSION), a box whose disks are all a hypervisor's (Virtual), or a
// box where a SMART read found no smartctl. Then no SMART is read at all.
func (s *Storage) hostMonitored(devs []StorageDevice) bool {
	if s.Root.Container() != "" || strings.EqualFold(s.Root.ReadVersion().Platform, "ova") {
		return true
	}
	virtual := 0
	for _, d := range devs {
		if d.Virtual {
			virtual++
		}
	}
	if len(devs) > 0 && virtual == len(devs) {
		return true
	}
	return s.smartctlMissing()
}

func (s *Storage) smartctlMissing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noSmartctl
}

func smartEligible(d StorageDevice) bool {
	switch d.Kind {
	case "usb", "sata", "nvme", "other":
		return !d.Virtual
	}
	return false
}

func (s *Storage) smartOf(ctx context.Context, device string, now time.Time) *SmartHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.smart[device]; ok && now.Sub(h.Read) < smartReadEvery && !now.Before(h.Read) {
		c := *h
		return &c
	}
	if s.noSmartctl {
		return nil
	}
	read := s.Smartctl
	if read == nil {
		if string(s.Root) != "/" || Priv == nil {
			return nil
		}
		read = func(ctx context.Context, dev string) ([]byte, error) {
			r, err := Priv.Smartctl(ctx, dev)
			if err != nil && len(r.Stdout) == 0 {
				return nil, err
			}
			return r.Stdout, nil
		}
	}
	rctx, cancel := context.WithTimeout(ctx, smartTimeout)
	defer cancel()
	b, err := read(rctx, device)
	if errors.Is(err, priv.ErrNotAvailable) {
		// not a failed read: an image without smartmontools, whose host watches the disks
		s.log().Info("storage: no smartctl on this box, the host monitors the disks", "device", device, "err", err)
		s.noSmartctl = true
		return nil
	}
	var h SmartHealth
	if err != nil && len(b) == 0 {
		s.log().Warn("storage: SMART could not be read", "device", device, "err", err)
		h = SmartHealth{Message: err.Error()}
	} else {
		h = ParseSmartctl(b)
	}
	h.Read = now
	if s.smart == nil {
		s.smart = map[string]*SmartHealth{}
	}
	s.smart[device] = &h
	c := h
	return &c
}

type smartctlJSON struct {
	Smartctl struct {
		Messages []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	ModelName    string `json:"model_name"`
	SmartSupport struct {
		Available *bool `json:"available"`
	} `json:"smart_support"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	PowerOnTime struct {
		Hours int64 `json:"hours"`
	} `json:"power_on_time"`
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	ATA struct {
		Table []struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			Value int    `json:"value"`
			Raw   struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMe *struct {
		Temperature    int    `json:"temperature"`
		PercentageUsed *int   `json:"percentage_used"`
		PowerOnHours   int64  `json:"power_on_hours"`
		MediaErrors    *int64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
}

// ataRemaining are the attributes whose normalized value is the remaining life in percent, by id
// and by name together: the same id means something else on another vendor's drive (202 is
// Data_Address_Mark_Errs on some disks).
var ataRemaining = []struct {
	id   int
	name string
}{
	{231, "SSD_Life_Left"}, {233, "Media_Wearout_Indicator"}, {177, "Wear_Leveling_Count"},
	{202, "Percent_Lifetime_Remain"}, {169, "Remaining_Lifetime_Perc"},
}

// ParseSmartctl reads smartctl's JSON. A device without SMART says so (Available false, with
// smartctl's message when it gave one) rather than failing.
func ParseSmartctl(b []byte) SmartHealth {
	var j smartctlJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return SmartHealth{Message: "smartctl gave no readable answer"}
	}
	h := SmartHealth{Model: strings.TrimSpace(j.ModelName), PowerOnHours: j.PowerOnTime.Hours}
	var msgs []string
	for _, m := range j.Smartctl.Messages {
		if m.Severity == "error" && m.String != "" {
			msgs = append(msgs, m.String)
		}
	}
	h.Message = strings.Join(msgs, "; ")
	h.Available = j.SmartStatus != nil || (j.SmartSupport.Available != nil && *j.SmartSupport.Available)
	if j.SmartStatus != nil {
		passed := j.SmartStatus.Passed
		h.Passed = &passed
	}
	if j.Temperature.Current > 0 {
		h.TemperatureC = j.Temperature.Current
	}
	if n := j.NVMe; n != nil {
		h.WearPercent = n.PercentageUsed
		h.MediaErrors = n.MediaErrors
		if h.PowerOnHours == 0 {
			h.PowerOnHours = n.PowerOnHours
		}
		if h.TemperatureC == 0 && n.Temperature > 0 {
			h.TemperatureC = n.Temperature
		}
	}
	attrs := map[int]int{}
	for i, a := range j.ATA.Table {
		attrs[a.ID] = i
	}
	if i, ok := attrs[5]; ok && j.ATA.Table[i].Name == "Reallocated_Sector_Ct" {
		v := j.ATA.Table[i].Raw.Value
		h.Reallocated = &v
	}
	if i, ok := attrs[197]; ok && j.ATA.Table[i].Name == "Current_Pending_Sector" {
		v := j.ATA.Table[i].Raw.Value
		h.Pending = &v
	}
	if h.WearPercent == nil {
		for _, w := range ataRemaining {
			if i, ok := attrs[w.id]; ok && j.ATA.Table[i].Name == w.name && j.ATA.Table[i].Value >= 0 && j.ATA.Table[i].Value <= 100 {
				used := 100 - j.ATA.Table[i].Value
				h.WearPercent = &used
				break
			}
		}
	}
	return h
}

// --- the kernel log ---

var (
	// blk_print_req_error: "I/O error, dev sda, sector 2048 op 0x1:(WRITE) ..."; a passthrough
	// command (smartctl's own) is never printed there
	kernelBlkErrorRe = regexp.MustCompile(`\b(?:I/O|critical medium|critical target|timeout) error, dev ([a-z0-9]+),`)
	kernelBufErrorRe = regexp.MustCompile(`Buffer I/O error on dev ([a-z0-9]+),`)
	// mmc_blk: "mmcblk0: error -110 transferring data, sector ...", "mmcblk0: recovery failed!"
	kernelMMCBlkRe = regexp.MustCompile(`^(mmcblk[0-9]+)(?:p[0-9]+)?: (?:error -?[0-9]+|recovery failed|timed out)`)
	// the host drivers: "mmc0: Timeout waiting for hardware interrupt.", "mmc0: card never left
	// busy state" - attributed through the host the card hangs on, so the Pi's SDIO wifi on mmc1
	// is nobody's disk
	kernelMMCHostRe = regexp.MustCompile(`(?i)^(mmc[0-9]+): .*(?:timeout|timed out|error -?[0-9]+|never left busy|crc)`)
)

func (s *Storage) kernelLines(ctx context.Context, now time.Time) ([]LogLine, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.kernelAt.IsZero() && now.Sub(s.kernelAt) < kernelScanEvery && !now.Before(s.kernelAt) {
		return s.kernel, s.kernelOK
	}
	read := s.Kernel
	if read == nil {
		if string(s.Root) != "/" {
			return nil, false
		}
		read = s.readKernelLog
	}
	rctx, cancel := context.WithTimeout(ctx, kernelTimeout)
	defer cancel()
	lines, err := read(rctx)
	if err != nil {
		s.log().Warn("storage: the kernel log could not be read", "err", err)
	}
	s.kernel, s.kernelOK, s.kernelAt = lines, err == nil, now
	return lines, err == nil
}

// readKernelLog: the kernel's messages of this boot at priority err and above - where every
// storage error is printed - from the journal (the daemon's unit is in systemd-journal), or from
// dmesg on a busybox box.
func (s *Storage) readKernelLog(ctx context.Context) ([]LogLine, error) {
	if !s.Systemd {
		out, err := exec.CommandContext(ctx, "dmesg").Output()
		if err != nil {
			return nil, fmt.Errorf("dmesg: %w", err)
		}
		lines := []LogLine{}
		for _, l := range strings.Split(string(out), "\n") {
			if i := strings.Index(l, "] "); strings.HasPrefix(l, "[") && i > 0 {
				l = l[i+2:]
			}
			if l != "" {
				lines = append(lines, LogLine{Message: l})
			}
		}
		return lines, nil
	}
	cmd := exec.CommandContext(ctx, "journalctl", append([]string{"-k", "-b", "-p", "3", "-o", "json", "--no-pager", "-q", "-n", "5000"}, JournalFileArgs(s.Root)...)...)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && len(out) == 0 && len(ee.Stderr) == 0) {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	lines := []LogLine{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l, ok := ParseJournalLine(sc.Bytes()); ok {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// attributeKernelErrors counts each storage error line against the disk it names - by the disk,
// a partition, or the MMC host the card hangs on. A line about a device that is not a listed disk
// (a loop device, the SDIO wifi) counts nowhere.
func attributeKernelErrors(devs []StorageDevice, lines []LogLine) {
	owner := map[string]int{}
	for i, d := range devs {
		owner[d.Name] = i
		for _, p := range d.partitions {
			owner[p] = i
		}
		if d.mmcHost != "" {
			owner[d.mmcHost] = i
		}
	}
	for _, l := range lines {
		msg := strings.TrimSpace(l.Message)
		var name string
		for _, re := range []*regexp.Regexp{kernelBlkErrorRe, kernelBufErrorRe, kernelMMCBlkRe, kernelMMCHostRe} {
			if m := re.FindStringSubmatch(msg); m != nil {
				name = m[1]
				break
			}
		}
		i, ok := owner[name]
		if name == "" || !ok {
			continue
		}
		devs[i].IOErrors++
		devs[i].LastIOError = msg
		devs[i].LastIOErrorAt = l.Timestamp
	}
}

// --- writes ---

type writeSample struct {
	T          time.Time `json:"t"`
	Boot       string    `json:"boot"`
	BootKB     int64     `json:"boot_kb"`
	LifetimeKB int64     `json:"lifetime_kb"`
}

type writeSeries struct {
	ID      string        `json:"id"`
	Samples []writeSample `json:"samples"`
}

type writeFile struct {
	Devices map[string]*writeSeries `json:"devices"`
}

// identity is what a series belongs to: another card in the same slot starts a new one.
func (d StorageDevice) identity() string {
	return strings.Join([]string{d.Kind, d.VendorID, d.Model, d.serial, strconv.FormatInt(d.CapacityBytes, 10)}, "|")
}

// applyWrites takes the daily sample where one is due, keeps the file and sets each device's
// writes per day.
func (s *Storage) applyWrites(devs []StorageDevice, now time.Time) {
	s.samplesMu.Lock()
	defer s.samplesMu.Unlock()
	wf := writeFile{Devices: map[string]*writeSeries{}}
	if s.StateFile != "" {
		if b, err := os.ReadFile(s.StateFile); err == nil {
			_ = json.Unmarshal(b, &wf)
			if wf.Devices == nil {
				wf.Devices = map[string]*writeSeries{}
			}
		}
	}
	boot := sysRead(s.Root.join("/proc/sys/kernel/random/boot_id"))
	var uptime float64
	if f := strings.Fields(sysRead(s.Root.join("/proc/uptime"))); len(f) > 0 {
		uptime, _ = strconv.ParseFloat(f[0], 64)
	}
	changed := false
	for i := range devs {
		d := &devs[i]
		live := writeSample{T: now.UTC(), Boot: boot, BootKB: d.Writes.SinceBootBytes / 1024, LifetimeKB: d.lifetimeKB}
		ser := wf.Devices[d.Name]
		if ser == nil || ser.ID != d.identity() {
			ser = &writeSeries{ID: d.identity()}
			wf.Devices[d.Name] = ser
			changed = true
		}
		kept := ser.Samples[:0]
		for _, smp := range ser.Samples {
			if now.Sub(smp.T) <= writeSampleKeep && !smp.T.After(now) {
				kept = append(kept, smp)
			}
		}
		if len(kept) != len(ser.Samples) {
			changed = true
		}
		ser.Samples = kept
		if n := len(ser.Samples); n == 0 || now.Sub(ser.Samples[n-1].T) >= writeSampleEvery {
			ser.Samples = append(ser.Samples, live)
			changed = true
		}
		d.Writes.PerDayBytes, d.Writes.PerDayDays, d.Writes.PerDayBasis = writeRate(ser.Samples, live, uptime, now)
	}
	// a series nobody has sampled for the keep period belongs to a disk that is gone
	for name, ser := range wf.Devices {
		if n := len(ser.Samples); n == 0 || now.Sub(ser.Samples[n-1].T) > writeSampleKeep {
			delete(wf.Devices, name)
			changed = true
		}
	}
	if changed && s.StateFile != "" {
		if err := writeJSONFile(s.StateFile, wf); err != nil {
			s.log().Warn("storage: the write samples could not be saved", "err", err)
		}
	}
}

// writeRate is the bytes per day over the samples of the window plus the live reading, or - with
// less than a day of samples - since the boot, once the box has been up an hour.
func writeRate(samples []writeSample, live writeSample, uptimeS float64, now time.Time) (int64, float64, string) {
	pts := []writeSample{}
	for _, smp := range samples {
		if now.Sub(smp.T) <= writeRateWindow {
			pts = append(pts, smp)
		}
	}
	if len(pts) == 0 || !pts[len(pts)-1].T.Equal(live.T) {
		pts = append(pts, live)
	}
	if span := pts[len(pts)-1].T.Sub(pts[0].T); len(pts) >= 2 && span >= 24*time.Hour {
		var kb int64
		for i := 1; i < len(pts); i++ {
			kb += writeDelta(pts[i-1], pts[i])
		}
		days := span.Hours() / 24
		return int64(float64(kb*1024) / days), days, "samples"
	}
	if uptimeS >= 3600 {
		days := uptimeS / 86400
		return int64(float64(live.BootKB*1024) / days), days, "boot"
	}
	return 0, 0, ""
}

// writeDelta is what was written between two samples: the device's counter within one boot
// (it covers every partition and the raw device), ext4's lifetime counter across a reboot (it
// survives one), and at least what the new boot wrote when neither is usable.
func writeDelta(a, b writeSample) int64 {
	if a.Boot == b.Boot && b.BootKB >= a.BootKB {
		return b.BootKB - a.BootKB
	}
	if a.LifetimeKB > 0 && b.LifetimeKB >= a.LifetimeKB {
		return b.LifetimeKB - a.LifetimeKB
	}
	return b.BootKB
}

// --- the verdict ---

// judgeDevice sets the device's verdict and the reasons of that level.
func judgeDevice(d *StorageDevice) {
	var all []StorageReason
	add := func(level, code string, set func(*StorageReason)) {
		r := StorageReason{Level: level, Code: code, Device: d.Name}
		if set != nil {
			set(&r)
		}
		all = append(all, r)
	}
	if sm := d.Smart; sm != nil {
		if sm.Passed != nil && !*sm.Passed {
			add(StorageReplace, ReasonSmartFailed, nil)
		}
		if w := sm.WearPercent; w != nil {
			switch {
			case *w >= WearReplacePercent:
				add(StorageReplace, ReasonWearOver, func(r *StorageReason) { r.Percent = *w })
			case *w >= WearWatchPercent:
				add(StorageWatch, ReasonWear, func(r *StorageReason) { r.Percent = *w })
			}
		}
		for _, c := range []struct {
			v    *int64
			code string
		}{{sm.Reallocated, ReasonReallocated}, {sm.Pending, ReasonPending}, {sm.MediaErrors, ReasonMediaErrors}} {
			if c.v != nil && *c.v >= SmartSectorsWatch {
				n := *c.v
				add(StorageWatch, c.code, func(r *StorageReason) { r.Count = n })
			}
		}
	}
	if e := d.EMMC; e != nil {
		switch e.PreEOL {
		case "urgent":
			add(StorageReplace, ReasonEMMCEOLUrgent, nil)
		case "warning":
			add(StorageWatch, ReasonEMMCEOLWarn, nil)
		}
		life := max(e.LifeTimeA, e.LifeTimeB)
		switch {
		case life >= EMMCLifeExceeded:
			add(StorageReplace, ReasonEMMCLifeOver, nil)
		case life >= EMMCLifeWatch:
			add(StorageWatch, ReasonEMMCLife, func(r *StorageReason) { r.Percent = (life - 1) * 10 })
		}
	}
	if d.IOErrors >= IOErrorsReplace {
		add(StorageReplace, ReasonIOErrors, func(r *StorageReason) { r.Count = d.IOErrors })
	}
	for _, fs := range d.Filesystems {
		if fs.Errors >= Ext4ErrorsWatch {
			where := fs.Name
			if len(fs.Mounts) > 0 {
				where = fs.Mounts[0]
			}
			add(StorageWatch, ReasonExt4Errors, func(r *StorageReason) { r.Count = fs.Errors; r.Filesystem = where })
		}
	}
	if d.Kind == "sd" && d.AgeMonths != nil && *d.AgeMonths >= SDAgeWatchYears*12 &&
		d.Writes.PerDayDays >= WriteRateMinDays && d.Writes.PerDayBytes >= SDHeavyWritesPerDay {
		add(StorageWatch, ReasonSDAgeWrites, func(r *StorageReason) { r.Years = *d.AgeMonths / 12; r.BytesPerDay = d.Writes.PerDayBytes })
	}
	d.Verdict = StorageGood
	for _, r := range all {
		if rank(r.Level) > rank(d.Verdict) {
			d.Verdict = r.Level
		}
	}
	d.Reasons = []StorageReason{}
	for _, r := range all {
		if r.Level == d.Verdict {
			d.Reasons = append(d.Reasons, r)
		}
	}
}
