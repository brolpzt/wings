package rcon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

type WebRconRequest struct {
	Identifier int32  `json:"Identifier"`
	Message    string `json:"Message"`
	Name       string `json:"Name"`
}

type WebRconResponse struct {
	Identifier int32  `json:"Identifier"`
	Message    string `json:"Message"`
	Type       string `json:"Type"`
}

func SendWebRconCommand(addr string, password string, command string) (string, error) {
	// The path is the RCON password
	u := url.URL{
		Scheme: "ws",
		Host:   addr,
		Path:   "/" + password,
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to connect WebRcon: %w", err)
	}
	defer conn.Close()

	// Send command
	reqID := int32(9999)
	req := WebRconRequest{
		Identifier: reqID,
		Message:    command,
		Name:       "Wings",
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	if err := conn.WriteMessage(websocket.TextMessage, reqBytes); err != nil {
		return "", fmt.Errorf("failed to send WebRcon command: %w", err)
	}

	// Wait for response with matching Identifier
	deadline := time.Now().Add(5 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return "", err
	}

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return "", fmt.Errorf("failed to read WebRcon response: %w", err)
		}

		var resp WebRconResponse
		if err := json.Unmarshal(message, &resp); err == nil {
			if resp.Identifier == reqID {
				return resp.Message, nil
			}
		}
	}
}
