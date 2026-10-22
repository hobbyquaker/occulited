package rpcsub

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// B-101: the listener keeps an idle keep-alive connection open longer than hmipserver's JDK
// HttpClient pool does (1200 s), so the client never sends an event onto a connection we are
// closing.
func TestCallbackIdleTimeoutAboveClientPool(t *testing.T) {
	srv := callbackServer(http.NotFoundHandler(), callbackReadTimeout, callbackIdleTimeout)
	if srv.IdleTimeout <= 1200*time.Second {
		t.Fatalf("IdleTimeout %s is not above the JDK pool's 1200 s", srv.IdleTimeout)
	}
	if srv.IdleTimeout <= srv.ReadTimeout {
		t.Fatalf("IdleTimeout %s does not exceed ReadTimeout %s - the read bound would govern the idle wait", srv.IdleTimeout, srv.ReadTimeout)
	}
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 {
		t.Fatalf("one call keeps its bounds: header %s read %s write %s", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout)
	}
}

// With the bounds scaled down: a connection kept idle for longer than the read timeout still
// serves the next call (the idle wait is the idle timeout's, not the read timeout's), and the
// same wait on a server whose idle timeout is shorter loses the connection - which is what the
// production system saw, and what shows this test would notice the regression.
func TestCallbackIdleConnectionServesNextCall(t *testing.T) {
	const read, idle = 100 * time.Millisecond, 5 * time.Second
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	for _, tc := range []struct {
		name  string
		idle  time.Duration
		alive bool
	}{
		{"idle above the wait", idle, true},
		{"idle below the wait (the old shape)", read, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewUnstartedServer(h)
			ts.Config = callbackServer(h, read, tc.idle)
			ts.Start()
			defer ts.Close()
			conn, err := net.Dial("tcp", ts.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			r := bufio.NewReader(conn)
			call := func() (string, error) {
				if _, err := conn.Write([]byte("GET /status HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
					return "", err
				}
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				resp, err := http.ReadResponse(r, nil)
				if err != nil {
					return "", err
				}
				defer resp.Body.Close()
				b := make([]byte, 16)
				n, _ := resp.Body.Read(b)
				return strings.TrimSpace(string(b[:n])), nil
			}
			if got, err := call(); err != nil || got != "ok" {
				t.Fatalf("first call: %q, %v", got, err)
			}
			time.Sleep(3 * read) // idle: longer than the read bound, shorter than the idle bound
			got, err := call()
			if tc.alive && (err != nil || got != "ok") {
				t.Fatalf("the second call on the idle connection: %q, %v", got, err)
			}
			if !tc.alive && err == nil {
				t.Fatalf("the control: the server with the short idle timeout still answered %q", got)
			}
		})
	}
}
