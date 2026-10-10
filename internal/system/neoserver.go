package system

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// mediola's NEO Server (roadmap task 37). OpenCCU's package/neoserver unpacks it onto the userfs
// - /usr/local/addons/mediola with rc.d/97NeoServer - and there it survives the switch to
// openccu-lite, whose image does not ship it. Its Homematic module posts to /tclrega.exe and
// /api/homematic.cgi (confirmed on the Pi 4 lab box, node_modules/xnm.aio.hm.js), the ReGa and
// the WebUI's CGI stack, neither of which exists here: it cannot work. So it is switched off with
// the vendor's own switch, $ADDONDIR/Disabled (the rc.d script logs "neo server disabled" and
// starts nothing), listed with the ReGa-dependent addons, and offered as a legacy leftover whose
// removal does what the addon's own uninstall does.

const (
	// NeoServerID is the rc.d name, and with it the unit's (addon-97NeoServer.service).
	NeoServerID = priv.NeoServerRCEntry
	// NeoServerDir is the addon's directory; the vendor's uninstall removes it.
	NeoServerDir = priv.NeoServerHome
	// NeoServerVendorMarker is the addon's own switch: present, its rc.d script starts nothing.
	NeoServerVendorMarker = priv.NeoServerDisabledMarker
	// NeoServerUninstalledMarker is what the vendor's uninstall touches so that OpenCCU's
	// package does not unpack the addon again at the next update ("do not install the neo
	// server after user uninstall").
	NeoServerUninstalledMarker = "/etc/config/neoDisabled"
	// neoServerWatchdog is the cron line the rc.d script appends to root's crontab at every
	// invocation - before it looks at the Disabled marker - and its uninstall never removes.
	neoServerWatchdog = NeoServerDir + "/bin/watchdog"
	neoServerCrontab  = "/usr/local/crontabs/root"
)

// neoServerScript is the rc.d entry; the .script twin is the addon's own script behind the
// addon-rc wrapper (28.8) when the box adopted it.
const neoServerScript = "/usr/local/etc/config/rc.d/" + NeoServerID

// neoServerPaths is everything the vendor's uninstall removes (its four paths), plus the
// wrapper's twin: the addon directory, its config directory, its web tree (a symlink into the
// addon directory on OpenCCU) and the rc.d entry.
var neoServerPaths = []string{NeoServerDir, "/usr/local/etc/config/addons/mediola", AddonWWW + "/mediola", neoServerScript, neoServerScript + ".script"}

// HasReGa says whether the ReGaHss binary is on the box - never on openccu-lite, whose point is
// not having it; the check keeps the NEO Server step honest rather than unconditional.
func (r Root) HasReGa() bool {
	_, err := os.Stat(r.join("/bin/ReGaHss"))
	return err == nil
}

// NeoServerInstalled says whether the addon is on the userfs: its rc.d entry or its directory.
func (r Root) NeoServerInstalled() bool {
	for _, p := range []string{neoServerScript, NeoServerDir} {
		if _, err := os.Lstat(r.join(p)); err == nil {
			return true
		}
	}
	return false
}

// NeoServerDisabled says whether the vendor's switch is set.
func (r Root) NeoServerDisabled() bool {
	_, err := os.Lstat(r.join(NeoServerVendorMarker))
	return err == nil
}

// DisableNeoServer switches the addon off: the vendor's marker, and the rc.d script's executable
// bit (the box's "disabled, incompatible", which the Addons page shows with the reason and a
// switch back). Idempotent; returns what it did, in words, for the log and the marker.
func (r Root) DisableNeoServer() ([]string, error) {
	var done []string
	if _, err := os.Lstat(r.join(NeoServerDir)); err == nil && !r.NeoServerDisabled() {
		// the addon's directory is no generic write's (openccu-lite B-294): the helper's own
		// operation sets the marker, and nothing else there
		if err := Priv.MarkNeoServerDisabled(r.join(NeoServerVendorMarker)); err != nil {
			return done, fmt.Errorf("%s: %w", NeoServerVendorMarker, err)
		}
		done = append(done, "vendor marker "+NeoServerVendorMarker)
	}
	if r.AddonEnabled(NeoServerID) {
		if err := r.SetAddonEnabled(NeoServerID, false); err != nil {
			return done, fmt.Errorf("rc.d/%s: %w", NeoServerID, err)
		}
		done = append(done, "rc.d/"+NeoServerID+" disabled")
	}
	return done, nil
}

// removeNeoServerRemains is the leftover's hook after its paths are gone: the vendor's
// "do not install again" marker, the hm_addons.cfg entry the WebUI's control panel read, and the
// watchdog line the rc.d script put into root's crontab, which would otherwise run a missing
// script every five minutes.
func (r Root) removeNeoServerRemains() error {
	var errs []error
	if err := Priv.Touch(r.join(NeoServerUninstalledMarker), 0o644); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", NeoServerUninstalledMarker, err))
	}
	cfgPath := r.join("/usr/local/etc/config/hm_addons.cfg")
	if entries := ParseHMAddonsCfg(readFile(cfgPath)); entries["mediola"].ConfigURL != "" || entries["mediola"].Name != "" {
		delete(entries, "mediola")
		if err := writeFileAtomic(cfgPath, []byte(WriteHMAddonsCfg(entries)), 0o664); err != nil {
			errs = append(errs, fmt.Errorf("hm_addons.cfg: %w", err))
		}
	}
	if _, err := r.dropCrontabLine(neoServerCrontab, neoServerWatchdog); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", neoServerCrontab, err))
	}
	ForgetAddonScans(NeoServerID)
	return errors.Join(errs...)
}

// dropCrontabLine removes every line of the crontab that names needle; nothing is written when
// none does. busybox crond re-reads the directory on its mtime, so the atomic write is enough.
func (r Root) dropCrontabLine(path, needle string) (bool, error) {
	path = r.join(path)
	text := readFile(path)
	if text == "" || !strings.Contains(text, needle) {
		return false, nil
	}
	var keep []string
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if !strings.Contains(line, needle) {
			keep = append(keep, line)
		}
	}
	out := strings.Join(keep, "\n")
	if out != "" {
		out += "\n"
	}
	return true, writeFileAtomic(path, []byte(out), 0o664)
}

// DropOrphanNeoWatchdog removes the NEO Server's watchdog line from root's crontab when the NEO
// Server is not installed (occulited B-60). The eQ-3 CCU3 firmware writes that line itself, on
// every CCU3, whether the addon was ever installed or not; after a migration busybox crond ran the
// missing /usr/local/addons/mediola/bin/watchdog every five minutes and logged "not found" for
// good (the maintainer's CCU, 2026-10-10: the crontab dated from the day the CCU3 was set up).
// Every other line stays - addons write theirs there too. Run at every start: it reads one small
// file, writes only when the line is there, and an installed NEO Server keeps its line.
func (r Root) DropOrphanNeoWatchdog() (bool, error) {
	if r.NeoServerInstalled() {
		return false, nil
	}
	return r.dropCrontabLine(neoServerCrontab, neoServerWatchdog)
}
