package priv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// task 29: the helper removes exactly the explicit remnants and checks their conditions itself.
func TestRemoveCCURemnant(t *testing.T) {
	root := t.TempDir()
	w := func(p, s string) string {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(s), 0o644)
		return full
	}
	c, _ := logListHelper(t, root, nil)
	mh := filepath.Dir(w("usr/local/etc/config/addons/mh/client.conf", "x"))
	if err := c.RemoveCCURemnant(mh); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mh); !os.IsNotExist(err) {
		t.Fatal("mh kept")
	}
	// an addon that is installed (its directory, or its rc.d entry) keeps its config directory
	hmm := filepath.Dir(w("usr/local/etc/config/addons/hmm/hmm.env", "x"))
	w("usr/local/addons/hmm/bin/node", "x")
	red := filepath.Dir(w("usr/local/etc/config/addons/red/x", "x"))
	w("usr/local/etc/config/rc.d/red.script", "#!/bin/sh\n")
	www := filepath.Dir(w("usr/local/etc/config/addons/www/x/i.html", "x"))
	for _, p := range []string{hmm, red, filepath.Dir(www), filepath.Join(root, "usr/local/etc/config/rfd.conf"), filepath.Join(root, "usr/local/etc/config/addons")} {
		if err := c.RemoveCCURemnant(p); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", p, err)
		}
	}
	// the crash dump
	if err := c.RemoveCCURemnant(w("usr/local/etc/config/homematic.regadom.err", "0123")); err != nil {
		t.Fatal(err)
	}
	// eQ-3-Backup: only without a byte in it
	full := w("usr/local/eQ-3-Backup/last/x.sbk", "data")
	if err := c.RemoveCCURemnant(filepath.Join(root, "usr/local/eQ-3-Backup")); !errors.Is(err, ErrRefused) {
		t.Fatalf("a backup with data: %v", err)
	}
	_ = os.Remove(full)
	if err := c.RemoveCCURemnant(filepath.Join(root, "usr/local/eQ-3-Backup")); err != nil {
		t.Fatal(err)
	}
}
