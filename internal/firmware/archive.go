package firmware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/httpwait"
)

// The OpenCCU device-firmware archive (occulited task 49). eQ-3's update service serves only the
// newest bundle of a type, so an older one - the version a device ran before a bad update -
// comes from github.com/OpenCCU/HMDeviceFirmware, which keeps eQ-3's past update files (the files
// under eQ-3's HomeMatic Software License, which allows their use on any hardware; the
// repository's own tooling under Apache-2.0). occulited reads its file list with one call to
// GitHub's tree API and fetches one file from raw.githubusercontent.com, both only on the user's
// request and for one type at a time. Only HmIP device bundles are offered: the directories HmIP,
// HmIPW and ELV and names of the form <type>_update_V<version>_<date>.tgz - never BidCos (rfd
// goes no lower than the firmware the system ships), never an access point (their bundles are
// named without _update_). A fetched file goes through the same checks as a download from eQ-3
// (deploy: the info, TypeCode, Name, CCU3FirmwareVersionMin, a firmware file) and must be a bundle
// of the type its name promised.

const (
	// ArchiveTreeURL lists the archive's files (GitHub's git tree API, one call).
	ArchiveTreeURL = "https://api.github.com/repos/OpenCCU/HMDeviceFirmware/git/trees/master?recursive=1"
	// ArchiveRawBase is where a listed file is fetched from.
	ArchiveRawBase = "https://raw.githubusercontent.com/OpenCCU/HMDeviceFirmware/master/"
)

// ArchiveEntry is one bundle in the archive.
type ArchiveEntry struct {
	// Type is the device type as the file name spells it.
	Type    string `json:"type"`
	Version string `json:"version"`
	// Date is the build date the name carries (yyyy-mm-dd), "" when it carries none.
	Date string `json:"date,omitempty"`
	// Path is the file's path in the archive, the handle a fetch names.
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Archive lists and fetches the archive's bundles. The list is kept for TTL.
type Archive struct {
	TreeURL string
	RawBase string
	HTTP    *http.Client
	TTL     time.Duration
	Now     func() time.Time

	mu   sync.Mutex
	list []ArchiveEntry
	at   time.Time
}

// NewArchive returns an archive at GitHub with a one-hour list cache.
func NewArchive() *Archive {
	return &Archive{TreeURL: ArchiveTreeURL, RawBase: ArchiveRawBase, HTTP: &http.Client{Timeout: 2 * time.Minute}, TTL: time.Hour}
}

func (a *Archive) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// archiveNameRe is an HmIP device bundle's path in the archive.
var archiveNameRe = regexp.MustCompile(`^(?:HmIP|HmIPW|ELV)/([A-Za-z0-9][A-Za-z0-9.-]{0,63})_update_V(\d+(?:_\d+)+)\.tgz$`)

// ErrNotArchivable is the answer for a type the archive is not offered for: BidCos, an access
// point, a name that is no device type.
var ErrNotArchivable = errors.New("older firmware is offered for HmIP devices only, not for BidCos devices or access points")

// ErrNotInArchive is the answer for a path the archive's list does not name.
var ErrNotInArchive = errors.New("no such file in the firmware archive")

// Archivable says whether older firmware is offered for a device type: HmIP device types, not
// BidCos (HM-…) and not the access points (HmIP-HAP…, HmIPW-DRAP…).
func Archivable(deviceType string) bool {
	k := TypeKey(deviceType)
	if !deviceTypeRe.MatchString(deviceType) || strings.HasPrefix(k, "hm-") || strings.HasPrefix(k, "hmw-") {
		return false
	}
	for _, p := range []string{"hmip-hap", "hmipw-drap", "hmip-drap"} {
		if strings.HasPrefix(k, p) {
			return false
		}
	}
	return true
}

// List returns the archive's HmIP device bundles, from the cache while it is fresh.
func (a *Archive) List(ctx context.Context) ([]ArchiveEntry, error) {
	a.mu.Lock()
	if a.list != nil && a.now().Sub(a.at) < a.TTL {
		l := a.list
		a.mu.Unlock()
		return l, nil
	}
	a.mu.Unlock()
	bounded, explain, cancel := httpwait.Bound(ctx, 0, hostOf(a.TreeURL))
	defer cancel()
	req, err := http.NewRequestWithContext(bounded, http.MethodGet, a.TreeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := a.HTTP.Do(req)
	if err != nil {
		return nil, explain(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("firmware archive: HTTP %d", res.StatusCode)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&tree); err != nil {
		return nil, fmt.Errorf("firmware archive: %w", explain(err))
	}
	list := []ArchiveEntry{}
	for _, t := range tree.Tree {
		if t.Type != "blob" {
			continue
		}
		m := archiveNameRe.FindStringSubmatch(t.Path)
		if m == nil {
			continue
		}
		v, d, ok := parseUpdateName(strings.Split(m[2], "_"))
		if !ok {
			continue
		}
		list = append(list, ArchiveEntry{Type: m[1], Version: v, Date: d, Path: t.Path, Size: t.Size})
	}
	a.mu.Lock()
	a.list, a.at = list, a.now()
	a.mu.Unlock()
	return list, nil
}

// For returns the archive's bundles of one device type, newest version first (a version listed
// twice: the newer build first).
func (a *Archive) For(ctx context.Context, deviceType string) ([]ArchiveEntry, error) {
	if !Archivable(deviceType) {
		return nil, ErrNotArchivable
	}
	all, err := a.List(ctx)
	if err != nil {
		return nil, err
	}
	key := TypeKey(deviceType)
	out := []ArchiveEntry{}
	for _, e := range all {
		if TypeKey(e.Type) == key {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if c := CompareVersions(out[i].Version, out[j].Version); c != 0 {
			return c > 0
		}
		return out[i].Date > out[j].Date
	})
	return out, nil
}

// entry is the listed entry for a path; the list is read first when it is not cached.
func (a *Archive) entry(ctx context.Context, p string) (ArchiveEntry, error) {
	all, err := a.List(ctx)
	if err != nil {
		return ArchiveEntry{}, err
	}
	for _, e := range all {
		if e.Path == p {
			return e, nil
		}
	}
	return ArchiveEntry{}, ErrNotInArchive
}

// Download fetches a listed file into dir and returns its local path. Only a path of the list is
// fetched - never a URL or a path the caller made up.
func (a *Archive) Download(ctx context.Context, p, dir string) (string, ArchiveEntry, error) {
	e, err := a.entry(ctx, p)
	if err != nil {
		return "", e, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.RawBase+e.Path, nil)
	if err != nil {
		return "", e, err
	}
	res, err := httpwait.Do(a.HTTP, req, 0)
	if err != nil {
		return "", e, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", e, fmt.Errorf("firmware archive %s: HTTP %d", e.Path, res.StatusCode)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", e, err
	}
	final := filepath.Join(dir, path.Base(e.Path))
	tmp, err := os.CreateTemp(dir, ".fw-*.part")
	if err != nil {
		return "", e, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(res.Body, 16<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", e, err
	}
	if n < 1024 {
		return "", e, fmt.Errorf("firmware archive %s: %d bytes is not a firmware file", e.Path, n)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", e, err
	}
	return final, e, nil
}

// archive is the service's archive (NewArchive on first use).
func (s *Service) archive() *Archive {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Archive == nil {
		s.Archive = NewArchive()
	}
	return s.Archive
}

// ArchivedVersion is an archive entry with what the system holds of it.
type ArchivedVersion struct {
	ArchiveEntry
	// OnSystem: a bundle of this type and version lies in the firmware or the held directory
	OnSystem bool `json:"on_system"`
}

// ArchiveFor lists the archive's bundles of a device type, newest first, each marked when its
// version is on the system already (occulited task 49).
func (s *Service) ArchiveFor(ctx context.Context, deviceType string) ([]ArchivedVersion, error) {
	list, err := s.archive().For(ctx, deviceType)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	key := TypeKey(deviceType)
	for _, dir := range []string{s.Dir, s.heldDir()} {
		if bs, err := Deployed(dir); err == nil {
			for _, b := range bs {
				if TypeKey(b.Name) == key {
					have[versionOf(b)] = true
				}
			}
		}
	}
	out := make([]ArchivedVersion, 0, len(list))
	for _, e := range list {
		on := false
		for v := range have {
			if CompareVersions(v, e.Version) == 0 {
				on = true
			}
		}
		out = append(out, ArchivedVersion{ArchiveEntry: e, OnSystem: on})
	}
	return out, nil
}

// ErrWrongBundle is FetchArchived's answer for a file whose bundle is not the type its name says.
var ErrWrongBundle = errors.New("the archive's file holds another type's bundle")

// FetchArchived fetches one listed bundle of the archive into the held directory (occulited task
// 49): checked as a download from eQ-3 is (DeployFor's checks, the system's version), and refused
// unless its info names the type of the file's name. It is not delivered: that is SetTransfer.
func (s *Service) FetchArchived(ctx context.Context, p string) (*Bundle, error) {
	tmp, err := os.MkdirTemp("", "occulite-fw-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	local, e, err := s.archive().Download(ctx, p, tmp)
	if err != nil {
		return nil, err
	}
	if !Archivable(e.Type) {
		return nil, ErrNotArchivable
	}
	held := s.heldDir()
	if err := Priv.MkdirAll(held, 0o755); err != nil {
		return nil, err
	}
	want := TypeKey(e.Type)
	b, err := deploy(local, held, s.SystemVersion, func(_ string, b *Bundle) (string, error) {
		if TypeKey(b.Name) != want {
			return "", fmt.Errorf("%w: %s holds %s", ErrWrongBundle, e.Path, b.Name)
		}
		return heldName(b), nil
	})
	if err != nil {
		return nil, err
	}
	b.Held = true
	s.Log.Info("firmware: fetched from the archive", "type", b.Name, "version", b.Version, "file", e.Path, "dir", b.Dir)
	return b, nil
}
