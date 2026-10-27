package sysupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// occulited task 22: the release list behind `occulited update check` and `install <version>` -
// the channels, the direction of each version, the download of a named one (a downgrade among
// them) and its sha256 check.

func TestDirection(t *testing.T) {
	for _, c := range []struct{ running, target, want string }{
		{"1.0.0-dev.42", "1.0.0-dev.43", "upgrade"},
		{"1.0.0-dev.42", "1.0.0-dev.42", "same"},
		{"1.0.0-dev.42", "1.0.0-dev.41", "downgrade"},
		{"1.0.0-dev.42", "1.0.0-beta.0", "upgrade"},
		{"1.0.0", "1.0.0-rc.1", "downgrade"},
		{"1.0.0-dev.42", "", "other"},
		{"3.89.11.20260919", "3.90.0.1", "other"},
		{"3.89.11.20260919", "3.89.11.20260919", "same"},
	} {
		if got := Direction(c.running, c.target); got != c.want {
			t.Errorf("Direction(%q, %q) = %q, want %q", c.running, c.target, got, c.want)
		}
	}
}

func TestReleasesAndDownloadVersion(t *testing.T) {
	body := releaseZip()
	sum := sha256.Sum256(body)
	var srv *httptest.Server
	sha := func(v string) string {
		return fmt.Sprintf(`{"name":"openccu-lite-x86_64-ova-%s.zip.sha256","browser_download_url":"%s/%s.sha256","size":1}`, v, srv.URL, v)
	}
	rel := func(v string, pre bool, extra string) string {
		return fmt.Sprintf(`{"tag_name":"v%s","prerelease":%t,"html_url":"%s/rel/%s","published_at":"2026-10-01T10:00:00Z","assets":[{"name":"openccu-lite-aarch64-rpi4-%s.zip","browser_download_url":"x","size":1},{"name":"openccu-lite-x86_64-ova-%s.zip","browser_download_url":"%s/%s","size":%d}%s]}`, v, pre, srv.URL, v, v, v, srv.URL, v, len(body), extra)
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := strings.TrimPrefix(r.URL.Path, "/"); {
		case p == "releases":
			fmt.Fprint(w, "["+strings.Join([]string{
				rel("1.0.0-dev.41", true, ","+sha("1.0.0-dev.41")),
				rel("1.0.0-dev.43", true, ","+sha("1.0.0-dev.43")),
				rel("0.9.0", false, ","+sha("0.9.0")),
				rel("1.0.0-dev.42", true, ","+sha("1.0.0-dev.42")),
				rel("1.0.0-dev.40", true, ""), // no .sha256
				rel("1.0.0-dev.39", true, ","+sha("1.0.0-dev.39")),
			}, ",")+"]")
		case p == "1.0.0-dev.39.sha256":
			w.WriteHeader(500) // published, but not readable
		case strings.HasSuffix(p, ".sha256"):
			fmt.Fprintf(w, "%s  file\n", hex.EncodeToString(sum[:]))
		case strings.HasPrefix(p, "1.") || strings.HasPrefix(p, "0."):
			_, _ = w.Write(body)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	r := fakeRoot(t, "VERSION=3.89.11.20260919\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0-dev.42\n")
	s := New(r, srv.URL+"/releases", true, nil)
	s.Channel = ChannelDev

	// the stored channel (dev), newest first; the newest is remembered
	l, err := s.Releases(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range l.Releases {
		got = append(got, x.Version+":"+x.Direction)
	}
	want := "1.0.0-dev.43:upgrade 1.0.0-dev.42:same 1.0.0-dev.41:downgrade 1.0.0-dev.40:downgrade 1.0.0-dev.39:downgrade 0.9.0:downgrade"
	if strings.Join(got, " ") != want || l.Channel != ChannelDev || l.Default != ChannelDev || l.Running != "1.0.0-dev.42" {
		t.Fatalf("%s / %+v", strings.Join(got, " "), l)
	}
	if !l.Releases[0].Prerelease || l.Releases[5].Prerelease || l.Releases[3].SHA256URL != "" || l.Releases[0].SHA256URL == "" {
		t.Errorf("%+v", l.Releases)
	}
	if av := s.State().Available; av == nil || av.Version != "1.0.0-dev.43" || !av.Newer {
		t.Errorf("not remembered: %+v", av)
	}
	// --stable: releases only; not the Updates page's channel, so nothing is remembered from it
	l, err = s.Releases(ctx, ChannelStable)
	if err != nil || len(l.Releases) != 1 || l.Releases[0].Version != "0.9.0" || l.Default != ChannelDev {
		t.Fatalf("%v %+v", err, l)
	}
	if s.State().Available.Version != "1.0.0-dev.43" {
		t.Error("the stable list replaced the remembered release")
	}
	// --pre is the dev channel's old name; beta lists no dev build
	if l, err := s.Releases(ctx, ChannelPre); err != nil || l.Channel != ChannelDev || len(l.Releases) != 6 {
		t.Errorf("pre: %v %+v", err, l)
	}
	if l, err := s.Releases(ctx, ChannelBeta); err != nil || l.Channel != ChannelBeta || len(l.Releases) != 1 {
		t.Errorf("beta: %v %+v", err, l)
	}
	if _, err := s.Releases(ctx, "nightly"); err == nil {
		t.Error("an unknown channel was taken")
	}

	// a named older version: downloaded, verified, staged
	u, err := s.DownloadVersion(ctx, "v1.0.0-dev.41")
	if err != nil {
		t.Fatal(err)
	}
	if u.Version != "1.0.0-dev.41" || u.SHA256 != hex.EncodeToString(sum[:]) || u.Foreign {
		t.Fatalf("%+v", u)
	}
	// "" is the newest of the default channel
	if u, err := s.DownloadVersion(ctx, ""); err != nil || u.Version != "1.0.0-dev.43" {
		t.Fatalf("%v %+v", err, u)
	}
	// a version the feed does not list
	if _, err := s.DownloadVersion(ctx, "1.0.0-dev.7"); !errors.Is(err, ErrNoSuchVersion) {
		t.Errorf("%v", err)
	}
	// no .sha256 published: staged, but not verified (the command line refuses that itself)
	if u, err := s.DownloadVersion(ctx, "1.0.0-dev.40"); err != nil || u.SHA256 != "" || !strings.Contains(u.Warning, "not verified") {
		t.Errorf("%v %+v", err, u)
	}
	// a published .sha256 that cannot be read fails the download, and nothing new is staged
	r.DiscardSystemUpdate()
	if _, err := s.DownloadVersion(ctx, "1.0.0-dev.39"); err == nil || !strings.Contains(err.Error(), "sha256: HTTP 500") {
		t.Errorf("%v", err)
	}
	if r.StagedSystemUpdate() != nil {
		t.Error("staged without its checksum")
	}

	// without a stored channel a system is on stable (D-122), a released one as a prerelease one
	rs := fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=0.9.0\n")
	if l, err := New(r, srv.URL+"/releases", true, nil).Releases(ctx, ""); err != nil || l.Channel != ChannelStable || l.Default != ChannelStable || len(l.Releases) != 1 || l.Releases[0].Direction != "downgrade" {
		t.Errorf("a dev system without a channel: %v %+v", err, l)
	}
	if l, err := New(rs, srv.URL+"/releases", true, nil).Releases(ctx, ""); err != nil || l.Channel != ChannelStable || len(l.Releases) != 1 || l.Releases[0].Direction != "same" {
		t.Errorf("%v %+v", err, l)
	}
	// the feed's failures
	gone := New(r, srv.URL+"/nothing", true, nil)
	if _, err := gone.Releases(ctx, ""); err == nil || !strings.Contains(err.Error(), "no release published yet") {
		t.Errorf("%v", err)
	}
}
