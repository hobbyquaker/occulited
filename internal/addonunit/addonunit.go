// Package addonunit renders the files of an addon's policy that root obeys (openccu-lite B-293):
// the systemd drop-in <id>.conf, which the fork's occu-addons generator installs as
// addon-<id>.service.d/10-policy.conf (and whose User= and uid comment its addon-users step turns
// into an account), the start order <id>.needs, the early start <id>.start and the start delay
// <id>.delay (openccu-lite task 369). The daemon decides
// what they say; the privilege helper checks that decision and writes the text itself, so a
// compromised daemon can no longer put an ExecStartPre=+, a User=root or a uid of 0 into a unit
// root starts. The same code renders on both sides: the daemon compares what is on disk with
// Render's text, the helper writes it.
//
// The package is pure: no files, no logging. What the daemon leaves out (a group the system does
// not know, a capability on the B-251 denylist) it leaves out before it builds a DropIn; what
// Validate refuses never reaches a unit.
package addonunit

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hobbyquaker/occulited/internal/manifest"
)

// The kinds of file, by their suffix under the policy directory.
const (
	KindDropIn = ".conf"
	KindNeeds  = ".needs"
	KindStart  = ".start"
	KindDelay  = ".delay"
)

// Kinds are the four suffixes, in the order the daemon writes them.
var Kinds = []string{KindDropIn, KindNeeds, KindStart, KindDelay}

// MaxStartDelay is the longest start delay in seconds (openccu-lite task 369): the generator adds
// it to the unit's TimeoutStartSec, and ten minutes after the interfaces answer is later than any
// stagger needs.
const MaxStartDelay = 600

// MinUID is the first uid of an addon user (system.AddonUIDBase): a confined drop-in names no
// lower one, so neither the unit nor the boot's addon-users step can make an account of the
// system's own users, root least of all.
const MinUID = 30000

// MaxUID keeps the uid inside what a passwd line and systemd take for a regular user.
const MaxUID = 1<<31 - 2

// NeedsIDs are the interface processes an addon can declare (task 94), in the order the file lists
// them: BidCos-RF, HmIP (and the VirtualDevices behind it), BidCos-Wired.
var NeedsIDs = []string{"rfd", "hmipserver", "hs485d"}

// StartEarly is the one line of a .start file.
const StartEarly = "early"

// CapSysAdmin is the capability a root addon declares to keep mounting (D-66).
const CapSysAdmin = "CAP_SYS_ADMIN"

var (
	// IDRe is an addon id as the policy files spell it (system's addonIDRe).
	IDRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}$`)
	capRe   = regexp.MustCompile(`^CAP_[A-Z_]{1,40}$`)
	groupRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	pathRe  = regexp.MustCompile(`^/[A-Za-z0-9_./-]{0,200}$`)
)

// DropIn is what an addon's systemd drop-in says, decided by the daemon and checked by the
// helper. Root mode carries nothing but MayMount; confined mode carries the user's uid (the user
// is always addon-<id>) and the grants, as they are to appear in the unit.
type DropIn struct {
	ID   string `json:"id"`
	Mode string `json:"mode"` // "root" or "confined"
	UID  int    `json:"uid,omitempty"`
	// Groups are the supplementary groups, as rendered (the daemon filtered them and added certs).
	Groups []string `json:"groups,omitempty"`
	// Capabilities are the ambient and bounding set of a confined addon.
	Capabilities []string `json:"capabilities,omitempty"`
	// MayMount: a root addon that declares CAP_SYS_ADMIN keeps the full set (D-66).
	MayMount bool `json:"may_mount,omitempty"`
	// Paths are the ReadWritePaths beyond the standard ones: the data directories the take-over
	// chowned (D-52), then the runtime block's paths.
	Paths []string `json:"paths,omitempty"`
}

// Validate is the helper's check of a drop-in: a well-formed id, a known mode, a uid in the addon
// range for a confined one and nothing but MayMount for a root one, and every group, capability
// and path of a shape that cannot add a line or a field to the unit - nothing root-equivalent
// among the grants of a confined addon (B-251's denylists).
func (d DropIn) Validate() error {
	if !IDRe.MatchString(d.ID) {
		return fmt.Errorf("addon id %q", d.ID)
	}
	switch d.Mode {
	case "root":
		if d.UID != 0 || len(d.Groups) > 0 || len(d.Capabilities) > 0 || len(d.Paths) > 0 {
			return errors.New("a root addon's drop-in carries no user, groups, capabilities or paths")
		}
		return nil
	case "confined":
	default:
		return fmt.Errorf("mode %q", d.Mode)
	}
	if d.MayMount {
		return errors.New("a confined addon does not mount")
	}
	if d.UID < MinUID || d.UID > MaxUID {
		return fmt.Errorf("uid %d is not an addon user's", d.UID)
	}
	for _, g := range d.Groups {
		if !groupRe.MatchString(g) {
			return fmt.Errorf("group %q", g)
		}
		if manifest.DeniedConfinedGroup(g) {
			return fmt.Errorf("group %s is root-equivalent", g)
		}
	}
	for _, c := range d.Capabilities {
		if !capRe.MatchString(c) {
			return fmt.Errorf("capability %q", c)
		}
		if manifest.DeniedConfinedCap(c) {
			return fmt.Errorf("capability %s is root-equivalent", c)
		}
	}
	for _, p := range d.Paths {
		if !pathRe.MatchString(p) || strings.Contains(p, "..") {
			return fmt.Errorf("path %q", p)
		}
	}
	return nil
}

// header is the drop-in's first two lines.
func header(id string) string {
	return fmt.Sprintf("# openccu-lite addon policy for %s: written by occulited, installed by the\n# occu-addons generator as addon-%s.service.d/10-policy.conf. Change it on the Services page.\n", id, id)
}

// ConfinedUID reads a drop-in Render wrote: the uid when it confines the addon id to its own user,
// ok false for a root addon's, another addon's, or text Render did not write (openccu-lite B-295:
// the helper asks its own rendered file whether an addon is confined).
func ConfinedUID(id, text string) (uid int, ok bool) {
	rest, found := strings.CutPrefix(text, header(id)+"# mode=confined uid=")
	if !found {
		return 0, false
	}
	num, rest, found := strings.Cut(rest, "\n")
	if !found || num == "" || len(num) > 9 || strings.TrimLeft(num, "0123456789") != "" || num[0] == '0' {
		return 0, false
	}
	user := "addon-" + id
	if !strings.HasPrefix(rest, "[Service]\nUser="+user+"\nGroup="+user+"\n") {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Render is the drop-in's text. Root mode without MayMount takes CAP_SYS_ADMIN away; with it the
// file is comments only - the generator installs a drop-in only when it has a [Service] section.
func (d DropIn) Render() string {
	var b strings.Builder
	// the header is part of what the unit editor shows as the effective unit, so it names no
	// decision id (task 52)
	b.WriteString(header(d.ID))
	if d.Mode != "confined" {
		b.WriteString("# mode=root\n")
		// D-66: a root addon keeps root, not the right to mount. Without CAP_SYS_ADMIN the
		// CCU-era `mount -o remount,rw /` around an addon script's writes answers "permission
		// denied" and the script goes on; what it writes into /firmware/rftypes lands in the
		// writable layer the image mounts there (occu-extension-dirs).
		if d.MayMount {
			b.WriteString("# may mount: the addon's entry declares " + CapSysAdmin + "\n")
			return b.String()
		}
		b.WriteString("[Service]\nCapabilityBoundingSet=~" + CapSysAdmin + "\n")
		return b.String()
	}
	user := "addon-" + d.ID
	fmt.Fprintf(&b, "# mode=confined uid=%d\n[Service]\nUser=%s\nGroup=%s\n", d.UID, user, user)
	if len(d.Groups) > 0 {
		fmt.Fprintf(&b, "SupplementaryGroups=%s\n", strings.Join(d.Groups, " "))
	}
	if len(d.Capabilities) > 0 {
		joined := strings.Join(d.Capabilities, " ")
		fmt.Fprintf(&b, "AmbientCapabilities=%s\nCapabilityBoundingSet=%s\n", joined, joined)
	} else {
		b.WriteString("CapabilityBoundingSet=\n")
	}
	// "-": a path that does not exist is skipped instead of failing the unit (226/NAMESPACE)
	paths := []string{"/usr/local/addons/" + d.ID, "/usr/local/etc/config/addons/" + d.ID, "/usr/local/etc/config/rc.d", "/run", "/var/log", "/tmp", "/var/tmp"}
	paths = append(paths, d.Paths...)
	for i := range paths {
		paths[i] = "-" + paths[i]
	}
	b.WriteString("NoNewPrivileges=yes\nProtectSystem=strict\nProtectKernelTunables=yes\nProtectControlGroups=yes\nRestrictSUIDSGID=yes\n")
	// /run/addon-<id>, owned by the user: where a confined addon can keep its pid file
	fmt.Fprintf(&b, "RuntimeDirectory=addon-%s\nRuntimeDirectoryPreserve=yes\n", d.ID)
	fmt.Fprintf(&b, "ReadWritePaths=%s\n", strings.Join(paths, " "))
	return b.String()
}

// ValidateNeeds checks a start order: ids out of NeedsIDs, each once. Empty is "none".
func ValidateNeeds(needs []string) error {
	for i, n := range needs {
		if !slices.Contains(NeedsIDs, n) {
			return fmt.Errorf("interface %q", n)
		}
		if slices.Contains(needs[:i], n) {
			return fmt.Errorf("interface %s twice", n)
		}
	}
	return nil
}

// RenderNeeds is the .needs file's one line: "none" for an empty list, else the ids in NeedsIDs'
// order.
func RenderNeeds(needs []string) string {
	if len(needs) == 0 {
		return "none\n"
	}
	var ids []string
	for _, id := range NeedsIDs {
		if slices.Contains(needs, id) {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, " ") + "\n"
}

// RenderStart is the .start file's one line.
func RenderStart() string { return StartEarly + "\n" }

// ValidateDelay checks a start delay: whole seconds, 1 to MaxStartDelay (0 is no file at all).
func ValidateDelay(seconds int) error {
	if seconds < 1 || seconds > MaxStartDelay {
		return fmt.Errorf("a start delay is 1 to %d seconds, not %d", MaxStartDelay, seconds)
	}
	return nil
}

// RenderDelay is the .delay file's one line: the seconds.
func RenderDelay(seconds int) string { return strconv.Itoa(seconds) + "\n" }

// File is one policy file as the helper's operation takes it: the kind comes from the path's
// suffix, and exactly that kind's field is set - or Remove, for any kind.
type File struct {
	DropIn *DropIn `json:"drop_in,omitempty"`
	// Needs is the .needs file's interfaces; an empty, non-nil list is "none".
	Needs *[]string `json:"needs,omitempty"`
	// Early writes the .start file.
	Early bool `json:"early,omitempty"`
	// Delay writes the .delay file: the seconds an addon waits at boot after its interfaces.
	Delay  *int `json:"delay,omitempty"`
	Remove bool `json:"remove,omitempty"`
}

// Content checks f against the file's kind and id and answers the text to write; remove is true
// when the file is to go instead.
func (f File) Content(kind, id string) (text string, remove bool, err error) {
	set := 0
	for _, b := range []bool{f.DropIn != nil, f.Needs != nil, f.Early, f.Delay != nil, f.Remove} {
		if b {
			set++
		}
	}
	if set != 1 {
		return "", false, errors.New("a policy file is one kind of content, or removed")
	}
	if f.Remove {
		return "", true, nil
	}
	switch kind {
	case KindDropIn:
		if f.DropIn == nil {
			return "", false, errors.New("a .conf file is a drop-in")
		}
		if f.DropIn.ID != id {
			return "", false, fmt.Errorf("the drop-in of %q in the file of %q", f.DropIn.ID, id)
		}
		if err := f.DropIn.Validate(); err != nil {
			return "", false, err
		}
		return f.DropIn.Render(), false, nil
	case KindNeeds:
		if f.Needs == nil {
			return "", false, errors.New("a .needs file is a start order")
		}
		if err := ValidateNeeds(*f.Needs); err != nil {
			return "", false, err
		}
		return RenderNeeds(*f.Needs), false, nil
	case KindStart:
		if !f.Early {
			return "", false, errors.New("a .start file is the early start")
		}
		return RenderStart(), false, nil
	case KindDelay:
		if f.Delay == nil {
			return "", false, errors.New("a .delay file is a start delay")
		}
		if err := ValidateDelay(*f.Delay); err != nil {
			return "", false, err
		}
		return RenderDelay(*f.Delay), false, nil
	}
	return "", false, fmt.Errorf("kind %q", kind)
}
