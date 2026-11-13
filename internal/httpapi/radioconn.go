package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hobbyquaker/occulited/internal/radio"
	"github.com/hobbyquaker/occulited/internal/system"
)

// The radio connections (task 129 phase 3): which local module each interface process uses.

func (a *SystemAPI) radioConnectionsReady(w http.ResponseWriter) bool {
	if a.RadioConnections == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no radio connection service"})
		return false
	}
	return true
}

func (a *SystemAPI) radioConnections(w http.ResponseWriter, r *http.Request) {
	if !a.radioConnectionsReady(w) {
		return
	}
	writeJSON(w, 200, a.RadioConnections.Status())
}

type radioConnBody struct {
	HmIP     string `json:"hmip"`
	BidCos   string `json:"bidcos"`
	HmIPPath string `json:"hmip_path"`
	Confirm  bool   `json:"confirm"`
}

func (b radioConnBody) choices() radio.Choices {
	c := radio.Choices{HmIP: b.HmIP, BidCos: b.BidCos, HmIPPath: b.HmIPPath}
	if c.HmIPPath == "auto" {
		c.HmIPPath = radio.ChoiceAuto
	}
	if c.HmIP == "auto" {
		c.HmIP = radio.ChoiceAuto
	}
	if strings.EqualFold(c.HmIP, radio.HmIPNone) {
		c.HmIP = radio.HmIPNone
	}
	if c.BidCos == "auto" {
		c.BidCos = radio.ChoiceAuto
	}
	return c
}

func radioConnError(w http.ResponseWriter, err error) {
	var confirm *system.ConfirmRequired
	var firmware *system.HmIPFirmwareRefused
	var blocked *system.SnapshotBlocked
	switch {
	case errors.As(err, &blocked):
		// openccu-lite B-301: a kept local-key snapshot of the module; the page offers its discard
		writeJSON(w, http.StatusConflict, apiError{Error: "snapshot-blocked", Message: err.Error(), Detail: map[string]any{"sgtin": blocked.SGTIN}})
	case errors.As(err, &firmware):
		hmipFirmwareError(w, firmware)
	case errors.As(err, &confirm):
		out := map[string]any{"error": "confirm-required", "message": err.Error(), "devices": confirm.Devices}
		if confirm.HmIPMove != nil {
			out["hmip_move"] = confirm.HmIPMove
		}
		if confirm.HmIPOff {
			out["hmip_off"] = true
		}
		writeJSON(w, http.StatusConflict, out)
	case errors.Is(err, system.ErrConnApplyRunning):
		writeJSON(w, http.StatusConflict, apiError{Error: "busy", Message: err.Error()})
	case errors.Is(err, system.ErrConnUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "unavailable", Message: err.Error()})
	default:
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
	}
}

// hmipFirmwareError answers the refusal of an HmIP move onto a module whose firmware cannot take
// the network key (openccu-lite B-289): 422 hmip-firmware, the module, its version and the minimum
// in the detail for the page's own words.
func hmipFirmwareError(w http.ResponseWriter, e *system.HmIPFirmwareRefused) {
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "hmip-firmware", Message: e.Error(), Detail: map[string]any{"module": e.Module, "version": e.Version, "minimum": e.Minimum}})
}

// radioConnectionsPreview answers what the choices would give, without changing anything.
func (a *SystemAPI) radioConnectionsPreview(w http.ResponseWriter, r *http.Request) {
	if !a.radioConnectionsReady(w) {
		return
	}
	var b radioConnBody
	if err := decodeSmall(w, r, &b); err != nil {
		badBody(w, err)
		return
	}
	pv, err := a.RadioConnections.Preview(r.Context(), b.choices())
	if err != nil {
		radioConnError(w, err)
		return
	}
	writeJSON(w, 200, pv)
}

// putRadioConnections writes the choices and starts the change; the page polls GET.
func (a *SystemAPI) putRadioConnections(w http.ResponseWriter, r *http.Request) {
	if !a.radioConnectionsReady(w) {
		return
	}
	var b radioConnBody
	if err := decodeSmall(w, r, &b); err != nil {
		badBody(w, err)
		return
	}
	if _, err := a.RadioConnections.Apply(r.Context(), b.choices(), b.Confirm); err != nil {
		radioConnError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, a.RadioConnections.Status())
}
