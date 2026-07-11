package consoleplayers

import (
	"context"
	"regexp"
	"strings"
	"time"

	"emperror.dev/errors"
)

const (
	statusCommand    = "status"
	waitAfterCommand = 800 * time.Millisecond
	logTailLines     = 200
)

// Player represents a connected player parsed from the GoldSrc `status` output.
type Player struct {
	UserId  int    `json:"userid"`
	Slot    int    `json:"slot"`
	Name    string `json:"name"`
	SteamId string `json:"steamid"`
	Score   int    `json:"score"`
	Ping    int    `json:"ping"`
	Loss    int    `json:"loss"`
	State   string `json:"state"`
	Address string `json:"address,omitempty"`
}

// Result is returned to the Panel after querying players via console logs.
type Result struct {
	Players   []Player `json:"players"`
	Map       *string  `json:"map,omitempty"`
	Hostname  *string  `json:"hostname,omitempty"`
	QueriedAt string   `json:"queried_at"`
	Source    string   `json:"source"`
}

// Environment exposes the server operations needed to fetch players from console output.
type Environment interface {
	IsRunning(ctx context.Context) (bool, error)
	SendCommand(command string) error
	Readlog(lines int) ([]string, error)
}

// Fetch sends `status` to the server console and parses the latest log lines.
func Fetch(ctx context.Context, env Environment) (*Result, error) {
	running, err := env.IsRunning(ctx)
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, errors.New("server is not running")
	}

	if err := env.SendCommand(statusCommand); err != nil {
		return nil, errors.Wrap(err, "failed to send status command")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(waitAfterCommand):
	}

	raw, err := env.Readlog(logTailLines)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read server logs")
	}

	return parseStatusOutput(raw), nil
}

var (
	rehldsPlayerLine = regexp.MustCompile(`^#\s*(\d+)\s+"([^"]*)"\s+(\d+)\s+(\S+)\s+(\d+)\s+(\S+)\s+(\d+)\s+(\d+)(?:\s+([\d.]+:\d+))?`)
	classicPlayerLine = regexp.MustCompile(`^#\s*(\d+)\s+"([^"]*)"\s+(\d+)\s+(\S+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(\S+)(?:\s+([\d.]+:\d+))?`)
	quotedSteamLine   = regexp.MustCompile(`^#\s*(\d+)\s+"([^"]*)"\s+"([^"]*)"\s+(\S+)\s+(\d+)\s+(\d+)\s+(\S+)(?:\s+([\d.]+:\d+))?`)
	unquotedNameLine  = regexp.MustCompile(`^#\s*(\d+)\s+(\S+)\s+(\d+)\s+(\S+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(\S+)(?:\s+([\d.]+:\d+))?`)
	connectionTime    = regexp.MustCompile(`\s\d{1,2}:\d{2}(?::\d{2})?\s`)
	statusHeaderLine  = regexp.MustCompile(`(?i)name.*userid.*uniqueid`)
	usersFooterLine   = regexp.MustCompile(`^\d+\s+users?$`)
)

func parseStatusOutput(lines []string) *Result {
	result := &Result{
		Players:   []Player{},
		QueriedAt: time.Now().UTC().Format(time.RFC3339),
		Source:    "console_status",
	}

	cleaned := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := cleanDockerLogLine(raw)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}

	for _, line := range cleaned {
		if result.Hostname == nil {
			if hostname := parseHostname(line); hostname != "" {
				result.Hostname = &hostname
			}
		}

		if result.Map == nil {
			if mapName := parseMap(line); mapName != "" {
				result.Map = &mapName
			}
		}
	}

	start := findStatusBlockStart(cleaned)
	if start >= 0 {
		parsePlayerLines(result, cleaned[start:])
		return result
	}

	parsePlayerLines(result, cleaned)
	return result
}

func parsePlayerLines(result *Result, lines []string) {
	parsingPlayers := false

	for _, line := range lines {
		if statusHeaderLine.MatchString(line) {
			parsingPlayers = true
			continue
		}

		if !parsingPlayers && !strings.HasPrefix(line, "#") {
			continue
		}

		if usersFooterLine.MatchString(strings.TrimSpace(line)) {
			break
		}

		if player, ok := parsePlayerLine(line); ok {
			parsingPlayers = true
			result.Players = append(result.Players, player)
		}
	}
}

func findStatusBlockStart(lines []string) int {
	lastHeader := -1
	for i, line := range lines {
		if statusHeaderLine.MatchString(line) {
			lastHeader = i
		}
	}
	return lastHeader
}

func cleanDockerLogLine(line string) string {
	if len(line) > 8 && (line[0] == 1 || line[0] == 2) {
		line = line[8:]
	}

	for _, marker := range []string{"hostname:", "version :", "map     :", "players :", "# "} {
		if idx := strings.Index(line, marker); idx > 0 {
			prefix := line[:idx]
			if strings.TrimFunc(prefix, func(r rune) bool { return r < 32 || r == 127 }) == "" {
				line = line[idx:]
				break
			}
		}
	}

	if idx := strings.Index(line, "#"); idx > 0 {
		prefix := line[:idx]
		if strings.TrimFunc(prefix, func(r rune) bool { return r < 32 || r == 127 }) == "" {
			line = line[idx:]
		}
	}

	return strings.TrimSpace(line)
}

func parseHostname(line string) string {
	if !strings.HasPrefix(line, "hostname:") {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(line, "hostname:"))
}

func parseMap(line string) string {
	if !strings.HasPrefix(line, "map") {
		return ""
	}

	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return ""
	}

	mapPart := strings.TrimSpace(parts[1])
	if mapPart == "" {
		return ""
	}

	if idx := strings.Index(mapPart, " at:"); idx != -1 {
		mapPart = strings.TrimSpace(mapPart[:idx])
	}

	return mapPart
}

func parsePlayerLine(line string) (Player, bool) {
	if !strings.HasPrefix(line, "#") {
		return Player{}, false
	}

	if statusHeaderLine.MatchString(line) {
		return Player{}, false
	}

	if matches := quotedSteamLine.FindStringSubmatch(line); len(matches) > 0 {
		return buildPlayer(matches[1], matches[2], matches[1], matches[3], "0", matches[5], matches[6], matches[7], matches[8]), true
	}

	if connectionTime.MatchString(line) {
		if matches := rehldsPlayerLine.FindStringSubmatch(line); len(matches) > 0 {
			return buildPlayer(matches[1], matches[2], matches[3], matches[4], matches[5], matches[7], matches[8], "active", matches[9]), true
		}
	}

	if matches := classicPlayerLine.FindStringSubmatch(line); len(matches) > 0 {
		return buildPlayer(matches[1], matches[2], matches[3], matches[4], matches[5], matches[6], matches[7], matches[8], matches[9]), true
	}

	if matches := unquotedNameLine.FindStringSubmatch(line); len(matches) > 0 {
		return buildPlayer(matches[1], matches[2], matches[3], matches[4], matches[5], matches[6], matches[7], matches[8], matches[9]), true
	}

	return Player{}, false
}

func buildPlayer(slot, name, userId, steamId, score, ping, loss, state, address string) Player {
	return Player{
		Slot:    atoi(slot),
		Name:    name,
		UserId:  atoi(userId),
		SteamId: steamId,
		Score:   atoi(score),
		Ping:    atoi(ping),
		Loss:    atoi(loss),
		State:   state,
		Address: address,
	}
}

func atoi(value string) int {
	var out int
	for _, r := range value {
		if r < '0' || r > '9' {
			break
		}
		out = out*10 + int(r-'0')
	}
	return out
}
