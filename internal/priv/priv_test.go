package priv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/certpem"
	"github.com/hobbyquaker/occulited/internal/certpem/testcert"
)

func TestLocalRunAndFiles(t *testing.T) {
	var ops Ops = Local{}
	r, err := ops.Run(context.Background(), "sh", []string{"-c", "echo out; echo err >&2; exit 3"}, nil)
	if err != nil || r.Exit != 3 || string(r.Stdout) != "out\n" || string(r.Stderr) != "err\n" {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := ops.Run(context.Background(), "/nonexistent/prog", nil, nil); err == nil {
		t.Error("missing program ran")
	}
	r, _ = ops.Run(context.Background(), "cat", nil, []byte("stdin"))
	if string(r.Stdout) != "stdin" {
		t.Errorf("%q", r.Stdout)
	}
	d := t.TempDir()
	p := filepath.Join(d, "sub", "f")
	if err := ops.WriteFile(p, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st.Mode())
	}
	if err := ops.Symlink("f", filepath.Join(d, "sub", "l")); err != nil {
		t.Fatal(err)
	}
	if err := ops.Symlink("f2", filepath.Join(d, "sub", "l")); err != nil {
		t.Fatal("replace link:", err)
	}
	if err := ops.Remove(filepath.Join(d, "nope")); err != nil {
		t.Error("missing path is an error")
	}
	if _, err := AsExitError(Result{Exit: 2}, nil); err == nil {
		t.Error("exit 2 not an error")
	}
}

func TestServerAllowlists(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "etc/config"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "usr/local/etc/occulite/staging"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "usr/local/tmp"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "etc/init.d"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "etc/init.d/S99x"), []byte("#!/bin/sh\necho init $1\n"), 0o755)
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite")}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}

	// allowed program by name, by directory; refused program. B-234: a program runs with the
	// arguments of its shape only - hostname takes exactly the new name (its bare call is not a
	// form the daemon has), cronBackup.sh takes nothing
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "bin/cronBackup.sh"), []byte("#!/bin/sh\necho cron\n"), 0o755)
	if r, err := c.Run(context.Background(), filepath.Join(root, "bin/cronBackup.sh"), nil, nil); err != nil || r.Exit != 0 || string(r.Stdout) != "cron\n" {
		t.Errorf("cronBackup.sh: %v %+v", err, r)
	}
	if _, err := c.Run(context.Background(), filepath.Join(root, "bin/cronBackup.sh"), []string{"--help"}, nil); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("cronBackup.sh with an argument: %v", err)
	}
	if _, err := c.Run(context.Background(), "hostname", nil, nil); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("hostname without a name: %v", err)
	}
	if r, err := c.Run(context.Background(), filepath.Join(root, "etc/init.d/S99x"), []string{"start"}, nil); err != nil || string(r.Stdout) != "init start\n" {
		t.Errorf("init script: %v %+v", err, r)
	}
	// B-144: the standard input crosses the socket - the restore script reads the key from it
	_ = os.WriteFile(filepath.Join(root, "bin/restoreBackup.sh"), []byte("#!/bin/sh\nset -e\nread -r KEY\necho \"key=[$KEY]\"\n"), 0o755)
	sbk := filepath.Join(root, "usr/local/tmp/x.sbk")
	if r, err := c.Run(context.Background(), filepath.Join(root, "bin/restoreBackup.sh"), []string{"-c", sbk}, []byte("s3cret\n")); err != nil || r.Exit != 0 || string(r.Stdout) != "key=[s3cret]\n" {
		t.Errorf("restore with stdin: %v %+v", err, r)
	}
	if r, err := c.Run(context.Background(), filepath.Join(root, "bin/restoreBackup.sh"), []string{"-c", sbk}, nil); err != nil || r.Exit == 0 {
		t.Errorf("restore without stdin must meet EOF: %v %+v", err, r)
	}
	if _, err := c.Run(context.Background(), "rm", []string{"-rf", "/"}, nil); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("rm allowed: %v", err)
	}
	// every firmware script the daemon runs is on the list; cronBackup.sh was not (B-16); the
	// firewall script left it with task 157
	if DefaultPolicy("/", "/usr/local/etc/occulite").programAllowed("/bin/setfirewall.tcl", nil) {
		t.Error("setfirewall.tcl is still on the program list")
	}
	for prog, args := range map[string][]string{
		"/bin/createBackup.sh": {"/usr/local/tmp/b.sbk"}, "/bin/cronBackup.sh": nil, "/bin/restoreBackup.sh": {"/usr/local/tmp/b.sbk"},
		"/bin/updateTZ.sh": nil, "/bin/SetInterfaceClock": {"127.0.0.1:2001"}, "/bin/install_addon": nil} {
		if !DefaultPolicy("/", "/usr/local/etc/occulite").programAllowed(prog, args) {
			t.Errorf("%s %v is not on the program list", prog, args)
		}
	}
	if _, err := c.Run(context.Background(), "sh", []string{"-c", "id"}, nil); err == nil {
		t.Error("arbitrary sh allowed")
	}
	// paths
	if err := c.WriteFile(filepath.Join(root, "etc/config/rfd.conf"), []byte("x"), 0o644); err != nil {
		t.Errorf("write: %v", err)
	}
	if err := c.WriteFile(filepath.Join(root, "etc/passwd"), []byte("x"), 0o644); err == nil {
		t.Error("write to /etc/passwd allowed")
	}
	if err := c.WriteFile(filepath.Join(root, "etc/config/../shadow"), []byte("x"), 0o644); err == nil {
		t.Error("traversal allowed")
	}
	if err := c.Touch(filepath.Join(root, "usr/local/.recoveryMode"), 0o644); err != nil {
		t.Errorf("touch: %v", err)
	}
	// task 109: the factory reset's marker, listed like the recovery's
	if err := c.Touch(filepath.Join(root, "usr/local/.doFactoryReset"), 0o644); err != nil {
		t.Errorf("touch of the factory reset marker: %v", err)
	}
	if err := c.Symlink("/usr/local/tmp/x", filepath.Join(root, "usr/local/.firmwareUpdate")); err != nil {
		t.Errorf("symlink: %v", err)
	}
	// the allowlisted directory itself, not only what is under it (B-6)
	if err := c.MkdirAll(filepath.Join(root, "usr/local/tmp"), 0o755); err != nil {
		t.Errorf("mkdir of the prefix directory: %v", err)
	}
	// a sibling with the same prefix is not the prefix: no write there. (mkdir of such a
	// sibling is an addon data directory since D-52 - TestAddonDataDirPolicy - so the prefix
	// rule is checked with the write, which the data-directory grant does not cover)
	if err := c.WriteFile(filepath.Join(root, "usr/local/tmpx"), []byte("x"), 0o644); err == nil {
		t.Error("a sibling with the same prefix allowed")
	}
	if err := c.RemoveAll(filepath.Join(root, "usr/local/tmpx")); err == nil {
		t.Error("removal of a sibling with the same prefix allowed")
	}
	// staging -> allowed destination
	st := filepath.Join(root, "usr/local/etc/occulite/staging/new.tar.gz")
	_ = os.WriteFile(st, []byte("big"), 0o644)
	if err := c.Rename(st, filepath.Join(root, "usr/local/tmp/new_addon.tar.gz")); err != nil {
		t.Errorf("rename: %v", err)
	}
	if err := c.Rename(filepath.Join(root, "etc/passwd"), filepath.Join(root, "usr/local/tmp/p")); err == nil {
		t.Error("rename from outside allowed")
	}
	if err := c.Remove(filepath.Join(root, "usr/local/tmp/new_addon.tar.gz")); err != nil {
		t.Errorf("remove: %v", err)
	}
	// root-only reads: the ReGa database yes, anything else no
	_ = os.WriteFile(filepath.Join(root, "etc/config/homematic.regadom"), []byte("<xml/>"), 0o600)
	if b, err := c.ReadFile(filepath.Join(root, "etc/config/homematic.regadom")); err != nil || string(b) != "<xml/>" {
		t.Errorf("read regadom: %v %q", err, b)
	}
	// netconfig and the interface configs are on the list too (B-13), the password file is not
	_ = os.WriteFile(filepath.Join(root, "etc/config/netconfig"), []byte("MODE=DHCP\n"), 0o600)
	if b, err := c.ReadFile(filepath.Join(root, "etc/config/netconfig")); err != nil || string(b) != "MODE=DHCP\n" {
		t.Errorf("read netconfig: %v %q", err, b)
	}
	if _, err := c.ReadFile(filepath.Join(root, "etc/config/shadow")); err == nil {
		t.Error("read outside the read list allowed")
	}
	// a context deadline ends a hanging call
	dctx, dcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer dcancel()
	if _, err := c.Run(dctx, filepath.Join(root, "etc/init.d/S99x"), []string{"$(sleep 5)"}, nil); err != nil && !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "refused") {
		t.Logf("deadline run: %v", err)
	}
}

// occulited B-139: the shell was the sharpest edge of the boundary (a prefix test on "tar -xOf "
// let everything after the prefix run as root, task 17), and its last three exact command lines
// are gone - the backup listing moved into the daemon (task 142), the LED trigger and the IPv6
// sysctls are typed operations. sh is not a program the helper runs, in any form.
func TestPolicyHasNoShell(t *testing.T) {
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	for _, prog := range []string{"sh", "/bin/sh", "bash", "/bin/bash", "ash"} {
		for _, args := range [][]string{nil, {"-c"}, {"-c", "id"},
			{"-c", "echo 'heartbeat' > '/sys/class/leds/ACT/trigger'"},
			{"-c", "echo '1' > '/proc/sys/net/ipv6/conf/eth0/disable_ipv6'"},
			{"-c", "tar -xOf '/usr/local/tmp/x.sbk' usr_local.tar.gz 2>/dev/null | tar -tzf - 2>/dev/null | grep -c 'etc/config/homematic.regadom$'"}} {
			if p.programAllowed(prog, args) {
				t.Errorf("allowed: %s %q", prog, args)
			}
		}
	}
	if slices.Contains(p.Programs, "sh") {
		t.Error("sh is on the program list")
	}
	// the LED class directory left Paths with the shell redirect: the led operation checks its
	// own directory and names, and a sysfs attribute is never renamed into place
	if p.pathAllowed("/sys/class/leds/ACT/trigger") {
		t.Error("/sys/class/leds is still a write path")
	}
}

// occulited B-139: the ipv6-conf operation - an interface, one of three keys, 0 to 2, written in
// place; every other shape is refused at the boundary and never reaches the file.
func TestIPv6ConfOperation(t *testing.T) {
	dir := t.TempDir()
	old := ipv6ConfDir
	ipv6ConfDir = dir
	t.Cleanup(func() { ipv6ConfDir = old })
	for _, i := range []string{"eth0", "wlan0", "eth0.10", "all", "lo"} {
		_ = os.MkdirAll(filepath.Join(dir, i), 0o755)
		for _, k := range []string{"disable_ipv6", "accept_ra", "autoconf", "forwarding"} {
			_ = os.WriteFile(filepath.Join(dir, i, k), []byte("9\n"), 0o644)
		}
	}
	var logged []string
	srv := &Server{Policy: DefaultPolicy("/", "/usr/local/etc/occulite"), Log: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	cases := []struct {
		name string
		req  request
		ok   bool
		file string // the file that must hold the value afterwards ("" = none written)
	}{
		{"disable", request{Op: opIPv6Conf, Name: "eth0", Target: "disable_ipv6", Data: []byte("1")}, true, "eth0/disable_ipv6"},
		{"accept_ra off", request{Op: opIPv6Conf, Name: "wlan0", Target: "accept_ra", Data: []byte("0")}, true, "wlan0/accept_ra"},
		{"accept_ra 2", request{Op: opIPv6Conf, Name: "eth0", Target: "accept_ra", Data: []byte("2")}, true, "eth0/accept_ra"},
		{"a VLAN", request{Op: opIPv6Conf, Name: "eth0.10", Target: "autoconf", Data: []byte("0")}, true, "eth0.10/autoconf"},
		{"all", request{Op: opIPv6Conf, Name: "all", Target: "disable_ipv6", Data: []byte("1")}, false, ""},
		{"lo", request{Op: opIPv6Conf, Name: "lo", Target: "disable_ipv6", Data: []byte("1")}, false, ""},
		{"traversal", request{Op: opIPv6Conf, Name: "..", Target: "disable_ipv6", Data: []byte("1")}, false, ""},
		{"another key", request{Op: opIPv6Conf, Name: "eth0", Target: "forwarding", Data: []byte("1")}, false, ""},
		{"a path as key", request{Op: opIPv6Conf, Name: "eth0", Target: "../../ipv4/ip_forward", Data: []byte("1")}, false, ""},
		{"value 3", request{Op: opIPv6Conf, Name: "eth0", Target: "accept_ra", Data: []byte("3")}, false, ""},
		{"value -1", request{Op: opIPv6Conf, Name: "eth0", Target: "accept_ra", Data: []byte("-1")}, false, ""},
		{"value text", request{Op: opIPv6Conf, Name: "eth0", Target: "accept_ra", Data: []byte("1; id")}, false, ""},
		{"no value", request{Op: opIPv6Conf, Name: "eth0", Target: "accept_ra"}, false, ""},
		{"a path besides", request{Op: opIPv6Conf, Name: "eth0", Target: "accept_ra", Data: []byte("1"), Path: "/etc/passwd"}, false, ""},
	}
	for _, c := range cases {
		logged = nil
		r := srv.do(context.Background(), c.req)
		if c.ok != r.OK {
			t.Errorf("%s: %+v", c.name, r)
			continue
		}
		if !c.ok {
			if !strings.HasPrefix(r.Error, "refused") || len(logged) != 1 {
				t.Errorf("%s: not a logged refusal: %+v %v", c.name, r, logged)
			}
			continue
		}
		if b, _ := os.ReadFile(filepath.Join(dir, c.file)); string(b) != string(c.req.Data) {
			t.Errorf("%s: %s holds %q", c.name, c.file, b)
		}
	}
	// nothing a refusal named was written
	for _, f := range []string{"all/disable_ipv6", "lo/disable_ipv6", "eth0/forwarding"} {
		if b, _ := os.ReadFile(filepath.Join(dir, f)); string(b) != "9\n" {
			t.Errorf("%s written: %q", f, b)
		}
	}
	// the operation checks again on its own
	for _, c := range []struct {
		iface, key string
		v          int
	}{{"all", "accept_ra", 1}, {"eth0", "forwarding", 1}, {"eth0", "accept_ra", 3}} {
		if err := (Local{}).SetIPv6Conf(context.Background(), c.iface, c.key, c.v); err == nil {
			t.Errorf("Local wrote %s %s=%d", c.iface, c.key, c.v)
		}
	}
}

// B-35: the helper opens the file as root and passes the descriptor back over the socket. The
// test exercises the mechanism itself - a real unix socket, SCM_RIGHTS, a file streamed from the
// descriptor - and the refusals around it, which are the half that matters: the allowlist is a
// directory, and everything an addon can plant inside it must not lead out of it.
func TestOpenPassesDescriptor(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"usr/local/tmp", "etc/config", "etc/secret"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(root, "usr/local/tmp/backup.sbk")
	if err := os.WriteFile(archive, []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "etc/config/homematic.regadom"), []byte("<xml/>"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "etc/secret/x"), []byte("secret"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "etc/passwd"), []byte("root:x:0:0:::\n"), 0o644)

	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite")}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}

	f, err := c.Open(archive)
	if err != nil {
		t.Fatalf("open the archive: %v", err)
	}
	got, err := io.ReadAll(f)
	if err != nil || string(got) != "archive bytes" {
		t.Errorf("streamed %q %v", got, err)
	}
	// it is a descriptor of its own, seekable - what http.ServeContent needs for a Range
	if _, err := f.Seek(8, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(f)
	if string(rest) != "bytes" {
		t.Errorf("seek: %q", rest)
	}
	if st, err := f.Stat(); err != nil || st.Size() != 13 {
		t.Errorf("stat through the descriptor: %v %v", st, err)
	}
	f.Close()

	// the two allowlists stay separate: what may be streamed is not what may be read, and
	// widening ReadPaths to /usr/local/tmp is exactly what B-35 decided against
	if _, err := c.ReadFile(archive); err == nil || !errors.Is(err, ErrRefused) {
		t.Errorf("the archive was readable through the read operation: %v", err)
	}
	// and the read list is not an open list either
	if _, err := c.Open(filepath.Join(root, "etc/config/homematic.regadom")); err == nil || !errors.Is(err, ErrRefused) {
		t.Errorf("a ReadPaths file was opened: %v", err)
	}

	// refusals: outside the allowlist, the directory itself, a traversal out of it
	for _, bad := range []string{
		filepath.Join(root, "etc/passwd"),
		filepath.Join(root, "usr/local/tmp"),
		filepath.Join(root, "usr/local/tmp/../../../etc/passwd"),
		root + "/usr/local/tmp/../../etc/passwd",
		"/etc/passwd",
		filepath.Join(root, "usr/local/tmpx/f"),
	} {
		f, err := c.Open(bad)
		if err == nil {
			f.Close()
			t.Errorf("opened %s", bad)
			continue
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v (expected a refusal)", bad, err)
		}
	}

	// a symlink an addon dropped in the directory: O_NOFOLLOW stops the last component ...
	link := filepath.Join(root, "usr/local/tmp/link")
	if err := os.Symlink(filepath.Join(root, "etc/secret/x"), link); err != nil {
		t.Fatal(err)
	}
	if f, err := c.Open(link); err == nil {
		b, _ := io.ReadAll(f)
		f.Close()
		t.Errorf("a symlink out of the directory was followed: %q", b)
	}
	// ... and a symlinked *directory* in the middle, which no test of the requested path can
	// see, is caught by resolving the descriptor itself
	if err := os.Symlink(filepath.Join(root, "etc/secret"), filepath.Join(root, "usr/local/tmp/sub")); err != nil {
		t.Fatal(err)
	}
	if f, err := c.Open(filepath.Join(root, "usr/local/tmp/sub/x")); err == nil {
		b, _ := io.ReadAll(f)
		f.Close()
		t.Errorf("a symlinked directory led out of the allowlist: %q", b)
	} else if !errors.Is(err, ErrRefused) {
		t.Errorf("symlinked directory: %v (expected a refusal)", err)
	}

	// nothing but a regular file: a directory under the allowlist, and a fifo, which without
	// O_NONBLOCK would have blocked the helper for good
	if err := os.MkdirAll(filepath.Join(root, "usr/local/tmp/dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if f, err := c.Open(filepath.Join(root, "usr/local/tmp/dir")); err == nil {
		f.Close()
		t.Error("a directory was passed back")
	}
	fifo := filepath.Join(root, "usr/local/tmp/fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := c.Open(fifo)
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a fifo was passed back")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("opening a fifo blocked the helper")
	}

	// a file that is gone is an error, not a refusal, and says nothing more
	if _, err := c.Open(filepath.Join(root, "usr/local/tmp/nothing")); err == nil {
		t.Error("a missing file opened")
	}
}

// The policy half on its own: what sendfileAllowed says yes to, and with which directory.
func TestSendfilePolicy(t *testing.T) {
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	if dir, ok := p.sendfileAllowed("/usr/local/tmp/x.tar.gz"); !ok || dir != "/usr/local/tmp" {
		t.Errorf("the archive path: %q %v", dir, ok)
	}
	for _, bad := range []string{
		"/usr/local/tmp", "/usr/local/tmp/", "/usr/local/tmpx/f", "/etc/config/shadow",
		"/etc/config/homematic.regadom", "/usr/local/tmp/../etc/passwd", "usr/local/tmp/f", "",
	} {
		if _, ok := p.sendfileAllowed(bad); ok {
			t.Errorf("allowed %q", bad)
		}
	}
	// the sendfile directory is not a read grant and the read list is not an open grant
	if p.readAllowed("/usr/local/tmp/x.tar.gz") {
		t.Error("/usr/local/tmp is on the read list")
	}
	if _, ok := p.sendfileAllowed("/etc/config/netconfig"); ok {
		t.Error("a ReadPaths file is openable")
	}
}

// Local.Open is the open the helper performs: read-only, no symlink, regular files only.
func TestLocalOpenRules(t *testing.T) {
	d := t.TempDir()
	file := filepath.Join(d, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lo := Local{}
	f, err := lo.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("y")); err == nil {
		t.Error("the descriptor is writable")
	}
	f.Close()
	if err := os.Symlink(file, filepath.Join(d, "l")); err != nil {
		t.Fatal(err)
	}
	if _, err := lo.Open(filepath.Join(d, "l")); err == nil {
		t.Error("a symlink was opened")
	}
	if _, err := lo.Open(d); err == nil {
		t.Error("a directory was opened")
	}
}

// B-15: the root password is set by an operation of its own, because /etc/config/shadow is
// 0640 root:root and must stay unreadable to the daemon. What the helper accepts is one hash for
// one line of one file - the tests are the refusals around that.
func TestRootPasswordOperation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc/config"), 0o755); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(root, "etc/config/shadow")
	const before = "root:$6$old$oldhash:19000:0:99999:7:::\nhm:*:19000:0:99999:7:::\nsshd:!:19000::::::\n"
	if err := os.WriteFile(shadow, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "etc/passwd"), []byte("root:x:0:0:::\n"), 0o644)

	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite")}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}

	if err := c.SetRootPasswordHash(shadow, "$6$newsalt$newhash./ABC"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := os.ReadFile(shadow)
	if err != nil {
		t.Fatal(err)
	}
	const want = "root:$6$newsalt$newhash./ABC:19000:0:99999:7:::\nhm:*:19000:0:99999:7:::\nsshd:!:19000::::::\n"
	if string(got) != want {
		t.Errorf("shadow is now %q", got)
	}
	// the file keeps its mode: it is the password file, not a config file (B-24's lesson)
	if st, _ := os.Stat(shadow); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st.Mode().Perm())
	}

	// the file stays unreadable through the helper, and unopenable: the whole point of giving
	// this its own operation is that the hashes never leave the privileged process (B-15)
	if _, err := c.ReadFile(shadow); err == nil || !errors.Is(err, ErrRefused) {
		t.Errorf("shadow is on the read list: %v", err)
	}
	if f, err := c.Open(shadow); err == nil {
		f.Close()
		t.Error("shadow was passed back as a descriptor")
	}

	// no other file, however writable it is otherwise
	for _, bad := range []string{
		filepath.Join(root, "etc/passwd"),
		filepath.Join(root, "etc/config/netconfig"),
		filepath.Join(root, "etc/config/rfd.conf"),
		filepath.Join(root, "etc/config/../config/shadow.bak"),
		filepath.Join(root, "usr/local/etc/config/shadow"),
		"/etc/config/shadow",
	} {
		if err := c.SetRootPasswordHash(bad, "$6$newsalt$newhash"); err == nil || !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v (expected a refusal)", bad, err)
		}
	}

	// and nothing that is not a hash: a colon adds fields to the line, a newline adds lines to
	// the file, and neither gets past the boundary
	for _, bad := range []string{
		"$6$salt$hash:0:0:::", "$6$salt$hash\nroot2:$6$x$y:", "", "*", "!", "x",
		"nodollar", "$$", "$6$", "$6$salt$hash with a space", "$6$salt$hash\\n",
		"$verylongid$salt$hash",
	} {
		if err := c.SetRootPasswordHash(shadow, bad); err == nil {
			t.Errorf("accepted the hash %q", bad)
		}
	}
	if got, _ := os.ReadFile(shadow); string(got) != want {
		t.Errorf("a refused write changed the file: %q", got)
	}

	// a shadow file without a root line is an error, not a silently rewritten file
	other := filepath.Join(root, "etc/config/shadow")
	_ = os.WriteFile(other, []byte("hm:*:19000:0:99999:7:::\n"), 0o640)
	if err := c.SetRootPasswordHash(other, "$6$salt$hash"); err == nil {
		t.Error("a shadow file with no root line was accepted")
	}
}

// The hash rule on its own, so the shapes it must accept and refuse are visible in one place.
func TestPasswordHashRule(t *testing.T) {
	for _, good := range []string{
		"$6$rQ8lM2/x$5Xk9Zb.7cD",
		"$6$rounds=5000$abcdef$0123456789./abcdef",
		"$y$j9T$F5Jx9$K1n0.abcdefgh",
		"$2b$10$abcdefghijklmnopqrstuv",
	} {
		if !validPasswordHash(good) {
			t.Errorf("refused %q", good)
		}
	}
	for _, bad := range []string{
		"", "x", "*", "!", "$6$", "$6$s", "nodollar", "$6$salt$hash:extra",
		"$6$salt$hash\n", "$6$salt$ha sh", "$6$salt$hash\t", "6$salt$hash",
		"$TOOLONGID$salt$hash", "$6$" + strings.Repeat("a", 300),
	} {
		if validPasswordHash(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// TestCertificateOperations covers task 35's two operations: the live TLS file is written only
// at its exact path and only when the data is a chain plus its key, and read back without the
// key.
func TestCertificateOperations(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "etc/config"), 0o755)
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var refused []string
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Log: func(f string, a ...any) { refused = append(refused, fmt.Sprintf(f, a...)) }}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}
	live := filepath.Join(root, "etc/config/server.pem")

	now := time.Now()
	leaf, key, ca := testcert.Issued([]string{"box.example.org"}, now.Add(90*24*time.Hour))
	pem, err := certpem.AssemblePEM(append(leaf, ca...), key)
	if err != nil {
		t.Fatal(err)
	}
	// the exact path, valid data: written 0640 (0600 without root and the certs group)
	if err := c.WriteCertificate(live, pem, "acme"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(live)
	if err != nil || st.Mode().Perm() != 0o600 && st.Mode().Perm() != 0o640 {
		t.Fatalf("%v %v", err, st.Mode())
	}
	// the marker beside it: one line, the issuer, 0644 - what S50lighttpd's check_certificate
	// reads to leave an ACME certificate alone
	mst, err := os.Stat(live + MarkerSuffix)
	if err != nil || mst.Mode().Perm() != 0o644 {
		t.Fatalf("marker: %v %v", err, mst)
	}
	if m, _ := os.ReadFile(live + MarkerSuffix); !strings.Contains(string(m), "Test CA") || strings.Count(string(m), "\n") != 1 {
		t.Fatalf("marker content: %q", m)
	}
	// task 38: the managed marker beside it names the mode
	if m, _ := os.ReadFile(live + ManagedSuffix); !strings.HasPrefix(string(m), "acme CN=Test CA") || strings.Count(string(m), "\n") != 1 {
		t.Fatalf("managed marker content: %q", m)
	}
	// read back: the certificates, never the key, and the marker's line
	got, marker, err := c.ReadCertificate(live)
	if err != nil || strings.Contains(string(got), "PRIVATE KEY") || strings.Count(string(got), "BEGIN CERTIFICATE") != 2 || !strings.HasPrefix(marker, "acme CN=Test CA") {
		t.Fatalf("%v %s %q", err, got, marker)
	}
	// the markers are not certificate paths: neither written nor read through the operation
	for _, suffix := range []string{MarkerSuffix, ManagedSuffix} {
		if err := c.WriteCertificate(live+suffix, pem, "acme"); err == nil || !errors.Is(err, ErrRefused) {
			t.Fatalf("marker as certificate path: %v", err)
		}
		if _, _, err := c.ReadCertificate(live + suffix); err == nil || !errors.Is(err, ErrRefused) {
			t.Fatalf("marker read: %v", err)
		}
	}
	// an unknown mode is refused at the boundary
	if err := c.WriteCertificate(live, pem, "self-signed"); err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("mode: %v", err)
	}
	// an old install with the .acme marker alone reads as mode acme; the manual mode as manual
	_ = os.Remove(live + ManagedSuffix)
	if _, marker, err := c.ReadCertificate(live); err != nil || marker != "acme CN=Test CA" {
		t.Fatalf("acme marker alone: %v %q", err, marker)
	}
	if err := c.WriteCertificate(live, pem, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, marker, _ := c.ReadCertificate(live); !strings.HasPrefix(marker, "manual CN=Test CA") {
		t.Fatalf("manual marker: %q", marker)
	}
	// without the markers the read says so
	_ = os.Remove(live + MarkerSuffix)
	_ = os.Remove(live + ManagedSuffix)
	if _, marker, err := c.ReadCertificate(live); err != nil || marker != "" {
		t.Fatalf("no marker: %v %q", err, marker)
	}
	// another path under /etc/config, which the generic write allows, is refused here
	other := filepath.Join(root, "etc/config/other.pem")
	if err := c.WriteCertificate(other, pem, "acme"); err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("other path: %v", err)
	}
	if _, _, err := c.ReadCertificate(filepath.Join(root, "etc/config/rfd.conf")); err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("read of a read-path file: %v", err)
	}
	if _, _, err := c.ReadCertificate(filepath.Join(root, "etc/config/../shadow")); err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("traversal: %v", err)
	}
	// the right path with the wrong data: a key that is not the certificate's, a file with two
	// keys, a certificate only, garbage - all refused at the boundary, the file untouched
	_, otherKey, _ := testcert.Issued([]string{"other"}, now.Add(time.Hour))
	for name, data := range map[string][]byte{
		"wrong key": append(append([]byte{}, leaf...), otherKey...),
		"two keys":  append(append([]byte{}, pem...), key...),
		"no key":    leaf,
		"garbage":   []byte("hello"),
		"empty":     nil,
	} {
		if err := c.WriteCertificate(live, data, "acme"); err == nil || !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(live); string(b) != string(pem) {
		t.Fatal("the live file changed")
	}
	if len(refused) < 7 {
		t.Fatalf("refusals not logged: %v", refused)
	}
	// Local refuses bad data too (the root path, no helper)
	if err := (Local{}).WriteCertificate(other, leaf, "acme"); err == nil {
		t.Fatal("Local wrote a certificate without a key")
	}
	if _, err := os.Stat(other); err == nil {
		t.Fatal("Local wrote the refused file")
	}
}

// B-53: a write through a symlink lands beside and onto the target, not beside the link
func TestWriteFileFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "var/etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "var/etc/hostname"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "etc/hostname")
	if err := os.Symlink(filepath.Join(dir, "var/etc/hostname"), link); err != nil {
		t.Fatal(err)
	}
	// the link's directory is not writable: exactly the read-only rootfs
	if err := os.Chmod(filepath.Join(dir, "etc"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "etc"), 0o755) })
	if err := (Local{}).WriteFile(link, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write through the link: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "var/etc/hostname")); string(b) != "new\n" {
		t.Errorf("target not written: %q", b)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a file")
	}
}

// B-54: the addon account is two appended lines on the existing inodes - on the image the files
// are bind mounts over a read-only /etc, and a temporary file plus rename (what busybox adduser
// and addgroup do) fails there and would replace the mount if it did not.
func TestAddonUserOperation(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	passwd, group := filepath.Join(root, "etc/passwd"), filepath.Join(root, "etc/group")
	if err := os.WriteFile(passwd, []byte("root:x:0:0::/:/bin/sh\nocculite:x:8100:8100::/var/lib/occulite:/bin/false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(group, []byte("root:x:0:\ncerts:x:8101:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inode := func(p string) uint64 {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return st.Sys().(*syscall.Stat_t).Ino
	}
	pIno, gIno := inode(passwd), inode(group)
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var refused []string
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Log: func(f string, a ...any) { refused = append(refused, fmt.Sprintf(f, a...)) }}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}

	// append: the two lines, the files' other lines untouched, the inodes the same
	if err := c.AddAddonUser(passwd, group, "addon-mosq", 30004); err != nil {
		t.Fatal(err)
	}
	wantPasswd := "root:x:0:0::/:/bin/sh\nocculite:x:8100:8100::/var/lib/occulite:/bin/false\naddon-mosq:x:30004:30004::/usr/local/addons/mosq:/bin/false\n"
	wantGroup := "root:x:0:\ncerts:x:8101:\naddon-mosq:x:30004:\n"
	if b, _ := os.ReadFile(passwd); string(b) != wantPasswd {
		t.Errorf("passwd:\n%s", b)
	}
	if b, _ := os.ReadFile(group); string(b) != wantGroup {
		t.Errorf("group:\n%s", b)
	}
	if inode(passwd) != pIno || inode(group) != gIno {
		t.Error("the files were replaced rather than edited in place")
	}
	// idempotent: the same account again changes nothing
	if err := c.AddAddonUser(passwd, group, "addon-mosq", 30004); err != nil {
		t.Errorf("second call: %v", err)
	}
	if b, _ := os.ReadFile(passwd); string(b) != wantPasswd {
		t.Errorf("passwd after the second call:\n%s", b)
	}
	// conflicts: the name with another uid, the uid with another name - an error, nothing written
	if err := c.AddAddonUser(passwd, group, "addon-mosq", 30005); err == nil || !strings.Contains(err.Error(), "addon-mosq exists with id 30004") {
		t.Errorf("name conflict: %v", err)
	}
	if err := c.AddAddonUser(passwd, group, "addon-other", 30004); err == nil || !strings.Contains(err.Error(), "belongs to addon-mosq") {
		t.Errorf("uid conflict: %v", err)
	}
	if b, _ := os.ReadFile(passwd); string(b) != wantPasswd {
		t.Errorf("passwd after the conflicts:\n%s", b)
	}
	if b, _ := os.ReadFile(group); string(b) != wantGroup {
		t.Errorf("group after the conflicts:\n%s", b)
	}
	// the boundary: other paths, a name outside the addon shape, a uid below the base
	for name, try := range map[string]func() error{
		"passwd elsewhere": func() error { return c.AddAddonUser(filepath.Join(root, "etc/config/passwd"), group, "addon-a", 30010) },
		"group elsewhere":  func() error { return c.AddAddonUser(passwd, filepath.Join(root, "etc/config/group"), "addon-a", 30010) },
		"files swapped":    func() error { return c.AddAddonUser(group, passwd, "addon-a", 30010) },
		"not an addon":     func() error { return c.AddAddonUser(passwd, group, "root2", 30010) },
		"a second line":    func() error { return c.AddAddonUser(passwd, group, "addon-a\nevil:x:0:0::/:/bin/sh", 30010) },
		"a field":          func() error { return c.AddAddonUser(passwd, group, "addon-a:x", 30010) },
		"uid below base":   func() error { return c.AddAddonUser(passwd, group, "addon-a", 8101) },
		"uid zero":         func() error { return c.AddAddonUser(passwd, group, "addon-a", 0) },
	} {
		if err := try(); err == nil || !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(refused) < 8 {
		t.Errorf("refusals not logged: %v", refused)
	}
	if b, _ := os.ReadFile(passwd); string(b) != wantPasswd {
		t.Errorf("passwd after the refusals:\n%s", b)
	}
	// Local: a file without a trailing newline gets one before the line; a name it does not
	// accept is an error before anything is opened
	if err := os.WriteFile(group, []byte("root:x:0:"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (Local{}).AddAddonUser(passwd, group, "addon-two", 30006); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(group); string(b) != "root:x:0:\naddon-two:x:30006:\n" {
		t.Errorf("group without a trailing newline:\n%s", b)
	}
	if !strings.HasSuffix(readOrEmpty(passwd), "addon-two:x:30006:30006::/usr/local/addons/two:/bin/false\n") {
		t.Errorf("passwd:\n%s", readOrEmpty(passwd))
	}
	if err := (Local{}).AddAddonUser(passwd, group, "www", 30007); err == nil {
		t.Error("Local accepted a name outside the addon shape")
	}
	// a missing file is an error, not a new file: the operation edits what the image has
	if err := (Local{}).AddAddonUser(passwd, filepath.Join(root, "etc/nogroup"), "addon-three", 30008); err == nil {
		t.Error("a missing group file was created")
	}
}

func readOrEmpty(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// task 41: the coprocessor flash is one narrow operation - a family, a raw-uart node, a file
// under the firmware directories - and the helper builds the command line itself
type recordFlash struct {
	Local
	calls [][]string
}

func (r *recordFlash) FlashCoprocessor(_ context.Context, family, devnode, file, version string) (Result, error) {
	r.calls = append(r.calls, []string{family, devnode, file, version})
	return Result{Stdout: []byte("flashed\n")}, nil
}

func TestFlashCoproPolicy(t *testing.T) {
	root := t.TempDir()
	shipped := filepath.Join(root, "firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.18.eq3")
	uploaded := filepath.Join(root, "usr/local/etc/config/radio-firmware/HmIP-RFUSB/dualcopro_update_blhmip-4.4.22.eq3")
	for _, f := range []string{shipped, uploaded} {
		_ = os.MkdirAll(filepath.Dir(f), 0o755)
		_ = os.WriteFile(f, []byte("eq3"), 0o644)
	}
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := &recordFlash{}
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Ops: ops}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}

	if r, err := c.FlashCoprocessor(context.Background(), CoproHmIP, "/dev/raw-uart1", shipped, "4.4.18"); err != nil || string(r.Stdout) != "flashed\n" {
		t.Fatalf("shipped file: %v %+v", err, r)
	}
	if _, err := c.FlashCoprocessor(context.Background(), CoproLegacy, "/dev/raw-uart", uploaded, "4.4.22"); err != nil {
		t.Fatalf("uploaded file: %v", err)
	}
	if len(ops.calls) != 2 || ops.calls[0][0] != CoproHmIP || ops.calls[0][1] != "/dev/raw-uart1" || ops.calls[0][2] != shipped || ops.calls[0][3] != "4.4.18" {
		t.Fatalf("calls: %v", ops.calls)
	}
	refused := []struct {
		name, family, dev, file, version string
	}{
		{"unknown family", "jar", "/dev/raw-uart1", shipped, "1"},
		{"not a raw-uart", CoproHmIP, "/dev/ttyUSB0", shipped, "1"},
		{"the loop device", CoproHmIP, "/dev/mmd_hmip", shipped, "1"},
		{"a device with a trailing path", CoproHmIP, "/dev/raw-uart1/../sda", shipped, "1"},
		{"a file outside the firmware directories", CoproHmIP, "/dev/raw-uart1", filepath.Join(root, "etc/config/x.eq3"), "1"},
		{"a file that is not .eq3", CoproHmIP, "/dev/raw-uart1", filepath.Join(root, "firmware/fwmap"), "1"},
		{"the directory itself", CoproHmIP, "/dev/raw-uart1", filepath.Join(root, "firmware/"), "1"},
		{"a dot-dot in the file", CoproHmIP, "/dev/raw-uart1", filepath.Join(root, "firmware/../etc/x.eq3"), "1"},
		{"a version with a newline", CoproLegacy, "/dev/raw-uart", shipped, "1\nCCU2 evil"},
		{"a version with a space", CoproLegacy, "/dev/raw-uart", shipped, "1 2"},
	}
	for _, c2 := range refused {
		if _, err := c.FlashCoprocessor(context.Background(), c2.family, c2.dev, c2.file, c2.version); err == nil || !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", c2.name, err)
		}
	}
	if len(ops.calls) != 2 {
		t.Fatalf("a refused flash reached the operation: %v", ops.calls)
	}
	// the version reader's program is on the list by its exact path, and java is not
	pol := DefaultPolicy("/", "/usr/local/etc/occulite")
	if !pol.programAllowed("/bin/detect_radio_module", []string{"/dev/raw-uart1"}) {
		t.Error("detect_radio_module is not on the program list")
	}
	if pol.programAllowed("/opt/java/bin/java", []string{"-jar", "/tmp/x.jar"}) {
		t.Error("java must not be on the program list")
	}
}

// Local builds the two flasher command lines: the jar for the HmIP family, eq3configcmd with a
// temporary directory and a synthetic fwmap for the legacy one, retried with -f on failure
func TestLocalFlashCoprocessor(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	fake := filepath.Join(dir, "fake")
	// records its argv, prints the fwmap it was pointed at, fails on the first legacy call
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nfor a in \"$@\"; do case $a in " + dir + "/*|/tmp/*) [ -f \"$a/fwmap\" ] && cat \"$a/fwmap\";; esac; done\n" +
		"if [ \"$1\" = update-coprocessor ] && [ ! -f " + dir + "/second ]; then touch " + dir + "/second; echo 'first try failed' >&2; exit 3; fi\necho ok\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	fw := filepath.Join(dir, "dualcopro_si1002_update_blhm-2.2.1.eq3")
	_ = os.WriteFile(fw, []byte("eq3"), 0o644)
	oldJava, oldCmd := coproJava, coproConfigCmd
	coproJava, coproConfigCmd = fake, fake
	t.Cleanup(func() { coproJava, coproConfigCmd = oldJava, oldCmd })
	// off a systemd box: the flasher is called directly, as before
	oldDir, oldWork := coproSystemdDir, coproWorkDir
	coproSystemdDir, coproWorkDir = filepath.Join(dir, "no-systemd"), dir
	t.Cleanup(func() { coproSystemdDir, coproWorkDir = oldDir, oldWork })

	var local Local
	r, err := local.FlashCoprocessor(context.Background(), CoproHmIP, "/dev/raw-uart1", fw, "2.2.1")
	if err != nil || r.Exit != 0 {
		t.Fatalf("hmip: %v %+v", err, r)
	}
	calls, _ := os.ReadFile(log)
	want := "-Dos.arch=" + unameMachine() + " -Dgnu.io.rxtx.SerialPorts=/dev/raw-uart1 -jar " + coproJar + " -p /dev/raw-uart1 -o -f " + fw + "\n"
	if string(calls) != want {
		t.Fatalf("hmip argv:\n%s\nwant\n%s", calls, want)
	}
	_ = os.Remove(log)
	r, err = local.FlashCoprocessor(context.Background(), CoproLegacy, "/dev/raw-uart", fw, "2.2.1")
	if err != nil || r.Exit != 0 {
		t.Fatalf("legacy: %v %+v", err, r)
	}
	calls, _ = os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "update-coprocessor -p /dev/raw-uart -t HM-MOD-UART -u -d ") || !strings.HasPrefix(lines[1], "update-coprocessor -p /dev/raw-uart -t HM-MOD-UART -u -f -d ") {
		t.Fatalf("legacy argv: %q", lines)
	}
	out := string(r.Combined())
	if !strings.Contains(out, "CCU2 dualcopro_si1002_update_blhm-2.2.1.eq3 2.2.1") || !strings.Contains(out, "first try failed") || !strings.Contains(out, "retry with -f") {
		t.Fatalf("legacy output: %q", out)
	}
	if _, err := local.FlashCoprocessor(context.Background(), "other", "/dev/raw-uart", fw, ""); err == nil {
		t.Error("an unknown family must be an error")
	}
	if unameMachine() == "" {
		t.Error("uname -m empty")
	}
}

// D-52: a confined addon's data directory outside its three standard ones is created and
// chowned through the boundary; the mechanical half of the guard rails lives here.
func TestAddonDataDirPolicy(t *testing.T) {
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	for path, want := range map[string]bool{
		"/usr/local/hmm":                   true,
		"/usr/local/hmm/cache":             true,
		"/usr/local/addons/hmm":            false, // B-294: no write prefix, and a shared tree to the data directories
		"/usr/local/etc/config/addons/hmm": false, // B-295: no drop-in here confines hmm
		"/usr/local":                       false,
		"/usr/local/":                      false,
		"/usr/local/addons":                false, // B-294: off Paths
		"/usr/local/etc":                   false,
		"/usr/local/etc/":                  false,
		"/usr/local/tmp":                   true, // on Paths as a prefix, the directory itself included (B-6)
		"/usr/local/crontabs":              false,
		"/usr/local/lost+found":            false,
		"/usr/local/.firmwareUpdate":       true, // an exact entry of Paths (the marker file)
		"/usr/local/.hidden/x":             false,
		"/usr/local/hmm/../etc":            false,
		"/usr/lib":                         false,
		"/etc/passwd":                      false,
		"/home/x":                          false,
		"/usr/local/sdcard":                false,
		"/usr/local/backup":                true, // the same: Paths' prefix, so what could be written could be chowned before
		"/usr/local/backup/x":              true,
	} {
		if got := p.dataDirAllowed(path); got != want {
			t.Errorf("dataDirAllowed(%q) = %v, want %v", path, got, want)
		}
	}
	// through the server: the fake root's /usr/local/<id> is chowned, /usr/local/etc refused
	root := t.TempDir()
	for _, d := range []string{"usr/local/hmm/cache", "usr/local/etc/config"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite")}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}
	if err := c.Chown(filepath.Join(root, "usr/local/hmm"), os.Getuid(), os.Getgid(), true); err != nil {
		t.Errorf("chown of the data directory refused: %v", err)
	}
	if err := c.Chown(filepath.Join(root, "usr/local/etc"), os.Getuid(), os.Getgid(), true); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("chown of /usr/local/etc accepted: %v", err)
	}
	if err := c.Chown(filepath.Join(root, "usr/local"), os.Getuid(), os.Getgid(), true); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("chown of /usr/local accepted: %v", err)
	}
	// a declared data directory that does not exist yet is created (a fresh install: the confined
	// addon cannot mkdir below /usr/local itself), a shared tree is not
	if err := c.MkdirAll(filepath.Join(root, "usr/local/redmatic/var"), 0o755); err != nil {
		t.Errorf("mkdir of a data directory refused: %v", err)
	}
	if st, err := os.Stat(filepath.Join(root, "usr/local/redmatic/var")); err != nil || !st.IsDir() {
		t.Error("data directory not created")
	}
	if err := c.MkdirAll(filepath.Join(root, "usr/local/sdcard/x"), 0o755); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("mkdir under a shared tree accepted: %v", err)
	}
}

// D-93 (task 67, task 103 step 5): on a systemd box the flasher runs in a transient unit as
// multimacd with the node's resource group and a device policy admitting that one node; the
// unit is bounded by RuntimeMaxSec; a node without a group falls back to root and says so.
func TestLocalFlashCoprocessorAsModuleUser(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	// the systemd-run stand-in records its argv and runs what follows "--"
	fakeRun := filepath.Join(dir, "systemd-run")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + log + "\nwhile [ \"$1\" != -- ]; do shift; done; shift\nexec \"$@\"\n"
	if err := os.WriteFile(fakeRun, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	flasher := filepath.Join(dir, "flasher")
	if err := os.WriteFile(flasher, []byte("#!/bin/sh\necho flashed as $(id -un)\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fw := filepath.Join(dir, "dualcopro_update_blhmip-4.4.22.eq3")
	_ = os.WriteFile(fw, []byte("eq3"), 0o644)
	oldJava, oldCmd, oldRun, oldDir, oldWork, oldGroup := coproJava, coproConfigCmd, coproSystemdRun, coproSystemdDir, coproWorkDir, coproNodeGroup
	coproJava, coproConfigCmd, coproSystemdRun, coproSystemdDir, coproWorkDir = flasher, flasher, fakeRun, dir, dir
	coproNodeGroup = func(string) string { return "raw-uart" }
	t.Cleanup(func() {
		coproJava, coproConfigCmd, coproSystemdRun, coproSystemdDir, coproWorkDir, coproNodeGroup = oldJava, oldCmd, oldRun, oldDir, oldWork, oldGroup
	})

	var local Local
	r, err := local.FlashCoprocessor(context.Background(), CoproHmIP, "/dev/raw-uart1", fw, "4.4.22")
	if err != nil {
		t.Fatal(err)
	}
	if r.Exit != 7 || !strings.Contains(string(r.Stdout), "flashed as") {
		t.Fatalf("the unit's exit and output: %+v", r)
	}
	argv, _ := os.ReadFile(log)
	got := strings.Split(strings.TrimSpace(string(argv)), "\n")
	want := []string{"--wait", "--pipe", "--collect", "--quiet", "--unit=",
		"-p", "RuntimeMaxSec=120",
		"-p", "User=multimacd", "-p", "Group=multimacd",
		"-p", "SupplementaryGroups=raw-uart", "-p", "SupplementaryGroups=lock",
		"-p", "DevicePolicy=closed", "-p", "DeviceAllow=/dev/raw-uart1 rw",
		"-p", "CapabilityBoundingSet=", "-p", "NoNewPrivileges=yes",
		"-p", "ProtectSystem=strict", "-p", "ReadWritePaths=/run/lock", "-p", "PrivateTmp=yes", "-p", "ProtectHome=yes",
		"-p", "ProtectKernelTunables=yes", "-p", "ProtectKernelModules=yes", "-p", "ProtectControlGroups=yes",
		"-p", "WorkingDirectory=/",
		"--", flasher, "-Dos.arch=" + unameMachine(), "-Dgnu.io.rxtx.SerialPorts=/dev/raw-uart1", "-jar", coproJar, "-p", "/dev/raw-uart1", "-o", "-f", fw}
	if len(got) != len(want) {
		t.Fatalf("argv:\n%s\nwant %d words, got %d", argv, len(want), len(got))
	}
	for i := range want {
		if want[i] == "--unit=" {
			if !strings.HasPrefix(got[i], "--unit=occulite-copro-") {
				t.Errorf("word %d: %q, want an occulite-copro unit", i, got[i])
			}
			continue
		}
		if got[i] != want[i] {
			t.Errorf("word %d: %q, want %q", i, got[i], want[i])
		}
	}
	// the legacy flasher: its directory under the work directory, world-readable, the longer
	// bound on the forced retry
	_ = os.Remove(log)
	if _, err := local.FlashCoprocessor(context.Background(), CoproLegacy, "/dev/raw-uart", fw, "4.4.22"); err != nil {
		t.Fatal(err)
	}
	argv, _ = os.ReadFile(log)
	if !strings.Contains(string(argv), "RuntimeMaxSec=240\n") || !strings.Contains(string(argv), "-d\n"+dir+"/copro-") {
		t.Fatalf("legacy retry argv: %s", argv)
	}
	// a node without a resource group: as root, and said
	coproNodeGroup = func(string) string { return "" }
	_ = os.Remove(log)
	r, err = local.FlashCoprocessor(context.Background(), CoproHmIP, "/dev/raw-uart1", fw, "4.4.22")
	if err != nil || r.Exit != 7 {
		t.Fatalf("root fallback: %v %+v", err, r)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("the root fallback must not go through systemd-run")
	}
	if !strings.HasPrefix(string(r.Stderr), "flashing as root: /dev/raw-uart1 carries no resource group") {
		t.Errorf("the fallback is not said: %q", r.Stderr)
	}
	// the real lookup: a node that does not exist has no group
	if g := oldGroup(filepath.Join(dir, "no-such-node")); g != "" {
		t.Errorf("a missing node has a group: %q", g)
	}
}

// task 147: the HM-CFG-USB-2's flash - the adapter named usb:<serial>, an .enc under the firmware
// directories; the command line flash-hmcfgusb -S <serial> <file>
func TestFlashHMCFGUSB(t *testing.T) {
	root := t.TempDir()
	enc := filepath.Join(root, "usr/local/etc/config/radio-firmware/HM-CFG-USB-2/hmusbif.03c7.enc")
	eq3 := filepath.Join(root, "usr/local/etc/config/radio-firmware/HM-CFG-USB-2/x.eq3")
	for _, f := range []string{enc, eq3} {
		_ = os.MkdirAll(filepath.Dir(f), 0o755)
		_ = os.WriteFile(f, []byte("enc"), 0o644)
	}
	p := DefaultPolicy(root, "/usr/local/etc/occulite")
	for _, c := range []struct {
		name, family, dev, file string
		ok                      bool
	}{
		{"the adapter by its serial", CoproHMCFGUSB, "usb:JEQ9000002", enc, true},
		{"an .eq3 for the adapter", CoproHMCFGUSB, "usb:JEQ9000002", eq3, false},
		{"a raw-uart for the adapter", CoproHMCFGUSB, "/dev/raw-uart1", enc, false},
		{"a serial that is not one", CoproHMCFGUSB, "usb:JEQ9000002 -x", enc, false},
		{"an .enc for a module", CoproHmIP, "/dev/raw-uart1", enc, false},
		{"usb: for a module", CoproHmIP, "usb:JEQ9000002", eq3, false},
	} {
		if got := p.coproAllowed(c.family, c.dev, c.file, "0.967"); got != c.ok {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	fake := filepath.Join(dir, "flash-hmcfgusb")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\necho 'Firmware update successfull!'\n"), 0o755)
	old, oldDir := coproHMCFGUSB, coproSystemdDir
	coproHMCFGUSB, coproSystemdDir = fake, filepath.Join(dir, "no-systemd")
	t.Cleanup(func() { coproHMCFGUSB, coproSystemdDir = old, oldDir })
	var local Local
	r, err := local.FlashCoprocessor(context.Background(), CoproHMCFGUSB, "usb:JEQ9000002", enc, "0.967")
	calls, _ := os.ReadFile(log)
	if err != nil || r.Exit != 0 || string(calls) != "-S JEQ9000002 "+enc+"\n" {
		t.Fatalf("argv %q: %v %+v", calls, err, r)
	}
	if _, err := local.FlashCoprocessor(context.Background(), CoproHMCFGUSB, "/dev/raw-uart1", enc, ""); err == nil {
		t.Error("a node that is not usb:<serial> must be an error")
	}
}

// B-119: a confined addon's rc.d script may run as the addon's user with the actions the rc.d ABI
// calls outside the unit - and only there, only as an addon uid, only those actions.
func TestAddonScriptRunAsPolicy(t *testing.T) {
	p := DefaultPolicy("/", "/usr/local/etc/occulite")
	for _, tt := range []struct {
		name string
		args []string
		uid  int
		want bool
		why  string
	}{
		{"/usr/local/etc/config/rc.d/hmm", []string{"info"}, 30000, true, "info as the addon's user"},
		{"/usr/local/etc/config/rc.d/hmm", []string{"info.de"}, 30000, true, "the WebUI's localised info"},
		{"/usr/local/etc/config/rc.d/hmm", []string{"init"}, 30001, true, "init as the addon's user"},
		{"/usr/local/etc/config/rc.d/hmm", []string{"uninstall"}, 30000, true, "uninstall as the addon's user"},
		{"/usr/local/etc/config/rc.d/hmm", []string{"uninstall", "purge"}, 30000, true, "an argument after the action"},
		{"/usr/local/etc/config/rc.d/hmm", []string{"start"}, 30000, false, "start is the unit's"},
		{"/usr/local/etc/config/rc.d/hmm", []string{"restart"}, 30000, false, "restart is the unit's"},
		{"/usr/local/etc/config/rc.d/hmm", nil, 30000, false, "no action"},
		{"/usr/local/etc/config/rc.d/hmm", []string{"info"}, 0, false, "root runs rc.d scripts through run, not runas"},
		{"/usr/local/etc/config/rc.d/hmm", []string{"info"}, 1000, false, "one of the box's own users"},
		{"/etc/init.d/S99x", []string{"info"}, 30000, false, "an init script is not an addon's"},
		{"/usr/local/etc/config/rc.d/../../../../bin/sh", []string{"info"}, 30000, false, "traversal"},
		{"/usr/local/etc/config/rc.d/sub/hmm", []string{"info"}, 30000, false, "not directly in rc.d"},
		{"/usr/local/addons/hmm/rc.d/hmm", []string{"info"}, 30000, false, "the addon's own tree is not rc.d"},
	} {
		if got := p.addonScriptAllowed(tt.name, tt.args, tt.uid); got != tt.want {
			t.Errorf("%s %v uid %d: %v, want %v - %s", tt.name, tt.args, tt.uid, got, tt.want, tt.why)
		}
		// the CGI rule does not admit any of these either: the two rules are disjoint
		if p.cgiAllowed(tt.name, tt.args, tt.uid) {
			t.Errorf("%s admitted as a CGI", tt.name)
		}
	}
	if (Policy{AddonUIDBase: 30000}).addonScriptAllowed("/usr/local/etc/config/rc.d/hmm", []string{"info"}, 30000) {
		t.Error("a policy without AddonRCDir admits nothing")
	}
}
