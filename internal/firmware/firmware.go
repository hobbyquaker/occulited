// Package firmware fetches device firmware from eQ-3's own update service for the device types
// that are actually paired (D-27, task 11). It downloads; it never installs — installing is a
// user action in the frontend. The mechanism is the one the CCU itself uses, prototyped in the
// maintainer's hm-firmware-downloader:
//
//	GET https://ccu3-update.homematic.com/firmware/api/firmware/search/DEVICE?product=HM-CCU3&version=<VERSION>
//	    → JSONP  homematic.com.setDeviceFirmwareVersions([{type, version, ...}])
//	GET https://ccu3-update.homematic.com/firmware/download?cmd=download&product=<TYPE>&serial=0
//	    → the .tgz, name in Content-Disposition
package firmware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/httpwait"
)

// DefaultBase is eQ-3's update service. Always https (the prototype used http for the download).
const DefaultBase = "https://ccu3-update.homematic.com"

// Entry is one device type in the index.
type Entry struct {
	Type    string          `json:"type"`
	Version string          `json:"version"`
	Raw     json.RawMessage `json:"-"`
}

// Client talks to the service.
type Client struct {
	Base string
	HTTP *http.Client
	// SystemVersion is the system's VERSION (/VERSION, 3.89.9.20260914). The index is asked with
	// it and the product HM-CCU3, as eQ-3's WebUI asks (B-195): the service leaves out the bundles
	// that need a newer CCU firmware. Empty = the bare query, which lists every bundle.
	SystemVersion string
	// IndexTimeout bounds the index request as a whole, HeaderWait a download's wait for its
	// response header (B-56); 0 is httpwait.Meta and httpwait.HeaderWait.
	IndexTimeout time.Duration
	HeaderWait   time.Duration
}

// hostOf is u's host, for an error that names who did not answer.
func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// IndexProduct is the product the index is asked for: the identity the stock CCU and OpenCCU
// WebUI send.
const IndexProduct = "HM-CCU3"

// New returns a client for base (DefaultBase when empty).
func New(base string) *Client {
	if base == "" {
		base = DefaultBase
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

var jsonpRe = regexp.MustCompile(`(?s)^\s*[\w.]+\s*\((.*)\)\s*;?\s*$`)

// Index fetches and parses the device-firmware index. The JSONP wrapper is stripped; the array
// elements are decoded leniently because eQ-3 does not document the schema.
func (c *Client) Index(ctx context.Context) ([]Entry, error) {
	u := c.Base + "/firmware/api/firmware/search/DEVICE"
	if c.SystemVersion != "" {
		u += "?" + url.Values{"product": {IndexProduct}, "version": {c.SystemVersion}}.Encode()
	}
	// B-56: the index is metadata - bounded as a whole, not by the downloads' client timeout
	bounded, explain, cancel := httpwait.Bound(ctx, c.IndexTimeout, hostOf(u))
	defer cancel()
	req, err := http.NewRequestWithContext(bounded, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, explain(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("index: HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, explain(err)
	}
	return ParseIndex(body)
}

// ParseIndex parses the JSONP (or plain JSON) index body.
func ParseIndex(body []byte) ([]Entry, error) {
	s := strings.TrimSpace(string(body))
	if m := jsonpRe.FindStringSubmatch(s); m != nil && !strings.HasPrefix(s, "[") {
		s = m[1]
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	out := make([]Entry, 0, len(raw))
	for _, r := range raw {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			continue
		}
		e := Entry{Raw: r}
		for _, k := range []string{"type", "TYPE", "deviceType", "product"} {
			if v, ok := m[k].(string); ok && v != "" {
				e.Type = v
				break
			}
		}
		for _, k := range []string{"version", "VERSION", "firmwareVersion"} {
			switch v := m[k].(type) {
			case string:
				e.Version = v
			case float64:
				e.Version = strconv.FormatFloat(v, 'f', -1, 64)
			}
			if e.Version != "" {
				break
			}
		}
		if e.Type != "" {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// deviceTypeRe is what a type may look like in a download: the index's spelling, which may carry a
// space (HmIP-HAP JS1), but nothing path-like.
var deviceTypeRe = regexp.MustCompile(`^[A-Za-z0-9._-][A-Za-z0-9._ -]{0,63}$`)

// Download fetches the firmware for one device type into dir, writing atomically. Returns the
// final file name (from Content-Disposition, sanitised) and its size.
func (c *Client) Download(ctx context.Context, deviceType, dir string) (string, int64, error) {
	if !deviceTypeRe.MatchString(deviceType) {
		return "", 0, errors.New("invalid device type")
	}
	q := url.Values{"cmd": {"download"}, "product": {deviceType}, "serial": {"0"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/firmware/download?"+q.Encode(), nil)
	if err != nil {
		return "", 0, err
	}
	res, err := httpwait.Do(c.HTTP, req, c.HeaderWait)
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", 0, fmt.Errorf("download %s: HTTP %d", deviceType, res.StatusCode)
	}
	name := ""
	if _, params, err := mime.ParseMediaType(res.Header.Get("Content-Disposition")); err == nil {
		name = filepath.Base(params["filename"])
	}
	if name == "" || name == "." || name == "/" || !strings.HasSuffix(strings.ToLower(name), ".tgz") && !strings.HasSuffix(strings.ToLower(name), ".tar.gz") {
		return "", 0, fmt.Errorf("download %s: no usable firmware filename in Content-Disposition (%q)", deviceType, res.Header.Get("Content-Disposition"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(dir, ".fw-*.part")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(res.Body, 64<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	if n < 1024 {
		return "", 0, fmt.Errorf("download %s: %d bytes is not a firmware file", deviceType, n)
	}
	// the archive must at least be a gzip stream
	f, _ := os.Open(tmp.Name())
	magic := make([]byte, 2)
	_, _ = io.ReadFull(f, magic)
	f.Close()
	if magic[0] != 0x1f || magic[1] != 0x8b {
		return "", 0, fmt.Errorf("download %s: not a gzip archive", deviceType)
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", 0, err
	}
	return name, n, nil
}

// --- versions ---

var versionRe = regexp.MustCompile(`\d+`)

// CompareVersions compares component-wise: "1.10.16" > "1.9.0", "V1_10_16" == "1.10.16".
// Returns -1, 0, 1.
func CompareVersions(a, b string) int {
	as := versionRe.FindAllString(a, -1)
	bs := versionRe.FindAllString(b, -1)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Device is what the interface processes report: type and running firmware.
type Device struct {
	Type     string
	Firmware string
	// NotUpdatable: the interface says UPDATABLE 0 - nothing is fetched for it (B-225)
	NotUpdatable bool
}

// TypeKey is the form in which a device type and an index type are compared - what eQ-3's WebUI
// does (B-195): case does not count (hmipserver reports some older types in upper case,
// HMIP-WRC2, where the index says HmIP-WRC2), the last underscore is a space (SPHM-1039), and
// HmIP-HAP-JS1 is HmIP-HAP JS1 (SPHM-1034).
func TypeKey(t string) string {
	k := strings.ToLower(strings.TrimSpace(t))
	if i := strings.LastIndex(k, "_"); i >= 0 {
		k = k[:i] + " " + k[i+1:]
	}
	if k == "hmip-hap js1" {
		k = "hmip-hap-js1"
	}
	return k
}

// typeFallback: a device type that uses another type's firmware when the index has none of its
// own - the HmIP-HAP-B1 takes the HmIP-HAP's (SPHM-1022).
var typeFallback = map[string]string{"hmip-hap-b1": "hmip-hap"}

// Index is the index keyed by TypeKey. A type listed twice (eQ-3 lists HM-LC-Dim1TPBU-FM twice)
// keeps its higher version.
type Index map[string]Entry

// ByKey builds the keyed index.
func ByKey(index []Entry) Index {
	out := Index{}
	for _, e := range index {
		k := TypeKey(e.Type)
		if cur, ok := out[k]; !ok || CompareVersions(e.Version, cur.Version) > 0 {
			out[k] = e
		}
	}
	return out
}

// Match is the index entry for a device type, with the index's spelling of the type - the one a
// download asks for.
func (x Index) Match(deviceType string) (Entry, bool) {
	k := TypeKey(deviceType)
	if e, ok := x[k]; ok {
		return e, true
	}
	if f, ok := typeFallback[k]; ok {
		e, ok := x[f]
		return e, ok
	}
	return Entry{}, false
}

// Newer says whether the index version beats the firmware a device runs. A device that reports
// only major.minor - the BidCos devices - is compared on those two, as the WebUI compares them.
func Newer(indexVersion, firmware string) bool {
	if n := versionRe.FindAllString(firmware, -1); len(n) == 2 {
		if iv := versionRe.FindAllString(indexVersion, -1); len(iv) > 2 {
			indexVersion = strings.Join(iv[:2], ".")
		}
	}
	return CompareVersions(indexVersion, firmware) > 0
}

// Plan says what to download: for every paired type, the index entry whose version beats the
// oldest firmware any device of that type runs. The entries carry the index's spelling.
func Plan(index []Entry, devices []Device) []Entry {
	byKey := ByKey(index)
	oldest := map[string]string{} // index key -> the oldest firmware among its devices
	for _, d := range devices {
		if d.Type == "" || d.NotUpdatable {
			continue
		}
		e, ok := byKey.Match(d.Type)
		if !ok {
			continue
		}
		k := TypeKey(e.Type)
		if cur, ok := oldest[k]; !ok || CompareVersions(d.Firmware, cur) < 0 {
			oldest[k] = d.Firmware
		}
	}
	var out []Entry
	for k, fw := range oldest {
		e := byKey[k]
		if e.Version == "" || Newer(e.Version, fw) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Prune removes firmware files for types no longer paired and older versions of a type.
// Files are named like `HmIP-BBL_update_V1_10_16_230616.tgz`; the type is the part before
// `_update_` (or before the first `_` for the HAP style `HMIP-HAP_3_0_36_241218.tgz`).
func Prune(dir string, pairedTypes []string) (removed []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	paired := map[string]bool{}
	for _, t := range pairedTypes {
		paired[TypeKey(t)] = true
	}
	newest := map[string]string{}
	files := map[string][]string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".tgz") || strings.HasSuffix(n, ".tar.gz")) {
			continue
		}
		typ := TypeOfFile(n)
		files[typ] = append(files[typ], n)
		if cur, ok := newest[typ]; !ok || CompareVersions(n, cur) > 0 {
			newest[typ] = n
		}
	}
	for typ, names := range files {
		for _, n := range names {
			if !paired[TypeKey(typ)] || n != newest[typ] {
				if err := os.Remove(filepath.Join(dir, n)); err == nil {
					removed = append(removed, n)
				}
			}
		}
	}
	sort.Strings(removed)
	return removed, nil
}

// TypeOfFile derives the device type from a firmware file name.
func TypeOfFile(name string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".tgz"), ".tar.gz")
	if i := strings.Index(base, "_update_"); i > 0 {
		return base[:i]
	}
	if i := strings.Index(base, "_"); i > 0 {
		return base[:i]
	}
	return base
}
