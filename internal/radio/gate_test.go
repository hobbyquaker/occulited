package radio

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGateContent(t *testing.T) {
	for _, c := range []struct {
		up, limit time.Duration
		want      string
	}{
		{0, 0, "0\n"},
		{123*time.Second + 400*time.Millisecond, 11 * time.Minute, "784\n"}, // rounded up
		{100 * time.Second, time.Minute, "160\n"},
	} {
		if got := string(GateContent(c.up, c.limit)); got != c.want {
			t.Errorf("GateContent(%v, %v) = %q, want %q", c.up, c.limit, got, c.want)
		}
	}
}

func TestGateCloseOpenAndUptime(t *testing.T) {
	root := t.TempDir()
	if _, ok := Uptime(root); ok {
		t.Fatal("uptime without /proc/uptime")
	}
	_ = os.MkdirAll(filepath.Join(root, "proc"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("3600.42 7000.10\n"), 0o444)
	if up, ok := Uptime(root); !ok || up != 3600420*time.Millisecond {
		t.Fatalf("uptime %v %v", up, ok)
	}
	_ = os.MkdirAll(filepath.Join(root, ShadowDir), 0o755)
	if err := OpenGate(root); err != nil {
		t.Fatalf("opening an open gate: %v", err)
	}
	if err := CloseGate(root, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(GatePath(root))
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("marker: %v %v", st, err)
	}
	if b, _ := os.ReadFile(GatePath(root)); string(b) != "3781\n" {
		t.Fatalf("deadline: %q", b)
	}
	if GatePath(root) != filepath.Join(root, "run/occulite/radio/changing") || GatePath("/") != "/run/occulite/radio/changing" {
		t.Fatalf("path %s", GatePath(root))
	}
	// the units let through one by one; again is once
	for _, u := range []string{"multimacd", "rfd", "rfd"} {
		if err := ReleaseGate(root, u); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(GatePath(root)); string(b) != "3781\nmultimacd\nrfd\n" {
		t.Fatalf("released: %q", b)
	}
	if st, _ := os.Stat(GatePath(root)); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode after a release: %v", st.Mode())
	}
	if err := OpenGate(root); err != nil || exists(GatePath(root)) {
		t.Fatalf("open: %v", err)
	}
	// an open gate releases nothing and is not created by a release
	if err := ReleaseGate(root, "rfd"); err != nil || exists(GatePath(root)) {
		t.Fatalf("release of an open gate: %v", err)
	}
	if got := string(GateReleased([]byte("5\n"), "hmipserver")); got != "5\nhmipserver\n" {
		t.Fatalf("GateReleased: %q", got)
	}
}

// openccu-lite B-307: the hotplug's re-plan holds the radio units from its stop until the new plan
// is written - every stop happens with the gate closed, every start after it opened again.
func TestHotplugGatesTheRadioUnits(t *testing.T) {
	fakeUsers(t)
	root, rec := boxRoot(t, map[string]string{"raw-uart1": "eQ-3 HmIP-RFUSB-TK@usb-1"})
	rec.probe.answers = map[string]string{"raw-uart1": "HMIP-RFUSB-TK 0001TK0001 3014F711A0000000001TK0001 0x000000 0x7F0A17 2.8.6"}
	var mu sync.Mutex
	var seen []string
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && (args[0] == "stop" || args[0] == "start") {
			mu.Lock()
			state := "open"
			if b, err := os.ReadFile(GatePath(root)); err == nil {
				state = "held"
				if strings.Contains(string(b), "\n"+strings.TrimSuffix(args[len(args)-1], ".service")+"\n") {
					state = "released"
				}
			}
			seen = append(seen, args[0]+" "+args[len(args)-1]+" "+state)
			mu.Unlock()
		}
		return rec.run(ctx, name, args...)
	}
	d := Detector{Root: root, Run: run, Sleep: func(time.Duration) {}}
	logf := func(string, ...any) {}
	ctx := context.Background()
	if _, err := Run(ctx, root, d, logf); err != nil {
		t.Fatal(err)
	}
	unplug(root, "raw-uart1")
	if _, err := Hotplug(ctx, root, d, 0, logf); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop hmipserver.service held", "start hmipserver.service released"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("gate at the units' calls: %v, want %v", seen, want)
	}
	if exists(GatePath(root)) {
		t.Fatal("the gate is left closed")
	}
	// a write that fails opens the gate all the same
	seen = nil
	plug(t, root, "raw-uart1", "eQ-3 HmIP-RFUSB-TK@usb-1")
	_ = os.Chmod(filepath.Join(root, "run/occulite/radio"), 0o755)
	envs, _ := filepath.Glob(filepath.Join(root, "run/occulite/radio/*.env"))
	for _, e := range envs {
		// a directory where the env file goes: the write fails there
		_ = os.Remove(e)
		_ = os.MkdirAll(e, 0o755)
		_ = os.WriteFile(filepath.Join(e, "x"), nil, 0o644)
	}
	_, err := Hotplug(ctx, root, d, 0, logf)
	if err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("the write should have failed: %v", err)
	}
	if exists(GatePath(root)) {
		t.Fatal("the gate is left closed after a failed write")
	}
	if len(seen) == 0 || !strings.HasSuffix(seen[len(seen)-1], " released") || !strings.HasPrefix(seen[0], "stop ") || !strings.HasSuffix(seen[0], " held") {
		t.Fatalf("calls after a failed write: %v", seen)
	}
}
