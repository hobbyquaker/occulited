package firewall

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Chain is the user chain INPUT jumps to after the frame. A load rewrites INPUT and this chain and
// nothing else, so a chain an addon made survives it.
const Chain = "lite-input"

// localStatic are the ranges libfirewall's local-only chain allowed besides the interfaces' own
// global networks: loopback, the private ranges, link-local and CGNAT; for IPv6 loopback, unique
// local and link-local.
var (
	localStaticV4 = []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10"}
	localStaticV6 = []string{"::1/128", "fc00::/7", "fe80::/10"}
)

// Local is what LocalNetworks stands for, per family.
type Local struct {
	V4 []string `json:"ipv4"`
	V6 []string `json:"ipv6"`
}

// LocalFor is the static ranges plus every global network the interfaces carry (the address with
// its prefix, masked), each once, and none another entry already contains - an IPv6 privacy
// address arriving as a /128 beside its /64 (task 172).
func LocalFor(ifnets []*net.IPNet) Local {
	l := Local{V4: slices.Clone(localStaticV4), V6: slices.Clone(localStaticV6)}
	for _, n := range ifnets {
		if n == nil || !n.IP.IsGlobalUnicast() || n.IP.IsPrivate() {
			continue // loopback, link-local and the private ranges are in the static list already
		}
		masked := &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
		s := masked.String()
		if n.IP.To4() != nil {
			if !slices.Contains(l.V4, s) {
				l.V4 = append(l.V4, s)
			}
		} else if !slices.Contains(l.V6, s) {
			l.V6 = append(l.V6, s)
		}
	}
	l.V4, l.V6 = withoutContained(l.V4), withoutContained(l.V6)
	return l
}

// WithV6 is the local networks with a configuration's LocalV6 added (openccu-lite B-339): the
// automatic list first, then the user's networks, each once and none another entry contains.
func (l Local) WithV6(extra []string) Local {
	if len(extra) == 0 {
		return l
	}
	out := Local{V4: l.V4, V6: slices.Clone(l.V6)}
	for _, s := range extra {
		if !slices.Contains(out.V6, s) {
			out.V6 = append(out.V6, s)
		}
	}
	out.V6 = withoutContained(out.V6)
	return out
}

// withoutContained drops every network another one in the list contains; the order stays.
func withoutContained(nets []string) []string {
	parsed := make([]*net.IPNet, len(nets))
	for i, s := range nets {
		_, parsed[i], _ = net.ParseCIDR(s)
	}
	var out []string
	for i, n := range parsed {
		inside := false
		for j, m := range parsed {
			if i == j || n == nil || m == nil {
				continue
			}
			ni, _ := n.Mask.Size()
			mi, _ := m.Mask.Size()
			// m contains n: n's network lies in m, and m is wider (or equal and earlier in the list)
			if m.Contains(n.IP) && (mi < ni || (mi == ni && j < i)) {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, nets[i])
		}
	}
	return out
}

// Frame is what INPUT always allows before the user's rules, per family: loopback, replies to
// connections the box made, the ICMP a network needs, and the DHCP client's replies.
func Frame(family string) []string {
	common := []string{
		"-A INPUT -i lo -j ACCEPT",
		"-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
	}
	if family == FamilyV4 {
		return append(common,
			"-A INPUT -p icmp -m icmp --icmp-type 8 -j ACCEPT",
			"-A INPUT -p icmp -m icmp --icmp-type 3 -j ACCEPT",
			"-A INPUT -p icmp -m icmp --icmp-type 11 -j ACCEPT",
			"-A INPUT -p udp -m udp --sport 67 --dport 68 -j ACCEPT",
		)
	}
	out := common
	// neighbour and router discovery, MLD, echo request and the error types
	for _, t := range []int{1, 2, 3, 4, 128, 130, 131, 132, 133, 134, 135, 136, 143} {
		out = append(out, "-A INPUT -p ipv6-icmp -m icmp6 --icmpv6-type "+strconv.Itoa(t)+" -j ACCEPT")
	}
	return append(out, "-A INPUT -p udp -m udp --sport 547 --dport 546 -j ACCEPT")
}

// sources is what a rule's source becomes in one family: nothing (the rule is not for this
// family), one address or network, or every local network.
func sources(r Rule, family string, local Local) []string {
	if !r.Covers(family) {
		return nil
	}
	switch r.Source {
	case AnySource, "":
		return []string{""}
	case LocalNetworks:
		if family == FamilyV4 {
			return local.V4
		}
		return local.V6
	}
	if sf := SourceFamily(r.Source); sf != "" && sf != family {
		return nil
	}
	return []string{r.Source}
}

// match is a rule's match for one source: the protocol, the addresses, the ports.
func match(r Rule, src string) string {
	m := "-A " + Chain + " -p " + r.Proto
	if src != "" {
		m += " -s " + src
	}
	if r.Dest != "" {
		m += " -d " + r.Dest
	}
	if r.SPort != 0 || r.Port != 0 {
		m += " -m " + r.Proto
		if r.SPort != 0 {
			m += " --sport " + strconv.Itoa(r.SPort)
		}
		if r.Port != 0 {
			m += " --dport " + strconv.Itoa(r.Port)
			if r.PortTo != 0 {
				m += ":" + strconv.Itoa(r.PortTo)
			}
		}
	}
	return m
}

// Render is the iptables-restore text for one family (ipv4 or ipv6). It is loaded with --noflush:
// INPUT and the user chain are flushed and written, every other chain stays as it is.
func Render(c Config, family string, local Local) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	policy := c.Policy.V4
	if family == FamilyV6 {
		policy = c.Policy.V6
	}
	var b strings.Builder
	b.WriteString("*filter\n")
	fmt.Fprintf(&b, ":INPUT %s [0:0]\n", policy)
	fmt.Fprintf(&b, ":%s - [0:0]\n", Chain)
	b.WriteString("-F INPUT\n")
	fmt.Fprintf(&b, "-F %s\n", Chain)
	for _, l := range Frame(family) {
		b.WriteString(l + "\n")
	}
	fmt.Fprintf(&b, "-A INPUT -j %s\n", Chain)
	if c.Policy.Log {
		b.WriteString("-A INPUT -m limit --limit 10/min -j LOG --log-prefix \"fw policy \"\n")
	}
	for _, l := range Lines(c, family, local) {
		b.WriteString(l.Text + "\n")
	}
	b.WriteString("COMMIT\n")
	return b.String(), nil
}

// Line is one line of the user chain as a load writes it: the rule it comes from, and whether it
// is the LOG line in front of the rule. A Reset line belongs to no rule: it is the policy's TCP
// reset behind the rules (resetLines).
type Line struct {
	ID    string
	Log   bool
	Reset bool
	Text  string
}

// Lines are the user chain's lines for one family, in order - what Render writes after the frame,
// and what the counters are laid onto (task 167). For IPv6 the local networks are the automatic
// ones plus the configuration's LocalV6, and the policy's TCP resets come after the rules.
func Lines(c Config, family string, local Local) []Line {
	if family == FamilyV6 {
		local = local.WithV6(c.LocalV6)
	}
	var out []Line
	for _, r := range c.Rules {
		if r.Disabled {
			continue
		}
		if df := SourceFamily(r.Dest); df != "" && df != family {
			continue
		}
		for _, src := range sources(r, family, local) {
			match := match(r, src)
			if r.Log {
				out = append(out, Line{ID: r.ID, Log: true, Text: fmt.Sprintf("%s -m limit --limit 10/min -j LOG --log-prefix \"fw %s \"", match, r.ID)})
			}
			out = append(out, Line{ID: r.ID, Text: fmt.Sprintf("%s -j %s", match, r.Target)})
		}
	}
	if family == FamilyV6 {
		out = append(out, resetLines(c)...)
	}
	return out
}

// resetLines are the IPv6 chain's tail (openccu-lite B-339): a TCP SYN to a port that an enabled
// ACCEPT rule opens from the local networks, arriving from an address they do not cover, is
// answered with a TCP reset instead of falling to the policy's DROP - so a dual-stack client in
// another VLAN falls back to IPv4 at once instead of hanging until its timeout. IPv4's local
// networks cover every private VLAN, so IPv4 is left as it is. The lines come after every rule,
// so the rules' order decides as before: only what the policy would drop is reset. Nothing when
// the policy is ACCEPT (it would reach the port). With the policy logged, these are logged as the
// policy's too.
func resetLines(c Config) []Line {
	if c.Policy.V6 != Drop {
		return nil
	}
	var out []Line
	seen := map[string]bool{}
	for _, r := range c.Rules {
		if r.Disabled || r.Proto != ProtoTCP || r.Source != LocalNetworks || r.Target != Accept || r.SPort != 0 || !r.Covers(FamilyV6) {
			continue
		}
		if df := SourceFamily(r.Dest); df != "" && df != FamilyV6 {
			continue
		}
		m := "-A " + Chain + " -p tcp"
		if r.Dest != "" {
			m += " -d " + r.Dest
		}
		m += " -m tcp --syn"
		if r.Port != 0 {
			m += " --dport " + strconv.Itoa(r.Port)
			if r.PortTo != 0 {
				m += ":" + strconv.Itoa(r.PortTo)
			}
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		if c.Policy.Log {
			out = append(out, Line{Log: true, Reset: true, Text: m + " -m limit --limit 10/min -j LOG --log-prefix \"fw policy \""})
		}
		out = append(out, Line{Reset: true, Text: m + " -j REJECT --reject-with tcp-reset"})
	}
	return out
}

// Count is a rule's (or the policy's) packets and bytes.
type Count struct {
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

var (
	saveRuleRe   = regexp.MustCompile(`^\[(\d+):(\d+)\] -A ` + Chain + ` `)
	savePolicyRe = regexp.MustCompile(`^:INPUT (?:ACCEPT|DROP) \[(\d+):(\d+)\]`)
)

// Counts lays one family's `iptables-save -c` onto the lines that were loaded: the user chain's
// lines are in the same order, so the n-th counter is the n-th line's. A LOG line counts the same
// packets as its rule and is left out; a Reset line's packets are the policy's (B-339). ok is
// false when the table does not hold these lines (a load since, or someone else's change) - no
// numbers rather than wrong ones.
func Counts(save []byte, lines []Line) (rules map[string]Count, policy Count, ok bool) {
	rules = map[string]Count{}
	i := 0
	for _, l := range strings.Split(string(save), "\n") {
		if m := savePolicyRe.FindStringSubmatch(l); m != nil {
			policy.Packets, _ = strconv.ParseUint(m[1], 10, 64)
			policy.Bytes, _ = strconv.ParseUint(m[2], 10, 64)
			continue
		}
		m := saveRuleRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if i >= len(lines) {
			return nil, policy, false
		}
		if !lines[i].Log {
			p, _ := strconv.ParseUint(m[1], 10, 64)
			b, _ := strconv.ParseUint(m[2], 10, 64)
			if lines[i].Reset {
				// what the policy would have dropped (B-339): the policy's count
				policy.Packets += p
				policy.Bytes += b
			} else {
				c := rules[lines[i].ID]
				c.Packets += p
				c.Bytes += b
				rules[lines[i].ID] = c
			}
		}
		i++
	}
	if i != len(lines) {
		return nil, policy, false
	}
	return rules, policy, true
}

// Families are the two families a load writes, in order.
var Families = []string{FamilyV4, FamilyV6}
