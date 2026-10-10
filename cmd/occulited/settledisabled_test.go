package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/system"
)

// B-58: only what a first-boot step disabled since the snapshot is settled - an addon that was
// already disabled (or that the user enabled again after an earlier boot's scan) is not touched.
func TestSettleFirstBootDisabled(t *testing.T) {
	d := t.TempDir()
	rcd := filepath.Join(d, "usr/local/etc/config/rc.d")
	_ = os.MkdirAll(rcd, 0o755)
	for name, mode := range map[string]os.FileMode{"redmatic": 0o755, "hmm": 0o755, "old": 0o644, "redmatic.script": 0o755} {
		_ = os.WriteFile(filepath.Join(rcd, name), []byte("#!/bin/sh\n"), mode)
	}
	root := system.Root(d)
	before := enabledAddons(root)
	if len(before) != 2 || !before["redmatic"] || !before["hmm"] {
		t.Fatalf("before %v", before)
	}
	_ = os.Chmod(filepath.Join(rcd, "redmatic"), 0o644) // the ReGa scan
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	sa := system.NewSystemdAddons(root, system.SystemdServices{Root: root, Run: run})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settleFirstBootDisabled(sa, before, log)
	want := "systemctl stop --no-pager -- addon-redmatic.service\nsystemctl daemon-reload\nsystemctl reset-failed --no-pager -- addon-redmatic.service"
	if got := strings.Join(calls, "\n"); got != want {
		t.Fatalf("calls:\n%s", got)
	}
	// nothing disabled since: nothing done; no systemd: nothing done
	calls = nil
	settleFirstBootDisabled(sa, enabledAddons(root), log)
	settleFirstBootDisabled(nil, before, log)
	if len(calls) != 0 {
		t.Fatalf("calls %v", calls)
	}
}
