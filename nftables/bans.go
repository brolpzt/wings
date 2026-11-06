package nftables

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// IP bans live in the same "inet ptero" table as the port rules, in their own
// chain hooked at prerouting with raw priority: it runs before Docker's DNAT and
// before connection tracking, so a ban also cuts connections already open.
// Banned addresses are set elements, so adding or removing one is atomic and
// takes effect immediately.
const (
	bansChain   = "ptero_bans"
	bansSet4    = "ptero_bans4"     // source . destination . port
	bansSet4Any = "ptero_bans4_any" // source . port (allocation bound to 0.0.0.0)
	bansSet6    = "ptero_bans6"
	bansSet6Any = "ptero_bans6_any"
)

// BanTarget is one address and port of a server that a banned IP must not reach.
type BanTarget struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// banScriptHeader creates the sets and chain when missing and rewrites the
// chain's rules in the same transaction (sets and their elements are kept).
func banScriptHeader() string {
	t := tableFamily + " " + tableName
	var b strings.Builder
	fmt.Fprintf(&b, "add table %s\n", t)
	fmt.Fprintf(&b, "add set %s %s { type ipv4_addr . ipv4_addr . inet_service; }\n", t, bansSet4)
	fmt.Fprintf(&b, "add set %s %s { type ipv4_addr . inet_service; }\n", t, bansSet4Any)
	fmt.Fprintf(&b, "add set %s %s { type ipv6_addr . ipv6_addr . inet_service; }\n", t, bansSet6)
	fmt.Fprintf(&b, "add set %s %s { type ipv6_addr . inet_service; }\n", t, bansSet6Any)
	fmt.Fprintf(&b, "add chain %s %s { type filter hook prerouting priority raw; policy accept; }\n", t, bansChain)
	fmt.Fprintf(&b, "flush chain %s %s\n", t, bansChain)
	fmt.Fprintf(&b, "add rule %s %s meta l4proto { tcp, udp } ip saddr . ip daddr . th dport @%s counter drop\n", t, bansChain, bansSet4)
	fmt.Fprintf(&b, "add rule %s %s meta l4proto { tcp, udp } ip saddr . th dport @%s counter drop\n", t, bansChain, bansSet4Any)
	fmt.Fprintf(&b, "add rule %s %s meta l4proto { tcp, udp } ip6 saddr . ip6 daddr . th dport @%s counter drop\n", t, bansChain, bansSet6)
	fmt.Fprintf(&b, "add rule %s %s meta l4proto { tcp, udp } ip6 saddr . th dport @%s counter drop\n", t, bansChain, bansSet6Any)
	return b.String()
}

// banElement returns the set and element that block source from reaching target.
// ok is false when they cannot match (IPv4 client and IPv6 server address or vice versa).
func banElement(source net.IP, target BanTarget) (set string, element string, ok bool, err error) {
	dst := net.ParseIP(strings.TrimSpace(target.IP))
	if dst == nil {
		return "", "", false, fmt.Errorf("nftables: invalid server address %q", target.IP)
	}
	if target.Port < 1 || target.Port > 65535 {
		return "", "", false, fmt.Errorf("nftables: invalid port %d", target.Port)
	}

	src4, dst4 := source.To4(), dst.To4()
	switch {
	case src4 != nil && dst.IsUnspecified():
		return bansSet4Any, fmt.Sprintf("%s . %d", src4, target.Port), true, nil
	case src4 == nil && dst.IsUnspecified():
		return bansSet6Any, fmt.Sprintf("%s . %d", source, target.Port), true, nil
	case src4 != nil && dst4 != nil:
		return bansSet4, fmt.Sprintf("%s . %s . %d", src4, dst4, target.Port), true, nil
	case src4 == nil && dst4 == nil:
		return bansSet6, fmt.Sprintf("%s . %s . %d", source, dst, target.Port), true, nil
	}
	return "", "", false, nil
}

// ApplyBans blocks (add) or unblocks (remove) ip on every target, in one atomic
// transaction. It returns how many targets the address family could match.
func ApplyBans(ip string, targets []BanTarget, add bool) (int, error) {
	source := net.ParseIP(strings.TrimSpace(ip))
	if source == nil {
		return 0, fmt.Errorf("nftables: invalid IP address %q", ip)
	}

	var b strings.Builder
	b.WriteString(banScriptHeader())
	matched := 0
	for _, target := range targets {
		set, element, ok, err := banElement(source, target)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		matched++
		t := tableFamily + " " + tableName
		// "add" never fails on an existing element, so add-then-delete removes
		// it whether or not it is currently present.
		fmt.Fprintf(&b, "add element %s %s { %s }\n", t, set, element)
		if !add {
			fmt.Fprintf(&b, "delete element %s %s { %s }\n", t, set, element)
		}
	}

	if err := runNftScript(b.String()); err != nil {
		return 0, err
	}
	return matched, nil
}

// EnsureBans creates the ban chain and sets (used at boot before restoring bans).
func EnsureBans() error {
	return runNftScript(banScriptHeader())
}

func runNftScript(script string) error {
	cmd := exec.Command(nftBinary(), "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("nftables: %s", msg)
	}
	return nil
}
