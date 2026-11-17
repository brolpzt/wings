// Package xdp keeps the node's XDP game firewall (the game_firewall CLI) in line
// with the port map the Panel sends. The Panel owns the whole map: ports it does
// not send are removed, ports with a different profile are updated.
package xdp

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/apex/log"
)

// Profiles accepted by game_firewall.
var Profiles = map[string]struct{}{
	"source":  {},
	"cs16":    {},
	"pluto":   {},
	"cod2":    {},
	"ts3":     {},
	"generic": {},
}

// Change is one port whose profile differs between the node and the Panel.
type Change struct {
	Port int    `json:"port"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// Result describes what a sync did (or would do, on a dry run).
type Result struct {
	DryRun    bool     `json:"dry_run"`
	Added     []Change `json:"added"`
	Updated   []Change `json:"updated"`
	Removed   []Change `json:"removed"`
	Unchanged int      `json:"unchanged"`
}

// Runner executes game_firewall; swapped in tests.
type Runner func(args ...string) ([]byte, error)

var (
	mu     sync.Mutex
	runner Runner = runBinary
)

// Binary is the game_firewall executable.
func Binary() string {
	if path, err := exec.LookPath("game_firewall"); err == nil {
		return path
	}
	return "/usr/local/bin/game_firewall"
}

// Available reports whether game_firewall is installed on this node.
func Available() bool {
	_, err := os.Stat(Binary())
	return err == nil
}

func runBinary(args ...string) ([]byte, error) {
	cmd := exec.Command(Binary(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("game_firewall %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// List returns the ports the kernel map currently protects, with their profile.
func List() (map[int]string, error) {
	mu.Lock()
	defer mu.Unlock()
	return list()
}

func list() (map[int]string, error) {
	out, err := runner("list", "--json")
	if err != nil {
		return nil, err
	}
	raw := map[string]string{}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("game_firewall list: unreadable JSON: %w", err)
	}
	ports := make(map[int]string, len(raw))
	for key, profile := range raw {
		port, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil {
			continue
		}
		ports[port] = strings.ToLower(strings.TrimSpace(profile))
	}
	return ports, nil
}

// Normalize validates the desired map (port -> profile).
func Normalize(desired map[string]string) (map[int]string, error) {
	out := make(map[int]string, len(desired))
	for key, profile := range desired {
		port, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("xdp: invalid port %q", key)
		}
		profile = strings.ToLower(strings.TrimSpace(profile))
		if _, ok := Profiles[profile]; !ok {
			return nil, fmt.Errorf("xdp: invalid profile %q for port %d", profile, port)
		}
		out[port] = profile
	}
	return out, nil
}

// Diff compares the node's map with the desired one.
func Diff(current, desired map[int]string) Result {
	res := Result{Added: []Change{}, Updated: []Change{}, Removed: []Change{}}
	for port, profile := range desired {
		have, ok := current[port]
		switch {
		case !ok:
			res.Added = append(res.Added, Change{Port: port, To: profile})
		case have != profile:
			res.Updated = append(res.Updated, Change{Port: port, From: have, To: profile})
		default:
			res.Unchanged++
		}
	}
	for port, profile := range current {
		if _, ok := desired[port]; !ok {
			res.Removed = append(res.Removed, Change{Port: port, From: profile})
		}
	}
	for _, list := range [][]Change{res.Added, res.Updated, res.Removed} {
		sort.Slice(list, func(i, j int) bool { return list[i].Port < list[j].Port })
	}
	return res
}

// Sync makes the node's map equal to desired. On a dry run nothing is changed.
// Ports are set before stale ones are removed, so a port never sits unprotected
// while its profile changes.
func Sync(desired map[int]string, dryRun bool) (Result, error) {
	mu.Lock()
	defer mu.Unlock()

	current, err := list()
	if err != nil {
		return Result{}, err
	}
	res := Diff(current, desired)
	res.DryRun = dryRun
	if dryRun {
		return res, nil
	}

	byProfile := map[string][]int{}
	for _, c := range append(append([]Change{}, res.Added...), res.Updated...) {
		byProfile[c.To] = append(byProfile[c.To], c.Port)
	}
	profiles := make([]string, 0, len(byProfile))
	for profile := range byProfile {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	for _, profile := range profiles {
		for _, chunk := range chunks(byProfile[profile]) {
			if _, err := runner("set", chunk, profile); err != nil {
				return res, err
			}
		}
	}

	removed := make([]int, 0, len(res.Removed))
	for _, c := range res.Removed {
		removed = append(removed, c.Port)
	}
	for _, chunk := range chunks(removed) {
		if _, err := runner("del", chunk); err != nil {
			return res, err
		}
	}

	log.WithFields(log.Fields{
		"added":     len(res.Added),
		"updated":   len(res.Updated),
		"removed":   len(res.Removed),
		"unchanged": res.Unchanged,
	}).Info("xdp: game firewall port map synchronized")
	return res, nil
}

// chunks joins ports into comma-separated groups that keep each command short.
func chunks(ports []int) []string {
	sort.Ints(ports)
	var out []string
	for i := 0; i < len(ports); i += 100 {
		end := i + 100
		if end > len(ports) {
			end = len(ports)
		}
		parts := make([]string, 0, end-i)
		for _, p := range ports[i:end] {
			parts = append(parts, strconv.Itoa(p))
		}
		out = append(out, strings.Join(parts, ","))
	}
	return out
}
