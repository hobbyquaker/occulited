package httpapi

import (
	"errors"
	"net/http"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// The caller's own display names in Control (occulited task 73): GET
// /api/auth/v1/me/display-names, PUT and DELETE /api/auth/v1/me/display-names/{id}, any role,
// self-service like /me/preferences. Control does not rename a channel in the metadata store; a
// display name is the user's own, shown only in Control and only to them. The anonymous
// administrator of auth mode off and a token have no account: GET answers the empty map, a write
// 409 no-account, and the shell keeps the names in the browser then.

type displayNamesBody struct {
	DisplayNames map[string]string `json:"display_names"`
}

func (a *AuthAPI) displayNames(w http.ResponseWriter, r *http.Request) {
	m, _ := a.Store.DisplayNames(SessionFrom(r).User)
	writeJSON(w, 200, displayNamesBody{DisplayNames: m})
}

func (a *AuthAPI) putDisplayName(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	a.setDisplayName(w, r, b.Name)
}

func (a *AuthAPI) deleteDisplayName(w http.ResponseWriter, r *http.Request) {
	a.setDisplayName(w, r, "")
}

func (a *AuthAPI) setDisplayName(w http.ResponseWriter, r *http.Request, name string) {
	user := SessionFrom(r).User
	if err := a.Store.SetDisplayName(user, r.PathValue("id"), name); err != nil {
		switch {
		case errors.Is(err, auth.ErrUnknownUser):
			writeJSON(w, http.StatusConflict, apiError{Error: "no-account", Message: "this session has no account to keep display names with"})
		case errors.Is(err, auth.ErrBadDisplayName):
			badBody(w, err)
		default:
			authErr(w, err)
		}
		return
	}
	m, _ := a.Store.DisplayNames(user)
	writeJSON(w, 200, displayNamesBody{DisplayNames: m})
}
