package priv

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// B-59: the helper answers where /proc/<pid>/exe leads - for exactly that shape, and nothing else.
func TestProcExe(t *testing.T) {
	c, sock := logListHelper(t, t.TempDir(), nil)
	want, err := os.Readlink(ProcExePath(os.Getpid()))
	if err != nil {
		t.Skip("no /proc here")
	}
	if got, err := c.ProcExe(os.Getpid()); err != nil || got != want {
		t.Fatalf("%q %v, want %q", got, err, want)
	}
	for _, bad := range []string{"/proc/self/exe", "/proc/1/cwd", "/proc/1/../1/exe", "/proc/01/exe", "/etc/passwd", "/proc/1/exe/"} {
		res, _ := rawLogList(t, sock, request{Op: opProcExe, Path: bad})
		if res.OK || !strings.Contains(res.Error, "refused") && !strings.Contains(res.Error, "procexe") {
			t.Errorf("%s: %+v", bad, res)
		}
	}
	if res, _ := rawLogList(t, sock, request{Op: opProcExe, Path: ProcExePath(1), Name: "x"}); res.OK || !strings.Contains(res.Error, "nothing else") {
		t.Errorf("with a name: %+v", res)
	}
	if _, err := c.ProcExe(999999999); err == nil || errors.Is(err, ErrRefused) {
		t.Errorf("a pid that is not there: %v", err)
	}
}
