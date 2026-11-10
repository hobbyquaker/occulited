package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/journalwatch"
)

func followLine(cursor, msg string) string {
	return fmt.Sprintf(`{"__CURSOR":%q,"__REALTIME_TIMESTAMP":"1794169197000000","PRIORITY":"6","SYSLOG_IDENTIFIER":"b140","MESSAGE":%q}`, cursor, msg) + "\n"
}

// B-140: the journal moves under the Log page's journalctl -f (a new machine ID, the switch to
// persistent); Follow starts it again after the last line emitted, and the stream goes on.
func TestFollowStartsAgainWhenTheJournalMoves(t *testing.T) {
	var mu sync.Mutex
	place := "id-a"
	pr, pw := io.Pipe()
	var calls []string
	j := JournalLog{
		Moves: journalwatch.Watch{Every: time.Millisecond, Where: func() string {
			mu.Lock()
			defer mu.Unlock()
			return place
		}},
		Stream: func(ctx context.Context, name string, args ...string) (io.ReadCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, name+" "+strings.Join(args, " "))
			if len(calls) == 1 {
				go func() { <-ctx.Done(); _ = pw.CloseWithError(ctx.Err()) }()
				return pr, nil
			}
			return io.NopCloser(strings.NewReader(followLine("s=3", "three"))), nil
		},
	}
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- j.Follow(context.Background(), LogQuery{Limit: 50, Unit: "rfd"}, func(l LogLine) {
			mu.Lock()
			got = append(got, l.Message)
			mu.Unlock()
		})
	}()
	for _, l := range []string{followLine("s=1", "one"), followLine("s=2", "two")} {
		if _, err := io.WriteString(pw, l); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	place = "id-b"
	mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the second journalctl ended cleanly: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Follow did not start journalctl again")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, " ") != "one two three" {
		t.Errorf("emitted %q", got)
	}
	want := []string{
		"journalctl -o json --no-pager -q -n 50 -u rfd.service -f",
		"journalctl -o json --no-pager -q -n 0 --after-cursor=s=2 -u rfd.service -f",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("journalctl runs\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

// A move before any line keeps the query as it was; a journalctl that cannot start is the error.
func TestFollowMovedBeforeAnyLine(t *testing.T) {
	var mu sync.Mutex
	n := 0
	j := JournalLog{
		Moves: journalwatch.Watch{Every: time.Millisecond, Where: func() string {
			mu.Lock()
			defer mu.Unlock()
			n++
			if n > 2 {
				return "b"
			}
			return "a"
		}},
	}
	var calls []string
	j.Stream = func(ctx context.Context, name string, args ...string) (io.ReadCloser, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(calls) == 1 {
			<-ctx.Done()
			return io.NopCloser(strings.NewReader("")), nil
		}
		return nil, errors.New("journalctl: not found")
	}
	err := j.Follow(context.Background(), LogQuery{Limit: -1}, func(LogLine) {})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err %v", err)
	}
	if len(calls) != 2 || calls[0] != calls[1] || !strings.Contains(calls[0], "-n 50") {
		t.Errorf("journalctl runs %q", calls)
	}
}

// The page's own cursor (after=) whose entry the flush copied comes once more from journalctl;
// the stream does not send it again.
func TestFollowSkipsTheFlushedCopyOfItsCursor(t *testing.T) {
	const (
		ram    = "s=aa;i=37;b=b0;m=2f1c0a3;t=6440d3c1a2b3c;x=9f8e"
		copied = "s=bb;i=901;b=b0;m=2f1c0a3;t=6440d3c1a2b3c;x=9f8e"
		next   = "s=bb;i=902;b=b0;m=2f1c0b0;t=6440d3c1a2b40;x=1234"
	)
	j := JournalLog{
		Moves: journalwatch.Watch{Where: func() string { return "a" }},
		Stream: func(ctx context.Context, name string, args ...string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(followLine(copied, "copy") + followLine(next, "next"))), nil
		},
	}
	var got []string
	if err := j.Follow(context.Background(), LogQuery{After: ram}, func(l LogLine) { got = append(got, l.Message) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "next" {
		t.Errorf("emitted %q", got)
	}
}
