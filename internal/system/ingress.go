package system

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// The web ingress (openccu-lite task 406). An addon that ends a tunnel in its own process - a VPN,
// Tailscale - forwards its peers' web requests to lighttpd itself. Connected to the loopback those
// requests would look like a local process, which the system trusts (the classic RPC login, the
// HTTPS redirect, token ranges, the lockout per address). So an addon that declares
// runtime.web_ingress in its manifest gets a door of its own: lighttpd listens on
// /run/lighttpd-addons/<id>/proxy.sock with the HAProxy PROXY protocol, the addon sends the
// peer's address in front of each request, and lighttpd makes it the connection's address before
// the request is read - inside the ranges the addon declared and nowhere else (lighttpd's block
// answers 403 outside them; httpapi.IngressGuard checks again). The socket's directory is the web
// server's with the addon's group, 0750, so no other addon reaches it.
//
// What occulited renders, from the stored manifests, in the drop-in sync (before every start and
// reload of lighttpd, and after every install and uninstall): one fragment
// /usr/local/etc/config/lighttpd/occulite-ingress-<id>.conf per ingress addon - a $SERVER["socket"]
// block with hap-PROXY on, every connection trusted, no forwarding header read, the ranges, TLS off,
// and the fork's webui.conf (the gate, the API proxy, the headers) - and the socket's directory
// through the helper's ingressdir operation. The addon's id reaches occulited as X-Occulite-Ingress,
// set by the fork's global magnet script and by the gate from the socket the request came in on
// (request.server-addr; mod_setenv's request header would be stripped by those scripts). A fragment
// whose addon is gone is removed. The fork's lighttpd loads mod_extforward and includes the
// drop-in directory at global scope; its lite-lighttpd-addon-socket makes the directories of the
// fragments it finds as a safety net before lighttpd starts, so the web server always has a place
// to bind in.

// IngressFragmentPrefix starts the generated fragments' file names: occulite-ingress-<id>.conf.
// The prefix is occulited's own (lighttpdOwnPrefix), so the drop-in sync leaves them alone.
const IngressFragmentPrefix = lighttpdOwnPrefix + "ingress-"

// IngressSocketPath is the socket of an addon's ingress.
func IngressSocketPath(id string) string {
	return filepath.Join(priv.IngressDirBase, id, "proxy.sock")
}

// IngressFragment renders the lighttpd block of an addon's ingress.
func IngressFragment(id string, ranges []netip.Prefix) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s: the web ingress of this addon (runtime.web_ingress in its manifest), rendered from the stored manifest at every sync; not a file to edit\n", lighttpdCopyPrefix, id)
	b.WriteString("# A Unix socket speaking the HAProxy PROXY protocol: the addon sends a device's address in front of each\n")
	b.WriteString("# request, and lighttpd takes it as the connection's address - inside the declared ranges alone (403\n")
	b.WriteString("# otherwise). The socket's directory is the web server's with the addon's group (0750): nothing else\n")
	b.WriteString("# connects. Plain HTTP (a PROXY v2 SSL TLV marks a request the addon took over TLS as https); no HTTPS\n")
	b.WriteString("# redirect and no HSTS here; the gate, the API proxy and the headers as on the LAN sockets (webui.conf).\n")
	fmt.Fprintf(&b, "$SERVER[\"socket\"] == \"%s\" {\n", IngressSocketPath(id))
	b.WriteString("  ssl.engine = \"disable\"\n")
	b.WriteString("  server.socket-perms = \"0666\"\n")
	b.WriteString("  extforward.hap-PROXY = \"enable\"\n")
	b.WriteString("  extforward.forwarder = ( \"all\" => \"trust\" )\n")
	b.WriteString("  extforward.headers = ()\n")
	indent := "  "
	for _, p := range ranges {
		fmt.Fprintf(&b, "%s$HTTP[\"remoteip\"] != \"%s\" {\n", indent, p)
		indent += "  "
	}
	fmt.Fprintf(&b, "%surl.access-deny = ( \"\" )\n", indent)
	for range ranges {
		indent = indent[2:]
		fmt.Fprintf(&b, "%s}\n", indent)
	}
	b.WriteString("  include \"/etc/lighttpd/conf.d/webui.conf\"\n")
	b.WriteString("}\n")
	return b.String()
}

// IngressRanges is the parsed ranges of an installed addon's ingress declaration, nil for an addon
// without one (or an id that is none). Read from the stored manifest; cached for a few seconds
// because every proxied request asks.
func (r Root) IngressRanges(id string) []netip.Prefix {
	if !addonIDRe.MatchString(id) {
		return nil
	}
	ingressCache.mu.Lock()
	defer ingressCache.mu.Unlock()
	if e, ok := ingressCache.m[string(r)+"\x00"+id]; ok && time.Since(e.at) < ingressCacheTTL {
		return e.ranges
	}
	var ranges []netip.Prefix
	if m := r.ReadAddonManifest(id); m != nil && m.Runtime != nil && m.Runtime.WebIngress != nil {
		ranges, _ = m.Runtime.WebIngress.Prefixes()
	}
	ingressCache.m[string(r)+"\x00"+id] = ingressEntry{ranges: ranges, at: time.Now()}
	return ranges
}

const ingressCacheTTL = 5 * time.Second

type ingressEntry struct {
	ranges []netip.Prefix
	at     time.Time
}

var ingressCache = struct {
	mu sync.Mutex
	m  map[string]ingressEntry
}{m: map[string]ingressEntry{}}

// ingressAddons are the installed addons whose stored manifest declares a web ingress, by id, with
// their ranges.
func (r Root) ingressAddons() map[string][]netip.Prefix {
	out := map[string][]netip.Prefix{}
	entries, err := os.ReadDir(r.join(AddonPolicyDir))
	if err != nil {
		return out
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), AddonManifestSuffix)
		if !ok || !addonIDRe.MatchString(id) {
			continue
		}
		m := r.ReadAddonManifest(id)
		if m == nil || m.Runtime == nil || m.Runtime.WebIngress == nil {
			continue
		}
		if p, err := m.Runtime.WebIngress.Prefixes(); err == nil {
			out[id] = p
		}
	}
	return out
}

// syncIngressFragments renders the ingress fragments and makes the socket directories, and removes
// the fragments of addons that are gone. It reports whether anything changed (a reload is due).
func syncIngressFragments(root Root) (changed bool, results []DropinResult, err error) {
	dir := root.join(lighttpdDropinDir)
	want := root.ingressAddons()
	have := map[string]bool{}
	if entries, rerr := os.ReadDir(dir); rerr == nil {
		for _, e := range entries {
			if rest, ok := strings.CutPrefix(e.Name(), IngressFragmentPrefix); ok {
				if id, ok := strings.CutSuffix(rest, ".conf"); ok && addonIDRe.MatchString(id) {
					have[id] = true
				}
			}
		}
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return false, nil, rerr
	}
	ids := make([]string, 0, len(want)+len(have))
	for id := range want {
		ids = append(ids, id)
	}
	for id := range have {
		if _, ok := want[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		path := filepath.Join(dir, IngressFragmentPrefix+id+".conf")
		ranges, wanted := want[id]
		if !wanted {
			if err := Priv.Remove(path); err != nil {
				return changed, results, err
			}
			changed = true
			results = append(results, DropinResult{ID: id, Action: "ingress-removed"})
			continue
		}
		// the directory first: lighttpd binds in it the moment the fragment is read
		if err := Priv.IngressDir(id); err != nil {
			results = append(results, DropinResult{ID: id, Action: "ingress-error", Reason: "socket directory: " + err.Error()})
			continue
		}
		text := IngressFragment(id, ranges)
		if cur, rerr := os.ReadFile(path); rerr == nil && string(cur) == text {
			results = append(results, DropinResult{ID: id, Action: "ingress-kept"})
			continue
		}
		if err := Priv.WriteFile(path, []byte(text), 0o644); err != nil {
			return changed, results, err
		}
		changed = true
		results = append(results, DropinResult{ID: id, Action: "ingress-written"})
	}
	ingressCache.mu.Lock()
	ingressCache.m = map[string]ingressEntry{}
	ingressCache.mu.Unlock()
	return changed, results, nil
}

// syncIngressAfterChange renders the ingress fragments once more and restarts lighttpd when a
// socket came or went: at the end of an install and an uninstall, when the stored manifest and the
// addon's group are there (the drop-in sync of the installer ran before the manifest was applied)
// or gone. A restart, not a reload: lighttpd's graceful restart keeps a listening socket the new
// configuration dropped and hands it to the next generation, the old generation unlinks the
// socket's path on its way out, and a configuration that names the socket again then takes the
// inherited, pathless socket instead of binding a new one (measured on a lab system: after an
// uninstall and a reinstall the directory stayed empty until a restart). The restart runs after a
// short delay, off the request, so the answer to the page - which comes through lighttpd - is out
// first (classic RPC's rule).
func syncIngressAfterChange(ctx context.Context, root Root) {
	changed, results, err := syncIngressFragments(root)
	for _, r := range results {
		if r.Action == "ingress-error" {
			slog.Warn("addons: an addon's web ingress", "addon", r.ID, "err", r.Reason)
		}
	}
	if err != nil {
		slog.Warn("addons: the web ingress fragments after the change", "err", err)
		return
	}
	if !changed {
		return
	}
	delay := IngressRestartDelay
	slog.Info("addons: a web ingress socket came or went; lighttpd restarts", "in", delay)
	go func() {
		time.Sleep(delay)
		rctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var out []byte
		var err error
		if _, serr := os.Stat(root.join("/run/systemd/system")); serr == nil {
			out, err = run(rctx, "systemctl", "restart", "--no-pager", "--", "lighttpd.service")
		} else {
			out, err = run(rctx, root.join("/etc/init.d/S50lighttpd"), "restart")
		}
		if err != nil {
			slog.Warn("addons: lighttpd's restart for the web ingress failed", "err", err, "output", strings.TrimSpace(string(out)))
			return
		}
		slog.Info("addons: lighttpd restarted for the web ingress")
	}()
	_ = ctx
}

// IngressRestartDelay is how long an ingress change waits before lighttpd restarts (the tests
// shorten it).
var IngressRestartDelay = 1500 * time.Millisecond

// reloadLighttpdDropins reloads the web server (systemd, or the init script without it).
func reloadLighttpdDropins(ctx context.Context, root Root) error {
	var err error
	if _, serr := os.Stat(root.join("/run/systemd/system")); serr == nil {
		_, err = run(ctx, "systemctl", "reload", "lighttpd")
	} else {
		_, err = run(ctx, root.join("/etc/init.d/S50lighttpd"), "reload")
	}
	return err
}
