package system

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// B-58: disabling stops the unit (cancelling a start job queued at boot), reloads the generator,
// and forgets the failed state of the unit whose file is gone.
func TestSettleDisabled(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/etc/config/rc.d/red": "#!/bin/sh\n", "usr/local/etc/config/rc.d/hdmi": "#!/bin/sh\n"})
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if args[0] == "stop" && args[len(args)-1] == "addon-hdmi.service" {
			return []byte("Unit addon-hdmi.service not loaded."), errors.New("exit status 5")
		}
		return nil, nil
	}
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
	if errs := a.SettleDisabled(context.Background()); len(errs) != 0 || len(calls) != 0 {
		t.Fatalf("nothing to settle: %v %v", errs, calls)
	}
	errs := a.SettleDisabled(context.Background(), "red", "hdmi")
	want := []string{
		"systemctl stop --no-pager -- addon-red.service",
		"systemctl stop --no-pager -- addon-hdmi.service",
		"systemctl daemon-reload",
		"systemctl reset-failed --no-pager -- addon-red.service",
		"systemctl reset-failed --no-pager -- addon-hdmi.service",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}
	if len(errs) != 1 || !strings.Contains(errs["hdmi"], "not loaded") {
		t.Errorf("errs %v", errs)
	}
}

// B-58: at start, a disabled addon's unit that systemd still lists as not-found and failed is
// reset; an enabled addon and a disabled one without a ghost are left alone.
func TestClearDisabledGhosts(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/etc/config/rc.d/ghost":         "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/quiet":         "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/live":          "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/live.script":   "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/ghost.script":  "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/bad name here": "#!/bin/sh\n",
	})
	for _, f := range []string{"ghost", "quiet"} {
		_ = os.Chmod(r.join("/usr/local/etc/config/rc.d/"+f), 0o644)
	}
	_ = os.Chmod(r.join("/usr/local/etc/config/rc.d/live"), 0o755)
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if args[0] == "show" {
			if args[len(args)-1] == "addon-ghost.service" {
				return []byte("LoadState=not-found\nActiveState=failed\n"), nil
			}
			return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
		}
		return nil, nil
	}
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
	ids := a.ClearDisabledGhosts(context.Background())
	if strings.Join(ids, ",") != "ghost,quiet" {
		t.Errorf("ids %v", ids)
	}
	want := []string{
		"systemctl show -p LoadState,ActiveState -- addon-ghost.service",
		"systemctl reset-failed --no-pager -- addon-ghost.service",
		"systemctl show -p LoadState,ActiveState -- addon-quiet.service",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}
}
