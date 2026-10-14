package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/apidoc"
	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// The API documents (openccu-lite task 298): docs/openapi.json, docs/asyncapi.json and
// docs/lite-rpc-methods.json are generated from the route table, the handlers' source and the
// summaries in apidocs_routes_test.go. The test fails when a route has no summary, a summary names
// no route, or a committed document differs from the generated one;
//
//	OCCULITED_UPDATE_DOCS=1 go test ./internal/httpapi -run TestAPIDocuments
//
// rewrites them.

const home = "github.com/hobbyquaker/occulited/internal/httpapi"

// docRoutes is the table joined with the summaries; missing lists the routes without one and
// stale the summaries without a route.
func docRoutes(t *testing.T) (routes []apidoc.Route, missing, stale []string) {
	t.Helper()
	fullAPI(t)
	routesMu.Lock()
	defer routesMu.Unlock()
	for pattern, scopes := range routeScopes {
		d, ok := routeDocs[pattern]
		if !ok || strings.TrimSpace(d.Summary) == "" {
			missing = append(missing, pattern)
			continue
		}
		method, path, _ := strings.Cut(pattern, " ")
		var sc []string
		for _, s := range scopes {
			sc = append(sc, string(s))
		}
		routes = append(routes, apidoc.Route{Method: method, Path: path, Scopes: sc, Handler: routeHandlers[pattern], Doc: d})
	}
	for pattern := range routeDocs {
		if _, ok := routeScopes[pattern]; !ok && !extraRoute(pattern) {
			stale = append(stale, pattern)
		}
	}
	for pattern, d := range extraDocs {
		method, path, _ := strings.Cut(pattern, " ")
		routes = append(routes, apidoc.Route{Method: method, Path: path, Scopes: []string{""}, Doc: d})
	}
	sort.Strings(missing)
	sort.Strings(stale)
	return routes, missing, stale
}

func extraRoute(pattern string) bool { _, ok := extraDocs[pattern]; return ok }

func TestEveryRouteDocumented(t *testing.T) {
	_, missing, stale := docRoutes(t)
	for _, p := range missing {
		t.Errorf("%s has no summary in apidocs_routes_test.go", p)
	}
	for _, p := range stale {
		t.Errorf("apidocs_routes_test.go documents %s, which the table does not have", p)
	}
}

func TestAPIDocuments(t *testing.T) {
	routes, missing, stale := docRoutes(t)
	if len(missing)+len(stale) > 0 {
		t.Skip("TestEveryRouteDocumented fails first")
	}
	an, err := apidoc.Load(".", home, "github.com/hobbyquaker/occulited/internal/literpc")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := an.OpenAPI(openAPIInfo("dev"), home, routes)
	if err != nil {
		t.Fatal(err)
	}
	b, err := apidoc.Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	checkDocument(t, "openapi.json", b)

	async, err := an.AsyncAPI(openAPIInfo("dev"), home, streamChannels(t, an))
	if err != nil {
		t.Fatal(err)
	}
	if b, err = apidoc.Encode(async); err != nil {
		t.Fatal(err)
	}
	checkDocument(t, "asyncapi.json", b)

	if b, err = apidoc.Encode(methodCatalogue(t)); err != nil {
		t.Fatal(err)
	}
	checkDocument(t, "lite-rpc-methods.json", b)

	// every stream of the REST document is a channel of the AsyncAPI one
	channels := map[string]bool{}
	for _, c := range streamChannels(t, an) {
		channels[c.Address] = true
	}
	for p, item := range doc["paths"].(map[string]any) {
		for _, op := range item.(map[string]any) {
			res := op.(map[string]any)["responses"].(map[string]any)
			_, ws := res["101"]
			sse := false
			if ok, _ := res["200"].(map[string]any); ok != nil {
				if c, _ := ok["content"].(map[string]any); c != nil {
					_, sse = c["text/event-stream"]
				}
			}
			if (ws || sse) && !channels[p] {
				t.Errorf("%s streams but has no channel in streamChannels", p)
			}
		}
	}
}

func checkDocument(t *testing.T, name string, b []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "docs", name)
	if os.Getenv("OCCULITED_UPDATE_DOCS") != "" {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("docs/%s: %v (OCCULITED_UPDATE_DOCS=1 go test ./internal/httpapi -run TestAPIDocuments writes it)", name, err)
	}
	if !bytes.Equal(have, b) {
		t.Errorf("docs/%s differs from the generated document: OCCULITED_UPDATE_DOCS=1 go test ./internal/httpapi -run TestAPIDocuments rewrites it", name)
	}
}

func openAPIInfo(version string) apidoc.Info {
	return apidoc.Info{
		Title:   "occulited",
		Version: version,
		Description: "The REST API of occulited, openccu-lite's system service: metadata (`/api/meta/v1`), " +
			"accounts and sessions (`/api/auth/v1`), system administration (`/api/system/v1`) and lite-rpc (`/api/rpc/v1`). " +
			"Generated from the route table and the handlers' source; the prose in docs/meta-api.md (normative) and " +
			"docs/system-api.md says what each route does in detail. The event streams are in docs/asyncapi.json, " +
			"lite-rpc's methods per scope in docs/lite-rpc-methods.json.",
		License: map[string]any{"name": "GPL-3.0-only", "identifier": "GPL-3.0-only"},
		Contact: map[string]any{"name": "openccu-lite", "url": "https://github.com/hobbyquaker/openccu-lite/issues"},
	}
}

// GET /api/openapi.json answers the committed document with system:read, and refuses without a
// credential (401) and with a token that lacks the scope (403).
func TestOpenAPIServed(t *testing.T) {
	srv, store := fullAPI(t)
	read, err := store.CreateToken("docs", auth.TokenOptions{Scopes: auth.Scopes{auth.ScopeSystemRead}})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.CreateToken("meta", auth.TokenOptions{Scopes: auth.Scopes{auth.ScopeMetaRead}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(bearer string) (int, []byte) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/openapi.json", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	if st, _ := get(""); st != 401 {
		t.Errorf("without a credential: %d, want 401", st)
	}
	if st, _ := get(meta); st != 403 {
		t.Errorf("with meta:read: %d, want 403", st)
	}
	st, b := get(read)
	if st != 200 {
		t.Fatalf("with system:read: %d %s", st, b)
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	want := strings.TrimPrefix(Implementation, "occulited ")
	if !strings.HasPrefix(doc.OpenAPI, "3.1.") || doc.Info.Version != want || doc.Paths["/api/openapi.json"] == nil {
		t.Errorf("served: openapi %q, version %q (want %q), %d paths", doc.OpenAPI, doc.Info.Version, want, len(doc.Paths))
	}
}

const literpcPkg = "github.com/hobbyquaker/occulited/internal/literpc"

// streamChannels is every event stream of the API, for docs/asyncapi.json. The payloads come from
// the source: lite-rpc's from literpc's Run (hello, resync, the devices snapshot) and convert (the
// bus messages), the others from what their handlers marshal.
func streamChannels(t *testing.T, an *apidoc.Analyzer) []apidoc.Channel {
	t.Helper()
	must := func(m []apidoc.Message, err error) []apidoc.Message {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	scopes := func(pattern string) []string {
		var out []string
		for _, s := range routeScopes[pattern] {
			out = append(out, string(s))
		}
		if out == nil {
			t.Fatalf("%s is not in the table", pattern)
		}
		return out
	}
	// lite-rpc: the messages by name; a bus message whose name is computed is a devices change
	lite := map[string][]apidoc.Body{}
	for _, m := range must(an.Calls(literpcPkg+".(*Service).Run", "send", 1, 2)) {
		if m.Name != "" {
			lite[m.Name] = append(lite[m.Name], m.Body)
		}
	}
	var devices []apidoc.Body
	for _, m := range must(an.Returns(literpcPkg+".convert", 0, 1)) {
		switch {
		case m.Name != "":
			lite[m.Name] = append(lite[m.Name], m.Body)
		case slices.Contains(m.Body.Keys(), "addresses"):
			devices = append(devices, m.Body)
		}
	}
	for _, name := range []string{"newDevices", "deleteDevices", "updateDevice", "replaceDevice", "readdedDevice"} {
		lite[name] = append(lite[name], devices...)
	}
	liteSummary := map[string]string{
		"hello":         "The stream's first message: the process's boot id, where the ring is, the interfaces, the buffer",
		"resync":        "The stream could not resume after the client's Last-Event-ID (reason gap or boot): read the state again",
		"event":         "A datapoint's value as an interface process reported it",
		"state":         "The state store's sweep confirmed or changed a datapoint",
		"interface":     "An interface process came up, went down, restarted, was added or removed",
		"firmware":      "A device's firmware update state changed, or its background transfer moved (percent from hmipserver's journal)",
		"newDevices":    "Devices were paired; with ?devices=true also each interface's device list at the start",
		"deleteDevices": "Devices were deleted",
		"updateDevice":  "A device's description changed",
		"replaceDevice": "A device was replaced by another",
		"readdedDevice": "Devices were paired again",
	}
	var liteMsgs []apidoc.ChannelMessage
	for _, name := range sortedNames(lite) {
		if liteSummary[name] == "" {
			t.Errorf("lite-rpc message %s has no summary in streamChannels", name)
		}
		liteMsgs = append(liteMsgs, apidoc.ChannelMessage{Name: name, Summary: liteSummary[name], Bodies: lite[name]})
	}
	marshal := func(fn string) []apidoc.Body {
		var out []apidoc.Body
		for _, m := range must(an.Calls(home+fn, "Marshal", -1, 0)) {
			out = append(out, m.Body)
		}
		return out
	}
	var metaBodies []apidoc.Body
	for _, m := range must(an.Calls(home+".(*MetaAPI).sse", "send", -1, 0)) {
		metaBodies = append(metaBodies, m.Body)
	}
	const liteDesc = "Query parameters: interface, address, key (datapoint), type filter the messages (comma-separated or repeated); " +
		"devices=true adds each interface's device list at the start; the last event id (Last-Event-ID, or ?last_event_id=) resumes. " +
		"Each message but resync and the devices list carries an id `<boot>:<seq>`."
	return []apidoc.Channel{
		{ID: "liteEvents", Address: "/api/rpc/v1/events", Scopes: scopes("GET /api/rpc/v1/events"),
			Summary: "lite-rpc: the interface processes' events as server-sent events", Description: liteDesc, Messages: liteMsgs},
		{ID: "liteEventsWS", Address: "/api/rpc/v1/events/ws", WebSocket: true, Scopes: scopes("GET /api/rpc/v1/events/ws"),
			Summary: "lite-rpc: the same events over a WebSocket, one frame {type, id?, data} per message", Description: liteDesc, Messages: liteMsgs},
		{ID: "metaEvents", Address: "/api/meta/v1/events/sse", Scopes: scopes("GET /api/meta/v1/events/sse"),
			Summary: "The metadata changes, one per revision", Description: "?since=<revision> replays the changes after it, or sends a resync when the log no longer holds them.",
			Messages: []apidoc.ChannelMessage{{Name: "message", Summary: "A change (kind names it) or a resync", Bodies: metaBodies}}},
		{ID: "logStream", Address: "/api/system/v1/log/stream", Scopes: scopes("GET /api/system/v1/log/stream"),
			Summary: "The journal as it grows, with the filters of GET /api/system/v1/log", Messages: []apidoc.ChannelMessage{
				{Name: "message", Summary: "One journal line", Bodies: marshal(".(*SystemAPI).logStream")},
				{Name: "error", Summary: "The journal could not be followed; the stream ends", Payload: apidoc.Schema{"type": "string"}},
			}},
		{ID: "addons", Address: "/api/system/v1/addons/stream", Scopes: scopes("GET /api/system/v1/addons/stream"),
			Summary: "The addons' revision, at once and again whenever the installed addons or the menu may have changed", Messages: []apidoc.ChannelMessage{
				{Name: "addons", Summary: "{revision}: read GET /addons and GET /nav again when it differs from the one seen before", Bodies: marshal(".(*SystemAPI).addonsStream")},
			}},
		{ID: "serviceMessages", Address: "/api/system/v1/service-messages/stream", Scopes: scopes("GET /api/system/v1/service-messages/stream"),
			Summary: "The service messages, the whole view again on every change", Messages: []apidoc.ChannelMessage{
				{Name: "messages", Summary: "The view of GET /api/system/v1/service-messages", Bodies: marshal(".(*SystemAPI).serviceMessagesStream")},
			}},
		{ID: "pairing", Address: "/api/auth/v1/pairing/stream", Scopes: scopes("GET /api/auth/v1/pairing/stream"),
			Summary: "Client pairing: the requests, the whole view again on every change", Messages: []apidoc.ChannelMessage{
				{Name: "pairing", Summary: "The view of GET /api/auth/v1/pairing", Bodies: marshal(".(*AuthAPI).pairingStream")},
			}},
		{ID: "shellStream", Address: "/api/system/v1/stream", Scopes: scopes("GET /api/system/v1/stream"),
			Summary: "The shell's stream (occulited B-53): the addons' revision, the service messages and the pairing requests in one connection",
			Description: "Query parameter topics: addons, service-messages, pairing (comma-separated or repeated; at least one). " +
				"Each topic sends the event of its own stream at once and again on every change; pairing needs auth:admin besides system:read. " +
				"The public mode may open it with topics=service-messages alone.",
			Messages: []apidoc.ChannelMessage{
				{Name: "addons", Summary: "topic addons: as GET /api/system/v1/addons/stream", Bodies: marshal(".(*SystemAPI).addonsStream")},
				{Name: "messages", Summary: "topic service-messages: as GET /api/system/v1/service-messages/stream", Bodies: marshal(".(*SystemAPI).serviceMessagesStream")},
				{Name: "pairing", Summary: "topic pairing: as GET /api/auth/v1/pairing/stream", Bodies: marshal(".(*AuthAPI).pairingStream")},
			}},
	}
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// methodCatalogue is docs/lite-rpc-methods.json: the methods lite-rpc forwards and the scope each
// needs, from literpc's tier table, with Tier's parameter rules checked against the table here.
func methodCatalogue(t *testing.T) map[string]any {
	t.Helper()
	methods := literpc.Methods()
	tiers := map[string][]string{}
	flat := map[string]string{}
	for m, s := range methods {
		tiers[string(s)] = append(tiers[string(s)], m)
		flat[m] = string(s)
	}
	for _, list := range tiers {
		sort.Strings(list)
	}
	includes := map[string][]string{}
	rpc := []auth.Scope{auth.ScopeRPCRead, auth.ScopeRPCOperate, auth.ScopeRPCConfigure, auth.ScopeRPCAdmin}
	for _, have := range rpc {
		includes[string(have)] = []string{}
		for _, want := range rpc {
			if want != have && auth.Covers(have, want) {
				includes[string(have)] = append(includes[string(have)], string(want))
			}
		}
	}
	rules := []map[string]any{
		{"method": "logLevel", "when": "called without a parameter (reading the level)", "scope": string(auth.ScopeRPCRead)},
		{"method": "putParamset", "when": "its second parameter is the paramset key VALUES (operating a device)", "scope": string(auth.ScopeRPCOperate)},
	}
	// the rules are Tier's: check them, so the catalogue cannot promise what the code does not do
	vals := xmlrpc.Value{FlatString: "VALUES"}
	for _, c := range []struct {
		call literpc.Call
		want auth.Scope
	}{
		{literpc.Call{Method: "logLevel"}, auth.ScopeRPCRead},
		{literpc.Call{Method: "logLevel", Params: []*xmlrpc.Value{{FlatString: "3"}}}, methods["logLevel"]},
		{literpc.Call{Method: "putParamset", Params: []*xmlrpc.Value{{FlatString: "ABC0000001:1"}, &vals, {}}}, auth.ScopeRPCOperate},
		{literpc.Call{Method: "putParamset", Params: []*xmlrpc.Value{{FlatString: "ABC0000001:1"}, {FlatString: "MASTER"}, {}}}, methods["putParamset"]},
		{literpc.Call{Method: "noSuchMethod"}, auth.ScopeRPCAdmin},
	} {
		if got := literpc.Tier(c.call); got != c.want {
			t.Errorf("Tier(%s %d params) = %s, the catalogue says %s", c.call.Method, len(c.call.Params), got, c.want)
		}
	}
	return map[string]any{
		"$comment": "Generated from occulited's tier table (internal/literpc/tiers.go) by internal/httpapi's tests; " +
			"OCCULITED_UPDATE_DOCS=1 go test ./internal/httpapi -run TestAPIDocuments rewrites it.",
		"description": "lite-rpc (POST /api/rpc/v1/xmlrpc/{interface}, /api/rpc/v1/json/{interface}): the scope a call needs. " +
			"Every route needs rpc:read; each call also the scope of its method (includes: what each scope covers besides itself). " +
			"A system.multicall (or a JSON-RPC batch) is checked per inner call.",
		"includes": includes,
		"refused":  []string{"init"},
		"unknown":  string(auth.ScopeRPCAdmin),
		"rules":    rules,
		"tiers":    tiers,
		"methods":  flat,
	}
}
