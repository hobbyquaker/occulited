package radio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Shadow mode (task 129, phase 1): the new detection and plan run beside upstream's chain on a
// box and change nothing. ShadowDetect runs between the coprocessor check and multimacd, while
// the raw UART is free, and writes what it found and decided into the run directory; ShadowCheck
// runs after the daemons are up and compares the render with what the old chain did - the
// files, the units, the command lines - and reports every difference.

// ShadowDir is where the shadow files live: modules.json (the detection), plan.json (the plan),
// render.json (the files and command lines), shadow.json (the check's report).
const ShadowDir = "/run/occulite/radio"

// ShadowRender is render.json: the render's outputs plus what the check needs.
type ShadowRender struct {
	At        time.Time `json:"at"`
	Detection Detection `json:"detection"`
	Plan      Plan      `json:"plan"`
	Files     Files     `json:"files"`
	// RFDConfBefore is /etc/config/rfd.conf as the detection found it (upstream edits it in
	// place afterwards; the check compares the render with the edited file).
	RFDConfBefore string `json:"rfd_conf_before"`
}

// ShadowReport is shadow.json.
type ShadowReport struct {
	At          time.Time `json:"at"`
	PlannedAt   time.Time `json:"planned_at"`
	Match       bool      `json:"match"`
	Checked     []string  `json:"checked"`
	Differences []string  `json:"differences"`
}

func shadowPath(root, name string) string {
	if root == "" || root == "/" {
		return filepath.Join(ShadowDir, name)
	}
	return filepath.Join(root, ShadowDir, name)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ShadowDetect runs the detection, the plan and the render and writes them into the run
// directory. Nothing else on the box changes (the module reset and an HB-RF-ETH's connect are
// the detection's own, as they are S47's).
func ShadowDetect(ctx context.Context, root string, d Detector, logf func(string, ...any)) (ShadowRender, error) {
	if d.Root == "" {
		d.Root = root
	}
	env := ParseKV(readFile(filepath.Join(root, "var/hm_mode")))
	if d.Host == "" {
		d.Host = env["HM_HOST"]
	}
	det := d.Detect(ctx)
	for _, l := range det.Log {
		logf("detect: %s", l)
	}
	in := Load(ctx, root, d.Run, det)
	in.Diagrams = d.Diagrams
	p := MakePlan(in)
	for _, n := range p.Notes {
		logf("plan: %s", n)
	}
	sr := ShadowRender{At: time.Now(), Detection: det, Plan: p, Files: Render(in, p), RFDConfBefore: in.RFDConf}
	for _, w := range []struct {
		name string
		v    any
	}{{"modules.json", det}, {"plan.json", p}, {"render.json", sr}} {
		if err := writeJSON(shadowPath(root, w.name), w.v); err != nil {
			return sr, fmt.Errorf("%s: %w", w.name, err)
		}
	}
	logf("shadow: detection %s, plan written to %s (multimacd %v, rfd %v, hmipserver %v on %s, hs485d %v)",
		det.Duration.Round(10*time.Millisecond), shadowPath(root, "plan.json"), p.Multimacd.Run, p.RFD.Run, p.HmIPServer.Run, p.HmIPServer.Node, p.HS485D.Run)
	return sr, nil
}

// Live is what the check reads from the running box; the tests substitute it.
type Live struct {
	Root string
	// UnitState answers a unit's ActiveState and MainPID ("" and 0 when unknown).
	UnitState func(ctx context.Context, unit string) (string, int)
	// Cmdline answers a process's command line.
	Cmdline func(pid int) string
}

// ShadowCheck compares render.json with the box.
func ShadowCheck(ctx context.Context, root string, live Live, logf func(string, ...any)) (ShadowReport, error) {
	var sr ShadowRender
	b, err := os.ReadFile(shadowPath(root, "render.json"))
	if err != nil {
		return ShadowReport{}, fmt.Errorf("no shadow plan: %w", err)
	}
	if err := json.Unmarshal(b, &sr); err != nil {
		return ShadowReport{}, fmt.Errorf("render.json: %w", err)
	}
	r := ShadowReport{At: time.Now(), PlannedAt: sr.At, Checked: []string{}, Differences: []string{}}
	diff := func(f string, a ...any) { r.Differences = append(r.Differences, fmt.Sprintf(f, a...)) }
	checked := func(what string) { r.Checked = append(r.Checked, what) }
	p := func(s string) string {
		if root == "" || root == "/" {
			return s
		}
		return filepath.Join(root, s)
	}
	f := sr.Files

	// /var/hm_mode: the keys the detection owns, and the others kept
	want := ParseKV(f.HMMode)
	got := ParseKV(readFile(p("/var/hm_mode")))
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w, g := want[k], got[k]
		if k == "HM_HMRF_ADDRESS" && strings.HasPrefix(w, "0xFF") && strings.HasPrefix(g, "0xFF") && sr.Plan.HmRF != nil && sr.Plan.HmRF.Address == w {
			// a random address differs by construction when the module has none of its own
			m := sr.Detection.Modules[sr.Plan.HmRF.Module]
			if m.HmRFAddress == "0x000000" {
				continue
			}
		}
		if k == "HM_HMRF_ADDRESS_ACTIVE" && sr.Plan.HmRF != nil && want["HM_HMRF_ADDRESS"] == w && strings.HasPrefix(g, "0xFF") && sr.Detection.Modules[sr.Plan.HmRF.Module].HmRFAddress == "0x000000" {
			continue
		}
		if w != g {
			diff("hm_mode %s: plan %q, box %q", k, w, g)
		}
	}
	checked("/var/hm_mode")
	for name, w := range f.VarFiles {
		g := readFile(p("/var/" + name))
		if name == "rf_address" && sr.Plan.HmRF != nil && sr.Detection.Modules[sr.Plan.HmRF.Module].HmRFAddress == "0x000000" {
			continue
		}
		if w != g {
			diff("/var/%s: plan %q, box %q", name, w, g)
		}
	}
	checked("/var/board_serial and the RF files")

	// the daemons' files
	files := []struct{ path, want string }{
		{"/var/etc/multimacd.conf", f.MultimacdConf}, {"/var/etc/crRFD.conf", f.CrRFDConf}, {"/var/etc/HMServer.conf", f.HMServerConf},
		{"/var/etc/hs485d.conf", f.VarHS485DConf}, {"/var/etc/rfd.conf", f.VarRFDConf}, {"/etc/config/rfd.conf", f.RFDConf},
	}
	for _, c := range files {
		g, ok := readExisting(p(c.path))
		switch {
		case c.want == "" && !ok:
		case c.want == "" && ok:
			diff("%s: the plan writes none, the box has one", c.path)
		case c.want != "" && !ok:
			diff("%s: the plan writes it, the box has none", c.path)
		case c.want != g:
			diff("%s differs:\n%s", c.path, unifiedish(c.want, g))
		}
		checked(c.path)
	}
	// InterfacesList.xml by its entries (the template's whitespace varies per entry)
	wantIf := ParseInterfaces(f.InterfacesList)
	gotIf := ParseInterfaces(readFile(p("/etc/config/InterfacesList.xml")))
	if f.InterfacesList != "" || len(gotIf) > 0 {
		w, g := interfacesText(wantIf), interfacesText(gotIf)
		if w != g {
			diff("/etc/config/InterfacesList.xml: plan [%s], box [%s]", w, g)
		}
	}
	checked("/etc/config/InterfacesList.xml")

	// the units: running where the plan says so, and the command lines
	if live.UnitState != nil {
		for _, u := range []struct {
			unit string
			run  bool
		}{{"multimacd", sr.Plan.Multimacd.Run}, {"rfd", sr.Plan.RFD.Run}, {"hmipserver", sr.Plan.HmIPServer.Run}, {"hs485d", sr.Plan.HS485D.Run}, {"hmlangw", sr.Plan.Hmlangw.Run}} {
			state, pid := live.UnitState(ctx, u.unit+".service")
			active := state == "active" || state == "activating"
			if active != u.run {
				diff("%s: plan %s, unit %s", u.unit, runWord(u.run), state)
			}
			if active && u.run && live.Cmdline != nil && pid > 0 {
				w, g := normCmd(f.Commands[u.unit]), normCmd(live.Cmdline(pid))
				if w != g {
					diff("%s command line: plan %q, box %q", u.unit, w, g)
				}
			}
			checked(u.unit + ".service")
		}
	}
	r.Match = len(r.Differences) == 0
	for _, d := range r.Differences {
		logf("shadow: DIFFERENCE %s", strings.ReplaceAll(d, "\n", " | "))
	}
	if r.Match {
		logf("shadow: the plan matches the box (%d checks)", len(r.Checked))
	} else {
		logf("shadow: %d difference(s) in %d checks", len(r.Differences), len(r.Checked))
	}
	return r, writeJSON(shadowPath(root, "shadow.json"), r)
}

func runWord(b bool) string {
	if b {
		return "runs"
	}
	return "does not run"
}

func interfacesText(list []Interface) string {
	var out []string
	for _, i := range list {
		out = append(out, i.Name+"|"+i.URL+"|"+i.Info)
	}
	return strings.Join(out, " ")
}

// normCmd makes a command line comparable: the program by its base name, the arguments as
// they are; a /proc cmdline's NUL separators become spaces.
func normCmd(s string) string {
	s = strings.ReplaceAll(strings.TrimRight(s, "\x00"), "\x00", " ")
	s = strings.TrimPrefix(s, "exec ")
	if i := strings.Index(s, " >/var/log/"); i > 0 {
		s = s[:i]
	}
	fields := strings.Fields(s)
	if len(fields) > 0 {
		fields[0] = filepath.Base(fields[0])
	}
	return strings.Join(fields, " ")
}

// unifiedish is a small line diff for the journal: the lines only one side has.
func unifiedish(want, got string) string {
	w := strings.Split(strings.TrimRight(want, "\n"), "\n")
	g := strings.Split(strings.TrimRight(got, "\n"), "\n")
	set := func(l []string) map[string]int {
		m := map[string]int{}
		for _, s := range l {
			m[s]++
		}
		return m
	}
	ws, gs := set(w), set(g)
	var out []string
	for _, s := range w {
		if gs[s] == 0 {
			out = append(out, "  plan: "+s)
		}
	}
	for _, s := range g {
		if ws[s] == 0 {
			out = append(out, "  box:  "+s)
		}
	}
	if len(out) > 20 {
		out = append(out[:20], "  ... ("+strconv.Itoa(len(out)-20)+" more)")
	}
	return strings.Join(out, "\n")
}

// SystemdLive reads the units and command lines from the box.
func SystemdLive(root string, run Runner) Live {
	if run == nil {
		run = ExecRunner
	}
	return Live{
		Root: root,
		UnitState: func(ctx context.Context, unit string) (string, int) {
			out, err := run(ctx, "systemctl", "show", "-p", "ActiveState,MainPID", "--", unit)
			if err != nil {
				return "", 0
			}
			kv := ParseKV(string(out))
			pid, _ := strconv.Atoi(kv["MainPID"])
			return kv["ActiveState"], pid
		},
		Cmdline: func(pid int) string { return readFile(filepath.Join(root, "proc", strconv.Itoa(pid), "cmdline")) },
	}
}
