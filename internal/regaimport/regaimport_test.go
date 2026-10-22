package regaimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/meta"
)

const sample = "I\t1035\tBidCos-RF\nI\t1036\tHmIP-RF\nI\t1007\tVirtualDevices\n" +
	"O\t1001\t1035\tJEQ9000001\tHM-CC-TC JEQ9000001\n" +
	"O\t1002\t1035\tJEQ9000001:1\tWohnzimmer Thermostat\n" +
	"O\t1003\t1035\tJEQ9000001:2\tJEQ9000001:2\n" +
	"O\t2001\t1036\t0001D0000000B2\tDimmer B\xfcro\n" +
	"O\t2002\t1036\t0001D0000000B2:3\tB\xfcro Licht\n" +
	"O\t3001\t1007\tINT0000001\tHeizgruppe\n" +
	"R\t1200\tWohnzimmer\nM\t1200\t1002\nM\t1200\t1003\n" +
	"R\t1201\tB\xfcro\nM\t1201\t2002\n" +
	"R\t1202\tBuero\nM\t1202\t9999\n" +
	"F\t1300\tHeizung\nM\t1300\t1002\n" +
	"F\t1301\tLicht\nM\t1301\t2002\n<xml><exec>/tclrega.exe</exec></xml>"

func TestParseAndConvert(t *testing.T) {
	out := latin1([]byte(sample))
	if i := strings.LastIndex(out, "<xml>"); i >= 0 {
		out = out[:i]
	}
	d, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Objects) != 6 || len(d.Rooms) != 3 || len(d.Functions) != 2 || len(d.Rooms[0].Members) != 2 {
		t.Fatalf("%+v", d)
	}
	if d.Objects[3].Name != "Dimmer Büro" {
		t.Errorf("latin1: %q", d.Objects[3].Name)
	}
	res := Convert(d, meta.Defaults())
	if res.Devices != 3 || res.Channels != 3 || res.Rooms != 3 || res.Functions != 2 || res.Unnamed != 2 {
		t.Errorf("%+v", res)
	}
	doc := res.Document
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	o := doc.Objects["BidCos-RF.JEQ9000001:1"]
	if o == nil || o.Name != "Wohnzimmer Thermostat" || strings.Join(o.Enums, ",") != "function/heizung,room/wohnzimmer" {
		t.Errorf("%+v", o)
	}
	// the unnamed channel is a room member: kept, named by address
	if o := doc.Objects["BidCos-RF.JEQ9000001:2"]; o == nil || o.Name != "JEQ9000001:2" || o.Enums[0] != "room/wohnzimmer" {
		t.Errorf("unnamed member: %+v", o)
	}
	if o := doc.Objects["HmIP-RF.0001D0000000B2:3"]; o == nil || strings.Join(o.Enums, ",") != "function/licht,room/buero" {
		t.Errorf("%+v", o)
	}
	// "Buero" collides with "Büro" on the slug and gets -2; its dangling member is ignored
	if res.Renamed["Buero"] != "buero-2" {
		t.Errorf("renamed: %v", res.Renamed)
	}
	tree := doc.Enums["room"].Tree
	if len(tree) != 3 || tree[0].ID != "wohnzimmer" || tree[1].ID != "buero" || tree[2].ID != "buero-2" {
		t.Errorf("%+v", tree)
	}
	if doc.Enums["function"] == nil || len(doc.Enums) != 2 {
		t.Errorf("defaults lost: %v", doc.Enums)
	}
	// merging into an existing tree reuses nodes by name
	base := meta.Defaults()
	base["room"].Tree = []*meta.Node{{ID: "eg", Name: "EG", Children: []*meta.Node{{ID: "wz", Name: "Wohnzimmer"}}}, {ID: "wohnzimmer", Name: "Wohnzimmer"}}
	res2 := Convert(d, base)
	if got := res2.Document.Objects["BidCos-RF.JEQ9000001:1"].Enums; !strings.Contains(strings.Join(got, ","), "room/wohnzimmer") || len(res2.Document.Enums["room"].Tree) != 4 {
		t.Errorf("merge: %v %+v", got, res2.Document.Enums["room"].Tree)
	}
	if len(base["room"].Tree) != 2 {
		t.Error("base modified")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Wohnzimmer": "wohnzimmer", "Büro EG": "buero-eg", "  Küche/Essen ": "kueche-essen", "!!!": "x", "Straße": "strasse", "Ægir": "-gir"} {
		if got := Slug(in); got != want && !(in == "Ægir" && got == "gir") {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestExecAgainstFakeRega(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tclrega.exe" || r.Method != "POST" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/xml; charset=iso-8859-1")
		w.Write([]byte(sample))
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	c := &Client{Host: host, Port: p}
	d, err := c.Fetch(context.Background())
	if err != nil || len(d.Objects) != 6 {
		t.Fatalf("%v %+v", err, d)
	}
	if d.Objects[3].Name != "Dimmer Büro" {
		t.Errorf("%q", d.Objects[3].Name)
	}
}

// B-102: the CCU's default name is the address or "<type> <address>"; on VirtualDevices the
// address is part of every group's name, so only a device type in front makes it a default.
func TestIsDefaultName(t *testing.T) {
	for _, c := range []struct {
		name, address, iface string
		want                 bool
	}{
		{"", "JEQ9000001", "BidCos-RF", true},
		{"JEQ9000001:2", "JEQ9000001:2", "BidCos-RF", true},
		{"HM-CC-TC JEQ9000001:1", "JEQ9000001:1", "BidCos-RF", true},
		{"Wohnzimmer Thermostat", "JEQ9000001:1", "BidCos-RF", false},
		{"Fenster JEQ9000001:1", "JEQ9000001:1", "BidCos-RF", true}, // the suffix rule stays for the radio interfaces
		{"INT0000003:1", "INT0000003:1", "VirtualDevices", true},
		{"Heizungsgruppe Gästetoilette INT0000003", "INT0000003", "VirtualDevices", false},
		{"Heizungsgruppe Gästetoilette INT0000003:1", "INT0000003:1", "VirtualDevices", false},
		{"Bad INT0000007", "INT0000007", "VirtualDevices", false},
		{"HmIP-HEATING INT0000003:6", "INT0000003:6", "VirtualDevices", true},
		{"HM-CC-VG-1 INT0000005", "INT0000005", "VirtualDevices", true},
		{"hmip-heating INT0000003", "INT0000003", "VirtualDevices", true},
		{"Heizgruppe", "INT0000001", "VirtualDevices", false},
	} {
		if got := isDefaultName(c.name, c.address, c.iface); got != c.want {
			t.Errorf("isDefaultName(%q, %q, %s) = %v, want %v", c.name, c.address, c.iface, got, c.want)
		}
	}
}
