package radio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openccu-lite B-309: hmipserver's stop removes the RXTX lock files of the nodes it was set up
// with when their process is gone - and only those: a live process's lock, a file without a PID,
// a link, and every other node's lock stay.
func TestStoppedRemovesStaleLocks(t *testing.T) {
	const live = "4242"
	type lock struct{ name, content string }
	cases := []struct {
		name   string
		crRFD  string
		hmMode string
		locks  []lock
		link   string // a lock that is a symlink
		gone   []string
		kept   []string
	}{
		{"HmIP only: the raw UART's stale lock goes", "Adapter.1.Type=HMIP_CCU2\nAdapter.1.Port=/dev/raw-uart1\n", "HM_HMIP_DEVNODE='/dev/raw-uart1'\n",
			[]lock{{"raw-uart1", "    122279\n"}}, "", []string{"raw-uart1"}, nil},
		{"dual stack: mmd_hmip's stale lock goes", "Adapter.1.Port=/dev/mmd_hmip\n", "HM_HMIP_DEVNODE='/dev/raw-uart1'\n",
			[]lock{{"mmd_hmip", "122279\n"}}, "", []string{"mmd_hmip"}, nil},
		{"the hm_mode node counts even when crRFD.conf names another", "Adapter.1.Port=/dev/mmd_hmip\n", "HM_HMIP_DEVNODE='/dev/raw-uart1'\n",
			[]lock{{"mmd_hmip", "1\n"}, {"raw-uart1", "122279\n"}}, "", []string{"mmd_hmip", "raw-uart1"}, nil},
		{"a live process keeps its lock", "Adapter.1.Port=/dev/raw-uart1\n", "",
			[]lock{{"raw-uart1", "      " + live + "\n"}}, "", nil, []string{"raw-uart1"}},
		{"no PID in the file: left alone", "Adapter.1.Port=/dev/raw-uart1\n", "",
			[]lock{{"raw-uart1", "garbage\n"}, {"ttyUSB0", ""}}, "", nil, []string{"raw-uart1", "ttyUSB0"}},
		{"another node's lock is not hmipserver's", "Adapter.1.Port=/dev/raw-uart1\n", "HM_HMIP_DEVNODE='/dev/raw-uart1'\n",
			[]lock{{"raw-uart", "122279\n"}, {"ttyUSB0", "122279\n"}, {"mmd_bidcos", "122279\n"}}, "", nil, []string{"raw-uart", "ttyUSB0", "mmd_bidcos"}},
		{"a USB adapter node from the config", "Adapter.1.Port=/dev/ttyUSB0\n", "",
			[]lock{{"ttyUSB0", "122279\n"}}, "", []string{"ttyUSB0"}, nil},
		{"a path outside /dev or with a slash is no node", "Adapter.1.Port=/tmp/x\nAdapter.2.Port=/dev/../run/lock/y\n", "",
			[]lock{{"x", "122279\n"}}, "", nil, []string{"x"}},
		{"a link is left alone", "Adapter.1.Port=/dev/raw-uart1\n", "", nil, "raw-uart1", nil, nil},
		{"nothing configured: nothing touched", "", "", []lock{{"raw-uart1", "122279\n"}}, "", nil, []string{"raw-uart1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			put := func(p, s string) {
				t.Helper()
				full := filepath.Join(root, p)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(s), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			put("var/etc/crRFD.conf", c.crRFD)
			put("var/hm_mode", c.hmMode)
			put("proc/"+live+"/status", "Name: java\n")
			for _, l := range c.locks {
				put("run/lock/LCK.."+l.name, l.content)
			}
			target := filepath.Join(root, "elsewhere")
			if c.link != "" {
				put("elsewhere", "122279\n")
				_ = os.MkdirAll(filepath.Join(root, "run/lock"), 0o755)
				if err := os.Symlink(target, filepath.Join(root, "run/lock/LCK.."+c.link)); err != nil {
					t.Fatal(err)
				}
			}
			var lines []string
			if err := Stopped(context.Background(), Detector{Root: root}, "hmipserver", logTo(&lines)); err != nil {
				t.Fatal(err)
			}
			for _, n := range c.gone {
				if _, err := os.Lstat(filepath.Join(root, "run/lock/LCK.."+n)); !os.IsNotExist(err) {
					t.Errorf("LCK..%s is still there; log %q", n, lines)
				}
			}
			for _, n := range c.kept {
				if _, err := os.Lstat(filepath.Join(root, "run/lock/LCK.."+n)); err != nil {
					t.Errorf("LCK..%s was removed; log %q", n, lines)
				}
			}
			if c.link != "" {
				if _, err := os.Lstat(filepath.Join(root, "run/lock/LCK.."+c.link)); err != nil {
					t.Error("the link was removed")
				}
				if _, err := os.Stat(target); err != nil {
					t.Error("the link's target was removed")
				}
			}
			if len(c.gone) > 0 && !strings.Contains(strings.Join(lines, "\n"), "removed the stale lock") {
				t.Errorf("no log line: %q", lines)
			}
		})
	}
	// another daemon's stop touches no lock
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "run/lock"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "var/etc"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "var/etc/crRFD.conf"), []byte("Adapter.1.Port=/dev/raw-uart1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "run/lock/LCK..raw-uart1"), []byte("122279\n"), 0o600)
	if err := Stopped(context.Background(), Detector{Root: root}, "multimacd", func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "run/lock/LCK..raw-uart1")); err != nil {
		t.Error("multimacd's stop removed hmipserver's lock")
	}
}
