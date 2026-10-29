package priv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/addonunit"
)

// openccu-lite B-293: the generic operations reached files root runs or obeys - rc.d, the addon web
// trees, the addon policy drop-ins - through the /etc/config/, /usr/local/etc/config/ and
// /usr/local/addons/ prefixes. They are named-only now, in both spellings, together with what the
// links in rc.d and the web trees lead to; the four named operations do what the daemon needs
// there, and nothing else around them changes.
func TestRootObeyedFilesAreOutsideTheGenericOnes(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for _, d := range []string{
		"usr/local/etc/config/rc.d", "usr/local/etc/config/addons/www/plain", "usr/local/etc/config/addon-policy",
		"usr/local/addons/hm2mqtt/www", "usr/local/addons/hm2mqtt/etc", "usr/local/tmp", "etc",
	} {
		if err := os.MkdirAll(at(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"usr/local/etc/config/rc.d/hm2mqtt":                  "#!/bin/sh\n# wrapper\n",
		"usr/local/addons/hm2mqtt/etc/hm2mqtt.sh":            "#!/bin/sh\n# the addon's script\n",
		"usr/local/addons/hm2mqtt/www/index.cgi":             "#!/bin/tclsh\n",
		"usr/local/addons/hm2mqtt/data.json":                 "{}\n",
		"usr/local/etc/config/addons/www/plain/index.cgi":    "#!/bin/tclsh\n",
		"usr/local/etc/config/addon-policy/hm2mqtt.conf":     "# mode=root\n",
		"usr/local/etc/config/addon-policy/hm2mqtt.needs":    "none\n",
		"usr/local/etc/config/addon-policy/hm2mqtt.start":    "early\n",
		"usr/local/etc/config/addon-policy/hm2mqtt.json":     "{}\n",
		"usr/local/etc/config/addon-policy/ghost.conf.d.txt": "x\n",
		"usr/local/tmp/new":                                  "new\n",
	}
	for rel, c := range files {
		if err := os.WriteFile(at(rel), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(at("usr/local/etc/config/rc.d/hm2mqtt"), 0o755); err != nil {
		t.Fatal(err)
	}
	// the image's link, and the installer's: the addon's script behind the wrapper and its web tree
	// are links into its directory, root's in root's directories
	for link, target := range map[string]string{
		"etc/config": at("usr/local/etc/config"),
		"usr/local/etc/config/rc.d/hm2mqtt.script": at("usr/local/addons/hm2mqtt/etc/hm2mqtt.sh"),
		"usr/local/etc/config/addons/www/hm2mqtt":  at("usr/local/addons/hm2mqtt/www"),
	} {
		if err := os.Symlink(target, at(link)); err != nil {
			t.Fatal(err)
		}
	}
	pol := DefaultPolicy(root, "/usr/local/etc/occulite")
	srv := &Server{Policy: pol}
	ctx := context.Background()
	refused := func(req request) {
		t.Helper()
		if r := srv.do(ctx, req); !strings.HasPrefix(r.Error, "refused") {
			t.Errorf("%s %s%s%s -> %s: not refused: %+v", req.Op, req.Path, req.Src, req.Dst, req.Target, r)
		}
	}
	ok := func(req request) response {
		t.Helper()
		r := srv.do(ctx, req)
		if r.Error != "" {
			t.Errorf("%s %s%s%s -> %s: %s", req.Op, req.Path, req.Src, req.Dst, req.Target, r.Error)
		}
		return r
	}

	// every generic operation on every such file, in both spellings of the config directory, and
	// on what the links lead to
	var protected []string
	for _, dir := range []string{"etc/config/", "usr/local/etc/config/"} {
		protected = append(protected,
			dir+"rc.d/hm2mqtt", dir+"rc.d/hm2mqtt.script", dir+"rc.d/new-addon",
			dir+"addons/www/hm2mqtt", dir+"addons/www/hm2mqtt/index.cgi", dir+"addons/www/hm2mqtt/new.cgi",
			dir+"addons/www/plain/index.cgi", dir+"addons/www/new",
			dir+"addon-policy/hm2mqtt.conf", dir+"addon-policy/hm2mqtt.needs", dir+"addon-policy/hm2mqtt.start",
			dir+"addon-policy/other.conf")
	}
	protected = append(protected, "usr/local/addons/hm2mqtt/etc/hm2mqtt.sh", "usr/local/addons/hm2mqtt/www/index.cgi",
		"usr/local/addons/hm2mqtt/www/new.cgi", "www/addons/hm2mqtt/index.cgi")
	for _, rel := range protected {
		p := at(rel)
		refused(request{Op: "write", Path: p, Data: []byte("[Service]\nExecStartPre=+/bin/sh -c id\n"), Mode: 0o755})
		refused(request{Op: "touch", Path: p, Mode: 0o755})
		refused(request{Op: "chmod", Path: p, Mode: 0o755})
		refused(request{Op: "chmod", Path: p, Mode: 0o644})
		refused(request{Op: "chown", Path: p, UID: os.Getuid(), GID: os.Getgid()})
		refused(request{Op: "remove", Path: p})
		refused(request{Op: "removeall", Path: p})
		refused(request{Op: "mkdir", Path: p, Mode: 0o755})
		refused(request{Op: "rename", Src: p, Dst: at("usr/local/tmp/moved")})
		refused(request{Op: "rename", Src: at("usr/local/tmp/new"), Dst: p})
		refused(request{Op: "symlink", Path: p, Target: at("usr/local/tmp/new")})
		refused(request{Op: "symlink", Path: at("usr/local/tmp/link"), Target: p})
	}
	// the files are as they were
	for rel, c := range files {
		if got, err := os.ReadFile(at(rel)); err != nil || string(got) != c {
			t.Errorf("%s: %q %v", rel, got, err)
		}
	}
	if st, _ := os.Stat(at("usr/local/etc/config/rc.d/hm2mqtt")); st.Mode().Perm() != 0o755 {
		t.Errorf("rc.d mode %v", st.Mode().Perm())
	}

	// the directories that hold them, as a whole: moved away (the file written there, moved back),
	// removed, given away, opened up, or - missing - replaced by a link to a directory the daemon
	// fills
	for _, rel := range []string{
		"usr/local/etc/config/rc.d", "etc/config/rc.d", "usr/local/etc/config/addons/www", "usr/local/etc/config/addons",
		"usr/local/etc/config/addon-policy", "etc/config/addon-policy", "usr/local/addons/hm2mqtt", "usr/local/addons/hm2mqtt/www",
		"usr/local/addons/hm2mqtt/etc",
	} {
		refused(request{Op: "rename", Src: at(rel), Dst: at("usr/local/tmp/aside")})
		refused(request{Op: "rename", Src: at("usr/local/tmp/new"), Dst: at(rel)})
		refused(request{Op: "removeall", Path: at(rel)})
		refused(request{Op: "chmod", Path: at(rel), Mode: 0o777})
		refused(request{Op: "chown", Path: at(rel), UID: os.Getuid(), GID: os.Getgid(), Recursive: true})
	}
	if err := os.Rename(at("usr/local/etc/config/addon-policy"), at("usr/local/policy-aside")); err != nil {
		t.Fatal(err)
	}
	refused(request{Op: "symlink", Path: at("usr/local/etc/config/addon-policy"), Target: at("usr/local/tmp")})
	if err := os.Rename(at("usr/local/policy-aside"), at("usr/local/etc/config/addon-policy")); err != nil {
		t.Fatal(err)
	}
	// through a link the daemon made: the resolved path is the one checked
	ok(request{Op: "symlink", Path: at("usr/local/tmp/rclink"), Target: at("usr/local/etc/config")})
	refused(request{Op: "write", Path: at("usr/local/tmp/rclink/rc.d/evil"), Data: []byte("x"), Mode: 0o755})
	refused(request{Op: "write", Path: at("usr/local/tmp/rclink/addon-policy/hm2mqtt.conf"), Data: []byte("x"), Mode: 0o644})

	// what stays: the daemon's own files in the policy directory, other files of the prefixes, and
	// a take-away chmod of the trees' directories (cleanup.go's hardening). The addon's files
	// outside its web tree and script stayed too until B-294 (addonhome_test.go)
	ok(request{Op: "write", Path: at("etc/config/addon-policy/hm2mqtt.json"), Data: []byte("{\"id\":\"hm2mqtt\"}\n"), Mode: 0o644})
	ok(request{Op: "write", Path: at("usr/local/etc/config/addon-policy/hm2mqtt.manifest.json"), Data: []byte("{}\n"), Mode: 0o644})
	refused(request{Op: "write", Path: at("usr/local/addons/hm2mqtt/data.json"), Data: []byte("{\"a\":1}\n"), Mode: 0o644})
	ok(request{Op: "write", Path: at("usr/local/etc/config/rfd.conf"), Data: []byte("[rfd]\n"), Mode: 0o644})
	ok(request{Op: "chmod", Path: at("usr/local/etc/config/rc.d"), Mode: 0o750})
	refused(request{Op: "chmod", Path: at("usr/local/etc/config/rc.d"), Mode: 0o755})
	if err := os.Chmod(at("usr/local/etc/config/rc.d"), 0o755); err != nil {
		t.Fatal(err)
	}

	// the named operations: the policy files rendered by the helper from checked data
	conf := at("etc/config/addon-policy/hm2mqtt.conf")
	ok(request{Op: opAddonPolicyFile, Path: conf, PolicyFile: &addonunit.File{DropIn: &addonunit.DropIn{
		ID: "hm2mqtt", Mode: "confined", UID: 30001, Groups: []string{"certs"}, Capabilities: []string{"CAP_NET_BIND_SERVICE"},
	}}})
	got, _ := os.ReadFile(at("usr/local/etc/config/addon-policy/hm2mqtt.conf"))
	if !strings.Contains(string(got), "# mode=confined uid=30001\n[Service]\nUser=addon-hm2mqtt\nGroup=addon-hm2mqtt\nSupplementaryGroups=certs\n") {
		t.Errorf("drop-in:\n%s", got)
	}
	for name, f := range map[string]addonunit.File{
		// root's user, a uid of the system's, root-equivalent grants, a line slipped in
		"root uid":     {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "confined", UID: 0}},
		"system uid":   {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "confined", UID: 1000}},
		"helper group": {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "confined", UID: 30001, Groups: []string{"occulite"}}},
		"root group":   {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "confined", UID: 30001, Groups: []string{"root"}}},
		"sys admin":    {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "confined", UID: 30001, Capabilities: []string{"CAP_SYS_ADMIN"}}},
		"newline":      {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "confined", UID: 30001, Paths: []string{"/x\nExecStartPre=+/bin/sh"}}},
		"space":        {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "confined", UID: 30001, Paths: []string{"/x +/bin/sh"}}},
		"other id":     {DropIn: &addonunit.DropIn{ID: "other", Mode: "root"}},
		"root grants":  {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "root", Groups: []string{"dialout"}}},
		"mode":         {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "user"}},
		"needs":        {Needs: &[]string{"rfd"}},
		"two kinds":    {DropIn: &addonunit.DropIn{ID: "hm2mqtt", Mode: "root"}, Remove: true},
		"nothing":      {},
	} {
		if r := srv.do(ctx, request{Op: opAddonPolicyFile, Path: conf, PolicyFile: &f}); !strings.HasPrefix(r.Error, "refused") {
			t.Errorf("%s: not refused: %+v", name, r)
		}
	}
	if again, _ := os.ReadFile(at("usr/local/etc/config/addon-policy/hm2mqtt.conf")); string(again) != string(got) {
		t.Errorf("the drop-in changed:\n%s", again)
	}
	needs := at("usr/local/etc/config/addon-policy/hm2mqtt.needs")
	ok(request{Op: opAddonPolicyFile, Path: needs, PolicyFile: &addonunit.File{Needs: &[]string{"hmipserver", "rfd"}}})
	if b, _ := os.ReadFile(needs); string(b) != "rfd hmipserver\n" {
		t.Errorf("needs %q", b)
	}
	refused(request{Op: opAddonPolicyFile, Path: needs, PolicyFile: &addonunit.File{Needs: &[]string{"sshd"}}})
	refused(request{Op: opAddonPolicyFile, Path: needs, PolicyFile: &addonunit.File{Early: true}})
	start := at("usr/local/etc/config/addon-policy/hm2mqtt.start")
	ok(request{Op: opAddonPolicyFile, Path: start, PolicyFile: &addonunit.File{Remove: true}})
	if _, err := os.Lstat(start); err == nil {
		t.Error(".start is still there")
	}
	ok(request{Op: opAddonPolicyFile, Path: start, PolicyFile: &addonunit.File{Early: true}})
	if b, _ := os.ReadFile(start); string(b) != "early\n" {
		t.Errorf("start %q", b)
	}
	// only the three kinds, only in the policy directory
	for _, p := range []string{"usr/local/etc/config/addon-policy/hm2mqtt.json", "usr/local/tmp/hm2mqtt.conf", "usr/local/etc/config/rc.d/hm2mqtt",
		"usr/local/etc/config/addon-policy/../rc.d/x.conf", "usr/local/etc/config/addon-policy/sub/x.conf"} {
		refused(request{Op: opAddonPolicyFile, Path: at(p), PolicyFile: &addonunit.File{Remove: true}})
	}

	// an addon enabled and disabled: the executable bits and nothing else, of an entry and of nothing
	// but an entry
	ok(request{Op: opAddonEnable, Path: at("etc/config/rc.d/hm2mqtt"), Enabled: false})
	if st, _ := os.Stat(at("usr/local/etc/config/rc.d/hm2mqtt")); st.Mode().Perm() != 0o644 {
		t.Errorf("disabled: mode %v", st.Mode().Perm())
	}
	ok(request{Op: opAddonEnable, Path: at("usr/local/etc/config/rc.d/hm2mqtt"), Enabled: true})
	if st, _ := os.Stat(at("usr/local/etc/config/rc.d/hm2mqtt")); st.Mode().Perm() != 0o755 {
		t.Errorf("enabled: mode %v", st.Mode().Perm())
	}
	for _, p := range []string{"usr/local/etc/config/rc.d/hm2mqtt.script", "usr/local/tmp/new", "usr/local/etc/config/addons/www/plain/index.cgi", "usr/local/etc/config/rc.d"} {
		refused(request{Op: opAddonEnable, Path: at(p), Enabled: true})
	}

	// the uninstall's removal: an rc.d entry with its script and a web entry, nothing else
	refused(request{Op: opAddonRemove, Path: at("usr/local/etc/config/rc.d/hm2mqtt"), WWW: at("usr/local/tmp/new")})
	refused(request{Op: opAddonRemove, Path: at("usr/local/etc/config/rc.d/hm2mqtt.script")})
	refused(request{Op: opAddonRemove, Path: at("usr/local/tmp/new")})
	refused(request{Op: opAddonRemove, WWW: at("usr/local/etc/config/addons/www/hm2mqtt/index.cgi")})
	refused(request{Op: opAddonRemove})
	r := ok(request{Op: opAddonRemove, Path: at("etc/config/rc.d/hm2mqtt"), WWW: at("etc/config/addons/www/hm2mqtt")})
	if strings.Join(r.Names, " ") != at("etc/config/rc.d/hm2mqtt")+" "+at("etc/config/rc.d/hm2mqtt.script")+" "+at("etc/config/addons/www/hm2mqtt") {
		t.Errorf("removed %v", r.Names)
	}
	for _, p := range []string{"usr/local/etc/config/rc.d/hm2mqtt", "usr/local/etc/config/rc.d/hm2mqtt.script", "usr/local/etc/config/addons/www/hm2mqtt"} {
		if _, err := os.Lstat(at(p)); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	// the links went, not what they led to
	if _, err := os.Stat(at("usr/local/addons/hm2mqtt/www/index.cgi")); err != nil {
		t.Error(err)
	}
	// with the links gone the addon's directory is still no generic write's (B-294): the
	// uninstall removes it, once empty, through RemoveAddonHome
	refused(request{Op: "write", Path: at("usr/local/addons/hm2mqtt/www/index.cgi"), Data: []byte("x"), Mode: 0o644})
	// a web directory that is not empty stays unless whole is asked for
	if err := os.Symlink(at("usr/local/addons/hm2mqtt/etc/hm2mqtt.sh"), at("usr/local/etc/config/rc.d/plain")); err != nil {
		t.Fatal(err)
	}
	r = srv.do(ctx, request{Op: opAddonRemove, Path: at("usr/local/etc/config/rc.d/plain"), WWW: at("usr/local/etc/config/addons/www/plain")})
	if r.Error == "" || strings.HasPrefix(r.Error, "refused") {
		t.Errorf("a full web directory without whole: %+v", r)
	}
	ok(request{Op: opAddonRemove, Path: at("usr/local/etc/config/rc.d/plain"), WWW: at("usr/local/etc/config/addons/www/plain"), Recursive: true})
	if _, err := os.Lstat(at("usr/local/etc/config/addons/www/plain")); err == nil {
		t.Error("www/plain is still there")
	}
	if _, err := os.Stat(at("usr/local/addons/hm2mqtt/etc/hm2mqtt.sh")); err != nil {
		t.Error(err)
	}
}

// A link a confined addon planted in its own web tree (its user's, in its user's directory) is not
// followed: its CGIs run as its user, and following it would let the addon close any directory to
// the daemon.
func TestRootObeyedFollowsOnlyTrustedLinks(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the test's untrusted link needs an owner other than the helper's")
	}
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for _, d := range []string{"usr/local/etc/config/addons/www/a", "usr/local/etc/config/rc.d", "usr/local/tmp"} {
		if err := os.MkdirAll(at(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// a directory others may write makes its links untrusted (trustedLink)
	if err := os.Chmod(at("usr/local/etc/config/addons/www/a"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(at("usr/local/tmp"), at("usr/local/etc/config/addons/www/a/tmp")); err != nil {
		t.Fatal(err)
	}
	pol := DefaultPolicy(root, "/usr/local/etc/occulite")
	if pol.namedOnly("/usr/local/tmp/x") {
		t.Error("an untrusted link closed /usr/local/tmp")
	}
	// a trusted one does: what it leads to is the web tree's
	if err := os.Symlink(at("usr/local/tmp"), at("usr/local/etc/config/addons/www/b")); err != nil {
		t.Fatal(err)
	}
	if !pol.namedOnly("/usr/local/tmp/x") || !pol.holdsNamed("/usr/local/tmp") || !pol.holdsNamed("/usr/local") {
		t.Error("a trusted link's target is not named-only")
	}
}

// The policy files and the trees are named-only by their shape, not by a list of what is there.
func TestRootObeyedShapes(t *testing.T) {
	p := DefaultPolicy(t.TempDir(), "/usr/local/etc/occulite")
	for rel, want := range map[string]bool{
		"/usr/local/etc/config/addon-policy/x.conf":          true,
		"/etc/config/addon-policy/x.needs":                   true,
		"/etc/config/addon-policy/x.start":                   true,
		"/usr/local/etc/config/addon-policy/.conf":           true,
		"/usr/local/etc/config/addon-policy/x.json":          false,
		"/usr/local/etc/config/addon-policy/x.manifest.json": false,
		"/usr/local/etc/config/addon-policy/x.conf.bak":      false,
		"/usr/local/etc/config/addon-policy":                 false,
		"/usr/local/etc/config/rc.d/anything":                true,
		"/etc/config/rc.d/x.script":                          true,
		"/usr/local/etc/config/rc.d":                         false, // a holder: take-away chmod stays
		"/usr/local/etc/config/addons/www/x/deep/a.cgi":      true,
		"/www/addons/x/index.cgi":                            true,
		"/usr/local/etc/config/addons/x/settings":            true, // B-295: x is not confined
		"/usr/local/etc/config/rfd.conf":                     false,
	} {
		if got := p.namedOnly(rel); got != want {
			t.Errorf("namedOnly(%s) = %v", rel, got)
		}
	}
	for rel, want := range map[string]bool{
		"/usr/local/etc/config/addon-policy": true,
		"/etc/config/rc.d":                   true,
		"/usr/local/etc/config/addons":       true,
		"/usr/local/etc/config/addons/x":     false,
		"/usr/local/tmp":                     false,
	} {
		if got := p.holdsNamed(rel); got != want {
			t.Errorf("holdsNamed(%s) = %v", rel, got)
		}
	}
}

// openccu-lite B-294: /usr/local/addons/ was a write prefix, so a root addon's own code beyond its
// rc.d script target and its web tree - its bin/, its node_modules, what the script starts - could
// be replaced by a compromised daemon and was then run as root. No generic operation reaches the
// addons' directories now; the daemon's three cases there are named operations of one shape each,
// and a confined addon's directories (OwnTree, its data directories) are as they were.
func TestAddonHomesAreOutsideTheGenericOnes(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for _, d := range []string{
		"usr/local/etc/config/rc.d", "usr/local/etc/config/addons/www", "usr/local/tmp", "etc",
		"usr/local/addons/redmatic/bin", "usr/local/addons/redmatic/lib/node_modules/x", "usr/local/addons/redmatic/www",
		"usr/local/addons/hm2mqtt/etc", "usr/local/addons/mediola/bin", "usr/local/addons/mediola/www", "usr/local/addons/hmm",
		"usr/local/hmm",
	} {
		if err := os.MkdirAll(at(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"usr/local/etc/config/rc.d/redmatic":                    "#!/bin/sh\n# wrapper\n",
		"usr/local/etc/config/rc.d/hm2mqtt":                     "#!/bin/sh\n# wrapper\n",
		"usr/local/etc/config/rc.d/97NeoServer":                 "#!/bin/sh\n# wrapper\n",
		"usr/local/addons/redmatic/bin/redmaticLoader":          "#!/bin/sh\nexec bin/node lib/node_modules/x/index.js\n",
		"usr/local/addons/redmatic/bin/node":                    "\x7fELF",
		"usr/local/addons/redmatic/lib/node_modules/x/index.js": "require('child_process')\n",
		"usr/local/addons/redmatic/etc/settings.json":           "{}\n",
		"usr/local/addons/hm2mqtt/etc/hm2mqtt.env":              "A=1\n",
		"usr/local/addons/mediola/bin/watchdog":                 "#!/bin/sh\n",
		"usr/local/addons/mediola/VERSION":                      "2.10.1\n",
		"usr/local/tmp/new":                                     "new\n",
		"etc/passwd":                                            "root:x:0:0::/:/bin/sh\naddon-hmm:x:30002:30002::/usr/local/addons/hmm:/bin/false\n",
		"usr/local/etc/config/addon-policy/hmm.conf":            addonunit.DropIn{ID: "hmm", Mode: "confined", UID: 30002}.Render(),
	}
	for rel, c := range files {
		if err := os.MkdirAll(filepath.Dir(at(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(at(rel), []byte(c), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"etc/config": at("usr/local/etc/config"),
		"usr/local/etc/config/rc.d/redmatic.script":    at("usr/local/addons/redmatic/bin/redmaticLoader"),
		"usr/local/etc/config/addons/www/redmatic":     at("usr/local/addons/redmatic/www"),
		"usr/local/etc/config/rc.d/97NeoServer.script": at("usr/local/addons/mediola/bin/watchdog"),
		"usr/local/etc/config/addons/www/mediola":      at("usr/local/addons/mediola/www"),
	} {
		if err := os.Symlink(target, at(link)); err != nil {
			t.Fatal(err)
		}
	}
	pol := DefaultPolicy(root, "/usr/local/etc/occulite")
	srv := &Server{Policy: pol}
	ctx := context.Background()
	refused := func(req request) {
		t.Helper()
		if r := srv.do(ctx, req); !strings.HasPrefix(r.Error, "refused") {
			t.Errorf("%s %s%s%s -> %s: not refused: %+v", req.Op, req.Path, req.Src, req.Dst, req.Target, r)
		}
	}
	ok := func(req request) response {
		t.Helper()
		r := srv.do(ctx, req)
		if r.Error != "" {
			t.Errorf("%s %s%s%s -> %s: %s", req.Op, req.Path, req.Src, req.Dst, req.Target, r.Error)
		}
		return r
	}
	gone := func(rel string) bool {
		_, err := os.Lstat(at(rel))
		return os.IsNotExist(err)
	}

	// every generic operation on what the root addon runs beyond its script, on its other files,
	// and on a path that is not there yet (a new file in its bin/, a new addon's directory)
	for _, rel := range []string{
		"usr/local/addons/redmatic/bin/node", "usr/local/addons/redmatic/lib/node_modules/x/index.js",
		"usr/local/addons/redmatic/etc/settings.json", "usr/local/addons/redmatic/bin/new",
		"usr/local/addons/hm2mqtt/etc/hm2mqtt.env", "usr/local/addons/mediola/VERSION", "usr/local/addons/evil",
	} {
		p := at(rel)
		refused(request{Op: "write", Path: p, Data: []byte("#!/bin/sh\nid > /tmp/pwned\n"), Mode: 0o755})
		refused(request{Op: "touch", Path: p, Mode: 0o755})
		refused(request{Op: "chmod", Path: p, Mode: 0o755})
		refused(request{Op: "chmod", Path: p, Mode: 0o644})
		refused(request{Op: "chown", Path: p, UID: os.Getuid(), GID: os.Getgid()})
		refused(request{Op: "remove", Path: p})
		refused(request{Op: "removeall", Path: p})
		refused(request{Op: "mkdir", Path: p, Mode: 0o755})
		refused(request{Op: "rename", Src: p, Dst: at("usr/local/tmp/moved")})
		refused(request{Op: "rename", Src: at("usr/local/tmp/new"), Dst: p})
		refused(request{Op: "symlink", Path: p, Target: at("usr/local/tmp/new")})
		refused(request{Op: "symlink", Path: at("usr/local/tmp/link"), Target: p})
	}
	// the directories as a whole: the tree, an addon's directory, its bin/
	for _, rel := range []string{"usr/local/addons", "usr/local/addons/redmatic", "usr/local/addons/redmatic/bin", "usr/local/addons/hmm"} {
		refused(request{Op: "rename", Src: at(rel), Dst: at("usr/local/tmp/aside")})
		refused(request{Op: "rename", Src: at("usr/local/tmp/new"), Dst: at(rel)})
		refused(request{Op: "removeall", Path: at(rel)})
		refused(request{Op: "chmod", Path: at(rel), Mode: 0o777})
		refused(request{Op: "chmod", Path: at(rel), Mode: 0o700})
		refused(request{Op: "chown", Path: at(rel), UID: os.Getuid(), GID: os.Getgid(), Recursive: true})
		refused(request{Op: "mkdir", Path: at(rel + "/sub"), Mode: 0o755})
	}
	for rel, c := range files {
		if got, err := os.ReadFile(at(rel)); err != nil || string(got) != c {
			t.Errorf("%s: %q %v", rel, got, err)
		}
	}
	if !gone("usr/local/addons/evil") || !gone("usr/local/tmp/link") {
		t.Error("something was made")
	}

	// a confined addon's directories are as they were: its home and a data directory go through
	// OwnTree, a data directory is created and chowned as before (D-52)
	if err := pol.OwnTreeAllowed("hmm", []string{at("usr/local/addons/hmm"), at("usr/local/hmm")}, 30002); err != nil {
		t.Errorf("OwnTree: %v", err)
	}
	ok(request{Op: "mkdir", Path: at("usr/local/hmm/cache"), Mode: 0o755})
	ok(request{Op: "chown", Path: at("usr/local/hmm"), UID: os.Getuid(), GID: os.Getgid(), Recursive: true})

	// the uninstall's rmdir: an addon's directory, not while its rc.d entry is there, never with
	// anything in it, nothing but an addon's directory
	refused(request{Op: opAddonHomeRemove, Path: at("usr/local/addons/hm2mqtt")})
	if err := os.Remove(at("usr/local/etc/config/rc.d/hm2mqtt")); err != nil { // the uninstall's RemoveAddonEntry
		t.Fatal(err)
	}
	for _, p := range []string{"usr/local/addons", "usr/local/addons/hm2mqtt/etc", "usr/local/addons/hm2mqtt/etc/hm2mqtt.env",
		"usr/local/addons/redmatic", "usr/local/tmp/new", "usr/local/addons/hm2mqtt/../redmatic", "usr/local/hmm"} {
		refused(request{Op: opAddonHomeRemove, Path: at(p)})
	}
	if r := srv.do(ctx, request{Op: opAddonHomeRemove, Path: at("usr/local/addons/hm2mqtt")}); r.Error == "" || strings.HasPrefix(r.Error, "refused") {
		t.Errorf("a directory with something in it: %+v", r)
	}
	if gone("usr/local/addons/hm2mqtt/etc/hm2mqtt.env") {
		t.Fatal("the addon's file went")
	}
	if err := os.RemoveAll(at("usr/local/addons/hm2mqtt/etc")); err != nil { // the addon's own uninstall emptied it
		t.Fatal(err)
	}
	ok(request{Op: opAddonHomeRemove, Path: at("usr/local/addons/hm2mqtt")})
	if !gone("usr/local/addons/hm2mqtt") {
		t.Error("the emptied directory is still there")
	}
	// a link as the addon's directory: the link goes, not what it leads to
	if err := os.Symlink(at("usr/local/hmm"), at("usr/local/addons/linked")); err != nil {
		t.Fatal(err)
	}
	ok(request{Op: opAddonHomeRemove, Path: at("usr/local/addons/linked")})
	if !gone("usr/local/addons/linked") || gone("usr/local/hmm/cache") {
		t.Error("the link's removal")
	}

	// the NEO Server's switch: exactly its marker, an empty file in its existing directory
	for _, p := range []string{"usr/local/addons/mediola/bin/watchdog", "usr/local/addons/redmatic/Disabled", "usr/local/addons/mediola/Disabled2",
		"usr/local/addons/mediola/bin/../Disabled/x", "usr/local/tmp/Disabled"} {
		refused(request{Op: opNeoServerDisabled, Path: at(p)})
	}
	ok(request{Op: opNeoServerDisabled, Path: at(NeoServerDisabledMarker)})
	if st, err := os.Lstat(at(NeoServerDisabledMarker)); err != nil || !st.Mode().IsRegular() || st.Size() != 0 || st.Mode().Perm()&0o111 != 0 {
		t.Errorf("the marker: %v %v", st, err)
	}
	ok(request{Op: opNeoServerDisabled, Path: at(NeoServerDisabledMarker)}) // idempotent
	// a link in the marker's place is not written through
	if err := os.Remove(at(NeoServerDisabledMarker)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(at("usr/local/addons/redmatic/bin/node"), at(NeoServerDisabledMarker)); err != nil {
		t.Fatal(err)
	}
	if r := srv.do(ctx, request{Op: opNeoServerDisabled, Path: at(NeoServerDisabledMarker)}); r.Error == "" {
		t.Error("written through a link")
	}
	if err := os.Remove(at(NeoServerDisabledMarker)); err != nil {
		t.Fatal(err)
	}

	// the NEO Server's leftover: its directory with everything in it, only once its rc.d entry and
	// its web entry are gone, and nothing but its directory
	refused(request{Op: opNeoServerHomeRemove, Path: at(NeoServerHome)})
	for _, p := range []string{"usr/local/etc/config/rc.d/97NeoServer", "usr/local/etc/config/rc.d/97NeoServer.script"} {
		if err := os.Remove(at(p)); err != nil {
			t.Fatal(err)
		}
	}
	refused(request{Op: opNeoServerHomeRemove, Path: at(NeoServerHome)}) // the web entry is still there
	if err := os.Remove(at("usr/local/etc/config/addons/www/mediola")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"usr/local/addons/redmatic", "usr/local/addons/mediola/bin", "usr/local/addons", "usr/local/tmp"} {
		refused(request{Op: opNeoServerHomeRemove, Path: at(p)})
	}
	ok(request{Op: opNeoServerHomeRemove, Path: at(NeoServerHome)})
	if !gone(NeoServerHome) || gone("usr/local/addons/redmatic/bin/node") {
		t.Error("the NEO Server's removal")
	}
	// the marker needs the directory: it is never made
	if r := srv.do(ctx, request{Op: opNeoServerDisabled, Path: at(NeoServerDisabledMarker)}); r.Error == "" || !gone(NeoServerHome) {
		t.Errorf("the marker without its directory: %+v", r)
	}
}

// openccu-lite B-295: a root addon's config directory, <config>/addons/<id>, may hold what its
// script sources or runs (the Email addon's userscript.tcl, a settings file read with "."); the
// /usr/local/etc/config/ prefix reached it. It is named-only now unless the addon's drop-in - the
// helper's own text - confines the addon; the CCU's addons/mh and a new id are closed as well. A
// chmod that only takes bits away stays (the first boot's hardening), and the emptied directory is
// removed by RemoveAddonHome.
func TestRootAddonConfigDirsAreOutsideTheGenericOnes(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	files := map[string]string{
		"usr/local/etc/config/addons/jp/settings.conf":     "PORT=1\n",
		"usr/local/etc/config/addons/email/userscript.tcl": "puts x\n",
		"usr/local/etc/config/addons/mh/addcron.sh":        "#!/bin/sh\n",
		"usr/local/etc/config/addons/mh/html/index.html":   "<html>",
		"usr/local/etc/config/addons/hmm/x":                "x\n",
		"usr/local/etc/config/rc.d/jp":                     "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/email":                  "#!/bin/sh\n",
		"usr/local/etc/config/addon-policy/jp.conf":        addonunit.DropIn{ID: "jp", Mode: "root"}.Render(),
		"usr/local/etc/config/addon-policy/hmm.conf":       addonunit.DropIn{ID: "hmm", Mode: "confined", UID: 30002}.Render(),
		"usr/local/tmp/new":                                "new\n",
	}
	for rel, c := range files {
		if err := os.MkdirAll(filepath.Dir(at(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(at(rel), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// B-118: only empty directories below it - emptied as well
	if err := os.MkdirAll(at("usr/local/etc/config/addons/empty/etc/deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(at("usr/local/etc/config/addons/mh/html"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(at("etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(at("usr/local/etc/config"), at("etc/config")); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite")}
	ctx := context.Background()
	refused := func(req request) {
		t.Helper()
		if r := srv.do(ctx, req); !strings.HasPrefix(r.Error, "refused") {
			t.Errorf("%s %s%s%s -> %s: not refused: %+v", req.Op, req.Path, req.Src, req.Dst, req.Target, r)
		}
	}
	ok := func(req request) {
		t.Helper()
		if r := srv.do(ctx, req); r.Error != "" {
			t.Errorf("%s %s%s%s -> %s: %s", req.Op, req.Path, req.Src, req.Dst, req.Target, r.Error)
		}
	}
	// the root addon's (a root drop-in), one without a drop-in, the CCU's mh, a new id - in both
	// spellings, the files, a new file, and the directories themselves
	for _, dir := range []string{"etc/config/addons/", "usr/local/etc/config/addons/"} {
		for _, rel := range []string{"jp/settings.conf", "jp/new.sh", "email/userscript.tcl", "mh/addcron.sh", "mh/html/index.html", "new/x", "jp", "email", "new"} {
			p := at(dir + rel)
			refused(request{Op: "write", Path: p, Data: []byte("#!/bin/sh\nid > /tmp/pwned\n"), Mode: 0o755})
			refused(request{Op: "touch", Path: p, Mode: 0o755})
			refused(request{Op: "chmod", Path: p, Mode: 0o777})
			refused(request{Op: "chown", Path: p, UID: os.Getuid(), GID: os.Getgid()})
			refused(request{Op: "remove", Path: p})
			refused(request{Op: "removeall", Path: p})
			refused(request{Op: "mkdir", Path: p + "/sub", Mode: 0o755})
			refused(request{Op: "rename", Src: p, Dst: at("usr/local/tmp/moved")})
			refused(request{Op: "rename", Src: at("usr/local/tmp/new"), Dst: p})
			refused(request{Op: "symlink", Path: p + ".link", Target: at("usr/local/tmp/new")})
			refused(request{Op: "symlink", Path: at("usr/local/tmp/link"), Target: p})
		}
	}
	for rel, c := range files {
		if got, err := os.ReadFile(at(rel)); err != nil || string(got) != c {
			t.Errorf("%s: %q %v", rel, got, err)
		}
	}
	// the hardening's chmod takes a bit away, never adds one
	ok(request{Op: "chmod", Path: at("usr/local/etc/config/addons/mh/html"), Mode: 0o775})
	refused(request{Op: "chmod", Path: at("usr/local/etc/config/addons/mh/html"), Mode: 0o777})
	// a confined addon's config directory is the daemon's as before (SetPolicy makes it)
	ok(request{Op: "write", Path: at("etc/config/addons/hmm/x"), Data: []byte("y\n"), Mode: 0o644})
	ok(request{Op: "mkdir", Path: at("usr/local/etc/config/addons/hmm/sub"), Mode: 0o755})
	// the confined drop-in rendered through the named operation opens jp's directory; root closes it
	jpConf := at("usr/local/etc/config/addon-policy/jp.conf")
	ok(request{Op: opAddonPolicyFile, Path: jpConf, PolicyFile: &addonunit.File{DropIn: &addonunit.DropIn{ID: "jp", Mode: "confined", UID: 30003}}})
	ok(request{Op: "write", Path: at("usr/local/etc/config/addons/jp/settings.conf"), Data: []byte("PORT=2\n"), Mode: 0o644})
	ok(request{Op: opAddonPolicyFile, Path: jpConf, PolicyFile: &addonunit.File{DropIn: &addonunit.DropIn{ID: "jp", Mode: "root"}}})
	refused(request{Op: "write", Path: at("usr/local/etc/config/addons/jp/settings.conf"), Data: []byte("PORT=3\n"), Mode: 0o644})
	// a drop-in that is a link to a confined one does not count
	if err := os.Symlink(at("usr/local/etc/config/addon-policy/hmm.conf"), at("usr/local/etc/config/addon-policy/email.conf")); err != nil {
		t.Fatal(err)
	}
	refused(request{Op: "write", Path: at("usr/local/etc/config/addons/email/userscript.tcl"), Data: []byte("x"), Mode: 0o644})

	// the emptied directory: removed by the named operation, never one with content, never while the
	// addon's rc.d entry is there
	ok(request{Op: opAddonHomeRemove, Path: at("usr/local/etc/config/addons/empty")})
	if _, err := os.Lstat(at("usr/local/etc/config/addons/empty")); err == nil {
		t.Error("the empty config directory is still there")
	}
	refused(request{Op: opAddonHomeRemove, Path: at("usr/local/etc/config/addons/jp")})
	refused(request{Op: opAddonHomeRemove, Path: at("usr/local/etc/config/addons/www")})
	refused(request{Op: opAddonHomeRemove, Path: at("usr/local/etc/config/addons/mh/html")})
	if r := srv.do(ctx, request{Op: opAddonHomeRemove, Path: at("etc/config/addons/mh")}); r.Error == "" || strings.HasPrefix(r.Error, "refused") {
		t.Errorf("a config directory with content: %+v", r)
	}
	// the NEO Server's config directory goes with its leftover
	if err := os.MkdirAll(at("usr/local/etc/config/addons/mediola/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	refused(request{Op: "removeall", Path: at(NeoServerConfig)})
	ok(request{Op: opNeoServerHomeRemove, Path: at(NeoServerConfig)})
	if _, err := os.Lstat(at(NeoServerConfig)); err == nil {
		t.Error("the NEO Server's config directory is still there")
	}
}
