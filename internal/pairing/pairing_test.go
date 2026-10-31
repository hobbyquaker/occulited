package pairing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
)

type fakeMinter struct {
	mu    sync.Mutex
	names []string
	last  auth.TokenClient
	sc    auth.Scopes
	ips   []string
}

func (f *fakeMinter) CreatePairedToken(base string, scopes auth.Scopes, ips []string, c auth.TokenClient) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = append(f.names, base)
	f.last, f.sc, f.ips = c, scopes, ips
	return base, "olt_00112233445566778899aabbccddeeff", nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newMgr() (*Manager, *fakeMinter, *clock) {
	f := &fakeMinter{}
	c := &clock{t: time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC)}
	return &Manager{Minter: f, Now: c.now, Local: func(a string) bool { return !strings.HasPrefix(a, "8.") }}, f, c
}

// a client's half: its nonce and the commitment to it
func clientHalf(b byte) (string, string) {
	cn := make([]byte, 16)
	for i := range cn {
		cn[i] = b
	}
	sum := sha256.Sum256(cn)
	return hex.EncodeToString(cn), hex.EncodeToString(sum[:])
}

func ask(app, instance, commit string) Ask {
	return Ask{App: app, AppVersion: "3.2.0", Instance: instance, Access: map[string]string{"devices": "operate", "names": "read"}, Purpose: map[string]string{"devices": "publishes values to MQTT", "bogus": "x"}, Commit: commit}
}

func TestScopesFor(t *testing.T) {
	s, err := ScopesFor(map[string]string{"devices": "administer", "names": "configure", "system": "read"})
	if err != nil || strings.Join(s.Strings(), " ") != strings.Join(auth.Scopes{auth.ScopeRPCRead, auth.ScopeRPCOperate, auth.ScopeRPCConfigure, auth.ScopeRPCAdmin, auth.ScopeMetaRead, auth.ScopeMetaWrite, auth.ScopeSystemRead, auth.ScopeLogsRead}.Normalize().Strings(), " ") {
		t.Errorf("%v %v", s, err)
	}
	for _, bad := range []map[string]string{{"system": "administer"}, {"power": "read"}, {"devices": "none"}, {}} {
		if _, err := ScopesFor(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	// nothing of what pairing never grants is in any level
	for _, lv := range levels {
		for _, sc := range lv {
			for _, x := range sc {
				for _, never := range auth.PairingNever {
					if x == never {
						t.Errorf("%s in a level", x)
					}
				}
			}
		}
	}
}

func TestCode(t *testing.T) {
	a := Code([]byte{1, 2}, []byte{3}, []byte{4})
	if len(a) != 6 || a != Code([]byte{1, 2}, []byte{3}, []byte{4}) || a == Code([]byte{1, 2}, []byte{3}, []byte{5}) {
		t.Errorf("code %s", a)
	}
	// a fixed vector for the client's implementation (task 196): nonce 00..0f, client 10..1f, no fingerprint
	n, c := make([]byte, 16), make([]byte, 16)
	for i := range n {
		n[i], c[i] = byte(i), byte(16+i)
	}
	sum := sha256.Sum256(append(append([]byte{}, n...), c...))
	want := (uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])) % 1_000_000
	if got := Code(n, c, nil); got != strings.Repeat("0", 6-len(itoa(want)))+itoa(want) {
		t.Errorf("vector %s", got)
	}
}

func itoa(n uint32) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// openccu-lite task 307: an ask may name installed addons whose ingress the program wants - the
// scope addon:<id> each, alone or beside the areas; the card names them, the token's client record
// and the approved answer carry the ids; an addon that is not installed, a bad id, a system without
// the hook, or more than MaxAddons is refused as invalid.
func TestAddons(t *testing.T) {
	m, f, _ := newMgr()
	m.AddonName = func(id string) (string, bool) {
		names := map[string]string{"openccu-loom": "OpenCCU-Loom", "hmm": "Homematic Manager"}
		n, ok := names[id]
		return n, ok
	}
	cn, commit := clientHalf(9)
	// addons alone, one of them twice
	a := Ask{App: "homematicip-local", Instance: "ha", Addons: []string{"openccu-loom", " openccu-loom", "hmm"}, Commit: commit}
	ans, err := m.Request(a, "192.168.1.60", nil)
	if err != nil {
		t.Fatalf("addons alone: %v", err)
	}
	if _, err := m.Poll(context.Background(), ans.ID, ans.Poll, cn, 0); err != nil {
		t.Fatal(err)
	}
	p := m.Pending()
	if len(p) != 1 || len(p[0].Addons) != 2 || p[0].Addons[0] != (AddonRef{ID: "openccu-loom", Name: "OpenCCU-Loom"}) || p[0].Addons[1].ID != "hmm" {
		t.Fatalf("card %+v", p)
	}
	if strings.Join(p[0].Scopes, " ") != "addon:hmm addon:openccu-loom" || len(p[0].Access) != 0 {
		t.Fatalf("scopes %v access %v", p[0].Scopes, p[0].Access)
	}
	// openccu-lite B-296: the list answers the access as an object, {} rather than null
	if p[0].Access == nil {
		t.Fatal("an addons-only request's access is nil")
	}
	if js, _ := json.Marshal(p[0]); !strings.Contains(string(js), `"access":{}`) {
		t.Fatalf("the card's JSON: %s", js)
	}
	client, _, err := m.Approve(ans.ID, p[0].Code, "admin", false)
	if err != nil || strings.Join(client.Addons, ",") != "openccu-loom,hmm" || strings.Join(f.sc.Strings(), " ") != "addon:hmm addon:openccu-loom" {
		t.Fatalf("approve: %v %+v minted %v", err, client, f.sc)
	}
	r, err := m.Poll(context.Background(), ans.ID, ans.Poll, "", 0)
	if err != nil || r.State != StateApproved || strings.Join(r.Addons, ",") != "openccu-loom,hmm" {
		t.Fatalf("answer %v %+v", err, r)
	}
	if js, _ := json.Marshal(r); !strings.Contains(string(js), `"access":{}`) {
		t.Fatalf("the approved answer's JSON: %s", js)
	}
	if js, _ := json.Marshal(client); !strings.Contains(string(js), `"access":{}`) {
		t.Fatalf("the client record's JSON: %s", js)
	}
	// beside the areas: both sets of scopes
	cn2, commit2 := clientHalf(10)
	b := ask("hm2mqtt.js", "nas", commit2)
	b.Addons = []string{"hmm"}
	ans2, err := m.Request(b, "192.168.1.61", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Poll(context.Background(), ans2.ID, ans2.Poll, cn2, 0); err != nil {
		t.Fatal(err)
	}
	for _, v := range m.Pending() {
		if v.ID == ans2.ID && strings.Join(v.Scopes, " ") != "addon:hmm meta:read rpc:operate" {
			t.Errorf("areas and an addon: %v", v.Scopes)
		}
	}
	// refusals
	for _, bad := range []Ask{
		{App: "x", Addons: []string{"mosquitto"}, Commit: commit},                             // not installed
		{App: "x", Addons: []string{"Bad Id"}, Commit: commit},                                // no addon id
		{App: "x", Addons: []string{}, Commit: commit},                                        // nothing at all
		{App: "x", Addons: tooMany(MaxAddons + 1), Commit: commit},                            // more than MaxAddons
		{App: "x", Access: map[string]string{"devices": "none"}, Addons: nil, Commit: commit}, // explicit none, no addon
	} {
		if _, err := m.Request(bad, "192.168.1.70", nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad.Addons, err)
		}
	}
	m.AddonName = nil
	if _, err := m.Request(Ask{App: "x", Addons: []string{"hmm"}, Commit: commit}, "192.168.1.71", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("without the hook: %v", err)
	}
}

func tooMany(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "hmm" + strings.Repeat("x", i)
	}
	return out
}

// The whole flow: ask, reveal, the card with the code, approve with it, the token once.
func TestFlow(t *testing.T) {
	m, f, _ := newMgr()
	changes := 0
	m.OnChange = func() { changes++ }
	cn, commit := clientHalf(7)
	fp := []byte{0xAB, 0xCD}
	ans, err := m.Request(ask("hm2mqtt.js", "nas", commit), "192.168.1.50", fp)
	if err != nil || ans.ID == "" || ans.Poll == "" || ans.ExpiresIn != 300 || ans.Interval != 2 || ans.Fingerprint != "abcd" {
		t.Fatalf("%v %+v", err, ans)
	}
	if len(m.Pending()) != 0 {
		t.Fatal("a card before the reveal")
	}
	if _, err := m.Poll(context.Background(), ans.ID, "wrong", cn, 0); !errors.Is(err, ErrPoll) {
		t.Fatalf("another poll secret: %v", err)
	}
	bad, _ := clientHalf(8)
	if _, err := m.Poll(context.Background(), ans.ID, ans.Poll, bad, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a nonce that is not the commitment's: %v", err)
	}
	if r, err := m.Poll(context.Background(), ans.ID, ans.Poll, cn, 0); err != nil || r.State != StatePending {
		t.Fatalf("reveal: %v %+v", err, r)
	}
	if _, err := m.Poll(context.Background(), ans.ID, ans.Poll, "", 0); !errors.Is(err, ErrSlowDown) {
		t.Fatalf("too fast: %v", err)
	}
	p := m.Pending()
	nonce, _ := hex.DecodeString(ans.Nonce)
	cnb, _ := hex.DecodeString(cn)
	if len(p) != 1 || p[0].Code != Code(nonce, cnb, fp) || p[0].Name != "hm2mqtt.js on nas" || p[0].Fingerprint != "AB:CD" || p[0].Purpose["bogus"] != "" || p[0].Purpose["devices"] == "" {
		t.Fatalf("card %+v", p)
	}
	// the waiting poll wakes on the approval and gets the token once
	done := make(chan Result, 1)
	go func() {
		r, _ := m.Poll(context.Background(), ans.ID, ans.Poll, "", 10*time.Second)
		done <- r
	}()
	time.Sleep(50 * time.Millisecond)
	client, name, err := m.Approve(ans.ID, p[0].Code, "admin", false)
	if err != nil || name != "hm2mqtt-js-nas" || client.PairedBy != "admin" || client.Address != "192.168.1.50" || client.Fingerprint != "abcd" || client.Label != "hm2mqtt.js on nas" {
		t.Fatalf("approve: %v %s %+v", err, name, client)
	}
	select {
	case r := <-done:
		if r.State != StateApproved || r.Token == "" || r.Name != "hm2mqtt-js-nas" || strings.Join(r.Scopes, " ") != "meta:read rpc:operate" || r.Access["devices"] != "operate" {
			t.Fatalf("approved answer %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the long poll did not wake")
	}
	if _, err := m.Poll(context.Background(), ans.ID, ans.Poll, "", 0); !errors.Is(err, ErrUnknown) {
		t.Fatalf("the token twice: %v", err)
	}
	if len(f.names) != 1 || changes < 2 {
		t.Errorf("minted %v, changes %d", f.names, changes)
	}
}

// A wrong code rejects and mutes; Reject mutes; the limits; outside the local networks; off.
func TestRefusals(t *testing.T) {
	m, _, c := newMgr()
	cn, commit := clientHalf(1)
	ans, _ := m.Request(ask("app", "one", commit), "192.168.1.5", nil)
	if _, _, err := m.Approve(ans.ID, "000000", "admin", false); !errors.Is(err, ErrNotReady) {
		t.Fatalf("before the reveal: %v", err)
	}
	_, _ = m.Poll(context.Background(), ans.ID, ans.Poll, cn, 0)
	code := m.Pending()[0].Code
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	if _, _, err := m.Approve(ans.ID, wrong, "admin", false); !errors.Is(err, ErrWrongCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if r, _ := m.Poll(context.Background(), ans.ID, ans.Poll, "", 0); r.State != StateRejected {
		t.Fatalf("after the wrong code: %+v", r)
	}
	if _, err := m.Request(ask("app", "one", commit), "192.168.1.5", nil); !errors.Is(err, ErrMuted) {
		t.Fatalf("muted: %v", err)
	}
	c.t = c.t.Add(MuteFor + time.Second)
	if _, err := m.Request(ask("app", "one", commit), "192.168.1.5", nil); err != nil {
		t.Fatalf("after the mute: %v", err)
	}
	// one per address and program
	if _, err := m.Request(ask("app", "two", commit), "192.168.1.5", nil); !errors.Is(err, ErrLimit) {
		t.Fatalf("a second from the same place: %v", err)
	}
	// five in total
	for i := 0; i < 4; i++ {
		if _, err := m.Request(ask("app"+itoa(uint32(i)), "x", commit), "192.168.1.6", nil); err != nil {
			t.Fatalf("pending %d: %v", i, err)
		}
	}
	if _, err := m.Request(ask("sixth", "x", commit), "192.168.1.7", nil); !errors.Is(err, ErrLimit) {
		t.Fatalf("a sixth: %v", err)
	}
	// ten an hour per address: each one withdrawn before the next
	c.t = c.t.Add(Lifetime + 2*time.Minute)
	n := 0
	for i := 0; i < 12; i++ {
		a, err := m.Request(ask("burst", "x", commit), "192.168.1.9", nil)
		if err != nil {
			if !errors.Is(err, ErrLimit) {
				t.Fatal(err)
			}
			break
		}
		n++
		_ = m.Withdraw(a.ID, a.Poll)
	}
	if n != PerHour {
		t.Errorf("per hour: %d", n)
	}
	if _, err := m.Request(ask("app", "x", commit), "8.8.8.8", nil); !errors.Is(err, ErrNotLocal) {
		t.Fatalf("outside: %v", err)
	}
	m.Enabled = func() bool { return false }
	if _, err := m.Request(ask("app", "x", commit), "192.168.1.8", nil); !errors.Is(err, ErrOff) {
		t.Fatalf("off: %v", err)
	}
	m.Enabled = nil
	for _, bad := range []Ask{{App: "", Commit: commit, Access: map[string]string{"devices": "read"}}, {App: "a", Commit: "short", Access: map[string]string{"devices": "read"}}, {App: "a", Commit: commit, Access: map[string]string{"system": "administer"}}, {App: "a", Instance: "x\ny", Commit: commit, Access: map[string]string{"devices": "read"}}} {
		if _, err := m.Request(bad, "192.168.1.20", nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

func TestRejectExpireWithdrawLookAlike(t *testing.T) {
	m, _, c := newMgr()
	cn, commit := clientHalf(3)
	a1, _ := m.Request(ask("hmm", "pc", commit), "192.168.1.10", nil)
	a2, _ := m.Request(ask("hmm", "pc", commit), "192.168.1.11", nil)
	_, _ = m.Poll(context.Background(), a1.ID, a1.Poll, cn, 0)
	_, _ = m.Poll(context.Background(), a2.ID, a2.Poll, cn, 0)
	p := m.Pending()
	if len(p) != 2 || !p[0].LookAlike || !p[1].LookAlike {
		t.Fatalf("look-alikes %+v", p)
	}
	if err := m.Reject(a1.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := m.Withdraw(a2.ID, a2.Poll); err != nil {
		t.Fatal(err)
	}
	if len(m.Pending()) != 0 {
		t.Fatal("still listed")
	}
	a3, _ := m.Request(ask("x", "y", commit), "192.168.1.12", nil)
	c.t = c.t.Add(Lifetime + time.Second)
	if r, err := m.Poll(context.Background(), a3.ID, a3.Poll, "", 0); err != nil || r.State != StateExpired {
		t.Fatalf("expired: %v %+v", err, r)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"hm2mqtt.js", "nas"}:                      "hm2mqtt-js-nas",
		{"node-red-contrib-ccu", "Node-RED on Pi"}: "node-red-contrib-ccu-node-re",
		{"x", ""}:          "client",
		{"Homematic", "Ü"}: "homematic",
	} {
		if got := slug(in[0], in[1]); got != want {
			t.Errorf("%v: %q", in, got)
		}
	}
}

// occulited task 135: the token's range per kind of address.
func TestTokenRanges(t *testing.T) {
	for _, c := range []struct{ addr, want string }{
		{"192.168.1.50", "192.168.1.50/32"},
		{"::ffff:192.168.1.50", "192.168.1.50/32"},
		{"127.0.0.1", "127.0.0.1/32"},
		{"::1", "::1/128"},
		{"fe80::1234", "fe80::1234/128"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"fd00:1:2:3::9", "fd00:1:2:3::/64"},
		{"nope", ""},
		{"", ""},
	} {
		if got := strings.Join(TokenRanges(c.addr), ","); got != c.want {
			t.Errorf("%q: %q, want %q", c.addr, got, c.want)
		}
	}
}

// occulited task 135: what a request for backups asks of Approve, and what the others do not.
func TestBackupRequest(t *testing.T) {
	m, f, _ := newMgr()
	a := ask("occulite-backup", "", "")
	cn := "00112233445566778899aabbccddeeff"
	raw, _ := hex.DecodeString(cn)
	sum := sha256.Sum256(raw)
	a.Commit = hex.EncodeToString(sum[:])
	a.Access = map[string]string{"backup": "download"}
	if _, err := m.Request(a, "not-an-address", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no address to limit the token to: %v", err)
	}
	a2 := a
	a2.Access = map[string]string{"backup": "everything"}
	if _, err := m.Request(a2, "192.168.1.50", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another level: %v", err)
	}
	ans, err := m.Request(a, "192.168.1.50", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.NeedsConfirmation(ans.ID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("before the reveal: %v", err)
	}
	if _, err := m.Poll(context.Background(), ans.ID, ans.Poll, cn, 0); err != nil {
		t.Fatal(err)
	}
	p := m.Pending()
	if len(p) != 1 || !p[0].NeedsConfirmation || strings.Join(p[0].TokenIPs, ",") != "192.168.1.50/32" || strings.Join(p[0].Scopes, ",") != "backup" {
		t.Fatalf("card %+v", p)
	}
	if need, err := m.NeedsConfirmation(ans.ID); err != nil || !need {
		t.Fatalf("%v %v", need, err)
	}
	if _, _, err := m.Approve(ans.ID, p[0].Code, "admin", false); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("unconfirmed: %v", err)
	}
	if _, _, err := m.Approve(ans.ID, "000000x", "admin", false); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("unconfirmed, a wrong code: %v", err)
	}
	if len(m.Pending()) != 1 || len(f.names) != 0 {
		t.Fatal("an unconfirmed approval changed something")
	}
	if _, _, err := m.Approve(ans.ID, p[0].Code, "admin", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.ips, ",") != "192.168.1.50/32" {
		t.Fatalf("ips %v", f.ips)
	}
	r, err := m.Poll(context.Background(), ans.ID, ans.Poll, "", 0)
	if err != nil || r.State != StateApproved || strings.Join(r.IPs, ",") != "192.168.1.50/32" {
		t.Fatalf("answer %v %+v", err, r)
	}
	if _, err := m.NeedsConfirmation(ans.ID); !errors.Is(err, ErrUnknown) {
		t.Fatalf("gone: %v", err)
	}

	// an ordinary request: no confirmation asked, and no limit on its token
	a.App, a.Access = "other", map[string]string{"devices": "read"}
	ans, err = m.Request(a, "192.168.1.50", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Poll(context.Background(), ans.ID, ans.Poll, cn, 0); err != nil {
		t.Fatal(err)
	}
	p = m.Pending()
	if p[0].NeedsConfirmation || p[0].TokenIPs != nil {
		t.Fatalf("card %+v", p[0])
	}
	if need, _ := m.NeedsConfirmation(ans.ID); need {
		t.Fatal("an ordinary request needs a confirmation")
	}
	if _, _, err := m.Approve(ans.ID, p[0].Code, "admin", false); err != nil || f.ips != nil {
		t.Fatalf("%v ips %v", err, f.ips)
	}
}
