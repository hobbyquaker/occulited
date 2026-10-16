package firmware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

// Service runs the fetch on a schedule and on demand, and holds the state the UI shows (D-27).
// It downloads and deploys bundles; installing onto a device stays a user action in the frontend.
type Service struct {
	Client     *Client
	Dir        string // /etc/config/firmware
	Interfaces func() []interfaces.Interface
	Interval   time.Duration // default 24 h, jittered by up to an hour
	// StartDelay is how long after the start a due run waits (the network, the interfaces): a run
	// is due when the switch is on and the last one is older than Interval, or there was none
	// (B-195). Default 5 min.
	StartDelay time.Duration
	// RetryAfter is when a failed run is tried again, instead of a whole interval later. Default 1 h.
	RetryAfter time.Duration
	// SystemVersion is the system's VERSION; a fetched bundle asking for more is refused (DeployFor).
	SystemVersion string
	Log           *slog.Logger

	// The live half (occulited task 44): Journal follows hmipserver's journal for the transfer
	// lines (nil = no journal); JournalUnitName overrides the unit (tests); Changed is told every
	// device whose state or transfer changed, at most once per device per NotifyEvery; LiveFast
	// and LiveSlow override the poll rates; Now the clock (tests).
	Journal         StreamRunner
	JournalUnitName string
	Changed         func(Change)
	NotifyEvery     time.Duration
	LiveFast        time.Duration
	LiveSlow        time.Duration
	Now             func() time.Time
	// HeldDirOverride is the held directory (occulited task 45); "" = HeldDir(Dir)
	HeldDirOverride string
	// Archive is the OpenCCU firmware archive older bundles come from (occulited task 49); nil =
	// NewArchive() on first use
	Archive *Archive

	mu      sync.Mutex
	enabled bool
	running bool
	state   State
	wake    chan struct{} // re-plan (the switch changed)
	trigger chan struct{} // run now
	// statePath keeps the last run across restarts (Open); empty = memory only
	statePath string
	// the live half: the reads asked for, the fast rate's end, the journal's transfers, the
	// notifier's last send per device and what it holds back
	refresh    chan string
	fastUntil  time.Time
	transfers  Transfers
	notifyLast map[string]time.Time
	notifyHeld map[string]Change
	// occulited task 45: the explicitly held types (type key) and the global switch (task 48:
	// off unless the user switched it on)
	held         map[string]bool
	autoDeployOn bool
	// occulited task 49: per type key the version delivered before the current one (kept in the
	// held directory, the way back) and the types whose delivered version the user chose below
	// the newest on the system (the check keeps every new download of them held)
	prev   map[string]string
	pinned map[string]bool
	// bidcosDelivered: the held BidCos bundles were moved out once (occulited task 63)
	bidcosDelivered bool
}

// State is what the last run produced.
type State struct {
	Enabled     bool              `json:"enabled"`
	Running     bool              `json:"running"`
	LastRun     *time.Time        `json:"last_run,omitempty"`
	NextRun     *time.Time        `json:"next_run,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
	LastResult  []TypeResult      `json:"last_result"`
	Devices     []DeviceStatus    `json:"devices"`
	Deployed    []Bundle          `json:"deployed"`
	IndexSize   int               `json:"index_size"`
	IndexErrors map[string]string `json:"interface_errors,omitempty"`
	// LiveAt is when the devices' states were last read from the interfaces outside a check
	// (occulited task 44); Transfers counts what is going on
	LiveAt    *time.Time     `json:"live_at,omitempty"`
	Transfers TransferCounts `json:"transfers"`
	// AutoDeploy is the global switch (occulited task 45): a downloaded bundle is offered at
	// once (true) or held until the user delivers it (false, the default since task 48); HeldTypes are the types held by
	// their own switch (type keys). Deployed lists the held bundles too, marked held.
	AutoDeploy bool     `json:"auto_deploy"`
	HeldTypes  []string `json:"held_types,omitempty"`
	// PinnedTypes are the types whose delivered version the user chose below the newest on the
	// system (occulited task 49): a new download of them stays held
	PinnedTypes []string `json:"pinned_types,omitempty"`
}

// TypeResult is the outcome for one device type in a run.
type TypeResult struct {
	Type    string `json:"type"`
	Version string `json:"version"`
	// VersionFromName and DateFromName: a deployed or downloaded bundle's Bundle.VersionFromName
	// and DateFromName, when its info states no version.
	VersionFromName string `json:"version_from_name,omitempty"`
	DateFromName    string `json:"date_from_name,omitempty"`
	Action          string `json:"action"` // downloaded, deployed, current, failed, pruned
	Detail          string `json:"detail,omitempty"`
	// Held: the bundle went into (or stays in) the held directory - the type is held or the
	// global switch is off (occulited task 45)
	Held bool `json:"held,omitempty"`
}

// DeviceStatus is a paired device with what the interface says is available and what eQ-3 lists.
type DeviceStatus struct {
	interfaces.Device
	// Latest is eQ-3's version for the device's type, from the index (B-195): shown per device, so
	// an update is visible before its bundle is deployed.
	Latest string `json:"latest,omitempty"`
	// NotListed: the index was read and has no entry for the type.
	NotListed       bool `json:"not_listed,omitempty"`
	UpdateAvailable bool `json:"update_available"`
	// Transfer is the background transfer hmipserver's journal reported for the device, while
	// the interface says it is in a transfer state (occulited task 44); nil otherwise
	Transfer *Transfer `json:"transfer,omitempty"`
}

// NewService returns a service; call Run in a goroutine and Enable/Disable as configured.
func NewService(c *Client, dir string, ifs func() []interfaces.Interface, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Client: c, Dir: dir, Interfaces: ifs, Interval: 24 * time.Hour, StartDelay: 5 * time.Minute, RetryAfter: time.Hour, Log: log, wake: make(chan struct{}, 1), trigger: make(chan struct{}, 1), state: State{LastResult: []TypeResult{}, Devices: []DeviceStatus{}, Deployed: []Bundle{}}}
}

// persisted is what the state file keeps: the last run's outcome, so the page is not empty after
// a restart and a due run is known (B-195).
type persisted struct {
	LastRun     *time.Time        `json:"last_run,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
	LastResult  []TypeResult      `json:"last_result"`
	Devices     []DeviceStatus    `json:"devices"`
	IndexSize   int               `json:"index_size"`
	IndexErrors map[string]string `json:"interface_errors,omitempty"`
	// Transfers are the journal's entries per device address (occulited task 44), so a restart
	// keeps the percent a transfer stood at
	Transfers Transfers `json:"transfers,omitempty"`
	// Held and AutoDeploy are the switches of occulited task 45. Task 48 made the global one off
	// by default on every system: the key is new (task 45 kept "auto_deploy_off", which is no
	// longer read), so an update switches every system to off once, and only a user's own "on"
	// after it is kept.
	Held       map[string]bool `json:"held,omitempty"`
	AutoDeploy bool            `json:"auto_deploy,omitempty"`
	// Previous and Pinned are task 49's: the version delivered before, the chosen older ones
	Previous map[string]string `json:"previous,omitempty"`
	Pinned   map[string]bool   `json:"pinned,omitempty"`
	// BidCosDelivered: DeliverHeldBidCos has run (occulited task 63)
	BidCosDelivered bool `json:"bidcos_delivered,omitempty"`
}

// Open sets the state file and reads the last run from it; a missing or unreadable file is a
// service that has not run yet.
func (s *Service) Open(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statePath = path
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.Log.Warn("firmware: state not read", "path", path, "err", err)
		}
		return
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		s.Log.Warn("firmware: state not read", "path", path, "err", err)
		return
	}
	s.state.LastRun, s.state.LastError, s.state.IndexSize, s.state.IndexErrors = p.LastRun, p.LastError, p.IndexSize, p.IndexErrors
	if p.LastResult != nil {
		s.state.LastResult = p.LastResult
	}
	if p.Devices != nil {
		s.state.Devices = p.Devices
	}
	if p.Transfers != nil {
		s.transfers = p.Transfers
	}
	s.held, s.autoDeployOn, s.prev, s.pinned, s.bidcosDelivered = p.Held, p.AutoDeploy, p.Previous, p.Pinned, p.BidCosDelivered
}

// save writes the state file (caller holds mu).
func (s *Service) save() {
	if s.statePath == "" {
		return
	}
	st := s.state
	raw, err := json.MarshalIndent(persisted{LastRun: st.LastRun, LastError: st.LastError, LastResult: st.LastResult, Devices: st.Devices, IndexSize: st.IndexSize, IndexErrors: st.IndexErrors, Transfers: s.transfers, Held: s.held, AutoDeploy: s.autoDeployOn, Previous: s.prev, Pinned: s.pinned, BidCosDelivered: s.bidcosDelivered}, "", "  ")
	if err == nil {
		tmp := s.statePath + ".tmp"
		if err = os.WriteFile(tmp, raw, 0o640); err == nil {
			err = os.Rename(tmp, s.statePath)
		}
	}
	if err != nil {
		s.Log.Warn("firmware: state not saved", "path", s.statePath, "err", err)
	}
}

// SetEnabled switches the scheduled run; a manual Check works either way.
func (s *Service) SetEnabled(on bool) {
	s.mu.Lock()
	s.enabled = on
	s.state.Enabled = on
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Enabled reports the switch.
func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// Status returns a copy of the state with the deployed bundles refreshed from disk, each
// device's transfer from the journal attached, and the transfers counted.
func (s *Service) Status() State {
	s.mu.Lock()
	st := s.state
	now := s.now()
	devs := make([]DeviceStatus, 0, len(st.Devices))
	for _, d := range st.Devices {
		d.Transfer = s.transferFor(d.Interface, d.Address, now)
		devs = append(devs, d)
	}
	st.AutoDeploy = s.autoDeploy()
	st.HeldTypes = s.heldTypes()
	st.PinnedTypes = sortedKeys(s.pinned)
	s.mu.Unlock()
	st.Devices = devs
	st.Transfers = countTransfers(devs)
	if dep, err := Deployed(s.Dir); err == nil {
		st.Deployed = dep
	}
	if held, err := Held(s.heldDir()); err == nil && len(held) > 0 {
		st.Deployed = append(st.Deployed, held...)
	}
	return st
}

// BundleFile is one text file of a deployed bundle (ReadBundleFile on the firmware directory),
// or of a held one when the firmware directory has no bundle of that name (task 48: a held
// bundle's changelog is what the user reads before delivering it).
func (s *Service) BundleFile(typeCode, name string) ([]byte, error) {
	raw, err := ReadBundleFile(s.Dir, typeCode, name)
	if errors.Is(err, ErrNoSuchFile) {
		if _, statErr := os.Lstat(filepath.Join(s.Dir, typeCode)); os.IsNotExist(statErr) {
			return ReadBundleFile(s.heldDir(), typeCode, name)
		}
	}
	return raw, err
}

// Trigger asks for a run now.
func (s *Service) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run is the scheduler loop: sleeps until the next slot or a trigger, never runs two at once.
// A run is due Interval (plus up to an hour) after the last one, a failed one RetryAfter after
// it; a run that came due while occulited was not running - or never ran - waits StartDelay
// after the start (the network may not be up yet), so a restart neither skips nor delays a day.
func (s *Service) Run(ctx context.Context) {
	s.DeliverHeldBidCos()
	for {
		s.mu.Lock()
		enabled := s.enabled
		last, failed := s.state.LastRun, s.state.LastError != ""
		s.mu.Unlock()
		var next time.Duration
		if enabled {
			next = s.StartDelay
			if last != nil {
				gap := s.Interval + time.Duration(rand.Int64N(int64(time.Hour)))
				if failed && s.RetryAfter > 0 && s.RetryAfter < gap {
					gap = s.RetryAfter
				}
				if due := time.Until(last.Add(gap)); due > next {
					next = due
				}
			}
			at := time.Now().Add(next)
			s.mu.Lock()
			s.state.NextRun = &at
			s.mu.Unlock()
		} else {
			next = 365 * 24 * time.Hour
			s.mu.Lock()
			s.state.NextRun = nil
			s.mu.Unlock()
		}
		timer := time.NewTimer(next)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if s.Enabled() {
				s.Check(ctx)
			}
		case <-s.trigger:
			timer.Stop()
			s.Check(ctx)
		case <-s.wake:
			timer.Stop()
			// the switch changed: re-plan only
		}
	}
}

// Check performs one run and records the state. Safe to call concurrently; a second call while
// one runs returns immediately.
func (s *Service) Check(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.state.Running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.state.Running = false
		s.mu.Unlock()
	}()
	now := time.Now()
	res := State{Enabled: s.Enabled(), LastRun: &now, LastResult: []TypeResult{}, Devices: []DeviceStatus{}}
	var planned, deployedTypes, failedTypes, heldTypes []string
	finish := func(err error) {
		if err != nil {
			res.LastError = err.Error()
			s.Log.Warn("firmware: run failed", "devices", len(res.Devices), "index", res.IndexSize, "err", err)
		} else {
			// one line per run (B-195): a run that deploys nothing is visible in the journal too
			s.Log.Info("firmware: run", "devices", len(res.Devices), "index", res.IndexSize, "planned", planned, "deployed", deployedTypes, "held", heldTypes, "failed", failedTypes)
		}
		s.mu.Lock()
		res.NextRun = s.state.NextRun
		s.state = res
		s.save()
		s.mu.Unlock()
	}

	ifs := s.Interfaces()
	devs, ierrs := interfaces.Devices(ctx, ifs, 20*time.Second)
	if len(ierrs) > 0 {
		res.IndexErrors = map[string]string{}
		for k, e := range ierrs {
			res.IndexErrors[k] = e.Error()
		}
	}
	status := func(byKey Index) {
		res.Devices = res.Devices[:0]
		for _, d := range devs {
			ds := DeviceStatus{Device: d, UpdateAvailable: d.AvailableFirmware != "" && CompareVersions(d.AvailableFirmware, d.Firmware) > 0}
			if d.Updatable != nil && !*d.Updatable {
				// B-225: the interface says the device cannot be updated over the air; the page says
				// so instead of "not in eQ-3's list"
				ds.UpdateAvailable = false
				res.Devices = append(res.Devices, ds)
				continue
			}
			if byKey != nil {
				if e, ok := byKey.Match(d.Type); ok {
					ds.Latest = e.Version
					if e.Version != "" && Newer(e.Version, d.Firmware) {
						ds.UpdateAvailable = true
					}
				} else {
					ds.NotListed = true
				}
			}
			res.Devices = append(res.Devices, ds)
		}
	}
	status(nil)
	if len(devs) == 0 {
		finish(fmt.Errorf("no devices reported by any interface (%d interfaces)", len(ifs)))
		return
	}
	index, err := s.Client.Index(ctx)
	if err != nil {
		finish(err)
		return
	}
	res.IndexSize = len(index)
	byKey := ByKey(index)
	status(byKey)
	plan := Plan(index, toPlanDevices(devs))
	matched := map[string]Entry{} // index key -> the entry, for every paired type the index lists
	bidcos := map[string]bool{}   // index key -> a device of the type is on BidCos-RF (task 63)
	for _, d := range devs {
		if d.Updatable != nil && !*d.Updatable {
			continue
		}
		if e, ok := byKey.Match(d.Type); ok {
			matched[TypeKey(e.Type)] = e
			if d.Interface == "BidCos-RF" {
				bidcos[TypeKey(e.Type)] = true
			}
		}
	}
	tmp, err := os.MkdirTemp("", "occulite-fw-*")
	if err != nil {
		finish(err)
		return
	}
	defer os.RemoveAll(tmp)
	deployed := 0
	have := map[string]Bundle{} // type key -> the bundle on the box, offered or held
	if dep, err := Deployed(s.Dir); err == nil {
		for _, b := range dep {
			have[TypeKey(b.Name)] = b
		}
	}
	if held, err := Held(s.heldDir()); err == nil {
		for _, b := range held {
			if cur, ok := have[TypeKey(b.Name)]; !ok || CompareVersions(b.Version, cur.Version) > 0 {
				have[TypeKey(b.Name)] = b
			}
		}
	}
	inPlan := map[string]bool{}
	for _, e := range plan {
		key := TypeKey(e.Type)
		inPlan[key] = true
		planned = append(planned, e.Type+" "+e.Version)
		// occulited task 45: where the type's bundle belongs - held back by its switch or the
		// global one, or offered to the interfaces; a BidCos-RF type's is offered at once (task 63)
		s.mu.Lock()
		target, heldBack := s.target(key, bidcos[key] || BidCosType(e.Type))
		s.mu.Unlock()
		if d, ok := have[key]; ok && CompareVersions(d.Version, e.Version) >= 0 {
			// the bundle is on the box already; the device is just not updated yet (the user's call)
			r := TypeResult{Type: e.Type, Version: d.Version, VersionFromName: d.VersionFromName, DateFromName: d.DateFromName, Action: "deployed", Held: d.Held}
			if d.Held && !heldBack {
				// held while the global switch was off, now to be offered
				if err := s.offerHeld(d); err != nil {
					r.Detail = "offer: " + err.Error()
				} else {
					r.Held = false
					deployed++
					deployedTypes = append(deployedTypes, e.Type+" "+d.Version+" ("+d.Dir+", offered)")
				}
			}
			res.LastResult = append(res.LastResult, r)
			continue
		}
		name, _, err := s.Client.Download(ctx, e.Type, tmp)
		if err != nil {
			failedTypes = append(failedTypes, e.Type)
			res.LastResult = append(res.LastResult, TypeResult{Type: e.Type, Version: e.Version, Action: "failed", Detail: err.Error()})
			continue
		}
		// the target may not exist yet (deploy unpacks beside it): the held directory, or the
		// firmware directory on a system that never had one
		if err := Priv.MkdirAll(target, 0o755); err != nil {
			failedTypes = append(failedTypes, e.Type)
			res.LastResult = append(res.LastResult, TypeResult{Type: e.Type, Version: e.Version, Action: "failed", Detail: "deploy: " + err.Error()})
			continue
		}
		deployTo := DeployFor
		if heldBack {
			deployTo = deployHeld
		}
		b, err := deployTo(filepath.Join(tmp, name), target, s.SystemVersion)
		if err != nil {
			failedTypes = append(failedTypes, e.Type)
			res.LastResult = append(res.LastResult, TypeResult{Type: e.Type, Version: e.Version, Action: "failed", Detail: "deploy: " + err.Error()})
			continue
		}
		if heldBack {
			// a newer bundle of a held type replaces the older held ones; the offered one, if
			// any, stays as it is (the delivery is the user's)
			s.pruneHeld(TypeKey(b.Name))
			heldTypes = append(heldTypes, e.Type+" "+b.Version+" ("+b.Dir+")")
		} else {
			deployed++
			deployedTypes = append(deployedTypes, e.Type+" "+b.Version+" ("+b.Dir+")")
		}
		res.LastResult = append(res.LastResult, TypeResult{Type: e.Type, Version: b.Version, VersionFromName: b.VersionFromName, DateFromName: b.DateFromName, Action: "downloaded", Detail: name, Held: heldBack})
	}
	for k, e := range matched {
		if !inPlan[k] {
			res.LastResult = append(res.LastResult, TypeResult{Type: e.Type, Version: e.Version, Action: "current"})
		}
	}
	sort.Slice(res.LastResult, func(i, j int) bool { return res.LastResult[i].Type < res.LastResult[j].Type })
	if deployed > 0 {
		// only the interfaces with devices, and of those BidCos-RF and HmIP-RF (RefreshFirmware):
		// VirtualDevices and hs485d know no refreshDeployedDeviceFirmwareList
		withDevices := map[string]bool{}
		for _, d := range devs {
			withDevices[d.Interface] = true
		}
		var refresh []interfaces.Interface
		for _, i := range ifs {
			if withDevices[i.Name] {
				refresh = append(refresh, i)
			}
		}
		for name, err := range interfaces.RefreshFirmware(refresh) {
			s.Log.Warn("firmware: refresh failed", "interface", name, "err", err)
		}
	}
	finish(nil)
}

// DeployUpload deploys a manually uploaded bundle and refreshes BidCos-RF and HmIP-RF (B-225:
// interfaces.RefreshFirmware leaves hs485d and VirtualDevices alone).
func (s *Service) DeployUpload(path string) (*Bundle, error) {
	b, err := Deploy(path, s.Dir)
	if err != nil {
		return nil, err
	}
	for name, err := range interfaces.RefreshFirmware(s.Interfaces()) {
		s.Log.Warn("firmware: refresh failed", "interface", name, "err", err)
	}
	return b, nil
}

func toPlanDevices(devs []interfaces.Device) []Device {
	out := make([]Device, 0, len(devs))
	for _, d := range devs {
		out = append(out, Device{Type: d.Type, Firmware: d.Firmware, NotUpdatable: d.Updatable != nil && !*d.Updatable})
	}
	return out
}
