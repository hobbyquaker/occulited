package priv

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B-71: the socket's deadline is the context's and can fire before the context's own timer; the
// caller then got "read unix ...: i/o timeout" where it expects context.DeadlineExceeded.
func TestClientErrDeadline(t *testing.T) {
	// a real socket timeout, from a read whose deadline has passed
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetReadDeadline(time.Now().Add(-time.Second))
	_, readErr := a.Read(make([]byte, 1))
	if !errors.Is(readErr, os.ErrDeadlineExceeded) {
		t.Fatalf("setup: %v", readErr)
	}

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cancelled, cancel2 := context.WithCancel(context.Background())
	cancel2()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want error
		text string
	}{
		{"socket deadline, context timer not yet fired", context.Background(), readErr, context.DeadlineExceeded, "context deadline exceeded"},
		{"wrapped socket deadline", context.Background(), fmt.Errorf("decode: %w", readErr), context.DeadlineExceeded, "context deadline exceeded"},
		{"context deadline", expired, readErr, context.DeadlineExceeded, "context deadline exceeded"},
		{"context cancelled", cancelled, errors.New("use of closed network connection"), context.Canceled, "context canceled"},
		{"other error", context.Background(), errors.New("connection reset by peer"), nil, "privilege helper: connection reset by peer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clientErr(tc.ctx, tc.err)
			if tc.want != nil && !errors.Is(got, tc.want) {
				t.Errorf("errors.Is(%v, %v) = false", got, tc.want)
			}
			if tc.want == nil && (errors.Is(got, context.DeadlineExceeded) || errors.Is(got, context.Canceled)) {
				t.Errorf("%v is not a context error", got)
			}
			if got.Error() != tc.text {
				t.Errorf("text %q, want %q", got.Error(), tc.text)
			}
		})
	}
}

// End to end: a helper that takes the request and never answers. Whichever of the socket's
// deadline and the context's timer fires first, the caller sees the context's deadline.
func TestClientCallTimeoutIsDeadlineExceeded(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan net.Conn, 64)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			held <- conn // read nothing, answer nothing
		}
	}()
	defer func() {
		l.Close()
		for {
			select {
			case c := <-held:
				c.Close()
			default:
				return
			}
		}
	}()
	c := Client{Socket: sock}
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := c.Run(ctx, "true", nil, nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "i/o timeout") {
			t.Fatalf("round %d: %v, want context deadline exceeded", i, err)
		}
	}
}
