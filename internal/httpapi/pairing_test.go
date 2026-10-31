package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/pairing"
)

// task 219 through HTTP: ask, reveal, the list with the code, approve, the token once; the
// switch; a PATCH of the label.
func TestPairingRoutes(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "occulited.json")
	_ = config.Save(cfgPath, config.Default())
	a := &AuthAPI{Store: store, ConfigFile: cfgPath, Pairing: &pairing.Manager{Minter: store, Enabled: func() bool { return PairingEnabled(cfgPath) }}}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cn := strings.Repeat("ab", 16)
	raw, _ := hex.DecodeString(cn)
	sum := sha256.Sum256(raw)
	st, out, body := do(t, srv, "POST", "/api/auth/v1/pairing/request", `{"app":"hm2mqtt.js","instance":"nas","access":{"devices":"operate","names":"read"},"commit":"`+hex.EncodeToString(sum[:])+`"}`, nil)
	if st != 202 || out["poll"] == nil {
		t.Fatalf("ask: %d %s", st, body)
	}
	id, poll := out["id"].(string), out["poll"].(string)
	hdr := map[string]string{"Authorization": "Pairing " + poll}
	if st, out, body := do(t, srv, "GET", "/api/auth/v1/pairing/request/"+id+"?client_nonce="+cn, "", hdr); st != 200 || out["state"] != "pending" {
		t.Fatalf("reveal: %d %s", st, body)
	}
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/pairing/request/"+id, "", map[string]string{"Authorization": "Pairing nope"}); st != 403 {
		t.Fatalf("another secret: %d", st)
	}
	st, out, body = do(t, srv, "GET", "/api/auth/v1/pairing", "", nil)
	reqs, _ := out["requests"].([]any)
	if st != 200 || out["enabled"] != true || len(reqs) != 1 || strings.Contains(body, poll) {
		t.Fatalf("list: %d %s", st, body)
	}
	code := reqs[0].(map[string]any)["code"].(string)
	if st, _, body := do(t, srv, "POST", "/api/auth/v1/pairing/"+id+"/approve", `{"code":"`+code+`"}`, nil); st != 200 || !strings.Contains(body, "hm2mqtt-js-nas") {
		t.Fatalf("approve: %d %s", st, body)
	}
	st, out, body = do(t, srv, "GET", "/api/auth/v1/pairing/request/"+id+"?wait=1", "", hdr)
	if st != 200 || out["state"] != "approved" || !strings.HasPrefix(out["token"].(string), "olt_") {
		t.Fatalf("token: %d %s", st, body)
	}
	if st, _, _ := do(t, srv, "GET", "/api/auth/v1/pairing/request/"+id, "", hdr); st != 404 {
		t.Fatalf("twice: %d", st)
	}
	if st, _, body := do(t, srv, "PATCH", "/api/auth/v1/tokens/hm2mqtt-js-nas", `{"label":"MQTT"}`, nil); st != 200 || !strings.Contains(body, `"label":"MQTT"`) {
		t.Fatalf("patch: %d %s", st, body)
	}
	if st, _, _ := do(t, srv, "PATCH", "/api/auth/v1/tokens/hm2mqtt-js-nas", `{"scopes":["*"]}`, nil); st != 422 {
		t.Fatalf("widen: %d", st)
	}
	// the switch
	if st, out, _ := do(t, srv, "PUT", "/api/auth/v1/pairing/settings", `{"enabled":false}`, nil); st != 200 || out["enabled"] != false {
		t.Fatalf("switch: %d", st)
	}
	if st, out, _ := do(t, srv, "POST", "/api/auth/v1/pairing/request", `{"app":"x","access":{"devices":"read"},"commit":"`+hex.EncodeToString(sum[:])+`"}`, nil); st != 403 || out["error"] != "pairing-off" {
		t.Fatalf("off: %d %v", st, out)
	}
	if st, _, _ := do(t, srv, "PUT", "/api/auth/v1/pairing/settings", `{}`, nil); st != 422 {
		t.Fatalf("empty switch: %d", st)
	}
}

// pairBackup is a system with an administrator and a clock, for occulited task 135.
type pairBackup struct {
	srv    *httptest.Server
	store  *auth.Store
	mgr    *pairing.Manager
	now    *time.Time
	admin  map[string]string // the administrator's session
	cookie string
}

func newPairBackup(t *testing.T) *pairBackup {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	clock := func() time.Time { return now }
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s"), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "occulited.json")
	_ = config.Save(cfgPath, config.Default())
	mgr := &pairing.Manager{Minter: store, Now: clock}
	a := &AuthAPI{Store: store, ConfigFile: cfgPath, Pairing: mgr}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	t.Cleanup(srv.Close)
	if st, out := call(t, srv, rpHost, "POST", "/api/auth/v1/setup", `{"username":"admin","password":"secret123"}`, nil); st != 200 {
		t.Fatalf("setup: %d %v", st, out)
	}
	cookie := cookieOf(t, srv, `{"username":"admin","password":"secret123"}`)
	return &pairBackup{srv: srv, store: store, mgr: mgr, now: &now, cookie: cookie, admin: map[string]string{"Cookie": CookieName + "=" + cookie}}
}

// ask makes a revealed request from addr: its id, poll secret and code.
func (p *pairBackup) ask(t *testing.T, addr, app, access string) (id, poll, code string) {
	t.Helper()
	cn := strings.Repeat("ab", 16)
	raw, _ := hex.DecodeString(cn)
	sum := sha256.Sum256(raw)
	st, out := call(t, p.srv, rpHost, "POST", "/api/auth/v1/pairing/request", `{"app":"`+app+`","access":`+access+`,"commit":"`+hex.EncodeToString(sum[:])+`"}`, map[string]string{"X-Forwarded-For": addr})
	if st != 202 {
		t.Fatalf("ask: %d %v", st, out)
	}
	id, poll = out["id"].(string), out["poll"].(string)
	if st, out := call(t, p.srv, rpHost, "GET", "/api/auth/v1/pairing/request/"+id+"?client_nonce="+cn, "", map[string]string{"Authorization": "Pairing " + poll}); st != 200 {
		t.Fatalf("reveal: %d %v", st, out)
	}
	for _, v := range p.mgr.Pending() {
		if v.ID == id {
			return id, poll, v.Code
		}
	}
	t.Fatal("the request is not on the card")
	return
}

func (p *pairBackup) ticket(t *testing.T, path string, confirm bool, hdr map[string]string) string {
	t.Helper()
	body := `{"path":"` + path + `"}`
	if confirm {
		body = `{"path":"` + path + `","confirm":true,"password":"secret123"}`
	}
	st, out := call(t, p.srv, rpHost, "POST", "/api/auth/v1/ticket", body, hdr)
	if st != 200 {
		t.Fatalf("ticket: %d %v", st, out)
	}
	return out["ticket"].(string)
}

func (p *pairBackup) with(extra ...string) map[string]string {
	h := map[string]string{}
	for k, v := range p.admin {
		h[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

// occulited task 135: approving a request for backups without a valid confirmation is refused on
// the API, and the request stays; a wrong code still rejects it and mutes, with the confirmation.
func TestPairingBackupNeedsConfirmation(t *testing.T) {
	const backup = `{"backup":"download"}`
	approve := func(id string) string { return "/api/auth/v1/pairing/" + id + "/approve" }
	for _, c := range []struct {
		name   string
		header func(t *testing.T, p *pairBackup, id string) map[string]string
	}{
		{"no ticket", func(_ *testing.T, p *pairBackup, _ string) map[string]string { return p.with() }},
		{"a made-up ticket", func(_ *testing.T, p *pairBackup, _ string) map[string]string {
			return p.with("X-Occulite-Confirm", "AAAAAAAAAAAAAAAAAAAAAAAAAA")
		}},
		{"a stale ticket", func(t *testing.T, p *pairBackup, id string) map[string]string {
			tk := p.ticket(t, PairingApprovePath(id), true, p.admin)
			*p.now = p.now.Add(auth.TicketTTL + time.Second)
			return p.with("X-Occulite-Confirm", tk)
		}},
		{"a plain ticket", func(t *testing.T, p *pairBackup, id string) map[string]string {
			return p.with("X-Occulite-Confirm", p.ticket(t, PairingApprovePath(id), false, p.admin))
		}},
		{"a ticket for another request", func(t *testing.T, p *pairBackup, _ string) map[string]string {
			return p.with("X-Occulite-Confirm", p.ticket(t, PairingApprovePath("00ff00ff00ff00ff"), true, p.admin))
		}},
		{"a ticket for another act", func(t *testing.T, p *pairBackup, _ string) map[string]string {
			return p.with("X-Occulite-Confirm", p.ticket(t, "/api/system/v1/ssh/keys", true, p.admin))
		}},
		{"another session's ticket", func(t *testing.T, p *pairBackup, id string) map[string]string {
			other := cookieOf(t, p.srv, `{"username":"admin","password":"secret123"}`)
			return p.with("X-Occulite-Confirm", p.ticket(t, PairingApprovePath(id), true, map[string]string{"Cookie": CookieName + "=" + other}))
		}},
		{"a spent ticket", func(t *testing.T, p *pairBackup, id string) map[string]string {
			tk := p.ticket(t, PairingApprovePath(id), true, p.admin)
			if !p.store.RedeemConfirmed(tk, PairingApprovePath(id), p.cookie) {
				t.Fatal("the first redemption")
			}
			return p.with("X-Occulite-Confirm", tk)
		}},
		{"an administrator's token", func(t *testing.T, p *pairBackup, _ string) map[string]string {
			secret, err := p.store.CreateToken("full", auth.TokenOptions{Scopes: auth.Scopes{auth.ScopeAuthAdmin}})
			if err != nil {
				t.Fatal(err)
			}
			return map[string]string{"Authorization": "Bearer " + secret, "X-Occulite-Confirm": "AAAAAAAAAAAAAAAAAAAAAAAAAA"}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPairBackup(t)
			id, poll, code := p.ask(t, "192.168.1.50", "occulite-backup", backup)
			st, out := call(t, p.srv, rpHost, "POST", approve(id), `{"code":"`+code+`"}`, c.header(t, p, id))
			if st != 403 || out["error"] != "confirm-required" {
				t.Fatalf("%d %v", st, out)
			}
			// still pending, nothing minted, nothing muted: a real confirmation still works
			if n := len(p.mgr.Pending()); n != 1 {
				t.Fatalf("pending %d", n)
			}
			if tokens := p.store.Tokens(); len(tokens) > 1 || (len(tokens) == 1 && tokens[0].Name != "full") {
				t.Fatalf("tokens %v", tokens)
			}
			st, out = call(t, p.srv, rpHost, "POST", approve(id), `{"code":"`+code+`"}`, p.with("X-Occulite-Confirm", p.ticket(t, PairingApprovePath(id), true, p.admin)))
			if st != 200 {
				t.Fatalf("confirmed: %d %v", st, out)
			}
			if st, out = call(t, p.srv, rpHost, "GET", "/api/auth/v1/pairing/request/"+id, "", map[string]string{"Authorization": "Pairing " + poll}); st != 200 || out["state"] != "approved" {
				t.Fatalf("poll: %d %v", st, out)
			}
		})
	}

	t.Run("the wrong code, confirmed", func(t *testing.T) {
		p := newPairBackup(t)
		id, _, _ := p.ask(t, "192.168.1.50", "occulite-backup", backup)
		st, out := call(t, p.srv, rpHost, "POST", approve(id), `{"code":"000000x"}`, p.with("X-Occulite-Confirm", p.ticket(t, PairingApprovePath(id), true, p.admin)))
		if st != 409 || out["error"] != "wrong-code" {
			t.Fatalf("%d %v", st, out)
		}
		if len(p.mgr.Pending()) != 0 {
			t.Fatal("still pending")
		}
		cn := strings.Repeat("ab", 16)
		raw, _ := hex.DecodeString(cn)
		sum := sha256.Sum256(raw)
		st, out = call(t, p.srv, rpHost, "POST", "/api/auth/v1/pairing/request", `{"app":"occulite-backup","access":`+backup+`,"commit":"`+hex.EncodeToString(sum[:])+`"}`, map[string]string{"X-Forwarded-For": "192.168.1.50"})
		if st != 429 {
			t.Fatalf("no mute: %d %v", st, out)
		}
	})

	t.Run("a wrong password makes no ticket", func(t *testing.T) {
		p := newPairBackup(t)
		id, _, _ := p.ask(t, "192.168.1.50", "occulite-backup", backup)
		st, out := call(t, p.srv, rpHost, "POST", "/api/auth/v1/ticket", `{"path":"`+PairingApprovePath(id)+`","confirm":true,"password":"nope"}`, p.admin)
		if st != 401 {
			t.Fatalf("%d %v", st, out)
		}
		if len(p.mgr.Pending()) != 1 {
			t.Fatal("the request is gone")
		}
	})

	t.Run("an ordinary request asks for no ticket and gets no range", func(t *testing.T) {
		p := newPairBackup(t)
		id, _, code := p.ask(t, "192.168.1.50", "hm2mqtt", `{"devices":"read"}`)
		if st, out := call(t, p.srv, rpHost, "POST", approve(id), `{"code":"`+code+`"}`, p.admin); st != 200 {
			t.Fatalf("%d %v", st, out)
		}
		if tk := p.store.Tokens(); len(tk) != 1 || len(tk[0].IPs) != 0 {
			t.Fatalf("tokens %+v", tk)
		}
	})
}

// occulited task 135: the token is held to the range the request's address gives, through the API.
func TestPairingBackupTokenRange(t *testing.T) {
	for _, c := range []struct{ from, ips, inside, outside string }{
		{"192.168.1.50", "192.168.1.50/32", "192.168.1.50", "192.168.1.51"},
		{"2001:db8:1:2:aaaa::1", "2001:db8:1:2::/64", "2001:db8:1:2:bbbb::9", "2001:db8:1:3::1"},
		{"fe80::1234", "fe80::1234/128", "fe80::1234", "fe80::1235"},
	} {
		t.Run(c.from, func(t *testing.T) {
			p := newPairBackup(t)
			id, poll, code := p.ask(t, c.from, "occulite-backup", `{"backup":"download"}`)
			var view map[string]any
			_, list := call(t, p.srv, rpHost, "GET", "/api/auth/v1/pairing", "", p.admin)
			view = list["requests"].([]any)[0].(map[string]any)
			if view["needs_confirmation"] != true || view["token_ips"].([]any)[0] != c.ips {
				t.Fatalf("card %v", view)
			}
			st, out := call(t, p.srv, rpHost, "POST", "/api/auth/v1/pairing/"+id+"/approve", `{"code":"`+code+`"}`, p.with("X-Occulite-Confirm", p.ticket(t, PairingApprovePath(id), true, p.admin)))
			if st != 200 {
				t.Fatalf("approve: %d %v", st, out)
			}
			st, out = call(t, p.srv, rpHost, "GET", "/api/auth/v1/pairing/request/"+id, "", map[string]string{"Authorization": "Pairing " + poll})
			if st != 200 || out["ips"].([]any)[0] != c.ips || out["scopes"].([]any)[0] != "backup" {
				t.Fatalf("poll: %d %v", st, out)
			}
			secret := out["token"].(string)
			if s := p.store.ValidateFrom(secret, c.inside); s == nil || !s.Has(auth.ScopeBackup) || s.Has(auth.ScopeRadioKeys) || s.Has(auth.ScopeSystemRead) {
				t.Fatalf("inside: %+v", s)
			}
			if p.store.ValidateFrom(secret, c.outside) != nil {
				t.Fatal("outside")
			}
			if st, _ := call(t, p.srv, rpHost, "GET", "/api/auth/v1/me", "", map[string]string{"Authorization": "Bearer " + secret, "X-Forwarded-For": c.outside}); st != 401 {
				t.Fatalf("through the API from outside: %d", st)
			}
		})
	}
}

// occulited task 135: a passkey of the admin's own account confirms the approval, and nothing else.
func TestPairingBackupPasskey(t *testing.T) {
	p := newPairBackup(t)
	id, _, code := p.ask(t, "192.168.1.50", "occulite-backup", `{"backup":"download"}`)
	path := PairingApprovePath(id)
	method := func(host, q string) map[string]any {
		t.Helper()
		_, out := call(t, p.srv, host, "GET", "/api/auth/v1/confirm?path="+q, "", p.admin)
		return out
	}
	if m := method(rpHost, path); m["passkey"] != nil || m["method"] != "password" {
		t.Fatalf("no key yet: %v", m)
	}
	key := newSoftKey(t, true, true)
	register(t, p.srv, p.cookie, "secret123", "blue", key)
	if m := method(rpHost, path); m["passkey"] != true {
		t.Fatalf("with a key: %v", m)
	}
	// only this kind of act (D-104 stands for the others), only on a name a key can work on
	if m := method(rpHost, "/api/system/v1/ssh/keys"); m["passkey"] != nil {
		t.Fatalf("another act: %v", m)
	}
	if m := method("192.168.1.10", path); m["passkey"] != nil {
		t.Fatalf("on an address: %v", m)
	}
	if st, out := call(t, p.srv, rpHost, "POST", "/api/auth/v1/confirm/passkey", `{"path":"/api/system/v1/ssh/keys"}`, p.admin); st != 409 || out["error"] != "no-passkey" {
		t.Fatalf("begin for another act: %d %v", st, out)
	}
	secret, err := p.store.CreateToken("full", auth.TokenOptions{Scopes: auth.Scopes{auth.ScopeAuthAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	if st, out := call(t, p.srv, rpHost, "POST", "/api/auth/v1/confirm/passkey", `{"path":"`+path+`"}`, map[string]string{"Authorization": "Bearer " + secret}); st != 409 || out["error"] != "no-session" {
		t.Fatalf("a token: %d %v", st, out)
	}
	begin := func() map[string]any {
		t.Helper()
		st, out := call(t, p.srv, rpHost, "POST", "/api/auth/v1/confirm/passkey", `{"path":"`+path+`"}`, p.admin)
		if st != 200 {
			t.Fatalf("begin: %d %v", st, out)
		}
		return out
	}
	// a key the account does not have, then one that did not verify the user: refused, no ticket
	for name, k := range map[string]*softKey{"foreign": newSoftKey(t, true, true), "unverified": {priv: key.priv, id: key.id, rk: true, uv: false}} {
		out := begin()
		st, res := call(t, p.srv, rpHost, "POST", "/api/auth/v1/confirm/passkey/finish", mustJSON(map[string]any{"confirmation": out["confirmation"], "response": json.RawMessage(k.assert(challengeOf(t, out), rpOrigin, rpHost, nil, 5))}), p.admin)
		if st != 401 || res["ticket"] != nil {
			t.Fatalf("%s: %d %v", name, st, res)
		}
	}
	out := begin()
	st, res := call(t, p.srv, rpHost, "POST", "/api/auth/v1/confirm/passkey/finish", mustJSON(map[string]any{"confirmation": out["confirmation"], "response": json.RawMessage(key.assert(challengeOf(t, out), rpOrigin, rpHost, nil, 7))}), p.admin)
	if st != 200 || res["ticket"] == nil {
		t.Fatalf("finish: %d %v", st, res)
	}
	if st, res := call(t, p.srv, rpHost, "POST", "/api/auth/v1/pairing/"+id+"/approve", `{"code":"`+code+`"}`, p.with("X-Occulite-Confirm", res["ticket"].(string))); st != 200 {
		t.Fatalf("approve: %d %v", st, res)
	}
}

// occulited task 135: with the login switched off there is nobody to ask, and the confirmation is
// the same ticket without a password.
func TestPairingBackupAuthOff(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(dir, auth.Options{SessionDir: filepath.Join(dir, "s")})
	if err != nil {
		t.Fatal(err)
	}
	mgr := &pairing.Manager{Minter: store}
	a := &AuthAPI{Store: store, Off: true, Pairing: mgr}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	defer srv.Close()
	p := &pairBackup{srv: srv, store: store, mgr: mgr}
	id, _, code := p.ask(t, "192.168.1.50", "occulite-backup", `{"backup":"download"}`)
	if st, out := call(t, srv, rpHost, "POST", "/api/auth/v1/pairing/"+id+"/approve", `{"code":"`+code+`"}`, nil); st != 403 {
		t.Fatalf("no ticket: %d %v", st, out)
	}
	st, out := call(t, srv, rpHost, "POST", "/api/auth/v1/ticket", `{"path":"`+PairingApprovePath(id)+`","confirm":true}`, nil)
	if st != 200 {
		t.Fatalf("ticket: %d %v", st, out)
	}
	if st, out := call(t, srv, rpHost, "POST", "/api/auth/v1/pairing/"+id+"/approve", `{"code":"`+code+`"}`, map[string]string{"X-Occulite-Confirm": out["ticket"].(string)}); st != 200 {
		t.Fatalf("approve: %d %v", st, out)
	}
}
