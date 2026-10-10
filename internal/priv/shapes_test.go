package priv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// openccu-lite B-234: a program on the list is a program with a shape. Every name on Programs has
// one, and nothing has a shape that is not on the list (a stale key would be a shape nobody checks).
func TestProgramsHaveShapes(t *testing.T) {
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	for _, prog := range p.Programs {
		if programShapes[prog] == nil {
			t.Errorf("%s is on the program list without an argument shape", prog)
		}
	}
	for key := range programShapes {
		found := false
		for _, prog := range p.Programs {
			found = found || prog == key
		}
		if !found {
			t.Errorf("shape for %s, which is not on the program list", key)
		}
	}
	// checkFirmwareUpdate.sh had no caller left (sysupdate replaced it) and is off the list
	if p.programAllowed("/bin/checkFirmwareUpdate.sh", nil) {
		t.Error("checkFirmwareUpdate.sh is still on the program list")
	}
}

// fakeProc writes a process's comm, cmdline and cgroup into a fake /proc.
func fakeProc(t *testing.T, dir string, pid int, comm string, cmdline []string, cgroup string) {
	t.Helper()
	d := filepath.Join(dir, strconv.Itoa(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(cmdline, "\x00")+"\x00"), 0o644)
	_ = os.WriteFile(filepath.Join(d, "cgroup"), []byte("0::"+cgroup+"\n"), 0o644)
}

// The daemon's real command lines (grep 'run(ctx, "' internal/system) pass, and one foreign form
// per program is refused - the forms task 120's audit named first: an arbitrary root command in a
// transient unit, a unit file an addon wrote linked into systemd, any pid killed, a restore of any
// file, the security key set to anything, the network re-plumbed, the clock moved.
func TestProgramShapes(t *testing.T) {
	proc := t.TempDir()
	fakeProc(t, proc, 4242, "udhcpc", []string{"/sbin/udhcpc", "-b", "-i", "eth0"}, "/system.slice/occu-network.service")
	fakeProc(t, proc, 4243, "udhcpc6", []string{"/sbin/udhcpc6", "-f", "-i", "eth0"}, "/system.slice/occu-dhcp6-eth0.service")
	fakeProc(t, proc, 5000, "node", []string{"/usr/local/addons/redmatic/bin/node", "red.js"}, "/user.slice/user-0.slice/session-3.scope")
	fakeProc(t, proc, 5001, "mosquitto", []string{"mosquitto", "-c", "/usr/local/addons/mosquitto/etc/m.conf"}, "/system.slice/addon-mosquitto.service")
	fakeProc(t, proc, 5002, "sh", []string{"/bin/sh", "/usr/local/addons/hmm/update_script"}, "/system.slice/occulite-addon-0a1b2c3d.scope")
	fakeProc(t, proc, 6000, "java", []string{"/opt/java/bin/java", "-jar", "/opt/HmIP/hmserver.jar", "/usr/local/addons/x"}, "/system.slice/hmipserver.service")
	fakeProc(t, proc, 6001, "rfd", []string{"/bin/rfd", "-f", "/etc/config/rfd.conf"}, "/system.slice/rfd.service")
	fakeProc(t, proc, 6002, "lighttpd", []string{"/usr/sbin/lighttpd", "-f", "/etc/lighttpd/lighttpd.conf"}, "/system.slice/lighttpd.service")
	fakeProc(t, proc, 1, "systemd", []string{"/sbin/init"}, "/init.scope")
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	p.ProcDir = proc

	type line struct {
		prog string
		args string
	}
	pass := []line{
		// systemctl: the Services page, the switch, the unit editor, the own timers, the network,
		// the boot chart, the addon layer, B-236's reset-failed
		{"systemctl", "list-units --type=service --all --no-pager --plain --output=json"},
		{"systemctl", "list-timers --all --no-pager --output=json"},
		{"systemctl", "show --timestamp=unix -p Id,MainPID,UnitFileState -- rfd.service addon-hmm.service"},
		{"systemctl", "show -p Id,LoadState,ActiveState -- lighttpd.service rfd.service hmipserver.service"},
		{"systemctl", "show --all -p Id,ActiveState *"},
		{"systemctl", "show -p Version,KernelTimestampMonotonic"},
		{"systemctl", "show -p ActiveState,MainPID -- hmipserver.service"},
		{"systemctl", "cat --no-pager -- rfd.service"},
		{"systemctl", "is-enabled -- chrony.service"},
		{"systemctl", "is-active -- multimacd.service rfd.service hmipserver.service hs485d.service hmlangw.service"},
		{"systemctl", "start --no-pager -- addon-hmm.service"},
		{"systemctl", "stop --no-pager -- addon-redmatic.service"},
		{"systemctl", "restart --no-pager -- occu-init-rf-hardware.service"},
		{"systemctl", "restart --no-pager -- local-backup.timer"},
		{"systemctl", "reload --no-pager -- lighttpd.service"},
		{"systemctl", "reload lighttpd"},
		{"systemctl", "reload occu-network.service"},
		{"systemctl", "stop occu-dhcp6-eth0.service"},
		{"systemctl", "start --no-block occu-radio-hotplug.service"},
		{"systemctl", "start --no-block --no-pager -- local-nightly.service"},
		{"systemctl", "start --no-block occu-backup-create@usb-1.service"},
		{"systemctl", "reset-failed addon-redmatic.service"},
		{"systemctl", "daemon-reload"},
		{"systemctl", "poweroff"},
		{"systemctl", "mask --runtime --now --no-pager -- hmlangw.service"},
		{"systemctl", "unmask --runtime --no-pager -- hmlangw.service"},
		{"systemctl", "enable --runtime --no-pager -- hs485d.service"},
		{"systemctl", "enable --runtime --now --no-pager -- local-backup.timer"},
		{"systemctl", "disable --runtime --now --no-pager -- local-backup.timer"},
		{"systemctl", "show -p Id -- getty@tty1.service dev-disk-by\\x2dlabel-usb.mount"},
		{"/usr/bin/systemctl", "daemon-reload"},
		// systemd-run: the install scope around the installer or an rc.d script, the DHCPv6 client
		{"systemd-run", "--scope --quiet --unit=occulite-addon-0a1b2c3d.scope -- /bin/install_addon"},
		{"systemd-run", "--scope --quiet --unit=occulite-addon-ffffffff.scope -- /usr/local/etc/config/rc.d/redmatic uninstall"},
		{"systemd-run", "--unit=occu-dhcp6-eth0.service --collect --quiet /sbin/udhcpc6 -f -S -t 5 -T 3 -O dns -O search -i eth0 -s /usr/libexec/occu/lite-dhcp6 -p /var/run/udhcpc6_eth0.pid"},
		// the DHCP clients, the address, the routes, resolvconf, the hostname
		{"/sbin/udhcpc", "-b -t 20 -T 3 -S -x hostname:openccu -i eth0 -F openccu -V eQ3-CCU3 -s /bin/dhcp.script -p /var/run/udhcpc_eth0.pid"},
		// openccu-lite task 327: the vendor class of /etc/dhcp-vendor-class
		{"/sbin/udhcpc", "-b -t 20 -T 3 -S -x hostname:openccu-lite-3f2a -i eth0 -F openccu-lite-3f2a -V openccu-lite -s /bin/dhcp.script -p /var/run/udhcpc_eth0.pid"},
		{"/sbin/udhcpc6", "-b -S -t 5 -T 3 -O dns -O search -i eth0 -s /usr/libexec/occu/lite-dhcp6 -p /var/run/udhcpc6_eth0.pid"},
		{"/sbin/ip", "-4 -o addr show dev eth0"},
		{"/sbin/ip", "-4 addr flush dev eth0"},
		{"/sbin/ip", "route del default"},
		{"/sbin/ip", "route add default via 192.0.2.1"},
		{"/sbin/ip", "route add 224.0.0.0/24 dev eth0 scope link"},
		{"/sbin/ip", "-6 route del default via fe80::1 dev eth0"},
		{"/sbin/ip", "-6 addr del 2001:db8::5/64 dev eth0"},
		{"/sbin/ip", "-6 addr del 2001:db8::5 dev eth0"},
		{"/sbin/ip", "-6 addr replace 2001:db8::5/64 dev eth0"},
		{"/sbin/ip", "-6 route replace default via fe80::1 dev eth0"},
		{"/sbin/ifconfig", "eth0 192.0.2.70 netmask 255.255.255.0"},
		{"/sbin/resolvconf", "-a eth0"},
		{"/sbin/resolvconf", "-a eth0.ipv6"},
		{"/sbin/resolvconf", "-x -a lo.occulite"},
		{"/sbin/resolvconf", "-d eth0.dhcp6"},
		{"/sbin/resolvconf", "-f -d lo.occulite"},
		{"hostname", "openccu-wz"},
		// kill: the DHCP clients by their pid files, an addon's strays (TERM, then KILL)
		{"kill", "4242"},
		{"kill", "4243"},
		{"kill", "-TERM 5000 5001 5002"},
		{"kill", "-KILL 5000"},
		// the clock, the key, the module's version, the scripts
		{"date", "-u\x00-s\x002026-09-26 10:00:00"},
		// openccu-lite B-281: the import outcome's journal read, the pattern fixed, the date validated
		{"journalctl", "-u\x00hmipserver.service\x00-o\x00cat\x00-q\x00--no-pager\x00-g\x00Adapter exchange\x00--since\x002026-09-27 21:38:16"},
		{"/sbin/hwclock", "-wu"},
		{"/bin/SetInterfaceClock", "127.0.0.1:2001"},
		{"/bin/crypttool", "-v -t 0"},
		{"/bin/crypttool", "-v -t 3 -k My_Key_2026"},
		{"/bin/crypttool", "-g"},
		{"/bin/crypttool", "-S -k My_Key_2026 -i 3"},
		{"/bin/crypttool", "-S -i 2 -k My_Key_2026"},
		{"/bin/eq3configcmd", "update-coprocessor -p /dev/raw-uart1 -t HM-MOD-UART -c -v"},
		{"/bin/detect_radio_module", "/dev/raw-uart"},
		{"/bin/install_addon", ""},
		{"/bin/createBackup.sh", "/usr/local/tmp/openccu-3.89.11-2026-09-26-1000.sbk"},
		{"/bin/restoreBackup.sh", "-c /usr/local/tmp/restore-1.sbk"},
		{"/bin/restoreBackup.sh", "/usr/local/tmp/restore-1.sbk"},
		{"/bin/restoreBackup.sh", "-f /usr/local/tmp/restore-1.sbk"},
		{"/bin/restoreBackup.sh", "-c -f /usr/local/tmp/restore-1.sbk"}, // the check past a missing key (task 296)
		{"/bin/restoreBackup.sh", "-c /usr/local/etc/occulite/staging/up.sbk"},
		{"/bin/cronBackup.sh", ""},
		{"/bin/updateTZ.sh", ""},
		{"/sbin/reboot", ""},
		{"/bin/reboot", ""},
		{"/sbin/poweroff", ""},
		{"/bin/poweroff", ""},
		{"/usr/libexec/occu/lite-addon-rc", "adopt hmm redmatic mosquitto"},
		{"/usr/libexec/occu/lite-addon-rc", "adopt"},
		{"/usr/libexec/occu/lite-extension-dirs", "reset /firmware/rftypes"},
		{"/usr/libexec/occu/lite-ca-certificates", ""},
		{"sh", "-c\x00echo '1' > '/proc/sys/net/ipv6/conf/eth0/accept_ra'"},
		// the init and rc.d scripts, one action word
		{"/etc/init.d/S50lighttpd", "reload"},
		{"/etc/init.d/S50sshd", "stop"},
		{"/etc/init.d/S46chronyd", "restart"},
		{"/etc/init.d/S47InitRFHardware", "start"},
		{"/usr/local/etc/config/rc.d/hmm", "info"},
		{"/usr/local/etc/config/rc.d/hmm", "uninstall"},
	}
	refuse := []line{
		// the finding's forms
		{"systemd-run", "/bin/sh -c id"},
		{"systemd-run", "--scope --quiet --unit=occulite-addon-0a1b2c3d.scope -- /bin/sh -c id"},
		{"systemd-run", "--scope --quiet --unit=occulite-addon-0a1b2c3d.scope -- systemctl link /usr/local/addons/x/evil.service"},
		{"systemd-run", "--scope --quiet --unit=occulite-addon-0a1b2c3d.scope -- systemd-run /bin/sh -c id"},
		{"systemd-run", "--scope --quiet --unit=occulite-addon-0a1b2c3d.scope -- /bin/install_addon /usr/local/tmp/x.tar.gz"},
		{"systemd-run", "--scope --quiet --unit=occulite-addon-0a1b2c3d.scope -- /usr/local/etc/config/rc.d/redmatic start-daemon"},
		{"systemd-run", "--scope --quiet --unit=rfd.service -- /bin/install_addon"},
		{"systemd-run", "--scope --quiet --unit=occulite-addon-0a1b2c3d.scope --uid=0 -- /bin/install_addon"},
		{"systemd-run", "--unit=occu-dhcp6-eth0.service --collect --quiet /sbin/udhcpc6 -f -S -t 5 -T 3 -O dns -O search -i eth1 -s /usr/libexec/occu/lite-dhcp6 -p /var/run/udhcpc6_eth1.pid"},
		{"systemd-run", "--unit=occu-dhcp6-eth0.service --collect --quiet /bin/sh -c id"},
		{"systemd-run", "--unit=occu-dhcp6-eth0.service --collect --quiet /sbin/udhcpc6 -f -S -t 5 -T 3 -O dns -O search -i eth0 -s /usr/local/addons/x/evil.sh -p /var/run/udhcpc6_eth0.pid"},
		{"systemd-run", "--unit=occu-dhcp6-eth0.service --collect --quiet --property=User=root /sbin/udhcpc6 -f"},
		{"systemctl", "link /usr/local/addons/x/evil.service"},
		{"systemctl", "enable --runtime /usr/local/addons/x/evil.service"},
		{"systemctl", "enable --runtime --now -- /usr/local/addons/x/evil.service"},
		{"systemctl", "enable hs485d.service"},
		{"systemctl", "enable --now -- hs485d.service"},
		{"systemctl", "mask --now -- rfd.service"},
		{"systemctl", "mask --runtime --now -- rfd.service hmipserver.service"},
		{"systemctl", "set-environment LD_PRELOAD=/usr/local/addons/x/evil.so"},
		{"systemctl", "edit --full rfd.service"},
		{"systemctl", "set-property occulited.service User=root"},
		{"systemctl", "kill -s KILL rfd.service"},
		{"systemctl", "isolate rescue.target"},
		{"systemctl", "daemon-reexec"},
		{"systemctl", "daemon-reload rfd.service"},
		{"systemctl", "halt"},
		{"systemctl", "reboot"},
		{"systemctl", "start"},
		{"systemctl", "start --root=/usr/local/addons/x evil.service"},
		{"systemctl", "--user start evil.service"},
		{"systemctl", "-H root@192.0.2.16 start rfd"},
		{"systemctl", "start --force --force rfd.service"},
		{"systemctl", "show -p Id;id -- rfd.service"},
		{"systemctl", "show -p \"Id -- rfd.service"},
		{"systemctl", "start -- ../evil.service"},
		{"systemctl", "start -- -evil.service"},
		{"systemctl", "list-units *"},
		{"systemctl", "list-unit-files"},
		{"systemctl", ""},
		{"kill", "1"},
		{"kill", "6000"}, // hmipserver, although its command line names the addons' tree
		{"kill", "6001"}, // rfd
		{"kill", "6002"}, // lighttpd
		{"kill", "-KILL 5000 6001"},
		{"kill", "-9 5000"},
		{"kill", "-HUP 5000"},
		{"kill", "-TERM"},
		{"kill", "-1"},
		{"kill", "0"},
		{"kill", "5000 ; id"},
		{"kill", "99999999999"},
		{"kill", "7777"}, // no such process
		{"kill", strconv.Itoa(os.Getpid())},
		{"/bin/install_addon", "/usr/local/tmp/x.tar.gz"},
		{"/bin/restoreBackup.sh", "/etc/config/shadow"},
		{"/bin/restoreBackup.sh", "-c /etc/passwd"},
		{"/bin/restoreBackup.sh", "-r /usr/local/tmp/restore-1.sbk"},
		{"/bin/restoreBackup.sh", "-f -c /usr/local/tmp/restore-1.sbk"},
		{"/bin/restoreBackup.sh", "/usr/local/tmp/../../etc/passwd"},
		{"/bin/restoreBackup.sh", "usr/local/tmp/x.sbk"},
		{"/bin/restoreBackup.sh", ""},
		{"/bin/createBackup.sh", "/etc/config/rfd.conf"},
		{"/bin/createBackup.sh", ""},
		{"/bin/cronBackup.sh", "-x"},
		{"/bin/crypttool", "-S -k My_Key_2026"},
		{"/bin/crypttool", "-S -k 'k' -i 1"},
		{"/bin/crypttool", "-S -k My_Key_2026 -i 1 -f /etc/config/keys"},
		{"/bin/crypttool", "-v -t 3 -k $(id)"},
		{"/bin/crypttool", "-h"},
		{"/sbin/ip", "link set eth0 down"},
		{"/sbin/ip", "netns exec x /bin/sh"},
		{"/sbin/ip", "-batch /usr/local/addons/x/cmds"},
		{"/sbin/ip", "route add default via 192.0.2.1 dev eth0 table 100"},
		{"/sbin/ip", "route add default via evil"},
		{"/sbin/ip", "-6 addr replace 2001:db8::5/129 dev eth0"},
		{"/sbin/ip", "-4 addr flush dev lo"},
		{"/sbin/ip", "-4 addr flush dev ../x"},
		{"/sbin/ifconfig", "eth0 192.0.2.70 netmask 255.255.255.0 up"},
		{"/sbin/ifconfig", "eth0 down"},
		{"/sbin/resolvconf", "-a ../../etc/passwd"},
		{"/sbin/resolvconf", "-u"},
		{"/sbin/resolvconf", "-a eth0 -x -f"},
		{"/sbin/udhcpc", "-b -i eth0 -s /usr/local/addons/x/evil.sh"},
		{"/sbin/udhcpc", "-b -t 20 -T 3 -S -x hostname:openccu -i eth0 -F other -V eQ3-CCU3 -s /bin/dhcp.script -p /var/run/udhcpc_eth0.pid"},
		{"/sbin/udhcpc", "-b -t 20 -T 3 -S -x hostname:openccu -i eth0 -F openccu -V eQ3-CCU3 -s /bin/dhcp.script -p /etc/config/rfd.conf"},
		{"/sbin/udhcpc", "-b -t 20 -T 3 -S -x hostname:openccu -i eth0 -F openccu -V -s -s /bin/dhcp.script -p /var/run/udhcpc_eth0.pid"},
		{"/sbin/udhcpc", "-b -t 20 -T 3 -S -x hostname:openccu -i eth0 -F openccu -V ../x -s /bin/dhcp.script -p /var/run/udhcpc_eth0.pid"},
		{"/sbin/udhcpc", "-b -t 20 -T 3 -S -x hostname:openccu -i eth0 -F openccu -V $(id) -s /bin/dhcp.script -p /var/run/udhcpc_eth0.pid"},
		{"/sbin/udhcpc6", "-f -S -t 5 -T 3 -O dns -O search -i eth0 -s /usr/local/addons/x/evil.sh -p /var/run/udhcpc6_eth0.pid"},
		{"hostname", ""},
		{"hostname", "-F /etc/passwd"},
		{"hostname", "a b"},
		{"date", "-s\x002026-09-26 10:00:00"},
		{"date", "-u\x00-s\x002026-09-26 10:00:00\x00+%s"},
		{"date", "-u -s now"},
		{"date", "-u -s 2026-09-26 10:00:00"}, // the time split into two arguments
		{"journalctl", "-u\x00hmipserver.service\x00-o\x00cat\x00-q\x00--no-pager\x00-g\x00Adapter exchange\x00--since\x00yesterday"},
		{"journalctl", "-u\x00rfd.service\x00-o\x00cat\x00-q\x00--no-pager\x00-g\x00Adapter exchange\x00--since\x002026-09-27 21:38:16"},
		{"journalctl", "-u\x00hmipserver.service\x00-o\x00cat\x00-q\x00--no-pager\x00-g\x00.*\x00--since\x002026-09-27 21:38:16"},
		{"journalctl", "-u\x00hmipserver.service\x00-o\x00cat\x00-q\x00--no-pager\x00-g\x00Adapter exchange\x00--since\x002026-09-27 21:38:16\x00-D\x00/tmp"},
		{"journalctl", "-b"},
		{"/sbin/hwclock", "-s"},
		{"/sbin/hwclock", "-wu -f /dev/rtc9"},
		{"/bin/SetInterfaceClock", "192.0.2.1:2001"},
		{"/bin/SetInterfaceClock", "127.0.0.1:2001 extra"},
		{"/bin/eq3configcmd", "update-coprocessor -p /dev/raw-uart1 -t HM-MOD-UART -f /usr/local/tmp/x.eq3"},
		{"/bin/eq3configcmd", "update-coprocessor -p /etc/passwd -t HM-MOD-UART -c -v"},
		{"/bin/detect_radio_module", "/dev/ttyAMA0"},
		{"/bin/detect_radio_module", ""},
		{"/usr/libexec/occu/lite-addon-rc", "adopt ../evil"},
		{"/usr/libexec/occu/lite-addon-rc", "release hmm"},
		{"/usr/libexec/occu/lite-extension-dirs", "reset /etc"},
		{"/usr/libexec/occu/lite-extension-dirs", "reset"},
		{"/usr/libexec/occu/lite-ca-certificates", "--fresh"},
		{"/sbin/reboot", "-f"},
		{"/bin/updateTZ.sh", "Europe/Berlin"},
		{"/etc/init.d/S50lighttpd", "reload now"},
		{"/etc/init.d/S50lighttpd", ""},
		{"/etc/init.d/S50lighttpd", "exec"},
		{"/usr/local/etc/config/rc.d/hmm", "start-daemon"},
		{"/usr/local/etc/config/rc.d/../../../../bin/sh", "start"},
		{"/bin/checkFirmwareUpdate.sh", ""},
		{"/usr/bin/systemd-run", "--scope -- /bin/install_addon"},
		{"chown", "-R 0:0 /"},
	}
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		if strings.Contains(s, "\x00") {
			return strings.Split(s, "\x00")
		}
		return strings.Fields(s)
	}
	for _, l := range pass {
		if !p.programAllowed(l.prog, split(l.args)) {
			t.Errorf("refused a form the daemon builds: %s %s", l.prog, l.args)
		}
	}
	for _, l := range refuse {
		if p.programAllowed(l.prog, split(l.args)) {
			t.Errorf("allowed: %s %s", l.prog, l.args)
		}
	}
}

// The same under a development root: the daemon joins the root onto the scripts' paths and the
// installer's, and the archive paths lie under that root's /usr/local/tmp.
func TestProgramShapesUnderRoot(t *testing.T) {
	root := t.TempDir()
	p := DefaultPolicy(root, "/usr/local/etc/occulite") // the state directory root-relative, as the helper's tests do
	at := func(rel string) string { return filepath.Join(root, rel) }
	for _, c := range []struct {
		prog string
		args []string
		want bool
	}{
		{at("/bin/install_addon"), nil, true},
		{at("/bin/restoreBackup.sh"), []string{"-c", at("/usr/local/tmp/x.sbk")}, true},
		{at("/bin/restoreBackup.sh"), []string{"-c", "/usr/local/tmp/x.sbk"}, false}, // outside the root
		{at("/bin/createBackup.sh"), []string{at("/usr/local/etc/occulite/staging/x.sbk")}, true},
		{at("/bin/crypttool"), []string{"-v", "-t", "0"}, true},
		{at("/etc/init.d/S50sshd"), []string{"start"}, true},
		{at("/usr/local/etc/config/rc.d/hmm"), []string{"info"}, true},
		{"systemd-run", []string{"--scope", "--quiet", "--unit=occulite-addon-0a1b2c3d.scope", "--", at("/bin/install_addon")}, true},
		{"systemd-run", []string{"--scope", "--quiet", "--unit=occulite-addon-0a1b2c3d.scope", "--", at("/usr/local/etc/config/rc.d/hmm"), "uninstall"}, true},
		{"systemd-run", []string{"--scope", "--quiet", "--unit=occulite-addon-0a1b2c3d.scope", "--", at("/bin/sh"), "-c", "id"}, false},
		{at("/bin/sh"), []string{"-c", "id"}, false},
	} {
		if got := p.programAllowed(c.prog, c.args); got != c.want {
			t.Errorf("%s %v = %v, want %v", c.prog, c.args, got, c.want)
		}
	}
}

// A refusal is one logged line at the boundary and never reaches the operation.
func TestRefusedRunIsLoggedAndNotRun(t *testing.T) {
	var lines []string
	ops := &recordRun{}
	srv := &Server{Policy: DefaultPolicy("/", "/usr/local/etc/occulite"), Ops: ops, Log: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	r := srv.do(context.Background(), request{Op: "run", Name: "systemd-run", Args: []string{"/bin/sh", "-c", "id"}})
	if !strings.HasPrefix(r.Error, "refused") {
		t.Fatalf("not refused: %+v", r)
	}
	if len(ops.cmds) != 0 {
		t.Errorf("the refused command reached the operation: %v", ops.cmds)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "refused run systemd-run") {
		t.Errorf("log: %q", lines)
	}
	// openccu-lite task 296: a passphrase the shape refuses (crypttool takes letters, digits and _)
	// is refused without its value in the journal
	lines = nil
	if r := srv.do(context.Background(), request{Op: "run", Name: "/bin/crypttool", Args: []string{"-v", "-t", "3", "-k", "pass word!"}}); !strings.HasPrefix(r.Error, "refused") {
		t.Fatalf("not refused: %+v", r)
	}
	if len(lines) != 1 || strings.Contains(lines[0], "pass word!") || !strings.Contains(lines[0], "-k <redacted>") {
		t.Errorf("the passphrase in the log: %q", lines)
	}
	r = srv.do(context.Background(), request{Op: "run", Name: "systemctl", Args: []string{"daemon-reload"}})
	if r.Error != "" || len(ops.cmds) != 1 {
		t.Errorf("the allowed command: %+v %v", r, ops.cmds)
	}
}

// occulited B-30: a process of an addon user may be signalled although its command line is a
// title alone (a confined node-red); one with a uid of the system's, or with any of its uids below
// the addon range, may not - nor an addon user's process inside a system unit.
func TestKillableAddonUser(t *testing.T) {
	proc := t.TempDir()
	status := func(pid int, uids string) {
		_ = os.WriteFile(filepath.Join(proc, strconv.Itoa(pid), "status"), []byte("Name:\tx\nUid:\t"+uids+"\nGid:\t0\t0\t0\t0\n"), 0o644)
	}
	fakeProc(t, proc, 7000, "node-red", []string{"node-red"}, "/system.slice/occulite-addon-0a1b2c3d.scope")
	status(7000, "30000\t30000\t30000\t30000")
	fakeProc(t, proc, 7001, "node-red", []string{"node-red"}, "/system.slice/occulite-addon-0a1b2c3d.scope")
	status(7001, "30000\t0\t30000\t30000") // setuid root
	fakeProc(t, proc, 7002, "sshd", []string{"sshd-session"}, "/system.slice/sshd.service")
	status(7002, "0\t0\t0\t0")
	fakeProc(t, proc, 7003, "node-red", []string{"node-red"}, "/system.slice/rfd.service")
	status(7003, "30000\t30000\t30000\t30000")
	fakeProc(t, proc, 7004, "sh", []string{"sh", "-c", "md5sum /usr/local/addons/hmm/etc/hmm.env"}, "/system.slice/sshd.service")
	status(7004, "0\t0\t0\t0")
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	p.ProcDir = proc
	for pid, want := range map[int]bool{7000: true, 7001: false, 7002: false, 7003: false, 7004: false} {
		if got := p.killable(pid); got != want {
			t.Errorf("killable(%d) = %v", pid, got)
		}
	}
}

// occulited B-59: a root process whose title names nothing (RedMatic's node-red started by its
// update script) may be signalled when its executable is under /usr/local/addons/ - and not when
// it sits in a system unit, nor when its executable is the system's.
func TestKillableByAddonExe(t *testing.T) {
	proc := t.TempDir()
	fakeProc(t, proc, 7100, "node-red", []string{"node-red"}, "/system.slice/occulite-addon-4de547d3.scope")
	_ = os.Symlink("/usr/local/addons/redmatic/bin/node", filepath.Join(proc, "7100", "exe"))
	fakeProc(t, proc, 7101, "node-red", []string{"node-red"}, "/system.slice/lighttpd.service")
	_ = os.Symlink("/usr/local/addons/redmatic/bin/node", filepath.Join(proc, "7101", "exe"))
	fakeProc(t, proc, 7102, "lighttpd", []string{"/usr/sbin/lighttpd"}, "/system.slice/occulite-addon-4de547d3.scope")
	_ = os.Symlink("/usr/sbin/lighttpd", filepath.Join(proc, "7102", "exe"))
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	p.ProcDir = proc
	for pid, want := range map[int]bool{7100: true, 7101: false, 7102: false} {
		if got := p.killable(pid); got != want {
			t.Errorf("killable(%d) = %v", pid, got)
		}
	}
}
