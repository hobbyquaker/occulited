// Package pairing is how a program off the system gets an API token without anybody copying a
// secret (openccu-lite task 219, the maintainer's decisions of 2026-09-24): the program asks
// without a credential, with the access it wants per area; both it and the system compute the
// same six-digit code; an administrator sees the request on the Status page with that code and
// approves it with one click - all or nothing - and the waiting program receives its token.
//
// The code is bound to the certificate the program sees: code = six digits of
// SHA-256(nonce ‖ client_nonce ‖ fingerprint), where nonce is the system's random half,
// client_nonce the program's (committed to as SHA-256(client_nonce) in the request, revealed with
// its first poll, so a man in the middle cannot try certificates until the codes match), and
// fingerprint the SHA-256 of the TLS certificate the program saw - the system's own on this side.
// Over plain HTTP the fingerprint is empty: the code still binds the request, not the connection.
//
// Nothing here is a credential until an administrator approves: a request lives five minutes, at
// most five wait, one per address and program, ten an hour per address, and a rejection mutes the
// address and program for ten minutes. Only the local networks may ask.
//
// The area backup (occulited task 135) is the one grant that asks more of the administrator: a
// backup holds the radio keys, so approving a request for it needs the administrator's
// confirmation besides the code (the HTTP layer redeems it, Approve is told), and the token comes
// limited to the address the request came from (TokenRanges).
package pairing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// The limits (task 219).
const (
	Lifetime     = 5 * time.Minute
	MaxPending   = 5
	PerHour      = 10
	MuteFor      = 10 * time.Minute
	Interval     = 2 * time.Second
	MaxWait      = 30 * time.Second
	maxPurposeLn = 200
)

// The states of a request.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateRejected = "rejected"
	StateExpired  = "expired"
)

// Errors the HTTP layer maps.
var (
	ErrOff       = errors.New("pairing is switched off on this system: an administrator switches it on in the API tokens panel, or creates a token there")
	ErrNotLocal  = errors.New("pairing is open to the local networks only")
	ErrLimit     = errors.New("too many pairing requests")
	ErrMuted     = errors.New("a request of this program from this address was rejected a moment ago")
	ErrInvalid   = errors.New("invalid pairing request")
	ErrUnknown   = errors.New("no such pairing request (any more)")
	ErrPoll      = errors.New("not this request's poll secret")
	ErrSlowDown  = errors.New("slow down")
	ErrWrongCode = errors.New("the code is not this request's")
	ErrNotReady  = errors.New("the program has not revealed its half of the code yet")
	// ErrConfirmRequired is an approval of a request for backups without a valid confirmation:
	// the request stays pending and nothing is muted.
	ErrConfirmRequired = errors.New("a request for backups is approved only with the administrator's confirmation (password, passkey or a fresh login at the provider): a backup holds the radio keys")
)

// The areas and their levels (task 219's decided table): each level carries the scopes below it.
// What pairing never grants - *, auth:admin, radio:keys, power - is in no level (auth.PairingNever).
var levels = map[string]map[string]auth.Scopes{
	"devices": {
		"read":       {auth.ScopeRPCRead},
		"operate":    {auth.ScopeRPCRead, auth.ScopeRPCOperate},
		"configure":  {auth.ScopeRPCRead, auth.ScopeRPCOperate, auth.ScopeRPCConfigure},
		"administer": {auth.ScopeRPCRead, auth.ScopeRPCOperate, auth.ScopeRPCConfigure, auth.ScopeRPCAdmin},
	},
	"names": {
		"read":      {auth.ScopeMetaRead},
		"configure": {auth.ScopeMetaRead, auth.ScopeMetaWrite},
	},
	"system": {
		"read":      {auth.ScopeSystemRead, auth.ScopeLogsRead},
		"configure": {auth.ScopeSystemRead, auth.ScopeLogsRead, auth.ScopeSystemWrite},
	},
	// occulited task 135: approved only with the administrator's confirmation
	"backup": {
		"download": {auth.ScopeBackup},
	},
}

// Areas lists the areas in the card's order.
var Areas = []string{"devices", "names", "system", "backup"}

// TokenRanges is what a token for backups is limited to (occulited task 135): an IPv4 address as
// its /32; a global or unique-local IPv6 address as its /64, because a client's temporary addresses
// (RFC 8981) change within that prefix about daily and a /128 would end a nightly backup a day
// later, while the /64 is still the one network the request came from; a link-local or loopback
// IPv6 address as its /128, since those do not move and their /64 is no one network. nil for what
// is no address.
func TokenRanges(addr string) []string {
	ip := net.ParseIP(addr)
	switch {
	case ip == nil:
		return nil
	case ip.To4() != nil:
		return []string{ip.To4().String() + "/32"}
	case ip.IsLoopback(), ip.IsLinkLocalUnicast():
		return []string{ip.String() + "/128"}
	}
	return []string{(&net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()}
}

// needsConfirmation says whether approving these scopes asks the administrator again.
func needsConfirmation(s auth.Scopes) bool { return slices.Contains(s, auth.ScopeBackup) }

// ScopesFor maps an access request to scopes; an unknown area or level is ErrInvalid, and so is
// an access that asks for nothing.
func ScopesFor(access map[string]string) (auth.Scopes, error) {
	out, err := scopesForAccess(access)
	if err == nil && len(out) == 0 {
		return nil, fmt.Errorf("%w: ask for access to at least one area", ErrInvalid)
	}
	return out, err
}

// scopesForAccess is ScopesFor without the "at least one" rule: empty when nothing is asked, for an
// ask that names addons alone (task 307).
func scopesForAccess(access map[string]string) (auth.Scopes, error) {
	var out auth.Scopes
	for area, level := range access {
		if level == "" || level == "none" {
			continue
		}
		lv, ok := levels[area]
		if !ok {
			return nil, fmt.Errorf("%w: unknown area %q (devices, names, system, backup)", ErrInvalid, area)
		}
		s, ok := lv[level]
		if !ok {
			var names []string
			for n := range lv {
				names = append(names, n)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("%w: %s has no level %q (%s)", ErrInvalid, area, level, strings.Join(names, ", "))
		}
		out = append(out, s...)
	}
	return out.Normalize(), nil
}

// Code is the six digits both sides compute.
func Code(nonce, clientNonce, fingerprint []byte) string {
	h := sha256.New()
	h.Write(nonce)
	h.Write(clientNonce)
	h.Write(fingerprint)
	sum := h.Sum(nil)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1_000_000)
}

// Ask is a program's request.
type Ask struct {
	App        string            `json:"app"`
	AppVersion string            `json:"app_version"`
	Instance   string            `json:"instance"`
	Name       string            `json:"name"`
	Access     map[string]string `json:"access"`
	Purpose    map[string]string `json:"purpose"`
	// Addons are the installed addons whose ingress the program asks for (openccu-lite task 307):
	// the scope addon:<id> each, which opens that addon's pages behind lighttpd's gate for a
	// token sent as Authorization: Bearer. An ask may consist of addons alone.
	Addons []string `json:"addons"`
	// Commit is hex SHA-256 of the program's client_nonce
	Commit string `json:"commit"`
}

// AddonRef names an addon the request asks for, as the card shows it.
type AddonRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MaxAddons is how many addons one request may ask for.
const MaxAddons = 8

// accessMap is the access as the list answers it: an object, empty when nothing was asked (B-296).
func accessMap(a map[string]string) map[string]string {
	if a == nil {
		return map[string]string{}
	}
	return a
}

// Answer is the 202 to a request.
type Answer struct {
	ID        string `json:"id"`
	Poll      string `json:"poll"`
	Nonce     string `json:"nonce"`
	ExpiresIn int    `json:"expires_in"`
	Interval  int    `json:"interval"`
	// Fingerprint is the certificate the system bound the code to (hex), "" over plain HTTP
	Fingerprint string `json:"fingerprint"`
}

// Result is what a poll answers.
type Result struct {
	State  string   `json:"state"`
	Token  string   `json:"token,omitempty"`
	Name   string   `json:"name,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
	// Access is {} rather than absent in an approved answer of addons alone (B-296): omitzero
	// leaves it out of the pending, rejected and expired answers only
	Access map[string]string `json:"access,omitzero"`
	Addons []string          `json:"addons,omitempty"`
	// IPs are the ranges the token is limited to, for a token for backups (occulited task 135)
	IPs []string `json:"ips,omitempty"`
}

// View is one request as the card shows it: never the poll secret.
type View struct {
	ID          string            `json:"id"`
	App         string            `json:"app"`
	AppVersion  string            `json:"app_version,omitempty"`
	Instance    string            `json:"instance,omitempty"`
	Name        string            `json:"name"`
	Address     string            `json:"address"`
	Access      map[string]string `json:"access"`
	Purpose     map[string]string `json:"purpose,omitempty"`
	Addons      []AddonRef        `json:"addons,omitempty"`
	Scopes      []string          `json:"scopes"`
	Code        string            `json:"code"`
	Fingerprint string            `json:"fingerprint,omitempty"` // colon-separated, for the card's certificate line
	Created     time.Time         `json:"created"`
	Expires     time.Time         `json:"expires"`
	// LookAlike: another pending request under the same program and instance, or from the same
	// address - "compare the code carefully"
	LookAlike bool `json:"look_alike,omitempty"`
	// NeedsConfirmation: approving asks for the administrator's confirmation, and TokenIPs are the
	// ranges the token will be limited to (occulited task 135: a request for backups)
	NeedsConfirmation bool     `json:"needs_confirmation,omitempty"`
	TokenIPs          []string `json:"token_ips,omitempty"`
}

type request struct {
	id          string
	pollHash    [32]byte
	nonce       []byte
	commit      []byte
	clientNonce []byte
	fp          []byte
	ask         Ask
	addons      []AddonRef
	scopes      auth.Scopes
	address     string
	created     time.Time
	expires     time.Time
	state       string
	result      Result
	lastPoll    time.Time
	changed     chan struct{} // closed and replaced on every change
}

// Minter makes the token of an approved request (the auth store).
type Minter interface {
	CreatePairedToken(base string, scopes auth.Scopes, ips []string, client auth.TokenClient) (name, secret string, err error)
}

// Manager keeps the pending requests.
type Manager struct {
	Minter Minter
	// Enabled is the switch (occulited.json auth.pairing; on unless switched off)
	Enabled func() bool
	// Local says whether an address is on the local networks
	Local func(addr string) bool
	// AddonName answers an installed addon's display name, and whether the addon is installed at
	// all (openccu-lite task 307: an ask for an addon's ingress). nil: no request may ask for one.
	AddonName func(id string) (string, bool)
	Log       *slog.Logger
	Now       func() time.Time
	// OnChange is told after every change of the pending list (the card's stream)
	OnChange func()

	mu       sync.Mutex
	reqs     map[string]*request
	mutes    map[string]time.Time
	perHour  map[string][]time.Time
	watchers map[chan struct{}]struct{}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

var (
	appRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$`)
	hex64  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	textRe = regexp.MustCompile(`^[^\x00-\x1f\x7f]*$`)
)

func randHex(n int) (string, []byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(b), b, nil
}

// expireLocked drops what has run out; true when the pending list changed.
func (m *Manager) expireLocked(now time.Time) bool {
	changed := false
	for id, r := range m.reqs {
		if r.state == StatePending && !now.Before(r.expires) {
			r.state = StateExpired
			m.log().Info("pairing: a request expired", "app", r.ask.App, "instance", r.ask.Instance, "address", r.address)
			m.signalLocked(r)
			changed = true
		}
		// a finished request stays a minute for the program's last poll
		if r.state != StatePending && now.Sub(r.expires) > time.Minute {
			delete(m.reqs, id)
		}
	}
	for k, until := range m.mutes {
		if !now.Before(until) {
			delete(m.mutes, k)
		}
	}
	return changed
}

func (m *Manager) signalLocked(r *request) {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (m *Manager) changedList() {
	m.mu.Lock()
	for w := range m.watchers {
		select {
		case w <- struct{}{}:
		default:
		}
	}
	m.mu.Unlock()
	if m.OnChange != nil {
		m.OnChange()
	}
}

// Watch is the card's stream: a channel told of every change, and its end.
func (m *Manager) Watch() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.mu.Lock()
	if m.watchers == nil {
		m.watchers = map[chan struct{}]struct{}{}
	}
	m.watchers[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.watchers, ch)
		m.mu.Unlock()
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// Request takes a program's request from addr; fingerprint is the certificate the system served
// on this connection (nil over plain HTTP).
func (m *Manager) Request(a Ask, addr string, fingerprint []byte) (Answer, error) {
	if m.Enabled != nil && !m.Enabled() {
		return Answer{}, ErrOff
	}
	if m.Local != nil && !m.Local(addr) {
		m.log().Warn("pairing: a request from outside the local networks refused", "address", addr, "app", a.App)
		return Answer{}, ErrNotLocal
	}
	a.App, a.Instance, a.AppVersion, a.Name = clip(a.App, 48), clip(a.Instance, 64), clip(a.AppVersion, 32), clip(a.Name, 80)
	if !appRe.MatchString(a.App) {
		return Answer{}, fmt.Errorf("%w: app is a short id (letters, digits, . _ -)", ErrInvalid)
	}
	for _, s := range []string{a.Instance, a.AppVersion, a.Name} {
		if !textRe.MatchString(s) {
			return Answer{}, fmt.Errorf("%w: no control characters", ErrInvalid)
		}
	}
	if !hex64.MatchString(strings.ToLower(a.Commit)) {
		return Answer{}, fmt.Errorf("%w: commit is the hex SHA-256 of the program's client_nonce", ErrInvalid)
	}
	scopes, err := scopesForAccess(a.Access)
	if err != nil {
		return Answer{}, err
	}
	// openccu-lite B-296: an ask of addons alone has no access map; it is listed and carried as an
	// empty one, never as null (the card read null as an object and was not drawn)
	if a.Access == nil {
		a.Access = map[string]string{}
	}
	// the addons' ingress (task 307): installed ones, each once, at most MaxAddons
	var addons []AddonRef
	seenAddon := map[string]bool{}
	for _, id := range a.Addons {
		id = strings.TrimSpace(id)
		if seenAddon[id] {
			continue
		}
		if _, ok := auth.AddonOf(auth.AddonScope(id)); !ok {
			return Answer{}, fmt.Errorf("%w: %q is no addon id", ErrInvalid, clip(id, 40))
		}
		if m.AddonName == nil {
			return Answer{}, fmt.Errorf("%w: this system offers no addon ingress to programs", ErrInvalid)
		}
		name, ok := m.AddonName(id)
		if !ok {
			return Answer{}, fmt.Errorf("%w: no addon %q is installed", ErrInvalid, id)
		}
		if len(addons) >= MaxAddons {
			return Answer{}, fmt.Errorf("%w: at most %d addons", ErrInvalid, MaxAddons)
		}
		seenAddon[id] = true
		addons = append(addons, AddonRef{ID: id, Name: name})
		scopes = append(scopes, auth.AddonScope(id))
	}
	a.Addons = nil
	for _, ad := range addons {
		a.Addons = append(a.Addons, ad.ID)
	}
	scopes = scopes.Normalize()
	if len(scopes) == 0 {
		return Answer{}, fmt.Errorf("%w: ask for access to at least one area or addon", ErrInvalid)
	}
	if needsConfirmation(scopes) && TokenRanges(addr) == nil {
		return Answer{}, fmt.Errorf("%w: a token for backups is limited to the address it was asked from", ErrInvalid)
	}
	purpose := map[string]string{}
	for area, p := range a.Purpose {
		if _, ok := levels[area]; !ok {
			continue
		}
		if p = clip(p, maxPurposeLn); p != "" && textRe.MatchString(p) {
			purpose[area] = p
		}
	}
	a.Purpose = purpose
	if a.Name == "" {
		a.Name = a.App
		if a.Instance != "" {
			a.Name += " on " + a.Instance
		}
	}
	now := m.now()
	m.mu.Lock()
	if m.reqs == nil {
		m.reqs, m.mutes, m.perHour = map[string]*request{}, map[string]time.Time{}, map[string][]time.Time{}
	}
	listChanged := m.expireLocked(now)
	key := addr + "|" + a.App
	if until, ok := m.mutes[key]; ok && now.Before(until) {
		m.mu.Unlock()
		return Answer{}, ErrMuted
	}
	var recent []time.Time
	for _, t := range m.perHour[addr] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	m.perHour[addr] = recent
	pending := 0
	for _, r := range m.reqs {
		if r.state != StatePending {
			continue
		}
		pending++
		if r.address == addr && r.ask.App == a.App {
			m.mu.Unlock()
			return Answer{}, fmt.Errorf("%w: one request per program and address at a time", ErrLimit)
		}
	}
	if pending >= MaxPending {
		m.mu.Unlock()
		return Answer{}, fmt.Errorf("%w: %d are waiting", ErrLimit, MaxPending)
	}
	if len(recent) >= PerHour {
		m.mu.Unlock()
		return Answer{}, fmt.Errorf("%w: %d an hour from one address", ErrLimit, PerHour)
	}
	id, _, err := randHex(8)
	if err != nil {
		m.mu.Unlock()
		return Answer{}, err
	}
	poll, _, err := randHex(24)
	if err != nil {
		m.mu.Unlock()
		return Answer{}, err
	}
	nonceHex, nonce, err := randHex(16)
	if err != nil {
		m.mu.Unlock()
		return Answer{}, err
	}
	commit, _ := hex.DecodeString(strings.ToLower(a.Commit))
	r := &request{id: id, pollHash: sha256.Sum256([]byte(poll)), nonce: nonce, commit: commit, fp: fingerprint, ask: a, addons: addons, scopes: scopes,
		address: addr, created: now, expires: now.Add(Lifetime), state: StatePending, changed: make(chan struct{})}
	m.reqs[id] = r
	m.perHour[addr] = append(recent, now)
	m.mu.Unlock()
	m.log().Info("pairing: a program asks for access", "app", a.App, "version", a.AppVersion, "instance", a.Instance, "address", addr, "access", a.Access, "addons", a.Addons, "scopes", scopes.Strings())
	if listChanged {
		m.changedList()
	}
	return Answer{ID: id, Poll: poll, Nonce: nonceHex, ExpiresIn: int(Lifetime / time.Second), Interval: int(Interval / time.Second), Fingerprint: hex.EncodeToString(fingerprint)}, nil
}

func (m *Manager) find(id, poll string) (*request, error) {
	r := m.reqs[id]
	if r == nil {
		return nil, ErrUnknown
	}
	h := sha256.Sum256([]byte(poll))
	if subtle.ConstantTimeCompare(h[:], r.pollHash[:]) != 1 {
		return nil, ErrPoll
	}
	return r, nil
}

// Poll is the program's wait: it reveals client_nonce (hex) with its first poll; wait > 0 holds
// the answer until the state changes or wait passes. The approved answer carries the token once:
// the request is gone after it.
func (m *Manager) Poll(ctx context.Context, id, poll, clientNonce string, wait time.Duration) (Result, error) {
	now := m.now()
	m.mu.Lock()
	listChanged := m.expireLocked(now)
	r, err := m.find(id, poll)
	if err != nil {
		m.mu.Unlock()
		return Result{}, err
	}
	revealed := false
	if clientNonce != "" && r.clientNonce == nil {
		cn, err := hex.DecodeString(strings.ToLower(clientNonce))
		sum := sha256.Sum256(cn)
		if err != nil || len(cn) < 16 || subtle.ConstantTimeCompare(sum[:], r.commit) != 1 {
			m.mu.Unlock()
			return Result{}, fmt.Errorf("%w: client_nonce does not match the commitment", ErrInvalid)
		}
		r.clientNonce = cn
		revealed = true
	}
	if wait <= 0 && !revealed && r.state == StatePending && !r.lastPoll.IsZero() && now.Sub(r.lastPoll) < Interval-200*time.Millisecond {
		m.mu.Unlock()
		return Result{}, ErrSlowDown
	}
	r.lastPoll = now
	if wait > MaxWait {
		wait = MaxWait
	}
	ch := r.changed
	state := r.state
	m.mu.Unlock()
	if revealed || listChanged {
		m.changedList()
	}
	if state == StatePending && wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(m.now())
	if r.state == StatePending {
		return Result{State: StatePending}, nil
	}
	res := r.result
	res.State = r.state
	delete(m.reqs, id) // the answer once: a second poll is ErrUnknown
	return res, nil
}

// Withdraw is the program giving up.
func (m *Manager) Withdraw(id, poll string) error {
	m.mu.Lock()
	r, err := m.find(id, poll)
	if err == nil {
		delete(m.reqs, id)
		m.signalLocked(r)
	}
	m.mu.Unlock()
	if err == nil {
		m.log().Info("pairing: a program withdrew its request", "app", r.ask.App, "address", r.address)
		m.changedList()
	}
	return err
}

// fingerprintText is the card's colon-separated certificate fingerprint.
func fingerprintText(fp []byte) string {
	if len(fp) == 0 {
		return ""
	}
	parts := make([]string, len(fp))
	for i, b := range fp {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// Pending is the card's list: the requests whose program revealed its half, oldest first.
func (m *Manager) Pending() []View {
	m.mu.Lock()
	changed := m.expireLocked(m.now())
	out := []View{}
	for _, r := range m.reqs {
		if r.state != StatePending || r.clientNonce == nil {
			continue
		}
		out = append(out, View{ID: r.id, App: r.ask.App, AppVersion: r.ask.AppVersion, Instance: r.ask.Instance, Name: r.ask.Name, Address: r.address,
			Access: accessMap(r.ask.Access), Purpose: r.ask.Purpose, Addons: r.addons, Scopes: r.scopes.Strings(), Code: Code(r.nonce, r.clientNonce, r.fp),
			Fingerprint: fingerprintText(r.fp), Created: r.created, Expires: r.expires})
		if needsConfirmation(r.scopes) {
			out[len(out)-1].NeedsConfirmation, out[len(out)-1].TokenIPs = true, TokenRanges(r.address)
		}
	}
	m.mu.Unlock()
	if changed {
		m.changedList()
	}
	for i := range out {
		for j := range out {
			if i != j && (out[i].Address == out[j].Address || out[i].App == out[j].App && out[i].Instance == out[j].Instance) {
				out[i].LookAlike = true
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created.Before(out[b].Created) })
	return out
}

// slug makes a token name from the program and its instance: lower case, [a-z0-9.-_], 2-28
// characters (room for "-NN").
func slug(app, instance string) string {
	s := strings.ToLower(app)
	if instance != "" {
		s += "-" + strings.ToLower(instance)
	}
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteRune(c)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_.")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) > 28 {
		out = strings.TrimRight(out[:28], "-_.")
	}
	if len(out) < 2 {
		out = "client"
	}
	return out
}

// NeedsConfirmation says whether approving the request asks for the administrator's confirmation:
// ErrUnknown and ErrNotReady as Approve answers them, so that no confirmation is spent on a request
// that could not be approved anyway.
func (m *Manager) NeedsConfirmation(id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(m.now())
	r := m.reqs[id]
	if r == nil || r.state != StatePending {
		return false, ErrUnknown
	}
	if r.clientNonce == nil {
		return false, ErrNotReady
	}
	return needsConfirmation(r.scopes), nil
}

// Approve mints the token for a request whose code the administrator compared; a code that is
// not the request's rejects it. confirmed says the administrator's confirmation was redeemed: a
// request for backups without it is ErrConfirmRequired and stays as it is, whatever the code.
func (m *Manager) Approve(id, code, by string, confirmed bool) (auth.TokenClient, string, error) {
	m.mu.Lock()
	m.expireLocked(m.now())
	r := m.reqs[id]
	if r == nil || r.state != StatePending {
		m.mu.Unlock()
		return auth.TokenClient{}, "", ErrUnknown
	}
	if r.clientNonce == nil {
		m.mu.Unlock()
		return auth.TokenClient{}, "", ErrNotReady
	}
	if needsConfirmation(r.scopes) && !confirmed {
		app, address := r.ask.App, r.address
		m.mu.Unlock()
		m.log().Warn("pairing: an approval of a request for backups refused - no valid confirmation", "app", app, "address", address, "by", by)
		return auth.TokenClient{}, "", ErrConfirmRequired
	}
	if want := Code(r.nonce, r.clientNonce, r.fp); subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimSpace(code))) != 1 {
		r.state = StateRejected
		m.mutes[r.address+"|"+r.ask.App] = m.now().Add(MuteFor)
		m.signalLocked(r)
		m.mu.Unlock()
		m.log().Warn("pairing: a wrong code - the request is rejected", "app", r.ask.App, "address", r.address, "by", by)
		m.changedList()
		return auth.TokenClient{}, "", ErrWrongCode
	}
	now := m.now()
	client := auth.TokenClient{App: r.ask.App, AppVersion: r.ask.AppVersion, Instance: r.ask.Instance, Label: r.ask.Name, PairedAt: now, PairedBy: by,
		Address: r.address, Fingerprint: hex.EncodeToString(r.fp), Access: r.ask.Access, Addons: r.ask.Addons}
	ask, scopes, address := r.ask, r.scopes, r.address
	m.mu.Unlock()
	// only a token for backups is limited to the address: an ordinary paired token stays as it was
	var ips []string
	if needsConfirmation(scopes) {
		ips = TokenRanges(address)
	}
	name, secret, err := m.Minter.CreatePairedToken(slug(ask.App, ask.Instance), scopes, ips, client)
	if err != nil {
		return auth.TokenClient{}, "", err
	}
	m.mu.Lock()
	if r2 := m.reqs[id]; r2 == r && r.state == StatePending {
		r.state = StateApproved
		r.result = Result{Token: secret, Name: name, Scopes: scopes.Strings(), Access: ask.Access, Addons: ask.Addons, IPs: ips}
		m.signalLocked(r)
	}
	m.mu.Unlock()
	m.log().Info("pairing: approved - a token minted", "app", ask.App, "instance", ask.Instance, "address", address, "token", name, "scopes", scopes.Strings(), "ips", ips, "confirmed", confirmed, "by", by)
	m.changedList()
	return client, name, nil
}

// Reject turns a request down and mutes its address and program for MuteFor.
func (m *Manager) Reject(id, by string) error {
	m.mu.Lock()
	r := m.reqs[id]
	if r == nil || r.state != StatePending {
		m.mu.Unlock()
		return ErrUnknown
	}
	r.state = StateRejected
	m.mutes[r.address+"|"+r.ask.App] = m.now().Add(MuteFor)
	m.signalLocked(r)
	m.mu.Unlock()
	m.log().Info("pairing: rejected", "app", r.ask.App, "instance", r.ask.Instance, "address", r.address, "by", by)
	m.changedList()
	return nil
}
