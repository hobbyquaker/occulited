package priv

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// occulited B-59: which addon a process belongs to is read off /proc/<pid>/exe among other
// things, and the kernel answers that link only to a process allowed to ptrace the owner - for an
// unprivileged occulited, its own processes. A daemon an addon's installer started as root and
// that names itself by a title alone (RedMatic's node-red) was therefore nobody's: neither argv nor
// uid nor the unreadable exe pointed at the addon, and it stayed in the install scope, as root,
// outside its unit. This operation answers the link's target and nothing else: one /proc/<pid>/exe
// by its exact shape, no file content, no other link of /proc.

// opProcExe reads one process's executable link.
const opProcExe = "procexe"

var procExeRe = regexp.MustCompile(`^/proc/[1-9][0-9]{0,9}/exe$`)

// ProcExePath is the helper's operand for pid.
func ProcExePath(pid int) string { return "/proc/" + strconv.Itoa(pid) + "/exe" }

// ProcExe asks the helper where pid's executable is.
func (c Client) ProcExe(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := c.call(ctx, request{Op: opProcExe, Path: ProcExePath(pid)})
	if err != nil {
		return "", err
	}
	if len(res.Names) != 1 {
		return "", os.ErrNotExist
	}
	return res.Names[0], nil
}

// ProcExe reads pid's executable link.
func (Local) ProcExe(pid int) (string, error) {
	return os.Readlink(ProcExePath(pid))
}

// procExe is the operation at the boundary: exactly /proc/<pid>/exe, nothing else in the request.
func (s *Server) procExe(req request) response {
	if req.Name != "" || len(req.Args) > 0 || len(req.Data) > 0 || len(req.Stdin) > 0 || len(req.Env) > 0 || req.Dir != "" || req.Src != "" || req.Dst != "" || req.Target != "" || req.Hash != "" || req.AllFiles {
		return refuse("procexe takes a /proc/<pid>/exe and nothing else")
	}
	if filepath.Clean(req.Path) != req.Path || !procExeRe.MatchString(req.Path) {
		s.log("helper: refused the executable link %s", req.Path)
		return refuse("procexe " + req.Path)
	}
	pid, _ := strconv.Atoi(req.Path[len("/proc/") : len(req.Path)-len("/exe")])
	exe, err := s.ops().ProcExe(pid)
	if err != nil {
		return response{Error: err.Error()}
	}
	return response{OK: true, Names: []string{exe}}
}
