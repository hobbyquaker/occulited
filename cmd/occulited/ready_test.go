package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/sdnotify"
)

// fakeSystemd is the notification socket; got delivers each datagram.
func fakeSystemd(t *testing.T) (*sdnotify.Notifier, <-chan string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify")
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	got := make(chan string, 4)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			got <- string(buf[:n])
		}
	}()
	return sdnotify.New(path), got
}

func quiet(t *testing.T, got <-chan string, d time.Duration, phase string) {
	t.Helper()
	select {
	case m := <-got:
		t.Fatalf("%s: systemd was told %q", phase, m)
	case <-time.After(d):
	}
}

// READY=1 goes out only once the server answers its version route with 200: not while the socket
// is merely bound (it accepts, nobody serves), not while it answers 503, and then exactly once.
func TestReadyOnlyOnceTheAPIAnswers(t *testing.T) {
	defer func(p, l time.Duration) { readyPoll, readyProbeTimeout = p, l }(readyPoll, readyProbeTimeout)
	readyPoll, readyProbeTimeout = 20*time.Millisecond, 200*time.Millisecond
	n, got := fakeSystemd(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- notifyWhenAnswering(ctx, n, "http://"+ln.Addr().String(), log) }()

	quiet(t, got, 500*time.Millisecond, "bound, not serving")

	var ok atomic.Bool
	var paths atomic.Value
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Store(r.URL.Path)
		if !ok.Load() {
			http.Error(w, `{"error":"starting"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"version":"x"}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	quiet(t, got, 300*time.Millisecond, "answering 503")

	ok.Store(true)
	select {
	case m := <-got:
		if m != "READY=1\nSTATUS=serving\n" {
			t.Errorf("datagram %q", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no READY=1 once the API answers")
	}
	if !<-done {
		t.Error("notifyWhenAnswering reported false after READY")
	}
	if p, _ := paths.Load().(string); p != readyPath {
		t.Errorf("probed %q, want %q", p, readyPath)
	}
	quiet(t, got, 200*time.Millisecond, "after READY")
}

// A stop before the API answers ends the probing without a READY; no socket means no probing.
func TestReadyEndsWithTheContext(t *testing.T) {
	defer func(p time.Duration) { readyPoll = p }(readyPoll)
	readyPoll = 20 * time.Millisecond
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n, got := fakeSystemd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// a port nothing listens on: every probe is refused at once
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if notifyWhenAnswering(ctx, n, "http://"+addr, log) {
		t.Error("READY reported without an answer")
	}
	quiet(t, got, 100*time.Millisecond, "stopped before answering")
	if notifyWhenAnswering(context.Background(), nil, "http://"+addr, log) {
		t.Error("READY reported without a socket")
	}
}
