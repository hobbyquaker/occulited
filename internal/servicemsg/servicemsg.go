// Package servicemsg keeps the box's service messages (task 75; since kept across a restart through
// the state store, task 194; confirmed over lite-rpc, task 82): which maintenance datapoints - UNREACH, LOWBAT, CONFIG_PENDING, UPDATE_PENDING, the
// ERROR_* codes, SABOTAGE, FAULT_REPORTING - are active on which device, and since when.
//
// Two sources feed one store. The sweep reads channel 0 of every device over the interface's
// own RPC (listDevices, then getParamset(<address>:0, VALUES)): at start, whenever the
// subscriber reports an interface up or restarted, and every 15 minutes as the safety net, because BidCos maintenance events are not measured yet (task 75, D-106). The
// events from the subscriber in between are what makes a message appear the moment a device
// drops. Which datapoints count is the CCU's own rule: the ones whose paramset description
// carries the service flag (FLAGS & 8), read once per device type and cached - so nothing here
// is a list to maintain, and a new device type with a new fault code is covered.
//
// The rules of a transition are task 82's, so its persistence can take the store as it is:
// inactive to active creates the entry with since = now; active to inactive removes it; the
// same active value again changes nothing; an active value that changes to another active
// value is a new message with a new since. What a sweep finds already active is "seen: start" -
// the real start is earlier; what an event made active is "seen: event".
//
// A sticky message (the description's FLAGS & 0x10: STICKY_UNREACH, STICKY_SABOTAGE) is the one
// kind no event clears: the interface process resets it when someone acknowledges it with
// setValue and tells nobody (occulited B-46, measured on rfd). So while one stands, its device's
// channel 0 is read again every minute, a few seconds after an event cleared another message of
// the device, and right after a write to that channel passed through occulited - the interface
// processes answer channel 0 from their cache, a read costs no airtime. What any read finds goes
// to Report as well, so the state store and the stream's clients learn what no event told them.
package servicemsg

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
)

// Message is one active service message.
type Message struct {
	Interface string    `json:"interface"`
	Address   string    `json:"address"` // the device
	Channel   string    `json:"channel"` // "0" for the maintenance channel
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	Since     time.Time `json:"since"`
	Seen      string    `json:"seen"` // event or start
	Type      string    `json:"type,omitempty"`

	// carried: a sticky message the interface process may have forgotten (B-322) - read back
	// from File, or its interface went down, came up or restarted since the process last said
	// true. A false does not end it; a confirmation through occulited does.
	carried bool
}

// Store is the in-memory store with its sweep.
type Store struct {
	// Interfaces gives the interface processes to sweep.
	Interfaces func() []interfaces.Interface
	// Timeout bounds one RPC call of the sweep.
	Timeout time.Duration
	// Every is the sweep interval; 15 minutes by default.
	Every time.Duration
	Log   *slog.Logger
	Now   func() time.Time
	// OnChange is told after every change of the active set, for the stream (may be nil).
	OnChange func()
	// Since is the state store's last change of a datapoint that holds value (openccu-lite task
	// 194: one clock for both, kept across a restart): a message the sweep finds already active
	// takes it as its since instead of the sweep's time, so a fault that stood for three weeks
	// does not read as "since the last restart". nil, or false: the sweep's time.
	Since func(iface, address, dp string, value any) (time.Time, bool)
	// Recheck is how often the channel 0 of a device with an active sticky message is read
	// again; a minute by default.
	Recheck time.Duration
	// RecheckAfter is how soon it is read after an event cleared another message of the device;
	// 5 seconds by default.
	RecheckAfter time.Duration
	// Report is told every channel 0 the store read, with the time its read began (may be nil):
	// the state store takes what differs from its own entries and puts it on the bus.
	Report func(iface, channel string, values map[string]any, at time.Time)
	// File keeps the active sticky messages across a restart (openccu-lite B-322); "" = in memory
	// only. Load reads it.
	File string

	saveMu   sync.Mutex // one write of File at a time
	saved    []byte     // what File holds
	mu       sync.Mutex
	active   map[string]Message // key: interface|address|channel:key
	types    map[string]string  // interface|address -> device type, from the sweep
	flags    map[string]map[string]bool
	sticky   map[string]map[string]bool // interface|type|channel -> the sticky datapoints
	due      map[string]time.Time       // interface|device -> when its channel 0 is read again
	evented  map[string]time.Time       // key -> when the last event for it came in (B-49)
	swept    time.Time
	sweepErr map[string]string
	// paired: interface -> the devices its last answered sweep listed, the CCU's own receivers
	// (RCV) left out (occulited task 89: the security-key warning asks whether BidCos-RF has any)
	paired   map[string]int
	sweeping bool
	wake     chan struct{}
	kick     chan struct{}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func key(iface, address, channel, dp string) string {
	return iface + "|" + address + "|" + channel + ":" + dp
}

// splitChannel divides "00010000000A10:0" into the device and the channel.
func splitChannel(addr string) (device, channel string) {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	return addr, ""
}

// Active says whether a datapoint value is a message: true, a number other than 0, a string
// other than empty. STICKY_UNREACH acknowledged is false; FAULT_REPORTING's no-fault is 0.
func Active(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != "" && x != "0" && !strings.EqualFold(x, "false")
	}
	return false
}

// knownServiceKeys are the maintenance datapoints every CCU user knows; the store counts them
// as service messages when it has no paramset description for the device type yet (an event
// before the first sweep), and the description decides afterwards.
var knownServiceKeys = map[string]bool{
	"UNREACH": true, "STICKY_UNREACH": true, "LOWBAT": true, "LOW_BAT": true, "CONFIG_PENDING": true, "UPDATE_PENDING": true,
	"SABOTAGE": true, "FAULT_REPORTING": true, "ERROR": true, "ERROR_CODE": true, "DUTY_CYCLE": true,
}

// isService says whether a datapoint of a device type counts, by the description's service flag
// when the type is known, by the well-known names when it is not.
func (s *Store) isService(iface, address, channel, dp string) bool {
	typ := s.types[iface+"|"+address]
	if f, ok := s.flags[iface+"|"+typ+"|"+channel]; ok && typ != "" {
		return f[dp]
	}
	return knownServiceKeys[dp] || strings.HasPrefix(dp, "ERROR_")
}

// isSticky says whether a datapoint keeps its value until it is acknowledged: by the
// description's sticky flag when the type is known, by its name when it is not.
func (s *Store) isSticky(iface, address, channel, dp string) bool {
	typ := s.types[iface+"|"+address]
	if f, ok := s.sticky[iface+"|"+typ+"|"+channel]; ok && typ != "" {
		return f[dp]
	}
	return strings.HasPrefix(dp, "STICKY_")
}

// hasSticky says whether a device has an active sticky message (the caller holds the lock).
func (s *Store) hasSticky(iface, device string) bool {
	for _, m := range s.active {
		if m.Interface == iface && m.Address == device && s.isSticky(iface, device, m.Channel, m.Key) {
			return true
		}
	}
	return false
}

func (s *Store) recheck() time.Duration {
	if s.Recheck > 0 {
		return s.Recheck
	}
	return time.Minute
}

func (s *Store) recheckAfter() time.Duration {
	if s.RecheckAfter > 0 {
		return s.RecheckAfter
	}
	return 5 * time.Second
}

// soon makes a device's channel 0 due at the given time unless it is due earlier already; it
// says whether the run loop's timer has to be set again (the caller holds the lock).
func (s *Store) soon(iface, device string, at time.Time) bool {
	k := iface + "|" + device
	if cur, ok := s.due[k]; ok && !cur.After(at) {
		return false
	}
	if s.due == nil {
		s.due = map[string]time.Time{}
	}
	s.due[k] = at
	return true
}

// plan gives every device with an active sticky message its next read, Recheck from now, unless
// it has one; the return value is soon's (the caller holds the lock).
func (s *Store) plan(now time.Time) bool {
	added := false
	for _, m := range s.active {
		if _, ok := s.due[m.Interface+"|"+m.Address]; ok || !s.isSticky(m.Interface, m.Address, m.Channel, m.Key) {
			continue
		}
		added = s.soon(m.Interface, m.Address, now.Add(s.recheck())) || added
	}
	return added
}

// nudge tells the run loop that the next read's time changed.
func (s *Store) nudge() {
	s.mu.Lock()
	k := s.kick
	s.mu.Unlock()
	if k != nil {
		select {
		case k <- struct{}{}:
		default:
		}
	}
}

// RecheckSoon reads one device's channel 0 again after a moment: a write to it passed through
// occulited (an acknowledgement over lite-rpc), and the interface process sends no event for it.
func (s *Store) RecheckSoon(iface, device string, after time.Duration) {
	s.mu.Lock()
	ok := s.soon(iface, device, s.now().Add(after))
	s.mu.Unlock()
	if ok {
		s.nudge()
	}
}

// apply is one transition; it says whether the active set changed.
func (s *Store) apply(iface, device, channel, dp string, value any, seen string, at time.Time) bool {
	k := key(iface, device, channel, dp)
	cur, has := s.active[k]
	switch {
	case !Active(value):
		if !has || cur.carried {
			return false // a carried sticky message's false is the process's restart (B-322)
		}
		delete(s.active, k)
		return true
	case !has:
		s.active[k] = Message{Interface: iface, Address: device, Channel: channel, Key: dp, Value: value, Since: at, Seen: seen, Type: s.types[iface+"|"+device]}
		return true
	case cur.Value == value:
		if cur.carried {
			cur.carried = false // the process knows it again: its acknowledgement ends it (B-46)
			s.active[k] = cur
		}
		return false
	default:
		cur.carried = false
		cur.Value, cur.Since, cur.Seen = value, at, seen // a new fault: a new message
		s.active[k] = cur
		return true
	}
}

// Event is the bus's: a datapoint change (rpcsub.Handler).
func (s *Store) Event(e rpcsub.Event) {
	device, channel := splitChannel(e.Address)
	now := s.now()
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]Message{}
	}
	service := s.isService(e.Interface, device, channel, e.Key)
	if service {
		// a read that began before this moment carries an older value than the event (B-49)
		if s.evented == nil {
			s.evented = map[string]time.Time{}
		}
		s.evented[key(e.Interface, device, channel, e.Key)] = now
	}
	changed := service && s.apply(e.Interface, device, channel, e.Key, e.Value, "event", e.Time)
	replan := false
	if changed {
		// UNREACH went while STICKY_UNREACH stands: the device answers again, and the
		// acknowledgement that follows will come without an event
		if !Active(e.Value) && s.hasSticky(e.Interface, device) {
			replan = s.soon(e.Interface, device, now.Add(s.recheckAfter()))
		}
		replan = s.plan(now) || replan
	}
	s.mu.Unlock()
	if changed {
		s.log().Info("service messages: changed by an event", "interface", e.Interface, "address", e.Address, "key", e.Key, "value", e.Value)
		s.changed()
	}
	if replan {
		s.nudge()
	}
}

// Interface is the bus's: an interface that came up or restarted is swept. Its sticky messages
// are carried from now on - a process that went down or restarted forgets them (B-322).
func (s *Store) Interface(name string, state string, _ time.Time) {
	if state == "up" || state == "restarted" || state == "down" {
		s.mu.Lock()
		s.carry(name)
		s.mu.Unlock()
	}
	if state == "up" || state == "restarted" {
		s.SweepSoon()
	}
}

// Devices is the bus's: a device list change is swept; a deleted device's messages go at once.
func (s *Store) Devices(iface, op string, addresses []string) {
	if op == "deleted" {
		s.mu.Lock()
		changed := s.drop(iface, addresses)
		s.mu.Unlock()
		if changed {
			s.changed()
		}
	}
	s.SweepSoon()
}

func (s *Store) changed() {
	s.persist()
	if s.OnChange != nil {
		s.OnChange()
	}
}

// SweepSoon asks the run loop for a sweep.
func (s *Store) SweepSoon() {
	s.mu.Lock()
	w := s.wake
	s.mu.Unlock()
	if w != nil {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

// Run sweeps at start, on request and every Every, and reads the devices that are due in
// between, until ctx ends.
func (s *Store) Run(ctx context.Context) {
	every := s.Every
	if every == 0 {
		every = 15 * time.Minute
	}
	s.mu.Lock()
	s.wake = make(chan struct{}, 1)
	s.kick = make(chan struct{}, 1)
	w, k := s.wake, s.kick
	s.mu.Unlock()
	t := time.NewTicker(every)
	defer t.Stop()
	s.Sweep(ctx)
	for {
		var due <-chan time.Time
		var timer *time.Timer
		if at, ok := s.nextDue(); ok {
			timer = time.NewTimer(max(at.Sub(s.now()), 0))
			due = timer.C
		}
		select {
		case <-ctx.Done():
		case <-t.C:
			s.Sweep(ctx)
		case <-w:
			// requests come in bursts (a restart's up, then the reconnect): one sweep per second
			time.Sleep(time.Second)
			for len(w) > 0 {
				<-w
			}
			s.Sweep(ctx)
		case <-k:
			// the next read's time changed: a new timer
		case <-due:
			s.readDue(ctx)
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// nextDue is the earliest read that is planned.
func (s *Store) nextDue() (at time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.due {
		if !ok || d.Before(at) {
			at, ok = d, true
		}
	}
	return at, ok
}

// readDue reads every device whose time has come.
func (s *Store) readDue(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	var list []string
	for k, at := range s.due {
		if !at.After(now) {
			list = append(list, k)
			delete(s.due, k)
		}
	}
	s.mu.Unlock()
	sort.Strings(list)
	for _, k := range list {
		if ctx.Err() != nil {
			return
		}
		iface, device, _ := strings.Cut(k, "|")
		s.read(ctx, iface, device)
	}
}

// read is the sweep for one device: its channel 0 is read and reconciled, by the sweep's rules.
// A read that fails keeps the entries; a sticky message that still stands is read again in Recheck.
func (s *Store) read(ctx context.Context, iface, device string) {
	if s.Interfaces == nil {
		return
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	channel := device + ":0"
	at := s.now()
	var vals map[string]any
	i, err := interfaces.Find(s.Interfaces(), iface)
	if err == nil {
		vals, err = interfaces.ChannelValues(ctx, i, channel, timeout)
	}
	var changes []string
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]Message{} // no sweep and no event has made it yet
	}
	if err == nil {
		seen := map[string]bool{}
		changes = s.take(iface, channel, vals, at, seen)
		for k, m := range s.active {
			if m.Interface == iface && m.Address == device && m.Channel == "0" && !seen[k] {
				delete(s.active, k)
				changes = append(changes, m.Key+" gone")
			}
		}
	}
	replan := s.plan(s.now())
	s.mu.Unlock()
	if replan {
		s.nudge()
	}
	if err != nil {
		s.log().Debug("service messages: a device's channel 0 could not be read again", "interface", iface, "address", channel, "err", err)
		return
	}
	if s.Report != nil {
		s.Report(iface, channel, vals, at)
	}
	if len(changes) > 0 {
		sort.Strings(changes)
		s.log().Info("service messages: changed by a read", "interface", iface, "address", channel, "changes", strings.Join(changes, " "))
		s.changed()
	}
}

// take reconciles the store with one channel's VALUES as a read found them (the caller holds the
// lock): active and stored stays with its since, active and new is created as seen at start,
// stored and no longer active goes. It marks the service datapoints the answer carried in seen
// and returns what changed, for the log. now is when the read began: a datapoint an event
// reported since then keeps what the event said, as the read's answer is older (B-49).
func (s *Store) take(iface, addr string, vals map[string]any, now time.Time, seen map[string]bool) (changes []string) {
	device, channel := splitChannel(addr)
	for dp, v := range vals {
		if !s.isService(iface, device, channel, dp) {
			continue
		}
		k := key(iface, device, channel, dp)
		seen[k] = true
		if ev, ok := s.evented[k]; ok {
			if !ev.Before(now) {
				continue // an event came in while the read was on its way
			}
			delete(s.evented, k) // the read is newer than the event
		}
		at := now
		if _, has := s.active[k]; !has && s.Since != nil {
			// new to the store: the state store may know since when the value stands
			if t, ok := s.Since(iface, addr, dp, v); ok && t.Before(now) {
				at = t
			}
		}
		if s.apply(iface, device, channel, dp, v, "start", at) {
			changes = append(changes, fmt.Sprintf("%s=%v", dp, v))
		}
	}
	return changes
}

// Sweep reads every device's maintenance channel and reconciles the store with it: active and
// stored stays with its since; active and new is created as seen at start; stored and no longer
// active goes. An interface that does not answer keeps its entries - a daemon that is down is
// not a device that recovered.
func (s *Store) Sweep(ctx context.Context) {
	if s.Interfaces == nil {
		return
	}
	s.mu.Lock()
	if s.sweeping {
		s.mu.Unlock()
		return
	}
	s.sweeping = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.sweeping = false
		s.mu.Unlock()
	}()
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ifs := s.Interfaces()
	now := s.now()
	errs := map[string]string{}
	changed := false
	for _, i := range ifs {
		if ctx.Err() != nil {
			return
		}
		devs, flags, sticky, values, err := interfaces.MaintenanceValues(ctx, i, timeout)
		if err != nil {
			errs[i.Name] = err.Error()
			continue
		}
		s.mu.Lock()
		if s.active == nil {
			s.active = map[string]Message{}
		}
		if s.types == nil {
			s.types = map[string]string{}
		}
		if s.flags == nil {
			s.flags = map[string]map[string]bool{}
		}
		if s.sticky == nil {
			s.sticky = map[string]map[string]bool{}
		}
		seen := map[string]bool{}
		listed := map[string]bool{}
		n := 0
		for _, d := range devs {
			s.types[i.Name+"|"+d.Address] = d.Type
			listed[d.Address] = true
			if !strings.Contains(strings.ToUpper(d.Type), "RCV") {
				n++
			}
		}
		if s.paired == nil {
			s.paired = map[string]int{}
		}
		s.paired[i.Name] = n
		for typ, f := range flags {
			s.flags[i.Name+"|"+typ+"|0"] = f
		}
		for typ, f := range sticky {
			s.sticky[i.Name+"|"+typ+"|0"] = f
		}
		for addr, vals := range values {
			if len(s.take(i.Name, addr, vals, now, seen)) > 0 {
				changed = true
			}
		}
		// stored, and this interface's sweep did not find it active: gone - but a carried sticky
		// message of a device that is listed and whose channel 0 did not answer stays (B-322)
		for k, m := range s.active {
			if m.Interface != i.Name || seen[k] {
				continue
			}
			if m.carried && listed[m.Address] && values[m.Address+":"+m.Channel] == nil {
				continue
			}
			delete(s.active, k)
			changed = true
		}
		s.mu.Unlock()
		// what the sweep read reaches the state store too: an event that never came cannot pin
		// its entry (B-46)
		if s.Report != nil {
			for addr, vals := range values {
				s.Report(i.Name, addr, vals, now)
			}
		}
	}
	s.mu.Lock()
	s.swept, s.sweepErr = now, errs
	n := len(s.active)
	replan := s.plan(s.now())
	s.mu.Unlock()
	s.log().Debug("service messages: swept", "active", n, "errors", len(errs))
	if changed {
		s.changed()
	}
	if replan {
		s.nudge()
	}
}

// Paired is how many devices the interface's last answered sweep listed, the CCU's own receivers
// (RCV) left out; false before a sweep the interface answered (occulited task 89). A device paired
// or deleted since is counted at the sweep its newDevices or deleteDevices starts.
func (s *Store) Paired(iface string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.paired[iface]
	return n, ok
}

// Snapshot is what the API shows.
type Snapshot struct {
	Count    int               `json:"count"`
	Messages []Message         `json:"messages"`
	Swept    time.Time         `json:"swept"`
	Errors   map[string]string `json:"errors"`
}

// List is the active messages, the oldest first.
func (s *Store) List() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{Messages: []Message{}, Swept: s.swept, Errors: map[string]string{}}
	for _, m := range s.active {
		out.Messages = append(out.Messages, m)
	}
	for k, v := range s.sweepErr {
		out.Errors[k] = v
	}
	sort.Slice(out.Messages, func(a, b int) bool {
		if !out.Messages[a].Since.Equal(out.Messages[b].Since) {
			return out.Messages[a].Since.Before(out.Messages[b].Since)
		}
		return out.Messages[a].Address+out.Messages[a].Key < out.Messages[b].Address+out.Messages[b].Key
	})
	out.Count = len(out.Messages)
	return out
}
