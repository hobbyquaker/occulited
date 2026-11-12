package wifi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recorder struct {
	calls  []string
	routes string // the answer to ip -4 route show default
}

func (r *recorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, line)
	if line == "ip -4 route show default" {
		return []byte(r.routes), nil
	}
	return nil, nil
}

// runIn records a command with its standard input after a "<" (B-143: resolvconf's record).
func (r *recorder) runIn(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " ")+" <"+string(stdin))
	return nil, nil
}

func (r *recorder) has(t *testing.T, want ...string) {
	t.Helper()
	all := strings.Join(r.calls, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Fatalf("no %q in\n%s", w, all)
		}
	}
}

// a Pi's sysfs: the onboard chip on SDIO, and wlan0 once the driver is loaded
func piRoot(t *testing.T, iface bool) string {
	t.Helper()
	root := t.TempDir()
	put := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("/sys/bus/sdio/devices/mmc1:0001:1/uevent", "SDIO_ID=02D0:A9A6\nOF_NAME=wifi\n")
	if iface {
		put("/sys/class/net/wlan0/wireless/.keep", "")
	}
	return root
}

func TestUpDHCP(t *testing.T) {
	root := piRoot(t, true)
	s := Defaults()
	s.Enabled = true
	if err := writeFile(filepath.Join(root, SettingsFile), s.Format(), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	b := Box{Root: root, Run: r.run, Wait: time.Second}
	if err := b.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.has(t, "modprobe brcmfmac", "rfkill unblock wlan", "iw dev wlan0 set power_save off", "ip link set wlan0 up", "systemctl start occu-wpa@wlan0.service", "systemctl restart occu-wifi-dhcp@wlan0.service")
	// with Wi-Fi preferred, a static Ethernet route moves under DHCP too
	s.Preferred = "wlan"
	if err := writeFile(filepath.Join(root, SettingsFile), s.Format(), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := &recorder{routes: "default via 192.168.1.1 dev eth0 \n"}
	if err := (Box{Root: root, Run: r2.run, Wait: time.Second}).Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	r2.has(t, "ip -4 route replace default via 192.168.1.1 dev eth0 metric 700")
	// a supplicant file without networks, 0600, so that it scans
	st, err := os.Stat(filepath.Join(root, ConfFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("conf: %v %v", st, err)
	}
}

func TestUpOffUnloads(t *testing.T) {
	root := piRoot(t, true)
	r := &recorder{}
	if err := (Box{Root: root, Run: r.run}).Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.has(t, "systemctl stop occu-wifi-dhcp@wlan0.service occu-wpa@wlan0.service", "ip link set wlan0 down", "rfkill block wlan", "modprobe -r brcmfmac_wcc brcmfmac_cyw brcmfmac brcmutil")
	for _, c := range r.calls {
		if strings.HasPrefix(c, "systemctl start") {
			t.Fatalf("switched off, but %q", c)
		}
	}
}

func TestUpStaticPreferredDemotesStaticEthernet(t *testing.T) {
	root := piRoot(t, true)
	s := Settings{Enabled: true, Iface: "wlan0", Country: "DE", Mode: "static", Address: "192.168.2.50", Netmask: "255.255.255.0", Gateway: "192.168.2.1", DNS: []string{"192.168.2.1"}, Preferred: "wlan"}
	if err := writeFile(filepath.Join(root, SettingsFile), s.Format(), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &recorder{routes: "default via 192.168.1.1 dev eth0 \ndefault via 192.168.2.1 dev wlan0 metric 5 \n"}
	if err := (Box{Root: root, Run: r.run, RunIn: r.runIn, Wait: time.Second}).Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.has(t, "systemctl stop occu-wifi-dhcp@wlan0.service", "ip -4 addr replace 192.168.2.50/24 dev wlan0", "ip -4 route replace default via 192.168.2.1 dev wlan0 metric 5", "resolvconf -a", "ip -4 route replace default via 192.168.1.1 dev eth0 metric 700", "ip -4 route del default via 192.168.1.1 dev eth0 metric 0")
	// dhcp.script's route has a metric: left alone
	r2 := &recorder{routes: "default via 192.168.1.1 dev eth0 metric 10 \n"}
	if err := (Box{Root: root, Run: r2.run, Wait: time.Second}).Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range r2.calls {
		if strings.Contains(c, "dev eth0 metric 700") {
			t.Fatalf("a DHCP Ethernet route was moved: %q", c)
		}
	}
}

func TestTakeBootFile(t *testing.T) {
	root := piRoot(t, true)
	if err := writeFile(filepath.Join(root, "/boot/openccu-lite-wifi.txt.txt"), "\uFEFFssid=Lab Net\r\npsk=secret password\r\ncountry=AT\r\n", 0o644); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	b := Box{Root: root, Run: r.run, Wait: time.Second}
	if err := b.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "/boot/openccu-lite-wifi.txt.txt")); !os.IsNotExist(err) {
		t.Fatal("the setup file is still there")
	}
	r.has(t, "mount -o remount,rw /boot", "mount -o remount,ro /boot", "systemctl start occu-wpa@wlan0.service")
	s := b.Settings()
	if !s.Enabled || s.Country != "AT" {
		t.Fatalf("%+v", s)
	}
	conf, _ := os.ReadFile(filepath.Join(root, ConfFile))
	if !strings.Contains(string(conf), `ssid="Lab Net"`) || strings.Contains(string(conf), "secret password") || !strings.Contains(string(conf), "country=AT") {
		t.Fatalf("conf:\n%s", conf)
	}
	// a broken file stays, and says why
	if err := writeFile(filepath.Join(root, "/boot/openccu-lite-wifi.txt"), "ssid=x\npsk=short\n", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.TakeBootFile(context.Background()); err == nil {
		t.Fatal("a short password was taken")
	}
	if why, _ := os.ReadFile(filepath.Join(root, SetupErrorFile)); !strings.Contains(string(why), "password") {
		t.Fatalf("why: %q", why)
	}
}

// occulited B-143: the nameservers of a static Wi-Fi address reach resolvconf on its standard input,
// and down takes the record away - argument slices, never a shell.
func TestResolvconfWithoutShell(t *testing.T) {
	cases := []struct {
		name  string
		dns   []string
		runIn bool
		want  []string // calls that must be there
		none  string   // a call that must not
	}{
		{"two servers", []string{"192.168.2.1", "9.9.9.9"}, true, []string{"/sbin/resolvconf -a wlan0 <nameserver 192.168.2.1\nnameserver 9.9.9.9\n"}, ""},
		{"one server", []string{"192.168.2.1"}, true, []string{"/sbin/resolvconf -a wlan0 <nameserver 192.168.2.1\n"}, ""},
		{"none: no record", nil, true, nil, "resolvconf -a"},
		{"no runner with stdin: skipped", []string{"192.168.2.1"}, false, nil, "resolvconf -a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := piRoot(t, true)
			s := Settings{Enabled: true, Iface: "wlan0", Country: "DE", Mode: "static", Address: "192.168.2.50", Netmask: "255.255.255.0", Gateway: "192.168.2.1", DNS: c.dns}
			if err := writeFile(filepath.Join(root, SettingsFile), s.Format(), 0o644); err != nil {
				t.Fatal(err)
			}
			r := &recorder{}
			b := Box{Root: root, Run: r.run, Wait: time.Second}
			if c.runIn {
				b.RunIn = r.runIn
			}
			if err := b.Up(context.Background()); err != nil {
				t.Fatal(err)
			}
			r.has(t, c.want...)
			all := strings.Join(r.calls, "\n")
			if c.none != "" && strings.Contains(all, c.none) {
				t.Errorf("%q in\n%s", c.none, all)
			}
			if err := b.Down(context.Background()); err != nil {
				t.Fatal(err)
			}
			r.has(t, "/sbin/resolvconf -d wlan0")
			for _, call := range r.calls {
				if strings.HasPrefix(call, "sh ") || strings.Contains(call, " -c ") {
					t.Errorf("a shell: %q", call)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "run/occulite/wifi-resolv.conf")); !os.IsNotExist(err) {
				t.Error("the record was written to a file")
			}
		})
	}
}
