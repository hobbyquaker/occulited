package auth

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Scopes (task 66, D-85): what an API token may do, by area × read/write. A token carries a list
// of them; an account keeps its role, which maps to a fixed list (RoleScopes). Every API route
// names the scope it needs (internal/httpapi's route table), a write scope includes its read,
// and Full access - the wildcard "*" - includes every scope that exists now and every one added
// later, the rpc tiers of task 78 included. The names are frozen with the API at 1.0 (OQ-9).

// Scope is one permission.
type Scope string

const (
	// ScopeAll is Full access: every scope, now and later. Offered when a token is created,
	// with the hint that explicit scopes are safer for a program.
	ScopeAll Scope = "*"
	// ScopeMetaRead is the metadata API's reads: the snapshot, objects, enums, the export, the
	// event stream. The local token's only scope.
	ScopeMetaRead Scope = "meta:read"
	// ScopeMetaWrite is the metadata mutations and the imports; includes meta:read.
	ScopeMetaWrite Scope = "meta:write"
	// ScopeSystemRead is every system read that is not the journal: status, health, radio,
	// services, timers, network, firewall, time, storage, warnings, the installed addons, the
	// catalogue, the navigation, the certificate and HTTPS settings (no secrets), the firmware
	// information, the LED configuration.
	ScopeSystemRead Scope = "system:read"
	// ScopeLogsRead is the journal: the log, its download and stream, the boots, the boot
	// timeline, the kernel log, the run logs. Split from system:read because the journal is
	// sensitive.
	ScopeLogsRead Scope = "logs:read"
	// ScopeSystemWrite is the system configuration: network, firewall, time, SSH, certificate,
	// HTTPS and HSTS, timers, starting and stopping services, log levels, the journal settings,
	// the LED configuration, the warning silences, the device firmware, the disk cleanup.
	// Includes system:read and led.
	ScopeSystemWrite Scope = "system:write"
	// ScopeAddonsWrite is addon management: install, update, uninstall, the policies, the
	// catalogue refresh, addon start and stop, the per-addon legacy session switch, the
	// ownership repair. Includes system:read (the addon and catalogue lists).
	ScopeAddonsWrite Scope = "addons:write"
	// ScopePower is what takes the box down or replaces it: reboot, halt, the system update,
	// the restore, the radio module's firmware.
	ScopePower Scope = "power"
	// ScopePowerHalt is powering the box off and nothing else (occulited task 132): what a UPS addon needs. The one
	// part of power an addon may hold; a reboot stays with power. power includes it.
	ScopePowerHalt Scope = "power:halt"
	// ScopeBackup is creating and downloading backups (D-62).
	ScopeBackup Scope = "backup"
	// ScopeLED is the status LED's state, an override of the token's own and locate (task 95's
	// role led): Home Assistant's token.
	ScopeLED Scope = "led"
	// ScopeRadioKeys is the HmIP device keys (task 154, D-104): the stored keys' list, adding and
	// removing one, applying them, and the key sheet - the one answer with every key in clear. No
	// other scope includes it, and an addon's token never gets it.
	ScopeRadioKeys Scope = "radio:keys"
	// ScopeAuthAdmin is accounts, tokens, every session, the security and login settings (the
	// identity provider, the password-login switch, the global legacy session switch).
	// Includes self.
	ScopeAuthAdmin Scope = "auth:admin"
	// The RPC tiers of task 78: each includes the ones before it. They have no routes until
	// task 77 brings the remote RPC surface.
	ScopeRPCRead      Scope = "rpc:read"
	ScopeRPCOperate   Scope = "rpc:operate"
	ScopeRPCConfigure Scope = "rpc:configure"
	ScopeRPCAdmin     Scope = "rpc:admin"
	// ScopeSelf is the caller's own account and session - the password, the sessions, the
	// preferences, logout, the legacy alias, the tickets. Every account session has it; a token
	// only through auth:admin (an administrator resets other passwords there). It cannot be
	// given to a token and is not in Grantable.
	ScopeSelf Scope = "self"
)

// The addon ingress scope (openccu-lite task 307, GitHub issue #3): "addon:<id>" opens lighttpd's
// session gate in front of /addons/<id>/ for a token sent as Authorization: Bearer, and nothing
// else - no API route needs it, and no other scope implies it (addons:write manages addons, it does
// not open their pages). Full access covers it like every scope. The id is an installed addon's;
// the scope is well-formed when the id has the manifest's shape, and the API checks the addon is
// installed when a token is made or a pairing asks for it. An addon's own token never gets one
// (AddonScopes). The gate finds a token's addons in the store's token mirror (auth.go).

// AddonScopePrefix is what an addon ingress scope starts with.
const AddonScopePrefix = "addon:"

// addonScopeID is the addon id rule (internal/manifest's idRe): a scope with another id is unknown.
var addonScopeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

// AddonScope is the ingress scope of addon id.
func AddonScope(id string) Scope { return Scope(AddonScopePrefix + id) }

// AddonOf answers the addon id of an ingress scope, and whether s is a well-formed one.
func AddonOf(s Scope) (string, bool) {
	id, ok := strings.CutPrefix(string(s), AddonScopePrefix)
	if !ok || !addonScopeID.MatchString(id) {
		return "", false
	}
	return id, true
}

// Grantable are the scopes a token can be given, in the order the pages list them. ScopeAll is
// offered beside them; ScopeSelf is nobody's to give. The addon ingress scopes are grantable too,
// one per installed addon: the API lists them beside this list (GET /tokens answers addons).
var Grantable = []Scope{
	ScopeMetaRead, ScopeMetaWrite,
	ScopeSystemRead, ScopeLogsRead, ScopeSystemWrite, ScopeAddonsWrite,
	ScopePower, ScopePowerHalt, ScopeBackup, ScopeLED, ScopeRadioKeys, ScopeAuthAdmin,
	ScopeRPCRead, ScopeRPCOperate, ScopeRPCConfigure, ScopeRPCAdmin,
}

// includes says what a scope brings along: a write its read, a tier the ones before it.
var includes = map[Scope][]Scope{
	ScopeMetaWrite:    {ScopeMetaRead},
	ScopeSystemWrite:  {ScopeSystemRead, ScopeLED},
	ScopeAddonsWrite:  {ScopeSystemRead},
	ScopeAuthAdmin:    {ScopeSelf},
	ScopePower:        {ScopePowerHalt},
	ScopeRPCOperate:   {ScopeRPCRead},
	ScopeRPCConfigure: {ScopeRPCOperate, ScopeRPCRead},
	ScopeRPCAdmin:     {ScopeRPCConfigure, ScopeRPCOperate, ScopeRPCRead},
}

// AddonNever are the scopes an addon's token is never given, whatever its catalogue entry
// declares (D-85): a declaration naming one is logged and the scope left out.
var AddonNever = []Scope{ScopeAll, ScopeAuthAdmin, ScopePower, ScopeBackup, ScopeRadioKeys}

// PairingNever are the scopes client pairing never grants. Backup is not among them (occulited task
// 135): CreatePairedToken holds a token for backups to an address range, and internal/pairing
// approves such a request only with the administrator's confirmation.
var PairingNever = []Scope{ScopeAll, ScopeAuthAdmin, ScopePower, ScopeRadioKeys}

// ErrBadScope is a scope name that does not exist, or one a token cannot carry.
var ErrBadScope = errors.New("unknown scope")

// Known reports whether s names a scope a token can carry (Grantable, ScopeAll, or a well-formed
// addon ingress scope).
func Known(s Scope) bool {
	if s == ScopeAll {
		return true
	}
	if _, ok := AddonOf(s); ok {
		return true
	}
	for _, g := range Grantable {
		if g == s {
			return true
		}
	}
	return false
}

// Covers reports whether holding scope have satisfies want: the same scope, Full access, or one
// the held scope includes. ScopeSelf is covered by auth:admin and by Full access.
func Covers(have, want Scope) bool {
	if have == want || have == ScopeAll {
		return true
	}
	for _, inc := range includes[have] {
		if inc == want {
			return true
		}
	}
	return false
}

// Scopes is a token's or a session's list. Order and repetition mean nothing; Normalize sorts
// and deduplicates for the store and the pages.
type Scopes []Scope

// Has reports whether the list covers want.
func (s Scopes) Has(want Scope) bool {
	for _, have := range s {
		if Covers(have, want) {
			return true
		}
	}
	return false
}

// Full reports whether the list is Full access.
func (s Scopes) Full() bool {
	for _, have := range s {
		if have == ScopeAll {
			return true
		}
	}
	return false
}

// Normalize returns the list sorted, without repetitions, with the ones already covered by
// another entry left out (Full access alone when it is in), and ScopeSelf never in it.
func (s Scopes) Normalize() Scopes {
	if s.Full() {
		return Scopes{ScopeAll}
	}
	var out Scopes
	for _, a := range s {
		if a == ScopeSelf {
			continue
		}
		covered := false
		for _, b := range s {
			if b != a && Covers(b, a) {
				covered = true
				break
			}
		}
		if !covered && !contains(out, a) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func contains(list Scopes, s Scope) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Strings is the list as strings (the mirrors, the run files).
func (s Scopes) Strings() []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, string(x))
	}
	return out
}

// RPCTier is what task 77's token mirror carries in its rpc field: "admin" for Full access,
// else the highest rpc tier the list covers ("read", "operate", "configure", "admin"), or ""
// when the token has no tier.
func (s Scopes) RPCTier() string {
	switch {
	case s.Has(ScopeRPCAdmin):
		return "admin"
	case s.Has(ScopeRPCConfigure):
		return "configure"
	case s.Has(ScopeRPCOperate):
		return "operate"
	case s.Has(ScopeRPCRead):
		return "read"
	}
	return ""
}

// ParseScopes turns the names a request or the console gives into a list a token may carry:
// each must be a known scope (ScopeSelf is not), the list must not be empty. Names are trimmed;
// the result is normalized.
func ParseScopes(names []string) (Scopes, error) {
	var out Scopes
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		s := Scope(n)
		if !Known(s) {
			return nil, fmt.Errorf("%w: %q", ErrBadScope, n)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: a token needs at least one scope", ErrBadScope)
	}
	return out.Normalize(), nil
}

// RoleScopes is what a role stands for (D-85): an administrator has Full access, a user the
// three read scopes (ScopeSelf comes with every account session, not with the list), and
// task 95's role led - a token's only - the scope led. The same mapping migrates the tokens of
// a users.json from before task 66 and serves the console's --role alias. An unknown role has
// nothing.
func RoleScopes(role Role) Scopes {
	switch role {
	case RoleAdmin:
		return Scopes{ScopeAll}
	case RoleUser:
		return Scopes{ScopeLogsRead, ScopeMetaRead, ScopeSystemRead} // sorted, as Normalize sorts
	case RoleLED:
		return Scopes{ScopeLED}
	}
	return nil
}

// LevelScopes is what a level of the ladder stands for (task 78, D-116): read is every read
// scope and rpc:read; operate adds rpc:operate (setValue, the stream); configure adds the
// metadata writes and rpc:configure; administer is Full access. Every account session has self.
func LevelScopes(level Level) Scopes {
	switch level {
	case LevelRead:
		return Scopes{ScopeLogsRead, ScopeMetaRead, ScopeRPCRead, ScopeSystemRead}
	case LevelOperate:
		return Scopes{ScopeLogsRead, ScopeMetaRead, ScopeRPCOperate, ScopeSystemRead}
	case LevelConfigure:
		return Scopes{ScopeLogsRead, ScopeMetaWrite, ScopeRPCConfigure, ScopeSystemRead}
	case LevelAdminister:
		return Scopes{ScopeAll}
	}
	return nil
}

// AddonScopes filters a catalogue entry's runtime.api_scopes into what an addon's token gets:
// every known scope except the ones in AddonNever and the addon ingress scopes (an addon does
// not reach another addon's pages through its own token). refused names what was left out - the
// caller logs it - and granted is normalized, empty when nothing stands.
func AddonScopes(declared []string) (granted Scopes, refused []string) {
	for _, n := range declared {
		n = strings.TrimSpace(n)
		s := Scope(n)
		if _, ingress := AddonOf(s); n == "" || !Known(s) || contains(AddonNever, s) || ingress {
			refused = append(refused, n)
			continue
		}
		granted = append(granted, s)
	}
	return granted.Normalize(), refused
}
