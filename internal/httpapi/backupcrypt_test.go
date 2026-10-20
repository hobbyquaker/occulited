package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/system"
)

// cryptRig is a bootRig with task 91's store, a createBackup.sh stand-in that writes the file it
// is told to, and a confirmed-ticket check that accepts "ok" for the plain download.
type cryptRig struct {
	*bootRig
	store *backupcrypt.Store
	sbk   string
}

func newCryptRig(t *testing.T) *cryptRig {
	t.Helper()
	g := &cryptRig{bootRig: newBootRig(t, false), sbk: "usr_local.tar.gz\x00\x00the backup"}
	g.store = &backupcrypt.Store{Dir: g.state}
	g.api.BackupCrypt = g.store
	g.api.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "/bin/createBackup.sh") {
			return nil, os.WriteFile(args[0], []byte(g.sbk), 0o644)
		}
		return []byte("generated on 3.89.9, applying to 3.89.9\nok"), nil
	}
	g.api.ConfirmTicket = func(ticket, path, sid string) bool { return ticket == "ok" && path == BackupPlainPath && sid == "s" }
	_ = os.MkdirAll(string(g.root)+system.BackupDir, 0o755)
	return g
}

// setup runs the wizard's two requests with a browser-made recipient and returns the secret.
func (g *cryptRig) setup(t *testing.T) *backupcrypt.Secret {
	t.Helper()
	sec, _ := backupcrypt.NewSecret()
	st, out := g.do(t, "POST", "/backup/encryption/recovery", `{"recipient":"`+sec.Recipient()+`"}`)
	if st != 200 || out["fingerprint"] != sec.Fingerprint() || out["pending_id"] == "" || out["recovery_key"] != nil {
		t.Fatalf("%d %v", st, out)
	}
	if st, out := g.do(t, "POST", "/backup/encryption/confirm", `{"pending_id":"`+out["pending_id"].(string)+`"}`); st != 200 || out["enabled"] != true {
		t.Fatalf("%d %v", st, out)
	}
	return sec
}

func (g *cryptRig) get(t *testing.T, path string) *http.Response {
	t.Helper()
	res, err := http.Get(g.srv.URL + "/api/system/v1" + path)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (g *cryptRig) upload(t *testing.T, name string, body []byte) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", g.srv.URL+"/api/system/v1/restore/check", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	if name != "" {
		// the multipart way, as the page sends it
		var mp bytes.Buffer
		mp.WriteString("--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + name + "\"\r\nContent-Type: application/octet-stream\r\n\r\n")
		mp.Write(body)
		mp.WriteString("\r\n--b--\r\n")
		req, _ = http.NewRequest("POST", g.srv.URL+"/api/system/v1/restore/check", &mp)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

func TestEncryptionRoutes(t *testing.T) {
	g := newCryptRig(t)
	st, out := g.do(t, "GET", "/backup/encryption", "")
	if st != 200 || out["enabled"] != false || out["recovery"] != nil || out["box"] != nil {
		t.Fatalf("%d %v", st, out)
	}
	if st, out := g.do(t, "PUT", "/backup/encryption", `{"enabled":true}`); st != 422 || out["error"] != "no-recovery-key" {
		t.Errorf("on without a key: %d %v", st, out)
	}
	if st, out := g.do(t, "POST", "/backup/encryption/recovery", `{"recipient":"age1pq1qqqq"}`); st != 422 || out["error"] != "unsupported" {
		t.Errorf("pq: %d %v", st, out)
	}
	if st, out := g.do(t, "POST", "/backup/encryption/recovery", `{}`); st != 422 {
		t.Errorf("empty: %d %v", st, out)
	}
	if st, out := g.do(t, "POST", "/backup/encryption/confirm", `{"pending_id":"nope"}`); st != 404 || out["error"] != "pending-unknown" {
		t.Errorf("unknown pending: %d %v", st, out)
	}
	sec := g.setup(t)
	st, out = g.do(t, "GET", "/backup/encryption", "")
	rec := out["recovery"].(map[string]any)
	box := out["box"].(map[string]any)
	if st != 200 || out["enabled"] != true || rec["fingerprint"] != sec.Fingerprint() || box["fingerprint"] == "" || rec["recipient"] != nil {
		t.Fatalf("%d %v", st, out)
	}
	// the answer never carries a recipient or an identity
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "age1") || strings.Contains(string(raw), "AGE-SECRET") {
		t.Errorf("secret material in the view: %s", raw)
	}
	// the test route
	for _, c := range []struct {
		key     string
		status  int
		matches string
		code    string
	}{
		{sec.Code(), 200, "current", ""},
		{strings.ToLower(sec.Code()), 200, "current", ""},
		{sec.IdentityString(), 200, "current", ""},
		{sec.Code()[:10], 422, "", "invalid-key"},
		{sec.Recipient(), 422, "", "is-recipient"},
		{"AGE-SECRET-KEY-PQ-1QQ", 422, "", "unsupported"},
	} {
		st, out := g.do(t, "POST", "/backup/encryption/test", `{"recovery_key":"`+c.key+`"}`)
		if st != c.status || (c.matches != "" && out["matches"] != c.matches) || (c.code != "" && out["error"] != c.code) {
			t.Errorf("%q: %d %v", c.key, st, out)
		}
		// the code comes back grouped; a key given as an age identity has none
		wantCode := sec.Code()
		if c.key == sec.IdentityString() {
			wantCode = ""
		}
		if st == 200 && (out["identity"] != sec.IdentityString() || out["recovery_key"] != wantCode || out["fingerprint"] != sec.Fingerprint()) {
			t.Errorf("%q: %v", c.key, out)
		}
	}
	other, _ := backupcrypt.NewSecret()
	if st, out := g.do(t, "POST", "/backup/encryption/test", `{"recovery_key":"`+other.Code()+`"}`); st != 200 || out["matches"] != "none" {
		t.Errorf("%d %v", st, out)
	}
	// a rotation: the old key is "previous"
	sec2 := g.setup(t)
	if st, out := g.do(t, "POST", "/backup/encryption/test", `{"recovery_key":"`+sec.Code()+`"}`); st != 200 || out["matches"] != "previous" || out["retired"] == nil {
		t.Errorf("%d %v", st, out)
	}
	st, out = g.do(t, "GET", "/backup/encryption", "")
	if st != 200 {
		t.Fatalf("%d %v", st, out)
	}
	if prev := out["previous"].([]any); len(prev) != 1 || prev[0].(map[string]any)["fingerprint"] != sec.Fingerprint() || out["recovery"].(map[string]any)["fingerprint"] != sec2.Fingerprint() {
		t.Errorf("%v", out)
	}
	// a key generated here (the HTTP fallback) comes back once, and confirms
	st, out = g.do(t, "POST", "/backup/encryption/recovery", `{"generate":true}`)
	if st != 200 || out["recovery_key"] == nil {
		t.Fatalf("%d %v", st, out)
	}
	gen, err := backupcrypt.ParseSecret(out["recovery_key"].(string))
	if err != nil || gen.Fingerprint() != out["fingerprint"] {
		t.Fatal(err)
	}
	if st, out := g.do(t, "POST", "/backup/encryption/confirm", `{"pending_id":"`+out["pending_id"].(string)+`"}`); st != 200 || out["recovery"].(map[string]any)["fingerprint"] != gen.Fingerprint() {
		t.Errorf("%d %v", st, out)
	}
	// off: the download is plain again, the key stays
	if st, out := g.do(t, "PUT", "/backup/encryption", `{"enabled":false}`); st != 200 || out["enabled"] != false || out["recovery"] == nil {
		t.Errorf("%d %v", st, out)
	}
	if st, out := g.do(t, "PUT", "/backup/encryption", `{"enabled":true}`); st != 200 || out["enabled"] != true {
		t.Errorf("%d %v", st, out)
	}
}

func TestBackupDownloadEncrypted(t *testing.T) {
	g := newCryptRig(t)
	// before any setup: the plain .sbk as always
	res := g.get(t, "/backup")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != g.sbk || !strings.HasSuffix(res.Header.Get("Content-Disposition"), `.sbk"`) {
		t.Fatalf("%d %q %s", res.StatusCode, body, res.Header.Get("Content-Disposition"))
	}
	sec := g.setup(t)
	res = g.get(t, "/backup")
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.HasPrefix(string(body), "age-encryption.org/v1\n") || !strings.HasSuffix(res.Header.Get("Content-Disposition"), `.sbk.age"`) {
		t.Fatalf("%d %q %s", res.StatusCode, body[:min(40, len(body))], res.Header.Get("Content-Disposition"))
	}
	// the recovery key opens it, and so does the system's own identity
	boxID, _, _ := g.store.BoxIdentity()
	for _, id := range []age.Identity{sec.Identity(), boxID} {
		out, err := age.Decrypt(bytes.NewReader(body), id)
		if err != nil {
			t.Fatal(err)
		}
		if plain, _ := io.ReadAll(out); string(plain) != g.sbk {
			t.Errorf("%q", plain)
		}
	}
	if !strings.Contains(string(body), "rec="+strings.ReplaceAll(sec.Fingerprint(), "-", "")) {
		t.Error("no meta stanza")
	}
	// the temporary .sbk is gone
	if entries, _ := os.ReadDir(string(g.root) + system.BackupDir); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
	// the plain download asks for the confirmation
	res = g.get(t, "/backup?encrypted=false")
	if res.StatusCode != 403 {
		t.Errorf("plain without confirmation: %d", res.StatusCode)
	}
	res = g.get(t, "/backup?encrypted=false&confirm=wrong")
	if res.StatusCode != 403 {
		t.Errorf("plain with a wrong ticket: %d", res.StatusCode)
	}
	res = g.get(t, "/backup?encrypted=false&confirm=ok")
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(body) != g.sbk || !strings.HasSuffix(res.Header.Get("Content-Disposition"), `.sbk"`) {
		t.Errorf("confirmed plain: %d %q", res.StatusCode, body)
	}
	// both downloads were recorded as made here: the restore check says so
	if st, out := g.upload(t, "", []byte(g.sbk)); st != 200 || out["encryption"].(map[string]any)["created_here"] != true {
		t.Errorf("%d %v", st, out)
	}
	if st, out := g.upload(t, "", []byte("some other backup")); st != 200 || out["encryption"].(map[string]any)["created_here"] != false {
		t.Errorf("%d %v", st, out)
	}
}

func encrypted(t *testing.T, plain string, meta backupcrypt.MetaRecipient, recipients ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := backupcrypt.Encrypt(&buf, meta, recipients...)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(plain))
	_ = w.Close()
	return buf.Bytes()
}

func TestRestoreCheckAndDecrypt(t *testing.T) {
	g := newCryptRig(t)
	sec := g.setup(t)
	boxID, _, _ := g.store.BoxIdentity()
	box := boxID.Recipient().String()
	meta := backupcrypt.MetaRecipient{BoxFingerprint: backupcrypt.Fingerprint(box), RecoveryFingerprint: sec.Fingerprint()}
	dir := string(g.root) + system.BackupDir

	// a plain .sbk behaves exactly as today
	st, out := g.upload(t, "ccu-1.sbk", []byte("plain sbk"))
	enc := out["encryption"].(map[string]any)
	if st != 200 || out["file"] != "restore-ccu-1.sbk" || out["check"] == nil || enc["encrypted"] != false || enc["needs_recovery_key"] != false {
		t.Fatalf("%d %v", st, out)
	}

	// a file the system's own key opens: decrypted on the way in, the check runs
	st, out = g.upload(t, "ccu-2.sbk.age", encrypted(t, "sbk two", meta, box, sec.Recipient()))
	enc = out["encryption"].(map[string]any)
	if st != 200 || out["file"] != "restore-ccu-2.sbk" || out["check"] == nil || enc["opened_with"] != "box" || enc["known"] != "current" || enc["recovery_fingerprint"] != sec.Fingerprint() || enc["needs_recovery_key"] != false {
		t.Fatalf("%d %v", st, out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "restore-ccu-2.sbk")); err != nil || string(b) != "sbk two" {
		t.Errorf("decrypted content: %v %q", err, b)
	}

	// a file for the recovery key only (another system's): stored encrypted, no check yet
	st, out = g.upload(t, "other-3.sbk.age", encrypted(t, "sbk three", backupcrypt.MetaRecipient{RecoveryFingerprint: sec.Fingerprint()}, sec.Recipient()))
	enc = out["encryption"].(map[string]any)
	if st != 200 || out["file"] != "restore-other-3.sbk.age" || out["check"] != nil || enc["needs_recovery_key"] != true || enc["known"] != "current" || enc["opened_with"] != nil {
		t.Fatalf("%d %v", st, out)
	}
	// apply refuses the .age
	if st, out := g.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-other-3.sbk.age"}`); st != 400 {
		t.Errorf("apply of .age: %d %v", st, out)
	}
	// a typo, the wrong key, then the right one
	if st, out := g.do(t, "POST", "/restore/decrypt", `{"file":"restore-other-3.sbk.age","recovery_key":"`+sec.Code()[:12]+`"}`); st != 422 || out["error"] != "invalid-key" {
		t.Errorf("typo: %d %v", st, out)
	}
	other, _ := backupcrypt.NewSecret()
	if st, out := g.do(t, "POST", "/restore/decrypt", `{"file":"restore-other-3.sbk.age","recovery_key":"`+other.Code()+`"}`); st != 422 || out["error"] != "wrong-key" {
		t.Errorf("wrong key: %d %v", st, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "restore-other-3.sbk.age")); err != nil {
		t.Error("the encrypted upload is gone after a refused key")
	}
	if st, out := g.do(t, "POST", "/restore/decrypt", `{"file":"restore-other-3.sbk.age","recovery_key":"`+strings.ToLower(sec.Code())+`"}`); st != 200 || out["file"] != "restore-other-3.sbk" || out["check"] == nil || out["encryption"].(map[string]any)["opened_with"] != "recovery" || out["encryption"].(map[string]any)["known"] != "current" {
		t.Fatalf("decrypt: %d %v", st, out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "restore-other-3.sbk")); err != nil || string(b) != "sbk three" {
		t.Errorf("%v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "restore-other-3.sbk.age")); err == nil {
		t.Error("the encrypted upload was not removed")
	}
	if st, out := g.do(t, "POST", "/restore/decrypt", `{"file":"restore-other-3.sbk.age","recovery_key":"`+sec.Code()+`"}`); st != 404 {
		t.Errorf("decrypt again: %d %v", st, out)
	}
	if st, out := g.do(t, "POST", "/restore/decrypt", `{"file":"../x.sbk.age","recovery_key":"x"}`); st != 400 {
		t.Errorf("bad name: %d %v", st, out)
	}

	// an earlier key's file: known previous, with the key's date
	sec2 := g.setup(t)
	st, out = g.upload(t, "old.sbk.age", encrypted(t, "old", backupcrypt.MetaRecipient{RecoveryFingerprint: sec.Fingerprint()}, sec.Recipient()))
	enc = out["encryption"].(map[string]any)
	if st != 200 || enc["known"] != "previous" || enc["key_created"] == nil {
		t.Errorf("%d %v", st, out)
	}
	_ = sec2
	// a file from the age tool, no meta stanza: unknown
	var buf bytes.Buffer
	w, _ := age.Encrypt(&buf, sec.Identity().Recipient())
	_, _ = w.Write([]byte("tool"))
	_ = w.Close()
	st, out = g.upload(t, "tool.sbk.age", buf.Bytes())
	if enc = out["encryption"].(map[string]any); st != 200 || enc["known"] != "unknown" || enc["needs_recovery_key"] != true {
		t.Errorf("%d %v", st, out)
	}
	// a passphrase file: said as such
	sr, _ := age.NewScryptRecipient("pw")
	sr.SetWorkFactor(10)
	buf.Reset()
	w, _ = age.Encrypt(&buf, sr)
	_, _ = w.Write([]byte("pw"))
	_ = w.Close()
	st, out = g.upload(t, "pw.sbk.age", buf.Bytes())
	if enc = out["encryption"].(map[string]any); st != 200 || enc["passphrase"] != true {
		t.Errorf("%d %v", st, out)
	}
	// a wrong name is tamed, an .age upload without the suffix gets it
	st, out = g.upload(t, "../../etc/passwd", encrypted(t, "x", backupcrypt.MetaRecipient{}, sec.Recipient()))
	if st != 200 || out["file"] != "restore-upload.sbk.age" {
		t.Errorf("%d %v", st, out)
	}
	st, out = g.upload(t, "noext.sbk", encrypted(t, "x", backupcrypt.MetaRecipient{}, sec.Recipient()))
	if st != 200 || out["file"] != "restore-noext.sbk.age" {
		t.Errorf("%d %v", st, out)
	}
}

func TestRestoreCorruptLeavesNothing(t *testing.T) {
	g := newCryptRig(t)
	sec := g.setup(t)
	boxID, _, _ := g.store.BoxIdentity()
	box := boxID.Recipient().String()
	dir := string(g.root) + system.BackupDir
	big := strings.Repeat("0123456789abcdef", 3*4096+7) // three chunks and a bit
	file := encrypted(t, big, backupcrypt.MetaRecipient{}, box, sec.Recipient())
	hdrEnd := bytes.Index(file, []byte("\n--- ")) + 1
	hdrEnd += bytes.IndexByte(file[hdrEnd:], '\n') + 1
	payload := hdrEnd + 16
	cases := map[string][]byte{
		"flipped byte in chunk 3": func() []byte { m := bytes.Clone(file); m[payload+2*(64*1024+16)+5] ^= 1; return m }(),
		"last byte removed":       file[:len(file)-1],
		"last chunk removed":      file[:payload+3*(64*1024+16)],
	}
	for name, m := range cases {
		// opened by the system's key on the way in: corrupt, nothing stored
		st, out := g.upload(t, "bad.sbk.age", m)
		if st != 422 || out["error"] != "corrupt" {
			t.Errorf("%s: %d %v", name, st, out)
		}
		// for the recovery key only: stored, then corrupt at the decryption, the partial output gone
		st, out = g.upload(t, "bad2.sbk.age", bytes.Replace(m, []byte("-> X25519"), []byte("-> X25518"), 1))
		if st != 200 || out["file"] != "restore-bad2.sbk.age" {
			t.Fatalf("%s: %d %v", name, st, out)
		}
		st, out = g.do(t, "POST", "/restore/decrypt", `{"file":"restore-bad2.sbk.age","recovery_key":"`+sec.Code()+`"}`)
		if st != 422 || out["error"] != "corrupt" || out["detail"].(map[string]any)["upload_removed"] != true {
			t.Errorf("%s: decrypt %d %v", name, st, out)
		}
		// openccu-lite B-194: the damaged upload itself is gone too, not only the partial output
		for _, d := range []string{dir, string(g.root) + system.StagingDir} {
			entries, _ := os.ReadDir(d)
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".sbk") || strings.HasSuffix(e.Name(), ".sbk.age") || strings.HasSuffix(e.Name(), ".part") {
					t.Errorf("%s: %s left in %s", name, e.Name(), d)
				}
			}
		}
	}
}

// openccu-lite B-194: the wrong recovery key keeps the upload for the next try; a new upload
// sweeps what an earlier one left behind, so at most one restore-* file waits under BackupDir.
func TestRestoreUploadsSweptAndWrongKeyKeeps(t *testing.T) {
	g := newCryptRig(t)
	sec := g.setup(t)
	dir := string(g.root) + system.BackupDir
	other, _ := backupcrypt.NewSecret()
	foreign := encrypted(t, "foreign", backupcrypt.MetaRecipient{}, other.Recipient())
	if st, out := g.upload(t, "foreign.sbk.age", foreign); st != 200 || out["file"] != "restore-foreign.sbk.age" {
		t.Fatalf("%d %v", st, out)
	}
	// the wrong key: refused, the file stays
	if st, out := g.do(t, "POST", "/restore/decrypt", `{"file":"restore-foreign.sbk.age","recovery_key":"`+sec.Code()+`"}`); st != 422 || out["error"] != "wrong-key" {
		t.Fatalf("%d %v", st, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "restore-foreign.sbk.age")); err != nil {
		t.Fatal("the upload did not wait for another key:", err)
	}
	// the next upload sweeps it, and the carry file beside them is not touched
	if err := os.WriteFile(string(g.root)+system.CarryFile, []byte("AGE-SECRET-KEY-1KEEP\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, out := g.upload(t, "plain.sbk", []byte("sbk")); st != 200 || out["file"] != "restore-plain.sbk" {
		t.Fatalf("%d %v", st, out)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != ".occulite-box-identity" || names[1] != "restore-plain.sbk" {
		t.Errorf("under %s: %v", system.BackupDir, names)
	}
}

func TestRestoreApplyCarriesBoxIdentity(t *testing.T) {
	g := newCryptRig(t)
	dir := string(g.root) + system.BackupDir
	_ = os.WriteFile(filepath.Join(dir, "restore-1.sbk"), []byte("sbk"), 0o644)
	// without a setup there is no identity and nothing is carried
	if st, _ := g.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-1.sbk"}`); st != 200 {
		t.Fatal(st)
	}
	if _, err := os.Stat(string(g.root) + system.CarryFile); err == nil {
		t.Error("carried without an identity")
	}
	g.setup(t)
	boxID, _, _ := g.store.BoxIdentity()
	// openccu-lite B-194: the identity's creation time travels with it (the restore boot's clock,
	// before NTP, may be months off)
	made := time.Date(2026, 9, 24, 7, 30, 38, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(g.store.Dir, backupcrypt.KeyDir, backupcrypt.IdentityFile), made, made); err != nil {
		t.Fatal(err)
	}
	if st, out := g.do(t, "POST", "/restore/apply", `{"confirm":true,"file":"restore-1.sbk"}`); st != 200 {
		t.Fatal(st, out)
	}
	b, err := os.ReadFile(string(g.root) + system.CarryFile)
	if err != nil || string(b) != boxID.String()+"\n" {
		t.Fatalf("carry file: %v %q", err, b)
	}
	if st, _ := os.Stat(string(g.root) + system.CarryFile); st.Mode().Perm() != 0o600 || !st.ModTime().Equal(made) {
		t.Errorf("mode %v mtime %v", st.Mode(), st.ModTime())
	}
	// the next start on a restored userfs: no identity of its own, the carried one is adopted with
	// its creation time and the file removed
	fresh := &backupcrypt.Store{Dir: t.TempDir()}
	if adopted, err := g.root.AdoptCarriedBoxIdentity(fresh); err != nil || !adopted {
		t.Fatal(adopted, err)
	}
	if id, created, ok := fresh.BoxIdentity(); !ok || id.String() != boxID.String() || !created.Equal(made) {
		t.Errorf("not the same identity, or created %v", created)
	}
	if _, err := os.Stat(string(g.root) + system.CarryFile); err == nil {
		t.Error("carry file not removed")
	}
	if adopted, err := g.root.AdoptCarriedBoxIdentity(fresh); err != nil || adopted {
		t.Error("a second start adopted again", err)
	}
	// a start with an identity of its own ignores a carried one and removes it
	_ = os.WriteFile(string(g.root)+system.CarryFile, []byte("AGE-SECRET-KEY-1GARBAGE\n"), 0o600)
	if adopted, err := g.root.AdoptCarriedBoxIdentity(g.store); err != nil || adopted {
		t.Error("adopted over its own", err)
	}
	if _, err := os.Stat(string(g.root) + system.CarryFile); err == nil {
		t.Error("stale carry file kept")
	}
}

func TestBackupUnencryptedWarning(t *testing.T) {
	g := newCryptRig(t)
	ws, _ := g.api.backupUnencryptedWarning(context.Background())
	if len(ws) != 1 || ws[0].ID != "backup-unencrypted" || ws[0].Variant != "plain" || ws[0].Href != "/backup#encryption" {
		t.Errorf("no key: %v", ws)
	}
	g.setup(t)
	if ws, _ := g.api.backupUnencryptedWarning(context.Background()); len(ws) != 0 {
		t.Errorf("encrypted: %v", ws)
	}
	_, _ = g.do(t, "PUT", "/backup/encryption", `{"enabled":false}`)
	if ws, _ := g.api.backupUnencryptedWarning(context.Background()); len(ws) != 1 {
		t.Errorf("switched off: %v", ws)
	}
	_ = os.MkdirAll(string(g.root)+"/etc/config", 0o755)
	_ = os.WriteFile(string(g.root)+"/etc/config/NoCronBackup", nil, 0o644)
	// occulited task 88: the warning is the Backup page's only notice now, so it no longer waits
	// for a nightly run or a network target - a download is unencrypted too
	if ws, _ := g.api.backupUnencryptedWarning(context.Background()); len(ws) != 1 {
		t.Errorf("no nightly, switched off: %v", ws)
	}
	g.api.BackupCrypt = nil
	if st, _ := g.do(t, "GET", "/backup/encryption", ""); st != 501 {
		t.Errorf("without the store: %d", st)
	}
	if ws, ok := g.api.backupUnencryptedWarning(context.Background()); len(ws) != 0 || !ok {
		t.Errorf("without encryption support: %v %v", ws, ok)
	}
}
