package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// B-120 (D-107): lighttpd reads its addon drop-ins, /usr/local/etc/config/lighttpd/<id>.conf, at
// every start and reload. Until now an addon's installer put a symlink there that pointed into the
// addon's own directory, and lighttpd - root, then - parsed whatever a confined addon wrote into it:
// an `include_shell` is a command run as root. Now lighttpd runs as its own user (the fork's unit),
// and the drop-in is only ever a root-owned COPY that occulited validated: the addon ships its
// fragment as etc/lighttpd.conf in its tree (RedMatic and Homematic Manager already do), or its
// installer plants a link; the sync below reads the fragment, holds it to the directive allowlist -
// the proxy and URL directives a frontend needs, nothing that includes, runs, binds or reads
// anything - and writes the copy, or a <id>.conf.rejected note beside a link it removed.
//
// The addon's fragment is the source for as long as it exists and is not a template (homematic-manager
// task 69): a changed fragment - an update, a new port the addon rendered into it - replaces the copy
// at the next sync, over our own copy and over a file an installer or rc.d script wrote there as
// root, and our copy goes when its fragment is gone (the addon was uninstalled). Once B-119 runs an
// addon's installer and rc.d script as the addon's user, nothing of the addon can write this
// directory, so the fragment in its tree is the only thing it can change. A file without a fragment
// beside it - an older installer's rendering, next to a template - is validated in place and refused
// the same way when it fails. The fragment is read through os.Root: a link out of the addon's tree is
// never followed (it is refused, and nothing of the file it points at lands in the note).
//
// The sync runs as root before lighttpd starts (`occulited lighttpd-dropins`, the unit's
// ExecStartPre, which also covers an addon installed while lighttpd was down - B-141's case) and
// through the helper after every install and uninstall, followed by a reload when something changed.

const (
	// AddonsDir holds the addons' own trees.
	AddonsDir = "/usr/local/addons"
	// addonLighttpdFragment is where an addon ships its drop-in, relative to its tree.
	addonLighttpdFragment = priv.AddonFragmentRel
	// LighttpdRejectedSuffix marks a drop-in the validator refused: <id>.conf.rejected holds the reason.
	LighttpdRejectedSuffix = ".rejected"
	// lighttpdDropinMax bounds a fragment; a frontend's few blocks are a page or two.
	lighttpdDropinMax = priv.AddonFragmentMax
	// lighttpdCopyHeader is the first line of every copy, so a reader knows where it came from.
	lighttpdCopyHeader = lighttpdCopyPrefix + "%s: a validated copy of the addon's lighttpd fragment, refreshed from it at every sync\n"
	// lighttpdCopyPrefix starts every copy's first line; a drop-in that starts with it is ours
	lighttpdCopyPrefix = "# written by occulited from "
	// lighttpdOwnPrefix names the drop-ins occulited itself writes into the directory - the Log
	// page's occulite-debug.conf and occulite-accesslog.conf (loglevel.go). They are no addon's
	// fragment and carry directives the allowlist refuses (accesslog.filename, debug.*), so the
	// sync leaves them alone (openccu-lite B-190: it renamed the access log's drop-in to .rejected
	// at every start of lighttpd, and the switch never took effect).
	lighttpdOwnPrefix = "occulite-"
)

// DropinResult is what the sync did for one addon.
type DropinResult struct {
	ID string `json:"id"`
	// Action: kept (the copy matches), written (a new or changed copy), removed (no source any
	// more), rejected (the fragment failed the allowlist; the reason says which line).
	Action string `json:"action"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Line and Statement (occulited task 23): where in the fragment the validator stopped - the first
	// line of the refused statement, 1-based, and the statement itself (shortened); absent for a
	// refusal that names no statement (a link out of the tree, a file too large).
	Line      int    `json:"line,omitempty"`
	Statement string `json:"statement,omitempty"`
}

// LighttpdRejection is why an addon's lighttpd fragment is not in use (occulited task 23): the
// validator's verdict, kept in <id>.conf.rejected beside the drop-in the fragment would have been,
// and shown on the addon's entry (GET /addons, lighttpd_rejected), in the install output and on
// the Addons page until a later sync accepts a fragment of the addon. Reason is the sentence,
// Line the first line of the refused statement (1-based; 0 when the verdict names no statement)
// and Statement that statement, shortened to one line.
type LighttpdRejection struct {
	Reason    string `json:"reason"`
	Line      int    `json:"line,omitempty"`
	Statement string `json:"statement,omitempty"`
}

// Error is the one-line form the journal and the note's first line carry.
func (r *LighttpdRejection) Error() string {
	if r.Line <= 0 {
		return r.Reason
	}
	if r.Statement == "" {
		return fmt.Sprintf("%s (line %d)", r.Reason, r.Line)
	}
	return fmt.Sprintf("%s (line %d: %q)", r.Reason, r.Line, r.Statement)
}

// lighttpdRejectedNotePrefix starts the note's first line, as every sync since B-120 wrote it; the
// keyed lines after it (`# reason: `, `# line: `, `# statement: `) came with task 23, so a note an
// older occulited wrote still reads as a reason.
const lighttpdRejectedNotePrefix = "# occulited refused this addon's lighttpd fragment: "

// LighttpdRejection reads the verdict the last sync left for an addon, nil when its fragment is in
// use or it has none.
func (r Root) LighttpdRejection(id string) *LighttpdRejection {
	raw, err := os.ReadFile(r.join(filepath.Join(lighttpdDropinDir, id+".conf"+LighttpdRejectedSuffix)))
	if err != nil {
		return nil
	}
	return parseLighttpdRejectedNote(string(raw))
}

func parseLighttpdRejectedNote(note string) *LighttpdRejection {
	lines := strings.Split(strings.TrimRight(note, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], lighttpdRejectedNotePrefix) {
		return nil
	}
	rej := &LighttpdRejection{Reason: strings.TrimPrefix(lines[0], lighttpdRejectedNotePrefix)}
	for _, l := range lines[1:] {
		switch {
		case strings.HasPrefix(l, "# reason: "):
			rej.Reason = strings.TrimPrefix(l, "# reason: ")
		case strings.HasPrefix(l, "# line: "):
			rej.Line, _ = strconv.Atoi(strings.TrimPrefix(l, "# line: "))
		case strings.HasPrefix(l, "# statement: "):
			rej.Statement = strings.TrimPrefix(l, "# statement: ")
		}
	}
	return rej
}

func lighttpdRejectedNote(rej *LighttpdRejection) string {
	note := lighttpdRejectedNotePrefix + rej.Error() + "\n"
	if rej.Line > 0 {
		note += "# reason: " + rej.Reason + "\n"
		note += "# line: " + strconv.Itoa(rej.Line) + "\n"
		if rej.Statement != "" {
			note += "# statement: " + rej.Statement + "\n"
		}
	}
	return note
}

var (
	lighttpdDropinIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)
	// @PORT@ and the like: a template, rendered by the addon
	lighttpdTemplateRe = regexp.MustCompile(`@[A-Z][A-Z0-9_]*@`)
)

// SyncLighttpdDropins makes /usr/local/etc/config/lighttpd/*.conf the validated copies of the
// addons' fragments. It says whether anything on disk changed (a reload is due then); an error is
// a failure to read the directory or to write, never a rejected fragment.
func SyncLighttpdDropins(root Root) (changed bool, results []DropinResult, err error) {
	dir := root.join(lighttpdDropinDir)
	ids := map[string]bool{}
	if entries, rerr := os.ReadDir(dir); rerr == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".conf") {
				ids[strings.TrimSuffix(e.Name(), ".conf")] = true
			} else if strings.HasSuffix(e.Name(), ".conf"+LighttpdRejectedSuffix) {
				// a note without a drop-in: its addon's verdict, which goes once the addon is gone
				// (an uninstall left fragbad.conf.rejected behind on the lab box, occulited task 23)
				ids[strings.TrimSuffix(e.Name(), ".conf"+LighttpdRejectedSuffix)] = true
			}
		}
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return false, nil, rerr
	}
	if entries, rerr := os.ReadDir(root.join(AddonsDir)); rerr == nil {
		for _, e := range entries {
			if e.IsDir() {
				ids[e.Name()] = true
			}
		}
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		if strings.HasPrefix(id, lighttpdOwnPrefix) {
			// occulited's own drop-in: left alone, and a note an older sync left beside it goes
			if ch, rerr := removeIfExists(filepath.Join(dir, id+".conf"+LighttpdRejectedSuffix)); rerr != nil {
				return changed, results, rerr
			} else if ch {
				changed = true
			}
			continue
		}
		if lighttpdDropinIDRe.MatchString(id) {
			sorted = append(sorted, id)
		}
	}
	sort.Strings(sorted)
	for _, id := range sorted {
		res, ch, serr := syncLighttpdDropin(root, dir, id)
		if serr != nil {
			return changed, results, serr
		}
		changed = changed || ch
		if res.Action != "" {
			results = append(results, res)
		}
	}
	// the web ingress fragments (openccu-lite task 406), from the stored manifests
	ich, ires, ierr := syncIngressFragments(root)
	changed = changed || ich
	results = append(results, ires...)
	if ierr != nil {
		return changed, results, ierr
	}
	return changed, results, nil
}

func syncLighttpdDropin(root Root, dir, id string) (DropinResult, bool, error) {
	res := DropinResult{ID: id}
	dropin := filepath.Join(dir, id+".conf")
	rejected := dropin + LighttpdRejectedSuffix
	reject := func(rej *LighttpdRejection) (DropinResult, bool, error) {
		res.Action, res.Reason, res.Line, res.Statement = "rejected", rej.Error(), rej.Line, rej.Statement
		ch, err := applyRejected(root, dropin, rejected, rej)
		return res, ch, err
	}
	addonTree := root.join(filepath.Join(AddonsDir, id))
	fragment := filepath.Join(addonTree, addonLighttpdFragment)
	frag := readAddonFragment(addonTree)
	var src string
	lst, lerr := os.Lstat(dropin)
	switch {
	case lerr == nil && lst.Mode()&os.ModeSymlink != 0:
		// the installer's link: its target is the fragment, and it has to lie in the addon's tree.
		// A link straight to the fragment is read as the fragment is (through the helper when the
		// tree is closed to occulited's user, B-35); resolving it here would fail on that tree.
		if lt, rerr := os.Readlink(dropin); rerr == nil && filepath.Clean(lt) == fragment {
			src = fragment
			break
		}
		target, rerr := filepath.EvalSymlinks(dropin)
		if rerr != nil || !underDir(target, addonTree) {
			return reject(&LighttpdRejection{Reason: "the drop-in is a link that does not lead into the addon's own directory"})
		}
		src = target
	case lerr == nil && lst.Mode().IsRegular():
		switch {
		case frag.exists && !frag.template:
			// the addon's fragment wins over whatever file is here - our copy of an older state,
			// or an older installer's rendering (homematic-manager task 69)
			src = fragment
		case !frag.exists && isOurLighttpdCopy(dropin):
			// our copy of a fragment that is gone: the addon was uninstalled, or ships none any more
			res.Action = "removed"
			if err := Priv.Remove(dropin); err != nil {
				return res, false, err
			}
			_, err := removeIfExists(rejected)
			return res, true, err
		default:
			// a file an installer or rc.d wrote as root beside a template, or for an addon without a
			// fragment: validated in place
			src = dropin
		}
	case lerr == nil:
		return reject(&LighttpdRejection{Reason: "the drop-in is neither a file nor a link"})
	default:
		if !frag.exists {
			// nothing to do for this addon; a stale note goes
			_, err := removeIfExists(rejected)
			return res, false, err
		}
		src = fragment
	}
	res.Source = strings.TrimPrefix(src, string(root))
	if string(root) == "/" {
		res.Source = src
	}
	var raw []byte
	if src == fragment {
		if frag.problem != "" {
			return reject(&LighttpdRejection{Reason: frag.problem})
		}
		if frag.template {
			// a template the addon's installer renders itself (Homematic Manager's @PORT@ before
			// task 69): not a fragment to copy, and nothing to complain about while no rendered
			// file is there
			res.Action = "template"
			_, err := removeIfExists(rejected)
			return res, false, err
		}
		raw = frag.raw
	} else {
		st, serr := os.Stat(src)
		if serr != nil || !st.Mode().IsRegular() {
			return reject(&LighttpdRejection{Reason: "the fragment is not a regular file"})
		}
		if st.Size() > lighttpdDropinMax {
			return reject(&LighttpdRejection{Reason: fmt.Sprintf("the fragment is larger than %d bytes", lighttpdDropinMax)})
		}
		var rerr error
		raw, rerr = os.ReadFile(src)
		if rerr != nil {
			return reject(&LighttpdRejection{Reason: "the fragment cannot be read: " + rerr.Error()})
		}
	}
	if rej := validateLighttpdDropin(id, raw); rej != nil {
		return reject(rej)
	}
	var want []byte
	if src == dropin {
		want = raw // validated in place: the file stands as it is
	} else {
		want = append([]byte(fmt.Sprintf(lighttpdCopyHeader, res.Source)), raw...)
	}
	if lerr == nil && lst.Mode().IsRegular() {
		if have, herr := os.ReadFile(dropin); herr == nil && bytes.Equal(have, want) {
			res.Action = "kept"
			changed := false
			if lst.Mode().Perm() != 0o644 {
				// a file an installer wrote under a tight umask: lighttpd's own user has to read it
				if err := Priv.Chmod(dropin, 0o644); err != nil {
					return res, false, err
				}
				changed = true
			}
			_, err := removeIfExists(rejected)
			return res, changed, err
		}
	}
	if lerr == nil && lst.Mode()&os.ModeSymlink != 0 {
		// WriteFile renames over the link's path; make sure the link itself goes, not its target
		if err := Priv.Remove(dropin); err != nil {
			return res, false, err
		}
	}
	if err := Priv.WriteFile(dropin, want, 0o644); err != nil {
		return res, false, err
	}
	res.Action = "written"
	_, err := removeIfExists(rejected)
	return res, true, err
}

// addonFragment is an addon's etc/lighttpd.conf as the sync found it.
type addonFragment struct {
	exists   bool   // there is something at that path (a file, a link, anything)
	raw      []byte // the content, when it could be read
	problem  string // why it cannot be taken, when it cannot
	template bool   // it carries @NAME@ placeholders: the addon renders it itself
}

// readAddonFragment reads <tree>/etc/lighttpd.conf through os.Root (priv.Local.ReadAddonFragment), so
// a link - the file itself or a directory on the way - that leads out of the addon's tree is refused
// instead of followed: the sync runs as root, and an addon must not get a root-only file read, let
// alone quoted in its note. A confined addon's tree is closed to occulited's user since openccu-lite
// B-252 (its etc 0751, the fragment 0640): then the helper reads exactly that file, by the same rules
// (occulited B-35).
func readAddonFragment(tree string) addonFragment {
	path := filepath.Join(tree, addonLighttpdFragment)
	raw, err := priv.Local{}.ReadAddonFragment(path)
	if errors.Is(err, fs.ErrPermission) && Priv != nil {
		raw, err = Priv.ReadAddonFragment(path)
	}
	switch {
	case err == nil:
		return addonFragment{exists: true, raw: raw, template: lighttpdTemplateRe.Match(raw)}
	case errors.Is(err, fs.ErrNotExist):
		return addonFragment{}
	}
	for _, e := range []error{priv.ErrFragmentUnreachable, priv.ErrFragmentLink, priv.ErrFragmentNotRegular, priv.ErrFragmentTooLarge} {
		if errors.Is(err, e) {
			return addonFragment{exists: true, problem: e.Error()}
		}
	}
	return addonFragment{exists: true, problem: "the fragment cannot be read: " + err.Error()}
}

// isOurLighttpdCopy says whether a drop-in is a copy the sync wrote (its first line says so).
func isOurLighttpdCopy(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(lighttpdCopyPrefix))
	n, _ := io.ReadFull(f, head)
	return n == len(head) && string(head) == lighttpdCopyPrefix
}

// applyRejected takes a refused drop-in out of lighttpd's way - the link or the file goes - and
// leaves the reason beside it.
func applyRejected(root Root, dropin, rejected string, rej *LighttpdRejection) (bool, error) {
	changed := false
	if _, err := os.Lstat(dropin); err == nil {
		if err := Priv.Remove(dropin); err != nil {
			return false, err
		}
		changed = true
	}
	note := lighttpdRejectedNote(rej)
	if have, err := os.ReadFile(rejected); err != nil || string(have) != note {
		if err := Priv.WriteFile(rejected, []byte(note), 0o644); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func removeIfExists(path string) (bool, error) {
	if _, err := os.Lstat(path); err != nil {
		return false, nil
	}
	return false, Priv.Remove(path)
}

// underDir says whether path lies inside dir (both absolute, dir without a trailing slash).
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// ---- the allowlist ---------------------------------------------------------------------------

// lighttpdAllowedDirectives is what a frontend's fragment may set: mapping a path to the addon's own
// server, rewriting and redirecting under it, its own error pages, response headers, a few static
// file knobs. Everything else - include and include_shell above all, server.*, cgi.*, magnet.*,
// auth.*, ssl.*, $SERVER sockets, variables - is refused.
var lighttpdAllowedDirectives = map[string]bool{
	"url.redirect": true, "url.redirect-code": true,
	"url.rewrite": true, "url.rewrite-once": true, "url.rewrite-repeat": true, "url.rewrite-if-not-file": true,
	"url.access-deny": true,
	"proxy.server":    true, "proxy.header": true, "proxy.forwarded": true, "proxy.balance": true, "proxy.replace-http-host": true,
	"server.errorfile-prefix":    true,
	"setenv.add-response-header": true, "setenv.set-response-header": true,
	"setenv.add-request-header": true, "setenv.set-request-header": true,
	"index-file.names": true, "dir-listing.activate": true, "static-file.exclude-extensions": true,
	"mimetype.assign": true, "expire.url": true, "alias.url": true,
	"server.max-request-size": true, "server.stream-response-body": true, "server.stream-request-body": true,
	"server.max-keep-alive-idle": true,
}

var (
	lighttpdConditionRe = regexp.MustCompile(`^\$HTTP\["(url|querystring|request-method|useragent|referer|cookie|language|remoteip|scheme|host)"\]\s*(==|!=|=~|!~)\s*"((?:[^"\\]|\\.)*)"\s*\{$`)
	lighttpdElseRe      = regexp.MustCompile(`^\}\s*else\s+(.*)$`)
	// (?s): a statement spans lines while a parenthesis is open (lighttpdStatements), so the value
	// may hold line breaks - without it every multi-line value was refused as "not a directive" (B-54)
	lighttpdDirectiveRe = regexp.MustCompile(`(?s)^([a-z][a-z0-9.-]*)\s*(\+?=)\s*(.*)$`)
	lighttpdStringRe    = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	lighttpdValueRestRe = regexp.MustCompile(`^[\s()=>,0-9]*$`)
	lighttpdHostRe      = regexp.MustCompile(`"host"\s*=>\s*"((?:[^"\\]|\\.)*)"`)
	lighttpdSocketRe    = regexp.MustCompile(`"socket"\s*=>\s*"((?:[^"\\]|\\.)*)"`)
)

// ValidateLighttpdDropin holds an addon's fragment to the allowlist. The error (a
// *LighttpdRejection) names the first statement that fails, its line and why.
func ValidateLighttpdDropin(id string, conf []byte) error {
	if rej := validateLighttpdDropin(id, conf); rej != nil {
		return rej
	}
	return nil
}

func validateLighttpdDropin(id string, conf []byte) *LighttpdRejection {
	if !utf8.Valid(conf) || bytes.IndexByte(conf, 0) >= 0 {
		return &LighttpdRejection{Reason: "the fragment is not text"}
	}
	if len(conf) > lighttpdDropinMax {
		return &LighttpdRejection{Reason: fmt.Sprintf("the fragment is larger than %d bytes", lighttpdDropinMax)}
	}
	tree := filepath.Join(AddonsDir, id)
	www := filepath.Join(AddonWWW, id)
	depth := 0
	for _, stmt := range lighttpdStatements(stripLighttpdComments(string(conf))) {
		s := strings.TrimSpace(stmt.text)
		if s == "" {
			continue
		}
		// at reports a verdict on this statement: the sentence, where it starts, what it says
		at := func(reason string) *LighttpdRejection {
			return &LighttpdRejection{Reason: reason, Line: stmt.line, Statement: excerpt(s)}
		}
		switch {
		case s == "}":
			depth--
			if depth < 0 {
				return at("a block is closed that was never opened")
			}
			continue
		case s == "} else {":
			if depth < 1 {
				return at("an else without a block")
			}
			continue
		}
		if m := lighttpdElseRe.FindStringSubmatch(s); m != nil {
			if depth < 1 {
				return at("an else without a block")
			}
			if !lighttpdConditionRe.MatchString(strings.TrimSpace(m[1])) {
				return at("a condition that is not allowed")
			}
			continue
		}
		if strings.HasPrefix(s, "$") {
			if !lighttpdConditionRe.MatchString(s) {
				return at("a condition that is not allowed")
			}
			depth++
			continue
		}
		m := lighttpdDirectiveRe.FindStringSubmatch(s)
		if m == nil {
			return at("not a directive")
		}
		name, value := m[1], m[3]
		if !lighttpdAllowedDirectives[name] {
			return at(fmt.Sprintf("the directive %s is not allowed in an addon's fragment", name))
		}
		if strings.Count(value, "(") != strings.Count(value, ")") {
			return at("unbalanced parentheses")
		}
		// the value is strings, parentheses, arrows, commas and numbers - never a name (no
		// variables, no calls, no env.*)
		if !lighttpdValueRestRe.MatchString(lighttpdStringRe.ReplaceAllString(value, "")) {
			return at("a value that is not plain strings and numbers")
		}
		for _, str := range lighttpdStringRe.FindAllString(value, -1) {
			if strings.ContainsAny(str, "\n\r") {
				return at("a string with a line break")
			}
		}
		switch name {
		case "proxy.server":
			for _, h := range lighttpdHostRe.FindAllStringSubmatch(value, -1) {
				if !loopbackHost(h[1]) {
					return at(fmt.Sprintf("proxy.server may point at this system only, not at %q", h[1]))
				}
			}
			for _, so := range lighttpdSocketRe.FindAllStringSubmatch(value, -1) {
				if !underDir(so[1], tree) {
					return at(fmt.Sprintf("proxy.server's socket must lie in the addon's directory, not %q", so[1]))
				}
			}
		case "server.errorfile-prefix", "alias.url":
			for _, str := range lighttpdStringRe.FindAllString(value, -1) {
				p := strings.Trim(str, `"`)
				if name == "alias.url" && strings.HasPrefix(p, "/addons/") && !strings.Contains(p, "..") {
					continue // the URL side of the mapping
				}
				if !strings.HasPrefix(p, "/") {
					continue
				}
				if !underDir(p, tree) && !underDir(p, www) {
					return at(fmt.Sprintf("%s must point into the addon's directory, not %q", name, p))
				}
			}
		}
	}
	if depth != 0 {
		return &LighttpdRejection{Reason: "a block is not closed"}
	}
	return nil
}

func loopbackHost(h string) bool {
	h = strings.Trim(strings.TrimSpace(h), "[]")
	switch h {
	case "localhost", "::1", "0:0:0:0:0:0:0:1":
		return true
	}
	return strings.HasPrefix(h, "127.")
}

// lighttpdStatement is one statement of a fragment and the line (1-based) it starts on.
type lighttpdStatement struct {
	text string
	line int
}

// lighttpdStatements splits the comment-free text into statements: a line ends one when the
// parentheses are balanced outside strings; `{` closes a condition line. stripLighttpdComments
// keeps the lines where they were, so the numbers are the fragment's own.
func lighttpdStatements(conf string) []lighttpdStatement {
	var out []lighttpdStatement
	var cur strings.Builder
	depth := 0
	inStr := false
	esc := false
	line, start := 1, 1
	for _, r := range conf {
		switch {
		case esc:
			esc = false
		case inStr && r == '\\':
			esc = true
		case r == '"':
			inStr = !inStr
		case !inStr && r == '(':
			depth++
		case !inStr && r == ')':
			depth--
		case !inStr && r == '\n' && depth <= 0:
			out = append(out, lighttpdStatement{cur.String(), start})
			cur.Reset()
			line++
			start = line
			continue
		}
		if r == '\n' {
			line++
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, lighttpdStatement{cur.String(), start})
	}
	return out
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

// lighttpdDropinsAfterChange is what an install or uninstall ends with: the sync, and a reload of
// lighttpd when a drop-in came, went or changed. A failure is logged by the caller through the
// returned error; the install itself stands.
func lighttpdDropinsAfterChange(ctx context.Context, root Root) ([]DropinResult, error) {
	changed, results, err := SyncLighttpdDropins(root)
	if err != nil {
		return results, err
	}
	if !changed {
		return results, nil
	}
	return results, reloadLighttpdDropins(ctx, root)
}
