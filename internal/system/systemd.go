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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The systemd implementations of the service and log interfaces (task 20, D-30). Nothing here
// links to libsystemd: systemctl and journalctl speak JSON and are always present on a systemd
// box. Chosen at start by HasSystemd.

// HasSystemd reports whether systemd is PID 1 on this root (/run/systemd/system exists).
func (r Root) HasSystemd() bool {
	st, err := os.Stat(r.join("/run/systemd/system"))
	return err == nil && st.IsDir()
}

// SystemdServices manages units through systemctl.
type SystemdServices struct {
	// Now is the clock used for timer countdowns; nil = time.Now.
	Now func() time.Time

	Root Root
	Run  Runner // nil = exec

	// SwitchFile is where the Services page's persistent enable/disable is kept: the units
	// switched off with a runtime mask (and the ones switched on that the image does not ship
	// enabled), replayed at every start because /run does not survive a boot (B-26). Empty
	// means no persistence - development and the tests that do not care.
	SwitchFile string

	// StopWait is how long an addon's leftover processes get after TERM before they are KILLed
	// when the addon is put back into its unit (task 48); 0 = five seconds. Tests shorten it.
	StopWait time.Duration

	// ActiveWait is how long a freshly installed addon's unit may stay activating after its start
	// before the install reports the state it is in (B-186); 0 = fifteen seconds. Tests shorten it.
	ActiveWait time.Duration

	// LookPath finds systemd-analyze for the own timers' checks (task 50); nil = exec.LookPath.
	LookPath func(file string) (string, error)

	// Cache keeps the units' `systemctl show` between two listings and reads the changing figures
	// from the cgroups instead (B-83, unitcache.go); nil = every listing shows every unit. A
	// pointer, so the copies of this value that the addon manager and the API hold share it.
	Cache *UnitCache
}

// managedUnits are the firmware daemons the UI offers to control (the rest of the unit list is
// shown, not touched); addon units (addon-*.service and units whose file lives under
// /usr/local) are always managed.
var managedUnits = map[string]struct {
	protocol string
	port     int
}{
	"rfd":        {"BidCos-RF", 32001},
	"hs485d":     {"BidCos-Wired", 32000},
	"hmipserver": {"HmIP-RF", 32010},
	"multimacd":  {"", 0},
	"eq3configd": {"", 0},
	"lighttpd":   {"", 80},
	"sshd":       {"", 22},
	"chronyd":    {"", 0},
	"chrony":     {"", 0}, // the lite_systemd overlay's name (overlay/lite_systemd, chrony.service)
	"ssdpd":      {"", 0},
	"hmlangw":    {"", 0},
	"crond":      {"", 0},
	"occulited":  {"", 8183},
}

type unitRow struct {
	Unit        string `json:"unit"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

func (s SystemdServices) run(ctx context.Context, args ...string) ([]byte, error) {
	// every systemctl call of this package passes here, so a command that changes units tells the
	// cache - after it ran, so that a listing that raced it is not taken as fresh
	if s.Cache != nil {
		defer s.Cache.noteCommand(args)
	}
	if s.Run != nil {
		return s.Run(ctx, "systemctl", args...)
	}
	return run(ctx, "systemctl", args...) // SYSTEMD_COLORS/PAGER: the helper and the unit set them
}

// serviceProps are what List reads per unit with `systemctl show`.
// InactiveExitTimestampMonotonic is when an activating unit began starting (task 94).
var serviceProps = []string{"MainPID", "UnitFileState", "FragmentPath", "SourcePath", "MemoryCurrent", "CPUUsageNSec", "ActiveEnterTimestamp", "ActiveEnterTimestampMonotonic", "InactiveExitTimestampMonotonic", "User", "Type", "Result", "TasksCurrent", "ControlGroup", "ConditionResult", "ConditionTimestampMonotonic"}

// run2 runs a program other than systemctl through the same Runner (tests) or the helper.
func (s SystemdServices) run2(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, name, args...)
	}
	return run(ctx, name, args...)
}

// List returns every service unit: the firmware's, occulited's, and the ones addons brought.
func (s SystemdServices) List() ([]Service, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.run(ctx, "list-units", "--type=service", "--all", "--no-pager", "--plain", "--output=json")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var rows []unitRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	var list []Service
	rowsByUnit := map[string]unitRow{}
	for _, u := range rows {
		rowsByUnit[u.Unit] = u
		id := strings.TrimSuffix(u.Unit, ".service")
		if strings.HasPrefix(id, "systemd-") || strings.HasPrefix(id, "dbus") || strings.Contains(id, "@") || strings.HasPrefix(id, "user") || strings.HasPrefix(id, "getty") || strings.HasPrefix(id, "serial-getty") {
			continue
		}
		// B-58: an addon unit whose file is gone (disabled, uninstalled) and that runs nothing is
		// a failure systemd remembers, not an addon that failed - not listed as one
		if strings.HasPrefix(id, "addon-") && u.Load == "not-found" && u.Active != "active" && u.Active != "activating" && u.Active != "deactivating" {
			continue
		}
		sv := Service{ID: id, Kind: "system", Script: u.Unit, Running: u.Active == "active" && (u.Sub == "running" || u.Sub == "exited"), Enabled: u.Load == "loaded", Description: u.Description, Category: categoryOf(id), UI: uiServices[id]}
		if m, ok := managedUnits[id]; ok {
			sv.Managed, sv.Protocol, sv.Port = true, m.protocol, m.port
		}
		if strings.HasPrefix(id, "addon-") {
			sv.Kind, sv.Managed = "addon", true
		}
		list = append(list, sv)
	}
	// details for what the page shows: pid, enabled state, and whether the unit came from an addon.
	// One `systemctl show` for all units (B-60): one per unit meant 60+ forks through the helper
	// every poll of the Services page - the helper alone sat at 85 % of a Pi 4 core while the
	// page was open. B-83: with the cache, only the units whose state changed since the last
	// listing, the figures from the cgroups.
	units := make([]string, len(list))
	for i := range list {
		units[i] = list[i].Script
	}
	var shown map[string]map[string]string
	if s.Cache != nil {
		shown = s.Cache.serviceDetails(ctx, s, units, rowsByUnit)
	} else {
		shown = s.showAll(ctx, units, serviceProps...)
	}
	uptime := s.uptime()
	for i := range list {
		props := shown[list[i].Script]
		// 30.1: a one-shot that has run and left nothing behind - an addon whose script only
		// prepares things, or a timer's service - is neither running nor stopped
		// The verdict needs the unit to have finished: active (exited) with no task left ("[not
		// set]" once systemd dropped the cgroup counts as none), or failed. A dead unit is stopped,
		// an activating one is starting. A daemon that was restarted from the addon's own page
		// before 28.8 lives outside the unit and looks finished here - the wrapper ends that.
		if props["Type"] == "oneshot" {
			row := rowsByUnit[list[i].Script]
			tasks, err := strconv.Atoi(props["TasksCurrent"])
			finished := row.Active == "active" && row.Sub == "exited" && (err != nil || tasks == 0)
			// an addon whose processes run outside the unit is neither finished nor stopped (a
			// process in the unit's own cgroup is not "outside", task 48). The pid shown is the
			// leader of what runs outside, so that it never points into the wrong cgroup
			// unannounced (task 49) - stray says where it is.
			if strings.HasPrefix(list[i].ID, "addon-") && (finished || !list[i].Running) {
				if outside := s.addonLeftovers(strings.TrimPrefix(list[i].ID, "addon-"), ""); len(outside) > 0 {
					list[i].Running, list[i].Stray = true, true
					finished = false
					pids := make([]int, len(outside))
					for k, p := range outside {
						pids[k] = p.PID
					}
					leader, rest := leaderOf(s.Root, pids)
					list[i].PID, list[i].PIDsMore, list[i].Procs = leader, len(rest), procList(s.Root, leader, rest)
				}
			}
			if finished || row.Active == "failed" {
				list[i].OneShot = true
				list[i].Result = props["Result"]
			}
		}
		// B-65: red is for a failure only. A unit a condition kept from starting is skipped, and
		// everything else that is not running - static, started by a timer, boot-only - is stopped.
		// Both come from the list-units row and the one show above.
		state := rowsByUnit[list[i].Script]
		list[i].Failed = state.Active == "failed"
		list[i].Skipped = !list[i].Running && conditionSkipped(state.Active, props)
		// task 94: activating is starting, neither running nor stopped - hmipserver's JVM while the
		// web UI is already up
		applyStarting(&list[i], state, props, uptime)
		if pid, err := strconv.Atoi(props["MainPID"]); err == nil && pid > 0 {
			list[i].PID = pid
		}
		// task 49: a running unit without a main process - a oneshot whose script started a
		// daemon, which is every generated addon unit - shows the leader of its cgroup and how
		// many more there are. A file read per such unit, not a helper call (B-60); a completed
		// oneshot has an empty cgroup and shows nothing, as before.
		if list[i].PID == 0 && list[i].Running && !list[i].OneShot {
			if leader, rest := leaderOf(s.Root, cgroupProcs(s.Root, props["ControlGroup"])); leader > 0 {
				list[i].PID, list[i].PIDsMore, list[i].Procs = leader, len(rest), procList(s.Root, leader, rest)
			}
		}
		if u := props["User"]; u != "" && u != "root" {
			list[i].User = u
		}
		// "[not set]" when accounting is off; a stopped unit reports nothing useful
		if list[i].Running {
			if n, err := strconv.ParseInt(props["MemoryCurrent"], 10, 64); err == nil && n < 1<<60 {
				list[i].MemoryBytes = n
			}
			if n, err := strconv.ParseInt(props["CPUUsageNSec"], 10, 64); err == nil && n < 1<<62 {
				list[i].CPUSeconds = float64(n) / 1e9
			}
			// B-61: the wall-clock stamp is wrong for a unit that started before the clock was
			// set (a Pi without an RTC gets its time from chronyd seconds after boot; the helper
			// showed "180 d" of uptime), so the monotonic stamp against /proc/uptime comes first.
			// --timestamp=unix prints "@<epoch>" for the wall-clock one.
			if n, err := strconv.ParseInt(props["ActiveEnterTimestampMonotonic"], 10, 64); err == nil && n > 0 && uptime > 0 {
				age := uptime - time.Duration(n)*time.Microsecond
				if age < 0 {
					age = 0
				}
				list[i].Since = s.now().Add(-age).Truncate(time.Second).Format(time.RFC3339)
			} else if n, err := strconv.ParseInt(strings.TrimPrefix(props["ActiveEnterTimestamp"], "@"), 10, 64); err == nil && n > 0 {
				list[i].Since = time.Unix(n, 0).Format(time.RFC3339)
			}
		}
		// task 49: the raw state for the Enabled column (static, generated, masked-runtime, ...)
		// beside the verdict the switch has always given
		list[i].UnitFileState = props["UnitFileState"]
		list[i].Enabled = unitFileEnabled(props["UnitFileState"])
		if strings.HasPrefix(props["FragmentPath"], "/usr/local/") || strings.HasPrefix(props["SourcePath"], "/usr/local/") {
			list[i].Kind, list[i].Managed, list[i].Category = "addon", true, "addon"
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind // addon before system
		}
		return list[i].ID < list[j].ID
	})
	return list, nil
}

// showAll reads props of every unit with a single `systemctl show`: it prints one block per
// unit, blank-line separated, in argument order; Id names the unit so the order is not relied on.
// A unit systemctl did not answer for has no entry (the callers read a nil map).
func (s SystemdServices) showAll(ctx context.Context, units []string, props ...string) map[string]map[string]string {
	all := map[string]map[string]string{}
	if len(units) == 0 {
		return all
	}
	args := append([]string{"show", "--timestamp=unix", "-p", "Id," + strings.Join(props, ","), "--"}, units...)
	out, err := s.run(ctx, args...)
	if err != nil {
		return all
	}
	block := map[string]string{}
	n := 0
	flush := func() {
		if len(block) == 0 {
			return
		}
		id := block["Id"]
		if id == "" && n < len(units) {
			id = units[n]
		}
		all[id] = block
		block = map[string]string{}
		n++
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			block[k] = strings.TrimSpace(v)
		}
	}
	flush()
	return all
}

// uptime is the box's time since boot from /proc/uptime (the monotonic clock systemd's
// *TimestampMonotonic properties count in); 0 when it cannot be read.
func (s SystemdServices) uptime() time.Duration {
	b, err := os.ReadFile(s.Root.join("/proc/uptime"))
	if err != nil {
		return 0
	}
	f, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	secs, err := strconv.ParseFloat(f, 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// Control runs start, stop, restart, enable or disable on a unit. enable and disable are the
// persistent switch, which on a read-only rootfs is a runtime mask replayed at start rather than
// systemctl's own enablement - see unitswitch.go and B-26.
func (s SystemdServices) Control(ctx context.Context, id, action string) (string, error) {
	switch action {
	case "start", "stop", "restart", "enable", "disable":
	default:
		return "", errors.New("action must be start, stop, restart, enable or disable")
	}
	if strings.ContainsAny(id, "/ \t") || id == "" {
		return "", errors.New("bad unit id")
	}
	unit := id
	if !strings.Contains(unit, ".") {
		unit += ".service"
	}
	if action == "enable" || action == "disable" {
		// an own timer (task 50) is switched with a runtime enable or disable of its own, not the
		// mask the shipped units get: its file lives at the very path a runtime mask would take
		if name, ok := s.ownTimerName(unit); ok {
			return s.SetLocalTimerEnabled(ctx, name, action == "enable")
		}
		if _, ok := s.ownTimerServiceName(unit); ok {
			return "", fmt.Errorf("%s is switched on and off through its timer, %s", unit, strings.TrimSuffix(unit, ".service")+".timer")
		}
		return s.switchUnit(ctx, unit, action == "enable")
	}
	// task 48: a restart of an addon whose daemon runs outside its unit would restart the empty
	// unit and leave the daemon where it is (the addon's start then finds it running and does
	// nothing); put it back into the unit instead - the same sequence as after an install
	if action == "restart" && strings.HasPrefix(unit, "addon-") && strings.HasSuffix(unit, ".service") {
		id := strings.TrimSuffix(strings.TrimPrefix(unit, "addon-"), ".service")
		if len(s.addonStray(ctx, id)) > 0 {
			stopped, err := s.resettleAddon(ctx, id, "")
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%d process(es) outside the unit stopped, %s started in its unit", len(stopped), unit), nil
		}
	}
	// openccu-lite B-158: an addon unit whose daemon ended is still active (exited) - a oneshot
	// with RemainAfterExit - and systemctl start does nothing to an active unit; the page's Start
	// means run it again, so it is a restart
	if action == "start" && strings.HasPrefix(unit, "addon-") && strings.HasSuffix(unit, ".service") && s.activeEmptyOneshot(ctx, unit) {
		action = "restart"
	}
	out, err := s.run(ctx, action, "--no-pager", "--", unit)
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("systemctl %s %s: %w", action, unit, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// activeEmptyOneshot: the unit is a oneshot that is active (exited) with no task left - finished,
// or its daemon ended.
func (s SystemdServices) activeEmptyOneshot(ctx context.Context, unit string) bool {
	p := s.showAll(ctx, []string{unit}, "Type", "ActiveState", "SubState", "TasksCurrent")[unit]
	if p["Type"] != "oneshot" || p["ActiveState"] != "active" || p["SubState"] != "exited" {
		return false
	}
	tasks, err := strconv.Atoi(p["TasksCurrent"])
	return err != nil || tasks == 0
}

// ---- putting an addon back into its unit (task 48) ---------------------------------------------

// addonLeftovers lists the addon's processes that are not in its unit's cgroup: in the named
// install scope when scope is set (an update script's `start` ran in it), in any other cgroup
// when it is empty (a daemon started by hand, or by the addon's own page before 28.8). A process
// whose cgroup cannot be read is taken as outside when no scope is asked for - /proc/<pid>/cgroup
// is readable for every live process, so that is one that is ending right now - and never as
// inside a scope it cannot be shown to be in.
func (s SystemdServices) addonLeftovers(id, scope string) []proc {
	return s.leftovers(id, scope, false)
}

// addonLeftoversDeep is addonLeftovers that also asks the helper for the executable of a process
// the daemon may not read (B-59): for the steps that stop what they find, not for a listing.
func (s SystemdServices) addonLeftoversDeep(id, scope string) []proc {
	return s.leftovers(id, scope, true)
}

func (s SystemdServices) leftovers(id, scope string, deep bool) []proc {
	var out []proc
	for _, p := range addonProcessesIn(s.Root, id, scope, deep) {
		if inCgroup(p.Cgroup, "addon-"+id+".service") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// resettleAddon stops the addon's unit, stops what the addon left outside it by pid, and starts
// the unit again - the one sequence behind the install step (scope = the install's scope) and
// the Services page's Restart on a stray addon (scope = "", any cgroup but the unit's).
//
// The unit's ExecStop is the addon's own `stop`, which finds its daemon wherever it runs (hmm by
// its pid file, start-stop-daemon -K), so most leftovers end there; what it did not find gets a
// TERM, a moment, and a KILL. Only pids of the addon's own processes are ever signalled - the
// scope itself is never stopped (B-3: an installer that restarted lighttpd through its init
// script leaves lighttpd in the scope, and stopping the scope took the web server down).
func (s SystemdServices) resettleAddon(ctx context.Context, id, scope string) (stopped []int, err error) {
	stopped, err = s.quietAddon(ctx, id, func() []proc { return s.addonLeftoversDeep(id, scope) })
	if err != nil {
		return stopped, err
	}
	return stopped, s.startAddonUnit(ctx, id)
}

// quietAddon is the first half of resettleAddon: the addon's unit stopped, and nothing of it left
// (B-106). A root daemon writes on its way out - Mosquitto its persistence file - so whatever gives
// a confined addon's files to its user has to run after the old processes are gone, not while they
// still run: `systemctl stop` returns once systemd has sent its signals, and the unit's cgroup is
// watched until it is empty (by the cgroup, not by name: a child whose command line does not name
// the addon's directory counts too), with a KILL by pid to what is still there after StopWait.
// Then left() - the addon's processes outside the unit - is stopped by pid and waited for.
func (s SystemdServices) quietAddon(ctx context.Context, id string, left func() []proc) (stopped []int, err error) {
	unit := "addon-" + id + ".service"
	// the unit's cgroup, read while it may still be active: a stopped unit reports none
	cg := s.showAll(ctx, []string{unit}, "ControlGroup")[unit]["ControlGroup"]
	if cg == "" {
		cg = "/system.slice/" + unit
	}
	if out, err := s.run(ctx, "stop", "--no-pager", "--", unit); err != nil {
		return nil, fmt.Errorf("systemctl stop %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	if rest := s.waitCgroupEmpty(ctx, cg); len(rest) > 0 {
		slog.Warn("addons: processes of a stopped addon unit did not end", "unit", unit, "pids", rest)
	}
	if l := left(); len(l) > 0 {
		stopped = s.stopByPID(ctx, l)
	}
	return stopped, nil
}

// unitSettled waits up to ActiveWait for an addon's unit to leave a transitional state after its
// start (B-186) and answers its ActiveState and Result. An unknown state (no answer from systemctl)
// is answered as it is, empty: the caller does not claim a failure it did not see.
func (s SystemdServices) unitSettled(ctx context.Context, id string) (state, result string) {
	unit := "addon-" + id + ".service"
	wait := s.ActiveWait
	if wait == 0 {
		wait = 15 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		st := s.showAll(ctx, []string{unit}, "ActiveState", "Result")[unit]
		state, result = st["ActiveState"], st["Result"]
		switch state {
		case "activating", "reloading", "deactivating":
			if time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return state, result
				case <-time.After(250 * time.Millisecond):
				}
				continue
			}
		}
		return state, result
	}
}

// startAddonUnit is the second half of resettleAddon.
func (s SystemdServices) startAddonUnit(ctx context.Context, id string) error {
	unit := "addon-" + id + ".service"
	if out, err := s.run(ctx, "start", "--no-pager", "--", unit); err != nil {
		return fmt.Errorf("systemctl start %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// stopWait is StopWait with its default.
func (s SystemdServices) stopWait() time.Duration {
	if s.StopWait == 0 {
		return 5 * time.Second
	}
	return s.StopWait
}

// waitCgroupEmpty waits up to StopWait for a cgroup to hold no process, KILLs by pid what is still
// in it, and waits once more; it answers the pids that are still there then (a process stuck in
// the kernel). A cgroup that is gone is empty.
func (s SystemdServices) waitCgroupEmpty(ctx context.Context, cg string) []int {
	wait := func() []int {
		deadline := time.Now().Add(s.stopWait())
		for {
			pids := cgroupProcs(s.Root, cg)
			if len(pids) == 0 || !time.Now().Before(deadline) || ctx.Err() != nil {
				return pids
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	rest := wait()
	if len(rest) == 0 {
		return nil
	}
	args := []string{"-KILL"}
	for _, pid := range rest {
		if pid > 1 && pid != os.Getpid() {
			args = append(args, strconv.Itoa(pid))
		}
	}
	if len(args) > 1 {
		_, _ = s.run2(ctx, "kill", args...)
	}
	return wait()
}

// stopByPID sends TERM to the processes, waits up to StopWait for them to go, and KILLs the
// rest. kill runs through the helper (it is on its program list); the pids are checked against
// /proc afterwards, not trusted to have ended.
func (s SystemdServices) stopByPID(ctx context.Context, procs []proc) []int {
	args := []string{"-TERM"}
	var pids []int
	for _, p := range procs {
		if p.PID <= 1 || p.PID == os.Getpid() {
			continue
		}
		args = append(args, strconv.Itoa(p.PID))
		pids = append(pids, p.PID)
	}
	if len(pids) == 0 {
		return nil
	}
	_, _ = s.run2(ctx, "kill", args...)
	wait := s.StopWait
	if wait == 0 {
		wait = 5 * time.Second
	}
	deadline := time.Now().Add(wait)
	alive := func() []int {
		var a []int
		for _, pid := range pids {
			if procAlive(s.Root, pid) {
				a = append(a, pid)
			}
		}
		return a
	}
	for len(alive()) > 0 && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(200 * time.Millisecond)
	}
	if rest := alive(); len(rest) > 0 {
		args = []string{"-KILL"}
		for _, pid := range rest {
			args = append(args, strconv.Itoa(pid))
		}
		_, _ = s.run2(ctx, "kill", args...)
	}
	return pids
}

// addonStray returns the addon's processes outside its unit when the unit has nothing of its own
// running - the Services page's "outside its unit" - and nil otherwise. A unit with tasks of its
// own runs its daemon where it belongs; a process of the addon elsewhere is then something else
// (a CGI, a cron job) and not a reason to stop anything. The /proc walk comes first, so the
// ordinary restart of an addon with nothing outside costs no `systemctl show`.
func (s SystemdServices) addonStray(ctx context.Context, id string) []proc {
	left := s.addonLeftoversDeep(id, "") // B-59: a root daemon with a bare title counts too
	if len(left) == 0 {
		return nil
	}
	unit := "addon-" + id + ".service"
	props := s.showAll(ctx, []string{unit}, "ActiveState", "TasksCurrent")[unit]
	if props == nil {
		return nil // systemd did not answer: nothing is stopped on a guess
	}
	switch props["ActiveState"] {
	case "active", "activating", "reloading":
		// "[not set]" (no task accounting) counts as empty, as on the Services page
		if tasks, err := strconv.Atoi(props["TasksCurrent"]); err == nil && tasks > 0 {
			return nil
		}
	}
	return left
}

// unitFileEnabled is what Enabled means for a UnitFileState: off for what is disabled or masked
// (masked-runtime is the page's Disable), on for everything else - enabled, enabled-runtime,
// static, generated, alias, indirect - and for a state systemd did not report.
func unitFileEnabled(state string) bool {
	switch state {
	case "disabled", "masked", "masked-runtime":
		return false
	}
	return true
}

// conditionSkipped reports whether an inactive unit's last start was skipped rather than run:
// ExecCondition= said no (Result=exec-condition), or a Condition*= was not met. ConditionResult is
// "no" as well for a unit whose conditions were never checked - one that was never started, such
// as emergency.service - so it counts only with a check on record (ConditionTimestampMonotonic).
// A failed unit stays failed even when a later start was skipped.
func conditionSkipped(active string, props map[string]string) bool {
	if active != "inactive" {
		return false
	}
	if props["Result"] == "exec-condition" {
		return true
	}
	checked, err := strconv.ParseUint(props["ConditionTimestampMonotonic"], 10, 64)
	return props["ConditionResult"] == "no" && err == nil && checked > 0
}

// Timer is one systemd timer (task 20: recurring actions are timers, visible in the UI).
type Timer struct {
	Unit      string `json:"unit"`
	Activates string `json:"activates"`
	Next      string `json:"next,omitempty"`
	Last      string `json:"last,omitempty"`
	Left      string `json:"left,omitempty"`
	Active    bool   `json:"active"`
	// Own: one of the timers made on the Services page, local-<name>.timer (task 50), whose two
	// files are edited through /timers/own and which can be deleted; a shipped timer gets an
	// override instead.
	Own bool `json:"own"`
	// Enabled and UnitFileState as on a service (task 49): the verdict and the raw state.
	Enabled       bool   `json:"enabled"`
	UnitFileState string `json:"unit_file_state,omitempty"`
}

// Timers lists the timers: everything `systemctl list-timers --all` knows, plus an own timer that
// is stored but not loaded (a switched-off one systemd dropped from memory), and the enabled state
// of all of them from one `systemctl show`.
func (s SystemdServices) Timers(ctx context.Context) ([]Timer, error) {
	out, err := s.run(ctx, "list-timers", "--all", "--no-pager", "--output=json")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-timers: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var rows []struct {
		Unit      string `json:"unit"`
		Activates string `json:"activates"`
		Next      any    `json:"next"`
		Left      any    `json:"left"`
		Last      any    `json:"last"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("systemctl list-timers: %w", err)
	}
	own := map[string]bool{}
	for _, name := range s.localTimerNames() {
		own[localTimerUnit(name)] = true
	}
	var list []Timer
	seen := map[string]bool{}
	for _, r := range rows {
		t := Timer{Unit: r.Unit, Activates: r.Activates, Next: usecToTime(r.Next), Last: usecToTime(r.Last), Active: r.Next != nil, Own: own[r.Unit]}
		seen[r.Unit] = true
		// systemd 257 fills "left" with the absolute "next" timestamp (seen on Debian 13), older
		// versions with the remaining microseconds: compute it from "next" and the clock instead
		if n, ok := r.Next.(float64); ok && n > 0 {
			if left := time.UnixMicro(int64(n)).Sub(s.now()); left > 0 {
				t.Left = left.Truncate(time.Second).String()
			}
		}
		list = append(list, t)
	}
	// systemctl's order (by next run) is kept; a stored own timer it does not list - switched off,
	// and garbage-collected by systemd since - comes after, by name
	var unloaded []string
	for unit := range own {
		if !seen[unit] {
			unloaded = append(unloaded, unit)
		}
	}
	sort.Strings(unloaded)
	for _, unit := range unloaded {
		list = append(list, Timer{Unit: unit, Activates: strings.TrimSuffix(unit, ".timer") + ".service", Own: true})
	}
	// the enabled state of every timer in one `systemctl show` (B-60's rule: never one per unit)
	units := make([]string, len(list))
	for i := range list {
		units[i] = list[i].Unit
	}
	var shown map[string]map[string]string
	if s.Cache != nil {
		shown = s.Cache.timerStates(ctx, s, units)
	} else {
		shown = s.showAll(ctx, units, "UnitFileState")
	}
	for i := range list {
		st := shown[list[i].Unit]["UnitFileState"]
		list[i].UnitFileState, list[i].Enabled = st, unitFileEnabled(st)
		// an own timer systemd said nothing about is not enabled: nothing links it anywhere
		if list[i].Own && st == "" {
			list[i].Enabled = false
		}
	}
	if list == nil {
		list = []Timer{}
	}
	return list, nil
}

func usecToTime(v any) string {
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return ""
	}
	return time.UnixMicro(int64(f)).Format(time.RFC3339)
}

// ---- journald --------------------------------------------------------------------------------

// JournalLog reads the journal through journalctl -o json.
type JournalLog struct {
	Run Runner // nil = exec
	// Stream starts journalctl for the download, whose output is read while it is written; nil = exec
	Stream StreamRunner
	// Root places a kernel line's own stamp on the wall clock (B-116, kernelClock); "" leaves the
	// kernel's lines at the time journald read them
	Root Root
	// Now is that wall clock; nil = time.Now
	Now func() time.Time
}

func (j JournalLog) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

var journalPriority = map[string]string{"debug": "7", "info": "6", "notice": "5", "warning": "4", "warn": "4", "err": "3", "error": "3", "crit": "2", "alert": "1", "emerg": "0"}
var priorityName = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// sourceArgs names the journal files to read when the journal's copies are on a USB stick (task
// 216): journalctl reads /run/log/journal and /var/log/journal by itself, and nothing of the stick
// is mounted there - so the RAM journal and the copies on the stick are passed as files, which
// journalctl merges by time (its --directory takes one directory). The globs are expanded here:
// journalctl refuses a --file pattern that matches nothing. nil - the default places - without a
// stick, or when the stick holds no copy yet. Not for following: that reads what journald writes
// now, the RAM journal, and a stick's files would only hold the stick while the view is open.
func (j JournalLog) sourceArgs() []string {
	if j.Root == "" {
		return nil
	}
	dir := j.Root.JournalStickDir()
	if dir == "" {
		return nil
	}
	stick, _ := filepath.Glob(j.Root.join(dir + "/*/*.journal"))
	if len(stick) == 0 {
		return nil
	}
	ram, _ := filepath.Glob(j.Root.join("/run/log/journal/*/*.journal"))
	root := filepath.Clean(string(j.Root))
	var out []string
	for _, f := range append(ram, stick...) {
		if root != "/" {
			f = "/" + strings.TrimPrefix(strings.TrimPrefix(f, root), "/")
		}
		out = append(out, "--file="+f)
	}
	return out
}

// JournalFileArgs is sourceArgs for the other readers of this boot's journal (an addon's last lines,
// the ssh logins, the kernel's errors): with the copies on a USB stick, what this boot logged
// before the last copy is only there.
func JournalFileArgs(r Root) []string { return JournalLog{Root: r}.sourceArgs() }

// namesFiles: the arguments name journal files (sourceArgs).
func namesFiles(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--file=") {
			return true
		}
	}
	return false
}

func (j JournalLog) args(q LogQuery) []string {
	args := []string{"-o", "json", "--no-pager", "-q"}
	if !q.Follow {
		args = append(args, j.sourceArgs()...)
	}
	// -n 0 matters when following: without it journalctl -f replays its default ten lines - and
	// Follow's default of 50 replayed the tail the page had just loaded (28.3, the Log filter
	// "not working": every filter change showed the last lines twice)
	if q.Head && !q.Follow && q.Limit > 0 {
		// task 178: the oldest N of the boot or the range, from the start
		args = append(args, "-n", "+"+strconv.Itoa(q.Limit))
	} else if q.Limit > 0 || (q.Follow && q.Limit == 0) {
		args = append(args, "-n", strconv.Itoa(q.Limit))
	}
	// task 178: the page before a cursor is read backwards from it (Read turns it round again);
	// the page after one forwards, which following takes too - the entries since the cursor are
	// replayed, so a stream that starts after a page has no gap to it
	if q.Before != "" && !q.Follow {
		args = append(args, "-r", "--after-cursor="+q.Before)
	} else if q.After != "" {
		args = append(args, "--after-cursor="+q.After)
	}
	// journalctl ANDs matches on different fields, so a unit and a tag narrow each other (task
	// 31: the Log page used to drop the tag once a unit was chosen)
	if q.Unit != "" {
		u := q.Unit
		if !strings.Contains(u, ".") {
			u += ".service"
		}
		args = append(args, "-u", u)
	}
	if q.Tag != "" {
		args = append(args, "-t", q.Tag)
	}
	if p, ok := journalPriority[q.Severity]; ok {
		args = append(args, "-p", p) // journalctl -p N means "N and more important"
	}
	if q.Since != "" {
		args = append(args, "--since", q.Since)
	}
	if q.Until != "" {
		args = append(args, "--until", q.Until)
	}
	if q.Contains != "" {
		args = append(args, "-g", q.Contains, "--case-sensitive=false")
	}
	// task 93: -k alone is this boot's kernel log; --boot= in its one-word form, because -b takes
	// its value only optionally and a separate "-1" would read as an option of its own
	if q.Kernel {
		args = append(args, "-k")
	}
	if q.Boot != "" {
		args = append(args, "--boot="+q.Boot)
	}
	if q.Follow {
		args = append(args, "-f")
	}
	// task 102: one run's lines, a field match - ANDed with the transports below, which are one
	// field of their own
	if q.Run != "" {
		args = append(args, "OCCULITE_RUN_ID="+q.Run)
	}
	// task 186: one of occulited's areas, a field match of the same kind
	if q.Area != "" {
		args = append(args, "OCCULITED_AREA="+q.Area)
	}
	// the System source: journalctl has no negative match, but matches on one field are ORed and
	// ANDed with the rest - the other transports are everything but the kernel's (measured on the
	// x86_64 VM: 775 + 654 kernel lines = the boot's 1429, and the same 7 lines of -u rfd with them)
	if q.NoKernel && !q.Kernel {
		args = append(args, systemTransports...)
	}
	return args
}

var systemTransports = []string{"_TRANSPORT=journal", "_TRANSPORT=stdout", "_TRANSPORT=syslog", "_TRANSPORT=driver", "_TRANSPORT=audit"}

// Read returns the newest matching entries, oldest first.
func (j JournalLog) Read(q LogQuery) ([]LogLine, error) {
	if q.Limit <= 0 {
		q.Limit = 500
	}
	q.Follow = false
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out []byte
	var err error
	// with the copies on a USB stick the files are named one by one, and a copy that removes one
	// from RAM between the listing and journalctl's start fails the read: once more, listed again
	for try := 0; try < 2; try++ {
		args := j.args(q)
		if j.Run != nil {
			out, err = j.Run(ctx, "journalctl", args...)
		} else {
			out, err = exec.CommandContext(ctx, "journalctl", args...).Output()
		}
		if err == nil || noMatchExit(q, out, err) || !namesFiles(args) {
			break
		}
	}
	if err != nil && !noMatchExit(q, out, err) {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	lines := []LogLine{}
	place := j.kernelClock(ctx)
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l, ok := parseJournalLine(sc.Bytes(), place); ok {
			lines = append(lines, l)
		}
	}
	if q.Before != "" {
		slices.Reverse(lines) // read newest first from the cursor; the answer is oldest first
	}
	return lines, nil
}

// noMatchExit: journalctl's own text filter (-g, LogQuery.Contains) exits 1 when its expression
// matches nothing, unlike every other filter it takes, which simply print nothing and exit 0. A
// read that finds nothing is an empty answer, not a failure - the Log page's text filter showed an
// error for a word that is not in the log, and task 201's decline is a grep that finds nothing
// almost always. Only an exit status of 1 with nothing written counts as that; a signal, a status
// above 1 and anything that wrote output stay errors.
func noMatchExit(q LogQuery, out []byte, err error) bool {
	if q.Contains == "" || len(out) > 0 {
		return false
	}
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 1
}

// bootStartsMax bounds the earlier boots one read looks up: a query over every boot of a kept
// journal can hold kernel lines of many.
const bootStartsMax = 16

// kernelClock places a kernel line's own stamp on the wall clock (B-116): the start of the boot
// the line belongs to plus the stamp, as Dmesg does for the ring buffer. The time journald read the
// line is no substitute - it reads the kernel's first messages seconds after they were printed, and
// on a box without a real-time clock that moment still carries the image's date (the Pi 4's
// kernel lines said 13 March). This boot began an uptime ago. An earlier boot began at its last
// entry's time less that entry's monotonic stamp: two stamps journald took at once, as late in that
// boot as there are, when its clock is most likely set. nil without a Root.
func (j JournalLog) kernelClock(ctx context.Context) func(bootID string) (time.Time, bool) {
	if j.Root == "" {
		return nil
	}
	this := j.Root.BootID()
	type start struct {
		t  time.Time
		ok bool
	}
	known := map[string]start{}
	return func(id string) (time.Time, bool) {
		id = NormalizeBoot(id)
		if s, ok := known[id]; ok {
			return s.t, s.ok
		}
		var s start
		switch {
		case !bootIDRe.MatchString(id):
		case id == this:
			if up, ok := j.Root.Uptime(); ok {
				s = start{j.now().Add(-up), true}
			}
		case len(known) < bootStartsMax:
			s.t, s.ok = j.lastEntryStart(ctx, id)
		}
		known[id] = s
		return s.t, s.ok
	}
}

// lastEntryStart is when a boot began by its last entry in the journal: __REALTIME_TIMESTAMP less
// __MONOTONIC_TIMESTAMP. The boot is named by its id, which journalctl finds whatever the entries'
// dates say (B-114).
func (j JournalLog) lastEntryStart(ctx context.Context, id string) (time.Time, bool) {
	args := append([]string{"-o", "json", "--no-pager", "-q", "-n", "1", "--boot=" + id}, j.sourceArgs()...)
	var out []byte
	var err error
	if j.Run != nil {
		out, err = j.Run(ctx, "journalctl", args...)
	} else {
		out, err = exec.CommandContext(ctx, "journalctl", args...).Output()
	}
	if err != nil {
		return time.Time{}, false
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	var e struct {
		Realtime  string `json:"__REALTIME_TIMESTAMP"`
		Monotonic string `json:"__MONOTONIC_TIMESTAMP"`
	}
	if json.Unmarshal([]byte(line), &e) != nil {
		return time.Time{}, false
	}
	rt, err1 := strconv.ParseInt(e.Realtime, 10, 64)
	mono, err2 := strconv.ParseInt(e.Monotonic, 10, 64)
	if err1 != nil || err2 != nil || rt <= 0 || mono < 0 || mono >= rt {
		return time.Time{}, false
	}
	return time.UnixMicro(rt - mono), true
}

// BootStart is when a boot began by its last entry in the journal (lastEntryStart): the start of
// an earlier boot whose first entries carry the clock before it was set (B-114).
func (j JournalLog) BootStart(ctx context.Context, bootID string) (time.Time, bool) {
	if !bootIDRe.MatchString(bootID) {
		return time.Time{}, false
	}
	return j.lastEntryStart(ctx, bootID)
}

// ParseJournalLine turns one journalctl -o json entry into a LogLine.
func ParseJournalLine(b []byte) (LogLine, bool) { return parseJournalLine(b, nil) }

// parseJournalLine is ParseJournalLine that places a kernel line on the wall clock by the start of
// its boot (kernelClock); a nil bootStart, or a boot it does not know, keeps journald's time.
func parseJournalLine(b []byte, bootStart func(bootID string) (time.Time, bool)) (LogLine, bool) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return LogLine{}, false
	}
	str := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return v
		case []any: // a binary/oversized field arrives as an array
			return ""
		}
		return ""
	}
	l := LogLine{Message: str("MESSAGE"), Host: str("_HOSTNAME"), Unit: strings.TrimSuffix(str("_SYSTEMD_UNIT"), ".service")}
	if ts, err := strconv.ParseInt(str("__REALTIME_TIMESTAMP"), 10, 64); err == nil {
		l.Time = time.UnixMicro(ts).Format("Jan _2 15:04:05")
		l.Timestamp = time.UnixMicro(ts).Format(time.RFC3339Nano)
	}
	l.Area = str("OCCULITED_AREA")
	l.Tag = str("SYSLOG_IDENTIFIER")
	if l.Tag == "" {
		l.Tag = str("_COMM")
	}
	if p, err := strconv.Atoi(str("PRIORITY")); err == nil && p >= 0 && p < len(priorityName) {
		l.Severity = priorityName[p]
	}
	if pid, err := strconv.Atoi(str("_PID")); err == nil {
		l.PID = pid
	}
	if f := str("SYSLOG_FACILITY"); f != "" {
		l.Facility = f
	}
	// the kernel's own stamp where the entry has one: journald reads the early kernel messages
	// seconds after they were printed, and dmesg shows the time they were printed. A stamp of 0 is
	// one - the kernel prints its first messages at [0.000000], and they had journald's 8.8 s on the
	// Pi 4 (B-116) - so the field's presence decides, not its value
	src, err := strconv.ParseInt(str("_SOURCE_MONOTONIC_TIMESTAMP"), 10, 64)
	hasSource := err == nil && src >= 0
	if hasSource {
		l.MonotonicUS = src
	} else if us, err := strconv.ParseInt(str("__MONOTONIC_TIMESTAMP"), 10, 64); err == nil && us > 0 {
		l.MonotonicUS = us
	}
	if hasSource && bootStart != nil && str("_TRANSPORT") == "kernel" {
		if start, ok := bootStart(str("_BOOT_ID")); ok {
			t := start.Add(time.Duration(src) * time.Microsecond)
			l.Time = t.Format("Jan _2 15:04:05")
			l.Timestamp = t.Format(time.RFC3339Nano)
		}
	}
	l.Cursor = str("__CURSOR")
	return l, true
}

// Follow streams entries as they arrive until ctx ends (the Log page's live view).
func (j JournalLog) Follow(ctx context.Context, q LogQuery, emit func(LogLine)) error {
	q.Follow = true
	if q.Limit < 0 { // unset; 0 is "no history", which the page asks for after its own load
		q.Limit = 50
	}
	cmd := exec.CommandContext(ctx, "journalctl", j.args(q)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	place := j.kernelClock(ctx)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l, ok := parseJournalLine(sc.Bytes(), place); ok {
			emit(l)
		}
	}
	return cmd.Wait()
}

func (s SystemdServices) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
