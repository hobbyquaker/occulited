package firewall

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// PortSpec is one rule an owner wants: a protocol and its destination port (or range, Port..PortTo
// - an addon's declared range, openccu-lite task 105), narrowed by a source port or a destination
// where the rule needs one.
type PortSpec struct {
	Port   int
	PortTo int
	Proto  string
	SPort  int
	Dest   string
	// Comment is the new rule's comment: why the rule is there. Not part of the rule's identity.
	Comment string
	// Local makes the new rule's source the local networks instead of every address (the mDNS
	// rules, openccu-lite B-219). Not part of the rule's identity either.
	Local bool
}

// SpecOf is the owner's spec a rule was made from.
func SpecOf(r Rule) PortSpec {
	return PortSpec{Port: r.Port, PortTo: r.PortTo, Proto: r.Proto, SPort: r.SPort, Dest: r.Dest}
}

// The owners' ports (D-105).
var (
	// WebPorts: lighttpd; 80 stays open for the HTTP → HTTPS redirect.
	WebPorts = []PortSpec{
		{Port: 80, Proto: ProtoTCP, Comment: "lighttpd HTTP, redirects to HTTPS"},
		{Port: 443, Proto: ProtoTCP, Comment: "lighttpd HTTPS: web UI and API"},
	}
	// SSHPorts while /etc/config/sshEnabled exists.
	SSHPorts = []PortSpec{{Port: 22, Proto: ProtoTCP, Comment: "sshd"}}
	// HmIPAPPorts: hmipserver's 9293 and 9294 and its discovery on udp 43438, while HmIP-RF runs
	// - what HmIP-HAPs and DRAPs connect to. The eQ-3 discovery on udp 43439 was here until
	// openccu-lite B-221; it is occulited's own and the apps' (D-108, D-110), so it is in
	// DiscoveryPorts, which every system has.
	HmIPAPPorts = []PortSpec{
		{Port: 9293, Proto: ProtoTCP, Comment: "hmipserver update server: HmIP-HAP, HmIPW-DRAP"},
		{Port: 9294, Proto: ProtoTCP, Comment: "hmipserver update server: HmIP-HAP2"},
		{Port: 43438, Proto: ProtoUDP, Comment: "hmipserver: HmIP access points (HAP, DRAP)"},
	}
	// DiscoveryPorts are what libfirewall allowed for finding and being found, always (the
	// maintainer, 2026-09-18: in the one list, editable, ACCEPT by default): link-local multicast
	// (mDNS, which RedMatic's Matter bridge answers; ff02::/16 its IPv6 counterpart, which
	// libfirewall did not have), IGMP, SSDP (occulited itself since task 165), and the discovery
	// of the radio stack - none of these three reply rules was eq3configd's, whatever its name
	// suggested: they are hmipserver's and rfd's own (task 163). The replies from
	// udp 43439 (HmIP access points) and udp 23272 (LAN gateways), and 23272 itself.
	//
	// The eQ-3 discovery on udp 43439, occulited's answer to the apps' "who are you" (task 163), on
	// a system without HmIP-RF too (openccu-lite B-221: it was an HmIP access point port, and a
	// BidCos-only system with the firewall on answered nobody). mDNS, udp 5353 from the local
	// networks (openccu-lite B-219, the maintainer: "5353 udp per default accept"): the answers
	// an mDNS query of this system gets - the HB-RF-ETH's Find asks for unicast answers, which come
	// from the responder's 5353 to the query's own port and match no conntrack entry, since the
	// query went to the group - and the queries sent to this system's 5353 (a responder of an addon
	// there, RedMatic's, already hears the group's).
	DiscoveryPorts = []PortSpec{
		{Proto: ProtoUDP, Dest: "224.0.0.0/24", Comment: "link-local multicast: mDNS (Matter), discovery"},
		{Proto: ProtoIGMP, Dest: "224.0.0.0/24", Comment: "IGMP: multicast group membership"},
		{Proto: ProtoUDP, Dest: "ff02::/16", Comment: "IPv6 link-local multicast: mDNS (Matter)"},
		{Port: 1900, Proto: ProtoUDP, Comment: "SSDP: apps find the system"},
		{Proto: ProtoUDP, SPort: 43439, Comment: "eQ-3 discovery replies: access points, gateways, systems"},
		{Proto: ProtoUDP, SPort: 23272, Comment: "discovery replies of LAN gateways"},
		{Port: 23272, Proto: ProtoUDP, Comment: "LAN gateway discovery"},
		{Port: 43439, Proto: ProtoUDP, Comment: "eQ-3 discovery: apps find the system"},
		{Proto: ProtoUDP, SPort: 5353, Local: true, Comment: "mDNS answers: HB-RF-ETH boards and other devices found"},
		{Port: 5353, Proto: ProtoUDP, Local: true, Comment: "mDNS queries to this system"},
	}

	// movedOwned are ports that went from one owner to another: an existing system keeps the rule
	// where it was, with its target, source and switch, under the new owner (the user's choices on
	// it survive the move, as D-110 wants them to survive a pause). old key -> new key.
	movedOwned = map[string]string{
		"hmip-ap/43439/udp": "discovery/43439/udp", // openccu-lite B-221
	}

	// XMLRPCComments say what is behind each port of firewall.conf's XMLRPC service (lighttpd
	// proxies them to the interface processes); classic RPC's owned rules (task 143) use them too.
	XMLRPCComments = map[int]string{
		2000:  "XMLRPC hs485d (Wired)",
		42000: "XMLRPC hs485d (Wired) TLS",
		2001:  "XMLRPC rfd (BidCos-RF)",
		42001: "XMLRPC rfd (BidCos-RF) TLS",
		2002:  "XMLRPC 2002: nothing listens on it on this system",
		2010:  "XMLRPC hmipserver (HmIP-RF)",
		42010: "XMLRPC hmipserver (HmIP-RF) TLS",
		9292:  "XMLRPC HMServer (virtual devices, groups)",
		49292: "XMLRPC HMServer (virtual devices, groups) TLS",
	}
)

// ACMEPorts: while the certificate is ACME with the HTTP-01 challenge the CA must reach port 80
// from the internet (task 175); lighttpd answers there only the challenge path, which it hands to
// occulited, and the redirect to HTTPS.
var ACMEPorts = []PortSpec{{Port: 80, Proto: ProtoTCP, Comment: "ACME HTTP-01: the CA fetches /.well-known/acme-challenge/"}}

// AlwaysOwners are the owners every system has: the web server and the network discovery.
func AlwaysOwners() map[string][]PortSpec {
	return map[string][]PortSpec{OwnerWeb: WebPorts, OwnerDiscovery: DiscoveryPorts}
}

// OwnerLabel is the owner as the page names it.
func OwnerLabel(owner string) string {
	switch owner {
	case OwnerWeb:
		return "Web server"
	case OwnerSSH:
		return "SSH"
	case OwnerRPC:
		return "Classic RPC"
	case OwnerHmIPAP:
		return "HmIP access points"
	case OwnerDiscovery:
		return "Network discovery"
	case OwnerACME:
		return "ACME HTTP-01"
	}
	if id, ok := strings.CutPrefix(owner, OwnerAddonPrefix); ok {
		return "addon: " + id
	}
	return owner
}

func ownerRank(o string) int {
	switch o {
	case OwnerWeb:
		return 0
	case OwnerSSH:
		return 1
	case OwnerRPC:
		return 2
	case OwnerHmIPAP:
		return 3
	case OwnerDiscovery:
		return 5
	}
	return 4 // the addons
}

// activeKey is an owner's spec in Config.Active: "<owner>/<port>/<proto>", with
// "|<sport>|<dest>" after it where the spec has either, and "<port>-<to>" for a range (task 105).
func activeKey(owner string, p PortSpec) string {
	port := strconv.Itoa(p.Port)
	if p.PortTo != 0 {
		port += "-" + strconv.Itoa(p.PortTo)
	}
	k := fmt.Sprintf("%s/%s/%s", owner, port, p.Proto)
	if p.SPort != 0 || p.Dest != "" {
		k += fmt.Sprintf("|%d|%s", p.SPort, p.Dest)
	}
	return k
}

// RuleKey is the key of the spec an owned rule stands for.
func RuleKey(r Rule) string { return activeKey(r.Owner, SpecOf(r)) }

func splitKey(k string) (owner string, p PortSpec, ok bool) {
	base, extra, hasExtra := strings.Cut(k, "|")
	if hasExtra {
		sp, dest, ok := strings.Cut(extra, "|")
		if !ok {
			return "", p, false
		}
		if _, err := fmt.Sscanf(sp, "%d", &p.SPort); err != nil {
			return "", p, false
		}
		p.Dest = dest
	}
	i := strings.LastIndex(base, "/")
	if i < 0 {
		return "", p, false
	}
	p.Proto = base[i+1:]
	rest := base[:i]
	j := strings.LastIndex(rest, "/")
	if j < 0 {
		return "", p, false
	}
	port, to, isRange := strings.Cut(rest[j+1:], "-")
	if _, err := fmt.Sscanf(port, "%d", &p.Port); err != nil {
		return "", p, false
	}
	if isRange {
		if _, err := fmt.Sscanf(to, "%d", &p.PortTo); err != nil {
			return "", p, false
		}
	}
	return rest[:j], p, true
}

// Reconcile brings the owned rules in line with what the owners want now: desired maps each
// switched-on owner to its ports. A port an owner newly wants gets its default rule at the end of
// the list; a port it no longer wants loses every rule of that owner on that port - edited ones and
// duplicates too; an owner that wants nothing any more loses all its rules. A port wanted before
// and now is left alone, so a rule the user deleted stays deleted.
func Reconcile(c Config, desired map[string][]PortSpec) Config {
	out := c.Clone()
	want := map[string]bool{}
	wantOwner := map[string]bool{}
	for owner, ports := range desired {
		for _, p := range ports {
			want[activeKey(owner, p)] = true
			wantOwner[owner] = true
		}
	}
	had := map[string]bool{}
	for _, k := range out.Active {
		had[k] = true
	}
	// removals first: a port no longer wanted, and every rule of an owner that wants nothing
	gone := func(r Rule) bool {
		if r.Owner == "" {
			return false
		}
		if !wantOwner[r.Owner] {
			return true
		}
		k := RuleKey(r)
		return had[k] && !want[k]
	}
	// a moved port first: its rule is rewritten under the new owner in place, so neither the
	// removal nor the addition below sees it
	for i, r := range out.Rules {
		if r.Owner == "" {
			continue
		}
		k := RuleKey(r)
		nk, ok := movedOwned[k]
		if !ok || want[k] || !want[nk] || had[nk] {
			continue
		}
		owner, p, _ := splitKey(nk)
		if np, ok := ownedSpec(owner, p); ok {
			p = np // its comment
		}
		fresh := OwnedRule(owner, p)
		if old, ok := ownedSpec(r.Owner, SpecOf(r)); ok && r.Comment == old.Comment {
			out.Rules[i].Comment = fresh.Comment
		}
		out.Rules[i].Owner, out.Rules[i].ID = owner, fresh.ID
		had[nk] = true
	}
	out.Rules = slices.DeleteFunc(out.Rules, gone)
	// additions in a stable order: owners by rank (the web server first, the network discovery
	// last), then by name; ports as the owner lists them
	owners := make([]string, 0, len(desired))
	for o := range desired {
		owners = append(owners, o)
	}
	sort.Slice(owners, func(i, j int) bool {
		if ri, rj := ownerRank(owners[i]), ownerRank(owners[j]); ri != rj {
			return ri < rj
		}
		return owners[i] < owners[j]
	})
	for _, o := range owners {
		for _, p := range desired[o] {
			if k := activeKey(o, p); !had[k] {
				out.Rules = append(out.Rules, OwnedRule(o, p))
			}
		}
	}
	out.Active = out.Active[:0]
	for k := range want {
		out.Active = append(out.Active, k)
	}
	sort.Strings(out.Active)
	return out
}

// ownedSpec is the spec an owner lists for this port, with its comment - also for a spec an owner
// no longer lists but did (a moved port's old comment).
func ownedSpec(owner string, p PortSpec) (PortSpec, bool) {
	lists := map[string][]PortSpec{OwnerWeb: WebPorts, OwnerSSH: SSHPorts, OwnerHmIPAP: HmIPAPPorts, OwnerDiscovery: DiscoveryPorts, OwnerACME: ACMEPorts}
	for _, x := range lists[owner] {
		if activeKey(owner, x) == activeKey(owner, p) {
			return x, true
		}
	}
	if owner == OwnerHmIPAP && p.Port == 43439 && p.Proto == ProtoUDP && p.SPort == 0 && p.Dest == "" {
		return PortSpec{Port: 43439, Proto: ProtoUDP, Comment: "device discovery"}, true // until B-221
	}
	return PortSpec{}, false
}

// ActiveOwners is the owners the configuration's rules were last made for, with their ports.
func (c Config) ActiveOwners() map[string][]PortSpec {
	out := map[string][]PortSpec{}
	for _, k := range c.Active {
		if o, p, ok := splitKey(k); ok {
			out[o] = append(out[o], p)
		}
	}
	return out
}

// PortVerdict is what the confirmed rules do with one IPv4 port from any source (openccu-lite
// task 217: the access point list shows it for hmipserver's ports): the first enabled rule for the
// port - iptables' first match - or the chain's policy when no rule names it.
type PortVerdict struct {
	Port   int    `json:"port"`
	Proto  string `json:"proto"`
	Target string `json:"target"` // ACCEPT, DROP or REJECT
	// Rule is the deciding rule's id; empty when the policy decides.
	Rule   string `json:"rule,omitempty"`
	Source string `json:"source,omitempty"`
	Owner  string `json:"owner,omitempty"`
	// Sources is from where the port is accepted when Target is ACCEPT (B-134): the sources of
	// every enabled ACCEPT rule for the port, in the rules' order and each once. The deciding
	// rule's Source is the first of them. A converted OpenCCU firewall has one rule per allowed
	// address, and naming the first alone made the access look narrower than it is.
	Sources []string `json:"sources,omitempty"`
}

// Verdict finds the rule that decides the port for IPv4. A rule narrowed by a source port or a
// destination (the discovery replies, multicast) does not decide a device's unicast packets, so
// it is passed over; a rule for a source does, since an access point is one address among them.
//
// An accepted port also names every source it is accepted from (Sources): the ACCEPT rules after
// the deciding one count as well, up to the first rule for the port from anywhere - nothing behind
// that one is reached. A DROP or REJECT for a single source between them takes that source out of
// a wider rule behind it, which a list of sources cannot say; it is passed over.
func Verdict(c Config, proto string, port int) PortVerdict {
	v := PortVerdict{Port: port, Proto: proto, Target: c.Policy.V4}
	decided := false
	for _, r := range c.Rules {
		if r.Disabled || r.Proto != proto || r.SPort != 0 || r.Dest != "" || !r.Covers(FamilyV4) {
			continue
		}
		if r.Port != 0 {
			to := r.Port
			if r.PortTo > to {
				to = r.PortTo
			}
			if port < r.Port || port > to {
				continue
			}
		}
		if !decided {
			v.Target, v.Rule, v.Source, v.Owner = r.Target, r.ID, r.Source, r.Owner
			decided = true
			if r.Target != Accept {
				return v
			}
		}
		if r.Target == Accept && !slices.Contains(v.Sources, r.Source) {
			v.Sources = append(v.Sources, r.Source)
		}
		if r.Source == AnySource {
			return v
		}
	}
	return v
}

// HmIPAPVerdicts is Verdict for the ports an HmIP access point connects to: hmipserver's update
// servers 9293 and 9294 and its access point port udp 43438 (not the discovery on 43439, which
// is the apps' as well, D-110).
func HmIPAPVerdicts(c Config) []PortVerdict {
	out := []PortVerdict{}
	for _, p := range HmIPAPPorts {
		out = append(out, Verdict(c, p.Proto, p.Port))
	}
	return out
}

// AdoptOwned replaces an owner's rules by rules made elsewhere and marks specs as the ports the
// owner's rules were made for: the import of a CCU backup's classic RPC (occulited task 142) puts
// the converted XMLRPC rules, with their old sources, in the place of the owner's default ones, as
// the firmware update's conversion does (Migrate). A port among specs that rules does not cover
// gets the owner's default rule. Reconcile then leaves the owner alone while it wants these
// ports, and drops what it does not want (hs485d's, where that does not run).
func AdoptOwned(c Config, owner string, specs []PortSpec, rules []Rule) Config {
	out := c.Clone()
	out.Rules = slices.DeleteFunc(out.Rules, func(r Rule) bool { return r.Owner == owner })
	covered := map[string]bool{}
	for _, r := range rules {
		if r.Owner != owner {
			continue
		}
		covered[RuleKey(r)] = true
		out.Rules = append(out.Rules, r)
	}
	active := map[string]bool{}
	for _, k := range out.Active {
		if o, _, ok := splitKey(k); ok && o == owner {
			continue
		}
		active[k] = true
	}
	for _, p := range specs {
		k := activeKey(owner, p)
		active[k] = true
		if !covered[k] {
			out.Rules = append(out.Rules, OwnedRule(owner, p))
		}
	}
	out.Active = out.Active[:0]
	for k := range active {
		out.Active = append(out.Active, k)
	}
	sort.Strings(out.Active)
	return out
}
