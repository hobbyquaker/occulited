package httpapi

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/system"
)

// openccu-lite B-239: deploy/lighttpd/occulited.conf caps every request body in front of occulited at
// 8 MiB except on the upload routes, which keep lighttpd's global ceiling. The exception is a regular
// expression in the fragment; this keeps it and the route table together: every route that takes an
// upload matches it, no other route with a body under /api/ does, and every route it names exists.
func TestLighttpdUploadRoutes(t *testing.T) {
	fullAPI(t)
	conf, err := os.ReadFile("../../deploy/lighttpd/occulited.conf")
	if err != nil {
		t.Fatal(err)
	}
	var pattern string
	for _, line := range strings.Split(string(conf), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `$HTTP["url"] !~ "^/api/`) {
			pattern = strings.TrimSuffix(strings.TrimPrefix(line, `$HTTP["url"] !~ "`), `" {`)
		}
	}
	if pattern == "" {
		t.Fatal("no exception to the 8 MiB cap under /api/ in occulited.conf")
	}
	exempt, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("the exception is not a regular expression Go reads too: %v", err)
	}
	uploads := map[string]bool{
		"POST /api/system/v1/addons/install":        true, // an addon archive, MaxAddonSize
		"POST /api/system/v1/radio/firmware/upload": true, // a coprocessor firmware file
		"POST /api/system/v1/firmware/upload":       true, // a device firmware bundle, 64 MiB
		"POST /api/system/v1/restore/check":         true, // a backup, MaxSBKUpload (B-141: its own ceiling)
		"POST /api/system/v1/system-update/upload":  true, // a system image, 2 GiB
		"POST /api/meta/v1/import/regadom":          true, // a ReGa database, 64 MiB, or a backup
	}
	table := RouteScopes()
	for p := range uploads {
		if _, ok := table[p]; !ok {
			t.Errorf("the upload route %q is not in the route table any more: rename it in occulited.conf too", p)
		}
	}
	for p := range table {
		method, path, _ := strings.Cut(p, " ")
		if !strings.HasPrefix(path, "/api/") {
			continue
		}
		if exempt.MatchString(path) != uploads[p] {
			if uploads[p] {
				t.Errorf("the upload route %q is capped at 8 MiB by occulited.conf", p)
			} else if method != "GET" && method != "DELETE" {
				t.Errorf("%q keeps lighttpd's global ceiling although it takes no upload", p)
			}
		}
	}
}

// occulited B-141: the two routes that take a backup have a ceiling of their own in occulited.conf,
// above the global 2.25 GiB - MaxSBKUpload plus room for the multipart framing - and stream with
// minimal buffering, so lighttpd neither refuses a backup occulited would take nor copies it into
// its upload directory on the same userfs first. This keeps the block, its routes and the figure
// together.
func TestLighttpdBackupCeiling(t *testing.T) {
	fullAPI(t)
	conf, err := os.ReadFile("../../deploy/lighttpd/occulited.conf")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(conf), "\n")
	var pattern string
	var kb int64 = -1
	stream := ""
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `$HTTP["url"] =~ "^/api/(system/v1/restore/check`) {
			continue
		}
		pattern = strings.TrimSuffix(strings.TrimPrefix(line, `$HTTP["url"] =~ "`), `" {`)
		for _, l := range lines[i+1:] {
			l = strings.TrimSpace(l)
			if l == "}" {
				break
			}
			if v, ok := strings.CutPrefix(l, "server.max-request-size = "); ok {
				kb, _ = strconv.ParseInt(v, 10, 64)
			}
			if v, ok := strings.CutPrefix(l, "server.stream-request-body = "); ok {
				stream = v
			}
		}
	}
	if pattern == "" {
		t.Fatal("no block for the backup routes in occulited.conf")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("the backup routes' pattern is not a regular expression Go reads too: %v", err)
	}
	backups := map[string]bool{"POST /api/system/v1/restore/check": true, "POST /api/meta/v1/import/regadom": true}
	for p := range RouteScopes() {
		method, path, _ := strings.Cut(p, " ")
		if method == "GET" || method == "DELETE" {
			continue
		}
		if re.MatchString(path) != backups[p] {
			t.Errorf("%q: matched by the backup routes' block %v, expected %v", p, re.MatchString(path), backups[p])
		}
	}
	if min := system.MaxSBKUpload>>10 + 1024; kb < min {
		t.Errorf("the backup routes' server.max-request-size is %d KiB, below MaxSBKUpload plus 1 MiB (%d KiB)", kb, min)
	}
	if kb > 4<<30-1 {
		t.Errorf("server.max-request-size %d KiB does not fit lighttpd's 32-bit field", kb)
	}
	if stream != "2" {
		t.Errorf("the backup routes stream with server.stream-request-body = %q, want 2 (minimal buffering)", stream)
	}
}
