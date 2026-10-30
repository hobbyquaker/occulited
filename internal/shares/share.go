// Package shares is openccu-lite task 228's network shares: SMB and NFS shares the Storage page
// adds, each mounted by PID 1 on demand at netmount.Base/<id> (a .mount and an .automount unit the
// privilege helper renders from checked fields), with a state, a test, and the reads that must not
// hang. The journal's copies and the backups use a share by its id and a folder (the location
// picker); task 86's backup targets of kind NFS and SMB were the first such mounts, and the
// machinery here is theirs, lifted.
//
// The state on the userfs, under occulited's state directory (in every backup, like task 86's
// targets: a restored system mounts its shares again without anyone typing the password):
//
//   - shares.json - the shares (no secrets);
//   - shares/<id>/ (0700) - cifs.cred (an SMB share's user, password and domain; 0600), the file
//     the helper's rendered mount unit names by the id.
package shares

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/atomicfile"
	"github.com/hobbyquaker/occulited/internal/netmount"
)

// The kinds of share.
const (
	KindNFS  = netmount.KindNFS
	KindCIFS = netmount.KindCIFS
)

// Files and directories under the state directory.
const (
	ConfigFile = "shares.json"
	SecretsDir = "shares"
	CredFile   = "cifs.cred"
)

// MaxShares bounds the list.
const MaxShares = 16

// Share is one network share. Its id is the name the user gave it: the mount point
// (netmount.Base/<id>), the units' name and the location id (share:<id>/<folder>).
type Share struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Server string `json:"server"`
	// Path is the NFS export (/volume1/data) or the SMB share, optionally with a directory in it.
	Path    string `json:"path"`
	Version string `json:"version"`
	// Seal asks for SMB3 encryption (SMB only).
	Seal bool `json:"seal,omitempty"`
	// ReadOnly mounts it read-only; nosuid, nodev and noexec are always set.
	ReadOnly bool `json:"read_only"`
	// User and Domain are the SMB account; the password lives in cifs.cred and is never answered.
	User   string `json:"user,omitempty"`
	Domain string `json:"domain,omitempty"`
	// HasPassword is derived: the credentials file holds a password.
	HasPassword bool `json:"has_password"`
	// Password is write-only: accepted on POST and PUT, never stored here or answered.
	Password *string   `json:"password,omitempty"`
	Created  time.Time `json:"created,omitzero"`
}

// Where is the share's mount point.
func (s Share) Where() string { return netmount.Base + "/" + s.ID }

// Spec is the mount the helper renders.
func (s Share) Spec() netmount.Spec {
	return netmount.Spec{ID: s.ID, Kind: s.Kind, Server: s.Server, Path: s.Path, Version: s.Version, Seal: s.Seal, ReadOnly: s.ReadOnly, Share: true}
}

// Source is the share as a mount names it: server:/export or //server/share.
func (s Share) Source() string { return s.Spec().What() }

var (
	userRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@-]{0,63}$`)
	domainRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// ErrInvalid wraps every validation error, so the API answers 400.
var ErrInvalid = errors.New("invalid")

// ErrNotFound: no share of that id.
var ErrNotFound = errors.New("no such share")

// ErrExists: the id is taken (another share, or a backup target's mount).
var ErrExists = errors.New("the name is taken")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Validate checks a share's settings.
func (s Share) Validate() error {
	if !netmount.ValidID(s.ID) {
		return invalid("id: a name of letters and digits, starting with a letter, lower case, at most 16 (it is the mount point %s/<name>)", netmount.Base)
	}
	if s.Kind != KindNFS && s.Kind != KindCIFS {
		return invalid("kind %q: nfs or cifs", s.Kind)
	}
	if err := s.Spec().Validate(); err != nil {
		return invalid("%v", err)
	}
	if s.Kind == KindCIFS {
		if !userRe.MatchString(s.User) {
			return invalid("user: the account on the server")
		}
		if s.Domain != "" && !domainRe.MatchString(s.Domain) {
			return invalid("domain")
		}
		if s.Password != nil && (strings.ContainsAny(*s.Password, "\n\r\x00") || len(*s.Password) > 256) {
			return invalid("password: one line")
		}
	} else if s.User != "" || s.Domain != "" || s.Password != nil {
		return invalid("user, domain and password are for SMB shares; NFS checks the address")
	}
	return nil
}

// Store keeps the shares. Dir is occulited's state directory.
type Store struct {
	Dir string
	// Taken says whether an id is used by another mount under netmount.Base (a task-86 backup
	// target of kind NFS or SMB); nil = none.
	Taken func(id string) bool

	mu sync.Mutex
}

type config struct {
	Shares []Share `json:"shares"`
}

func (st *Store) load() (config, error) {
	var c config
	b, err := os.ReadFile(filepath.Join(st.Dir, ConfigFile))
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

func (st *Store) save(c config) error {
	for i := range c.Shares {
		// derived and write-only fields never land in the file
		c.Shares[i].Password = nil
		c.Shares[i].HasPassword = false
	}
	if c.Shares == nil {
		c.Shares = []Share{}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(st.Dir, ConfigFile), b, 0o600)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, b, mode)
}

// SecretDir is shares/<id>.
func (st *Store) SecretDir(id string) string { return filepath.Join(st.Dir, SecretsDir, id) }

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (st *Store) derive(s Share) Share {
	s.Password = nil
	s.HasPassword = s.Kind == KindCIFS && credValue(readTrim(filepath.Join(st.SecretDir(s.ID), CredFile)), "password") != ""
	return s
}

// List answers every share, in the order they were added.
func (st *Store) List() ([]Share, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	c, err := st.load()
	if err != nil {
		return nil, err
	}
	out := make([]Share, 0, len(c.Shares))
	for _, s := range c.Shares {
		out = append(out, st.derive(s))
	}
	return out, nil
}

// Get answers one share.
func (st *Store) Get(id string) (Share, bool, error) {
	list, err := st.List()
	if err != nil {
		return Share{}, false, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, true, nil
		}
	}
	return Share{}, false, nil
}

// Create adds a share; its id must be new.
func (st *Store) Create(s Share) (Share, error) {
	return st.put(s, true)
}

// Update changes a share's settings; the id and the kind stay.
func (st *Store) Update(s Share) (Share, error) {
	return st.put(s, false)
}

func (st *Store) put(s Share, create bool) (Share, error) {
	if err := s.Validate(); err != nil {
		return Share{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	c, err := st.load()
	if err != nil {
		return Share{}, err
	}
	idx := -1
	for i := range c.Shares {
		if c.Shares[i].ID == s.ID {
			idx = i
		}
	}
	switch {
	case create && idx >= 0:
		return Share{}, fmt.Errorf("%w: a share named %s exists", ErrExists, s.ID)
	case create && st.Taken != nil && st.Taken(s.ID):
		return Share{}, fmt.Errorf("%w: %s/%s is a backup target's", ErrExists, netmount.Base, s.ID)
	case create && len(c.Shares) >= MaxShares:
		return Share{}, invalid("at most %d shares", MaxShares)
	case !create && idx < 0:
		return Share{}, ErrNotFound
	case !create && c.Shares[idx].Kind != s.Kind:
		return Share{}, invalid("the kind of a share cannot change (%s)", c.Shares[idx].Kind)
	}
	if create {
		if s.Created.IsZero() {
			s.Created = time.Now().UTC()
		}
		c.Shares = append(c.Shares, s)
	} else {
		s.Created = c.Shares[idx].Created
		c.Shares[idx] = s
	}
	if s.Kind == KindCIFS {
		dir := st.SecretDir(s.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Share{}, err
		}
		pw := credValue(readTrim(filepath.Join(dir, CredFile)), "password") // unchanged unless given
		if s.Password != nil {
			pw = *s.Password
		}
		// the file always exists for an SMB share: the mount unit names it, and a guest account
		// has an empty password
		if err := WriteCred(dir, s.User, pw, s.Domain); err != nil {
			return Share{}, err
		}
	} else if !create {
		_ = os.RemoveAll(st.SecretDir(s.ID))
	}
	if err := st.save(c); err != nil {
		return Share{}, err
	}
	return st.derive(s), nil
}

// WriteCred writes mount.cifs's credentials file: the password on a line of its own, never on a
// command line or in the unit.
func WriteCred(dir, user, password, domain string) error {
	var b strings.Builder
	b.WriteString("username=" + user + "\n")
	b.WriteString("password=" + password + "\n")
	if domain != "" {
		b.WriteString("domain=" + domain + "\n")
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

// Delete removes a share's configuration and secrets - never anything on the share.
func (st *Store) Delete(id string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	c, err := st.load()
	if err != nil {
		return err
	}
	kept := c.Shares[:0]
	found := false
	for _, s := range c.Shares {
		if s.ID == id {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		return ErrNotFound
	}
	c.Shares = kept
	if err := st.save(c); err != nil {
		return err
	}
	if netmount.ValidID(id) {
		return os.RemoveAll(st.SecretDir(id))
	}
	return nil
}
