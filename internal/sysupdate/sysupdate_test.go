package sysupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/system"
)

func fakeRoot(t *testing.T, version string) system.Root {
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "VERSION"), []byte(version), 0o644)
	_ = os.MkdirAll(filepath.Join(d, "usr/local/tmp"), 0o755)
	return system.Root(d)
}

func releaseZip() []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, n := range []string{"OpenCCU-3.90.0.20261001-x86_64-ova.img", "EULA.en", "EULA.de"} {
		f, _ := w.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Store})
		_, _ = f.Write(bytes.Repeat([]byte("q"), 300))
	}
	_ = w.Close()
	return b.Bytes()
}

func TestCheckAndDownload(t *testing.T) {
	body := releaseZip()
	sum := sha256.Sum256(body)
	var srv *httptest.Server
	hits := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			hits++
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprintf(w, `{"tag_name":"3.90.0.20261001","html_url":"%s/rel","published_at":"2026-10-01T10:00:00Z","assets":[
{"name":"OpenCCU-3.90.0.20261001-aarch64-rpi4.zip","browser_download_url":"%s/x","size":1},
{"name":"openccu-lite-x86_64-ova-1.0.1.zip","browser_download_url":"%s/asset","size":%d},
{"name":"openccu-lite-x86_64-ova-1.0.1.zip.sha256","browser_download_url":"%s/asset.sha256","size":100}]}`, srv.URL, srv.URL, srv.URL, len(body), srv.URL)
		case "/asset":
			_, _ = w.Write(body)
		case "/asset.sha256":
			fmt.Fprintf(w, "%s  openccu-lite-x86_64-ova-1.0.1.zip\n", hex.EncodeToString(sum[:]))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	r := fakeRoot(t, "VERSION=3.89.8.20260906\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\n")
	s := New(r, srv.URL+"/releases/latest", true, nil)
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.Available == nil || st.Available.Version != "1.0.1" || !st.Available.Newer || st.Available.SHA256URL == "" || st.Error != "" {
		t.Fatalf("%+v", st)
	}
	if err := s.Check(context.Background()); err != nil || hits != 2 || s.State().Available == nil {
		t.Fatalf("etag round: %v %d", err, hits)
	}
	staged, err := s.Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if staged.Kind != "zip" || staged.Board != "x86_64-ova" || !strings.Contains(staged.Warning, "verified") {
		t.Fatalf("%+v", staged)
	}
	if r.StagedSystemUpdate() == nil {
		t.Error("not staged")
	}
	// same version: nothing newer, and a container refuses
	r2 := fakeRoot(t, "VERSION=3.90.0.20261001\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.1\n")
	s2 := New(r2, srv.URL+"/releases/latest", true, nil)
	_ = s2.Check(context.Background())
	if s2.State().Available == nil || s2.State().Available.Newer {
		t.Errorf("%+v", s2.State())
	}
	r3 := fakeRoot(t, "VERSION=1\nPRODUCT=oci_amd64\nPLATFORM=oci\nVARIANT=lite\n")
	if err := New(r3, srv.URL+"/releases/latest", true, nil).Check(context.Background()); err == nil {
		t.Error("container accepted")
	}
}

// Every box before release day asks a feed with no published release, and GitHub answers 404 on
// releases/latest for that: "HTTP 404 Not Found" on the Status page reads like a broken box.
func TestFeedWithNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	t.Cleanup(srv.Close)
	r := fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\n")
	s := New(r, srv.URL+"/latest", true, nil)
	err := s.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no release published yet") {
		t.Fatalf("%v", err)
	}
	if st := s.State(); !strings.Contains(st.Error, "no release published yet") || st.Available != nil {
		t.Errorf("%+v", st)
	}
}

// B-115: GitHub's rate limit answer is JSON with a message and a documentation URL; the Status page
// showed the raw body, one run of text wider than a phone. The message is what stays.
func TestFeedErrorText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"message":"API rate limit exceeded for 203.0.113.7. (But here's the good news: Authenticated requests get a higher rate limit. Check out the documentation for more details.)","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api"}`)
	}))
	t.Cleanup(srv.Close)
	r := fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\n")
	s := New(r, srv.URL+"/latest", true, nil)
	if err := s.Check(context.Background()); err == nil {
		t.Fatal("a 403 checked fine")
	}
	st := s.State().Error
	if !strings.HasPrefix(st, "feed: HTTP 403 API rate limit exceeded for 203.0.113.7.") || strings.Contains(st, "documentation_url") || strings.Contains(st, "{") {
		t.Errorf("%q", st)
	}
	for body, want := range map[string]string{
		"  Service Unavailable\n":     "Service Unavailable",
		`{"message":""}`:              `{"message":""}`,
		`{"error":"x"}`:               `{"error":"x"}`,
		"":                            "",
		strings.Repeat("ä", 400):      strings.Repeat("ä", feedErrorMax) + "…",
		`{"message":" Bad gateway "}`: "Bad gateway",
	} {
		if got := feedErrorText([]byte(body)); got != want {
			t.Errorf("%q: %q, want %q", body, got, want)
		}
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	body := releaseZip()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"t","assets":[{"name":"openccu-lite-x86_64-ova-2.zip","browser_download_url":"%s/a","size":%d},{"name":"openccu-lite-x86_64-ova-2.zip.sha256","browser_download_url":"%s/s","size":1}]}`, srv.URL, len(body), srv.URL)
		case "/a":
			_, _ = w.Write(body)
		case "/s":
			fmt.Fprint(w, "0000000000000000000000000000000000000000000000000000000000000000  x\n")
		}
	}))
	t.Cleanup(srv.Close)
	r := fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\n")
	s := New(r, srv.URL+"/latest", true, nil)
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(context.Background()); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("%v", err)
	}
	if r.StagedSystemUpdate() != nil {
		t.Error("bad download left staged")
	}
}

// The feed looks for the lite product name that D-39/D-43 give the release files, resolved from
// the upstream PRODUCT that D-31 keeps in /VERSION. The table is the inverse of the case in the
// fork's board/lite/post-build.sh; a PRODUCT missing from it must say so rather than look for a
// file nobody publishes.
func TestAssetSuffix(t *testing.T) {
	for _, c := range []struct {
		version, want, wantErr string
	}{
		{"PRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\n", "openccu-lite-x86_64-ova-.zip", ""},
		{"PRODUCT=rpi3\nPLATFORM=rpi3\nVARIANT=lite\n", "openccu-lite-aarch64-rpi3-.zip", ""},
		{"PRODUCT=rpi4\nPLATFORM=rpi4\nVARIANT=lite\n", "openccu-lite-aarch64-rpi4-.zip", ""},
		// upstream's own box: the file is named after PRODUCT itself
		{"PRODUCT=rpi4\nPLATFORM=rpi4\n", "OpenCCU--rpi4.zip", ""},
		{"PRODUCT=lxc_arm64\nPLATFORM=lxc\n", "OpenCCU--lxc_arm64.tar.xz", ""},
		// task 34: the lite CT templates carry the board name and the template's extension
		{"PRODUCT=lxc_amd64\nPLATFORM=lxc\nVARIANT=lite\n", "openccu-lite-lxc-amd64-.tar.xz", ""},
		{"PRODUCT=lxc_arm64\nPLATFORM=lxc\nVARIANT=lite\n", "openccu-lite-lxc-arm64-.tar.xz", ""},
		{"PRODUCT=rpi5\nPLATFORM=rpi5\nVARIANT=lite\n", "openccu-lite-aarch64-rpi5-.zip", ""},
		// a lite box on a product openccu-lite does not build an image for
		{"PRODUCT=odroid-c4\nPLATFORM=odroid-c4\nVARIANT=lite\n", "", "no openccu-lite image for PRODUCT=odroid-c4"},
		{"PRODUCT=oci_amd64\nPLATFORM=oci\nVARIANT=lite\n", "", "container"},
		{"PLATFORM=ova\n", "", "no PRODUCT"},
	} {
		pre, suf, err := assetPattern(fakeRoot(t, "VERSION=1\n"+c.version).ReadVersion())
		got := pre + suf
		switch {
		case c.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%q: %v, wanted %q", c.version, err, c.wantErr)
			}
		case err != nil || got != c.want:
			t.Errorf("%q: %q %v, wanted %q", c.version, got, err, c.want)
		}
	}
}

func TestSemverNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.0-alpha.1", "1.0.0-alpha.0", true},
		{"1.0.0-alpha.0", "1.0.0-alpha.1", false},
		{"1.0.0-beta.0", "1.0.0-alpha.9", true},
		{"1.0.0-rc.1", "1.0.0-beta.3", true},
		{"1.0.0", "1.0.0-rc.1", true},
		{"1.0.0-rc.1", "1.0.0", false},
		{"1.0.1", "1.0.0", true},
		{"1.10.0", "1.9.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0-alpha.0-snapshot.abc", "1.0.0-alpha.0", true},
		{"0-beta.2", "1.0.0-alpha.0", true}, // not semver on one side: differs
		// openccu-lite task 291: dev < alpha < beta < rc, not SemVer's ASCII order (dev > beta)
		{"1.0.0-alpha.0", "1.0.0-dev.30", true},
		{"1.0.0-beta.0", "1.0.0-dev.31", true},
		{"1.0.0-dev.31", "1.0.0-beta.0", false},
		{"1.0.0-dev.31", "1.0.0-dev.30", true},
		{"1.0.0-beta.10", "1.0.0-beta.1", true},
		{"1.0.0-beta.1", "1.0.0-beta.10", false},
		{"1.0.0-dev.99", "1.0.0-rc.1", false},
		{"1.0.0-dev.0", "1.0.0-nightly.5", true}, // an unknown tag ranks below dev
	} {
		if got := semverNewer(c.a, c.b); got != c.want {
			t.Errorf("%s newer than %s: %v", c.a, c.b, got)
		}
	}
	// the whole chain, each one newer than every one before it and none newer than itself
	chain := []string{"1.0.0-dev.30", "1.0.0-alpha.0", "1.0.0-beta.0", "1.0.0-beta.1", "1.0.0-beta.10", "1.0.0-rc.1", "1.0.0"}
	for i, a := range chain {
		for j, b := range chain {
			if got := semverNewer(a, b); got != (i > j) {
				t.Errorf("%s newer than %s: %v", a, b, got)
			}
		}
	}
	if p, s, err := assetPattern(system.Version{Product: "ova", Platform: "ova", Variant: "lite", Lite: "1.0.0-alpha.0"}); err != nil || p != "openccu-lite-x86_64-ova-" || s != ".zip" {
		t.Errorf("lite pattern: %q %q %v", p, s, err)
	}
	if p, s, err := assetPattern(system.Version{Product: "rpi4", Platform: "rpi4"}); err != nil || p != "OpenCCU-" || s != "-rpi4.zip" {
		t.Errorf("upstream pattern: %q %q %v", p, s, err)
	}
}

// task 258: the check reads the release list, because releases/latest never returns a
// prerelease; task 118 (D-122): the channel decides what is offered - stable releases only, beta
// with alpha/beta/rc, dev everything. The newest by semver wins whatever the list's order, drafts
// never count, and an older version than the running one is never "newer" (no downgrade).
func TestReleaseListFollowsTheChannel(t *testing.T) {
	asset := func(v string) string {
		return fmt.Sprintf(`{"name":"openccu-lite-x86_64-ova-%s.zip","browser_download_url":"https://example.org/%s","size":1},{"name":"openccu-lite-x86_64-ova-%s.zip.sha256","browser_download_url":"https://example.org/%s.sha256","size":1}`, v, v, v, v)
	}
	rel := func(tag string, pre, draft bool, v string) string {
		return fmt.Sprintf(`{"tag_name":"%s","prerelease":%t,"draft":%t,"html_url":"https://example.org/%s","assets":[%s]}`, tag, pre, draft, tag, asset(v))
	}
	list := "[" + strings.Join([]string{
		rel("v1.0.0-dev.30", true, true, "1.0.0-dev.30"), // a draft: never
		rel("v1.0.0-dev.26", true, false, "1.0.0-dev.26"),
		rel("v0.9.0", false, false, "0.9.0"),
		rel("v1.0.0-dev.27", true, false, "1.0.0-dev.27"), // out of order on purpose
		rel("v0.9.1", false, false, "0.9.1"),
		`{"tag_name":"v2-other","prerelease":false,"assets":[{"name":"openccu-lite-aarch64-rpi4-2.0.0.zip","browser_download_url":"x","size":1}]}`,
	}, ",") + "]"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			fmt.Fprint(w, list)
		case "/empty":
			fmt.Fprint(w, " [ ]\n")
		case "/beta":
			// openccu-lite task 291: the first beta after the last dev release, newest first as GitHub lists them
			fmt.Fprint(w, "["+rel("v1.0.0-beta.0", true, false, "1.0.0-beta.0")+","+rel("v1.0.0-dev.31", true, false, "1.0.0-dev.31")+","+rel("v1.0.0-dev.30", true, false, "1.0.0-dev.30")+"]")
		case "/beta-reversed":
			fmt.Fprint(w, "["+rel("v1.0.0-dev.31", true, false, "1.0.0-dev.31")+","+rel("v1.0.0-beta.0", true, false, "1.0.0-beta.0")+"]")
		case "/flagged":
			fmt.Fprint(w, "["+rel("v1.2.0", true, false, "1.2.0")+","+rel("v1.1.0", false, false, "1.1.0")+"]")
		case "/unflagged":
			// a prerelease version the publisher forgot to flag is still a prerelease
			fmt.Fprint(w, "["+rel("v1.1.0-beta.1", false, false, "1.1.0-beta.1")+","+rel("v1.0.1", false, false, "1.0.1")+"]")
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	for _, c := range []struct {
		name, running, channel, feed, want, tag string
		newer                                   bool
	}{
		{"stable: releases only", "0.9.0", ChannelStable, "/releases", "0.9.1", "v0.9.1", true},
		{"stable: a dev system without a channel is on stable, offered nothing older", "1.0.0-dev.25", "", "/releases", "0.9.1", "v0.9.1", false},
		{"stable: an unflagged prerelease version is skipped", "1.0.0", ChannelStable, "/unflagged", "1.0.1", "v1.0.1", true},
		{"stable: a flagged release is a prerelease", "1.0.0", ChannelStable, "/flagged", "1.1.0", "v1.1.0", true},
		{"beta: a flagged release without a prerelease part is beta's", "1.0.0", ChannelBeta, "/flagged", "1.2.0", "v1.2.0", true},
		{"beta: the beta over an older release", "1.1.0-alpha.2", ChannelBeta, "/unflagged", "1.1.0-beta.1", "v1.1.0-beta.1", true},
		{"beta: a release system on beta takes the beta", "1.0.1", ChannelBeta, "/unflagged", "1.1.0-beta.1", "v1.1.0-beta.1", true},
		{"beta: a dev system is offered the first beta", "1.0.0-dev.30", ChannelBeta, "/beta", "1.0.0-beta.0", "v1.0.0-beta.0", true},
		{"beta: not the last dev build", "1.0.0-beta.0", ChannelBeta, "/beta", "1.0.0-beta.0", "v1.0.0-beta.0", false},
		{"beta: nor in the other order", "1.0.0-beta.0", ChannelBeta, "/beta-reversed", "1.0.0-beta.0", "v1.0.0-beta.0", false},
		{"beta: no dev build at all", "1.0.0-dev.25", ChannelBeta, "/releases", "0.9.1", "v0.9.1", false},
		{"dev: the newest prerelease", "1.0.0-dev.25", ChannelDev, "/releases", "1.0.0-dev.27", "v1.0.0-dev.27", true},
		{"dev: the newest prerelease is not newer than itself", "1.0.0-dev.27", ChannelDev, "/releases", "1.0.0-dev.27", "v1.0.0-dev.27", false},
		{"dev: the last dev build is offered the first beta", "1.0.0-dev.31", ChannelDev, "/beta", "1.0.0-beta.0", "v1.0.0-beta.0", true},
		{"dev: whatever the list's order", "1.0.0-dev.31", ChannelDev, "/beta-reversed", "1.0.0-beta.0", "v1.0.0-beta.0", true},
		{"dev: a release system on dev takes the dev build", "0.9.1", ChannelDev, "/releases", "1.0.0-dev.27", "v1.0.0-dev.27", true},
		{"an unknown stored channel is stable", "1.0.0", "nightly", "/unflagged", "1.0.1", "v1.0.1", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := fakeRoot(t, "VERSION=3.89.11.20260919\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE="+c.running+"\n")
			s := New(r, srv.URL+c.feed+"?per_page=20", true, nil)
			s.Channel = c.channel
			if err := s.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			st := s.State()
			av := st.Available
			if av == nil || av.Version != c.want || av.Tag != c.tag || av.Newer != c.newer || av.SHA256URL != "https://example.org/"+c.want+".sha256" {
				t.Fatalf("%+v", av)
			}
			if st.Channel != NormalChannel(c.channel) || st.InstalledNewer != (!c.newer && c.want != c.running) {
				t.Errorf("state %+v", st)
			}
		})
	}
	// the list of a repository with nothing published reads as "nothing yet", not as an error
	r := fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0-dev.25\n")
	if err := New(r, srv.URL+"/empty", true, nil).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "no release published yet") {
		t.Errorf("empty list: %v", err)
	}
	// a system on stable with nothing but prereleases in the list is offered nothing - and that is
	// no failed check (stable before the first release): none_in_channel. Its channel switched to
	// dev, the remembered answer is forgotten at once and the next check offers the dev build.
	r = fakeRoot(t, "VERSION=1\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0-rc.3\n")
	onlyPre := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"one"`)
		fmt.Fprint(w, "["+rel("v1.1.0-dev.1", true, false, "1.1.0-dev.1")+"]")
	}))
	t.Cleanup(onlyPre.Close)
	ps := New(r, onlyPre.URL, true, nil)
	if err := ps.Check(context.Background()); err != nil {
		t.Errorf("prereleases only: %v", err)
	}
	if st := ps.State(); st.Available != nil || st.Error != "" || !st.NoneInChannel || st.Checked == "" || st.Channel != ChannelStable {
		t.Errorf("prereleases only: %+v", st)
	}
	ps.SetChannel(ChannelDev)
	if st := ps.State(); st.NoneInChannel || st.Checked != "" || st.Channel != ChannelDev {
		t.Errorf("after the switch: %+v", st)
	}
	if err := ps.Check(context.Background()); err != nil || ps.State().Available == nil || ps.State().Available.Version != "1.1.0-dev.1" || !ps.State().Available.Newer {
		t.Errorf("dev: %v %+v", err, ps.State())
	}
	ps.SetChannel("nonsense")
	if ps.State().Channel != ChannelStable || ps.State().Available != nil {
		t.Errorf("back to stable: %+v", ps.State())
	}
	// an upstream (OpenCCU) system takes the first matching release of the list
	up := fakeRoot(t, "VERSION=3.89.11.20260919\nPRODUCT=rpi4\nPLATFORM=rpi4\n")
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"b","assets":[{"name":"OpenCCU-3.90.1.1-rpi4.zip","browser_download_url":"x","size":1}]},{"tag_name":"a","assets":[{"name":"OpenCCU-3.90.0.1-rpi4.zip","browser_download_url":"y","size":1}]}]`)
	}))
	t.Cleanup(upSrv.Close)
	s := New(up, upSrv.URL, true, nil)
	if err := s.Check(context.Background()); err != nil || s.State().Available.Version != "3.90.1.1" {
		t.Fatalf("%v %+v", err, s.State().Available)
	}
	// a feed that is neither shape
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "[{") }))
	t.Cleanup(bad.Close)
	bs := New(r, bad.URL, true, nil)
	if err := bs.Check(context.Background()); err == nil || !strings.HasPrefix(bs.State().Error, "feed:") {
		t.Errorf("broken list: %v %q", err, bs.State().Error)
	}
}

// task 118: which channel a release belongs to, by its prerelease part's first identifier and
// GitHub's pre-release flag.
func TestInChannel(t *testing.T) {
	for _, c := range []struct {
		version           string
		flagged           bool
		stable, beta, dev bool
	}{
		{"1.0.0", false, true, true, true},
		{"1.0.0", true, false, true, true},
		{"1.1.0-rc.1", true, false, true, true},
		{"1.1.0-beta.0", false, false, true, true},
		{"1.1.0-alpha.3", true, false, true, true},
		{"1.1.0-dev.4", true, false, false, true},
		{"1.1.0-dev", false, false, false, true},
		{"1.1.0-nightly.1", true, false, true, true},
		{"3.89.11.20260919", false, true, true, true}, // not semver: a release
	} {
		got := [3]bool{inChannel(c.version, c.flagged, ChannelStable), inChannel(c.version, c.flagged, ChannelBeta), inChannel(c.version, c.flagged, ChannelDev)}
		if got != [3]bool{c.stable, c.beta, c.dev} || !inChannel(c.version, c.flagged, ChannelAll) {
			t.Errorf("%s flagged=%t: stable/beta/dev %v", c.version, c.flagged, got)
		}
	}
	for c, want := range map[string]string{"": "stable", "stable": "stable", "beta": "beta", "dev": "dev", "pre": "stable", "all": "stable", "x": "stable"} {
		if NormalChannel(c) != want {
			t.Errorf("NormalChannel(%q) = %q", c, NormalChannel(c))
		}
	}
}
