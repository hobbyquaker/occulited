package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/logctl"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
)

// DefaultHelperSocket is where `occulited helper` listens and the daemon looks.
const DefaultHelperSocket = "/run/occulite/helper.sock"

// helperMain is `occulited helper`: the root half of task 17. It listens on a unix socket
// that only the occulite group may open and performs the enumerated operations of
// priv.DefaultPolicy for the unprivileged daemon - nothing else.
func helperMain(args []string) error {
	fs := flag.NewFlagSet("occulited helper", flag.ContinueOnError)
	socket := fs.String("socket", DefaultHelperSocket, "unix socket to serve")
	group := fs.String("group", "occulite", "group that may connect")
	rootDir := fs.String("root", "/", "filesystem root the allowlists are under (development)")
	stateDir := fs.String("state-dir", "/usr/local/etc/occulite", "occulited's state directory (its staging area)")
	cfgPath := fs.String("config", "/usr/local/etc/occulite/occulited.json", "occulited's configuration file, for the log level at start")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !priv.IsRoot() {
		return fmt.Errorf("the helper must run as root")
	}
	gid := 0
	if g, err := user.LookupGroup(*group); err == nil {
		gid, _ = strconv.Atoi(g.Gid)
	} else if n, err := strconv.Atoi(*group); err == nil {
		gid = n
	} else {
		return fmt.Errorf("group %q: %w", *group, err)
	}
	l, err := priv.Listen(*socket, gid)
	if err != nil {
		return err
	}
	// task 101: the helper logs at occulited's level - read from its file at start, and set by
	// occulited through the loglevel operation whenever it changes (and when occulited starts)
	var level slog.LevelVar
	if cfg, err := config.Load(*cfgPath); err == nil {
		level.Set(logctl.SlogLevel(cfg.LogLevel))
	}
	// task 186: the journal like the daemon, as occulited-helper
	h, err := logHandler("auto", "occulited-helper", &level)
	if err != nil {
		return err
	}
	log := slog.New(h)
	slog.SetDefault(log)
	srv := &priv.Server{
		Policy: priv.DefaultPolicy(*rootDir, *stateDir),
		Log:    func(f string, a ...any) { log.Warn(fmt.Sprintf(f, a...)) },
		Info:   func(f string, a ...any) { log.Info(fmt.Sprintf(f, a...)) },
		Debug:  func(f string, a ...any) { log.Debug(fmt.Sprintf(f, a...)) },
		SetLogLevel: func(name string) {
			if l := logctl.SlogLevel(name); l != level.Level() {
				level.Set(l)
				log.Info("occulited helper: log level " + name)
			}
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// openccu-lite task 164: the RPI-RF-MOD's button is read here, so its long press works while
	// occulited does not; the long press's action and threshold come from occulited.json
	btn := &priv.ButtonWatcher{Root: *rootDir, Log: srv.Log, Info: srv.Info, Config: func() priv.ButtonConfig {
		c, err := config.Load(*cfgPath)
		if err != nil {
			return priv.ButtonConfig{}
		}
		s, err := c.Hardware.Button.Normalize()
		if err != nil {
			return priv.ButtonConfig{}
		}
		return priv.ButtonConfig{LongS: s.LongS, Long: s.Long}
	}}
	btn.Start(ctx)
	srv.Button = btn
	log.Info("occulited helper listening", "socket", *socket, "group", *group, "root", *rootDir)
	err = srv.Serve(ctx, l)
	_ = os.Remove(*socket)
	return err
}

// usePrivilegeHelper switches the daemon's privileged operations to the helper when it does not
// run as root itself. Returns what was chosen, for the start-up log line.
func usePrivilegeHelper(socket, stateDir string) string {
	system.StagingDir = stateDir + "/staging"
	if priv.IsRoot() {
		return "root (no helper needed)"
	}
	if _, err := os.Stat(socket); err != nil {
		return "unprivileged, no helper at " + socket + " - privileged operations will fail"
	}
	system.Priv = priv.Client{Socket: socket}
	// the device-firmware fetcher writes into /etc/config/firmware, which is root's (B-20)
	firmware.Priv = priv.Client{Socket: socket}
	firmware.StagingDir = stateDir + "/staging"
	return "unprivileged, helper at " + socket
}
