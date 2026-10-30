package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWrite(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, path string)
		wantErr error // nil: success
		want    string
	}{
		{name: "new file", want: "new"},
		{name: "replaces", prepare: func(t *testing.T, path string) { write(t, path, "old") }, want: "new"},
		{name: "a stale .tmp is replaced", prepare: func(t *testing.T, path string) { write(t, path+".tmp", "stale") }, want: "new"},
		{
			// the .tmp's write hits a full disk as on ova-1: the file is created, the write
			// answers ENOSPC, and the empty .tmp would stay
			name: "full disk",
			prepare: func(t *testing.T, path string) {
				write(t, path, "old")
				writeFile = func(name string, _ []byte, mode os.FileMode) error {
					if err := os.WriteFile(name, nil, mode); err != nil {
						return err
					}
					return &os.PathError{Op: "write", Path: name, Err: syscall.ENOSPC}
				}
				t.Cleanup(func() { writeFile = os.WriteFile })
			},
			wantErr: syscall.ENOSPC, want: "old",
		},
		{
			// the rename fails: path is a directory that is not empty
			name: "rename fails",
			prepare: func(t *testing.T, path string) {
				if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: errAny,
		},
		{
			name:    "no directory",
			prepare: func(t *testing.T, path string) { _ = os.Remove(filepath.Dir(path)) },
			wantErr: os.ErrNotExist,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "d")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "created.json")
			if c.prepare != nil {
				c.prepare(t, path)
			}
			err := Write(path, []byte("new"), 0o600)
			switch {
			case c.wantErr == nil && err != nil:
				t.Fatalf("err %v", err)
			case c.wantErr != nil && err == nil:
				t.Fatal("no error")
			case c.wantErr != nil && c.wantErr != errAny && !errors.Is(err, c.wantErr):
				t.Fatalf("err %v, want %v", err, c.wantErr)
			}
			if _, err := os.Lstat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf(".tmp left behind (%v)", err)
			}
			if c.want != "" {
				if b, err := os.ReadFile(path); err != nil || string(b) != c.want {
					t.Errorf("content %q %v, want %q", b, err, c.want)
				}
			}
			if c.wantErr == nil {
				if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
					t.Errorf("mode %v %v", st, err)
				}
			}
		})
	}
}

var errAny = errors.New("any")

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
