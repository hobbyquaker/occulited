package regaimport

import (
	"errors"
	"testing"

	"github.com/hobbyquaker/occulited/internal/meta"
)

// occulited B-84: objects of ReGa's System pseudo-interface - and, where the system's interfaces
// are known, of an interface it does not have - are not imported; the store's old ones go at the
// start. Control showed them and asked lite-rpc for "System".
func TestConvertLeavesOutForeignInterfaces(t *testing.T) {
	d := &Dump{
		Objects: []DumpObject{
			{ID: 1, Interface: "HmIP-RF", Address: "00010000000A10", Name: "Remote hallway"},
			{ID: 2, Interface: "HmIP-RF", Address: "00010000000A10:1", Name: "Remote hallway key"},
			{ID: 3, Interface: "System", Address: "BidCoS-RF", Name: "Zentrale"},
			{ID: 4, Interface: "System", Address: "BidCoS-RF:1", Name: "Zentrale Taste 1"},
			{ID: 5, Interface: "CUxD", Address: "CUX2801001:1", Name: "Exec"},
		},
		Rooms: []DumpEnum{{ID: 100, Name: "Flur", Members: []int{2, 4, 5}}},
	}
	res := Convert(d, meta.Defaults())
	if res.Foreign != 2 || res.Document.Objects["System.BidCoS-RF"] != nil || res.Document.Objects["System.BidCoS-RF:1"] != nil {
		t.Fatalf("System kept: foreign %d, %v", res.Foreign, res.Document.Objects)
	}
	// without a list of interfaces another one is taken
	if res.Document.Objects["CUxD.CUX2801001:1"] == nil || res.Document.Objects["HmIP-RF.00010000000A10:1"] == nil {
		t.Fatalf("objects missing: %v", res.Document.Objects)
	}
	if res.Devices != 1 || res.Channels != 2 || res.Rooms != 1 {
		t.Errorf("counts: %+v", res)
	}
	// with the system's interfaces: CUxD is not among them
	known := func(name string) bool { return name == "HmIP-RF" || name == "BidCos-RF" }
	res = ConvertKnown(d, meta.Defaults(), known)
	if res.Foreign != 3 || res.Document.Objects["CUxD.CUX2801001:1"] != nil {
		t.Fatalf("CUxD kept: foreign %d, %v", res.Foreign, res.Document.Objects)
	}
	// the room keeps its one member of this system
	o := res.Document.Objects["HmIP-RF.00010000000A10:1"]
	if o == nil || len(o.Enums) != 1 || o.Enums[0] != "room/flur" {
		t.Errorf("member: %+v", o)
	}
	if len(res.Document.Objects) != 2 {
		t.Errorf("objects: %v", res.Document.Objects)
	}
	// System is left out whatever the list says
	if !ForeignInterface("System", func(string) bool { return true }) || ForeignInterface("HmIP-RF", nil) {
		t.Error("ForeignInterface")
	}
}

func TestDropPseudoObjects(t *testing.T) {
	doc := meta.NewDocument()
	doc.Objects["System.BidCoS-RF"] = &meta.Object{Name: "Zentrale", Enums: []string{}}
	doc.Objects["System.BidCoS-RF:1"] = &meta.Object{Name: "Zentrale Taste 1", Enums: []string{}}
	doc.Objects["HmIP-RF.00010000000A10"] = &meta.Object{Name: "Remote hallway", Enums: []string{}}
	// an interface whose name only starts like it stays, and so does any other interface
	doc.Objects["SystemX.00010000000A11"] = &meta.Object{Name: "Other", Enums: []string{}}
	doc.Objects["CUxD.CUX2801001:1"] = &meta.Object{Name: "CUxD", Enums: []string{}}
	s, err := meta.New(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	rev := s.Revision()
	dropped, err := DropPseudoObjects(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 2 || dropped[0] != "System.BidCoS-RF" || dropped[1] != "System.BidCoS-RF:1" {
		t.Errorf("dropped %v", dropped)
	}
	if s.Revision() != rev+1 {
		t.Errorf("revision %d, want one bump from %d", s.Revision(), rev)
	}
	snap := s.Snapshot()
	if len(snap.Objects) != 3 || snap.Objects["HmIP-RF.00010000000A10"] == nil || snap.Objects["SystemX.00010000000A11"] == nil || snap.Objects["CUxD.CUX2801001:1"] == nil {
		t.Errorf("left: %v", snap.Objects)
	}
	// a second start finds nothing and changes nothing
	if dropped, err := DropPseudoObjects(s); err != nil || len(dropped) != 0 || s.Revision() != rev+1 {
		t.Errorf("second run: %v %v rev %d", dropped, err, s.Revision())
	}
	// a store that cannot save says so
	bad, err := meta.New(doc, func(*meta.Document) error { return errors.New("disk full") })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DropPseudoObjects(bad); err == nil {
		t.Error("no error from a failing save")
	}
}

// The maintainer (2026-10-20): "never automatic" - ForeignObjects only lists, per interface, what
// the button would remove; with no list read it lists nothing.
func TestForeignObjects(t *testing.T) {
	doc := meta.NewDocument()
	for _, ref := range []string{"System.BidCoS-RF:1", "CUxD.CUX2801001", "CUxD.CUX2801001:1", "HmIP-RF.00010000000A10", "BidCos-RF.ABC0000001:1"} {
		doc.Objects[ref] = &meta.Object{Name: ref, Enums: []string{}}
	}
	if got := ForeignObjects(doc, nil); len(got) != 0 {
		t.Errorf("no list read: %v", got)
	}
	got := ForeignObjects(doc, []string{"BidCos-RF", "HmIP-RF"})
	if len(got) != 2 || len(got["CUxD"]) != 2 || got["CUxD"][0] != "CUxD.CUX2801001" || len(got["System"]) != 1 {
		t.Errorf("by the list: %v", got)
	}
	if got := ForeignObjects(doc, []string{"BidCos-RF", "HmIP-RF", "CUxD", "System"}); len(got) != 0 {
		t.Errorf("all listed: %v", got)
	}
}
