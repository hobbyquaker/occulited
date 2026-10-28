package sysupdate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/resolved"
)

// occulited task 124: the update check reads the feed repository's release list from the
// pre-resolved catalogue first - per channel the same picks as from the API - and asks the API
// only when the file is missing, stale, of another format or without the list.
func TestCheckFromResolved(t *testing.T) {
	r := fakeRoot(t, "VERSION=3.89.11\nPRODUCT=ova\nPLATFORM=ova\nVARIANT=lite\nLITE=1.0.0\n")
	asset := func(v string) []resolved.Asset {
		return []resolved.Asset{{Name: "openccu-lite-x86_64-ova-" + v + ".zip", Size: 1, URL: "https://example.invalid/" + v}}
	}
	file := resolved.File{Format: resolved.Format, GeneratedAt: time.Now().UTC(), Repos: map[string]resolved.Repo{
		"o/lite": {PerPage: 100, Releases: []resolved.Release{
			{TagName: "v1.1.0-beta.1", Prerelease: true, HTMLURL: "https://example.invalid/b", Assets: asset("1.1.0-beta.1")},
			{TagName: "v1.0.2", HTMLURL: "https://example.invalid/r", Assets: asset("1.0.2")},
		}},
	}}
	var api, res atomic.Int32
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		switch q.URL.Path {
		case "/resolved.json":
			res.Add(1)
			b, _ := body.Load().([]byte)
			if b == nil {
				http.NotFound(w, q)
				return
			}
			_, _ = w.Write(b)
		case "/repos/o/lite/releases":
			api.Add(1)
			fmt.Fprint(w, `[{"tag_name":"v1.0.1","assets":[{"name":"openccu-lite-x86_64-ova-1.0.1.zip","browser_download_url":"x","size":1}]}]`)
		default:
			http.NotFound(w, q)
		}
	}))
	t.Cleanup(srv.Close)
	serve := func(f *resolved.File) {
		if f == nil {
			body.Store([]byte(nil))
			return
		}
		b, _ := json.Marshal(f)
		body.Store(b)
	}
	newService := func() *Service {
		s := New(r, srv.URL+"/repos/o/lite/releases?per_page=100", true, nil)
		s.ResolvedURL = srv.URL + "/resolved.json"
		return s
	}
	for _, tc := range []struct {
		name    string
		file    func() *resolved.File
		channel string
		want    string
		viaAPI  bool
	}{
		{"stable from the file", func() *resolved.File { return &file }, ChannelStable, "1.0.2", false},
		{"beta from the file", func() *resolved.File { return &file }, ChannelBeta, "1.1.0-beta.1", false},
		{"stale: the API", func() *resolved.File { f := file; f.GeneratedAt = time.Now().Add(-7 * time.Hour); return &f }, ChannelStable, "1.0.1", true},
		{"missing: the API", func() *resolved.File { return nil }, ChannelStable, "1.0.1", true},
		{"without the repository: the API", func() *resolved.File { f := file; f.Repos = nil; return &f }, ChannelStable, "1.0.1", true},
		{"a shorter list than the feed's: the API", func() *resolved.File {
			f := file
			f.Repos = map[string]resolved.Repo{"o/lite": {PerPage: 10, Releases: file.Repos["o/lite"].Releases}}
			return &f
		}, ChannelStable, "1.0.1", true},
	} {
		serve(tc.file())
		api.Store(0)
		s := newService()
		s.Channel = tc.channel
		if err := s.Check(t.Context()); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if av := s.State().Available; av == nil || av.Version != tc.want {
			t.Errorf("%s: %+v", tc.name, av)
		}
		if (api.Load() > 0) != tc.viaAPI {
			t.Errorf("%s: %d API calls", tc.name, api.Load())
		}
		// the release list of the Updates page and `occulited update` the same way
		list, err := s.Releases(t.Context(), "")
		if err != nil || len(list.Releases) == 0 || list.Releases[0].Version != tc.want {
			t.Errorf("%s: releases %+v %v", tc.name, list, err)
		}
	}
	// a file on the system (file://) stands in the same way; none configured asks the API alone
	p := filepath.Join(t.TempDir(), "resolved.json")
	b, _ := json.Marshal(file)
	_ = os.WriteFile(p, b, 0o644)
	s := newService()
	s.ResolvedURL = "file://" + p
	api.Store(0)
	if err := s.Check(t.Context()); err != nil || s.State().Available.Version != "1.0.2" || api.Load() != 0 {
		t.Errorf("file://: %v %+v %d", err, s.State().Available, api.Load())
	}
	s.ResolvedURL = ""
	res.Store(0)
	if err := s.Check(t.Context()); err != nil || s.State().Available.Version != "1.0.1" || res.Load() != 0 {
		t.Errorf("none: %v %+v %d", err, s.State().Available, res.Load())
	}
}
