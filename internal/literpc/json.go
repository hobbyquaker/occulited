package literpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// The JSON path (D-79: JSON-RPC 2.0): the same calls in UTF-8 JSON, a batch as the multicall
// equivalent, faults as error objects. The types map as docs/rpc-remote.md says: i4/int/double
// to number, boolean to bool, dateTime.iso8601 to RFC 3339, base64 to {"base64": "..."},
// struct to object, array to array.

// ToJSON converts a daemon's value for a JSON answer.
func ToJSON(v *xmlrpc.Value) any {
	if v == nil {
		return nil
	}
	switch {
	case v.I4 != "":
		if n, err := strconv.ParseInt(strings.TrimSpace(v.I4), 10, 64); err == nil {
			return n
		}
		return v.I4
	case v.Int != "":
		if n, err := strconv.ParseInt(strings.TrimSpace(v.Int), 10, 64); err == nil {
			return n
		}
		return v.Int
	case v.Boolean != "":
		return strings.TrimSpace(v.Boolean) == "1" || strings.EqualFold(strings.TrimSpace(v.Boolean), "true")
	case v.Double != "":
		if f, err := strconv.ParseFloat(strings.TrimSpace(v.Double), 64); err == nil {
			return f
		}
		return v.Double
	case v.DateTime != "":
		if t, err := time.Parse("20060102T15:04:05", strings.TrimSpace(v.DateTime)); err == nil {
			return t.Format(time.RFC3339)
		}
		return v.DateTime
	case v.Base64 != "":
		return map[string]any{"base64": strings.TrimSpace(v.Base64)}
	case v.Struct != nil:
		m := map[string]any{}
		for _, mb := range v.Struct.Members {
			m[mb.Name] = ToJSON(mb.Value)
		}
		return m
	case v.Array != nil:
		out := make([]any, 0, len(v.Array.Data))
		for _, e := range v.Array.Data {
			out = append(out, ToJSON(e))
		}
		return out
	case v.ElemString != "":
		return v.ElemString
	}
	return v.FlatString
}

// FromJSON converts a client's JSON parameter to an XML-RPC value: an integral number to i4,
// any other to double, {"double": n} to double whatever n looks like (openccu-lite task 196, S2:
// a FLOAT datapoint written as 1 must not become an i4), {"explicitDouble": n} the same
// (homematic-xmlrpc's marker for <double>, which a client built on it may pass through
// unchanged - B-125; as a struct it reached the process and was refused), {"base64": "..."} to
// base64, an RFC 3339 string stays a string (the daemons take strings where they want dates).
func FromJSON(in any) (*xmlrpc.Value, error) {
	switch x := in.(type) {
	case nil:
		return &xmlrpc.Value{}, nil
	case bool:
		return xmlrpc.NewBool(x), nil
	case string:
		return xmlrpc.NewString(x), nil
	case json.Number:
		if n, err := x.Int64(); err == nil && n >= math.MinInt32 && n <= math.MaxInt32 {
			return xmlrpc.NewInt(int(n)), nil
		}
		f, err := x.Float64()
		if err != nil {
			return nil, err
		}
		return xmlrpc.NewFloat64(f), nil
	case float64:
		if x == math.Trunc(x) && x >= math.MinInt32 && x <= math.MaxInt32 {
			return xmlrpc.NewInt(int(x)), nil
		}
		return xmlrpc.NewFloat64(x), nil
	case int:
		return xmlrpc.NewInt(x), nil
	case []any:
		arr := make([]*xmlrpc.Value, 0, len(x))
		for _, e := range x {
			v, err := FromJSON(e)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		return &xmlrpc.Value{Array: &xmlrpc.Array{Data: arr}}, nil
	case map[string]any:
		if b, ok := x["base64"].(string); ok && len(x) == 1 {
			return &xmlrpc.Value{Base64: b}, nil
		}
		d, ok := x["double"]
		if !ok {
			d, ok = x["explicitDouble"]
		}
		if ok && len(x) == 1 {
			switch n := d.(type) {
			case json.Number:
				f, err := n.Float64()
				if err != nil {
					return nil, err
				}
				return xmlrpc.NewFloat64(f), nil
			case float64:
				return xmlrpc.NewFloat64(n), nil
			case int:
				return xmlrpc.NewFloat64(float64(n)), nil
			}
			return nil, fmt.Errorf("{\"double\": …} and {\"explicitDouble\": …} take a number")
		}
		st := &xmlrpc.Struct{}
		for k, e := range x {
			v, err := FromJSON(e)
			if err != nil {
				return nil, err
			}
			st.Members = append(st.Members, &xmlrpc.Member{Name: k, Value: v})
		}
		return &xmlrpc.Value{Struct: st}, nil
	}
	return nil, fmt.Errorf("unsupported JSON type %T", in)
}

// JSON-RPC 2.0 error codes: the standard ones, and -32000 for a process that does not answer.
const (
	jsonParseError     = -32700
	jsonInvalidRequest = -32600
	jsonMethodNotFound = -32601
	jsonInvalidParams  = -32602
	jsonDown           = -32000
)

type jsonRequest struct {
	Version string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type jsonError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonResponse struct {
	Version string          `json:"jsonrpc"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonError      `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

func jsonErr(id json.RawMessage, code int, msg string) jsonResponse {
	if id == nil {
		id = json.RawMessage("null")
	}
	return jsonResponse{Version: "2.0", Error: &jsonError{Code: code, Message: msg}, ID: id}
}

// Checker decides whether a call may go: nil, or the error to answer (a *Fault for a refusal
// the daemon would have faulted on, ErrRefused-wrapped for init and a missing tier).
type Checker func(c Call) error

// ServeJSON answers one JSON-RPC 2.0 request or batch: the response body and the HTTP status
// (200, or 400 for a body that is not JSON-RPC at all). A notification (no id) is forwarded and
// gets no response object; a batch of notifications only gets an empty body.
func (s *Service) ServeJSON(ctx context.Context, i Interface, body []byte, check Checker) ([]byte, int) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		b, _ := json.Marshal(jsonErr(nil, jsonParseError, "parse error: "+err.Error()))
		return b, 400
	}
	trimmed := strings.TrimSpace(string(raw))
	batch := strings.HasPrefix(trimmed, "[")
	var reqs []json.RawMessage
	if batch {
		if err := json.Unmarshal(raw, &reqs); err != nil || len(reqs) == 0 {
			b, _ := json.Marshal(jsonErr(nil, jsonInvalidRequest, "invalid request: an empty batch"))
			return b, 400
		}
	} else {
		reqs = []json.RawMessage{raw}
	}
	var out []jsonResponse
	for _, r := range reqs {
		resp, answer := s.serveOne(ctx, i, r, check)
		if answer {
			out = append(out, resp)
		}
	}
	if !batch {
		if len(out) == 0 {
			return nil, 204
		}
		b, _ := json.Marshal(out[0])
		return b, 200
	}
	if len(out) == 0 {
		return nil, 204
	}
	b, _ := json.Marshal(out)
	return b, 200
}

func (s *Service) serveOne(ctx context.Context, i Interface, raw json.RawMessage, check Checker) (jsonResponse, bool) {
	var req jsonRequest
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if err := d.Decode(&req); err != nil || req.Version != "2.0" || req.Method == "" {
		return jsonErr(req.ID, jsonInvalidRequest, "invalid request: jsonrpc 2.0 with a method"), true
	}
	notification := req.ID == nil
	c := Call{Method: req.Method}
	if len(req.Params) > 0 && string(req.Params) != "null" {
		var params []any
		pd := json.NewDecoder(strings.NewReader(string(req.Params)))
		pd.UseNumber()
		if err := pd.Decode(&params); err != nil {
			return jsonErr(req.ID, jsonInvalidParams, "invalid params: an array is expected"), !notification
		}
		for _, p := range params {
			v, err := FromJSON(p)
			if err != nil {
				return jsonErr(req.ID, jsonInvalidParams, "invalid params: "+err.Error()), !notification
			}
			c.Params = append(c.Params, v)
		}
	}
	if err := check(c); err != nil {
		return jsonErr(req.ID, codeOf(err), messageOf(err)), !notification
	}
	v, err := s.Forward(ctx, i, c)
	if err != nil {
		return jsonErr(req.ID, codeOf(err), messageOf(err)), !notification
	}
	if notification {
		return jsonResponse{}, false
	}
	return jsonResponse{Version: "2.0", Result: ToJSON(v), ID: req.ID}, true
}

// messageOf is the error's text: a fault's own message, else the error.
func messageOf(err error) string {
	var f *Fault
	if errors.As(err, &f) {
		return f.Message
	}
	return err.Error()
}

// codeOf maps an error to its JSON-RPC code: a daemon's fault keeps its code, init and a
// refused tier are -32601, a process down is -32000.
func codeOf(err error) int {
	var f *Fault
	switch {
	case errors.As(err, &f):
		return f.Code
	case errors.Is(err, ErrRefused):
		return jsonMethodNotFound
	case errors.Is(err, ErrDown):
		return jsonDown
	}
	return jsonDown
}
