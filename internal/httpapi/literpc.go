package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/devstate"
	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/hobbyquaker/occulited/internal/paramsets"
	"github.com/hobbyquaker/occulited/internal/rpctrace"
	"github.com/hobbyquaker/occulited/internal/system"
)

// ---- lite-rpc: /api/rpc/v1 (task 77, D-70 to D-74, D-79, D-95, D-115) --------------------------
//
// The interface processes' XML-RPC over the web port with the API's credentials, and their events
// as a stream. Every route needs rpc:read at the table, and each call the tier its method needs
// (task 78, literpc.Tier); init is never forwarded (D-70). lite-rpc is always on (the maintainer,
// 2026-09-22, D-117: the switch of D-95 is gone - the App reads and writes through it).
//
// Browser sessions (D-79, D-115): a token is a program's and passes as it is. An account session
// that came in a cookie must prove the box's own origin (Sec-Fetch-Site same-origin, an Origin
// that is this host, or - from a browser that says neither - the API's header credential
// X-Occulite-Request, see originRefusal) - that stops cross-site WebSocket hijacking and CSRF on
// the stream - and a request (POST) needs the session in the Authorization header as well (D-78's
// header credential; the shell sends it), so the cookie alone cannot call methods. Nothing here is
// accepted from the query string (?sid=), tokens included.

const liteRPCPrefix = "/api/rpc/v1"

func (a *SystemAPI) registerLiteRPC(mux *http.ServeMux) {
	p := liteRPCPrefix
	route(mux, auth.ScopeRPCRead, "GET "+p+"/interfaces", a.liteInterfaces)
	route(mux, auth.ScopeRPCRead, "POST "+p+"/xmlrpc/{interface}", a.liteXMLRPC)
	route(mux, auth.ScopeRPCRead, "POST "+p+"/json/{interface}", a.liteJSON)
	route(mux, auth.ScopeRPCRead, "GET "+p+"/events", a.liteEvents)
	route(mux, auth.ScopeRPCRead, "GET "+p+"/events/ws", a.liteEventsWS)
	route(mux, auth.ScopeRPCRead, "GET "+p+"/state", a.liteState)     // task 194
	route(mux, auth.ScopeRPCRead, "GET "+p+"/history", a.liteHistory) // task 195
	// task 108: the paramset description cache, nrcc's paramsets.json schema
	route(mux, auth.ScopeRPCRead, "GET "+p+"/paramsets", a.liteParamsets)
	route(mux, auth.ScopeRPCRead, "POST "+p+"/paramsets/{interface}", a.liteParamsetsFor)
	// the open streams are the Interfaces page's (task 76's clients list), system:read/write
	route(mux, auth.ScopeSystemRead, "GET "+p+"/streams", a.liteStreams)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/streams/{id}", a.liteStreamClose)
	// task 79: the RPC trace's switch, the Interfaces page's
	route(mux, auth.ScopeSystemRead, "GET /api/system/v1/rpc-trace", a.rpcTraceGet)
	route(mux, auth.ScopeSystemWrite, "PUT /api/system/v1/rpc-trace", a.rpcTracePut)
}

// traceWho names a caller for the trace: the remote address and the token's or account's name
// - never the credential.
func traceWho(r *http.Request, sess *auth.Session) string {
	if sess.IsToken() {
		return remote(r) + " (token " + strings.TrimPrefix(sess.ID, auth.TokenSessionPrefix) + ")"
	}
	return remote(r) + " (user " + sess.User + ")"
}

// rpcTraceView is GET /rpc-trace: the switch, and whether the permanent choice deserves the
// SD-card warning - an ARM product whose journal persists (rpi3, rpi4 with the journal on the
// card; D-79).
type rpcTraceView struct {
	rpctrace.State
	SDWarning bool `json:"sd_warning"`
	Available bool `json:"available"`
}

func (a *SystemAPI) rpcTraceView() rpcTraceView {
	v := rpcTraceView{State: rpctrace.State{Mode: rpctrace.ModeOff}}
	if a.RPCTrace == nil {
		return v
	}
	v.State, v.Available = a.RPCTrace.State(), true
	ver := a.Root.ReadVersion()
	if (ver.Platform == "rpi3" || ver.Platform == "rpi4" || strings.HasPrefix(ver.Product, "rpi")) && a.Root.ReadJournalSetting().JournalWanted() == system.JournalPersistent {
		v.SDWarning = true
	}
	return v
}

func (a *SystemAPI) rpcTraceGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, a.rpcTraceView())
}

// rpcTracePut is PUT /rpc-trace {mode: off|until|permanent, minutes?}: a timed trace ends on
// its own, the deadline survives a restart.
func (a *SystemAPI) rpcTracePut(w http.ResponseWriter, r *http.Request) {
	if a.RPCTrace == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the RPC trace is not available on this system"})
		return
	}
	var b struct {
		Mode    string `json:"mode"`
		Minutes int    `json:"minutes"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if b.Mode == rpctrace.ModeUntil && (b.Minutes < 1 || b.Minutes > 60*24*30) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "minutes is 1 to 43200 (30 days) for a timed trace"})
		return
	}
	if err := a.RPCTrace.Set(b.Mode, time.Duration(b.Minutes)*time.Minute); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	withCaller(r, a.liteLog()).Info("rpc trace: switched", "mode", b.Mode, "minutes", b.Minutes)
	writeJSON(w, 200, a.rpcTraceView())
}

func (a *SystemAPI) liteLog() *slog.Logger {
	if a.LiteRPC != nil && a.LiteRPC.Log() != nil {
		return a.LiteRPC.Log()
	}
	return slog.Default()
}

// liteGate is every lite-rpc route's first step: the service, the credential's place, and the
// browser defences. write is true for the request paths.
func (a *SystemAPI) liteGate(w http.ResponseWriter, r *http.Request, write bool) (*auth.Session, bool) {
	if a.LiteRPC == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "lite-rpc is not available on this system"})
		return nil, false
	}
	if r.URL.Query().Get("sid") != "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "credentials are not accepted in the query string here: use the Authorization header"})
		return nil, false
	}
	sess := SessionFrom(r)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "login required"})
		return nil, false
	}
	if msg := browserRefusal(r, sess, write); msg != "" {
		a.liteLog().Warn("lite-rpc: a browser session refused", "user", sess.User, "path", r.URL.Path, "why", msg)
		writeJSON(w, http.StatusForbidden, apiError{Error: "forbidden", Message: msg})
		return nil, false
	}
	return sess, true
}

// bearerOf is the Authorization header's bearer value, "" when there is none.
func bearerOf(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// browserRefusal is the reason an account session is refused, "" when it passes. A token
// always passes: it is a program's credential in a header.
func browserRefusal(r *http.Request, sess *auth.Session, write bool) string {
	if sess.IsToken() {
		return ""
	}
	// task 193: the public principal has no session to put in a header; what it keeps is the
	// origin rule - a page of this system, not another site's page in a visitor's browser
	if sess.Method == auth.MethodPublic {
		return originRefusal(r)
	}
	viaHeader := bearerOf(r) == sess.ID
	if write && !viaHeader {
		return "a browser session calls methods only with the session in the Authorization header, not with the cookie alone"
	}
	if viaHeader {
		return ""
	}
	return originRefusal(r)
}

// originRefusal is the cookie-alone rule: the box's own origin, as the browser says it.
//
// What a browser says depends on where the page lives (occulited B-47, measured with Chromium
// 153): Sec-Fetch-Site goes out from a secure context only - HTTPS, localhost - and Origin on a
// WebSocket handshake, a CORS request to another origin and a POST, but not on a same-origin GET.
// So a page of this system opened over plain http://<address>/ - how most installations are
// reached - says nothing at all on its stream's GET, and neither does a no-cors GET (an <img>, a
// navigation) from another port of the same host, which carries the cookie too. The two are told
// apart by the API's header credential (X-Occulite-Request, task 259): a page of another origin
// cannot put a header of its own on a request without a CORS preflight, and nothing here answers
// one, so the request never leaves that browser. The header counts only where the browser says
// nothing: a Sec-Fetch-Site or an Origin that names another place refuses whatever else is sent.
func originRefusal(r *http.Request) string {
	switch sfs := r.Header.Get("Sec-Fetch-Site"); sfs {
	case "same-origin", "none":
		return ""
	case "":
	default:
		return "the stream is opened from this system's own pages only (Sec-Fetch-Site: " + sfs + ")"
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err == nil && strings.EqualFold(u.Host, requestHost(r)) {
			return ""
		}
		return "the stream is opened from this system's own pages only (Origin: " + o + ")"
	}
	if r.Header.Get(RequestHeader) != "" {
		return ""
	}
	return "the stream needs the session in the Authorization header, or a browser that says its origin (over plain HTTP: the header " + RequestHeader + ")"
}

// requestHost is the host the browser addressed: what lighttpd forwarded, or the Host header.
func requestHost(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return r.Host
}

// liteSubject is who a stream belongs to.
func liteSubject(sess *auth.Session, r *http.Request) literpc.Subject {
	if sess.IsToken() {
		return literpc.Subject{Kind: "token", Name: strings.TrimPrefix(sess.ID, auth.TokenSessionPrefix)}
	}
	if sess.Method == auth.MethodPublic {
		// task 193: every public browser is its own subject, or two phones would share the limit
		return literpc.Subject{Kind: "public", Name: remote(r)}
	}
	return literpc.Subject{Kind: "session", Name: sess.User}
}

// liteChecker refuses init and a method above the session's tier; the refusal is logged.
func (a *SystemAPI) liteChecker(sess *auth.Session, iface string) literpc.Checker {
	return func(c literpc.Call) error {
		for _, ic := range literpc.Calls(c) {
			if ic.Method == "init" {
				a.liteLog().Warn("lite-rpc: init refused", "user", sess.User, "interface", iface)
				return literpc.Refuse(literpc.InitFault)
			}
			if tier := literpc.Tier(ic); !sess.Has(tier) {
				a.liteLog().Warn("lite-rpc: method refused", "user", sess.User, "interface", iface, "method", ic.Method, "needs", string(tier))
				return literpc.Refuse("not permitted: " + ic.Method + " needs " + string(tier))
			}
		}
		return nil
	}
}

// liteInterfaces is GET /interfaces: what is offered, with whether each process answers.
func (a *SystemAPI) liteInterfaces(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.liteGate(w, r, false); !ok {
		return
	}
	writeJSON(w, 200, a.LiteRPC.Interfaces())
}

func (a *SystemAPI) liteInterface(w http.ResponseWriter, r *http.Request) (literpc.Interface, bool) {
	i, ok := a.LiteRPC.Lookup(r.PathValue("interface"))
	if !ok {
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-interface", Message: "no such interface: " + r.PathValue("interface")})
	}
	return i, ok
}

// liteXMLRPC is POST /xmlrpc/{interface}: one call parsed, checked and forwarded; the answer as
// the daemon gave it, in UTF-8. A process that does not answer is 503 with the interface named.
func (a *SystemAPI) liteXMLRPC(w http.ResponseWriter, r *http.Request) {
	sess, ok := a.liteGate(w, r, true)
	if !ok {
		return
	}
	i, ok := a.liteInterface(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	c, err := literpc.DecodeCall(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "not an XML-RPC call: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	if err := a.liteChecker(sess, i.Name)(c); err != nil {
		w.Write(literpc.EncodeResponse(nil, err))
		return
	}
	v, err := a.LiteRPC.Forward(literpc.WithSource(r.Context(), traceWho(r, sess), "xmlrpc"), i, c)
	if errors.Is(err, literpc.ErrDown) {
		a.liteLog().Warn("lite-rpc: the interface process does not answer", "interface", i.Name, "method", c.Method, "err", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "down", Message: err.Error()})
		return
	}
	w.Write(literpc.EncodeResponse(v, err))
}

// liteJSON is POST /json/{interface}: JSON-RPC 2.0, a batch as the multicall equivalent.
func (a *SystemAPI) liteJSON(w http.ResponseWriter, r *http.Request) {
	sess, ok := a.liteGate(w, r, true)
	if !ok {
		return
	}
	i, ok := a.liteInterface(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	out, st := a.LiteRPC.ServeJSON(literpc.WithSource(r.Context(), traceWho(r, sess), "json"), i, body, a.liteChecker(sess, i.Name))
	if st == 204 {
		w.WriteHeader(204)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(st)
	w.Write(out)
	w.Write([]byte("\n"))
}

// The bulk read's page size: 1000 entries by default (~190 KB of JSON), 5000 at most - a big
// installation's whole set is ~2 MB, which a slow client chokes on (task 194's measurement).
const (
	stateDefaultLimit = 1000
	stateMaxLimit     = 5000
)

// liteState is GET /state (task 194): the state store's entries - the last value of every
// datapoint of the chosen set with ts, lc, confirmed and source - filtered by interface=,
// address= (a device matches its channels) and datapoint= (key= is its alias, as the stream
// says it), all repeatable or comma-separated; paged by limit= and after= (the answer's next).
// event_id is where the stream resumes to get everything after this answer (Last-Event-ID).
func (a *SystemAPI) liteState(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.liteGate(w, r, false); !ok {
		return
	}
	if a.State == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the state store is not available on this system"})
		return
	}
	q := r.URL.Query()
	limit := stateDefaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > stateMaxLimit {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "limit is 1 to " + strconv.Itoa(stateMaxLimit)})
			return
		}
		limit = n
	}
	f := literpc.ParseFilter(q)
	// the resume point first: an event between it and the read is in both, which is harmless
	var eventID string
	if a.RPC != nil {
		eventID = a.RPC.BootID() + "-" + strconv.FormatUint(a.RPC.Seq(), 10)
	}
	page := a.State.Read(devstate.Filter{Interfaces: f.Interfaces, Addresses: f.Addresses, Datapoints: f.Keys}, q.Get("after"), limit)
	out := map[string]any{"entries": page.Entries, "total": page.Total, "unconfirmed": page.Unconfirmed, "event_id": eventID, "sweeps": a.State.Sweeps()}
	if page.Next != "" {
		out["next"] = page.Next
	}
	if q.Get("after") == "" {
		out["datapoints"] = devstate.Keys()
	}
	writeJSON(w, 200, out)
}

// liteHistory is GET /history (task 195): one series of the datapoint history as compact rows,
// [[ts, value], …] oldest first, ts in milliseconds since the epoch - from=/to= (RFC 3339 or
// milliseconds) cut it, max_points= keeps every nth row counted from the newest. 404 when the
// datapoint is not recorded at all; [] when it is and nothing came yet.
func (a *SystemAPI) liteHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.liteGate(w, r, false); !ok {
		return
	}
	if a.History == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the datapoint history is not available on this system"})
		return
	}
	q := r.URL.Query()
	iface, address, dp := q.Get("interface"), q.Get("address"), q.Get("datapoint")
	if dp == "" {
		dp = q.Get("key")
	}
	if iface == "" || address == "" || dp == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "interface, address and datapoint are required: a card asks for one series"})
		return
	}
	var from, to time.Time
	for _, p := range []struct {
		name string
		t    *time.Time
	}{{"from", &from}, {"to", &to}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			*p.t = time.UnixMilli(ms)
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			*p.t = t
		} else {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: p.name + " is RFC 3339 or milliseconds since the epoch"})
			return
		}
	}
	maxPoints := 0
	if v := q.Get("max_points"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "max_points is a positive number"})
			return
		}
		maxPoints = n
	}
	_, points, ok := a.History.Read(iface, address, dp, from, to, maxPoints)
	if !ok {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-recorded", Message: dp + " is not in the datapoint history's list (System → Log → settings → History)"})
		return
	}
	out := make([][]any, 0, len(points))
	for _, p := range points {
		out = append(out, []any{p.At.UnixMilli(), p.Value})
	}
	writeJSON(w, 200, out)
}

// liteRunOptions reads the stream's query and makes the credential re-check.
func (a *SystemAPI) liteRunOptions(r *http.Request, sess *auth.Session) literpc.RunOptions {
	q := r.URL.Query()
	last := r.Header.Get("Last-Event-ID")
	if last == "" {
		last = q.Get("last_event_id")
	}
	opt := literpc.RunOptions{LastEventID: last, Devices: q.Get("devices") == "1" || q.Get("devices") == "true"}
	if a.Revalidate != nil {
		opt.Valid = func() bool {
			s := a.Revalidate(r)
			return s != nil && s.ID == sess.ID && s.Has(auth.ScopeRPCRead)
		}
	}
	return opt
}

func (a *SystemAPI) liteOpen(w http.ResponseWriter, r *http.Request, sess *auth.Session, transport string) (*literpc.Stream, bool) {
	st, err := a.LiteRPC.Open(liteSubject(sess, r), transport, remote(r), literpc.ParseFilter(r.URL.Query()))
	if err != nil {
		a.liteLog().Warn("lite-rpc: a stream refused", "user", sess.User, "transport", transport, "remote", remote(r), "err", err)
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "too-many-streams", Message: err.Error()})
		return nil, false
	}
	return st, true
}

// liteEvents is GET /events: the SSE stream.
func (a *SystemAPI) liteEvents(w http.ResponseWriter, r *http.Request) {
	sess, ok := a.liteGate(w, r, false)
	if !ok {
		return
	}
	st, ok := a.liteOpen(w, r, sess, "sse")
	if !ok {
		return
	}
	log := a.liteLog().With("stream", st.ID, "transport", "sse", "user", sess.User, "remote", st.Remote)
	log.Info("lite-rpc: stream opened", "filter", st.Filter)
	why := a.LiteRPC.Run(r.Context(), st, a.liteRunOptions(r, sess), literpc.NewSSESink(w))
	log.Info("lite-rpc: stream closed", "why", why, "sent", st.Sent)
}

// liteEventsWS is GET /events/ws: the same stream over WebSocket (D-74).
func (a *SystemAPI) liteEventsWS(w http.ResponseWriter, r *http.Request) {
	sess, ok := a.liteGate(w, r, false)
	if !ok {
		return
	}
	if !literpc.IsWebSocket(r) {
		writeJSON(w, http.StatusUpgradeRequired, apiError{Error: "upgrade-required", Message: "this path speaks WebSocket; the SSE stream is /api/rpc/v1/events"})
		return
	}
	st, ok := a.liteOpen(w, r, sess, "websocket")
	if !ok {
		return
	}
	opt := a.liteRunOptions(r, sess)
	c, err := literpc.Upgrade(w, r)
	if err != nil {
		a.LiteRPC.Close(st.ID, "handshake failed")
		return
	}
	log := a.liteLog().With("stream", st.ID, "transport", "websocket", "user", sess.User, "remote", st.Remote)
	log.Info("lite-rpc: stream opened", "filter", st.Filter)
	// the request's context, not a fresh one: it stays valid after the hijack until this
	// handler returns, and it is what the stop cancels - a background context left the
	// WebSocket streams open through the stop, to end without a close frame when the process
	// exited (occulited B-14)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-c.Done():
		case <-ctx.Done():
		}
		cancel()
	}()
	why := a.LiteRPC.Run(ctx, st, opt, literpc.NewWSSink(c))
	log.Info("lite-rpc: stream closed", "why", why, "sent", st.Sent)
}

// liteStreams is GET /streams: the open ones, for the Interfaces page.
func (a *SystemAPI) liteStreams(w http.ResponseWriter, _ *http.Request) {
	if a.LiteRPC == nil {
		writeJSON(w, 200, map[string]any{"streams": []literpc.Stream{}, "limits": map[string]int{"per_token": literpc.PerSubject, "total": literpc.Total}})
		return
	}
	per, total := a.LiteRPC.Limits()
	writeJSON(w, 200, map[string]any{"streams": a.LiteRPC.Streams(), "limits": map[string]int{"per_token": per, "total": total}})
}

// liteStreamClose is DELETE /streams/{id}: the page's x.
func (a *SystemAPI) liteStreamClose(w http.ResponseWriter, r *http.Request) {
	if a.LiteRPC == nil || !a.LiteRPC.Close(r.PathValue("id"), "removed") {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-found", Message: "no such stream (any more)"})
		return
	}
	withCaller(r, a.liteLog()).Info("lite-rpc: a stream closed from the Interfaces page", "stream", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// liteView is the Remote access page's lite object: lite-rpc is always on (D-117), so it says
// the limits and the ring, and whether the service exists at all.
type liteView struct {
	Streams struct {
		PerToken int `json:"per_token"`
		Total    int `json:"total"`
		Open     int `json:"open"`
	} `json:"streams"`
	Buffer struct {
		Seconds int `json:"seconds"`
		Events  int `json:"events"`
	} `json:"buffer"`
	// Available: false on a system without the service (development).
	Available bool `json:"available"`
}

func (a *SystemAPI) liteRPCView() liteView {
	var v liteView
	v.Streams.PerToken, v.Streams.Total = literpc.PerSubject, literpc.Total
	v.Buffer.Seconds, v.Buffer.Events = literpc.BufferSeconds, literpc.BufferEvents
	if a.LiteRPC == nil {
		return v
	}
	v.Available = true
	v.Streams.PerToken, v.Streams.Total = a.LiteRPC.Limits()
	v.Streams.Open = len(a.LiteRPC.Streams())
	return v
}

// ---- task 108: the paramset description cache --------------------------------------------------

// liteParamsets is GET /paramsets: the whole cache in node-red-contrib-ccu's paramsets.json
// schema - one object keyed "<interface>/<device type>/<firmware>/<version>/<channel type>/<paramset>",
// each value the description, parameter id → its description.
func (a *SystemAPI) liteParamsets(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.liteGate(w, r, false); !ok {
		return
	}
	if a.Paramsets == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the paramset description cache is not available on this system"})
		return
	}
	writeJSON(w, 200, a.Paramsets.All())
}

// paramsetsRequest is POST /paramsets/{interface}'s body.
type paramsetsRequest struct {
	Addresses []string `json:"addresses"`
	Paramset  string   `json:"paramset"`
}

// paramsetsMax bounds one request's addresses.
const paramsetsMax = 2000

// liteParamsetsFor is POST /paramsets/{interface} {addresses, paramset?}: the descriptions of the
// channels' (or devices') paramsets, each once by its key - {keys: {address: key}, paramsets:
// {key: description}, errors: {address: message}}. A description the cache lacks is read from
// the process once and kept; an address the process does not know is in errors.
func (a *SystemAPI) liteParamsetsFor(w http.ResponseWriter, r *http.Request) {
	sess, ok := a.liteGate(w, r, false)
	if !ok {
		return
	}
	if a.Paramsets == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the paramset description cache is not available on this system"})
		return
	}
	i, ok := a.liteInterface(w, r)
	if !ok {
		return
	}
	var req paramsetsRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	if len(req.Addresses) == 0 || len(req.Addresses) > paramsetsMax {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "addresses: 1 to " + strconv.Itoa(paramsetsMax)})
		return
	}
	if req.Paramset == "" {
		req.Paramset = "VALUES"
	}
	// the tier of getParamsetDescription, as a call would need it (rpc:read)
	if err := a.liteChecker(sess, i.Name)(literpc.Call{Method: "getParamsetDescription"}); err != nil {
		writeJSON(w, http.StatusForbidden, apiError{Error: "forbidden", Message: err.Error()})
		return
	}
	ctx := literpc.WithSource(r.Context(), traceWho(r, sess), "json")
	keys := map[string]string{}
	descs := map[string]paramsets.Description{}
	errs := map[string]string{}
	for _, addr := range req.Addresses {
		if addr == "" {
			continue
		}
		key, d, err := a.Paramsets.Describe(ctx, i.Name, addr, req.Paramset)
		if errors.Is(err, literpc.ErrDown) {
			a.liteLog().Warn("lite-rpc: the interface process does not answer", "interface", i.Name, "method", "getParamsetDescription", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "down", Message: err.Error()})
			return
		}
		if err != nil {
			errs[addr] = err.Error()
			continue
		}
		keys[addr] = key
		descs[key] = d
	}
	writeJSON(w, 200, map[string]any{"keys": keys, "paramsets": descs, "errors": errs})
}
