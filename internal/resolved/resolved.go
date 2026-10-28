// Package resolved is the pre-resolved catalogue (occulited task 124): one JSON file that a GitHub
// Action builds every hour with an authenticated token - every catalogue entry's release list and
// star count, the manifest's tag and images checked, and openccu-lite's own release list for the
// system update check - and publishes on the catalogue's host (the catalog-resolved branch,
// raw.githubusercontent.com). A system then reads that one file per check instead of two
// unauthenticated api.github.com calls per entry, which shared public addresses ran into GitHub's
// 60 calls an hour with.
//
// The file only answers what the API would have answered: the release lists in GitHub's own shape,
// the star counts. Everything the system derives from them (the tag, the asset for its
// architecture, the channel's release) is derived by the same code as before, so the file and the
// API path cannot drift. A file that is missing, of another format, stale or without a repository
// leaves that repository to the API, as before.
package resolved

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Format is the file's format this binary reads and writes.
const Format = 1

// MaxAge is how old the file may be before a system leaves it for the API: the Action runs every
// hour, so six hours is five missed runs.
const MaxAge = 6 * time.Hour

// MaxSize bounds the file a system reads.
const MaxSize = 8 << 20

// FileName is the file's name on the catalog-resolved branch.
const FileName = "catalog-resolved.json"

// File is the pre-resolved catalogue.
type File struct {
	Format      int       `json:"format"`
	GeneratedAt time.Time `json:"generated_at"`
	// Catalog is the catalogue file's URL it was built from.
	Catalog string `json:"catalog"`
	// Repos are the release lists and star counts by "owner/repo": every entry's repository, every
	// manifest's release repository, and openccu-lite's (the system update feed).
	Repos map[string]Repo `json:"repos"`
	// Entries are what the build found per catalogue entry, for whoever reads the file: the tag
	// the manifest was read at, its images, and the error of an entry that failed (kept, not
	// dropped).
	Entries []Entry `json:"entries"`
}

// Repo is one repository as the API answered it.
type Repo struct {
	// Releases is GitHub's release list (…/releases?per_page=N) in GitHub's shape, the fields the
	// systems read; absent with Error.
	Releases []Release `json:"releases"`
	// PerPage is how many releases were asked for: 10 for an addon, 100 for the system feed.
	PerPage int `json:"per_page"`
	// Stars is the repository's star count, absent when it was not read.
	Stars *int `json:"stars,omitempty"`
	// Error is why the list could not be read; a system asks the API itself then.
	Error string `json:"error,omitempty"`
}

// Release is a GitHub release, the fields the catalogue and the system update read.
type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name,omitempty"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	HTMLURL     string  `json:"html_url"`
	PublishedAt string  `json:"published_at,omitempty"`
	Assets      []Asset `json:"assets"`
}

// Asset is a release's file.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Entry is one catalogue entry as the build found it.
type Entry struct {
	Git      string `json:"git"`
	Manifest string `json:"manifest"`
	ID       string `json:"id,omitempty"`
	Tag      string `json:"tag,omitempty"`
	// ReleaseRepo is the manifest's release.github, the repository its packages come from.
	ReleaseRepo string `json:"release_repo,omitempty"`
	// Images are the manifest's declared images: kind → "ok", or why it could not be fetched
	// (openccu-lite's icon-404 class of bug shows here before any system runs into it).
	Images map[string]string `json:"images,omitempty"`
	Error  string            `json:"error,omitempty"`
}

// Parse reads a file and checks it against now: the format, a generation time, and an age below
// MaxAge (a clock up to an hour behind the Action's is not a reason to refuse it).
func Parse(b []byte, now time.Time) (*File, error) {
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("the resolved catalogue is not JSON: %w", err)
	}
	if err := f.Valid(now); err != nil {
		return nil, err
	}
	return &f, nil
}

// Valid says whether a system may use the file at now.
func (f *File) Valid(now time.Time) error {
	switch {
	case f == nil:
		return errors.New("no resolved catalogue")
	case f.Format != Format:
		return fmt.Errorf("the resolved catalogue has format %d, this system reads %d", f.Format, Format)
	case f.GeneratedAt.IsZero():
		return errors.New("the resolved catalogue carries no generation time")
	case now.Sub(f.GeneratedAt) > MaxAge:
		return fmt.Errorf("the resolved catalogue is stale (generated %s)", f.GeneratedAt.UTC().Format(time.RFC3339))
	case f.GeneratedAt.Sub(now) > time.Hour:
		return fmt.Errorf("the resolved catalogue is from the future (generated %s)", f.GeneratedAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// Releases is a repository's release list from the file, ok false when the file does not hold it
// (absent, failed in the build, or asked with fewer releases than perPage needs).
func (f *File) Releases(repo string, perPage int) ([]Release, bool) {
	if f == nil {
		return nil, false
	}
	r, ok := f.Repos[repo]
	if !ok || r.Error != "" || r.Releases == nil || r.PerPage < perPage {
		return nil, false
	}
	return r.Releases, true
}

// Stars is a repository's star count from the file.
func (f *File) Stars(repo string) (int, bool) {
	if f == nil {
		return 0, false
	}
	r, ok := f.Repos[repo]
	if !ok || r.Stars == nil {
		return 0, false
	}
	return *r.Stars, true
}
