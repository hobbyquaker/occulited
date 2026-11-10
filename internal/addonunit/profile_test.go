package addonunit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// occulited task 136: the snippet is the image's own PATH pattern with the addon's directory, and
// nothing of the addon's choosing beyond the checked id and path.
func TestProfileSnippet(t *testing.T) {
	got, err := ProfileSnippet("tools", "bin")
	if err != nil {
		t.Fatal(err)
	}
	want := "# openccu-lite addon tools: runtime.bin of its manifest, the addon's bin directory at the end of\n" +
		"# PATH in interactive login shells, while it is installed and switched on and the directory is root's\n" +
		"# and writable by nobody else. Written by occulited, removed with the addon; not a file to edit.\n" +
		"_occulite_bin_ok() {\n" +
		"\tset -- $(command ls -ldn \"$1\" 2>/dev/null)\n" +
		"\tcase \"$1:$3\" in d????-??-?*:0) return 0 ;; esac\n" +
		"\treturn 1\n" +
		"}\n" +
		"case $- in\n" +
		"*i*)\n" +
		"\tif _occulite_bin_ok \"/usr/local/addons/tools/bin\"; then\n" +
		"\t\texport PATH=\"${PATH:+${PATH}:}/usr/local/addons/tools/bin\"\n" +
		"\tfi\n" +
		"\t;;\n" +
		"esac\n" +
		"unset -f _occulite_bin_ok\n"
	if got != want {
		t.Errorf("snippet:\n%s\nwant:\n%s", got, want)
	}
	if got, _ := ProfileSnippet("hm2mqtt", "opt/node/bin"); !strings.Contains(got, "\t\texport PATH=\"${PATH:+${PATH}:}/usr/local/addons/hm2mqtt/opt/node/bin\"\n") {
		t.Errorf("nested: %q", got)
	}
	for _, c := range []struct{ id, bin string }{
		{"", "bin"}, {"../x", "bin"}, {"a b", "bin"}, {"a\"b", "bin"}, {"$(id)", "bin"}, {strings.Repeat("a", 33), "bin"},
		{"tools", ""}, {"tools", "/bin"}, {"tools", "../bin"}, {"tools", "bin/"}, {"tools", "b\"in"}, {"tools", "${x}"}, {"tools", "`id`"},
	} {
		if s, err := ProfileSnippet(c.id, c.bin); err == nil {
			t.Errorf("%q %q: rendered %q", c.id, c.bin, s)
		}
	}
}

func TestProfileName(t *testing.T) {
	if ProfileName("tools") != "addon-tools.sh" {
		t.Error(ProfileName("tools"))
	}
	for name, want := range map[string]string{
		"addon-tools.sh": "tools", "addon-a.b-c_d.sh": "a.b-c_d",
		"addon-.sh": "", "tools.sh": "", "addon-tools": "", "addon-tools.sh.bak": "", "addon-a b.sh": "", "usr-local-path.sh": "",
	} {
		id, ok := ProfileID(name)
		if id != want || ok != (want != "") {
			t.Errorf("%s: %q %v", name, id, ok)
		}
	}
}

// The snippet in a real shell: only an interactive shell, only a directory that is root's and that
// nobody else may write, appended at the end, and the shell's own positional parameters untouched.
// The addon's path is swapped for directories every Linux has: /usr (root's, 0755), /tmp (root's,
// but world-writable), the test's own directory (not root's).
func TestProfileSnippetInAShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	if st, err := os.Stat("/usr"); err != nil || st.Mode().Perm()&0o022 != 0 {
		t.Skip("/usr is not 0755 here")
	}
	snippet, _ := ProfileSnippet("tools", "bin")
	run := func(dir string, interactive bool) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "addon-tools.sh")
		if err := os.WriteFile(f, []byte(strings.ReplaceAll(snippet, "/usr/local/addons/tools/bin", dir)), 0o644); err != nil {
			t.Fatal(err)
		}
		args := []string{}
		if interactive {
			args = append(args, "-i")
		}
		args = append(args, "-c", `set -- one two; . "$0"; echo "$PATH|$1 $2|$(type _occulite_bin_ok >/dev/null 2>&1 && echo left)"`, f)
		cmd := exec.Command(sh, args...)
		cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + t.TempDir(), "ENV="}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return lines[len(lines)-1]
	}
	if got := run("/usr", true); got != "/bin:/usr/bin:/usr|one two|" {
		t.Errorf("root's directory, interactive: %q", got)
	}
	if got := run("/usr", false); got != "/bin:/usr/bin|one two|" {
		t.Errorf("not interactive: %q", got)
	}
	if got := run("/tmp", true); got != "/bin:/usr/bin|one two|" {
		t.Errorf("a world-writable directory: %q", got)
	}
	if os.Geteuid() != 0 {
		if got := run(t.TempDir(), true); got != "/bin:/usr/bin|one two|" {
			t.Errorf("a directory of another user: %q", got)
		}
	}
	if got := run("/nonexistent/bin", true); got != "/bin:/usr/bin|one two|" {
		t.Errorf("a missing directory: %q", got)
	}
}
