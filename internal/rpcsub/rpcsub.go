// Package rpcsub is the box's own XML-RPC event subscriber (task 75): the inner half of what a
// bridge does. It registers one loopback listener at every interface process that speaks XML-RPC
// - rfd, hmipserver, hs485d where it runs, the VirtualDevices process, and any custom entry of
// InterfacesList.xml - answers their callbacks at once, and puts what arrives on a bus inside
// occulited: the service messages, the carrier sense and duty cycle the HmIP module reports, and
// "when did this interface last say anything".
//
// It runs inside occulited (D-115: one process, lite-rpc on the same 8183 as everything lighttpd
// proxies), with the daemons' callbacks on a second loopback socket that lighttpd never proxies -
// on 8183 the callback path would be reachable from the LAN through lighttpd, whose requests
// arrive from 127.0.0.1 too, and a LAN client could inject events. What the measurement of
// 2026-09-12 taught shapes it (task 75, *Measured*):
//
//   - a slow subscriber blocks rfd's whole RPC server for the length of its init, and the
//     VirtualDevices process delivers serially across subscribers - so every callback here is
//     answered before anything else happens, and the work is done behind a queue;
//   - the daemons send ISO-8859-1, which go-hmccu's handler decodes; a wrong answer to listDevices
//     made hmipserver drop the registration, and made the VirtualDevices init take 10 s;
//   - hmipserver and VirtualDevices keep a dead subscriber's entry, so the callback URL and the id
//     are fixed: a restart, or a hard end and a new start, overwrites its own entry instead of
//     piling up;
//   - after an hmipserver restart a plain init(url, "") no longer removed the restored entry; a
//     fresh init followed by the removal did, so an interface whose daemon called us again on its
//     own is deregistered that way;
//   - ping/PONG works on all three and is a broadcast to every subscriber, whose id the PONG's
//     value carries, so every other bridge's ping counts as life here too.
//
// Nothing here is authenticated: the listener binds the loopback, and what it answers is what a
// callback client answers - nothing that reads or changes the box.
package rpcsub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpctrace"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// DefaultListen is the loopback address the callback listener binds. 8184 sits beside
// occulited's 8183 (task 182): nothing on a CCU used either, and the 2121/2122 that task 77's
// notes proposed are CCU-Jack's. The number stands only in the daemons' handlers files.
const DefaultListen = "127.0.0.1:8184"

// DefaultInterfaces is the interface list the process watches.
const DefaultInterfaces = "/etc/config/InterfacesList.xml"

// IDPrefix is what this process's registrations are called in the daemons' handlers files:
// occulited_<interface>. The Interfaces page marks these as the system's own (D-80) by it.
const IDPrefix = "occulited_"

// CallbackPath is the path under the listener every interface gets: /cb/<interface>.
const CallbackPath = "/cb/"

// Config is what New takes; the zero value of everything but Log is the default.
type Config struct {
	Listen     string
	Interfaces string
	// PingAfter is the silence after which an interface is pinged; PingTimeout how long the
	// answer may take before the registration is made afresh.
	PingAfter   time.Duration
	PingTimeout time.Duration
	// InitTimeout bounds one init, ping or deregistration call.
	InitTimeout time.Duration
	// InitTimeouts bounds the registering init of single interfaces instead (openccu-lite B-270);
	// nil = DefaultInitTimeouts. Pings and deregistrations keep InitTimeout.
	InitTimeouts map[string]time.Duration
	// Taken says whether the daemon's handlers file holds the registration id -> callback of that
	// interface, written at or after since (B-270): an init that got no answer within its bound but
	// was taken counts as registered. nil = never asked, every unanswered init is a failure.
	Taken func(iface, id, callback string, since time.Time) bool
	// InitGrace is how long after an init the daemon's listDevices and listMethods still count as
	// part of it: hmipserver's init returns before it calls back (measured, 4-18 ms and then the
	// calls), so a call in this window is the registration, not a restart.
	InitGrace time.Duration
	// Watch is how often the interface list file is looked at.
	Watch time.Duration
	Log   *slog.Logger
	Now   func() time.Time
	// Trace takes the daemons' calls to the listener as lines (task 79); nil = no trace.
	Trace rpctrace.Tracing
	// Enrich sees every message as it is published, stamped and in order, before the ring and
	// the readers get it (openccu-lite task 194: the state store is fed here, and adds the
	// datapoint's last change to the event). It runs under the bus's lock: quick, and never a
	// call back into the subscriber.
	Enrich func(m *Message, at time.Time)
}

// DefaultInitTimeouts are the interfaces whose init may take longer than InitTimeout (B-270):
// hmipserver answers the VirtualDevices init only once its BackendUpdateDevicesCommand has
// answered, which took 10 s on a system without HmIP radio - a 10 s bound counted every such init
// as failed and tried again, and each try held hmipserver's event loop for as long. rfd and
// HmIP-RF keep the short bound: the stall detection (B-201) relies on it.
var DefaultInitTimeouts = map[string]time.Duration{"VirtualDevices": 30 * time.Second}

// Message is what goes over the bus: the bus's ring and the remote stream carry it as one JSON
// object; the in-process handlers get it typed.
type Message struct {
	Type string `json:"type"` // hello, event, state, interface, devices, resync
	Seq  uint64 `json:"seq,omitempty"`
	TS   string `json:"ts,omitempty"`
	// event: one (interface, address, key, value); batch is the seq of the first event of the
	// system.multicall it came in, on every event of that call, so a reader can rebuild it
	Interface string `json:"interface,omitempty"`
	Address   string `json:"address,omitempty"`
	Key       string `json:"key,omitempty"`
	Value     any    `json:"value,omitempty"`
	Batch     uint64 `json:"batch,omitempty"`
	// event of a datapoint the state store keeps (task 194), and state: the entry's last change
	// and how long the value before it stood
	LC           string   `json:"lc,omitempty"`
	PreviousForS *float64 `json:"previous_for_s,omitempty"`
	// state: the store's entry after its sweep confirmed or changed it (the datapoint in Key,
	// the time of the sweep's read in TS)
	Confirmed *bool  `json:"confirmed,omitempty"`
	Source    string `json:"source,omitempty"`
	// interface: state up, down, restarted, added, removed
	State string `json:"state,omitempty"`
	// devices: op new, deleted, updated, replaced, readded, with the addresses
	Op        string   `json:"op,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	// devices, op updated: updateDevice's hint as the interface sent it (0 unspecified, 1 the
	// links changed, 2 the description changed); a pointer, so a 0 still goes out (occulited task 5)
	Hint *int `json:"hint,omitempty"`
	// devices, op replaced: the replaced device and its successor (Addresses holds both, in order)
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
	// hello: the boot id, the seq the ring is at, and every interface's status (the stream's
	// first message); resync: reason gap or boot
	BootID     string        `json:"boot_id,omitempty"`
	Interfaces []IfaceStatus `json:"interfaces,omitempty"`
	Reason     string        `json:"reason,omitempty"`
}

// Datapoint is a state message's datapoint (it rides in Key).
func (m Message) Datapoint() string { return m.Key }

// IfaceStatus is one interface as the process sees it.
type IfaceStatus struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// State: up (registered, the daemon answered), down (the daemon does not answer), silent
	// (registered, but nothing arrived since PingAfter and a ping is out)
	State string `json:"state"`
	// Registered says whether the last init succeeded and was not undone.
	Registered bool `json:"registered"`
	// LastActivity is the last time the daemon called the listener for this interface, whatever
	// it called - an event, a PONG for somebody else, a listDevices at its start.
	LastActivity string `json:"last_activity,omitempty"`
	LastInit     string `json:"last_init,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	Events       uint64 `json:"events"`
	// Telegrams counts the calls that carried a device's events (occulited task 13): a
	// system.multicall is one, however many events it holds, and so is a single event call; a
	// ping's PONG is none. The radio sampler makes the Status page's telegrams/s of it.
	Telegrams uint64 `json:"telegrams"`
	Calls     uint64 `json:"calls"`
	// Restored: the daemon called us on its own after our init - its restart kept the entry
	Restored bool `json:"restored,omitempty"`
	// Stalled is when the daemon was found stuck (openccu-lite B-201), absent while it is not:
	// StallKind "delivery" - it answers our calls, but a registration made afresh brought no
	// callback, so it delivers nothing to anybody (hmipserver held by a listener that does not
	// answer) - or "calls" - its XML-RPC calls do not answer at all (rfd held the same way).
	Stalled   string `json:"stalled,omitempty"`
	StallKind string `json:"stall_kind,omitempty"`
}

// The kinds of stall.
const (
	StallDelivery = "delivery"
	StallCalls    = "calls"
)

// Stall is one interface found stuck, for the check that looks for the listener behind it.
type Stall struct {
	Interface string
	URL       string
	Kind      string
	Since     time.Time
}

// Subscriber is the process's state.
type Subscriber struct {
	cfg    Config
	log    *slog.Logger
	bootID string

	mu       sync.Mutex
	ifaces   map[string]*iface
	bus      *bus
	handlers []*reader
	now      func() time.Time
	// base is the listener's address once bound - the callback URLs are made from it, so a
	// test's port 0 works and the daemons get the port the kernel gave
	base string
}

type iface struct {
	name, rawURL string
	caller       xmlrpc.Caller
	callback     string // http://127.0.0.1:<port>/cb/<name>
	id           string

	registered bool
	restored   bool
	// reinit: the daemon restarted and kept the entry; the next tick makes a fresh init, since
	// hmipserver delivers no events to an entry it restored from its handlers file (B-286)
	reinit     bool
	inInit     bool
	state      string
	lastAct    time.Time
	lastInit   time.Time
	lastErr    string
	events     uint64
	telegrams  uint64
	calls      uint64
	pingSent   time.Time
	pingOK     bool      // the last ping call was answered (whatever came of it)
	lastCB     time.Time // the daemon's last call to the listener - lastAct without our own inits
	verifyFrom time.Time // re-registered after an unanswered but accepted ping: a callback must follow
	stallKind  string
	stallSince time.Time
	failures   int
	nextTry    time.Time
	handler    *xmlrpc.Handler
	lateSaid   bool // the "took it but answered late" line was written (once per interface and run)
}

// New makes a subscriber; Run starts it.
func New(cfg Config) *Subscriber {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Interfaces == "" {
		cfg.Interfaces = DefaultInterfaces
	}
	if cfg.PingAfter == 0 {
		cfg.PingAfter = 5 * time.Minute
	}
	if cfg.PingTimeout == 0 {
		cfg.PingTimeout = 30 * time.Second
	}
	if cfg.InitTimeout == 0 {
		cfg.InitTimeout = 10 * time.Second
	}
	if cfg.InitTimeouts == nil {
		cfg.InitTimeouts = DefaultInitTimeouts
	}
	if cfg.Watch == 0 {
		cfg.Watch = 10 * time.Second
	}
	if cfg.InitGrace == 0 {
		cfg.InitGrace = 30 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	b := newBus(cfg.Now)
	b.enrich = cfg.Enrich
	return &Subscriber{cfg: cfg, log: cfg.Log, bootID: hex.EncodeToString(id[:]), ifaces: map[string]*iface{}, bus: b, now: cfg.Now}
}

// BootID identifies this process's life: a reader that sees another one resyncs.
func (s *Subscriber) BootID() string { return s.bootID }

// Base is the listener's address once Run bound it (http://127.0.0.1:<port>), for tests.
func (s *Subscriber) Base() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base
}

// Run serves the callback listener, registers at every interface and keeps the registrations
// alive until ctx ends, then deregisters and closes the handlers' queues.
func (s *Subscriber) Run(ctx context.Context) error {
	srv, port, err := s.listen()
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(s.cfg.Listen)
	s.mu.Lock()
	s.base = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	s.mu.Unlock()
	s.log.Info("rpc: callback listener up", "callbacks", s.cfg.Listen, "boot_id", s.bootID)
	done := make(chan struct{})
	go func() { defer close(done); s.watch(ctx) }()
	<-ctx.Done()
	s.deregisterAll()
	srv.Close()
	<-done
	s.mu.Lock()
	hs := s.handlers
	s.handlers = nil
	s.mu.Unlock()
	for _, r := range hs {
		s.bus.detach(r)
		close(r.ch)
	}
	return nil
}

// watch reads the interface list at start and whenever its file changes, and runs the
// registration loop every few seconds.
func (s *Subscriber) watch(ctx context.Context) {
	var lastMod time.Time
	var lastSize int64
	t := time.NewTicker(s.cfg.Watch)
	defer t.Stop()
	for {
		st, err := os.Stat(s.cfg.Interfaces)
		if err == nil && (!st.ModTime().Equal(lastMod) || st.Size() != lastSize) {
			lastMod, lastSize = st.ModTime(), st.Size()
			s.reload()
		} else if err != nil && lastMod.IsZero() {
			s.log.Warn("rpc: the interface list is not readable yet", "file", s.cfg.Interfaces, "err", err)
			lastMod = time.Unix(1, 0) // said once
		}
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Entry is one interface of the list file.
type Entry struct{ Name, URL, Info string }

// ParseInterfaces reads InterfacesList.xml.
func ParseInterfaces(b []byte) ([]Entry, error) {
	var doc struct {
		IPC []struct {
			Name string `xml:"name"`
			URL  string `xml:"url"`
			Info string `xml:"info"`
		} `xml:"ipc"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range doc.IPC {
		n, u := strings.TrimSpace(e.Name), strings.TrimSpace(e.URL)
		if n != "" && u != "" {
			out = append(out, Entry{Name: n, URL: u, Info: strings.TrimSpace(e.Info)})
		}
	}
	return out, nil
}

// Speaks says whether an entry is one this process subscribes to (D-70): every xmlrpc:// or
// http:// entry, and BidCos-RF and BidCos-Wired, which are listed as xmlrpc_bin:// but answer
// XML-RPC on the same port (measured for rfd). Any other xmlrpc_bin:// entry - CUxD - is BIN-RPC
// only and is left alone.
func Speaks(e Entry) bool {
	scheme, _, ok := strings.Cut(e.URL, "://")
	if !ok {
		return false
	}
	switch scheme {
	case "xmlrpc", "http", "https":
		return true
	case "xmlrpc_bin":
		return e.Name == "BidCos-RF" || e.Name == "BidCos-Wired"
	}
	return false
}

// callerAddr is go-hmccu's Addr for an entry: host[:port][/path], the scheme dropped.
func callerAddr(raw string) (string, error) {
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return "", fmt.Errorf("not a URL: %q", raw)
	}
	if _, err := url.Parse("http://" + rest); err != nil {
		return "", err
	}
	return rest, nil
}

func (s *Subscriber) reload() {
	b, err := os.ReadFile(s.cfg.Interfaces)
	if err != nil {
		s.log.Warn("rpc: the interface list could not be read", "file", s.cfg.Interfaces, "err", err)
		return
	}
	entries, err := ParseInterfaces(b)
	if err != nil {
		s.log.Warn("rpc: the interface list could not be parsed", "file", s.cfg.Interfaces, "err", err)
		return
	}
	s.Set(entries)
}

// Set replaces the interface set: new ones are added and registered at the next tick, gone
// ones deregistered and dropped. Tests call it in place of the file.
func (s *Subscriber) Set(entries []Entry) {
	want := map[string]Entry{}
	for _, e := range entries {
		if Speaks(e) {
			want[e.Name] = e
		}
	}
	s.mu.Lock()
	var gone []*iface
	for name, i := range s.ifaces {
		if e, ok := want[name]; !ok || e.URL != i.rawURL {
			gone = append(gone, i)
			delete(s.ifaces, name)
		}
	}
	var added []string
	for name, e := range want {
		if _, ok := s.ifaces[name]; ok {
			continue
		}
		addr, err := callerAddr(e.URL)
		if err != nil {
			s.log.Warn("rpc: interface skipped", "interface", name, "url", e.URL, "err", err)
			continue
		}
		i := &iface{name: name, rawURL: e.URL, caller: &xmlrpc.Client{Addr: addr}, id: IDPrefix + name, state: "down"}
		base := s.base
		if base == "" {
			base = "http://" + s.cfg.Listen
		}
		i.callback = base + CallbackPath + url.PathEscape(name)
		i.handler = s.handlerFor(i)
		s.ifaces[name] = i
		added = append(added, name)
	}
	s.mu.Unlock()
	for _, i := range gone {
		s.log.Info("rpc: interface gone from the list", "interface", i.name)
		s.deregister(i)
		s.bus.publish(Message{Type: "interface", Interface: i.name, State: "removed"})
	}
	sort.Strings(added)
	for _, n := range added {
		s.log.Info("rpc: interface added", "interface", n)
		s.bus.publish(Message{Type: "interface", Interface: n, State: "added"})
	}
}

// tick is the registration and liveness loop, once per Watch.
func (s *Subscriber) tick(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	var todo []*iface
	for _, i := range s.ifaces {
		todo = append(todo, i)
	}
	s.mu.Unlock()
	sort.Slice(todo, func(a, b int) bool { return todo[a].name < todo[b].name })
	for _, i := range todo {
		if ctx.Err() != nil {
			return
		}
		s.checkDelivery(i, now)
		s.mu.Lock()
		registered, next, act, ping, reinit := i.registered, i.nextTry, i.lastAct, i.pingSent, i.reinit
		s.mu.Unlock()
		switch {
		case reinit:
			// the daemon restarted and kept our entry: a fresh init, because a kept entry is not
			// delivered to (openccu-lite B-286, measured on hmipserver: five minutes of nothing on
			// the kept entry, events again within seconds of a fresh init)
			s.register(i)
		case !registered:
			if now.Before(next) {
				continue
			}
			s.register(i)
		case !ping.IsZero() && now.Sub(ping) > s.cfg.PingTimeout && act.Before(ping):
			// the ping went unanswered: the daemon forgot us, or it is gone - register afresh,
			// and the failure path below says which
			s.log.Warn("rpc: no answer to the ping, registering again", "interface", i.name, "silent_for", now.Sub(act).Round(time.Second))
			s.mu.Lock()
			i.pingSent = time.Time{}
			i.registered = false
			if i.pingOK {
				// the daemon took the ping and nothing came back: it forgot us (a restart), or it
				// delivers to nobody (B-201). A fresh registration tells the two apart - a daemon
				// that forgot us calls back at once, a stuck one does not
				i.verifyFrom = now
			}
			s.mu.Unlock()
			s.register(i)
		case ping.IsZero() && !act.IsZero() && now.Sub(act) > s.cfg.PingAfter:
			s.mu.Lock()
			i.pingSent = now
			i.pingOK = false
			i.state = "silent"
			s.mu.Unlock()
			go s.ping(i)
		case !ping.IsZero() && act.After(ping):
			// answered, by our PONG or anything else that came in
			s.mu.Lock()
			i.pingSent = time.Time{}
			if i.state == "silent" {
				i.state = "up"
			}
			s.mu.Unlock()
		}
	}
}

// checkDelivery ends the wait after a re-registration that followed an accepted but unanswered
// ping: a callback since then clears it, none within PingTimeout is a delivery stall (B-201).
func (s *Subscriber) checkDelivery(i *iface, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i.verifyFrom.IsZero() || !i.registered || now.Sub(i.verifyFrom) <= s.cfg.PingTimeout {
		return
	}
	from := i.verifyFrom
	i.verifyFrom = time.Time{}
	if i.lastCB.After(from) {
		return // it had forgotten us, and the new registration works
	}
	if i.stallKind != StallDelivery {
		i.stallKind, i.stallSince = StallDelivery, from
		s.log.Warn("rpc: the daemon answers but delivers nothing, not even to a registration made afresh - a callback listener that does not answer may hold it", "interface", i.name)
	}
}

// markCall notes how a call to the daemon went: a call that got no answer at all is a stall of
// its calls (B-201), any answer ends one.
func (s *Subscriber) markCall(i *iface, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		if i.stallKind == StallCalls {
			i.stallKind, i.stallSince = "", time.Time{}
		}
	case errors.Is(err, errNoAnswer) && i.stallKind == "":
		i.stallKind, i.stallSince = StallCalls, s.now()
	}
}

// errNoAnswer is a call that got no answer within InitTimeout.
var errNoAnswer = errors.New("no answer")

func (s *Subscriber) call(i *iface, method string, params ...*xmlrpc.Value) (*xmlrpc.Value, error) {
	return s.callWithin(i, s.cfg.InitTimeout, method, params...)
}

// initTimeout is the bound of an interface's registering init.
func (s *Subscriber) initTimeout(name string) time.Duration {
	if d, ok := s.cfg.InitTimeouts[name]; ok && d > 0 {
		return d
	}
	return s.cfg.InitTimeout
}

func (s *Subscriber) callWithin(i *iface, limit time.Duration, method string, params ...*xmlrpc.Value) (*xmlrpc.Value, error) {
	type res struct {
		v   *xmlrpc.Value
		err error
	}
	ch := make(chan res, 1)
	go func() {
		v, err := i.caller.Call(method, params)
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(limit):
		return nil, fmt.Errorf("%w within %s", errNoAnswer, limit)
	}
}

// register is one init(url, id). The daemon calls back during it (rfd: listMethods and
// listDevices before it returns), which the handler answers from another goroutine.
//
// An init that got no answer within its bound, while the daemon's handlers file shows it took the
// registration (written since the init began), counts as registered (B-270): trying again would
// only make the daemon do the slow part again, and it is no stall of its calls. The ping watchdog
// judges its liveness from then on as for any registration.
func (s *Subscriber) register(i *iface) {
	s.mu.Lock()
	i.inInit = true
	was := i.state
	s.mu.Unlock()
	start := s.now()
	limit := s.initTimeout(i.name)
	_, err := s.callWithin(i, limit, "init", xmlrpc.NewString(i.callback), xmlrpc.NewString(i.id))
	late := false
	if errors.Is(err, errNoAnswer) && s.cfg.Taken != nil && s.cfg.Taken(i.name, i.id, i.callback, start) {
		late, err = true, nil
	}
	s.markCall(i, err)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	i.inInit = false
	if late && !i.lateSaid {
		i.lateSaid = true
		s.log.Info("rpc: the daemon took the registration but answered its init late", "interface", i.name, "bound", limit)
	}
	if err != nil {
		i.failures++
		i.lastErr = err.Error()
		i.registered = false
		i.reinit = false // the retry path takes over
		// 5 s, 10, 20, 40, 80, then every 2 min: a daemon that is down at boot is tried again
		// soon, one that is gone for good does not fill the journal
		delay := 5 * time.Second << min(i.failures-1, 4)
		if delay > 2*time.Minute {
			delay = 2 * time.Minute
		}
		i.nextTry = now.Add(delay)
		i.state = "down"
		// said once per outage: the first failure, or the fall from up - the retries every two
		// minutes of a daemon that is switched off do not fill the journal or the readers
		if was != "down" || i.failures == 1 {
			s.log.Warn("rpc: interface down", "interface", i.name, "err", err, "retry_in", delay)
			s.bus.publish(Message{Type: "interface", Interface: i.name, State: "down"})
		}
		return
	}
	i.registered = true
	i.restored = false
	i.reinit = false
	i.failures = 0
	i.lastErr = ""
	i.lastInit = now
	i.lastAct = now
	i.pingSent = time.Time{}
	i.state = "up"
	s.log.Info("rpc: registered", "interface", i.name, "callback", i.callback, "id", i.id)
	state := "up"
	if was == "up" || was == "silent" {
		state = "restarted" // we had it and lost it: from the reader's view the daemon came back
	}
	s.bus.publish(Message{Type: "interface", Interface: i.name, State: state})
}

func (s *Subscriber) ping(i *iface) {
	_, err := s.call(i, "ping", xmlrpc.NewString(i.id))
	s.markCall(i, err)
	if err != nil {
		s.log.Debug("rpc: ping failed", "interface", i.name, "err", err)
		return
	}
	s.mu.Lock()
	i.pingOK = true
	s.mu.Unlock()
}

// deregister takes this process's entry out of the daemon's list. After a daemon restart that
// kept the entry, the plain removal did nothing (measured on hmipserver); a fresh init first
// makes it take.
func (s *Subscriber) deregister(i *iface) {
	s.mu.Lock()
	registered, restored := i.registered, i.restored
	i.registered = false
	s.mu.Unlock()
	if !registered {
		return
	}
	if restored {
		if _, err := s.call(i, "init", xmlrpc.NewString(i.callback), xmlrpc.NewString(i.id)); err != nil {
			s.log.Debug("rpc: the re-init before the removal failed", "interface", i.name, "err", err)
		}
	}
	if _, err := s.call(i, "init", xmlrpc.NewString(i.callback), xmlrpc.NewString("")); err != nil {
		if connRefused(err) {
			// task 34: the daemon is gone - at a shutdown it stops before occulited - and its list
			// with it; there is nothing to take out (openccu-lite task 345, row 11)
			s.log.Debug("rpc: not deregistered, the daemon is not running", "interface", i.name)
			return
		}
		s.log.Warn("rpc: deregistration failed", "interface", i.name, "err", err)
		return
	}
	s.log.Info("rpc: deregistered", "interface", i.name)
}

// connRefused: the daemon's port is closed. The XML-RPC client puts the dial error into its message
// rather than wrapping it, so the text is the fallback.
func connRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused")
}

func (s *Subscriber) deregisterAll() {
	s.mu.Lock()
	var all []*iface
	for _, i := range s.ifaces {
		all = append(all, i)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, i := range all {
		wg.Add(1)
		go func(i *iface) { defer wg.Done(); s.deregister(i) }(i)
	}
	wg.Wait()
}

// Status is every interface's state, sorted by name.
func (s *Subscriber) Status() []IfaceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Subscriber) statusLocked() []IfaceStatus {
	out := []IfaceStatus{}
	for _, i := range s.ifaces {
		st := IfaceStatus{Name: i.name, URL: i.rawURL, State: i.state, Registered: i.registered, LastError: i.lastErr, Events: i.events, Telegrams: i.telegrams, Calls: i.calls, Restored: i.restored}
		if i.stallKind != "" {
			st.Stalled, st.StallKind = i.stallSince.UTC().Format(time.RFC3339), i.stallKind
		}
		if !i.lastAct.IsZero() {
			st.LastActivity = i.lastAct.UTC().Format(time.RFC3339Nano)
		}
		if !i.lastInit.IsZero() {
			st.LastInit = i.lastInit.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Stalls are the interfaces found stuck right now (B-201), sorted by name.
func (s *Subscriber) Stalls() []Stall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Stall
	for _, i := range s.ifaces {
		if i.stallKind != "" {
			out = append(out, Stall{Interface: i.name, URL: i.rawURL, Kind: i.stallKind, Since: i.stallSince})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Interface < out[b].Interface })
	return out
}

// View is what the pages get from GET /radio/health and GET /service-messages: the subscriber
// as occulited's own part, which is why Connected is always true while occulited runs - the
// field stays for the day the daemons' callbacks are watched from outside.
type View struct {
	Connected  bool          `json:"connected"`
	BootID     string        `json:"boot_id"`
	Received   uint64        `json:"received"`
	Interfaces []IfaceStatus `json:"interfaces"`
}

// View is the subscriber's state for the API.
func (s *Subscriber) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	return View{Connected: true, BootID: s.bootID, Received: s.bus.next, Interfaces: s.statusLocked()}
}
