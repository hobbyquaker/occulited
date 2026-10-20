package httpapi

import (
	"net/http"
	"slices"

	"github.com/hobbyquaker/occulited/internal/addonunit"
	"github.com/hobbyquaker/occulited/internal/auth"
)

// Task 119 (D-75): the early start of addons. An addon whose catalogue entry declares
// runtime.start "early" starts before the radio interfaces are ready; the user can switch that off
// on the Addons page, for all addons and per addon. Both switches are on by default and live in
// occulited.json's addons block; occulited writes addon-policy/<id>.start from them and the
// declarations (system.SystemdAddons.RefreshAddonStart), and the occu-addons generator reads the
// file at the next boot - a change never restarts anything.

// EarlyStarts keeps the two switches; nil on SystemAPI means on for every addon and no way to
// change them (the routes answer 501).
type EarlyStarts interface {
	EarlyStart() (on bool, off []string)
	SetEarlyStart(on bool, off []string) error
}

// StartDelays keeps the addons' start delays (openccu-lite task 369): per addon, the seconds it
// waits at boot after its radio interfaces answer. Beside the early start in occulited.json's
// addons block; occulited writes addon-policy/<id>.delay from it and the occu-addons generator reads
// the file at the next boot - a change restarts nothing.
type StartDelays interface {
	StartDelays() map[string]int
	SetStartDelay(id string, seconds int) error
}

// earlyStartView is GET /early-start and the answer of the PUTs. next_boot says that a change
// takes effect at the next boot. delays are the addons' start delays (task 369), max_delay their
// limit.
func (a *SystemAPI) earlyStartView() map[string]any {
	on, off := a.Early.EarlyStart()
	if off == nil {
		off = []string{}
	}
	delays := map[string]int{}
	if a.Delays != nil {
		for id, s := range a.Delays.StartDelays() {
			if addonunit.ValidateDelay(s) == nil {
				delays[id] = s
			}
		}
	}
	return map[string]any{"enabled": on, "off": off, "next_boot": true, "delays": delays, "max_delay": addonunit.MaxStartDelay}
}

func (a *SystemAPI) registerEarlyStart(mux *http.ServeMux, p string) {
	route(mux, auth.ScopeSystemRead, "GET "+p+"/early-start", a.earlyStartGet)
	route(mux, auth.ScopeAddonsWrite, "PUT "+p+"/early-start", a.earlyStartPut)
	route(mux, auth.ScopeAddonsWrite, "PUT "+p+"/addons/{id}/early-start", a.addonEarlyStartPut)
	route(mux, auth.ScopeAddonsWrite, "PUT "+p+"/addons/{id}/start-delay", a.addonStartDelayPut) // task 369
}

// addonStartDelayPut is PUT /addons/{id}/start-delay {seconds}: the start delay of one addon, 0 to
// MaxStartDelay seconds (0 removes it). It takes effect at the next boot; the answer is the
// early-start view with the delays.
func (a *SystemAPI) addonStartDelayPut(w http.ResponseWriter, r *http.Request) {
	if !a.earlyStartReady(w) {
		return
	}
	if a.Delays == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "this daemon keeps no start delays"})
		return
	}
	if s := SessionFrom(r); s != nil && !s.Has(auth.ScopeAddonsWrite) {
		forbiddenScope(w, auth.ScopeAddonsWrite)
		return
	}
	id := r.PathValue("id")
	if !validAddonID(id) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "invalid addon id"})
		return
	}
	var body struct {
		Seconds *int `json:"seconds"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if body.Seconds == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "seconds is required"})
		return
	}
	if *body.Seconds != 0 {
		if err := addonunit.ValidateDelay(*body.Seconds); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
			return
		}
	}
	if err := a.Delays.SetStartDelay(id, *body.Seconds); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "config", Message: err.Error()})
		return
	}
	a.refreshEarlyStart()
	writeJSON(w, 200, a.earlyStartView())
}

func (a *SystemAPI) earlyStartReady(w http.ResponseWriter) bool {
	if a.Early == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "this daemon was started without a configuration file"})
		return false
	}
	return true
}

// refreshEarlyStart writes the .start files after a switch changed; nothing on a busybox box.
func (a *SystemAPI) refreshEarlyStart() {
	if sa := a.policyManager(); sa != nil {
		sa.RefreshAddonStart()
	}
}

// earlyStartGet is GET /early-start, any session: the global switch and the addons it is off for.
func (a *SystemAPI) earlyStartGet(w http.ResponseWriter, _ *http.Request) {
	if !a.earlyStartReady(w) {
		return
	}
	writeJSON(w, 200, a.earlyStartView())
}

// earlyStartPut is PUT /early-start {enabled, off?}: the global switch, and the per-addon list when
// the body carries one.
func (a *SystemAPI) earlyStartPut(w http.ResponseWriter, r *http.Request) {
	if !a.earlyStartReady(w) {
		return
	}
	if s := SessionFrom(r); s != nil && !s.Has(auth.ScopeAddonsWrite) {
		forbiddenScope(w, auth.ScopeAddonsWrite)
		return
	}
	var body struct {
		Enabled *bool     `json:"enabled"`
		Off     *[]string `json:"off"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	on, off := a.Early.EarlyStart()
	if body.Enabled != nil {
		on = *body.Enabled
	}
	if body.Off != nil {
		off = cleanIDs(*body.Off)
	}
	if err := a.Early.SetEarlyStart(on, off); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "config", Message: err.Error()})
		return
	}
	a.refreshEarlyStart()
	writeJSON(w, 200, a.earlyStartView())
}

// addonEarlyStartPut is PUT /addons/{id}/early-start {enabled}: the switch of one addon. It does
// not touch the global one.
func (a *SystemAPI) addonEarlyStartPut(w http.ResponseWriter, r *http.Request) {
	if !a.earlyStartReady(w) {
		return
	}
	if s := SessionFrom(r); s != nil && !s.Has(auth.ScopeAddonsWrite) {
		forbiddenScope(w, auth.ScopeAddonsWrite)
		return
	}
	id := r.PathValue("id")
	if !validAddonID(id) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "invalid addon id"})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	if body.Enabled == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "enabled is required"})
		return
	}
	if err := a.setAddonEarlyStart(id, *body.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "config", Message: err.Error()})
		return
	}
	out := a.earlyStartView()
	for k, v := range a.addonEarlyStartView(id) {
		out[k] = v
	}
	writeJSON(w, 200, out)
}

// setAddonEarlyStart switches the early start of one addon and renews the .start files.
func (a *SystemAPI) setAddonEarlyStart(id string, enabled bool) error {
	on, off := a.Early.EarlyStart()
	off = slices.DeleteFunc(slices.Clone(off), func(x string) bool { return x == id })
	if !enabled {
		off = append(off, id)
	}
	if err := a.Early.SetEarlyStart(on, cleanIDs(off)); err != nil {
		return err
	}
	a.refreshEarlyStart()
	return nil
}

// addonEarlyStartView is the per-addon part: its own switch (addon_enabled), whether it declares
// the early start, and whether it starts early at the next boot (start_early).
func (a *SystemAPI) addonEarlyStartView(id string) map[string]any {
	out := map[string]any{"id": id, "addon_enabled": true, "start_early_declared": false, "start_early": false}
	if a.Early != nil {
		_, off := a.Early.EarlyStart()
		out["addon_enabled"] = !slices.Contains(off, id)
	}
	if sa := a.policyManager(); sa != nil {
		declared, early := sa.StartEarly(id)
		out["start_early_declared"], out["start_early"] = declared, early
	}
	return out
}
