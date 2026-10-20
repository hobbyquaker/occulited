package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/system"
)

// A backup prepared before its download (occulited B-90). GET /backup builds the .sbk first and
// writes nothing until it is complete - 7.5 minutes for a 2.5 GB /usr/local on a Pi 3. Over
// HTTP/2 lighttpd ends a stream after server.max-write-idle (360 s on openccu-lite) without a
// write on the connection, and that limit cannot be raised for one URL: HTTP/2 checks it against
// the connection's configuration, not the request's. So the page prepares the backup as a job of
// its own (POST /backup/prepare), polls it, and downloads the finished file with GET
// /backup?prepared=<id>, which streams from the first byte. HTTP/1.1 has no such limit before the
// response starts, so GET /backup alone still serves curl --http1.1 and older clients.

// BackupJob is one prepared backup as GET /backup/prepare/{id} answers it.
type BackupJob struct {
	ID string `json:"id"`
	// State is "running", "ready" (the file waits for its download), "downloaded" (handed to its
	// one download, which removes it) or "failed" (Error: why, "cancelled" or "expired" included)
	State    string     `json:"state"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Size     int64      `json:"size,omitempty"`
	Error    string     `json:"error,omitempty"`
	path     string
	cancel   context.CancelFunc
	// busy: createBackup.sh still runs - also after a DELETE, which only marks the job; the script
	// ends on its own and its file is removed then (B-91)
	busy bool
}

// backupJobs keeps one prepared backup at a time: a second POST while one runs or waits joins it.
type backupJobs struct {
	mu  sync.Mutex
	job *BackupJob
	// expire removes a ready file nobody fetched (a closed tab); a variable for the tests
	expire time.Duration
}

// backupJobExpiry is how long a prepared .sbk waits for its download before it is removed.
const backupJobExpiry = 30 * time.Minute

func (a *SystemAPI) jobs() *backupJobs {
	a.backupJobsOnce.Do(func() {
		if a.backupJobs == nil {
			a.backupJobs = &backupJobs{expire: backupJobExpiry}
		}
	})
	return a.backupJobs
}

// backupPrepare is POST /backup/prepare: starts the backup detached from the request (a closed
// tab does not end it), or answers the one that runs or waits already.
func (a *SystemAPI) backupPrepare(w http.ResponseWriter, r *http.Request) {
	j := a.jobs()
	j.mu.Lock()
	if cur := j.job; cur != nil && (cur.State == "running" || cur.State == "ready") {
		out := *cur
		j.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
		return
	}
	if cur := j.job; cur != nil && cur.busy {
		j.mu.Unlock()
		writeJSON(w, http.StatusConflict, apiError{Error: "backup-busy", Message: "a cancelled backup is still finishing; try again in a moment"})
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	ctx, cancel := context.WithCancel(context.Background())
	job := &BackupJob{ID: hex.EncodeToString(b), State: "running", Started: time.Now(), cancel: cancel, busy: true}
	j.job = job
	out := *job
	j.mu.Unlock()
	reqLog(r).Info("backup: preparing", "id", job.ID)
	go a.runBackupJob(ctx, job)
	writeJSON(w, http.StatusAccepted, out)
}

func (a *SystemAPI) runBackupJob(ctx context.Context, job *BackupJob) {
	// a leftover of a backup that was killed (B-91) goes before the next one is made
	a.Root.SweepBackupTemp(time.Now(), system.BackupTempMaxAge)
	// CreateBackup lets the script finish even when ctx ends, and removes its file then
	path, err := a.Root.CreateBackup(ctx, a.Run)
	j := a.jobs()
	j.mu.Lock()
	job.busy = false
	now := time.Now()
	if job.Finished == nil {
		job.Finished = &now
	}
	switch {
	case job.State != "running":
		// cancelled by DELETE meanwhile: already failed, its file gone with CreateBackup
	case err != nil:
		job.State, job.Error = "failed", err.Error()
	default:
		job.State, job.path = "ready", path
		if st, serr := os.Stat(path); serr == nil {
			job.Size = st.Size()
		}
	}
	ready := job.State == "ready"
	expire := j.expire
	j.mu.Unlock()
	if !ready {
		if err != nil && ctx.Err() == nil {
			slog.Warn("backup: preparing failed", "id", job.ID, "err", err)
		}
		return
	}
	// a file nobody fetched is not kept for ever: it is the size of /usr/local
	time.AfterFunc(expire, func() {
		j.mu.Lock()
		stale := job.State == "ready"
		if stale {
			job.State, job.Error = "failed", "expired"
		}
		j.mu.Unlock()
		if stale {
			a.removeBackupFile(path)
		}
	})
}

// backupJob is GET /backup/prepare/{id}.
func (a *SystemAPI) backupJob(w http.ResponseWriter, r *http.Request) {
	j := a.jobs()
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job == nil || j.job.ID != r.PathValue("id") {
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-backup", Message: "no such prepared backup"})
		return
	}
	writeJSON(w, http.StatusOK, *j.job)
}

// backupJobCancel is DELETE /backup/prepare/{id}: a running backup is stopped, a ready file removed.
func (a *SystemAPI) backupJobCancel(w http.ResponseWriter, r *http.Request) {
	j := a.jobs()
	j.mu.Lock()
	job := j.job
	if job == nil || job.ID != r.PathValue("id") {
		j.mu.Unlock()
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-backup", Message: "no such prepared backup"})
		return
	}
	var remove string
	switch {
	case job.State == "running":
		// the script runs to its end (B-91: a stop half-way leaves its working directory); the job
		// says cancelled now, and CreateBackup removes the file once the script is done
		now := time.Now()
		job.State, job.Error, job.Finished = "failed", "cancelled", &now
		job.cancel()
	case job.State == "ready":
		job.State, job.Error = "failed", "cancelled"
		remove = job.path
	}
	j.mu.Unlock()
	if remove != "" {
		a.removeBackupFile(remove)
	}
	w.WriteHeader(http.StatusNoContent)
}

// takePrepared hands the ready file of job id to exactly one download; "" with a status and code
// when there is none.
func (a *SystemAPI) takePrepared(id string) (path string, status int, code string) {
	j := a.jobs()
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.job
	switch {
	case job == nil || job.ID != id:
		return "", http.StatusNotFound, "unknown-backup"
	case job.State == "running":
		return "", http.StatusConflict, "backup-running"
	case job.State != "ready":
		return "", http.StatusGone, "backup-gone"
	}
	job.State = "downloaded"
	return job.path, 0, ""
}

func (a *SystemAPI) removeBackupFile(path string) {
	if err := a.Root.RemoveBackupFile(path); err != nil {
		slog.Warn("backup: temporary file not removed", "path", path, "err", err)
	}
}
