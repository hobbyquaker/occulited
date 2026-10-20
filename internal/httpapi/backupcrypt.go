package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// Encrypted backups (openccu-lite task 91; D-64, D-65, D-80): the routes under /backup/encryption,
// the encrypted download, the restore's sniff and decryption, and the carry-over of the system's
// identity across a restore. The crypto is internal/backupcrypt's, the files internal/system's.

// BackupPlainPath is the virtual path a confirmed ticket (task 154, D-104) is issued for when an
// administrator downloads a backup unencrypted although encryption is on: the password every
// time (D-64), as for the device key sheet. The download itself is GET /backup?encrypted=false
// with the ticket in ?confirm= (a download is a navigation and carries no header).
const BackupPlainPath = "/api/system/v1/backup/unencrypted"

func (a *SystemAPI) registerBackupCrypt(mux *http.ServeMux, p string) {
	route(mux, auth.ScopeBackup, "GET "+p+"/backup/encryption", a.encryptionView)
	route(mux, auth.ScopeBackup, "PUT "+p+"/backup/encryption", a.encryptionPut)
	route(mux, auth.ScopeBackup, "POST "+p+"/backup/encryption/recovery", a.encryptionRecovery)
	route(mux, auth.ScopeBackup, "POST "+p+"/backup/encryption/confirm", a.encryptionConfirm)
	route(mux, auth.ScopeBackup, "POST "+p+"/backup/encryption/test", a.encryptionTest)
	route(mux, auth.ScopePower, "POST "+p+"/restore/decrypt", a.restoreDecrypt)
}

// encryptionReady answers 501 when the store is not wired (a development run without one).
func (a *SystemAPI) encryptionReady(w http.ResponseWriter) bool {
	if a.BackupCrypt == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "backup encryption is not available here"})
		return false
	}
	return true
}

func (a *SystemAPI) encryptionView(w http.ResponseWriter, _ *http.Request) {
	if !a.encryptionReady(w) {
		return
	}
	v, err := a.BackupCrypt.View()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// encryptionRecovery starts a setup or a rotation: the recipient the browser derived from the code
// it made, or {generate: true} when the browser could not (no WebCrypto on plain HTTP) - then the
// code comes back in this one answer and is forgotten at the confirmation.
func (a *SystemAPI) encryptionRecovery(w http.ResponseWriter, r *http.Request) {
	if !a.encryptionReady(w) {
		return
	}
	var b struct {
		Recipient string `json:"recipient"`
		Generate  bool   `json:"generate"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	out := map[string]any{}
	switch {
	case b.Generate:
		id, fp, code, err := a.BackupCrypt.BeginGenerated()
		if err != nil {
			writeErr(w, err)
			return
		}
		out["pending_id"], out["fingerprint"], out["recovery_key"] = id, fp, code
	case b.Recipient != "":
		id, fp, err := a.BackupCrypt.BeginRecovery(b.Recipient)
		if err != nil {
			writeCryptErr(w, err)
			return
		}
		out["pending_id"], out["fingerprint"] = id, fp
	default:
		badBody(w, errors.New("recipient (age1…) or generate: true"))
		return
	}
	writeJSON(w, 200, out)
}

// encryptionConfirm finishes it once the user ticked that the key is stored outside this system.
func (a *SystemAPI) encryptionConfirm(w http.ResponseWriter, r *http.Request) {
	if !a.encryptionReady(w) {
		return
	}
	var b struct {
		PendingID string `json:"pending_id"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	st, err := a.BackupCrypt.Confirm(b.PendingID)
	if err != nil {
		if errors.Is(err, backupcrypt.ErrPendingUnknown) {
			writeJSON(w, http.StatusNotFound, apiError{Error: "pending-unknown", Message: err.Error()})
			return
		}
		writeErr(w, err)
		return
	}
	reqLog(r).Info("backup encryption: recovery key set", "fingerprint", st.Recovery.Fingerprint, "previous", len(st.Previous))
	a.encryptionView(w, r)
}

// encryptionPut is the switch; on needs a recovery key (422 no-recovery-key).
func (a *SystemAPI) encryptionPut(w http.ResponseWriter, r *http.Request) {
	if !a.encryptionReady(w) {
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if _, err := a.BackupCrypt.SetEnabled(b.Enabled); err != nil {
		if errors.Is(err, backupcrypt.ErrNoRecoveryKey) {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "no-recovery-key", Message: err.Error()})
			return
		}
		writeErr(w, err)
		return
	}
	reqLog(r).Info("backup encryption: switched", "enabled", b.Enabled)
	a.encryptionView(w, r)
}

// encryptionTest checks a typed recovery key against the current and earlier ones without
// storing it: what the emergency kit and "Test my recovery key" need. The answer carries the
// derived age identity and the grouped code, so the page can print the kit for a key the user
// only pasted. 422 invalid-key for a typo (never "wrong key"), is-recipient, unsupported.
func (a *SystemAPI) encryptionTest(w http.ResponseWriter, r *http.Request) {
	if !a.encryptionReady(w) {
		return
	}
	var b struct {
		RecoveryKey string `json:"recovery_key"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	sec, err := backupcrypt.ParseSecret(b.RecoveryKey)
	if err != nil {
		writeCryptErr(w, err)
		return
	}
	kind, key := a.BackupCrypt.Match(sec.Recipient())
	out := map[string]any{"matches": kind, "fingerprint": sec.Fingerprint(), "identity": sec.IdentityString(), "recovery_key": sec.Code()}
	if key != nil {
		out["created"] = key.Created
		if !key.Retired.IsZero() {
			out["retired"] = key.Retired
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// writeCryptErr maps the crypto package's errors onto the API's codes.
func writeCryptErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backupcrypt.ErrTypo), errors.Is(err, backupcrypt.ErrFormat):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-key", Message: err.Error()})
	case errors.Is(err, backupcrypt.ErrIsRecipient):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "is-recipient", Message: err.Error()})
	case errors.Is(err, backupcrypt.ErrUnsupported):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "unsupported", Message: err.Error()})
	case errors.Is(err, backupcrypt.ErrWrongKey):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "wrong-key", Message: err.Error()})
	case errors.Is(err, backupcrypt.ErrCorrupt):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "corrupt", Message: err.Error()})
	case errors.Is(err, system.ErrNoSpace):
		writeJSON(w, http.StatusInsufficientStorage, apiError{Error: "no-space", Message: err.Error()})
	default:
		writeErr(w, err)
	}
}

// encryptedDownload says whether GET /backup hands out the .sbk.age: encryption on, unless the
// caller asked for the plain file and confirmed it. plainRefused is set when the answer was
// written already (the confirmation is missing).
func (a *SystemAPI) encryptedDownload(w http.ResponseWriter, r *http.Request) (encrypted, plainRefused bool) {
	if a.BackupCrypt == nil || !a.BackupCrypt.Enabled() {
		return false, false
	}
	if r.URL.Query().Get("encrypted") != "false" {
		return true, false
	}
	sess := SessionFrom(r)
	if sess != nil && sess.IsToken() {
		return false, false
	}
	t := r.Header.Get("X-Occulite-Confirm")
	if t == "" {
		t = r.URL.Query().Get("confirm")
	}
	if sess == nil || t == "" || a.ConfirmTicket == nil || !a.ConfirmTicket(t, BackupPlainPath, sess.ID) {
		writeJSON(w, http.StatusForbidden, apiError{Error: "confirm-required", Message: "an unencrypted download asks for the password (or a fresh login at the provider) every time"})
		return false, true
	}
	return false, false
}

// streamEncrypted writes the .sbk as one age stream for the system's identity and the recovery
// key, hashing what goes out; the returned hash and size are recorded as created here.
func (a *SystemAPI) streamEncrypted(w io.Writer, src io.Reader) (sha string, n int64, err error) {
	box, rec, meta, err := a.BackupCrypt.Recipients()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(w, h)}
	enc, err := backupcrypt.Encrypt(cw, meta, box, rec)
	if err != nil {
		return "", 0, err
	}
	if _, err := io.Copy(enc, src); err != nil {
		return "", 0, err
	}
	if err := enc.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), cw.n, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// recordCreated notes a handed-out backup; a failure is logged, never the download's.
func (a *SystemAPI) recordCreated(name, sha string, size int64, encrypted bool) {
	if a.BackupCrypt == nil || sha == "" {
		return
	}
	if err := a.BackupCrypt.RecordCreated(backupcrypt.Created{SHA256: sha, Name: name, Size: size, Time: time.Now(), Encrypted: encrypted}); err != nil {
		slog.Warn("backup: created list not written", "err", err)
	}
}

// RestoreEncryption is the encryption part of POST /restore/check's and /restore/decrypt's answer.
type RestoreEncryption struct {
	Encrypted bool   `json:"encrypted"`
	Format    string `json:"format,omitempty"`
	Armored   bool   `json:"armored,omitempty"`
	// The fingerprints the file's header names (empty for a file without the meta stanza).
	BoxFingerprint      string `json:"box_fingerprint,omitempty"`
	RecoveryFingerprint string `json:"recovery_fingerprint,omitempty"`
	// Known says whose recovery key the header names: current, previous or unknown.
	Known string `json:"known,omitempty"`
	// KeyCreated is that recovery key's date when it is known.
	KeyCreated *time.Time `json:"key_created,omitempty"`
	// OpenedWith: "box" when this system's own key decrypted it, "recovery" after /restore/decrypt.
	OpenedWith string `json:"opened_with,omitempty"`
	// NeedsRecoveryKey: the file is still encrypted; /restore/decrypt with the key comes next.
	NeedsRecoveryKey bool `json:"needs_recovery_key"`
	// Passphrase: an age passphrase file, which this system cannot open at all.
	Passphrase bool `json:"passphrase,omitempty"`
	// CreatedHere: the uploaded file's hash is in this system's list of backups it handed out.
	CreatedHere bool `json:"created_here"`
}

func (a *SystemAPI) restoreEncryption(up system.UploadResult, opened string) RestoreEncryption {
	e := RestoreEncryption{Encrypted: up.Header.Encrypted}
	if a.BackupCrypt != nil {
		e.CreatedHere = a.BackupCrypt.CreatedHere(up.SHA256)
	}
	if !up.Header.Encrypted {
		return e
	}
	e.Format, e.Armored = "age-v1", up.Header.Armored
	e.BoxFingerprint, e.RecoveryFingerprint = up.Header.BoxFingerprint, up.Header.RecoveryFingerprint
	e.Passphrase = up.Header.Scrypt
	e.Known = "unknown"
	if a.BackupCrypt != nil {
		kind, key := a.BackupCrypt.MatchFingerprint(up.Header.RecoveryFingerprint)
		e.Known = kind
		if key != nil {
			c := key.Created
			e.KeyCreated = &c
		}
	}
	e.OpenedWith = opened
	e.NeedsRecoveryKey = opened == ""
	return e
}

// restoreDecrypt turns a stored restore-*.sbk.age into the .sbk with the recovery key, removes the
// encrypted file, runs the check and answers like /restore/check. 422 wrong-key right after the
// header, corrupt on a failed chunk, invalid-key for a typo; 507 no-space.
func (a *SystemAPI) restoreDecrypt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File        string `json:"file"`
		RecoveryKey string `json:"recovery_key"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if filepath.Base(body.File) != body.File || !strings.HasPrefix(body.File, "restore-") || !strings.HasSuffix(body.File, ".sbk.age") {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "file must be the .sbk.age name restore/check returned"})
		return
	}
	path := filepath.Join(string(a.Root), system.BackupDir, body.File)
	if _, err := os.Stat(path); err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: "upload not found - check it again"})
		return
	}
	sec, err := backupcrypt.ParseSecret(body.RecoveryKey)
	if err != nil {
		writeCryptErr(w, err)
		return
	}
	out, err := a.Root.RestoreDecrypt(path, sec.Identity())
	if err != nil {
		// openccu-lite B-194: a damaged upload is gone with the refusal (no key will open it);
		// a wrong key keeps it, the right one may come next
		if errors.Is(err, backupcrypt.ErrCorrupt) {
			reqLog(r).Info("restore: damaged encrypted upload removed", "file", body.File, "err", err)
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "corrupt", Message: err.Error(), Detail: map[string]any{"upload_removed": true}})
			return
		}
		writeCryptErr(w, err)
		return
	}
	// the answer's encryption block: opened with the recovery key just typed
	enc := a.restoreEncryption(system.UploadResult{Path: out, Header: backupcrypt.Header{Encrypted: true}}, "recovery")
	if a.BackupCrypt != nil {
		kind, key := a.BackupCrypt.Match(sec.Recipient())
		enc.Known = kind
		enc.RecoveryFingerprint = sec.Fingerprint()
		if key != nil {
			c := key.Created
			enc.KeyCreated = &c
		}
	}
	c := a.checkBackup(r.Context(), out)
	reqLog(r).Info("restore: backup decrypted with the recovery key", "file", filepath.Base(out), "key", enc.Known)
	writeJSON(w, 200, map[string]any{"file": filepath.Base(out), "check": c, "encryption": enc})
}

// carryBoxIdentity puts the system's identity where the restore at boot does not reach, right
// before restore/apply (system.CarryFile). A failure is logged: the restore goes on, and the
// older encrypted backups then need the recovery key on this system.
func (a *SystemAPI) carryBoxIdentity() {
	if a.BackupCrypt == nil {
		return
	}
	identity, created := a.BackupCrypt.ExportBoxIdentity()
	if err := a.Root.CarryBoxIdentity(identity, created); err != nil {
		slog.Warn("restore: the system's backup identity was not carried over", "err", err)
	}
}

// backup-unencrypted (task 91, D-80; occulited task 88): backups leave this system readable while
// encryption is not on - no recovery key set up, or the switch off. Silenceable. Since task 88 it is
// the one place that says so: the Backup page dropped its three inline notices for it (maintainer,
// 2026-10-20), so it no longer waits for a nightly or a network target - a download is a backup too.
func (a *SystemAPI) backupUnencryptedWarning(context.Context) ([]warnings.Warning, bool) {
	if a.BackupCrypt == nil || a.BackupCrypt.Enabled() {
		return nil, true
	}
	return []warnings.Warning{{ID: "backup-unencrypted", Variant: "plain", Severity: warnings.SeverityWarning, Href: "/backup#encryption"}}, true
}
