package auth

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Display names (occulited task 73): Control ("Bedienung") does not rename devices. A user may
// give a channel a name of their own instead, shown only in Control and only to that user; the
// metadata store keeps the name every other view and app sees. They are kept with the account in
// users.json, beside its preferences, keyed by the metadata object's id
// (`<interface>.<address>:<channel>`). A stale key - a channel that is gone - does no harm: Control
// shows only the objects the store has.

// ErrBadDisplayName is a display name or key the store refuses; the message says why.
var ErrBadDisplayName = errors.New("invalid display name")

const (
	// maxDisplayNames bounds the map: users.json is read on every login.
	maxDisplayNames = 2000
	// maxDisplayNameLen is the longest name in characters, the metadata store's own bound.
	maxDisplayNameLen = 200
	// maxDisplayKeyLen is the longest object id.
	maxDisplayKeyLen = 200
)

// cleanDisplayKey checks an object id: not empty, no space or control character, not too long.
func cleanDisplayKey(id string) error {
	if id == "" || len(id) > maxDisplayKeyLen || !utf8.ValidString(id) {
		return fmt.Errorf("%w: object id %q", ErrBadDisplayName, id)
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' {
			return fmt.Errorf("%w: object id %q", ErrBadDisplayName, id)
		}
	}
	return nil
}

// cleanDisplayName trims a name and checks it: no control character, not too long. "" clears.
func cleanDisplayName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if !utf8.ValidString(n) || utf8.RuneCountInString(n) > maxDisplayNameLen {
		return "", fmt.Errorf("%w: at most %d characters", ErrBadDisplayName, maxDisplayNameLen)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: control character in the name", ErrBadDisplayName)
		}
	}
	return n, nil
}

// DisplayNames returns a copy of an account's display names (never nil) and whether the account
// exists. A name that is no account - the anonymous administrator, a token - gets the empty map.
func (s *Store) DisplayNames(name string) (map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	out := map[string]string{}
	u, ok := s.users[name]
	if !ok {
		return out, false
	}
	for k, v := range u.DisplayNames {
		out[k] = v
	}
	return out, true
}

// SetDisplayName sets one object's display name for an account; an empty (or blank) name removes
// it. ErrUnknownUser for a name that is no account, ErrBadDisplayName for a key or name the store
// will not keep, or one more than the map holds.
func (s *Store) SetDisplayName(name, id, display string) error {
	if err := cleanDisplayKey(id); err != nil {
		return err
	}
	n, err := cleanDisplayName(display)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reload()
	u, ok := s.users[name]
	if !ok {
		return ErrUnknownUser
	}
	if n == "" {
		if _, had := u.DisplayNames[id]; !had {
			return nil
		}
		delete(u.DisplayNames, id)
		if len(u.DisplayNames) == 0 {
			u.DisplayNames = nil // nothing to keep: the account reads as before
		}
		return s.save()
	}
	if u.DisplayNames[id] == n {
		return nil
	}
	if _, had := u.DisplayNames[id]; !had && len(u.DisplayNames) >= maxDisplayNames {
		return fmt.Errorf("%w: at most %d display names", ErrBadDisplayName, maxDisplayNames)
	}
	if u.DisplayNames == nil {
		u.DisplayNames = map[string]string{}
	}
	u.DisplayNames[id] = n
	return s.save()
}
