package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// occulited B-91: the script runs to its end although the request went away (a stop half-way would
// skip its EXIT trap), and the file it made is removed then.
func TestCreateBackupCancelledRunsToTheEnd(t *testing.T) {
	r := rootWith(t, map[string]string{"VERSION": "VERSION=3.89.8\n", "proc/sys/kernel/hostname": "lite\n", "usr/local/tmp/.keep": ""})
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var made string
	script := func(sctx context.Context, _ string, args ...string) ([]byte, error) {
		if sctx.Done() != nil {
			t.Error("the script's context can be cancelled")
		}
		<-release
		made = args[0]
		return nil, os.WriteFile(args[0], []byte("sbk"), 0o644)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.CreateBackup(ctx, script)
		done <- err
	}()
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(made); !os.IsNotExist(err) {
		t.Errorf("the cancelled backup's file is still there: %v", err)
	}
	// a script that failed after the request went away: nothing to remove, still the context's error
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := r.CreateBackup(ctx2, func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exit 1") }); !errors.Is(err, context.Canceled) {
		t.Errorf("failed and cancelled: %v", err)
	}
}

func TestSweepBackupTemp(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/tmp/.keep": ""})
	dir := filepath.Join(string(r), BackupDir)
	now := time.Now()
	old := now.Add(-13 * time.Hour)
	mk := func(name string, dirEntry bool, mtime time.Time) {
		p := filepath.Join(dir, name)
		if dirEntry {
			_ = os.MkdirAll(p, 0o700)
			_ = os.WriteFile(filepath.Join(p, "usr_local.tar.gz"), []byte("x"), 0o600)
		} else {
			_ = os.WriteFile(p, []byte("x"), 0o600)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	mk("tmp.gzUUOv", true, old)                  // a killed backup's: goes
	mk("tmp.Ab12Cd34", true, old)                // longer mktemp names too
	mk("tmp.running", true, now.Add(-time.Hour)) // a backup at work: stays
	mk("tmp.abc", true, old)                     // not mktemp's shape: stays
	mk("tmp.fileXY", false, old)                 // a file: stays
	mk("restore-ccu.sbk", false, old)            // an upload waiting for its restore: stays
	mk("upload", true, old)                      // any other directory: stays
	got := r.SweepBackupTemp(now, BackupTempMaxAge)
	if len(got) != 2 || got[0] != "tmp.Ab12Cd34" || got[1] != "tmp.gzUUOv" {
		t.Errorf("removed %v", got)
	}
	for _, n := range []string{"tmp.running", "tmp.abc", "tmp.fileXY", "restore-ccu.sbk", "upload"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	// a missing directory is nothing to sweep
	if got := Root(t.TempDir()).SweepBackupTemp(now, BackupTempMaxAge); got != nil {
		t.Errorf("%v", got)
	}
}
