package gamequery

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"emperror.dev/errors"
)

func cstr(s string) []byte { return append([]byte(s), 0x00) }

func sourceInfoPacket(name, mapName string, players, maxPlayers byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, goldsrcInfoSource, 48})
	b.Write(cstr(name))
	b.Write(cstr(mapName))
	b.Write(cstr("cstrike"))
	b.Write(cstr("Counter-Strike"))
	_ = binary.Write(&b, binary.LittleEndian, uint16(10))
	b.Write([]byte{players, maxPlayers, 0, 'd', 'l', 0, 1})
	b.Write(cstr("1.1.2.7/Stdio"))
	return b.Bytes()
}

func legacyInfoPacket(name, mapName string, players, maxPlayers byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, goldsrcInfoLegacy})
	b.Write(cstr("1.2.3.4:27015"))
	b.Write(cstr(name))
	b.Write(cstr(mapName))
	b.Write(cstr("cstrike"))
	b.Write(cstr("Counter-Strike"))
	b.Write([]byte{players, maxPlayers, 47, 'd', 'l', 0, 0, 1, 0})
	return b.Bytes()
}

func playerPacket(names ...string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, goldsrcPlayerResponse, byte(len(names))})
	for i, name := range names {
		b.WriteByte(byte(i))
		b.Write(cstr(name))
		_ = binary.Write(&b, binary.LittleEndian, int32(10+i))
		_ = binary.Write(&b, binary.LittleEndian, float32(120))
	}
	return b.Bytes()
}

// fakeGoldsrc answers A2S_INFO with infoReplies and A2S_PLAYER with a challenge
// followed by the player list, mimicking ReHLDS + Reunion.
func fakeGoldsrc(t *testing.T, infoReplies [][]byte, players [][]byte) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 5 {
				continue
			}
			switch buf[4] {
			case goldsrcInfoRequest:
				for _, reply := range infoReplies {
					_, _ = conn.WriteTo(reply, addr)
				}
			case goldsrcPlayerRequest:
				if players == nil {
					continue
				}
				if n >= 9 && bytes.Equal(buf[5:9], []byte{1, 2, 3, 4}) {
					for _, reply := range players {
						_, _ = conn.WriteTo(reply, addr)
					}
				} else {
					_, _ = conn.WriteTo([]byte{0xFF, 0xFF, 0xFF, 0xFF, goldsrcChallenge, 1, 2, 3, 4}, addr)
				}
			}
		}
	}()

	return conn.LocalAddr().(*net.UDPAddr).Port
}

func runGoldsrc(t *testing.T, port int) *Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := &Result{PlayerList: []Player{}}
	out, err := queryGoldsrc(ctx, result, "127.0.0.1", port)
	if err != nil {
		t.Fatalf("queryGoldsrc: %v", err)
	}
	return out
}

func TestGoldsrc_HybridModePrefersSourceReply(t *testing.T) {
	port := fakeGoldsrc(t,
		[][]byte{legacyInfoPacket("legacy name", "zm_old", 1, 32), sourceInfoPacket("ZOMBIE PLAGUE", "zm_fazenda_new_lm", 5, 32)},
		[][]byte{playerPacket("a", "b", "c", "d", "e")},
	)

	out := runGoldsrc(t, port)
	if !out.Online || *out.Hostname != "ZOMBIE PLAGUE" || *out.Map != "zm_fazenda_new_lm" {
		t.Fatalf("unexpected result: online=%v host=%v map=%v", out.Online, out.Hostname, out.Map)
	}
	if out.Players != 5 || out.MaxPlayers != 32 {
		t.Fatalf("expected 5/32 players, got %d/%d", out.Players, out.MaxPlayers)
	}
	if len(out.PlayerList) != 5 || out.PlayerList[0].Name != "a" || out.PlayerList[0].Time != 120 {
		t.Fatalf("unexpected player list: %+v", out.PlayerList)
	}
}

func TestGoldsrc_SourceFirstThenLegacyDoesNotBreakPlayers(t *testing.T) {
	port := fakeGoldsrc(t,
		[][]byte{sourceInfoPacket("srv", "de_dust2", 2, 20), legacyInfoPacket("srv", "de_dust2", 2, 20)},
		[][]byte{playerPacket("x", "y")},
	)

	out := runGoldsrc(t, port)
	if *out.Map != "de_dust2" || len(out.PlayerList) != 2 {
		t.Fatalf("unexpected result: map=%v players=%+v", out.Map, out.PlayerList)
	}
}

func TestGoldsrc_LegacyOnlyMode(t *testing.T) {
	port := fakeGoldsrc(t, [][]byte{legacyInfoPacket("old server", "de_nuke", 3, 16)}, nil)

	out := runGoldsrc(t, port)
	if !out.Online || *out.Hostname != "old server" || *out.Map != "de_nuke" || out.Players != 3 || out.MaxPlayers != 16 {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestGoldsrc_SourceOnlyMode(t *testing.T) {
	port := fakeGoldsrc(t, [][]byte{sourceInfoPacket("new server", "de_inferno", 0, 12)}, [][]byte{playerPacket()})

	out := runGoldsrc(t, port)
	if !out.Online || *out.Map != "de_inferno" || out.Players != 0 || out.MaxPlayers != 12 {
		t.Fatalf("unexpected result: %+v", out)
	}
	if out.Version == nil || *out.Version != "1.1.2.7/Stdio" {
		t.Fatalf("unexpected version: %v", out.Version)
	}
}

func TestGoldsrc_NoReplyIsTimeout(t *testing.T) {
	port := fakeGoldsrc(t, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := queryGoldsrc(ctx, &Result{}, "127.0.0.1", port)
	var netErr net.Error
	if err == nil || !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestIsGoldsrcGame(t *testing.T) {
	for _, game := range []string{"cs16", "cstrike", "cscz", "counter-strike"} {
		if !isGoldsrcGame(game) {
			t.Fatalf("%s should be goldsrc", game)
		}
	}
	for _, game := range []string{"css", "csgo", "tf2", "cod2"} {
		if isGoldsrcGame(game) {
			t.Fatalf("%s should not be goldsrc", game)
		}
	}
}
