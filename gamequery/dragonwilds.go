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

// RuneScape: Dragonwilds dedicated servers register with Epic Online Services
// (Redpoint EOS). There is no A2S/UDP query — status comes from Sessions filter.
// Credentials were extracted from DomConfEOS / DedicatedServerClient* in the
// LinuxServer pak (same values logged at EOSSDK Platform boot).
const (
	dragonwildsClientID     = "xyza7891CtSKDkoM4GKYmSVqMmI5Jkh2"
	dragonwildsClientSecret = "HdpSCYuVXQxrCewQOaGjlJlXCYkjthalEIGnZ6FO3yE"
	dragonwildsDeploymentID = "dd10a15a04d945e2950e1e106884e17a"
	dragonwildsEpicAPI      = "https://api.epicgames.dev"

	// Empty world-password sentinel observed when no WorldPassword is set.
	dragonwildsEmptyPasswordHash uint64 = 18446744073709551614
)

func isDragonwildsGame(gameType string) bool {
	switch gameType {
	case "dragonwilds", "rsdragonwilds", "runescape-dragonwilds", "rs-dragonwilds":
		return true
	default:
		return false
	}
}

func queryDragonwilds(ctx context.Context, result *Result, host string, port int) (*Result, error) {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "127.0.0.1" {
		// EOS indexes by public ADDRESS_s; loopback / bridge IPs will not match.
		return result, nil
	}

	client := &http.Client{Timeout: defaultTimeout}
	token, err := dragonwildsAccessToken(ctx, client)
	if err != nil {
		return result, nil
	}

	session, err := dragonwildsFindSession(ctx, client, token, host, port)
	if err != nil || session == nil {
		return result, nil
	}

	attrs := session.Attributes
	result.Online = true
	if addr := tieAttrString(attrs, "ADDRESS_s"); addr != "" {
		result.Address = addr
	} else if host != "" {
		result.Address = host
	}
	// VN_s = server name, XS_s = world/slot name (what players search in-game).
	hostname := tieAttrString(attrs, "VN_s")
	if hostname == "" {
		hostname = tieAttrString(attrs, "XS_s")
	}
	result.Hostname = stringPtr(hostname)
	result.Map = stringPtr(firstNonEmpty(
		tieAttrString(attrs, "MAPNAME_s"),
		tieAttrString(attrs, "XS_s"),
	))
	result.Game = stringPtr("RuneScape: Dragonwilds")
	if build := tieAttrString(attrs, "XB_l"); build != "" {
		result.Version = stringPtr(build)
	}
	result.Players = session.TotalPlayers
	result.MaxPlayers = session.Settings.MaxPublicPlayers
	result.PasswordProtected = dragonwildsPasswordProtected(attrs)
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

func dragonwildsAccessToken(ctx context.Context, client *http.Client) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("deployment_id", dragonwildsDeploymentID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dragonwildsEpicAPI+"/auth/v1/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(dragonwildsClientID, dragonwildsClientSecret)
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

func dragonwildsFindSession(ctx context.Context, client *http.Client, token, host string, port int) (*tieSession, error) {
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

	endpoint := fmt.Sprintf("%s/matchmaking/v1/%s/filter", dragonwildsEpicAPI, dragonwildsDeploymentID)
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
	}

	// Single session on this public IP: accept even if bound format differs.
	if len(parsed.Sessions) == 1 {
		return &parsed.Sessions[0], nil
	}

	return nil, nil
}

func dragonwildsPasswordProtected(attrs map[string]interface{}) bool {
	if attrs == nil {
		return false
	}
	v, ok := attrs["XP_l"]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case float64:
		if t == 0 || t == -2 {
			return false
		}
		// JSON numbers may lose uint64 precision; treat the known empty sentinel.
		if uint64(t) == dragonwildsEmptyPasswordHash {
			return false
		}
		return true
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			u, uerr := strconv.ParseUint(t.String(), 10, 64)
			if uerr == nil {
				return u != 0 && u != dragonwildsEmptyPasswordHash
			}
			return false
		}
		return n != 0 && n != -2
	case string:
		if t == "" || t == "0" || t == "-2" || t == strconv.FormatUint(dragonwildsEmptyPasswordHash, 10) {
			return false
		}
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
