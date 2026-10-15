package firmware

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// placeholderVersionRe is a version made only of zeros: eQ-3 writes FirmwareVersion=0.0.0 into
// some bundles, whose real version is then only in the update file's name.
var placeholderVersionRe = regexp.MustCompile(`^0+(\.0+)*$`)

// BundleVersion is the version a bundle's info file states, or "" when it states none worth
// showing - empty, or eQ-3's all-zero placeholder. The info map keeps the raw value; every
// client reads an empty version as "unknown" and needs no knowledge of the placeholder.
func BundleVersion(raw string) string {
	v := strings.TrimSpace(raw)
	if placeholderVersionRe.MatchString(v) {
		return ""
	}
	return v
}

// updateNameRe finds the version eQ-3 writes into an update file's name when the info file does
// not state one: HmIPW-DRS8_update_V1_2_6_220928.efw, HmIPW-DRAP_update_V3_0_36_2024_12_18.zip.
var updateNameRe = regexp.MustCompile(`(?i)_update_v(\d+(?:_\d+)+)\.[a-z0-9]+$`)

// VersionFromName is the version and the build date the first update file among files carries in
// its name - "1.2.6" and "2022-09-28" for HmIPW-DRS8_update_V1_2_6_220928.efw. The date is the
// last group, written yymmdd, yyyymmdd or yyyy_mm_dd; it is "" when the name has none, and both
// are "" when no name follows the pattern. A version needs two parts at least, each of up to
// three digits (leading zeros dropped), so a date is never read as one.
func VersionFromName(files []string) (version, date string) {
	for _, f := range files {
		m := updateNameRe.FindStringSubmatch(f)
		if m == nil {
			continue
		}
		if v, d, ok := parseUpdateName(strings.Split(m[1], "_")); ok {
			return v, d
		}
	}
	return "", ""
}

func parseUpdateName(parts []string) (version, date string, ok bool) {
	n := len(parts)
	day := func(layout, s string) string {
		t, err := time.Parse(layout, s)
		if err != nil {
			return ""
		}
		return t.Format("2006-01-02")
	}
	switch {
	case n >= 5 && len(parts[n-3]) == 4 && len(parts[n-2]) == 2 && len(parts[n-1]) == 2:
		if date = day("2006_01_02", strings.Join(parts[n-3:], "_")); date != "" {
			parts = parts[:n-3]
		}
	case n >= 3 && len(parts[n-1]) == 8:
		if date = day("20060102", parts[n-1]); date != "" {
			parts = parts[:n-1]
		}
	case n >= 3 && len(parts[n-1]) == 6:
		if date = day("060102", parts[n-1]); date != "" {
			parts = parts[:n-1]
		}
	}
	if len(parts) < 2 {
		return "", "", false
	}
	nums := make([]string, len(parts))
	for i, p := range parts {
		if len(p) > 3 {
			return "", "", false
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return "", "", false
		}
		nums[i] = strconv.Itoa(v)
	}
	return strings.Join(nums, "."), date, true
}

// fillVersionFromName sets VersionFromName and DateFromName for a bundle whose info states no
// version; a bundle with one keeps them empty.
func (b *Bundle) fillVersionFromName() {
	b.VersionFromName, b.DateFromName = "", ""
	if b.Version == "" {
		b.VersionFromName, b.DateFromName = VersionFromName(b.Files)
	}
}

// MaxViewSize is the largest bundle file ReadBundleFile hands out. A changelog is a few
// kilobytes; anything near this is not something to read in a dialog.
const MaxViewSize = 256 << 10

// The refusals of ReadBundleFile; the API maps them to 404, 415 and 413.
var (
	ErrNoSuchFile  = errors.New("no such file in this bundle")
	ErrNotViewable = errors.New("not a text file: only info and .txt, .md and .log files are shown")
)

// TooLargeError says how large the refused file is.
type TooLargeError struct {
	Size  int64
	Limit int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("the file is %d bytes, more than the %d bytes shown here", e.Size, e.Limit)
}

// Viewable reports whether a bundle file is one of the text files the Firmware page opens: the
// bundle's info file and any .txt, .md or .log. Firmware images (.efw, .eq3, .hex, .bin, .gbl)
// are not.
func Viewable(name string) bool {
	if name == "info" {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".log":
		return true
	}
	return false
}

// ReadBundleFile returns one text file of the bundle dir/<typeCode>, as UTF-8. The name must be
// an entry of that bundle's own directory listing - a plain name, never a path - and a regular
// file, not a symlink; the type code must be a bundle directory Deployed would list. A file that
// is not valid UTF-8 is read as Windows-1252, which is what the older eQ-3 changelogs are written
// in (Latin-1 is its subset).
func ReadBundleFile(dir, typeCode, name string) ([]byte, error) {
	if !bundleDirRe.MatchString(typeCode) {
		return nil, ErrNoSuchFile
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return nil, ErrNoSuchFile
	}
	bundle := filepath.Join(dir, typeCode)
	if st, err := os.Lstat(bundle); err != nil || !st.IsDir() {
		return nil, ErrNoSuchFile
	}
	entries, err := os.ReadDir(bundle)
	if err != nil {
		return nil, ErrNoSuchFile
	}
	var entry os.DirEntry
	for _, e := range entries {
		if e.Name() == name {
			entry = e
			break
		}
	}
	if entry == nil {
		return nil, ErrNoSuchFile
	}
	if !Viewable(name) {
		return nil, ErrNotViewable
	}
	if !entry.Type().IsRegular() {
		// a symlink could point anywhere on the box, a fifo would hang the request
		return nil, ErrNoSuchFile
	}
	f, err := os.OpenFile(filepath.Join(bundle, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrNoSuchFile
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, ErrNoSuchFile
	}
	if st.Size() > MaxViewSize {
		return nil, &TooLargeError{Size: st.Size(), Limit: MaxViewSize}
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxViewSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxViewSize {
		return nil, &TooLargeError{Size: int64(len(b)), Limit: MaxViewSize}
	}
	return TextToUTF8(b), nil
}

var utf8BOM = []byte("\xef\xbb\xbf")

// cp1252 is what Windows-1252 puts at 0x80-0x9F; a zero is one of the five unassigned bytes,
// which map to the code point of the same number as the WHATWG decoder does. Everything from
// 0xA0 up is Latin-1 and its own code point.
var cp1252 = [32]rune{
	0x20AC, 0, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017D, 0,
	0, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0, 0x017E, 0x0178,
}

// TextToUTF8 returns valid UTF-8 as it is (without a byte order mark) and decodes anything else
// as Windows-1252.
func TextToUTF8(b []byte) []byte {
	if utf8.Valid(b) {
		return bytes.TrimPrefix(b, utf8BOM)
	}
	var out bytes.Buffer
	out.Grow(len(b) + len(b)/8)
	for _, c := range b {
		switch {
		case c < 0x80:
			out.WriteByte(c)
		case c < 0xA0 && cp1252[c-0x80] != 0:
			out.WriteRune(cp1252[c-0x80])
		default:
			out.WriteRune(rune(c))
		}
	}
	return out.Bytes()
}
