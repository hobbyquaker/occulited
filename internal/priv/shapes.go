package priv

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// A program on the list is a program with a shape (openccu-lite B-234).
//
// Programs used to admit a name and pass every argument through: `systemd-run /bin/sh -c
// <anything>`, `systemctl link <a unit an addon wrote>`, `kill <hmipserver's pid>`, `crypttool
// -S -k <key>` all ran as root once the daemon asked, and the daemon is exactly the process the
// helper exists to distrust (task 17). Now every name on Programs has an argShape here: the exact
// forms the daemon builds - `grep 'run(ctx, "' internal/system` is the inventory - with the
// variable parts (an interface, an address, a unit name, a path under an allowed prefix) each
// checked against its own rule. A form the daemon does not build is refused and logged, and
// TestProgramsHaveShapes fails when a name is on the list without a shape. The scripts under
// ProgramDirs take one action word (dirScriptShape). `sh` keeps its three exact command lines.

// argShape says whether args is one of the forms the daemon builds for a program.
type argShape func(p Policy, args []string) bool

// programShapes: one shape per entry of DefaultPolicy's Programs, keyed exactly as listed there.
// Filled at init: systemdRunShape checks the program inside the scope through programAllowed,
// which would be an initialization cycle for a literal.
var programShapes map[string]argShape

func init() { programShapes = defaultProgramShapes() }

func defaultProgramShapes() map[string]argShape {
	return map[string]argShape{
		"systemctl":          systemctlShape,
		"/usr/bin/systemctl": systemctlShape,
		"systemd-run":        systemdRunShape,
		"kill":               killShape,
		"sh":                 shellShape,
		// the running hostname (netwrite.go ApplyHostname)
		"hostname": templates("<host>"),
		// the clock set by hand (sysconf.go SetClock) and pushed to the RTC and rfd (pushClock)
		"date":                   templates("-u -s <datetime>"),
		"/sbin/hwclock":          templates("-wu"),
		"/bin/SetInterfaceClock": templates("127.0.0.1:<port>"),
		// the network (netwrite.go, ipv6conf.go, dnsoverride.go): the address, the routes, the
		// multicast route, the DHCP clients and openresolv's records
		"/sbin/ip": templates(
			"-4 -o addr show dev <iface>",
			"-4 addr flush dev <iface>",
			"route del default",
			"route add default via <ip4>",
			"route add 224.0.0.0/24 dev <iface> scope link",
			"-6 route del default via <ip6> dev <iface>",
			"-6 addr del <addr6> dev <iface>",
			"-6 addr replace <cidr6> dev <iface>",
			"-6 route replace default via <ip6> dev <iface>",
		),
		"/sbin/ifconfig":   templates("<iface> <ip4> netmask <ip4>"),
		"/sbin/resolvconf": templates("-a <record>", "-x -a <record>", "-d <record>", "-f -d <record>"),
		"/sbin/udhcpc":     udhcpcShape,
		"/sbin/udhcpc6":    udhcpc6Shape,
		// the power menu: busybox's applets take nothing
		"/sbin/reboot":   noArgs,
		"/bin/reboot":    noArgs,
		"/sbin/poweroff": noArgs,
		"/bin/poweroff":  noArgs,
		// the BidCos security key (lgw.go SetSecurityKey, keystate.go, system.go, radioimport.go):
		// the test for a user key, the comparison, the listing, and setting one under an index
		"/bin/crypttool": templates("-v -t 0", "-v -t 3 -k <key>", "-g", "-S -k <key> -i <index>", "-S -i <index> -k <key>"),
		// the firmware's scripts: install_addon reads /usr/local/tmp/new_addon.tar.gz itself and
		// takes nothing; the backup scripts take one archive under the backup directory or the
		// staging directory (the check and the restore are told apart by -c)
		"/bin/install_addon":    noArgs,
		"/bin/createBackup.sh":  templates("<backupfile>"),
		"/bin/restoreBackup.sh": templates("-c <backupfile>", "-c -f <backupfile>", "<backupfile>", "-f <backupfile>"),
		"/bin/cronBackup.sh":    noArgs,
		"/bin/updateTZ.sh":      noArgs,
		// the radio module's version (radiofw.go): the legacy flasher's read-only query, and
		// upstream's detection script, both on one raw-uart node
		"/bin/eq3configcmd":        templates("update-coprocessor -p <copronode> -t HM-MOD-UART -c -v"),
		"/bin/detect_radio_module": templates("<copronode>"),
		// the device import's outcome (radioimportstate.go): hmipserver's lines about the adapter
		// exchange since the import (openccu-lite B-281)
		"journalctl": importJournalShape,
		// the fork's scripts: the addon-rc adopter (28.8), the writable device descriptions' reset
		// (D-66) and the CA bundle's rebuild (task 231)
		"/usr/libexec/occu/lite-addon-rc":        adoptShape,
		"/usr/libexec/occu/lite-extension-dirs":  templates("reset /firmware/rftypes"),
		"/usr/libexec/occu/lite-ca-certificates": noArgs,
	}
}

// importJournalShape is the one journalctl form the daemon runs as root: hmipserver's unit, the
// message alone, quiet, the fixed pattern "Adapter exchange", since a date (openccu-lite B-281).
// Everything else journald has to say the daemon reads as its own user.
func importJournalShape(p Policy, args []string) bool {
	want := []string{"-u", "hmipserver.service", "-o", "cat", "-q", "--no-pager", "-g", "Adapter exchange", "--since", "<datetime>"}
	if len(args) != len(want) {
		return false
	}
	for i, tok := range want {
		if !p.placeholder(tok, args[i]) {
			return false
		}
	}
	return true
}

// dirScriptActions are the one word an init script or an rc.d script may be run with as root:
// the init scripts' verbs (S50lighttpd reload, S50sshd start, S46chronyd restart,
// S47InitRFHardware start) and the rc.d scripts' info and uninstall, which cp_software.cgi and
// the uninstall run as root when the addon has no user of its own.
var dirScriptActions = map[string]bool{
	"start": true, "stop": true, "restart": true, "reload": true, "force-reload": true, "status": true,
	"info": true, "info.de": true, "info.en": true, "init": true, "uninstall": true,
}

// dirScriptShape: a script under ProgramDirs takes exactly one action word.
func dirScriptShape(_ Policy, args []string) bool {
	return len(args) == 1 && dirScriptActions[args[0]]
}

func noArgs(_ Policy, args []string) bool { return len(args) == 0 }

// programKey names the Programs entry (or "dir:<ProgramDir>") that admits name, if any.
func (p Policy) programKey(name string) (string, bool) {
	for _, a := range p.Programs {
		if a == name {
			return a, true
		}
	}
	rel, ok := p.rel(name)
	if !ok {
		return "", false
	}
	for _, a := range p.Programs {
		if a == rel {
			return a, true
		}
	}
	for _, d := range p.ProgramDirs {
		if filepath.Dir(rel) == d {
			return "dir:" + d, true
		}
	}
	return "", false
}

func (p Policy) programAllowed(name string, args []string) bool {
	key, ok := p.programKey(name)
	if !ok {
		return false
	}
	shape := programShapes[key]
	if strings.HasPrefix(key, "dir:") {
		shape = dirScriptShape
	}
	if shape == nil {
		// on the list without a shape: refused, and TestProgramsHaveShapes says so before a
		// release does
		return false
	}
	return shape(p, args)
}

// The value rules of the templates' placeholders. Each is what the daemon's own validation
// admits, written down again here because this is the boundary and that was the caller.
var (
	shapeHostRe     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	shapeVendorRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	shapeIfaceRe    = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,14}$`)
	shapeDatetimeRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}$`)
	shapeKeyRe      = regexp.MustCompile(`^[0-9A-Za-z_]{5,128}$`)
	shapeIndexRe    = regexp.MustCompile(`^[0-9]{1,3}$`)
	shapePortRe     = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)
	shapeRecordRe   = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)
	shapeAddonIDRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}$`)
	shapePIDRe      = regexp.MustCompile(`^[1-9][0-9]{0,7}$`)
)

func shapeIface(s string) bool {
	return shapeIfaceRe.MatchString(s) && !strings.Contains(s, "..") && s != "all" && s != "default" && s != "lo"
}

func shapeIP4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && strings.Count(s, ".") == 3
}

func shapeIP6(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && strings.Contains(s, ":")
}

func shapeCIDR6(s string) bool {
	addr, bits, ok := strings.Cut(s, "/")
	if !ok || !shapeIP6(addr) {
		return false
	}
	n, err := strconv.Atoi(bits)
	return err == nil && n >= 0 && n <= 128 && strconv.Itoa(n) == bits
}

// placeholder checks one template token against one argument.
func (p Policy) placeholder(tok, arg string) bool {
	switch tok {
	case "<host>":
		return shapeHostRe.MatchString(arg)
	case "<iface>":
		return shapeIface(arg)
	case "<ip4>":
		return shapeIP4(arg)
	case "<ip6>":
		return shapeIP6(arg)
	case "<cidr6>":
		return shapeCIDR6(arg)
	case "<addr6>":
		return shapeIP6(arg) || shapeCIDR6(arg)
	case "<datetime>":
		return shapeDatetimeRe.MatchString(arg)
	case "<key>":
		return shapeKeyRe.MatchString(arg)
	case "<index>":
		return shapeIndexRe.MatchString(arg)
	case "127.0.0.1:<port>": // SetInterfaceClock's one argument, rfd's loopback address
		port, ok := strings.CutPrefix(arg, "127.0.0.1:")
		return ok && shapePortRe.MatchString(port)
	case "<record>":
		return shapeRecordRe.MatchString(arg)
	case "<copronode>":
		return coproDeviceRe.MatchString(arg)
	case "<backupfile>":
		return p.backupFileAllowed(arg)
	}
	return tok == arg
}

// backupDir is where the firmware's backup scripts read and write their archives (system.BackupDir),
// and where the daemon puts an uploaded one for the restore.
const backupDir = "/usr/local/tmp/"

// backupFileAllowed: an archive under the backup directory or the daemon's staging directory -
// not any writable path (/etc/config/ is one), since the restore script unpacks what it is given
// over the userfs at the next boot.
func (p Policy) backupFileAllowed(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	rel, ok := p.rel(path)
	if !ok || strings.Contains(rel, "..") {
		return false
	}
	return (strings.HasPrefix(rel, backupDir) && len(rel) > len(backupDir)) || p.stagingAllowed(path)
}

// templates builds a shape from space-separated command lines whose <tokens> are placeholders.
func templates(lines ...string) argShape {
	var tpls [][]string
	for _, l := range lines {
		tpls = append(tpls, strings.Fields(l))
	}
	return func(p Policy, args []string) bool {
		for _, tpl := range tpls {
			if len(tpl) != len(args) {
				continue
			}
			ok := true
			for i, tok := range tpl {
				if !p.placeholder(tok, args[i]) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
}

// shellShape is what programAllowed did for sh before B-234: three exact command lines (B-14,
// B-39, task 227), each with its one variable checked.
func shellShape(p Policy, args []string) bool {
	if len(args) != 2 || args[0] != "-c" {
		return false
	}
	if m := backupListRe.FindStringSubmatch(args[1]); m != nil { // listing a backup archive
		path := strings.ReplaceAll(m[1], `'\''`, "'")
		return p.pathAllowed(path) || p.stagingAllowed(path)
	}
	if m := ledTriggerRe.FindStringSubmatch(args[1]); m != nil { // an LED trigger
		return p.pathAllowed(m[1])
	}
	if m := ipv6ConfRe.FindStringSubmatch(args[1]); m != nil { // an interface's IPv6 sysctl
		return shapeIface(m[1])
	}
	return false
}

// udhcpcShape is the one IPv4 client line eQ3StartNetwork builds and the daemon copies
// (netwrite.go): the same hostname in -x and -F, the pid file named after the interface.
func udhcpcShape(_ Policy, args []string) bool {
	tpl := strings.Fields("-b -t 20 -T 3 -S -x hostname:<host> -i <iface> -F <host> -V <vendor> -s /bin/dhcp.script -p <pidfile>")
	if len(args) != len(tpl) {
		return false
	}
	host, iface := strings.TrimPrefix(args[7], "hostname:"), args[9]
	for i, tok := range tpl {
		switch tok {
		case "hostname:<host>":
			if args[i] != "hostname:"+host || !shapeHostRe.MatchString(host) {
				return false
			}
		case "<host>":
			if args[i] != host {
				return false
			}
		case "<iface>":
			if !shapeIface(iface) {
				return false
			}
		case "<vendor>":
			// the vendor class of /etc/dhcp-vendor-class (openccu-lite task 327): one word
			if !shapeVendorRe.MatchString(args[i]) {
				return false
			}
		case "<pidfile>":
			if args[i] != "/var/run/udhcpc_"+iface+".pid" {
				return false
			}
		default:
			if args[i] != tok {
				return false
			}
		}
	}
	return true
}

// udhcpc6Args are the IPv6 client's arguments after its first flag (ipv6conf.go Apply): -f in
// the transient unit, -b straight on a busybox box.
func udhcpc6Args(args []string) (iface string, ok bool) {
	tpl := strings.Fields("-S -t 5 -T 3 -O dns -O search -i <iface> -s /usr/libexec/occu/lite-dhcp6 -p <pidfile>")
	if len(args) != len(tpl) {
		return "", false
	}
	iface = args[10]
	for i, tok := range tpl {
		switch tok {
		case "<iface>":
			if !shapeIface(iface) {
				return "", false
			}
		case "<pidfile>":
			if args[i] != "/var/run/udhcpc6_"+iface+".pid" {
				return "", false
			}
		default:
			if args[i] != tok {
				return "", false
			}
		}
	}
	return iface, true
}

func udhcpc6Shape(_ Policy, args []string) bool {
	if len(args) == 0 || (args[0] != "-f" && args[0] != "-b") {
		return false
	}
	_, ok := udhcpc6Args(args[1:])
	return ok
}

// addonScopeRe is the install scope's name (systemd_addons.go scopeName).
var addonScopeRe = regexp.MustCompile(`^occulite-addon-[0-9a-f]{8}\.scope$`)

// systemdRunShape: the two transient units the daemon starts. The install scope runs the
// firmware's installer or an rc.d script - programs with shapes of their own, checked again
// here, and never the shell, systemctl or systemd-run itself. The DHCPv6 client runs in a
// service named after its interface with the client line udhcpc6Shape admits. No other program
// gets a unit: `systemd-run /bin/sh -c …` was the finding.
func systemdRunShape(p Policy, args []string) bool {
	switch {
	case len(args) >= 5 && args[0] == "--scope" && args[1] == "--quiet" && strings.HasPrefix(args[2], "--unit=") && args[3] == "--":
		if !addonScopeRe.MatchString(strings.TrimPrefix(args[2], "--unit=")) {
			return false
		}
		key, ok := p.programKey(args[4])
		if !ok || !(key == "/bin/install_addon" || strings.HasPrefix(key, "dir:")) {
			return false
		}
		return p.programAllowed(args[4], args[5:])
	case len(args) >= 4 && strings.HasPrefix(args[0], "--unit=occu-dhcp6-") && args[1] == "--collect" && args[2] == "--quiet" && args[3] == "/sbin/udhcpc6":
		rest := args[4:]
		if len(rest) == 0 || rest[0] != "-f" {
			return false
		}
		iface, ok := udhcpc6Args(rest[1:])
		return ok && args[0] == "--unit=occu-dhcp6-"+iface+".service"
	}
	return false
}

// systemctlOptions are the switches the daemon's systemctl lines carry; anything else that
// starts with a dash (--root, --user, -H, -M, --force, --global …) is refused.
var systemctlOptions = map[string]bool{
	"--no-pager": true, "--plain": true, "--all": true, "--quiet": true, "-q": true, "--no-block": true,
	"--runtime": true, "--now": true, "--output=json": true, "--type=service": true, "--type=timer": true,
	"--timestamp=unix": true,
}

// systemctlVerbs: the verb, the units it takes (min, max; -1 = any) and whether it may only
// act on the runtime configuration (`--runtime`, which /run keeps and a boot forgets - the
// Services page's switch, B-26, and the own timers, task 50). Missing on purpose: link, edit,
// enable of a path, set-environment, set-property, kill, isolate, switch-root, daemon-reexec,
// and every other verb the daemon never uses.
var systemctlVerbs = map[string]struct {
	minUnits, maxUnits int
	runtimeOnly        bool
}{
	"daemon-reload": {0, 0, false},
	"poweroff":      {0, 0, false},
	"list-units":    {0, 0, false},
	"list-timers":   {0, 0, false},
	"show":          {0, -1, false},
	"cat":           {1, -1, false},
	"status":        {1, -1, false},
	"is-active":     {1, -1, false},
	"is-enabled":    {1, -1, false},
	"is-failed":     {1, -1, false},
	"start":         {1, -1, false},
	"stop":          {1, -1, false},
	"restart":       {1, -1, false},
	"reload":        {1, -1, false},
	"reset-failed":  {1, -1, false}, // openccu-lite B-236: the uninstall's leftover
	"mask":          {1, 1, true},
	"unmask":        {1, 1, true},
	"enable":        {1, 1, true},
	"disable":       {1, 1, true},
}

// systemctlUnitRe is a unit name as systemd spells it: no "/", so never a path to a unit file
// (`systemctl enable /usr/local/addons/x/evil.service` links it), and not starting with a dash.
var systemctlUnitRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@\\-]{0,255}$`)

// systemctlPropsRe is -p's value: property names, comma separated.
var systemctlPropsRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(,[A-Za-z][A-Za-z0-9_]*)*$`)

// systemctlShape parses one systemctl line: known switches, one verb from the table, and unit
// names - "*" only for show (the boot chart reads every unit at once).
func systemctlShape(_ Policy, args []string) bool {
	var verb string
	var units []string
	runtime := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			units = append(units, args[i+1:]...)
			i = len(args)
		case a == "-p" || a == "--property":
			i++
			if i >= len(args) || !systemctlPropsRe.MatchString(args[i]) {
				return false
			}
		case strings.HasPrefix(a, "--property="):
			if !systemctlPropsRe.MatchString(strings.TrimPrefix(a, "--property=")) {
				return false
			}
		case strings.HasPrefix(a, "-"):
			if !systemctlOptions[a] {
				return false
			}
			runtime = runtime || a == "--runtime"
		case verb == "":
			verb = a
		default:
			units = append(units, a)
		}
	}
	v, ok := systemctlVerbs[verb]
	if !ok || len(units) < v.minUnits || (v.maxUnits >= 0 && len(units) > v.maxUnits) || (v.runtimeOnly && !runtime) {
		return false
	}
	for _, u := range units {
		if u == "*" && verb == "show" {
			continue
		}
		if !systemctlUnitRe.MatchString(u) {
			return false
		}
	}
	return true
}

// killShape: TERM (the default), or -TERM or -KILL, to one or more pids the helper looks at itself
// (killable) - the daemon's own idea of a pid is not what decides.
func killShape(p Policy, args []string) bool {
	if len(args) > 0 && (args[0] == "-TERM" || args[0] == "-KILL") {
		args = args[1:]
	}
	if len(args) == 0 {
		return false
	}
	for _, a := range args {
		if !shapePIDRe.MatchString(a) {
			return false
		}
		pid, _ := strconv.Atoi(a)
		if !p.killable(pid) {
			return false
		}
	}
	return true
}

// killable is what the daemon may signal as root (netwrite.go, ipv6conf.go, systemd.go): the
// DHCP clients it started - found by their pid files, so the pid is what a file under /var/run
// says, and /var/run is a write prefix - and an addon's processes outside their unit (task 48,
// B-106), which are the processes whose command line names the addons' tree. Never PID 1, never
// the helper, and never a process inside one of the system's own units: rfd, hmipserver,
// lighttpd, sshd and the rest live in system.slice under their unit's name, and only an addon's
// unit or the install scope is an addon's place there.
func (p Policy) killable(pid int) bool {
	if pid <= 1 || pid == os.Getpid() {
		return false
	}
	procDir := p.ProcDir
	if procDir == "" {
		procDir = filepath.Join(p.Root, "proc")
	}
	dir := filepath.Join(procDir, strconv.Itoa(pid))
	comm, err := os.ReadFile(filepath.Join(dir, "comm"))
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(comm)) {
	case "udhcpc", "udhcpc6":
		return true
	}
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return false
	}
	// the daemon's own signal (procs.go addonProcesses): a file under /usr/local/addons/<id>/, or
	// - occulited B-30 - a process of an addon user, whose command line may be a title alone
	// (node-red); an addon user's process is not root's and not the system's
	if !strings.Contains(string(cmdline), "/usr/local/addons/") && !p.addonUserProc(dir) && !addonExe(dir) {
		return false
	}
	cgroup, _ := os.ReadFile(filepath.Join(dir, "cgroup"))
	for _, line := range strings.Split(strings.TrimSpace(string(cgroup)), "\n") {
		_, path, ok := strings.Cut(line, "::")
		if !ok {
			continue // cgroup v1 lines say nothing the v2 line does not
		}
		if unit, ok := systemSliceUnit(path); ok && !addonUnit(unit) {
			return false
		}
	}
	return true
}

// addonExe: the process in dir runs an executable under /usr/local/addons/ (occulited B-59: a
// root daemon an installer started, whose title is a bare name - RedMatic's node-red - is the
// addon's by its executable alone, and the daemon found it through procexe).
func addonExe(dir string) bool {
	exe, err := os.Readlink(filepath.Join(dir, "exe"))
	return err == nil && strings.HasPrefix(exe, "/usr/local/addons/")
}

// addonUserProc: the process in dir (/proc/<pid>) runs as an addon user - its real, effective,
// saved and file system uids all from AddonUIDBase up.
func (p Policy) addonUserProc(dir string) bool {
	if p.AddonUIDBase <= 0 {
		return false
	}
	status, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(status), "\n") {
		rest, ok := strings.CutPrefix(line, "Uid:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return false
		}
		for _, v := range f {
			if n, err := strconv.Atoi(v); err != nil || n < p.AddonUIDBase {
				return false
			}
		}
		return true
	}
	return false
}

// systemSliceUnit names the unit a cgroup v2 path under /system.slice/ belongs to.
func systemSliceUnit(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/system.slice/")
	if !ok {
		return "", false
	}
	unit, _, _ := strings.Cut(rest, "/")
	return unit, unit != ""
}

func addonUnit(unit string) bool {
	return (strings.HasPrefix(unit, "addon-") && strings.HasSuffix(unit, ".service")) || addonScopeRe.MatchString(unit)
}

// adoptShape is lite-addon-rc's one call (addonctl.go adoptRC): adopt, then addon ids.
func adoptShape(_ Policy, args []string) bool {
	if len(args) == 0 || args[0] != "adopt" {
		return false
	}
	for _, id := range args[1:] {
		if !shapeAddonIDRe.MatchString(id) {
			return false
		}
	}
	return true
}
