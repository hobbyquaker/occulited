package httpapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/hobbyquaker/occulited/internal/radio"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/backuptarget"
	"github.com/hobbyquaker/occulited/internal/catalog"
	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/interfaces"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/unitshow"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// Task 81: the Status page's warnings are evaluated here, not in the browser, so that a silence
// can end when its warning clears even while nobody has the page open. The sentences stay in the
// UI, keyed by id and variant; each source below says when its warning is active and what the
// sentence needs.

// WarningTracker is the tracker type SystemAPI carries.
type WarningTracker = warnings.Tracker

// processStart names this run of the daemon: a recovery of the metadata store belongs to it.
var processStart = time.Now()

// keyStateTTL is how long the security key's state is reused - crypttool through the helper,
// and the Status page reads the warnings every half minute.
const keyStateTTL = 30 * time.Second

// warningsState is the API's own state for the warnings.
type warningsState struct {
	keyMu    sync.Mutex
	keyAt    time.Time
	keySet   bool
	keyKnown bool
	keyOK    bool

	ownershipOnce sync.Once
	ownership     *system.AddonOwnership

	// the boot gate (B-107, bootSettled): settled once seen, asked again at most every bootRecheck
	bootMu      sync.Mutex
	bootDone    bool
	bootAskedAt time.Time

	// whether INPUT jumps to lite-input (B-152, task 157), cached like the key: two iptables runs
	// through the helper
	fwMu        sync.Mutex
	fwAt        time.Time
	fwLoaded    map[string]bool
	fwAvailable bool

	// the radio load's warning (openccu-lite task 316): which kinds are above their threshold now,
	// kept for the hysteresis
	loadMu sync.Mutex
	loadOn map[string]bool

	// test seams: crypttool's answer, the radio sampler's last poll and the INPUT policies; nil =
	// the real ones
	keyState func(ctx context.Context) (set, known bool, err error)
	rfdPoll  func() (time.Time, []interfaces.RadioInterface)
	// the BidCos-RF devices of the service-message sweep (occulited task 89)
	paired func() (int, bool)
	fwRead func(ctx context.Context) (map[string]bool, bool, error)
	// the interfaces with a device firmware transfer on its way (occulited task 61)
	transfers func() map[string]bool
	// the radio plan of the boot, which says which entries share one radio (occulited B-83)
	plan func() (radio.Plan, bool)
}

// forgetFirewall drops the cached reading after the firewall was loaded.
func (s *warningsState) forgetFirewall() {
	s.fwMu.Lock()
	s.fwAt = time.Time{}
	s.fwMu.Unlock()
}

// firewall answers per family whether the rules are loaded, cached for keyStateTTL; a failed read
// is not remembered.
func (s *warningsState) firewall(ctx context.Context) (loaded map[string]bool, available, ok bool) {
	s.fwMu.Lock()
	defer s.fwMu.Unlock()
	if !s.fwAt.IsZero() && time.Since(s.fwAt) < keyStateTTL {
		return s.fwLoaded, s.fwAvailable, true
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	read := system.FirewallLoaded
	if s.fwRead != nil {
		read = s.fwRead
	}
	loaded, available, err := read(ctx)
	if err != nil {
		return nil, true, false
	}
	s.fwLoaded, s.fwAvailable, s.fwAt = loaded, available, time.Now()
	return loaded, available, true
}

// forgetKey drops the cached key state after the key was set.
func (s *warningsState) forgetKey() {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	s.keyAt = time.Time{}
}

// key answers the security key's state, cached for keyStateTTL. A browser that goes away does not
// end the read: a cancelled crypttool is a failed read, and a failed read is not remembered.
func (s *warningsState) key(ctx context.Context, root system.Root) (set, known, ok bool) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if !s.keyAt.IsZero() && time.Since(s.keyAt) < keyStateTTL {
		return s.keySet, s.keyKnown, s.keyOK
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	read := root.SecurityKeyState
	if s.keyState != nil {
		read = s.keyState
	}
	set, known, err := read(ctx)
	if err != nil {
		return false, true, false
	}
	s.keySet, s.keyKnown, s.keyOK, s.keyAt = set, known, true, time.Now()
	return set, known, true
}

// addonOwnership is B-92's check, made on first use; nil without the systemd addon manager.
func (a *SystemAPI) addonOwnership() *system.AddonOwnership {
	a.warn.ownershipOnce.Do(func() {
		if sa := a.policyManager(); sa != nil {
			a.warn.ownership = &system.AddonOwnership{Addons: sa}
		}
	})
	return a.warn.ownership
}

// WarningTracker builds the tracker on this API's services and keeps it on a.Warnings; the caller
// runs it. file keeps the silences ("" = memory only).
func (a *SystemAPI) WarningTracker(file string, log *slog.Logger) *WarningTracker {
	a.Warnings = &warnings.Tracker{File: file, Sources: a.warningSources(), Log: log, Settled: a.bootSettled}
	if sa := a.policyManager(); sa != nil {
		sa.AfterStart = func(ids []string) { go a.recheckOwnership(ids) }
	}
	return a.Warnings
}

// ownershipRecheck is how long after an install or a policy switch started a confined addon B-92's
// check looks at its files again (B-106).
var ownershipRecheck = 10 * time.Second

// recheckOwnership: a moment after the addons ran up as their users, B-92's check looks at their
// files once more and the warnings are evaluated, so a root-owned file something left anyway is the
// Status page's warning - and the LED's - at once, not only once the hour's result has run out.
func (a *SystemAPI) recheckOwnership(ids []string) {
	time.Sleep(ownershipRecheck)
	if o := a.addonOwnership(); o != nil && len(ids) > 0 {
		o.Check(ids...)
	}
	if a.Warnings != nil {
		a.Warnings.Evaluate(context.Background())
	}
}

// bootSettleCap is how long after the kernel started a boot counts as settled whatever systemd says:
// a unit that never finishes starting (B-109's loops) must not keep every silence for good.
const bootSettleCap = 15 * time.Minute

// bootRecheck is how often a boot that has not settled is asked about again.
const bootRecheck = 10 * time.Second

// bootSettled is the tracker's boot gate (B-107): the box has finished starting - systemd's manager
// has its FinishTimestamp, as the boot timeline reads it (task 93) - or it has been up for
// bootSettleCap. Before that a warning that reads as clear may only not be set up yet: the backup
// target's USB stick is mounted by udev, the journal's boot script is not ordered before occulited,
// and occulited no longer waits for the radio detection (task 94). Without systemd (a development
// root, the tests) the boot is settled; once settled it stays so.
func (a *SystemAPI) bootSettled() bool {
	s := &a.warn
	s.bootMu.Lock()
	defer s.bootMu.Unlock()
	if s.bootDone {
		return true
	}
	if a.BootChart == nil || a.BootChart.Systemctl == nil {
		s.bootDone = true
		return true
	}
	if up, ok := a.Root.Uptime(); ok && up >= bootSettleCap {
		s.bootDone = true
		return true
	}
	if !s.bootAskedAt.IsZero() && time.Since(s.bootAskedAt) < bootRecheck {
		return false
	}
	s.bootAskedAt = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := a.BootChart.Systemctl(ctx, "show", "-p", "FinishTimestampMonotonic")
	if err != nil {
		return false
	}
	for _, b := range unitshow.Blocks(out) {
		if unitshow.Micros(b, "FinishTimestampMonotonic") > 0 {
			s.bootDone = true
		}
	}
	return s.bootDone
}

// warningSources are the warnings in the order the page lists them within a severity.
func (a *SystemAPI) warningSources() []warnings.Source {
	return []warnings.Source{
		{IDs: []string{"addon-payload", "addon-ended", "addon-failed", "rega", "arch"}, Lists: true, Eval: a.addonWarnings},
		{IDs: []string{"crash-loop"}, Lists: true, Eval: a.crashLoopWarning},
		{IDs: []string{"occulited-crash-loop"}, Eval: a.occulitedCrashLoopWarning},
		{IDs: []string{"meta"}, Eval: a.metaWarning},
		{IDs: []string{"unclean"}, Eval: a.uncleanWarning},
		{IDs: []string{"backup-target", "backup-userfs"}, Eval: a.backupWarnings},
		{IDs: []string{"backup-delivery"}, Eval: a.backupDeliveryWarnings},
		{IDs: []string{"backup-unencrypted"}, Eval: a.backupUnencryptedWarning},
		{IDs: []string{"journal-target", "journal-sync"}, Eval: a.journalWarnings},
		{IDs: []string{"store-target"}, Eval: a.storeWarnings},
		{IDs: []string{"certificate"}, Eval: a.certificateWarning},
		{IDs: []string{"storage"}, Eval: a.storageWarning},
		{IDs: []string{"addon-ownership"}, Lists: true, Eval: a.ownershipWarning},
		{IDs: []string{"security-key"}, Eval: a.securityKeyWarning},
		{IDs: []string{"hmip-adapter"}, Eval: a.hmipAdapterWarning},
		{IDs: []string{"hmip-local-swap"}, Eval: a.hmipLocalSwapWarning},
		{IDs: []string{"hmip-module-missing"}, Eval: a.hmipModuleMissingWarning},
		// openccu-lite task 299: the HmIP security counter near, past or below its wrap, and the
		// hold of hmipserver for a trusted clock
		{IDs: []string{"hmip-security-counter", "hmip-clock-hold"}, Eval: a.hmipCounterWarnings},
		{IDs: []string{"devices-import"}, Eval: a.devicesImportWarning},
		{IDs: []string{"hb-rf-eth"}, Eval: a.hbRFETHWarning},
		// openccu-lite task 316: the radio's load high, separate from the LED's levels
		{IDs: []string{"radio-load"}, Eval: a.radioLoadWarning},
		{IDs: []string{"radio-module-unusable", "radio-firmware"}, Eval: a.radioFirmwareWarnings},
		{IDs: []string{"addon-update"}, Eval: a.addonUpdateWarning},
		{IDs: []string{"catalog-check"}, Eval: a.catalogCheckWarning}, // occulited B-52
		{IDs: []string{"rpc-stalled"}, Eval: a.rpcStallWarnings},
		{IDs: []string{"firewall"}, Eval: a.firewallWarning},
		{IDs: []string{"classic-rpc-open"}, Eval: a.classicRPCWarning},
		{IDs: []string{"app-public"}, Eval: a.appPublicWarning},
		// openccu-lite task 231: a server presents a CA occulited's store lacks (strict)
		{IDs: []string{"trust-ca"}, Lists: true, Eval: a.trustWarnings},
		{IDs: []string{"trust-pin"}, Eval: a.pinWarnings}, // openccu-lite task 232
		{IDs: []string{"hmip-port-open"}, Eval: a.hmipPortWarning},
		{IDs: []string{"hmip-local-key"}, Eval: a.hmipLocalKeyWarning},
		{IDs: []string{"hmip-key-declined"}, Eval: a.hmipKeyDeclinedWarning},
		{IDs: []string{"legacy-session"}, Lists: true, Eval: a.legacySessionWarning},
		// occulited task 14: an account's access was reset on the console
		{IDs: []string{"console-reset"}, Lists: true, Eval: a.consoleResetWarning},
		// openccu-lite task 366: a newer bootloader EEPROM image to install (info)
		{IDs: []string{"bootloader-update"}, Eval: a.bootloaderWarning},
	}
}

// console-reset (occulited task 14): `occulited admin reset-auth` reset accounts' access on the
// console - passkeys removed, a one-time password set - in the last auth.ConsoleResetNotice. The
// variant is the set of user@time, so another reset is a new notice; it clears by itself after
// the week.
func (a *SystemAPI) consoleResetWarning(context.Context) ([]warnings.Warning, bool) {
	if a.ConsoleResets == nil {
		return nil, true
	}
	resets := a.ConsoleResets(time.Now().Add(-auth.ConsoleResetNotice))
	if len(resets) == 0 {
		return nil, true
	}
	ids := make([]string, len(resets))
	list := make([]map[string]any, len(resets))
	for i, r := range resets {
		ids[i] = fmt.Sprintf("%s@%d", r.User, r.At.Unix())
		list[i] = map[string]any{"user": r.User, "at": r.At.UTC().Format(time.RFC3339)}
	}
	sort.Strings(ids)
	return []warnings.Warning{{ID: "console-reset", Variant: strings.Join(ids, ","), Severity: warnings.SeverityWarning, Href: "/system/users", Params: map[string]any{"accounts": list}}}, true
}

// journal-target: the journal was to be persistent or ram-sync and is in RAM, because the boot
// script could not set the userfs up (its reason; the variant is the mode). Only once the script
// has tried: before it ran at boot there is no mount and no reason either, and that is not a
// warning. journal-sync: ram-sync is in effect and its last copy failed; the variant is "failed",
// so a copy that works again spends a silence and the next failure is new (task 85). Its state is on
// /run: before the first copy of a boot whether a copy fails is not known, and that is unknown, not
// cleared (B-107) - journal-target cannot be active then, the journal is mounted. With a USB stick as
// ram-sync's target (task 216) journal-target's variant is "usb" while the stick is not plugged in
// (its label and directory are params), and journal-sync names the stick.
func (a *SystemAPI) journalWarnings(context.Context) ([]warnings.Warning, bool) {
	if a.Journal == nil {
		return nil, true
	}
	c := a.Root.ReadJournalSetting()
	want := c.JournalWanted()
	if want == system.JournalRAMSync && c.Effective == system.JournalRAMSync && c.LastSync == nil {
		return nil, false
	}
	var out []warnings.Warning
	if c.Fallback != "" {
		variant := want
		params := map[string]any{"mode": want, "reason": c.Fallback, "path": "/usr/local/var/log/journal"}
		if c.TargetLabel != "" {
			params["path"], params["label"], params["dir"] = c.Target, c.TargetLabel, c.TargetDir
			if want == system.JournalRAMSync {
				variant = "usb"
			}
		}
		if c.TargetShare != "" {
			// task 228: a share that the last copy could not reach
			params["path"], params["share"], params["dir"] = c.Target, c.TargetShare, c.TargetDir
			if want == system.JournalRAMSync {
				variant = "share"
			}
		}
		out = append(out, warnings.Warning{ID: "journal-target", Variant: variant, Severity: warnings.SeverityWarning, Href: "/log?settings=journal", Params: params})
	}
	if want == system.JournalRAMSync && c.Effective == system.JournalRAMSync && c.LastSyncResult == "failed" {
		params := map[string]any{"reason": c.LastSyncError}
		if c.TargetLabel != "" {
			params["target"], params["label"] = "usb", c.TargetLabel
		}
		if c.TargetShare != "" {
			params["target"], params["share"] = "share", c.TargetShare
		}
		if c.LastSync != nil {
			params["at"] = c.LastSync.UTC().Format(time.RFC3339)
		}
		out = append(out, warnings.Warning{ID: "journal-sync", Variant: "failed", Severity: warnings.SeverityWarning, Href: "/log?settings=journal", Params: params})
	}
	return out, true
}

// storeWarnings: store-target (task 229) - the USB stick the database's copy goes to is not
// plugged in (variant usb), or the last copy to it failed (variant failed).
func (a *SystemAPI) storeWarnings(context.Context) ([]warnings.Warning, bool) {
	if a.DataStore == nil {
		return nil, true
	}
	c := a.DataStore.Status().Copy
	if c == nil {
		return nil, true
	}
	params := map[string]any{"label": c.Label, "folder": c.Folder}
	switch {
	case !c.Present:
		return []warnings.Warning{{ID: "store-target", Variant: "usb", Severity: warnings.SeverityWarning, Href: "/log?settings=history", Params: params}}, true
	case c.LastError != "":
		params["reason"] = c.LastError
		return []warnings.Warning{{ID: "store-target", Variant: "failed", Severity: warnings.SeverityWarning, Href: "/log?settings=history", Params: params}}, true
	}
	return nil, true
}

// warnAddon is an addon a warning names.
type warnAddon struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
	// Path is the first root-owned entry (addon-ownership), Count how many there are (task 110)
	Path  string `json:"path,omitempty"`
	Count int    `json:"count,omitempty"`
}

// addonListWarning: the variant is the sorted ids, so one more addon is a new warning.
func addonListWarning(id, severity, href string, list []warnAddon) warnings.Warning {
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	ids := make([]string, len(list))
	for i, x := range list {
		ids[i] = x.ID
	}
	return warnings.Warning{ID: id, Variant: strings.Join(ids, ","), Severity: severity, Href: href, Params: map[string]any{"addons": list}}
}

// rega: installed addons that need the ReGa (disabled on the first boot after OpenCCU, or switched
// on again by hand); arch: addons whose binaries this box cannot run.
func (a *SystemAPI) addonWarnings(ctx context.Context) ([]warnings.Warning, bool) {
	if a.Addons == nil {
		return nil, true
	}
	list, err := a.Addons.ListAddons(ctx)
	if err != nil {
		return nil, false
	}
	// openccu-lite task 146: an addon a restore brought back without its program files is
	// "reinstall it", not "wrong architecture" (its binaries are simply gone) - checked first
	a.MarkPayloadMissing(list)
	var rega, arch, payload, ended, failed []warnAddon
	for _, ad := range list {
		ref := warnAddon{ID: ad.ID, Name: ad.Name, Enabled: ad.Enabled}
		switch {
		case ad.PayloadMissing:
			if !ad.ReinstallDismissed {
				payload = append(payload, ref)
			}
		case ad.RegaDependent:
			rega = append(rega, ref)
		case ad.BinaryIncompatible:
			arch = append(arch, ref)
		case ad.Ended:
			// openccu-lite B-158: the addon keeps a daemon and its unit is empty
			ended = append(ended, ref)
		case ad.Failed && ad.Enabled:
			// task 248: the addon's unit failed (systemd's failed state) - the Addons dot's red
			failed = append(failed, ref)
		}
	}
	var out []warnings.Warning
	if len(payload) > 0 {
		out = append(out, addonListWarning("addon-payload", warnings.SeverityError, "/addons#reinstall", payload))
	}
	if len(ended) > 0 {
		out = append(out, addonListWarning("addon-ended", warnings.SeverityWarning, "/addons", ended))
	}
	if len(failed) > 0 {
		out = append(out, addonListWarning("addon-failed", warnings.SeverityError, "/addons", failed))
	}
	if len(rega) > 0 {
		out = append(out, addonListWarning("rega", warnings.SeverityError, "/addons", rega))
	}
	if len(arch) > 0 {
		out = append(out, addonListWarning("arch", warnings.SeverityError, "/catalog", arch))
	}
	return out, true
}

// meta: the store was recovered from its backup at this start; the variant is the start, so a
// recovery at a later start is a new warning.
func (a *SystemAPI) metaWarning(context.Context) ([]warnings.Warning, bool) {
	if !a.MetaRecovered {
		return nil, true
	}
	return []warnings.Warning{{ID: "meta", Variant: strconv.FormatInt(processStart.Unix(), 10), Severity: warnings.SeverityError, Href: "/app"}}, true
}

// unclean: the marker of this boot; its time is the variant. The Log opens on the previous boot,
// which ends where the box went down (task 93), when the journal still holds that boot; in RAM, or
// without a journal, it holds this boot alone, and the Log opens an hour before the marker.
func (a *SystemAPI) uncleanWarning(ctx context.Context) ([]warnings.Warning, bool) {
	u := a.Root.ReadStatus().UncleanShutdown
	if u == nil {
		return nil, true
	}
	// B-114: the marker is written while this boot starts, so a time before this boot began is the
	// clock before it was set - a box without a real-time clock writes it with the image's date (the
	// Pi 4's notice said 13 March). The notice and the link then take the boot's start; the variant
	// stays the marker's own time, so a silence of it still holds.
	at := u.At
	if up, ok := a.Root.Uptime(); ok {
		if start := time.Now().Add(-up); at.Before(start) {
			at = start
		}
	}
	href := fmt.Sprintf("/log?since=@%d", at.Unix()-3600)
	if a.journalHoldsPreviousBoot(ctx) {
		href = "/log?boot=-1"
	}
	return []warnings.Warning{{ID: "unclean", Variant: strconv.FormatInt(u.At.Unix(), 10), Severity: warnings.SeverityError,
		Href: href, Params: map[string]any{"at": at.UTC().Format(time.RFC3339)}}}, true
}

// journalHoldsPreviousBoot: the journal lists a boot before the running one (--list-boots' index -1).
// The list is GET /boots' cached one; a journal that cannot be read counts as not holding it.
func (a *SystemAPI) journalHoldsPreviousBoot(ctx context.Context) bool {
	if a.Journal == nil {
		return false
	}
	boots, err := a.journalBoots(ctx)
	if err != nil {
		return false
	}
	for _, b := range boots {
		if b.Index < 0 {
			return true
		}
	}
	return false
}

// backup-target: the nightly backup is on and its directory cannot be read; backup-userfs: it
// writes onto the box itself (D-41). The variant is the path.
func (a *SystemAPI) backupWarnings(context.Context) ([]warnings.Warning, bool) {
	// task 86: without a directory target (no path set) the other targets warn for themselves
	if a.BackupTargets != nil {
		if _, ok, err := a.BackupTargets.Store.Get(backuptarget.DirectoryID); err != nil || !ok {
			return nil, err == nil
		}
	}
	c, answered := a.readCronBackup()
	if !answered {
		return nil, false
	}
	if !c.Enabled {
		return nil, true
	}
	params := map[string]any{"path": c.Path}
	switch {
	case !c.PathExists:
		return []warnings.Warning{{ID: "backup-target", Variant: c.Path, Severity: warnings.SeverityError, Href: "/backup", Params: params}}, true
	case c.OnUserfs:
		return []warnings.Warning{{ID: "backup-userfs", Variant: c.Path, Severity: warnings.SeverityError, Href: "/backup", Params: params}}, true
	}
	return nil, true
}

// certificate: renewal-failed, expired or expiring (below 14 days) - the Status card's rule.
func (a *SystemAPI) certificateWarning(context.Context) ([]warnings.Warning, bool) {
	if a.Cert == nil {
		return nil, true
	}
	st := a.Cert.Status()
	c := st.Current
	if c == nil {
		// the live certificate could not be read this time: unknown, not fine
		return nil, st.CurrentError == ""
	}
	variant := certificateVariant(st.Warning, c.DaysLeft)
	if variant == "" {
		return nil, true
	}
	return []warnings.Warning{{ID: "certificate", Variant: variant, Severity: warnings.SeverityError, Href: "/certificate", Params: map[string]any{"days": c.DaysLeft}}}, true
}

// certificateVariant is the rule of the Status card: a failed renewal first, then expired, then
// fewer than 14 days; "" = no warning.
func certificateVariant(warning string, daysLeft int) string {
	switch {
	case warning == "last-attempt-failed":
		return "renewal-failed"
	case daysLeft < 0:
		return "expired"
	case warning == "expiring" || daysLeft < 14:
		return "expiring"
	}
	return ""
}

// storage: task 69's verdict; replace is an error, watch a warning.
func (a *SystemAPI) storageWarning(ctx context.Context) ([]warnings.Warning, bool) {
	if a.Storage == nil {
		return nil, true
	}
	rep := a.Storage.Report(ctx)
	// the kernel log could not be read: its I/O errors are unknown, so a verdict below replace may be
	// one that lacks them - unknown, not cleared (B-107)
	if !rep.KernelLog && rep.Verdict != system.StorageReplace {
		return nil, false
	}
	severity := warnings.SeverityWarning
	switch rep.Verdict {
	case system.StorageReplace:
		severity = warnings.SeverityError
	case system.StorageWatch:
	default:
		return nil, true
	}
	type dev struct {
		Name  string `json:"name"`
		Kind  string `json:"kind"`
		Model string `json:"model,omitempty"`
	}
	devs := make([]dev, 0, len(rep.Devices))
	for _, d := range rep.Devices {
		devs = append(devs, dev{Name: d.Name, Kind: d.Kind, Model: d.Model})
	}
	return []warnings.Warning{{ID: "storage", Variant: rep.Verdict, Severity: severity, Href: "#storage",
		Params: map[string]any{"verdict": rep.Verdict, "reasons": rep.Reasons, "devices": devs}}}, true
}

// addon-ownership (B-92, D-64): confined addons with a root-owned file in their directories.
func (a *SystemAPI) ownershipWarning(context.Context) ([]warnings.Warning, bool) {
	o := a.addonOwnership()
	if o == nil {
		return nil, true
	}
	found, complete := o.Findings()
	if !complete {
		return nil, false
	}
	if len(found) == 0 {
		return nil, true
	}
	list := make([]warnAddon, 0, len(found))
	for _, f := range found {
		// B-267: an addon a restore left without its program is the reinstall warning's; its
		// files are root's because its unit (and the ownership step in it) did not start
		if a.AddonPayload != nil && a.AddonPayload.ProgramMissing(f.ID) {
			continue
		}
		list = append(list, warnAddon{ID: f.ID, Path: f.Path, Count: f.Count, Enabled: true})
	}
	if len(list) == 0 {
		return nil, true
	}
	return []warnings.Warning{addonListWarning("addon-ownership", warnings.SeverityWarning, "/services", list)}, true
}

// rfdState is what the radio sampler knows about rfd.
type rfdState int

const (
	rfdUnknown rfdState = iota
	rfdUp
	rfdDown
)

// bidcosState is what the security-key rule knows about BidCos-RF at one evaluation.
type bidcosState struct {
	// detected: the radio detection has finished in this boot, so /var/hm_mode is its answer; true
	// where there is no detection unit to wait for (no systemd, or a product without the unit)
	detected bool
	// module: /var/hm_mode assigns BidCos-RF a module
	module bool
	// rfd: the radio sampler's last poll; rfdUnknown when it was taken before the detection finished
	rfd rfdState
	// paired: the BidCos-RF devices rfd listed at the service-message sweep, the CCU's own receiver
	// left out (occulited task 89); -1 before a sweep rfd answered
	paired int
}

// securityKeyRule: BidCos-RF is there and rfd answers, and no own key is set. The state is unknown
// (unknown is not cleared, B-107) until the radio detection has finished in this boot - occulited
// starts before it since task 94, and read an hm_mode without the BidCos-RF module as an HmIP-only
// box, which spent the silence at every boot - and while a module is assigned and rfd does not
// answer (down, or not asked since the detection). An HmIP-only box - no module, and a poll after the
// detection without BidCos-RF - has nothing to warn about, and neither has a box without crypttool
// (a development root). rfd answering BidCos-RF is BidCos-RF being there, with or without a module.
// And BidCos-RF being there is not enough (occulited task 89, the maintainer): the key protects the
// BidCos devices, so a system with none paired - HmIP only on a radio that carries both - has nothing
// to warn about either; unknown until rfd answered a service-message sweep.
func securityKeyRule(st bidcosState, key func() (set, known, ok bool)) ([]warnings.Warning, bool) {
	switch {
	case !st.detected:
		return nil, false
	case st.rfd == rfdUp:
	case st.module, st.rfd == rfdUnknown:
		return nil, false
	default:
		return nil, true // HmIP only
	}
	switch {
	case st.paired < 0:
		return nil, false
	case st.paired == 0:
		return nil, true // no BidCos device paired
	}
	set, known, ok := key()
	switch {
	case !ok:
		return nil, false
	case !known, set:
		return nil, true
	}
	return []warnings.Warning{{ID: "security-key", Variant: "default", Severity: warnings.SeverityWarning, Href: "/system/keys#security-key"}}, true
}

func (a *SystemAPI) securityKeyWarning(ctx context.Context) ([]warnings.Warning, bool) {
	return securityKeyRule(a.bidcosState(ctx), func() (bool, bool, bool) { return a.warn.key(ctx, a.Root) })
}

// bidcosState reads the detection unit (the radio stack's units, as the Status page has them), the
// module assignment and the sampler's last poll.
func (a *SystemAPI) bidcosState(ctx context.Context) bidcosState {
	st := bidcosState{detected: true}
	var detectedFor time.Duration // how long ago the detection finished; 0 = not known
	if _, systemd := a.Services.(InterfaceUnitReader); systemd {
		units := a.radioUnits(ctx)
		// systemd did not answer: the detection is not known to have finished
		st.detected = units != nil
		for _, u := range units {
			if u.Unit == system.RadioDetectionUnit {
				st.detected, detectedFor = u.ActiveState == "active", u.ActiveFor
			}
		}
	}
	for _, m := range a.Root.ReadRadio().Modules {
		st.module = st.module || m.Protocol == "BidCos-RF"
	}
	polled, list := a.rfdPoll()
	st.rfd = rfdOf(polled, list)
	st.paired = -1
	if n, ok := a.bidcosPaired(); ok {
		st.paired = n
	}
	// a poll from before the detection finished did not ask the rfd of this boot's assignment
	if st.rfd != rfdUnknown && detectedFor > 0 && time.Since(polled) >= detectedFor {
		st.rfd = rfdUnknown
	}
	return st
}

// transferringInterfaces are the interfaces on which a device firmware transfer runs (occulited
// task 61); none without the firmware service.
func (a *SystemAPI) transferringInterfaces() map[string]bool {
	if a.warn.transfers != nil {
		return a.warn.transfers()
	}
	if a.Firmware == nil {
		return nil
	}
	return firmware.TransferringInterfaces(a.Firmware.Status().Devices)
}

// radioPlan is the boot's radio plan (task 156); false without one.
func (a *SystemAPI) radioPlan() (radio.Plan, bool) {
	if a.warn.plan != nil {
		return a.warn.plan()
	}
	return a.RadioConnections.BootPlan()
}

// TransferEntries says for each entry of list whether a device firmware transfer runs on its
// radio (occulited B-83). One module can carry both stacks - an RPI-RF-MOD or HmIP-RFUSB shared
// by rfd and hmipserver through multimacd - and both stacks then report that module's duty
// cycle; an HmIP transfer fills the BidCos entry's reading as much as its own. So a transfer on
// one interface counts for every entry of the same transmitter (radio.Plan.Transmitter); without
// a plan each entry is its own radio, as before.
func TransferEntries(p radio.Plan, havePlan bool, list []interfaces.RadioInterface, transfers map[string]bool) []bool {
	out := make([]bool, len(list))
	if len(transfers) == 0 {
		return out
	}
	hmip := 0
	for _, ri := range list {
		if ri.Interface == "HmIP-RF" {
			hmip++
		}
	}
	keys := make([]string, len(list))
	busy := map[string]bool{}
	for i, ri := range list {
		keys[i] = "if:" + ri.Interface + "/" + ri.Address
		if havePlan {
			keys[i], _ = p.Transmitter(ri.Interface, ri.Address, ri.Type, hmip)
		}
		if transfers[ri.Interface] {
			busy[keys[i]] = true
		}
	}
	for i := range list {
		out[i] = transfers[list[i].Interface] || busy[keys[i]]
	}
	return out
}

// markThresholds sets each entry's duty-cycle threshold and transfer flag (occulited task 79): the
// radio-load warning's 25 %, 75 % where onRadio says a transfer runs on the entry's radio.
func markThresholds(list []interfaces.RadioInterface, onRadio []bool) {
	for i := range list {
		list[i].DCThreshold, list[i].Transfer = warnings.DutyCycleThreshold, onRadio[i]
		if onRadio[i] {
			list[i].DCThreshold = warnings.DutyCycleThresholdTransfer
		}
	}
}

// bidcosPaired is how many BidCos-RF devices rfd listed at the service-message sweep; false before
// a sweep rfd answered, and without the store (occulited task 89).
func (a *SystemAPI) bidcosPaired() (int, bool) {
	if a.warn.paired != nil {
		return a.warn.paired()
	}
	if a.ServiceMessages == nil {
		return 0, false
	}
	return a.ServiceMessages.Paired("BidCos-RF")
}

// rfdPoll is the radio sampler's last poll; a zero time without a sampler.
func (a *SystemAPI) rfdPoll() (time.Time, []interfaces.RadioInterface) {
	if a.warn.rfdPoll != nil {
		return a.warn.rfdPoll()
	}
	if a.Health == nil {
		return time.Time{}, nil
	}
	st := a.Health.Status()
	return st.Polled, st.Interfaces
}

// rfdOf reads the sampler: rfd answered when its last poll listed a BidCos-RF interface.
func rfdOf(polled time.Time, list []interfaces.RadioInterface) rfdState {
	if polled.IsZero() {
		return rfdUnknown
	}
	for _, ri := range list {
		if ri.Interface == "BidCos-RF" {
			return rfdUp
		}
	}
	return rfdDown
}

func (a *SystemAPI) registerWarnings(mux *http.ServeMux, p string) {
	route(mux, auth.ScopeSystemRead, "GET "+p+"/warnings", a.warningsList)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/warnings/silence", a.warningsSilence)
	// the variant is the rest of the path: a backup target's is a path itself
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/warnings/silence/{id}/{variant...}", a.warningsUnsilence)
	route(mux, auth.ScopeAddonsWrite, "POST "+p+"/addons/{id}/ownership", a.addonOwnershipFix)
}

// caller is the session's user name and whether it may change the system configuration
// (system:write): the silences, the LED configuration, the legacy session switches.
func caller(r *http.Request) (string, bool) {
	if s := SessionFrom(r); s != nil {
		return s.User, s.Has(auth.ScopeSystemWrite)
	}
	return "", false
}

func (a *SystemAPI) warningsReady(w http.ResponseWriter) bool {
	if a.Warnings == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no warnings tracker"})
		return false
	}
	return true
}

// forbidden is the 403 of a handler whose route's scope is system:write - the silences, the LED
// configuration - when the session lacks it.
func forbidden(w http.ResponseWriter) { forbiddenScope(w, auth.ScopeSystemWrite) }

// warningsAnswer: an administrator also gets the periods a silence may have.
func (a *SystemAPI) warningsAnswer(r *http.Request) map[string]any {
	user, admin := caller(r)
	list := a.Warnings.List(r.Context(), user, admin)
	if list == nil {
		list = []warnings.Warning{}
	}
	out := map[string]any{"warnings": list}
	if admin {
		out["periods"] = warnings.Periods
	}
	return out
}

// warningsList is GET /warnings, any session: the active warnings, each administrator's own
// silences on them.
func (a *SystemAPI) warningsList(w http.ResponseWriter, r *http.Request) {
	if !a.warningsReady(w) {
		return
	}
	writeJSON(w, 200, a.warningsAnswer(r))
}

// warningsSilence is POST /warnings/silence {id, variant, days}, administrators only (D-64).
func (a *SystemAPI) warningsSilence(w http.ResponseWriter, r *http.Request) {
	if !a.warningsReady(w) {
		return
	}
	user, admin := caller(r)
	if !admin {
		forbidden(w)
		return
	}
	var body struct {
		ID      string `json:"id"`
		Variant string `json:"variant"`
		Days    int    `json:"days"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	s, err := a.Warnings.Silence(r.Context(), user, body.ID, body.Variant, body.Days)
	switch {
	case errors.Is(err, warnings.ErrPeriod):
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid-period", Message: err.Error()})
		return
	case errors.Is(err, warnings.ErrNotActive):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-active", Message: err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "save-failed", Message: err.Error()})
		return
	}
	out := a.warningsAnswer(r)
	out["silence"] = s
	writeJSON(w, 200, out)
}

// warningsUnsilence is DELETE /warnings/silence/{id}/{variant}, administrators only: the caller's
// own silence.
func (a *SystemAPI) warningsUnsilence(w http.ResponseWriter, r *http.Request) {
	if !a.warningsReady(w) {
		return
	}
	user, admin := caller(r)
	if !admin {
		forbidden(w)
		return
	}
	err := a.Warnings.Unsilence(user, r.PathValue("id"), r.PathValue("variant"))
	switch {
	case errors.Is(err, warnings.ErrNotSilenced):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-silenced", Message: err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "save-failed", Message: err.Error()})
		return
	}
	writeJSON(w, 200, a.warningsAnswer(r))
}

// addonOwnershipFix is POST /addons/{id}/ownership, administrators only (B-92): the confined
// addon's directories are given back to its user, as an install through occulited does.
func (a *SystemAPI) addonOwnershipFix(w http.ResponseWriter, r *http.Request) {
	if !SessionFrom(r).Has(auth.ScopeAddonsWrite) {
		forbiddenScope(w, auth.ScopeAddonsWrite)
		return
	}
	o := a.addonOwnership()
	if o == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no systemd addon manager"})
		return
	}
	id := r.PathValue("id")
	fix, err := o.FixOwnership(r.Context(), id)
	switch {
	case errors.Is(err, system.ErrNotConfined):
		writeJSON(w, http.StatusConflict, apiError{Error: "not-confined", Message: err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "ownership", Message: err.Error()})
		return
	}
	reqLog(r).Info("addon ownership: the addon's files were given back to its user", "addon", id, "root_owned_left", fix.RootOwned)
	out := map[string]any{"id": id, "root_owned": fix.RootOwned, "started": fix.Started}
	switch {
	case fix.Started:
		reqLog(r).Info("addon ownership: the addon's unit had failed and was started", "addon", id)
	case fix.StartError != "":
		slog.Warn("addon ownership: the addon's unit had failed and could not be started", "addon", id, "err", fix.StartError)
		out["start_error"] = fix.StartError
	}
	writeJSON(w, 200, out)
}

// hmip-adapter (D-102): hmipserver's last start failed on a known fatal error - the key server
// rejected the adapter exchange - and the unit waits for the next run of the radio stack. The
// variant is the error's code, so a different one is a new warning; the cause (D-106: the key
// server unreachable, or the module refused) goes with the parameters and picks the text.
func (a *SystemAPI) hmipAdapterWarning(context.Context) ([]warnings.Warning, bool) {
	f := radio.ReadHmIPFatal(string(a.Root))
	if f == nil {
		return nil, true
	}
	return []warnings.Warning{{ID: "hmip-adapter", Variant: f.Code, Severity: warnings.SeverityError, Href: "/system/interfaces#connections", Params: map[string]any{"adapter": f.Adapter, "line": f.Line, "cause": f.Cause}}}, true
}

// hmip-local-swap (openccu-lite B-289): the newest adapter exchange in the local record moved the
// HmIP network onto the module HmIP-RF runs on now without the network key - its application
// firmware is below 2.8.0, so hmipserver swapped the access point locally and called it a success.
// hmipserver runs, but the module cannot send, and the key server refuses every exchange away from
// it; nothing else on the system says so. The variant is the module and the time of the move, so
// a later one is new. Gone once HmIP-RF runs on another module (the way back from a kept identity).
func (a *SystemAPI) hmipLocalSwapWarning(context.Context) ([]warnings.Warning, bool) {
	list := radio.ReadHmIPExchanges(string(a.Root))
	if len(list) == 0 || list[0].Outcome != radio.ExchangeRejected || list[0].Cause != radio.ExchangeCauseAdapterVersion {
		return nil, true
	}
	x := list[0]
	p, ok := a.RadioConnections.BootPlan()
	if !ok {
		var err error
		p, err = radio.LoadPlan(string(a.Root))
		ok = err == nil
	}
	if !ok || p.HmIP == nil || !strings.EqualFold(p.HmIP.SGTIN, x.To) {
		return nil, true
	}
	return []warnings.Warning{{ID: "hmip-local-swap", Variant: x.To + "@" + x.At.UTC().Format(time.RFC3339), Severity: warnings.SeverityError, Href: "/system/interfaces#connections",
		Params: map[string]any{"module": x.To, "from": x.From, "version": x.ToVersion, "minimum": radio.MinHmIPKeyExchangeVersion}}}, true
}

// hmip-module-missing (openccu-lite task 318, D-120): on Automatic, HmIP-RF is kept on the module
// that holds the HmIP network, and that module is missing or did not answer at the last run - the
// HmIP devices are out of reach, hmipserver runs its VirtualDevices half alone, and no other module
// is taken unasked. The variant is the module. Gone once it is back, or HmIP-RF is moved by choice.
func (a *SystemAPI) hmipModuleMissingWarning(context.Context) ([]warnings.Warning, bool) {
	p, ok := a.RadioConnections.BootPlan()
	if !ok {
		var err error
		p, err = radio.LoadPlan(string(a.Root))
		ok = err == nil
	}
	if !ok || p.HmIPPin == "" || !strings.EqualFold(p.MissingHmIP, p.HmIPPin) {
		return nil, true
	}
	return []warnings.Warning{{ID: "hmip-module-missing", Variant: p.HmIPPin, Severity: warnings.SeverityError, Href: "/system/interfaces#connections", Params: map[string]any{"module": p.HmIPPin}}}, true
}

// devices-import (openccu-lite task 275): a device import brought an HmIP identity of another
// module, and hmipserver has not taken it over onto this one yet - it is starting, the key server
// was not reached, or the devices have not answered. The variant is the state, so a state that
// changes is a new warning; the rejected case is hmip-adapter's. Gone with the record's dismissal.
func (a *SystemAPI) devicesImportWarning(ctx context.Context) ([]warnings.Warning, bool) {
	rec := a.ImportRecord.Read()
	if rec == nil || !rec.HmIP.ModuleChanged {
		return nil, true
	}
	out := a.ImportRecord.Outcome(ctx, *rec)
	switch out.HmIP.State {
	case system.ImportPending, system.ImportUnknown, system.ImportNoModule:
		return []warnings.Warning{{ID: "devices-import", Variant: out.HmIP.State, Severity: warnings.SeverityWarning, Href: "/system/interfaces#connections", Params: map[string]any{"from": rec.HmIP.FromSGTIN, "module": out.HmIP.ModuleNow, "count": rec.HmIP.Devices}}}, true
	}
	return nil, true
}

// firewall (B-152, task 157): a family's INPUT chain has no jump to lite-input - the rules were not
// loaded, or were flushed since, so that family accepts what its policy says whatever the rules.
// The variant is the families, "ipv4,ipv6", so a different set is new. A box without iptables has
// nothing to check, one without the rule file (before task 157) neither; a read that failed is
// unknown, not clear. The tracker's boot gate keeps it quiet until the boot has settled.
func firewallRule(loaded map[string]bool, available, ok bool) ([]warnings.Warning, bool) {
	switch {
	case !ok:
		return nil, false
	case !available:
		return nil, true
	}
	open := system.FirewallNotLoaded(loaded)
	if len(open) == 0 {
		return nil, true
	}
	return []warnings.Warning{{ID: "firewall", Variant: strings.Join(open, ","), Severity: warnings.SeverityError, Href: "/system/firewall",
		Params: map[string]any{"families": open}}}, true
}

func (a *SystemAPI) firewallWarning(ctx context.Context) ([]warnings.Warning, bool) {
	if _, ok := a.Root.ReadRules(); !ok {
		return nil, true // no rule file yet: occulited converts and loads at its start
	}
	loaded, available, ok := a.warn.firewall(ctx)
	return firewallRule(loaded, available, ok)
}

// classic-rpc-open (task 173): classic RPC (task 143) is on without a login - whoever the firewall
// lets in controls every device, as on a CCU without authentication. A warning, not an error: it
// can be meant, and then it is silenced. The variant is what is open, "plain", "tls" or
// "plain,tls", so opening more after a silence is new.
func classicRPCRule(c system.ClassicRPC) []warnings.Warning {
	if c.Auth != "none" || (!c.Plain && !c.TLS) {
		return nil
	}
	var open []string
	if c.Plain {
		open = append(open, "plain")
	}
	if c.TLS {
		open = append(open, "tls")
	}
	return []warnings.Warning{{ID: "classic-rpc-open", Variant: strings.Join(open, ","), Severity: warnings.SeverityWarning, Href: "/system/remote-access"}}
}

func (a *SystemAPI) classicRPCWarning(context.Context) ([]warnings.Warning, bool) {
	if a.ClassicRPC == nil {
		return nil, true
	}
	return classicRPCRule(a.Root.ReadClassicRPC()), true
}

// app-public (task 193): the Control app is public - anyone who reaches the web port operates
// the house. The variant is the account, so a changed account is a new warning.
func (a *SystemAPI) appPublicWarning(context.Context) ([]warnings.Warning, bool) {
	if a.Public == nil {
		return nil, true
	}
	on, account := a.Public.PublicView()
	if !on {
		return nil, true
	}
	return []warnings.Warning{{ID: "app-public", Variant: account, Severity: warnings.SeverityWarning, Href: "/settings#public", Params: map[string]any{"account": account}}}, true
}

// hmip-port-open (B-89): hmipserver's HTTP port (39292) listens on an address other than the
// loopback - the image's bind shim did not load, or the image has none. The firewall still closes
// the port; the warning says that its second guard is missing. The variant is the port.
func (a *SystemAPI) hmipPortWarning(context.Context) ([]warnings.Warning, bool) {
	port, open := radio.HMServerPortOpen(string(a.Root))
	if len(open) == 0 {
		return nil, true
	}
	return []warnings.Warning{{ID: "hmip-port-open", Variant: strconv.Itoa(port), Severity: warnings.SeverityWarning, Params: map[string]any{"port": port, "addresses": strings.Join(open, ", ")}, Href: "/system/firewall"}}, true
}

// hmip-local-key (task 149): the check after a switch to local key mode found most HmIP devices
// unreachable - an entered key was probably not the network's. The variant is the switch's time,
// so a later switch that fails again is a new warning.
func (a *SystemAPI) hmipLocalKeyWarning(context.Context) ([]warnings.Warning, bool) {
	if a.HmIPLocalKey == nil {
		return nil, true
	}
	c := a.HmIPLocalKey.CheckFailed()
	if c == nil {
		return nil, true
	}
	return []warnings.Warning{{ID: "hmip-local-key", Variant: c.Started.UTC().Format(time.RFC3339), Severity: warnings.SeverityError, Href: "/system/keys#local-key",
		Params: map[string]any{"unreachable": c.Total - c.Heard, "total": c.Total}}}, true
}

// hmipserverUnit is the unit whose log carries the decline, and declineLines how many of its
// matching lines are read: a handful of devices, and only the newest line per device counts.
const (
	hmipserverUnit = "hmipserver"
	declineLines   = 50
)

// hmip-key-declined (task 201, decided in task 154's Q&A): hmipserver turned an inclusion request
// down because the device's local key it was given (in sgtin.map, or entered for the pairing) is
// not that device's key. Without this the device simply never appears and nothing says why - the line
// is in hmipserver's log and nowhere else. The image's log4j2 template keeps the logger that writes it
// at debug whatever LOGLEVEL_HMIP says, so the line is there at the warn default too.
//
// Read on demand from the journal of this boot, pre-filtered by journalctl on the fixed part of
// the line, so the usual answer costs one grep that finds nothing. The devices are listed only
// when there is a decline to clear: a pairing that has since worked takes its warning away, which
// is the only way it can go - the line stays in the journal. One warning per device, the variant
// the SGTIN and the time of the newest decline, so a further decline after a silence is new.
func (a *SystemAPI) hmipKeyDeclinedWarning(ctx context.Context) ([]warnings.Warning, bool) {
	if a.Journal == nil {
		return nil, true
	}
	lines, err := a.Journal.Read(system.LogQuery{Unit: hmipserverUnit, Boot: "0", Contains: radio.InclusionDeclinedMatch, Limit: declineLines})
	if err != nil {
		return nil, false
	}
	// the newest decline per device, in the order the devices were first declined
	at := map[string]string{}
	var order []string
	for _, l := range lines {
		sgtin := radio.InclusionDeclinedSGTIN(l.Message)
		if sgtin == "" {
			continue
		}
		if _, seen := at[sgtin]; !seen {
			order = append(order, sgtin)
		}
		at[sgtin] = l.Timestamp
		if at[sgtin] == "" {
			at[sgtin] = l.Time
		}
	}
	if len(order) == 0 {
		return nil, true
	}
	paired := map[string]bool{}
	if a.HmIPDeviceKeys != nil {
		p, ok := a.HmIPDeviceKeys.PairedSGTINs(ctx, order)
		if !ok {
			// hmipserver did not answer: whether the device is in now is unknown, and unknown is
			// not "still declined" - the silences stay, nothing is shown (warnings.Source's ok)
			return nil, false
		}
		paired = p
	}
	var out []warnings.Warning
	for _, sgtin := range order {
		if paired[sgtin] {
			continue
		}
		params := map[string]any{"sgtin": sgtin, "address": radio.DeviceAddressOf(sgtin)}
		if at[sgtin] != "" {
			params["at"] = at[sgtin]
		}
		out = append(out, warnings.Warning{ID: "hmip-key-declined", Variant: sgtin + "@" + at[sgtin], Severity: warnings.SeverityWarning,
			Href: "/system/keys#device-keys", Params: params})
	}
	return out, true
}

// addon-update (openccu-lite task 248): an installed addon has a newer version - what the Addons
// page's check found, from the catalogue's latest releases (as the system holds them, nothing is
// fetched for this) and, for an addon the catalogue does not know, its own update check. The variant
// is every id@version, so a newer version after a silence is new; it clears when the updates are
// installed or a check no longer finds them. The Addons menu shows the same as its yellow dot.
func (a *SystemAPI) addonUpdateWarning(ctx context.Context) ([]warnings.Warning, bool) {
	type upd struct{ ID, Name, Installed, Available string }
	found := map[string]upd{}
	installed := map[string]system.Addon{}
	if a.Addons != nil {
		list, err := a.Addons.ListAddons(ctx)
		if err != nil {
			return nil, false
		}
		for _, ad := range list {
			installed[ad.ID] = ad
		}
	}
	known := map[string]bool{}
	if c, ok := a.Catalog.(interface{ Cached() *catalog.View }); ok && a.Catalog != nil {
		if v := c.Cached(); v != nil {
			for _, it := range v.Addons {
				if it.Manifest == nil {
					continue
				}
				known[it.ID] = true
				ad, ok := installed[it.ID]
				if ok && it.Latest != nil && catalog.UpdateAvailable(ad.Version, it.Latest.Version) {
					found[it.ID] = upd{it.ID, ad.Name, ad.Version, it.Latest.Version}
				}
			}
		}
	}
	if a.Updates != nil {
		for _, r := range a.Updates.State().Results {
			if known[r.ID] || !r.Info.UpdateAvailable {
				continue
			}
			if _, ok := installed[r.ID]; !ok && len(installed) > 0 {
				continue // uninstalled since the check
			}
			found[r.ID] = upd{r.ID, r.Name, r.Info.Installed, r.Info.Available}
		}
	}
	if len(found) == 0 {
		return nil, true
	}
	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var variant []string
	var list []map[string]any
	for _, id := range ids {
		u := found[id]
		name := u.Name
		if name == "" {
			name = id
		}
		variant = append(variant, id+"@"+u.Available)
		list = append(list, map[string]any{"id": id, "name": name, "installed": u.Installed, "available": u.Available})
	}
	return []warnings.Warning{{ID: "addon-update", Variant: strings.Join(variant, ","), Severity: warnings.SeverityWarning, Href: "/addons",
		Params: map[string]any{"count": len(list), "addons": list}}}, true
}

// catalog-check (occulited B-52): the addon catalogue's check keeps failing - three checks in a
// row, or failures over a day (catalog.CheckFailure.Persistent); one failed check is the Addons
// page's alone. The variant is "failing", so a silence lasts until a check works again. Read from
// what the system holds, nothing is fetched for it.
func (a *SystemAPI) catalogCheckWarning(context.Context) ([]warnings.Warning, bool) {
	c, ok := a.Catalog.(interface{ CheckError() *catalog.CheckFailure })
	if !ok || a.Catalog == nil {
		return nil, true
	}
	f := c.CheckError()
	if !f.Persistent() {
		return nil, true
	}
	return []warnings.Warning{{ID: "catalog-check", Variant: "failing", Severity: warnings.SeverityWarning, Href: "/addons",
		Params: map[string]any{"failures": f.Failures, "since": f.Since.UTC().Format(time.RFC3339), "detail": f.Message, "host": f.Host}}}, true
}

// radio-firmware and radio-module-unusable (openccu-lite task 137, D-89: the boot never flashes the
// coprocessor, so what it would have flashed is said here). radio-firmware: a module's newest file
// on the system is newer than what it runs - a notice, no automatic flash; the variant is the
// module and the newer version, so a newer file after a silence is new. An HM-CFG-USB-2 below 0.967
// says more (D-100: it drops off the USB bus). radio-module-unusable: an HmIP-RFUSB the detection
// found but could not read - no radio on it until it is flashed; the variant is its node.
func (a *SystemAPI) radioFirmwareWarnings(context.Context) ([]warnings.Warning, bool) {
	if a.RadioFirmware == nil {
		return nil, true
	}
	var out []warnings.Warning
	for _, m := range a.RadioFirmware.Status().Modules {
		newest := ""
		for _, f := range m.Files {
			if f.Name == m.Newest {
				newest = f.Version
			}
		}
		switch m.Verdict {
		case "newer-available":
			params := map[string]any{"device": m.Device, "running": m.RunningVersion, "newest": newest}
			if m.Device == "HM-CFG-USB-2" && system.CompareVersions(m.RunningVersion, "0.967") < 0 {
				params["drops_off"] = true
			}
			out = append(out, warnings.Warning{ID: "radio-firmware", Variant: m.Device + "@" + newest, Severity: warnings.SeverityWarning, Href: "/system/updates#radio-firmware", Params: params})
		case "unusable":
			out = append(out, warnings.Warning{ID: "radio-module-unusable", Variant: m.DeviceNode, Severity: warnings.SeverityWarning, Href: "/system/updates#radio-firmware",
				Params: map[string]any{"device": m.Device, "node": m.DeviceNode, "newest": newest}})
		}
	}
	return out, true
}

// crash-loop (openccu-lite task 283): units that restarted CrashLoopClass.Restarts times within
// the window and have not stayed up since - hidden otherwise by the restart backoff, which never
// gives up and never marks the unit failed. The variant is the set of units, so a unit joining
// the loop is a new warning; it clears once each has stayed up (the sampler's Stable).
func (a *SystemAPI) crashLoopWarning(context.Context) ([]warnings.Warning, bool) {
	if a.CrashLoops == nil {
		return nil, true
	}
	loops := a.CrashLoops.Loops()
	if len(loops) == 0 {
		return nil, true
	}
	ids := make([]string, len(loops))
	for i, l := range loops {
		ids[i] = l.Unit
	}
	return []warnings.Warning{{ID: "crash-loop", Variant: strings.Join(ids, ","), Severity: warnings.SeverityError, Href: "/system/services", Params: map[string]any{"units": loops}}}, true
}

// occulitedCrashLoopTTL is how long after its last failure occulited's own crash loop stays a
// warning: it could not warn while it looped, and the loop may be over by the time anybody looks.
const occulitedCrashLoopTTL = 24 * time.Hour

// occulited-crash-loop (task 283): occulited itself was in a crash loop - the unit's state file
// (written by systemd's hooks outside occulited) says so after it came back. The variant is the
// loop's first failure, so the next loop is a new warning.
func (a *SystemAPI) occulitedCrashLoopWarning(context.Context) ([]warnings.Warning, bool) {
	s := a.Root.ReadOcculitedUnitState()
	if s == nil || s.Fails < system.OcculitedCrashLoopFails || s.LastFail <= 0 {
		return nil, true
	}
	last := time.Unix(s.LastFail, 0)
	if time.Since(last) > occulitedCrashLoopTTL {
		return nil, true
	}
	return []warnings.Warning{{ID: "occulited-crash-loop", Variant: strconv.FormatInt(s.FirstFail, 10), Severity: warnings.SeverityWarning, Href: "/system/log?unit=occulited", Params: map[string]any{
		"fails": s.Fails, "restarts": s.Restarts, "result": s.Result,
		"first": time.Unix(s.FirstFail, 0).UTC().Format(time.RFC3339), "last": last.UTC().Format(time.RFC3339),
	}}}, true
}

// hmip-security-counter (openccu-lite task 299; eq-3/occu#134): what hmipserver's last start and the
// access point file say about the HmIP security counter - near (past 2^31), wrapped (past 2^32: the
// server's check protects nothing any more), backwards (the module now holds a value below what
// the devices saw: they refuse the system until they are power-cycled). The variant is the verdict,
// so a change is a new warning. hmip-clock-hold: the prep step holds hmipserver back on such an
// access point while the clock is not trusted; the Time page's manual setting releases it.
func (a *SystemAPI) hmipCounterWarnings(context.Context) ([]warnings.Warning, bool) {
	root := string(a.Root)
	var out []warnings.Warning
	if h := radio.ReadCounterHold(root); h != nil {
		out = append(out, warnings.Warning{ID: "hmip-clock-hold", Variant: h.ClockState, Severity: warnings.SeverityError, Href: "/system/network", Params: map[string]any{"sgtin": h.SGTIN, "since": h.Since.UTC().Format(time.RFC3339), "reason": h.Reason, "clock_state": h.ClockState, "calc": h.Calc, "behind": h.Behind}})
	}
	st := radio.ReadCounterState(root)
	ids := make([]string, 0, len(st.AccessPoints))
	for id := range st.AccessPoints {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ap := st.AccessPoints[id]
		if ap.Verdict == radio.CounterFine || len(ap.Starts) == 0 {
			continue
		}
		last := ap.Starts[len(ap.Starts)-1]
		params := map[string]any{"sgtin": ap.SGTIN, "calc": last.Calc, "at": last.At.UTC().Format(time.RFC3339), "source": last.Source, "offset": ap.Offset}
		if last.Source == "journal" {
			params["current"], params["written"] = last.Current, last.Written
		}
		if !ap.WrapsAt.IsZero() {
			params["wraps_at"] = ap.WrapsAt.UTC().Format(time.RFC3339)
		}
		sev := warnings.SeverityWarning
		if ap.Verdict == radio.CounterBackwards {
			sev = warnings.SeverityError
		}
		out = append(out, warnings.Warning{ID: "hmip-security-counter", Variant: ap.Verdict, Severity: sev, Href: "/system/interfaces#connections", Params: params})
	}
	return out, true
}

// The radio load's thresholds: the duty cycle's are warnings.DutyCycleThreshold (25 %) and, while
// a device firmware transfer runs on the interface, warnings.DutyCycleThresholdTransfer (75 %)
// (occulited task 61; openccu-lite task 316 had 50 %); a carrier sense above 10 % (task 316). A
// kind clears once its value is radioLoadHysteresis points below.
const (
	radioLoadCarrierSense = 10
	radioLoadHysteresis   = 5
)

// radioLoadWarning: the radio sampler's last poll puts an interface's duty cycle at or above its
// threshold - 25 %, 75 % during a firmware transfer on that interface or on another stack of the
// same radio (occulited task 61, B-83) - or its
// carrier sense above 10 % (openccu-lite task 316). The variant names the kinds (`dc`, `cs`,
// `dc,cs`), the params the highest values and the interface of the highest one, and `transfer`
// when the duty cycle's interface has a transfer running. With hysteresis per interface, so a
// value around the threshold does not raise and clear the warning every sample; a transfer that
// ends while the value sits between the two thresholds raises it then, as the load outlives it.
func (a *SystemAPI) radioLoadWarning(_ context.Context) ([]warnings.Warning, bool) {
	polled, list := a.rfdPoll()
	if polled.IsZero() {
		return nil, false
	}
	dc, cs, csIface, hasCS := -1, -1, "", false
	for _, ri := range list {
		dc = max(dc, ri.DutyCycle)
		if ri.CarrierSense != nil && (!hasCS || *ri.CarrierSense > cs) {
			cs, csIface, hasCS = *ri.CarrierSense, ri.Interface, true
		}
	}
	// the firmware service is asked only when a duty cycle could warn at all
	// occulited B-83: per entry, so a transfer counts for every stack on the same radio
	onRadio := make([]bool, len(list))
	if dc >= warnings.DutyCycleThreshold-radioLoadHysteresis {
		if transfers := a.transferringInterfaces(); len(transfers) > 0 {
			p, ok := a.radioPlan()
			onRadio = TransferEntries(p, ok, list, transfers)
		}
	}
	a.warn.loadMu.Lock()
	defer a.warn.loadMu.Unlock()
	if a.warn.loadOn == nil {
		a.warn.loadOn = map[string]bool{}
	}
	// at or above the threshold raises (the maintainer: "warn starting with 25%"), the hysteresis
	// below it clears
	above := func(kind string, value, threshold int, inclusive bool) bool {
		on := a.warn.loadOn[kind]
		switch {
		case value > threshold || inclusive && value == threshold:
			on = true
		case value <= threshold-radioLoadHysteresis:
			on = false
		}
		a.warn.loadOn[kind] = on
		return on
	}
	var kinds []string
	params := map[string]any{}
	iface := ""
	// the duty cycle per interface, each against its own threshold; the highest warning one is named
	dcWarn, dcIface, dcTransfer := -1, "", false
	for i, ri := range list {
		threshold := warnings.DutyCycleThreshold
		if onRadio[i] {
			threshold = warnings.DutyCycleThresholdTransfer
		}
		if above("dc:"+ri.Interface+"/"+ri.Address, ri.DutyCycle, threshold, true) && ri.DutyCycle > dcWarn {
			dcWarn, dcIface, dcTransfer = ri.DutyCycle, ri.Interface, onRadio[i]
		}
	}
	if dcWarn >= 0 {
		kinds = append(kinds, "dc")
		params["duty_cycle"], iface = dcWarn, dcIface
		if dcTransfer {
			params["transfer"] = true
		}
	}
	if hasCS && above("cs", cs, radioLoadCarrierSense, false) {
		kinds = append(kinds, "cs")
		params["carrier_sense"] = cs
		if iface == "" {
			iface = csIface
		}
	}
	if len(kinds) == 0 {
		return nil, true
	}
	params["interface"] = iface
	// beside a carrier-sense warning the highest duty cycle is told too; a duty-cycle warning keeps
	// the value of the interface it names (another one may be higher and under its own threshold
	// during a transfer)
	if _, ok := params["duty_cycle"]; !ok && dc >= 0 {
		params["duty_cycle"] = dc
	}
	if hasCS {
		params["carrier_sense"] = cs
	}
	return []warnings.Warning{{ID: "radio-load", Variant: strings.Join(kinds, ","), Severity: warnings.SeverityWarning, Href: "/radio", Params: params}}, true
}
