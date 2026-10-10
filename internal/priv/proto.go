package priv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"github.com/hobbyquaker/occulited/internal/certpem"
	"github.com/hobbyquaker/occulited/internal/ownwalk"
)

// The wire format: one JSON object per line each way over a unix stream socket, one request
// per connection (the helper serves connections concurrently; a long install does not block a
// firewall write).

type request struct {
	Op        string   `json:"op"`
	Name      string   `json:"name,omitempty"`
	Args      []string `json:"args,omitempty"`
	Stdin     []byte   `json:"stdin,omitempty"`
	Path      string   `json:"path,omitempty"`
	Data      []byte   `json:"data,omitempty"`
	Mode      uint32   `json:"mode,omitempty"`
	Target    string   `json:"target,omitempty"`
	Src       string   `json:"src,omitempty"`
	Dst       string   `json:"dst,omitempty"`
	UID       int      `json:"uid,omitempty"`
	GID       int      `json:"gid,omitempty"`
	Recursive bool     `json:"recursive,omitempty"`
	Env       []string `json:"env,omitempty"`
	Dir       string   `json:"dir,omitempty"`
	Groups    []int    `json:"groups,omitempty"`
	Hash      string   `json:"hash,omitempty"`
	// GroupFile is the second file of the addon-account operation (Path is the passwd file).
	GroupFile string `json:"group_file,omitempty"`
	// CertMode is writecert's mode, acme or manual - what the marker beside the file says.
	CertMode string `json:"cert_mode,omitempty"`
	// Version is flashcopro's target version - what the legacy flasher's fwmap line carries.
	Version string `json:"version,omitempty"`
	// LEDs is the led operation's frame (Path is the LED class directory).
	LEDs []LEDWrite `json:"leds,omitempty"`
	// AllFiles is listlogs' every-file flag (Path is the directory): every regular file, not only
	// those named like logs - admitted under Policy.LogListEveryFileDirs alone (B-113).
	AllFiles bool `json:"all_files,omitempty"`
	// DryRun makes owntree count only.
	DryRun bool `json:"dry_run,omitempty"`
	// Quick makes owntree look at the top directories and their direct entries only (task 110).
	Quick bool `json:"quick,omitempty"`
	// FWv4, FWv6 and WindowMS are firewall-load's texts and confirm window (task 157).
	FWv4     []byte `json:"fw_v4,omitempty"`
	FWv6     []byte `json:"fw_v6,omitempty"`
	WindowMS int64  `json:"window_ms,omitempty"`
	// PolicyFile is addon-policy-file's content: what the drop-in, the start order or the early
	// start says, which the helper renders (openccu-lite B-293).
	PolicyFile *addonunit.File `json:"policy_file,omitempty"`
	// Enabled is addon-enable's direction; WWW is addon-remove's web entry (Path is the rc.d
	// entry, Recursive removes the web directory with what is in it).
	Enabled bool   `json:"enabled,omitempty"`
	WWW     string `json:"www,omitempty"`
}

type response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Exit   int    `json:"exit,omitempty"`
	Stdout []byte `json:"stdout,omitempty"`
	Stderr []byte `json:"stderr,omitempty"`
	// Marker is readcert's second answer: the marker's line ("<mode> <issuer>"), empty when
	// there is none.
	Marker string `json:"marker,omitempty"`
	// Files is listlogs' answer: paths, sizes and times, never content (B-113).
	Files []LogFileInfo `json:"files,omitempty"`
	// Names is listdir's answer: the entries' names, nothing else (openccu-lite B-253).
	Names []string `json:"names,omitempty"`
	// Errno is the failed open's errno name (shareopen, B-217), so the caller can tell a file
	// that is not there from one it may not read; addon-fragment's says why a fragment cannot be
	// taken (occulited B-35).
	Errno string `json:"errno,omitempty"`
}

// opRootPassword replaces the password field of the "root:" line of the shadow file the policy
// names. It exists so that /etc/config/shadow does not have to become readable to the
// unprivileged side: the hashes never leave the helper (B-15).
const opRootPassword = "rootpassword"

// opWriteCert and opReadCert are the live TLS file's operations (task 35): one exact path
// (Policy.CertPaths), the data checked to be a chain plus its key before root writes it, and
// only the certificate blocks ever read back.
const (
	opWriteCert = "writecert"
	opReadCert  = "readcert"
)

// opAddonUser creates a confined addon's user and group by appending to the two account files
// in place (B-54). Two exact paths (Policy.AccountFiles), a name of the addon-<id> shape and a uid
// from Policy.AddonUIDBase up; busybox adduser and addgroup, which this replaces, are no longer
// on the program list.
const opAddonUser = "addonuser"

// opOpen is the descriptor-passing operation: its answer carries the file descriptor as
// ancillary data, so it is the one request the Server does not answer through do().
const opOpen = "openfd"

// opFlashCopro flashes a radio module's coprocessor (task 41). The helper builds the flasher's
// command line itself from three checked pieces - the module family, the raw-uart node and the
// firmware file under one of Policy.CoproFirmwareDirs - so that neither /opt/java/bin/java nor
// an arbitrary "-jar" ever joins the Programs list: a policy that let any jar run as root would
// be a root shell with extra steps (the same reasoning as rootpassword, B-15, and openfd, B-35).
const opFlashCopro = "flashcopro"

// opSmartctl reads one disk's SMART data (task 69). The request carries the device and nothing
// else; the helper runs SmartctlArgs itself, so no option ever travels over the socket and
// smartctl never joins the Programs list (the same reasoning as flashcopro).
const opSmartctl = "smartctl"

// opFirewallInput lists one address family's INPUT chain (B-152). The request carries the family
// and nothing else; the helper runs FirewallInputArgs itself, so iptables never joins the Programs
// list (the same reasoning as smartctl).
const opFirewallInput = "firewall-input"

// The firewall's load, confirm and revert, and the socket owners (task 157, firewall.go). The load
// carries the two texts and the window and nothing else; confirm, revert and the owners carry
// nothing.
const (
	opFirewallLoad     = "firewall-load"
	opFirewallConfirm  = "firewall-confirm"
	opFirewallRevert   = "firewall-revert"
	opSocketOwners     = "socket-owners"
	opFirewallCounters = "firewall-counters"
)

// opLogLevel sets the helper's own log level (task 101): occulited's level follows the Log
// settings while it runs, and the helper's follows occulited's. The request carries the level's
// name and nothing else.
const opLogLevel = "loglevel"

// opOwnTree gives a confined addon's directories to its user (task 107, B-92). The request names the
// addon (Name), its directories (Args) and its uid; the helper walks only what Policy.OwnTreeAllowed
// admits and answers ownwalk's result as JSON in Stdout.
const opOwnTree = "owntree"

// LogLevelNames are the names opLogLevel takes (logctl.Levels).
var LogLevelNames = []string{"debug", "info", "warn", "error"}

// The two flasher families S48UpdateRFHardware knows, and the HM-CFG-USB-2's (task 147).
const (
	CoproHmIP   = "hmip"   // RPI-RF-MOD and HmIP-RFUSB: hmip-copro-update.jar
	CoproLegacy = "legacy" // HM-MOD-RPI-PCB: eq3configcmd update-coprocessor
	// CoproHMCFGUSB is the HM-CFG-USB-2: hmcfgusb's flash-hmcfgusb with the adapter's serial; the
	// "device" is usb:<serial>, the file hmusbif.<hex>.enc
	CoproHMCFGUSB = "hmcfgusb"
)

// HMCFGUSBDevice is the device shape of the HM-CFG-USB-2's flash: usb:<serial>.
var HMCFGUSBDevice = regexp.MustCompile(`^usb:([A-Z]{3}[0-9]{7})$`)

// Client talks to the helper.
type Client struct {
	Socket string
}

// ErrRefused is what the helper answers for an operation outside its allowlists.
var ErrRefused = errors.New("refused by the privilege helper")

// ErrNotAvailable is what the helper answers when the box does not have what the operation runs:
// smartctl on the VM and the container products, which do not build smartmontools (task 111). It
// is neither a refusal nor a failure; the caller says so quietly.
var ErrNotAvailable = errors.New("not available on this system")

func (c Client) call(ctx context.Context, req request) (response, error) {
	return c.callWith(ctx, req, nil)
}

// callWith is call with a descriptor sent along with the request (SCM_RIGHTS in the same message):
// the helper reads the daemon's file from it and never opens a path the daemon names (storecopy).
func (c Client) callWith(ctx context.Context, req request, f *os.File) (response, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return response{}, fmt.Errorf("privilege helper: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, fmt.Errorf("privilege helper: %w", err)
	}
	var res response
	r := bufio.NewReaderSize(conn, 1<<20)
	dec := json.NewDecoder(r)
	if f != nil {
		// the helper says it is ready for the descriptor, which goes with one byte of its own
		var ready response
		if err := dec.Decode(&ready); err != nil {
			return response{}, fmt.Errorf("privilege helper: %w", err)
		}
		if err := ready.err(); err != nil {
			return ready, err
		}
		uc, ok := conn.(*net.UnixConn)
		if !ok {
			return response{}, fmt.Errorf("privilege helper: %s is not a unix socket", c.Socket)
		}
		if _, _, err := uc.WriteMsgUnix([]byte{0}, syscall.UnixRights(int(f.Fd())), nil); err != nil {
			return response{}, fmt.Errorf("privilege helper: %w", err)
		}
	}
	if err := dec.Decode(&res); err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, fmt.Errorf("privilege helper: %w", err)
	}
	if err := res.err(); err != nil {
		return res, err
	}
	return res, nil
}

// notExistError is the helper's answer for a file that is not there, with its own text: it is
// fs.ErrNotExist to errors.Is, so a caller that reads through the helper can tell a missing file
// from one it could not read (openccu-lite B-271).
type notExistError string

func (e notExistError) Error() string        { return string(e) }
func (e notExistError) Is(target error) bool { return target == fs.ErrNotExist }

// err turns a helper answer into the client's error: a refusal keeps ErrRefused and a missing
// program ErrNotAvailable, so callers can tell "not allowed" and "not here" from "it broke".
func (r response) err() error {
	if r.OK {
		return nil
	}
	if strings.HasPrefix(r.Error, "refused:") {
		return fmt.Errorf("%w: %s", ErrRefused, strings.TrimPrefix(r.Error, "refused: "))
	}
	if strings.HasPrefix(r.Error, "not-available:") {
		return fmt.Errorf("%w: %s", ErrNotAvailable, strings.TrimPrefix(r.Error, "not-available: "))
	}
	if strings.HasSuffix(r.Error, ": "+syscall.ENOENT.Error()) {
		return notExistError(r.Error)
	}
	return errors.New(r.Error)
}

func (c Client) RunAs(ctx context.Context, cred Credential, name string, args []string, env []string, dir string, stdin []byte) (Result, error) {
	res, err := c.call(ctx, request{Op: "runas", Name: name, Args: args, Env: env, Dir: dir, Stdin: stdin, UID: cred.UID, GID: cred.GID, Groups: cred.Groups})
	return Result{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit}, err
}

func (c Client) Run(ctx context.Context, name string, args []string, stdin []byte) (Result, error) {
	res, err := c.call(ctx, request{Op: "run", Name: name, Args: args, Stdin: stdin})
	return Result{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit}, err
}

// file operations get a generous deadline of their own (a recursive chown of an addon tree
// can take a while on an SD card)
func (c Client) fileOp(req request) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, err := c.call(ctx, req)
	return err
}

func (c Client) WriteFile(path string, data []byte, mode os.FileMode) error {
	return c.fileOp(request{Op: "write", Path: path, Data: data, Mode: uint32(mode)})
}

func (c Client) Touch(path string, mode os.FileMode) error {
	return c.fileOp(request{Op: "touch", Path: path, Mode: uint32(mode)})
}

func (c Client) Remove(path string) error { return c.fileOp(request{Op: "remove", Path: path}) }

func (c Client) RemoveAll(path string) error { return c.fileOp(request{Op: "removeall", Path: path}) }

func (c Client) MkdirAll(path string, mode os.FileMode) error {
	return c.fileOp(request{Op: "mkdir", Path: path, Mode: uint32(mode)})
}

func (c Client) Symlink(target, link string) error {
	return c.fileOp(request{Op: "symlink", Target: target, Path: link})
}

func (c Client) Rename(src, dst string) error {
	return c.fileOp(request{Op: "rename", Src: src, Dst: dst})
}

func (c Client) ReadFile(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := c.call(ctx, request{Op: "read", Path: path})
	return res.Stdout, err
}

// SetRootPasswordHash asks the helper to put hash into the "root:" line of the shadow file. What
// travels is the new hash and nothing else - not the file, not the hashes that are in it.
func (c Client) SetRootPasswordHash(path, hash string) error {
	return c.fileOp(request{Op: opRootPassword, Path: path, Hash: hash})
}

// Open asks the helper to open the file as root and to pass the descriptor back over the socket
// (SCM_RIGHTS). Nothing of the file's content travels through the socket and the unprivileged
// side never holds the privilege to name the path again: what it gets is one read-only
// descriptor of one regular file the helper's SendfileDirs allowlist named (B-35).
func (c Client) Open(path string) (*os.File, error) { return c.openOp(opOpen, path) }

// openOp is Open for the descriptor-passing operations (openfd, shareopen).
func (c Client) openOp(op, path string) (*os.File, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, fmt.Errorf("privilege helper: %w", err)
	}
	defer conn.Close()
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("privilege helper: %s is not a unix socket", c.Socket)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = uc.SetDeadline(dl)
	}
	if err := json.NewEncoder(uc).Encode(request{Op: op, Path: path}); err != nil {
		return nil, fmt.Errorf("privilege helper: %w", err)
	}
	// the answer is one message: the JSON line, with the descriptor as its ancillary data
	buf := make([]byte, 1<<16)
	oob := make([]byte, syscall.CmsgSpace(4))
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, fmt.Errorf("privilege helper: %w", err)
	}
	fds := parseRights(oob[:oobn])
	var res response
	if err := json.Unmarshal(buf[:n], &res); err != nil {
		closeFDs(fds)
		return nil, fmt.Errorf("privilege helper: %w", err)
	}
	if err := res.err(); err != nil {
		closeFDs(fds)
		if res.Errno != "" {
			return nil, &ShareError{Errno: res.Errno, Msg: err.Error()}
		}
		return nil, err
	}
	if len(fds) != 1 {
		closeFDs(fds)
		return nil, fmt.Errorf("privilege helper: %d descriptors for %s", len(fds), op)
	}
	return os.NewFile(uintptr(fds[0]), path), nil
}

// parseRights collects the descriptors of an SCM_RIGHTS control message; a malformed one yields
// none rather than an error, and every descriptor that is not wanted is closed by the caller.
func parseRights(oob []byte) []int {
	if len(oob) == 0 {
		return nil
	}
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	var fds []int
	for i := range msgs {
		if got, err := syscall.ParseUnixRights(&msgs[i]); err == nil {
			fds = append(fds, got...)
		}
	}
	return fds
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		_ = syscall.Close(fd)
	}
}

func (c Client) WriteCertificate(path string, pem []byte, mode string) error {
	return c.fileOp(request{Op: opWriteCert, Path: path, Data: pem, CertMode: mode})
}

func (c Client) ReadCertificate(path string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := c.call(ctx, request{Op: opReadCert, Path: path})
	return res.Stdout, res.Marker, err
}

func (c Client) AddAddonUser(passwd, group, name string, uid int) error {
	return c.fileOp(request{Op: opAddonUser, Path: passwd, GroupFile: group, Name: name, UID: uid})
}

// FlashCoprocessor asks the helper to run the flasher; the run takes minutes and the answer is
// its whole output at the end (the helper cannot stream).
func (c Client) FlashCoprocessor(ctx context.Context, family, devnode, file, version string) (Result, error) {
	res, err := c.call(ctx, request{Op: opFlashCopro, Name: family, Path: devnode, Src: file, Version: version})
	return Result{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit}, err
}

// Smartctl asks the helper for one disk's `smartctl -j -H -A -i` answer. Only the device travels.
func (c Client) Smartctl(ctx context.Context, device string) (Result, error) {
	res, err := c.call(ctx, request{Op: opSmartctl, Path: device})
	return Result{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit}, err
}

// FirewallInput asks the helper for one family's `-S INPUT` listing, "ipv4" or "ipv6". Only the
// family travels.
func (c Client) FirewallInput(ctx context.Context, family string) (Result, error) {
	res, err := c.call(ctx, request{Op: opFirewallInput, Name: family})
	return Result{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit}, err
}

// FirewallLoad sends the two texts and the window to the helper (task 157).
func (c Client) FirewallLoad(ctx context.Context, v4, v6 []byte, window time.Duration) error {
	_, err := c.call(ctx, request{Op: opFirewallLoad, FWv4: v4, FWv6: v6, WindowMS: window.Milliseconds()})
	return err
}

// FirewallConfirm closes the helper's confirm window.
func (c Client) FirewallConfirm(ctx context.Context) error {
	_, err := c.call(ctx, request{Op: opFirewallConfirm})
	return err
}

// FirewallRevert puts the window's tables back now.
func (c Client) FirewallRevert(ctx context.Context) error {
	_, err := c.call(ctx, request{Op: opFirewallRevert})
	return err
}

// FirewallCounters asks the helper for the filter tables with their counters.
func (c Client) FirewallCounters(ctx context.Context) (map[string][]byte, error) {
	res, err := c.call(ctx, request{Op: opFirewallCounters})
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SocketOwners asks the helper for the socket owners.
func (c Client) SocketOwners(ctx context.Context) ([]SocketOwner, error) {
	res, err := c.call(ctx, request{Op: opSocketOwners})
	if err != nil {
		return nil, err
	}
	var out []SocketOwner
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetLogLevel sets the helper's own log level: debug, info, warn or error (task 101).
func (c Client) SetLogLevel(ctx context.Context, level string) error {
	_, err := c.call(ctx, request{Op: opLogLevel, Name: level})
	return err
}

func (c Client) Chmod(path string, mode os.FileMode) error {
	return c.fileOp(request{Op: "chmod", Path: path, Mode: uint32(mode)})
}

func (c Client) Chown(path string, uid, gid int, recursive bool) error {
	return c.fileOp(request{Op: "chown", Path: path, UID: uid, GID: gid, Recursive: recursive})
}

// OwnTree asks the helper for the walk; a large tree on a slow card takes a while, as a chown -R did.
func (c Client) OwnTree(id string, dirs []string, uid int, opt OwnTreeOptions) (ownwalk.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := c.call(ctx, request{Op: opOwnTree, Name: id, Args: dirs, UID: uid, DryRun: opt.DryRun, Quick: opt.Quick})
	if err != nil {
		return ownwalk.Result{}, err
	}
	var out ownwalk.Result
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return ownwalk.Result{}, fmt.Errorf("privilege helper: %w", err)
	}
	return out, nil
}

// Policy is what the helper allows. Root is the filesystem root the paths are under ("/" on a
// box, a fake tree in development), so the allowlists are relative to it.
type Policy struct {
	Root string
	// Programs are the absolute programs, or bare names looked up in PATH, that may run - each
	// with the argument shapes of programShapes (shapes.go), never with free arguments.
	Programs []string
	// ProgramDirs are directories whose executables may run (init scripts, rc.d scripts), with
	// one action word (dirScriptShape).
	ProgramDirs []string
	// RunUnitDir is systemd's runtime unit directory, /run/systemd/system/ (task 27.4, task 50).
	// It is not a prefix grant: under it write and remove admit only <unit>.d/50-occulite.conf
	// for a .service or .timer and an own timer's local-<name>.timer and local-<name>.service,
	// and mkdir only such a <unit>.d. Empty = nothing there.
	RunUnitDir string
	// Paths are what may be written, removed, linked, renamed: a directory entry ends in "/" and
	// admits everything below it, an entry with a "*" is a pattern for one name, and any other
	// entry is exactly one file (pathEntryAllows, B-6).
	Paths []string
	// StagingDirs are where the unprivileged side writes big files it then asks to Rename
	// into a Path: the addon archive, an update image.
	StagingDirs []string
	// AddonDataDirs are the prefixes under which a directory may be created and chowned - and
	// nothing else - although it is not on Paths: an addon's own data directory on the userfs,
	// /usr/local/<id> or whatever its catalogue entry declares (D-52, B-62), which a confined
	// addon has to own and cannot create itself. The shared trees directly below such a prefix
	// (UserfsSharedDirs) and the prefix itself are never reachable through this list, and
	// neither is a dotfile there.
	AddonDataDirs []string
	// ReadPaths are root-only files the daemon may read through the helper.
	ReadPaths []string
	// ReadGlobs are shapes of such files (filepath.Match, one directory level: "*" never crosses a
	// "/"): hmipserver's access-point identity files, whose names carry the module's SGTIN
	// (openccu-lite B-253, listdir.go). Nothing else in that directory matches.
	ReadGlobs []string
	// ListDirs are the directories whose entry names the daemon may ask for (listdir.go): exact
	// directories, no path below one. hmipserver's data directory, 0700 since B-253.
	ListDirs []string
	// ShadowPaths are the password files whose "root:" line may be rewritten
	// (SetRootPasswordHash). Exact paths, and deliberately not on ReadPaths: the operation
	// writes one field of one line and the file itself stays unreadable to the daemon (B-15).
	ShadowPaths []string
	// AuthorizedKeys is root's authorized_keys (task 185): read whole, and written only as
	// occulited's section of it (ssh.go). Exact, and not on ReadPaths or the write prefixes.
	AuthorizedKeys string
	// ProcDir is where ssh-end looks a pid up; "" = <Root>/proc. A test's fake tree.
	ProcDir string
	// CertPaths is the live TLS file, /etc/config/server.pem, exact: the one path WriteCertificate
	// may write (as root:certs 0640, after checking the PEM) and ReadCertificate may read (the
	// certificate blocks only). Not on Paths' terms - /etc/config/ is a prefix there, and a
	// generic write of a trust anchor is exactly what task 35 was told not to add, and since
	// B-238 the generic operations refuse it although the prefix covers it (namedonly.go). The other
	// entries are the markers the operation writes beside it (path+ManagedSuffix, and task
	// 35's path+MarkerSuffix), listed so the list says everything the operation touches; a
	// marker is never a certificate path of its own.
	CertPaths []string
	// AccountFiles are the passwd file and the group file, in that order, exact: the two files
	// AddAddonUser appends to. Not on Paths - a generic write, remove or rename of /etc/passwd
	// is not something the daemon gets, and WriteFile's temporary file plus rename cannot land
	// on a bind-mounted file in a read-only /etc anyway (B-54). Nothing is read back through
	// this list.
	AccountFiles []string
	// SendfileDirs are the directories whose regular files the helper opens and hands back as a
	// descriptor (Open, SCM_RIGHTS): the file an addon's CGI names in an X-Sendfile answer. A
	// directory here is not a read grant - ReadPaths stays exact and stays separate (B-35).
	SendfileDirs []string
	// CoproFirmwareDirs are the directories a coprocessor firmware file may be flashed from
	// (FlashCoprocessor): the image's /firmware/ and the upload directory on the userfs. The
	// device node is not a policy list but a shape, /dev/raw-uart<n>, checked at the boundary.
	CoproFirmwareDirs []string
	// CGIRoots hold the addon web trees; CGIInterpreters may run a script from them (RunAs).
	CGIRoots        []string
	CGIInterpreters []string
	// AddonRCDir is the addons' rc.d directory: a script there may run as an addon user (RunAs)
	// with one of addonRCActions - a confined addon's info, init and uninstall are its own code
	// and run as its user, not as root (openccu-lite B-119). Empty = none.
	AddonRCDir string
	// AddonUIDBase: RunAs may switch to uids from here up (the addon users), or stay root.
	AddonUIDBase int
	// AddonPolicyDir is where the addon policies are (openccu-lite B-293): its <id>.conf, .needs
	// and .start are root's to obey and written by AddonPolicyFile alone, never by the generic
	// operations; the other files there (the stored policy and manifest) are the daemon's.
	AddonPolicyDir string
	// LEDDir is the LED class directory the led operation writes under, and LEDNames the LEDs it
	// may write there (task 95): the RPI-RF-MOD's three and the red power LED. Exact names.
	LEDDir   string
	LEDNames []string
	// LogListDirs are the directories the log listing may walk (B-113, loglist.go): the addons'
	// trees and /var/log, where a directory only its owner reads is out of the daemon's sight.
	// LogListEveryFileDirs are those among them where every regular file counts, as the storage
	// hint counts them under /var/log; elsewhere only a file named like a log.
	LogListDirs          []string
	LogListEveryFileDirs []string
}

// DefaultPolicy is the box's.
func DefaultPolicy(root, stateDir string) Policy {
	return Policy{
		Root: root,
		// every name here has an argument shape in programShapes (shapes.go, openccu-lite B-234):
		// the list used to check the program and pass its arguments through, which let the daemon
		// run `chown -R` on anything as root (task 107 took chown off for that) and, until B-234,
		// `systemd-run /bin/sh -c …`; an addon's files go through owntree, which walks without
		// following a link and admits one addon's directories and uid
		Programs: []string{"systemctl", "systemd-run", "kill", "hostname", "date", "sh",
			"/sbin/ip", "/sbin/udhcpc", "/sbin/udhcpc6", "/sbin/ifconfig", "/sbin/resolvconf", "/sbin/hwclock", "/sbin/reboot", "/bin/reboot",
			// the power menu's halt on a busybox box, where the two paths are busybox's applet (a
			// systemd box halts through systemctl, which is on the list already)
			"/sbin/poweroff", "/bin/poweroff",
			"/bin/crypttool", "/bin/install_addon", "/bin/restoreBackup.sh", "/bin/createBackup.sh", "/bin/cronBackup.sh",
			"/bin/SetInterfaceClock", "/bin/updateTZ.sh", "/bin/eq3configcmd", "/usr/bin/systemctl",
			"/usr/libexec/occu/lite-addon-rc", // 28.8: the addon-rc wrapper adopter
			// D-66: the writable extension directories' reset (the image's device descriptions
			// win again); the daemon reads their status itself
			"/usr/libexec/occu/lite-extension-dirs",
			// openccu-lite task 231: the CA bundle rebuilt after the Trust stores page changed the
			// userfs additions or the distrust file (the same script the boot runs)
			"/usr/libexec/occu/lite-ca-certificates",
			"/bin/detect_radio_module", // task 41: the coprocessor's running version, read off the raw-uart
			// openccu-lite B-281: the device import's one journal read (hmipserver's adapter exchange
			// lines; the shape fixes the unit, the pattern and the date's form)
			"journalctl"},
		ProgramDirs: []string{"/etc/init.d", "/usr/local/etc/config/rc.d"},
		Paths: []string{"/etc/config/", "/usr/local/etc/config/", "/usr/local/tmp/", "/usr/local/.firmwareUpdate", "/usr/local/.recoveryMode", "/usr/local/.doFactoryReset",
			// an addon's monit file, removed at its uninstall (system.RemoveAddon)
			"/usr/local/etc/monit-*.cfg", "/etc/hostname", "/etc/hosts",
			// B-53: on the image the two are links into /var/etc; a write resolves through them
			// (B-235) and the target has to be on the list as well
			"/var/etc/hostname", "/var/etc/hosts",
			// openccu-lite B-294: not /usr/local/addons/ - what a root addon's rc.d script starts
			// lies there, and root runs it; the daemon's three cases there are named operations
			// (addonhome.go), a confined addon's directory is its own
			"/var/run/", "/run/occulite/", "/sys/class/leds/",
			"/usr/local/backup/", "/media/",
			// openccu-lite task 145: the recovery system's install logs, written as root and
			// removed by the daemon once they are in the journal (system.RecoveryLogDir)
			"/usr/local/var/recovery/",
			// root's crontab, exact: the NEO Server leftover's watchdog line is removed from it
			// (task 37); nothing else in /usr/local/crontabs/ is the daemon's
			"/usr/local/crontabs/root",
			// openccu-lite task 231: the system trust store's userfs half - the administrator's CA
			// files update-ca-certificates adds, and the file whose "!<name>" lines deselect image
			// certificates; both exact to their purpose, nothing else lives there
			"/usr/local/share/ca-certificates/", "/usr/local/etc/ca-certificates.conf"},
		// task 27.4 and task 50: /run/systemd/system is not on Paths - a prefix there let the
		// daemon write any runtime unit, masks and wants links included. Exactly two shapes are
		// admitted under it (runUnitFileAllowed): <unit>.d/50-occulite.conf, and an own timer's
		// local-<name>.timer and local-<name>.service.
		RunUnitDir:  "/run/systemd/system/",
		StagingDirs: []string{filepath.Join(stateDir, "staging")},
		// D-52: an addon's data directory outside its three standard ones, taken over when the
		// addon is confined; the daemon applies the addon rules, the boundary the mechanical ones
		AddonDataDirs: []string{"/usr/local/"},
		// root-only files the daemon has to see to show and preserve them (B-1, B-13): the ReGa
		// database at first boot, netconfig (0600; the Network page and the rewrite that keeps
		// the keys it does not manage) and the two interface configs, whose [Interface N]
		// sections are the LAN gateway list. rfd.conf and hs485d.conf carry the gateways'
		// encryption keys: they stay inside occulited, which strips them from every answer, and
		// re-reads them so a write does not blank a key the caller did not resend. Nothing else
		// is readable - /etc/config/shadow in particular is not (see B-14).
		ReadPaths: []string{
			"/etc/config/homematic.regadom", "/etc/config/homematic.regadom.bak",
			"/usr/local/etc/config/homematic.regadom", "/usr/local/etc/config/homematic.regadom.bak",
			"/etc/config/netconfig", "/usr/local/etc/config/netconfig",
			"/etc/config/rfd.conf", "/usr/local/etc/config/rfd.conf",
			"/etc/config/hs485d.conf", "/usr/local/etc/config/hs485d.conf",
			// task 149: 0640 once it carries the HmIP network key (local key mode)
			"/etc/config/crRFD/hmip_user.conf", "/usr/local/etc/config/crRFD/hmip_user.conf",
			// task 154: the HmIP devices' keys (0640, hmipserver's after its start); the page lists
			// the SGTINs, and only the confirmed key sheet answers a key
			"/etc/config/crRFD/sgtin.map", "/usr/local/etc/config/crRFD/sgtin.map",
			// task 143: classic RPC's pair (root 0600, lighttpd reads it); the page shows the user
			"/etc/config/classic-rpc.htpasswd", "/usr/local/etc/config/classic-rpc.htpasswd",
			// task 89: the Wi-Fi networks (root 0600, hashed PSKs and SAE passwords); the page lists
			// the SSIDs, a change keeps the other networks' keys, and no key leaves occulited
			"/etc/config/wpa_supplicant.conf", "/usr/local/etc/config/wpa_supplicant.conf",
		},
		// openccu-lite B-253: hmipserver's data directory is 0700 with 0600 files. The daemon
		// lists its names (which devices, which modules) and reads the module's three identity
		// files for the local key mode's snapshot (task 149, D-103). Since openccu-lite B-285
		// (maintainer, 2026-09-30) the device files too: the snapshot a connection change takes
		// before it moves HmIP-RF to another module keeps them, since hmipserver rewrites them
		// for the new access point and the way back needs the old ones. linkData.conf and
		// metaData.conf stay unreadable.
		ReadGlobs: []string{
			"/etc/config/crRFD/data/*.ap", "/usr/local/etc/config/crRFD/data/*.ap",
			"/etc/config/crRFD/data/*.apkx", "/usr/local/etc/config/crRFD/data/*.apkx",
			"/etc/config/crRFD/data/*.bbkx", "/usr/local/etc/config/crRFD/data/*.bbkx",
			"/etc/config/crRFD/data/*.dev", "/usr/local/etc/config/crRFD/data/*.dev",
		},
		ListDirs: []string{"/etc/config/crRFD/data", "/usr/local/etc/config/crRFD/data"},
		// the SSH page's "set root's password": the file is 0640 root:root and stays that way,
		// and the daemon never reads it - it hands over one hash and the helper puts it in
		// (B-15). /etc/config is the userfs path on every product; it is not a prefix.
		ShadowPaths: []string{"/etc/config/shadow"},
		// task 185: the Remote access page's keys (occulited's section of the file)
		AuthorizedKeys: AuthorizedKeysPath,
		// task 35: the live certificate lighttpd serves and the certs group reads (D-46)
		CertPaths: []string{"/etc/config/server.pem", "/etc/config/server.pem" + ManagedSuffix, "/etc/config/server.pem" + MarkerSuffix},
		// D-36's addon accounts: on the image the two are bind mounts of /run/openccu-lite/*
		// (occu-etc-writable), edited in place by the one operation that may touch them (B-54)
		AccountFiles: []string{"/etc/passwd", "/etc/group"},
		// the docroot lighttpd's cgi.x-sendfile used: an addon CGI writes its archive there and
		// names it, and a confined addon (D-36) writes it 0600 as its own user, which the
		// unprivileged daemon cannot open at all (B-35). The helper opens it and passes the
		// descriptor; nothing here becomes readable by path.
		SendfileDirs: []string{"/usr/local/tmp/"},
		// task 41: what the image ships and what the user uploaded (Radio firmware section)
		CoproFirmwareDirs: []string{"/firmware/", "/usr/local/etc/config/radio-firmware/"},
		CGIRoots:          []string{"/usr/local/etc/config/addons/www/", "/etc/config/addons/www/", "/www/addons/"},
		CGIInterpreters:   []string{"/bin/tclsh", "/usr/bin/tclsh"},
		AddonRCDir:        "/usr/local/etc/config/rc.d/",
		AddonUIDBase:      30000,
		AddonPolicyDir:    "/usr/local/etc/config/addon-policy/",
		// task 95: the status LED controller's frames
		LEDDir:   "/sys/class/leds",
		LEDNames: []string{"rpi_rf_mod:red", "rpi_rf_mod:green", "rpi_rf_mod:blue", "PWR"},
		// B-113: the storage hint's log files in directories the daemon cannot open
		LogListDirs:          []string{"/usr/local/addons/", "/var/log/"},
		LogListEveryFileDirs: []string{"/var/log/"},
	}
}

// addonRCActions are the rc.d actions an addon's script may be run with as the addon's user: what
// the CCU's WebUI, S55InitAddons and the uninstall call outside the unit. start, stop and restart
// are the unit's (the addon-rc wrapper routes them to systemd) and never come here.
var addonRCActions = map[string]bool{"info": true, "info.de": true, "info.en": true, "init": true, "uninstall": true}

// addonScriptAllowed: an rc.d script run as an addon user (never as root, never as one of the box's
// own users) with one of addonRCActions - the wrapper rc.d/<name>, which runs the addon's own script
// with the caller's credentials. The uid is not matched against the script's addon: the daemon
// chooses it from the addon's policy, and the helper has no cheaper truth about that mapping than
// the daemon's files.
func (p Policy) addonScriptAllowed(name string, args []string, uid int) bool {
	if p.AddonRCDir == "" || uid == 0 || uid < p.AddonUIDBase {
		return false
	}
	if len(args) == 0 || !addonRCActions[args[0]] {
		return false
	}
	rel, ok := p.rel(name)
	if !ok || strings.Contains(rel, "..") {
		return false
	}
	return filepath.Dir(rel)+"/" == p.AddonRCDir
}

// cgiAllowed: the script under a CGI root, run directly or by a listed interpreter, as root or
// as an addon user.
func (p Policy) cgiAllowed(name string, args []string, uid int) bool {
	if uid != 0 && uid < p.AddonUIDBase {
		return false
	}
	script := name
	interp := false
	for _, i := range p.CGIInterpreters {
		if name == i || (p.Root != "/" && strings.HasSuffix(name, i)) {
			interp = true
		}
	}
	if interp {
		if len(args) == 0 {
			return false
		}
		script = args[0]
	}
	rel, ok := p.rel(script)
	if !ok || strings.Contains(rel, "..") {
		return false
	}
	for _, r := range p.CGIRoots {
		if strings.HasPrefix(rel, r) {
			return true
		}
	}
	return false
}

func (p Policy) rel(path string) (string, bool) {
	path = filepath.Clean(path)
	root := filepath.Clean(p.Root)
	if root == "/" {
		return path, filepath.IsAbs(path) && !strings.Contains(path, "/../")
	}
	if !strings.HasPrefix(path, root+"/") {
		return "", false
	}
	return strings.TrimPrefix(path, root), true
}

func (p Policy) pathAllowed(path string) bool {
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") {
		return false
	}
	// B-238: a named operation's file is never a generic one's, whatever prefix holds it
	if p.namedOnly(rel) {
		return false
	}
	for _, a := range p.Paths {
		if pathEntryAllows(a, rel) {
			return true
		}
	}
	return false
}

// pathEntryAllows is the one rule for a Paths entry (occulited B-6): an entry ending in "/" is a
// directory - itself (the installer creates /usr/local/tmp before using it) and everything below
// it; an entry with a "*" is a filepath.Match pattern for one name, where "*" never crosses a "/"
// (/usr/local/etc/monit-*.cfg); every other entry is exactly one file. Before, an entry without
// the slash matched by prefix, so /usr/local/.recoveryMode also let /usr/local/.recoveryModeXYZ
// and /etc/hosts also /etc/hosts.allow through.
func pathEntryAllows(entry, rel string) bool {
	switch {
	case strings.HasSuffix(entry, "/"):
		return strings.HasPrefix(rel, entry) || rel == strings.TrimSuffix(entry, "/")
	case strings.Contains(entry, "*"):
		ok, err := filepath.Match(entry, rel)
		return err == nil && ok
	default:
		return rel == entry
	}
}

// localUnitNameRe is the one shape of an own unit's name, <name> in local-<name>.timer (task 50).
var localUnitNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// ValidLocalUnitName is the name rule of occulited's own units: 1 to 32 letters, digits, - and _,
// starting with a letter or a digit. The daemon checks it before it stores anything, and the
// helper checks it again on the path it is asked to write - the same function on both sides.
func ValidLocalUnitName(name string) bool { return localUnitNameRe.MatchString(name) }

// runDropInDirRe and runDropInRe are an override drop-in's directory and file for a service or a
// timer, the two kinds the unit editor admits (systemd's unit name characters: letters, digits,
// ":", "-", "_", ".", "\" and "@"; no "/", so nothing below the directory's first level).
var (
	runDropInDirRe = regexp.MustCompile(`^[A-Za-z0-9:_.@\\-]+\.(service|timer)\.d$`)
	runDropInRe    = regexp.MustCompile(`^[A-Za-z0-9:_.@\\-]+\.(service|timer)\.d/50-occulite\.conf$`)
)

// runUnitRel is path relative to RunUnitDir, when it lies below it.
func (p Policy) runUnitRel(path string) (string, bool) {
	if p.RunUnitDir == "" {
		return "", false
	}
	rel, ok := p.rel(path)
	dir := strings.TrimSuffix(p.RunUnitDir, "/") + "/"
	if !ok || strings.Contains(rel, "..") || !strings.HasPrefix(rel, dir) {
		return "", false
	}
	return strings.TrimPrefix(rel, dir), true
}

// runUnitFileAllowed: what write and remove may touch in systemd's runtime unit directory - an
// override drop-in (<unit>.d/50-occulite.conf, task 27.4), or an own timer's local-<name>.timer
// or local-<name>.service with a valid name (task 50). Not a runtime unit of any other name (it
// would shadow a shipped one), not a wants link, not a mask.
func (p Policy) runUnitFileAllowed(path string) bool {
	rest, ok := p.runUnitRel(path)
	if !ok {
		return false
	}
	if runDropInRe.MatchString(rest) {
		return true
	}
	if !strings.HasPrefix(rest, "local-") {
		return false
	}
	for _, suffix := range []string{".timer", ".service"} {
		if strings.HasSuffix(rest, suffix) {
			return ValidLocalUnitName(strings.TrimSuffix(strings.TrimPrefix(rest, "local-"), suffix))
		}
	}
	return false
}

// runUnitDirAllowed: the one directory mkdir may make there, a service's or a timer's <unit>.d.
func (p Policy) runUnitDirAllowed(path string) bool {
	rest, ok := p.runUnitRel(path)
	return ok && runDropInDirRe.MatchString(rest)
}

// UserfsSharedDirs are the entries directly below /usr/local that belong to the box or to every
// addon at once, never to one addon: no take-over (D-52) may chown one of them, and a candidate
// data directory that is one of them - or an ancestor of one - is refused on both sides of the
// privilege boundary.
var UserfsSharedDirs = []string{"addons", "etc", "tmp", "backup", "crontabs", "lost+found", "var", "sdcard", "usb"}

// dataDirAllowed (mkdir and chown): everything pathAllowed, plus a directory under an
// AddonDataDirs prefix that is at least one component below it, whose first component is not a
// shared tree and not a dotfile (/usr/local/.firmwareUpdate is a marker, not a directory of
// anybody's).
func (p Policy) dataDirAllowed(path string) bool {
	if p.pathAllowed(path) {
		return true
	}
	rel, ok := p.rel(path)
	// B-293: a file root runs or obeys is not made the daemon's by a chown either, wherever a link
	// in rc.d or a web tree put it
	if !ok || strings.Contains(rel, "..") || p.namedOnly(rel) {
		return false
	}
	for _, d := range p.AddonDataDirs {
		if !strings.HasSuffix(d, "/") || !strings.HasPrefix(rel, d) {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(rel, d), "/")
		if first == "" || strings.HasPrefix(first, ".") {
			return false
		}
		for _, shared := range UserfsSharedDirs {
			if first == shared {
				return false
			}
		}
		return true
	}
	return false
}

// OwnTreeAllowed is the boundary of owntree (task 107), and the check the root subcommand applies
// before its own walk: an addon id, that addon's user with a uid from AddonUIDBase up as the passwd
// file under Root has it, and 1 to 32 directories, each a clean path that is one of the addon's
// three standard directories or a data directory below /usr/local outside the shared trees (the
// shape of AddonDataDirs; which of those is really the addon's is the daemon's guard rail, D-52).
func (p Policy) OwnTreeAllowed(id string, dirs []string, uid int) error {
	name := AddonUserPrefix + id
	if !ValidAddonUser(name, uid) || uid < p.AddonUIDBase {
		return fmt.Errorf("not an addon account: %s (%d)", name, uid)
	}
	if len(p.AccountFiles) == 0 {
		return errors.New("no passwd file to check the uid against")
	}
	passwd, err := os.ReadFile(filepath.Join(p.Root, p.AccountFiles[0]))
	if err != nil {
		return fmt.Errorf("passwd: %w", err)
	}
	found := false
	for _, line := range strings.Split(string(passwd), "\n") {
		if f := strings.Split(line, ":"); len(f) >= 3 && f[0] == name {
			if f[2] != strconv.Itoa(uid) {
				return fmt.Errorf("%s has uid %s, not %d", name, f[2], uid)
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no user %s", name)
	}
	// openccu-lite B-295: only a confined addon's tree is given to its user - its drop-in, which
	// the helper renders itself, says so and names this uid. A root addon's tree handed to an
	// unprivileged uid would be a tree root runs that another user may change.
	if confined, ok := p.addonConfinedUID(id); !ok || confined != uid {
		return fmt.Errorf("%s is not confined to uid %d (its drop-in)", id, uid)
	}
	if len(dirs) == 0 || len(dirs) > 32 {
		return fmt.Errorf("%d directories", len(dirs))
	}
	standard := []string{AddonHomeDir + id, "/usr/local/etc/config/addons/www/" + id}
	if id != "www" { // config/addons/www is every addon's web directory, never one addon's
		standard = append(standard, "/usr/local/etc/config/addons/"+id)
	}
	for _, d := range dirs {
		// the path as given, not as rel cleans it: the walk takes it as given too
		if filepath.Clean(d) != d || strings.Contains(d, "/../") {
			return fmt.Errorf("not a clean path: %s", d)
		}
		rel, ok := p.rel(d)
		if !ok || strings.Contains(rel, "..") {
			return fmt.Errorf("not a clean path: %s", d)
		}
		if slices.Contains(standard, rel) || p.addonDataDirShape(rel) {
			continue
		}
		return fmt.Errorf("not a directory of %s: %s", id, d)
	}
	return nil
}

// addonDataDirShape: a directory at least one component below an AddonDataDirs prefix, whose first
// component is not a shared tree of the box and not a dotfile - dataDirAllowed without Paths.
func (p Policy) addonDataDirShape(rel string) bool {
	for _, d := range p.AddonDataDirs {
		if !strings.HasSuffix(d, "/") || !strings.HasPrefix(rel, d) {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(rel, d), "/")
		if first == "" || strings.HasPrefix(first, ".") || slices.Contains(UserfsSharedDirs, first) {
			return false
		}
		return true
	}
	return false
}

func (p Policy) readAllowed(path string) bool {
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") {
		return false
	}
	for _, r := range p.ReadPaths {
		if rel == r {
			return true
		}
	}
	for _, g := range p.ReadGlobs {
		if ok, _ := filepath.Match(g, rel); ok {
			return true
		}
	}
	return false
}

// shadowAllowed: the exact password file the root-password operation may touch.
func (p Policy) shadowAllowed(path string) bool {
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") {
		return false
	}
	for _, s := range p.ShadowPaths {
		if rel == s {
			return true
		}
	}
	return false
}

// certAllowed: the exact live TLS file, whose markers are on the list as well (the operation
// writes them); a marker alone is not a certificate path.
func (p Policy) certAllowed(path string) bool {
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") || strings.HasSuffix(rel, MarkerSuffix) || strings.HasSuffix(rel, ManagedSuffix) {
		return false
	}
	for _, c := range p.CertPaths {
		if rel == c {
			return true
		}
	}
	return false
}

// accountAllowed: exactly the passwd file and the group file of the policy, in that order, and
// an account of the addon shape with a uid in the addon range.
func (p Policy) accountAllowed(passwd, group, name string, uid int) bool {
	if len(p.AccountFiles) != 2 || !ValidAddonUser(name, uid) || uid < p.AddonUIDBase {
		return false
	}
	for i, path := range []string{passwd, group} {
		rel, ok := p.rel(path)
		if !ok || strings.Contains(rel, "..") || rel != p.AccountFiles[i] {
			return false
		}
	}
	return true
}

// sendfileAllowed answers whether a descriptor for path may be passed back, and with which
// allowlisted directory the Server then has to prove the descriptor really is inside (a symlink
// an addon planted between the two is exactly what this operation must not follow). The
// directory itself is not a file: only something under it can be opened.
func (p Policy) sendfileAllowed(path string) (dir string, ok bool) {
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") {
		return "", false
	}
	for _, d := range p.SendfileDirs {
		d = strings.TrimSuffix(d, "/") + "/"
		if strings.HasPrefix(rel, d) && len(rel) > len(d) {
			return filepath.Join(p.Root, d), true
		}
	}
	return "", false
}

// coproDeviceRe is the one device shape the flasher may be pointed at: a raw-uart node, which is
// what S47InitRFHardware assigns every coprocessor module (the built-in ones and the RFUSB alike).
var coproDeviceRe = regexp.MustCompile(`^/dev/raw-uart[0-9]{0,2}$`)

// coproVersionRe bounds the legacy flasher's fwmap line.
var coproVersionRe = regexp.MustCompile(`^[0-9A-Za-z._-]{0,32}$`)

// coproAllowed checks the three pieces of a flash: a known family, a raw-uart node, an .eq3
// file under one of the firmware directories (no "..", no symlink games past rel), and a version
// that fits a fwmap line. Everything else about the command line is the operation's own.
func (p Policy) coproAllowed(family, devnode, file, version string) bool {
	suffix := ".eq3"
	switch family {
	case CoproHmIP, CoproLegacy:
		if !coproDeviceRe.MatchString(devnode) {
			return false
		}
	case CoproHMCFGUSB:
		// the adapter has no node: it is named by its serial, and the file is an .enc
		if !HMCFGUSBDevice.MatchString(devnode) {
			return false
		}
		suffix = ".enc"
	default:
		return false
	}
	if !coproVersionRe.MatchString(version) {
		return false
	}
	rel, ok := p.rel(file)
	if !ok || strings.Contains(rel, "..") || !strings.HasSuffix(rel, suffix) || filepath.Base(rel) == suffix {
		return false
	}
	for _, d := range p.CoproFirmwareDirs {
		d = strings.TrimSuffix(d, "/") + "/"
		if strings.HasPrefix(rel, d) && len(rel) > len(d) {
			return true
		}
	}
	return false
}

func (p Policy) stagingAllowed(path string) bool {
	rel, ok := p.rel(path)
	if !ok {
		return false
	}
	for _, d := range p.StagingDirs {
		if strings.HasPrefix(rel, strings.TrimSuffix(d, "/")+"/") {
			return true
		}
	}
	return false
}

// ledTriggerRe is the exact command SetLEDsDisabled builds: echo '<trigger>' > '<path>/trigger',
// both halves quoted by shellQuote. A sysfs attribute cannot be written the way WriteFile writes
// a file (temp file plus rename), so this one goes through the shell (B-14).
var ledTriggerRe = regexp.MustCompile(`^echo '[A-Za-z0-9_ -]{0,32}' > '(/[^']*/trigger)'$`)

// ipv6ConfRe is the exact command the IPv6 settings build (openccu-lite task 227): one of three
// IPv6 sysctls of one named interface - never all, default or lo - set to 0, 1 or 2. A procfs file
// cannot be written by rename either.
var ipv6ConfRe = regexp.MustCompile(`^echo '[012]' > '/proc/sys/net/ipv6/conf/([a-z][a-z0-9._-]{0,14})/(disable_ipv6|accept_ra|autoconf)'$`)

// backupListRe is the exact command RestoreCheck builds to answer "does this .sbk carry a ReGa
// database?" - one fixed pipeline with the archive path as its only variable, and the path is
// checked against the allowlists like every other. A prefix test ("does it start with tar -xOf")
// would let anything after the prefix run as root, which is the whole boundary (task 17).
var backupListRe = regexp.MustCompile(`^tar -xOf '((?:[^']|'\\'')*)' usr_local\.tar\.gz 2>/dev/null \| tar -tzf - 2>/dev/null \| grep -c 'etc/config/homematic\.regadom\$'$`)

// Server is the root side.
type Server struct {
	Policy Policy
	Ops    Ops // Local
	Log    func(string, ...any)
	// Debug writes one line per request - the operation, the program or the path, never the
	// arguments, the data or the input, which can carry secrets; nil = none (task 101).
	Debug func(string, ...any)
	// SetLogLevel applies the level opLogLevel carries; nil = the operation is refused.
	SetLogLevel func(level string)
	// rpcShut shuts one socket of another process for rpc-drop; nil = a child of this binary
	// (shutInChild). The tests put a fake here.
	rpcShut func(ctx context.Context, pid, fd int) error

	// logListRefused: a log listing was refused in this run, and said so as a warning (B-122)
	logListRefused atomic.Bool
}

// redactArgs is an argument list for the journal: the value after -k is a security key's
// passphrase (crypttool) and is never written anywhere (openccu-lite task 296).
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 1; i < len(out); i++ {
		if out[i-1] == "-k" {
			out[i] = "<redacted>"
		}
	}
	return out
}

func (s *Server) log(format string, a ...any) {
	if s.Log != nil {
		s.Log(format, a...)
	}
}

func (s *Server) debugf(format string, a ...any) {
	if s.Debug != nil {
		s.Debug(format, a...)
	}
}

func (s *Server) debug(req request) {
	if s.Debug == nil {
		return
	}
	what := req.Name
	if req.Path != "" {
		what = strings.TrimSpace(what + " " + req.Path)
	}
	s.Debug("helper: %s %s", req.Op, what)
}

// Serve accepts connections until ctx ends.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(ctx, conn)
	}
}

// receiveFile is the second step of a request that sends a descriptor (storecopy): the helper
// says it is ready, and the caller sends one byte with the descriptor attached - after the request
// was read, so no buffered read can swallow the descriptor. nil when none came.
func (s *Server) receiveFile(conn net.Conn) *os.File {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	if err := json.NewEncoder(conn).Encode(response{OK: true, Stdout: []byte("send")}); err != nil {
		return nil
	}
	buf := make([]byte, 1)
	oob := make([]byte, syscall.CmsgSpace(4))
	_, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil
	}
	fds := parseRights(oob[:oobn])
	if len(fds) != 1 {
		closeFDs(fds)
		return nil
	}
	return os.NewFile(uintptr(fds[0]), "snapshot")
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	var req request
	if err := json.NewDecoder(bufio.NewReaderSize(conn, 1<<20)).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(response{Error: "bad request: " + err.Error()})
		return
	}
	s.debug(req)
	if req.Op == opStoreCopy {
		_ = json.NewEncoder(conn).Encode(s.storeCopy(req, s.receiveFile(conn)))
		return
	}
	if req.Op == opOpen || req.Op == opShareOpen {
		s.serveOpen(conn, req)
		return
	}
	res := s.do(ctx, req)
	_ = json.NewEncoder(conn).Encode(res)
}

// serveOpen answers the one operation whose result is a descriptor rather than bytes: the JSON
// line and the descriptor go out in a single sendmsg, so the caller sees them together (B-35).
func (s *Server) serveOpen(conn net.Conn, req request) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		_ = json.NewEncoder(conn).Encode(response{Error: opOpen + " needs a unix socket"})
		return
	}
	f, res := s.openForSend(req)
	body, err := json.Marshal(res)
	if err != nil {
		body = []byte(`{"error":"encoding the answer failed"}`)
	}
	body = append(body, '\n')
	var rights []byte
	if f != nil {
		defer f.Close()
		rights = syscall.UnixRights(int(f.Fd()))
	}
	if _, _, err := uc.WriteMsgUnix(body, rights, nil); err != nil {
		s.log("helper: passing a descriptor for %s failed: %v", req.Path, err)
	}
}

// openForSend applies the allowlist, opens the file and proves the descriptor is what the
// allowlist meant. Two things are deliberate here. The open is O_NOFOLLOW (Local.Open), so the
// last component cannot be a symlink into /etc that an addon dropped in the same directory. And
// the descriptor is checked *after* opening, through /proc/self/fd, because a component in the
// middle of the path can be a symlink too and a string test of what was asked for would never
// see it - the kernel's answer for what a descriptor actually points at is the only one that
// cannot be raced or dressed up.
func (s *Server) openForSend(req request) (*os.File, response) {
	if !reflect.DeepEqual(req, request{Op: req.Op, Path: req.Path}) {
		return nil, refuse(req.Op + " takes a path and nothing else")
	}
	dir, ok := s.Policy.sendfileAllowed(req.Path)
	if req.Op == opShareOpen {
		// B-217: a backup file on a network share, not the X-Sendfile directories
		dir, ok = s.Policy.shareOpenAllowed(req.Path)
	}
	if !ok {
		s.log("helper: refused open %s", req.Path)
		return nil, refuse("open " + req.Path)
	}
	open := s.ops().Open
	if req.Op == opShareOpen {
		open = s.ops().ShareOpen
	}
	f, err := open(req.Path)
	if err != nil {
		return nil, response{Error: err.Error(), Errno: ErrnoName(err)}
	}
	if err := fdUnder(f, dir); err != nil {
		f.Close()
		s.log("helper: refused open %s: %v", req.Path, err)
		return nil, refuse("open " + req.Path)
	}
	return f, response{OK: true}
}

// fdUnder reports that the open descriptor names a file inside dir. dir is resolved as well: the
// allowlisted directory may itself sit behind a symlink (a development root, a test's temp dir),
// and that is not the case this is guarding against.
func fdUnder(f *os.File, dir string) error {
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
	if err != nil {
		return fmt.Errorf("the descriptor cannot be resolved: %w", err)
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if !strings.HasPrefix(filepath.Clean(real), filepath.Clean(base)+"/") {
		return fmt.Errorf("the descriptor is not under %s", base)
	}
	return nil
}

func refuse(why string) response { return response{Error: "refused: " + why} }

// resolved is the boundary's path check for a file operation (B-235): the path as the daemon
// gave it passes allowed, it resolves through trusted links only (Policy.resolve), and the
// resolved path passes allowed as well. The operation then runs on the resolved path. A refusal
// is logged once, here.
func (s *Server) resolved(path string, final bool, allowed func(string) bool) (string, error) {
	if !allowed(path) {
		return "", errors.New("not on the list")
	}
	res, err := s.Policy.resolve(path, final)
	if err != nil {
		s.log("helper: refused %s: %v", path, err)
		return "", err
	}
	if res != filepath.Clean(path) && !allowed(res) {
		s.log("helper: refused %s: it resolves to %s, which is not on the list", path, res)
		return "", fmt.Errorf("resolves to %s, which is not on the list", res)
	}
	return res, nil
}

// ops is what the Server performs with: Local unless a test substitutes something.
func (s *Server) ops() Ops {
	if s.Ops == nil {
		return Local{noFollow: true} // B-235: the paths were resolved here, nothing is followed below
	}
	return s.Ops
}

func (s *Server) do(ctx context.Context, req request) response {
	if res, ok := s.doNamedRootObeyed(req); ok {
		return res
	}
	if res, ok := s.doAddonHome(req); ok {
		return res
	}
	ops := s.ops()
	fail := func(err error) response {
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true}
	}
	switch req.Op {
	case "run":
		if !s.Policy.programAllowed(req.Name, req.Args) {
			s.log("helper: refused run %s %v", req.Name, redactArgs(req.Args))
			return refuse("program " + req.Name)
		}
		for _, a := range req.Args {
			if strings.ContainsAny(a, "\x00") {
				return refuse("argument")
			}
		}
		r, err := ops.Run(ctx, req.Name, req.Args, req.Stdin)
		if err != nil {
			return response{Error: err.Error(), Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
		}
		return response{OK: true, Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
	case "runas":
		if !s.Policy.cgiAllowed(req.Name, req.Args, req.UID) && !s.Policy.addonScriptAllowed(req.Name, req.Args, req.UID) {
			s.log("helper: refused runas %s %v uid=%d", req.Name, req.Args, req.UID)
			return refuse("cgi " + req.Name)
		}
		r, err := ops.RunAs(ctx, Credential{UID: req.UID, GID: req.GID, Groups: req.Groups}, req.Name, req.Args, req.Env, req.Dir, req.Stdin)
		if err != nil {
			return response{Error: err.Error(), Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
		}
		return response{OK: true, Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
	case "write":
		// systemd's runtime unit directory admits its two exact shapes for write and remove, and
		// no other operation (RunUnitDir). B-235: the path as given and as it resolves through
		// the image's links both have to pass, and the write follows nothing below
		writable := func(path string) bool { return s.Policy.pathAllowed(path) || s.Policy.runUnitFileAllowed(path) }
		path, err := s.resolved(req.Path, true, writable)
		if err != nil {
			return refuse("path " + req.Path + ": " + err.Error())
		}
		return fail(ops.WriteFile(path, req.Data, os.FileMode(req.Mode)))
	case "touch":
		path, err := s.resolved(req.Path, true, s.Policy.pathAllowed)
		if err != nil {
			return refuse("path " + req.Path + ": " + err.Error())
		}
		return fail(ops.Touch(path, os.FileMode(req.Mode)))
	case "remove", "removeall":
		// B-238: not a directory that holds a named operation's file; since B-293 not the
		// certificate either (RemoveCertificate) - no named file has a generic exception
		allowed := func(path string) bool {
			return s.Policy.wholeAllowed(path) || (req.Op == "remove" && s.Policy.runUnitFileAllowed(path))
		}
		path, err := s.resolved(req.Path, false, allowed)
		if err != nil {
			return refuse("path " + req.Path + ": " + err.Error())
		}
		if req.Op == "remove" {
			return fail(ops.Remove(path))
		}
		return fail(ops.RemoveAll(path))
	case "mkdir":
		allowed := func(path string) bool { return s.Policy.dataDirAllowed(path) || s.Policy.runUnitDirAllowed(path) }
		path, err := s.resolved(req.Path, true, allowed)
		if err != nil {
			return refuse("path " + req.Path + ": " + err.Error())
		}
		return fail(ops.MkdirAll(path, os.FileMode(req.Mode)))
	case "symlink":
		// B-235: the target too - a link from an allowed path to /etc/passwd made the next
		// write there root's
		// B-293: nor a link in place of a directory that holds a named file (a missing rc.d or
		// policy directory made a link to one the daemon fills)
		link, err := s.resolved(req.Path, false, s.Policy.wholeAllowed)
		if err != nil {
			return refuse("path " + req.Path + ": " + err.Error())
		}
		if !s.Policy.symlinkTargetAllowed(link, req.Target) {
			s.log("helper: refused the link %s -> %s", req.Path, req.Target)
			return refuse("symlink target " + req.Target)
		}
		return fail(ops.Symlink(req.Target, link))
	case "rename":
		// B-238: neither side a directory that holds a named operation's file (moved away, the
		// file written there, moved back)
		src, err := s.resolved(req.Src, false, func(p string) bool { return s.Policy.wholeAllowed(p) || s.Policy.stagingAllowed(p) })
		if err != nil {
			return refuse("rename " + req.Src + ": " + err.Error())
		}
		dst, err := s.resolved(req.Dst, false, s.Policy.wholeAllowed)
		if err != nil {
			return refuse("rename " + req.Dst + ": " + err.Error())
		}
		if isSymlink(src) {
			s.log("helper: refused to move the link %s to %s", req.Src, req.Dst)
			return refuse("rename " + req.Src + ": a symlink")
		}
		return fail(ops.Rename(src, dst))
	case "chown":
		// the entry itself, never through a link at the end (Lchown; the recursive walk follows
		// none either)
		// B-238: not a directory that holds a named operation's file (its owner could replace it)
		allowed := func(path string) bool {
			rel, ok := s.Policy.rel(path)
			return s.Policy.dataDirAllowed(path) && ok && !s.Policy.holdsNamed(rel)
		}
		path, err := s.resolved(req.Path, false, allowed)
		if err != nil {
			return refuse("path " + req.Path + ": " + err.Error())
		}
		return fail(ops.Chown(path, req.UID, req.GID, req.Recursive))
	case opOwnTree:
		if err := s.Policy.OwnTreeAllowed(req.Name, req.Args, req.UID); err != nil {
			s.log("helper: refused the ownership walk for %s: %v", req.Name, err)
			return refuse("owntree: " + err.Error())
		}
		r, err := ops.OwnTree(req.Name, req.Args, req.UID, OwnTreeOptions{DryRun: req.DryRun, Quick: req.Quick})
		if err != nil {
			return response{Error: err.Error()}
		}
		b, err := json.Marshal(r)
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true, Stdout: b}
	case "chmod":
		// B-238: a directory that holds a named operation's file may lose bits, never gain one
		path, err := s.resolved(req.Path, true, func(p string) bool { return s.Policy.chmodAllowed(p, os.FileMode(req.Mode)) })
		if err != nil {
			return refuse("path " + req.Path + ": " + err.Error())
		}
		return fail(ops.Chmod(path, os.FileMode(req.Mode)))
	case opRootPassword:
		if !s.Policy.shadowAllowed(req.Path) {
			s.log("helper: refused the root password write to %s", req.Path)
			return refuse("shadow " + req.Path)
		}
		// the hash is checked here too, not only in the operation: this is the boundary, and
		// what arrives is a string the unprivileged side produced
		if !validPasswordHash(req.Hash) {
			s.log("helper: refused a root password that is not a crypt hash")
			return refuse("password hash")
		}
		return fail(ops.SetRootPasswordHash(req.Path, req.Hash))
	case opWriteCert:
		if !s.Policy.certAllowed(req.Path) {
			s.log("helper: refused the certificate write to %s", req.Path)
			return refuse("certificate path " + req.Path)
		}
		// checked here as well as in the operation: this is the boundary, and what arrives is
		// bytes the unprivileged side assembled
		if err := certpem.ValidLivePEM(req.Data); err != nil {
			s.log("helper: refused a certificate file that is not a chain plus its key: %v", err)
			return refuse("certificate: " + err.Error())
		}
		if !ValidCertMode(req.CertMode) {
			s.log("helper: refused the certificate mode %q", req.CertMode)
			return refuse("certificate mode " + req.CertMode)
		}
		return fail(ops.WriteCertificate(req.Path, req.Data, req.CertMode))
	case opReadCert:
		if !s.Policy.certAllowed(req.Path) {
			return refuse("certificate path " + req.Path)
		}
		b, marker, err := ops.ReadCertificate(req.Path)
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true, Stdout: b, Marker: marker}
	case opAddonUser:
		if !s.Policy.accountAllowed(req.Path, req.GroupFile, req.Name, req.UID) {
			s.log("helper: refused the addon account %s (%d) in %s, %s", req.Name, req.UID, req.Path, req.GroupFile)
			return refuse("addon account " + req.Name)
		}
		return fail(ops.AddAddonUser(req.Path, req.GroupFile, req.Name, req.UID))
	case opFlashCopro:
		if !s.Policy.coproAllowed(req.Name, req.Path, req.Src, req.Version) {
			s.log("helper: refused the coprocessor flash %s %s %s %q", req.Name, req.Path, req.Src, req.Version)
			return refuse("coprocessor flash " + req.Src + " on " + req.Path)
		}
		r, err := ops.FlashCoprocessor(ctx, req.Name, req.Path, req.Src, req.Version)
		if err != nil {
			return response{Error: err.Error(), Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
		}
		return response{OK: true, Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
	case opSmartctl:
		// the device and nothing else: a request that carries arguments, a program, input, an
		// environment or a second path is refused rather than ignored, so an attempt to slip an
		// option in is logged as what it is
		if req.Name != "" || len(req.Args) > 0 || len(req.Stdin) > 0 || len(req.Env) > 0 || req.Dir != "" || req.Src != "" || req.Dst != "" || req.Target != "" || len(req.Data) > 0 {
			s.log("helper: refused a SMART read with more than a device: %s %v", req.Name, req.Args)
			return refuse("smartctl takes a device and nothing else")
		}
		if !SmartDeviceRe.MatchString(req.Path) {
			s.log("helper: refused a SMART read of %q", req.Path)
			return refuse("smartctl device " + req.Path)
		}
		r, err := ops.Smartctl(ctx, req.Path)
		if errors.Is(err, ErrNotAvailable) {
			// no smartctl on this image (task 111): the client's ErrNotAvailable, with the reason once
			return response{Error: "not-available: " + strings.TrimPrefix(err.Error(), ErrNotAvailable.Error()+": ")}
		}
		if err != nil {
			return response{Error: err.Error(), Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
		}
		return response{OK: true, Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
	case opFirewallInput:
		// the family and nothing else - every other field empty, those added later included -
		// refused rather than ignored (as the SMART read)
		if !reflect.DeepEqual(req, request{Op: req.Op, Name: req.Name}) {
			s.log("helper: refused a firewall read with more than a family: %s %v", req.Path, req.Args)
			return refuse("firewall-input takes a family and nothing else")
		}
		if _, ok := FirewallFamilies[req.Name]; !ok {
			s.log("helper: refused a firewall read of family %q", req.Name)
			return refuse("firewall family " + req.Name)
		}
		r, err := ops.FirewallInput(ctx, req.Name)
		if errors.Is(err, ErrNotAvailable) {
			return response{Error: "not-available: " + strings.TrimPrefix(err.Error(), ErrNotAvailable.Error()+": ")}
		}
		if err != nil {
			return response{Error: err.Error(), Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
		}
		return response{OK: true, Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit}
	case opFirewallLoad:
		if !reflect.DeepEqual(req, request{Op: req.Op, FWv4: req.FWv4, FWv6: req.FWv6, WindowMS: req.WindowMS}) {
			s.log("helper: refused a firewall load with more than its texts and window")
			return refuse("firewall-load takes two texts and a window")
		}
		for _, t := range [][]byte{req.FWv4, req.FWv6} {
			if err := ValidFirewallText(t); err != nil {
				s.log("helper: refused a firewall load: %v", err)
				return refuse("firewall-load: " + err.Error())
			}
		}
		fwState.mu.Lock()
		fwState.logf = s.log
		fwState.mu.Unlock()
		err := ops.FirewallLoad(ctx, req.FWv4, req.FWv6, time.Duration(req.WindowMS)*time.Millisecond)
		if errors.Is(err, ErrNotAvailable) {
			return response{Error: "not-available: " + strings.TrimPrefix(err.Error(), ErrNotAvailable.Error()+": ")}
		}
		if err == nil {
			s.log("helper: firewall loaded (confirm window %d ms)", req.WindowMS)
		}
		return fail(err)
	case opFirewallConfirm, opFirewallRevert:
		if !reflect.DeepEqual(req, request{Op: req.Op}) {
			return refuse(req.Op + " takes nothing")
		}
		if req.Op == opFirewallConfirm {
			return fail(ops.FirewallConfirm(ctx))
		}
		s.log("helper: firewall reverted on request")
		return fail(ops.FirewallRevert(ctx))
	case opFirewallCounters:
		if !reflect.DeepEqual(req, request{Op: req.Op}) {
			return refuse("firewall-counters takes nothing")
		}
		tables, err := ops.FirewallCounters(ctx)
		if err != nil {
			return response{Error: err.Error()}
		}
		b, _ := json.Marshal(tables)
		return response{OK: true, Stdout: b}
	case opSocketOwners:
		if !reflect.DeepEqual(req, request{Op: req.Op}) {
			return refuse("socket-owners takes nothing")
		}
		owners, err := ops.SocketOwners(ctx)
		if err != nil {
			return response{Error: err.Error()}
		}
		b, _ := json.Marshal(owners)
		return response{OK: true, Stdout: b}
	case opLED:
		if err := s.Policy.ledAllowed(req.Path, req.LEDs); err != nil {
			s.log("helper: refused an LED frame for %s: %v", req.Path, err)
			return refuse("led: " + err.Error())
		}
		if err := s.Policy.ledUnderSys(req.Path, req.LEDs); err != nil {
			s.log("helper: refused an LED frame: %v", err)
			return refuse("led: " + err.Error())
		}
		return fail(ops.WriteLEDs(req.Path, req.LEDs))
	case opLEDModule:
		// one of LEDModules and nothing else
		if !reflect.DeepEqual(req, request{Op: req.Op, Name: req.Name}) || !slices.Contains(LEDModules, req.Name) {
			s.log("helper: refused an LED module request %q", req.Name)
			return refuse("led-module: " + req.Name)
		}
		return fail(ops.LoadLEDModule(ctx, req.Name))
	case opLogLevel:
		// a level's name and nothing else
		if req.Path != "" || len(req.Args) > 0 || len(req.Data) > 0 || len(req.Stdin) > 0 || len(req.Env) > 0 || req.Src != "" || req.Dst != "" {
			return refuse("loglevel takes a level and nothing else")
		}
		known := false
		for _, n := range LogLevelNames {
			known = known || n == req.Name
		}
		if !known || s.SetLogLevel == nil {
			return refuse("log level " + req.Name)
		}
		s.SetLogLevel(req.Name)
		return response{OK: true}
	case opListLogs:
		return s.listLogs(req)
	case opListDir:
		return s.listDir(req)
	case opProcExe:
		return s.procExe(req)
	case opRCTargetRemove:
		return s.rcTarget(req)
	case opRemnantRemove:
		return s.remnant(req)
	case opAddonFragment:
		return s.addonFragment(req)
	case opAddonImage:
		return s.addonImage(req)
	case opNetMount, opNetUnmount, opNetMountRemove, opWriteTest:
		return s.netMount(ctx, req)
	case opShareList:
		return s.shareList(ctx, req)
	case opUSBFormat, opUSBEject:
		return s.usbDisk(ctx, req)
	case opAuthKeysRead, opAuthKeysWrite, opSSHEnd, opSSHKeyOnly:
		return s.ssh(req)
	case opRPCDrop:
		return s.rpcDrop(ctx, req)
	case "read":
		if !s.Policy.readAllowed(req.Path) {
			return refuse("read " + req.Path)
		}
		b, err := ops.ReadFile(req.Path)
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true, Stdout: b}
	}
	return refuse("op " + req.Op)
}

// Listen creates the socket owned by root:group with mode 0660 - the occulite user reaches it
// through the group.
func Listen(socket string, gid int) (net.Listener, error) {
	_ = os.MkdirAll(filepath.Dir(socket), 0o755)
	_ = os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(socket, 0, gid); err != nil {
		l.Close()
		return nil, err
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
