package radio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Hotplug (task 129 phase 4, D-83): a radio stick plugged in or pulled while the box runs. A udev
// rule starts occu-radio-hotplug.service, which runs `occulited radio hotplug`: the detection is
// done again without disturbing the modules the daemons hold (Rescan), the plan is made from it,
// and only the daemons whose plan changed are stopped, written for and started again. A stick
// that nothing uses (a second one) changes the results files and nothing else.

// Lock serialises the run and the hotplug (and with the run, the connection change that re-runs
// it): an exclusive flock on <RunDir>/.lock, released by the function it returns.
func Lock(root string) (func(), error) {
	p := shadowPath(root, ".lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", p, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// Rescan is the detection for a hotplug: the nodes and the adapter are listed again; a node the
// last detection knew with the same device type keeps its result (a daemon holds it, and a
// probe would reset the module under it); a new node is reset and probed (nothing holds it); a
// node that went away drops out. The board's MAC is the last detection's; a configured HB-RF-ETH
// that is not connected is tried again first (task 218).
func (d Detector) Rescan(ctx context.Context, prev Detection) Detection {
	start := d.now()
	det := Detection{HBRFETH: prev.HBRFETH, HBRFETHConnected: prev.HBRFETHConnected, TTYFallback: prev.TTYFallback, BoardMAC: prev.BoardMAC}
	logf := func(f string, a ...any) { det.Log = append(det.Log, fmt.Sprintf(f, a...)) }
	d.hbRFETHRescan(ctx, prev, &det, logf) // task 218: a late board, or one set or removed since
	d.enumerate(ctx, &det, logf)
	var fresh []Module
	var idx []int
	for i := range det.Modules {
		m := &det.Modules[i]
		if m.USBAdapter {
			continue
		}
		if old, ok := knownModule(prev, *m); ok {
			*m = old
			continue
		}
		fresh = append(fresh, *m)
		idx = append(idx, i)
	}
	if len(fresh) > 0 {
		d.reset(fresh)
		d.sleep(d.resetWait())
		for _, i := range idx {
			d.probe(ctx, &det.Modules[i], d.probeLimit(), logf)
		}
	}
	det.Duration = d.now().Sub(start)
	return det
}

// knownModule is the last detection's module on the same node with the same device type.
func knownModule(prev Detection, m Module) (Module, bool) {
	for _, o := range prev.Modules {
		if !o.USBAdapter && o.Name == m.Name && o.DeviceType == m.DeviceType {
			return o, true
		}
	}
	return Module{}, false
}

// SameModules: two detections found the same hardware (nodes, device types, probe results and
// identities, the adapter).
func SameModules(a, b []Module) bool {
	key := func(m Module) string {
		return strings.Join([]string{m.Name, m.DeviceType, m.Probe, m.Hardware, m.Serial, fmt.Sprint(m.USBAdapter)}, "|")
	}
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, m := range a {
		seen[key(m)]++
	}
	for _, m := range b {
		seen[key(m)]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func serialOf(r *Role) string {
	if r == nil {
		return ""
	}
	return r.Serial
}

// Affected are the radio daemons, in boot order, whose plan differs: whether they run, what they
// open, the module behind them - and the ones on the multiplexer when multimacd changes.
func Affected(old, p Plan) []string {
	mm := old.Multimacd.Run != p.Multimacd.Run || old.Multimacd.Node != p.Multimacd.Node
	rfd := old.RFD.Run != p.RFD.Run || old.RFD.Node != p.RFD.Node || old.RFDLocal != p.RFDLocal || old.RFDUSBAdapter != p.RFDUSBAdapter ||
		serialOf(old.HmRF) != serialOf(p.HmRF) || (mm && (old.RFDLocal || p.RFDLocal))
	hm := old.HmIPServer.Run != p.HmIPServer.Run || old.HmIPServer.Node != p.HmIPServer.Node || old.HmIPServerHmIP != p.HmIPServerHmIP ||
		serialOf(old.HmIP) != serialOf(p.HmIP) || (mm && (old.HmIPServer.Node == "/dev/mmd_hmip" || p.HmIPServer.Node == "/dev/mmd_hmip"))
	var out []string
	for _, x := range []struct {
		name string
		on   bool
	}{{"multimacd", mm}, {"rfd", rfd}, {"hmipserver", hm}} {
		if x.on {
			out = append(out, x.name)
		}
	}
	return out
}

// BusyFiles are occulited's markers of a coprocessor flash and a connection change in flight: both
// stop the daemons on purpose and re-run the detection themselves when they are done, and a flashed
// stick re-enumerates on its way - a hotplug stands aside while one is there.
var BusyFiles = []string{"/usr/local/etc/occulite/radio-firmware/running.json", "/usr/local/etc/occulite/radio-connections/running.json"}

// HotplugReport is what a hotplug did.
type HotplugReport struct {
	Changed   bool
	Restarted []string
}

// Hotplug waits for the devices to settle, rescans, and restarts what the new plan changes. It
// repeats while the hardware keeps changing (a stick that enumerates in steps), at most three
// times.
func Hotplug(ctx context.Context, root string, d Detector, settle time.Duration, logf func(string, ...any)) (HotplugReport, error) {
	if d.Root == "" {
		d.Root = root
	}
	var rep HotplugReport
	unlock, err := Lock(root)
	if err != nil {
		return rep, err
	}
	defer unlock()
	closed := false // the gate (B-307) is open again on every way out, a panic in the write included
	defer func() {
		if closed {
			_ = OpenGate(root)
		}
	}()
	for round := 0; round < 3; round++ {
		d.sleep(settle)
		for _, f := range BusyFiles {
			if exists(d.path(f)) {
				logf("hotplug: a flash or a connection change is running (%s); it re-runs the detection itself", f)
				return rep, nil
			}
		}
		var prev Detection
		var old Plan
		if err := readJSONFile(shadowPath(root, "modules.json"), &prev); err != nil {
			return rep, fmt.Errorf("no detection of the boot to compare with: %w", err)
		}
		if err := readJSONFile(shadowPath(root, "plan.json"), &old); err != nil {
			return rep, fmt.Errorf("no plan of the boot to compare with: %w", err)
		}
		if old.Mode == "HM-LGW" {
			logf("hotplug: LAN-gateway mode, nothing is re-planned")
			return rep, nil
		}
		det := d.Rescan(ctx, prev)
		if SameModules(prev.Modules, det.Modules) {
			// task 218: a board that was tried and did not answer changes no module - say so all the same
			for _, l := range det.Log {
				if strings.HasPrefix(l, "HB-RF-ETH") {
					logf("hotplug: %s", l)
				}
			}
			if round == 0 {
				logf("hotplug: the radio hardware is unchanged")
			}
			// B-218: the board came back from a power cycle - the kernel's reconnect had not taken
			// it, a fresh connect did. Its module was reset under daemons that still hold the node
			// and still say CONNECTED 1, and deliver nothing (the lab, 2026-09-25 14:56-15:03): they
			// are restarted. A link drop the kernel mends on its own restarts nothing.
			if det.HBRFETHFresh {
				if _, _, daemons := old.OnHBRFETH(); len(daemons) > 0 {
					logf("hotplug: the HB-RF-ETH was connected afresh after a loss (its module was reset) - restarting %s", strings.Join(daemons, ", "))
					for i := len(daemons) - 1; i >= 0; i-- {
						if _, err := d.run()(ctx, "systemctl", "stop", "--", daemons[i]+".service"); err != nil {
							logf("hotplug: stopping %s: %v", daemons[i], err)
						}
					}
					for _, u := range daemons {
						if _, err := d.run()(ctx, "systemctl", "start", "--", u+".service"); err != nil {
							logf("hotplug: starting %s: %v", u, err)
						}
					}
					rep.Restarted = append(rep.Restarted, daemons...)
				}
			}
			return rep, nil
		}
		rep.Changed = true
		for _, l := range det.Log {
			logf("hotplug: %s", l)
		}
		in := Load(ctx, root, d.Run, det)
		in.Diagrams = d.Diagrams
		p := MakePlan(in)
		aff := Affected(old, p)
		if len(aff) == 0 {
			logf("hotplug: the hardware changed, the daemons' plan did not")
		} else {
			logf("hotplug: restarting %s", strings.Join(aff, ", "))
		}
		// openccu-lite B-307: no start from outside (an addon's Wants=, a Restart=) on the old
		// plan's files between the stop and the write
		gated := len(aff) > 0
		if gated {
			if err := CloseGate(root, HotplugGateLimit); err != nil {
				logf("hotplug: the radio units could not be gated: %v", err)
				gated = false
			}
			closed = closed || gated
		}
		for i := len(aff) - 1; i >= 0; i-- {
			if _, err := d.run()(ctx, "systemctl", "stop", "--", aff[i]+".service"); err != nil {
				logf("hotplug: stopping %s: %v", aff[i], err)
			}
		}
		_, werr := write(ctx, root, d, det, in, p, logf)
		// the daemons come back whether the write worked or not; a unit the plan does not need
		// is skipped by its condition. Each is let through the gate right before its start, in
		// boot order, and the gate opens after the last.
		for _, u := range aff {
			if gated {
				if err := ReleaseGate(root, u); err != nil {
					logf("hotplug: releasing %s: %v", u, err)
				}
			}
			if _, err := d.run()(ctx, "systemctl", "start", "--", u+".service"); err != nil {
				logf("hotplug: starting %s: %v", u, err)
			}
		}
		if gated {
			if err := OpenGate(root); err != nil {
				logf("hotplug: the radio units' gate could not be opened: %v", err)
			}
		}
		rep.Restarted = append(rep.Restarted, aff...)
		if werr != nil {
			return rep, werr
		}
	}
	return rep, nil
}

func readJSONFile(p string, v any) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
