package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/appviews"
	"github.com/hobbyquaker/occulited/internal/auth"
)

// occulited task 125: the addons' views in the Control app (internal/appviews). The addon's
// routes take its control token - /run/occulite/addon-tokens/<id>, as the addon notices of task
// 119 do - and reach that addon's registration alone:
//
//	PUT    /addon-views   {group: {de, en}, views: [{id, title: {de, en}, path, icon?}]} - the whole list
//	DELETE /addon-views   clears it
//	GET    /addon-views   the addon's registration as kept
//
// The App reads every running addon's groups from GET /app-views (any session, the public mode
// too).

const (
	addonViewsPath = "/api/system/v1/addon-views"
	appViewsPath   = "/api/system/v1/app-views"
)

func (a *SystemAPI) registerAddonViews(mux *http.ServeMux, p string) {
	route(mux, scopeOpen, "PUT "+p+"/addon-views", a.addonViewsPut)
	route(mux, scopeOpen, "DELETE "+p+"/addon-views", a.addonViewsDelete)
	route(mux, scopeOpen, "GET "+p+"/addon-views", a.addonViewsGet)
	route(mux, auth.ScopeSystemRead, "GET "+p+"/app-views", a.appViews)
}

// viewsAddon checks the control token and answers its addon; false has answered.
func (a *SystemAPI) viewsAddon(w http.ResponseWriter, r *http.Request) (string, bool) {
	if a.AddonViews == nil || a.AddonCtl == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "addon views need the systemd product"})
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

func (a *SystemAPI) addonViewsPut(w http.ResponseWriter, r *http.Request) {
	addon, ok := a.viewsAddon(w, r)
	if !ok {
		return
	}
	var in appviews.Registration
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad-request", Message: err.Error()})
		return
	}
	changed, err := a.AddonViews.Put(addon, in)
	switch {
	case errors.Is(err, appviews.ErrInvalid):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	if changed {
		reqLog(r).Info("addon views: registered", "addon", addon, "views", len(in.Views))
	}
	e, _ := a.AddonViews.Get(addon)
	writeJSON(w, 200, map[string]any{"addon": addon, "changed": changed, "group": e.Group, "views": viewsOrEmpty(e.Views), "max": appviews.MaxViews})
}

func (a *SystemAPI) addonViewsDelete(w http.ResponseWriter, r *http.Request) {
	addon, ok := a.viewsAddon(w, r)
	if !ok {
		return
	}
	changed, err := a.AddonViews.Delete(addon)
	if err != nil {
		writeErr(w, err)
		return
	}
	if changed {
		reqLog(r).Info("addon views: cleared", "addon", addon)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "changed": changed})
}

func (a *SystemAPI) addonViewsGet(w http.ResponseWriter, r *http.Request) {
	addon, ok := a.viewsAddon(w, r)
	if !ok {
		return
	}
	e, _ := a.AddonViews.Get(addon)
	writeJSON(w, 200, map[string]any{"addon": addon, "group": e.Group, "views": viewsOrEmpty(e.Views), "max": appviews.MaxViews})
}

func viewsOrEmpty(v []appviews.View) []appviews.View {
	if v == nil {
		return []appviews.View{}
	}
	return v
}

// appViewGroup is one addon's entry in the App's drawer.
type appViewGroup struct {
	Addon string            `json:"addon"`
	Name  map[string]string `json:"name"`
	Views []appviews.View   `json:"views"`
}

// appViews answers the running addons' groups, ordered by the addon's name: an addon whose unit
// does not run has no entry, so the drawer never offers a page that would not answer.
func (a *SystemAPI) appViews(w http.ResponseWriter, r *http.Request) {
	out := []appViewGroup{}
	if a.AddonViews != nil {
		all := a.AddonViews.All()
		running := a.runningAddons()
		names := a.Root.InstalledAddonNames()
		for id, e := range all {
			if running != nil && !running[id] {
				continue
			}
			out = append(out, appViewGroup{Addon: id, Name: e.Group, Views: e.Views})
		}
		sort.Slice(out, func(i, j int) bool {
			ni, nj := strings.ToLower(names[out[i].Addon]), strings.ToLower(names[out[j].Addon])
			if ni != nj {
				return ni < nj
			}
			return out[i].Addon < out[j].Addon
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"groups": out})
}

// runningCache keeps the addon units' running state for ten seconds.
type runningCache struct {
	mu    sync.Mutex
	at    time.Time
	value map[string]bool
}

// runningAddons answers addon id → its unit runs, from the services listing; nil when there is
// none to ask (the development mode: every addon counts as running).
func (a *SystemAPI) runningAddons() map[string]bool {
	if a.Services == nil {
		return nil
	}
	c := &a.addonsRunning
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != nil && time.Since(c.at) < 10*time.Second {
		return c.value
	}
	list, err := a.Services.List()
	if err != nil {
		return c.value // the last answer, or every addon
	}
	if len(list) == 0 {
		return nil // no systemd: nothing is listed
	}
	m := map[string]bool{}
	for _, s := range list {
		if id, ok := strings.CutPrefix(s.ID, "addon-"); ok && s.Kind == "addon" {
			m[id] = s.Running
		}
	}
	c.value, c.at = m, time.Now()
	return m
}
