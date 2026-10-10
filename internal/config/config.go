// Package config is occulited's own configuration: one JSON file, atomic write, defaults that make
// an unconfigured box work.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Config is the content of occulited.json.
type Config struct {
	// Listen is the loopback address lighttpd proxies to. Never a LAN address (D-29).
	Listen string `json:"listen"`
	// StateDir holds meta.json, users.json and everything else that must survive a reboot.
	StateDir string `json:"state_dir"`
	// LogLevel is debug, info, warn or error. The Log settings change it while occulited runs
	// (task 101, internal/logctl).
	LogLevel string `json:"log_level"`
	// LogDebugAreas are the parts of occulited that log at debug while LogLevel is above it:
	// acme, radio-firmware, addons, metadata, http, auth, led (logctl.Areas).
	LogDebugAreas []string `json:"log_debug_areas,omitempty"`
	// Firmware is the device-firmware fetcher (D-27, opt-in since D-90 / B-241): off by default;
	// on, an outbound call to eQ-3's update service once a day for the paired device types only.
	Firmware FirmwareConfig `json:"firmware"`
	// Catalog is the addon catalogue (task 10): index URLs, fetched on demand only.
	Catalog CatalogConfig `json:"catalog"`
	// Auth holds the optional external login (task 6: "authentik support via oauth").
	Auth AuthConfig `json:"auth"`
	// MQTT mirrors the metadata store to a broker as retained messages (meta-api.md); off by default.
	MQTT MQTTConfig `json:"mqtt"`
	// RPC is the box's own event subscriber (task 75) and, with lite-rpc, the remote paths.
	RPC RPCConfig `json:"rpc"`
	// SystemUpdate is the daily release feed check for the system firmware (task 16).
	SystemUpdate SystemUpdateConfig `json:"system_update"`
	// Addons: how third-party addons run on the systemd products (D-36).
	Addons AddonsConfig `json:"addons"`
	// HmIPServer is what occulited sets up around hmipserver (task 33).
	HmIPServer HmIPServerConfig `json:"hmipserver"`
	// Store is occulited's database file (openccu-lite task 214, internal/store): where the health
	// history - and later the state store and the datapoint history - is kept.
	Store StoreConfig `json:"store"`
	// Stale names the keys of earlier versions the file still carries and this version ignores
	// (Load fills it, Save leaves them out): a line in the log at start, so nobody relies on a key
	// that does nothing.
	Stale []string `json:"-"`
	// OutboundUnset names the outbound switches the file does not carry (Load fills it):
	// firmware.enabled, system_update.enabled, catalog.daily. SettleOutbound writes them once.
	OutboundUnset []string `json:"-"`
}

// StoreConfig is the storage mode of occulited's database file (task 214, with task 194's
// modes): "" = the product's default (ram-sync on the SD-card products, persistent on ova, oci
// and lxc), ram, ram-sync or persistent; SyncInterval is ram-sync's write interval (15min to 1d,
// "" = 1h). Location is where the file lives, as a location id and a folder (task 228's shape:
// userfs:<folder>; "" = userfs:etc/occulite/data, the state directory's data/); never a share.
type StoreConfig struct {
	Mode         string `json:"mode,omitempty"`
	SyncInterval string `json:"sync_interval,omitempty"`
	Location     string `json:"location,omitempty"`
	// HistoryAdd and HistoryRemove change the datapoint history's built-in list (task 195):
	// datapoint names recorded in addition, and names of the list not recorded
	HistoryAdd    []string `json:"history_add,omitempty"`
	HistoryRemove []string `json:"history_remove,omitempty"`
}

// AddonsConfig is the confinement default (D-36). "confined", the default since the maintainer
// decided it on 2026-09-07: an addon without a policy of its own runs as its own user addon-<id>
// with the catalogue entry's grants, and root is the explicit, warned opt-out the user chooses on
// the Services page. "root" restores the old behaviour for a whole box - every addon runs as root,
// as the rc.d ABI always did. An addon whose catalogue entry declares no runtime block is confined
// like any other and shown as *undeclared* in the UI, so the reason it may misbehave is on the page
// rather than in the log. Busybox products ignore all of it.
type AddonsConfig struct {
	DefaultMode string `json:"default_mode"`
	// LegacySession (task 125, D-77): whether the shell passes the session's ?sid=@..@ alias in
	// the URLs of the addons that live by the CCU convention - those without
	// runtime.session.header_since for their installed version, and every addon outside the
	// catalogue. nil = on, the default; LegacySessionOff names the addons it is switched off for.
	LegacySession    *bool    `json:"legacy_session,omitempty"`
	LegacySessionOff []string `json:"legacy_session_off,omitempty"`
	// EarlyStart (task 119, D-75): whether an addon whose catalogue entry declares
	// runtime.start "early" starts before the radio interfaces are ready. nil = on, the default;
	// EarlyStartOff names the addons it is switched off for. Read at the next boot.
	EarlyStart    *bool    `json:"early_start,omitempty"`
	EarlyStartOff []string `json:"early_start_off,omitempty"`
}

// LegacySessionOn is the global switch's value: on unless switched off.
func (a AddonsConfig) LegacySessionOn() bool {
	return a.LegacySession == nil || *a.LegacySession
}

// EarlyStartOn is the early start's global switch: on unless switched off.
func (a AddonsConfig) EarlyStartOn() bool {
	return a.EarlyStart == nil || *a.EarlyStart
}

// DefaultPath is where the image keeps occulited.json (the unit's --config).
const DefaultPath = "/usr/local/etc/occulite/occulited.json"

// HmIPServerConfig: Diagrams carries hmipserver's diagram data between its tmpfs and the stick at
// start and stop, as the CCU did (task 33). Off by default: openccu-lite has no WebUI to configure
// or show a diagram, and with it off a migrated system's diagram data is removed once. Read by
// `occulited radio prep|stopped|run` (as root) at hmipserver's next start or stop.
type HmIPServerConfig struct {
	Diagrams bool `json:"diagrams"`
}

// SystemUpdateConfig configures the release feed check: when enabled, one outbound call a day to
// a GitHub release list (off by default, D-90); the download itself only ever happens on request.
type SystemUpdateConfig struct {
	Enabled bool   `json:"enabled"`
	Feed    string `json:"feed"`
}

// DefaultSystemUpdateFeed is where openccu-lite's releases are published: the release list, not
// releases/latest, which never returns a prerelease (task 258) - a system on 1.0.0-dev.N or a beta
// follows the prereleases published after it.
const DefaultSystemUpdateFeed = "https://api.github.com/repos/hobbyquaker/openccu-lite/releases?per_page=20"

// oldDefaultSystemUpdateFeed is the default before task 258; a stored configuration that still
// names it follows the new one.
const oldDefaultSystemUpdateFeed = "https://api.github.com/repos/hobbyquaker/openccu-lite/releases/latest"

// MQTTConfig is the optional publication of the metadata store.
// RPCConfig: CallbackListen is the second loopback socket the interface daemons call the
// subscriber back on (D-115) - never lighttpd's proxy target, so a LAN client cannot reach it.
type RPCConfig struct {
	CallbackListen string `json:"callback_listen,omitempty"`
	// StreamsPerSession is lite-rpc's limit of open event streams per token or session (occulited
	// task 19); absent = DefaultStreamsPerSession. See StreamsPerSessionLimit.
	StreamsPerSession int `json:"streams_per_session,omitempty"`
	// Trace is the RPC trace's switch (task 79): off, until (with the deadline) or permanent.
	Trace TraceConfig `json:"trace"`
}

// DefaultStreamsPerSession and MaxStreamsPerSession bound rpc.streams_per_session: 3 by default
// (occulited task 19; 2 before), at most lite-rpc's total of 16.
const (
	DefaultStreamsPerSession = 3
	MaxStreamsPerSession     = 16
)

// StreamsPerSessionLimit is the per-session stream limit in force, and false when the stored value
// was out of bounds (1-16) and the default stands in for it - the caller logs that.
func (r RPCConfig) StreamsPerSessionLimit() (int, bool) {
	switch {
	case r.StreamsPerSession == 0:
		return DefaultStreamsPerSession, true
	case r.StreamsPerSession < 1 || r.StreamsPerSession > MaxStreamsPerSession:
		return DefaultStreamsPerSession, false
	}
	return r.StreamsPerSession, true
}

// TraceConfig is the RPC trace's switch as stored: a timed trace keeps its deadline (RFC 3339),
// so a restart of occulited keeps the trace and its end.
type TraceConfig struct {
	Mode  string `json:"mode,omitempty"`
	Until string `json:"until,omitempty"`
}

type MQTTConfig struct {
	Enabled  bool   `json:"enabled"`
	Broker   string `json:"broker"`   // host:port, e.g. 127.0.0.1:1883 (the mosquitto addon)
	Username string `json:"username"` //
	Password string `json:"password"` //
	Prefix   string `json:"prefix"`   // topic prefix, default occulite
	ClientID string `json:"client_id"`
}

// AuthConfig configures how the box authenticates (task 29).
type AuthConfig struct {
	// Mode is "local" (the default when empty: the users in users.json), "oidc" (local users plus
	// the external login below), or "off": no login at all, every caller is an anonymous
	// administrator - occulited's middleware, the addon CGIs and lighttpd's gate all honour it
	// through one anonymous session occulited keeps alive. A change takes effect at the next
	// start of occulited; the Settings page writes it and offers the restart.
	Mode string     `json:"mode"`
	OIDC OIDCConfig `json:"oidc"`
	// Public is the Control app without a login (task 193): a request to the App's paths without
	// a session is the named account, restricted to operating; everything else stays behind the
	// login. Switched on System -> Remote access, in force at once.
	Public PublicConfig `json:"public"`
	// Pairing is the switch of client pairing (task 219): a program may ask for access and an
	// administrator approves it on the Status page. nil = on (the default).
	Pairing *bool `json:"pairing,omitempty"`
	// SessionIdle and SessionMax are the login sessions' idle timeout and absolute lifetime
	// (openccu-lite task 262), Go durations ("30m", "12h"); empty means the defaults, ASVS Level
	// 2's 30 minutes and 12 hours. Set on System -> Users (Authentication); in force at once.
	SessionIdle string `json:"session_idle,omitempty"`
	SessionMax  string `json:"session_max,omitempty"`
}

// SessionLimits parses auth.session_idle and auth.session_max: zero for an empty value (the
// store's default), an error for a value that is not a duration - the caller keeps the default
// then and says so.
func (a AuthConfig) SessionLimits() (idle, maxAge time.Duration, err error) {
	if a.SessionIdle != "" {
		if idle, err = time.ParseDuration(a.SessionIdle); err != nil {
			return 0, 0, fmt.Errorf("auth.session_idle %q: %w", a.SessionIdle, err)
		}
	}
	if a.SessionMax != "" {
		if maxAge, err = time.ParseDuration(a.SessionMax); err != nil {
			return 0, 0, fmt.Errorf("auth.session_max %q: %w", a.SessionMax, err)
		}
	}
	return idle, maxAge, nil
}

// PublicConfig is auth.public: the Control app's public mode.
type PublicConfig struct {
	Enabled bool `json:"enabled"`
	// Account is the principal a request without a session becomes: an account of the ladder
	// (read or operate) when one of that name exists, else a virtual one at operate. "guest" by
	// default.
	Account string `json:"account,omitempty"`
}

// PublicAccount is the account name public mode uses: Account, or guest.
func (p PublicConfig) PublicAccount() string {
	if p.Account == "" {
		return "guest"
	}
	return p.Account
}

// PasswordLoginOn reports whether accounts may sign in with a password (task 19, D-53): the
// switch auth.oidc.password_login, which only holds while the mode is oidc - without the provider
// the password is the only login, so switching the provider off switches the password back on.
func (a AuthConfig) PasswordLoginOn() bool {
	return a.EffectiveMode() != "oidc" || a.OIDC.PasswordLogin
}

// AuthModes are the values Mode takes.
var AuthModes = []string{"local", "oidc", "off"}

// EffectiveMode normalises Mode: empty is local, and an enabled OIDC block without a mode is oidc.
func (a AuthConfig) EffectiveMode() string {
	switch a.Mode {
	case "oidc", "off":
		return a.Mode
	case "local":
		return "local"
	}
	if a.OIDC.Enabled {
		return "oidc"
	}
	return "local"
}

// OIDCConfig is one OpenID Connect provider (authentik, Keycloak, Authelia, ...): the issuer's
// discovery document supplies the endpoints; the authorization-code flow with PKCE brings the
// user back to /api/auth/v1/oidc/callback; claims come from the userinfo endpoint.
//
// A provider login needs an account of the same name here (task 19, D-53): the value of
// UsernameClaim must equal an account's name exactly, nothing is created from a provider identity,
// and the role is the account's (D-54). The keys groups_claim and admin_groups of earlier versions
// are therefore gone; Load reports a file that still carries them (Config.Stale).
type OIDCConfig struct {
	Enabled      bool   `json:"enabled"`
	Name         string `json:"name"`          // the button label, e.g. "authentik"
	Issuer       string `json:"issuer"`        // https://auth.example.org/application/o/openccu-lite/
	ClientID     string `json:"client_id"`     //
	ClientSecret string `json:"client_secret"` // empty for a public client (PKCE only)
	// UsernameClaim is the claim whose value names the account (default preferred_username).
	UsernameClaim string `json:"username_claim"`
	// Scopes defaults to "openid profile email".
	Scopes string `json:"scopes"`
	// PasswordLogin (default true) offers the password form and POST /login beside the provider.
	// Off, the provider is the only way in through the web interface and the console is the way
	// back (`occulited auth password-login on`, `occulited passwd`); AuthConfig.PasswordLoginOn
	// says when the switch holds.
	PasswordLogin bool `json:"password_login"`
}

// CatalogConfig configures the addon catalogue.
type CatalogConfig struct {
	Enabled bool     `json:"enabled"`
	URLs    []string `json:"urls"`
	// Daily is the daily check of the catalogue's releases and the installed addons' updates
	// (task 244: *Check daily* on the Addons page); absent = off (D-90, B-241; it meant on until
	// then - SettleOutbound keeps that for a system that ran before).
	Daily *bool `json:"daily,omitempty"`
}

// DailyOn is Daily with its default.
func (c CatalogConfig) DailyOn() bool { return c.Daily != nil && *c.Daily }

// DefaultCatalogURL is where the catalogue file is published: occulited's own repository on GitHub
// (catalog/catalog.json on master, D-119). The raw file, not a release asset: the adapter manifests
// are read from beside it (catalog/manifests/).
const DefaultCatalogURL = "https://raw.githubusercontent.com/hobbyquaker/occulited/master/catalog/catalog.json"

// oldDefaultCatalogPaths are the defaults of earlier versions, which named the private Gitea the
// project was developed on before it was published: the openccu-lite-addons index (the catalogue
// until 2026-09-23, D-119) and this repository's own file there. A stored configuration that still
// names one follows the new default. They are matched by the path on that forge, so the forge's
// host name does not have to be spelled out here.
var oldDefaultCatalogPaths = []string{
	"/hobbyquaker/openccu-lite-addons/raw/branch/master/index.json",
	"/hobbyquaker/occulited/raw/branch/master/catalog/catalog.json",
}

// oldDefaultCatalog reports whether u is one of the old defaults: an https URL whose host is not
// GitHub's and whose path is one of oldDefaultCatalogPaths.
func oldDefaultCatalog(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "https" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "", "github.com", "raw.githubusercontent.com":
		return false
	}
	return slices.Contains(oldDefaultCatalogPaths, parsed.Path)
}

// BundledCatalog is the copy of catalog/catalog.json the image carries (installed by the buildroot
// package from the same source archive as the binary). It comes *after* the published file in the
// default list: the first occurrence of a repository wins, so a reachable published file (with new
// entries) takes precedence, and the bundled copy is what a box without network falls back to.
const BundledCatalog = "file:///etc/occulite/catalog.json"

// BundledManifestsDir holds the adapter manifests the image carries beside the bundled catalogue
// (catalog/manifests/ of the occulited repository, D-119).
const BundledManifestsDir = "/etc/occulite/manifests"

// FirmwareConfig configures the fetcher.
type FirmwareConfig struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`
	Base    string `json:"base,omitempty"`
}

// DefaultListen is occulited's own listener, which lighttpd proxies to (D-3, D-29).
//
// Task 182: it was 127.0.0.1:2121 until 2026-09-22, and 2121 is CCU-Jack's established HTTP port
// (its own docs, and OpenCCU's Home Assistant addon maps it) - an installed CCU-Jack found the
// port taken and served nothing. 8183 was ReGaHSS's, and this firmware has no ReGaHSS (D-1), so
// nothing on the system wants it: the maintainer's choice.
const DefaultListen = "127.0.0.1:8183"

// oldDefaultListen is what a file written before task 182 carries; Load moves it to DefaultListen
// so an untouched system follows, while a listener someone chose on purpose stays.
const oldDefaultListen = "127.0.0.1:2121"

// Default is what a fresh box runs with. The three outbound switches - the device firmware
// check, the release check, the catalogue's daily check - are off (D-90, B-241): the welcome page
// asks once, naming each destination, and each has its switch on its page.
func Default() Config {
	return Config{Listen: DefaultListen, StateDir: "/usr/local/etc/occulite", LogLevel: "info", Firmware: FirmwareConfig{Enabled: false, Dir: "/etc/config/firmware"}, Catalog: CatalogConfig{Enabled: true, URLs: []string{DefaultCatalogURL, BundledCatalog}}, SystemUpdate: SystemUpdateConfig{Enabled: false, Feed: DefaultSystemUpdateFeed}, Addons: AddonsConfig{DefaultMode: "confined"},
		Auth: AuthConfig{OIDC: OIDCConfig{PasswordLogin: true}}}
}

// OutboundSwitches are the keys SettleOutbound decides once per system, in the file's order.
var OutboundSwitches = []string{"firmware.enabled", "system_update.enabled", "catalog.daily"}

// outboundUnset lists the outbound switches the file does not carry. A missing file lacks all
// three; a file the daemon saved before B-241 carries the two booleans (never omitted) and lacks
// catalog.daily unless the user switched it.
func outboundUnset(b []byte) []string {
	var raw struct {
		Firmware struct {
			Enabled *bool `json:"enabled"`
		} `json:"firmware"`
		SystemUpdate struct {
			Enabled *bool `json:"enabled"`
		} `json:"system_update"`
		Catalog struct {
			Daily *bool `json:"daily"`
		} `json:"catalog"`
	}
	_ = json.Unmarshal(b, &raw)
	var out []string
	if raw.Firmware.Enabled == nil {
		out = append(out, "firmware.enabled")
	}
	if raw.SystemUpdate.Enabled == nil {
		out = append(out, "system_update.enabled")
	}
	if raw.Catalog.Daily == nil {
		out = append(out, "catalog.daily")
	}
	return out
}

// SettleOutbound writes the outbound switches the file lacks, once (B-241, D-90): on a system
// whose setup is done the values it has been running with - on, the defaults before B-241 - so
// nothing changes under a user who chose or accepted them; on a fresh system (no administrator
// yet) the new defaults, off, so a restart after the setup cannot turn them on. The file then
// carries all three explicitly, whatever version wrote it, and the decision is never taken again.
// It returns the keys it wrote; nothing is written when the file carries them all.
func SettleOutbound(path string, c *Config, setupDone bool) ([]string, error) {
	if len(c.OutboundUnset) == 0 {
		return nil, nil
	}
	for _, k := range c.OutboundUnset {
		switch k {
		case "firmware.enabled":
			c.Firmware.Enabled = setupDone
		case "system_update.enabled":
			c.SystemUpdate.Enabled = setupDone
		case "catalog.daily":
			on := setupDone
			c.Catalog.Daily = &on
		}
	}
	if err := Save(path, *c); err != nil {
		return nil, err
	}
	wrote := c.OutboundUnset
	c.OutboundUnset = nil
	return wrote, nil
}

// Load reads path over the defaults; a missing file is not an error.
func Load(path string) (Config, error) {
	c := Default()
	c.OutboundUnset = OutboundSwitches
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	// task 182: a file that still names the old default follows the new one. Only that exact
	// value - anything else is a choice and is left alone.
	if c.Listen == oldDefaultListen {
		c.Listen = DefaultListen
	}
	// D-119: the catalogue moved into the occulited repository, and with its publication to
	// GitHub; a file that names an old default follows. Only those exact values - another URL is
	// a choice and is left alone.
	for i, u := range c.Catalog.URLs {
		if oldDefaultCatalog(u) {
			c.Catalog.URLs[i] = DefaultCatalogURL
		}
	}
	// task 258: the feed moved from releases/latest to the release list; only that exact value.
	if c.SystemUpdate.Feed == oldDefaultSystemUpdateFeed {
		c.SystemUpdate.Feed = DefaultSystemUpdateFeed
	}
	c.Stale = stale(b)
	c.OutboundUnset = outboundUnset(b)
	return c, nil
}

// stale lists the keys of earlier versions a file still sets to something: the group-to-role
// mapping of task 19's first shape (the role is the account's since D-54). An absent, null or
// empty value is not reported - occulited itself wrote `"admin_groups": null` for a while.
func stale(b []byte) []string {
	var old struct {
		Auth struct {
			OIDC struct {
				GroupsClaim string   `json:"groups_claim"`
				AdminGroups []string `json:"admin_groups"`
			} `json:"oidc"`
		} `json:"auth"`
	}
	_ = json.Unmarshal(b, &old)
	var out []string
	if old.Auth.OIDC.GroupsClaim != "" {
		out = append(out, "auth.oidc.groups_claim")
	}
	if len(old.Auth.OIDC.AdminGroups) > 0 {
		out = append(out, "auth.oidc.admin_groups")
	}
	return out
}

// SetPasswordLogin writes the password-login switch into the file at path: what the console's
// `occulited auth password-login on|off` does (task 19) - the break-glass when the provider is
// down and the switch is off. The running daemon reads the file again before its next password
// login, so no restart is needed. Off is written only in mode oidc, where it means something.
func SetPasswordLogin(path string, on bool) (Config, error) {
	c, err := Load(path)
	if err != nil {
		return c, err
	}
	if !on && c.Auth.EffectiveMode() != "oidc" {
		return c, errors.New("password login can only be switched off while the mode is oidc: without the provider it is the only login")
	}
	c.Auth.OIDC.PasswordLogin = on
	return c, Save(path, c)
}

// SetPublic writes auth.public (task 193): the Remote access page's switch, in force at once.
func SetPublic(path string, on bool, account string) (Config, error) {
	c, err := Load(path)
	if err != nil {
		return c, err
	}
	c.Auth.Public = PublicConfig{Enabled: on, Account: account}
	return c, Save(path, c)
}

// Save writes path atomically. Run as root - the console's `occulited auth password-login on` -
// the new file keeps the owner of the one it replaces (or of its directory), or the daemon, which
// runs as its own user, could not read its own configuration any more (0600).
func Save(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".occulited-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		owner, err := os.Stat(path)
		if err != nil {
			owner, err = os.Stat(filepath.Dir(path))
		}
		if err == nil {
			if sys, ok := owner.Sys().(*syscall.Stat_t); ok {
				if err := os.Chown(tmp.Name(), int(sys.Uid), int(sys.Gid)); err != nil {
					return err
				}
			}
		}
	}
	return os.Rename(tmp.Name(), path)
}
