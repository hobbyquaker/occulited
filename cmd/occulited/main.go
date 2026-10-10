// occulited is the system service of openccu-lite: the metadata store and its API today; sessions,
// system administration and addon management as the roadmap's tasks land. It listens on the
// loopback only; lighttpd is what faces the LAN (D-3, D-29).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hobbyquaker/occulited/internal/acme"
	"github.com/hobbyquaker/occulited/internal/addonupdates"
	"github.com/hobbyquaker/occulited/internal/devstate"
	"github.com/hobbyquaker/occulited/internal/eq3disc"
	"github.com/hobbyquaker/occulited/internal/health"
	"github.com/hobbyquaker/occulited/internal/journald"
	"github.com/hobbyquaker/occulited/internal/led"
	"github.com/hobbyquaker/occulited/internal/linkwatch"
	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/hobbyquaker/occulited/internal/logctl"
	"github.com/hobbyquaker/occulited/internal/mqttpub"
	"github.com/hobbyquaker/occulited/internal/oidc"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/regaimport"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/rpctrace"
	"github.com/hobbyquaker/occulited/internal/runlog"
	"github.com/hobbyquaker/occulited/internal/servicemsg"
	"github.com/hobbyquaker/occulited/internal/ssdp"
	datastore "github.com/hobbyquaker/occulited/internal/store"

	"golang.org/x/term"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/bootchart"
	"github.com/hobbyquaker/occulited/internal/bootexpect"
	"github.com/hobbyquaker/occulited/internal/catalog"
	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/favorites"
	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/hmgroups"
	"github.com/hobbyquaker/occulited/internal/httpapi"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/meta"
	"github.com/hobbyquaker/occulited/internal/pairing"
	"github.com/hobbyquaker/occulited/internal/radio"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/sysupdate"
	"github.com/hobbyquaker/occulited/internal/trust"
	"github.com/hobbyquaker/occulited/internal/ui"
)

// version and commit are set at build time with -ldflags -X: version is the commit occulited was
// built from (occulited task 16 took back task 9's image version) - the full hash, -dirty for an
// uncommitted tree, -hot for a binary deployed by hand, "dev" for a bare go build; commit is the bare
// hash. scripts/build.sh sets both, the image's package passes the pinned commit as both.
var (
	version = "dev"
	commit  = ""
)

// versionLine is what --version prints and the journal's start line says: the version, and the
// commit beside it only where the version does not begin with it.
func versionLine() string {
	if commit == "" || strings.HasPrefix(version, commit) {
		return "occulited " + version
	}
	return "occulited " + version + " (" + commit + ")"
}

func main() {
	// B-5: a first word that is no command is refused before anything else runs
	if _, err := pickCommand(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "occulited: %s\n%s\n", strings.TrimPrefix(err.Error(), "usage: "), usageLine)
		os.Exit(2)
	}
	// task 107: first, and a flag - see addonOwnFlag
	if len(os.Args) > 1 && os.Args[1] == addonOwnFlag {
		os.Exit(addonOwnMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "passwd" {
		if err := passwd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited passwd:", err)
			os.Exit(1)
		}
		return
	}
	// B-120: a flag for the same reason as addonOwnFlag - an older binary handed the word would
	// start a second daemon as root inside lighttpd's ExecStartPre; handed the flag, it exits 2
	if len(os.Args) > 1 && os.Args[1] == lighttpdDropinsFlag {
		os.Exit(lighttpdDropinsMain(os.Args[2:]))
	}
	// openccu-lite task 86: the backup pipeline's halves, a flag for the same reason
	if len(os.Args) > 1 && os.Args[1] == backupFlag {
		os.Exit(backupMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "helper" {
		if err := helperMain(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited helper:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "syslog-forward" {
		if err := syslogForward(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited syslog-forward:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == priv.RPCDropCommand {
		// openccu-lite B-201: the helper's child that shuts an interface process's connection to
		// a callback listener that does not answer (priv.dropRPC); pid and descriptor
		if err := rpcDropChild(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited rpc-drop:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "token" {
		if err := token(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited token:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "radio" {
		if err := radioMain(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited radio:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "wifi" {
		if err := wifiMain(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited wifi:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "firewall" {
		if err := firewallCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited firewall:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "auth" {
		if err := authCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited auth:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		if err := adminCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited admin:", err)
			os.Exit(1)
		}
		return
	}
	// occulited task 22: the system update from the command line, through the running daemon
	if len(os.Args) > 1 && os.Args[1] == "update" {
		os.Exit(updateCmd(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "webauthn" {
		if err := webauthnCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "occulited webauthn:", err)
			os.Exit(1)
		}
		return
	}
	opts, err := parseDaemonArgs(os.Args[1:], os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return
	case err != nil:
		fmt.Fprintf(os.Stderr, "occulited: %s\n%s\n", strings.TrimPrefix(err.Error(), "usage: "), usageLine)
		os.Exit(2)
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "occulited:", err)
		os.Exit(1)
	}
}

func run(opts daemonOptions) error {
	cfgPath, listen, stateDir, logTarget := &opts.config, &opts.listen, &opts.stateDir, &opts.log
	rootDir, sessionDir, helperSocket := &opts.root, &opts.sessionDir, &opts.helperSocket
	if opts.version {
		fmt.Println(versionLine())
		return nil
	}
	// B-5: before the config is read or anything written
	if err := refuseRootDaemon(priv.IsRoot(), "/", *helperSocket); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *stateDir != "" {
		cfg.StateDir = *stateDir
	}
	// task 101: occulited's level applies while it runs (internal/logctl). The base handler lets
	// every level through and the control decides per line; an area's logger (area below) also
	// writes that area's debug lines while the Log settings switch the area on.
	logs := logctl.New()
	_, levelErr := logs.Set(logctl.Setting{Level: cfg.LogLevel, DebugAreas: cfg.LogDebugAreas})
	// task 186: the journal, with the message alone and the real priority; text on a terminal
	base, err := logHandler(*logTarget, "occulited", slog.LevelDebug)
	if err != nil {
		return err
	}
	log := slog.New(logs.Handler(base))
	// the package-level slog calls and the log package (net/http's "TLS handshake error") go
	// through the same handler and the same level
	slog.SetDefault(log)
	area := func(name string) *slog.Logger { return logs.Logger(base, name) }
	if levelErr != nil {
		log.Warn("config: log_level or log_debug_areas is not valid; occulited logs at info", "err", levelErr)
	}
	if len(cfg.Stale) > 0 {
		// task 19 (D-54): the group-to-role mapping is gone, the role is the account's; the keys
		// are dropped at the next save of the file, and until then this line says they do nothing
		log.Warn("config: keys of an earlier version are ignored - a provider login takes the role of its account here", "keys", strings.Join(cfg.Stale, ", "), "path", *cfgPath)
	}

	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("listen address %q must be a loopback host:port", cfg.Listen)
	}
	if err := os.MkdirAll(cfg.StateDir, 0o750); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}

	metaPath := filepath.Join(cfg.StateDir, "meta.json")
	loaded, err := meta.Load(metaPath)
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	switch {
	case loaded.Fresh:
		log.Info("metadata: fresh store", "path", metaPath)
	case loaded.RecoveredFromBackup:
		log.Error("metadata: meta.json was unusable, RECOVERED FROM meta.json.bak — the last change may be lost", "path", metaPath, "revision", loaded.Doc.Revision)
	default:
		log.Info("metadata: loaded", "path", metaPath, "revision", loaded.Doc.Revision, "objects", len(loaded.Doc.Objects), "enums", len(loaded.Doc.Enums))
	}
	store, err := meta.New(loaded.Doc, meta.Saver(metaPath))
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	if loaded.Fresh {
		if err := meta.Save(metaPath, loaded.Doc); err != nil {
			return fmt.Errorf("metadata: cannot write %s: %w", metaPath, err)
		}
	}

	// B-102, D-67: the sessions survive a restart and a reboot, by hash, in a directory of their
	// own that no backup carries; those of a login the running mode does not offer are dropped
	// task 193: the favorites nodes follow the accounts (internal/favorites) - told by the auth
	// store after every write, and run once at start below
	var syncFavorites func()
	// task 262: the session lengths from occulited.json; an unusable value keeps the default
	idle, maxAge, lerr := cfg.Auth.SessionLimits()
	if lerr != nil {
		slog.Warn("auth: a session limit in the configuration is not a duration; the default stays", "err", lerr)
		idle, maxAge = 0, 0
	} else if (idle != 0 || maxAge != 0) && !auth.ValidSessionLimits(orDefault(idle, auth.DefaultIdleTimeout), orDefault(maxAge, auth.DefaultMaxAge)) {
		slog.Warn("auth: the session limits in the configuration are out of bounds; the defaults stay", "idle", idle, "max", maxAge, "bounds", auth.ErrSessionLimits)
		idle, maxAge = 0, 0
	}
	// task 307: the gate's token mirror lists, per token, the URL segments its addon:<id> scopes
	// open - the addon's id and what its lighttpd drop-in proxies
	users, err := auth.Open(cfg.StateDir, auth.Options{SessionDir: *sessionDir, SessionFile: auth.SessionStorePath(cfg.StateDir), RestoreMethods: sessionMethods(cfg.Auth), IdleTimeout: idle, MaxAge: maxAge, AddonSegments: system.Root(*rootDir).AddonIngressSegments, Changed: func() {
		if syncFavorites != nil {
			syncFavorites()
		}
	}})
	if err == nil && *sessionDir != "" && users.GateTokenMirror() == "" {
		log.Warn("auth: the gate's token mirror could not be made - API tokens open no addon page until the unit lists /run/occulite/gate-tokens (RuntimeDirectory=, ReadWritePaths=)")
	}
	if err == nil {
		// the box's own read token for programs running on it (addons): <state>/local-token
		if lerr := users.EnsureLocalToken(filepath.Join(cfg.StateDir, "local-token")); lerr != nil {
			slog.Warn("local token", "err", lerr)
		}
	}
	if err != nil {
		return fmt.Errorf("users: %w", err)
	}
	if users.SetupRequired() {
		log.Warn("no users yet: the web UI asks for the administrator password on first visit")
	}
	// B-241 (D-90): the outbound switches the file lacks are written once - on for a system that
	// ran before this version (its setup is done, or it was configured to run without a login),
	// off for a fresh one, whose welcome page asks
	if wrote, err := config.SettleOutbound(*cfgPath, &cfg, !users.SetupRequired() || cfg.Auth.EffectiveMode() == "off"); err != nil {
		log.Warn("config: the outbound switches could not be written; their values hold for this run and are written at the next start", "keys", strings.Join(cfg.OutboundUnset, ", "), "err", err)
	} else if len(wrote) > 0 {
		log.Info("config: outbound switches written explicitly", "keys", strings.Join(wrote, ", "), "firmware", cfg.Firmware.Enabled, "system_update", cfg.SystemUpdate.Enabled, "catalog_daily", cfg.Catalog.DailyOn())
	}
	favSync := &favorites.Sync{Store: store, Log: area("metadata")}
	syncFavorites = func() {
		// read under the sync's lock: an older list must not re-create a deleted account's node
		favSync.RunFrom(func() []favorites.Account {
			var accounts []favorites.Account
			for _, u := range users.Users() {
				accounts = append(accounts, favorites.Account{ID: u.ID, Name: u.Name})
			}
			return accounts
		})
	}
	if !users.SetupRequired() {
		syncFavorites()
	}
	// openccu-lite task 231: the four trust stores - the system's bundle on the box, occulited's
	// own base set, the OIDC and ACME anchors - one store behind the Trust stores page, the OIDC
	// settings and the ACME settings; occulited's own outbound clients trust its own store only
	trustStore := trust.Open(cfg.StateDir)
	trustStore.Paths, trustStore.Sys, trustStore.Log = trust.DefaultPaths(*rootDir), system.TrustWriter{}, area("trust")
	authAPI := &httpapi.AuthAPI{Store: users, ConfigFile: *cfgPath, Log: area("auth"), Trust: trustStore}
	trustStore.OnChange(func() {
		if err := authAPI.ApplyOIDCTrust(); err != nil {
			log.Warn("auth.oidc: the trust anchors could not be applied", "err", err)
		}
	})
	// task 307: the addon ingress scope - the installed addons a token may be given it for, and the
	// addon behind /addons/<segment>/ for the guard of the addon pages
	authAPI.Addons = system.Root(*rootDir).InstalledAddonNames
	authAPI.AddonForSegment = system.Root(*rootDir).AddonForIngressSegment
	// task 219: a program asks for access, an administrator approves it on Status
	authAPI.Pairing = &pairing.Manager{Minter: users, Log: area("auth"), Local: pairingLocal,
		Enabled: func() bool { return httpapi.PairingEnabled(*cfgPath) },
		AddonName: func(id string) (string, bool) {
			name, ok := system.Root(*rootDir).InstalledAddonNames()[id]
			return name, ok
		}}
	authAPI.CertFingerprint = certFingerprint(system.Root(*rootDir))
	// task 262: the name security keys are made on - <host>.<domain>, or the host alone without a domain
	authAPI.FQDN = func() string {
		sysRoot := system.Root(*rootDir)
		h, d := sysRoot.Hostname(), sysRoot.Domain()
		if d == "" {
			return h
		}
		return h + "." + d
	}
	authAPI.InitPublic(cfg.Auth.Public) // task 193: the Control app's public mode
	if cfg.Auth.Public.Enabled {
		log.Warn("auth.public: the Control app is public - anyone who reaches the web port operates the house", "account", cfg.Auth.Public.PublicAccount())
	}
	switch cfg.Auth.EffectiveMode() { // task 29
	case "off":
		authAPI.Off = true
		log.Warn("auth.mode is off: no login, every caller is an anonymous administrator")
	case "oidc":
		cfg.Auth.OIDC.Enabled = true
	}
	if o := cfg.Auth.OIDC; o.Enabled && !authAPI.Off {
		if o.Issuer == "" || o.ClientID == "" {
			log.Warn("auth.oidc: enabled without issuer or client_id - ignored")
		} else {
			authAPI.OIDC = oidc.New(oidc.Config{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: o.ClientSecret, UsernameClaim: o.UsernameClaim, Scopes: o.Scopes})
			authAPI.OIDCName = o.Name
			// task 230: the certificates trusted for the provider besides the system's pool
			if err := authAPI.ApplyOIDCTrust(); err != nil {
				log.Warn("auth.oidc: the trusted certificates could not be read; the system's pool alone applies", "err", err)
			}
			// task 19: the switch holds while the provider runs; the API re-reads it from the file
			authAPI.PasswordLoginOff = !cfg.Auth.PasswordLoginOn()
			log.Info("auth.oidc: external login enabled; accounts are matched by the user name claim", "issuer", o.Issuer, "name", o.Name, "username_claim", o.UsernameClaim, "password_login", !authAPI.PasswordLoginOff)
			if authAPI.PasswordLoginOff {
				log.Warn("auth.oidc: password login is switched off - with the provider down, `occulited auth password-login on` on the console is the way back in")
			}
		}
	}

	httpapi.Implementation = "occulited " + version
	httpapi.Commit = commit
	mux := http.NewServeMux()
	authAPI.Register(mux)
	metaAPI := &httpapi.MetaAPI{Store: store, Root: *rootDir, AfterImport: func() {
		if syncFavorites != nil {
			syncFavorites()
		}
	}}
	metaAPI.Register(mux)
	// task 17: as root the privileged operations happen in process; as occulite they go to
	// `occulited helper` (root, unix socket, enumerated operations)
	log.Info("privileges: " + usePrivilegeHelper(*helperSocket, cfg.StateDir))
	// task 101: occulited's own level behind the Log settings. The helper reads the same file at its
	// own start and takes every later change through its loglevel operation.
	own := &ownLogLevel{ctl: logs, cfgPath: *cfgPath, log: log, stored: func(s logctl.Setting) { cfg.LogLevel, cfg.LogDebugAreas = s.Level, s.DebugAreas }}
	if c, ok := system.Priv.(priv.Client); ok {
		own.helper = &c
	}
	root := system.Root(*rootDir)
	// task 192: /version tells a pairing client the key-server mode and the count of device keys
	metaAPI.HmIPPairing = func() radio.Pairing { return system.HmIPPairing(root) }
	// what a client can use (tasks 194, 195, 219): the capability object of /version
	// task 19: lite-rpc's streams per token or session, from occulited.json at the start
	streamsPerSession, ok := cfg.RPC.StreamsPerSessionLimit()
	if !ok {
		log.Warn("occulited.json: rpc.streams_per_session is out of bounds (1-16), the default stands", "value", cfg.RPC.StreamsPerSession, "default", streamsPerSession)
	}
	metaAPI.Capabilities = func() map[string]any {
		return httpapi.Capabilities(httpapi.PairingEnabled(*cfgPath), streamsPerSession)
	}
	// the addons' rc.d layer (task 187): the nav, the update check and - without systemd - the
	// read-only addon list; SystemdAddons runs the same scripts in a scope below
	scripts := system.AddonScripts{Root: root, HTTP: trustStore.HTTPClient(trust.StoreOcculited, 0)} // task 231: an addon's own update URL
	var lister httpapi.AddonLister = scripts
	var addonCtl httpapi.AddonController // 28.8: nil off systemd
	var installToken string              // openccu-lite B-274: POST /addons/install/local's credential
	// B-2: an addon's update check goes to occulited's own CGI route with a credential - the
	// daemon's own, minted for this process (task 66: the local token reads names alone now)
	if tok, err := users.MintEphemeral("occulited:update-check", auth.Scopes{auth.ScopeSystemRead}); err == nil {
		scripts.UpdateToken = tok
	} else {
		log.Warn("update check: no credential for the CGI route", "err", err)
	}
	// task 187: without systemd (a workstation, the development mode) no service is listed or
	// controlled and addons are not installed or removed; systemd replaces all of it below
	var services httpapi.ServiceManager = system.NoInit{}
	// task 186: the journal is the log (D-30); /var/log/messages of the busybox init is not read
	var logReader system.LogReader = system.JournalLog{Root: root}
	var journal *system.JournalLog
	var timers httpapi.TimerLister
	var manager httpapi.AddonManager        // nil = the addon routes answer 501
	var sa *system.SystemdAddons            // set on a systemd box; its start-time refreshes run once the catalogue exists
	var crashLoops *system.CrashLoops       // task 283: the crash-loop sampler and addon supervisor, on a systemd box
	early := &earlySwitches{path: *cfgPath} // task 119: the early start's switches in occulited.json
	// system commands run for real on a box; with --root pointing anywhere else (development)
	// they are logged, so a network form can never reconfigure the workstation
	var run system.Runner = system.ExecRunner
	var runStdin system.StdinRunner = system.ExecStdinRunner
	if *rootDir != "/" {
		run = system.DryRunner
		runStdin = system.DryStdinRunner
	}
	// the firewall's USERPORTS is composed from the manual list and the addons' opened ports (D-47)
	fwm := &system.FirewallManager{Root: root}
	if root.HasSystemd() {
		// the unit cache (B-83): the Services page's poll shows only the units that changed
		sd := system.SystemdServices{Root: root, SwitchFile: filepath.Join(cfg.StateDir, "unit-switch.json"), Cache: system.NewUnitCache()}
		services, timers = sd, sd
		// B-26: the Services page's switch is a runtime mask, and /run does not survive a boot -
		// so what the user switched off is stored on the userfs and applied again here, before
		// anything else looks at the unit list
		replayUnitSwitch(sd, log)
		sa = system.NewSystemdAddons(root, sd) // scripts in a scope, units take over (D-36)
		sa.Scripts.UpdateToken = scripts.UpdateToken
		// B-119: the nav and the update checker read addons through scripts (below), and an addon's
		// `info` is the addon's own code - it runs as the addon's user there too, not only through sa
		scripts.Credential = sa.Credential
		sa.APITokens = users // task 66: the addons' API tokens from their catalogue declarations
		sa.DefaultMode = cfg.Addons.DefaultMode
		sa.EarlyStart = early.On // task 119: the Addons page's switches, in occulited.json
		sa.Firewall = fwm        // an uninstall or a changed runtime block closes ports (D-47)
		// B-158: the addons seen keeping a process, so an empty unit of theirs reads Exited
		sa.Daemons = &system.AddonDaemons{Path: filepath.Join(cfg.StateDir, "addon-daemons.json")}
		if *rootDir == "/" {
			// the installer's output into the journal as well, identifier addon-install (D-59)
			sa.Journal = &journald.Writer{}
		}
		// D-36's default is confined now; what is already installed keeps what it runs as
		firstBootAddonPolicies(sa, cfg.StateDir, area("addons"))
		// openccu-lite task 283: the units' restart counters for the crash-loop warning, and the
		// addons that declare a daemon restarted when it ended (the rc.d stop and start)
		crashLoops = &system.CrashLoops{Systemd: sd, Supervised: sa.SupervisedDaemons, Paused: sa.Busy, Log: area("services"),
			Restart: func(ctx context.Context, unit string) error { _, err := sd.Control(ctx, unit, "restart"); return err }}
		manager = sa
		// the addon list comes from the same object on a systemd box: its running state is the
		// unit's, not a pid file most addons never write (B-29)
		lister = sa
		if problems := sa.RefreshAddonTokens(context.Background()); len(problems) > 0 {
			log.Warn("addon control tokens", "problems", problems)
		}
		addonCtl = sa.Tokens
		// openccu-lite B-274: /bin/install_addon outside occulited hands its archive to the install
		if *rootDir == "/" {
			if tok, err := system.WriteInstallToken(root); err != nil {
				log.Warn("install token: not written, /bin/install_addon installs on its own", "err", err)
			} else {
				installToken = tok
			}
			// occulited task 22: `occulited update` calls this API with the console's credential
			if tok, err := users.MintConsoleToken(); err != nil {
				log.Warn("console token: not minted, `occulited update` cannot reach the API", "err", err)
			} else if err := system.WriteConsoleToken(root, tok); err != nil {
				users.DropEphemeral(auth.ConsoleTokenName)
				log.Warn("console token: not written, `occulited update` cannot reach the API", "err", err)
			}
		}
		j := system.JournalLog{Root: root} // B-116: the kernel's lines on the wall clock by their boot's start
		journal, logReader = &j, j
		log.Info("init: systemd detected - services through systemctl, log through journald")
	} else {
		log.Warn("init: no systemd - development mode: services and addons are read-only (no init control, no install or removal)")
	}
	// D-35: an update from OpenCCU leaves /etc/config/homematic.regadom behind; on the first boot
	// with an empty store the names, rooms and functions in it become the store, once the addons
	// known to run here are named (ported addons keep their ReGa path, which the scan would
	// otherwise flag): an addon says it with its manifest (D-119), and the adapter manifests the
	// image carries say it for the addons whose authors ship none, unless the adapter itself says
	// the addon needs the ReGa - read before the first-boot step
	for _, id := range catalog.RegaFreeAdapterIDs(config.BundledManifestsDir) {
		system.RegaCompatible[id] = true
	}
	// the binary compatibility scan is its own once-per-box step (task 25): it must also run on
	// a box that has no ReGa database to import, or whose store is no longer empty
	enabledBefore := enabledAddons(root)
	disabledBinary := firstBootAddonABI(root, cfg.StateDir, area("addons"))
	firstBoot := firstBootImport(store, root, cfg.StateDir, area("metadata"), disabledBinary)
	// B-58: what the two scans disabled just now still has the unit the generator wrote while its
	// script was executable, and a start job addons.target queued for it
	settleFirstBootDisabled(sa, enabledBefore, area("addons"))
	if sa != nil && *rootDir == "/" {
		sa.ClearDisabledGhosts(context.Background()) // B-58: a box that disabled one with an earlier binary
	}
	// a store imported before the importer knew the CCU's built-in rooms and functions holds their
	// translation keys as names (roomBathroom): renamed in place at every start, which finds
	// nothing once it has run (B-79)
	if renamed, err := regaimport.TranslateBuiltinNodes(store); err != nil {
		log.Warn("metadata: the CCU's built-in room and function names could not be translated", "err", err, "renamed", len(renamed))
	} else if len(renamed) > 0 {
		log.Info("metadata: the CCU's built-in room and function names translated", "count", len(renamed), "nodes", strings.Join(renamed, ", "))
	}
	// a box with no ReGa database to import has no first-boot report at all, and the addons came
	// across on /usr/local all the same: give the Status page one that carries just this
	if firstBoot == nil && len(disabledBinary) > 0 {
		firstBoot = &httpapi.FirstBootImport{At: time.Now(), DisabledBinaryAddons: disabledBinary}
	}
	radioIfs := func() []interfaces.Interface {
		var list []struct{ Name, URL string }
		for _, i := range root.ReadRadio().Interfaces {
			list = append(list, struct{ Name, URL string }{i.Name, i.URL})
		}
		return interfaces.FromList(list)
	}
	var changeKey func(ctx context.Context, key string) error
	var setLogLevel func(ctx context.Context, iface string, level int) error
	// task 76: init(url, "") on one interface process, the unsubscribe of the Interfaces page
	var initInterface func(ctx context.Context, iface, url, id string) error
	if *rootDir == "/" {
		changeKey = func(ctx context.Context, key string) error { return interfaces.ChangeKey(ctx, radioIfs(), key) }
		setLogLevel = func(ctx context.Context, iface string, level int) error {
			return interfaces.SetLogLevel(ctx, radioIfs(), iface, level)
		}
		initInterface = func(ctx context.Context, iface, url, id string) error {
			return interfaces.Init(ctx, radioIfs(), iface, url, id)
		}
	}
	// B-195: the index is asked with the system's VERSION as the WebUI asks, a fetched bundle that
	// needs more is refused, and the last run is kept in the state dir across restarts
	fwClient := firmware.New(cfg.Firmware.Base)
	fwClient.HTTP = trustStore.HTTPClient(trust.StoreOcculited, 2*time.Minute) // task 231: eQ-3's servers
	fwClient.SystemVersion = root.ReadVersion().Version
	fw := firmware.NewService(fwClient, cfg.Firmware.Dir, radioIfs, log)
	fw.SystemVersion = fwClient.SystemVersion
	fw.Open(filepath.Join(cfg.StateDir, "firmware-state.json"))
	// the radio sampler: duty cycle and link per interface, once a minute, 500 samples of history
	sampler := &health.Sampler{Interfaces: radioIfs}
	// task 214: occulited's database file (bbolt, D-113) keeps the history across a restart - in
	// memory only, written at an interval, or at every round, by the product's default or the
	// setting; opened before the first poll, so the curves are there from the start
	storeLoc := cfg.Store.Location
	if err := datastore.ValidateLocation(storeLoc); err != nil {
		log.Warn("store: the location in occulited.json is not usable - the default on the userfs applies", "location", storeLoc, "err", err)
		storeLoc = ""
	}
	dataStoreMgr := &datastore.Manager{Path: datastore.FilePath(storeLoc, cfg.StateDir), Platform: root.ReadVersion().Platform, Log: log}
	dataStoreMgr.Register(sampler)
	// task 194: the state store - the last value, ts, lc and confirmed of every datapoint of the
	// chosen set, in the same file (bucket state); a value change is committed at once in
	// persistent mode, the timestamp-only refreshes at the interval
	devState := &devstate.Store{Log: area("radio"), Kick: dataStoreMgr.Kick}
	dataStoreMgr.RegisterEntries(devState)
	// task 195: the datapoint history - a ring of 500 rows per series of what the App's cards
	// draw, fed by every report the state store gets, bucket history of the same file
	dpHistory := &devstate.History{Kick: dataStoreMgr.Kick}
	dpHistory.SetList(cfg.Store.HistoryAdd, cfg.Store.HistoryRemove)
	dataStoreMgr.Register(dpHistory)
	devState.OnObserve = dpHistory.Record
	// task 229: a USB stick as the location takes a snapshot of ram-sync's file
	if datastore.IsUSB(storeLoc) {
		if err := datastore.ValidateFor(dataStoreMgr.Platform, cfg.Store.Mode, cfg.Store.SyncInterval, storeLoc); err != nil {
			log.Warn("store: the USB stick in occulited.json takes ram-sync's snapshot only - no copy is made", "location", storeLoc, "err", err)
		} else {
			dataStoreMgr.SetMirror(mirrorFor(root, storeLoc))
		}
	}
	dataStoreMgr.Start(cfg.Store.Mode, cfg.Store.SyncInterval)
	sampler.Polled = dataStoreMgr.Kick
	// task 75: the service messages - a sweep of every device's maintenance channel at start,
	// every 15 minutes and whenever the subscriber says an interface came back, and the events
	// in between, which the sampler reads too for the module's carrier sense and duty cycle
	serviceMsgs := &servicemsg.Store{Interfaces: radioIfs, Log: area("radio"), OnChange: httpapi.ServiceMessageWatchers.Changed}
	// one clock for both (task 194): a message found active at the start takes the state store's
	// last change of that value, kept across the restart
	serviceMsgs.Since = func(iface, address, dp string, value any) (time.Time, bool) {
		e, ok := devState.Get(iface, address, dp)
		if !ok || !devstate.Same(e.Value, value) {
			return time.Time{}, false
		}
		return e.LC, true
	}
	// the subscriber itself, inside occulited (D-115), with its callback listener on a second
	// loopback socket; the sampler and the store read its bus
	// task 79: the RPC trace - its lines go to the journal under the identifier rpc-trace at
	// debug, whatever occulited's own level; the switch lives in occulited.json
	traceHandler, err := logHandler(*logTarget, "rpc-trace", slog.LevelDebug)
	if err != nil {
		log.Error("rpc trace: no log handler", "err", err)
		traceHandler = base
	}
	tracer := rpctrace.New(rpctrace.Options{Log: slog.New(traceHandler), Store: &traceStore{path: *cfgPath}})
	// B-270: an init the daemon answered late but took (its handlers file says so) is a registration
	rpcSub := rpcsub.New(rpcsub.Config{Listen: cfg.RPC.CallbackListen, Interfaces: filepath.Join(*rootDir, "etc/config/InterfacesList.xml"), Log: area("radio"), Trace: tracer, Enrich: devState.Enrich,
		Taken: system.Root(*rootDir).HoldsRegistration})
	rpcSub.Attach(sampler, serviceMsgs, devState)
	// B-46: what the service-message store reads from a maintenance channel goes to the state
	// store and, where it differs, onto the bus - an acknowledged STICKY_UNREACH comes without an
	// event, and /state and the stream's clients said "still unreachable" until a restart
	serviceMsgs.Report = func(iface, channel string, values map[string]any, at time.Time) {
		devState.Reconcile(iface, channel, values, at, rpcSub.Publish)
	}
	// occulited task 13: the sampler makes the interface cards' events/min of the subscriber's counts
	sampler.Feed = rpcSub.Status
	// the daily addon update check (what checkAddonUpdates.sh did into a ReGa variable)
	// the update check runs the addon's update.cgi through occulited's own CGI route (not
	// lighttpd's gate), authenticated with the box's local token (B-2)
	updates := &addonupdates.Service{Lister: scripts, Checker: scripts, WebBase: "http://" + cfg.Listen}
	fw.SetEnabled(cfg.Firmware.Enabled)
	toggle := func(on bool) error {
		cfg.Firmware.Enabled = on
		return config.Save(*cfgPath, cfg)
	}
	// task 244: the catalogue's and the addon updates' daily check, one switch
	var catalogDaily atomic.Bool
	catalogDaily.Store(cfg.Catalog.DailyOn())
	updates.Daily = catalogDaily.Load
	var cat httpapi.Catalog
	var catSvc *catalog.Service // the same service, for the manifest fallback below
	if cfg.Catalog.Enabled && len(cfg.Catalog.URLs) > 0 {
		c := catalog.New(cfg.Catalog.URLs, httpapi.ArchName(), installAdapter{manager})
		c.HTTP = trustStore.HTTPClient(trust.StoreOcculited, 10*time.Minute) // task 231: GitHub and the catalogue
		c.TimingsFile = filepath.Join(cfg.StateDir, "catalog-timings.json")  // 30.2: the bar's pace
		c.CacheFile = filepath.Join(cfg.StateDir, "catalog-cache.json")      // D-119: the fetched manifests, stars, releases
		c.ImagesDir = filepath.Join(cfg.StateDir, "catalog-images")          // openccu-lite task 100: the manifests' icons and logos
		c.BundledManifests = config.BundledManifestsDir
		c.Daily = catalogDaily.Load
		cat, catSvc = c, c
	}
	// D-47, D-119: what an addon declares is its policy's block merged with its stored manifest's,
	// so a key this binary learned after the install still counts
	fwm.Declared = root.DeclaredRuntime
	if sa != nil {
		// D-119: a package without a manifest takes the catalogue's word - an adapter manifest, or
		// the manifest fetched for the page
		if catSvc != nil {
			sa.FallbackManifest = catSvc.ManifestAt
		}
		// openccu-lite B-288: every addon uid the system holds is in the uid registry before the
		// sweep below takes a gone addon's policy - its uid stays reserved for its reinstall
		if ids := sa.SeedAddonUIDs(); len(ids) > 0 {
			log.Info("addon uids: registered", "addons", ids)
		}
		// openccu-lite B-283: the policy files of an addon that is gone (an uninstall before this
		// binary, NEO Server switched off) go first, so the refreshes below see installed addons only
		if ids := sa.SweepStalePolicyFiles(); len(ids) > 0 {
			log.Info("addon policies: files of addons that are no longer installed removed", "addons", ids)
		}
		// a policy follows its stored manifest, and an installed addon without one adopts the
		// catalogue's adapter when there is one; the drop-in below is rendered from that
		if ids := sa.RefreshManifestRuntimes(); len(ids) > 0 {
			log.Info("addon policies: runtime blocks refreshed from the manifests", "addons", ids)
		}
		// task 94: <id>.needs, the start order the occu-addons generator reads at the next boot,
		// from what every addon declares now - the manifest's needs, else the stored block
		if ids := sa.RefreshAddonNeeds(); len(ids) > 0 {
			log.Info("addon policies: start order files renewed", "addons", ids)
		}
		// task 119: <id>.start, the early start, from the declarations and the user's switches
		if ids := sa.RefreshAddonStart(); len(ids) > 0 {
			log.Info("addon policies: early start files renewed", "addons", ids)
		}
		// D-52 / B-62: the state a confined addon keeps outside its three directories
		// (/usr/local/<id>, the entry's data_dirs) is chowned to it and goes on ReadWritePaths -
		// again at every start, so a box confined before this binary heals itself
		if ids := sa.RefreshDataDirs(); len(ids) > 0 {
			log.Info("addon policies: data directories taken over", "addons", ids)
		}
		// D-46 and whatever a later binary adds to a drop-in: the stored policies say what an
		// addon may do, the binary and the box say how that is spelled
		if ids := sa.RefreshPolicyDropIns(context.Background()); len(ids) > 0 {
			log.Info("addon policies: drop-ins renewed", "addons", ids)
		}
	}
	if catSvc != nil && *rootDir == "/" {
		go catSvc.Run(context.Background()) // the daily GitHub refresh of the latest releases (conditional requests)
	}
	// the release feed check (task 16): once a day, only on a real box
	var feed *sysupdate.Service
	if cfg.SystemUpdate.Feed != "" {
		feed = sysupdate.New(root, cfg.SystemUpdate.Feed, cfg.SystemUpdate.Enabled, log)
		feed.HTTP = trustStore.HTTPClient(trust.StoreOcculited, 30*time.Minute) // task 231: GitHub's releases
		if *rootDir == "/" {
			go feed.Run(context.Background()) // the daily check follows the setting (task 244)
		}
	}
	// task 37: mediola's NEO Server needs the ReGa; switched off with its own marker, once
	enabledBefore = enabledAddons(root)
	firstBootNeoServer(root, services, cfg.StateDir, log)
	settleFirstBootDisabled(sa, enabledBefore, area("addons"))
	// B-60: the CCU3 firmware's own crontab line for a NEO Server that is not installed
	if dropped, err := root.DropOrphanNeoWatchdog(); err != nil {
		log.Warn("crontab: the NEO Server's watchdog line could not be removed", "err", err)
	} else if dropped {
		log.Info("crontab: the NEO Server's watchdog line removed - the NEO Server is not installed, crond ran a missing program every five minutes", "crontab", "/usr/local/crontabs/root")
	}
	// task 250: the CCU's leftovers go once, after the first start has imported the names
	firstBootLeftovers(root, services, manager, store, cfg.StateDir, area("addons"))
	// task 35: the ACME certificate service - its state under <state>/acme, the flow through
	// lego, the live file through the helper's enumerated operation (system.CertInstaller)
	certSvc, err := acme.New(filepath.Join(cfg.StateDir, "acme"), area("acme"))
	if err != nil {
		return fmt.Errorf("certificate service: %w", err)
	}
	certSvc.Issuer = acme.LegoIssuer{UserAgent: "occulited/" + version}
	// task 231: the directory connection trusts occulited's store plus the ACME anchors - and,
	// task 232, must match the ACME pins where there are any; a lost handshake is recorded for the
	// Status page. A CA root kept in the settings before moves into the ACME store once
	certSvc.HTTP = func() *http.Client { return trustStore.HTTPClient(trust.PurposeACME, 2*time.Minute) }
	if pem := certSvc.TakeCARoot(); pem != "" {
		if _, err := trustStore.AddTo(context.Background(), trust.PurposeACME, []byte(pem), "", trust.OriginACMESettings); err != nil {
			log.Warn("acme: the settings' CA root could not move into the ACME trust store", "err", err)
		} else {
			log.Info("acme: the settings' CA root is in the ACME trust store now (System → Trust stores)")
		}
	}
	certSvc.Installer = system.CertInstaller{Root: root, Run: run, Systemd: root.HasSystemd()} // dry off a real box
	// task 102 (D-59): an attempt's lines and a firmware flash's go to the journal, one entry each
	// with the run's fields (internal/runlog); off a real systemd box they go to occulited's log
	var runJournal runlog.Sender
	if *rootDir == "/" && root.HasSystemd() {
		runJournal = &journald.Writer{}
	}
	certSvc.Journal = runJournal
	// a last.json of an older binary kept the lines: into the journal, once
	certSvc.MigrateLog()
	// task 145 (D-64): the recovery system's install log, left on the userfs by the update's
	// recovery boot, into the journal as identifier recovery; the file goes once it is in
	if runJournal != nil {
		go (&system.RecoveryImporter{Root: root, Journal: runJournal, Log: area("recovery")}).Import()
	}
	// the names proposed while none are stored, and taken by an ACME save without names
	certSvc.Suggest = func() []string { return acme.DefaultNames(root.Hostname(), root.Domain()) }
	// ACME names that are the default of the domain seen last follow a new one (another DHCP
	// lease): looked at now, for a change while occulited was down, then on the Network page's
	// poll, the Certificate page and every renewal check
	certSvc.HostDomain = func() (string, string) { return root.Hostname(), root.Domain() }
	if _, err := certSvc.FollowDomain(root.Hostname(), root.Domain()); err != nil {
		log.Warn("certificate: the ACME names could not follow the domain", "err", err)
	}
	// the redirect from the bare host name keeps its target the box's <host>.<domain> at the same
	// points: now, for a rename or a lease while occulited was down, then at every renewal check
	// (and on the pages' routes, in httpapi)
	httpsCfg := &system.HTTPSConfig{Root: root, Run: run, Systemd: root.HasSystemd()}
	followFQDN := func(hostname, domain string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, _, err := httpsCfg.FollowFQDN(ctx, hostname, domain, func(l string) { log.Info("https: " + l) }); err != nil {
			log.Warn("https: the bare host name redirect could not follow the box's name", "err", err)
		}
		// task 96: an HSTS clearing (max-age=0) whose deadline has passed ends at the same points
		if _, err := httpsCfg.ExpireHSTSClearing(ctx, func(l string) { log.Info("https: " + l) }); err != nil {
			log.Warn("https: the HSTS clearing could not be ended", "err", err)
		}
	}
	certSvc.Follow = followFQDN
	followFQDN(root.Hostname(), root.Domain())
	// task 41: the coprocessor firmware service - the flash orchestration around the helper's
	// one operation; a run interrupted by a restart of occulited is closed and the daemons
	// ensured up before the API is on
	radioFW := &system.RadioFirmware{Root: root, Services: services, Systemd: root.HasSystemd(), Health: sampler, StateDir: filepath.Join(cfg.StateDir, "radio-firmware"), Log: area("radio-firmware"), Journal: runJournal}
	radioFW.Load(context.Background())
	// task 129 phase 3: the connection per interface process; like the flash, a change cut off by
	// a restart of occulited is closed and the daemons started before the API is on
	radioConn := &system.RadioConnections{Root: root, Services: services, Systemd: root.HasSystemd(), Firmware: radioFW, StateDir: filepath.Join(cfg.StateDir, "radio-connections"), Log: area("radio-connections")}
	if *rootDir == "/" {
		radioConn.BidCosDevices = func(ctx context.Context) ([]system.BidCosDevice, error) {
			var rf []interfaces.Interface
			for _, i := range radioIfs() {
				if i.Name == "BidCos-RF" {
					rf = append(rf, i)
				}
			}
			devs, errs := interfaces.Devices(ctx, rf, 10*time.Second)
			out := []system.BidCosDevice{}
			for _, d := range devs {
				out = append(out, system.BidCosDevice{Address: d.Address, Type: d.Type})
			}
			if err := errs["BidCos-RF"]; err != nil {
				return out, err
			}
			return out, nil
		}
	}
	radioFW.ConnBusy = radioConn.Busy
	radioConn.Load(context.Background())
	// task 149 (D-103): local key mode - the HmIP network key on the box
	radioBusy := func() bool { return radioConn.Busy() || radioFW.Status().Running != nil }
	if crashLoops != nil {
		// openccu-lite B-307: the addon supervisor waits out a radio change as it waits out an
		// addon job - an addon's restart would pull the stopped radio daemons in mid-change
		crashLoops.Paused = system.AnyBusy(crashLoops.Paused, radioBusy)
	}
	hmipRestarts := &system.HmIPRestartLock{} // B-198: one lock for every hmipserver rewrite and restart
	localKey := &system.HmIPLocalKey{Root: root, Services: services, StateDir: filepath.Join(cfg.StateDir, "hmip-local-key"), Plan: radioConn.BootPlan, Busy: radioBusy, Restarts: hmipRestarts, Log: area("hmip-local-key")}
	// openccu-lite B-285: the snapshot before HmIP-RF moves to another module, and the way back
	radioConn.BeforeHmIPMove = localKey.SnapshotBeforeMove
	radioConn.HmIPIdentity = localKey.HasIdentity
	radioConn.LocalKeySnapshot = localKey.LocalKeySnapshotKept
	radioConn.MoveBackOffer = localKey.MoveBackOffer
	radioConn.CounterGap = localKey.CounterGap
	localKey.Conn = radioConn.ApplyRestore
	// openccu-lite task 275: the record of the last device import, read by the Interfaces page
	importRecord := &system.ImportRecord{Path: filepath.Join(cfg.StateDir, "devices-import.json"), Root: root, Plan: radioConn.BootPlan}
	if *rootDir == "/" {
		importRecord.Interfaces = radioIfs
	}
	if *rootDir == "/" {
		localKey.HmIP = func() (interfaces.Interface, bool) {
			for _, i := range radioIfs() {
				if i.Name == "HmIP-RF" {
					return i, true
				}
			}
			return interfaces.Interface{}, false
		}
	}
	localKey.Load()
	// task 154 (D-103, D-104): the HmIP device keys in sgtin.map
	deviceKeys := &system.HmIPDeviceKeys{Root: root, Services: services, StateDir: filepath.Join(cfg.StateDir, "hmip-device-keys"), HmIP: localKey.HmIP, Busy: radioBusy, Restarts: hmipRestarts, Log: area("hmip-device-keys"),
		Name: func(ref string) string {
			if o, err := store.GetObject(ref); err == nil {
				return o.Name
			}
			return ""
		}}
	// task 157 (D-105): the firewall - the rule file (converted from firewall.conf once), the
	// owners' automatic rules (the web server, SSH, the HmIP access points while HmIP-RF runs, the
	// addons' opened ports), reloaded when they or the local networks change
	fwRules := &system.FirewallRules{Root: root, StateDir: cfg.StateDir, Log: area("firewall"), Owners: func() map[string][]firewall.PortSpec {
		o := fwm.AddonOwners()
		if p := root.ClassicRPCOwner(); p != nil {
			o[firewall.OwnerRPC] = p
		}
		// task 175: the CA reaches port 80 while the certificate is ACME with HTTP-01
		if s := certSvc.Settings(); s.Mode == acme.ModeACME && s.Challenge == acme.ChallengeHTTP01 {
			o[firewall.OwnerACME] = firewall.ACMEPorts
		}
		if p := root.SSHOwner(); p != nil {
			o[firewall.OwnerSSH] = p
		}
		if plan, ok := radioConn.BootPlan(); ok && plan.HmIP != nil {
			o[firewall.OwnerHmIPAP] = firewall.HmIPAPPorts
		}
		return o
	}, Unknown: func() []string {
		// B-181: no plan yet (the radio detection has not run this boot) says nothing about
		// HmIP-RF; the access points' rules stay until it has
		if _, ok := radioConn.BootPlan(); !ok {
			return []string{firewall.OwnerHmIPAP}
		}
		return nil
	}}
	system.FirewallChanged = func(ctx context.Context) {
		if err := fwRules.Sync(context.WithoutCancel(ctx), false); err != nil {
			log.Warn("firewall: following a change failed", "err", err)
		}
	}
	if *rootDir == "/" {
		if err := fwRules.Start(context.Background()); err != nil {
			log.Error("firewall: the rules could not be loaded", "err", err)
		}
		go fwRules.Watch(context.Background(), 30*time.Second)
	}
	// task 69: the storage health panel - SMART through the helper's one read, the kernel log from
	// the journal, the daily write sample under <state>
	storage := &system.Storage{Root: root, StateFile: filepath.Join(cfg.StateDir, "storage-writes.json"), Systemd: root.HasSystemd(), Log: log}
	netTx := &system.NetTx{Root: root, Applier: system.NetApplier{Root: root, Iface: "eth0", Run: run, Systemd: root.HasSystemd()}, Override: system.DNSOverride{Path: filepath.Join(cfg.StateDir, "dns-override")}}
	// task 227: IPv6 per interface; a change still pending at the last stop is rolled back, then
	// the confirmed configuration applied (in the background: a DHCPv6 client may take seconds)
	ipv6Tx := &system.IPv6Tx{Applier: system.IPv6Applier{Root: root, Run: run, Systemd: root.HasSystemd()}, Store: system.IPv6Store{Path: filepath.Join(cfg.StateDir, "ipv6.json")}, PendingPath: filepath.Join(cfg.StateDir, "ipv6.pending.json"), Log: area("network")}
	if !root.HostManaged() {
		go func() {
			ipv6Tx.RecoverPending(context.Background())
			ipv6Tx.ApplyStored(context.Background())
		}()
	}
	if *rootDir == "/" {
		netTx.Applier.Run = nil // through the helper, with stdin support for resolvconf
		// B-167: a DHCP system's DNS override is a resolvconf record in /run, empty after a boot
		if !root.HostManaged() {
			go netTx.KeepDNSOverride(context.Background(), time.Minute, area("network"))
		}
	}
	// task 94: how long a user-initiated reboot takes here, for the page's countdown; systemctl
	// through the helper like the services, chronyc needs no privileges
	bootTiming := &bootexpect.Recorder{StateDir: cfg.StateDir, Root: *rootDir, Product: bootexpect.ProductOf(root.ReadVersion().Platform), Log: log,
		// openccu-lite B-193: a restore's marker also in /usr/local/tmp, which the restore at boot keeps
		Carry: &bootexpect.Carry{Path: root.Path(system.BootMarkerCarryFile), Place: root.CarryBootMarker, Remove: root.RemoveCarriedBootMarker}}
	if root.HasSystemd() {
		bootTiming.Systemctl = func(ctx context.Context, args ...string) ([]byte, error) { return run(ctx, "systemctl", args...) }
	}
	// task 93: the boot timeline of the Services page, from the same systemctl
	var bootChart *bootchart.Reader
	if root.HasSystemd() {
		bootChart = &bootchart.Reader{Root: *rootDir, Systemctl: bootTiming.Systemctl}
	}
	var bootSnapshots *bootchart.Store
	if bootChart != nil {
		bootSnapshots = &bootchart.Store{Dir: filepath.Join(cfg.StateDir, "boots"), Log: log}
	}
	// task 91: encrypted backups - the recovery key's state and the system's own identity in the
	// state directory; an identity carried across a restore (system.CarryFile) is adopted first
	backupStore := &backupcrypt.Store{Dir: cfg.StateDir}
	if adopted, err := root.AdoptCarriedBoxIdentity(backupStore); err != nil {
		log.Warn("backup encryption: carried identity", "err", err)
	} else if adopted {
		log.Info("backup encryption: the system's identity was carried across the restore")
	}
	sysAPI := &httpapi.SystemAPI{Trust: trustStore, Root: root, Services: services, Log: logReader, Journal: journal, Timers: timers, Addons: lister, Manager: manager, Nav: scripts, Firmware: fw, OnFirmwareToggle: toggle, Catalog: cat, CatalogDaily: catalogDaily.Load, OnCatalogDaily: func(on bool) error {
		catalogDaily.Store(on)
		cfg.Catalog.Daily = &on
		return config.Save(*cfgPath, cfg)
	}, OnSystemUpdateToggle: func(on bool) error {
		cfg.SystemUpdate.Enabled = on
		return config.Save(*cfgPath, cfg)
	}, HmIPServerDiagrams: func() bool { return cfg.HmIPServer.Diagrams }, OnHmIPServerDiagrams: func(on bool) error {
		cfg.HmIPServer.Diagrams = on // task 33: read by the radio steps at hmipserver's next start
		return config.Save(*cfgPath, cfg)
	}, WebBase: "http://" + cfg.Listen, MetaRecovered: loaded.RecoveredFromBackup, NetTx: netTx, IPv6: ipv6Tx, Run: run, RunStdin: runStdin, Firewall: fwm, Health: sampler, Updates: updates, FirstBoot: firstBoot, ChangeKey: changeKey, SetLogLevel: setLogLevel, InitInterface: initInterface, AddonCtl: addonCtl, InstallToken: installToken, Feed: feed, Cert: certSvc, RadioFirmware: radioFW, RadioConnections: radioConn, RadioBusy: radioBusy, HmIPLocalKey: localKey, HmIPDeviceKeys: deviceKeys, ImportRecord: importRecord, NamesImport: metaAPI.ImportNamesFromSBK, ConfirmTicket: users.RedeemConfirmed, FirewallRules: fwRules, HTTPS: httpsCfg, ClassicRPC: &system.ClassicRPCConfig{Root: root, Run: run, Systemd: root.HasSystemd()}, WiFi: wifiService(root, root.HasSystemd()), Power: &system.Power{Root: root, Systemd: root.HasSystemd(), Run: run}, RadioInterfaces: radioIfs, Storage: storage, Clock: &system.ClockCheck{Root: root}, BootTiming: bootTiming, Version: version, Commit: commit}
	// openccu-lite task 232: the server a purpose's pins are taken from - the OIDC issuer as the
	// configuration file has it now, the ACME directory the settings point at
	sysAPI.PinTarget = func(purpose string) string {
		switch purpose {
		case trust.PurposeOIDC:
			if c, err := config.Load(*cfgPath); err == nil {
				return c.Auth.OIDC.Issuer
			}
		case trust.PurposeACME:
			return certSvc.Settings().DirectoryURLFor()
		}
		return ""
	}
	sysAPI.BackupCrypt = backupStore
	sysAPI.DataStore = &dataStore{path: *cfgPath, stateDir: cfg.StateDir, m: dataStoreMgr, h: dpHistory, root: root}
	// openccu-lite task 86: the backup targets - the USB directory, NFS, SMB, SFTP
	sysAPI.BackupTargets = backupTargets(root, cfg.StateDir, backupStore)
	// openccu-lite task 228: the SMB and NFS shares of System → Storage
	sysAPI.Shares = shareManager(root, cfg.StateDir, sysAPI.BackupTargets)
	sysAPI.Shares.Uses = sysAPI.ShareUses // the journal's copies and the backup targets on a share
	// openccu-lite task 146: an addon whose .nobackup directories hold nothing but the tag lost
	// its program files to a restore (tar --exclude-tag keeps the directory and the tag); at this
	// start they are logged, the Addons page lists them with Reinstall, the state file keeps the
	// dismissals
	payload := &system.PayloadRecord{Root: root, Path: filepath.Join(cfg.StateDir, system.PayloadFile)}
	sysAPI.AddonPayload = payload
	if list, err := lister.ListAddons(context.Background()); lister != nil && err == nil {
		sysAPI.MarkPayloadMissing(list)
		var missing []string
		for _, ad := range list {
			if ad.PayloadMissing {
				missing = append(missing, ad.ID)
			}
		}
		if len(missing) > 0 {
			log.Warn("addons: program files missing - a backup carries no .nobackup directory; reinstall them from the catalogue (Addons page)", "addons", missing)
		}
	}
	sysAPI.BootChart = bootChart
	sysAPI.BootSnapshots = bootSnapshots
	sysAPI.OcculitedLog = own
	sysAPI.Legacy = &legacySwitches{path: *cfgPath} // task 125: the legacy session's switches, in occulited.json
	sysAPI.Early = early                            // task 119: the early start's switches, beside them
	// task 81: the Status page's warnings, evaluated here (every five minutes, below, and on every
	// read) so a silence ends when its warning clears; the silences in <state>/warnings.json
	sysAPI.CrashLoops = crashLoops
	warnTracker := sysAPI.WarningTracker(filepath.Join(cfg.StateDir, "warnings.json"), log)
	if crashLoops != nil {
		// a loop that begins or ends is on the Status page and the LED at once, not after the
		// tracker's five minutes
		crashLoops.OnChange = func() { warnTracker.Evaluate(context.Background()) }
	}
	// task 95: the status LED - the controller owns the RGB LED once the box is up; led.json in the
	// state directory, the frames through the helper
	ledCtl := &led.Controller{Root: root, File: filepath.Join(cfg.StateDir, "led.json"), Log: area("led"), Src: ledSources(services, warnTracker, feed, updates, ledInternetTargets(cfg.Catalog, certSvc), sampler)}
	sysAPI.LED = ledCtl
	sysAPI.RPC, sysAPI.ServiceMessages = rpcSub, serviceMsgs // task 75
	// task 77: lite-rpc - the request paths and the stream on the bus, always on (D-117); streams
	// re-check their credential through the auth API
	liteSvc := literpc.New(literpc.Config{Sub: rpcSub, Log: area("rpc"), Trace: tracer, PerSubject: streamsPerSession,
		// B-46: a write to a maintenance channel that passed through here (the acknowledgement of
		// a sticky message) is read back a second later, as no event will tell
		Wrote: func(iface string, c literpc.Call) {
			if device, ok := maintenanceWrite(c); ok {
				serviceMsgs.RecheckSoon(iface, device, time.Second)
			}
		}})
	sysAPI.LiteRPC, sysAPI.Revalidate = liteSvc, authAPI.SessionOf
	sysAPI.State = devState    // task 194: GET /api/rpc/v1/state
	sysAPI.History = dpHistory // task 195: GET /api/rpc/v1/history
	// task 218: a configured HB-RF-ETH that is not connected is tried again through the radio
	// hotplug (its unit runs as root), which re-plans the radio stack once the board answers
	hbWatch := &radio.HBRFETHWatch{Root: *rootDir, Log: area("radio"), Start: func(ctx context.Context) error {
		_, err := run(ctx, "systemctl", "start", "--no-block", "occu-radio-hotplug.service")
		return err
	},
		// B-218: after the kernel had the board back, the daemons on it are asked, and restarted
		// only when they do not have their module
		Healthy: hbrfethHealthy(*rootDir, radioIfs), Restart: hbrfethRestart(*rootDir, services)}
	sysAPI.HBRFETH = hbWatch
	// task 180: the heating groups through hmipserver's group pages, with a session of occulited's own
	groupSession := hmgroups.NewSession()
	sysAPI.LAN = &eq3disc.Client{} // task 220: one client, so finds and writes take turns
	sysAPI.Groups = &httpapi.GroupsAPI{Client: &hmgroups.Client{Base: fmt.Sprintf("http://127.0.0.1:%d", radio.HMServerPort(*rootDir)), Session: groupSession}, Session: groupSession, Meta: store, Log: area("rpc"), Interface: "VirtualDevices"}
	sysAPI.Public = authAPI
	sysAPI.PairingFeed = authAPI                       // occulited B-53: GET /stream's topic pairing
	sysAPI.ConsoleResets = authAPI.Store.ConsoleResets // occulited task 14: the Status page's notice
	sysAPI.RPCTrace = tracer                           // task 79
	sysAPI.Names = func(ref string) (string, []string, bool) {
		o, err := store.GetObject(ref)
		if err != nil || o == nil {
			return "", nil, false
		}
		return o.Name, o.Enums, true
	}
	sysAPI.Register(mux)
	// the HTTP-01 route: lighttpd hands /.well-known/acme-challenge/ here (deploy/lighttpd/
	// occulited.conf); no session, tokens of an order in flight only
	mux.Handle(acme.ChallengePrefix, certSvc.HTTP01)
	// task 18: addon CGIs (lighttpd proxies /addons/<name>/*.cgi here) run as the addon's user
	cgiCred := func(name string) *priv.Credential {
		sa, ok := manager.(*system.SystemdAddons)
		if !ok {
			return nil
		}
		return sa.Credential(name) // the same credential its rc.d script gets (B-119)
	}
	// B-120: the static files too, as occulite and never through a link out of the addon's tree
	mux.Handle("/addons/", authAPI.RequireSession(system.CGIRunner{Root: root, Credential: cgiCred, Static: system.AddonStatic{Root: root}}))
	// B-51: the bare /addons is the shell's Addons page, not the addon directory - without this
	// exact pattern the mux answered it with a 307 to /addons/, which lighttpd then refused
	// task 165 (D-108): occulited announces the system over SSDP and serves the UPnP description
	// that ssdpd and /www/upnp/basic_dev.cgi used to. The path is OpenCCU's, so a cached LOCATION
	// and NetFinder still find it; no session - it is the same information the announcement
	// carries over the air. Registered before the shell's catch-all, which answered it with the
	// UI page until now.
	// B-199: the serial is read again until the radio detection, which runs beside occulited at
	// boot, has written /var/board_sgtin or /var/board_serial; one Identity for both responders
	boardID := &ssdp.Identity{Root: *rootDir, Hostname: root.Hostname()}
	// B-245: the first announcement waits for the detection's files (up to 15 s), and an identity
	// that settles later is announced again with a byebye for the host-name one
	ssdpDev := ssdp.Device{SerialFunc: boardID.Serial, Settled: boardID.Settled, Hostname: root.Hostname(),
		Server: "Linux UPnP/1.0 openccu-lite/" + root.ReadVersion().Version,
		Presentation: ssdpPresentation(root, func() []string {
			if st := certSvc.Status(); st.Current != nil {
				return st.Current.Names
			}
			return nil
		})}
	mux.HandleFunc(ssdp.DescriptionPath, ssdpDev.Describe)
	mux.Handle("/addons", ui.Handler())
	mux.Handle("/", ui.Handler())
	started := time.Now()
	mux.HandleFunc("GET /api/system/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		rv := root.ReadVersion() // D-44: the release identity for the shell's top bar, before login
		fmt.Fprintf(w, `{"ok":true,"version":%q,"commit":%q,"release":%q,"base":%q,"uptime_s":%d,"meta":{"revision":%d,"recovered":%t}}`+"\n",
			version, commit, rv.Full(), rv.Version, int(time.Since(started).Seconds()), store.Revision(), loaded.RecoveredFromBackup)
	})

	samplerCtx, stopSampler := context.WithCancel(context.Background())
	defer stopSampler()
	// task 214: written at the interval or per round until the stop, then a last time
	storeCtx, stopStore := context.WithCancel(context.Background())
	defer stopStore()
	go dataStoreMgr.Run(storeCtx)
	rpcDone := make(chan struct{}) // closed when the subscriber has deregistered at the daemons
	if *rootDir != "/" {
		close(rpcDone)
	}
	if *rootDir == "/" {
		go sampler.Run(samplerCtx)
		go updates.Run(samplerCtx)
		go func() { // task 75: the subscriber - registered at the daemons until the shutdown
			defer close(rpcDone)
			if err := rpcSub.Run(samplerCtx); err != nil {
				log.Error("rpc: the callback listener could not start; the pages poll as before", "err", err)
			}
		}()
		go serviceMsgs.Run(samplerCtx) // and the sweep that covers what events miss
		// task 194: the state store's sweep of what is not confirmed, paced, when an interface is up
		go devState.RunSweeps(samplerCtx, stateSource{ifs: radioIfs}, rpcSub.Publish, 0)
		go hbWatch.Run(samplerCtx) // task 218
		if crashLoops != nil {
			go crashLoops.Run(samplerCtx) // task 283
		}
	}
	if *rootDir == "/" {
		// task 163: the eQ-3 discovery on UDP 43439, in place of eq3configd. Only "who are you";
		// the network is configured on the Network page and nowhere else, so every other opcode
		// gets the NAK. The version is the lite image's where the system has one: the type
		// already says -lite, and naming OpenCCU's would claim a firmware this system does not run.
		rv := root.ReadVersion()
		disc := &eq3disc.Responder{Device: eq3disc.Device{SerialFunc: boardID.Serial, Version: discoveryVersion(rv)}, Log: area("discovery")}
		go func() {
			if err := disc.Run(samplerCtx); err != nil {
				log.Error("discovery: UDP 43439 could not be opened; apps will not find this system by broadcast", "err", err)
			}
		}()
		// task 165: the SSDP announcements and the answers to an M-SEARCH, in place of ssdpd.
		// Not under a development root: it would join a multicast group and announce a system
		// that is not there.
		resp := &ssdp.Responder{Device: ssdpDev, Log: area("ssdp")}
		go func() {
			if err := resp.Run(samplerCtx); err != nil {
				log.Error("ssdp: the announcements could not start; the system is not found over UPnP", "err", err)
			}
		}()
	}
	go certSvc.Run(samplerCtx) // the renewal timer (twice a day, below 30 days)
	go warnTracker.Run(samplerCtx)
	if m := cfg.MQTT; m.Enabled && m.Broker != "" {
		pub := &mqttpub.Publisher{Config: mqttpub.Config{Broker: m.Broker, Username: m.Username, Password: m.Password, Prefix: m.Prefix, ClientID: m.ClientID}, Store: store, Log: area("metadata")}
		go pub.Run(samplerCtx)
	}
	go func() {
		for range time.Tick(5 * time.Minute) {
			users.Sweep()
		}
	}()
	fwCtx, fwStop := context.WithCancel(context.Background())
	defer fwStop()
	go fw.Run(fwCtx)
	// the owner duty of the metadata store: keep `orphaned` in step with what the interfaces
	// report (docs/meta-format.md). Every ten minutes, and never on an interface that is silent.
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-fwCtx.Done():
				return
			case <-t.C:
				var list []struct{ Name, URL string }
				for _, i := range root.ReadRadio().Interfaces {
					list = append(list, struct{ Name, URL string }{i.Name, i.URL})
				}
				present, errs := interfaces.Addresses(fwCtx, interfaces.FromList(list), 20*time.Second)
				for name, err := range errs {
					log.Debug("reconcile: interface silent", "interface", name, "err", err)
				}
				if n, err := store.Reconcile(present); err != nil {
					log.Warn("reconcile failed", "err", err)
				} else if n > 0 {
					log.Info("reconcile: orphaned flags updated", "objects", n)
				}
			}
		}
	}()
	// every request's context ends when occulited stops (B-151, shutdown.go)
	srv, endRequests := newHTTPServer(cfg.Listen, logRequests(area("http"), authAPI.Middleware(mux)))
	defer endRequests()
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Info("occulited started", "version", version, "commit", commit, "listen", cfg.Listen, "state", cfg.StateDir)
	bootTiming.MarkListening()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	storage.Start(ctx)     // the daily write sample, whether or not anybody opens the Status page
	go ledCtl.Run(ctx)     // task 95: the status LED
	go bootTiming.Run(ctx) // finishes the record of the reboot before this boot, if one was marked
	if bt, sh := sysAPI.BackupTargets, sysAPI.Shares; bt != nil && sh != nil {
		// task 228: task 86's NFS/SMB targets become shares, before either renders units
		migrateShares(ctx, bt, sh, log)
	}
	if bt := sysAPI.BackupTargets; bt != nil {
		// task 86: /run is empty after a boot - the mount targets' units again, then the watchdog
		go func() {
			for id, err := range bt.ApplyAll(ctx) {
				log.Warn("backup targets: the mount units were not written", "target", id, "err", err)
			}
			bt.Watch(ctx, time.Minute)
		}()
	}
	if sh := sysAPI.Shares; sh != nil {
		// task 228: the same for the shares
		go func() {
			for id, err := range sh.ApplyAll(ctx) {
				log.Warn("shares: the mount units were not written", "share", id, "err", err)
			}
			sh.Watch(ctx, time.Minute)
		}()
	}
	if bootSnapshots != nil {
		// task 93: this boot's timeline is kept once it has finished and the clock can be trusted
		go bootSnapshots.Record(ctx, bootChart, func(c context.Context) bool { return bootexpect.ClockTrusted(c, *rootDir, nil) }, 0, 0)
		// openccu-lite B-249, occulited B-34: a boot whose network does not work 3 minutes in (no
		// carrier, the LED's no-network, or the Pi 3's LAN9514 missing) gets a record of it
		if id := root.BootID(); id != "" {
			go (&linkwatch.Watcher{Root: *rootDir, Uptime: root.Uptime, Other: func() bool { return wifiCarrier(*rootDir) },
				Kernel: func(context.Context) []string {
					var lines []system.LogLine
					if journal != nil {
						lines, _ = journal.Read(system.LogQuery{Kernel: true, Boot: "0", Limit: 5000})
					}
					if len(lines) == 0 {
						lines, _ = system.Dmesg{Root: root}.Read(system.LogQuery{Kernel: true, Limit: 5000})
					}
					out := make([]string, 0, len(lines))
					for _, l := range lines {
						out = append(out, l.Time+" "+l.Message)
					}
					return out
				},
				Log: func(context.Context) []string {
					if journal == nil {
						return nil
					}
					var out []string
					for _, u := range []string{"occu-lan-reset.service", "occu-network.service"} {
						lines, _ := journal.Read(system.LogQuery{Unit: u, Boot: "0", Limit: 200})
						for _, l := range lines {
							out = append(out, l.Time+" "+l.Message)
						}
					}
					return out
				},
				Save: func(c linkwatch.Capture) error {
					err := bootSnapshots.SaveNetwork(id, c)
					if err != nil {
						log.Warn("link watch: the no-link record was not written", "err", err)
					} else {
						log.Warn("link watch: the network does not work since the boot - a record of it is kept with the boot", "reasons", strings.Join(c.Reasons, ","), "interface", c.Interface, "uptime_s", c.UptimeS, "final", c.Final, "carrier_at_s", c.CarrierAtS)
					}
					return err
				}}).Run(ctx)
		}
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		log.Info("occulited stopping")
		ledCtl.Stop() // the shutdown pattern: a box going down never looks fine
		stopHTTPServer(srv, endRequests, shutdownLimit, log, liteSvc.Wait)
		users.Close() // the sessions' last-seen times the store does not have yet (B-102)
		// task 75: the subscriber deregisters at the daemons (init(url, "")), a few loopback
		// calls; a daemon that does not answer is left with the fixed entry, which the next
		// start overwrites
		stopSampler()
		select {
		case <-rpcDone:
		case <-time.After(5 * time.Second):
			log.Warn("occulited stopping: the subscriber's deregistration did not finish in time")
		}
		// task 214: the history's last rows into the database, once nothing appends any more
		stopStore()
		select {
		case <-dataStoreMgr.Done():
		case <-time.After(5 * time.Second):
			log.Warn("occulited stopping: the database was not written and closed in time")
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// sessionMethods are the login methods the authentication mode offers, whose stored sessions a
// start restores (B-102): a password login in `local` and `oidc` (the login route takes local
// accounts in both), the provider's where the start enables it (run: mode `oidc` or
// `oidc.enabled`, with an issuer and a client id), none with authentication off - so switching
// the mode off and on again restores nothing.
// orDefault is d, or def when d is zero.
func orDefault(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}

func sessionMethods(a config.AuthConfig) []string {
	mode := a.EffectiveMode()
	if mode == "off" {
		return nil
	}
	methods := []string{auth.MethodPassword, auth.MethodPasskey} // task 262: a passkey is a local login too
	if (mode == "oidc" || a.OIDC.Enabled) && a.OIDC.Issuer != "" && a.OIDC.ClientID != "" {
		methods = append(methods, auth.MethodOIDC)
	}
	return methods
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if !strings.HasSuffix(r.URL.Path, "/events/sse") {
			log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

// Unwrap lets http.ResponseController reach the server's own writer through this wrapper: the
// WebSocket upgrade's Hijack and the streams' write deadlines (task 77) need it.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// repeatable is a flag given more than once.
type repeatable []string

func (r *repeatable) String() string     { return strings.Join(*r, ",") }
func (r *repeatable) Set(v string) error { *r = append(*r, v); return nil }

const tokenUsage = "usage: occulited token <name> [--scope SCOPE]... [--role admin|user|led] [--expires DATE|DURATION] [--ip RANGE]... [--state-dir DIR]"

// token is the console way to an API token for headless provisioning (task 66): `occulited
// token <name> --scope meta:read --scope system:read` prints the secret - once, to stdout,
// nothing else there. --role is the alias of before the scopes (admin = Full access, user =
// the read scopes, led = led), taken when no --scope is given; --expires is a date
// (2027-01-01, or RFC 3339) or a duration (30d, 12h); --ip an address or a CIDR range the token
// is accepted from, more than once for more than one.
func token(args []string) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "/usr/local/etc/occulite", "state directory")
	role := fs.String("role", "", "admin, user or led: the scopes that role stands for (an alias; --scope is the way)")
	expires := fs.String("expires", "", "a date (2027-01-01 or RFC 3339) or a duration (30d, 12h) after which the token ends")
	var scopes, ips repeatable
	fs.Var(&scopes, "scope", "a scope the token carries (repeatable): "+strings.Join(auth.Scopes(auth.Grantable).Strings(), " ")+" or * for Full access")
	fs.Var(&ips, "ip", "an address or CIDR range the token is accepted from (repeatable)")
	var flags []string
	name := ""
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else if name == "" {
			name = args[i]
		} else {
			return errors.New(tokenUsage)
		}
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if name == "" {
		return errors.New(tokenUsage)
	}
	opt := auth.TokenOptions{IPs: ips}
	switch {
	case len(scopes) > 0:
		var err error
		if opt.Scopes, err = auth.ParseScopes(scopes); err != nil {
			return err
		}
	case *role != "":
		if opt.Scopes = auth.RoleScopes(auth.Role(*role)); opt.Scopes == nil {
			return errors.New("--role must be admin, user or led")
		}
	default:
		return errors.New("a token needs at least one --scope (or --role as the alias); " + tokenUsage)
	}
	if *expires != "" {
		t, err := parseExpiry(*expires, time.Now())
		if err != nil {
			return err
		}
		opt.Expires = &t
	}
	secret, err := auth.ConsoleCreateToken(*stateDir, name, opt)
	if errors.Is(err, auth.ErrSetupRequired) {
		return fmt.Errorf("no user exists in %s yet; set up the first administrator (in the web interface, or with `occulited passwd <user>`) before creating a token", *stateDir)
	}
	if err != nil {
		return err
	}
	fmt.Println(secret)
	extra := ""
	if opt.Expires != nil {
		extra += ", expires " + opt.Expires.UTC().Format(time.RFC3339)
	}
	if len(opt.IPs) > 0 {
		extra += ", from " + strings.Join(opt.IPs, " ")
	}
	fmt.Fprintf(os.Stderr, "token %s (%s%s) created in %s; it is not shown again\n", name, strings.Join(opt.Scopes.Strings(), " "), extra, *stateDir)
	return nil
}

// parseExpiry reads --expires: RFC 3339, a date, or a duration with d for days.
func parseExpiry(v string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		return t, nil
	}
	if strings.HasSuffix(v, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n > 0 {
			return now.Add(time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return now.Add(d), nil
	}
	return time.Time{}, fmt.Errorf("--expires %q: a date (2027-01-01), an RFC 3339 time or a duration (30d, 12h)", v)
}

// passwd is the console recovery path (D-28): `occulited passwd <user> [--state-dir DIR]` sets or
// resets a password by editing users.json directly; a running daemon picks the change up.
func passwd(args []string) error {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "/usr/local/etc/occulite", "state directory")
	// flag stops at the first positional; accept `passwd <user> --state-dir X` as well as
	// `passwd --state-dir X <user>` by moving the user name to the end.
	var flags []string
	name := ""
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else if name == "" {
			name = args[i]
		} else {
			return errors.New("usage: occulited passwd <user> [--state-dir DIR]")
		}
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if name == "" {
		return errors.New("usage: occulited passwd <user> [--state-dir DIR]")
	}
	var pw string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "New password for %s: ", name)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, "Repeat: ")
		c, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		if string(b) != string(c) {
			return errors.New("passwords do not match")
		}
		pw = string(b)
	} else {
		// piped: first line is the password (for scripted recovery)
		var line string
		if _, err := fmt.Fscanln(os.Stdin, &line); err != nil {
			return errors.New("read password from stdin")
		}
		pw = line
	}
	if err := auth.ConsoleSetPassword(*stateDir, name, pw); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "password of %s set (users.json in %s)\n", name, *stateDir)
	return nil
}

// authCmd is the console side of the password-login switch (task 19, D-53): `occulited auth
// password-login on|off [--config PATH]` writes auth.oidc.password_login into occulited.json. `on`
// is the break-glass: with the switch off and the provider down, nobody reaches the web interface,
// and this - with `occulited passwd <user>` for a password - is the way back in. Run as root on
// the box, like passwd; the file keeps its owner. The running daemon reads the file again before
// its next password login, so no restart is needed.
func authCmd(args []string) error {
	const usage = "usage: occulited auth password-login on|off [--config PATH]"
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	cfgPath := fs.String("config", "/usr/local/etc/occulite/occulited.json", "configuration file")
	var flags, words []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			words = append(words, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(words) != 2 || words[0] != "password-login" || (words[1] != "on" && words[1] != "off") {
		return errors.New(usage)
	}
	cfg, err := config.SetPasswordLogin(*cfgPath, words[1] == "on")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "password login %s (%s, mode %s)\n", words[1], *cfgPath, cfg.Auth.EffectiveMode())
	if words[1] == "on" && cfg.Auth.EffectiveMode() == "oidc" {
		fmt.Fprintln(os.Stderr, "a running occulited applies it at once; an account without a password gets one with `occulited passwd <user>`")
	}
	return nil
}

// webauthnCmd is the console side of the passkeys (openccu-lite task 262): `occulited webauthn
// list <user>` shows an account's keys, `occulited webauthn remove <user> <id>|--all` removes one
// or all of them and ends the account's stored sessions. `occulited admin reset-auth` (task 14)
// is the whole way back in. Run as root on the system, like
// passwd; a running occulited follows the changed users.json at the account's next request.
func webauthnCmd(args []string) error {
	const usage = "usage: occulited webauthn list <user> | remove <user> <id>|--all   [--state-dir DIR]"
	fs := flag.NewFlagSet("webauthn", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "/usr/local/etc/occulite", "state directory")
	all := fs.Bool("all", false, "remove every key of the account")
	var flags, words []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if args[i] != "--all" && args[i] != "-all" && !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			words = append(words, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(words) == 0 {
		return errors.New(usage)
	}
	switch {
	case len(words) == 2 && words[0] == "list":
		keys, err := auth.ConsoleWebAuthnKeys(*stateDir, words[1])
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Fprintf(os.Stderr, "%s has no security key\n", words[1])
			return nil
		}
		for _, k := range keys {
			kind := "cannot sign in (a second-factor key from before)"
			if k.Passkey {
				kind = "passkey"
			}
			last := "never used"
			if k.LastUsed != nil {
				last = "last used " + k.LastUsed.UTC().Format(time.RFC3339)
			}
			fmt.Printf("%s\t%s\t%s\tcreated %s\t%s\n", k.ID, k.Name, kind, k.Created.UTC().Format(time.RFC3339), last)
		}
		return nil
	case words[0] == "remove" && ((len(words) == 3 && !*all) || (len(words) == 2 && *all)):
		id := ""
		if !*all {
			id = words[2]
		}
		n, err := auth.ConsoleRemoveWebAuthn(*stateDir, words[1], id)
		if err != nil {
			return err
		}
		if n == 0 {
			fmt.Fprintf(os.Stderr, "%s has no security key; nothing changed\n", words[1])
			return nil
		}
		fmt.Fprintf(os.Stderr, "%d key(s) of %s removed (users.json in %s); the account's sessions are ended\n", n, words[1], *stateDir)
		return nil
	}
	return errors.New(usage)
}

// ledInternetTargets is what the LED's no-internet check connects to (task 95, decided in D-67):
// hosts the box talks to anyway instead of a third party - the addon catalogue index's (the URLs
// are the configuration's, fixed while occulited runs), then the ACME directory's while the
// certificate mode is ACME, read at every check since that setting changes at runtime.
func ledInternetTargets(cat config.CatalogConfig, certs *acme.Service) func() []string {
	return func() []string {
		var urls []string
		if cat.Enabled {
			urls = append(urls, cat.URLs...)
		}
		if certs != nil {
			if s := certs.Settings(); s.Mode == acme.ModeACME {
				urls = append(urls, s.DirectoryURLFor())
			}
		}
		return led.InternetTargets(urls...)
	}
}

// ledSources are the status LED's inputs besides the files: the units (systemd boxes only), the
// Status page's warnings nobody silenced, the release feed, the addon update check and the hosts the
// no-internet check connects to.
func ledSources(services httpapi.ServiceManager, tracker *httpapi.WarningTracker, feed *sysupdate.Service, updates *addonupdates.Service, internet func() []string, sampler *health.Sampler) led.Sources {
	src := led.Sources{Warnings: tracker.Unsilenced, WarningIDs: []string{}, InternetTargets: internet}
	if sampler != nil {
		// openccu-lite task 316: the radio load as the sampler last saw it - the Status page's and
		// the sparklines' sampling, no poll of the LED's own
		src.Radio = func() []led.RadioLoad {
			var out []led.RadioLoad
			for _, ri := range sampler.Status().Interfaces {
				l := led.RadioLoad{Interface: ri.Interface, DutyCycle: ri.DutyCycle}
				if ri.CarrierSense != nil {
					l.CarrierSense, l.HasCS = *ri.CarrierSense, true
				}
				out = append(out, l)
			}
			return out
		}
	}
	for _, s := range tracker.Sources {
		src.WarningIDs = append(src.WarningIDs, s.IDs...)
	}
	if u, ok := services.(interface {
		UnitRows(context.Context) ([]system.UnitRow, error)
	}); ok {
		src.Units = u.UnitRows
	}
	if feed != nil {
		src.SystemUpdate = func() string {
			if av := feed.State().Available; av != nil && av.Newer {
				return av.Version
			}
			return ""
		}
	}
	if updates != nil {
		src.AddonUpdates = func() int { return updates.State().Available }
	}
	return src
}

// installAdapter lets the catalogue hand an archive to the firmware's installer.
type installAdapter struct{ m httpapi.AddonManager }

func (i installAdapter) Install(ctx context.Context, r io.Reader) (any, error) {
	if i.m == nil {
		return nil, errors.New("addons are not installed without systemd (development mode)")
	}
	// the catalogue knows which addon the archive is; the install's journal entries name it
	return i.m.Install(system.WithAddonID(ctx, catalog.AddonID(ctx)), r)
}

// replayUnitSwitch re-applies the Services page's persistent switch at start (B-26). It is
// deliberately loud in the log: a unit that has gone away, or a mask that failed, is the
// difference between a service the user switched off and one that is running again.
func replayUnitSwitch(sd system.SystemdServices, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// task 50: the own timers first - written into /run, reloaded, the ones not switched off
	// enabled - so that the switch replay below still applies on top of them
	tapplied, tproblems := sd.ReplayLocalTimers(ctx)
	if len(tapplied) > 0 {
		log.Info("services: the own timers were written and enabled", "units", strings.Join(tapplied, ", "))
	}
	for _, p := range tproblems {
		log.Warn("services: an own timer could not be applied", "unit", p)
	}
	applied, problems := sd.ReplayUnitSwitch(ctx)
	if len(applied) > 0 {
		log.Info("services: the stored switch was applied", "units", strings.Join(applied, ", "))
	}
	for _, p := range problems {
		log.Warn("services: the stored switch could not be applied", "unit", p)
	}
	// task 27.4: the unit overrides, same mechanism, same reason - /run is empty after a boot
	oapplied, oproblems := sd.ReplayUnitOverrides(ctx)
	if len(oapplied) > 0 {
		log.Info("services: the stored unit overrides were applied", "units", strings.Join(oapplied, ", "))
	}
	for _, p := range oproblems {
		log.Warn("services: a stored unit override could not be applied", "unit", p)
	}
}

// firstBootAddonABI disables the installed addons whose binaries this box cannot execute, once
// per box (task 25, D-43). Separate from the regadom import because it has to happen on every box
// that came from somewhere else, not only on one with a ReGa database to read: /usr/local and the
// addons in it survive a switch in both directions. The marker keeps what was disabled, so the
// Status page can still say so after a restart - it is written even when nothing was disabled,
// because the scan is a one-off and a user who re-enables such an addon is not overruled at the
// next boot.
func firstBootAddonABI(root system.Root, stateDir string, log *slog.Logger) []string {
	marker := filepath.Join(stateDir, "addon-abi-checked")
	if b, err := os.ReadFile(marker); err == nil {
		var m struct {
			Disabled []string `json:"disabled"`
		}
		_ = json.Unmarshal(b, &m)
		return m.Disabled
	}
	disabled := root.DisableIncompatibleBinaryAddons()
	if len(disabled) > 0 {
		log.Warn("addons: binaries this box cannot execute, disabled", "addons", strings.Join(disabled, ", "))
	}
	b, _ := json.Marshal(struct {
		At       time.Time `json:"at"`
		Disabled []string  `json:"disabled,omitempty"`
	}{time.Now(), disabled})
	if err := os.WriteFile(marker, append(b, '\n'), 0o600); err != nil {
		log.Warn("addons: the binary check marker could not be written", "err", err)
	}
	return disabled
}

// firstBootAddonPolicies pins the addons that were already installed when D-36's default flipped
// to what they run as today - root - once per box.
//
// Confined is the default since 2026-09-07, and it applies to an addon *installed from now on*.
// An addon that came across on /usr/local from OpenCCU, or was installed before the systemd
// product, has no policy file: the generator installs no drop-in for it and it runs as root. If
// the flipped default reached those too, a firmware update would take write access away from
// every working addon at a reboot nobody connects to it - the same class of surprise B-43 handled
// with a once-per-box step and a release note, and there the maintainer chose the break because
// closing a port is reversible from the page that reports it. Confinement is not that: an addon
// that cannot write ends up in a restart loop or half-working, and the message is in the log.
// So this pins, and the Services page offers the switch with what it means written next to it.
//
// The marker is written when every addon was pinned; a failure writes none and the next boot
// tries again (B-33), which is safe because the step only touches addons that have no policy.
func firstBootAddonPolicies(sa *system.SystemdAddons, stateDir string, log *slog.Logger) {
	marker := filepath.Join(stateDir, "addon-policy-adopted")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	ids, err := sa.AdoptInstalledAddons(context.Background())
	if err != nil {
		log.Warn("addons: the installed addons could not be pinned to root; the confined default is not applied to them", "err", err)
		return
	}
	if len(ids) > 0 {
		log.Info("addons: installed before the confined default, kept as root - the Services page confines them one at a time",
			"addons", strings.Join(ids, ", "))
	}
	b, _ := json.Marshal(struct {
		At     time.Time `json:"at"`
		Pinned []string  `json:"pinned,omitempty"`
		Mode   string    `json:"mode"`
	}{time.Now(), ids, "root"})
	if err := os.WriteFile(marker, append(b, '\n'), 0o600); err != nil {
		log.Warn("addons: the policy adoption marker could not be written", "err", err)
	}
}

// enabledAddons is the set of addons whose rc.d script is executable now.
func enabledAddons(root system.Root) map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(string(root), "usr/local/etc/config/rc.d"))
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".script") && root.AddonEnabled(e.Name()) {
			out[e.Name()] = true
		}
	}
	return out
}

// settleFirstBootDisabled stops, unloads and forgets the units of the addons a first-boot step
// disabled since before was taken (occulited B-58). The scans run in occulited's start, while
// occu-addons.service has already generated the units and queued addons.target: on the
// maintainer's CCU the ReGa scan took 30 s over RedMatic's 33 869 files, and the unit, generated
// while rc.d/redmatic was still executable, started afterwards and failed 126 - the first thing he
// saw on the migrated system was a failed unit and "degraded". The stop cancels such a queued
// start; only what was enabled before and is not now is touched, so an addon the user switched
// back on is never stopped by a marker's list.
func settleFirstBootDisabled(sa *system.SystemdAddons, before map[string]bool, log *slog.Logger) {
	if sa == nil {
		return
	}
	var fresh []string
	for id := range before {
		if !sa.Scripts.Root.AddonEnabled(id) {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) == 0 {
		return
	}
	sort.Strings(fresh)
	errs := sa.SettleDisabled(context.Background(), fresh...)
	log.Info("addons: units of the addons disabled at this start stopped and unloaded", "addons", strings.Join(fresh, ", "), "stop_errors", len(errs))
}

// firstBootNeoServer switches mediola's NEO Server off on a box without the ReGa, once per box
// (task 37). The addon came across on /usr/local from OpenCCU and posts to /tclrega.exe and
// /api/homematic.cgi, neither of which exists here; on the Pi 4 lab box its unit was generated
// and running as root after the switch (2026-09-09). The switch is the vendor's own -
// $ADDONDIR/Disabled, which its rc.d script honours by starting nothing - plus the rc.d script's
// executable bit, the box's way of saying "disabled, incompatible" for every ReGa-dependent
// addon, and the unit is stopped if it is up. The marker (<state>/neoserver-disabled.json) is
// written only when the addon was found, so a restore that brings it later is handled at the
// next start, and a user who deliberately switches it back on is not overridden at every boot.
// The Status page lists it with the other ReGa-dependent addons; the Backup page offers it as a
// leftover with its size.
func firstBootNeoServer(root system.Root, services httpapi.ServiceManager, stateDir string, log *slog.Logger) {
	marker := filepath.Join(stateDir, "neoserver-disabled.json")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if root.HasReGa() || !root.NeoServerInstalled() {
		return
	}
	done, err := root.DisableNeoServer()
	if err != nil {
		log.Warn("neo server: could not be switched off; the next start tries again", "err", err, "done", done)
		return
	}
	if services != nil && root.HasSystemd() {
		if _, err := services.Control(context.Background(), "addon-"+system.NeoServerID, "stop"); err != nil {
			log.Warn("neo server: the unit could not be stopped", "err", err)
		}
	}
	log.Warn("neo server: switched off - it needs the ReGa, which this box does not have", "done", done)
	b, _ := json.Marshal(struct {
		At   time.Time `json:"at"`
		Done []string  `json:"done,omitempty"`
	}{time.Now(), done})
	if err := os.WriteFile(marker, append(b, '\n'), 0o600); err != nil {
		log.Warn("neo server: the marker could not be written", "err", err)
	}
}

// firstBootLeftovers removes what the CCU left on the userfs that openccu-lite never reads - the
// ReGa database, the WebUI's data, the NEO Server (system.RemoveLeftoversOnce's list) - once, after
// the first-boot name import settled (task 250: the maintainer, "kill these files after first boot
// of openccu-lite"; the way back is the (Open)CCU's backup from before the switch). An import that
// still has to run keeps them, with a warning, until a later start.
func firstBootLeftovers(root system.Root, services httpapi.ServiceManager, manager httpapi.AddonManager, store *meta.Store, stateDir string, log *slog.Logger) {
	var stop func(string)
	var reload func()
	if sd, ok := manager.(*system.SystemdAddons); ok && services != nil {
		stop = func(unit string) { _, _ = services.Control(context.Background(), unit, "stop") }
		reload = func() { sd.Reload(context.Background()) }
	}
	settled := importSettled(root, store, stateDir)
	// task 29: the known-useless CCU remnants, once - before the hardening, which would otherwise
	// take the world-writable bit off every file of a directory that goes anyway
	if rr, ran, err := root.RemoveCCURemnantsOnce(stateDir, settled, time.Now(), log.Info); err != nil && !errors.Is(err, system.ErrMigrationIncomplete) {
		log.Warn("ccu remnants: not all removed; the next start tries again", "err", err)
	} else if ran && len(rr.Removed) > 0 {
		var freed int64
		for _, it := range rr.Removed {
			freed += it.Bytes
		}
		log.Info("ccu remnants removed after the switch", "count", len(rr.Removed), "freed_bytes", freed)
	}
	// B-257's hardening of the world-writable config directories, once per pass, with a marker of
	// its own (B-264): also on a system whose leftovers pass ran before the hardening existed, and
	// again where a newer pass (task 312: the files under addons/mh) has not run yet
	if h, ran, err := root.HardenConfigDirsOnce(stateDir, time.Now()); err != nil && !errors.Is(err, system.ErrMigrationIncomplete) {
		log.Warn("config directories: the hardening's marker could not be written; the next start runs it again", "err", err)
	} else if ran {
		// task 29: one line per directory with the number of entries, not every file in one 8 KB line
		for _, line := range system.SummarizeHardened(h.Hardened) {
			log.Info("config directories: world-writable leftovers hardened", "dir", line.Dir, "entries", line.Entries)
		}
	}
	run, ran, err := root.RemoveLeftoversOnce(stateDir, settled, time.Now(), stop, reload)
	switch {
	case !ran && err == nil:
		return
	case errors.Is(err, system.ErrMigrationIncomplete):
		log.Warn("ccu leftovers: kept for now, the next start tries again", "reason", err.Error())
	case err != nil:
		log.Warn("ccu leftovers: removal failed; the next start tries again", "err", err, "removed", run.Removed)
	case len(run.Removed) == 0:
		log.Info("ccu leftovers: none on this system")
	default:
		log.Warn("ccu leftovers removed after the first start on openccu-lite; the way back is a backup made on the CCU before the switch",
			"items", strings.Join(run.Removed, ","), "paths", strings.Join(run.Paths, " "), "freed_bytes", run.Freed)
	}
}

// importSettled: the first-boot name import will not need the ReGa database any more - it is gone,
// the import ran (or gave up) as its marker says, or the store holds names already, so the import
// never runs (firstBootImport's guard).
func importSettled(root system.Root, store *meta.Store, stateDir string) bool {
	if _, err := os.Stat(filepath.Join(string(root), "etc/config/homematic.regadom")); err != nil {
		return true
	}
	if b, err := os.ReadFile(filepath.Join(stateDir, "regadom-imported.json")); err == nil {
		var fb httpapi.FirstBootImport
		if json.Unmarshal(b, &fb) == nil && (fb.Error == "" || fb.GaveUp != "") {
			return true
		}
		return false
	}
	snap := store.Snapshot()
	if len(snap.Objects) > 0 {
		return true
	}
	for _, e := range snap.Enums {
		if len(e.Tree) > 0 {
			return true
		}
	}
	return false
}

// firstBootImport runs the regadom import once: the store empty, the file present, no marker yet.
//
// A marker that records a *failure* is not that promise (B-33). Every box updated to the beta.1
// image hit B-1 - the ReGa database is root's and occulited had no way to read it - and kept a
// Status page saying the import failed, with a stale list of addons it disabled at the same
// moment, for the rest of its life. A failed import protects nothing, so it is retried: the store
// has to be empty for the retry to happen anyway, which is the same guard as the first time.
//
// A retry the guard turns down is given up, once, in the marker (B-170): the store holds names
// by now (the Backup page's ReGa import, a restore, the user's own work), or the file is gone,
// and neither changes by starting again. Before that, the recorded error (B-1's "permission
// denied", from before the read went through the helper) was logged as "trying again" at every
// start while nothing was tried. A given-up import has no report for the welcome page either.
//
// disabledBinary is what firstBootAddonABI disabled, carried into the report the Status page reads.
func firstBootImport(store *meta.Store, root system.Root, stateDir string, log *slog.Logger, disabledBinary []string) *httpapi.FirstBootImport {
	marker := filepath.Join(stateDir, "regadom-imported.json")
	var failed *httpapi.FirstBootImport
	if b, err := os.ReadFile(marker); err == nil {
		var fb httpapi.FirstBootImport
		if json.Unmarshal(b, &fb) == nil {
			if fb.GaveUp != "" {
				return nil
			}
			if fb.Error == "" {
				return &fb
			}
			failed = &fb
		}
	}
	path := filepath.Join(string(root), "etc/config/homematic.regadom")
	_, statErr := os.Stat(path)
	snap := store.Snapshot()
	empty := len(snap.Objects) == 0
	for _, e := range snap.Enums {
		if len(e.Tree) > 0 {
			empty = false
		}
	}
	if statErr != nil || !empty {
		if failed != nil {
			failed.GaveUp = "the store holds names already"
			if statErr != nil {
				failed.GaveUp = "the ReGa database is gone"
			}
			log.Info("regadom: the recorded first-boot import failed and is not tried again", "reason", failed.GaveUp, "err", failed.Error)
			if b, err := json.Marshal(failed); err == nil {
				if err := os.WriteFile(marker, b, 0o600); err != nil {
					log.Warn("regadom: the marker could not be written", "err", err)
				}
			}
		}
		return nil
	}
	if failed != nil {
		log.Info("regadom: the recorded first-boot import failed, trying again", "err", failed.Error)
	}
	// the database is root's (0640) and occulited is not root: read it through the boundary
	var dump *regaimport.Dump
	raw, err := system.Priv.ReadFile(path)
	if err == nil {
		dump, err = regaimport.ParseRegadom(bytes.NewReader(raw))
	}
	fb := &httpapi.FirstBootImport{At: time.Now(), Source: path, DisabledBinaryAddons: disabledBinary}
	if err != nil {
		fb.Error = err.Error()
		log.Warn("regadom: first-boot import failed", "err", err)
	} else {
		res := regaimport.Convert(dump, meta.Defaults())
		if _, _, err := store.Import(nil, res.Document, meta.ImportMerge); err != nil {
			fb.Error = err.Error()
		} else {
			fb.Objects, fb.Devices, fb.Channels, fb.Rooms, fb.Functions, fb.Unnamed = len(res.Document.Objects), res.Devices, res.Channels, res.Rooms, res.Functions, res.Unnamed
			log.Info("regadom: names imported on first boot", "objects", fb.Objects, "rooms", fb.Rooms, "functions", fb.Functions, "unnamed", fb.Unnamed)
		}
	}
	// the addons that need the ReGa are disabled now, once; the Addons page lists them as
	// "disabled, incompatible" with a switch back (maintainer, 2026-09-06)
	if fb.DisabledAddons = root.DisableRegaDependentAddons(); len(fb.DisabledAddons) > 0 {
		log.Warn("regadom: ReGa-dependent addons disabled", "addons", strings.Join(fb.DisabledAddons, ", "))
	}
	if b, err := json.Marshal(fb); err == nil {
		_ = os.WriteFile(marker, b, 0o600)
	}
	return fb
}

// wifiService is the Network page's Wi-Fi (task 89) on a systemd image, where occu-wifi.service
// applies it; nil elsewhere.
func wifiService(root system.Root, systemd bool) *system.WiFi {
	if !systemd {
		return nil
	}
	return &system.WiFi{Root: root, LocalDir: filepath.Join(string(root), "/run/occulite/wpa"), Log: slog.Default().With("area", "wifi")}
}

// discoveryVersion is what the eQ-3 discovery answer carries as the firmware version (task 163):
// the lite image's own version where /VERSION has one, the OpenCCU base's otherwise. The answer's
// type already says `-lite`, so a client that reached this system knows it is not a CCU3; naming
// the base version alone would claim a firmware this system does not run, and homematic-manager
// prints the string as it comes.
func discoveryVersion(v system.Version) string {
	if v.Lite != "" {
		return v.Lite
	}
	return v.Version
}

// maintenanceWrite is the device of a setValue or putParamset on a maintenance channel
// (<device>:0), ok false for any other call.
func maintenanceWrite(c literpc.Call) (device string, ok bool) {
	addr, ok := literpc.Address(c)
	if !ok {
		return "", false
	}
	device, ok = strings.CutSuffix(addr, ":0")
	return device, ok && device != ""
}

// stateSource is the state store's sweep over the interface processes' own RPC (task 194).
type stateSource struct{ ifs func() []interfaces.Interface }

func (s stateSource) find(name string) (interfaces.Interface, error) {
	return interfaces.Find(s.ifs(), name)
}

func (s stateSource) Channels(ctx context.Context, iface string) ([]string, error) {
	i, err := s.find(iface)
	if err != nil {
		return nil, err
	}
	return interfaces.Channels(ctx, i, 30*time.Second)
}

func (s stateSource) Values(ctx context.Context, iface, channel string) (map[string]any, error) {
	i, err := s.find(iface)
	if err != nil {
		return nil, err
	}
	return interfaces.ChannelValues(ctx, i, channel, 10*time.Second)
}

// rpcDropChild is `occulited rpc-drop <pid> <fd>`, which only the helper runs.
func rpcDropChild(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: occulited rpc-drop <pid> <fd>")
	}
	pid, err1 := strconv.Atoi(args[0])
	fd, err2 := strconv.Atoi(args[1])
	if err1 != nil || err2 != nil || pid <= 1 || fd < 0 {
		return errors.New("pid and fd are numbers")
	}
	return priv.ShutdownForeignSocket(pid, fd)
}

// wifiCarrier says whether a Wi-Fi interface has a carrier: the system then has its network without
// the cable, and an Ethernet without a link is no lost link (B-249).
func wifiCarrier(root string) bool {
	m, _ := filepath.Glob(filepath.Join(root, "sys/class/net/wlan*/carrier"))
	for _, p := range m {
		if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == "1" {
			return true
		}
	}
	return false
}
