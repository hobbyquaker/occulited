package radio

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// openccu-lite task 442: since eQ-3's HMIPServer 1.5.1 the HmIP server - HMIPServer.jar with an
// HmIP module and HMServer.jar without one - reads the devices of the BidCos interface processes
// once, while it sets up VirtualDevices at its start. With rfd not answering, that start thread
// ends on a refused connection (with hs485d down it waits instead); VirtualDevices' port never
// opens, the server never reports itself started, and the ready step fails the start after 300 s -
// a restart loop that brings nothing (2.0.0-beta.0: the QEMU test's absent LAN gateway; a Pi 4
// with rfd stopped; the Charly with hs485d stopped). 3.89.11's jars started without them.
//
// So the prep step waits, before the server starts, for every BidCos interface process the plan
// runs to accept connections on its port: as long as its unit is starting, up to BidCosWaitLimit;
// a unit that is inactive ends the wait after BidCosWaitGrace (the boot starts rfd beside the
// server, and its start job may still be queued behind multimacd); a unit that failed or waits
// between two of its restarts ends it at once - it does not answer before its next start, and the
// server's own backoff then follows rfd's instead of keeping a start job of its own open beside it
// (a boot whose jobs never all end never finishes).
// Past that the start is held, as the clock hold does (task 299): the marker tells the pages
// which process the server waits for, the step fails, and the unit retries with its backoff (2 s
// doubling to 5 minutes, no start limit), so a LAN gateway that comes back is taken up at the next
// try. The wait and the hold are logged once per start, not per poll.

// BidCosHoldFile is the marker the prep step leaves while it holds hmipserver back for a BidCos
// interface process that does not answer.
const BidCosHoldFile = "hmipserver.bidcos-hold.json"

var (
	// BidCosWaitLimit bounds the wait while a unit is starting. With the ready step's 300 s it
	// stays inside the unit's TimeoutStartSec=420.
	BidCosWaitLimit = 90 * time.Second
	// BidCosWaitGrace is how long a process whose unit is not starting is waited for.
	BidCosWaitGrace = 20 * time.Second
	// bidcosDial answers whether something accepts a connection on the loopback port; a test
	// replaces it.
	bidcosDial = func(port int) bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}
)

// BidCosWait is one interface process the HmIP server waits for.
type BidCosWait struct {
	Daemon string `json:"daemon"`          // rfd or hs485d
	Port   int    `json:"port"`            // the loopback port the server asks it on
	State  string `json:"state,omitempty"` // its unit's ActiveState/SubState when the wait ended
}

// BidCosHold is why the prep step holds hmipserver back.
type BidCosHold struct {
	Since time.Time    `json:"since"`
	Wait  []BidCosWait `json:"wait"`
	// HmIP: the server runs its HmIP half (a module), so HmIP-RF waits as well; otherwise only
	// VirtualDevices do.
	HmIP bool `json:"hmip"`
}

// ReadBidCosHold answers the hold marker, nil without one.
func ReadBidCosHold(root string) *BidCosHold {
	var h BidCosHold
	if err := readJSONFile(shadowPath(root, BidCosHoldFile), &h); err != nil {
		return nil
	}
	return &h
}

// bidcosWaits are the processes the plan runs, with the ports the radio run renders for them
// (rfd.conf's and hs485d.conf's Listen Port, see VarRFDConf and VarHS485DConf).
func bidcosWaits(p Plan) []BidCosWait {
	var w []BidCosWait
	if p.RFD.Run {
		w = append(w, BidCosWait{Daemon: "rfd", Port: 32001})
	}
	if p.HS485D.Run {
		w = append(w, BidCosWait{Daemon: "hs485d", Port: 32000})
	}
	return w
}

// unitPhase is what the unit's state says about an answer soon.
type unitPhase int

const (
	unitDown     unitPhase = iota // failed, stopping, or between two restarts: no answer before its next start
	unitQueued                    // inactive: stopped, or its start job still waits for another unit
	unitStarting                  // a start in progress, or up and only about to open its port
)

// unitState reads the unit's ActiveState and SubState.
func unitState(ctx context.Context, d Detector, daemon string) (string, unitPhase) {
	out, err := d.run()(ctx, "systemctl", "show", "-p", "ActiveState", "-p", "SubState", "--", daemon+".service")
	if err != nil {
		return "unknown", unitQueued
	}
	kv := ParseKV(string(out))
	state := kv["ActiveState"] + "/" + kv["SubState"]
	switch {
	case kv["ActiveState"] == "activating" && kv["SubState"] != "auto-restart":
		return state, unitStarting
	case kv["ActiveState"] == "active", kv["ActiveState"] == "reloading":
		return state, unitStarting
	case kv["ActiveState"] == "inactive":
		return state, unitQueued
	}
	return state, unitDown
}

// bidcosPreStart is the prep step's wait (see the top of the file). nil: every process answers,
// or the plan runs none.
func bidcosPreStart(ctx context.Context, d Detector, p Plan, logf func(string, ...any)) error {
	pending := bidcosWaits(p)
	start := d.now()
	logged := false
	for {
		var still []BidCosWait
		for _, w := range pending {
			if !bidcosDial(w.Port) {
				still = append(still, w)
			}
		}
		if len(still) == 0 {
			_ = os.Remove(shadowPath(d.Root, BidCosHoldFile))
			if logged {
				logf("prep hmipserver: %s answers after %s; the HmIP server starts", waitNames(bidcosWaits(p)), d.now().Sub(start).Round(time.Second))
			}
			return nil
		}
		pending = still
		waited := d.now().Sub(start)
		phase := unitDown
		for i := range pending {
			var ph unitPhase
			pending[i].State, ph = unitState(ctx, d, pending[i].Daemon)
			phase = max(phase, ph)
		}
		if waited >= BidCosWaitLimit || phase == unitDown || (waited >= BidCosWaitGrace && phase != unitStarting) {
			break
		}
		if !logged {
			logf("prep hmipserver: waiting for %s (%s): the HmIP server reads the BidCos devices at its start and does not come up without them", waitNames(pending), waitStates(pending))
			logged = true
		}
		d.sleep(pollInterval)
	}
	hold := BidCosHold{Since: d.now().UTC(), Wait: pending, HmIP: p.HmIPServerHmIP}
	if prev := ReadBidCosHold(d.Root); prev != nil {
		hold.Since = prev.Since
	}
	if b, err := json.MarshalIndent(hold, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(shadowPath(d.Root, BidCosHoldFile)), 0o755)
		_ = os.WriteFile(shadowPath(d.Root, BidCosHoldFile), b, 0o644)
		_ = os.Chmod(shadowPath(d.Root, BidCosHoldFile), 0o644) // the unit's UMask=0077 (see writeCounterState)
	}
	what := "VirtualDevices"
	if p.HmIPServerHmIP {
		what = "HmIP-RF and VirtualDevices"
	}
	return fmt.Errorf("hmipserver is held back: %s does not answer (%s) - the HmIP server reads the BidCos devices at its start and does not come up without them, so %s stay down; the unit tries again with its backoff (up to every 5 minutes)", waitNames(pending), waitStates(pending), what)
}

func waitNames(w []BidCosWait) string {
	var n []string
	for _, x := range w {
		n = append(n, x.Daemon)
	}
	return strings.Join(n, " and ")
}

func waitStates(w []BidCosWait) string {
	var n []string
	for _, x := range w {
		s := fmt.Sprintf("%s on 127.0.0.1:%d", x.Daemon, x.Port)
		if x.State != "" {
			s += ", unit " + x.State
		}
		n = append(n, s)
	}
	return strings.Join(n, "; ")
}
