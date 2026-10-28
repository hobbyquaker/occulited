package resolved

import (
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body, err string
	}{
		{"fresh", `{"format":1,"generated_at":"2026-10-28T11:00:00Z","repos":{}}`, ""},
		{"at the limit", `{"format":1,"generated_at":"2026-10-28T06:00:00Z"}`, ""},
		{"stale", `{"format":1,"generated_at":"2026-10-28T05:59:00Z"}`, "stale"},
		{"a clock behind the Action's", `{"format":1,"generated_at":"2026-10-28T12:30:00Z"}`, ""},
		{"from the future", `{"format":1,"generated_at":"2026-10-28T13:30:00Z"}`, "future"},
		{"another format", `{"format":2,"generated_at":"2026-10-28T11:00:00Z"}`, "format 2"},
		{"no time", `{"format":1}`, "generation time"},
		{"not JSON", `<html>`, "not JSON"},
	} {
		_, err := Parse([]byte(tc.body), now)
		if (tc.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.err)
		}
	}
	var nilFile *File
	if nilFile.Valid(now) == nil {
		t.Error("a nil file is valid")
	}
}

func TestReleasesAndStars(t *testing.T) {
	n := 7
	f := &File{Repos: map[string]Repo{
		"o/a":    {PerPage: 10, Releases: []Release{{TagName: "v1"}}, Stars: &n},
		"o/none": {PerPage: 10, Releases: []Release{}},
		"o/fail": {PerPage: 10, Error: "HTTP 502"},
		"o/feed": {PerPage: 100, Releases: []Release{{TagName: "v2"}}},
	}}
	for _, tc := range []struct {
		repo    string
		perPage int
		ok      bool
		n       int
	}{
		{"o/a", 10, true, 1},
		{"o/a", 100, false, 0},  // a shorter list than asked for is not the answer
		{"o/none", 10, true, 0}, // no releases is an answer
		{"o/fail", 10, false, 0},
		{"o/missing", 10, false, 0},
		{"o/feed", 100, true, 1},
	} {
		rels, ok := f.Releases(tc.repo, tc.perPage)
		if ok != tc.ok || len(rels) != tc.n {
			t.Errorf("%s/%d: %v %v", tc.repo, tc.perPage, ok, rels)
		}
	}
	if s, ok := f.Stars("o/a"); !ok || s != 7 {
		t.Errorf("stars %d %v", s, ok)
	}
	if _, ok := f.Stars("o/feed"); ok {
		t.Error("stars without a count")
	}
	var nilFile *File
	if _, ok := nilFile.Releases("o/a", 1); ok {
		t.Error("nil file")
	}
	if _, ok := nilFile.Stars("o/a"); ok {
		t.Error("nil file")
	}
}
