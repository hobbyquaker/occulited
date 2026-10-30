package servicemsg

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/atomicfile"
)

// A sticky message stays until it is confirmed, across a restart of occulited, of the system and
// of the interface process (openccu-lite B-322, the maintainer's decision of 2026-10-14), as a
// CCU's service message does. rfd does not keep STICKY_UNREACH across its own restart: after a
// reboot it answers false for a device that was away an hour before, and the sweep took that
// for an acknowledgement and dropped the message.
//
// So the store keeps every active sticky message in a small file of its own (File), and a
// message the interface process may have forgotten is "carried": every one read back from the
// file at the start, and every one of an interface that went down, came up or restarted. A
// carried message is not ended by a read or an event that says false - that false is the
// process's restart, not an acknowledgement. It ends when it is confirmed through occulited
// (Acknowledge: a setValue STICKY_*=false over lite-rpc, the Status page's Confirm), when its
// device is deleted or no longer listed, or when its datapoint is gone from the device's
// channel 0. A read that finds it true again (the device went away once more) makes it an
// ordinary message, which the process's own acknowledgement ends as before (B-46).

// FileName is File's name in occulited's state directory.
const FileName = "service-messages.json"

// fileVersion is the file's format.
const fileVersion = 1

type stickyFile struct {
	Version  int       `json:"version"`
	Messages []Message `json:"messages"`
}

// Load reads the sticky messages kept in File into the store, each carried; a missing file is
// an empty one. Called before Run.
func (s *Store) Load() error {
	if s.File == "" {
		return nil
	}
	b, err := os.ReadFile(s.File)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f stickyFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = map[string]Message{}
	}
	for _, m := range f.Messages {
		if m.Interface == "" || m.Address == "" || m.Key == "" || !Active(m.Value) {
			continue
		}
		if m.Channel == "" {
			m.Channel = "0"
		}
		m.carried = true
		s.active[key(m.Interface, m.Address, m.Channel, m.Key)] = m
	}
	s.saved = b
	return nil
}

// persist writes the active sticky messages to File when they differ from what it holds. A
// failed write is logged and tried again at the next change.
func (s *Store) persist() {
	if s.File == "" {
		return
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	f := stickyFile{Version: fileVersion, Messages: []Message{}}
	for _, m := range s.active {
		if m.carried || s.isSticky(m.Interface, m.Address, m.Channel, m.Key) {
			f.Messages = append(f.Messages, m)
		}
	}
	saved := s.saved
	s.mu.Unlock()
	sort.Slice(f.Messages, func(a, b int) bool {
		x, y := f.Messages[a], f.Messages[b]
		return key(x.Interface, x.Address, x.Channel, x.Key) < key(y.Interface, y.Address, y.Channel, y.Key)
	})
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if saved == nil && len(f.Messages) == 0 {
		return // no file and nothing to keep: no write
	}
	if bytes.Equal(b, saved) {
		return
	}
	if err := writeAtomic(s.File, b); err != nil {
		s.log().Warn("service messages: the sticky messages could not be kept", "file", s.File, "err", err)
		return
	}
	s.mu.Lock()
	s.saved = b
	s.mu.Unlock()
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// carry marks an interface's sticky messages carried: the process went down, came up or
// restarted, and may answer false for each of them now (the caller holds the lock).
func (s *Store) carry(iface string) {
	for k, m := range s.active {
		if m.Interface == iface && !m.carried && s.isSticky(m.Interface, m.Address, m.Channel, m.Key) {
			m.carried = true
			s.active[k] = m
		}
	}
}

// Acknowledge is a confirmation that passed through occulited: a write of false to the sticky
// datapoints keys of a device's channel 0 that the interface process took (lite-rpc's Wrote).
// Their messages end at once, carried or not; a read that began before this moment does not
// bring them back (B-49).
func (s *Store) Acknowledge(iface, device string, keys []string) {
	now := s.now()
	changed := false
	s.mu.Lock()
	for _, dp := range keys {
		k := key(iface, device, "0", dp)
		m, ok := s.active[k]
		if !ok || !(m.carried || s.isSticky(iface, device, "0", dp)) {
			continue
		}
		delete(s.active, k)
		if s.evented == nil {
			s.evented = map[string]time.Time{}
		}
		s.evented[k] = now
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.log().Info("service messages: a sticky message was confirmed", "interface", iface, "address", device, "keys", strings.Join(keys, ","))
		s.changed()
	}
}

// drop removes every message of the devices named (device or channel addresses) of an
// interface: they were deleted (the caller holds the lock).
func (s *Store) drop(iface string, addresses []string) bool {
	gone := map[string]bool{}
	for _, a := range addresses {
		d, _ := splitChannel(a)
		gone[d] = true
	}
	changed := false
	for k, m := range s.active {
		if m.Interface == iface && gone[m.Address] {
			delete(s.active, k)
			changed = true
		}
	}
	return changed
}
