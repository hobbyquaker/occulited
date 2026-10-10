package priv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// task 28: the helper removes one regular file an rc.d entry led to - under /usr/local/, outside
// the addons' tree, the configuration and the state; never a directory, never through a link.
func TestRemoveRCTarget(t *testing.T) {
	root := t.TempDir()
	w := func(p string) string {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o755)
		return full
	}
	c, _ := logListHelper(t, root, nil)
	hack := w("usr/local/bin/hack.sh")
	if err := c.RemoveRCTarget(hack); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hack); !os.IsNotExist(err) {
		t.Fatal("kept")
	}
	for _, p := range []string{"usr/local/addons/red/bin/node", "usr/local/etc/config/rfd.conf", "usr/local/var/lib/occulite/x.json", "etc/passwd"} {
		if err := c.RemoveRCTarget(w(p)); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", p, err)
		}
	}
	// a link under /usr/local/bin to the configuration is not followed out
	cfg := w("usr/local/etc/config/keep")
	link := filepath.Join(root, "usr/local/bin/link")
	_ = os.Symlink(cfg, link)
	if err := c.RemoveRCTarget(link); err == nil {
		t.Error("a link was removed")
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Error("followed the link")
	}
	// a directory is not a file
	_ = os.MkdirAll(filepath.Join(root, "usr/local/bin/dir"), 0o755)
	if err := c.RemoveRCTarget(filepath.Join(root, "usr/local/bin/dir")); err == nil {
		t.Error("a directory was removed")
	}
}
