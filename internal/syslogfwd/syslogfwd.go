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
//
// A reboot (openccu-lite B-342): the lines logged just before it are the ones a remote log is
// for, and a stop used to end with the follower - a line logged 32 ms before `systemctl reboot`
// never left (systemd's SIGTERM reaches the follower's journalctl as well). So a stop drains: one
// more journalctl, without -f, after the last entry sent, whose entries are sent within Drain
// before the forwarder ends. And the cursor of the last entry sent is written at the stop to
// BootCursorFile as well, on the userfs, where the next boot's start finds it: when it is from the
// previous boot, the journal still has that boot (a persistent journal) and it lies at most
// BootTail before the boot's last entry, the start sends that boot's entries after it in one pass
// - what was logged after the stop, the rest of the shutdown - and then this boot from its start.
// Anything else (a journal in RAM, an older boot, a cursor further back: the forwarder was
// stopped long before the reboot) is a start without a cursor. The file is removed with the first
// entry a start sends, so a cursor in it never lies behind what was sent - a boot that ends
// without a stop (a power loss) leaves none, and the next one starts without a cursor rather than
// sending anything twice. It is written once per stop, never while entries flow: the userfs may
// be an SD card.
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

// DefaultDrain is how long a stop sends the entries after the last one sent before it ends; the
// unit's TimeoutStopSec leaves room for it.
const DefaultDrain = 3 * time.Second

// DefaultBootTail is how far before the previous boot's last entry the cursor of a stop may lie for
// the next boot to continue after it.
const DefaultBootTail = 10 * time.Minute

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
	// BootCursorFile keeps the cursor of a stop across the reboot (the userfs); "" = none.
	BootCursorFile string
	// BootTail bounds how far back into the previous boot a start continues; 0 = DefaultBootTail.
	BootTail time.Duration
	// Drain is how long a stop sends the journal's last entries; 0 = DefaultDrain.
	Drain    time.Duration
	MaxSize  int // 0 = DefaultMaxSize
	Backlog  time.Duration
	Hostname string // for an entry without _HOSTNAME; "" = os.Hostname

	// Stream runs journalctl; nil = the real one.
	Stream func(ctx context.Context, name string, args ...string) (io.ReadCloser, error)
	// Query runs journalctl once and returns its output (the previous boot's last entry at a
	// start); nil = the real one.
	Query func(ctx context.Context, args ...string) ([]byte, error)
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
	prevBoot := ""
	if st.cursor == "" {
		if c, boot := f.previousBootCursor(ctx); c != "" {
			st.cursor, prevBoot = c, boot
		}
	}
	st.saved, st.savedAt = st.cursor, f.now()
	defer f.writeBootCursor(st)
	defer f.save(st)

	if prevBoot != "" {
		// B-342: the previous boot's rest - what was logged after its stop, the shutdown - in one
		// pass first: journalctl -f does not leave the current boot, even after a cursor of an
		// earlier one (systemd 258); the follower then reads this boot from its start
		f.log().Info("syslog-forward: continuing after the last entry sent before the reboot", "to", hostport, "addr", f.addr.String())
		n, err := f.catchUp(ctx, hostport, st, "--after-cursor="+st.cursor, "-b", prevBoot)
		if err != nil && ctx.Err() == nil {
			f.log().Warn("syslog-forward: the previous boot's last entries could not be read", "err", err)
		}
		f.log().Info("syslog-forward: the previous boot's last entries are sent", "entries", n)
		st.otherBoot = true
		if ctx.Err() != nil {
			return nil
		}
	}

	restarted := false
	for {
		w := journalwatch.Watch{Every: f.MoveEvery, Where: f.Where}
		moved, err := w.Run(ctx, func(rctx context.Context) error {
			return f.follow(ctx, rctx, hostport, st, restarted)
		})
		if ctx.Err() != nil {
			f.drain(ctx, hostport, st)
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
// handled and when it was last written to CursorFile; whether that cursor is still the previous
// boot's (the follower then reads this boot from its start), and whether the start has removed
// BootCursorFile.
type readState struct {
	cursor, saved string
	savedAt       time.Time
	otherBoot     bool
	bootCleared   bool
}

// drain is a stop's last pass (B-342): the follower is gone - the stop ended it, and systemd's
// SIGTERM may have reached its journalctl first - so the entries after the last one sent that are
// in the journal now are read in one pass and sent, within Drain. A line logged just before
// `systemctl reboot` leaves this way.
func (f *Forwarder) drain(ctx context.Context, hostport string, st *readState) {
	if st.cursor == "" || st.otherBoot {
		return
	}
	d := orDefault(f.Drain, DefaultDrain)
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
	defer cancel()
	n, err := f.catchUp(dctx, hostport, st, "--after-cursor="+st.cursor)
	switch {
	case dctx.Err() != nil:
		f.log().Warn("syslog-forward: stopped before the journal's last entry was sent", "entries", n, "within", d.String())
	case err != nil:
		f.log().Warn("syslog-forward: the journal's last entries could not be read at the stop", "err", err)
	case n > 0:
		f.log().Info("syslog-forward: stopping, the journal's last entries are sent", "entries", n)
	}
}

// catchUp runs journalctl once - without -f, with args - and sends what it prints, as follow does;
// it returns how many entries it sent. A send that fails is retried until ctx ends.
func (f *Forwarder) catchUp(ctx context.Context, hostport string, st *readState, args ...string) (int, error) {
	stream := f.Stream
	if stream == nil {
		stream = execStream
	}
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	out, err := stream(cmdCtx, "journalctl", append([]string{"-o", "json", "--no-pager", "-q"}, args...)...)
	if err != nil {
		return 0, fmt.Errorf("journalctl: %w", err)
	}
	n := 0
	after := st.cursor
	rd := bufio.NewReaderSize(out, 64<<10)
	for {
		line, rerr := readLine(rd, maxLine)
		if e, ok := ParseEntry(line); len(line) > 0 && ok {
			c := e["__CURSOR"]
			if after != "" && journalwatch.SameEntry(after, c) {
				st.cursor = firstOf(c, st.cursor)
				continue
			}
			after = ""
			if !f.send(ctx, hostport, Format(e, f.Hostname, f.MaxSize)) {
				break
			}
			st.cursor = firstOf(c, st.cursor)
			f.clearBootCursor(st)
			n++
		}
		if rerr != nil {
			break
		}
	}
	cancel()
	closeErr := out.Close()
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	if closeErr != nil {
		return n, fmt.Errorf("journalctl: %w", closeErr)
	}
	return n, nil
}

// lastEntry is the last line of journalctl -o json output that parses.
func lastEntry(out []byte) (Entry, bool) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if e, ok := ParseEntry([]byte(lines[i])); ok {
			return e, true
		}
	}
	return nil, false
}

// previousBootCursor is BootCursorFile's cursor and its boot when the next start may continue after
// it: it is from the previous boot, which the journal still has, and lies at most BootTail before
// that boot's last entry. "" otherwise - the start is one without a cursor.
func (f *Forwarder) previousBootCursor(ctx context.Context) (string, string) {
	if f.BootCursorFile == "" {
		return "", ""
	}
	c := readCursorFile(f.BootCursorFile)
	if c == "" {
		return "", ""
	}
	fields := cursorFields(c)
	t, err := strconv.ParseInt(fields["t"], 16, 64)
	if fields["b"] == "" || err != nil {
		f.log().Info("syslog-forward: the cursor kept across the reboot is not one, starting without it", "file", f.BootCursorFile)
		return "", ""
	}
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := f.query(qctx, "-b", "-1", "-n", "1", "-o", "json", "-q", "--no-pager")
	e, ok := lastEntry(out)
	if err != nil || !ok {
		f.log().Info("syslog-forward: the previous boot is not in the journal (kept in RAM), starting with this boot")
		return "", ""
	}
	if e["_BOOT_ID"] != fields["b"] {
		f.log().Info("syslog-forward: the cursor kept across the reboot is from an older boot, starting with this boot")
		return "", ""
	}
	last, err := strconv.ParseInt(e["__REALTIME_TIMESTAMP"], 10, 64)
	tail := orDefault(f.BootTail, DefaultBootTail)
	if err != nil || time.Duration(last-t)*time.Microsecond > tail {
		f.log().Info("syslog-forward: the cursor kept across the reboot lies too far before the previous boot's end, starting with this boot", "bound", tail.String())
		return "", ""
	}
	return c, fields["b"]
}

func cursorFields(c string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(c, ";") {
		if k, v, ok := strings.Cut(part, "="); ok {
			m[k] = v
		}
	}
	return m
}

// clearBootCursor removes BootCursorFile once a start has sent its first entry: from then on the
// cursor in it lies behind what was sent.
func (f *Forwarder) clearBootCursor(st *readState) {
	if st.bootCleared || f.BootCursorFile == "" {
		return
	}
	st.bootCleared = true
	if err := os.Remove(f.BootCursorFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		f.log().Warn("syslog-forward: the cursor kept across the reboot could not be removed", "file", f.BootCursorFile, "err", err)
	}
}

// writeBootCursor keeps the cursor of the last entry handled for the next boot (at the stop).
func (f *Forwarder) writeBootCursor(st *readState) {
	if f.BootCursorFile == "" || st.cursor == "" {
		return
	}
	if err := writeFileAtomic(f.BootCursorFile, st.cursor, 0o640); err != nil {
		f.log().Warn("syslog-forward: the cursor for the next boot could not be written", "file", f.BootCursorFile, "err", err)
	}
}

func (f *Forwarder) query(ctx context.Context, args ...string) ([]byte, error) {
	if f.Query != nil {
		return f.Query(ctx, args...)
	}
	return exec.CommandContext(ctx, "journalctl", args...).Output()
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
	after := st.cursor // the entry sent last, which a flushed journal hands out once more
	switch {
	case st.otherBoot:
		// the previous boot's rest is sent (Run): this boot from its start, whole
		args = append(args, "-b", "-n", "all")
		after = ""
	case st.cursor != "":
		args = append(args, "--after-cursor="+st.cursor)
		if !restarted {
			f.log().Info("syslog-forward: continuing after the last entry sent", "to", hostport, "addr", f.addr.String())
		}
	default:
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
				st.otherBoot = false
				f.clearBootCursor(st)
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
	return readCursorFile(f.CursorFile)
}

func readCursorFile(file string) string {
	b, err := os.ReadFile(file)
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
	return writeFileAtomic(f.CursorFile, c, 0o600)
}

// writeFileAtomic writes c and a newline to file through a rename. BootCursorFile is 0640: its
// directory is the systemd-journal group's (setgid), and the next boot's dynamic user may have
// another uid.
func writeFileAtomic(file, c string, mode os.FileMode) error {
	tmp := file + ".new"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, []byte(c+"\n"), mode); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Clean(file))
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
