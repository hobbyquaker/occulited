package priv

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hdmiTree builds a /sys with a DRM class directory whose connectors are links into
// sys/devices, as the kernel's are, and points drmDir and cmdlinePath at it.
func hdmiTree(t *testing.T, cmdline string, conns ...string) (root string) {
	t.Helper()
	root = t.TempDir()
	cls := filepath.Join(root, "sys", "class", "drm")
	dev := filepath.Join(root, "sys", "devices", "platform", "gpu", "drm", "card0")
	for _, d := range []string{cls, dev} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range append([]string{"card0", "renderD128", "card0-Writeback-1"}, conns...) {
		if err := os.MkdirAll(filepath.Join(dev, c), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, c, "status"), []byte("connected\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "devices", "platform", "gpu", "drm", "card0", c), filepath.Join(cls, c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cmdline"), []byte(cmdline+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldDir, oldCmd := drmDir, cmdlinePath
	drmDir, cmdlinePath = cls, filepath.Join(root, "cmdline")
	t.Cleanup(func() { drmDir, cmdlinePath = oldDir, oldCmd })
	return root
}

func statusOf(t *testing.T, root, conn string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "sys", "devices", "platform", "gpu", "drm", "card0", conn, "status"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestHDMIRestore(t *testing.T) {
	for _, c := range []struct{ cmdline, conn, want string }{
		{"console=tty2 video=HDMI-A-1:640x480M@60D quiet", "card0-HDMI-A-1", "on"},
		{"video=HDMI-A-1:640x480M@60D", "card0-HDMI-A-2", "detect"},
		{"video=HDMI-A-2:1920x1080e", "card1-HDMI-A-2", "on"},
		{"video=HDMI-A-1:d", "card0-HDMI-A-1", "off"},
		{"video=HDMI-A-1:1280x720", "card0-HDMI-A-1", "detect"},
		{"video=HDMI-A-1:", "card0-HDMI-A-1", "detect"},
		{"", "card0-HDMI-A-1", "detect"},
		{"video=HDMI-A-10:640x480D", "card0-HDMI-A-1", "detect"},
	} {
		if got := hdmiRestore(c.cmdline, c.conn); got != c.want {
			t.Errorf("%q %s: %s, want %s", c.cmdline, c.conn, got, c.want)
		}
	}
}

func TestHDMIConnectors(t *testing.T) {
	root := hdmiTree(t, "", "card0-HDMI-A-1", "card0-HDMI-A-2", "card0-DSI-1")
	got := HDMIConnectors(filepath.Join(root, "sys", "class", "drm"))
	if strings.Join(got, ",") != "card0-HDMI-A-1,card0-HDMI-A-2" {
		t.Errorf("connectors %v", got)
	}
	if HDMIConnectors(filepath.Join(root, "missing")) != nil {
		t.Error("a missing directory has connectors")
	}
}

// Task 340: off writes off to every HDMI connector and nothing else; on gives each back what the
// command line forced (the rpi4's video=HDMI-A-1:…D) or detect.
func TestSetHDMILocal(t *testing.T) {
	root := hdmiTree(t, "console=tty2 video=HDMI-A-1:640x480M@60D", "card0-HDMI-A-1", "card0-HDMI-A-2")
	if err := (Local{}).SetHDMI(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"card0-HDMI-A-1", "card0-HDMI-A-2"} {
		if s := statusOf(t, root, c); s != "off" {
			t.Errorf("%s: %s after off", c, s)
		}
	}
	if s := statusOf(t, root, "card0-Writeback-1"); s != "connected" {
		t.Errorf("the writeback connector was written: %s", s)
	}
	if err := (Local{}).SetHDMI(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if a, b := statusOf(t, root, "card0-HDMI-A-1"), statusOf(t, root, "card0-HDMI-A-2"); a != "on" || b != "detect" {
		t.Errorf("after on: %s %s", a, b)
	}
}

func TestSetHDMIRefusals(t *testing.T) {
	hdmiTree(t, "")
	if err := (Local{}).SetHDMI(context.Background(), false); err == nil {
		t.Error("no connector is no error")
	}
	// a connector link that leads out of /sys is not written, the others are
	root := hdmiTree(t, "", "card0-HDMI-A-1")
	outside := filepath.Join(t.TempDir(), "card0-HDMI-A-2")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "status"), []byte("connected"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(drmDir, "card0-HDMI-A-2")); err != nil {
		t.Fatal(err)
	}
	err := (Local{}).SetHDMI(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "outside /sys") {
		t.Errorf("a link out of /sys: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "status")); string(b) != "connected" {
		t.Errorf("written outside /sys: %s", b)
	}
	if s := statusOf(t, root, "card0-HDMI-A-1"); s != "off" {
		t.Errorf("the good connector was not written: %s", s)
	}
}

// Through the helper: the switch alone, a request with anything else is refused.
func TestHDMIOperation(t *testing.T) {
	root := hdmiTree(t, "", "card0-HDMI-A-1")
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logged []string
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Log: func(f string, a ...any) { logged = append(logged, f) }}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}
	if err := c.SetHDMI(ctx, false); err != nil {
		t.Fatalf("off through the helper: %v", err)
	}
	if s := statusOf(t, root, "card0-HDMI-A-1"); s != "off" {
		t.Errorf("after off: %s", s)
	}
	if err := c.SetHDMI(ctx, true); err != nil {
		t.Fatalf("on through the helper: %v", err)
	}
	if s := statusOf(t, root, "card0-HDMI-A-1"); s != "detect" {
		t.Errorf("after on: %s", s)
	}
	if _, err := c.call(ctx, request{Op: opHDMI, Path: "/sys/class/drm/card0-HDMI-A-1"}); err == nil || !errors.Is(err, ErrRefused) {
		t.Errorf("a request with a path: %v", err)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "HDMI") {
		t.Errorf("the refusal was not logged: %v", logged)
	}
}
