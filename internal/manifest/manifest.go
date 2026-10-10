// Package manifest is the addon manifest openccu-lite.json (docs/manifest-format.md): what an addon
// says about itself, shipped at the root of its package and at a path of its repository. The
// package's copy is what the system applies at install and update; the repository's copy at the
// latest release tag is what the Addons page shows before an install. The catalogue
// (docs/catalog-format.md) only says where the manifests are.
//
// The package is pure: types, the parser with its validation, and the reader that finds the file
// inside a package archive. Nothing here knows the policy files or the network.
package manifest

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// FileName is the manifest's name at the root of the package archive.
const FileName = "openccu-lite.json"

// Format is the one format this binary reads.
const Format = 1

// MaxSize bounds a manifest: a package whose manifest is larger than this is treated as having
// none, which keeps a hostile or broken archive from occupying the reader.
const MaxSize = 256 << 10

// ErrNoManifest is FromArchive's answer for a package without openccu-lite.json at its root.
var ErrNoManifest = errors.New("the package carries no " + FileName)

// Text is a string in the two UI languages, keyed "de" and "en". A plain string in the JSON is
// taken as both.
type Text map[string]string

// UnmarshalJSON takes {"de": …, "en": …} or a plain string.
func (t *Text) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = Text{"de": s, "en": s}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*t = Text(m)
	return nil
}

// In answers the text in lang, falling back to English, then to German, then to anything.
func (t Text) In(lang string) string {
	if t == nil {
		return ""
	}
	for _, l := range []string{lang, "en", "de"} {
		if s := strings.TrimSpace(t[l]); s != "" {
			return s
		}
	}
	for _, s := range t {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// Manifest is one openccu-lite.json.
type Manifest struct {
	Format int `json:"format"`
	// ID is the addon's rc.d id: what its update_script links into /usr/local/etc/config/rc.d.
	ID string `json:"id"`
	// Version is the package's version, informational; the system reads the installed version
	// from the rc.d script's info as it always did.
	Version     string `json:"version,omitempty"`
	Name        Text   `json:"name"`
	Description Text   `json:"description,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	// Changelog is where the release notes are (occulited task 26): an http(s) URL, e.g. a
	// CHANGELOG.md; the Addons page links it beside an offered update instead of the release's page.
	Changelog string `json:"changelog,omitempty"`
	Licence   string `json:"licence,omitempty"`
	// Release says where the packages are published: the update check and the catalogue install
	// read it. Absent for an addon that is installed by upload only.
	Release  *Release `json:"release,omitempty"`
	Requires Requires `json:"requires,omitempty"`
	UI       UI       `json:"ui,omitempty"`
	// Runtime is what the addon needs to run: applied as declared (D-119).
	Runtime *Runtime `json:"runtime,omitempty"`
}

// Release is where the packages are published.
type Release struct {
	GitHub string `json:"github"` // owner/repo
	// Asset is the asset name with {arch} and {version} placeholders; Assets maps an architecture
	// to a pattern for projects that do not name packages by uname -m; Fallback is tried when
	// neither matches (a package for every architecture).
	Asset      string            `json:"asset,omitempty"`
	Assets     map[string]string `json:"assets,omitempty"`
	Fallback   string            `json:"fallback_asset,omitempty"`
	Prerelease bool              `json:"prerelease,omitempty"` // prereleases count (a project without a stable release yet)
}

// Requires is the addon's compatibility.
type Requires struct {
	// Lite is the oldest openccu-lite the addon runs on, e.g. "1.0.0"; "" for any.
	Lite string `json:"lite,omitempty"`
	// Rega: the addon needs the ReGa, which openccu-lite does not have. A manifest is an
	// openccu-lite declaration, so its absence means the addon runs without it.
	Rega bool `json:"rega,omitempty"`
	// Forms are the product forms the addon runs on (sd, ova); empty = all.
	Forms []string `json:"forms,omitempty"`
	// Architectures are the uname -m names the package exists for; empty = any.
	Architectures []string `json:"architectures,omitempty"`
}

// UI is what the shell shows and how it opens the addon.
type UI struct {
	// Icon, IconDark, Logo and LogoDark are paths relative to the package root (www/icon.svg):
	// a square icon and a wide logo, each with a variant for dark backgrounds.
	Icon     string `json:"icon,omitempty"`
	IconDark string `json:"icon_dark,omitempty"`
	Logo     string `json:"logo,omitempty"`
	LogoDark string `json:"logo_dark,omitempty"`
	// SettingsURL names the settings page where the addon's Config-Url is not it: a path under
	// /addons/, with a query.
	SettingsURL string `json:"settings_url,omitempty"`
	// SessionHeader: this version reads the gate's X-Occulite-Session everywhere the shell opens
	// it, so the shell leaves ?sid= off its URLs.
	SessionHeader bool `json:"session_header,omitempty"`
	// OwnUpdater: the addon still carries an update mechanism of its own, which the system's
	// updates bypass; the page notes it.
	OwnUpdater bool `json:"own_updater,omitempty"`
	// Fullscreen (occulited task 24, openccu-lite #11): the addon's frontend brings a header and
	// a menu of its own and offers its own way back to the system (a link to /), so the shell may
	// show it as the whole window, without its top bar. The flag only makes the choice available:
	// the user ticks it per addon on the Settings page, and it is off until then.
	Fullscreen bool `json:"fullscreen,omitempty"`
}

// Runtime is what the addon needs when it runs under systemd as its own user (or as root).
type Runtime struct {
	Root         bool                `json:"root,omitempty"`
	Capabilities []string            `json:"capabilities,omitempty"`
	Groups       []string            `json:"groups,omitempty"`
	Paths        []string            `json:"paths,omitempty"`
	DataDirs     []string            `json:"data_dirs,omitempty"`
	Ports        []int               `json:"ports,omitempty"`
	PortInfo     map[string]PortInfo `json:"port_info,omitempty"`
	// Needs are the interface processes the addon talks to (rfd, hmipserver, hs485d): its unit
	// starts after them. nil = undeclared (after rfd and hmipserver); [] = none (right after the
	// network).
	Needs *[]string `json:"needs,omitempty"`
	// Start is "early" for an addon that copes with interfaces not ready yet, or "".
	Start string `json:"start,omitempty"`
	// Daemon: the addon keeps a process running after its rc.d start (openccu-lite B-158), so an
	// empty unit is an addon whose daemon ended, not one that only prepared things.
	Daemon    bool     `json:"daemon,omitempty"`
	APIScopes []string `json:"api_scopes,omitempty"`
	Note      Text     `json:"note,omitempty"`
}

// PortInfo describes one declared port.
type PortInfo struct {
	Proto string `json:"proto,omitempty"`
	TLS   bool   `json:"tls,omitempty"`
	Label Text   `json:"label,omitempty"`
}

// StartEarly is the one value of runtime.start that means something.
const StartEarly = "early"

// deniedConfinedCaps are the capabilities a confined addon may not declare: each one is
// root-equivalent in effect, so granting it to an addon the system calls "confined" would make the
// label a lie (B-251, D-119). Every name here was checked against capabilities(7):
//
//   - CAP_SYS_ADMIN, CAP_SYS_MODULE, CAP_SYS_RAWIO, CAP_MKNOD, CAP_BPF, CAP_SYS_BOOT: mount, load a
//     kernel module, raw I/O to /dev/mem, make a device node, load BPF, kexec a kernel - the kernel
//     itself, which is root and more.
//   - CAP_DAC_OVERRIDE, CAP_DAC_READ_SEARCH, CAP_FOWNER, CAP_CHOWN: bypass or rewrite file
//     permissions and ownership - read the keys and every other addon's tree, write anywhere.
//   - CAP_SETUID, CAP_SETGID, CAP_SETPCAP: become uid 0 / any gid, or hand capabilities around -
//     the confinement's whole point undone.
//   - CAP_SYS_PTRACE: attach to a root process and inject code.
//   - CAP_SYS_CHROOT: pivot the root, a building block of a namespace escape.
//   - CAP_MAC_ADMIN, CAP_MAC_OVERRIDE: configure or bypass mandatory access control (an LSM) - the
//     policy that would otherwise fence the addon.
//   - CAP_NET_ADMIN: not filesystem-root, but system-integrity-root. It reconfigures every
//     interface, the routing table and the packet filter. On lite the firewall is a control
//     occulited owns (the Firewall page, D-9's "one door"); an addon with CAP_NET_ADMIN could flush
//     or rewrite it and expose every port to the LAN, or to the internet where a port is forwarded,
//     put an interface into promiscuous mode and sniff the LAN, or reroute the box's own traffic
//     (ACME, updates) for a MITM. That crosses the confinement boundary, so it is denied. The real
//     case - an addon that brings up a VPN interface - runs as *root (unsafe)* by the user's choice
//     on the Services page, exactly as a mounting addon does with CAP_SYS_ADMIN (D-66).
//
// Not on the list, on purpose: CAP_NET_BIND_SERVICE and CAP_NET_RAW (a low port, a ping socket),
// CAP_SYS_NICE, CAP_SYS_TIME, CAP_KILL, CAP_SYSLOG, CAP_SYS_RESOURCE, CAP_LINUX_IMMUTABLE - each is
// a nuisance or a narrow DoS at worst, not a path to root or to the keys.
var deniedConfinedCaps = map[string]bool{
	"CAP_SYS_ADMIN":       true,
	"CAP_SYS_MODULE":      true,
	"CAP_SYS_RAWIO":       true,
	"CAP_SYS_PTRACE":      true,
	"CAP_SYS_CHROOT":      true,
	"CAP_SYS_BOOT":        true,
	"CAP_DAC_OVERRIDE":    true,
	"CAP_DAC_READ_SEARCH": true,
	"CAP_FOWNER":          true,
	"CAP_CHOWN":           true,
	"CAP_SETUID":          true,
	"CAP_SETGID":          true,
	"CAP_SETPCAP":         true,
	"CAP_MKNOD":           true,
	"CAP_BPF":             true,
	"CAP_MAC_ADMIN":       true,
	"CAP_MAC_OVERRIDE":    true,
	"CAP_NET_ADMIN":       true,
}

// deniedConfinedGroups are the supplementary groups a confined addon may not join: occulite, whose
// only member reaches the privilege helper's socket (/run/occulite/helper.sock) and so has every
// operation of the helper's policy - full root by proxy - and root, the GID-0 group, which is root
// itself. On the image root is the only GID-0 group; a GID-0 alias under another name would be an
// image change, and the drop-in never names such a group anyway (renderDropIn filters by this list).
var deniedConfinedGroups = map[string]bool{
	"occulite": true,
	"root":     true,
}

// DeniedConfinedCap reports whether a capability is root-equivalent and so refused for a confined
// addon (B-251). A root addon - one the user runs as *root (unsafe)* - is not checked: it has root.
func DeniedConfinedCap(name string) bool { return deniedConfinedCaps[name] }

// DeniedConfinedGroup reports whether a supplementary group is root-equivalent and so refused for a
// confined addon (B-251).
func DeniedConfinedGroup(name string) bool { return deniedConfinedGroups[name] }

// DeniedConfinedCaps and DeniedConfinedGroups are the two denylists, sorted, for the docs and the
// tests.
func DeniedConfinedCaps() []string   { return sortedKeys(deniedConfinedCaps) }
func DeniedConfinedGroups() []string { return sortedKeys(deniedConfinedGroups) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

var (
	idRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	capRe      = regexp.MustCompile(`^CAP_[A-Z_]{1,40}$`)
	groupRe    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	pathRe     = regexp.MustCompile(`^/[A-Za-z0-9_./-]{0,200}$`)
	relPathRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,200}$`)
	scopeRe    = regexp.MustCompile(`^[a-z]+(:[a-z]+)?$`)
	githubRe   = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
	versionRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*([-+][A-Za-z0-9.+-]+)?$`)
	settingsRe = regexp.MustCompile(`^/addons/[A-Za-z0-9][A-Za-z0-9._~-]*(?:/[A-Za-z0-9][A-Za-z0-9._~-]*)*/?(?:\?[A-Za-z0-9._~%&=+-]*)?$`)
	urlRe      = regexp.MustCompile(`^https?://[^\s"'<>]+$`)
)

// Parse reads and validates one manifest.
func Parse(b []byte) (*Manifest, error) {
	if len(b) > MaxSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", FileName, MaxSize)
	}
	// unknown keys are ignored: a manifest written for a newer occulited must still install on
	// an older one (the JSON schema is the strict reader, for the author's editor and the PR check)
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return &m, nil
}

// ParseFile is Parse on a file.
func ParseFile(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Validate refuses what the system could not apply or show. The shape checks here are the ones
// the format promises; the system's guard rails (the data directory fences, the scope names it
// knows, the interface ids) are applied again where the values are used.
func (m *Manifest) Validate() error {
	if m.Format != Format {
		return fmt.Errorf("format %d is not %d", m.Format, Format)
	}
	if !idRe.MatchString(m.ID) {
		return fmt.Errorf("id %q", m.ID)
	}
	if m.Name.In("en") == "" {
		return errors.New("name is empty")
	}
	if m.Version != "" && !versionRe.MatchString(m.Version) {
		return fmt.Errorf("version %q", m.Version)
	}
	if m.Homepage != "" && !urlRe.MatchString(m.Homepage) {
		return fmt.Errorf("homepage %q", m.Homepage)
	}
	if m.Changelog != "" && !urlRe.MatchString(m.Changelog) {
		return fmt.Errorf("changelog %q", m.Changelog)
	}
	if m.Release != nil {
		if !githubRe.MatchString(m.Release.GitHub) {
			return fmt.Errorf("release.github %q is not owner/repo", m.Release.GitHub)
		}
		if m.Release.Asset == "" && len(m.Release.Assets) == 0 && m.Release.Fallback == "" {
			return errors.New("release names no asset pattern")
		}
	}
	if m.Requires.Lite != "" && !versionRe.MatchString(m.Requires.Lite) {
		return fmt.Errorf("requires.lite %q", m.Requires.Lite)
	}
	for _, p := range []string{m.UI.Icon, m.UI.IconDark, m.UI.Logo, m.UI.LogoDark} {
		if p != "" && (!relPathRe.MatchString(p) || strings.Contains(p, "..")) {
			return fmt.Errorf("ui image path %q", p)
		}
	}
	if u := m.UI.SettingsURL; u != "" && (!settingsRe.MatchString(u) || strings.Contains(u, "..")) {
		return fmt.Errorf("ui.settings_url %q is not a path under /addons/", u)
	}
	return m.Runtime.validate()
}

func (rt *Runtime) validate() error {
	if rt == nil {
		return nil
	}
	for _, c := range rt.Capabilities {
		if !capRe.MatchString(c) {
			return fmt.Errorf("runtime.capabilities %q", c)
		}
		// B-251/D-119: a confined addon (the default; every addon that does not declare root)
		// may not hold a root-equivalent capability - the system would render it into the unit
		// unfiltered and the "confined" label would be a lie. A root addon (root: true) has root
		// already, so its capabilities are the user's *root (unsafe)* choice and not checked here.
		if !rt.Root && DeniedConfinedCap(c) {
			return fmt.Errorf("runtime.capabilities %q is root-equivalent and refused for a confined addon; declare \"root\": true to run as root (shown as unsafe), or drop it", c)
		}
	}
	for _, g := range rt.Groups {
		if !groupRe.MatchString(g) {
			return fmt.Errorf("runtime.groups %q", g)
		}
		if !rt.Root && DeniedConfinedGroup(g) {
			return fmt.Errorf("runtime.groups %q is root-equivalent (the privilege helper's group, or root) and refused for a confined addon", g)
		}
	}
	for _, p := range rt.Paths {
		if !pathRe.MatchString(p) || strings.Contains(p, "..") {
			return fmt.Errorf("runtime.paths %q", p)
		}
	}
	for _, d := range rt.DataDirs {
		if !pathRe.MatchString(d) || strings.Contains(d, "..") || !strings.HasPrefix(d, "/usr/local/") {
			return fmt.Errorf("runtime.data_dirs %q is not a directory under /usr/local/", d)
		}
	}
	for _, n := range rt.Ports {
		if n < 1 || n > 65535 {
			return fmt.Errorf("runtime.ports %d", n)
		}
	}
	for k := range rt.PortInfo {
		if n, err := strconv.Atoi(k); err != nil || !slices.Contains(rt.Ports, n) {
			return fmt.Errorf("runtime.port_info %q names no declared port", k)
		}
	}
	if rt.Start != "" && rt.Start != StartEarly {
		return fmt.Errorf("runtime.start %q is not %q", rt.Start, StartEarly)
	}
	for _, s := range rt.APIScopes {
		if !scopeRe.MatchString(s) {
			return fmt.Errorf("runtime.api_scopes %q", s)
		}
	}
	return nil
}

// NeedsRega says whether the addon needs the ReGa: only when its manifest says so.
func (m *Manifest) NeedsRega() bool { return m != nil && m.Requires.Rega }

// SupportsArch says whether the package exists for arch (uname -m); no list means every one.
func (m *Manifest) SupportsArch(arch string) bool {
	if m == nil || len(m.Requires.Architectures) == 0 {
		return true
	}
	return slices.Contains(m.Requires.Architectures, arch) || slices.Contains(m.Requires.Architectures, "any")
}

// FromArchive finds openccu-lite.json at the root of a package archive (a gzipped or plain tar)
// and parses it. ErrNoManifest when the archive has none - and, wrapped with the reason, when it
// is not a readable archive at all: the installer says what is wrong with such a package. A
// manifest that does not parse is an error of its own, so an installer can tell "no declaration"
// from "a broken one". The whole archive is read once - the file may be anywhere in the tar's order.
func FromArchive(r io.Reader) (*Manifest, error) {
	br := newPeekReader(r)
	head, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %v", ErrNoManifest, err)
	}
	var tr *tar.Reader
	if len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoManifest, err)
		}
		defer gz.Close()
		tr = tar.NewReader(gz)
	} else {
		tr = tar.NewReader(br)
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, ErrNoManifest
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoManifest, err)
		}
		name := strings.TrimPrefix(h.Name, "./")
		if name != FileName || h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size > MaxSize {
			return nil, fmt.Errorf("%s is larger than %d bytes", FileName, MaxSize)
		}
		b, err := io.ReadAll(io.LimitReader(tr, MaxSize+1))
		if err != nil {
			return nil, err
		}
		return Parse(b)
	}
}

// FromArchiveFile is FromArchive on a file.
func FromArchiveFile(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return FromArchive(f)
}

// peekReader is the two bytes of lookahead the gzip check needs, without pulling in bufio for a
// stream the tar reader consumes anyway.
type peekReader struct {
	r    io.Reader
	head []byte
}

func newPeekReader(r io.Reader) *peekReader { return &peekReader{r: r} }

func (p *peekReader) Peek(n int) ([]byte, error) {
	buf := make([]byte, n)
	got, err := io.ReadFull(p.r, buf)
	p.head = buf[:got]
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return p.head, err
	}
	return p.head, nil
}

func (p *peekReader) Read(b []byte) (int, error) {
	if len(p.head) > 0 {
		n := copy(b, p.head)
		p.head = p.head[n:]
		return n, nil
	}
	return p.r.Read(b)
}
