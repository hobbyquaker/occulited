// Package netmount is a network share's mount (openccu-lite tasks 86 and 228): the shape of the
// request, its checks, and the .mount and .automount units rendered from it. It is pure - no files,
// no commands - so the daemon and the privilege helper apply the same checks, and the helper
// renders the units itself from checked fields instead of taking unit text from the daemon: What=
// and Options= are exactly where a mistake mounts something over /etc.
package netmount

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Base is where every share is mounted, /media/net/<id>: /media is a tmpfs on the image, and the
// helper's write prefix already covers it. The one place the mount point is named (task 228: the
// maintainer may still want /media/share; changing this constant is the whole change, since the
// shares are stored by id and every path is derived from it).
const Base = "/media/net"

// The kinds a mount target can be.
const (
	KindNFS  = "nfs"
	KindCIFS = "cifs"
)

// IDRe is a target's id: a letter, then letters and digits, 16 at most. No "-": systemd escapes it
// as \x2d in a mount unit's name, and the id is part of that name.
var IDRe = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// ValidID reports whether id is a target id.
func ValidID(id string) bool { return IDRe.MatchString(id) }

// hostRe is a DNS name (letters, digits, "-" and "." with the usual edges).
var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?)*$`)

// NFS versions: "" lets mount.nfs negotiate (4.2, 4.1, 4.0, then 3).
var nfsVersions = map[string]bool{"": true, "4.2": true, "4.1": true, "4": true, "3": true}

// CIFS versions: "" is "3.0 or newer" (vers=3); SMB1 and SMB2.x are never offered.
var cifsVersions = map[string]bool{"": true, "3.1.1": true, "3.0": true}

// Spec is one mount target, as the daemon asks for it and the helper renders it.
type Spec struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Server string `json:"server"`
	// Path is the NFS export (/volume1/backup) or the CIFS share, optionally with a directory
	// inside it (backup or backup/ccu).
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
	// Seal asks for SMB3 encryption (CIFS only).
	Seal bool `json:"seal,omitempty"`
	// ReadOnly mounts the share read-only (task 228's small set of options: ro or rw; nosuid,
	// nodev and noexec always).
	ReadOnly bool `json:"read_only,omitempty"`
	// Share says the credentials are the share store's (<state>/shares/<id>/cifs.cred, task 228),
	// not a task-86 backup target's (<state>/backup-targets/<id>/cifs.cred). The helper maps it
	// onto one of two fixed directories; the daemon never names the file.
	Share bool `json:"share,omitempty"`
}

// Where is the mount point.
func (s Spec) Where() string { return Base + "/" + s.ID }

// UnitName is the units' name without the suffix: media-net-<id>.
func UnitName(id string) string { return "media-net-" + id }

// ValidHost reports whether h is a DNS name or an IP literal.
func ValidHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return !strings.Contains(h, "%") // no zone: it would be a specifier in a unit file
	}
	return len(h) <= 253 && hostRe.MatchString(h)
}

// pathOK: printable, no whitespace, no "," (the option separator), no quotes, no "\" and no "%"
// (a specifier in a unit file), no ".." component.
func pathOK(p string) bool {
	if p == "" || len(p) > 1024 {
		return false
	}
	for _, r := range p {
		if r < 0x21 || r == 0x7f || strings.ContainsRune(`,"'\%`, r) {
			return false
		}
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." || c == "." {
			return false
		}
	}
	return true
}

// Validate checks every field; the helper calls it again at the boundary.
func (s Spec) Validate() error {
	if !ValidID(s.ID) {
		return fmt.Errorf("id: %q is not a target id", s.ID)
	}
	if !ValidHost(s.Server) {
		return fmt.Errorf("server: %q is not a host name or an IP address", s.Server)
	}
	switch s.Kind {
	case KindNFS:
		if !strings.HasPrefix(s.Path, "/") || !pathOK(s.Path) || strings.Contains(s.Path, "//") {
			return errors.New("path: the export, an absolute path without spaces, commas, quotes or %")
		}
		if !nfsVersions[s.Version] {
			return fmt.Errorf("version: %q is not an NFS version (4.2, 4.1, 4, 3 or empty)", s.Version)
		}
		if s.Seal {
			return errors.New("seal: CIFS only")
		}
	case KindCIFS:
		if strings.HasPrefix(s.Path, "/") || strings.HasSuffix(s.Path, "/") || !pathOK(s.Path) || strings.Contains(s.Path, "//") {
			return errors.New("path: the share name, optionally with a directory in it (backup or backup/ccu)")
		}
		if !cifsVersions[s.Version] {
			return fmt.Errorf("version: %q is not an SMB version (3.1.1, 3.0 or empty)", s.Version)
		}
	default:
		return fmt.Errorf("kind: %q is not nfs or cifs", s.Kind)
	}
	return nil
}

// What is the mount source.
func (s Spec) What() string {
	host := s.Server
	if a, err := netip.ParseAddr(host); err == nil && a.Is6() {
		host = "[" + host + "]"
	}
	if s.Kind == KindCIFS {
		return "//" + host + "/" + s.Path
	}
	return host + ":" + s.Path
}

// Common options: a mount that cannot run programs or device nodes from the share, whatever the
// server offers.
const commonOptions = "nosuid,nodev,noexec"

// Options renders the mount options. credFile is the CIFS credentials file (the helper's fixed
// path for this id), gid the occulite group's id, so the daemon can list and read what root wrote.
func (s Spec) Options(credFile string, gid int) string {
	var o []string
	if s.ReadOnly {
		o = append(o, "ro")
	}
	switch s.Kind {
	case KindNFS:
		if s.Version != "" {
			o = append(o, "vers="+s.Version)
		}
		// soft: a dead server is an error after about a minute, not a process in D state until it
		// is back; the backup is verified after writing, so a soft error costs one night
		o = append(o, "proto=tcp", "soft", "timeo=100", "retrans=2")
		if s.Version == "3" {
			// locking needs rpc.statd and inbound callbacks, which the firewall drops; a backup
			// needs no locks
			o = append(o, "nolock")
		}
		// an IP literal is handed to the kernel as well: without mount.nfs (older images) the
		// kernel needs addr=, and mount.nfs replaces the option with its own
		if a, err := netip.ParseAddr(s.Server); err == nil {
			o = append(o, "addr="+a.String())
		}
	case KindCIFS:
		v := s.Version
		if v == "" {
			v = "3"
		}
		o = append(o, "vers="+v, "credentials="+credFile, "uid=0", "gid="+strconv.Itoa(gid), "file_mode=0640", "dir_mode=0750", "soft", "echo_interval=15")
		if s.Seal {
			o = append(o, "seal")
		}
	}
	o = append(o, commonOptions)
	return strings.Join(o, ",")
}

// Type is the mount unit's Type=.
func (s Spec) Type() string {
	if s.Kind == KindCIFS {
		return "cifs"
	}
	return "nfs"
}

// MountTimeout bounds one mount attempt; IdleTimeout unmounts an unused share.
const (
	MountTimeout = "30s"
	IdleTimeout  = "5min"
)

// Render returns the two unit files. It validates first: nothing is rendered from a Spec that
// does not pass.
func (s Spec) Render(credFile string, gid int) (mount, automount []byte, err error) {
	if err := s.Validate(); err != nil {
		return nil, nil, err
	}
	desc := "openccu-lite network share " + s.ID
	mount = []byte(fmt.Sprintf(`# openccu-lite: a network share, rendered by occulited's helper
[Unit]
Description=%s
Documentation=https://github.com/openccu-lite/openccu-lite

[Mount]
What=%s
Where=%s
Type=%s
Options=%s
TimeoutSec=%s
# a stop detaches the share even when its server does not answer (umount -l -f), so an unmount and
# a shutdown never wait for a dead server
LazyUnmount=yes
ForceUnmount=yes
`, desc, s.What(), s.Where(), s.Type(), s.Options(credFile, gid), MountTimeout))
	automount = []byte(fmt.Sprintf(`# openccu-lite: a network share, rendered by occulited's helper. Mounted on the first
# access (a backup, a copy, a test, a listing) and unmounted when idle: a server gone at night leaves no
# stale mount for the day, and a shutdown does not wait for it.
[Unit]
Description=%s (automount)
Documentation=https://github.com/openccu-lite/openccu-lite

[Automount]
Where=%s
TimeoutIdleSec=%s
`, desc, s.Where(), IdleTimeout))
	return mount, automount, nil
}

// Mountinfo is one entry of /proc/self/mountinfo.
type Mountinfo struct {
	Source, FSType, Options string
}

// ParseMountinfo finds the network mount on where in a mountinfo text: the armed automount
// (autofs) below it is not one, and a later line is the share.
func ParseMountinfo(r io.Reader, where string) (Mountinfo, bool) {
	var found Mountinfo
	ok := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		// id parent major:minor root mountpoint options [optional...] - fstype source superoptions
		pre, post, cut := strings.Cut(sc.Text(), " - ")
		if !cut {
			continue
		}
		f := strings.Fields(pre)
		g := strings.Fields(post)
		if len(f) < 6 || len(g) < 3 || unescapeMount(f[4]) != where {
			continue
		}
		switch g[0] {
		case "nfs", "nfs4", "cifs", "smb3":
			found, ok = Mountinfo{Source: unescapeMount(g[1]), FSType: g[0], Options: g[2]}, true
		}
	}
	return found, ok
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}
