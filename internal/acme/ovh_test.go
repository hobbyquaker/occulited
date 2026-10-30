package acme

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
)

// fakeOVH is OVH's API as far as the provider uses it: /auth/time, the zone list, record create
// and delete, refresh. Every authenticated request's signature is recomputed from the known
// secret and checked, with the timestamp against the fake's own clock.
type fakeOVH struct {
	t                 *testing.T
	srv               *httptest.Server
	appKey, appSecret string
	consumer          string
	serverTime        time.Time
	zones             []string

	mu        sync.Mutex
	nextID    int64
	records   map[string]map[int64]ovhRecord // zone -> id -> record
	refreshes map[string]int
	calls     []string
	timeCalls int
	// failWith answers every authenticated call with this status and body
	failStatus int
	failBody   string
}

func newFakeOVH(t *testing.T) *fakeOVH {
	f := &fakeOVH{t: t, appKey: "ak-visible", appSecret: "as-secret-1234", consumer: "ck-secret-5678",
		serverTime: time.Unix(1_800_000_000, 0), zones: []string{"example.org", "other.net", "sub.example.org"}, nextID: 1000,
		records: map[string]map[int64]ovhRecord{}, refreshes: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOVH) provider(t *testing.T) *ovhProvider {
	p, err := newOVHProvider(map[string]string{"endpoint": "ovh-eu", "application_key": f.appKey, "application_secret": f.appSecret, "consumer_key": f.consumer})
	if err != nil {
		t.Fatal(err)
	}
	p.base = f.srv.URL + "/1.0"
	p.http = f.srv.Client()
	// the box's clock is an hour and a bit off: only /auth/time makes the signature valid
	p.now = func() time.Time { return f.serverTime.Add(65 * time.Minute) }
	p.findZone = func(fqdn string) (string, error) { return "", errors.New("no SOA in the test") }
	return p
}

func (f *fakeOVH) fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (f *fakeOVH) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/1.0")
	f.calls = append(f.calls, r.Method+" "+path)
	if path == "/auth/time" && r.Method == http.MethodGet {
		f.timeCalls++
		if r.Header.Get("X-Ovh-Signature") != "" {
			f.t.Error("/auth/time is called unsigned")
		}
		fmt.Fprint(w, f.serverTime.Unix())
		return
	}
	// the signature, as OVH checks it
	if r.Header.Get("X-Ovh-Application") != f.appKey {
		f.fail(w, http.StatusBadRequest, "Invalid application key")
		return
	}
	ts, _ := strconv.ParseInt(r.Header.Get("X-Ovh-Timestamp"), 10, 64)
	if d := ts - f.serverTime.Unix(); d < -5 || d > 5 {
		f.fail(w, http.StatusBadRequest, "Invalid timestamp")
		return
	}
	if r.Header.Get("X-Ovh-Consumer") != f.consumer {
		f.fail(w, http.StatusForbidden, "This credential does not exist")
		return
	}
	if want := ovhSignature(f.appSecret, f.consumer, r.Method, f.srv.URL+r.URL.Path, body, ts); r.Header.Get("X-Ovh-Signature") != want {
		f.fail(w, http.StatusBadRequest, "Invalid signature")
		return
	}
	if f.failStatus != 0 {
		w.WriteHeader(f.failStatus)
		fmt.Fprint(w, f.failBody)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/domain/zone"), "/") // "", zone, "record"|"refresh", id
	switch {
	case path == "/domain/zone" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(f.zones)
	case len(parts) == 3 && parts[2] == "record" && r.Method == http.MethodPost:
		if !f.hasZone(parts[1]) {
			f.fail(w, http.StatusNotFound, "This service does not exist")
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			f.t.Errorf("create without a JSON content type")
		}
		var rec ovhRecord
		if err := json.Unmarshal(body, &rec); err != nil || rec.FieldType != "TXT" || rec.Target == "" {
			f.fail(w, http.StatusBadRequest, "bad record "+string(body))
			return
		}
		f.nextID++
		rec.ID = f.nextID
		if f.records[parts[1]] == nil {
			f.records[parts[1]] = map[int64]ovhRecord{}
		}
		f.records[parts[1]][rec.ID] = rec
		_ = json.NewEncoder(w).Encode(map[string]any{"id": rec.ID, "fieldType": rec.FieldType, "subDomain": rec.SubDomain, "target": rec.Target, "ttl": rec.TTL, "zone": parts[1]})
	case len(parts) == 4 && parts[2] == "record" && r.Method == http.MethodDelete:
		id, _ := strconv.ParseInt(parts[3], 10, 64)
		if _, ok := f.records[parts[1]][id]; !ok {
			f.fail(w, http.StatusNotFound, "The requested object does not exist")
			return
		}
		delete(f.records[parts[1]], id)
		fmt.Fprint(w, "null")
	case len(parts) == 3 && parts[2] == "refresh" && r.Method == http.MethodPost:
		if len(body) != 0 {
			f.t.Errorf("refresh with a body: %q", body)
		}
		f.refreshes[parts[1]]++
		fmt.Fprint(w, "null")
	default:
		f.fail(w, http.StatusNotFound, "Got an invalid (or empty) URL")
	}
}

func (f *fakeOVH) hasZone(z string) bool {
	for _, x := range f.zones {
		if x == z {
			return true
		}
	}
	return false
}

func TestOVHPresentCleanUp(t *testing.T) {
	f := newFakeOVH(t)
	p := f.provider(t)
	// a wildcard and its base name: lego presents both under the same _acme-challenge name, two records
	for _, c := range []struct{ domain, token string }{{"example.org", "tok-wild"}, {"example.org", "tok-base"}, {"box.sub.example.org", "tok-box"}} {
		if err := p.Present(c.domain, c.token, c.token+".keyauth"); err != nil {
			t.Fatalf("present %s: %v", c.domain, err)
		}
	}
	f.mu.Lock()
	if len(f.records["example.org"]) != 2 || len(f.records["sub.example.org"]) != 1 {
		t.Fatalf("records %+v", f.records)
	}
	for _, rec := range f.records["sub.example.org"] {
		// the longest zone wins: sub.example.org, not example.org
		if rec.SubDomain != "_acme-challenge.box" || rec.TTL != dns01.DefaultTTL || rec.Target != dns01.GetChallengeInfo("box.sub.example.org", "tok-box.keyauth").Value {
			t.Fatalf("record %+v", rec)
		}
	}
	for _, rec := range f.records["example.org"] {
		if rec.SubDomain != "_acme-challenge" {
			t.Fatalf("record %+v", rec)
		}
	}
	if f.refreshes["example.org"] != 2 || f.refreshes["sub.example.org"] != 1 {
		t.Fatalf("refreshes %+v", f.refreshes)
	}
	if f.timeCalls != 1 {
		t.Fatalf("/auth/time called %d times, once is enough", f.timeCalls)
	}
	f.mu.Unlock()
	for _, tok := range []string{"tok-wild", "tok-base", "tok-box"} {
		domain := "example.org"
		if tok == "tok-box" {
			domain = "box.sub.example.org"
		}
		if err := p.CleanUp(domain, tok, tok+".keyauth"); err != nil {
			t.Fatalf("cleanup %s: %v", tok, err)
		}
	}
	f.mu.Lock()
	if len(f.records["example.org"])+len(f.records["sub.example.org"]) != 0 || f.refreshes["example.org"] != 4 || f.refreshes["sub.example.org"] != 2 {
		t.Fatalf("after cleanup: %+v %+v", f.records, f.refreshes)
	}
	f.mu.Unlock()
	// a second cleanup (or one after a failed Present) has nothing to delete and calls nothing
	f.mu.Lock()
	n := len(f.calls)
	f.mu.Unlock()
	if err := p.CleanUp("example.org", "tok-wild", "tok-wild.keyauth"); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != n {
		t.Fatalf("calls after the second cleanup: %v", f.calls[n:])
	}
}

func TestOVHZoneBySOA(t *testing.T) {
	f := newFakeOVH(t)
	p := f.provider(t)
	p.findZone = func(fqdn string) (string, error) {
		if fqdn != "_acme-challenge.ccu.other.net." {
			t.Errorf("SOA lookup for %q", fqdn)
		}
		return "other.net.", nil
	}
	if err := p.Present("ccu.other.net", "t", "t.ka"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == "GET /domain/zone" {
			t.Fatal("the zone list was read although DNS answered")
		}
	}
	if len(f.records["other.net"]) != 1 {
		t.Fatalf("%+v", f.records)
	}
}

func TestOVHNoZone(t *testing.T) {
	f := newFakeOVH(t)
	err := f.provider(t).Present("ccu.example.com", "t", "t.ka")
	if err == nil || !strings.Contains(err.Error(), "none of the account's zones") || !strings.Contains(err.Error(), "example.org") {
		t.Fatalf("%v", err)
	}
}

func TestOVHBadCredentials(t *testing.T) {
	for _, c := range []struct {
		name string
		mod  func(*ovhProvider)
		want string
	}{
		{"consumer key", func(p *ovhProvider) { p.consum = "ck-wrong-0000" }, "HTTP 403: This credential does not exist"},
		{"secret", func(p *ovhProvider) { p.appSecret = "as-wrong-0000" }, "HTTP 400: Invalid signature"},
		{"application key", func(p *ovhProvider) { p.appKey = "ak-wrong" }, "HTTP 400: Invalid application key"},
	} {
		f := newFakeOVH(t)
		p := f.provider(t)
		c.mod(p)
		err := p.Present("box.example.org", "t", "t.ka")
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "refused the credentials") {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		var oe *ovhError
		if !errors.As(err, &oe) || !oe.credentials() {
			t.Errorf("%s: not a credentials error: %v", c.name, err)
		}
		// the message never carries a secret, and the run's redaction has nothing to hide in it
		for _, s := range []string{f.appSecret, f.consumer, p.appSecret, p.consum} {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: secret %q in %v", c.name, s, err)
			}
		}
		r := Redactor(Settings{Challenge: ChallengeDNS01, DNSProvider: "ovh", DNSCredentials: map[string]string{"application_secret": p.appSecret, "consumer_key": p.consum}})
		if got := r(err.Error()); got != err.Error() {
			t.Errorf("%s: the redaction changed the message:\n%s\n%s", c.name, err, got)
		}
	}
	// a server error is not reported as a credentials problem
	f := newFakeOVH(t)
	p := f.provider(t)
	p.findZone = func(string) (string, error) { return "example.org.", nil }
	f.failStatus, f.failBody = http.StatusInternalServerError, `{"class":"Server::InternalServerError","message":"Internal server error"}`
	err := p.Present("box.example.org", "t", "t.ka")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500: Server::InternalServerError: Internal server error") || strings.Contains(err.Error(), "credentials") {
		t.Fatalf("500: %v", err)
	}
	// a body that is not JSON is quoted, cut
	f.failStatus, f.failBody = http.StatusBadGateway, strings.Repeat("x", 500)
	if err := p.Present("box.example.org", "t", "t.ka"); err == nil || !strings.Contains(err.Error(), "HTTP 502: xxx") || len(err.Error()) > 400 {
		t.Fatalf("502: %v", err)
	}
}

func TestOVHAuthTimeFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer srv.Close()
	p, _ := newOVHProvider(map[string]string{"application_key": "a", "application_secret": "b", "consumer_key": "c"})
	p.base, p.findZone = srv.URL+"/1.0", func(string) (string, error) { return "example.org.", nil }
	if err := p.Present("box.example.org", "t", "t.ka"); err == nil || !strings.Contains(err.Error(), "the API's time: HTTP 503: down") {
		t.Fatalf("%v", err)
	}
}

func TestOVHSignature(t *testing.T) {
	// the reference: go-ovh's "$1$" + sha1hex(AS+CK+METHOD+URL+BODY+TS)
	got := ovhSignature("AS", "CK", "POST", "https://eu.api.ovh.com/1.0/domain/zone/example.org/refresh", nil, 1366560945)
	// echo -n 'AS+CK+POST+https://eu.api.ovh.com/1.0/domain/zone/example.org/refresh++1366560945' | sha1sum
	if got != "$1$50fc1a8b8e7e7f8fe286c1be577bf2dafa109619" {
		t.Fatal(got)
	}
	if len(got) != 3+40 {
		t.Fatalf("length %d", len(got))
	}
}

func TestOVHConstructor(t *testing.T) {
	full := map[string]string{"application_key": "a", "application_secret": "b", "consumer_key": "c"}
	p, err := newOVHProvider(full)
	if err != nil || p.base != "https://eu.api.ovh.com/1.0" {
		t.Fatalf("default endpoint: %v %v", p, err)
	}
	for ep, base := range map[string]string{"ovh-ca": "https://ca.api.ovh.com/1.0", "ovh-us": "https://api.us.ovhcloud.com/1.0"} {
		full["endpoint"] = ep
		if p, err := newOVHProvider(full); err != nil || p.base != base {
			t.Fatalf("%s: %v", ep, err)
		}
	}
	full["endpoint"] = "kimsufi-eu"
	if _, err := newOVHProvider(full); err == nil {
		t.Fatal("unknown endpoint")
	}
	if _, err := newOVHProvider(map[string]string{"application_key": "a", "application_secret": "b"}); err == nil {
		t.Fatal("no consumer key")
	}
	// through the provider switch, with lego's longer-timeout interface
	prov, err := dnsProvider("ovh", map[string]string{"endpoint": "ovh-eu", "application_key": "a", "application_secret": "b", "consumer_key": "c"})
	if err != nil {
		t.Fatal(err)
	}
	pt, ok := prov.(challenge.ProviderTimeout)
	if !ok {
		t.Fatal("no Timeout")
	}
	if to, iv := pt.Timeout(); to < 5*time.Minute || iv <= 0 {
		t.Fatalf("timeout %v %v", to, iv)
	}
}

// the settings: the endpoint defaults and is a choice, the secrets are stored, never shown, and an
// empty field on save keeps them.
func TestOVHSettings(t *testing.T) {
	u := Update{Mode: ModeACME, Directory: DirLetsEncrypt, Names: []string{"*.example.org", "example.org"}, Challenge: ChallengeDNS01, DNSProvider: "ovh",
		DNSCredentials: map[string]string{"application_key": "ak-1", "application_secret": "as-secret", "consumer_key": "ck-secret"}}
	s, err := Default().Apply(u)
	if err != nil {
		t.Fatal(err)
	}
	if s.DNSCredentials["endpoint"] != "ovh-eu" {
		t.Fatalf("endpoint default: %+v", s.DNSCredentials)
	}
	v := s.View()
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "as-secret") || strings.Contains(string(b), "ck-secret") {
		t.Fatalf("a secret in the view: %s", b)
	}
	if v.DNSCredentials["application_key"] != "ak-1" || v.DNSCredentials["endpoint"] != "ovh-eu" || !v.DNSSecretsSet["application_secret"] || !v.DNSSecretsSet["consumer_key"] {
		t.Fatalf("%+v", v)
	}
	back := Update{Mode: v.Mode, Directory: v.Directory, Names: v.Names, Challenge: v.Challenge, DNSProvider: v.DNSProvider, DNSCredentials: map[string]string{"endpoint": "ovh-ca", "application_key": "ak-1"}}
	n, err := s.Apply(back)
	if err != nil {
		t.Fatal(err)
	}
	if n.DNSCredentials["application_secret"] != "as-secret" || n.DNSCredentials["consumer_key"] != "ck-secret" || n.DNSCredentials["endpoint"] != "ovh-ca" {
		t.Fatalf("%+v", n.DNSCredentials)
	}
	back.DNSCredentials["endpoint"] = "ovh-asia"
	if _, err := s.Apply(back); err == nil || !strings.Contains(err.Error(), "Endpoint must be one of ovh-eu, ovh-ca, ovh-us") {
		t.Fatalf("bad endpoint: %v", err)
	}
	// without a consumer key, mode ACME is refused
	u.DNSCredentials = map[string]string{"application_key": "ak-1", "application_secret": "as-secret"}
	if _, err := Default().Apply(u); err == nil || !strings.Contains(err.Error(), "Consumer key is missing") {
		t.Fatalf("missing consumer key: %v", err)
	}
}
