package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/types"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Route is one route of the table with its documentation.
type Route struct {
	Method, Path string   // the mux pattern's parts; Path may hold {name} and {name...}
	Scopes       []string // the table's scopes, the route's own first; [""] is an open route
	Handler      string   // the handler's runtime name; "" for a route documented by hand only
	Doc          Doc
}

// Doc is what the analyzer cannot see: the summary above all, and where a handler does not use
// writeJSON/readJSON, what it takes and answers.
type Doc struct {
	Summary     string
	Description string
	Query       map[string]string // query parameter → description (adds to what the analyzer finds)
	Req, Resp   string            // a type as "importpath.Name" that replaces what the analyzer finds
	ReqContent  string            // a request body that is not JSON: its media type
	RespContent string            // an answer that is not JSON: its media type (a download, text/xml)
	Status      int               // the success status when the analyzer finds none (default 200)
	WebSocket   bool              // the route upgrades to a WebSocket (101)
	Stream      bool              // the route answers server-sent events (where the analyzer cannot see it)
	RespSchema  Schema            // the JSON answer's schema, written by hand (a route outside the package)
}

// Info is the document's head.
type Info struct {
	Title, Version, Description string
	License, Contact            map[string]any
}

// OpenAPI builds the OpenAPI 3.1 document of routes. Home is the import path whose types are
// named without a package prefix.
func (a *Analyzer) OpenAPI(info Info, home string, routes []Route) (map[string]any, error) {
	comps := NewComponents("#/components/schemas/", home)
	errType, err := a.Lookup(home + ".apiError")
	if err != nil {
		return nil, err
	}
	comps.names[compKey{types.TypeString(errType, nil), Response}] = "Error"
	comps.taken["Error"] = true
	comps.schemas["Error"] = comps.object(errType.Underlying().(*types.Struct), Response, 0)

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	paths := map[string]any{}
	opIDs := map[string]string{}
	tags := map[string]string{} // tag → the path prefix its routes share
	for _, r := range routes {
		if strings.TrimSpace(r.Doc.Summary) == "" {
			return nil, fmt.Errorf("apidoc: %s %s has no summary", r.Method, r.Path)
		}
		op, err := a.operation(comps, r)
		if err != nil {
			return nil, err
		}
		id := op["operationId"].(string)
		if other, dup := opIDs[id]; dup {
			return nil, fmt.Errorf("apidoc: operationId %s of %s %s is %s's too", id, r.Method, r.Path, other)
		}
		opIDs[id] = r.Method + " " + r.Path
		for _, t := range op["tags"].([]string) {
			tags[t] = commonPrefix(tags[t], r.Path)
		}
		p := openAPIPath(r.Path)
		item, _ := paths[p].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[p] = item
		}
		item[strings.ToLower(r.Method)] = op
	}
	var tagList []any
	for _, t := range sortedKeys(tags) {
		tagList = append(tagList, map[string]any{"name": t, "description": "The routes under `" + strings.TrimSuffix(tags[t], "/") + "`."})
	}
	doc := map[string]any{
		"openapi":           "3.1.1",
		"jsonSchemaDialect": "https://spec.openapis.org/oas/3.1/dialect/base",
		"info": map[string]any{
			"title": info.Title, "version": info.Version, "description": info.Description, "license": info.License, "contact": info.Contact,
		},
		"servers": []any{map[string]any{"url": "/", "description": "the system itself, over its web port"}},
		"tags":    tagList,
		"paths":   paths,
		"components": map[string]any{
			"schemas": comps.Schemas(),
			"securitySchemes": map[string]any{
				"bearer": map[string]any{
					"type": "http", "scheme": "bearer",
					"description": "A session id or an API token in `Authorization: Bearer <id>`. The route's scope is the requirement's scope; an open route has none. The API takes no credential from the query string: a `?sid=` is refused with 401. A one-time ticket goes as `?ticket=` on the GET it was made for (POST /api/auth/v1/ticket).",
				},
				"cookie": map[string]any{
					"type": "apiKey", "in": "cookie", "name": "occulite_session",
					"description": "The session cookie of the web UI (`__Secure-occulite_session` over HTTPS). lite-rpc's POST routes also need the header credential.",
				},
			},
			"responses": map[string]any{
				"Error": map[string]any{
					"description": "An error: `error` is a stable code, `message` says it in words.",
					"content":     jsonContent(comps.Ref("Error")),
				},
				"Unauthorized": map[string]any{
					"description": "No session or token (`unauthenticated`).",
					"content":     jsonContent(comps.Ref("Error")),
				},
				"Forbidden": map[string]any{
					"description": "The credential lacks the route's scope; `scope` names it.",
					"content":     jsonContent(comps.Ref("Error")),
				},
			},
		},
	}
	return doc, nil
}

// commonPrefix is the longest leading run of whole path segments a and b share; "" is none yet.
func commonPrefix(a, b string) string {
	if a == "" {
		return b
	}
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return strings.Join(as[:n], "/")
}

func jsonContent(s Schema) map[string]any {
	return map[string]any{"application/json": map[string]any{"schema": s}}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// credentialParam are the query parameters that carry a credential (a session id, a ticket): the
// middleware's, described with the security schemes, not per route.
var credentialParam = map[string]bool{"sid": true, "ticket": true}

var paramRE = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(\.\.\.)?\}`)

// openAPIPath is the mux path in OpenAPI's form: {name...} is {name}.
func openAPIPath(p string) string { return paramRE.ReplaceAllString(p, "{$1}") }

// OperationID is the operation's id: the method and the path's words after the API's version,
// path parameters as By<Name> - GET /api/system/v1/wifi/networks/{ssid} is
// getSystemWifiNetworksBySsid.
func OperationID(method, path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	var words []string
	for i, s := range segs {
		if i == 0 && s == "api" {
			continue
		}
		if len(s) >= 2 && s[0] == 'v' && unicode.IsDigit(rune(s[1])) {
			continue
		}
		if m := paramRE.FindStringSubmatch(s); m != nil {
			words = append(words, "By", camel(m[1]))
			continue
		}
		words = append(words, camel(s))
	}
	return strings.ToLower(method) + strings.Join(words, "")
}

func camel(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			up = true
			continue
		}
		if up {
			b.WriteRune(unicode.ToUpper(r))
			up = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tagOf groups a route by its API and first word: /api/system/v1/wifi/... is "system/wifi".
func tagOf(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) >= 3 && segs[0] == "api" {
		api := segs[1]
		if len(segs) >= 4 {
			w := segs[3]
			if i := strings.IndexAny(w, ":{"); i >= 0 {
				w = w[:i]
			}
			if w != "" {
				return api + "/" + w
			}
		}
		return api
	}
	return "api"
}

func (a *Analyzer) operation(comps *Components, r Route) (map[string]any, error) {
	h := &Handler{Responses: map[int][]Body{}, Headers: map[string][]string{}}
	if r.Handler != "" {
		var err error
		if h, err = a.Handler(r.Handler); err != nil {
			return nil, err
		}
	}
	op := map[string]any{
		"operationId": OperationID(r.Method, r.Path),
		"summary":     r.Doc.Summary,
		"tags":        []string{tagOf(r.Path)},
	}
	if r.Doc.Description != "" {
		op["description"] = r.Doc.Description
	}
	open := len(r.Scopes) == 0 || r.Scopes[0] == ""
	if open {
		op["security"] = []any{}
		op["x-occulite-scopes"] = []string{}
	} else {
		var sec []any
		for _, s := range r.Scopes {
			sec = append(sec, map[string]any{"bearer": []string{s}}, map[string]any{"cookie": []string{s}})
		}
		op["security"] = sec
		op["x-occulite-scopes"] = r.Scopes
	}

	var params []any
	for _, m := range paramRE.FindAllStringSubmatch(r.Path, -1) {
		p := map[string]any{"name": m[1], "in": "path", "required": true, "schema": Schema{"type": "string"}}
		if m[2] != "" {
			p["description"] = "The rest of the path; it may hold slashes."
		}
		params = append(params, p)
	}
	query := map[string]string{}
	for _, q := range h.Query {
		if !credentialParam[q] {
			query[q] = ""
		}
	}
	for q, d := range r.Doc.Query {
		query[q] = d
	}
	for _, q := range sortedKeys(query) {
		p := map[string]any{"name": q, "in": "query", "required": false, "schema": Schema{"type": "string"}}
		if query[q] != "" {
			p["description"] = query[q]
		}
		params = append(params, p)
	}
	if params != nil {
		op["parameters"] = params
	}

	// the request body
	var req Schema
	if r.Doc.Req != "" {
		t, err := a.Lookup(r.Doc.Req)
		if err != nil {
			return nil, err
		}
		req = comps.Of(t, Request)
	} else {
		req = comps.Distinct(h.Requests, Request)
	}
	switch {
	case r.Doc.ReqContent != "":
		s := Schema{"type": "string", "contentMediaType": r.Doc.ReqContent}
		if req != nil && r.Doc.ReqContent == "application/json" {
			s = req
		}
		op["requestBody"] = map[string]any{"required": true, "content": map[string]any{r.Doc.ReqContent: map[string]any{"schema": s}}}
	case req != nil:
		op["requestBody"] = map[string]any{"required": true, "content": jsonContent(req)}
	}

	// the answers
	responses := map[string]any{}
	if r.Doc.Resp != "" {
		t, err := a.Lookup(r.Doc.Resp)
		if err != nil {
			return nil, err
		}
		st := r.Doc.Status
		if st == 0 {
			st = 200
		}
		responses[fmt.Sprint(st)] = map[string]any{"description": "OK", "content": jsonContent(comps.Of(t, Response))}
	}
	if r.Doc.RespSchema != nil {
		responses["200"] = map[string]any{"description": "OK", "content": jsonContent(r.Doc.RespSchema)}
	}
	for _, st := range h.Statuses() {
		if _, done := responses[fmt.Sprint(st)]; done {
			continue
		}
		s := comps.Distinct(h.Responses[st], Response)
		responses[fmt.Sprint(st)] = map[string]any{"description": statusText(st), "content": jsonContent(s)}
	}
	switch {
	case r.Doc.WebSocket:
		responses["101"] = map[string]any{"description": "Switching to the WebSocket protocol; the messages are in the AsyncAPI document."}
	case h.Streams || r.Doc.Stream:
		responses["200"] = map[string]any{"description": "A stream of server-sent events; the messages are in the AsyncAPI document.",
			"content": map[string]any{"text/event-stream": map[string]any{"schema": Schema{"type": "string"}}}}
	case r.Doc.RespContent != "":
		responses["200"] = map[string]any{"description": "OK",
			"content": map[string]any{r.Doc.RespContent: map[string]any{"schema": Schema{"type": "string", "contentMediaType": r.Doc.RespContent}}}}
	default:
		// a body written by hand (a download, text): the media types the handler sets
		if _, typed := responses["200"]; !typed {
			content := map[string]any{}
			for _, ct := range h.Headers["Content-Type"] {
				mt, _, _ := strings.Cut(ct, ";")
				if mt != "application/json" {
					content[mt] = map[string]any{"schema": Schema{"type": "string", "contentMediaType": mt}}
				}
			}
			if len(content) > 0 {
				responses["200"] = map[string]any{"description": "OK", "content": content}
			}
		}
	}
	for _, st := range h.Empty {
		if _, done := responses[fmt.Sprint(st)]; !done && !(st == 200 && (h.Streams || r.Doc.Stream || r.Doc.WebSocket)) {
			responses[fmt.Sprint(st)] = map[string]any{"description": statusText(st)}
		}
	}
	for _, st := range h.Redirects {
		responses[fmt.Sprint(st)] = map[string]any{"description": "A redirect; `Location` says where."}
	}
	if len(responses) == 0 {
		st := r.Doc.Status
		if st == 0 {
			st = 200
		}
		responses[fmt.Sprint(st)] = map[string]any{"description": statusText(st)}
	}
	if !open {
		responses["401"] = map[string]any{"$ref": "#/components/responses/Unauthorized"}
		responses["403"] = map[string]any{"$ref": "#/components/responses/Forbidden"}
	}
	responses["default"] = map[string]any{"$ref": "#/components/responses/Error"}
	op["responses"] = responses
	return op, nil
}

func statusText(st int) string {
	switch st {
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 202:
		return "Accepted"
	case 204:
		return "No content"
	}
	return fmt.Sprint(st)
}

// Encode is a document as committed: indented, keys sorted, a newline at the end.
func Encode(doc any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
