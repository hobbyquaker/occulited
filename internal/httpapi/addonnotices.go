package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hobbyquaker/occulited/internal/warnings"
)

// occulited task 119: an addon's own notices on the Status page (warnings.AddonNotices). The
// routes are the addon's, not a person's: the credential is the addon's control token - the one
// the addon-rc wrapper sends to /addonctl, /run/occulite/addon-tokens/<id>, readable by that
// addon's user alone - and it reaches that addon's notices and nothing else. They are registered
// without a scope (open), and the handler checks the token, as /addonctl does.
//
//	GET    /addon-notices        the addon's notices
//	PUT    /addon-notices/{id}   posts or replaces one: {severity, title: {de, en}, text?: {de, en}, link?, ttl?}
//	DELETE /addon-notices/{id}   clears one
//	DELETE /addon-notices        clears them all
//
// Administrators see and silence them with the system's warnings (GET /warnings: id addon-notice,
// variant <addon>/<notice id>).

// addonNoticesPrefix is the routes' path, for the open list.
const addonNoticesPrefix = "/api/system/v1/addon-notices"

func (a *SystemAPI) registerAddonNotices(mux *http.ServeMux, p string) {
	route(mux, scopeOpen, "GET "+p+"/addon-notices", a.addonNoticesList)
	route(mux, scopeOpen, "DELETE "+p+"/addon-notices", a.addonNoticesClear)
	route(mux, scopeOpen, "PUT "+p+"/addon-notices/{id}", a.addonNoticePut)
	route(mux, scopeOpen, "DELETE "+p+"/addon-notices/{id}", a.addonNoticeDelete)
}

// addonNoticeWarnings is the warnings source of the addon notices.
func (a *SystemAPI) addonNoticeWarnings(context.Context) ([]warnings.Warning, bool) {
	if a.AddonNotices == nil {
		return nil, true
	}
	return a.AddonNotices.Warnings(), true
}

// noticeAddon checks the control token and answers the addon it belongs to; false has answered.
func (a *SystemAPI) noticeAddon(w http.ResponseWriter, r *http.Request) (string, bool) {
	if a.AddonNotices == nil || a.AddonCtl == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "addon notices need the systemd product"})
		return "", false
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "the addon's control token is required (/run/occulite/addon-tokens/<id>)"})
		return "", false
	}
	id, ok := a.AddonCtl.Lookup(strings.TrimSpace(h[7:]))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated", Message: "unknown addon control token"})
		return "", false
	}
	return id, true
}

// noticesChanged evaluates the warnings at once, so the LED and the System menu follow the post
// and a silence of a cleared notice is spent.
func (a *SystemAPI) noticesChanged() {
	if a.Warnings != nil {
		go a.Warnings.Evaluate(context.Background())
	}
}

func (a *SystemAPI) addonNoticesList(w http.ResponseWriter, r *http.Request) {
	addon, ok := a.noticeAddon(w, r)
	if !ok {
		return
	}
	list := a.AddonNotices.List(addon)
	if list == nil {
		list = []warnings.AddonNotice{}
	}
	writeJSON(w, 200, map[string]any{"addon": addon, "notices": list, "max": warnings.MaxAddonNotices})
}

func (a *SystemAPI) addonNoticePut(w http.ResponseWriter, r *http.Request) {
	addon, ok := a.noticeAddon(w, r)
	if !ok {
		return
	}
	var in warnings.AddonNoticeInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	n, created, err := a.AddonNotices.Put(addon, r.PathValue("id"), in)
	switch {
	case errors.Is(err, warnings.ErrNoticeInvalid):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	case errors.Is(err, warnings.ErrTooManyNotices):
		writeJSON(w, http.StatusConflict, apiError{Error: "too-many", Message: err.Error()})
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	if created {
		reqLog(r).Info("addon notice: posted", "addon", addon, "notice", n.ID, "severity", n.Severity)
	}
	a.noticesChanged()
	st := http.StatusOK
	if created {
		st = http.StatusCreated
	}
	writeJSON(w, st, n)
}

func (a *SystemAPI) addonNoticeDelete(w http.ResponseWriter, r *http.Request) {
	addon, ok := a.noticeAddon(w, r)
	if !ok {
		return
	}
	err := a.AddonNotices.Delete(addon, r.PathValue("id"))
	switch {
	case errors.Is(err, warnings.ErrNoSuchNotice):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: err.Error()})
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	reqLog(r).Info("addon notice: cleared", "addon", addon, "notice", r.PathValue("id"))
	a.noticesChanged()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *SystemAPI) addonNoticesClear(w http.ResponseWriter, r *http.Request) {
	addon, ok := a.noticeAddon(w, r)
	if !ok {
		return
	}
	n, err := a.AddonNotices.Clear(addon)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n > 0 {
		reqLog(r).Info("addon notice: all cleared", "addon", addon, "count", n)
		a.noticesChanged()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "cleared": n})
}
