package gamequery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The Isle Evrima (GameDig type "tie") uses Epic Online Services matchmaking,
// not Source/A2S. Credentials match node-gamedig's theisleevrima protocol
// (extracted from the game client / published in gamedig).
const (
	tieClientID     = "xyza7891gk5PRo3J7G9puCJGFJjmEguW"
	tieClientSecret = "pKWl6t5i9NJK8gTpVlAxzENZ65P8hYzodV8Dqe5Rlc8"
	tieDeploymentID = "6db6bea492f94b1bbdfcdfe3e4f898dc"
	tieEpicAPI      = "https://api.epicgames.dev"
)

type tieTokenResponse struct {
	AccessToken string `json:"access_token"`
}

type tieFilterResponse struct {
	Sessions []tieSession `json:"sessions"`
}

type tieSession struct {
	TotalPlayers  int                    `json:"totalPlayers"`
	PublicPlayers []tiePlayer            `json:"publicPlayers"`
	Settings      tieSessionSettings     `json:"settings"`
	Attributes    map[string]interface{} `json:"attributes"`
}

type tieSessionSettings struct {
	MaxPublicPlayers int `json:"maxPublicPlayers"`
}

type tiePlayer struct {
	Name string `json:"name"`
}

func isTieGame(gameType string) bool {
	switch gameType {
	case "tie", "theisleevrima", "the-isle-evrima", "evrima":
		return true
	default:
		return false
	}
}

func queryTie(ctx context.Context, result *Result, host string, port int) (*Result, error) {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "127.0.0.1" {
		// EOS indexes by public ADDRESS_s; loopback cannot match a session.
		return result, nil
	}

	client := &http.Client{Timeout: defaultTimeout}
	token, err := tieAccessToken(ctx, client)
	if err != nil {
		return result, nil
	}

	session, err := tieFindSession(ctx, client, token, host, port)
	if err != nil || session == nil {
		return result, nil
	}

	attrs := session.Attributes
	result.Online = true
	result.Hostname = stringPtr(tieAttrString(attrs, "SERVERNAME_s"))
	result.Map = stringPtr(tieAttrString(attrs, "MAP_NAME_s"))
	result.Game = stringPtr("The Isle Evrima")
	result.Version = stringPtr(tieAttrString(attrs, "SERVER_VERSION_s"))
	result.Players = session.TotalPlayers
	result.MaxPlayers = session.Settings.MaxPublicPlayers
	result.PasswordProtected = tieAttrBool(attrs, "PASSWORD_ENABLED_b")
	result.QueryPort = port

	for _, player := range session.PublicPlayers {
		name := strings.TrimSpace(player.Name)
		if name == "" {
			continue
		}
		result.PlayerList = append(result.PlayerList, Player{Name: name})
	}

	return result, nil
}

func tieAccessToken(ctx context.Context, client *http.Client) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("deployment_id", tieDeploymentID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tieEpicAPI+"/auth/v1/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(tieClientID, tieClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("eos auth status %d", res.StatusCode)
	}

	var parsed tieTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("eos auth missing access_token")
	}

	return parsed.AccessToken, nil
}

func tieFindSession(ctx context.Context, client *http.Client, token, host string, port int) (*tieSession, error) {
	payload := map[string]interface{}{
		"criteria": []map[string]interface{}{
			{
				"key":   "attributes.ADDRESS_s",
				"op":    "EQUAL",
				"value": host,
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/matchmaking/v1/%s/filter", tieEpicAPI, tieDeploymentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("eos filter status %d", res.StatusCode)
	}

	var parsed tieFilterResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	portStr := strconv.Itoa(port)
	wantBoundLocal := "0.0.0.0:" + portStr
	wantBoundHost := host + ":" + portStr

	for i := range parsed.Sessions {
		session := &parsed.Sessions[i]
		bound := tieAttrString(session.Attributes, "ADDRESSBOUND_s")
		if bound == wantBoundLocal || bound == wantBoundHost {
			return session, nil
		}
		if tieAttrInt(session.Attributes, "GAMESERVER_PORT_l") == port {
			return session, nil
		}
	}

	return nil, nil
}

func tieAttrString(attrs map[string]interface{}, key string) string {
	if attrs == nil {
		return ""
	}
	v, ok := attrs[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func tieAttrBool(attrs map[string]interface{}, key string) bool {
	if attrs == nil {
		return false
	}
	v, ok := attrs[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true") || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}

func tieAttrInt(attrs map[string]interface{}, key string) int {
	if attrs == nil {
		return 0
	}
	v, ok := attrs[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	default:
		return 0
	}
}
