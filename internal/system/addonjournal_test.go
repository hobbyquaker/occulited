package system

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/journald"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// fakeJournal is a unixgram socket in a temp dir standing in for journald's.
type fakeJournal struct {
	t    *testing.T
	sock string
	conn *net.UnixConn
}

func newFakeJournal(t *testing.T) *fakeJournal {
	t.Helper()
	// a short directory of its own: t.TempDir() carries the (sub)test's name, and under a long
	// TMPDIR the socket's path went past the 108 bytes a unix socket address holds (B-48)
	dir, err := os.MkdirTemp("", "j")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "j.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &fakeJournal{t: t, sock: sock, conn: conn}
}

// run reads entries up to and including a run's summary (the entry with ADDON_EXIT, or the one
// that says what failed).
func (j *fakeJournal) run() []map[string]string {
	j.t.Helper()
	var out []map[string]string
	buf := make([]byte, 1<<20)
	_ = j.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		n, err := j.conn.Read(buf)
		if err != nil {
			j.t.Fatalf("after %d entries: %v", len(out), err)
		}
		fields, err := journald.Decode(buf[:n])
		if err != nil {
			j.t.Fatal(err)
		}
		m := map[string]string{}
		for _, f := range fields {
			m[f.Key] = f.Value
		}
		out = append(out, m)
		if _, ok := m["ADDON_EXIT"]; ok || m["PRIORITY"] == "3" && strings.Contains(m["MESSAGE"], "failed:") {
			return out
		}
	}
}

// nothing says no entry arrives for a while.
func (j *fakeJournal) nothing() {
	j.t.Helper()
	_ = j.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 1<<16)
	if n, err := j.conn.Read(buf); err == nil {
		j.t.Errorf("an entry arrived: %q", buf[:n])
	}
}

// The install paths write the output and a summary into the journal: the id from the catalogue's
// context or from the rc.d entry the installer created, none when neither says it.
func TestAddonInstallJournal(t *testing.T) {
	for _, c := range []struct {
		name    string
		ctxID   string // WithAddonID, as the catalogue install passes it
		script  string // the fake install_addon's body; RC is the rc.d directory
		noInst  bool   // no /bin/install_addon at all: Install fails before a script runs
		id      string // ADDON_ID wanted; "" = the field is absent
		lines   []string
		summary string
		prio    string
		exit    string // "" = no ADDON_EXIT
	}{
		{
			name:    "an upload of a new addon: the id is the rc.d entry it created",
			script:  "echo unpacking\nprintf 'step 1\\rstep 2\\r\\n'\necho\nprintf '#!/bin/sh\\nexit 0\\n' > $RC/fresh\nchmod +x $RC/fresh\necho done\nexit 0\n",
			id:      "fresh",
			lines:   []string{"unpacking", "step 2", "done"},
			summary: "fresh: install finished, exit 0 (installed)",
			prio:    "6",
			exit:    "0",
		},
		{
			name:    "a catalogue install that fails: the id from the context, priority err",
			ctxID:   "hm2mqtt",
			script:  "echo 'update_script: no space left on device' >&2\nexit 1\n",
			id:      "hm2mqtt",
			lines:   []string{"update_script: no space left on device"},
			summary: "hm2mqtt: install finished, exit 1 (update_script failed with exit code 1)",
			prio:    "3",
			exit:    "1",
		},
		{
			name:    "an upload that fails before touching rc.d: no id to give",
			script:  "echo 'not an addon'\nexit 104\n",
			lines:   []string{"not an addon"},
			summary: "install finished, exit 104 (archive has no executable update_script at its top level)",
			prio:    "3",
			exit:    "104",
		},
		{
			name:    "reboot required is a success",
			ctxID:   "cuxd",
			script:  "echo 'reboot now'\nexit 10\n",
			id:      "cuxd",
			lines:   []string{"reboot now"},
			summary: "cuxd: install finished, exit 10 (installed, reboot required)",
			prio:    "6",
			exit:    "10",
		},
		{
			name:    "no installer: the error is the summary",
			ctxID:   "mosquitto",
			noInst:  true,
			id:      "mosquitto",
			summary: "mosquitto: install failed: this system has no /bin/install_addon",
			prio:    "3",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := rootWith(t, map[string]string{"usr/local/tmp/.keep": ""})
			rc := r.join("/usr/local/etc/config/rc.d")
			_ = os.MkdirAll(rc, 0o755)
			if !c.noInst {
				_ = os.MkdirAll(r.join("/bin"), 0o755)
				_ = os.WriteFile(r.join("/bin/install_addon"), []byte("#!/bin/sh\nRC="+rc+"\n"+c.script), 0o755)
			}
			run := func(_ context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
			a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
			j := newFakeJournal(t)
			a.Journal = &journald.Writer{Socket: j.sock}
			ctx := context.Background()
			if c.ctxID != "" {
				ctx = WithAddonID(ctx, c.ctxID)
			}
			_, _ = a.Install(ctx, strings.NewReader(strings.Repeat("x", 100)))
			got := j.run()
			var lines []string
			for i, e := range got {
				if e["SYSLOG_IDENTIFIER"] != "addon-install" || e["ADDON_ACTION"] != "install" {
					t.Errorf("entry %d: %v", i, e)
				}
				if id, ok := e["ADDON_ID"]; id != c.id || ok != (c.id != "") {
					t.Errorf("entry %d: ADDON_ID %q (present %v), want %q", i, id, ok, c.id)
				}
				if i < len(got)-1 {
					if e["PRIORITY"] != "6" {
						t.Errorf("output line %d at priority %s", i, e["PRIORITY"])
					}
					if !strings.HasPrefix(e["MESSAGE"], "[systemd]") { // what occulited adds after the script
						lines = append(lines, e["MESSAGE"])
					}
				}
			}
			if fmt.Sprint(lines) != fmt.Sprint(c.lines) {
				t.Errorf("lines %q, want %q", lines, c.lines)
			}
			s := got[len(got)-1]
			if s["MESSAGE"] != c.summary || s["PRIORITY"] != c.prio || s["ADDON_EXIT"] != c.exit {
				t.Errorf("summary %v", s)
			}
		})
	}
}

// An uninstall goes the same way; a refused one (no such addon) writes nothing.
func TestAddonUninstallJournal(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/etc/config/rc.d/gone": "#!/bin/sh\necho stopping\necho removed\nexit 0\n",
		"usr/local/etc/config/rc.d/bad":  "#!/bin/sh\necho 'cannot remove'\nexit 2\n",
	})
	_ = os.Chmod(r.join("/usr/local/etc/config/rc.d/gone"), 0o755)
	_ = os.Chmod(r.join("/usr/local/etc/config/rc.d/bad"), 0o755)
	run := func(_ context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
	j := newFakeJournal(t)
	a.Journal = &journald.Writer{Socket: j.sock}

	for _, c := range []struct {
		id      string
		lines   string
		summary string
		prio    string
		exit    string
	}{
		// occulited B-26: after the script's lines what the system removed after it
		{"gone", "[stopping removed gone: the system then removed: /usr/local/etc/config/rc.d/gone]", "gone: uninstall finished, exit 0 (uninstalled)", "6", "0"},
		{"bad", "[cannot remove bad: the system then removed: /usr/local/etc/config/rc.d/bad]", "bad: uninstall finished, exit 2 (uninstall script failed)", "3", "2"},
	} {
		_, _ = a.Uninstall(context.Background(), c.id)
		got := j.run()
		var lines []string
		for _, e := range got[:len(got)-1] {
			lines = append(lines, e["MESSAGE"])
		}
		s := got[len(got)-1]
		if fmt.Sprint(lines) != c.lines || s["MESSAGE"] != c.summary || s["PRIORITY"] != c.prio || s["ADDON_EXIT"] != c.exit || s["ADDON_ID"] != c.id || s["ADDON_ACTION"] != "uninstall" || s["SYSLOG_IDENTIFIER"] != "addon-install" {
			t.Errorf("%s: lines %q, summary %v", c.id, lines, s)
		}
	}
	if _, err := a.Uninstall(context.Background(), "never-installed"); err == nil {
		t.Error("an unknown addon uninstalled")
	}
	j.nothing()
}

// Without a journal nothing is attempted, and the install is what it was.
func TestAddonInstallWithoutJournal(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/tmp/.keep": ""})
	_ = os.MkdirAll(r.join("/bin"), 0o755)
	_ = os.WriteFile(r.join("/bin/install_addon"), []byte("#!/bin/sh\necho hi\nexit 0\n"), 0o755)
	run := func(_ context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
	if res, err := a.Install(context.Background(), strings.NewReader(strings.Repeat("x", 100))); err != nil || res.Exit != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	// a socket that is not there: the install does not notice either
	a.Journal = &journald.Writer{Socket: filepath.Join(t.TempDir(), "none")}
	if res, err := a.Install(context.Background(), strings.NewReader(strings.Repeat("x", 100))); err != nil || res.Exit != 0 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestJournalLines(t *testing.T) {
	long := strings.Repeat("é", journalLineMax) // twice the limit in bytes
	many := strings.Repeat("line\n", journalLinesMax+5)
	for _, c := range []struct {
		name string
		in   string
		want func([]string) bool
	}{
		{"blank lines and trailing space go", "a  \n\n \t\nb\r\n", func(l []string) bool { return fmt.Sprint(l) == "[a b]" }},
		{"a redrawn line is its last state", "10%\r50%\r100%\n", func(l []string) bool { return fmt.Sprint(l) == "[100%]" }},
		{"nothing", "", func(l []string) bool { return len(l) == 0 }},
		{"a long line is split at a character", long, func(l []string) bool {
			return len(l) == 2 && len(l[0]) <= journalLineMax && l[0]+l[1] == long && strings.HasPrefix(l[1], "é")
		}},
		{"the last lines, and a note of the rest", many, func(l []string) bool {
			return len(l) == journalLinesMax+1 && l[0] == "(the first 5 lines of the output are left out here)" && l[1] == "line"
		}},
	} {
		if got := journalLines(c.in); !c.want(got) {
			t.Errorf("%s: %d lines %.200q", c.name, len(got), got)
		}
	}
}

// occulited B-26: a confined addon's uninstall runs as its user and its rm lines are refused; the
// journal says so after them, with what the system removed, and the answer's output stays the
// script's own.
func TestConfinedUninstallJournalSaysWhatTheSystemRemoved(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/etc/config/rc.d/mosq":              "#!/bin/sh\n# openccu-lite addon-rc wrapper\nexit 0\n",
		"usr/local/etc/config/addon-policy/mosq.json": `{"id":"mosq","mode":"confined","uid":30002,"user":"addon-mosq"}`,
		"usr/local/etc/config/hm_addons.cfg":          "mosq {CONFIG_URL /addons/mosq/index.html ID mosq CONFIG_NAME Mosq}\n",
	})
	refused := "rm: can't remove '/usr/local/etc/config/rc.d/mosq': Permission denied\n"
	fp := &fakePriv{out: priv.Result{Stdout: []byte(refused)}}
	old := Priv
	Priv = fp
	t.Cleanup(func() { Priv = old })
	run := func(_ context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
	j := newFakeJournal(t)
	a.Journal = &journald.Writer{Socket: j.sock}
	res, err := a.Uninstall(context.Background(), "mosq")
	if err != nil || res.Output != strings.TrimSpace(refused) {
		t.Fatalf("%v %+v", err, res)
	}
	got := j.run()
	if len(got) != 3 {
		t.Fatalf("entries %v", got)
	}
	want := "mosq: the uninstall ran as the addon's user addon-mosq, as a confined addon's does; the system then removed: " +
		"/usr/local/etc/config/rc.d/mosq, hm_addons.cfg: mosq, /usr/local/etc/config/addon-policy/mosq.json"
	if got[0]["MESSAGE"] != strings.TrimSpace(refused) || got[1]["MESSAGE"] != want || got[2]["MESSAGE"] != "mosq: uninstall finished, exit 0 (uninstalled)" {
		t.Errorf("journal:\n%q\n%q\n%q", got[0]["MESSAGE"], got[1]["MESSAGE"], got[2]["MESSAGE"])
	}
}
