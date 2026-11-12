// Package firewall is task 157's firewall (D-105): one ordered list of INPUT rules, rendered into iptables-restore texts for IPv4 and IPv6 and loaded by occulited. It replaces
// OpenCCU's libfirewall (firewall.conf with its modes, services, allowed addresses and user ports).
//
// Everything here is pure: the model and its validation, the rendering, the conversion of an old
// firewall.conf, and the owners' automatic rules. Loading, the confirm window and the files are
// the system package's and the privilege helper's.
package firewall

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"strings"
	"time"
)

// RulesPath is the rule file; it is in /etc/config, so a backup carries it.
const RulesPath = "/etc/config/firewall-rules.json"

// The address families a rule covers.
const (
	FamilyV4   = "ipv4"
	FamilyV6   = "ipv6"
	FamilyBoth = "both"
)

// The targets and policies.
const (
	Accept = "ACCEPT"
	Drop   = "DROP"
	Reject = "REJECT"
)

// LocalNetworks is the source token for the local networks (the private, link-local and CGNAT
// ranges, and every global network on the box's interfaces) - what libfirewall's local-only chain
// allowed for the web server and SSH.
const LocalNetworks = "local networks"

// AnySource is the default source: every address.
const AnySource = "0/0"

// The owners of automatic rules. An addon's owner is OwnerAddonPrefix + its id.
const (
	OwnerWeb         = "web"
	OwnerSSH         = "ssh"
	OwnerRPC         = "rpc"
	OwnerHmIPAP      = "hmip-ap"
	OwnerDiscovery   = "discovery"
	OwnerACME        = "acme"
	OwnerAddonPrefix = "addon:"
)

// The protocols a rule matches; only tcp and udp have ports.
const (
	ProtoTCP  = "tcp"
	ProtoUDP  = "udp"
	ProtoIGMP = "igmp"
)

// Rule is one INPUT rule: a protocol and its destination port, from a source, for one or both
// families. Port 0 is every port; the source port and the destination narrow a rule further (the
// discovery rules: replies from eQ-3's discovery ports, link-local multicast).
type Rule struct {
	ID   string `json:"id"`
	Port int    `json:"port"`
	// PortTo makes the rule a range, Port..PortTo (task 171); 0 is the one port.
	PortTo int    `json:"port_to,omitempty"`
	Proto  string `json:"proto"` // tcp, udp or igmp
	SPort  int    `json:"sport,omitempty"`
	Source string `json:"source"`         // an address, a CIDR, 0/0 or LocalNetworks
	Dest   string `json:"dest,omitempty"` // an address or a CIDR; empty for every destination
	Family string `json:"family"`         // ipv4, ipv6 or both
	Target string `json:"target"`         // ACCEPT, DROP or REJECT
	Log    bool   `json:"log,omitempty"`
	// Disabled keeps the rule in the list without loading it (task 170): switched off, not deleted.
	Disabled bool   `json:"disabled,omitempty"`
	Comment  string `json:"comment,omitempty"`
	// Owner is the feature an automatic rule belongs to (web, ssh, rpc, hmip-ap, addon:<id>);
	// empty for a rule made by hand. Switching the feature off removes every rule it owns.
	Owner string `json:"owner,omitempty"`
}

// Policy is the INPUT chain's policy per family and whether what reaches it is logged.
type Policy struct {
	V4  string `json:"ipv4"`
	V6  string `json:"ipv6"`
	Log bool   `json:"log,omitempty"`
}

// Migration says what a conversion from firewall.conf did; the page shows it until dismissed.
type Migration struct {
	At        time.Time `json:"at"`
	Notes     []Note    `json:"notes"`
	Dismissed bool      `json:"dismissed,omitempty"`
}

// Note is one line of the conversion's notice: an English template with {name} placeholders, the
// page's translation key, and its values - so the page says it in the viewer's language.
type Note struct {
	Text string            `json:"text"`
	Args map[string]string `json:"args,omitempty"`
}

// NewNote pairs the template with its values: key, value, key, value.
func NewNote(text string, kv ...string) Note {
	n := Note{Text: text}
	if len(kv) > 0 {
		n.Args = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			n.Args[kv[i]] = kv[i+1]
		}
	}
	return n
}

// Config is the rule file.
type Config struct {
	Version int    `json:"version"`
	Policy  Policy `json:"policy"`
	Rules   []Rule `json:"rules"`
	// Active is what the owners' automatic rules were last made for: an owner that is switched
	// on, and for an addon each opened port ("addon:<id>/<port>/<proto>"). Only a change of this
	// adds or removes owned rules, so a user who deleted an owned rule while its feature stays on
	// keeps it deleted.
	Active    []string   `json:"active"`
	Migration *Migration `json:"migration,omitempty"`
	// LocalV6 are the user's IPv6 networks that LocalNetworks includes besides the automatic ones
	// (openccu-lite B-339): IPv6 has no private range that covers every VLAN as RFC 1918 does for
	// IPv4, and the interfaces give the on-link /64 alone, so the clients in another VLAN of the
	// same delegated prefix are named here (the prefix, say). IPv4 has no such list.
	LocalV6 []string `json:"local_ipv6,omitempty"`
}

// Clone is a deep copy.
func (c Config) Clone() Config {
	out := c
	out.Rules = append([]Rule{}, c.Rules...)
	out.Active = append([]string{}, c.Active...)
	if c.LocalV6 != nil {
		out.LocalV6 = append([]string{}, c.LocalV6...)
	}
	if c.Migration != nil {
		m := *c.Migration
		m.Notes = make([]Note, len(c.Migration.Notes))
		for i, n := range c.Migration.Notes {
			m.Notes[i] = Note{Text: n.Text, Args: maps.Clone(n.Args)}
		}
		out.Migration = &m
	}
	return out
}

// NewID is a rule id: 8 hex digits.
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SourceFamily is the family a source limits a rule to: ipv4 for an IPv4 address or network,
// ipv6 for an IPv6 one, "" for 0/0 and LocalNetworks (both).
func SourceFamily(src string) string {
	s := strings.TrimSpace(src)
	if s == "" || s == AnySource || s == LocalNetworks {
		return ""
	}
	host := s
	if ip, _, err := net.ParseCIDR(s); err == nil {
		host = ip.String()
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		return FamilyV4
	}
	return FamilyV6
}

// Covers says whether a rule is loaded into the given family (ipv4 or ipv6).
func (r Rule) Covers(family string) bool {
	return r.Family == FamilyBoth || r.Family == family
}

// What names the rule in an error: its port (or range) and protocol, or the protocol alone.
func (r Rule) What() string {
	if r.Port == 0 {
		return r.Proto
	}
	if r.PortTo != 0 {
		return fmt.Sprintf("ports %d-%d/%s", r.Port, r.PortTo, r.Proto)
	}
	return fmt.Sprintf("port %d/%s", r.Port, r.Proto)
}

// HasPort says whether the rule's destination port covers port: every port, the one, or the range.
func (r Rule) HasPort(port int) bool {
	if r.Port == 0 {
		return true
	}
	if r.PortTo != 0 {
		return port >= r.Port && port <= r.PortTo
	}
	return port == r.Port
}

func validSource(s string) bool {
	if s == AnySource || s == LocalNetworks {
		return true
	}
	if _, _, err := net.ParseCIDR(s); err == nil {
		return true
	}
	return net.ParseIP(s) != nil
}

// Validate checks one rule.
func (r Rule) Validate() error {
	w := r.What()
	switch r.Proto {
	case ProtoTCP, ProtoUDP:
		if r.Port < 0 || r.Port > 65535 || r.SPort < 0 || r.SPort > 65535 {
			return fmt.Errorf("%s: a port is 1 to 65535, or empty for every port", w)
		}
	case ProtoIGMP:
		if r.Port != 0 || r.SPort != 0 || r.PortTo != 0 {
			return fmt.Errorf("%s: igmp has no ports", w)
		}
	default:
		return fmt.Errorf("%s: the protocol is tcp, udp or igmp", w)
	}
	if r.PortTo != 0 && (r.Port < 1 || r.PortTo <= r.Port || r.PortTo > 65535) {
		return fmt.Errorf("%s: a range runs from a lower port to a higher one, at most 65535", w)
	}
	if !validSource(r.Source) {
		return fmt.Errorf("%s: the source %q is not an address, a network, 0/0 or %q", w, r.Source, LocalNetworks)
	}
	if r.Dest != "" && (r.Dest == AnySource || r.Dest == LocalNetworks || !validSource(r.Dest)) {
		return fmt.Errorf("%s: the destination %q is not an address or a network", w, r.Dest)
	}
	switch r.Family {
	case FamilyV4, FamilyV6, FamilyBoth:
	default:
		return fmt.Errorf("%s: the family is ipv4, ipv6 or both", w)
	}
	sf, df := SourceFamily(r.Source), SourceFamily(r.Dest)
	if sf != "" && df != "" && sf != df {
		return fmt.Errorf("%s: the source %s and the destination %s are of different families", w, r.Source, r.Dest)
	}
	if sf != "" && r.Family != sf {
		return fmt.Errorf("%s: the source %s is an %s address, so the rule is for %s only", w, r.Source, sf, sf)
	}
	if df != "" && r.Family != df {
		return fmt.Errorf("%s: the destination %s is an %s address, so the rule is for %s only", w, r.Dest, df, df)
	}
	switch r.Target {
	case Accept, Drop, Reject:
	default:
		return fmt.Errorf("%s: the target is ACCEPT, DROP or REJECT", w)
	}
	if len(r.Comment) > 200 || strings.ContainsAny(r.Comment, "\n\r\"") {
		return fmt.Errorf("%s: the comment is one line of at most 200 characters, without quotes", w)
	}
	if r.ID == "" || len(r.ID) > 32 || strings.ContainsAny(r.ID, " \"\n") {
		return fmt.Errorf("%s: a rule needs an id", w)
	}
	return nil
}

// Validate checks the whole configuration.
func (c Config) Validate() error {
	for _, p := range []string{c.Policy.V4, c.Policy.V6} {
		if p != Accept && p != Drop {
			return errors.New("the policy is DROP or ACCEPT")
		}
	}
	seen := map[string]bool{}
	for _, r := range c.Rules {
		if err := r.Validate(); err != nil {
			return err
		}
		if seen[r.ID] {
			return fmt.Errorf("the rule id %s is there twice", r.ID)
		}
		seen[r.ID] = true
	}
	if len(c.LocalV6) > 64 {
		return errors.New("local networks (IPv6): at most 64 networks")
	}
	nets := map[string]bool{}
	for _, s := range c.LocalV6 {
		if s == AnySource || s == LocalNetworks || !validSource(s) || SourceFamily(s) != FamilyV6 {
			return fmt.Errorf("local networks (IPv6): %q is not an IPv6 address or network", s)
		}
		if nets[s] {
			return fmt.Errorf("local networks (IPv6): %s is there twice", s)
		}
		nets[s] = true
	}
	return nil
}

// OwnedRule is an automatic rule's defaults: the web server, SSH and classic RPC default to the
// local networks (the old local-only chain), everything else to 0/0; a destination's family is the
// rule's.
func OwnedRule(owner string, p PortSpec) Rule {
	src := AnySource
	if owner == OwnerWeb || owner == OwnerSSH || owner == OwnerRPC || p.Local {
		// classic RPC too (the maintainer, task 143): as OpenCCU ran full XMLRPC
		src = LocalNetworks
	}
	fam := FamilyBoth
	if f := SourceFamily(p.Dest); f != "" {
		fam = f
	}
	return Rule{ID: OwnedID(owner, p), Port: p.Port, PortTo: p.PortTo, Proto: p.Proto, SPort: p.SPort, Source: src, Dest: p.Dest, Family: fam, Target: Accept, Comment: p.Comment, Owner: owner}
}

// OwnedID is an owned rule's id: 8 hex digits derived from the owner and the port spec (its
// active key), the same at every start and after a remove and re-add - not a random one, which
// made the four hmip-ap rules new at every boot and the rule file and the draft rewritten twice
// per boot (openccu-lite B-181). A user's rule keeps NewID.
func OwnedID(owner string, p PortSpec) string {
	sum := sha256.Sum256([]byte("owned:" + activeKey(owner, p)))
	return hex.EncodeToString(sum[:4])
}

// Default is a fresh system's configuration: policy DROP in both families, the web server's and
// the network discovery's rules.
func Default() Config {
	c := Config{Version: 1, Policy: Policy{V4: Drop, V6: Drop}, Rules: []Rule{}, Active: []string{}}
	return Reconcile(c, AlwaysOwners())
}
