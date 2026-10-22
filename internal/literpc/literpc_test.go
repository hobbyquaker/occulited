package literpc

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// A fake interface process: getValue answers a fixed value with an umlaut, setValue records
// what it got, listDevices two devices, a fault for an unknown address; init records the
// callback so a test can send events through the subscriber's bus.
type fakeDaemon struct {
	srv  *httptest.Server
	mu   sync.Mutex
	set  []string
	cbs  map[string]string // callback url -> id
	seen []string          // every method called
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	f := &fakeDaemon{cbs: map[string]string{}}
	d := &xmlrpc.BasicDispatcher{}
	d.AddSystemMethods()
	rec := func(name string) {
		f.mu.Lock()
		f.seen = append(f.seen, name)
		f.mu.Unlock()
	}
	d.HandleFunc("init", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		q := xmlrpc.Q(args)
		u, id := q.Idx(0).String(), q.Idx(1).String()
		f.mu.Lock()
		if id == "" {
			delete(f.cbs, u)
		} else {
			f.cbs[u] = id
		}
		f.mu.Unlock()
		return xmlrpc.NewString(""), nil
	})
	d.HandleFunc("ping", func(*xmlrpc.Value) (*xmlrpc.Value, error) { return xmlrpc.NewBool(true), nil })
	d.HandleFunc("getValue", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		rec("getValue")
		q := xmlrpc.Q(args)
		if q.Idx(0).String() == "NOPE:1" {
			return nil, &xmlrpc.MethodError{Code: -2, Message: "Unknown instance"}
		}
		if q.Idx(1).String() == "LEVEL" {
			return xmlrpc.NewFloat64(0.5), nil
		}
		return xmlrpc.NewString("Küche"), nil
	})
	d.HandleFunc("setValue", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		rec("setValue")
		q := xmlrpc.Q(args)
		f.mu.Lock()
		f.set = append(f.set, q.Idx(0).String()+" "+q.Idx(1).String()+"="+fmt.Sprint(q.Idx(2).Any()))
		f.mu.Unlock()
		return &xmlrpc.Value{}, nil
	})
	d.HandleFunc("listDevices", func(*xmlrpc.Value) (*xmlrpc.Value, error) {
		rec("listDevices")
		a, _ := xmlrpc.NewMap(map[string]interface{}{"ADDRESS": "ABC0000001", "TYPE": "HmIP-WRC2", "CHILDREN": []interface{}{"ABC0000001:0"}})
		b, _ := xmlrpc.NewMap(map[string]interface{}{"ADDRESS": "ABC0000002", "TYPE": "HmIP-PDT", "CHILDREN": []interface{}{}})
		return &xmlrpc.Value{Array: &xmlrpc.Array{Data: []*xmlrpc.Value{a, b}}}, nil
	})
	f.srv = httptest.NewServer(&xmlrpc.Handler{Dispatcher: d})
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDaemon) url() string { return "xmlrpc://" + strings.TrimPrefix(f.srv.URL, "http://") }

// send posts events to every registered callback, as a multicall when asked.
func (f *fakeDaemon) send(t *testing.T, events [][3]string, multicall bool) {
	t.Helper()
	f.mu.Lock()
	cbs := map[string]string{}
	for u, id := range f.cbs {
		cbs[u] = id
	}
	f.mu.Unlock()
	if len(cbs) == 0 {
		t.Fatal("fake: nobody registered")
	}
	for u, id := range cbs {
		c := &xmlrpc.Client{Addr: strings.TrimPrefix(u, "http://")}
		if !multicall {
			for _, e := range events {
				if _, err := c.Call("event", xmlrpc.Values{xmlrpc.NewString(id), xmlrpc.NewString(e[0]), xmlrpc.NewString(e[1]), xmlrpc.NewString(e[2])}); err != nil {
					t.Fatalf("fake event: %v", err)
				}
			}
			continue
		}
		var calls []*xmlrpc.Value
		for _, e := range events {
			m, _ := xmlrpc.NewMap(map[string]interface{}{"methodName": "event", "params": []interface{}{id, e[0], e[1], e[2]}})
			calls = append(calls, m)
		}
		if _, err := c.Call("system.multicall", xmlrpc.Values{{Array: &xmlrpc.Array{Data: calls}}}); err != nil {
			t.Fatalf("fake multicall: %v", err)
		}
	}
}

// traceRec records trace lines (task 79).
type traceRec struct {
	mu    sync.Mutex
	on    bool
	lines []string
}

func (r *traceRec) On() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.on }
func (r *traceRec) Line(l string) {
	r.mu.Lock()
	r.lines = append(r.lines, l)
	r.mu.Unlock()
}
func (r *traceRec) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// rig is a subscriber registered at the fake and the service on it.
func rig(t *testing.T, cfg Config) (*fakeDaemon, *Service, *rpcsub.Subscriber) {
	t.Helper()
	fd := newFakeDaemon(t)
	list := filepath.Join(t.TempDir(), "InterfacesList.xml")
	_ = os.WriteFile(list, []byte(`<interfaces><ipc><name>HmIP-RF</name><url>`+fd.url()+`</url></ipc><ipc><name>CUxD</name><url>xmlrpc_bin://127.0.0.1:8701</url></ipc></interfaces>`), 0o644)
	sub := rpcsub.New(rpcsub.Config{Listen: "127.0.0.1:0", Interfaces: list, Watch: 50 * time.Millisecond, PingAfter: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = sub.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for {
		up := false
		for _, s := range sub.Status() {
			if s.Name == "HmIP-RF" && s.State == "up" {
				up = true
			}
		}
		if up {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the subscriber did not register: %+v", sub.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cfg.Sub = sub
	if cfg.Heartbeat == 0 {
		cfg.Heartbeat = 40 * time.Millisecond
	}
	return fd, New(cfg), sub
}

func TestInterfacesAndForward(t *testing.T) {
	tr := &traceRec{on: true}
	fd, svc, _ := rig(t, Config{Trace: tr})
	ifs := svc.Interfaces()
	if len(ifs) != 1 || ifs[0].Name != "HmIP-RF" || !ifs[0].Running || ifs[0].URLPath != "/api/rpc/v1/xmlrpc/HmIP-RF" || ifs[0].Protocol != "xmlrpc" {
		t.Fatalf("interfaces: %+v", ifs)
	}
	// task 286: without a subscriber the list is empty, never null
	if n := New(Config{}).Interfaces(); n == nil || len(n) != 0 {
		t.Fatalf("interfaces without a subscriber: %#v", n)
	}
	i, _ := svc.Lookup("HmIP-RF")
	// a client's call in ISO-8859-1 with an umlaut, the answer back in UTF-8
	c, err := DecodeCall([]byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><methodCall><methodName>setValue</methodName><params><param><value>ABC:1</value></param><param><value><string>NAME</string></value></param><param><value>K\xfcche</value></param></params></methodCall>"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != "setValue" || len(c.Params) != 3 || c.Params[2].FlatString != "Küche" {
		t.Fatalf("decoded %+v", c)
	}
	if _, err := svc.Forward(context.Background(), i, c); err != nil {
		t.Fatal(err)
	}
	if fd.set[0] != "ABC:1 NAME=Küche" {
		t.Fatalf("the daemon got %q", fd.set[0])
	}
	v, err := svc.Forward(WithSource(context.Background(), "10.0.0.5 (token ha)", "json"), i, Call{Method: "getValue", Params: []*xmlrpc.Value{xmlrpc.NewString("ABC:1"), xmlrpc.NewString("NAME")}})
	if err != nil || v.FlatString != "Küche" {
		t.Fatalf("getValue: %v %v", v, err)
	}
	// the trace (task 79): the call with its source and encoding, and the answer
	lines := tr.get()
	if len(lines) != 4 || lines[0] != `xmlrpc occulited → HmIP-RF setValue ["ABC:1","NAME","Küche"]` || lines[1] != `← HmIP-RF occulited setValue ""` ||
		lines[2] != `json 10.0.0.5 (token ha) → HmIP-RF getValue ["ABC:1","NAME"]` || lines[3] != `← HmIP-RF 10.0.0.5 (token ha) getValue "Küche"` {
		t.Fatalf("trace %q", lines)
	}
	out := string(EncodeResponse(v, nil))
	if !strings.Contains(out, `encoding="UTF-8"`) || !strings.Contains(out, "Küche") {
		t.Fatalf("response %q", out)
	}
	// a fault keeps its code; the encoding shows it as a fault
	_, err = svc.Forward(context.Background(), i, Call{Method: "getValue", Params: []*xmlrpc.Value{xmlrpc.NewString("NOPE:1"), xmlrpc.NewString("NAME")}})
	var f *Fault
	if err == nil || !asFault(err, &f) || f.Code != -2 {
		t.Fatalf("fault: %v", err)
	}
	if lines := tr.get(); lines[len(lines)-1] != `← HmIP-RF occulited getValue fault -2 "Unknown instance"` {
		t.Fatalf("fault trace %q", lines[len(lines)-1])
	}
	// the switch off: nothing more
	tr.mu.Lock()
	tr.on = false
	tr.mu.Unlock()
	n := len(tr.get())
	_, _ = svc.Forward(context.Background(), i, Call{Method: "getValue", Params: []*xmlrpc.Value{xmlrpc.NewString("ABC:1"), xmlrpc.NewString("NAME")}})
	if len(tr.get()) != n {
		t.Fatal("traced while off")
	}
	if out := string(EncodeResponse(nil, err)); !strings.Contains(out, "<fault>") || !strings.Contains(out, "<i4>-2</i4>") {
		t.Fatalf("fault response %q", out)
	}
	// a daemon that is down
	fd.srv.Close()
	if _, err := svc.Forward(context.Background(), i, Call{Method: "getValue"}); err == nil || !isDown(err) {
		t.Fatalf("down: %v", err)
	}
}

func asFault(err error, f **Fault) bool {
	x, ok := err.(*Fault)
	if ok {
		*f = x
	}
	return ok
}

func isDown(err error) bool { return strings.Contains(err.Error(), "does not answer") }

func TestCallsInitAndTiers(t *testing.T) {
	inner := func(m string, ps ...interface{}) *xmlrpc.Value {
		v, _ := xmlrpc.NewMap(map[string]interface{}{"methodName": m, "params": ps})
		return v
	}
	mc := Call{Method: "system.multicall", Params: []*xmlrpc.Value{{Array: &xmlrpc.Array{Data: []*xmlrpc.Value{inner("getValue", "A:1", "STATE"), inner("init", "http://x", "y")}}}}}
	if cs := Calls(mc); len(cs) != 2 || cs[0].Method != "getValue" || cs[1].Method != "init" || len(cs[0].Params) != 2 {
		t.Fatalf("calls %+v", cs)
	}
	if !IsInit(mc) || IsInit(Call{Method: "getValue"}) || !IsInit(Call{Method: "init"}) {
		t.Fatal("IsInit")
	}
	for m, want := range map[string]auth.Scope{"getValue": auth.ScopeRPCRead, "setValue": auth.ScopeRPCOperate, "setInstallMode": auth.ScopeRPCConfigure, "deleteDevice": auth.ScopeRPCAdmin, "somethingNew": auth.ScopeRPCAdmin, "logLevel": auth.ScopeRPCRead} {
		if got := Tier(Call{Method: m}); got != want {
			t.Errorf("Tier(%s) = %s, want %s", m, got, want)
		}
	}
	if Tier(Call{Method: "logLevel", Params: []*xmlrpc.Value{xmlrpc.NewInt(3)}}) != auth.ScopeRPCConfigure {
		t.Error("logLevel with a level is configure")
	}
	if Tier(Call{Method: "putParamset", Params: []*xmlrpc.Value{xmlrpc.NewString("A:1"), xmlrpc.NewString("VALUES")}}) != auth.ScopeRPCOperate {
		t.Error("putParamset VALUES is operate")
	}
	if Tier(Call{Method: "putParamset", Params: []*xmlrpc.Value{xmlrpc.NewString("A:1"), xmlrpc.NewString("MASTER")}}) != auth.ScopeRPCConfigure {
		t.Error("putParamset MASTER is configure")
	}
}

func TestJSONRPC(t *testing.T) {
	fd, svc, _ := rig(t, Config{})
	i, _ := svc.Lookup("HmIP-RF")
	check := func(c Call) error {
		if IsInit(c) {
			return Refuse(InitFault)
		}
		return nil
	}
	body, st := svc.ServeJSON(context.Background(), i, []byte(`{"jsonrpc":"2.0","method":"getValue","params":["ABC:1","LEVEL"],"id":7}`), check)
	if st != 200 || string(body) != `{"jsonrpc":"2.0","result":0.5,"id":7}` {
		t.Fatalf("single: %d %s", st, body)
	}
	// a batch: a value, a fault with its code, init refused as -32601, an invalid one
	body, st = svc.ServeJSON(context.Background(), i, []byte(`[{"jsonrpc":"2.0","method":"getValue","params":["ABC:1","NAME"],"id":"a"},{"jsonrpc":"2.0","method":"getValue","params":["NOPE:1","NAME"],"id":2},{"jsonrpc":"2.0","method":"init","params":["http://x","y"],"id":3},{"method":"getValue","id":4},{"jsonrpc":"2.0","method":"setValue","params":["ABC:1","STATE",true]}]`), check)
	var out []map[string]any
	if err := json.Unmarshal(body, &out); err != nil || st != 200 || len(out) != 4 {
		t.Fatalf("batch: %d %s %v", st, body, err)
	}
	if out[0]["result"] != "Küche" || out[0]["id"] != "a" {
		t.Fatalf("first %v", out[0])
	}
	if e := out[1]["error"].(map[string]any); e["code"] != float64(-2) || e["message"] != "Unknown instance" {
		t.Fatalf("fault %v", out[1])
	}
	if e := out[2]["error"].(map[string]any); e["code"] != float64(-32601) || e["message"] != InitFault {
		t.Fatalf("init %v", out[2])
	}
	if e := out[3]["error"].(map[string]any); e["code"] != float64(-32600) {
		t.Fatalf("invalid %v", out[3])
	}
	// the notification was forwarded, without an answer
	if len(fd.set) != 1 || fd.set[0] != "ABC:1 STATE=true" {
		t.Fatalf("notification: %v", fd.set)
	}
	if body, st := svc.ServeJSON(context.Background(), i, []byte(`{"jsonrpc":"2.0",`), check); st != 400 || !strings.Contains(string(body), "-32700") {
		t.Fatalf("parse error: %d %s", st, body)
	}
	if body, st := svc.ServeJSON(context.Background(), i, []byte(`{"jsonrpc":"2.0","method":"getValue","params":{"a":1},"id":1}`), check); st != 200 || !strings.Contains(string(body), "-32602") {
		t.Fatalf("object params: %d %s", st, body)
	}
	// the types both ways
	v, _ := FromJSON(map[string]any{"n": json.Number("3"), "f": json.Number("2.5"), "b": true, "s": "x", "l": []any{json.Number("1")}, "b64": map[string]any{"base64": "AQ=="}})
	back := ToJSON(v).(map[string]any)
	if back["n"] != int64(3) || back["f"] != 2.5 || back["b"] != true || back["s"] != "x" || back["l"].([]any)[0] != int64(1) || back["b64"].(map[string]any)["base64"] != "AQ==" {
		t.Fatalf("round trip %v", back)
	}
	if ToJSON(&xmlrpc.Value{DateTime: "20260922T14:15:16"}) != "2026-09-22T14:15:16Z" {
		t.Fatalf("dateTime %v", ToJSON(&xmlrpc.Value{DateTime: "20260922T14:15:16"}))
	}
}

// recSink records what the stream sends.
type recSink struct {
	mu     sync.Mutex
	msgs   []string // "type id data"
	pings  int
	closed int
	reason string
	slow   time.Duration
}

func (r *recSink) Send(id, typ string, data any) error {
	if r.slow > 0 {
		time.Sleep(r.slow)
	}
	b, _ := json.Marshal(data)
	r.mu.Lock()
	r.msgs = append(r.msgs, typ+" "+id+" "+string(b))
	r.mu.Unlock()
	return nil
}
func (r *recSink) Ping() error { r.mu.Lock(); r.pings++; r.mu.Unlock(); return nil }
func (r *recSink) Close(code int, reason string) {
	r.mu.Lock()
	r.closed, r.reason = code, reason
	r.mu.Unlock()
}
func (r *recSink) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		out = append(out, strings.SplitN(m, " ", 2)[0])
	}
	return out
}
func (r *recSink) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		c := len(r.msgs)
		r.mu.Unlock()
		if c >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d messages, have %v", n, r.types())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStreamHelloLiveFilterResume(t *testing.T) {
	tr := &traceRec{on: true}
	fd, svc, sub := rig(t, Config{Trace: tr})
	st, err := svc.Open(Subject{Kind: "token", Name: "ha"}, "sse", "10.0.0.5", ParseFilter(map[string][]string{"address": {"ABC0000001"}, "type": {"event", "interface"}}))
	if err != nil {
		t.Fatal(err)
	}
	sink := &recSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- svc.Run(ctx, st, RunOptions{}, sink) }()
	sink.wait(t, 1)
	if ty := sink.types(); ty[0] != "hello" || !strings.Contains(sink.msgs[0], `"boot_id":"`+sub.BootID()+`"`) || !strings.Contains(sink.msgs[0], `"buffer":{"events":5000,"seconds":300}`) {
		t.Fatalf("hello: %v", sink.msgs)
	}
	// live: the device's channel passes, another device not, a multicall carries the batch mark
	fd.send(t, [][3]string{{"ABC0000001:1", "PRESS_SHORT", "1"}, {"ABC0000002:0", "LOW_BAT", "0"}}, false)
	fd.send(t, [][3]string{{"ABC0000001:0", "UNREACH", "1"}, {"ABC0000001:0", "RSSI_DEVICE", "-60"}}, true)
	sink.wait(t, 4)
	if ty := sink.types(); strings.Join(ty, ",") != "hello,event,event,event" {
		t.Fatalf("types %v", ty)
	}
	if !strings.Contains(sink.msgs[1], `"key":"PRESS_SHORT"`) || strings.Contains(sink.msgs[1], `"batch"`) {
		t.Fatalf("single event %s", sink.msgs[1])
	}
	if !strings.Contains(sink.msgs[2], `"batch":`) || !strings.Contains(sink.msgs[3], `"batch":`) {
		t.Fatalf("batch mark %s / %s", sink.msgs[2], sink.msgs[3])
	}
	// the heartbeat pings, and the page sees the stream
	time.Sleep(100 * time.Millisecond)
	sink.mu.Lock()
	pings := sink.pings
	sink.mu.Unlock()
	if pings == 0 {
		t.Fatal("no ping")
	}
	if l := svc.Streams(); len(l) != 1 || l[0].Subject.Name != "ha" || l[0].Sent != 4 || l[0].Transport != "sse" || l[0].Remote != "10.0.0.5" {
		t.Fatalf("streams %+v", l)
	}
	// the page's x ends it
	lastID := strings.Fields(sink.msgs[3])[1]
	if !svc.Close(st.ID, "removed") {
		t.Fatal("close")
	}
	if r := <-done; r != "removed" || sink.closed != CloseNormal {
		t.Fatalf("ended %q %d", r, sink.closed)
	}
	cancel()
	// the trace: connect, one line per delivery, disconnect
	lines := tr.get()
	if len(lines) < 5 || !strings.HasPrefix(lines[0], `stream sse ha@10.0.0.5 connect filter={"address":["ABC0000001"],"type":["event","interface"]} last_event_id="" devices=false`) ||
		lines[1] != `→ sse ha@10.0.0.5 event HmIP-RF ABC0000001:1 PRESS_SHORT "1"` || lines[len(lines)-1] != "stream sse ha@10.0.0.5 disconnect sent=4" {
		t.Fatalf("trace %q", lines)
	}
	if len(svc.Streams()) != 0 {
		t.Fatal("still listed")
	}
	// resume from the last id: what came after (nothing yet), then live; the bus counted 4 seqs
	fd.send(t, [][3]string{{"ABC0000001:1", "PRESS_LONG", "1"}}, false)
	st2, _ := svc.Open(Subject{Kind: "token", Name: "ha"}, "websocket", "", Filter{})
	sink2 := &recSink{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go svc.Run(ctx2, st2, RunOptions{LastEventID: lastID}, sink2)
	sink2.wait(t, 2)
	if ty := sink2.types(); strings.Join(ty, ",") != "hello,event" || !strings.Contains(sink2.msgs[1], "PRESS_LONG") {
		t.Fatalf("resume %v", sink2.msgs)
	}
	// another boot id: resync boot; an id the ring cannot reach: resync gap
	st3, _ := svc.Open(Subject{Kind: "session", Name: "admin"}, "sse", "", Filter{})
	sink3 := &recSink{}
	go svc.Run(ctx2, st3, RunOptions{LastEventID: "deadbeef-3"}, sink3)
	sink3.wait(t, 2)
	if ty := sink3.types(); ty[1] != "resync" || !strings.Contains(sink3.msgs[1], `"reason":"boot"`) {
		t.Fatalf("boot resync %v", sink3.msgs)
	}
	if l := svc.Streams(); len(l) != 2 || l[1].LastResync != "boot" {
		t.Fatalf("last resync %+v", l)
	}
}

func TestStreamDevicesAndOverflow(t *testing.T) {
	fd, svc, _ := rig(t, Config{})
	st, _ := svc.Open(Subject{Kind: "token", Name: "x"}, "sse", "", Filter{})
	sink := &recSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan string, 1)
	go func() { done <- svc.Run(ctx, st, RunOptions{Devices: true}, sink) }()
	sink.wait(t, 2)
	if ty := sink.types(); ty[1] != "newDevices" || !strings.Contains(sink.msgs[1], `"ADDRESS":"ABC0000001"`) || !strings.Contains(sink.msgs[1], `"interface":"HmIP-RF"`) {
		t.Fatalf("devices %v", sink.msgs)
	}
	// a client that does not read: the bus drops for it, the stream says overflow and ends
	sink.mu.Lock()
	sink.slow = 5 * time.Millisecond
	sink.mu.Unlock()
	var burst [][3]string
	for i := 0; i < 1500; i++ {
		burst = append(burst, [3]string{"ABC0000001:1", "PRESS_SHORT", "1"})
	}
	fd.send(t, burst, true)
	select {
	case r := <-done:
		if r != "overflow" || sink.closed != CloseOverflow {
			t.Fatalf("ended %q %d", r, sink.closed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no overflow")
	}
	if ty := sink.types(); ty[len(ty)-1] != "resync" || !strings.Contains(sink.msgs[len(ty)-1], "overflow") {
		t.Fatalf("last %v", sink.msgs[len(ty)-1])
	}
}

// occulited task 19: 3 streams per token or session unless occulited.json says otherwise, and
// Limits says the ones in force
func TestLimitsDefault(t *testing.T) {
	_, svc, _ := rig(t, Config{})
	if per, total := svc.Limits(); per != 3 || total != 16 {
		t.Fatalf("defaults: %d %d", per, total)
	}
	a := Subject{Kind: "session", Name: "a"}
	for i := 0; i < 3; i++ {
		if _, err := svc.Open(a, "sse", "", Filter{}); err != nil {
			t.Fatalf("stream %d: %v", i+1, err)
		}
	}
	if _, err := svc.Open(a, "sse", "", Filter{}); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "3 per token") {
		t.Fatalf("fourth: %v", err)
	}
	_, svc5, _ := rig(t, Config{PerSubject: 5})
	if per, _ := svc5.Limits(); per != 5 {
		t.Fatalf("configured: %d", per)
	}
}

func TestLimitsDisabledAndRevoked(t *testing.T) {
	_, svc, _ := rig(t, Config{PerSubject: 2, Total: 3})
	a := Subject{Kind: "token", Name: "a"}
	if _, err := svc.Open(a, "sse", "", Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Open(a, "websocket", "", Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Open(a, "sse", "", Filter{}); err == nil || !strings.Contains(err.Error(), "per token") {
		t.Fatalf("third for a: %v", err)
	}
	b, _ := svc.Open(Subject{Kind: "session", Name: "b"}, "sse", "", Filter{})
	if _, err := svc.Open(Subject{Kind: "session", Name: "c"}, "sse", "", Filter{}); err == nil || !strings.Contains(err.Error(), "in total") {
		t.Fatalf("fourth: %v", err)
	}
	// a revoked credential ends the stream at the next heartbeat with 4003
	var valid atomic.Bool // read by the stream's heartbeat, written here
	valid.Store(true)
	sink := &recSink{}
	done := make(chan string, 1)
	go func() {
		done <- svc.Run(context.Background(), b, RunOptions{Valid: valid.Load}, sink)
	}()
	sink.wait(t, 1)
	valid.Store(false)
	if r := <-done; r != "unauthorized" || sink.closed != CloseUnauthorized {
		t.Fatalf("revoked: %q %d", r, sink.closed)
	}
	// CloseAll (the shutdown) ends the rest with 4004
	c, _ := svc.Open(Subject{Kind: "session", Name: "c"}, "sse", "", Filter{})
	sink2 := &recSink{}
	go func() { done <- svc.Run(context.Background(), c, RunOptions{}, sink2) }()
	sink2.wait(t, 1)
	svc.CloseAll("disabled")
	if r := <-done; r != "disabled" || sink2.closed != CloseDisabled {
		t.Fatalf("disabled: %q %d", r, sink2.closed)
	}
}

// wsClient is the smallest client: the handshake and a frame reader.
type wsClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func dialWS(t *testing.T, srvURL, path string) (*wsClient, http.Header) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srvURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	req := "GET " + path + " HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: " + Subprotocol + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("handshake: %s", resp.Status)
	}
	return &wsClient{conn: conn, r: r}, resp.Header
}

// frame reads one frame: opcode and payload.
func (c *wsClient) frame(t *testing.T) (byte, []byte) {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var h [2]byte
	if _, err := io_ReadFull(c.r, h[:]); err != nil {
		t.Fatalf("frame: %v", err)
	}
	n := uint64(h[1] & 0x7f)
	if n == 126 {
		var b [2]byte
		_, _ = io_ReadFull(c.r, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	} else if n == 127 {
		var b [8]byte
		_, _ = io_ReadFull(c.r, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	p := make([]byte, n)
	_, _ = io_ReadFull(c.r, p)
	return h[0] & 0x0f, p
}

// send writes one masked frame, as a client must.
func (c *wsClient) send(opcode byte, payload []byte) {
	mask := [4]byte{1, 2, 3, 4}
	b := []byte{0x80 | opcode, 0x80 | byte(len(payload))}
	b = append(b, mask[:]...)
	for i, p := range payload {
		b = append(b, p^mask[i%4])
	}
	_, _ = c.conn.Write(b)
}

func io_ReadFull(r *bufio.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestWebSocketStream(t *testing.T) {
	fd, svc, _ := rig(t, Config{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		st, _ := svc.Open(Subject{Kind: "token", Name: "ws"}, "websocket", "", ParseFilter(r.URL.Query()))
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-c.Done(); cancel() }()
		svc.Run(ctx, st, RunOptions{LastEventID: r.URL.Query().Get("last_event_id")}, NewWSSink(c))
	})
	// behind a middleware's wrapper that only says Unwrap, as main's statusRecorder does
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(&wrapped{ResponseWriter: w}, r)
	}))
	t.Cleanup(srv.Close)
	c, hdr := dialWS(t, srv.URL, "/events/ws?key=PRESS_SHORT")
	if hdr.Get("Sec-WebSocket-Protocol") != Subprotocol {
		t.Fatalf("subprotocol %q", hdr.Get("Sec-WebSocket-Protocol"))
	}
	op, p := c.frame(t)
	var m map[string]any
	if op != 0x1 || json.Unmarshal(p, &m) != nil || m["type"] != "hello" || m["id"] == nil {
		t.Fatalf("hello frame %x %s", op, p)
	}
	fd.send(t, [][3]string{{"A:1", "PRESS_LONG", "1"}, {"A:1", "PRESS_SHORT", "1"}}, false)
	op, p = c.frame(t)
	if op != 0x1 || json.Unmarshal(p, &m) != nil || m["type"] != "event" || m["data"].(map[string]any)["key"] != "PRESS_SHORT" {
		t.Fatalf("event frame %x %s", op, p)
	}
	// the heartbeat is a ping; the client's pong keeps it alive; a client ping is answered
	op, _ = c.frame(t)
	if op != 0x9 {
		t.Fatalf("expected a ping, got %x", op)
	}
	c.send(0xA, []byte("ping"))
	c.send(0x9, []byte("hi"))
	op, p = c.frame(t)
	if op != 0xA || string(p) != "hi" {
		t.Fatalf("pong %x %q", op, p)
	}
	// the page closes it: a close frame with 1000, and the stream is gone
	if l := svc.Streams(); len(l) != 1 || l[0].Transport != "websocket" {
		t.Fatalf("streams %+v", l)
	}
	svc.Close(svc.Streams()[0].ID, "removed")
	for {
		op, p = c.frame(t)
		if op == 0x9 {
			continue
		}
		break
	}
	if op != 0x8 || binary.BigEndian.Uint16(p[:2]) != CloseNormal {
		t.Fatalf("close %x %v", op, p)
	}
	time.Sleep(50 * time.Millisecond)
	if len(svc.Streams()) != 0 {
		t.Fatal("still listed")
	}
	// the client closing ends the handler
	c2, _ := dialWS(t, srv.URL, "/events/ws")
	c2.frame(t)
	c2.send(0x8, []byte{0x03, 0xe8})
	deadline := time.Now().Add(3 * time.Second)
	for len(svc.Streams()) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(svc.Streams()) != 0 {
		t.Fatal("a client's close did not end the stream")
	}
	// a plain GET is 426
	resp, _ := http.Get(srv.URL + "/events/ws")
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("plain GET: %d", resp.StatusCode)
	}
}

type wrapped struct{ http.ResponseWriter }

func (w *wrapped) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// B-169: the URL's path stays in the address a call is posted to - VirtualDevices is
// hmipserver's HTTP port with /groups, and / answers 404 there.
func TestHTTPAddrKeepsPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"xmlrpc://127.0.0.1:2010", "http://127.0.0.1:2010"},
		{"xmlrpc://127.0.0.1:39292/groups", "http://127.0.0.1:39292/groups"},
		{"xmlrpc_bin://127.0.0.1:32001", "http://127.0.0.1:32001"},
		{"http://127.0.0.1:9292/groups", "http://127.0.0.1:9292/groups"},
		{"https://ccu.example:42010/x", "https://ccu.example:42010/x"},
	} {
		if got, err := httpAddr(tc.in); err != nil || got != tc.want {
			t.Errorf("httpAddr(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"127.0.0.1:2010", "xmlrpc:///groups", "bin://127.0.0.1:1"} {
		if got, err := httpAddr(bad); err == nil {
			t.Errorf("httpAddr(%q) = %q, want an error", bad, got)
		}
	}

	d := &xmlrpc.BasicDispatcher{}
	d.AddSystemMethods()
	mux := http.NewServeMux()
	mux.Handle("/groups", &xmlrpc.Handler{Dispatcher: d})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr, err := httpAddr("xmlrpc://" + strings.TrimPrefix(srv.URL, "http://") + "/groups")
	if err != nil {
		t.Fatal(err)
	}
	svc := New(Config{})
	v, err := svc.Forward(context.Background(), Interface{Name: "VirtualDevices", addr: addr}, Call{Method: "system.listMethods"})
	if err != nil || v == nil || v.Array == nil || len(v.Array.Data) == 0 {
		t.Fatalf("system.listMethods on /groups: %v %v", v, err)
	}
}

// task 196, S2: {"double": 1} is a double, never an i4.
func TestFromJSONDouble(t *testing.T) {
	for _, in := range []any{json.Number("1"), 1.0, 2} {
		v, err := FromJSON(map[string]any{"double": in})
		if err != nil || v.Double == "" || v.I4 != "" || v.Int != "" {
			t.Errorf("%v: %+v %v", in, v, err)
		}
	}
	if _, err := FromJSON(map[string]any{"double": "x"}); err == nil {
		t.Error("a string taken as a double")
	}
	if v, _ := FromJSON(map[string]any{"double": json.Number("1"), "other": true}); v.Struct == nil {
		t.Error("a struct with a double member is a struct")
	}
}

// occulited task 5: the stream's updateDevice carries the hint (0 too), replaceDevice names the
// old and the new device beside the addresses; the other device messages stay as they were.
func TestConvertDeviceCalls(t *testing.T) {
	two, zero := 2, 0
	for _, c := range []struct {
		m    rpcsub.Message
		typ  string
		want string
	}{
		{rpcsub.Message{Type: "devices", Interface: "BidCos-RF", Op: "updated", Addresses: []string{"JEQ1"}, Hint: &two, TS: "t"}, "updateDevice", `{"addresses":["JEQ1"],"hint":2,"interface":"BidCos-RF","ts":"t"}`},
		{rpcsub.Message{Type: "devices", Interface: "BidCos-RF", Op: "updated", Addresses: []string{"JEQ1"}, Hint: &zero, TS: "t"}, "updateDevice", `{"addresses":["JEQ1"],"hint":0,"interface":"BidCos-RF","ts":"t"}`},
		{rpcsub.Message{Type: "devices", Interface: "HmIP-RF", Op: "replaced", Addresses: []string{"OLD", "NEW"}, Old: "OLD", New: "NEW", TS: "t"}, "replaceDevice", `{"addresses":["OLD","NEW"],"interface":"HmIP-RF","new":"NEW","old":"OLD","ts":"t"}`},
		{rpcsub.Message{Type: "devices", Interface: "HmIP-RF", Op: "deleted", Addresses: []string{"A"}, TS: "t"}, "deleteDevices", `{"addresses":["A"],"interface":"HmIP-RF","ts":"t"}`},
		// occulited task 44: a device's firmware state, with its transfer when the journal told one
		{rpcsub.Message{Type: "firmware", Interface: "HmIP-RF", Address: "00010000000A10", UpdateState: "DELIVER_FIRMWARE_IMAGE", Transfer: map[string]any{"state": "running", "percent": 42}, TS: "t"}, "firmware", `{"address":"00010000000A10","interface":"HmIP-RF","transfer":{"percent":42,"state":"running"},"ts":"t","update_state":"DELIVER_FIRMWARE_IMAGE"}`},
		{rpcsub.Message{Type: "firmware", Interface: "HmIP-RF", Address: "00010000000A10", UpdateState: "READY_FOR_UPDATE", TS: "t"}, "firmware", `{"address":"00010000000A10","interface":"HmIP-RF","ts":"t","update_state":"READY_FOR_UPDATE"}`},
		// occulited task 95: the change time the store saw, on the event and the state message; absent when unknown
		{rpcsub.Message{Type: "event", Interface: "HmIP-RF", Address: "A:1", Key: "STATE", Value: 1, TS: "t", LC: "l", ChangedAt: "l"}, "event", `{"address":"A:1","changed_at":"l","confirmed":true,"interface":"HmIP-RF","key":"STATE","lc":"l","ts":"t","value":1}`},
		{rpcsub.Message{Type: "event", Interface: "HmIP-RF", Address: "A:1", Key: "STATE", Value: 1, TS: "t", LC: "l"}, "event", `{"address":"A:1","confirmed":true,"interface":"HmIP-RF","key":"STATE","lc":"l","ts":"t","value":1}`},
		{rpcsub.Message{Type: "state", Interface: "HmIP-RF", Address: "A:1", Key: "STATE", Value: 1, TS: "t", LC: "l", ChangedAt: "l", Source: "sweep"}, "state", `{"address":"A:1","changed_at":"l","datapoint":"STATE","interface":"HmIP-RF","lc":"l","source":"sweep","ts":"t","value":1}`},
	} {
		typ, data := convert(c.m)
		b, _ := json.Marshal(data)
		if typ != c.typ || string(b) != c.want {
			t.Errorf("%s: %s %s, want %s %s", c.m.Op, typ, b, c.typ, c.want)
		}
	}
}

// occulited B-45: a stream is a subscriber of every interface its filter takes - all without an
// interface filter, whatever else it filters
func TestStreamCarries(t *testing.T) {
	for _, tc := range []struct {
		f    Filter
		want map[string]bool
	}{
		{Filter{}, map[string]bool{"HmIP-RF": true, "BidCos-RF": true}},
		{Filter{Types: []string{"devices"}, Addresses: []string{"ABC"}}, map[string]bool{"HmIP-RF": true, "BidCos-RF": true}},
		{Filter{Interfaces: []string{"HmIP-RF"}}, map[string]bool{"HmIP-RF": true, "BidCos-RF": false}},
		{Filter{Interfaces: []string{"BidCos-RF", "CUxD"}}, map[string]bool{"HmIP-RF": false, "BidCos-RF": true, "CUxD": true}},
	} {
		for iface, want := range tc.want {
			if got := (Stream{Filter: tc.f}).Carries(iface); got != want {
				t.Errorf("%+v carries %s: %v", tc.f, iface, got)
			}
		}
	}
}

// B-46: Wrote names every setValue and putParamset an interface took, a multicall's one by one,
// and nothing else - not a read, not a call that failed.
func TestWroteNamesTheWritesAnInterfaceTook(t *testing.T) {
	var got []string
	_, svc, _ := rig(t, Config{Wrote: func(iface string, c Call) {
		a, _ := Address(c)
		got = append(got, iface+" "+c.Method+" "+a)
	}})
	i, _ := svc.Lookup("HmIP-RF")
	str := xmlrpc.NewString
	call := func(c Call) {
		t.Helper()
		if _, err := svc.Forward(context.Background(), i, c); err != nil {
			t.Fatal(err)
		}
	}
	call(Call{Method: "setValue", Params: []*xmlrpc.Value{str("ABC0000001:0"), str("STICKY_UNREACH"), xmlrpc.NewBool(false)}})
	call(Call{Method: "getValue", Params: []*xmlrpc.Value{str("ABC0000001:0"), str("UNREACH")}})
	inner := func(m string, ps ...interface{}) *xmlrpc.Value {
		v, _ := xmlrpc.NewMap(map[string]interface{}{"methodName": m, "params": ps})
		return v
	}
	call(Call{Method: "system.multicall", Params: []*xmlrpc.Value{{Array: &xmlrpc.Array{Data: []*xmlrpc.Value{inner("getValue", "ABC0000002:1", "STATE"), inner("setValue", "ABC0000002:0", "STICKY_UNREACH", false)}}}}})
	// a fault is no write
	if _, err := svc.Forward(context.Background(), i, Call{Method: "putParamset", Params: []*xmlrpc.Value{str("ABC0000001:0"), str("VALUES")}}); err == nil {
		t.Fatal("the fake has no putParamset: a fault was expected")
	}
	if fmt.Sprint(got) != "[HmIP-RF setValue ABC0000001:0 HmIP-RF setValue ABC0000002:0]" {
		t.Fatalf("wrote: %v", got)
	}
	for c, want := range map[*Call]string{{Method: "ping"}: "", {Method: "setValue", Params: []*xmlrpc.Value{{Array: &xmlrpc.Array{}}}}: ""} {
		if a, ok := Address(*c); ok || a != want {
			t.Errorf("Address(%+v) = %q, %v", c, a, ok)
		}
	}
}

// occulited task 44: a firmware message passes the interface and type filters as the devices
// messages do, and the address filter as an event does (it has no key, so key filters pass it).
func TestFilterMatchesFirmware(t *testing.T) {
	m := rpcsub.Message{Type: "firmware", Interface: "HmIP-RF", Address: "00010000000A10", UpdateState: "DELIVER_FIRMWARE_IMAGE"}
	for _, c := range []struct {
		f    Filter
		want bool
	}{
		{Filter{}, true},
		{Filter{Types: []string{"firmware"}}, true},
		{Filter{Types: []string{"event"}}, false},
		{Filter{Interfaces: []string{"HmIP-RF"}}, true},
		{Filter{Interfaces: []string{"BidCos-RF"}}, false},
		{Filter{Addresses: []string{"00010000000A10"}}, true},
		{Filter{Addresses: []string{"00010000000A11"}}, false},
		{Filter{Keys: []string{"LEVEL"}}, true},
	} {
		if got := c.f.matches("firmware", m); got != c.want {
			t.Errorf("%+v: %v, want %v", c.f, got, c.want)
		}
	}
}
