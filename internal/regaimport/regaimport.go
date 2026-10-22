// Package regaimport reads names, rooms and functions out of a running CCU / OpenCCU over the
// ReGa remote script interface and turns them into a metadata document (D-17: the bridge from a
// system with ReGa to one without). It is a ReGa *client*, which D-1 does not forbid; nothing
// here serves ReGa.
//
// The script is line-oriented on purpose: ReGa's output is ISO-8859-1 and its string handling
// has no escaping worth relying on, so one record per line with tab separators is the format
// that survives every name a user can type into the WebUI.
package regaimport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hobbyquaker/occulited/internal/meta"
)

// Client talks to one CCU.
type Client struct {
	Host string
	Port int  // 8181 (lighttpd's proxy) or 8183 (ReGa itself, on the box); 0 = 8181
	TLS  bool // 48181
	HTTP *http.Client
}

// Script is what the importer runs. Records: I<tab>id<tab>name for interfaces,
// O<tab>id<tab>interface-id<tab>address<tab>name for devices and channels, R/F<tab>id<tab>name
// for rooms and functions, M<tab>enum-id<tab>channel-id for memberships. Interfaces are emitted
// as ids and resolved here: dom.GetObject(oDevice.Interface()).Name() is a runtime error on
// current ReGa builds. Written here, not taken from any existing project.
const Script = `string sIfId;
string sDevId;
string sChnId;
string sEnumId;
string sMemberId;
foreach (sIfId, root.Interfaces().EnumUsedIDs()) {
  object oIf = dom.GetObject(sIfId);
  Write("I\t" # sIfId # "\t" # oIf.Name() # "\n");
}
foreach (sDevId, root.Devices().EnumUsedIDs()) {
  object oDevice = dom.GetObject(sDevId);
  if (oDevice.ReadyConfig()) {
    Write("O\t" # sDevId # "\t" # oDevice.Interface() # "\t" # oDevice.Address() # "\t" # oDevice.Name() # "\n");
    foreach (sChnId, oDevice.Channels()) {
      object oChannel = dom.GetObject(sChnId);
      Write("O\t" # sChnId # "\t" # oDevice.Interface() # "\t" # oChannel.Address() # "\t" # oChannel.Name() # "\n");
    }
  }
}
foreach (sEnumId, dom.GetObject(ID_ROOMS).EnumUsedIDs()) {
  object oEnum = dom.GetObject(sEnumId);
  Write("R\t" # sEnumId # "\t" # oEnum.Name() # "\n");
  foreach (sMemberId, oEnum.EnumUsedIDs()) { Write("M\t" # sEnumId # "\t" # sMemberId # "\n"); }
}
foreach (sEnumId, dom.GetObject(ID_FUNCTIONS).EnumUsedIDs()) {
  object oEnum = dom.GetObject(sEnumId);
  Write("F\t" # sEnumId # "\t" # oEnum.Name() # "\n");
  foreach (sMemberId, oEnum.EnumUsedIDs()) { Write("M\t" # sEnumId # "\t" # sMemberId # "\n"); }
}
`

// Exec runs a script and returns its output (converted from ISO-8859-1) without ReGa's XML
// trailer.
func (c *Client) Exec(ctx context.Context, script string) (string, error) {
	port := c.Port
	if port == 0 {
		port = 8181
		if c.TLS {
			port = 48181
		}
	}
	scheme := "http"
	if c.TLS {
		scheme = "https"
	}
	u := scheme + "://" + net.JoinHostPort(c.Host, strconv.Itoa(port)) + "/tclrega.exe"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(script))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/plain")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("tclrega.exe: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return "", err
	}
	out := latin1(b)
	if i := strings.LastIndex(out, "<xml>"); i >= 0 {
		out = out[:i]
	}
	return out, nil
}

// latin1 maps every byte to the rune of the same value: ISO-8859-1 to UTF-8. The answer is mixed
// in the same way the regadom is, so every name Parse takes out of it goes through fixMojibake.
func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// Dump is the parsed script output.
type Dump struct {
	Objects   []DumpObject
	Rooms     []DumpEnum
	Functions []DumpEnum
	// Favorites are the CCU's favorite pages with members (task 193): a user's own page under the
	// user's name, a shared page under its own. A regadom has them; the live script does not.
	Favorites []DumpEnum
}

// DumpObject is one device or channel as ReGa knows it.
type DumpObject struct {
	ID        int
	Interface string
	Address   string
	Name      string
}

// DumpEnum is one room or function with its member channel ids.
type DumpEnum struct {
	ID      int
	Name    string
	Members []int
}

// Parse turns the script output into a Dump.
func Parse(out string) (*Dump, error) {
	d := &Dump{}
	enums := map[int]*DumpEnum{}
	ifaces := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 2 {
			continue
		}
		id, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		switch f[0] {
		case "I":
			if len(f) >= 3 {
				ifaces[f[1]] = strings.TrimSpace(f[2])
			}
		case "O":
			if len(f) >= 5 {
				iface := ifaces[f[2]]
				if iface == "" {
					iface = f[2] // an older script variant already sent the name
				}
				d.Objects = append(d.Objects, DumpObject{ID: id, Interface: iface, Address: f[3], Name: cleanName(strings.TrimSpace(f[4]))})
			}
		case "R", "F":
			if len(f) >= 3 {
				e := &DumpEnum{ID: id, Name: cleanName(strings.TrimSpace(f[2]))}
				enums[id] = e
				if f[0] == "R" {
					d.Rooms = append(d.Rooms, *e)
				} else {
					d.Functions = append(d.Functions, *e)
				}
			}
		case "M":
			if len(f) >= 3 {
				if m, err := strconv.Atoi(f[2]); err == nil {
					if e, ok := enums[id]; ok {
						e.Members = append(e.Members, m)
					}
				}
			}
		}
	}
	// the M records arrived after the R/F copies were taken: resolve members now
	for i := range d.Rooms {
		d.Rooms[i].Members = enums[d.Rooms[i].ID].Members
	}
	for i := range d.Functions {
		d.Functions[i].Members = enums[d.Functions[i].ID].Members
	}
	if len(d.Objects) == 0 {
		// Two very different situations answer with no devices, and telling a user the box is
		// unreachable when it is not sends them to the firewall for nothing: a CCU that answered
		// and simply has nothing paired still sends its rooms and functions, which a CCU3's
		// factory list always has (measured on a lab OpenCCU with ReGaHSS up and no devices:
		// 0 objects, 11 rooms, 10 functions).
		if len(d.Rooms) > 0 || len(d.Functions) > 0 {
			return d, fmt.Errorf("the CCU answered, and has no devices paired (%d rooms, %d functions): there are no names to import yet", len(d.Rooms), len(d.Functions))
		}
		return d, errors.New("the CCU returned no devices - is this a CCU, and is the remote script port reachable?")
	}
	return d, nil
}

// Result is what Convert produces beside the document: the counts and the decisions a user
// wants to see before importing.
type Result struct {
	Devices   int               `json:"devices"`
	Channels  int               `json:"channels"`
	Rooms     int               `json:"rooms"`
	Functions int               `json:"functions"`
	Skipped   []string          `json:"skipped"` // objects without a usable ref
	Renamed   map[string]string `json:"renamed"` // enum name -> node id when the slug collided
	Unnamed   int               `json:"unnamed"` // objects whose ReGa name is just the address (not imported)
	// Foreign: objects of an interface that is no interface process here - ReGa's System
	// pseudo-interface (the CCU's own objects), or one InterfacesList.xml does not list -, left
	// out (occulited B-84: Control cannot operate them and asked lite-rpc for them)
	Foreign int `json:"foreign"`
	// Favorites is the number of favorite pages imported (task 193), FavoritesFor their names:
	// an account of that name here takes the page over (favorites.Sync adopts it), any other
	// waits under the CCU's name until such an account exists.
	Favorites    int            `json:"favorites"`
	FavoritesFor []string       `json:"favorites_for"`
	Document     *meta.Document `json:"-"`
}

var nonID = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a node id out of a name: lower case, umlauts transcribed, everything else a hyphen.
func Slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch r {
		case 'ä':
			b.WriteString("ae")
		case 'ö':
			b.WriteString("oe")
		case 'ü':
			b.WriteString("ue")
		case 'ß':
			b.WriteString("ss")
		default:
			if r > unicode.MaxASCII {
				b.WriteRune('-')
			} else {
				b.WriteRune(r)
			}
		}
	}
	s := strings.Trim(nonID.ReplaceAllString(b.String(), "-"), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	if s == "" {
		s = "x"
	}
	return s
}

// Convert builds the metadata document: every device and channel with a name that is not just
// its address becomes an object; rooms and functions become flat nodes under the default enums.
// base supplies the enums to extend (the store's current ones for a merge, the defaults for a
// replace); it is not modified.
func Convert(d *Dump, base map[string]*meta.Enum) *Result {
	return ConvertKnown(d, base, nil)
}

// PseudoInterfaces are ReGa's interfaces that are no interface process: their objects are never
// imported (occulited B-84, the maintainer 2026-10-16: "skip them entirely").
var PseudoInterfaces = map[string]bool{"System": true}

// ForeignInterface says whether objects of the interface are left out: a pseudo-interface, or
// one known does not take (known nil: only the pseudo-interfaces).
func ForeignInterface(iface string, known func(string) bool) bool {
	return PseudoInterfaces[iface] || known != nil && !known(iface)
}

// ConvertKnown is Convert with the interfaces this system has: an object of any other one
// (known false) is left out and counted in Foreign, as ReGa's System pseudo-interface always is.
func ConvertKnown(d *Dump, base map[string]*meta.Enum, known func(string) bool) *Result {
	res := &Result{Skipped: []string{}, Renamed: map[string]string{}, FavoritesFor: []string{}}
	doc := meta.NewDocument()
	for id, e := range base {
		doc.Enums[id] = e.Clone()
	}
	for _, id := range []string{"room", "function"} {
		if doc.Enums[id] == nil {
			doc.Enums[id] = meta.Defaults()[id]
		}
	}
	byID := map[int]string{}
	for _, o := range d.Objects {
		if o.Interface == "" || o.Address == "" || strings.ContainsAny(o.Address, " /") {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%d %s", o.ID, o.Address))
			continue
		}
		if ForeignInterface(o.Interface, known) {
			res.Foreign++
			continue
		}
		ref := o.Interface + "." + o.Address
		byID[o.ID] = ref
		if strings.Contains(o.Address, ":") {
			res.Channels++
		} else {
			res.Devices++
		}
		if isDefaultName(o.Name, o.Address, o.Interface) {
			res.Unnamed++
			continue
		}
		doc.Objects[ref] = &meta.Object{Name: o.Name, Enums: []string{}}
	}
	add := func(enumID string, list []DumpEnum) int {
		e := doc.Enums[enumID]
		counted := map[string]bool{}
		used := map[string]bool{}
		for _, n := range e.Tree {
			used[n.ID] = true
		}
		n := 0
		for _, de := range list {
			if de.Name == "" {
				continue
			}
			// the CCU's own rooms and functions carry a translation key, not a name
			name, _ := BuiltinName(de.Name)
			id := ""
			for _, existing := range e.Tree {
				if existing.Name == name {
					id = existing.ID
				}
			}
			if id == "" {
				id = Slug(name)
				if used[id] {
					for i := 2; ; i++ {
						if !used[id+"-"+strconv.Itoa(i)] {
							id = id + "-" + strconv.Itoa(i)
							break
						}
					}
					res.Renamed[name] = id
				}
				used[id] = true
				e.Tree = append(e.Tree, &meta.Node{ID: id, Name: name})
			}
			path := enumID + "/" + id
			if !counted[id] {
				counted[id] = true
				n++ // what arrived is nodes in the tree, not source objects: two ReGa rooms of
				// the same name (the mixed-encoding duplicates fixMojibake collapses) are one
			}
			for _, m := range de.Members {
				ref, ok := byID[m]
				if !ok {
					continue
				}
				o := doc.Objects[ref]
				if o == nil {
					// a member with the default name still belongs to the room: keep it, named by address
					o = &meta.Object{Name: strings.TrimPrefix(ref, byID[m][:strings.Index(byID[m], ".")+1]), Enums: []string{}}
					doc.Objects[ref] = o
				}
				if !contains(o.Enums, path) {
					o.Enums = append(o.Enums, path)
				}
			}
		}
		return n
	}
	res.Rooms = add("room", d.Rooms)
	res.Functions = add("function", d.Functions)
	if len(d.Favorites) > 0 {
		// task 193: the favorites enum, one node per page; an account's node here has the account's
		// id, and a page of that account's name lands beside it under its slug until the sync moves
		// the members over and drops the waiting node
		if doc.Enums[FavoritesEnum] == nil {
			doc.Enums[FavoritesEnum] = &meta.Enum{Name: map[string]string{"de": "Favoriten", "en": "Favorites"}}
		}
		res.Favorites = add(FavoritesEnum, d.Favorites)
		for _, f := range d.Favorites {
			if f.Name != "" && !contains(res.FavoritesFor, f.Name) {
				res.FavoritesFor = append(res.FavoritesFor, f.Name)
			}
		}
	}
	for _, o := range doc.Objects {
		sort.Strings(o.Enums)
	}
	res.Document = doc
	return res
}

// FavoritesEnum is the favorites enum's id (favorites.EnumID; a literal here, so the packages
// stay apart).
const FavoritesEnum = "favorite"

// isDefaultName recognises what the CCU calls a device nobody has named: the address alone or
// "<type> <address>" (HM-CC-TC JEQ9000001:1). Such names are not worth importing.
//
// The heating groups are the exception (occulited B-102): ReGa names a group device
// "<group name> INT000000N" and its channels "<group name> INT000000N:n" - the address is part
// of the name the user gave, and the suffix rule swallowed every group of a production system
// (52 objects, 7 groups, none imported). On VirtualDevices only the address alone and the real
// default "<type> <address>" with a device type in front (HmIP-HEATING INT0000003:6: a channel a
// firmware update added after the group was named) count as unnamed.
func isDefaultName(name, address, iface string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == address {
		return true
	}
	prefix, ok := strings.CutSuffix(name, " "+address)
	if !ok {
		return false
	}
	if iface != GroupsInterface {
		return true
	}
	return looksLikeDeviceType(prefix)
}

// GroupsInterface is the interface of the heating groups' devices.
const GroupsInterface = "VirtualDevices"

// looksLikeDeviceType says whether s is a device type (HmIP-HEATING, HM-CC-VG-1), not a name a
// person gave: one token with a HomeMatic family prefix.
func looksLikeDeviceType(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false
	}
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "hm-") || strings.HasPrefix(l, "hmip-") || strings.HasPrefix(l, "hmw-")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Fetch runs the script on the CCU and parses it.
func (c *Client) Fetch(ctx context.Context) (*Dump, error) {
	out, err := c.Exec(ctx, Script)
	if err != nil {
		return nil, err
	}
	return Parse(out)
}
