package httpwait

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// silent accepts connections and never sends a byte: GitHub on the lab during B-56.
func silent(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	return ln.Addr().String()
}

func TestBoundNoAnswer(t *testing.T) {
	addr := silent(t)
	c := &http.Client{Timeout: 30 * time.Minute}
	ctx, explain, cancel := Bound(context.Background(), 150*time.Millisecond, addr)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/releases", nil)
	start := time.Now()
	_, err := c.Do(req)
	err = explain(err)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("waited %s", time.Since(start))
	}
	na, ok := As(err)
	if !ok {
		t.Fatalf("err = %v, want a NoAnswerError", err)
	}
	if na.Host != addr || na.Seconds() != 0 || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("na = %+v, %q", na, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("not a deadline: %v", err)
	}
}

func TestBoundCallerCancelStaysCallers(t *testing.T) {
	addr := silent(t)
	parent, stop := context.WithCancel(context.Background())
	ctx, explain, cancel := Bound(parent, time.Minute, addr)
	defer cancel()
	go func() { time.Sleep(50 * time.Millisecond); stop() }()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/", nil)
	_, err := http.DefaultClient.Do(req)
	err = explain(err)
	if _, ok := As(err); ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancel", err)
	}
	if explain(nil) != nil {
		t.Fatal("nil explained into an error")
	}
	other := errors.New("refused")
	if explain(other) != other {
		t.Fatal("an unrelated error was rewritten")
	}
}

func TestBoundDefault(t *testing.T) {
	ctx, _, cancel := Bound(context.Background(), 0, "h")
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl) > Meta || time.Until(dl) < Meta-time.Second {
		t.Fatalf("deadline %v", dl)
	}
}

func TestDoHeaderWait(t *testing.T) {
	addr := silent(t)
	c := &http.Client{Timeout: 30 * time.Minute}
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/image.tgz", nil)
	start := time.Now()
	_, err := Do(c, req, 150*time.Millisecond)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("waited %s", time.Since(start))
	}
	if na, ok := As(err); !ok || na.Host != addr {
		t.Fatalf("err = %v", err)
	}
}

func TestDoBodyOutlivesTheWait(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "6")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond) // slower than the header wait: the body must survive it
		_, _ = w.Write([]byte("abcdef"))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	res, err := Do(http.DefaultClient, req, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil || string(b) != "abcdef" {
		t.Fatalf("body %q, %v", b, err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDoCallerCancel(t *testing.T) {
	addr := silent(t)
	ctx, stop := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); stop() }()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/", nil)
	_, err := Do(http.DefaultClient, req, 0)
	if _, ok := As(err); ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
