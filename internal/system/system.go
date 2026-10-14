// Package system is occulited's view of the box: host status, the radio inventory, services, the
// log and the installed addons. Everything reads the files the firmware already writes; nothing
// here detects hardware itself. Service and log access go through small interfaces (D-30): systemd
// and the journal on every product since D-39, NoInit in the development mode without systemd
// (task 187 removed the busybox init implementation).
package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Root lets tests and development point everything at a fake filesystem root.
type Root string

func (r Root) join(p string) string { return filepath.Join(string(r), p) }

// onBox is a joined path as the box spells it, without the root in front.
func (r Root) onBox(full string) string {
	if rel, err := filepath.Rel(filepath.Join(string(r), "/"), full); err == nil && !strings.HasPrefix(rel, "..") {
		return "/" + rel
	}
	return full
}

// Path is a path under the root (the root itself on a system).
func (r Root) Path(p string) string { return r.join(p) }

// Version is the content of /VERSION.
type Version struct {
	Version  string `json:"version"`
	Product  string `json:"product"`
	Platform string `json:"platform"`
	Variant  string `json:"variant,omitempty"`
	// Lite is openccu-lite's own semantic version (D-44): "1.0.0-alpha.0"; on an image built
	// under D-37 it is the old release suffix ("0-beta.2"), on OpenCCU it is empty.
	Lite string `json:"lite,omitempty"`
}

// Full is the release identity the feed and the file names carry: the lite version on a lite
// image (openccu-lite-<product>-<lite>.zip), the OpenCCU version otherwise.
func (v Version) Full() string {
	if v.Lite != "" {
		return v.Lite
	}
	return v.Version
}

func (r Root) ReadVersion() Version {
	var v Version
	for k, val := range readKV(r.join("/VERSION")) {
		switch k {
		case "VERSION":
			v.Version = val
		case "PRODUCT":
			v.Product = val
		case "PLATFORM":
			v.Platform = val
		case "VARIANT":
			v.Variant = val
		case "LITE":
			v.Lite = val
		}
	}
	return v
}

// Status is the landing-page summary.
type Status struct {
	Hostname string  `json:"hostname"`
	Version  Version `json:"version"`
	// Occulited is occulited's own version (main.version, task 133): the commit it was built from,
	// -dirty or -hot after it (occulited task 16); OcculitedCommit the bare hash
	Occulited       string    `json:"occulited_version,omitempty"`
	OcculitedCommit string    `json:"occulited_commit,omitempty"`
	UptimeS         int64     `json:"uptime_s"`
	Load            []float64 `json:"load"`
	// CPUs is the number of CPUs the load average is read against (occulited task 37)
	CPUs     int       `json:"cpus"`
	MemTotal int64     `json:"mem_total_kb"`
	MemAvail int64     `json:"mem_available_kb"`
	Disks    []Disk    `json:"disks"`
	Time     time.Time `json:"time"`
	// Timezone is the zone name (Europe/Berlin) the Network page shows too; TZ is the POSIX
	// string of /etc/config/TZ (CET-1CEST-2,M3.5.0/02:00:00,…), sent only when it is not the same.
	// The Status page showed the POSIX string, which nobody reads and which wraps on a phone.
	Timezone  string `json:"timezone"`
	TZ        string `json:"tz,omitempty"`
	HMMode    string `json:"hm_mode"`
	Recovered bool   `json:"meta_recovered,omitempty"`
	// UncleanShutdown: the marker S00watchdog/lite-watchdog-marker leaves when the box was not
	// shut down cleanly, with the time it was written. On OpenCCU monit read it and raised a
	// ReGa alarm variable; there is no ReGa here, so the Status page is its consumer (D-41).
	UncleanShutdown *UncleanShutdown `json:"unclean_shutdown,omitempty"`
	// Container: "lxc" (or "oci") when this box is one (task 34), empty on a machine or a VM.
	// The Status page's update section reads it: a container is updated by swapping the
	// template on the host, not from inside.
	Container string `json:"container,omitempty"`
	// Clock (task 94): the boot's clock gate, /run/occulite/clock-state, and after a timeout
	// whether chrony has synchronised since; set by the API (ClockCheck), absent without the file.
	Clock *ClockStatus `json:"clock,omitempty"`
}

// UncleanShutdown is /var/status/uncleanShutdown as the Status page reports it.
type UncleanShutdown struct {
	At time.Time `json:"at"`
}

// Disk is one mount point of interest.
type Disk struct {
	Mount   string `json:"mount"`
	TotalKB int64  `json:"total_kb"`
	UsedKB  int64  `json:"used_kb"`
}

// timezoneNames is the timezone as the Status page shows it: the zone name updateTZ.sh derived
// into /etc/config/timezone, and the POSIX string of /etc/config/TZ beside it when that says
// something else. Without the name the TZ file is all there is - and SetTimezone (like the
// WebUI's cp_time.cgi) writes a zone name into it - so then it is the name.
func (r Root) timezoneNames() (name, posix string) {
	zone := strings.TrimSpace(readFile(r.join("/etc/config/timezone")))
	tz := strings.TrimSpace(readFile(r.join("/etc/config/TZ")))
	if zone == "" {
		return tz, ""
	}
	if tz == zone {
		return zone, ""
	}
	return zone, tz
}

// ReadStatus assembles the status from /proc and the firmware's files.
func (r Root) ReadStatus() Status {
	s := Status{Time: time.Now(), Version: r.ReadVersion(), Load: []float64{}}
	if b, err := os.ReadFile(r.join("/proc/sys/kernel/hostname")); err == nil {
		s.Hostname = strings.TrimSpace(string(b))
	} else if h, err := os.Hostname(); err == nil {
		s.Hostname = h
	}
	if b, err := os.ReadFile(r.join("/proc/uptime")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			up, _ := strconv.ParseFloat(f[0], 64)
			s.UptimeS = int64(up)
		}
	}
	if b, err := os.ReadFile(r.join("/proc/loadavg")); err == nil {
		for i, f := range strings.Fields(string(b)) {
			if i > 2 {
				break
			}
			v, _ := strconv.ParseFloat(f, 64)
			s.Load = append(s.Load, v)
		}
	}
	s.CPUs = runtime.NumCPU()
	for k, v := range readMeminfo(r.join("/proc/meminfo")) {
		switch k {
		case "MemTotal":
			s.MemTotal = v
		case "MemAvailable":
			s.MemAvail = v
		}
	}
	s.Timezone, s.TZ = r.timezoneNames()
	s.HMMode = r.HMMode()["HM_MODE"]
	for _, m := range []string{"/", "/usr/local"} {
		if d, ok := diskUsage(r.join(m)); ok {
			d.Mount = m
			s.Disks = append(s.Disks, d)
		}
	}
	if s.Disks == nil {
		s.Disks = []Disk{}
	}
	// the marker lives on the /var tmpfs and the watchdog unit writes it at boot only when the
	// previous shutdown was unclean, so its presence is the answer and its mtime is when this
	// boot noticed (B-25, D-41)
	if st, err := os.Stat(r.join("/var/status/uncleanShutdown")); err == nil {
		s.UncleanShutdown = &UncleanShutdown{At: st.ModTime()}
	}
	s.Container = r.Container()
	return s
}

// HMMode returns the KEY='value' inventory of /var/hm_mode written by S47InitRFHardware.
func (r Root) HMMode() map[string]string {
	return readKV(r.join("/var/hm_mode"))
}

// Radio is the radio inventory the UI shows: one entry per protocol the firmware assigned a
// module to, straight from /var/hm_mode and the /var/rf_* files.
type Radio struct {
	Mode       string            `json:"mode"`
	Host       string            `json:"host"`
	Modules    []RadioModule     `json:"modules"`
	LEDs       map[string]string `json:"leds"`
	Interfaces []Interface       `json:"interfaces"`
}

// RadioModule is a protocol's module as the firmware detected it.
type RadioModule struct {
	Protocol      string `json:"protocol"` // BidCos-RF or HmIP-RF
	Device        string `json:"device"`
	DeviceNode    string `json:"device_node,omitempty"`
	DeviceType    string `json:"device_type,omitempty"`
	Serial        string `json:"serial,omitempty"`
	SGTIN         string `json:"sgtin,omitempty"`
	Firmware      string `json:"firmware,omitempty"`
	Address       string `json:"address,omitempty"`
	AddressActive string `json:"address_active,omitempty"`
}

// Interface is one entry of InterfacesList.xml, with the XML-RPC clients currently registered
// with it. Subscribers ride along on GET /radio rather than having an endpoint of their own: the
// Radio page already polls that, the files are on a tmpfs so reading them is free, and the list
// changes only when a client registers or deregisters. A second poll would have doubled the
// page's request rate to show data that is stable for hours.
type Interface struct {
	Name        string                `json:"name"`
	URL         string                `json:"url"`
	Info        string                `json:"info"`
	Subscribers []InterfaceSubscriber `json:"subscribers"`
}

// ReadRadio reads the inventory.
func (r Root) ReadRadio() Radio {
	kv := r.HMMode()
	rad := Radio{Mode: kv["HM_MODE"], Host: kv["HM_HOST"], Modules: []RadioModule{}, LEDs: map[string]string{}, Interfaces: []Interface{}}
	for _, p := range []struct{ prefix, proto string }{{"HM_HMRF", "BidCos-RF"}, {"HM_HMIP", "HmIP-RF"}} {
		if kv[p.prefix+"_DEV"] == "" {
			continue
		}
		rad.Modules = append(rad.Modules, RadioModule{
			Protocol: p.proto, Device: kv[p.prefix+"_DEV"], DeviceNode: kv[p.prefix+"_DEVNODE"], DeviceType: kv[p.prefix+"_DEVTYPE"],
			Serial: kv[p.prefix+"_SERIAL"], SGTIN: kv[p.prefix+"_SGTIN"], Firmware: kv[p.prefix+"_VERSION"],
			Address: kv[p.prefix+"_ADDRESS"], AddressActive: kv[p.prefix+"_ADDRESS_ACTIVE"],
		})
	}
	for k, v := range kv {
		if strings.HasPrefix(k, "HM_LED_") {
			rad.LEDs[strings.ToLower(strings.TrimPrefix(k, "HM_LED_"))] = v
		}
	}
	rad.Interfaces = parseInterfacesList(readFile(r.join("/etc/config/InterfacesList.xml")))
	for i := range rad.Interfaces {
		rad.Interfaces[i].Subscribers = r.InterfaceSubscribers(rad.Interfaces[i].Name)
	}
	return rad
}

var ipcRe = regexp.MustCompile(`(?s)<ipc>(.*?)</ipc>`)
var tagRe = regexp.MustCompile(`<(name|url|info)>\s*(.*?)\s*</(name|url|info)>`)

func parseInterfacesList(xml string) []Interface {
	out := []Interface{}
	for _, m := range ipcRe.FindAllStringSubmatch(xml, -1) {
		var i Interface
		for _, t := range tagRe.FindAllStringSubmatch(m[1], -1) {
			switch t[1] {
			case "name":
				i.Name = t[2]
			case "url":
				i.URL = t[2]
			case "info":
				i.Info = t[2]
			}
		}
		if i.Name != "" {
			i.Subscribers = []InterfaceSubscriber{}
			out = append(out, i)
		}
	}
	return out
}

// --- small readers ---

// readFile reads a file the daemon is allowed to see, falling back to the privilege helper when
// the file is root-only.
//
// Since task 17 occulited runs as occulite, and several of the files the firmware writes are
// 0600 or 0640 root:root - netconfig, rfd.conf, hs485d.conf. Reading them directly returned "",
// which does not look like an error anywhere upstream of here: the Network page showed an empty
// configuration for a box that had one, and a save would then have written that emptiness back
// (B-13). The fallback is exact - the helper's ReadPaths allowlist decides what may be read, and
// a file that is not on it stays unreadable.
func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err == nil {
		return string(b)
	}
	if !errors.Is(err, fs.ErrPermission) || Priv == nil {
		return ""
	}
	pb, perr := Priv.ReadFile(path)
	if perr != nil {
		return ""
	}
	return string(pb)
}

// readFileErr is readFile for a caller that rewrites the file from what it read: a missing file is
// ("", false, nil), a file that could not be read - a closed directory the helper did not open, a
// helper that did not answer - is an error, never an empty file. hmipserver's crRFD directory is
// closed to occulited since B-253; a stat there failed, the connection choice read as automatic and
// its write replaced hmip_user.conf with the two choice lines, dropping the local key's (B-271).
func readFileErr(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return string(b), true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if !errors.Is(err, fs.ErrPermission) || Priv == nil {
		return "", false, err
	}
	pb, perr := Priv.ReadFile(path)
	switch {
	case perr == nil:
		return string(pb), true, nil
	case errors.Is(perr, fs.ErrNotExist):
		return "", false, nil
	}
	return "", false, fmt.Errorf("%s: %w", path, perr)
}

// readDir lists the names in a directory the daemon may see, falling back to the privilege helper
// when the directory is closed to it: hmipserver's data directory is 0700 since openccu-lite
// B-253, and what occulited needs from it are names (which devices, which modules have files
// there). The helper's ListDirs allowlist decides which directory that may be; any other closed
// directory, and a missing one, is an empty list.
func readDir(path string) []string {
	entries, err := os.ReadDir(path)
	if err == nil {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	if !errors.Is(err, fs.ErrPermission) || Priv == nil {
		return nil
	}
	names, perr := Priv.ListDir(path)
	if perr != nil {
		return nil
	}
	return names
}

// readKV parses KEY=value and KEY='value' lines (the shell-sourceable files the firmware writes).
func readKV(path string) map[string]string {
	out := map[string]string{}
	text := readFile(path)
	if text == "" {
		return out
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// ReadUsage is the system history's reading (occulited task 37): the 1-minute load average and the
// memory in use in per cent of MemTotal (-1 when /proc/meminfo has no MemAvailable); false when
// /proc/loadavg cannot be read.
func (r Root) ReadUsage() (load float64, memPct int, ok bool) {
	b, err := os.ReadFile(r.join("/proc/loadavg"))
	if err != nil {
		return 0, 0, false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, 0, false
	}
	if load, err = strconv.ParseFloat(f[0], 64); err != nil {
		return 0, 0, false
	}
	mi := readMeminfo(r.join("/proc/meminfo"))
	total, okT := mi["MemTotal"]
	avail, okA := mi["MemAvailable"]
	memPct = -1
	if okT && okA && total > 0 {
		memPct = int(math.Round(float64(total-avail) / float64(total) * 100))
	}
	return load, memPct, true
}

func readMeminfo(path string) map[string]int64 {
	out := map[string]int64{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, _ := strconv.ParseInt(fields[0], 10, 64)
		out[k] = n
	}
	return out
}

// LANGateway is one [Interface N] section of /etc/config/rfd.conf that is not the built-in
// module: a HM-LGW (Type = Lan Interface) with its serial and address, or an HM-CFG-LAN.
type LANGateway struct {
	Index   int               `json:"index"`
	Type    string            `json:"type"`
	Name    string            `json:"name,omitempty"`
	Serial  string            `json:"serial,omitempty"`
	Address string            `json:"address,omitempty"`
	HasKey  bool              `json:"has_key"`
	Raw     map[string]string `json:"raw,omitempty"`
	// key is the section's "Encryption Key". It is deliberately not in Raw and not exported:
	// Raw goes out over the API, and the key must not (B-23). WriteLANGateways reads it through
	// Key() to keep a key the caller did not resend.
	key string
}

// Key returns the gateway's stored encryption key. Only the write path uses it; it never appears
// in a response.
func (g LANGateway) Key() string { return g.key }

// ReadRFDInterfaces parses the [Interface N] sections of rfd.conf. The first CCU2/RPI-RF-MOD
// entry is the module the Radio page already shows from /var/hm_mode; everything else is a
// LAN gateway.
func (r Root) ReadRFDInterfaces() []LANGateway {
	return r.readInterfaceSections("/etc/config/rfd.conf")
}

func (r Root) readInterfaceSections(path string) []LANGateway {
	var out []LANGateway
	var cur *LANGateway
	for _, line := range strings.Split(readFile(r.join(path)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			cur = nil
			var n int
			if _, err := fmt.Sscanf(line, "[Interface %d]", &n); err == nil {
				out = append(out, LANGateway{Index: n, Raw: map[string]string{}})
				cur = &out[len(out)-1]
			}
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "Encryption Key" {
			// never into Raw: Raw is serialised into the API answer (B-23)
			cur.key = v
			cur.HasKey = v != ""
			continue
		}
		cur.Raw[k] = v
		switch k {
		case "Type":
			cur.Type = v
		case "Serial Number":
			cur.Serial = v
		case "Address", "IP Address":
			cur.Address = v
		case "Name":
			cur.Name = v
		}
	}
	if out == nil {
		out = []LANGateway{}
	}
	return out
}

// SecurityKeySet reports whether the box has a user security key (Zentralenschlüssel): the
// WebUI's test is `crypttool -v -t 0`, which fails when a user key replaced the default.
func (r Root) SecurityKeySet(ctx context.Context) (set bool, known bool) {
	tool := r.join("/bin/crypttool")
	if _, err := os.Stat(tool); err != nil || string(r) != "/" {
		return false, false
	}
	res, err := Priv.Run(ctx, tool, []string{"-v", "-t", "0"}, nil)
	return err != nil || res.Exit != 0, true
}
