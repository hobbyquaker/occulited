// catalog-resolve builds the pre-resolved catalogue (occulited task 124): what a system's check asks
// api.github.com per catalogue entry - the release lists, the star counts - and openccu-lite's own
// release list for the system update check, asked once with an authenticated token and written to
// one file. The GitHub Action catalog-resolve.yml runs it every hour and publishes the file on the
// catalog-resolved branch. It runs internal/catalog's own check, so the file and a system's fallback
// to the API cannot drift.
//
//	GITHUB_TOKEN=… catalog-resolve --catalog file://$PWD/catalog/catalog.json --out catalog-resolved.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/catalog"
	"github.com/hobbyquaker/occulited/internal/config"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "catalog-resolve:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("catalog-resolve", flag.ContinueOnError)
	cat := fs.String("catalog", config.DefaultCatalogURL, "the catalogue file (https:// or file://)")
	out := fs.String("out", "catalog-resolved.json", "where the file is written")
	feed := fs.String("feed-repo", "hobbyquaker/openccu-lite", "the system update feed's repository, comma-separated for several")
	perPage := fs.Int("feed-per-page", 100, "how many of the feed's releases are listed")
	api := fs.String("api", "https://api.github.com", "the GitHub API (tests)")
	raw := fs.String("raw", "https://raw.githubusercontent.com", "GitHub's raw host (tests)")
	timeout := fs.Duration("timeout", 10*time.Minute, "the whole run's limit")
	// occulited task 130: a system reads an image its repository lacks from the package (task 128);
	// the resolver checks as a system of the common architecture, so its images say what systems get
	arch := fs.String("arch", "aarch64", "the architecture whose package an image the repository lacks is read from")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	s := catalog.New([]string{*cat}, *arch, nil)
	s.GitHubAPI, s.RawGitHub, s.Token = *api, *raw, os.Getenv("GITHUB_TOKEN")
	var repos []string
	for _, r := range strings.Split(*feed, ",") {
		if r = strings.TrimSpace(r); r != "" {
			repos = append(repos, r)
		}
	}
	f, err := catalog.BuildResolved(ctx, s, repos, *perPage)
	if err != nil {
		return err
	}
	if len(f.Entries) == 0 {
		return fmt.Errorf("the catalogue %s has no entries", *cat)
	}
	failed := 0
	for _, e := range f.Entries {
		if e.Error != "" {
			failed++
			fmt.Fprintf(os.Stderr, "entry %s: %s\n", e.Git, e.Error)
		}
		for kind, st := range e.Images {
			if st != "ok" {
				fmt.Fprintf(os.Stderr, "entry %s: image %s %s\n", e.Git, kind, st)
			}
		}
	}
	for name, r := range f.Repos {
		if r.Error != "" {
			failed++
			fmt.Fprintf(os.Stderr, "repo %s: %s\n", name, r.Error)
		}
	}
	// every repository failing is a broken run (no token, no network), not a file to publish
	if failed > 0 && failed >= len(f.Entries)+len(f.Repos) {
		return fmt.Errorf("nothing could be resolved")
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := *out + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		return err
	}
	fmt.Printf("catalog-resolve: %d entries, %d repositories, %d failed, %s\n", len(f.Entries), len(f.Repos), failed, *out)
	return nil
}
