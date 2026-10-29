package system

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func tarGz(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gz.Close()
	return &buf
}

func TestInstall(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(r.join("/bin"), 0o755)
	_ = os.MkdirAll(r.join("/tmp"), 0o755)
	// a fake install_addon that mirrors the firmware's contract: archive present → unpack → run update_script;
	// it unpacks inside the test's root, as its exit 104 leaves the directory behind (B-42)
	_ = os.WriteFile(r.join("/bin/install_addon"), []byte("#!/bin/sh\n[ -f "+r.join("/usr/local/tmp/new_addon.tar.gz")+" ] || exit 101\nD=$(mktemp -d -p "+r.join("/tmp")+")\ntar -C $D -xzf "+r.join("/usr/local/tmp/new_addon.tar.gz")+" || exit 102\nrm -f "+r.join("/usr/local/tmp/new_addon.tar.gz")+"\n[ -x $D/update_script ] || exit 104\n(cd $D && ./update_script HM-RASPBERRYMATIC); R=$?\nrm -rf $D\nexit $R\n"), 0o755)
	b := AddonScripts{Root: r}
	res, err := b.Install(t.Context(), tarGz(t, map[string]string{"update_script": "#!/bin/sh\necho hello from update_script\nexit 0\n"}))
	if err != nil || res.Exit != 0 || res.RebootRequired || res.Meaning != "installed" {
		t.Fatalf("install: %v %+v", err, res)
	}
	res, err = b.Install(t.Context(), tarGz(t, map[string]string{"update_script": "#!/bin/sh\nexit 10\n"}))
	if err != nil || res.Exit != 10 || !res.RebootRequired {
		t.Fatalf("reboot required: %v %+v", err, res)
	}
	res, err = b.Install(t.Context(), tarGz(t, map[string]string{"README": "no update_script here"}))
	if err != nil || res.Exit != 104 || !strings.Contains(res.Meaning, "update_script") {
		t.Fatalf("no update_script: %v %+v", err, res)
	}
	if _, err := b.Install(t.Context(), strings.NewReader("tiny")); err == nil {
		t.Fatal("an empty upload must be refused")
	}
	// occulited task 23: an addon whose fragment the validator refuses is told so in the output,
	// and the verdict stays on its entry until an install brings a fragment that passes
	frag := r.join("/usr/local/addons/badfrag/etc/lighttpd.conf")
	res, err = b.Install(t.Context(), tarGz(t, map[string]string{"update_script": "#!/bin/sh\nmkdir -p " + filepath.Dir(frag) + "\nprintf '# the frontend\\n$HTTP[\"url\"] =~ \"^/addons/badfrag/\" {\\n  proxy.server = ( \"\" => (\\n    ( \"host\" => \"10.0.0.1\", \"port\" => 80 )\\n  ))\\n}\\n' > " + frag + "\nexit 0\n"}))
	if err != nil || res.Exit != 0 {
		t.Fatalf("badfrag: %v %+v", err, res)
	}
	if !strings.Contains(res.Output, "[lighttpd] badfrag: the addon's lighttpd fragment was refused and is not in use: proxy.server may point at this system only, not at \"10.0.0.1\" (line 3: ") {
		t.Fatalf("the refusal is not in the output: %q", res.Output)
	}
	if rej := r.LighttpdRejection("badfrag"); rej == nil || rej.Line != 3 || !strings.HasPrefix(rej.Statement, "proxy.server") {
		t.Fatalf("badfrag's verdict: %+v", rej)
	}
	res, err = b.Install(t.Context(), tarGz(t, map[string]string{"update_script": "#!/bin/sh\nprintf 'url.redirect = ( \"^/addons/badfrag$\" => \"/addons/badfrag/\" )\\n' > " + frag + "\nexit 0\n"}))
	if err != nil || res.Exit != 0 || strings.Contains(res.Output, "[lighttpd]") {
		t.Fatalf("a good fragment: %v %+v", err, res)
	}
	if rej := r.LighttpdRejection("badfrag"); rej != nil {
		t.Fatalf("the verdict stayed: %+v", rej)
	}
	if _, err := os.Stat(r.join(lighttpdDropinDir + "/badfrag.conf")); err != nil {
		t.Fatalf("no copy of the good fragment: %v", err)
	}
	if _, err := os.Stat(r.join("/usr/local/tmp/new_addon.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("archive must not linger")
	}
}

// B-4: an archive the install API staged already is moved into place as it is, not copied again;
// the staging refuses what Install refuses
func TestInstallStagedArchive(t *testing.T) {
	r := fakeRoot(t)
	_ = os.MkdirAll(r.join("/bin"), 0o755)
	_ = os.MkdirAll(r.join("/tmp"), 0o755)
	_ = os.WriteFile(r.join("/bin/install_addon"), []byte("#!/bin/sh\nD=$(mktemp -d -p "+r.join("/tmp")+")\ntar -C $D -xzf "+r.join("/usr/local/tmp/new_addon.tar.gz")+" || exit 102\nrm -f "+r.join("/usr/local/tmp/new_addon.tar.gz")+"\n(cd $D && ./update_script); R=$?\nrm -rf $D\nexit $R\n"), 0o755)
	sa, err := StageAddonArchive(r, "upload-x.tar.gz", tarGz(t, map[string]string{"update_script": "#!/bin/sh\nexit 0\n"}))
	if err != nil || sa.Size < 64 {
		t.Fatalf("stage: %v %+v", err, sa)
	}
	res, err := AddonScripts{Root: r}.Install(t.Context(), sa)
	if err != nil || res.Exit != 0 {
		t.Fatalf("install: %v %+v", err, res)
	}
	if _, err := os.Stat(sa.Path); !os.IsNotExist(err) {
		t.Fatal("the staged archive was copied, not moved")
	}
	sa.Remove() // nothing left: no error, no panic
	if _, err := StageAddonArchive(r, "upload-y.tar.gz", strings.NewReader("tiny")); err == nil {
		t.Fatal("an empty upload must be refused")
	}
	if left, _ := filepath.Glob(r.join(StagingDir + "/upload-*")); len(left) != 0 {
		t.Fatalf("a refused upload left %v", left)
	}
	// an installer that does not know the type reads it
	sa, _ = StageAddonArchive(r, "upload-z.tar.gz", strings.NewReader(strings.Repeat("z", 70)))
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(sa); err != nil || buf.Len() != 70 {
		t.Fatalf("read: %v %d", err, buf.Len())
	}
	sa.Remove()
	if _, err := os.Stat(sa.Path); !os.IsNotExist(err) {
		t.Fatal("Remove left the file")
	}
}

func TestUninstallAndCfgWriter(t *testing.T) {
	r := fakeRoot(t)
	b := AddonScripts{Root: r}
	// mosquitto's rc.d script from fakeRoot handles "uninstall" by echoing; add a monit fragment
	_ = os.WriteFile(r.join("/usr/local/etc/monit-mosquitto.cfg"), []byte("check process"), 0o644)
	out, err := b.Uninstall(t.Context(), "mosquitto")
	if err != nil || out.Output != "mosquitto uninstall" {
		t.Fatalf("uninstall: %+v %v", out, err)
	}
	if _, err := os.Stat(r.join("/usr/local/etc/monit-mosquitto.cfg")); !os.IsNotExist(err) {
		t.Fatal("monit fragment must be removed")
	}
	// B-283: the answer names what the system removed - what was there, in order - so the
	// confined script's refused rm lines do not read as a failure
	if got := strings.Join(out.SystemRemoved, " "); !strings.HasPrefix(got, "/usr/local/etc/config/rc.d/mosquitto ") || !strings.Contains(got, "/usr/local/etc/monit-mosquitto.cfg") || !strings.HasSuffix(got, "hm_addons.cfg: mosquitto") {
		t.Fatalf("system_removed: %v", out.SystemRemoved)
	}
	cfg := readFile(r.join("/usr/local/etc/config/hm_addons.cfg"))
	if strings.Contains(cfg, "mosquitto") {
		t.Fatalf("hm_addons.cfg entry must be removed: %q", cfg)
	}
	if _, err := b.Uninstall(t.Context(), "../etc"); err == nil {
		t.Fatal("path-like ids must be refused")
	}
	if _, err := b.Uninstall(t.Context(), "nope"); err == nil {
		t.Fatal("unknown addon must be refused")
	}
	// the writer round-trips through the parser, braces and nested braces included
	in := map[string]AddonSettings{
		"a": {ConfigURL: "/addons/a/s.cgi", Name: "A Name", Description: map[string]string{"de": "<li>x {y}</li>", "en": "<li>z</li>"}},
		"b": {ConfigURL: "/addons/b/s.cgi", Name: "B", Description: map[string]string{}},
	}
	text := WriteHMAddonsCfg(in)
	back := ParseHMAddonsCfg(text)
	if back["a"].Name != "A Name" || back["a"].Description["de"] != "<li>x {y}</li>" || back["b"].ConfigURL != "/addons/b/s.cgi" || back["b"].Name != "B" {
		t.Fatalf("round trip: %q -> %+v", text, back)
	}
}

func TestParseInfoKeepsUnknownKeysOut(t *testing.T) {
	a := parseInfo("x", "Name: X\nVersion: 1\nFoo: bar\nOperations: restart\n")
	if a.Name != "X" || a.Version != "1" || len(a.Operations) != 1 {
		t.Fatalf("%+v", a)
	}
	_ = filepath.Join
}

// occulited B-118: longpressdim's uninstall left its settings link www/longpressdim-config (named
// other than its id) and its emptied etc/ and var/. Every www link into the addon's own directory
// goes - by its text, the tree may be gone - and a directory left with only empty directories in
// it. Another addon's link, a link elsewhere and a directory with a file stay.
func TestUninstallRemovesOtherWWWLinksAndEmptyDirs(t *testing.T) {
	r := fakeRoot(t)
	b := AddonScripts{Root: r}
	www := r.join(AddonWWW)
	for _, d := range []string{AddonWWW, "/usr/local/addons/mosquitto/etc", "/usr/local/addons/mosquitto/var/run", "/usr/local/addons/other/www", "/usr/local/etc/config/addons/mosquitto/empty"} {
		if err := os.MkdirAll(r.join(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"mosquitto-config": "/usr/local/addons/mosquitto/www",         // box path, dangling
		"mosquitto-rel":    "../../../../addons/mosquitto/www/config", // relative, dangling
		"mosquitto-joined": r.join("/usr/local/addons/mosquitto/etc"), // the test root's spelling, resolves
		"other-config":     "/usr/local/addons/other/www",
		"elsewhere":        "/tmp",
		"mosquittoX":       "/usr/local/addons/mosquittoX/www", // a prefix is not the tree
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(www, name)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := b.Uninstall(t.Context(), "mosquitto")
	if err != nil {
		t.Fatalf("uninstall: %+v %v", out, err)
	}
	for _, gone := range []string{"mosquitto-config", "mosquitto-rel", "mosquitto-joined"} {
		if _, err := os.Lstat(filepath.Join(www, gone)); !os.IsNotExist(err) {
			t.Errorf("www/%s is still there", gone)
		}
		if !slices.Contains(out.SystemRemoved, AddonWWW+"/"+gone) {
			t.Errorf("system_removed lacks www/%s: %v", gone, out.SystemRemoved)
		}
	}
	for _, kept := range []string{"other-config", "elsewhere", "mosquittoX"} {
		if _, err := os.Lstat(filepath.Join(www, kept)); err != nil {
			t.Errorf("www/%s went: %v", kept, err)
		}
	}
	for _, gone := range []string{"/usr/local/addons/mosquitto", "/usr/local/etc/config/addons/mosquitto"} {
		if _, err := os.Lstat(r.join(gone)); !os.IsNotExist(err) {
			t.Errorf("%s (only empty directories) is still there", gone)
		}
	}

	// a directory with a file below stays, with the directories above it; its empty siblings go
	b2 := AddonScripts{Root: r}
	_ = os.WriteFile(r.join("/usr/local/etc/config/rc.d/keep"), []byte("#!/bin/sh\n"), 0o755)
	for _, d := range []string{"/usr/local/addons/keep/etc", "/usr/local/addons/keep/var/data"} {
		_ = os.MkdirAll(r.join(d), 0o755)
	}
	_ = os.WriteFile(r.join("/usr/local/addons/keep/var/data/settings.json"), []byte("{}"), 0o644)
	if _, err := b2.Uninstall(t.Context(), "keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.join("/usr/local/addons/keep/var/data/settings.json")); err != nil {
		t.Errorf("the kept settings went: %v", err)
	}
	if _, err := os.Lstat(r.join("/usr/local/addons/keep/etc")); !os.IsNotExist(err) {
		t.Errorf("the empty etc/ beside them is still there")
	}
}
