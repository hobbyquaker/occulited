package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/manifest"
	"github.com/hobbyquaker/occulited/internal/system"
	"github.com/hobbyquaker/occulited/internal/warnings"
)

// The addons' part in the system backup (openccu-lite task 399, system/backuphooks.go): the
// declared hooks with their switches and last results, and the switch per addon.

func (a *SystemAPI) registerBackupAddons(mux *http.ServeMux, p string) {
	route(mux, auth.ScopeSystemRead, "GET "+p+"/backup/addons", a.backupAddonsGet)
	route(mux, auth.ScopeSystemWrite, "PUT "+p+"/backup/addons/{id}", a.backupAddonPut)
}

// backupAddonsView is GET /backup/addons and the PUT's answer.
func (a *SystemAPI) backupAddonsView() map[string]any {
	list := a.BackupHooks.Declared()
	if list == nil {
		list = []system.BackupHookAddon{}
	}
	return map[string]any{"supported": a.BackupHooks.Supported(), "large_mb": manifest.BackupLargeMB, "addons": list}
}

func (a *SystemAPI) backupAddonsGet(w http.ResponseWriter, _ *http.Request) {
	if a.BackupHooks == nil {
		writeJSON(w, 200, map[string]any{"supported": false, "large_mb": manifest.BackupLargeMB, "addons": []system.BackupHookAddon{}})
		return
	}
	writeJSON(w, 200, a.backupAddonsView())
}

// backupAddonPut is PUT /backup/addons/{id} {enabled}: whether the addon's snapshot is taken with
// every backup. 404 for an addon that declares no backup block.
func (a *SystemAPI) backupAddonPut(w http.ResponseWriter, r *http.Request) {
	if a.BackupHooks == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "this daemon keeps no backup hooks"})
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
	if err := a.BackupHooks.SetEnabled(id, *body.Enabled); err != nil {
		if errors.Is(err, system.ErrNoBackupBlock) {
			writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Message: "the addon " + id + " declares no backup block"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "config", Message: err.Error()})
		return
	}
	reqLog(r).Info("backup: an addon's snapshot switched", "addon", id, "enabled", *body.Enabled)
	writeJSON(w, 200, a.backupAddonsView())
}

// backupAddonWarning is the Status warning backup-addon: an addon whose snapshot is switched on and
// whose last hook run failed - the backups since then carry no snapshot of its data.
func (a *SystemAPI) backupAddonWarning(context.Context) ([]warnings.Warning, bool) {
	if a.BackupHooks == nil {
		return nil, true
	}
	failed := a.BackupHooks.Failed()
	if len(failed) == 0 {
		return nil, true
	}
	list := make([]warnAddon, 0, len(failed))
	for _, f := range failed {
		list = append(list, warnAddon{ID: f.ID, Name: f.Name.In("en"), Enabled: true})
	}
	return []warnings.Warning{addonListWarning("backup-addon", warnings.SeverityWarning, "/backup#addons", list)}, true
}
