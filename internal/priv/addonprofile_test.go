package priv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"github.com/hobbyquaker/occulited/internal/ownwalk"
)

// occulited task 136: the profile snippet is a named operation of exactly one shape - the daemon
// names the snippet, the addon's directory and the relative bin; the helper writes its own text,
// only for a bin that is a directory inside the addon's tree, and nothing generic reaches the
// profile directory.
func TestAddonProfileOperation(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for _, d := range []string{"usr/local/addons/tools/bin", "usr/local/addons/tools/sub/real", "usr/local/addons/other/bin", "usr/local/tmp", "usr/local/etc", "outside"} {
		if err := os.MkdirAll(at(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(at("usr/local/addons/tools/file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"usr/local/addons/tools/inside": "sub/real",            // stays in the tree
		"usr/local/addons/tools/escape": "../../../../outside", // leaves it
		"usr/local/addons/tools/abs":    at("outside"),         // leaves it, absolute
		"usr/local/addons/linked":       at("usr/local/addons/tools"),
	} {
		if err := os.Symlink(target, at(link)); err != nil {
			t.Fatal(err)
		}
	}
	walked := fakeRootBin(t, nil)
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite")}
	ctx := context.Background()
	snippet := at("usr/local/etc/profile.d/addon-tools.sh")
	home := at("usr/local/addons/tools")
	do := func(req request) response {
		t.Helper()
		req.Op = opAddonProfile
		return srv.do(ctx, req)
	}

	// written: the directory made 0755, the file 0644, the helper's text
	if r := do(request{Path: snippet, Dir: home, Target: "bin"}); r.Error != "" {
		t.Fatalf("write: %s", r.Error)
	}
	want, _ := addonunit.ProfileSnippet("tools", "bin")
	if got, err := os.ReadFile(snippet); err != nil || string(got) != want {
		t.Fatalf("snippet %q %v", got, err)
	}
	if st, _ := os.Stat(snippet); st.Mode().Perm() != 0o644 {
		t.Errorf("snippet mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(at("usr/local/etc/profile.d")); !st.IsDir() || st.Mode().Perm() != 0o755 {
		t.Errorf("directory mode %v", st.Mode())
	}
	// the directory was given to root before the snippet was written, and checked after it; no
	// adoption unless the request asks for it (the install's)
	if len(*walked) != 2 || (*walked)[0] != home+" bin false false" || (*walked)[1] != home+" bin true false" {
		t.Errorf("the root walk: %v", *walked)
	}
	*walked = nil
	if r := do(request{Path: snippet, Dir: home, Target: "bin", Recursive: true}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if len(*walked) != 2 || (*walked)[0] != home+" bin false true" || (*walked)[1] != home+" bin true false" {
		t.Errorf("the install's root walk: %v", *walked)
	}
	// refused at the boundary, the snippet as it was
	before, _ := os.ReadFile(snippet)
	for _, req := range []request{
		{Path: at("usr/local/etc/profile.d/tools.sh"), Dir: home, Target: "bin"},           // not addon-<id>.sh
		{Path: at("usr/local/etc/profile.d/addon-tools.conf"), Dir: home, Target: "bin"},   // not .sh
		{Path: at("usr/local/etc/profile.d/sub/addon-tools.sh"), Dir: home, Target: "bin"}, // deeper
		{Path: at("usr/local/etc/addon-tools.sh"), Dir: home, Target: "bin"},               // another directory
		{Path: at("etc/profile.d/addon-tools.sh"), Dir: home, Target: "bin"},               // the image's
		{Path: "/elsewhere/usr/local/etc/profile.d/addon-tools.sh", Dir: home, Target: "bin"},
		{Path: at("usr/local/etc/profile.d/addon-.sh"), Dir: at("usr/local/addons/"), Target: "bin"},
		{Path: snippet, Dir: at("usr/local/addons/other"), Target: "bin"},      // another addon's tree
		{Path: snippet, Dir: at("usr/local/addons/tools/sub"), Target: "real"}, // not the tree itself
		{Path: snippet, Dir: root + "/usr/local/addons/../addons/tools", Target: "bin"},
		{Path: snippet, Dir: home, Target: "/usr/bin"},
		{Path: snippet, Dir: home, Target: "../other/bin"},
		{Path: snippet, Dir: home, Target: "bin/../../other/bin"},
		{Path: snippet, Dir: home, Target: "./bin"},
		{Path: snippet, Dir: home, Target: "bin/"},
		{Path: snippet, Dir: home, Target: "b in"},
		{Path: snippet, Dir: home, Target: "bin\"; id; \""},
		{Path: snippet, Dir: home, Target: "$(id)"},
	} {
		if r := do(req); !strings.HasPrefix(r.Error, "refused") {
			t.Errorf("%s %s %q: not refused: %+v", req.Path, req.Dir, req.Target, r)
		}
	}
	if got, _ := os.ReadFile(snippet); string(got) != string(before) {
		t.Errorf("a refusal changed the snippet: %q", got)
	}
	// passed the boundary, failed on the installed tree: the snippet goes, PATH names nothing that
	// does not pass
	for _, bin := range []string{"inside", "escape", "abs", "file", "missing"} {
		if r := do(request{Path: snippet, Dir: home, Target: "bin"}); r.Error != "" {
			t.Fatal(r.Error)
		}
		r := do(request{Path: snippet, Dir: home, Target: bin})
		if r.Error == "" || strings.HasPrefix(r.Error, "refused") {
			t.Errorf("%s: %+v", bin, r)
		}
		if _, err := os.Lstat(snippet); !os.IsNotExist(err) {
			t.Errorf("%s: the snippet stayed", bin)
		}
	}
	// a directory the walk cannot make root's alone: no snippet, the one there removed
	if r := do(request{Path: snippet, Dir: home, Target: "bin"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	fakeRootBin(t, func(dry bool) ownwalk.Result {
		return ownwalk.Result{ErrorCount: 1, Errors: []string{"bin/x: a link to \"/bin/sh\""}}
	})
	if r := do(request{Path: snippet, Dir: home, Target: "bin"}); !strings.Contains(r.Error, "not made root's") {
		t.Errorf("an unclean directory: %+v", r)
	}
	if _, err := os.Lstat(snippet); !os.IsNotExist(err) {
		t.Error("the snippet stayed for an unclean directory")
	}
	fakeRootBin(t, func(dry bool) ownwalk.Result {
		if dry {
			return ownwalk.Result{Wrong: 1, FirstWrong: home + "/bin/new"}
		}
		return ownwalk.Result{}
	})
	if r := do(request{Path: snippet, Dir: home, Target: "bin"}); !strings.Contains(r.Error, "not root's alone") {
		t.Errorf("a directory the check finds wrong: %+v", r)
	}
	fakeRootBin(t, nil)
	// an addon whose directory is a link is not followed
	linked := at("usr/local/etc/profile.d/addon-linked.sh")
	if r := do(request{Path: linked, Dir: at("usr/local/addons/linked"), Target: "bin"}); r.Error == "" {
		t.Error("a linked addon directory was taken")
	}
	// removed: an empty bin, no directory needed; a missing snippet is no error
	if r := do(request{Path: snippet, Dir: home, Target: "bin"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if r := do(request{Path: snippet}); r.Error != "" {
		t.Errorf("remove: %s", r.Error)
	}
	if _, err := os.Lstat(snippet); !os.IsNotExist(err) {
		t.Error("not removed")
	}
	if r := do(request{Path: snippet}); r.Error != "" {
		t.Errorf("remove again: %s", r.Error)
	}
	if r := do(request{Path: at("usr/local/etc/profile.d/other.sh")}); !strings.HasPrefix(r.Error, "refused") {
		t.Errorf("removing another file: %+v", r)
	}

	// nothing generic reaches the profile directory, nor through a link the daemon made
	if err := os.WriteFile(at("usr/local/tmp/new"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{snippet, at("usr/local/etc/profile.d/evil.sh"), at("usr/local/etc/profile.d")} {
		for _, req := range []request{
			{Op: "write", Path: p, Data: []byte("id\n"), Mode: 0o644},
			{Op: "touch", Path: p, Mode: 0o644},
			{Op: "mkdir", Path: p, Mode: 0o755},
			{Op: "chmod", Path: p, Mode: 0o777},
			{Op: "chown", Path: p, UID: os.Getuid(), GID: os.Getgid()},
			{Op: "remove", Path: p},
			{Op: "removeall", Path: p},
			{Op: "symlink", Path: p, Target: at("usr/local/tmp/new")},
			{Op: "rename", Src: at("usr/local/tmp/new"), Dst: p},
		} {
			if r := srv.do(ctx, req); !strings.HasPrefix(r.Error, "refused") {
				t.Errorf("%s %s%s: not refused: %+v", req.Op, req.Path, req.Dst, r)
			}
		}
	}
	if r := srv.do(ctx, request{Op: "symlink", Path: at("usr/local/tmp/plink"), Target: at("usr/local/etc/profile.d")}); !strings.HasPrefix(r.Error, "refused") {
		t.Errorf("a link to the profile directory: %+v", r)
	}
	if _, err := os.Lstat(at("usr/local/etc/profile.d/evil.sh")); !os.IsNotExist(err) {
		t.Error("evil.sh was written")
	}
}

// An unchanged snippet is not rewritten (its inode stays), a changed one is.
func TestAddonProfileKeepsAnUnchangedSnippet(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "usr/local/addons/tools")
	for _, d := range []string{"bin", "sbin"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "usr/local/etc/profile.d/addon-tools.sh")
	fakeRootBin(t, nil)
	l := Local{noFollow: true}
	if err := l.AddonProfile(path, home, "bin", false); err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(path)
	if err := l.AddonProfile(path, home, "bin", false); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(path)
	if !os.SameFile(st1, st2) {
		t.Error("an unchanged snippet was rewritten")
	}
	if err := l.AddonProfile(path, home, "sbin", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "${PATH:+${PATH}:}/usr/local/addons/tools/sbin\"") {
		t.Errorf("not rewritten: %q", b)
	}
	// a name that is no snippet, a home of another id
	if err := l.AddonProfile(filepath.Join(root, "usr/local/etc/profile.d/x.sh"), home, "bin", false); err == nil {
		t.Error("x.sh taken")
	}
	if err := l.AddonProfile(filepath.Join(root, "usr/local/etc/profile.d/addon-other.sh"), home, "bin", false); err == nil {
		t.Error("another addon's home taken")
	}
}

// fakeRootBin stands in for ownwalk.RootBin, which needs root: it records "<home> <bin> <dry run> <adopt>"
// and answers what answer gives (nil: a clean directory).
func fakeRootBin(t *testing.T, answer func(dry bool) ownwalk.Result) *[]string {
	t.Helper()
	var calls []string
	old := RootBinWalk
	RootBinWalk = func(home, bin string, opt ownwalk.Options) ownwalk.Result {
		calls = append(calls, fmt.Sprintf("%s %s %v %v", home, bin, opt.DryRun, opt.Adopt))
		if answer == nil {
			return ownwalk.Result{}
		}
		return answer(opt.DryRun)
	}
	t.Cleanup(func() { RootBinWalk = old })
	return &calls
}

// occulited task 136: the helper's ownership walk for an addon leaves its runtime.bin out, decided
// from the stored manifest whatever the request says.
func TestOwnTreeExcludesBin(t *testing.T) {
	root := t.TempDir()
	pol := filepath.Join(root, AddonPolicyDir)
	if err := os.MkdirAll(pol, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pol, "tools"+AddonManifestSuffix), []byte(`{"format":1,"id":"tools","name":"Tools","runtime":{"bin":"bin"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := AddonBinDir(root, "tools"); got != filepath.Join(root, "usr/local/addons/tools/bin") {
		t.Errorf("AddonBinDir %q", got)
	}
	for _, id := range []string{"other", "../tools", ""} {
		if got := AddonBinDir(root, id); got != "" {
			t.Errorf("%q: %q", id, got)
		}
	}
	// another addon's manifest under this name, a manifest without bin
	if err := os.WriteFile(filepath.Join(pol, "fake"+AddonManifestSuffix), []byte(`{"format":1,"id":"tools","name":"T","runtime":{"bin":"bin"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := AddonBinDir(root, "fake"); got != "" {
		t.Errorf("another addon's manifest: %q", got)
	}
	// the dispatch: the request names no exclusion, the helper adds bin's
	root = ownTreeRoot(t)
	pol = filepath.Join(root, AddonPolicyDir)
	if err := os.WriteFile(filepath.Join(pol, "hmm"+AddonManifestSuffix), []byte(`{"format":1,"id":"hmm","name":"HMM","runtime":{"bin":"bin"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &ownTreeRecorder{}
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite"), Ops: rec}
	if r := srv.do(context.Background(), request{Op: opOwnTree, Name: "hmm", UID: 30007, Args: []string{filepath.Join(root, "usr/local/addons/hmm")}}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if len(rec.opts) != 1 || len(rec.opts[0].Exclude) != 1 || rec.opts[0].Exclude[0] != filepath.Join(root, "usr/local/addons/hmm/bin") {
		t.Errorf("the walk's options: %+v", rec.opts)
	}
}

type ownTreeRecorder struct {
	Local
	opts []OwnTreeOptions
}

func (r *ownTreeRecorder) OwnTree(_ string, _ []string, _ int, opt OwnTreeOptions) (ownwalk.Result, error) {
	r.opts = append(r.opts, opt)
	return ownwalk.Result{}, nil
}

// occulited task 136: at the uninstall the addon's user cannot empty its root-owned runtime.bin, so
// the helper's removal of the emptied addon directory takes bin with it - once the rc.d entry is
// gone, and only bin: anything else the uninstall left keeps the directory.
func TestAddonHomeRemoveTakesBin(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for _, d := range []string{"usr/local/addons/tools/opt/bin/sub", "usr/local/addons/kept/bin", "usr/local/etc/config/rc.d", AddonPolicyDir[1:]} {
		if err := os.MkdirAll(at(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for rel, c := range map[string]string{
		"usr/local/addons/tools/opt/bin/htop":       "x",
		"usr/local/addons/tools/opt/bin/sub/y":      "y",
		"usr/local/addons/kept/bin/htop":            "x",
		"usr/local/addons/kept/config.json":         "{}",
		"usr/local/etc/config/rc.d/kept.unused":     "",
		AddonPolicyDir[1:] + "/tools.manifest.json": `{"format":1,"id":"tools","name":"T","runtime":{"bin":"opt/bin"}}`,
		AddonPolicyDir[1:] + "/kept.manifest.json":  `{"format":1,"id":"kept","name":"K","runtime":{"bin":"bin"}}`,
	} {
		if err := os.WriteFile(at(rel), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{Policy: DefaultPolicy(root, "/usr/local/etc/occulite")}
	ctx := context.Background()
	// still installed: refused, nothing removed
	if err := os.WriteFile(at("usr/local/etc/config/rc.d/tools"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if r := srv.do(ctx, request{Op: opAddonHomeRemove, Path: at("usr/local/addons/tools")}); !strings.HasPrefix(r.Error, "refused") {
		t.Errorf("an installed addon: %+v", r)
	}
	if _, err := os.Stat(at("usr/local/addons/tools/opt/bin/htop")); err != nil {
		t.Error("bin went while the addon was installed")
	}
	if err := os.Remove(at("usr/local/etc/config/rc.d/tools")); err != nil {
		t.Fatal(err)
	}
	if r := srv.do(ctx, request{Op: opAddonHomeRemove, Path: at("usr/local/addons/tools")}); r.Error != "" {
		t.Fatalf("uninstalled: %s", r.Error)
	}
	if _, err := os.Lstat(at("usr/local/addons/tools")); !os.IsNotExist(err) {
		t.Error("the addon's directory stayed")
	}
	// an addon that left its configuration: bin goes, the directory stays with the rest
	if r := srv.do(ctx, request{Op: opAddonHomeRemove, Path: at("usr/local/addons/kept")}); r.Error == "" {
		t.Error("a directory with a file left in it was removed")
	}
	if _, err := os.Lstat(at("usr/local/addons/kept/bin")); !os.IsNotExist(err) {
		t.Error("bin stayed")
	}
	if _, err := os.Stat(at("usr/local/addons/kept/config.json")); err != nil {
		t.Error("the configuration went")
	}
}
