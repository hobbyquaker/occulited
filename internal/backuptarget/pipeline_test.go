package backuptarget

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/backupcrypt"
)

type pipeRig struct {
	p       *Pipeline
	srv     *sftpServer
	sftp    Target
	usb     string
	content []byte
	secret  *backupcrypt.Secret
}

func newPipeRig(t *testing.T, encrypt bool) *pipeRig {
	t.Helper()
	old := checkDir
	checkDir = func(Target) (string, error) { return "", nil }
	t.Cleanup(func() { checkDir = old })
	root := t.TempDir()
	st := &Store{Dir: t.TempDir(), Root: root}
	crypt := &backupcrypt.Store{Dir: st.Dir}
	r := &pipeRig{usb: filepath.Join(t.TempDir(), "backup"), content: make([]byte, 200_000)}
	_, _ = rand.Read(r.content)
	if encrypt {
		id, _, code, err := crypt.BeginGenerated()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := crypt.Confirm(id); err != nil {
			t.Fatal(err)
		}
		if _, err := crypt.SetEnabled(true); err != nil {
			t.Fatal(err)
		}
		if _, err := crypt.EnsureBoxIdentity(); err != nil {
			t.Fatal(err)
		}
		r.secret, _ = backupcrypt.ParseSecret(code)
	}
	if _, err := st.Put(Target{Kind: KindDirectory, Name: "USB", Enabled: true, MaxBackups: 2, Encrypt: true, Directory: &Directory{Path: r.usb}}); err != nil {
		t.Fatal(err)
	}
	r.srv = newSFTPServer(t)
	r.sftp = trusted(t, r.srv, st)
	r.p = &Pipeline{Store: st, Crypt: crypt, Staging: filepath.Join(t.TempDir(), "staging"), Hostname: "lab-box", Version: "1.0.0",
		Now: func() time.Time { return time.Date(2026, 9, 24, 0, 7, 0, 0, time.UTC) },
		Create: func(_ context.Context, path string) error {
			return os.WriteFile(path, r.content, 0o600)
		}}
	return r
}

func (r *pipeRig) open(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.secret == nil {
		return b
	}
	_, rest, err := backupcrypt.Sniff(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := backupcrypt.Decrypt(rest, r.secret.Identity())
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(plain)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPipelineEncrypted(t *testing.T) {
	r := newPipeRig(t, true)
	ctx := context.Background()
	// older backups of this system and one of another in the USB directory: retention keeps 2
	_ = os.MkdirAll(r.usb, 0o755)
	for i, n := range []string{"lab-box-1.0.0-2026-09-20-0007.sbk", "lab-box-1.0.0-2026-09-21-0007.sbk.age", "other-1-2026-09-01-0007.sbk"} {
		p := filepath.Join(r.usb, n)
		_ = os.WriteFile(p, []byte("old"), 0o644)
		when := time.Date(2026, 9, 20+i, 0, 7, 0, 0, time.UTC)
		_ = os.Chtimes(p, when, when)
	}
	if err := r.p.CreateRun(ctx, "all"); err != nil {
		t.Fatal(err)
	}
	name := "lab-box-1.0.0-2026-09-24-0007.sbk.age"
	if got := r.open(t, filepath.Join(r.usb, name)); !bytes.Equal(got, r.content) {
		t.Fatal("the USB copy does not decrypt to the backup")
	}
	res, ok := ReadResult(r.p.Store.SecretDir(DirectoryID))
	if !ok || !res.OK || !res.Encrypted || res.Name != name || res.LastOK == nil || len(res.Removed) != 1 || res.Removed[0] != "lab-box-1.0.0-2026-09-20-0007.sbk" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(r.usb, "other-1-2026-09-01-0007.sbk")); err != nil {
		t.Fatal("another system's backup was removed")
	}
	// nothing plain was staged: every target encrypts
	staged, _ := os.ReadDir(r.p.Staging)
	for _, e := range staged {
		if strings.HasSuffix(e.Name(), ".sbk") {
			t.Fatalf("a plain file staged: %s", e.Name())
		}
	}
	if err := r.p.DeliverRun(ctx, "all"); err != nil {
		t.Fatal(err)
	}
	if got := r.open(t, filepath.Join(r.srv.root, "backups", "box", name)); !bytes.Equal(got, r.content) {
		t.Fatal("the SFTP copy does not decrypt to the backup")
	}
	if res, ok := ReadResult(r.p.Store.SecretDir(r.sftp.ID)); !ok || !res.OK || res.SHA256 == "" {
		t.Fatalf("%+v", res)
	}
	if left, _ := os.ReadDir(r.p.Staging); len(left) != 0 {
		t.Fatalf("staging not emptied: %v", left)
	}
	b, _ := os.ReadFile(filepath.Join(r.p.Store.SecretDir(DirectoryID), LastFile))
	if !r.p.Crypt.CreatedHere(res.SHA256) {
		t.Fatalf("the delivered file is not in the created list: %s", b)
	}
}

func TestPipelinePlainAndOneTarget(t *testing.T) {
	r := newPipeRig(t, false)
	ctx := context.Background()
	if err := r.p.CreateRun(ctx, r.sftp.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.p.DeliverRun(ctx, r.sftp.ID); err != nil {
		t.Fatal(err)
	}
	name := "lab-box-1.0.0-2026-09-24-0007.sbk"
	if got := r.open(t, filepath.Join(r.srv.root, "backups", "box", name)); !bytes.Equal(got, r.content) {
		t.Fatal("the plain SFTP copy differs")
	}
	// only the one target: the USB directory got nothing
	if _, err := os.Stat(filepath.Join(r.usb, name)); !os.IsNotExist(err) {
		t.Fatal("the USB target was written although only the SFTP one was asked for")
	}
}

func TestPipelineNightlyOffAndFailures(t *testing.T) {
	r := newPipeRig(t, false)
	ctx := context.Background()
	_ = r.p.Store.SetNightly(false)
	called := false
	r.p.Create = func(context.Context, string) error { called = true; return nil }
	if err := r.p.CreateRun(ctx, "nightly"); err != nil || called {
		t.Fatalf("%v %v", err, called)
	}
	_ = r.p.Store.SetNightly(true)
	r.p.Create = func(context.Context, string) error { return errors.New("exit 2") }
	if err := r.p.CreateRun(ctx, "nightly"); err == nil {
		t.Fatal("a failed createBackup.sh is not an error")
	}
	for _, id := range []string{DirectoryID, r.sftp.ID} {
		res, ok := ReadResult(r.p.Store.SecretDir(id))
		if !ok || res.OK || res.Step != "create" || res.State != StateError {
			t.Fatalf("%s: %+v", id, res)
		}
	}
	if err := r.p.DeliverRun(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(r.p.Staging); len(left) != 0 {
		t.Fatalf("staging not emptied: %v", left)
	}
	// the server gone: the SFTP target fails, the USB one does not, and the next good run keeps
	// the earlier success's time
	r.p.Create = func(_ context.Context, path string) error { return os.WriteFile(path, r.content, 0o600) }
	if err := r.p.CreateRun(ctx, "all"); err != nil {
		t.Fatal(err)
	}
	srvSFTP := *r.sftp.SFTP
	srvSFTP.Port = closedPort(t) // nothing there
	tg := r.sftp
	tg.SFTP = &srvSFTP
	if _, err := r.p.Store.Put(tg); err != nil {
		t.Fatal(err)
	}
	if err := r.p.DeliverRun(ctx, "all"); err == nil {
		t.Fatal("a failed upload is not an error")
	}
	res, _ := ReadResult(r.p.Store.SecretDir(r.sftp.ID))
	if res.OK || res.Step != "connect" {
		t.Fatalf("%+v", res)
	}
	if bad := []string{"../x", "ALL", "a-b", ""}; true {
		for _, b := range bad {
			if err := r.p.CreateRun(ctx, b); err == nil {
				t.Errorf("instance %q accepted", b)
			}
		}
	}
}

func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

// task 86's follow-up (task 161): the USB directory target without its stick is skipped - its
// result says no-medium, the run succeeds, the other target gets its copy - and when it is the
// only target nothing is made at all.
func TestPipelineUSBStickMissing(t *testing.T) {
	r := newPipeRig(t, false)
	ctx := context.Background()
	usb, ok, err := r.p.Store.Get(DirectoryID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	usb.Directory = &Directory{Path: "/media/usb0/backup"}
	if _, err := r.p.Store.Put(usb); err != nil {
		t.Fatal(err)
	}
	checkDir = func(t Target) (string, error) {
		if t.OnUSB() {
			return "statfs", errors.New("/media/usb0/backup is in RAM")
		}
		return "", nil
	}
	made := 0
	r.p.Create = func(_ context.Context, path string) error { made++; return os.WriteFile(path, r.content, 0o600) }
	if err := r.p.CreateRun(ctx, "nightly"); err != nil {
		t.Fatalf("a missing stick fails the run: %v", err)
	}
	res, ok := ReadResult(r.p.Store.SecretDir(DirectoryID))
	if !ok || res.OK || res.State != StateNoMedium || res.Step != "medium" || !strings.Contains(res.Error, "no USB stick") || !Failed(res.State) {
		t.Fatalf("usb: %+v", res)
	}
	if err := r.p.DeliverRun(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	if res, _ := ReadResult(r.p.Store.SecretDir(r.sftp.ID)); !res.OK || made != 1 {
		t.Fatalf("the SFTP target gets its copy: %+v, made %d", res, made)
	}
	// the stick as the only target: nothing is made
	sftpT := r.sftp
	sftpT.Enabled = false
	if _, err := r.p.Store.Put(sftpT); err != nil {
		t.Fatal(err)
	}
	if err := r.p.CreateRun(ctx, "nightly"); err != nil || made != 1 {
		t.Fatalf("%v, made %d", err, made)
	}
	if left, _ := os.ReadDir(r.p.Staging); len(left) != 0 {
		t.Fatalf("staging: %v", left)
	}
	// a share's mount point and a directory elsewhere are no USB stick
	if (Target{Kind: KindDirectory, Directory: &Directory{Path: "/media/net/x"}}).OnUSB() || (Target{Kind: KindDirectory, Directory: &Directory{Path: "/srv/b"}}).OnUSB() {
		t.Fatal("OnUSB")
	}
}

// B-212: a share target that cannot take a file is found before the backup is made: its result
// says why (read-only, not the mount unit's "Mounted …"), the other targets get their copy, and
// when it is the only target nothing is made and the run fails.
func TestPipelineProbeBeforeCreate(t *testing.T) {
	r := newPipeRig(t, false)
	ctx := context.Background()
	share, err := r.p.Store.Put(Target{Name: "NAS", Kind: KindNFS, Enabled: true, Subdir: "lab", NFS: &NFS{Server: "192.0.2.10", Export: "/b"}})
	if err != nil {
		t.Fatal(err)
	}
	oldProbe, oldMounted, oldLog := probeDir, shareMounted, MountLog
	t.Cleanup(func() { probeDir, shareMounted, MountLog = oldProbe, oldMounted, oldLog })
	probeDir = func(t Target) (string, error) {
		if t.IsMount() {
			return "mkdir", &os.PathError{Op: "mkdir", Path: t.Dir(), Err: syscall.EACCES}
		}
		return oldProbe(t)
	}
	shareMounted = func(string) bool { return true }
	MountLog = func(string) string { return "Mounting x...\nMounted openccu-lite network share x." }
	made := 0
	r.p.Create = func(_ context.Context, path string) error { made++; return os.WriteFile(path, r.content, 0o600) }
	if err := r.p.CreateRun(ctx, "all"); err == nil || !strings.Contains(err.Error(), share.ID) {
		t.Fatalf("the share's failure is the run's: %v", err)
	}
	res, ok := ReadResult(r.p.Store.SecretDir(share.ID))
	if !ok || res.OK || res.State != StateReadOnly || res.Step != "mkdir" || !strings.Contains(res.Error, "permission denied") {
		t.Fatalf("share: %+v", res)
	}
	if made != 1 {
		t.Fatalf("made %d", made)
	}
	if res, _ := ReadResult(r.p.Store.SecretDir(DirectoryID)); !res.OK {
		t.Fatalf("the directory target: %+v", res)
	}
	if left, _ := os.ReadDir(r.usb); len(left) != 1 {
		t.Fatalf("the probe file stayed: %v", left)
	}
	// the share alone: nothing is made
	if err := r.p.CreateRun(ctx, share.ID); err == nil || made != 1 {
		t.Fatalf("%v, made %d", err, made)
	}
	// not mounted: the mount unit's reason
	probeDir = func(t Target) (string, error) {
		return "mount", errors.New("/media/net/x is not mounted (autofs: the mount failed)")
	}
	shareMounted = func(string) bool { return false }
	MountLog = func(string) string { return "Mounting x...\nmount.nfs: access denied by server\nFailed to mount x." }
	_ = r.p.CreateRun(ctx, share.ID)
	if res, _ := ReadResult(r.p.Store.SecretDir(share.ID)); res.State != StateAuthFailed || res.Step != "mount" || made != 1 {
		t.Fatalf("%+v", res)
	}
	MountLog = nil
	_ = r.p.CreateRun(ctx, share.ID)
	if res, _ := ReadResult(r.p.Store.SecretDir(share.ID)); res.State != StateUnreachable {
		t.Fatalf("%+v", res)
	}
}

// B-99: on a full user partition the created list was not written and created.json.tmp stayed;
// the deliver unit said nothing. Now it fails with the reason, the .tmp is gone, and the targets'
// results stay what their deliveries were.
func TestPipelineCreatedListNotWritten(t *testing.T) {
	r := newPipeRig(t, false)
	ctx := context.Background()
	if err := r.p.CreateRun(ctx, "all"); err != nil {
		t.Fatal(err)
	}
	// a created.json the rename cannot replace stands in for the disk that filled
	list := filepath.Join(r.p.Crypt.Dir, backupcrypt.KeyDir, backupcrypt.CreatedFile)
	if err := os.MkdirAll(filepath.Join(list, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := r.p.DeliverRun(ctx, "all")
	if err == nil || !strings.Contains(err.Error(), "the list of backups this system made is not written") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Lstat(list + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("created.json.tmp left behind (%v)", err)
	}
	for _, id := range []string{DirectoryID, r.sftp.ID} {
		if res, ok := ReadResult(r.p.Store.SecretDir(id)); !ok || !res.OK {
			t.Fatalf("%s: %+v", id, res)
		}
	}
	if left, _ := os.ReadDir(r.p.Staging); len(left) != 0 {
		t.Fatalf("staging not emptied: %v", left)
	}
}
