package system

// openccu-lite task 251 (the maintainer: "as we can import rega names from a homematic backup .sbk
// it would be also nice to have 'import paired devices' that fetches everything needed that devices
// pairings (hmip, bidcos-rf and -wired) and possibly keys that are present are imported"): the
// paired devices of a CCU/OpenCCU backup, with the radio identity they are bound to, onto a system
// that has none yet.
//
// What comes across (decision 2, all three radios, all or nothing): rfd's device files, address
// file and AES key store (/etc/config/rfd, ids, keys with crypttool.cfg), hmipserver's crRFD tree
// (the HmIP identity bound to the old module, the device files, the device key map, local key
// mode's hmip_user.conf) and hmip_address.conf, hs485d's device files, and the daemons' own
// configurations with their LAN gateways (rfd.conf, hs485d.conf - the radio plan cuts them for
// this system's hardware at boot, as it does for a userfs switched in place, D-99). Not the
// interface list (the plan renders it), not the ReGa database (the names import), nothing else.
//
// Why a reboot: the address file, the identity and the key store are read by the daemons at
// start, the plan renders the daemons' configurations from them, and hmipserver moves the
// imported identity onto this system's module the way it does after a CCU restore onto new
// hardware - the adapter exchange, with eQ-3's key server or offline in local key mode (task 155's
// diagnosis says how it went). The import writes the files and the caller reboots, like the
// restore.
//
// Why only onto a system without paired devices (decision 1): the import replaces the radio
// identity - a BidCos device knows its central by address and key, an HmIP device by the identity
// - so devices paired here before would be orphaned. The target's own radio files are moved aside
// under /usr/local/etc/config/.import-devices-aside/<time>/, never deleted.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/radio"
)

// RadioBackup is what a backup holds of the three radios.
type RadioBackup struct {
	BidCosRF struct {
		Devices int    `json:"devices"`
		Address string `json:"address,omitempty"`
		Serial  string `json:"serial,omitempty"`
		// HasKey: the AES key store is not empty (an individual security key was set)
		HasKey   bool `json:"has_key"`
		Gateways int  `json:"gateways"`
	} `json:"bidcos_rf"`
	HmIP struct {
		Devices int `json:"devices"`
		// IdentitySGTIN is the module the identity files are bound to; "" without an identity
		IdentitySGTIN string `json:"identity_sgtin,omitempty"`
		LocalKey      bool   `json:"local_key"`
		DeviceKeyMap  bool   `json:"device_key_map"`
		// SecurityCounter is what the access point file says about the HmIP security counter
		// (openccu-lite task 299): what hmipserver would compute at the first start here.
		SecurityCounter *CounterOutlook `json:"security_counter,omitempty"`
	} `json:"hmip"`
	BidCosWired struct {
		Devices  int `json:"devices"`
		Gateways int `json:"gateways"`
	} `json:"bidcos_wired"`
	// KeyIndex is the backup's key_index file (0: the default key).
	KeyIndex int `json:"key_index"`
	// Files are the archive members the import takes, relative to /usr/local.
	Files []string `json:"files"`
	// Version is the backup's firmware version.
	Version string `json:"version,omitempty"`
}

// CounterOutlook is the HmIP security counter as the access point file foretells it: the first
// connection and the offset from the file, the value hmipserver would compute now, and the verdict
// (fine, near, wrapped; behind when the clock is before the first connection).
type CounterOutlook struct {
	FirstConnect time.Time `json:"first_connect"`
	Offset       uint64    `json:"offset"`
	Calc         uint64    `json:"calc"`
	Behind       bool      `json:"behind,omitempty"`
	Verdict      string    `json:"verdict"`
	WrapsAt      time.Time `json:"wraps_at"`
}

// counterOutlook judges an access point file's bytes at now; nil when they are not one.
func counterOutlook(b []byte, now time.Time) *CounterOutlook {
	ap, err := radio.ParseAccessPoint(b)
	if err != nil {
		return nil
	}
	calc, behind := ap.Calc(now)
	return &CounterOutlook{FirstConnect: ap.FirstConnect, Offset: ap.Offset, Calc: calc, Behind: behind, Verdict: radio.CounterVerdict(0, calc, true, 0), WrapsAt: ap.WrapsAt()}
}

// radioNow is the clock the inspection judges the counter by; a test's seam.
var radioNow = time.Now

// Empty says whether the backup holds no pairing at all.
func (b RadioBackup) Empty() bool {
	return b.BidCosRF.Devices == 0 && b.HmIP.Devices == 0 && b.BidCosWired.Devices == 0 && b.HmIP.IdentitySGTIN == "" && b.BidCosRF.Address == ""
}

// NonDefaultKey says whether the backup's BidCos-RF key store is not the factory key (openccu-lite
// task 278): its key_index counts the key changes since the factory key, and rfd's key store is
// persisted only once a key was set. The import takes the store as it is, without the passphrase
// that derived it (rfd reads the derived key; nothing on this system needs the passphrase later),
// and tells the user so.
func (b RadioBackup) NonDefaultKey() bool { return b.KeyIndex > 0 || b.BidCosRF.HasKey }

// configPrefix is where the radio files live inside usr_local.tar.gz.
const configPrefix = "usr/local/etc/config/"

// radioImportDirs are the directories taken whole, radioImportFiles the single files.
var (
	radioImportDirs  = []string{"rfd", "crRFD", "hs485d"}
	radioImportFiles = []string{"ids", "keys", "crypttool.cfg", "hmip_address.conf", "rfd.conf", "hs485d.conf"}
	lgwRFRe          = regexp.MustCompile(`(?m)^Type = (HMLGW2|Lan Interface)`)
	lgwWiredRe       = regexp.MustCompile(`(?m)^Type = HMWLGW`)
	sgtinFileRe      = regexp.MustCompile(`^([0-9A-F]{24})\.(ap|apkx|bbkx)$`)
)

// radioMember says whether an archive member (usr/local/...) is one the import takes.
func radioMember(name string) bool {
	if !strings.HasPrefix(name, configPrefix) {
		return false
	}
	rel := strings.TrimPrefix(name, configPrefix)
	for _, f := range radioImportFiles {
		if rel == f {
			return true
		}
	}
	for _, d := range radioImportDirs {
		if strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// radioMemberOK refuses what must never come out of an archive into /etc/config: a path that
// climbs, a link, anything but a regular file or a directory.
func radioMemberOK(h *tar.Header) bool {
	clean := path.Clean(h.Name)
	if clean != strings.TrimSuffix(h.Name, "/") || strings.Contains(clean, "..") || strings.HasPrefix(clean, "/") {
		return false
	}
	return h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeDir
}

// maxRadioFile bounds one member: a device file is a few KB, sgtin.map a few hundred KB at most.
const maxRadioFile = 4 << 20

// ErrNotSBK says the file is not a CCU backup.
var ErrNotSBK = errors.New("not a CCU backup: no usr_local.tar.gz in the archive")

// walkSBK opens the .sbk (a tar) and hands every member of the inner usr_local.tar.gz to fn,
// along with the outer key_index and firmware_version.
func walkSBK(sbk string, fn func(h *tar.Header, r io.Reader) error) (keyIndex int, version string, err error) {
	f, err := os.Open(sbk)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	info, err := walkSBKFrom(f, fn)
	return info.KeyIndex, info.Firmware["VERSION"], err
}

// sbkInfo is what the outer archive says: key_index and firmware_version's lines.
type sbkInfo struct {
	KeyIndex int
	Firmware map[string]string
}

// walkSBKFrom is walkSBK on an open archive: the helper's addon import reads the descriptor the
// daemon handed it and never a path (occulited task 142).
func walkSBKFrom(f io.Reader, fn func(h *tar.Header, r io.Reader) error) (sbkInfo, error) {
	info := sbkInfo{Firmware: map[string]string{}}
	outer := tar.NewReader(f)
	found := false
	for {
		h, err := outer.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return info, fmt.Errorf("the backup archive: %w", err)
		}
		switch path.Base(h.Name) {
		case "key_index":
			b, _ := io.ReadAll(io.LimitReader(outer, 64))
			info.KeyIndex, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		case "firmware_version":
			b, _ := io.ReadAll(io.LimitReader(outer, 4096))
			info.Firmware = radio.ParseKV(string(b))
		case "usr_local.tar.gz":
			found = true
			gz, err := gzip.NewReader(outer)
			if err != nil {
				return info, fmt.Errorf("usr_local.tar.gz: %w", err)
			}
			inner := tar.NewReader(gz)
			for {
				ih, err := inner.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return info, fmt.Errorf("usr_local.tar.gz: %w", err)
				}
				if err := fn(ih, inner); err != nil {
					return info, err
				}
			}
		}
	}
	if !found {
		return info, ErrNotSBK
	}
	return info, nil
}

// InspectRadioBackup reads what the backup holds of the radios without touching the system.
func InspectRadioBackup(sbk string) (RadioBackup, error) {
	var b RadioBackup
	keyIndex, version, err := walkSBK(sbk, func(h *tar.Header, r io.Reader) error {
		b.inspectMember(h, r)
		return nil
	})
	if err != nil {
		return b, err
	}
	b.KeyIndex, b.Version = keyIndex, version
	sort.Strings(b.Files)
	return b, nil
}

// inspectMember takes what one archive member says about the radios; a member that is none of the
// radio files is ignored. InspectBackup's walk shares it (occulited task 142).
func (b *RadioBackup) inspectMember(h *tar.Header, r io.Reader) {
	if h.Typeflag != tar.TypeReg || !radioMember(h.Name) || !radioMemberOK(h) {
		return
	}
	rel := strings.TrimPrefix(h.Name, configPrefix)
	b.Files = append(b.Files, rel)
	small := func(limit int64) string {
		d, _ := io.ReadAll(io.LimitReader(r, limit))
		return string(d)
	}
	switch {
	case rel == "ids":
		ids := small(4096)
		kv := radio.ParseKV(ids)
		b.BidCosRF.Address, b.BidCosRF.Serial = radio.IDsAddress(ids), kv["SerialNumber"]
	case rel == "keys":
		b.BidCosRF.HasKey = h.Size > 0
	case rel == "rfd.conf":
		b.BidCosRF.Gateways = len(lgwRFRe.FindAllString(small(1<<20), -1))
	case rel == "hs485d.conf":
		b.BidCosWired.Gateways = len(lgwWiredRe.FindAllString(small(1<<20), -1))
	case strings.HasPrefix(rel, "rfd/") && strings.HasSuffix(rel, ".dev"):
		b.BidCosRF.Devices++
	case strings.HasPrefix(rel, "hs485d/") && strings.HasSuffix(rel, ".dev"):
		b.BidCosWired.Devices++
	case strings.HasPrefix(rel, "crRFD/data/") && strings.HasSuffix(rel, ".dev"):
		b.HmIP.Devices++
	case strings.HasPrefix(rel, "crRFD/data/"):
		if m := sgtinFileRe.FindStringSubmatch(path.Base(rel)); m != nil && m[2] == "ap" {
			b.HmIP.IdentitySGTIN = m[1]
			b.HmIP.SecurityCounter = counterOutlook([]byte(small(1<<16)), radioNow())
		}
	case rel == "crRFD/hmip_user.conf":
		b.HmIP.LocalKey = radio.ReadLocalKey(small(1 << 20)).Enabled()
	case rel == "crRFD/sgtin.map":
		b.HmIP.DeviceKeyMap = h.Size > 0
	}
}

// RadioImportResult is what the import did.
type RadioImportResult struct {
	Backup RadioBackup `json:"backup"`
	// Written are the files put in place, relative to /etc/config; Aside is where this system's
	// own radio files went.
	Written []string `json:"written"`
	Aside   string   `json:"aside,omitempty"`
	// NonDefaultKey: the backup's BidCos-RF key store is not the factory key and came along as it
	// is (task 278); TargetKeyReplaced: this system had a key store of its own, now aside.
	NonDefaultKey     bool `json:"non_default_key"`
	TargetKeyReplaced bool `json:"target_key_replaced"`
}

// asideDir is where the target's own radio files are moved before the import: under /etc/config
// (the userfs), a dotdir the backup leaves out (createBackup excludes usr/local/.* only at the top,
// so this one travels with a later backup - and that is right: it is the system's history).
const asideDir = "/etc/config/.import-devices-aside"

// owners are the users the daemons run as, for the imported files; a name the system lacks (a
// development root) leaves root's ownership, which the boot's prep step corrects for keys and ids.
var radioOwners = map[string]string{"rfd": "rfd", "crRFD": "hmipserver", "hs485d": "hs485d", "keys": "rfd", "ids": "rfd", "hmip_address.conf": "hmipserver"}

// ImportRadio puts the backup's radio files onto this system: the target's own moved aside, the
// backup's written, owned by the daemons. The backup's key store (keys, crypttool.cfg) comes as it
// is - rfd reads the derived key from it, and no passphrase is needed on this system (task 278:
// the stock CCU's restore asks for it as a proof of possession only; the import says instead that
// a non-default key came along). The caller has checked that nothing is paired here and, when this
// system has a key store of its own, that the user wants it replaced. It does not reboot.
func (r Root) ImportRadio(ctx context.Context, sbk string) (RadioImportResult, error) {
	_ = ctx // the import has no command to run; the context stays for the boundary's sake
	b, err := InspectRadioBackup(sbk)
	if err != nil {
		return RadioImportResult{}, err
	}
	res := RadioImportResult{Backup: b, Written: []string{}, NonDefaultKey: b.NonDefaultKey()}
	if st, err := os.Stat(r.join("/etc/config/keys")); err == nil && st.Size() > 0 {
		res.TargetKeyReplaced = true
	}
	if b.Empty() {
		return res, errors.New("the backup holds no paired device and no radio identity")
	}
	cfg := r.join("/etc/config")
	stamp := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	aside := r.join(filepath.Join(asideDir, stamp))
	moved := false
	for _, name := range append(append([]string{}, radioImportDirs...), radioImportFiles...) {
		src := filepath.Join(cfg, name)
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		if !moved {
			if err := Priv.MkdirAll(aside, 0o700); err != nil {
				return res, fmt.Errorf("aside directory: %w", err)
			}
			moved = true
		}
		if err := Priv.Rename(src, filepath.Join(aside, name)); err != nil {
			return res, fmt.Errorf("moving %s aside: %w", name, err)
		}
	}
	if moved {
		res.Aside = strings.TrimPrefix(aside, string(r))
	}
	// the files, small and few: written one by one through the boundary
	uids := map[string][2]int{}
	lookup := func(name string) ([2]int, bool) {
		if ids, ok := uids[name]; ok {
			return ids, ids[0] >= 0
		}
		u, err := user.Lookup(name)
		if err != nil {
			uids[name] = [2]int{-1, -1}
			return uids[name], false
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		uids[name] = [2]int{uid, gid}
		return uids[name], true
	}
	dirs := map[string]bool{}
	_, _, err = walkSBK(sbk, func(h *tar.Header, rd io.Reader) error {
		if !radioMember(h.Name) || !radioMemberOK(h) {
			return nil
		}
		rel := strings.TrimPrefix(h.Name, configPrefix)
		dst := filepath.Join(cfg, rel)
		if h.Typeflag == tar.TypeDir {
			if err := Priv.MkdirAll(dst, 0o775); err != nil {
				return err
			}
			dirs[rel] = true
			return nil
		}
		if h.Size > maxRadioFile {
			return fmt.Errorf("%s is larger than a radio file can be (%d bytes)", rel, h.Size)
		}
		data, err := io.ReadAll(io.LimitReader(rd, maxRadioFile+1))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if d := filepath.Dir(rel); d != "." && !dirs[d] {
			if err := Priv.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
				return err
			}
			dirs[d] = true
		}
		mode := os.FileMode(0o644)
		switch {
		case rel == "keys", strings.HasSuffix(rel, "/hmip_user.conf"):
			mode = 0o600
		case rel == "rfd.conf", rel == "hs485d.conf", rel == "crypttool.cfg":
			mode = 0o640
		case strings.HasPrefix(rel, "crRFD/data/"):
			// hmipserver's device and identity files are its own alone (openccu-lite B-253);
			// the prep step at its start makes the directory 0700 and owns the files
			mode = 0o600
		case strings.HasPrefix(rel, "crRFD/") || strings.HasPrefix(rel, "rfd/") || strings.HasPrefix(rel, "hs485d/"):
			mode = 0o664
		}
		if err := Priv.WriteFile(dst, data, mode); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		res.Written = append(res.Written, rel)
		return nil
	})
	if err != nil {
		return res, err
	}
	// the owners: each daemon's tree and files as its user (the boot's prep step repeats it for
	// the key store); rfd.conf and hs485d.conf stay root's, readable by the daemon's group
	for name, owner := range radioOwners {
		p := filepath.Join(cfg, name)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		ids, ok := lookup(owner)
		if !ok {
			continue
		}
		st, _ := os.Lstat(p)
		if err := Priv.Chown(p, ids[0], ids[1], st != nil && st.IsDir()); err != nil {
			return res, fmt.Errorf("owner of %s: %w", name, err)
		}
	}
	for _, name := range []string{"rfd.conf", "hs485d.conf"} {
		p := filepath.Join(cfg, name)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if ids, ok := lookup(radioOwners[strings.TrimSuffix(name, ".conf")]); ok {
			_ = Priv.Chown(p, 0, ids[1], false)
		}
	}
	sort.Strings(res.Written)
	return res, nil
}
