package system

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"filippo.io/age"

	"github.com/hobbyquaker/occulited/internal/backupcrypt"
)

// occulited B-141: a backup upload is never cut off quietly. The tests lower MaxSBKUpload and set
// UploadReserve instead of writing gigabytes.

func lowCaps(t *testing.T, max, reserve int64) {
	t.Helper()
	oldMax, oldRes := MaxSBKUpload, UploadReserve
	MaxSBKUpload, UploadReserve = max, reserve
	t.Cleanup(func() { MaxSBKUpload, UploadReserve = oldMax, oldRes })
}

// leftovers lists the upload files under BackupDir and StagingDir.
func leftovers(t *testing.T, r Root) []string {
	t.Helper()
	var out []string
	for _, d := range []string{BackupDir, StagingDir} {
		entries, _ := os.ReadDir(r.join(d))
		for _, e := range entries {
			if n := e.Name(); strings.HasPrefix(n, "restore-") {
				out = append(out, d+"/"+n)
			}
		}
	}
	return out
}

func TestCapReader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in, max int
		err     error
	}{
		{"below", 10, 16, nil},
		{"exactly the cap", 16, 16, nil},
		{"one byte over", 17, 16, ErrUploadTooLarge},
		{"far over", 4096, 16, ErrUploadTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCapReader(bytes.NewReader(make([]byte, tc.in)), int64(tc.max))
			n, err := io.Copy(io.Discard, c)
			if !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Fatalf("err %v, want %v", err, tc.err)
			}
			if want := min(tc.in, tc.max); n != int64(want) {
				t.Errorf("passed %d bytes, want %d", n, want)
			}
			if tc.err != nil && !errors.Is(c.sourceErr(), ErrUploadTooLarge) {
				t.Errorf("sourceErr %v", c.sourceErr())
			}
			if tc.err == nil && c.sourceErr() != nil {
				t.Errorf("sourceErr %v on a clean source", c.sourceErr())
			}
		})
	}
	// a body that ends early is the incomplete upload, any other error passes as it is
	c := newCapReader(io.MultiReader(strings.NewReader("abc"), iotestErr{io.ErrUnexpectedEOF}), 100)
	if _, err := io.Copy(io.Discard, c); !errors.Is(err, io.ErrUnexpectedEOF) || c.sourceErr() != ErrUploadIncomplete {
		t.Errorf("%v %v", err, c.sourceErr())
	}
	other := errors.New("reset by peer")
	c = newCapReader(iotestErr{other}, 100)
	if _, _ = io.Copy(io.Discard, c); c.sourceErr() != other {
		t.Errorf("%v", c.sourceErr())
	}
}

type iotestErr struct{ err error }

func (e iotestErr) Read([]byte) (int, error) { return 0, e.err }

func TestCheckUploadRoom(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/tmp/.keep": ""})
	lowCaps(t, 1<<20, 0)
	if err := r.CheckUploadRoom(0); err != nil {
		t.Errorf("unknown size: %v", err)
	}
	if err := r.CheckUploadRoom(1 << 20); err != nil {
		t.Errorf("at the cap: %v", err)
	}
	// twice the file: the upload and the copy the check unpacks beside it
	if n := UploadRoomNeeded(1000); n != 2000 {
		t.Errorf("UploadRoomNeeded(1000) = %d", n)
	}
	var big *TooLargeError
	if err := r.CheckUploadRoom(1<<20 + 1); !errors.As(err, &big) || big.Size != 1<<20+1 || big.Limit != 1<<20 || !errors.Is(err, ErrUploadTooLarge) {
		t.Errorf("over the cap: %v", err)
	}
	// a reserve no filesystem has: the room is refused, with the sizes
	UploadReserve = 1 << 60
	var room *NoRoomError
	err := r.CheckUploadRoom(5000)
	if !errors.As(err, &room) || room.Size != 5000 || room.Need != 2*5000+1<<60 || room.Free <= 0 || room.Reserve != 1<<60 {
		t.Fatalf("no room: %v", err)
	}
	if !strings.Contains(err.Error(), "not enough free space") || !strings.Contains(err.Error(), "nothing was stored") {
		t.Errorf("message: %s", err)
	}
}

func TestNoRoomMapsENOSPC(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/tmp/.keep": ""})
	lowCaps(t, 1<<20, 7)
	var room *NoRoomError
	if err := r.noRoom(fmt.Errorf("write: %w", syscall.ENOSPC), 100); !errors.As(err, &room) || room.Size != 100 || room.Need != 207 {
		t.Errorf("known size: %v", err)
	}
	if err := r.noRoom(&os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}, -1); !errors.As(err, &room) || room.Size != 0 || !strings.Contains(err.Error(), "ran out of free space") {
		t.Errorf("unknown size: %v", err)
	}
	other := errors.New("other")
	if err := r.noRoom(other, 100); err != other {
		t.Errorf("other errors pass: %v", err)
	}
}

func TestSaveUploadSniffedSizes(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/tmp/.keep": ""})
	lowCaps(t, 64<<10, 0)
	body := bytes.Repeat([]byte("sbk!"), 16<<10) // exactly the cap

	// at the cap: stored whole
	up, err := r.SaveUploadSniffed(bytes.NewReader(body), "a.sbk", int64(len(body)), nil)
	if err != nil || up.Size != int64(len(body)) {
		t.Fatalf("%v %+v", err, up)
	}
	if st, _ := os.Stat(up.Path); st == nil || st.Size() != int64(len(body)) {
		t.Fatalf("stored %v", st)
	}

	over := append(bytes.Clone(body), 'x')
	for _, tc := range []struct {
		name string
		size int64
	}{{"announced", int64(len(over))}, {"in the stream", -1}} {
		t.Run("too large "+tc.name, func(t *testing.T) {
			_, err := r.SaveUploadSniffed(bytes.NewReader(over), "b.sbk", tc.size, nil)
			var big *TooLargeError
			if !errors.As(err, &big) || big.Limit != 64<<10 {
				t.Fatalf("%v", err)
			}
			// the earlier upload is swept, the cut one never kept: nothing is left to "check"
			if l := leftovers(t, r); len(l) != 0 {
				t.Errorf("left behind: %v", l)
			}
		})
	}

	t.Run("ended early", func(t *testing.T) {
		src := io.MultiReader(bytes.NewReader(body[:1000]), iotestErr{io.ErrUnexpectedEOF})
		if _, err := r.SaveUploadSniffed(src, "c.sbk", -1, nil); !errors.Is(err, ErrUploadIncomplete) {
			t.Fatalf("%v", err)
		}
		if l := leftovers(t, r); len(l) != 0 {
			t.Errorf("left behind: %v", l)
		}
	})

	t.Run("no room", func(t *testing.T) {
		UploadReserve = 1 << 60
		defer func() { UploadReserve = 0 }()
		var room *NoRoomError
		if _, err := r.SaveUploadSniffed(bytes.NewReader(body), "d.sbk", int64(len(body)), nil); !errors.As(err, &room) {
			t.Fatalf("%v", err)
		}
		if l := leftovers(t, r); len(l) != 0 {
			t.Errorf("left behind: %v", l)
		}
	})

	// an encrypted upload the system's key opens is decrypted on the way: past the cap it is the
	// cap's error, not the decryption's "corrupt" (the age reader rewraps every read error)
	t.Run("encrypted, too large in the stream", func(t *testing.T) {
		id, _ := age.GenerateX25519Identity()
		var buf bytes.Buffer
		w, err := backupcrypt.Encrypt(&buf, backupcrypt.MetaRecipient{}, id.Recipient().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(over)
		_ = w.Close()
		_, err = r.SaveUploadSniffed(bytes.NewReader(buf.Bytes()), "e.sbk.age", -1, id)
		if !errors.Is(err, ErrUploadTooLarge) || errors.Is(err, backupcrypt.ErrCorrupt) {
			t.Fatalf("%v", err)
		}
		if l := leftovers(t, r); len(l) != 0 {
			t.Errorf("left behind: %v", l)
		}
	})
}

// RestoreRoom counts what the next upload's sweep frees.
func TestRestoreRoomCountsSweepable(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/tmp/.keep": ""})
	lowCaps(t, 1<<30, 5)
	old := roomFree
	roomFree = func(string) (int64, bool) { return 1000, true }
	defer func() { roomFree = old }()
	free, reserve, max, ok := r.RestoreRoom()
	if !ok || reserve != 5 || max != 1<<30 || free != 1000 {
		t.Fatalf("%d %d %d %v", free, reserve, max, ok)
	}
	if err := os.WriteFile(r.join(BackupDir+"/restore-old.sbk"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.join(BackupDir+"/other.sbk"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if free, _, _, _ := r.RestoreRoom(); free != 1000+1<<20 {
		t.Errorf("free %d, want 1000 plus only the restore-* file", free)
	}
	roomFree = func(string) (int64, bool) { return 0, false }
	if _, _, _, ok := r.RestoreRoom(); ok {
		t.Error("an unreadable filesystem is not ok")
	}
}
