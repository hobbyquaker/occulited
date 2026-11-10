package journalwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWhere(t *testing.T) {
	root := t.TempDir()
	if got := Where(root); got != "- - -" {
		t.Errorf("an empty root: %q", got)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "etc"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "var/log/journal"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "run/log/journal"), 0o755))
	must(os.WriteFile(filepath.Join(root, "etc/machine-id"), []byte("3cf25f523e0540018131248aa45d3c52\n"), 0o644))
	a := Where(root)
	f := strings.Fields(a)
	if len(f) != 3 || f[0] != "3cf25f523e0540018131248aa45d3c52" || f[1] == "-" || f[2] == "-" || f[1] == f[2] {
		t.Fatalf("place %q", a)
	}
	if Where(root) != a {
		t.Error("the same place twice is not the same")
	}

	// another machine ID
	must(os.WriteFile(filepath.Join(root, "etc/machine-id"), []byte("b869f6269af7444f83ebc3abe4cff04d\n"), 0o644))
	b := Where(root)
	if b == a || strings.Fields(b)[1] != f[1] {
		t.Errorf("a new ID: %q after %q", b, a)
	}

	// another directory at /var/log/journal (what a mount over it is to a stat): made while the
	// old one exists, so its inode cannot be the old one's
	must(os.MkdirAll(filepath.Join(root, "var/log/other"), 0o755))
	must(os.Rename(filepath.Join(root, "var/log/journal"), filepath.Join(root, "var/log/old")))
	must(os.Rename(filepath.Join(root, "var/log/other"), filepath.Join(root, "var/log/journal")))
	c := Where(root)
	if strings.Fields(c)[1] == strings.Fields(b)[1] || strings.Fields(c)[2] != strings.Fields(b)[2] {
		t.Errorf("a new /var/log/journal: %q after %q", c, b)
	}

	// /run/log/journal gone, an empty machine-id
	must(os.Remove(filepath.Join(root, "run/log/journal")))
	must(os.WriteFile(filepath.Join(root, "etc/machine-id"), nil, 0o644))
	if d := strings.Fields(Where(root)); d[0] != "-" || d[2] != "-" {
		t.Errorf("place %q", d)
	}
	if Where("") == "" {
		t.Error("the system's place is never empty")
	}
}

// places hands out a fixed sequence of places, then the last one for good, and counts the calls.
type places struct {
	mu    sync.Mutex
	seq   []string
	calls int
}

func (p *places) where() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if len(p.seq) > 1 {
		s := p.seq[0]
		p.seq = p.seq[1:]
		return s
	}
	return p.seq[0]
}

func (p *places) n() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestRunEndsTheFollowerWhenTheJournalMoves(t *testing.T) {
	p := &places{seq: []string{"a", "a", "a", "b"}}
	w := Watch{Every: time.Millisecond, Where: p.where}
	moved, err := w.Run(context.Background(), func(ctx context.Context) error {
		<-ctx.Done()
		return errors.New("journalctl ended")
	})
	if !moved || err == nil {
		t.Errorf("moved %v, err %v", moved, err)
	}
}

func TestRunWaitsForTheNewPlaceToStay(t *testing.T) {
	// a place that differs once and is back at the next look is no move
	p := &places{seq: []string{"a", "b", "a", "c", "a", "a"}}
	w := Watch{Every: time.Millisecond, Where: p.where}
	moved, err := w.Run(context.Background(), func(ctx context.Context) error {
		for p.n() < 30 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		return nil
	})
	if moved || err != nil {
		t.Errorf("moved %v, err %v", moved, err)
	}
}

func TestRunFollowerEndsByItself(t *testing.T) {
	w := Watch{Where: func() string { return "a" }}
	want := errors.New("exit status 1")
	moved, err := w.Run(context.Background(), func(context.Context) error { return want })
	if moved || !errors.Is(err, want) {
		t.Errorf("moved %v, err %v", moved, err)
	}
	// the defaults: the system's place, every 5 s
	moved, err = Watch{}.Run(context.Background(), func(context.Context) error { return nil })
	if moved || err != nil {
		t.Errorf("moved %v, err %v", moved, err)
	}
}

func TestRunStoppedIsNoMove(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &places{seq: []string{"a", "b"}}
	w := Watch{Every: time.Millisecond, Where: p.where}
	moved, _ := w.Run(ctx, func(fctx context.Context) error {
		<-fctx.Done() // the move ends it
		cancel()      // and the caller stops at the same time
		return nil
	})
	if moved {
		t.Error("a stopped caller does not start again")
	}
}

func TestSameEntry(t *testing.T) {
	ram := "s=5f485d0c41ea467a84fbf682e5a4c4fd;i=37;b=0c9e3a2a1f6b4b5e8d2c7a1b3c4d5e6f;m=2f1c0a3;t=6440d3c1a2b3c;x=9f8e7d6c5b4a3921"
	disk := "s=03b67d4ae3cb4db2b732e068848862d5;i=10f9c41;b=0c9e3a2a1f6b4b5e8d2c7a1b3c4d5e6f;m=2f1c0a3;t=6440d3c1a2b3c;x=9f8e7d6c5b4a3921"
	other := "s=03b67d4ae3cb4db2b732e068848862d5;i=10f9c42;b=0c9e3a2a1f6b4b5e8d2c7a1b3c4d5e6f;m=2f1c0a3;t=6440d3c1a2b3c;x=1111111111111111"
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{ram, disk, true},             // the entry flushed to /var/log/journal
		{ram, ram, true},              // the same file
		{disk, other, false},          // another entry of the same microsecond
		{"s=1;i=2", "s=1;i=2", false}, // without the entry's own fields nothing is known
		{"", disk, false},
		{"garbage", "garbage", false},
	} {
		if got := SameEntry(c.a, c.b); got != c.want {
			t.Errorf("SameEntry(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
