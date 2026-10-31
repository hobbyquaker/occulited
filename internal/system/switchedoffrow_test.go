package system

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// occulited B-132: systemd does not list a masked unit nothing refers to, so a switched-off addon had
// no row on the Services page, and the way to switch it on went with the row.
func TestSwitchedOffAddonKeepsItsRow(t *testing.T) {
	root := t.TempDir()
	rcd := filepath.Join(root, "usr/local/etc/config/rc.d")
	if err := os.MkdirAll(rcd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rcd, "nut"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sw := filepath.Join(t.TempDir(), "unit-switch.json")
	if err := os.WriteFile(sw, []byte(`{"masked":["addon-nut.service","addon-gone.service","sshd.service"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list-units" {
			return []byte(`[{"unit":"rfd.service","load":"loaded","active":"active","sub":"running","description":"rfd"}]`), nil
		}
		if strings.Contains(strings.Join(args, " "), "addon-nut.service") {
			return []byte("Id=rfd.service\nUnitFileState=enabled\n\nId=addon-nut.service\nUnitFileState=masked-runtime\n"), nil
		}
		return nil, nil
	}
	s := SystemdServices{Root: Root(root), Run: run, SwitchFile: sw}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Service{}
	for _, v := range list {
		by[v.ID] = v
	}
	nut, ok := by["addon-nut"]
	if !ok {
		t.Fatalf("no row: %+v", list)
	}
	if nut.Running || nut.Enabled || nut.UnitFileState != "masked-runtime" || nut.Kind != "addon" || nut.Category != "addon" || !nut.Managed {
		t.Errorf("row: %+v", nut)
	}
	if _, ok := by["addon-gone"]; ok {
		t.Error("an entry whose addon is gone has a row")
	}
	if _, ok := by["sshd"]; ok {
		t.Error("only addons get a row from the switch file")
	}

	a := NewSystemdAddons(Root(root), s)
	addons, err := a.ListAddons(context.Background())
	if err != nil || len(addons) != 1 || addons[0].ID != "nut" || addons[0].Enabled || addons[0].Running {
		t.Fatalf("ListAddons: %+v %v", addons, err)
	}
	if err := a.ClearSwitch(context.Background(), "nut"); err != nil {
		t.Fatal(err)
	}
	if a.Systemd.switchedOff("addon-nut.service") {
		t.Error("ClearSwitch left the entry")
	}
	if addons, _ = a.ListAddons(context.Background()); !addons[0].Enabled {
		t.Errorf("not enabled after ClearSwitch: %+v", addons[0])
	}
}
