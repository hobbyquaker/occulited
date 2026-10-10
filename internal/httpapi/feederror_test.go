package httpapi

import (
	"errors"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/httpwait"
)

// B-56: a feed host that did not answer in time is named in the 502's detail.
func TestFeedError(t *testing.T) {
	plain := feedError(errors.New("feed: HTTP 500"), "feed", 1)
	if plain["error"] != "feed-unreachable" || plain["feed"] != 1 || plain["detail"] != nil {
		t.Fatalf("%v", plain)
	}
	na := feedError(&httpwait.NoAnswerError{Host: "api.github.com", After: 45 * time.Second}, "releases", 2)
	d, _ := na["detail"].(map[string]any)
	if na["message"] != "api.github.com did not answer within 45s" || d["host"] != "api.github.com" || d["timeout"] != 45 || na["releases"] != 2 {
		t.Fatalf("%v", na)
	}
}
