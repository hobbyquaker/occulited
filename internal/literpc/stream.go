package literpc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpcsub"
	"github.com/hobbyquaker/occulited/internal/rpctrace"
)

// The stream (D-70, D-72 to D-74, D-79): one per-connection queue on task 75's bus, two
// encoders - SSE and WebSocket - and one loop. Every message carries an id <boot_id>-<seq>;
// a client that reconnects with the last one it saw gets the ring's replay, or resync when
// the ring no longer reaches back that far (gap), the process is another one (boot), or the
// client did not read fast enough (overflow, and the connection is closed).

// Subject is who a stream belongs to, for the limits and the page: a token by name, or an
// account session by user.
type Subject struct {
	Kind string `json:"kind"` // token or session
	Name string `json:"name"`
}

// Key is the limit's key: one token's streams count together whichever session they came in
// on, one user's sessions too.
func (s Subject) Key() string { return s.Kind + ":" + s.Name }

// Filter is the query's filters: AND across kinds, OR within one. An address matches a device
// and its channels.
type Filter struct {
	Interfaces []string `json:"interface,omitempty"`
	Addresses  []string `json:"address,omitempty"`
	Keys       []string `json:"key,omitempty"`
	Types      []string `json:"type,omitempty"`
}

// ParseFilter reads the repeatable query parameters interface=, address=, key=, type=.
func ParseFilter(q url.Values) Filter {
	clean := func(vs []string) []string {
		var out []string
		for _, v := range vs {
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
		}
		return out
	}
	// datapoint= is key='s alias, the state store's word for it (task 194)
	return Filter{Interfaces: clean(q["interface"]), Addresses: clean(q["address"]), Keys: append(clean(q["key"]), clean(q["datapoint"])...), Types: clean(q["type"])}
}

// matches says whether a bus message passes: hello and resync always do; an interface or
// devices message is matched by interface and type only; a firmware message by interface, type
// and address (it has no key).
func (f Filter) matches(typ string, m rpcsub.Message) bool {
	if len(f.Types) > 0 && !has(f.Types, typ) {
		return false
	}
	if len(f.Interfaces) > 0 && !has(f.Interfaces, m.Interface) {
		return false
	}
	if m.Type != "event" && m.Type != "state" && m.Type != "firmware" {
		return true
	}
	if m.Type != "firmware" && len(f.Keys) > 0 && !has(f.Keys, m.Key) {
		return false
	}
	if len(f.Addresses) > 0 {
		ok := false
		for _, a := range f.Addresses {
			if m.Address == a || strings.HasPrefix(m.Address, a+":") {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Carries says whether the stream takes interface iface's messages (occulited B-45): its filter
// names that interface, or none. Such a stream is one of the interface's subscribers on the Status
// page, beside the callbacks registered with the process itself - whatever else it filters, as an
// XML-RPC client's init is a subscription however much of it the client then uses.
func (st Stream) Carries(iface string) bool {
	return len(st.Filter.Interfaces) == 0 || has(st.Filter.Interfaces, iface)
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Stream is one open connection.
type Stream struct {
	ID        string    `json:"id"`
	Transport string    `json:"transport"` // sse or websocket
	Subject   Subject   `json:"subject"`
	Remote    string    `json:"remote,omitempty"`
	Filter    Filter    `json:"filter"`
	Since     time.Time `json:"since"`
	// Sent counts the messages written, Dropped the ones the bus could not queue for this
	// connection (an overflow ends it), LastResync the last resync sent with its reason.
	Sent       uint64 `json:"sent"`
	Dropped    uint64 `json:"dropped"`
	LastResync string `json:"last_resync,omitempty"`

	cancel context.CancelFunc
	closed string // the reason it was closed from outside: removed, disabled
}

// ErrLimit is a stream over the per-subject or the total limit (429).
var ErrLimit = errors.New("too many streams")

// Open registers a stream, or refuses it over the limits.
func (s *Service) Open(subj Subject, transport, remote string, f Filter) (*Stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streams) >= s.cfg.Total {
		return nil, fmt.Errorf("%w: %d in total", ErrLimit, s.cfg.Total)
	}
	mine := 0
	for _, st := range s.streams {
		if st.Subject.Key() == subj.Key() {
			mine++
		}
	}
	if mine >= s.cfg.PerSubject {
		return nil, fmt.Errorf("%w: %d per token or session", ErrLimit, s.cfg.PerSubject)
	}
	s.nextID++
	st := &Stream{ID: strconv.FormatUint(s.nextID, 10), Transport: transport, Subject: subj, Remote: remote, Filter: f, Since: s.cfg.Now()}
	s.streams[st.ID] = st
	return st, nil
}

func (s *Service) release(st *Stream) {
	s.mu.Lock()
	delete(s.streams, st.ID)
	s.mu.Unlock()
}

// Wait returns once no stream is open any more, or when ctx ends (false then). The stop waits
// with it for the WebSocket streams: their connections are hijacked, so http.Server.Shutdown
// neither tracks nor waits for them, and without the wait the process could exit before a
// stream's close frame is written (occulited B-14).
func (s *Service) Wait(ctx context.Context) bool {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		s.mu.Lock()
		n := len(s.streams)
		s.mu.Unlock()
		if n == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}

// Close ends one stream from outside (the page's x): true when there was one.
func (s *Service) Close(id, reason string) bool {
	s.mu.Lock()
	st, ok := s.streams[id]
	if ok {
		st.closed = reason
	}
	s.mu.Unlock()
	if ok && st.cancel != nil {
		st.cancel()
	}
	return ok
}

// CloseAll ends every stream with a reason (close code 4004): the shutdown.
func (s *Service) CloseAll(reason string) {
	s.mu.Lock()
	var all []*Stream
	for _, st := range s.streams {
		st.closed = reason
		all = append(all, st)
	}
	s.mu.Unlock()
	for _, st := range all {
		if st.cancel != nil {
			st.cancel()
		}
	}
}

// Streams lists the open ones, oldest first.
func (s *Service) Streams() []Stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Stream, 0, len(s.streams))
	for _, st := range s.streams {
		out = append(out, *st)
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].Since.Before(out[b].Since) || out[a].Since.Equal(out[b].Since) && out[a].ID < out[b].ID
	})
	return out
}

// Sink is one transport's encoder: Send writes a message, Ping the heartbeat, and Close ends
// the connection with a reason the transport can express (a WebSocket close code).
type Sink interface {
	Send(id, typ string, data any) error
	Ping() error
	Close(code int, reason string)
}

// Close codes (D-79), the WebSocket ones; SSE just ends. CloseGoingAway is RFC 6455's 1001:
// occulited stops or restarts, and the client reconnects at once (occulited B-14) - unlike
// 4004, which says lite-rpc was switched off.
const (
	CloseOverflow     = 4001
	CloseUnauthorized = 4003
	CloseDisabled     = 4004
	CloseNormal       = 1000
	CloseGoingAway    = 1001
)

// ErrStopping is the cause the HTTP server's base context is cancelled with when occulited stops
// (cmd/occulited's endRequests): a stream whose context ends with it says "going away" rather
// than "client gone" (occulited B-14).
var ErrStopping = errors.New("occulited is stopping")

// RunOptions are one connection's query: the resume point, whether to send the device lists
// first, and how to re-check the credential (every heartbeat; a revoked token is closed).
type RunOptions struct {
	LastEventID string
	Devices     bool
	Valid       func() bool
}

// eventID is <boot_id>-<seq>.
func (s *Service) eventID(seq uint64) string {
	return s.cfg.Sub.BootID() + "-" + strconv.FormatUint(seq, 10)
}

// parseEventID splits a client's id; ok false when it is not one of ours.
func parseEventID(id string) (boot string, seq uint64, ok bool) {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.ParseUint(id[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return id[:i], n, true
}

// convert turns a bus message into the stream's type and data.
func convert(m rpcsub.Message) (typ string, data map[string]any) {
	switch m.Type {
	case "event":
		d := map[string]any{"interface": m.Interface, "address": m.Address, "key": m.Key, "value": m.Value, "ts": m.TS}
		if m.Batch != 0 && m.Batch != m.Seq {
			d["batch"] = m.Batch
		} else if m.Batch != 0 {
			d["batch"] = m.Seq
		}
		// task 194: a datapoint the state store keeps carries its last change - an event is its
		// confirmation, so confirmed is always true here
		if m.LC != "" {
			d["lc"], d["confirmed"] = m.LC, true
			if m.PreviousForS != nil {
				d["previous_for_s"] = *m.PreviousForS
			}
			if m.ChangedAt != "" {
				d["changed_at"] = m.ChangedAt
			}
		}
		return "event", d
	case "state":
		// task 194: the state store's sweep confirmed or changed an entry - the bulk read's shape
		d := map[string]any{"interface": m.Interface, "address": m.Address, "datapoint": m.Datapoint(), "value": m.Value, "ts": m.TS, "lc": m.LC, "source": m.Source}
		if m.Confirmed != nil {
			d["confirmed"] = *m.Confirmed
		}
		if m.PreviousForS != nil {
			d["previous_for_s"] = *m.PreviousForS
		}
		if m.ChangedAt != "" {
			d["changed_at"] = m.ChangedAt
		}
		return "state", d
	case "interface":
		return "interface", map[string]any{"interface": m.Interface, "state": m.State, "ts": m.TS}
	case "firmware":
		// occulited task 44: a device's firmware update state changed, or its transfer moved
		d := map[string]any{"interface": m.Interface, "address": m.Address, "update_state": m.UpdateState, "ts": m.TS}
		if m.Transfer != nil {
			d["transfer"] = m.Transfer
		}
		return "firmware", d
	case "devices":
		typ := map[string]string{"new": "newDevices", "deleted": "deleteDevices", "updated": "updateDevice", "replaced": "replaceDevice", "readded": "readdedDevice"}[m.Op]
		if typ == "" {
			typ = "devices"
		}
		d := map[string]any{"interface": m.Interface, "addresses": m.Addresses, "ts": m.TS}
		// occulited task 5: what a callback server would have had from the call
		if m.Hint != nil {
			d["hint"] = *m.Hint
		}
		if m.Op == "replaced" {
			d["old"], d["new"] = m.Old, m.New
		}
		return typ, d
	}
	return m.Type, map[string]any{"ts": m.TS}
}

// Run serves one stream until ctx ends, the client stops reading, the credential goes, the
// switch goes off, or the page closes it. It returns why it ended, for the journal.
func (s *Service) Run(ctx context.Context, st *Stream, opt RunOptions, sink Sink) string {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	st.cancel = cancel
	s.mu.Unlock()
	defer cancel()
	defer s.release(st)
	if s.cfg.Sub == nil {
		sink.Close(CloseDisabled, "no subscriber")
		return "no subscriber"
	}
	ch, dropped, stop := s.cfg.Sub.Messages()
	defer stop()
	seq := s.cfg.Sub.Seq()
	// the trace (task 79): the stream's life and every delivery, one line per connection
	who := st.Transport + " " + st.Subject.Name + "@" + st.Remote
	trace := func(line string) {
		if s.tracing() {
			s.cfg.Trace.Line(line)
		}
	}
	trace("stream " + who + " connect filter=" + rpctrace.Value(st.Filter) + " last_event_id=" + rpctrace.Value(opt.LastEventID) + " devices=" + fmt.Sprint(opt.Devices))
	defer func() {
		s.mu.Lock()
		sent := st.Sent
		s.mu.Unlock()
		trace("stream " + who + " disconnect sent=" + fmt.Sprint(sent))
	}()
	send := func(id, typ string, data any) bool {
		if err := sink.Send(id, typ, data); err != nil {
			return false
		}
		s.mu.Lock()
		st.Sent++
		s.mu.Unlock()
		if s.tracing() && typ != "hello" {
			if d, ok := data.(map[string]any); ok && typ == "event" {
				trace("→ " + who + " event " + fmt.Sprint(d["interface"]) + " " + fmt.Sprint(d["address"]) + " " + fmt.Sprint(d["key"]) + " " + rpctrace.Value(d["value"]))
			} else {
				trace("→ " + who + " " + typ + " " + rpctrace.Value(data))
			}
		}
		return true
	}
	resync := func(reason string) bool {
		s.mu.Lock()
		st.LastResync = reason
		s.mu.Unlock()
		return send("", "resync", map[string]any{"reason": reason})
	}
	// hello: the process, where the ring is, the interfaces, the buffer's bounds
	hello := map[string]any{"boot_id": s.cfg.Sub.BootID(), "seq": seq, "interfaces": s.cfg.Sub.Status(), "buffer": map[string]any{"seconds": BufferSeconds, "events": BufferEvents}}
	if !send(s.eventID(seq), "hello", hello) {
		return "write failed"
	}
	// resume: what the ring still holds after the client's id, or resync
	last := uint64(0)
	if opt.LastEventID != "" {
		boot, since, ok := parseEventID(opt.LastEventID)
		switch {
		case !ok || boot != s.cfg.Sub.BootID():
			if !resync("boot") {
				return "write failed"
			}
		default:
			msgs, ok := s.cfg.Sub.Replay(since)
			if !ok {
				if !resync("gap") {
					return "write failed"
				}
			}
			for _, m := range msgs {
				typ, data := convert(m)
				last = m.Seq
				if !st.Filter.matches(typ, m) {
					continue
				}
				if !send(s.eventID(m.Seq), typ, data) {
					return "write failed"
				}
			}
		}
	}
	// ?devices=1: the device lists, one newDevices per interface, before the live events
	if opt.Devices {
		for _, i := range s.Interfaces() {
			if len(st.Filter.Interfaces) > 0 && !has(st.Filter.Interfaces, i.Name) {
				continue
			}
			v, err := s.Forward(ctx, i, Call{Method: "listDevices"})
			data := map[string]any{"interface": i.Name}
			if err != nil {
				data["error"] = err.Error()
			} else {
				data["devices"] = ToJSON(v)
			}
			if !send("", "newDevices", data) {
				return "write failed"
			}
		}
	}
	tick := time.NewTicker(s.cfg.Heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			reason := st.closed
			s.mu.Unlock()
			switch reason {
			case "disabled":
				sink.Close(CloseDisabled, "disabled")
			case "":
				if errors.Is(context.Cause(ctx), ErrStopping) {
					sink.Close(CloseGoingAway, "stopping")
					return "stopping"
				}
				sink.Close(CloseNormal, "")
				return "client gone"
			default:
				sink.Close(CloseNormal, reason)
			}
			return reason
		case m, ok := <-ch:
			if !ok {
				// the subscriber stopped: occulited is going down (B-14), not lite-rpc switched off
				sink.Close(CloseGoingAway, "stopping")
				return "shutdown"
			}
			if m.Seq <= last {
				continue // replayed already
			}
			typ, data := convert(m)
			if !st.Filter.matches(typ, m) {
				continue
			}
			if !send(s.eventID(m.Seq), typ, data) {
				return "write failed"
			}
		case <-tick.C:
			if d := dropped(); d > 0 {
				s.mu.Lock()
				st.Dropped = d
				s.mu.Unlock()
				resync("overflow")
				sink.Close(CloseOverflow, "overflow")
				return "overflow"
			}
			if opt.Valid != nil && !opt.Valid() {
				sink.Close(CloseUnauthorized, "unauthorized")
				return "unauthorized"
			}
			if err := sink.Ping(); err != nil {
				return "write failed"
			}
		}
	}
}
