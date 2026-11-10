package addonimport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type member struct {
	name string
	typ  byte
	mode int64
	body string
	link string
}

func file(name, body string, mode int64) member {
	return member{name: name, typ: tar.TypeReg, mode: mode, body: body}
}
func dir(name string) member { return member{name: name, typ: tar.TypeDir, mode: 0o755} }
func link(name, to string) member {
	return member{name: name, typ: tar.TypeSymlink, mode: 0o777, link: to}
}
func other(name string, t byte) member {
	return member{name: name, typ: t, mode: 0o644, link: "usr/local/addons/a/x"}
}

// sbk builds a backup with these members in its usr_local.tar.gz.
func sbk(t *testing.T, members ...member) *bytes.Reader {
	t.Helper()
	var inner bytes.Buffer
	gz := gzip.NewWriter(&inner)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Typeflag: m.typ, Mode: m.mode, Linkname: m.link}
		if m.typ == tar.TypeReg {
			h.Size = int64(len(m.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if m.typ == tar.TypeReg {
			_, _ = tw.Write([]byte(m.body))
		}
	}
	tw.Close()
	gz.Close()
	var outer bytes.Buffer
	ow := tar.NewWriter(&outer)
	_ = ow.WriteHeader(&tar.Header{Name: "usr_local.tar.gz", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(inner.Len())})
	_, _ = ow.Write(inner.Bytes())
	ow.Close()
	return bytes.NewReader(outer.Bytes())
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode()
}

func TestExtractAnAddon(t *testing.T) {
	root := t.TempDir()
	d := DestUnder(root)
	res, err := Extract(sbk(t,
		dir("usr/local/addons/wd/"),
		file("usr/local/addons/wd/bin/wd", "ELF", 0o4755),
		file("usr/local/addons/wd/VERSION", "0.9\n", 0o644),
		link("usr/local/addons/wd/bin/alias", "wd"),
		file("usr/local/etc/config/rc.d/wd", "#!/bin/sh\n", 0o755),
		link("usr/local/etc/config/addons/www/wd", "/usr/local/addons/wd/www"),
		file("usr/local/etc/config/addons/wd/settings", "a=1\n", 0o600),
		// not asked for, and not an addon's
		file("usr/local/addons/other/x", "x", 0o644),
		file("usr/local/etc/config/rc.d/other", "#!/bin/sh\n", 0o755),
		file("usr/local/etc/config/shadow", "root:x", 0o600),
		file("usr/local/etc/config/addons/www/index.html", "x", 0o644),
	), []string{"wd"}, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !res[0].Imported || !res[0].WasEnabled || res[0].Files != 2 || res[0].Error != "" {
		t.Fatalf("result: %+v", res)
	}
	if m := mode(t, filepath.Join(d.RCDir, "wd")); m.Perm() != 0o644 {
		t.Errorf("the rc.d script keeps executable bits: %v", m)
	}
	if m := mode(t, filepath.Join(d.Home, "wd/bin/wd")); m != 0o755 {
		t.Errorf("the setuid bit came along, or the mode is off: %v", m)
	}
	if to, _ := os.Readlink(filepath.Join(d.WWWDir, "wd")); to != "/usr/local/addons/wd/www" {
		t.Errorf("web link: %q", to)
	}
	if b, _ := os.ReadFile(filepath.Join(d.Config, "wd/settings")); string(b) != "a=1\n" {
		t.Errorf("settings: %q", b)
	}
	for _, p := range []string{filepath.Join(d.Home, "other"), filepath.Join(d.RCDir, "other"), filepath.Join(root, "usr/local/etc/config/shadow"), filepath.Join(d.WWWDir, "index.html")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s was written", p)
		}
	}
}

func TestExtractLinkedEntryIsDisabledAtItsTarget(t *testing.T) {
	d := DestUnder(t.TempDir())
	res, err := Extract(sbk(t,
		link("usr/local/etc/config/rc.d/red", "/usr/local/addons/red/bin/rc"),
		file("usr/local/addons/red/bin/rc", "#!/bin/sh\n", 0o755),
	), []string{"red"}, d)
	if err != nil || !res[0].Imported || !res[0].WasEnabled {
		t.Fatalf("%+v %v", res, err)
	}
	if m := mode(t, filepath.Join(d.Home, "red/bin/rc")); m.Perm()&0o111 != 0 {
		t.Errorf("the linked script is still executable: %v", m)
	}
}

func TestExtractRefusals(t *testing.T) {
	cases := []struct {
		name    string
		ids     []string
		members []member
		prepare func(d Dest)
		want    string
	}{
		{"an id that is none", []string{"../etc"}, nil, nil, "invalid"},
		{"the web trees' directory", []string{"www"}, nil, nil, "invalid"},
		{"the NEO Server", []string{"97NeoServer"}, nil, nil, "invalid"},
		{"a wrapper's twin", []string{"x.script"}, nil, nil, "invalid"},
		{"an addon that is here", []string{"a"}, []member{file("usr/local/etc/config/rc.d/a", "x", 0o755)}, func(d Dest) { _ = os.MkdirAll(filepath.Join(d.Home, "a"), 0o755) }, "exists"},
		{"an rc.d entry that is here", []string{"a"}, []member{file("usr/local/etc/config/rc.d/a", "x", 0o755)}, func(d Dest) {
			_ = os.MkdirAll(d.RCDir, 0o755)
			_ = os.WriteFile(filepath.Join(d.RCDir, "a.script"), nil, 0o755)
		}, "exists"},
		{"no rc.d entry in the backup", []string{"a"}, []member{file("usr/local/addons/a/x", "x", 0o644)}, nil, "no-entry"},
		{"an entry that leads elsewhere", []string{"a"}, []member{link("usr/local/etc/config/rc.d/a", "/usr/local/bin/hack.sh"), file("usr/local/addons/a/x", "x", 0o644)}, nil, "entry"},
		{"an entry that leads into another addon", []string{"a"}, []member{link("usr/local/etc/config/rc.d/a", "/usr/local/addons/b/rc")}, nil, "entry"},
		{"an entry that climbs", []string{"a"}, []member{link("usr/local/etc/config/rc.d/a", "/usr/local/addons/a/../../../bin/sh")}, nil, "entry"},
		{"an entry that leads to a directory", []string{"a"}, []member{link("usr/local/etc/config/rc.d/a", "/usr/local/addons/a/bin"), dir("usr/local/addons/a/bin/")}, nil, "entry"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			d := DestUnder(root)
			if c.prepare != nil {
				c.prepare(d)
			}
			res, err := Extract(sbk(t, c.members...), c.ids, d)
			if err != nil {
				t.Fatal(err)
			}
			if res[0].Imported || res[0].Error != c.want {
				t.Fatalf("%+v, want %s", res[0], c.want)
			}
			if c.prepare == nil {
				for _, p := range []string{d.Home, d.RCDir, d.WWWDir, d.Config} {
					if e, _ := os.ReadDir(p); len(e) > 0 && !(p == d.Config && len(e) == 1 && e[0].Name() == "www") {
						t.Errorf("%s holds %v after a refusal", p, e[0].Name())
					}
				}
			}
		})
	}
}

// What an archive may try: none of it leaves the addon's own trees.
func TestExtractStaysInside(t *testing.T) {
	root := t.TempDir()
	d := DestUnder(root)
	outside := filepath.Join(root, "outside")
	_ = os.MkdirAll(outside, 0o755)
	res, err := Extract(sbk(t,
		file("usr/local/etc/config/rc.d/a", "#!/bin/sh\n", 0o755),
		// a link out of the tree, then a member through it
		link("usr/local/addons/a/out", outside),
		file("usr/local/addons/a/out/planted", "x", 0o755),
		link("usr/local/addons/a/rel", "../../../outside"),
		file("usr/local/addons/a/rel/planted2", "x", 0o755),
		// the directory itself as a link
		link("usr/local/addons/b", outside),
		// climbing names
		file("usr/local/addons/a/../../etc/passwd", "x", 0o644),
		file("/usr/local/addons/a/abs", "x", 0o644),
		// a web entry and a config directory that lead elsewhere
		link("usr/local/etc/config/addons/www/a", "/etc"),
		link("usr/local/etc/config/addons/a", "/etc"),
		// what is no file, directory or link
		other("usr/local/addons/a/hard", tar.TypeLink),
		other("usr/local/addons/a/dev", tar.TypeChar),
		other("usr/local/addons/a/fifo", tar.TypeFifo),
		// a directory in rc.d, something below an rc.d entry
		dir("usr/local/etc/config/rc.d/a.d/"),
		file("usr/local/addons/a/ok", "fine", 0o644),
	), []string{"a", "b"}, d)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Imported || res[0].Files != 1 || res[0].Skipped < 6 {
		t.Errorf("a: %+v", res[0])
	}
	if res[1].Imported || res[1].Error != "no-entry" {
		t.Errorf("b: %+v", res[1])
	}
	if e, _ := os.ReadDir(outside); len(e) != 0 {
		t.Errorf("written outside: %v", e[0].Name())
	}
	for _, p := range []string{filepath.Join(d.WWWDir, "a"), filepath.Join(d.Config, "a"), filepath.Join(d.Home, "b"), filepath.Join(d.Home, "a/hard"), filepath.Join(d.Home, "a/dev"), filepath.Join(d.Home, "a/abs"), filepath.Join(root, "usr/local/etc/passwd"), filepath.Join(root, "usr/etc/passwd")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s exists", strings.TrimPrefix(p, root))
		}
	}
	if b, _ := os.ReadFile(filepath.Join(d.Home, "a/ok")); string(b) != "fine" {
		t.Errorf("the plain file is missing")
	}
}

func TestExtractCleansUpAfterABrokenArchive(t *testing.T) {
	d := DestUnder(t.TempDir())
	good := sbk(t, file("usr/local/etc/config/rc.d/a", "x", 0o755), file("usr/local/addons/a/big", randomText(1<<16), 0o644))
	b := make([]byte, good.Len())
	_, _ = good.Read(b)
	res, err := Extract(bytes.NewReader(b[:512+good.Size()/8]), []string{"a"}, d)
	if err == nil {
		t.Fatalf("a cut archive was taken: %+v", res)
	}
	if res[0].Imported || res[0].Error != "failed" {
		t.Errorf("%+v", res[0])
	}
	for _, p := range []string{filepath.Join(d.Home, "a"), filepath.Join(d.RCDir, "a")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s was left behind", p)
		}
	}
	if _, err := Extract(strings.NewReader("no archive"), []string{"a"}, d); err == nil {
		t.Error("garbage was taken for a backup")
	}
}

func TestExtractOversizedScript(t *testing.T) {
	d := DestUnder(t.TempDir())
	_, err := Extract(sbk(t, file("usr/local/etc/config/rc.d/a", strings.Repeat("x", maxRCScript+1), 0o755)), []string{"a"}, d)
	if err == nil {
		t.Fatal("a 1 MB rc.d script was taken")
	}
}

// randomText does not compress to nothing, so a cut archive is cut inside its content.
func randomText(n int) string {
	b := make([]byte, n)
	x := uint32(1)
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return string(b)
}
