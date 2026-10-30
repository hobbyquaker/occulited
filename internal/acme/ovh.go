package acme

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
)

// OVH (OVHcloud) as a DNS-01 provider (issue #13): a small client of its signed REST API, not
// lego's provider - that one pulls in go-ovh and an ini parser for three calls (+108 KiB of
// binary on amd64 against this file's few KiB). Classic authentication: an application key and
// secret and a consumer key from the createToken page; OAuth2 clients can follow.
//
// The flow is lego's own provider's: the zone by the SOA of the challenge name (or, when DNS
// cannot say, the longest zone of the account that holds it), POST the TXT record, POST the
// zone's refresh - OVH serves a changed zone only after it - and on cleanup DELETE the record by
// the id the create returned, then refresh again.

// ovhEndpoints are the API roots by the names go-ovh and lego use.
var ovhEndpoints = map[string]string{
	"ovh-eu": "https://eu.api.ovh.com/1.0",
	"ovh-ca": "https://ca.api.ovh.com/1.0",
	"ovh-us": "https://api.us.ovhcloud.com/1.0",
}

// ovhEndpointNames is the order the page offers them in.
var ovhEndpointNames = []string{"ovh-eu", "ovh-ca", "ovh-us"}

type ovhProvider struct {
	base                      string // the endpoint's root, ".../1.0"
	appKey, appSecret, consum string
	http                      *http.Client
	now                       func() time.Time
	// findZone is lego's SOA lookup; a test replaces it
	findZone func(fqdn string) (string, error)
	ttl      int

	mu      sync.Mutex
	delta   time.Duration // local clock minus OVH's, from /auth/time
	haveDel bool
	records map[string]ovhRecordRef // by challenge token
}

type ovhRecordRef struct {
	zone string
	id   int64
}

type ovhRecord struct {
	ID        int64  `json:"id,omitempty"`
	FieldType string `json:"fieldType"`
	SubDomain string `json:"subDomain"`
	Target    string `json:"target"`
	TTL       int    `json:"ttl,omitempty"`
}

// newOVHProvider builds the provider from the page's fields.
func newOVHProvider(f map[string]string) (*ovhProvider, error) {
	ep := f["endpoint"]
	if ep == "" {
		ep = "ovh-eu"
	}
	base, ok := ovhEndpoints[ep]
	if !ok {
		return nil, fmt.Errorf("ovh: unknown endpoint %q (ovh-eu, ovh-ca or ovh-us)", ep)
	}
	p := &ovhProvider{base: base, appKey: f["application_key"], appSecret: f["application_secret"], consum: f["consumer_key"],
		http: &http.Client{Timeout: 30 * time.Second}, now: time.Now, findZone: dns01.FindZoneByFqdn, ttl: dns01.DefaultTTL,
		records: map[string]ovhRecordRef{}}
	if p.appKey == "" || p.appSecret == "" || p.consum == "" {
		return nil, errors.New("ovh: the application key, the application secret and the consumer key are all needed")
	}
	return p, nil
}

// Timeout: OVH applies a zone a minute or two after the refresh, its secondaries later; lego's
// default of one minute is too short for that.
func (p *ovhProvider) Timeout() (timeout, interval time.Duration) {
	return 10 * time.Minute, 10 * time.Second
}

// Present creates the TXT record and applies the zone.
func (p *ovhProvider) Present(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	zone, err := p.zoneFor(info.EffectiveFQDN)
	if err != nil {
		return err
	}
	sub, err := dns01.ExtractSubDomain(info.EffectiveFQDN, zone)
	if err != nil {
		return fmt.Errorf("ovh: %w", err)
	}
	var created ovhRecord
	if err := p.call(http.MethodPost, "/domain/zone/"+url.PathEscape(zone)+"/record", ovhRecord{FieldType: "TXT", SubDomain: sub, Target: info.Value, TTL: p.ttl}, &created); err != nil {
		return fmt.Errorf("ovh: creating the TXT record %s in zone %s: %w", info.EffectiveFQDN, zone, err)
	}
	p.mu.Lock()
	p.records[token] = ovhRecordRef{zone: zone, id: created.ID}
	p.mu.Unlock()
	if err := p.refresh(zone); err != nil {
		return err
	}
	return nil
}

// CleanUp deletes the record Present created, by its id, and applies the zone. lego cleans up
// after a failed Present too: with no record there is nothing to do, and no second message
// beside the Present's error.
func (p *ovhProvider) CleanUp(domain, token, keyAuth string) error {
	p.mu.Lock()
	ref, ok := p.records[token]
	p.mu.Unlock()
	if !ok {
		return nil
	}
	if err := p.call(http.MethodDelete, "/domain/zone/"+url.PathEscape(ref.zone)+"/record/"+strconv.FormatInt(ref.id, 10), nil, nil); err != nil {
		return fmt.Errorf("ovh: deleting the TXT record %d in zone %s: %w", ref.id, ref.zone, err)
	}
	p.mu.Lock()
	delete(p.records, token)
	p.mu.Unlock()
	return p.refresh(ref.zone)
}

func (p *ovhProvider) refresh(zone string) error {
	if err := p.call(http.MethodPost, "/domain/zone/"+url.PathEscape(zone)+"/refresh", nil, nil); err != nil {
		return fmt.Errorf("ovh: applying zone %s (refresh): %w", zone, err)
	}
	return nil
}

// zoneFor is the OVH zone that holds fqdn: the SOA's zone as DNS answers it; when DNS cannot say
// (a split horizon, a resolver that refuses), the longest zone of the account fqdn ends in.
func (p *ovhProvider) zoneFor(fqdn string) (string, error) {
	soaZone, soaErr := p.findZone(fqdn)
	if soaErr == nil {
		return dns01.UnFqdn(soaZone), nil
	}
	var zones []string
	if err := p.call(http.MethodGet, "/domain/zone", nil, &zones); err != nil {
		return "", fmt.Errorf("ovh: finding the zone of %s: DNS: %v; the account's zones: %w", fqdn, soaErr, err)
	}
	name := strings.ToLower(dns01.UnFqdn(fqdn))
	best := ""
	for _, z := range zones {
		z = strings.ToLower(dns01.UnFqdn(z))
		if (name == z || strings.HasSuffix(name, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	if best == "" {
		return "", fmt.Errorf("ovh: none of the account's zones (%s) holds %s", strings.Join(zones, ", "), fqdn)
	}
	return best, nil
}

// ovhError is the API's error answer.
type ovhError struct {
	Status  int
	Class   string `json:"class"`
	Message string `json:"message"`
}

func (e *ovhError) Error() string {
	msg := e.Message
	if e.Class != "" {
		msg = e.Class + ": " + msg
	}
	s := fmt.Sprintf("HTTP %d: %s", e.Status, msg)
	if e.credentials() {
		s += " - the API refused the credentials: check the endpoint, the application key and secret and the consumer key, and that the token allows GET, POST, PUT and DELETE on /domain/zone/*"
	}
	return s
}

// credentials tells an authentication failure from any other error: 401 and 403 always, a 400
// that names the application key, the signature or the timestamp.
func (e *ovhError) credentials() bool {
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return true
	}
	m := strings.ToLower(e.Message)
	return e.Status == http.StatusBadRequest && (strings.Contains(m, "application") || strings.Contains(m, "signature") || strings.Contains(m, "timestamp") || strings.Contains(m, "consumer"))
}

// call is one signed request: body and answer are JSON, either may be nil.
func (p *ovhProvider) call(method, path string, body, out any) error {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	ts, err := p.timestamp()
	if err != nil {
		return err
	}
	target := p.base + path
	req, err := http.NewRequest(method, target, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json;charset=utf-8")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Ovh-Application", p.appKey)
	req.Header.Set("X-Ovh-Consumer", p.consum)
	req.Header.Set("X-Ovh-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Ovh-Signature", ovhSignature(p.appSecret, p.consum, method, target, raw, ts))
	return p.do(req, out)
}

// ovhSignature is "$1$" + hex(SHA1(AS+CK+METHOD+URL+BODY+TIMESTAMP)), the parts joined by "+".
func ovhSignature(appSecret, consumer, method, target string, body []byte, ts int64) string {
	h := sha1.New()
	h.Write([]byte(appSecret + "+" + consumer + "+" + method + "+" + target + "+" + string(body) + "+" + strconv.FormatInt(ts, 10)))
	return "$1$" + hex.EncodeToString(h.Sum(nil))
}

// timestamp is OVH's clock: the box's own corrected by the difference /auth/time showed once - a
// box without a synced clock still signs within OVH's window.
func (p *ovhProvider) timestamp() (int64, error) {
	p.mu.Lock()
	have, d := p.haveDel, p.delta
	p.mu.Unlock()
	if !have {
		req, err := http.NewRequest(http.MethodGet, p.base+"/auth/time", nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Accept", "application/json")
		var server int64
		if err := p.do(req, &server); err != nil {
			return 0, fmt.Errorf("the API's time: %w", err)
		}
		d = p.now().Sub(time.Unix(server, 0))
		p.mu.Lock()
		p.delta, p.haveDel = d, true
		p.mu.Unlock()
	}
	return p.now().Add(-d).Unix(), nil
}

func (p *ovhProvider) do(req *http.Request, out any) error {
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := &ovhError{Status: resp.StatusCode}
		if json.Unmarshal(data, e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
			if len(e.Message) > 200 {
				e.Message = e.Message[:200]
			}
		}
		return e
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected answer from %s: %w", req.URL.Path, err)
	}
	return nil
}
