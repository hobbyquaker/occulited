package radio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The root half of a confined interface daemon's unit (task 67, D-55; task 129 phase 2 moved it
// here from the shell helper lite-radio-prep). rfd, multimacd, hmipserver, hs485d and hmlangw run
// as users of their own inside sandboxed units - no capabilities, ProtectSystem=strict - so what
// has to happen as root around the daemon happens here, from the unit's "+" steps:
//
//	prep     ExecStartPre: the node the plan gives the daemon exists, the device nodes carry
//	         their resource groups, and the files the daemon writes are the daemon's - at every
//	         start, because a backup restore, an update from OpenCCU and the LAN gateway page all
//	         leave root-owned files behind
//	ready    ExecStartPost: the daemon reports itself started (the status file carries its pid;
//	         hmipserver touches HMServerStarted), and the nodes multimacd created get their groups
//	stopped  ExecStopPost: what follows the kill (hmipserver's diagram data back to the stick, the
//	         last good copy of its metadata and link stores)
//
// Whether the daemon runs at all is the plan's (the unit's ConditionPathExists on the marker Run
// wrote); a missing node or file here is a failure the unit retries, not a skip.

// LoadPlan reads plan.json from the run directory.
func LoadPlan(root string) (Plan, error) {
	var p Plan
	b, err := os.ReadFile(shadowPath(root, "plan.json"))
	if err != nil {
		return p, fmt.Errorf("no radio plan (has the detection run?): %w", err)
	}
	return p, json.Unmarshal(b, &p)
}

// Prep is the daemon's ExecStartPre as root.
func Prep(ctx context.Context, d Detector, daemon string, p Plan, logf func(string, ...any)) error {
	cfg := "/etc/config"
	switch daemon {
	case "multimacd":
		if !isNode(d, "/dev/eq3loop") {
			return fmt.Errorf("/dev/eq3loop is missing (eq3_char_loop not loaded)")
		}
		if !isNode(d, p.Multimacd.Node) {
			return fmt.Errorf("%s is missing", p.Multimacd.Node)
		}
		nodeGroup(d, "raw-uart", rawUARTNodes(d)...)
		nodeGroup(d, "eq3loop", "/dev/eq3loop")
	case "rfd":
		Own(d.path(cfg+"/rfd.conf"), "root", "rfd", 0o640)
		varConf(d, "rfd", logf)
		_ = os.MkdirAll(d.path(cfg+"/rfd"), 0o755)
		OwnTree(d.path(cfg+"/rfd"), "rfd", "rfd")
		// the security keys: rfd writes the file in place on a key change and creates it if it
		// is missing, which it cannot in a root-owned directory - so it exists, empty, from the start
		if err := TouchOwn(d.path(cfg+"/keys"), "rfd", "rfd", 0o600); err != nil {
			return err
		}
		// openccu-lite B-266: the address file, for the same reason. A module that carries a
		// BidCos address has it written by the radio run; without one (the HM-CFG-USB-2, LAN
		// gateways only) rfd makes a random address and writes it here - in place, into the file
		// its unit opens (ReadWritePaths). A missing file after a factory reset or a first boot
		// left the address unwritten, and the next start chose another. The plan reads an empty
		// file as none.
		if err := TouchOwn(d.path(cfg+"/ids"), "rfd", "rfd", 0o644); err != nil {
			return err
		}
		if err := TouchOwn(d.path("/var/RFD.handlers"), "rfd", "rfd", 0o644); err != nil {
			return err
		}
		if p.RFDLocal {
			if !isNode(d, "/dev/mmd_bidcos") {
				return fmt.Errorf("/dev/mmd_bidcos is missing (multimacd not up?)")
			}
			nodeGroup(d, "mmd-bidcos", "/dev/mmd_bidcos")
		}
		nodeGroup(d, "mmd-bidcos", usbAdapterNodes(d)...)
	case "hmipserver":
		if p.HmIPServerHmIP {
			if !isNode(d, p.HmIPServer.Node) {
				return fmt.Errorf("%s is missing", p.HmIPServer.Node)
			}
			// openccu-lite task 299: what this start would write into the HmIP security counter,
			// and a hold while the clock is not trusted on a wrapped or at-risk access point
			if err := counterPreStart(d, p, logf); err != nil {
				return err
			}
			// openccu-lite task 301: the exchange this start will attempt, for the record
			exchangeBeforeStart(ctx, d, p, logf)
		}
		log4j2(d, logf)
		removeMigratedDiagrams(d, logf) // task 33
		measureDir := filepath.Dir(d.path(DiagramPath))
		_ = os.MkdirAll(d.path(DiagramPath), 0o750)
		OwnTree(measureDir, "hmipserver", "hmipserver")
		Own(measureDir, "hmipserver", "hmipserver", 0o750)
		// openccu-lite B-266: the store is made before the ownership pass - made after it, on a
		// fresh userfs (a factory reset, a first boot), it stayed root's 0700 and the server could
		// neither list it nor write the module's identity into it
		_ = os.MkdirAll(d.path(cfg+"/crRFD/data"), 0o700)
		for _, sub := range []string{"/crRFD", "/eshlight"} {
			_ = os.MkdirAll(d.path(cfg+sub), 0o755)
			OwnTree(d.path(cfg+sub), "hmipserver", "hmipserver")
		}
		// openccu-lite B-253: the server's key and device files are its own alone, as rfd's AES
		// device files are rfd's. The unit's UMask=0077 keeps what it writes from now on 0600;
		// this makes what is there already so - a system that ran an older image, a restored
		// backup (its archive carries the old 0664), the vendor's 0775 directory - and closes the
		// crRFD directory itself to everyone but the server and root (its hmip_user.conf and
		// sgtin.map are 0640 and read through the helper, tasks 149 and 154).
		Own(d.path(cfg+"/crRFD"), "hmipserver", "hmipserver", 0o750)
		RestrictTree(d.path(cfg+"/crRFD/data"), 0o700, 0o600)
		// openccu-lite B-298: an empty or broken metadata or link store is repaired from its last
		// good copy before the server reads it, and a good one is copied
		guardHMIPStores(d, "prep", logf)
		// the HmIP address: the server writes a random one into this file when it has none, in
		// place - its unit opens the file, not /etc/config, and a missing bind source is skipped,
		// so the file exists from the start (B-266); the plan reads an empty file as none. 0644:
		// the address is no secret, and occulited reads the file as its own user.
		if err := TouchOwn(d.path(cfg+"/hmip_address.conf"), "hmipserver", "hmipserver", 0o644); err != nil {
			return err
		}
		Own(d.path(cfg+"/hmip_networkkey.conf"), "hmipserver", "hmipserver", 0o600)
		// the heating group store (HMServer.conf's groupStorageFilePath, task 180): the server
		// writes the file in place, and its unit opens only the file, not the directory, so the
		// file has to exist before the namespace is set up. Not empty: gson reads an empty file
		// as null and the server's start thread dies on it (measured 2026-09-22 on the OVA), so
		// a missing or empty store starts as the no-groups object the server itself writes.
		if err := groupStore(d.path(cfg + "/groups.gson")); err != nil {
			return err
		}
		for _, h := range []string{"/var/LegacyService.handlers", "/var/HMSERVER.handlers"} {
			if err := TouchOwn(d.path(h), "hmipserver", "hmipserver", 0o644); err != nil {
				return err
			}
		}
		// the serial library's lock: the directory is 1775 root:lock, and a lock another user
		// left behind would refuse the node
		lock := d.path("/run/lock")
		_ = os.MkdirAll(lock, 0o1775)
		Own(lock, "root", "lock", 0o1775)
		locks, _ := filepath.Glob(filepath.Join(lock, "LCK..*"))
		for _, l := range locks {
			if owner(l) != "hmipserver" {
				_ = os.Remove(l)
			}
		}
		nodeGroup(d, "mmd-hmip", "/dev/mmd_hmip")
		nodeGroup(d, "raw-uart", rawUARTNodes(d)...)
	case "hs485d":
		Own(d.path(cfg+"/hs485d.conf"), "root", "hs485d", 0o640)
		varConf(d, "hs485d", logf)
		// B-165: the file hs485d writes its registered XML-RPC callbacks into, as rfd has one.
		// It has to exist on the real /var before the unit starts, because the unit shows the
		// daemon a /var of its own and binds this file in: a missing bind source is skipped, and
		// the daemon then writes into the tmpfs, where nothing can read it - which is exactly how
		// the Interfaces page came to show no subscribers for BidCos-Wired. tmpfiles.d makes it at
		// boot; this is the second chance, for the start after a first wired gateway.
		if err := TouchOwn(d.path("/var/HS485D.handlers"), "hs485d", "hs485d", 0o644); err != nil {
			return err
		}
		_ = os.MkdirAll(d.path(cfg+"/hs485d"), 0o755)
		OwnTree(d.path(cfg+"/hs485d"), "hs485d", "hs485d")
		OwnTree(d.path(cfg+"/hs485types"), "hs485d", "hs485d")
		// the unit shows the daemon a private /var with these two mounted in: the loader's pid
		// file under /var/run and the log the daemon opens under /var/log
		for _, sub := range []string{"/run/hs485d/run", "/run/hs485d/log"} {
			if err := os.MkdirAll(d.path(sub), 0o750); err != nil {
				return fmt.Errorf("cannot create %s: %w", sub, err)
			}
		}
		OwnTree(d.path("/run/hs485d"), "hs485d", "hs485d")
	case "hmlangw":
		nodeGroup(d, "mmd-bidcos", "/dev/mmd_bidcos")
	default:
		return fmt.Errorf("unknown daemon %q", daemon)
	}
	logf("prep %s: done", daemon)
	return nil
}

// varConf re-renders the daemon's /var/etc/<daemon>.conf from /etc/config/<daemon>.conf, at every
// start (B-161, B-163). The file the daemon runs from is otherwise the boot's: the radio run
// renders it once, so a LAN gateway added, renamed or removed while the system runs reached rfd
// only after a reboot - and on a system whose boot plan had switched hs485d off (no wired
// interface then) the file did not exist at all, which makes hs485dLoader exit without
// daemonising and the unit fail with "protocol".
//
// What the render does to the file is force the port, and the sections themselves are the ones
// /etc/config carries: that file is the boot render's own output plus what the pages wrote into
// it, so re-rendering keeps the plan and cannot drop what the user configured. An empty or
// unreadable /etc/config file changes nothing - a run that wrote a /var file is not undone here.
func varConf(d Detector, daemon string, logf func(string, ...any)) {
	conf := readFile(d.path("/etc/config/" + daemon + ".conf"))
	if strings.TrimSpace(conf) == "" {
		return
	}
	out := VarRFDConf(conf)
	if daemon == "hs485d" {
		out = VarHS485DConf(conf)
	}
	// root:<daemon> 0640: both files carry the LAN gateways' encryption keys (B-24), and the
	// daemon reads its own through the unit's bind of /var/etc
	if err := (&writer{d: d, report: &RunReport{}}).file("/var/etc/"+daemon+".conf", out, 0o640, "root", daemon); err != nil {
		// Never fatal. The step runs inside the unit's mount namespace - a "+" prefix buys
		// privileges, not a way out of it - so /var/etc is writable only where the unit lists it
		// (ReadWritePaths=). An occulited deployed onto an image whose unit is older finds it
		// read-only, and a daemon that starts with the boot's configuration is better than one
		// that does not start at all. The line says which of the two happened.
		logf("prep %s: /var/etc/%s.conf was not rendered (%v); the daemon starts with what /var/etc holds", daemon, daemon, err)
	}
}

// log4j2 re-renders /var/etc/log4j2.xml from the current /etc/config/syslog before hmipserver
// starts (B-161's third point): the file is the boot render's, so the HmIP level the Log page
// writes reached the daemon only after a reboot. Like varConf, it is never fatal - a system whose
// unit does not list /var/etc as writable keeps the boot's file and says so.
func log4j2(d Detector, logf func(string, ...any)) {
	tmpl := readFile(d.path("/etc/config_templates/log4j2.xml"))
	if strings.TrimSpace(tmpl) == "" {
		return
	}
	syslog := ParseKV(readFile(d.path("/etc/config/syslog")))
	defaults := map[string]string{}
	for _, f := range []string{"/etc/hmipserver.default", "/etc/config/hmipserver.default"} {
		for k, v := range ParseKV(readFile(d.path(f))) {
			defaults[k] = v
		}
	}
	out := QuietKeyServerWarning(Log4j2Config(tmpl, syslog, defaults, logLevels(syslog).HmIP), logLevels(syslog).HmIP, localNetworkKey(d))
	if err := (&writer{d: d, report: &RunReport{}}).file("/var/etc/log4j2.xml", out, 0o644, "", ""); err != nil {
		logf("prep hmipserver: /var/etc/log4j2.xml was not rendered (%v); the server starts with what /var/etc holds", err)
	}
}

// Ready is the daemon's ExecStartPost: the wait for the daemon's own "started", as upstream's
// waitStartupComplete did, and multimacd's endpoints.
func Ready(ctx context.Context, d Detector, daemon string, mainPID int, logf func(string, ...any)) error {
	status := func(name string) bool {
		return strings.TrimSpace(readFile(d.path("/var/status/"+name))) == strconv.Itoa(mainPID)
	}
	switch daemon {
	case "multimacd":
		if !waitFor(d, 40*time.Second, func() bool { return status("multimacd.status") }) {
			return fmt.Errorf("multimacd did not report itself started")
		}
		if !waitFor(d, 20*time.Second, func() bool { return isNode(d, "/dev/mmd_bidcos") && isNode(d, "/dev/mmd_hmip") }) {
			return fmt.Errorf("multimacd did not create /dev/mmd_bidcos and /dev/mmd_hmip")
		}
		nodeGroup(d, "mmd-bidcos", "/dev/mmd_bidcos")
		nodeGroup(d, "mmd-hmip", "/dev/mmd_hmip")
		// openccu-lite B-275: the endpoints say nothing about the module; multimacd's own lines do
		switch verdict, line := multimacdStarted(ctx, d, mainPID); verdict {
		case multimacdStartVersion:
			logf("ready multimacd: started, the module answered (%s), the multiplexer endpoints are there", line)
		case multimacdStartFailed:
			return fmt.Errorf("multimacd got no version from the radio module (%q): rfd and hmipserver could not use it, so the start fails and the unit restarts it", line)
		default:
			logf("ready multimacd: started, the multiplexer endpoints are there; no version line from multimacd within %s (log level above %s?), the module not checked", multimacdStartWait, MultimacdMaxLevel)
		}
	case "rfd":
		if !waitFor(d, 40*time.Second, func() bool { return status("rfd.status") }) {
			return fmt.Errorf("rfd did not report itself started")
		}
		logf("ready rfd: started")
	case "hmipserver":
		// the started marker, and on the way a known fatal error in its own output (D-102): that
		// start cannot succeed, so it fails now, and the marker keeps the unit from restarting
		var fatal HmIPFatal
		polls := 0
		started := waitFor(d, 300*time.Second, func() bool {
			if exists(d.path("/var/status/HMServerStarted")) {
				return true
			}
			if polls++; polls%3 == 0 {
				if code, line, cause := hmipFatal(daemonOutput(ctx, d, mainPID)); code != "" {
					fatal = HmIPFatal{Code: code, Line: line, Cause: cause, At: d.now()}
					return true
				}
			}
			return false
		})
		if fatal.Code != "" {
			if p, err := LoadPlan(d.Root); err == nil && p.HmIP != nil {
				fatal.Adapter = p.HmIP.SGTIN
			}
			writeHmIPFatal(d.Root, fatal)
			// openccu-lite task 301: the exchange this start attempted, with its outcome
			exchangeAfterStart(d, daemonOutput(ctx, d, mainPID), &fatal, logf)
			return fmt.Errorf("the HmIP server cannot start (%s): %s", fatal.Code, fatal.Line)
		}
		if !started {
			return fmt.Errorf("the HmIP server did not report itself started in 300 s")
		}
		logf("ready hmipserver: started")
		output := daemonOutput(ctx, d, mainPID)
		// openccu-lite task 299: the security counter lines of this start
		if p, err := LoadPlan(d.Root); err == nil {
			counterAfterStart(output, d, p, logf)
		}
		// openccu-lite task 301: the exchange this start attempted, with its outcome
		exchangeAfterStart(d, output, nil, logf)
		// B-89: the shim that holds the HTTP port on the loopback fails open when it cannot load
		if port, open := HMServerPortOpen(d.Root); len(open) > 0 {
			logf("ready hmipserver: port %d listens on %s, not only on the loopback - the bind shim did not take (LD_PRELOAD /usr/lib/openccu-lite/libbindlo.so in the unit); the firewall still closes it", port, strings.Join(open, ", "))
		}
	}
	return nil
}

// Stopped is the daemon's ExecStopPost: hmipserver's started marker goes, and its diagram data
// is copied back to the stick when there is one.
func Stopped(ctx context.Context, d Detector, daemon string, logf func(string, ...any)) error {
	if daemon != "hmipserver" {
		return nil
	}
	_ = os.Remove(d.path("/var/status/HMServerStarted"))
	// openccu-lite task 301: an adapter exchange the ready step left open is closed from the files
	exchangeAfterStop(ctx, d, logf)
	// openccu-lite B-298: the server is gone, so its stores hold still - the last good copy
	guardHMIPStores(d, "stopped", logf)
	stick, measure := d.path("/media/usb0/measurement"), d.path(DiagramPath)
	if !d.Diagrams {
		// task 33: no round trip; a stick that was mounted only after the start is looked at here
		removeMigratedDiagrams(d, logf)
		return nil
	}
	if isDir(stick) && isDir(measure) {
		// B-146: with rsync, as upstream's S62HMServer mirrored. The image carries it again
		// (maintainer, 2026-09-17); where it is missing all the same - an older image, a product
		// built without it - the stick's copy is replaced with the image's own `cp -a`, the tool
		// the start side copies with, instead of failing the stop and with it the unit.
		if _, err := d.run()(ctx, d.tool("/usr/bin/rsync"), stickRsyncArgs(d, stick, measure)...); err != nil && !rsyncVanished(err) {
			logf("stopped hmipserver: rsync could not copy the diagram data to the stick (%v); copying with cp", err)
			if err := os.RemoveAll(stick); err != nil {
				logf("stopped hmipserver: the stick's old diagram data could not be removed: %v", err)
			} else if _, err := d.run()(ctx, d.tool("/bin/cp"), "-a", measure, stick); err != nil {
				logf("stopped hmipserver: the diagram data could not be copied to the stick: %v", err)
			}
		}
	}
	return nil
}

// MeasurementRemovedFile records the one-time removal of a migrated system's diagram data (task
// 33): when, which paths, how many bytes.
const MeasurementRemovedFile = "/usr/local/var/lib/occulite/measurement-removed.json"

// migratedDiagramPaths are the places a migrated CCU's hmipserver kept its diagram database, and
// nothing else: the stick's measurement/ directory (/media/usb0 is the stick, or the storage
// directory on the userfs it links to), and the WebUI's copy in the config directory. Never
// another file on the stick.
var migratedDiagramPaths = []string{"/media/usb0/measurement", "/usr/local/etc/config/measurement"}

// removeMigratedDiagrams removes a migrated system's diagram data once, with the setting off
// (task 33, maintainer 2026-10-10: "after a migration occulited should disable/remove measurements").
// openccu-lite has no WebUI to configure or show a diagram, and hmipserver's measurement service
// cannot be switched off in the jar (task 32); it records only configured diagrams, so on a system
// that never was a CCU the directory stays empty. What came along with a migration was copied in
// at every start and back at every stop. Each removal is logged with its path and size; the marker
// says it ran. It is written only once the stick's place was really looked at - /media/usb0
// there, the stick mounted or the storage directory linked - so a stick that usbmount mounted
// after the first start is still found at a later start or stop; until then the two directories
// are looked for at every start and stop (two stats). The user's CCU backup from before the
// switch is the way back.
func removeMigratedDiagrams(d Detector, logf func(string, ...any)) {
	if d.Diagrams || exists(d.path(MeasurementRemovedFile)) {
		return
	}
	type removal struct {
		Path  string `json:"path"`
		Bytes int64  `json:"bytes"`
		Error string `json:"error,omitempty"`
	}
	var done []removal
	failed := false
	for _, p := range migratedDiagramPaths {
		full := d.path(p)
		if !isDir(full) {
			continue
		}
		size := treeSize(full)
		r := removal{Path: p, Bytes: size}
		if err := os.RemoveAll(full); err != nil {
			r.Error, failed = err.Error(), true
			logf("diagram data: %s (%d bytes) could not be removed: %v", p, size, err)
		} else {
			logf("diagram data: %s removed (%d bytes) - openccu-lite records no diagrams (hmipserver.diagrams is off); the CCU backup from before the switch keeps them", p, size)
		}
		done = append(done, r)
	}
	if failed || !isDir(d.path("/media/usb0")) {
		return // no stick (or storage directory) seen yet: looked at again at the next start or stop
	}
	b, _ := json.Marshal(struct {
		At      time.Time `json:"at"`
		Removed []removal `json:"removed"`
	}{d.now(), append([]removal{}, done...)})
	_ = os.MkdirAll(filepath.Dir(d.path(MeasurementRemovedFile)), 0o755)
	_ = os.WriteFile(d.path(MeasurementRemovedFile), append(b, '\n'), 0o644)
}

// treeSize is the bytes of the regular files under dir.
func treeSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, e os.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() {
			if fi, ierr := e.Info(); ierr == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// stickRsyncArgs is the rsync line for the diagram data (B-62). On a stick that keeps owners,
// groups and xattrs (ext4) it mirrors as upstream's S62HMServer did, -aogX. A FAT or exFAT stick
// (usbmount's vfat with gid/fmask, the usual case) can keep none of them: -aogX then ended every
// stop with exit 23 "some files/attrs were not transferred", the stick's copy was removed and the
// whole database copied again with cp, and the journal said it failed. There the copy is what it
// is for - a backup of the data - with times kept to FAT's two seconds.
func stickRsyncArgs(d Detector, stick, measure string) []string {
	if noPosixFS[mountFSType(d, stick)] {
		return []string{"-rt", "--modify-window=2", "--delete-after", "--no-whole-file", "--checksum", measure + "/", stick + "/"}
	}
	return []string{"-aogX", "--delete-after", "--no-whole-file", "--checksum", measure + "/", stick + "/"}
}

// noPosixFS are the stick filesystems without owners, modes or xattrs.
var noPosixFS = map[string]bool{"vfat": true, "msdos": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true}

// mountFSType is the type of the filesystem path lies on, from /proc/mounts (the longest mount
// point that contains the path after its links: /media/usb0 is a link to /media/usb1); "" unknown.
func mountFSType(d Detector, path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	root := filepath.Clean(d.Root)
	if root != "/" && root != "." && root != "" {
		real = "/" + strings.TrimPrefix(strings.TrimPrefix(real, root), "/")
	}
	best, fstype := -1, ""
	for _, line := range strings.Split(readFile(d.path("/proc/mounts")), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		mp := strings.ReplaceAll(f[1], "\\040", " ")
		if (real == mp || mp == "/" || strings.HasPrefix(real, strings.TrimSuffix(mp, "/")+"/")) && len(mp) > best {
			best, fstype = len(mp), f[2]
		}
	}
	return fstype
}

// rsyncVanished: rsync's exit 24, files that vanished while it ran - the copy is whole otherwise.
func rsyncVanished(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 24
}

// --- ownership ------------------------------------------------------------------------------------

// The system calls and lookups, as variables so a test records the calls and supplies the
// daemons' users (the box has them from the image's users table; a test host does not).
var (
	chownFn      = os.Lchown
	chmodFn      = os.Chmod
	lookupUser   = user.Lookup
	lookupGroup  = user.LookupGroup
	lookupUserID = user.LookupId
)

// ids resolves a user and a group; "" keeps the current one (-1). An unknown name is reported
// once and the ownership left as it is - a box without the users table is misbuilt, not a
// reason to keep the radio down.
func ids(userName, groupName string) (uid, gid int, ok bool) {
	uid, gid = -1, -1
	if userName != "" {
		u, err := lookupUser(userName)
		if err != nil {
			return 0, 0, false
		}
		uid, _ = strconv.Atoi(u.Uid)
		if groupName == "" {
			gid, _ = strconv.Atoi(u.Gid)
		}
	}
	if groupName != "" {
		g, err := lookupGroup(groupName)
		if err != nil {
			return 0, 0, false
		}
		gid, _ = strconv.Atoi(g.Gid)
	}
	return uid, gid, true
}

// Own sets a path's owner and mode where they differ; a missing path is left alone, a symlink
// gets the owner only.
func Own(path, userName, groupName string, mode os.FileMode) {
	st, err := os.Lstat(path)
	if err != nil {
		return
	}
	uid, gid, ok := ids(userName, groupName)
	if !ok {
		return
	}
	if cur, ok := statIDs(st); !ok || (uid >= 0 && cur[0] != uid) || (gid >= 0 && cur[1] != gid) {
		_ = chownFn(path, uid, gid)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return
	}
	if st.Mode().Perm() != mode.Perm() || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) {
		_ = chmodFn(path, mode)
	}
}

// OwnTree gives a directory and everything in it to a user (the files keep their modes).
func OwnTree(path, userName, groupName string) {
	if !isDir(path) {
		return
	}
	uid, gid, ok := ids(userName, groupName)
	if !ok {
		return
	}
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if cur, ok := statIDs(info); !ok || cur[0] != uid || cur[1] != gid {
			_ = chownFn(p, uid, gid)
		}
		return nil
	})
}

// RestrictTree gives every directory below and including path dirMode and every regular file
// fileMode (owners untouched); a link is neither followed nor changed, and an entry that has its
// mode is not touched. For a daemon's private store, where the owner's own umask wrote wider modes.
func RestrictTree(path string, dirMode, fileMode os.FileMode) {
	if !isDir(path) {
		return
	}
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		want := fileMode
		switch {
		case info.IsDir():
			want = dirMode
		case !info.Mode().IsRegular():
			return nil
		}
		if info.Mode().Perm() != want.Perm() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			_ = chmodFn(p, want)
		}
		return nil
	})
}

// TouchOwn creates a file empty when it is missing, then owns it.
func TouchOwn(path, userName, groupName string, mode os.FileMode) error {
	if !exists(path) {
		if err := os.WriteFile(path, nil, mode); err != nil {
			return fmt.Errorf("cannot create %s: %w", path, err)
		}
	}
	Own(path, userName, groupName, mode)
	return nil
}

// groupStore makes hmipserver's heating group store exist with the no-groups object (its
// GroupList, {"groups":[]}) when it is missing or empty, then makes it the server's.
func groupStore(path string) error {
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		if err := os.WriteFile(path, []byte(`{"groups":[]}`), 0o640); err != nil {
			return fmt.Errorf("cannot create %s: %w", path, err)
		}
	}
	Own(path, "hmipserver", "hmipserver", 0o640)
	return nil
}

// nodeGroup gives device nodes their resource group (root:<group> 0660); a missing node is skipped.
func nodeGroup(d Detector, group string, nodes ...string) {
	for _, n := range nodes {
		if isNode(d, n) {
			Own(d.path(n), "root", group, 0o660)
		}
	}
}

func rawUARTNodes(d Detector) []string {
	var out []string
	for _, n := range d.rawUARTNames() {
		out = append(out, "/dev/"+n)
	}
	return out
}

// usbAdapterNodes are the HM-CFG-USB-2's USB device nodes (opened through libusb), where udev
// is not running.
func usbAdapterNodes(d Detector) []string {
	var out []string
	dirs, _ := filepath.Glob(d.path("/sys/bus/usb/devices/*"))
	for _, dir := range dirs {
		if strings.TrimSpace(readFile(filepath.Join(dir, "idVendor"))) != "1b1f" || strings.TrimSpace(readFile(filepath.Join(dir, "idProduct"))) != "c00f" {
			continue
		}
		b, err1 := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(dir, "busnum"))))
		n, err2 := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(dir, "devnum"))))
		if err1 == nil && err2 == nil {
			out = append(out, fmt.Sprintf("/dev/bus/usb/%03d/%03d", b, n))
		}
	}
	return out
}

func owner(path string) string {
	st, err := os.Lstat(path)
	if err != nil {
		return ""
	}
	cur, ok := statIDs(st)
	if !ok {
		return ""
	}
	u, err := lookupUserID(strconv.Itoa(cur[0]))
	if err != nil {
		return ""
	}
	return u.Username
}
