package firmware

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// Bundle is what an eQ-3 firmware .tgz carries, from its `info` file.
type Bundle struct {
	TypeCode string `json:"type_code"` // the bundle's TypeCode, the RF type number
	// Dir is its directory under /etc/config/firmware: the TypeCode, or <TypeCode>-<Name> when
	// another type's bundle holds that TypeCode already (B-225: HmIP-WRC2 and HM-LC-Dim1T-DR are
	// both 261). rfd and hmipserver read every subdirectory by its info, whatever its name.
	Dir     string `json:"dir,omitempty"`
	Name    string `json:"name"` // the device type, e.g. HmIP-PDT
	Version string `json:"version"`
	// VersionFromName and DateFromName are what the update file's name carries (1.2.6 and
	// 2022-09-28 for …_update_V1_2_6_220928.efw), set only when Version is empty: eQ-3 writes
	// FirmwareVersion=0.0.0 into some bundles, and the page shows the name's version instead of a
	// dash, marked as taken from the file name.
	VersionFromName string            `json:"version_from_name,omitempty"`
	DateFromName    string            `json:"date_from_name,omitempty"`
	Files           []string          `json:"files"`
	Info            map[string]string `json:"info"`
	// Held: the bundle lies in the held directory, not offered to the interfaces (occulited
	// task 45)
	Held bool `json:"held,omitempty"`
}

var typeCodeRe = regexp.MustCompile(`^[0-9A-Za-z._-]{1,32}$`)

// Deploy unpacks a bundle into dir/<TypeCode>/ — the layout the WebUI produces and the interface
// processes read after refreshDeployedDeviceFirmwareList. Flat archive only (no directories, no
// traversal), atomic per bundle: unpacked into a temp dir first, then renamed into place over any
// previous bundle of that type code.
// Priv is the privilege boundary for the two directories this package writes: /etc/config/firmware
// belongs to root, and occulited has not been root since task 17 (B-20). main points it at the
// helper client exactly as it points system.Priv.
var Priv priv.Ops = priv.Local{}

// StagingDir is where a bundle is unpacked before the helper moves it into place; it has to be on
// the same filesystem as the firmware directory (both are under /usr/local on a box, /etc/config
// being a symlink into it). Empty = unpack next to the destination, which is what a root
// occulited and the tests do.
var StagingDir string

func Deploy(archive string, dir string) (*Bundle, error) {
	return deploy(archive, dir, "", nil)
}

// ErrNeedsNewerSystem is DeployFor's refusal: the bundle's CCU3FirmwareVersionMin is above the
// system's VERSION.
var ErrNeedsNewerSystem = errors.New("the bundle needs a newer system")

// DeployFor is Deploy for the fetcher (B-195): a bundle whose info asks for a CCU firmware
// (CCU3FirmwareVersionMin) above systemVersion is refused before anything is written, the second
// guard behind the index query that already names the version. A manual upload goes through
// Deploy and may override it, as it may today. Empty systemVersion = no check.
func DeployFor(archive, dir, systemVersion string) (*Bundle, error) {
	return deploy(archive, dir, systemVersion, nil)
}

// deployHeld is DeployFor into the held directory (occulited task 48): the bundle's directory
// there is heldName's, one per type and version.
func deployHeld(archive, dir, systemVersion string) (*Bundle, error) {
	return deploy(archive, dir, systemVersion, func(_ string, b *Bundle) (string, error) { return heldName(b), nil })
}

// deploy unpacks and checks a bundle and moves it into dir under the name dirName picks (nil =
// bundleDir).
func deploy(archive, dir, systemVersion string, dirName func(string, *Bundle) (string, error)) (*Bundle, error) {
	if dirName == nil {
		dirName = bundleDir
	}
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	tmpBase := dir
	if StagingDir != "" {
		if err := os.MkdirAll(StagingDir, 0o700); err != nil {
			return nil, err
		}
		tmpBase = StagingDir
	}
	tmp, err := os.MkdirTemp(tmpBase, ".deploy-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	tr := tar.NewReader(gz)
	b := &Bundle{Info: map[string]string{}}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		name := filepath.Base(filepath.Clean(h.Name))
		if h.Typeflag == tar.TypeDir || name == "." || name == "/" || strings.HasPrefix(name, "..") {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		total += h.Size
		if total > 64<<20 {
			return nil, errors.New("archive too large")
		}
		out, err := os.OpenFile(filepath.Join(tmp, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o664)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(out, io.LimitReader(tr, h.Size)); err != nil {
			out.Close()
			return nil, err
		}
		out.Close()
		// readable for the interface processes whatever the daemon's umask: occulited runs with
		// 0077, and hmipserver, which is not root since task 67, could not read a 0600 bundle and
		// kept AVAILABLE_FIRMWARE at 0.0.0 (B-195's lab check)
		if err := os.Chmod(filepath.Join(tmp, name), 0o644); err != nil {
			return nil, err
		}
		b.Files = append(b.Files, name)
	}
	infoPath := filepath.Join(tmp, "info")
	inf, err := os.Open(infoPath)
	if err != nil {
		return nil, errors.New("bundle has no info file")
	}
	sc := bufio.NewScanner(inf)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok {
			b.Info[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	inf.Close()
	b.TypeCode, b.Name, b.Version = b.Info["TypeCode"], b.Info["Name"], BundleVersion(b.Info["FirmwareVersion"])
	b.fillVersionFromName()
	if !typeCodeRe.MatchString(b.TypeCode) || b.Name == "" {
		return nil, fmt.Errorf("bundle info incomplete: TypeCode=%q Name=%q", b.TypeCode, b.Name)
	}
	if min := b.Info["CCU3FirmwareVersionMin"]; systemVersion != "" && min != "" && CompareVersions(min, systemVersion) > 0 {
		return nil, fmt.Errorf("%w: %s %s needs CCU firmware %s or newer, this system is %s", ErrNeedsNewerSystem, b.Name, b.Version, min, systemVersion)
	}
	hasFW := false
	for _, n := range b.Files {
		switch low := strings.ToLower(n); {
		case strings.HasSuffix(low, ".efw"), strings.HasSuffix(low, ".eq3"):
			hasFW = true
		case strings.HasSuffix(low, ".zip"):
			// B-204: access point bundles (HmIP-HAP, -HAP-JS1, -HAP2, -HAP2-A, HmIPW-DRAP) carry
			// their firmware as one .zip, which the interface process reads as it is; a stock CCU
			// deploys it the same way. It is copied as it is, never unpacked - checked first
			if err := accessPointZip(filepath.Join(tmp, n)); err != nil {
				return nil, fmt.Errorf("bundle has an unreadable access point firmware (%s): %w", n, err)
			}
			hasFW = true
		}
	}
	if !hasFW {
		return nil, errors.New("bundle has no firmware file (.efw/.eq3/.zip)")
	}
	// everything from here writes into the firmware directory, which is root's: through Priv
	if err := Priv.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b.Dir, err = dirName(dir, b)
	if err != nil {
		return nil, err
	}
	final := filepath.Join(dir, b.Dir)
	old := final + ".old"
	_ = Priv.RemoveAll(old)
	if _, err := os.Stat(final); err == nil {
		if err := Priv.Rename(final, old); err != nil {
			return nil, err
		}
	}
	if err := Priv.Rename(tmp, final); err != nil {
		_ = Priv.Rename(old, final)
		return nil, err
	}
	_ = Priv.RemoveAll(old)
	// the bundle came out of an archive as the daemon's user; the firmware directory is root's
	// and eq3configd reads it as root
	_ = Priv.Chown(final, 0, 0, true)
	if err := Priv.Chmod(final, 0o775); err != nil {
		return nil, err
	}
	return b, nil
}

// bundleDir picks the bundle's directory (B-225): where a bundle of the same type lies already -
// <TypeCode> or <TypeCode>-<Name> - so a newer one replaces it; otherwise <TypeCode> when that is
// free, and <TypeCode>-<Name> when another type's bundle holds the TypeCode. Two types never
// overwrite each other there any more (the daily fetch replaced one with the other on a system
// with both, and each lost its offer every other day). Checked on the lab: hmipserver keeps the
// HmIP-WRC2's AVAILABLE_FIRMWARE from its own bundle with HM-LC-Dim1T-DR's beside it, in either
// directory.
func bundleDir(dir string, b *Bundle) (string, error) {
	own := b.TypeCode + "-" + dirSafe(b.Name)
	if !typeCodeRe.MatchString(own) {
		own = ""
	}
	// the Name in a bundle directory's info; "" for a free one or one without a readable info
	// (taken over, as before)
	nameAt := func(sub string) string {
		f, err := os.Open(filepath.Join(dir, sub, "info"))
		if err != nil {
			return ""
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && strings.TrimSpace(k) == "Name" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	// the same type's bundle already there, under either name (and however the type is spelled)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() && strings.HasPrefix(n, b.TypeCode+"-") && TypeKey(nameAt(n)) == TypeKey(b.Name) {
				return n, nil
			}
		}
	}
	held := nameAt(b.TypeCode)
	if held == "" || TypeKey(held) == TypeKey(b.Name) {
		return b.TypeCode, nil
	}
	if own == "" {
		return "", fmt.Errorf("TypeCode %s is held by %s's bundle, and %q cannot name a directory of its own", b.TypeCode, held, b.Name)
	}
	return own, nil
}

// dirSafe keeps the characters a bundle directory may have (typeCodeRe).
func dirSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}

// accessPointZip checks an access point's firmware pack: a zip archive with at least one .eq3.
func accessPointZip(path string) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".eq3") {
			return nil
		}
	}
	return errors.New("no .eq3 in it")
}

// Deployed lists the bundles present under dir.
func Deployed(dir string) ([]Bundle, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Bundle{}, nil
		}
		return nil, err
	}
	out := []Bundle{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b := Bundle{TypeCode: e.Name(), Dir: e.Name(), Info: map[string]string{}}
		if f, err := os.Open(filepath.Join(dir, e.Name(), "info")); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
					b.Info[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			f.Close()
		}
		b.Name, b.Version = b.Info["Name"], BundleVersion(b.Info["FirmwareVersion"])
		if tc := b.Info["TypeCode"]; typeCodeRe.MatchString(tc) {
			b.TypeCode = tc // a <TypeCode>-<Name> directory's own TypeCode (B-225)
		}
		files, _ := os.ReadDir(filepath.Join(dir, e.Name()))
		for _, fl := range files {
			b.Files = append(b.Files, fl.Name())
		}
		b.fillVersionFromName()
		out = append(out, b)
	}
	return out, nil
}
