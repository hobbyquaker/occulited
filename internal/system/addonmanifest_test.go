package system

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/manifest"
)

// D-119: the manifest's runtime block reaches the policy as it was declared - needs absent stays
// absent, [] stays [], the ports keep their descriptions - as copies, never the manifest's slices.
func TestRuntimeFromManifest(t *testing.T) {
	if RuntimeFromManifest(nil) != nil {
		t.Fatal("no block")
	}
	empty := []string{}
	m := &manifest.Runtime{Root: true, Capabilities: []string{"CAP_NET_RAW"}, Groups: []string{"dialout"}, Paths: []string{"/etc/config/x"}, DataDirs: []string{"/usr/local/x"},
		Ports: []int{1883, 8883}, PortInfo: map[string]manifest.PortInfo{"8883": {Proto: "tcp", TLS: true, Label: manifest.Text{"en": "MQTT over TLS"}}},
		Needs: &empty, Start: "early", APIScopes: []string{"meta:write"}}
	rt := RuntimeFromManifest(m)
	if !rt.Root || rt.Capabilities[0] != "CAP_NET_RAW" || rt.Groups[0] != "dialout" || rt.Paths[0] != "/etc/config/x" || rt.DataDirs[0] != "/usr/local/x" || len(rt.Ports) != 2 || !rt.PortInfo["8883"].TLS || rt.PortInfo["8883"].Label["en"] != "MQTT over TLS" || rt.Start != AddonStartEarly || rt.APIScopes[0] != "meta:write" {
		t.Fatalf("%+v", rt)
	}
	if rt.Needs == nil || len(*rt.Needs) != 0 || rt.NeedsLine() != "none" {
		t.Fatalf("needs []: %+v", rt.Needs)
	}
	if RuntimeFromManifest(&manifest.Runtime{Ports: []int{1}}).Needs != nil {
		t.Error("needs absent stays absent")
	}
	two := []string{"rfd", "hmipserver"}
	rt = RuntimeFromManifest(&manifest.Runtime{Needs: &two})
	(*rt.Needs)[0] = "changed"
	if two[0] != "rfd" {
		t.Error("the conversion shares the manifest's slice")
	}
}

// package builds an addon tarball with update_script and, when body is not "", openccu-lite.json at
// the root.
func packageWith(t *testing.T, body string) *bytes.Reader {
	t.Helper()
	return packageWithFiles(t, body, nil)
}

// packageWithFiles is packageWith plus more files at the given archive paths.
func packageWithFiles(t *testing.T, body string, files map[string]string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	add("update_script", "#!/bin/sh\nexit 0\n")
	add("mosq/bin/x", "x")
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		add(n, files[n])
	}
	if body != "" {
		add(manifest.FileName, body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

// manifestBox is a systemd box whose fake install_addon creates the rc.d entry newID (and touches
// nothing else). Installed again, it adds a line to the entry: Install sees an update by the entry's
// size and mtime, and two installs within one tick of the file system's coarse clock left both as
// they were - the addon counted as untouched, and TestInstallKeepsThePackagesImages failed (B-48).
func manifestBox(t *testing.T, newID string) (Root, *SystemdAddons) {
	t.Helper()
	r := rootWith(t, map[string]string{"usr/local/tmp/.keep": "", "etc/passwd": "root:x:0:0::/:/bin/sh\n", "etc/group": "root:x:0:\n", "usr/local/etc/config/rc.d/old": "#!/bin/sh\n"})
	rc := r.join("/usr/local/etc/config/rc.d/" + newID)
	_ = os.MkdirAll(r.join("/bin"), 0o755)
	_ = os.WriteFile(r.join("/bin/install_addon"), []byte("#!/bin/sh\nif [ -f "+rc+" ]; then echo '# installed again' >> "+rc+"; else printf '#!/bin/sh\\nexit 0\\n' > "+rc+"; fi\nchmod +x "+rc+"\nrm -f "+r.join("/usr/local/tmp/new_addon.tar.gz")+"\nexit 0\n"), 0o755)
	run := func(context.Context, string, ...string) ([]byte, error) { return []byte("ok"), nil }
	return r, NewSystemdAddons(r, SystemdServices{Root: r, Run: run})
}

// The package's manifest is read before the installer runs and applied to the addon it names: the
// policy is written from its runtime block (root as declared), the copy is stored beside it, and the
// pages see the addon as declared.
func TestInstallAppliesThePackageManifest(t *testing.T) {
	r, a := manifestBox(t, "mosq")
	body := `{"format": 1, "id": "mosq", "name": "Mosquitto", "ui": {"session_header": true, "settings_url": "/addons/mosq/settings.cgi"},
		"runtime": {"needs": [], "ports": [1883, 8883], "port_info": {"8883": {"tls": true, "label": {"en": "MQTT over TLS"}}}, "groups": ["dialout"]}}`
	res, err := a.Install(context.Background(), packageWith(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "[manifest] mosq: the package's openccu-lite.json applied") {
		t.Errorf("output:\n%s", res.Output)
	}
	p := r.ReadAddonPolicy("mosq")
	if p == nil || p.Mode != "confined" || p.Source != SourceManifest || p.Runtime == nil || len(p.Runtime.Ports) != 2 || p.Runtime.Groups[0] != "dialout" || p.Runtime.NeedsLine() != "none" {
		t.Fatalf("policy: %+v", p)
	}
	m := r.ReadAddonManifest("mosq")
	if m == nil || !m.UI.SessionHeader || m.UI.SettingsURL != "/addons/mosq/settings.cgi" {
		t.Fatalf("stored manifest: %+v", m)
	}
	if v := a.PolicyView("mosq"); v.Undeclared || v.Source != SourceManifest {
		t.Errorf("view: %+v", v)
	}
	if got := readFile(r.join(AddonPolicyDir + "/mosq.needs")); got != "none\n" {
		t.Errorf("needs file: %q", got)
	}
	if rt := r.DeclaredRuntime("mosq"); rt == nil || !rt.Declares(8883) || !rt.DeclaredPorts()[1].TLS {
		t.Errorf("declared: %+v", rt)
	}
	// the addon runs without the ReGa: its manifest says so without a marker file
	if dep, _ := r.RegaDependence("mosq"); dep {
		t.Error("an addon with a manifest is ReGa-compatible")
	}
	// uninstall forgets the stored copy
	_ = os.WriteFile(r.join("/usr/local/etc/config/rc.d/mosq"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if _, err := a.Uninstall(context.Background(), "mosq"); err != nil {
		t.Logf("uninstall: %v (the fake script has no uninstall; the cleanup still runs)", err)
	}
	if r.ReadAddonManifest("mosq") != nil {
		t.Error("the stored manifest outlived the addon")
	}
	// B-283: and so do the policy, its drop-in and the start order - a reinstall starts clean
	for _, f := range []string{"mosq.json", "mosq.conf", "mosq.needs"} {
		if _, err := os.Stat(r.join(AddonPolicyDir + "/" + f)); !os.IsNotExist(err) {
			t.Errorf("%s outlived the addon", f)
		}
	}
}

// root: true runs the addon as root, as declared, and a manifest without a runtime block leaves
// the addon on the box default, undeclared
func TestInstallManifestRoot(t *testing.T) {
	r, a := manifestBox(t, "mounter")
	if _, err := a.Install(context.Background(), packageWith(t, `{"format": 1, "id": "mounter", "name": "M", "runtime": {"root": true}}`)); err != nil {
		t.Fatal(err)
	}
	if p := r.ReadAddonPolicy("mounter"); p == nil || p.Mode != "root" || p.Source != SourceManifest {
		t.Fatalf("%+v", p)
	}
	r, a = manifestBox(t, "plain")
	if _, err := a.Install(context.Background(), packageWith(t, `{"format": 1, "id": "plain", "name": "P"}`)); err != nil {
		t.Fatal(err)
	}
	if p := r.ReadAddonPolicy("plain"); p == nil || p.Mode != "confined" || p.Source != SourceManifest || !a.PolicyView("plain").Undeclared {
		t.Fatalf("%+v", p)
	}
}

// A manifest that names an addon the install did not touch is not applied, and the log says so;
// a broken manifest is ignored with a note; a package without one takes the box default - or the
// catalogue's word when it has one.
func TestInstallManifestMismatchBrokenAndFallback(t *testing.T) {
	r, a := manifestBox(t, "mosq")
	res, err := a.Install(context.Background(), packageWith(t, `{"format": 1, "id": "other", "name": "O", "runtime": {"root": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "names other, which the installer neither created nor changed") {
		t.Errorf("output:\n%s", res.Output)
	}
	if p := r.ReadAddonPolicy("mosq"); p == nil || p.Mode != "confined" || p.Source != "default" || r.ReadAddonManifest("mosq") != nil || r.ReadAddonManifest("other") != nil {
		t.Fatalf("a mismatched manifest changed something: %+v", p)
	}

	r, a = manifestBox(t, "mosq")
	res, err = a.Install(context.Background(), packageWith(t, `{"format": 1, "id": "Mosq!"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "is not usable and was ignored") || r.ReadAddonPolicy("mosq").Source != "default" {
		t.Errorf("a broken manifest: %s", res.Output)
	}

	r, a = manifestBox(t, "jp")
	a.FallbackManifest = func(id string) (*manifest.Manifest, string) {
		if id == "jp" {
			return manifestFor("jp", &AddonRuntime{Root: true}), ""
		}
		return nil, ""
	}
	res, err = a.Install(context.Background(), packageWith(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "the catalogue's declaration applied") {
		t.Errorf("output:\n%s", res.Output)
	}
	if p := r.ReadAddonPolicy("jp"); p == nil || p.Mode != "root" || p.Source != SourceCatalog || r.ReadAddonManifest("jp") == nil {
		t.Fatalf("the fallback: %+v", p)
	}
	// the fallback's id must be the addon's
	r, a = manifestBox(t, "x")
	a.FallbackManifest = func(string) (*manifest.Manifest, string) { return manifestFor("y", nil), "" }
	if _, err := a.Install(context.Background(), packageWith(t, "")); err != nil {
		t.Fatal(err)
	}
	if p := r.ReadAddonPolicy("x"); p.Source != "default" {
		t.Errorf("a fallback for another id was applied: %+v", p)
	}
}

// ApplyManifest keeps a mode the user chose on the Services page; the block follows the package.
func TestApplyManifestKeepsTheUsersMode(t *testing.T) {
	r := rootWith(t, map[string]string{"etc/passwd": "root:x:0:0::/:/bin/sh\n", "etc/group": "root:x:0:\n"})
	a := NewSystemdAddons(r, SystemdServices{Root: r, Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }})
	if _, err := a.SetPolicy(context.Background(), "mosq", "root", "user", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyManifest(context.Background(), "mosq", manifestFor("mosq", &AddonRuntime{Ports: []int{1883}}), SourceManifest); err != nil {
		t.Fatal(err)
	}
	if p := r.ReadAddonPolicy("mosq"); p.Mode != "root" || p.Source != "user" || p.Runtime == nil || !p.Runtime.Declares(1883) {
		t.Errorf("%+v", p)
	}
	if err := a.ApplyManifest(context.Background(), "mosq", manifestFor("other", nil), SourceManifest); err == nil {
		t.Error("a manifest for another id was applied")
	}
	if err := a.ApplyManifest(context.Background(), "mosq", nil, SourceManifest); err == nil {
		t.Error("no manifest")
	}
}

// requires.rega decides the ReGa verdict where a manifest is known: the stored copy first, the file
// in the addon's own tree for this one flag; the older marker file stays accepted.
func TestRegaFromManifest(t *testing.T) {
	r := rootWith(t, map[string]string{
		"usr/local/addons/tree/" + manifest.FileName:      `{"format": 1, "id": "tree", "name": "T"}`,
		"usr/local/addons/needy/" + manifest.FileName:     `{"format": 1, "id": "needy", "name": "N", "requires": {"rega": true}}`,
		"usr/local/addons/marked/" + RegaCompatibleMarker: "",
		"usr/local/addons/cuxd/x.tcl":                     "dom.GetObject",
	})
	writeManifest(t, r, "stored", nil)
	for id, want := range map[string]bool{"tree": false, "needy": true, "marked": false, "stored": false, "cuxd": true} {
		if dep, _ := r.RegaDependence(id); dep != want {
			t.Errorf("%s: dependent %v, want %v", id, dep, want)
		}
	}
}

// versionedBox is manifestBox whose installed rc.d script answers info with the given version.
func versionedBox(t *testing.T, id, version string) (Root, *SystemdAddons) {
	t.Helper()
	r, a := manifestBox(t, id)
	rc := r.join("/usr/local/etc/config/rc.d/" + id)
	script := "#!/bin/sh\\n[ \\\"\\$1\\\" = info ] && echo 'Version: " + version + "'\\nexit 0\\n"
	if err := os.WriteFile(r.join("/bin/install_addon"), []byte("#!/bin/sh\nprintf \""+script+"\" > "+rc+"\nchmod +x "+rc+"\nrm -f "+r.join("/usr/local/tmp/new_addon.tar.gz")+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.DefaultMode = "root" // info runs as root, without the helper
	return r, a
}

// aged sets the rc.d entry's mtime an hour back, so that the next fake install's rewrite of the
// same bytes is seen as a change (a coarse file system clock could otherwise give the same time).
func aged(t *testing.T, r Root, id string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(r.join("/usr/local/etc/config/rc.d/"+id), old, old); err != nil {
		t.Fatal(err)
	}
}

// B-27: a package without a manifest over one with a manifest takes the stored copy away - its
// ui.session_header would otherwise keep ?sid= off a page that cannot read the header - and the
// API scopes with it; the policy keeps the rest of its runtime block and its mode.
func TestInstallWithoutManifestDropsTheEarlierCopy(t *testing.T) {
	r, a := versionedBox(t, "mosq", "2.1.2+3")
	body := `{"format": 1, "id": "mosq", "name": "Mosquitto", "ui": {"session_header": true},
		"runtime": {"ports": [1883], "data_dirs": ["/usr/local/mosqdata"], "api_scopes": ["meta:read"]}}`
	if _, err := a.Install(context.Background(), packageWith(t, body)); err != nil {
		t.Fatal(err)
	}
	if m := r.ReadAddonManifest("mosq"); m == nil || !m.UI.SessionHeader {
		t.Fatalf("the first install's copy: %+v", m)
	}
	aged(t, r, "mosq")
	res, err := a.Install(context.Background(), packageWith(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "[manifest] mosq: the package carries no openccu-lite.json; the stored declaration of the earlier install was removed") {
		t.Errorf("output:\n%s", res.Output)
	}
	if m := r.ReadAddonManifest("mosq"); m != nil {
		t.Fatalf("the earlier install's copy survived: %+v", m)
	}
	p := r.ReadAddonPolicy("mosq")
	if p == nil || p.Mode != "root" || p.Runtime == nil || !p.Runtime.Declares(1883) || len(p.Runtime.DataDirs) != 1 || p.Runtime.APIScopes != nil {
		t.Fatalf("policy: %+v runtime %+v", p, p.Runtime)
	}
	// a third install without a manifest has nothing to remove and says nothing
	aged(t, r, "mosq")
	res, err = a.Install(context.Background(), packageWith(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "[manifest]") {
		t.Errorf("output:\n%s", res.Output)
	}
}

// B-27: the catalogue's manifest read at a release tag stands in only for that version; one read
// at the default branch, or an adapter, for any. Without a fitting one the copy goes.
func TestInstallWithoutManifestFallbackByVersion(t *testing.T) {
	withCopy := `{"format": 1, "id": "mosq", "name": "Mosquitto", "ui": {"session_header": true}}`
	for _, tc := range []struct {
		name, tag, installed string
		applied              bool
	}{
		{"the latest release's manifest, an older package", "v2.1.2+4", "2.1.2+3", false},
		{"the same release", "v2.1.2+3", "2.1.2+3", true},
		{"the same release, no v", "2.1.2+3", "2.1.2+3", true},
		{"the default branch or an adapter", "", "2.1.2+3", true},
		{"a package that names no version", "v2.1.2+3", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, a := versionedBox(t, "mosq", tc.installed)
			if _, err := a.Install(context.Background(), packageWith(t, withCopy)); err != nil {
				t.Fatal(err)
			}
			a.FallbackManifest = func(id string) (*manifest.Manifest, string) {
				m := manifestFor(id, &AddonRuntime{Ports: []int{1883}})
				m.UI.SettingsURL = "/addons/mosq/catalogue.cgi"
				return m, tc.tag
			}
			aged(t, r, "mosq")
			res, err := a.Install(context.Background(), packageWith(t, ""))
			if err != nil {
				t.Fatal(err)
			}
			m := r.ReadAddonManifest("mosq")
			if tc.applied {
				if m == nil || m.UI.SessionHeader || m.UI.SettingsURL != "/addons/mosq/catalogue.cgi" || !strings.Contains(res.Output, "the catalogue's declaration applied") {
					t.Fatalf("the catalogue's manifest was not applied: %+v\n%s", m, res.Output)
				}
			} else if m != nil || !strings.Contains(res.Output, "the stored declaration of the earlier install was removed") {
				t.Fatalf("stored copy %+v\n%s", m, res.Output)
			}
		})
	}
}

// B-27: a package whose manifest names another addon leaves the stored copies of the addons it
// touched as they are - only a package without any manifest replaced what one described.
func TestInstallForeignManifestKeepsOtherCopies(t *testing.T) {
	r, a := manifestBox(t, "mosq")
	writeManifest(t, r, "mosq", &AddonRuntime{Ports: []int{1883}})
	if _, err := a.Install(context.Background(), packageWith(t, `{"format": 1, "id": "other", "name": "O"}`)); err != nil {
		t.Fatal(err)
	}
	if m := r.ReadAddonManifest("mosq"); m == nil {
		t.Fatal("the copy of an addon the package's manifest does not name was removed")
	}
}

// B-27: at start an addon without a stored copy adopts the catalogue's manifest only when it speaks
// for the installed version - else the latest release's would come back after every boot.
func TestRefreshManifestRuntimesFallbackByVersion(t *testing.T) {
	r, a := versionedBox(t, "mosq", "2.1.2+3")
	if _, err := a.Install(context.Background(), packageWith(t, "")); err != nil {
		t.Fatal(err)
	}
	a.FallbackManifest = func(id string) (*manifest.Manifest, string) {
		return manifestFor(id, &AddonRuntime{Ports: []int{1883}}), "v2.1.2+4"
	}
	if ids := a.RefreshManifestRuntimes(); len(ids) != 0 || r.ReadAddonManifest("mosq") != nil {
		t.Fatalf("the latest release's manifest was adopted for 2.1.2+3: %v", ids)
	}
	a.FallbackManifest = func(id string) (*manifest.Manifest, string) {
		return manifestFor(id, &AddonRuntime{Ports: []int{1883}}), "v2.1.2+3"
	}
	if ids := a.RefreshManifestRuntimes(); len(ids) != 1 || r.ReadAddonManifest("mosq") == nil {
		t.Fatalf("the installed release's manifest was not adopted: %v", ids)
	}
}
