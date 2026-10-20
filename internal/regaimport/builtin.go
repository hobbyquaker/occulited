package regaimport

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hobbyquaker/occulited/internal/meta"
)

// builtinNames are the rooms and functions a CCU creates on its own, by translation key. ReGa
// stores the key as the object's Name() - `roomBathroom`, or `${roomBathroom}` in the form the
// WebUI's string substitution uses - and the WebUI replaces it from its string tables on every
// page. openccu-lite does not ship those tables, so an import that takes the raw value shows the
// key (B-79: 21 of them on a CCU3 upgraded to openccu-lite).
//
// Source: OpenCCU's occu, release 3.89.8-2 (github.com/OpenCCU/occu),
// WebUI/www/webui/js/lang/de/translate.lang.extension.js and its en twin, lines 27-47; the key
// lists are ROOMLIST and FUNCTIONLIST in WebUI/bin/hm_startup, which creates them. The German file
// writes the umlauts as %FC. The same table, from the same files, is homematic-manager's
// REGA_STOCK_NAMES. WebUI/www/api/methods/ccu/setsystemlanguage.tcl carries a second English
// spelling ("Central", "Children`s room 1"); the translate.lang files are what the WebUI showed.
var builtinNames = map[string]struct{ de, en string }{
	"roomLivingRoom":     {"Wohnzimmer", "Living room"},
	"roomKitchen":        {"Küche", "Kitchen"},
	"roomBedroom":        {"Schlafzimmer", "Bed room"},
	"roomChildrensRoom1": {"Kinderzimmer 1", "Children's room 1"},
	"roomChildrensRoom2": {"Kinderzimmer 2", "Children's room 2"},
	"roomOffice":         {"Büro", "Home office"},
	"roomBathroom":       {"Badezimmer", "Bathroom"},
	"roomGarage":         {"Garage", "Garage"},
	"roomHWR":            {"Hauswirtschaftsraum", "Utility room"},
	"roomGarden":         {"Garten", "Garden"},
	"roomTerrace":        {"Terrasse", "Terrace"},
	"funcLight":          {"Licht", "Light"},
	"funcHeating":        {"Heizung", "Heating"},
	"funcClimateControl": {"Klima", "Climatic conditions"},
	"funcWeather":        {"Wetter", "Weather"},
	"funcEnvironment":    {"Umwelt", "Environment"},
	"funcSecurity":       {"Sicherheit", "Security"},
	"funcLock":           {"Verschluss", "Lock"},
	"funcButton":         {"Taster", "Button"},
	"funcCentral":        {"Zentrale", "Central control unit"},
	"funcEnergy":         {"Energiemanagement", "Energy management"},
}

// BuiltinName answers the name a CCU's WebUI shows for one of its built-in rooms or functions:
// name is exactly a key (`roomBathroom`) or exactly a key in substitution form (`${roomBathroom}`).
// Anything else - a name a user typed, a key with a space around it, a key the table does not
// know - comes back unchanged with ok false.
//
// The name is German. The store holds one name per node and occulited knows no box language: the
// admin UI follows the browser, and the CCU's own /etc/config/systemLanguage is written by the
// WebUI's language dialog, which renames these very objects to the chosen language at the same
// moment - so a database that still holds a bare key is one whose language was never chosen, and
// the file is absent (as it was on the lab CCU3 this was found on). German is also what the admin
// surface puts first. The English names stay in the table for the day a box language exists.
func BuiltinName(name string) (string, bool) {
	key := name
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		key = key[2 : len(key)-1]
	}
	if n, ok := builtinNames[key]; ok {
		return n.de, true
	}
	return name, false
}

// TranslateBuiltinNodes renames the nodes of the room and function enums whose name is exactly a
// built-in key to the name BuiltinName gives, through the store's own node update: a revision and
// a node.updated event each, like a rename on the Metadata page. It is for a store filled by an
// import from before the table existed; ids stay what they are, since memberships, subscribers and
// saved filters point at them. Nothing else is touched - not objects, not other enums, not a name
// that merely contains a key.
//
// It keeps no marker, because it needs none: a renamed node no longer matches, so a second run
// finds nothing, and a key that reaches the store later (a meta.json from an old export put back
// through the import route) is healed at the next start instead of being skipped for ever. The
// price is that a user who names a room `roomBathroom` on purpose gets Badezimmer at the next
// start, which is a translation key nobody types.
//
// Returns "path: old -> new" per rename, in tree order.
func TranslateBuiltinNodes(store *meta.Store) ([]string, error) {
	type rename struct{ path, from, to string }
	var todo []rename
	var walk func(prefix string, nodes []*meta.Node)
	walk = func(prefix string, nodes []*meta.Node) {
		for _, n := range nodes {
			path := prefix + "/" + n.ID
			if to, ok := BuiltinName(n.Name); ok {
				todo = append(todo, rename{path, n.Name, to})
			}
			walk(path, n.Children)
		}
	}
	snap := store.Snapshot()
	for _, id := range []string{"room", "function"} {
		if e := snap.Enums[id]; e != nil {
			walk(id, e.Tree)
		}
	}
	var done []string
	for _, r := range todo {
		to := r.to
		if _, _, err := store.UpdateNode(nil, r.path, meta.NodePatch{Name: &to}); err != nil {
			return done, fmt.Errorf("%s: %w", r.path, err)
		}
		done = append(done, r.path+": "+r.from+" -> "+r.to)
	}
	return done, nil
}

// DropPseudoObjects removes the objects of ReGa's pseudo-interfaces (System) that an import before
// occulited B-84 brought in: Control showed them under the unassigned channels and asked lite-rpc
// for an interface that cannot exist here ("System: no such interface: System"). It is the only
// removal at start (the maintainer, 2026-10-20: "never automatic" - an interface InterfacesList.xml
// does not list for one boot, a radio module that did not answer, cost every name of it). System is
// no interface process on any firmware, no InterfacesList.xml ever lists it, and no client can
// address its objects, so their names name nothing. One revision for all of them; it keeps no
// marker and finds nothing once it has run. Returns the refs removed, sorted.
func DropPseudoObjects(store *meta.Store) ([]string, error) {
	var del []string
	for ref := range store.Snapshot().Objects {
		if i := strings.IndexByte(ref, '.'); i > 0 && PseudoInterfaces[ref[:i]] {
			del = append(del, ref)
		}
	}
	if len(del) == 0 {
		return nil, nil
	}
	sort.Strings(del)
	if _, _, err := store.Bulk(nil, nil, del); err != nil {
		return nil, err
	}
	return del, nil
}

// ForeignObjects lists, per interface, the refs of doc whose interface listed (InterfacesList.xml's
// names) does not name - ReGa's pseudo-interfaces included. With no list read (listed empty) it
// lists nothing: a missing file is no reason to offer every name for removal (occulited B-84).
// Refs without an interface are never listed. Each list is sorted.
func ForeignObjects(doc *meta.Document, listed []string) map[string][]string {
	if len(listed) == 0 {
		return map[string][]string{}
	}
	set := make(map[string]bool, len(listed))
	for _, n := range listed {
		set[n] = true
	}
	out := map[string][]string{}
	for ref := range doc.Objects {
		if i := strings.IndexByte(ref, '.'); i > 0 && !set[ref[:i]] {
			out[ref[:i]] = append(out[ref[:i]], ref)
		}
	}
	for _, refs := range out {
		sort.Strings(refs)
	}
	return out
}
