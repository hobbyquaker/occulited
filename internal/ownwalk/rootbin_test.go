package ownwalk

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// occulited task 136: an addon's runtime.bin is root's - every entry 0:0, directories 0755, files
// 0755 or 0644 by their execute bit, no setuid and no group or world write - and a dry run after it
// finds the directory clean.
func TestRootBin(t *testing.T) {
	home := filepath.Join(t.TempDir(), "tools")
	for _, d := range []string{"bin/sub", "lib", "data"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]os.FileMode{"bin/htop": 0o4777, "bin/README": 0o666, "bin/sub/x": 0o700, "lib/libx.so": 0o755, "data/state": 0o600}
	for rel, m := range files {
		p := filepath.Join(home, rel)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(home, "bin"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("htop", filepath.Join(home, "bin/top2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/x", filepath.Join(home, "bin/x")); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	ownAll(t, f, home)
	opt := Options{Adopt: true, hooks: f.hooks()}
	res := RootBin(home, "bin", opt)
	if p := res.Problem(); p != "" {
		t.Fatalf("problem: %s", p)
	}
	for rel, want := range map[string]os.FileMode{"bin": 0o755, "bin/sub": 0o755, "bin/htop": 0o755, "bin/README": 0o644, "bin/sub/x": 0o755} {
		st, err := os.Lstat(filepath.Join(home, rel))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != want {
			t.Errorf("%s: mode %v, want %v", rel, st.Mode(), want)
		}
		if f.times(t, filepath.Join(home, rel)) != 1 {
			t.Errorf("%s: not given to root", rel)
		}
		if uid, gid := f.hooks().owner(statPath(t, filepath.Join(home, rel))); uid != 0 || gid != 0 {
			t.Errorf("%s: owner %d:%d", rel, uid, gid)
		}
	}
	// outside bin nothing changed
	for _, rel := range []string{"lib/libx.so", "data/state", "lib", "data", "."} {
		if f.times(t, filepath.Join(home, rel)) != 0 {
			t.Errorf("%s was changed", rel)
		}
	}
	if st, _ := os.Stat(filepath.Join(home, "data/state")); st.Mode().Perm() != 0o600 {
		t.Errorf("data/state mode %v", st.Mode())
	}
	// the dry run after it: clean
	dry := opt
	dry.DryRun, dry.Adopt = true, false
	if r := RootBin(home, "bin", dry); r.Wrong != 0 || r.Problem() != "" {
		t.Errorf("not clean after the walk: %+v", r)
	}
	// a file the addon's user made after the install: without Adopt an error, left as it is
	newFile := filepath.Join(home, "bin/planted")
	if err := os.WriteFile(newFile, []byte("x"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(newFile, 0o777); err != nil {
		t.Fatal(err)
	}
	f.set(t, newFile, addonUID, addonGID)
	noAdopt := opt
	noAdopt.Adopt = false
	if r := RootBin(home, "bin", noAdopt); !strings.Contains(r.Problem(), "planted: owned by 30007, not root") || f.times(t, newFile) != 0 {
		t.Errorf("a planted file: %+v", r)
	}
	if st, _ := os.Stat(newFile); st.Mode().Perm() != 0o777 {
		t.Errorf("a planted file's mode was changed: %v", st.Mode())
	}
	// a dry run on a fresh tree is not clean, and changes nothing
	other := filepath.Join(t.TempDir(), "o")
	if err := os.MkdirAll(filepath.Join(other, "bin"), 0o775); err != nil {
		t.Fatal(err)
	}
	fo := newFake()
	ownAll(t, fo, other)
	if r := RootBin(other, "bin", Options{DryRun: true, hooks: fo.hooks()}); r.Wrong == 0 {
		t.Errorf("a group-writable directory of the user counted clean: %+v", r)
	}
}

// ownAll gives every entry below dir (links aside) to the addon's user in the fake layer, so the
// tests hold as root too, where everything they make is root's already.
func ownAll(t *testing.T, f *fakeOwners, dir string) {
	t.Helper()
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			f.set(t, p, addonUID, addonGID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func statPath(t *testing.T, path string) Stat {
	t.Helper()
	fd, err := syscall.Open(path, 0x200000|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0) // O_PATH
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	st, err := statFD(fd)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// What RootBin refuses: a bin path that is a link anywhere, leaves the tree, is not a directory; and
// inside bin a link that may lead out, a fifo, a file linked twice without protected_hardlinks.
func TestRootBinRefuses(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "tools")
	for _, d := range []string{"real/bin", "usr", "outside"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real/bin", filepath.Join(home, "bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../real", filepath.Join(home, "usr/lnk")); err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{"bin", "usr/lnk/bin", "file", "missing", "../tools/real/bin", "/etc", ".", "real/../real/bin"} {
		f := newFake()
		r := RootBin(home, bin, Options{hooks: f.hooks()})
		if len(r.Refused) == 0 {
			t.Errorf("%s: not refused: %+v", bin, r)
		}
		if len(f.chowned) != 0 {
			t.Errorf("%s: changed something", bin)
		}
	}
	for name, mk := range map[string]func(p string) error{
		"abs":  func(p string) error { return os.Symlink("/bin/sh", p) },
		"up":   func(p string) error { return os.Symlink("../outside/x", p) },
		"deep": func(p string) error { return os.Symlink("a/../../x", p) },
		"fifo": func(p string) error { return syscall.Mkfifo(p, 0o644) },
	} {
		dir := filepath.Join(home, "real/bin")
		p := filepath.Join(dir, name)
		if err := mk(p); err != nil {
			t.Fatal(err)
		}
		r := RootBin(home, "real/bin", Options{Adopt: true, hooks: newFake().hooks()})
		if r.ErrorCount == 0 || !strings.Contains(r.Problem(), name) {
			t.Errorf("%s: not an error: %+v", name, r)
		}
		os.Remove(p)
	}
	// a file with two names, fs.protected_hardlinks off
	p := filepath.Join(home, "real/bin/two")
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(p, filepath.Join(home, "outside/two")); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	h := f.hooks()
	h.protectedHardlinks = func() bool { return false }
	if r := RootBin(home, "real/bin", Options{Adopt: true, hooks: h}); r.ErrorCount == 0 || f.times(t, p) != 0 {
		t.Errorf("a file with two names: %+v", r)
	}
	// with it on, the file is root's like any other
	f = newFake()
	ownAll(t, f, home)
	if r := RootBin(home, "real/bin", Options{Adopt: true, hooks: f.hooks()}); r.Problem() != "" || f.times(t, p) != 1 {
		t.Errorf("protected_hardlinks on: %+v", r)
	}
}

// The hand-back to the addon's user leaves an excluded directory - runtime.bin - as it is: neither
// changed nor entered.
func TestOwnExclude(t *testing.T) {
	home := filepath.Join(t.TempDir(), "tools")
	for _, d := range []string{"bin/sub", "lib"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"bin/htop", "bin/sub/x", "lib/y", "binary"} {
		if err := os.WriteFile(filepath.Join(home, rel), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := newFake()
	// everything is root's, as RootBin left it
	for _, rel := range []string{".", "bin", "bin/sub", "bin/htop", "bin/sub/x", "lib", "lib/y", "binary"} {
		f.set(t, filepath.Join(home, rel), 0, 0)
	}
	opt := Options{UID: addonUID, GID: addonGID, Exclude: []string{home + "/bin"}, hooks: f.hooks()}
	res := Own([]string{home}, opt)
	if res.Problem() != "" || res.Excluded != 1 {
		t.Fatalf("%+v", res)
	}
	for _, rel := range []string{"bin", "bin/sub", "bin/htop", "bin/sub/x"} {
		if f.times(t, filepath.Join(home, rel)) != 0 {
			t.Errorf("%s was given to the addon", rel)
		}
	}
	for _, rel := range []string{".", "lib", "lib/y", "binary"} {
		if f.times(t, filepath.Join(home, rel)) != 1 {
			t.Errorf("%s was not given to the addon", rel)
		}
	}
	// a quick dry run counts nothing wrong in bin
	dry := opt
	dry.DryRun, dry.MaxDepth = true, QuickDepth
	if r := Own([]string{home}, dry); r.RootOwned != 0 {
		t.Errorf("bin counted as root-owned: %+v", r)
	}
	// an excluded top directory is not walked at all
	if r := Own([]string{home + "/bin"}, opt); r.Checked != 0 || r.Excluded != 1 {
		t.Errorf("excluded top: %+v", r)
	}
}

// As root: after RootBin the addon's user (uid 30007, owner of the rest of the tree) can neither
// create, replace, rename, remove nor change anything in bin. Skipped without root; run on a lab
// system or a root CI.
func TestRootBinAsRoot(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("OWNWALK_NAMESPACE") != "" {
		t.Skip("root only: acts as uid 30007")
	}
	base := t.TempDir()
	// the test's directory and the one Go makes above it are 0700: the addon's user must reach home
	for _, d := range []string{filepath.Dir(base), base} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(base, "tools")
	if err := os.MkdirAll(filepath.Join(home, "bin/sub"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bin/htop"), []byte("#!/bin/sh\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	// the addon's user had the whole tree, as the hand-back leaves it
	for _, p := range []string{home, home + "/bin", home + "/bin/sub", home + "/bin/htop"} {
		if err := os.Chown(p, addonUID, addonGID); err != nil {
			t.Fatal(err)
		}
	}
	if r := RootBin(home, "bin", Options{Adopt: true}); r.Problem() != "" || r.Fixed != 3 {
		t.Fatalf("%+v", r)
	}
	for _, cmd := range []string{
		"touch bin/new", "echo x > bin/htop", "echo x >> bin/htop", "mv bin/htop bin/x", "rm -f bin/htop",
		"chmod 0777 bin/htop", "chmod 0777 bin", "ln -s /bin/sh bin/sh2", "touch bin/sub/new", "mkdir bin/d",
	} {
		c := exec.Command("/bin/sh", "-c", cmd)
		c.Dir = home
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: addonUID, Gid: addonGID}}
		out, err := c.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			// no shell ran as the user at all, so nothing was tried: that proves nothing
			t.Fatalf("%s: the shell did not run as the addon's user: %v %s", cmd, err, out)
		}
		if !strings.Contains(string(out), "ermission denied") && !strings.Contains(string(out), "not permitted") {
			t.Errorf("%s: failed, but not for want of permission: %s", cmd, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, "bin/htop")); string(b) != "#!/bin/sh\n" {
		t.Errorf("htop changed: %q", b)
	}
	// moved aside in its user's parent and replaced: the next walk without Adopt does not take the
	// user's directory
	c := exec.Command("/bin/sh", "-c", "mv bin bin.old && mkdir bin && echo evil > bin/htop && chmod 0755 bin/htop")
	c.Dir = home
	c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: addonUID, Gid: addonGID}}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("the swap: %v %s", err, out)
	}
	if r := RootBin(home, "bin", Options{}); r.Problem() == "" {
		t.Errorf("the user's own bin was taken: %+v", r)
	}
	if st, _ := os.Stat(filepath.Join(home, "bin/htop")); st.Sys().(*syscall.Stat_t).Uid != addonUID {
		t.Error("the planted htop was given to root")
	}
}
