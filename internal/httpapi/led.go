package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/led"
)

// Task 95: the status LED. The controller is internal/led; these are its routes. GET /led and
// GET /led/state for every session, the configuration and the preview for administrators, the
// overrides and locate for administrators and tokens with the role led (auth.go's ledRoleRoute).

func (a *SystemAPI) registerLED(mux *http.ServeMux, p string) {
	route(mux, auth.ScopeSystemRead, "GET "+p+"/led", a.ledView)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/led", a.ledPut)
	route(mux, auth.ScopeLED, "GET "+p+"/led/state", a.ledState, auth.ScopeSystemRead)
	route(mux, auth.ScopeLED, "POST "+p+"/led/override", a.ledOverride)
	route(mux, auth.ScopeLED, "DELETE "+p+"/led/override", a.ledOverrideClear)
	route(mux, auth.ScopeLED, "DELETE "+p+"/led/override/{id}", a.ledOverrideClear)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/led/overrides", a.ledOverridesClear)
	route(mux, auth.ScopeLED, "POST "+p+"/led/locate", a.ledLocate)
	route(mux, auth.ScopeLED, "DELETE "+p+"/led/locate", a.ledLocateStop)
	route(mux, auth.ScopeSystemWrite, "POST "+p+"/led/preview", a.ledPreview)
	route(mux, auth.ScopeSystemWrite, "DELETE "+p+"/led/preview", a.ledPreviewStop)
	route(mux, auth.ScopeLED, "POST "+p+"/led/mute", a.ledMute)
	route(mux, auth.ScopeLED, "DELETE "+p+"/led/mute", a.ledUnmute)
}

// ledMute is POST /led/mute {minutes?} (openccu-lite task 164): the warning states silenced for a
// while, as the button's press does; 60 minutes without a body.
func (a *SystemAPI) ledMute(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	if _, admin, ledRole := ledCaller(r); !admin && !ledRole {
		forbiddenScope(w, auth.ScopeLED)
		return
	}
	var body struct {
		Minutes int `json:"minutes"`
	}
	if !ledBody(w, r, &body) {
		return
	}
	if _, err := a.LED.Mute(time.Duration(body.Minutes) * time.Minute); err != nil {
		ledErr(w, err)
		return
	}
	writeJSON(w, 200, a.LED.State())
}

func (a *SystemAPI) ledUnmute(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	if _, admin, ledRole := ledCaller(r); !admin && !ledRole {
		forbiddenScope(w, auth.ScopeLED)
		return
	}
	a.LED.Unmute()
	writeJSON(w, 200, a.LED.State())
}

func (a *SystemAPI) ledReady(w http.ResponseWriter) bool {
	if a.LED == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no status LED controller"})
		return false
	}
	return true
}

// ledCaller is who asks: the session's user ("token:<name>" for a token), whether it may
// configure the LED and clear anyone's override (system:write), whether it holds the scope led
// (an override of its own, locate).
func ledCaller(r *http.Request) (by string, admin, ledRole bool) {
	s := SessionFrom(r)
	if s == nil {
		return "", false, false
	}
	return s.User, s.Has(auth.ScopeSystemWrite), s.Has(auth.ScopeLED)
}

func ledErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, led.ErrInvalid):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
	case errors.Is(err, led.ErrUnavailable):
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: err.Error()})
	case errors.Is(err, led.ErrRateLimited):
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "rate-limited", Message: err.Error()})
	case errors.Is(err, led.ErrLimit):
		writeJSON(w, http.StatusConflict, apiError{Error: "limit", Message: err.Error()})
	case errors.Is(err, led.ErrForbidden):
		writeJSON(w, http.StatusForbidden, apiError{Error: "forbidden", Message: err.Error()})
	case errors.Is(err, led.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-found", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
	}
}

// ledBody reads an optional JSON body: an empty one is the zero value.
func ledBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	if err := readJSON(r, v); err != nil {
		badBody(w, err)
		return false
	}
	return true
}

func (a *SystemAPI) ledView(w http.ResponseWriter, _ *http.Request) {
	if !a.ledReady(w) {
		return
	}
	writeJSON(w, 200, a.LED.View())
}

func (a *SystemAPI) ledPut(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	if _, admin, _ := ledCaller(r); !admin {
		forbidden(w)
		return
	}
	var cfg led.Config
	if err := readJSON(r, &cfg); err != nil {
		badBody(w, err)
		return
	}
	v, err := a.LED.SetConfig(cfg)
	if err != nil {
		ledErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *SystemAPI) ledState(w http.ResponseWriter, _ *http.Request) {
	if !a.ledReady(w) {
		return
	}
	writeJSON(w, 200, a.LED.State())
}

// ledOverrideAnswer is the state with the override just set.
type ledOverrideAnswer struct {
	led.StateView
	Override led.Override `json:"override"`
}

func (a *SystemAPI) ledOverride(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	by, admin, ledRole := ledCaller(r)
	if !admin && !ledRole {
		forbiddenScope(w, auth.ScopeLED)
		return
	}
	var body led.OverrideRequest
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	o, err := a.LED.SetOverride(by, admin, body)
	if err != nil {
		ledErr(w, err)
		return
	}
	writeJSON(w, 200, ledOverrideAnswer{StateView: a.LED.State(), Override: o})
}

func (a *SystemAPI) ledOverrideClear(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	by, admin, ledRole := ledCaller(r)
	if !admin && !ledRole {
		forbiddenScope(w, auth.ScopeLED)
		return
	}
	if err := a.LED.ClearOverride(by, r.PathValue("id"), admin); err != nil {
		ledErr(w, err)
		return
	}
	writeJSON(w, 200, a.LED.State())
}

func (a *SystemAPI) ledOverridesClear(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	if _, admin, _ := ledCaller(r); !admin {
		forbidden(w)
		return
	}
	a.LED.ClearOverrides()
	writeJSON(w, 200, a.LED.State())
}

func (a *SystemAPI) ledLocate(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	if _, admin, ledRole := ledCaller(r); !admin && !ledRole {
		forbiddenScope(w, auth.ScopeLED)
		return
	}
	var body struct {
		DurationS int `json:"duration_s"`
	}
	if !ledBody(w, r, &body) {
		return
	}
	if _, err := a.LED.StartLocate(body.DurationS); err != nil {
		ledErr(w, err)
		return
	}
	writeJSON(w, 200, a.LED.State())
}

func (a *SystemAPI) ledLocateStop(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	if _, admin, ledRole := ledCaller(r); !admin && !ledRole {
		forbiddenScope(w, auth.ScopeLED)
		return
	}
	if err := a.LED.StopLocate(); err != nil {
		ledErr(w, err)
		return
	}
	writeJSON(w, 200, a.LED.State())
}

func (a *SystemAPI) ledPreview(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	if _, admin, _ := ledCaller(r); !admin {
		forbidden(w)
		return
	}
	var look led.Look
	if err := readJSON(r, &look); err != nil {
		badBody(w, err)
		return
	}
	if _, err := a.LED.StartPreview(look); err != nil {
		ledErr(w, err)
		return
	}
	writeJSON(w, 200, a.LED.State())
}

func (a *SystemAPI) ledPreviewStop(w http.ResponseWriter, r *http.Request) {
	if !a.ledReady(w) {
		return
	}
	if _, admin, _ := ledCaller(r); !admin {
		forbidden(w)
		return
	}
	if err := a.LED.StopPreview(); err != nil {
		ledErr(w, err)
		return
	}
	writeJSON(w, 200, a.LED.State())
}
