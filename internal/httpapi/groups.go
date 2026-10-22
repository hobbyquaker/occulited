package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/hmgroups"
	"github.com/hobbyquaker/occulited/internal/meta"
)

// ---- task 180: heating groups through occulited's API ---------------------------------------
//
// hmipserver's group pages do the work (internal/hmgroups); occulited is their only client, with
// the logins it already has, and answers hmipserver's session check on the loopback. The ReGa
// side effects the WebUI did from the browser afterwards are occulited's now, in the same
// request: the members' inHeatingGroup metadata, and the names of the group device and its
// channels (task 42: as the WebUI's GroupEditPage named them, on a create and a rename only).

// GroupsAPI is what the routes need: the client, the session it answers for, and the metadata
// store for the side effects.
type GroupsAPI struct {
	Client  *hmgroups.Client
	Session *hmgroups.Session
	Meta    *meta.Store
	Log     *slog.Logger
	// Interface is the group devices' interface in the metadata refs (VirtualDevices).
	Interface string
	// Channels lists a group device's channel addresses as the interface has them
	// (getDeviceDescription's CHILDREN on VirtualDevices; task 42). nil, or an interface that does
	// not answer: the channels the store already has objects for are named.
	Channels func(ctx context.Context, device string) ([]string, error)
	// ChannelsRetry is the wait before asking Channels again when a new group's device is not
	// there yet (1 s when zero; two more tries).
	ChannelsRetry time.Duration
}

// MetaNamespace is the objects' meta namespace the group membership lives in; the WebUI wrote
// ReGa's inHeatingGroup, the CCU's clients read it there.
const groupsMetaNamespace = "hmip"

var groupIDRe = regexp.MustCompile(`^[0-9]{1,9}$`)

func (a *SystemAPI) groupsUnavailable(w http.ResponseWriter) bool {
	if a.Groups == nil || a.Groups.Client == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "heating groups are not available on this system"})
		return true
	}
	return false
}

func (g *GroupsAPI) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

// groupView is a group as the API lists it.
type groupView struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	TypeLabel string `json:"type_label"`
	// Device is the group device's address on VirtualDevices, and Ref its metadata ref.
	Device string `json:"device"`
	Ref    string `json:"ref"`
}

func (g *GroupsAPI) view(x hmgroups.Group) groupView {
	id, _ := strconv.Atoi(x.ID)
	serial := hmgroups.Serial(id)
	return groupView{ID: id, Name: x.Name, Type: x.Type, TypeLabel: x.TypeLabel, Device: serial, Ref: g.Interface + "." + serial}
}

// memberView is a device as the group pages know it, with its metadata ref when the serial is a
// device address.
type memberView struct {
	ID     string `json:"id"`
	Serial string `json:"serial"`
	Type   string `json:"type"`
}

func members(list []hmgroups.Member) []memberView {
	out := make([]memberView, 0, len(list))
	for _, m := range list {
		out = append(out, memberView{ID: m.ID, Serial: m.Serial, Type: m.Type})
	}
	return out
}

// GET /groups: the groups, and the devices whose configuration is still pending from the last change.
func (a *SystemAPI) groupsList(w http.ResponseWriter, r *http.Request) {
	if a.groupsUnavailable(w) {
		return
	}
	v, err := a.Groups.Client.List(r.Context())
	if err != nil {
		groupsErr(w, err)
		return
	}
	out := make([]groupView, 0, len(v.Groups))
	for _, x := range v.Groups {
		out = append(out, a.Groups.view(x))
	}
	writeJSON(w, 200, map[string]any{"groups": out, "devices_to_configure": members(v.DevicesToConfigure)})
}

// GET /groups/types: the group types the editor offers, each with what a group of it could take
// now and what fits no group any more.
func (a *SystemAPI) groupsTypes(w http.ResponseWriter, r *http.Request) {
	if a.groupsUnavailable(w) {
		return
	}
	ev, err := a.Groups.Client.Create(r.Context())
	if err != nil {
		groupsErr(w, err)
		return
	}
	type typeView struct {
		ID         string       `json:"id"`
		Label      string       `json:"label"`
		Assignable []memberView `json:"assignable"`
		Leftover   []memberView `json:"leftover"`
	}
	out := make([]typeView, 0, len(ev.Types))
	for _, t := range ev.Types {
		assignable, leftover, err := a.Groups.Client.SuitableMembers(r.Context(), t.ID)
		if err != nil {
			groupsErr(w, err)
			return
		}
		out = append(out, typeView{ID: t.ID, Label: t.Label, Assignable: members(assignable), Leftover: members(leftover)})
	}
	writeJSON(w, 200, map[string]any{"types": out})
}

// GET /groups/{id}: one group with its members, what it could take, and what fits no more.
func (a *SystemAPI) groupsGet(w http.ResponseWriter, r *http.Request) {
	if a.groupsUnavailable(w) {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	g, ok := a.groupKnown(w, r, id)
	if !ok {
		return
	}
	ev, err := a.Groups.Client.Edit(r.Context(), id, deviceName(g.Name, id))
	if err != nil {
		groupsErr(w, err)
		return
	}
	writeJSON(w, 200, editView(a.Groups, ev))
}

func editView(g *GroupsAPI, ev hmgroups.EditView) map[string]any {
	serial := hmgroups.Serial(ev.ID)
	return map[string]any{
		"id": ev.ID, "name": ev.Name, "type": ev.Type, "device": serial, "ref": g.Interface + "." + serial,
		"device_name": ev.GroupDeviceName, "forbid_single_operation": ev.ForbidSingle,
		"members": members(ev.Assigned), "assignable": members(ev.Assignable), "leftover": members(ev.Leftover),
		"types": ev.Types,
	}
}

type groupBody struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Members []string `json:"members"`
	// ForbidSingleOperation is the WebUI's "operate as group only" (nil = leave as it is)
	ForbidSingleOperation *bool `json:"forbid_single_operation"`
}

var groupNameRe = regexp.MustCompile(`^[^\x00-\x1f]{1,64}$`)

func (b groupBody) check(needType bool) error {
	if !groupNameRe.MatchString(strings.TrimSpace(b.Name)) {
		return errors.New("name: 1 to 64 characters on one line")
	}
	if needType && b.Type == "" {
		return errors.New("type: the group type, e.g. HomeMatic.heating")
	}
	for _, m := range b.Members {
		if m == "" || len(m) > 64 || strings.ContainsAny(m, "\r\n\"") {
			return errors.New("members: hmipserver's device ids, as GET /groups/types lists them")
		}
	}
	return nil
}

// POST /groups {name, type, members}: a new group - the editor's create, then its save with the
// members; the answer names the new group and the devices whose configuration is pending.
//
// hmipserver's save stores the group before it takes the members (B-269): a save that fails can
// leave a group behind. The API then reads the list again: a new group of that name with all the
// members asked for is the success the answer missed; one without them is deleted, so a failed
// create leaves nothing, or the error names the group that could not be removed.
func (a *SystemAPI) groupsCreate(w http.ResponseWriter, r *http.Request) {
	if a.groupsUnavailable(w) {
		return
	}
	var b groupBody
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if err := b.check(true); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	ctx := r.Context()
	lv, err := a.Groups.Client.List(ctx)
	if err != nil {
		groupsErr(w, err)
		return
	}
	existed := map[string]bool{}
	for _, g := range lv.Groups {
		existed[g.ID] = true
	}
	ev, err := a.Groups.Client.Create(ctx)
	if err != nil {
		groupsErr(w, err)
		return
	}
	if !typeOffered(ev, b.Type) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "type: not one hmipserver offers: " + typeIDs(ev)})
		return
	}
	forbid := false
	if b.ForbidSingleOperation != nil {
		forbid = *b.ForbidSingleOperation
	}
	name := strings.TrimSpace(b.Name)
	// the new group's id is hmipserver's to give (the editor's is 0): the device name is set
	// with its real serial by the edit that reads the group back
	res, err := a.Groups.Client.Save(ctx, hmgroups.SaveBody{ID: ev.ID, Name: name, TypeID: b.Type, ForbidSingle: forbid, MemberIDs: b.Members, IsNew: true, GroupDeviceName: name})
	if err != nil {
		id, done := a.createFailed(w, r, existed, name, b.Members, err)
		if !done {
			return
		}
		res.ID = id
	}
	a.Groups.afterSave(ctx, res.ID, b.Members, nil)
	a.Groups.nameDevice(ctx, res.ID, name, "")
	a.groupsAnswer(w, r, res.ID, name)
}

// createFailed handles a save that did not answer as it should: done with the new group's id when
// hmipserver did the whole create after all; otherwise the half-made group is deleted and the
// error answered.
func (a *SystemAPI) createFailed(w http.ResponseWriter, r *http.Request, existed map[string]bool, name string, want []string, cause error) (int, bool) {
	ctx := context.WithoutCancel(r.Context())
	log := a.Groups.log()
	lv, err := a.Groups.Client.List(ctx)
	if err != nil {
		log.Warn("groups: a create failed, and the list to check what it left could not be read", "err", err, "cause", cause)
		writeJSON(w, http.StatusBadGateway, apiError{Error: "hmipserver", Message: cause.Error() + "; whether a group was stored could not be read: " + err.Error()})
		return 0, false
	}
	var left []int
	for _, g := range lv.Groups {
		if existed[g.ID] || g.Name != name {
			continue
		}
		id, _ := strconv.Atoi(g.ID)
		ev, err := a.Groups.Client.Edit(ctx, id, deviceName(name, id))
		if err == nil && sameMembers(ev.Assigned, want) {
			log.Warn("groups: the create's save did not answer, but the group was stored with its members", "group", id, "cause", cause)
			return id, true
		}
		left = append(left, id)
	}
	if len(left) == 0 {
		groupsErr(w, cause)
		return 0, false
	}
	var kept []string
	for _, id := range left {
		if _, err := a.Groups.Client.Delete(ctx, id); err != nil {
			log.Warn("groups: the half-made group could not be deleted", "group", id, "err", err)
			kept = append(kept, strconv.Itoa(id))
			continue
		}
		log.Warn("groups: a create failed after hmipserver had stored the group; it was deleted again", "group", id, "cause", cause)
	}
	msg := cause.Error() + "; hmipserver had stored the group without its members, and it was deleted again"
	if len(kept) > 0 {
		msg = cause.Error() + "; hmipserver stored the group without its members, and deleting it failed: group " + strings.Join(kept, ", ") + " is left - delete it"
	}
	writeJSON(w, http.StatusBadGateway, apiError{Error: "hmipserver", Message: msg})
	return 0, false
}

// sameMembers says whether the group holds exactly the ids.
func sameMembers(have []hmgroups.Member, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	set := map[string]bool{}
	for _, m := range have {
		set[m.ID] = true
	}
	for _, id := range want {
		if !set[id] {
			return false
		}
	}
	return true
}

// deviceName is the group device's name: "<group name> INT000000N", as the WebUI named it.
func deviceName(name string, id int) string { return name + " " + hmgroups.Serial(id) }

// PUT /groups/{id} {name, members}: the name and the members as a whole - adding and removing
// members is one save with the new list, as the WebUI did it. A save that fails is read back: the
// change is kept when hmipserver made all of it, and undone with the old state when it made a part.
func (a *SystemAPI) groupsUpdate(w http.ResponseWriter, r *http.Request) {
	if a.groupsUnavailable(w) {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	var b groupBody
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	known, ok := a.groupKnown(w, r, id)
	if !ok {
		return
	}
	ctx := r.Context()
	ev, err := a.Groups.Client.Edit(ctx, id, deviceName(known.Name, id))
	if err != nil {
		groupsErr(w, err)
		return
	}
	if b.Name == "" {
		b.Name = ev.Name
	}
	if b.Type == "" {
		b.Type = ev.Type
	}
	if b.Members == nil {
		for _, m := range ev.Assigned {
			b.Members = append(b.Members, m.ID)
		}
	}
	if err := b.check(true); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	forbid := ev.ForbidSingle
	if b.ForbidSingleOperation != nil {
		forbid = *b.ForbidSingleOperation
	}
	name := strings.TrimSpace(b.Name)
	before := make([]string, 0, len(ev.Assigned))
	for _, m := range ev.Assigned {
		before = append(before, m.ID)
	}
	res, err := a.Groups.Client.Save(ctx, hmgroups.SaveBody{ID: ev.ID, Name: name, TypeID: b.Type, ForbidSingle: forbid, MemberIDs: b.Members, IsNew: false, GroupDeviceName: deviceName(name, ev.ID)})
	if err != nil {
		if !a.updateFailed(w, r, ev, name, forbid, b.Members, err) {
			return
		}
		res.ID = ev.ID
	}
	a.Groups.afterSave(ctx, res.ID, b.Members, before)
	if name != ev.Name {
		// task 42: the names follow a rename only, as the WebUI's; a members-only save keeps them
		a.Groups.nameDevice(ctx, res.ID, name, ev.Name)
	}
	a.groupsAnswer(w, r, res.ID, name)
}

// updateFailed reads a group back after a save that did not answer: true when hmipserver made
// the whole change after all; otherwise the old state is saved again, and the error answered.
func (a *SystemAPI) updateFailed(w http.ResponseWriter, r *http.Request, old hmgroups.EditView, name string, forbid bool, want []string, cause error) bool {
	ctx := context.WithoutCancel(r.Context())
	log := a.Groups.log()
	now, err := a.Groups.Client.Edit(ctx, old.ID, deviceName(name, old.ID))
	if err != nil {
		log.Warn("groups: a change failed, and the group could not be read back", "group", old.ID, "err", err, "cause", cause)
		writeJSON(w, http.StatusBadGateway, apiError{Error: "hmipserver", Message: cause.Error() + "; the group could not be read back: " + err.Error()})
		return false
	}
	if now.Name == name && now.ForbidSingle == forbid && sameMembers(now.Assigned, want) {
		log.Warn("groups: the change's save did not answer, but the group holds the change", "group", old.ID, "cause", cause)
		return true
	}
	oldIDs := make([]string, 0, len(old.Assigned))
	for _, m := range old.Assigned {
		oldIDs = append(oldIDs, m.ID)
	}
	if now.Name == old.Name && now.ForbidSingle == old.ForbidSingle && sameMembers(now.Assigned, oldIDs) {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "hmipserver", Message: cause.Error() + "; the group is unchanged"})
		return false
	}
	_, rerr := a.Groups.Client.Save(ctx, hmgroups.SaveBody{ID: old.ID, Name: old.Name, TypeID: old.Type, ForbidSingle: old.ForbidSingle, MemberIDs: oldIDs, IsNew: false, GroupDeviceName: deviceName(old.Name, old.ID)})
	if rerr != nil {
		log.Warn("groups: a change failed half-way, and the old state could not be saved again", "group", old.ID, "err", rerr, "cause", cause)
		writeJSON(w, http.StatusBadGateway, apiError{Error: "hmipserver", Message: cause.Error() + "; the group was changed in part, and restoring it failed: " + rerr.Error()})
		return false
	}
	log.Warn("groups: a change failed half-way; the old state was saved again", "group", old.ID, "cause", cause)
	writeJSON(w, http.StatusBadGateway, apiError{Error: "hmipserver", Message: cause.Error() + "; the group was changed in part, and its old state was restored"})
	return false
}

// DELETE /groups/{id}: the group goes; its former members lose the group membership.
func (a *SystemAPI) groupsDelete(w http.ResponseWriter, r *http.Request) {
	if a.groupsUnavailable(w) {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	if _, ok := a.groupKnown(w, r, id); !ok {
		return
	}
	former, err := a.Groups.Client.Delete(r.Context(), id)
	if err != nil {
		groupsErr(w, err)
		return
	}
	a.Groups.afterDelete(r.Context(), id, former)
	writeJSON(w, 200, map[string]any{"deleted": id, "former_members": members(former)})
}

// groupsAnswer is what a create or an update ends with: the group as it stands, and the devices
// whose configuration is pending.
func (a *SystemAPI) groupsAnswer(w http.ResponseWriter, r *http.Request, id int, name string) {
	ev, err := a.Groups.Client.Edit(r.Context(), id, deviceName(name, id))
	if err != nil {
		groupsErr(w, err)
		return
	}
	pending, perr := a.Groups.Client.ConfigureDevices(r.Context(), hmgroups.Serial(id))
	if perr != nil {
		a.Groups.log().Warn("groups: the pending configuration could not be read", "group", id, "err", perr)
		pending = []hmgroups.Member{}
	}
	out := editView(a.Groups, ev)
	out["devices_to_configure"] = members(pending)
	writeJSON(w, 200, out)
}

// groupKnown is the group as hmipserver lists it: its edit and delete of an unknown id end in a
// NullPointerException in its worker and no answer at all (measured 2026-09-22 on the OVA), so
// the API asks the list first and answers 404 itself.
func (a *SystemAPI) groupKnown(w http.ResponseWriter, r *http.Request, id int) (hmgroups.Group, bool) {
	v, err := a.Groups.Client.List(r.Context())
	if err != nil {
		groupsErr(w, err)
		return hmgroups.Group{}, false
	}
	want := strconv.Itoa(id)
	for _, g := range v.Groups {
		if g.ID == want {
			return g, true
		}
	}
	writeJSON(w, http.StatusNotFound, apiError{Error: "unknown-group", Message: fmt.Sprintf("there is no group %d", id)})
	return hmgroups.Group{}, false
}

func groupID(w http.ResponseWriter, r *http.Request) (int, bool) {
	s := r.PathValue("id")
	if !groupIDRe.MatchString(s) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: "the group id is a number"})
		return 0, false
	}
	id, _ := strconv.Atoi(s)
	return id, true
}

func typeOffered(ev hmgroups.EditView, id string) bool {
	for _, t := range ev.Types {
		if t.ID == id {
			return true
		}
	}
	return false
}

func typeIDs(ev hmgroups.EditView) string {
	ids := make([]string, 0, len(ev.Types))
	for _, t := range ev.Types {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ", ")
}

func groupsErr(w http.ResponseWriter, err error) {
	if errors.Is(err, hmgroups.ErrSession) {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "hmipserver", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusBadGateway, apiError{Error: "hmipserver", Message: err.Error()})
}

// ---- the side effects in the metadata store ---------------------------------------------------

// afterSave: the members' inHeatingGroup (true for the members, false for the ones that left).
func (g *GroupsAPI) afterSave(ctx context.Context, id int, memberIDs, before []string) {
	if g.Meta == nil {
		return
	}
	now := map[string]bool{}
	for _, m := range memberIDs {
		now[m] = true
	}
	for _, m := range memberIDs {
		g.setInGroup(m, true)
	}
	for _, m := range before {
		if !now[m] {
			g.setInGroup(m, false)
		}
	}
}

// nameDevice names the group device "<name> INT000000N" and each of its channels
// "<name> INT000000N:n" in the metadata store, as the WebUI's GroupEditPage did in ReGa after a
// save (task 42). old is "" for a new group: every name is written. On a rename (old is the
// group's name before it) a name is written only where it is missing, empty or still the one the
// old name generated: a name the user gave the device or a channel stays - occulited has no
// dialog to ask, as the WebUI did, so its "No" is the default for a name set by hand.
func (g *GroupsAPI) nameDevice(ctx context.Context, id int, name, old string) {
	if g.Meta == nil {
		return
	}
	serial := hmgroups.Serial(id)
	ref := g.Interface + "." + serial
	g.setName(ref, name+" "+serial, old, old+" "+serial)
	for _, ch := range g.channelsOf(ctx, serial, old == "") {
		n := ch[strings.IndexByte(ch, ':'):]
		g.setName(g.Interface+"."+ch, name+" "+serial+n, old, old+" "+serial+n)
	}
}

// setName writes one object's name: always when old is "" (a create), otherwise only over a
// missing or empty name or the generated one.
func (g *GroupsAPI) setName(ref, name, old, generated string) {
	if old != "" {
		if o, err := g.Meta.GetObject(ref); err == nil && o.Name != "" && o.Name != generated {
			g.log().Debug("groups: a name set by hand stays", "ref", ref, "name", o.Name)
			return
		}
	}
	if _, _, err := g.Meta.SetObject(nil, ref, meta.ObjectPatch{Name: &name}); err != nil {
		// an object the store does not have yet (the group device is new): put it
		if _, _, perr := g.Meta.PutObject(nil, ref, name, nil, nil); perr != nil {
			g.log().Warn("groups: the group device's name", "ref", ref, "err", perr)
		}
	}
}

// channelsOf is the group device's channel addresses (INT000000N:n): the interface's, with the
// ones the store has objects for. A new group's device may take a moment to appear on the
// interface, so a create asks again twice.
func (g *GroupsAPI) channelsOf(ctx context.Context, serial string, fresh bool) []string {
	set := map[string]bool{}
	if g.Channels != nil {
		tries := 1
		if fresh {
			tries = 3
		}
		wait := g.ChannelsRetry
		if wait <= 0 {
			wait = time.Second
		}
		for n := 0; n < tries; n++ {
			if n > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
			}
			list, err := g.Channels(ctx, serial)
			if err == nil && len(list) > 0 {
				for _, ch := range list {
					if strings.HasPrefix(ch, serial+":") {
						set[ch] = true
					}
				}
				break
			}
			if n == tries-1 {
				g.log().Warn("groups: the group device's channels could not be read; only the ones the store knows are named", "device", serial, "err", err)
			}
		}
	}
	prefix := g.Interface + "." + serial + ":"
	for ref := range g.Meta.Snapshot().Objects {
		if strings.HasPrefix(ref, prefix) {
			set[strings.TrimPrefix(ref, g.Interface+".")] = true
		}
	}
	out := make([]string, 0, len(set))
	for ch := range set {
		out = append(out, ch)
	}
	sort.Strings(out)
	return out
}

// afterDelete: the former members' inHeatingGroup off; the group device's object and its
// channels' objects go.
func (g *GroupsAPI) afterDelete(ctx context.Context, id int, former []hmgroups.Member) {
	if g.Meta == nil {
		return
	}
	for _, m := range former {
		g.setInGroup(m.ID, false)
	}
	ref := g.Interface + "." + hmgroups.Serial(id)
	refs := []string{ref}
	for r := range g.Meta.Snapshot().Objects {
		if strings.HasPrefix(r, ref+":") {
			refs = append(refs, r)
		}
	}
	for _, r := range refs {
		if _, _, err := g.Meta.DeleteObject(nil, r); err != nil {
			g.log().Debug("groups: the group device's object", "ref", r, "err", err)
		}
	}
}

// setInGroup writes {"inHeatingGroup": …} into the device's hmip meta namespace. The member id
// is hmipserver's; the ref is the device's on its interface, which the id names when it is a
// serial (hmipserver hands the serial as the id where no ReGa numbers the devices).
func (g *GroupsAPI) setInGroup(memberID string, in bool) {
	ref := g.refOf(memberID)
	if ref == "" {
		return
	}
	cur := map[string]any{}
	if o, err := g.Meta.GetObject(ref); err == nil {
		if raw, ok := o.Meta[groupsMetaNamespace]; ok {
			_ = json.Unmarshal(raw, &cur)
		}
	}
	cur["inHeatingGroup"] = in
	raw, _ := json.Marshal(cur)
	if _, _, err := g.Meta.SetObject(nil, ref, meta.ObjectPatch{Meta: map[string]json.RawMessage{groupsMetaNamespace: raw}}); err != nil {
		if _, _, perr := g.Meta.PutObject(nil, ref, "", nil, map[string]json.RawMessage{groupsMetaNamespace: raw}); perr != nil {
			g.log().Debug("groups: the member's metadata", "ref", ref, "err", perr)
		}
	}
}

// refOf turns a member id into a metadata ref: an id that is a device serial of a paired
// interface is looked up among the store's objects by its address part; anything else is left.
func (g *GroupsAPI) refOf(memberID string) string {
	id := strings.TrimSpace(memberID)
	if id == "" {
		return ""
	}
	for ref := range g.Meta.Snapshot().Objects {
		if strings.HasSuffix(ref, "."+id) {
			return ref
		}
	}
	return ""
}

// ---- hmipserver's session check ----------------------------------------------------------------

// homematicCGI answers POST /api/homematic.cgi for hmipserver's SessionVerifier: the JSON-RPC
// Event.poll with {_session_id_} - the CCU JSON-API call it makes before every group command -
// answered as a CCU does for a live session ({"version":"1.1","result":[],"error":null}) when
// the sid is occulited's own, and with the CCU's "access denied" otherwise. Loopback only: the
// request comes through lighttpd from 127.0.0.1, and nothing else is a CCU JSON-API here (D-1).
func (a *SystemAPI) homematicCGI(w http.ResponseWriter, r *http.Request) {
	if !loopbackOnly(r) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not-found", Message: "not found"})
		return
	}
	var b struct {
		Method string `json:"method"`
		Params struct {
			SID string `json:"_session_id_"`
		} `json:"params"`
	}
	_ = readJSON(r, &b)
	if a.Groups != nil && a.Groups.Session != nil && b.Method == "Event.poll" && a.Groups.Session.Verify(b.Params.SID) {
		writeJSON(w, 200, map[string]any{"version": "1.1", "result": []any{}, "error": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"version": "1.1", "result": nil, "error": map[string]any{"name": "JSONRPCError", "code": 400, "message": fmt.Sprintf("access denied: %s", b.Method)}})
}

// loopbackOnly says the request came from this system: the address lighttpd forwarded, or the
// connection's own.
func loopbackOnly(r *http.Request) bool {
	ip := net.ParseIP(remote(r))
	return ip != nil && ip.IsLoopback()
}

// GroupsScopes documents the routes' scopes: reads system:read, changes system:write.
var _ = auth.ScopeSystemWrite

// ---- B-102: the heal at start ----------------------------------------------------------------

// Heal names the group devices and channels the metadata store has no name for, from hmipserver's
// group list: the device "<group name> INT000000N", each channel "<group name> INT000000N:n", as a
// create names them. An object that is missing, has an empty name or is named by its own address
// is named; any other name stays - a name the user typed, or a heal's or a create's earlier one.
// The regadom import before B-102 dropped every group (ReGa's "<name> INT…" looked like the CCU's
// default "<type> <address>" name) and left the room members named by address; this writes the
// names back at the first start of a fixed binary, and finds nothing on the starts after it.
// It returns the refs it named.
func (g *GroupsAPI) Heal(ctx context.Context) ([]string, error) {
	if g.Client == nil || g.Meta == nil {
		return nil, nil
	}
	lv, err := g.Client.List(ctx)
	if err != nil {
		return nil, err
	}
	var named []string
	for _, x := range lv.Groups {
		id, err := strconv.Atoi(x.ID)
		name := strings.TrimSpace(x.Name)
		if err != nil || name == "" {
			continue
		}
		serial := hmgroups.Serial(id)
		if g.healName(g.Interface+"."+serial, name+" "+serial, serial) {
			named = append(named, g.Interface+"."+serial)
		}
		for _, ch := range g.channelsOf(ctx, serial, false) {
			n := ch[strings.IndexByte(ch, ':'):]
			if g.healName(g.Interface+"."+ch, name+" "+serial+n, ch) {
				named = append(named, g.Interface+"."+ch)
			}
		}
	}
	sort.Strings(named)
	return named, nil
}

// healName writes name over a missing, empty or address-only name (the heal's one rule: a name
// equal to the object's own address counts as none here, nowhere else) and says whether it did.
func (g *GroupsAPI) healName(ref, name, address string) bool {
	if o, err := g.Meta.GetObject(ref); err == nil && o.Name != "" && o.Name != address {
		return false
	}
	g.setName(ref, name, "", "")
	return true
}

// HealAtStart runs Heal once hmipserver answers its group list: it starts minutes after occulited
// on a slow system, so the list is asked again every `every` until it answers, at most `tries`
// times (a system without hmipserver never answers; the heal is then left for the next start).
// What was named is logged once; nothing named is one Debug line.
func (g *GroupsAPI) HealAtStart(ctx context.Context, every time.Duration, tries int) {
	var err error
	for n := 0; n < tries; n++ {
		if n > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
		}
		var named []string
		named, err = g.Heal(ctx)
		if err != nil {
			continue
		}
		if len(named) > 0 {
			g.log().Info("groups: heating groups named in the metadata store from hmipserver's group list", "count", len(named), "refs", strings.Join(named, ", "))
		} else {
			g.log().Debug("groups: every heating group is named")
		}
		return
	}
	g.log().Info("groups: hmipserver did not answer its group list; the group names were not checked this start", "err", err, "tries", tries)
}
