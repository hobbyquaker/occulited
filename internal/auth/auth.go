// Package auth is occulited's users, tokens and sessions (task 6, D-14, D-28): local accounts
// with argon2id and two roles, API tokens with scopes (task 66, scopes.go), session ids of 130
// bits (26 characters of base32, task 125), failed-login lockout, a first-boot setup, and a
// session directory on tmpfs that the lighttpd gate reads.
//
// Sessions survive a restart and a reboot (B-102, D-67): the store keeps each one under the
// sha256 of its id, never the id itself - in memory, in the session store on the userfs (a
// directory of its own with .nobackup, so no backup carries it) and in the gate's mirror on tmpfs,
// whose files are named by that hash. Whoever holds an id hashes it to look it up.
//
// The legacy alias (task 125, D-77): the CCU's addon convention passes the session as
// `?sid=@xxxxxxxxxx@`, ten alphanumerics, and the addons' CGIs parse exactly that shape. A session
// gets such an alias only when the shell opens an addon that needs it (LegacyID), one per session,
// drawn without bias. The alias is accepted by lighttpd's gate and the tclrega shim for /addons/
// alone - never by the API, never as a cookie - and ends with its session. Its mirror is a second
// directory, named by the alias's hash like the session's; the alias itself the process keeps in
// memory only, so that it can hand it out again, and a restart forgets it (a restored session's
// alias keeps working at the gate until the shell asks for a new one).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/argon2"
)

// Role is an account's: admin or user. A session's role maps to a fixed list of scopes
// (RoleScopes); a token carries scopes instead of a role since task 66.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
	// RoleLED was a token's role before task 66 (task 95: the status LED's state, overrides and
	// locate, nothing else). It survives as the console's --role alias and in the migration of
	// an older users.json, both of which turn it into the scope led; no account has it.
	RoleLED Role = "led"
)

// Level is an account's place on the ladder (task 78, D-116): read, operate, configure,
// administer - the same four tiers a token's rpc:* scopes name. It replaces the role: today's
// user is operate (may switch and set values, not rename or assign rooms), today's admin is
// administer. Role stays as the derived field the API answers for one release (administer is
// admin, everything else user), so older clients and addons keep working.
type Level string

const (
	LevelRead       Level = "read"
	LevelOperate    Level = "operate"
	LevelConfigure  Level = "configure"
	LevelAdminister Level = "administer"
)

// Levels are the four, lowest first - the order the pages list them.
var Levels = []Level{LevelRead, LevelOperate, LevelConfigure, LevelAdminister}

// ValidLevel reports whether l is one of the four.
func ValidLevel(l Level) bool {
	for _, x := range Levels {
		if x == l {
			return true
		}
	}
	return false
}

// LevelOf is the migration of a role: admin is administer, user (and an unset role) operate.
func LevelOf(r Role) Level {
	if r == RoleAdmin {
		return LevelAdminister
	}
	return LevelOperate
}

// RoleOf is the derived role: administer is admin, every other level user.
func RoleOf(l Level) Role {
	if l == LevelAdminister {
		return RoleAdmin
	}
	return RoleUser
}

// User is one account as stored in users.json. An account without a hash has no password (task
// 19, D-53): it was created for the identity provider and signs in there only, until an
// administrator or the console gives it one. The fields `external` and `subject` of earlier
// versions - the provider's name and stable id, bound on first sight - are not read any more: an
// account is matched by its name alone, and the next save drops them.
type User struct {
	Name string `json:"name"`
	// ID is the account's stable id (task 193): 8 hex characters, given once and kept across
	// renames, so what hangs off an account - its favorites node in the metadata store - survives.
	// An older file without ids gets them when it is read, written at the next save.
	ID string `json:"id,omitempty"`
	// Role is derived from Level since task 78 (RoleOf); it is still written, so a file an older
	// occulited reads keeps its administrators.
	Role Role `json:"role"`
	// Level is the account's place on the ladder; an older file without it is migrated from the
	// role when it is read (LevelOf).
	Level        Level     `json:"level,omitempty"`
	Hash         string    `json:"hash"` // argon2id$<salt>$<hash>, base64; empty: no password
	Created      time.Time `json:"created"`
	MustChangePW bool      `json:"must_change_password,omitempty"`
	// Preferences are the shell's per-account settings (preferences.go); nil when there are none.
	Preferences *Preferences `json:"preferences,omitempty"`
	// DisplayNames are the account's own names for channels in Control (occulited task 73,
	// displaynames.go), keyed by the metadata object's id; nil when there are none.
	DisplayNames map[string]string `json:"display_names,omitempty"`
	// LastProviderLogin is when the identity provider last signed this account in.
	LastProviderLogin *time.Time `json:"last_provider_login,omitempty"`
	// WebAuthn are the account's passkeys (openccu-lite task 262, occulited task 14, webauthn.go);
	// nil when it has none. Each signs in on its own; the password stays a login of its own too.
	WebAuthn []*WebAuthnCredential `json:"webauthn,omitempty"`
	// ConsoleReset is when `occulited admin reset-auth` last reset the account's access on the
	// console (occulited task 14, consolereset.go): the Status page shows it for a while.
	ConsoleReset *time.Time `json:"console_reset,omitempty"`
}

// assignIDs gives every account without a stable id one, unique among the accounts. The id is
// derived from the name (the first 8 hex characters of its SHA-256), so an older file reads the
// same ids on every start without being rewritten on read - it is written at the next save, and
// from then on it is the id whatever the name becomes. A collision takes the next derivation.
// True when something was given.
func assignIDs(users map[string]*User) bool {
	taken := map[string]bool{}
	for _, u := range users {
		if u.ID != "" {
			taken[u.ID] = true
		}
	}
	names := make([]string, 0, len(users))
	for n := range users {
		names = append(names, n)
	}
	sort.Strings(names) // the same order every time, so a collision resolves the same way
	given := false
	for _, n := range names {
		u := users[n]
		if u.ID != "" && idRe.MatchString(u.ID) {
			continue
		}
		for i := 0; ; i++ {
			id := accountIDFor(u.Name, i)
			if !taken[id] {
				u.ID, taken[id] = id, true
				break
			}
		}
		given = true
	}
	return given
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// accountIDFor is the derived id: 8 hex characters of SHA-256(name), or of name#n for the n-th
// collision.
func accountIDFor(name string, n int) string {
	key := name
	if n > 0 {
		key = fmt.Sprintf("%s#%d", name, n)
	}
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:4])
}

// normalize settles Level and Role against each other: a file without levels gets them from
// the roles, and the role always follows the level.
func (u *User) normalize() {
	if u.Level == "" || !ValidLevel(u.Level) {
		u.Level = LevelOf(u.Role)
	}
	u.Role = RoleOf(u.Level)
}

// Account is a User as the list hands it out: without the hash, with whether one is set, and
// how many security keys it has (task 262) - never the keys themselves.
type Account struct {
	User
	PasswordSet  bool `json:"password_set"`
	WebAuthnKeys int  `json:"webauthn_keys"`
	// WebAuthnUnusable counts the keys among them that cannot sign in: second-factor keys from
	// before occulited task 14.
	WebAuthnUnusable int `json:"webauthn_unusable,omitempty"`
}

// Session is one live login as the store hands it out. ID is the session id for the caller that
// opened or presented it (Login, Validate, EnsureAnonymous); in Sessions' list it is the session's
// handle (SessionHandle), because the store does not know other sessions' ids.
type Session struct {
	ID   string `json:"id"`
	User string `json:"user"`
	// Role is the account's (derived from Level since task 78); a token's session has none.
	Role Role `json:"role,omitempty"`
	// Level is the account's place on the ladder (task 78); a token's session has none.
	Level Level `json:"level,omitempty"`
	// AccountID is the account's stable id (task 193); a token's session has none.
	AccountID string `json:"account_id,omitempty"`
	// Scopes is what the session may do (task 66): the account's role mapped by RoleScopes, or
	// the token's own list. Has is the question to ask; the list itself is for the pages.
	Scopes   Scopes    `json:"scopes"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
	Remote   string    `json:"remote,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	// Method is how the session was opened: MethodPassword, or the identity provider ("oidc").
	Method string `json:"method,omitempty"`
	// Legacy is the session's ?sid=@..@ alias when this process knows it (LegacyID made it, or
	// it is auth-off's fixed one); never in a listing, never in JSON.
	Legacy string `json:"-"`
}

// TokenSessionPrefix marks the ID and the User of a token's session ("token:<name>"), so
// handlers and logs can tell a program from a person.
const TokenSessionPrefix = "token:"

// IsToken reports whether the session is a token's, not an account's.
func (s *Session) IsToken() bool { return strings.HasPrefix(s.ID, TokenSessionPrefix) }

// Has reports whether the session may do what scope names. ScopeSelf - the caller's own account
// and session - every account session has; a token only through auth:admin (Covers).
func (s *Session) Has(scope Scope) bool {
	if scope == ScopeSelf && !s.IsToken() {
		return true
	}
	return s.Scopes.Has(scope)
}

// The login methods a session records.
const (
	MethodPassword = "password"
	// MethodPublic is the Control app's public mode (task 193): the principal a request without
	// a session becomes on the App's paths - never stored, never mirrored for the gate.
	MethodPublic = "public"
	MethodOIDC   = "oidc"
	// methodAnonymous is auth-off mode's one session, which is never stored.
	methodAnonymous = "anonymous"
)

// LastSeenEvery is how stale a session's last-seen time may be in the session store: a request
// writes it only when the stored one is at least this old (D-67: not on every request). A restart
// can therefore end a session's idle time up to this much early, never late.
const LastSeenEvery = 10 * time.Minute

// live is a session as the store holds it: under the hash of its id, never the id itself.
type live struct {
	hash     string
	user     string
	role     Role
	level    Level
	acct     string // the account's stable id
	method   string
	created  time.Time
	lastSeen time.Time
	expires  time.Time // created + MaxAge, fixed at the login and kept across restarts
	saved    time.Time // lastSeen as the session store last holds it
	remote   string
	agent    string
	// aliasHash is the sha256 of the session's legacy alias ("" without one): the name of its
	// file in the alias mirror and what the session store keeps; alias is the alias itself,
	// known to the process that made it and forgotten with a restart.
	aliasHash string
	alias     string
}

func (x *live) public(id string) Session {
	return Session{ID: id, User: x.user, Role: x.role, Level: x.level, AccountID: x.acct, Scopes: LevelScopes(x.level), Created: x.created, LastSeen: x.lastSeen, Remote: x.remote, Agent: x.agent, Method: x.method, Legacy: x.alias}
}

// Errors with stable meaning; the API maps them to codes.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrLockedOut          = errors.New("too many failed attempts, try again later")
	ErrSetupDone          = errors.New("setup already done")
	ErrSetupRequired      = errors.New("no users yet: setup required")
	ErrUnknownUser        = errors.New("unknown user")
	ErrDuplicateUser      = errors.New("user exists")
	ErrLastAdmin          = errors.New("cannot remove the last administrator")
	ErrWeakPassword       = errors.New("password must be at least 8 characters")
	ErrBadUsername        = errors.New("username must match [a-z0-9][a-z0-9_.-]* (2-32 characters)")
	// ErrNoPassword: the account exists but has no password, so only the identity provider signs
	// it in (task 19). Login answers ErrInvalidCredentials for it - the login page must not tell
	// names apart - and SetPassword with a current password answers this.
	ErrNoPassword = errors.New("the account has no password: it signs in through the identity provider")
	// ErrBadRole: an account's role is admin or user (the role led is a token's only).
	ErrBadRole = errors.New("role must be admin or user")
)

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{1,31}$`)

// ValidName says whether name has an account name's shape (the public mode's account, task 193).
func ValidName(name string) bool { return usernameRe.MatchString(name) }

// Options tune the store; zero values are the defaults.
type Options struct {
	// IdleTimeout ends a session after this much inactivity (default 30 min since openccu-lite
	// task 262 - ASVS Level 2; 24 h before; auth.session_idle in occulited.json).
	IdleTimeout time.Duration
	// MaxAge ends a session regardless of activity, measured from the login (default 12 h since
	// task 262; 30 days before; auth.session_max).
	MaxAge time.Duration
	// LockAfter failed attempts per user or remote within LockWindow lock for LockFor.
	LockAfter  int
	LockWindow time.Duration
	LockFor    time.Duration
	// SessionDir, when set, mirrors live sessions as files named by the sha256 of the session id
	// (lower-case hex) containing the user name, on tmpfs, for the lighttpd gate (D-28) and the
	// tclrega shim. Empty disables the mirror.
	SessionDir string
	// LegacyDir mirrors the legacy aliases the same way (task 125): a file named by the sha256 of
	// the alias, holding the user name and, on a second line, the hash of the session it belongs
	// to. Empty with a SessionDir: its sibling "legacy-sessions". Empty without one: no mirror.
	LegacyDir string
	// GateTokenDir mirrors the stored API tokens for the gate the same way (openccu-lite task 307):
	// a file per token named by the sha256 of its secret, holding the token's name, the URL
	// segments under /addons/ its ingress scopes open, its expiry and its address ranges (the
	// format at syncGateTokens). Empty with a SessionDir: its sibling "gate-tokens". Empty without
	// one: no mirror.
	GateTokenDir string
	// AddonSegments answers the URL segments under /addons/ that belong to the addon id besides
	// the id itself - the path its lighttpd drop-in proxies (redmatic's /addons/red/) - for the
	// token mirror. nil: the id alone.
	AddonSegments func(id string) []string
	// SessionFile, when set, is the session store on the userfs (B-102, D-67): the sessions by
	// hash, written on a login, a logout and an expiry, and a last-seen time at most every
	// LastSeenEvery. Its directory is the store's own and carries .nobackup. Empty: sessions live
	// in memory only (the console commands).
	SessionFile string
	// RestoreMethods are the login methods whose stored sessions Open restores; a session of any
	// other method is dropped (a login the running authentication mode does not offer). None: no
	// session is restored.
	RestoreMethods []string
	Now            func() time.Time
	// Changed is told, off the store's lock, whenever users.json was written or found changed
	// by another writer (task 193: the favorites nodes follow the accounts). May be nil.
	Changed func()
}

func (o *Options) defaults() {
	if o.IdleTimeout == 0 {
		o.IdleTimeout = DefaultIdleTimeout
	}
	if o.MaxAge == 0 {
		o.MaxAge = DefaultMaxAge
	}
	if o.LockAfter == 0 {
		o.LockAfter = 8
	}
	if o.LockWindow == 0 {
		o.LockWindow = 10 * time.Minute
	}
	if o.LockFor == 0 {
		o.LockFor = 15 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.LegacyDir == "" && o.SessionDir != "" {
		o.LegacyDir = filepath.Join(filepath.Dir(filepath.Clean(o.SessionDir)), "legacy-sessions")
	}
	if o.GateTokenDir == "" && o.SessionDir != "" {
		o.GateTokenDir = filepath.Join(filepath.Dir(filepath.Clean(o.SessionDir)), "gate-tokens")
	}
}

// Store holds users (persisted) and sessions (memory + optional tmpfs mirror).
type Store struct {
	mu     sync.Mutex
	path   string
	opt    Options
	users  map[string]*User
	tokens []*Token
	// ephemeral are the tokens that live in this process only (task 66): an addon's API token
	// from its catalogue declaration, the daemon's own for its update checks. By name; never in
	// users.json, gone with the process, minted again at the next start.
	ephemeral map[string]*Token
	// disk is users.json as this store last read or wrote it (nil: no file); base holds the
	// records it contained then, which save merges against.
	disk     os.FileInfo
	base     records
	readErr  string                 // the last failed read of users.json, logged once
	sessions map[string]*live       // by sessionKey(id)
	aliases  map[string]string      // the session key by the alias's hash (live.aliasHash)
	tickets  map[string]ticket      // by the ticket's hash
	failures map[string][]time.Time // key: "u:<name>" or "r:<remote>"
	locked   map[string]time.Time
	// pending are the logins and ceremonies between their two halves (webauthn.go), by id;
	// memory only, PendingTTL each
	pending map[string]pendingLogin
}

// ticket is a one-time credential a session issued for one URL path: a download link that must
// not carry the session, or the hand-over of the session to another host (the network change's
// confirm link). Single use, TicketTTL, memory only - a restart voids it.
type ticket struct {
	key     string // the session's
	path    string
	expires time.Time
	// confirmed (task 154, D-104): issued after the account's password or a fresh login at the
	// provider, for a route that asks for that even in a valid session; it is spent only by
	// RedeemConfirmed, never as a download ticket or a session hand-over
	confirmed bool
}

// TicketTTL is how long a download ticket may be redeemed.
const TicketTTL = 60 * time.Second

// SessionTicketTTL is how long a session hand-over ticket may be redeemed (D-84): as long as the
// network change's confirm window, which is what the ticket is fetched for - right before the
// change, since the page may not reach the box afterwards.
const SessionTicketTTL = 90 * time.Second

// TicketSession is the path of a ticket that hands out the session id itself, for a browser on
// a host without the cookie (the network change's confirm link, task 8).
const TicketSession = "session"

// TicketLifetime is how long a ticket for path may be redeemed: SessionTicketTTL for a session
// hand-over, TicketTTL for a download.
func TicketLifetime(path string) time.Duration {
	if path == TicketSession {
		return SessionTicketTTL
	}
	return TicketTTL
}

// Open loads users.json from dir (missing = no users, setup required), restores the stored
// sessions (Options.SessionFile) and rebuilds the gate's mirror from them.
func Open(dir string, opt Options) (*Store, error) {
	opt.defaults()
	s := &Store{path: filepath.Join(dir, "users.json"), opt: opt, users: map[string]*User{}, ephemeral: map[string]*Token{}, sessions: map[string]*live{}, aliases: map[string]string{}, tickets: map[string]ticket{}, failures: map[string][]time.Time{}, locked: map[string]time.Time{}}
	if err := s.reload(); err != nil {
		return nil, err
	}
	if opt.SessionFile != "" {
		s.loadSessions()
	}
	if opt.SessionDir != "" {
		// 0711: the gate (root) and an addon's CGI (its own user, task 18) can open a session
		// file whose name - the hash of the sid - they compute, but nobody can list them
		if err := os.MkdirAll(opt.SessionDir, 0o711); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(opt.LegacyDir, 0o711); err != nil {
			return nil, err
		}
		s.syncMirror()
	}
	if opt.GateTokenDir != "" {
		// a directory the daemon creates itself under its unit's UMask=0077 would be 0700, and
		// lighttpd's gate could open nothing in it: the mode is set, not left to the umask. A
		// directory that cannot be made (a unit from before the mirror, whose sandbox does not
		// open it) costs the mirror, not the start: GateTokenMirror answers "" then.
		if err := os.MkdirAll(opt.GateTokenDir, 0o711); err != nil {
			s.opt.GateTokenDir = ""
		} else {
			_ = os.Chmod(opt.GateTokenDir, 0o711)
			s.syncGateTokens()
		}
	}
	return s, nil
}

// GateTokenMirror is the directory the gate's token mirror is written to, "" when there is none
// (no session directory, or one that could not be made - the caller logs that).
func (s *Store) GateTokenMirror() string { return s.opt.GateTokenDir }

// usersDoc is the content of users.json.
type usersDoc struct {
	Users  []*User  `json:"users"`
	Tokens []*Token `json:"tokens,omitempty"`
}

// records are the users and the tokens of one state of users.json, each as its JSON, by name.
type records struct {
	users, tokens map[string]string
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// reload reads users.json when it is not the file this store last read or wrote: the `passwd`
// and `token` console commands write it while the daemon runs. A failure keeps what is in
// memory; save then refuses to overwrite the file it could not read.
func (s *Store) reload() error {
	st, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if len(s.users) > 0 || s.disk != nil {
			// removed: no users, setup again; the tokens stay and the next save writes them back
			old := s.users
			s.users = map[string]*User{}
			s.disk, s.base = nil, records{}
			s.accountsChanged(old)
		}
		return nil
	}
	if err != nil {
		return s.failed(err)
	}
	if !s.changed(st) {
		return nil
	}
	doc, st, err := s.read()
	if err != nil {
		return s.failed(err)
	}
	old := s.users
	s.users = map[string]*User{}
	for _, u := range doc.Users {
		if u != nil {
			u.normalize()
			s.users[u.Name] = u
		}
	}
	assignIDs(s.users) // an older file: derived ids, written at the next save
	defer s.accountsChanged(old)
	s.tokens = nil
	for _, t := range doc.Tokens {
		if t != nil {
			s.tokens = append(s.tokens, t)
		}
	}
	s.synced(st)
	s.syncGateTokens() // a console-made token opens the gate as soon as the daemon has seen it
	return nil
}

// changed reports whether users.json is another file than the one this store last read or
// wrote: another inode (every write renames a new file into place), size or modification time.
// Equality, not "newer": a clock set back between two writes - a box without a real-time clock
// before its time is synchronised - must not hide the second one.
func (s *Store) changed(st os.FileInfo) bool {
	return s.disk == nil || !os.SameFile(s.disk, st) || st.Size() != s.disk.Size() || !st.ModTime().Equal(s.disk.ModTime())
}

// read parses users.json, together with the identity of the file it read.
func (s *Store) read() (usersDoc, os.FileInfo, error) {
	var doc usersDoc
	f, err := os.Open(s.path)
	if err != nil {
		return doc, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return doc, nil, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return doc, nil, err
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return doc, nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	for _, t := range doc.Tokens {
		if t != nil {
			t.migrate()
		}
	}
	return doc, st, nil
}

// failed logs a failed read of users.json once - every credential check reloads - and returns
// the error.
func (s *Store) failed(err error) error {
	if msg := err.Error(); msg != s.readErr {
		s.readErr = msg
		slog.Warn("users: cannot read users.json; accounts and tokens stay as they were", "path", s.path, "err", err)
	}
	return err
}

// synced records that users.json (st) now holds exactly what is in memory.
func (s *Store) synced(st os.FileInfo) {
	s.disk, s.readErr = st, ""
	s.base = records{users: map[string]string{}, tokens: map[string]string{}}
	for name, u := range s.users {
		s.base.users[name] = asJSON(u)
	}
	for _, t := range s.tokens {
		s.base.tokens[t.Name] = asJSON(t)
	}
}

// save writes users.json. When another writer changed the file since this store last synced
// with it - a console command within a mutation, or while the file could not be read - what it
// wrote is merged in first: save never drops an account or a token this store did not remove
// itself, and it does not overwrite a file it cannot read.
func (s *Store) save() error {
	if st, err := os.Stat(s.path); err == nil && s.changed(st) {
		doc, _, err := s.read()
		if err != nil {
			return fmt.Errorf("users.json was changed by someone else and cannot be read, so it is not overwritten: %w", s.failed(err))
		}
		old := s.users
		s.merge(doc)
		s.accountsChanged(old)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	users := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	sort.SliceStable(s.tokens, func(i, j int) bool { return s.tokens[i].Name < s.tokens[j].Name })
	b, _ := json.MarshalIndent(usersDoc{Users: users, Tokens: s.tokens}, "", "  ")
	st, err := writeAtomic(s.path, append(b, '\n'), 0o600)
	if err != nil {
		return err
	}
	s.synced(st)
	s.syncGateTokens()
	if s.opt.Changed != nil {
		go s.opt.Changed() // off the lock: the listener reads the accounts back
	}
	return nil
}

// merge folds users.json as another writer left it (doc) into memory, record by record: what
// this store added, changed or removed since it last synced stands, everything else is taken
// from the file - including what the other writer added, changed or removed.
func (s *Store) merge(doc usersDoc) {
	users := map[string]*User{}
	for _, u := range doc.Users {
		if u != nil {
			u.normalize()
			users[u.Name] = u
		}
	}
	assignIDs(users)
	for name, u := range s.users {
		if b, ok := s.base.users[name]; !ok || b != asJSON(u) {
			users[name] = u
		}
	}
	for name := range s.base.users {
		if _, ok := s.users[name]; !ok {
			delete(users, name)
		}
	}
	s.users = users

	tokens := map[string]*Token{}
	for _, t := range doc.Tokens {
		if t != nil {
			tokens[t.Name] = t
		}
	}
	mine := map[string]bool{}
	for _, t := range s.tokens {
		mine[t.Name] = true
		if b, ok := s.base.tokens[t.Name]; !ok || b != asJSON(t) {
			tokens[t.Name] = t
		}
	}
	for name := range s.base.tokens {
		if !mine[name] {
			delete(tokens, name)
		}
	}
	s.tokens = make([]*Token, 0, len(tokens))
	for _, t := range tokens {
		s.tokens = append(s.tokens, t)
	}
	sort.Slice(s.tokens, func(i, j int) bool { return s.tokens[i].Name < s.tokens[j].Name })
}

// writeAtomic replaces path with b and returns the identity of the new file.
func writeAtomic(path string, b []byte, mode os.FileMode) (os.FileInfo, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return nil, err
	}
	// The console commands run as root, the daemon as its own user. A file root writes keeps
	// the owner of the state directory, or the daemon could not read it (0600) and its next
	// save would put its own state in place of what the command wrote.
	if os.Geteuid() == 0 {
		if dir, err := os.Stat(filepath.Dir(path)); err == nil {
			if sys, ok := dir.Sys().(*syscall.Stat_t); ok {
				if err := os.Chown(tmp.Name(), int(sys.Uid), int(sys.Gid)); err != nil {
					return nil, err
				}
			}
		}
	}
	// renaming keeps inode, size and modification time: this is the identity path will have
	st, err := os.Stat(tmp.Name())
	if err != nil {
		return nil, err
	}
	return st, os.Rename(tmp.Name(), path)
}

// --- passwords ---

// argon2id parameters. 32 MiB is what a 1 GB box shared with a JVM can spare per login (OWASP's
// floor is 19 MiB at t=2); the parameters travel in the hash so they can change later without
// invalidating anyone's password. Hashes from before the parameters were recorded used 64 MiB,
// t=3, p=2 and still verify.
const (
	argonTime    = 3
	argonMemory  = 32 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

// one hash at a time: the memory cost is the point of argon2, and two logins at once must not
// double the peak on a small box
var hashSem = make(chan struct{}, 1)

// hashes counts the argon2 computations and lastCost holds the parameters of the latest; the tests
// use them to see that a refusal cost one run of the current parameters, whether the account exists
// or not (occulited B-36: the same thing measured in wall time failed on a busy CI runner)
var (
	hashes   atomic.Int64
	lastCost atomic.Value // argonCost
)

// argonCost is what an argon2 run costs: its time, memory, threads and key length.
type argonCost struct {
	t, m uint32
	p    uint8
	n    uint32
}

func idKey(pw string, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	hashes.Add(1)
	lastCost.Store(argonCost{t: t, m: m, p: p, n: n})
	hashSem <- struct{}{}
	defer func() {
		<-hashSem
		debug.FreeOSMemory() // hand the block back now, not when the scavenger gets to it
	}()
	return argon2.IDKey([]byte(pw), salt, t, m, p, n)
}

// HashPassword returns an argon2id hash string: argon2id$m=<KiB>,t=<n>,p=<n>$<salt>$<key>.
func HashPassword(pw string) (string, error) {
	if len(pw) < 8 {
		return "", ErrWeakPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := idKey(pw, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// dummyHash is what a password is verified against when the account does not exist or has no
// password (B-233): the refusal then costs the same argon2 run as a wrong password, so the time
// of the answer does not tell which account names exist. The parameters are the current ones;
// the key is not the hash of any known password, so nothing ever verifies against it.
var dummyHash = fmt.Sprintf("argon2id$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
	"b2NjdWxpdGVkLWR1bW15", "4ebZYjMVzOmZcI8q4fS8m0V9rJzV2Y3n4Sx6QvS7x1A")

// verifyOrBurn verifies pw against the account's hash, or against dummyHash when there is none,
// and reports whether it matched: always false without a hash.
func verifyOrBurn(u *User, ok bool, pw string) bool {
	if !ok || u == nil || u.Hash == "" {
		verifyPassword(dummyHash, pw)
		return false
	}
	return verifyPassword(u.Hash, pw)
}

func verifyPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if parts[0] != "argon2id" {
		return false
	}
	t, m, p := uint32(3), uint32(64*1024), uint8(2) // the format without parameters
	switch len(parts) {
	case 3:
	case 4:
		for _, kv := range strings.Split(parts[1], ",") {
			k, v, _ := strings.Cut(kv, "=")
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return false
			}
			switch k {
			case "m":
				m = uint32(n)
			case "t":
				t = uint32(n)
			case "p":
				p = uint8(n)
			default:
				return false
			}
		}
		parts = parts[1:]
	default:
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[1])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || m > 256*1024 {
		return false
	}
	got := idKey(pw, salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// --- users ---

// SetupRequired is true while no user exists.
func (s *Store) SetupRequired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	return len(s.users) == 0
}

// Setup creates the first administrator; refused once any user exists.
func (s *Store) Setup(name, pw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	if len(s.users) > 0 {
		return ErrSetupDone
	}
	return s.createLocked(name, &pw, LevelAdminister, false)
}

// CreateUser adds an account (admin only, enforced by the caller) by the older role: admin is
// administer, user operate (LevelOf).
func (s *Store) CreateUser(name, pw string, role Role, mustChange bool) error {
	if role != RoleAdmin && role != RoleUser {
		return ErrBadRole
	}
	return s.CreateUserLevel(name, pw, LevelOf(role), mustChange)
}

// CreateUserLevel adds an account at a level of the ladder (task 78).
func (s *Store) CreateUserLevel(name, pw string, level Level, mustChange bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	return s.createLocked(name, &pw, level, mustChange)
}

// CreateUserWithoutPassword adds an account the identity provider signs in (task 19, D-53): no
// password, so POST /login refuses it until an administrator or the console sets one. The caller
// offers this only while a provider is configured - without one such an account could never log in.
func (s *Store) CreateUserWithoutPassword(name string, role Role) error {
	if role != RoleAdmin && role != RoleUser {
		return ErrBadRole
	}
	return s.CreateUserWithoutPasswordLevel(name, LevelOf(role))
}

// CreateUserWithoutPasswordLevel is CreateUserWithoutPassword at a level of the ladder.
func (s *Store) CreateUserWithoutPasswordLevel(name string, level Level) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	return s.createLocked(name, nil, level, false)
}

// createLocked adds the account; pw nil means no password.
func (s *Store) createLocked(name string, pw *string, level Level, mustChange bool) error {
	if !usernameRe.MatchString(name) {
		return ErrBadUsername
	}
	if !ValidLevel(level) {
		return ErrBadRole
	}
	if _, ok := s.users[name]; ok {
		return ErrDuplicateUser
	}
	u := &User{Name: name, Role: RoleOf(level), Level: level, Created: s.opt.Now(), MustChangePW: mustChange}
	s.users[name] = u // so the id is unique among them
	assignIDs(s.users)
	delete(s.users, name)
	if pw != nil {
		h, err := HashPassword(*pw)
		if err != nil {
			return err
		}
		u.Hash = h
	}
	s.users[name] = u
	return s.save()
}

// DeleteUser removes an account and its sessions; the last administrator cannot be removed.
func (s *Store) DeleteUser(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	u, ok := s.users[name]
	if !ok {
		return ErrUnknownUser
	}
	if u.Level == LevelAdminister && s.adminCount() == 1 {
		return ErrLastAdmin
	}
	delete(s.users, name)
	if s.dropSessionsOf(name, "") > 0 {
		s.saveSessions(true)
	}
	return s.save()
}

func (s *Store) adminCount() int {
	n := 0
	for _, u := range s.users {
		if u.Level == LevelAdminister {
			n++
		}
	}
	return n
}

// SetRole changes an account by the older role (admin is administer, user operate); demoting
// the last administrator is refused.
func (s *Store) SetRole(name string, role Role) error {
	if role != RoleAdmin && role != RoleUser {
		return ErrBadRole
	}
	return s.SetLevel(name, LevelOf(role))
}

// SetLevel moves an account on the ladder (task 78); its live sessions follow at once. Taking
// the last administrator below administer is refused.
func (s *Store) SetLevel(name string, level Level) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	if !ValidLevel(level) {
		return ErrBadRole
	}
	u, ok := s.users[name]
	if !ok {
		return ErrUnknownUser
	}
	if u.Level == LevelAdminister && level != LevelAdminister && s.adminCount() == 1 {
		return ErrLastAdmin
	}
	u.Level, u.Role = level, RoleOf(level)
	changed := false
	for _, x := range s.sessions {
		if x.user == name && x.level != level {
			x.role, x.level, changed = u.Role, level, true
		}
	}
	if changed {
		s.saveSessions(false) // a restored session takes its role from users.json anyway
	}
	return s.save()
}

// SetPassword sets a new password. When current is non-nil it must verify (a user changing their
// own); an administrator resets without it. Other sessions of the user are ended.
func (s *Store) SetPassword(name, newPW string, current *string, keepSession string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	u, ok := s.users[name]
	if !ok {
		return ErrUnknownUser
	}
	if current != nil && u.Hash == "" {
		return ErrNoPassword // nothing to verify against: an administrator or the console sets the first one
	}
	if current != nil && !verifyPassword(u.Hash, *current) {
		return ErrInvalidCredentials
	}
	h, err := HashPassword(newPW)
	if err != nil {
		return err
	}
	u.Hash = h
	u.MustChangePW = false
	if s.dropSessionsOf(name, keepSession) > 0 {
		s.saveSessions(true)
	}
	return s.save()
}

// accountOf is u as a list hands it out: no hash, no preferences, the keys counted.
func accountOf(u *User) Account {
	c := Account{User: *u, PasswordSet: u.Hash != "", WebAuthnKeys: len(u.WebAuthn)}
	for _, k := range u.WebAuthn {
		if !k.Passkey() {
			c.WebAuthnUnusable++
		}
	}
	c.Hash = ""
	c.Preferences = nil // the list is about accounts, not what their shells remember
	c.DisplayNames = nil
	c.WebAuthn = nil // the count says enough; the records hold public keys and counters
	return c
}

// Users lists accounts without hashes.
func (s *Store) Users() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	out := make([]Account, 0, len(s.users))
	for _, u := range s.users {
		c := accountOf(u)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- sessions ---

// A session id is what crypto/rand.Text hands out: 26 characters of base32 (A-Z, 2-7), 130 bits,
// no bias (task 125; OWASP asks for 64). Alphanumeric, so it fits a cookie, a header, a file name
// and the addons' own checks. The legacy alias is the CCU's shape - ten of [0-9a-zA-Z], about 59.5
// bits - which the addon CGIs parse with `@([0-9a-zA-Z]{10})@`.
var (
	sidRe   = regexp.MustCompile(`^[A-Z2-7]{26}$`)
	aliasRe = regexp.MustCompile(`^[A-Za-z0-9]{10}$`)
)

// IsSessionID reports whether s has the shape of a session id (not whether it names one).
func IsSessionID(s string) bool { return sidRe.MatchString(s) }

// IsLegacyID reports whether s has the shape of a legacy alias.
func IsLegacyID(s string) bool { return aliasRe.MatchString(s) }

// newSID returns a fresh session id.
func newSID() (string, error) {
	return rand.Text(), nil
}

const aliasAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// newAlias returns ten alphanumerics, each drawn uniformly from the 62 by rejection: a random
// byte of 248 or more is drawn again, so no character is likelier than another (no modulo bias).
func newAlias() (string, error) {
	out := make([]byte, 0, 10)
	b := make([]byte, 16)
	for len(out) < 10 {
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		for _, c := range b {
			if int(c) < 256-256%len(aliasAlphabet) && len(out) < 10 {
				out = append(out, aliasAlphabet[int(c)%len(aliasAlphabet)])
			}
		}
	}
	return string(out), nil
}

// sessionKey is the name a session goes by everywhere outside the request that carries its id:
// the store's map, the session store and the gate's mirror. The sha256 of the id, lower-case hex -
// what the gate computes with lighty.c.md and the tclrega shim with its own sha256.
func sessionKey(id string) string { return hashToken(id) }

var sessionKeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// handleLen is how much of a session's hash its handle shows.
const handleLen = 6

// SessionHandle is how the session list names the session with this id: the first characters of
// its hash, then "……". It names a session without revealing anything of its id.
func SessionHandle(id string) string { return handleOf(sessionKey(id)) }

func handleOf(key string) string { return key[:handleLen] + "……" }

func (s *Store) lockedOut(key string) bool {
	if until, ok := s.locked[key]; ok {
		if s.opt.Now().Before(until) {
			return true
		}
		delete(s.locked, key)
	}
	return false
}

// recordFailure counts a failed attempt for key and reports whether it started a lockout.
func (s *Store) recordFailure(key string) bool {
	now := s.opt.Now()
	kept := s.failures[key][:0]
	for _, t := range s.failures[key] {
		if now.Sub(t) < s.opt.LockWindow {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	s.failures[key] = kept
	if len(kept) >= s.opt.LockAfter {
		s.locked[key] = now.Add(s.opt.LockFor)
		delete(s.failures, key)
		return true
	}
	return false
}

// Refusal says why a password login was refused, for the log only (B-231): the caller's answer
// stays the generic one. Nothing in it is a secret.
type Refusal struct {
	// Reason is "unknown user", "no password", "wrong password" or "locked".
	Reason string
	// LockedUser and LockedRemote are set when this attempt started a lockout of the name or the
	// address; Until is when it ends.
	LockedUser, LockedRemote bool
	Until                    time.Time
}

// failLogin records a failed attempt for the name and the address and fills the refusal.
func (s *Store) failLogin(name, remote string, why *Refusal) {
	why.LockedUser = s.recordFailure("u:" + name)
	why.LockedRemote = s.recordFailure("r:" + remote)
	if why.LockedUser || why.LockedRemote {
		why.Until = s.opt.Now().Add(s.opt.LockFor)
	}
}

// Login verifies credentials and opens a session. remote is the client address for lockout and
// the session list; agent the User-Agent.
func (s *Store) Login(name, pw, remote, agent string) (*Session, error) {
	sess, _, err := s.LoginDetail(name, pw, remote, agent)
	return sess, err
}

// LoginDetail is Login that also says why a login was refused, for the log (B-231).
func (s *Store) LoginDetail(name, pw, remote, agent string) (*Session, Refusal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	var why Refusal
	if len(s.users) == 0 {
		return nil, why, ErrSetupRequired
	}
	if s.lockedOut("u:"+name) || s.lockedOut("r:"+remote) {
		why.Reason = "locked"
		return nil, why, ErrLockedOut
	}
	u, ok := s.users[name]
	// an account without a password (the provider's) fails like a wrong password: the page must
	// not tell an attacker which names exist and how they sign in
	if !verifyOrBurn(u, ok, pw) {
		switch {
		case !ok:
			why.Reason = "unknown user"
		case u.Hash == "":
			why.Reason = "no password"
		default:
			why.Reason = "wrong password"
		}
		s.failLogin(name, remote, &why)
		return nil, why, ErrInvalidCredentials
	}
	delete(s.failures, "u:"+name)
	// occulited task 14: an account's passkeys are a login of their own, never a second factor -
	// the password opens the session whatever keys the account has
	sess, err := s.openSession(name, u.Level, MethodPassword, remote, agent)
	return sess, why, err
}

// openSession opens a session for a user who has just authenticated, mirrors it for the gate and
// writes it to the session store.
func (s *Store) openSession(name string, level Level, method, remote, agent string) (*Session, error) {
	id, err := newSID()
	if err != nil {
		return nil, err
	}
	key := sessionKey(id)
	if _, taken := s.sessions[key]; taken {
		return nil, errors.New("session id collision") // 2^-130: not in this universe, but never two in one entry
	}
	now := s.opt.Now()
	acct := ""
	if u := s.users[name]; u != nil {
		acct = u.ID
	}
	x := &live{hash: key, user: name, role: RoleOf(level), level: level, acct: acct, method: method, created: now, lastSeen: now, expires: now.Add(s.opt.MaxAge), remote: remote, agent: agent}
	s.sessions[key] = x
	s.mirror(x)
	s.saveSessions(false)
	sess := x.public(id)
	return &sess, nil
}

// Validate looks a session up, touches it, and returns it; nil when unknown or expired.
func (s *Store) Validate(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if IsToken(id) {
		return s.validateTokenLocked(id, "")
	}
	// users.json as another writer may have left it (one stat): a password set on the console
	// ends that account's sessions before its next request, not at the next login
	_ = s.reload()
	key := sessionKey(id)
	x, ok := s.sessions[key]
	if !ok {
		return nil
	}
	now := s.opt.Now()
	if s.expired(x, now) {
		s.dropSession(key)
		s.saveSessions(true)
		return nil
	}
	// never backwards: a box without a real-time clock may start with a clock behind the
	// last-seen time it restored, and must not count the jump to the right time as idle time
	if now.After(x.lastSeen) {
		x.lastSeen = now
	}
	if x.method != methodAnonymous && x.lastSeen.Sub(x.saved) >= LastSeenEvery {
		s.saveSessions(false)
	}
	sess := x.public(id)
	return &sess
}

// expired reports whether a session has idled out or passed its maximum age - the one fixed at
// the login (expires) or, when an administrator shortened the lifetime since (task 262,
// SetSessionLimits), the new one measured from the login.
func (s *Store) expired(x *live, now time.Time) bool {
	return now.Sub(x.lastSeen) > s.opt.IdleTimeout || now.After(x.expires) || now.Sub(x.created) > s.opt.MaxAge
}

// AnonymousSID is the id of the one session that exists when authentication is off (task 29), in
// the session id's shape; AnonymousLegacySID is its fixed alias, the shape the ?sid=@..@
// convention needs. With authentication off anyone is an administrator anyway.
const (
	AnonymousSID       = "ANONYMOUSANONYMOUSANONYMOU"
	AnonymousLegacySID = "anonymous0"
)

// EnsureAnonymous creates or refreshes the anonymous administrator session of auth-off mode and
// returns a copy. Called on every request in that mode, so the session never idles out and its
// mirror files for lighttpd's gate are always there.
func (s *Store) EnsureAnonymous() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	key := sessionKey(AnonymousSID)
	x, ok := s.sessions[key]
	if !ok {
		x = &live{hash: key, user: "anonymous", role: RoleAdmin, level: LevelAdminister, method: methodAnonymous, agent: "auth off", alias: AnonymousLegacySID, aliasHash: sessionKey(AnonymousLegacySID)}
		s.sessions[key] = x
		s.aliases[x.aliasHash] = key
	}
	x.created, x.lastSeen, x.expires = now, now, now.Add(s.opt.MaxAge)
	if s.opt.SessionDir != "" {
		_, err1 := os.Stat(filepath.Join(s.opt.SessionDir, key))
		_, err2 := os.Stat(filepath.Join(s.opt.LegacyDir, x.aliasHash))
		if err1 != nil || err2 != nil {
			s.mirror(x)
		}
	}
	sess := x.public(AnonymousSID)
	return &sess
}

// Public is the principal of the Control app's public mode (task 193): the named account when
// one exists - read or operate; a higher level is refused by the switch - with its own id, so its
// favorites are its own, else a virtual account at operate whose id is the derived one. It has no
// session id: nothing is stored, no cookie is set, and every request resolves it afresh, so the
// switch takes effect at once.
func (s *Store) Public(name string) *Session {
	level, id := LevelOperate, accountIDFor(name, 0)
	s.mu.Lock()
	_ = s.reload()
	if u, ok := s.users[name]; ok {
		if u.Level == LevelRead {
			level = LevelRead
		}
		if u.ID != "" {
			id = u.ID
		}
	}
	now := s.opt.Now()
	s.mu.Unlock()
	return &Session{User: name, Role: RoleOf(level), Level: level, AccountID: id, Scopes: LevelScopes(level), Created: now, LastSeen: now, Method: MethodPublic}
}

// LegacyID is the ?sid=@..@ alias of the session id (task 125): the one the session has when
// this process knows it, else a fresh one - a session that never opened an addon needing it has
// none, and a restart forgets the alias of a restored session (its file at the gate stays until
// here). "" when id names no live session. The alias replaces any earlier one in the mirror and
// the session store.
func (s *Store) LegacyID(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.sessions[sessionKey(id)]
	if !ok || s.expired(x, s.opt.Now()) {
		return "", nil
	}
	if x.alias != "" {
		return x.alias, nil
	}
	alias, err := newAlias()
	if err != nil {
		return "", err
	}
	h := sessionKey(alias)
	if _, taken := s.aliases[h]; taken {
		return "", errors.New("alias collision")
	}
	s.dropAlias(x)
	x.alias, x.aliasHash = alias, h
	s.aliases[h] = x.hash
	s.mirror(x)
	if x.method != methodAnonymous {
		s.saveSessions(false)
	}
	return alias, nil
}

// ValidateLegacy looks a session up by its alias, touches it and returns it - without its id,
// which the store does not hold. For the addon CGIs' guard alone (the gate's rule); the API never
// takes an alias (D-77). nil when the alias names no live session.
func (s *Store) ValidateLegacy(alias string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !IsLegacyID(alias) {
		return nil
	}
	key, ok := s.aliases[sessionKey(alias)]
	if !ok {
		return nil
	}
	x, ok := s.sessions[key]
	if !ok {
		return nil
	}
	now := s.opt.Now()
	if s.expired(x, now) {
		s.dropSession(key)
		s.saveSessions(true)
		return nil
	}
	if now.After(x.lastSeen) {
		x.lastSeen = now
	}
	sess := x.public("")
	return &sess
}

// dropAlias forgets a session's alias: its index entry and its mirror file.
func (s *Store) dropAlias(x *live) {
	if x.aliasHash == "" {
		return
	}
	delete(s.aliases, x.aliasHash)
	if s.opt.LegacyDir != "" {
		_ = os.Remove(filepath.Join(s.opt.LegacyDir, x.aliasHash))
	}
	x.alias, x.aliasHash = "", ""
}

// --- one-time tickets ---

// IssueTicket makes a ticket for the session id, redeemable once within TicketLifetime(path) on
// the given path (a URL path, or TicketSession). "" when id names no live session.
func (s *Store) IssueTicket(id, path string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	x, ok := s.sessions[sessionKey(id)]
	if !ok || s.expired(x, now) || path == "" {
		return "", nil
	}
	s.sweepTickets(now)
	t := rand.Text()
	s.tickets[sessionKey(t)] = ticket{key: x.hash, path: path, expires: now.Add(TicketLifetime(path))}
	return t, nil
}

// IssueConfirmedTicket makes a confirmed ticket (task 154, D-104) for the session id and path:
// the caller has just checked the account's password, or the provider's fresh login. Single use,
// TicketTTL, bound to that session. "" when id names no live session.
func (s *Store) IssueConfirmedTicket(id, path string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	x, ok := s.sessions[sessionKey(id)]
	if !ok || s.expired(x, now) || path == "" {
		return "", nil
	}
	s.sweepTickets(now)
	t := rand.Text()
	s.tickets[sessionKey(t)] = ticket{key: x.hash, path: path, expires: now.Add(TicketTTL), confirmed: true}
	return t, nil
}

// RedeemConfirmed spends a confirmed ticket: true when it was issued for path to the session id,
// which is still live. Anything else - unknown, spent, run out, another path or session, a plain
// ticket - is false, and the ticket is spent all the same.
func (s *Store) RedeemConfirmed(t, path, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	s.sweepTickets(now)
	h := sessionKey(t)
	tk, ok := s.tickets[h]
	if !ok {
		return false
	}
	delete(s.tickets, h)
	if !tk.confirmed || tk.path != path || tk.key != sessionKey(id) {
		return false
	}
	x, ok := s.sessions[tk.key]
	return ok && !s.expired(x, now)
}

// ConfirmPassword checks the password of a signed-in account for a confirmation (task 154). It
// counts like a login: a wrong one is a failure towards the lockout of the account and the address.
func (s *Store) ConfirmPassword(name, pw, remote string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	if s.lockedOut("u:"+name) || s.lockedOut("r:"+remote) {
		return ErrLockedOut
	}
	u, ok := s.users[name]
	if !ok || u.Hash == "" {
		return ErrNoPassword
	}
	if !verifyPassword(u.Hash, pw) {
		s.recordFailure("u:" + name)
		s.recordFailure("r:" + remote)
		return ErrInvalidCredentials
	}
	delete(s.failures, "u:"+name)
	return nil
}

// HasPassword reports whether the account can confirm with a password.
func (s *Store) HasPassword(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	u, ok := s.users[name]
	return ok && u.Hash != ""
}

// SessionKey is the hash the store keeps for a session id: a confirmation that leaves for the
// provider remembers the session by it, never by the id.
func SessionKey(id string) string { return sessionKey(id) }

// RedeemTicket spends a ticket issued for path and returns its session (as Validate does), or nil:
// unknown, spent, run out, another path, or a session that has ended meanwhile.
func (s *Store) RedeemTicket(t, path string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	s.sweepTickets(now)
	h := sessionKey(t)
	tk, ok := s.tickets[h]
	if !ok {
		return nil
	}
	delete(s.tickets, h)
	if tk.path != path || tk.confirmed {
		return nil
	}
	x, ok := s.sessions[tk.key]
	if !ok || s.expired(x, now) {
		return nil
	}
	if now.After(x.lastSeen) {
		x.lastSeen = now
	}
	sess := x.public("")
	return &sess
}

// RedeemSessionTicket spends a ticket issued for TicketSession and opens a session of its own
// for the same account, on the same login method - for a browser on a host without the cookie,
// which then holds that session's id. The store never holds an id, so it cannot hand the
// original's out; the original stays and ends on its own. nil, nil when the ticket is no good.
func (s *Store) RedeemSessionTicket(t, remote, agent string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	s.sweepTickets(now)
	h := sessionKey(t)
	tk, ok := s.tickets[h]
	if !ok {
		return nil, nil
	}
	delete(s.tickets, h)
	if tk.path != TicketSession || tk.confirmed {
		return nil, nil
	}
	_ = s.reload() // an account removed or re-keyed meanwhile has no sessions left
	x, ok := s.sessions[tk.key]
	if !ok || s.expired(x, now) || x.method == methodAnonymous {
		return nil, nil
	}
	return s.openSession(x.user, x.level, x.method, remote, agent)
}

func (s *Store) sweepTickets(now time.Time) {
	for h, tk := range s.tickets {
		if !now.Before(tk.expires) {
			delete(s.tickets, h)
		}
	}
}

// MustChangePassword reports the flag for a user.
func (s *Store) MustChangePassword(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[name]
	return ok && u.MustChangePW
}

// Logout ends one session.
func (s *Store) Logout(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(id)
	if _, ok := s.sessions[key]; ok {
		s.dropSession(key)
		s.saveSessions(true)
	}
}

// LogoutUser ends every session of a user except keep ("sign out everywhere").
func (s *Store) LogoutUser(name, keep string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.dropSessionsOf(name, keep)
	if n > 0 {
		s.saveSessions(true)
	}
	return n
}

// EndSessions ends the sessions the session list names handle (SessionHandle, with or without its
// "……"), of the user name - of every user when name is empty - except the session keep (an id).
// A handle that is not one ends nothing.
func (s *Store) EndSessions(handle, name, keep string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := strings.TrimSuffix(handle, "……")
	if len(h) != handleLen || strings.Trim(h, "0123456789abcdef") != "" {
		return 0
	}
	keepKey := ""
	if keep != "" {
		keepKey = sessionKey(keep)
	}
	n := 0
	for key, x := range s.sessions {
		if key[:handleLen] == h && (name == "" || x.user == name) && key != keepKey {
			s.dropSession(key)
			n++
		}
	}
	if n > 0 {
		s.saveSessions(true)
	}
	return n
}

// Sessions lists live sessions (of one user, or all when name is empty), each with its handle as
// ID.
func (s *Store) Sessions(name string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Session{}
	for key, x := range s.sessions {
		if name == "" || x.user == name {
			out = append(out, x.public(handleOf(key)))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// dropSessionsOf drops every session of a user except the one with the id keep, and counts them;
// the caller writes the store.
func (s *Store) dropSessionsOf(name, keep string) int {
	keepKey := ""
	if keep != "" {
		keepKey = sessionKey(keep)
	}
	n := 0
	for key, x := range s.sessions {
		if x.user == name && key != keepKey {
			s.dropSession(key)
			n++
		}
	}
	return n
}

// dropSession ends a session: its entry, its alias, its mirror files and its tickets.
func (s *Store) dropSession(key string) {
	if x, ok := s.sessions[key]; ok {
		s.dropAlias(x)
	}
	delete(s.sessions, key)
	if s.opt.SessionDir != "" {
		_ = os.Remove(filepath.Join(s.opt.SessionDir, key))
	}
	for h, tk := range s.tickets {
		if tk.key == key {
			delete(s.tickets, h)
		}
	}
}

// mirror writes the session for the lighttpd gate: file name = the hash of the sid, content = the
// user name (what the tclrega shim answers). Its alias, when it has one, goes to the alias mirror
// the same way, with the session's hash on a second line.
func (s *Store) mirror(x *live) {
	if s.opt.SessionDir == "" {
		return
	}
	writeMirror(filepath.Join(s.opt.SessionDir, x.hash), x.user+"\n")
	if x.aliasHash != "" {
		writeMirror(filepath.Join(s.opt.LegacyDir, x.aliasHash), x.user+"\n"+x.hash+"\n")
	}
}

// writeMirror writes one mirror file, world-readable whatever the process's umask: the gate
// (lighttpd) and the tclrega shim (an addon's user) open it, and the unit runs with a umask that
// keeps everything else the daemon creates private.
func writeMirror(path, body string) {
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return
	}
	_ = os.Chmod(path, 0o644)
}

// syncMirror makes the two mirrors hold exactly the live sessions and their aliases: a file a
// previous run left for a session that is gone - or in the old shape, named by the id itself - is
// removed, every live session written. No gap: a restored session's file is there throughout.
func (s *Store) syncMirror() {
	entries, _ := os.ReadDir(s.opt.SessionDir)
	for _, e := range entries {
		if _, ok := s.sessions[e.Name()]; !ok {
			_ = os.Remove(filepath.Join(s.opt.SessionDir, e.Name()))
		}
	}
	entries, _ = os.ReadDir(s.opt.LegacyDir)
	for _, e := range entries {
		if _, ok := s.aliases[e.Name()]; !ok {
			_ = os.Remove(filepath.Join(s.opt.LegacyDir, e.Name()))
		}
	}
	for _, x := range s.sessions {
		s.mirror(x)
	}
}

// ---- the token mirror for the gate (openccu-lite task 307, GitHub issue #3) -------------------
//
// lighttpd's gate accepts an API token as Authorization: Bearer in front of /addons/<segment>/
// when the token holds the ingress scope of that addon (addon:<id>, or Full access). The gate has
// no subrequest, so it reads a mirror like the sessions': one file per stored token in
// GateTokenDir, named by the sha256 of the secret (the hash users.json keeps), 0644 so lighttpd can
// open it. The file is lines of "key value":
//
//	name <token name>          always
//	addons <seg> <seg> …       the URL segments under /addons/ the token opens; "*" for Full access;
//	                           the line is absent when the token opens none
//	expires <unix seconds>     when the token has an expiry
//	ip <cidr>                  one line per allowed range
//
// A rotation's previous secret keeps a file until its grace minute ends (expires says when); an
// ephemeral token (an addon's, the daemon's own) is never mirrored. The mirror is rewritten on every
// token change, after a merge of users.json (a console-made token), at every sweep, and when the
// addons change (SyncGateTokens from the addon token lifecycle: a drop-in may have moved an
// addon's path). Nothing in it is a credential: a hash opens no gate.

// gateSegmentRe is what a segment may look like in the file: no whitespace, no slash.
var gateSegmentRe = regexp.MustCompile(`^[^\s/]+$`)

// gateTokenFiles is the mirror as it should be: file name → content.
func (s *Store) gateTokenFiles() map[string]string {
	now := s.opt.Now()
	out := map[string]string{}
	for _, t := range s.tokens {
		if t.Expires != nil && !now.Before(*t.Expires) {
			continue
		}
		out[t.Hash] = s.gateTokenBody(t, t.Expires)
		if t.PrevHash != "" && t.PrevUntil != nil && now.Before(*t.PrevUntil) {
			until := *t.PrevUntil
			if t.Expires != nil && t.Expires.Before(until) {
				until = *t.Expires
			}
			out[t.PrevHash] = s.gateTokenBody(t, &until)
		}
	}
	return out
}

func (s *Store) gateTokenBody(t *Token, expires *time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name %s\n", t.Name)
	if segs := s.gateSegments(t.Scopes); len(segs) > 0 {
		fmt.Fprintf(&b, "addons %s\n", strings.Join(segs, " "))
	}
	if expires != nil {
		fmt.Fprintf(&b, "expires %d\n", expires.Unix())
	}
	for _, ip := range t.IPs {
		fmt.Fprintf(&b, "ip %s\n", ip)
	}
	return b.String()
}

// gateSegments is the sorted list of URL segments the scopes open at the gate: "*" for Full
// access, else each ingress scope's addon id and the segments AddonSegments adds for it.
func (s *Store) gateSegments(scopes Scopes) []string {
	if scopes.Full() {
		return []string{"*"}
	}
	seen := map[string]bool{}
	var out []string
	for _, sc := range scopes {
		id, ok := AddonOf(sc)
		if !ok {
			continue
		}
		segs := []string{id}
		if s.opt.AddonSegments != nil {
			segs = append(segs, s.opt.AddonSegments(id)...)
		}
		for _, seg := range segs {
			if seg != "*" && gateSegmentRe.MatchString(seg) && !seen[seg] {
				seen[seg] = true
				out = append(out, seg)
			}
		}
	}
	sort.Strings(out)
	return out
}

// syncGateTokens makes GateTokenDir hold exactly gateTokenFiles: stale files go, changed ones are
// written, unchanged ones are left alone. The lock is held.
func (s *Store) syncGateTokens() {
	dir := s.opt.GateTokenDir
	if dir == "" {
		return
	}
	want := s.gateTokenFiles()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if _, ok := want[e.Name()]; !ok {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	for name, body := range want {
		p := filepath.Join(dir, name)
		if cur, err := os.ReadFile(p); err == nil && string(cur) == body {
			continue
		}
		writeMirror(p, body)
	}
}

// SyncGateTokens rewrites the gate's token mirror from users.json as it is now; the addon token
// lifecycle calls it when an addon came, went or changed, since the segments a token opens follow
// the addons' lighttpd drop-ins.
func (s *Store) SyncGateTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	s.syncGateTokens()
}

// Sweep drops expired sessions; call it periodically.
func (s *Store) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload() // a token the console made since reaches the gate's mirror by the next sweep at the latest
	now := s.opt.Now()
	n := 0
	for key, x := range s.sessions {
		if s.expired(x, now) {
			s.dropSession(key)
			n++
		}
	}
	s.sweepTickets(now)
	if n > 0 {
		s.saveSessions(true)
	}
	s.syncGateTokens() // an expired token, or a rotation's previous secret past its minute, leaves the gate
}

// Close writes the last-seen times the session store does not have yet; call it at a clean
// shutdown.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.sessions {
		if x.method != methodAnonymous && !x.lastSeen.Equal(x.saved) {
			s.saveSessions(false)
			return
		}
	}
}

// ---- the session store (B-102, D-67) ----------------------------------------------------------
//
// <state>/sessions/sessions.json, 0600, in a directory of its own (0700) with .nobackup: the
// firmware's backup tars /usr/local with --exclude-tag=.nobackup, which leaves out the whole
// directory holding the tag, so the tag cannot sit in the state directory itself. Each session is
// its hash, never its id; whoever reads the file holds no credential.

// sessionsDoc is the content of the session store.
type sessionsDoc struct {
	Version  int             `json:"version"`
	Sessions []storedSession `json:"sessions"`
}

type storedSession struct {
	Hash   string `json:"hash"` // sha256 of the id, lower-case hex
	User   string `json:"user"`
	Role   Role   `json:"role"`
	Level  Level  `json:"level,omitempty"`
	Method string `json:"method"`
	// Created and Expires (created + the maximum age) are fixed at the login; LastSeen is at most
	// LastSeenEvery old, IdleExpires is LastSeen + the idle timeout.
	Created     time.Time `json:"created"`
	LastSeen    time.Time `json:"last_seen"`
	IdleExpires time.Time `json:"idle_expires"`
	Expires     time.Time `json:"expires"`
	Remote      string    `json:"remote,omitempty"`
	Agent       string    `json:"agent,omitempty"`
	// LegacyHash is the sha256 of the session's ?sid=@..@ alias, when it has one (task 125): the
	// alias keeps working at the gate across a restart; the alias itself is not stored.
	LegacyHash string `json:"legacy_hash,omitempty"`
}

// loadSessions restores the stored sessions that are still valid: known hash shape, a user that
// still exists and fits the login method, a method the running mode offers, neither expiry
// passed. The role is the user's in users.json now. An unreadable or corrupt store restores
// nothing - everyone logs in again - with a warning, and never stops the start.
func (s *Store) loadSessions() {
	path := s.opt.SessionFile
	if err := s.sessionDir(); err != nil {
		slog.Warn("sessions: the session store's directory is not usable; sessions end with occulited", "path", path, "err", err)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Warn("sessions: cannot read the session store; everyone logs in again", "path", path, "err", err)
		return
	}
	var doc sessionsDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		slog.Warn("sessions: the session store is corrupt; everyone logs in again", "path", path, "err", err)
		s.saveSessions(true)
		return
	}
	if doc.Version == 1 {
		// the store of an occulited before task 125: its sessions have ten-character ids, which no
		// request can present any more (IsSessionID), and none of them may ever count as an alias
		slog.Info("sessions: the session store is from before the 128-bit session ids; everyone logs in once", "path", path)
		s.saveSessions(true)
		return
	}
	if doc.Version != sessionsVersion {
		slog.Warn("sessions: the session store has a version this occulited does not know; everyone logs in again", "path", path, "version", doc.Version)
		s.saveSessions(true)
		return
	}
	restore := map[string]bool{}
	for _, m := range s.opt.RestoreMethods {
		restore[m] = true
	}
	now := s.opt.Now()
	dropped := 0
	for _, e := range doc.Sessions {
		u := s.users[e.User]
		switch {
		case !sessionKeyRe.MatchString(e.Hash), u == nil, !restore[e.Method], s.sessions[e.Hash] != nil,
			e.Method == MethodPassword && u.Hash == "", // a password session of an account that has none
			e.Created.IsZero(), e.LastSeen.IsZero(), e.Expires.IsZero(), e.IdleExpires.IsZero(),
			now.After(e.Expires), now.After(e.IdleExpires),
			e.LegacyHash != "" && (!sessionKeyRe.MatchString(e.LegacyHash) || s.aliases[e.LegacyHash] != "" || e.LegacyHash == e.Hash):
			dropped++
			continue
		}
		s.sessions[e.Hash] = &live{hash: e.Hash, user: e.User, role: u.Role, level: u.Level, acct: u.ID, method: e.Method, created: e.Created, lastSeen: e.LastSeen,
			expires: e.Expires, saved: e.LastSeen, remote: e.Remote, agent: e.Agent, aliasHash: e.LegacyHash}
		if e.LegacyHash != "" {
			s.aliases[e.LegacyHash] = e.Hash
		}
	}
	if len(doc.Sessions) > 0 {
		slog.Info("sessions: restored from the session store", "sessions", len(s.sessions), "dropped", dropped)
	}
	if dropped > 0 {
		s.saveSessions(true)
	}
}

// sessionDir makes the session store's directory (0700) and its .nobackup.
func (s *Store) sessionDir() error {
	dir := filepath.Dir(s.opt.SessionFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tag := filepath.Join(dir, ".nobackup")
	if _, err := os.Stat(tag); err == nil {
		return nil
	}
	return os.WriteFile(tag, nil, 0o644)
}

// saveSessions writes the session store: every session but auth-off's, atomically. It is never
// written without its .nobackup beside it. revoked says the write ends a session: when it fails,
// the store is removed (or emptied) rather than left with a session that must not come back at
// the next start.
func (s *Store) saveSessions(revoked bool) {
	path := s.opt.SessionFile
	if path == "" {
		return
	}
	doc := sessionsDoc{Version: sessionsVersion, Sessions: []storedSession{}}
	for _, x := range s.sessions {
		if x.method == methodAnonymous {
			continue
		}
		doc.Sessions = append(doc.Sessions, storedSession{Hash: x.hash, User: x.user, Role: x.role, Level: x.level, Method: x.method, Created: x.created,
			LastSeen: x.lastSeen, IdleExpires: x.lastSeen.Add(s.opt.IdleTimeout), Expires: x.expires, Remote: x.remote, Agent: x.agent, LegacyHash: x.aliasHash})
	}
	sort.Slice(doc.Sessions, func(i, j int) bool {
		if a, b := doc.Sessions[i].Created, doc.Sessions[j].Created; !a.Equal(b) {
			return a.Before(b)
		}
		return doc.Sessions[i].Hash < doc.Sessions[j].Hash
	})
	b, _ := json.MarshalIndent(doc, "", "  ")
	err := s.sessionDir()
	if err == nil {
		_, err = writeAtomic(path, append(b, '\n'), 0o600)
	}
	if err != nil {
		slog.Warn("sessions: cannot write the session store; sessions may not survive a restart", "path", path, "err", err)
		if revoked {
			if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				if terr := os.Truncate(path, 0); terr != nil {
					slog.Error("sessions: cannot remove the session store either; a session that just ended may be restored at the next start", "path", path, "err", rerr)
				}
			}
		}
		return
	}
	for _, x := range s.sessions {
		x.saved = x.lastSeen
	}
}

// sessionsVersion is the session store's format: 2 since task 125 (26-character ids, the
// alias's hash); a version-1 store is dropped at the first start, see loadSessions.
const sessionsVersion = 2

// SessionStorePath is the session store under a state directory.
func SessionStorePath(stateDir string) string {
	return filepath.Join(stateDir, "sessions", "sessions.json")
}

// accountsChanged follows a change another writer made to users.json - the console's `passwd`, a
// hand edit, a restored file: the sessions of an account that is gone or has a new password end,
// the sessions of an account with another role take that role. old is the accounts before.
func (s *Store) accountsChanged(old map[string]*User) {
	ended, changed := 0, false
	for key, x := range s.sessions {
		if x.method == methodAnonymous {
			continue
		}
		u := s.users[x.user]
		if u == nil || (old[x.user] != nil && old[x.user].Hash != u.Hash) {
			s.dropSession(key)
			ended++
			continue
		}
		if x.role != u.Role || x.level != u.Level {
			x.role, x.level, changed = u.Role, u.Level, true
		}
	}
	if s.opt.Changed != nil {
		go s.opt.Changed()
	}
	if ended > 0 {
		slog.Info("sessions: users.json changed outside occulited; the sessions of removed accounts and changed passwords end", "sessions", ended)
		s.saveSessions(true)
	} else if changed {
		s.saveSessions(false)
	}
}

// endStoredSessions removes one account's sessions from the session store at path, where there is
// one - for a console command that runs beside the daemon or instead of it. It never creates the
// store or its directory (a command running as root would leave them root's), and leaves a store
// it cannot parse alone: the daemon restores nothing from that.
func endStoredSessions(path, name string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc sessionsDoc
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	kept := make([]storedSession, 0, len(doc.Sessions))
	for _, e := range doc.Sessions {
		if e.User != name {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(doc.Sessions) {
		return nil
	}
	doc.Sessions = kept
	out, _ := json.MarshalIndent(doc, "", "  ")
	_, err = writeAtomic(path, append(out, '\n'), 0o600)
	return err
}

// ConsoleSetPassword is what `occulited passwd <user>` uses: it edits users.json directly, so a
// locked-out administrator can recover from SSH or the console; the running daemon notices the
// changed mtime on its next lookup.
func ConsoleSetPassword(dir, name, pw string) error {
	s, err := Open(dir, Options{})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[name]
	if !ok {
		if len(s.users) == 0 {
			return s.createLocked(name, &pw, LevelAdminister, false)
		}
		return ErrUnknownUser
	}
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	u.Hash = h
	u.MustChangePW = false
	if err := s.save(); err != nil {
		return err
	}
	// B-102: a password set here ends the account's sessions, as one set in the web interface
	// does. A running daemon drops them when it sees users.json change; for one that is not
	// running they leave the session store now.
	if err := endStoredSessions(SessionStorePath(dir), name); err != nil {
		return fmt.Errorf("the password is set, but the account's stored sessions could not be ended: %w", err)
	}
	return nil
}

// ConsoleCreateToken is what `occulited token <name>` uses: it writes the token into users.json
// directly, and a running daemon finds it on the next credential check. While no user exists it
// refuses with ErrSetupRequired: provisioning starts with the first administrator.
func ConsoleCreateToken(dir, name string, opt TokenOptions) (string, error) {
	s, err := Open(dir, Options{})
	if err != nil {
		return "", err
	}
	if s.SetupRequired() {
		return "", ErrSetupRequired
	}
	return s.CreateToken(name, opt)
}

// ---- API tokens -------------------------------------------------------------------------------
//
// A token is a long-lived credential for a program - a daemon addon that needs names and rooms
// without a user logging in, a CI job, a Node-RED flow. It carries scopes (scopes.go), is stored
// as a sha256 hash, and is shown to the administrator exactly once. It may carry an expiry and
// a list of address ranges it is accepted from (D-79). The box provisions one "local" token
// itself, with meta:read alone, and writes it to <state>/local-token (0644) so addons running
// on the box - as root or as their own user - can read names and rooms without anyone pasting
// anything. An addon whose catalogue entry declares api_scopes gets a token of its own, minted
// at every start and never stored (MintAddonToken).

// Token is an API token as stored (the secret itself is never stored).
type Token struct {
	Name   string `json:"name"`
	Scopes Scopes `json:"scopes"`
	// Role is what a token carried before task 66; read for the migration (migrate), never
	// written again: an admin token becomes Full access, a user token the read scopes, a led
	// token the scope led.
	Role     Role       `json:"role,omitempty"`
	Hash     string     `json:"hash,omitempty"` // sha256 hex of the secret; blanked in listings
	Prefix   string     `json:"prefix"`         // the first 8 characters after olt_, for the list
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"last_used,omitempty"`
	// Expires ends the token (D-79); nil = never.
	Expires *time.Time `json:"expires,omitempty"`
	// IPs are the address ranges (CIDR) the token is accepted from (D-79); none = from anywhere.
	// Checked against the address lighttpd forwards, so a program on the box itself is never
	// held to it - it can reach occulited's listener directly, with whatever address it likes.
	IPs []string `json:"ips,omitempty"`
	// Client is a paired program's record (task 219): what asked, who approved, from where it
	// was last used. nil for a token made in the panel or on the console.
	Client *TokenClient `json:"client,omitempty"`
	// PrevHash is the secret a rotation replaced, accepted until PrevUntil (task 219: a program
	// that rotates keeps working while it stores the new secret).
	PrevHash  string     `json:"prev_hash,omitempty"`
	PrevUntil *time.Time `json:"prev_until,omitempty"`
}

// TokenClient is what a paired token carries about its program (task 219).
type TokenClient struct {
	App        string    `json:"app"`
	AppVersion string    `json:"app_version,omitempty"`
	Instance   string    `json:"instance,omitempty"`
	Label      string    `json:"label"`
	PairedAt   time.Time `json:"paired_at"`
	PairedBy   string    `json:"paired_by"`
	Address    string    `json:"address"`
	// Fingerprint is the certificate (hex SHA-256) the code was bound to, "" over plain HTTP
	Fingerprint string `json:"fingerprint,omitempty"`
	// Access is {} for a pairing of addons alone (openccu-lite B-296), absent only in a record
	// from before the fix
	Access map[string]string `json:"access,omitzero"`
	// Addons are the addon ids whose ingress the pairing asked for (openccu-lite task 307)
	Addons      []string `json:"addons,omitempty"`
	LastAddress string   `json:"last_address,omitempty"`
}

// migrate turns a record from before task 66 - a role, no scopes - into its scopes. A record
// that has both keeps its scopes.
func (t *Token) migrate() {
	if len(t.Scopes) == 0 && t.Role != "" {
		t.Scopes = RoleScopes(t.Role)
	}
	t.Role = ""
}

// TokenOptions is what a token is created with.
type TokenOptions struct {
	Scopes  Scopes
	Expires *time.Time
	IPs     []string
}

// ErrBadIPRange is an allowed-range entry that is not an address or a CIDR range.
var ErrBadIPRange = errors.New("allowed address must be an IP address or a CIDR range")

// ErrTokenExpiry is an expiry in the past.
var ErrTokenExpiry = errors.New("the expiry must lie in the future")

// parseRanges checks the allowed ranges and writes them in their canonical form; a bare address
// becomes its /32 or /128.
func parseRanges(ips []string) ([]string, error) {
	var out []string
	for _, e := range ips {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, fmt.Errorf("%w: %q", ErrBadIPRange, e)
			}
			if ip.To4() != nil {
				e += "/32"
			} else {
				e += "/128"
			}
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrBadIPRange, e)
		}
		out = append(out, n.String())
	}
	return out, nil
}

// fromAllowed reports whether remote lies in one of the token's ranges; a token without ranges
// is accepted from anywhere, one with ranges never without a known remote.
func (t *Token) fromAllowed(remote string) bool {
	if len(t.IPs) == 0 {
		return true
	}
	ip := net.ParseIP(remote)
	if ip == nil {
		return false
	}
	for _, e := range t.IPs {
		if _, n, err := net.ParseCIDR(e); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// TokenPrefix marks a token in a bearer header or ?sid=; the rest is 32 hex characters.
const TokenPrefix = "olt_"

var (
	tokenRe   = regexp.MustCompile(`^olt_[a-f0-9]{32}$`)
	tokenName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{1,31}$`)
	// ErrBadTokenName is the name rule.
	ErrBadTokenName = errors.New("token name must match [a-z0-9][a-z0-9_.-]* (2-32 characters)")
	// ErrDuplicateToken is returned by CreateToken for a taken name.
	ErrDuplicateToken = errors.New("token exists")
	// ErrUnknownToken is returned by DeleteToken.
	ErrUnknownToken = errors.New("unknown token")
)

// IsToken reports whether a credential string is a token rather than a session id.
func IsToken(s string) bool { return tokenRe.MatchString(s) }

func hashToken(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// CreateToken creates a token and returns the secret - the only time it is available. The
// options' scopes must name at least one known scope; the expiry, when set, lies in the
// future; the ranges are addresses or CIDR ranges.
func (s *Store) CreateToken(name string, opt TokenOptions) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	return s.createTokenLocked(name, opt)
}

func (s *Store) createTokenLocked(name string, opt TokenOptions) (string, error) {
	if !tokenName.MatchString(name) {
		return "", ErrBadTokenName
	}
	scopes, err := ParseScopes(opt.Scopes.Strings())
	if err != nil {
		return "", err
	}
	ips, err := parseRanges(opt.IPs)
	if err != nil {
		return "", err
	}
	if opt.Expires != nil && !opt.Expires.After(s.opt.Now()) {
		return "", ErrTokenExpiry
	}
	for _, t := range s.tokens {
		if t.Name == name {
			return "", ErrDuplicateToken
		}
	}
	secret, t, err := s.newToken(name, scopes)
	if err != nil {
		return "", err
	}
	t.Expires, t.IPs = opt.Expires, ips
	s.tokens = append(s.tokens, t)
	return secret, s.save()
}

// newToken draws a secret and builds the record for it.
func (s *Store) newToken(name string, scopes Scopes) (string, *Token, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	secret := TokenPrefix + hex.EncodeToString(b)
	return secret, &Token{Name: name, Scopes: scopes, Hash: hashToken(secret), Prefix: secret[4:12], Created: s.opt.Now()}, nil
}

// MintEphemeral makes a token that lives in this process only - never in users.json, gone with
// the process - and returns its secret. A token of that name is replaced. The scopes are taken
// as given (the caller filtered them; AddonScopes for an addon's). For the addons' API tokens
// and the daemon's own credential for its update checks.
func (s *Store) MintEphemeral(name string, scopes Scopes) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scopes = scopes.Normalize()
	if len(scopes) == 0 {
		return "", fmt.Errorf("%w: a token needs at least one scope", ErrBadScope)
	}
	secret, t, err := s.newToken(name, scopes)
	if err != nil {
		return "", err
	}
	s.ephemeral[name] = t
	return secret, nil
}

// ConsoleTokenName is the console's own credential (occulited task 22): minted at every start,
// its secret in a file only root reads (system.ConsoleTokenFile), accepted from the loopback
// alone. `occulited update` uses it, so the command line and the web UI go through the same
// routes; the journal names it as token:console.
const ConsoleTokenName = "console"

// ConsoleTokenScopes is what the console's credential holds: what `occulited update` calls.
var ConsoleTokenScopes = Scopes{ScopeSystemRead, ScopePower, ScopeBackup}

// MintConsoleToken mints the console's credential, accepted from the loopback only (the
// command runs on the system; behind lighttpd a request carries the browser's address).
func (s *Store) MintConsoleToken() (string, error) {
	secret, err := s.MintEphemeral(ConsoleTokenName, ConsoleTokenScopes)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.ephemeral[ConsoleTokenName].IPs = []string{"127.0.0.0/8", "::1/128"}
	s.mu.Unlock()
	return secret, nil
}

// DropEphemeral forgets an ephemeral token.
func (s *Store) DropEphemeral(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ephemeral, name)
}

// EphemeralScopes is what the ephemeral token of that name holds; nil when there is none.
func (s *Store) EphemeralScopes(name string) Scopes {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.ephemeral[name]; ok {
		return append(Scopes(nil), t.Scopes...)
	}
	return nil
}

// AddonTokenName is the ephemeral token of an addon: "addon:<id>"; its session is
// "token:addon:<id>".
func AddonTokenName(id string) string { return "addon:" + id }

// MintAddonToken mints the API token of an addon from its catalogue declaration (task 66):
// every known scope but the ones in AddonNever, which are refused - the caller logs them and
// writes the secret to the addon's run file. Nothing is minted when nothing stands, and an
// earlier token of the addon is dropped then. What the system package calls at every start,
// after an install and after a policy change.
func (s *Store) MintAddonToken(id string, declared []string) (secret string, granted, refused []string, err error) {
	scopes, refused := AddonScopes(declared)
	if len(scopes) == 0 {
		s.DropEphemeral(AddonTokenName(id))
		return "", nil, refused, nil
	}
	secret, err = s.MintEphemeral(AddonTokenName(id), scopes)
	return secret, scopes.Strings(), refused, err
}

// DropAddonToken forgets an addon's API token (uninstall).
func (s *Store) DropAddonToken(id string) { s.DropEphemeral(AddonTokenName(id)) }

// CreatePairedToken mints the token of an approved pairing (task 219): the name is base, or
// base-2, -3, ... when taken; the client record rides along. The secret is returned once.
func (s *Store) CreatePairedToken(base string, scopes Scopes, client TokenClient) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	taken := map[string]bool{}
	for _, t := range s.tokens {
		taken[t.Name] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	if !tokenName.MatchString(name) {
		return "", "", ErrBadTokenName
	}
	scopes = scopes.Normalize()
	for _, sc := range scopes {
		for _, never := range AddonNever {
			if sc == never {
				return "", "", fmt.Errorf("%w: pairing never grants %s", ErrBadScope, sc)
			}
		}
	}
	secret, t, err := s.newToken(name, scopes)
	if err != nil {
		return "", "", err
	}
	c := client
	t.Client = &c
	s.tokens = append(s.tokens, t)
	return name, secret, s.save()
}

// ErrWiden is a PATCH that would give a token a scope it does not have.
var ErrWiden = errors.New("a token's scopes can only be narrowed; widening is a new token or a new pairing")

// UpdateToken renames a paired token's label and narrows its scopes (task 219); nil leaves a
// field as it is. The name, which logs and programs know it by, never changes.
func (s *Store) UpdateToken(name string, label *string, scopes []string) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	for _, t := range s.tokens {
		if t.Name != name {
			continue
		}
		if scopes != nil {
			want, err := ParseScopes(scopes)
			if err != nil {
				return Token{}, err
			}
			for _, w := range want {
				if !t.Scopes.Has(w) {
					return Token{}, fmt.Errorf("%w (%s)", ErrWiden, w)
				}
			}
			t.Scopes = want
		}
		if label != nil {
			l := strings.TrimSpace(*label)
			if len(l) > 80 {
				l = l[:80]
			}
			if t.Client == nil {
				t.Client = &TokenClient{}
			}
			t.Client.Label = l
		}
		if err := s.save(); err != nil {
			return Token{}, err
		}
		c := *t
		c.Hash, c.PrevHash = "", ""
		return c, nil
	}
	return Token{}, ErrUnknownToken
}

// RotateToken gives a stored token a new secret; the old one stays valid for grace (task 219).
func (s *Store) RotateToken(name string, grace time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	for _, t := range s.tokens {
		if t.Name != name {
			continue
		}
		secret, nt, err := s.newToken(name, t.Scopes)
		if err != nil {
			return "", err
		}
		until := s.opt.Now().Add(grace)
		t.PrevHash, t.PrevUntil = t.Hash, &until
		t.Hash, t.Prefix = nt.Hash, nt.Prefix
		return secret, s.save()
	}
	return "", ErrUnknownToken
}

// DeleteToken revokes a token.
func (s *Store) DeleteToken(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	for i, t := range s.tokens {
		if t.Name == name {
			s.tokens = append(s.tokens[:i], s.tokens[i+1:]...)
			return s.save()
		}
	}
	return ErrUnknownToken
}

// Tokens lists the tokens (without secrets), sorted by name.
func (s *Store) Tokens() []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	out := make([]Token, 0, len(s.tokens))
	for _, t := range s.tokens {
		c := *t
		c.Hash, c.PrevHash, c.PrevUntil = "", "", nil
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ValidateFrom is Validate for a request from a known address: a token that carries allowed
// ranges is accepted from those alone, and one with an expiry not past it. Validate - without
// an address - never accepts a token with ranges.
func (s *Store) ValidateFrom(id, remote string) *Session {
	if !IsToken(id) {
		return s.Validate(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validateTokenLocked(id, remote)
}

// validateTokenLocked resolves a secret to a synthetic session: ID and User are "token:<name>",
// so handlers and logs can tell a program from a person. Nothing is stored in the session table.
// The stored tokens first, then the ephemeral ones (an addon's, the daemon's own).
func (s *Store) validateTokenLocked(secret, remote string) *Session {
	_ = s.reload()
	h := hashToken(secret)
	now := s.opt.Now()
	found := func(t *Token) *Session {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) != 1 &&
			(t.PrevHash == "" || t.PrevUntil == nil || !now.Before(*t.PrevUntil) || subtle.ConstantTimeCompare([]byte(t.PrevHash), []byte(h)) != 1) {
			return nil
		}
		if t.Expires != nil && !now.Before(*t.Expires) {
			return nil
		}
		if !t.fromAllowed(remote) {
			return nil
		}
		return &Session{ID: TokenSessionPrefix + t.Name, User: TokenSessionPrefix + t.Name, Scopes: append(Scopes(nil), t.Scopes...), Created: t.Created, LastSeen: now}
	}
	for _, t := range s.tokens {
		if sess := found(t); sess != nil {
			// task 219: a paired program's list line says where it was last seen from
			moved := t.Client != nil && remote != "" && t.Client.LastAddress != remote
			if moved {
				t.Client.LastAddress = remote
			}
			if moved || t.LastUsed == nil || now.Sub(*t.LastUsed) > time.Minute {
				t.LastUsed = &now
				_ = s.save()
			}
			return sess
		}
	}
	for _, t := range s.ephemeral {
		if sess := found(t); sess != nil {
			t.LastUsed = &now
			return sess
		}
	}
	return nil
}

// LocalTokenName is the name of the box's own token, and LocalTokenScopes what it holds
// (D-85): names and rooms, nothing else - the journal and the system pages are no longer
// reachable with it.
const LocalTokenName = "local"

// LocalTokenScopes is the local token's list.
var LocalTokenScopes = Scopes{ScopeMetaRead}

// EnsureLocalToken makes sure a token named "local" with meta:read alone exists and its secret
// is in path (0644, world-readable on purpose: it is the box's read token for programs running
// on it, and a confined addon - its own user, D-36 - must be able to read it too; the state
// directory is 0711 for the same reason, reachable but not listable). A missing file or a
// missing token regenerates both: the secret cannot be recovered from the hash, and the file is
// the only copy. A local token from before task 66 keeps its secret and loses every scope but
// meta:read - the programs that read the file keep working, narrower.
func (s *Store) EnsureLocalToken(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	if b, err := os.ReadFile(path); err == nil {
		secret := strings.TrimSpace(string(b))
		if IsToken(secret) {
			for _, t := range s.tokens {
				if t.Name == LocalTokenName && t.Hash == hashToken(secret) {
					if asJSON(t.Scopes) == asJSON(LocalTokenScopes) && t.Expires == nil && len(t.IPs) == 0 {
						return nil
					}
					t.Scopes, t.Expires, t.IPs = append(Scopes(nil), LocalTokenScopes...), nil, nil
					return s.save()
				}
			}
		}
	}
	for i, t := range s.tokens {
		if t.Name == LocalTokenName {
			s.tokens = append(s.tokens[:i], s.tokens[i+1:]...)
			break
		}
	}
	secret, err := s.createTokenLocked(LocalTokenName, TokenOptions{Scopes: LocalTokenScopes})
	if err != nil {
		return err
	}
	_, err = writeAtomic(path, []byte(secret+"\n"), 0o644)
	return err
}

// LoginExternal opens a session for a name the identity provider has just authenticated (task
// 19, D-53): the account of exactly that name, with or without a password, with the role it has
// here (D-54). No such account: ErrUnknownUser, and nothing is written - nothing is ever created
// from a provider identity. The provider is trusted with the name; that is the design. Nothing
// here counts towards the lockout - the provider did the authentication.
func (s *Store) LoginExternal(provider, name, remote, agent string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	u, ok := s.users[name]
	if !ok {
		return nil, ErrUnknownUser
	}
	now := s.opt.Now()
	u.LastProviderLogin = &now
	if err := s.save(); err != nil {
		return nil, err
	}
	return s.openSession(name, u.Level, provider, remote, agent)
}
