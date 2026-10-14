package radio

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Inputs is everything the plan decides from: the detection, the boot's environment, and the
// daemons' own files on the userfs (D-83: no separate radio configuration file). Load reads them
// from a root; the plan itself is a pure function of this struct, so the tests build it by hand.
type Inputs struct {
	Detection Detection `json:"detection"`
	// Env is /var/hm_mode as the host and system scripts left it before the detection: HM_HOST,
	// HM_MODE, HM_RTC, HM_LED_*. The render keeps every key it does not own.
	Env map[string]string `json:"env"`
	// Host is HM_HOST, Mode HM_MODE (NORMAL, or HM-LGW when /usr/local/HMLGW exists).
	Host string `json:"host"`
	Mode string `json:"mode"`
	// The userfs files ("" and false when absent).
	RFDConf          string `json:"rfd_conf,omitempty"`
	RFDConfExists    bool   `json:"rfd_conf_exists"`
	HS485DConf       string `json:"hs485d_conf,omitempty"`
	HS485DConfExists bool   `json:"hs485d_conf_exists"`
	InterfacesList   string `json:"interfaces_list,omitempty"`
	InterfacesListOK bool   `json:"interfaces_list_exists"`
	IDs              string `json:"ids,omitempty"`
	IDsExists        bool   `json:"ids_exists"`
	HmIPAddressConf  string `json:"hmip_address_conf,omitempty"`
	HmIPAddressOK    bool   `json:"hmip_address_conf_exists"`
	HmIPUserConf     string `json:"hmip_user_conf,omitempty"`
	HMLGW            bool   `json:"hmlgw"`
	CustomStorage    string `json:"custom_storage_path,omitempty"`
	HmIPNetworkKey   bool   `json:"hmip_networkkey_exists"`
	// The image's templates and defaults.
	TemplateRFDConf        string            `json:"-"`
	TemplateMultimacdConf  string            `json:"-"`
	TemplateCrRFDConf      string            `json:"-"`
	TemplateInterfacesList string            `json:"-"`
	TemplateHmIPNetworkKey bool              `json:"-"`
	HMServerConf           string            `json:"-"`
	HmIPServerDefaults     map[string]string `json:"hmipserver_defaults,omitempty"`
	Syslog                 map[string]string `json:"syslog,omitempty"`
	// The box.
	Arch         string `json:"arch"`
	MemTotalKB   int64  `json:"mem_total_kb"`
	CgroupMemMax int64  `json:"cgroup_mem_max,omitempty"` // 0 = "max" or no cgroup limit
	UserfsOnMMC  bool   `json:"userfs_on_mmc"`
	Hostname     string `json:"hostname,omitempty"`
	// HmIPPin is the module HmIP-RF last ran on (openccu-lite task 318), nil without a record.
	HmIPPin *HmIPPin `json:"hmip_pin,omitempty"`
	// Diagrams is the Detector's (occulited.json's hmipserver.diagrams): HMServer.conf's diagram
	// directories follow it (B-69).
	Diagrams bool `json:"diagrams"`
	// RandomAddress is the BidCos address S47 makes up for a module that answers 0x000000
	// (0xFF0000-0xFFFFFE); Load draws one, a test sets it.
	RandomAddress string `json:"random_address,omitempty"`
}

// Load reads the inputs from a root after Detect ran there.
func Load(ctx context.Context, root string, run Runner, det Detection) Inputs {
	if run == nil {
		run = ExecRunner
	}
	p := func(s string) string {
		if root == "" || root == "/" {
			return s
		}
		return filepath.Join(root, s)
	}
	in := Inputs{Detection: det, Env: ParseKV(readFile(p("/var/hm_mode")))}
	in.Host, in.Mode = in.Env["HM_HOST"], in.Env["HM_MODE"]
	in.HMLGW = exists(p("/usr/local/HMLGW"))
	if in.HMLGW {
		in.Mode = "HM-LGW"
	}
	if in.Mode == "" {
		in.Mode = "NORMAL"
	}
	in.RFDConf, in.RFDConfExists = readExisting(p("/etc/config/rfd.conf"))
	in.HS485DConf, in.HS485DConfExists = readExisting(p("/etc/config/hs485d.conf"))
	in.InterfacesList, in.InterfacesListOK = readExisting(p("/etc/config/InterfacesList.xml"))
	in.IDs, in.IDsExists = readExisting(p("/etc/config/ids"))
	in.HmIPAddressConf, in.HmIPAddressOK = readExisting(p("/etc/config/hmip_address.conf"))
	in.HmIPUserConf, _ = readExisting(p("/etc/config/crRFD/hmip_user.conf"))
	in.HmIPPin = ReadHmIPPin(root)
	in.CustomStorage = strings.TrimSpace(readFile(p("/etc/config/CustomStoragePath")))
	in.HmIPNetworkKey = exists(p("/etc/config/hmip_networkkey.conf"))
	in.TemplateRFDConf = readFile(p("/etc/config_templates/rfd.conf"))
	in.TemplateMultimacdConf = readFile(p("/etc/config_templates/multimacd.conf"))
	in.TemplateCrRFDConf = readFile(p("/etc/config_templates/crRFD.conf"))
	in.TemplateInterfacesList = readFile(p("/etc/config_templates/InterfacesList.xml"))
	in.TemplateHmIPNetworkKey = exists(p("/etc/config_templates/hmip_networkkey.conf"))
	in.HMServerConf = readFile(p("/etc/HMServer.conf"))
	in.HmIPServerDefaults = map[string]string{}
	for _, f := range []string{"/etc/hmipserver.default", "/etc/config/hmipserver.default"} {
		for k, v := range ParseKV(readFile(p(f))) {
			in.HmIPServerDefaults[k] = v
		}
	}
	in.Syslog = ParseKV(readFile(p("/etc/config/syslog")))
	if out, err := run(ctx, "uname", "-m"); err == nil {
		in.Arch = strings.TrimSpace(string(out))
	}
	for _, l := range strings.Split(readFile(p("/proc/meminfo")), "\n") {
		if strings.HasPrefix(l, "MemTotal:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				in.MemTotalKB, _ = strconv.ParseInt(f[1], 10, 64)
			}
		}
	}
	if v := strings.TrimSpace(readFile(p("/sys/fs/cgroup/memory.max"))); v != "" && v != "max" {
		in.CgroupMemMax, _ = strconv.ParseInt(v, 10, 64)
	}
	for _, l := range strings.Split(readFile(p("/proc/mounts")), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[1] == "/usr/local" {
			in.UserfsOnMMC = strings.HasPrefix(f[0], "/dev/mmc")
		}
	}
	if out, err := run(ctx, "hostname"); err == nil {
		in.Hostname = strings.TrimSpace(string(out))
	}
	in.RandomAddress = randomAddress()
	return in
}

// hasIDs: /etc/config/ids is there and not empty. The prep step makes the file empty for rfd to
// fill (openccu-lite B-266), so an empty one is no address at all - not an unusable one to move
// aside - and the module's address is written into it.
func (in Inputs) hasIDs() bool { return in.IDsExists && strings.TrimSpace(in.IDs) != "" }

// hasHmIPAddress: hmip_address.conf is there and not empty, for the same reason (the prep step
// makes it empty for hmipserver, B-266); an empty one would otherwise give upstream's "0x".
func (in Inputs) hasHmIPAddress() bool {
	return in.HmIPAddressOK && strings.TrimSpace(in.HmIPAddressConf) != ""
}

func readExisting(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// ParseKV reads KEY=value and KEY='value' lines (the shell-sourceable files the firmware writes),
// comments and blanks skipped.
func ParseKV(text string) map[string]string {
	out := map[string]string{}
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
