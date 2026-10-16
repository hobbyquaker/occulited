package firmware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
// (true) or held until the user delivers it (false, the default since occulited task 48).
func (s *Service) AutoDeploy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoDeploy()
}

func (s *Service) autoDeploy() bool {
	return s.autoDeployOn
}

// SetAutoDeploy sets the global switch and keeps it in the state file.
func (s *Service) SetAutoDeploy(on bool) {
	s.mu.Lock()
	s.autoDeployOn = on
	s.save()
	s.mu.Unlock()
}

// heldTypes is the sorted list of the explicitly held type keys (caller holds mu).
func (s *Service) heldTypes() []string {
	return sortedKeys(s.held)
}

// sortedKeys are a set's keys whose value is true, sorted.
func sortedKeys(m map[string]bool) []string {
	var out []string
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ErrNoBundle is SetTransfer's answer for a directory that holds no bundle, offered or held.
var ErrNoBundle = errors.New("no such bundle")

// bundleDirRe is a bundle directory's name as the API names it: a firmware directory's
// <TypeCode> or <TypeCode>-<Name> (typeCodeRe), or a held one's <TypeCode>-<Name>-<version>
// (heldName), which may be longer.
var bundleDirRe = regexp.MustCompile(`^[0-9A-Za-z_-][0-9A-Za-z._-]{0,63}$`)

// heldName is a bundle's directory in the held directory (occulited task 48): its TypeCode, type
// and version, so two versions of one type lie side by side there and a download of the same
// version replaces its own. Bundles held before task 48 keep their <TypeCode> directories; every
// listing reads a bundle by its info, whatever its directory is called.
func heldName(b *Bundle) string {
	v := b.Version
	if v == "" {
		v = b.VersionFromName
	}
	if v == "" {
		v = "0"
	}
	n := dirSafe(b.TypeCode + "-" + b.Name + "-" + v)
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// SetTransfer delivers a bundle to the devices (on) or holds it back (off): the bundle moves
// between the held directory and the firmware directory, the type is marked held (or no
// longer), the interfaces re-read the directory, and the live states are read again.
//
// On takes the held bundle of that directory name when there is one - a newer download of a
// delivered type lies in the held directory under its own name while the older one is still
// offered - and moves it into the firmware directory over the type's offered bundle, which goes
// into the held directory (task 48: nothing the user had is deleted by a delivery). Off moves the
// offered bundle into the held directory. A bundle that is where it should be already changes
// only the mark.
//
// Task 49: any held version may be delivered, an older one too (hmipserver offers whatever is
// deployed when it differs from the device's firmware, and only one version of a type may be
// deployed - of several, the highest wins). The replaced version is remembered as the previous
// one; a delivered version below the newest on the system pins the type, so the check keeps every
// new download of it held instead of undoing the choice; the held directory then keeps the newest
// and the previous version of the type (pruneHeld).
func (s *Service) SetTransfer(bundleDir string, on bool) (*Bundle, error) {
	if !bundleDirRe.MatchString(bundleDir) {
		return nil, ErrNoBundle
	}
	live, held := filepath.Join(s.Dir, bundleDir), filepath.Join(s.heldDir(), bundleDir)
	var b, replaced *Bundle
	if on {
		if hb, _ := bundleAt(held, bundleDir); hb != nil {
			prev, err := s.deliver(held, hb)
			if err != nil {
				return nil, fmt.Errorf("move %s: %w", bundleDir, err)
			}
			b, replaced = hb, prev
		} else if lb, _ := bundleAt(live, bundleDir); lb != nil {
			b = lb
		}
	} else {
		if lb, _ := bundleAt(live, bundleDir); lb != nil {
			name := heldName(lb)
			if err := moveBundle(live, s.heldDir(), name); err != nil {
				return nil, fmt.Errorf("move %s: %w", bundleDir, err)
			}
			lb.Dir = name
			b = lb
		} else if hb, _ := bundleAt(held, bundleDir); hb != nil {
			b = hb
		}
	}
	if b == nil {
		return nil, ErrNoBundle
	}
	b.Held = !on
	key := TypeKey(b.Name)
	// task 49: is the delivered version below the newest of the type on the system?
	older := false
	if on {
		if held, err := Held(s.heldDir()); err == nil {
			for _, h := range held {
				if TypeKey(h.Name) == key && CompareVersions(versionOf(h), versionOf(*b)) > 0 {
					older = true
				}
			}
		}
	}
	s.mu.Lock()
	if s.held == nil {
		s.held = map[string]bool{}
	}
	if s.pinned == nil {
		s.pinned = map[string]bool{}
	}
	if s.prev == nil {
		s.prev = map[string]string{}
	}
	if on {
		delete(s.held, key)
		if older {
			s.pinned[key] = true
		} else {
			delete(s.pinned, key)
		}
		if replaced != nil && versionOf(*replaced) != versionOf(*b) {
			s.prev[key] = versionOf(*replaced)
		}
	} else {
		s.held[key] = true
		delete(s.pinned, key)
	}
	s.save()
	s.mu.Unlock()
	if on && replaced != nil {
		s.pruneHeld(key)
	}
	for name, err := range interfaces.RefreshFirmware(s.Interfaces()) {
		s.Log.Warn("firmware: refresh failed", "interface", name, "err", err)
	}
	s.Log.Info("firmware: transfer switched", "type", b.Name, "version", b.Version, "offered", on)
	s.Refresh()
	return b, nil
}

// deliver moves the held bundle at path into the firmware directory - into the directory of the
// type's offered bundle, which goes into the held directory first (bundleDir: the same type's
// directory, else a free one) - and sets b.Dir and b.Held to where it is now; it returns the
// replaced bundle (nil for none). Without the refresh: the caller refreshes.
func (s *Service) deliver(path string, b *Bundle) (*Bundle, error) {
	name, err := bundleDir(s.Dir, b)
	if err != nil {
		return nil, err
	}
	to := filepath.Join(s.Dir, name)
	prev, _ := bundleAt(to, name)
	if prev != nil {
		if err := moveBundle(to, s.heldDir(), heldName(prev)); err != nil {
			return nil, err
		}
		prev.Dir, prev.Held = heldName(prev), true
	}
	if err := moveBundle(path, s.Dir, name); err != nil {
		return nil, err
	}
	b.Dir, b.Held = name, false
	return prev, nil
}

// moveBundle renames a bundle directory to base/name, over whatever lies there, and gives it the
// owner and mode Deploy gives one.
func moveBundle(from, base, name string) error {
	if err := Priv.MkdirAll(base, 0o755); err != nil {
		return err
	}
	to := filepath.Join(base, name)
	if filepath.Clean(from) == filepath.Clean(to) {
		return nil
	}
	_ = Priv.RemoveAll(to)
	if err := Priv.Rename(from, to); err != nil {
		return err
	}
	_ = Priv.Chown(to, 0, 0, true)
	return Priv.Chmod(to, 0o775)
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
// explicitly held type, a type whose delivered version the user chose below the newest (task 49)
// and while the global switch is off, else the firmware directory. A BidCos-RF type's download
// always goes to the firmware directory (occulited task 63): rfd transfers nothing by itself, a
// bundle there only lets the frontends offer the update.
func (s *Service) target(typeKey string, bidcos bool) (dir string, heldBack bool) {
	if bidcos {
		return s.Dir, false
	}
	if s.held[typeKey] || s.pinned[typeKey] || !s.autoDeploy() {
		return s.heldDir(), true
	}
	return s.Dir, false
}

// offerHeld moves a held bundle into the firmware directory (the global switch back on, the type
// not held): the check's counterpart of SetTransfer, without the refresh - the check refreshes
// once at its end.
func (s *Service) offerHeld(b Bundle) error {
	prev, err := s.deliver(filepath.Join(s.heldDir(), b.Dir), &b)
	if err == nil && prev != nil && versionOf(*prev) != versionOf(b) {
		key := TypeKey(b.Name)
		s.mu.Lock()
		if s.prev == nil {
			s.prev = map[string]string{}
		}
		s.prev[key] = versionOf(*prev)
		s.save()
		s.mu.Unlock()
		s.pruneHeld(key)
	}
	return err
}

// pruneHeld keeps a type's newest held bundle and the one of its previously delivered version
// (task 49: the way back) and removes its other held bundles (task 48 kept the newest only):
// called after a download into the held directory and after a delivery. Other types are left
// alone.
func (s *Service) pruneHeld(key string) {
	held, err := Held(s.heldDir())
	if err != nil {
		return
	}
	s.mu.Lock()
	prev := s.prev[key]
	s.mu.Unlock()
	var mine []Bundle
	for _, h := range held {
		if TypeKey(h.Name) == key {
			mine = append(mine, h)
		}
	}
	newest := ""
	for _, h := range mine {
		if newest == "" || CompareVersions(versionOf(h), newest) > 0 {
			newest = versionOf(h)
		}
	}
	keptPrev := false
	for _, h := range mine {
		v := versionOf(h)
		if CompareVersions(v, newest) == 0 {
			continue
		}
		if prev != "" && CompareVersions(v, prev) == 0 && !keptPrev {
			keptPrev = true
			continue
		}
		if err := Priv.RemoveAll(filepath.Join(s.heldDir(), h.Dir)); err != nil {
			s.Log.Warn("firmware: held bundle not removed", "dir", h.Dir, "err", err)
		}
	}
}

// versionOf is a bundle's version, or the one its update file's name carries.
func versionOf(b Bundle) string {
	if b.Version != "" {
		return b.Version
	}
	return b.VersionFromName
}

// BidCosType reports whether a device type is a BidCos-RF one, as eQ-3 names them: HM-… (the
// wired HMW-… types get their firmware with the system image, the HmIP types begin with HmIP).
// The check also takes a type as BidCos when a device of it is on BidCos-RF.
func BidCosType(deviceType string) bool {
	return strings.HasPrefix(TypeKey(deviceType), "hm-")
}

// DeliverHeldBidCos moves the BidCos-RF bundles out of the held directory once (occulited
// task 63, the maintainer 2026-10-16: BidCos firmware is delivered at once): per type the newest
// one goes into the firmware directory when it is newer than the type's offered bundle, and every
// other held bundle of the type is removed - a BidCos type has no held state any more. The types'
// hold, pin and previous marks go too. Done when the state file says so; the interfaces are told
// when a bundle moved. It runs at the start of Run.
func (s *Service) DeliverHeldBidCos() {
	s.mu.Lock()
	done := s.bidcosDelivered
	s.mu.Unlock()
	if done {
		return
	}
	held, err := Held(s.heldDir())
	if err != nil {
		s.Log.Warn("firmware: held bundles not read, BidCos ones stay for the next start", "err", err)
		return
	}
	newest := map[string]Bundle{}
	for _, h := range held {
		if !BidCosType(h.Name) {
			continue
		}
		key := TypeKey(h.Name)
		if cur, ok := newest[key]; !ok || CompareVersions(versionOf(h), versionOf(cur)) > 0 {
			newest[key] = h
		}
	}
	offered := map[string]Bundle{}
	if dep, err := Deployed(s.Dir); err == nil {
		for _, b := range dep {
			offered[TypeKey(b.Name)] = b
		}
	}
	moved := 0
	for key, h := range newest {
		if o, ok := offered[key]; ok && CompareVersions(versionOf(h), versionOf(o)) <= 0 {
			continue
		}
		b := h
		if _, err := s.deliver(filepath.Join(s.heldDir(), h.Dir), &b); err != nil {
			s.Log.Warn("firmware: held BidCos bundle not delivered", "type", h.Name, "version", versionOf(h), "err", err)
			continue
		}
		moved++
		s.Log.Info("firmware: held BidCos bundle delivered", "type", h.Name, "version", versionOf(h), "dir", b.Dir)
	}
	// what is left of these types in the held directory: older versions, the replaced offered one
	if rest, err := Held(s.heldDir()); err == nil {
		for _, h := range rest {
			if _, ok := newest[TypeKey(h.Name)]; ok {
				if err := Priv.RemoveAll(filepath.Join(s.heldDir(), h.Dir)); err != nil {
					s.Log.Warn("firmware: held BidCos bundle not removed", "dir", h.Dir, "err", err)
				}
			}
		}
	}
	s.mu.Lock()
	for _, m := range []map[string]bool{s.held, s.pinned} {
		for k := range m {
			if BidCosType(k) {
				delete(m, k)
			}
		}
	}
	for k := range s.prev {
		if BidCosType(k) {
			delete(s.prev, k)
		}
	}
	s.bidcosDelivered = true
	s.save()
	s.mu.Unlock()
	if moved > 0 {
		for name, err := range interfaces.RefreshFirmware(s.Interfaces()) {
			s.Log.Warn("firmware: refresh failed", "interface", name, "err", err)
		}
	}
}
