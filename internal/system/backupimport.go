package system

// occulited task 142 (D-125): what the import of an (Open)CCU backup carries besides the paired
// devices, their keys and the names - the groups the user chooses from. Each is applied the way
// this system's own setter would apply the value; nothing of the backup's files is copied as it
// is, except the addons' own trees (the helper's addon-import).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonimport"
	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/hbrfeth"
	"github.com/hobbyquaker/occulited/internal/radio"
)

// ImportGroupAddon is one addon of the backup as the import sees it on this system.
type ImportGroupAddon struct {
	BackupAddon
	// Here: something of that id is on this system already - it is not imported, the installed
	// one stays as it is
	Here bool `json:"here,omitempty"`
}

// ImportGroup is one selectable group of the import: what it is in this backup, what this system
// has now, and whether it is ticked by default.
type ImportGroup struct {
	ID string `json:"id"`
	// Value is the setting's value in the backup; Current this system's
	Value   string `json:"value,omitempty"`
	Current string `json:"current,omitempty"`
	Default bool   `json:"default"`
	// Unavailable says why the group is shown and cannot be chosen: "invalid" (this system's
	// setter refuses the value), "auth" (classic RPC: the old system asked the ReGa accounts for a
	// login), "on" (classic RPC is on here already), "here" (every addon is installed here
	// already). Empty: it can be chosen.
	Unavailable string             `json:"unavailable,omitempty"`
	Addons      []ImportGroupAddon `json:"addons,omitempty"`
	// Sources are classic RPC's: where the converted rules allow clients from
	Sources []string `json:"sources,omitempty"`
}

// addonHere says whether anything of the id is installed: what the helper's import refuses.
func (r Root) addonHere(id string) bool {
	for _, p := range []string{"/usr/local/addons/" + id, "/usr/local/etc/config/rc.d/" + id, "/usr/local/etc/config/rc.d/" + id + ".script", AddonWWW + "/" + id, "/usr/local/etc/config/addons/" + id} {
		if _, err := os.Lstat(r.join(p)); err == nil {
			return true
		}
	}
	return false
}

// ImportGroups are the groups this backup offers on this system, in the page's order. A group
// the backup does not hold is not listed. Only an (Open)CCU backup has any: an openccu-lite
// backup's addons carry wrappers, units and policies, and that is the restore.
func (r Root) ImportGroups(b BackupReport) []ImportGroup {
	out := []ImportGroup{}
	if b.Kind != BackupKindCCU {
		return out
	}
	if len(b.Addons) > 0 {
		g := ImportGroup{ID: GroupAddons, Default: true, Unavailable: "here"}
		for _, a := range b.Addons {
			ga := ImportGroupAddon{BackupAddon: a, Here: r.addonHere(a.ID)}
			if !ga.Here {
				g.Unavailable = ""
			}
			g.Addons = append(g.Addons, ga)
		}
		g.Default = g.Unavailable == ""
		out = append(out, g)
	}
	setting := func(id, current string, def bool) {
		s, ok := b.Settings[id]
		if !ok {
			return
		}
		g := ImportGroup{ID: id, Value: s.Value, Current: current, Default: def}
		if !s.Valid {
			g.Unavailable, g.Default = "invalid", false
		}
		out = append(out, g)
	}
	setting(GroupHBRFETH, radio.HBRFETHAddress(string(r)), true)
	// the host name: ticked only while this system has none of its own (netconfig's HOSTNAME is
	// empty on a fresh system; the name it shows is the image's)
	net := r.ReadNetwork()
	setting(GroupHostname, net.Hostname, strings.TrimSpace(net.Raw["HOSTNAME"]) == "")
	setting(GroupLogHost, r.ReadLogLevels().LogHost, true)
	if c := b.ClassicRPC; c.Open() {
		g := ImportGroup{ID: GroupClassicRPC, Sources: c.Sources}
		here := r.ReadClassicRPC()
		switch {
		case c.Auth:
			g.Unavailable = "auth"
		case here.Plain || here.TLS:
			g.Unavailable = "on"
		}
		out = append(out, g)
	}
	return out
}

// ErrGroupUnavailable is a group the request names that this backup does not offer here.
var ErrGroupUnavailable = errors.New("group not available")

// CheckImportGroups says whether every asked group can be taken from this backup on this system.
func (r Root) CheckImportGroups(b BackupReport, asked []string) error {
	offered := map[string]ImportGroup{}
	for _, g := range r.ImportGroups(b) {
		offered[g.ID] = g
	}
	seen := map[string]bool{}
	for _, id := range asked {
		g, ok := offered[id]
		switch {
		case seen[id]:
			return fmt.Errorf("%w: %s is named twice", ErrGroupUnavailable, id)
		case !ok:
			return fmt.Errorf("%w: this backup does not offer %s", ErrGroupUnavailable, id)
		case g.Unavailable != "":
			return fmt.Errorf("%w: %s cannot be imported here (%s)", ErrGroupUnavailable, id, g.Unavailable)
		}
		seen[id] = true
	}
	return nil
}

// ImportedAddon is what became of one addon of the backup.
type ImportedAddon struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Imported bool   `json:"imported"`
	// Enabled: it starts with the system. Disabled says why not: "rega" (it needs the ReGa),
	// "binary" (its programs are for another architecture), "backup" (it was switched off on the
	// old system), "program" (the backup holds its settings and not its program: install it again)
	Enabled  bool   `json:"enabled"`
	Disabled string `json:"disabled,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Error says why it was not imported: "exists", "entry", "no-entry", or the failure
	Error string `json:"error,omitempty"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// ImportAddons brings the backup's addons onto this system: the helper extracts each addon's own
// trees (never one of an id that is here already), then every imported addon is judged as the
// first boot after an update judges it - one that needs the ReGa or whose programs cannot run
// here stays disabled, as does one that was disabled on the old system - and gets its
// hm_addons.cfg entry. pin, when not nil, records how the addon runs (root, "migrated").
func (r Root) ImportAddons(ctx context.Context, sbk string, b BackupReport, pin func(ctx context.Context, id string) error) ([]ImportedAddon, error) {
	var ids []string
	for _, a := range b.Addons {
		ids = append(ids, a.ID)
	}
	if len(ids) == 0 {
		return []ImportedAddon{}, nil
	}
	f, err := os.Open(sbk)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	res, err := Priv.ImportAddons(f, string(r), ids)
	if err != nil {
		return nil, fmt.Errorf("the addons could not be taken from the backup: %w", err)
	}
	by := map[string]addonimport.Result{}
	for _, x := range res {
		by[x.ID] = x
	}
	out := make([]ImportedAddon, 0, len(ids))
	cfgPath := r.join("/usr/local/etc/config/hm_addons.cfg")
	entries := ParseHMAddonsCfg(readFile(cfgPath))
	entriesChanged := false
	for _, a := range b.Addons {
		x := by[a.ID]
		ia := ImportedAddon{ID: a.ID, Name: a.Name, Imported: x.Imported, Error: x.Error, Files: x.Files, Bytes: x.Bytes}
		if !x.Imported {
			if ia.Error == "" {
				ia.Error = "failed"
			}
			out = append(out, ia)
			continue
		}
		ForgetAddonScans(a.ID)
		switch {
		case x.Files == 0:
			ia.Disabled = "program"
		case !x.WasEnabled:
			ia.Disabled = "backup"
		default:
			if dep, why := r.RegaDependence(a.ID); dep {
				ia.Disabled, ia.Reason = "rega", why
			} else if bad, why := r.BinaryCompatibility(a.ID); bad {
				ia.Disabled, ia.Reason = "binary", why
			}
		}
		// "program" keeps its bit: the unit's own condition skips an addon without its program,
		// and the Addons page then offers the reinstall (the restore's case, handbook chapter 11)
		if ia.Disabled == "" || (ia.Disabled == "program" && x.WasEnabled) {
			if err := r.SetAddonEnabled(a.ID, true); err != nil {
				if ia.Disabled == "" {
					ia.Disabled, ia.Reason = "error", err.Error()
				}
			} else if ia.Disabled == "" {
				ia.Enabled = true
			}
		}
		if e, ok := b.AddonEntries[a.ID]; ok && (e.ConfigURL != "" || e.Name != "") {
			if _, has := entries[a.ID]; !has {
				entries[a.ID] = e
				entriesChanged = true
			}
		}
		if pin != nil {
			if err := pin(ctx, a.ID); err != nil && ia.Reason == "" {
				ia.Reason = "policy: " + err.Error()
			}
		}
		out = append(out, ia)
	}
	if entriesChanged {
		if err := writeFileAtomic(cfgPath, []byte(WriteHMAddonsCfg(entries)), 0o664); err != nil {
			return out, fmt.Errorf("hm_addons.cfg: %w", err)
		}
	}
	return out, nil
}

// ImportHBRFETH sets the HB-RF-ETH's address to the backup's, checked as the Interfaces page's
// setter checks it.
func (r Root) ImportHBRFETH(addr string) error {
	addr = strings.TrimSpace(addr)
	if err := hbrfeth.ValidAddress(addr); err != nil {
		return err
	}
	return r.WriteHBRFETH(addr)
}

// ImportHostname writes the backup's host name into this system's netconfig - that key alone;
// the address, the mode and the name servers stay this system's. The boot applies it.
func (r Root) ImportHostname(name string) error {
	if !hostnameRe.MatchString(name) {
		return errors.New("not a host name: letters, digits and hyphens, at most 63 characters")
	}
	s := r.ReadNetwork().Settings()
	s.Hostname = name
	return r.WriteNetconfig(s)
}

// ImportLogHost sets LOGHOST to the backup's and leaves the log levels this system's.
func (r Root) ImportLogHost(host string) error {
	l := r.ReadLogLevels()
	l.LogHost = strings.TrimSpace(host)
	if l.LogHost == "" || !validLogHost(l.LogHost) {
		return errors.New("syslog server must be a host name or an address, optionally with :port")
	}
	_, _, err := r.SetLogLevels(l)
	return err
}

// ImportClassicRPC does for the backup's firewall.conf what the firmware update's conversion does
// for the XMLRPC ports (openccu-lite task 143): classic RPC switched on, plain and TLS, and its
// rules the converted ones with their old sources. Nothing else of the file is taken. It answers
// the number of rules; lighttpd and the firewall pick both up at the next start (the import
// reboots).
func (r Root) ImportClassicRPC(c *BackupClassicRPC) (int, error) {
	if !c.Open() {
		return 0, errors.New("the backup's firewall did not open the XMLRPC ports")
	}
	if c.Auth {
		return 0, errors.New("the old system asked for a login on the XMLRPC ports, which cannot come along")
	}
	cur, ok := r.ReadRules()
	if !ok {
		return 0, errors.New("this system has no firewall rules yet")
	}
	specs := classicSpecs(ClassicRPCPorts(true, true, true))
	conv := firewall.Migrate(firewall.MigrateInputs{Old: c.Old, Owners: map[string][]firewall.PortSpec{firewall.OwnerRPC: specs}}, time.Now())
	var rules []firewall.Rule
	for _, x := range conv.Rules {
		if x.Owner == firewall.OwnerRPC {
			rules = append(rules, x)
		}
	}
	next := firewall.AdoptOwned(cur, firewall.OwnerRPC, specs, rules)
	if err := next.Validate(); err != nil {
		return 0, fmt.Errorf("the converted rules: %w", err)
	}
	if err := r.switchClassicRPCOn(); err != nil {
		return 0, err
	}
	if err := r.WriteRules(next); err != nil {
		return 0, err
	}
	return len(rules), nil
}
