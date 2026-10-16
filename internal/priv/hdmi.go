package priv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// HDMI (openccu-lite task 340). On the Raspberry Pi products the HDMI output is switched off at
// runtime once the system is up, not in config.txt: a monitor still shows the boot and the
// recovery system, and switching it on again needs no reboot. With the KMS driver (vc4) the way
// is the DRM connector's sysfs status attribute: "off" forces the connector disconnected, the
// fbdev emulation's hotplug handling then disables the CRTC, the vc4 HDMI encoder powers its PHY
// down and its device goes to runtime suspend (measured on rpi3 and rpi4, see the item).
//
// One typed operation with a boolean and nothing else: the helper finds the connectors itself,
// under drmDir, by an exact name shape, and writes one of three fixed words to each one's status.
// On "on" it gives each connector back what the kernel command line asked for it: the firmware
// writes `video=HDMI-A-1:…D` there for hdmi_force_hotplug, so the forced connector is forced on
// again; a connector the command line does not name goes back to "detect".

// opHDMI is the operation's name on the wire.
const opHDMI = "hdmi"

var (
	// drmDir and cmdlinePath are variables so a test can point them at a tree of its own.
	drmDir      = "/sys/class/drm"
	cmdlinePath = "/proc/cmdline"
	// hdmiConnRe is a DRM HDMI connector's sysfs name: card0-HDMI-A-1.
	hdmiConnRe = regexp.MustCompile(`^card[0-9]{1,2}-HDMI-[AB]-[0-9]{1,2}$`)
)

// HDMIConnectors lists the HDMI connectors under dir (the DRM class directory): their sysfs names,
// sorted. None on a system without the KMS driver or without HDMI.
func HDMIConnectors(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if hdmiConnRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// hdmiRestore is what a connector's status goes back to on "on": the kernel command line's
// `video=<connector>:…` force flag (D or e: on, d: off), else detect.
func hdmiRestore(cmdline, conn string) string {
	name := conn[strings.Index(conn, "-")+1:] // HDMI-A-1
	for _, f := range strings.Fields(cmdline) {
		v, ok := strings.CutPrefix(f, "video="+name+":")
		if !ok || v == "" {
			continue
		}
		switch v[len(v)-1] {
		case 'D', 'e':
			return "on"
		case 'd':
			return "off"
		}
	}
	return "detect"
}

// SetHDMI switches every HDMI connector off, or back to what the boot gave it. A connector whose
// directory does not resolve inside /sys is refused; one that fails is reported and the others
// are still written.
func (Local) SetHDMI(_ context.Context, on bool) error {
	conns := HDMIConnectors(drmDir)
	if len(conns) == 0 {
		return fmt.Errorf("hdmi: no HDMI connector under %s", drmDir)
	}
	sys, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(drmDir)))
	if err != nil {
		return err
	}
	cmdline := ""
	if on {
		b, _ := os.ReadFile(cmdlinePath)
		cmdline = string(b)
	}
	var failed error
	for _, c := range conns {
		real, err := filepath.EvalSymlinks(filepath.Join(drmDir, c))
		if err == nil && !strings.HasPrefix(real, filepath.Clean(sys)+"/") {
			err = fmt.Errorf("resolves to %s, outside /sys", real)
		}
		if err == nil {
			value := "off"
			if on {
				value = hdmiRestore(cmdline, c)
			}
			err = writeLEDAttr(filepath.Join(real, "status"), value)
		}
		if err != nil && failed == nil {
			failed = fmt.Errorf("hdmi %s: %w", c, err)
		}
	}
	return failed
}

// SetHDMI asks the helper to switch the HDMI output off or on.
func (c Client) SetHDMI(ctx context.Context, on bool) error {
	_, err := c.call(ctx, request{Op: opHDMI, Enabled: on})
	return err
}
