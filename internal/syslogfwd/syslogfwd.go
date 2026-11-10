// Package syslogfwd sends the journal to a remote syslog host: every entry, as RFC 5424 over UDP
// (RFC 5426), read from `journalctl -f -o json` (B-96).
//
// Its predecessor was busybox syslogd with -R, which bound /dev/log. That took journald's own
// syslog socket away - every syslog() message of rfd, lighttpd and sshd was missing from the
// journal while it ran, and stayed missing after it stopped - and it only ever saw what programs
// sent through syslog(), so the units' stdout (hmipserver, the addons, occulited) reached no remote
// host at all. Reading the journal covers every transport - syslog, stdout, native, the kernel -
// and takes nothing away from journald.
//
// Where it is: a journal cursor kept in a file (the unit's RuntimeDirectory, so it lasts until the
// reboot) lets a restart continue after the last entry sent. Without a cursor it starts with this
// boot's entries of the last Backlog - a box that just booted sends its boot, one where LOGHOST was
// set after days does not send those days. The boot's own clock decides that (the monotonic
// timestamp), not the wall clock, which jumps when chrony sets it.
//
// When the journal moves under the running journalctl - the machine ID changes, the switch from
// RAM to persistent mounts the userfs over /var/log/journal - journalctl stays alive and prints
// nothing more (B-140, openccu-lite B-340). The forwarder watches the journal's place
// (journalwatch) and starts journalctl again after the last entry sent; every start logs the first
// entry it sends, so a forwarder that has gone quiet shows in the journal by the line it lacks.
//
// A send that fails (no route yet, the network restarting) is retried with the same entry until
// it works: journalctl blocks meanwhile and the journal keeps what is not sent. UDP has no
// acknowledgement, so "sent" is as far as it goes.
package syslogfwd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hobbyquaker/occulited/internal/journalwatch"
)

// DefaultPort is syslog's UDP port.
const DefaultPort = "514"

// DefaultMaxSize is the largest message sent: RFC 5426 asks a receiver to take 2048 octets, and a
// datagram of that size is not fragmented on a LAN. A longer message is cut at a character boundary.
const DefaultMaxSize = 2048

// DefaultBacklog is how far back a start without a cursor reaches into this boot.
const DefaultBacklog = 10 * time.Minute

// maxLine is the longest journalctl line read; a longer entry is skipped rather than ending the
// read (journald's own limit for a stdout line is 48 KiB, a native entry can be larger).
const maxLine = 4 << 20

// Entry is one journal entry: its fields as journalctl -o json prints them, a binary value
// (an array of bytes) as its text, a field that occurs more than once as its first value.
type Entry map[string]string

// ParseEntry reads one line of journalctl -o json.
func ParseEntry(b []byte) (Entry, bool) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	e := Entry{}
	for k, v := range m {
		if s, ok := fieldString(v); ok {
			e[k] = s
		}
	}
	return e, true
}

func fieldString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []any:
		if len(x) == 0 {
			return "", true
		}
		// a field that occurs more than once: its values; the first one is the entry's
		if _, isNum := x[0].(float64); !isNum {
			return fieldString(x[0])
		}
		// a binary value: its bytes
		b := make([]byte, 0, len(x))
		for _, n := range x {
			f, ok := n.(float64)
			if !ok || f < 0 || f > 255 {
				return "", false
			}
			b = append(b, byte(f))
		}
		return strings.ToValidUTF8(string(b), "�"), true
	}
	return "", false
}

// ParseLogHost turns LOGHOST - host, host:port, [v6]:port or a bare IPv6 address, as busybox
// syslogd's -R took it - into a host:port to send to.
func ParseLogHost(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("LOGHOST is empty")
	}
	if strings.Count(s, ":") > 1 && !strings.HasPrefix(s, "[") {
		// a bare IPv6 address has no port
		if net.ParseIP(s) == nil {
			return "", fmt.Errorf("LOGHOST %q is not a host, host:port or an address", s)
		}
		return net.JoinHostPort(s, DefaultPort), nil
	}
	host, port := s, DefaultPort
	if strings.HasPrefix(s, "[") || strings.Contains(s, ":") {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
				return "", fmt.Errorf("LOGHOST %q: %v", s, err)
			}
			h, p = strings.Trim(s, "[]"), DefaultPort // [v6] without a port
		}
		host, port = h, p
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("LOGHOST %q: the port is 1 to 65535", s)
	}
	if host == "" || strings.ContainsAny(host, " \t/") {
		return "", fmt.Errorf("LOGHOST %q is not a host", s)
	}
	return net.JoinHostPort(host, port), nil
}

// Format is an entry as one RFC 5424 message:
//
//	<PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID STRUCTURED-DATA MSG
//
// PRI is the entry's SYSLOG_FACILITY (the kernel's 0 for the kernel transport, user otherwise) times
// 8 plus its PRIORITY (info without one). The timestamp is the entry's realtime in the box's zone
// with microseconds; the host its _HOSTNAME, or host; the app name its SYSLOG_IDENTIFIER, or _COMM;
// the process id SYSLOG_PID, or _PID. No MSGID and no structured data. The message is MESSAGE
// without its trailing newlines, without a BOM, and the whole is at most max bytes.
func Format(e Entry, host string, max int) []byte {
	sev := 6
	if n, err := strconv.Atoi(e["PRIORITY"]); err == nil && n >= 0 && n <= 7 {
		sev = n
	}
	fac := 1
	if e["_TRANSPORT"] == "kernel" {
		fac = 0
	}
	if n, err := strconv.Atoi(e["SYSLOG_FACILITY"]); err == nil && n >= 0 && n <= 23 {
		fac = n
	}
	ts := "-"
	if us, err := strconv.ParseInt(e["__REALTIME_TIMESTAMP"], 10, 64); err == nil && us > 0 {
		ts = time.UnixMicro(us).Format("2006-01-02T15:04:05.000000Z07:00")
	}
	hostname := header(e["_HOSTNAME"], 255)
	if hostname == "-" {
		hostname = header(host, 255)
	}
	app := header(firstOf(e["SYSLOG_IDENTIFIER"], e["_COMM"]), 48)
	pid := header(firstOf(e["SYSLOG_PID"], e["_PID"]), 128)
	var b strings.Builder
	fmt.Fprintf(&b, "<%d>1 %s %s %s %s - -", fac*8+sev, ts, hostname, app, pid)
	if msg := strings.TrimRight(e["MESSAGE"], "\r\n"); msg != "" {
		b.WriteByte(' ')
		b.WriteString(msg)
	}
	return []byte(cut(b.String(), max))
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// header is a header field of RFC 5424: printable US-ASCII without spaces, at most n characters,
// "-" (the NILVALUE) when there is nothing. Anything else becomes an underscore.
func header(s string, n int) string {
	if s == "" {
		return "-"
	}
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if len(b) == n {
			break
		}
		if r >= 33 && r <= 126 {
			b = append(b, byte(r))
		} else {
			b = append(b, '_')
		}
	}
	return string(b)
}

// cut shortens s to at most n bytes without splitting a UTF-8 sequence.
func cut(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Forwarder follows the journal and sends it to LogHost.
type Forwarder struct {
	LogHost    string // LOGHOST as the file has it
	CursorFile string // "" = no cursor kept: every start is a start without one
	MaxSize    int    // 0 = DefaultMaxSize
	Backlog    time.Duration
	Hostname   string // for an entry without _HOSTNAME; "" = os.Hostname

	// Stream runs journalctl; nil = the real one.
	Stream func(ctx context.Context, name string, args ...string) (io.ReadCloser, error)
	// Resolve turns host:port into an address; nil = net.ResolveUDPAddr.
	Resolve func(ctx context.Context, hostport string) (*net.UDPAddr, error)
	// Send writes one datagram; nil = a UDP socket.
	Send func(b []byte, to *net.UDPAddr) error
	// Uptime is the time since the boot; nil = /proc/uptime.
	Uptime func() (time.Duration, bool)
	// RetryMin and RetryMax bound the wait between two tries of a failing send or resolution;
	// 0 = 1 s and 30 s.
	RetryMin, RetryMax time.Duration
	// ResolveEvery is how often a host name is looked up again; 0 = 10 minutes.
	ResolveEvery time.Duration
	// CursorEvery is how often the cursor is written while entries flow; 0 = 2 s. It is always
	// written when the forwarder stops.
	CursorEvery time.Duration
	// MoveEvery and Where are how the forwarder notices that the journal moved under its
	// journalctl (journalwatch); 0 and nil = every 5 s, the system's place.
	MoveEvery time.Duration
	Where     func() string
	Now       func() time.Time
	Log       *slog.Logger

	addr       *net.UDPAddr
	resolvedAt time.Time
	conns      map[string]net.PacketConn
	failures   int
	failLogged time.Time
}

func (f *Forwarder) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Forwarder) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Run forwards until ctx ends (nil) or journalctl stops on its own (an error: the unit restarts
// the forwarder, which continues at its cursor). When the journal moves under journalctl - the
// machine ID changes, a directory is mounted over a journal root - it would stay alive and silent
// (B-140); Run notices (journalwatch) and starts it again after the last entry sent.
func (f *Forwarder) Run(ctx context.Context) error {
	hostport, err := ParseLogHost(f.LogHost)
	if err != nil {
		return err
	}
	if f.MaxSize <= 0 {
		f.MaxSize = DefaultMaxSize
	}
	if f.Hostname == "" {
		f.Hostname, _ = os.Hostname()
	}
	defer f.closeConns()
	if err := f.resolve(ctx, hostport, true); err != nil {
		return nil // ctx ended while waiting for the name
	}

	st := &readState{cursor: f.readCursor()}
	st.saved, st.savedAt = st.cursor, f.now()
	defer f.save(st)
	restarted := false
	for {
		w := journalwatch.Watch{Every: f.MoveEvery, Where: f.Where}
		moved, err := w.Run(ctx, func(rctx context.Context) error {
			return f.follow(ctx, rctx, hostport, st, restarted)
		})
		if ctx.Err() != nil {
			return nil
		}
		if !moved {
			return err
		}
		// B-140: journald writes where this journalctl does not look; a new one, from the cursor
		f.save(st)
		f.log().Info("syslog-forward: the journal moved (machine ID or mount), reading it again after the last entry sent", "to", hostport)
		restarted = true
	}
}

// readState is what one Run keeps across its journalctl runs: the cursor of the last entry
// handled and when it was last written to CursorFile.
type readState struct {
	cursor, saved string
	savedAt       time.Time
}

func (f *Forwarder) save(st *readState) {
	if st.cursor != st.saved {
		if err := f.writeCursor(st.cursor); err != nil {
			f.log().Warn("syslog-forward: the cursor could not be written", "file", f.CursorFile, "err", err)
		}
		st.saved, st.savedAt = st.cursor, f.now()
	}
}

// follow runs one journalctl -f under rctx (which ends it when the journal moves) and sends what
// it prints; sends wait on ctx alone, so an entry read before the end is still sent. A run with a
// cursor continues after it - after a move too, so nothing is sent twice and nothing is left
// out; one without starts with this boot's last Backlog. The cursor's own entry, when it comes
// again from the file the flush copied it into, is not sent again (journalwatch.SameEntry).
func (f *Forwarder) follow(ctx, rctx context.Context, hostport string, st *readState, restarted bool) error {
	args := []string{"-f", "-o", "json", "--no-pager", "-q"}
	skipBefore := int64(-1)
	backlog := orDefault(f.Backlog, DefaultBacklog)
	if st.cursor != "" {
		args = append(args, "--after-cursor="+st.cursor)
		if !restarted {
			f.log().Info("syslog-forward: continuing after the last entry sent", "to", hostport, "addr", f.addr.String())
		}
	} else {
		args = append(args, "-b", "-n", "all")
		if up, ok := f.uptime(); ok && up > backlog {
			skipBefore = (up - backlog).Microseconds()
		}
		if !restarted {
			f.log().Info("syslog-forward: sending the journal as RFC 5424 over UDP, starting with this boot's last minutes", "to", hostport, "addr", f.addr.String(), "backlog", backlog.String())
		}
	}

	cmdCtx, cancel := context.WithCancel(rctx)
	defer cancel()
	stream := f.Stream
	if stream == nil {
		stream = execStream
	}
	out, err := stream(cmdCtx, "journalctl", args...)
	if err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}

	first := true
	after := st.cursor // the entry sent last, which a flushed journal hands out once more
	rd := bufio.NewReaderSize(out, 64<<10)
	for {
		line, err := readLine(rd, maxLine)
		if len(line) > 0 {
			if e, ok := ParseEntry(line); ok {
				if after != "" {
					c := e["__CURSOR"]
					if journalwatch.SameEntry(after, c) {
						st.cursor = firstOf(c, st.cursor)
						continue
					}
					after = ""
				}
				if skipBefore >= 0 {
					if mono, err := strconv.ParseInt(e["__MONOTONIC_TIMESTAMP"], 10, 64); err == nil && mono < skipBefore {
						st.cursor = firstOf(e["__CURSOR"], st.cursor)
						continue
					}
					skipBefore = -1 // the entries are in order: from here on every one is sent
				}
				if !f.send(ctx, hostport, Format(e, f.Hostname, f.MaxSize)) {
					break // ctx ended while the send was failing
				}
				st.cursor = firstOf(e["__CURSOR"], st.cursor)
				if first {
					// B-140: a forwarder that sends nothing looks like one with nothing to send;
					// this line in the journal says from when on it did
					first = false
					f.log().Info("syslog-forward: the first entry since the start is sent", "to", hostport, "entry_time", entryTime(e))
				}
				if f.now().Sub(st.savedAt) >= orDefault(f.CursorEvery, 2*time.Second) {
					f.save(st)
				}
			}
		}
		if err != nil {
			break
		}
	}
	cancel()
	closeErr := out.Close()
	if ctx.Err() != nil {
		return nil
	}
	if closeErr != nil {
		return fmt.Errorf("journalctl ended: %w", closeErr)
	}
	return errors.New("journalctl ended")
}

// entryTime is an entry's realtime as RFC 3339, "" without one.
func entryTime(e Entry) string {
	us, err := strconv.ParseInt(e["__REALTIME_TIMESTAMP"], 10, 64)
	if err != nil || us <= 0 {
		return ""
	}
	return time.UnixMicro(us).Format(time.RFC3339)
}

// readLine reads one line; a line longer than max is read to its end and dropped (nil).
func readLine(rd *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	over := false
	for {
		chunk, err := rd.ReadSlice('\n')
		if !over {
			if len(buf)+len(chunk) > max {
				over, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if over {
			return nil, err
		}
		return buf, err
	}
}

// send writes one message, retrying with a growing wait until it works or ctx ends (false).
func (f *Forwarder) send(ctx context.Context, hostport string, msg []byte) bool {
	wait := orDefault(f.RetryMin, time.Second)
	for {
		if f.now().Sub(f.resolvedAt) >= orDefault(f.ResolveEvery, 10*time.Minute) {
			_ = f.resolve(ctx, hostport, false) // a failure keeps the address it had
		}
		err := f.sendTo(msg, f.addr)
		if err == nil {
			if f.failures > 0 {
				f.log().Info("syslog-forward: sending works again", "to", hostport, "failed_tries", f.failures)
				f.failures = 0
			}
			return true
		}
		f.failures++
		if f.failures == 1 || f.now().Sub(f.failLogged) >= time.Minute {
			f.log().Warn("syslog-forward: sending failed, retrying", "to", hostport, "addr", f.addr.String(), "err", err, "failed_tries", f.failures)
			f.failLogged = f.now()
		}
		if !sleep(ctx, wait) {
			return false
		}
		wait = min(wait*2, orDefault(f.RetryMax, 30*time.Second))
		// the address may be what changed
		_ = f.resolve(ctx, hostport, false)
	}
}

// resolve looks the host up. With wait it retries until it works or ctx ends (an error then);
// without, one try whose failure keeps the previous address (and is an error).
func (f *Forwarder) resolve(ctx context.Context, hostport string, wait bool) error {
	res := f.Resolve
	if res == nil {
		res = func(ctx context.Context, hp string) (*net.UDPAddr, error) {
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			host, port, err := net.SplitHostPort(hp)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%s has no address", host)
			}
			p, _ := strconv.Atoi(port)
			// an IPv4 address first where there is one: the box's IPv6 is often link-local only
			pick := ips[0]
			for _, ip := range ips {
				if ip.Unmap().Is4() {
					pick = ip.Unmap()
					break
				}
			}
			return &net.UDPAddr{IP: pick.AsSlice(), Port: p, Zone: pick.Zone()}, nil
		}
	}
	delay := orDefault(f.RetryMin, time.Second)
	logged := false
	for {
		addr, err := res(ctx, hostport)
		if err == nil {
			if f.addr != nil && addr.String() != f.addr.String() {
				f.log().Info("syslog-forward: the syslog host has a new address", "to", hostport, "addr", addr.String(), "was", f.addr.String())
			}
			f.addr, f.resolvedAt = addr, f.now()
			return nil
		}
		if !wait {
			f.resolvedAt = f.now() // the next try after ResolveEvery, not at the next entry
			return err
		}
		if !logged {
			f.log().Warn("syslog-forward: the syslog host cannot be resolved yet, retrying", "to", hostport, "err", err)
			logged = true
		}
		if !sleep(ctx, delay) {
			return ctx.Err()
		}
		delay = min(delay*2, orDefault(f.RetryMax, 30*time.Second))
	}
}

func (f *Forwarder) sendTo(b []byte, to *net.UDPAddr) error {
	if f.Send != nil {
		return f.Send(b, to)
	}
	network := "udp6"
	if to.IP.To4() != nil {
		network = "udp4"
	}
	if f.conns == nil {
		f.conns = map[string]net.PacketConn{}
	}
	c := f.conns[network]
	if c == nil {
		var err error
		if c, err = net.ListenPacket(network, ""); err != nil {
			return err
		}
		f.conns[network] = c
	}
	_ = c.SetWriteDeadline(f.now().Add(5 * time.Second))
	_, err := c.WriteTo(b, to)
	return err
}

func (f *Forwarder) closeConns() {
	for _, c := range f.conns {
		_ = c.Close()
	}
	f.conns = nil
}

func (f *Forwarder) uptime() (time.Duration, bool) {
	if f.Uptime != nil {
		return f.Uptime()
	}
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	s, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(s * float64(time.Second)), true
}

func (f *Forwarder) readCursor() string {
	if f.CursorFile == "" {
		return ""
	}
	b, err := os.ReadFile(f.CursorFile)
	if err != nil {
		return ""
	}
	c := strings.TrimSpace(string(b))
	// a cursor is s=…;i=…;b=…: one line, nothing that journalctl would read as an option
	if strings.ContainsAny(c, " \n") || !strings.HasPrefix(c, "s=") {
		return ""
	}
	return c
}

func (f *Forwarder) writeCursor(c string) error {
	if f.CursorFile == "" || c == "" {
		return nil
	}
	tmp := f.CursorFile + ".new"
	if err := os.WriteFile(tmp, []byte(c+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Clean(f.CursorFile))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// execStream starts the command and hands over its standard output; Close waits for it.
func execStream(ctx context.Context, name string, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = os.Stderr // journalctl's complaints go where the forwarder's own log goes
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &cmdStream{ReadCloser: out, cmd: cmd}, nil
}

type cmdStream struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (c *cmdStream) Close() error {
	_ = c.ReadCloser.Close()
	return c.cmd.Wait()
}
