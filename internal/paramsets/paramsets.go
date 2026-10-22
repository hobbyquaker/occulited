// Package paramsets is the paramset description cache (occulited task 108): what the interface
// processes answer to getParamsetDescription, kept once per device model, firmware and channel
// type, persisted in node-red-contrib-ccu's paramsets.json schema, and served to Control, to
// lite-rpc's clients and to anyone who wants the file.
//
// The schema is nrcc's as it is, so the two projects can share data: one flat JSON object,
// keyed "<interface>/<device type>/<firmware>/<version>/<channel type>/<paramset>" (the channel
// type is empty for a paramset of the device itself; a LINK paramset asked by its peer address
// is keyed LINK), each value the description as the process gave it, parameter id → the
// parameter's description (TYPE, OPERATIONS, FLAGS, MIN, MAX, DEFAULT, UNIT, VALUE_LIST, …) in
// lite-rpc's JSON form.
//
// Two devices can share a channel type name and differ in their VALUES paramset: HmIP-WTH and
// HmIP-eTRV both have HEATING_CLIMATECONTROL_TRANSCEIVER, only the wall thermostat has HUMIDITY,
// only the valve LEVEL and VALVE_STATE. Control described a type once, by whichever channel came
// first, and gave every channel of the type that description; the key here carries the device
// model and firmware, so each gets its own. A firmware update changes the key, and the new key
// is filled when it is first needed.
package paramsets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/literpc"
	"github.com/mdzio/go-hmccu/v2/itf/xmlrpc"
)

// FileName is the cache file's name in the state directory - nrcc's name, nrcc's schema.
const FileName = "paramsets.json"

// SaveDelay is how long after a change the file is written: a start fills many keys in a row,
// and they go into one write.
const SaveDelay = 2 * time.Second

// Description is one paramset description in lite-rpc's JSON form, parameter id → description.
type Description map[string]any

// Device is what the key needs of a device description: the parent device's type, firmware and
// version, and for a channel its own type and parent.
type Device struct {
	Address    string
	Type       string
	Parent     string // "" for a device
	ParentType string
	Firmware   string
	Version    int
	Paramsets  []string // the paramset names the process lists (MASTER, VALUES, LINK)
	Children   []string
}

// Forwarder makes one call to an interface process: lite-rpc's Forward with the method and its
// parameters. The cache marks its contexts with literpc.FromCache, so Forward sends them out
// instead of asking the cache.
type Forwarder func(ctx context.Context, iface string, method string, params ...*xmlrpc.Value) (*xmlrpc.Value, error)

// Cache is the store. The zero value needs Forward; File "" keeps it in memory only.
type Cache struct {
	// File is the paramsets.json path; "" = memory only.
	File string
	// Forward makes the process calls (getDeviceDescription, getParamsetDescription, listDevices).
	Forward Forwarder
	Log     *slog.Logger
	// Now and AfterFunc are the clock (tests).
	Now       func() time.Time
	AfterFunc func(d time.Duration, f func()) *time.Timer

	mu      sync.Mutex
	entries map[string]Description // key → description
	devices map[string]Device      // iface + "/" + address → the process's device description
	pending map[string]chan struct{}
	saveT   *time.Timer
	saved   bool // the file holds the entries
	loaded  bool
	// Filled counts the descriptions asked from the processes since the start (the log, tests).
	Filled int
}

func (c *Cache) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Cache) init() {
	if c.entries == nil {
		c.entries = map[string]Description{}
		c.devices = map[string]Device{}
		c.pending = map[string]chan struct{}{}
	}
}

// Load reads the file; a missing or unreadable file is an empty cache (logged when unreadable).
func (c *Cache) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.loaded = true
	if c.File == "" {
		return nil
	}
	b, err := os.ReadFile(c.File)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var in map[string]Description
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("%s: %w", c.File, err)
	}
	for k, d := range in {
		if d != nil && validKey(k) {
			c.entries[k] = d
		}
	}
	c.saved = true
	return nil
}

var keyRe = regexp.MustCompile(`^[^/]+/[^/]*/[^/]*/[^/]*/[^/]*/[^/]+$`)

func validKey(k string) bool { return keyRe.MatchString(k) }

// Key is the nrcc key of a paramset: interface, the parent device's type, firmware and version,
// the channel's type ("" for the device itself) and the paramset name; a paramset named by a peer
// address (a LINK paramset) is keyed LINK, as nrcc does it.
func Key(iface string, parent Device, channelType, paramset string) string {
	if isAddress(paramset) {
		paramset = "LINK"
	}
	return strings.Join([]string{iface, parent.Type, parent.Firmware, strconv.Itoa(parent.Version), channelType, paramset}, "/")
}

var addressRe = regexp.MustCompile(`^[0-9A-Za-z]+:[0-9]+$`)

func isAddress(s string) bool { return addressRe.MatchString(s) }

// KeyOf resolves the key of a channel's or device's paramset: the device descriptions are the
// cache's own (from a list sweep or an earlier look) or asked from the process now.
func (c *Cache) KeyOf(ctx context.Context, iface, address, paramset string) (string, error) {
	d, err := c.device(ctx, iface, address)
	if err != nil {
		return "", err
	}
	if d.Parent == "" {
		return Key(iface, d, "", paramset), nil
	}
	p, err := c.device(ctx, iface, d.Parent)
	if err != nil {
		return "", err
	}
	return Key(iface, p, d.Type, paramset), nil
}

// device is the process's description of an address, cached in memory.
func (c *Cache) device(ctx context.Context, iface, address string) (Device, error) {
	c.mu.Lock()
	c.init()
	d, ok := c.devices[iface+"/"+address]
	c.mu.Unlock()
	if ok {
		return d, nil
	}
	if c.Forward == nil {
		return Device{}, errors.New("no interface process to ask")
	}
	v, err := c.Forward(literpc.FromCache(ctx), iface, "getDeviceDescription", xmlrpc.NewString(address))
	if err != nil {
		return Device{}, err
	}
	d = deviceOf(xmlrpc.Q(v))
	if d.Address == "" {
		d.Address = address
	}
	c.mu.Lock()
	c.devices[iface+"/"+address] = d
	c.mu.Unlock()
	return d, nil
}

// deviceOf reads what the key needs from a device description.
func deviceOf(q *xmlrpc.Query) Device {
	d := Device{Address: q.TryKey("ADDRESS").String(), Type: q.TryKey("TYPE").String(), Parent: q.TryKey("PARENT").String(),
		ParentType: q.TryKey("PARENT_TYPE").String(), Firmware: q.TryKey("FIRMWARE").String()}
	if v := q.TryKey("VERSION"); v.IsNotEmpty() {
		d.Version = v.Int()
	}
	d.Paramsets = q.TryKey("PARAMSETS").Strings()
	d.Children = q.TryKey("CHILDREN").Strings()
	return d
}

// Remember takes device descriptions as a process listed them (listDevices, newDevices), so the
// keys of their paramsets need no getDeviceDescription.
func (c *Cache) Remember(iface string, list []Device) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	for _, d := range list {
		if d.Address != "" {
			c.devices[iface+"/"+d.Address] = d
		}
	}
}

// Forget drops the device descriptions of the addresses (deleted, updated or replaced devices):
// the next look asks the process again, and a changed firmware makes a new key.
func (c *Cache) Forget(iface string, addresses []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	for _, a := range addresses {
		delete(c.devices, iface+"/"+a)
		dev := a
		if i := strings.IndexByte(a, ':'); i > 0 {
			dev = a[:i]
		}
		for k := range c.devices {
			if strings.HasPrefix(k, iface+"/"+dev+":") || k == iface+"/"+dev {
				delete(c.devices, k)
			}
		}
	}
}

// Get is the description under a key, ok false when the cache has none.
func (c *Cache) Get(key string) (Description, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	d, ok := c.entries[key]
	return d, ok
}

// Description answers getParamsetDescription(address, paramset) on iface: from the cache under
// its key, else asked from the process once and kept. Two callers asking the same new key at once
// make one call. The answer is the XML-RPC value for lite-rpc; Describe is the JSON form.
func (c *Cache) Description(ctx context.Context, iface, address, paramset string) (*xmlrpc.Value, error) {
	key, d, err := c.Describe(ctx, iface, address, paramset)
	if err != nil {
		return nil, err
	}
	v, err := toValue(d)
	if err != nil {
		return nil, fmt.Errorf("the cached description %s: %w", key, err)
	}
	return v, nil
}

// Describe is Description in the JSON form, with the key.
func (c *Cache) Describe(ctx context.Context, iface, address, paramset string) (string, Description, error) {
	key, err := c.KeyOf(ctx, iface, address, paramset)
	if err != nil {
		return "", nil, err
	}
	d, err := c.fill(ctx, iface, address, paramset, key)
	return key, d, err
}

// fill is the description under key, asked from the process when the cache has none. Two
// callers asking the same new key at once make one call: the second waits for the first's
// answer, and asks itself only when the first failed.
func (c *Cache) fill(ctx context.Context, iface, address, paramset, key string) (Description, error) {
	for {
		c.mu.Lock()
		c.init()
		if d, ok := c.entries[key]; ok {
			c.mu.Unlock()
			return d, nil
		}
		if ch, busy := c.pending[key]; busy {
			c.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		ch := make(chan struct{})
		c.pending[key] = ch
		c.mu.Unlock()
		d, err := c.ask(ctx, iface, address, paramset, key)
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		close(ch)
		return d, err
	}
}

func (c *Cache) ask(ctx context.Context, iface, address, paramset, key string) (Description, error) {
	if c.Forward == nil {
		return nil, errors.New("no interface process to ask")
	}
	v, err := c.Forward(literpc.FromCache(ctx), iface, "getParamsetDescription", xmlrpc.NewString(address), xmlrpc.NewString(paramset))
	if err != nil {
		return nil, err
	}
	d, err := normalize(literpc.ToJSON(v))
	if err != nil {
		return nil, fmt.Errorf("getParamsetDescription(%s, %s) on %s: %w", address, paramset, iface, err)
	}
	c.mu.Lock()
	c.entries[key] = d
	c.Filled++
	c.saved = false
	c.scheduleSaveLocked()
	c.mu.Unlock()
	c.log().Debug("paramsets: a description learnt", "key", key, "parameters", len(d))
	return d, nil
}

// normalize takes a description in lite-rpc's JSON form through the encoder, so what the cache
// holds is exactly what the file holds (numbers as json.Number, which FromJSON reads back).
func normalize(j any) (Description, error) {
	b, err := json.Marshal(j)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var d Description
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errors.New("the answer is no struct")
	}
	return d, nil
}

// toValue is the cached JSON form back as the XML-RPC value, with the one thing the JSON form
// loses put back: a FLOAT parameter's MIN, MAX and DEFAULT are doubles whatever they look like
// (lite-rpc's JSON makes an integral number an i4).
func toValue(d Description) (*xmlrpc.Value, error) {
	out := map[string]any{}
	for id, p := range d {
		pm, ok := p.(map[string]any)
		if !ok {
			out[id] = p
			continue
		}
		if t, _ := pm["TYPE"].(string); t == "FLOAT" {
			cp := make(map[string]any, len(pm))
			for k, v := range pm {
				cp[k] = v
			}
			for _, k := range []string{"MIN", "MAX", "DEFAULT"} {
				if n, ok := number(cp[k]); ok {
					cp[k] = map[string]any{"double": n}
				}
			}
			pm = cp
		}
		out[id] = pm
	}
	return literpc.FromJSON(out)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// Sweep takes an interface's whole device list (listDevices) as the process answered it: every
// device description is remembered, and every channel's VALUES paramset whose key the cache has
// not got is asked, one after another. It returns how many it asked. It is what a start and a
// process that came up run, so Control's first load finds everything here.
func (c *Cache) Sweep(ctx context.Context, iface string, list *xmlrpc.Value) (int, error) {
	q := xmlrpc.Q(list)
	var devs []Device
	for _, e := range q.Slice() {
		devs = append(devs, deviceOf(e))
	}
	if q.Err() != nil {
		return 0, fmt.Errorf("invalid listDevices answer: %w", q.Err())
	}
	c.Remember(iface, devs)
	byAddr := map[string]Device{}
	for _, d := range devs {
		byAddr[d.Address] = d
	}
	asked := 0
	seen := map[string]bool{}
	for _, d := range devs {
		if ctx.Err() != nil {
			return asked, ctx.Err()
		}
		if d.Parent == "" || !has(d.Paramsets, "VALUES") {
			continue
		}
		p, ok := byAddr[d.Parent]
		if !ok {
			continue
		}
		key := Key(iface, p, d.Type, "VALUES")
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := c.Get(key); ok {
			continue
		}
		if _, err := c.fill(ctx, iface, d.Address, "VALUES", key); err != nil {
			c.log().Debug("paramsets: a description could not be read", "interface", iface, "channel", d.Address, "err", err)
			continue
		}
		asked++
	}
	return asked, nil
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Keys is every key the cache holds, sorted.
func (c *Cache) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	out := make([]string, 0, len(c.entries))
	for k := range c.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// All is the whole cache in the file's schema (a copy of the map; the descriptions are shared).
func (c *Cache) All() map[string]Description {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	out := make(map[string]Description, len(c.entries))
	for k, d := range c.entries {
		out[k] = d
	}
	return out
}

// Len is the number of descriptions held.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Cache) scheduleSaveLocked() {
	if c.File == "" || c.saveT != nil {
		return
	}
	after := c.AfterFunc
	if after == nil {
		after = time.AfterFunc
	}
	c.saveT = after(SaveDelay, func() {
		c.mu.Lock()
		c.saveT = nil
		c.mu.Unlock()
		if err := c.Save(); err != nil {
			c.log().Warn("paramsets: the cache could not be written", "file", c.File, "err", err)
		}
	})
}

// Save writes the file now when it is behind (the schedule's write, and the shutdown's). Atomic:
// a temporary file renamed over the old one; keys sorted, two-space indent as nrcc writes it.
func (c *Cache) Save() error {
	c.mu.Lock()
	if c.File == "" || c.saved {
		c.mu.Unlock()
		return nil
	}
	b, err := marshal(c.entries)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := writeAtomic(c.File, b); err != nil {
		return err
	}
	c.mu.Lock()
	c.saved = true
	c.mu.Unlock()
	c.log().Info("paramsets: the description cache written", "file", c.File, "descriptions", len(c.entries))
	return nil
}

func marshal(entries map[string]Description) ([]byte, error) {
	return json.MarshalIndent(entries, "", "  ") // encoding/json sorts a map's keys
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
