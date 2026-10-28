package regaimport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/occulited/internal/meta"
)

// a trimmed regadom in ReGa's own shape, ISO-8859-1 with an umlaut in a room name
func sampleRegadom() []byte {
	x := `<?xml version="1.0" encoding="iso-8859-1" ?>
<dom>
<interfacemap><count>2</count>
<oid>1035</oid><ifc><obj><id>1035</id><name>BidCos-RF</name><type>458753</type></obj><ifc-url>xmlrpc_bin://127.0.0.1:32001</ifc-url></ifc>
<oid>1036</oid><ifc><obj><id>1036</id><name>HmIP-RF</name><type>458753</type></obj></ifc>
</interfacemap>
<devicemap><count>2</count>
<oid>1555</oid><device><obj><id>1555</id><name>HM-CC-TC JEQ9000001</name><type>17</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;JEQ9000001&quot;,CHILDREN:{&quot;JEQ9000001:1&quot;},INTERFACE:&quot;OEQ9000007&quot;,TYPE:&quot;HM-CC-TC&quot;]</value></metadata></obj></device>
<oid>1403</oid><device><obj><id>1403</id><name>Dimmer B&#252;ro</name><type>17</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;0001D0000000B2&quot;,CHILDREN:{&quot;0001D0000000B2:3&quot;},INTERFACE:&quot;3014F711A0&quot;]</value></metadata></obj></device>
</devicemap>
<channelmap><count>3</count>
<oid>1575</oid><channel><obj><id>1575</id><name>Wohnzimmer Thermostat</name><type>33</type><metadata><count>2</count><property>AutoconfRoles</property><value>WEATHER</value><property>DEVDESC</property><value>[ADDRESS:&quot;JEQ9000001:1&quot;,PARENT:&quot;JEQ9000001&quot;,TYPE:&quot;WEATHER&quot;]</value></metadata></obj></channel>
<oid>1578</oid><channel><obj><id>1578</id><name>HM-CC-TC JEQ9000001:2</name><type>33</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;JEQ9000001:2&quot;,PARENT:&quot;JEQ9000001&quot;]</value></metadata></obj></channel>
<oid>1410</oid><channel><obj><id>1410</id><name>B&#252;ro Licht</name><type>33</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;0001D0000000B2:3&quot;,PARENT:&quot;0001D0000000B2&quot;]</value></metadata></obj></channel>
</channelmap>
<hssdpmap><count>2</count>
<oid>1576</oid><dp><obj><id>1576</id><name>BidCos-RF.JEQ9000001:1.HUMIDITY</name><type>393281</type></obj></dp>
<oid>1579</oid><dp><obj><id>1579</id><name>BidCos-RF.JEQ9000001:2.STATE</name><type>393281</type></obj></dp>
<oid>1411</oid><dp><obj><id>1411</id><name>HmIP-RF.0001D0000000B2:3.LEVEL</name><type>393281</type></obj></dp>
</hssdpmap>
<usermap><count>1</count>
<oid>1004</oid><user><obj><id>1004</id><name>Admin</name><type>129</type></obj><userlevel>8</userlevel><name></name><favorite>65535</favorite></user>
</usermap>
<favoritemap><count>3</count>
<oid>202</oid><favorite><enum><obj><id>202</id><name>PC</name><type>10485763</type></obj><entype>6</entype><enel><count>0</count></enel></enum></favorite>
<oid>1033</oid><favorite><enum><obj><id>1033</id><name>_USER1004</name><type>10485763</type></obj><entype>6</entype><enel><count>2</count><oid>1410</oid><ot>33</ot><oid>1575</oid><ot>33</ot></enel></enum></favorite>
<oid>1034</oid><favorite><enum><obj><id>1034</id><name>Wand</name><type>10485763</type></obj><entype>6</entype><enel><count>1</count><oid>1410</oid><ot>33</ot></enel></enum></favorite>
</favoritemap>
<enummap><count>6</count>
<oid>201</oid><enum><obj><id>201</id><name>Favoriten</name><type>3</type></obj><entype>5</entype><enel><count>3</count><oid>202</oid><ot>10485763</ot><oid>1033</oid><ot>10485763</ot><oid>1034</oid><ot>10485763</ot></enel></enum>
<oid>101</oid><enum><obj><id>101</id><name>Rooms</name><type>3</type></obj><entype>1</entype><enel><count>2</count><oid>1019</oid><ot>3</ot><oid>1021</oid><ot>3</ot></enel></enum>
<oid>102</oid><enum><obj><id>102</id><name>Functions</name><type>3</type></obj><entype>1</entype><enel><count>1</count><oid>1301</oid><ot>3</ot></enel></enum>
<oid>1019</oid><enum><obj><id>1019</id><name>Wohnzimmer</name><type>3</type><metadata><count>1</count><property>trID</property><value>roomLivingRoom</value></metadata></obj><entype>2</entype><enel><count>2</count><oid>1575</oid><ot>33</ot><oid>1578</oid><ot>33</ot></enel></enum>
<oid>1021</oid><enum><obj><id>1021</id><name>B&#252;ro</name><type>3</type></obj><entype>2</entype><enel><count>1</count><oid>1410</oid><ot>33</ot></enel></enum>
<oid>1301</oid><enum><obj><id>1301</id><name>Heizung</name><type>3</type></obj><entype>2</entype><enel><count>1</count><oid>1575</oid><ot>33</ot></enel></enum>
</enummap>
</dom>
`
	// the file is ISO-8859-1: write the umlaut as the single byte 0xFC where the entity form is
	// not used (the real file uses entities for names, but the declaration must be honoured)
	return []byte(strings.ReplaceAll(x, "Küche", "K\xfcche"))
}

func TestParseRegadom(t *testing.T) {
	d, err := ParseRegadom(bytes.NewReader(sampleRegadom()))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Objects) != 5 || len(d.Rooms) != 2 || len(d.Functions) != 1 {
		t.Fatalf("%+v", d)
	}
	find := func(addr string) *DumpObject {
		for i := range d.Objects {
			if d.Objects[i].Address == addr {
				return &d.Objects[i]
			}
		}
		return nil
	}
	if o := find("JEQ9000001"); o == nil || o.Interface != "BidCos-RF" || o.Name != "HM-CC-TC JEQ9000001" {
		t.Errorf("device: %+v", o)
	}
	if o := find("0001D0000000B2:3"); o == nil || o.Interface != "HmIP-RF" || o.Name != "Büro Licht" {
		t.Errorf("hmip channel: %+v", o)
	}
	if d.Rooms[0].Name != "Wohnzimmer" || len(d.Rooms[0].Members) != 2 || d.Rooms[1].Name != "Büro" || d.Functions[0].Name != "Heizung" || d.Functions[0].Members[0] != 1575 {
		t.Errorf("enums: %+v %+v", d.Rooms, d.Functions)
	}
	res := Convert(d, meta.Defaults())
	if res.Rooms != 2 || res.Functions != 1 || res.Unnamed != 2 || len(res.Document.Objects) != 4 {
		t.Errorf("%+v objects=%d", res, len(res.Document.Objects))
	}
	if o := res.Document.Objects["BidCos-RF.JEQ9000001:1"]; o == nil || strings.Join(o.Enums, ",") != "favorite/admin,function/heizung,room/wohnzimmer" {
		t.Errorf("%+v", o)
	}
	// task 193: the favorites - the user's page under the user's name, the shared one under its
	// own, the empty factory page left out
	if len(d.Favorites) != 2 || d.Favorites[0].Name != "Admin" || len(d.Favorites[0].Members) != 2 || d.Favorites[1].Name != "Wand" {
		t.Errorf("favorites: %+v", d.Favorites)
	}
	if res.Favorites != 2 || strings.Join(res.FavoritesFor, ",") != "Admin,Wand" {
		t.Errorf("favorites result: %d %v", res.Favorites, res.FavoritesFor)
	}
	if o := res.Document.Objects["HmIP-RF.0001D0000000B2:3"]; o == nil || strings.Join(o.Enums, ",") != "favorite/admin,favorite/wand,function/heizung,room/buero" && strings.Join(o.Enums, ",") != "favorite/admin,favorite/wand,room/buero" {
		t.Errorf("the lamp's enums: %+v", o)
	}
}

func TestRegadomFromSBK(t *testing.T) {
	// usr_local.tar.gz with the regadom inside, wrapped in the .sbk tar
	var inner bytes.Buffer
	gz := gzip.NewWriter(&inner)
	tw := tar.NewWriter(gz)
	body := sampleRegadom()
	_ = tw.WriteHeader(&tar.Header{Name: "usr/local/etc/config/ids", Mode: 0o644, Size: 3})
	_, _ = tw.Write([]byte("abc"))
	_ = tw.WriteHeader(&tar.Header{Name: "usr/local/etc/config/homematic.regadom", Mode: 0o640, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	var outer bytes.Buffer
	ow := tar.NewWriter(&outer)
	_ = ow.WriteHeader(&tar.Header{Name: "signature", Mode: 0o644, Size: 2})
	_, _ = ow.Write([]byte("xx"))
	_ = ow.WriteHeader(&tar.Header{Name: "usr_local.tar.gz", Mode: 0o644, Size: int64(inner.Len())})
	_, _ = ow.Write(inner.Bytes())
	_ = ow.Close()
	path := filepath.Join(t.TempDir(), "lab.sbk")
	if err := os.WriteFile(path, outer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := RegadomFromSBK(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Objects) != 5 || len(d.Rooms) != 2 {
		t.Errorf("%+v", d)
	}
	bad := filepath.Join(t.TempDir(), "bad.sbk")
	_ = os.WriteFile(bad, outer.Bytes()[:200], 0o644)
	if _, err := RegadomFromSBK(bad); err == nil {
		t.Error("truncated backup accepted")
	}
}

// The regadom declares iso-8859-1 and is mixed: eQ-3's own strings are Latin-1, a name a user
// typed on a current firmware is UTF-8 in the same file. Measured on a real CCU3 database
// (2026-09-07): thirteen rooms where the WebUI shows eleven, "Büro" and "Küche" each once per
// encoding. Both spellings have to come out as the same name.
func TestFixMojibake(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// what a Latin-1 decode makes of UTF-8 bytes: undone
		{"BÃ¼ro", "Büro"},
		{"KÃ¼che", "Küche"},
		// the Latin-1 decode of UTF-8 "Ö" and "ß" is C3 + a C1 control, not the CP1252 glyphs a
		// terminal shows for them - write the runes, not what they look like
		{"\u00c3\u0096lheizung", "Ölheizung"},
		{"Stra\u00c3\u009fe", "Straße"},
		// a name that really was Latin-1: the decode was right, leave it
		{"Büro", "Büro"},
		{"°C", "°C"},
		{"Kinderzimmer 1", "Kinderzimmer 1"},
		{"", ""},
		// already beyond Latin-1, so not something a Latin-1 decode produced
		{"Küche ☂", "Küche ☂"},
	} {
		if got := fixMojibake(c.in); got != c.want {
			t.Errorf("fixMojibake(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// B-38 made a control character in a name a validation error. A CCU's own names go through
// cleanName instead, so one stray character cannot cost a whole import.
func TestCleanName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Wohnzimmer", "Wohnzimmer"},
		{"B\u00c3\u00bcro", "B\u00fcro"},     // the encoding repair still happens
		{"Bad \u0000 1", "Bad   1"},          // a NUL becomes a space
		{"Flur\u001bX", "Flur X"},            // so does an ESC
		{"a\tb", "a b"},                      // and a tab
		{"Stra\u00c3\u009fe", "Stra\u00dfe"}, // repaired first, then cleaned
	} {
		if got := cleanName(c.in); got != c.want {
			t.Errorf("cleanName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// occulited B-124: a store converted on 2026-09-06, the day before B-37's fix, held "BÃ¼ro" and
// "KÃ¼che" (ids b-ro, k-che) beside "Büro" and "Küche". The source was a ReGa database with two
// room objects per name: the factory rooms eQ-3 wrote in Latin-1, and the ones a user typed on a
// current firmware in UTF-8. Every way a CCU writes such a name - the Latin-1 byte, its entity,
// the UTF-8 bytes and their entities - comes out of both import paths (the regadom a switch reads
// at first boot, an upload or a backup; the live script over 8181) as one name, so both rooms of a
// name land in one node with the members of both, and no "Ã" reaches the store.
func TestMixedEncodingRoomsMerge(t *testing.T) {
	regadom := `<?xml version="1.0" encoding="iso-8859-1" ?>
<dom>
<interfacemap><count>1</count>
<oid>1036</oid><ifc><obj><id>1036</id><name>HmIP-RF</name><type>458753</type></obj></ifc>
</interfacemap>
<devicemap><count>1</count>
<oid>1400</oid><device><obj><id>1400</id><name>Schaltaktor</name><type>17</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;0001D0000000B2&quot;,CHILDREN:{&quot;0001D0000000B2:1&quot;,&quot;0001D0000000B2:2&quot;},INTERFACE:&quot;3014F711A0&quot;]</value></metadata></obj></device>
</devicemap>
<channelmap><count>2</count>
<oid>1401</oid><channel><obj><id>1401</id><name>Licht 1</name><type>33</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;0001D0000000B2:1&quot;,PARENT:&quot;0001D0000000B2&quot;]</value></metadata></obj></channel>
<oid>1402</oid><channel><obj><id>1402</id><name>Licht 2</name><type>33</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;0001D0000000B2:2&quot;,PARENT:&quot;0001D0000000B2&quot;]</value></metadata></obj></channel>
</channelmap>
<hssdpmap><count>1</count>
<oid>1403</oid><dp><obj><id>1403</id><name>HmIP-RF.0001D0000000B2:1.STATE</name><type>393281</type></obj></dp>
</hssdpmap>
<enummap><count>5</count>
<oid>101</oid><enum><obj><id>101</id><name>Rooms</name><type>3</type></obj><entype>1</entype><enel><count>4</count><oid>1020</oid><ot>3</ot><oid>1024</oid><ot>3</ot><oid>1631</oid><ot>3</ot><oid>1632</oid><ot>3</ot></enel></enum>
<oid>1020</oid><enum><obj><id>1020</id><name>K` + "\xfc" + `che</name><type>3</type></obj><entype>2</entype><enel><count>1</count><oid>1401</oid><ot>33</ot></enel></enum>
<oid>1024</oid><enum><obj><id>1024</id><name>B&#252;ro</name><type>3</type></obj><entype>2</entype><enel><count>1</count><oid>1401</oid><ot>33</ot></enel></enum>
<oid>1631</oid><enum><obj><id>1631</id><name>B` + "\xc3\xbc" + `ro</name><type>3</type></obj><entype>2</entype><enel><count>1</count><oid>1402</oid><ot>33</ot></enel></enum>
<oid>1632</oid><enum><obj><id>1632</id><name>K&#195;&#188;che</name><type>3</type></obj><entype>2</entype><enel><count>1</count><oid>1402</oid><ot>33</ot></enel></enum>
</enummap>
</dom>
`
	live := "I\t1036\tHmIP-RF\n" +
		"O\t1400\t1036\t0001D0000000B2\tSchaltaktor\n" +
		"O\t1401\t1036\t0001D0000000B2:1\tLicht 1\n" +
		"O\t1402\t1036\t0001D0000000B2:2\tLicht 2\n" +
		"R\t1020\tK\xfcche\nM\t1020\t1401\n" +
		"R\t1024\tB\xfcro\nM\t1024\t1401\n" +
		"R\t1631\tB\xc3\xbcro\nM\t1631\t1402\n" +
		"R\t1632\tK\xc3\xbcche\nM\t1632\t1402\n"
	fromFile, err := ParseRegadom(strings.NewReader(regadom))
	if err != nil {
		t.Fatal(err)
	}
	fromScript, err := Parse(latin1([]byte(live))) // what Exec hands Parse
	if err != nil {
		t.Fatal(err)
	}
	for path, d := range map[string]*Dump{"regadom": fromFile, "script": fromScript} {
		if len(d.Rooms) != 4 {
			t.Fatalf("%s: %d rooms, want the 4 ReGa objects: %+v", path, len(d.Rooms), d.Rooms)
		}
		res := Convert(d, meta.Defaults())
		tree := res.Document.Enums["room"].Tree
		var got []string
		for _, n := range tree {
			got = append(got, n.ID+"="+n.Name)
			if strings.ContainsRune(n.Name, 'Ã') {
				t.Errorf("%s: mojibake reached the tree: %q", path, n.Name)
			}
		}
		if res.Rooms != 2 || strings.Join(got, ",") != "kueche=Küche,buero=Büro" {
			t.Errorf("%s: rooms %d %v, want kueche=Küche,buero=Büro", path, res.Rooms, got)
		}
		for _, ref := range []string{"HmIP-RF.0001D0000000B2:1", "HmIP-RF.0001D0000000B2:2"} {
			if o := res.Document.Objects[ref]; o == nil || strings.Join(o.Enums, ",") != "room/buero,room/kueche" {
				t.Errorf("%s: %s: %+v", path, ref, o)
			}
		}
	}
}

// B-102: ReGa names a heating group's device "<group name> INT000000N" and its channels
// "<group name> INT000000N:n" - names the import took for the CCU's default "<type> <address>"
// and dropped, every group of a production system with them (52 objects). They are imported now;
// a group channel the CCU itself named by type (HmIP-HEATING INT0000003:6, added by a firmware
// update) still counts as unnamed, and a BidCos device's default name does too.
func TestRegadomHeatingGroups(t *testing.T) {
	x := `<?xml version="1.0" encoding="iso-8859-1" ?>
<dom>
<interfacemap><count>2</count>
<oid>1035</oid><ifc><obj><id>1035</id><name>BidCos-RF</name><type>458753</type></obj></ifc>
<oid>1007</oid><ifc><obj><id>1007</id><name>VirtualDevices</name><type>458753</type></obj></ifc>
</interfacemap>
<devicemap><count>2</count>
<oid>1555</oid><device><obj><id>1555</id><name>HM-CC-TC JEQ9000001</name><type>17</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;JEQ9000001&quot;,CHILDREN:{&quot;JEQ9000001:1&quot;}]</value></metadata></obj></device>
<oid>19554</oid><device><obj><id>19554</id><name>Heizungsgruppe G&#228;stetoilette INT0000003</name><type>17</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;INT0000003&quot;,CHILDREN:{&quot;INT0000003:1&quot;,&quot;INT0000003:6&quot;},TYPE:&quot;HmIP-HEATING&quot;]</value></metadata></obj></device>
</devicemap>
<channelmap><count>3</count>
<oid>1575</oid><channel><obj><id>1575</id><name>Wohnzimmer Thermostat</name><type>33</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;JEQ9000001:1&quot;,PARENT:&quot;JEQ9000001&quot;]</value></metadata></obj></channel>
<oid>19585</oid><channel><obj><id>19585</id><name>Heizungsgruppe G&#228;stetoilette INT0000003:1</name><type>33</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;INT0000003:1&quot;,PARENT:&quot;INT0000003&quot;]</value></metadata></obj></channel>
<oid>22738</oid><channel><obj><id>22738</id><name>HmIP-HEATING INT0000003:6</name><type>33</type><metadata><count>1</count><property>DEVDESC</property><value>[ADDRESS:&quot;INT0000003:6&quot;,PARENT:&quot;INT0000003&quot;]</value></metadata></obj></channel>
</channelmap>
<hssdpmap><count>3</count>
<oid>1576</oid><dp><obj><id>1576</id><name>BidCos-RF.JEQ9000001:1.HUMIDITY</name><type>393281</type></obj></dp>
<oid>19590</oid><dp><obj><id>19590</id><name>VirtualDevices.INT0000003:1.SET_TEMPERATURE</name><type>393281</type></obj></dp>
<oid>22740</oid><dp><obj><id>22740</id><name>VirtualDevices.INT0000003:6.STATE</name><type>393281</type></obj></dp>
</hssdpmap>
<enummap><count>2</count>
<oid>101</oid><enum><obj><id>101</id><name>Rooms</name><type>3</type></obj><entype>1</entype><enel><count>1</count><oid>1019</oid><ot>3</ot></enel></enum>
<oid>1019</oid><enum><obj><id>1019</id><name>Bad</name><type>3</type></obj><entype>2</entype><enel><count>1</count><oid>22738</oid><ot>33</ot></enel></enum>
</enummap>
</dom>
`
	d, err := ParseRegadom(strings.NewReader(x))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Objects) != 5 {
		t.Fatalf("objects: %+v", d.Objects)
	}
	res := Convert(d, meta.Defaults())
	// unnamed: the BidCos device (HM-CC-TC JEQ9000001) and the type-named group channel (:6)
	if res.Devices != 2 || res.Channels != 3 || res.Unnamed != 2 {
		t.Errorf("%+v", res)
	}
	want := map[string]string{
		"VirtualDevices.INT0000003":   "Heizungsgruppe Gästetoilette INT0000003",
		"VirtualDevices.INT0000003:1": "Heizungsgruppe Gästetoilette INT0000003:1",
		"VirtualDevices.INT0000003:6": "INT0000003:6", // unnamed, kept by its room, named by address
		"BidCos-RF.JEQ9000001:1":      "Wohnzimmer Thermostat",
	}
	for ref, name := range want {
		if o := res.Document.Objects[ref]; o == nil || o.Name != name {
			t.Errorf("%s: %+v, want %q", ref, o, name)
		}
	}
	if res.Document.Objects["BidCos-RF.JEQ9000001"] != nil {
		t.Error("the BidCos device's default name was imported")
	}
	if err := res.Document.Validate(); err != nil {
		t.Fatal(err)
	}
}
