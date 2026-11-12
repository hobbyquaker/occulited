package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// task 85: "Copy now" restarts the ram-sync copy unit, and only in ram-sync; the journal's two
// Status warnings.

const journalMounted = "40 30 179:3 /var/log/journal /var/log/journal rw,relatime shared:9 - ext4 /dev/mmcblk0p3 rw\n"

func putRootFile(t *testing.T, r system.Root, p, body string) {
	t.Helper()
	full := filepath.Join(string(r), p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestJournalSyncNow(t *testing.T) {
	r := fakeRoot(t)
	putRootFile(t, r, "etc/config/journal", "STORAGE=ram-sync\n")
	svc := &fakeServices{}
	run := func(context.Context, string, ...string) ([]byte, error) { return []byte("32.4M\n"), nil }
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Journal: &system.JournalLog{Run: run}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// ram-sync chosen, but nothing mounted yet: no copy to make
	st, out, _ := do(t, srv, "POST", "/api/system/v1/journal/sync", "", nil)
	if st != 409 || out["error"] != "not-ram-sync" || len(svc.calls) != 0 {
		t.Fatalf("not in effect: %d %v %v", st, out, svc.calls)
	}

	// in effect: the unit is restarted - its stop is the copy - and the view answers
	putRootFile(t, r, "proc/self/mountinfo", journalMounted)
	putRootFile(t, r, "run/log/journal/0123abcd/system.journal", "x")
	putRootFile(t, r, "run/occu-journal/sync.state", "LAST_SYNC=1789236000\nLAST_RESULT=ok\nLAST_COPIED=3\nLAST_ERROR=\n")
	putRootFile(t, r, "run/occu-journal/sync.next", "1789257600\n")
	st, out, _ = do(t, srv, "POST", "/api/system/v1/journal/sync", "", nil)
	if st != 200 || fmt.Sprint(svc.calls) != "[occu-journal-sync restart]" {
		t.Fatalf("copy now: %d %v %v", st, out, svc.calls)
	}
	if out["effective"] != "ram-sync" || out["last_sync_result"] != "ok" || out["last_sync_copied"] != 3.0 || out["last_sync"] != "2026-09-12T18:00:00Z" || out["next_sync"] != "2026-09-13T00:00:00Z" || out["usage"] != "32.4M" {
		t.Errorf("answer %v", out)
	}

	// switched away (RAM from the next boot): no copy
	svc.calls = nil
	putRootFile(t, r, "etc/config/journal", "STORAGE=ram\n")
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/journal/sync", "", nil); st != 409 || len(svc.calls) != 0 {
		t.Errorf("switched away: %d %v %v", st, out, svc.calls)
	}
	// persistent now: no copy either
	putRootFile(t, r, "etc/config/journal", "STORAGE=ram-sync\n")
	_ = os.RemoveAll(filepath.Join(string(r), "run/log/journal"))
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/journal/sync", "", nil); st != 409 || len(svc.calls) != 0 {
		t.Errorf("persistent now: %d %v", st, svc.calls)
	}

	// no journald, or no service manager: 501
	mux2 := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc}).Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	if st, out, _ := do(t, srv2, "POST", "/api/system/v1/journal/sync", "", nil); st != 501 || out["error"] != "not-systemd" {
		t.Errorf("no journal: %d %v", st, out)
	}
}

func TestJournalWarnings(t *testing.T) {
	r := fakeRoot(t) // ova: persistent by default
	a := &SystemAPI{Root: r, Journal: &system.JournalLog{}}
	ctx := context.Background()
	ids := func() string {
		list, ok := a.journalWarnings(ctx)
		if !ok {
			t.Fatal("not ok")
		}
		s := ""
		for _, w := range list {
			s += fmt.Sprintf("%s/%s/%s %v;", w.ID, w.Variant, w.Severity, w.Params["reason"])
		}
		return s
	}
	// nothing mounted and no reason: the boot script has not run yet, which is no warning
	if got := ids(); got != "" {
		t.Errorf("before the script: %q", got)
	}
	// the script tried and left the journal in RAM
	putRootFile(t, r, "run/occu-journal/fallback", "the userfs target cannot be mounted, the journal stays in RAM (the product default)\n")
	if got := ids(); got != "journal-target/persistent/warning the userfs target cannot be mounted, the journal stays in RAM (the product default);" {
		t.Errorf("fallback: %q", got)
	}
	list, _ := a.journalWarnings(ctx)
	if list[0].Href != "/log?settings=journal" || list[0].Params["path"] != "/usr/local/var/log/journal" {
		t.Errorf("fallback warning %+v", list[0])
	}
	// mounted now: gone, whatever the file says
	putRootFile(t, r, "proc/self/mountinfo", journalMounted)
	if got := ids(); got != "" {
		t.Errorf("mounted: %q", got)
	}
	// ram-sync in effect before its first copy of this boot (sync.state is on /run): whether a copy
	// fails is not known yet, so a silence of journal-sync is kept, not spent at every boot (B-107)
	putRootFile(t, r, "etc/config/journal", "STORAGE=ram-sync\n")
	putRootFile(t, r, "run/log/journal/0123abcd/system.journal", "x")
	if list, ok := a.journalWarnings(ctx); ok || len(list) != 0 {
		t.Errorf("before the first copy: %v %v", list, ok)
	}
	// ram-sync whose last copy failed; one that worked again
	putRootFile(t, r, "run/occu-journal/sync.state", "LAST_SYNC=1789236000\nLAST_RESULT=failed\nLAST_COPIED=0\nLAST_ERROR=the userfs target /usr/local/var/log/journal cannot be created\n")
	if got := ids(); got != "journal-sync/failed/warning the userfs target /usr/local/var/log/journal cannot be created;" {
		t.Errorf("failed copy: %q", got)
	}
	if list, _ := a.journalWarnings(ctx); list[0].Params["at"] != "2026-09-12T18:00:00Z" {
		t.Errorf("failed copy params %v", list[0].Params)
	}
	putRootFile(t, r, "run/occu-journal/sync.state", "LAST_SYNC=1789239600\nLAST_RESULT=ok\nLAST_COPIED=2\n")
	if got := ids(); got != "" {
		t.Errorf("copy ok again: %q", got)
	}
	// a failed copy after a switch to RAM is not ram-sync's warning any more
	putRootFile(t, r, "run/occu-journal/sync.state", "LAST_SYNC=1789239600\nLAST_RESULT=failed\n")
	putRootFile(t, r, "etc/config/journal", "STORAGE=ram\n")
	if got := ids(); got != "" {
		t.Errorf("switched to RAM: %q", got)
	}
	// busybox: no journal, no warning
	if list, ok := (&SystemAPI{Root: r}).journalWarnings(ctx); !ok || len(list) != 0 {
		t.Errorf("busybox: %v %v", list, ok)
	}
}

// task 216: with a USB stick as ram-sync's target, journal-target is the "usb" variant while the
// stick is not plugged in (its label a param), journal-sync names the stick, and Copy now answers
// 409 without it.
func TestJournalWarningsStick(t *testing.T) {
	r := fakeRoot(t)
	putRootFile(t, r, "etc/config/journal", "STORAGE=ram-sync\nTARGET=usb:LOGSTICK/journal\n")
	putRootFile(t, r, "run/log/journal/0123abcd/system.journal", "x")
	putRootFile(t, r, "proc/mounts", "/dev/sda1 /media/usb1 vfat rw 0 0\n")
	_ = os.MkdirAll(filepath.Join(string(r), "media/usb1"), 0o755)
	_ = os.MkdirAll(filepath.Join(string(r), "media/usb2"), 0o755)
	old := system.USBStickProps
	system.USBStickProps = func(_ system.Root, dev string) map[string]string {
		return map[string]map[string]string{"/dev/sda1": {"ID_FS_LABEL": "OTHERSTICK"}, "/dev/sdb1": {"ID_FS_LABEL": "LOGSTICK"}}[dev]
	}
	t.Cleanup(func() { system.USBStickProps = old })
	a := &SystemAPI{Root: r, Journal: &system.JournalLog{}}
	ctx := context.Background()

	putRootFile(t, r, "run/occu-journal/fallback", "the USB stick LOGSTICK is not plugged in, the journal stays in RAM until it is\n")
	list, ok := a.journalWarnings(ctx)
	if !ok || len(list) != 1 || list[0].ID != "journal-target" || list[0].Variant != "usb" || list[0].Params["label"] != "LOGSTICK" || list[0].Params["dir"] != "journal" || list[0].Params["path"] != "usb:LOGSTICK/journal" {
		t.Fatalf("stick missing: %v %+v", ok, list)
	}
	svc := &fakeServices{}
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Journal: &system.JournalLog{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/journal/sync", "", nil); st != 409 || !strings.Contains(fmt.Sprint(out["message"]), "USB stick LOGSTICK") || len(svc.calls) != 0 {
		t.Errorf("copy now without the stick: %d %v %v", st, out, svc.calls)
	}

	// plugged in: no warning until a copy failed; then journal-sync names the stick
	putRootFile(t, r, "proc/mounts", "/dev/sda1 /media/usb1 vfat rw 0 0\n/dev/sdb1 /media/usb2 vfat rw 0 0\n")
	putRootFile(t, r, "run/occu-journal/sync.state", "LAST_SYNC=1789236000\nLAST_RESULT=failed\nLAST_COPIED=0\nLAST_ERROR=the USB stick LOGSTICK is mounted read-only\nLAST_REASON=plug\n")
	list, _ = a.journalWarnings(ctx)
	if len(list) != 1 || list[0].ID != "journal-sync" || list[0].Params["target"] != "usb" || list[0].Params["label"] != "LOGSTICK" {
		t.Fatalf("failed copy to the stick: %+v", list)
	}
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/journal/sync", "", nil); st != 200 || len(svc.calls) != 1 {
		t.Errorf("copy now with the stick: %d %v", st, svc.calls)
	}
	st, out, _ := do(t, srv, "GET", "/api/system/v1/journal", "", nil)
	if st != 200 || out["effective"] != "ram-sync" || out["target_path"] != "/media/usb2/journal" || out["target_label"] != "LOGSTICK" || out["last_sync_reason"] != "plug" {
		t.Errorf("get with the stick: %d %v", st, out)
	}
	// persistent on a stick (a file written by hand; the API refuses it): the plain variant
	putRootFile(t, r, "etc/config/journal", "STORAGE=persistent\nTARGET=usb:LOGSTICK/journal\n")
	putRootFile(t, r, "run/occu-journal/fallback", "persistent on the USB stick LOGSTICK is not possible\n")
	if list, _ = a.journalWarnings(ctx); len(list) != 1 || list[0].Variant != "persistent" || list[0].Params["label"] != "LOGSTICK" {
		t.Errorf("persistent on a stick: %+v", list)
	}
}

// openccu-lite task 341: journal-ram is a notice while the journal is in RAM by the setting (the
// SD-card products' default): the logs are lost at a reboot. Not while a switch to RAM waits for the
// reboot, not beside journal-target, not in ram-sync or persistent. It names the first boot's copy
// after a switch while that file exists.
func TestJournalRAMNotice(t *testing.T) {
	r := fakeRoot(t)
	a := &SystemAPI{Root: r, Journal: &system.JournalLog{}}
	ctx := context.Background()
	putRootFile(t, r, "etc/config/journal", "STORAGE=ram\n")
	list, ok := a.journalWarnings(ctx)
	if !ok || len(list) != 1 || list[0].ID != "journal-ram" || list[0].Variant != "ram" || list[0].Severity != warnings.SeverityInfo || list[0].Href != "/log?settings=journal" {
		t.Fatalf("RAM by the setting: %v %+v", ok, list)
	}
	if _, has := list[0].Params["first_boot_journal"]; has {
		t.Errorf("no copy, but a param: %v", list[0].Params)
	}
	putRootFile(t, r, "usr/local/var/log/first-boot-journal.txt.gz", "12345")
	list, _ = a.journalWarnings(ctx)
	if len(list) != 1 || list[0].Params["first_boot_journal"] != FirstBootJournal || list[0].Params["first_boot_journal_size"] != int64(5) {
		t.Errorf("with the first boot's copy: %+v", list)
	}
	// a switch to RAM waiting for the reboot: the userfs still mounted
	putRootFile(t, r, "proc/self/mountinfo", journalMounted)
	if list, _ := a.journalWarnings(ctx); len(list) != 0 {
		t.Errorf("reboot pending: %+v", list)
	}
	// persistent wanted, the boot script fell back to RAM: journal-target alone
	_ = os.Remove(filepath.Join(string(r), "proc/self/mountinfo"))
	putRootFile(t, r, "etc/config/journal", "STORAGE=persistent\n")
	putRootFile(t, r, "run/occu-journal/fallback", "the userfs target cannot be mounted\n")
	if list, _ := a.journalWarnings(ctx); len(list) != 1 || list[0].ID != "journal-target" {
		t.Errorf("fallback: %+v", list)
	}
	// persistent and mounted: nothing
	_ = os.Remove(filepath.Join(string(r), "run/occu-journal/fallback"))
	putRootFile(t, r, "proc/self/mountinfo", journalMounted)
	if list, _ := a.journalWarnings(ctx); len(list) != 0 {
		t.Errorf("persistent: %+v", list)
	}
}
