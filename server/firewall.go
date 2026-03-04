package server

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/apex/log"
)

// FirewallRule represents a single IP ban tied to a specific server allocation.
type FirewallRule struct {
	IP         string `json:"ip"`
	Reason     string `json:"reason"`
	ServerIP   string `json:"server_ip"`
	ServerPort int    `json:"server_port"`
}

// iptablesComment returns the iptables comment used to uniquely identify rules
// belonging to this server UUID + IP combination so we can remove them cleanly.
func iptablesComment(serverUUID, ip string) string {
	return fmt.Sprintf("ptero-%s-%s", serverUUID[:8], sanitizeIP(ip))
}

// sanitizeIP replaces characters that are invalid in iptables comments (colons for IPv6).
func sanitizeIP(ip string) string {
	return strings.ReplaceAll(ip, ":", "-")
}

// AddFirewallRule inserts an iptables DROP rule that blocks the given IP from
// reaching this server's IP:port over TCP and UDP.
func (s *Server) AddFirewallRule(ip, serverIP string, serverPort int) error {
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}

	comment := iptablesComment(s.ID(), ip)

	// Block TCP
	if err := runIPTables("-I", "INPUT",
		"-s", ip,
		"-d", serverIP,
		"-p", "tcp",
		"--dport", fmt.Sprintf("%d", serverPort),
		"-m", "comment", "--comment", comment,
		"-j", "DROP",
	); err != nil {
		return fmt.Errorf("failed to add TCP iptables rule: %w", err)
	}

	// Block UDP
	if err := runIPTables("-I", "INPUT",
		"-s", ip,
		"-d", serverIP,
		"-p", "udp",
		"--dport", fmt.Sprintf("%d", serverPort),
		"-m", "comment", "--comment", comment,
		"-j", "DROP",
	); err != nil {
		// Attempt to rollback TCP rule
		_ = removeIPTablesByComment(comment)
		return fmt.Errorf("failed to add UDP iptables rule: %w", err)
	}

	s.Log().WithFields(log.Fields{
		"banned_ip":   ip,
		"server_ip":   serverIP,
		"server_port": serverPort,
	}).Info("firewall rule added via iptables")

	return nil
}

// RemoveFirewallRule deletes the iptables rules associated with this IP ban.
func (s *Server) RemoveFirewallRule(ip string) error {
	comment := iptablesComment(s.ID(), ip)
	if err := removeIPTablesByComment(comment); err != nil {
		return fmt.Errorf("failed to remove iptables rule for IP %s: %w", ip, err)
	}

	s.Log().WithField("banned_ip", ip).Info("firewall rule removed from iptables")
	return nil
}

// FlushFirewallRules removes all iptables rules that belong to this server.
// This is called when a server is deleted to clean up all firewall entries.
func (s *Server) FlushFirewallRules() {
	prefix := fmt.Sprintf("ptero-%s-", s.ID()[:8])

	// List all rules with their line numbers and comments
	out, err := exec.Command("iptables", "-L", "INPUT", "-n", "--line-numbers").Output()
	if err != nil {
		s.Log().WithField("error", err).Warn("failed to list iptables rules during firewall flush")
		return
	}

	lines := strings.Split(string(out), "\n")
	var lineNums []string
	for _, line := range lines {
		if strings.Contains(line, prefix) {
			// Extract line number (first field)
			fields := strings.Fields(line)
			if len(fields) > 0 {
				lineNums = append(lineNums, fields[0])
			}
		}
	}

	// Delete in reverse order so line numbers remain valid
	for i := len(lineNums) - 1; i >= 0; i-- {
		if err := runIPTables("-D", "INPUT", lineNums[i]); err != nil {
			s.Log().WithFields(log.Fields{
				"line":  lineNums[i],
				"error": err,
			}).Warn("failed to delete iptables rule during flush")
		}
	}

	s.Log().Info("flushed all firewall rules for server from iptables")
}

// removeIPTablesByComment removes all INPUT rules that have the given comment tag.
// Uses iptables-save + grep + iptables -D approach to handle multiple matches.
func removeIPTablesByComment(comment string) error {
	// We use iptables-save to find matching rules, then delete each one.
	save, err := exec.Command("iptables-save").Output()
	if err != nil {
		return fmt.Errorf("iptables-save failed: %w", err)
	}

	var removed int
	for _, line := range strings.Split(string(save), "\n") {
		if strings.HasPrefix(line, "-A INPUT") && strings.Contains(line, comment) {
			// Convert -A INPUT ... to -D INPUT ...
			deleteLine := strings.Replace(line, "-A INPUT", "-D INPUT", 1)
			args := strings.Fields(deleteLine)
			if err := runIPTables(args...); err != nil {
				log.WithFields(log.Fields{
					"rule":  line,
					"error": err,
				}).Warn("failed to remove iptables rule by comment")
			} else {
				removed++
			}
		}
	}

	log.WithFields(log.Fields{
		"comment": comment,
		"removed": removed,
	}).Debug("removed iptables rules by comment")

	return nil
}

// runIPTables is a thin wrapper around exec.Command("iptables", args...).
func runIPTables(args ...string) error {
	cmd := exec.Command("iptables", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %s: %w (output: %s)", strings.Join(args, " "), err, string(out))
	}
	return nil
}
