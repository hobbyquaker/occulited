package system

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/priv"
)

type fakePriv struct {
	priv.Local
	// opened records every X-Sendfile path that went through the privilege boundary (B-35);
	// openAs is the file the fake helper hands back, openErr the refusal it answers with.
	opened  []string
	openAs  string
	openErr error
	last    struct {
		cred priv.Credential
		name string
		args []string
		env  []string
		dir  string
		in   []byte
	}
	out priv.Result
}

func (f *fakePriv) RunAs(_ context.Context, cred priv.Credential, name string, args []string, env []string, dir string, stdin []byte) (priv.Result, error) {
	f.last.cred, f.last.name, f.last.args, f.last.env, f.last.dir, f.last.in = cred, name, args, env, dir, stdin
	return f.out, nil
}

func (f *fakePriv) Open(path string) (*os.File, error) {
	f.opened = append(f.opened, path)
	if f.openErr != nil {
		return nil, f.openErr
	}
	if f.openAs != "" {
		return priv.Local{}.Open(f.openAs)
	}
	return priv.Local{}.Open(path)
}

func TestCGIRunner(t *testing.T) {
	r := rootWith(t, map[string]string{"usr/local/etc/config/addons/www/mosq/index.cgi": "#!/bin/tclsh\n", "usr/local/etc/config/addons/www/mosq/run.ccc": "x", "bin/tclsh": ""})
	fp := &fakePriv{out: priv.Result{Stdout: []byte("Content-Type: text/plain\r\nX-Addon: yes\r\nStatus: 201 Created\r\n\r\nhello")}}
	old := Priv
	Priv = fp
	t.Cleanup(func() { Priv = old })
	c := CGIRunner{Root: r, Credential: func(a string) *priv.Credential {
		if a == "mosq" {
			return &priv.Credential{UID: 30004, GID: 30004}
		}
		return nil
	}}
	req := httptest.NewRequest("POST", "http://box.lan/addons/mosq/index.cgi/extra/path?sid=@abc@&x=1", strings.NewReader("a=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", "sid=abc")
	req.Header["Host"] = []string{"forged.example"} // never a server request's: r.Host is the one
	// through lighttpd (B-230): a client-sent element first, lighttpd's last; only the last counts
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Forwarded-For", "10.99.1.1, 192.168.1.9")
	// B-94: the gate's session header reaches the CGI; a look-alike that maps to the same variable
	// does not (lighttpd removes those; a raw map entry stands for a caller on the loopback)
	req.Header.Set("X-Occulite-Session", "ABCDEFGHIJ")
	req.Header["X_Occulite_Session"] = []string{"FORGED0000"}
	req.Header["x.occulite.session"] = []string{"FORGED1111"}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, req)
	if w.Code != 201 || w.Body.String() != "hello" || w.Header().Get("X-Addon") != "yes" {
		t.Fatalf("%d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if fp.last.cred.UID != 30004 || !strings.HasSuffix(fp.last.name, "/bin/tclsh") || !strings.HasSuffix(fp.last.args[0], "/addons/www/mosq/index.cgi") || string(fp.last.in) != "a=b" {
		t.Errorf("%+v", fp.last)
	}
	env := strings.Join(fp.last.env, "\n")
	for _, want := range []string{"REQUEST_METHOD=POST", "QUERY_STRING=sid=@abc@&x=1", "SCRIPT_NAME=/addons/mosq/index.cgi", "PATH_INFO=/extra/path", "HTTP_COOKIE=sid=abc", "REMOTE_ADDR=192.168.1.9", "CONTENT_TYPE=application/x-www-form-urlencoded", "CONTENT_LENGTH=3", "SERVER_NAME=box.lan", "HTTP_HOST=box.lan", "HTTP_X_OCCULITE_SESSION=ABCDEFGHIJ"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s:\n%s", want, env)
		}
	}
	// B-114: Host reaches the CGI once, as the request's (Go keeps it out of r.Header)
	if n := strings.Count(env, "HTTP_HOST="); n != 1 || strings.Contains(env, "forged.example") {
		t.Errorf("HTTP_HOST %d times:\n%s", n, env)
	}
	if n := strings.Count(env, "HTTP_X_OCCULITE_SESSION"); n != 1 || strings.Contains(env, "FORGED") || strings.Contains(env, "x.occulite") {
		t.Errorf("the session header's look-alikes reached the CGI (%d):\n%s", n, env)
	}
	// a .ccc runs directly; an unknown addon runs as root
	fp.out = priv.Result{Stdout: []byte("Location: /addons/other/x.cgi\r\n\r\n")}
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "/addons/other/run.ccc", nil))
	if w.Code != 404 {
		t.Errorf("missing file: %d", w.Code)
	}
	_ = os.WriteFile(r.join("/usr/local/etc/config/addons/www/mosq/run.ccc"), []byte("x"), 0o755)
	c2 := CGIRunner{Root: r}
	w = httptest.NewRecorder()
	c2.ServeHTTP(w, httptest.NewRequest("GET", "/addons/mosq/run.ccc", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/addons/other/x.cgi" || fp.last.cred.UID != 0 || fp.last.args != nil {
		t.Errorf("%d %v %+v", w.Code, w.Header(), fp.last)
	}
	// traversal and static files are not ours
	for _, p := range []string{"/addons/../etc/passwd.cgi", "/addons/mosq/style.css", "/addons/mosq/../mosq/index.cgi"} {
		w = httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	// X-Sendfile: the named file under /usr/local/tmp is delivered - and it is opened through
	// the privilege boundary, never with the daemon's own credentials (B-35). The file the fake
	// helper hands back stands in for the archive a confined addon wrote 0600 as its own user:
	// occulited could not have opened it itself.
	archive := filepath.Join(t.TempDir(), "backup.sbk")
	if err := os.WriteFile(archive, []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	const named = "/usr/local/tmp/redmatic-backup.tar.gz"
	fp.openAs, fp.opened = archive, nil
	fp.out = priv.Result{Stdout: []byte("Content-Type: application/x-tar\r\nX-Sendfile: " + named + "\r\n\r\n")}
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "/addons/mosq/index.cgi", nil))
	if w.Code != 200 || w.Body.String() != "archive bytes" || w.Header().Get("Content-Type") != "application/x-tar" {
		t.Errorf("sendfile: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if len(fp.opened) != 1 || fp.opened[0] != named {
		t.Errorf("the file was not opened through the privilege helper: %v", fp.opened)
	}
	// B-36: the answer advertises Accept-Ranges, so the caller's Range must reach
	// http.ServeContent - a resumed download of a large addon backup depends on it
	w = httptest.NewRecorder()
	rreq := httptest.NewRequest("GET", "/addons/mosq/index.cgi", nil)
	rreq.Header.Set("Range", "bytes=0-6")
	c.ServeHTTP(w, rreq)
	if w.Code != http.StatusPartialContent || w.Body.String() != "archive" {
		t.Errorf("sendfile range: %d %q", w.Code, w.Body.String())
	}
	// the helper refusing is a 404 that names nothing: the path is the addon's and the reason
	// is what the caller may not know (B-21)
	fp.openErr = errors.New("refused by the privilege helper: open " + named)
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "/addons/mosq/index.cgi", nil))
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "/usr/local/tmp") {
		t.Errorf("refused sendfile: %d %q", w.Code, w.Body.String())
	}
	fp.openErr, fp.openAs, fp.opened = nil, "", nil
	// a path outside the docroot never reaches the helper at all
	fp.out = priv.Result{Stdout: []byte("X-Sendfile: /etc/passwd\r\n\r\n")}
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "/addons/mosq/index.cgi", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("sendfile outside docroot: %d", w.Code)
	}
	if len(fp.opened) != 0 {
		t.Errorf("a path outside the docroot was sent to the helper: %v", fp.opened)
	}
	// no header block: everything is the body
	fp.out = priv.Result{Stdout: []byte("<html>plain</html>")}
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "/addons/mosq/index.cgi", nil))
	if w.Code != 200 || w.Body.String() != "<html>plain</html>" {
		t.Errorf("%d %q", w.Code, w.Body.String())
	}
}
