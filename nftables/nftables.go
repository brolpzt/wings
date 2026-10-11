package nftables

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/apex/log"
)

const (
	tableFamily = "inet"
	tableName   = "ptero"
	chainName   = "ptero_ports"
)

// Rule is a single ACCEPT entry managed by the Panel sync.
type Rule struct {
	IP    string `json:"ip"`
	Port  int    `json:"port"`
	Proto string `json:"proto"` // tcp | udp
}

// Sync replaces the entire ptero_ports chain with the desired ACCEPT rules.
// The Panel owns this chain: anything not in rules is removed (flush + rewrite).
func Sync(rules []Rule) error {
	if err := ensureChain(); err != nil {
		return err
	}

	normalized, err := normalizeRules(rules)
	if err != nil {
		return err
	}

	if err := runNft("flush", "chain", tableFamily, tableName, chainName); err != nil {
		return fmt.Errorf("nftables: flush chain: %w", err)
	}

	for _, rule := range normalized {
		comment := fmt.Sprintf("ptero:%s:%d:%s", rule.IP, rule.Port, rule.Proto)
		args := []string{
			"add", "rule", tableFamily, tableName, chainName,
			"ip", "daddr", rule.IP,
			rule.Proto, "dport", strconv.Itoa(rule.Port),
			"accept",
			"comment", comment,
		}
		if err := runNft(args...); err != nil {
			return fmt.Errorf("nftables: add rule %s:%d/%s: %w", rule.IP, rule.Port, rule.Proto, err)
		}
	}

	log.WithField("rules", len(normalized)).Info("nftables ptero_ports chain synchronized")
	return nil
}

func ensureChain() error {
	// Create table if missing (ignore "File exists").
	if err := runNft("add", "table", tableFamily, tableName); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("nftables: add table: %w", err)
	}

	// Create chain with input hook; policy accept so SSH/Wings stay reachable.
	chainSpec := "{ type filter hook input priority -10; policy accept; }"
	if err := runNft("add", "chain", tableFamily, tableName, chainName, chainSpec); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("nftables: add chain: %w", err)
	}

	return nil
}

func normalizeRules(rules []Rule) ([]Rule, error) {
	out := make([]Rule, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))

	for _, rule := range rules {
		ip := strings.TrimSpace(rule.IP)
		proto := strings.ToLower(strings.TrimSpace(rule.Proto))
		port := rule.Port

		if net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
			return nil, fmt.Errorf("nftables: invalid IPv4 address %q", rule.IP)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("nftables: invalid port %d", port)
		}
		if proto != "tcp" && proto != "udp" {
			return nil, fmt.Errorf("nftables: invalid proto %q (want tcp|udp)", rule.Proto)
		}

		key := fmt.Sprintf("%s:%d:%s", ip, port, proto)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Rule{IP: ip, Port: port, Proto: proto})
	}

	return out, nil
}

func nftBinary() string {
	if path, err := exec.LookPath("nft"); err == nil {
		return path
	}
	for _, candidate := range []string{"/usr/sbin/nft", "/sbin/nft", "/usr/bin/nft"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "nft"
}

func runNft(args ...string) error {
	cmd := exec.Command(nftBinary(), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "file exists") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "exists")
}
