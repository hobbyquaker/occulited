package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/acme"
	"github.com/hobbyquaker/occulited/internal/certpem"
	"github.com/hobbyquaker/occulited/internal/certpem/testcert"
	"github.com/hobbyquaker/occulited/internal/system"
)

type stubIssuer struct{ names []string }

func (s *stubIssuer) Issue(_ context.Context, req acme.Request) (*acme.Result, error) {
	s.names = req.Names
	leaf, key, ca := testcert.Issued(req.Names, time.Now().Add(90*24*time.Hour))
	return &acme.Result{Certificate: append(leaf, ca...), PrivateKey: key, Registration: json.RawMessage(`{"uri":"https://ca/acct/7"}`)}, nil
}

// stubInstaller keeps the live certificate under a mutex: the ACME service installs on its own
// goroutine while a handler reads the live one (occulited B-13, found by -race).
type stubInstaller struct {
	mu   sync.Mutex
	live []byte
	mode string
}

func (s *stubInstaller) Install(_ context.Context, pem []byte, mode string, _ func(string)) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = pem
	s.mode = mode
	return []string{"mosquitto"}, nil
}
func (s *stubInstaller) Remove(context.Context, func(string)) ([]string, error) {
	cert, key := testcert.SelfSigned([]string{"openccu"}, nil, time.Now().Add(time.Hour))
	s.set(append(cert, key...), s.getMode())
	return []string{"mosquitto"}, nil
}
func (s *stubInstaller) ReadLive() ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	marker := ""
	if s.mode != "" {
		marker = s.mode + " CN=Test CA"
	}
	return certpem.CertificatesOnly(s.live), marker, nil
}

func (s *stubInstaller) set(live []byte, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live, s.mode = live, mode
}

func (s *stubInstaller) getMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func (s *stubInstaller) hasLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live != nil
}

func TestCertificateRoutes(t *testing.T) {
	r := fakeRoot(t)
	svc := scriptBox{system.AddonScripts{Root: r}}
	mux := http.NewServeMux()
	// without the service the routes say so
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc}).Register(mux)
	srv := httptest.NewServer(mux)
	if st, _, _ := do(t, srv, "GET", "/api/system/v1/certificate", "", nil); st != 501 {
		t.Fatalf("no service: %d", st)
	}
	srv.Close()

	cs, err := acme.New(filepath.Join(t.TempDir(), "acme"), nil)
	if err != nil {
		t.Fatal(err)
	}
	inst := &stubInstaller{}
	_, _ = inst.Remove(context.Background(), nil) // a self-signed live file to start with
	iss := &stubIssuer{}
	cs.Issuer, cs.Installer = iss, inst
	mux = http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc, Cert: cs}).Register(mux)
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st, out, _ := do(t, srv, "GET", "/api/system/v1/certificate", "", nil)
	if st != 200 || out["settings"].(map[string]any)["mode"] != "self-signed" || out["current"].(map[string]any)["self_signed"] != true || out["issued"] != nil {
		t.Fatalf("%d %v", st, out)
	}
	if len(out["providers"].([]any)) != len(acme.Providers) {
		t.Fatalf("providers: %v", out["providers"])
	}
	// invalid settings: 422 with the reason
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/certificate/settings", `{"mode":"acme","directory":"letsencrypt","names":["192.0.2.119"],"challenge":"http-01"}`, nil)
	if st != 422 || !strings.Contains(out["message"].(string), "IP address") {
		t.Fatalf("%d %v", st, out)
	}
	// an unknown field is refused like everywhere else
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/certificate/settings", `{"mode":"acme","bogus":1}`, nil); st != 422 {
		t.Fatalf("unknown field: %d", st)
	}
	// valid: saved and answered without the secrets; the firewall follows (task 175: ACME with
	// HTTP-01 owns a rule for port 80), a refused save does not
	synced := 0
	oldFC := system.FirewallChanged
	system.FirewallChanged = func(context.Context) { synced++ }
	t.Cleanup(func() { system.FirewallChanged = oldFC })
	body := `{"mode":"acme","directory":"letsencrypt","email":"me@example.org","names":["box.example.org"],"challenge":"dns-01","dns_provider":"cloudflare","dns_credentials":{"api_token":"cf-secret"},"eab_kid":"","eab_hmac":""}`
	st, out, raw := do(t, srv, "PUT", "/api/system/v1/certificate/settings", body, nil)
	if st != 200 || strings.Contains(string(raw), "cf-secret") {
		t.Fatalf("%d %s", st, raw)
	}
	set := out["settings"].(map[string]any)
	if set["mode"] != "acme" || set["dns_secrets_set"].(map[string]any)["api_token"] != true || set["dns_provider"] != "cloudflare" {
		t.Fatalf("%v", set)
	}
	if synced != 1 {
		t.Errorf("the firewall was told %d times, want 1", synced)
	}
	// issue: 202, then the status shows the result
	st, out, _ = do(t, srv, "POST", "/api/system/v1/certificate/issue", "{}", nil)
	if st != 202 {
		t.Fatalf("%d %v", st, out)
	}
	var last map[string]any
	for i := 0; i < 200; i++ {
		_, out, _ = do(t, srv, "GET", "/api/system/v1/certificate", "", nil)
		if out["running"] == nil && out["last"] != nil {
			last = out["last"].(map[string]any)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if last == nil || last["ok"] != true || last["kind"] != "issue" || last["installed"] != true || out["live_is_issued"] != true {
		t.Fatalf("%v", out)
	}
	if iss.names[0] != "box.example.org" || !inst.hasLive() {
		t.Fatal("the issuer was not asked, or nothing installed")
	}
	// renew and test start the same way; an unknown action is not a route
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/certificate/renew", "{}", nil); st != 202 {
		t.Fatalf("renew: %d", st)
	}
	for i := 0; i < 200; i++ {
		_, out, _ = do(t, srv, "GET", "/api/system/v1/certificate", "", nil)
		if out["running"] == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st, _, _ := do(t, srv, "POST", "/api/system/v1/certificate/revoke", "{}", nil); st != 404 && st != 405 {
		t.Fatalf("revoke: %d", st)
	}
	// back to self-signed: synchronous, the restarted addons named, the mode flipped
	st, out, _ = do(t, srv, "POST", "/api/system/v1/certificate/self-signed", "{}", nil)
	if st != 200 || out["restarted_addons"].([]any)[0] != "mosquitto" || out["status"].(map[string]any)["settings"].(map[string]any)["mode"] != "self-signed" {
		t.Fatalf("%d %v", st, out)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/certificate/issue", "{}", nil); st != 422 || !strings.Contains(out["message"].(string), "self-signed") {
		t.Fatalf("issue while self-signed: %d %v", st, out)
	}
}

// TestChallengeRouteIsOpen: the HTTP-01 path needs no session, and answers only live tokens.
func TestChallengeRouteIsOpen(t *testing.T) {
	if !open(acme.ChallengePrefix+"abc") || open("/.well-known/other") {
		t.Fatal("open()")
	}
}

// TestCertificateManualRoutes (task 38): the key and CSR, the CSR download, the install as JSON
// with the pending key and as multipart with DER files, the preview, the refusals as 422.
func TestCertificateManualRoutes(t *testing.T) {
	r := fakeRoot(t)
	svc := scriptBox{system.AddonScripts{Root: r}}
	cs, err := acme.New(filepath.Join(t.TempDir(), "acme"), nil)
	if err != nil {
		t.Fatal(err)
	}
	inst := &stubInstaller{}
	_, _ = inst.Remove(context.Background(), nil)
	cs.Issuer, cs.Installer = &stubIssuer{}, inst
	mux := http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc, Cert: cs}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// no request yet: 404
	if st, _, _ := do(t, srv, "GET", "/api/system/v1/certificate/csr", "", nil); st != 404 {
		t.Fatalf("csr before generate: %d", st)
	}
	// generate: the CSR in the answer and in the status
	st, out, _ := do(t, srv, "POST", "/api/system/v1/certificate/key", `{"algorithm":"p256","cn":"ccu.example.org","sans":["ccu.lan"],"org":""}`, nil)
	if st != 200 || !strings.HasPrefix(out["csr"].(string), "-----BEGIN CERTIFICATE REQUEST-----") {
		t.Fatalf("%d %v", st, out)
	}
	pending := out["pending"].(map[string]any)
	if pending["cn"] != "ccu.example.org" || pending["algorithm"] != "P-256" {
		t.Fatalf("%v", pending)
	}
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/certificate/key", `{"cn":""}`, nil); st != 422 || !strings.Contains(out["message"].(string), "common name") {
		t.Fatalf("empty cn: %d %v", st, out)
	}
	// the download: the PEM as a file
	res, err := http.Get(srv.URL + "/api/system/v1/certificate/csr")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Disposition") != `attachment; filename="ccu.example.org.csr"` || res.Header.Get("Content-Type") != "application/pkcs10" || string(body) != out["csr"].(string) {
		t.Fatalf("%d %v %s", res.StatusCode, res.Header, body)
	}
	csr, err := certpem.ParseCSR(body)
	if err != nil {
		t.Fatal(err)
	}
	leaf, ca := testcert.Sign(csr, time.Now().Add(90*24*time.Hour))
	// the preview: the leaf parsed, the pending key matches, the chain warns
	st, out, _ = do(t, srv, "POST", "/api/system/v1/certificate/inspect", jsonBody(map[string]string{"certificate": string(leaf)}), nil)
	if st != 200 || out["certificate"].(map[string]any)["subject"] != "CN=ccu.example.org" || out["pending_matches"] != true || out["chain_warning"] == nil {
		t.Fatalf("%d %v", st, out)
	}
	// install as JSON with the pending key: the mode is manual, the marker says so
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/certificate/manual", jsonBody(map[string]string{"certificate": string(leaf), "chain": string(ca)}), nil)
	if st != 200 || out["restarted_addons"].([]any)[0] != "mosquitto" || out["warning"] != "" {
		t.Fatalf("%d %v", st, out)
	}
	status := out["status"].(map[string]any)
	if status["settings"].(map[string]any)["mode"] != "manual" || status["managed_mode"] != "manual" || status["pending"] != nil || inst.getMode() != "manual" {
		t.Fatalf("%v", status)
	}
	if st, out, _ := do(t, srv, "GET", "/api/system/v1/certificate", "", nil); st != 200 || out["current"].(map[string]any)["subject"] != "CN=ccu.example.org" {
		t.Fatalf("%d %v", st, out)
	}
	// a certificate for another key: 422 with the reason
	other, otherKey, _ := testcert.Issued([]string{"other"}, time.Now().Add(time.Hour))
	st, out, _ = do(t, srv, "PUT", "/api/system/v1/certificate/manual", jsonBody(map[string]string{"certificate": string(other)}), nil)
	if st != 422 || !strings.Contains(out["message"].(string), "does not belong") {
		t.Fatalf("%d %v", st, out)
	}
	// multipart with DER files: the certificate, the chain and the key
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	for name, pemBytes := range map[string][]byte{"certificate": other, "chain": ca, "key": otherKey} {
		block, _ := pem.Decode(pemBytes)
		fw, _ := mw.CreateFormFile(name, name+".der")
		_, _ = fw.Write(block.Bytes)
	}
	mw.Close()
	req, _ := http.NewRequest("PUT", srv.URL+"/api/system/v1/certificate/manual", &mp)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var mpOut map[string]any
	_ = json.NewDecoder(res.Body).Decode(&mpOut)
	res.Body.Close()
	if res.StatusCode != 200 || mpOut["result"].(map[string]any)["key_from"] != "upload" || mpOut["result"].(map[string]any)["certificate"].(map[string]any)["subject"] != "CN=other,O=HomeMatic" {
		t.Fatalf("%d %v", res.StatusCode, mpOut)
	}
	// the wrong CA as the chain: installed, with the warning
	if w := mpOut["warning"]; w == nil || !strings.Contains(w.(string), "not in the chain") {
		t.Fatalf("warning: %v", w)
	}
	// an empty body, and an unknown field
	if st, _, _ := do(t, srv, "PUT", "/api/system/v1/certificate/manual", `{"cert":"x"}`, nil); st != 422 {
		t.Fatalf("unknown field: %d", st)
	}
	// the switch back from manual
	if st, out, _ := do(t, srv, "POST", "/api/system/v1/certificate/self-signed", "{}", nil); st != 200 || out["status"].(map[string]any)["settings"].(map[string]any)["mode"] != "self-signed" {
		t.Fatalf("%d %v", st, out)
	}
	// the routes need the service
	mux = http.NewServeMux()
	(&SystemAPI{Root: r, Services: svc, Log: testLog, Addons: svc, Manager: svc, Nav: svc}).Register(mux)
	srv2 := httptest.NewServer(mux)
	t.Cleanup(srv2.Close)
	for _, rt := range [][2]string{{"PUT", "/api/system/v1/certificate/manual"}, {"POST", "/api/system/v1/certificate/key"}, {"GET", "/api/system/v1/certificate/csr"}, {"POST", "/api/system/v1/certificate/inspect"}} {
		if st, _, _ := do(t, srv2, rt[0], rt[1], "{}", nil); st != 501 {
			t.Fatalf("%s %s without the service: %d", rt[0], rt[1], st)
		}
	}
}

func jsonBody(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}
