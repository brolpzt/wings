package rcon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	PacketAuth         = 3
	PacketAuthResponse = 2
	PacketExecCommand  = 2
	PacketResponse     = 0
)

func SendSourceCommand(addr string, password string, command string) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("failed to connect RCON: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}

	// 1. Authenticate
	authID := int32(1234)
	if err := writeSourcePacket(conn, authID, PacketAuth, []byte(password)); err != nil {
		return "", fmt.Errorf("failed to send auth: %w", err)
	}

	// Read auth responses
	for {
		respID, respType, _, err := readSourcePacket(conn)
		if err != nil {
			return "", fmt.Errorf("failed to read auth response: %w", err)
		}
		if respType == PacketAuthResponse {
			if respID == -1 {
				return "", fmt.Errorf("authentication failed")
			}
			break
		}
	}

	// 2. Exec command
	cmdID := int32(5678)
	if err := writeSourcePacket(conn, cmdID, PacketExecCommand, []byte(command)); err != nil {
		return "", fmt.Errorf("failed to send command: %w", err)
	}

	// Read command response
	_, respType, body, err := readSourcePacket(conn)
	if err != nil {
		return "", fmt.Errorf("failed to read command response: %w", err)
	}

	if respType == PacketResponse {
		return string(body), nil
	}

	return string(body), nil
}

func writeSourcePacket(w io.Writer, id int32, typ int32, body []byte) error {
	size := int32(4 + 4 + len(body) + 2) // id + typ + body + 2 null bytes
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, size); err != nil {
		return err
	}
	if err := binary.Write(buf, binary.LittleEndian, id); err != nil {
		return err
	}
	if err := binary.Write(buf, binary.LittleEndian, typ); err != nil {
		return err
	}
	if _, err := buf.Write(body); err != nil {
		return err
	}
	if _, err := buf.Write([]byte{0x00, 0x00}); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func readSourcePacket(r io.Reader) (int32, int32, []byte, error) {
	var size int32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return 0, 0, nil, err
	}
	if size < 10 {
		return 0, 0, nil, fmt.Errorf("invalid packet size: %d", size)
	}

	var id int32
	if err := binary.Read(r, binary.LittleEndian, &id); err != nil {
		return 0, 0, nil, err
	}

	var typ int32
	if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
		return 0, 0, nil, err
	}

	bodyLen := size - 4 - 4 - 2
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, 0, nil, err
	}

	// Read the remaining two null bytes
	nulls := make([]byte, 2)
	if _, err := io.ReadFull(r, nulls); err != nil {
		return 0, 0, nil, err
	}

	return id, typ, body, nil
}
