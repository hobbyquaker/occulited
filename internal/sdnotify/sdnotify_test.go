package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// listen is a stand-in for systemd's notification socket: a unixgram socket at addr.
func listen(t *testing.T, addr string) *net.UnixConn {
	t.Helper()
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func read(t *testing.T, c *net.UnixConn) string {
	t.Helper()
	buf := make([]byte, 4096)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func TestNotifyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify")
	c := listen(t, path)
	n := New(path)
	if !n.Enabled() {
		t.Fatal("not enabled")
	}
	if err := n.Notify("READY=1", "STATUS=serving"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "READY=1\nSTATUS=serving\n" {
		t.Errorf("datagram %q", got)
	}
}

func TestNotifyAbstract(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("abstract sockets are Linux's")
	}
	name := "@occulited-sdnotify-test-" + strconv.Itoa(os.Getpid())
	c := listen(t, name)
	if err := New(name).Notify("READY=1"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "READY=1\n" {
		t.Errorf("datagram %q", got)
	}
}

func TestFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify")
	c := listen(t, path)
	t.Setenv("NOTIFY_SOCKET", path)
	n := FromEnv()
	if _, set := os.LookupEnv("NOTIFY_SOCKET"); set {
		t.Error("NOTIFY_SOCKET still in the environment: the children would inherit it")
	}
	if err := n.Notify("READY=1"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "READY=1\n" {
		t.Errorf("datagram %q", got)
	}
}

// Without the variable, or with a form that is not a unix socket, nothing is sent and nothing fails.
func TestNoSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	for _, n := range []*Notifier{FromEnv(), New(""), New("vsock:2:1234"), New("relative/path"), nil, {}} {
		if n.Enabled() {
			t.Errorf("%+v enabled", n)
		}
		if err := n.Notify("READY=1"); err != nil {
			t.Errorf("%+v: %v", n, err)
		}
	}
}

func TestNotifyRefusesBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify")
	listen(t, path)
	n := New(path)
	for _, l := range []string{"STATUS=a\nREADY=1", "READY"} {
		if err := n.Notify(l); err == nil {
			t.Errorf("%q accepted", l)
		}
	}
	if err := n.Notify(); err != nil {
		t.Errorf("no lines: %v", err)
	}
}

// A socket that is gone is an error the caller logs, never a panic or a hang.
func TestNotifyGoneSocket(t *testing.T) {
	if err := New(filepath.Join(t.TempDir(), "gone")).Notify("READY=1"); err == nil {
		t.Error("no error for a missing socket")
	}
}
