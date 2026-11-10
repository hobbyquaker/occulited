package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/radio"
	"github.com/hobbyquaker/occulited/internal/system"
)

// The passphrases of these tests; none of them may appear in a log line, an answer or a file.
const (
	backupPass = "Backup_Pass296"
	wrongPass  = "Wrong_Pass296"
	systemPass = "System_Pass296"
)

// signFor is crypttool's signature (system.BackupSignature's comment; TestBackupSignature anchors
// it on crypttool's own output): the MD5 of the data, encrypted with the MD5 of the passphrase.
func signFor(passphrase string, data []byte) string {
	key := md5.Sum([]byte(passphrase))
	sum := md5.Sum(data)
	block, _ := aes.NewCipher(key[:])
	var out [16]byte
	block.Encrypt(out[:], sum[:])
	return hex.EncodeToString(out[:])
}

// writeSignedSBK is writeSBK with a signature made with passphrase under keyIndex.
func writeSignedSBK(t *testing.T, root system.Root, name string, files map[string]string, passphrase, keyIndex string) {
	t.Helper()
	var inner bytes.Buffer
	gz := gzip.NewWriter(&inner)
	tw := tar.NewWriter(gz)
	for n, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	var outer bytes.Buffer
	ow := tar.NewWriter(&outer)
	for _, m := range []struct{ n, body string }{{"usr_local.tar.gz", inner.String()}, {"signature", signFor(passphrase, inner.Bytes()) + "\n"}, {"key_index", keyIndex + "\n"}, {"firmware_version", "VERSION=3.89.11\n"}} {
		_ = ow.WriteHeader(&tar.Header{Name: m.n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(m.body))})
		_, _ = ow.Write([]byte(m.body))
	}
	_ = ow.Close()
	dir := filepath.Join(string(root), system.BackupDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), outer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureLogs sends slog's default logger into a buffer for the test.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	logs := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return logs
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// jsonString is v as JSON, for the passphrase search.
func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// noPassphrase fails the test when one of the passphrases is in s.
func noPassphrase(t *testing.T, what, s string) {
	t.Helper()
	for _, p := range []string{backupPass, wrongPass, systemPass} {
		if strings.Contains(s, p) {
			t.Errorf("%s holds a passphrase (%s): %s", what, p, s)
		}
	}
}

// keyRig is a power rig whose SystemAPI has the restore's runners and the system key's seam.
type keyRig struct {
	*powerRig
	api *SystemAPI
	// systemKey is this system's own passphrase, "" = the factory key
	systemKey string
	// stdin and args are the restore script's last input and arguments; scriptErr its failure
	stdin     string
	args      []string
	scriptErr error
}

func newKeyRig(t *testing.T) *keyRig {
	t.Helper()
	rig := &keyRig{powerRig: newPowerRig(t, true)}
	api := &SystemAPI{Root: rig.root, Manager: rig.m, Power: rig.power,
		RunStdin: func(_ context.Context, in []byte, _ string, args ...string) ([]byte, error) {
			rig.stdin, rig.args = string(in), args
			if rig.scriptErr != nil {
				return []byte("   backup protected with a key, WARNING: security key does NOT match key in backup, aborting"), rig.scriptErr
			}
			return []byte("4) Scheduling backup restore for next boot cycle, OK"), nil
		},
		SystemKeyCheck: func(_ context.Context, pass string) (bool, bool, bool) {
			return rig.systemKey != "", rig.systemKey != "" && pass == rig.systemKey, true
		},
	}
	rig.api = api
	mux := http.NewServeMux()
	api.Register(mux)
	rig.srv.Close()
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, &auth.Session{ID: "s", User: "u", Role: rig.role, Scopes: auth.RoleScopes(rig.role)})))
	}))
	t.Cleanup(rig.srv.Close)
	return rig
}

// TestRestoreVerifyKey: the passphrase check of openccu-lite task 296 - match, mismatch, skip, on
// the backup's side and this system's - and the refusals of the route.
func TestRestoreVerifyKey(t *testing.T) {
	logs := captureLogs(t)
	rig := newKeyRig(t)
	writeSignedSBK(t, rig.root, "restore-key.sbk", ccuBackup, backupPass, "2")
	writeSignedSBK(t, rig.root, "restore-plain.sbk", ccuBackup, "the factory key stands in", "0")
	verify := func(file, key string) (int, map[string]any) {
		t.Helper()
		st, _, out := rig.do(t, "POST", "/restore/verify-key", `{"file":"`+file+`","key":"`+key+`"}`)
		return st, out
	}
	for _, c := range []struct {
		name, file, key, sysKey, backup, system string
	}{
		{"match, this system on the factory key", "restore-key.sbk", backupPass, "", "match", "none"},
		{"match, this system with another key", "restore-key.sbk", backupPass, systemPass, "match", "mismatch"},
		{"match on both sides", "restore-key.sbk", backupPass, backupPass, "match", "match"},
		{"mismatch", "restore-key.sbk", wrongPass, "", "mismatch", "none"},
		{"the system's passphrase is not the backup's", "restore-key.sbk", systemPass, systemPass, "mismatch", "match"},
		{"skipped", "restore-key.sbk", "", systemPass, "skipped", "skipped"},
		{"a backup on the factory key", "restore-plain.sbk", wrongPass, "", "none", "none"},
	} {
		rig.systemKey = c.sysKey
		st, out := verify(c.file, c.key)
		if st != 200 || out["backup"] != c.backup || out["system"] != c.system {
			t.Errorf("%s: %d %v", c.name, st, out)
		}
		if c.file == "restore-key.sbk" && out["key_index"] != 2.0 {
			t.Errorf("%s: key_index %v", c.name, out["key_index"])
		}
	}
	// crypttool could not say (a development root, the helper down): unknown, not a mismatch
	rig.api.SystemKeyCheck = func(context.Context, string) (bool, bool, bool) { return false, false, false }
	if st, out := verify("restore-key.sbk", backupPass); st != 200 || out["system"] != "unknown" || out["backup"] != "match" {
		t.Errorf("unknown system key: %d %v", st, out)
	}
	if st, _ := verify("../etc/passwd", backupPass); st != 400 {
		t.Errorf("bad name: %d", st)
	}
	if st, _ := verify("restore-nope.sbk", backupPass); st != 404 {
		t.Errorf("missing: %d", st)
	}
	_ = os.WriteFile(filepath.Join(string(rig.root), system.BackupDir, "restore-junk.sbk"), []byte("not a backup"), 0o600)
	if st, _, out := rig.do(t, "POST", "/restore/verify-key", `{"file":"restore-junk.sbk","key":"`+backupPass+`"}`); st != 422 || out["error"] != "not_a_backup" {
		t.Errorf("not a backup: %d %v", st, out)
	}
	rig.role = auth.RoleUser
	if st, _ := verify("restore-key.sbk", backupPass); st != 403 {
		t.Errorf("a user: %d", st)
	}
	rig.role = auth.RoleAdmin
	noPassphrase(t, "the journal", logs.String())
	if !strings.Contains(logs.String(), "backup=match") || !strings.Contains(logs.String(), "backup=mismatch") {
		t.Errorf("the verdicts are not in the journal: %s", logs.String())
	}
}

// TestRestoreApplyKey: the restore takes the passphrase along. A matching one reaches the
// script; without the backup's passphrase the restore still runs on force (never blocked), the
// script gets a random placeholder instead of the wrong word, and the answer says the verdict.
func TestRestoreApplyKey(t *testing.T) {
	logs := captureLogs(t)
	rig := newKeyRig(t)
	writeSignedSBK(t, rig.root, "restore-key.sbk", liteOf(ccuBackup), backupPass, "2")
	apply := func(body string) (int, apiError, map[string]any) {
		t.Helper()
		st, e, out := rig.do(t, "POST", "/restore/apply", body)
		return st, e, out
	}
	check := func(name string, st int, out map[string]any, backup string) {
		t.Helper()
		kc, _ := out["key_check"].(map[string]any)
		if st != 200 || out["rebooting"] != true || kc["backup"] != backup {
			t.Errorf("%s: %d %v", name, st, out)
		}
	}
	st, _, out := apply(`{"confirm":true,"file":"restore-key.sbk","key":"` + backupPass + `"}`)
	check("match", st, out, "match")
	if rig.stdin != backupPass+"\n" || strings.Join(rig.args, " ") != filepath.Join(string(rig.root), system.BackupDir, "restore-key.sbk") {
		t.Errorf("match: the script got %q %v", rig.stdin, rig.args)
	}
	placeholders := map[string]bool{}
	for _, c := range []struct{ name, key, want string }{{"mismatch", wrongPass, "mismatch"}, {"skipped", "", "skipped"}} {
		st, _, out := apply(`{"confirm":true,"file":"restore-key.sbk","key":"` + c.key + `","force":true}`)
		check(c.name, st, out, c.want)
		got := strings.TrimSuffix(rig.stdin, "\n")
		if len(got) != 24 || strings.Trim(got, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" || got == c.key {
			t.Errorf("%s: the script got %q, not a placeholder", c.name, rig.stdin)
		}
		placeholders[got] = true
		if rig.args[0] != "-f" {
			t.Errorf("%s: no force: %v", c.name, rig.args)
		}
	}
	if len(placeholders) != 2 {
		t.Errorf("the placeholder is not random: %v", placeholders)
	}
	// a wrong passphrase without force: the script refuses, as before; the answer names no key
	rig.scriptErr = errors.New("exit status 1")
	st, e, out := apply(`{"confirm":true,"file":"restore-key.sbk","key":"` + wrongPass + `"}`)
	if st != 422 || e.Error != "restore-failed" || rig.stdin != wrongPass+"\n" {
		t.Errorf("without force: %d %+v %q", st, e, rig.stdin)
	}
	noPassphrase(t, "the refusal", e.Message)
	noPassphrase(t, "the refusal's body", jsonString(out))
	noPassphrase(t, "the journal", logs.String())
	if !strings.Contains(logs.String(), "key_backup=skipped") {
		t.Errorf("the verdict is not in the journal: %s", logs.String())
	}
}

// TestRestoreDevicesKeyCheck: the device import takes the passphrase as a check (task 296): a
// wrong one does not stop it, the verdict is in the answer and the record, and neither the record
// file nor the journal holds the passphrase.
func TestRestoreDevicesKeyCheck(t *testing.T) {
	for _, c := range []struct{ name, key, want string }{
		{"match", backupPass, system.KeyCheckMatch},
		{"mismatch", wrongPass, system.KeyCheckMismatch},
		{"skipped", "", system.KeyCheckSkipped},
	} {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLogs(t)
			rig := newKeyRig(t)
			root := string(rig.root)
			files := map[string]string{}
			for k, v := range ccuBackup {
				files[k] = v
			}
			files["usr/local/etc/config/keys"] = "the backup's key store"
			writeSignedSBK(t, rig.root, "restore-key.sbk", files, backupPass, "1")
			old := system.PairedDevicesTimeout
			system.PairedDevicesTimeout = 2e9
			t.Cleanup(func() { system.PairedDevicesTimeout = old })
			hmipFree := listDevicesStub(t, "HmIP-RFUSB")
			rig.api.ImportRecord = &system.ImportRecord{Path: filepath.Join(root, "state/devices-import.json"), Root: rig.root, Plan: func() (radio.Plan, bool) { return radio.Plan{}, false }}
			rig.api.RadioInterfaces = func() []interfaces.Interface {
				return interfaces.FromList([]struct{ Name, URL string }{{"HmIP-RF", "xmlrpc://" + strings.TrimPrefix(hmipFree.URL, "http://")}})
			}
			st, e, out := rig.do(t, "POST", "/restore/import-devices", `{"confirm":true,"file":"restore-key.sbk","key":"`+c.key+`"}`)
			if st != 200 || out["key_check"] != c.want || out["rebooting"] != true {
				t.Fatalf("import: %d %+v %v", st, e, out)
			}
			rec := rig.api.ImportRecord.Read()
			if rec == nil || rec.BidCosRF.KeyCheck != c.want || !rec.BidCosRF.NonDefaultKey {
				t.Errorf("record: %+v", rec)
			}
			raw, _ := os.ReadFile(rig.api.ImportRecord.Path)
			noPassphrase(t, "the record", string(raw))
			noPassphrase(t, "the answer", jsonString(out))
			noPassphrase(t, "the journal", logs.String())
			if !strings.Contains(logs.String(), "key_check="+c.want) {
				t.Errorf("the verdict is not in the journal: %s", logs.String())
			}
		})
	}
}

// TestCheckBackupKeyIndex: the check answers the key prompt (stdin, -c -f) and adds the backup's
// key index when a key is involved.
func TestCheckBackupKeyIndex(t *testing.T) {
	rig := newKeyRig(t)
	writeSignedSBK(t, rig.root, "restore-key.sbk", ccuBackup, backupPass, "3")
	path := filepath.Join(string(rig.root), system.BackupDir, "restore-key.sbk")
	rig.api.RunStdin = func(_ context.Context, in []byte, _ string, args ...string) ([]byte, error) {
		rig.stdin, rig.args = string(in), args
		return []byte("   backup and/or system protected by security key...\n   system NOT protected with a key, OK\n   backup protected with a key, WARNING: security key does NOT match key in backup, forced restore.\n"), nil
	}
	c := rig.api.checkBackup(context.Background(), path)
	if !c.OK || !c.BackupKey || c.SystemKey || c.KeyIndex != 3 || rig.stdin != "\n" || strings.Join(rig.args, " ") != "-c -f "+path {
		t.Errorf("%+v %q %v", c, rig.stdin, rig.args)
	}
	// a runner without stdin (the recorder of other tests) is adapted
	rig.api.RunStdin = nil
	var ran []string
	rig.api.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		return []byte("   backup or system NOT protected by security key, OK"), nil
	}
	if c := rig.api.checkBackup(context.Background(), path); !c.OK || c.NeedsKey || c.KeyIndex != 0 || len(ran) != 1 || ran[0] != "-c -f "+path {
		t.Errorf("plain runner: %+v %v", c, ran)
	}
}
