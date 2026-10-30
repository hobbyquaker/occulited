package acme

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/certpem"
	"github.com/hobbyquaker/occulited/internal/certpem/testcert"
)

func TestSettingsValidation(t *testing.T) {
	base := Settings{Mode: ModeACME, Directory: DirLetsEncrypt, Names: []string{"box.example.org"}, Challenge: ChallengeHTTP01, DNSCredentials: map[string]string{}}
	cases := []struct {
		name string
		mod  func(s *Settings)
		want string // "" = valid, else a fragment of the message
	}{
		{"valid http-01", func(*Settings) {}, ""},
		{"an ip address", func(s *Settings) { s.Names = []string{"192.0.2.119"} }, "IP address"},
		{"an ipv6 address", func(s *Settings) { s.Names = []string{"fe80::1"} }, "IP address"},
		{"a bad name", func(s *Settings) { s.Names = []string{"box_1.example.org"} }, "not a DNS name"},
		{"a wildcard needs dns-01", func(s *Settings) { s.Names = []string{"*.example.org"} }, "wildcard"},
		{"a wildcard with dns-01", func(s *Settings) {
			s.Names = []string{"*.example.org"}
			s.Challenge, s.DNSProvider, s.DNSCredentials = ChallengeDNS01, "cloudflare", map[string]string{"api_token": "x"}
		}, ""},
		{"no names under acme", func(s *Settings) { s.Names = nil }, "at least one"},
		{"no names when self-signed", func(s *Settings) { s.Names = nil; s.Mode = ModeSelfSigned }, ""},
		{"twice", func(s *Settings) { s.Names = []string{"a.example.org", "a.example.org"} }, "twice"},
		{"bad mode", func(s *Settings) { s.Mode = "auto" }, "mode must be"},
		{"bad directory", func(s *Settings) { s.Directory = "buypass" }, "unknown directory"},
		{"custom needs https", func(s *Settings) { s.Directory, s.DirectoryURL = DirCustom, "http://ca.lan/acme/acme/directory" }, "https URL"},
		{"custom ok", func(s *Settings) { s.Directory, s.DirectoryURL = DirCustom, "https://ca.lan:9000/acme/acme/directory" }, ""},
		{"custom bad root", func(s *Settings) { s.Directory, s.DirectoryURL, s.CARoot = DirCustom, "https://ca.lan/d", "garbage" }, "not a PEM certificate"},
		{"bad email", func(s *Settings) { s.Email = "nobody" }, "e-mail"},
		{"eab needs both", func(s *Settings) { s.EABKID = "kid" }, "both"},
		{"zerossl needs eab", func(s *Settings) { s.Directory = DirZeroSSL }, "ZeroSSL needs"},
		{"zerossl with eab", func(s *Settings) { s.Directory, s.EABKID, s.EABHMAC = DirZeroSSL, "kid", "hmac" }, ""},
		{"bad challenge", func(s *Settings) { s.Challenge = "tls-alpn-01" }, "challenge must be"},
		{"dns-01 without a provider", func(s *Settings) { s.Challenge = ChallengeDNS01 }, "choose a DNS provider"},
		{"dns-01 unknown provider", func(s *Settings) { s.Challenge, s.DNSProvider = ChallengeDNS01, "route53" }, "choose a DNS provider"},
		{"dns-01 missing field", func(s *Settings) { s.Challenge, s.DNSProvider = ChallengeDNS01, "cloudflare" }, "API token is missing"},
		{"dns-01 unknown field", func(s *Settings) {
			s.Challenge, s.DNSProvider, s.DNSCredentials = ChallengeDNS01, "cloudflare", map[string]string{"api_token": "x", "zone": "y"}
		}, "no field"},
		{"dns-01 optional field may be empty", func(s *Settings) {
			s.Challenge, s.DNSProvider, s.DNSCredentials = ChallengeDNS01, "exec", map[string]string{"program": "/usr/local/etc/config/acme-dns.sh"}
		}, ""},
	}
	for _, c := range cases {
		s := base
		s.Names = append([]string{}, base.Names...)
		s.DNSCredentials = map[string]string{}
		c.mod(&s)
		err := s.Validate()
		if c.want == "" && err != nil {
			t.Errorf("%s: unexpected %v", c.name, err)
		}
		if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want) || !errors.Is(err, ErrInvalid)) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
	if Default().Validate() != nil {
		t.Error("the default is invalid")
	}
	s := Settings{Directory: DirLetsEncrypt}
	if s.DirectoryURLFor() != "https://acme-v02.api.letsencrypt.org/directory" || !strings.Contains(s.TestDirectoryURL(), "staging") {
		t.Error("directory urls")
	}
	s = Settings{Directory: DirCustom, DirectoryURL: "https://ca.lan/d"}
	if s.TestDirectoryURL() != "https://ca.lan/d" {
		t.Error("a custom CA tests against itself")
	}
}

func TestSettingsSecrets(t *testing.T) {
	stored := Settings{Mode: ModeACME, Directory: DirZeroSSL, EABKID: "kid", EABHMAC: "hmac-secret", Names: []string{"box.example.org"}, Challenge: ChallengeDNS01,
		DNSProvider: "netcup", DNSCredentials: map[string]string{"customer": "12345", "api_key": "key-secret", "api_password": "pw-secret"}}
	// the view carries the flags and the non-secret fields, never a secret's value
	v := stored.View()
	b, _ := json.Marshal(v)
	for _, secret := range []string{"hmac-secret", "pw-secret", "key-secret"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret %q in the view: %s", secret, b)
		}
	}
	if !v.EABHMACSet || v.DNSCredentials["customer"] != "12345" || !v.DNSSecretsSet["api_key"] || !v.DNSSecretsSet["api_password"] || v.DNSCredentials["api_password"] != "" {
		t.Fatalf("%+v", v)
	}
	// saving the view back (empty secrets) keeps every secret
	u := Update{Mode: v.Mode, Directory: v.Directory, EABKID: v.EABKID, Names: v.Names, Challenge: v.Challenge, DNSProvider: v.DNSProvider, DNSCredentials: v.DNSCredentials}
	n, err := stored.Apply(u)
	if err != nil {
		t.Fatal(err)
	}
	if n.EABHMAC != "hmac-secret" || n.DNSCredentials["api_password"] != "pw-secret" || n.DNSCredentials["api_key"] != "key-secret" || n.DNSCredentials["customer"] != "12345" {
		t.Fatalf("%+v", n)
	}
	// a new value replaces
	u.DNSCredentials = map[string]string{"customer": "12345", "api_password": "new-pw"}
	n, _ = stored.Apply(u)
	if n.DNSCredentials["api_password"] != "new-pw" || n.DNSCredentials["api_key"] != "key-secret" {
		t.Fatalf("%+v", n)
	}
	// switching the provider drops the old credentials rather than keeping them around
	u.DNSProvider, u.DNSCredentials = "duckdns", map[string]string{"token": "tok"}
	n, _ = stored.Apply(u)
	if len(n.DNSCredentials) != 1 || n.DNSCredentials["token"] != "tok" {
		t.Fatalf("%+v", n)
	}
	// a changed EAB kid does not inherit the old HMAC
	u.EABKID = "other"
	if _, err := stored.Apply(u); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("changed kid without hmac: %v", err)
	}
	// dropping the kid drops the hmac; switching to http-01 drops the provider; names are
	// trimmed and lower-cased; a non-custom directory drops url and root
	u = Update{Mode: ModeACME, Directory: DirLetsEncrypt, DirectoryURL: "https://x", CARoot: "y", Names: []string{" Box.Example.ORG ", ""}, Challenge: ChallengeHTTP01, DNSProvider: "netcup", DNSCredentials: map[string]string{"customer": "12345"}}
	n, err = stored.Apply(u)
	if err != nil {
		t.Fatal(err)
	}
	if n.EABHMAC != "" || n.DNSProvider != "" || len(n.DNSCredentials) != 0 || n.DirectoryURL != "" || n.CARoot != "" || len(n.Names) != 1 || n.Names[0] != "box.example.org" {
		t.Fatalf("%+v", n)
	}
	// an invalid update leaves the stored value as it was
	u.Names = []string{"192.0.2.1"}
	if got, err := stored.Apply(u); err == nil || got.Names[0] != "box.example.org" {
		t.Fatalf("invalid update changed the settings: %v %+v", err, got)
	}
}

// fakeIssuer records requests and answers a certificate for the names asked, or an error.
type fakeIssuer struct {
	mu    sync.Mutex
	reqs  []Request
	err   error
	days  int
	hours int // a lifetime in hours, when set (a LAN CA's 24 h)
	reg   string
}

func (f *fakeIssuer) Issue(_ context.Context, req Request) (*Result, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if req.Log != nil {
		req.Log("fake: order for " + strings.Join(req.Names, ","))
	}
	if req.HTTP01 != nil && req.Challenge == ChallengeHTTP01 {
		_ = req.HTTP01.Present(req.Names[0], "tok", "tok.auth")
		_ = req.HTTP01.CleanUp(req.Names[0], "tok", "tok.auth")
	}
	if f.err != nil {
		return nil, f.err
	}
	days := f.days
	if days == 0 {
		days = 90
	}
	life := time.Duration(days) * 24 * time.Hour
	if f.hours > 0 {
		life = time.Duration(f.hours) * time.Hour
	}
	leaf, key, ca := testcert.Issued(req.Names, time.Now().Add(life))
	reg := f.reg
	if reg == "" {
		reg = `{"uri":"https://ca/acct/1"}`
	}
	return &Result{Certificate: append(leaf, ca...), PrivateKey: key, Registration: json.RawMessage(reg)}, nil
}

// fakeInstaller is the box: a live file in a temp dir, a list of what was "restarted".
type fakeInstaller struct {
	mu        sync.Mutex
	live      []byte
	installs  int
	removes   int
	restarted []string
	fail      error
	managed   bool   // the marker beside the live file
	mode      string // what the marker names
}

func (f *fakeInstaller) Install(_ context.Context, pem []byte, mode string, log func(string)) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	f.live = pem
	f.installs++
	f.managed = true
	f.mode = mode
	log("fake: installed")
	return f.restarted, nil
}

func (f *fakeInstaller) Remove(_ context.Context, log func(string)) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes++
	f.managed = false
	// S50lighttpd regenerates a self-signed one on the reload
	cert, key := testcert.SelfSigned([]string{"openccu"}, []string{"192.0.2.119"}, time.Now().Add(3650*24*time.Hour))
	f.live = append(cert, key...)
	log("fake: removed")
	return f.restarted, nil
}

func (f *fakeInstaller) ReadLive() ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.live == nil {
		return nil, "", os.ErrNotExist
	}
	marker := ""
	if f.managed {
		marker = f.mode + " CN=Test CA"
	}
	return certpem.CertificatesOnly(f.live), marker, nil
}

func newService(t *testing.T, issuer Issuer, inst *fakeInstaller) *Service {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "acme"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Issuer, s.Installer = issuer, inst
	return s
}

func wait(t *testing.T, s *Service) *Attempt {
	t.Helper()
	for i := 0; i < 200; i++ {
		st := s.Status()
		if st.Running == nil && st.Last != nil {
			return st.Last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the attempt did not finish")
	return nil
}

func acmeSettings() Update {
	return Update{Mode: ModeACME, Directory: DirLetsEncrypt, Email: "me@example.org", Names: []string{"box.example.org", "alias.example.org"}, Challenge: ChallengeHTTP01}
}

func TestServiceIssueRenewAndBack(t *testing.T) {
	iss := &fakeIssuer{}
	inst := &fakeInstaller{restarted: []string{"mosquitto"}}
	cert, key := testcert.SelfSigned([]string{"openccu"}, []string{"192.0.2.119"}, time.Now().Add(3650*24*time.Hour))
	inst.live = append(cert, key...)
	s := newService(t, iss, inst)

	// the fresh box: self-signed, nothing issued, no attempt
	st := s.Status()
	if st.Settings.Mode != ModeSelfSigned || !st.Current.SelfSigned || st.Issued != nil || st.Last != nil || st.Running != nil || st.Warning != "" || len(st.Providers) != len(Providers) {
		t.Fatalf("%+v", st)
	}
	// issue needs the ACME mode
	if err := s.Start(KindIssue); err == nil || !strings.Contains(err.Error(), "self-signed") {
		t.Fatalf("issue while self-signed: %v", err)
	}
	if _, err := s.SetSettings(acmeSettings()); err != nil {
		t.Fatal(err)
	}
	// the settings are on disk, 0600, and survive a restart
	if st, err := os.Stat(s.st.path(fileSettings)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("settings file: %v %v", err, st)
	}
	again, err := New(s.st.dir, nil)
	if err != nil || again.Settings().Mode != ModeACME || len(again.Settings().Names) != 2 {
		t.Fatalf("reload: %v %+v", err, again.Settings())
	}

	// a test run: staging, nothing installed, the certificate described
	j := &testJournal{}
	s.Journal = j
	if err := s.Start(KindTest); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(KindIssue); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second run while one is in flight: %v", err)
	}
	a := wait(t, s)
	if !a.OK || a.Kind != KindTest || a.Installed || !strings.Contains(a.Directory, "staging") || a.Certificate == nil || inst.installs != 0 {
		t.Fatalf("%+v", a)
	}
	// task 102: the lines are the run's journal entries
	if lines := j.messages(a.RunID); len(lines) < 3 || !strings.Contains(strings.Join(lines, "\n"), "fake: order") {
		t.Fatalf("lines of %q: %v", a.RunID, lines)
	}
	if st := s.Status(); st.Issued != nil || !st.Current.SelfSigned {
		t.Fatalf("a test installed something: %+v", st)
	}
	// the request carried what the issuer needs, and the account key exists
	if r := iss.reqs[0]; r.Email != "me@example.org" || r.AccountKey == nil || r.HTTP01 == nil || len(r.Registration) != 0 || len(r.Names) != 2 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(s.st.path(fileAccount)); err != nil {
		t.Fatal("no account key")
	}

	// issue for real: stored, installed, the addons restarted, the registration kept
	if err := s.Start(KindIssue); err != nil {
		t.Fatal(err)
	}
	a = wait(t, s)
	if !a.OK || a.Kind != KindIssue || !a.Installed || a.RestartedAddons[0] != "mosquitto" || inst.installs != 1 || !strings.Contains(a.Directory, "acme-v02") {
		t.Fatalf("%+v", a)
	}
	st = s.Status()
	if st.Issued == nil || st.Current == nil || !st.LiveIsIssued || st.Current.SelfSigned || st.Current.DaysLeft < 88 || st.Warning != "" || st.Issued.Names[0] != "box.example.org" {
		t.Fatalf("%+v %+v", st.Current, st.Issued)
	}
	if !st.Managed || st.ManagedBy != "CN=Test CA" || a.Warning != "" {
		t.Fatalf("managed: %+v", st)
	}
	if err := certpem.ValidLivePEM(inst.live); err != nil {
		t.Fatal(err)
	}
	regs := s.st.registrations()
	if !strings.Contains(string(regs[a.Directory]), "acct/1") {
		t.Fatalf("registration not kept: %v", regs)
	}
	// the production registration is handed back on the next run at that directory, the
	// staging one is separate
	if err := s.Start(KindRenew); err != nil {
		t.Fatal(err)
	}
	a = wait(t, s)
	if !a.OK || a.Kind != KindRenew || inst.installs != 2 || !strings.Contains(string(iss.reqs[2].Registration), "acct/1") {
		t.Fatalf("%+v %s", a, iss.reqs[2].Registration)
	}
	// the last attempt survives a restart of the daemon
	again, _ = New(s.st.dir, nil)
	if l := again.Status().Last; l == nil || l.Kind != KindRenew || !l.OK {
		t.Fatalf("last not reloaded: %+v", l)
	}

	// a failed renewal: the old certificate stays, the status warns
	iss.err = errors.New("dns-01: propagation timed out")
	if err := s.Start(KindRenew); err != nil {
		t.Fatal(err)
	}
	a = wait(t, s)
	if a.OK || !strings.Contains(a.Error, "propagation") || a.Installed || inst.installs != 2 {
		t.Fatalf("%+v", a)
	}
	st = s.Status()
	if !st.LiveIsIssued || st.Warning != "last-attempt-failed" {
		t.Fatalf("%+v", st)
	}
	// a failed test is not a warning
	if err := s.Start(KindTest); err != nil {
		t.Fatal(err)
	}
	wait(t, s)
	if st := s.Status(); st.Warning != "" {
		t.Fatalf("a failed test warned: %+v", st)
	}
	iss.err = nil

	// the install failing is the attempt's error, not a silent success
	inst.fail = errors.New("refused by the privilege helper")
	if err := s.Start(KindIssue); err != nil {
		t.Fatal(err)
	}
	a = wait(t, s)
	if a.OK || !strings.Contains(a.Error, "install: refused") || a.Installed {
		t.Fatalf("%+v", a)
	}
	inst.fail = nil

	// back to self-signed: the live file removed (and regenerated by the box), the mode saved
	restarted, err := s.SelfSigned(context.Background())
	if err != nil || inst.removes != 1 || restarted[0] != "mosquitto" {
		t.Fatalf("%v %v %d", err, restarted, inst.removes)
	}
	st = s.Status()
	if st.Settings.Mode != ModeSelfSigned || !st.Current.SelfSigned || st.LiveIsIssued || st.Warning != "" || st.Managed {
		t.Fatalf("%+v %+v", st.Settings, st.Current)
	}
	// the names and the account are kept for the next switch
	if len(st.Settings.Names) != 2 || st.Settings.Email != "me@example.org" {
		t.Fatalf("%+v", st.Settings)
	}
}

func TestServiceTestValidatesAsACME(t *testing.T) {
	s := newService(t, &fakeIssuer{}, &fakeInstaller{})
	// self-signed with no names: a test cannot run, and says what is missing
	if err := s.Start(KindTest); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("%v", err)
	}
	// a test with names but still self-signed runs against staging without switching the mode
	u := acmeSettings()
	u.Mode = ModeSelfSigned
	if _, err := s.SetSettings(u); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(KindTest); err != nil {
		t.Fatal(err)
	}
	a := wait(t, s)
	if !a.OK || s.Status().Settings.Mode != ModeSelfSigned {
		t.Fatalf("%+v", a)
	}
	// a custom CA's root travels with the request
	u.Directory, u.DirectoryURL, u.Mode = DirCustom, "https://ca.lan:9000/acme/acme/directory", ModeACME
	_, _, ca := testcert.Issued([]string{"x"}, time.Now().Add(time.Hour))
	u.CARoot = string(ca)
	if _, err := s.SetSettings(u); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(KindTest); err != nil {
		t.Fatal(err)
	}
	a = wait(t, s)
	iss := s.Issuer.(*fakeIssuer)
	// task 231: the pool the service is given travels with the request; the settings' CA root is
	// what TakeCARoot hands to the trust store once
	if r := iss.reqs[len(iss.reqs)-1]; a.Directory != "https://ca.lan:9000/acme/acme/directory" || r.HTTP != nil {
		t.Fatalf("%+v", r)
	}
	if got := s.TakeCARoot(); got != strings.TrimSpace(string(ca)) || s.Settings().CARoot != "" || s.TakeCARoot() != "" {
		t.Fatalf("TakeCARoot: %q, settings %q", got, s.Settings().CARoot)
	}
	own := &http.Client{Timeout: time.Second}
	s.HTTP = func() *http.Client { return own }
	if err := s.Start(KindTest); err != nil {
		t.Fatal(err)
	}
	a = wait(t, s)
	if r := iss.reqs[len(iss.reqs)-1]; !a.OK || r.HTTP != own {
		t.Fatalf("http client: %+v, attempt %+v", r, a)
	}
	// the DNS fields travel too, and the secrets are in the request but never in the status
	u.Challenge, u.DNSProvider, u.DNSCredentials = ChallengeDNS01, "cloudflare", map[string]string{"api_token": "cf-secret"}
	if _, err := s.SetSettings(u); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(KindIssue); err != nil {
		t.Fatal(err)
	}
	wait(t, s)
	if r := iss.reqs[len(iss.reqs)-1]; r.DNSProvider != "cloudflare" || r.DNSFields["api_token"] != "cf-secret" || r.Challenge != ChallengeDNS01 {
		t.Fatalf("%+v", r)
	}
	if b, _ := json.Marshal(s.Status()); strings.Contains(string(b), "cf-secret") {
		t.Fatalf("secret in the status: %s", b)
	}
}

func TestShouldRenew(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	// a certificate of lifetime life with left remaining
	short := func(life, left time.Duration) *certpem.Info {
		return &certpem.Info{NotBefore: now.Add(left - life), NotAfter: now.Add(left)}
	}
	// a 60-day certificate with days left: half of it is the 30-day floor, so the floor decides
	info := func(days int) *certpem.Info { return short(60*24*time.Hour, time.Duration(days)*24*time.Hour) }
	cases := []struct {
		mode string
		info *certpem.Info
		want bool
	}{
		{ModeACME, info(59), false},
		{ModeACME, info(31), false},
		{ModeACME, info(30), false}, // exactly 30 days: not yet
		{ModeACME, info(29), true},
		{ModeACME, info(1), true},
		{ModeACME, info(-1), true},
		{ModeACME, nil, false},           // nothing issued: issuing is the user's act
		{ModeSelfSigned, info(1), false}, // S50's business
		// the half-lifetime rule: a step-ca's 24-hour certificate is due at every check, a
		// 7-day one after three and a half days, Let's Encrypt's 90-day one at 45 - none of
		// them waits for the 30-day line
		{ModeACME, short(24*time.Hour, 23*time.Hour), true},
		{ModeACME, short(24*time.Hour, 13*time.Hour), true},
		{ModeACME, short(24*time.Hour, 11*time.Hour), true},
		{ModeACME, short(7*24*time.Hour, 4*24*time.Hour), true},
		{ModeACME, short(90*24*time.Hour, 44*24*time.Hour), true},
		{ModeACME, short(90*24*time.Hour, 46*24*time.Hour), false},
		{ModeSelfSigned, short(24*time.Hour, 1*time.Hour), false},
		// 30 days is the floor whatever the lifetime: half of 200 days is 100
		{ModeACME, short(200*24*time.Hour, 99*24*time.Hour), true},
		{ModeACME, short(200*24*time.Hour, 101*24*time.Hour), false},
	}
	for i, c := range cases {
		if got := ShouldRenew(c.mode, c.info, now); got != c.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
	// the timer renews a short certificate and leaves a long one alone
	iss := &fakeIssuer{days: 20}
	inst := &fakeInstaller{}
	s := newService(t, iss, inst)
	if _, err := s.SetSettings(acmeSettings()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(KindIssue); err != nil {
		t.Fatal(err)
	}
	wait(t, s)
	iss.days = 90
	s.check()
	a := wait(t, s)
	if a.Kind != KindRenew || !a.OK || inst.installs != 2 || s.Status().Issued.DaysLeft < 88 {
		t.Fatalf("%+v", a)
	}
	s.check() // 90 days left: nothing happens
	if st := s.Status(); st.Running != nil || inst.installs != 2 {
		t.Fatal("renewed a fresh certificate")
	}
	// the warning rule: under 14 days
	iss.days = 10
	if err := s.Start(KindRenew); err != nil {
		t.Fatal(err)
	}
	wait(t, s)
	if st := s.Status(); st.Warning != "expiring" {
		t.Fatalf("%+v", st.Warning)
	}
}

func TestShortLifetimeWarning(t *testing.T) {
	now := time.Now()
	if w := ShortLifetimeWarning(&certpem.Info{NotBefore: now, NotAfter: now.Add(90 * 24 * time.Hour)}); w != "" {
		t.Fatalf("90 days warned: %s", w)
	}
	if w := ShortLifetimeWarning(&certpem.Info{NotBefore: now, NotAfter: now.Add(48 * time.Hour)}); w != "" {
		t.Fatalf("48 h warned: %s", w)
	}
	w := ShortLifetimeWarning(&certpem.Info{NotBefore: now, NotAfter: now.Add(24 * time.Hour)})
	if !strings.Contains(w, "24h0m0s") || !strings.Contains(w, "twice a day") || !strings.Contains(w, "12 hours") {
		t.Fatalf("%s", w)
	}
	// an issue of a short certificate carries the warning in the attempt and in its lines
	iss := &fakeIssuer{hours: 24}
	s := newService(t, iss, &fakeInstaller{})
	j := &testJournal{}
	s.Journal = j
	if _, err := s.SetSettings(acmeSettings()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(KindIssue); err != nil {
		t.Fatal(err)
	}
	a := wait(t, s)
	warned := false
	for _, e := range j.ofRun(a.RunID) {
		if strings.HasPrefix(jField(e, "MESSAGE"), "warning: the certificate's lifetime") && jField(e, "PRIORITY") == "4" {
			warned = true
		}
	}
	if !a.OK || !strings.Contains(a.Warning, "lifetime") || !warned {
		t.Fatalf("%+v %v", a, j.messages(a.RunID))
	}
	// and the timer finds it due at once (24 h lifetime, more than 30 days is not the rule here)
	s.check()
	b := wait(t, s)
	if b.Kind != KindRenew || !b.OK {
		t.Fatalf("%+v", b)
	}
	// a persisted attempt keeps the warning
	again, _ := New(s.st.dir, nil)
	if l := again.Status().Last; l == nil || l.Warning == "" {
		t.Fatalf("%+v", l)
	}
}

func TestHTTP01TokenStore(t *testing.T) {
	ts := &TokenStore{}
	srv := httptest.NewServer(ts)
	defer srv.Close()
	get := func(path string) (int, string) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var b strings.Builder
		buf := make([]byte, 1024)
		for {
			n, err := res.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return res.StatusCode, b.String()
	}
	if st, _ := get(ChallengePrefix + "abc"); st != 404 {
		t.Fatalf("unknown token: %d", st)
	}
	_ = ts.Present("box.example.org", "abc", "abc.keyauth")
	if ts.Pending() != 1 {
		t.Fatal("pending")
	}
	if st, body := get(ChallengePrefix + "abc"); st != 200 || body != "abc.keyauth" {
		t.Fatalf("%d %q", st, body)
	}
	if st, _ := get(ChallengePrefix + "abc/../x"); st != 404 {
		t.Fatalf("traversal: %d", st)
	}
	if st, _ := get(ChallengePrefix); st != 404 {
		t.Fatalf("empty token: %d", st)
	}
	_ = ts.CleanUp("box.example.org", "abc", "abc.keyauth")
	if st, _ := get(ChallengePrefix + "abc"); st != 404 || ts.Pending() != 0 {
		t.Fatalf("after cleanup: %d", st)
	}
	res, _ := http.Post(srv.URL+ChallengePrefix+"abc", "text/plain", nil)
	if res.StatusCode != 405 {
		t.Fatalf("post: %d", res.StatusCode)
	}
}

func TestProviders(t *testing.T) {
	want := []string{"cloudflare", "hetzner", "netcup", "duckdns", "ovh", "exec"} // D-49, OVH on request (issue #13)
	for i, id := range want {
		if Providers[i].ID != id || ProviderByID(id) == nil {
			t.Fatalf("provider %s", id)
		}
		for _, f := range Providers[i].Fields {
			if f.Key == "" || f.Label == "" {
				t.Fatalf("%s: %+v", id, f)
			}
		}
	}
	for _, id := range []string{"route53", "desec", "inwx", "rfc2136"} { // the last three: D-49
		if ProviderByID(id) != nil {
			t.Fatalf("provider %s present", id)
		}
		if _, err := dnsProvider(id, map[string]string{}); err == nil {
			t.Fatalf("provider %s builds", id)
		}
	}
	// every provider builds from its fields (the lego constructors validate)
	for _, p := range Providers {
		f := map[string]string{}
		for _, fd := range p.Fields {
			if !fd.Optional {
				f[fd.Key] = "value"
			}
			if len(fd.Options) > 0 {
				f[fd.Key] = fd.Options[0]
			}
		}
		if p.ID == "exec" {
			f["program"] = "/bin/true"
		}
		if _, err := dnsProvider(p.ID, f); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
	}
	if _, err := dnsProvider("exec", map[string]string{}); err == nil {
		t.Error("exec without a program")
	}
}
