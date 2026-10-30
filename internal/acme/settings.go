// Package acme is occulited's TLS certificate service (task 35, D-48): a certificate for the
// box from an ACME CA - Let's Encrypt for a box with a public name, a LAN CA such as step-ca
// for everything else - issued and renewed by the daemon itself with lego as a library, and
// handed to lighttpd and to the confined addons through /etc/config/server.pem (D-46).
//
// The package keeps the settings and the account, the issued chain and the last attempt under
// <state>/acme/ (0600, the occulite user), runs the ACME flow in the background, and installs the
// result through an Installer the system package provides: the privilege helper writes the live
// file as root:certs 0640, lighttpd is reloaded, and every confined addon of the certs group is
// restarted. What the flow itself does is behind the Issuer interface, so the tests never talk
// to a CA.
package acme

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hobbyquaker/occulited/internal/certpem"
)

// The modes of the box's certificate; the third, ModeManual, is in manual.go (task 38).
const (
	ModeSelfSigned = "self-signed" // S50lighttpd's ten-year certificate, regenerated when it expires
	ModeACME       = "acme"        // issued by the CA below, renewed by occulited
)

// The directory presets. Custom is any URL - a step-ca's /acme/<provisioner>/directory - with
// an optional CA root PEM trusted for that connection only.
const (
	DirLetsEncrypt        = "letsencrypt"
	DirLetsEncryptStaging = "letsencrypt-staging"
	DirZeroSSL            = "zerossl"
	DirCustom             = "custom"
)

// The directory URLs behind the presets.
var directoryURLs = map[string]string{
	DirLetsEncrypt:        "https://acme-v02.api.letsencrypt.org/directory",
	DirLetsEncryptStaging: "https://acme-staging-v02.api.letsencrypt.org/directory",
	DirZeroSSL:            "https://acme.zerossl.com/v2/DV90",
}

// The challenges.
const (
	ChallengeHTTP01 = "http-01"
	ChallengeDNS01  = "dns-01"
)

// Settings is <state>/acme/settings.json - the whole configuration, secrets included. The API
// never returns it as it is: View is what goes out.
type Settings struct {
	Mode string `json:"mode"`
	// Directory is a preset name or "custom"; DirectoryURL and CARoot (PEM) are read for custom.
	Directory    string `json:"directory"`
	DirectoryURL string `json:"directory_url,omitempty"`
	CARoot       string `json:"ca_root,omitempty"`
	// Email is the account contact; optional for Let's Encrypt, which uses it for expiry mail.
	Email string `json:"email,omitempty"`
	// External account binding (ZeroSSL, company CAs): both or neither. The HMAC is a secret.
	EABKID  string `json:"eab_kid,omitempty"`
	EABHMAC string `json:"eab_hmac,omitempty"`
	// Names are the DNS names of the certificate; the first is the subject, all are SANs.
	Names []string `json:"names"`
	// Challenge is http-01 or dns-01; the DNS provider and its fields for the latter.
	Challenge      string            `json:"challenge"`
	DNSProvider    string            `json:"dns_provider,omitempty"`
	DNSCredentials map[string]string `json:"dns_credentials,omitempty"`
}

// Default is a box that has never been configured: self-signed, Let's Encrypt and HTTP-01
// pre-selected for when the mode is switched.
func Default() Settings {
	return Settings{Mode: ModeSelfSigned, Directory: DirLetsEncrypt, Challenge: ChallengeHTTP01, Names: []string{}}
}

// View is the settings as the API answers them: every secret replaced by "is it set".
type View struct {
	Mode           string            `json:"mode"`
	Directory      string            `json:"directory"`
	DirectoryURL   string            `json:"directory_url"`
	CARoot         string            `json:"ca_root"`
	Email          string            `json:"email"`
	EABKID         string            `json:"eab_kid"`
	EABHMACSet     bool              `json:"eab_hmac_set"`
	Names          []string          `json:"names"`
	Challenge      string            `json:"challenge"`
	DNSProvider    string            `json:"dns_provider"`
	DNSCredentials map[string]string `json:"dns_credentials"`
	DNSSecretsSet  map[string]bool   `json:"dns_secrets_set"`
}

// View redacts s. Non-secret provider fields (a user name, a nameserver, a program path) are
// returned; secret ones only as a flag.
func (s Settings) View() View {
	v := View{Mode: s.Mode, Directory: s.Directory, DirectoryURL: s.DirectoryURL, CARoot: s.CARoot, Email: s.Email, EABKID: s.EABKID, EABHMACSet: s.EABHMAC != "",
		Names: append([]string{}, s.Names...), Challenge: s.Challenge, DNSProvider: s.DNSProvider, DNSCredentials: map[string]string{}, DNSSecretsSet: map[string]bool{}}
	if v.Names == nil {
		v.Names = []string{}
	}
	if p := ProviderByID(s.DNSProvider); p != nil {
		for _, f := range p.Fields {
			val := s.DNSCredentials[f.Key]
			if f.Secret {
				v.DNSSecretsSet[f.Key] = val != ""
			} else {
				v.DNSCredentials[f.Key] = val
			}
		}
	}
	return v
}

// Update is what PUT /certificate/settings carries: the View's fields, plus the secrets, each
// optional - an empty secret keeps what is stored, so a page that never saw the value cannot
// wipe it by saving.
type Update struct {
	Mode           string            `json:"mode"`
	Directory      string            `json:"directory"`
	DirectoryURL   string            `json:"directory_url"`
	CARoot         string            `json:"ca_root"`
	Email          string            `json:"email"`
	EABKID         string            `json:"eab_kid"`
	EABHMAC        string            `json:"eab_hmac"`
	Names          []string          `json:"names"`
	Challenge      string            `json:"challenge"`
	DNSProvider    string            `json:"dns_provider"`
	DNSCredentials map[string]string `json:"dns_credentials"`
}

// Apply merges u over the stored settings and validates the result. Secrets: a non-empty value
// replaces, an empty one keeps the stored value *for the same provider* - switching the provider
// drops every credential of the old one.
func (s Settings) Apply(u Update) (Settings, error) {
	n := Settings{Mode: u.Mode, Directory: u.Directory, DirectoryURL: strings.TrimSpace(u.DirectoryURL), CARoot: strings.TrimSpace(u.CARoot),
		Email: strings.TrimSpace(u.Email), EABKID: strings.TrimSpace(u.EABKID), EABHMAC: strings.TrimSpace(u.EABHMAC),
		Challenge: u.Challenge, DNSProvider: u.DNSProvider, DNSCredentials: map[string]string{}}
	if n.EABHMAC == "" && n.EABKID != "" && n.EABKID == s.EABKID {
		n.EABHMAC = s.EABHMAC
	}
	if n.EABKID == "" {
		n.EABHMAC = ""
	}
	for _, raw := range u.Names {
		if name := strings.ToLower(strings.TrimSpace(raw)); name != "" {
			n.Names = append(n.Names, name)
		}
	}
	if n.Names == nil {
		n.Names = []string{}
	}
	if p := ProviderByID(n.DNSProvider); p != nil {
		for _, f := range p.Fields {
			val := strings.TrimSpace(u.DNSCredentials[f.Key])
			if val == "" && f.Secret && s.DNSProvider == n.DNSProvider {
				val = s.DNSCredentials[f.Key]
			}
			if val == "" && len(f.Options) > 0 {
				val = f.Options[0]
			}
			if val != "" {
				n.DNSCredentials[f.Key] = val
			}
		}
	}
	if n.Challenge != ChallengeDNS01 {
		n.DNSProvider, n.DNSCredentials = "", map[string]string{}
	}
	if n.Directory != DirCustom {
		n.DirectoryURL, n.CARoot = "", ""
	}
	if err := n.Validate(); err != nil {
		return s, err
	}
	return n, nil
}

// ErrInvalid marks a settings error the API answers with 422.
var ErrInvalid = errors.New("invalid certificate settings")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// dnsNameRe is one label of a DNS name (RFC 1123): the whole name is labels joined by dots, a
// leading "*." allowed for DNS-01 only.
var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidName checks one certificate name. An IP address is refused by name: a public CA will not
// put one in a certificate, and the message should say that rather than "invalid".
func ValidName(name string, wildcardOK bool) error {
	if name == "" {
		return invalid("an empty name")
	}
	if net.ParseIP(name) != nil {
		return invalid("%q is an IP address - an ACME certificate carries DNS names only; give the system a name in your DNS and use that", name)
	}
	if len(name) > 253 {
		return invalid("%q is too long for a DNS name", name)
	}
	labels := strings.Split(name, ".")
	for i, l := range labels {
		if i == 0 && l == "*" {
			if !wildcardOK {
				return invalid("%q: a wildcard needs the DNS-01 challenge", name)
			}
			if len(labels) < 2 {
				return invalid("%q is not a name", name)
			}
			continue
		}
		if !dnsLabelRe.MatchString(l) {
			return invalid("%q is not a DNS name (letters, digits and hyphens, labels joined by dots)", name)
		}
	}
	return nil
}

// Validate checks a complete Settings value.
func (s Settings) Validate() error {
	switch s.Mode {
	case ModeSelfSigned, ModeACME, ModeManual:
	default:
		return invalid("mode must be %s, %s or %s", ModeSelfSigned, ModeACME, ModeManual)
	}
	switch s.Directory {
	case DirLetsEncrypt, DirLetsEncryptStaging, DirZeroSSL:
	case DirCustom:
		u, err := url.Parse(s.DirectoryURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return invalid("the directory URL must be an https URL (a step-ca's is https://<ca>/acme/<provisioner>/directory)")
		}
		if s.CARoot != "" {
			if _, err := certpem.ParseCertificates([]byte(s.CARoot)); err != nil {
				return invalid("the CA root is not a PEM certificate: %v", err)
			}
		}
	default:
		return invalid("unknown directory preset %q", s.Directory)
	}
	if s.Email != "" && (!strings.Contains(s.Email, "@") || strings.ContainsAny(s.Email, " \t\n,")) {
		return invalid("%q is not an e-mail address", s.Email)
	}
	if (s.EABKID == "") != (s.EABHMAC == "") {
		return invalid("external account binding needs both the key id and the HMAC key")
	}
	if s.Directory == DirZeroSSL && s.EABKID == "" {
		return invalid("ZeroSSL needs external account binding: the EAB key id and HMAC from the ZeroSSL dashboard")
	}
	switch s.Challenge {
	case ChallengeHTTP01, ChallengeDNS01:
	default:
		return invalid("challenge must be %s or %s", ChallengeHTTP01, ChallengeDNS01)
	}
	if s.Mode == ModeACME && len(s.Names) == 0 {
		return invalid("at least one DNS name is needed - the name this system is reached by")
	}
	seen := map[string]bool{}
	for _, n := range s.Names {
		if err := ValidName(n, s.Challenge == ChallengeDNS01); err != nil {
			return err
		}
		if seen[n] {
			return invalid("%q is listed twice", n)
		}
		seen[n] = true
	}
	if s.Challenge == ChallengeDNS01 {
		p := ProviderByID(s.DNSProvider)
		if p == nil {
			return invalid("choose a DNS provider for the DNS-01 challenge")
		}
		if s.Mode == ModeACME {
			for _, f := range p.Fields {
				if !f.Optional && s.DNSCredentials[f.Key] == "" {
					return invalid("%s: %s is missing", p.Name, f.Label)
				}
			}
		}
		for k, v := range s.DNSCredentials {
			f := p.field(k)
			if f == nil {
				return invalid("%s has no field %q", p.Name, k)
			}
			if len(f.Options) > 0 && !slices.Contains(f.Options, v) {
				return invalid("%s: %s must be one of %s", p.Name, f.Label, strings.Join(f.Options, ", "))
			}
		}
	}
	return nil
}

// DirectoryURLFor is the URL the settings point at.
func (s Settings) DirectoryURLFor() string {
	if s.Directory == DirCustom {
		return s.DirectoryURL
	}
	return directoryURLs[s.Directory]
}

// TestDirectoryURL is where a Test run goes: Let's Encrypt's staging for both Let's Encrypt
// presets, and the configured directory itself for ZeroSSL and a custom CA (neither has a
// staging twin; a test against a step-ca costs nothing).
func (s Settings) TestDirectoryURL() string {
	switch s.Directory {
	case DirLetsEncrypt, DirLetsEncryptStaging:
		return directoryURLs[DirLetsEncryptStaging]
	}
	return s.DirectoryURLFor()
}

// ---- the DNS providers ------------------------------------------------------------------------

// Field is one input of a provider.
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"` // English; the UI translates it
	Secret      bool   `json:"secret,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Options makes the field a choice of these values, the first the default (OVH's endpoint).
	Options []string `json:"options,omitempty"`
}

// Provider is one DNS-01 provider the binary carries.
type Provider struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
	// Note is a hint for the page (English; translated there).
	Note string `json:"note,omitempty"`
}

func (p *Provider) field(key string) *Field {
	for i := range p.Fields {
		if p.Fields[i].Key == key {
			return &p.Fields[i]
		}
	}
	return nil
}

// Providers is the set D-49 chose (2026-09-09): the light ones a home box behind NAT is likely
// to use, plus exec for everything else. deSEC, INWX and RFC 2136 were dropped for the binary
// size and can come back on request. OVH came on request (issue #13), with a client of its own
// (ovh.go).
var Providers = []Provider{
	{ID: "cloudflare", Name: "Cloudflare", Fields: []Field{{Key: "api_token", Label: "API token", Secret: true}},
		Note: "An API token with Zone:DNS:Edit (and Zone:Zone:Read) for the zone; not the global API key."},
	{ID: "hetzner", Name: "Hetzner DNS", Fields: []Field{{Key: "api_token", Label: "API token", Secret: true}}},
	{ID: "netcup", Name: "netcup", Fields: []Field{{Key: "customer", Label: "Customer number"}, {Key: "api_key", Label: "API key", Secret: true},
		{Key: "api_password", Label: "API password", Secret: true}},
		Note: "netcup propagates slowly; an issue can take a quarter of an hour."},
	{ID: "duckdns", Name: "DuckDNS", Fields: []Field{{Key: "token", Label: "Token", Secret: true}},
		Note: "DuckDNS holds one TXT record per domain: order one name at a time."},
	{ID: "ovh", Name: "OVHcloud", Fields: []Field{{Key: "endpoint", Label: "Endpoint", Options: ovhEndpointNames}, {Key: "application_key", Label: "Application key"},
		{Key: "application_secret", Label: "Application secret", Secret: true}, {Key: "consumer_key", Label: "Consumer key", Secret: true}},
		Note: "A token from the createToken page of the endpoint (eu.api.ovh.com, ca.api.ovh.com or api.us.ovhcloud.com) with GET, POST, PUT and DELETE on /domain/zone/*."},
	{ID: "exec", Name: "Script (exec)", Fields: []Field{{Key: "program", Label: "Program", Placeholder: "/usr/local/etc/config/acme-dns.sh"},
		{Key: "mode", Label: "Mode", Optional: true, Placeholder: "(default) or RAW"}},
		Note: "Runs as the occulite user: program present|cleanup <domain> <token> <keyAuth> (or, in mode RAW, <fqdn> <record>)."},
}

// ProviderByID looks a provider up; nil for an unknown id.
func ProviderByID(id string) *Provider {
	for i := range Providers {
		if Providers[i].ID == id {
			return &Providers[i]
		}
	}
	return nil
}

// SortedNames is names, sorted, for a stable answer.
func SortedNames(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}
