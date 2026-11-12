package priv

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
)

// An interface's IPv6 sysctls (openccu-lite task 227, occulited B-139). The IPv6 settings switch
// three of them per interface; a procfs file cannot be written the way WriteFile writes a file
// (temporary file plus rename), so until B-139 this was the helper's shell shape
// `echo '<v>' > '/proc/sys/net/ipv6/conf/<iface>/<key>'`. Now it is one typed operation: a named
// interface (never all, default or lo), one of three keys, the value 0, 1 or 2, written in place.

// opIPv6Conf is the operation's name on the wire.
const opIPv6Conf = "ipv6-conf"

// IPv6ConfKeys are the sysctls the operation writes.
var IPv6ConfKeys = []string{"disable_ipv6", "accept_ra", "autoconf"}

// ipv6ConfDir is the per-interface sysctl directory; a variable so a test can point it elsewhere.
var ipv6ConfDir = "/proc/sys/net/ipv6/conf"

// validIPv6Conf checks the request's three parts; the helper checks at its boundary, the
// operation again.
func validIPv6Conf(iface, key string, v int) error {
	if !shapeIface(iface) {
		return fmt.Errorf("interface %q", iface)
	}
	if !slices.Contains(IPv6ConfKeys, key) {
		return fmt.Errorf("key %q", key)
	}
	if v < 0 || v > 2 {
		return fmt.Errorf("value %d", v)
	}
	return nil
}

// SetIPv6Conf writes one of IPv6ConfKeys of one interface.
func (Local) SetIPv6Conf(_ context.Context, iface, key string, v int) error {
	if err := validIPv6Conf(iface, key, v); err != nil {
		return fmt.Errorf("ipv6-conf: %w", err)
	}
	return writeLEDAttr(filepath.Join(ipv6ConfDir, iface, key), strconv.Itoa(v))
}

// SetIPv6Conf asks the helper to write one of an interface's IPv6 sysctls.
func (c Client) SetIPv6Conf(ctx context.Context, iface, key string, v int) error {
	_, err := c.call(ctx, request{Op: opIPv6Conf, Name: iface, Target: key, Data: []byte(strconv.Itoa(v))})
	return err
}
