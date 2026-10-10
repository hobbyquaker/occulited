package radio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The run step (task 129, phase 2): the detection, the plan and the render written to the box.
// occu-init-rf-hardware.service runs it once at boot, as root, in the slot upstream's
// S47InitRFHardware had; it writes every file the old chain's S47, S49, S60multimacd, S61rfd and
// S62HMServer wrote, and the activation markers the daemon units condition on. The daemons
// themselves are started by their units from the rendered command lines, with the root steps
// of Prep, Ready and Stopped around them.

// RunDir is where the run leaves its results for the units and the check: modules.json (the
// detection), plan.json, render.json, the daemons' environment files (<daemon>.env) and the
// activation markers (<daemon>.enabled).
const RunDir = ShadowDir

// Daemons are the interface daemons in boot order; their unit names are <name>.service.
var Daemons = []string{"multimacd", "rfd", "hmipserver", "hs485d", "hmlangw"}

// RunReport is what Run did.
type RunReport struct {
	Render  ShadowRender
	Written []string // the files written, box paths
	Enabled []string // the daemons the markers enable
}

// ErrDaemonsRunning is Run's refusal while an interface daemon holds the module: the detection
// resets the module and could only fail against a busy UART, and the plan it would write would
// switch a working stack off.
var ErrDaemonsRunning = errors.New("the interface daemons are running; stop them before the detection runs again")

// Run detects, plans, renders and writes.
func Run(ctx context.Context, root string, d Detector, logf func(string, ...any)) (RunReport, error) {
	if d.Root == "" {
		d.Root = root
	}
	r := RunReport{}
	unlock, err := Lock(root)
	if err != nil {
		return r, err
	}
	defer unlock()
	if active := activeDaemons(ctx, d.run()); len(active) > 0 {
		return r, fmt.Errorf("%w (%s)", ErrDaemonsRunning, strings.Join(active, ", "))
	}
	env := ParseKV(readFile(d.path("/var/hm_mode")))
	if d.Host == "" {
		d.Host = env["HM_HOST"]
	}
	det := d.Detect(ctx)
	for _, l := range det.Log {
		logf("detect: %s", l)
	}
	in := Load(ctx, root, d.Run, det)
	return write(ctx, root, d, det, in, MakePlan(in), logf)
}

// write renders a plan and writes the box: Run's step after the detection, and the hotplug's after
// its rescan.
func write(ctx context.Context, root string, d Detector, det Detection, in Inputs, p Plan, logf func(string, ...any)) (RunReport, error) {
	r := RunReport{}
	for _, n := range p.Notes {
		logf("plan: %s", n)
	}
	f := Render(in, p)
	r.Render = ShadowRender{At: time.Now(), Detection: det, Plan: p, Files: f, RFDConfBefore: in.RFDConf}
	w := &writer{d: d, report: &r}

	// what S47 moved and copied on the userfs
	if p.IDsInvalid {
		old := d.path("/etc/config/ids")
		if exists(old) {
			moved := old + "_old-" + time.Now().Format("20060102-150405")
			if err := os.Rename(old, moved); err != nil {
				return r, fmt.Errorf("moving %s aside: %w", old, err)
			}
			logf("run: /etc/config/ids carried no usable address, moved to %s", filepath.Base(moved))
		}
	}
	if in.TemplateHmIPNetworkKey && !in.HmIPNetworkKey {
		if err := w.file("/etc/config/hmip_networkkey.conf", readFile(d.path("/etc/config_templates/hmip_networkkey.conf")), 0o600, "hmipserver", "hmipserver"); err != nil {
			return r, err
		}
	}

	// /var/hm_mode and the RF files (S47)
	if err := w.file("/var/hm_mode", f.HMMode, 0o644, "", ""); err != nil {
		return r, err
	}
	var names []string
	for n := range f.VarFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := w.file("/var/"+n, f.VarFiles[n], 0o644, "", ""); err != nil {
			return r, err
		}
	}

	// /etc/config/ids and crypttool.cfg, which eq3configd's unit prepared before its daemon
	// (task 163). The daemon is gone; these are not its files. The copy happens once, on a system
	// that has no ids yet and a BidCos address to write: afterwards rfd owns the file and rewrites
	// it itself, and overwriting it would take a paired system's address away.
	if f.VarFiles["rf_address"] != "" && !in.hasIDs() {
		if err := w.file("/etc/config/ids", f.VarFiles["ids"], 0o644, "rfd", "rfd"); err != nil {
			return r, err
		}
		logf("run: /etc/config/ids written from the module's address")
	}
	if err := ensureCryptToolConfig(d.path("/etc/config/crypttool.cfg")); err != nil {
		logf("run: %s", err)
	}

	// the daemons' files (S49, S60multimacd, S61rfd, S62HMServer)
	if f.RFDConf != "" {
		if err := os.MkdirAll(d.path("/etc/config/rfd"), 0o755); err != nil {
			return r, err
		}
		if f.RFDConf != in.RFDConf || !in.RFDConfExists {
			if err := w.file("/etc/config/rfd.conf", f.RFDConf, 0o640, "root", "rfd"); err != nil {
				return r, err
			}
		} else {
			w.own("/etc/config/rfd.conf", 0o640, "root", "rfd")
		}
	}
	for _, c := range []struct {
		path, content string
		mode          os.FileMode
		user, group   string
	}{
		{"/var/etc/rfd.conf", f.VarRFDConf, 0o640, "root", "rfd"},
		{"/var/etc/multimacd.conf", f.MultimacdConf, 0o644, "", ""},
		{"/var/etc/crRFD.conf", f.CrRFDConf, 0o644, "", ""},
		{"/var/etc/HMServer.conf", f.HMServerConf, 0o644, "", ""},
		// root:hs485d 0640 like rfd.conf beside it: the file carries the wired LAN gateway's
		// encryption key, and 0644 handed it to every addon user (B-163)
		{"/var/etc/hs485d.conf", f.VarHS485DConf, 0o640, "root", "hs485d"},
		{"/etc/config/InterfacesList.xml", f.InterfacesList, 0o644, "", ""},
	} {
		if c.content == "" {
			continue
		}
		if err := w.file(c.path, c.content, c.mode, c.user, c.group); err != nil {
			return r, err
		}
	}
	if p.Mode != "HM-LGW" {
		if err := os.MkdirAll(d.path("/etc/config/hs485d"), 0o755); err != nil {
			return r, err
		}
		log4j := QuietKeyServerWarning(Log4j2Config(readFile(d.path("/etc/config_templates/log4j2.xml")), in.Syslog, in.HmIPServerDefaults, p.LogLevels.HmIP), p.LogLevels.HmIP, localNetworkKey(d))
		if err := w.file("/var/etc/log4j2.xml", log4j, 0o644, "", ""); err != nil {
			return r, err
		}
		if err := w.storage(p, in); err != nil {
			return r, err
		}
		w.firmwareDirs(logf)
	}

	// the kernel side S47 and S60multimacd did: the LED driver of an RPI-RF-MOD on an HB-RF
	// adapter, and the loop device the multiplexer needs
	if p.HBRFLED != nil {
		w.hbrfLED(ctx, p.HBRFLED, logf)
	}
	if p.Multimacd.Run && !isNode(d, "/dev/eq3loop") {
		logf("run: loading eq3_char_loop for %s", "/dev/eq3loop")
		_, _ = d.run()(ctx, d.tool("/sbin/modprobe"), "eq3_char_loop")
		waitFor(d, 10*time.Second, func() bool { return isNode(d, "/dev/eq3loop") })
	}

	// the units' environment files and the activation markers, the results, and the markers last
	for name, content := range EnvFiles(p, in.Arch) {
		if err := w.file(filepath.Join(RunDir, name+".env"), content, 0o644, "", ""); err != nil {
			return r, err
		}
	}
	for _, x := range []struct {
		name string
		v    any
	}{{"modules.json", det}, {"plan.json", p}, {"render.json", r.Render}} {
		if err := writeJSON(shadowPath(root, x.name), x.v); err != nil {
			return r, fmt.Errorf("%s: %w", x.name, err)
		}
	}
	// a new plan: a known fatal error of hmipserver's last start no longer holds it back (D-102)
	_ = os.Remove(shadowPath(root, FatalFile))
	for name, run := range map[string]bool{"multimacd": p.Multimacd.Run, "rfd": p.RFD.Run, "hmipserver": p.HmIPServer.Run, "hs485d": p.HS485D.Run, "hmlangw": p.Hmlangw.Run} {
		marker := shadowPath(root, name+".enabled")
		if run {
			if err := os.WriteFile(marker, []byte(p.Mode+"\n"), 0o644); err != nil {
				return r, err
			}
			r.Enabled = append(r.Enabled, name)
		} else {
			_ = os.Remove(marker)
		}
	}
	sort.Strings(r.Enabled)
	// openccu-lite task 318: the module HmIP-RF runs on is kept for the next plans on Automatic
	if ok, err := recordHmIPPin(root, p, det.BoardMAC, time.Now()); err != nil {
		logf("run: the HmIP module could not be recorded: %v", err)
	} else if ok {
		logf("run: HmIP-RF runs on module %s; on Automatic it stays there", p.HmIP.SGTIN)
	}
	logf("run: detection %s; %s enabled (multimacd %v, rfd %v, hmipserver %v on %s, hs485d %v, hmlangw %v); %d files written",
		det.Duration.Round(10*time.Millisecond), strings.Join(r.Enabled, " "), p.Multimacd.Run, p.RFD.Run, p.HmIPServer.Run, p.HmIPServer.Node, p.HS485D.Run, p.Hmlangw.Run, len(r.Written))
	return r, nil
}

// LogLevelEnvFiles are the environment files that carry nothing but a daemon's log level, by
// daemon name. The radio run writes them at boot; the Log page writes them again when a level
// changes, so a restart applies the new one and no reboot is needed (B-161). One place, so the
// two writers cannot drift apart.
//
// Why the page and not the daemon's own prep step, which renders its configuration: systemd reads
// EnvironmentFile= for each command, so an ExecStartPre could write it in time (measured) - but
// the daemons' prep steps run inside their units' mount namespaces, and making the run directory
// writable there would let one confined daemon write another's environment file, which is read by
// a unit of another user. The page runs as occulited and writes through the helper instead.
//
// multimacd's level is held at MultimacdMaxLevel or more verbose here as well (openccu-lite B-275):
// the ready check reads its start lines, which quieter levels drop.
func LogLevelEnvFiles(rfd, hs485d, multimacd string) map[string]string {
	return map[string]string{
		"rfd":       "LOGLEVEL_RFD=" + rfd + "\n",
		"hs485d":    "LOGLEVEL_HS485D=" + hs485d + "\n",
		"multimacd": "MULTIMACD_LOGLEVEL=" + MultimacdLevel(multimacd) + "\n",
	}
}

// EnvFiles are the daemons' environment files the units read (EnvironmentFile=): the log levels,
// and hmipserver's JVM pieces.
func EnvFiles(p Plan, arch string) map[string]string {
	files := LogLevelEnvFiles(p.LogLevels.RFD, p.LogLevels.HS485D, p.LogLevels.Multimacd)
	files["hmlangw"] = "# hmlangw takes its arguments from /var/hm_mode\n"
	cp, class, args := "/opt/HMServer/HMIPServer.jar:/opt/HMServer/coupling/ESHBridge.jar", "de.eq3.ccu.server.ip.HMIPServer", "/var/etc/crRFD.conf /var/etc/HMServer.conf"
	if !p.HmIPServerHmIP {
		cp, class, args = "/opt/HMServer/HMServer.jar:/opt/HMServer/coupling/ESHBridge.jar", "de.eq3.ccu.server.HMServer", "/var/etc/HMServer.conf"
	}
	node := ""
	if p.HmIPServerHmIP {
		node = p.HmIPServer.Node
	}
	files["hmipserver"] = "HMIP_JAVA_OPTS=-Dos.arch=" + arch + " " + JavaOptions(p) + "\nHMIP_CLASSPATH=" + cp + "\nHMIP_CLASS=" + class + "\nHMIP_ARGS=" + args + "\nHMIP_DEVNODE=" + node + "\n"
	return files
}

// Log4j2Config is /var/etc/log4j2.xml as S62HMServer's setupLog4j2 made it, edit for edit: the
// template; with a LOGHOST the SYSLOG appender's host and an AppenderRef line after every File
// appender reference (not when the server logs to stdout, which the journal forwards); then
// every level="..." set to the HmIP log level (sed's greedy match on the line, as upstream).
func Log4j2Config(tmpl string, syslog, defaults map[string]string, level string) string {
	out := tmpl
	if h := syslog["LOGHOST"]; h != "" && defaults["HMIP_LOG_STDOUT"] != "1" {
		out = strings.ReplaceAll(out, `host="127.0.0.1"`, `host="`+h+`"`)
		lines := strings.Split(out, "\n")
		var with []string
		for _, l := range lines {
			with = append(with, l)
			if strings.Contains(l, `AppenderRef ref="File"`) {
				with = append(with, `    <AppenderRef ref="SYSLOG"/>`)
			}
		}
		out = strings.Join(with, "\n")
	}
	// openccu-lite task 299: a logger line the template marks keeps its own level whatever
	// LOGLEVEL_HMIP says - the one that writes the HmIP security counter lines at every start
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.Contains(l, Log4j2FixedLevelMarker) {
			continue
		}
		lines[i] = log4jLevelRe.ReplaceAllString(l, `level="`+strings.ToLower(level)+`"`)
	}
	return strings.Join(lines, "\n")
}

// QuietKeyServerWarning adds a filter for the one line hmipserver's KeyServerWorker writes at
// every start in key-server mode (occulited B-63): "Missing or invalid key server configuration
// parameter (Network.Key / Network.Key.Base) for mode: KEYSERVER_LOCAL". The stock CCU3 and
// OpenCCU render crRFD.conf exactly so (KeyServer.Mode=KEYSERVER_LOCAL, no Network.Key - the
// radio oracle's files), the line is theirs too, and it means "no local network key", which is
// key-server mode by definition; the maintainer read it as a broken configuration. Only that
// message is denied - the worker's other lines (a key server it cannot reach) still come through,
// at the level of every other logger. In local key mode (a Network.Key in the configuration) the
// line would mean what it says, so nothing is added there.
func QuietKeyServerWarning(out, level string, localKey bool) string {
	if localKey || strings.Contains(out, keyServerWorkerLogger) {
		return out
	}
	block := "\t\t<!-- openccu-lite: key-server mode has no local network key; the worker says so at every start -->\n" +
		"\t\t<Logger name=\"" + keyServerWorkerLogger + "\" level=\"" + strings.ToLower(level) + "\">\n" +
		"\t\t\t<RegexFilter regex=\".*Missing or invalid key server configuration parameter \\(Network\\.Key / Network\\.Key\\.Base\\).*\" onMatch=\"DENY\" onMismatch=\"NEUTRAL\"/>\n" +
		"\t\t</Logger>\n"
	for _, anchor := range []string{`<Logger name="de.eq3"`, `</Loggers>`} {
		if i := strings.Index(out, anchor); i >= 0 {
			j := strings.LastIndex(out[:i], "\n") + 1
			return out[:j] + block + out[j:]
		}
	}
	return out
}

const keyServerWorkerLogger = "de.eq3.cbcs.server.core.vertx.KeyServerWorker"

// localNetworkKey: hmipserver's configuration carries a local network key (local key mode, D-120):
// a non-empty Network.Key or Network.Key.Base in the user's override or the rendered file.
func localNetworkKey(d Detector) bool {
	for _, f := range []string{"/etc/config/crRFD/hmip_user.conf", "/var/etc/crRFD.conf"} {
		kv := ParseKV(readFile(d.path(f)))
		if strings.TrimSpace(kv["Network.Key"]) != "" || strings.TrimSpace(kv["Network.Key.Base"]) != "" {
			return true
		}
	}
	return false
}

// Log4j2FixedLevelMarker marks a logger line of the template whose level the render leaves alone
// (an XML comment on the line: <!-- occulite:fixed-level -->).
const Log4j2FixedLevelMarker = "occulite:fixed-level"

// log4jLevelRe is upstream's s/level=".*"/.../g: greedy within a line.
var log4jLevelRe = regexp.MustCompile(`level=".*"`)

// activeDaemons asks systemd which interface daemons are active or starting.
func activeDaemons(ctx context.Context, run Runner) []string {
	args := append([]string{"is-active", "--"}, func() []string {
		var u []string
		for _, n := range Daemons {
			u = append(u, n+".service")
		}
		return u
	}()...)
	out, _ := run(ctx, "systemctl", args...)
	var active []string
	for i, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i < len(Daemons) && (l == "active" || l == "activating" || l == "reloading") {
			active = append(active, Daemons[i])
		}
	}
	return active
}

// writer writes the box's files and records them.
type writer struct {
	d      Detector
	report *RunReport
}

// file writes a file atomically (a temporary beside it, renamed) with its mode and owner; a file
// that already has the content keeps its inode (rfd rewrites /etc/config/ids and keys in place).
func (w *writer) file(box, content string, mode os.FileMode, user, group string) error {
	path := w.d.path(box)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		w.own(box, mode, user, group)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return fmt.Errorf("writing %s: %w", box, err)
	}
	_ = chmodFn(tmp, mode)
	if user != "" || group != "" {
		uid, gid, ok := ids(user, group)
		if ok {
			_ = chownFn(tmp, uid, gid)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", box, err)
	}
	w.report.Written = append(w.report.Written, box)
	return nil
}

func (w *writer) own(box string, mode os.FileMode, user, group string) {
	Own(w.d.path(box), user, group, mode)
}

// storage is S62HMServer's storage setup: /usr/local/sdcard on a non-SD userfs (or the custom
// path), its measurement directory and .nobackup, the /media/usb0 link and the status files; the
// diagram database copied from the stick into hmipserver's own directory (D-93), or an empty one.
func (w *writer) storage(p Plan, in Inputs) error {
	d := w.d
	if p.StoragePath != "" {
		sp := d.path(p.StoragePath)
		if in.CustomStorage == "" {
			_ = os.MkdirAll(sp, 0o755)
		}
		if st, err := os.Stat(sp); err == nil && st.IsDir() {
			_ = os.MkdirAll(filepath.Join(sp, "measurement"), 0o755)
			if !exists(filepath.Join(sp, ".nobackup")) {
				_ = os.WriteFile(filepath.Join(sp, ".nobackup"), nil, 0o644)
			}
			link := d.path("/media/usb0")
			_ = os.MkdirAll(filepath.Dir(link), 0o755)
			if cur, err := os.Readlink(link); err != nil || cur != p.StoragePath {
				_ = os.Remove(link)
				_ = os.Symlink(p.StoragePath, link)
			}
			_ = os.MkdirAll(d.path("/var/status"), 0o755)
			for _, s := range []string{"SDinitialised", "hasSD"} {
				_ = os.WriteFile(d.path("/var/status/"+s), nil, 0o644)
			}
		}
	}
	measure := d.path(DiagramPath)
	if err := os.MkdirAll(filepath.Dir(measure), 0o750); err != nil {
		return err
	}
	if stick := d.path("/media/usb0/measurement"); d.Diagrams && isDir(stick) {
		_ = os.RemoveAll(measure)
		if _, err := d.run()(context.Background(), d.tool("/bin/cp"), "-a", stick, measure); err != nil {
			return fmt.Errorf("copying the diagram data from the stick: %w", err)
		}
	}
	if err := os.MkdirAll(measure, 0o750); err != nil {
		return err
	}
	OwnTree(filepath.Dir(measure), "hmipserver", "hmipserver")
	Own(filepath.Dir(measure), "hmipserver", "hmipserver", 0o750)
	return nil
}

// firmwareDirs removes invalid device firmware directories, as S62HMServer did: one without an
// info file naming the firmware.
func (w *writer) firmwareDirs(logf func(string, ...any)) {
	dirs, _ := filepath.Glob(w.d.path("/etc/config/firmware/*"))
	for _, dir := range dirs {
		if !isDir(dir) {
			continue
		}
		info := readFile(filepath.Join(dir, "info"))
		if !strings.Contains(info, "Name=") || strings.Contains(info, "Name=\n") {
			logf("run: removed invalid firmware directory %s", filepath.Base(dir))
			_ = os.RemoveAll(dir)
		}
	}
}

// hbrfLED loads the LED driver of an RPI-RF-MOD on an HB-RF-USB/-ETH with the adapter's pins
// (S47): whatever driver holds the rpi_rf_mod:* names is unbound first, the module loaded, and that
// driver bound again, so the module's LEDs get the names and the header's come back as "…_1".
// openccu-lite task 326: since task 315 the names belong to leds_pwm's rpi_rf_mod_leds (the
// header overlay), not to leds-gpio's "leds" - unbinding leds-gpio by name freed nothing, and
// rpi_rf_mod_led's LEDs were renamed "…_1" instead, so everything drove the empty header pins.
func (w *writer) hbrfLED(ctx context.Context, led *HBRFLED, logf func(string, ...any)) {
	d := w.d
	drv, dev := ledOwner(d.path("/sys/class/leds/rpi_rf_mod:blue"))
	if drv != "" {
		logf("run: freeing the rpi_rf_mod LED names from %s's %s", filepath.Base(drv), dev)
		_ = os.WriteFile(filepath.Join(drv, "unbind"), []byte(dev), 0)
	}
	if !d.moduleLoaded("rpi_rf_mod_led") {
		logf("run: loading rpi_rf_mod_led for the module on %s (pins %s %s %s)", led.Node, led.Red, led.Green, led.Blue)
		_, _ = d.run()(ctx, d.tool("/sbin/modprobe"), "-q", "rpi_rf_mod_led", "red_gpio_pin="+led.Red, "green_gpio_pin="+led.Green, "blue_gpio_pin="+led.Blue)
	}
	if drv != "" && !exists(filepath.Join(drv, dev)) {
		_ = os.WriteFile(filepath.Join(drv, "bind"), []byte(dev), 0)
	}
}

// ledOwner is the driver directory and the device name behind an LED class device (its "device"
// link, the device's "driver" link): leds_pwm and rpi_rf_mod_leds on a Pi image since task 315,
// leds-gpio and leds (or gpio-leds) before. Empty when the LED has no parent device - the
// adapter's own rpi_rf_mod_led LEDs, which registered without one - or is not there.
func ledOwner(class string) (driver, device string) {
	devPath, err := filepath.EvalSymlinks(filepath.Join(class, "device"))
	if err != nil {
		return "", ""
	}
	drv, err := filepath.EvalSymlinks(filepath.Join(devPath, "driver"))
	if err != nil {
		return "", ""
	}
	return drv, filepath.Base(devPath)
}

// isNode says whether the path is a character device; in a sandbox (a root that is not /) a
// plain file stands in for one.
func isNode(d Detector, box string) bool {
	st, err := os.Stat(d.path(box))
	if err != nil {
		return false
	}
	if st.Mode()&os.ModeCharDevice != 0 {
		return true
	}
	return d.Root != "" && d.Root != "/" && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// waitFor polls a condition until it holds or the time is up.
func waitFor(d Detector, limit time.Duration, cond func() bool) bool {
	deadline := d.now().Add(limit)
	for !cond() {
		if !d.now().Before(deadline) {
			return false
		}
		d.sleep(pollInterval)
	}
	return true
}

// pollInterval is the readiness polls' step; a test shortens it.
var pollInterval = time.Second

// ensureCryptToolConfig creates /etc/config/crypttool.cfg when it is missing and gives it the mode
// crypttool expects, which eq3configd's unit did (B-87). The group is no longer eq3cfg: nothing on
// this system runs as that user since task 163, and crypttool runs as root through occulited's
// privileged helper. A file that is already there keeps its owner and group.
func ensureCryptToolConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("crypttool.cfg: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("crypttool.cfg: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("crypttool.cfg: %w", err)
	}
	return os.Chmod(path, 0o640)
}
