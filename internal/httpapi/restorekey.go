package httpapi

// openccu-lite task 296: a backup with a non-default BidCos security key. The restore and the
// device import ask for the backup's passphrase as a check - "does the passphrase you have match?"
// - and never block: a wrong or skipped passphrase leads to a clear warning, and the restore goes
// on after the user's confirmation. POST /restore/verify-key answers the check; /restore/apply and
// /restore/import-devices take the passphrase along and say the verdict in their answer (the
// import also in its record). The passphrase is never stored, logged or answered.

import (
	"context"
	"crypto/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/system"
)

func (a *SystemAPI) registerRestoreKey(mux *http.ServeMux, p string) {
	route(mux, auth.ScopePower, "POST "+p+"/restore/verify-key", a.restoreVerifyKey)
}

// backupSigCache keeps the signature of one upload: the check reads it, the passphrase check and
// the apply reuse it while the file is unchanged.
type backupSigCache struct {
	mu   sync.Mutex
	path string
	size int64
	mod  time.Time
	sig  system.BackupSignature
}

// backupSignature answers the upload's signature, from the cache while the file is the same.
func (a *SystemAPI) backupSignature(path string) (system.BackupSignature, error) {
	st, err := os.Stat(path)
	if err != nil {
		return system.BackupSignature{}, err
	}
	c := &a.backupSigs
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == path && c.size == st.Size() && c.mod.Equal(st.ModTime()) {
		return c.sig, nil
	}
	sig, err := system.ReadBackupSignature(path)
	if err != nil {
		return sig, err
	}
	c.path, c.size, c.mod, c.sig = path, st.Size(), st.ModTime(), sig
	return sig, nil
}

// checkBackup runs the firmware's check (restoreBackup.sh -c, answered with an empty key) and adds
// the backup's key index when a key is involved; the signature read for it stays in the cache for
// the passphrase check.
func (a *SystemAPI) checkBackup(ctx context.Context, path string) system.RestoreCheck {
	run := a.RunStdin
	if run == nil && a.Run != nil {
		// a runner without an input (the tests' recorder, the dry runner's twin): the check's one
		// empty line is all it would get
		plain := a.Run
		run = func(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
			return plain(ctx, name, args...)
		}
	}
	c := a.Root.CheckBackup(ctx, run, path)
	if rep, err := a.backupReports.For(path); err == nil {
		c.HasRega = rep.Rega
	}
	if c.NeedsKey || c.BackupKey {
		if sig, err := a.backupSignature(path); err == nil {
			c.KeyIndex = sig.KeyIndex
			// the script tells a key by the signature; key_index > 0 says it as well, for a script
			// output this code does not know
			c.BackupKey = c.BackupKey || sig.NonDefault()
		}
	}
	return c
}

// systemKeyMatches is SystemKeyCheck, or the system's crypttool.
func (a *SystemAPI) systemKeyMatches(ctx context.Context, passphrase string) (set, match, known bool) {
	if a.SystemKeyCheck != nil {
		return a.SystemKeyCheck(ctx, passphrase)
	}
	return a.Root.SystemKeyMatches(ctx, passphrase)
}

// keyVerdict is the answer of /restore/verify-key: the backup's side (system.KeyCheck*) and this
// system's (none: the factory key; match, mismatch, skipped as for the backup; unknown: crypttool
// could not say).
type keyVerdict struct {
	Backup   string `json:"backup"`
	System   string `json:"system"`
	KeyIndex int    `json:"key_index"`
}

func (a *SystemAPI) verdictFor(ctx context.Context, sig system.BackupSignature, passphrase string) keyVerdict {
	v := keyVerdict{Backup: sig.KeyCheck(passphrase), KeyIndex: sig.KeyIndex}
	set, match, known := a.systemKeyMatches(ctx, passphrase)
	switch {
	case !known:
		v.System = "unknown"
	case !set:
		v.System = system.KeyCheckNone
	case match:
		v.System = system.KeyCheckMatch
	case strings.TrimSpace(passphrase) == "":
		v.System = system.KeyCheckSkipped
	default:
		v.System = system.KeyCheckMismatch
	}
	return v
}

// restoreVerifyKey: {file, key} → {backup, system, key_index}. 200 whatever the verdict - a
// mismatch is an answer, not an error. The passphrase goes nowhere but the comparison.
func (a *SystemAPI) restoreVerifyKey(w http.ResponseWriter, r *http.Request) {
	if s := SessionFrom(r); s == nil || !s.Has(auth.ScopePower) {
		forbiddenScope(w, auth.ScopePower)
		return
	}
	var body struct {
		File string `json:"file"`
		Key  string `json:"key"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	path, ok := a.restoreUploadPath(w, body.File)
	if !ok {
		return
	}
	sig, err := a.backupSignature(path)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "not_a_backup", Message: err.Error()})
		return
	}
	v := a.verdictFor(r.Context(), sig, body.Key)
	reqLog(r).Info("restore: the backup's security key was checked", "file", body.File, "backup", v.Backup, "system", v.System, "key_index", v.KeyIndex)
	writeJSON(w, 200, v)
}

// placeholderKey is what the restore script is given when the user restores without the
// passphrase: the script sets the system key from what it reads before the restore (its step 3,
// only when this system has none and the backup has one), and the restore at boot then brings the
// backup's own key files over it. A random value, so that nothing derived from a guessable word is
// left if that step is the last to write the files; letters and digits as crypttool takes them.
func placeholderKey() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
