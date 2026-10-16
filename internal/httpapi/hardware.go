package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/bootexpect"
	"github.com/hobbyquaker/occulited/internal/button"
	"github.com/hobbyquaker/occulited/internal/buttonsvc"
	"github.com/hobbyquaker/occulited/internal/system"
)

// openccu-lite task 340: the Hardware page's HDMI switch. GET for every session, PUT for
// administrators; a system without the section (not a Pi, no HDMI connector) answers
// available false, and its PUT 501.

func (a *SystemAPI) registerHardware(mux *http.ServeMux, p string) {
	route(mux, auth.ScopeSystemRead, "GET "+p+"/hardware/hdmi", a.hdmiGet)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/hardware/hdmi", a.hdmiPut)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/hardware/button", a.buttonGet)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/hardware/button", a.buttonPut)
}

func (a *SystemAPI) hdmiGet(w http.ResponseWriter, _ *http.Request) {
	if a.HDMI == nil {
		writeJSON(w, 200, system.HDMIView{Reason: "not-a-pi", Connectors: []system.HDMIConnector{}})
		return
	}
	writeJSON(w, 200, a.HDMI.View())
}

func (a *SystemAPI) hdmiPut(w http.ResponseWriter, r *http.Request) {
	if a.HDMI == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: system.ErrHDMIUnavailable.Error()})
		return
	}
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if b.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "enabled: true or false"})
		return
	}
	v, err := a.HDMI.Switch(r.Context(), *b.Enabled)
	switch {
	case errors.Is(err, system.ErrHDMIUnavailable):
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: err.Error()})
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	reqLog(r).Info("hdmi: switched", "on", *b.Enabled)
	writeJSON(w, 200, v)
}

// openccu-lite task 164: the RPI-RF-MOD's button. GET for every session, PUT for administrators.
// The section shows where the button can be read (the module on the Pi's header); elsewhere the
// answer says why not.

// ButtonService is the daemon's half of the button (internal/buttonsvc).
type ButtonService interface {
	State() buttonsvc.State
}

// buttonView is GET /hardware/button.
type buttonView struct {
	buttonsvc.State
	Settings button.Settings `json:"settings"`
	// Actions and LongActions are what the gestures may be set to.
	Actions     []string `json:"actions"`
	LongActions []string `json:"long_actions"`
	// Recovery: whether the system has a recovery system (not a container).
	Recovery bool `json:"recovery"`
}

func (a *SystemAPI) buttonView() buttonView {
	v := buttonView{Actions: button.Actions, LongActions: button.LongActions, Recovery: a.Root.Container() == ""}
	if a.Button != nil {
		v.State = a.Button.State()
	} else {
		v.Reason = "no-module"
	}
	set := button.DefaultSettings()
	if a.ButtonSettings != nil {
		if s, err := a.ButtonSettings().Normalize(); err == nil {
			set = s
		}
	}
	v.Settings = set
	return v
}

func (a *SystemAPI) buttonGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, a.buttonView())
}

func (a *SystemAPI) buttonPut(w http.ResponseWriter, r *http.Request) {
	if a.OnButtonSettings == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the button's settings cannot be saved here"})
		return
	}
	var s button.Settings
	if err := readJSON(r, &s); err != nil {
		badBody(w, err)
		return
	}
	n, err := s.Normalize()
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	if err := a.OnButtonSettings(n); err != nil {
		writeErr(w, err)
		return
	}
	reqLog(r).Info("button: settings changed", "single", n.Single, "double", n.Double, "long", n.Long, "long_s", n.LongS, "mute_minutes", n.MuteMinutes)
	writeJSON(w, 200, a.buttonView())
}

// RebootIntoRecovery is POST /reboot/recovery's steps for the button (task 164): the marker, the
// boot-timing mark, the reboot; the marker taken back when the reboot does not start.
func (a *SystemAPI) RebootIntoRecovery() error {
	if a.Manager == nil {
		return errors.New("no addon manager")
	}
	if err := a.Root.ArmRecovery(); err != nil {
		return err
	}
	a.markBoot(bootexpect.KindRecovery)
	if err := a.Manager.Reboot(context.Background()); err != nil {
		_ = a.Root.DisarmRecovery()
		a.unmarkBoot()
		return err
	}
	slog.Warn("power: the button reboots into the recovery system")
	return nil
}

// RebootNow is POST /reboot's steps for the button (task 164).
func (a *SystemAPI) RebootNow() error {
	if a.Manager == nil {
		return errors.New("no addon manager")
	}
	kind := bootexpect.KindReboot
	if u := a.Root.StagedSystemUpdate(); u != nil && u.RecoveryArmed {
		kind = bootexpect.KindUpdate
	}
	a.markBoot(kind)
	if err := a.Manager.Reboot(context.Background()); err != nil {
		a.unmarkBoot()
		return err
	}
	slog.Warn("power: the button reboots the system")
	return nil
}
