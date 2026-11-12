package wifi

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The root side, `occulited wifi up|down|reload` from occu-wifi.service (after the network start).
// upstream's hook never sees wlan* on openccu-lite (the lite /etc/network/interfaces names eth0
// only), so nothing else loads the driver or starts a supplicant.

// Run runs a program and answers its combined output.
type Run func(ctx context.Context, name string, args ...string) ([]byte, error)

// RunIn runs a program with stdin as its standard input (resolvconf reads its record there).
type RunIn func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)

// Box is what the steps need of the system.
type Box struct {
	Root string
	Run  Run
	// RunIn is Run with a standard input (occulited B-139/B-143: no shell for a redirect); nil =
	// what needs it is skipped and logged.
	RunIn RunIn
	// Log is the unit's journal.
	Log func(format string, args ...any)
	// Wait is how long up waits for the interface to appear after the driver is loaded.
	Wait time.Duration
}

func (b Box) path(p string) string { return filepath.Join(b.Root, p) }

func (b Box) logf(format string, args ...any) {
	if b.Log != nil {
		b.Log(format, args...)
	}
}

func (b Box) run(ctx context.Context, name string, args ...string) error {
	out, err := b.Run(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// quiet: a step that may fail without consequence (the module already gone, rfkill without a device)
func (b Box) quiet(ctx context.Context, name string, args ...string) {
	_, _ = b.Run(ctx, name, args...)
}

// resolvconfAdd hands the interface's nameserver record to resolvconf on its standard input - an
// argument slice, no shell redirect (occulited B-143). A failure is logged, not fatal: the address
// and the routes are up either way.
func (b Box) resolvconfAdd(ctx context.Context, iface, record string) {
	if b.RunIn == nil {
		b.logf("wifi: no runner with a standard input; the nameservers of %s are not handed to resolvconf", iface)
		return
	}
	if out, err := b.RunIn(ctx, []byte(record), "/sbin/resolvconf", "-a", iface); err != nil {
		b.logf("wifi: resolvconf -a %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
}

// Settings reads /etc/config/wifi.
func (b Box) Settings() Settings {
	data, _ := os.ReadFile(b.path(SettingsFile))
	return ParseSettings(string(data))
}

// Onboard says whether the board's own chip is there: an SDIO function the device tree names wifi
// (the Raspberry Pis' brcmfmac chips), which appears as an interface only with its driver loaded.
func (b Box) Onboard() bool {
	ents, _ := os.ReadDir(b.path("/sys/bus/sdio/devices"))
	for _, e := range ents {
		data, _ := os.ReadFile(b.path(filepath.Join("/sys/bus/sdio/devices", e.Name(), "uevent")))
		if strings.Contains(string(data), "OF_NAME=wifi") {
			return true
		}
	}
	return false
}

// the onboard chip's modules, unloaded when Wi-Fi is off, as upstream does
var brcmModules = []string{"brcmfmac_wcc", "brcmfmac_cyw", "brcmfmac", "brcmutil"}

// wpaUnit and dhcpUnit are the long-running halves, per interface.
func wpaUnit(iface string) string  { return "occu-wpa@" + iface + ".service" }
func dhcpUnit(iface string) string { return "occu-wifi-dhcp@" + iface + ".service" }

// Up brings Wi-Fi up as the settings say, or down when it is switched off. A setup file on the boot
// partition is taken first.
func (b Box) Up(ctx context.Context) error {
	if err := b.TakeBootFile(ctx); err != nil {
		b.logf("wifi: the setup file on the boot partition was not used: %v", err)
	}
	s := b.Settings()
	if !s.Enabled {
		b.logf("wifi: switched off")
		return b.Down(ctx)
	}
	if err := s.Validate(); err != nil {
		return fmt.Errorf("wifi: %s: %w", SettingsFile, err)
	}
	if b.Onboard() {
		b.quiet(ctx, "modprobe", "brcmfmac")
	}
	if !b.waitIface(ctx, s.Iface) {
		return fmt.Errorf("wifi: %s did not appear", s.Iface)
	}
	// a supplicant needs a file, even without a network: it scans then
	if _, err := os.Stat(b.path(ConfFile)); err != nil {
		if err := writeFile(b.path(ConfFile), Conf(s.Country, nil), 0o600); err != nil {
			return err
		}
	}
	b.quiet(ctx, "rfkill", "unblock", "wlan")
	b.quiet(ctx, "iw", "dev", s.Iface, "set", "power_save", "off")
	if err := b.run(ctx, "ip", "link", "set", s.Iface, "up"); err != nil {
		return err
	}
	if err := b.run(ctx, "systemctl", "start", wpaUnit(s.Iface)); err != nil {
		return err
	}
	if err := b.address(ctx, s); err != nil {
		return err
	}
	b.logf("wifi: %s up (%s, preferred %s, route metric %d, country %s)", s.Iface, s.Mode, s.Preferred, s.WLANMetric(), s.Country)
	return nil
}

// address sets up the addressing: DHCP through its unit, or the static address and route here.
func (b Box) address(ctx context.Context, s Settings) error {
	if s.Mode == "dhcp" {
		if err := b.run(ctx, "systemctl", "restart", dhcpUnit(s.Iface)); err != nil {
			return err
		}
		return b.demoteEth(ctx, s)
	}
	b.quiet(ctx, "systemctl", "stop", dhcpUnit(s.Iface))
	ones, _ := parseMask(s.Netmask)
	if err := b.run(ctx, "ip", "-4", "addr", "replace", fmt.Sprintf("%s/%d", s.Address, ones), "dev", s.Iface); err != nil {
		return err
	}
	if s.Gateway != "" {
		if err := b.run(ctx, "ip", "-4", "route", "replace", "default", "via", s.Gateway, "dev", s.Iface, "metric", strconv.Itoa(s.WLANMetric())); err != nil {
			return err
		}
	}
	if len(s.DNS) > 0 {
		var conf strings.Builder
		for _, d := range s.DNS {
			fmt.Fprintf(&conf, "nameserver %s\n", d)
		}
		b.resolvconfAdd(ctx, s.Iface, conf.String())
	}
	return b.demoteEth(ctx, s)
}

// demoteEth: with Wi-Fi preferred, a static Ethernet default route (metric 0, the static branch of
// upstream's hook) would still win; it moves to EthDemotedMetric. dhcp.script's routes (10 and
// up) are above Wi-Fi's 5 already.
func (b Box) demoteEth(ctx context.Context, s Settings) error {
	if s.Preferred != "wlan" {
		return nil
	}
	out, err := b.Run(ctx, "ip", "-4", "route", "show", "default")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// "default via 192.168.1.1 dev eth0" - no metric: 0
		if len(f) >= 5 && f[0] == "default" && f[1] == "via" && f[3] == "dev" && strings.HasPrefix(f[4], "eth") && !strings.Contains(line, " metric ") {
			if err := b.run(ctx, "ip", "-4", "route", "replace", "default", "via", f[2], "dev", f[4], "metric", strconv.Itoa(EthDemotedMetric)); err != nil {
				return err
			}
			b.quiet(ctx, "ip", "-4", "route", "del", "default", "via", f[2], "dev", f[4], "metric", "0")
			b.logf("wifi: the static Ethernet default route moved to metric %d (Wi-Fi is preferred)", EthDemotedMetric)
		}
	}
	return nil
}

// Reload applies changed addressing or a changed preference without dropping the association.
func (b Box) Reload(ctx context.Context) error {
	s := b.Settings()
	if !s.Enabled {
		return b.Down(ctx)
	}
	if err := s.Validate(); err != nil {
		return fmt.Errorf("wifi: %s: %w", SettingsFile, err)
	}
	if s.Mode == "static" {
		b.quiet(ctx, "ip", "-4", "addr", "flush", "dev", s.Iface)
	}
	return b.address(ctx, s)
}

// Down stops the supplicant and DHCP, takes the addresses away, blocks the radio and, for the
// onboard chip, unloads its driver.
func (b Box) Down(ctx context.Context) error {
	s := b.Settings()
	ifaces := []string{s.Iface}
	for _, name := range b.wirelessIfaces() {
		if name != s.Iface {
			ifaces = append(ifaces, name)
		}
	}
	for _, i := range ifaces {
		b.quiet(ctx, "systemctl", "stop", dhcpUnit(i), wpaUnit(i))
		b.quiet(ctx, "/sbin/resolvconf", "-d", i)
		b.quiet(ctx, "ip", "-4", "addr", "flush", "dev", i)
		b.quiet(ctx, "ip", "link", "set", i, "down")
	}
	b.quiet(ctx, "rfkill", "block", "wlan")
	if b.Onboard() {
		b.quiet(ctx, "modprobe", "-r", brcmModules[0], brcmModules[1], brcmModules[2], brcmModules[3])
	}
	return nil
}

// wirelessIfaces are the interfaces with a wireless directory in sysfs.
func (b Box) wirelessIfaces() []string {
	ents, _ := os.ReadDir(b.path("/sys/class/net"))
	var out []string
	for _, e := range ents {
		if _, err := os.Stat(b.path(filepath.Join("/sys/class/net", e.Name(), "wireless"))); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

func (b Box) waitIface(ctx context.Context, iface string) bool {
	wait := b.Wait
	if wait == 0 {
		wait = 15 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		if _, err := os.Stat(b.path(filepath.Join("/sys/class/net", iface))); err == nil {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func maskOf(ip []byte) net.IPMask { return net.IPv4Mask(ip[0], ip[1], ip[2], ip[3]) }

func parseMask(m string) (int, bool) {
	ip := v4(m)
	if ip == nil {
		return 0, false
	}
	ones, bits := maskOf(ip).Size()
	return ones, bits == 32
}

// writeFile writes atomically (the root side; the daemon writes through the helper).
func writeFile(p, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
