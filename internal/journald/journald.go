// Package journald writes entries into the systemd journal over journald's native protocol: one
// unix datagram per entry to /run/systemd/journal/socket, each field a KEY=value line, and a
// value that holds a newline in the length-prefixed binary form. Pure Go, no cgo, no library.
//
// It is what sd_journal_sendv does, minus one thing: an entry larger than a datagram is passed
// there as a sealed memfd over SCM_RIGHTS. Here the entry is shortened instead (Fit) - the
// callers write log lines, and a line of 64 KiB is already one nobody reads to its end.
//
// The protocol: https://systemd.io/JOURNAL_NATIVE_PROTOCOL/
package journald

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// DefaultSocket is where journald takes native entries.
const DefaultSocket = "/run/systemd/journal/socket"

// Syslog priorities, the values of the PRIORITY field.
const (
	PriorityErr     = 3
	PriorityWarning = 4
	PriorityInfo    = 6
)

// DefaultMaxDatagram is the largest entry Send writes unless told otherwise: well below the
// kernel's limit for one unix datagram, which is the sending socket's buffer (about 208 KiB by
// default). A socket with a smaller buffer answers EMSGSIZE, and Send halves the size then.
const DefaultMaxDatagram = 64 << 10

// minDatagram is where the halving stops: every socket takes an entry of that size.
const minDatagram = 2 << 10

// truncated marks a value Fit had to cut.
const truncated = " [truncated]"

// Field is one field of an entry. A key is upper-case letters, digits and underscores, at most
// 64 of them, and starts with neither a digit nor an underscore (those are journald's own).
// Encode leaves out a field whose key is not like that, as journald would drop it anyway.
type Field struct {
	Key, Value string
}

// Writer sends entries to journald. The zero value writes to DefaultSocket.
type Writer struct {
	Socket      string        // "" = DefaultSocket
	MaxDatagram int           // 0 = DefaultMaxDatagram
	Timeout     time.Duration // the most one Send waits for the socket; 0 = 5 s
}

// Send writes the entries in their order over one connection and stops at the first error. A
// full journald queue blocks a datagram socket; the Timeout bounds that, so a caller is never
// held up longer than it.
func (w *Writer) Send(entries ...[]Field) error {
	if len(entries) == 0 {
		return nil
	}
	sock := w.Socket
	if sock == "" {
		sock = DefaultSocket
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	limit := w.MaxDatagram
	if limit <= 0 {
		limit = DefaultMaxDatagram
	}
	for _, e := range entries {
		for {
			_, err := conn.Write(Encode(Fit(e, limit)))
			if err == nil {
				break
			}
			if errors.Is(err, syscall.EMSGSIZE) && limit > minDatagram {
				limit /= 2 // the socket's own limit is below ours; the rest of the batch keeps the smaller one
				continue
			}
			return err
		}
	}
	return nil
}

// Encode is one entry as a datagram of the native protocol.
func Encode(fields []Field) []byte {
	var b bytes.Buffer
	for _, f := range fields {
		if !validKey(f.Key) {
			continue
		}
		b.WriteString(f.Key)
		if !strings.Contains(f.Value, "\n") {
			b.WriteByte('=')
			b.WriteString(f.Value)
			b.WriteByte('\n')
			continue
		}
		// a newline in the value: KEY, newline, the length as 64-bit little endian, the value,
		// newline
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(f.Value)))
		b.WriteByte('\n')
		b.Write(n[:])
		b.WriteString(f.Value)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// Decode reads a datagram of the native protocol back into its fields: the inverse of Encode.
func Decode(b []byte) ([]Field, error) {
	var out []Field
	for len(b) > 0 {
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 {
			return out, errors.New("journald: a field without its closing newline")
		}
		line := b[:nl]
		b = b[nl+1:]
		if eq := bytes.IndexByte(line, '='); eq >= 0 {
			out = append(out, Field{Key: string(line[:eq]), Value: string(line[eq+1:])})
			continue
		}
		if len(b) < 8 {
			return out, fmt.Errorf("journald: field %s has no length", line)
		}
		n := binary.LittleEndian.Uint64(b[:8])
		b = b[8:]
		if n >= uint64(len(b)) || b[n] != '\n' {
			return out, fmt.Errorf("journald: field %s is shorter than its length", line)
		}
		out = append(out, Field{Key: string(line), Value: string(b[:n])})
		b = b[n+1:]
	}
	return out, nil
}

// Fit returns the entry cut down to at most max bytes as a datagram. The longest value is
// shortened, at a character boundary and marked " [truncated]", until the entry fits; every
// field stays in it. An entry that fits comes back as it is.
func Fit(fields []Field, max int) []Field {
	size := len(Encode(fields))
	if size <= max {
		return fields
	}
	out := append([]Field(nil), fields...)
	for size > max {
		i := longest(out)
		if i < 0 {
			break // nothing left to cut: the keys alone are larger than max
		}
		keep := len(out[i].Value) - (size - max) - len(truncated)
		if keep <= 0 {
			out[i].Value = ""
		} else {
			out[i].Value = cut(out[i].Value, keep) + truncated
		}
		size = len(Encode(out))
	}
	return out
}

// longest is the index of the longest non-empty value of a valid field, or -1.
func longest(fields []Field) int {
	at, n := -1, 0
	for i, f := range fields {
		if validKey(f.Key) && len(f.Value) > n {
			at, n = i, len(f.Value)
		}
	}
	return at
}

// cut is s shortened to at most n bytes without splitting a UTF-8 sequence.
func cut(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func validKey(k string) bool {
	if k == "" || len(k) > 64 || k[0] == '_' || (k[0] >= '0' && k[0] <= '9') {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
