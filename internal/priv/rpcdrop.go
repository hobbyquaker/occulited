package priv

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpcstall"
)

// openccu-lite B-201: an interface process held by a callback listener that takes its call and
// never answers is released by nothing but the end of that one TCP connection - not by a
// deregistration (init(url, "") answers, and the thread stays in its read), and it never times out.
// The kernels openccu-lite runs have no SOCK_DESTROY (CONFIG_INET_DIAG_DESTROY), so the helper
// takes a duplicate of the daemon's own socket with pidfd_getfd and shuts it down: the daemon's
// read ends, its call fails the way a closed listener's does, and it goes on with the others.
//
// The operation is as narrow as the ssh-end one: the port must be one an interface process of
// InterfacesList.xml listens on at the loopback, the daemon is whoever owns that listening socket,
// and only its established outgoing connections to the one remote address are touched.

// opRPCDrop ends a daemon's connections to one callback listener: Args are the daemon's port and
// the listener's host:port.
const opRPCDrop = "rpc-drop"

// RPCDropCommand is the subcommand the helper runs itself as for the syscalls: pidfd_getfd is in
// systemd's @debug set, which the helper's unit filters, and a filtered call kills the process -
// better a child than the helper. The unit of an image that allows it lets the child through.
const RPCDropCommand = "rpc-drop"

// ErrDropNotAllowed is the child killed by the system call filter: the image's helper unit does
// not let pidfd_getfd through.
var ErrDropNotAllowed = errors.New("this system does not allow the helper to end another process's connection (its unit filters pidfd_getfd)")

// RPCDropOps is the operation for the daemon's side.
type RPCDropOps interface {
	DropRPCConnections(port int, remote string) (int, error)
}

func (c Client) DropRPCConnections(port int, remote string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.call(ctx, request{Op: opRPCDrop, Args: []string{strconv.Itoa(port), remote}})
	if err != nil {
		return 0, err
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	return n, nil
}

func (Local) DropRPCConnections(port int, remote string) (int, error) {
	return dropRPC(context.Background(), rpcDropEnv{root: "/", proc: "/proc"}, port, remote)
}

// rpcDropEnv is where dropRPC looks, and how it shuts a socket: the tests fake the second.
type rpcDropEnv struct {
	root, proc string
	shut       func(ctx context.Context, pid, fd int) error
}

var interfaceURL = regexp.MustCompile(`<url>\s*([^<\s]+)\s*</url>`)

// interfacePorts are the ports of InterfacesList.xml's entries on the loopback.
func interfacePorts(root string) map[int]bool {
	b, err := os.ReadFile(filepath.Join(root, "etc/config/InterfacesList.xml"))
	if err != nil {
		return nil
	}
	out := map[int]bool{}
	for _, m := range interfaceURL.FindAllStringSubmatch(string(b), -1) {
		_, rest, ok := strings.Cut(m[1], "://")
		if !ok {
			continue
		}
		hostport, _, _ := strings.Cut(rest, "/")
		ap, err := netip.ParseAddrPort(hostport)
		if err != nil || !ap.Addr().IsLoopback() {
			continue
		}
		out[int(ap.Port())] = true
	}
	return out
}

// dropRPC ends the connections and answers how many it ended.
func dropRPC(ctx context.Context, env rpcDropEnv, port int, remote string) (int, error) {
	if !interfacePorts(env.root)[port] {
		return 0, fmt.Errorf("port %d is not an interface process of InterfacesList.xml", port)
	}
	want, err := netip.ParseAddrPort(remote)
	if err != nil {
		return 0, fmt.Errorf("the listener %q is not host:port", remote)
	}
	want = netip.AddrPortFrom(want.Addr().Unmap(), want.Port())
	conns := rpcstall.ReadAll(func(p string) ([]byte, error) {
		return os.ReadFile(filepath.Join(env.proc, strings.TrimPrefix(p, "/proc/")))
	})
	d, ok := rpcstall.DaemonOf(conns, uint16(port))
	if !ok {
		return 0, fmt.Errorf("nothing listens on port %d", port)
	}
	// the daemon's established connections to that listener that it made itself (its local port
	// is none it listens on) - named explicitly, so DaemonOf's filter of connections to the
	// daemon's own ports does not apply
	own := map[uint16]bool{}
	for _, p := range d.Listen {
		own[p] = true
	}
	inodes := map[string]bool{}
	for _, c := range conns {
		if c.State == 0x01 && c.UID == d.UID && c.Remote == want && !own[c.Local.Port()] && c.Inode != 0 {
			inodes["socket:["+strconv.FormatUint(c.Inode, 10)+"]"] = true
		}
	}
	if len(inodes) == 0 {
		return 0, nil
	}
	shut := env.shut
	if shut == nil {
		shut = shutInChild
	}
	n := 0
	var errs []error
	for _, pid := range pidsOf(env.proc, d.UID) {
		fdDir := filepath.Join(env.proc, strconv.Itoa(pid), "fd")
		ents, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			l, err := os.Readlink(filepath.Join(fdDir, e.Name()))
			if err != nil || !inodes[l] {
				continue
			}
			fd, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			if err := shut(ctx, pid, fd); err != nil {
				errs = append(errs, err)
				continue
			}
			n++
		}
	}
	if n == 0 && len(errs) > 0 {
		for _, e := range errs {
			if !errors.Is(e, ErrDropNotAllowed) {
				return 0, errors.Join(errs...)
			}
		}
		return 0, ErrDropNotAllowed // said once, however many sockets it was
	}
	return n, nil
}

// pidsOf are the processes whose /proc directory the user owns - the daemon and nothing of
// anybody else's (the interface daemons run under users of their own).
func pidsOf(proc string, uid int) []int {
	ents, err := os.ReadDir(proc)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		st, err := os.Stat(filepath.Join(proc, e.Name()))
		if err != nil {
			continue
		}
		if s, ok := st.Sys().(*syscall.Stat_t); ok && int(s.Uid) == uid {
			out = append(out, pid)
		}
	}
	return out
}

// shutInChild runs `occulited rpc-drop <pid> <fd>` - this binary, as a child, so that a system
// call filter that kills it does not kill the helper.
func shutInChild(ctx context.Context, pid, fd int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, self, RPCDropCommand, strconv.Itoa(pid), strconv.Itoa(fd)).CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGSYS {
			return ErrDropNotAllowed
		}
	}
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// The system call numbers are the same on every architecture since the unified table (424 on).
const (
	sysPidfdOpen  = 434
	sysPidfdGetfd = 438
)

// ShutdownForeignSocket is the child's work: a duplicate of descriptor fd of process pid, and
// shutdown(SHUT_RDWR) on it, which ends the connection for the process's descriptor as well.
// Only sockets: shutdown refuses anything else with ENOTSOCK.
func ShutdownForeignSocket(pid, fd int) error {
	pfd, _, e := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	if e != 0 {
		return fmt.Errorf("pidfd_open %d: %w", pid, e)
	}
	defer syscall.Close(int(pfd))
	dup, _, e := syscall.Syscall(sysPidfdGetfd, pfd, uintptr(fd), 0)
	if e != 0 {
		return fmt.Errorf("pidfd_getfd %d/%d: %w", pid, fd, e)
	}
	defer syscall.Close(int(dup))
	if err := syscall.Shutdown(int(dup), syscall.SHUT_RDWR); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// rpcDrop is the operation at the boundary.
func (s *Server) rpcDrop(ctx context.Context, req request) response {
	if req.Path != "" || len(req.Data) > 0 || len(req.Stdin) > 0 || len(req.Env) > 0 || req.Name != "" || req.Dir != "" || req.Src != "" || req.Dst != "" || req.Target != "" || req.Hash != "" {
		return refuse(opRPCDrop + " takes its arguments and nothing else")
	}
	if len(req.Args) != 2 {
		return refuse(opRPCDrop + " takes a port and a listener")
	}
	port, err := strconv.Atoi(req.Args[0])
	if err != nil || port < 1 || port > 65535 {
		return refuse(opRPCDrop + ": port " + req.Args[0])
	}
	procDir := s.Policy.ProcDir
	if procDir == "" {
		procDir = filepath.Join(s.Policy.Root, "proc")
	}
	root := s.Policy.Root
	if root == "" {
		root = "/"
	}
	n, err := dropRPC(ctx, rpcDropEnv{root: root, proc: procDir, shut: s.rpcShut}, port, req.Args[1])
	if err != nil {
		s.log("helper: rpc-drop %d %s: %v", port, req.Args[1], err)
		if errors.Is(err, ErrDropNotAllowed) {
			return response{Error: err.Error()}
		}
		return refuse(err.Error())
	}
	s.info("helper: rpc-drop %d %s: %d connection(s) ended", port, req.Args[1], n)
	return response{OK: true, Stdout: []byte(strconv.Itoa(n))}
}
