package system

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/hobbyquaker/occulited/internal/backupcrypt"
)

// Encrypted backups (openccu-lite task 91): the file side. The crypto is internal/backupcrypt's;
// here are the staging of an encrypted upload, its decryption into the .sbk restoreBackup.sh
// reads, and the carry-over of the system's own identity across a restore.

// CarryFile is where the system's age identity waits across a restore at boot. The restore
// (S05CheckBackupRestore) deletes everything under /usr/local except tmp; S06InitSystem then empties
// /usr/local/tmp but keeps its dotfiles; and /usr/local/tmp is in no backup. So a dotfile there is
// the one place that survives a restore without ever being in an archive. At the next start the
// daemon adopts it when the restored state directory brought no identity (the key directory is
// .nobackup) and removes it. A factory reset makes the filesystem anew, so nothing is carried -
// intended.
const CarryFile = BackupDir + "/.occulite-box-identity"

// ErrNoSpace: the decryption needs room for the plain file beside the encrypted one.
var ErrNoSpace = errors.New("not enough free space in the upload directory for the decrypted backup")

// UploadResult is what SaveUploadSniffed tells the API about the file it stored.
type UploadResult struct {
	// Path is the stored file: restore-<name>.sbk (plain, or decrypted with the system's key), or
	// restore-<name>.sbk.age when the system's key does not open it.
	Path string
	// Header is the sniff of the upload.
	Header backupcrypt.Header
	// OpenedWithBox: the file was encrypted and the system's own identity decrypted it on the way.
	OpenedWithBox bool
	// SHA256 is the hash of the upload as received (the encrypted file when it was one), hex.
	SHA256 string
	Size   int64
}

// SaveUploadSniffed stores an uploaded backup. A plain .sbk goes in as SaveUpload did; an age file
// the box identity opens is decrypted while it uploads, so a 1 GB backup needs 1 GB in the upload
// directory, not 2; any other age file is stored as it is for RestoreDecrypt. A decryption error
// leaves nothing behind and returns backupcrypt.ErrCorrupt.
//
// size is the upload's size when the caller knows it (a request's Content-Length, which includes
// the multipart framing), 0 or less otherwise. occulited B-141: the earlier uploads are swept
// first, then a size above MaxSBKUpload or one the userfs cannot hold with its check
// (UploadRoomNeeded: twice the file and UploadReserve) is refused before a byte is read; the stream is capped at MaxSBKUpload with an error rather than cut
// off quietly, an upload that ends early is ErrUploadIncomplete, and a full userfs a *NoRoomError -
// each leaving nothing behind, where a cut-off file used to be kept and checked ("could not untar").
func (r Root) SaveUploadSniffed(src io.Reader, name string, size int64, box age.Identity) (UploadResult, error) {
	var res UploadResult
	// openccu-lite B-194: one upload at a time - an earlier one a wrong key left behind (200 MB
	// until the next boot's S06InitSystem) goes when the next arrives; before the room is measured,
	// so its space counts (B-141)
	r.sweepUploads()
	if err := r.CheckUploadRoom(size); err != nil {
		return res, err
	}
	hash := sha256.New()
	capped := newCapReader(src, MaxSBKUpload)
	counted := &countingReader{r: io.TeeReader(capped, hash)}
	res, err := r.saveSniffed(counted, name, box, hash)
	if err != nil {
		if serr := capped.sourceErr(); serr != nil {
			err = serr
		}
		return res, r.noRoom(err, size)
	}
	return res, nil
}

func (r Root) saveSniffed(counted *countingReader, name string, box age.Identity, sum hash.Hash) (UploadResult, error) {
	var res UploadResult
	h, rest, err := backupcrypt.Sniff(counted)
	if err != nil {
		return res, err
	}
	res.Header = h
	name = safeUploadName(name)
	var content io.Reader = rest
	if h.Encrypted {
		if box != nil && h.Opens(box) {
			content, err = backupcrypt.Decrypt(rest, box)
			if err != nil {
				return res, err
			}
			res.OpenedWithBox = true
			name = strings.TrimSuffix(name, ".age")
		} else {
			if !strings.HasSuffix(name, ".age") {
				name += ".age"
			}
		}
	} else {
		name = strings.TrimSuffix(name, ".age")
	}
	path, err := r.storeUpload(content, name)
	if err != nil {
		return res, err
	}
	res.Path = path
	res.SHA256 = hex.EncodeToString(sum.Sum(nil))
	res.Size = counted.n
	return res, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

var uploadNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+\.sbk(\.age)?$`)

// safeUploadName is the upload's own name when it is a plain .sbk or .sbk.age name, upload.sbk
// (plus .age when the upload said so) otherwise.
func safeUploadName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if uploadNameRe.MatchString(name) {
		return name
	}
	if strings.HasSuffix(strings.ToLower(name), ".age") {
		return "upload.sbk.age"
	}
	return "upload.sbk"
}

// storeUpload writes content into the staging directory as occulited and has the helper move it
// under BackupDir (B-20), as SaveUpload does.
func (r Root) storeUpload(content io.Reader, name string) (string, error) {
	dir := r.join(BackupDir)
	if err := Priv.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "restore-"+name)
	// B-256: the callers already cap content (MaxSBKUpload), but stageFile is bounded here too so
	// no path stages an unbounded reader - the invariant the walker test in staging_test.go checks;
	// B-141: with an error past the cap, never a quiet cut
	tmp, _, err := stageFile(r, "restore-"+name+".part", newCapReader(content, MaxSBKUpload))
	if err != nil {
		return "", err
	}
	if err := Priv.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// sweepUploads removes the restore-*.sbk and restore-*.sbk.age uploads under BackupDir before a
// new upload is stored (openccu-lite B-194): the page handles one restore at a time, and a
// leftover - an encrypted upload whose key never came, a checked file never applied - would
// otherwise hold its 200 MB until the next boot. Best effort; the carry file is a dotfile and
// not touched.
func (r Root) sweepUploads() {
	for _, n := range r.restoreUploads() {
		if err := Priv.Remove(filepath.Join(r.join(BackupDir), n)); err != nil {
			slog.Warn("restore: stale upload not removed", "file", n, "err", err)
		}
	}
}

// restoreUploads are the names of the restore-*.sbk and restore-*.sbk.age files under BackupDir.
func (r Root) restoreUploads() []string {
	entries, err := os.ReadDir(r.join(BackupDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "restore-") || !(strings.HasSuffix(n, ".sbk") || strings.HasSuffix(n, ".sbk.age")) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// sweepableUploads is the room the next upload's sweep frees: the sizes of restoreUploads, when
// BackupDir is on the staging directory's filesystem (it is on a system; RestoreRoom counts it).
func (r Root) sweepableUploads() int64 {
	if same, ok := sameFilesystem(r.join(BackupDir), r.join(StagingDir)); !ok || !same {
		return 0
	}
	var n int64
	for _, name := range r.restoreUploads() {
		if st, err := os.Stat(filepath.Join(r.join(BackupDir), name)); err == nil {
			n += st.Size()
		}
	}
	return n
}

// RestoreDecrypt turns a stored restore-<name>.sbk.age into restore-<name>.sbk with the identity
// and removes the encrypted file. The wrong identity is refused before any payload is read
// (backupcrypt.ErrWrongKey); a damaged file leaves no partial output (ErrCorrupt); too little
// room for the plain file beside the encrypted one is ErrNoSpace, checked first.
func (r Root) RestoreDecrypt(path string, id age.Identity) (string, error) {
	if filepath.Dir(path) != r.join(BackupDir) || !strings.HasSuffix(path, ".sbk.age") {
		return "", fmt.Errorf("not an encrypted upload under %s", BackupDir)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	_ = os.MkdirAll(r.join(StagingDir), 0o700)
	if free, ok := freeBytes(r.join(StagingDir)); ok && free < st.Size() {
		return "", ErrNoSpace
	}
	h, rest, err := backupcrypt.Sniff(f)
	if err != nil {
		return "", err
	}
	if !h.Encrypted {
		return "", fmt.Errorf("%w: not an age file", backupcrypt.ErrCorrupt)
	}
	plain, err := backupcrypt.Decrypt(rest, id)
	if err != nil {
		return "", r.dropCorruptUpload(f, path, err)
	}
	name := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), ".age"), "restore-")
	out, err := r.storeUpload(plain, name)
	if err != nil {
		return "", r.dropCorruptUpload(f, path, err)
	}
	if err := Priv.Remove(path); err != nil {
		return out, fmt.Errorf("encrypted upload not removed: %w", err)
	}
	return out, nil
}

// dropCorruptUpload removes an encrypted upload the decryption gave up on (backupcrypt.ErrCorrupt:
// a damaged or tampered file no key will open) and returns err; any other error - the wrong key,
// no room - keeps the file for the next try (openccu-lite B-194). f is the open handle, closed
// first so the helper's remove does not race a read.
func (r Root) dropCorruptUpload(f *os.File, path string, err error) error {
	if !errors.Is(err, backupcrypt.ErrCorrupt) {
		return err
	}
	_ = f.Close()
	if rerr := Priv.Remove(path); rerr != nil {
		return fmt.Errorf("%w (damaged upload not removed: %v)", err, rerr)
	}
	return err
}

// CarryBoxIdentity puts the identity into CarryFile before a restore: staged as occulited (so the
// daemon can read it back at the next start) and moved into root's /usr/local/tmp by the helper.
// created, when set, becomes the file's mtime, which the rename keeps: it is the identity's
// creation time, read back at the adoption (openccu-lite B-194).
func (r Root) CarryBoxIdentity(identity []byte, created time.Time) error {
	if len(identity) == 0 {
		return nil
	}
	tmp, _, err := stageFile(r, ".occulite-box-identity.part", strings.NewReader(string(identity)))
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if !created.IsZero() {
		if err := os.Chtimes(tmp, created, created); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := Priv.Rename(tmp, r.join(CarryFile)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// AdoptCarriedBoxIdentity runs at start: a CarryFile left by a restore is adopted when the store
// has no identity of its own, and removed either way. adopted says whether the store took it.
func (r Root) AdoptCarriedBoxIdentity(store *backupcrypt.Store) (adopted bool, err error) {
	path := r.join(CarryFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// the file's mtime is the identity's creation time (CarryBoxIdentity); zero when unknown
	var created time.Time
	if st, serr := os.Stat(path); serr == nil {
		created = st.ModTime()
	}
	adopted, aerr := store.AdoptBoxIdentity(b, created)
	if rerr := Priv.Remove(path); rerr != nil && aerr == nil {
		aerr = fmt.Errorf("carried identity not removed: %w", rerr)
	}
	return adopted, aerr
}
