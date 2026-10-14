package radio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The shadow phase against a sandbox: the detection and plan written, then the check against a
// box that did what the plan says, and against one that did not.
func TestShadowDetectAndCheck(t *testing.T) {
	root := sandbox(t, map[string]string{"raw-uart": "GPIO@3f201000.serial"})
	for _, d := range []string{"var/etc", "etc/config_templates", "run/occulite/radio", "proc/4242", "proc/4243", "proc/4244"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	in := inputs()
	write("var/hm_mode", "HM_HOST='rpi3'\nHM_MODE='NORMAL'\nHM_RTC='rx8130'\n")
	write("etc/config_templates/rfd.conf", rfdTemplate)
	write("etc/config_templates/multimacd.conf", in.TemplateMultimacdConf)
	write("etc/config_templates/crRFD.conf", in.TemplateCrRFDConf)
	write("etc/config_templates/InterfacesList.xml", in.TemplateInterfacesList)
	write("etc/HMServer.conf", in.HMServerConf)
	write("etc/hmipserver.default", "HMIP_BIND_ADDRESS=127.0.0.1\n")
	write("proc/meminfo", "MemTotal:        946000 kB\n")
	write("proc/mounts", "/dev/mmcblk0p3 /usr/local ext4 rw 0 0\n")
	fp := &fakeProbe{answers: map[string]string{"raw-uart": "RPI-RF-MOD 0000000A03 3014F711A0001F0000000A03 0x1F0A14 0x3F0A15 4.4.22"}}
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, f) }
	d := Detector{Root: root, Run: fp.run, GPIOLimit: 0, Sleep: func(d time.Duration) {}}
	sr, err := ShadowDetect(context.Background(), root, d, logf)
	if err != nil {
		t.Fatal(err)
	}
	if !sr.Plan.Multimacd.Run || sr.Plan.HmIPServer.Node != "/dev/mmd_hmip" {
		t.Fatalf("plan: %+v", sr.Plan)
	}
	for _, n := range []string{"modules.json", "plan.json", "render.json"} {
		if !exists(filepath.Join(root, "run/occulite/radio", n)) {
			t.Fatalf("%s not written", n)
		}
	}

	// the box did what the plan says: the old chain's files and daemons as the render has them
	f := sr.Files
	write("var/hm_mode", f.HMMode)
	for k, v := range f.VarFiles {
		write("var/"+k, v)
	}
	write("var/etc/multimacd.conf", f.MultimacdConf)
	write("var/etc/crRFD.conf", f.CrRFDConf)
	write("var/etc/HMServer.conf", f.HMServerConf)
	write("var/etc/rfd.conf", f.VarRFDConf)
	write("etc/config/rfd.conf", f.RFDConf)
	write("etc/config/InterfacesList.xml", in.TemplateInterfacesList)
	// multimacd at level 2, not rfd's 5: the plan holds it there (openccu-lite B-275), and a box
	// whose chain started it at 5 is a reported difference by design
	write("proc/4242/cmdline", "/bin/multimacd\x00-f\x00/var/etc/multimacd.conf\x00-l\x002\x00")
	write("proc/4243/cmdline", "/bin/rfd\x00-f\x00/var/etc/rfd.conf\x00-l\x005\x00")
	write("proc/4244/cmdline", strings.ReplaceAll(strings.Replace(f.Commands["hmipserver"], "java ", "/opt/java/bin/java ", 1), " ", "\x00")+"\x00")
	states := map[string]struct {
		state string
		pid   int
	}{"multimacd.service": {"active", 4242}, "rfd.service": {"active", 4243}, "hmipserver.service": {"active", 4244}, "hs485d.service": {"inactive", 0}, "hmlangw.service": {"inactive", 0}}
	live := Live{Root: root, UnitState: func(ctx context.Context, u string) (string, int) { return states[u].state, states[u].pid }, Cmdline: SystemdLive(root, nil).Cmdline}
	r, err := ShadowCheck(context.Background(), root, live, logf)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Match || len(r.Checked) < 10 {
		t.Fatalf("expected a match: %+v", r)
	}
	if !exists(filepath.Join(root, "run/occulite/radio/shadow.json")) {
		t.Fatal("shadow.json not written")
	}

	// the box differs: another adapter node in crRFD.conf, rfd not running, a foreign interface
	write("var/etc/crRFD.conf", strings.Replace(f.CrRFDConf, "/dev/mmd_hmip", "/dev/raw-uart", 1))
	states["rfd.service"] = struct {
		state string
		pid   int
	}{"failed", 0}
	write("etc/config/InterfacesList.xml", strings.Replace(in.TemplateInterfacesList, "</interfaces>", "<ipc><name>CCU-Jack</name><url>x</url><info>i</info></ipc></interfaces>", 1))
	r, err = ShadowCheck(context.Background(), root, live, logf)
	if err != nil {
		t.Fatal(err)
	}
	if r.Match || len(r.Differences) != 3 {
		t.Fatalf("expected three differences: %+v", r.Differences)
	}
	want := []string{"/var/etc/crRFD.conf differs", "InterfacesList.xml: plan", "rfd: plan runs, unit failed"}
	for i, w := range want {
		if !strings.Contains(r.Differences[i], w) {
			t.Fatalf("difference %d: %q, want %q", i, r.Differences[i], w)
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "shadow: DIFFERENCE") {
		t.Fatalf("log: %s", joined)
	}
}
