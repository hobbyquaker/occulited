package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/radio"
)

// radioMain is `occulited radio <command>` (task 129, D-83): the radio stack's detection, plan
// and units' root steps, run as root at boot independently of the occulited service.
//
//	run              occu-init-rf-hardware.service: detect, plan, render and write the box (the
//	                 daemons' files, /var/hm_mode, the RF files, the environment files and the
//	                 activation markers under /run/occulite/radio)
//	stop             its stop: the coprocessor bootloader handover before a staged firmware update,
//	                 an HB-RF-ETH disconnected
//	prep <daemon>    a daemon unit's ExecStartPre as root (nodes, groups, ownership)
//	ready <daemon>   its ExecStartPost (the daemon's own "started", multimacd's endpoints)
//	stopped <daemon> its ExecStopPost
//	check            occu-radio-shadow-check.service: the render against the box after the daemons
//	                 are up (the self-check of the marker boxes; every difference one journal line)
//	detect, plan     what the box has, and the decisions, as JSON
//	oracle           the fork's differential harness's file set for a sandbox root
//	hotplug          occu-radio-hotplug.service (udev, a radio stick plugged in or pulled): the
//	                 rescan, and only the daemons whose plan changed restarted
//	lgw-firmware     occu-lgw-firmware-update.service: the LAN gateways' firmware (S58)
//	lgw-keys         occu-set-lgw-key.service: the queued LAN gateway key changes (S59)
func radioMain(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: occulited radio run|stop|hotplug|lgw-firmware|lgw-keys|prep <daemon>|ready <daemon>|stopped <daemon>|check|detect|plan|oracle [--root <dir>] [--out <dir>]")
	}
	// The steps run inside the daemons' units and inherit their UMask= - hmipserver's 0077 is for
	// the server's own files (its device files, the access point's identity and key) - while what
	// the steps write under /run/occulite/radio and /var/status is read by the daemon as its own
	// user (openccu-lite B-284: the rejection marker came out 0600 and the pages saw no rejection).
	// The steps write with the run step's umask; a file that must stay closed gets its mode set
	// explicitly (the network key, rfd.conf).
	syscall.Umask(0o022)
	cmd := args[0]
	daemon := ""
	switch cmd {
	case "shadow":
		// the phase-1 names: "shadow check" is check; "shadow detect" wrote without changing the box
		// and has no unit any more
		if len(args) > 1 && args[1] == "check" {
			cmd, args = "check", args[1:]
		} else {
			return fmt.Errorf("occulited radio shadow: only \"check\" remains (the run step replaced \"shadow detect\")")
		}
	case "prep", "ready", "stopped":
		if len(args) < 2 {
			return fmt.Errorf("usage: occulited radio %s <multimacd|rfd|hmipserver|hs485d|hmlangw>", cmd)
		}
		daemon, args = args[1], args[1:]
	}
	fs := flag.NewFlagSet("occulited radio "+cmd, flag.ContinueOnError)
	root := fs.String("root", "/", "the filesystem root (a sandbox for a test)")
	out := fs.String("out", "", "oracle: the directory the file set is written into")
	gpio := fs.Duration("gpio-limit", 6*time.Second, "the first probe's limit on a GPIO header node (0 = none, and no second pass)")
	limit := fs.Duration("probe-limit", 45*time.Second, "the limit on every other probe")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	env := radio.ParseKV(readFileOr(*root+"/var/hm_mode", *root))
	d := radio.Detector{Root: *root, Host: env["HM_HOST"], GPIOLimit: *gpio, ProbeLimit: *limit}
	// task 33: hmipserver's diagram data only with hmipserver.diagrams on (occulited.json)
	if c, err := config.Load(filepath.Join(*root, config.DefaultPath)); err == nil {
		d.Diagrams = c.HmIPServer.Diagrams
	}
	switch cmd {
	case "run":
		_, err := radio.Run(ctx, *root, d, radioLog)
		return err
	case "stop":
		radio.Stop(ctx, d, radioLog)
		return nil
	case "prep":
		p, err := radio.LoadPlan(*root)
		if err != nil {
			return err
		}
		return radio.Prep(ctx, d, daemon, p, radioLog)
	case "ready":
		pid, _ := strconv.Atoi(os.Getenv("MAINPID"))
		return radio.Ready(ctx, d, daemon, pid, radioLog)
	case "stopped":
		return radio.Stopped(ctx, d, daemon, radioLog)
	case "check":
		r, err := radio.ShadowCheck(ctx, *root, radio.SystemdLive(*root, nil), radioLog)
		if err != nil {
			return err
		}
		if !r.Match {
			// the unit shows the difference; the report is in shadow.json
			return fmt.Errorf("%d difference(s) between the plan and the box", len(r.Differences))
		}
		return nil
	case "detect":
		det := d.Detect(ctx)
		return json.NewEncoder(os.Stdout).Encode(det)
	case "plan":
		det := d.Detect(ctx)
		in := radio.Load(ctx, *root, nil, det)
		p := radio.MakePlan(in)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	case "hotplug":
		_, err := radio.Hotplug(ctx, *root, d, 2*time.Second, radioLog)
		return err
	case "lgw-firmware":
		return radio.LGWFirmware(ctx, radio.LGWStep{Root: *root}, radioLog)
	case "lgw-keys":
		return radio.LGWKeys(ctx, radio.LGWStep{Root: *root}, radioLog)
	case "oracle":
		if *out == "" {
			return fmt.Errorf("oracle: --out is required")
		}
		_, err := radio.WriteOracle(ctx, *root, *out, d)
		return err
	}
	return fmt.Errorf("unknown radio command %q", cmd)
}

// radioLog prints one journal line (the units run with StandardOutput=journal).
func radioLog(f string, a ...any) { fmt.Printf(f+"\n", a...) }

func readFileOr(path, root string) string {
	if root == "/" {
		path = "/var/hm_mode"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
