package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hobbyquaker/occulited/internal/acme"
	"github.com/hobbyquaker/occulited/internal/addonupdates"
	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/backuptarget"
	"github.com/hobbyquaker/occulited/internal/bootchart"
	"github.com/hobbyquaker/occulited/internal/bootexpect"
	"github.com/hobbyquaker/occulited/internal/devstate"
	"github.com/hobbyquaker/occulited/internal/hbrfeth"
	"github.com/hobbyquaker/occulited/internal/health"
	"github.com/hobbyquaker/occulited/internal/httpwait"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/led"
	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/hobbyquaker/occulited/internal/logctl"
	"github.com/hobbyquaker/occulited/internal/radio"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/rpctrace"
	"github.com/hobbyquaker/occulited/internal/servicemsg"
	"github.com/hobbyquaker/occulited/internal/shares"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"strconv"

	"filippo.io/age"

	"github.com/hobbyquaker/occulited/internal/catalog"
	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/firmware"

	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/sysupdate"
	"github.com/hobbyquaker/occulited/internal/trust"
)

// AddonLister lists installed addons: system.AddonScripts runs every rc.d script's info,
// system.SystemdAddons adds the units' state.
type AddonLister interface {
	ListAddons(ctx context.Context) ([]system.Addon, error)
}

// NavProvider lists the shell's menu entries (nav.d drop-ins and the addons that bring a web
// frontend of their own).
type NavProvider interface {
	NavEntries(ctx context.Context) []system.NavEntry
}

// ServiceManager is what the services routes drive: systemctl, or system.NoInit without systemd.
type ServiceManager = system.ServiceManager

// TimerLister lists systemd timers; nil without systemd.
type TimerLister interface {
	Timers(ctx context.Context) ([]system.Timer, error)
}

// Catalog is the addon catalogue (task 10, D-119): the view from the cache, the user's check, the
// install from a manifest's release source.
type Catalog interface {
	Fetch(ctx context.Context, force bool) (*catalog.View, error)
	Refresh(ctx context.Context) error
	// Start takes the one install slot for the addon and installs it in the background; done
	// receives the result. catalog.ErrInstallRunning while an install runs (B-25).
	Start(ctx context.Context, id string) (done <-chan error, err error)
	Progress() *catalog.Progress
	// Image is a catalogue entry's image of a kind (openccu-lite task 100), the copy the check
	// fetched with the manifest; fs.ErrNotExist for none.
	Image(id, kind string) ([]byte, error)
}

// Firmware is the device-firmware service (task 11).
type Firmware interface {
	Status() firmware.State
	Trigger()
	SetEnabled(bool)
	DeployUpload(path string) (*firmware.Bundle, error)
	BundleFile(typeCode, name string) ([]byte, error)
}

// AddonManager installs, removes and checks addons (task 10).
type AddonManager interface {
	Install(ctx context.Context, archive io.Reader) (*system.InstallResult, error)
	Uninstall(ctx context.Context, id string) (system.UninstallResult, error)
	CheckUpdate(ctx context.Context, a system.Addon, base string) system.UpdateInfo
	Reboot(ctx context.Context) error
}

// SystemAPI serves /api/system/v1: status, radio, services, addons, log.
type SystemAPI struct {
	Root system.Root
	// Version is occulited's own main.version, answered on /status (task 133): the commit it was
	// built from, -dirty or -hot after it (occulited task 16)
	Version string
	// Commit is the commit occulited was built from (main.commit), the bare hash, answered beside it
	Commit   string
	Services system.ServiceManager
	Log      system.LogReader
	Addons   AddonLister
	Manager  AddonManager
	Nav      NavProvider
	Firmware Firmware
	Catalog  Catalog
	// DataStore is occulited's database file's setting (task 214); nil = the routes answer 501.
	DataStore DataStore
	// OnFirmwareToggle persists the switch (the config file); nil = memory only.
	OnFirmwareToggle func(enabled bool) error
	// OnSystemUpdateToggle persists the release feed's daily check (task 244); nil = memory only.
	OnSystemUpdateToggle func(enabled bool) error
	// HmIPServerDiagrams reads and OnHmIPServerDiagrams persists hmipserver.diagrams (task 33);
	// nil = the route answers 501.
	HmIPServerDiagrams   func() bool
	OnHmIPServerDiagrams func(on bool) error
	// CatalogDaily and OnCatalogDaily are the catalogue's and the addon updates' daily check
	// (task 244): its state, and the switch that persists and applies it; nil = always on, fixed.
	CatalogDaily   func() bool
	OnCatalogDaily func(on bool) error
	// WebBase is where an addon's own update check is fetched from when its `Update:` URL is
	// box-relative: **occulited's own listener**, http://127.0.0.1:8183, not lighttpd on :80.
	// Through lighttpd the request meets the mod_magnet session gate, which takes a cookie or
	// ?sid= and knows nothing about the Bearer token this daemon holds, so the check comes back
	// as a 401 body (B-2 for the daily service, B-32 for this one). occulited's own CGI route
	// accepts the box's local token.
	WebBase string
	// MetaRecovered is surfaced on the status page: the store had to fall back to its backup.
	MetaRecovered bool
	// NetTx is the confirm-or-revert transaction for network changes (task 8); nil = read-only.
	NetTx *system.NetTx
	// IPv6 is the per-interface IPv6 configuration with its rollback (task 227); nil = none.
	IPv6 *system.IPv6Tx
	// Run executes the system commands behind firewall/time writes; nil = exec for real. main
	// substitutes the dry runner when --root is not /.
	Run system.Runner
	// RunStdin executes the commands that read their standard input - the restore, which reads
	// the security key (B-144); nil = exec for real. main substitutes the dry runner as for Run.
	RunStdin system.StdinRunner
	// PasswordHasher makes root's SSH password hash; nil = mkpasswd in the daemon's own process
	// (openccu-lite task 302). Never the Runner: that is the privilege helper, which has no
	// mkpasswd and takes no stdin, so the password would never reach it. Tests substitute a fake.
	PasswordHasher system.PasswordHasher
	// Firewall composes USERPORTS from the manual list and the addons' opened ports (D-47);
	// nil = one on Root with the default state directory.
	Firewall *system.FirewallManager
	// Health is the radio sampler (duty cycle, link); nil = not available.
	Health *health.Sampler
	// RPC is the box's own event subscriber (task 75, D-115: inside occulited); nil = not available.
	RPC *rpcsub.Subscriber
	// ServiceMessages is the service-message store the feed and the sweep fill (task 75); nil =
	// the route answers 501.
	ServiceMessages *servicemsg.Store
	// LiteRPC is lite-rpc (task 77, D-115): the request paths and the stream under /api/rpc/v1;
	// nil = 501 there, and the Remote access page says it is not available.
	LiteRPC *literpc.Service
	// State is the state store (openccu-lite task 194): GET /api/rpc/v1/state
	State *devstate.Store
	// History is the datapoint history (task 195): GET /api/rpc/v1/history
	History *devstate.History
	// HBRFETH is the network radio board's retry (task 218); HBRFETHClient reads boards (nil: the
	// LAN); OwnAddresses the system's IPv4 addresses (nil: the interfaces'), both for tests
	HBRFETH       *radio.HBRFETHWatch
	HBRFETHClient *hbrfeth.Client
	OwnAddresses  func() []string
	// Groups is task 180's heating groups through hmipserver's group pages; nil = unsupported.
	Groups *GroupsAPI
	// Public is the Control app's public mode (task 193): the auth API's switch, shown and set on
	// the Remote access page; nil where there is no auth API.
	Public PublicSwitch
	// PairingFeed is the pairing requests for GET /stream's topic pairing (occulited B-53); nil =
	// the topic sends nothing. main gives it the auth API.
	PairingFeed PairingFeed
	// Revalidate re-checks a request's credential while its stream is open (every heartbeat);
	// nil = never re-checked. main gives it the auth API's session lookup.
	Revalidate func(r *http.Request) *auth.Session
	// ConsoleResets lists the accounts whose access `occulited admin reset-auth` reset after a
	// time (occulited task 14), for the Status page's notice; nil = none. main gives it the
	// auth store's.
	ConsoleResets func(since time.Time) []auth.ConsoleReset
	// RPCTrace is the RPC trace's switch (task 79); nil = the routes say it is not available.
	RPCTrace *rpctrace.Tracer
	// Names looks a device up in the metadata store for the service-message list: its name and
	// its taxonomy memberships; nil = no names.
	Names func(ref string) (name string, enums []string, ok bool)
	// Updates is the daily addon update check; nil = not available.
	Updates *addonupdates.Service
	// installs are the upload installs, run detached from their request (B-4)
	installs installJobs
	// addonFeed is the addons' revision and its open streams (openccu-lite B-297)
	addonFeed addonChanges
	// installGate makes the upload's and the catalogue's "none runs - start" one step each, so
	// the two never both start (B-25)
	installGate sync.Mutex
	// FirstBoot is the result of the regadom import at first boot (D-35); nil = none happened.
	FirstBoot *FirstBootImport
	// Journal is set on a systemd box: the live log stream and the journal's extra filters.
	Journal *system.JournalLog
	// logCap is where a log download stops; 0 = logDownloadCap. The tests make it small.
	logCap int64
	// Timers lists systemd timers; nil without systemd.
	Timers TimerLister
	// ChangeKey sends a new security key to rfd (task 8); nil = crypttool only (development).
	ChangeKey func(ctx context.Context, key string) error
	// SystemKeyCheck checks a passphrase against this system's own BidCos security key
	// (openccu-lite task 296): set, match, known as system.Root.SystemKeyMatches, which nil means.
	SystemKeyCheck func(ctx context.Context, passphrase string) (set, match, known bool)
	// backupSigs keeps the signature of the checked upload, whose MD5 takes a moment (task 296)
	backupSigs backupSigCache
	// AddonCtl answers which addon an addonctl token belongs to (28.8); nil = the route answers 501.
	AddonCtl AddonController
	// InstallToken is the credential of POST /addons/install/local (openccu-lite B-274, the fork's
	// /bin/install_addon): system.InstallTokenFile's secret; "" = the route answers 401.
	InstallToken string
	// SetLogLevel applies a level to rfd (BidCos-RF) or hs485d (BidCos-Wired) live over XML-RPC
	// (task 27.8); nil = the file only, the daemons pick it up at their next start.
	SetLogLevel func(ctx context.Context, iface string, level int) error
	// OcculitedLog is occulited's own level and debug areas (task 101): stored in occulited.json
	// and applied live; nil = GET /loglevels answers the default and a PUT that changes it 501.
	OcculitedLog OcculitedLogLevel
	// InitInterface makes the XML-RPC init(url, id) call on the interface process of that name
	// (task 76: with an empty id, the unsubscribe); nil = the route answers 501.
	InitInterface func(ctx context.Context, iface, url, id string) error
	// StallSource replaces RPC.Stalls in the tests (openccu-lite B-201).
	StallSource func() []rpcsub.Stall
	// stalls are the checks of the stalled interfaces' listeners (B-201).
	stalls stallState
	// Feed is the release feed check (task 16); nil = uploads only.
	Feed *sysupdate.Service
	// Cert is the ACME certificate service (task 35); nil = the routes answer 501.
	Cert *acme.Service
	// Trust is the four trust stores (openccu-lite task 231); nil = the trust routes answer 501.
	Trust *trust.Store
	// PinTarget names the server a purpose's connections go to (openccu-lite task 232: the OIDC
	// issuer, the ACME directory), for Pin the current certificate; nil or "" = none configured.
	PinTarget func(purpose string) string
	// RadioFirmware is the coprocessor firmware service (task 41); nil = the routes answer 501.
	RadioFirmware *system.RadioFirmware
	// RadioConnections: the connection per interface process (task 129 phase 3).
	RadioConnections *system.RadioConnections
	// RadioBusy says whether a connection change or a coprocessor flash holds the radio stack: an
	// addon start through the API waits meanwhile (openccu-lite B-307); nil = never.
	RadioBusy func() bool
	// FirewallRules: task 157's firewall; nil = the rule routes answer 501.
	FirewallRules *system.FirewallRules
	// HmIPLocalKey: task 149's local key mode; nil = the routes answer 501.
	HmIPLocalKey *system.HmIPLocalKey
	// HmIPDeviceKeys: task 154's device keys; nil = the routes answer 501.
	HmIPDeviceKeys *system.HmIPDeviceKeys
	// ConfirmTicket spends a confirmed ticket (task 154): true when it was issued for path to the
	// session id. The auth store's RedeemConfirmed.
	ConfirmTicket func(ticket, path, sessionID string) bool
	// BackupCrypt is task 91's backup encryption (the recovery key, the system's identity); nil =
	// the encryption routes answer 501 and backups are handed out plain.
	BackupCrypt *backupcrypt.Store
	// BackupTargets is task 86's targets (the USB directory, NFS, SMB, SFTP); nil = the routes
	// answer 501.
	BackupTargets *backuptarget.Manager
	// Shares is task 228's SMB and NFS shares (System → Storage); nil = the routes answer 501.
	Shares *shares.Manager
	// AddonPayload (openccu-lite task 146) records every addon's .nobackup directories and says
	// after a restore which addon came back without its program files; nil = never missing.
	AddonPayload *system.PayloadRecord
	// HTTPS applies the HTTP → HTTPS redirect and HSTS markers (task 36); nil = the routes answer 501.
	HTTPS *system.HTTPSConfig
	// ClassicRPC is System -> Remote access (task 143); nil answers 501.
	// WiFi is the Network page's Wi-Fi (task 89); nil where there is none.
	WiFi       *system.WiFi
	ClassicRPC *system.ClassicRPCConfig
	// Power halts the box (task 60); nil = POST /halt answers 501.
	Power *system.Power
	// RadioInterfaces are the XML-RPC clients of the interfaces in InterfacesList.xml (the daemon's
	// radioIfs); the factory reset's warning lists their devices (task 109). nil = unknown.
	RadioInterfaces func() []interfaces.Interface
	// ImportRecord is the record of the last device import (openccu-lite task 275); nil = none is
	// kept, GET /radio/import answers {imported: false}.
	ImportRecord *system.ImportRecord
	// NamesImport imports the ReGa names of a checked backup before the device import reboots
	// (openccu-lite task 281): MetaAPI.ImportNamesFromSBK; nil = no names are imported.
	NamesImport func(sbkPath string) NamesImportResult
	// LAN finds eQ-3's LAN devices and writes their network settings (openccu-lite task 220); nil =
	// an eq3disc.Client. LANNetworks and AddressInUse are test seams (nil = this system's).
	LAN          LANFinder
	LANNetworks  func() []netip.Prefix
	AddressInUse func(ctx context.Context, ip netip.Addr) bool

	// servicesPoll turns what the service listing costs into the interval GET /services suggests
	servicesPoll pollMeter
	// journalCheck is the last write test of the journal's copy target (B-213)
	journalCheck journalTargetCheck
	// units is the last reading of the radio stack's units for GET /radio/health (task 94)
	units unitsCache
	// Clock answers the boot's clock gate for GET /status (task 94); nil = no clock in the status.
	Clock *system.ClockCheck

	// Storage is the storage health panel (task 69); nil = GET /storage answers 501.
	Storage *system.Storage

	// BootTiming records how long user-initiated reboots take (task 94): the power routes, the
	// system update's install and the restore mark the reboot before they start it. nil = no
	// marker, GET /boot-expect answers the product default and POST /boot-timing 501.
	BootTiming *bootexpect.Recorder

	// Warnings is the Status page's warnings and their silences (task 81, WarningTracker); nil =
	// the routes answer 501.
	Warnings *WarningTracker
	// warn is the warnings' own state: the cached security key, B-92's ownership check
	warn warningsState
	// CrashLoops is the crash-loop sampler and addon supervisor (openccu-lite task 283); nil = no
	// crash-loop warning.
	CrashLoops *system.CrashLoops
	// Legacy keeps the legacy session switches (task 125, legacysession.go); nil = on for every
	// addon, and the routes answer 501.
	Legacy LegacySessions
	// Early keeps the early start's switches (task 119, earlystart.go); nil = on for every addon,
	// and the routes answer 501.
	Early EarlyStarts

	// Dmesg reads the kernel log where there is no journal (task 93); nil = dmesg on this box.
	Dmesg system.LogReader
	// LED is the status LED controller (task 95, led.go); nil = the routes answer 501.
	LED *led.Controller
	// bootList keeps journalctl --list-boots for a moment: the Log page asks on every visit
	bootList bootsCache
	// BootChart is the boot timeline of the Services page (task 93); nil = GET /boot answers 501.
	BootChart *bootchart.Reader
	// BootSnapshots keeps finished boots' timelines (task 93); nil = this boot only.
	BootSnapshots *bootchart.Store
}

// FirstBootImport is what the welcome page reports after an update from OpenCCU.
type FirstBootImport struct {
	At        time.Time `json:"at"`
	Source    string    `json:"source"`
	Objects   int       `json:"objects"`
	Devices   int       `json:"devices"`
	Channels  int       `json:"channels"`
	Rooms     int       `json:"rooms"`
	Functions int       `json:"functions"`
	Unnamed   int       `json:"unnamed"`
	Error     string    `json:"error,omitempty"`
	// GaveUp: why a failed import is not tried again (B-170) - the store holds names by now, or
	// the file is gone. Set only in the marker; a given-up import is not reported.
	GaveUp string `json:"gave_up,omitempty"`
	// DisabledAddons: installed addons that need the ReGa, disabled on this first boot.
	DisabledAddons []string `json:"disabled_addons,omitempty"`
	// DisabledBinaryAddons: installed addons whose binaries this box cannot execute - an addon
	// that came along from a stock CCU3 or another architecture (task 25). Disabled on this
	// first boot too, and shown as "disabled, needs update".
	DisabledBinaryAddons []string `json:"disabled_binary_addons,omitempty"`
}

// Register mounts the routes on mux.
func (a *SystemAPI) Register(mux *http.ServeMux) {
	p := "/api/system/v1"
	route(mux, auth.ScopeSystemRead, "GET "+p+"/status", a.status)
	route(mux, auth.ScopeSystemRead, "GET /api/openapi.json", a.openAPI) // task 298, openapi.go
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio", a.radio)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/services", a.services)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/services/{id}/{action}", a.control)
	route(mux, scopeOpen, "POST "+p+"/addonctl", a.addonCtl)
	route(mux, scopeOpen, "GET "+p+"/sbom", a.sbom)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/wifi", a.wifiGet)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/wifi", a.wifiPut)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/wifi/scan", a.wifiScan)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/wifi/networks", a.wifiConnect)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/wifi/networks/{ssid}", a.wifiForget)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/wifi/confirm", a.wifiConfirm)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/journal", a.journalConfig)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/journal", a.journalConfigPut)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/journal/sync", a.journalSyncNow)
	// openccu-lite task 214: occulited's database file - the health history now
	route(mux, auth.ScopeSystemRead, "GET "+p+"/datastore", a.dataStoreGet)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/datastore", a.dataStorePut)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/datastore/history", a.dataStoreHistoryGet)  // task 195
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/datastore/history", a.dataStoreHistoryPut) // task 195
	route(mux, auth.ScopeSystemRead, "GET "+p+"/loglevels", a.logLevels)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/loglevels", a.logLevelsPut)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/restart", a.radioRestart)
	a.registerHBRFETH(mux, p) // task 218
	// D-66: rfd's device descriptions - the image's plus what addons added in the writable layer
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio/device-descriptions", a.deviceDescriptions)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/device-descriptions/reset", a.deviceDescriptionsReset)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/services/{id}/unit", a.unitOverride)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/services/{id}/unit", a.unitOverridePut)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/addons", a.addons)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/addons/stream", a.addonsStream)                       // openccu-lite B-297
	route(mux, auth.ScopeSystemRead, "GET "+p+"/stream", a.shellStream)                               // occulited B-53: the shell's streams in one
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/addons/{id}/reinstall-dismiss", a.reinstallDismiss) // openccu-lite task 146
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/addons/{id}/enable", a.addonEnable)
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/addons/{id}/disable", a.addonDisable)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/addons/{id}/policy", a.addonPolicy)
	route(mux, auth.ScopeAddonsWrite, "PUT "+p+"/addons/{id}/policy", a.addonPolicyPut)
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/addons/install", a.install)
	route(mux, scopeOpen, "POST "+p+"/addons/install/local", a.installLocal)   // openccu-lite B-274: its own token
	route(mux, auth.ScopeSystemRead, "GET "+p+"/addons/install", a.installJob) // B-4: the job
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/addons/{id}/uninstall", a.uninstall)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/addons/{id}/update", a.update)
	a.registerAddonImages(mux, p) // openccu-lite task 100: the addons' icons and logos
	route(mux, auth.ScopeSystemRead, "GET "+p+"/addons/updates", a.updates)
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/addons/updates/check", a.updatesCheck)
	route(mux, auth.ScopePower, "POST "+p+"/reboot", a.reboot)
	route(mux, auth.ScopePower, "POST "+p+"/reboot/recovery", a.rebootRecovery)
	route(mux, auth.ScopePower, "POST "+p+"/halt", a.halt)
	a.registerFactoryReset(mux, p) // task 109
	route(mux, auth.ScopeSystemRead, "GET "+p+"/boot-expect", a.bootExpect)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/boot-timing", a.bootTiming)
	route(mux, auth.ScopeLogsRead, "GET "+p+"/log", a.log)
	route(mux, auth.ScopeLogsRead, "GET "+p+"/log/stream", a.logStream)
	route(mux, auth.ScopeLogsRead, "GET "+p+"/log/download", a.logDownload)
	route(mux, auth.ScopeLogsRead, "GET "+p+"/boots", a.listBoots)
	route(mux, auth.ScopeLogsRead, "GET "+p+"/boot", a.bootTimeline)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/timers", a.timers)
	// task 50: own timers, local-<name>.timer with its .service
	route(mux, auth.ScopeSystemRead, "GET "+p+"/timers/own", a.localTimers)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/timers/own", a.localTimerCreate)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/timers/own/{name}", a.localTimer)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/timers/own/{name}", a.localTimerPut)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/timers/own/{name}", a.localTimerDelete)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/timers/own/{name}/run", a.localTimerRun)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/timers/calendar", a.timerCalendar)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/nav", a.nav)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/network", a.network)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/network", a.networkBegin)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/network/pending", a.networkPending)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/network/confirm", a.networkConfirm)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/network/revert", a.networkRevert)
	a.registerIPv6(mux, p) // ipv6conf.go
	// task 157 (D-105): the rule list, its draft and the confirm window, the listeners, the addons' ports
	route(mux, auth.ScopeSystemRead, "GET "+p+"/firewall", a.firewall)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/firewall", a.firewallPut)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/firewall/draft", a.firewallDiscard)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/firewall/apply", a.firewallApply)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/firewall/confirm", a.firewallConfirm)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/firewall/revert", a.firewallRevert)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/firewall/migration/dismiss", a.firewallDismiss)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/firewall/listeners", a.firewallListeners)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/firewall/counters", a.firewallCounters)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/firewall/counters/reset", a.firewallCountersReset)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/firewall/addons", a.firewallAddons)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/firewall/addons/{id}/ports", a.firewallAddonPorts)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/time", a.timeConfig)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/time", a.timePut)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/time/zones", a.timeZones)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/time/clock", a.timeClock)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio/health", a.radioHealth)
	// task 75: the service messages, and their stream for an open Status page
	route(mux, auth.ScopeSystemRead, "GET "+p+"/service-messages", a.serviceMessages)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/service-messages/stream", a.serviceMessagesStream)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/usb", a.usb)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/usb/storage", a.usbStorage)
	a.registerStorageUSB(mux, p) // storageusb.go, task 228
	a.registerShares(mux, p)     // storageshares.go, task 228 phase 2
	a.registerLocations(mux, p)  // storagelocations.go, task 228 phases 3-4
	route(mux, auth.ScopeSystemRead, "GET "+p+"/storage", a.storage)
	route(mux, auth.ScopePower, "GET "+p+"/radio/firmware", a.radioFirmware)
	route(mux, auth.ScopePower, "POST "+p+"/radio/firmware/upload", a.radioFirmwareUpload)
	route(mux, auth.ScopePower, "DELETE "+p+"/radio/firmware/{module}/{name}", a.radioFirmwareDelete)
	route(mux, auth.ScopePower, "POST "+p+"/radio/firmware/flash", a.radioFirmwareFlash)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio/connections", a.radioConnections)
	route(mux, auth.ScopeSystemRead, "POST "+p+"/radio/connections/preview", a.radioConnectionsPreview)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/radio/connections", a.putRadioConnections)
	// task 149 (D-103): local key mode, the administrator's alone
	route(mux, auth.ScopeSystemWrite, "GET "+p+"/radio/hmip/local-key", a.localKeyStatus)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/radio/hmip/local-key", a.localKeyEnable)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/radio/hmip/local-key", a.localKeyDisable)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/hmip/local-key/override", a.localKeyOverride)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/radio/hmip/local-key/snapshots/{sgtin}", a.localKeyDiscard)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/hmip/local-key/snapshots/{sgtin}/restore", a.localKeyRestore)
	// task 155 (D-106): the adapter exchange's diagnosis, the retry and the guided fresh start
	route(mux, auth.ScopeSystemWrite, "GET "+p+"/radio/hmip/exchange", a.exchangeView)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/hmip/exchange/retry", a.exchangeRetry)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/hmip/exchange/fresh-start", a.exchangeFreshStart)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/hmip/module-move/back", a.hmipMoveBack) // openccu-lite B-285
	// task 154 (D-104): the device keys, all under radio:keys; the export asks a session to confirm
	route(mux, auth.ScopeRadioKeys, "GET "+p+"/radio/hmip/device-keys", a.deviceKeys)
	route(mux, auth.ScopeRadioKeys, "POST "+p+"/radio/hmip/device-keys", a.deviceKeyAdd)
	route(mux, auth.ScopeRadioKeys, "DELETE "+p+"/radio/hmip/device-keys/{sgtin}", a.deviceKeyDelete)
	route(mux, auth.ScopeRadioKeys, "POST "+p+"/radio/hmip/device-keys/apply", a.deviceKeysApply)
	route(mux, auth.ScopeRadioKeys, "GET "+p+"/radio/hmip/device-keys/export", a.deviceKeysExport)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio/hmip/access-points", a.accessPoints) // openccu-lite task 217
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio/lan-devices", a.lanDevices)          // openccu-lite task 220
	route(mux, auth.ScopeSystemRead, "POST "+p+"/radio/lan-devices/search", a.lanSearch)   // task 237: only this sends
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/lan-devices/{serial}/network", a.lanNetwork)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio/lan-gateways", a.lanGateways)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/radio/lan-gateways", a.putLANGateways)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/lan-gateways/key", a.lanGatewayKey)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/security-key", a.securityKey)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/subscribers/remove", a.radioSubscriberRemove)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio/subscribers/reachability", a.radioSubscriberReach)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/radio/subscribers/stalls", a.radioStalls) // openccu-lite B-201
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/radio/subscribers/drop", a.radioStallDrop)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/ssh", a.ssh)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/ssh", a.sshPut)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/ssh/password", a.sshPassword)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/ssh/key-only", a.sshKeyOnly)
	// task 185: root's keys and the SSH sessions open now (sshaccess.go)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/ssh/keys", a.sshKeys)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/ssh/keys", a.sshKeyAdd)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/ssh/keys", a.sshKeyRemove)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/ssh/sessions", a.sshSessions)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/ssh/sessions/{id}", a.sshSessionEnd)
	// task 35: the box's TLS certificate
	route(mux, auth.ScopeSystemRead, "GET "+p+"/certificate", a.certificate)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/certificate/settings", a.certificateSettingsPut)
	a.registerTrust(mux, p)          // openccu-lite task 231
	a.registerPins(mux, p)           // openccu-lite task 232
	a.registerRestoreDevices(mux, p) // openccu-lite task 251
	a.registerRestoreKey(mux, p)     // openccu-lite task 296
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/certificate/test", a.certificateStart(acme.KindTest))
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/certificate/issue", a.certificateStart(acme.KindIssue))
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/certificate/renew", a.certificateStart(acme.KindRenew))
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/certificate/self-signed", a.certificateSelfSigned)
	// task 38: mode manual - the user's certificate, a key and CSR made here, the preview
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/certificate/manual", a.certificateManualPut)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/certificate/key", a.certificateKey)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/certificate/csr", a.certificateCSR)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/certificate/inspect", a.certificateInspect)
	// task 36: the HTTP → HTTPS redirect and HSTS (System → Certificate since task 132)
	// task 180: heating groups; the CGI path is hmipserver's session check, loopback only
	route(mux, auth.ScopeSystemRead, "GET "+p+"/groups", a.groupsList)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/groups/types", a.groupsTypes)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/groups/{id}", a.groupsGet)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/groups", a.groupsCreate)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/groups/{id}", a.groupsUpdate)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/groups/{id}", a.groupsDelete)
	route(mux, scopeOpen, "POST /api/homematic.cgi", a.homematicCGI)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/remote-access", a.remoteAccess)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/remote-access", a.remoteAccessPut)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/remote-access/classic-password", a.classicPasswordPut)
	a.registerLiteRPC(mux) // task 77: /api/rpc/v1
	route(mux, auth.ScopeSystemRead, "GET "+p+"/https", a.https)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/https", a.httpsPut)
	route(mux, auth.ScopeBackup, "GET "+p+"/backup", a.backup)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/backup/schedule", a.backupSchedule)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/backup/schedule", a.backupSchedulePut)
	route(mux, auth.ScopeBackup, "POST "+p+"/backup/schedule/run", a.backupScheduleRun)
	route(mux, auth.ScopePower, "POST "+p+"/restore/check", a.restoreCheck)
	route(mux, auth.ScopePower, "POST "+p+"/restore/apply", a.restoreApply)
	a.registerBackupCrypt(mux, p)   // task 91: /backup/encryption, /restore/decrypt
	a.registerBackupTargets(mux, p) // task 86: /backup/targets, /backup/run, /backup/nightly
	route(mux, auth.ScopeSystemRead, "GET "+p+"/leds", a.leds)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/leds", a.ledsPut)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/catalog", a.catalogIndex)
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/catalog/refresh", a.catalogRefresh)
	route(mux, auth.ScopeAddonsWrite, "PUT "+p+"/catalog/settings", a.catalogSettings)
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/catalog/{id}/install", a.catalogInstall)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/catalog/progress", a.catalogProgress)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/firmware", a.firmwareStatus)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/firmware/check", a.firmwareCheck)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/firmware/settings", a.firmwareSettings)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/firmware/upload", a.firmwareUpload)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/firmware/bundles/{type_code}/files/{name}", a.firmwareBundleFile)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/system-update", a.systemUpdate)
	route(mux, auth.ScopePower, "POST "+p+"/system-update/upload", a.systemUpdateUpload)
	route(mux, auth.ScopePower, "POST "+p+"/system-update/install", a.systemUpdateInstall)
	route(mux, auth.ScopePower, "DELETE "+p+"/system-update", a.systemUpdateDiscard)
	route(mux, auth.ScopePower, "POST "+p+"/system-update/check", a.systemUpdateCheck)
	route(mux, auth.ScopePower, "PUT "+p+"/system-update/settings", a.systemUpdateSettings)
	route(mux, auth.ScopePower, "POST "+p+"/system-update/download", a.systemUpdateDownload)
	route(mux, auth.ScopePower, "GET "+p+"/system-update/releases", a.systemUpdateReleases)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/hmipserver/settings", a.hmipServerSettings)
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/addons/{id}/remove-rc-entry", a.addonRemoveRCEntry)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/hmipserver/settings", a.hmipServerSettingsPut)
	a.registerWarnings(mux, p)      // task 81, warnings.go
	a.registerLED(mux, p)           // task 95, led.go
	a.registerLegacySession(mux, p) // task 125, legacysession.go
	a.registerEarlyStart(mux, p)    // task 119, earlystart.go
}

func (a *SystemAPI) status(w http.ResponseWriter, r *http.Request) {
	s := a.Root.ReadStatus()
	s.Recovered = a.MetaRecovered
	s.Occulited = a.Version
	s.OcculitedCommit = a.Commit
	// task 94: the boot's clock gate - and after its timeout, whether chrony has synchronised since
	if a.Clock != nil {
		s.Clock = a.Clock.Status(r.Context())
	}
	if a.FirstBoot == nil {
		writeJSON(w, 200, s)
		return
	}
	// the first-boot regadom import rides along on the status (D-35); one extra field
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["first_boot_import"] = a.FirstBoot
	writeJSON(w, 200, m)
}

func (a *SystemAPI) radio(w http.ResponseWriter, r *http.Request) {
	radio := a.Root.ReadRadio()
	keySet, keyKnown := a.Root.SecurityKeySet(r.Context())
	writeJSON(w, 200, map[string]any{"mode": radio.Mode, "host": radio.Host, "modules": radio.Modules, "leds": radio.LEDs, "interfaces": radio.Interfaces,
		"lan_gateways": stripKeys(a.Root.ReadLANGateways(system.GatewayRF)), "wired_gateways": stripKeys(a.Root.ReadLANGateways(system.GatewayWired)),
		"pending_key_changes": a.Root.PendingGatewayKeyChanges(), "security_key_set": keySet, "security_key_known": keyKnown})
}

// usb is task 42: the devices of /sys/bus/usb/devices with the raw-uart join, read on demand
// (the Interfaces page's collapsed section), role user like the other reads.
func (a *SystemAPI) usb(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"devices": a.Root.ReadUSB()})
}

// usbStorage is task 161: the USB sticks mounted at /media/usb1…8, by label, for the Backup page's
// directory target.
func (a *SystemAPI) usbStorage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"sticks": a.Root.USBSticks()})
}

// storage is task 69: the Status page's storage health panel - every disk, its health facts and
// the verdict - role user like the other reads. SMART and the kernel log are cached inside.
func (a *SystemAPI) storage(w http.ResponseWriter, r *http.Request) {
	if a.Storage == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no storage health service"})
		return
	}
	writeJSON(w, 200, a.Storage.Report(r.Context()))
}

// task 41: the coprocessor firmware - the list, the upload, the delete, the flash. The scope
// power throughout, the GET included: it names paths on the box and the attempt's flasher output.
func (a *SystemAPI) radioFirmwareReady(w http.ResponseWriter, r *http.Request) bool {
	if a.RadioFirmware == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no radio firmware service"})
		return false
	}
	if s := SessionFrom(r); s == nil || !s.Has(auth.ScopePower) {
		forbiddenScope(w, auth.ScopePower)
		return false
	}
	return true
}

func (a *SystemAPI) radioFirmware(w http.ResponseWriter, r *http.Request) {
	if !a.radioFirmwareReady(w, r) {
		return
	}
	writeJSON(w, 200, a.RadioFirmware.Status())
}

// radioFirmwareUpload takes ?module=<HM_*_DEV> and the file as multipart "file" (its name from
// the part) or as the raw body with ?name=.
func (a *SystemAPI) radioFirmwareUpload(w http.ResponseWriter, r *http.Request) {
	if !a.radioFirmwareReady(w, r) {
		return
	}
	module := r.URL.Query().Get("module")
	name := r.URL.Query().Get("name")
	var src io.Reader = r.Body
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart") {
		mr, err := r.MultipartReader()
		if err != nil {
			badBody(w, err)
			return
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				badBody(w, io.ErrUnexpectedEOF)
				return
			}
			if part.FormName() == "file" {
				src = part
				if name == "" {
					name = part.FileName()
				}
				break
			}
		}
	}
	f, err := a.RadioFirmware.Upload(module, name, src)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "firmware-rejected", Message: err.Error()})
		return
	}
	writeJSON(w, 200, f)
}

func (a *SystemAPI) radioFirmwareDelete(w http.ResponseWriter, r *http.Request) {
	if !a.radioFirmwareReady(w, r) {
		return
	}
	if err := a.RadioFirmware.Delete(r.PathValue("module"), r.PathValue("name")); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "firmware-rejected", Message: err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// radioFirmwareFlash starts the run detached (202); the page polls GET /radio/firmware.
func (a *SystemAPI) radioFirmwareFlash(w http.ResponseWriter, r *http.Request) {
	if !a.radioFirmwareReady(w, r) {
		return
	}
	var b struct {
		Module string `json:"module"`
		File   string `json:"file"`
	}
	if err := decodeSmall(w, r, &b); err != nil {
		badBody(w, err)
		return
	}
	if err := a.RadioFirmware.Flash(b.Module, b.File); err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, system.ErrFlashRunning) {
			status = http.StatusConflict
		}
		writeJSON(w, status, apiError{Error: "flash-refused", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, a.RadioFirmware.Status())
}

// stripKeys drops the raw sections: the encryption keys stay on the box.
func stripKeys(gws []system.LANGateway) []system.LANGateway {
	for i := range gws {
		gws[i].Raw = nil
	}
	return gws
}

func gatewayClass(s string) (system.GatewayClass, bool) {
	switch s {
	case "", "rf":
		return system.GatewayRF, true
	case "wired":
		return system.GatewayWired, true
	}
	return "", false
}

func (a *SystemAPI) lanGateways(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"rf": stripKeys(a.Root.ReadLANGateways(system.GatewayRF)), "wired": stripKeys(a.Root.ReadLANGateways(system.GatewayWired)),
		"types": system.GatewayTypes, "pending_key_changes": a.Root.PendingGatewayKeyChanges()})
}

// putLANGateways replaces the gateway list of one class (the WebUI's setConfiguration: the
// whole list, numbered afresh) and restarts the daemon when asked.
func (a *SystemAPI) putLANGateways(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Class    string                  `json:"class"`
		Gateways []system.LANGatewaySpec `json:"gateways"`
		Restart  bool                    `json:"restart"`
	}
	if err := decodeSmall(w, r, &body); err != nil {
		if bodyTooLarge(w, err) {
			return
		}
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	class, ok := gatewayClass(body.Class)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "class: rf or wired"})
		return
	}
	out, err := a.Root.WriteLANGateways(class, body.Gateways)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	resp := map[string]any{"gateways": stripKeys(out), "service": class.Service(), "restarted": false}
	if body.Restart {
		if o, err := a.Services.Control(r.Context(), class.Service(), "restart"); err != nil {
			resp["restart_error"] = err.Error() + ": " + o
		} else {
			resp["restarted"] = true
		}
	}
	writeJSON(w, 200, resp)
}

// lanGatewayKey queues an access-key change for a gateway (applied at the next boot).
func (a *SystemAPI) lanGatewayKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Class, Serial, IP, Key, CurrentKey string
	}
	var raw map[string]string
	if err := decodeSmall(w, r, &raw); err != nil {
		if bodyTooLarge(w, err) {
			return
		}
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	body.Class, body.Serial, body.IP, body.Key, body.CurrentKey = raw["class"], raw["serial"], raw["ip"], raw["key"], raw["current_key"]
	class, ok := gatewayClass(body.Class)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "class: rf or wired"})
		return
	}
	if err := a.Root.QueueGatewayKeyChange(class, body.Serial, body.IP, body.Key, body.CurrentKey); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"queued": true, "pending_key_changes": a.Root.PendingGatewayKeyChanges()})
}

// securityKey sets the system security key (Zentralenschlüssel).
func (a *SystemAPI) securityKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := decodeSmall(w, r, &body); err != nil {
		if bodyTooLarge(w, err) {
			return
		}
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	err := a.Root.SetSecurityKey(r.Context(), body.Key, a.ChangeKey)
	switch {
	case errors.Is(err, system.ErrSameKey):
		writeJSON(w, http.StatusConflict, apiError{Error: "same-key", Message: err.Error()})
	case err != nil:
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "security-key", Message: err.Error()})
	default:
		a.warn.forgetKey() // task 81: the Status page's warning reads the new state at once
		set, _ := a.Root.SecurityKeySet(r.Context())
		writeJSON(w, 200, map[string]any{"security_key_set": set})
	}
}

func (a *SystemAPI) services(w http.ResponseWriter, _ *http.Request) {
	start := time.Now()
	list, err := a.Services.List()
	if err != nil {
		writeErr(w, err)
		return
	}
	pollSeconds := a.servicesPoll.observe(time.Since(start))
	// D-36: the page shows each addon unit's confinement next to its state, so "runs as root"
	// and "nobody declared what this addon needs" are visible where the switch is
	if sa := a.policyManager(); sa != nil {
		list = sa.OverlayServicePolicy(list)
	}
	list = a.RadioConnections.OverlayServiceNotes(list)
	// openccu-lite task 100: an addon unit's row shows the addon's icon
	for i := range list {
		if id, ok := strings.CutPrefix(list[i].ID, "addon-"); ok && list[i].Kind == "addon" {
			list[i].Images = addonImageURLs(a.Root, id, "")
		}
	}
	writeJSON(w, 200, map[string]any{"services": list, "systemd": a.Timers != nil, "poll_seconds": pollSeconds})
}

func (a *SystemAPI) control(w http.ResponseWriter, r *http.Request) {
	// openccu-lite task 243: the units the web interface runs through are restarted, never stopped
	// or disabled from it - that would take the page away until a reboot or ssh
	if act := r.PathValue("action"); (act == "stop" || act == "disable") && system.CutsUI(r.PathValue("id")) {
		writeJSON(w, http.StatusConflict, apiError{Error: "cuts-ui", Message: r.PathValue("id") + " carries the web interface: stopping or disabling it here would take this page away. Restart it, or stop it over ssh."})
		return
	}
	if !a.waitRadio(w, r, r.PathValue("id"), r.PathValue("action")) {
		return
	}
	out, err := a.Services.Control(r.Context(), r.PathValue("id"), r.PathValue("action"))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "service-control", "message": err.Error(), "output": out})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "output": out})
}

// waitRadio holds a start or restart of an addon's unit while a radio connection change or a
// coprocessor flash runs (openccu-lite B-307): the radio daemons are stopped on purpose then, and
// the addon would come up without its interfaces. False when the request ended while it waited
// (answered here).
func (a *SystemAPI) waitRadio(w http.ResponseWriter, r *http.Request, unit, action string) bool {
	if a.RadioBusy == nil || !system.AddonStartAction(unit, action) {
		return true
	}
	waited, err := system.WaitRadioIdle(r.Context(), a.RadioBusy)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "radio-busy", Message: "a radio connection change or a coprocessor flash is running; the " + action + " of " + unit + " was not done: " + err.Error()})
		return false
	}
	if waited > 0 {
		reqLog(r).Info("addon start waited for the radio change", "unit", unit, "action", action, "waited", waited.Round(time.Millisecond))
	}
	return true
}

// unitEditor is what a service manager offers when a unit can be edited (task 27.4): systemd,
// through a runtime drop-in. NoInit does not, and answers 501 below.
type unitEditor interface {
	ReadUnitOverride(ctx context.Context, id string) (system.UnitOverride, error)
	SetUnitOverride(ctx context.Context, id, override string) (system.UnitOverride, error)
}

func (a *SystemAPI) unitOverride(w http.ResponseWriter, r *http.Request) {
	ed, ok := a.Services.(unitEditor)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-systemd", Message: "unit overrides need systemd"})
		return
	}
	uo, err := ed.ReadUnitOverride(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	writeJSON(w, 200, uo)
}

func (a *SystemAPI) unitOverridePut(w http.ResponseWriter, r *http.Request) {
	ed, ok := a.Services.(unitEditor)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-systemd", Message: "unit overrides need systemd"})
		return
	}
	var body struct {
		Override string `json:"override"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	uo, err := ed.SetUnitOverride(r.Context(), r.PathValue("id"), body.Override)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	writeJSON(w, 200, uo)
}

func (a *SystemAPI) addons(w http.ResponseWriter, r *http.Request) {
	list, err := a.Addons.ListAddons(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	for i := range list {
		// B-134: the manifest may name the settings page where the Config-Url is not it
		if u := SettingsURL(a.Root, list[i].ID); u != "" {
			list[i].ConfigURL = u
		}
		path := addonSettingsURL(list[i])
		list[i].SessionHeader = SessionHeader(a.Root, list[i].ID, list[i].Version, path) // task 88, D-67
		list[i].LegacySession = a.LegacySession(list[i].ID, list[i].Version, path)       // task 125
		list[i].Images = addonImageURLs(a.Root, list[i].ID, list[i].Version)             // openccu-lite task 100
		list[i].Fullscreen = Fullscreen(a.Root, list[i].ID)                              // occulited task 24
	}
	a.MarkPayloadMissing(list) // openccu-lite task 146
	writeJSON(w, 200, map[string]any{"addons": list})
}

// MarkPayloadMissing sets PayloadMissing, PayloadMissingDirs and ReinstallDismissed from the
// record (openccu-lite task 146): an addon a restore brought back without its .nobackup
// directories. The call also refreshes the record for the addons that have theirs.
func (a *SystemAPI) MarkPayloadMissing(list []system.Addon) {
	if a.AddonPayload == nil {
		return
	}
	ids := make([]string, len(list))
	versions := make(map[string]string, len(list))
	skipped := map[string]bool{} // B-267: the unit's program check said no
	for i, ad := range list {
		ids[i], versions[ad.ID] = ad.ID, ad.Version
		if ad.Skipped {
			skipped[ad.ID] = true
		}
	}
	st := a.AddonPayload.Check(ids, versions, skipped)
	for i := range list {
		if s := st[list[i].ID]; s.Missing {
			list[i].PayloadMissing, list[i].PayloadMissingDirs, list[i].ReinstallDismissed = true, s.Dirs, s.Dismissed
		}
	}
}

// reinstallDismiss (openccu-lite task 146) hides the reinstall hint for one addon at its current
// version; a reinstall or a new version shows it again where it still applies.
func (a *SystemAPI) reinstallDismiss(w http.ResponseWriter, r *http.Request) {
	if a.AddonPayload == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no payload record on this system"})
		return
	}
	id := r.PathValue("id")
	list, err := a.Addons.ListAddons(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	var found *system.Addon
	for i := range list {
		if list[i].ID == id {
			found = &list[i]
		}
	}
	if found == nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: "no such addon"})
		return
	}
	a.MarkPayloadMissing(list)
	if !found.PayloadMissing {
		writeJSON(w, http.StatusConflict, apiError{Error: "not-missing", Message: "the addon's program files are there; nothing to dismiss"})
		return
	}
	if err := a.AddonPayload.Dismiss(id, found.Version); err != nil {
		writeErr(w, err)
		return
	}
	reqLog(r).Info("addons: reinstall hint dismissed", "addon", id, "version", found.Version)
	writeJSON(w, 200, map[string]any{"ok": true, "id": id, "version": found.Version})
}

// logPageDefault is /log's page when the query names no limit.
const logPageDefault = 500

// logCursorRe is what a page's cursor may look like: a journal cursor (s=…;i=…;b=…;m=…;t=…;x=…)
// or a dmesg one (<monotonic_us>/<k>). It goes to journalctl as an argument, never to a shell;
// the check keeps the answer's error small when it is not one.
var logCursorRe = regexp.MustCompile(`^[A-Za-z0-9=;/]{1,255}$`)

// logPage reads the page a /log query asks for (task 178): before or after a cursor, or the head
// of the boot or the range - one at most - and its size.
func logPage(v url.Values, q *system.LogQuery) error {
	q.Before, q.After = v.Get("before"), v.Get("after")
	q.Head = v.Get("head") == "1" || v.Get("head") == "true"
	n := 0
	for _, set := range []bool{q.Before != "", q.After != "", q.Head} {
		if set {
			n++
		}
	}
	if n > 1 {
		return errors.New("before, after and head exclude each other")
	}
	for _, c := range []string{q.Before, q.After} {
		if c != "" && !logCursorRe.MatchString(c) {
			return errors.New("before and after are a line's cursor")
		}
	}
	return nil
}

func (a *SystemAPI) log(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	limit, _ := strconv.Atoi(v.Get("limit"))
	if limit <= 0 {
		limit = logPageDefault
	}
	// one more than the page, so the answer can say whether the log goes on past its far end
	q, err := logQueryChecked(v, limit+1)
	if err == nil {
		err = logPage(v, &q)
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	a.resolveBoot(r.Context(), &q) // B-114: an earlier boot by its id
	reader, source := a.logReader(q)
	lines, err := reader.Read(q)
	if err != nil {
		writeJSON(w, 200, map[string]any{"lines": []system.LogLine{}, "error": err.Error(), "source": source})
		return
	}
	if fallback, src, ok := a.kernelFallback(q, lines); ok {
		lines, source = fallback, src
	}
	// task 178: the pages. forward reads (the head, after a cursor) may run past the page at the
	// new end, the others at the old end; the extra entry is cut and the flags say what is there
	forward := q.Head || q.After != ""
	more := len(lines) > limit
	if more {
		if forward {
			lines = lines[:limit]
		} else {
			lines = lines[len(lines)-limit:]
		}
	}
	older, newer := more, false
	if forward {
		older, newer = q.After != "", more
	} else if q.Before != "" {
		newer = true
	}
	answer := map[string]any{"lines": lines, "source": source, "older": older, "newer": newer}
	// B-223: the journal's copies on a share the system cannot read (a root-squashed NFS export) -
	// the lines above are the RAM journal alone, and the page says why
	if source == "journald" && !q.Follow {
		if dir, err := a.Root.JournalCopiesUnreadable(); err != nil {
			answer["copies_unreadable"] = map[string]any{"path": dir, "error": err.Error(), "hint": shares.UnreadableHint}
		}
	}
	// task 102: a run the log holds no line of - in a RAM journal a run from before the last boot
	// is gone; the page says so for a run that has finished
	if q.Run != "" && len(lines) == 0 {
		answer["run_missing"] = true
	}
	writeJSON(w, 200, answer)
}

func (a *SystemAPI) uninstall(w http.ResponseWriter, r *http.Request) {
	if a.Manager == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no addon manager"})
		return
	}
	out, err := a.Manager.Uninstall(r.Context(), r.PathValue("id"))
	// openccu-lite B-297: the open shells read their menus again - a failed uninstall may have
	// removed part of the addon too
	a.addonsChanged()
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "uninstall-failed", "message": err.Error(), "output": out.Output, "system_removed": out.SystemRemoved})
		return
	}
	if a.Updates != nil {
		// an update of an addon that is gone is not waiting any more
		a.Updates.Forget(r.PathValue("id"))
	}
	// B-283: the script's output as it was, and what the system removed after it - a confined
	// script's refused rm lines are expected, and the page says the system did the rest
	writeJSON(w, 200, map[string]any{"ok": true, "output": out.Output, "system_removed": out.SystemRemoved})
}

func (a *SystemAPI) update(w http.ResponseWriter, r *http.Request) {
	if a.Manager == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no addon manager"})
		return
	}
	list, err := a.Addons.ListAddons(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, ad := range list {
		if ad.ID == r.PathValue("id") {
			base := a.WebBase
			if base == "" {
				base = "http://127.0.0.1"
			}
			writeJSON(w, 200, a.Manager.CheckUpdate(r.Context(), ad, base))
			return
		}
	}
	writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-addon", Message: "no such addon"})
}

// powerConfirmed is the gate of the three power routes (task 60): the scope power, and
// {"confirm": true} in the body - a reboot or a halt is never what a stray POST causes. The
// answer is written when it refuses.
func powerConfirmed(w http.ResponseWriter, r *http.Request) bool {
	if s := SessionFrom(r); s == nil || !s.Has(auth.ScopePower) {
		forbiddenScope(w, auth.ScopePower)
		return false
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		badBody(w, err)
		return false
	}
	if !body.Confirm {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "confirm", Message: "confirm: true is required"})
		return false
	}
	return true
}

// reboot restarts the box; the reboot itself starts a moment after the answer.
func (a *SystemAPI) reboot(w http.ResponseWriter, r *http.Request) {
	if !powerConfirmed(w, r) {
		return
	}
	if a.Manager == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no addon manager"})
		return
	}
	// a reboot with an update set to install is the install
	kind := bootexpect.KindReboot
	if u := a.Root.StagedSystemUpdate(); u != nil && u.RecoveryArmed {
		kind = bootexpect.KindUpdate
	}
	a.markBoot(kind)
	if err := a.Manager.Reboot(context.Background()); err != nil {
		a.unmarkBoot()
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "rebooting"})
}

// markBoot writes the calibration's marker right before a reboot starts. A marker that cannot be
// written costs one measurement, never the reboot.
func (a *SystemAPI) markBoot(kind string) {
	if a.BootTiming == nil {
		return
	}
	if err := a.BootTiming.Mark(kind); err != nil {
		slog.Warn("boot timing: marker not written", "kind", kind, "err", err)
	}
}

// unmarkBoot removes it again when the reboot did not start.
func (a *SystemAPI) unmarkBoot() {
	if a.BootTiming != nil {
		a.BootTiming.Unmark()
	}
}

// bootExpect answers how long a reboot of a kind is expected to take, phase by phase: the product
// default, overridden by the median of this box's own recorded reboots. The page asks right before
// it writes its countdown and sends the reboot.
func (a *SystemAPI) bootExpect(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = bootexpect.KindReboot
	}
	if !bootexpect.ValidKind(kind) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "kind must be reboot, recovery, update, restore or halt"})
		return
	}
	if a.BootTiming != nil {
		writeJSON(w, 200, a.BootTiming.Expect(kind))
		return
	}
	writeJSON(w, 200, bootexpect.DefaultAnswer(a.Root.ReadVersion().Platform, kind))
}

// bootTiming takes the checkpoints the page observed during a reboot (epoch milliseconds of the
// browser) and adds them to the record of that reboot.
func (a *SystemAPI) bootTiming(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
		bootexpect.Browser
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if !bootexpect.ValidKind(body.Kind) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "kind must be reboot, recovery, update, restore or halt"})
		return
	}
	if a.BootTiming == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no boot timing on this system"})
		return
	}
	where, err := a.BootTiming.Attach(body.Kind, body.Browser)
	switch {
	case errors.Is(err, bootexpect.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
	case errors.Is(err, bootexpect.ErrNoRecord):
		writeJSON(w, http.StatusConflict, apiError{Error: "no-record", Message: err.Error()})
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, 200, map[string]any{"ok": true, "attached": where})
	}
}

// rebootRecovery boots into the recovery system without an update: the marker a staged update's
// install sets, then the reboot. A container has no recovery system (501), and a staged update
// would be installed by it (409) - that is the Status page's own button, never a surprise.
func (a *SystemAPI) rebootRecovery(w http.ResponseWriter, r *http.Request) {
	if !powerConfirmed(w, r) {
		return
	}
	if kind := a.Root.Container(); kind != "" {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-available", Message: "there is no recovery system: this system is a " + kind + " container"})
		return
	}
	if a.Manager == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no addon manager"})
		return
	}
	if err := a.Root.ArmRecovery(); err != nil {
		switch {
		case errors.Is(err, system.ErrUpdateStaged):
			detail := map[string]any{}
			if u := a.Root.StagedSystemUpdate(); u != nil {
				detail["file"] = u.File
			}
			writeJSON(w, http.StatusConflict, apiError{Error: "update-staged", Message: err.Error(), Detail: detail})
		case errors.Is(err, system.ErrNoRecovery):
			writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-available", Message: err.Error()})
		default:
			writeErr(w, err)
		}
		return
	}
	a.markBoot(bootexpect.KindRecovery)
	if err := a.Manager.Reboot(context.Background()); err != nil {
		// no reboot, no recovery at some later boot either
		_ = a.Root.DisarmRecovery()
		a.unmarkBoot()
		writeErr(w, err)
		return
	}
	slog.Warn("power: rebooting into the recovery system")
	writeJSON(w, 200, map[string]any{"ok": true, "rebooting": true, "recovery": true})
}

// halt powers the box off: systemctl poweroff, or busybox's poweroff, a moment after the answer.
// A container is stopped; nothing starts it but its host.
func (a *SystemAPI) halt(w http.ResponseWriter, r *http.Request) {
	if !powerConfirmed(w, r) {
		return
	}
	if a.Power == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no power control on this system"})
		return
	}
	if err := a.Power.Halt(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "halting": true})
}

func (a *SystemAPI) nav(w http.ResponseWriter, r *http.Request) {
	if a.Nav == nil {
		writeJSON(w, 200, map[string]any{"entries": []system.NavEntry{}})
		return
	}
	entries := a.Nav.NavEntries(r.Context())
	for i := range entries {
		e := &entries[i]
		e.SessionHeader = e.Addon != "" && SessionHeader(a.Root, e.Addon, e.AddonVersion, e.Href) // task 88, D-67
		// task 125: an addon's frontend by the addon's switch; a nav.d page that is no addon by
		// the global one alone - a same-origin page under /addons/ may use the alias. Never a page
		// the addon's own server answers behind lighttpd's proxy (B-133): it has the gate's header
		// and the API refuses the alias, so ?sid=@alias@ can only make it refuse the page.
		e.LegacySession = !e.Proxied && a.LegacySession(e.Addon, e.AddonVersion, e.Href)
	}
	writeJSON(w, 200, map[string]any{"entries": entries})
}

// hostManaged answers 501 for what the host owns when this box is a container (task 34): the
// network (the veth is the container manager's), the clock (settimeofday is EPERM in an
// unprivileged container and there is no time daemon) and the rootfs (a template swapped on
// the host, not a file staged for a recovery system). True when the answer was written.
func (a *SystemAPI) hostManaged(w http.ResponseWriter, what string) bool {
	kind := a.Root.Container()
	if kind == "" {
		return false
	}
	writeJSON(w, http.StatusNotImplemented, apiError{Error: "host-managed", Message: what + " is managed by the host: this system is a " + kind + " container"})
	return true
}

// followDomain hands the box's host name and domain to the certificate service: ACME names that
// are the default of the domain it saw last follow a new one (acme.Service.FollowDomain).
func (a *SystemAPI) followDomain(hostname, domain string) {
	if a.Cert == nil {
		return
	}
	if _, err := a.Cert.FollowDomain(hostname, domain); err != nil {
		slog.Warn("certificate: the ACME names could not follow the domain", "err", err)
	}
}

func (a *SystemAPI) network(w http.ResponseWriter, _ *http.Request) {
	n := a.Root.ReadNetwork()
	// the Network page polls this route: a domain another DHCP lease brought is seen here
	a.followDomain(n.Hostname, n.Domain)
	a.followFQDN(n.Hostname, n.Domain)
	// on a container the page is a display: the address, the gateway and the DNS servers are
	// the container manager's to set, and a write to /etc/config/netconfig would be applied
	// by nobody (task 34)
	hm := a.Root.HostManaged()
	out := map[string]any{"network": n, "settings": n.Settings(), "current": n.Current(), "writable": a.NetTx != nil && !hm, "host_managed": hm}
	if a.NetTx != nil {
		// a DHCP setup's DNS override is the NetTx's (B-167), not netconfig's
		out["settings"], out["current"] = a.NetTx.Settings(n)
		out["pending"] = a.NetTx.Pending()
	}
	writeJSON(w, 200, out)
}

// networkBegin applies the submitted settings live and opens the confirmation window. The
// answer carries the token the browser must send back - from the new address, if it changed.
func (a *SystemAPI) networkBegin(w http.ResponseWriter, r *http.Request) {
	if a.hostManaged(w, "the network") {
		return
	}
	if a.NetTx == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "network changes are not enabled"})
		return
	}
	var s system.NetworkSettings
	if err := readJSON(r, &s); err != nil {
		badBody(w, err)
		return
	}
	if s.DNS == nil {
		s.DNS = []string{}
	}
	oldHost := a.Root.Hostname()
	p, ren, err := a.NetTx.Begin(r.Context(), s)
	if err != nil {
		switch {
		case errors.Is(err, system.ErrTxPending):
			writeJSON(w, http.StatusConflict, apiError{Error: "pending", Message: err.Error()})
		default:
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
		}
		return
	}
	if p == nil {
		// a hostname-only change is saved at once: the ACME names follow it here, and the answer
		// says what became of them
		out := map[string]any{"ok": true, "applied": true, "pending": nil}
		if s.Hostname != oldHost {
			out["acme_names"] = a.followHostname(oldHost, s.Hostname)
			if fr := a.followFQDNRename(s.Hostname); fr != nil {
				out["fqdn_redirect"] = fr
			}
			// openccu-lite task 62: what the rename did about the lease, and whether the live
			// certificate still names the old host
			if ren != nil {
				out["rename"] = ren
			}
			out["certificate"] = a.certificateNames(s.Hostname)
		}
		writeJSON(w, 200, out)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "applied": false, "pending": p})
}

// followHostname applies a rename to the ACME names (acme.Service.FollowHostname) with the domain
// the box has now. Without a certificate service there are no ACME names to adapt.
func (a *SystemAPI) followHostname(oldHost, newHost string) acme.NamesChange {
	if a.Cert == nil {
		return acme.NamesChange{State: acme.NamesNotInUse, Names: []string{}, Previous: []string{}}
	}
	// a domain change not yet seen is applied first, with the old host name, so the rename compares
	// with the current domain's default
	domain := a.Root.Domain()
	a.followDomain(oldHost, domain)
	ch, err := a.Cert.FollowHostname(oldHost, newHost, domain)
	if err != nil {
		slog.Warn("certificate: the ACME names could not follow the host name", "err", err)
	}
	return ch
}

func (a *SystemAPI) networkPending(w http.ResponseWriter, _ *http.Request) {
	if a.NetTx == nil {
		writeJSON(w, 200, map[string]any{"pending": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"pending": a.NetTx.Pending()})
}

func (a *SystemAPI) networkConfirm(w http.ResponseWriter, r *http.Request) {
	a.networkFinish(w, r, true)
}
func (a *SystemAPI) networkRevert(w http.ResponseWriter, r *http.Request) {
	a.networkFinish(w, r, false)
}

func (a *SystemAPI) networkFinish(w http.ResponseWriter, r *http.Request, confirm bool) {
	if a.hostManaged(w, "the network") {
		return
	}
	if a.NetTx == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "network changes are not enabled"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	// a confirmed change that renames the box renames it now: the ACME names follow as they do
	// for a hostname-only change
	var renamed *system.NetPending
	if p := a.NetTx.Pending(); confirm && p != nil && p.Token == body.Token && p.Settings.Hostname != p.Previous.Hostname {
		renamed = p
	}
	var err error
	if confirm {
		err = a.NetTx.Confirm(r.Context(), body.Token)
	} else {
		err = a.NetTx.Revert(r.Context(), body.Token)
	}
	if errors.Is(err, system.ErrNoTx) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: err.Error()})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]any{"ok": true, "confirmed": confirm}
	if renamed != nil {
		out["acme_names"] = a.followHostname(renamed.Previous.Hostname, renamed.Settings.Hostname)
		if fr := a.followFQDNRename(renamed.Settings.Hostname); fr != nil {
			out["fqdn_redirect"] = fr
		}
	}
	writeJSON(w, 200, out)
}

func (a *SystemAPI) firewallManager() *system.FirewallManager {
	if a.Firewall != nil {
		return a.Firewall
	}
	return &system.FirewallManager{Root: a.Root, Declared: a.Root.DeclaredRuntime}
}

func (a *SystemAPI) firewallReady(w http.ResponseWriter) bool {
	if a.FirewallRules == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no firewall service"})
		return false
	}
	return true
}

func firewallError(w http.ResponseWriter, err error) {
	if errors.Is(err, system.ErrFirewallPending) {
		writeJSON(w, http.StatusConflict, apiError{Error: "pending", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
}

// firewall is GET /firewall: the confirmed rules, the draft, the open window, the frame and what
// "local networks" stands for now.
func (a *SystemAPI) firewall(w http.ResponseWriter, _ *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	writeJSON(w, 200, a.FirewallRules.View())
}

// firewallPut is PUT /firewall {policy, rules}: the draft. Nothing is loaded until Apply.
func (a *SystemAPI) firewallPut(w http.ResponseWriter, r *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	var body struct {
		Policy firewall.Policy `json:"policy"`
		Rules  []firewall.Rule `json:"rules"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if _, err := a.FirewallRules.SetDraft(body.Policy, body.Rules); err != nil {
		firewallError(w, err)
		return
	}
	writeJSON(w, 200, a.FirewallRules.View())
}

func (a *SystemAPI) firewallDiscard(w http.ResponseWriter, _ *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	if err := a.FirewallRules.DiscardDraft(); err != nil {
		firewallError(w, err)
		return
	}
	writeJSON(w, 200, a.FirewallRules.View())
}

// firewallApply loads the draft with the confirm window; the page confirms over the new rules.
func (a *SystemAPI) firewallApply(w http.ResponseWriter, r *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	if _, err := a.FirewallRules.Apply(r.Context()); err != nil {
		firewallError(w, err)
		return
	}
	a.warn.forgetFirewall()
	writeJSON(w, 200, a.FirewallRules.View())
}

func (a *SystemAPI) firewallConfirm(w http.ResponseWriter, r *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	if err := a.FirewallRules.Confirm(r.Context()); err != nil {
		firewallError(w, err)
		return
	}
	a.warn.forgetFirewall()
	writeJSON(w, 200, a.FirewallRules.View())
}

func (a *SystemAPI) firewallRevert(w http.ResponseWriter, r *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	if err := a.FirewallRules.Revert(r.Context()); err != nil {
		firewallError(w, err)
		return
	}
	a.warn.forgetFirewall()
	writeJSON(w, 200, a.FirewallRules.View())
}

// firewallCounters is each rule's packets and bytes since the last load (task 167).
func (a *SystemAPI) firewallCounters(w http.ResponseWriter, r *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	c, err := a.FirewallRules.Counters(r.Context())
	if err != nil {
		firewallError(w, err)
		return
	}
	writeJSON(w, 200, c)
}

// firewallCountersReset starts the counters at zero: the rules are loaded again.
func (a *SystemAPI) firewallCountersReset(w http.ResponseWriter, r *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	if err := a.FirewallRules.ResetCounters(r.Context()); err != nil {
		firewallError(w, err)
		return
	}
	c, err := a.FirewallRules.Counters(r.Context())
	if err != nil {
		firewallError(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (a *SystemAPI) firewallDismiss(w http.ResponseWriter, _ *http.Request) {
	if !a.firewallReady(w) {
		return
	}
	if err := a.FirewallRules.DismissMigration(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, a.FirewallRules.View())
}

// firewallListeners is every listening socket with its process and, per family, the rule that
// decides its packets or the policy. The owners come through the helper; without it the processes
// are left out.
func (a *SystemAPI) firewallListeners(w http.ResponseWriter, r *http.Request) {
	c, _ := a.Root.ReadRules()
	if c.Policy.V4 == "" {
		c = firewall.Default()
	}
	owners, _ := system.Priv.SocketOwners(r.Context())
	l := a.Root.ListeningPorts(c, owners)
	if l == nil {
		l = []system.Listener{}
	}
	writeJSON(w, 200, map[string]any{"listeners": l, "policy": c.Policy})
}

// firewallAddons is the installed addons' declared ports and their switches (the Addons page).
func (a *SystemAPI) firewallAddons(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.addonPorts(r.Context()))
}

func (a *SystemAPI) addonPorts(ctx context.Context) []system.FirewallAddon {
	c, _ := a.Root.ReadRules()
	// occulited B-31: whose socket it is decides whether the addon listens - nil when the helper
	// does not answer, and the ports then say the owner is unknown
	owners, _ := system.Priv.SocketOwners(ctx)
	l := a.Root.ListeningPorts(c, owners)
	names := map[string]string{}
	if a.Addons != nil {
		if list, err := a.Addons.ListAddons(ctx); err == nil {
			for _, ad := range list {
				names[ad.ID] = ad.Name
			}
		}
	}
	return a.firewallManager().Addons(names, l)
}

// firewallAddonPorts is PUT /firewall/addons/{id}/ports {"open": [8883]} (D-47): which of the
// addon's declared ports are open. Validated against the declaration, stored in the policy, and the
// firewall follows with an owned rule per open port; answers the addons' list.
func (a *SystemAPI) firewallAddonPorts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Open []int `json:"open"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if err := a.firewallManager().SetAddonPorts(r.Context(), r.PathValue("id"), body.Open); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	a.warn.forgetFirewall()
	writeJSON(w, 200, a.addonPorts(r.Context()))
}

func (a *SystemAPI) timeConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, a.Root.ReadTime())
}

func (a *SystemAPI) timeZones(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"zones": a.Root.Zones()})
}

// timePut changes the zone and/or the NTP servers; a missing field is left alone.
func (a *SystemAPI) timePut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Zone       *string  `json:"zone"`
		NTPServers []string `json:"ntp_servers"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if body.Zone != nil {
		if err := a.Root.SetTimezone(r.Context(), a.Run, *body.Zone); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
			return
		}
	}
	if body.NTPServers != nil {
		if a.hostManaged(w, "the clock") {
			return
		}
		if err := a.Root.SetNTPServers(r.Context(), a.Run, a.Services, body.NTPServers); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
			return
		}
	}
	writeJSON(w, 200, a.Root.ReadTime())
}

func (a *SystemAPI) timeClock(w http.ResponseWriter, r *http.Request) {
	if a.hostManaged(w, "the clock") {
		return
	}
	var body struct {
		Time time.Time `json:"time"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if err := a.Root.SetClock(r.Context(), a.Run, body.Time); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	// openccu-lite task 299: a time set by hand is a trusted clock; a hmipserver held back for one
	// (its unit in its restart backoff) starts now
	if radio.ReadCounterHold(string(a.Root)) != nil && a.Run != nil {
		if out, err := a.Run(r.Context(), "systemctl", "restart", "hmipserver.service"); err != nil {
			withCaller(r, slog.Default()).Warn("clock set by hand: hmipserver not restarted", "err", err, "out", strings.TrimSpace(string(out)))
		} else {
			withCaller(r, slog.Default()).Info("clock set by hand: hmipserver, held back for a trusted clock, restarted")
		}
	}
	writeJSON(w, 200, a.Root.ReadTime())
}

func (a *SystemAPI) leds(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, a.Root.ReadLEDs())
}

func (a *SystemAPI) ledsPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if err := a.Root.SetLEDsDisabled(body.Disabled); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, a.Root.ReadLEDs())
}

func (a *SystemAPI) firmwareStatus(w http.ResponseWriter, _ *http.Request) {
	if a.Firmware == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no firmware service"})
		return
	}
	writeJSON(w, 200, a.Firmware.Status())
}

func (a *SystemAPI) firmwareCheck(w http.ResponseWriter, _ *http.Request) {
	if a.Firmware == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no firmware service"})
		return
	}
	a.Firmware.Trigger()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "check started"})
}

func (a *SystemAPI) firmwareSettings(w http.ResponseWriter, r *http.Request) {
	if a.Firmware == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no firmware service"})
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	a.Firmware.SetEnabled(b.Enabled)
	if a.OnFirmwareToggle != nil {
		if err := a.OnFirmwareToggle(b.Enabled); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": b.Enabled})
}

func (a *SystemAPI) firmwareUpload(w http.ResponseWriter, r *http.Request) {
	if a.Firmware == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no firmware service"})
		return
	}
	var src io.Reader = r.Body
	if ct := r.Header.Get("Content-Type"); len(ct) >= 9 && ct[:9] == "multipart" {
		mr, err := r.MultipartReader()
		if err != nil {
			badBody(w, err)
			return
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				badBody(w, io.ErrUnexpectedEOF)
				return
			}
			if part.FormName() == "file" {
				src = part
				break
			}
		}
	}
	// B-255/B-256: stage on the userfs, not /tmp (a tmpfs), and refuse a bundle over the cap with
	// 413 instead of truncating it silently and failing later as "not a gzip".
	path, _, serr := system.StageUpload(a.Root, "firmware-upload", src, system.MaxFirmwareUpload)
	if errors.Is(serr, system.ErrUploadTooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: "too-large", Message: fmt.Sprintf("the firmware bundle is larger than %d MiB", int64(system.MaxFirmwareUpload)>>20)})
		return
	} else if serr != nil {
		writeErr(w, serr)
		return
	}
	defer os.Remove(path)
	b, err := a.Firmware.DeployUpload(filepath.Clean(path))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "bundle-rejected", Message: err.Error()})
		return
	}
	writeJSON(w, 200, b)
}

// firmwareBundleFile hands out one text file of a deployed bundle - its changelog, its info - for
// the Firmware page's viewer: a GET like the page itself, so any session may read it. The name
// has to be an entry of that bundle's own listing; a firmware image, a file over the cap and
// anything that is not a plain regular file there are refused.
func (a *SystemAPI) firmwareBundleFile(w http.ResponseWriter, r *http.Request) {
	if a.Firmware == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no firmware service"})
		return
	}
	b, err := a.Firmware.BundleFile(r.PathValue("type_code"), r.PathValue("name"))
	var tooLarge *firmware.TooLargeError
	switch {
	case errors.As(err, &tooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: "too-large", Message: err.Error(), Detail: map[string]any{"size": tooLarge.Size, "limit": tooLarge.Limit}})
	case errors.Is(err, firmware.ErrNotViewable):
		writeJSON(w, http.StatusUnsupportedMediaType, apiError{Error: "not-viewable", Message: err.Error()})
	case errors.Is(err, firmware.ErrNoSuchFile):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-found", Message: err.Error()})
	case err != nil:
		writeErr(w, err)
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}
}

func (a *SystemAPI) catalogIndex(w http.ResponseWriter, r *http.Request) {
	if a.Catalog == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the catalogue is switched off"})
		return
	}
	// ?refresh=1 is the user's check (D-90): the catalogue files again, every manifest at its latest
	// release tag, the star counts and the latest releases. Without it the page answers from what
	// the system holds - the last check's copy of the catalogue files, the bundled copy, the cached
	// manifests and releases - and nothing leaves the system: Fetch without force goes nowhere
	// (B-240; catalog.TestNothingGoesOutWithoutTheUsersCheck pins it), whatever *Check daily* says.
	if r.URL.Query().Get("refresh") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		err := a.Catalog.Refresh(ctx)
		cancel()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiError{Error: "catalog-unreachable", Message: err.Error()})
			return
		}
	}
	view, err := a.Catalog.Fetch(r.Context(), false)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "catalog-unreachable", Message: err.Error()})
		return
	}
	installed := map[string]string{}
	if list, err := a.Addons.ListAddons(r.Context()); err == nil {
		for _, ad := range list {
			installed[ad.ID] = ad.Version
		}
	}
	// the view is the caller's copy; the update flag goes on it
	for i := range view.Addons {
		it := &view.Addons[i]
		if it.Manifest != nil && it.Latest != nil {
			it.UpdateAvailable = catalog.UpdateAvailable(installed[it.ID], it.Latest.Version)
		}
		it.ReleaseNotes = it.NotesURL() // task 26: beside the offered update
		if it.Manifest != nil {
			it.Images = catalogImageURLs(it.ID, it.ImageHashes) // openccu-lite task 100
		}
	}
	writeJSON(w, 200, map[string]any{"catalog": view, "installed": installed, "arch": ArchName(), "daily": a.CatalogDaily == nil || a.CatalogDaily()})
}

// catalogSettings switches the daily check of the catalogue's releases and the installed addons'
// updates (task 244: *Check daily* beside *Check for updates*); the button's check runs either way.
func (a *SystemAPI) catalogSettings(w http.ResponseWriter, r *http.Request) {
	if a.OnCatalogDaily == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the daily check cannot be switched here"})
		return
	}
	var b struct {
		Daily bool `json:"daily"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if err := a.OnCatalogDaily(b.Daily); err != nil {
		writeErr(w, err)
		return
	}
	reqLog(r).Info("catalog: daily check switched", "on", b.Daily)
	writeJSON(w, 200, map[string]any{"ok": true, "daily": b.Daily})
}

// systemUpdateSettings switches the release feed's daily check (task 244: *Check daily* beside
// *Check now*); the button's check runs either way.
func (a *SystemAPI) systemUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if a.Feed == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no release feed configured"})
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	a.Feed.SetEnabled(b.Enabled)
	if a.OnSystemUpdateToggle != nil {
		if err := a.OnSystemUpdateToggle(b.Enabled); err != nil {
			writeErr(w, err)
			return
		}
	}
	reqLog(r).Info("system update: daily check switched", "on", b.Enabled)
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": b.Enabled})
}

// addonRemoveRCEntry is "remove the rc.d entry" (occulited task 28): for a script that came along
// from the CCU and is no addon. {"target": true} also removes the file its link led to outside
// /usr/local/addons/ (the page names it; removed only when the user ticks it).
func (a *SystemAPI) addonRemoveRCEntry(w http.ResponseWriter, r *http.Request) {
	sd, ok := a.Manager.(*system.SystemdAddons)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "only on the systemd products"})
		return
	}
	var b struct {
		Target bool `json:"target"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &b); err != nil {
			badBody(w, err)
			return
		}
	}
	res, err := sd.RemoveRCEntry(r.Context(), r.PathValue("id"), b.Target)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid", "message": err.Error(), "removed": res.Removed})
		return
	}
	a.addonsChanged()
	writeJSON(w, 200, map[string]any{"ok": true, "removed": res.Removed, "target": res.Target})
}

// hmipServerSettings is hmipserver.diagrams (task 33): whether hmipserver's diagram data is carried
// between its tmpfs and the stick. No page shows it yet; it takes effect at hmipserver's next start.
func (a *SystemAPI) hmipServerSettings(w http.ResponseWriter, r *http.Request) {
	if a.HmIPServerDiagrams == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no hmipserver settings here"})
		return
	}
	writeJSON(w, 200, map[string]any{"diagrams": a.HmIPServerDiagrams()})
}

func (a *SystemAPI) hmipServerSettingsPut(w http.ResponseWriter, r *http.Request) {
	if a.OnHmIPServerDiagrams == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no hmipserver settings here"})
		return
	}
	var b struct {
		Diagrams *bool `json:"diagrams"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if b.Diagrams == nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "diagrams: true or false"})
		return
	}
	if err := a.OnHmIPServerDiagrams(*b.Diagrams); err != nil {
		writeErr(w, err)
		return
	}
	reqLog(r).Info("hmipserver: diagram data switched; it applies at hmipserver's next start", "on", *b.Diagrams)
	writeJSON(w, 200, map[string]any{"ok": true, "diagrams": *b.Diagrams, "applies": "next-start"})
}

func (a *SystemAPI) catalogRefresh(w http.ResponseWriter, r *http.Request) {
	if a.Catalog == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the catalogue is switched off"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := a.Catalog.Refresh(ctx); err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "catalog-unreachable", Message: err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *SystemAPI) catalogInstall(w http.ResponseWriter, r *http.Request) {
	if a.Catalog == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the catalogue is switched off"})
		return
	}
	id := r.PathValue("id")
	// the check and the start under one lock with the upload's, so neither slips in between
	a.installGate.Lock()
	if a.installs.running() { // an uploaded archive is being installed (B-4)
		a.installGate.Unlock()
		writeJSON(w, http.StatusConflict, apiError{Error: "install-running", Message: errInstallRunning.Error()})
		return
	}
	// runs detached: the page polls /catalog/progress; a RedMatic download is a hundred megabytes.
	// The slot is taken before the answer (B-25): a second start while one runs is a 409, never
	// a 202 for an install that does not happen.
	done, err := a.Catalog.Start(context.Background(), id)
	a.installGate.Unlock()
	if err != nil {
		if errors.Is(err, catalog.ErrInstallRunning) {
			running := ""
			if p := a.Catalog.Progress(); p != nil {
				running = p.AddonID
			}
			reqLog(r).Info("catalog: install refused, another one runs", "addon", id, "running", running)
			writeJSON(w, http.StatusConflict, apiError{Error: "install-running", Message: err.Error()})
			return
		}
		writeErr(w, err)
		return
	}
	go func() {
		err := <-done
		a.addonsChanged() // openccu-lite B-297: installed, updated, or a failed run that changed something
		if err != nil || a.Updates == nil {
			return
		}
		// the addon's own update check ran before this install, and the Status page counts what
		// it said: ask it again, as the catalogue's own flag already follows the installed version
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		a.Updates.Recheck(ctx, id)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "addon_id": id})
}

func (a *SystemAPI) catalogProgress(w http.ResponseWriter, _ *http.Request) {
	if a.Catalog == nil {
		writeJSON(w, 200, map[string]any{"progress": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"progress": a.Catalog.Progress()})
}

// ArchName maps GOARCH to what uname -m and the addon packages say.
func ArchName() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "arm":
		return "armv7l"
	}
	return runtime.GOARCH
}

// backup creates a .sbk and streams it. A backup carries the radio keys, so a GET is not enough:
// the session needs the scope backup. With encryption on (task 91) the stream is the .sbk.age for
// the system's identity and the recovery key; ?encrypted=false with a confirmed ticket in ?confirm=
// gives the plain .sbk, journaled.
func (a *SystemAPI) backup(w http.ResponseWriter, r *http.Request) {
	if s := SessionFrom(r); s == nil || !s.Has(auth.ScopeBackup) {
		forbiddenScope(w, auth.ScopeBackup)
		return
	}
	encrypted, refused := a.encryptedDownload(w, r)
	if refused {
		return
	}
	path, err := a.Root.CreateBackup(r.Context(), a.Run)
	if err != nil {
		writeErr(w, err)
		return
	}
	// the .sbk is ~1 GB and lives in root's /usr/local/tmp: removing it needs the helper, and
	// os.Remove silently failed there, so every download left a copy behind (B-20)
	defer func() {
		if err := a.Root.RemoveBackupFile(path); err != nil {
			slog.Warn("backup: temporary file not removed", "path", path, "err", err)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	name := filepath.Base(path)
	w.Header().Set("Content-Type", "application/octet-stream")
	if encrypted {
		name += ".age"
		w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
		sha, n, err := a.streamEncrypted(w, f)
		if err != nil {
			slog.Warn("backup: encrypted download not finished", "err", err)
			return
		}
		a.recordCreated(name, sha, n, true)
		return
	}
	if a.BackupCrypt != nil && a.BackupCrypt.Enabled() {
		reqLog(r).Info("backup: unencrypted download although encryption is on", "file", name)
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	if st != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(w, h)}
	if _, err := io.Copy(cw, f); err != nil {
		return
	}
	a.recordCreated(name, hex.EncodeToString(h.Sum(nil)), cw.n, false)
}

// restoreCheck stores the upload and runs the firmware's check on it; the answer names the
// file for restoreApply and says whether a security key is needed and whether the archive
// carries a ReGa database (which this system cannot use - names come from the metadata import).
func (a *SystemAPI) restoreCheck(w http.ResponseWriter, r *http.Request) {
	var src io.Reader = r.Body
	name := "upload.sbk"
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		// task 86: a backup straight from a target, {target, name}
		rc, n, ok := a.openTargetBackup(w, r)
		if !ok {
			return
		}
		defer rc.Close()
		src, name = rc, n
	} else if len(ct) >= 9 && ct[:9] == "multipart" {
		mr, err := r.MultipartReader()
		if err != nil {
			badBody(w, err)
			return
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				badBody(w, io.ErrUnexpectedEOF)
				return
			}
			if part.FormName() == "file" {
				src, name = part, part.FileName()
				break
			}
		}
	}
	// task 91: an encrypted upload the system's own key opens is decrypted on the way in; one it
	// does not open is stored as it is and waits for /restore/decrypt with the recovery key
	var box age.Identity
	if a.BackupCrypt != nil {
		if id, _, ok := a.BackupCrypt.BoxIdentity(); ok {
			box = id
		}
	}
	up, err := a.Root.SaveUploadSniffed(src, name, box)
	if err != nil {
		writeCryptErr(w, err)
		return
	}
	opened := ""
	if up.OpenedWithBox {
		opened = "box"
	}
	enc := a.restoreEncryption(up, opened)
	out := map[string]any{"file": filepath.Base(up.Path), "encryption": enc}
	if enc.NeedsRecoveryKey {
		out["check"] = nil
	} else {
		out["check"] = a.checkBackup(r.Context(), up.Path)
	}
	writeJSON(w, 200, out)
}

// restoreApply applies a checked upload and reboots: {file, key?, force?, confirm} - confirm is
// required (400 confirm; D-120).
func (a *SystemAPI) restoreApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File    string `json:"file"`
		Key     string `json:"key"`
		Force   bool   `json:"force"`
		Confirm bool   `json:"confirm"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	// openccu-lite task 317 (D-120): the restore replaces /usr/local, the HmIP identity files with it
	if !a.identityConfirmed(w, body.Confirm, "", false) {
		return
	}
	if filepath.Base(body.File) != body.File || !strings.HasPrefix(body.File, "restore-") || !strings.HasSuffix(body.File, ".sbk") {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "file must be the name restore/check returned"})
		return
	}
	path := filepath.Join(string(a.Root), system.BackupDir, body.File)
	if _, err := os.Stat(path); err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: "upload not found - check it again"})
		return
	}
	// openccu-lite B-289: not onto an HmIP module that cannot take the backup's network key
	if refused := a.restoreHmIPRefusal(path); refused != nil {
		hmipFirmwareError(w, refused)
		return
	}
	// openccu-lite task 296: the passphrase's verdict for the answer and the journal (never the
	// passphrase). Without the backup's own passphrase the script gets a random placeholder: its
	// step 3 would otherwise derive this system's key from a wrong word, which a later key change
	// would then take for the right one; the restore at boot brings the backup's key files anyway.
	verdict := keyVerdict{Backup: system.KeyCheckNone, System: system.KeyCheckNone}
	scriptKey := body.Key
	if sig, err := a.backupSignature(path); err == nil {
		verdict = a.verdictFor(r.Context(), sig, body.Key)
		if body.Force && (verdict.Backup == system.KeyCheckMismatch || verdict.Backup == system.KeyCheckSkipped) {
			scriptKey = placeholderKey()
		}
	}
	// task 91: the system's backup identity survives the restore in /usr/local/tmp
	a.carryBoxIdentity()
	// openccu-lite B-193: the script stages the restore for the next boot and does not reboot
	// (no -r); it runs on its own context, so a browser that gives up mid-way cannot leave the
	// staging half done. The reboot comes after the answer, as /system-update/install does it:
	// the ServiceManager's reboot starts a moment later, once the answer has reached the client.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	out, err := a.Root.RestoreBackup(ctx, a.RunStdin, path, scriptKey, body.Force)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "restore-failed", Message: err.Error() + ": " + out, Detail: map[string]any{"key_check": verdict}})
		return
	}
	reqLog(r).Info("restore: backup staged for the next boot", "file", body.File, "force", body.Force, "key_backup", verdict.Backup, "key_system", verdict.System, "key_index", verdict.KeyIndex)
	if a.Manager == nil {
		writeJSON(w, 200, map[string]any{"ok": true, "output": out, "rebooting": false, "key_check": verdict, "message": "restore staged; reboot to apply it"})
		return
	}
	a.markBoot(bootexpect.KindRestore)
	if err := a.Manager.Reboot(context.Background()); err != nil {
		a.unmarkBoot()
		writeJSON(w, 200, map[string]any{"ok": true, "output": out, "rebooting": false, "key_check": verdict, "message": "restore staged, but the reboot did not start: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "output": out, "rebooting": true, "key_check": verdict})
}

func (a *SystemAPI) radioHealth(w http.ResponseWriter, r *http.Request) {
	if a.Health == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no radio sampler"})
		return
	}
	st := a.Health.Status()
	busy, which := a.Health.Busy(80)
	body := map[string]any{"busy": busy, "busy_interface": which}
	// task 94: the radio stack's units, so a page says "starting" for an interface process systemd
	// is still starting instead of "not answering"; an error from before its unit became active is
	// stale, dropped, and the sampler asked to poll again
	if units := a.radioUnits(r.Context()); units != nil {
		if staleErrors(&st, units) {
			a.Health.PollSoon()
		}
		body["units"] = units
	}
	// task 156: which physical radio each entry is, so the Status page shows one duty cycle per radio
	if p, ok := a.RadioConnections.BootPlan(); ok {
		hmip := 0
		for _, ri := range st.Interfaces {
			if ri.Interface == "HmIP-RF" {
				hmip++
			}
		}
		for i := range st.Interfaces {
			ri := &st.Interfaces[i]
			ri.Radio, ri.RadioName = p.Transmitter(ri.Interface, ri.Address, ri.Type, hmip)
			// occulited task 13: the module and the way to it, for the interface card's subtitle
			if l, ok := p.Link(ri.Interface, ri.Address, ri.Type, hmip); ok {
				ri.Module, ri.Adapter, ri.Path = l.Module, l.Adapter, l.Path
			}
		}
	}
	body["polled"], body["interfaces"], body["errors"], body["history"] = st.Polled, st.Interfaces, st.Errors, st.History
	body["answering"] = st.Answering // up, without a radio list: BidCos-Wired's hs485d, CUxD
	// occulited task 13: the telegram rates per interface process, and who is subscribed to each
	body["rates"] = st.Rates
	names := map[string]bool{}
	for _, ri := range st.Interfaces {
		names[ri.Interface] = true
	}
	for _, n := range st.Answering {
		names[n] = true
	}
	for n := range st.Errors {
		names[n] = true
	}
	// task 75: the subscriber's view - when each interface last spoke
	if a.RPC != nil {
		view := a.RPC.View()
		body["feed"] = view
		for _, f := range view.Interfaces {
			names[f.Name] = true
		}
	}
	// occulited B-45: and lite-rpc's open event streams, each one subscriber of every interface
	// its filter takes
	var streams []literpc.Stream
	if a.LiteRPC != nil {
		streams = a.LiteRPC.Streams()
	}
	subs := map[string]system.SubscriberSummary{}
	for n := range names {
		if s, ok := a.Root.SubscriberSummary(n); ok {
			var cs []system.SubscriberClient
			for _, st := range streams {
				if st.Carries(n) {
					cs = append(cs, system.SubscriberClient{ID: st.Subject.Name, Stream: &system.SubscriberStream{ID: st.ID, Kind: st.Subject.Kind, Transport: st.Transport, Remote: st.Remote}})
				}
			}
			s.AddStreams(cs)
			subs[n] = s
		}
	}
	body["subscribers"] = subs
	writeJSON(w, 200, body)
}

func (a *SystemAPI) ssh(w http.ResponseWriter, _ *http.Request) {
	s := a.Root.ReadSSH(a.Services)
	// task 245: whether sshd takes a key only - from the helper, which reads its configuration
	if on, err := system.SSHKeyOnly(); err == nil {
		s.KeyOnly = &on
	}
	writeJSON(w, 200, s)
}

func (a *SystemAPI) sshPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if err := a.Root.SetSSH(r.Context(), a.Run, a.Services, body.Enabled); err != nil {
		writeErr(w, err)
		return
	}
	a.ssh(w, r)
}

// sshPassword sets root's password for SSH; it is the one credential occulited manages that is
// not its own, so it is admin-only and never logged.
func (a *SystemAPI) sshPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	// task 185: the user's own password first, every time
	if !a.confirmed(w, r, SSHPasswordPath) {
		return
	}
	if err := a.Root.SetRootPassword(r.Context(), a.PasswordHasher, body.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *SystemAPI) updates(w http.ResponseWriter, _ *http.Request) {
	if a.Updates == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no update checker"})
		return
	}
	writeJSON(w, 200, a.Updates.State())
}

// updatesCheck runs the check now and answers with the result.
func (a *SystemAPI) updatesCheck(w http.ResponseWriter, r *http.Request) {
	if a.Updates == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no update checker"})
		return
	}
	a.Updates.Check(r.Context())
	writeJSON(w, 200, a.Updates.State())
}

func (a *SystemAPI) backupSchedule(w http.ResponseWriter, _ *http.Request) {
	c, ok := a.readCronBackup()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "stale", Message: "the backup directory does not answer"})
		return
	}
	writeJSON(w, 200, c)
}

func (a *SystemAPI) backupSchedulePut(w http.ResponseWriter, r *http.Request) {
	var s system.CronBackupSettings
	if err := readJSON(r, &s); err != nil {
		badBody(w, err)
		return
	}
	if err := a.Root.SetCronBackup(s); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	writeJSON(w, 200, a.Root.ReadCronBackup())
}

func (a *SystemAPI) backupScheduleRun(w http.ResponseWriter, r *http.Request) {
	// task 86 (D-80): the directory target is in the pipeline - upstream's cronBackup.sh is not
	// used on lite; the run starts and the answer comes at once (202)
	if a.BackupTargets != nil {
		a.startRun(w, r, backuptarget.DirectoryID)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	out, err := a.Root.RunCronBackup(ctx, a.Run)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "backup-failed", Message: err.Error() + ": " + out})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "output": out, "schedule": a.Root.ReadCronBackup()})
}

func (a *SystemAPI) logSource() string {
	if a.Journal != nil {
		return "journald"
	}
	return "syslog"
}

// logStream follows the journal as Server-Sent Events (journald only): one event per entry,
// the same JSON as /log's lines. Without systemd the answer is 501; the page polls instead.
func (a *SystemAPI) logStream(w http.ResponseWriter, r *http.Request) {
	if a.Journal == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the live log needs journald"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, errors.New("streaming not supported"))
		return
	}
	v := r.URL.Query()
	limit := -1 // absent: Follow's default history; "0": none (docs: limit lines of history first, 0 = none)
	if s := v.Get("limit"); s != "" {
		limit, _ = strconv.Atoi(s)
	}
	q, err := logQueryChecked(v, limit)
	if err == nil {
		// task 178: after a page's last cursor the entries since it are replayed first, so the
		// stream joins the page without a gap; before and head make no sense here
		err = logPage(v, &q)
		if err == nil && (q.Before != "" || q.Head) {
			err = errors.New("the stream takes after, not before or head")
		}
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	a.resolveBoot(r.Context(), &q) // B-114: an earlier boot by its id
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()
	err = a.Journal.Follow(r.Context(), q, func(l system.LogLine) {
		b, _ := json.Marshal(l)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	})
	if err != nil && r.Context().Err() == nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
		fl.Flush()
	}
}

func (a *SystemAPI) timers(w http.ResponseWriter, r *http.Request) {
	if a.Timers == nil {
		writeJSON(w, 200, map[string]any{"timers": []system.Timer{}, "systemd": false})
		return
	}
	list, err := a.Timers.Timers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"timers": list, "systemd": true})
}

// ---- own timers (task 50) ----------------------------------------------------------------------

// timerEditor is what a timer lister offers when own timers can be kept: systemd, where an own
// timer is two runtime units in /run replayed from the userfs. Without systemd there are no timers (501).
type timerEditor interface {
	ListLocalTimers() []system.LocalTimer
	ReadLocalTimer(id string) (system.LocalTimer, error)
	CreateLocalTimer(ctx context.Context, name, timer, service string) (system.LocalTimer, error)
	UpdateLocalTimer(ctx context.Context, id, timer, service string) (system.LocalTimer, error)
	DeleteLocalTimer(ctx context.Context, id string) error
	RunLocalTimer(ctx context.Context, id string) (string, error)
	CheckCalendar(ctx context.Context, expr string) (system.CalendarCheck, error)
	AnalyzeAvailable() bool
}

func (a *SystemAPI) ownTimerEditor(w http.ResponseWriter) (timerEditor, bool) {
	ed, ok := a.Timers.(timerEditor)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-systemd", Message: "own timers need systemd"})
	}
	return ed, ok
}

// writeTimerErr answers a failed own-timer operation: a timer that is not stored is 404, a name
// that is taken 409, everything else - a bad name, a file of the wrong shape, what systemd refused
// to load, a failed systemctl - 422 with the reason, which the dialog shows beside the text.
func writeTimerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, system.ErrLocalTimerNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-found", Message: err.Error()})
	case errors.Is(err, system.ErrLocalTimerExists):
		writeJSON(w, http.StatusConflict, apiError{Error: "exists", Message: err.Error()})
	default:
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
	}
}

func (a *SystemAPI) localTimers(w http.ResponseWriter, _ *http.Request) {
	ed, ok := a.ownTimerEditor(w)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"timers": ed.ListLocalTimers(), "analyze": ed.AnalyzeAvailable()})
}

type localTimerBody struct {
	Name    string `json:"name"`
	Timer   string `json:"timer"`
	Service string `json:"service"`
}

func (a *SystemAPI) localTimerCreate(w http.ResponseWriter, r *http.Request) {
	ed, ok := a.ownTimerEditor(w)
	if !ok {
		return
	}
	var body localTimerBody
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	lt, err := ed.CreateLocalTimer(r.Context(), body.Name, body.Timer, body.Service)
	if err != nil {
		writeTimerErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, lt)
}

func (a *SystemAPI) localTimer(w http.ResponseWriter, r *http.Request) {
	ed, ok := a.ownTimerEditor(w)
	if !ok {
		return
	}
	lt, err := ed.ReadLocalTimer(r.PathValue("name"))
	if err != nil {
		writeTimerErr(w, err)
		return
	}
	writeJSON(w, 200, lt)
}

func (a *SystemAPI) localTimerPut(w http.ResponseWriter, r *http.Request) {
	ed, ok := a.ownTimerEditor(w)
	if !ok {
		return
	}
	var body localTimerBody
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	lt, err := ed.UpdateLocalTimer(r.Context(), r.PathValue("name"), body.Timer, body.Service)
	if err != nil {
		writeTimerErr(w, err)
		return
	}
	writeJSON(w, 200, lt)
}

func (a *SystemAPI) localTimerDelete(w http.ResponseWriter, r *http.Request) {
	ed, ok := a.ownTimerEditor(w)
	if !ok {
		return
	}
	if err := ed.DeleteLocalTimer(r.Context(), r.PathValue("name")); err != nil {
		writeTimerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *SystemAPI) localTimerRun(w http.ResponseWriter, r *http.Request) {
	ed, ok := a.ownTimerEditor(w)
	if !ok {
		return
	}
	out, err := ed.RunLocalTimer(r.Context(), r.PathValue("name"))
	if err != nil {
		writeTimerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "output": out})
}

// timerCalendar checks an OnCalendar= expression: the next three run times, or systemd's
// complaint; available false when the box has no systemd-analyze.
func (a *SystemAPI) timerCalendar(w http.ResponseWriter, r *http.Request) {
	ed, ok := a.ownTimerEditor(w)
	if !ok {
		return
	}
	var body struct {
		Expression string `json:"expression"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	res, err := ed.CheckCalendar(r.Context(), body.Expression)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid", "message": err.Error(), "output": res.Output})
		return
	}
	writeJSON(w, 200, res)
}

// System firmware update (task 16): staged as the WebUI's Firmware update dialog staged it, so
// the recovery system installs it at the next boot - forward to a newer openccu-lite, or back
// to OpenCCU with its release zip.

func (a *SystemAPI) systemUpdate(w http.ResponseWriter, _ *http.Request) {
	// container: "lxc" when the rootfs is a template that is swapped on the host (task 34);
	// the feed still says whether a newer template exists, nothing here downloads or installs it
	out := map[string]any{"running": a.Root.ReadVersion(), "staged": a.Root.StagedSystemUpdate(), "feed": nil, "container": a.Root.Container()}
	if a.Feed != nil {
		out["feed"] = a.Feed.State()
	}
	writeJSON(w, 200, out)
}

func (a *SystemAPI) systemUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if a.Feed == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no release feed configured"})
		return
	}
	err := a.Feed.Check(r.Context())
	st := a.Feed.State()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, feedError(err, "feed", st))
		return
	}
	writeJSON(w, 200, map[string]any{"feed": st})
}

func (a *SystemAPI) systemUpdateDownload(w http.ResponseWriter, r *http.Request) {
	if a.hostManaged(w, "the system update") {
		return
	}
	if a.Feed == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no release feed configured"})
		return
	}
	// occulited task 22: {"version"} downloads that published version (a downgrade among them),
	// {"version": "latest"} the newest of the default channel; no body is the available release
	var b struct {
		Version string `json:"version"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &b); err != nil {
			badBody(w, err)
			return
		}
	}
	// the download outlives the request's context on purpose: a closed browser tab must not
	// leave a half-written file behind as "staged"
	var u *system.StagedUpdate
	var err error
	switch b.Version {
	case "":
		u, err = a.Feed.Download(context.Background())
	case "latest":
		u, err = a.Feed.DownloadVersion(context.Background(), "")
	default:
		u, err = a.Feed.DownloadVersion(context.Background(), b.Version)
	}
	if noUpdateSpace(w, err) {
		return
	}
	if errors.Is(err, sysupdate.ErrNoSuchVersion) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "download-failed", Message: err.Error()})
		return
	}
	writeJSON(w, 200, u)
}

// feedError is the 502 of a failed feed request; a host that did not answer in time (B-56) is
// named in detail, so the page can say so in the user's language.
func feedError(err error, key string, v any) map[string]any {
	out := map[string]any{"error": "feed-unreachable", "message": err.Error(), key: v}
	if na, ok := httpwait.As(err); ok {
		out["detail"] = map[string]any{"host": na.Host, "timeout": na.Seconds()}
	}
	return out
}

// systemUpdateReleases lists this product's published releases in a channel, newest first, each
// with what installing it means (occulited task 22: `occulited update check` and `install
// <version>`). ?channel=pre|stable|all; none is the channel the Updates page follows.
func (a *SystemAPI) systemUpdateReleases(w http.ResponseWriter, r *http.Request) {
	if a.Feed == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no release feed configured"})
		return
	}
	ch := r.URL.Query().Get("channel")
	if ch != "" && ch != sysupdate.ChannelPre && ch != sysupdate.ChannelStable && ch != sysupdate.ChannelAll {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "channel: pre, stable or all"})
		return
	}
	list, err := a.Feed.Releases(r.Context(), ch)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, feedError(err, "releases", list))
		return
	}
	writeJSON(w, 200, list)
}

// noUpdateSpace answers a staged update that would not fit where the recovery unpacks it
// (B-247): 422 no-space with the free and the required bytes, which the Updates page words.
func noUpdateSpace(w http.ResponseWriter, err error) bool {
	var se *system.UpdateSpaceError
	if !errors.As(err, &se) {
		return false
	}
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "no-space", Message: se.Error(), Detail: map[string]any{"free": se.Free, "required": se.Required}})
	return true
}

// uploadPart returns the "file" part of a multipart body (or the body itself) and its name.
func uploadPart(r *http.Request) (io.Reader, string, error) {
	if ct := r.Header.Get("Content-Type"); len(ct) >= 9 && ct[:9] == "multipart" {
		mr, err := r.MultipartReader()
		if err != nil {
			return nil, "", err
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				return nil, "", io.ErrUnexpectedEOF
			}
			if part.FormName() == "file" {
				return part, part.FileName(), nil
			}
		}
	}
	return r.Body, r.URL.Query().Get("name"), nil
}

func (a *SystemAPI) systemUpdateUpload(w http.ResponseWriter, r *http.Request) {
	if a.hostManaged(w, "the system update") {
		return
	}
	src, name, err := uploadPart(r)
	if err != nil {
		badBody(w, err)
		return
	}
	u, err := a.Root.StageSystemUpdate(r.Context(), name, r.ContentLength, src)
	if noUpdateSpace(w, err) {
		return
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "update-rejected", Message: err.Error()})
		return
	}
	writeJSON(w, 200, a.clearHSTSForWayBack(r.Context(), u))
}

// stagedAnswer is the upload's answer: the staged file, and what staging a way back did to HSTS.
type stagedAnswer struct {
	*system.StagedUpdate
	// HSTSCleared: HSTS was on and is now off, sending max-age=0 until HSTSClearingUntil.
	HSTSCleared       bool       `json:"hsts_cleared,omitempty"`
	HSTSClearingUntil *time.Time `json:"hsts_clearing_until,omitempty"`
	// HSTSError: HSTS could not be switched off; the file is staged all the same.
	HSTSError string `json:"hsts_error,omitempty"`
}

// clearHSTSForWayBack (task 96, D-64): a staged file that may be the way back to OpenCCU switches
// HSTS into the clearing state at once, so max-age=0 reaches the browsers while this box still
// answers with its trusted certificate - OpenCCU sends no HSTS and later serves a self-signed one.
func (a *SystemAPI) clearHSTSForWayBack(ctx context.Context, u *system.StagedUpdate) stagedAnswer {
	out := stagedAnswer{StagedUpdate: u}
	if !u.WayBack || a.HTTPS == nil {
		return out
	}
	cleared, until, err := a.HTTPS.ClearHSTS(ctx, func(l string) { slog.Info("https: " + l) })
	if err != nil {
		slog.Warn("https: HSTS could not be switched off for the way back", "err", err)
		out.HSTSError = err.Error()
	}
	out.HSTSCleared = cleared
	if until > 0 {
		t := time.Unix(until, 0).UTC()
		out.HSTSClearingUntil = &t
	}
	return out
}

func (a *SystemAPI) systemUpdateInstall(w http.ResponseWriter, _ *http.Request) {
	if a.hostManaged(w, "the system update") {
		return
	}
	if err := a.Root.ArmSystemUpdate(); err != nil {
		if noUpdateSpace(w, err) {
			return
		}
		writeJSON(w, http.StatusConflict, apiError{Error: "nothing-staged", Message: err.Error()})
		return
	}
	if a.Manager == nil {
		writeJSON(w, 200, map[string]any{"armed": true, "rebooting": false, "message": "recovery armed; reboot to install"})
		return
	}
	a.markBoot(bootexpect.KindUpdate)
	if err := a.Manager.Reboot(context.Background()); err != nil {
		a.unmarkBoot()
		writeJSON(w, 200, map[string]any{"armed": true, "rebooting": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"armed": true, "rebooting": true})
}

func (a *SystemAPI) systemUpdateDiscard(w http.ResponseWriter, _ *http.Request) {
	a.Root.DiscardSystemUpdate()
	writeJSON(w, 200, map[string]any{"staged": nil})
}

// Addon policy (D-36): how an addon runs on a systemd box. The manager is the systemd one or
// there is nothing to report.

func (a *SystemAPI) policyManager() *system.SystemdAddons {
	sa, _ := a.Manager.(*system.SystemdAddons)
	return sa
}

func (a *SystemAPI) addonPolicy(w http.ResponseWriter, r *http.Request) {
	sa := a.policyManager()
	if sa == nil {
		writeJSON(w, 200, map[string]any{"systemd": false, "policy": nil})
		return
	}
	id := r.PathValue("id")
	v := sa.PolicyView(id)
	declared, early := sa.StartEarly(id)
	writeJSON(w, 200, map[string]any{"systemd": true, "default_mode": sa.EffectiveDefaultMode(), "policy": sa.Policy(id),
		"mode": v.Mode, "source": v.Source, "undeclared": v.Undeclared,
		// D-66: a root addon that keeps the right to mount, and one whose remount was refused
		"may_mount": v.MayMount, "remount_refused": sa.RemountRefused(r.Context())[id],
		// task 119: whether the entry declares the early start and whether it applies at the next boot
		"start_early_declared": declared, "start_early": early})
}

// addonPolicyPut switches an addon between root and its own user, and restarts its unit.
func (a *SystemAPI) addonPolicyPut(w http.ResponseWriter, r *http.Request) {
	sa := a.policyManager()
	if sa == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "addon policies need the systemd product"})
		return
	}
	var body struct {
		Mode    string `json:"mode"`
		Unsafe  bool   `json:"unsafe"`
		Restart *bool  `json:"restart"`
		// StartEarly is the addon's early-start switch (task 119); it takes effect at the next
		// boot and restarts nothing. A body with it and without a mode changes only the switch.
		StartEarly *bool `json:"start_early"`
	}
	if err := decodeSmall(w, r, &body); err != nil {
		badBody(w, err)
		return
	}
	if body.StartEarly != nil {
		if !a.earlyStartReady(w) {
			return
		}
		id := r.PathValue("id")
		if !validAddonID(id) {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "invalid addon id"})
			return
		}
		if err := a.setAddonEarlyStart(id, *body.StartEarly); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "config", Message: err.Error()})
			return
		}
		if body.Mode == "" {
			declared, early := sa.StartEarly(id)
			writeJSON(w, 200, map[string]any{"policy": sa.Policy(id), "restarted": false, "start_early_declared": declared, "start_early": early, "next_boot": true})
			return
		}
	}
	// D-36: confined is the default and root is the opt-out, so root is not a mode one reaches
	// by leaving a field out - the caller has to say `"unsafe": true` and mean it. The UI's
	// button is labelled unsafe and its confirmation says what it costs: an addon running as
	// root can change anything on the box, the firmware and occulited's own files included.
	if body.Mode == "root" && !body.Unsafe {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "unsafe_not_confirmed",
			Message: "running an addon as root lets it change anything on the system; repeat the request with \"unsafe\": true"})
		return
	}
	id := r.PathValue("id")
	if body.Restart == nil || *body.Restart {
		// B-106: the addon is stopped before its files change hands and started as the new user after
		sw, err := sa.SwitchPolicy(r.Context(), id, body.Mode, "user")
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
			return
		}
		out := map[string]any{"policy": sw.Policy, "restarted": sw.Restarted}
		if sw.RestartError != "" {
			out["restart_error"] = sw.RestartError
		}
		writeJSON(w, 200, out)
		return
	}
	p, err := sa.SetPolicy(r.Context(), id, body.Mode, "user", nil)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"policy": p, "restarted": false})
}

// Enabled/disabled addons: the rc.d script's executable bit (run-parts and the systemd generator
// both skip a non-executable script). ReGa-dependent addons are disabled after an update from
// OpenCCU; re-enabling one is the user's call, with the warning on the page.

func (a *SystemAPI) addonEnable(w http.ResponseWriter, r *http.Request) {
	a.setAddonEnabled(w, r, true)
}
func (a *SystemAPI) addonDisable(w http.ResponseWriter, r *http.Request) {
	a.setAddonEnabled(w, r, false)
}

func (a *SystemAPI) setAddonEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := r.PathValue("id")
	if err := a.Root.SetAddonEnabled(id, enabled); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	out := map[string]any{"id": id, "enabled": enabled}
	// a disabled addon is stopped, an enabled one started - through whatever runs it here
	action := "stop"
	if enabled {
		action = "start"
	}
	if sd, ok := a.Manager.(*system.SystemdAddons); ok {
		if !enabled {
			// B-58: stop, reload (the unit disappears), and forget a failure of the unit whose
			// file is gone - a disabled addon leaves no "not-found failed" ghost behind
			if e := sd.SettleDisabled(r.Context(), id); e[id] != "" {
				out["control_error"] = e[id]
			}
			a.addonsChanged()
			writeJSON(w, 200, out)
			return
		}
		sd.Reload(r.Context()) // the generator sees the changed executable bit
	}
	if !a.waitRadio(w, r, serviceIDFor(a, id), action) {
		return
	}
	if o, err := a.Services.Control(r.Context(), serviceIDFor(a, id), action); err != nil {
		out["control_error"] = err.Error() + ": " + o
	}
	a.addonsChanged() // openccu-lite B-297
	writeJSON(w, 200, out)
}

// serviceIDFor names the addon's service as the service manager knows it.
func serviceIDFor(a *SystemAPI, id string) string {
	if _, sd := a.Manager.(*system.SystemdAddons); sd {
		return "addon-" + id
	}
	return id
}

// ---- log levels (task 27.8) ------------------------------------------------------------------

// logLevelsView is both answers: the levels as stored, and after a PUT what still has to be
// restarted for the change to show, and which live applications went through.
type logLevelsView struct {
	system.LogLevels
	// Occulited is occulited's own level (task 101), from occulited.json, applied live.
	Occulited logctl.Setting    `json:"occulited"`
	Restart   []string          `json:"restart"` // units whose next start applies the change
	Applied   []string          `json:"applied"` // interfaces (and occulited) that took the level live
	Errors    map[string]string `json:"errors,omitempty"`
}

// OcculitedLogLevel is occulited's own level behind the Log settings (task 101).
type OcculitedLogLevel interface {
	Get() logctl.Setting
	// Set validates, stores and applies; the answer is the setting in effect.
	Set(ctx context.Context, s logctl.Setting) (logctl.Setting, error)
}

func (a *SystemAPI) occulitedLog() logctl.Setting {
	if a.OcculitedLog == nil {
		return logctl.Setting{Level: logctl.DefaultLevel, DebugAreas: []string{}}
	}
	return a.OcculitedLog.Get()
}

func (a *SystemAPI) logLevels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, logLevelsView{LogLevels: a.Root.ReadLogLevels(), Occulited: a.occulitedLog(), Restart: []string{}, Applied: []string{}})
}

// logLevelsPut takes the daemons' levels and occulited's. Two keys may be left out by a client
// that does not know them: without "multimacd" its stored level stays, without "occulited"
// occulited's stays. multimacd's is 1 or 2 (task 297): null, the old "same as rfd", reads as 0
// and is refused with the rest. Everything is checked before anything is written.
func (a *SystemAPI) logLevelsPut(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	var body system.LogLevels
	var extra struct {
		MultiMACD json.RawMessage `json:"multimacd"`
		Occulited *logctl.Setting `json:"occulited"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	if err := json.Unmarshal(raw, &extra); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	old := a.Root.ReadLogLevels()
	if extra.MultiMACD == nil {
		body.MultiMACD = old.MultiMACD
	}
	ownWas := a.occulitedLog()
	var own *logctl.Setting
	if extra.Occulited != nil {
		n, err := logctl.Normalize(*extra.Occulited)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
			return
		}
		if n.Level != ownWas.Level || strings.Join(n.DebugAreas, ",") != strings.Join(ownWas.DebugAreas, ",") {
			if a.OcculitedLog == nil {
				writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-available", Message: "occulited's level cannot be set here"})
				return
			}
			own = &n
		}
	}
	levels, restart, err := a.Root.SetLogLevels(body)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	view := logLevelsView{LogLevels: levels, Occulited: ownWas, Restart: restart, Applied: []string{}}
	if view.Restart == nil {
		view.Restart = []string{}
	}
	if own != nil {
		set, err := a.OcculitedLog.Set(r.Context(), *own)
		if err != nil {
			view.Errors = map[string]string{"occulited": err.Error()}
		} else {
			view.Occulited = set
			view.Applied = append(view.Applied, "occulited")
		}
	}
	// rfd and hs485d take the level live; when that fails the restart list says so instead.
	for _, x := range []struct {
		iface, unit string
		was, now    int
	}{{"BidCos-RF", "rfd", old.RFD, levels.RFD}, {"BidCos-Wired", "hs485d", old.HS485D, levels.HS485D}} {
		if x.was == x.now {
			continue
		}
		if a.SetLogLevel == nil {
			view.Restart = append(view.Restart, x.unit)
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := a.SetLogLevel(ctx, x.iface, x.now)
		cancel()
		if err != nil {
			if view.Errors == nil {
				view.Errors = map[string]string{}
			}
			view.Errors[x.unit] = err.Error()
			view.Restart = append(view.Restart, x.unit)
			continue
		}
		view.Applied = append(view.Applied, x.unit)
	}
	writeJSON(w, http.StatusOK, view)
}

// radioRestart restarts the radio stack in order (task 101): hmipserver, rfd and multimacd stop,
// multimacd, rfd and hmipserver start - the units that ran. It is how a new multimacd level
// applies; multimacd alone cannot restart under the two daemons that hold its nodes.
func (a *SystemAPI) radioRestart(w http.ResponseWriter, r *http.Request) {
	if a.Services == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no service manager"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	res, err := system.RestartRadioStack(ctx, a.Services)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "service-control", "message": err.Error(), "stopped": res.Stopped, "started": res.Started, "errors": res.Errors})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- the device descriptions' writable layer (D-66) ------------------------------------------

// deviceDescriptions answers what /firmware/rftypes holds and how it is writable
// (system.Root.DeviceDescriptions).
func (a *SystemAPI) deviceDescriptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Root.DeviceDescriptions())
}

// deviceDescriptionsReset makes the image's files win again in the writable layer and restarts
// rfd when it runs; the answer is the state afterwards.
func (a *SystemAPI) deviceDescriptionsReset(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := system.ResetDeviceDescriptions(ctx, a.Root, a.Services)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "device-descriptions", "message": err.Error(), "state": res.State})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- the journal's knobs (task 23, task 85) --------------------------------------------------

// journalConfig answers the file, what is in effect and the figures (system.ReadJournalConfig).
func (a *SystemAPI) journalConfig(w http.ResponseWriter, r *http.Request) {
	if a.Journal == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-systemd", Message: "the journal's settings need journald"})
		return
	}
	c := a.Root.ReadJournalConfig()
	c.Usage = a.Journal.DiskUsage(r.Context())
	a.journalCheck.apply(&c)
	writeJSON(w, http.StatusOK, c)
}

// journalConfigPut writes the file and re-runs the boot script through its unit, which is
// where the decision is made at boot too - one place, not two. A switch to persistent applies at
// once (the script mounts and flushes), one to RAM at the next reboot (a journal in use is not
// unmounted; the answer's reboot_pending says so), the sizes at once.
//
// storage is the setting; the deprecated persist is honoured only from a client that does not
// know storage, i.e. a body with no storage key at all - a present storage, even an empty one,
// wins over whatever persist says.
func (a *SystemAPI) journalConfigPut(w http.ResponseWriter, r *http.Request) {
	if a.Journal == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-systemd", Message: "the journal's settings need journald"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	var body system.JournalConfig
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	if err := json.Unmarshal(raw, &keys); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	if _, ok := keys["storage"]; !ok {
		s, err := system.JournalStorageFromPersist(body.Persist)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
			return
		}
		body.Storage = s
	}
	if err := a.journalShareOK(body.Target); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	// B-213: a share or a plugged-in stick the copies go to is write-tested in the folder before the
	// setting is taken; a new target or a switch to ram-sync that fails is refused with the reason,
	// a save that keeps both (the sizes, say) is taken and target_ok says what the test found.
	cur := a.Root.ReadJournalSetting()
	if res, tested := a.journalTargetTest(r.Context(), body); tested {
		a.journalCheck.set(strings.TrimSpace(body.Target), res)
		wanted := strings.TrimSpace(body.Storage)
		if wanted == "" {
			wanted = cur.DefaultStorage
		}
		changed := cur.Target != strings.TrimSpace(body.Target) || cur.JournalWanted() != wanted
		if !res.ok && changed {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: res.state, Message: res.message,
				Detail: map[string]any{"state": res.state, "step": res.step, "error": res.detail, "kind": res.kind}})
			return
		}
	}
	if _, err := a.Root.SetJournalConfig(body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	if out, err := a.Services.Control(r.Context(), "occu-persist", "restart"); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "service-control", "message": err.Error(), "output": out})
		return
	}
	c := a.Root.ReadJournalConfig()
	c.Usage = a.Journal.DiskUsage(r.Context())
	a.journalCheck.apply(&c)
	writeJSON(w, http.StatusOK, c)
}

// journalTargetCheck is the last write test of the journal's copy target (B-213): the view's
// target_ok is false while it says the target cannot take the copies, not only when its mount
// options say read-only.
type journalTargetCheck struct {
	mu     sync.Mutex
	target string
	res    journalTestResult
}

// journalTestResult is one write test of the journal's target.
type journalTestResult struct {
	ok                                 bool
	state, step, detail, message, kind string
}

func (j *journalTargetCheck) set(target string, res journalTestResult) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.target, j.res = target, res
}

func (j *journalTargetCheck) apply(c *system.JournalConfig) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.target != "" && j.target == c.Target && !j.res.ok {
		c.TargetOK = false
	}
	// B-223: written, but not readable by the system's own user - a look while the share is
	// mounted says so too; the last test covers an idle share
	if j.target != "" && j.target == c.Target && j.res.state == shares.StateUnreadable {
		c.TargetUnreadable = true
	}
}

// journalTargetTest write-tests the folder the journal's copies go to when the setting sends them
// to a share or a stick that is plugged in (as root, through the helper - the copies are written by
// a root script); tested is false for anything else (the userfs, RAM only, a stick not there).
func (a *SystemAPI) journalTargetTest(ctx context.Context, body system.JournalConfig) (res journalTestResult, tested bool) {
	storage := strings.TrimSpace(body.Storage)
	if storage == "" {
		storage = a.Root.ReadJournalSetting().DefaultStorage
	}
	if storage != system.JournalRAMSync || a.Shares == nil {
		return res, false
	}
	target := strings.TrimSpace(body.Target)
	if name, dir, ok := system.ParseJournalShareTarget(target); ok {
		kind := ""
		if sh, found, _ := a.Shares.Store.Get(name); found {
			kind = sh.Kind
		}
		r, err := a.Shares.TestFolder(ctx, name, dir)
		if err != nil {
			return journalTestResult{state: shares.StateError, step: "test", detail: err.Error(), kind: kind,
				message: "the network share " + name + " could not be tested: " + err.Error()}, true
		}
		res = journalTestResult{ok: r.OK, state: r.State, step: r.Step, detail: r.Error, kind: kind}
		if r.State == shares.StateUnreadable {
			// B-223: the copies can be written, so the setting is taken; the view says they cannot
			// be shown
			res.message = "the network share " + name + " takes the journal's copies in " + dir + ", but the system cannot read them back: " + r.Error + ". " + shares.UnreadableHint
		}
		if !r.OK {
			res.message = "the network share " + name + " cannot take the journal's copies in " + dir + ": " + r.State
			if r.Error != "" {
				res.message += " (" + r.Step + ": " + r.Error + ")"
			}
			if r.State == shares.StateReadOnly && kind == shares.KindNFS {
				res.message += ". An export that maps root to nobody (root_squash) needs a directory that user may write, or \"map all users\" to one account."
			}
		}
		return res, true
	}
	if label, dir, ok := system.ParseJournalUSBTarget(target); ok {
		stick, found := a.Root.USBStickByLabel(label)
		if !found {
			return res, false // the copies wait for the stick (task 216); nothing to test yet
		}
		w, err := a.Shares.Helper().WriteTest(ctx, a.Root.Path(stick.Mount+"/"+dir))
		if err != nil {
			return journalTestResult{state: shares.StateError, step: "helper", detail: err.Error(), kind: "usb",
				message: "the USB stick " + label + " could not be tested: " + err.Error()}, true
		}
		res = journalTestResult{ok: w.OK, state: shares.StateFromTest(w), step: w.Step, detail: w.Error, kind: "usb"}
		if !w.OK {
			res.message = "the USB stick " + label + " cannot take the journal's copies in " + dir + ": " + res.state + " (" + w.Step + ": " + w.Error + ")"
		}
		return res, true
	}
	return res, false
}

// ---- addon control (task 28.8) ---------------------------------------------------------------

// journalShareOK: a share target (task 228) names a share of System → Storage that can take the
// copies; other targets are the file's own checks.
func (a *SystemAPI) journalShareOK(target string) error {
	name, _, isShare := system.ParseJournalShareTarget(target)
	if !isShare {
		return nil
	}
	var sh shares.Share
	found := false
	if a.Shares != nil {
		sh, found, _ = a.Shares.Store.Get(name)
	}
	switch {
	case !found:
		return fmt.Errorf("there is no network share %s on System → Storage", name)
	case sh.ReadOnly:
		return fmt.Errorf("the network share %s is mounted read-only: it cannot take the journal's copies", name)
	}
	return nil
}

// journalSyncNow is the Journal panel's "Copy now" (task 85): a restart of the ram-sync copy unit,
// whose stop is the copy - the same path as at shutdown, serialised with the interval's copy by
// lite-journal-sync's lock. Only while ram-sync is chosen and in effect; otherwise 409, as there
// is nothing to copy (RAM only) or nothing in RAM (persistent). The answer is the GET view, with
// the copy's result in last_sync_*.
func (a *SystemAPI) journalSyncNow(w http.ResponseWriter, r *http.Request) {
	if a.Journal == nil || a.Services == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-systemd", Message: "the journal's copies need journald"})
		return
	}
	c := a.Root.ReadJournalConfig()
	// a share the last copy did not reach may be tried again by hand (task 228)
	if c.JournalWanted() != system.JournalRAMSync || (c.Effective != system.JournalRAMSync && c.TargetShare == "") {
		msg := "the journal is not in RAM with copies to the userfs now"
		if c.TargetLabel != "" {
			msg = "the journal is not in RAM with copies to the USB stick " + c.TargetLabel + " now: the stick is not plugged in, or ram-sync is not chosen"
		}
		writeJSON(w, http.StatusConflict, apiError{Error: "not-ram-sync", Message: msg})
		return
	}
	if out, err := a.Services.Control(r.Context(), system.JournalSyncUnit, "restart"); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "service-control", "message": err.Error(), "output": out})
		return
	}
	c = a.Root.ReadJournalConfig()
	c.Usage = a.Journal.DiskUsage(r.Context())
	writeJSON(w, http.StatusOK, c)
}

// AddonController maps an addon control token to its addon.
type AddonController interface {
	Lookup(token string) (string, bool)
}

// addonCtl is the one route an addon's own rc.d wrapper may call as the addon's user: start,
// stop or restart of addon-<id>.service, with the token from /run/occulite/addon-tokens/<id>.
// The path is outside the session check (auth.open); the token is the whole of its authority.
func (a *SystemAPI) addonCtl(w http.ResponseWriter, r *http.Request) {
	if a.AddonCtl == nil || a.Services == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "not-systemd", Message: "addon control needs the systemd product"})
		return
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "an addon control token is required"})
		return
	}
	id, ok := a.AddonCtl.Lookup(strings.TrimSpace(h[7:]))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "unknown addon control token"})
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	switch body.Action {
	case "start", "stop", "restart":
	default:
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "action must be start, stop or restart"})
		return
	}
	if !a.waitRadio(w, r, "addon-"+id, body.Action) {
		return
	}
	out, err := a.Services.Control(r.Context(), "addon-"+id, body.Action)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "service-control", "message": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unit": "addon-" + id + ".service", "action": body.Action, "output": out})
}

// CertificateNames is the rename's certificate reminder (openccu-lite task 62): the names the
// live certificate carries, whether one of them is the new host (bare, or as <host>.<domain>),
// and how the certificate is managed - self-signed (S50lighttpd's, renewed on its own only when
// it expires), acme or manual.
type CertificateNames struct {
	Names []string `json:"names"`
	Fits  bool     `json:"fits"`
	Mode  string   `json:"mode"`
	// Known is false when the live certificate could not be read
	Known bool `json:"known"`
}

// certificateNames reads the live certificate through the certificate service.
func (a *SystemAPI) certificateNames(host string) CertificateNames {
	c := CertificateNames{Names: []string{}, Mode: "self-signed"}
	if a.Cert == nil {
		return c
	}
	st := a.Cert.Status()
	if st.Managed {
		c.Mode = st.ManagedMode
	}
	if st.Current == nil {
		return c
	}
	c.Known = true
	c.Names = append(c.Names, st.Current.Names...)
	c.Fits = NamesFit(c.Names, host)
	return c
}

// NamesFit says whether one of a certificate's names is the host itself or the host in a domain.
func NamesFit(names []string, host string) bool {
	h := strings.ToLower(host)
	for _, n := range names {
		n = strings.ToLower(strings.TrimPrefix(n, "*."))
		if n == h || strings.HasPrefix(n, h+".") {
			return true
		}
	}
	return false
}
