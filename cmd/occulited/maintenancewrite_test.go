package main

import (
	"fmt"
	"testing"

	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// B-46: only a write to a maintenance channel (<device>:0) has the service messages read back.
func TestMaintenanceWrite(t *testing.T) {
	for addr, want := range map[string]string{"JEQ9000001:0": "JEQ9000001", "00010000000A10:0": "00010000000A10", "JEQ9000001:1": "", "JEQ9000001:10": "", "JEQ9000001": "", ":0": "", "": ""} {
		c := literpc.Call{Method: "setValue", Params: []*xmlrpc.Value{xmlrpc.NewString(addr), xmlrpc.NewString("STICKY_UNREACH"), xmlrpc.NewBool(false)}}
		if got, ok := maintenanceWrite(c); got != want && ok || ok != (want != "") {
			t.Errorf("maintenanceWrite(%q) = %q, %v", addr, got, ok)
		}
	}
	if _, ok := maintenanceWrite(literpc.Call{Method: "setValue"}); ok {
		t.Error("a call without parameters")
	}
}

// openccu-lite B-322: the datapoints a write sets to false are the confirmation of their sticky
// messages; true, another paramset or a malformed call confirm nothing.
func TestAcknowledgedKeys(t *testing.T) {
	set := func(key string, v *xmlrpc.Value) literpc.Call {
		return literpc.Call{Method: "setValue", Params: []*xmlrpc.Value{xmlrpc.NewString("JEQ9000001:0"), xmlrpc.NewString(key), v}}
	}
	put := func(paramset string, m map[string]interface{}) literpc.Call {
		v, _ := xmlrpc.NewMap(m)
		return literpc.Call{Method: "putParamset", Params: []*xmlrpc.Value{xmlrpc.NewString("JEQ9000001:0"), xmlrpc.NewString(paramset), v}}
	}
	for name, c := range map[string]struct {
		call literpc.Call
		want string
	}{
		"setValue false":       {set("STICKY_UNREACH", xmlrpc.NewBool(false)), "[STICKY_UNREACH]"},
		"setValue true":        {set("STICKY_UNREACH", xmlrpc.NewBool(true)), "[]"},
		"putParamset VALUES":   {put("VALUES", map[string]interface{}{"STICKY_UNREACH": false, "STICKY_SABOTAGE": false, "X": true}), "[STICKY_SABOTAGE STICKY_UNREACH]"},
		"putParamset MASTER":   {put("MASTER", map[string]interface{}{"STICKY_UNREACH": false}), "[]"},
		"too few parameters":   {literpc.Call{Method: "setValue", Params: []*xmlrpc.Value{xmlrpc.NewString("JEQ9000001:0")}}, "[]"},
		"another method":       {literpc.Call{Method: "getValue", Params: []*xmlrpc.Value{xmlrpc.NewString("JEQ9000001:0"), xmlrpc.NewString("STICKY_UNREACH"), xmlrpc.NewBool(false)}}, "[]"},
		"putParamset no value": {literpc.Call{Method: "putParamset", Params: []*xmlrpc.Value{xmlrpc.NewString("JEQ9000001:0"), xmlrpc.NewString("VALUES"), nil}}, "[]"},
	} {
		got := acknowledgedKeys(c.call)
		if s := fmt.Sprint(got); s != c.want && !(c.want == "[]" && len(got) == 0) {
			t.Errorf("%s: %v, want %s", name, got, c.want)
		}
	}
}
