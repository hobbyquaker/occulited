package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonimage"
	"github.com/hobbyquaker/occulited/internal/manifest"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// The addon manifest on the box (D-119, docs/manifest-format.md). An addon's openccu-lite.json is
// read out of the package archive before its update_script runs and, when the installer created
// or changed the rc.d entry it names, applied: its runtime block becomes the policy, the file is
// kept root-owned beside the policy as <id>.manifest.json, and the copy inside the addon's own
// directory is never read again - a confined addon owns that directory (D-69). A package without
// a manifest takes the catalogue's word for the id (an adapter manifest, or the one fetched for the
// Addons page, when it speaks for the installed version) when there is one; otherwise the stored
// copy of an earlier install goes (B-27) and the policy keeps its runtime block without the API
// scopes, as for an addon installed before D-119.

// AddonManifestSuffix names the stored copy beside the policy, AddonPolicyDir/<id>.manifest.json.
const AddonManifestSuffix = priv.AddonManifestSuffix

// Policy sources a manifest writes: the package's own, and the catalogue's (an adapter manifest
// or the manifest fetched for the page, standing in for a package without one).
const (
	SourceManifest = "manifest"
	SourceCatalog  = "catalog"
)

// ReadAddonManifest is the stored manifest of an addon, nil when it has none or the file does not
// parse (logged once per read: a file occulited wrote itself is not expected to break).
func (r Root) ReadAddonManifest(id string) *manifest.Manifest {
	if !addonIDRe.MatchString(id) {
		return nil
	}
	b, err := os.ReadFile(r.join(AddonPolicyDir + "/" + id + AddonManifestSuffix))
	if err != nil {
		return nil
	}
	m, err := manifest.Parse(b)
	if err != nil {
		slog.Warn("addon manifest: the stored copy does not parse", "addon", id, "err", err)
		return nil
	}
	if m.ID != id {
		return nil
	}
	return m
}

// writeAddonManifest keeps the accepted manifest beside the policy.
func (r Root) writeAddonManifest(id string, m *manifest.Manifest) error {
	dir := r.join(AddonPolicyDir)
	if err := Priv.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, id+AddonManifestSuffix), append(b, '\n'), 0o644)
}

// removeAddonManifest forgets the stored copy: the addon was uninstalled, and a package installed
// later must not inherit a declaration it did not bring.
func (r Root) removeAddonManifest(id string) {
	if !addonIDRe.MatchString(id) {
		return
	}
	if err := remove(r.join(AddonPolicyDir + "/" + id + AddonManifestSuffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("addon manifest: the stored copy could not be removed", "addon", id, "err", err)
	}
	r.removeAddonImages(id)
}

// DeclaredRuntime is what an addon declares now: its stored manifest's runtime block, nil without
// one. The firewall's switches and the policies' declarations are checked against it (D-47).
func (r Root) DeclaredRuntime(id string) *AddonRuntime {
	m := r.ReadAddonManifest(id)
	if m == nil {
		return nil
	}
	return RuntimeFromManifest(m.Runtime)
}

// RuntimeFromManifest is a manifest's runtime block as the policy stores it; nil for none.
func RuntimeFromManifest(rt *manifest.Runtime) *AddonRuntime {
	if rt == nil {
		return nil
	}
	out := &AddonRuntime{
		Root:         rt.Root,
		Capabilities: slices.Clone(rt.Capabilities),
		Groups:       slices.Clone(rt.Groups),
		Paths:        slices.Clone(rt.Paths),
		DataDirs:     slices.Clone(rt.DataDirs),
		Ports:        slices.Clone(rt.Ports),
		APIScopes:    slices.Clone(rt.APIScopes),
		Start:        rt.Start,
		Daemon:       rt.Daemon,
	}
	if rt.WebIngress != nil {
		out.WebIngress = &manifest.WebIngress{Ranges: slices.Clone(rt.WebIngress.Ranges)}
	}
	if rt.Needs != nil {
		ids := slices.Clone(*rt.Needs)
		if ids == nil {
			ids = []string{}
		}
		out.Needs = &ids
	}
	for k, v := range rt.PortInfo {
		if out.PortInfo == nil {
			out.PortInfo = map[string]PortInfo{}
		}
		out.PortInfo[k] = PortInfo{Proto: v.Proto, TLS: v.TLS, Label: map[string]string(v.Label)}
	}
	return out
}

// ApplyManifest stores a manifest for an addon and writes the policy from its runtime block, as
// declared (D-119): root when it says root, else the box's default - except that a mode the user
// chose on the Services page (source "user") stands, and only the block follows the package. A
// manifest without a runtime block keeps the stored block, if any, and the addon stays marked
// undeclared. source is SourceManifest for the package's own file, SourceCatalog for the
// catalogue's word standing in.
func (a *SystemdAddons) ApplyManifest(ctx context.Context, id string, m *manifest.Manifest, source string) error {
	if m == nil {
		return errors.New("no manifest")
	}
	if m.ID != id {
		return fmt.Errorf("the manifest names %q, not %q", m.ID, id)
	}
	root := a.Scripts.Root
	rt := RuntimeFromManifest(m.Runtime)
	if err := rt.validate(); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if err := root.writeAddonManifest(id, m); err != nil {
		return fmt.Errorf("storing the manifest: %w", err)
	}
	mode := a.defaultMode()
	if rt != nil && rt.Root {
		mode = "root"
	}
	if p := root.ReadAddonPolicy(id); p != nil && p.Source == "user" {
		mode, source = p.Mode, p.Source
	}
	_, err := a.SetPolicy(ctx, id, mode, source, rt)
	return err
}

// fallbackManifest is the catalogue's word for an addon without a manifest of its own: nil
// without a catalogue or an entry, and nil when the catalogue read it at a release tag that is not
// the installed version (B-27): the latest release's manifest does not describe an older package
// installed from a file - its ui.session_header would open a page without ?sid= that cannot read
// the header. An adapter manifest, and one read at the default branch, speak for any version.
func (a *SystemdAddons) fallbackManifest(ctx context.Context, id string) *manifest.Manifest {
	if a.FallbackManifest == nil {
		return nil
	}
	m, tag := a.FallbackManifest(id)
	if m == nil || m.ID != id {
		return nil
	}
	if tag != "" && !sameVersion(tag, a.installedVersion(ctx, id)) {
		return nil
	}
	return m
}

// installedVersion is the Version line of the addon's rc.d info, "" when it says none.
func (a *SystemdAddons) installedVersion(ctx context.Context, id string) string {
	if !addonIDRe.MatchString(id) {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, _ := a.Scripts.addonScriptOutput(cctx, id, a.Scripts.Root.join("/usr/local/etc/config/rc.d/"+id), "info")
	return parseInfo(id, string(raw)).Version
}

// sameVersion says whether a release tag names an installed version: a leading "v" and the case
// ignored; an empty version is never the same.
func sameVersion(tag, version string) bool {
	tag, version = strings.TrimPrefix(strings.TrimSpace(tag), "v"), strings.TrimPrefix(strings.TrimSpace(version), "v")
	return version != "" && strings.EqualFold(tag, version)
}

// forgetManifest drops what an earlier install's manifest declared for an addon whose new package
// brings none and that the catalogue does not describe (B-27): the stored copy - its ui block
// (session_header, settings_url, own_updater), requires and release - and the policy's api_scopes,
// a grant nothing in the package backs. The rest of the runtime block stays in the policy, where an
// addon without a manifest keeps it: the directories, groups, ports and start order are facts
// about the addon that an older version has as well, and taking them away would stop it. The mode
// is not changed (AdoptInstalledAddons' reason). Says whether there was a copy.
func (a *SystemdAddons) forgetManifest(id string) bool {
	root := a.Scripts.Root
	if !addonIDRe.MatchString(id) {
		return false
	}
	if _, err := os.Lstat(root.join(AddonPolicyDir + "/" + id + AddonManifestSuffix)); err != nil {
		return false
	}
	root.removeAddonManifest(id)
	policyMu.Lock()
	defer policyMu.Unlock()
	if p := root.ReadAddonPolicy(id); p != nil && p.Runtime != nil && p.Runtime.APIScopes != nil {
		p.Runtime.APIScopes = nil
		if err := root.writeAddonPolicy(p); err != nil {
			slog.Warn("addon manifest: the policy's API scopes could not be dropped", "addon", id, "err", err)
		}
	}
	return true
}

// applyInstalledManifest is Install's step between the installer and the units: the package's
// manifest goes to the addon it names when the installer created or changed that addon's rc.d
// entry; every other fresh or updated addon takes the catalogue's word when there is one, and
// otherwise loses the stored copy an earlier install left (B-27: "rewritten by every install and
// update" - a package without a manifest declares nothing). What was applied is noted in the
// result. Returns the ids that got a policy.
func (a *SystemdAddons) applyInstalledManifest(ctx context.Context, m *manifest.Manifest, imgs PackageImages, fresh, touched []string, res *InstallResult) []string {
	var applied []string
	candidates := append(append([]string{}, fresh...), touched...)
	if m != nil {
		if !slices.Contains(candidates, m.ID) {
			res.Output += fmt.Sprintf("\n[manifest] the package's %s names %s, which the installer neither created nor changed (%s): not applied", manifest.FileName, m.ID, joinOrNone(candidates))
			slog.Warn("addon manifest: the package's manifest names an addon the install did not touch", "manifest", m.ID, "installed", candidates)
		} else if err := a.ApplyManifest(ctx, m.ID, m, SourceManifest); err != nil {
			res.Output += fmt.Sprintf("\n[manifest] %s: the package's declaration could not be applied: %v", m.ID, err)
			slog.Warn("addon manifest: not applied", "addon", m.ID, "err", err)
		} else {
			res.Output += fmt.Sprintf("\n[manifest] %s: the package's %s applied", m.ID, manifest.FileName)
			applied = append(applied, m.ID)
			a.keepPackageImages(m, imgs, res)
		}
	}
	root := a.Scripts.Root
	for _, id := range candidates {
		// the package's own manifest for the id, refused above, is not stood in for
		if slices.Contains(applied, id) || (m != nil && m.ID == id) {
			continue
		}
		// B-27: a package without a manifest replaced what the stored copy described; one whose
		// manifest names another addon leaves the copies of the others as they are
		bare := m == nil
		if !bare && root.ReadAddonManifest(id) != nil {
			continue
		}
		fb := a.fallbackManifest(ctx, id)
		if fb == nil {
			if bare && a.forgetManifest(id) {
				res.Output += fmt.Sprintf("\n[manifest] %s: the package carries no %s; the stored declaration of the earlier install was removed", id, manifest.FileName)
			}
			continue
		}
		if err := a.ApplyManifest(ctx, id, fb, SourceCatalog); err != nil {
			res.Output += fmt.Sprintf("\n[manifest] %s: the catalogue's declaration could not be applied: %v", id, err)
			slog.Warn("addon manifest: the catalogue's manifest not applied", "addon", id, "err", err)
			continue
		}
		// the catalogue's images are its own copies (task 100), not the package's: none are kept
		root.removeAddonImages(id)
		res.Output += fmt.Sprintf("\n[manifest] %s: the package carries no %s; the catalogue's declaration applied", id, manifest.FileName)
		applied = append(applied, id)
	}
	sort.Strings(applied)
	return applied
}

func joinOrNone(ids []string) string {
	if len(ids) == 0 {
		return "nothing"
	}
	return fmt.Sprint(ids)
}

// RefreshManifestRuntimes brings every stored policy's runtime block in line with the addon's
// stored manifest - a binary that learned a new key renders it from the same file - and gives an
// addon without a stored manifest the catalogue's adapter manifest when one exists (an addon
// installed before the catalogue carried adapters, or before this binary). The mode is never
// changed here: a working addon is not confined or freed by a boot (AdoptInstalledAddons' reason).
// Run at start before RefreshPolicyDropIns, which renders what this changed. Returns the ids it
// changed.
func (a *SystemdAddons) RefreshManifestRuntimes() []string {
	root := a.Scripts.Root
	var changed []string
	for id, p := range root.AddonPolicies() {
		m := root.ReadAddonManifest(id)
		if m == nil {
			if m = a.fallbackManifest(context.Background(), id); m == nil {
				continue
			}
			if err := root.writeAddonManifest(id, m); err != nil {
				slog.Warn("addon manifest: the catalogue's manifest could not be stored", "addon", id, "err", err)
				continue
			}
			slog.Info("addon manifest: the catalogue's manifest adopted for an installed addon", "addon", id)
		}
		rt := RuntimeFromManifest(m.Runtime)
		if rt == nil || rt.validate() != nil {
			continue
		}
		before, _ := json.Marshal(p)
		p.Runtime = rt
		p.pruneOpenPorts(rt)
		if after, _ := json.Marshal(p); string(after) == string(before) {
			continue
		}
		if err := root.writeAddonPolicy(p); err == nil {
			changed = append(changed, id)
		}
	}
	sort.Strings(changed)
	return changed
}

// PackageImages are the images a package's manifest declares, read out of the archive before the
// installer runs (occulited task 11): the image of each kind found, and why each other declared
// kind was not taken.
type PackageImages struct {
	Images  map[string][]byte
	Missing map[string]string
	Err     error // the archive could not be read for them
}

// keepPackageImages keeps the package's images beside the manifest just stored and says in the
// install log what was kept and what was not; a declared image the package does not carry is
// shown as the shell's fallback, and the install goes on either way.
func (a *SystemdAddons) keepPackageImages(m *manifest.Manifest, imgs PackageImages, res *InstallResult) {
	root := a.Scripts.Root
	if imgs.Err != nil {
		res.Output += fmt.Sprintf("\n[manifest] %s: the package's images could not be read: %v", m.ID, imgs.Err)
		slog.Warn("addon images: the package could not be read for its images", "addon", m.ID, "err", imgs.Err)
	}
	kept, err := root.keepAddonImages(m.ID, imgs.Images)
	if err != nil {
		res.Output += fmt.Sprintf("\n[manifest] %s: the package's images could not all be kept: %v", m.ID, err)
		slog.Warn("addon images: not all kept", "addon", m.ID, "err", err)
	}
	if len(kept) > 0 {
		res.Output += fmt.Sprintf("\n[manifest] %s: images kept from the package: %s", m.ID, strings.Join(kept, ", "))
	}
	for _, kind := range addonimage.Kinds {
		if why, ok := imgs.Missing[kind]; ok {
			res.Output += fmt.Sprintf("\n[manifest] %s: %s %s: %s; the system shows its fallback", m.ID, kind, addonimage.Declared(m.UI, kind), why)
		}
	}
}
