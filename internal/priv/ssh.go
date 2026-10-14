package priv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hobbyquaker/occulited/internal/sshkeys"
)

// Task 185: the Remote access page's SSH keys and sessions. root's authorized_keys is root's
// (0600), and an SSH session is root's processes - the daemon reads the file and ends a session
// through these three operations, and through nothing more general: no read of the file by path,
// no write of it, no kill of an arbitrary process.
const (
	// opAuthKeysRead answers root's authorized_keys (Policy.AuthorizedKeys); public keys only.
	opAuthKeysRead = "authkeys-read"
	// opAuthKeysWrite replaces occulited's section of it with the keys in Args, each checked as the
	// page checks a pasted key; every line outside the section stays as it is.
	opAuthKeysWrite = "authkeys-write"
	// opSSHEnd ends the SSH session whose sshd-session process has the pid in Args - that process
	// and no other kind.
	opSSHEnd = "ssh-end"
	// opSSHKeyOnly reads (no Args) or sets (Args "on" or "off") occulited's block in the sshd
	// configuration the image includes from the userfs - PasswordAuthentication and
	// KbdInteractiveAuthentication off, so root logs in with a key only (task 245) - checked with
	// sshd -t and applied with a reload of sshd.
	opSSHKeyOnly = "ssh-keyonly"
)

// SSHOps are the three operations, for the daemon's side (system.Priv implements them as Client,
// and as Local when occulited runs as root).
type SSHOps interface {
	ReadAuthorizedKeys() ([]byte, error)
	WriteAuthorizedKeys(lines []string) error
	EndSSHSession(pid int) error
	SSHKeyOnly() (bool, error)
	SetSSHKeyOnly(on bool) error
}

// SSHDConfigName is the file beside root's authorized_keys that the image's sshd_config includes
// at its end (Include /usr/local/etc/ssh/sshd_config). sshd takes a keyword's first value, and the
// image's own file sets neither PasswordAuthentication nor KbdInteractiveAuthentication, so the
// included block decides them.
const SSHDConfigName = "sshd_config"

// the block occulited keeps in that file; lines outside it are the user's and stay
const (
	keyOnlyBegin = "# BEGIN occulited: only key login (System → Remote access → SSH); edit it there"
	keyOnlyEnd   = "# END occulited"
)

var keyOnlyLines = []string{"PasswordAuthentication no", "KbdInteractiveAuthentication no"}

func (c Client) SSHKeyOnly() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.call(ctx, request{Op: opSSHKeyOnly})
	return strings.TrimSpace(string(res.Stdout)) == "on", err
}

func (c Client) SetSSHKeyOnly(on bool) error {
	arg := "off"
	if on {
		arg = "on"
	}
	return c.fileOp(request{Op: opSSHKeyOnly, Args: []string{arg}})
}

func (Local) SSHKeyOnly() (bool, error) {
	return keyOnlyIn(filepath.Join(filepath.Dir(AuthorizedKeysPath), SSHDConfigName))
}

func (l Local) SetSSHKeyOnly(on bool) error {
	return setKeyOnly(context.Background(), l, "/", filepath.Join(filepath.Dir(AuthorizedKeysPath), SSHDConfigName), on)
}

// keyOnlyIn: the file holds occulited's block.
func keyOnlyIn(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == keyOnlyBegin {
			return true, nil
		}
	}
	return false, nil
}

// withKeyOnly is content with occulited's block taken out and, when on, put first (sshd's first
// value wins, so it is ahead of anything the user wrote below).
func withKeyOnly(content string, on bool) string {
	var keep []string
	in := false
	for _, l := range strings.Split(content, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == keyOnlyBegin:
			in = true
		case in && t == keyOnlyEnd:
			in = false
		case !in:
			keep = append(keep, l)
		}
	}
	rest := strings.TrimLeft(strings.Join(keep, "\n"), "\n")
	if !on {
		return rest
	}
	block := keyOnlyBegin + "\n" + strings.Join(keyOnlyLines, "\n") + "\n" + keyOnlyEnd + "\n"
	return block + rest
}

// setKeyOnly writes the block (0600 root, atomically), checks the whole configuration with sshd -t
// - the old file comes back when it fails - and reloads a running sshd; new logins follow it, the
// sessions open now stay.
func setKeyOnly(ctx context.Context, ops Ops, root, path string, on bool) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	had := err == nil
	next := withKeyOnly(string(old), on)
	if strings.TrimSpace(next) == "" {
		// nothing of the user's and no block: no file, as the image has it
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if err := writeFileAtomic(path, []byte(next), 0o600); err != nil {
		return err
	}
	if sshd := filepath.Join(root, "usr/sbin/sshd"); fileExists(sshd) {
		r, err := ops.Run(ctx, "/usr/sbin/sshd", []string{"-t"}, nil)
		if err != nil || r.Exit != 0 {
			if had {
				_ = writeFileAtomic(path, old, 0o600)
			} else {
				_ = os.Remove(path)
			}
			msg := ""
			if err != nil {
				msg = err.Error()
			} else {
				msg = strings.TrimSpace(string(r.Stderr))
			}
			return fmt.Errorf("sshd refused the configuration, nothing changed: %s", msg)
		}
		if r, err := ops.Run(ctx, "systemctl", []string{"try-reload-or-restart", "sshd"}, nil); err != nil || r.Exit != 0 {
			return fmt.Errorf("the setting is written, but sshd was not reloaded: %v %s", err, strings.TrimSpace(string(r.Stderr)))
		}
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".occulited-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// AuthorizedKeysPath is root's authorized_keys where it lives: sshd reads .ssh/authorized_keys
// under root's home, and the image links /root/.ssh to /usr/local/etc/ssh on the userfs (OpenCCU's
// sshd start sets it up, 0700 root). The helper names the userfs path, not /root: its unit has
// ProtectHome=yes, which hides /root and the link with it.
const AuthorizedKeysPath = "/usr/local/etc/ssh/authorized_keys"

func (c Client) ReadAuthorizedKeys() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.call(ctx, request{Op: opAuthKeysRead})
	return res.Stdout, err
}

func (c Client) WriteAuthorizedKeys(lines []string) error {
	return c.fileOp(request{Op: opAuthKeysWrite, Args: lines})
}

func (c Client) EndSSHSession(pid int) error {
	return c.fileOp(request{Op: opSSHEnd, Args: []string{strconv.Itoa(pid)}})
}

func (Local) ReadAuthorizedKeys() ([]byte, error) { return readAuthorizedKeys(AuthorizedKeysPath) }

func (Local) WriteAuthorizedKeys(lines []string) error {
	keys, err := parseKeyLines(lines)
	if err != nil {
		return err
	}
	return writeAuthorizedKeys(AuthorizedKeysPath, keys)
}

func (Local) EndSSHSession(pid int) error { return endSSHSession("/proc", pid) }

func readAuthorizedKeys(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}
	return b, err
}

func parseKeyLines(lines []string) ([]sshkeys.Key, error) {
	keys := make([]sshkeys.Key, 0, len(lines))
	for _, l := range lines {
		k, err := sshkeys.Parse(l)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// writeAuthorizedKeys puts the section into the file: read, replaced, written to a new file in the
// same directory and renamed over it, 0600 - sshd refuses a file others can write, and a half-written
// file must never be the one sshd reads. The directory is made 0700 when it is missing.
func writeAuthorizedKeys(path string, keys []sshkeys.Key) error {
	cur, err := readAuthorizedKeys(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".authorized_keys.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(sshkeys.WithSection(cur, keys)); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// endSSHSession sends SIGTERM to pid when it is an sshd-session process of a connection - not the
// listener (sshd) and not anything else.
func endSSHSession(procDir string, pid int) error {
	if pid <= 1 {
		return fmt.Errorf("pid %d", pid)
	}
	base := filepath.Join(procDir, strconv.Itoa(pid))
	comm, err := os.ReadFile(filepath.Join(base, "comm"))
	if err != nil {
		return fmt.Errorf("no process %d", pid)
	}
	cmd, _ := os.ReadFile(filepath.Join(base, "cmdline"))
	if strings.TrimSpace(string(comm)) != "sshd-session" || !strings.HasPrefix(string(cmd), "sshd-session: ") {
		return fmt.Errorf("process %d is not an SSH session", pid)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// ssh is the three operations at the boundary.
func (s *Server) ssh(req request) response {
	if req.Path != "" || len(req.Data) > 0 || len(req.Stdin) > 0 || len(req.Env) > 0 || req.Name != "" || req.Dir != "" || req.Src != "" || req.Dst != "" || req.Target != "" || req.Hash != "" {
		s.log("helper: refused an SSH operation with more than its arguments")
		return refuse(req.Op + " takes its arguments and nothing else")
	}
	if s.Policy.AuthorizedKeys == "" && req.Op != opSSHEnd {
		return refuse("no authorized_keys in the policy")
	}
	path := filepath.Join(s.Policy.Root, s.Policy.AuthorizedKeys)
	switch req.Op {
	case opSSHKeyOnly:
		conf := filepath.Join(filepath.Dir(path), SSHDConfigName)
		switch {
		case len(req.Args) == 0:
			on, err := keyOnlyIn(conf)
			if err != nil {
				return response{Error: err.Error()}
			}
			if on {
				return response{OK: true, Stdout: []byte("on\n")}
			}
			return response{OK: true, Stdout: []byte("off\n")}
		case len(req.Args) == 1 && (req.Args[0] == "on" || req.Args[0] == "off"):
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			root := s.Policy.Root
			if root == "" {
				root = "/"
			}
			if err := setKeyOnly(ctx, s.ops(), root, conf, req.Args[0] == "on"); err != nil {
				return response{Error: err.Error()}
			}
			s.info("helper: ssh key-only login %s", req.Args[0])
			return response{OK: true}
		}
		return refuse("ssh-keyonly takes nothing, on or off")
	case opAuthKeysRead:
		if len(req.Args) > 0 {
			return refuse("authkeys-read takes nothing")
		}
		b, err := readAuthorizedKeys(path)
		if err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true, Stdout: b}
	case opAuthKeysWrite:
		// checked here as well as on the page: this is the boundary, and what arrives is lines the
		// unprivileged side assembled
		keys, err := parseKeyLines(req.Args)
		if err != nil {
			s.log("helper: refused an authorized_keys section: %v", err)
			return refuse("key: " + err.Error())
		}
		if err := writeAuthorizedKeys(path, keys); err != nil {
			return response{Error: err.Error()}
		}
		return response{OK: true}
	case opSSHEnd:
		if len(req.Args) != 1 {
			return refuse("ssh-end takes one pid")
		}
		pid, err := strconv.Atoi(req.Args[0])
		if err != nil {
			return refuse("ssh-end: pid " + req.Args[0])
		}
		procDir := s.Policy.ProcDir
		if procDir == "" {
			procDir = filepath.Join(s.Policy.Root, "proc")
		}
		if err := endSSHSession(procDir, pid); err != nil {
			s.log("helper: refused to end %d: %v", pid, err)
			return refuse(err.Error())
		}
		return response{OK: true}
	}
	return refuse("op " + req.Op)
}
