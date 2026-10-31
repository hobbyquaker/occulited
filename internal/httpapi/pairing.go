package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/pairing"
)

// ---- client pairing: /api/auth/v1/pairing (openccu-lite task 219) ------------------------------
//
// A program asks for access without a credential (open, local networks only), shows a six-digit
// code, and waits; an administrator sees the request on the Status page with the same code and
// approves it - all or nothing - or rejects it. The code is bound to the certificate the program
// saw (internal/pairing). The token that comes out is an ordinary API token with a client record.

func (a *AuthAPI) registerPairing(mux *http.ServeMux, p string) {
	// the program's side is open (its poll secret is its credential), under a path of its own:
	// the middleware's open list goes by path, and GET /pairing is the administrator's
	route(mux, scopeOpen, "POST "+p+"/pairing/request", a.pairingAsk)
	route(mux, scopeOpen, "GET "+p+"/pairing/request/{id}", a.pairingPoll)
	route(mux, scopeOpen, "DELETE "+p+"/pairing/request/{id}", a.pairingWithdraw)
	route(mux, auth.ScopeAuthAdmin, "GET "+p+"/pairing", a.pairingList)
	route(mux, auth.ScopeAuthAdmin, "GET "+p+"/pairing/stream", a.pairingStream)
	route(mux, auth.ScopeAuthAdmin, "POST "+p+"/pairing/{id}/approve", a.pairingApprove)
	route(mux, auth.ScopeAuthAdmin, "POST "+p+"/pairing/{id}/reject", a.pairingReject)
	route(mux, auth.ScopeAuthAdmin, "PUT "+p+"/pairing/settings", a.pairingSettings)
	route(mux, auth.ScopeAuthAdmin, "PATCH "+p+"/tokens/{name}", a.patchToken)
	route(mux, auth.ScopeSelf, "POST "+p+"/tokens/self/rotate", a.rotateToken)
}

// PairingEnabled reads the switch: on unless occulited.json says auth.pairing: false.
func PairingEnabled(path string) bool {
	if path == "" {
		return true
	}
	c, err := config.Load(path)
	if err != nil || c.Auth.Pairing == nil {
		return true
	}
	return *c.Auth.Pairing
}

func (a *AuthAPI) pairingOff(w http.ResponseWriter) bool {
	if a.Pairing == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "pairing is not available on this system"})
		return true
	}
	return false
}

func pairingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pairing.ErrOff):
		writeJSON(w, http.StatusForbidden, apiError{Error: "pairing-off", Message: err.Error()})
	case errors.Is(err, pairing.ErrNotLocal):
		writeJSON(w, http.StatusForbidden, apiError{Error: "not-local", Message: err.Error()})
	case errors.Is(err, pairing.ErrLimit), errors.Is(err, pairing.ErrMuted):
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "limit", Message: err.Error()})
	case errors.Is(err, pairing.ErrSlowDown):
		w.Header().Set("Retry-After", strconv.Itoa(int(pairing.Interval/time.Second)))
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "slow_down", Message: "poll every " + pairing.Interval.String() + ", or wait with ?wait=30"})
	case errors.Is(err, pairing.ErrInvalid), errors.Is(err, auth.ErrBadScope):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
	case errors.Is(err, pairing.ErrUnknown):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: err.Error()})
	case errors.Is(err, pairing.ErrPoll):
		writeJSON(w, http.StatusForbidden, apiError{Error: "forbidden", Message: err.Error()})
	case errors.Is(err, pairing.ErrWrongCode):
		writeJSON(w, http.StatusConflict, apiError{Error: "wrong-code", Message: "the code is not this request's: the request is rejected"})
	case errors.Is(err, pairing.ErrConfirmRequired):
		writeJSON(w, http.StatusForbidden, apiError{Error: "confirm-required", Message: err.Error()})
	case errors.Is(err, pairing.ErrNotReady):
		writeJSON(w, http.StatusConflict, apiError{Error: "not-ready", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed", Message: err.Error()})
	}
}

// the certificate the program saw: the system's own when the request came over HTTPS
func (a *AuthAPI) servedFingerprint(r *http.Request) []byte {
	if !a.secure(r) || a.CertFingerprint == nil {
		return nil
	}
	return a.CertFingerprint()
}

func (a *AuthAPI) pairingAsk(w http.ResponseWriter, r *http.Request) {
	if a.pairingOff(w) {
		return
	}
	var ask pairing.Ask
	if err := readJSON(r, &ask); err != nil {
		badBody(w, err)
		return
	}
	ans, err := a.Pairing.Request(ask, remote(r), a.servedFingerprint(r))
	if err != nil {
		pairingError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, ans)
}

func pollSecret(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Pairing ") {
		return strings.TrimSpace(h[8:])
	}
	return ""
}

// pairingPoll is GET /pairing/request/{id} with Authorization: Pairing <poll>; ?client_nonce= reveals the
// program's half of the code (first poll), ?wait= (seconds, at most 30) long-polls.
func (a *AuthAPI) pairingPoll(w http.ResponseWriter, r *http.Request) {
	if a.pairingOff(w) {
		return
	}
	q := r.URL.Query()
	wait := time.Duration(0)
	if v := q.Get("wait"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: "wait is seconds, at most 30"})
			return
		}
		wait = time.Duration(n) * time.Second
	}
	res, err := a.Pairing.Poll(r.Context(), r.PathValue("id"), pollSecret(r), q.Get("client_nonce"), wait)
	if err != nil {
		pairingError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, res)
}

func (a *AuthAPI) pairingWithdraw(w http.ResponseWriter, r *http.Request) {
	if a.pairingOff(w) {
		return
	}
	if err := a.Pairing.Withdraw(r.PathValue("id"), pollSecret(r)); err != nil {
		pairingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *AuthAPI) pairingView() map[string]any {
	return map[string]any{"enabled": PairingEnabled(a.ConfigFile), "requests": a.Pairing.Pending()}
}

func (a *AuthAPI) pairingList(w http.ResponseWriter, _ *http.Request) {
	if a.pairingOff(w) {
		return
	}
	writeJSON(w, 200, a.pairingView())
}

// pairingStream is the card's live list: the whole view at once and on every change, a ping every 30 s.
func (a *AuthAPI) pairingStream(w http.ResponseWriter, r *http.Request) {
	if a.pairingOff(w) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	ch, stop := a.Pairing.Watch()
	defer stop()
	send := func() {
		b, _ := json.Marshal(a.pairingView())
		fmt.Fprintf(w, "event: pairing\ndata: %s\n\n", b)
		flusher.Flush()
	}
	send()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	// a request expires on its own: look again every few seconds while any waits
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			send()
		case <-tick.C:
			_ = a.Pairing.Pending() // expiry signals the watchers
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func byUser(r *http.Request) string {
	if s := SessionFrom(r); s != nil {
		return s.User
	}
	return ""
}

// PairingApprovePath is the path a confirmed ticket is issued for when it approves request id: one
// ticket, one request.
func PairingApprovePath(id string) string { return "/api/auth/v1/pairing/" + id + "/approve" }

func (a *AuthAPI) pairingApprove(w http.ResponseWriter, r *http.Request) {
	if a.pairingOff(w) {
		return
	}
	var b struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	id := r.PathValue("id")
	// occulited task 135: a request for backups is approved with a confirmed ticket for this very
	// request, in this session; a token has no password to confirm and never holds one
	need, err := a.Pairing.NeedsConfirmation(id)
	if err != nil {
		pairingError(w, err)
		return
	}
	confirmed := false
	if sess := SessionFrom(r); need && sess != nil && !sess.IsToken() {
		if t := r.Header.Get("X-Occulite-Confirm"); t != "" {
			confirmed = a.Store.RedeemConfirmed(t, PairingApprovePath(id), sess.ID)
		}
	}
	_, name, err := a.Pairing.Approve(id, b.Code, byUser(r), confirmed)
	if err != nil {
		pairingError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "token": name})
}

func (a *AuthAPI) pairingReject(w http.ResponseWriter, r *http.Request) {
	if a.pairingOff(w) {
		return
	}
	if err := a.Pairing.Reject(r.PathValue("id"), byUser(r)); err != nil {
		pairingError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// pairingSettings is PUT {enabled}: the switch, occulited.json auth.pairing, in force at once.
func (a *AuthAPI) pairingSettings(w http.ResponseWriter, r *http.Request) {
	if a.pairingOff(w) {
		return
	}
	if a.ConfigFile == "" {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no configuration file"})
		return
	}
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &b); err != nil || b.Enabled == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "{enabled: true|false}"})
		return
	}
	c, err := config.Load(a.ConfigFile)
	if err != nil {
		writeErr(w, err)
		return
	}
	c.Auth.Pairing = b.Enabled
	if err := config.Save(a.ConfigFile, c); err != nil {
		writeErr(w, err)
		return
	}
	withCaller(r, a.pairLog()).Info("pairing: switched", "enabled", *b.Enabled)
	writeJSON(w, 200, a.pairingView())
}

// patchToken is PATCH /tokens/{name} {label?, scopes?}: a paired program's label, and scopes
// narrowed (never widened).
func (a *AuthAPI) patchToken(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Label  *string  `json:"label"`
		Scopes []string `json:"scopes"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	t, err := a.Store.UpdateToken(r.PathValue("name"), b.Label, b.Scopes)
	switch {
	case errors.Is(err, auth.ErrUnknownToken):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: err.Error()})
	case errors.Is(err, auth.ErrWiden), errors.Is(err, auth.ErrBadScope):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, 200, t)
	}
}

// rotateToken is POST /tokens/self/rotate: a token's program asks for a new secret; the old one
// stays valid for a minute.
func (a *AuthAPI) rotateToken(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r)
	if sess == nil || !sess.IsToken() {
		writeJSON(w, http.StatusForbidden, apiError{Error: "forbidden", Message: "a token rotates itself: call this with the token"})
		return
	}
	name := strings.TrimPrefix(sess.ID, auth.TokenSessionPrefix)
	secret, err := a.Store.RotateToken(name, time.Minute)
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: "only a stored token rotates: " + err.Error()})
		return
	}
	withCaller(r, a.pairLog()).Info("auth: token rotated", "token", name)
	writeJSON(w, 200, map[string]any{"token": secret, "name": name, "previous_valid_s": 60})
}

func (a *AuthAPI) pairLog() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}
