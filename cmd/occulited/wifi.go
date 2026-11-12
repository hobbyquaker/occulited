package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os/exec"
	"time"

	"github.com/hobbyquaker/occulited/internal/wifi"
)

// wifiMain is occu-wifi.service's root side (task 89): `up` at boot and after a switch or a new
// network, `reload` for changed addressing or a changed preference, `down` at its stop.
func wifiMain(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: occulited wifi up|down|reload [--root <dir>]")
	}
	cmd := args[0]
	fs := flag.NewFlagSet("occulited wifi "+cmd, flag.ContinueOnError)
	root := fs.String("root", "/", "the filesystem root (a sandbox for a test)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b := wifi.Box{Root: *root, Log: radioLog, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}, RunIn: func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, name, args...)
		c.Stdin = bytes.NewReader(stdin)
		return c.CombinedOutput()
	}}
	switch cmd {
	case "up":
		return b.Up(ctx)
	case "down":
		return b.Down(ctx)
	case "reload":
		return b.Reload(ctx)
	}
	return fmt.Errorf("unknown wifi command %q", cmd)
}
