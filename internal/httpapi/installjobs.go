package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/system"
)

// An uploaded addon's install runs as a job of its own (B-4). The installer reloads lighttpd when
// the addon brings a web fragment (B-120), and the reload ends the proxied request: an install
// that ran on the request's context lost its tail - the unit generation and the start of the new
// addon - to "context canceled", and the client got no answer. Now the request only stores the
// archive; the install runs detached, and the client polls the job.

// InstallJob is one upload install as GET /addons/install answers it.
type InstallJob struct {
	ID string `json:"id"`
	// State is "running", "done" (the installer ran and exited 0 or 10) or "failed" (it did not
	// run, or exited with anything else - Result says how)
	State    string                `json:"state"`
	Started  time.Time             `json:"started"`
	Finished *time.Time            `json:"finished,omitempty"`
	Bytes    int64                 `json:"bytes"`
	Result   *system.InstallResult `json:"result,omitempty"`
	Error    string                `json:"error,omitempty"`
	done     chan struct{}
}

// installJobs keeps the running job and the last few finished ones; one install at a time.
type installJobs struct {
	mu   sync.Mutex
	list []*InstallJob // oldest first
}

const keepInstallJobs = 8

// errInstallRunning refuses a second install while one runs: install_addon has one archive path.
var errInstallRunning = errors.New("an install is already running")

// errUninstallRunning refuses an install while an uninstall runs, and a second uninstall (B-113):
// the uninstall is part of the one install slot, so install_addon never runs beside the script
// that removes an addon - possibly the very one it updates.
var errUninstallRunning = errors.New("an uninstall is already running")

// InstallBusy is what the one install slot holds now, as GET /catalog/progress answers it in
// `busy` (B-113): the Addons page's one state for every tab, as GET /system-update's `busy` is the
// Updates page's (B-111). Kind is "catalog" (a catalogue run, AddonID; `progress` describes it),
// "upload" (an uploaded archive's job, Job and Bytes; GET /addons/install?job=) or "uninstall"
// (AddonID).
type InstallBusy struct {
	Kind    string    `json:"kind"`
	AddonID string    `json:"addon_id,omitempty"`
	Job     string    `json:"job,omitempty"`
	Started time.Time `json:"started"`
	Bytes   int64     `json:"bytes,omitempty"`
}

// installBusy says what the install slot holds: a catalogue run first (it is what `progress`
// shows), then an upload's job, then an uninstall; nil when nothing runs.
func (a *SystemAPI) installBusy() *InstallBusy {
	if a.Catalog != nil {
		if p := a.Catalog.Progress(); p != nil && p.Finished == nil {
			return &InstallBusy{Kind: "catalog", AddonID: p.AddonID, Started: p.Started}
		}
	}
	if job, ok := a.installs.get(""); ok && job.State == "running" {
		return &InstallBusy{Kind: "upload", Job: job.ID, Started: job.Started, Bytes: job.Bytes}
	}
	a.installGate.Lock()
	defer a.installGate.Unlock()
	if a.uninstalling != nil {
		u := *a.uninstalling
		return &u
	}
	return nil
}

// slotTaken is the refusal for a start while the slot is held: the error that names what runs,
// or nil. The caller holds installGate when it matters (the check and the start as one step).
func (a *SystemAPI) slotTaken() error {
	if a.uninstalling != nil {
		return errUninstallRunning
	}
	if a.installs.running() || a.catalogInstalling() {
		return errInstallRunning
	}
	return nil
}

func (j *installJobs) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.list) > 0 && j.list[len(j.list)-1].State == "running"
}

func (j *installJobs) begin(bytes int64) (*InstallJob, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.list) > 0 && j.list[len(j.list)-1].State == "running" {
		return nil, errInstallRunning
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	job := &InstallJob{ID: hex.EncodeToString(b), State: "running", Started: time.Now(), Bytes: bytes, done: make(chan struct{})}
	j.list = append(j.list, job)
	if len(j.list) > keepInstallJobs {
		j.list = j.list[len(j.list)-keepInstallJobs:]
	}
	return job, nil
}

func (j *installJobs) finish(job *InstallJob, res *system.InstallResult, err error) {
	j.mu.Lock()
	now := time.Now()
	job.Finished, job.Result = &now, res
	switch {
	case err != nil:
		job.State, job.Error = "failed", err.Error()
	case res.Exit != 0 && res.Exit != 10:
		job.State = "failed"
	default:
		job.State = "done"
	}
	j.mu.Unlock()
	close(job.done)
}

// get returns a copy of the job of that id, or of the newest one for "".
func (j *installJobs) get(id string) (InstallJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := len(j.list) - 1; i >= 0; i-- {
		if id == "" || j.list[i].ID == id {
			return *j.list[i], true
		}
	}
	return InstallJob{}, false
}

// install takes an uploaded archive (multipart "file" or a raw body), stores it and starts the
// install detached; 202 with the job. ?wait=true answers when the job ends instead (a script's
// convenience - the install runs detached either way, so a cut connection costs only the answer).
func (a *SystemAPI) install(w http.ResponseWriter, r *http.Request) {
	job := a.acceptInstall(w, r)
	if job == nil {
		return
	}
	if r.URL.Query().Get("wait") == "true" {
		select {
		case <-job.done:
		case <-r.Context().Done():
			return
		}
		out, _ := a.installs.get(job.ID)
		status := http.StatusOK
		if out.State != "done" {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, out)
		return
	}
	out, _ := a.installs.get(job.ID)
	writeJSON(w, http.StatusAccepted, out)
}

// acceptInstall stores the uploaded archive and starts its install as a job; nil after it has
// answered the request with the reason there is none.
func (a *SystemAPI) acceptInstall(w http.ResponseWriter, r *http.Request) *InstallJob {
	if a.Manager == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no addon manager"})
		return nil
	}
	// refused before the upload is stored: install_addon has one archive path, the catalogue
	// installs through the same installer, and an uninstall holds the slot too (B-113)
	a.installGate.Lock()
	err := a.slotTaken()
	a.installGate.Unlock()
	if err != nil {
		writeJSON(w, http.StatusConflict, apiError{Error: "install-running", Message: err.Error()})
		return nil
	}
	var src io.Reader = r.Body
	if ct := r.Header.Get("Content-Type"); len(ct) >= 9 && ct[:9] == "multipart" {
		mr, err := r.MultipartReader()
		if err != nil {
			badBody(w, err)
			return nil
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				badBody(w, io.ErrUnexpectedEOF)
				return nil
			}
			if part.FormName() == "file" {
				src = part
				break
			}
		}
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	staged, err := system.StageAddonArchive(a.Root, "upload-"+hex.EncodeToString(b)+".tar.gz", src)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "install-failed", Message: err.Error()})
		return nil
	}
	// asked again now that the upload is stored, under the lock the catalogue's start and the
	// uninstall take too: one may have begun while the archive arrived
	a.installGate.Lock()
	var job *InstallJob
	if err = a.slotTaken(); err == nil {
		job, err = a.installs.begin(staged.Size)
	}
	a.installGate.Unlock()
	if err != nil {
		staged.Remove()
		writeJSON(w, http.StatusConflict, apiError{Error: "install-running", Message: err.Error()})
		return nil
	}
	go a.runInstall(job, staged)
	return job
}

// InstallExitHeader carries the installer's exit code on POST /addons/install/local's answer.
const InstallExitHeader = "X-Occulite-Addon-Exit"

// installLocal answers POST /addons/install/local (openccu-lite B-274): the fork's /bin/install_addon,
// called outside occulited - on the command line, by an addon's own updater - hands its archive
// here, so the install is the same job as an upload's (the manifest, the policy, the unit stopped
// and started by systemd) instead of an update script's `rc.d/<id> start` leaving the addon running
// as its caller, root and unconfined. The credential is the install token, root's alone
// (system.InstallTokenFile); only from this system. The answer is for a shell: the output as plain
// text, the installer's exit code in X-Occulite-Addon-Exit (1 when the installer did not run), and
// the status of POST /addons/install?wait=true.
func (a *SystemAPI) installLocal(w http.ResponseWriter, r *http.Request) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if a.InstallToken == "" || !ok || !loopbackOnly(r) || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(tok)), []byte(a.InstallToken)) != 1 {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "the install token is required"})
		return
	}
	job := a.acceptInstall(w, r)
	if job == nil {
		return
	}
	select {
	case <-job.done:
	case <-r.Context().Done():
		return
	}
	out, _ := a.installs.get(job.ID)
	text, exit := out.Error, 1
	if out.Result != nil {
		text, exit = out.Result.Output, out.Result.Exit
	}
	status := http.StatusOK
	if out.State != "done" {
		status = http.StatusUnprocessableEntity
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set(InstallExitHeader, strconv.Itoa(exit))
	w.WriteHeader(status)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, _ = io.WriteString(w, text)
}

// runInstall is the job: on a context of its own, which no request ends.
func (a *SystemAPI) runInstall(job *InstallJob, staged *system.StagedArchive) {
	defer staged.Remove()
	res, err := a.Manager.Install(context.Background(), staged)
	// the staged file goes before the job says it is over: a reader that sees "done" finds
	// nothing left behind (Gitea run 618 caught the deferred remove after finish)
	staged.Remove()
	a.installs.finish(job, res, err)
	a.addonsChanged() // openccu-lite B-297
	if err != nil {
		slog.Warn("addons: an uploaded archive was not installed", "job", job.ID, "err", err)
		return
	}
	slog.Info("addons: an uploaded archive was installed", "job", job.ID, "exit", res.Exit, "seconds", res.Seconds)
	if (res.Exit == 0 || res.Exit == 10) && a.Updates != nil {
		// the addon update check's results are from before this install; an archive does not say
		// which addon it was, so the whole check runs again (in its loop, not in this job)
		a.Updates.Trigger()
	}
}

// installJob answers GET /addons/install: ?job=<id>, or the newest job without one. Without one
// and before the first install it is 204: the Addons page asks at every opening, and a 404 there
// was a "Failed to load resource" in the browser's console after every start (occulited B-15).
func (a *SystemAPI) installJob(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("job")
	job, ok := a.installs.get(id)
	if !ok && id == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-job", Message: "no such install job"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// catalogInstalling says whether a catalogue install runs now.
func (a *SystemAPI) catalogInstalling() bool {
	if a.Catalog == nil {
		return false
	}
	p := a.Catalog.Progress()
	return p != nil && p.Finished == nil
}
