package catalog

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonimage"
	"github.com/hobbyquaker/occulited/internal/resolved"
)

// loadResolved reads the pre-resolved catalogue once per check (occulited task 124): one request
// to the catalogue's own host, conditional on the last answer's ETag. A file that cannot be read
// or is not valid leaves every repository to the API; a 304 keeps the file of the last answer,
// which is still checked for its age.
func (s *Service) loadResolved(ctx context.Context) {
	if s.ResolvedURL == "" {
		return
	}
	s.mu.Lock()
	etag := s.resolvedETag
	if s.resolvedFile == nil {
		etag = ""
	}
	s.mu.Unlock()
	var b []byte
	var newETag string
	var err error
	if p, ok := strings.CutPrefix(s.ResolvedURL, "file://"); ok {
		// a copy on the system (a test, a lab, a system without the internet's file)
		b, err = os.ReadFile(p)
		if err == nil && len(b) > resolved.MaxSize {
			err = errors.New("the resolved catalogue is too large")
		}
	} else {
		b, newETag, err = s.getETag(ctx, s.ResolvedURL, etag, resolved.MaxSize)
	}
	if errors.Is(err, errNotModified) {
		return
	}
	if err != nil {
		slog.Info("catalog: the resolved catalogue could not be read; the GitHub API answers instead", "url", s.ResolvedURL, "err", err)
		s.mu.Lock()
		s.resolvedFile, s.resolvedETag = nil, ""
		s.mu.Unlock()
		return
	}
	f, err := resolved.Parse(b, s.clock())
	if err != nil {
		slog.Info("catalog: the resolved catalogue is not used; the GitHub API answers instead", "url", s.ResolvedURL, "err", err)
		f, newETag = nil, ""
	}
	s.mu.Lock()
	s.resolvedFile, s.resolvedETag = f, newETag
	s.mu.Unlock()
}

// resolvedReleases is a repository's release list from the pre-resolved catalogue while it is valid.
func (s *Service) resolvedReleases(repo string, perPage int) ([]ghRelease, bool) {
	s.mu.Lock()
	f := s.resolvedFile
	s.mu.Unlock()
	if f == nil || f.Valid(s.clock()) != nil {
		return nil, false
	}
	return f.Releases(repo, perPage)
}

// resolvedStars is a repository's star count from the pre-resolved catalogue while it is valid.
func (s *Service) resolvedStars(repo string) (int, bool) {
	s.mu.Lock()
	f := s.resolvedFile
	s.mu.Unlock()
	if f == nil || f.Valid(s.clock()) != nil {
		return 0, false
	}
	return f.Stars(repo)
}

// ResolvedInUse says whether the last check's pre-resolved catalogue is still in use (for the log
// and the tests).
func (s *Service) ResolvedInUse() bool {
	s.mu.Lock()
	f := s.resolvedFile
	s.mu.Unlock()
	return f != nil && f.Valid(s.clock()) == nil
}

// BuildResolved is the GitHub Action's half of task 124: it runs a check as a system does - the
// catalogue file, every entry's latest tag and manifest at it, the images, the release lists, the
// star counts - with s.Token against the API, and writes down what the API answered. feedRepos are
// the repositories whose release lists the system update check reads (openccu-lite), asked with
// feedPerPage releases. The service must have no ResolvedURL: the file is built from the API, never
// from an older file. An entry or a repository that fails is kept with its error.
func BuildResolved(ctx context.Context, s *Service, feedRepos []string, feedPerPage int) (*resolved.File, error) {
	if s.ResolvedURL != "" {
		return nil, errors.New("the resolver reads the API, not a resolved catalogue")
	}
	if s.ImagesDir == "" {
		dir, err := os.MkdirTemp("", "catalog-resolve-images")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		s.ImagesDir = dir
		defer func() { s.ImagesDir = "" }()
	}
	if _, err := s.loadFiles(ctx, true); err != nil {
		return nil, err
	}
	s.mu.Lock()
	cat, sources := s.catalog, s.sources
	s.mu.Unlock()
	out := &resolved.File{Format: resolved.Format, Repos: map[string]resolved.Repo{}, Entries: []resolved.Entry{}}
	if len(s.URLs) > 0 {
		out.Catalog = s.URLs[0]
	}
	repos := map[string]int{} // repo → the list's length
	for _, e := range cat.Addons {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c := s.fetchManifest(ctx, e, sources[entryKey(e.Git)])
		s.mu.Lock()
		s.cache.Entries[entryKey(e.Git)] = c
		s.mu.Unlock()
		re := resolved.Entry{Git: e.Git, Manifest: e.Manifest, Tag: c.Tag, Error: c.Error}
		if !e.Adapter() {
			if r := RepoName(e.Git); repoRe.MatchString(r) && isGitHub(e.Git) {
				repos[r] = addonReleases
			}
		}
		if m := c.Manifest; m != nil {
			re.ID = m.ID
			if m.Release != nil && repoRe.MatchString(m.Release.GitHub) {
				re.ReleaseRepo = m.Release.GitHub
				repos[m.Release.GitHub] = addonReleases
			}
			if !e.Adapter() {
				for _, kind := range addonimage.Kinds {
					if addonimage.Declared(m.UI, kind) == "" {
						continue
					}
					if re.Images == nil {
						re.Images = map[string]string{}
					}
					if _, ok := c.Images[kind]; ok {
						re.Images[kind] = "ok"
					} else {
						re.Images[kind] = "not fetched: missing at the tag, too large or not an image"
					}
				}
			}
		}
		out.Entries = append(out.Entries, re)
	}
	for _, r := range feedRepos {
		repos[r] = max(repos[r], feedPerPage)
	}
	for _, repo := range slices.Sorted(maps.Keys(repos)) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n := repos[repo]
		r := resolved.Repo{PerPage: n}
		rels, err := s.releasesPage(ctx, repo, n)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Releases = rels
			if r.Releases == nil {
				r.Releases = []resolved.Release{}
			}
		}
		s.refreshStars(ctx, repo)
		s.mu.Lock()
		if v, ok := s.cache.Stars[repo]; ok {
			r.Stars = &v
		}
		s.mu.Unlock()
		out.Repos[repo] = r
	}
	out.GeneratedAt = time.Now().UTC().Truncate(time.Second)
	return out, nil
}
