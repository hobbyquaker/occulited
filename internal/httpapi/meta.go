// Package httpapi is the HTTP surface of occulited: docs/meta-api.md under /api/meta/v1/, the
// system API under /api/system/v1/ and the health endpoint. Thin by design: parse, call, encode.
package httpapi

import (
	"github.com/hobbyquaker/occulited/internal/auth"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/radio"
	"github.com/hobbyquaker/occulited/internal/regaimport"
	"github.com/hobbyquaker/occulited/internal/system"

	"gopkg.in/yaml.v3"

	"github.com/hobbyquaker/occulited/internal/meta"
)

// Implementation names the server in /version: "occulited <version>", the version being the commit
// occulited was built from (occulited task 16).
var Implementation = "occulited dev"

// Commit is the commit the server was built from, the bare hash, answered beside Implementation;
// empty for a build that carries none.
var Commit = ""

const maxBody = 1 << 20 // 1 MiB; an import of a large house is well under 200 KB

// MetaAPI serves the metadata API for one store.
type MetaAPI struct {
	Store *meta.Store
	// AfterImport runs after a regadom or CCU import wrote the store (task 193: the favorites sync
	// hands an imported page to the account of its name); nil = nothing.
	AfterImport func()
	// Root is the filesystem root the regadom import reads from ("/" on a box).
	Root string
	// HmIPPairing (task 192) is what /version says about HmIP pairing - the key-server mode and
	// the count of device keys, never a key - for a client that has no system scope; nil = the
	// answer has no `hmip`, which is how a client tells an older system apart.
	HmIPPairing func() radio.Pairing
	// Capabilities is /version's capability object (openccu-lite tasks 194, 195, 219): what a
	// client can use on this system - pairing (on or off), the state store, the datapoint history
	Capabilities func() map[string]any
	// KnownInterface says whether an interface is one of this system's (InterfacesList.xml): the
	// ReGa imports leave out the objects of any other, as they do ReGa's System (occulited B-84);
	// nil leaves out only System
	KnownInterface func(string) bool
	// Interfaces names the interfaces InterfacesList.xml lists now (none when no list was read):
	// what GET /foreign and POST /foreign:remove measure the store against (occulited B-84); nil =
	// no list, so nothing is offered for removal
	Interfaces func() []string
}

// Register mounts the routes on mux.
func (a *MetaAPI) Register(mux *http.ServeMux) {
	p := "/api/meta/v1"
	route(mux, scopeOpen, "GET "+p+"/version", a.version)
	route(mux, auth.ScopeMetaRead, "GET "+p+"/snapshot", a.snapshot)
	route(mux, auth.ScopeMetaRead, "GET "+p+"/objects", a.listObjects)
	route(mux, auth.ScopeMetaWrite, "POST "+p+"/objects:bulk", a.bulk)
	route(mux, auth.ScopeMetaRead, "GET "+p+"/objects/{ref}", a.getObject)
	route(mux, auth.ScopeMetaWrite, "PUT "+p+"/objects/{ref}", a.putObject)
	route(mux, auth.ScopeMetaWrite, "PATCH "+p+"/objects/{ref}", a.patchObject)
	route(mux, auth.ScopeMetaWrite, "DELETE "+p+"/objects/{ref}", a.deleteObject)
	route(mux, auth.ScopeMetaRead, "GET "+p+"/enums", a.listEnums)
	route(mux, auth.ScopeMetaWrite, "POST "+p+"/enums", a.createEnum)
	route(mux, auth.ScopeMetaWrite, "PATCH "+p+"/enums/{enum}", a.updateEnum)
	route(mux, auth.ScopeMetaWrite, "DELETE "+p+"/enums/{enum}", a.deleteEnum)
	route(mux, auth.ScopeMetaRead, "GET "+p+"/enums/{enum}/tree", a.tree)
	route(mux, auth.ScopeMetaWrite, "POST "+p+"/enums/{enum}/nodes", a.createNode)
	route(mux, auth.ScopeMetaWrite, "PATCH "+p+"/enums/{enum}/nodes/{path...}", a.updateNode)
	route(mux, auth.ScopeMetaWrite, "DELETE "+p+"/enums/{enum}/nodes/{path...}", a.deleteNode)
	route(mux, auth.ScopeMetaRead, "GET "+p+"/export", a.export)
	route(mux, auth.ScopeMetaWrite, "PUT "+p+"/import", a.importDoc)
	route(mux, auth.ScopeMetaWrite, "POST "+p+"/import/ccu", a.importCCU)
	route(mux, auth.ScopeMetaWrite, "POST "+p+"/import/regadom", a.importRegadom)
	route(mux, auth.ScopeMetaRead, "GET "+p+"/foreign", a.foreign)
	route(mux, auth.ScopeMetaWrite, "POST "+p+"/foreign:remove", a.removeForeign)
	route(mux, auth.ScopeMetaRead, "GET "+p+"/events/sse", a.sse)
}

// --- helpers ---

type apiError struct {
	Error   string         `json:"error"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
	// Scope names the scope a 403 is missing (task 66).
	Scope string `json:"scope,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var me *meta.Error
	if errors.As(err, &me) {
		writeJSON(w, me.Code.HTTPStatus(), apiError{Error: string(me.Code), Message: me.Message, Detail: me.Detail})
		return
	}
	writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal", Message: err.Error()})
}

// smallBody is the limit of a JSON body of a few fields (B-232): more is 413 before the decoder
// has grown the heap by it.
const smallBody = 64 << 10

func badBody(w http.ResponseWriter, err error) {
	if bodyTooLarge(w, err) {
		return
	}
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-body", Message: err.Error()})
}

// bodyTooLarge answers 413 too-large with the limit when err is a body over its route's limit
// (http.MaxBytesReader's error), and reports whether it did.
func bodyTooLarge(w http.ResponseWriter, err error) bool {
	var mb *http.MaxBytesError
	if !errors.As(err, &mb) {
		return false
	}
	writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: "too-large", Message: fmt.Sprintf("the body exceeds %d bytes", mb.Limit), Detail: map[string]any{"limit": mb.Limit}})
	return true
}

// decodeSmall decodes a JSON body of a few fields, at most smallBody bytes (B-232). Every JSON
// body goes through this, readJSON or a MaxBytesReader of its own; TestNoUnboundedBody keeps it so.
func decodeSmall(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, smallBody)).Decode(v)
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(b) > maxBody {
		return &http.MaxBytesError{Limit: maxBody}
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return fmt.Errorf("empty body")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ifMatch parses the optional If-Match header as a revision.
func ifMatch(r *http.Request) (*uint64, error) {
	h := strings.Trim(r.Header.Get("If-Match"), `" `)
	if h == "" {
		return nil, nil
	}
	n, err := strconv.ParseUint(h, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("If-Match must be a revision number")
	}
	return &n, nil
}

// mutated writes the standard response of a mutating call.
func mutated(w http.ResponseWriter, rev uint64, changed bool, err error, status int) {
	if err != nil {
		writeErr(w, err)
		return
	}
	if !changed {
		w.Header().Set("ETag", strconv.FormatUint(rev, 10))
		writeJSON(w, http.StatusNotModified, map[string]any{"revision": rev})
		return
	}
	w.Header().Set("ETag", strconv.FormatUint(rev, 10))
	writeJSON(w, status, map[string]any{"revision": rev})
}

// --- objects ---

func (a *MetaAPI) version(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"api": "meta", "version": 1, "format": meta.Format, "revision": a.Store.Revision(), "implementation": Implementation}
	if Commit != "" {
		out["commit"] = Commit
	}
	if a.HmIPPairing != nil {
		out["hmip"] = a.HmIPPairing()
	}
	if a.Capabilities != nil {
		out["capabilities"] = a.Capabilities()
	}
	writeJSON(w, 200, out)
}

func (a *MetaAPI) snapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, a.Store.Snapshot())
}

func (a *MetaAPI) listObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var orphaned *bool
	if v := q.Get("orphaned"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badBody(w, fmt.Errorf("orphaned must be true or false"))
			return
		}
		orphaned = &b
	}
	refs, err := a.Store.Query(q.Get("enum"), orphaned)
	if err != nil {
		writeErr(w, err)
		return
	}
	snap := a.Store.Snapshot()
	objs := make(map[string]*meta.Object, len(refs))
	for _, ref := range refs {
		objs[ref] = snap.Objects[ref]
	}
	writeJSON(w, 200, map[string]any{"revision": snap.Revision, "objects": objs})
}

func (a *MetaAPI) getObject(w http.ResponseWriter, r *http.Request) {
	o, err := a.Store.GetObject(r.PathValue("ref"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": a.Store.Revision(), "ref": r.PathValue("ref"), "object": o})
}

// ParsePatch turns a JSON body into an ObjectPatch; `orphaned` in a client body is forbidden.
func ParsePatch(raw map[string]json.RawMessage) (meta.ObjectPatch, error) {
	if _, ok := raw["orphaned"]; ok {
		return meta.ObjectPatch{}, &meta.Error{Code: meta.ErrForbidden, Message: "orphaned is set by the system, not by clients"}
	}
	var p meta.ObjectPatch
	for k, v := range raw {
		switch k {
		case "name":
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return p, &meta.Error{Code: meta.ErrInvalidBody, Message: "name must be a string"}
			}
			p.Name = &s
		case "enums":
			var e []string
			if err := json.Unmarshal(v, &e); err != nil {
				return p, &meta.Error{Code: meta.ErrInvalidBody, Message: "enums must be an array of paths"}
			}
			p.Enums = &e
		case "meta":
			var m map[string]json.RawMessage
			if err := json.Unmarshal(v, &m); err != nil {
				return p, &meta.Error{Code: meta.ErrInvalidBody, Message: "meta must be an object"}
			}
			p.Meta = map[string]json.RawMessage{}
			for ns, x := range m {
				if string(x) == "null" {
					p.Meta[ns] = nil
				} else {
					p.Meta[ns] = x
				}
			}
		default:
			return p, &meta.Error{Code: meta.ErrInvalidBody, Message: fmt.Sprintf("unknown field %q", k)}
		}
	}
	return p, nil
}

func (a *MetaAPI) patchObject(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		badBody(w, err)
		return
	}
	p, err := ParsePatch(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	rev, changed, err := a.Store.SetObject(im, r.PathValue("ref"), p)
	mutated(w, rev, changed, err, 200)
}

func (a *MetaAPI) putObject(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	var b struct {
		Name  string                     `json:"name"`
		Enums []string                   `json:"enums"`
		Meta  map[string]json.RawMessage `json:"meta"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	rev, changed, err := a.Store.PutObject(im, r.PathValue("ref"), b.Name, b.Enums, b.Meta)
	mutated(w, rev, changed, err, 200)
}

func (a *MetaAPI) deleteObject(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	rev, changed, err := a.Store.DeleteObject(im, r.PathValue("ref"))
	mutated(w, rev, changed, err, 200)
}

func (a *MetaAPI) bulk(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	var b struct {
		Set    map[string]map[string]json.RawMessage `json:"set"`
		Delete []string                              `json:"delete"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	set := make(map[string]meta.ObjectPatch, len(b.Set))
	for ref, raw := range b.Set {
		p, err := ParsePatch(raw)
		if err != nil {
			writeErr(w, err)
			return
		}
		set[ref] = p
	}
	rev, changed, err := a.Store.Bulk(im, set, b.Delete)
	mutated(w, rev, changed, err, 200)
}

// --- enums and nodes ---

func (a *MetaAPI) listEnums(w http.ResponseWriter, _ *http.Request) {
	snap := a.Store.Snapshot()
	writeJSON(w, 200, map[string]any{"revision": snap.Revision, "enums": snap.Enums})
}

func (a *MetaAPI) createEnum(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	var b struct {
		ID   string            `json:"id"`
		Name map[string]string `json:"name"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	rev, changed, err := a.Store.CreateEnum(im, b.ID, b.Name)
	mutated(w, rev, changed, err, http.StatusCreated)
}

func (a *MetaAPI) updateEnum(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	var b struct {
		Name map[string]string `json:"name"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	rev, changed, err := a.Store.UpdateEnum(im, r.PathValue("enum"), b.Name)
	mutated(w, rev, changed, err, 200)
}

func (a *MetaAPI) deleteEnum(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	rev, changed, err := a.Store.DeleteEnum(im, r.PathValue("enum"), r.URL.Query().Get("members") == "detach")
	mutated(w, rev, changed, err, 200)
}

func (a *MetaAPI) tree(w http.ResponseWriter, r *http.Request) {
	snap := a.Store.Snapshot()
	e, ok := snap.Enums[r.PathValue("enum")]
	if !ok {
		writeErr(w, &meta.Error{Code: meta.ErrUnknownEnum, Message: "enum does not exist"})
		return
	}
	writeJSON(w, 200, map[string]any{"revision": snap.Revision, "enum": r.PathValue("enum"), "name": e.Name, "tree": e.Tree})
}

func (a *MetaAPI) createNode(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	var b struct {
		Parent   *string `json:"parent"`
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		Icon     string  `json:"icon"`
		Position *int    `json:"position"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	rev, changed, err := a.Store.CreateNode(im, r.PathValue("enum"), b.Parent, b.ID, b.Name, b.Icon, b.Position)
	mutated(w, rev, changed, err, http.StatusCreated)
}

func (a *MetaAPI) updateNode(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		badBody(w, err)
		return
	}
	var p meta.NodePatch
	for k, v := range raw {
		switch k {
		case "name":
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				badBody(w, fmt.Errorf("name must be a string"))
				return
			}
			p.Name = &s
		case "icon":
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				badBody(w, fmt.Errorf("icon must be a string"))
				return
			}
			p.Icon = &s
		case "parent":
			if string(v) == "null" {
				empty := ""
				p.Parent = &empty
			} else {
				var s string
				if err := json.Unmarshal(v, &s); err != nil {
					badBody(w, fmt.Errorf("parent must be a path or null"))
					return
				}
				p.Parent = &s
			}
		case "position":
			var n int
			if err := json.Unmarshal(v, &n); err != nil {
				badBody(w, fmt.Errorf("position must be an integer"))
				return
			}
			p.Position = &n
		default:
			badBody(w, fmt.Errorf("unknown field %q", k))
			return
		}
	}
	path := r.PathValue("enum") + "/" + r.PathValue("path")
	rev, changed, err := a.Store.UpdateNode(im, path, p)
	mutated(w, rev, changed, err, 200)
}

func (a *MetaAPI) deleteNode(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	path := r.PathValue("enum") + "/" + r.PathValue("path")
	rev, changed, err := a.Store.DeleteNode(im, path, r.URL.Query().Get("members") == "detach")
	mutated(w, rev, changed, err, 200)
}

// --- import / export ---

func (a *MetaAPI) export(w http.ResponseWriter, r *http.Request) {
	snap := a.Store.Snapshot()
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Query().Get("format") {
	case "", "json":
		b, _ := meta.Marshal(snap)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="meta.json"`)
		_, _ = w.Write(append(b, '\n'))
	case "yaml":
		// go through JSON so that RawMessage meta values become plain YAML
		var generic any
		b, _ := json.Marshal(snap)
		_ = json.Unmarshal(b, &generic)
		out, err := yaml.Marshal(generic)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="meta.yaml"`)
		_, _ = w.Write(out)
	default:
		// The HM-Script export is gone (maintainer, 2026-09-08). It existed to replay names onto a
		// CCU after switching back, which was part of a promise openccu-lite no longer makes: the
		// way back is restoring the backup taken before the migration, not carrying pieces across.
		badBody(w, fmt.Errorf("format must be json or yaml"))
	}
}

func (a *MetaAPI) importDoc(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		badBody(w, fmt.Errorf("body missing or larger than %d bytes", maxBody))
		return
	}
	var doc meta.Document
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "yaml") {
		var generic any
		if err := yaml.Unmarshal(body, &generic); err != nil {
			badBody(w, err)
			return
		}
		b, err := json.Marshal(yamlToJSON(generic))
		if err != nil {
			badBody(w, err)
			return
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			badBody(w, err)
			return
		}
	} else if err := json.Unmarshal(body, &doc); err != nil {
		badBody(w, err)
		return
	}
	mode := meta.ImportReplace
	switch r.URL.Query().Get("mode") {
	case "", "replace":
	case "merge":
		mode = meta.ImportMerge
	default:
		badBody(w, fmt.Errorf("mode must be replace or merge"))
		return
	}
	rev, changed, err := a.Store.Import(im, &doc, mode)
	mutated(w, rev, changed, err, 200)
}

// yamlToJSON converts yaml.v3's map[string]any / map[any]any trees into JSON-encodable ones.
func yamlToJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = yamlToJSON(x)
		}
		return t
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = yamlToJSON(x)
		}
		return m
	case []any:
		for i, x := range t {
			t[i] = yamlToJSON(x)
		}
		return t
	default:
		return v
	}
}

// --- change stream (SSE) ---

func (a *MetaAPI) sse(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	// a comment line at once: clients see "stream established" without waiting for the first
	// event or the heartbeat (node-red-contrib-ccu's port found the 30 s gap)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// subscribe before replaying so nothing is lost in between
	ch := a.Store.Subscribe()
	defer a.Store.Unsubscribe(ch)
	if s := r.URL.Query().Get("since"); s != "" {
		since, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			since = -1
		}
		events, ok := a.Store.EventsSince(since)
		if !ok {
			send(map[string]any{"kind": "resync", "revision": a.Store.Revision()})
		} else {
			for _, e := range events {
				send(e)
			}
		}
	}
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			send(e)
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// importRegadom reads names, rooms and functions out of a ReGa database (D-35): the box's own
// /etc/config/homematic.regadom left behind by an update from OpenCCU, an uploaded regadom or
// .sbk, or a backup already uploaded to /restore/check. JSON body {source: "box" | "restore:<file>",
// mode, dry_run} or multipart "file" (a regadom or an .sbk) with ?mode=&dry_run=.
func (a *MetaAPI) importRegadom(w http.ResponseWriter, r *http.Request) {
	var (
		dump   *regaimport.Dump
		err    error
		mode   = "merge"
		dryRun bool
	)
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/") {
		mr, merr := r.MultipartReader()
		if merr != nil {
			badBody(w, merr)
			return
		}
		mode, dryRun = r.URL.Query().Get("mode"), r.URL.Query().Get("dry_run") == "true"
		var (
			part *multipart.Part
			name string
		)
		for {
			p, perr := mr.NextPart()
			if perr != nil {
				break
			}
			if p.FormName() == "file" {
				part, name = p, p.FileName()
				break
			}
		}
		if part == nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "no file part"})
			return
		}
		// B-255: stage the upload on the userfs, not /tmp (a tmpfs = RAM), with a cap - a regadom is
		// a few tens of MB, an .sbk up to a backup's 2 GiB - so a caller cannot fill the RAM until
		// the OOM killer takes hmipserver's JVM. The copy error is not swallowed, and a body over
		// the cap is refused with 413 instead of parsed as if it were whole.
		isSBK := strings.HasSuffix(strings.ToLower(name), ".sbk")
		limit := int64(system.MaxRegadomUpload)
		if isSBK {
			limit = system.MaxSBKUpload
		}
		path, _, serr := system.StageUpload(system.Root(a.Root), "regadom", part, limit)
		if errors.Is(serr, system.ErrUploadTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: "too-large", Message: fmt.Sprintf("the upload is larger than %d MiB", limit>>20)})
			return
		} else if serr != nil {
			writeErr(w, serr)
			return
		}
		defer os.Remove(path)
		if isSBK {
			dump, err = regaimport.RegadomFromSBK(path)
		} else {
			dump, err = regaimport.ParseRegadomFile(path)
		}
	} else {
		var body struct {
			Source string `json:"source"`
			Mode   string `json:"mode"`
			DryRun bool   `json:"dry_run"`
		}
		if err := readJSON(r, &body); err != nil {
			badBody(w, err)
			return
		}
		mode, dryRun = body.Mode, body.DryRun
		switch {
		case body.Source == "" || body.Source == "box":
			// root-only on the box: through the privilege boundary
			var raw []byte
			if raw, err = system.Priv.ReadFile(a.RegadomPath()); err == nil {
				dump, err = regaimport.ParseRegadom(bytes.NewReader(raw))
			}
		case strings.HasPrefix(body.Source, "restore:"):
			name := strings.TrimPrefix(body.Source, "restore:")
			if filepath.Base(name) != name || !strings.HasPrefix(name, "restore-") || !strings.HasSuffix(name, ".sbk") {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "source: restore:<file from /restore/check>"})
				return
			}
			dump, err = regaimport.RegadomFromSBK(filepath.Join(a.BackupDir(), name))
		default:
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "source: box or restore:<file>"})
			return
		}
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "regadom", Message: err.Error()})
		return
	}
	imode := meta.ImportMerge
	switch mode {
	case "", "merge":
		mode = "merge"
	case "replace":
		imode = meta.ImportReplace
	default:
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "mode: merge or replace"})
		return
	}
	base := meta.Defaults()
	if imode == meta.ImportMerge {
		base = a.Store.Snapshot().Enums
	}
	res := regaimport.ConvertKnown(dump, base, a.KnownInterface)
	out := map[string]any{"result": res, "objects": len(res.Document.Objects), "mode": mode, "dry_run": dryRun}
	if dryRun {
		writeJSON(w, 200, out)
		return
	}
	rev, changed, err := a.Store.Import(nil, res.Document, imode)
	if err != nil {
		writeErr(w, err)
		return
	}
	if changed && a.AfterImport != nil {
		a.AfterImport()
	}
	out["revision"], out["changed"] = rev, changed
	writeJSON(w, 200, out)
}

// NamesImportResult is what the device import reports about the ReGa names it took from the same
// backup (openccu-lite task 281): the counts, or the error - a names failure never stops the
// device import.
type NamesImportResult struct {
	OK        bool   `json:"ok"`
	Objects   int    `json:"objects"`
	Rooms     int    `json:"rooms"`
	Functions int    `json:"functions"`
	Changed   bool   `json:"changed"`
	Error     string `json:"error,omitempty"`
}

// ImportNamesFromSBK imports the names, rooms and functions of a checked backup's ReGa database
// into the store, merging (existing objects and nodes stay) - the same path as POST
// /import/regadom with source restore:<file>, for the device import that runs it before its reboot
// (openccu-lite task 281: one action takes the devices, the keys and the names).
func (a *MetaAPI) ImportNamesFromSBK(path string) NamesImportResult {
	dump, err := regaimport.RegadomFromSBK(path)
	if err != nil {
		return NamesImportResult{Error: err.Error()}
	}
	res := regaimport.ConvertKnown(dump, a.Store.Snapshot().Enums, a.KnownInterface)
	_, changed, err := a.Store.Import(nil, res.Document, meta.ImportMerge)
	if err != nil {
		return NamesImportResult{Error: err.Error()}
	}
	if changed && a.AfterImport != nil {
		a.AfterImport()
	}
	return NamesImportResult{OK: true, Objects: len(res.Document.Objects), Rooms: res.Rooms, Functions: res.Functions, Changed: changed}
}

// RegadomPath is the box's ReGa database (under --root for development).
func (a *MetaAPI) RegadomPath() string { return filepath.Join(a.Root, "etc/config/homematic.regadom") }

// BackupDir is where /restore/check stores uploads (under --root for development).
func (a *MetaAPI) BackupDir() string { return filepath.Join(a.Root, "usr/local/tmp") }

// importCCU pulls names, rooms and functions out of a running CCU over its remote script port
// (D-17). dry_run answers with the counts and decisions only; otherwise the document is imported
// in the given mode (merge by default: existing objects and nodes stay, the CCU's are added).
func (a *MetaAPI) importCCU(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host   string `json:"host"`
		Port   int    `json:"port"`
		TLS    bool   `json:"tls"`
		Mode   string `json:"mode"`
		DryRun bool   `json:"dry_run"`
	}
	if err := readJSON(r, &body); err != nil {
		badBody(w, err)
		return
	}
	body.Host = strings.TrimSpace(body.Host)
	if body.Host == "" || strings.ContainsAny(body.Host, " /") {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "host: an address or a name"})
		return
	}
	mode := meta.ImportMerge
	switch body.Mode {
	case "", "merge":
	case "replace":
		mode = meta.ImportReplace
	default:
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "mode: merge or replace"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	dump, err := (&regaimport.Client{Host: body.Host, Port: body.Port, TLS: body.TLS}).Fetch(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "ccu-unreachable", Message: err.Error()})
		return
	}
	base := meta.Defaults()
	if mode == meta.ImportMerge {
		base = a.Store.Snapshot().Enums
	}
	res := regaimport.ConvertKnown(dump, base, a.KnownInterface)
	out := map[string]any{"result": res, "objects": len(res.Document.Objects), "mode": body.Mode, "dry_run": body.DryRun}
	if body.DryRun {
		writeJSON(w, 200, out)
		return
	}
	rev, changed, err := a.Store.Import(nil, res.Document, mode)
	if err != nil {
		writeErr(w, err)
		return
	}
	out["revision"], out["changed"] = rev, changed
	writeJSON(w, 200, out)
}

// --- the objects of interfaces this system does not have (occulited B-84) ---

// ForeignInterface is one interface of GET /foreign: objects in the store whose interface
// InterfacesList.xml does not list now.
type ForeignInterface struct {
	Interface string `json:"interface"`
	Objects   int    `json:"objects"`
	Devices   int    `json:"devices"`
	Channels  int    `json:"channels"`
}

func (a *MetaAPI) listed() []string {
	if a.Interfaces == nil {
		return nil
	}
	return a.Interfaces()
}

func foreignList(m map[string][]string) []ForeignInterface {
	out := []ForeignInterface{}
	for iface, refs := range m {
		f := ForeignInterface{Interface: iface, Objects: len(refs)}
		for _, ref := range refs {
			if strings.Contains(ref, ":") {
				f.Channels++
			} else {
				f.Devices++
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Interface < out[j].Interface })
	return out
}

// foreign is GET /foreign: what the removal button would take, per interface, never removing
// anything itself (the maintainer, 2026-10-20: "never automatic").
func (a *MetaAPI) foreign(w http.ResponseWriter, _ *http.Request) {
	listed := a.listed()
	snap := a.Store.Snapshot()
	if listed == nil {
		listed = []string{}
	}
	writeJSON(w, 200, map[string]any{
		"revision":   snap.Revision,
		"list_read":  len(listed) > 0,
		"listed":     listed,
		"interfaces": foreignList(regaimport.ForeignObjects(snap, listed)),
	})
}

// removeForeign is POST /foreign:remove {"interfaces": [...]}: the objects of exactly the named
// interfaces go, in one revision - each of them must be one GET /foreign lists now, so a radio
// module that came back since the page was read, or a missing InterfacesList.xml, refuses rather
// than removes.
func (a *MetaAPI) removeForeign(w http.ResponseWriter, r *http.Request) {
	im, err := ifMatch(r)
	if err != nil {
		badBody(w, err)
		return
	}
	var b struct {
		Interfaces []string `json:"interfaces"`
	}
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if len(b.Interfaces) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid-body", Message: "interfaces: name at least one"})
		return
	}
	listed := a.listed()
	if len(listed) == 0 {
		writeJSON(w, http.StatusConflict, apiError{Error: "no-interface-list", Message: "InterfacesList.xml could not be read: nothing is removed"})
		return
	}
	foreign := regaimport.ForeignObjects(a.Store.Snapshot(), listed)
	var del []string
	seen := map[string]bool{}
	for _, iface := range b.Interfaces {
		if seen[iface] {
			continue
		}
		seen[iface] = true
		refs, ok := foreign[iface]
		if !ok {
			writeJSON(w, http.StatusConflict, apiError{Error: "not-foreign", Message: iface + ": not an interface this system lacks that has objects", Detail: map[string]any{"interface": iface}})
			return
		}
		del = append(del, refs...)
	}
	rev, changed, err := a.Store.Bulk(im, nil, del)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": rev, "changed": changed, "removed": len(del)})
}
