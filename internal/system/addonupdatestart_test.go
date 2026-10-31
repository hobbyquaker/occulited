package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// B-98: an update through occulited left RedMatic stopped on the Charly. Its update script stopped
// the unit (the wrapper sends rc.d stop there), put its own link over the wrapper, and started
// Node-RED as root in the install scope, where it died at once (EACCES on a file of its confined
// user, RedMatic bug 8). Nothing of the addon ran afterwards, so the install reported "not running
// and was not started" and the addon stayed down. An addon that ran before the installer is started
// in its unit now; one that was stopped before stays stopped.

// failedStartUpdate is that update, reduced: the marker tells the rig that the installer ran, the
// link replaces the wrapper, and nothing is left running.
const failedStartUpdate = "#!/bin/sh\nmkdir -p ROOT/tmp\ntouch ROOT/tmp/installer-ran\nrm -f ROOT/usr/local/etc/config/rc.d/hmm\nln -sfn ROOT/usr/local/addons/hmm/rc.d/hmm ROOT/usr/local/etc/config/rc.d/hmm\nrm -f ROOT/usr/local/tmp/new_addon.tar.gz\necho 'Error loading settings file: EACCES: permission denied'\nexit 0\n"

// withStates gives addon-hmm.service one ActiveState until the installer has run and another after
// it, and can make the unit's start fail.
func withStates(rig *settleRig, before, after string, failStart bool) {
	orig := rig.a.Systemd.Run
	rig.active["addon-hmm.service"] = before
	rig.a.Systemd.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if _, err := os.Stat(rig.root.join("/tmp/installer-ran")); err == nil {
			rig.active["addon-hmm.service"] = after
		}
		if failStart && name == "systemctl" && len(args) > 0 && args[0] == "start" {
			rig.calls = append(rig.calls, name+" "+strings.Join(args, " "))
			return []byte("Job for addon-hmm.service failed"), errors.New("exit status 1")
		}
		return orig(ctx, name, args...)
	}
}

// B-98, decided in D-67: an installer that failed or asks for a reboot starts nothing; the result names
// the addons that ran before and are stopped now, so the page can ask, and says whether the next boot
// starts them anyway.
func TestInstallNamesTheAddonsItLeftStopped(t *testing.T) {
	exit := func(code string) string { return strings.Replace(failedStartUpdate, "exit 0", "exit "+code, 1) }
	cases := []struct {
		name       string
		installer  string
		before     string
		after      string
		executable bool
		masked     bool
		safemode   bool
		want       []StoppedAddon
		wantOutput string
	}{
		{name: "the installer failed", installer: exit("1"), before: "active", after: "inactive", executable: true,
			want:       []StoppedAddon{{ID: "hmm", StartsAtBoot: true}},
			wantOutput: "[systemd] hmm was running before the install and is stopped now; it was not started: the installer failed"},
		{name: "a reboot, enabled", installer: exit("10"), before: "active", after: "inactive", executable: true,
			want:       []StoppedAddon{{ID: "hmm", StartsAtBoot: true}},
			wantOutput: "[systemd] hmm was running before the install and is stopped now; it was not started: the installer asks for a reboot"},
		{name: "a reboot, the rc.d script not executable", installer: exit("10"), before: "active", after: "inactive",
			want: []StoppedAddon{{ID: "hmm"}}},
		{name: "a reboot, switched off on the Services page", installer: exit("10"), before: "activating", after: "failed", executable: true, masked: true,
			want: []StoppedAddon{{ID: "hmm"}}},
		{name: "a reboot in safe mode", installer: exit("10"), before: "active", after: "inactive", executable: true, safemode: true,
			want: []StoppedAddon{{ID: "hmm"}}},
		{name: "stopped before", installer: exit("1"), before: "inactive", after: "inactive", executable: true},
		{name: "still running afterwards", installer: exit("10"), before: "active", after: "active", executable: true},
		{name: "a good install starts it itself (B-98)", installer: failedStartUpdate, before: "active", after: "inactive", executable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newSettleRig(t, tc.installer)
			old := Priv
			Priv = settlePriv{rig: rig}
			t.Cleanup(func() { Priv = old })
			if tc.executable {
				if err := os.Chmod(rig.root.join("/usr/local/addons/hmm/rc.d/hmm"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.masked {
				rig.a.Systemd.SwitchFile = rig.root.join("/unit-switch.json")
				if err := os.WriteFile(rig.a.Systemd.SwitchFile, []byte(`{"masked":["addon-hmm.service"]}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.safemode {
				if err := os.WriteFile(rig.root.join("/usr/local/etc/config/safemode"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			withStates(rig, tc.before, tc.after, false)
			res, err := rig.a.Install(context.Background(), strings.NewReader(strings.Repeat("x", 100)))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.StoppedAddons) != len(tc.want) || (len(tc.want) > 0 && res.StoppedAddons[0] != tc.want[0]) {
				t.Fatalf("stopped addons %+v, want %+v\noutput:\n%s", res.StoppedAddons, tc.want, res.Output)
			}
			if tc.wantOutput != "" && !strings.Contains(res.Output, tc.wantOutput) {
				t.Errorf("output lacks %q:\n%s", tc.wantOutput, res.Output)
			}
			if len(tc.want) == 0 && strings.Contains(res.Output, "was running before the install") {
				t.Errorf("output names a stopped addon:\n%s", res.Output)
			}
			if len(tc.want) > 0 {
				if rig.index(t, "systemctl start --no-pager -- addon-hmm.service") >= 0 {
					t.Errorf("an addon was started although the installer failed or asks for a reboot:\n%s", strings.Join(rig.calls, "\n"))
				}
				b, _ := json.Marshal(res)
				wantJSON := fmt.Sprintf(`"stopped_addons":[{"id":"hmm","starts_at_boot":%v}]`, tc.want[0].StartsAtBoot)
				if !strings.Contains(string(b), wantJSON) {
					t.Errorf("the API answer %s lacks %s", b, wantJSON)
				}
			}
		})
	}
}

func TestInstallStartsAnAddonItsUpdateLeftStopped(t *testing.T) {
	const confined = `{"id":"hmm","mode":"confined","uid":30007,"user":"addon-hmm","source":"catalog"}`
	cases := []struct {
		name      string
		installer string
		before    string // addon-hmm.service before the installer runs; after it the unit is inactive
		policy    string
		failStart bool
		wantStart bool
		want      []string
		notWant   []string
		wantLog   string
	}{
		{name: "ran before, the update's own start failed", installer: failedStartUpdate, before: "active", wantStart: true,
			want:    []string{"[systemd] hmm ran before the update and its update script left it stopped: started in its unit"},
			notWant: []string{"not running and was not started"},
			wantLog: `msg="addons: an addon that ran before its update was started again" id=hmm`},
		{name: "starting before", installer: failedStartUpdate, before: "activating", wantStart: true,
			want: []string{"left it stopped: started in its unit"}},
		{name: "stopped before", installer: failedStartUpdate, before: "inactive",
			want:    []string{"[systemd] hmm is not running and was not started"},
			notWant: []string{"ran before the update"}},
		{name: "ran before, the installer failed", installer: strings.Replace(failedStartUpdate, "exit 0", "exit 1", 1), before: "active",
			notWant: []string{"ran before the update", "restarted in its unit"}},
		{name: "ran before, the installer asks for a reboot", installer: strings.Replace(failedStartUpdate, "exit 0", "exit 10", 1), before: "active",
			notWant: []string{"ran before the update", "restarted in its unit"}},
		{name: "ran before, the start in its unit fails too", installer: failedStartUpdate, before: "active", failStart: true, wantStart: true,
			want:    []string{"[systemd] hmm ran before the update and is stopped now; starting it in its unit failed: systemctl start addon-hmm.service: exit status 1: Job for addon-hmm.service failed"},
			notWant: []string{"left it stopped: started in its unit"},
			wantLog: `msg="addons: an addon that ran before its update could not be started again" id=hmm`},
		{name: "confined: its files are its own again before the start", installer: failedStartUpdate, before: "active", policy: confined, wantStart: true,
			want: []string{"left it stopped: started in its unit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newSettleRig(t, tc.installer)
			old := Priv
			Priv = settlePriv{rig: rig}
			t.Cleanup(func() { Priv = old })
			if tc.policy != "" {
				if err := os.WriteFile(rig.root.join(AddonPolicyDir+"/hmm.json"), []byte(tc.policy), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			withStates(rig, tc.before, "inactive", tc.failStart)
			var logs bytes.Buffer
			oldLog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(oldLog) })

			res, err := rig.a.Install(context.Background(), strings.NewReader(strings.Repeat("x", 100)))
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(rig.calls, "\n")
			start := rig.index(t, "systemctl start --no-pager -- addon-hmm.service")
			if (start >= 0) != tc.wantStart {
				t.Fatalf("started: %v, want %v\noutput:\n%s\ncalls:\n%s", start >= 0, tc.wantStart, res.Output, joined)
			}
			for _, w := range tc.want {
				if !strings.Contains(res.Output, w) {
					t.Errorf("output lacks %q:\n%s", w, res.Output)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(res.Output, w) {
					t.Errorf("output has %q:\n%s", w, res.Output)
				}
			}
			if tc.wantLog != "" && !strings.Contains(logs.String(), tc.wantLog) {
				t.Errorf("log %q lacks %q", logs.String(), tc.wantLog)
			}
			if tc.wantStart {
				// adopted again before the unit is started, and never the install scope stopped (B-3)
				if adopt := rig.index(t, AddonRCTool+" adopt hmm"); adopt < 0 || adopt > start {
					t.Errorf("the wrapper was not put back before the start:\n%s", joined)
				}
				if strings.Contains(joined, testScope) {
					t.Errorf("the install scope was stopped:\n%s", joined)
				}
			}
			if tc.policy != "" {
				chown := -1
				for i, c := range rig.calls {
					if strings.HasPrefix(c, "chown ") || strings.HasPrefix(c, "lchown ") {
						chown = i
						break
					}
				}
				if chown < 0 || chown > start {
					t.Errorf("a confined addon was started before its files were its own again:\n%s", joined)
				}
			}
		})
	}
}

// B-128: an update whose script started a switched-off addon in its unit: the unit is stopped again and
// stays stopped; before, the install took it for an addon its update had started and left it running.
func TestUpdateKeepsASwitchedOffAddonStopped(t *testing.T) {
	rig := newSettleRig(t, failedStartUpdate)
	old := Priv
	Priv = settlePriv{rig: rig}
	t.Cleanup(func() { Priv = old })
	if err := os.Chmod(rig.root.join("/usr/local/addons/hmm/rc.d/hmm"), 0o755); err != nil {
		t.Fatal(err)
	}
	withStates(rig, "inactive", "active", false)
	rig.a.Systemd.SwitchFile = rig.root.join("/unit-switch.json")
	if err := os.WriteFile(rig.a.Systemd.SwitchFile, []byte(`{"masked":["addon-hmm.service"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.Install(context.Background(), strings.NewReader(strings.Repeat("x", 100)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "hmm is switched off and stays stopped") {
		t.Fatalf("output: %s", res.Output)
	}
	var stops, starts int
	for _, c := range rig.calls {
		if strings.HasPrefix(c, "systemctl stop ") && strings.HasSuffix(c, "addon-hmm.service") {
			stops++
		}
		if (strings.HasPrefix(c, "systemctl start ") || strings.HasPrefix(c, "systemctl restart ")) && strings.HasSuffix(c, "addon-hmm.service") {
			starts++
		}
	}
	if stops == 0 || starts != 0 {
		t.Fatalf("stops %d, starts %d: %v", stops, starts, rig.calls)
	}
}
