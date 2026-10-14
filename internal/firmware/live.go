package firmware

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

// The live half of the device firmware (occulited task 44, with task 41 folded in): the paired
// devices' FIRMWARE, AVAILABLE_FIRMWARE and FIRMWARE_UPDATE_STATE are read again from the
// interfaces while something is going on - a transfer, an install - and the transfer's progress
// is followed in hmipserver's journal, so GET /firmware says what is happening now and not what
// the last daily check saw, and the frontends get one firmware message per change instead of
// polling the interfaces themselves.
//
// The poll rule: a device in DO_UPDATE_PENDING or PERFORMING_UPDATE (an install, seconds to a
// few minutes) is read every LiveFast; a device in NEW_FIRMWARE_AVAILABLE or
// DELIVER_FIRMWARE_IMAGE (a transfer, hours) every LiveSlow; READY_FOR_UPDATE alone is never
// polled - it lasts until the user installs - and nothing else is either. Outside the timer a
// read is asked for by the bus (a devices change, an UPDATE_PENDING event) and by the journal
// (a transfer started, continued or aborted), debounced by liveDebounce.

const (
	LiveFast = 5 * time.Second
	LiveSlow = 60 * time.Second
	// liveFastFor is how long after an install was seen the fast rate holds, so the new FIRMWARE
	// is read soon after the device comes back
	liveFastFor  = 3 * time.Minute
	liveDebounce = 2 * time.Second
	// NotifyEvery bounds the firmware messages per device: at most one per this long, the last
	// change always sent (a running transfer's percent moves every few minutes anyway)
	NotifyEvery = 10 * time.Second
	// transferKeep is how long a journal transfer entry is kept without the interface saying the
	// device is in a transfer state (the live state not read yet, a device gone)
	transferKeep = 48 * time.Hour
	// JournalUnit is hmipserver's unit, whose journal carries the transfer lines
	JournalUnit = "hmipserver.service"
	// journalReplay is how far back the follower reads at its start, so a restart of occulited
	// keeps the percent a transfer stood at
	journalReplay = 48 * time.Hour
)

// StreamRunner starts a command whose output is read while it runs (system.ExecStream's shape).
type StreamRunner func(ctx context.Context, name string, args ...string) (io.ReadCloser, error)

// Change is one device's firmware state as the notifier sends it.
type Change struct {
	Interface   string
	Address     string
	UpdateState string
	Transfer    *Transfer
}

func (s *Service) liveInit() {
	if s.refresh == nil {
		s.refresh = make(chan string, 16)
	}
}

// Refresh asks for a live read of the named interfaces soon (all of them for none).
func (s *Service) Refresh(ifaces ...string) {
	s.mu.Lock()
	s.liveInit()
	ch := s.refresh
	s.mu.Unlock()
	if len(ifaces) == 0 {
		ifaces = []string{""}
	}
	for _, i := range ifaces {
		select {
		case ch <- i:
		default:
		}
	}
}

// InstallSeen notes that an install may be running (an UPDATE_PENDING or DEVICE_IN_BOOTLOADER
// event): the fast rate holds for a while and a read is asked for.
func (s *Service) InstallSeen(iface string) {
	s.mu.Lock()
	s.fastUntil = s.now().Add(liveFastFor)
	s.mu.Unlock()
	s.Refresh(iface)
}

// Event is the bus handler's event: an UPDATE_PENDING or DEVICE_IN_BOOTLOADER on a maintenance
// channel is an install under way (the states are then read at the fast rate).
func (s *Service) Event(iface, address, key string) {
	if key == "UPDATE_PENDING" || key == "DEVICE_IN_BOOTLOADER" {
		s.InstallSeen(iface)
	}
}

// Devices is the bus handler's device-list change: the interface is read again.
func (s *Service) Devices(iface, op string) {
	s.Refresh(iface)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// liveWait is how long until the next timed read: 0 for none (caller holds mu).
func (s *Service) liveWait() time.Duration {
	fast, slow := false, false
	now := s.now()
	if s.fastUntil.After(now) {
		fast = true
	}
	for _, d := range s.state.Devices {
		switch d.UpdateState {
		case "DO_UPDATE_PENDING", "PERFORMING_UPDATE", "LIVE_DELIVER_FIRMWARE_IMAGE":
			fast = true
		case "NEW_FIRMWARE_AVAILABLE", "DELIVER_FIRMWARE_IMAGE":
			slow = true
		}
	}
	switch {
	case fast:
		return s.liveFast()
	case slow:
		return s.liveSlow()
	}
	return 0
}

func (s *Service) liveFast() time.Duration {
	if s.LiveFast > 0 {
		return s.LiveFast
	}
	return LiveFast
}

func (s *Service) liveSlow() time.Duration {
	if s.LiveSlow > 0 {
		return s.LiveSlow
	}
	return LiveSlow
}

// RunLive reads the interfaces' states on the poll rule until ctx ends. Call it in a goroutine
// beside Run.
func (s *Service) RunLive(ctx context.Context) {
	s.mu.Lock()
	s.liveInit()
	ch := s.refresh
	s.mu.Unlock()
	var pending map[string]bool
	var debounce <-chan time.Time
	for {
		s.mu.Lock()
		wait := s.liveWait()
		s.mu.Unlock()
		var timer <-chan time.Time
		var t *time.Timer
		if wait > 0 {
			t = time.NewTimer(wait)
			timer = t.C
		}
		select {
		case <-ctx.Done():
			if t != nil {
				t.Stop()
			}
			return
		case <-timer:
			s.ReadLive(ctx, nil)
		case name := <-ch:
			if t != nil {
				t.Stop()
			}
			if pending == nil {
				pending = map[string]bool{}
			}
			pending[name] = true
			if debounce == nil {
				debounce = time.After(liveDebounce)
			}
			continue
		case <-debounce:
			if t != nil {
				t.Stop()
			}
			var names []string
			if !pending[""] {
				for n := range pending {
					names = append(names, n)
				}
			}
			pending, debounce = nil, nil
			s.ReadLive(ctx, names)
		}
	}
}

// ReadLive reads the named interfaces (all for none) and folds what they say into the state:
// each device's firmware, available firmware, update state and updatable flag; devices paired
// since the last check are added, deleted ones removed. Changes go to the notifier.
func (s *Service) ReadLive(ctx context.Context, names []string) {
	var ifs []interfaces.Interface
	for _, i := range s.Interfaces() {
		if len(names) == 0 || has(names, i.Name) {
			ifs = append(ifs, i)
		}
	}
	if len(ifs) == 0 {
		return
	}
	devs, errs := interfaces.Devices(ctx, ifs, 20*time.Second)
	read := map[string]bool{}
	for _, i := range ifs {
		if errs[i.Name] == nil {
			read[i.Name] = true
		}
	}
	for name, err := range errs {
		s.Log.Debug("firmware: live read failed", "interface", name, "err", err)
	}
	if len(read) == 0 {
		return
	}
	now := s.now()
	s.mu.Lock()
	changes := s.fold(devs, read, now)
	s.state.LiveAt = &now
	if len(changes) > 0 {
		s.save()
	}
	s.mu.Unlock()
	s.notify(changes)
}

// fold merges a live listing into the state's devices (caller holds mu) and returns the devices
// whose state or firmware changed.
func (s *Service) fold(devs []interfaces.Device, read map[string]bool, now time.Time) []Change {
	latestByType := map[string]string{} // type key -> eQ-3's version, from the devices the check saw
	notListed := map[string]bool{}
	for _, d := range s.state.Devices {
		if d.Latest != "" {
			latestByType[TypeKey(d.Type)] = d.Latest
		}
		if d.NotListed {
			notListed[TypeKey(d.Type)] = true
		}
	}
	var out []DeviceStatus
	var changes []Change
	byKey := map[string]DeviceStatus{}
	for _, d := range s.state.Devices {
		byKey[d.Interface+"."+d.Address] = d
	}
	for _, d := range devs {
		if !read[d.Interface] {
			continue
		}
		k := d.Interface + "." + d.Address
		cur, had := byKey[k]
		next := cur
		if !had {
			next = DeviceStatus{Device: d}
			next.Latest = latestByType[TypeKey(d.Type)]
			next.NotListed = notListed[TypeKey(d.Type)]
		}
		next.Device = d
		if had {
			next.Latest, next.NotListed = cur.Latest, cur.NotListed
		}
		next.UpdateAvailable = d.AvailableFirmware != "" && CompareVersions(d.AvailableFirmware, d.Firmware) > 0
		if d.Updatable != nil && !*d.Updatable {
			next.UpdateAvailable, next.Latest, next.NotListed = false, "", false
		} else if next.Latest != "" && Newer(next.Latest, d.Firmware) {
			next.UpdateAvailable = true
		}
		if !had || cur.UpdateState != next.UpdateState || cur.Firmware != next.Firmware || cur.AvailableFirmware != next.AvailableFirmware {
			changes = append(changes, Change{Interface: d.Interface, Address: d.Address, UpdateState: next.UpdateState})
		}
		out = append(out, next)
	}
	for _, d := range s.state.Devices {
		// the devices of the interfaces not read stay; a device an interface read no longer lists
		// is dropped without a message - the bus's deleteDevices said so already
		if !read[d.Interface] {
			out = append(out, d)
		}
	}
	sortDevices(out)
	s.state.Devices = out
	s.pruneTransfers(now)
	for i := range changes {
		changes[i].Transfer = s.transferFor(changes[i].Interface, changes[i].Address, now)
	}
	return changes
}

// transferFor is the journal's entry for a device, shown while the interface says the device is
// in a transfer state, or while no live state was read yet (caller holds mu).
func (s *Service) transferFor(iface, address string, now time.Time) *Transfer {
	if iface != "HmIP-RF" {
		return nil
	}
	tr, ok := s.transfers[address]
	if !ok {
		return nil
	}
	for _, d := range s.state.Devices {
		if d.Interface == iface && d.Address == address {
			if d.UpdateState != "" && !transferState(d.UpdateState) {
				return nil
			}
			break
		}
	}
	if now.Sub(tr.Updated) > transferKeep {
		return nil
	}
	t := tr
	return &t
}

// pruneTransfers drops journal entries the interface has moved past (caller holds mu).
func (s *Service) pruneTransfers(now time.Time) {
	for a, tr := range s.transfers {
		if now.Sub(tr.Updated) > transferKeep {
			delete(s.transfers, a)
			continue
		}
		for _, d := range s.state.Devices {
			if d.Interface == "HmIP-RF" && d.Address == a && d.UpdateState != "" && !transferState(d.UpdateState) {
				delete(s.transfers, a)
			}
		}
	}
}

func sortDevices(devs []DeviceStatus) {
	for i := 1; i < len(devs); i++ {
		for j := i; j > 0 && less(devs[j], devs[j-1]); j-- {
			devs[j], devs[j-1] = devs[j-1], devs[j]
		}
	}
}

func less(a, b DeviceStatus) bool {
	if a.Interface != b.Interface {
		return a.Interface < b.Interface
	}
	return a.Address < b.Address
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---- the journal ----

// RunJournal follows hmipserver's journal for the transfer lines until ctx ends, while HmIP-RF
// is among the interfaces; without Journal (no journal on the system) it returns at once. A
// follower that ends is started again after a pause.
func (s *Service) RunJournal(ctx context.Context) {
	if s.Journal == nil {
		return
	}
	for {
		if !s.hasInterface("HmIP-RF") {
			if !sleep(ctx, time.Minute) {
				return
			}
			continue
		}
		since := s.journalSince()
		err := s.followJournal(ctx, since)
		if ctx.Err() != nil {
			return
		}
		pause := 30 * time.Second
		if err != nil {
			var exitErr *exec.Error
			if errors.As(err, &exitErr) {
				// no journalctl: a development machine - try again rarely
				pause = 10 * time.Minute
			}
			s.Log.Debug("firmware: journal follower ended", "err", err)
		}
		if !sleep(ctx, pause) {
			return
		}
	}
}

func (s *Service) hasInterface(name string) bool {
	for _, i := range s.Interfaces() {
		if i.Name == name {
			return true
		}
	}
	return false
}

// journalSince is where the follower starts: the newest transfer entry it knows, else the replay
// window - so a restart of occulited keeps a transfer's percent and sees nothing twice.
func (s *Service) journalSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := s.now().Add(-journalReplay)
	for _, tr := range s.transfers {
		if tr.Updated.After(since) {
			since = tr.Updated
		}
	}
	return since
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// followJournal runs journalctl -f on the unit from since and feeds every line to Line.
func (s *Service) followJournal(ctx context.Context, since time.Time) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	unit := s.JournalUnitName
	if unit == "" {
		unit = JournalUnit
	}
	out, err := s.Journal(ctx, "journalctl", "-o", "json", "--no-pager", "-q", "-u", unit, "-f", "--since", "@"+strconv.FormatInt(since.Unix()+1, 10))
	if err != nil {
		return err
	}
	defer out.Close()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		at, msg, ok := journalEntry(sc.Bytes(), s.now())
		if !ok {
			continue
		}
		s.Line(at, msg)
	}
	return sc.Err()
}

// journalEntry reads one `journalctl -o json` line: its time and MESSAGE (a message journald
// stored as bytes - an array - is skipped).
func journalEntry(b []byte, fallback time.Time) (at time.Time, msg string, ok bool) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return time.Time{}, "", false
	}
	msg, ok = m["MESSAGE"].(string)
	if !ok || msg == "" {
		return time.Time{}, "", false
	}
	at = fallback
	if ts, isStr := m["__REALTIME_TIMESTAMP"].(string); isStr {
		if us, err := strconv.ParseInt(ts, 10, 64); err == nil {
			at = time.UnixMicro(us)
		}
	}
	return at, msg, true
}

// Line feeds one journal line of hmipserver to the transfer tracker; lines that are not the
// transfer's are ignored. A start, continuation or abort asks for a live read too, so the
// interface's state follows.
func (s *Service) Line(at time.Time, msg string) {
	ev, ok := ParseTransferLine(msg)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.transfers == nil {
		s.transfers = Transfers{}
	}
	changedAddrs := s.transfers.Apply(ev, at)
	var changes []Change
	for _, a := range changedAddrs {
		c := Change{Interface: "HmIP-RF", Address: a, Transfer: s.transferFor("HmIP-RF", a, at)}
		for _, d := range s.state.Devices {
			if d.Interface == "HmIP-RF" && d.Address == a {
				c.UpdateState = d.UpdateState
			}
		}
		if c.Transfer != nil {
			changes = append(changes, c)
		}
	}
	if len(changedAddrs) > 0 {
		s.save()
	}
	s.mu.Unlock()
	s.notify(changes)
	if ev.Kind == "started" || ev.Kind == "continues" || ev.Kind == "aborted" {
		s.Refresh("HmIP-RF")
	}
}

// ---- the notifier ----

// notify hands the changes to Changed, at most one per device per NotifyEvery: a change inside
// the window is held and sent when it ends, the newest winning.
func (s *Service) notify(changes []Change) {
	if s.Changed == nil || len(changes) == 0 {
		return
	}
	s.mu.Lock()
	if s.notifyLast == nil {
		s.notifyLast = map[string]time.Time{}
		s.notifyHeld = map[string]Change{}
	}
	now := s.now()
	var send []Change
	for _, c := range changes {
		k := c.Interface + "." + c.Address
		if last, ok := s.notifyLast[k]; ok && now.Sub(last) < s.notifyEvery() {
			if _, held := s.notifyHeld[k]; !held {
				time.AfterFunc(s.notifyEvery()-now.Sub(last), func() { s.flush(k) })
			}
			s.notifyHeld[k] = c
			continue
		}
		s.notifyLast[k] = now
		send = append(send, c)
	}
	s.mu.Unlock()
	for _, c := range send {
		s.Changed(c)
	}
}

func (s *Service) flush(k string) {
	s.mu.Lock()
	c, ok := s.notifyHeld[k]
	delete(s.notifyHeld, k)
	if ok {
		s.notifyLast[k] = s.now()
	}
	s.mu.Unlock()
	if ok && s.Changed != nil {
		s.Changed(c)
	}
}

func (s *Service) notifyEvery() time.Duration {
	if s.NotifyEvery > 0 {
		return s.NotifyEvery
	}
	return NotifyEvery
}
