package system

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/ownwalk"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// owners is the fake file ownership of the B-92 tests: a path not in the map is the addon's
type owners struct {
	mu sync.Mutex
	m  map[string]int
	// walks are the helper's dry runs asked for, "<uid> <dirs>", task 110's quick one "quick <uid> <dirs>";
	// answer, when set, is their answer
	walks  []string
	answer func() (ownwalk.Result, error)
	// asked are the options of every walk, the ones that change entries included
	asked []priv.OwnTreeOptions
}

func (o *owners) walkCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.walks)
}

// takeWalks answers the dry runs asked for since the last call.
func (o *owners) takeWalks() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	w := o.walks
	o.walks = nil
	return w
}

// markWhole leaves the marker "full walk needed" as an install does (task 110).
func markWhole(t *testing.T, r Root, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := os.WriteFile(r.FullWalkMarker(id), []byte("install\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// dryRun is the helper's walk as root sees the fake ownership (B-123): a directory the tester cannot
// open is opened for the moment of its listing, as root can; a link is neither followed nor counted;
// a top directory that is missing or a link is skipped; an entry the fake gives to root is root-owned.
// quick looks at a top directory and its direct entries only (task 110).
func (o *owners) dryRun(dirs []string, uid int, quick bool) (ownwalk.Result, error) {
	o.mu.Lock()
	prefix := ""
	if quick {
		prefix = "quick "
	}
	o.walks = append(o.walks, fmt.Sprintf("%s%d %s", prefix, uid, strings.Join(dirs, " ")))
	answer := o.answer
	o.mu.Unlock()
	if answer != nil {
		return answer()
	}
	var res ownwalk.Result
	look := func(path string) {
		res.Checked++
		if u, _, ok := ownerOf(path); ok && u == 0 {
			res.Wrong++
			res.RootOwned++
			if res.FirstRootOwned == "" {
				res.FirstRootOwned = path
			}
		}
	}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrPermission) {
			if st, serr := os.Lstat(dir); serr == nil && os.Chmod(dir, 0o700) == nil {
				defer os.Chmod(dir, st.Mode().Perm())
				entries, err = os.ReadDir(dir)
			}
		}
		if err != nil {
			res.ErrorCount++
			res.Errors = append(res.Errors, dir+": "+err.Error())
			return
		}
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			switch {
			case e.Type()&fs.ModeSymlink != 0:
				res.Symlinks++
			case e.IsDir():
				look(full)
				if quick {
					res.TooDeep++
				} else {
					walk(full)
				}
			default:
				look(full)
			}
		}
	}
	for _, d := range dirs {
		if st, err := os.Lstat(d); err != nil || !st.IsDir() {
			res.Skipped = append(res.Skipped, d)
			continue
		}
		look(d)
		walk(d)
	}
	return res, nil
}

func (o *owners) set(path string, uid int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[path] = uid
}

// chown gives a tree to uid, as chown -R does
func (o *owners) chown(path string, uid int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for p := range o.m {
		if p == path || strings.HasPrefix(p, path+"/") {
			o.m[p] = uid
		}
	}
}

type ownersPriv struct {
	priv.Local
	o    *owners
	note func(string)
}

func (p ownersPriv) Chown(path string, uid, _ int, _ bool) error {
	p.o.chown(path, uid)
	return nil
}

// OwnTree gives each existing directory's tree to uid, as the helper's walk does (task 107), and
// notes it as "chown -R".
func (p ownersPriv) OwnTree(_ string, dirs []string, uid int, opt priv.OwnTreeOptions) (ownwalk.Result, error) {
	p.o.mu.Lock()
	p.o.asked = append(p.o.asked, opt)
	p.o.mu.Unlock()
	if opt.DryRun {
		return p.o.dryRun(dirs, uid, opt.Quick)
	}
	if opt.Quick {
		return ownwalk.Result{}, errors.New("a quick walk that changes entries is not asked for")
	}
	for _, d := range dirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		p.o.chown(d, uid)
		if p.note != nil {
			p.note(fmt.Sprintf("chown -R %d:%d %s", uid, uid, d))
		}
	}
	return ownwalk.Result{}, nil
}

func ownershipRig(t *testing.T) (Root, *AddonOwnership, *owners, *[]string) {
	t.Helper()
	r := rootWith(t, map[string]string{
		"etc/passwd":                            "root:x:0:0::/:/bin/sh\naddon-hmm:x:30001:30001::/usr/local/addons/hmm:/bin/false\naddon-redmatic:x:30002:30002::/usr/local/addons/redmatic:/bin/false\n",
		"etc/group":                             "root:x:0:\n",
		"usr/local/etc/config/rc.d/hmm":         "#!/bin/sh\n",
		"usr/local/addons/hmm/var/hmm.pid":      "1\n",
		"usr/local/addons/hmm/www/index.html":   "",
		"usr/local/hmm/token":                   "secret\n",
		"usr/local/etc/config/rc.d/redmatic":    "#!/bin/sh\n",
		"usr/local/addons/redmatic/lib/x.js":    "",
		"usr/local/etc/config/rc.d/cuxd":        "#!/bin/sh\n",
		"usr/local/addons/cuxd/cuxd":            "",
		"usr/local/etc/config/addons/www/.keep": "",
	})
	if err := os.Symlink(r.join("/usr/local/addons/hmm/www"), r.join("/usr/local/etc/config/addons/www/hmm")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", r.join("/usr/local/addons/redmatic/lib/passwd")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*AddonPolicy{
		{ID: "hmm", Mode: "confined", UID: 30001, User: "addon-hmm", DataDirs: []string{"/usr/local/hmm"}},
		{ID: "redmatic", Mode: "confined", UID: 30002, User: "addon-redmatic"},
		{ID: "cuxd", Mode: "root"},
	} {
		if err := r.writeAddonPolicy(p); err != nil {
			t.Fatal(err)
		}
	}
	own := &owners{m: map[string]int{}}
	oldOwner, oldPriv := ownerOf, Priv
	ownerOf = func(path string) (int, int, bool) {
		if _, err := os.Lstat(path); err != nil {
			return 0, 0, false
		}
		own.mu.Lock()
		defer own.mu.Unlock()
		uid, ok := own.m[path]
		if !ok {
			uid = 424242
		}
		return uid, uid, true
	}
	var calls []string
	var callsMu sync.Mutex
	note := func(line string) {
		callsMu.Lock()
		calls = append(calls, line)
		callsMu.Unlock()
	}
	Priv = ownersPriv{o: own, note: note}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		note(name + " " + strings.Join(args, " "))
		return nil, nil
	}
	o := &AddonOwnership{Addons: NewSystemdAddons(r, SystemdServices{Root: r, Run: run})}
	t.Cleanup(func() {
		waitOwnership(t, o)
		ownerOf, Priv = oldOwner, oldPriv
	})
	// everything root's by default in the tests' tree would be the tester's; name the root ones
	own.set(r.join("/usr/local/addons/redmatic/lib/passwd"), 0) // a symlink: never counted
	own.set(r.join("/usr/local/addons/cuxd/cuxd"), 0)           // a root addon: never counted
	return r, o, own, &calls
}

func waitOwnership(t *testing.T, o *AddonOwnership) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		o.mu.Lock()
		running := o.running
		o.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background check did not end")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAddonOwnershipFindsRootOwnedFiles(t *testing.T) {
	r, o, own, _ := ownershipRig(t)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return now }
	// task 110: a file below var/ is seen by the whole walk, which an install's marker asks for
	markWhole(t, r, "hmm")
	own.set(r.join("/usr/local/addons/hmm/var/hmm.pid"), 0)

	// the first look has no results yet: it says so and checks in the background
	if list, complete := o.Findings(); complete || len(list) != 0 {
		t.Fatalf("before any check: %v %v", list, complete)
	}
	waitOwnership(t, o)
	list, complete := o.Findings()
	if !complete || len(list) != 1 || list[0] != (RootOwned{ID: "hmm", Path: "/usr/local/addons/hmm/var/hmm.pid", Count: 1}) {
		t.Fatalf("after the check: %v %v", list, complete)
	}

	// a chown by hand clears it at the next look, without waiting for the hour
	own.set(r.join("/usr/local/addons/hmm/var/hmm.pid"), 30001)
	if list, _ := o.Findings(); len(list) != 0 {
		t.Errorf("after the chown by hand: %v", list)
	}
	waitOwnership(t, o)

	// a root-owned file in a data directory, found once the hour has passed
	own.set(r.join("/usr/local/hmm/token"), 0)
	if list, _ := o.Findings(); len(list) != 0 {
		t.Errorf("a result stands for an hour: %v", list)
	}
	waitOwnership(t, o)
	now = now.Add(61 * time.Minute)
	o.Findings()
	waitOwnership(t, o)
	if list, _ := o.Findings(); len(list) != 1 || list[0].Path != "/usr/local/hmm/token" {
		t.Errorf("after the hour: %v", list)
	}
	waitOwnership(t, o)
}

// an install touches the rc.d script: the result is stale at once
func TestAddonOwnershipFollowsAnInstall(t *testing.T) {
	r, o, own, _ := ownershipRig(t)
	o.Check()
	if list, complete := o.Findings(); !complete || len(list) != 0 {
		t.Fatalf("clean: %v %v", list, complete)
	}
	// task 110: an install over SSH leaves no marker; what it made root's at the top the quick check sees
	own.set(r.join("/usr/local/addons/redmatic/lib"), 0)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(r.join("/usr/local/etc/config/rc.d/redmatic"), later, later); err != nil {
		t.Fatal(err)
	}
	o.Findings()
	waitOwnership(t, o)
	if list, _ := o.Findings(); len(list) != 1 || list[0].ID != "redmatic" {
		t.Errorf("after the install: %v", list)
	}
}

func TestFixOwnership(t *testing.T) {
	r, o, own, calls := ownershipRig(t)
	own.set(r.join("/usr/local/addons/hmm/var/hmm.pid"), 0)
	own.set(r.join("/usr/local/hmm/token"), 0)
	o.Check()
	if list, _ := o.Findings(); len(list) != 1 {
		t.Fatalf("before: %v", list)
	}
	fix, err := o.FixOwnership(context.Background(), "hmm")
	if err != nil || fix != (OwnershipFix{}) {
		t.Fatalf("fix: %+v %v", fix, err)
	}
	if got := strings.Join(*calls, "\n"); !strings.Contains(got, "chown -R 30001:30001 "+r.join("/usr/local/addons/hmm")) {
		t.Errorf("the standard directories are chowned:\n%s", got)
	}
	if got := strings.Join(*calls, "\n"); strings.Contains(got, "systemctl start") {
		t.Errorf("an addon whose unit did not fail is not started:\n%s", got)
	}
	if list, complete := o.Findings(); !complete || len(list) != 0 {
		t.Errorf("after the fix: %v %v", list, complete)
	}
	if _, err := o.FixOwnership(context.Background(), "cuxd"); !errors.Is(err, ErrNotConfined) {
		t.Errorf("a root addon: %v", err)
	}
	if _, err := o.FixOwnership(context.Background(), "nope"); !errors.Is(err, ErrNotConfined) {
		t.Errorf("no policy: %v", err)
	}
}

// task 81, D-67: after the fix an addon whose unit had failed is started once; a running addon, one
// stopped on purpose and one whose daemon still runs outside its failed unit are left alone.
func TestFixOwnershipStartsAFailedAddon(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     string
		stray     bool // a process of the addon runs outside its unit
		failStart bool
		want      OwnershipFix
	}{
		{name: "failed", state: "failed", want: OwnershipFix{Started: true}},
		{name: "running", state: "active"},
		{name: "starting", state: "activating"},
		{name: "stopped on purpose", state: "inactive"},
		{name: "no answer from systemctl", state: ""},
		{name: "failed, but its daemon runs elsewhere", state: "failed", stray: true},
		{name: "failed, and the start fails", state: "failed", failStart: true,
			want: OwnershipFix{StartError: "systemctl start addon-hmm.service: exit status 1: Job for addon-hmm.service failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, o, own, _ := ownershipRig(t)
			own.set(r.join("/usr/local/addons/hmm/var/hmm.pid"), 0)
			if tc.stray {
				fakeProcess(t, r, 4711, "/usr/local/addons/hmm/bin/hmm", "/system.slice/sshd.service", 1, 100)
			}
			var calls []string
			var order []string
			Priv = ownersPriv{o: own, note: func(string) { order = append(order, "chown") }}
			o.Addons.Systemd.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				line := name + " " + strings.Join(args, " ")
				calls = append(calls, line)
				switch {
				case name == "systemctl" && args[0] == "show":
					if tc.state == "" {
						return nil, errors.New("exit status 1")
					}
					return []byte("Id=addon-hmm.service\nActiveState=" + tc.state + "\n"), nil
				case name == "systemctl" && args[0] == "start":
					order = append(order, "start")
					if tc.failStart {
						return []byte("Job for addon-hmm.service failed"), errors.New("exit status 1")
					}
				}
				return nil, nil
			}
			fix, err := o.FixOwnership(context.Background(), "hmm")
			if err != nil || fix != tc.want {
				t.Fatalf("fix: %+v %v, want %+v", fix, err, tc.want)
			}
			starts := 0
			for _, c := range calls {
				if strings.HasPrefix(c, "systemctl start") {
					starts++
					if c != "systemctl start --no-pager -- addon-hmm.service" {
						t.Errorf("the normal unit start: %q", c)
					}
				}
			}
			wantStarts := 0
			if tc.want.Started || tc.want.StartError != "" {
				wantStarts = 1
			}
			if starts != wantStarts {
				t.Errorf("%d start(s), want %d:\n%s", starts, wantStarts, strings.Join(calls, "\n"))
			}
			if wantStarts == 1 && (len(order) == 0 || order[0] != "chown" || order[len(order)-1] != "start") {
				t.Errorf("the files first, then the start: %v", order)
			}
		})
	}
}

// Task 110: B-92's look is the quick check - the top directories and their direct entries - unless an
// install, an update or a switch to the addon's own user left the marker. A root-owned file deep in
// the tree is not seen without it (the maintainer's trade: root writes there in an install), it is
// seen with it, and a root-owned entry at the top brings the whole dry run for the exact count.
func TestAddonOwnershipQuickCheck(t *testing.T) {
	r, o, own, _ := ownershipRig(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return now }
	hmm := "30001 " + r.join("/usr/local/addons/hmm") + " " + r.join("/usr/local/etc/config/addons/hmm") + " " + r.join("/usr/local/etc/config/addons/www/hmm") + " " + r.join("/usr/local/hmm")
	own.set(r.join("/usr/local/addons/hmm/var/hmm.pid"), 0)

	o.Check()
	if list, complete := o.Findings(); !complete || len(list) != 0 {
		t.Fatalf("a file below var/ without the marker: %v %v", list, complete)
	}
	waitOwnership(t, o)
	if w := own.takeWalks(); len(w) != 2 || !strings.HasPrefix(w[0], "quick ") || !strings.HasPrefix(w[1], "quick ") {
		t.Errorf("the hourly look, one quick check per confined addon: %q", w)
	}

	// an install's marker: the whole dry run, and the daemon leaves the marker to the unit's start
	markWhole(t, r, "hmm")
	o.Check("hmm")
	if list, complete := o.Findings(); !complete || len(list) != 1 || list[0] != (RootOwned{ID: "hmm", Path: "/usr/local/addons/hmm/var/hmm.pid", Count: 1}) {
		t.Fatalf("with the marker: %v %v", list, complete)
	}
	waitOwnership(t, o)
	if w := own.takeWalks(); len(w) != 1 || w[0] != hmm {
		t.Errorf("with the marker, the whole dry run only: %q", w)
	}
	if !r.FullWalkNeeded("hmm") {
		t.Error("the look removed the marker")
	}

	// no marker, var/ itself root's: the quick check finds it, the whole dry run counts both
	if err := os.Remove(r.FullWalkMarker("hmm")); err != nil {
		t.Fatal(err)
	}
	own.set(r.join("/usr/local/addons/hmm/var"), 0)
	o.Check("hmm")
	if list, _ := o.Findings(); len(list) != 1 || list[0] != (RootOwned{ID: "hmm", Path: "/usr/local/addons/hmm/var", Count: 2}) {
		t.Fatalf("root's at the top: %v", list)
	}
	waitOwnership(t, o)
	if w := own.takeWalks(); len(w) != 2 || w[0] != "quick "+hmm || w[1] != hmm {
		t.Errorf("the quick check, then the whole dry run: %q", w)
	}

	// a whole dry run that fails keeps what the quick check found
	own.set(r.join("/usr/local/addons/hmm/var/hmm.pid"), 30001)
	calls := 0
	own.answer = func() (ownwalk.Result, error) {
		calls++
		if calls == 1 {
			return ownwalk.Result{Checked: 3, Wrong: 1, RootOwned: 1, FirstRootOwned: r.join("/usr/local/addons/hmm/var")}, nil
		}
		return ownwalk.Result{}, priv.ErrRefused
	}
	o.Check("hmm")
	if list, complete := o.Findings(); !complete || len(list) != 1 || list[0] != (RootOwned{ID: "hmm", Path: "/usr/local/addons/hmm/var", Count: 1}) {
		t.Errorf("the whole dry run refused: %v %v", list, complete)
	}
	waitOwnership(t, o)
}

// Task 110: Fix ownership walks the whole tree, with or without the marker, and leaves the marker to
// the unit's next start.
func TestFixOwnershipWalksTheWholeTree(t *testing.T) {
	r, o, own, _ := ownershipRig(t)
	pid := r.join("/usr/local/addons/hmm/var/hmm.pid")
	for _, marked := range []bool{false, true} {
		own.set(pid, 0)
		if marked {
			markWhole(t, r, "hmm")
		}
		own.mu.Lock()
		own.asked = nil
		own.mu.Unlock()
		if _, err := o.FixOwnership(context.Background(), "hmm"); err != nil {
			t.Fatal(err)
		}
		own.mu.Lock()
		asked := append([]priv.OwnTreeOptions{}, own.asked...)
		uid := own.m[pid]
		own.mu.Unlock()
		if len(asked) == 0 || asked[0].DryRun || asked[0].Quick || asked[0].Exclude != nil {
			t.Errorf("marked %v: the fix is not the whole walk: %+v", marked, asked)
		}
		if uid != 30001 {
			t.Errorf("marked %v: the file below var/ is uid %d after the fix", marked, uid)
		}
		if r.FullWalkNeeded("hmm") != marked {
			t.Errorf("marked %v: the fix changed the marker", marked)
		}
		waitOwnership(t, o)
	}
}

// B-123: on the Pi 4 a root-owned file in Mosquitto's var/ (drwx------ addon-mosquitto) was never
// reported, because the daemon walked as its own user and skipped what it could not open. The check
// is the helper's walk now: a root-owned file in a directory closed to the daemon is found, stays
// found at the next look although the daemon cannot lstat it, and Fix ownership clears it.
func TestAddonOwnershipSeesIntoAPrivateDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens every directory")
	}
	r, o, own, _ := ownershipRig(t)
	// task 110: a file this deep is seen by the whole walk, which an install's marker asks for
	markWhole(t, r, "hmm")
	private := r.join("/usr/local/addons/hmm/var/private")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(private, "mosquitto.db")
	if err := os.WriteFile(db, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	own.set(db, 0)
	if err := os.Chmod(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(private, 0o755) })
	if _, err := os.ReadDir(private); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("the test's daemon can open the private directory: %v", err)
	}

	o.Check()
	want := RootOwned{ID: "hmm", Path: "/usr/local/addons/hmm/var/private/mosquitto.db", Count: 1}
	list, complete := o.Findings()
	if !complete || len(list) != 1 || list[0] != want {
		t.Fatalf("the private directory: %v %v", list, complete)
	}
	own.mu.Lock()
	walks := strings.Join(own.walks, "\n")
	own.mu.Unlock()
	if !strings.Contains(walks, "30001 "+r.join("/usr/local/addons/hmm")+" ") || !strings.Contains(walks, r.join("/usr/local/hmm")) {
		t.Errorf("the helper's walk, with the addon's uid over its directories and data directories:\n%s", walks)
	}
	// the next look: the daemon cannot lstat the file, the finding stands, and no walk is started
	n := own.walkCount()
	if list, _ := o.Findings(); len(list) != 1 || list[0] != want {
		t.Errorf("the next look: %v", list)
	}
	waitOwnership(t, o)
	if own.walkCount() != n {
		t.Errorf("a finding the daemon cannot look at started a walk at the next look")
	}
	// Fix ownership gives the files back, and the check through the helper sees it
	if _, err := o.FixOwnership(context.Background(), "hmm"); err != nil {
		t.Fatal(err)
	}
	if list, complete := o.Findings(); !complete || len(list) != 0 {
		t.Errorf("after the fix: %v %v", list, complete)
	}
}

// B-123: a walk that did not look everywhere - the helper refused it, a directory was refused or
// unreadable, the walk stopped - is never a clean result, and it is not repeated at every look
// either: it is tried again after ownershipRetry.
func TestAddonOwnershipIncompleteWalk(t *testing.T) {
	for name, answer := range map[string]func() (ownwalk.Result, error){
		"refused by the helper": func() (ownwalk.Result, error) { return ownwalk.Result{}, priv.ErrRefused },
		"a directory refused": func() (ownwalk.Result, error) {
			return ownwalk.Result{Checked: 3, Refused: []string{"/usr/local/hmm: a link of user 30001"}}, nil
		},
		"an unreadable directory": func() (ownwalk.Result, error) {
			return ownwalk.Result{Checked: 3, ErrorCount: 1, Errors: []string{"/usr/local/addons/hmm/var: permission denied"}}, nil
		},
		"stopped": func() (ownwalk.Result, error) { return ownwalk.Result{Checked: 2_000_000, Incomplete: true}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			_, o, own, _ := ownershipRig(t)
			now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
			o.Now = func() time.Time { return now }
			own.answer = answer
			o.Check()
			if list, complete := o.Findings(); complete || len(list) != 0 {
				t.Fatalf("a walk that did not look everywhere: %v %v", list, complete)
			}
			waitOwnership(t, o)
			n := own.walkCount()
			for i := 0; i < 5; i++ {
				if _, complete := o.Findings(); complete {
					t.Fatal("complete at a later look")
				}
				waitOwnership(t, o)
			}
			if own.walkCount() != n {
				t.Errorf("walked again at every look: %d walks after %d", own.walkCount(), n)
			}
			now = now.Add(ownershipRetry + time.Second)
			o.Findings()
			waitOwnership(t, o)
			if own.walkCount() != n+2 {
				t.Errorf("not tried again after %s: %d walks, want %d", ownershipRetry, own.walkCount(), n+2)
			}
		})
	}
}

// a walk that hits its limit leaves no result: an incomplete walk proves nothing
func TestAddonOwnershipLimit(t *testing.T) {
	_, o, _, _ := ownershipRig(t)
	o.Limit = 1
	o.Check()
	if _, complete := o.Findings(); complete {
		t.Error("an incomplete walk is not a result")
	}
	waitOwnership(t, o)
}

// B-131: at boot the first look comes before occu-addons writes the addons' users into passwd. It
// asks the helper for no walk (which it would refuse with a warning), keeps no result, and walks at
// the next look once the users are there - not only after ownershipRetry.
func TestAddonOwnershipWaitsForTheAddonUsers(t *testing.T) {
	r, o, own, _ := ownershipRig(t)
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return now }
	passwd := r.join("/etc/passwd")
	full, err := os.ReadFile(passwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwd, []byte("root:x:0:0::/:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, complete := o.Findings(); complete {
			t.Fatal("complete without the addons' users")
		}
		waitOwnership(t, o)
	}
	if n := own.walkCount(); n != 0 {
		t.Fatalf("walked %d times before the users existed: %v", n, own.takeWalks())
	}
	// a user with another uid is not the addon's
	if err := os.WriteFile(passwd, []byte("root:x:0:0::/:/bin/sh\naddon-hmm:x:31111:31111::/:/bin/false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.Findings()
	waitOwnership(t, o)
	if n := own.walkCount(); n != 0 {
		t.Fatalf("walked for a user of another uid: %v", own.takeWalks())
	}
	// the users are back (a moment later, no retry interval passed): the next look walks, and completes
	if err := os.WriteFile(passwd, full, 0o644); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	o.Findings()
	waitOwnership(t, o)
	if n := own.walkCount(); n != 2 {
		t.Fatalf("after the users came: %d walks, want one per confined addon", n)
	}
	if _, complete := o.Findings(); !complete {
		t.Error("not complete once the users exist")
	}
	waitOwnership(t, o)
}

func TestAddonOwnershipWithoutAddons(t *testing.T) {
	var o *AddonOwnership
	if list, complete := o.Findings(); !complete || list != nil {
		t.Errorf("nil: %v %v", list, complete)
	}
}
