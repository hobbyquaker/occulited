package priv

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// importBackup is a small CCU backup with one addon and a file that is nobody's.
func importBackup(t *testing.T) *os.File {
	t.Helper()
	var inner bytes.Buffer
	gz := gzip.NewWriter(&inner)
	tw := tar.NewWriter(gz)
	for _, m := range []struct {
		name, body string
		mode       int64
	}{
		{"usr/local/etc/config/rc.d/wd", "#!/bin/sh\n", 0o755},
		{"usr/local/addons/wd/bin/wd", "program", 0o755},
		{"usr/local/etc/config/shadow", "root:evil", 0o600},
		{"usr/local/etc/config/rc.d/other", "#!/bin/sh\n", 0o755},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: m.name, Typeflag: tar.TypeReg, Mode: m.mode, Size: int64(len(m.body))})
		_, _ = tw.Write([]byte(m.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	var outer bytes.Buffer
	ow := tar.NewWriter(&outer)
	_ = ow.WriteHeader(&tar.Header{Name: "usr_local.tar.gz", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(inner.Len())})
	_, _ = ow.Write(inner.Bytes())
	_ = ow.Close()
	p := filepath.Join(t.TempDir(), "restore-x.sbk")
	if err := os.WriteFile(p, outer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestAddonImportBoundary: the helper takes the backup as a descriptor and addon ids, extracts
// only those addons' own trees below its own root, and refuses everything else.
func TestAddonImportBoundary(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var logged []string
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Log: func(f string, a ...any) { logged = append(logged, f) }}
	go func() { _ = srv.Serve(ctx, l) }()
	c := Client{Socket: sock}
	f := importBackup(t)

	// the root argument is the helper's own: a client cannot name another one
	res, err := c.ImportAddons(f, "/somewhere/else", []string{"wd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !res[0].Imported || !res[0].WasEnabled || res[0].Files != 1 {
		t.Fatalf("result: %+v", res)
	}
	st, err := os.Stat(filepath.Join(root, "usr/local/etc/config/rc.d/wd"))
	if err != nil || st.Mode().Perm()&0o111 != 0 {
		t.Errorf("the rc.d script: %v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "usr/local/addons/wd/bin/wd")); string(b) != "program" {
		t.Errorf("the program: %q", b)
	}
	for _, p := range []string{"usr/local/etc/config/shadow", "usr/local/etc/config/rc.d/other"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s was written", p)
		}
	}
	// again: it is here now, and stays as it is
	res, err = c.ImportAddons(f, "", []string{"wd"})
	if err != nil || res[0].Imported || res[0].Error != "exists" {
		t.Errorf("a second import: %+v %v", res, err)
	}
	// ids that are none, no ids, too many
	many := make([]string, maxImportAddons+1)
	for i := range many {
		many[i] = "a" + strings.Repeat("b", i%20)
	}
	for _, bad := range [][]string{nil, {"../etc"}, {"wd", "www"}, {"97NeoServer"}, {"a/b"}, {""}, {"x.script"}, many} {
		if _, err := c.ImportAddons(f, "", bad); !errors.Is(err, ErrRefused) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	// anything beside the ids, or no descriptor: refused (the operation closes what it was handed)
	dup := func() *os.File { g, _ := os.Open(f.Name()); return g }
	if res := srv.addonImport(request{Op: opAddonImport, Args: []string{"wd"}, Path: "/etc/passwd"}, dup()); res.OK || !strings.Contains(res.Error, "nothing else") {
		t.Errorf("a path with the request: %+v", res)
	}
	if res := srv.addonImport(request{Op: opAddonImport, Args: []string{"wd"}}, nil); res.OK {
		t.Errorf("without a descriptor: %+v", res)
	}
	// what is no archive fails and leaves nothing
	junk := filepath.Join(t.TempDir(), "junk")
	_ = os.WriteFile(junk, []byte("no archive"), 0o600)
	jf, _ := os.Open(junk)
	defer jf.Close()
	if res, err := c.ImportAddons(jf, "", []string{"new"}); err == nil {
		t.Errorf("junk: %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(root, "usr/local/addons/new")); err == nil {
		t.Error("junk left a directory")
	}
	if len(logged) == 0 {
		t.Error("the import was not logged")
	}
	d, _ := os.Open(t.TempDir())
	defer d.Close()
	if _, err := (Local{}).ImportAddons(d, root, []string{"x"}); err == nil {
		t.Error("a directory was taken for a backup")
	}
}
