// Package journalwatch tells a long-running `journalctl -f` that the journal moved under it
// (occulited B-140).
//
// journalctl -f reads the journal directories it finds when it starts: everything below
// /run/log/journal, and below /var/log/journal only the directory of the machine ID of that
// moment, read once from /etc/machine-id. When the journal moves while it runs it stays alive and
// prints nothing more:
//
//   - the machine ID changes (openccu-lite's lite-machine-id on the boot that derives it, or
//     anything else that restarts journald on another ID): journald writes a directory below
//     /var/log/journal that this journalctl never looks at, while the files it holds stay open
//     and silent - it keeps every descriptor, so counting its open journal files does not show it;
//   - a directory is mounted over /var/log/journal after it started (the switch from RAM to
//     persistent: lite-journal-persist binds the userfs there and journald flushes onto it): the
//     journalctl watches the directory underneath, which nobody writes again.
//
// Where is a fingerprint of exactly those three things - the machine ID and the identity
// (device and inode) of the two journal roots - so a change of it is a change journalctl does not
// follow by itself. Run polls it next to a follower and ends the follower when it changes; the
// caller starts journalctl again from the last entry it handled. Polling every few seconds costs
// two stat calls and one small read; inotify would not see a mount, and journalctl --merge, which
// reads every machine ID's directory, would read foreign IDs' files and still miss the mount.
package journalwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultEvery is how often Run looks at the journal's place.
const DefaultEvery = 5 * time.Second

// Where is the journal's place as journalctl -f fixes it at its start, below root ("" or "/" for
// the system): "<machine id> <dev:ino of /var/log/journal> <dev:ino of /run/log/journal>", a
// missing part as "-".
func Where(root string) string {
	if root == "" {
		root = "/"
	}
	id := "-"
	if b, err := os.ReadFile(filepath.Join(root, "etc/machine-id")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			id = s
		}
	}
	return id + " " + ident(filepath.Join(root, "var/log/journal")) + " " + ident(filepath.Join(root, "run/log/journal"))
}

func ident(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "?"
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}

// Watch is how Run looks: every Every (0 = DefaultEvery), at Where (nil = the system's place).
type Watch struct {
	Every time.Duration
	Where func() string
}

func (w Watch) where() string {
	if w.Where != nil {
		return w.Where()
	}
	return Where("/")
}

// Run calls follow with a context that ends when the journal moves, and returns follow's error
// and whether that was the reason it ended (never when ctx ended). The place is taken before
// follow starts, so a move between the two is seen too (a needless restart at worst). A move
// counts once the new place has been seen twice in a row - a journald restart and the files
// moved after it (lite-machine-id) are over by then, and a journalctl started afterwards finds
// them where they stay.
func (w Watch) Run(ctx context.Context, follow func(ctx context.Context) error) (moved bool, err error) {
	start := w.where()
	every := w.Every
	if every <= 0 {
		every = DefaultEvery
	}
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var hit atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		pending := ""
		for {
			select {
			case <-fctx.Done():
				return
			case <-t.C:
			}
			now := w.where()
			switch {
			case now == start:
				pending = ""
			case now == pending:
				hit.Store(true)
				cancel()
				return
			default:
				pending = now
			}
		}
	}()
	err = follow(fctx)
	cancel()
	wg.Wait()
	return hit.Load() && ctx.Err() == nil, err
}

// SameEntry reports whether two journal cursors name the same entry, in the same file or not.
// A cursor is "s=<seqnum id>;i=<seqnum>;b=<boot id>;m=<monotonic>;t=<realtime>;x=<xor hash>"; the
// flush from RAM to /var/log/journal copies every entry with a new sequence number, so after a
// move journalctl --after-cursor no longer recognises the cursor's own entry and prints it once
// more. Its boot, both clocks and the hash of its fields stay the same - what sd-journal itself
// compares to drop an entry it finds in two files - so a follower that started after a cursor
// skips the entry this says is the one it had.
func SameEntry(a, b string) bool {
	fa, fb := cursorFields(a), cursorFields(b)
	for _, k := range []string{"b", "m", "t", "x"} {
		if fa[k] == "" || fa[k] != fb[k] {
			return false
		}
	}
	return true
}

func cursorFields(c string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(c, ";") {
		if k, v, ok := strings.Cut(part, "="); ok {
			m[k] = v
		}
	}
	return m
}
