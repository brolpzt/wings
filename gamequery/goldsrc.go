package gamequery

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"emperror.dev/errors"
)

// GoldSrc (Half-Life 1 / CS 1.6) query client.
//
// gjq only reads the first A2S_INFO packet and requires the Source 0x49 header.
// Servers running Reunion with ServerInfoAnswerType = 2 ("hybrid") answer each
// A2S_INFO with two packets: the legacy GoldSrc 0x6d reply followed by the
// Source 0x49 reply. gjq rejects the first one and the leftover packet then
// poisons the A2S_PLAYER exchange. This client reads every packet, prefers the
// 0x49 reply, falls back to 0x6d, and skips stray packets while asking for
// players.

const (
	goldsrcInfoRequest     = 0x54
	goldsrcInfoSource      = 0x49 // 'I'
	goldsrcInfoLegacy      = 0x6d // 'm'
	goldsrcPlayerRequest   = 0x55
	goldsrcPlayerResponse  = 0x44 // 'D'
	goldsrcChallenge       = 0x41 // 'A'
	goldsrcSinglePacket    = 0xFFFFFFFF
	goldsrcHybridSecondGap = 300 * time.Millisecond
)

var goldsrcInfoPayload = append(
	[]byte{0xFF, 0xFF, 0xFF, 0xFF, goldsrcInfoRequest},
	append([]byte("Source Engine Query"), 0x00)...,
)

type goldsrcInfo struct {
	Name       string
	Map        string
	Folder     string
	Game       string
	Players    int
	MaxPlayers int
	Bots       int
	Private    bool
	Version    string
}

type goldsrcPlayer struct {
	Name     string
	Score    int
	Duration float32
}

// isGoldsrcGame reports whether the game type should use the GoldSrc client.
func isGoldsrcGame(gameType string) bool {
	if mapped, ok := gameTypeOverrides[gameType]; ok {
		gameType = mapped
	}

	return gameType == "counter-strike"
}

func queryGoldsrc(ctx context.Context, result *Result, host string, queryPort int) (*Result, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > defaultTimeout {
		deadline = time.Now().Add(defaultTimeout)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(queryPort))
	conn, err := net.DialTimeout("udp", addr, time.Until(deadline))
	if err != nil {
		return nil, errors.Wrap(err, "goldsrc: connect")
	}
	defer conn.Close()

	info, err := goldsrcQueryInfo(conn, deadline)
	if err != nil {
		return nil, err
	}

	result.Online = true
	result.Hostname = stringPtr(info.Name)
	result.Map = stringPtr(info.Map)
	result.Game = stringPtr(info.Game)
	result.Players = info.Players
	result.MaxPlayers = info.MaxPlayers
	result.PasswordProtected = info.Private
	result.Version = stringPtr(info.Version)

	// Some servers never answer A2S_PLAYER; that must not mark them offline.
	if players, err := goldsrcQueryPlayers(conn, deadline); err == nil {
		for _, player := range players {
			result.PlayerList = append(result.PlayerList, Player{
				Name:  player.Name,
				Score: player.Score,
				Time:  int(player.Duration),
			})
		}
		if result.Players == 0 && len(players) > 0 {
			result.Players = len(players)
		}
	}

	return result, nil
}

// goldsrcQueryInfo sends A2S_INFO and returns the best reply: the Source 0x49
// packet when present, otherwise the legacy 0x6d packet.
func goldsrcQueryInfo(conn net.Conn, deadline time.Time) (*goldsrcInfo, error) {
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if _, err := conn.Write(goldsrcInfoPayload); err != nil {
		return nil, errors.Wrap(err, "goldsrc: send a2s_info")
	}

	var legacy *goldsrcInfo
	challenged := false

	for {
		data, err := goldsrcReadPacket(conn)
		if err != nil {
			if legacy != nil {
				return legacy, nil
			}
			return nil, errors.Wrap(err, "goldsrc: read a2s_info")
		}
		if len(data) == 0 {
			continue
		}

		switch data[0] {
		case goldsrcInfoSource:
			return parseGoldsrcSourceInfo(data[1:])
		case goldsrcInfoLegacy:
			if parsed, perr := parseGoldsrcLegacyInfo(data[1:]); perr == nil && legacy == nil {
				legacy = parsed
				// In hybrid mode the 0x49 reply follows right away; wait briefly for it.
				short := time.Now().Add(goldsrcHybridSecondGap)
				if short.Before(deadline) {
					_ = conn.SetReadDeadline(short)
				}
			}
		case goldsrcChallenge:
			if challenged || len(data) < 5 {
				continue
			}
			challenged = true
			retry := append(append([]byte{}, goldsrcInfoPayload...), data[1:5]...)
			if _, err := conn.Write(retry); err != nil {
				return nil, errors.Wrap(err, "goldsrc: send a2s_info challenge")
			}
		}
	}
}

// goldsrcQueryPlayers performs the A2S_PLAYER challenge exchange, ignoring any
// stray info packets still queued from the hybrid A2S_INFO reply.
func goldsrcQueryPlayers(conn net.Conn, deadline time.Time) ([]goldsrcPlayer, error) {
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}

	request := []byte{0xFF, 0xFF, 0xFF, 0xFF, goldsrcPlayerRequest, 0xFF, 0xFF, 0xFF, 0xFF}
	if _, err := conn.Write(request); err != nil {
		return nil, errors.Wrap(err, "goldsrc: send a2s_player")
	}

	challenged := false
	for {
		data, err := goldsrcReadPacket(conn)
		if err != nil {
			return nil, errors.Wrap(err, "goldsrc: read a2s_player")
		}
		if len(data) == 0 {
			continue
		}

		switch data[0] {
		case goldsrcPlayerResponse:
			return parseGoldsrcPlayers(data[1:])
		case goldsrcChallenge:
			if challenged || len(data) < 5 {
				continue
			}
			challenged = true
			retry := append([]byte{0xFF, 0xFF, 0xFF, 0xFF, goldsrcPlayerRequest}, data[1:5]...)
			if _, err := conn.Write(retry); err != nil {
				return nil, errors.Wrap(err, "goldsrc: send a2s_player challenge")
			}
		}
	}
}

// goldsrcReadPacket reads one datagram and strips the 0xFFFFFFFF header.
// Split packets are skipped: GoldSrc info/player replies fit in one datagram.
func goldsrcReadPacket(conn net.Conn) ([]byte, error) {
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n < 5 {
			continue
		}
		if binary.LittleEndian.Uint32(buf[:4]) != goldsrcSinglePacket {
			continue
		}

		out := make([]byte, n-4)
		copy(out, buf[4:n])
		return out, nil
	}
}

// parseGoldsrcSourceInfo parses a 0x49 reply (payload after the type byte).
func parseGoldsrcSourceInfo(data []byte) (*goldsrcInfo, error) {
	r := bytes.NewReader(data)
	if _, err := r.ReadByte(); err != nil { // protocol
		return nil, errors.New("goldsrc: truncated a2s_info")
	}

	info := &goldsrcInfo{}
	var err error
	if info.Name, err = readCString(r); err != nil {
		return nil, err
	}
	if info.Map, err = readCString(r); err != nil {
		return nil, err
	}
	if info.Folder, err = readCString(r); err != nil {
		return nil, err
	}
	if info.Game, err = readCString(r); err != nil {
		return nil, err
	}

	var appID uint16
	if err := binary.Read(r, binary.LittleEndian, &appID); err != nil {
		return nil, errors.New("goldsrc: truncated a2s_info")
	}

	fields := make([]byte, 3) // players, max players, bots
	if _, err := r.Read(fields); err != nil {
		return nil, errors.New("goldsrc: truncated a2s_info")
	}
	info.Players = int(fields[0])
	info.MaxPlayers = int(fields[1])
	info.Bots = int(fields[2])

	// server type, environment, visibility, VAC — optional from here on.
	tail := make([]byte, 4)
	if n, _ := r.Read(tail); n >= 3 {
		info.Private = tail[2] == 1
	}
	if version, err := readCString(r); err == nil {
		info.Version = version
	}

	return info, nil
}

// parseGoldsrcLegacyInfo parses the obsolete GoldSrc 0x6d reply.
func parseGoldsrcLegacyInfo(data []byte) (*goldsrcInfo, error) {
	r := bytes.NewReader(data)

	if _, err := readCString(r); err != nil { // server address
		return nil, err
	}

	info := &goldsrcInfo{}
	var err error
	if info.Name, err = readCString(r); err != nil {
		return nil, err
	}
	if info.Map, err = readCString(r); err != nil {
		return nil, err
	}
	if info.Folder, err = readCString(r); err != nil {
		return nil, err
	}
	if info.Game, err = readCString(r); err != nil {
		return nil, err
	}

	fields := make([]byte, 2) // players, max players
	if _, err := r.Read(fields); err != nil {
		return nil, errors.New("goldsrc: truncated legacy a2s_info")
	}
	info.Players = int(fields[0])
	info.MaxPlayers = int(fields[1])

	// protocol, server type, environment, visibility — optional.
	tail := make([]byte, 4)
	if n, _ := r.Read(tail); n >= 4 {
		info.Private = tail[3] == 1
	}

	return info, nil
}

func parseGoldsrcPlayers(data []byte) ([]goldsrcPlayer, error) {
	r := bytes.NewReader(data)
	count, err := r.ReadByte()
	if err != nil {
		return nil, errors.New("goldsrc: truncated a2s_player")
	}

	players := make([]goldsrcPlayer, 0, count)
	for i := 0; i < int(count); i++ {
		if _, err := r.ReadByte(); err != nil { // index
			break
		}
		name, err := readCString(r)
		if err != nil {
			break
		}
		var score int32
		if err := binary.Read(r, binary.LittleEndian, &score); err != nil {
			break
		}
		var duration float32
		if err := binary.Read(r, binary.LittleEndian, &duration); err != nil {
			break
		}
		if math.IsNaN(float64(duration)) || math.IsInf(float64(duration), 0) || duration < 0 || duration > 365*24*3600 {
			duration = 0
		}

		players = append(players, goldsrcPlayer{
			Name:     strings.TrimSpace(name),
			Score:    int(score),
			Duration: duration,
		})
	}

	return players, nil
}

func readCString(r *bytes.Reader) (string, error) {
	var sb strings.Builder
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", errors.New("goldsrc: unterminated string")
		}
		if b == 0x00 {
			return sb.String(), nil
		}
		sb.WriteByte(b)
	}
}
