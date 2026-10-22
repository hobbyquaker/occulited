package rpcsub

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpctrace"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// The callback listener: one HTTP server on the loopback, one XML-RPC handler per interface
// under /cb/<name>, and GET /status for the long run and for a look with curl. Every method
// answers at once with what a callback client answers; the events go onto the bus from the
// handler, which is a channel send, never a call back to a daemon.

// listen binds the callback listener and serves it.
func (s *Subscriber) listen() (*http.Server, int, error) {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc(CallbackPath, s.serveCallback)
	mux.HandleFunc("GET /status", s.serveStatus)
	srv := callbackServer(mux, callbackReadTimeout, callbackIdleTimeout)
	go func() { _ = srv.Serve(ln) }()
	return srv, port, nil
}

// The listener's timeouts. The daemons' callback clients keep their connections to us open
// between calls: hmipserver's JDK HttpClient pools them with an idle timeout of 1200 s, and
// sends the next event on a pooled connection without checking it first. A server that
// closes an idle connection sooner than that races the client at the idle boundary - the
// request goes out onto a socket we are closing, the client sees "Connection reset" and the
// event is lost (B-101: one to three such RpcIOExceptions a day on a production system).
// So the idle timeout stays above the client pool's; a connection is only ours to close
// when the client has given up on it. The read and write timeouts bound one call, not the
// idle wait between calls (Go uses ReadTimeout for that only when IdleTimeout is unset).
// Loopback only (D-29): the open connections cost nothing but a file descriptor each.
const (
	callbackReadTimeout = 60 * time.Second
	callbackIdleTimeout = 1300 * time.Second
)

// callbackServer builds the listener's HTTP server with the given bounds on one call (read)
// and on the keep-alive wait between two (idle).
func callbackServer(h http.Handler, read, idle time.Duration) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: read, WriteTimeout: 60 * time.Second, IdleTimeout: idle}
}

func (s *Subscriber) serveCallback(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, CallbackPath))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	i := s.ifaces[name]
	s.mu.Unlock()
	if i == nil {
		http.NotFound(w, r)
		return
	}
	i.handler.ServeHTTP(w, r)
}

func (s *Subscriber) serveStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	body := map[string]any{"boot_id": s.bootID, "seq": s.bus.seq(), "interfaces": s.statusLocked()}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(body)
}

// handlerFor builds the interface's XML-RPC handler. The methods are the ones the daemons call on
// a subscriber (measured): system.listMethods and listDevices at init, newDevices right after,
// event and system.multicall for the events, and the device-list changes.
func (s *Subscriber) handlerFor(i *iface) *xmlrpc.Handler {
	d := &xmlrpc.BasicDispatcher{}
	d.AddSystemMethods() // system.listMethods and system.methodHelp; multicall is replaced below
	empty := func() *xmlrpc.Value { return xmlrpc.NewString("") }
	seen := func() {
		s.mu.Lock()
		i.calls++
		i.lastAct = s.now()
		i.lastCB = i.lastAct
		if i.stallKind == StallDelivery {
			i.stallKind, i.stallSince = "", time.Time{} // it delivers again
			s.log.Info("rpc: the daemon delivers again", "interface", i.name)
		}
		if i.state == "silent" {
			i.state = "up"
		}
		s.mu.Unlock()
	}
	// a daemon that calls listDevices or listMethods while we are not in an init has restarted
	// and kept our entry (hmipserver, VirtualDevices) or is looking us up (rfd at its start)
	//
	// A kept entry is registered again at the next tick (openccu-lite B-286): hmipserver calls
	// listDevices and newDevices on the entries it restores from its handlers file, and answers
	// pings to them, but delivers no events to them until a fresh init - measured on a lab
	// system, where five minutes after its restart the kept entry had seen nothing and a fresh
	// init brought the events back within seconds. rfd's lookup at its start costs one cheap
	// init the same way.
	unsolicited := func() {
		s.mu.Lock()
		restarted := !i.inInit && i.registered && !i.reinit && s.now().Sub(i.lastInit) > s.cfg.InitGrace
		if restarted {
			i.restored = true
			i.reinit = true
		}
		s.mu.Unlock()
		if restarted {
			s.log.Info("rpc: the daemon called us on its own - it restarted and kept the entry; registering afresh, a kept entry gets no events", "interface", i.name)
			s.bus.publish(Message{Type: "interface", Interface: i.name, State: "restarted"})
		}
	}
	d.HandleFunc("listDevices", func(*xmlrpc.Value) (*xmlrpc.Value, error) {
		seen()
		unsolicited()
		return &xmlrpc.Value{Array: &xmlrpc.Array{Data: []*xmlrpc.Value{}}}, nil
	})
	d.HandleFunc("system.listMethods", func(*xmlrpc.Value) (*xmlrpc.Value, error) {
		seen()
		unsolicited()
		names := []*xmlrpc.Value{}
		for _, n := range []string{"system.listMethods", "system.multicall", "listDevices", "newDevices", "deleteDevices", "updateDevice", "replaceDevice", "readdedDevice", "event"} {
			names = append(names, xmlrpc.NewString(n))
		}
		return &xmlrpc.Value{Array: &xmlrpc.Array{Data: names}}, nil
	})
	devices := func(op string, addrs func(q *xmlrpc.Query) []string) func(*xmlrpc.Value) (*xmlrpc.Value, error) {
		return func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
			seen()
			q := xmlrpc.Q(args)
			list := addrs(q)
			s.bus.publish(Message{Type: "devices", Interface: i.name, Op: op, Addresses: list})
			return empty(), nil
		}
	}
	// newDevices(id, [description...]): the addresses are the descriptions' ADDRESS members
	d.HandleFunc("newDevices", devices("new", func(q *xmlrpc.Query) []string {
		var out []string
		for _, dq := range q.Idx(1).Slice() {
			if a := dq.TryKey("ADDRESS").String(); a != "" {
				out = append(out, a)
			}
		}
		return out
	}))
	d.HandleFunc("deleteDevices", devices("deleted", func(q *xmlrpc.Query) []string { return q.Idx(1).Strings() }))
	d.HandleFunc("readdedDevice", devices("readded", func(q *xmlrpc.Query) []string { return q.Idx(1).Strings() }))
	// updateDevice(id, address, hint) and replaceDevice(id, old, new) carry more than addresses
	// (occulited task 5): the hint, which a consumer acts on (2: read the description again), and
	// which of the two is the new device. The address is read first: a missing argument marks the
	// whole query as failed, and every read after it is empty.
	d.HandleFunc("updateDevice", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		seen()
		q := xmlrpc.Q(args)
		addr := q.Idx(1).String()
		hint := q.Idx(2).Int() // 0 when the interface sent none
		s.bus.publish(Message{Type: "devices", Interface: i.name, Op: "updated", Addresses: []string{addr}, Hint: &hint})
		return empty(), nil
	})
	d.HandleFunc("replaceDevice", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		seen()
		q := xmlrpc.Q(args)
		old, nw := q.Idx(1).String(), q.Idx(2).String()
		s.bus.publish(Message{Type: "devices", Interface: i.name, Op: "replaced", Addresses: []string{old, nw}, Old: old, New: nw})
		return empty(), nil
	})
	// event(id, address, key, value); says whether it is a device's event - every one but the
	// PONG of a ping, which is RPC traffic and no radio telegram (occulited task 13)
	event := func(args *xmlrpc.Value, batch uint64) bool {
		q := xmlrpc.Q(args)
		addr, key := q.Idx(1).String(), q.Idx(2).String()
		val := anyValue(q.Idx(3))
		s.mu.Lock()
		i.events++
		s.mu.Unlock()
		s.bus.publish(Message{Type: "event", Interface: i.name, Address: addr, Key: key, Value: val, Batch: batch})
		return key != "PONG"
	}
	// telegram counts one call that carried a device's events (occulited task 13): a device sends
	// a channel's datapoints in one telegram, which reaches a subscriber as one system.multicall of
	// several events - the Status page's rate is of telegrams, not of datapoints (maintainer)
	telegram := func() {
		s.mu.Lock()
		i.telegrams++
		s.mu.Unlock()
	}
	d.HandleFunc("event", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		seen()
		if event(args, 0) {
			telegram()
		}
		return empty(), nil
	})
	// system.multicall: rfd sends every event this way, hmipserver the batches of one device.
	// The stock multicall would do, but the batch mark needs the seq of the first event of the
	// call, and the answer is one [""] per call, which is what the daemons get from everyone
	d.HandleFunc("system.multicall", func(args *xmlrpc.Value) (*xmlrpc.Value, error) {
		seen()
		calls := xmlrpc.Q(args).Idx(0).Slice()
		batch := s.bus.seq() + 1
		results := []*xmlrpc.Value{}
		device := false
		for _, c := range calls {
			method := c.TryKey("methodName").String()
			params := c.TryKey("params").Value()
			switch method {
			case "event":
				if params != nil && event(params, batch) {
					device = true
				}
			case "":
			default:
				if params != nil {
					if _, err := d.Dispatch(method, params); err != nil {
						s.log.Debug("rpc: multicall member failed", "interface", i.name, "method", method, "err", err)
					}
				}
			}
			results = append(results, &xmlrpc.Value{Array: &xmlrpc.Array{Data: []*xmlrpc.Value{empty()}}})
		}
		if device {
			telegram()
		}
		return &xmlrpc.Value{Array: &xmlrpc.Array{Data: results}}, nil
	})
	d.HandleUnknownFunc(func(name string, _ *xmlrpc.Value) (*xmlrpc.Value, error) {
		seen()
		s.log.Debug("rpc: unknown callback method", "interface", i.name, "method", name)
		return empty(), nil
	})
	return &xmlrpc.Handler{Dispatcher: &tracedDispatcher{BasicDispatcher: d, trace: s.cfg.Trace, name: i.name}, RequestSizeLimit: 8 << 20} // newDevices is 200 kB on the Charly
}

// tracedDispatcher writes a daemon's call to the listener as a trace line (task 79): the
// process as the source, occulited as the target, the method and its parameters; the answer
// as a second line when it says something (listDevices' list; an event's "" is not a line).
type tracedDispatcher struct {
	*xmlrpc.BasicDispatcher
	trace rpctrace.Tracing
	name  string
}

func (t *tracedDispatcher) Dispatch(method string, args *xmlrpc.Value) (*xmlrpc.Value, error) {
	if t.trace == nil || !t.trace.On() {
		return t.BasicDispatcher.Dispatch(method, args)
	}
	t.trace.Line("xmlrpc " + t.name + " → occulited " + method + " " + rpctrace.Value(anyValue(xmlrpc.Q(args))))
	v, err := t.BasicDispatcher.Dispatch(method, args)
	switch {
	case err != nil:
		t.trace.Line("← occulited " + t.name + " " + method + " fault " + rpctrace.Value(err.Error()))
	case v != nil && (v.Array != nil || v.Struct != nil):
		t.trace.Line("← occulited " + t.name + " " + method + " " + rpctrace.Value(anyValue(xmlrpc.Q(v))))
	}
	return v, err
}

// anyValue turns an XML-RPC value into what JSON carries: the scalars as they are, a struct as
// an object, an array as a list, a dateTime or base64 as its string.
func anyValue(q *xmlrpc.Query) any {
	v := q.Value()
	if v == nil {
		return nil
	}
	switch {
	case v.Struct != nil:
		m := map[string]any{}
		for _, mb := range v.Struct.Members {
			m[mb.Name] = anyValue(xmlrpc.Q(mb.Value))
		}
		return m
	case v.Array != nil:
		l := []any{}
		for _, e := range v.Array.Data {
			l = append(l, anyValue(xmlrpc.Q(e)))
		}
		return l
	case v.I4 != "" || v.Int != "":
		n, _ := strconv.Atoi(strings.TrimSpace(firstOf(v.I4, v.Int)))
		return n
	case v.Boolean != "":
		return strings.TrimSpace(v.Boolean) == "1" || strings.EqualFold(strings.TrimSpace(v.Boolean), "true")
	case v.Double != "":
		f, _ := strconv.ParseFloat(strings.TrimSpace(v.Double), 64)
		return f
	case v.DateTime != "":
		return v.DateTime
	case v.Base64 != "":
		return v.Base64
	case v.ElemString != "":
		return v.ElemString
	}
	return v.FlatString
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
