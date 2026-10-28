// Package sdnotify is systemd's service notification protocol (sd_notify(3)) without cgo or a
// dependency (D-15): one datagram of newline-separated KEY=value lines to the unix socket that
// $NOTIFY_SOCKET names. occulited's units are Type=notify (occulited task 122): systemd calls the
// daemon started only on READY=1, so a unit ordered After=occulited.service - every addon - starts
// once the API answers, not once the process exists.
//
// Without $NOTIFY_SOCKET (a busybox system, a terminal, the tests) every call is a no-op.
package sdnotify

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
)

// Notifier sends to one socket. The zero value and a nil *Notifier send nothing.
type Notifier struct {
	addr string
	mu   sync.Mutex
}

// FromEnv takes the socket from $NOTIFY_SOCKET and removes the variable from the environment, as
// sd_notify's unset_environment does: the programs the process starts (journalctl, systemctl, the
// helper's children) would otherwise inherit it, and with NotifyAccess=all a child's message
// would count as the service's. Returns nil without the variable.
func FromEnv() *Notifier {
	addr := os.Getenv("NOTIFY_SOCKET")
	_ = os.Unsetenv("NOTIFY_SOCKET")
	return New(addr)
}

// New is a notifier for the socket at addr: a path, or "@name" for an abstract socket. Only those
// two forms are unix sockets; anything else (empty, systemd's vsock: form) yields nil.
func New(addr string) *Notifier {
	if addr == "" || (addr[0] != '/' && addr[0] != '@') {
		return nil
	}
	return &Notifier{addr: addr}
}

// Enabled says whether the notifier sends anything.
func (n *Notifier) Enabled() bool { return n != nil && n.addr != "" }

// Notify sends the lines as one datagram ("READY=1", "STATUS=serving"). A value must not contain a
// newline; such a line is refused rather than split into two assignments.
func (n *Notifier) Notify(lines ...string) error {
	if !n.Enabled() || len(lines) == 0 {
		return nil
	}
	for _, l := range lines {
		if strings.ContainsRune(l, '\n') || !strings.Contains(l, "=") {
			return errors.New("sdnotify: not a KEY=value line: " + l)
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	// a name that starts with "@" is an abstract socket: Go's net package puts the leading NUL
	// there itself on Linux
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: n.addr, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte(strings.Join(lines, "\n") + "\n"))
	return err
}
