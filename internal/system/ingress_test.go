package system

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// ingressPriv records the ingressdir operation instead of touching /run.
type ingressPriv struct {
	priv.Local
	dirs []string
}

func (p *ingressPriv) IngressDir(id string) error {
	p.dirs = append(p.dirs, id)
	return nil
}

const wgManifest = `{"format": 1, "id": "wireguard", "name": "VPN (WireGuard)", "description": {"en": "x"},
  "runtime": {"daemon": true, "needs": [], "ports": [51820], "web_ingress": {"ranges": ["10.77.0.0/24", "fd77::/64"]}}}`

// openccu-lite task 406: the rendered block names the socket, trusts the PROXY header alone, reads no
// forwarding header, refuses every address outside the ranges, marks the request with the addon's id,
// and takes webui.conf; nothing of TLS, HSTS or the HTTPS redirect.
func TestIngressFragment(t *testing.T) {
	f := IngressFragment("wireguard", []netip.Prefix{netip.MustParsePrefix("10.77.0.0/24"), netip.MustParsePrefix("fd77::/64")})
	for _, want := range []string{
		`$SERVER["socket"] == "/run/lighttpd-addons/wireguard/proxy.sock" {`,
		`  ssl.engine = "disable"`,
		`  server.socket-perms = "0666"`,
		`  extforward.hap-PROXY = "enable"`,
		`  extforward.forwarder = ( "all" => "trust" )`,
		`  extforward.headers = ()`,
		"  $HTTP[\"remoteip\"] != \"10.77.0.0/24\" {\n    $HTTP[\"remoteip\"] != \"fd77::/64\" {\n      url.access-deny = ( \"\" )\n    }\n  }",
		`  include "/etc/lighttpd/conf.d/webui.conf"`,
	} {
		if !strings.Contains(f, want) {
			t.Errorf("fragment lacks %q:\n%s", want, f)
		}
	}
	for _, never := range []string{"hsts", "httpsredirect", "fqdnredirect", "ssl.pemfile", "include_shell"} {
		if strings.Contains(f, never) {
			t.Errorf("fragment holds %q", never)
		}
	}
	if !strings.HasPrefix(f, lighttpdCopyPrefix) {
		t.Errorf("the first line must start with %q", lighttpdCopyPrefix)
	}
	if strings.Count(f, "{") != strings.Count(f, "}") {
		t.Error("braces")
	}
}

// The sync renders a fragment and makes the directory for every installed addon with a declaration,
// keeps it when nothing changed, and removes it with the addon.
func TestSyncIngressFragments(t *testing.T) {
	root := Root(t.TempDir())
	old := Priv
	fp := &ingressPriv{}
	Priv = fp
	t.Cleanup(func() { Priv = old })
	pol := root.join(AddonPolicyDir)
	if err := os.MkdirAll(pol, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root.join(lighttpdDropinDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pol, "wireguard"+AddonManifestSuffix), []byte(wgManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// an addon without an ingress declaration
	if err := os.WriteFile(filepath.Join(pol, "nut"+AddonManifestSuffix), []byte(`{"format": 1, "id": "nut", "name": "UPS", "description": {"en": "x"}, "runtime": {"daemon": true, "needs": []}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, results, err := SyncLighttpdDropins(root)
	if err != nil || !changed {
		t.Fatalf("sync: %v changed=%v %+v", err, changed, results)
	}
	frag := root.join(lighttpdDropinDir + "/" + IngressFragmentPrefix + "wireguard.conf")
	b, err := os.ReadFile(frag)
	if err != nil || !strings.Contains(string(b), `/run/lighttpd-addons/wireguard/proxy.sock`) || !strings.Contains(string(b), `!= "fd77::/64"`) {
		t.Fatalf("fragment: %v\n%s", err, b)
	}
	if len(fp.dirs) != 1 || fp.dirs[0] != "wireguard" {
		t.Fatalf("ingressdir calls %v", fp.dirs)
	}
	if _, err := os.Stat(root.join(lighttpdDropinDir + "/" + IngressFragmentPrefix + "nut.conf")); err == nil {
		t.Fatal("a fragment for an addon without a declaration")
	}
	if got := root.IngressRanges("wireguard"); len(got) != 2 || got[0].String() != "10.77.0.0/24" {
		t.Fatalf("ranges %v", got)
	}
	if root.IngressRanges("nut") != nil || root.IngressRanges("../x") != nil || root.IngressRanges("ghost") != nil {
		t.Fatal("ranges for an addon without a declaration")
	}
	// unchanged: kept, no reload
	changed, results, err = SyncLighttpdDropins(root)
	if err != nil || changed {
		t.Fatalf("second sync: %v changed=%v %+v", err, changed, results)
	}
	// the addon's manifest gone (uninstalled): the fragment goes, the ranges with it
	if err := os.Remove(filepath.Join(pol, "wireguard"+AddonManifestSuffix)); err != nil {
		t.Fatal(err)
	}
	changed, _, err = SyncLighttpdDropins(root)
	if err != nil || !changed {
		t.Fatalf("third sync: %v changed=%v", err, changed)
	}
	if _, err := os.Stat(frag); err == nil {
		t.Fatal("the fragment stayed after the uninstall")
	}
	if root.IngressRanges("wireguard") != nil {
		t.Fatal("ranges after the uninstall")
	}
	// a stray fragment of an addon the system does not know goes too
	if err := os.WriteFile(root.join(lighttpdDropinDir+"/"+IngressFragmentPrefix+"ghost.conf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, _, err = SyncLighttpdDropins(root); err != nil || !changed {
		t.Fatalf("stray: %v %v", err, changed)
	}
	if _, err := os.Stat(root.join(lighttpdDropinDir + "/" + IngressFragmentPrefix + "ghost.conf")); err == nil {
		t.Fatal("the stray fragment stayed")
	}
}
