// Package priv is the privilege boundary of occulited (task 17). Everything that needs root -
// running the firmware's scripts, writing under /etc/config, staging an update, creating an
// addon user - goes through an Ops value. As root that is Local, which does the work in
// process; as the unprivileged occulite user it is a Client talking to `occulited helper`, a
// root process on a unix socket that offers exactly the operations below and refuses
// everything else (Server, with its allowlists). The HTTP side of occulited never has more
// than the helper grants.
package priv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"github.com/hobbyquaker/occulited/internal/certpem"
	"github.com/hobbyquaker/occulited/internal/netmount"
	"github.com/hobbyquaker/occulited/internal/ownwalk"
)

// Result of a command.
type Result struct {
	Stdout []byte
	Stderr []byte
	Exit   int
}

// Combined is stdout followed by stderr, what exec.CombinedOutput would have given.
func (r Result) Combined() []byte { return append(append([]byte{}, r.Stdout...), r.Stderr...) }

// Ops is the enumerated set of privileged operations.
type Ops interface {
	// Run executes name with args; stdin may be nil. A non-zero exit is not an error: it is
	// Result.Exit, and err is only a failure to run at all (or the helper refusing it).
	Run(ctx context.Context, name string, args []string, stdin []byte) (Result, error)
	// WriteFile writes data to path atomically (temp file + rename) with mode.
	WriteFile(path string, data []byte, mode os.FileMode) error
	// Touch creates path (mode) when missing, like the firmware's marker files.
	Touch(path string, mode os.FileMode) error
	// Remove deletes a file or empty directory; a missing path is not an error.
	Remove(path string) error
	// RemoveAll deletes a tree.
	RemoveAll(path string) error
	// MkdirAll creates a directory tree.
	MkdirAll(path string, mode os.FileMode) error
	// Symlink creates link -> target, replacing an existing link.
	Symlink(target, link string) error
	// Rename moves src to dst on the same filesystem (large files: written by the caller into
	// its own directory, moved into place here).
	Rename(src, dst string) error
	// Chown changes the owner, recursively when asked. The recursive one is ownwalk's walk: no link
	// followed, nothing on another file system, only entries with another owner changed.
	Chown(path string, uid, gid int, recursive bool) error
	// OwnTree gives a confined addon's directories to its user (task 107, B-92): ownwalk's walk of
	// dirs, every entry whose owner is not uid:uid changed through its own descriptor - or, with
	// dryRun, only counted. The helper admits the directories of exactly one addon (its three
	// standard ones and data directories below /usr/local, Policy.OwnTreeAllowed) and only the uid
	// the passwd file gives that addon's user.
	OwnTree(id string, dirs []string, uid int, opt OwnTreeOptions) (ownwalk.Result, error)
	// NetMount writes a backup target's .mount and .automount units (rendered from the checked
	// spec, openccu-lite task 86) into PID 1's runtime unit directory and (re)starts the
	// automount; NetUnmount stops the mount now; NetMountRemove removes both units.
	NetMount(ctx context.Context, s netmount.Spec) error
	NetUnmount(ctx context.Context, id string) error
	NetMountRemove(ctx context.Context, id string) error
	// WriteTest creates, writes, syncs, reads back and deletes a 1 MiB file in dir as root - what
	// a backup to that directory does - and answers the step it reached (task 86).
	WriteTest(ctx context.Context, dir string) (WriteTestResult, error)
	// Chmod sets a file's mode (an addon's rc.d script: executable = enabled).
	Chmod(path string, mode os.FileMode) error
	// ReadFile reads a root-only file the daemon must see (the ReGa database at first boot).
	ReadFile(path string) ([]byte, error)
	// SetRootPasswordHash replaces the password field of the "root:" line of the firmware's
	// /etc/config/shadow with hash, in the privileged process. The file is 0640 root:root and
	// stays unreadable to the daemon: neither the old hashes nor the rest of the file ever
	// cross the boundary, which is why this is an operation of its own rather than shadow
	// joining ReadPaths (B-15).
	SetRootPasswordHash(path, hash string) error
	// Open opens a regular file for reading and hands back the descriptor itself - the daemon
	// streams from it without ever holding the path privilege, and the bytes never pass through
	// the socket (B-35: an addon CGI's X-Sendfile answer). Across the helper the descriptor
	// travels as SCM_RIGHTS; the allowlist is Policy.SendfileDirs, which is deliberately not
	// ReadPaths - a file that may be streamed to the caller of a CGI is not a file the daemon
	// may read into itself.
	Open(path string) (*os.File, error)
	// ShareList lists one folder of a network share as root, and ShareOpen opens a backup file
	// there (openccu-lite B-217): on a root-squashed export only root - the identity that wrote the
	// backups - may read them. Policy: under /media/net/<id> only; ShareOpen only *.sbk[.age].
	ShareList(ctx context.Context, dir string) (ShareListResult, error)
	ShareOpen(path string) (*os.File, error)
	// StoreCopy puts the database's snapshot into a folder on a USB stick (openccu-lite task 229):
	// the snapshot is the daemon's open file (a descriptor across the helper), the folder one on
	// /media/usb1…8 with a stick mounted there.
	StoreCopy(in *os.File, dir string) error
	// RunAs runs an addon's CGI as that addon's user (task 18): the interpreter and the script
	// are checked against the CGI roots, the environment is the caller's (a CGI environment),
	// stdin is the request body. uid 0 is allowed only for scripts under those roots (the
	// busybox products, where addons have no user).
	RunAs(ctx context.Context, cred Credential, name string, args []string, env []string, dir string, stdin []byte) (Result, error)
	// WriteCertificate writes the box's live TLS file - the certificate chain and its private
	// key, one PEM, what lighttpd and the addons read - atomically as root:certs 0640 (task 35,
	// D-46/D-48). It is an operation of its own rather than a generic write because the file
	// is a trust anchor: the helper checks at its boundary that the data is exactly a chain
	// plus the one key that belongs to it, and the allowlist is Policy.CertPaths, one exact
	// path.
	//
	// It also writes the marker path+".managed" (root 0644, one line: the mode - acme or
	// manual - a space, and the leaf's issuer), which S50lighttpd's check_certificate honours:
	// a certificate occulited installed is occulited's - renewed by it in mode acme, left to
	// the user in mode manual - and is never regenerated by the init script; a step-ca's
	// 24-hour certificate would otherwise be replaced by a self-signed one on the very reload
	// that installs it. Task 35's marker path+".acme" (the issuer alone) is written as well
	// until the fork's S50lighttpd reads the new name (task 38). The issuer is derived from
	// the PEM here, not taken from the caller; the mode is checked at the boundary.
	WriteCertificate(path string, pem []byte, mode string) error
	// ReadCertificate returns the CERTIFICATE blocks of that file and nothing else - the daemon
	// shows what the box serves without ever holding the private key S50lighttpd made (the
	// occulite user is not in the certs group, and the same list applies) - and the marker's
	// line, "<mode> <issuer>" (an old ".acme" marker alone reads as mode acme), empty when the
	// certificate is not occulited's.
	ReadCertificate(path string) (pem []byte, marker string, err error)
	// AddAddonUser creates the account of a confined addon (D-36): the user name, whose id
	// (the part after "addon-") names the home directory /usr/local/addons/<id>, with uid and
	// a group of the same name and number, shell /bin/false, no password. The two lines are
	// appended to the passwd and group files **in place** - O_APPEND on the existing inode,
	// never a temporary file plus rename: on the image the two files are bind mounts of
	// /run/openccu-lite/{passwd,group} over a read-only /etc, and busybox adduser/addgroup,
	// which write "/etc/group+" beside the file and rename it, fail there with "Read-only file
	// system" (B-54). Idempotent: an existing line for the same name and number is fine; the
	// name with another number, or the number with another name, is an error and nothing is
	// written. /etc/shadow is left alone - "x" in passwd with no shadow line is an account
	// nobody can log in to, which is what /bin/false says as well; busybox with -D wrote a
	// locked "!" line there, and nothing needed it.
	AddAddonUser(passwd, group, name string, uid int) error
	// FlashCoprocessor runs the radio module's coprocessor flasher (task 41) - the exact
	// invocation S48UpdateRFHardware uses at boot, built here from three checked pieces rather
	// than handed over as a command line: for CoproHmIP the jar
	// (java -Dos.arch=<uname -m> -Dgnu.io.rxtx.SerialPorts=<dev> -jar hmip-copro-update.jar
	// -p <dev> -o -f <file>, 120 s), for CoproLegacy eq3configcmd update-coprocessor with a
	// temporary directory holding the file and a synthetic fwmap line "CCU2 <file> <version>"
	// (120 s, then once more with -f and 240 s, as S48 does). The output is captured - S48
	// throws it away, and it is the only thing a failed flash leaves behind. A non-zero exit is
	// Result.Exit, not an error. The caller has stopped everything that holds the node.
	FlashCoprocessor(ctx context.Context, family, devnode, file, version string) (Result, error)
	// Smartctl reads one whole disk's health data for the Status page's storage panel (task 69):
	// exactly `smartctl -j -H -A -i <device>` - JSON, the overall health, the attributes, the
	// identity - and nothing else. smartctl needs root to open the device, and a program on the
	// Programs list would take any option (-s, -t, -X change a drive's state), so the helper
	// builds this one command line itself from one checked operand: a device of the shape
	// SmartDeviceRe names, which must be a block device. A non-zero exit is Result.Exit, not an
	// error - smartctl's exit status is a bit mask and its JSON is on stdout either way.
	Smartctl(ctx context.Context, device string) (Result, error)
	// FirewallInput reads one address family's INPUT chain for the Status page's firewall check
	// (B-152): exactly `iptables -w 5 -S INPUT` or `ip6tables -w 5 -S INPUT`, and nothing else.
	// Listing the rules needs CAP_NET_ADMIN, and iptables on the Programs list would take -F and
	// -P as well, so the helper builds the command line itself from the family, a key of
	// FirewallFamilies. A non-zero exit is Result.Exit, not an error.
	FirewallInput(ctx context.Context, family string) (Result, error)
	// FirewallLoad loads task 157's rules, one iptables-restore text per family (firewall.go):
	// checked (ValidFirewallText), tested, loaded --noflush, the first family put back if the
	// second fails. With a window the helper keeps the tables as they were and puts them back
	// after it unless FirewallConfirm comes first.
	FirewallLoad(ctx context.Context, v4, v6 []byte, window time.Duration) error
	// FirewallConfirm closes an open window; FirewallRevert puts its tables back now.
	FirewallConfirm(ctx context.Context) error
	FirewallRevert(ctx context.Context) error
	// SocketOwners maps socket inodes to the processes holding them: pid, name, systemd unit.
	SocketOwners(ctx context.Context) ([]SocketOwner, error)
	// FirewallCounters is each family's filter table with its counters (task 167).
	FirewallCounters(ctx context.Context) (map[string][]byte, error)
	// WriteLEDs applies a status LED frame under dir, the LED class directory (task 95, led.go):
	// every LED of the frame to trigger none, then each one's trigger and attributes. The LEDs are
	// Policy.LEDNames, the triggers LEDTriggers, the numbers bounded (ValidLEDFrame).
	WriteLEDs(dir string, frame []LEDWrite) error
	// LoadLEDModule loads one of LEDModules (openccu-lite B-299, led.go): the pattern trigger,
	// which the daemon cannot see in /lib/modules nor load itself (ProtectKernelModules=). The
	// helper admits the names of that list and nothing else.
	LoadLEDModule(ctx context.Context, name string) error
	// ListLogFiles lists the log files under dir for the storage hint (B-113, loglist.go): each
	// regular file's path, length and modification time, never a byte of it; only the files named
	// like logs unless allFiles; no symlink followed. The helper admits a directory of
	// Policy.LogListDirs only.
	ListLogFiles(dir string, allFiles bool) ([]LogFileInfo, error)
	// ListDir answers the names in dir (openccu-lite B-253, listdir.go), nothing about them: the
	// helper admits a directory of Policy.ListDirs only - hmipserver's data directory, which is
	// 0700 - so the daemon knows which devices and modules have files there.
	ListDir(dir string) ([]string, error)
	// ProcExe answers where /proc/<pid>/exe leads (occulited B-59, procexe.go): the link only,
	// for any process - the daemon can read it for its own processes alone.
	ProcExe(pid int) (string, error)
	// RemoveRCTarget removes the regular file a migrated rc.d entry led to (occulited task 28,
	// rctarget.go): under /usr/local/, outside the addons' tree, the configuration and the state.
	RemoveRCTarget(path string) error
	// RemoveCCURemnant removes one of the known-useless CCU remnants (occulited task 29,
	// remnants.go): an addon's config directory whose addon is gone, the ReGa's crash dump, the
	// CCU3's empty eQ-3-Backup folder - the helper checks each condition itself.
	RemoveCCURemnant(path string) error
	// AddonPolicyFile writes or removes one of an addon's policy files root obeys (openccu-lite
	// B-293): path is <AddonPolicyDir>/<id>.conf, .needs or .start, and the text is the helper's,
	// rendered from f (addonunit) after checking it - a drop-in with the addon's own user and a uid
	// in the addon range, grants of a shape that adds no line and nothing root-equivalent for a
	// confined addon, a start order of known interfaces, the early start's one word.
	AddonPolicyFile(path string, f addonunit.File) error
	// SetAddonEnabled sets or clears the executable bits of an addon's rc.d entry (B-293): the
	// entry's mode is the helper's to compute, nothing but those bits changes.
	SetAddonEnabled(path string, enabled bool) error
	// RemoveAddonEntry removes an addon's rc.d entry, the addon's own script beside the wrapper
	// and its web entry in the config directory (a link, an empty directory, or with whole the
	// directory and everything in it) - the uninstall's and the leftovers' removal (B-293). It
	// answers the paths that were there and went.
	RemoveAddonEntry(rcd, www string, whole bool) ([]string, error)
	// RemoveCertificate removes the live certificate and its two markers (B-293; until then a
	// generic remove with an exception in the policy).
	RemoveCertificate(path string) error
	// RemoveAddonHome removes an addon's emptied directory, AddonHomeDir + <id> - a link, a file
	// or an empty directory - once its rc.d entry is gone: the uninstall's (openccu-lite B-294,
	// /usr/local/addons/ is no write prefix).
	RemoveAddonHome(path string) error
	// MarkNeoServerDisabled creates the NEO Server's own switch, NeoServerDisabledMarker, an empty
	// file in its existing directory (B-294).
	MarkNeoServerDisabled(path string) error
	// RemoveNeoServerHome removes the NEO Server leftover's directory, NeoServerHome, with what is
	// in it, once its rc.d entry and its web entry are gone (B-294).
	RemoveNeoServerHome(path string) error
	// ReadAddonFragment reads an addon's lighttpd fragment, /usr/local/addons/<id>/etc/lighttpd.conf
	// (occulited B-35, addonfragment.go): through os.Root on the addon's tree, a regular file of at
	// most AddonFragmentMax bytes. Confined addons' files are 0640 since openccu-lite B-252, so the
	// drop-in sync reads them through the helper.
	ReadAddonFragment(path string) ([]byte, error)
	// ReadAddonImage reads one of an addon's declared images (openccu-lite task 100,
	// addonimage.go): manifestPath is the stored manifest, tree the addon's directory, kind one of
	// addonimage.Kinds. The helper resolves the kind to the path the manifest declares, opens it
	// through os.Root on the tree, and answers it only when its content is an image.
	ReadAddonImage(manifestPath, tree, kind string) ([]byte, error)
}

// SmartDeviceRe is the one device shape the SMART read admits: a whole SCSI/SATA/USB disk
// (/dev/sda) or an NVMe namespace (/dev/nvme0n1). Not a partition, not an MMC or SD card (they
// have no SMART), not a virtio disk (its health is the host's), not a controller node
// (/dev/nvme0 is a character device), and no path that could be made to point elsewhere.
var SmartDeviceRe = regexp.MustCompile(`^/dev/(sd[a-z]{1,2}|nvme[0-9]{1,2}n[0-9]{1,2})$`)

// SmartctlArgs is the whole argument list of the SMART read - the same slice the operation runs
// and the tests compare against.
func SmartctlArgs(device string) []string { return []string{"-j", "-H", "-A", "-i", device} }

// smartctlPath is where smartmontools puts the program (measured on the rpi3, rpi4 and ova
// products); since task 111 only the hardware products build it, and where it is not installed the
// read answers ErrNotAvailable. blockDevice is the check that the operand is a block device. Both
// are variables so a test can point them at a script and a fake node.
var (
	smartctlPath = "/usr/bin/smartctl"
	blockDevice  = func(path string) error {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeDevice == 0 || st.Mode()&os.ModeCharDevice != 0 {
			return fmt.Errorf("%s is not a block device", path)
		}
		return nil
	}
)

// smartctlTimeout bounds one read; a USB bridge that does not answer the passthrough can take
// tens of seconds before smartctl gives up.
const smartctlTimeout = 60 * time.Second

func (l Local) Smartctl(ctx context.Context, device string) (Result, error) {
	if !SmartDeviceRe.MatchString(device) {
		return Result{}, fmt.Errorf("not a whole disk: %q", device)
	}
	if _, err := os.Stat(smartctlPath); os.IsNotExist(err) {
		return Result{}, fmt.Errorf("%w: %s is not installed", ErrNotAvailable, smartctlPath)
	}
	if err := blockDevice(device); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, smartctlTimeout)
	defer cancel()
	return l.Run(ctx, smartctlPath, SmartctlArgs(device), nil)
}

// FirewallFamilies are the families the INPUT read takes, and the program each one runs (where
// the iptables package puts them on the images). A variable so a test can point them at scripts.
var FirewallFamilies = map[string]string{"ipv4": "/usr/bin/iptables", "ipv6": "/usr/bin/ip6tables"}

// FirewallInputArgs is the whole argument list of the INPUT read: wait up to 5 s for the xtables
// lock (a firewall being applied holds it), then list the chain as rules.
func FirewallInputArgs() []string { return []string{"-w", "5", "-S", "INPUT"} }

// firewallInputTimeout bounds one read: the lock wait plus the listing.
const firewallInputTimeout = 15 * time.Second

func (l Local) FirewallInput(ctx context.Context, family string) (Result, error) {
	prog, ok := FirewallFamilies[family]
	if !ok {
		return Result{}, fmt.Errorf("not a firewall family: %q", family)
	}
	if _, err := os.Stat(prog); os.IsNotExist(err) {
		return Result{}, fmt.Errorf("%w: %s is not installed", ErrNotAvailable, prog)
	}
	ctx, cancel := context.WithTimeout(ctx, firewallInputTimeout)
	defer cancel()
	return l.Run(ctx, prog, FirewallInputArgs(), nil)
}

// AddonUserPrefix is what every addon account's name starts with; the rest is the addon's id.
const AddonUserPrefix = "addon-"

// AddonHomeDir is where an addon's home directory is: AddonHomeDir + id.
const AddonHomeDir = "/usr/local/addons/"

// addonUserRe is the account name AddAddonUser accepts: the prefix plus an addon id as
// internal/system's addonIDRe spells it. It is checked in the operation and at the helper's
// boundary both: a ":" or a newline in the name would add fields or lines to the two files.
var addonUserRe = regexp.MustCompile(`^addon-[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}$`)

// ValidAddonUser guards the account operation.
func ValidAddonUser(name string, uid int) bool {
	return uid > 0 && addonUserRe.MatchString(name)
}

// MarkerSuffix names task 35's ACME marker next to the live file, /etc/config/server.pem.acme,
// which the fork's S50lighttpd reads; ManagedSuffix is task 38's, /etc/config/server.pem.managed,
// carrying the mode. Both are written; ManagedSuffix is the one to keep.
const (
	MarkerSuffix  = ".acme"
	ManagedSuffix = ".managed"
)

// The modes a certificate marker may name.
const (
	CertModeACME   = "acme"
	CertModeManual = "manual"
)

// ValidCertMode guards the write operation's mode.
func ValidCertMode(mode string) bool { return mode == CertModeACME || mode == CertModeManual }

// CertsGroup is the group that may read the live TLS file (D-46); its gid is looked up on the
// box at write time. Without the group (an older image) the file stays root-only 0600.
const CertsGroup = "certs"

// Credential is who a CGI runs as.
type Credential struct {
	UID    int   `json:"uid"`
	GID    int   `json:"gid"`
	Groups []int `json:"groups,omitempty"`
}

// Local does the operations in this process. It is what root uses and what the helper runs.
type Local struct {
	// noFollow: the file operations follow no symlink at all (nofollow.go, openccu-lite B-235) -
	// the helper Server sets it, having resolved the path through the image's own links itself.
	// Off, a link is followed as before: root without a helper has no boundary to hold.
	noFollow bool
}

func (Local) Run(ctx context.Context, name string, args []string, stdin []byte) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = "/"
	if stdin != nil {
		cmd.Stdin = bytesReader(stdin)
	}
	var out, errb bytesBuffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
		if res.Exit < 0 {
			res.Exit = 128
		}
	default:
		return res, err
	}
	return res, nil
}

// The flasher's fixed pieces (S48UpdateRFHardware:142,236 and :72,74); variables so a test
// can point them at a script that records what it was given.
var (
	coproJava      = "/opt/java/bin/java"
	coproJar       = "/opt/HmIP/hmip-copro-update.jar"
	coproConfigCmd = "/bin/eq3configcmd"
	// coproHMCFGUSB is hmcfgusb's flasher (task 147, package/hmcfgusb in the fork)
	coproHMCFGUSB = "/usr/bin/flash-hmcfgusb"
)

// The flasher runs as the module's user, not as root (task 103 step 5, D-93): on a systemd box
// the command line goes into a transient unit with User=multimacd - the user that owns the raw
// UART at runtime - the node's resource group (raw-uart, set by udev and lite-radio-prep), the
// lock group for the serial library's lock file, and a device policy that admits the one node
// and nothing else; the file system is read-only, /tmp private, no capabilities. Both flashers
// need nothing beyond the node and the firmware directory (eq3configcmd's reset file is a CCU2
// default it never writes for HM-MOD-UART). Off a systemd box (upstream's OCI image runs busybox
// init) and on a node that carries no resource group (root:root: an older image, a container
// without udev) the flasher runs as root as before, and the result's stderr says so.
var (
	coproSystemdRun = "systemd-run"
	coproSystemdDir = "/run/systemd/system" // there when systemd is PID 1
	coproUser       = "multimacd"
	// coproWorkDir holds the legacy flasher's directory (the file and its fwmap): under /run so
	// that the unit, whose /tmp is private, can read it. A test points it elsewhere.
	coproWorkDir = "/run/occulite"
	// coproNodeGroup is the group that owns the node: "" when there is none or it is root's.
	coproNodeGroup = func(devnode string) string {
		st, err := os.Stat(devnode)
		if err != nil {
			return ""
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || sys.Gid == 0 {
			return ""
		}
		g, err := user.LookupGroupId(strconv.Itoa(int(sys.Gid)))
		if err != nil {
			return ""
		}
		return g.Name
	}
)

// runFlasher runs one flasher command line as the module's user (see above), bounded by
// timeout: the transient unit gets RuntimeMaxSec= as well, so a systemd-run killed at the
// deadline leaves no flasher behind.
func (l Local) runFlasher(ctx context.Context, timeout time.Duration, devnode, name string, args []string) (Result, error) {
	if st, err := os.Stat(coproSystemdDir); err != nil || !st.IsDir() {
		return l.Run(ctx, name, args, nil)
	}
	group := coproNodeGroup(devnode)
	if group == "" {
		r, err := l.Run(ctx, name, args, nil)
		r.Stderr = append([]byte("flashing as root: "+devnode+" carries no resource group\n"), r.Stderr...)
		return r, err
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	full := []string{"--wait", "--pipe", "--collect", "--quiet", "--unit=occulite-copro-" + hex.EncodeToString(b),
		"-p", "RuntimeMaxSec=" + strconv.Itoa(int(timeout/time.Second)),
		"-p", "User=" + coproUser, "-p", "Group=" + coproUser,
		"-p", "SupplementaryGroups=" + group, "-p", "SupplementaryGroups=lock",
		"-p", "DevicePolicy=closed", "-p", "DeviceAllow=" + devnode + " rw",
		"-p", "CapabilityBoundingSet=", "-p", "NoNewPrivileges=yes",
		"-p", "ProtectSystem=strict", "-p", "ReadWritePaths=/run/lock", "-p", "PrivateTmp=yes", "-p", "ProtectHome=yes",
		"-p", "ProtectKernelTunables=yes", "-p", "ProtectKernelModules=yes", "-p", "ProtectControlGroups=yes",
		"-p", "WorkingDirectory=/",
		"--", name}
	full = append(full, args...)
	return l.Run(ctx, coproSystemdRun, full, nil)
}

// runFlasherUSB runs the HM-CFG-USB-2's flasher as rfd, in rfd's group for the adapter's node
// (mmd-bidcos; the udev rule gives it to the application's 1b1f:c00f and the bootloader's
// 1b1f:c010 alike): the adapter re-enumerates into its bootloader and back during the flash, so
// the unit may open USB device nodes as a class rather than one node.
func (l Local) runFlasherUSB(ctx context.Context, timeout time.Duration, name string, args []string) (Result, error) {
	if st, err := os.Stat(coproSystemdDir); err != nil || !st.IsDir() {
		return l.Run(ctx, name, args, nil)
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	full := []string{"--wait", "--pipe", "--collect", "--quiet", "--unit=occulite-copro-" + hex.EncodeToString(b),
		"-p", "RuntimeMaxSec=" + strconv.Itoa(int(timeout/time.Second)),
		"-p", "User=rfd", "-p", "Group=rfd", "-p", "SupplementaryGroups=mmd-bidcos",
		"-p", "DevicePolicy=closed", "-p", "DeviceAllow=char-usb_device rw",
		"-p", "CapabilityBoundingSet=", "-p", "NoNewPrivileges=yes",
		"-p", "ProtectSystem=strict", "-p", "PrivateTmp=yes", "-p", "ProtectHome=yes",
		"-p", "ProtectKernelTunables=yes", "-p", "ProtectKernelModules=yes", "-p", "ProtectControlGroups=yes",
		"-p", "WorkingDirectory=/",
		"--", name}
	full = append(full, args...)
	return l.Run(ctx, coproSystemdRun, full, nil)
}

const (
	coproHMCFGUSBTimeout = 180 * time.Second
	coproJarTimeout      = 120 * time.Second
	coproLegacyT1        = 120 * time.Second
	coproLegacyT2        = 240 * time.Second
)

func (l Local) FlashCoprocessor(ctx context.Context, family, devnode, file, version string) (Result, error) {
	switch family {
	case CoproHmIP:
		ctx, cancel := context.WithTimeout(ctx, coproJarTimeout)
		defer cancel()
		return l.runFlasher(ctx, coproJarTimeout, devnode, coproJava, []string{"-Dos.arch=" + unameMachine(), "-Dgnu.io.rxtx.SerialPorts=" + devnode, "-jar", coproJar, "-p", devnode, "-o", "-f", file})
	case CoproHMCFGUSB:
		m := HMCFGUSBDevice.FindStringSubmatch(devnode)
		if m == nil {
			return Result{}, fmt.Errorf("not an HM-CFG-USB-2: %q", devnode)
		}
		ctx, cancel := context.WithTimeout(ctx, coproHMCFGUSBTimeout)
		defer cancel()
		return l.runFlasherUSB(ctx, coproHMCFGUSBTimeout, coproHMCFGUSB, []string{"-S", m[1], file})
	case CoproLegacy:
		// the legacy flasher wants a directory with the file and a fwmap naming it (S48:52-56);
		// world-readable, since the flasher reads it as the module's user
		dir, err := os.MkdirTemp(coproWorkDir, "copro-*")
		if err != nil {
			return Result{}, err
		}
		defer os.RemoveAll(dir)
		if err := os.Chmod(dir, 0o755); err != nil {
			return Result{}, err
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return Result{}, err
		}
		name := filepath.Base(file)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "fwmap"), []byte("CCU2 "+name+" "+version+"\n"), 0o644); err != nil {
			return Result{}, err
		}
		ctx1, cancel1 := context.WithTimeout(ctx, coproLegacyT1)
		r, err := l.runFlasher(ctx1, coproLegacyT1, devnode, coproConfigCmd, []string{"update-coprocessor", "-p", devnode, "-t", "HM-MOD-UART", "-u", "-d", dir})
		cancel1()
		if err == nil && r.Exit == 0 {
			return r, nil
		}
		// S48 retries once, forced, with twice the time
		ctx2, cancel2 := context.WithTimeout(ctx, coproLegacyT2)
		defer cancel2()
		r2, err2 := l.runFlasher(ctx2, coproLegacyT2, devnode, coproConfigCmd, []string{"update-coprocessor", "-p", devnode, "-t", "HM-MOD-UART", "-u", "-f", "-d", dir})
		r2.Stdout = append(append(r.Combined(), []byte("\n--- retry with -f ---\n")...), r2.Stdout...)
		return r2, err2
	}
	return Result{}, fmt.Errorf("unknown coprocessor family %q", family)
}

// unameMachine is `uname -m`, what S48 passes as -Dos.arch (aarch64, x86_64, armv7l).
func unameMachine() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return runtime.GOARCH
	}
	b := make([]byte, 0, len(u.Machine))
	for _, c := range u.Machine {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

func (Local) RunAs(ctx context.Context, cred Credential, name string, args []string, env []string, dir string, stdin []byte) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytesReader(stdin)
	}
	if cred.UID != 0 || cred.GID != 0 {
		groups := make([]uint32, 0, len(cred.Groups))
		for _, g := range cred.Groups {
			groups = append(groups, uint32(g))
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(cred.UID), Gid: uint32(cred.GID), Groups: groups, NoSetGroups: len(groups) == 0}}
	}
	var out, errb bytesBuffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	default:
		return res, err
	}
	return res, nil
}

func (l Local) WriteFile(path string, data []byte, mode os.FileMode) error {
	if l.noFollow {
		return l.writeFileNoFollow(path, data, mode)
	}
	// B-53: /etc/hostname and /etc/hosts are symlinks into /var/etc on the image, and the
	// rootfs is read-only - a temporary file beside the *link* fails with "read-only file
	// system". Write beside, and rename onto, the file the link points to.
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	} else {
		path = throughLinks(path)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// throughLinks resolves path component by component, following symlinks whose target does not
// exist yet as well - EvalSymlinks refuses those. On the image /etc/config is a link to
// ../usr/local/etc/config, and on a fresh userfs (only .doFactoryReset on it) that directory is
// made by the init scripts, after the firewall's rules are loaded; the first boot's
// `occulited firewall load` then found a dangling link and could not create its file, and
// occu-firewall.service failed before any iptables call (openccu-lite B-188). With the link
// resolved the write creates the directory itself.
func throughLinks(path string) string {
	path = filepath.Clean(path)
	cur := ""
	if filepath.IsAbs(path) {
		cur = string(filepath.Separator)
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next := filepath.Join(cur, part)
		for hops := 0; hops < 40; hops++ {
			fi, err := os.Lstat(next)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				break
			}
			target, err := os.Readlink(next)
			if err != nil {
				break
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(next), target)
			}
			next = filepath.Clean(target)
		}
		cur = next
	}
	return cur
}

func (l Local) Touch(path string, mode os.FileMode) error {
	if l.noFollow {
		return l.touchNoFollow(path, mode)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	return f.Close()
}

func (l Local) Remove(path string) error {
	if l.noFollow {
		return l.removeNoFollow(path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l Local) RemoveAll(path string) error {
	if l.noFollow {
		return l.removeAllNoFollow(path)
	}
	return os.RemoveAll(path)
}

func (l Local) MkdirAll(path string, mode os.FileMode) error {
	if l.noFollow {
		return l.mkdirAllNoFollow(path, mode)
	}
	return os.MkdirAll(path, mode)
}

func (l Local) Symlink(target, link string) error {
	if l.noFollow {
		return l.symlinkNoFollow(target, link)
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(target, link)
}

func (l Local) Rename(src, dst string) error {
	if l.noFollow {
		return l.renameNoFollow(src, dst)
	}
	return os.Rename(src, dst)
}

func (l Local) Chmod(path string, mode os.FileMode) error {
	if l.noFollow {
		return l.chmodNoFollow(path, mode)
	}
	return os.Chmod(path, mode)
}

func (Local) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// Open is O_NOFOLLOW and regular-files-only: the caller names a file in a directory addons write
// into, so the last component must not be a symlink into /etc, and a fifo would hang the answer.
// What is *under* that directory is the Server's business (Policy.SendfileDirs, and the
// /proc/self/fd check that no symlink in between led out of it).
func (Local) Open(path string) (*os.File, error) {
	// O_NONBLOCK as well, because an addon can leave anything in that directory: opening a fifo
	// nobody writes to would otherwise block the helper's goroutine for good.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("open %s: not a regular file", path)
	}
	return f, nil
}

// passwordHashRe is what may be written into the password field. It is not a parser for one
// crypt scheme - the box makes sha512 hashes today and a firmware may make another tomorrow -
// but it is exact about the thing that matters: a value that starts with the "$id$" marker of a
// modern crypt hash and carries nothing but that alphabet. A ":" would add fields to the line, a
// newline would add lines to the file, and neither can get through here.
var passwordHashRe = regexp.MustCompile(`^\$[0-9a-z]{1,4}\$[A-Za-z0-9./=,+$-]{6,250}$`)

// validPasswordHash guards the one operation that writes into the password file.
func validPasswordHash(hash string) bool {
	return strings.Count(hash, "$") >= 2 && passwordHashRe.MatchString(hash)
}

// SetRootPasswordHash rewrites exactly one field of exactly one line: the password of "root:".
// Every other line, and every other field of that line (the ageing values), is written back
// unchanged, and the file keeps its mode and its owner.
func (Local) SetRootPasswordHash(path, hash string) error {
	if !validPasswordHash(hash) {
		return errors.New("not a usable password hash")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	done := false
	for i, l := range lines {
		if !strings.HasPrefix(l, "root:") {
			continue
		}
		f := strings.Split(l, ":")
		if len(f) < 2 {
			continue
		}
		f[1] = hash
		lines[i] = strings.Join(f, ":")
		done = true
	}
	if !done {
		return errors.New("no root entry in shadow")
	}
	mode := os.FileMode(0o640)
	uid, gid := -1, -1
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(sys.Uid), int(sys.Gid)
		}
	}
	if err := (Local{}).WriteFile(path, []byte(strings.Join(lines, "\n")), mode); err != nil {
		return err
	}
	if uid >= 0 {
		_ = os.Lchown(path, uid, gid)
	}
	return nil
}

// WriteCertificate checks the PEM, writes it 0640 and, as root on a box that has the group,
// makes it root:certs - what lite-cert-perms does after a lighttpd reload, done here already so
// the confined addons restarted next can read it at once - and the two markers beside it.
func (Local) WriteCertificate(path string, pem []byte, mode string) error {
	if !ValidCertMode(mode) {
		return fmt.Errorf("certificate mode %q is neither %s nor %s", mode, CertModeACME, CertModeManual)
	}
	if err := certpem.ValidLivePEM(pem); err != nil {
		return fmt.Errorf("not a certificate file: %w", err)
	}
	gid := certsGID()
	perm := os.FileMode(0o640)
	if gid < 0 || syscall.Geteuid() != 0 {
		perm = 0o600
	}
	if err := (Local{}).WriteFile(path, pem, perm); err != nil {
		return err
	}
	if gid >= 0 && syscall.Geteuid() == 0 {
		if err := os.Lchown(path, 0, gid); err != nil {
			return err
		}
	}
	certs, err := certpem.ParseCertificates(pem)
	if err != nil {
		return err
	}
	line := strings.ReplaceAll(certs[0].Issuer.String(), "\n", " ")
	if err := (Local{}).WriteFile(path+ManagedSuffix, []byte(mode+" "+line+"\n"), 0o644); err != nil {
		return err
	}
	return (Local{}).WriteFile(path+MarkerSuffix, []byte(line+"\n"), 0o644)
}

// certsGID is the certs group's gid, -1 when the box has none.
func certsGID() int {
	g, err := user.LookupGroup(CertsGroup)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(g.Gid)
	if err != nil {
		return -1
	}
	return n
}

// ReadCertificate strips the key and reads the marker beside the file.
func (Local) ReadCertificate(path string) ([]byte, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	marker := ""
	if m, err := os.ReadFile(path + ManagedSuffix); err == nil {
		marker = strings.TrimSpace(string(m))
	} else if m, err := os.ReadFile(path + MarkerSuffix); err == nil && strings.TrimSpace(string(m)) != "" {
		marker = CertModeACME + " " + strings.TrimSpace(string(m))
	}
	return certpem.CertificatesOnly(b), marker, nil
}

// accountMu serialises the read-check-append of the account files: the helper serves
// connections concurrently, and two installs at once must not both decide a line is missing.
var accountMu sync.Mutex

func (Local) AddAddonUser(passwd, group, name string, uid int) error {
	if !ValidAddonUser(name, uid) {
		return fmt.Errorf("not an addon account: %s (%d)", name, uid)
	}
	accountMu.Lock()
	defer accountMu.Unlock()
	// the group first, as addgroup came before adduser: a failure between the two leaves a
	// state the next call completes
	if err := appendAccountLine(group, name, uid, fmt.Sprintf("%s:x:%d:", name, uid)); err != nil {
		return fmt.Errorf("group: %w", err)
	}
	home := AddonHomeDir + strings.TrimPrefix(name, AddonUserPrefix)
	if err := appendAccountLine(passwd, name, uid, fmt.Sprintf("%s:x:%d:%d::%s:/bin/false", name, uid, uid, home)); err != nil {
		return fmt.Errorf("passwd: %w", err)
	}
	return nil
}

// appendAccountLine adds line to a passwd- or group-shaped file unless a line with the same name
// and number is there already, and refuses when either is taken by something else. The read is a
// plain read of the path - what the bind mount presents - and the write is an O_APPEND write to
// the same inode, so a file that was mounted over another stays the file that was mounted.
func appendAccountLine(path, name string, id int, line string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) < 3 {
			continue
		}
		sameName, sameID := f[0] == name, f[2] == strconv.Itoa(id)
		switch {
		case sameName && sameID:
			return nil // already there
		case sameName:
			return fmt.Errorf("%s exists with id %s, not %d", name, f[2], id)
		case sameID:
			return fmt.Errorf("id %d belongs to %s, not %s", id, f[0], name)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		line = "\n" + line
	}
	if _, err := f.Write([]byte(line + "\n")); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Chown's recursive form was filepath.WalkDir with os.Lchown until task 107: a walk by path
// strings, which opens every directory by its name again - a directory the addon's user swapped for
// a link between the walk's lstat and its open was entered, and root changed owners wherever the
// link led. It is ownwalk's descriptor walk now.
func (l Local) Chown(path string, uid, gid int, recursive bool) error {
	if !recursive {
		if l.noFollow {
			return l.lchownNoFollow(path, uid, gid)
		}
		return os.Lchown(path, uid, gid)
	}
	res := ownwalk.Own([]string{path}, ownwalk.Options{UID: uid, GID: gid})
	if p := res.Problem(); p != "" {
		return fmt.Errorf("chown %s: %s", path, p)
	}
	for _, s := range res.Skipped {
		if strings.HasSuffix(s, ": does not exist") {
			return fmt.Errorf("chown %s: %w", path, os.ErrNotExist)
		}
	}
	return nil
}

// OwnTreeOptions say how OwnTree walks.
type OwnTreeOptions struct {
	// DryRun only counts what is wrong and changes nothing.
	DryRun bool
	// Quick looks at the top directories and their direct entries only (task 110,
	// ownwalk.QuickDepth): B-92's hourly look. The whole tree is walked after an install or update of
	// the addon, and by Fix ownership.
	Quick bool
}

// OwnTree is the walk itself; the checks of what may be walked are the helper's
// (Policy.OwnTreeAllowed), and the subcommand's that runs it as root.
func (Local) OwnTree(_ string, dirs []string, uid int, opt OwnTreeOptions) (ownwalk.Result, error) {
	if uid <= 0 {
		return ownwalk.Result{}, fmt.Errorf("not an addon's uid: %d", uid)
	}
	o := ownwalk.Options{UID: uid, GID: uid, DryRun: opt.DryRun}
	if opt.Quick {
		o.MaxDepth = ownwalk.QuickDepth
	}
	return ownwalk.Own(dirs, o), nil
}

// ExitError is returned by callers that want "non-zero exit" as an error, in exec's shape.
type ExitError struct {
	Code   int
	Output []byte
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// ExitCode mirrors exec.ExitError.
func (e *ExitError) ExitCode() int { return e.Code }

// AsExitError turns a Result into (output, error) in exec.CombinedOutput's shape.
func AsExitError(r Result, err error) ([]byte, error) {
	if err != nil {
		return r.Combined(), err
	}
	if r.Exit != 0 {
		return r.Combined(), &ExitError{Code: r.Exit, Output: r.Combined()}
	}
	return r.Combined(), nil
}

// IsRoot reports whether this process can do the operations itself.
func IsRoot() bool { return syscall.Geteuid() == 0 }
