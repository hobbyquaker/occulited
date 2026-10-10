package system

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// B-59: RedMatic's update script started Node-RED as root in the install scope; its title is the
// bare "node-red", its uid 0, and an unprivileged occulited may not read a root process's exe
// link. The settle step's deep check asks the helper for that link and finds the addon's node;
// the listing (no helper) does not, and a process of another user is never asked about.
func TestLeftoversDeepFindTheRootDaemonByItsExe(t *testing.T) {
	root := Root(t.TempDir())
	scope := "/system.slice/" + testScope
	status := func(pid, uid int) {
		_ = os.WriteFile(root.join(fmt.Sprintf("/proc/%d/status", pid)), []byte(fmt.Sprintf("Name:\tx\nUid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid)), 0o644)
	}
	fakeProcess(t, root, 3221, "node-red", scope, 1, 100) // the daemon, root
	status(3221, 0)
	fakeProcess(t, root, 3222, "/usr/sbin/lighttpd -f /etc/lighttpd/lighttpd.conf", scope, 1, 90) // B-3: lighttpd in the scope
	status(3222, 0)
	fakeProcess(t, root, 3223, "logger -t redmatic", scope, 1, 110) // another user's
	status(3223, 1000)
	fakeProcess(t, root, 3224, "node-red", "/system.slice/sshd.service", 1, 120) // root, outside the scope
	status(3224, 0)

	exes := map[string]string{"3221": "/usr/local/addons/redmatic/bin/node", "3222": "/usr/sbin/lighttpd", "3223": "/usr/bin/logger", "3224": "/usr/local/addons/redmatic/bin/node"}
	oldRead, oldHelper := readExeLink, procExeViaHelper
	t.Cleanup(func() { readExeLink, procExeViaHelper = oldRead, oldHelper })
	readExeLink = func(string) (string, error) { return "", os.ErrPermission }
	var asked []int
	procExeViaHelper = func(_ Root, pid int) (string, error) {
		asked = append(asked, pid)
		return exes[fmt.Sprint(pid)], nil
	}
	s := SystemdServices{Root: root}
	pids := func(ps []proc) string {
		var out []string
		for _, p := range ps {
			out = append(out, fmt.Sprint(p.PID))
		}
		return strings.Join(out, " ")
	}
	if got := pids(s.addonLeftovers("redmatic", testScope)); got != "" || len(asked) != 0 {
		t.Fatalf("the listing found %q, asked %v", got, asked)
	}
	if got := pids(s.addonLeftoversDeep("redmatic", testScope)); got != "3221" {
		t.Fatalf("deep in the scope: %q", got)
	}
	if fmt.Sprint(asked) != "[3221 3222]" {
		t.Fatalf("asked the helper about %v, want root's processes in the scope only", asked)
	}
	asked = nil
	if got := pids(s.addonLeftoversDeep("redmatic", "")); got != "3221 3224" {
		t.Fatalf("deep anywhere: %q", got)
	}
	// the helper is only for the real root
	procExeViaHelper = oldHelper
	if _, err := procExeViaHelper(root, 3221); !os.IsPermission(err) {
		t.Fatalf("a development root asked the helper: %v", err)
	}
}

func TestStrayWarning(t *testing.T) {
	if strayWarning(nil, "redmatic") != "" {
		t.Fatal("a warning without a process")
	}
	w := strayWarning([]proc{{PID: 3221}, {PID: 3230}}, "redmatic")
	if !strings.Contains(w, "redmatic still runs outside its unit (pid 3221, 3230)") {
		t.Fatalf("%q", w)
	}
}
