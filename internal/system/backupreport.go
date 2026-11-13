package system

// occulited task 142 (openccu-lite/openccu-lite#19, D-125): what an uploaded backup is, before
// anything is done with it. An (Open)CCU or CCU3 backup is imported, an openccu-lite backup is
// restored, and the page says which this file is, where it is from and what it holds.
//
// One walk of the archive in Go answers it. The outer archive cannot tell the two kinds apart:
// firmware_version is "VERSION=<base>" on both (createBackup.sh writes that one line). So the
// content decides - occulited's accounts, and a ReGa database that is not newer than them.

import (
	"archive/tar"
	"encoding/json"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/hbrfeth"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/radio"
)

// The two kinds of backup.
const (
	BackupKindLite = "openccu-lite"
	BackupKindCCU  = "ccu"
)

// The import's selectable groups (D-125 and the maintainer's addition of 2026-11-10): the paired
// devices, their keys and the names are the import itself and no group.
const (
	GroupAddons     = "addons"
	GroupHBRFETH    = "hb_rf_eth"
	GroupHostname   = "hostname"
	GroupLogHost    = "loghost"
	GroupClassicRPC = "classic_rpc"
)

// ImportGroups are the groups in the order the page lists them.
var ImportGroups = []string{GroupAddons, GroupHBRFETH, GroupHostname, GroupLogHost, GroupClassicRPC}

// BackupAddon is one addon a backup holds: an rc.d entry and what belongs to it.
type BackupAddon struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Files and Bytes are the regular files of the addon's directory in the archive
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// Program is false when the backup has the entry but nothing of the addon's directory
	// (.nobackup on its program): its settings come, the addon has to be installed again
	Program bool `json:"program"`
	// WWW and Config: the web entry and the config directory are in the backup
	WWW    bool `json:"www,omitempty"`
	Config bool `json:"config,omitempty"`
}

// BackupSetting is one setting of the backup: its value, and whether this system's own setter
// would take it (a value it would refuse is shown and cannot be imported).
type BackupSetting struct {
	Value string `json:"value"`
	Valid bool   `json:"valid"`
}

// BackupClassicRPC is what the backup's firewall.conf says about the XMLRPC ports.
type BackupClassicRPC struct {
	// XMLRPC is the service's access: full, restricted or none
	XMLRPC string `json:"xmlrpc"`
	// Sources are where the converted rules would allow clients from: "local networks", "0/0",
	// or the allowed addresses of a restricted service
	Sources []string `json:"sources"`
	// Auth: the old system asked the ReGa accounts for a login (authEnabled), which cannot come
	// along - classic RPC is then not offered
	Auth bool `json:"auth"`
	// Old is the parsed file for the conversion; not part of the answer
	Old firewall.Old `json:"-"`
}

// Open says whether the old system let clients reach the XMLRPC ports.
func (c *BackupClassicRPC) Open() bool {
	return c != nil && (c.XMLRPC == "full" || c.XMLRPC == "restricted")
}

// BackupReport is the answer.
type BackupReport struct {
	Kind string `json:"kind"`
	// System names the firmware by the version's shape: openccu-lite, OpenCCU, CCU3, CCU2, CCU
	System      string `json:"system"`
	Version     string `json:"version,omitempty"`
	LiteVersion string `json:"lite_version,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	KeyIndex    int    `json:"key_index"`
	// Radio is the paired devices and the radio identity (task 251)
	Radio RadioBackup `json:"radio"`
	// Rega: a ReGa database - its names, rooms and functions can be imported
	Rega bool `json:"rega"`
	// Accounts: occulited's users.json is in the backup; Users and Tokens count what it holds
	Accounts bool                     `json:"accounts"`
	Users    int                      `json:"users"`
	Tokens   int                      `json:"tokens"`
	Addons   []BackupAddon            `json:"addons"`
	Settings map[string]BackupSetting `json:"settings"`
	// ClassicRPC is nil without a firewall.conf
	ClassicRPC *BackupClassicRPC `json:"classic_rpc,omitempty"`
	// LeftBehind are the ids of what this backup holds and an import does not take
	LeftBehind []string `json:"left_behind"`
	// AddonEntries is hm_addons.cfg as parsed, for the import; not part of the answer
	AddonEntries map[string]AddonSettings `json:"-"`
}

// Addon is the backup's addon of that id, nil when it has none.
func (b *BackupReport) Addon(id string) *BackupAddon {
	for i := range b.Addons {
		if b.Addons[i].ID == id {
			return &b.Addons[i]
		}
	}
	return nil
}

// usrLocal is where everything lies inside usr_local.tar.gz.
const usrLocal = "usr/local/"

// What an import leaves behind, by id; the page has a sentence for each.
const (
	LeftRega        = "rega"
	LeftUsers       = "users"
	LeftNetwork     = "network"
	LeftFirewall    = "firewall"
	LeftCertificate = "certificate"
	LeftSSH         = "ssh"
	LeftTime        = "time"
	LeftLogLevels   = "loglevels"
	LeftCron        = "cron"
	LeftAddonExtras = "addon_extras"
	LeftNeoServer   = "neoserver"
	LeftOther       = "other"
)

// BackupSystemName names the firmware a version belongs to.
func BackupSystemName(kind, version string) string {
	if kind == BackupKindLite {
		return "openccu-lite"
	}
	parts := strings.Split(version, ".")
	switch {
	case len(parts) >= 4:
		return "OpenCCU" // 3.x.y.<date>: OpenCCU and RaspberryMatic
	case len(parts) == 3 && parts[0] == "3":
		return "CCU3"
	case len(parts) == 3 && parts[0] == "2":
		return "CCU2"
	}
	return "CCU"
}

// firstLine is the first line of s, trimmed.
func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(l)
}

// backupAddonID says whether an rc.d entry's name is an addon's: the id's shape, not the script
// beside openccu-lite's wrapper, not the NEO Server's entry (OpenCCU unpacks that one itself and
// the first boot here removes it, task 37).
func backupAddonID(name string) bool {
	return addonIDRe.MatchString(name) && !strings.HasSuffix(name, ".script") && name != priv.NeoServerRCEntry
}

// InspectBackup reads what the backup is and holds, without touching the system.
func InspectBackup(sbk string) (BackupReport, error) {
	f, err := os.Open(sbk)
	if err != nil {
		return BackupReport{}, err
	}
	defer f.Close()
	return inspectBackupFrom(f)
}

func inspectBackupFrom(f io.Reader) (BackupReport, error) {
	b := BackupReport{Addons: []BackupAddon{}, Settings: map[string]BackupSetting{}, LeftBehind: []string{}}
	var (
		usersAt, regaAt time.Time
		authEnabled     bool
		allowExternal   bool
		fw              *Firewall
		left            = map[string]bool{}
		rc              = map[string]bool{}
		homes           = map[string]*BackupAddon{}
		versions        = map[string]string{}
		www, config     = map[string]bool{}, map[string]bool{}
	)
	home := func(id string) *BackupAddon {
		if homes[id] == nil {
			homes[id] = &BackupAddon{ID: id}
		}
		return homes[id]
	}
	info, err := walkSBKFrom(f, func(h *tar.Header, r io.Reader) error {
		b.Radio.inspectMember(h, r)
		name := path.Clean(h.Name)
		rel, ok := strings.CutPrefix(name, usrLocal)
		if !ok || strings.Contains(name, "..") {
			return nil
		}
		reg := h.Typeflag == tar.TypeReg
		small := func(limit int64) string {
			if !reg {
				return ""
			}
			d, _ := io.ReadAll(io.LimitReader(r, limit))
			return string(d)
		}
		if rest, ok := strings.CutPrefix(rel, "addons/"); ok {
			id, below, _ := strings.Cut(rest, "/")
			if !addonIDRe.MatchString(id) {
				return nil
			}
			a := home(id)
			if reg {
				a.Files++
				a.Bytes += h.Size
				if below == "VERSION" {
					if v := firstLine(small(256)); addonVersionRe.MatchString(v) {
						versions[id] = v
					}
				}
			}
			return nil
		}
		cfg, ok := strings.CutPrefix(rel, "etc/config/")
		if !ok {
			switch {
			case rel == "etc/occulite/users.json" && reg:
				b.Accounts, usersAt = true, h.ModTime
				var doc struct {
					Users  []json.RawMessage `json:"users"`
					Tokens []json.RawMessage `json:"tokens"`
				}
				if json.Unmarshal([]byte(small(4<<20)), &doc) == nil {
					b.Users, b.Tokens = len(doc.Users), len(doc.Tokens)
				}
			case strings.HasPrefix(rel, "etc/ssh"):
				left[LeftSSH] = true
			case rel == "crontabs/root" && reg && h.Size > 0, strings.HasPrefix(rel, "etc/monit-"):
				left[LeftCron] = true
			}
			return nil
		}
		switch {
		case cfg == "homematic.regadom" && reg:
			b.Rega, regaAt = true, h.ModTime
		case cfg == "netconfig" && reg:
			kv := radio.ParseKV(small(64 << 10))
			b.Hostname = strings.Trim(kv["HOSTNAME"], `"'`)
			left[LeftNetwork] = true
		case cfg == "hb_rf_eth" && reg:
			if v := firstLine(small(256)); v != "" {
				b.Settings[GroupHBRFETH] = BackupSetting{Value: v, Valid: hbrfeth.ValidAddress(v) == nil}
			}
		case cfg == "syslog" && reg:
			kv := parseSyslogConfig(small(64 << 10))
			if v := strings.TrimSpace(kv["LOGHOST"]); v != "" {
				b.Settings[GroupLogHost] = BackupSetting{Value: v, Valid: validLogHost(v)}
			}
			for k := range kv {
				if strings.HasPrefix(k, "LOGLEVEL_") {
					left[LeftLogLevels] = true
				}
			}
		case cfg == "firewall.conf" && reg:
			p := ParseFirewall(small(1 << 20))
			fw = &p
		case cfg == "authEnabled":
			authEnabled = true
		case cfg == "AllowExternalAccess":
			allowExternal = true
		case cfg == "hm_addons.cfg" && reg:
			b.AddonEntries = ParseHMAddonsCfg(small(1 << 20))
		case cfg == "server.pem":
			left[LeftCertificate] = true
		case cfg == "shadow", cfg == "authorized_keys":
			left[LeftSSH] = true
		case cfg == "TZ", cfg == "timezone", cfg == "ntpclient", cfg == "time.conf":
			left[LeftTime] = true
		case strings.HasPrefix(cfg, "rc.d/"):
			n := strings.TrimPrefix(cfg, "rc.d/")
			switch {
			case n == priv.NeoServerRCEntry:
				left[LeftNeoServer] = true
			case backupAddonID(n) && (reg || h.Typeflag == tar.TypeSymlink):
				rc[n] = true
			}
		case strings.HasPrefix(cfg, "addons/www/"):
			if id, _, _ := strings.Cut(strings.TrimPrefix(cfg, "addons/www/"), "/"); addonIDRe.MatchString(id) {
				www[id] = true
			}
		case strings.HasPrefix(cfg, "addons/"):
			if id, _, _ := strings.Cut(strings.TrimPrefix(cfg, "addons/"), "/"); addonIDRe.MatchString(id) {
				config[id] = true
			}
		}
		return nil
	})
	if err != nil {
		return b, err
	}
	b.KeyIndex, b.Version, b.LiteVersion = info.KeyIndex, info.Firmware["VERSION"], info.Firmware["LITE"]
	b.Radio.KeyIndex, b.Radio.Version = b.KeyIndex, b.Version
	sort.Strings(b.Radio.Files)
	// the kind: a LITE line wins (no image writes it yet); else occulited's accounts, unless a ReGa
	// database in the same backup is newer than them - an OpenCCU that once ran openccu-lite keeps
	// the directory on its userfs and its ReGa keeps writing, while a leftover database on
	// openccu-lite is frozen at the day of the switch
	b.Kind = BackupKindCCU
	if b.LiteVersion != "" || (b.Accounts && !(b.Rega && regaAt.After(usersAt))) {
		b.Kind = BackupKindLite
	}
	b.System = BackupSystemName(b.Kind, b.Version)
	if b.Hostname != "" {
		b.Settings[GroupHostname] = BackupSetting{Value: b.Hostname, Valid: hostnameRe.MatchString(b.Hostname)}
	}
	if fw != nil {
		c := &BackupClassicRPC{XMLRPC: "none", Sources: []string{}, Auth: authEnabled}
		c.Old = firewall.Old{Mode: fw.Mode, IPs: fw.IPs, UserPorts: fw.UserPorts, AllowExternal: allowExternal}
		// OpenCCU ships MOST_OPEN and this system's default is DROP (D-29): as the firmware
		// update's conversion on a system that never applied B-43's default
		if c.Old.Mode == "MOST_OPEN" {
			c.Old.Mode = "RESTRICTIVE"
		}
		for _, s := range fw.Services {
			c.Old.Services = append(c.Old.Services, firewall.OldService{ID: s.ID, Ports: s.Ports, Access: s.Access})
			if s.ID == "XMLRPC" && (s.Access == "full" || s.Access == "restricted") {
				c.XMLRPC = s.Access
			}
		}
		switch {
		case c.XMLRPC == "restricted":
			c.Sources = append(c.Sources, fw.IPs...)
		case c.XMLRPC == "full" && allowExternal:
			c.Sources = []string{firewall.AnySource}
		case c.XMLRPC == "full":
			c.Sources = []string{firewall.LocalNetworks}
		}
		b.ClassicRPC = c
		left[LeftFirewall] = true
	}
	ids := make([]string, 0, len(rc))
	for id := range rc {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := BackupAddon{ID: id, Name: id, Version: versions[id], WWW: www[id], Config: config[id]}
		if h := homes[id]; h != nil {
			a.Files, a.Bytes, a.Program = h.Files, h.Bytes, h.Files > 0
		}
		if n := b.AddonEntries[id].Name; n != "" {
			a.Name = n
		} else if n := KnownAddonNames[id]; n != "" {
			a.Name = n
		}
		b.Addons = append(b.Addons, a)
	}
	for id := range homes {
		switch {
		case rc[id]:
		case id == path.Base(priv.NeoServerHome):
			left[LeftNeoServer] = true
		default:
			left[LeftAddonExtras] = true
		}
	}
	if len(b.Addons) > 0 {
		left[LeftAddonExtras] = true // what an addon keeps outside its own directories
	}
	if b.Rega {
		left[LeftRega], left[LeftUsers] = true, true
	}
	left[LeftOther] = true
	for _, id := range []string{LeftRega, LeftUsers, LeftNetwork, LeftFirewall, LeftCertificate, LeftSSH, LeftTime, LeftLogLevels, LeftCron, LeftAddonExtras, LeftNeoServer, LeftOther} {
		if left[id] {
			b.LeftBehind = append(b.LeftBehind, id)
		}
	}
	return b, nil
}

// BackupReports keeps the report of the checked upload: the check, the view, the import and the
// restore each ask for it, and a backup with a large addon is 300 MB of gzip to walk.
type BackupReports struct {
	mu   sync.Mutex
	path string
	size int64
	mod  time.Time
	rep  BackupReport
}

// For is the report of the file at path - the kept one while the file is the same.
func (c *BackupReports) For(sbk string) (BackupReport, error) {
	st, err := os.Stat(sbk)
	if err != nil {
		return BackupReport{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == sbk && c.size == st.Size() && c.mod.Equal(st.ModTime()) {
		return c.rep, nil
	}
	rep, err := InspectBackup(sbk)
	if err != nil {
		return rep, err
	}
	c.path, c.size, c.mod, c.rep = sbk, st.Size(), st.ModTime(), rep
	return rep, nil
}
