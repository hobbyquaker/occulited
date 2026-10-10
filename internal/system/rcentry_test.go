package system

import (
	"context"
	"os"
	"strings"
	"testing"
)

// task 28: the maintainer's own CCU3 hack - rc.d/hdmi-wlan-disable a link to
// /usr/local/bin/hdmi-wlan-disable.sh, adopted behind the wrapper (the link is the .script now).
// The target is named; "remove the rc.d entry" removes the entry and its twin, the target only on
// request, the policy files with them, and leaves no failed unit behind.
func TestRemoveRCEntry(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/bin/hdmi-wlan-disable.sh":                       "#!/bin/sh\ntvservice -o\n",
		"usr/local/etc/config/rc.d/hdmi-wlan-disable":              "#!/bin/sh\n# openccu-lite addon-rc wrapper\n",
		"usr/local/etc/config/addon-policy/hdmi-wlan-disable.conf": "[Service]\n",
		"usr/local/etc/config/rc.d/redmatic":                       "#!/bin/sh\n",
		"usr/local/addons/redmatic/bin/x":                          "x",
	})
	if err := os.Symlink("/usr/local/bin/hdmi-wlan-disable.sh", r.join("/usr/local/etc/config/rc.d/hdmi-wlan-disable.script")); err != nil {
		t.Fatal(err)
	}
	if got := r.RCTarget("hdmi-wlan-disable"); got != "/usr/local/bin/hdmi-wlan-disable.sh" {
		t.Fatalf("target %q", got)
	}
	if r.RCTarget("redmatic") != "" {
		t.Fatal("a plain entry has a target")
	}
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
	// an addon with its own directory is uninstalled, not this
	if _, err := a.RemoveRCEntry(context.Background(), "redmatic", false); err == nil || !strings.Contains(err.Error(), "uninstall") {
		t.Fatalf("redmatic: %v", err)
	}
	calls = nil
	res, err := a.RemoveRCEntry(context.Background(), "hdmi-wlan-disable", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/usr/local/etc/config/rc.d/hdmi-wlan-disable", "/usr/local/etc/config/rc.d/hdmi-wlan-disable.script", "/usr/local/etc/config/addon-policy/hdmi-wlan-disable.conf"} {
		if _, err := os.Lstat(r.join(p)); !os.IsNotExist(err) {
			t.Errorf("%s kept", p)
		}
	}
	if _, err := os.Stat(r.join("/usr/local/bin/hdmi-wlan-disable.sh")); err != nil {
		t.Fatal("the target went without being asked for")
	}
	if res.Target != "/usr/local/bin/hdmi-wlan-disable.sh" {
		t.Errorf("target %q", res.Target)
	}
	want := []string{"systemctl stop --no-pager -- addon-hdmi-wlan-disable.service", "systemctl daemon-reload", "systemctl reset-failed --no-pager -- addon-hdmi-wlan-disable.service"}
	var got []string
	for _, c := range calls {
		if strings.HasPrefix(c, "systemctl stop") || strings.HasPrefix(c, "systemctl daemon-reload") || strings.HasPrefix(c, "systemctl reset-failed") {
			got = append(got, c)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}

	// with the target ticked
	r2 := rootWith(t, map[string]string{"usr/local/bin/hack.sh": "#!/bin/sh\n"})
	_ = os.MkdirAll(r2.join("/usr/local/etc/config/rc.d"), 0o755)
	_ = os.Symlink("../../../bin/hack.sh", r2.join("/usr/local/etc/config/rc.d/hack")) // relative
	a2 := NewSystemdAddons(r2, SystemdServices{Root: r2, Run: run})
	if res, err := a2.RemoveRCEntry(context.Background(), "hack", true); err != nil || res.Target != "/usr/local/bin/hack.sh" {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := os.Stat(r2.join("/usr/local/bin/hack.sh")); !os.IsNotExist(err) {
		t.Error("the ticked target kept")
	}
}

func TestRCTargetRemovable(t *testing.T) {
	for p, want := range map[string]bool{
		"/usr/local/bin/x.sh":                true,
		"/usr/local/sdcard/x":                true,
		"/usr/local/addons/red/bin/x":        false,
		"/usr/local/etc/config/x":            false,
		"/usr/local/var/lib/occulite/x.json": false,
		"/usr/bin/x":                         false,
		"/usr/local/bin/../etc/x":            false,
	} {
		if RCTargetRemovable(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
}
