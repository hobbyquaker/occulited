package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/hobbyquaker/occulited/internal/syslogfwd"
	"github.com/hobbyquaker/occulited/internal/system"
)

// syslogForwardMarker is in the binary so that the fork's wrapper can tell an occulited that has
// this subcommand from an older one, which would take the word for no subcommand and start a
// second daemon.
const syslogForwardMarker = "occulited-syslog-forward-v1"

// syslogForward is `occulited syslog-forward`: the journal to LOGHOST of /etc/config/syslog, as
// RFC 5424 over UDP (B-96). occu-syslog-forward.service runs it; with no LOGHOST it says so and
// ends.
func syslogForward(args []string) error {
	fs := flag.NewFlagSet("syslog-forward", flag.ContinueOnError)
	root := fs.String("root", "/", "filesystem root whose /etc/config/syslog is read")
	loghost := fs.String("loghost", "", "send here instead of LOGHOST (host, host:port, [v6]:port)")
	cursor := fs.String("cursor", "", "the file keeping the journal cursor; default $RUNTIME_DIRECTORY/cursor, none without it")
	maxSize := fs.Int("max-size", syslogfwd.DefaultMaxSize, "the largest message in bytes")
	backlog := fs.Duration("backlog", syslogfwd.DefaultBacklog, "how far back into this boot a start without a cursor reaches")
	bootCursor := fs.String("boot-cursor", os.Getenv("OCCU_SYSLOG_BOOT_CURSOR"), "the file keeping the cursor of a stop for the next boot (on the userfs); default $OCCU_SYSLOG_BOOT_CURSOR, none without it")
	drain := fs.Duration("drain", syslogfwd.DefaultDrain, "how long a stop reads on to the journal's last entry")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: occulited syslog-forward [flags]   (%s)\n", syslogForwardMarker)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	host := *loghost
	if host == "" {
		host = system.Root(*root).ReadLogLevels().LogHost
	}
	if host == "" {
		fmt.Fprintln(os.Stderr, "syslog-forward: no LOGHOST in /etc/config/syslog, nothing to forward")
		return nil
	}
	if _, err := syslogfwd.ParseLogHost(host); err != nil {
		return err
	}
	file := *cursor
	if file == "" {
		if dir := os.Getenv("RUNTIME_DIRECTORY"); dir != "" {
			file = filepath.Join(filepath.SplitList(dir)[0], "cursor")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	// task 186: the journal like the daemon, as occu-syslog-forward
	h, err := logHandler("auto", "occu-syslog-forward", slog.LevelInfo)
	if err != nil {
		return err
	}
	log := slog.New(h)
	slog.SetDefault(log)
	f := &syslogfwd.Forwarder{
		LogHost:        host,
		CursorFile:     file,
		BootCursorFile: *bootCursor,
		Drain:          *drain,
		MaxSize:        *maxSize,
		Backlog:        *backlog,
		Log:            log,
	}
	return f.Run(ctx)
}
