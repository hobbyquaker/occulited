package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// InstallResult is what the firmware's install_addon reported.
type InstallResult struct {
	Exit           int     `json:"exit"`
	Meaning        string  `json:"meaning"`
	RebootRequired bool    `json:"reboot_required"`
	Output         string  `json:"output,omitempty"`
	Seconds        float64 `json:"seconds"`
	// StoppedAddons are the addons that ran before an installer that failed or asks for a reboot
	// and are stopped afterwards (B-98, D-67): nothing starts them, so the page asks the user.
	StoppedAddons []StoppedAddon `json:"stopped_addons,omitempty"`
}

// StoppedAddon is one addon of InstallResult.StoppedAddons.
type StoppedAddon struct {
	ID string `json:"id"`
	// StartsAtBoot: the next boot starts it anyway - its rc.d script is executable, its unit is not
	// switched off on the Services page, and the box is not in safe mode
	StartsAtBoot bool `json:"starts_at_boot"`
}

// installMeanings are OpenCCU's install_addon exit codes plus the addon convention.
var installMeanings = map[int]string{
	0:   "installed",
	10:  "installed, reboot required",
	13:  "unsupported platform",
	101: "no archive at /usr/local/tmp/new_addon.tar.gz",
	102: "archive could not be unpacked (not a .tar.gz?)",
	103: "archive could not be removed",
	104: "archive has no executable update_script at its top level",
	105: "checksum verification failed",
	106: "checksum mismatch (.sha256)",
}

// The refusals of Uninstall before any script runs.
var (
	errInvalidAddonID = errors.New("invalid addon id")
	errUnknownAddon   = errors.New("unknown addon")
)

// MaxAddonSize caps an upload; RedMatic with its Node runtime is ~100 MB.
const MaxAddonSize = 300 << 20

// Install stores the uploaded archive where install_addon expects it and runs the installer,
// exactly as the WebUI's cp_software.cgi does. The reader is consumed and closed.
func (b AddonScripts) Install(ctx context.Context, archive io.Reader) (*InstallResult, error) {
	tmpDir := b.Root.join("/usr/local/tmp")
	if err := Priv.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	target := filepath.Join(tmpDir, "new_addon.tar.gz")
	// the archive is written into occulited's own staging directory and moved into place
	// (same filesystem) - as root that is one rename, as occulite the helper does the rename; one
	// the install API staged already (B-4) is moved as it is
	sa, ok := archive.(*StagedArchive)
	if !ok {
		var err error
		if sa, err = StageAddonArchive(b.Root, "new_addon.tar.gz", archive); err != nil {
			return nil, err
		}
	}
	staged := sa.Path
	installer := b.Root.join("/bin/install_addon")
	if _, err := os.Stat(installer); err != nil {
		_ = os.Remove(staged)
		return nil, errors.New("this system has no /bin/install_addon")
	}
	if err := Priv.Rename(staged, target); err != nil {
		_ = os.Remove(staged)
		return nil, err
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	out, runErr := b.command(ctx, installer)
	res := &InstallResult{Output: strings.TrimSpace(string(out)), Seconds: time.Since(start).Seconds()}
	var ee *exec.ExitError
	var pe *priv.ExitError
	switch {
	case runErr == nil:
		res.Exit = 0
	case errors.As(runErr, &pe):
		res.Exit = pe.ExitCode()
	case errors.As(runErr, &ee):
		res.Exit = ee.ExitCode()
	default:
		return nil, runErr
	}
	res.Meaning = installMeanings[res.Exit]
	if res.Meaning == "" {
		res.Meaning = fmt.Sprintf("update_script failed with exit code %d", res.Exit)
	}
	res.RebootRequired = res.Exit == 10
	_ = remove(target) // install_addon removes it itself; belt and braces
	// the archive named the addon, not the caller: every cached scan verdict is stale now
	ForgetAddonScans("")
	// B-120: the addon's lighttpd fragment becomes a validated copy, and lighttpd is reloaded. A
	// refused fragment goes into the output too (occulited task 23): until then the author saw only
	// the journal and <id>.conf.rejected, and the install read as a success.
	results, lerr := lighttpdDropinsAfterChange(ctx, b.Root)
	for _, d := range results {
		if d.Action == "rejected" {
			slog.Warn("addons: an addon's lighttpd fragment was refused", "addon", d.ID, "reason", d.Reason)
			res.Output += fmt.Sprintf("\n[lighttpd] %s: the addon's lighttpd fragment was refused and is not in use: %s", d.ID, d.Reason)
		}
	}
	if lerr != nil {
		slog.Warn("addons: the lighttpd drop-ins after the install", "err", lerr)
	}
	return res, nil
}

// UninstallResult is what an uninstall answers: the script's own output, unchanged, and what the
// system removed after it (openccu-lite B-283). A confined addon's uninstall runs as its user
// and its `rm` of the rc.d entry, the www link and its directories is refused - root owns the
// parents - so its output carries "permission denied" lines that read as a failure; the list says
// the system did that removal.
type UninstallResult struct {
	Output string `json:"output"`
	// SystemRemoved are the paths the system removed after the script, in the order it did: the
	// rc.d entry, the www link, the emptied standard directories, the monit fragment, the
	// hm_addons.cfg entry (named as "hm_addons.cfg: <id>"), and on systemd the addon's policy
	// files. Only what was there and went.
	SystemRemoved []string `json:"system_removed,omitempty"`
}

// Uninstall runs the addon's rc.d script with `uninstall` and removes the rc.d entry, exactly as
// cp_software.cgi does, then what an addon commonly leaves behind: its hm_addons.cfg entry and a
// monit fragment. Returns the script output and what was removed after it; a failing script is
// reported as the WebUI reports it (which writes /var/log/addon-uninstall-error.log — here the
// output goes to the caller).
func (b AddonScripts) Uninstall(ctx context.Context, id string) (UninstallResult, error) {
	var res UninstallResult
	if strings.ContainsAny(id, "/\\ ") || id == "" {
		return res, errInvalidAddonID
	}
	script := b.Root.join("/usr/local/etc/config/rc.d/" + id)
	if _, err := os.Lstat(script); err != nil {
		return res, errUnknownAddon
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// occulited B-118: the addon's www links named other than its id, looked at before the script
	// empties the tree they lead into
	otherWWW := addonOtherWWWLinks(b.Root, id)
	// B-119: a confined addon's uninstall is its own code and runs as its user - not in the install
	// scope, which cannot drop root's groups (systemd-run --scope --uid keeps them); the unit was
	// stopped before, and an uninstall starts nothing. Any other runs as root, as the WebUI ran it.
	var out []byte
	var runErr error
	if b.credential(id) != nil {
		out, runErr = b.addonScript(ctx, id, script, "uninstall")
	} else {
		out, runErr = b.command(ctx, script, "uninstall")
	}
	// what went is recorded for the answer (B-283): a path that was not there is not a removal
	// (the helper's remove treats a missing path as done, so it is looked at first)
	rm := func(box string, del func(string) error) {
		if _, err := os.Lstat(b.Root.join(box)); err != nil {
			return
		}
		if err := del(b.Root.join(box)); err == nil {
			res.SystemRemoved = append(res.SystemRemoved, box)
		}
	}
	// the WebUI removes the rc.d entry unconditionally after `uninstall` (cp_software.cgi:
	// `exec rm -rf $script`), and so do we — otherwise a half-removed addon keeps a ghost row. With
	// it the addon's own script behind the addon-rc wrapper (28.8) and - B-119, what the script
	// could not remove as the addon's user, because root owns the parent - its www link, or the
	// directory once the script emptied it. rc.d and the web trees are root's to run, so the
	// helper's own operation removes them (openccu-lite B-293).
	removed, err := Priv.RemoveAddonEntry(script, b.Root.join(AddonWWW+"/"+id), false)
	if err != nil {
		slog.Warn("addons: the rc.d entry or the web link could not be removed", "addon", id, "err", err)
	}
	for _, p := range removed {
		res.SystemRemoved = append(res.SystemRemoved, b.Root.onBox(p))
	}
	// B-118: and its www links named other than its id (battery-config, longpressdim-config) - a
	// link only, as the id's own: a directory an installer put there is no link into the addon
	for _, l := range otherWWW {
		if fi, err := os.Lstat(l); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		removed, err := Priv.RemoveAddonEntry("", l, false)
		if err != nil {
			slog.Warn("addons: a web link of the addon could not be removed", "addon", id, "link", b.Root.onBox(l), "err", err)
		}
		for _, p := range removed {
			res.SystemRemoved = append(res.SystemRemoved, b.Root.onBox(p))
		}
	}
	// its standard directories, only once the script emptied them, never with anything left inside
	// (an addon that keeps its configuration for a reinstall keeps it). Root's uninstall did all
	// this itself; for it these are no-ops. The addon's directory is no generic write's
	// (openccu-lite B-294): the helper's own operation removes it, and only empty.
	rm("/usr/local/addons/"+id, Priv.RemoveAddonHome)
	rm("/usr/local/etc/config/addons/"+id, Priv.RemoveAddonHome) // B-295: likewise its config directory
	rm("/usr/local/etc/monit-"+id+".cfg", remove)
	cfgPath := b.Root.join("/usr/local/etc/config/hm_addons.cfg")
	if entries := ParseHMAddonsCfg(readFile(cfgPath)); entries[id].ConfigURL != "" || entries[id].Name != "" {
		delete(entries, id)
		if writeFileAtomic(cfgPath, []byte(WriteHMAddonsCfg(entries)), 0o664) == nil {
			res.SystemRemoved = append(res.SystemRemoved, "hm_addons.cfg: "+id)
		}
	}
	ForgetAddonScans(id)
	res.Output = strings.TrimSpace(string(out))
	if runErr != nil {
		return res, fmt.Errorf("uninstall script: %w", runErr)
	}
	if _, err := lighttpdDropinsAfterChange(ctx, b.Root); err != nil {
		slog.Warn("addons: the lighttpd drop-ins after the uninstall", "err", err)
	}
	return res, nil
}

// WriteHMAddonsCfg serialises entries in the Tcl `array get` form the firmware reads.
func WriteHMAddonsCfg(entries map[string]AddonSettings) string {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var sb strings.Builder
	for _, id := range ids {
		e := entries[id]
		langs := make([]string, 0, len(e.Description))
		for l := range e.Description {
			langs = append(langs, l)
		}
		sort.Strings(langs)
		var desc strings.Builder
		for i, l := range langs {
			if i > 0 {
				desc.WriteString(" ")
			}
			fmt.Fprintf(&desc, "%s {%s}", l, e.Description[l])
		}
		fmt.Fprintf(&sb, "%s {CONFIG_URL %s CONFIG_DESCRIPTION {%s} ID %s CONFIG_NAME %s}\n", id, tclWord(e.ConfigURL), desc.String(), id, tclWord(e.Name))
	}
	return sb.String()
}

// tclWord braces a value that contains whitespace, as `array get` would.
func tclWord(s string) string {
	if s == "" {
		return "{}"
	}
	if strings.ContainsAny(s, " \t\n{}") {
		return "{" + s + "}"
	}
	return s
}

// UpdateInfo is the result of an addon's own update check.
type UpdateInfo struct {
	Installed string `json:"installed"`
	Available string `json:"available"`
	// UpdateAvailable is a string comparison, exactly as the WebUI does it (the addon howto
	// warns that the CCU compares strings, not versions).
	UpdateAvailable bool   `json:"update_available"`
	URL             string `json:"url"`
	Error           string `json:"error,omitempty"`
}

// CheckUpdate calls the addon's `Update:` CGI through the local lighttpd with
// ?cmd=check_version&version=<installed>, as the Zusatzsoftware page does.
func (b AddonScripts) CheckUpdate(ctx context.Context, a Addon, base string) UpdateInfo {
	info := UpdateInfo{Installed: a.Version}
	if a.Update == "" {
		info.Error = "addon has no update check"
		return info
	}
	u := a.Update
	if strings.HasPrefix(u, "/") {
		u = base + u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	info.URL = u + sep + "cmd=check_version&version=" + a.Version
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	if strings.HasPrefix(a.Update, "/") && b.UpdateToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.UpdateToken)
	}
	hc := http.DefaultClient
	if b.HTTP != nil && !strings.HasPrefix(a.Update, "/") {
		hc = b.HTTP // task 231: an addon's own https URL trusts occulited's store
	}
	res, err := hc.Do(req)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	info.Available = strings.TrimSpace(string(body))
	if res.StatusCode != 200 {
		info.Error = fmt.Sprintf("update check answered %d", res.StatusCode)
		return info
	}
	if av := strings.ToLower(info.Available); av != "" && av != "n/a" && av != strings.ToLower(a.Version) {
		info.UpdateAvailable = true
	}
	return info
}

// Reboot asks the firmware to reboot; nothing after this returns.
func (b AddonScripts) Reboot(ctx context.Context) error {
	for _, p := range []string{"/sbin/reboot", "/bin/reboot"} {
		if _, err := os.Stat(b.Root.join(p)); err == nil {
			// reboot returns at once; through the helper it is a plain run. It starts a moment after
			// this returns, so the route's answer reaches the browser before the box goes away.
			go func(p string) {
				time.Sleep(PowerDelay)
				_, _ = run(context.Background(), b.Root.join(p))
			}(p)
			return nil
		}
	}
	return errors.New("no reboot command")
}

// addonOtherWWWLinks are the entries of AddonWWW other than the addon's id that are links into its
// own directory /usr/local/addons/<id> (occulited B-118; the install side is B-115's
// addonPublicWWW): by the link's text, made absolute against AddonWWW - the tree may be gone
// already - or, where it resolves, by where it ends. Full paths.
func addonOtherWWWLinks(root Root, id string) []string {
	dir := root.join(AddonWWW)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	homes := []string{"/usr/local/addons/" + id, root.join("/usr/local/addons/" + id)}
	if real, err := filepath.EvalSymlinks(root.join("/usr/local/addons/" + id)); err == nil {
		homes = append(homes, real)
	}
	into := func(p string) bool {
		for _, h := range homes {
			if p == h || underDir(p, h) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, e := range entries {
		if e.Name() == id || e.Type()&os.ModeSymlink == 0 {
			continue
		}
		l := filepath.Join(dir, e.Name())
		target, err := os.Readlink(l)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		if into(filepath.Clean(target)) {
			out = append(out, l)
			continue
		}
		if real, err := filepath.EvalSymlinks(l); err == nil && into(real) {
			out = append(out, l)
		}
	}
	return out
}
