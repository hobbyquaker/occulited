package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// ---- firewall -------------------------------------------------------------------------------
//
// /etc/config/firewall.conf was OpenCCU's libfirewall configuration. Since task 157 occulited owns
// the firewall (firewall-rules.json, internal/firewall); the file is read once, to convert it.

// FirewallService is one [SERVICE] section: a named port group with an access level.
type FirewallService struct {
	ID     string
	Ports  []int
	Access string // full, restricted, none
}

// Firewall is the parsed firewall.conf - read once, by the conversion to the rule list (task 157).
type Firewall struct {
	Mode      string // MOST_OPEN or RESTRICTIVE
	IPs       []string
	UserPorts []string
	Services  []FirewallService
}

var firewallSectionRe = regexp.MustCompile(`^\[([^\]]+)\]`)

// FirewallDefaultMode is the mode of a box whose firewall.conf is missing or carries no MODE
// line. It is RESTRICTIVE for two reasons that agree: D-29 decides it ("the firewall default
// becomes RESTRICTIVE ... the CCU's MOST_OPEN default is a compatibility decision we do not have
// to inherit"), and it is what /lib/libfirewall.tcl itself falls back to, so reporting anything
// else describes a box that does not exist. occulited said MOST_OPEN until 2026-09-07 (B-43),
// which meant a freshly flashed box enforced RESTRICTIVE while the Network page displayed
// MOST_OPEN - and the first save on that page made the display true.
const FirewallDefaultMode = "RESTRICTIVE"

// ReadFirewall parses firewall.conf: KEY = value lines, then [SERVICE X] sections with Id, Ports
// and Access.
func (r Root) ReadFirewall() Firewall {
	return ParseFirewall(readFile(r.join("/etc/config/firewall.conf")))
}

// ParseFirewall is ReadFirewall for a firewall.conf's text: this system's own, or the one a CCU
// backup holds (occulited task 142).
func ParseFirewall(text string) Firewall {
	fw := Firewall{Mode: FirewallDefaultMode, IPs: []string{}, UserPorts: []string{}, Services: []FirewallService{}}
	var cur *FirewallService
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := firewallSectionRe.FindStringSubmatch(line); m != nil {
			id := ""
			if f := strings.Fields(m[1]); len(f) == 2 {
				id = f[1]
			}
			fw.Services = append(fw.Services, FirewallService{Ports: []int{}, ID: id})
			cur = &fw.Services[len(fw.Services)-1]
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if cur != nil {
			switch k {
			case "Id":
				cur.ID = v
			case "Ports":
				for _, p := range strings.Fields(v) {
					if n, err := strconv.Atoi(p); err == nil {
						cur.Ports = append(cur.Ports, n)
					}
				}
			case "Access":
				cur.Access = v
			}
			continue
		}
		switch strings.ToUpper(k) {
		case "MODE":
			fw.Mode = v
		case "USERPORTS":
			fw.UserPorts = strings.Fields(v)
		case "IPS":
			fw.IPs = strings.Fields(v)
		}
	}
	return fw
}

// ---- time -----------------------------------------------------------------------------------

// TimeConfig is timezone, NTP and the clock as the box has them.
type TimeConfig struct {
	TZ         string   `json:"tz"`   // /etc/config/TZ: a POSIX string or a zone name
	Zone       string   `json:"zone"` // /etc/config/timezone: the zone name updateTZ.sh derived
	NTPServers []string `json:"ntp_servers"`
	// NTPSource is where chrony's servers come from (occulited task 138): "dhcp" - the lease's
	// servers, NTPServers behind them as the fallback - or "manual" - NTPServers alone.
	NTPSource string `json:"ntp_source"`
	// NTPServersActive are the servers chrony was started with: lite-chrony's server lines in
	// /var/etc/chrony.conf, so a page can say what "from DHCP" resolved to. Empty where there is
	// no chrony (the container) or it has not started yet.
	NTPServersActive []string  `json:"ntp_servers_active"`
	HasNTP           bool      `json:"has_ntp"` // /var/status/hasNTP: chrony accepted at least one server
	Now              time.Time `json:"now"`
}

// The NTP source (occulited task 138). The fork's lite-chrony puts the DHCP lease's servers in
// front of NTPSERVERS unless NoDHCPNTPFile exists - so "manual" is that file plus NTPSERVERS, and
// "from DHCP" is no file with the template's servers as the fallback behind the lease's.
const (
	NTPSourceDHCP   = "dhcp"
	NTPSourceManual = "manual"
	NoDHCPNTPFile   = "/etc/config/noDHCPNTP"
	ntpTemplate     = "/etc/config_templates/ntpclient"
	chronyConf      = "/var/etc/chrony.conf"
)

var (
	ntpServersRe   = regexp.MustCompile(`(?m)^\s*NTPSERVERS\s*=\s*'?([^'\n]*)'?\s*$`)
	chronyServerRe = regexp.MustCompile(`(?m)^\s*(?:server|pool)\s+(\S+)`)
)

// ntpServersIn is NTPSERVERS of an ntpclient file, nil when it has none.
func ntpServersIn(text string) []string {
	if m := ntpServersRe.FindStringSubmatch(text); m != nil {
		return strings.Fields(m[1])
	}
	return nil
}

// ReadTime reads /etc/config/TZ, /etc/config/timezone, NTPSERVERS from /etc/config/ntpclient, the
// NTP source and the servers chrony runs with.
func (r Root) ReadTime() TimeConfig {
	t := TimeConfig{TZ: strings.TrimSpace(readFile(r.join("/etc/config/TZ"))), Zone: strings.TrimSpace(readFile(r.join("/etc/config/timezone"))), NTPServers: []string{}, NTPServersActive: []string{}, Now: time.Now()}
	if s := ntpServersIn(readFile(r.join("/etc/config/ntpclient"))); s != nil {
		t.NTPServers = s
	}
	t.NTPSource = r.ntpSource(t.NTPServers)
	for _, m := range chronyServerRe.FindAllStringSubmatch(readFile(r.join(chronyConf)), -1) {
		t.NTPServersActive = append(t.NTPServersActive, m[1])
	}
	if _, err := os.Stat(r.join("/var/status/hasNTP")); err == nil {
		t.HasNTP = true
	}
	return t
}

// ntpSource is "manual" when noDHCPNTP exists, and also - the maintainer's decision 2 on task 138 -
// when servers were entered that are not the template's (a system that set its own before the
// source existed shows what it meant; the file is created only at the next save, never here).
// An empty list, the template's servers and lite-chrony's legacy ntp.homematic.com are "dhcp".
func (r Root) ntpSource(servers []string) string {
	if _, err := os.Stat(r.join(NoDHCPNTPFile)); err == nil {
		return NTPSourceManual
	}
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "ntp.homematic.com") || sameSet(servers, r.ntpTemplateServers()) {
		return NTPSourceDHCP
	}
	return NTPSourceManual
}

// ntpTemplateServers are the template's servers (the de.pool on the image), nil without it.
func (r Root) ntpTemplateServers() []string {
	return ntpServersIn(readFile(r.join(ntpTemplate)))
}

// sameSet says whether a and b hold the same names, order and repetition aside.
func sameSet(a, b []string) bool {
	in := func(x []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range x {
			m[s] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	if len(ma) != len(mb) {
		return false
	}
	for s := range ma {
		if !mb[s] {
			return false
		}
	}
	return true
}

// Zone is one line of zone.tab.
type Zone struct {
	Name    string `json:"name"` // Europe/Berlin
	Country string `json:"country"`
	Code    string `json:"code"`
	Comment string `json:"comment,omitempty"`
}

// Zones lists the IANA zones the box knows, from zone.tab with country names from iso3166.tab.
func (r Root) Zones() []Zone {
	countries := map[string]string{}
	for _, line := range strings.Split(readFile(r.join("/usr/share/zoneinfo/iso3166.tab")), "\n") {
		if f := strings.SplitN(line, "\t", 2); len(f) == 2 && !strings.HasPrefix(line, "#") {
			countries[f[0]] = strings.TrimSpace(f[1])
		}
	}
	var zones []Zone
	for _, line := range strings.Split(readFile(r.join("/usr/share/zoneinfo/zone.tab")), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		z := Zone{Code: f[0], Country: countries[f[0]], Name: f[2]}
		if len(f) > 3 {
			z.Comment = f[3]
		}
		zones = append(zones, z)
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	if zones == nil {
		zones = []Zone{}
	}
	return zones
}

var zoneNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z][A-Za-z0-9_+-]*){0,2}$`)

// SetTimezone writes the zone name into /etc/config/TZ (what cp_time.cgi does for zone.tab
// entries), keeps time.conf's TIMEZONE in step, and runs /bin/updateTZ.sh, hwclock and
// SetInterfaceClock as the WebUI does.
func (r Root) SetTimezone(ctx context.Context, run Runner, zone string) error {
	if run == nil {
		run = ExecRunner
	}
	if !zoneNameRe.MatchString(zone) || path.Clean(zone) != zone {
		return errors.New("zone: not a zone name")
	}
	if _, err := os.Stat(r.join("/usr/share/zoneinfo/" + zone)); err != nil {
		return fmt.Errorf("zone: %s is not installed", zone)
	}
	if err := writeFileAtomic(r.join("/etc/config/TZ"), []byte(zone+"\n"), 0o644); err != nil {
		return err
	}
	conf := setProperty(readFile(r.join("/etc/config/time.conf")), "TIMEZONE", zone)
	if err := writeFileAtomic(r.join("/etc/config/time.conf"), []byte(conf), 0o644); err != nil {
		return err
	}
	if out, err := run(ctx, r.join("/bin/updateTZ.sh")); err != nil {
		return fmt.Errorf("updateTZ.sh: %w: %s", err, strings.TrimSpace(string(out)))
	}
	r.pushClock(ctx, run)
	return nil
}

// pushClock is what the WebUI does after every clock-related change: the RTC gets the system
// time and rfd is told (it timestamps radio traffic).
func (r Root) pushClock(ctx context.Context, run Runner) {
	_, _ = run(ctx, "/sbin/hwclock", "-wu")
	port := "2001"
	if m := regexp.MustCompile(`set EQ3_SERVICE_RFD_PORT (\d+)`).FindStringSubmatch(readFile(r.join("/etc/eq3services.ports.tcl"))); m != nil {
		port = m[1]
	}
	_, _ = run(ctx, "/bin/SetInterfaceClock", "127.0.0.1:"+port)
}

var ntpHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252}[A-Za-z0-9])?$`)

// SetNTP sets the NTP source and servers (occulited task 138), then restarts chrony.
//
//   - source "manual": servers alone - NTPSERVERS in /etc/config/ntpclient and noDHCPNTP created,
//     so lite-chrony leaves the lease's servers out. At least one server.
//   - source "dhcp": noDHCPNTP removed and NTPSERVERS back to the template's servers, the fallback
//     behind the lease's (decision 1). servers must be empty: a list of its own would read back as
//     "manual" (decision 2).
//   - source "": the request before the source existed - a list is "manual", an empty one "dhcp".
//
// The restart goes through the service manager, as SetSSH's does (openccu-lite task 115): on the
// systemd product /etc/init.d/S46chronyd is the init-script wrapper, which turned the call into
// `systemctl restart chrony.service` anyway; asking the manager directly is the same restart
// without the detour. svc == nil keeps the init-script call for development and the busybox tests.
func (r Root) SetNTP(ctx context.Context, run Runner, svc ServiceManager, source string, servers []string) error {
	if run == nil {
		run = ExecRunner
	}
	if source == "" {
		source = NTPSourceDHCP
		if len(servers) > 0 {
			source = NTPSourceManual
		}
	}
	for _, s := range servers {
		if !ntpHostRe.MatchString(s) && net.ParseIP(s) == nil {
			return fmt.Errorf("ntp_servers: %q is not a host name or address", s)
		}
	}
	switch source {
	case NTPSourceManual:
		if len(servers) == 0 {
			return errors.New("ntp_servers: manual needs at least one server")
		}
	case NTPSourceDHCP:
		if len(servers) > 0 {
			return errors.New("ntp_servers: only with ntp_source manual; from DHCP the template's servers are the fallback")
		}
		servers = r.ntpTemplateServers()
	default:
		return fmt.Errorf("ntp_source: %q is neither dhcp nor manual", source)
	}
	if err := writeFileAtomic(r.join("/etc/config/ntpclient"), []byte("NTPSERVERS='"+strings.Join(servers, " ")+"'\n"), 0o644); err != nil {
		return err
	}
	if source == NTPSourceManual {
		if err := writeFileAtomic(r.join(NoDHCPNTPFile), nil, 0o644); err != nil {
			return err
		}
	} else if err := Priv.Remove(r.join(NoDHCPNTPFile)); err != nil {
		return err
	}
	if svc != nil {
		if out, err := svc.Control(ctx, "chrony", "restart"); err != nil {
			return fmt.Errorf("chronyd restart: %w: %s", err, strings.TrimSpace(out))
		}
		return nil
	}
	if out, err := run(ctx, r.join("/etc/init.d/S46chronyd"), "restart"); err != nil {
		return fmt.Errorf("chronyd restart: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ClockWindowYears is how far past the image's build a clock is still trusted (openccu-lite task
// 299, the maintainer's choice: systemd's own cap for a clock advanced from its built-in epoch). The
// same bound holds on every path - the fork's clock gate for a real-time clock and for NTP, and the
// time set by hand here. A clock far ahead would push the HmIP security counter past 2^32 for good
// (eq-3/occu#134); one before the build is wrong on its face.
const ClockWindowYears = 15

// ClockWindow is the range a time set by hand is accepted in: from the image's build (/VERSION's
// modification time, as the fork's clock gate reads it) to ClockWindowYears after it. Without a
// plausible /VERSION (a development root) the years 2020 to 2100, as before.
func (r Root) ClockWindow() (lo, hi time.Time) {
	if st, err := os.Stat(r.join("/VERSION")); err == nil && st.ModTime().Year() >= 2020 {
		lo = st.ModTime().UTC()
		return lo, lo.AddDate(ClockWindowYears, 0, 0)
	}
	return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 12, 31, 23, 59, 59, 0, time.UTC)
}

// SetClock sets the system time by hand (a box without NTP), then pushes it to the RTC and rfd, and
// marks the clock trusted (ClockStateFile "manual"), which releases a hmipserver held back for one.
func (r Root) SetClock(ctx context.Context, run Runner, t time.Time) error {
	if run == nil {
		run = ExecRunner
	}
	if lo, hi := r.ClockWindow(); t.Before(lo) || t.After(hi) {
		return fmt.Errorf("time: implausible: %s is outside the range this image trusts, %s to %s (from its build to %d years after)", t.UTC().Format(time.RFC3339), lo.Format("2006-01-02"), hi.Format("2006-01-02"), ClockWindowYears)
	}
	if out, err := run(ctx, "date", "-u", "-s", t.UTC().Format("2006-01-02 15:04:05")); err != nil {
		return fmt.Errorf("date: %w: %s", err, strings.TrimSpace(string(out)))
	}
	r.pushClock(ctx, run)
	if _, err := os.Stat(r.join(filepath.Dir(ClockStateFile))); err == nil {
		if err := Priv.WriteFile(r.join(ClockStateFile), []byte("manual\n"), 0o644); err != nil {
			slog.Warn("clock set by hand: the clock state was not written", "err", err)
		}
	}
	return nil
}

// ---- LEDs -----------------------------------------------------------------------------------

// LED is one onboard LED as /var/hm_mode names it.
type LED struct {
	Name    string `json:"name"` // green, red, yellow
	Path    string `json:"path"`
	Trigger string `json:"trigger"` // the active trigger from sysfs
	Normal  string `json:"normal"`  // HM_LED_*_MODE2: what "on" means for this box
}

// LEDs is the LED state: the list and the disableOnboardLED marker.
type LEDs struct {
	Disabled bool  `json:"disabled"`
	LEDs     []LED `json:"leds"`
}

var triggerActiveRe = regexp.MustCompile(`\[([^\]]+)\]`)

// ReadLEDs lists the onboard LEDs the box knows about (none on a VM).
func (r Root) ReadLEDs() LEDs {
	out := LEDs{LEDs: []LED{}}
	if _, err := os.Stat(r.join("/etc/config/disableOnboardLED")); err == nil {
		out.Disabled = true
	}
	kv := r.HMMode()
	for _, c := range []string{"GREEN", "RED", "YELLOW"} {
		p := kv["HM_LED_"+c]
		if p == "" {
			continue
		}
		l := LED{Name: strings.ToLower(c), Path: p, Normal: kv["HM_LED_"+c+"_MODE2"]}
		if m := triggerActiveRe.FindStringSubmatch(readFile(r.join(p + "/trigger"))); m != nil {
			l.Trigger = m[1]
		}
		if l.Trigger == "" && !strings.HasPrefix(p, "/sys") {
			continue
		}
		out.LEDs = append(out.LEDs, l)
	}
	return out
}

// ledClassDir is where /var/hm_mode's LEDs live.
const ledClassDir = "/sys/class/leds"

// setLEDTrigger sets one LED's trigger through the helper's led operation (occulited B-139): a
// frame of one entry, the LED by its name under the class directory. A sysfs attribute is not
// renamed into place, so WriteFile cannot do it; until B-139 it was a shell redirect. One LED per
// frame, so one the helper refuses does not keep the others dark or lit.
func (r Root) setLEDTrigger(path, trigger string) error {
	if filepath.Dir(path) != ledClassDir {
		return fmt.Errorf("%s is not under %s", path, ledClassDir)
	}
	return Priv.WriteLEDs(r.join(ledClassDir), []priv.LEDWrite{{LED: filepath.Base(path), Trigger: trigger}})
}

// SetLEDsDisabled writes the marker S99SetupLEDs honours at boot and applies it now.
func (r Root) SetLEDsDisabled(disabled bool) error {
	marker := r.join("/etc/config/disableOnboardLED")
	if disabled {
		if err := touch(marker, 0o644); err != nil {
			return err
		}
	} else if err := remove(marker); err != nil {
		return err
	}
	var failed error
	for _, l := range r.ReadLEDs().LEDs {
		trigger := l.Normal
		if disabled || trigger == "" {
			trigger = "none"
		}
		if err := r.setLEDTrigger(l.Path, trigger); err != nil && failed == nil {
			failed = fmt.Errorf("led %s: %w", l.Name, err)
		}
	}
	return failed
}
