package priv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- the firewall's load, confirm and revert (task 157, D-105) --------------------------------
//
// occulited renders the rules; the helper loads them with iptables-restore, family by family, and
// owns the confirm window: a load with a window keeps the filter table as it was and a timer, and
// reverts to it by itself unless Confirm comes first - so a page that locked itself out, or an
// occulited that died, still gets the box back.
//
// The texts are occulited's, so the helper checks them: the filter table only, the INPUT chain and
// the lite-input chain only, a fixed set of targets, and nothing that deletes or renames a chain.
// An addon's own chains are never touched (the load is --noflush).

// FirewallTools are each family's restore and save programs (where the iptables package puts them
// on the images). A variable so a test can point them at scripts.
var FirewallTools = map[string][2]string{
	"ipv4": {"/usr/bin/iptables-restore", "/usr/bin/iptables-save"},
	"ipv6": {"/usr/bin/ip6tables-restore", "/usr/bin/ip6tables-save"},
}

// firewallFamilies is the load's order.
var firewallFamilies = []string{"ipv4", "ipv6"}

// firewallLoadTimeout bounds one restore or save, the xtables lock wait included.
const firewallLoadTimeout = 30 * time.Second

// MaxFirewallWindow is the longest confirm window a load may ask for.
const MaxFirewallWindow = 10 * time.Minute

var (
	fwChainLine = regexp.MustCompile(`^:(INPUT (ACCEPT|DROP)|lite-input -) \[0:0\]$`)
	fwRuleLine  = regexp.MustCompile(`^-A (INPUT|lite-input) (.+)$`)
	// REJECT --reject-with tcp-reset: the IPv6 chain's tail (openccu-lite B-339)
	fwTarget = regexp.MustCompile(` -j (ACCEPT|DROP|REJECT( --reject-with tcp-reset)?|LOG|lite-input)( --log-prefix "fw [0-9a-z]{1,32} ")?$`)
)

// ValidFirewallText checks one family's restore text: the filter table, INPUT's policy and the
// lite-input chain declared, both flushed, rules appended to those two only, with the targets the
// rendering uses. No quote but the LOG prefix's, no table switch, no -X, -E, -P, -D or -I.
func ValidFirewallText(text []byte) error {
	lines := strings.Split(strings.TrimRight(string(text), "\n"), "\n")
	if len(lines) < 6 || lines[0] != "*filter" || lines[len(lines)-1] != "COMMIT" {
		return errors.New("not a filter table text")
	}
	declared := map[string]bool{}
	for _, l := range lines[1 : len(lines)-1] {
		switch {
		case fwChainLine.MatchString(l):
			declared[strings.Fields(l)[0]] = true
		case l == "-F INPUT" || l == "-F lite-input":
		case fwRuleLine.MatchString(l):
			if !fwTarget.MatchString(l) {
				return fmt.Errorf("a rule with another target: %q", l)
			}
			rest := fwTarget.ReplaceAllString(l, "")
			if strings.ContainsAny(rest, "\"';`$\\") {
				return fmt.Errorf("a rule with a quote or a shell character: %q", l)
			}
		default:
			return fmt.Errorf("not a line of the firewall's: %q", l)
		}
	}
	if !declared[":INPUT"] || !declared[":lite-input"] {
		return errors.New("INPUT and lite-input must be declared")
	}
	return nil
}

// fwState is the helper's confirm window: the filter tables as they were before the load, and the
// timer that puts them back.
var fwState struct {
	mu    sync.Mutex
	saved map[string][]byte
	timer *time.Timer
	gen   int
	// logf is the helper's log, for the revert the timer makes on its own
	logf func(format string, a ...any)
}

func firewallTool(family string, save bool) (string, error) {
	t, ok := FirewallTools[family]
	if !ok {
		return "", fmt.Errorf("not a firewall family: %q", family)
	}
	prog := t[0]
	if save {
		prog = t[1]
	}
	if _, err := os.Stat(prog); os.IsNotExist(err) {
		return "", fmt.Errorf("%w: %s is not installed", ErrNotAvailable, prog)
	}
	return prog, nil
}

func (l Local) fwRun(ctx context.Context, family string, save bool, args []string, stdin []byte) ([]byte, error) {
	prog, err := firewallTool(family, save)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, firewallLoadTimeout)
	defer cancel()
	r, err := l.Run(ctx, prog, args, stdin)
	if err != nil {
		return nil, err
	}
	if r.Exit != 0 {
		return nil, fmt.Errorf("%s %s exited %d: %s", filepath.Base(prog), strings.Join(args, " "), r.Exit, strings.TrimSpace(string(r.Stderr)))
	}
	return r.Stdout, nil
}

// fwSave is one family's filter table as it is.
func (l Local) fwSave(ctx context.Context, family string) ([]byte, error) {
	return l.fwRun(ctx, family, true, []string{"-t", "filter"}, nil)
}

// FirewallCounters is each family's filter table with the counters (iptables-save -c), for the
// rules' hit counts (task 167). Read only.
func (l Local) FirewallCounters(ctx context.Context) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, f := range firewallFamilies {
		b, err := l.fwRun(ctx, f, true, []string{"-c", "-t", "filter"}, nil)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out[f] = b
	}
	return out, nil
}

// fwRestoreSaved puts a saved filter table back whole.
func (l Local) fwRestoreSaved(ctx context.Context, family string, saved []byte) error {
	_, err := l.fwRun(ctx, family, false, []string{"-w", "5"}, saved)
	return err
}

// FirewallLoad loads the two texts (v4, then v6) after checking both and letting iptables-restore
// test them. If the second family fails the first is put back. With a window the tables as they
// were are kept and put back after it, unless FirewallConfirm comes first; without one any open
// window is closed (the new rules stand).
func (l Local) FirewallLoad(ctx context.Context, v4, v6 []byte, window time.Duration) error {
	if window < 0 || window > MaxFirewallWindow {
		return fmt.Errorf("a confirm window is between 0 and %s", MaxFirewallWindow)
	}
	texts := map[string][]byte{"ipv4": v4, "ipv6": v6}
	for _, f := range firewallFamilies {
		if err := ValidFirewallText(texts[f]); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if _, err := l.fwRun(ctx, f, false, []string{"-w", "5", "--noflush", "-t"}, texts[f]); err != nil {
			return fmt.Errorf("%s: the rules do not load: %w", f, err)
		}
	}
	fwState.mu.Lock()
	defer fwState.mu.Unlock()
	saved := fwState.saved
	if saved == nil {
		// the tables to go back to: those before the first load of an open window
		saved = map[string][]byte{}
		for _, f := range firewallFamilies {
			b, err := l.fwSave(ctx, f)
			if err != nil {
				return fmt.Errorf("%s: saving the rules before the load: %w", f, err)
			}
			saved[f] = b
		}
	}
	for i, f := range firewallFamilies {
		if _, err := l.fwRun(ctx, f, false, []string{"-w", "5", "--noflush"}, texts[f]); err != nil {
			for _, back := range firewallFamilies[:i] {
				_ = l.fwRestoreSaved(context.Background(), back, saved[back])
			}
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	if fwState.timer != nil {
		fwState.timer.Stop()
		fwState.timer = nil
	}
	fwState.gen++
	if window == 0 {
		fwState.saved = nil
		return nil
	}
	fwState.saved = saved
	gen := fwState.gen
	fwState.timer = time.AfterFunc(window, func() {
		fwState.mu.Lock()
		defer fwState.mu.Unlock()
		if fwState.gen != gen || fwState.saved == nil {
			return
		}
		var errs []string
		for _, f := range firewallFamilies {
			if err := l.fwRestoreSaved(context.Background(), f, fwState.saved[f]); err != nil {
				errs = append(errs, err.Error())
			}
		}
		fwState.saved, fwState.timer = nil, nil
		if fwState.logf != nil {
			if len(errs) > 0 {
				fwState.logf("helper: firewall not confirmed within %s; putting the rules before it back failed: %s", window, strings.Join(errs, "; "))
			} else {
				fwState.logf("helper: firewall not confirmed within %s, the rules before it are back", window)
			}
		}
	})
	return nil
}

// FirewallConfirm closes the open window: the loaded rules stand.
func (l Local) FirewallConfirm(context.Context) error {
	fwState.mu.Lock()
	defer fwState.mu.Unlock()
	if fwState.saved == nil {
		return errors.New("no firewall load is waiting for its confirmation")
	}
	if fwState.timer != nil {
		fwState.timer.Stop()
	}
	fwState.saved, fwState.timer = nil, nil
	fwState.gen++
	return nil
}

// FirewallRevert puts the tables of the open window back now.
func (l Local) FirewallRevert(ctx context.Context) error {
	fwState.mu.Lock()
	defer fwState.mu.Unlock()
	if fwState.saved == nil {
		return errors.New("no firewall load is waiting for its confirmation")
	}
	if fwState.timer != nil {
		fwState.timer.Stop()
	}
	var errs []error
	for _, f := range firewallFamilies {
		if err := l.fwRestoreSaved(ctx, f, fwState.saved[f]); err != nil {
			errs = append(errs, err)
		}
	}
	fwState.saved, fwState.timer = nil, nil
	fwState.gen++
	return errors.Join(errs...)
}

// SocketOwner is a process holding a socket: the socket's inode, the pid, the process's name and
// its systemd unit (from its cgroup), for the Firewall page's listeners.
type SocketOwner struct {
	Inode uint64 `json:"inode"`
	PID   int    `json:"pid"`
	Comm  string `json:"comm"`
	Unit  string `json:"unit,omitempty"`
}

// procDir is /proc, a variable for the tests.
var procDir = "/proc"

// SocketOwners walks /proc/<pid>/fd for socket links. Only what the page shows leaves the helper:
// the inode, pid, name and unit - never a command line or an environment.
func (Local) SocketOwners(context.Context) ([]SocketOwner, error) {
	pids, err := os.ReadDir(procDir)
	if err != nil {
		return nil, err
	}
	var out []SocketOwner
	for _, p := range pids {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		base := filepath.Join(procDir, p.Name())
		fds, err := os.ReadDir(filepath.Join(base, "fd"))
		if err != nil {
			continue
		}
		var comm, unit string
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(base, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]"), 10, 64)
			if err != nil {
				continue
			}
			if comm == "" {
				b, _ := os.ReadFile(filepath.Join(base, "comm"))
				comm = strings.TrimSpace(string(b))
				unit = unitOfCgroup(filepath.Join(base, "cgroup"))
			}
			out = append(out, SocketOwner{Inode: inode, PID: pid, Comm: comm, Unit: unit})
		}
	}
	return out, nil
}

// unitOfCgroup is the systemd unit a process's cgroup names: the last .service or .scope element
// of the unified hierarchy's path ("0::/system.slice/lighttpd.service").
func unitOfCgroup(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		f := strings.SplitN(string(line), ":", 3)
		if len(f) != 3 {
			continue
		}
		parts := strings.Split(f[2], "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.HasSuffix(parts[i], ".service") || strings.HasSuffix(parts[i], ".scope") {
				return parts[i]
			}
		}
	}
	return ""
}
