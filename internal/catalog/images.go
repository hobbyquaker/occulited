package catalog

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hobbyquaker/occulited/internal/addonimage"
	"github.com/hobbyquaker/occulited/internal/manifest"
)

// The catalogue's addon images (openccu-lite task 100). An entry's manifest declares its icon and
// logo as paths relative to the package root; for the Addons page before an install the check
// fetches them from the addon's repository at the tag the manifest was read at (or the default
// branch, where the manifest came from there), beside the manifest - its directory in the
// repository is the package root there (addon_files/openccu-lite.json declaring
// mosquitto/www/icon.svg: addon_files/mosquitto/www/icon.svg) - into ImagesDir under their content
// hash, with the same rules as the installed ones: at most addonimage.MaxSize bytes, and the type by
// content. A file that is not an image is not kept. Nothing is fetched outside the user's check
// (D-90, B-240): the page and the image routes answer from the files. An adapter manifest (one the
// catalogue carries for a third-party addon) declares images of a package whose repository does not
// carry them beside the manifest, so no image is fetched for it.
//
// occulited task 128: the manifest's directory is not always the package root. ccu-addon-mui
// declares dist/icon.svg, which its build puts into the package while the repository has
// public/icon.svg. An image the repository answers 404 for is read from the package itself: the
// release asset this system would install (Resolve), streamed through gzip and tar until the
// declared files are found, at most packageImageLimit bytes of download, each file at most
// addonimage.MaxSize - nothing asked of addon authors. Only in the check, and once per asset: the
// asset read is remembered (cached.PackageRead), so an image the package lacks too is not
// downloaded for again until a new release.

var hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// fetchImages is the entry's images after this check: the ones the manifest declares, kept where
// the file of the previous check is still right (same path, same tag, still on disk), fetched where
// not. c is this check's result so far (its Manifest and Tag), prev the previous check's entry.
func (s *Service) fetchImages(ctx context.Context, e Entry, c cached, prev cached) (images map[string]string, packageRead string) {
	if s.ImagesDir == "" || c.Manifest == nil || e.Adapter() {
		return nil, ""
	}
	if prev.Tag == c.Tag {
		packageRead = prev.PackageRead
	}
	var out map[string]string
	// the package's files, read at the first image the repository does not have (task 128)
	var pkg map[string][]byte
	pkgRead := false
	fromPackage := func(p string) ([]byte, bool) {
		if !pkgRead {
			pkgRead = true
			var asset string
			pkg, asset = s.packageImages(ctx, e, c.Manifest, packageRead)
			if asset != "" {
				packageRead = asset
			}
		}
		b, ok := pkg[path.Clean(p)]
		return b, ok
	}
	keep := func(kind, hash string) {
		if out == nil {
			out = map[string]string{}
		}
		out[kind] = hash
	}
	for _, kind := range addonimage.Kinds {
		p := addonimage.Declared(c.Manifest.UI, kind)
		if p == "" {
			continue
		}
		if h, ok := prev.Images[kind]; ok && prev.Manifest != nil && prev.Tag == c.Tag && addonimage.Declared(prev.Manifest.UI, kind) == p && s.imageOnDisk(h) {
			keep(kind, h)
			continue
		}
		if ctx.Err() != nil {
			return out, packageRead
		}
		var b []byte
		var err error
		for _, u := range rawURLs(s.RawGitHub, e.Git, path.Join(path.Dir(e.Manifest), p), c.Tag) {
			b, _, err = s.getETag(ctx, u, "", addonimage.MaxSize)
			if err == nil {
				break
			}
		}
		if err != nil && isNotFound(err) {
			if pb, ok := fromPackage(p); ok {
				b, err = pb, nil
			}
		}
		if err != nil {
			slog.Warn("catalog: an addon image could not be fetched", "git", e.Git, "kind", kind, "path", p, "err", err)
			continue
		}
		if _, ok := addonimage.Sniff(b); !ok {
			slog.Warn("catalog: an addon image is not an image and was not kept", "git", e.Git, "kind", kind, "path", p)
			continue
		}
		h, err := s.storeImage(b)
		if err != nil {
			slog.Warn("catalog: an addon image could not be stored", "git", e.Git, "kind", kind, "err", err)
			continue
		}
		keep(kind, h)
	}
	return out, packageRead
}

// imageOnDisk says whether the file of a hash is in ImagesDir.
func (s *Service) imageOnDisk(hash string) bool {
	if !hashRe.MatchString(hash) {
		return false
	}
	st, err := os.Stat(filepath.Join(s.ImagesDir, hash))
	return err == nil && st.Mode().IsRegular()
}

// storeImage writes an image under its content hash and answers the hash.
func (s *Service) storeImage(b []byte) (string, error) {
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(s.ImagesDir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(s.ImagesDir, h)
	if s.imageOnDisk(h) {
		return h, nil
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return h, nil
}

// pruneImagesLocked removes the files of ImagesDir no cached entry names any more. s.mu held.
func (s *Service) pruneImagesLocked() {
	if s.ImagesDir == "" {
		return
	}
	used := map[string]bool{}
	for _, c := range s.cache.Entries {
		for _, h := range c.Images {
			used[h] = true
		}
	}
	entries, err := os.ReadDir(s.ImagesDir)
	if err != nil {
		return
	}
	for _, en := range entries {
		if name := en.Name(); !used[name] && (hashRe.MatchString(name) || filepath.Ext(name) == ".tmp") {
			_ = os.Remove(filepath.Join(s.ImagesDir, name))
		}
	}
}

// Image is a catalogue entry's image of a kind, as the check fetched it: the bytes, which the
// caller types by content (addonimage.Sniff). fs.ErrNotExist when the catalogue knows no such
// addon, the manifest declares no such kind, or the check could not take it.
func (s *Service) Image(id, kind string) ([]byte, error) {
	s.mu.Lock()
	s.loadLocked()
	var hash string
	for _, c := range s.cache.Entries {
		if c.Manifest != nil && c.Manifest.ID == id {
			hash = c.Images[kind]
			break
		}
	}
	dir := s.ImagesDir
	s.mu.Unlock()
	if hash == "" || dir == "" || !hashRe.MatchString(hash) {
		return nil, fmt.Errorf("%s %s: %w", id, kind, fs.ErrNotExist)
	}
	b, err := os.ReadFile(filepath.Join(dir, hash))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s %s: %w", id, kind, fs.ErrNotExist)
		}
		return nil, err
	}
	if len(b) > addonimage.MaxSize {
		return nil, fmt.Errorf("%s %s: larger than %d bytes", id, kind, addonimage.MaxSize)
	}
	return b, nil
}

// packageImageLimit bounds the download of a package read for its images (occulited task 128): the
// lite packages are a few megabytes (ccu-addon-mui's about 5 MB); a larger one is not read.
const packageImageLimit = 64 << 20

// packageImages reads the images e's manifest declares from the release asset this system would
// install - path (cleaned, relative to the package root) → bytes, only regular files of at most
// addonimage.MaxSize - and the asset it resolved. It reads nothing when the asset is the one read
// before (read) or larger than packageImageLimit, and answers nothing when the manifest has no
// release for this architecture or the download fails.
func (s *Service) packageImages(ctx context.Context, e Entry, m *manifest.Manifest, read string) (map[string][]byte, string) {
	// no architecture (a service without one, e.g. cmd/catalog-resolve --arch ''): no package to read
	if m == nil || m.Release == nil || s.Arch == "" || !m.SupportsArch(s.Arch) {
		return nil, ""
	}
	want := map[string]bool{}
	for _, kind := range addonimage.Kinds {
		if p := addonimage.Declared(m.UI, kind); p != "" {
			want[path.Clean(p)] = true
		}
	}
	r, err := s.Resolve(ctx, m.Release)
	if err != nil {
		slog.Warn("catalog: the package for an addon image could not be resolved", "git", e.Git, "err", err)
		return nil, ""
	}
	if r.Asset == read {
		return nil, r.Asset // read for this release before: what it had is kept, what it lacked it lacks
	}
	if r.Size > packageImageLimit {
		slog.Warn("catalog: the package is too large to read an addon image from", "git", e.Git, "asset", r.Asset, "size", r.Size)
		return nil, r.Asset
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, ""
	}
	res, err := s.do(req)
	if err != nil {
		slog.Warn("catalog: the package for an addon image could not be downloaded", "git", e.Git, "asset", r.Asset, "err", err)
		return nil, ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		slog.Warn("catalog: the package for an addon image could not be downloaded", "git", e.Git, "asset", r.Asset, "err", statusError("download", res))
		return nil, ""
	}
	out, err := imagesInPackage(io.LimitReader(res.Body, packageImageLimit), want)
	if err != nil {
		slog.Warn("catalog: the package for an addon image could not be read", "git", e.Git, "asset", r.Asset, "err", err)
	}
	slog.Info("catalog: addon images read from the package", "git", e.Git, "asset", r.Asset, "found", len(out), "declared", len(want))
	return out, r.Asset
}

// imagesInPackage reads a .tar.gz up to the last of the wanted files (paths relative to its root;
// "./" prefixes are the same paths) and answers the ones it found. What it found before an error is
// kept.
func imagesInPackage(r io.Reader, want map[string]bool) (map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for len(out) < len(want) {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if !want[name] || h.Typeflag != tar.TypeReg || h.Size > addonimage.MaxSize {
			continue
		}
		if _, dup := out[name]; dup {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, addonimage.MaxSize+1))
		if err != nil {
			return out, err
		}
		if len(b) <= addonimage.MaxSize {
			out[name] = b
		}
	}
	return out, nil
}
