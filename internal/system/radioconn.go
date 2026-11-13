package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/radio"
)

// The radio connections (task 129 phase 3, D-81, D-82, D-98): which local module each interface
// process uses. The choices live in the daemons' own files (radio.ReadChoices); a change is
// written there, then the stack is re-planned the way a flash re-plans it - the daemons stop in
// the reverse of the boot order, occu-init-rf-hardware runs `occulited radio run` again, and the
// daemons start in boot order, each skipped by its unit's condition when the new plan does not
// need it.

const (
	hmipUserConf = "/etc/config/crRFD/hmip_user.conf"
	rfdConfPath  = "/etc/config/rfd.conf"
)

// ErrConnApplyRunning: one change at a time, and none while a flash holds the stack.
var ErrConnApplyRunning = errors.New("a radio connection change or a coprocessor flash is running")

// ErrConnUnavailable: the box has no plan - the radio stack is not occulited's (an older image,
// busybox init) or the detection has not run yet.
var ErrConnUnavailable = errors.New("the radio plan is not available on this system")

// ConfirmRequired is the refusal of a change that takes the local BidCos-RF radio away while
// devices are paired: the caller repeats it with confirm once the user has seen the list (D-81).
//
// It is also the refusal of a change that moves HmIP-RF to another module without confirm
// (openccu-lite task 317, D-120): the move makes hmipserver rewrite the HmIP identity files
// (crRFD/data/<SGTIN>.ap, .apkx, .bbkx) for the new module - with eQ-3's key server an adapter
// exchange it may refuse back - so it happens only on the user's word after the warning.
//
// And of a change that sets HmIP to none while HmIP-RF runs on a module (occulited task 147): the
// HmIP devices are out of reach until HmIP is set back; their pairings stay.
type ConfirmRequired struct {
	Devices  []BidCosDevice `json:"devices"`
	HmIPMove *HmIPMoveInfo  `json:"hmip_move,omitempty"`
	HmIPOff  bool           `json:"hmip_off,omitempty"`
}

func (e *ConfirmRequired) Error() string {
	if e.HmIPOff && len(e.Devices) == 0 {
		return "HmIP is switched off and the HmIP devices are out of reach until it is set back; confirm the change"
	}
	if e.HmIPMove != nil && len(e.Devices) == 0 {
		return fmt.Sprintf("HmIP-RF moves from module %s to module %s and its identity files are rewritten; confirm the change", e.HmIPMove.From, e.HmIPMove.To)
	}
	return fmt.Sprintf("%d paired BidCos-RF devices lose the local radio; confirm the change", len(e.Devices))
}

// SnapshotBlocked refuses a move of HmIP-RF away from a module of which a snapshot from a switch to
// local key mode is kept (openccu-lite B-301): the module-move snapshot would replace it, so it is
// discarded first, on the user's word.
type SnapshotBlocked struct {
	SGTIN string `json:"sgtin"`
}

func (e *SnapshotBlocked) Error() string {
	return "a snapshot of " + e.SGTIN + " from before local key mode was switched off is kept; discard it, then move HmIP-RF"
}

// BidCosDevice is one paired device as rfd lists it.
type BidCosDevice struct {
	Address string `json:"address"`
	Type    string `json:"type"`
}

// ConnApply is one change, in flight or finished.
type ConnApply struct {
	Choices  radio.Choices `json:"choices"`
	Previous radio.Choices `json:"previous"`
	Started  time.Time     `json:"started"`
	Finished *time.Time    `json:"finished,omitempty"`
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Lines    []string      `json:"lines"`
}

// ConnDaemon is one interface process in the page's terms.
type ConnDaemon struct {
	Run    bool   `json:"run"`
	Node   string `json:"node,omitempty"`
	Reason string `json:"reason"`
}

// ConnPlan is the part of a plan the page shows: per process whether it runs, on what and why,
// the modules in their roles, and a pinned module that is missing.
type ConnPlan struct {
	Multimacd  ConnDaemon `json:"multimacd"`
	RFD        ConnDaemon `json:"rfd"`
	HmIPServer ConnDaemon `json:"hmipserver"`
	// HmIPServerJar is the server hmipserver runs: HMIPServer.jar, or HMServer.jar with HmIP set
	// to none (occulited task 147).
	HmIPServerJar string      `json:"hmipserver_jar"`
	HmRF          *radio.Role `json:"hmrf,omitempty"`
	HmIP          *radio.Role `json:"hmip,omitempty"`
	RFDLocal      bool        `json:"rfd_local"`
	RFDUSBAdapter bool        `json:"rfd_usb_adapter"`
	RFDLANGateway bool        `json:"rfd_lan_gateway"`
	// HmIPAdvanced: duty cycle, carrier sense and LAN routing (HAP, DRAP) - an RPI-RF-MOD or an
	// HmIP-RFUSB only, as upstream sets it.
	HmIPAdvanced  bool   `json:"hmip_advanced"`
	MissingHmIP   string `json:"missing_hmip,omitempty"`
	MissingBidCos string `json:"missing_bidcos,omitempty"`
	// HmIPPin: on Automatic, the module HmIP-RF is kept on - the one holding the HmIP network
	// (openccu-lite task 318); missing_hmip names it when it is not there.
	HmIPPin    string   `json:"hmip_pin,omitempty"`
	Interfaces []string `json:"interfaces"`
	Notes      []string `json:"notes"`
}

func connPlan(p radio.Plan) ConnPlan {
	c := ConnPlan{
		Multimacd:  ConnDaemon{Run: p.Multimacd.Run, Node: p.Multimacd.Node, Reason: p.Multimacd.Reason},
		RFD:        ConnDaemon{Run: p.RFD.Run, Node: p.RFD.Node, Reason: p.RFD.Reason},
		HmIPServer: ConnDaemon{Run: p.HmIPServer.Run, Node: p.HmIPServer.Node, Reason: p.HmIPServer.Reason}, HmIPServerJar: p.HmIPServerJar,
		HmRF: p.HmRF, HmIP: p.HmIP, RFDLocal: p.RFDLocal, RFDUSBAdapter: p.RFDUSBAdapter, RFDLANGateway: p.RFDLANGateway,
		HmIPAdvanced: p.HmIPServerAdvanced, MissingHmIP: p.MissingHmIP, MissingBidCos: p.MissingBidCos,
		HmIPPin: p.HmIPPin, Interfaces: []string{}, Notes: p.Notes,
	}
	for _, i := range p.Interfaces {
		c.Interfaces = append(c.Interfaces, i.Name)
	}
	if c.Notes == nil {
		c.Notes = []string{}
	}
	return c
}

// localBidCos: rfd has a radio of its own - a module through multimacd or the HM-CFG-USB-2.
func (c ConnPlan) localBidCos() bool { return c.RFD.Run && (c.RFDLocal || c.RFDUSBAdapter) }

// RadioConnStatus is GET /radio/connections.
type RadioConnStatus struct {
	Available bool          `json:"available"`
	Choices   radio.Choices `json:"choices"`
	Options   radio.Options `json:"options"`
	// Plan is what the boot's (or the last change's) run decided.
	Plan *ConnPlan `json:"plan,omitempty"`
	Mode string    `json:"mode,omitempty"`
	// HmIPFatal: hmipserver's last start failed on a known fatal error (D-102), e.g. the key server
	// rejecting the adapter exchange; the unit waits for the next run of the radio stack
	HmIPFatal *radio.HmIPFatal `json:"hmip_fatal,omitempty"`
	// Modules is every module the detection found, with the roles the plan gave it - the page's
	// module cards (openccu-lite B-272): a module in no role, such as the one on an HB-RF-ETH
	// added while both interface processes are pinned to a stick, is shown all the same.
	Modules []ConnModule `json:"modules"`
	// HeaderSilent: the GPIO header's UART answered nothing while a module chosen for an interface
	// process is missing from the detection (openccu-lite B-300) - the one case where a module may
	// be expected on the header; nil otherwise. An empty header has no module card.
	HeaderSilent *ConnHeaderSilent `json:"header_silent,omitempty"`
	// HBRFETH is the configured HB-RF-ETH's state, nil when none is configured (B-272): the page
	// says a board is on its way while its module is not in the detection yet.
	HBRFETH *ConnHBRFETH `json:"hb_rf_eth,omitempty"`
	Running *ConnApply   `json:"running"`
	Last    *ConnApply   `json:"last"`
	// HmIPMoveBack: a snapshot from before a move of HmIP-RF to another module is kept, and the
	// page offers the way back (openccu-lite B-285); absent without one, and during a change.
	// Hostname is the system's name, which the way back has typed as its confirmation.
	HmIPMoveBack *HmIPMoveBack `json:"hmip_move_back,omitempty"`
	// HmIPCounterGap: after the way back, the previous module's HmIP security counter is still
	// behind what the other module sent (openccu-lite B-302): the page's notice with the end time.
	HmIPCounterGap *HmIPCounterGap `json:"hmip_counter_gap,omitempty"`
	Hostname       string          `json:"hostname,omitempty"`
}

// ConnModule is one detected module as the Interfaces page lists it (openccu-lite B-272).
type ConnModule struct {
	Serial     string `json:"serial"`
	Hardware   string `json:"hardware"`
	Node       string `json:"node,omitempty"`
	DeviceType string `json:"device_type,omitempty"`
	SGTIN      string `json:"sgtin,omitempty"`
	Version    string `json:"version,omitempty"`
	// Probe is the detection's verdict (ok, none, timeout, error); Detail its message when not ok.
	Probe  string `json:"probe"`
	Detail string `json:"detail,omitempty"`
	// Roles are the interface protocols the plan runs on the module: BidCos-RF, HmIP-RF; empty
	// for a module no process uses.
	Roles []string `json:"roles"`
}

// ConnHeaderSilent is the silent GPIO header where a chosen module is missing (openccu-lite B-300).
type ConnHeaderSilent struct {
	Node string `json:"node"`
	// Probe is timeout or none; Detail the detection's message.
	Probe  string `json:"probe"`
	Detail string `json:"detail,omitempty"`
	// Missing are the chosen identities no answering module carries.
	Missing []string `json:"missing"`
}

// headerSilent is the hint for a silent header while a chosen module is missing, nil otherwise.
func headerSilent(det radio.Detection, c radio.Choices) *ConnHeaderSilent {
	var missing []string
	for _, id := range []string{c.HmIP, c.BidCos} {
		if id == radio.ChoiceAuto || id == radio.BidCosNone || id == radio.HmIPNone || det.HasModule(id) || slices.Contains(missing, id) {
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return nil
	}
	for _, m := range det.Modules {
		if m.EmptyHeader() {
			return &ConnHeaderSilent{Node: m.Node, Probe: m.Probe, Detail: m.Detail, Missing: missing}
		}
	}
	return nil
}

// ConnHBRFETH is the configured HB-RF-ETH's state on the Interfaces page (openccu-lite B-272).
type ConnHBRFETH struct {
	Address string `json:"address"`
	// Connected: the kernel module has the board.
	Connected bool `json:"connected"`
	// Detected: a module behind the board is in the detection (the radio hotplug has run since
	// the board was connected); Serial is that module's.
	Detected bool   `json:"detected"`
	Serial   string `json:"serial,omitempty"`
}

// connModules lists the detection's modules with the plan's roles (B-272). A role names its module
// by node, the HM-CFG-USB-2 (no node) by serial. The GPIO header's UART that nothing answered on
// is no module and is left out (openccu-lite B-300).
func connModules(det radio.Detection, p radio.Plan) []ConnModule {
	out := []ConnModule{}
	holds := func(r *radio.Role, m radio.Module) bool {
		if r == nil {
			return false
		}
		if m.Node != "" || r.Node != "" {
			return r.Node == m.Node
		}
		return r.Serial != "" && r.Serial == m.Serial
	}
	for _, m := range det.Modules {
		c := ConnModule{Serial: m.Serial, Hardware: m.Hardware, Node: m.Node, DeviceType: m.DeviceType, SGTIN: m.SGTIN, Version: m.Version, Probe: m.Probe, Detail: m.Detail, Roles: []string{}}
		if m.USBAdapter {
			c.Hardware, c.DeviceType = "HM-CFG-USB-2", "USB"
		}
		if holds(p.HmRF, m) {
			c.Roles = append(c.Roles, "BidCos-RF")
		}
		if p.HmIPServerHmIP && holds(p.HmIP, m) {
			c.Roles = append(c.Roles, "HmIP-RF")
		}
		if m.EmptyHeader() && len(c.Roles) == 0 {
			continue
		}
		out = append(out, c)
	}
	return out
}

// hbRFETH is the configured board's state against the detection (B-272), nil without a board.
func (s *RadioConnections) hbRFETH(det radio.Detection) *ConnHBRFETH {
	addr := radio.HBRFETHAddress(string(s.Root))
	if addr == "" {
		return nil
	}
	h := &ConnHBRFETH{Address: addr}
	h.Connected, _ = radio.HBRFETHConnected(string(s.Root))
	for _, m := range det.Modules {
		if m.DeviceType == "HB-RF-ETH@"+addr {
			h.Detected, h.Serial = true, m.Serial
		}
	}
	return h
}

// ConnPreview is what a change would give: the new plan, and what it costs.
type ConnPreview struct {
	Choices radio.Choices `json:"choices"`
	Plan    ConnPlan      `json:"plan"`
	// Changed: the plan differs from the running one in a daemon or its connection.
	Changed bool `json:"changed"`
	// Restarts are the units the change stops and starts again (all radio units: the detection
	// must have the modules free).
	Restarts []string `json:"restarts"`
	// BidCosLost: the change takes rfd's local radio away; Devices are the paired ones then.
	BidCosLost bool           `json:"bidcos_lost"`
	Devices    []BidCosDevice `json:"devices"`
	// DevicesError: rfd did not answer, so the list may be incomplete.
	DevicesError string `json:"devices_error,omitempty"`
	// HmIPMove: the change moves HmIP-RF from one module to another (openccu-lite B-285) - an
	// adapter exchange through eQ-3's key server while local key mode is off, and one-way in
	// practice: the change takes a module-move snapshot first (Snapshot), the way back.
	HmIPMove *HmIPMoveInfo `json:"hmip_move,omitempty"`
	// HmIPOff: the change sets HmIP to none while HmIP-RF runs on a module (occulited task 147):
	// hmipserver runs HMServer.jar, the HmIP devices are out of reach, their pairings stay.
	HmIPOff bool `json:"hmip_off"`
}

// HmIPMoveInfo says which module HmIP-RF leaves and which it goes to.
type HmIPMoveInfo struct {
	From     string `json:"from"`
	To       string `json:"to"`
	LocalKey bool   `json:"local_key"`
	Snapshot bool   `json:"snapshot"`
	// SnapshotBlocked: a snapshot of From from a switch to local key mode is kept (openccu-lite
	// B-301, maintainer 2026-10-03: the refusal stays) - the module-move snapshot cannot be taken
	// until it is discarded; the page explains it and offers the discard before the attempt.
	SnapshotBlocked bool `json:"snapshot_blocked,omitempty"`
	// ToVersion is the target module's application firmware, when the detection read it.
	ToVersion string `json:"to_version,omitempty"`
	// Refused: the change is refused (openccu-lite B-289) - the target's firmware cannot take the
	// network key and local key mode is off; Apply answers *HmIPFirmwareRefused.
	Refused *HmIPFirmwareRefused `json:"refused,omitempty"`
}

// HmIPFirmwareRefused refuses moving HmIP-RF onto a module whose application firmware is below
// radio.MinHmIPKeyExchangeVersion while local key mode is off (openccu-lite B-289, maintainer
// 2026-10-02): hmipserver would move the access point without the network key, the module could
// not send, and the key server refuses every exchange away from it. The way is the module's
// firmware update first, or local key mode. The same refusal guards the device import and the
// restore of a backup whose HmIP identity belongs to another module.
type HmIPFirmwareRefused struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Minimum string `json:"minimum"`
}

func (e *HmIPFirmwareRefused) Error() string {
	return fmt.Sprintf("module %s runs application firmware %s: below %s it cannot take the HmIP network's key, so HmIP-RF cannot move onto it while local key mode is off - update the module's firmware first", e.Module, e.Version, e.Minimum)
}

// HmIPFirmwareRefusal is the refusal for moving an HmIP network onto a module with this firmware,
// nil when the move may go ahead: in local key mode (offline, no key server), or with a version
// that is not known or not below the minimum.
func HmIPFirmwareRefusal(module, version string, localKey bool) *HmIPFirmwareRefused {
	if localKey || !radio.HmIPKeyExchangeUnsupported(version) {
		return nil
	}
	return &HmIPFirmwareRefused{Module: strings.ToUpper(module), Version: version, Minimum: radio.MinHmIPKeyExchangeVersion}
}

// RadioConnections is the service.
type RadioConnections struct {
	Root     Root
	Services ServiceManager
	Systemd  bool
	// Firmware is the flash service: no change while a flash runs, no flash while a change does.
	Firmware *RadioFirmware
	// BeforeHmIPMove takes the module-move snapshot before a change moves HmIP-RF to another
	// module (openccu-lite B-285: HmIPLocalKey.SnapshotBeforeMove); nil = no snapshot. An error
	// fails the change before anything is written.
	BeforeHmIPMove func(from string, prev radio.Choices) error
	// HmIPIdentity says whether the module's HmIP identity files are on the system (task 318: a
	// move away from a missing module takes the snapshot only when they are); nil = assume so.
	HmIPIdentity func(sgtin string) bool
	// LocalKeySnapshot says whether a snapshot of the module from a switch to local key mode is
	// kept (HmIPLocalKey.LocalKeySnapshotKept, B-301); nil = none.
	LocalKeySnapshot func(sgtin string) bool
	// MoveBackOffer says whether a module-move snapshot is kept (HmIPLocalKey.MoveBackOffer); the
	// status carries it for the page's "back to the previous module".
	MoveBackOffer func() *HmIPMoveBack
	// CounterGap is the open HmIP security counter gap after a way back (HmIPLocalKey.CounterGap,
	// openccu-lite B-302); nil = none.
	CounterGap func() *HmIPCounterGap
	// BidCosDevices lists rfd's paired devices; nil = not available.
	BidCosDevices func(ctx context.Context) ([]BidCosDevice, error)
	// Run executes commands for radio.Load (uname, hostname); nil = exec.
	Run      radio.Runner
	StateDir string
	Log      *slog.Logger
	Now      func() time.Time
	// Timeout bounds one change (the stop, the run and hmipserver's start with its readiness).
	Timeout time.Duration

	mu      sync.Mutex
	running *ConnApply
	last    *ConnApply
}

func (s *RadioConnections) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *RadioConnections) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Busy: a change is running (the flash service asks).
func (s *RadioConnections) Busy() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running != nil
}

// Load reads the last change and closes one that was in flight when occulited stopped: marked
// failed, and the radio units started - the daemons are never left stopped by a crash.
func (s *RadioConnections) Load(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last ConnApply
	if b, err := os.ReadFile(filepath.Join(s.StateDir, "last.json")); err == nil && json.Unmarshal(b, &last) == nil {
		s.last = &last
	}
	var stale ConnApply
	if b, err := os.ReadFile(filepath.Join(s.StateDir, "running.json")); err == nil && json.Unmarshal(b, &stale) == nil {
		now := s.now()
		stale.Finished, stale.OK = &now, false
		stale.Error = "occulited was restarted while the change was running; the radio daemons were started"
		s.last = &stale
		_ = writeJSONFile(filepath.Join(s.StateDir, "last.json"), &stale)
		_ = os.Remove(filepath.Join(s.StateDir, "running.json"))
		s.log().Warn("radio connections: a change was running when occulited stopped; starting the radio daemons")
		s.openGate(nil, false) // B-307: the interrupted change's hold on the radio units
		if s.Services != nil {
			for _, u := range append([]string{RadioDetectionUnit}, radioUnits...) {
				if _, err := s.Services.Control(ctx, u, "start"); err != nil {
					s.log().Warn("radio connections: start after the interrupted change failed", "unit", u, "err", err)
				}
			}
		}
	}
}

// read returns a file through the helper where occulited may not read it (rfd.conf is 0640
// root:rfd; hmip_user.conf lies in hmipserver's crRFD directory, 0750 since B-253), and whether it
// exists. A stat is no test here: in a closed directory it fails for a file that is there, and the
// choices read as automatic (B-271).
func (s *RadioConnections) read(p string) (string, bool) {
	c, ok, _ := readFileErr(s.Root.join(p))
	return c, ok
}

func (s *RadioConnections) detection() (radio.Detection, bool) {
	var det radio.Detection
	b, err := os.ReadFile(s.Root.join(filepath.Join(radio.RunDir, "modules.json")))
	if err != nil || json.Unmarshal(b, &det) != nil {
		return det, false
	}
	return det, true
}

// BootPlan is the plan the last radio run wrote, and false when there is none.
func (s *RadioConnections) BootPlan() (radio.Plan, bool) {
	if s == nil {
		return radio.Plan{}, false
	}
	return s.bootPlan()
}

func (s *RadioConnections) bootPlan() (radio.Plan, bool) {
	var p radio.Plan
	b, err := os.ReadFile(s.Root.join(filepath.Join(radio.RunDir, "plan.json")))
	if err != nil || json.Unmarshal(b, &p) != nil {
		return p, false
	}
	return p, true
}

func (s *RadioConnections) choices() radio.Choices {
	hu, _ := s.read(hmipUserConf)
	rc, _ := s.read(rfdConfPath)
	return radio.ReadChoices(hu, rc)
}

// Status assembles the answer.
func (s *RadioConnections) Status() RadioConnStatus {
	st := RadioConnStatus{Choices: s.choices(), Options: radio.Options{HmIP: []radio.Option{}, BidCos: []radio.Option{}}, Modules: []ConnModule{}}
	det, okDet := s.detection()
	p, okPlan := s.bootPlan()
	if okDet && okPlan {
		st.Available = true
		st.Options = radio.ChoiceOptions(det)
		st.Modules = connModules(det, p)
		st.HeaderSilent = headerSilent(det, st.Choices)
		cp := connPlan(p)
		st.Plan, st.Mode = &cp, p.Mode
		st.HmIPFatal = radio.ReadHmIPFatal(string(s.Root))
	}
	st.HBRFETH = s.hbRFETH(det)
	s.mu.Lock()
	st.Running, st.Last = copyApply(s.running), copyApply(s.last)
	// openccu-lite B-285: the way back after a module move, while its snapshot is kept
	if s.CounterGap != nil {
		st.HmIPCounterGap = s.CounterGap()
	}
	if s.MoveBackOffer != nil && st.Running == nil {
		if st.HmIPMoveBack = s.MoveBackOffer(); st.HmIPMoveBack != nil {
			st.Hostname = s.Root.Hostname()
		}
	}
	s.mu.Unlock()
	return st
}

func copyApply(a *ConnApply) *ConnApply {
	if a == nil {
		return nil
	}
	c := *a
	c.Lines = append([]string{}, a.Lines...)
	return &c
}

// plan makes the plan the choices would give, from the boot's detection and today's files.
func (s *RadioConnections) plan(ctx context.Context, c radio.Choices) (radio.Plan, error) {
	det, ok := s.detection()
	if !ok {
		return radio.Plan{}, ErrConnUnavailable
	}
	in := radio.Load(ctx, string(s.Root), s.Run, det)
	// the daemons' files are not all occulited's to read: through the helper
	in.RFDConf, in.RFDConfExists = s.read(rfdConfPath)
	in.HS485DConf, in.HS485DConfExists = s.read("/etc/config/hs485d.conf")
	in.HmIPUserConf, _ = s.read(hmipUserConf)
	in.IDs, in.IDsExists = s.read("/etc/config/ids")
	in.RFDConf = radio.SetBidCosChoice(in.RFDConf, c.BidCos)
	in.HmIPUserConf = radio.SetHmIPPath(radio.SetHmIPChoice(in.HmIPUserConf, c.HmIP), c.HmIPPath)
	return radio.MakePlan(in), nil
}

// validate: auto, none or a detected module that can carry the process.
func (s *RadioConnections) validate(c radio.Choices) error {
	det, ok := s.detection()
	if !ok {
		return ErrConnUnavailable
	}
	o := radio.ChoiceOptions(det)
	has := func(list []radio.Option, id string) bool {
		for _, x := range list {
			if strings.EqualFold(x.ID, id) || (x.SGTIN != "" && strings.EqualFold(x.SGTIN, id)) {
				return true
			}
		}
		return false
	}
	current := s.choices()
	switch {
	case c.HmIP == radio.ChoiceAuto, c.HmIP == current.HmIP, c.HmIP == radio.HmIPNone:
		// auto, unchanged (a pinned module that is missing now may stay pinned), or none: no HmIP,
		// HMServer.jar (occulited task 147)
	case !radio.ValidIdentity(c.HmIP):
		return fmt.Errorf("hmip: %q is not a module serial or SGTIN", c.HmIP)
	case !has(o.HmIP, c.HmIP):
		return fmt.Errorf("hmip: no detected module %s can carry HmIP", c.HmIP)
	}
	switch c.HmIPPath {
	case radio.ChoiceAuto:
	case radio.PathDirect, radio.PathMultimacd:
		if c.HmIP == radio.HmIPNone {
			return fmt.Errorf("hmip_path: HmIP is set to none, there is no path to choose")
		}
	default:
		return fmt.Errorf("hmip_path: %q is not direct, multimacd or automatic", c.HmIPPath)
	}
	switch {
	case c.BidCos == radio.ChoiceAuto, c.BidCos == radio.BidCosNone, c.BidCos == current.BidCos:
	case !radio.ValidIdentity(c.BidCos):
		return fmt.Errorf("bidcos: %q is not a module serial", c.BidCos)
	case !has(o.BidCos, c.BidCos):
		return fmt.Errorf("bidcos: no detected module %s can carry BidCos-RF", c.BidCos)
	}
	return nil
}

// Preview says what the choices would give.
func (s *RadioConnections) Preview(ctx context.Context, c radio.Choices) (ConnPreview, error) {
	if err := s.validate(c); err != nil {
		return ConnPreview{}, err
	}
	np, err := s.plan(ctx, c)
	if err != nil {
		return ConnPreview{}, err
	}
	if np.Conflict != "" {
		return ConnPreview{}, errors.New(np.Conflict)
	}
	pv := ConnPreview{Choices: c, Plan: connPlan(np), Restarts: []string{}, Devices: []BidCosDevice{}}
	cur, ok := s.bootPlan()
	if !ok {
		return pv, ErrConnUnavailable
	}
	cp := connPlan(cur)
	pv.Changed = cp.Multimacd.Run != pv.Plan.Multimacd.Run || cp.Multimacd.Node != pv.Plan.Multimacd.Node ||
		cp.RFD.Run != pv.Plan.RFD.Run || cp.RFD.Node != pv.Plan.RFD.Node || cp.HmIPServer.Node != pv.Plan.HmIPServer.Node ||
		cp.RFDLocal != pv.Plan.RFDLocal || cp.RFDUSBAdapter != pv.Plan.RFDUSBAdapter || cp.HmIPServerJar != pv.Plan.HmIPServerJar || c != s.choices()
	if pv.Changed {
		pv.Restarts = append(pv.Restarts, radioUnits...)
	}
	// openccu-lite B-285: HmIP-RF leaves one module for another - task 318: or leaves the missing
	// module it was kept on, which holds the HmIP network
	from := ""
	switch {
	case cp.HmIP != nil:
		from = cp.HmIP.SGTIN
	case cp.HmIPPin != "" && strings.EqualFold(cp.MissingHmIP, cp.HmIPPin):
		from = cp.HmIPPin
	}
	if from != "" && pv.Plan.HmIP != nil && pv.Plan.HmIP.SGTIN != "" && !strings.EqualFold(from, pv.Plan.HmIP.SGTIN) {
		hu, _ := s.read(hmipUserConf)
		local := radio.ReadLocalKey(hu).Enabled()
		// the snapshot needs the identity files of the module left; a missing module's that are not
		// on the system (never written) leave nothing to keep
		snap := !local && s.BeforeHmIPMove != nil && (cp.HmIP != nil || s.HmIPIdentity == nil || s.HmIPIdentity(from))
		pv.HmIPMove = &HmIPMoveInfo{From: strings.ToUpper(from), To: strings.ToUpper(pv.Plan.HmIP.SGTIN), LocalKey: local, Snapshot: snap,
			ToVersion: pv.Plan.HmIP.Version, Refused: HmIPFirmwareRefusal(pv.Plan.HmIP.SGTIN, pv.Plan.HmIP.Version, local)}
		pv.HmIPMove.SnapshotBlocked = snap && s.LocalKeySnapshot != nil && s.LocalKeySnapshot(from)
	}
	pv.HmIPOff = cp.HmIP != nil && pv.Plan.HmIPServerJar == radio.JarHMServer
	pv.BidCosLost = cp.localBidCos() && !pv.Plan.localBidCos()
	if pv.BidCosLost && s.BidCosDevices != nil {
		devs, err := s.BidCosDevices(ctx)
		if err != nil {
			pv.DevicesError = err.Error()
		}
		if devs != nil {
			pv.Devices = devs
		}
	}
	return pv, nil
}

// Apply writes the choices and re-plans the stack in the background. A change that takes the
// local BidCos-RF radio away while devices are paired, or that moves HmIP-RF to another module
// (D-120), needs confirm (a *ConfirmRequired otherwise).
func (s *RadioConnections) Apply(ctx context.Context, c radio.Choices, confirm bool) (*ConnApply, error) {
	if s.Firmware != nil && s.Firmware.Status().Running != nil {
		return nil, ErrConnApplyRunning
	}
	pv, err := s.Preview(ctx, c)
	if err != nil {
		return nil, err
	}
	// openccu-lite B-289: not onto a module that cannot take the network key
	if pv.HmIPMove != nil && pv.HmIPMove.Refused != nil {
		return nil, pv.HmIPMove.Refused
	}
	if pv.BidCosLost && !confirm && (len(pv.Devices) > 0 || pv.DevicesError != "") {
		return nil, &ConfirmRequired{Devices: pv.Devices, HmIPMove: pv.HmIPMove, HmIPOff: pv.HmIPOff}
	}
	// occulited task 147: HmIP off while it runs on a module - on the user's word
	if pv.HmIPOff && !confirm {
		return nil, &ConfirmRequired{Devices: []BidCosDevice{}, HmIPOff: true}
	}
	// openccu-lite B-301: the module-move snapshot is blocked by a kept local-key snapshot - refused
	// before anything starts, with the module named for the discard
	if pv.HmIPMove != nil && pv.HmIPMove.SnapshotBlocked {
		return nil, &SnapshotBlocked{SGTIN: pv.HmIPMove.From}
	}
	// openccu-lite task 317 (D-120): moving HmIP-RF rewrites its identity files - on the user's word
	if pv.HmIPMove != nil && !confirm {
		return nil, &ConfirmRequired{Devices: []BidCosDevice{}, HmIPMove: pv.HmIPMove}
	}
	s.mu.Lock()
	if s.running != nil {
		s.mu.Unlock()
		return nil, ErrConnApplyRunning
	}
	a := &ConnApply{Choices: c, Previous: s.choices(), Started: s.now(), Lines: []string{}}
	s.running = a
	s.mu.Unlock()
	// openccu-lite B-285: the snapshot before HmIP-RF leaves its module - a failure ends the
	// change before anything is written
	if pv.HmIPMove != nil && pv.HmIPMove.Snapshot {
		if err := s.BeforeHmIPMove(pv.HmIPMove.From, a.Previous); err != nil {
			err = fmt.Errorf("the snapshot of %s before the move: %w", pv.HmIPMove.From, err)
			s.finish(a, err)
			return copyApply(a), err
		}
		s.line(a, "the identity and device files of "+pv.HmIPMove.From+" kept in a snapshot: the way back to that module")
	}
	if err := s.writeChoices(a.Previous, c); err != nil {
		s.finish(a, err)
		return copyApply(a), err
	}
	s.line(a, fmt.Sprintf("choices written: HmIP %s (%s), BidCos-RF %s", orAuto(c.HmIP), orAuto(c.HmIPPath), orAuto(c.BidCos)))
	_ = writeJSONFile(filepath.Join(s.StateDir, "running.json"), a)
	// the answer's copy is taken before the attempt runs: from here on apply appends its lines
	s.mu.Lock()
	out := copyApply(a)
	s.mu.Unlock()
	go s.applyWith(a, nil)
	return out, nil
}

// ApplyRestore is the way back after a module move (openccu-lite B-285, HmIPLocalKey.MoveBack):
// a connection change to the choices from before the move, run to its end here, with restore
// called while the daemons are stopped - the previous module's identity and device files return
// before the detection runs and hmipserver starts on it. No module-move snapshot is taken (the
// move back is not a move away), and the BidCos confirmation is given: the choices were the
// system's own before.
func (s *RadioConnections) ApplyRestore(ctx context.Context, c radio.Choices, restore func() error) error {
	if s.Firmware != nil && s.Firmware.Status().Running != nil {
		return ErrConnApplyRunning
	}
	if _, err := s.Preview(ctx, c); err != nil {
		return err
	}
	s.mu.Lock()
	if s.running != nil {
		s.mu.Unlock()
		return ErrConnApplyRunning
	}
	a := &ConnApply{Choices: c, Previous: s.choices(), Started: s.now(), Lines: []string{}}
	s.running = a
	s.mu.Unlock()
	if err := s.writeChoices(a.Previous, c); err != nil {
		s.finish(a, err)
		return err
	}
	s.line(a, fmt.Sprintf("back to the previous module: choices written: HmIP %s (%s), BidCos-RF %s", orAuto(c.HmIP), orAuto(c.HmIPPath), orAuto(c.BidCos)))
	_ = writeJSONFile(filepath.Join(s.StateDir, "running.json"), a)
	s.applyWith(a, restore)
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Error != "" {
		return errors.New(a.Error)
	}
	return nil
}

func orAuto(v string) string {
	if v == "" {
		return "auto"
	}
	return v
}

// writeChoices writes the files whose choice changed.
func (s *RadioConnections) writeChoices(prev, c radio.Choices) error {
	if c.BidCos != prev.BidCos {
		conf, ok, err := readFileErr(s.Root.join(rfdConfPath))
		if err != nil {
			return fmt.Errorf("reading %s: %w", rfdConfPath, err)
		}
		if !ok {
			return fmt.Errorf("%s is missing", rfdConfPath)
		}
		if conf == "" {
			return fmt.Errorf("%s is empty or unreadable", rfdConfPath)
		}
		// 0640 root:rfd is restored by the run that follows
		if err := writeFileAtomic(s.Root.join(rfdConfPath), []byte(radio.SetBidCosChoice(conf, c.BidCos)), 0o640); err != nil {
			return fmt.Errorf("writing %s: %w", rfdConfPath, err)
		}
	}
	if c.HmIP != prev.HmIP || c.HmIPPath != prev.HmIPPath {
		// the file holds hmipserver's other lines too (the local key, the device key map): a
		// file that could not be read is not rewritten from nothing (B-271)
		conf, _, err := readFileErr(s.Root.join(hmipUserConf))
		if err != nil {
			return fmt.Errorf("reading %s: %w", hmipUserConf, err)
		}
		conf = radio.SetHmIPPath(radio.SetHmIPChoice(conf, c.HmIP), c.HmIPPath)
		if err := writeFileAtomic(s.Root.join(hmipUserConf), []byte(conf), hmipUserConfMode(conf)); err != nil {
			return fmt.Errorf("writing %s: %w", hmipUserConf, err)
		}
	}
	return nil
}

func (s *RadioConnections) line(a *ConnApply, l string) {
	s.mu.Lock()
	a.Lines = append(a.Lines, s.now().Format("15:04:05")+" "+l)
	s.mu.Unlock()
	s.log().Info("radio connections: " + l)
}

func (s *RadioConnections) finish(a *ConnApply, err error) {
	s.mu.Lock()
	now := s.now()
	a.Finished, a.OK = &now, err == nil
	if err != nil {
		a.Error = err.Error()
	}
	// the files settle before the change counts as finished: a reader that sees no running change
	// finds no running.json, and the next change's running.json is never removed by this one
	_ = writeJSONFile(filepath.Join(s.StateDir, "last.json"), copyApply(a))
	_ = os.Remove(filepath.Join(s.StateDir, "running.json"))
	s.running, s.last = nil, a
	s.mu.Unlock()
	if err != nil {
		s.log().Warn("radio connections: the change failed", "err", err)
	}
}

// applyWith is the orchestration: stop, restore (the way back after a module move, else nil),
// re-run the detection and the plan, start.
func (s *RadioConnections) applyWith(a *ConnApply, restore func() error) {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var err error
	defer func() { s.finish(a, err) }()
	// openccu-lite B-307: from here until its own starts are done no radio unit starts from
	// outside the change - an addon's Wants=, the addon supervisor, a Restart= would start it on
	// the old plan's files, or before multimacd. startAll lets each unit through right before its
	// start and opens the gate after the last; opened on every way out as well. The marker's own
	// deadline is the change's timeout and a minute.
	if gerr := s.closeGate(timeout + time.Minute); gerr != nil {
		s.line(a, "the radio units could not be held against starts from outside the change: "+gerr.Error())
	} else {
		defer s.openGate(a, false)
		s.line(a, "radio units held against starts from outside the change")
	}
	// the daemons stop in the reverse of the boot order: the run refuses while one holds a module
	for i := len(radioUnits) - 1; i >= 0; i-- {
		if _, cerr := s.Services.Control(ctx, radioUnits[i], "stop"); cerr != nil {
			err = fmt.Errorf("stopping %s: %w", radioUnits[i], cerr)
			s.startAll(ctx, a)
			return
		}
		s.line(a, radioUnits[i]+" stopped")
	}
	if restore != nil {
		if rerr := restore(); rerr != nil {
			err = fmt.Errorf("restoring the previous module's files: %w", rerr)
			s.line(a, "restoring the previous module's files failed: "+rerr.Error())
			s.startAll(ctx, a)
			return
		}
		s.line(a, "the previous module's identity and device files are back")
	}
	s.line(a, "re-running the radio detection and the plan")
	if _, rerr := s.Services.Control(ctx, RadioDetectionUnit, "restart"); rerr != nil {
		err = fmt.Errorf("the detection and plan did not run: %w", rerr)
		s.startAll(ctx, a)
		return
	}
	s.line(a, "detection and plan done")
	if serr := s.startAll(ctx, a); serr != nil {
		err = serr
		return
	}
	if p, ok := s.bootPlan(); ok {
		s.line(a, "now: "+summary(connPlan(p)))
	}
}

// startAll starts the radio units in boot order; a unit the plan does not need is skipped by its
// condition and ends inactive, not failed.
func (s *RadioConnections) startAll(ctx context.Context, a *ConnApply) error {
	var first error
	for _, u := range radioUnits {
		// B-307: the gate lets the unit through right before the change starts it, in boot order -
		// an outside start of rfd or hmipserver stays held until multimacd is up
		s.releaseGate(u)
		if _, err := s.Services.Control(ctx, u, "start"); err != nil {
			s.line(a, "starting "+u+" failed: "+err.Error())
			if first == nil {
				first = fmt.Errorf("%s did not start: %w", u, err)
			}
			continue
		}
		s.line(a, u+" started (or skipped: not needed by the plan)")
	}
	s.openGate(a, true)
	s.startHeldBack(ctx, a)
	return first
}

// gatedOthers are the radio units the change does not stop itself but the gate holds all the same
// (B-307): one that ended during the change and whose Restart= the gate refused is started again.
var gatedOthers = []string{"hs485d", "hmlangw"}

func (s *RadioConnections) startHeldBack(ctx context.Context, a *ConnApply) {
	for _, u := range startHeldBack(ctx, s.Services, s.Root, gatedOthers) {
		if u.err != nil {
			s.line(a, "starting "+u.unit+" (held back during the change) failed: "+u.err.Error())
			continue
		}
		s.line(a, u.unit+" started: its start during the change was held back")
	}
}

func (s *RadioConnections) closeGate(limit time.Duration) error { return closeRadioGate(s.Root, limit) }

func (s *RadioConnections) releaseGate(unit string) { releaseRadioGate(s.Root, unit, s.log()) }

// openGate removes the marker; logged (a != nil and say) the first time it actually goes.
func (s *RadioConnections) openGate(a *ConnApply, say bool) {
	gone, err := openRadioGate(s.Root, s.log())
	switch {
	case err != nil && a != nil:
		s.line(a, "the radio units' hold could not be lifted: "+err.Error())
	case gone && say && a != nil:
		s.line(a, "the change's starts are done: radio units may start from outside it again")
	}
}

// summary is one line per plan: which daemon runs on what.
func summary(c ConnPlan) string {
	var parts []string
	for _, d := range []struct {
		name string
		d    ConnDaemon
	}{{"multimacd", c.Multimacd}, {"rfd", c.RFD}, {"hmipserver", c.HmIPServer}} {
		switch {
		case !d.d.Run:
			parts = append(parts, d.name+" off")
		case d.d.Node != "":
			parts = append(parts, d.name+" on "+d.d.Node)
		default:
			parts = append(parts, d.name+" on")
		}
	}
	return strings.Join(parts, ", ")
}

// OverlayServiceNotes gives each skipped interface daemon the plan's reason, so the Services page
// says why it is off instead of only that a condition was not met.
func (s *RadioConnections) OverlayServiceNotes(list []Service) []Service {
	if s == nil {
		return list
	}
	p, ok := s.bootPlan()
	if !ok {
		return list
	}
	why := map[string]radio.Daemon{"multimacd": p.Multimacd, "rfd": p.RFD, "hmipserver": p.HmIPServer, "hs485d": p.HS485D, "hmlangw": p.Hmlangw}
	for i := range list {
		if d, ok := why[list[i].ID]; ok && list[i].Skipped && !d.Run && d.Reason != "" {
			list[i].Note = d.Reason
		}
	}
	return list
}
