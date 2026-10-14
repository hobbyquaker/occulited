package meta

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// EventRetention is how many events the change stream keeps for replay.
const EventRetention = 1000

// Event is one entry of the change stream (docs/meta-api.md).
type Event struct {
	Revision uint64 `json:"revision"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref,omitempty"`
	Enum     string `json:"enum,omitempty"`
	Path     string `json:"path,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Value    any    `json:"value,omitempty"`
	Objects  int    `json:"objects,omitempty"`
	Enums    int    `json:"enums,omitempty"`
}

// Store holds the document, serialises mutations, keeps the event log and persists changes.
type Store struct {
	mu     sync.RWMutex
	doc    *Document
	events []Event
	first  uint64 // revision of events[0]; replay before it is impossible
	saver  func(*Document) error
	subs   map[chan Event]struct{}
}

// New returns a store around doc (nil = fresh). saver is called after every successful mutation
// with the new document; a persisting store passes Save, tests pass nil.
func New(doc *Document, saver func(*Document) error) (*Store, error) {
	if doc == nil {
		doc = NewDocument()
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return &Store{doc: doc, saver: saver, first: doc.Revision + 1, subs: map[chan Event]struct{}{}}, nil
}

// Revision is the current revision.
func (s *Store) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.Revision
}

// Snapshot returns a deep copy of the whole document.
func (s *Store) Snapshot() *Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.Clone()
}

// Subscribe returns a channel that receives every future event; Unsubscribe when done.
func (s *Store) Subscribe() chan Event {
	ch := make(chan Event, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

// Unsubscribe stops delivery and closes the channel.
func (s *Store) Unsubscribe(ch chan Event) {
	s.mu.Lock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
	s.mu.Unlock()
}

// EventsSince returns the events with a revision greater than since, or ok=false when the store
// cannot replay from there (too old, or since is ahead of the store) and the caller must re-snapshot.
func (s *Store) EventsSince(since int64) (events []Event, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if since < 0 || uint64(since) > s.doc.Revision {
		return nil, false
	}
	if uint64(since)+1 < s.first {
		return nil, false
	}
	for _, e := range s.events {
		if e.Revision > uint64(since) {
			events = append(events, e)
		}
	}
	if events == nil {
		events = []Event{}
	}
	return events, true
}

// mutate runs fn on a clone; if fn reports a change, the clone becomes the document at revision+1,
// the events are stamped and published, and the saver is called. ifMatch, when non-nil, must equal
// the current revision.
func (s *Store) mutate(ifMatch *uint64, fn func(d *Document) (changed bool, events []Event, err error)) (uint64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ifMatch != nil && *ifMatch != s.doc.Revision {
		return s.doc.Revision, false, errf(ErrRevisionConflict, "store is at revision %d, not %d", s.doc.Revision, *ifMatch)
	}
	c := s.doc.Clone()
	changed, events, err := fn(c)
	if err != nil {
		return s.doc.Revision, false, err
	}
	if !changed {
		return s.doc.Revision, false, nil
	}
	if err := c.Validate(); err != nil {
		return s.doc.Revision, false, err
	}
	c.Revision = s.doc.Revision + 1
	if s.saver != nil {
		if err := s.saver(c); err != nil {
			return s.doc.Revision, false, err
		}
	}
	s.doc = c
	for i := range events {
		events[i].Revision = c.Revision
	}
	s.events = append(s.events, events...)
	if len(s.events) > EventRetention {
		drop := len(s.events) - EventRetention
		s.events = s.events[drop:]
		s.first = s.events[0].Revision
	}
	for ch := range s.subs {
		for _, e := range events {
			select {
			case ch <- e:
			default: // a slow subscriber loses events and will notice the revision gap
			}
		}
	}
	return c.Revision, true, nil
}

// --- objects ---

// GetObject returns a copy of one object.
func (s *Store) GetObject(ref string) (*Object, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.doc.Objects[ref]
	if !ok {
		return nil, errf(ErrUnknownObject, "object %q does not exist", ref)
	}
	return o.clone(), nil
}

// NodeName returns the name of the node a path such as "room/garage" names (occulited B-66: the
// service messages show a device's rooms by their names, not by their ids).
func (s *Store) NodeName(path string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, n, _, _, err := s.doc.findNode(path)
	if err != nil {
		return "", err
	}
	if n == nil {
		return "", errf(ErrUnknownPath, "path %q names an enum, not a node", path)
	}
	return n.Name, nil
}

// Query returns the refs of objects that belong to the subtree of path (an enum id or a node
// path), sorted, optionally filtered on the orphaned flag.
func (s *Store) Query(path string, orphaned *bool) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if path != "" {
		if _, _, _, _, err := s.doc.findNode(path); err != nil {
			if e, ok := err.(*Error); ok && e.Code == ErrUnknownEnum {
				return nil, errf(ErrUnknownPath, "path %q does not resolve", path)
			}
			return nil, err
		}
	}
	refs := []string{}
	for ref, o := range s.doc.Objects {
		if orphaned != nil && o.Orphaned != *orphaned {
			continue
		}
		if path == "" || inSubtree(o.Enums, path) {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs, nil
}

func inSubtree(paths []string, root string) bool {
	for _, p := range paths {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}

// applyPatch applies p to o (creating it when o is nil); returns the object and whether it changed.
func applyPatch(o *Object, p ObjectPatch, ref string) (*Object, bool, error) {
	created := o == nil
	if created {
		if p.Name == nil {
			return nil, false, errf(ErrInvalidName, "creating %q needs a name", ref)
		}
		o = &Object{Enums: []string{}, Meta: map[string]json.RawMessage{}}
	}
	before := o.clone()
	if p.Name != nil {
		n, err := NormalizeName(*p.Name)
		if err != nil {
			return nil, false, err
		}
		o.Name = n
	}
	if p.Enums != nil {
		o.Enums = append([]string{}, (*p.Enums)...)
	}
	for ns, v := range p.Meta {
		if err := ValidateID(ns); err != nil {
			return nil, false, errf(ErrInvalidID, "meta namespace %q is invalid", ns)
		}
		if v == nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			delete(o.Meta, ns)
		} else {
			o.Meta[ns] = append(json.RawMessage{}, v...)
		}
	}
	return o, created || !objectEqual(before, o), nil
}

func objectEqual(a, b *Object) bool {
	if a.Name != b.Name || a.Orphaned != b.Orphaned || len(a.Enums) != len(b.Enums) || len(a.Meta) != len(b.Meta) {
		return false
	}
	for i := range a.Enums {
		if a.Enums[i] != b.Enums[i] {
			return false
		}
	}
	for k, v := range a.Meta {
		w, ok := b.Meta[k]
		if !ok || !jsonEqual(v, w) {
			return false
		}
	}
	return true
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

// SetObject creates or updates an object with PATCH semantics.
func (s *Store) SetObject(ifMatch *uint64, ref string, p ObjectPatch) (uint64, bool, error) {
	if err := ValidateRef(ref); err != nil {
		return s.Revision(), false, err
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		o, changed, err := applyPatch(d.Objects[ref], p, ref)
		if err != nil || !changed {
			return false, nil, err
		}
		d.Objects[ref] = o
		return true, []Event{{Kind: "object.updated", Ref: ref, Value: o.clone()}}, nil
	})
}

// PutObject creates or replaces an object; absent optional fields are reset to defaults.
func (s *Store) PutObject(ifMatch *uint64, ref string, name string, enums []string, meta map[string]json.RawMessage) (uint64, bool, error) {
	if err := ValidateRef(ref); err != nil {
		return s.Revision(), false, err
	}
	nm, err := NormalizeName(name)
	if err != nil {
		return s.Revision(), false, err
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		o := &Object{Name: nm, Enums: []string{}, Meta: map[string]json.RawMessage{}}
		if old := d.Objects[ref]; old != nil {
			o.Orphaned = old.Orphaned
		}
		o.Enums = append(o.Enums, enums...)
		for ns, v := range meta {
			if err := ValidateID(ns); err != nil {
				return false, nil, errf(ErrInvalidID, "meta namespace %q is invalid", ns)
			}
			o.Meta[ns] = append(json.RawMessage{}, v...)
		}
		if old := d.Objects[ref]; old != nil && objectEqual(old, o) {
			return false, nil, nil
		}
		d.Objects[ref] = o
		return true, []Event{{Kind: "object.updated", Ref: ref, Value: o.clone()}}, nil
	})
}

// DeleteObject removes an object.
func (s *Store) DeleteObject(ifMatch *uint64, ref string) (uint64, bool, error) {
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		if _, ok := d.Objects[ref]; !ok {
			return false, nil, errf(ErrUnknownObject, "object %q does not exist", ref)
		}
		delete(d.Objects, ref)
		return true, []Event{{Kind: "object.deleted", Ref: ref}}, nil
	})
}

// SetOrphaned is the owner-only flag update.
func (s *Store) SetOrphaned(ref string, orphaned bool) (uint64, bool, error) {
	return s.mutate(nil, func(d *Document) (bool, []Event, error) {
		o, ok := d.Objects[ref]
		if !ok {
			return false, nil, errf(ErrUnknownObject, "object %q does not exist", ref)
		}
		if o.Orphaned == orphaned {
			return false, nil, nil
		}
		o.Orphaned = orphaned
		return true, []Event{{Kind: "object.updated", Ref: ref, Value: o.clone()}}, nil
	})
}

// Bulk applies many patches and deletes as one revision, all or nothing.
func (s *Store) Bulk(ifMatch *uint64, set map[string]ObjectPatch, del []string) (uint64, bool, error) {
	for ref := range set {
		if err := ValidateRef(ref); err != nil {
			return s.Revision(), false, err
		}
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		var events []Event
		refs := make([]string, 0, len(set))
		for ref := range set {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			o, changed, err := applyPatch(d.Objects[ref], set[ref], ref)
			if err != nil {
				return false, nil, err
			}
			if changed {
				d.Objects[ref] = o
				events = append(events, Event{Kind: "object.updated", Ref: ref, Value: o.clone()})
			}
		}
		for _, ref := range del {
			if _, ok := d.Objects[ref]; ok {
				delete(d.Objects, ref)
				events = append(events, Event{Kind: "object.deleted", Ref: ref})
			}
		}
		return len(events) > 0, events, nil
	})
}

// --- enums and nodes ---

// CreateEnum adds a taxonomy.
func (s *Store) CreateEnum(ifMatch *uint64, id string, name map[string]string) (uint64, bool, error) {
	if err := ValidateID(id); err != nil {
		return s.Revision(), false, err
	}
	if err := validateEnumName(name); err != nil {
		return s.Revision(), false, err
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		if _, ok := d.Enums[id]; ok {
			return false, nil, errf(ErrDuplicateID, "enum %q already exists", id)
		}
		e := &Enum{Name: map[string]string{}, Tree: []*Node{}}
		for k, v := range name {
			e.Name[k] = strings.TrimSpace(v)
		}
		d.Enums[id] = e
		return true, []Event{{Kind: "enum.created", Enum: id, Value: e.clone()}}, nil
	})
}

// UpdateEnum renames a taxonomy.
func (s *Store) UpdateEnum(ifMatch *uint64, id string, name map[string]string) (uint64, bool, error) {
	if err := validateEnumName(name); err != nil {
		return s.Revision(), false, err
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		e, ok := d.Enums[id]
		if !ok {
			return false, nil, errf(ErrUnknownEnum, "enum %q does not exist", id)
		}
		changed := len(e.Name) != len(name)
		for k, v := range name {
			if e.Name[k] != strings.TrimSpace(v) {
				changed = true
			}
		}
		if !changed {
			return false, nil, nil
		}
		e.Name = map[string]string{}
		for k, v := range name {
			e.Name[k] = strings.TrimSpace(v)
		}
		return true, []Event{{Kind: "enum.updated", Enum: id, Value: e.clone()}}, nil
	})
}

// members returns the refs of objects with a path in the subtree of root.
func (d *Document) members(root string) []string {
	var refs []string
	for ref, o := range d.Objects {
		if inSubtree(o.Enums, root) {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}

// detach removes every path in the subtree of root from every object.
func (d *Document) detach(root string) {
	for _, o := range d.Objects {
		kept := o.Enums[:0]
		for _, p := range o.Enums {
			if !(p == root || strings.HasPrefix(p, root+"/")) {
				kept = append(kept, p)
			}
		}
		o.Enums = kept
	}
}

// DeleteEnum removes a taxonomy and every node; refused with has-members unless detach.
func (s *Store) DeleteEnum(ifMatch *uint64, id string, detach bool) (uint64, bool, error) {
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		if _, ok := d.Enums[id]; !ok {
			return false, nil, errf(ErrUnknownEnum, "enum %q does not exist", id)
		}
		if m := d.members(id); len(m) > 0 {
			if !detach {
				return false, nil, &Error{Code: ErrHasMembers, Message: "enum has members", Detail: map[string]any{"refs": m}}
			}
			d.detach(id)
		}
		delete(d.Enums, id)
		return true, []Event{{Kind: "enum.deleted", Enum: id}}, nil
	})
}

// CreateNode adds a node under parent (the enum id alone = root) at position (nil = append).
func (s *Store) CreateNode(ifMatch *uint64, enumID string, parent *string, id, name, icon string, position *int) (uint64, bool, error) {
	if err := ValidateID(id); err != nil {
		return s.Revision(), false, err
	}
	nm, err := NormalizeName(name)
	if err != nil {
		return s.Revision(), false, err
	}
	if err := validateIcon(icon); err != nil {
		return s.Revision(), false, err
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		e, ok := d.Enums[enumID]
		if !ok {
			return false, nil, errf(ErrUnknownEnum, "enum %q does not exist", enumID)
		}
		list := &e.Tree
		parentPath := enumID
		if parent != nil && *parent != "" && *parent != enumID {
			if !strings.HasPrefix(*parent, enumID+"/") {
				return false, nil, errf(ErrUnknownPath, "parent %q is not in enum %q", *parent, enumID)
			}
			_, pn, _, _, err := d.findNode(*parent)
			if err != nil {
				return false, nil, err
			}
			list = &pn.Children
			parentPath = *parent
		}
		for _, c := range *list {
			if c.ID == id {
				return false, nil, errf(ErrDuplicateID, "node %q already exists under %q", id, parentPath)
			}
		}
		n := &Node{ID: id, Name: nm, Icon: icon}
		*list = insertNode(*list, n, position)
		return true, []Event{{Kind: "node.created", Enum: enumID, Path: parentPath + "/" + id, Value: n}}, nil
	})
}

func insertNode(list []*Node, n *Node, position *int) []*Node {
	if position == nil || *position >= len(list) {
		return append(list, n)
	}
	pos := *position
	if pos < 0 {
		pos = 0
	}
	list = append(list, nil)
	copy(list[pos+1:], list[pos:])
	list[pos] = n
	return list
}

// UpdateNode renames, re-icons, moves or reorders a node.
func (s *Store) UpdateNode(ifMatch *uint64, path string, p NodePatch) (uint64, bool, error) {
	enumID, ids := splitPath(path)
	if len(ids) == 0 {
		return s.Revision(), false, errf(ErrUnknownPath, "%q names an enum, not a node", path)
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		e, n, siblings, idx, err := d.findNode(path)
		if err != nil {
			return false, nil, err
		}
		var events []Event
		changed := false
		if p.Name != nil {
			nm, err := NormalizeName(*p.Name)
			if err != nil {
				return false, nil, err
			}
			if nm != n.Name {
				n.Name = nm
				changed = true
			}
		}
		if p.Icon != nil {
			if err := validateIcon(*p.Icon); err != nil {
				return false, nil, err
			}
			if *p.Icon != n.Icon {
				n.Icon = *p.Icon
				changed = true
			}
		}
		if changed {
			events = append(events, Event{Kind: "node.updated", Enum: enumID, Path: path, Value: n})
		}
		newPath := path
		if p.Parent != nil {
			target := *p.Parent
			if target == "" {
				target = enumID
			}
			if target == path || strings.HasPrefix(target, path+"/") {
				return false, nil, errf(ErrInvalidMove, "cannot move %q under itself", path)
			}
			if !(target == enumID || strings.HasPrefix(target, enumID+"/")) {
				return false, nil, errf(ErrInvalidMove, "cannot move a node to another enum")
			}
			currentParent := strings.Join(append([]string{enumID}, ids[:len(ids)-1]...), "/")
			if target != currentParent {
				list := &e.Tree
				if target != enumID {
					_, pn, _, _, err := d.findNode(target)
					if err != nil {
						return false, nil, err
					}
					list = &pn.Children
				}
				for _, c := range *list {
					if c.ID == n.ID {
						return false, nil, errf(ErrDuplicateID, "node %q already exists under %q", n.ID, target)
					}
				}
				*siblings = append((*siblings)[:idx], (*siblings)[idx+1:]...)
				*list = insertNode(*list, n, p.Position)
				newPath = target + "/" + n.ID
				for _, o := range d.Objects {
					for i, mp := range o.Enums {
						if mp == path {
							o.Enums[i] = newPath
						} else if strings.HasPrefix(mp, path+"/") {
							o.Enums[i] = newPath + mp[len(path):]
						}
					}
				}
				events = append(events, Event{Kind: "node.moved", Enum: enumID, From: path, To: newPath})
				changed = true
				p.Position = nil
			}
		}
		if p.Position != nil {
			pos := *p.Position
			if pos < 0 {
				pos = 0
			}
			if pos >= len(*siblings) {
				pos = len(*siblings) - 1
			}
			if pos != idx {
				list := append((*siblings)[:idx], (*siblings)[idx+1:]...)
				*siblings = insertNode(list, n, &pos)
				events = append(events, Event{Kind: "node.moved", Enum: enumID, From: newPath, To: newPath})
				changed = true
			}
		}
		return changed, events, nil
	})
}

// DeleteNode removes a node and its subtree; refused with has-members unless detach.
func (s *Store) DeleteNode(ifMatch *uint64, path string, detach bool) (uint64, bool, error) {
	enumID, ids := splitPath(path)
	if len(ids) == 0 {
		return s.Revision(), false, errf(ErrUnknownPath, "%q names an enum, not a node", path)
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		_, _, siblings, idx, err := d.findNode(path)
		if err != nil {
			return false, nil, err
		}
		if m := d.members(path); len(m) > 0 {
			if !detach {
				return false, nil, &Error{Code: ErrHasMembers, Message: "node has members", Detail: map[string]any{"refs": m}}
			}
			d.detach(path)
		}
		*siblings = append((*siblings)[:idx], (*siblings)[idx+1:]...)
		return true, []Event{{Kind: "node.deleted", Enum: enumID, Path: path}}, nil
	})
}

// Import applies a whole document as one revision, all or nothing.
func (s *Store) Import(ifMatch *uint64, doc *Document, mode ImportMode) (uint64, bool, error) {
	if doc.Format > Format {
		return s.Revision(), false, errf(ErrFormatUnsupported, "format %d is newer than %d", doc.Format, Format)
	}
	in := doc.Clone()
	if err := in.Validate(); err != nil {
		return s.Revision(), false, err
	}
	return s.mutate(ifMatch, func(d *Document) (bool, []Event, error) {
		before := d.Clone()
		switch mode {
		case ImportReplace:
			d.Objects, d.Enums = in.Objects, in.Enums
		case ImportMerge:
			for k, v := range in.Enums {
				d.Enums[k] = v
			}
			for k, v := range in.Objects {
				d.Objects[k] = v
			}
		}
		// an import that changes nothing (the same CCU pulled twice) is not a revision
		if sameDocument(before, d) {
			return false, nil, nil
		}
		return true, []Event{{Kind: "import", Objects: len(in.Objects), Enums: len(in.Enums)}}, nil
	})
}

// Reconcile sets the orphaned flag from what the interfaces report: a ref whose interface answered
// and does not list its address is orphaned; one that is listed again is not. Interfaces that did
// not answer (absent from present) are left alone — a silent interface must not orphan its
// devices. Returns how many objects changed.
func (s *Store) Reconcile(present map[string][]string) (changed int, err error) {
	listed := map[string]map[string]bool{}
	for iface, addrs := range present {
		m := make(map[string]bool, len(addrs))
		for _, a := range addrs {
			m[a] = true
		}
		listed[iface] = m
	}
	for ref, o := range s.Snapshot().Objects {
		i := strings.IndexByte(ref, '.')
		if i <= 0 {
			continue
		}
		iface, addr := ref[:i], ref[i+1:]
		m, answered := listed[iface]
		if !answered {
			continue
		}
		want := !m[addr]
		if o.Orphaned == want {
			continue
		}
		if _, did, e := s.SetOrphaned(ref, want); e != nil {
			err = e
		} else if did {
			changed++
		}
	}
	return changed, err
}

// sameDocument compares objects and enums (not the revision) by their canonical JSON.
func sameDocument(a, b *Document) bool {
	ja, _ := json.Marshal(struct {
		O map[string]*Object `json:"o"`
		E map[string]*Enum   `json:"e"`
	}{a.Objects, a.Enums})
	jb, _ := json.Marshal(struct {
		O map[string]*Object `json:"o"`
		E map[string]*Enum   `json:"e"`
	}{b.Objects, b.Enums})
	return bytes.Equal(ja, jb)
}
