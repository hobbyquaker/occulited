package httpapi

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/system"
)

// waitJob polls GET /backup/prepare/{id} until its state is want (a state, never a time, decides).
func waitJob(t *testing.T, g *cryptRig, id, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, out := g.do(t, "GET", "/backup/prepare/"+id, "")
		if st != 200 {
			t.Fatalf("job %s: %d %v", id, st, out)
		}
		if out["state"] == want {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s: state %v, want %s", id, out["state"], want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func backupDirEntries(g *cryptRig) []os.DirEntry {
	e, _ := os.ReadDir(string(g.root) + system.BackupDir)
	return e
}

// occulited B-90: the backup is prepared as a job, then downloaded from the first byte; one
// download takes the file, a second POST while one waits joins it.
func TestBackupPrepared(t *testing.T) {
	g := newCryptRig(t)
	st, out := g.do(t, "POST", "/backup/prepare", "")
	if st != 202 || out["state"] != "running" || len(out["id"].(string)) != 32 {
		t.Fatalf("prepare: %d %v", st, out)
	}
	id := out["id"].(string)
	ready := waitJob(t, g, id, "ready")
	if ready["size"] != float64(len(g.sbk)) {
		t.Errorf("size: %v", ready)
	}
	// a second prepare while the file waits joins it rather than building another one
	if st, again := g.do(t, "POST", "/backup/prepare", ""); st != 200 || again["id"] != id {
		t.Errorf("second prepare: %d %v", st, again)
	}
	if st, _ := g.do(t, "GET", "/backup/prepare/unknown", ""); st != 404 {
		t.Errorf("unknown job: %d", st)
	}
	if res := g.get(t, "/backup?prepared=unknown"); res.StatusCode != 404 {
		t.Errorf("unknown prepared: %d", res.StatusCode)
	}
	res := g.get(t, "/backup?prepared="+id)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != g.sbk || !strings.HasSuffix(res.Header.Get("Content-Disposition"), `.sbk"`) {
		t.Fatalf("download: %d %q", res.StatusCode, body)
	}
	if e := backupDirEntries(g); len(e) != 0 {
		t.Errorf("left behind: %v", e)
	}
	waitJob(t, g, id, "downloaded")
	if res := g.get(t, "/backup?prepared="+id); res.StatusCode != 410 {
		t.Errorf("second download: %d", res.StatusCode)
	}
	// a new prepare after the download builds a new file; with encryption on it goes out encrypted
	g.setup(t)
	_, out = g.do(t, "POST", "/backup/prepare", "")
	if out["id"] == id {
		t.Fatal("the downloaded job was joined")
	}
	waitJob(t, g, out["id"].(string), "ready")
	res = g.get(t, "/backup?prepared="+out["id"].(string))
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.HasPrefix(string(body), "age-encryption.org/v1\n") {
		t.Errorf("encrypted: %d %q", res.StatusCode, body[:min(30, len(body))])
	}
}

// A running backup is not downloadable yet; DELETE marks it cancelled at once, the script runs to
// its end (B-91: a stop half-way leaves its working directory) and its file is removed then - and
// no new backup starts until it has. A ready one nobody fetches is removed at its expiry, and
// DELETE removes it too.
func TestBackupPreparedCancelAndExpiry(t *testing.T) {
	g := newCryptRig(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	g.api.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "/bin/createBackup.sh") {
			started <- struct{}{}
			<-release
			return nil, os.WriteFile(args[0], []byte(g.sbk), 0o644)
		}
		return nil, nil
	}
	_, out := g.do(t, "POST", "/backup/prepare", "")
	id := out["id"].(string)
	<-started
	if res := g.get(t, "/backup?prepared="+id); res.StatusCode != 409 {
		t.Errorf("running: %d", res.StatusCode)
	}
	if st, _ := g.do(t, "DELETE", "/backup/prepare/"+id, ""); st != 204 {
		t.Errorf("cancel: %d", st)
	}
	if st, job := g.do(t, "GET", "/backup/prepare/"+id, ""); st != 200 || job["state"] != "failed" || job["error"] != "cancelled" {
		t.Errorf("cancelled at once: %d %v", st, job)
	}
	if st, busy := g.do(t, "POST", "/backup/prepare", ""); st != 409 || busy["error"] != "backup-busy" {
		t.Errorf("while the cancelled one finishes: %d %v", st, busy)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _ := g.do(t, "POST", "/backup/prepare", "")
		if st != 409 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cancelled backup never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// the cancelled one's file is gone; the new one (a release is closed: it finishes at once) is ready
	_, out = g.do(t, "POST", "/backup/prepare", "")
	waitJob(t, g, out["id"].(string), "ready")
	if e := backupDirEntries(g); len(e) != 1 {
		t.Errorf("want the new backup's file only: %v", e)
	}
	if st, _ := g.do(t, "DELETE", "/backup/prepare/"+out["id"].(string), ""); st != 204 {
		t.Errorf("cancel ready: %d", st)
	}
	if st, _ := g.do(t, "DELETE", "/backup/prepare/nope", ""); st != 404 {
		t.Errorf("cancel unknown: %d", st)
	}

	// a ready file removed by DELETE
	g.api.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return nil, os.WriteFile(args[0], []byte(g.sbk), 0o644)
	}
	_, out = g.do(t, "POST", "/backup/prepare", "")
	id = out["id"].(string)
	waitJob(t, g, id, "ready")
	if st, _ := g.do(t, "DELETE", "/backup/prepare/"+id, ""); st != 204 {
		t.Errorf("cancel ready: %d", st)
	}
	waitJob(t, g, id, "failed")
	if e := backupDirEntries(g); len(e) != 0 {
		t.Errorf("left behind after cancel: %v", e)
	}

	// a ready file nobody fetches expires
	g.api.jobs().mu.Lock()
	g.api.jobs().expire = time.Millisecond
	g.api.jobs().mu.Unlock()
	_, out = g.do(t, "POST", "/backup/prepare", "")
	id = out["id"].(string)
	if expired := waitJob(t, g, id, "failed"); expired["error"] != "expired" {
		t.Errorf("expiry: %v", expired)
	}
	if e := backupDirEntries(g); len(e) != 0 {
		t.Errorf("left behind after expiry: %v", e)
	}
}
