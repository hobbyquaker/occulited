package system

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// B-26: what the Services page's switch means on a read-only rootfs.
//
// Unit enablement lives in /etc/systemd/system/<target>.wants, and / is mounted ro on every CCU
// product by design, so `systemctl disable` answered "Read-only file system" and `enable` only
// ever succeeded when it had nothing to do. `systemctl --runtime disable` is not the way around
// it - it writes to /run, and an /etc enablement is not overridden by it; the box's is-enabled
// still said `enabled`. What does override everything is `systemctl --runtime mask`, verified on
// the box: it links /run/systemd/system/<unit> to /dev/null, is-enabled then says
// `masked-runtime`, and `--runtime unmask` restores the shipped state exactly.
//
// So the switch is a runtime mask, and occulited keeps the set on the userfs and replays it at
// every start (the maintainer's decision of 2026-09-07, option 1 of roadmap-archive/B-26.md).
// Nothing on the rootfs is written, and a firmware update still brings its new units enabled by
// default, because the shipped state is never edited - only shadowed.
//
// B-131: that mask does not hold an addon's unit. The generator writes addon-<id>.service into
// /run/systemd/generator.early, which outranks /run/systemd/system, so systemctl start ran it through
// the mask. The fork's generator reads this file and links the unit of a switched-off addon to
// /dev/null in generator.early itself, at every boot and daemon-reload; so the switch reloads systemd
// after writing the file. The runtime mask stays for every other unit.

// unitSwitch is the persisted set: units switched off, and the ones switched on that the image
// does not ship enabled (a runtime mask hides an enablement, but nothing conjures one, so those
// need a runtime enable of their own to survive a boot).
type unitSwitch struct {
	Masked  []string `json:"masked"`
	Enabled []string `json:"enabled,omitempty"`
}

// readSwitch reads the set; anything unreadable or malformed is an empty set, because a missing
// file is the normal state of a box where nobody has touched a switch.
func (s SystemdServices) readSwitch() unitSwitch {
	var sw unitSwitch
	if s.SwitchFile == "" {
		return sw
	}
	b, err := os.ReadFile(s.SwitchFile)
	if err != nil {
		return unitSwitch{}
	}
	if err := json.Unmarshal(b, &sw); err != nil {
		return unitSwitch{}
	}
	return sw
}

// writeSwitch stores the set on the userfs (occulited's own state directory: it is the daemon's
// to write, no privilege needed, and it survives a firmware update with the rest of /usr/local).
func (s SystemdServices) writeSwitch(sw unitSwitch) error {
	if s.SwitchFile == "" {
		return nil
	}
	sort.Strings(sw.Masked)
	sort.Strings(sw.Enabled)
	b, err := json.MarshalIndent(sw, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(s.SwitchFile)
	tmp, err := os.CreateTemp(dir, ".unit-switch.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.SwitchFile)
}

func withUnit(list []string, unit string) []string {
	for _, u := range list {
		if u == unit {
			return list
		}
	}
	return append(list, unit)
}

func withoutUnit(list []string, unit string) []string {
	out := list[:0:0]
	for _, u := range list {
		if u != unit {
			out = append(out, u)
		}
	}
	return out
}

// switchUnit is `enable` and `disable` as this box can implement them.
//
// Off is `systemctl --runtime mask --now`: it overrides the shipped enablement whatever it is,
// and stops the unit. On is `--runtime unmask` and a start - plus a `--runtime enable` when the
// unit is one the image does not ship enabled, because unmasking a unit that was never enabled
// starts it now and not at the next boot. Either way the set is written to the userfs first, so
// a crash between the systemctl call and the file leaves the box describing what it did.
func (s SystemdServices) switchUnit(ctx context.Context, unit string, on bool) (string, error) {
	sw := s.readSwitch()
	if !on {
		out, err := s.run(ctx, "mask", "--runtime", "--now", "--no-pager", "--", unit)
		if err != nil {
			return strings.TrimSpace(string(out)), fmt.Errorf("systemctl mask --runtime %s: %w", unit, err)
		}
		sw.Enabled, sw.Masked = withoutUnit(sw.Enabled, unit), withUnit(sw.Masked, unit)
		if err := s.writeSwitch(sw); err != nil {
			return strings.TrimSpace(string(out)), fmt.Errorf("the switched-off units could not be stored: %w", err)
		}
		if err := s.reloadForSwitch(ctx, unit); err != nil {
			return strings.TrimSpace(string(out)), err
		}
		return strings.TrimSpace(string(out)), nil
	}
	out, err := s.run(ctx, "unmask", "--runtime", "--no-pager", "--", unit)
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("systemctl unmask --runtime %s: %w", unit, err)
	}
	sw.Masked = withoutUnit(sw.Masked, unit)
	// B-131: the generator's link goes first, or is-enabled and the start below still find a masked unit
	if isAddonUnit(unit) {
		if err := s.writeSwitch(sw); err != nil {
			return "", fmt.Errorf("the switched-on units could not be stored: %w", err)
		}
		if err := s.reloadForSwitch(ctx, unit); err != nil {
			return "", err
		}
	}
	// is-enabled exits non-zero for a disabled unit, which is an answer and not a failure
	state, _ := s.run(ctx, "is-enabled", "--", unit)
	if strings.TrimSpace(string(state)) == "disabled" {
		if o, err := s.run(ctx, "enable", "--runtime", "--no-pager", "--", unit); err != nil {
			return strings.TrimSpace(string(o)), fmt.Errorf("systemctl enable --runtime %s: %w", unit, err)
		}
		sw.Enabled = withUnit(sw.Enabled, unit)
	} else {
		sw.Enabled = withoutUnit(sw.Enabled, unit)
	}
	if err := s.writeSwitch(sw); err != nil {
		return "", fmt.Errorf("the switched-on units could not be stored: %w", err)
	}
	o, err := s.run(ctx, "start", "--no-pager", "--", unit)
	if err != nil {
		return strings.TrimSpace(string(o)), fmt.Errorf("systemctl start %s: %w", unit, err)
	}
	return strings.TrimSpace(string(o)), nil
}

func isAddonUnit(unit string) bool {
	return strings.HasPrefix(unit, "addon-") && strings.HasSuffix(unit, ".service")
}

// reloadForSwitch runs the generator again after the file changed, for an addon's unit (B-131).
func (s SystemdServices) reloadForSwitch(ctx context.Context, unit string) error {
	if !isAddonUnit(unit) {
		return nil
	}
	// occulited task 136: a switched-off addon's bin directory leaves the PATH, and comes back with it
	s.SyncAddonProfiles()
	if out, err := s.run(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// switchedOff says whether a unit is switched off on the Services page.
func (s SystemdServices) switchedOff(unit string) bool {
	return slices.Contains(s.readSwitch().Masked, unit)
}

// forgetSwitch drops what is stored for an addon that is new on this system: an entry left by one of
// that name that was uninstalled must not keep a fresh install from starting (B-131). The caller
// reloads systemd.
func (s SystemdServices) forgetSwitch(ctx context.Context, unit string) error {
	sw := s.readSwitch()
	if !slices.Contains(sw.Masked, unit) && !slices.Contains(sw.Enabled, unit) {
		return nil
	}
	_, _ = s.run(ctx, "unmask", "--runtime", "--no-pager", "--", unit)
	sw.Masked, sw.Enabled = withoutUnit(sw.Masked, unit), withoutUnit(sw.Enabled, unit)
	return s.writeSwitch(sw)
}

// ReplayUnitSwitch re-applies the stored switch, which is what makes it persistent: /run is empty
// after a boot, so every runtime mask has to be made again. It returns what it applied and what
// it could not, for the start-up log; a unit that no longer exists (an addon removed, a firmware
// update that dropped it) is a message and not a failure.
//
// occulited's own unit is never masked here. A user who switches it off stops the daemon at once,
// as `stop` always did; replaying that at the next boot would stop the box's only administration
// interface every time it came up, and the mask itself is gone with /run - so the box comes back
// with occulited running, which is the state a person can act on.
func (s SystemdServices) ReplayUnitSwitch(ctx context.Context) (applied []string, problems []string) {
	sw := s.readSwitch()
	for _, unit := range sw.Masked {
		if !plausibleUnit(unit) {
			problems = append(problems, unit+": not a unit name")
			continue
		}
		if strings.TrimSuffix(unit, ".service") == "occulited" {
			problems = append(problems, "occulited: not masked at start - the daemon would stop itself")
			continue
		}
		if out, err := s.run(ctx, "mask", "--runtime", "--now", "--no-pager", "--", unit); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v %s", unit, err, strings.TrimSpace(string(out))))
			continue
		}
		applied = append(applied, "masked "+unit)
	}
	for _, unit := range sw.Enabled {
		if !plausibleUnit(unit) {
			problems = append(problems, unit+": not a unit name")
			continue
		}
		if out, err := s.run(ctx, "enable", "--runtime", "--now", "--no-pager", "--", unit); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v %s", unit, err, strings.TrimSpace(string(out))))
			continue
		}
		applied = append(applied, "enabled "+unit)
	}
	return applied, problems
}

// plausibleUnit is the same rule Control applies to what a request names: the stored file is
// occulited's own, but it is read at start and turned into systemctl arguments, so it is checked
// like anything else that comes from outside the process.
func plausibleUnit(unit string) bool {
	if unit == "" || len(unit) > 128 || strings.HasPrefix(unit, "-") {
		return false
	}
	return !strings.ContainsAny(unit, "/ \t\n\r\x00")
}
