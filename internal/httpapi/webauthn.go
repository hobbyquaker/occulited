package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// Passkeys (openccu-lite task 262, D-78's neighbour in ASVS V6.5; occulited task 14): the WebAuthn
// ceremonies of the login page and the Account page, with github.com/go-webauthn/webauthn.
//
// The relying party is the system's name. A WebAuthn credential is bound to an RP ID, which must
// be the host name the page was opened on (or a registrable suffix of it) - never an IP address -
// so a key is made on the system's full name, <host>.<domain>, and works there. The name the
// request came in on (r.Host) is the RP ID of every ceremony; a registration is refused on an IP
// literal, and on a name that is not the system's full name where the system knows it (FQDN), so
// that a key is not made on a name the browser will not use again (the bare host, a mDNS alias).
// A login uses whatever name the page is on: a key made on another name is simply not found by
// the browser. The browser needs a secure context and, in Chrome, a certificate it trusts; the
// page says so when a ceremony fails.
//
// The ceremonies' state - the challenge and what it was issued for - lives in the auth store's
// pending table between the two halves (auth.StorePending / TakePending, PendingTTL), keyed by an
// id the client sends back, and bound to the client's address. A ceremony is spent by its second
// half whatever the outcome.
//
// A passkey is a login of its own, never a second factor (occulited task 14): POST /login/passkey
// answers a challenge for a discoverable credential with user verification required, and
// POST /login/passkey/finish opens the session (method passkey) for the account the credential
// names - no name, no password. POST /login with the password is not touched by an account's keys.
// A registration asks for a resident key and user verification, both required, and refuses a
// credential that is not a passkey. A refused assertion counts towards the lockout like a wrong
// password. Adding and removing a key needs the confirmed ticket of task 154 (X-Occulite-Confirm:
// the password again, or a fresh login at the provider); an administrator removes another
// account's keys with auth:admin, which ends that account's sessions like a password reset.

// RPDisplayName is what an authenticator shows as the relying party.
const RPDisplayName = "openccu-lite"

// ceremonyTimeout is the browser's timeout for a ceremony; the store's PendingTTL bounds the
// server side.
const ceremonyTimeout = 90 * time.Second

func (a *AuthAPI) registerWebAuthn(mux *http.ServeMux, p string) {
	route(mux, scopeOpen, "GET "+p+"/webauthn", a.webauthnInfo)
	route(mux, scopeOpen, "POST "+p+"/login/passkey", a.loginPasskeyBegin)
	route(mux, scopeOpen, "POST "+p+"/login/passkey/finish", a.loginPasskeyFinish)
	route(mux, auth.ScopeSelf, "GET "+p+"/me/webauthn", a.myKeys)
	route(mux, auth.ScopeSelf, "POST "+p+"/me/webauthn/begin", a.registerBegin)
	route(mux, auth.ScopeSelf, "POST "+p+"/me/webauthn/finish", a.registerFinish)
	route(mux, auth.ScopeSelf, "DELETE "+p+"/me/webauthn/{id}", a.removeMyKey)
	route(mux, auth.ScopeAuthAdmin, "GET "+p+"/users/{name}/webauthn", a.userKeys)
	route(mux, auth.ScopeAuthAdmin, "DELETE "+p+"/users/{name}/webauthn/{id}", a.removeUserKey)
}

// webauthnOpen are the routes without a session (open() names them too).
var webauthnOpen = map[string]bool{
	"/api/auth/v1/webauthn":             true,
	"/api/auth/v1/login/passkey":        true,
	"/api/auth/v1/login/passkey/finish": true,
}

// hostOnly is r.Host without a port, lower case, brackets off an IPv6 literal.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// fqdn is the system's full name, lower case, or "" when it is not known (a development daemon
// without a root, or a system without a domain).
func (a *AuthAPI) fqdn() string {
	if a.FQDN == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(a.FQDN(), "."))
}

// relyingParty is the WebAuthn configuration for the name the request came in on. registering
// says whether a key is about to be made, which is where the name is held to the system's own.
func (a *AuthAPI) relyingParty(w http.ResponseWriter, r *http.Request, registering bool) *webauthn.WebAuthn {
	host := hostOnly(r.Host)
	// an RP ID is a domain name: not an address, and not a bare label such as "ccu" either (the
	// library refuses one; a browser would too) - localhost is the one exception, for development
	if host == "" || net.ParseIP(host) != nil || (!strings.Contains(host, ".") && host != "localhost") {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "webauthn-needs-name", "message": "security keys are bound to the system's full name (host and domain): open the system by that name, not by its address or its bare host name", "name": a.fqdn()})
		return nil
	}
	if registering {
		if full := a.fqdn(); full != "" && host != full && !strings.HasSuffix(full, "."+host) && host != "localhost" {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "webauthn-wrong-name", "message": "a key is made on the system's full name, so that it is found there later: open the system as " + full, "name": full})
			return nil
		}
	}
	scheme := "http"
	if a.secure(r) {
		scheme = "https"
	}
	cfg := &webauthn.Config{
		RPID:                  host,
		RPDisplayName:         RPDisplayName,
		RPOrigins:             []string{scheme + "://" + strings.ToLower(r.Host)},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTimeout, TimeoutUVD: ceremonyTimeout},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTimeout, TimeoutUVD: ceremonyTimeout},
		},
	}
	rp, err := webauthn.New(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
		return nil
	}
	return rp
}

// webauthnInfo is GET /webauthn, open: what the login and account pages need before a ceremony -
// whether any account has a passkey (the button), whether any has a key at all (the Network
// page's warning before a rename), and the name keys are made on.
func (a *AuthAPI) webauthnInfo(w http.ResponseWriter, _ *http.Request) {
	// occulited task 70: offer - how the login page offers a passkey (autofill, prompt, off)
	out := map[string]any{"passkeys": !a.Off && a.passwordLoginOn() && a.Store.AnyPasskey(), "registered": a.Store.AnyWebAuthn(), "name": a.fqdn(), "offer": a.loginOptions().EffectivePasskeyOffer()}
	writeJSON(w, 200, out)
}

// pendingBody is the second half's envelope: the ceremony's id and the browser's credential as
// PublicKeyCredential.toJSON() renders it.
type pendingBody struct {
	Login        string          `json:"login,omitempty"`
	Registration string          `json:"registration,omitempty"`
	Response     json.RawMessage `json:"response"`
}

// loginPasskeyBegin is POST /login/passkey: a challenge for a discoverable credential, user
// verification required; nothing names an account yet.
func (a *AuthAPI) loginPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if a.Off {
		writeJSON(w, http.StatusConflict, apiError{Error: "auth-off", Message: "the login is switched off on this system"})
		return
	}
	if !a.passwordLoginOn() {
		passwordLoginDisabled(w)
		return
	}
	if a.Store.LockedRemote(remote(r)) {
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "locked-out", Message: auth.ErrLockedOut.Error()})
		return
	}
	rp := a.relyingParty(w, r, false)
	if rp == nil {
		return
	}
	options, data, err := rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
		return
	}
	id, err := a.Store.StorePending("", remote(r), data)
	if err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"login": id, "options": options.Response})
}

// loginPasskeyFinish is POST /login/passkey/finish: the assertion names the account.
func (a *AuthAPI) loginPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	var b pendingBody
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if !a.passwordLoginOn() {
		passwordLoginDisabled(w)
		return
	}
	name, data, err := a.Store.TakePending(b.Login, remote(r))
	session, ok := data.(*webauthn.SessionData)
	if err != nil || name != "" || !ok {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid-credentials", Message: auth.ErrPendingLogin.Error()})
		return
	}
	rp := a.relyingParty(w, r, false)
	if rp == nil {
		return
	}
	found := ""
	parsed, err := protocol.ParseCredentialRequestResponseBytes(b.Response)
	if err == nil {
		var cred *webauthn.Credential
		var user webauthn.User
		user, cred, err = rp.ValidatePasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) {
			u, ferr := a.Store.WebAuthnUserByHandle(rawID, handle)
			if ferr != nil {
				return nil, ferr
			}
			found = u.Name
			return u, nil
		}, *session, parsed)
		if err == nil {
			// user verification is what makes a key a passkey: the library asked for it and
			// checked the flag; a key that did not verify the user signs nobody in
			if !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
				err = errors.New("the key did not verify the user")
			} else {
				sess, cerr := a.Store.CompleteLogin(user.WebAuthnName(), cred, auth.MethodPasskey, remote(r), r.UserAgent())
				if cerr != nil {
					a.logRefused(r, user.WebAuthnName(), auth.MethodPasskey, cerr.Error())
					authErr(w, cerr)
					return
				}
				a.logLogin(sess, auth.MethodPasskey)
				a.clearStale(w, r, a.cookieName(r))
				a.setCookie(w, r, sess.ID)
				writeJSON(w, 200, map[string]any{"sid": sess.ID, "user": sess.User, "role": sess.Role, "level": sess.Level, "account_id": sess.AccountID, "must_change_password": false})
				return
			}
		}
	}
	why := a.Store.FailKeyStep(found, remote(r))
	a.logRefused(r, found, auth.MethodPasskey, "passkey refused: "+webauthnReason(err))
	a.logLockout(r, found, why)
	writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid-credentials", Message: "the passkey was not accepted"})
}

// webauthnReason is the library's error for the log: its details, never the client's data.
func webauthnReason(err error) string {
	if err == nil {
		return "no error"
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		if pe.DevInfo != "" {
			return pe.Details + ": " + pe.DevInfo
		}
		return pe.Details
	}
	return err.Error()
}

// --- the account's keys -----------------------------------------------------------------------

// myKeys is GET /me/webauthn: the caller's keys.
func (a *AuthAPI) myKeys(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	if sess == nil || sess.IsToken() || sess.User == "" {
		writeJSON(w, http.StatusConflict, apiError{Error: "no-session", Message: "an account session is needed"})
		return
	}
	writeJSON(w, 200, map[string]any{"keys": nonNilKeys(a.Store.WebAuthnKeys(sess.User)), "name": a.fqdn()})
}

func nonNilKeys(k []auth.WebAuthnView) []auth.WebAuthnView {
	if k == nil {
		return []auth.WebAuthnView{}
	}
	return k
}

// webauthnConfirmPath is the path the confirmed ticket of adding or removing a key is for.
const webauthnConfirmPath = "/api/auth/v1/me/webauthn"

// confirmedSelf is the re-authentication of a key change: the account's session with a confirmed
// ticket for the keys' path (task 154's X-Occulite-Confirm). A token is not an account here.
func (a *AuthAPI) confirmedSelf(w http.ResponseWriter, r *http.Request) *auth.Session {
	sess := SessionFrom(r)
	if sess == nil || sess.IsToken() || sess.User == "" || a.Off {
		writeJSON(w, http.StatusConflict, apiError{Error: "no-session", Message: "an account session is needed; a token has no security keys"})
		return nil
	}
	t := r.Header.Get("X-Occulite-Confirm")
	if t == "" || !a.Store.RedeemConfirmed(t, webauthnConfirmPath, sess.ID) {
		writeJSON(w, http.StatusForbidden, apiError{Error: "confirm-required", Message: "this asks for the password (or a fresh login at the provider) every time"})
		return nil
	}
	return sess
}

// registerBegin is POST /me/webauthn/begin {name}: the creation options for a new key of the
// caller's account, with the account's keys excluded; the ceremony's id comes back as
// registration. The confirmed ticket is taken here, at the start of the ceremony.
func (a *AuthAPI) registerBegin(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	sess := a.confirmedSelf(w, r)
	if sess == nil {
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" || len(name) > 64 {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-body", Message: auth.ErrWebAuthnName.Error()})
		return
	}
	user := a.Store.WebAuthnUser(sess.User)
	if user == nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-user", Message: auth.ErrUnknownUser.Error()})
		return
	}
	if len(user.Credentials) >= auth.MaxWebAuthnKeys {
		writeJSON(w, http.StatusConflict, apiError{Error: "conflict", Message: auth.ErrWebAuthnLimit.Error()})
		return
	}
	rp := a.relyingParty(w, r, true)
	if rp == nil {
		return
	}
	// occulited task 14: a key is a passkey or nothing - a discoverable credential, the user
	// verified by PIN or biometrics (the configuration's selection: both required)
	options, data, err := rp.BeginRegistration(user,
		webauthn.WithExclusions(webauthn.Credentials(user.Credentials).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExtensions(webauthn.WithExtensionCredProps()))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
		return
	}
	id, err := a.Store.StorePending(sess.User, remote(r), &registration{data: data, name: name})
	if err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"registration": id, "options": options.Response})
}

// registration is a pending key ceremony: the library's state and the name the key gets.
type registration struct {
	data *webauthn.SessionData
	name string
}

// registerFinish is POST /me/webauthn/finish {registration, response}: the attestation
// verified, the key stored under its name.
func (a *AuthAPI) registerFinish(w http.ResponseWriter, r *http.Request) {
	var b pendingBody
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	sess := SessionFrom(r)
	if sess == nil || sess.IsToken() || sess.User == "" {
		writeJSON(w, http.StatusConflict, apiError{Error: "no-session", Message: "an account session is needed"})
		return
	}
	name, data, err := a.Store.TakePending(b.Registration, remote(r))
	reg, ok := data.(*registration)
	if err != nil || !ok || name != sess.User {
		writeJSON(w, http.StatusConflict, apiError{Error: "pending-unknown", Message: auth.ErrPendingLogin.Error()})
		return
	}
	user := a.Store.WebAuthnUser(sess.User)
	if user == nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-user", Message: auth.ErrUnknownUser.Error()})
		return
	}
	rp := a.relyingParty(w, r, true)
	if rp == nil {
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(b.Response)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "webauthn-refused", Message: "the browser's answer could not be read: " + webauthnReason(err)})
		return
	}
	cred, err := rp.CreateCredential(user, *reg.data, parsed)
	if err != nil {
		withCaller(r, a.logger()).Warn("auth: a security key was refused", "user", sess.User, "reason", webauthnReason(err))
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "webauthn-refused", Message: "the key was not accepted: " + webauthnReason(err)})
		return
	}
	// the library checked the user verification (required); a resident credential is what the
	// browser was asked for - one that reports it made none is refused, one that does not report
	// (no credProps) made one, or the ceremony would have failed
	if cred.Extensions.RK == nil {
		rk := true
		cred.Extensions.RK = &rk
	}
	if !*cred.Extensions.RK || !cred.Flags.UserVerified {
		withCaller(r, a.logger()).Warn("auth: a security key was refused", "user", sess.User, "reason", "not a passkey")
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "webauthn-not-passkey", Message: auth.ErrNotPasskey.Error()})
		return
	}
	v, err := a.Store.AddWebAuthn(sess.User, *cred, reg.name)
	if err != nil {
		authErr(w, err)
		return
	}
	withCaller(r, a.logger()).Info("auth: passkey added", "user", sess.User, "key", v.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"key": v})
}

// removeMyKey is DELETE /me/webauthn/{id}: the caller's own key, with the confirmed ticket.
func (a *AuthAPI) removeMyKey(w http.ResponseWriter, r *http.Request) {
	sess := a.confirmedSelf(w, r)
	if sess == nil {
		return
	}
	if err := a.Store.RemoveWebAuthn(sess.User, r.PathValue("id"), false, ""); err != nil {
		authErr(w, err)
		return
	}
	withCaller(r, a.logger()).Info("auth: security key removed", "user", sess.User, "by", "owner")
	writeJSON(w, 200, map[string]any{"ok": true, "keys": nonNilKeys(a.Store.WebAuthnKeys(sess.User))})
}

// userKeys is GET /users/{name}/webauthn (auth:admin): another account's keys.
func (a *AuthAPI) userKeys(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if a.Store.WebAuthnUser(name) == nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-user", Message: auth.ErrUnknownUser.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"keys": nonNilKeys(a.Store.WebAuthnKeys(name))})
}

// removeUserKey is DELETE /users/{name}/webauthn/{id} (auth:admin): an administrator removes
// another account's key - the recovery when it is lost - which ends that account's sessions like
// a password reset; the caller's own session stays when it is the account.
func (a *AuthAPI) removeUserKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	keep := ""
	if sess := SessionFrom(r); sess != nil && sess.User == name {
		keep = sess.ID
	}
	if err := a.Store.RemoveWebAuthn(name, r.PathValue("id"), true, keep); err != nil {
		authErr(w, err)
		return
	}
	withCaller(r, a.logger()).Info("auth: security key removed", "user", name, "by", "administrator")
	writeJSON(w, 200, map[string]any{"ok": true, "keys": nonNilKeys(a.Store.WebAuthnKeys(name))})
}
