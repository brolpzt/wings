package rcon

import (
	"bytes"
	"fmt"
	"net"
	"time"
)

func SendQuake3Command(addr string, password string, command string) (string, error) {
	conn, err := net.DialTimeout("udp", addr, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("failed to connect UDP: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}

	// Format: \xff\xff\xff\xffrcon <password> <command>\n
	payload := fmt.Sprintf("\xff\xff\xff\xffrcon %s %s\n", password, command)
	if _, err := conn.Write([]byte(payload)); err != nil {
		return "", fmt.Errorf("failed to send UDP packet: %w", err)
	}

	// Read response
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("failed to read UDP response: %w", err)
	}

	resp := buf[:n]
	// Remove leading \xff\xff\xff\xff
	if len(resp) >= 4 && bytes.Equal(resp[:4], []byte{0xff, 0xff, 0xff, 0xff}) {
		resp = resp[4:]
	}

	// Strip prefix like "print\n" if present
	respStr := string(resp)
	if len(respStr) >= 6 && respStr[:6] == "print\n" {
		respStr = respStr[6:]
	}

	return respStr, nil
}
