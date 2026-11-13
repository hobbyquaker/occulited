package radio

import (
	"regexp"
	"strings"
)

// The connection choices (D-98). Each interface process has at most one local radio; the user
// may pin it by the module's identity, and nothing written means auto, upstream's pick. The
// choices live in the daemons' own files (D-83), in forms the daemons and OpenCCU ignore:
//   - hmipserver: occulite.hmip.adapter=<serial|SGTIN>|none in /etc/config/crRFD/hmip_user.conf (D-97);
//   - rfd: the comment line "# occulite.bidcos.module=<serial>|none" in /etc/config/rfd.conf.

// ChoiceAuto is the absent choice; BidCosNone is rfd without a local radio (LAN gateways only);
// HmIPNone is no HmIP at all (occulited task 147): hmipserver runs HMServer.jar, the virtual
// devices alone, and a module carrying both protocols serves BidCos-RF only. It is the only
// choice that runs HMServer.jar: auto or a pinned module always run HMIPServer.jar, with the
// module or without it.
const (
	ChoiceAuto = ""
	BidCosNone = "none"
	HmIPNone   = "none"
)

// HmIPChoiceKey is the key in hmip_user.conf.
const HmIPChoiceKey = "occulite.hmip.adapter"

// HmIPPathKey is the key in hmip_user.conf for how hmipserver reaches its module (task 150):
// "direct" (the raw UART) or "multimacd" (/dev/mmd_hmip); absent is automatic.
const HmIPPathKey = "occulite.hmip.path"

// The paths.
const (
	PathDirect    = "direct"
	PathMultimacd = "multimacd"
)

// BidCosChoicePrefix starts the marker line in rfd.conf.
const BidCosChoicePrefix = "# occulite.bidcos.module="

// Choices are the two pins; "" is auto.
type Choices struct {
	HmIP   string `json:"hmip"`
	BidCos string `json:"bidcos"`
	// HmIPPath is how hmipserver reaches its module (task 150): PathDirect, PathMultimacd, or ""
	// for what the hardware and the BidCos-RF choice make of it.
	HmIPPath string `json:"hmip_path"`
}

// Explicit: at least one process is pinned, so the plan follows the choices instead of
// reproducing upstream's chain.
func (c Choices) Explicit() bool {
	return c.HmIP != ChoiceAuto || c.BidCos != ChoiceAuto || c.HmIPPath != ChoiceAuto
}

var (
	hmipChoiceRe   = regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(HmIPChoiceKey) + `[ \t]*=[ \t]*(\S*)[ \t]*$`)
	bidcosChoiceRe = regexp.MustCompile(`(?m)^#[ \t]*occulite\.bidcos\.module[ \t]*=[ \t]*(\S*)[ \t]*$`)
	identityRe     = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
	hmipPathRe     = regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(HmIPPathKey) + `[ \t]*=[ \t]*(\S*)[ \t]*$`)
	hmipPathLine   = regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(HmIPPathKey) + `[ \t]*=[^\n]*(\n|$)`)
	// the whole lines with their newline, for the removal
	hmipChoiceLine   = regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(HmIPChoiceKey) + `[ \t]*=[^\n]*(\n|$)`)
	bidcosChoiceLine = regexp.MustCompile(`(?m)^#[ \t]*occulite\.bidcos\.module[ \t]*=[^\n]*(\n|$)`)
)

// ValidIdentity: a module serial (10 characters) or an SGTIN (24 hex digits).
func ValidIdentity(s string) bool { return identityRe.MatchString(s) }

// ReadChoices takes the choices from hmip_user.conf and rfd.conf.
func ReadChoices(hmipUserConf, rfdConf string) Choices {
	var c Choices
	if m := hmipChoiceRe.FindStringSubmatch(hmipUserConf); m != nil && strings.ToLower(m[1]) != "auto" {
		c.HmIP = m[1]
		if strings.EqualFold(c.HmIP, HmIPNone) {
			c.HmIP = HmIPNone
		}
	}
	if m := hmipPathRe.FindStringSubmatch(hmipUserConf); m != nil {
		switch v := strings.ToLower(m[1]); v {
		case PathDirect, PathMultimacd:
			c.HmIPPath = v
		}
	}
	if m := bidcosChoiceRe.FindStringSubmatch(rfdConf); m != nil && strings.ToLower(m[1]) != "auto" {
		c.BidCos = m[1]
		if strings.EqualFold(c.BidCos, BidCosNone) {
			c.BidCos = BidCosNone
		}
	}
	return c
}

// SetHmIPChoice returns hmip_user.conf with the key set, or removed for auto. Every other line
// stays as it is: an hmipserver key the user put there keeps working.
func SetHmIPChoice(conf, v string) string {
	conf = hmipChoiceLine.ReplaceAllString(conf, "")
	if v == ChoiceAuto {
		return conf
	}
	if conf != "" && !strings.HasSuffix(conf, "\n") {
		conf += "\n"
	}
	return conf + HmIPChoiceKey + "=" + v + "\n"
}

// SetHmIPPath returns hmip_user.conf with the path key set, or removed for automatic.
func SetHmIPPath(conf, v string) string {
	conf = hmipPathLine.ReplaceAllString(conf, "")
	if v == ChoiceAuto {
		return conf
	}
	if conf != "" && !strings.HasSuffix(conf, "\n") {
		conf += "\n"
	}
	return conf + HmIPPathKey + "=" + v + "\n"
}

// SetBidCosChoice returns rfd.conf with the marker line set as the file's first line, or removed
// for auto. The first line is outside every [Interface n] section, so the render's section edits
// and the LAN gateway page's rewrite (which keeps the header) never touch it.
func SetBidCosChoice(conf, v string) string {
	conf = bidcosChoiceLine.ReplaceAllString(conf, "")
	if v == ChoiceAuto {
		return conf
	}
	return BidCosChoicePrefix + v + "\n" + conf
}

// matches: the module answers to the identity (its serial or its SGTIN, case ignored).
func (m Module) matches(id string) bool {
	if id == "" {
		return false
	}
	return strings.EqualFold(m.Serial, id) || (m.SGTIN != "" && strings.EqualFold(m.SGTIN, id))
}

// HmIPCapable: the module can carry hmipserver - a serial and a real HmIP address.
func (m Module) HmIPCapable() bool {
	return m.OK() && !m.USBAdapter && m.Serial != "" && m.HmIPAddress != "" && m.HmIPAddress != "0x000000"
}

// HmIPOnly: the module carries HmIP and nothing else - the Telekom stick (S47's exclusion), a
// module that reported no serial (deviation 16), or an HmIP-RFUSB on the HmIP-only firmware line
// (HMIP_TRX_App, firmware below 4; openccu-lite B-282, deviation 17). rfd cannot use it, and
// multimacd refuses its coprocessor ("Please install DualCoPro Firmware"), so hmipserver opens it
// directly.
func (m Module) HmIPOnly() bool {
	return m.Hardware == "HMIP-RFUSB-TK" || m.SerialFromSGTIN || m.Application == AppHmIPOnly
}

// BidCosCapable: the module can carry rfd - the HM-CFG-USB-2, or a module with a serial and a
// BidCos address that is not HmIP-only.
func (m Module) BidCosCapable() bool {
	if m.USBAdapter {
		return m.Serial != ""
	}
	return m.OK() && m.Serial != "" && m.HmRFAddress != "" && !m.HmIPOnly()
}

// HmIPPaths are the ways hmipserver can reach the module (task 150): a dual-protocol
// HM-MOD-RPI-PCB only through multimacd (deviation 15), an HmIP-only module (HmIPOnly) only
// directly (multimacd refuses its coprocessor), the others either way.
func (m Module) HmIPPaths() []string {
	switch {
	case !m.HmIPCapable():
		return nil
	case m.Hardware == "HM-MOD-RPI-PCB" && m.HmRFAddress != "" && m.HmRFAddress != "0x000000" && !m.SerialFromSGTIN:
		return []string{PathMultimacd}
	case m.HmIPOnly() || m.Hardware == "HM-MOD-RPI-PCB":
		return []string{PathDirect}
	}
	return []string{PathDirect, PathMultimacd}
}

// Option is one module a process may be pinned to.
type Option struct {
	ID       string `json:"id"` // the identity written into the file: the serial
	Hardware string `json:"hardware"`
	Node     string `json:"node,omitempty"`
	// DeviceType tells the header, an HB-RF adapter and a USB stick apart.
	DeviceType string `json:"device_type,omitempty"`
	SGTIN      string `json:"sgtin,omitempty"`
	Version    string `json:"version,omitempty"`
	// Paths: for HmIP, how hmipserver can reach it (HmIPPaths)
	Paths []string `json:"paths,omitempty"`
	// Application is the coprocessor's firmware line (Module.Application); HmIPOnly says the
	// module carries HmIP alone (openccu-lite B-282) - the page says why it is not offered for
	// BidCos-RF or through multimacd, and where the dual firmware is flashed.
	Application string `json:"application,omitempty"`
	HmIPOnly    bool   `json:"hmip_only,omitempty"`
}

// Options are the modules each process can be pinned to on this detection.
type Options struct {
	HmIP   []Option `json:"hmip"`
	BidCos []Option `json:"bidcos"`
}

// ChoiceOptions lists them.
func ChoiceOptions(det Detection) Options {
	o := Options{HmIP: []Option{}, BidCos: []Option{}}
	for _, m := range det.Modules {
		opt := Option{ID: m.Serial, Hardware: m.Hardware, Node: m.Node, DeviceType: m.DeviceType, SGTIN: m.SGTIN, Version: m.Version, Application: m.Application, HmIPOnly: m.OK() && !m.USBAdapter && m.HmIPOnly()}
		if m.USBAdapter {
			opt.Hardware, opt.DeviceType = "HM-CFG-USB-2", "USB"
		}
		if m.HmIPCapable() {
			h := opt
			h.Paths = m.HmIPPaths()
			o.HmIP = append(o.HmIP, h)
		}
		if m.BidCosCapable() {
			o.BidCos = append(o.BidCos, opt)
		}
	}
	return o
}
