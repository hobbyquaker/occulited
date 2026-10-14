package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/radio"
)

// openccu-lite task 349: the command - the numbers, a dry run, the question on a terminal, --yes
// off one, and --cancel.
func TestCounterRepairCommand(t *testing.T) {
	root := t.TempDir()
	fixture, err := os.ReadFile("../../internal/radio/testdata/accesspoint.ap")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(fixture), "BACKUP_DON") + 11
	binary.BigEndian.PutUint64(fixture[i:i+8], 4031374848) // an offset that wraps
	ap := filepath.Join(root, "etc/config/crRFD/data/3014F711A000040000000A01.ap")
	_ = os.MkdirAll(filepath.Dir(ap), 0o755)
	if err := os.WriteFile(ap, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC) }
	run := func(stdin string, tty bool, args ...string) (string, error) {
		var out bytes.Buffer
		err := counterRepair(append([]string{"--root", root}, args...), strings.NewReader(stdin), &out, tty, now)
		return out.String(), err
	}
	out, err := run("", false, "--dry-run")
	if err != nil || !strings.Contains(out, "3014F711A000040000000A01: wrapped") || !strings.Contains(out, "sent as") || !strings.Contains(out, "Dry run") || radio.ReadCounterRepair(root) != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if _, err := run("", false); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no terminal, no --yes: %v", err)
	}
	if out, _ := run("n\n", true); !strings.Contains(out, "Nothing requested") || radio.ReadCounterRepair(root) != nil {
		t.Fatalf("answered no: %s", out)
	}
	if out, err := run("y\n", true, "3014F711A000040000000A01"); err != nil || !strings.Contains(out, "Requested for 1") || radio.ReadCounterRepair(root) == nil {
		t.Fatalf("answered yes: %v %s", err, out)
	}
	if out, _ := run("", false, "--dry-run"); !strings.Contains(out, "already requested") {
		t.Fatalf("a pending request not named: %s", out)
	}
	if out, _ := run("", false, "--cancel"); !strings.Contains(out, "withdrawn") || radio.ReadCounterRepair(root) != nil {
		t.Fatalf("cancel: %s", out)
	}
	if out, _ := run("", false, "--cancel"); !strings.Contains(out, "No repair request") {
		t.Fatalf("cancel again: %s", out)
	}
	if _, err := run("", false, "--yes", "A", "B"); err == nil {
		t.Fatal("two SGTINs taken")
	}
	if _, err := run("", false, "--yes", "3014F711A000040000000A01", "--margin", "5"); err != nil || radio.ReadCounterRepair(root) == nil {
		t.Fatalf("flags after the SGTIN: %v", err)
	}
	// nothing there
	var empty bytes.Buffer
	if err := counterRepair([]string{"--root", t.TempDir()}, strings.NewReader(""), &empty, false, now); err != nil || !strings.Contains(empty.String(), "No HmIP access point") {
		t.Fatalf("empty root: %v %s", err, empty.String())
	}
}
