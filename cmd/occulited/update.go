package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/sysupdate"
)

// updateCmd is `occulited update` (occulited task 22, openccu-lite #9): the system update from the
// command line - over ssh, from a script or a timer - as the Updates page does it. It is a client
// of the running occulited's API with the console's own credential (auth.ConsoleTokenName, read
// from system.ConsoleTokenFile), so the web UI and the command line share one implementation: the
// release feed, the download with its sha256 check, the staging, the backup, the reboot into the
// recovery system. Root only, as the other console commands.
//
// The maintainer's decisions (Q&A 2026-10-07): any published version can be installed, a
// downgrade warns and asks (or needs --yes); a backup is taken before every install (--no-backup
// skips it); "latest" follows the Updates page's channel - prereleases while the system runs one
// - and --pre/--stable override it.
func updateCmd(args []string) int {
	o, err := parseUpdateArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "occulited update: %v\n%s\n", err, updateUsage)
		return updateExitUsage
	}
	if !updateIsRoot() {
		fmt.Fprintln(os.Stderr, "occulited update: only root may do this: run it as root on the system (ssh, or keyboard and display)")
		return updateExitError
	}
	cfg, err := config.Load(o.config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "occulited update: config:", err)
		return updateExitError
	}
	tok, err := os.ReadFile(updateTokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "occulited update: no console credential (%v): is occulited running? `systemctl status occulited`\n", err)
		return updateExitError
	}
	env := &updateEnv{
		api:    &updateAPI{base: "http://" + cfg.Listen, token: strings.TrimSpace(string(tok)), http: &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}}},
		in:     bufio.NewReader(os.Stdin),
		out:    os.Stdout,
		errOut: os.Stderr,
		tty:    isTerminal(os.Stdin),
		now:    time.Now,
		sleep:  time.Sleep,
	}
	return env.run(context.Background(), o)
}

// updateIsRoot and updateTokenFile are the command's view of the system; the tests replace them.
var (
	updateIsRoot    = priv.IsRoot
	updateTokenFile = system.ConsoleTokenFile
)

// The exit codes, for scripts. check follows dnf's check-update: 100 when an update is available.
const (
	updateExitOK        = 0
	updateExitError     = 1
	updateExitUsage     = 2
	updateExitDeclined  = 3   // a question not answered yes, or no terminal to ask on and no --yes
	updateExitAvailable = 100 // check: a newer release is available
)

const updateUsage = `usage: occulited update check [--pre|--stable] [--json]
       occulited update install [<version>|latest] [--pre|--stable] [--yes] [--no-backup] [--json]
       occulited update install --file <path> [--yes] [--no-backup] [--json]
       occulited update status [--json]
       occulited update discard [--json]
       every form takes --config FILE (default /usr/local/etc/occulite/occulited.json)
exit codes: 0 done (check: up to date), 1 failed, 2 usage, 3 not confirmed, 100 check: an update is available`

type updateOpts struct {
	cmd      string // check, install, status, discard
	version  string // install: a version, or "" / "latest"
	file     string // install --file
	channel  string // --pre / --stable: sysupdate.ChannelPre / ChannelStable; "" = the Updates page's
	yes      bool
	noBackup bool
	json     bool
	config   string
}

// parseUpdateArgs reads the words and flags in any order (`install 1.0.0-dev.41 --yes` and
// `install --yes 1.0.0-dev.41` alike). Only --file and --config take a value.
func parseUpdateArgs(args []string) (updateOpts, error) {
	o := updateOpts{config: "/usr/local/etc/occulite/occulited.json"}
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			words = append(words, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		takesValue := name == "file" || name == "config"
		if hasVal && !takesValue {
			return o, fmt.Errorf("--%s takes no value", name)
		}
		if takesValue && !hasVal {
			if i+1 >= len(args) {
				return o, fmt.Errorf("--%s needs a value", name)
			}
			i++
			val = args[i]
		}
		switch name {
		case "file":
			o.file = val
		case "config":
			o.config = val
		case "yes", "y":
			o.yes = true
		case "no-backup":
			o.noBackup = true
		case "pre":
			if o.channel == sysupdate.ChannelStable {
				return o, errors.New("--pre and --stable exclude each other")
			}
			o.channel = sysupdate.ChannelPre
		case "stable":
			if o.channel == sysupdate.ChannelPre {
				return o, errors.New("--pre and --stable exclude each other")
			}
			o.channel = sysupdate.ChannelStable
		case "json":
			o.json = true
		default:
			return o, fmt.Errorf("unknown option %s", a)
		}
	}
	if len(words) == 0 {
		return o, errors.New("which command?")
	}
	o.cmd = words[0]
	rest := words[1:]
	switch o.cmd {
	case "check":
		if len(rest) > 0 || o.file != "" || o.yes || o.noBackup {
			return o, errors.New("check takes only --pre, --stable and --json")
		}
	case "status", "discard":
		if len(rest) > 0 || o.file != "" || o.yes || o.noBackup || o.channel != "" {
			return o, fmt.Errorf("%s takes only --json", o.cmd)
		}
	case "install":
		if len(rest) > 1 {
			return o, fmt.Errorf("one version at a time, not %q", strings.Join(rest, " "))
		}
		if len(rest) == 1 {
			o.version = strings.TrimPrefix(rest[0], "v")
			if o.version == "latest" {
				o.version = ""
			}
		}
		if o.file != "" && (len(rest) > 0 || o.channel != "") {
			return o, errors.New("--file installs that file: no version, --pre or --stable beside it")
		}
		if o.version != "" && o.channel != "" {
			return o, errors.New("a named version is found in either channel: no --pre or --stable beside it")
		}
	default:
		return o, fmt.Errorf("unknown command %q", o.cmd)
	}
	return o, nil
}

// updateAPI is the running occulited's API, reached on its loopback listener with the console's
// credential.
type updateAPI struct {
	base, token string
	http        *http.Client
}

// apiErr is an error answer of the API: its status, code and message.
type apiErr struct {
	Status  int
	Code    string
	Message string
}

func (e *apiErr) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d %s", e.Status, e.Code)
}

// call sends a request and decodes a JSON answer into out (when not nil); an answer of 400 or
// more is an *apiErr. raw, when not nil, receives the answer's bytes (for --json).
func (c *updateAPI) call(ctx context.Context, method, path string, body io.Reader, size int64, ctype string, out any, raw *[]byte) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/system/v1"+path, body)
	if err != nil {
		return err
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("occulited does not answer on %s: %w", c.base, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		if res.StatusCode == http.StatusUnauthorized {
			e.Message = "occulited refused the console credential: restart occulited (`systemctl restart occulited`), which mints it anew"
		}
		return &apiErr{Status: res.StatusCode, Code: e.Error, Message: e.Message}
	}
	if raw != nil {
		*raw = b
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (c *updateAPI) getJSON(ctx context.Context, path string, out any, raw *[]byte) error {
	return c.call(ctx, http.MethodGet, path, nil, -1, "", out, raw)
}

func (c *updateAPI) sendJSON(ctx context.Context, method, path string, in, out any, raw *[]byte) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.call(ctx, method, path, bytes.NewReader(b), int64(len(b)), "application/json", out, raw)
}

// updateEnv is what a run talks to: the API, the terminal, the clock.
type updateEnv struct {
	api         *updateAPI
	in          *bufio.Reader
	out, errOut io.Writer
	tty         bool
	now         func() time.Time
	sleep       func(time.Duration)
	// backupWait bounds the wait for the backup; 0 = 30 minutes
	backupWait time.Duration
}

// The API's answers, as far as the command reads them.
type (
	updStaged struct {
		File          string `json:"file"`
		Size          int64  `json:"size"`
		Kind          string `json:"kind"`
		Version       string `json:"version"`
		Board         string `json:"board"`
		Warning       string `json:"warning"`
		RecoveryArmed bool   `json:"recovery_armed"`
		WayBack       bool   `json:"way_back"`
		Foreign       bool   `json:"foreign"`
		SHA256        string `json:"sha256"`
		HSTSCleared   bool   `json:"hsts_cleared"`
		HSTSError     string `json:"hsts_error"`
	}
	updState struct {
		Running   system.Version `json:"running"`
		Staged    *updStaged     `json:"staged"`
		Container string         `json:"container"`
		Feed      *struct {
			Enabled     bool                 `json:"enabled"`
			Checked     string               `json:"checked"`
			Error       string               `json:"error"`
			Downloading string               `json:"downloading"`
			Available   *sysupdate.Available `json:"available"`
			// B-57: the installed version is newer than the newest published one
			InstalledNewer bool `json:"installed_newer"`
		} `json:"feed"`
	}
	updTarget struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		State   struct {
			State string `json:"state"`
		} `json:"state"`
		LastBackup *struct {
			At    time.Time `json:"at"`
			OK    bool      `json:"ok"`
			Error string    `json:"error"`
			Name  string    `json:"name"`
			Size  int64     `json:"size"`
		} `json:"last_backup"`
	}
)

// say writes progress: to stdout for a person, to stderr under --json (stdout is the JSON then).
func (e *updateEnv) say(o updateOpts, f string, a ...any) {
	w := e.out
	if o.json {
		w = e.errOut
	}
	fmt.Fprintf(w, f+"\n", a...)
}

func (e *updateEnv) fail(err error) int {
	fmt.Fprintln(e.errOut, "occulited update:", err)
	return updateExitError
}

func (e *updateEnv) printJSON(v any) {
	enc := json.NewEncoder(e.out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (e *updateEnv) run(ctx context.Context, o updateOpts) int {
	switch o.cmd {
	case "status":
		return e.status(ctx, o)
	case "discard":
		return e.discard(ctx, o)
	case "check":
		return e.check(ctx, o)
	case "install":
		return e.install(ctx, o)
	}
	return updateExitUsage
}

// runningName is what the system runs, as the Updates page names it.
func runningName(v system.Version) string {
	if v.Lite != "" {
		return "openccu-lite " + v.Lite
	}
	return "OpenCCU " + v.Version
}

func mb(n int64) string { return fmt.Sprintf("%d MB", (n+(1<<19))>>20) }

func (e *updateEnv) status(ctx context.Context, o updateOpts) int {
	var st updState
	var raw []byte
	if err := e.api.getJSON(ctx, "/system-update", &st, &raw); err != nil {
		return e.fail(err)
	}
	if o.json {
		_, _ = e.out.Write(raw)
		if !bytes.HasSuffix(raw, []byte("\n")) {
			fmt.Fprintln(e.out)
		}
		return updateExitOK
	}
	fmt.Fprintf(e.out, "installed:   %s\n", runningName(st.Running))
	if st.Container != "" {
		fmt.Fprintln(e.out, "container:   a new release is a new template, swapped on the host")
	}
	switch s := st.Staged; {
	case s == nil:
		fmt.Fprintln(e.out, "staged:      none")
	default:
		armed := "staged, not yet scheduled"
		if s.RecoveryArmed {
			armed = "installs at the next boot"
		}
		v := ""
		if s.Version != "" {
			v = s.Version + ", "
		}
		fmt.Fprintf(e.out, "staged:      %s (%s%s) - %s\n", s.File, v, mb(s.Size), armed)
		if s.Warning != "" {
			fmt.Fprintf(e.out, "             %s\n", s.Warning)
		}
	}
	if f := st.Feed; f != nil {
		switch {
		case f.Downloading != "":
			fmt.Fprintf(e.out, "downloading: %s\n", f.Downloading)
		case f.Error != "":
			fmt.Fprintf(e.out, "last check:  %s: failed: %s\n", f.Checked, f.Error)
		case f.Available != nil && f.Available.Newer:
			fmt.Fprintf(e.out, "last check:  %s: %s (an update)\n", f.Checked, f.Available.Version)
		case f.Available != nil && f.InstalledNewer:
			// B-57: a prerelease round or a local build ahead of the feed is not "installed"
			fmt.Fprintf(e.out, "last check:  %s: newest published %s; the installed %s is newer\n", f.Checked, f.Available.Version, st.Running.Full())
		case f.Available != nil:
			fmt.Fprintf(e.out, "last check:  %s: %s (installed)\n", f.Checked, f.Available.Version)
		default:
			fmt.Fprintln(e.out, "last check:  none yet")
		}
		daily := "off"
		if f.Enabled {
			daily = "on"
		}
		fmt.Fprintf(e.out, "daily check: %s\n", daily)
	}
	return updateExitOK
}

func (e *updateEnv) discard(ctx context.Context, o updateOpts) int {
	var raw []byte
	if err := e.api.call(ctx, http.MethodDelete, "/system-update", nil, -1, "", nil, &raw); err != nil {
		return e.fail(err)
	}
	if o.json {
		e.printJSON(map[string]any{"staged": nil})
		return updateExitOK
	}
	fmt.Fprintln(e.out, "nothing staged any more")
	return updateExitOK
}

// releases asks the feed through the daemon: the channel's releases, newest first.
func (e *updateEnv) releases(ctx context.Context, channel string) (sysupdate.ReleaseList, error) {
	var l sysupdate.ReleaseList
	path := "/system-update/releases"
	if channel != "" {
		path += "?channel=" + url.QueryEscape(channel)
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := e.api.getJSON(c, path, &l, nil)
	return l, err
}

func channelName(l sysupdate.ReleaseList) string {
	n := map[string]string{sysupdate.ChannelPre: "prereleases included", sysupdate.ChannelStable: "releases only"}[l.Channel]
	if l.Channel == l.Default {
		return n + ", as the Updates page"
	}
	return n
}

func (e *updateEnv) check(ctx context.Context, o updateOpts) int {
	l, err := e.releases(ctx, o.channel)
	if err != nil {
		return e.fail(err)
	}
	var newest *sysupdate.Release
	if len(l.Releases) > 0 {
		newest = &l.Releases[0]
	}
	available := newest != nil && newest.Direction == "upgrade"
	if o.json {
		e.printJSON(map[string]any{"running": l.Running, "channel": l.Channel, "default_channel": l.Default, "newest": newest, "update_available": available})
	} else {
		fmt.Fprintf(e.out, "installed: %s\n", l.Running)
		fmt.Fprintf(e.out, "channel:   %s (%s)\n", l.Channel, channelName(l))
		switch {
		case newest == nil:
			fmt.Fprintln(e.out, "newest:    none published for this system")
		case available:
			fmt.Fprintf(e.out, "newest:    %s - an update (%s, %s)\n", newest.Version, newest.Published, newest.Notes)
		case newest.Direction == "downgrade":
			// B-57: nothing to install, and not "up to date" with an older version either
			fmt.Fprintf(e.out, "newest:    %s published - the installed %s is newer, nothing to install\n", newest.Version, l.Running)
		default:
			fmt.Fprintf(e.out, "newest:    %s - this system is up to date\n", newest.Version)
		}
	}
	if available {
		return updateExitAvailable
	}
	return updateExitOK
}

// confirm asks a yes/no question on the terminal; --yes answers it. No terminal and no --yes is
// no.
func (e *updateEnv) confirm(o updateOpts, question string) bool {
	if o.yes {
		return true
	}
	if !e.tty {
		fmt.Fprintln(e.errOut, "occulited update: not confirmed: there is no terminal to ask on - run it with --yes")
		return false
	}
	fmt.Fprintf(e.errOut, "%s [y/N] ", question)
	line, _ := e.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	fmt.Fprintln(e.errOut, "occulited update: not confirmed, nothing changed")
	return false
}

// installPlan is what install is about to do.
type installPlan struct {
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	Direction string `json:"direction"`
	File      string `json:"file,omitempty"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

// sha256File is the checksum of a file on the system, and the one published beside it as
// <file>.sha256 ("" when there is none).
func sha256File(path string) (got, published string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", err
	}
	if b, err := os.ReadFile(path + ".sha256"); err == nil {
		published = strings.ToLower(strings.SplitN(strings.TrimSpace(string(b))+" ", " ", 2)[0])
	}
	return hex.EncodeToString(h.Sum(nil)), published, nil
}

func (e *updateEnv) install(ctx context.Context, o updateOpts) int {
	var st updState
	if err := e.api.getJSON(ctx, "/system-update", &st, nil); err != nil {
		return e.fail(err)
	}
	if st.Container != "" {
		return e.fail(errors.New("this system is a container: a new release is a new template, swapped on the host (docs/install-lxc.md); nothing is installed from in here"))
	}
	running := st.Running.Full()
	plan := installPlan{From: running}
	if o.file != "" {
		abs, err := filepath.Abs(o.file)
		if err != nil {
			return e.fail(err)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return e.fail(err)
		}
		if !fi.Mode().IsRegular() {
			return e.fail(fmt.Errorf("%s is not a file", abs))
		}
		got, published, err := sha256File(abs)
		if err != nil {
			return e.fail(err)
		}
		if published != "" && published != got {
			return e.fail(fmt.Errorf("sha256 mismatch: %s.sha256 says %s, the file is %s", filepath.Base(abs), published, got))
		}
		plan.File, plan.Size, plan.SHA256 = abs, fi.Size(), got
		if v, ok := system.ReleaseFileVersion(abs); ok {
			plan.To = v
		}
		plan.Direction = sysupdate.Direction(running, plan.To)
		if published != "" {
			e.say(o, "%s: sha256 verified against %s.sha256", filepath.Base(abs), filepath.Base(abs))
		} else {
			e.say(o, "%s: no %s.sha256 beside it, not verified (sha256 %s)", filepath.Base(abs), filepath.Base(abs), got)
		}
	} else {
		channel := o.channel
		if o.version != "" {
			channel = sysupdate.ChannelAll
		}
		l, err := e.releases(ctx, channel)
		if err != nil {
			return e.fail(err)
		}
		var rel *sysupdate.Release
		for i := range l.Releases {
			if o.version == "" || l.Releases[i].Version == o.version {
				rel = &l.Releases[i]
				break
			}
		}
		switch {
		case rel == nil && o.version != "":
			return e.fail(fmt.Errorf("the release feed has no %s for this system", o.version))
		case rel == nil:
			return e.fail(fmt.Errorf("the release feed has no release for this system (%s)", channelName(l)))
		case o.version == "" && rel.Direction != "upgrade":
			if o.json {
				e.printJSON(map[string]any{"from": running, "newest": rel.Version, "up_to_date": true})
			} else {
				fmt.Fprintf(e.out, "up to date: %s runs, the newest (%s) is %s; name a version to install that one\n", running, channelName(l), rel.Version)
			}
			return updateExitOK
		}
		if rel.SHA256URL == "" {
			return e.fail(fmt.Errorf("%s publishes no .sha256: not installed from the command line; the Updates page can stage it unverified", rel.Name))
		}
		plan.To, plan.Direction, plan.File, plan.Size = rel.Version, rel.Direction, rel.Name, rel.Size
	}
	// the question - a downgrade's with the warning
	name := plan.To
	if name == "" {
		name = filepath.Base(plan.File)
	}
	var q string
	switch plan.Direction {
	case "upgrade":
		q = fmt.Sprintf("Install %s (now %s) and reboot?", name, running)
	case "same":
		q = fmt.Sprintf("Reinstall %s, the version that runs now, and reboot?", name)
	case "downgrade":
		fmt.Fprintf(e.errOut, "WARNING: %s is older than the running %s. A downgrade may lose settings that only the newer version knows; the backup taken before the install keeps them.\n", name, running)
		q = fmt.Sprintf("Downgrade to %s and reboot?", name)
	default:
		fmt.Fprintf(e.errOut, "NOTE: the version of %s cannot be compared with the running %s.\n", name, running)
		q = fmt.Sprintf("Install %s and reboot?", name)
	}
	if !e.confirm(o, q+" The system is unreachable for a few minutes.") {
		return updateExitDeclined
	}
	var backups []backupOutcome
	if o.noBackup {
		e.say(o, "backup: skipped (--no-backup)")
	} else {
		var err error
		if backups, err = e.backup(ctx, o); err != nil {
			return e.fail(err)
		}
	}
	staged, err := e.stage(ctx, o, plan)
	if err != nil {
		return e.fail(err)
	}
	// the answer is the reboot's: the request may also end with the system going down
	var res struct {
		Armed     bool   `json:"armed"`
		Rebooting bool   `json:"rebooting"`
		Message   string `json:"message"`
	}
	err = e.api.sendJSON(ctx, http.MethodPost, "/system-update/install", map[string]any{}, &res, nil)
	var ae *apiErr
	if err != nil && errors.As(err, &ae) {
		return e.fail(fmt.Errorf("install: %w (the file stays staged; `occulited update discard` removes it)", err))
	}
	if err != nil {
		// no answer at all: the system went down with the request
		res.Armed, res.Rebooting = true, true
	}
	if o.json {
		e.printJSON(map[string]any{"from": plan.From, "to": plan.To, "direction": plan.Direction, "backup": backups, "staged": staged, "armed": res.Armed, "rebooting": res.Rebooting, "message": res.Message})
	}
	if !res.Rebooting {
		return e.fail(fmt.Errorf("the update is armed, but the reboot did not start (%s): reboot to install it", res.Message))
	}
	e.say(o, "rebooting into the recovery system: it installs %s and starts the system again in a few minutes", staged.File)
	return updateExitOK
}

// stage puts the file in place through the daemon: the upload of --file, or the download of the
// release (sha256 checked by the daemon). A file for another board is discarded again.
func (e *updateEnv) stage(ctx context.Context, o updateOpts, plan installPlan) (*updStaged, error) {
	var staged updStaged
	if o.file != "" {
		f, err := os.Open(plan.File)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		e.say(o, "uploading %s (%s)…", filepath.Base(plan.File), mb(plan.Size))
		if err := e.api.call(ctx, http.MethodPost, "/system-update/upload?name="+url.QueryEscape(filepath.Base(plan.File)), f, plan.Size, "application/octet-stream", &staged, nil); err != nil {
			return nil, fmt.Errorf("upload: %w", err)
		}
	} else {
		e.say(o, "downloading %s (%s)…", plan.File, mb(plan.Size))
		if err := e.api.sendJSON(ctx, http.MethodPost, "/system-update/download", map[string]string{"version": plan.To}, &staged, nil); err != nil {
			return nil, fmt.Errorf("download: %w", err)
		}
		if staged.SHA256 == "" {
			e.discardQuietly(ctx)
			return nil, fmt.Errorf("download: %s was staged without its sha256 check; discarded", staged.File)
		}
		if plan.To != "" && staged.Version != "" && staged.Version != plan.To {
			e.discardQuietly(ctx)
			return nil, fmt.Errorf("download: %s is %s, not %s; discarded", staged.File, staged.Version, plan.To)
		}
		e.say(o, "%s: sha256 verified", staged.File)
	}
	if staged.Foreign {
		e.discardQuietly(ctx)
		return nil, fmt.Errorf("%s; discarded", staged.Warning)
	}
	if staged.WayBack {
		e.say(o, "%s is not an openccu-lite release: it may be the way back to OpenCCU", staged.File)
		if staged.HSTSCleared {
			e.say(o, "HSTS is switched off for it: OpenCCU sends none and serves a self-signed certificate")
		}
		if staged.HSTSError != "" {
			e.say(o, "HSTS could not be switched off: %s", staged.HSTSError)
		}
	}
	e.say(o, "staged: %s", staged.File)
	return &staged, nil
}

func (e *updateEnv) discardQuietly(ctx context.Context) {
	_ = e.api.call(ctx, http.MethodDelete, "/system-update", nil, -1, "", nil, nil)
}

// backupOutcome is one target's result of the backup before the install.
type backupOutcome struct {
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	File   string `json:"file,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Error  string `json:"error,omitempty"`
}

// backup is the Backup page's Back up now for every enabled target, waited for: the install goes
// on when at least one target holds the new backup.
func (e *updateEnv) backup(ctx context.Context, o updateOpts) ([]backupOutcome, error) {
	targets := func() ([]updTarget, error) {
		var v struct {
			Targets []updTarget `json:"targets"`
		}
		err := e.api.getJSON(ctx, "/backup/targets", &v, nil)
		var on []updTarget
		for _, t := range v.Targets {
			if t.Enabled {
				on = append(on, t)
			}
		}
		return on, err
	}
	list, err := targets()
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	if len(list) == 0 {
		return nil, errors.New("backup: no backup target is enabled - set one up on the Backup page, or skip the backup with --no-backup")
	}
	for _, t := range list {
		if t.State.State == "running" {
			return nil, fmt.Errorf("backup: a backup to %s is running; try again when it is done", t.Name)
		}
	}
	start := e.now().Add(-2 * time.Second)
	if err := e.api.sendJSON(ctx, http.MethodPost, "/backup/run", map[string]any{}, nil, nil); err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	names := make([]string, 0, len(list))
	for _, t := range list {
		names = append(names, t.Name)
	}
	e.say(o, "backup: to %s…", strings.Join(names, ", "))
	wait := e.backupWait
	if wait == 0 {
		wait = 30 * time.Minute
	}
	deadline := e.now().Add(wait)
	for {
		e.sleep(3 * time.Second)
		list, err = targets()
		if err != nil {
			return nil, fmt.Errorf("backup: %w", err)
		}
		done := true
		for _, t := range list {
			if t.State.State == "running" || t.LastBackup == nil || t.LastBackup.At.Before(start) {
				done = false
			}
		}
		if done {
			break
		}
		if e.now().After(deadline) {
			return nil, fmt.Errorf("backup: not done after %s; nothing installed (the Backup page shows the run)", wait)
		}
	}
	var out []backupOutcome
	ok := false
	for _, t := range list {
		r := backupOutcome{Target: t.Name, OK: t.LastBackup.OK, File: t.LastBackup.Name, Size: t.LastBackup.Size, Error: t.LastBackup.Error}
		out = append(out, r)
		if r.OK {
			ok = true
			e.say(o, "backup: %s: %s (%s)", t.Name, r.File, mb(r.Size))
		} else {
			e.say(o, "backup: %s: failed: %s", t.Name, r.Error)
		}
	}
	if !ok {
		return out, errors.New("backup: no target holds the new backup; nothing installed (fix the target on the Backup page, or skip the backup with --no-backup)")
	}
	return out, nil
}
