package meta

import (
	"errors"
	"testing"
)

// occulited B-66: a node's name by its path, for the service messages' rooms.
func TestNodeName(t *testing.T) {
	s, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateNode(nil, "room", nil, "garage", "Garage", "", nil); err != nil {
		t.Fatal(err)
	}
	parent := "room/garage"
	if _, _, err := s.CreateNode(nil, "room", &parent, "shelf", "Regal", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, want string
		code       Code
	}{
		{"room/garage", "Garage", ""},
		{"room/garage/shelf", "Regal", ""},
		{"room/cellar", "", ErrUnknownPath},
		{"room", "", ErrUnknownPath},
		{"nothing/garage", "", ErrUnknownPath},
	} {
		got, err := s.NodeName(c.path)
		var e *Error
		switch {
		case c.code == "" && (err != nil || got != c.want):
			t.Errorf("%s: %q, %v", c.path, got, err)
		case c.code != "" && (!errors.As(err, &e) || e.Code != c.code):
			t.Errorf("%s: %v, want %s", c.path, err, c.code)
		}
	}
}
