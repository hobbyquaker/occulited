package system

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// bm is one member of a test backup's usr_local.tar.gz.
type bm struct {
	name string
	body string
	mode int64
	link string
	at   time.Time
}

// backupOf writes a .sbk with these members and this firmware_version; a member with link is a
// symbolic link, one whose name ends in "/" a directory.
func backupOf(t *testing.T, firmware string, members ...bm) string {
	t.Helper()
	var inner bytes.Buffer
	gz := gzip.NewWriter(&inner)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Typeflag: tar.TypeReg, Mode: m.mode, Size: int64(len(m.body)), ModTime: m.at}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		switch {
		case m.link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, m.link, 0
		case strings.HasSuffix(m.name, "/"):
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(m.body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	var outer bytes.Buffer
	ow := tar.NewWriter(&outer)
	for _, m := range [][2]string{{"usr_local.tar.gz", inner.String()}, {"signature", "sig"}, {"key_index", "1\n"}, {"firmware_version", firmware}} {
		_ = ow.WriteHeader(&tar.Header{Name: m[0], Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(m[1]))})
		_, _ = ow.Write([]byte(m[1]))
	}
	_ = ow.Close()
	p := filepath.Join(t.TempDir(), "restore-test.sbk")
	if err := os.WriteFile(p, outer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const cfgDir = "usr/local/etc/config/"

var (
	day1 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	day2 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
)

// TestInspectBackupKind: what tells an openccu-lite backup from an (Open)CCU one.
func TestInspectBackupKind(t *testing.T) {
	users := bm{name: "usr/local/etc/occulite/users.json", body: `{"users":[{"name":"a"},{"name":"b"}],"tokens":[{"name":"t"}]}`, at: day2}
	oldUsers := users
	oldUsers.at = day1
	cases := []struct {
		name     string
		firmware string
		members  []bm
		kind     string
		system   string
	}{
		{"a CCU3", "VERSION=3.89.11\n", []bm{{name: cfgDir + "homematic.regadom", body: "x", at: day2}}, BackupKindCCU, "CCU3"},
		{"an OpenCCU", "VERSION=3.89.11.20260919\n", []bm{{name: cfgDir + "homematic.regadom", body: "x", at: day2}}, BackupKindCCU, "OpenCCU"},
		{"a CCU2", "VERSION=2.61.7\n", nil, BackupKindCCU, "CCU2"},
		{"no version at all", "", nil, BackupKindCCU, "CCU"},
		{"openccu-lite", "VERSION=3.89.11.20260919\n", []bm{users}, BackupKindLite, "openccu-lite"},
		{"openccu-lite with the ReGa database it was updated from", "VERSION=3.89.11.20260919\n", []bm{users, {name: cfgDir + "homematic.regadom", body: "x", at: day1}}, BackupKindLite, "openccu-lite"},
		{"an OpenCCU that once ran openccu-lite", "VERSION=3.89.11.20260919\n", []bm{oldUsers, {name: cfgDir + "homematic.regadom", body: "x", at: day2}}, BackupKindCCU, "OpenCCU"},
		{"a LITE line decides", "VERSION=3.89.11.20260919\nLITE=1.3.0\n", []bm{{name: cfgDir + "homematic.regadom", body: "x", at: day2}}, BackupKindLite, "openccu-lite"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := InspectBackup(backupOf(t, c.firmware, c.members...))
			if err != nil {
				t.Fatal(err)
			}
			if b.Kind != c.kind || b.System != c.system {
				t.Errorf("kind %q system %q, want %q %q", b.Kind, b.System, c.kind, c.system)
			}
		})
	}
	b, _ := InspectBackup(backupOf(t, "VERSION=3.89.11.20260919\nLITE=1.3.0\n", users))
	if !b.Accounts || b.Users != 2 || b.Tokens != 1 || b.LiteVersion != "1.3.0" || b.Version != "3.89.11.20260919" || b.KeyIndex != 1 {
		t.Errorf("accounts: %+v", b)
	}
	if _, err := InspectBackup(filepath.Join(t.TempDir(), "none.sbk")); err == nil {
		t.Error("a missing file was inspected")
	}
	p := filepath.Join(t.TempDir(), "restore-junk.sbk")
	_ = os.WriteFile(p, []byte("this is no archive"), 0o600)
	if _, err := InspectBackup(p); err == nil {
		t.Error("junk was taken for a backup")
	}
}

// ccuMembers is an OpenCCU's backup with everything the report looks at.
func ccuMembers() []bm {
	return []bm{
		{name: cfgDir + "homematic.regadom", body: "x", at: day2},
		{name: cfgDir + "netconfig", body: "HOSTNAME=old-ccu\nMODE=DHCP\n"},
		{name: cfgDir + "hb_rf_eth", body: "192.168.1.50\n"},
		{name: cfgDir + "syslog", body: "LOGLEVEL_RFD=5\nLOGHOST=\"loghost.lan:5514\"\n"},
		{name: cfgDir + "firewall.conf", body: "MODE = MOST_OPEN\nIPS = 192.168.1.20 fd00::20\nUSERPORTS = 1883\n[SERVICE XMLRPC]\nId = XMLRPC\nPorts = 2000 2001 2010 9292 42000 42001 42010 49292\nAccess = restricted\n[SERVICE REGA]\nId = REGA\nPorts = 8181\nAccess = full\n"},
		{name: cfgDir + "hm_addons.cfg", body: "wd {CONFIG_URL /addons/wd/ CONFIG_NAME {HM Watchdog} CONFIG_DESCRIPTION {de x en y}}\n"},
		{name: cfgDir + "server.pem", body: "x"},
		{name: cfgDir + "shadow", body: "x"},
		{name: cfgDir + "TZ", body: "CET"},
		{name: "usr/local/crontabs/root", body: "* * * * * x\n"},
		{name: cfgDir + "ids", body: "BidCoS-Address=0xFF1234\nSerialNumber=ABC0000001\n"},
		{name: cfgDir + "rfd/JEQ0000001.dev", body: "d"},
		// addons: a plain one, one whose entry is a link into its directory, one without its
		// program, the NEO Server, a user's hack, a directory without an entry
		{name: cfgDir + "rc.d/wd", body: "#!/bin/sh\n", mode: 0o755},
		{name: "usr/local/addons/wd/", mode: 0o755},
		{name: "usr/local/addons/wd/bin/wd", body: "\x7fELF", mode: 0o755},
		{name: "usr/local/addons/wd/VERSION", body: "0.9\n"},
		{name: cfgDir + "addons/www/wd", link: "/usr/local/addons/wd/www"},
		{name: cfgDir + "addons/wd/settings", body: "a=1"},
		{name: cfgDir + "rc.d/red", link: "/usr/local/addons/red/bin/rc"},
		{name: "usr/local/addons/red/bin/rc", body: "#!/bin/sh\n", mode: 0o755},
		{name: "usr/local/addons/red/VERSION", body: "not a version, a sentence\n"},
		{name: cfgDir + "rc.d/noprog", body: "#!/bin/sh\n", mode: 0o755},
		{name: "usr/local/addons/noprog/", mode: 0o755},
		{name: cfgDir + "rc.d/97NeoServer", body: "#!/bin/sh\n", mode: 0o755},
		{name: "usr/local/addons/mediola/neo_server", body: "x", mode: 0o755},
		{name: cfgDir + "rc.d/red.script", body: "#!/bin/sh\n", mode: 0o755},
		{name: "usr/local/addons/orphan/data", body: "x"},
		// names that climb or are no id are nobody's
		{name: "usr/local/addons/../../etc/passwd", body: "x"},
		{name: cfgDir + "rc.d/bad name", body: "x"},
	}
}

func TestInspectBackupContent(t *testing.T) {
	b, err := InspectBackup(backupOf(t, "VERSION=3.89.11.20260919\n", ccuMembers()...))
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != BackupKindCCU || b.Hostname != "old-ccu" || !b.Rega || b.Accounts {
		t.Errorf("report: %+v", b)
	}
	if b.Radio.BidCosRF.Devices != 1 || b.Radio.BidCosRF.Address == "" || b.Radio.KeyIndex != 1 {
		t.Errorf("radio: %+v", b.Radio)
	}
	want := map[string]BackupSetting{GroupHBRFETH: {"192.168.1.50", true}, GroupHostname: {"old-ccu", true}, GroupLogHost: {"loghost.lan:5514", true}}
	for id, s := range want {
		if b.Settings[id] != s {
			t.Errorf("setting %s: %+v", id, b.Settings[id])
		}
	}
	var ids []string
	for _, a := range b.Addons {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, " ") != "noprog red wd" {
		t.Fatalf("addons: %v", ids)
	}
	wd := b.Addon("wd")
	if wd.Name != "HM Watchdog" || wd.Version != "0.9" || wd.Files != 2 || !wd.Program || !wd.WWW || !wd.Config {
		t.Errorf("wd: %+v", wd)
	}
	if red := b.Addon("red"); red.Name != "red" || red.Version != "" || !red.Program {
		t.Errorf("red: %+v", red)
	}
	if np := b.Addon("noprog"); np.Program || np.Files != 0 {
		t.Errorf("noprog: %+v", np)
	}
	if b.Addon("97NeoServer") != nil || b.Addon("orphan") != nil {
		t.Error("the NEO Server or a directory without an entry is listed as an addon")
	}
	c := b.ClassicRPC
	if !c.Open() || c.XMLRPC != "restricted" || strings.Join(c.Sources, " ") != "192.168.1.20 fd00::20" || c.Auth || c.Old.Mode != "RESTRICTIVE" {
		t.Errorf("classic RPC: %+v", c)
	}
	for _, id := range []string{LeftRega, LeftUsers, LeftNetwork, LeftFirewall, LeftCertificate, LeftSSH, LeftTime, LeftLogLevels, LeftCron, LeftAddonExtras, LeftNeoServer, LeftOther} {
		if !slices.Contains(b.LeftBehind, id) {
			t.Errorf("left behind lacks %s: %v", id, b.LeftBehind)
		}
	}
}

func TestInspectBackupValues(t *testing.T) {
	b, err := InspectBackup(backupOf(t, "VERSION=3.89.11\n",
		bm{name: cfgDir + "netconfig", body: "HOSTNAME='bad name;reboot'\n"},
		bm{name: cfgDir + "hb_rf_eth", body: "board.lan\n"},
		bm{name: cfgDir + "syslog", body: "LOGHOST=$(reboot)\n"},
		bm{name: cfgDir + "firewall.conf", body: "MODE = RESTRICTIVE\n[SERVICE XMLRPC]\nId = XMLRPC\nPorts = 2001\nAccess = full\n"},
		bm{name: cfgDir + "authEnabled"},
		bm{name: cfgDir + "AllowExternalAccess"},
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{GroupHBRFETH, GroupHostname, GroupLogHost} {
		if s, ok := b.Settings[id]; !ok || s.Valid {
			t.Errorf("%s: %+v - a value this system's setter refuses must be shown as invalid", id, s)
		}
	}
	if c := b.ClassicRPC; !c.Auth || c.XMLRPC != "full" || strings.Join(c.Sources, " ") != "0/0" {
		t.Errorf("classic RPC: %+v", c)
	}
	// nothing to offer: no settings, no firewall
	e, _ := InspectBackup(backupOf(t, "VERSION=3.89.11\n", bm{name: cfgDir + "syslog", body: "LOGHOST=\n"}))
	if len(e.Settings) != 0 || e.ClassicRPC != nil || len(e.Addons) != 0 {
		t.Errorf("an empty backup offers %+v", e)
	}
}

func TestBackupReportsKeepsTheReport(t *testing.T) {
	p := backupOf(t, "VERSION=3.89.11\n", bm{name: cfgDir + "netconfig", body: "HOSTNAME=one\n"})
	var c BackupReports
	a, err := c.For(p)
	if err != nil || a.Hostname != "one" {
		t.Fatal(a, err)
	}
	// the same file is not walked again; a changed one is
	q := backupOf(t, "VERSION=3.89.11\n", bm{name: cfgDir + "netconfig", body: "HOSTNAME=second\n"})
	data, _ := os.ReadFile(q)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if b, err := c.For(p); err != nil || b.Hostname != "second" {
		t.Errorf("after the file changed: %+v %v", b.Hostname, err)
	}
	if _, err := c.For(filepath.Join(t.TempDir(), "gone.sbk")); err == nil {
		t.Error("a missing file has a report")
	}
}
