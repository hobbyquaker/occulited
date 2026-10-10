package catalog

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/httpwait"
)

// silentServer accepts connections and never answers (B-56).
func silentServer(t *testing.T) string {
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

// B-56: a catalogue request to a host that never answers ends after HeaderWait per try (one retry
// on a fresh connection), not after the client's 10 minutes.
func TestCatalogNoAnswer(t *testing.T) {
	addr := silentServer(t)
	s := &Service{HTTP: &http.Client{Timeout: 10 * time.Minute}, HeaderWait: 150 * time.Millisecond}
	start := time.Now()
	_, _, err := s.getETag(context.Background(), "http://"+addr+"/catalog.json", "", 1<<20)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("waited %s", time.Since(start))
	}
	if na, ok := httpwait.As(err); !ok || na.Host != addr {
		t.Fatalf("err = %v", err)
	}
}
