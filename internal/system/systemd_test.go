package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B-65: failed and skipped from a unit's list-units row and the one `systemctl show` - what the
// Services page marks red, what it calls skipped, and what is only stopped
func TestSystemdServicesState(t *testing.T) {
	cases := []struct {
		unit, active, sub, props string
		running, failed, skipped bool
	}{
		{"rfd", "active", "running", "MainPID=688\nConditionResult=yes\nConditionTimestampMonotonic=4000000", true, false, false},
		// ConditionPathExists=/usr/local/HMLGW not met at boot
		{"hmlangw", "inactive", "dead", "UnitFileState=enabled\nConditionResult=no\nConditionTimestampMonotonic=4000000", false, false, true},
		// ExecCondition= said no; the unit's own conditions were met
		{"hs485d", "inactive", "dead", "UnitFileState=enabled\nResult=exec-condition\nConditionResult=yes\nConditionTimestampMonotonic=4000000", false, false, true},
		// never started: systemd answers ConditionResult=no without a check on record
		{"emergency", "inactive", "dead", "UnitFileState=static\nConditionResult=no\nConditionTimestampMonotonic=0", false, false, false},
		// a timer's service between two runs
		{"occu-cron-backup", "inactive", "dead", "UnitFileState=static\nType=oneshot\nResult=success\nConditionResult=yes\nConditionTimestampMonotonic=4000000", false, false, false},
		// stopped by hand
		{"lighttpd", "inactive", "dead", "UnitFileState=enabled\nResult=success\nConditionResult=yes\nConditionTimestampMonotonic=4000000", false, false, false},
		{"occu-interface-clock", "failed", "failed", "UnitFileState=enabled\nResult=exit-code\nConditionResult=yes\nConditionTimestampMonotonic=4000000", false, true, false},
		{"occu-oneshot", "failed", "failed", "Type=oneshot\nResult=exit-code\nConditionResult=yes\nConditionTimestampMonotonic=4000000", false, true, false},
		// failed before, and a later start was skipped: still failed
		{"occu-flaky", "failed", "failed", "Result=exit-code\nConditionResult=no\nConditionTimestampMonotonic=4000000", false, true, false},
		// a systemd that answers neither property
		{"old", "inactive", "dead", "UnitFileState=disabled", false, false, false},
	}
	var rows []string
	blocks := map[string]string{}
	for _, c := range cases {
		unit := c.unit + ".service"
		rows = append(rows, fmt.Sprintf(`{"unit":%q,"load":"loaded","active":%q,"sub":%q,"description":"x"}`, unit, c.active, c.sub))
		blocks[unit] = "Id=" + unit + "\n" + c.props + "\n"
	}
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch args[0] {
		case "list-units":
			return []byte("[" + strings.Join(rows, ",") + "]"), nil
		case "show":
			var out []string
			for i, a := range args {
				if a != "--" {
					continue
				}
				for _, unit := range args[i+1:] {
					out = append(out, blocks[unit])
				}
			}
			return []byte(strings.Join(out, "\n")), nil
		}
		return nil, errors.New("unexpected " + args[0])
	}
	list, err := SystemdServices{Root: Root(t.TempDir()), Run: run}.List()
	if err != nil {
		t.Fatal(err)
	}
	// still list-units and one show for all units (B-60), the two properties part of it
	if len(calls) != 2 || !strings.Contains(calls[1], ",ConditionResult,ConditionTimestampMonotonic -- ") {
		t.Errorf("calls: %v", calls)
	}
	by := map[string]Service{}
	for _, sv := range list {
		by[sv.ID] = sv
	}
	for _, c := range cases {
		t.Run(c.unit, func(t *testing.T) {
			sv, ok := by[c.unit]
			if !ok {
				t.Fatalf("not listed: %+v", list)
			}
			if sv.Running != c.running || sv.Failed != c.failed || sv.Skipped != c.skipped {
				t.Errorf("running/failed/skipped = %v/%v/%v, want %v/%v/%v", sv.Running, sv.Failed, sv.Skipped, c.running, c.failed, c.skipped)
			}
			raw, _ := json.Marshal(sv)
			if strings.Contains(string(raw), `"failed":true`) != c.failed || strings.Contains(string(raw), `"skipped":true`) != c.skipped {
				t.Errorf("json: %s", raw)
			}
		})
	}
}

func fakeSystemctl(t *testing.T, calls *[]string) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		switch {
		case name == "systemctl" && args[0] == "list-units":
			return []byte(`[{"unit":"rfd.service","load":"loaded","active":"active","sub":"running","description":"BidCos-RF Interface"},
{"unit":"addon-mosquitto.service","load":"loaded","active":"active","sub":"exited","description":"Addon mosquitto"},
{"unit":"addon-hmm.service","load":"loaded","active":"inactive","sub":"dead","description":"Addon hmm"},
{"unit":"addon-jp.service","load":"loaded","active":"active","sub":"exited","description":"Addon jp"},
{"unit":"addon-ghost.service","load":"not-found","active":"failed","sub":"failed","description":"addon-ghost.service"},
{"unit":"hs485d.service","load":"loaded","active":"inactive","sub":"dead","description":"hs485d"},
{"unit":"systemd-journald.service","load":"loaded","active":"active","sub":"running","description":"journald"},
{"unit":"redmatic.service","load":"loaded","active":"active","sub":"running","description":"RedMatic"}]`), nil
		case name == "systemctl" && args[0] == "show":
			// B-60: one call for every unit, blank-line separated blocks in argument order
			var blocks []string
			sep := 0
			for i, a := range args {
				if a == "--" {
					sep = i + 1
				}
			}
			for _, unit := range args[sep:] {
				switch unit {
				case "rfd.service":
					// the wall-clock stamp is from before the clock was set (B-61); the monotonic one counts
					blocks = append(blocks, "Id=rfd.service\nMainPID=688\nUnitFileState=enabled\nFragmentPath=/usr/lib/systemd/system/rfd.service\nSourcePath=\nMemoryCurrent=12345678\nCPUUsageNSec=2500000000\nActiveEnterTimestamp=@1773000000\nActiveEnterTimestampMonotonic=1000000000\n")
				case "hs485d.service":
					blocks = append(blocks, "Id=hs485d.service\nMainPID=0\nUnitFileState=disabled\nFragmentPath=/usr/lib/systemd/system/hs485d.service\nSourcePath=\n")
				case "redmatic.service":
					blocks = append(blocks, "Id=redmatic.service\nMainPID=900\nUnitFileState=enabled\nFragmentPath=/usr/local/addons/redmatic/etc/systemd/redmatic.service\nSourcePath=\nActiveEnterTimestamp=@1788693933\n")
				case "addon-hmm.service", "addon-jp.service":
					blocks = append(blocks, "Id="+unit+"\nMainPID=0\nUnitFileState=generated\nFragmentPath=/run/systemd/generator.early/"+unit+"\nSourcePath=/usr/local/etc/config/rc.d/x\nType=oneshot\nResult=success\nTasksCurrent=[not set]\n")
				default:
					blocks = append(blocks, "Id="+unit+"\nMainPID=0\nUnitFileState=generated\nFragmentPath=/run/systemd/generator.early/addon-mosquitto.service\nSourcePath=/usr/local/etc/config/rc.d/mosquitto\n")
				}
			}
			return []byte(strings.Join(blocks, "\n")), nil
		case name == "systemctl" && args[0] == "list-timers":
			// systemd 257 shape: "left" repeats the absolute "next"
			return []byte(`[{"next":1789000000000000,"left":1789000000000000,"last":1788900000000000,"passed":0,"unit":"occulite-firmware.timer","activates":"occulite-firmware.service"},
{"next":null,"left":null,"last":null,"passed":null,"unit":"idle.timer","activates":"idle.service"}]`), nil
		case name == "systemctl":
			return []byte("ok"), nil
		case name == "journalctl":
			return []byte(`{"__CURSOR":"s=1;i=2","__REALTIME_TIMESTAMP":"1788673245000000","_HOSTNAME":"lite","PRIORITY":"6","SYSLOG_IDENTIFIER":"occulited","_PID":"1053","_SYSTEMD_UNIT":"occulited.service","MESSAGE":"occulited started"}
{"__CURSOR":"s=1;i=3","__REALTIME_TIMESTAMP":"1788673246000000","_HOSTNAME":"lite","PRIORITY":"4","_COMM":"rfd","_PID":"688","_SYSTEMD_UNIT":"rfd.service","MESSAGE":["b","i","n"]}
not json at all
`), nil
		}
		return nil, nil
	}
}

func TestSystemdServices(t *testing.T) {
	var calls []string
	// a fake /proc: hmm's node runs (started outside its unit), nothing of jp does
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "proc/4242"), 0o755)
	os.WriteFile(filepath.Join(root, "proc/4242/cmdline"), []byte("node\x00/usr/local/addons/hmm/lib/server.js\x00"), 0o644)
	os.MkdirAll(filepath.Join(root, "proc/1"), 0o755)
	os.WriteFile(filepath.Join(root, "proc/1/cmdline"), []byte("/sbin/init\x00"), 0o644)
	os.WriteFile(filepath.Join(root, "proc/uptime"), []byte("5000.00 19000.00\n"), 0o644)
	s := SystemdServices{Root: Root(root), Run: fakeSystemctl(t, &calls), Now: func() time.Time { return time.UnixMicro(1789000000000000 - 3600*1e6) }}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	// B-60: list-units and one show for all units, not one per unit
	if len(calls) != 2 || !strings.HasPrefix(calls[1], "systemctl show --timestamp=unix -p Id,MainPID,") || !strings.HasSuffix(calls[1], "-- rfd.service addon-mosquitto.service addon-hmm.service addon-jp.service hs485d.service redmatic.service") {
		t.Errorf("calls: %v", calls)
	}
	if len(list) != 6 {
		t.Fatalf("%+v", list)
	}
	by := map[string]Service{}
	for _, sv := range list {
		by[sv.ID] = sv
	}
	if r := by["rfd"]; !r.Running || r.PID != 688 || !r.Managed || r.Protocol != "BidCos-RF" || !r.Enabled || r.Kind != "system" {
		t.Errorf("rfd: %+v", r)
	}
	// B-61: since = now - (uptime 5000 s - 1000 s monotonic) = now - 4000 s, not the pre-NTP wall stamp
	if r := by["rfd"]; r.Description != "BidCos-RF Interface" || r.MemoryBytes != 12345678 || r.CPUSeconds != 2.5 || r.Since != time.Unix(1788996400-4000, 0).Format(time.RFC3339) {
		t.Errorf("rfd details: %+v", r)
	}
	// no monotonic stamp: the wall-clock one is still used
	if r := by["redmatic"]; r.Since != time.Unix(1788693933, 0).Format(time.RFC3339) {
		t.Errorf("redmatic since: %+v", r)
	}
	if h := by["hs485d"]; h.MemoryBytes != 0 || h.Since != "" {
		t.Errorf("stopped unit carries resource use: %+v", h)
	}
	// 30.1 and the maintainer's two cases of 2026-09-09: a one-shot that ran and ended is
	// Completed; an addon whose daemon runs outside its dead unit is Running, and stray
	if j := by["addon-jp"]; !j.OneShot || j.Result != "success" || j.Stray {
		t.Errorf("one-shot addon: %+v", j)
	}
	if h := by["addon-hmm"]; !h.Running || !h.Stray || h.OneShot {
		t.Errorf("stray daemon: %+v", h)
	}
	if h := by["hs485d"]; h.Running || h.Enabled || !h.Managed {
		t.Errorf("hs485d: %+v", h)
	}
	if a := by["addon-mosquitto"]; a.Kind != "addon" || !a.Managed || !a.Running {
		t.Errorf("generated addon unit: %+v", a)
	}
	if r := by["redmatic"]; r.Kind != "addon" || !r.Managed || r.PID != 900 {
		t.Errorf("addon-shipped unit: %+v", r)
	}
	if list[0].Kind != "addon" {
		t.Errorf("addons first: %+v", list[0])
	}
	if _, err := s.Control(context.Background(), "rfd", "reboot"); err == nil {
		t.Error("bad action accepted")
	}
	if _, err := s.Control(context.Background(), "../x", "start"); err == nil {
		t.Error("bad id accepted")
	}
	calls = nil
	// B-26: disable is a runtime mask now, never `systemctl disable`, which cannot work on a
	// read-only rootfs
	if _, err := s.Control(context.Background(), "addon-mosquitto", "disable"); err != nil || calls[0] != "systemctl mask --runtime --now --no-pager -- addon-mosquitto.service" {
		t.Errorf("%v %v", err, calls)
	}
	tm, err := s.Timers(context.Background())
	if err != nil || len(tm) != 2 || tm[0].Unit != "occulite-firmware.timer" || !tm[0].Active || tm[0].Left != "1h0m0s" || tm[0].Next == "" {
		t.Errorf("%v %+v", err, tm)
	}
	if tm[1].Active || tm[1].Next != "" || tm[1].Left != "" {
		t.Errorf("idle timer: %+v", tm[1])
	}
}

func TestJournalLog(t *testing.T) {
	var calls []string
	j := JournalLog{Run: fakeSystemctl(t, &calls)}
	lines, err := j.Read(LogQuery{Unit: "rfd", Severity: "warning", Since: "1h", Contains: "x", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if calls[0] != "journalctl -o json --no-pager -q -n 10 -u rfd.service -p 4 --since 1h -g x --case-sensitive=false" {
		t.Errorf("%v", calls)
	}
	if len(lines) != 2 {
		t.Fatalf("%+v", lines)
	}
	// task 31: a unit and a tag both go to journalctl (it ANDs them), and a range has two ends
	if _, err := j.Read(LogQuery{Unit: "rfd", Tag: "S61rfd.script", Since: "@1757350800", Until: "@1757354400", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if calls[1] != "journalctl -o json --no-pager -q -n 5 -u rfd.service -t S61rfd.script --since @1757350800 --until @1757354400" {
		t.Errorf("%v", calls[1])
	}
	if lines[0].Tag != "occulited" || lines[0].Severity != "info" || lines[0].PID != 1053 || lines[0].Unit != "occulited" || lines[0].Cursor != "s=1;i=2" || lines[0].Message != "occulited started" || lines[0].Timestamp == "" {
		t.Errorf("%+v", lines[0])
	}
	if lines[1].Tag != "rfd" || lines[1].Severity != "warning" || lines[1].Message != "" {
		t.Errorf("binary message and _COMM fallback: %+v", lines[1])
	}
}

func TestHasSystemd(t *testing.T) {
	r := rootWith(t, map[string]string{"etc/x": ""})
	if r.HasSystemd() {
		t.Error("no /run/systemd/system yet")
	}
	r2 := rootWith(t, map[string]string{"run/systemd/system/.keep": ""})
	if !r2.HasSystemd() {
		t.Error("should detect systemd")
	}
}

// B-26: the Services page's persistent switch. Unit enablement lives on the read-only rootfs, so
// off is a runtime mask, on is a runtime unmask (plus a runtime enable when the image does not
// ship the unit enabled), and the set is kept on the userfs and replayed at every start - /run is
// empty after a boot.
func TestUnitSwitch(t *testing.T) {
	var calls []string
	states := map[string]string{"rfd.service": "enabled", "ssdpd.service": "disabled"}
	failing := ""
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		unit := args[len(args)-1]
		if failing != "" && args[0] == failing {
			return []byte("Failed to " + failing + " unit: Read-only file system"), errors.New("exit status 1")
		}
		if args[0] == "is-enabled" {
			st := states[unit]
			if st == "disabled" {
				return []byte("disabled\n"), errors.New("exit status 1") // systemctl's own answer
			}
			return []byte(st + "\n"), nil
		}
		return []byte("ok"), nil
	}
	file := filepath.Join(t.TempDir(), "unit-switch.json")
	s := SystemdServices{Run: run, SwitchFile: file}

	// off: a runtime mask, and the unit is remembered
	if _, err := s.Control(context.Background(), "rfd", "disable"); err != nil {
		t.Fatal(err)
	}
	if calls[0] != "systemctl mask --runtime --now --no-pager -- rfd.service" {
		t.Errorf("%v", calls)
	}
	if got := s.readSwitch(); len(got.Masked) != 1 || got.Masked[0] != "rfd.service" || len(got.Enabled) != 0 {
		t.Errorf("%+v", got)
	}
	// the file is the persistence, so it has to be readable as what it says
	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), `"rfd.service"`) {
		t.Errorf("%v %s", err, b)
	}

	// on again: unmask, and no runtime enable for a unit the image ships enabled
	calls = nil
	if _, err := s.Control(context.Background(), "rfd", "enable"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl unmask --runtime --no-pager -- rfd.service",
		"systemctl is-enabled -- rfd.service",
		"systemctl start --no-pager -- rfd.service",
	}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Errorf("%v", calls)
	}
	if got := s.readSwitch(); len(got.Masked) != 0 || len(got.Enabled) != 0 {
		t.Errorf("%+v", got)
	}

	// on for a unit the image does not ship enabled: a runtime enable, and it is remembered so
	// the next boot has it too
	calls = nil
	if _, err := s.Control(context.Background(), "ssdpd", "enable"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[2] != "systemctl enable --runtime --no-pager -- ssdpd.service" {
		t.Errorf("%v", calls)
	}
	if got := s.readSwitch(); len(got.Enabled) != 1 || got.Enabled[0] != "ssdpd.service" {
		t.Errorf("%+v", got)
	}
	// and switching it off again takes it out of both halves
	if _, err := s.Control(context.Background(), "ssdpd", "disable"); err != nil {
		t.Fatal(err)
	}
	if got := s.readSwitch(); len(got.Enabled) != 0 || len(got.Masked) != 1 || got.Masked[0] != "ssdpd.service" {
		t.Errorf("%+v", got)
	}

	// a systemctl that fails leaves the stored set alone: the file says what the box does
	failing = "mask"
	if _, err := s.Control(context.Background(), "rfd", "disable"); err == nil {
		t.Error("a failed mask was reported as success")
	}
	if got := s.readSwitch(); len(got.Masked) != 1 || got.Masked[0] != "ssdpd.service" {
		t.Errorf("a failed mask was stored: %+v", got)
	}
	failing = ""

	// the replay: what makes the switch persistent
	if err := os.WriteFile(file, []byte(`{"masked":["ssdpd.service","occulited.service","not a unit"],"enabled":["hmlangw.service"]}`), 0o640); err != nil {
		t.Fatal(err)
	}
	calls = nil
	applied, problems := s.ReplayUnitSwitch(context.Background())
	if len(applied) != 2 || applied[0] != "masked ssdpd.service" || applied[1] != "enabled hmlangw.service" {
		t.Errorf("applied %v", applied)
	}
	if len(problems) != 2 {
		t.Errorf("problems %v", problems)
	}
	// occulited is never masked at start: the daemon would stop itself at every boot
	for _, c := range calls {
		if strings.Contains(c, "occulited") {
			t.Errorf("occulited was masked by the replay: %v", calls)
		}
	}
	if calls[0] != "systemctl mask --runtime --now --no-pager -- ssdpd.service" || calls[1] != "systemctl enable --runtime --now --no-pager -- hmlangw.service" {
		t.Errorf("%v", calls)
	}

	// no file, no switch: a box where nobody touched one replays nothing and says nothing
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if a, p := s.ReplayUnitSwitch(context.Background()); len(a) != 0 || len(p) != 0 {
		t.Errorf("%v %v", a, p)
	}
	// and with no file configured at all (development) the switch still switches
	calls = nil
	plain := SystemdServices{Run: run}
	if _, err := plain.Control(context.Background(), "rfd", "disable"); err != nil || len(calls) != 1 {
		t.Errorf("%v %v", err, calls)
	}
}

// 28.3: a follow with no history must say -n 0, or journalctl -f replays its default ten lines
// on top of what the page loaded; a plain read without a limit says nothing about -n.
func TestJournalArgsFollowNoHistory(t *testing.T) {
	j := JournalLog{}
	got := strings.Join(j.args(LogQuery{Follow: true, Limit: 0, Unit: "rfd"}), " ")
	if !strings.Contains(" "+got+" ", " -n 0 ") {
		t.Errorf("follow without history: %q", got)
	}
	if got := " " + strings.Join(j.args(LogQuery{Limit: 0}), " ") + " "; strings.Contains(got, " -n ") {
		t.Errorf("a read without a limit must not pass -n: %q", got)
	}
	if got := strings.Join(j.args(LogQuery{Follow: true, Limit: 50}), " "); !strings.Contains(got, "-n 50") {
		t.Errorf("follow with history: %q", got)
	}
}

// journalctl -g exits 1 when its expression matches nothing: an empty answer, not a failure
// (the Log page's text filter, task 201's decline grep).
func TestJournalTextFilterWithoutAMatch(t *testing.T) {
	// a status of 1 with nothing written, with a text filter: no lines, no error
	fail := func(code int, out string) Runner {
		return func(context.Context, string, ...string) ([]byte, error) {
			b, err := exec.Command("sh", "-c", fmt.Sprintf("printf %%s %s; exit %d", shellQuote(out), code)).Output()
			return b, err
		}
	}
	j := JournalLog{Run: fail(1, "")}
	lines, err := j.Read(LogQuery{Contains: "nothing like this"})
	if err != nil || len(lines) != 0 {
		t.Fatalf("no match: %v %v", lines, err)
	}
	// without a text filter the same status is a failure
	if _, err := j.Read(LogQuery{Unit: "rfd"}); err == nil {
		t.Error("a failure without -g must stay a failure")
	}
	// a worse status, or output beside it, stays a failure too
	if _, err := (JournalLog{Run: fail(2, "")}).Read(LogQuery{Contains: "x"}); err == nil {
		t.Error("exit 2 must stay a failure")
	}
	if _, err := (JournalLog{Run: fail(1, "{}\n")}).Read(LogQuery{Contains: "x"}); err == nil {
		t.Error("a status of 1 that wrote lines must stay a failure")
	}
}
