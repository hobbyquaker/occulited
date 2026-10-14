package radio

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The files and command lines that follow from a plan, as strings: the render's outputs. Writing
// them to the box is the run step (phase 2); the oracle writer and the tests read them here.

// Files is what the render produces.
type Files struct {
	// HMMode is /var/hm_mode's content: the environment's keys and the roles'.
	HMMode string
	// VarFiles are /var/board_serial and the /var/rf_*, /var/hmip_* files, by name.
	VarFiles map[string]string
	// The daemons' files ("" = not written).
	RFDConf        string // /etc/config/rfd.conf
	VarRFDConf     string // /var/etc/rfd.conf
	MultimacdConf  string // /var/etc/multimacd.conf
	CrRFDConf      string // /var/etc/crRFD.conf
	HMServerConf   string // /var/etc/HMServer.conf
	VarHS485DConf  string // /var/etc/hs485d.conf
	InterfacesList string // /etc/config/InterfacesList.xml
	// Commands are the daemons' command lines, by daemon (multimacd, rfd, hmipserver, hs485d-init,
	// hs485d, hmlangw); absent = not started.
	Commands map[string]string
}

// DiagramPath is where hmipserver keeps its diagram database on lite: the daemon's own
// directory, not /tmp (D-93).
const DiagramPath = "/var/hmipserver/measurement"

// DiagramsOffPath is the empty directory hmipserver's measurement service is given for its
// diagram configurations and templates while hmipserver.diagrams is off (occulited B-69). Its
// defaults, /etc/config/measurement (the WebUI's diagram configurations, which task 33 removes
// from a migrated system) and /opt/HMServer/measurement (the templates, not on the image), do
// not exist on lite, and MeasurementService logs an ERR line for each at every start. An empty
// directory holds no diagram configuration and no template, so the service creates no
// diagram and the database directory stays empty, as before.
const DiagramsOffPath = "/var/hmipserver/diagrams-off"

// HMModeKeys are the keys the detection owns in /var/hm_mode.
var HMModeKeys = []string{
	"HM_HMRF_DEV", "HM_HMRF_DEVNODE", "HM_HMRF_DEVTYPE", "HM_HMRF_ADDRESS", "HM_HMRF_ADDRESS_ACTIVE", "HM_HMRF_SERIAL", "HM_HMRF_VERSION",
	"HM_HMIP_DEV", "HM_HMIP_DEVNODE", "HM_HMIP_DEVTYPE", "HM_HMIP_ADDRESS", "HM_HMIP_ADDRESS_ACTIVE", "HM_HMIP_SERIAL", "HM_HMIP_SGTIN", "HM_HMIP_VERSION",
}

// HMModeValues are the plan's values for HMModeKeys.
func HMModeValues(p Plan) map[string]string {
	v := map[string]string{}
	for _, k := range HMModeKeys {
		v[k] = ""
	}
	if r := p.HmRF; r != nil {
		v["HM_HMRF_DEV"], v["HM_HMRF_DEVNODE"], v["HM_HMRF_DEVTYPE"] = r.Hardware, r.Node, r.DeviceType
		v["HM_HMRF_ADDRESS"], v["HM_HMRF_SERIAL"], v["HM_HMRF_VERSION"] = r.Address, r.Serial, r.Version
	}
	v["HM_HMRF_ADDRESS_ACTIVE"] = p.HmRFAddressActive
	if r := p.HmIP; r != nil {
		v["HM_HMIP_DEV"], v["HM_HMIP_DEVNODE"], v["HM_HMIP_DEVTYPE"] = r.Hardware, r.Node, r.DeviceType
		v["HM_HMIP_ADDRESS"], v["HM_HMIP_SERIAL"], v["HM_HMIP_SGTIN"], v["HM_HMIP_VERSION"] = r.Address, r.Serial, r.SGTIN, r.Version
	}
	v["HM_HMIP_ADDRESS_ACTIVE"] = p.HmIPAddressActive
	return v
}

// RenderHMMode merges the plan's keys into the environment's, sorted, quoted as the shell's
// set does (deviation 11: one atomic write, the foreign keys kept).
func RenderHMMode(env map[string]string, p Plan) string {
	all := map[string]string{}
	for k, v := range env {
		if strings.HasPrefix(k, "HM_") {
			all[k] = v
		}
	}
	for k, v := range HMModeValues(p) {
		all[k] = v
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "='" + all[k] + "'\n")
	}
	return b.String()
}

// VarFiles are the RF files S47 wrote under /var, and `ids`, which eq3configd's unit wrote before
// its daemon (task 163: the daemon goes, this stays - it is rfd's `Address File`).
func VarFiles(p Plan) map[string]string {
	f := map[string]string{"board_serial": p.BoardSerial}
	if p.BoardSGTIN != "" {
		f["board_sgtin"] = p.BoardSGTIN
	}
	f["rf_board_serial"], f["rf_address"], f["rf_firmware_version"] = "", "", ""
	if p.HmRF != nil {
		f["rf_board_serial"], f["rf_address"], f["rf_firmware_version"] = p.HmRF.Serial, p.HmRF.Address, p.HmRF.Version
	}
	f["hmip_board_serial"], f["hmip_firmware_version"], f["hmip_address"], f["hmip_board_sgtin"] = "", "", "", ""
	if p.HmIP != nil {
		f["hmip_board_serial"], f["hmip_firmware_version"], f["hmip_address"], f["hmip_board_sgtin"] = p.HmIP.Serial, p.HmIP.Version, p.HmIP.Address, p.HmIP.SGTIN
	}
	f["ids"] = IDsFile(f["rf_address"], f["board_serial"])
	return f
}

// IDsFile is the content eq3configd's unit wrote to /var/ids before it started the daemon, and
// copied to /etc/config/ids on a system that had none. It is rfd's `Address File`
// (config_templates/rfd.conf), so it outlives the daemon that happened to write it (task 163).
// The lines keep eq3-3's own spelling, empty values and all: a system without a BidCos module
// wrote an empty address there too.
func IDsFile(rfAddress, boardSerial string) string {
	return "BidCoS-Address=" + rfAddress + "\nSerialNumber=" + boardSerial + "\n"
}

var (
	coproPathLine  = regexp.MustCompile(`(?m)^Coprocessor Device Path = /dev/.*$`)
	adapterPort    = regexp.MustCompile(`(?m)^Adapter\.1\.Port=/dev/.*$`)
	localDevice    = regexp.MustCompile(`(?m)^Adapter\.Local\.Device\.Enabled=.*$`)
	lanRouting     = regexp.MustCompile(`(?m)^Lan\.Routing\.Enabled=.*$`)
	bindAddrLine   = regexp.MustCompile(`(?m)^Legacy\.BindAddress=.*\n?`)
	diagramPath    = regexp.MustCompile(`(?m)^diagramDatabasePath=.*$`)
	diagramDirs    = regexp.MustCompile(`(?m)^diagram(Config|Template)Path=.*\n?`)
	javaAddOpens   = []string{"java.lang", "java.util", "java.nio", "sun.nio.ch", "jdk.internal.misc"}
	javaAddExports = []string{"sun.nio.ch", "jdk.internal.misc"}
)

// MultimacdConf is /var/etc/multimacd.conf from the template.
func MultimacdConf(tmpl, node string) string {
	return coproPathLine.ReplaceAllString(tmpl, "Coprocessor Device Path = "+node)
}

// CrRFDConf is /var/etc/crRFD.conf from the template: the adapter's node, the advanced flags,
// the bind address.
func CrRFDConf(tmpl, node string, advanced bool, bind string) string {
	adv := "false"
	if advanced {
		adv = "true"
	}
	out := adapterPort.ReplaceAllString(tmpl, "Adapter.1.Port="+node)
	out = localDevice.ReplaceAllString(out, "Adapter.Local.Device.Enabled="+adv)
	out = lanRouting.ReplaceAllString(out, "Lan.Routing.Enabled="+adv)
	if bind != "" {
		out = bindAddrLine.ReplaceAllString(out, "")
		out += "Legacy.BindAddress=" + bind + "\n"
	}
	return out
}

// HMServerConf is /var/etc/HMServer.conf: the image's with the diagram path, and with diagrams
// off the configuration and template paths on the empty DiagramsOffPath (B-69).
func HMServerConf(base string, diagrams bool) string {
	out := diagramPath.ReplaceAllString(base, "diagramDatabasePath="+DiagramPath)
	if diagrams {
		return out
	}
	out = diagramDirs.ReplaceAllString(out, "")
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + "diagramConfigPath=" + DiagramsOffPath + "\ndiagramTemplatePath=" + DiagramsOffPath + "\n"
}

// JavaOptions are hmipserver's JVM options as S62 built them (the serial port first, then the
// heap and the reflective access Netty needs).
func JavaOptions(p Plan) string {
	o := fmt.Sprintf("-Xmx%dm -Dio.netty.tryReflectionSetAccessible=true", p.HeapMB)
	for _, m := range javaAddOpens {
		o += " --add-opens java.base/" + m + "=ALL-UNNAMED"
	}
	for _, m := range javaAddExports {
		o += " --add-exports java.base/" + m + "=ALL-UNNAMED"
	}
	if p.HmIPServerHmIP && p.HmIPServer.Node != "" {
		o = "-Dgnu.io.rxtx.SerialPorts=" + p.HmIPServer.Node + " " + o
	}
	return o
}

// HmIPServerCommand is the JVM's command line: the classpath, class and arguments of the HmIP
// server, or of the HMServer half alone.
func HmIPServerCommand(p Plan, arch string) string {
	cp, class, args := "/opt/HMServer/HMIPServer.jar:/opt/HMServer/coupling/ESHBridge.jar", "de.eq3.ccu.server.ip.HMIPServer", "/var/etc/crRFD.conf /var/etc/HMServer.conf"
	if !p.HmIPServerHmIP {
		cp, class, args = "/opt/HMServer/HMServer.jar:/opt/HMServer/coupling/ESHBridge.jar", "de.eq3.ccu.server.HMServer", "/var/etc/HMServer.conf"
	}
	return "java -Dos.arch=" + arch + " " + JavaOptions(p) + " -Dlog4j.configurationFile=file:///var/etc/log4j2.xml -Dfile.encoding=ISO-8859-1 -cp " + cp + " " + class + " " + args
}

// Render produces every file and command line of the plan.
func Render(in Inputs, p Plan) Files {
	f := Files{VarFiles: VarFiles(p), Commands: map[string]string{}}
	f.HMMode = RenderHMMode(in.Env, p)
	if p.Multimacd.Run {
		f.MultimacdConf = MultimacdConf(in.TemplateMultimacdConf, p.Multimacd.Node)
		f.Commands["multimacd"] = "/bin/multimacd -f /var/etc/multimacd.conf -l " + p.LogLevels.Multimacd
	}
	if p.Mode == "HM-LGW" {
		if p.Hmlangw.Run && p.HmRF != nil {
			f.Commands["hmlangw"] = "exec /bin/hmlangw -b -n " + p.HmRF.Serial + " -s /dev/mmd_bidcos -r -1 >/var/log/hmlangw.log 2>&1"
		}
		return f
	}
	f.RFDConf = p.RFDConf
	f.VarRFDConf = VarRFDConf(p.RFDConf)
	if p.RFD.Run {
		f.Commands["rfd"] = "/bin/rfd -f /var/etc/rfd.conf -l " + p.LogLevels.RFD
	}
	if p.HmIPServerHmIP {
		f.CrRFDConf = CrRFDConf(in.TemplateCrRFDConf, p.HmIPServer.Node, p.HmIPServerAdvanced, p.HmIPServerBind)
	}
	f.HMServerConf = HMServerConf(in.HMServerConf, in.Diagrams)
	f.Commands["hmipserver"] = HmIPServerCommand(p, in.Arch)
	if in.HS485DConfExists {
		f.VarHS485DConf = VarHS485DConf(in.HS485DConf)
	}
	if p.HS485D.Run {
		f.Commands["hs485d-init"] = "/bin/hs485dLoader -l " + p.LogLevels.HS485D + " -ds -dd /var/etc/hs485d.conf"
		f.Commands["hs485d"] = "/bin/hs485dLoader -l " + p.LogLevels.HS485D + " -dw /var/etc/hs485d.conf"
	}
	f.InterfacesList = RenderInterfaces(in.TemplateInterfacesList, p.Interfaces)
	return f
}
