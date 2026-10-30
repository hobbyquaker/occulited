package acme

import (
	"context"
	"crypto"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/duckdns"
	"github.com/go-acme/lego/v4/providers/dns/exec"
	"github.com/go-acme/lego/v4/providers/dns/hetzner"
	"github.com/go-acme/lego/v4/providers/dns/netcup"
	"github.com/go-acme/lego/v4/registration"
)

// LegoIssuer runs the flow with lego (github.com/go-acme/lego/v4) as a library: no second
// process, no shell hooks (D-48). UserAgent identifies the box to the CA.
type LegoIssuer struct {
	UserAgent string
}

// legoUser is registration.User.
type legoUser struct {
	email string
	key   crypto.PrivateKey
	reg   *registration.Resource
}

func (u *legoUser) GetEmail() string                        { return u.email }
func (u *legoUser) GetRegistration() *registration.Resource { return u.reg }
func (u *legoUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// lego logs through one package-level logger; runs are serialised by the Service, so one sink
// that forwards to the current request's Log is exact. The default (stderr) is never used.
var (
	sinkMu  sync.Mutex
	sinkLog func(string)
)

type sink struct{}

func (sink) emit(s string) {
	sinkMu.Lock()
	f := sinkLog
	sinkMu.Unlock()
	if f != nil {
		f(strings.TrimRight(s, "\n"))
	}
}
func (l sink) Fatal(a ...any)            { l.emit(fmt.Sprint(a...)) }
func (l sink) Fatalln(a ...any)          { l.emit(fmt.Sprintln(a...)) }
func (l sink) Fatalf(f string, a ...any) { l.emit(fmt.Sprintf(f, a...)) }
func (l sink) Print(a ...any)            { l.emit(fmt.Sprint(a...)) }
func (l sink) Println(a ...any)          { l.emit(fmt.Sprintln(a...)) }
func (l sink) Printf(f string, a ...any) { l.emit(fmt.Sprintf(f, a...)) }
func init()                              { legolog.Logger = sink{} }
func setSink(f func(string))             { sinkMu.Lock(); sinkLog = f; sinkMu.Unlock() }

// Issue registers (or finds) the account at the directory, solves the challenge and obtains the
// certificate. Nothing is installed here.
func (li LegoIssuer) Issue(ctx context.Context, req Request) (*Result, error) {
	logf := func(format string, a ...any) {
		if req.Log != nil {
			req.Log(fmt.Sprintf(format, a...))
		}
	}
	if req.LibLog != nil {
		setSink(req.LibLog)
	} else {
		setSink(req.Log)
	}
	defer setSink(nil)

	user := &legoUser{email: req.Email, key: req.AccountKey}
	if len(req.Registration) > 0 {
		var r registration.Resource
		if err := json.Unmarshal(req.Registration, &r); err == nil && r.URI != "" {
			user.reg = &r
		}
	}
	cfg := lego.NewConfig(user)
	cfg.CADirURL = req.DirectoryURL
	cfg.UserAgent = li.UserAgent
	cfg.Certificate.KeyType = certcrypto.EC256
	cfg.HTTPClient = req.HTTP
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = httpClient()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logf("directory %s", req.DirectoryURL)
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("directory: %w", err)
	}
	switch req.Challenge {
	case ChallengeHTTP01:
		if req.HTTP01 == nil {
			return nil, errors.New("http-01: no token store")
		}
		if err := client.Challenge.SetHTTP01Provider(req.HTTP01); err != nil {
			return nil, err
		}
	case ChallengeDNS01:
		p, err := dnsProvider(req.DNSProvider, req.DNSFields)
		if err != nil {
			return nil, err
		}
		if err := client.Challenge.SetDNS01Provider(p); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown challenge %q", req.Challenge)
	}
	if user.reg == nil {
		reg, err := register(client, req, logf)
		if err != nil {
			return nil, err
		}
		user.reg = reg
	} else {
		logf("account %s", user.reg.URI)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// the flow is synchronous inside lego; ctx bounds it through the HTTP client's transport
	// only loosely, so the caller's deadline is checked at the steps above and honoured by the
	// provider's own timeouts below
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: req.Names, Bundle: true})
	if err != nil {
		return nil, fmt.Errorf("order: %w", err)
	}
	regJSON, _ := json.Marshal(user.reg)
	logf("certificate obtained for %s", strings.Join(req.Names, ", "))
	return &Result{Certificate: res.Certificate, PrivateKey: res.PrivateKey, Registration: regJSON}, nil
}

// register creates the account, or finds the one this key already has at the directory (a box
// restored from a backup that lost account.json, or a second run after a crash).
func register(client *lego.Client, req Request, logf func(string, ...any)) (*registration.Resource, error) {
	if reg, err := client.Registration.ResolveAccountByKey(); err == nil && reg != nil && reg.URI != "" {
		logf("account found by key: %s", reg.URI)
		return reg, nil
	}
	if req.EABKID != "" {
		logf("registering with external account binding (kid %s)", req.EABKID)
		reg, err := client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{TermsOfServiceAgreed: true, Kid: req.EABKID, HmacEncoded: req.EABHMAC})
		if err != nil {
			return nil, fmt.Errorf("register: %w", err)
		}
		return reg, nil
	}
	if client.GetExternalAccountRequired() {
		return nil, errors.New("register: this CA requires external account binding (an EAB key id and HMAC)")
	}
	logf("registering an account%s", emailNote(req.Email))
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	return reg, nil
}

func emailNote(email string) string {
	if email == "" {
		return " without a contact address"
	}
	return " for " + email
}

// httpClient is lego's default client on Go's own roots, for a Request without one (a test).
// On the box the request carries the trust store's client (task 231, task 232).
func httpClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{Timeout: 2 * time.Minute, Transport: tr}
}

// dnsProvider builds the chosen provider from its fields. Each provider's own defaults
// (propagation timeout, polling) stay as lego ships them. The set is D-49's (2026-09-09): the
// five light ones; deSEC, INWX and RFC 2136 were dropped for their clients and the kerberos
// chain (+3.8 MB of binary) and come back on request - one case here, one entry in Providers.
func dnsProvider(id string, f map[string]string) (challenge.Provider, error) {
	switch id {
	case "cloudflare":
		c := cloudflare.NewDefaultConfig()
		c.AuthToken = f["api_token"]
		return cloudflare.NewDNSProviderConfig(c)
	case "hetzner":
		c := hetzner.NewDefaultConfig()
		c.APIToken = f["api_token"]
		return hetzner.NewDNSProviderConfig(c)
	case "netcup":
		c := netcup.NewDefaultConfig()
		c.Customer, c.Key, c.Password = f["customer"], f["api_key"], f["api_password"]
		return netcup.NewDNSProviderConfig(c)
	case "ovh":
		return newOVHProvider(f)
	case "duckdns":
		c := duckdns.NewDefaultConfig()
		c.Token = f["token"]
		return duckdns.NewDNSProviderConfig(c)
	case "exec":
		c := exec.NewDefaultConfig()
		c.Program, c.Mode = f["program"], f["mode"]
		if c.Program == "" {
			return nil, errors.New("exec: no program")
		}
		return exec.NewDNSProviderConfig(c)
	}
	return nil, fmt.Errorf("unknown DNS provider %q", id)
}
