package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"github.com/hobbyquaker/occulited/internal/ownwalk"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
)

// task 107: `occulited -addon-own <id>`, what the fork's addon units run before every start. The walk
// itself is ownwalk's (tested there, symlinks, races and mounts included); here the walk is a fake
// that answers what a test wants, and the subcommand is checked for what it walks, what it says in
// the journal, and (task 110) when it walks the whole tree.

type ownRig struct {
	root  string
	calls []ownwalk.Options
	dirs  [][]string
	// answers are the results of the walks, in order; the last one repeats
	answers []ownwalk.Result
	out     bytes.Buffer
	errOut  bytes.Buffer
}

func newOwnRig(t *testing.T, policy string) *ownRig {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		"etc/passwd":                                 "root:x:0:0::/:/bin/sh\naddon-hmm:x:30007:30007::/usr/local/addons/hmm:/bin/false\naddon-other:x:30008:30008::/usr/local/addons/other:/bin/false\n",
		"usr/local/addons/hmm/var/x":                 "",
		"usr/local/etc/config/addons/hmm/.keep":      "",
		"usr/local/hmm/token":                        "",
		"usr/local/addons/other/x":                   "",
		"usr/local/etc/config/addon-policy/hmm.json": policy,
		// B-295: the helper's walk asks the drop-in it rendered
		"usr/local/etc/config/addon-policy/hmm.conf":   addonunit.DropIn{ID: "hmm", Mode: "confined", UID: 30007}.Render(),
		"usr/local/etc/config/addon-policy/other.json": `{"id":"other","mode":"confined","uid":30008,"user":"addon-other","source":"catalog","data_dirs":["/usr/local/shared"]}`,
		"usr/local/etc/config/rc.d/hmm":                "#!/bin/sh\n",
		"usr/local/etc/config/rc.d/other":              "#!/bin/sh\n",
		"usr/local/shared/x":                           "",
	} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &ownRig{root: root}
}

func (r *ownRig) run(args ...string) int {
	r.out.Reset()
	r.errOut.Reset()
	r.calls, r.dirs = nil, nil
	return addonOwn(addonOwnEnv{
		root: system.Root(r.root),
		walk: func(dirs []string, opt ownwalk.Options) ownwalk.Result {
			r.calls = append(r.calls, opt)
			r.dirs = append(r.dirs, dirs)
			res := r.answers[0]
			if len(r.answers) > 1 {
				r.answers = r.answers[1:]
			}
			return res
		},
		policy: priv.DefaultPolicy(r.root, "/usr/local/etc/occulite"),
		stdout: &r.out,
		stderr: &r.errOut,
	}, args)
}

func (r *ownRig) markerPath() string {
	return filepath.Join(r.root, system.AddonPolicyDir, "hmm"+system.FullWalkSuffix)
}

// mark leaves the marker as occulited's install flow does (through the helper, root's)
func (r *ownRig) mark(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(r.markerPath(), []byte("install\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *ownRig) marked() bool {
	_, err := os.Lstat(r.markerPath())
	return err == nil
}

func isQuick(o ownwalk.Options) bool { return o.DryRun && o.MaxDepth == ownwalk.QuickDepth }

func isWhole(o ownwalk.Options) bool { return !o.DryRun && o.MaxDepth == 0 }

const confinedHmm = `{"id":"hmm","mode":"confined","uid":30007,"user":"addon-hmm","source":"catalog","data_dirs":["/usr/local/hmm"]}`

// RedMatic's update copied var/ as root, then started its unit: the install left the marker, so the
// start walks the whole tree, gives the entries back, says so with the count, and removes the marker.
// The next start takes the quick check only and says nothing.
func TestAddonOwnWholeWalkAfterAnInstallThenQuick(t *testing.T) {
	r := newOwnRig(t, confinedHmm)
	r.mark(t)
	r.answers = []ownwalk.Result{{Checked: 345, Wrong: 12, Fixed: 12, Duration: 250 * time.Millisecond}}
	if code := r.run("hmm"); code != 0 {
		t.Fatalf("exit %d: %s", code, r.out.String())
	}
	if got, want := r.out.String(), "addon-own: hmm: 12 of 345 entries given to addon-hmm (30007) in 0.25 s\n"; got != want {
		t.Errorf("journal %q, want %q", got, want)
	}
	wantDirs := []string{
		filepath.Join(r.root, "usr/local/addons/hmm"), filepath.Join(r.root, "usr/local/etc/config/addons/hmm"),
		filepath.Join(r.root, "usr/local/etc/config/addons/www/hmm"), filepath.Join(r.root, "usr/local/hmm"),
	}
	if len(r.calls) != 1 || !isWhole(r.calls[0]) || r.calls[0].UID != 30007 || r.calls[0].GID != 30007 ||
		strings.Join(r.dirs[0], " ") != strings.Join(wantDirs, " ") {
		t.Errorf("walks %+v over %v", r.calls, r.dirs)
	}
	if r.marked() {
		t.Error("the marker stays after the whole walk")
	}

	// the next start: the quick check over the same directories, everything right, nothing said
	r.answers = []ownwalk.Result{{Checked: 14, TooDeep: 5}}
	if code := r.run("hmm"); code != 0 || r.out.Len() != 0 || len(r.calls) != 1 || !isQuick(r.calls[0]) ||
		strings.Join(r.dirs[0], " ") != strings.Join(wantDirs, " ") || r.calls[0].UID != 30007 {
		t.Fatalf("quick: exit %d, journal %q, walks %+v", code, r.out.String(), r.calls)
	}

	// the whole walk after a switch to its own user that found everything right says so, once
	r.mark(t)
	r.answers = []ownwalk.Result{{Checked: 345, Duration: 310 * time.Millisecond}}
	if code := r.run("hmm"); code != 0 || len(r.calls) != 1 || !isWhole(r.calls[0]) || r.marked() {
		t.Fatalf("marked, all right: exit %d, walks %+v, marker %v", code, r.calls, r.marked())
	}
	if got, want := r.out.String(), "addon-own: hmm: the whole tree after an install or a policy change: 345 entries, all addon-hmm's, in 0.31 s\n"; got != want {
		t.Errorf("journal %q, want %q", got, want)
	}
}

// A wrong owner the quick check finds at the top brings the whole walk for this start: an update
// occulited did not run (install_addon over SSH) left var/ root's. It writes no marker.
func TestAddonOwnQuickCheckFindsAWrongOwner(t *testing.T) {
	r := newOwnRig(t, confinedHmm)
	r.answers = []ownwalk.Result{
		{Checked: 14, Wrong: 1, RootOwned: 1, FirstWrong: r.root + "/usr/local/addons/hmm/var"},
		{Checked: 345, Wrong: 20, Fixed: 20, Duration: time.Second},
	}
	if code := r.run("hmm"); code != 0 || len(r.calls) != 2 || !isQuick(r.calls[0]) || !isWhole(r.calls[1]) {
		t.Fatalf("exit %d, walks %+v: %s", code, r.calls, r.out.String())
	}
	want := "addon-own: hmm: the quick check found 1 entries to put right at the top, first " + r.root + "/usr/local/addons/hmm/var: the whole tree is walked\n" +
		"addon-own: hmm: 20 of 345 entries given to addon-hmm (30007) in 1.00 s\n"
	if r.out.String() != want {
		t.Errorf("journal %q, want %q", r.out.String(), want)
	}
	if r.marked() {
		t.Error("the fallback wrote a marker")
	}

	// a quick check that did not look everywhere walks the whole tree too
	r.answers = []ownwalk.Result{{Checked: 2, ErrorCount: 1, Errors: []string{"x: input/output error"}}, {Checked: 345}}
	if code := r.run("hmm"); code != 0 || len(r.calls) != 2 || !isWhole(r.calls[1]) || !strings.Contains(r.out.String(), "the quick check did not look everywhere (1 error(s), first: x: input/output error): the whole tree is walked") {
		t.Errorf("a problem: exit %d, walks %+v, journal %q", code, r.calls, r.out.String())
	}

	// a device node at the top keeps its owner in the whole walk as well: no walk for it at every start
	r.answers = []ownwalk.Result{{Checked: 14, Wrong: 2, Devices: 1, HardLinks: 1, FirstWrong: "null"}}
	if code := r.run("hmm"); code != 0 || len(r.calls) != 1 || r.out.Len() != 0 {
		t.Errorf("a device node and a hard link: exit %d, walks %+v, journal %q", code, r.calls, r.out.String())
	}
}

// A whole walk with a problem keeps the marker, so the next start walks the whole tree again.
func TestAddonOwnMarkerStaysAfterAProblem(t *testing.T) {
	r := newOwnRig(t, confinedHmm)
	r.mark(t)
	r.answers = []ownwalk.Result{{Checked: 9, Wrong: 2, Fixed: 1, ErrorCount: 1, Errors: []string{"x: operation not permitted"}}}
	if code := r.run("hmm"); code != 1 || len(r.calls) != 1 || !isWhole(r.calls[0]) || !r.marked() {
		t.Fatalf("exit %d, walks %+v, marker %v: %s", code, r.calls, r.marked(), r.out.String())
	}
	r.answers = []ownwalk.Result{{Checked: 345, Fixed: 1, Wrong: 1}}
	if code := r.run("hmm"); code != 0 || len(r.calls) != 1 || !isWhole(r.calls[0]) || r.marked() {
		t.Fatalf("the next start: exit %d, walks %+v, marker %v", code, r.calls, r.marked())
	}
}

// The marker is only ever a name: a link planted in its place (by whoever could) is not followed - it
// counts as the marker, costs one whole walk, and is removed as a link; its target stays. Task 107's
// digest marker left on a box is removed at the first start.
func TestAddonOwnMarkerIsANameOnly(t *testing.T) {
	r := newOwnRig(t, confinedHmm)
	target := filepath.Join(r.root, "usr/local/shared/x")
	if err := os.Symlink(target, r.markerPath()); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(r.root, system.AddonPolicyDir, "hmm.owned")
	if err := os.WriteFile(legacy, []byte("v1 uid=30007 dirs=0 digest="+strings.Repeat("a", 64)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.answers = []ownwalk.Result{{Checked: 345}}
	if code := r.run("hmm"); code != 0 || len(r.calls) != 1 || !isWhole(r.calls[0]) {
		t.Fatalf("exit %d, walks %+v", code, r.calls)
	}
	if r.marked() {
		t.Error("the link stays")
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the link's target was removed: %v", err)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Errorf("task 107's marker stays: %v", err)
	}
}

func TestAddonOwnProblems(t *testing.T) {
	cases := []struct {
		name    string
		policy  string
		args    []string
		marked  bool
		answer  ownwalk.Result
		code    int
		journal string // a part of stdout; "" = nothing at all
		walks   int
	}{
		{name: "a root addon", policy: `{"id":"hmm","mode":"root","source":"user"}`, args: []string{"hmm"}, marked: true},
		{name: "no policy", policy: `{"id":"x"}`, args: []string{"hmm"}},
		{name: "not an id", policy: confinedHmm, args: []string{"../etc"}, code: 1, journal: `addon-own: "../etc": not an addon id`},
		{name: "a policy with another user", policy: `{"id":"hmm","mode":"confined","uid":30007,"user":"addon-other"}`, args: []string{"hmm"}, marked: true, code: 1, journal: "not the addon's"},
		{name: "a uid the passwd file gives another user", policy: `{"id":"hmm","mode":"confined","uid":30008,"user":"addon-hmm"}`, args: []string{"hmm"}, marked: true, code: 1, journal: "refused: addon-hmm has uid 30007, not 30008"},
		{name: "a data directory of another addon", policy: `{"id":"hmm","mode":"confined","uid":30007,"user":"addon-hmm","data_dirs":["/usr/local/shared","/usr/local/addons/other"]}`, args: []string{"hmm"},
			answer: ownwalk.Result{Checked: 3}, journal: "a data directory is not walked: /usr/local/shared: another addon's directory /usr/local/shared", walks: 1},
		{name: "a refused directory in the walk", policy: confinedHmm, args: []string{"hmm"},
			answer: ownwalk.Result{Refused: []string{"/usr/local/addons/hmm: /usr/local/addons can be changed by a user other than root"}}, code: 1, journal: "addon-own: hmm: refused /usr/local/addons/hmm", walks: 2},
		{name: "errors", policy: confinedHmm, args: []string{"hmm"},
			answer: ownwalk.Result{Checked: 9, Wrong: 2, Fixed: 1, ErrorCount: 1, Errors: []string{"x: operation not permitted"}}, code: 1, journal: "1 error(s), first: x: operation not permitted", walks: 2},
		{name: "a device node", policy: confinedHmm, args: []string{"hmm"}, marked: true,
			answer: ownwalk.Result{Checked: 9, Wrong: 1, Devices: 1, FirstWrong: "/usr/local/addons/hmm/var/null"}, journal: "1 entries keep another owner (1 device nodes, 0 files with more than one link), first /usr/local/addons/hmm/var/null", walks: 1},
		{name: "no id", policy: confinedHmm, code: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newOwnRig(t, tc.policy)
			if tc.marked {
				r.mark(t)
			}
			r.answers = []ownwalk.Result{tc.answer}
			code := r.run(tc.args...)
			if code != tc.code || len(r.calls) != tc.walks {
				t.Fatalf("exit %d, %d walks; want %d, %d: %s%s", code, len(r.calls), tc.code, tc.walks, r.out.String(), r.errOut.String())
			}
			if tc.journal == "" && r.out.Len() != 0 {
				t.Errorf("journal %q, want nothing", r.out.String())
			}
			if !strings.Contains(r.out.String(), tc.journal) {
				t.Errorf("journal %q lacks %q", r.out.String(), tc.journal)
			}
			if !tc.marked && r.marked() {
				t.Error("a marker was written")
			}
			for _, dirs := range r.dirs {
				for _, d := range dirs {
					if strings.Contains(d, "/usr/local/addons/other") || strings.Contains(d, "/usr/local/shared") {
						t.Errorf("walked another addon's directory %s", d)
					}
				}
			}
		})
	}
}

// openccu-lite B-252: the subcommand tells the walk to close the tree to other users and to leave the
// addon's www world-readable; it names the tightening in the journal.
func TestAddonOwnTightensModesAndKeepsWww(t *testing.T) {
	r := newOwnRig(t, confinedHmm)
	// the addon's www, a symlink into its own directory as on the image
	wwwReal := filepath.Join(r.root, "usr/local/addons/hmm/www")
	if err := os.MkdirAll(wwwReal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.root, "usr/local/etc/config/addons/www"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(wwwReal, filepath.Join(r.root, "usr/local/etc/config/addons/www/hmm")); err != nil {
		t.Fatal(err)
	}
	r.mark(t)
	r.answers = []ownwalk.Result{{Checked: 40, ModeTightened: 7, Duration: 120 * time.Millisecond}}
	if code := r.run("hmm"); code != 0 {
		t.Fatalf("exit %d: %s", code, r.out.String())
	}
	if len(r.calls) != 1 || !r.calls[0].TightenModes {
		t.Fatalf("the walk was not told to tighten modes: %+v", r.calls)
	}
	if len(r.calls[0].PublicDirs) != 1 || r.calls[0].PublicDirs[0] != wwwReal {
		t.Errorf("the www was not marked public: %v", r.calls[0].PublicDirs)
	}
	if !strings.Contains(r.out.String(), "7 entries closed to other users (directories 0751, files 0640; the www tree left)") {
		t.Errorf("journal %q", r.out.String())
	}
}

// A quick check that finds a world-readable top brings the whole walk.
func TestAddonOwnQuickCheckFindsAWorldReadableTop(t *testing.T) {
	r := newOwnRig(t, confinedHmm)
	r.answers = []ownwalk.Result{
		{Checked: 4, Wrong: 1, FirstWrong: r.root + "/usr/local/addons/hmm"},
		{Checked: 40, ModeTightened: 6, Duration: time.Second},
	}
	if code := r.run("hmm"); code != 0 || len(r.calls) != 2 || !isQuick(r.calls[0]) || !isWhole(r.calls[1]) {
		t.Fatalf("exit %d, walks %+v: %s", code, r.calls, r.out.String())
	}
	if !strings.Contains(r.out.String(), "the quick check found 1 entries to put right at the top") ||
		!strings.Contains(r.out.String(), "6 entries closed to other users") {
		t.Errorf("journal %q", r.out.String())
	}
	if r.calls[0].TightenModes != true || r.calls[1].TightenModes != true {
		t.Errorf("both walks tighten modes: %+v", r.calls)
	}
}

// openccu-lite task 406: an addon whose stored manifest declares a web ingress gets its socket
// directory before anything else, whatever the policy says; one without does not.
func TestAddonOwnIngressDir(t *testing.T) {
	root := t.TempDir()
	pol := filepath.Join(root, system.AddonPolicyDir)
	if err := os.MkdirAll(pol, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pol, "wireguard"+system.AddonManifestSuffix), []byte(`{"format": 1, "id": "wireguard", "name": "VPN", "description": {"en": "x"}, "runtime": {"daemon": true, "needs": [], "web_ingress": {"ranges": ["10.77.0.0/24"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var made []string
	var out bytes.Buffer
	env := addonOwnEnv{root: system.Root(root), policy: priv.DefaultPolicy(root, "/usr/local/etc/occulite"), stdout: &out, stderr: &out,
		ingressDir: func(id string) error { made = append(made, id); return nil }}
	if rc := addonOwn(env, []string{"wireguard"}); rc != 0 {
		t.Fatalf("exit %d: %s", rc, out.String())
	}
	if len(made) != 1 || made[0] != "wireguard" {
		t.Fatalf("ingressdir calls %v", made)
	}
	made = nil
	if rc := addonOwn(env, []string{"nut"}); rc != 0 || len(made) != 0 {
		t.Fatalf("an addon without a declaration: exit %d, calls %v", rc, made)
	}
}
