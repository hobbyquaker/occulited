package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/devstate"
	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/hobbyquaker/occulited/internal/paramsets"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// task 77: the lite-rpc routes with the session the middleware would give them - a token with a
// tier, an account session by cookie or header - against a fake interface process behind a
// real subscriber.

type liteRig struct {
	cache *paramsets.Cache
	srv   *httptest.Server
	svc   *literpc.Service
	sess  *auth.Session // what the middleware stand-in injects; nil = none
	mu    sync.Mutex
	calls []string
	cbs   map[string]string
	valid bool
	state *devstate.Store
	hist  *devstate.History
}

func newLiteRig(t *testing.T) *liteRig {
	t.Helper()
	rig := &liteRig{cbs: map[string]string{}, valid: true}
	d := &xmlrpc.BasicDispatcher{}
	d.AddSystemMethods()
	d.HandleFunc("init", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		q := xmlrpc.Q(args)
		rig.mu.Lock()
		if q.Idx(1).String() == "" {
			delete(rig.cbs, q.Idx(0).String())
		} else {
			rig.cbs[q.Idx(0).String()] = q.Idx(1).String()
		}
		rig.mu.Unlock()
		return xmlrpc.NewString(""), nil
	})
	d.HandleFunc("ping", func(*xmlrpc.Value) (*xmlrpc.Value, error) { return xmlrpc.NewBool(true), nil })
	d.HandleFunc("getValue", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		rig.mu.Lock()
		rig.calls = append(rig.calls, "getValue")
		rig.mu.Unlock()
		return xmlrpc.NewInt(42), nil
	})
	d.HandleFunc("setValue", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		rig.mu.Lock()
		rig.calls = append(rig.calls, "setValue")
		rig.mu.Unlock()
		return &xmlrpc.Value{}, nil
	})
	// task 108: a wall thermostat and a valve sharing a channel type name, described per model
	descOf := func(addr string) map[string]any {
		switch addr {
		case "WTH01:1":
			return map[string]any{"SET_POINT_TEMPERATURE": map[string]any{"TYPE": "FLOAT", "OPERATIONS": 7, "MIN": 4.5, "MAX": 30.5}, "HUMIDITY": map[string]any{"TYPE": "INTEGER", "OPERATIONS": 5, "MIN": 0, "MAX": 100}}
		case "TRV01:1":
			return map[string]any{"SET_POINT_TEMPERATURE": map[string]any{"TYPE": "FLOAT", "OPERATIONS": 7, "MIN": 4.5, "MAX": 30.5}, "LEVEL": map[string]any{"TYPE": "FLOAT", "OPERATIONS": 5, "MIN": 0, "MAX": 1}}
		}
		return nil
	}
	devOf := func(addr string) map[string]any {
		switch addr {
		case "WTH01":
			return map[string]any{"ADDRESS": addr, "TYPE": "HmIP-WTH-2", "FIRMWARE": "1.0.14", "VERSION": 1, "CHILDREN": []any{"WTH01:1"}, "PARAMSETS": []any{"MASTER"}}
		case "TRV01":
			return map[string]any{"ADDRESS": addr, "TYPE": "HmIP-eTRV-2", "FIRMWARE": "2.0.2", "VERSION": 1, "CHILDREN": []any{"TRV01:1"}, "PARAMSETS": []any{"MASTER"}}
		case "WTH01:1":
			return map[string]any{"ADDRESS": addr, "TYPE": "HEATING_CLIMATECONTROL_TRANSCEIVER", "PARENT": "WTH01", "PARENT_TYPE": "HmIP-WTH-2", "PARAMSETS": []any{"MASTER", "VALUES"}}
		case "TRV01:1":
			return map[string]any{"ADDRESS": addr, "TYPE": "HEATING_CLIMATECONTROL_TRANSCEIVER", "PARENT": "TRV01", "PARENT_TYPE": "HmIP-eTRV-2", "PARAMSETS": []any{"MASTER", "VALUES"}}
		}
		return nil
	}
	d.HandleFunc("getDeviceDescription", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		rig.mu.Lock()
		rig.calls = append(rig.calls, "getDeviceDescription")
		rig.mu.Unlock()
		dd := devOf(xmlrpc.Q(args).Idx(0).String())
		if dd == nil {
			return nil, &xmlrpc.MethodError{Code: -2, Message: "Unknown instance"}
		}
		return literpc.FromJSON(dd)
	})
	d.HandleFunc("getParamsetDescription", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		rig.mu.Lock()
		rig.calls = append(rig.calls, "getParamsetDescription")
		rig.mu.Unlock()
		pd := descOf(xmlrpc.Q(args).Idx(0).String())
		if pd == nil || xmlrpc.Q(args).Idx(1).String() != "VALUES" {
			return nil, &xmlrpc.MethodError{Code: -2, Message: "Unknown instance"}
		}
		return literpc.FromJSON(pd)
	})
	daemon := httptest.NewServer(&xmlrpc.Handler{Dispatcher: d})
	t.Cleanup(daemon.Close)
	list := filepath.Join(t.TempDir(), "InterfacesList.xml")
	_ = os.WriteFile(list, []byte(`<interfaces><ipc><name>HmIP-RF</name><url>xmlrpc://`+strings.TrimPrefix(daemon.URL, "http://")+`</url></ipc></interfaces>`), 0o644)
	rig.hist = &devstate.History{}                          // task 195
	rig.state = &devstate.Store{OnObserve: rig.hist.Record} // task 194: fed by the bus, read by GET /state
	sub := rpcsub.New(rpcsub.Config{Listen: "127.0.0.1:0", Interfaces: list, Watch: 50 * time.Millisecond, PingAfter: time.Hour, Enrich: rig.state.Enrich})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = sub.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for {
		up := false
		for _, s := range sub.Status() {
			up = up || s.State == "up"
		}
		if up {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not registered: %+v", sub.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	rig.cache = &paramsets.Cache{Forward: func(ctx context.Context, iface, method string, params ...*xmlrpc.Value) (*xmlrpc.Value, error) {
		i, ok := rig.svc.Lookup(iface)
		if !ok {
			return nil, literpc.ErrDown
		}
		return rig.svc.Forward(ctx, i, literpc.Call{Method: method, Params: params})
	}}
	rig.svc = literpc.New(literpc.Config{Sub: sub, Heartbeat: 40 * time.Millisecond, Descriptions: rig.cache})
	api := &SystemAPI{Root: fakeRoot(t), LiteRPC: rig.svc, State: rig.state, History: rig.hist, RPC: sub, Paramsets: rig.cache, Revalidate: func(*http.Request) *auth.Session {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		if !rig.valid {
			return nil
		}
		return rig.sess
	}}
	mux := http.NewServeMux()
	api.Register(mux)
	// the middleware stand-in: the rig's session into the context
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.mu.Lock()
		s := rig.sess
		rig.mu.Unlock()
		if s != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, s))
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(rig.srv.Close)
	return rig
}

func (r *liteRig) as(s *auth.Session) {
	r.mu.Lock()
	r.sess = s
	r.mu.Unlock()
}

func (r *liteRig) sendEvent(t *testing.T, addr, key, val string) {
	t.Helper()
	r.mu.Lock()
	cbs := map[string]string{}
	for u, id := range r.cbs {
		cbs[u] = id
	}
	r.mu.Unlock()
	for u, id := range cbs {
		c := &xmlrpc.Client{Addr: strings.TrimPrefix(u, "http://")}
		if _, err := c.Call("event", xmlrpc.Values{xmlrpc.NewString(id), xmlrpc.NewString(addr), xmlrpc.NewString(key), xmlrpc.NewString(val)}); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	tokenOperate = &auth.Session{ID: "token:ha", User: "token:ha", Scopes: auth.Scopes{auth.ScopeRPCOperate}}
	tokenRead    = &auth.Session{ID: "token:ro", User: "token:ro", Scopes: auth.Scopes{auth.ScopeRPCRead}}
	adminSess    = &auth.Session{ID: "sessionid0000000000000000000001", User: "admin", Role: auth.RoleAdmin, Scopes: auth.Scopes{auth.ScopeAll}}
)

func TestLiteRPCRequests(t *testing.T) {
	rig := newLiteRig(t)
	rig.as(tokenOperate)
	st, _, raw := do(t, rig.srv, "GET", "/api/rpc/v1/interfaces", "", nil)
	if st != 200 || !strings.Contains(raw, `"name":"HmIP-RF"`) || !strings.Contains(raw, `"running":true`) {
		t.Fatalf("interfaces: %d %s", st, raw)
	}
	// XML-RPC: a read for an operate token, the answer in UTF-8
	xml := `<?xml version="1.0"?><methodCall><methodName>getValue</methodName><params><param><value>ABC:1</value></param><param><value>STATE</value></param></params></methodCall>`
	st, _, raw = do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", xml, map[string]string{"Content-Type": "text/xml"})
	if st != 200 || !strings.Contains(raw, "<i4>42</i4>") || !strings.Contains(raw, `encoding="UTF-8"`) {
		t.Fatalf("getValue: %d %s", st, raw)
	}
	// init is a fault, alone and in a multicall
	initXML := `<methodCall><methodName>init</methodName><params><param><value>http://x</value></param><param><value>y</value></param></params></methodCall>`
	if st, _, raw := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", initXML, nil); st != 200 || !strings.Contains(raw, "<fault>") || !strings.Contains(raw, "init is not available remotely") {
		t.Fatalf("init: %d %s", st, raw)
	}
	mc := `<methodCall><methodName>system.multicall</methodName><params><param><value><array><data><value><struct><member><name>methodName</name><value>getValue</value></member><member><name>params</name><value><array><data><value>A:1</value><value>STATE</value></data></array></value></member></struct></value><value><struct><member><name>methodName</name><value>init</value></member><member><name>params</name><value><array><data><value>http://x</value><value>y</value></data></array></value></member></struct></value></data></array></value></param></params></methodCall>`
	if st, _, raw := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", mc, nil); st != 200 || !strings.Contains(raw, "init is not available remotely") {
		t.Fatalf("init in a multicall: %d %s", st, raw)
	}
	// the tier: a read token may not setValue, an operate token may
	setXML := `<methodCall><methodName>setValue</methodName><params><param><value>ABC:1</value></param><param><value>STATE</value></param><param><value><boolean>1</boolean></value></param></params></methodCall>`
	rig.as(tokenRead)
	if st, _, raw := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", setXML, nil); st != 200 || !strings.Contains(raw, "needs rpc:operate") {
		t.Fatalf("read token setValue: %d %s", st, raw)
	}
	rig.as(tokenOperate)
	if st, _, raw := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", setXML, nil); st != 200 || strings.Contains(raw, "<fault>") {
		t.Fatalf("operate token setValue: %d %s", st, raw)
	}
	if strings.Join(rig.calls, ",") != "getValue,setValue" {
		t.Fatalf("the daemon saw %v", rig.calls)
	}
	// an unknown interface, a body that is not XML-RPC, ?sid=
	if st, out, _ := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/CUxD", xml, nil); st != 404 || out["error"] != "unknown-interface" {
		t.Fatalf("CUxD: %d %v", st, out)
	}
	if st, _, _ := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", "<nope/>", nil); st != 400 {
		t.Fatalf("not a call: %d", st)
	}
	if st, _, _ := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF?sid=olt_00000000000000000000000000000000", xml, nil); st != 400 {
		t.Fatalf("?sid=: %d", st)
	}
	// JSON-RPC
	st, out, _ := do(t, rig.srv, "POST", "/api/rpc/v1/json/HmIP-RF", `{"jsonrpc":"2.0","method":"getValue","params":["ABC:1","STATE"],"id":1}`, nil)
	if st != 200 || out["result"] != float64(42) {
		t.Fatalf("json: %d %v", st, out)
	}
	if st, out, _ := do(t, rig.srv, "POST", "/api/rpc/v1/json/HmIP-RF", `{"jsonrpc":"2.0","method":"init","params":[],"id":1}`, nil); st != 200 || out["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("json init: %d %v", st, out)
	}
	// the browser rules: an admin session by cookie alone may not call; with the header it may
	rig.as(adminSess)
	if st, out, _ := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", xml, map[string]string{"Cookie": "occulite_session=" + adminSess.ID}); st != 403 || !strings.Contains(out["message"].(string), "Authorization header") {
		t.Fatalf("cookie call: %d %v", st, out)
	}
	if st, _, raw := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", xml, map[string]string{"Authorization": "Bearer " + adminSess.ID}); st != 200 || !strings.Contains(raw, "<i4>42</i4>") {
		t.Fatalf("header call: %d %s", st, raw)
	}
	// task 286: the interfaces' names are here, behind rpc:read (the open /version does not list
	// them): a read token gets them, a token without an rpc tier does not
	rig.as(tokenRead)
	if st, _, raw := do(t, rig.srv, "GET", "/api/rpc/v1/interfaces", "", nil); st != 200 || !strings.Contains(raw, `"name":"HmIP-RF"`) {
		t.Fatalf("interfaces for rpc:read: %d %s", st, raw)
	}
	// the scope is the middleware's table (this rig has no middleware)
	if got := RouteScopes()["GET /api/rpc/v1/interfaces"]; len(got) != 1 || got[0] != auth.ScopeRPCRead {
		t.Fatalf("the interfaces route's scope: %v", got)
	}
	// no session at all
	rig.as(nil)
	if st, _, _ := do(t, rig.srv, "GET", "/api/rpc/v1/interfaces", "", nil); st != 401 {
		t.Fatalf("no session: %d", st)
	}
	// no service: 501
	mux := http.NewServeMux()
	(&SystemAPI{Root: fakeRoot(t)}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if st, _, _ := do(t, srv, "GET", "/api/rpc/v1/interfaces", "", nil); st != 501 {
		t.Fatalf("no service: %d", st)
	}
}

// sseLines reads SSE messages (event + data) from an open response.
func sseNext(t *testing.T, r *bufio.Reader) (string, string, string) {
	t.Helper()
	var id, ev, data string
	var sofar []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended: %v (read so far: %q)", err, sofar)
		}
		sofar = append(sofar, line)
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "id: "):
			id = line[4:]
		case strings.HasPrefix(line, "event: "):
			ev = line[7:]
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		case line == "" && ev != "":
			return id, ev, data
		}
	}
}

func TestLiteRPCStream(t *testing.T) {
	rig := newLiteRig(t)
	rig.as(tokenRead)
	open := func(path string, hdr map[string]string) (*http.Response, *bufio.Reader) {
		t.Helper()
		req, _ := http.NewRequest("GET", rig.srv.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp, bufio.NewReader(resp.Body)
	}
	resp, r := open("/api/rpc/v1/events?key=STATE", nil)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || !strings.HasPrefix(ct, "text/event-stream") || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("open: %d %q", resp.StatusCode, ct)
	}
	id, ev, data := sseNext(t, r)
	if ev != "hello" || id == "" || !strings.Contains(data, `"boot_id"`) {
		t.Fatalf("hello: %s %s %s", id, ev, data)
	}
	rig.sendEvent(t, "ABC:1", "LEVEL", "1")
	rig.sendEvent(t, "ABC:1", "STATE", "1")
	id, ev, data = sseNext(t, r)
	if ev != "event" || !strings.Contains(data, `"key":"STATE"`) || !strings.Contains(data, `"lc":"`) || !strings.Contains(data, `"confirmed":true`) || !strings.HasPrefix(id, strings.Split(id, "-")[0]+"-") {
		t.Fatalf("event: %s %s %s", id, ev, data)
	}
	// the list shows it with the limits in force; the second and third stream of the token are
	// fine, the fourth is 429 (3 per token or session by default, occulited task 19)
	st, out, _ := do(t, rig.srv, "GET", "/api/rpc/v1/streams", "", nil)
	streams := out["streams"].([]any)
	if st != 200 || len(streams) != 1 || streams[0].(map[string]any)["transport"] != "sse" || streams[0].(map[string]any)["subject"].(map[string]any)["name"] != "ro" {
		t.Fatalf("streams: %d %v", st, out)
	}
	if l := out["limits"].(map[string]any); l["per_token"] != float64(3) || l["total"] != float64(16) {
		t.Fatalf("limits: %v", l)
	}
	resp2, _ := open("/api/rpc/v1/events", nil)
	defer resp2.Body.Close()
	resp3, _ := open("/api/rpc/v1/events", nil)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Fatalf("third stream: %d", resp3.StatusCode)
	}
	resp4, _ := open("/api/rpc/v1/events", nil)
	if resp4.StatusCode != 429 {
		t.Fatalf("fourth stream: %d", resp4.StatusCode)
	}
	resp4.Body.Close()
	// the page's x on the first: the response ends
	sid := streams[0].(map[string]any)["id"].(string)
	if st, _, _ := do(t, rig.srv, "DELETE", "/api/rpc/v1/streams/"+sid, "", nil); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	if _, err := r.ReadString('\x00'); err == nil {
		t.Fatal("the stream did not end")
	}
	if st, _, _ := do(t, rig.srv, "DELETE", "/api/rpc/v1/streams/"+sid, "", nil); st != 404 {
		t.Fatalf("delete again: %d", st)
	}
	// the second one closed by the client: the server notices at the next heartbeat and the
	// slot is free again
	resp2.Body.Close()
	time.Sleep(120 * time.Millisecond)
	// resume from the event's id after more events: the replay, no duplicate
	rig.sendEvent(t, "ABC:1", "STATE", "0")
	resp4, r4 := open("/api/rpc/v1/events", map[string]string{"Last-Event-ID": id})
	defer resp4.Body.Close()
	if _, ev, _ := sseNext(t, r4); ev != "hello" {
		t.Fatalf("resume hello: %s", ev)
	}
	if _, ev, data := sseNext(t, r4); ev != "event" || !strings.Contains(data, `"value":"0"`) {
		t.Fatalf("replayed: %s %s", ev, data)
	}
	// another boot: resync
	resp5, r5 := open("/api/rpc/v1/events", map[string]string{"Last-Event-ID": "cafe-1"})
	defer resp5.Body.Close()
	sseNext(t, r5)
	if _, ev, data := sseNext(t, r5); ev != "resync" || !strings.Contains(data, "boot") {
		t.Fatalf("resync: %s %s", ev, data)
	}
	resp4.Body.Close()
	resp5.Body.Close()
	// the browser rules on the stream: a cookie session needs the origin
	rig.as(adminSess)
	if resp, _ := open("/api/rpc/v1/events", map[string]string{"Cookie": "x=y"}); resp.StatusCode != 403 {
		t.Fatalf("cookie without origin: %d", resp.StatusCode)
	}
	if resp, _ := open("/api/rpc/v1/events", map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != 403 {
		t.Fatalf("cross-site: %d", resp.StatusCode)
	}
	if resp, _ := open("/api/rpc/v1/events", map[string]string{"Origin": "http://evil.example"}); resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	// occulited B-47: a page of this system over plain HTTP says neither Sec-Fetch-Site nor Origin
	// on a GET; the header credential, which another origin's page cannot send, stands in - but
	// never against what the browser does say
	if resp, _ := open("/api/rpc/v1/events", map[string]string{"X-Occulite-Request": "1", "Sec-Fetch-Site": "same-site"}); resp.StatusCode != 403 {
		t.Fatalf("the header from another port of this host: %d", resp.StatusCode)
	}
	if resp, _ := open("/api/rpc/v1/events", map[string]string{"X-Occulite-Request": "1", "Origin": "http://evil.example"}); resp.StatusCode != 403 {
		t.Fatalf("the header with a foreign origin: %d", resp.StatusCode)
	}
	if resp, _ := open("/api/rpc/v1/events?sid="+adminSess.ID, map[string]string{"X-Occulite-Request": "1"}); resp.StatusCode != 400 {
		t.Fatalf("a session in the query string: %d", resp.StatusCode)
	}
	time.Sleep(60 * time.Millisecond) // the closed streams release their slots
	respH, rH := open("/api/rpc/v1/events", map[string]string{"Cookie": "x=y", "X-Occulite-Request": "1"})
	if _, ev, _ := sseNext(t, rH); respH.StatusCode != 200 || ev != "hello" {
		t.Fatalf("plain HTTP, the cookie and the header credential: %d %s", respH.StatusCode, ev)
	}
	respH.Body.Close()
	// the session as a bearer, as the shell's fetch sends it: no word about the origin needed
	respB, rB := open("/api/rpc/v1/events", map[string]string{"Authorization": "Bearer " + adminSess.ID})
	if _, ev, _ := sseNext(t, rB); respB.StatusCode != 200 || ev != "hello" {
		t.Fatalf("the session in the Authorization header: %d %s", respB.StatusCode, ev)
	}
	respB.Body.Close()
	time.Sleep(60 * time.Millisecond)
	resp6, r6 := open("/api/rpc/v1/events", map[string]string{"Sec-Fetch-Site": "same-origin"})
	if _, ev, _ := sseNext(t, r6); resp6.StatusCode != 200 || ev != "hello" {
		t.Fatalf("same-origin: %d %s", resp6.StatusCode, ev)
	}
	resp6.Body.Close()
	resp7, r7 := open("/api/rpc/v1/events", map[string]string{"Origin": "http://" + strings.TrimPrefix(rig.srv.URL, "http://")})
	if _, ev, _ := sseNext(t, r7); resp7.StatusCode != 200 || ev != "hello" {
		t.Fatalf("own origin: %d %s", resp7.StatusCode, ev)
	}
	// a revoked credential ends the stream at the next heartbeat
	rig.mu.Lock()
	rig.valid = false
	rig.mu.Unlock()
	if _, err := r7.ReadString('\x00'); err == nil {
		t.Fatal("the revoked stream did not end")
	}
	resp7.Body.Close()
	// the switch off ends everything and the paths are 404
	rig.mu.Lock()
	rig.valid = true
	rig.mu.Unlock()
	rig.as(tokenRead)
	resp8, r8 := open("/api/rpc/v1/events", nil)
	sseNext(t, r8)
	// the shutdown ends every stream
	rig.svc.CloseAll("disabled")
	if _, err := r8.ReadString('\x00'); err == nil {
		t.Fatal("the stream survived CloseAll")
	}
	resp8.Body.Close()
	_ = json.Valid
}

// occulited B-47: the browser rule of the lite-rpc paths, case by case - what each kind of
// credential needs on a read (the stream, the state store) and on a request (POST). The header
// sets are the ones Chromium 153 was seen to send (the comment at originRefusal).
func TestBrowserRefusal(t *testing.T) {
	const host = "192.0.2.10"
	public := &auth.Session{User: "guest", Method: auth.MethodPublic, Scopes: auth.Scopes{auth.ScopeRPCOperate}}
	bearer := "Bearer " + adminSess.ID
	for _, c := range []struct {
		name   string
		sess   *auth.Session
		method string
		hdr    map[string]string
		pass   bool
	}{
		// a program's token: never asked for an origin
		{"token, nothing else", tokenRead, "GET", nil, true},
		{"token, a foreign origin", tokenRead, "GET", map[string]string{"Origin": "http://evil.example"}, true},
		{"token, a request", tokenOperate, "POST", nil, true},
		// the cookie alone on a read
		{"https or localhost: Sec-Fetch-Site same-origin", adminSess, "GET", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"a typed address: Sec-Fetch-Site none", adminSess, "GET", map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"https, another site", adminSess, "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"https, another port of this host", adminSess, "GET", map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"a WebSocket from this system's page", adminSess, "GET", map[string]string{"Origin": "http://" + host}, true},
		{"a WebSocket from another port of this host", adminSess, "GET", map[string]string{"Origin": "http://" + host + ":1880"}, false},
		{"a WebSocket or an EventSource from another site", adminSess, "GET", map[string]string{"Origin": "http://evil.example"}, false},
		{"plain http, this system's page without the header (an EventSource), or an <img> from another port", adminSess, "GET", nil, false},
		{"plain http, this system's page with the header credential", adminSess, "GET", map[string]string{"X-Occulite-Request": "1"}, true},
		// the header never outweighs what the browser says
		{"the header, but Sec-Fetch-Site cross-site", adminSess, "GET", map[string]string{"X-Occulite-Request": "1", "Sec-Fetch-Site": "cross-site"}, false},
		{"the header, but a foreign Origin", adminSess, "GET", map[string]string{"X-Occulite-Request": "1", "Origin": "http://evil.example"}, false},
		// the session in the Authorization header
		{"the session as a bearer, nothing else", adminSess, "GET", map[string]string{"Authorization": bearer}, true},
		{"another session as a bearer beside the cookie: the cookie's rule", adminSess, "GET", map[string]string{"Authorization": "Bearer someoneelse0000000000000000000"}, false},
		// a request: the cookie alone never calls a method, whatever the origin
		{"a request on the cookie, same-origin", adminSess, "POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"a request on the cookie with the header credential", adminSess, "POST", map[string]string{"X-Occulite-Request": "1", "Origin": "http://" + host}, false},
		{"a request with the session as a bearer", adminSess, "POST", map[string]string{"Authorization": bearer}, true},
		// the public principal has no session to put in a header: the origin rule, reads and requests
		{"public, plain http, a read without the header", public, "GET", nil, false},
		{"public, plain http, a read with the header credential", public, "GET", map[string]string{"X-Occulite-Request": "1"}, true},
		{"public, plain http, a request (a POST says its Origin)", public, "POST", map[string]string{"X-Occulite-Request": "1", "Origin": "http://" + host}, true},
		{"public, a request from another site", public, "POST", map[string]string{"Origin": "http://evil.example"}, false},
		{"public, https, another site", public, "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "http://"+host+"/api/rpc/v1/events", nil)
			for k, v := range c.hdr {
				r.Header.Set(k, v)
			}
			msg := browserRefusal(r, c.sess, c.method == "POST")
			if (msg == "") != c.pass {
				t.Fatalf("pass = %v, want %v (%q)", msg == "", c.pass, msg)
			}
		})
	}
	// behind lighttpd the host the browser addressed is the forwarded one
	r := httptest.NewRequest("GET", "http://127.0.0.1:2121/api/rpc/v1/events/ws", nil)
	r.Header.Set("X-Forwarded-Host", host)
	r.Header.Set("Origin", "http://"+host)
	if msg := browserRefusal(r, adminSess, false); msg != "" {
		t.Fatalf("the forwarded host: %q", msg)
	}
}

// task 194: the bulk read - what the bus brought, filtered and paged, with the stream's resume
// point; a restored entry reads confirmed false until reported.
func TestLiteRPCState(t *testing.T) {
	rig := newLiteRig(t)
	rig.as(tokenRead)
	rig.state.RestoreEntries(nil)
	rig.sendEvent(t, "ABC:1", "STATE", "1")
	rig.sendEvent(t, "ABC:2", "LEVEL", "0.5")
	rig.sendEvent(t, "ABC:0", "RSSI_DEVICE", "-60") // not in the chosen set
	deadline := time.Now().Add(5 * time.Second)
	for rig.state.Count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st, out, raw := do(t, rig.srv, "GET", "/api/rpc/v1/state", "", nil)
	if st != 200 || out["total"] != 2.0 || out["unconfirmed"] != 0.0 || !strings.HasPrefix(out["event_id"].(string), "") || out["datapoints"] == nil {
		t.Fatalf("state: %d %s", st, raw)
	}
	e := out["entries"].([]any)[0].(map[string]any)
	if e["interface"] != "HmIP-RF" || e["address"] != "ABC:1" || e["datapoint"] != "STATE" || e["value"] != "1" || e["confirmed"] != true || e["source"] != "event" || e["ts"] == nil || e["lc"] == nil {
		t.Fatalf("entry %v", e)
	}
	// filtered and paged
	st, out, raw = do(t, rig.srv, "GET", "/api/rpc/v1/state?datapoint=LEVEL", "", nil)
	if st != 200 || out["total"] != 1.0 {
		t.Fatalf("datapoint filter: %d %s", st, raw)
	}
	st, out, raw = do(t, rig.srv, "GET", "/api/rpc/v1/state?limit=1", "", nil)
	if st != 200 || len(out["entries"].([]any)) != 1 || out["next"] != "HmIP-RF/ABC:1/STATE" {
		t.Fatalf("page 1: %d %s", st, raw)
	}
	st, out, raw = do(t, rig.srv, "GET", "/api/rpc/v1/state?limit=1&after=HmIP-RF/ABC:1/STATE", "", nil)
	if st != 200 || len(out["entries"].([]any)) != 1 || out["next"] != nil || out["datapoints"] != nil {
		t.Fatalf("page 2: %d %s", st, raw)
	}
	if st, _, _ := do(t, rig.srv, "GET", "/api/rpc/v1/state?limit=0", "", nil); st != 400 {
		t.Fatalf("limit 0: %d", st)
	}
	rig.as(nil)
	if st, _, _ := do(t, rig.srv, "GET", "/api/rpc/v1/state", "", nil); st != 401 {
		t.Fatalf("no session: %d", st)
	}
}

// task 195: one series as compact rows; not recorded is 404, recorded and empty is [].
func TestLiteRPCHistory(t *testing.T) {
	rig := newLiteRig(t)
	rig.as(tokenRead)
	rig.sendEvent(t, "ABC:1", "STATE", "1")
	rig.sendEvent(t, "ABC:1", "STATE", "0")
	deadline := time.Now().Add(5 * time.Second)
	for rig.hist.Count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	st, _, raw := do(t, rig.srv, "GET", "/api/rpc/v1/history?interface=HmIP-RF&address=ABC:1&datapoint=STATE", "", nil)
	if st != 200 || !strings.HasPrefix(raw, "[[") || strings.Count(raw, "],[") != 1 || !strings.Contains(raw, `,"0"]]`) {
		t.Fatalf("history: %d %s", st, raw)
	}
	if st, _, raw := do(t, rig.srv, "GET", "/api/rpc/v1/history?interface=HmIP-RF&address=ABC:1&datapoint=STATE&max_points=1", "", nil); st != 200 || strings.Count(raw, "],[") != 0 || !strings.Contains(raw, `"0"`) {
		t.Fatalf("max_points: %d %s", st, raw)
	}
	if st, _, raw := do(t, rig.srv, "GET", "/api/rpc/v1/history?interface=HmIP-RF&address=ABC:1&key=STATE&from=2000-01-01T00:00:00Z&to=2000-01-02T00:00:00Z", "", nil); st != 200 || strings.TrimSpace(raw) != "[]" {
		t.Fatalf("from/to: %d %s", st, raw)
	}
	if st, _, raw := do(t, rig.srv, "GET", "/api/rpc/v1/history?interface=HmIP-RF&address=ABC:9&datapoint=HUMIDITY", "", nil); st != 200 || strings.TrimSpace(raw) != "[]" {
		t.Fatalf("listed, empty: %d %s", st, raw)
	}
	if st, _, _ := do(t, rig.srv, "GET", "/api/rpc/v1/history?interface=HmIP-RF&address=ABC:1&datapoint=RSSI_DEVICE", "", nil); st != 404 {
		t.Fatalf("not recorded: %d", st)
	}
	for _, q := range []string{"interface=HmIP-RF&address=ABC:1", "interface=HmIP-RF&address=ABC:1&datapoint=STATE&from=yesterday", "interface=HmIP-RF&address=ABC:1&datapoint=STATE&max_points=0"} {
		if st, _, _ := do(t, rig.srv, "GET", "/api/rpc/v1/history?"+q, "", nil); st != 400 {
			t.Fatalf("%s: %d", q, st)
		}
	}
}

// occulited B-14: the stop ends every lite-rpc stream at once - the SSE response ends, and the
// WebSocket, whose connection the server no longer tracks after the upgrade, gets a close frame
// 1001 "going away" (a client reconnects at once) instead of lingering until the process exits.
func TestLiteRPCStreamsEndAtStop(t *testing.T) {
	rig := newLiteRig(t)
	rig.as(tokenRead)
	// a server like cmd/occulited's: request contexts derive from a base the stop cancels
	base, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	srv := httptest.NewUnstartedServer(rig.srv.Config.Handler)
	srv.Config.BaseContext = func(net.Listener) context.Context { return base }
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/rpc/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sse := bufio.NewReader(resp.Body)
	if _, ev, _ := sseNext(t, sse); ev != "hello" {
		t.Fatalf("sse hello: %s", ev)
	}

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /api/rpc/v1/events/ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	ws := bufio.NewReader(conn)
	up, err := http.ReadResponse(ws, nil)
	if err != nil || up.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", up, err)
	}
	// one server frame: FIN+opcode, a 7-bit or 16-bit length, never masked
	frame := func() (byte, []byte) {
		t.Helper()
		var h [2]byte
		if _, err := io.ReadFull(ws, h[:]); err != nil {
			t.Fatalf("websocket frame: %v", err)
		}
		n := int(h[1] & 0x7f)
		if n == 126 {
			var b [2]byte
			_, _ = io.ReadFull(ws, b[:])
			n = int(b[0])<<8 | int(b[1])
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(ws, p); err != nil {
			t.Fatalf("websocket payload: %v", err)
		}
		return h[0] & 0x0f, p
	}
	if op, p := frame(); op != 0x1 || !strings.Contains(string(p), `"hello"`) {
		t.Fatalf("websocket hello: %x %s", op, p)
	}

	start := time.Now()
	stop(literpc.ErrStopping)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		op, p := frame()
		if op != 0x8 {
			continue // a ping or an event that was under way
		}
		if len(p) < 2 || int(p[0])<<8|int(p[1]) != literpc.CloseGoingAway {
			t.Fatalf("websocket close: %x", p)
		}
		break
	}
	sseDone := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, sse); sseDone <- err }()
	select {
	case <-sseDone:
	case <-time.After(time.Second):
		t.Fatal("the SSE stream is still open after the stop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !rig.svc.Wait(ctx) {
		t.Fatalf("streams left after the stop: %+v", rig.svc.Streams())
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the stop took %v", d)
	}
}

// task 108: the paramset description cache behind lite-rpc and its own routes - two models
// sharing a channel type name get their own description, every later call is served from the
// cache, and the whole cache comes in nrcc's schema.
func TestLiteParamsets(t *testing.T) {
	rig := newLiteRig(t)
	rig.as(tokenRead)
	calls := func(m string) int {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		n := 0
		for _, c := range rig.calls {
			if c == m {
				n++
			}
		}
		return n
	}
	st, out, raw := do(t, rig.srv, "POST", "/api/rpc/v1/paramsets/HmIP-RF", `{"addresses":["WTH01:1","TRV01:1","NOPE:1"]}`, nil)
	if st != 200 {
		t.Fatalf("paramsets: %d %s", st, raw)
	}
	keys := out["keys"].(map[string]any)
	wk, tk := keys["WTH01:1"], keys["TRV01:1"]
	if wk != "HmIP-RF/HmIP-WTH-2/1.0.14/1/HEATING_CLIMATECONTROL_TRANSCEIVER/VALUES" || tk != "HmIP-RF/HmIP-eTRV-2/2.0.2/1/HEATING_CLIMATECONTROL_TRANSCEIVER/VALUES" {
		t.Fatalf("keys: %v", keys)
	}
	ps := out["paramsets"].(map[string]any)
	if _, ok := ps[wk.(string)].(map[string]any)["HUMIDITY"]; !ok {
		t.Error("the wall thermostat's HUMIDITY")
	}
	if _, ok := ps[tk.(string)].(map[string]any)["LEVEL"]; !ok {
		t.Error("the valve's LEVEL")
	}
	if errs := out["errors"].(map[string]any); errs["NOPE:1"] == nil {
		t.Errorf("the unknown address is not in errors: %v", errs)
	}
	if n := calls("getParamsetDescription"); n != 2 {
		t.Fatalf("the process was asked %d times, want 2", n)
	}
	// a lite-rpc call for the same description: from the cache, no call to the process
	st, out, _ = do(t, rig.srv, "POST", "/api/rpc/v1/json/HmIP-RF", `{"jsonrpc":"2.0","method":"getParamsetDescription","params":["TRV01:1","VALUES"],"id":1}`, nil)
	if st != 200 || out["result"].(map[string]any)["LEVEL"] == nil || calls("getParamsetDescription") != 2 {
		t.Fatalf("json getParamsetDescription: %d %v, calls %d", st, out, calls("getParamsetDescription"))
	}
	// and over XML-RPC, with the FLOAT's bounds as doubles
	xml := `<?xml version="1.0"?><methodCall><methodName>getParamsetDescription</methodName><params><param><value>TRV01:1</value></param><param><value>VALUES</value></param></params></methodCall>`
	if st, _, raw := do(t, rig.srv, "POST", "/api/rpc/v1/xmlrpc/HmIP-RF", xml, map[string]string{"Content-Type": "text/xml"}); st != 200 || !strings.Contains(raw, "<double>0</double>") || calls("getParamsetDescription") != 2 {
		t.Fatalf("xmlrpc getParamsetDescription: %d %s", st, raw)
	}
	// the whole cache, nrcc's schema
	if st, out, _ := do(t, rig.srv, "GET", "/api/rpc/v1/paramsets", "", nil); st != 200 || len(out) != 2 || out[tk.(string)] == nil {
		t.Fatalf("GET paramsets: %d %v", st, out)
	}
	// bad requests
	if st, _, _ := do(t, rig.srv, "POST", "/api/rpc/v1/paramsets/HmIP-RF", `{"addresses":[]}`, nil); st != 400 {
		t.Errorf("empty: %d", st)
	}
	if st, _, _ := do(t, rig.srv, "POST", "/api/rpc/v1/paramsets/CUxD", `{"addresses":["A:1"]}`, nil); st != 404 {
		t.Errorf("unknown interface: %d", st)
	}
	// the scopes: rpc:read on both routes
	scopes := RouteScopes()
	for _, p := range []string{"GET /api/rpc/v1/paramsets", "POST /api/rpc/v1/paramsets/{interface}"} {
		if got := scopes[p]; len(got) != 1 || got[0] != auth.ScopeRPCRead {
			t.Errorf("%s: %v", p, got)
		}
	}
}
