package radio

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// openccu-lite B-309: hmipserver leaves its RXTX lock file behind when it is stopped. Through the
// hmipserver unit's UMask=0077 the file is hmipserver's alone (0600), so RXTX in another user's
// process - the coprocessor flasher, run as multimacd - cannot read the PID in it to see that it
// is stale, and refuses the node ("device is locked by another application") until a reboot
// empties /run/lock. hmipserver removes its own stale files at its next start, but the flash
// comes first after a period of HmIP only (hmipserver directly on the raw UART).
//
// So hmipserver's ExecStopPost removes the lock files of the nodes it used whose PID is no longer
// alive (the maintainer, 2026-11-11: at the stop). The nodes are the ones hmipserver was
// configured with - the Adapter.<n>.Port lines of the crRFD.conf rendered for it, and the HmIP
// node /var/hm_mode names - never a wildcard over /run/lock; a file whose PID is alive, or that
// says no PID, stays.

// LockDir is where RXTX keeps its lock files (/var/lock links here).
const LockDir = "/run/lock"

var (
	adapterPortLine = regexp.MustCompile(`(?m)^\s*Adapter\.[0-9]+\.Port\s*=\s*(\S+)\s*$`)
	lockNodeName    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// hmipserverNodes are the device nodes hmipserver was set up to open, by their names under /dev.
func hmipserverNodes(d Detector) []string {
	var out []string
	add := func(node string) {
		node = strings.TrimSpace(node)
		if !strings.HasPrefix(node, "/dev/") {
			return
		}
		name := strings.TrimPrefix(node, "/dev/")
		if !lockNodeName.MatchString(name) || name == "." || name == ".." {
			return
		}
		for _, n := range out {
			if n == name {
				return
			}
		}
		out = append(out, name)
	}
	for _, m := range adapterPortLine.FindAllStringSubmatch(readFile(d.path("/var/etc/crRFD.conf")), -1) {
		add(m[1])
	}
	add(ParseKV(readFile(d.path("/var/hm_mode")))["HM_HMIP_DEVNODE"])
	return out
}

// removeStaleLocks removes LCK..<node> of hmipserver's nodes when the PID it names is gone.
func removeStaleLocks(d Detector, logf func(string, ...any)) {
	for _, name := range hmipserverNodes(d) {
		f := d.path(filepath.Join(LockDir, "LCK.."+name))
		st, err := os.Lstat(f)
		if err != nil {
			continue
		}
		if !st.Mode().IsRegular() {
			logf("stopped hmipserver: %s is not a plain file, left alone", f)
			continue
		}
		b, _ := os.ReadFile(f)
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			logf("stopped hmipserver: %s names no process (%q), left alone", f, strings.TrimSpace(string(b)))
			continue
		}
		if exists(d.path("/proc/" + strconv.Itoa(pid))) {
			logf("stopped hmipserver: %s belongs to the running process %d, left alone", f, pid)
			continue
		}
		if err := os.Remove(f); err != nil {
			logf("stopped hmipserver: the stale lock %s (process %d is gone) could not be removed: %v", f, pid, err)
			continue
		}
		logf("stopped hmipserver: removed the stale lock %s (process %d is gone)", f, pid)
	}
}
