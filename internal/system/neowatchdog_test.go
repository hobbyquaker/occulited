package system

import (
	"os"
	"testing"
)

// B-60: the CCU3 firmware's watchdog line goes when the NEO Server is not installed; the other
// lines stay, and an installed NEO Server keeps its line.
func TestDropOrphanNeoWatchdog(t *testing.T) {
	const tab = "*/5 * * * * /usr/local/addons/mediola/bin/watchdog\n0 3 * * * /usr/local/addons/cuxd/cron.sh\n"
	r := rootWith(t, map[string]string{"usr/local/crontabs/root": tab})
	dropped, err := r.DropOrphanNeoWatchdog()
	if err != nil || !dropped {
		t.Fatalf("%v %v", dropped, err)
	}
	if b, _ := os.ReadFile(r.join(neoServerCrontab)); string(b) != "0 3 * * * /usr/local/addons/cuxd/cron.sh\n" {
		t.Fatalf("crontab %q", b)
	}
	// nothing left to do: nothing written
	if dropped, err := r.DropOrphanNeoWatchdog(); err != nil || dropped {
		t.Fatalf("again: %v %v", dropped, err)
	}
	// the line alone (the stock CCU3): the file stays, empty
	only := rootWith(t, map[string]string{"usr/local/crontabs/root": "*/5 * * * * /usr/local/addons/mediola/bin/watchdog\n"})
	if dropped, err := only.DropOrphanNeoWatchdog(); err != nil || !dropped {
		t.Fatalf("only: %v %v", dropped, err)
	}
	if b, err := os.ReadFile(only.join(neoServerCrontab)); err != nil || len(b) != 0 {
		t.Fatalf("only: %q %v", b, err)
	}
	// no crontab at all
	if dropped, err := rootWith(t, nil).DropOrphanNeoWatchdog(); err != nil || dropped {
		t.Fatalf("none: %v %v", dropped, err)
	}
	// the NEO Server installed: its line stays
	inst := rootWith(t, map[string]string{"usr/local/crontabs/root": tab, "usr/local/addons/mediola/bin/watchdog": "#!/bin/sh\n"})
	if dropped, err := inst.DropOrphanNeoWatchdog(); err != nil || dropped {
		t.Fatalf("installed: %v %v", dropped, err)
	}
	if b, _ := os.ReadFile(inst.join(neoServerCrontab)); string(b) != tab {
		t.Fatalf("installed: %q", b)
	}
}
