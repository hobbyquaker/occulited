package system

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// Priv is the privilege boundary (task 17): root does the operations itself (priv.Local, the
// default); the unprivileged occulite user goes through the helper (priv.Client, set by main).
var Priv priv.Ops = priv.Local{}

// touch creates a marker file, remove deletes one (missing = fine), both through Priv.
func touch(path string, mode os.FileMode) error { return Priv.Touch(path, mode) }
func remove(path string) error                  { return Priv.Remove(path) }

// run executes a command through Priv in exec.CombinedOutput's shape.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return priv.AsExitError(Priv.Run(ctx, name, args, nil))
}

// runOutput is exec's Output: stdout only.
func runOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	r, err := Priv.Run(ctx, name, args, nil)
	if err != nil {
		return r.Stdout, err
	}
	if r.Exit != 0 {
		return r.Stdout, &priv.ExitError{Code: r.Exit, Output: r.Combined()}
	}
	return r.Stdout, nil
}

// Service is one managed service as the UI shows it.
type Service struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // "system" (init script) or "addon" (rc.d script)
	Script  string `json:"script"`
	Running bool   `json:"running"`
	// OneShot (30.1): the unit ran to completion and nothing of it is left running - a
	// Type=oneshot unit with no task in its cgroup. Result is systemd's Result for it
	// ("success", "exit-code", ...), so the page can say Completed or Failed rather than
	// Running or Stopped.
	OneShot bool   `json:"oneshot,omitempty"`
	Result  string `json:"result,omitempty"`
	// Failed (B-65): systemd's ActiveState is "failed" - the last run ended in an error, whatever
	// the unit's type. It is what the page marks red; a unit that is merely stopped - a static
	// one, one a timer starts, one that only runs at boot - is not a failure.
	Failed bool `json:"failed,omitempty"`
	// Skipped (B-65): the unit is inactive because its last start was skipped - a Condition*= was
	// not met (ConditionResult=no after a check that happened) or ExecCondition= said no
	// (Result=exec-condition). hs485d without a wired interface and hmlangw outside LAN gateway
	// mode are such units: not meant to run on this box, and not failures either.
	Skipped bool `json:"skipped,omitempty"`
	// Note says why a skipped interface daemon is off: the radio plan's reason (task 129), such as
	// "off by choice" or "not required: HmIP and BidCos-RF do not share a module".
	Note string `json:"note,omitempty"`
	// Starting (task 94): systemd is starting the unit - ActiveState activating, not waiting for an
	// automatic restart - so it is neither running nor stopped: hmipserver's JVM for about 40 s
	// after the web UI is up. StartingS is how long, in whole seconds, from its
	// InactiveExitTimestampMonotonic (B-61's monotonic rule); absent when systemd did not say.
	Starting  bool  `json:"starting,omitempty"`
	StartingS int64 `json:"starting_s,omitempty"`
	// Ended (openccu-lite B-158): an addon that keeps a daemon (declared or learned) whose unit is
	// active with nothing left in it - its daemon ended. Running is false then, so Start is offered;
	// EndedAt is the time of the unit's last journal line of this boot, EndedLog those lines.
	Ended    bool     `json:"ended,omitempty"`
	EndedAt  string   `json:"ended_at,omitempty"`
	EndedLog []string `json:"ended_log,omitempty"`
	// Stray: the addon\'s daemon runs, but outside its unit (started by an installer or by hand);
	// Running is true, and Restart puts it back into the unit (task 48).
	Stray bool `json:"stray,omitempty"`
	// PID is systemd's MainPID, or - for a running unit without one, every generated addon unit
	// being Type=oneshot - the leader of the unit's cgroup (task 49): the process whose parent is
	// not in the cgroup, the oldest of them when there are several. For a stray addon it is the
	// leader of the processes found outside the unit, so a pid never points into the wrong
	// cgroup unannounced. PIDsMore counts the other processes and Procs lists them all, the
	// leader first, capped at maxProcs; both only where the pid did not come from MainPID.
	PID      int           `json:"pid,omitempty"`
	PIDsMore int           `json:"pids_more,omitempty"`
	Procs    []ServiceProc `json:"procs,omitempty"`
	Enabled  bool          `json:"enabled"`
	// UnitFileState is systemd's raw state behind Enabled (task 49): enabled, enabled-runtime,
	// alias, indirect, generated (the addon units), static, disabled, masked, masked-runtime (what
	// the page's Disable does).
	UnitFileState string `json:"unit_file_state,omitempty"`
	Protocol      string `json:"protocol,omitempty"` // for the interface processes
	Port          int    `json:"port,omitempty"`
	Managed       bool   `json:"managed"` // start/stop/restart offered
	// Category sorts the list for the page: "core" (the Homematic daemons, occulited and the web
	// server), "addon", "occu" (the firmware's init steps as units), "system" (everything else:
	// ssh, time, cron, systemd's own).
	Category string `json:"category"`
	// UI: the web interface runs through this unit (task 243) - no stop, no disable from the page.
	UI bool `json:"ui,omitempty"`
	// Description is the unit's Description= (systemd) or the built-in text for a firmware daemon.
	Description string `json:"description,omitempty"`
	// User is the unit's User= (systemd); empty = root.
	User string `json:"user,omitempty"`
	// The addon's confinement policy (D-36), generated addon units only: the mode it runs in
	// ("root"/"confined"), where that came from (AddonPolicyView.Source) and whether the addon
	// ever declared a runtime block. Undeclared is what the page marks, so a user can see that
	// nobody has said what this addon needs.
	PolicyMode   string `json:"policy_mode,omitempty"`
	PolicySource string `json:"policy_source,omitempty"`
	Undeclared   bool   `json:"undeclared,omitempty"`
	// MayMount: a root addon whose entry declares CAP_SYS_ADMIN, the one capability every other
	// root addon lost (D-66). RemountRefused: the unit's output in this boot shows a refused
	// `mount -o remount,rw /` - a hint that the addon still writes the CCU way and its writes
	// went into the writable extension directory instead, not an error.
	MayMount       bool `json:"may_mount,omitempty"`
	RemountRefused bool `json:"remount_refused,omitempty"`
	// Resource use, systemd only (cgroup accounting): bytes, seconds of CPU time, start time.
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
	CPUSeconds  float64 `json:"cpu_seconds,omitempty"`
	Since       string  `json:"since,omitempty"`
	// Images as on Addon (openccu-lite task 100), for a generated addon unit whose addon declares
	// them. Set per request by GET /services.
	Images map[string]string `json:"images,omitempty"`
}

// coreServices are what the Services page shows by default: the Homematic daemons, occulited, and
// lighttpd, which serves the web interface (openccu-lite task 243, the maintainer: "lighttpd should
// appear under system-services even when system services are hidden").
var coreServices = map[string]bool{"rfd": true, "hs485d": true, "hmipserver": true, "multimacd": true, "eq3configd": true, "hmlangw": true, "occulited": true, "occulited-helper": true, "lighttpd": true}

// uiServices carry the web interface itself (task 243): lighttpd serves it, occulited answers it,
// the helper does its root work. A stop or a disable from the page would cut the page off until a
// reboot or a restart over ssh, so the API refuses both (ErrCutsUI) and the page offers neither;
// a restart is offered, and the page reconnects.
var uiServices = map[string]bool{"lighttpd": true, "occulited": true, "occulited-helper": true}

// CutsUI says whether stopping or disabling the unit takes the web interface away (uiServices).
func CutsUI(id string) bool { return uiServices[strings.TrimSuffix(id, ".service")] }

func categoryOf(id string) string {
	switch {
	case coreServices[id]:
		return "core"
	case strings.HasPrefix(id, "addon-"):
		return "addon"
	case strings.HasPrefix(id, "occu-"):
		return "occu"
	}
	return "system"
}

// ServiceManager lists and controls the units: SystemdServices on every product (D-39), NoInit in
// the development mode without systemd.
type ServiceManager interface {
	List() ([]Service, error)
	Control(ctx context.Context, id, action string) (output string, err error)
}

// ErrNoInit is NoInit's answer to every control: occulited runs without systemd.
var ErrNoInit = errors.New("no init control: occulited runs without systemd (development mode), services and addons are read-only")

// NoInit is the service manager of the development mode (task 187): occulited started where
// systemd is not PID 1 - a workstation, a test - lists no services and controls none. The busybox
// init implementation it replaces went with D-39: every product is systemd.
type NoInit struct{}

// List returns no services.
func (NoInit) List() ([]Service, error) { return []Service{}, nil }

// Control refuses with ErrNoInit.
func (NoInit) Control(context.Context, string, string) (string, error) { return "", ErrNoInit }

// AddonScripts is the addons' rc.d layer (D-36 keeps their scripts unmodified): it runs an addon's
// rc.d script (info, uninstall), the firmware's install_addon, an addon's update check, reads the
// nav.d drop-ins and asks for the reboot an installer wants. SystemdAddons runs it in a transient
// scope and lets the addon's unit take the daemon over; in the development mode without systemd it
// is the read-only addon list (task 187: it was BusyboxServices, whose init-script listing and
// control went with the busybox init).
type AddonScripts struct {
	Root Root
	// Exec runs the addon scripts (install_addon, rc.d uninstall) and returns their combined
	// output; nil = run through Priv. The systemd manager wraps them in a transient scope.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
	// UpdateToken is sent as Bearer when an addon's update check goes to a box-relative URL:
	// the CGI route needs a session or token (B-2).
	UpdateToken string
	// Credential is who an addon's own rc.d script runs as for info, init and uninstall - its user,
	// when the systemd manager confines it (openccu-lite B-119); nil, or nil for an id, = root, as
	// the rc.d ABI always ran them.
	Credential func(id string) *priv.Credential
	// HTTP fetches an addon's own update URL when it is not box-relative (openccu-lite task 231:
	// a client on occulited's trust store); nil = http.DefaultClient.
	HTTP *http.Client
}

// credential is Credential's answer for id, nil where there is none.
func (b AddonScripts) credential(id string) *priv.Credential {
	if b.Credential == nil {
		return nil
	}
	return b.Credential(id)
}

// addonScriptEnv is the environment an addon's script gets when it runs as the addon's user: the
// firmware's PATH (/etc/profile) and the addon's own directory as HOME, nothing of the daemon's.
func addonScriptEnv(id string) []string {
	return []string{"PATH=/bin:/sbin:/usr/bin:/usr/sbin", "HOME=/usr/local/addons/" + id, "USER=addon-" + id, "LOGNAME=addon-" + id}
}

// addonScript runs the addon's rc.d script with action: as the addon's user through Priv.RunAs when
// it has a credential (its own code, its own rights - B-119), as root through Priv.Run otherwise.
// The shape is exec.CombinedOutput's; a non-zero exit is a *priv.ExitError.
func (b AddonScripts) addonScript(ctx context.Context, id, script, action string) ([]byte, error) {
	cred := b.credential(id)
	if cred == nil {
		return run(ctx, script, action)
	}
	return priv.AsExitError(Priv.RunAs(ctx, *cred, script, []string{action}, addonScriptEnv(id), "/", nil))
}

// addonScriptOutput is addonScript with stdout only (exec's Output).
func (b AddonScripts) addonScriptOutput(ctx context.Context, id, script, action string) ([]byte, error) {
	cred := b.credential(id)
	if cred == nil {
		return runOutput(ctx, script, action)
	}
	r, err := Priv.RunAs(ctx, *cred, script, []string{action}, addonScriptEnv(id), "/", nil)
	if err != nil {
		return r.Stdout, err
	}
	if r.Exit != 0 {
		return r.Stdout, &priv.ExitError{Code: r.Exit, Output: r.Combined()}
	}
	return r.Stdout, nil
}

func (b AddonScripts) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	if b.Exec != nil {
		return b.Exec(ctx, name, args...)
	}
	return run(ctx, name, args...)
}

// rcdAddonIDs lists the addon ids in rc.d: every entry except directories and the `<id>.script`
// files - the addon's own script behind the addon-rc wrapper (28.8), which is not an addon of its
// own. Before this the Services and Addons pages showed every adopted addon twice on a systemd
// box, once as `<id>` and once as `<id>.script` (seen on the first LXC container, 2026-09-09).
func rcdAddonIDs(root Root) []string {
	entries, _ := os.ReadDir(root.join("/usr/local/etc/config/rc.d"))
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".script") {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// --- addons ---

// Addon is an installed addon as its rc.d `info` output describes it.
type Addon struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Info       string   `json:"info,omitempty"` // HTML, from the addon; sanitised by the UI
	Update     string   `json:"update,omitempty"`
	ConfigURL  string   `json:"config_url,omitempty"`
	Operations []string `json:"operations"`
	Running    bool     `json:"running"`
	OneShot    bool     `json:"oneshot,omitempty"` // 30.1: ran once and ended, nothing left running
	Result     string   `json:"result,omitempty"`
	Stray      bool     `json:"stray,omitempty"` // runs outside its unit
	// Ended, EndedAt, EndedLog as on Service (openccu-lite B-158): the addon's daemon ended.
	Ended    bool     `json:"ended,omitempty"`
	EndedAt  string   `json:"ended_at,omitempty"`
	EndedLog []string `json:"ended_log,omitempty"`
	// Failed as on Service (B-65): the addon's unit is in systemd's failed state (task 248: the
	// Addons dot's red)
	Failed bool `json:"failed,omitempty"`
	// Skipped as on Service: the addon's unit did not start because its ExecCondition= said no -
	// the fork's program check (openccu-lite B-267): a restore brought the addon back without the
	// program files it keeps in .nobackup directories. Not a failure; PayloadMissing says why.
	Skipped bool `json:"skipped,omitempty"`
	PID     int  `json:"pid,omitempty"`
	// Settings comes from hm_addons.cfg when the addon registered a settings page there.
	Settings *AddonSettings `json:"settings,omitempty"`
	// Enabled: the rc.d script is executable. RegaDependent addons are disabled after an update
	// from OpenCCU and shown as "disabled, incompatible".
	Enabled       bool   `json:"enabled"`
	RegaDependent bool   `json:"rega_dependent,omitempty"`
	RegaReason    string `json:"rega_reason,omitempty"`
	// BinaryIncompatible: the addon carries binaries this box cannot execute (task 25). It is
	// disabled the same way and shown as "disabled, needs update"; the repair is a release for
	// this architecture, not a switch.
	BinaryIncompatible bool   `json:"binary_incompatible,omitempty"`
	BinaryReason       string `json:"binary_reason,omitempty"`
	// The confinement policy (D-36), systemd products only - the same three fields as Service,
	// so the Addons page can show the badge without asking for every addon's policy one by one.
	PolicyMode   string `json:"policy_mode,omitempty"`
	PolicySource string `json:"policy_source,omitempty"`
	Undeclared   bool   `json:"undeclared,omitempty"`
	// FromCCU (occulited task 28): the addon came along from the CCU (policy source "migrated").
	// FailedLog is the failed unit's last journal line, for such an addon only; RCTarget is where
	// its rc.d entry leads when that is a file outside /usr/local/addons/ (a user's own script,
	// /usr/local/bin/hdmi-wlan-disable.sh) - named by "remove the rc.d entry", removed only on request.
	FromCCU   bool   `json:"from_ccu,omitempty"`
	FailedLog string `json:"failed_log,omitempty"`
	RCTarget  string `json:"rc_target,omitempty"`
	// MayMount and RemountRefused as on Service (D-66).
	MayMount       bool `json:"may_mount,omitempty"`
	RemountRefused bool `json:"remount_refused,omitempty"`
	// LighttpdRejected (occulited task 23): the addon ships a lighttpd fragment the last sync refused,
	// so none of it is in use - the verdict from <id>.conf.rejected, gone once a later install
	// brings a fragment that passes.
	LighttpdRejected *LighttpdRejection `json:"lighttpd_rejected,omitempty"`
	// APIScopes are the scopes the addon's own API token holds (task 66): the catalogue's
	// runtime.api_scopes less what an addon never gets; absent when it has no token of its own.
	APIScopes []string `json:"api_scopes,omitempty"`
	// SessionHeader: the addon's settings page reads the gate's X-Occulite-Session, so the shell
	// opens it without ?sid= (task 88, D-67). Set per request by GET /addons (httpapi.SessionHeader).
	SessionHeader bool `json:"session_header,omitempty"`
	// Fullscreen: the addon's stored manifest declares ui.fullscreen (occulited task 24) - its
	// frontend offers its own way back, so the shell offers to show it as the whole window. The
	// user's choice is a preference (auth.AddonPreference.Fullscreen), not this. Set per request
	// by GET /addons (httpapi.Fullscreen).
	Fullscreen bool `json:"fullscreen,omitempty"`
	// LegacySession: the shell opens the settings page with the session's ?sid=@..@ alias (task
	// 125): the addon does not read the header, and the legacy session is switched on for it.
	// Set per request by GET /addons (httpapi.LegacySession).
	LegacySession bool `json:"legacy_session,omitempty"`
	// StartEarlyDeclared: the addon's manifest declares runtime.start "early" (task 119);
	// StartEarly: it starts early at the next boot - declared, and not switched off by the user.
	// Systemd products only.
	StartEarlyDeclared bool `json:"start_early_declared,omitempty"`
	StartEarly         bool `json:"start_early,omitempty"`
	// PayloadMissing (openccu-lite task 146): the addon's program files are gone - a CCU backup
	// leaves out its .nobackup directories, so after a restore only its settings and data are
	// back; PayloadMissingDirs names them (relative to its directory). ReinstallDismissed: the
	// user dismissed the reinstall hint at this version. Set per request by GET /addons from
	// the PayloadRecord.
	PayloadMissing     bool     `json:"payload_missing,omitempty"`
	PayloadMissingDirs []string `json:"payload_missing_dirs,omitempty"`
	ReinstallDismissed bool     `json:"reinstall_dismissed,omitempty"`
	// Images are the addon's icon and logo as its stored manifest declares them (openccu-lite task
	// 100): kind (icon, icon-dark, logo, logo-dark) → the URL the shell loads it from, on this
	// origin. Absent for an addon that declares none. Set per request by GET /addons.
	Images map[string]string `json:"images,omitempty"`
}

// AddonSettings is one entry of hm_addons.cfg.
type AddonSettings struct {
	ConfigURL   string            `json:"config_url"`
	Name        string            `json:"name"`
	Description map[string]string `json:"description"`
}

// ListAddons runs every rc.d script's `info` (as the WebUI does) and merges hm_addons.cfg.
func (b AddonScripts) ListAddons(ctx context.Context) ([]Addon, error) {
	settings := ParseHMAddonsCfg(readFile(b.Root.join("/usr/local/etc/config/hm_addons.cfg")))
	out := []Addon{}
	for _, id := range rcdAddonIDs(b.Root) {
		script := b.Root.join("/usr/local/etc/config/rc.d/" + id)
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		// info needs no root; a confined addon's script is its own code and runs as its user (B-119),
		// any other as root, as rc.d scripts may be root-only
		raw, _ := b.addonScriptOutput(cctx, id, script, "info")
		cancel()
		// running, the pid and the rest are the unit's (SystemdAddons.ListAddons); without systemd
		// nothing says it
		a := parseInfo(id, string(raw))
		a.Enabled = b.Root.AddonEnabled(id)
		a.RegaDependent, a.RegaReason = b.Root.RegaDependence(id)
		a.BinaryIncompatible, a.BinaryReason = b.Root.BinaryCompatibility(id)
		a.LighttpdRejected = b.Root.LighttpdRejection(id)
		if s, ok := settings[id]; ok {
			s := s
			a.Settings = &s
		}
		// the name a person reads: the addon's own, else the one it registered in hm_addons.cfg,
		// else a known one; empty only when there is none, and the pages show the id then
		if a.Name == "" {
			if a.Settings != nil && a.Settings.Name != "" {
				a.Name = a.Settings.Name
			} else {
				a.Name = KnownAddonNames[id]
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// parseInfo parses the `Key: value` lines of an rc.d info output (docs/ccu-addon-howto 03).
func parseInfo(id, raw string) Addon {
	a := Addon{ID: id, Operations: []string{}}
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Name":
			a.Name = v
		case "Version":
			a.Version = v
		case "Info":
			a.Info = v
		case "Update":
			a.Update = v
		case "Config-Url":
			a.ConfigURL = v
		case "Operations":
			a.Operations = strings.Fields(v)
		}
	}
	return a
}

// ParseHMAddonsCfg parses the flat Tcl `array get` list of hm_addons.cfg:
//
//	id {CONFIG_URL /addons/x/settings.cgi CONFIG_DESCRIPTION {de {<li>..</li>} en {<li>..</li>}} ID x CONFIG_NAME X}
//
// A brace-aware tokenizer, because the values contain spaces and nested braces.
func ParseHMAddonsCfg(s string) map[string]AddonSettings {
	out := map[string]AddonSettings{}
	toks := tclList(s)
	for i := 0; i+1 < len(toks); i += 2 {
		id := toks[i]
		kv := tclList(toks[i+1])
		as := AddonSettings{Description: map[string]string{}}
		for j := 0; j+1 < len(kv); j += 2 {
			switch kv[j] {
			case "CONFIG_URL":
				as.ConfigURL = kv[j+1]
			case "CONFIG_NAME":
				as.Name = kv[j+1]
			case "CONFIG_DESCRIPTION":
				d := tclList(kv[j+1])
				for k := 0; k+1 < len(d); k += 2 {
					as.Description[d[k]] = d[k+1]
				}
			}
		}
		out[id] = as
	}
	return out
}

// tclList splits a Tcl list into its elements, honouring {braces}.
func tclList(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '{' {
			depth, j := 0, i
			for ; j < len(s); j++ {
				if s[j] == '{' {
					depth++
				} else if s[j] == '}' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			out = append(out, s[i+1:j])
			i = j + 1
			continue
		}
		j := i
		for j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != '\n' && s[j] != '\r' {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

// --- log ---

// LogLine is one parsed syslog line.
type LogLine struct {
	Time      string `json:"time"`
	Timestamp string `json:"timestamp,omitempty"` // RFC 3339 with the journal's precision
	Host      string `json:"host,omitempty"`
	Facility  string `json:"facility,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Tag       string `json:"tag,omitempty"`
	Unit      string `json:"unit,omitempty"` // systemd unit (journald only)
	PID       int    `json:"pid,omitempty"`
	Message   string `json:"message"`
	Cursor    string `json:"cursor,omitempty"` // journal cursor (journald only)
	// MonotonicUS is the time since the boot began, in microseconds: for a kernel line the kernel's
	// own stamp (what dmesg prints as [   12.345678]), for any other journal line journald's.
	MonotonicUS int64 `json:"monotonic_us,omitempty"`
	// Area is the area of one of occulited's lines (task 186: OCCULITED_AREA, logctl.Areas).
	Area string `json:"area,omitempty"`
}

// LogQuery filters the log.
type LogQuery struct {
	Tag      string
	Unit     string // a unit name; the kernel's ring buffer ignores it
	Severity string // minimum: debug, info, notice, warning, err, crit, alert, emerg
	Contains string
	Since    string // journald: what journalctl --since accepts ("-1h", "@1757350800", "2026-09-08 19:00")
	Until    string // journald: --until, the same forms (task 31: a from/to range in the Log page)
	Limit    int
	Follow   bool
	// Kernel is the kernel log (task 93): the journal's kernel transport (journalctl -k), or the
	// ring buffer through dmesg where there is no journal.
	Kernel bool
	// NoKernel is the log without the kernel's messages (the Log page's System source): every
	// journal transport but the kernel's.
	NoKernel bool
	// Boot is one boot of the journal (journalctl --boot): a boot id, 0 for this boot or -N for the
	// Nth before it; "" = every boot the journal holds (the kernel log: this boot). ValidBoot
	// checks it.
	Boot string
	// Run is one run's lines (task 102): OCCULITE_RUN_ID in the journal, the run_id the fallback
	// writes into the message on busybox syslog. runlog.ValidID checks it.
	Run string
	// Area is one of occulited's areas (task 186): OCCULITED_AREA in the journal, a field match
	// like Run. The caller checks it against logctl.Areas. The kernel's lines have none.
	Area string
	// The page (task 178): a read is the newest Limit entries unless one of these says otherwise.
	// Before is a line's Cursor: the Limit entries older than it (journalctl -r --after-cursor,
	// turned oldest first again). After is the Limit entries newer than a cursor (--after-cursor
	// forward; following, the entries since it are replayed before the new ones). Head is the
	// oldest Limit entries of the boot or the range (journalctl -n +N). One of the three at most;
	// a dmesg cursor is the line's stamp and its ordinal among equal stamps ("<monotonic_us>/<k>").
	Before string
	After  string
	Head   bool
}

var severityRank = map[string]int{"debug": 0, "info": 1, "notice": 2, "warning": 3, "warn": 3, "err": 4, "error": 4, "crit": 5, "alert": 6, "emerg": 7}

// LogReader reads the log: the journal (JournalLog), or the kernel's ring buffer (Dmesg).
type LogReader interface {
	Read(q LogQuery) ([]LogLine, error)
}

// lineMatcher is the filter of a log read without journalctl - the kernel's ring buffer through
// dmesg (Dmesg): the tag, the severity floor, the text and a run's id. The unit and the time range
// are ignored. /log and the download share it, so the two can never disagree about what a filter
// lets through.
func lineMatcher(q LogQuery) func(LogLine) bool {
	minSev := -1
	if q.Severity != "" {
		if r, ok := severityRank[strings.ToLower(q.Severity)]; ok {
			minSev = r
		}
	}
	contains := strings.ToLower(q.Contains)
	// task 102: a run's lines outside the journal are the ones with the run's id as an attribute
	runMark := ""
	if q.Run != "" {
		runMark = "run_id=" + q.Run
	}
	return func(l LogLine) bool {
		if q.Tag != "" && !strings.EqualFold(l.Tag, q.Tag) {
			return false
		}
		if runMark != "" && !strings.Contains(l.Message, runMark) {
			return false
		}
		if q.Area != "" && l.Area != q.Area {
			return false
		}
		if minSev >= 0 {
			if r, ok := severityRank[l.Severity]; !ok || r < minSev {
				return false
			}
		}
		return contains == "" || strings.Contains(strings.ToLower(l.Message), contains)
	}
}
