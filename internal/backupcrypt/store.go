package backupcrypt

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/hobbyquaker/occulited/internal/atomicfile"
)

// The state on the userfs, under occulited's state directory:
//
//   - backup-encryption.json - the recovery recipient (public), its predecessors and the switch.
//     In every backup, so a restored system keeps encrypting to the same recovery key.
//   - backup-key/ - carries .nobackup, so createBackup.sh's --exclude-tag=.nobackup leaves it out
//     of every backup (and the recovery system's create_backup.cgi does the same):
//     box-identity.txt, the system's own age identity, 0600; created.json, the SHA-256 of every
//     backup this system handed out, so a restore can say "this system made this file".
const (
	StateFile   = "backup-encryption.json"
	KeyDir      = "backup-key"
	IdentityTag = ".nobackup"
	// IdentityFile is the system's age identity, inside KeyDir.
	IdentityFile = "box-identity.txt"
	CreatedFile  = "created.json"
	// PendingTTL is how long a started setup waits for its confirmation.
	PendingTTL = 30 * time.Minute
	// maxCreated bounds created.json.
	maxCreated = 500
)

// Key is one recovery recipient. The recipient string never leaves the daemon (the API shows
// fingerprints): a recipient lets anyone encrypt a planted backup to this system.
type Key struct {
	Recipient   string    `json:"recipient"`
	Fingerprint string    `json:"fingerprint"`
	Created     time.Time `json:"created"`
	Retired     time.Time `json:"retired,omitempty"`
}

// State is backup-encryption.json.
type State struct {
	Enabled  bool  `json:"enabled"`
	Recovery *Key  `json:"recovery,omitempty"`
	Previous []Key `json:"previous,omitempty"`
}

// KeyView is a Key for the API: no recipient.
type KeyView struct {
	Fingerprint string     `json:"fingerprint"`
	Created     time.Time  `json:"created"`
	Retired     *time.Time `json:"retired,omitempty"`
}

// View is GET /backup/encryption.
type View struct {
	Enabled  bool      `json:"enabled"`
	Recovery *KeyView  `json:"recovery"`
	Previous []KeyView `json:"previous"`
	Box      *KeyView  `json:"box"`
}

// Created is one entry of created.json.
type Created struct {
	SHA256 string    `json:"sha256"`
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Time   time.Time `json:"time"`
	// Encrypted says whether the file handed out was the .sbk.age or the plain .sbk.
	Encrypted bool `json:"encrypted"`
}

// Store keeps the state. Dir is occulited's state directory.
type Store struct {
	Dir string
	// Now is the clock; nil = time.Now.
	Now func() time.Time

	mu      sync.Mutex
	pending map[string]pending
}

type pending struct {
	recipient string
	code      string // the code when the system generated it, shown once, else ""
	expires   time.Time
}

var (
	// ErrNoRecoveryKey: encryption cannot be switched on without a recovery key - such a backup
	// would die with the system.
	ErrNoRecoveryKey = errors.New("no recovery key: set one up before switching encryption on")
	// ErrPendingUnknown: the confirmation named no started setup (or one older than PendingTTL).
	ErrPendingUnknown = errors.New("no such pending recovery key - start again")
)

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Load reads the state; a missing file is the empty state, an unreadable one an error.
func (s *Store) Load() (State, error) {
	var st State
	b, err := os.ReadFile(filepath.Join(s.Dir, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("%s: %w", StateFile, err)
	}
	return st, nil
}

func (s *Store) save(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.Dir, StateFile), b, 0o600)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return atomicfile.Write(path, b, mode)
}

// View is the state for the API.
func (s *Store) View() (View, error) {
	st, err := s.Load()
	if err != nil {
		return View{}, err
	}
	v := View{Enabled: st.Enabled, Previous: []KeyView{}}
	if st.Recovery != nil {
		v.Recovery = &KeyView{Fingerprint: st.Recovery.Fingerprint, Created: st.Recovery.Created}
	}
	for _, k := range st.Previous {
		r := k.Retired
		v.Previous = append(v.Previous, KeyView{Fingerprint: k.Fingerprint, Created: k.Created, Retired: &r})
	}
	if id, created, ok := s.BoxIdentity(); ok {
		v.Box = &KeyView{Fingerprint: Fingerprint(id.Recipient().String()), Created: created}
	}
	return v, nil
}

// Enabled says whether backups handed out are encrypted: the switch, and a recovery key.
func (s *Store) Enabled() bool {
	st, err := s.Load()
	return err == nil && st.Enabled && st.Recovery != nil
}

// Recipients are what a backup is encrypted to: the system's identity (made if missing) and the
// recovery key. Never the system alone (ErrNoRecoveryKey).
func (s *Store) Recipients() (box, recovery string, meta MetaRecipient, err error) {
	st, err := s.Load()
	if err != nil {
		return "", "", meta, err
	}
	if st.Recovery == nil {
		return "", "", meta, ErrNoRecoveryKey
	}
	id, err := s.EnsureBoxIdentity()
	if err != nil {
		return "", "", meta, err
	}
	box = id.Recipient().String()
	return box, st.Recovery.Recipient, MetaRecipient{BoxFingerprint: Fingerprint(box), RecoveryFingerprint: st.Recovery.Fingerprint}, nil
}

// BoxIdentity reads the system's own identity; ok false when there is none.
func (s *Store) BoxIdentity() (id *age.X25519Identity, created time.Time, ok bool) {
	path := filepath.Join(s.Dir, KeyDir, IdentityFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	id, err = age.ParseX25519Identity(string(trimSpace(b)))
	if err != nil {
		return nil, time.Time{}, false
	}
	if st, err := os.Stat(path); err == nil {
		created = st.ModTime()
	}
	return id, created, true
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

// EnsureBoxIdentity returns the system's identity, generating it when there is none. The
// .nobackup tag is written (again) before the identity, every time: the identity must never be
// in a backup.
func (s *Store) EnsureBoxIdentity() (*age.X25519Identity, error) {
	if err := s.ensureTag(); err != nil {
		return nil, err
	}
	if id, _, ok := s.BoxIdentity(); ok {
		return id, nil
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	if err := s.writeIdentity(id.String()); err != nil {
		return nil, err
	}
	return id, nil
}

func (s *Store) ensureTag() error {
	dir := filepath.Join(s.Dir, KeyDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tag := filepath.Join(dir, IdentityTag)
	if _, err := os.Stat(tag); errors.Is(err, os.ErrNotExist) {
		return os.WriteFile(tag, nil, 0o600)
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Store) writeIdentity(identity string) error {
	return writeAtomic(filepath.Join(s.Dir, KeyDir, IdentityFile), []byte(identity+"\n"), 0o600)
}

// ExportBoxIdentity is the identity as bytes for the carry-over across a restore (the caller
// puts it where the restore does not reach) and when it was created; nil when there is none.
func (s *Store) ExportBoxIdentity() ([]byte, time.Time) {
	id, created, ok := s.BoxIdentity()
	if !ok {
		return nil, time.Time{}
	}
	return []byte(id.String() + "\n"), created
}

// AdoptBoxIdentity takes a carried identity when the system has none of its own (after a restore
// wiped the key directory). With an identity of its own nothing changes; adopted says which.
// created, when set, becomes the adopted identity's creation time (its file's mtime, what
// BoxIdentity reports): the identity is the one made back then, not a new one of the restore
// boot - whose clock, before NTP, may be months off (openccu-lite B-194).
func (s *Store) AdoptBoxIdentity(data []byte, created time.Time) (adopted bool, err error) {
	if _, _, ok := s.BoxIdentity(); ok {
		return false, nil
	}
	id, err := age.ParseX25519Identity(string(trimSpace(data)))
	if err != nil {
		return false, fmt.Errorf("carried identity: %w", err)
	}
	if err := s.ensureTag(); err != nil {
		return false, err
	}
	if err := s.writeIdentity(id.String()); err != nil {
		return true, err
	}
	if !created.IsZero() {
		if err := os.Chtimes(filepath.Join(s.Dir, KeyDir, IdentityFile), created, created); err != nil {
			return true, fmt.Errorf("carried identity's creation time: %w", err)
		}
	}
	return true, nil
}

// BeginRecovery starts a setup or rotation with a recipient the browser made (age1…). The
// recipient waits in memory for Confirm; nothing is saved yet.
func (s *Store) BeginRecovery(recipient string) (pendingID, fingerprint string, err error) {
	r, err := ParseRecipient(recipient)
	if err != nil {
		return "", "", err
	}
	return s.addPending(r, ""), Fingerprint(r), nil
}

// BeginGenerated starts a setup with a recovery key made here (the browser could not: no
// WebCrypto on plain HTTP). The code is returned once and forgotten at Confirm.
func (s *Store) BeginGenerated() (pendingID, fingerprint, code string, err error) {
	sec, err := NewSecret()
	if err != nil {
		return "", "", "", err
	}
	r := sec.Recipient()
	return s.addPending(r, sec.Code()), Fingerprint(r), sec.Code(), nil
}

func (s *Store) addPending(recipient, code string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	if s.pending == nil {
		s.pending = map[string]pending{}
	}
	id := rand.Text()
	s.pending[id] = pending{recipient: recipient, code: code, expires: s.now().Add(PendingTTL)}
	return id
}

func (s *Store) sweep() {
	now := s.now()
	for id, p := range s.pending {
		if !now.Before(p.expires) {
			delete(s.pending, id)
		}
	}
}

// Confirm finishes a setup once the user has ticked that the key is stored elsewhere (D-80): the
// pending recipient becomes the recovery key, the old one moves to Previous, encryption is on and
// the system's identity exists. A generated code is dropped from memory here.
func (s *Store) Confirm(pendingID string) (State, error) {
	s.mu.Lock()
	s.sweep()
	p, ok := s.pending[pendingID]
	delete(s.pending, pendingID)
	s.mu.Unlock()
	if !ok {
		return State{}, ErrPendingUnknown
	}
	st, err := s.Load()
	if err != nil {
		return State{}, err
	}
	now := s.now()
	if st.Recovery != nil {
		if st.Recovery.Recipient == p.recipient {
			// the same key again: nothing rotates
			st.Enabled = true
			return st, s.save(st)
		}
		old := *st.Recovery
		old.Retired = now
		st.Previous = append([]Key{old}, st.Previous...)
	}
	// a previous key set up again comes back to the front
	for i, k := range st.Previous {
		if k.Recipient == p.recipient {
			st.Previous = append(st.Previous[:i], st.Previous[i+1:]...)
			break
		}
	}
	st.Recovery = &Key{Recipient: p.recipient, Fingerprint: Fingerprint(p.recipient), Created: now}
	st.Enabled = true
	if _, err := s.EnsureBoxIdentity(); err != nil {
		return State{}, err
	}
	return st, s.save(st)
}

// SetEnabled switches encryption of the backups handed out; on needs a recovery key.
func (s *Store) SetEnabled(on bool) (State, error) {
	st, err := s.Load()
	if err != nil {
		return State{}, err
	}
	if on && st.Recovery == nil {
		return State{}, ErrNoRecoveryKey
	}
	st.Enabled = on
	return st, s.save(st)
}

// Match says which recovery key a recipient is: "current", "previous" or "none".
func (s *Store) Match(recipient string) (kind string, key *Key) {
	st, err := s.Load()
	if err != nil {
		return "none", nil
	}
	if st.Recovery != nil && st.Recovery.Recipient == recipient {
		return "current", st.Recovery
	}
	for i := range st.Previous {
		if st.Previous[i].Recipient == recipient {
			return "previous", &st.Previous[i]
		}
	}
	return "none", nil
}

// MatchFingerprint is Match by the fingerprint a file's header names: "current", "previous" or
// "unknown" (an empty fingerprint is unknown too).
func (s *Store) MatchFingerprint(fp string) (kind string, key *Key) {
	if fp == "" {
		return "unknown", nil
	}
	st, err := s.Load()
	if err != nil {
		return "unknown", nil
	}
	if st.Recovery != nil && st.Recovery.Fingerprint == fp {
		return "current", st.Recovery
	}
	for i := range st.Previous {
		if st.Previous[i].Fingerprint == fp {
			return "previous", &st.Previous[i]
		}
	}
	return "unknown", nil
}

// RecordCreated notes a backup this system handed out.
func (s *Store) RecordCreated(c Created) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureTag(); err != nil {
		return err
	}
	list, _ := s.created()
	list = append([]Created{c}, list...)
	if len(list) > maxCreated {
		list = list[:maxCreated]
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.Dir, KeyDir, CreatedFile), b, 0o600)
}

func (s *Store) created() ([]Created, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, KeyDir, CreatedFile))
	if err != nil {
		return nil, err
	}
	var list []Created
	return list, json.Unmarshal(b, &list)
}

// CreatedHere says whether a file with that SHA-256 was handed out by this system.
func (s *Store) CreatedHere(sha256hex string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, _ := s.created()
	for _, c := range list {
		if c.SHA256 == sha256hex {
			return true
		}
	}
	return false
}
