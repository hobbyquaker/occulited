package system

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/clientaddr"
	"github.com/hobbyquaker/occulited/internal/priv"
)

// Addon CGIs (task 18, D-36): lighttpd hands /addons/<name>/*.cgi to occulited, which runs the
// script the way lighttpd's mod_cgi did (tclsh, the CGI environment, the request body on stdin)
// but as the addon's own user when the addon has one - through the privilege helper. Static
// files under /addons are occulited's too since B-120 (AddonStatic: no link followed out of the
// addon's tree, nothing read with root's rights); lighttpd proxies all of /addons/ here.

// AddonWWW is the addon web tree (/www/addons -> /etc/config/addons/www).
const AddonWWW = "/usr/local/etc/config/addons/www"

// MaxCGIBody caps the request body a CGI receives.
const MaxCGIBody = 64 << 20

// SessionHeader carries the session lighttpd's gate validated (B-94, D-65); a CGI reads it as
// HTTP_X_OCCULITE_SESSION.
const SessionHeader = "X-Occulite-Session"

// sessionHeaderAlias reports a header that is not the session header but would reach a CGI as
// HTTP_X_OCCULITE_SESSION all the same: X_Occulite_Session, x.occulite.session. Go canonicalises
// only names made of letters, digits and dashes, and every other character becomes "_" in mod_cgi's
// variable names. lighttpd removes these from every request before it proxies (the gate and the
// fork's modules.conf); dropping them here too keeps a loopback caller from placing a second value
// beside the gate's.
func sessionHeaderAlias(name string) bool {
	if name == SessionHeader {
		return false
	}
	if len(name) != len(SessionHeader) {
		return false
	}
	for i := 0; i < len(name); i++ {
		c, want := name[i], SessionHeader[i]
		if want == '-' {
			if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') {
				return false
			}
			continue
		}
		if c|0x20 != want|0x20 {
			return false
		}
	}
	return true
}

var cgiNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}$`)

// CGIRunner serves the addon CGIs.
type CGIRunner struct {
	Root Root
	// Credential returns who the addon's CGI runs as: nil = root (no policy, busybox).
	Credential func(addon string) *priv.Credential
	Timeout    time.Duration
	// Static answers what is not a CGI (B-120); nil = 404, as when lighttpd served those itself.
	Static http.Handler
}

// ServeHTTP expects /addons/<name>/<path>. Anything but a .cgi/.ccc file goes to Static.
func (c CGIRunner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/addons/"), "/", 2)
	if len(parts) != 2 || !cgiNameRe.MatchString(parts[0]) {
		http.NotFound(w, r)
		return
	}
	name, rest := parts[0], parts[1]
	// PATH_INFO: everything after the script; the script is the first .cgi/.ccc component
	script, pathInfo := rest, ""
	for i, seg := range strings.Split(rest, "/") {
		if strings.HasSuffix(seg, ".cgi") || strings.HasSuffix(seg, ".ccc") {
			segs := strings.Split(rest, "/")
			script = strings.Join(segs[:i+1], "/")
			if i+1 < len(segs) {
				pathInfo = "/" + strings.Join(segs[i+1:], "/")
			}
			break
		}
	}
	if strings.Contains(script, "..") {
		http.NotFound(w, r)
		return
	}
	if !(strings.HasSuffix(script, ".cgi") || strings.HasSuffix(script, ".ccc")) {
		// a directory whose index is a CGI (lighttpd's index-file.names ends with index.cgi)
		index := ""
		if strings.HasSuffix(r.URL.Path, "/") {
			if st, err := os.Stat(c.Root.join(filepath.Join(AddonWWW, name, rest, "index.cgi"))); err == nil && !st.IsDir() {
				index = strings.TrimPrefix(strings.TrimSuffix(rest, "/")+"/index.cgi", "/")
			}
		}
		if index == "" {
			if c.Static != nil {
				c.Static.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		script, pathInfo = index, ""
	}
	file := c.Root.join(filepath.Join(AddonWWW, name, script))
	if st, err := os.Stat(file); err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxCGIBody+1))
	if err != nil || len(body) > MaxCGIBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	env := c.env(r, name, script, file, pathInfo, len(body))
	prog, args := c.Root.join("/bin/tclsh"), []string{file}
	if strings.HasSuffix(script, ".ccc") {
		prog, args = file, nil
	}
	cred := priv.Credential{}
	if c.Credential != nil {
		if cr := c.Credential(name); cr != nil {
			cred = *cr
		}
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res, err := Priv.RunAs(ctx, cred, prog, args, env, filepath.Dir(file), body)
	if err != nil {
		http.Error(w, "addon CGI: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeCGIResponse(w, r, res)
}

func (c CGIRunner) env(r *http.Request, name, script, file, pathInfo string, bodyLen int) []string {
	host, port, _ := net.SplitHostPort(r.Host)
	if host == "" {
		host = r.Host
	}
	if port == "" {
		port = "80"
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			port = "443"
		}
	}
	remote := clientaddr.Of(r) // lighttpd's element, never a client-sent one (B-230)
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"GATEWAY_INTERFACE=CGI/1.1",
		"SERVER_SOFTWARE=occulited",
		"SERVER_PROTOCOL=" + r.Proto,
		"SERVER_NAME=" + host,
		"SERVER_PORT=" + port,
		"SERVER_ADDR=" + host,
		"DOCUMENT_ROOT=" + c.Root.join("/www"),
		"REQUEST_METHOD=" + r.Method,
		"REQUEST_URI=" + r.URL.RequestURI(),
		"QUERY_STRING=" + r.URL.RawQuery,
		"SCRIPT_NAME=/addons/" + name + "/" + script,
		"SCRIPT_FILENAME=" + file,
		"REMOTE_ADDR=" + remote,
		"REMOTE_PORT=0",
		"CONTENT_LENGTH=" + strconv.Itoa(bodyLen),
		"REDIRECT_STATUS=200",
	}
	if pathInfo != "" {
		env = append(env, "PATH_INFO="+pathInfo, "PATH_TRANSLATED="+c.Root.join("/www")+pathInfo)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		env = append(env, "CONTENT_TYPE="+ct)
	}
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		env = append(env, "HTTPS=on")
	}
	for k, vs := range r.Header {
		uk := strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
		if uk == "CONTENT_TYPE" || uk == "CONTENT_LENGTH" || uk == "PROXY" || uk == "HOST" {
			continue
		}
		if sessionHeaderAlias(k) {
			continue
		}
		env = append(env, "HTTP_"+uk+"="+strings.Join(vs, ", "))
	}
	// B-114: Go keeps the Host header out of r.Header (it is r.Host), so the loop above never sees
	// it; mod_cgi and RFC 3875 pass it as HTTP_HOST, and a CGI that checks Origin against it (the
	// usual CSRF check on a CCU, ccu-addon-kit's) needs it. lighttpd's proxy passes the browser's
	// Host through. A Host in r.Header itself is not a request's and is skipped above.
	if r.Host != "" {
		env = append(env, "HTTP_HOST="+r.Host)
	}
	return env
}

// writeCGIResponse parses the script's headers (Status:, Location:, the rest verbatim) and
// streams the body. req is the request that ran the CGI: an X-Sendfile answer is served from it
// so that Range and the conditional headers reach http.ServeContent (B-36).
func writeCGIResponse(w http.ResponseWriter, req *http.Request, res priv.Result) {
	rd := bufio.NewReader(bytes.NewReader(res.Stdout))
	status := 0
	sendfile := ""
	for {
		line, err := rd.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // the blank line ends the headers (or the output is empty)
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.ContainsAny(k, " <") {
			// no header block at all: the whole output is the body
			rd = bufio.NewReader(bytes.NewReader(res.Stdout))
			break
		}
		if err != nil {
			// headers but no body
			rd = bufio.NewReader(bytes.NewReader(nil))
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch strings.ToLower(k) {
		case "status":
			if n, err := strconv.Atoi(strings.Fields(v + " ")[0]); err == nil {
				status = n
			}
		case "location":
			w.Header().Set("Location", v)
			if status == 0 {
				status = http.StatusFound
			}
		case "x-sendfile":
			// lighttpd's cgi.x-sendfile with docroot /usr/local/tmp: the CGI names a file, the
			// server delivers it (RedMatic's backup download)
			sendfile = v
		default:
			w.Header().Add(k, v)
		}
	}
	if sendfile != "" {
		clean := filepath.Clean(sendfile)
		if !strings.HasPrefix(clean, "/usr/local/tmp/") || strings.Contains(sendfile, "..") {
			http.Error(w, "X-Sendfile outside /usr/local/tmp", http.StatusForbidden)
			return
		}
		// through the privilege boundary, never with a plain os.Open (B-35): the daemon is the
		// unprivileged occulite user, and a confined addon's CGI (D-36) writes its archive as
		// its own user with mode 0600. The helper opens the file as root and passes the
		// descriptor back; occulited streams from it and never gains the privilege to name the
		// path. What it will open is the helper's SendfileDirs allowlist, not this prefix test -
		// the test above is the caller's half of the same rule.
		f, err := Priv.Open(clean)
		if err != nil {
			// the path stays out of the answer: it is the addon's temp file name, and on a
			// permission error it would tell the caller what it is not allowed to see (B-21)
			http.Error(w, "X-Sendfile: the file named by the addon cannot be read", http.StatusNotFound)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.Error(w, "X-Sendfile: the file named by the addon cannot be read", http.StatusNotFound)
			return
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Header().Del("Content-Length")
		// only a GET or HEAD carries meaningful Range and conditional headers; anything else
		// gets the whole file, as before
		sreq := req
		if req == nil || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
			sreq = &http.Request{Method: http.MethodGet}
		}
		http.ServeContent(w, sreq, st.Name(), st.ModTime(), f)
		return
	}
	if status == 0 {
		status = http.StatusOK
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if res.Exit != 0 && status == http.StatusOK && len(res.Stderr) > 0 {
		w.Header().Set("X-CGI-Exit", fmt.Sprint(res.Exit))
	}
	w.WriteHeader(status)
	_, _ = io.Copy(w, rd)
}
