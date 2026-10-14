package firmware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

// Holding a firmware back (occulited task 45). hmipserver transfers every deployed bundle to the
// devices of its type by itself, in the background, as soon as it is in /etc/config/firmware -
// there is no OTAU setting a user could usefully touch (the research of 2026-10-14), but there is
// the deploy: what is not in the directory is not transferred. So a bundle is either *offered* -
// in the firmware directory, where the interfaces read it - or *held* - in the held directory
// beside it, which they do not read. Measured on the lab (2026-10-14, the HmIP-BBL): a bundle
// moved out of the firmware directory and a refreshDeployedDeviceFirmwareList stop a running
// transfer within a second (the handler aborts, the device's state falls back to UP_TO_DATE,
// AVAILABLE_FIRMWARE to 0.0.0); a subdirectory of the firmware directory is NOT a hiding place -
// hmipserver scans it and transferred on, from byte 0 - which is why the held directory is a
// sibling. Two switches use it: the per-type one ("Transfer this firmware": off moves the type's
// bundle into the held directory and marks the type, so the daily check keeps a newer download
// there too) and the global one ("Deploy automatically": off makes every new download land in the
// held directory until the type's switch offers it).

// HeldDirName is the held directory's name next to the firmware directory:
// /etc/config/firmware-held beside /etc/config/firmware (the same filesystem, so a bundle moves
// by a rename; under /etc/config, so it is in every backup).
const HeldDirName = "firmware-held"

// HeldDir is the held directory for a firmware directory.
func HeldDir(dir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(dir)), HeldDirName)
}

// heldDir is the service's held directory (HeldDirOverride for the tests).
func (s *Service) heldDir() string {
	if s.HeldDirOverride != "" {
		return s.HeldDirOverride
	}
	return HeldDir(s.Dir)
}

// Held lists the bundles in the held directory, each marked Held.
func Held(dir string) ([]Bundle, error) {
	out, err := Deployed(dir)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Held = true
	}
	return out, nil
}

// AutoDeploy reports the global switch: a downloaded bundle is offered to the interfaces at once
// (true, the default) or held until the type's switch offers it.
func (s *Service) AutoDeploy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoDeploy()
}

func (s *Service) autoDeploy() bool {
	return s.autoDeployOff == nil || !*s.autoDeployOff
}

// SetAutoDeploy sets the global switch and keeps it in the state file.
func (s *Service) SetAutoDeploy(on bool) {
	s.mu.Lock()
	off := !on
	s.autoDeployOff = &off
	s.save()
	s.mu.Unlock()
}

// heldTypes is the sorted list of the explicitly held type keys (caller holds mu).
func (s *Service) heldTypes() []string {
	var out []string
	for k, v := range s.held {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ErrNoBundle is SetTransfer's answer for a directory that holds no bundle, offered or held.
var ErrNoBundle = errors.New("no such bundle")

// SetTransfer offers a bundle to the interfaces (on) or holds it back (off): the bundle moves
// between the firmware directory and the held directory by its directory name, the type is
// marked held (or no longer), the interfaces re-read the directory, and the live states are read
// again. A bundle that is where it should be already changes only the mark.
func (s *Service) SetTransfer(bundleDir string, on bool) (*Bundle, error) {
	if !typeCodeRe.MatchString(bundleDir) {
		return nil, ErrNoBundle
	}
	live, held := filepath.Join(s.Dir, bundleDir), filepath.Join(s.heldDir(), bundleDir)
	var from, to string
	switch {
	case on:
		from, to = held, live
	default:
		from, to = live, held
	}
	b, where := bundleAt(to, bundleDir)
	if where == "" {
		b, where = bundleAt(from, bundleDir)
		if where == "" {
			return nil, ErrNoBundle
		}
		if err := Priv.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return nil, err
		}
		_ = Priv.RemoveAll(to)
		if err := Priv.Rename(from, to); err != nil {
			return nil, fmt.Errorf("move %s: %w", bundleDir, err)
		}
		_ = Priv.Chown(to, 0, 0, true)
		_ = Priv.Chmod(to, 0o775)
	}
	b.Held = !on
	s.mu.Lock()
	if s.held == nil {
		s.held = map[string]bool{}
	}
	if on {
		delete(s.held, TypeKey(b.Name))
	} else {
		s.held[TypeKey(b.Name)] = true
	}
	s.save()
	s.mu.Unlock()
	for name, err := range interfaces.RefreshFirmware(s.Interfaces()) {
		s.Log.Warn("firmware: refresh failed", "interface", name, "err", err)
	}
	s.Log.Info("firmware: transfer switched", "type", b.Name, "version", b.Version, "offered", on)
	s.Refresh()
	return b, nil
}

// bundleAt reads the bundle in a directory; where is "" when there is none.
func bundleAt(path, dirName string) (*Bundle, string) {
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return nil, ""
	}
	list, err := Deployed(filepath.Dir(path))
	if err != nil {
		return nil, ""
	}
	for i := range list {
		if list[i].Dir == dirName {
			return &list[i], path
		}
	}
	return nil, ""
}

// target says where a type's download goes (caller holds mu): the held directory for an
// explicitly held type and while the global switch is off, else the firmware directory.
func (s *Service) target(typeKey string) (dir string, heldBack bool) {
	if s.held[typeKey] || !s.autoDeploy() {
		return s.heldDir(), true
	}
	return s.Dir, false
}

// offerHeld moves a held bundle into the firmware directory (the global switch back on, the type
// not held): the check's counterpart of SetTransfer, without the refresh - the check refreshes
// once at its end.
func (s *Service) offerHeld(b Bundle) error {
	from, to := filepath.Join(s.heldDir(), b.Dir), filepath.Join(s.Dir, b.Dir)
	if err := Priv.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	_ = Priv.RemoveAll(to)
	if err := Priv.Rename(from, to); err != nil {
		return err
	}
	_ = Priv.Chown(to, 0, 0, true)
	return Priv.Chmod(to, 0o775)
}
