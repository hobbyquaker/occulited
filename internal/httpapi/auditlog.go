package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// The caller of a system change in the journal (task 269, the threat model's repudiation row,
// after B-231's login lines): the middleware puts who and from where into the request's context,
// the handlers' Info lines of a change log through reqLog/withCaller and so carry `user` and
// `remote`, and a change whose handler wrote no such line gets one line from the middleware
// itself - `api: change` with the method, the route and the status - so every change of the
// system names its caller, also where the line that says what changed comes from deeper down
// (internal/system writes the network, the firewall, the units without knowing the request).
//
// What is not a change of the system stays out of Info: the RPC proxies (lite-rpc's /json and
// /xmlrpc: setValue and every other interface call, at the rate of the frontends and
// automations), the metadata (names and taxonomy, the frontends' own data), the status LED's
// live state (set by automations), previews and the browser's own reports, and a request refused
// as bad, conflicting or not found (4xx: nothing changed; it is Debug). A 403 - an attempt
// without the right, from the handler or from the scope check - is Info: "api: change refused".

type callerKey struct{}

// reqCaller is who made the request and from where, and whether a handler already wrote an Info
// line with them.
type reqCaller struct {
	user, remote string
	logged       atomic.Bool
}

func callerFrom(r *http.Request) *reqCaller {
	c, _ := r.Context().Value(callerKey{}).(*reqCaller)
	return c
}

// withCaller is l with the request's caller: `user` and `remote`. A request without one (a
// test that calls a handler directly, a route outside the middleware) gets the session's user
// and the address it came from, unmarked.
func withCaller(r *http.Request, l *slog.Logger) *slog.Logger {
	if l == nil {
		l = slog.Default()
	}
	c := callerFrom(r)
	if c == nil {
		return l.With("user", who(r), "remote", remote(r))
	}
	return slog.New(callerMark{Handler: l.Handler(), c: c}).With("user", c.user, "remote", c.remote)
}

// reqLog is the default logger with the request's caller: the Info line of a system change.
func reqLog(r *http.Request) *slog.Logger { return withCaller(r, nil) }

// callerMark notes that a handler wrote an Info line with the caller, so the middleware does not
// add its own.
type callerMark struct {
	slog.Handler
	c *reqCaller
}

func (h callerMark) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelInfo {
		h.c.logged.Store(true)
	}
	return h.Handler.Handle(ctx, rec)
}

func (h callerMark) WithAttrs(as []slog.Attr) slog.Handler {
	return callerMark{Handler: h.Handler.WithAttrs(as), c: h.c}
}

func (h callerMark) WithGroup(name string) slog.Handler {
	return callerMark{Handler: h.Handler.WithGroup(name), c: h.c}
}

// quietScopes are the routes whose calls are not changes of the system (see above).
var quietScopes = map[auth.Scope]bool{
	auth.ScopeMetaWrite:    true,
	auth.ScopeRPCRead:      true,
	auth.ScopeRPCOperate:   true,
	auth.ScopeRPCConfigure: true,
	auth.ScopeRPCAdmin:     true,
	auth.ScopeSystemRead:   true, // the previews and searches that are POSTs
	auth.ScopeLED:          true, // the LED's live state: overrides and locate
}

// quietRoutes are single routes of a changing scope that change nothing lasting: the LED page's
// preview, the browser's boot timings, the own UI preferences, the session hand-overs.
var quietRoutes = map[string]bool{
	"POST /api/system/v1/led/preview":           true,
	"DELETE /api/system/v1/led/preview":         true,
	"POST /api/system/v1/boot-timing":           true,
	"PUT /api/auth/v1/me/preferences":           true,
	"PUT /api/auth/v1/me/display-names/{id}":    true, // task 73: the user's own names in Control
	"DELETE /api/auth/v1/me/display-names/{id}": true,
	"POST /api/auth/v1/me/webauthn/begin":       true, // task 262: a challenge, not yet a key
	"POST /api/auth/v1/ticket":                  true,
	"POST /api/auth/v1/legacy-sid":              true,
}

// audited reports whether a request to the route pattern with these scopes changes the system.
func audited(method, pattern string, scopes []auth.Scope) bool {
	if safeMethod(method) || pattern == "" || len(scopes) == 0 || quietScopes[scopes[0]] || quietRoutes[pattern] {
		return false
	}
	return true
}

// serveAudited serves a change and, unless the handler logged it with its caller, writes the
// middleware's line.
func (a *AuthAPI) serveAudited(w http.ResponseWriter, r *http.Request, h http.Handler, c *reqCaller, pattern string) {
	rec := &auditRecorder{ResponseWriter: w, status: http.StatusOK}
	h.ServeHTTP(rec, r)
	if c.logged.Load() {
		return
	}
	msg, level := "api: change", slog.LevelInfo
	switch {
	case rec.status == http.StatusForbidden:
		msg = "api: change refused" // an attempt without the right: Info (the maintainer, 2026-09-26)
	case rec.status >= 400 && rec.status < 500:
		level = slog.LevelDebug // a bad request, a conflict, not found: nothing changed
	}
	a.logChange(r, level, msg, pattern, rec.status, c)
}

// logChange writes the middleware's line of a change or of a refused one.
func (a *AuthAPI) logChange(r *http.Request, level slog.Level, msg, pattern string, status int, c *reqCaller) {
	_, path, _ := strings.Cut(pattern, " ")
	a.logger().Log(r.Context(), level, msg, "method", r.Method, "route", path, "status", status, "user", c.user, "remote", c.remote)
}

// refusedScope is the line of a change the middleware refused because the session lacks the
// route's scope: the handler never ran, so it is written here.
func (a *AuthAPI) refusedScope(r *http.Request, sess *auth.Session, pattern string, scopes []auth.Scope) {
	if !audited(r.Method, pattern, scopes) {
		return
	}
	a.logChange(r, slog.LevelInfo, "api: change refused", pattern, http.StatusForbidden, &reqCaller{user: sess.User, remote: remote(r)})
}

// auditRecorder keeps the status of the answer. Unwrap and Flush keep the server's writer
// reachable through it (http.ResponseController, streaming answers).
type auditRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *auditRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *auditRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

func (s *auditRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *auditRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
