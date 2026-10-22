// Package literpc is lite-rpc (task 77, D-70 to D-74, D-95, D-115): the interface processes'
// XML-RPC reachable over the web port with the API's own credentials, and their events as a
// stream over SSE or WebSocket instead of a callback server at the client. It lives inside
// occulited (D-115): the request paths forward one call each to a daemon's loopback port, the
// stream reads task 75's bus, whose ring buffer gives a reconnecting client the five minutes it
// missed.
//
// Nothing here knows HTTP sessions: the httpapi layer authenticates, checks the browser
// defences and hands over a Subject - who the stream belongs to, for the limits and the list.
package literpc

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/rpctrace"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
)

const (
	// PerSubject and Total are the stream limits (D-79): 3 per token or session by default, 16
	// in all, SSE and WebSocket counted together. The per-session limit is occulited.json's
	// rpc.streams_per_session (occulited task 19: it was 2, and the Control app keeps a stream per
	// window open - a wall tablet, a phone and a desktop under one account).
	PerSubject = 3
	Total      = 16
	// Heartbeat is the SSE ": ping" and the WebSocket ping interval; a client treats 45 s of
	// silence as a dead connection.
	Heartbeat = 15 * time.Second
	// BufferSeconds and BufferEvents are the ring buffer's bounds, as rpcsub keeps it - here for
	// the hello message and the page.
	BufferSeconds = 300
	BufferEvents  = 5000
	// InitFault is the fault text for init, alone or inside a multicall (D-70: no callbacks).
	InitFault = "init is not available remotely on openccu-lite: subscribe to /api/rpc/v1/events - see docs/rpc-remote.md"
	// Subprotocol is the WebSocket subprotocol the server echoes when the client offers it.
	Subprotocol = "openccu-lite.rpc-events.v1"
)

// Config wires the service.
type Config struct {
	// Sub is task 75's subscriber: the bus the stream reads and the interface list.
	Sub *rpcsub.Subscriber
	// Timeout bounds one forwarded call (30 s by default: hmipserver's listDevices takes a
	// few seconds on a Pi 3).
	Timeout time.Duration
	Log     *slog.Logger
	Now     func() time.Time
	// PerSubject and Total override the limits: PerSubject from occulited.json's
	// rpc.streams_per_session, Total in the tests. 0 = the default.
	PerSubject, Total int
	// Heartbeat overrides the interval (tests).
	Heartbeat time.Duration
	// Trace takes the calls, answers, stream deliveries and stream life as lines (task 79);
	// nil = no trace.
	Trace rpctrace.Tracing
	// Wrote is told every setValue and putParamset an interface took (a multicall's inner
	// calls one by one), after its answer: the interface processes send no event for some
	// writes - the acknowledgement of a sticky service message, occulited B-46 - so whoever
	// keeps such a value reads it again. nil = nobody.
	Wrote func(iface string, c Call)
	// Descriptions answers getParamsetDescription from the paramset description cache (task
	// 108): the process is asked once per device model, firmware and channel type, every later
	// call is served here. nil = every call goes to the process.
	Descriptions Descriptions
}

// Descriptions is the paramset description cache as Forward sees it (task 108).
type Descriptions interface {
	// Description is getParamsetDescription(address, paramset) on iface, from the cache or the
	// process. The cache calls the process through the same Service (its Forward), so the
	// trace sees what it asks.
	Description(ctx context.Context, iface, address, paramset string) (*xmlrpc.Value, error)
}

// sourceKey carries who calls and how (xmlrpc or json) from the HTTP layer to Forward's
// trace lines.
type sourceKey struct{}

type source struct{ who, enc string }

// WithSource marks a request's context with the caller as the trace names it - the remote
// address and the token's or account's name, never the credential - and the encoding.
func WithSource(ctx context.Context, who, enc string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source{who: who, enc: enc})
}

func (s *Service) tracing() bool { return s.cfg.Trace != nil && s.cfg.Trace.On() }

// traceCall writes a forwarded call's line and returns the function for its answer.
func (s *Service) traceCall(ctx context.Context, i Interface, c Call) func(*xmlrpc.Value, error) {
	if !s.tracing() {
		return func(*xmlrpc.Value, error) {}
	}
	src, _ := ctx.Value(sourceKey{}).(source)
	if src.who == "" {
		src.who = "occulited"
	}
	if src.enc == "" {
		src.enc = "xmlrpc"
	}
	params := make([]any, 0, len(c.Params))
	for _, p := range c.Params {
		params = append(params, ToJSON(p))
	}
	s.cfg.Trace.Line(src.enc + " " + src.who + " → " + i.Name + " " + c.Method + " " + rpctrace.Value(params))
	return func(v *xmlrpc.Value, err error) {
		switch {
		case err != nil:
			var f *Fault
			if errors.As(err, &f) {
				s.cfg.Trace.Line("← " + i.Name + " " + src.who + " " + c.Method + " fault " + fmt.Sprint(f.Code) + " " + rpctrace.Value(f.Message))
			} else {
				s.cfg.Trace.Line("← " + i.Name + " " + src.who + " " + c.Method + " error " + rpctrace.Value(err.Error()))
			}
		default:
			s.cfg.Trace.Line("← " + i.Name + " " + src.who + " " + c.Method + " " + rpctrace.Value(ToJSON(v)))
		}
	}
}

// Service is lite-rpc's state: the forwarding client and the open streams.
type Service struct {
	cfg    Config
	log    *slog.Logger
	client *http.Client

	mu      sync.Mutex
	streams map[string]*Stream
	nextID  uint64
}

// New makes the service.
func New(cfg Config) *Service {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PerSubject == 0 {
		cfg.PerSubject = PerSubject
	}
	if cfg.Total == 0 {
		cfg.Total = Total
	}
	if cfg.Heartbeat == 0 {
		cfg.Heartbeat = Heartbeat
	}
	return &Service{cfg: cfg, log: cfg.Log, streams: map[string]*Stream{}, client: &http.Client{Timeout: cfg.Timeout}}
}

// Limits are the stream limits in force: per token or session, and in total.
func (s *Service) Limits() (perSubject, total int) { return s.cfg.PerSubject, s.cfg.Total }

// Log is the service's logger, for the HTTP layer's lines.
func (s *Service) Log() *slog.Logger { return s.log }

// Interface is one interface process as GET /interfaces lists it.
type Interface struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"` // xmlrpc
	URLPath  string `json:"url_path"`
	// Running: the subscriber's view - the process answered its last init or ping.
	Running bool   `json:"running"`
	addr    string // http://127.0.0.1:<port>[/path], where the call goes
}

// Interfaces lists what is offered: every InterfacesList.xml entry the subscriber speaks to
// (xmlrpc:// and http(s):// URLs, and BidCos-RF/BidCos-Wired on their xmlrpc_bin ports, which
// answer XML-RPC too); other BIN-RPC entries (CUxD) are not offered. Live: rpcsub watches the
// file.
func (s *Service) Interfaces() []Interface {
	var out []Interface
	if s.cfg.Sub == nil {
		return []Interface{}
	}
	for _, st := range s.cfg.Sub.Status() {
		addr, err := httpAddr(st.URL)
		if err != nil {
			continue
		}
		out = append(out, Interface{Name: st.Name, Protocol: "xmlrpc", URLPath: "/api/rpc/v1/xmlrpc/" + st.Name, Running: st.State != "down", addr: addr})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	if out == nil {
		out = []Interface{}
	}
	return out
}

// Lookup finds one interface by its InterfacesList.xml name.
func (s *Service) Lookup(name string) (Interface, bool) {
	for _, i := range s.Interfaces() {
		if i.Name == name {
			return i, true
		}
	}
	return Interface{}, false
}

// httpAddr turns an InterfacesList.xml URL into the HTTP address of the process. "xmlrpc_bin"
// is not a scheme net/url accepts (the underscore), so split by hand. The URL's path stays:
// VirtualDevices is hmipserver's HTTP port with /groups (xmlrpc://127.0.0.1:39292/groups), and a
// call posted to / answers 404 there (B-169).
func httpAddr(raw string) (string, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return "", fmt.Errorf("no scheme in %q", raw)
	}
	switch scheme {
	case "xmlrpc", "http", "xmlrpc_bin":
		u, err := url.Parse("http://" + rest)
		if err != nil {
			return "", err
		}
		if u.Host == "" {
			return "", fmt.Errorf("no host in %q", raw)
		}
		return "http://" + u.Host + u.EscapedPath(), nil
	case "https":
		return "https://" + rest, nil
	}
	return "", fmt.Errorf("unsupported scheme %q", scheme)
}

// Call is one XML-RPC call as a client sent it: the method and its parameters.
type Call struct {
	Method string
	Params []*xmlrpc.Value
}

// ErrDown is an interface process that does not answer (503 with the interface named).
var ErrDown = errors.New("the interface process does not answer")

// ErrRefused is a call the service does not forward: init in any form.
var ErrRefused = errors.New("refused")

// Fault is an XML-RPC fault the daemon answered, or one the service made (init).
type Fault = xmlrpc.MethodError

// Calls flattens a call for the checks: the call itself, or every inner call of a
// system.multicall ([{methodName, params}]). A malformed multicall is one call named
// system.multicall, which the daemon then faults on.
func Calls(c Call) []Call {
	if c.Method != "system.multicall" || len(c.Params) != 1 || c.Params[0].Array == nil {
		return []Call{c}
	}
	var out []Call
	for _, inner := range c.Params[0].Array.Data {
		if inner.Struct == nil {
			return []Call{c}
		}
		var ic Call
		for _, m := range inner.Struct.Members {
			switch m.Name {
			case "methodName":
				ic.Method = xmlrpc.Q(m.Value).String()
			case "params":
				if m.Value.Array != nil {
					ic.Params = m.Value.Array.Data
				}
			}
		}
		out = append(out, ic)
	}
	if len(out) == 0 {
		return []Call{c}
	}
	return out
}

// Address is the address a call names in its first parameter (setValue, putParamset, getValue
// and the like), ok false when it has none or it is not a string.
func Address(c Call) (string, bool) {
	if len(c.Params) == 0 || c.Params[0] == nil {
		return "", false
	}
	q := xmlrpc.Q(c.Params[0])
	addr := q.String()
	return addr, q.Err() == nil && addr != ""
}

// Forward sends one call to the interface and returns the daemon's value, a *Fault it
// answered, or ErrDown. The request goes out in ISO-8859-1 as the daemons speak it (a
// character outside Latin-1 becomes a numeric character reference, which is valid XML), the
// answer is decoded by its declared charset.
func (s *Service) Forward(ctx context.Context, i Interface, c Call) (v *xmlrpc.Value, err error) {
	answer := s.traceCall(ctx, i, c)
	defer func() {
		answer(v, err)
		if err == nil && s.cfg.Wrote != nil {
			for _, ic := range Calls(c) {
				if ic.Method == "setValue" || ic.Method == "putParamset" {
					s.cfg.Wrote(i.Name, ic)
				}
			}
		}
	}()
	if s.cfg.Descriptions != nil && c.Method == "getParamsetDescription" && len(c.Params) == 2 && !fromCache(ctx) {
		// task 108: served from the description cache; the cache's own call to the process (a
		// miss) comes back through Forward marked, so it goes out
		addr, ps := xmlrpc.Q(c.Params[0]).String(), xmlrpc.Q(c.Params[1]).String()
		if addr != "" && ps != "" {
			return s.cfg.Descriptions.Description(ctx, i.Name, addr, ps)
		}
	}
	return s.send(ctx, i, c)
}

// cacheKey marks a context as the description cache's own call, which Forward sends to the
// process instead of asking the cache again.
type cacheKey struct{}

// FromCache marks ctx as the description cache's own call (task 108).
func FromCache(ctx context.Context) context.Context { return context.WithValue(ctx, cacheKey{}, true) }

func fromCache(ctx context.Context) bool { v, _ := ctx.Value(cacheKey{}).(bool); return v }

// send is the call on the wire: encoded in ISO-8859-1, posted to the process, the answer decoded.
func (s *Service) send(ctx context.Context, i Interface, c Call) (*xmlrpc.Value, error) {
	body, err := encodeCall(c)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.addr, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml")
	resp, err := s.client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "connection refused") {
			return nil, fmt.Errorf("%w: %s: %v", ErrDown, i.Name, err)
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrDown, i.Name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDown, i.Name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%w: %s: HTTP %s", ErrDown, i.Name, resp.Status)
	}
	return decodeResponse(raw)
}

// encodeCall is the request body in ISO-8859-1.
func encodeCall(c Call) ([]byte, error) {
	ps := make([]*xmlrpc.Param, len(c.Params))
	for i, p := range c.Params {
		ps[i] = &xmlrpc.Param{Value: p}
	}
	var buf bytes.Buffer
	w := encoding.HTMLEscapeUnsupported(charmap.ISO8859_1.NewEncoder()).Writer(&buf)
	if _, err := w.Write([]byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n")); err != nil {
		return nil, err
	}
	if err := xml.NewEncoder(w).Encode(&xmlrpc.MethodCall{MethodName: c.Method, Params: &xmlrpc.Params{Param: ps}}); err != nil {
		return nil, fmt.Errorf("encoding the call: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeCall parses a client's methodCall in whatever charset it declares.
func DecodeCall(raw []byte) (Call, error) {
	var mc xmlrpc.MethodCall
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.CharsetReader = charset.NewReaderLabel
	if err := dec.Decode(&mc); err != nil {
		return Call{}, err
	}
	if mc.MethodName == "" {
		return Call{}, errors.New("no methodName")
	}
	c := Call{Method: mc.MethodName}
	if mc.Params != nil {
		for _, p := range mc.Params.Param {
			v := p.Value
			if v == nil {
				v = &xmlrpc.Value{}
			}
			c.Params = append(c.Params, v)
		}
	}
	return c, nil
}

// decodeResponse parses the daemon's methodResponse: the value, or the fault as *Fault.
func decodeResponse(raw []byte) (*xmlrpc.Value, error) {
	var mr xmlrpc.MethodResponse
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.CharsetReader = charset.NewReaderLabel
	if err := dec.Decode(&mr); err != nil {
		return nil, fmt.Errorf("%w: undecodable answer: %v", ErrDown, err)
	}
	if mr.Fault != nil {
		q := xmlrpc.Q(mr.Fault)
		code, msg := q.Key("faultCode").Int(), q.Key("faultString").String()
		if q.Err() != nil {
			return nil, fmt.Errorf("%w: malformed fault: %v", ErrDown, q.Err())
		}
		return nil, &Fault{Code: code, Message: msg}
	}
	if mr.Params == nil || len(mr.Params.Param) != 1 || mr.Params.Param[0].Value == nil {
		// a void answer (some setValue implementations): an empty string, as the CCU says it
		return &xmlrpc.Value{}, nil
	}
	return mr.Params.Param[0].Value, nil
}

// EncodeResponse is the methodResponse for the client, in UTF-8: the value, or a fault for an
// error - a *Fault as it is, anything else as code -1.
func EncodeResponse(v *xmlrpc.Value, err error) []byte {
	var mr xmlrpc.MethodResponse
	if err != nil {
		code, msg := -1, err.Error()
		var f *Fault
		if errors.As(err, &f) {
			code, msg = f.Code, f.Message
		}
		mr.Fault = &xmlrpc.Value{Struct: &xmlrpc.Struct{Members: []*xmlrpc.Member{
			{Name: "faultCode", Value: &xmlrpc.Value{I4: fmt.Sprint(code)}},
			{Name: "faultString", Value: &xmlrpc.Value{FlatString: msg}},
		}}}
	} else {
		if v == nil {
			v = &xmlrpc.Value{}
		}
		mr.Params = &xmlrpc.Params{Param: []*xmlrpc.Param{{Value: v}}}
	}
	var buf bytes.Buffer
	buf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	_ = xml.NewEncoder(&buf).Encode(&mr)
	buf.WriteByte('\n')
	return buf.Bytes()
}

// IsInit says whether a call, or any inner call of a multicall, is init.
func IsInit(c Call) bool {
	for _, ic := range Calls(c) {
		if ic.Method == "init" {
			return true
		}
	}
	return false
}

// refusedError is a call the service does not forward, with the text the client sees.
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }
func (e *refusedError) Unwrap() error { return ErrRefused }

// Refuse makes the error for a call that is not forwarded: init, or a tier the caller lacks.
func Refuse(msg string) error { return &refusedError{msg: msg} }
