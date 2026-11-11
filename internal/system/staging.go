package system

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// StagingDir is where the unprivileged occulited writes big files (an addon archive, an
// update image) before the privilege boundary moves them into place with one rename: it is on
// the userfs like the destinations, and the helper's policy allows renames out of it.
var StagingDir = "/usr/local/etc/occulite/staging"

// MaxRegadomUpload caps a raw regadom uploaded for the import (B-255): the largest known CCU
// regadom is a few tens of MB, so 64 MiB is generous and stops a caller from filling the userfs.
// MaxFirmwareUpload caps an uploaded device-firmware bundle (B-255/B-256).
const (
	MaxRegadomUpload  = 64 << 20
	MaxFirmwareUpload = 64 << 20
)

// MaxSBKUpload is the cap for an uploaded .sbk (a backup, plain or encrypted), for the restore's
// check and the metadata import alike. It was 2 GiB until occulited B-141, when real systems'
// backups had reached 3.3 and 4.55 GB and could not be uploaded at all; 16 GiB is well above any
// backup seen and only a backstop - the free space on the userfs is the bound that decides, checked
// before an upload is taken (CheckUploadRoom). lighttpd's ceiling for the two routes
// (deploy/lighttpd/occulited.conf) is this plus room for the multipart framing, kept together by
// TestLighttpdBackupCeiling. A var so the tests can lower it.
var MaxSBKUpload int64 = 16 << 30

// UploadReserve is what stays free on the userfs after a backup upload and its check (occulited
// B-141): the daemons keep writing there (the journal, occulited's database, hmipserver's files).
// A var so the tests can set it.
var UploadReserve int64 = 128 << 20

// UploadRoomNeeded is the room a backup of size bytes needs on the userfs: twice the file - the
// upload, and the copy restoreBackup.sh -c unpacks beside it into a directory under /usr/local/tmp
// (its "could not untar" when that does not fit; the apply moves the same unpacked archive) - plus
// UploadReserve. Seen in the lab run: a 3.06 GB backup took 6.1 GB while it was checked.
func UploadRoomNeeded(size int64) int64 { return 2*size + UploadReserve }

// ErrUploadTooLarge is returned by StageUpload when the body exceeds the cap: the caller answers
// 413 instead of parsing a file that was cut off (B-255). A backup's upload returns it wrapped in
// a *TooLargeError, which names the sizes (B-141).
var ErrUploadTooLarge = errors.New("upload too large")

// ErrUploadIncomplete: the upload ended before its file did - the connection broke, or the client
// stopped sending (occulited B-141). The partial file is removed; before, a cut-off backup was
// kept and the check answered "could not untar".
var ErrUploadIncomplete = errors.New("the upload ended before the file was complete: the connection broke or the browser stopped sending; nothing was kept")

// TooLargeError is a backup larger than MaxSBKUpload. Size is the upload's size when it was known
// up front (a Content-Length), 0 when the stream itself passed the cap.
type TooLargeError struct{ Size, Limit int64 }

func (e *TooLargeError) Error() string {
	if e.Size > 0 {
		return fmt.Sprintf("the backup is %d MB, larger than the %d MB an upload may have", e.Size/1e6, e.Limit/1e6)
	}
	return fmt.Sprintf("the backup is larger than the %d MB an upload may have", e.Limit/1e6)
}

func (e *TooLargeError) Is(target error) bool { return target == ErrUploadTooLarge }

// NoRoomError is a backup the userfs has no room for (occulited B-141): Need is UploadRoomNeeded, Free what the filesystem has (after the earlier uploads were swept). Size is 0
// when the upload's size was not known up front and the filesystem filled up while it ran.
type NoRoomError struct{ Size, Need, Free, Reserve int64 }

func (e *NoRoomError) Error() string {
	if e.Size > 0 {
		return fmt.Sprintf("not enough free space on the system for this backup: it needs %d MB (twice the %d MB file - the upload and the copy its check unpacks - and %d MB that stay free), %d MB are free under /usr/local; nothing was stored", e.Need/1e6, e.Size/1e6, e.Reserve/1e6, e.Free/1e6)
	}
	return fmt.Sprintf("the system ran out of free space under /usr/local while the backup was uploaded (%d MB free now); nothing was stored", e.Free/1e6)
}

// RestoreRoom is what a backup upload may take now: free is the userfs's free space plus what the
// next upload sweeps away (the earlier restore-*.sbk[.age] files), reserve UploadReserve, and max
// MaxSBKUpload. ok is false when the filesystem could not be asked.
func (r Root) RestoreRoom() (free, reserve, max int64, ok bool) {
	dir := r.join(StagingDir)
	_ = os.MkdirAll(dir, 0o700)
	free, ok = roomFree(dir)
	if ok {
		free += r.sweepableUploads()
	}
	return free, UploadReserve, MaxSBKUpload, ok
}

// roomFree is freeBytes, a var so a test reads a fixed figure instead of a filesystem other
// tests write to at the same time.
var roomFree = freeBytes

// CheckUploadRoom refuses a backup of size bytes before it is read: larger than MaxSBKUpload is a
// *TooLargeError, more than the userfs can hold (UploadRoomNeeded) a *NoRoomError. size <= 0
// (not known) passes; the stream is capped and an ENOSPC reported as it runs.
func (r Root) CheckUploadRoom(size int64) error {
	if size <= 0 {
		return nil
	}
	if size > MaxSBKUpload {
		return &TooLargeError{Size: size, Limit: MaxSBKUpload}
	}
	dir := r.join(StagingDir)
	_ = os.MkdirAll(dir, 0o700)
	if free, ok := freeBytes(dir); ok && free < UploadRoomNeeded(size) {
		return &NoRoomError{Size: size, Need: UploadRoomNeeded(size), Free: free, Reserve: UploadReserve}
	}
	return nil
}

// noRoom turns an ENOSPC from writing an upload into a *NoRoomError naming the free space now
// (the partial file is gone by then); other errors pass unchanged.
func (r Root) noRoom(err error, size int64) error {
	if !errors.Is(err, syscall.ENOSPC) {
		return err
	}
	free, _ := freeBytes(r.join(StagingDir))
	if size > 0 {
		return &NoRoomError{Size: size, Need: UploadRoomNeeded(size), Free: free, Reserve: UploadReserve}
	}
	return &NoRoomError{Free: free}
}

// capReader passes at most max bytes of r and fails with a *TooLargeError when r has more, where
// io.LimitReader ends quietly and hands on a cut-off file (occulited B-141: a 4.55 GB backup was
// stored as its first 2 GiB and the check answered "could not untar"). err keeps the first error
// the source returned other than io.EOF, and the cap's, so a caller whose reader chain rewraps
// errors (the age decryption marks every read error as corrupt) can still say what happened.
type capReader struct {
	r    io.Reader
	left int64
	max  int64
	err  error
}

func newCapReader(r io.Reader, max int64) *capReader { return &capReader{r: r, left: max, max: max} }

func (c *capReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	if int64(n) > c.left {
		n = int(c.left)
		c.left = 0
		c.err = &TooLargeError{Limit: c.max}
		return n, c.err
	}
	c.left -= int64(n)
	if err != nil && err != io.EOF {
		c.err = err
	}
	return n, err
}

// sourceErr is what the source did wrong, classified: the cap, or an upload that ended early
// (io.ErrUnexpectedEOF, which net/http and mime/multipart return for a cut-off body); nil when
// the source was fine.
func (c *capReader) sourceErr() error {
	switch {
	case c.err == nil:
		return nil
	case errors.Is(c.err, ErrUploadTooLarge):
		return c.err
	case errors.Is(c.err, io.ErrUnexpectedEOF):
		return ErrUploadIncomplete
	}
	return c.err
}

// StageUpload stores an uploaded body in the staging directory on the userfs, capped at max bytes,
// under a unique name derived from prefix, and returns the path and the byte count (B-255). It is
// how an upload that a handler needs as a file on disk - a regadom, an .sbk, a firmware bundle -
// leaves the tmpfs /tmp behind: the userfs is where the big files belong, the copy error is not
// swallowed, and a body over the cap is removed and reported as ErrUploadTooLarge rather than
// parsed truncated. The caller removes the returned path.
func StageUpload(r Root, prefix string, src io.Reader, max int64) (string, int64, error) {
	dir := r.join(StagingDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	f, err := os.CreateTemp(dir, prefix+"-*")
	if err != nil {
		return "", 0, err
	}
	path := f.Name()
	n, err := io.Copy(f, io.LimitReader(src, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", 0, err
	}
	if n > max {
		_ = os.Remove(path)
		return "", n, ErrUploadTooLarge
	}
	return path, n, nil
}

// stageFile stores src under StagingDir/<name> and returns the path and the size.
func stageFile(r Root, name string, src io.Reader) (string, int64, error) {
	dir := r.join(StagingDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", 0, err
	}
	return path, n, nil
}

// StagedArchive is an addon archive already stored in the staging directory (B-4): the install
// API stores the upload while its request runs and installs it detached from the request, and
// Install, handed one, moves it into place instead of copying it again. Read reads the file, for
// an installer that does not know the type.
type StagedArchive struct {
	Path string
	Size int64
	f    *os.File
}

func (s *StagedArchive) Read(p []byte) (int, error) {
	if s.f == nil {
		f, err := os.Open(s.Path)
		if err != nil {
			return 0, err
		}
		s.f = f
	}
	return s.f.Read(p)
}

// Remove closes and deletes the staged file; after an install moved it into place there is
// nothing left to delete.
func (s *StagedArchive) Remove() {
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
	_ = os.Remove(s.Path)
}

// StageAddonArchive stores an addon archive in the staging directory under name, refused as
// Install refuses it: larger than MaxAddonSize, or empty.
func StageAddonArchive(r Root, name string, src io.Reader) (*StagedArchive, error) {
	path, n, err := stageFile(r, name, io.LimitReader(src, MaxAddonSize+1))
	if err != nil {
		return nil, err
	}
	if n > MaxAddonSize {
		_ = os.Remove(path)
		return nil, fmt.Errorf("archive larger than %d MB", MaxAddonSize>>20)
	}
	if n < 64 {
		_ = os.Remove(path)
		return nil, errors.New("archive is empty")
	}
	return &StagedArchive{Path: path, Size: n}, nil
}
