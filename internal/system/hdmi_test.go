package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// hdmiRoot is a system root with /VERSION's platform and the given HDMI connectors in the DRM class
// directory (plain directories: the view reads through the class directory's names).
func hdmiRoot(t *testing.T, platform string, conns ...string) Root {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("PRODUCT="+platform+"\nPLATFORM="+platform+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range append([]string{"card0", "card0-Writeback-1"}, conns...) {
		d := filepath.Join(root, "sys", "class", "drm", c)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for attr, v := range map[string]string{"status": "connected\n", "enabled": "enabled\n", "dpms": "On\n"} {
			if err := os.WriteFile(filepath.Join(d, attr), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "var", "status"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Root(root)
}

// hdmiRig is an HDMI with an in-memory setting and a recording switch.
type hdmiRig struct {
	mu    sync.Mutex
	on    bool
	calls []bool
	fail  error
}

func (g *hdmiRig) hdmi(r Root) *HDMI {
	return &HDMI{Root: r,
		Get:  func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.on },
		Save: func(on bool) error { g.mu.Lock(); defer g.mu.Unlock(); g.on = on; return nil },
		Set: func(_ context.Context, on bool) error {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.calls = append(g.calls, on)
			return g.fail
		}}
}

func (g *hdmiRig) written() []bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]bool(nil), g.calls...)
}

func TestHDMIView(t *testing.T) {
	for _, c := range []struct {
		platform string
		conns    []string
		avail    bool
		reason   string
	}{
		{"rpi4", []string{"card0-HDMI-A-1", "card0-HDMI-A-2"}, true, ""},
		{"rpi3", []string{"card0-HDMI-A-1"}, true, ""},
		{"rpi3", nil, false, "no-connector"},
		{"ova", []string{"card0-HDMI-A-1"}, false, "not-a-pi"},
		{"lxc", nil, false, "not-a-pi"},
	} {
		g := &hdmiRig{}
		v := g.hdmi(hdmiRoot(t, c.platform, c.conns...)).View()
		if v.Available != c.avail || v.Reason != c.reason {
			t.Errorf("%s %v: available %v reason %q", c.platform, c.conns, v.Available, v.Reason)
		}
		if c.avail && (len(v.Connectors) != len(c.conns) || v.Connectors[0].Name != "HDMI-A-1" || v.Connectors[0].Status != "connected" || !v.Connectors[0].Enabled || v.Connectors[0].DPMS != "On") {
			t.Errorf("%s: connectors %+v", c.platform, v.Connectors)
		}
		if !c.avail && len(v.Connectors) != 0 {
			t.Errorf("%s: connectors on an unavailable system %+v", c.platform, v.Connectors)
		}
		if v.Enabled {
			t.Errorf("%s: on by default", c.platform)
		}
	}
	if !hdmiPlatform(Version{Product: "ccu3"}) {
		t.Error("the CCU3 is a Pi")
	}
}

// Task 340: off once the boot has finished, nothing before; with the setting on nothing at all.
func TestHDMIApplyAtBoot(t *testing.T) {
	old := hdmiPoll
	hdmiPoll = 5 * time.Millisecond
	defer func() { hdmiPoll = old }()

	r := hdmiRoot(t, "rpi4", "card0-HDMI-A-1")
	g := &hdmiRig{}
	h := g.hdmi(r)
	done := make(chan struct{})
	go func() { h.ApplyAtBoot(context.Background()); close(done) }()
	time.Sleep(30 * time.Millisecond)
	if len(g.written()) != 0 {
		t.Fatalf("written before the boot finished: %v", g.written())
	}
	if v := h.View(); !v.Pending {
		t.Errorf("not pending before the boot finished: %+v", v)
	}
	if err := os.WriteFile(filepath.Join(string(r), "var", "status", "startupFinished"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ApplyAtBoot did not return after the boot finished")
	}
	if w := g.written(); len(w) != 1 || w[0] {
		t.Errorf("writes %v, want one off", w)
	}
	if v := h.View(); v.Pending || v.Error != "" {
		t.Errorf("after the boot: %+v", v)
	}

	// the setting on: the kernel's own state stays, nothing is written
	g2 := &hdmiRig{on: true}
	g2.hdmi(r).ApplyAtBoot(context.Background())
	if len(g2.written()) != 0 {
		t.Errorf("written with the setting on: %v", g2.written())
	}
	// not a Pi: nothing, and it returns at once
	g3 := &hdmiRig{}
	g3.hdmi(hdmiRoot(t, "ova", "card0-HDMI-A-1")).ApplyAtBoot(context.Background())
	if len(g3.written()) != 0 {
		t.Errorf("written on ova: %v", g3.written())
	}
	// a failing helper: the error is in the view
	g4 := &hdmiRig{fail: errors.New("refused")}
	h4 := g4.hdmi(r)
	h4.ApplyAtBoot(context.Background())
	if v := h4.View(); v.Error != "refused" {
		t.Errorf("the error is not shown: %+v", v)
	}
	// a cancelled context before the boot finished: no write
	r5 := hdmiRoot(t, "rpi3", "card0-HDMI-A-1")
	g5 := &hdmiRig{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g5.hdmi(r5).ApplyAtBoot(ctx)
	if len(g5.written()) != 0 {
		t.Errorf("written after the cancel: %v", g5.written())
	}
}

// The boot counts as finished after hdmiBootLimit of uptime, marker or not.
func TestHDMIApplyAtBootUptime(t *testing.T) {
	r := hdmiRoot(t, "rpi3", "card0-HDMI-A-1")
	if err := os.MkdirAll(filepath.Join(string(r), "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(string(r), "proc", "uptime"), []byte("901.00 100.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &hdmiRig{}
	g.hdmi(r).ApplyAtBoot(context.Background())
	if w := g.written(); len(w) != 1 || w[0] {
		t.Errorf("writes %v, want one off", w)
	}
}

func TestHDMISwitch(t *testing.T) {
	old := hdmiSettle
	hdmiSettle = 50 * time.Millisecond
	defer func() { hdmiSettle = old }()
	r := hdmiRoot(t, "rpi4", "card0-HDMI-A-1")
	if err := os.WriteFile(filepath.Join(string(r), "var", "status", "startupFinished"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	g := &hdmiRig{}
	h := g.hdmi(r)
	h.ApplyAtBoot(context.Background())
	v, err := h.Switch(context.Background(), true)
	if err != nil || !v.Enabled {
		t.Fatalf("on: %v %+v", err, v)
	}
	if _, err := h.Switch(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if w := g.written(); len(w) != 3 || w[0] || !w[1] || w[2] {
		t.Errorf("writes %v, want off (boot), on, off", w)
	}
	g.fail = errors.New("no")
	if _, err := h.Switch(context.Background(), true); err == nil {
		t.Error("a failed write is no error")
	}
	if v := h.View(); v.Error != "no" || !v.Enabled {
		t.Errorf("after the failure: %+v", v)
	}

	// before the boot finished: on is applied at once, off is saved and waits for the boot
	r2 := hdmiRoot(t, "rpi3", "card0-HDMI-A-1")
	g2 := &hdmiRig{}
	h2 := g2.hdmi(r2)
	if _, err := h2.Switch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	v2, err := h2.Switch(context.Background(), false)
	if err != nil || !v2.Pending {
		t.Errorf("off before the boot: %v %+v", err, v2)
	}
	if w := g2.written(); len(w) != 1 || !w[0] {
		t.Errorf("writes %v, want only the on", w)
	}

	// not a Pi: refused, nothing saved
	g3 := &hdmiRig{}
	if _, err := g3.hdmi(hdmiRoot(t, "ova")).Switch(context.Background(), true); !errors.Is(err, ErrHDMIUnavailable) || g3.on {
		t.Errorf("ova: %v, saved %v", err, g3.on)
	}
}
