package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/oidc"
	"github.com/hobbyquaker/occulited/internal/trust"

	"github.com/hobbyquaker/occulited/internal/acme"
	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/clientaddr"
	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/pairing"
)

// The session cookie has one name per scheme. A browser keeps a Secure cookie away from plain
// HTTP and refuses to let an HTTP response replace it, so a single name set Secure by an HTTPS
// login left HTTP without any cookie, and addon frames - which have nothing else - without a
// session. With two names neither login blocks the other; both are read everywhere. The same id
// also works as a Bearer credential and as `?sid=`, with or without the CCU's `@` wrapping.
//
// Two cookies per scheme since openccu-lite task 259 (D-78's measure "the API cookie kept away
// from /addons/"): the session cookie is scoped to Path=/api, so no request to an addon's page,
// CGI or proxy carries it, and a second cookie of the same id, the gate cookie, is scoped to
// Path=/addons/ and opens the addon pages alone - lighttpd's gate and RequireSession read it, the
// API never does. An addon's server side still learns the session id from X-Occulite-Session,
// which the gate sets for it (D-65): the measure keeps the credential out of what an addon
// forwards or logs as a cookie, not out of the addon's hands - the threat model's R1 says so.
//
// The header credential (the second measure): a state-changing API call whose only credential is
// a cookie must also carry X-Occulite-Request (any value). A custom header cannot ride on a
// cross-site form post or a link, so it closes the top-level POST that SameSite=Lax lets through;
// the shell sends it on every call. A Bearer, a token or ?sid= is not ambient and needs no header.
//
// The session's legacy alias - the CCU's ten-character `?sid=@xxxxxxxxxx@` that the addon CGIs
// parse (task 125, D-77) - is not a credential here: the API refuses it as a cookie, as Bearer and
// as `?sid=`, from anywhere. It is accepted by lighttpd's gate and the tclrega shim for /addons/,
// and by RequireSession, which guards the addon CGIs by the gate's rule.
const (
	// CookieName is the session cookie set by a login over HTTP.
	CookieName = "occulite_session"
	// SecureCookieName is the session cookie set (Secure) by a login over HTTPS.
	SecureCookieName = "__Secure-occulite_session"
	// GateCookieName is the addon-page cookie set by a login over HTTP (task 259).
	GateCookieName = "occulite_gate"
	// SecureGateCookieName is the addon-page cookie set (Secure) by a login over HTTPS.
	SecureGateCookieName = "__Secure-occulite_gate"
	// CookiePath is the session cookie's path: the API and nothing else.
	CookiePath = "/api"
	// GateCookiePath is the gate cookie's path: the addon pages and nothing else.
	GateCookiePath = "/addons/"
	// RequestHeader is the header a state-changing API call on the cookie alone must carry.
	RequestHeader = "X-Occulite-Request"
)

// gateCookieName is the gate cookie of the session cookie called name.
func gateCookieName(name string) string {
	if name == SecureCookieName {
		return SecureGateCookieName
	}
	return GateCookieName
}

// bareSID returns the session id a credential value carries - the value, with the CCU's
// `@` wrapping removed - when it has a session id's shape.
func bareSID(v string) (string, bool) {
	if len(v) >= 2 && v[0] == '@' && v[len(v)-1] == '@' {
		v = v[1 : len(v)-1]
	}
	return v, auth.IsSessionID(v)
}

// bareAlias is bareSID for the legacy alias's shape.
func bareAlias(v string) (string, bool) {
	if len(v) >= 2 && v[0] == '@' && v[len(v)-1] == '@' {
		v = v[1 : len(v)-1]
	}
	return v, auth.IsLegacyID(v)
}

type ctxKey struct{}

// SessionFrom returns the session of a request, or nil.
func SessionFrom(r *http.Request) *auth.Session {
	s, _ := r.Context().Value(ctxKey{}).(*auth.Session)
	return s
}

// AuthAPI serves /api/auth/v1 and provides the middleware.
type AuthAPI struct {
	Store *auth.Store
	// Secure marks the cookie Secure; behind lighttpd that is decided by X-Forwarded-Proto.
	Secure bool
	// OIDC is the optional external login; nil = local accounts only.
	OIDC *oidc.Client
	// OIDCName is the button label on the login page.
	OIDCName string
	// Trust is the store of extra trust anchors (task 230): the ones with the purpose "oidc" are
	// the provider client's besides the system's pool; nil = the trust routes answer 501.
	Trust *trust.Store
	// Off is auth mode "off" (task 29): every caller is the anonymous administrator.
	Off bool
	// ConfigFile is occulited.json, for GET/PUT /config; "" = the route answers 501.
	ConfigFile string
	// Log is the auth area's logger (task 101); nil = slog's default.
	Log *slog.Logger
	// Pairing is client pairing (task 219); CertFingerprint the SHA-256 of the certificate the
	// system serves, which a pairing code is bound to over HTTPS
	Pairing         *pairing.Manager
	CertFingerprint func() []byte
	// PasswordLoginOff is the switch auth.oidc.password_login (task 19, D-53) as the daemon
	// started with; passwordLoginOn keeps it in step with ConfigFile. Without a provider it means
	// nothing: the password is the only login then.
	PasswordLoginOff bool
	// FQDN answers the system's full name, <host>.<domain> (task 262: the name security keys are
	// made on); nil or "" when it is not known.
	FQDN func() string
	// The addon ingress scope addon:<id> (openccu-lite task 307): Addons answers the installed
	// addons, id → display name - what a token may be given the scope for and what GET /tokens
	// lists; AddonForSegment answers the addon behind /addons/<segment>/, for RequireSession's
	// token check. nil: no ingress scope can be given, and a token opens no addon page.
	Addons          func() map[string]string
	AddonForSegment func(segment string) string

	swMu   sync.Mutex
	swInfo os.FileInfo // ConfigFile as the switch was last read from it
	// occulited task 70: the login page's options, read from ConfigFile like the switch
	optMu   sync.Mutex
	optInfo os.FileInfo
	optCfg  config.AuthConfig

	// the Control app's public mode (task 193): in memory as the daemon started with it or the
	// Remote access page last set it; ConfigFile keeps it across restarts
	pubMu      sync.Mutex
	pubOn      bool
	pubAccount string
	// OnPublic is told when SetPublic switched the mode (occulited task 125); nil = nobody.
	OnPublic func(on bool)

	refused refusedLimit // the flood limit of the refused-login lines (B-231)
}

// PublicView is the public mode as it stands: whether it is on, and the account it uses.
func (a *AuthAPI) PublicView() (on bool, account string) {
	a.pubMu.Lock()
	defer a.pubMu.Unlock()
	return a.pubOn, a.pubAccount
}

// InitPublic takes the mode from the configuration at start.
func (a *AuthAPI) InitPublic(c config.PublicConfig) {
	a.pubMu.Lock()
	a.pubOn, a.pubAccount = c.Enabled, c.PublicAccount()
	a.pubMu.Unlock()
}

// SetPublic switches the mode (the Remote access page) and writes it to ConfigFile. The account
// is a name of the ladder's shape; an existing account above operate is refused - public means a
// restricted principal, never an administrator without a login.
func (a *AuthAPI) SetPublic(on bool, account string) error {
	if account == "" {
		account = "guest"
	}
	if !auth.ValidName(account) {
		return errors.New("the public account's name has 1 to 32 letters, digits, dots, underscores or dashes")
	}
	for _, u := range a.Store.Users() {
		if u.Name == account && u.Level != auth.LevelRead && u.Level != auth.LevelOperate {
			return fmt.Errorf("the account %s may %s: the public account must be one that reads or operates", account, u.Level)
		}
	}
	if a.ConfigFile != "" {
		if _, err := config.SetPublic(a.ConfigFile, on, account); err != nil {
			return err
		}
	}
	a.pubMu.Lock()
	a.pubOn, a.pubAccount = on, account
	a.pubMu.Unlock()
	if a.OnPublic != nil {
		a.OnPublic(on) // occulited task 125: the gate's mirror of the addons' public views
	}
	if on {
		a.logger().Warn("auth: the Control app is public", "account", account)
	} else {
		a.logger().Info("auth: the Control app is behind the login again")
	}
	return nil
}

// publicPath is the list the public mode opens (task 193: a list, not a hole): the metadata's
// reads, lite-rpc, the service messages and the shell's preferences of the account - what the
// Control app needs and nothing of the system's administration. The scope table then holds the
// principal to its level, so a write to the metadata is 403 even here.
func publicPath(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/rpc/v1/"):
		return true
	case strings.HasPrefix(p, "/api/meta/v1/"):
		return r.Method == http.MethodGet
	case p == "/api/system/v1/service-messages", p == "/api/system/v1/service-messages/stream", p == "/api/auth/v1/me/preferences", p == "/api/auth/v1/me/display-names",
		p == appViewsPath: // occulited task 125: the addons' views are in the public App too
		return r.Method == http.MethodGet
	case p == "/api/system/v1/stream":
		// occulited B-53: the shell's stream, with the service messages as its only topic
		return r.Method == http.MethodGet && publicShellStream(r)
	}
	return false
}

// publicSession is the public principal for a request without a session on a public path, nil
// otherwise.
func (a *AuthAPI) publicSession(r *http.Request) *auth.Session {
	a.pubMu.Lock()
	on, account := a.pubOn, a.pubAccount
	a.pubMu.Unlock()
	if !on || !publicPath(r) {
		return nil
	}
	return a.Store.Public(account)
}

// passwordLoginOn reports whether the password form and POST /login are offered. With a provider
// running, the switch is re-read from the configuration file whenever that changed (another
// inode, size or modification time - every write renames a new file into place): the Security
// page writes it, and so does the console's `occulited auth password-login on`, which is the way
// back in when the provider is down and must not wait for a restart. A file that cannot be read
// leaves the switch as it was.
func (a *AuthAPI) passwordLoginOn() bool {
	if a.OIDC == nil {
		return true
	}
	a.swMu.Lock()
	defer a.swMu.Unlock()
	if a.ConfigFile == "" {
		return !a.PasswordLoginOff
	}
	st, err := os.Stat(a.ConfigFile)
	if err != nil {
		return !a.PasswordLoginOff
	}
	if a.swInfo == nil || !os.SameFile(a.swInfo, st) || st.Size() != a.swInfo.Size() || !st.ModTime().Equal(a.swInfo.ModTime()) {
		cfg, err := config.Load(a.ConfigFile)
		if err != nil {
			a.logger().Warn("auth: cannot read the configuration file; the password-login switch stays as it was", "path", a.ConfigFile, "err", err)
			return !a.PasswordLoginOff
		}
		if off := !cfg.Auth.PasswordLoginOn(); off != a.PasswordLoginOff && a.swInfo != nil {
			a.logger().Info("auth: the password-login switch changed in the configuration file", "password_login", !off)
		}
		a.swInfo, a.PasswordLoginOff = st, !cfg.Auth.PasswordLoginOn()
	}
	return !a.PasswordLoginOff
}

// loginOptions is the auth section of ConfigFile for the login page's options (occulited task 70:
// the hidden password form, how a passkey is offered), re-read whenever the file changed, as the
// password-login switch is; without a file (or one that cannot be read) the defaults or what was
// read last.
func (a *AuthAPI) loginOptions() config.AuthConfig {
	a.optMu.Lock()
	defer a.optMu.Unlock()
	if a.ConfigFile == "" {
		return a.optCfg
	}
	st, err := os.Stat(a.ConfigFile)
	if err != nil {
		return a.optCfg
	}
	if a.optInfo == nil || !os.SameFile(a.optInfo, st) || st.Size() != a.optInfo.Size() || !st.ModTime().Equal(a.optInfo.ModTime()) {
		cfg, err := config.Load(a.ConfigFile)
		if err != nil {
			return a.optCfg
		}
		a.optInfo, a.optCfg = st, cfg.Auth
	}
	return a.optCfg
}

// providerLoginSeen: an account has signed in through the identity provider at least once - the
// password form may only be hidden once the provider has shown it works (occulited task 70).
func (a *AuthAPI) providerLoginSeen() bool {
	for _, u := range a.Store.Users() {
		if u.LastProviderLogin != nil {
			return true
		}
	}
	return false
}

// passwordLoginDisabled is the answer of every password route while the switch is off.
func passwordLoginDisabled(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, apiError{Error: "password_login_disabled", Message: "password login is switched off on this system: sign in through the identity provider"})
}

func (a *AuthAPI) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// Register mounts the routes.
func (a *AuthAPI) Register(mux *http.ServeMux) {
	p := "/api/auth/v1"
	route(mux, scopeOpen, "GET "+p+"/state", a.state)
	route(mux, scopeOpen, "POST "+p+"/setup", a.setup)
	route(mux, scopeOpen, "POST "+p+"/login", a.login)
	route(mux, auth.ScopeSelf, "POST "+p+"/logout", a.logout)
	route(mux, auth.ScopeSelf, "POST "+p+"/password", a.password)
	route(mux, auth.ScopeSelf, "GET "+p+"/sessions", a.sessions)
	route(mux, auth.ScopeSelf, "DELETE "+p+"/sessions", a.logoutEverywhere)
	route(mux, auth.ScopeSelf, "DELETE "+p+"/sessions/{sid}", a.deleteSession)
	route(mux, auth.ScopeAuthAdmin, "GET "+p+"/users", a.users)
	route(mux, auth.ScopeAuthAdmin, "POST "+p+"/users", a.createUser)
	route(mux, auth.ScopeAuthAdmin, "DELETE "+p+"/users/{name}", a.deleteUser)
	route(mux, auth.ScopeAuthAdmin, "PATCH "+p+"/users/{name}", a.patchUser)
	route(mux, auth.ScopeSelf, "GET "+p+"/me/preferences", a.preferences) // preferences.go
	route(mux, auth.ScopeSelf, "PUT "+p+"/me/preferences", a.putPreferences)
	route(mux, auth.ScopeSelf, "GET "+p+"/me/display-names", a.displayNames) // displaynames.go, task 73
	route(mux, auth.ScopeSelf, "PUT "+p+"/me/display-names/{id}", a.putDisplayName)
	route(mux, auth.ScopeSelf, "DELETE "+p+"/me/display-names/{id}", a.deleteDisplayName)
	route(mux, scopeOpen, "GET "+p+"/oidc", a.oidcInfo)
	route(mux, scopeOpen, "GET "+p+"/oidc/start", a.oidcStart)
	route(mux, scopeOpen, "GET "+p+"/oidc/callback", a.oidcCallback)
	route(mux, auth.ScopeAuthAdmin, "GET "+p+"/config", a.authConfig)
	route(mux, auth.ScopeAuthAdmin, "PUT "+p+"/config", a.authConfigPut)
	route(mux, auth.ScopeAuthAdmin, "GET "+p+"/tokens", a.tokens)
	route(mux, auth.ScopeAuthAdmin, "POST "+p+"/tokens", a.createToken)
	route(mux, auth.ScopeAuthAdmin, "DELETE "+p+"/tokens/{name}", a.deleteToken)
	route(mux, auth.ScopeSelf, "POST "+p+"/legacy-sid", a.legacySID)
	route(mux, auth.ScopeSelf, "POST "+p+"/ticket", a.ticket)
	route(mux, auth.ScopeSelf, "GET "+p+"/confirm", a.confirmMethod)
	route(mux, auth.ScopeSelf, "GET "+p+"/oidc/confirm", a.oidcConfirm)
	route(mux, scopeOpen, "POST "+p+"/ticket/redeem", a.ticketRedeem)
	a.registerOIDCTrust(mux, p) // oidctrust.go
	a.registerPairing(mux, p)   // task 219
	a.registerWebAuthn(mux, p)  // task 262: security keys and passkeys (webauthn.go)
}

// cookieSIDs returns the well-formed session ids of the cookies called name, in header order (a
// browser may send one name twice, for different paths).
func cookieSIDs(r *http.Request, name string) []string {
	var out []string
	for _, c := range r.CookiesNamed(name) {
		if id, ok := bareSID(c.Value); ok {
			out = append(out, id)
		}
	}
	return out
}

// gateCookieSIDs returns the well-formed session ids of the gate cookies (task 259), the HTTPS
// one first. Read by RequireSession for the addon pages alone; the API never takes them.
func gateCookieSIDs(r *http.Request) []string {
	return append(cookieSIDs(r, SecureGateCookieName), cookieSIDs(r, GateCookieName)...)
}

// sessionIDs lists every credential a request carries, most specific first: the HTTPS cookie,
// the HTTP cookie (a browser sends that one over HTTPS too), the Authorization header, ?sid=.
// Each is a whole credential on its own, so a stale one must not hide a live one behind it. A
// value of the legacy alias's shape is not a credential and is left out (D-77), and so is a gate
// cookie (task 259): it opens addon pages, never the API.
func sessionIDs(r *http.Request) []string {
	ids := append(cookieSIDs(r, SecureCookieName), cookieSIDs(r, CookieName)...)
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		v := strings.TrimSpace(h[7:])
		if auth.IsToken(v) {
			ids = append(ids, v)
		} else if id, ok := bareSID(v); ok {
			ids = append(ids, id)
		}
	}
	// a token as the basic-auth password (task 77: XML-RPC libraries speak basic auth, not
	// Bearer); the user name is ignored
	if _, pw, ok := r.BasicAuth(); ok && auth.IsToken(pw) {
		ids = append(ids, pw)
	}
	if v := r.URL.Query().Get("sid"); auth.IsToken(v) {
		ids = append(ids, v)
	} else if id, ok := bareSID(v); ok {
		ids = append(ids, id)
	}
	return ids
}

// open lists the paths that need no session: feature detection, health, the auth flow itself,
// and the HTTP-01 challenge (task 35) - a CA fetches its token there, and the handler answers
// only tokens of an order in flight.
func open(path string) bool {
	if strings.HasPrefix(path, acme.ChallengePrefix) {
		return true
	}
	// occulited task 119: an addon's notices - its control token is the credential, checked by the handler
	if path == addonNoticesPrefix || strings.HasPrefix(path, addonNoticesPrefix+"/") {
		return true
	}
	// occulited task 125: an addon's views in the Control app - its control token, checked by the handler
	if path == addonViewsPath {
		return true
	}
	// task 219: a program's pairing request and its polls - the poll secret is the credential
	if path == "/api/auth/v1/pairing/request" {
		return true
	}
	if id, ok := strings.CutPrefix(path, "/api/auth/v1/pairing/request/"); ok && id != "" && !strings.Contains(id, "/") {
		return true
	}
	if webauthnOpen[path] { // task 262: the passkey login and the feature
		return true
	}
	switch path {
	case "/api/meta/v1/version", "/api/system/v1/health", "/api/auth/v1/state", "/api/auth/v1/login", "/api/auth/v1/setup",
		"/api/auth/v1/oidc", "/api/auth/v1/oidc/start", "/api/auth/v1/oidc/callback",
		"/api/auth/v1/ticket/redeem",          // the ticket is the credential
		"/api/system/v1/addonctl",             // 28.8: its own token, checked by the handler
		"/api/system/v1/addons/install/local", // openccu-lite B-274: the install token, checked by the handler
		"/api/homematic.cgi",                  // task 180: hmipserver's session check, answered for the loopback only
		"/api/system/v1/sbom":                 // task 179: the Licences page, also from the login screen
		return true
	}
	return false
}

// Middleware enforces sessions and scopes on /api/* (task 66): a request needs a session, and
// the session needs the scope of the route that serves it (scopes.go's table, looked up on the
// mux the routes were registered on). A route the table does not know is not reached: the
// default is deny. Everything outside /api (the UI, addons) is passed through; the UI decides
// what to show.
func (a *AuthAPI) Middleware(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || open(r.URL.Path) {
			mux.ServeHTTP(w, r)
			return
		}
		sess := a.session(r)
		if sess == nil {
			if presentsToken(r) {
				a.logRefused(r, "", "token", "unknown, expired or not allowed from this address")
			}
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "login required"})
			return
		}
		scopes, pattern, known := scopesOf(mux, r)
		if pattern != "" && !known {
			a.logger().Error("auth: a route without a scope in the table was refused", "pattern", pattern)
			writeJSON(w, http.StatusForbidden, apiError{Error: "forbidden", Message: "this route has no scope; nobody reaches it"})
			return
		}
		if known && !allowed(sess, scopes) {
			a.refusedScope(r, sess, pattern, scopes)
			forbiddenScope(w, scopes[0])
			return
		}
		// task 213 (F-2): a state-changing call with the cookie alone must come from this system
		if !safeMethod(r.Method) && a.ambient(r, sess) {
			if v := crossSite(r); v.refuse != "" || v.navigate {
				a.logger().Warn("auth: a cross-site request refused", "method", r.Method, "path", r.URL.Path, "user", sess.User, "why", v.refuse)
				refuseCrossSite(w)
				return
			}
			// task 259 (D-78): and it must carry the header credential - a custom header no
			// form post or link from elsewhere can send. A request with an Authorization header
			// has shown as much already.
			if a.cookieAmbient(r, sess) && r.Header.Get("Authorization") == "" && r.Header.Get(RequestHeader) == "" {
				a.logger().Warn("auth: a state-changing request on the cookie alone refused: no "+RequestHeader+" header", "method", r.Method, "path", r.URL.Path, "user", sess.User)
				writeJSON(w, http.StatusForbidden, apiError{Error: "request-header", Message: "a state-changing call with the session cookie alone must carry the header " + RequestHeader})
				return
			}
		}
		// task 269: who and from where, for the Info lines of a change (auditlog.go)
		c := &reqCaller{user: sess.User, remote: remote(r)}
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), ctxKey{}, sess), callerKey{}, c))
		if known && audited(r.Method, pattern, scopes) {
			a.serveAudited(w, r, mux, c, pattern)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// RequireSession guards a non-API handler (the addon CGIs): any live account session, whatever
// its role - the same rule as lighttpd's gate, which also takes the session's legacy alias from
// ?sid= there (task 125: the CCU convention the CGIs live by; not from a cookie, and nowhere on
// the API) - and a token with system:read (the daemon's own update check goes through here,
// B-2) or with the ingress scope of the addon behind the path (openccu-lite task 307: the gate
// let the Bearer token through for /addons/<segment>/, and the CGI or static file is served here).
// Browsers get the login page, API callers 401.
func (a *AuthAPI) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := a.session(r)
		if sess == nil && !a.Off {
			// task 259: the gate cookie is what a browser carries here - the session cookie
			// stays at /api
			for _, id := range gateCookieSIDs(r) {
				if sess = a.Store.ValidateFrom(id, remote(r)); sess != nil {
					break
				}
			}
		}
		if sess == nil && !a.Off {
			if alias, ok := bareAlias(r.URL.Query().Get("sid")); ok {
				sess = a.Store.ValidateLegacy(alias)
			}
		}
		if sess == nil {
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				http.Redirect(w, r, "/login?return="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "login required"})
			return
		}
		if sess.IsToken() && !sess.Has(auth.ScopeSystemRead) {
			need := auth.ScopeSystemRead
			if id := a.addonOfPath(r.URL.Path); id != "" {
				need = auth.AddonScope(id)
			}
			if !sess.Has(need) {
				forbiddenScope(w, need)
				return
			}
		}
		// task 213: the gate's rule for the addon pages - refused from elsewhere, and a link
		// from another site opens the page without its query (a state change cannot ride on it)
		if a.ambient(r, sess) {
			v := crossSite(r)
			if v.refuse != "" {
				a.logger().Warn("auth: a cross-site request to an addon refused", "method", r.Method, "path", r.URL.Path, "user", sess.User, "why", v.refuse)
				refuseCrossSite(w)
				return
			}
			if v.navigate && r.URL.RawQuery != "" {
				http.Redirect(w, r, (&url.URL{Path: r.URL.Path}).EscapedPath(), http.StatusFound)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

// addonOfPath is the addon whose pages a request under /addons/<segment>/ asks for, "" when the
// path is elsewhere, the segment is nobody's or the system has no addons to ask.
func (a *AuthAPI) addonOfPath(path string) string {
	rest, ok := strings.CutPrefix(path, "/addons/")
	if !ok || a.AddonForSegment == nil {
		return ""
	}
	seg, _, _ := strings.Cut(rest, "/")
	if seg == "" {
		return ""
	}
	return a.AddonForSegment(seg)
}

// checkAddonScopes refuses an ingress scope of an addon that is not installed (task 307): the
// token page and the console offer the installed ones, and a program asking for another gets the
// name of the missing addon.
func (a *AuthAPI) checkAddonScopes(scopes auth.Scopes) error {
	var names map[string]string
	for _, s := range scopes {
		id, ok := auth.AddonOf(s)
		if !ok {
			continue
		}
		if names == nil {
			if a.Addons != nil {
				names = a.Addons()
			} else {
				names = map[string]string{}
			}
		}
		if _, installed := names[id]; !installed {
			return fmt.Errorf("%w: no addon %q is installed", auth.ErrBadScope, id)
		}
	}
	return nil
}

// addonScopeList is GET /tokens' addons: the installed addons' ingress scopes with their names,
// sorted by id, for the token page's grid.
func (a *AuthAPI) addonScopeList() []map[string]string {
	out := []map[string]string{}
	if a.Addons == nil {
		return out
	}
	names := a.Addons()
	ids := make([]string, 0, len(names))
	for id := range names {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := auth.AddonOf(auth.AddonScope(id)); ok {
			out = append(out, map[string]string{"id": id, "name": names[id], "scope": string(auth.AddonScope(id))})
		}
	}
	return out
}

// remote is the address the request came from: lighttpd's element of X-Forwarded-For on a
// connection from the loopback, never a client-sent one (B-230; package clientaddr).
func remote(r *http.Request) string {
	return clientaddr.Of(r)
}

// secure reports whether the browser reached the box over HTTPS; behind lighttpd that is
// X-Forwarded-Proto.
func (a *AuthAPI) secure(r *http.Request) bool {
	return a.Secure || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// cookieName is the session cookie of the scheme the request came in over.
func (a *AuthAPI) cookieName(r *http.Request) string {
	if a.secure(r) {
		return SecureCookieName
	}
	return CookieName
}

// cookieMaxAge is the cookies' lifetime: the sessions' absolute lifetime (task 262), so that a
// browser drops the cookie when the session cannot be alive any more; the session's own limits
// end it earlier.
func (a *AuthAPI) cookieMaxAge() int {
	_, maxAge := a.Store.SessionLimits()
	return int(maxAge.Seconds())
}

// setCookie sets the cookies of the request's scheme only; the other scheme's are left alone, so
// a login over one scheme never ends the other's. Two go out (task 259): the session cookie for
// /api and the gate cookie for /addons/, the same id. A cookie of the shape before task 259 -
// the session's name at Path=/ - is deleted in the same answer: a browser keeps cookies by name
// and path, so the old one would otherwise ride along to the addon pages until it ran out. The
// deletion costs nothing when there is none, which lets GET /state call this on every load.
func (a *AuthAPI) setCookie(w http.ResponseWriter, r *http.Request, sid string) {
	secure := a.secure(r)
	name := a.cookieName(r)
	deleteCookie(w, name, "/") // first, so that the last header of this name is the one that sets it
	maxAge := a.cookieMaxAge()
	http.SetCookie(w, &http.Cookie{Name: name, Value: sid, Path: CookiePath, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
	http.SetCookie(w, &http.Cookie{Name: gateCookieName(name), Value: sid, Path: GateCookiePath, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

// deleteCookie sends the deletion of one cookie at one path. The HTTPS ones are only accepted
// with Secure; over plain HTTP a browser ignores their deletion, which cannot be helped - that
// page cannot see them either.
func deleteCookie(w http.ResponseWriter, name, path string) {
	secure := name == SecureCookieName || name == SecureGateCookieName
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// clearCookie deletes the session cookie called name wherever a browser may hold it - at /api,
// at the pre-259 Path=/ - and its gate cookie at /addons/.
func clearCookie(w http.ResponseWriter, name string) {
	deleteCookie(w, name, CookiePath)
	deleteCookie(w, name, "/")
	deleteCookie(w, gateCookieName(name), GateCookiePath)
}

// clearStale deletes session cookies the request carries that name no live session, except the
// one called keep (about to be set). What it is for: a browser that logged in over HTTPS before
// the two names existed still holds occulite_session marked Secure, and as long as it does, an
// HTTP login cannot set its own. The sessions of that time ended with the daemon's restart, so
// the cookie is stale, and an HTTPS response may delete it. Over HTTP only the HTTP cookie is
// touched.
func (a *AuthAPI) clearStale(w http.ResponseWriter, r *http.Request, keep string) {
	for _, name := range []string{SecureCookieName, CookieName} {
		if name == keep || (name == SecureCookieName && !a.secure(r)) || len(r.CookiesNamed(name)) == 0 {
			continue
		}
		live := false
		for _, sid := range cookieSIDs(r, name) {
			if a.Store.Validate(sid) != nil {
				live = true
				break
			}
		}
		if !live {
			clearCookie(w, name)
		}
	}
}

func authErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid-credentials", Message: err.Error()})
	case errors.Is(err, auth.ErrLockedOut):
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "locked-out", Message: err.Error()})
	case errors.Is(err, auth.ErrSetupDone), errors.Is(err, auth.ErrDuplicateUser), errors.Is(err, auth.ErrLastAdmin):
		writeJSON(w, http.StatusConflict, apiError{Error: "conflict", Message: err.Error()})
	case errors.Is(err, auth.ErrSetupRequired):
		writeJSON(w, http.StatusPreconditionRequired, apiError{Error: "setup-required", Message: err.Error()})
	case errors.Is(err, auth.ErrUnknownUser):
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-user", Message: err.Error()})
	case errors.Is(err, auth.ErrNoPassword):
		writeJSON(w, http.StatusConflict, apiError{Error: "no-password", Message: err.Error()})
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrBadUsername), errors.Is(err, auth.ErrBadRole), errors.Is(err, auth.ErrWebAuthnName), errors.Is(err, auth.ErrSessionLimits):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-body", Message: err.Error()})
	// task 262: the passkeys
	case errors.Is(err, auth.ErrWebAuthnClone), errors.Is(err, auth.ErrPendingLogin):
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid-credentials", Message: err.Error()})
	case errors.Is(err, auth.ErrWebAuthnDuplicate), errors.Is(err, auth.ErrWebAuthnLimit):
		writeJSON(w, http.StatusConflict, apiError{Error: "conflict", Message: err.Error()})
	case errors.Is(err, auth.ErrWebAuthnUnknown):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-found", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
	}
}

// --- handlers ---

// session is the caller's session, or in auth-off mode the anonymous administrator. A request
// without one may carry a one-time ticket for its path (?ticket=, task 125: a download link that
// must not hold the session); redeeming it spends it.
// SessionOf is the request's session for another package's re-check (task 77's streams ask
// every heartbeat whether their credential still stands); nil when there is none.
func (a *AuthAPI) SessionOf(r *http.Request) *auth.Session { return a.session(r) }

func (a *AuthAPI) session(r *http.Request) *auth.Session {
	if a.Off {
		return a.Store.EnsureAnonymous()
	}
	for _, id := range sessionIDs(r) {
		if sess := a.Store.ValidateFrom(id, remote(r)); sess != nil {
			return sess
		}
	}
	if t := r.URL.Query().Get("ticket"); t != "" {
		return a.Store.RedeemTicket(t, r.URL.Path)
	}
	return a.publicSession(r)
}

func (a *AuthAPI) state(w http.ResponseWriter, r *http.Request) {
	if a.Off {
		// task 29: no login, everyone is the anonymous administrator; the cookie carries the
		// session id so lighttpd's gate (session files) works, and the fixed alias serves the
		// ?sid=@..@ convention
		// (task 259: set on every call - the gate cookie lives at /addons/, so this route cannot
		// see whether the browser has it; the id is fixed, so the answer is the same each time)
		sess := a.Store.EnsureAnonymous()
		a.setCookie(w, r, sess.ID)
		writeJSON(w, 200, map[string]any{"setup_required": false, "authenticated": true, "user": sess.User, "role": sess.Role, "level": sess.Level, "account_id": sess.AccountID, "must_change_password": false, "sid": sess.ID, "legacy_sid": sess.Legacy, "auth_off": true})
		return
	}
	out := map[string]any{"setup_required": a.Store.SetupRequired(), "authenticated": false}
	sess := a.session(r)
	// task 259: a session the cookie carried gets its cookies set again - the shell calls this
	// route on every load, so a browser from before the two cookies existed (one cookie at Path=/)
	// is moved to the new shape before it opens an addon frame, and the gate cookie, which no
	// request here can show, is there when the frame needs it
	keep := ""
	if sess != nil && a.cookieAmbient(r, sess) {
		keep = a.cookieName(r)
	}
	a.clearStale(w, r, keep)
	if keep != "" {
		a.setCookie(w, r, sess.ID)
	}
	if sess == nil {
		// task 193: the public mode's principal, so the shell shows the Control app without a login
		a.pubMu.Lock()
		on, account := a.pubOn, a.pubAccount
		a.pubMu.Unlock()
		if on {
			sess = a.Store.Public(account)
		}
	}
	if sess != nil {
		out["authenticated"] = true
		out["user"] = sess.User
		if sess.Method == auth.MethodPublic {
			out["public"] = true
		}
		if sess.Role != "" {
			out["role"] = sess.Role
			out["level"] = sess.Level
			out["account_id"] = sess.AccountID
		}
		out["scopes"] = sess.Scopes
		// the flag is a password's: a session the provider opened is not sent to change one
		out["must_change_password"] = sess.Method == auth.MethodPassword && a.Store.MustChangePassword(sess.User)
		if !sess.IsToken() && sess.ID != "" {
			out["sid"] = sess.ID // the caller's own session, the shell's Bearer for a host without the cookie
			out["method"] = sess.Method
		}
		if sess.Legacy != "" {
			out["legacy_sid"] = sess.Legacy // its alias, when this process made one (POST /legacy-sid)
		}
	}
	writeJSON(w, 200, out)
}

// legacySID is POST /legacy-sid (task 125): the caller's session gets its ?sid=@..@ alias - made
// now, or the one it has - for the shell to put into the URL of an addon that lives by the CCU
// convention. A token has no session and gets 409.
func (a *AuthAPI) legacySID(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	if sess == nil || sess.ID == "" || sess.IsToken() {
		writeJSON(w, http.StatusConflict, apiError{Error: "no-session", Message: "a session is needed; a token has no alias"})
		return
	}
	alias, err := a.Store.LegacyID(sess.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
		return
	}
	if alias == "" {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "login required"})
		return
	}
	writeJSON(w, 200, map[string]any{"legacy_sid": alias})
}

// ticket is POST /ticket {path} (task 125): a one-time credential for one GET on that path - a
// download the browser fetches as a plain navigation without any header - or, with the path
// "session", for a browser on a host without the cookie (the network change's confirm link),
// which exchanges it at POST /ticket/redeem for the session. Single use; 60 s for a download, 90 s
// for the hand-over (D-84: as long as the confirm window).
//
// With "confirm": true it is a confirmed ticket (task 154, D-104) for a route that asks again even
// in a valid session: it needs the account's password in the same body, and a wrong one counts as
// a failed login. An account that signs in at the provider only, or any account while password
// login is off, confirms there instead: the answer is 409 confirm-oidc with the URL to go to.
func (a *AuthAPI) ticket(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	var b struct {
		Path     string `json:"path"`
		Confirm  bool   `json:"confirm"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if b.Path != auth.TicketSession && (!strings.HasPrefix(b.Path, "/api/") || strings.ContainsAny(b.Path, "?#")) {
		badBody(w, errors.New("path is an API path, or \"session\""))
		return
	}
	if sess == nil || sess.ID == "" || sess.IsToken() {
		writeJSON(w, http.StatusConflict, apiError{Error: "no-session", Message: "a session is needed; a token gets no ticket"})
		return
	}
	if b.Confirm {
		a.confirmTicket(w, r, sess, b.Path, b.Password)
		return
	}
	t, err := a.Store.IssueTicket(sess.ID, b.Path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
		return
	}
	if t == "" {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "login required"})
		return
	}
	writeJSON(w, 200, map[string]any{"ticket": t, "expires_in": int(auth.TicketLifetime(b.Path).Seconds())})
}

// confirmWith is how the session's account confirms: "password", "oidc" (a fresh login at the
// provider), "none" when the login is switched off (everyone is the anonymous administrator), or
// "" when it cannot (no password and no provider).
func (a *AuthAPI) confirmWith(sess *auth.Session) string {
	switch {
	case a.Off:
		return "none"
	case a.passwordLoginOn() && a.Store.HasPassword(sess.User):
		return "password"
	case a.OIDC != nil:
		return "oidc"
	}
	return ""
}

// confirmURL is where the page sends the browser to confirm at the provider.
func confirmURL(path, returnTo string) string {
	return "/api/auth/v1/oidc/confirm?path=" + url.QueryEscape(path) + "&return=" + url.QueryEscape(returnTo)
}

// confirmMethod is GET /confirm?path=&return=: how this session confirms, so the page asks for the
// password or goes to the provider at once.
func (a *AuthAPI) confirmMethod(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	if sess == nil || sess.IsToken() {
		writeJSON(w, http.StatusConflict, apiError{Error: "no-session", Message: "a token is not asked to confirm"})
		return
	}
	m := a.confirmWith(sess)
	out := map[string]any{"method": m}
	if m == "oidc" {
		out["url"] = confirmURL(r.URL.Query().Get("path"), localPath(r.URL.Query().Get("return")))
	}
	writeJSON(w, 200, out)
}

func (a *AuthAPI) confirmTicket(w http.ResponseWriter, r *http.Request, sess *auth.Session, path, password string) {
	switch a.confirmWith(sess) {
	case "none":
	case "password":
		if err := a.Store.ConfirmPassword(sess.User, password, remote(r)); err != nil {
			a.logger().Warn("auth: confirmation refused", "user", sess.User, "path", path, "remote", remote(r), "err", err)
			authErr(w, err)
			return
		}
	case "oidc":
		writeJSON(w, http.StatusConflict, map[string]any{"error": "confirm-oidc", "message": "confirm with a fresh login at the identity provider", "url": confirmURL(path, "")})
		return
	default:
		writeJSON(w, http.StatusConflict, apiError{Error: "no-confirmation", Message: "the account has no password and no identity provider is configured"})
		return
	}
	a.issueConfirmed(w, sess, path, remote(r), "password")
}

func (a *AuthAPI) issueConfirmed(w http.ResponseWriter, sess *auth.Session, path, from, how string) {
	t, err := a.Store.IssueConfirmedTicket(sess.ID, path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
		return
	}
	if t == "" {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "login required"})
		return
	}
	a.logger().Info("auth: confirmed", "user", sess.User, "path", path, "by", how, "remote", from)
	writeJSON(w, 200, map[string]any{"ticket": t, "expires_in": int(auth.TicketTTL.Seconds())})
}

// confirmPurpose marks a provider round trip as a confirmation of the session with this key for
// path, not a sign-in.
const confirmPurpose = "confirm\x00"

// confirmSkew is how much earlier than the round trip's start the provider's auth_time may be:
// the provider's clock and the box's are not the same clock.
const confirmSkew = 30 * time.Second

// oidcConfirm is GET /oidc/confirm?path=&return=: the provider asks for the password again
// (prompt=login, max_age=0), and the callback issues a confirmed ticket for path to this session.
func (a *AuthAPI) oidcConfirm(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	if a.OIDC == nil || sess == nil || sess.IsToken() {
		writeJSON(w, http.StatusConflict, apiError{Error: "unsupported", Message: "no identity provider, or no session to confirm"})
		return
	}
	path := r.URL.Query().Get("path")
	if !strings.HasPrefix(path, "/api/") || strings.ContainsAny(path, "?#\x00") {
		badBody(w, errors.New("path is an API path"))
		return
	}
	purpose := confirmPurpose + auth.SessionKey(sess.ID) + "\x00" + path
	u, err := a.OIDC.StartWith(r.Context(), a.callbackURL(r), localPath(r.URL.Query().Get("return")), oidc.StartOptions{ForceLogin: true, Purpose: purpose})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "oidc", Message: err.Error()})
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// oidcConfirmed finishes a confirmation: the provider's account must be the session's, the session
// the one that set out, and the login fresh (auth_time after the start). The ticket travels in the
// fragment, which no server and no log sees; a refusal says why the same way.
func (a *AuthAPI) oidcConfirmed(w http.ResponseWriter, r *http.Request, id *oidc.Identity) {
	rest := strings.TrimPrefix(id.Purpose, confirmPurpose)
	key, path, _ := strings.Cut(rest, "\x00")
	back := id.ReturnTo
	if back == "" {
		back = "/"
	}
	refuse := func(why string) {
		a.logger().Warn("auth: confirmation at the provider refused", "user", id.Username, "path", path, "why", why, "remote", remote(r))
		http.Redirect(w, r, back+"#confirm-error="+url.QueryEscape(why), http.StatusFound)
	}
	var sess *auth.Session
	for _, sid := range sessionIDs(r) {
		if s := a.Store.ValidateFrom(sid, remote(r)); s != nil && auth.SessionKey(sid) == key {
			sess = s
		}
	}
	switch {
	case sess == nil:
		refuse("the session that asked has ended")
		return
	case sess.User != id.Username:
		refuse("the provider signed in " + id.Username + ", not " + sess.User)
		return
	case id.AuthTime.IsZero():
		refuse("the provider did not say when the login happened (auth_time)")
		return
	case id.AuthTime.Before(id.Started.Add(-confirmSkew)):
		refuse("the provider did not ask for the password again")
		return
	}
	t, err := a.Store.IssueConfirmedTicket(sess.ID, path)
	if err != nil || t == "" {
		refuse("no ticket")
		return
	}
	a.logger().Info("auth: confirmed", "user", sess.User, "path", path, "by", "oidc", "remote", remote(r))
	http.Redirect(w, r, back+"#confirm="+url.QueryEscape(t), http.StatusFound)
}

// ticketRedeem is POST /ticket/redeem {ticket} (open): a session ticket opens a session of its own
// for the ticket's account on this host - the store holds no id it could hand over - with the
// cookie of the request's scheme, and the answer is the login's.
func (a *AuthAPI) ticketRedeem(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Ticket string `json:"ticket"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if a.Off {
		sess := a.Store.EnsureAnonymous()
		a.setCookie(w, r, sess.ID)
		writeJSON(w, 200, map[string]any{"sid": sess.ID, "user": sess.User, "role": sess.Role, "level": sess.Level, "account_id": sess.AccountID, "must_change_password": false})
		return
	}
	sess, err := a.Store.RedeemSessionTicket(b.Ticket, remote(r), r.UserAgent())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
		return
	}
	if sess == nil {
		a.logRefused(r, "", "ticket", "unknown, spent or run-out ticket")
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid-ticket", Message: "the ticket is unknown, spent or run out"})
		return
	}
	a.logLogin(sess, "ticket")
	a.clearStale(w, r, a.cookieName(r))
	a.setCookie(w, r, sess.ID)
	writeJSON(w, 200, map[string]any{"sid": sess.ID, "user": sess.User, "role": sess.Role, "level": sess.Level, "account_id": sess.AccountID, "must_change_password": a.Store.MustChangePassword(sess.User)})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *AuthAPI) setup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := readJSON(r, &c); err != nil {
		badBody(w, err)
		return
	}
	if err := a.Store.Setup(c.Username, c.Password); err != nil {
		authErr(w, err)
		return
	}
	a.loginWith(w, r, c)
}

func (a *AuthAPI) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := readJSON(r, &c); err != nil {
		badBody(w, err)
		return
	}
	// task 19: with the switch off no account signs in with a password, whatever the name; the
	// setup (loginWith from setup) is not affected - without an account there is nothing to match
	if !a.passwordLoginOn() {
		a.logRefused(r, c.Username, auth.MethodPassword, "password login off")
		passwordLoginDisabled(w)
		return
	}
	a.loginWith(w, r, c)
}

func (a *AuthAPI) loginWith(w http.ResponseWriter, r *http.Request, c credentials) {
	// a failed login costs the client a little time: argon2 already does, this keeps it uniform
	start := time.Now()
	// occulited task 14: an account's passkeys do not touch the password login - there is no
	// second factor
	sess, why, err := a.Store.LoginDetail(c.Username, c.Password, remote(r), r.UserAgent())
	if err != nil {
		reason := why.Reason
		if reason == "" {
			reason = err.Error() // setup required, or the session could not be written
		}
		a.logRefused(r, c.Username, auth.MethodPassword, reason)
		a.logLockout(r, c.Username, why)
		if d := 300*time.Millisecond - time.Since(start); d > 0 {
			time.Sleep(d)
		}
		authErr(w, err)
		return
	}
	a.logLogin(sess, auth.MethodPassword)
	a.clearStale(w, r, a.cookieName(r))
	a.setCookie(w, r, sess.ID)
	writeJSON(w, 200, map[string]any{"sid": sess.ID, "user": sess.User, "role": sess.Role, "level": sess.Level, "account_id": sess.AccountID, "must_change_password": a.Store.MustChangePassword(sess.User)})
}

// logout ends every session the request carries - over HTTPS a browser sends the HTTP cookie
// too, and logging out means the browser is logged out - and deletes both cookies.
func (a *AuthAPI) logout(w http.ResponseWriter, r *http.Request) {
	for _, id := range sessionIDs(r) {
		a.Store.Logout(id)
	}
	clearCookie(w, SecureCookieName)
	clearCookie(w, CookieName)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *AuthAPI) password(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	var b struct {
		Current  *string `json:"current"`
		Password string  `json:"password"`
		User     string  `json:"user"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if !a.passwordLoginOn() {
		passwordLoginDisabled(w)
		return
	}
	target := sess.User
	current := b.Current
	if b.User != "" && b.User != sess.User {
		if !sess.Has(auth.ScopeAuthAdmin) {
			forbiddenScope(w, auth.ScopeAuthAdmin)
			return
		}
		target = b.User
		current = nil // an administrator resets without the current password
	} else if current == nil {
		badBody(w, errors.New("current password required"))
		return
	}
	if err := a.Store.SetPassword(target, b.Password, current, sess.ID); err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *AuthAPI) sessions(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	name := sess.User
	if r.URL.Query().Get("all") == "true" {
		if !sess.Has(auth.ScopeAuthAdmin) {
			forbiddenScope(w, auth.ScopeAuthAdmin)
			return
		}
		name = ""
	}
	// each session by its handle (B-102): the store keeps hashes, never ids, and the list never
	// hands out an id anyway
	current := ""
	if !sess.IsToken() {
		current = auth.SessionHandle(sess.ID)
	}
	writeJSON(w, 200, map[string]any{"sessions": a.Store.Sessions(name), "current": current})
}

func (a *AuthAPI) logoutEverywhere(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	n := a.Store.LogoutUser(sess.User, sess.ID)
	writeJSON(w, 200, map[string]any{"ended": n})
}

// deleteSession ends the session the list names {sid} (its handle): one of the caller's own, or
// any for an administrator, never the caller's current one.
func (a *AuthAPI) deleteSession(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	name := sess.User
	if sess.Has(auth.ScopeAuthAdmin) {
		name = ""
	}
	n := a.Store.EndSessions(r.PathValue("sid"), name, sess.ID)
	writeJSON(w, 200, map[string]any{"ended": n})
}

func (a *AuthAPI) users(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"users": a.Store.Users()})
}

func (a *AuthAPI) createUser(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Username   string     `json:"username"`
		Password   string     `json:"password"`
		Role       auth.Role  `json:"role"`
		Level      auth.Level `json:"level"`
		MustChange bool       `json:"must_change_password"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	// task 78: the level is the account's place on the ladder; role is the older body's alias
	// (admin = administer, user = operate), taken when no level is given
	if b.Level != "" && !auth.ValidLevel(b.Level) {
		badLevel(w)
		return
	}
	if b.Level == "" {
		if b.Role == "" {
			b.Role = auth.RoleUser
		}
		if b.Role != auth.RoleAdmin && b.Role != auth.RoleUser {
			authErr(w, auth.ErrBadRole)
			return
		}
		b.Level = auth.LevelOf(b.Role)
	}
	// task 19: while a provider is configured the password is optional - an account without one
	// signs in through the provider only. Without a provider it could never sign in.
	var err error
	switch {
	case b.Password != "":
		err = a.Store.CreateUserLevel(b.Username, b.Password, b.Level, b.MustChange)
	case a.OIDC != nil:
		err = a.Store.CreateUserWithoutPasswordLevel(b.Username, b.Level)
	default:
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-body", Message: "a password is required: no external login is configured, so an account without one could not sign in"})
		return
	}
	if err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// badLevel is a level that is not one of the four.
func badLevel(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-body", Message: "level must be read, operate, configure or administer"})
}

func (a *AuthAPI) deleteUser(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("name") == SessionFrom(r).User {
		writeJSON(w, http.StatusConflict, apiError{Error: "conflict", Message: "cannot delete the account you are logged in with"})
		return
	}
	if err := a.Store.DeleteUser(r.PathValue("name")); err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *AuthAPI) patchUser(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Role  auth.Role  `json:"role"`
		Level auth.Level `json:"level"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	// task 78: {level} moves the account on the ladder; {role} is the older alias
	var err error
	if b.Level != "" && !auth.ValidLevel(b.Level) {
		badLevel(w)
		return
	}
	if b.Level != "" {
		err = a.Store.SetLevel(r.PathValue("name"), b.Level)
	} else {
		err = a.Store.SetRole(r.PathValue("name"), b.Role)
	}
	if err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// tokens lists the API tokens (auth:admin), the scopes a token can be given, in the order the
// page shows them, and the installed addons' ingress scopes (addons: {id, name, scope}, task 307).
// Secrets are never listed.
func (a *AuthAPI) tokens(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"tokens": a.Store.Tokens(), "scopes": auth.Grantable, "addons": a.addonScopeList()})
}

// createToken makes a token {name, scopes, expires?, ips?} and answers with the secret exactly
// once. role is the older body's alias (admin → Full access, user → the read scopes, led →
// led), taken when scopes is absent.
func (a *AuthAPI) createToken(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    string     `json:"name"`
		Scopes  []string   `json:"scopes"`
		Role    auth.Role  `json:"role"`
		Expires *time.Time `json:"expires"`
		IPs     []string   `json:"ips"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	var scopes auth.Scopes
	switch {
	case len(b.Scopes) > 0:
		var err error
		if scopes, err = auth.ParseScopes(b.Scopes); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-scope", Message: err.Error()})
			return
		}
		if err := a.checkAddonScopes(scopes); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-scope", Message: err.Error()})
			return
		}
	case b.Role != "":
		if scopes = auth.RoleScopes(b.Role); scopes == nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-scope", Message: "role must be admin, user or led"})
			return
		}
	default:
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-scope", Message: "a token needs at least one scope"})
		return
	}
	secret, err := a.Store.CreateToken(b.Name, auth.TokenOptions{Scopes: scopes, Expires: b.Expires, IPs: b.IPs})
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrDuplicateToken):
			writeJSON(w, http.StatusConflict, apiError{Error: "duplicate", Message: err.Error()})
		case errors.Is(err, auth.ErrBadScope), errors.Is(err, auth.ErrBadIPRange), errors.Is(err, auth.ErrTokenExpiry), errors.Is(err, auth.ErrBadTokenName):
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		default:
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
		}
		return
	}
	withCaller(r, a.logger()).Info("auth: token created", "name", b.Name, "scopes", scopes, "expires", b.Expires, "ips", b.IPs)
	out := map[string]any{"name": b.Name, "scopes": scopes, "token": secret}
	if b.Expires != nil {
		out["expires"] = b.Expires
	}
	if len(b.IPs) > 0 {
		out["ips"] = b.IPs
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *AuthAPI) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.DeleteToken(r.PathValue("name")); err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- external login (OIDC) --------------------------------------------------------------------

// oidcInfo tells the login page what to offer: the provider's button, and whether the password
// form is on (task 19: the switch holds only with a provider, so it reads true without one).
func (a *AuthAPI) oidcInfo(w http.ResponseWriter, _ *http.Request) {
	if a.OIDC == nil {
		writeJSON(w, 200, map[string]any{"enabled": false, "password_login": true})
		return
	}
	name := a.OIDCName
	if name == "" {
		name = "SSO"
	}
	// occulited task 70: hide_password_form - the page shows the form only behind its link
	writeJSON(w, 200, map[string]any{"enabled": true, "name": name, "password_login": a.passwordLoginOn(), "hide_password_form": a.passwordLoginOn() && a.loginOptions().HidePasswordFormOn()})
}

// callbackURL is this box as the browser reaches it: lighttpd forwards the scheme, the Host
// header carries the address.
func (a *AuthAPI) callbackURL(r *http.Request) string {
	scheme := "http"
	if a.secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/auth/v1/oidc/callback"
}

func (a *AuthAPI) oidcStart(w http.ResponseWriter, r *http.Request) {
	if a.OIDC == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no external login configured"})
		return
	}
	u, err := a.OIDC.Start(r.Context(), a.callbackURL(r), localPath(r.URL.Query().Get("return")))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "oidc", Message: err.Error()})
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// oidcCallback finishes the flow: the provider's user name is matched to the account of exactly
// that name (task 19, D-53), a session with the account's role is opened, the browser lands on
// the shell. No such account: nothing is created, the name and the remote address go to the
// journal, and the login page says so (`?error=no-account&user=<name>`). Every other failure
// goes to the login page as its message.
func (a *AuthAPI) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if a.OIDC == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no external login configured"})
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		http.Redirect(w, r, "/login?error="+url.QueryEscape(e+": "+q.Get("error_description")), http.StatusFound)
		return
	}
	id, err := a.OIDC.Finish(r.Context(), q.Get("state"), q.Get("code"))
	if err != nil {
		a.logRefused(r, "", auth.MethodOIDC, "provider: "+logName(err.Error()))
		http.Redirect(w, r, "/login?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if strings.HasPrefix(id.Purpose, confirmPurpose) {
		a.oidcConfirmed(w, r, id)
		return
	}
	sess, err := a.Store.LoginExternal("oidc", id.Username, remote(r), r.UserAgent())
	if errors.Is(err, auth.ErrUnknownUser) {
		a.logRefused(r, id.Username, auth.MethodOIDC, "unknown user (nothing is created)", "subject", logName(id.Subject))
		http.Redirect(w, r, "/login?error=no-account&user="+url.QueryEscape(id.Username), http.StatusFound)
		return
	}
	if err != nil {
		a.logRefused(r, id.Username, auth.MethodOIDC, err.Error())
		http.Redirect(w, r, "/login?error="+url.QueryEscape("account "+id.Username+": "+err.Error()), http.StatusFound)
		return
	}
	a.logLogin(sess, auth.MethodOIDC)
	a.clearStale(w, r, a.cookieName(r))
	a.setCookie(w, r, sess.ID)
	// the cookie is the session on this host; the shell reads its id from GET /state. Until task
	// 125 the redirect carried ?sid= as well, one more URL with the session in it.
	target := "/"
	if id.ReturnTo != "" {
		target = id.ReturnTo
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// localPath accepts only a same-origin absolute path as a place to return to after login;
// anything else (a full URL, a protocol-relative one) is dropped.
func localPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\r\n") {
		return ""
	}
	return p
}

// ---- the authentication mode (task 29) --------------------------------------------------------

// authConfigView is GET /config and the answer of PUT: the mode and the provider, the secret
// only as "set or not".
type authConfigView struct {
	Mode            string   `json:"mode"`
	Modes           []string `json:"modes"`
	Name            string   `json:"name"`
	Issuer          string   `json:"issuer"`
	ClientID        string   `json:"client_id"`
	ClientSecretSet bool     `json:"client_secret_set"`
	UsernameClaim   string   `json:"username_claim"`
	Scopes          string   `json:"scopes"`
	// PasswordLogin is the switch (task 19): the stored value, meaningful in mode oidc only.
	PasswordLogin bool `json:"password_login"`
	// RestartRequired: the running daemon still uses the mode it started with.
	RestartRequired bool   `json:"restart_required"`
	Running         string `json:"running"` // the mode in force now
	// SessionIdle and SessionMax are the sessions' idle timeout and lifetime in force (task 262),
	// Go durations; PUT takes them as such, within auth.ValidSessionLimits' bounds.
	SessionIdle string `json:"session_idle"`
	SessionMax  string `json:"session_max"`
	// occulited task 70: the login page's options. HidePasswordForm (mode oidc) puts the password
	// form behind a link; it can be switched on only once ProviderLoginSeen (an account has signed
	// in through the provider). PasskeyOffer is one of PasskeyOffers.
	HidePasswordForm  bool     `json:"hide_password_form"`
	ProviderLoginSeen bool     `json:"provider_login_seen"`
	PasskeyOffer      string   `json:"passkey_offer"`
	PasskeyOffers     []string `json:"passkey_offers"`
}

func (a *AuthAPI) runningMode() string {
	switch {
	case a.Off:
		return "off"
	case a.OIDC != nil:
		return "oidc"
	}
	return "local"
}

func (a *AuthAPI) view(c config.AuthConfig, restart bool) authConfigView {
	idle, maxAge := a.Store.SessionLimits()
	return authConfigView{Mode: c.EffectiveMode(), Modes: config.AuthModes, Name: c.OIDC.Name, Issuer: c.OIDC.Issuer, ClientID: c.OIDC.ClientID,
		ClientSecretSet: c.OIDC.ClientSecret != "", UsernameClaim: c.OIDC.UsernameClaim, Scopes: c.OIDC.Scopes, PasswordLogin: c.PasswordLoginOn(),
		RestartRequired: restart, Running: a.runningMode(), SessionIdle: idle.String(), SessionMax: maxAge.String(),
		HidePasswordForm: c.OIDC.HidePasswordForm, ProviderLoginSeen: a.providerLoginSeen(), PasskeyOffer: c.EffectivePasskeyOffer(), PasskeyOffers: config.PasskeyOffers}
}

func (a *AuthAPI) authConfig(w http.ResponseWriter, r *http.Request) {
	if a.ConfigFile == "" {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "no-config", Message: "this daemon was started without a configuration file"})
		return
	}
	cfg, err := config.Load(a.ConfigFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "config", Message: err.Error()})
		return
	}
	writeJSON(w, 200, a.view(cfg.Auth, cfg.Auth.EffectiveMode() != a.runningMode()))
}

// authConfigWritable are the fields PUT /config takes; the rest of GET's answer is read-only.
var authConfigWritable = []string{"mode", "name", "issuer", "client_id", "client_secret", "username_claim", "scopes", "password_login", "session_idle", "session_max", "hide_password_form", "passkey_offer"}

// unknownField is the field name of encoding/json's DisallowUnknownFields error.
func unknownField(err error) (string, bool) {
	const prefix = `json: unknown field "`
	m := err.Error()
	if !strings.HasPrefix(m, prefix) || !strings.HasSuffix(m, `"`) {
		return "", false
	}
	return m[len(prefix) : len(m)-1], true
}

// authConfigPut writes the mode and the provider into occulited.json. The client secret is
// write-only: an empty one in the body keeps the stored one. The mode takes effect at the next
// start; the password-login switch (task 19) at once. The switch can only be off in mode oidc -
// any other mode writes it back on - and it can only be turned off from a session that came
// through the provider: whoever does it has just proven the provider signs them in, so the box
// is not locked by the very click. The console is the way back in either way.
func (a *AuthAPI) authConfigPut(w http.ResponseWriter, r *http.Request) {
	if a.ConfigFile == "" {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "no-config", Message: "this daemon was started without a configuration file"})
		return
	}
	var body struct {
		Mode          string `json:"mode"`
		Name          string `json:"name"`
		Issuer        string `json:"issuer"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		UsernameClaim string `json:"username_claim"`
		Scopes        string `json:"scopes"`
		PasswordLogin *bool  `json:"password_login"` // absent: as stored
		// task 262: the session lengths, Go durations; absent: as stored
		SessionIdle *string `json:"session_idle"`
		SessionMax  *string `json:"session_max"`
		// occulited task 70; absent: as stored
		HidePasswordForm *bool   `json:"hide_password_form"`
		PasskeyOffer     *string `json:"passkey_offer"`
	}
	if err := readJSON(r, &body); err != nil {
		// openccu-lite B-206: a client that sends GET's answer back (modes, client_secret_set,
		// restart_required, running) is told which fields PUT takes, not only that one is unknown
		if f, ok := unknownField(err); ok {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-body", Message: fmt.Sprintf("%q is not a field PUT /config takes (it may be one of GET's read-only fields); send only %s", f, strings.Join(authConfigWritable, ", "))})
			return
		}
		badBody(w, err)
		return
	}
	switch body.Mode {
	case "local", "oidc", "off":
	default:
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "mode must be local, oidc or off"})
		return
	}
	body.Issuer, body.ClientID = strings.TrimSpace(body.Issuer), strings.TrimSpace(body.ClientID)
	if body.Mode == "oidc" && (body.Issuer == "" || body.ClientID == "") {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "oidc needs an issuer and a client id"})
		return
	}
	if body.Issuer != "" && !strings.HasPrefix(body.Issuer, "https://") && !strings.HasPrefix(body.Issuer, "http://") {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "the issuer is a URL"})
		return
	}
	cfg, err := config.Load(a.ConfigFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "config", Message: err.Error()})
		return
	}
	o := &cfg.Auth.OIDC
	wasOn := cfg.Auth.PasswordLoginOn()
	if body.PasswordLogin != nil {
		o.PasswordLogin = *body.PasswordLogin
	}
	if body.Mode != "oidc" {
		o.PasswordLogin = true
	}
	if wasOn && !o.PasswordLogin && SessionFrom(r).Method != auth.MethodOIDC {
		writeJSON(w, http.StatusForbidden, apiError{Error: "provider_session_required", Message: "password login can only be switched off from a session that came through the identity provider: sign in that way first"})
		return
	}
	// occulited task 70: the password form hidden only once the provider has signed someone in -
	// whoever hides it has seen the provider work; the link and the console stay the way back
	if body.HidePasswordForm != nil {
		if *body.HidePasswordForm && body.Mode == "oidc" && !o.HidePasswordForm && !a.providerLoginSeen() {
			writeJSON(w, http.StatusConflict, apiError{Error: "provider_login_required", Message: "the password form can only be hidden once an account has signed in through the identity provider"})
			return
		}
		o.HidePasswordForm = *body.HidePasswordForm
	}
	if body.Mode != "oidc" {
		o.HidePasswordForm = false
	}
	if body.PasskeyOffer != nil {
		v := strings.TrimSpace(*body.PasskeyOffer)
		if v != "" && !slices.Contains(config.PasskeyOffers, v) {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "passkey_offer must be one of " + strings.Join(config.PasskeyOffers, ", ")})
			return
		}
		if v == "autofill" {
			v = "" // the default stays out of the file
		}
		cfg.Auth.PasskeyOffer = v
	}
	// task 262: the session lengths - parsed, within the bounds, in force at once
	if body.SessionIdle != nil {
		cfg.Auth.SessionIdle = strings.TrimSpace(*body.SessionIdle)
	}
	if body.SessionMax != nil {
		cfg.Auth.SessionMax = strings.TrimSpace(*body.SessionMax)
	}
	idle, maxAge, lerr := cfg.Auth.SessionLimits()
	if lerr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "the session lengths are durations such as 30m or 12h: " + lerr.Error()})
		return
	}
	if idle == 0 {
		idle = auth.DefaultIdleTimeout
	}
	if maxAge == 0 {
		maxAge = auth.DefaultMaxAge
	}
	if !auth.ValidSessionLimits(idle, maxAge) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: auth.ErrSessionLimits.Error()})
		return
	}
	cfg.Auth.Mode = body.Mode
	o.Enabled = body.Mode == "oidc"
	o.Name, o.Issuer, o.ClientID = strings.TrimSpace(body.Name), body.Issuer, body.ClientID
	if body.ClientSecret != "" {
		o.ClientSecret = body.ClientSecret
	}
	o.UsernameClaim, o.Scopes = strings.TrimSpace(body.UsernameClaim), strings.TrimSpace(body.Scopes)
	if err := config.Save(a.ConfigFile, cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "config", Message: err.Error()})
		return
	}
	if on := cfg.Auth.PasswordLoginOn(); on != wasOn {
		withCaller(r, a.logger()).Info("auth: password login switched", "password_login", on)
	}
	if oldIdle, oldMax := a.Store.SessionLimits(); oldIdle != idle || oldMax != maxAge {
		_ = a.Store.SetSessionLimits(idle, maxAge) // validated above
		withCaller(r, a.logger()).Info("auth: session limits changed", "idle", idle.String(), "max", maxAge.String())
	}
	writeJSON(w, 200, a.view(cfg.Auth, cfg.Auth.EffectiveMode() != a.runningMode()))
}
