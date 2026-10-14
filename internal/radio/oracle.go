package radio

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WriteOracle runs the detection, the plan and the render against a root and writes the fork's
// differential harness's file set into out (scripts/testcases/lite-radio-oracle-test.sh, task
// 129): the same names the harness records from upstream's scripts, so that a diff between the
// two is the list of differences between the plan and the old chain. Nothing under the root is
// changed except what a detection changes (the reset and connect files in sysfs).
func WriteOracle(ctx context.Context, root, out string, d Detector) (Plan, error) {
	if d.Root == "" {
		d.Root = root
	}
	env := ParseKV(readFile(filepath.Join(root, "var/hm_mode")))
	if d.Host == "" {
		d.Host = env["HM_HOST"]
	}
	det := d.Detect(ctx)
	in := Load(ctx, root, d.Run, det)
	in.Diagrams = d.Diagrams
	p := MakePlan(in)
	f := Render(in, p)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return p, err
	}
	w := func(name, content string) error {
		if content == "" {
			content = "(absent)\n"
		}
		return os.WriteFile(filepath.Join(out, name), []byte(content), 0o644)
	}
	// hm_mode: sorted, unquoted, as the harness normalises it
	var hm []string
	for k, v := range ParseKV(f.HMMode) {
		hm = append(hm, k+"="+v)
	}
	sort.Strings(hm)
	if err := w("hm_mode", strings.Join(hm, "\n")+"\n"); err != nil {
		return p, err
	}
	var vf []string
	for _, k := range []string{"board_serial", "board_sgtin", "rf_board_serial", "rf_address", "rf_firmware_version", "hmip_board_serial", "hmip_firmware_version", "hmip_address", "hmip_board_sgtin"} {
		if v, ok := f.VarFiles[k]; ok {
			vf = append(vf, k+"="+v)
		}
	}
	if err := w("var-files", strings.Join(vf, "\n")+"\n"); err != nil {
		return p, err
	}
	var dm []string
	for _, n := range []string{"multimacd", "rfd", "hmipserver", "hs485d-init", "hs485d", "hmlangw"} {
		if c, ok := f.Commands[n]; ok {
			dm = append(dm, n+": "+c)
		} else {
			dm = append(dm, n+": not started")
		}
	}
	if err := w("daemons", strings.Join(dm, "\n")+"\n"); err != nil {
		return p, err
	}
	for name, content := range map[string]string{
		"rfd.conf": f.RFDConf, "var-rfd.conf": f.VarRFDConf, "multimacd.conf": f.MultimacdConf, "crRFD.conf": f.CrRFDConf,
		"HMServer.conf": f.HMServerConf, "hs485d.conf": f.VarHS485DConf,
	} {
		if err := w(name, content); err != nil {
			return p, err
		}
	}
	var ifl []string
	for _, i := range p.Interfaces {
		ifl = append(ifl, i.Name+"|"+i.URL+"|"+i.Info)
	}
	// in LAN-gateway mode nothing touches the file: it stays what it was
	ifText := strings.Join(ifl, "\n") + "\n"
	if p.Mode == "HM-LGW" {
		ifText = ""
		for _, i := range ParseInterfaces(in.InterfacesList) {
			ifText += i.Name + "|" + i.URL + "|" + i.Info + "\n"
		}
	}
	if err := w("interfaces", ifText); err != nil {
		return p, err
	}
	// the userfs afterwards: what is there plus what the run would add or move
	files := map[string]bool{}
	for _, dir := range []string{"etc/config", "usr/local", "media"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			files[rel] = true
			return nil
		})
	}
	if p.Mode != "HM-LGW" {
		files["etc/config/rfd.conf"] = true
		files["etc/config/InterfacesList.xml"] = true
		if in.TemplateHmIPNetworkKey && !in.HmIPNetworkKey {
			files["etc/config/hmip_networkkey.conf"] = true
		}
		if p.StoragePath != "" {
			rel := strings.TrimPrefix(p.StoragePath, "/")
			files[rel+"/.nobackup"] = true
			files["media/usb0"] = true
		}
	}
	if p.IDsInvalid {
		delete(files, "etc/config/ids")
		files["etc/config/ids_old-"+time.Now().Format("20060102-150405")] = true
	}
	var uf []string
	for k := range files {
		uf = append(uf, k)
	}
	sort.Strings(uf)
	return p, w("userfs", strings.Join(uf, "\n")+"\n")
}
