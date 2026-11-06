package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/apex/log"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/nftables"
)

// FirewallRule represents a single IP ban tied to a specific server allocation.
type FirewallRule struct {
	IP         string `json:"ip"`
	Reason     string `json:"reason"`
	ServerIP   string `json:"server_ip"`
	ServerPort int    `json:"server_port"`
}

// firewallBan is a banned IP and the server addresses it was blocked on, kept
// so the exact same set elements are removed later and restored after a reboot.
type firewallBan struct {
	IP      string               `json:"ip"`
	Targets []nftables.BanTarget `json:"targets"`
}

var firewallMu sync.Mutex

func firewallStatePath() string {
	return filepath.Join(config.Get().System.RootDirectory, "firewall_bans.json")
}

// loadFirewallState returns the bans per server UUID.
func loadFirewallState() map[string][]firewallBan {
	state := map[string][]firewallBan{}
	data, err := os.ReadFile(firewallStatePath())
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		log.WithField("error", err).Warn("firewall: ignoring unreadable ban state file")
		return map[string][]firewallBan{}
	}
	return state
}

func saveFirewallState(state map[string][]firewallBan) error {
	for id, bans := range state {
		if len(bans) == 0 {
			delete(state, id)
		}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := firewallStatePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, firewallStatePath())
}

// firewallTargets lists every address and port of the server, plus the one the
// Panel sent, so a ban blocks the IP from all of the server's ports.
func (s *Server) firewallTargets(serverIP string, serverPort int) []nftables.BanTarget {
	seen := map[string]struct{}{}
	var targets []nftables.BanTarget
	add := func(ip string, port int) {
		ip = strings.TrimSpace(ip)
		if net.ParseIP(ip) == nil || port < 1 || port > 65535 {
			return
		}
		key := ip + ":" + strconv.Itoa(port)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		targets = append(targets, nftables.BanTarget{IP: ip, Port: port})
	}

	add(serverIP, serverPort)
	for ip, ports := range s.Config().Allocations.Mappings {
		for _, port := range ports {
			add(ip, port)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].IP != targets[j].IP {
			return targets[i].IP < targets[j].IP
		}
		return targets[i].Port < targets[j].Port
	})
	return targets
}

// AddFirewallRule blocks ip from reaching any of this server's ports (TCP and
// UDP) right away, including connections that are already open.
func (s *Server) AddFirewallRule(ip, serverIP string, serverPort int) error {
	if net.ParseIP(strings.TrimSpace(ip)) == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}
	ip = strings.TrimSpace(ip)

	firewallMu.Lock()
	defer firewallMu.Unlock()

	targets := s.firewallTargets(serverIP, serverPort)
	matched, err := nftables.ApplyBans(ip, targets, true)
	if err != nil {
		return fmt.Errorf("failed to add firewall ban: %w", err)
	}
	if matched == 0 {
		return fmt.Errorf("IP %s cannot reach this server's addresses (different IP version)", ip)
	}

	state := loadFirewallState()
	bans := state[s.ID()]
	for i := range bans {
		if bans[i].IP == ip {
			bans = append(bans[:i], bans[i+1:]...)
			break
		}
	}
	state[s.ID()] = append(bans, firewallBan{IP: ip, Targets: targets})
	if err := saveFirewallState(state); err != nil {
		s.Log().WithField("error", err).Warn("firewall: ban applied but could not be saved; it will not survive a reboot")
	}

	s.Log().WithFields(log.Fields{"banned_ip": ip, "targets": len(targets)}).Info("firewall ban added via nftables")
	return nil
}

// RemoveFirewallRule lifts the ban on ip for this server.
func (s *Server) RemoveFirewallRule(ip string) error {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}

	firewallMu.Lock()
	defer firewallMu.Unlock()

	state := loadFirewallState()
	bans := state[s.ID()]
	targets := s.firewallTargets("", 0)
	for i := range bans {
		if bans[i].IP == ip {
			targets = append(targets, bans[i].Targets...)
			bans = append(bans[:i], bans[i+1:]...)
			break
		}
	}

	if _, err := nftables.ApplyBans(ip, targets, false); err != nil {
		return fmt.Errorf("failed to remove firewall ban for IP %s: %w", ip, err)
	}

	state[s.ID()] = bans
	if err := saveFirewallState(state); err != nil {
		s.Log().WithField("error", err).Warn("firewall: could not save ban state")
	}

	s.Log().WithField("banned_ip", ip).Info("firewall ban removed from nftables")
	return nil
}

// FlushFirewallRules removes every ban of this server (server deletion).
func (s *Server) FlushFirewallRules() {
	firewallMu.Lock()
	defer firewallMu.Unlock()

	state := loadFirewallState()
	for _, ban := range state[s.ID()] {
		if _, err := nftables.ApplyBans(ban.IP, ban.Targets, false); err != nil {
			s.Log().WithFields(log.Fields{"banned_ip": ban.IP, "error": err}).Warn("failed to remove firewall ban during flush")
		}
	}
	delete(state, s.ID())
	if err := saveFirewallState(state); err != nil {
		s.Log().WithField("error", err).Warn("firewall: could not save ban state")
	}

	removeLegacyIPTablesRules(fmt.Sprintf("ptero-%s-", s.ID()[:8]))
	s.Log().Info("flushed all firewall bans for server")
}

// RestoreFirewallBans re-applies the saved bans at boot (nftables state does not
// survive a reboot) and removes the iptables rules older versions created.
func RestoreFirewallBans() {
	firewallMu.Lock()
	defer firewallMu.Unlock()

	removeLegacyIPTablesRules("ptero-")

	state := loadFirewallState()
	if len(state) == 0 {
		return
	}
	if err := nftables.EnsureBans(); err != nil {
		log.WithField("error", err).Error("firewall: could not prepare nftables to restore bans")
		return
	}

	restored := 0
	for id, bans := range state {
		for _, ban := range bans {
			if _, err := nftables.ApplyBans(ban.IP, ban.Targets, true); err != nil {
				log.WithFields(log.Fields{"server": id, "banned_ip": ban.IP, "error": err}).Error("firewall: failed to restore ban")
				continue
			}
			restored++
		}
	}
	log.WithField("bans", restored).Info("firewall: restored IP bans")
}

// removeLegacyIPTablesRules deletes INPUT rules whose comment starts with
// prefix. Older versions banned IPs there, which never matched Docker traffic.
func removeLegacyIPTablesRules(prefix string) {
	if _, err := exec.LookPath("iptables"); err != nil {
		return
	}
	out, err := exec.Command("iptables", "-S", "INPUT").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "-A INPUT") || !strings.Contains(line, prefix) {
			continue
		}
		// "iptables -S" quotes the comment; the quotes are not part of the rule.
		args := strings.Fields(strings.Replace(line, "-A INPUT", "-D INPUT", 1))
		for i := range args {
			args[i] = strings.Trim(args[i], `"`)
		}
		if out, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
			log.WithFields(log.Fields{"rule": line, "error": strings.TrimSpace(string(out))}).Warn("firewall: failed to remove legacy iptables rule")
		} else {
			log.WithField("rule", line).Info("firewall: removed legacy iptables rule")
		}
	}
}
