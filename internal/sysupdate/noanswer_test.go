package sysupdate

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/httpwait"
)

// silentFeed accepts connections and never answers: GitHub from the lab during B-56.
func silentFeed(t *testing.T) string {
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

// B-56: a feed that accepts and never answers ends the check after FeedTimeout, not after the
// download client's 30 minutes, and the state names the host and the wait.
func TestFeedNoAnswer(t *testing.T) {
	addr := silentFeed(t)
	r := fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0-dev.42\n")
	s := New(r, "http://"+addr+"/releases", true, nil)
	s.FeedTimeout = 200 * time.Millisecond
	ctx := context.Background()

	start := time.Now()
	err := s.Check(ctx)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the check waited %s", time.Since(start))
	}
	if na, ok := httpwait.As(err); !ok || na.Host != addr {
		t.Fatalf("check: %v", err)
	}
	st := s.State()
	if st.ErrorHost != addr || st.ErrorTimeout != 0 || !strings.Contains(st.Error, "did not answer within") {
		t.Fatalf("state %+v", st)
	}

	// the release list (the command line's check) fails the same way and records it for the
	// default channel
	s2 := New(r, "http://"+addr+"/releases", true, nil)
	s2.FeedTimeout = 200 * time.Millisecond
	if _, err := s2.Releases(ctx, ""); err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("releases: %v", err)
	}
	if st := s2.State(); st.ErrorHost != addr || st.Checked == "" {
		t.Fatalf("releases state %+v", st)
	}
	// another channel is not the Updates page's answer: nothing recorded
	s3 := New(r, "http://"+addr+"/releases", true, nil)
	s3.FeedTimeout = 200 * time.Millisecond
	if _, err := s3.Releases(ctx, ChannelAll); err == nil {
		t.Fatal("no error")
	}
	if st := s3.State(); st.Error != "" {
		t.Fatalf("all-channel state %+v", st)
	}

	// a successful check afterwards clears the host and the wait
	s.FeedURL = "http://" + addr + "/x"
	s.remember(&Available{Version: "1.0.0-dev.42"}, nil)
	if st := s.State(); st.ErrorHost != "" || st.ErrorTimeout != 0 || st.Error != "" {
		t.Fatalf("cleared state %+v", st)
	}
}

// B-56: a download waits HeaderWait for its header, and the sha256 file is metadata.
func TestDownloadNoAnswer(t *testing.T) {
	addr := silentFeed(t)
	r := fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0-dev.42\n")
	s := New(r, "http://"+addr+"/releases", true, nil)
	s.FeedTimeout, s.HeaderWait = 200*time.Millisecond, 200*time.Millisecond
	ctx := context.Background()
	if _, err := s.download(ctx, &Available{Name: "a.zip", URL: "http://" + addr + "/a.zip"}); err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("download: %v", err)
	}
	if _, err := s.download(ctx, &Available{Name: "a.zip", URL: "http://" + addr + "/a.zip", SHA256URL: "http://" + addr + "/a.zip.sha256"}); err == nil || !strings.Contains(err.Error(), "sha256: ") || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("sha256: %v", err)
	}
	// the caller's own cancel stays the caller's
	c, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Check(c); err == nil || strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("cancelled check: %v", err)
	}
}

// B-57: a system ahead of the feed (a round not yet published) is reported as such, never as
// running the feed's newest version.
func TestInstalledNewer(t *testing.T) {
	r := fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0-dev.43\n")
	s := New(r, "http://127.0.0.1:1/releases", true, nil)
	s.remember(&Available{Version: "1.0.0-dev.42"}, nil)
	if st := s.State(); !st.InstalledNewer {
		t.Fatalf("%+v", st)
	}
	s.remember(&Available{Version: "1.0.0-dev.43"}, nil)
	if st := s.State(); st.InstalledNewer {
		t.Fatalf("same version: %+v", st)
	}
	s.remember(&Available{Version: "1.0.0-dev.44", Newer: true}, nil)
	if st := s.State(); st.InstalledNewer {
		t.Fatalf("an update: %+v", st)
	}
	// OpenCCU's versions are no semver: never "newer"
	o := New(fakeRoot(t, "VERSION=3.89.11.20260919\nPRODUCT=ova\nPLATFORM=ova\n"), "http://127.0.0.1:1/x", true, nil)
	o.remember(&Available{Version: "3.89.10.20260801"}, nil)
	if st := o.State(); st.InstalledNewer {
		t.Fatalf("OpenCCU: %+v", st)
	}
}
