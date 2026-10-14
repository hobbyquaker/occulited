package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/radio"
)

// counterRepairMain is `occulited radio repair-counter` (openccu-lite task 349): it requests that
// hmipserver's next start sets the HmIP security counter above the highest value the devices are
// known to have seen, for a system whose counter wrapped or went backwards (the Status page's
// hmip-security-counter warning). Command line only (the maintainer, 2026-10-12).
func counterRepairMain(args []string) error {
	return counterRepair(args, os.Stdin, os.Stdout, isTerminal(os.Stdin), time.Now)
}

const counterRepairUsage = `usage: occulited radio repair-counter [--dry-run] [--force] [--yes] [--margin N] [--cancel] [--root DIR] [SGTIN]

Requests that the next start of hmipserver sets the HmIP security counter of the access point
(all of them without SGTIN) above the highest value its devices are known to have seen, so they
accept the system's frames again without being power-cycled or paired again. Applied by the
start's preparation, with a trusted clock only; restart hmipserver or the system afterwards.
`

func counterRepair(args []string, stdin io.Reader, out io.Writer, tty bool, now func() time.Time) error {
	fs := flag.NewFlagSet("occulited radio repair-counter", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprint(out, counterRepairUsage); fs.PrintDefaults() }
	root := fs.String("root", "/", "the filesystem root (a sandbox for a test)")
	dry := fs.Bool("dry-run", false, "show the numbers, request nothing")
	force := fs.Bool("force", false, "repair an access point the detector calls fine as well")
	yes := fs.Bool("yes", false, "do not ask (needed when the input is not a terminal)")
	margin := fs.Uint64("margin", radio.CounterRepairMargin, "how far above the highest known value the counter is set")
	cancel := fs.Bool("cancel", false, "withdraw a pending request")
	// flags before and after the SGTIN
	var rest []string
	for a := args; ; {
		if err := fs.Parse(a); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		a = fs.Args()[1:]
	}
	if len(rest) > 1 {
		return fmt.Errorf("one SGTIN at most: %s", strings.Join(rest, " "))
	}
	sgtin := ""
	if len(rest) == 1 {
		sgtin = rest[0]
	}
	if *cancel {
		had, err := radio.CancelCounterRepair(*root)
		if err != nil {
			return err
		}
		if had {
			fmt.Fprintln(out, "The pending repair request is withdrawn.")
		} else {
			fmt.Fprintln(out, "No repair request was pending.")
		}
		return nil
	}
	if req := radio.ReadCounterRepair(*root); req != nil {
		fmt.Fprintf(out, "A repair is already requested (%s) and waits for hmipserver's next start; --cancel withdraws it.\n", req.RequestedAt.Local().Format(time.DateTime))
	}
	plans, err := radio.PlanCounterRepair(*root, sgtin, *margin, *force, now())
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		fmt.Fprintln(out, "No HmIP access point found in /etc/config/crRFD/data.")
		return nil
	}
	todo := 0
	for _, p := range plans {
		fmt.Fprintf(out, "%s: %s\n", p.SGTIN, p.Verdict)
		known := "the highest value the devices are known to have seen"
		if p.FromOffset {
			known = "no start recorded - the stored offset stands in"
		}
		fmt.Fprintf(out, "  known:      %d (%s)\n", p.Known, known)
		fmt.Fprintf(out, "  offset:     %d, a start now computes %d", p.Offset, p.CalcNow)
		if p.CalcNow >= radio.CounterWrap {
			fmt.Fprintf(out, " (sent as %d)", p.CalcNow%radio.CounterWrap)
		}
		fmt.Fprintln(out)
		if p.Refused != "" {
			fmt.Fprintf(out, "  not repaired: %s\n", p.Refused)
			continue
		}
		todo++
		fmt.Fprintf(out, "  repair:     the next start computes %d (offset about %d at this clock)\n", p.Target, p.NewOffset)
	}
	if todo == 0 || *dry {
		if *dry && todo > 0 {
			fmt.Fprintln(out, "Dry run: nothing requested.")
		}
		return nil
	}
	if !*yes {
		if !tty {
			return fmt.Errorf("the input is not a terminal: pass --yes to request the repair")
		}
		fmt.Fprint(out, "Request this repair for hmipserver's next start? [y/N] ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Nothing requested.")
			return nil
		}
	}
	n, err := radio.RequestCounterRepair(*root, plans, now())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Requested for %d access point(s). It is applied when hmipserver starts next: systemctl restart hmipserver (or reboot the system). The journal of hmipserver's start names the change; the original file is kept as <SGTIN>.ap.bak.\n", n)
	return nil
}
