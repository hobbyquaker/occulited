package firmware

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

// B-56: eQ-3's index is metadata, bounded as a whole; a download waits HeaderWait for its header.
func TestFirmwareNoAnswer(t *testing.T) {
	addr := silentServer(t)
	c := New("http://" + addr)
	c.HTTP = &http.Client{Timeout: 2 * time.Minute}
	c.IndexTimeout, c.HeaderWait = 150*time.Millisecond, 150*time.Millisecond
	start := time.Now()
	if _, err := c.Index(context.Background()); err == nil {
		t.Fatal("index: no error")
	} else if na, ok := httpwait.As(err); !ok || na.Host != addr {
		t.Fatalf("index: %v", err)
	}
	if _, _, err := c.Download(context.Background(), "HmIP-BWTH", t.TempDir()); err == nil {
		t.Fatal("download: no error")
	} else if _, ok := httpwait.As(err); !ok {
		t.Fatalf("download: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("waited %s", time.Since(start))
	}
	if hostOf("::bad") != "::bad" {
		t.Fatal("hostOf of a bad URL")
	}
}
