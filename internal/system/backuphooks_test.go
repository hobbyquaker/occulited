package system

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// fakeHookRunner records the steps and answers what the test says.
type fakeHookRunner struct {
	calls   []string
	results map[string]priv.BackupHookResult
	errs    map[string]error
}

func (f *fakeHookRunner) BackupHook(_ context.Context, id, step string) (priv.BackupHookResult, error) {
	f.calls = append(f.calls, id+":"+step)
	if err := f.errs[id]; err != nil && step == priv.BackupHookSnapshot {
		return priv.BackupHookResult{Step: step}, err
	}
	r := f.results[id]
	r.Step = step
	return r, nil
}

func hooksRoot(t *testing.T) Root {
	t.Helper()
	dir := t.TempDir()
	r := Root(dir)
	if err := os.MkdirAll(r.join(AddonPolicyDir), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(r.join(AddonPolicyDir+"/"+name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("battery.json", `{"id": "battery", "mode": "confined", "uid": 30007}`)
	write("battery"+AddonManifestSuffix, `{"format": 1, "id": "battery", "name": {"de": "Batterien", "en": "Batteries"}, "backup": {"hook": "bin/backup-snapshot", "staging": "var/backup", "size_hint_mb": 20, "restore": "bin/backup-restore"}}`)
	write("history.json", `{"id": "history", "mode": "confined", "uid": 30008}`)
	write("history"+AddonManifestSuffix, `{"format": 1, "id": "history", "name": "History", "backup": {"hook": "bin/snap", "staging": "var/backup", "timeout": 600, "size_hint_mb": 500}}`)
	write("mosquitto.json", `{"id": "mosquitto", "mode": "confined", "uid": 30009}`)
	write("mosquitto"+AddonManifestSuffix, `{"format": 1, "id": "mosquitto", "name": "Mosquitto"}`)
	return r
}

// openccu-lite task 399: the declared addons with their defaults (small on, large off), the user's
// switch, the run's results kept, failures logged and kept but never stopping the backup.
func TestBackupHooks(t *testing.T) {
	r := hooksRoot(t)
	runner := &fakeHookRunner{results: map[string]priv.BackupHookResult{
		"battery": {Exit: 0, Bytes: 4096, Files: 1, DurationMS: 120, Output: "ok"},
		"history": {Exit: 1, Output: "disk full"},
	}}
	now := time.Date(2026, 11, 11, 22, 0, 0, 0, time.UTC)
	h := &BackupHooks{Root: r, Runner: runner, StateFile: filepath.Join(string(r), "backup-addons.json"), UID: -1, GID: -1, Now: func() time.Time { return now }}

	list := h.Declared()
	if len(list) != 2 || list[0].ID != "battery" || list[1].ID != "history" {
		t.Fatalf("declared: %+v", list)
	}
	if b := list[0]; !b.Enabled || b.Large || b.Chosen != nil || b.Timeout != 120 || b.SizeHintMB != 20 || b.Restore != "bin/backup-restore" || b.Name.In("de") != "Batterien" || b.Last != nil {
		t.Errorf("battery: %+v", b)
	}
	if hst := list[1]; hst.Enabled || !hst.Large || hst.Timeout != 600 {
		t.Errorf("history: %+v", hst)
	}

	// the run: battery alone (history is off by default); its result kept
	ran := h.Run(context.Background())
	if len(ran) != 1 || ran[0].ID != "battery" || !ran[0].OK || ran[0].Bytes != 4096 || !ran[0].At.Equal(now) {
		t.Fatalf("ran: %+v", ran)
	}
	if strings.Join(runner.calls, ",") != "battery:snapshot" {
		t.Fatalf("calls: %v", runner.calls)
	}
	h.Clean(context.Background(), ran)
	if strings.Join(runner.calls, ",") != "battery:snapshot,battery:clean" {
		t.Fatalf("calls after clean: %v", runner.calls)
	}
	if last := h.Declared()[0].Last; last == nil || !last.OK || last.Bytes != 4096 {
		t.Fatalf("battery's last: %+v", last)
	}
	if h.Failed() != nil {
		t.Fatal("a failure listed")
	}

	// the switch: history on, battery off; the choice is kept and shown
	if err := h.SetEnabled("history", true); err != nil {
		t.Fatal(err)
	}
	if err := h.SetEnabled("battery", false); err != nil {
		t.Fatal(err)
	}
	if err := h.SetEnabled("mosquitto", true); !errors.Is(err, ErrNoBackupBlock) {
		t.Fatalf("an addon without the block: %v", err)
	}
	list = h.Declared()
	if list[0].Enabled || list[0].Chosen == nil || *list[0].Chosen || !list[1].Enabled || list[1].Chosen == nil || !*list[1].Chosen {
		t.Fatalf("switched: %+v", list)
	}
	var st backupHookState
	b, _ := os.ReadFile(h.StateFile)
	if json.Unmarshal(b, &st) != nil || strings.Join(st.On, ",") != "history" || strings.Join(st.Off, ",") != "battery" {
		t.Fatalf("state file: %s", b)
	}

	// history's hook fails: the run goes on, the result says so, the warning lists it
	runner.calls = nil
	ran = h.Run(context.Background())
	if len(ran) != 1 || ran[0].ID != "history" || ran[0].OK || ran[0].Exit != 1 || !strings.Contains(ran[0].Error, "exited 1") || ran[0].Output != "disk full" {
		t.Fatalf("history's run: %+v", ran)
	}
	h.Clean(context.Background(), ran)
	if strings.Join(runner.calls, ",") != "history:snapshot,history:clean" {
		t.Fatalf("calls: %v", runner.calls)
	}
	if failed := h.Failed(); len(failed) != 1 || failed[0].ID != "history" {
		t.Fatalf("failed: %+v", failed)
	}
	// the helper refuses (an error, not an exit): kept as the error
	runner.errs = map[string]error{"history": errors.New("the hook of history, bin/snap, is not an executable file")}
	ran = h.Run(context.Background())
	if len(ran) != 1 || ran[0].OK || !strings.Contains(ran[0].Error, "not an executable file") {
		t.Fatalf("a refused hook: %+v", ran)
	}
	// a timed-out hook
	runner.errs = nil
	runner.results["history"] = priv.BackupHookResult{Exit: 143, TimedOut: true}
	if ran = h.Run(context.Background()); len(ran) != 1 || ran[0].OK || !ran[0].TimedOut || !strings.Contains(ran[0].Error, "600 s") {
		t.Fatalf("a timed-out hook: %+v", ran)
	}
	// switching battery on again clears its choice? No: the choice stands as made
	if err := h.SetEnabled("battery", true); err != nil {
		t.Fatal(err)
	}
	if list = h.Declared(); !list[0].Enabled || list[0].Chosen == nil || !*list[0].Chosen {
		t.Fatalf("battery on again: %+v", list[0])
	}

	// without a runner: nothing runs, the list stays
	none := &BackupHooks{Root: r, StateFile: h.StateFile, UID: -1, GID: -1}
	if none.Supported() || none.Run(context.Background()) != nil || len(none.Declared()) != 2 {
		t.Fatal("without a runner")
	}
	// CreateBackupWithHooks: the hooks around the script, in order
	runner.calls = nil
	var order []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		order = append(order, "script")
		path := args[0]
		return nil, os.WriteFile(path, []byte("sbk"), 0o644)
	}
	if err := os.MkdirAll(r.join(BackupDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(r.join("/proc/sys/kernel"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, ran, err := r.CreateBackupWithHooks(context.Background(), run, h)
	if err != nil || path == "" || len(ran) != 2 {
		t.Fatalf("with hooks: %q %+v %v", path, ran, err)
	}
	if got := strings.Join(append(runner.calls[:2], append([]string{order[0]}, runner.calls[2:]...)...), ","); got != "battery:snapshot,history:snapshot,script,battery:clean,history:clean" {
		t.Fatalf("order: %s", got)
	}
	// a nil hooks: the plain backup
	if _, ran, err := r.CreateBackupWithHooks(context.Background(), run, nil); err != nil || ran != nil {
		t.Fatalf("without hooks: %+v %v", ran, err)
	}
}
