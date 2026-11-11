package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/meta"
	"github.com/hobbyquaker/occulited/internal/system"
)

// occulited B-141: a backup above the old 2 GiB cut, or above what the userfs holds, is answered
// honestly - 413 too-large, 507 no-space with the sizes - and never stored cut off and checked
// ("could not untar"). The caps are lowered for the test instead of writing gigabytes.

func lowUploadCaps(t *testing.T, max, reserve int64) {
	t.Helper()
	oldMax, oldRes := system.MaxSBKUpload, system.UploadReserve
	system.MaxSBKUpload, system.UploadReserve = max, reserve
	t.Cleanup(func() { system.MaxSBKUpload, system.UploadReserve = oldMax, oldRes })
}

// chunked posts body to /restore/check without a Content-Length, so only the stream's own cap
// can stop it.
func (g *cryptRig) chunked(t *testing.T, body []byte) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", g.srv.URL+"/api/system/v1/restore/check", io.MultiReader(bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = -1
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

func (g *cryptRig) restoreLeftovers(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, d := range []string{system.BackupDir, system.StagingDir} {
		entries, _ := os.ReadDir(string(g.root) + d)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "restore-") {
				out = append(out, d+"/"+e.Name())
			}
		}
	}
	return out
}

func TestRestoreCheckSizes(t *testing.T) {
	g := newCryptRig(t)
	lowUploadCaps(t, 64<<10, 0)
	fits := bytes.Repeat([]byte("s"), 60<<10)
	over := bytes.Repeat([]byte("s"), 80<<10)

	// what the page asks before it uploads
	st, out := g.do(t, "GET", "/restore/space", "")
	if st != 200 || out["max_bytes"] != float64(64<<10) || out["reserve_bytes"] != float64(0) {
		t.Fatalf("%d %v", st, out)
	}
	if f, ok := out["free_bytes"].(float64); !ok || f <= 0 {
		t.Fatalf("free_bytes %v", out["free_bytes"])
	}

	if st, out := g.upload(t, "fits.sbk", fits); st != 200 || out["file"] != "restore-fits.sbk" {
		t.Fatalf("below the cap: %d %v", st, out)
	}
	// announced by its Content-Length (multipart, as the page sends it): refused before it is read
	st, out = g.upload(t, "big.sbk", over)
	if st != 413 || out["error"] != "too-large" || !strings.Contains(out["message"].(string), "larger than") {
		t.Fatalf("announced: %d %v", st, out)
	}
	if d := out["detail"].(map[string]any); d["max_bytes"] != float64(64<<10) || d["size_bytes"].(float64) < float64(len(over)) {
		t.Errorf("detail %v", d)
	}
	// without a Content-Length: the stream's cap, not a quiet cut and a "could not untar"
	st, out = g.chunked(t, over)
	if st != 413 || out["error"] != "too-large" {
		t.Fatalf("streamed: %d %v", st, out)
	}
	if l := g.restoreLeftovers(t); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}

	// no room on the userfs: 507 with the sizes, before the upload is read
	system.UploadReserve = 1 << 60
	st, out = g.upload(t, "fits.sbk", fits)
	if st != 507 || out["error"] != "no-space" || !strings.Contains(out["message"].(string), "not enough free space") {
		t.Fatalf("no room: %d %v", st, out)
	}
	if d := out["detail"].(map[string]any); d["reserve_bytes"] != float64(1<<60) || d["free_bytes"].(float64) <= 0 || d["need_bytes"].(float64) < float64(1<<60) {
		t.Errorf("detail %v", d)
	}

}

// The metadata import takes a backup as well and refuses it the same way: over the cap 413, no
// room 507, both before it is read.
func TestImportRegadomBackupRoom(t *testing.T) {
	lowUploadCaps(t, 64<<10, 0)
	root := t.TempDir()
	s, err := meta.New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&MetaAPI{Store: s, Root: root}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	post := func(size int) (int, apiError) {
		var mp bytes.Buffer
		mp.WriteString("--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"x.sbk\"\r\nContent-Type: application/octet-stream\r\n\r\n")
		mp.Write(make([]byte, size))
		mp.WriteString("\r\n--b--\r\n")
		req, _ := http.NewRequest("POST", srv.URL+"/api/meta/v1/import/regadom?dry_run=true", &mp)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var e apiError
		_ = json.NewDecoder(res.Body).Decode(&e)
		return res.StatusCode, e
	}
	if st, e := post(80 << 10); st != 413 || e.Error != "too-large" {
		t.Errorf("over the cap: %d %v", st, e)
	}
	system.UploadReserve = 1 << 60
	if st, e := post(1 << 10); st != 507 || e.Error != "no-space" {
		t.Errorf("no room: %d %v", st, e)
	}
	if e, _ := os.ReadDir(filepath.Join(root, system.StagingDir)); len(e) != 0 {
		t.Errorf("staged: %v", e)
	}
}

func TestWriteUploadErr(t *testing.T) {
	for _, tc := range []struct {
		err  error
		st   int
		code string
	}{
		{&system.TooLargeError{Limit: 1}, 413, "too-large"},
		{&system.NoRoomError{Free: 1}, 507, "no-space"},
		{system.ErrUploadIncomplete, 400, "incomplete"},
		{errors.New("other"), 500, ""},
	} {
		rec := httptest.NewRecorder()
		writeCryptErr(rec, tc.err)
		var e apiError
		_ = json.NewDecoder(rec.Body).Decode(&e)
		if rec.Code != tc.st || (tc.code != "" && e.Error != tc.code) {
			t.Errorf("%v: %d %s", tc.err, rec.Code, e.Error)
		}
	}
}
