package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/journald"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/radio"
	"github.com/hobbyquaker/occulited/internal/runlog"
)

// task 41: the radio module's coprocessor firmware - what the image ships under /firmware, what
// the user uploads, and the flash. Upstream flashes once, at boot, at S48UpdateRFHardware,
// before multimacd, rfd and hmipserver have opened the raw UART; at runtime multimacd owns the
// node, so a flash here is an orchestration - stop the stack in the reverse of the boot order,
// read the version, flash through the helper's one narrow operation, read again, re-run the
// detection so /var/hm_mode is true again, start the stack in boot order - and the phases are
// the attempt's lines. Nothing streams (the helper cannot); the page polls.
//
// Task 102 (D-59): the lines go to the journal, one entry each with OCCULITE_RUN=radio-firmware and
// the attempt's OCCULITE_RUN_ID, the flasher's output with OCCULITE_RUN_SOURCE=flasher; the state
// files keep the result and the run id only.

// RadioFirmwareUploadDir is where uploaded files go: on the userfs, inside the helper's
// /usr/local/etc/config/ prefix (no policy change), excluded from the nightly backup by the
// .nobackup file written beside them (createBackup.sh's --exclude-tag).
const RadioFirmwareUploadDir = "/usr/local/etc/config/radio-firmware"

// RadioFirmwareShippedDir is the image's, from the OCCU tarball at build time.
const RadioFirmwareShippedDir = "/firmware"

const (
	radioFirmwareMaxSize  = 4 << 20 // the shipped files are 96-135 KB
	radioFirmwareMaxFiles = 10      // per module directory, uploads only
)

// ForceNoCoproUpdate and ForcedCoproVersion are the two kill switches S48 honours (41.0.8).
const (
	ForceNoCoproUpdate = "/etc/config/force-no-coprocessor-update"
	ForcedCoproVersion = "/etc/config/forced_coprocessor_version"
)

// coproModule describes what S48UpdateRFHardware does for one HM_*_DEV value.
type coproModule struct {
	Dir    string // under /firmware and under the upload directory
	Family string // priv.CoproHmIP, priv.CoproLegacy or priv.CoproHMCFGUSB
	NameRe *regexp.Regexp
	// Version turns the name's match into the version; nil = the first group as it is
	Version func(m []string) string
	// Product is what the raw-uart's device_type starts with, to find the node again after a
	// flash that re-enumerated the stick.
	Product string
	Skip    string // a module the flasher passes over, and why
	// SkipCode names Skip for the UI, which words it in the user's language (occulited task 50)
	SkipCode string
}

var coproModules = map[string]coproModule{
	"HMIP-RFUSB":     {Dir: "HmIP-RFUSB", Family: priv.CoproHmIP, NameRe: regexp.MustCompile(`^dualcopro_update_blhmip-([0-9]+(?:\.[0-9]+)*)\.eq3$`), Product: "eQ-3 HmIP-RFUSB"},
	"RPI-RF-MOD":     {Dir: "RPI-RF-MOD", Family: priv.CoproHmIP, NameRe: regexp.MustCompile(`^dualcopro_update_blhmip-([0-9]+(?:\.[0-9]+)*)\.eq3$`)},
	"HM-MOD-RPI-PCB": {Dir: "HM-MOD-UART", Family: priv.CoproLegacy, NameRe: regexp.MustCompile(`^dualcopro_si1002_update_blhm(?:-([0-9]+(?:\.[0-9]+)*))?\.eq3$`)},
	// the Telekom variant of the stick: eQ-3 publishes no firmware for it, and S48UpdateRFHardware
	// skips it too
	"HMIP-RFUSB-TK": {Skip: "no firmware from eQ-3 for this stick", SkipCode: "no-eq3-firmware"},
	// task 147 (D-100): hmcfgusb's flash-hmcfgusb; eQ-3's file is uploaded, never shipped
	"HM-CFG-USB-2": {Dir: "HM-CFG-USB-2", Family: priv.CoproHMCFGUSB, NameRe: regexp.MustCompile(`^hmusbif\.([0-9a-fA-F]{4})\.enc$`), Version: hexFirmwareVersion},
}

// hexFirmwareVersion is the HM-CFG-USB-2's: hmusbif.03c7.enc is 0x3c7 = 967, version 0.967 - the
// form rfd reports (FIRMWARE_VERSION 967) and the USB descriptor's bcdDevice carries (0967).
func hexFirmwareVersion(m []string) string {
	n, err := strconv.ParseUint(m[1], 16, 32)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%03d", n/1000, n%1000)
}

// versionOf is the file's version from its name's match.
func (cm coproModule) versionOf(m []string) string {
	if cm.Version != nil {
		return cm.Version(m)
	}
	return m[1]
}

// HMCFGUSBFirmwareURL is where the page says the adapter's firmware comes from (D-100: the image
// does not ship eQ-3's file).
const HMCFGUSBFirmwareURL = "https://git.zerfleddert.de/hmcfgusb/firmware/"

var radioFirmwareNameRe = regexp.MustCompile(`^[0-9A-Za-z._-]{1,64}\.(eq3|enc)$`)

// FirmwareFile is one .eq3 on the box.
type FirmwareFile struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Source  string `json:"source"` // shipped or uploaded
	Version string `json:"version,omitempty"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	// Direction names what flashing this file would be against the running version: upgrade,
	// downgrade, same, or unknown (41.0.6: upstream's comparison is a string inequality and
	// downgrades as readily as it upgrades; the UI must say which way it goes).
	Direction string `json:"direction"`
}

// FirmwareModule is one detected module with its files.
type FirmwareModule struct {
	Protocols      []string       `json:"protocols"` // the stacks on this module: BidCos-RF, HmIP-RF
	Device         string         `json:"device"`    // HMIP-RFUSB, RPI-RF-MOD, HM-MOD-RPI-PCB
	DeviceNode     string         `json:"device_node"`
	DeviceType     string         `json:"device_type,omitempty"`
	Family         string         `json:"family,omitempty"`
	Dir            string         `json:"dir,omitempty"`
	RunningVersion string         `json:"running_version"`
	Files          []FirmwareFile `json:"files"`
	Newest         string         `json:"newest,omitempty"` // the newest file's name
	// Verdict: up-to-date, newer-available, older-only (every file is older than what runs),
	// no-file, unknown (no running version), skipped (Note says why), unusable (task 137: an
	// HmIP-RFUSB the radio detection found but could not read - the daemons have no radio on it
	// until it is flashed).
	Verdict string `json:"verdict"`
	Note    string `json:"note,omitempty"`
	// NoteCode names a Note the UI translates (occulited task 50: no-eq3-firmware); empty for
	// the notes it shows as they are
	NoteCode  string `json:"note_code,omitempty"`
	Flashable bool   `json:"flashable"`
}

// FlashAttempt is one run, in flight or finished.
type FlashAttempt struct {
	Module      string     `json:"module"`
	DeviceNode  string     `json:"device_node"`
	File        string     `json:"file"`
	FileVersion string     `json:"file_version"`
	Started     time.Time  `json:"started"`
	Finished    *time.Time `json:"finished,omitempty"`
	OK          bool       `json:"ok"`
	Error       string     `json:"error,omitempty"`
	Before      string     `json:"before,omitempty"` // the version read before the flash
	// Unusable: the module was listed unusable (task 137); a version that cannot be read before
	// the flash is expected then, and the flash goes on, as the boot's S48 did for such a stick.
	Unusable bool   `json:"unusable,omitempty"`
	After    string `json:"after,omitempty"` // and after it
	Exit     int    `json:"exit"`
	// RunID finds the attempt's lines in the journal (task 102).
	RunID string `json:"run_id,omitempty"`
	// run writes the lines; set while the attempt runs.
	run *runlog.Run
}

// RadioFirmwareStatus is GET /radio/firmware.
type RadioFirmwareStatus struct {
	Modules       []FirmwareModule `json:"modules"`
	ForceNoUpdate bool             `json:"force_no_update"`
	ForcedVersion string           `json:"forced_version"`
	// FirmwareStaged: a system update is staged (lite-rf-stop hands the coprocessors to the
	// bootloader in that state); a flash is refused until it is installed or discarded.
	FirmwareStaged bool          `json:"firmware_staged"`
	Busy           bool          `json:"radio_busy"`
	BusyInterface  string        `json:"radio_busy_interface,omitempty"`
	UploadDir      string        `json:"upload_dir"`
	Running        *FlashAttempt `json:"running"`
	Last           *FlashAttempt `json:"last"`
}

// BusyChecker is the radio sampler's "do not flash now" (health.Sampler.Busy).
type BusyChecker interface {
	Busy(threshold int) (bool, string)
}

// ErrFlashRunning: one flash at a time.
var ErrFlashRunning = errors.New("a coprocessor flash is already running")

// RadioFirmware is the service.
type RadioFirmware struct {
	Root     Root
	Services ServiceManager
	Systemd  bool
	Health   BusyChecker // nil = never busy
	// ConnBusy: a radio connection change holds the stack (RadioConnections.Busy); nil = never.
	ConnBusy func() bool
	StateDir string // <state>/radio-firmware: running.json while a flash runs, last.json after
	Log      *slog.Logger
	// Journal takes the attempts' lines (task 102); nil = they go to Log with their run id.
	Journal runlog.Sender
	Now     func() time.Time
	// Poll and Wait bound the waits for the node to become free and to come back; the tests
	// shorten them.
	Poll time.Duration
	Wait time.Duration

	mu      sync.Mutex
	running *FlashAttempt
	last    *FlashAttempt
}

// the units that hold the radio, in boot order (S60 multimacd, S61 rfd, S62 hmipserver); they
// stop in the reverse. hs485d is not among them: BidCos-Wired sits on its own device and an
// After= ordering in multimacd.service is not a claim on the raw UART.
var radioUnits = []string{"multimacd", "rfd", "hmipserver"}

func (s *RadioFirmware) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *RadioFirmware) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *RadioFirmware) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return 500 * time.Millisecond
}

func (s *RadioFirmware) wait() time.Duration {
	if s.Wait > 0 {
		return s.Wait
	}
	return 30 * time.Second
}

// Load reads the last attempt from the state directory and closes a run that was in flight when
// occulited last stopped: marked failed, and the stack ensured up - the daemons must never be
// left stopped by a crash (41.5).
func (s *RadioFirmware) Load(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last FlashAttempt
	if b, err := os.ReadFile(filepath.Join(s.StateDir, "last.json")); err == nil && json.Unmarshal(b, &last) == nil {
		s.last = &last
		// a file from before task 102 kept the lines: into the journal, and the file without them
		if kept := keptLines(b); kept != nil {
			s.moveKept(&last, kept)
			s.saveLast(&last)
		}
	}
	var stale FlashAttempt
	if b, err := os.ReadFile(filepath.Join(s.StateDir, "running.json")); err == nil && json.Unmarshal(b, &stale) == nil {
		now := s.now()
		stale.Finished = &now
		stale.OK = false
		stale.Error = "occulited was restarted while the flash was running; the outcome is unknown"
		s.moveKept(&stale, keptLines(b))
		stale.run.Err("occulited restarted: the attempt is closed as failed, the radio daemons are started")
		s.last = &stale
		s.saveLast(&stale)
		_ = os.Remove(filepath.Join(s.StateDir, "running.json"))
		s.log().Warn("radio firmware: a flash was running when occulited stopped; starting the radio daemons", "module", stale.Module)
		_, _ = openRadioGate(s.Root, s.log()) // B-308: the interrupted flash's hold on the radio units
		if s.Services != nil {
			for _, u := range radioUnits {
				if _, err := s.Services.Control(ctx, u, "start"); err != nil {
					s.log().Warn("radio firmware: start after the interrupted flash failed", "unit", u, "err", err)
				}
			}
		}
	}
}

// keptLines are the lines an attempt file carried before task 102; nil when it has none.
func keptLines(b []byte) []string {
	var old struct {
		Lines []string `json:"lines"`
	}
	_ = json.Unmarshal(b, &old)
	return old.Lines
}

// moveKept gives an attempt from a state file its run - a run id of its start when it has none -
// and writes the lines it kept into the journal.
func (s *RadioFirmware) moveKept(a *FlashAttempt, kept []string) {
	if a.RunID == "" {
		a.RunID = runlog.NewID(a.Started)
	}
	a.run = s.newRun(a)
	for _, l := range kept {
		a.run.Line(runlog.KeptPriority(l), l)
	}
	if len(kept) > 0 {
		s.log().Info("radio firmware: an attempt's lines moved from its state file into the journal", "run_id", a.RunID, "lines", len(kept))
	}
}

// newRun is the journal writer of an attempt's run.
func (s *RadioFirmware) newRun(a *FlashAttempt) *runlog.Run {
	return runlog.New(runlog.KindRadioFirmware, a.RunID, runlog.Options{
		Sender: s.Journal,
		Log:    s.log(),
		Fields: []journald.Field{{Key: "OCCULITE_RUN_MODULE", Value: a.Module}, {Key: "OCCULITE_RUN_FILE", Value: filepath.Base(a.File)}},
	})
}

func (s *RadioFirmware) saveLast(a *FlashAttempt) {
	if err := writeJSONFile(filepath.Join(s.StateDir, "last.json"), a); err != nil {
		s.log().Warn("radio firmware: the attempt could not be recorded", "err", err)
	}
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Status assembles the answer.
func (s *RadioFirmware) Status() RadioFirmwareStatus {
	s.mu.Lock()
	running, last := copyFlash(s.running), copyFlash(s.last)
	s.mu.Unlock()
	st := RadioFirmwareStatus{Modules: s.modules(last), UploadDir: RadioFirmwareUploadDir, Running: running, Last: last}
	if _, err := os.Stat(s.Root.join(ForceNoCoproUpdate)); err == nil {
		st.ForceNoUpdate = true
	}
	st.ForcedVersion = strings.TrimSpace(readFile(s.Root.join(ForcedCoproVersion)))
	st.FirmwareStaged = s.firmwareStaged()
	if s.Health != nil {
		st.Busy, st.BusyInterface = s.Health.Busy(80)
	}
	return st
}

func (s *RadioFirmware) firmwareStaged() bool {
	for _, m := range []string{"/usr/local/.firmwareUpdate", "/usr/local/.recoveryMode"} {
		if _, err := os.Lstat(s.Root.join(m)); err == nil {
			return true
		}
	}
	return false
}

func copyFlash(a *FlashAttempt) *FlashAttempt {
	if a == nil {
		return nil
	}
	c := *a
	return &c
}

// modules groups /var/hm_mode by device node: a dual module (the RFUSB, the RPI-RF-MOD) serves
// both stacks through one node and is one module here.
func (s *RadioFirmware) modules(last *FlashAttempt) []FirmwareModule {
	kv := s.Root.HMMode()
	out := []FirmwareModule{}
	byNode := map[string]int{}
	for _, p := range []struct{ prefix, proto string }{{"HM_HMRF", "BidCos-RF"}, {"HM_HMIP", "HmIP-RF"}} {
		dev := kv[p.prefix+"_DEV"]
		if dev == "" || dev == "HM-CFG-USB-2" {
			// the adapter has no node; it is listed from sysfs below
			continue
		}
		node := kv[p.prefix+"_DEVNODE"]
		key := dev + "|" + node
		if i, ok := byNode[key]; ok {
			out[i].Protocols = append(out[i].Protocols, p.proto)
			continue
		}
		m := FirmwareModule{Protocols: []string{p.proto}, Device: dev, DeviceNode: node, DeviceType: kv[p.prefix+"_DEVTYPE"], RunningVersion: kv[p.prefix+"_VERSION"], Files: []FirmwareFile{}}
		byNode[key] = len(out)
		out = append(out, m)
	}
	for _, a := range s.usbAdapters() {
		m := FirmwareModule{Protocols: []string{}, Device: "HM-CFG-USB-2", DeviceNode: "usb:" + a.Serial, DeviceType: "USB", RunningVersion: a.Version, Files: []FirmwareFile{}}
		if kv["HM_HMRF_DEV"] == "HM-CFG-USB-2" {
			m.Protocols = []string{"BidCos-RF"}
		}
		if a.Bootloader {
			m.Note = "the adapter is in its bootloader (an interrupted flash?): flashing a file brings it back"
		}
		out = append(out, m)
	}
	// task 137: an HmIP-RFUSB the detection found on a raw-uart node but could not read is in no
	// role - /var/hm_mode does not name it - and the daemons have no radio on it
	known := map[string]bool{}
	for _, m := range out {
		known[m.DeviceNode] = true
	}
	unusable := s.unusableSticks(known)
	out = append(out, unusable...)
	for i := range out {
		s.fillModule(&out[i], last)
		if i >= len(out)-len(unusable) && !(last != nil && last.OK && last.DeviceNode == out[i].DeviceNode && last.Unusable) {
			out[i].Verdict = "unusable"
			out[i].Note = "the radio detection found the stick but could not read its firmware; it carries no radio until it is flashed"
		}
	}
	return out
}

// unusableSticks are the HmIP-RFUSBs in the radio detection's result (modules.json, the same
// device_type test as S48UpdateRFHardware's "HmIP-RFUSB@") whose probe did not answer, on a
// node no role uses. A stick that answered and is simply not chosen (a connection pinned to
// another module) is not one of them.
func (s *RadioFirmware) unusableSticks(known map[string]bool) []FirmwareModule {
	var det radio.Detection
	b, err := os.ReadFile(s.Root.join(radio.RunDir + "/modules.json"))
	if err != nil || json.Unmarshal(b, &det) != nil {
		return nil
	}
	var out []FirmwareModule
	for _, m := range det.Modules {
		if m.OK() || m.Node == "" || known[m.Node] || !strings.Contains(m.DeviceType, "HmIP-RFUSB@") || strings.Contains(m.DeviceType, "HmIP-RFUSB-TK") {
			continue
		}
		out = append(out, FirmwareModule{Protocols: []string{}, Device: "HMIP-RFUSB", DeviceNode: m.Node, DeviceType: m.DeviceType, Files: []FirmwareFile{}})
	}
	return out
}

// usbAdapter is an HM-CFG-USB-2 on the USB bus.
type usbAdapter struct {
	Serial     string
	Version    string // from bcdDevice, "" in the bootloader
	Bootloader bool   // 1b1f:c010
}

// usbAdapters lists the HM-CFG-USB-2s from sysfs: the application (1b1f:c00f) with its firmware
// version from bcdDevice (0967 = 0.967), and one in its bootloader (1b1f:c010).
func (s *RadioFirmware) usbAdapters() []usbAdapter {
	var out []usbAdapter
	dirs, _ := filepath.Glob(s.Root.join("/sys/bus/usb/devices/*"))
	sort.Strings(dirs)
	for _, d := range dirs {
		if strings.TrimSpace(readFile(filepath.Join(d, "idVendor"))) != "1b1f" {
			continue
		}
		prod := strings.TrimSpace(readFile(filepath.Join(d, "idProduct")))
		serial := strings.TrimSpace(readFile(filepath.Join(d, "serial")))
		if (prod != "c00f" && prod != "c010") || !priv.HMCFGUSBDevice.MatchString("usb:"+serial) {
			continue
		}
		a := usbAdapter{Serial: serial, Bootloader: prod == "c010"}
		if !a.Bootloader {
			a.Version = bcdVersion(readFile(filepath.Join(d, "bcdDevice")))
		}
		out = append(out, a)
	}
	return out
}

// usbAdapter is the adapter with the serial of a usb:<serial> node, or nil.
func (s *RadioFirmware) usbAdapter(node string) *usbAdapter {
	for _, a := range s.usbAdapters() {
		if "usb:"+a.Serial == node {
			return &a
		}
	}
	return nil
}

// bcdVersion reads a bcdDevice of the form 0967 as 0.967.
func bcdVersion(bcd string) string {
	bcd = strings.TrimSpace(bcd)
	n, err := strconv.Atoi(bcd)
	if err != nil || len(bcd) != 4 {
		return ""
	}
	return fmt.Sprintf("%d.%03d", n/1000, n%1000)
}

func (s *RadioFirmware) fillModule(m *FirmwareModule, last *FlashAttempt) {
	// a successful flash whose "after" the boot-time snapshot does not know yet (the detection
	// unit's re-run failed, or a busybox box): the read version is the true one
	if last != nil && last.OK && last.Module == m.Device && last.DeviceNode == m.DeviceNode && last.After != "" && m.RunningVersion == last.Before && last.Before != last.After {
		m.RunningVersion = last.After
		m.Note = "the version was read after the flash; /var/hm_mode still carries the boot-time one"
	}
	cm, ok := coproModules[m.Device]
	switch {
	case !ok:
		m.Verdict = "skipped"
		m.Note = "no coprocessor flasher is known for this module"
		return
	case cm.Skip != "":
		m.Verdict = "skipped"
		m.Note, m.NoteCode = cm.Skip, cm.SkipCode
		return
	}
	m.Family, m.Dir = cm.Family, cm.Dir
	m.Files = s.files(cm)
	for i := range m.Files {
		m.Files[i].Direction = direction(m.Files[i].Version, m.RunningVersion)
	}
	m.Flashable = m.DeviceNode != ""
	newest := -1
	for i, f := range m.Files {
		if f.Version == "" {
			continue
		}
		if newest < 0 || compareVersions(f.Version, m.Files[newest].Version) > 0 {
			newest = i
		}
	}
	switch {
	case len(m.Files) == 0:
		m.Verdict = "no-file"
	case newest < 0:
		m.Verdict = "unknown"
	case m.RunningVersion == "":
		m.Newest = m.Files[newest].Name
		m.Verdict = "unknown"
	default:
		m.Newest = m.Files[newest].Name
		switch c := compareVersions(m.Files[newest].Version, m.RunningVersion); {
		case c > 0:
			m.Verdict = "newer-available"
		case c == 0:
			m.Verdict = "up-to-date"
		default:
			m.Verdict = "older-only"
		}
	}
}

// files lists the module's .eq3 files, shipped then uploaded, each with its version, size and
// SHA-256. A shipped file without a version in its name (the legacy
// dualcopro_si1002_update_blhm.eq3) takes it from the directory's fwmap, as S48 does.
func (s *RadioFirmware) files(cm coproModule) []FirmwareFile {
	out := []FirmwareFile{}
	for _, src := range []struct{ base, source string }{{RadioFirmwareShippedDir, "shipped"}, {RadioFirmwareUploadDir, "uploaded"}} {
		dir := s.Root.join(filepath.Join(src.base, cm.Dir))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		fwmap := parseFwmap(readFile(filepath.Join(dir, "fwmap")))
		for _, e := range entries {
			name := e.Name()
			m := cm.NameRe.FindStringSubmatch(name)
			if m == nil || e.IsDir() {
				continue
			}
			f := FirmwareFile{Name: name, Path: filepath.Join(src.base, cm.Dir, name), Source: src.source, Version: cm.versionOf(m)}
			if f.Version == "" {
				f.Version = fwmap[name]
			}
			if st, err := e.Info(); err == nil {
				f.Size = st.Size()
			}
			f.SHA256 = fileSHA256(filepath.Join(dir, name))
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source == "shipped"
		}
		return compareVersions(out[i].Version, out[j].Version) > 0
	})
	return out
}

// parseFwmap reads "CCU2 <file> <version>" lines (comments and blanks skipped).
func parseFwmap(text string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Fields(l)
		if len(f) >= 3 {
			out[f[1]] = f[2]
		}
	}
	return out
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// compareVersions orders dotted numbers ("4.4.18" < "4.4.22" < "4.10.0"); a non-numeric part
// compares as a string, an empty version sorts lowest.
// CompareVersions compares two dotted versions as the flasher's check does: <0, 0, >0.
func CompareVersions(a, b string) int { return compareVersions(a, b) }

func compareVersions(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return -1
	}
	if b == "" {
		return 1
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil:
			if nx != ny {
				if nx < ny {
					return -1
				}
				return 1
			}
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	return 0
}

func direction(file, running string) string {
	if file == "" || running == "" {
		return "unknown"
	}
	switch c := compareVersions(file, running); {
	case c > 0:
		return "upgrade"
	case c < 0:
		return "downgrade"
	}
	return "same"
}

// moduleFor is the detected module named dev, or an error the API can show.
func (s *RadioFirmware) moduleFor(dev string) (FirmwareModule, coproModule, error) {
	cm, ok := coproModules[dev]
	if !ok || cm.Skip != "" {
		return FirmwareModule{}, cm, fmt.Errorf("no firmware handling for module %q", dev)
	}
	for _, m := range s.modules(nil) {
		if m.Device == dev {
			return m, cm, nil
		}
	}
	return FirmwareModule{}, cm, fmt.Errorf("module %q is not detected on this system", dev)
}

// Upload stores an .eq3 for the module under the upload directory: the name checked against the
// module's own pattern (so the version can be read from it), 4 MiB at most, ten files per
// module, staged as the daemon's user and moved into place by the helper.
func (s *RadioFirmware) Upload(dev, name string, r io.Reader) (FirmwareFile, error) {
	_, cm, err := s.moduleFor(dev)
	if err != nil {
		return FirmwareFile{}, err
	}
	name = filepath.Base(name)
	if !radioFirmwareNameRe.MatchString(name) {
		return FirmwareFile{}, errors.New("the file name must be letters, digits, dot, dash or underscore and end in .eq3 (.enc for the HM-CFG-USB-2)")
	}
	m := cm.NameRe.FindStringSubmatch(name)
	if m == nil {
		return FirmwareFile{}, fmt.Errorf("the name does not fit this module's firmware files (%s)", cm.NameRe.String())
	}
	version := cm.versionOf(m)
	if version == "" {
		return FirmwareFile{}, errors.New("the name carries no version; name the file <name>-<version>.eq3")
	}
	dir := s.Root.join(filepath.Join(RadioFirmwareUploadDir, cm.Dir))
	if entries, err := os.ReadDir(dir); err == nil {
		n := 0
		for _, e := range entries {
			if radioFirmwareNameRe.MatchString(e.Name()) && e.Name() != name {
				n++
			}
		}
		if n >= radioFirmwareMaxFiles {
			return FirmwareFile{}, fmt.Errorf("at most %d uploaded files per module; delete one first", radioFirmwareMaxFiles)
		}
	}
	staged, size, err := stageFile(s.Root, "radio-firmware-"+name, io.LimitReader(r, radioFirmwareMaxSize+1))
	if err != nil {
		return FirmwareFile{}, err
	}
	defer os.Remove(staged)
	if size > radioFirmwareMaxSize {
		return FirmwareFile{}, fmt.Errorf("the file is larger than %d MiB", radioFirmwareMaxSize>>20)
	}
	if size == 0 {
		return FirmwareFile{}, errors.New("the file is empty")
	}
	if err := Priv.MkdirAll(dir, 0o755); err != nil {
		return FirmwareFile{}, err
	}
	// the nightly backup tars /usr/local with --exclude-tag=.nobackup: firmware images stay out
	if err := Priv.Touch(filepath.Join(dir, ".nobackup"), 0o644); err != nil {
		return FirmwareFile{}, err
	}
	dest := filepath.Join(dir, name)
	if err := Priv.Rename(staged, dest); err != nil {
		return FirmwareFile{}, err
	}
	_ = Priv.Chmod(dest, 0o644)
	return FirmwareFile{Name: name, Path: filepath.Join(RadioFirmwareUploadDir, cm.Dir, name), Source: "uploaded", Version: version, Size: size, SHA256: fileSHA256(dest)}, nil
}

// Delete removes an uploaded file; a shipped one is never touched.
func (s *RadioFirmware) Delete(dev, name string) error {
	cm, ok := coproModules[dev]
	if !ok || cm.Dir == "" {
		return fmt.Errorf("no firmware handling for module %q", dev)
	}
	name = filepath.Base(name)
	if !radioFirmwareNameRe.MatchString(name) {
		return errors.New("bad file name")
	}
	path := s.Root.join(filepath.Join(RadioFirmwareUploadDir, cm.Dir, name))
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s: not an uploaded file", name)
	}
	return Priv.Remove(path)
}

// Flash starts the run in the background; the page polls Status. Refused when one is running,
// when the radio is busy, when the boot-time flasher is switched off, when a system update is
// staged, or when the file is not one of the module's.
func (s *RadioFirmware) Flash(dev, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != nil {
		return ErrFlashRunning
	}
	if s.ConnBusy != nil && s.ConnBusy() {
		return ErrConnApplyRunning
	}
	if _, err := os.Stat(s.Root.join(ForceNoCoproUpdate)); err == nil {
		return errors.New(ForceNoCoproUpdate + " exists: coprocessor updates are switched off on this system")
	}
	if s.firmwareStaged() {
		return errors.New("a system update is staged; install or discard it first")
	}
	if s.Health != nil {
		if busy, which := s.Health.Busy(80); busy {
			return fmt.Errorf("the radio is busy (%s): wait for the duty cycle to drop", which)
		}
	}
	m, cm, err := s.moduleFor(dev)
	if err != nil {
		return err
	}
	if !m.Flashable {
		return fmt.Errorf("module %s has no device node", dev)
	}
	var file *FirmwareFile
	for i := range m.Files {
		if m.Files[i].Name == filepath.Base(name) {
			file = &m.Files[i]
		}
	}
	if file == nil {
		return fmt.Errorf("%s is not a firmware file of module %s", name, dev)
	}
	if file.Version == "" {
		return fmt.Errorf("%s carries no version; it cannot be checked after the flash", name)
	}
	now := s.now()
	a := &FlashAttempt{Module: dev, DeviceNode: m.DeviceNode, File: file.Path, FileVersion: file.Version, Started: now, RunID: runlog.NewID(now), Unusable: m.Verdict == "unusable"}
	a.run = s.newRun(a)
	s.running = a
	if err := writeJSONFile(filepath.Join(s.StateDir, "running.json"), a); err != nil {
		s.log().Warn("radio firmware: the running attempt could not be recorded", "err", err)
	}
	go s.run(a, cm)
	return nil
}

// line, warn and fail write one of the run's lines to the journal at its priority.
func (s *RadioFirmware) line(a *FlashAttempt, l string) { a.run.Info(l) }
func (s *RadioFirmware) warn(a *FlashAttempt, l string) { a.run.Warn(l) }
func (s *RadioFirmware) fail(a *FlashAttempt, l string) { a.run.Err(l) }

// FlashLimit bounds one flash, from the stop to the stack's start (the gate's deadline is a
// minute more).
const FlashLimit = 15 * time.Minute

// run is the background half: the sequence of 41.3, the failure handling of 41.5.
func (s *RadioFirmware) run(a *FlashAttempt, cm coproModule) {
	ctx, cancel := context.WithTimeout(context.Background(), FlashLimit)
	defer cancel()
	err := s.attempt(ctx, a, cm)
	if err != nil {
		s.fail(a, "failed: "+err.Error())
		s.log().Warn("radio firmware: the flash failed", "module", a.Module, "file", a.File, "run_id", a.RunID, "err", err)
	} else {
		s.line(a, "done: the coprocessor runs "+a.After)
		s.log().Info("radio firmware: flashed", "module", a.Module, "file", a.File, "before", a.Before, "after", a.After, "run_id", a.RunID)
	}
	// the files settle before the flash counts as finished: a reader that sees no running flash
	// finds its last.json and no running.json (TestRadioFirmwareNodeGone's temporary directory was
	// still being written when the test ended, on Gitea's runner)
	s.mu.Lock()
	now := s.now()
	a.Finished = &now
	a.OK = err == nil
	if err != nil {
		a.Error = err.Error()
	}
	s.saveLast(a)
	_ = os.Remove(filepath.Join(s.StateDir, "running.json"))
	s.running, s.last = nil, a
	s.mu.Unlock()
}

func (s *RadioFirmware) attempt(ctx context.Context, a *FlashAttempt, cm coproModule) (err error) {
	// which of the radio units are up now: those stop, and those start again - whatever happens
	running := s.runningUnits()
	stopped := []string{}
	// the HM-CFG-USB-2 is rfd's alone and is in no plan decision beyond "it is there": its flash
	// stops rfd only and needs no re-detection - multimacd and hmipserver keep the module they
	// hold (a re-detection resets it, and hmipserver direct on an HM-MOD-RPI-PCB did not come up
	// again after that on .170)
	units := radioUnits
	if cm.Family == priv.CoproHMCFGUSB {
		units = []string{"rfd"}
	}
	// openccu-lite B-308: from before the stop until the stack is back no radio unit starts from
	// outside the flash (a user's systemctl, a Restart=) - it would open the module being flashed,
	// or start on the files of the detection that runs after it. The stack's own starts below let
	// each unit through first, in boot order; the gate opens after them and on every way out, and
	// its deadline is the flash's limit and a minute.
	gated := false
	if gerr := closeRadioGate(s.Root, FlashLimit+time.Minute); gerr != nil {
		s.warn(a, "the radio units could not be held against starts from outside the flash: "+gerr.Error())
	} else {
		gated = true
		defer func() { _, _ = openRadioGate(s.Root, s.log()) }()
		s.line(a, "radio units held against starts from outside the flash")
	}
	defer func() {
		// 6. the stack comes back in boot order, on success and on failure alike: a stopped
		// stack is worse than a failed flash
		for _, u := range units {
			if !containsUnit(stopped, u) {
				continue
			}
			if gated {
				releaseRadioGate(s.Root, u, s.log())
			}
			if _, cerr := s.Services.Control(ctx, u, "start"); cerr != nil {
				s.fail(a, "starting "+u+" failed: "+cerr.Error())
				if err == nil {
					err = fmt.Errorf("the flash succeeded but %s did not start: %w", u, cerr)
				}
				continue
			}
			s.line(a, u+" started")
		}
		if gated {
			if gone, _ := openRadioGate(s.Root, s.log()); gone {
				s.line(a, "the flash's starts are done: radio units may start from outside it again")
			}
			// a unit the flash did not stop whose Restart= the gate refused meanwhile
			var others []string
			for _, u := range radio.Daemons {
				if !containsUnit(stopped, u) {
					others = append(others, u)
				}
			}
			for _, h := range startHeldBack(ctx, s.Services, s.Root, others) {
				if h.err != nil {
					s.warn(a, "starting "+h.unit+" (held back during the flash) failed: "+h.err.Error())
				} else {
					s.line(a, h.unit+" started: its start during the flash was held back")
				}
			}
		}
		if containsUnit(stopped, "multimacd") {
			if s.waitFor(ctx, func() bool { return s.mmdNodesPresent() }) {
				s.line(a, "/dev/mmd_bidcos and /dev/mmd_hmip are back")
			} else {
				s.warn(a, "the /dev/mmd_* nodes did not appear in time (multimacd may not be required on this system)")
			}
		}
	}()
	// 2. stop the stack in the reverse of the boot order
	for i := len(units) - 1; i >= 0; i-- {
		u := units[i]
		if !running[u] {
			s.line(a, u+" is not running")
			continue
		}
		if _, err := s.Services.Control(ctx, u, "stop"); err != nil {
			return fmt.Errorf("stopping %s: %w", u, err)
		}
		stopped = append(stopped, u)
		s.line(a, u+" stopped")
	}
	if !s.waitFor(ctx, func() bool { return !s.mmdNodesPresent() && s.rawUARTFree(a.DeviceNode) }) {
		return fmt.Errorf("%s is still held after the daemons stopped (open_count or /dev/mmd_* remain)", a.DeviceNode)
	}
	s.line(a, a.DeviceNode+" is free")
	// 3. the version before
	before, out, err := s.readVersion(ctx, cm, a.DeviceNode)
	if err != nil && a.Unusable {
		// task 137: the stick was found without a readable firmware - that is why it is flashed
		s.appendOutput(a, "version-read", out)
		s.warn(a, "the coprocessor reports no readable version (the stick was found unusable); flashing it all the same, as the boot did")
		before, err = "", nil
	}
	if err != nil {
		s.appendOutput(a, "version-read", out)
		return fmt.Errorf("reading the version before the flash: %w", err)
	}
	s.setAttempt(func() { a.Before = before })
	s.line(a, "coprocessor reports "+orUnknown(before))
	// 4. the flash, through the helper's one operation
	s.line(a, "flashing "+filepath.Base(a.File)+" ("+a.FileVersion+", "+cm.Family+")")
	res, err := Priv.FlashCoprocessor(ctx, cm.Family, a.DeviceNode, s.Root.join(a.File), a.FileVersion)
	s.appendOutput(a, "flasher", res.Combined())
	if err != nil {
		return fmt.Errorf("the flasher could not be run: %w", err)
	}
	s.setAttempt(func() { a.Exit = res.Exit })
	s.line(a, "flasher exited "+strconv.Itoa(res.Exit))
	// 5. the version after; S48 gives the coprocessor application a second to come up
	s.sleep(ctx, time.Second)
	node := a.DeviceNode
	if cm.Family == priv.CoproHMCFGUSB {
		// the adapter went through its bootloader and back: wait for its application by serial
		if !s.waitFor(ctx, func() bool { ad := s.usbAdapter(node); return ad != nil && !ad.Bootloader }) {
			return fmt.Errorf("the HM-CFG-USB-2 %s did not come back after the flash", strings.TrimPrefix(node, "usb:"))
		}
	} else if !s.rawUARTExists(node) {
		s.line(a, node+" is gone: waiting for the module to enumerate again")
		if !s.waitFor(ctx, func() bool { return s.rawUARTExists(node) || s.findNode(cm) != "" }) {
			return fmt.Errorf("%s did not come back after the flash", node)
		}
		if !s.rawUARTExists(node) {
			node = s.findNode(cm)
			s.line(a, "the module is on "+node+" now")
		}
	}
	after, out, verr := s.readVersion(ctx, cm, node)
	if verr != nil {
		s.appendOutput(a, "version-read", out)
	}
	s.setAttempt(func() { a.After = after })
	s.line(a, "coprocessor reports "+orUnknown(after))
	// 7. the detection unit rewrites /var/hm_mode - between the flash and the daemons (D-50);
	// the adapter's version is not in it
	if cm.Family != priv.CoproHMCFGUSB {
		s.line(a, "re-running the radio detection (/var/hm_mode)")
		if derr := s.redetect(ctx); derr != nil {
			s.warn(a, "the detection did not re-run: "+derr.Error())
		} else {
			s.line(a, "detection done")
		}
	}
	switch {
	case res.Exit != 0:
		return fmt.Errorf("the flasher exited %d; the version was not changed", res.Exit)
	case verr != nil:
		return fmt.Errorf("the version could not be read after the flash: %w", verr)
	case after == "":
		return errors.New("no version read after the flash")
	case after != a.FileVersion:
		if after == before {
			return fmt.Errorf("the flasher exited 0 but the coprocessor still reports %s", after)
		}
		return fmt.Errorf("the coprocessor reports %s, not %s", after, a.FileVersion)
	}
	return nil
}

func containsUnit(list []string, u string) bool {
	for _, x := range list {
		if x == u {
			return true
		}
	}
	return false
}

func orUnknown(v string) string {
	if v == "" {
		return "(no version)"
	}
	return v
}

// setAttempt changes the running attempt under the lock Status copies it under (B-209: the flash's
// goroutine wrote Before, Exit and After while the Interfaces page's poll copied the attempt).
func (s *RadioFirmware) setAttempt(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

// appendOutput writes a tool's output to the run, one entry per line, marked with its source: the
// flasher, or the version read (detect_radio_module, eq3configcmd).
func (s *RadioFirmware) appendOutput(a *FlashAttempt, source string, out []byte) {
	a.run.Output(source, string(out), nil)
}

func (s *RadioFirmware) runningUnits() map[string]bool {
	out := map[string]bool{}
	if s.Services == nil {
		return out
	}
	list, err := s.Services.List()
	if err != nil {
		// unknown: treat every radio unit as running, a stop of a stopped unit is harmless
		for _, u := range radioUnits {
			out[u] = true
		}
		return out
	}
	for _, sv := range list {
		for _, u := range radioUnits {
			if sv.ID == u && sv.Running {
				out[u] = true
			}
		}
	}
	return out
}

func (s *RadioFirmware) waitFor(ctx context.Context, ok func() bool) bool {
	deadline := time.Now().Add(s.wait())
	for {
		if ok() {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return ok()
		}
		s.sleep(ctx, s.poll())
	}
}

func (s *RadioFirmware) sleep(ctx context.Context, d time.Duration) {
	if s.Poll > 0 && d > s.Poll { // tests: never sleep longer than the poll interval
		d = s.Poll
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (s *RadioFirmware) mmdNodesPresent() bool {
	for _, n := range []string{"/dev/mmd_bidcos", "/dev/mmd_hmip"} {
		if _, err := os.Stat(s.Root.join(n)); err == nil {
			return true
		}
	}
	return false
}

// rawUARTFree reads the class device's open_count; a node without one (an old driver) counts
// as free once the loop nodes are gone.
func (s *RadioFirmware) rawUARTFree(node string) bool {
	b := strings.TrimSpace(readFile(s.Root.join("/sys/class/raw-uart/" + filepath.Base(node) + "/open_count")))
	if b == "" {
		return true
	}
	n, err := strconv.Atoi(b)
	return err != nil || n == 0
}

func (s *RadioFirmware) rawUARTExists(node string) bool {
	_, err := os.Stat(s.Root.join("/sys/class/raw-uart/" + filepath.Base(node)))
	return err == nil
}

// findNode re-scans /sys/class/raw-uart for the module's product string, the way
// S48UpdateRFHardware:212-222 finds a stick that re-enumerated.
func (s *RadioFirmware) findNode(cm coproModule) string {
	if cm.Product == "" {
		return ""
	}
	entries, _ := os.ReadDir(s.Root.join("/sys/class/raw-uart"))
	for _, e := range entries {
		dt := readFile(s.Root.join("/sys/class/raw-uart/" + e.Name() + "/device_type"))
		if strings.HasPrefix(dt, cm.Product+"@") {
			return "/dev/" + e.Name()
		}
	}
	return ""
}

// readVersion is 41.0.6: detect_radio_module's sixth field for the HmIP family, the "Version:"
// line of eq3configcmd update-coprocessor -c -v for the legacy one.
func (s *RadioFirmware) readVersion(ctx context.Context, cm coproModule, node string) (string, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var res priv.Result
	var err error
	switch cm.Family {
	case priv.CoproHMCFGUSB:
		// the USB descriptor's bcdDevice: no tool, and nothing that talks to the adapter
		ad := s.usbAdapter(node)
		switch {
		case ad == nil:
			return "", nil, fmt.Errorf("no HM-CFG-USB-2 %s on the USB bus", strings.TrimPrefix(node, "usb:"))
		case ad.Bootloader:
			return "", []byte("the adapter is in its bootloader\n"), nil
		}
		return ad.Version, nil, nil
	case priv.CoproLegacy:
		res, err = Priv.Run(ctx, s.Root.join("/bin/eq3configcmd"), []string{"update-coprocessor", "-p", node, "-t", "HM-MOD-UART", "-c", "-v"}, nil)
		if err != nil {
			return "", res.Combined(), err
		}
		for _, l := range strings.Split(string(res.Combined()), "\n") {
			if strings.Contains(l, "Version:") {
				if f := strings.Fields(l); len(f) >= 5 {
					return f[4], res.Combined(), nil
				}
			}
		}
		return "", res.Combined(), errors.New("no Version: line in the flasher's answer")
	default:
		res, err = Priv.Run(ctx, s.Root.join("/bin/detect_radio_module"), []string{node}, nil)
		if err != nil {
			return "", res.Combined(), err
		}
		if res.Exit != 0 {
			return "", res.Combined(), fmt.Errorf("detect_radio_module exited %d", res.Exit)
		}
		f := strings.Fields(strings.TrimSpace(string(res.Stdout)))
		if len(f) < 6 {
			return "", res.Combined(), fmt.Errorf("detect_radio_module answered %q", strings.TrimSpace(string(res.Stdout)))
		}
		return f[5], res.Combined(), nil
	}
}

// redetect re-runs S47InitRFHardware, which rewrites /var/hm_mode (and resets the module): the
// unit on systemd, the script on busybox.
func (s *RadioFirmware) redetect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if s.Systemd {
		_, err := run(ctx, "systemctl", "restart", "--no-pager", "--", "occu-init-rf-hardware.service")
		return err
	}
	_, err := run(ctx, s.Root.join("/etc/init.d/S47InitRFHardware"), "start")
	return err
}
