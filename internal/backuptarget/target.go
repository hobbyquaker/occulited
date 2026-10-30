// Package backuptarget is openccu-lite task 86: the places a backup goes besides the browser - the
// USB directory, NFS and CIFS shares (mounted on demand by PID 1 through the helper's units) and SSH
// servers (an SFTP upload from occulited). Here are the targets' configuration and secrets, their
// states, the SFTP client, the reads a hung share must not block, and the nightly pipeline that the
// fork's units run as `occulited -backup create|deliver <instance>`.
//
// The state on the userfs, under occulited's state directory (in every backup, D-80: a restored
// system reconnects without anyone typing the credentials again):
//
//   - backup-targets.json - the targets (no secrets);
//   - backup-targets/<id>/ (0700) - id_ed25519 and host_key (SFTP), cifs.cred (CIFS), last.json
//     (the last delivery's result, written by the process that delivered).
package backuptarget

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/atomicfile"
	"github.com/hobbyquaker/occulited/internal/location"
	"github.com/hobbyquaker/occulited/internal/netmount"
)

// The kinds of target.
const (
	KindDirectory = "directory"
	KindNFS       = netmount.KindNFS
	KindCIFS      = netmount.KindCIFS
	KindSFTP      = "sftp"
	// KindShare is a folder on a share of System → Storage (task 228): the share is defined once
	// there and mounted by the shares' manager; the target names it and a folder. Task 86's NFS
	// and SMB targets become this kind at the first start (MigrateMounts).
	KindShare = "share"
)

// DirectoryID is the USB directory target's fixed id: upstream's CronBackupPath and
// CronBackupMaxBackups markers are its storage, so GET/PUT /backup/schedule and a restored
// OpenCCU backup keep working.
const DirectoryID = "directory"

// Files and directories under the state directory.
const (
	ConfigFile = "backup-targets.json"
	SecretsDir = "backup-targets"
	KeyFile    = "id_ed25519"
	HostKey    = "host_key"
	CredFile   = "cifs.cred"
	LastFile   = "last.json"
)

// The markers the directory target shares with upstream's cronBackup.sh.
const (
	MarkerPath      = "/etc/config/CronBackupPath"
	MarkerMax       = "/etc/config/CronBackupMaxBackups"
	MarkerNoNightly = "/etc/config/NoCronBackup"
	// DefaultDirectory is cronBackup.sh's default, shown when no path was set.
	DefaultDirectory = "/media/usb0/backup"
	// DefaultMaxBackups is cronBackup.sh's default.
	DefaultMaxBackups = 30
)

// Target is one backup target.
type Target struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	// MaxBackups is how many of this system's backups stay on the target, 0 = all.
	MaxBackups int `json:"max_backups"`
	// Subdir is the per-system directory inside the share or server path (default: the host
	// name), so several systems share one export. Not for the directory target.
	Subdir string `json:"subdir"`
	// Encrypt: backups to this target are encrypted when a recovery key exists (task 91).
	Encrypt bool `json:"encrypt"`
	// AppendOnly: no retention and no delete attempts on this target (D-80); the server prunes.
	AppendOnly bool `json:"append_only"`
	// Created is when the target was added (the too-old warning's start for one that never ran).
	Created   time.Time  `json:"created,omitzero"`
	Directory *Directory `json:"directory,omitempty"`
	Share     *ShareRef  `json:"share,omitempty"`
	NFS       *NFS       `json:"nfs,omitempty"`
	CIFS      *CIFS      `json:"cifs,omitempty"`
	SFTP      *SFTP      `json:"sftp,omitempty"`
}

// Directory is the directory target: its path, and - chosen with task 228's location picker -
// the location it was chosen as (userfs:backup, usb:<label>/<folder>). With a location the path is
// derived from it each time (a stick in another port, /media/usb2 today and /media/usb1 tomorrow);
// the path alone is what upstream's CronBackupPath says (an OpenCCU backup, an older setting).
type Directory struct {
	Path     string `json:"path"`
	Location string `json:"location,omitempty"`
}

// ShareRef is a share target's share and the folder on it ("" = the share's root).
type ShareRef struct {
	ID     string `json:"id"`
	Folder string `json:"folder"`
}

// NFS is an NFS export.
type NFS struct {
	Server  string `json:"server"`
	Export  string `json:"export"`
	Version string `json:"version"`
}

// CIFS is an SMB share; the password lives in cifs.cred and is never answered.
type CIFS struct {
	Server  string `json:"server"`
	Share   string `json:"share"`
	Version string `json:"version"`
	Seal    bool   `json:"seal"`
	User    string `json:"user"`
	Domain  string `json:"domain,omitempty"`
	// HasPassword is derived: a credentials file with a password exists.
	HasPassword bool `json:"has_password"`
	// Password is write-only: accepted on POST and PUT, never stored here or answered.
	Password *string `json:"password,omitempty"`
}

// SFTP is an SSH server the backup is uploaded to.
type SFTP struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	// Path is the directory on the server (relative to the user's home when not absolute).
	Path string `json:"path"`
	// PublicKey is derived: the target's own key, as an authorized_keys line.
	PublicKey string `json:"public_key,omitempty"`
	// HostKey is derived: the server's key the user trusted.
	HostKey *HostKeyInfo `json:"host_key,omitempty"`
}

// HostKeyInfo is a server key's type and SHA-256 fingerprint.
type HostKeyInfo struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
}

// Mount is a mount target's spec for the helper.
func (t Target) Mount() (netmount.Spec, bool) {
	switch {
	case t.Kind == KindNFS && t.NFS != nil:
		return netmount.Spec{ID: t.ID, Kind: KindNFS, Server: t.NFS.Server, Path: t.NFS.Export, Version: t.NFS.Version}, true
	case t.Kind == KindCIFS && t.CIFS != nil:
		return netmount.Spec{ID: t.ID, Kind: KindCIFS, Server: t.CIFS.Server, Path: t.CIFS.Share, Version: t.CIFS.Version, Seal: t.CIFS.Seal}, true
	}
	return netmount.Spec{}, false
}

// IsMount says whether the target is on a share PID 1 mounts.
func (t Target) IsMount() bool { return t.Kind == KindNFS || t.Kind == KindCIFS || t.Kind == KindShare }

// MountID is the id of the mount under netmount.Base the target is on: the share's name, or a
// task-86 NFS/SMB target's own id; "" for the other kinds.
func (t Target) MountID() string {
	switch {
	case t.Kind == KindShare && t.Share != nil:
		return t.Share.ID
	case t.Kind == KindNFS || t.Kind == KindCIFS:
		return t.ID
	}
	return ""
}

// IsLocal says whether root writes the target as a directory (the directory and the mounts).
func (t Target) IsLocal() bool { return t.Kind == KindDirectory || t.IsMount() }

// Dir is where the backups go on a directory or mount target: the path, or the mount point plus
// the subdirectory.
func (t Target) Dir() string {
	switch {
	case t.Kind == KindDirectory && t.Directory != nil:
		return t.Directory.Path
	case t.Kind == KindShare && t.Share != nil:
		return location.SharePath(t.Share.ID, t.Share.Folder)
	case t.IsMount():
		if t.Subdir == "" {
			return netmount.Base + "/" + t.ID
		}
		return netmount.Base + "/" + t.ID + "/" + t.Subdir
	}
	return ""
}

// RemoteDir is the directory on an SFTP server.
func (t Target) RemoteDir() string {
	if t.SFTP == nil {
		return ""
	}
	base := t.SFTP.Path
	if base == "" {
		base = "."
	}
	if t.Subdir == "" {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/" + t.Subdir
}

var (
	subdirRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
	userRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@-]{0,63}$`)
	domainRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// ErrInvalid wraps every validation error, so the API answers 400.
var ErrInvalid = errors.New("invalid")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// printable: no control characters, bounded.
func printable(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Validate checks a target's settings (the secrets separately).
func (t Target) Validate() error {
	if t.ID != DirectoryID && !netmount.ValidID(t.ID) {
		return invalid("id %q", t.ID)
	}
	if strings.TrimSpace(t.Name) == "" || !printable(t.Name, 64) {
		return invalid("name: 1 to 64 printable characters")
	}
	if t.MaxBackups < 0 || t.MaxBackups > 1000 {
		return invalid("max_backups: 0 (keep all) to 1000")
	}
	if t.Subdir != "" && (!subdirRe.MatchString(t.Subdir) || t.Subdir == "." || t.Subdir == "..") {
		return invalid("subdir: one directory name (letters, digits, . _ -)")
	}
	set := 0
	for _, p := range []bool{t.Directory != nil, t.NFS != nil, t.CIFS != nil, t.SFTP != nil, t.Share != nil} {
		if p {
			set++
		}
	}
	if set != 1 {
		return invalid("exactly one of directory, share, sftp (nfs, cifs: the older kinds)")
	}
	switch t.Kind {
	case KindDirectory:
		if t.ID != DirectoryID || t.Directory == nil {
			return invalid("the directory target is the one with id %q", DirectoryID)
		}
		if l := t.Directory.Location; l != "" {
			loc, err := location.Parse(l)
			if err != nil {
				return invalid("directory.location: %v", err)
			}
			if loc.Kind == location.KindShare {
				return invalid("directory.location: a share is a target of kind share")
			}
			if err := location.Check(location.UseBackup, loc); err != nil {
				return invalid("directory.location: %v", err)
			}
		}
		p := t.Directory.Path
		if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || p == "/usr/local" || !printable(p, 1024) {
			return invalid("directory.path: an absolute directory path (a USB stick under /media), not / or /usr/local")
		}
	case KindNFS, KindCIFS:
		if t.ID == DirectoryID {
			return invalid("id %q is the directory target's", DirectoryID)
		}
		spec, ok := t.Mount()
		if !ok {
			return invalid("%s settings missing", t.Kind)
		}
		if err := spec.Validate(); err != nil {
			return invalid("%s.%v", t.Kind, err)
		}
		if t.Kind == KindCIFS {
			if !userRe.MatchString(t.CIFS.User) {
				return invalid("cifs.user: the account on the server")
			}
			if t.CIFS.Domain != "" && !domainRe.MatchString(t.CIFS.Domain) {
				return invalid("cifs.domain")
			}
			if t.CIFS.Password != nil && (strings.ContainsAny(*t.CIFS.Password, "\n\r\x00") || len(*t.CIFS.Password) > 256) {
				return invalid("cifs.password: one line")
			}
		}
	case KindShare:
		if t.ID == DirectoryID || t.Share == nil {
			return invalid("share settings missing")
		}
		if !netmount.ValidID(t.Share.ID) {
			return invalid("share.id: the name of a share of System → Storage")
		}
		if !location.ValidFolder(t.Share.Folder, true) {
			return invalid("share.folder: up to %d levels of letters, digits, . _ -", location.MaxDepth)
		}
		if t.Subdir != "" {
			return invalid("subdir: a share target's folder is share.folder")
		}
	case KindSFTP:
		if t.ID == DirectoryID || t.SFTP == nil {
			return invalid("sftp settings missing")
		}
		s := t.SFTP
		if !netmount.ValidHost(s.Host) {
			return invalid("sftp.host: a host name or an IP address")
		}
		if s.Port < 1 || s.Port > 65535 {
			return invalid("sftp.port: 1 to 65535")
		}
		if !userRe.MatchString(s.User) {
			return invalid("sftp.user: the account on the server")
		}
		if s.Path != "" && (!printable(s.Path, 1024) || strings.Contains(s.Path, "..")) {
			return invalid("sftp.path: a directory on the server, without ..")
		}
	default:
		return invalid("kind %q: directory, share or sftp (nfs, cifs: the older kinds)", t.Kind)
	}
	return nil
}

// config is backup-targets.json.
type config struct {
	Targets []Target `json:"targets"`
	// Directory keeps the directory target's own switches; its path and count are the markers.
	Directory *directorySettings `json:"directory,omitempty"`
}

type directorySettings struct {
	// Location is the directory as task 228's picker chose it; the path is derived from it.
	Location   string `json:"location,omitempty"`
	Disabled   bool   `json:"disabled,omitempty"`
	Encrypt    *bool  `json:"encrypt,omitempty"`
	AppendOnly bool   `json:"append_only,omitempty"`
	Name       string `json:"name,omitempty"`
}

// Store keeps the targets. Dir is occulited's state directory, Root the filesystem root the
// markers are under ("/" on a system).
type Store struct {
	Dir  string
	Root string
	// WriteMarker and RemoveMarker change the markers under /etc/config (root's, through the
	// helper in the daemon); nil = direct file operations (tests, root).
	WriteMarker  func(path string, data []byte) error
	RemoveMarker func(path string) error
	// USBMount answers where the USB stick with a label (udev's ID_FS_LABEL) is mounted now
	// (task 228: a directory target chosen as usb:<label>/<folder>); nil = never.
	USBMount func(label string) (string, bool)
	// ShareInfo answers whether a share of System → Storage exists and is read-only; nil = none.
	ShareInfo func(id string) (exists, readOnly bool)

	mu sync.Mutex
}

func (s *Store) join(p string) string {
	if s.Root == "" || s.Root == "/" {
		return p
	}
	return filepath.Join(s.Root, p)
}

func (s *Store) load() (config, error) {
	var c config
	b, err := os.ReadFile(filepath.Join(s.Dir, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return config{}, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	return c, nil
}

func (s *Store) save(c config) error {
	for i := range c.Targets {
		// derived and write-only fields never land in the file
		if c.Targets[i].CIFS != nil {
			c.Targets[i].CIFS.Password = nil
			c.Targets[i].CIFS.HasPassword = false
		}
		if c.Targets[i].SFTP != nil {
			c.Targets[i].SFTP.PublicKey = ""
			c.Targets[i].SFTP.HostKey = nil
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.Dir, ConfigFile), b, 0o600)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, b, mode)
}

// SecretDir is backup-targets/<id>.
func (s *Store) SecretDir(id string) string { return filepath.Join(s.Dir, SecretsDir, id) }

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Nightly says whether the nightly run is on (no NoCronBackup marker).
func (s *Store) Nightly() bool {
	_, err := os.Stat(s.join(MarkerNoNightly))
	return errors.Is(err, os.ErrNotExist)
}

// SetNightly switches the nightly run.
func (s *Store) SetNightly(on bool) error {
	if on {
		return s.removeMarker(MarkerNoNightly)
	}
	return s.writeMarker(MarkerNoNightly, nil)
}

func (s *Store) writeMarker(path string, data []byte) error {
	if s.WriteMarker != nil {
		return s.WriteMarker(s.join(path), data)
	}
	if err := os.MkdirAll(filepath.Dir(s.join(path)), 0o755); err != nil {
		return err
	}
	return writeAtomic(s.join(path), data, 0o644)
}

func (s *Store) removeMarker(path string) error {
	if s.RemoveMarker != nil {
		return s.RemoveMarker(s.join(path))
	}
	if err := os.Remove(s.join(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// directory is the directory target as the markers and the settings say; ok false when no path is
// set (the target is not configured).
func (s *Store) directory(c config) (Target, bool) {
	path := readTrim(s.join(MarkerPath))
	max := DefaultMaxBackups
	if n, err := strconv.Atoi(readTrim(s.join(MarkerMax))); err == nil && n >= 0 {
		max = n
	}
	t := Target{ID: DirectoryID, Kind: KindDirectory, Name: "USB", Enabled: path != "", MaxBackups: max, Encrypt: true,
		Directory: &Directory{Path: path}}
	if d := c.Directory; d != nil && d.Location != "" && path != "" {
		t.Directory.Location = d.Location
		t.Directory.Path = s.LocationPath(d.Location, path)
	}
	if d := c.Directory; d != nil {
		t.Enabled = t.Enabled && !d.Disabled
		if d.Encrypt != nil {
			t.Encrypt = *d.Encrypt
		}
		t.AppendOnly = d.AppendOnly
		if d.Name != "" {
			t.Name = d.Name
		}
	}
	return t, path != ""
}

// MissingStickDir is where a directory target on a USB stick that is not plugged in points: under
// /media's tmpfs, where nothing is mounted - checkDir calls that no-medium, as it does /media/usb0
// without a stick.
const MissingStickDir = "/media/.usb-not-plugged-in"

// LocationPath is a directory location's path now: the userfs folder, or the folder on the stick
// with the label where it is mounted now (MissingStickDir/<folder> while it is not); fallback when
// the location cannot be read.
func (s *Store) LocationPath(loc, fallback string) string {
	l, err := location.Parse(loc)
	if err != nil {
		return fallback
	}
	switch l.Kind {
	case location.KindUserfs:
		return location.UserfsPath(l.Folder)
	case location.KindUSB:
		if s.USBMount != nil {
			if mp, ok := s.USBMount(l.Name); ok {
				return mp + "/" + l.Folder
			}
		}
		return MissingStickDir + "/" + l.Folder
	}
	return fallback
}

// List returns every configured target, the directory target first (when a path is set), with
// the derived fields filled in.
func (s *Store) List() ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.load()
	if err != nil {
		return nil, err
	}
	out := []Target{}
	if d, ok := s.directory(c); ok {
		out = append(out, d)
	}
	for _, t := range c.Targets {
		out = append(out, s.derive(t))
	}
	return out, nil
}

// Get returns one target.
func (s *Store) Get(id string) (Target, bool, error) {
	list, err := s.List()
	if err != nil {
		return Target{}, false, err
	}
	for _, t := range list {
		if t.ID == id {
			return t, true, nil
		}
	}
	return Target{}, false, nil
}

// derive fills the secret-derived fields in.
func (s *Store) derive(t Target) Target {
	dir := s.SecretDir(t.ID)
	switch {
	case t.CIFS != nil:
		c := *t.CIFS
		c.Password = nil
		c.HasPassword = strings.Contains(readTrim(filepath.Join(dir, CredFile)), "password=")
		t.CIFS = &c
	case t.SFTP != nil:
		sf := *t.SFTP
		sf.PublicKey, _ = PublicKeyLine(dir)
		sf.HostKey = TrustedHostKey(dir)
		t.SFTP = &sf
	}
	return t
}

// NewID makes a target id: "t" and seven random letters and digits.
func NewID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 7)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "t" + string(b)
}

// ErrNotFound: no target of that id.
var ErrNotFound = errors.New("no such backup target")

// Put creates (t.ID == "") or replaces a network target, or sets the directory target; the secrets
// in t (a CIFS password) are written beside it. It answers the target as stored.
func (s *Store) Put(t Target) (Target, error) {
	if t.Kind == KindDirectory {
		t.ID = DirectoryID
	} else if t.ID == "" {
		t.ID = NewID()
	}
	if t.SFTP != nil && t.SFTP.Port == 0 {
		t.SFTP.Port = 22
	}
	if t.Kind == KindDirectory && t.Directory != nil && t.Directory.Location != "" {
		// the path follows the location; the marker keeps it for upstream's tools
		t.Directory.Path = s.LocationPath(t.Directory.Location, t.Directory.Path)
	}
	if err := t.Validate(); err != nil {
		return Target{}, err
	}
	if t.Kind == KindShare && s.ShareInfo != nil {
		switch exists, ro := s.ShareInfo(t.Share.ID); {
		case !exists:
			return Target{}, invalid("share.id: there is no share %q on System → Storage", t.Share.ID)
		case ro:
			return Target{}, invalid("share.id: the share %q is mounted read-only", t.Share.ID)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.load()
	if err != nil {
		return Target{}, err
	}
	if t.Kind == KindDirectory {
		if err := s.writeMarker(MarkerPath, []byte(t.Directory.Path+"\n")); err != nil {
			return Target{}, err
		}
		if err := s.writeMarker(MarkerMax, []byte(strconv.Itoa(t.MaxBackups)+"\n")); err != nil {
			return Target{}, err
		}
		enc := t.Encrypt
		c.Directory = &directorySettings{Location: t.Directory.Location, Disabled: !t.Enabled, Encrypt: &enc, AppendOnly: t.AppendOnly, Name: t.Name}
		if err := s.save(c); err != nil {
			return Target{}, err
		}
		d, _ := s.directory(c)
		return d, nil
	}
	found := false
	for i := range c.Targets {
		if c.Targets[i].ID == t.ID {
			if c.Targets[i].Kind != t.Kind {
				return Target{}, invalid("kind cannot change (%s)", c.Targets[i].Kind)
			}
			t.Created = c.Targets[i].Created
			c.Targets[i] = t
			found = true
		}
	}
	if !found {
		if len(c.Targets) >= 16 {
			return Target{}, invalid("at most 16 targets")
		}
		if t.Created.IsZero() {
			t.Created = time.Now().UTC()
		}
		c.Targets = append(c.Targets, t)
	}
	dir := s.SecretDir(t.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Target{}, err
	}
	if t.CIFS != nil && t.CIFS.Password != nil {
		if err := writeCred(dir, t.CIFS); err != nil {
			return Target{}, err
		}
	} else if t.CIFS != nil {
		// user or domain changed: the file keeps the password it has
		if old := readTrim(filepath.Join(dir, CredFile)); old != "" {
			pw := credValue(old, "password")
			cc := *t.CIFS
			cc.Password = &pw
			if err := writeCred(dir, &cc); err != nil {
				return Target{}, err
			}
		}
	}
	if err := s.save(c); err != nil {
		return Target{}, err
	}
	return s.derive(t), nil
}

// writeCred writes mount.cifs's credentials file: the password on a line of its own, never on a
// command line or in the unit.
func writeCred(dir string, c *CIFS) error {
	var b strings.Builder
	b.WriteString("username=" + c.User + "\n")
	b.WriteString("password=" + *c.Password + "\n")
	if c.Domain != "" {
		b.WriteString("domain=" + c.Domain + "\n")
	}
	return writeAtomic(filepath.Join(dir, CredFile), []byte(b.String()), 0o600)
}

func credValue(text, key string) string {
	for _, l := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	return ""
}

// Delete removes a target's configuration and secrets - never a file on the target. The
// directory target loses its path marker.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.load()
	if err != nil {
		return err
	}
	if id == DirectoryID {
		if err := s.removeMarker(MarkerPath); err != nil {
			return err
		}
		c.Directory = nil
		_ = os.RemoveAll(s.SecretDir(DirectoryID))
		return s.save(c)
	}
	kept := c.Targets[:0]
	found := false
	for _, t := range c.Targets {
		if t.ID == id {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return ErrNotFound
	}
	c.Targets = kept
	if err := s.save(c); err != nil {
		return err
	}
	if netmount.ValidID(id) {
		return os.RemoveAll(s.SecretDir(id))
	}
	return nil
}
