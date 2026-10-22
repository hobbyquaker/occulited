package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/hmgroups"
	"github.com/hobbyquaker/occulited/internal/hmgroups/hmgroupstest"
	"github.com/hobbyquaker/occulited/internal/meta"
)

// groupsRig: a system API with task 180's groups on the fake hmipserver, and a metadata store
// that knows the two devices.
func groupsRig(t *testing.T) (*http.ServeMux, *hmgroupstest.Fake, *meta.Store, *hmgroups.Session) {
	t.Helper()
	s := hmgroups.NewSession()
	f := hmgroupstest.New(s.SID())
	t.Cleanup(f.Close)
	f.Devices = []hmgroupstest.Device{{ID: "KEQ9000003", Serial: "KEQ9000003", Type: "HM-Sec-SC"}, {ID: "00010000000A10:1", Serial: "00010000000A10:1", Type: "HmIP-eTRV"}}
	ms, _ := meta.New(nil, nil)
	for ref, name := range map[string]string{"BidCos-RF.KEQ9000003": "Fenster", "HmIP-RF.00010000000A10:1": "Ventil"} {
		if _, _, err := ms.PutObject(nil, ref, name, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	// a short timeout: the fake's dying saves are never answered
	c := &hmgroups.Client{Base: f.URL(), Session: s, HTTP: &http.Client{Timeout: 300 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}}
	// task 42: the group device has the maintenance channel and one more, as an HmIP-HEATING
	channels := func(_ context.Context, device string) ([]string, error) {
		return []string{device + ":0", device + ":1"}, nil
	}
	a := &SystemAPI{Groups: &GroupsAPI{Client: c, Session: s, Meta: ms, Interface: "VirtualDevices", Log: slog.New(slog.DiscardHandler), Channels: channels}}
	mux := http.NewServeMux()
	a.Register(mux)
	return mux, f, ms, s
}

func inGroup(t *testing.T, ms *meta.Store, ref string) any {
	t.Helper()
	o, err := ms.GetObject(ref)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(o.Meta["hmip"], &m)
	return m["inHeatingGroup"]
}

func TestGroupsRoutes(t *testing.T) {
	mux, f, ms, _ := groupsRig(t)
	const p = "/api/system/v1/groups"
	// the scopes are the middleware's: reads system:read, changes system:write, the CGI open
	scopes := RouteScopes()
	for pattern, want := range map[string]auth.Scope{"GET " + p: auth.ScopeSystemRead, "GET " + p + "/types": auth.ScopeSystemRead, "GET " + p + "/{id}": auth.ScopeSystemRead,
		"POST " + p: auth.ScopeSystemWrite, "PUT " + p + "/{id}": auth.ScopeSystemWrite, "DELETE " + p + "/{id}": auth.ScopeSystemWrite, "POST /api/homematic.cgi": scopeOpen} {
		if got := scopes[pattern]; len(got) != 1 || got[0] != want {
			t.Errorf("scopes of %s: %v, want %s", pattern, got, want)
		}
	}
	st, out := warnCall(t, mux, "GET", p, "", "monitor", user)
	if st != 200 || len(out["groups"].([]any)) != 0 || out["devices_to_configure"] == nil {
		t.Fatalf("list: %d %v", st, out)
	}
	st, out = warnCall(t, mux, "GET", p+"/types", "", "admin", auth.RoleAdmin)
	types := out["types"].([]any)
	if st != 200 || len(types) != 2 || len(types[0].(map[string]any)["assignable"].([]any)) != 2 {
		t.Fatalf("types: %d %v", st, out)
	}
	// the checks
	for body, want := range map[string]string{
		`{"name":"","type":"HomeMatic.heating"}`:                                "name",
		`{"name":"x"}`:                                                          "type",
		`{"name":"x","type":"nope"}`:                                            "not one hmipserver offers",
		`{"name":"x","type":"HomeMatic.heating","members":["a\"b"]}`:            "members",
		`{"name":"` + strings.Repeat("y", 65) + `","type":"HomeMatic.heating"}`: "name",
	} {
		if st, out := warnCall(t, mux, "POST", p, body, "admin", auth.RoleAdmin); st != 422 || !strings.Contains(out["message"].(string), want) {
			t.Errorf("POST %s: %d %v", body, st, out)
		}
	}
	if st, _ := warnCall(t, mux, "POST", p, `{`, "admin", auth.RoleAdmin); st != 422 {
		t.Errorf("bad body: %d", st)
	}
	// create: hmipserver's create then save, the members' flag and the group device's name
	st, out = warnCall(t, mux, "POST", p, `{"name":" Bad ","type":"HomeMatic.heating","members":["KEQ9000003"],"forbid_single_operation":true}`, "admin", auth.RoleAdmin)
	if st != 200 || out["id"] != float64(1) || out["name"] != "Bad" || out["device"] != "INT0000001" || out["ref"] != "VirtualDevices.INT0000001" || out["forbid_single_operation"] != true || len(out["members"].([]any)) != 1 || out["devices_to_configure"] == nil {
		t.Fatalf("create: %d %v", st, out)
	}
	if g := f.Groups[1]; g == nil || g.Name != "Bad" || g.DeviceName != "Bad INT0000001" || !g.ForbidSingle {
		t.Fatalf("stored: %+v", f.Groups[1])
	}
	if inGroup(t, ms, "BidCos-RF.KEQ9000003") != true || inGroup(t, ms, "HmIP-RF.00010000000A10:1") != nil {
		t.Fatalf("inHeatingGroup after create: %v %v", inGroup(t, ms, "BidCos-RF.KEQ9000003"), inGroup(t, ms, "HmIP-RF.00010000000A10:1"))
	}
	if o, err := ms.GetObject("VirtualDevices.INT0000001"); err != nil || o.Name != "Bad INT0000001" {
		t.Fatalf("group device object: %v %+v", err, o)
	}
	// B-269: the member ids went as a string holding the JSON list
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "save ") && !strings.Contains(c, `"assignedDevicesIds":"[\"KEQ9000003\"]"`) {
			t.Fatalf("save's member ids: %s", c)
		}
	}
	// a read keeps hmipserver's device name (its edit stores the name it is given)
	if st, _ := warnCall(t, mux, "GET", p+"/1", "", "monitor", user); st != 200 || f.Groups[1].DeviceName != "Bad INT0000001" {
		t.Fatalf("the device name after a read: %d %q", st, f.Groups[1].DeviceName)
	}
	// the pending configuration comes with the answer
	f.Pending["INT0000001"] = []hmgroupstest.Device{{ID: "KEQ9000003", Serial: "KEQ9000003", Type: "HM-Sec-SC"}}
	st, out = warnCall(t, mux, "PUT", p+"/1", `{"name":"Schlafzimmer"}`, "admin", auth.RoleAdmin)
	if st != 200 || out["name"] != "Schlafzimmer" || len(out["members"].([]any)) != 1 || len(out["devices_to_configure"].([]any)) != 1 {
		t.Fatalf("rename: %d %v", st, out)
	}
	if o, _ := ms.GetObject("VirtualDevices.INT0000001"); o.Name != "Schlafzimmer INT0000001" {
		t.Fatalf("group device renamed: %+v", o)
	}
	// the members as a whole: the one that left loses the flag, the new one gets it
	st, out = warnCall(t, mux, "PUT", p+"/1", `{"members":["00010000000A10:1"]}`, "admin", auth.RoleAdmin)
	if st != 200 || out["name"] != "Schlafzimmer" || out["members"].([]any)[0].(map[string]any)["id"] != "00010000000A10:1" {
		t.Fatalf("members: %d %v", st, out)
	}
	if inGroup(t, ms, "BidCos-RF.KEQ9000003") != false || inGroup(t, ms, "HmIP-RF.00010000000A10:1") != true {
		t.Fatal("inHeatingGroup after the member change")
	}
	st, out = warnCall(t, mux, "GET", p+"/1", "", "monitor", user)
	if st != 200 || out["type"] != "HomeMatic.heating" || len(out["assignable"].([]any)) != 1 {
		t.Fatalf("get: %d %v", st, out)
	}
	if st, _ := warnCall(t, mux, "GET", p+"/x", "", "monitor", user); st != 400 {
		t.Errorf("bad id: %d", st)
	}
	// an unknown id is 404 from the list, never hmipserver's edit (which answers nothing for it)
	calls := len(f.Calls)
	if st, out := warnCall(t, mux, "GET", p+"/9", "", "monitor", user); st != 404 || out["error"] != "unknown-group" {
		t.Errorf("unknown group: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "PUT", p+"/9", `{"name":"x"}`, "admin", auth.RoleAdmin); st != 404 || out["error"] != "unknown-group" {
		t.Errorf("update unknown group: %d %v", st, out)
	}
	for _, c := range f.Calls[calls:] {
		if !strings.HasPrefix(c, "list ") {
			t.Errorf("an unknown id reached hmipserver: %s", c)
		}
	}
	if st, _ := warnCall(t, mux, "PUT", p+"/1", `{"name":""}`, "admin", auth.RoleAdmin); st != 200 {
		t.Errorf("an empty name keeps the old one: %d", st)
	}
	if st, _ := warnCall(t, mux, "PUT", p+"/1", `{"name":"\n"}`, "admin", auth.RoleAdmin); st != 422 {
		t.Errorf("a bad name: %d", st)
	}
	// delete: the former members' flag off, the object gone
	st, out = warnCall(t, mux, "DELETE", p+"/1", "", "admin", auth.RoleAdmin)
	if st != 200 || out["deleted"] != float64(1) || len(out["former_members"].([]any)) != 1 {
		t.Fatalf("delete: %d %v", st, out)
	}
	if inGroup(t, ms, "HmIP-RF.00010000000A10:1") != false {
		t.Fatal("inHeatingGroup after delete")
	}
	if _, err := ms.GetObject("VirtualDevices.INT0000001"); err == nil {
		t.Fatal("the group device's object must go")
	}
	if st, out := warnCall(t, mux, "DELETE", p+"/1", "", "admin", auth.RoleAdmin); st != 404 || out["error"] != "unknown-group" {
		t.Errorf("delete again: %d %v", st, out)
	}
	// hmipserver down: 502 with its word
	f.Close()
	if st, out := warnCall(t, mux, "GET", p, "", "monitor", user); st != 502 || out["error"] != "hmipserver" {
		t.Errorf("down: %d %v", st, out)
	}
}

func TestGroupsUnsupported(t *testing.T) {
	mux := http.NewServeMux()
	(&SystemAPI{}).Register(mux)
	for _, c := range [][2]string{{"GET", "/api/system/v1/groups"}, {"GET", "/api/system/v1/groups/types"}, {"GET", "/api/system/v1/groups/1"}, {"POST", "/api/system/v1/groups"}, {"PUT", "/api/system/v1/groups/1"}, {"DELETE", "/api/system/v1/groups/1"}} {
		if st, out := warnCall(t, mux, c[0], c[1], `{}`, "admin", auth.RoleAdmin); st != 501 || out["error"] != "unsupported" {
			t.Errorf("%s %s: %d %v", c[0], c[1], st, out)
		}
	}
	// the session check without groups: access denied, never a panic
	req := httptest.NewRequest("POST", "/api/homematic.cgi", strings.NewReader(`{"method":"Event.poll","params":{"_session_id_":"x"}}`))
	req.RemoteAddr = "127.0.0.1:5"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "access denied") {
		t.Fatalf("cgi without groups: %d %s", w.Code, w.Body.String())
	}
}

// the session check hmipserver makes before every group command
func TestHomematicCGI(t *testing.T) {
	mux, _, _, s := groupsRig(t)
	call := func(body, remote, forwarded string) (int, string) {
		req := httptest.NewRequest("POST", "/api/homematic.cgi", strings.NewReader(body))
		req.RemoteAddr = remote
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	live := `{"method":"Event.poll","params":{"_session_id_":"` + s.SID() + `"}}`
	if st, b := call(live, "127.0.0.1:5", ""); st != 200 || b != `{"error":null,"result":[],"version":"1.1"}` {
		t.Fatalf("live session: %d %s", st, b)
	}
	// through lighttpd: the forwarded address counts
	if st, b := call(live, "127.0.0.1:5", "127.0.0.1"); st != 200 || !strings.Contains(b, `"result":[]`) {
		t.Fatalf("forwarded loopback: %d %s", st, b)
	}
	if st, b := call(live, "127.0.0.1:5", "192.168.1.9"); st != 404 || !strings.Contains(b, "not-found") {
		t.Fatalf("forwarded from the LAN: %d %s", st, b)
	}
	if st, b := call(live, "192.0.2.1:1234", ""); st != 404 {
		t.Fatalf("from the LAN: %d %s", st, b)
	}
	// B-230: a client-sent X-Forwarded-For comes before lighttpd's element and is never the address
	if st, b := call(live, "127.0.0.1:5", "127.0.0.1, 192.168.1.9"); st != 404 {
		t.Fatalf("a forged loopback through lighttpd: %d %s", st, b)
	}
	if st, b := call(live, "192.0.2.1:1234", "127.0.0.1"); st != 404 {
		t.Fatalf("a forged loopback from the LAN directly: %d %s", st, b)
	}
	for _, body := range []string{
		`{"method":"Event.poll","params":{"_session_id_":"WRONGSID"}}`,
		`{"method":"Session.login","params":{"_session_id_":"` + s.SID() + `"}}`,
		`{"method":"Event.poll"}`,
		`not json`,
	} {
		st, b := call(body, "127.0.0.1:5", "")
		if st != 200 || !strings.Contains(b, `"result":null`) || !strings.Contains(b, `"code":400`) || !strings.Contains(b, "access denied") {
			t.Errorf("%s: %d %s", body, st, b)
		}
	}
}

// B-269: hmipserver's save stores the group before its members; a save that dies there is never
// answered. A failed create leaves no group behind, a failed change no half of one.
func TestGroupsSaveFails(t *testing.T) {
	mux, f, ms, _ := groupsRig(t)
	const p = "/api/system/v1/groups"
	create := `{"name":"Loom","type":"hmip.heating.group","members":["00010000000A10:1"]}`

	// the save dies after storing the group: the half-made group is deleted again
	f.Set(func(f *hmgroupstest.Fake) { f.SaveDies = 1 })
	st, out := warnCall(t, mux, "POST", p, create, "admin", auth.RoleAdmin)
	if st != 502 || out["error"] != "hmipserver" || !strings.Contains(out["message"].(string), "deleted again") {
		t.Fatalf("dying create: %d %v", st, out)
	}
	f.Set(func(f *hmgroupstest.Fake) {
		if len(f.Groups) != 0 {
			t.Fatalf("a group was left: %+v", f.Groups)
		}
	})
	if inGroup(t, ms, "HmIP-RF.00010000000A10:1") != nil {
		t.Fatal("a failed create must not flag the member")
	}
	if _, err := ms.GetObject("VirtualDevices.INT0000001"); err == nil {
		t.Fatal("a failed create must not name a group device")
	}

	// the save did everything and only the answer is missing: that is the success
	f.Set(func(f *hmgroupstest.Fake) { f.SaveUnanswered = 1 })
	st, out = warnCall(t, mux, "POST", p, create, "admin", auth.RoleAdmin)
	if st != 200 || out["id"] != float64(2) || len(out["members"].([]any)) != 1 || out["device"] != "INT0000002" {
		t.Fatalf("unanswered create: %d %v", st, out)
	}
	if inGroup(t, ms, "HmIP-RF.00010000000A10:1") != true {
		t.Fatal("the member's flag after the unanswered create")
	}

	// a change whose save dies after the name: the old state is saved again
	f.Set(func(f *hmgroupstest.Fake) { f.SaveDies = 1 })
	st, out = warnCall(t, mux, "PUT", p+"/2", `{"name":"Neu","members":["KEQ9000003"]}`, "admin", auth.RoleAdmin)
	if st != 502 || !strings.Contains(out["message"].(string), "old state was restored") {
		t.Fatalf("dying change: %d %v", st, out)
	}
	f.Set(func(f *hmgroupstest.Fake) {
		g := f.Groups[2]
		if g.Name != "Loom" || len(g.Members) != 1 || g.Members[0] != "00010000000A10:1" {
			t.Fatalf("restored: %+v", g)
		}
	})
	if inGroup(t, ms, "BidCos-RF.KEQ9000003") != nil || inGroup(t, ms, "HmIP-RF.00010000000A10:1") != true {
		t.Fatal("a failed change must leave the flags")
	}

	// the save dies with the name unchanged: nothing to restore
	f.Set(func(f *hmgroupstest.Fake) { f.SaveDies = 1 })
	st, out = warnCall(t, mux, "PUT", p+"/2", `{"members":["KEQ9000003"]}`, "admin", auth.RoleAdmin)
	if st != 502 || !strings.Contains(out["message"].(string), "unchanged") {
		t.Fatalf("dying member change: %d %v", st, out)
	}

	// the restore dies too: the answer says so
	f.Set(func(f *hmgroupstest.Fake) { f.SaveDies = 2 })
	st, out = warnCall(t, mux, "PUT", p+"/2", `{"name":"Neu","members":["KEQ9000003"]}`, "admin", auth.RoleAdmin)
	if st != 502 || !strings.Contains(out["message"].(string), "restoring it failed") {
		t.Fatalf("dying restore: %d %v", st, out)
	}

	// a change that was made and only not answered: the success (the name the failed restore
	// left is changed again)
	f.Set(func(f *hmgroupstest.Fake) { f.SaveUnanswered = 1 })
	st, out = warnCall(t, mux, "PUT", p+"/2", `{"name":"Loom 2","members":["KEQ9000003"]}`, "admin", auth.RoleAdmin)
	if st != 200 || out["name"] != "Loom 2" || out["members"].([]any)[0].(map[string]any)["id"] != "KEQ9000003" {
		t.Fatalf("unanswered change: %d %v", st, out)
	}
	if inGroup(t, ms, "BidCos-RF.KEQ9000003") != true || inGroup(t, ms, "HmIP-RF.00010000000A10:1") != false {
		t.Fatal("the flags after the unanswered change")
	}

	// a create whose save is refused before anything is stored: the plain error, nothing deleted
	f.Set(func(f *hmgroupstest.Fake) { f.Broken = true })
	if st, out := warnCall(t, mux, "POST", p, create, "admin", auth.RoleAdmin); st != 502 || strings.Contains(out["message"].(string), "deleted") {
		t.Fatalf("broken: %d %v", st, out)
	}
}

func objectName(ms *meta.Store, ref string) string {
	o, err := ms.GetObject(ref)
	if err != nil {
		return "<none>"
	}
	return o.Name
}

// task 42: the group device and its channels are named as the WebUI named them in ReGa - on a
// create, and on a rename over the names the old one generated; a members-only save and a name
// the user set stay.
func TestGroupsDeviceNames(t *testing.T) {
	mux, _, ms, _ := groupsRig(t)
	const p = "/api/system/v1/groups"
	const dev, ch0, ch1 = "VirtualDevices.INT0000001", "VirtualDevices.INT0000001:0", "VirtualDevices.INT0000001:1"
	names := func() [3]string { return [3]string{objectName(ms, dev), objectName(ms, ch0), objectName(ms, ch1)} }

	if st, out := warnCall(t, mux, "POST", p, `{"name":"Bad","type":"HomeMatic.heating","members":["KEQ9000003"]}`, "admin", auth.RoleAdmin); st != 200 {
		t.Fatalf("create: %d %v", st, out)
	}
	if got := names(); got != [3]string{"Bad INT0000001", "Bad INT0000001:0", "Bad INT0000001:1"} {
		t.Fatalf("after create: %q", got)
	}
	// a members-only save names nothing: a name set by hand meanwhile stays
	devName, chName := "Heizung Bad", "Bad Kanal"
	if _, _, err := ms.SetObject(nil, dev, meta.ObjectPatch{Name: &devName}); err != nil {
		t.Fatal(err)
	}
	if st, out := warnCall(t, mux, "PUT", p+"/1", `{"members":["00010000000A10:1"]}`, "admin", auth.RoleAdmin); st != 200 {
		t.Fatalf("members: %d %v", st, out)
	}
	if got := names(); got != [3]string{"Heizung Bad", "Bad INT0000001:0", "Bad INT0000001:1"} {
		t.Fatalf("after a members-only save: %q", got)
	}
	// the same name sent again is no rename either
	if st, _ := warnCall(t, mux, "PUT", p+"/1", `{"name":"Bad"}`, "admin", auth.RoleAdmin); st != 200 || names()[0] != "Heizung Bad" {
		t.Fatalf("the same name: %d %q", st, names())
	}
	// a rename: the generated names follow, the device's hand-set name and a hand-set channel stay
	if _, _, err := ms.SetObject(nil, ch1, meta.ObjectPatch{Name: &chName}); err != nil {
		t.Fatal(err)
	}
	if st, out := warnCall(t, mux, "PUT", p+"/1", `{"name":"Bad oben"}`, "admin", auth.RoleAdmin); st != 200 {
		t.Fatalf("rename: %d %v", st, out)
	}
	if got := names(); got != [3]string{"Heizung Bad", "Bad oben INT0000001:0", "Bad Kanal"} {
		t.Fatalf("after the rename: %q", got)
	}
	// a missing object counts as untouched (the store keeps no empty names); a device name back in
	// the generated form follows again
	gen := "Bad oben INT0000001"
	if _, _, err := ms.SetObject(nil, dev, meta.ObjectPatch{Name: &gen}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ms.DeleteObject(nil, ch0); err != nil {
		t.Fatal(err)
	}
	if st, _ := warnCall(t, mux, "PUT", p+"/1", `{"name":"Gäste"}`, "admin", auth.RoleAdmin); st != 200 {
		t.Fatal("second rename")
	}
	if got := names(); got != [3]string{"Gäste INT0000001", "Gäste INT0000001:0", "Bad Kanal"} {
		t.Fatalf("after the second rename: %q", got)
	}
	// delete: the device's object and its channels' go
	if st, _ := warnCall(t, mux, "DELETE", p+"/1", "", "admin", auth.RoleAdmin); st != 200 {
		t.Fatal("delete")
	}
	if got := names(); got != [3]string{"<none>", "<none>", "<none>"} {
		t.Fatalf("after delete: %q", got)
	}
}

// task 42: an interface that does not name the channels - the store's channel objects are named,
// and a new group's device is asked for three times
func TestGroupsDeviceNamesWithoutChannels(t *testing.T) {
	ms, _ := meta.New(nil, nil)
	if _, _, err := ms.PutObject(nil, "VirtualDevices.INT0000003:1", "Alt INT0000003:1", nil, nil); err != nil {
		t.Fatal(err)
	}
	calls := 0
	g := &GroupsAPI{Meta: ms, Interface: "VirtualDevices", Log: slog.New(slog.DiscardHandler), ChannelsRetry: time.Millisecond,
		Channels: func(context.Context, string) ([]string, error) { calls++; return nil, errors.New("no answer") }}
	g.nameDevice(t.Context(), 3, "Neu", "")
	if calls != 3 {
		t.Fatalf("a create asks %d times, want 3", calls)
	}
	if objectName(ms, "VirtualDevices.INT0000003") != "Neu INT0000003" || objectName(ms, "VirtualDevices.INT0000003:1") != "Neu INT0000003:1" {
		t.Fatalf("names: %q %q", objectName(ms, "VirtualDevices.INT0000003"), objectName(ms, "VirtualDevices.INT0000003:1"))
	}
	calls = 0
	g.nameDevice(t.Context(), 3, "Neuer", "Neu")
	if calls != 1 || objectName(ms, "VirtualDevices.INT0000003:1") != "Neuer INT0000003:1" {
		t.Fatalf("a rename asks once (%d) and names the known channel: %q", calls, objectName(ms, "VirtualDevices.INT0000003:1"))
	}
	// a channel list that arrives on the second try; one that is not the device's is ignored
	calls = 0
	g.Channels = func(_ context.Context, d string) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return []string{d + ":0", "INT0000009:1"}, nil
	}
	g.nameDevice(t.Context(), 3, "Drei", "")
	if calls != 2 || objectName(ms, "VirtualDevices.INT0000003:0") != "Drei INT0000003:0" || objectName(ms, "VirtualDevices.INT0000009:1") != "<none>" {
		t.Fatalf("second try: %d %q", calls, objectName(ms, "VirtualDevices.INT0000003:0"))
	}
	// no store, no names
	(&GroupsAPI{}).nameDevice(t.Context(), 3, "x", "")
}

// B-102: the heal at start names the groups hmipserver lists where the store has no name, an
// empty one or the object's own address; a name set by hand stays, and a second run finds nothing.
func TestGroupsHeal(t *testing.T) {
	_, f, ms, s := groupsRig(t)
	f.Set(func(f *hmgroupstest.Fake) {
		f.Groups[3] = &hmgroupstest.Group{ID: 3, Name: "Gästetoilette", Type: "HmIP.heating", DeviceName: "Gästetoilette INT0000003"}
		f.Groups[5] = &hmgroupstest.Group{ID: 5, Name: "Kinderzimmer", Type: "HomeMatic.heating", DeviceName: "Kinderzimmer INT0000005"}
		f.Groups[7] = &hmgroupstest.Group{ID: 7, Name: "Bad", Type: "HmIP.heating", DeviceName: "Bad INT0000007"}
	})
	// what the dropped import left: the channels named by their address, no device objects
	for ref, name := range map[string]string{"VirtualDevices.INT0000003:1": "INT0000003:1", "VirtualDevices.INT0000005:1": "INT0000005:1", "VirtualDevices.INT0000007:1": "Heizung Bad", "VirtualDevices.INT0000007": "Badgruppe"} {
		if _, _, err := ms.PutObject(nil, ref, name, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	g := &GroupsAPI{Client: &hmgroups.Client{Base: f.URL(), Session: s, HTTP: &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}}, Meta: ms, Interface: "VirtualDevices", Log: slog.New(slog.DiscardHandler),
		Channels: func(_ context.Context, device string) ([]string, error) {
			return []string{device + ":0", device + ":1"}, nil
		}}
	named, err := g.Heal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantNamed := []string{"VirtualDevices.INT0000003", "VirtualDevices.INT0000003:0", "VirtualDevices.INT0000003:1", "VirtualDevices.INT0000005", "VirtualDevices.INT0000005:0", "VirtualDevices.INT0000005:1", "VirtualDevices.INT0000007:0"}
	if strings.Join(named, " ") != strings.Join(wantNamed, " ") {
		t.Fatalf("named: %q", named)
	}
	for ref, name := range map[string]string{
		"VirtualDevices.INT0000003":   "Gästetoilette INT0000003",
		"VirtualDevices.INT0000003:0": "Gästetoilette INT0000003:0",
		"VirtualDevices.INT0000003:1": "Gästetoilette INT0000003:1", // the address-only name counted as none
		"VirtualDevices.INT0000005:1": "Kinderzimmer INT0000005:1",
		"VirtualDevices.INT0000007":   "Badgruppe",   // set by hand: stays
		"VirtualDevices.INT0000007:1": "Heizung Bad", // so does this
		"VirtualDevices.INT0000007:0": "Bad INT0000007:0",
	} {
		if got := objectName(ms, ref); got != name {
			t.Errorf("%s: %q, want %q", ref, got, name)
		}
	}
	rev := ms.Revision()
	if named, err := g.Heal(t.Context()); err != nil || len(named) != 0 || ms.Revision() != rev {
		t.Fatalf("the second heal: %q %v, revision %d -> %d", named, err, rev, ms.Revision())
	}
	// HealAtStart: hmipserver not answering is tried again, and the heal runs once it does
	f.Set(func(f *hmgroupstest.Fake) { f.Broken = true })
	done := make(chan struct{})
	_, _, _ = ms.DeleteObject(nil, "VirtualDevices.INT0000003")
	go func() { defer close(done); g.HealAtStart(t.Context(), 20*time.Millisecond, 50) }()
	time.Sleep(60 * time.Millisecond)
	if objectName(ms, "VirtualDevices.INT0000003") != "<none>" {
		t.Fatal("named while hmipserver was broken")
	}
	f.Set(func(f *hmgroupstest.Fake) { f.Broken = false })
	<-done
	if got := objectName(ms, "VirtualDevices.INT0000003"); got != "Gästetoilette INT0000003" {
		t.Fatalf("after hmipserver came up: %q", got)
	}
}
