package system

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hobbyquaker/occulited/internal/firewall"
)

// ---- the addons' ports in the firewall (D-47, task 157) -------------------------------------
//
// B-52: under a DROP policy an addon's ports are closed until a rule opens them. The catalogue's
// runtime block declares an addon's ports, and the maintainer wants them opened one by one - the TLS
// listener but not the plaintext one - so each declared port is a switch (on the addon's page since
// task 157) and the opened ones live in the addon's policy (AddonPolicy.OpenPorts). Each opened port
// is an owned rule of the firewall (owner addon:<id>, source 0/0): switching it off, or uninstalling
// the addon, removes the rule (FirewallRules.Sync, through FirewallChanged).

// FirewallChanged is what a change the firewall follows calls - SSH switched, an addon's port
// opened or closed, an addon uninstalled: main points it at FirewallRules.Sync. Nil does nothing.
var FirewallChanged func(ctx context.Context)

func notifyFirewall(ctx context.Context) {
	if FirewallChanged != nil {
		FirewallChanged(ctx)
	}
}

// FirewallManager is the addons' side of the firewall: their declared ports and the switches.
type FirewallManager struct {
	Root Root
	// Declared is the addon's stored manifest's runtime block (Root.DeclaredRuntime; nil for
	// none); what an addon declares is that merged with its policy's own block (MergeRuntime).
	Declared func(id string) *AddonRuntime
}

// declared is the merged declaration of one addon.
func (m FirewallManager) declared(id string, p *AddonPolicy) *AddonRuntime {
	var cat *AddonRuntime
	if m.Declared != nil {
		cat = m.Declared(id)
	}
	return MergeRuntime(p.Runtime, cat)
}

// FirewallAddon is one addon's declared ports as GET /firewall lists them.
type FirewallAddon struct {
	ID    string              `json:"id"`
	Name  string              `json:"name"`
	Mode  string              `json:"mode"`
	Ports []FirewallAddonPort `json:"ports"`
}

// FirewallAddonPort is one declared port: the declaration, whether a socket is on it, whether the
// user opened it.
type FirewallAddonPort struct {
	Port int `json:"port"`
	// PortTo makes the declaration a range Port..PortTo (openccu-lite task 105); the switch and
	// the open list name its first port.
	PortTo int               `json:"port_to,omitempty"`
	Proto  string            `json:"proto,omitempty"`
	TLS    bool              `json:"tls,omitempty"`
	Label  map[string]string `json:"label,omitempty"`
	// Listening: the addon holds a socket on that port and protocol, loopback or not
	// (ListeningPorts): one of its unit's processes owns it (occulited B-31). Without the socket
	// owners (the helper did not answer) any socket on the port and protocol counts, and
	// OwnerUnknown says so.
	Listening bool `json:"listening"`
	// HeldBy is the process that holds the port when it is not the addon's (occulited B-31): RedMatic's
	// node-red on a port openccu-loom declares. Absent when nobody holds it or the owner is unknown.
	HeldBy *PortHolder `json:"held_by,omitempty"`
	// OwnerUnknown: a socket is open on the port, and whose could not be read.
	OwnerUnknown bool `json:"owner_unknown,omitempty"`
	// Open: the port is in the policy's OpenPorts, so USERPORTS carries it.
	Open bool `json:"open"`
}

// PortHolder is the process that holds a declared port, as the helper's socket owners name it.
type PortHolder struct {
	Process string `json:"process,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

// addonPortState is a declared port's listening state from the sockets on it (occulited B-31): the
// protocol must match (tcp and tcp6 are tcp; a port declared without one takes either), and the
// socket must be held by a process in the addon's own unit. Another holder is named; an owner
// that could not be read is said, and counts as listening as before.
func addonPortState(id string, d Port, listening []Listener) FirewallAddonPort {
	p := FirewallAddonPort{Port: d.Port, PortTo: d.PortTo, Proto: d.Proto, TLS: d.TLS, Label: d.Label}
	unit := "addon-" + id + ".service"
	var other *PortHolder
	unknown := false
	for _, l := range listening {
		// a range: any socket inside it
		inRange := l.Port == d.Port || (d.PortTo != 0 && l.Port > d.Port && l.Port <= d.PortTo)
		if !inRange || (d.Proto != "" && strings.TrimSuffix(l.Proto, "6") != d.Proto) {
			continue
		}
		switch {
		case l.Unit == unit:
			p.Listening = true
			return p
		case l.Unit == "" && l.Process == "":
			unknown = true
		case other == nil:
			other = &PortHolder{Process: l.Process, Unit: l.Unit}
		}
	}
	if unknown {
		p.Listening, p.OwnerUnknown = true, true
		return p
	}
	p.HeldBy = other
	return p
}

// AddonPorts is every installed addon's opened ports: port -> addon id. A policy whose addon is
// no longer in rc.d counts for nothing - the policy file outlives an uninstall (it reserves the
// uid), its ports do not.
func (m FirewallManager) AddonPorts() map[int]string {
	installed := map[string]bool{}
	for _, id := range rcdAddonIDs(m.Root) {
		installed[id] = true
	}
	out := map[int]string{}
	for id, p := range m.Root.AddonPolicies() {
		if !installed[id] {
			continue
		}
		declared := m.declared(id, p)
		for _, n := range p.OpenPorts {
			if declared.Declares(n) {
				if prev, ok := out[n]; !ok || id < prev {
					out[n] = id
				}
			}
		}
	}
	return out
}

// SetAddonPorts stores which declared ports of an addon are open, and the firewall follows: an
// owned rule per opened port.
func (m FirewallManager) SetAddonPorts(ctx context.Context, id string, open []int) error {
	p := m.Root.ReadAddonPolicy(id)
	if p == nil {
		return fmt.Errorf("addon %q has no policy", id)
	}
	if _, err := m.Root.SetAddonOpenPorts(id, open, m.declared(id, p)); err != nil {
		return err
	}
	notifyFirewall(ctx)
	return nil
}

// AddonOwners is the firewall's addon owners: for every installed addon its opened ports that it
// declares, with their protocol (tcp unless the declaration says udp).
func (m FirewallManager) AddonOwners() map[string][]firewall.PortSpec {
	installed := map[string]bool{}
	for _, id := range rcdAddonIDs(m.Root) {
		installed[id] = true
	}
	out := map[string][]firewall.PortSpec{}
	for id, p := range m.Root.AddonPolicies() {
		if !installed[id] || len(p.OpenPorts) == 0 {
			continue
		}
		declared := m.declared(id, p).DeclaredPorts()
		for _, n := range p.OpenPorts {
			for _, d := range declared {
				if d.Port != n {
					continue
				}
				proto := "tcp"
				if d.Proto == "udp" {
					proto = "udp"
				}
				out[firewall.OwnerAddonPrefix+id] = append(out[firewall.OwnerAddonPrefix+id], firewall.PortSpec{Port: n, PortTo: d.PortTo, Proto: proto, Comment: addonPortComment(id, d)})
			}
		}
	}
	return out
}

// Addons lists every installed addon that declares ports, with each port's state. names maps an
// id to its display name (the id when missing); listening is ListeningPorts' answer.
func (m FirewallManager) Addons(names map[string]string, listening []Listener) []FirewallAddon {
	installed := map[string]bool{}
	for _, id := range rcdAddonIDs(m.Root) {
		installed[id] = true
	}
	out := []FirewallAddon{}
	for id, p := range m.Root.AddonPolicies() {
		declared := m.declared(id, p).DeclaredPorts()
		if !installed[id] || len(declared) == 0 {
			continue
		}
		a := FirewallAddon{ID: id, Name: names[id], Mode: p.Mode, Ports: []FirewallAddonPort{}}
		if a.Name == "" {
			a.Name = id
		}
		for _, d := range declared {
			port := addonPortState(id, d, listening)
			port.Open = slices.Contains(p.OpenPorts, d.Port)
			a.Ports = append(a.Ports, port)
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// addonPortComment is an addon's opened port's comment: its declared label, in English.
func addonPortComment(id string, d Port) string {
	label := d.Label["en"]
	if label == "" {
		label = d.Label["de"]
	}
	if d.TLS && label != "" && !strings.Contains(label, "TLS") {
		label += " (TLS)"
	}
	if label == "" {
		return id
	}
	return id + ": " + label
}
