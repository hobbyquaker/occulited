package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/system"
)

// fakeExporter serves fixed lines to the download and records the queries it was given.
type fakeExporter struct {
	lines []system.LogLine
	err   error
	got   []system.LogQuery
}

func (f *fakeExporter) Read(system.LogQuery) ([]system.LogLine, error) { return f.lines, nil }
func (f *fakeExporter) Export(_ context.Context, q system.LogQuery, emit func(system.LogLine) error) error {
	f.got = append(f.got, q)
	for _, l := range f.lines {
		if err := emit(l); err != nil {
			return err
		}
	}
	return f.err
}

func logRoot(t *testing.T) system.Root {
	t.Helper()
	r := fakeRoot(t)
	_ = os.MkdirAll(filepath.Join(string(r), "proc/sys/kernel"), 0o755)
	_ = os.WriteFile(filepath.Join(string(r), "proc/sys/kernel/hostname"), []byte("openccu-lite\n"), 0o644)
	return r
}

func logDownloadServer(t *testing.T, api *SystemAPI) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fetch(t *testing.T, srv *httptest.Server, path string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestLogDownloadPassesEveryFilterToTheJournal(t *testing.T) {
	cases := []struct {
		name, path, want string
	}{
		{"no filter", "", "journalctl -o json --no-pager -q"},
		{"every filter, the page's limit dropped", "?unit=rfd&tag=S61rfd.script&severity=warning&since=%401757350800&until=%401757354400&q=Address+in+use&limit=5&format=json",
			"journalctl -o json --no-pager -q -u rfd.service -t S61rfd.script -p 4 --since @1757350800 --until @1757354400 -g Address in use --case-sensitive=false"},
		{"a relative start", "?since=-1h&format=text", "journalctl -o json --no-pager -q --since -1h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			j := system.JournalLog{Stream: func(_ context.Context, name string, args ...string) (io.ReadCloser, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				return io.NopCloser(strings.NewReader(`{"__REALTIME_TIMESTAMP":"1789203930250771","_HOSTNAME":"openccu-lite","SYSLOG_IDENTIFIER":"rfd","_PID":"688","MESSAGE":"Address in use"}` + "\n")), nil
			}}
			srv := logDownloadServer(t, &SystemAPI{Root: logRoot(t), Log: j, Journal: &j})
			res, body := fetch(t, srv, "/api/system/v1/log/download"+c.path, nil)
			if res.StatusCode != 200 || len(calls) != 1 || calls[0] != c.want {
				t.Fatalf("%d, command %q\nwant %q", res.StatusCode, calls, c.want)
			}
			if !strings.Contains(body, "Address in use") {
				t.Errorf("body %q", body)
			}
		})
	}
}

func TestLogDownloadFormats(t *testing.T) {
	line := system.LogLine{Time: "Sep 12 09:08:00", Timestamp: time.Date(2026, 9, 12, 9, 8, 0, 0, time.FixedZone("CEST", 7200)).Format(time.RFC3339Nano), Host: "openccu-lite", Tag: "rfd", Unit: "rfd", PID: 688, Severity: "warning", Message: "Address <in> use", Cursor: "s=1;i=2"}
	since, until := int64(1757350800), int64(1757354400)
	rangeName := time.Unix(since, 0).Format(logNameLayout) + "-" + time.Unix(until, 0).Format(logNameLayout)
	cases := []struct {
		name, path  string
		status      int
		ctype       string
		disposition string // a pattern
		body        string
	}{
		{"text by default", "", 200, "text/plain; charset=utf-8", `^attachment; filename="openccu-lite-log-\d{4}-\d\d-\d\dT\d{4}\.txt"$`, "2026-09-12T09:08:00+02:00 openccu-lite rfd[688]: Address <in> use\n"},
		{"text by name", "?format=text", 200, "text/plain; charset=utf-8", `\.txt"$`, "2026-09-12T09:08:00+02:00 openccu-lite rfd[688]: Address <in> use\n"},
		{"JSON lines", "?format=json", 200, "application/x-ndjson", `^attachment; filename="openccu-lite-log-\d{4}-\d\d-\d\dT\d{4}\.jsonl"$`, ""},
		{"the range in the name", fmt.Sprintf("?since=%%40%d&until=%%40%d", since, until), 200, "text/plain; charset=utf-8", `^attachment; filename="openccu-lite-log-` + regexp.QuoteMeta(rangeName) + `\.txt"$`, "2026-09-12T09:08:00+02:00 openccu-lite rfd[688]: Address <in> use\n"},
		{"another format is refused", "?format=xml", 422, "application/json", `^$`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fe := &fakeExporter{lines: []system.LogLine{line}}
			srv := logDownloadServer(t, &SystemAPI{Root: logRoot(t), Log: fe, Journal: &system.JournalLog{}})
			res, body := fetch(t, srv, "/api/system/v1/log/download"+c.path, nil)
			if res.StatusCode != c.status || !strings.HasPrefix(res.Header.Get("Content-Type"), c.ctype) {
				t.Fatalf("%d %q, want %d %q: %s", res.StatusCode, res.Header.Get("Content-Type"), c.status, c.ctype, body)
			}
			if cd := res.Header.Get("Content-Disposition"); !regexp.MustCompile(c.disposition).MatchString(cd) {
				t.Errorf("disposition %q, want %s", cd, c.disposition)
			}
			if c.status != 200 {
				return
			}
			if res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("headers %v", res.Header)
			}
			if strings.Contains(c.path, "json") {
				var got system.LogLine
				if err := json.Unmarshal([]byte(body), &got); err != nil || got != line || strings.Count(body, "\n") != 1 {
					t.Errorf("json %q: %v %+v", body, err, got)
				}
			} else if body != c.body {
				t.Errorf("body %q, want %q", body, c.body)
			}
		})
	}
	// HEAD answers the headers and does not read the log
	fe := &fakeExporter{lines: []system.LogLine{line}}
	srv := logDownloadServer(t, &SystemAPI{Root: logRoot(t), Log: fe})
	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/api/system/v1/log/download?format=json", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/x-ndjson" || !strings.HasSuffix(res.Header.Get("Content-Disposition"), `.jsonl"`) || len(fe.got) != 0 {
		t.Errorf("HEAD: %d %v, exports %d", res.StatusCode, res.Header, len(fe.got))
	}
	// a log without an exporter cannot be downloaded
	srv = logDownloadServer(t, &SystemAPI{Root: logRoot(t), Log: readOnlyLog{}})
	if res, _ := fetch(t, srv, "/api/system/v1/log/download", nil); res.StatusCode != 501 {
		t.Errorf("no exporter: %d", res.StatusCode)
	}
}

type readOnlyLog struct{}

func (readOnlyLog) Read(system.LogQuery) ([]system.LogLine, error) { return nil, nil }

func TestLogDownloadCutLines(t *testing.T) {
	var lines []system.LogLine
	for i := range 100 {
		lines = append(lines, system.LogLine{Time: "Sep  6 03:46:28", Host: "openccu", Tag: "rfd", PID: 1678, Message: fmt.Sprintf("line %03d of the fixture", i)})
	}
	oneText := len(system.LogText(lines[0]))
	cases := []struct {
		name    string
		format  string
		cap     int64
		err     error
		last    string // text: the last line; json: the cut object's message prefix
		cut     string // json: "limit", "error" or "" (no cut line)
		entries int
	}{
		{"text at the cap", "text", 1000, nil, "-- Cut off here: the download stops at 1000 bytes. The later entries are not in this file; a later start of the time range or narrower filters fetch them. --", "limit", 1000 / oneText},
		{"json at the cap", "json", 1000, nil, "Cut off here: the download stops at 1000 bytes.", "limit", -1},
		{"text under the cap: no cut line", "text", 1 << 20, nil, "Sep  6 03:46:28 openccu rfd[1678]: line 099 of the fixture", "", 100},
		{"json under the cap: no cut line", "json", 1 << 20, nil, "", "", 100},
		{"text on a read error", "text", 1 << 20, errors.New("journalctl: exit status 1: boom"), "-- Cut off here: the log could not be read any further (journalctl: exit status 1: boom). --", "error", 100},
		{"json on a read error", "json", 1 << 20, errors.New("journalctl: exit status 1: boom"), "Cut off here: the log could not be read any further (journalctl: exit status 1: boom).", "error", 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := logDownloadServer(t, &SystemAPI{Root: logRoot(t), Log: &fakeExporter{lines: lines, err: c.err}, logCap: c.cap})
			res, body := fetch(t, srv, "/api/system/v1/log/download?format="+c.format, nil)
			if res.StatusCode != 200 || !strings.HasSuffix(body, "\n") {
				t.Fatalf("%d %q", res.StatusCode, body)
			}
			rows := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
			last := rows[len(rows)-1]
			entries := rows
			if c.cut != "" {
				entries = rows[:len(rows)-1]
			}
			if size := len(body) - len(last) - 1; c.cut == "limit" && (int64(size) > c.cap || int64(size) <= c.cap-int64(len(rows[0]))-1) {
				t.Errorf("%d bytes before the cut line, cap %d", size, c.cap)
			}
			if c.entries >= 0 && len(entries) != c.entries {
				t.Errorf("%d entries, want %d", len(entries), c.entries)
			}
			if c.format == "text" {
				if last != c.last {
					t.Errorf("last line %q\nwant      %q", last, c.last)
				}
				return
			}
			for _, e := range entries {
				var l system.LogLine
				if err := json.Unmarshal([]byte(e), &l); err != nil || !strings.HasPrefix(l.Message, "line ") {
					t.Fatalf("entry %q: %v", e, err)
				}
			}
			var cut logCut
			_ = json.Unmarshal([]byte(last), &cut)
			if cut.Cut != c.cut || !strings.HasPrefix(cut.Message, c.last) || (c.cut == "limit") != (cut.LimitBytes == c.cap) {
				t.Errorf("last line %q", last)
			}
		})
	}
	// the default cap is 50 MB
	if sizeName(logDownloadCap) != "50 MB" {
		t.Errorf("%q", sizeName(logDownloadCap))
	}
}

func TestLogFileName(t *testing.T) {
	zone := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 9, 12, 11, 30, 15, 0, zone)
	cases := []struct {
		name, host string
		q          system.LogQuery
		ranged     bool
		ext, want  string
	}{
		{"no range: the download time", "openccu-lite", system.LogQuery{}, true, "txt", "openccu-lite-log-2026-09-12T1130.txt"},
		{"busybox has no range", "openccu", system.LogQuery{Since: "-1h", Until: "@1757354400"}, false, "txt", "openccu-log-2026-09-12T1130.txt"},
		{"a preset runs to the download time", "box", system.LogQuery{Since: "-1h"}, true, "jsonl", "box-log-2026-09-12T1030-2026-09-12T1130.jsonl"},
		{"minutes, days and a compound span", "box", system.LogQuery{Since: "-1h30min", Until: "-15min"}, true, "txt", "box-log-2026-09-12T1000-2026-09-12T1115.txt"},
		{"seven days", "box", system.LogQuery{Since: "-7d"}, true, "txt", "box-log-2026-09-05T1130-2026-09-12T1130.txt"},
		{"a picked pair of instants", "box", system.LogQuery{Since: "@1757350800", Until: "@1757354400"}, true, "txt", "box-log-2025-09-08T1900-2025-09-08T2000.txt"},
		{"only an end", "box", system.LogQuery{Until: "@1757354400"}, true, "txt", "box-log-until-2025-09-08T2000.txt"},
		{"a wall-clock date", "box", system.LogQuery{Since: "2026-09-06 12:00", Until: "2026-09-07"}, true, "txt", "box-log-2026-09-06T1200-2026-09-07T0000.txt"},
		{"today and yesterday", "box", system.LogQuery{Since: "yesterday", Until: "today"}, true, "txt", "box-log-2026-09-11T0000-2026-09-12T0000.txt"},
		{"an expression it cannot read stands as given", "box", system.LogQuery{Since: "3 fortnights ago"}, true, "txt", "box-log-3-fortnights-ago-2026-09-12T1130.txt"},
		{"a host name reduced to what a file name carries", `my "box"/../x`, system.LogQuery{}, true, "txt", "my-box-..-x-log-2026-09-12T1130.txt"},
		{"no host name", "", system.LogQuery{}, true, "txt", "log-2026-09-12T1130.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := logFileName(c.host, c.q, c.ranged, now, c.ext); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
	if _, ok := parseSpan(""); ok {
		t.Error("an empty span")
	}
	if _, ok := parseSpan("1h and more"); ok {
		t.Error("a span with trailing words")
	}
}

// The download is a GET like /log: any session may fetch it - a plain user, from the cookie or
// the Authorization header (the shell follows the link with a one-time ticket; a credential in
// ?sid= is refused since openccu-lite task 260) - and nobody without one.
func TestLogDownloadNeedsASession(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup("admin", "admin password 123"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("bob", "bobs password 123", auth.RoleUser, false); err != nil {
		t.Fatal(err)
	}
	bob, err := store.Login("bob", "bobs password 123", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateToken("ci", auth.TokenOptions{Scopes: auth.Scopes{auth.ScopeLogsRead}})
	if err != nil {
		t.Fatal(err)
	}
	r := logRoot(t)
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store}
	a.Register(mux)
	(&SystemAPI{Root: r, Log: testLog}).Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	cases := []struct {
		name   string
		query  string
		hdr    map[string]string
		status int
	}{
		{"no session", "", nil, 401},
		{"a session nobody has", "?sid=@AAAAAAAAAA@", nil, 401},
		{"a user session's cookie", "", map[string]string{"Cookie": CookieName + "=" + bob.ID}, 200},
		{"a user session over HTTPS", "", map[string]string{"Cookie": SecureCookieName + "=" + bob.ID}, 200},
		{"a user session as bearer", "", map[string]string{"Authorization": "Bearer " + bob.ID}, 200},
		{"a user session as ?sid=@..@ is refused", "?sid=@" + bob.ID + "@", nil, 401},
		{"a token as ?sid= is refused", "?format=json&sid=" + token, nil, 401},
		{"a token as bearer", "?format=json", map[string]string{"Authorization": "Bearer " + token}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, body := fetch(t, srv, "/api/system/v1/log/download"+c.query, c.hdr)
			if res.StatusCode != c.status {
				t.Fatalf("%d, want %d: %s", res.StatusCode, c.status, body)
			}
			if c.status == 200 && (!strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment; ") || !strings.Contains(body, "Address in use")) {
				t.Errorf("%v %q", res.Header, body)
			}
			if c.status == 401 && (strings.Contains(body, "Address in use") || res.Header.Get("Content-Disposition") != "") {
				t.Errorf("the log leaked: %q", body)
			}
		})
	}
}
