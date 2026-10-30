package system

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func zipPadded(names ...string) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, n := range names {
		f, _ := w.Create(n)
		_, _ = f.Write(bytes.Repeat([]byte("x"), 400))
	}
	_ = w.Close()
	return b.Bytes()
}

// noise is incompressible enough to keep a test archive above the 512-byte minimum
func noise(n int) []byte {
	b := make([]byte, n)
	x := uint32(2463534242)
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

func tgzWith(names ...string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		_ = tw.WriteHeader(&tar.Header{Name: n, Size: 1500, Mode: 0o644})
		_, _ = tw.Write(noise(1500))
	}
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}

func mbrImage(parts int, thirdType byte) []byte {
	b := make([]byte, 4096)
	for i := 0; i < parts; i++ {
		e := b[446+16*i:]
		e[4] = 0x0c
		if i == 2 {
			e[4] = thirdType
		}
		binary.LittleEndian.PutUint32(e[8:], uint32(2048*(i+1)))
		binary.LittleEndian.PutUint32(e[12:], 2048)
	}
	b[510], b[511] = 0x55, 0xAA
	return b
}

func TestStageSystemUpdate(t *testing.T) {
	r := rootWith(t, map[string]string{"VERSION": "VERSION=3.89.8.20260719\nPRODUCT=ova\nPLATFORM=ova\n", "usr/local/.keep": ""})
	if r.StagedSystemUpdate() != nil {
		t.Fatal("nothing staged yet")
	}
	u, err := r.StageSystemUpdate(context.Background(), "OpenCCU-3.89.8.20260906-x86_64-ova.zip", 0, bytes.NewReader(zipPadded("OpenCCU-3.89.8.20260906-x86_64-ova.img", "EULA.en", "EULA.de", "LICENSE")))
	if err != nil {
		t.Fatal(err)
	}
	if u.Kind != "zip" || u.Version != "3.89.8.20260906" || u.Board != "x86_64-ova" || u.Warning != "" || u.RecoveryArmed {
		t.Fatalf("%+v", u)
	}
	link, _ := os.Readlink(r.join("/usr/local/.firmwareUpdate"))
	if link != "/usr/local/tmp/OpenCCU-3.89.8.20260906-x86_64-ova.zip" {
		t.Errorf("link %q", link)
	}
	got := r.StagedSystemUpdate()
	if got == nil || got.File != u.File || got.Kind != "zip" || got.Size == 0 {
		t.Fatalf("%+v", got)
	}
	if err := r.ArmSystemUpdate(); err != nil {
		t.Fatal(err)
	}
	if !r.StagedSystemUpdate().RecoveryArmed {
		t.Error("not armed")
	}
	// a second upload replaces the first and clears the marker
	u, err = r.StageSystemUpdate(context.Background(), "../../OpenCCU-3.89.8.20260906-rpi4.img", 0, bytes.NewReader(mbrImage(3, 0x83)))
	if err != nil {
		t.Fatal(err)
	}
	if u.Kind != "image" || u.Board != "rpi4" || !strings.Contains(u.Warning, "refuse") || u.RecoveryArmed {
		t.Fatalf("%+v", u)
	}
	if _, err := os.Stat(r.join("/usr/local/tmp/OpenCCU-3.89.8.20260906-x86_64-ova.zip")); !os.IsNotExist(err) {
		t.Error("old file kept")
	}
	if _, err := os.Stat(r.join("/usr/local/.recoveryMode")); !os.IsNotExist(err) {
		t.Error("marker kept")
	}
	r.DiscardSystemUpdate()
	if r.StagedSystemUpdate() != nil {
		t.Error("still staged")
	}
	if err := r.ArmSystemUpdate(); err == nil {
		t.Error("armed nothing")
	}
}

func TestStageSystemUpdateRejects(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/.keep": ""})
	for name, body := range map[string][]byte{
		"zip without EULA":  zipPadded("OpenCCU-x.img", "LICENSE"),
		"zip without image": zipPadded("EULA.en", "EULA.de"),
		"tgz without EULA":  tgzWith("rootfs.ext4"),
		"two partitions":    mbrImage(2, 0),
		"text":              bytes.Repeat([]byte("hello\n"), 200),
		"tiny":              []byte("PK"),
	} {
		if _, err := r.StageSystemUpdate(context.Background(), "f", 0, bytes.NewReader(body)); err == nil {
			t.Errorf("%s accepted", name)
		}
		if r.StagedSystemUpdate() != nil {
			t.Errorf("%s left a link", name)
		}
	}
	u, err := r.StageSystemUpdate(context.Background(), "", 0, bytes.NewReader(tgzWith("EULA.de", "rootfs.ext4")))
	if err != nil || u.Kind != "tar" || u.File != "firmwareUpdateFile" {
		t.Errorf("%v %+v", err, u)
	}
}

// B-256: the upload has a ceiling. A Content-Length claim over the cap is refused before anything is
// written; a body over the cap is refused even when the claim lies small (a chunked upload), and
// neither leaves a .part behind.
func TestStageSystemUpdateCap(t *testing.T) {
	old := MaxSystemUpdate
	MaxSystemUpdate = 1 << 20 // 1 MiB, so the test writes kilobytes, not gigabytes
	defer func() { MaxSystemUpdate = old }()
	staging := func(r Root) int {
		e, _ := os.ReadDir(r.join(StagingDir))
		return len(e)
	}
	// a Content-Length claim over the cap: refused before any write
	r := rootWith(t, map[string]string{"usr/local/.keep": ""})
	if _, err := r.StageSystemUpdate(context.Background(), "f", MaxSystemUpdate+1, bytes.NewReader([]byte("PK"))); err == nil {
		t.Error("a Content-Length over the cap was accepted")
	}
	if staging(r) != 0 || r.StagedSystemUpdate() != nil {
		t.Error("the over-cap claim left a file or link")
	}
	// a body over the cap with a small (lying) Content-Length, and with none at all (chunked):
	// the copy cap catches it, no .part is left
	big := bytes.Repeat([]byte("a"), int(MaxSystemUpdate)+4096)
	for _, size := range []int64{10, 0} {
		r := rootWith(t, map[string]string{"usr/local/.keep": ""})
		if _, err := r.StageSystemUpdate(context.Background(), "f", size, bytes.NewReader(big)); err == nil {
			t.Errorf("size=%d: a body over the cap was accepted", size)
		}
		if staging(r) != 0 || r.StagedSystemUpdate() != nil {
			t.Errorf("size=%d: an over-cap body left a file or link", size)
		}
	}
}

func TestReleaseNames(t *testing.T) {
	r := rootWith(t, map[string]string{"VERSION": "VERSION=3.89.8.20260719\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=0-beta.1\n"})
	for name, want := range map[string][2]string{
		"OpenCCU-3.89.8.20260719-lite.1-x86_64-ova.zip":                {"3.89.8.20260719-lite.1", "x86_64-ova"},
		"OpenCCU-3.89.8.20260719-lite.0-beta.2-x86_64-ova.zip":         {"3.89.8.20260719-lite.0-beta.2", "x86_64-ova"},
		"OpenCCU-3.89.8.20260719-lite.snapshot.abc1234-x86_64-ova.zip": {"3.89.8.20260719-lite.snapshot.abc1234", "x86_64-ova"},
		"OpenCCU-3.89.8.20260719-ova.zip":                              {"3.89.8.20260719", "ova"},
		"OpenCCU-3.89.8.20260719-lite.0-beta.1-ova-lite-systemd.zip":   {"3.89.8.20260719-lite.0-beta.1", "ova-lite-systemd"}, // beta.1, before D-39/D-43
		"OpenCCU-3.89.8.20260719-lite.0-beta.2-aarch64-rpi3.img":       {"3.89.8.20260719-lite.0-beta.2", "aarch64-rpi3"},
		// D-44
		"openccu-lite-x86_64-ova-1.0.0-alpha.0.zip":                  {"1.0.0-alpha.0", "x86_64-ova"},
		"openccu-lite-aarch64-rpi3-1.0.0-alpha.1-ccu3.tgz":           {"1.0.0-alpha.1", "aarch64-rpi3"},
		"openccu-lite-aarch64-rpi4-1.2.0.img":                        {"1.2.0", "aarch64-rpi4"},
		"openccu-lite-x86_64-ova-1.0.0-alpha.0-snapshot.abc1234.zip": {"1.0.0-alpha.0-snapshot.abc1234", "x86_64-ova"},
		"OpenCCU-3.89.8.20260719-rpi4-lite.img":                      {"3.89.8.20260719", "rpi4-lite"},
	} {
		v, b, w := r.describeRelease(name)
		if v != want[0] || b != want[1] {
			t.Errorf("%s: %q %q", name, v, b)
		}
		// this box is PRODUCT=ova: every ova file is for it, the two Pi ones are not (B-18)
		wantWarn := !strings.Contains(b, "ova")
		if (w != "") != wantWarn {
			t.Errorf("%s: warning %q, wanted one: %v", name, w, wantWarn)
		}
	}
	// the boards whose name and /VERSION disagree on purpose (B-18): upstream's CCU3 update
	// package out of the rpi3 build, and the D-39/D-43 product names on top of D-31's /VERSION
	for _, c := range []struct {
		board, product string
		match          bool
	}{
		{"ccu3", "rpi3", true},
		{"aarch64-rpi3", "rpi3", true},
		{"aarch64-rpi3", "rpi4", false}, // the two board images are not interchangeable
		{"aarch64-rpi4", "rpi4", true},
		{"aarch64-rpi4", "rpi3", false},
		{"x86_64-ova", "ova", true},
		{"aarch64", "rpi4", false},         // D-43: no product is called that any more
		{"generic-aarch64", "rpi4", false}, // an alias never applies to a board that contains it
		{"rpi4", "rpi4", true},
		{"ova-lite-systemd", "ova", true}, // a beta.1 file still names its box
		{"rpi4-lite", "ova", false},
	} {
		if got := boardMatches(c.board, c.product, c.product); got != c.match {
			t.Errorf("boardMatches(%q, %q) = %v, want %v", c.board, c.product, got, c.match)
		}
	}
	if r.ReadVersion().Full() != "0-beta.1" { // D-44: the LITE line alone is the identity
		t.Error(r.ReadVersion().Full())
	}
}

func TestDetectRootfsBootfs(t *testing.T) {
	ext := make([]byte, 4096)
	binary.LittleEndian.PutUint16(ext[1024+0x38:], 0xEF53)
	copy(ext[1024+0x78:], "rootfs")
	f := t.TempDir() + "/rootfs.ext4"
	_ = os.WriteFile(f, ext, 0o644)
	if k, err := detectUpdateKind(f); err != nil || k != "rootfs" {
		t.Errorf("%v %s", err, k)
	}
	vfat := make([]byte, 4096)
	copy(vfat[71:], "bootfs     ")
	vfat[510], vfat[511] = 0x55, 0xAA
	_ = os.WriteFile(f, vfat, 0o644)
	if k, err := detectUpdateKind(f); err != nil || k != "bootfs" {
		t.Errorf("%v %s", err, k)
	}
}

// B-247: an update the recovery could not unpack is refused when it is staged, and again when it
// is armed (a nightly backup may have taken the room since); the answer says free and required.
func TestUpdateSpace(t *testing.T) {
	var free int64
	old := updateFree
	updateFree = func(string) (int64, error) { return free, nil }
	t.Cleanup(func() { updateFree = old })
	zipBody := zipPadded("openccu-lite-x86_64-ova-1.0.0.img", "EULA.en", "EULA.de", "LICENSE") // 4 × 400 bytes unpacked
	tgzBody := tgzWith("EULA.de", "rootfs.ext4")                                               // 2 × 1500
	for _, c := range []struct {
		name     string
		body     []byte
		free     int64
		required int64 // 0: fits
	}{
		{"zip fits", zipBody, 1600 + updateSpaceMargin + 1, 0},
		{"zip: the recovery's rule, not below the free space", zipBody, 1600 + updateSpaceMargin, 1600 + updateSpaceMargin},
		{"zip, far too little", zipBody, 10 << 20, 1600 + updateSpaceMargin},
		{"tgz fits", tgzBody, 3000 + updateSpaceMargin + 1, 0},
		{"tgz too big", tgzBody, 3000, 3000 + updateSpaceMargin},
		{"a disk image is not unpacked", mbrImage(3, 0x83), 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := rootWith(t, map[string]string{"usr/local/.keep": ""})
			free = c.free
			u, err := r.StageSystemUpdate(context.Background(), "openccu-lite-x86_64-ova-1.0.0.zip", 0, bytes.NewReader(c.body))
			if c.required == 0 {
				if err != nil || u == nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var se *UpdateSpaceError
			if !errors.As(err, &se) || se.Free != c.free || se.Required != c.required {
				t.Fatalf("err %v, want free %d required %d", err, c.free, c.required)
			}
			if !strings.Contains(err.Error(), "remove old backups") {
				t.Errorf("no hint: %v", err)
			}
			if r.StagedSystemUpdate() != nil {
				t.Error("staged anyway")
			}
			if left, _ := os.ReadDir(r.join("/usr/local/tmp")); len(left) != 0 {
				t.Errorf("the refused file stayed: %v", left)
			}
		})
	}
	// staged with room, armed without
	r := rootWith(t, map[string]string{"usr/local/.keep": ""})
	free = 1 << 30
	if _, err := r.StageSystemUpdate(context.Background(), "openccu-lite-x86_64-ova-1.0.0.zip", 0, bytes.NewReader(zipBody)); err != nil {
		t.Fatal(err)
	}
	free = 1 << 20
	var se *UpdateSpaceError
	if err := r.ArmSystemUpdate(); !errors.As(err, &se) {
		t.Fatalf("armed without room: %v", err)
	}
	if _, err := os.Stat(r.join("/usr/local/.recoveryMode")); !os.IsNotExist(err) {
		t.Error("the marker was set")
	}
	free = 1 << 30
	if err := r.ArmSystemUpdate(); err != nil {
		t.Fatal(err)
	}
}

// B-99: before a file of a known size is written, the room for it and for its unpacking is
// checked - on ova-1 a 1.3 GB image filled the user partition beside a nightly backup. A staged
// update counts as free (it is discarded first); the refusal writes nothing and keeps the staged one.
func TestDownloadSpace(t *testing.T) {
	var free int64
	old := updateFree
	updateFree = func(string) (int64, error) { return free, nil }
	t.Cleanup(func() { updateFree = old })
	const size = 1_300_000_000
	zipBody := zipPadded("openccu-lite-x86_64-ova-1.0.0.img", "EULA.en", "EULA.de", "LICENSE")
	for _, c := range []struct {
		name     string
		file     string
		free     int64
		staged   int64 // a staged update of that size, 0: none
		required int64 // 0: passes the check
	}{
		{"a zip: the file and its unpacking", "openccu-lite-x86_64-ova-1.0.1.zip", 2*size + updateSpaceMargin + 1, 0, 0},
		{"a zip: one byte short", "openccu-lite-x86_64-ova-1.0.1.zip", 2*size + updateSpaceMargin, 0, 2*size + updateSpaceMargin},
		{"ova-1: room for the file alone", "openccu-lite-x86_64-ova-1.0.1.zip", size + 32<<20, 0, 2*size + updateSpaceMargin},
		{"a tgz", "openccu-lite-aarch64-rpi4-1.0.1-ccu3.tgz", size + updateSpaceMargin + 1, 0, 2*size + updateSpaceMargin},
		{"a disk image is not unpacked", "openccu-lite-x86_64-ova-1.0.1.img", size + updateSpaceMargin + 1, 0, 0},
		{"the staged update's room counts", "openccu-lite-x86_64-ova-1.0.1.zip", size, int64(len(zipBody)), 2*size + updateSpaceMargin},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := rootWith(t, map[string]string{"usr/local/.keep": ""})
			if c.staged > 0 {
				free = 1 << 40
				if _, err := r.StageSystemUpdate(context.Background(), "openccu-lite-x86_64-ova-1.0.0.zip", 0, bytes.NewReader(zipBody)); err != nil {
					t.Fatal(err)
				}
			}
			free = c.free
			err := r.checkDownloadSpace(r.join(updateDir), c.file, size)
			if c.required == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var se *UpdateSpaceError
			if !errors.As(err, &se) || !se.Download || se.Free != c.free+c.staged || se.Required != c.required {
				t.Fatalf("err %#v, want free %d required %d", err, c.free+c.staged, c.required)
			}
			if !strings.Contains(err.Error(), "needed to store and unpack it") || !strings.Contains(err.Error(), "remove old backups") {
				t.Errorf("text: %v", err)
			}
		})
	}
	// through StageSystemUpdate: refused before a byte is read, the staged update kept
	r := rootWith(t, map[string]string{"usr/local/.keep": ""})
	free = 1 << 40
	if _, err := r.StageSystemUpdate(context.Background(), "openccu-lite-x86_64-ova-1.0.0.zip", 0, bytes.NewReader(zipBody)); err != nil {
		t.Fatal(err)
	}
	free = 1 << 30
	read := false
	src := readerFunc(func(p []byte) (int, error) { read = true; return 0, io.EOF })
	var se *UpdateSpaceError
	if _, err := r.StageSystemUpdate(context.Background(), "openccu-lite-x86_64-ova-1.0.1.zip", size, src); !errors.As(err, &se) || !se.Download {
		t.Fatalf("err %v", err)
	}
	if read {
		t.Error("the body was read")
	}
	if u := r.StagedSystemUpdate(); u == nil || u.File != "openccu-lite-x86_64-ova-1.0.0.zip" {
		t.Errorf("the staged update is gone: %+v", u)
	}
	if left, _ := os.ReadDir(r.join("/usr/local/tmp")); len(left) != 1 {
		t.Errorf("tmp holds %v", left)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
