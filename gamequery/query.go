package gamequery

import (
	"context"
	"strings"
	"time"

	"emperror.dev/errors"
	gomcping "github.com/cubexteam/gomc-ping"
	"github.com/cubexteam/gomc-ping/models"
	"github.com/warsmite/gjq"
)

const defaultTimeout = 5 * time.Second

// Request contains the parameters needed to query a game server.
type Request struct {
	GameType  string
	Host      string
	Port      int
	QueryPort int
}

// Player represents an online player entry.
type Player struct {
	Name  string `json:"name,omitempty"`
	Score int    `json:"score,omitempty"`
	Time  int    `json:"time,omitempty"`
	Ping  int    `json:"ping,omitempty"`
}

// Result is the normalized response returned to the Panel.
type Result struct {
	Online            bool     `json:"online"`
	Type              string   `json:"type"`
	Address           string   `json:"address"`
	Port              int      `json:"port"`
	QueryPort         int      `json:"query_port"`
	Hostname          *string  `json:"hostname"`
	Map               *string  `json:"map"`
	Game              *string  `json:"game"`
	Players           int      `json:"players"`
	MaxPlayers        int      `json:"max_players"`
	PasswordProtected bool     `json:"password_protected"`
	Version           *string  `json:"version"`
	PlayerList        []Player `json:"player_list"`
	QueriedAt         string   `json:"queried_at"`
}

// Query performs a UDP game server query from the Wings node.
func Query(ctx context.Context, req Request) (*Result, error) {
	gameType := normalizeGameType(req.GameType)
	if gameType == "" {
		return nil, errors.New("game type is required")
	}

	host := normalizeHost(req.Host)
	if host == "" {
		return nil, errors.New("query host is required")
	}

	port := req.Port
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid game port")
	}

	queryPort := req.QueryPort
	if queryPort < 1 || queryPort > 65535 {
		queryPort = port
	}

	result := &Result{
		Online:     false,
		Type:       req.GameType,
		Address:    host,
		Port:       port,
		QueryPort:    queryPort,
		PlayerList: []Player{},
		QueriedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	if isSampGame(gameType) {
		return querySamp(ctx, result, host, queryPort)
	}

	return queryGjq(ctx, result, gameType, host, port, queryPort)
}

func queryGjq(ctx context.Context, result *Result, gameType, host string, port, queryPort int) (*Result, error) {
	opts := gjq.QueryOptions{
		Timeout: defaultTimeout,
		Players: true,
	}

	if game := resolveGjqGame(gameType); game != "" {
		opts.Game = game
	} else if protocol := protocolFallback(gameType); protocol != "" {
		opts.Protocol = protocol
		opts.Direct = true
	} else {
		return nil, errors.Errorf("unsupported game type %q", gameType)
	}

	queryPortU16 := uint16(queryPort)
	if queryPort != port {
		opts.Direct = true
	}

	info, err := gjq.Query(ctx, host, queryPortU16, opts)
	if err != nil {
		return result, nil
	}

	result.Online = true
	result.Hostname = stringPtr(info.Name)
	result.Map = stringPtr(info.Map)
	result.Game = stringPtr(info.Game)
	result.Players = normalizePlayerCount(info)
	result.MaxPlayers = info.MaxPlayers
	result.PasswordProtected = strings.EqualFold(info.Visibility, "private")
	result.Version = stringPtr(info.Version)
	result.QueryPort = int(info.QueryPort)

	for _, player := range info.PlayerList {
		result.PlayerList = append(result.PlayerList, Player{
			Name:  player.Name,
			Score: player.Score,
			Time:  int(player.Duration.Seconds()),
		})
	}

	return result, nil
}

func querySamp(ctx context.Context, result *Result, host string, queryPort int) (*Result, error) {
	queryCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	type queryResponse struct {
		resp *models.Response
		err  error
	}

	ch := make(chan queryResponse, 1)
	go func() {
		resp, err := gomcping.PingSAMP(host, uint16(queryPort))
		ch <- queryResponse{resp: resp, err: err}
	}()

	select {
	case <-queryCtx.Done():
		return result, nil
	case out := <-ch:
		if out.err != nil || out.resp == nil || !out.resp.Online {
			return result, nil
		}

		result.Online = true
		result.Hostname = stringPtr(out.resp.MOTD)
		result.Map = stringPtr(out.resp.Map)
		result.Game = stringPtr("SA-MP")
		result.Players = out.resp.PlayersOn
		result.MaxPlayers = out.resp.PlayersMax
		result.PasswordProtected = out.resp.Password
		result.Version = stringPtr(out.resp.Version)

		for _, player := range out.resp.Sample {
			result.PlayerList = append(result.PlayerList, Player{
				Name: player.Name,
			})
		}

		return result, nil
	}
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0":
		return "127.0.0.1"
	default:
		return host
	}
}

func normalizeGameType(gameType string) string {
	return strings.ToLower(strings.TrimSpace(gameType))
}

func isSampGame(gameType string) bool {
	switch gameType {
	case "samp", "gtasa":
		return true
	default:
		return false
	}
}

func resolveGjqGame(gameType string) string {
	if mapped, ok := gameTypeOverrides[gameType]; ok {
		gameType = mapped
	}

	if g := gjq.Registry.Get(gameType); g != nil && g.HasQuery() {
		return gameType
	}

	return ""
}

func protocolFallback(gameType string) string {
	if protocol, ok := protocolFallbacks[gameType]; ok {
		return protocol
	}

	return ""
}

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	return &value
}

// normalizePlayerCount prefers the protocol player count, but falls back to the
// parsed player list. CoD2 and some Quake3-based servers omit the "clients" key
// in getstatus while still returning player lines.
func normalizePlayerCount(info *gjq.ServerInfo) int {
	if info == nil {
		return 0
	}

	if info.Players > 0 {
		return info.Players
	}

	if len(info.PlayerList) == 0 {
		return 0
	}

	count := 0
	for _, player := range info.PlayerList {
		if strings.TrimSpace(player.Name) != "" {
			count++
		}
	}

	if count > 0 {
		return count
	}

	return len(info.PlayerList)
}
