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
// HmIP module and HMServer.jar without one - reads the devices of every BidCos interface process
// InterfacesList.xml lists, once, while it sets up VirtualDevices at its start. With rfd not
// answering, that start thread ends on a refused connection (with hs485d down it waits instead);
// VirtualDevices' port never opens, the server never reports itself started, and the ready step
// fails the start after 300 s - a restart loop that takes HmIP-RF down with it (2.0.0-beta.0 and
// beta.1: the QEMU test's absent LAN gateway; a Pi 4 with rfd stopped; the Charly with hs485d
// stopped). 3.89.11's jars started without them. With BidCos-RF not listed - the BidCos choice
// "none" - 1.5.1 never asks rfd and starts as before (beta.1's HmIP-only test).
//
// So the prep step first waits, briefly, for every BidCos interface process the plan runs to
// answer - to accept connections on its port, and rfd, which opens its port before it knows
// whether its interfaces come up and closes it again when none does (the QEMU test's silent LAN
// gateway), to have reported itself started as well (its status file names its running process,
// what the ready step of rfd's own unit waits for); hs485d writes no such file. As long as its
// unit is starting, up to BidCosWaitLimit; a unit that is inactive ends the wait after
// BidCosWaitGrace (the boot starts rfd beside the server, and its start job may still be queued
// behind multimacd); a unit that failed or waits between two of its restarts ends it at once - it
// does not answer before its next start.
//
// A process that still does not answer is left out (the maintainer's decision of 2026-11-12):
// its interface - BidCos-RF for rfd, BidCos-Wired for hs485d - is taken out of InterfacesList.xml
// for this start, the marker BidCosDegradedFile records what was taken out and why, and the
// server starts: HmIP-RF and VirtualDevices come up, without the BidCos devices and without the
// BidCos groups in VirtualDevices. Nothing of the BidCos data is touched - rfd's ids, keys,
// devices and rfd.conf stay as they are; only the generated list changes. The next start that
// finds the process answering puts the entry back and removes the marker; occulited's
// BidCosRecovery restarts the server once the process has answered for a while, so that start
// comes by itself.

// BidCosDegradedFile is the marker the prep step leaves while hmipserver runs without a BidCos
// interface whose process did not answer at its start.
const BidCosDegradedFile = "hmipserver.bidcos-degraded.json"

var (
	// BidCosWaitLimit bounds the wait while a unit is starting. With the ready step's 300 s it
	// stays inside the unit's TimeoutStartSec=420.
	BidCosWaitLimit = 90 * time.Second
	// BidCosWaitGrace is how long a process whose unit is not starting is waited for.
	BidCosWaitGrace = 20 * time.Second
	// bidcosWriteList is writeInPlace; a test replaces it.
	bidcosWriteList = writeInPlace
	// bidcosRFDReported is rfdReported; a test replaces it.
	bidcosRFDReported = rfdReported
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

// BidCosWait is one BidCos interface process the HmIP server reads at its start.
type BidCosWait struct {
	Daemon    string `json:"daemon"`          // rfd or hs485d
	Port      int    `json:"port"`            // the loopback port the server asks it on
	Interface string `json:"interface"`       // its InterfacesList.xml entry: BidCos-RF, BidCos-Wired
	State     string `json:"state,omitempty"` // its unit's ActiveState/SubState when the wait ended
}

// BidCosDegraded is the marker: hmipserver started without these processes' interfaces.
type BidCosDegraded struct {
	// Since is the first start without them; At this start (BidCosRecovery tells by it whether a
	// restart it asked for came back without them again).
	Since time.Time `json:"since"`
	At    time.Time `json:"at"`
	// Missing are the processes that did not answer; Removed the entries taken out of
	// InterfacesList.xml for them, which the start that finds them answering puts back.
	Missing []BidCosWait `json:"missing"`
	Removed []Interface  `json:"removed"`
	// HmIP: the server runs its HmIP half (a module), so HmIP-RF is up; otherwise VirtualDevices alone.
	HmIP bool `json:"hmip"`
}

// ReadBidCosDegraded answers the marker, nil without one.
func ReadBidCosDegraded(root string) *BidCosDegraded {
	var m BidCosDegraded
	if err := readJSONFile(shadowPath(root, BidCosDegradedFile), &m); err != nil {
		return nil
	}
	return &m
}

// bidcosWaits are the processes the plan runs, with the ports the radio run renders for them
// (rfd.conf's and hs485d.conf's Listen Port, see VarRFDConf and VarHS485DConf) and their entries.
func bidcosWaits(p Plan) []BidCosWait {
	var w []BidCosWait
	if p.RFD.Run {
		w = append(w, BidCosWait{Daemon: "rfd", Port: 32001, Interface: "BidCos-RF"})
	}
	if p.HS485D.Run {
		w = append(w, BidCosWait{Daemon: "hs485d", Port: 32000, Interface: BidCosWired.Name})
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
func unitState(ctx context.Context, run Runner, daemon string) (string, unitPhase) {
	out, err := run(ctx, "systemctl", "show", "-p", "ActiveState", "-p", "SubState", "--", daemon+".service")
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

// rfdReported: rfd's status file names the main process of its unit - rfd writes it once its
// interfaces are up (the ready step of rfd.service waits for the same).
func rfdReported(ctx context.Context, d Detector) bool {
	out, err := d.run()(ctx, "systemctl", "show", "-p", "MainPID", "--", "rfd.service")
	if err != nil {
		return false
	}
	pid := strings.TrimSpace(ParseKV(string(out))["MainPID"])
	return pid != "" && pid != "0" && strings.TrimSpace(readFile(d.path("/var/status/rfd.status"))) == pid
}

// bidcosWait is the prep step's wait (see the top of the file): the processes that still do not
// answer at its end, none when every one answers or the plan runs none.
func bidcosWait(ctx context.Context, d Detector, p Plan, logf func(string, ...any)) []BidCosWait {
	pending := bidcosWaits(p)
	start := d.now()
	logged := false
	for {
		var still []BidCosWait
		for _, w := range pending {
			if !bidcosDial(w.Port) || (w.Daemon == "rfd" && !bidcosRFDReported(ctx, d)) {
				still = append(still, w)
			}
		}
		if len(still) == 0 {
			if logged {
				all := bidcosWaits(p)
				verb := "answers"
				if len(all) > 1 {
					verb = "answer"
				}
				logf("prep hmipserver: %s %s after %s; the HmIP server starts", waitNames(all), verb, d.now().Sub(start).Round(time.Second))
			}
			return nil
		}
		pending = still
		waited := d.now().Sub(start)
		phase := unitDown
		for i := range pending {
			var ph unitPhase
			pending[i].State, ph = unitState(ctx, d.run(), pending[i].Daemon)
			phase = max(phase, ph)
		}
		if waited >= BidCosWaitLimit || phase == unitDown || (waited >= BidCosWaitGrace && phase != unitStarting) {
			return pending
		}
		if !logged {
			logf("prep hmipserver: waiting for %s (%s): the HmIP server reads the BidCos devices at its start and does not come up without them", waitNames(pending), waitStates(pending))
			logged = true
		}
		d.sleep(pollInterval)
	}
}

// bidcosPreStart is the prep step's part of task 442: the wait, then InterfacesList.xml without
// the processes that do not answer and with the ones an earlier start left out that answer now,
// and the marker. It never fails the start: a list it cannot write (an image whose unit does not
// let the step write it) leaves the file as it is, and the server starts as it would have.
func bidcosPreStart(ctx context.Context, d Detector, p Plan, logf func(string, ...any)) {
	missing := bidcosWait(ctx, d, p, logf)
	prev := ReadBidCosDegraded(d.Root)
	if len(missing) == 0 && prev == nil {
		return // the common start: everything answers, nothing was left out
	}
	listPath := d.path("/etc/config/InterfacesList.xml")
	entries := ParseInterfaces(readFile(listPath))
	dead := map[string]bool{}
	for _, w := range missing {
		dead[w.Interface] = true
	}
	// put back what an earlier start left out, when its process answers now and the plan still
	// lists it (a connection change may have dropped it meanwhile, and the radio run's own list
	// may already have it again)
	var back []string
	if prev != nil {
		for _, r := range prev.Removed {
			if dead[r.Name] || hasInterface(entries, r.Name) {
				continue
			}
			if e, ok := planInterface(p, r.Name); ok {
				entries = append(entries, e)
				back = append(back, e.Name)
			}
		}
	}
	// take out what does not answer; the entry recorded is the plan's, so it comes back as planned
	var removed []Interface
	changed := len(back) > 0
	for _, w := range missing {
		e, ok := planInterface(p, w.Interface)
		if !ok {
			continue
		}
		removed = append(removed, e)
		if hasInterface(entries, w.Interface) {
			entries = removeInterface(entries, w.Interface)
			changed = true
		}
	}
	if changed {
		list := RenderInterfaces(readFile(d.path("/etc/config_templates/InterfacesList.xml")), entries)
		if err := bidcosWriteList(listPath, []byte(list)); err != nil {
			logf("prep hmipserver: InterfacesList.xml could not be written (%v); the HmIP server starts with the list as it is", err)
			return
		}
	}
	if len(back) > 0 {
		logf("prep hmipserver: %s answers again: %s back in InterfacesList.xml, with the BidCos devices", strings.Join(daemonsOf(back), " and "), strings.Join(back, " and "))
	}
	if len(missing) == 0 {
		_ = os.Remove(shadowPath(d.Root, BidCosDegradedFile))
		return
	}
	m := BidCosDegraded{Since: d.now().UTC(), At: d.now().UTC(), Missing: missing, Removed: removed, HmIP: p.HmIPServerHmIP}
	if prev != nil {
		m.Since = prev.Since
	}
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		marker := shadowPath(d.Root, BidCosDegradedFile)
		_ = os.MkdirAll(filepath.Dir(marker), 0o755)
		_ = os.WriteFile(marker, b, 0o644)
		_ = os.Chmod(marker, 0o644) // the unit's UMask=0077 (see writeCounterState)
	}
	up := "VirtualDevices come"
	if p.HmIPServerHmIP {
		up = "HmIP-RF and VirtualDevices come"
	}
	var names []string
	for _, r := range removed {
		names = append(names, r.Name)
	}
	logf("prep hmipserver: %s does not answer (%s): %s left out of InterfacesList.xml for this start, so %s up without the BidCos devices and groups; occulited puts it back and restarts the server once %s answers", waitNames(missing), waitStates(missing), strings.Join(names, " and "), up, waitNames(missing))
}

// planInterface is the plan's entry by name (BidCos-Wired's own when the plan runs hs485d and
// lists none).
func planInterface(p Plan, name string) (Interface, bool) {
	for _, i := range p.Interfaces {
		if i.Name == name {
			return i, true
		}
	}
	if name == BidCosWired.Name && p.HS485D.Run {
		return BidCosWired, true
	}
	return Interface{}, false
}

// daemonsOf names the processes behind the entries.
func daemonsOf(ifaces []string) []string {
	var out []string
	for _, n := range ifaces {
		switch n {
		case "BidCos-RF":
			out = append(out, "rfd")
		case BidCosWired.Name:
			out = append(out, "hs485d")
		}
	}
	return out
}

// writeInPlace writes the file into its own inode: hmipserver's unit lets its prep step write
// InterfacesList.xml as a single bound file, which a rename onto it would fail on. The content is
// written over the old and the rest cut off, so a reader never sees an empty file in between.
func writeInPlace(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(b, 0); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Truncate(int64(len(b))); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
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
