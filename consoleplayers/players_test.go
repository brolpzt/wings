package consoleplayers

import "testing"

func TestParseStatusOutput_ClassicFormat(t *testing.T) {
	lines := []string{
		"hostname: HostGamer CS 1.6",
		"version : 1.1.2.7/Stdio 48 8000 secure",
		"map     : de_dust2 at: 0 x, 0 y, 0 z",
		"players : 2 active (32 max)",
		"#      name userid uniqueid  frag ping loss  state adr",
		`# 1 "PlayerOne" 2 STEAM_0:1:12345  32   45   0     active 192.168.0.10:27005`,
		`# 2 "Bot" 3 BOT               0    0    0     active`,
		"2 users",
	}

	result := parseStatusOutput(lines)

	if result.Hostname == nil || *result.Hostname != "HostGamer CS 1.6" {
		t.Fatalf("unexpected hostname: %#v", result.Hostname)
	}

	if result.Map == nil || *result.Map != "de_dust2" {
		t.Fatalf("unexpected map: %#v", result.Map)
	}

	if len(result.Players) != 2 {
		t.Fatalf("expected 2 players, got %d", len(result.Players))
	}

	if result.Players[0].UserId != 2 || result.Players[0].SteamId != "STEAM_0:1:12345" {
		t.Fatalf("unexpected first player: %#v", result.Players[0])
	}

	if result.Players[0].Address != "192.168.0.10:27005" {
		t.Fatalf("unexpected address: %q", result.Players[0].Address)
	}
}

func TestParseStatusOutput_ReHLDSFormat(t *testing.T) {
	lines := []string{
		"hostname:  hostname",
		"version :  48/1.1.2.7/Stdio 4002 insecure  (10)",
		"map     :  de_dust2 at: 0 x, 0 y, 0 z",
		"players :  2 active (4 max)",
		"#      name userid uniqueid frag time ping loss adr",
		`# 1 "(HE)fofo?" 13 STEAM_0:1:519045739   0 03:18   49    0 95.25.5.109:15931`,
		`# 2 "PlayerTwo" 14 STEAM_0:0:12345678   5 12:01  32    0 10.0.0.2:27005`,
		"2 users",
	}

	result := parseStatusOutput(lines)

	if len(result.Players) != 2 {
		t.Fatalf("expected 2 players, got %d (%#v)", len(result.Players), result.Players)
	}

	player := result.Players[0]
	if player.UserId != 13 || player.SteamId != "STEAM_0:1:519045739" || player.Ping != 49 {
		t.Fatalf("unexpected first player: %#v", player)
	}

	if player.Address != "95.25.5.109:15931" {
		t.Fatalf("unexpected address: %q", player.Address)
	}

	if player.Connected != "03:18" {
		t.Fatalf("unexpected connected time: %q", player.Connected)
	}

	second := result.Players[1]
	if second.UserId != 14 || second.Ping != 32 {
		t.Fatalf("unexpected second player: %#v", second)
	}

	if second.Connected != "12:01" {
		t.Fatalf("unexpected second connected time: %q", second.Connected)
	}
}

func TestParseStatusOutput_QuotedSteamId(t *testing.T) {
	lines := []string{
		`# 2 "Player" "STEAM_0:0:99999" 05:23 45 0 active 10.0.0.5:27005`,
	}

	result := parseStatusOutput(lines)

	if len(result.Players) != 1 {
		t.Fatalf("expected 1 player, got %d", len(result.Players))
	}

	player := result.Players[0]
	if player.UserId != 2 || player.SteamId != "STEAM_0:0:99999" || player.Ping != 45 {
		t.Fatalf("unexpected player: %#v", player)
	}
}

func TestCleanDockerLogLine(t *testing.T) {
	raw := string([]byte{1, 0, 0, 0, 0, 0, 0, 0}) + `# 1 "Test" 2 STEAM_0:1:1 0 0 0 active`
	line := cleanDockerLogLine(raw)

	if line != `# 1 "Test" 2 STEAM_0:1:1 0 0 0 active` {
		t.Fatalf("unexpected cleaned line: %q", line)
	}
}

func TestParseStatusOutput_UsesLatestStatusBlock(t *testing.T) {
	lines := []string{
		"#      name userid uniqueid frag time ping loss adr",
		`# 1 "OldPlayer" 1 STEAM_0:1:1 0 00:10 10 0`,
		"1 users",
		"hostname:  hostname",
		"#      name userid uniqueid frag time ping loss adr",
		`# 1 "NewPlayer" 9 STEAM_0:1:999 0 01:00 25 0`,
		"1 users",
	}

	result := parseStatusOutput(lines)

	if len(result.Players) != 1 {
		t.Fatalf("expected 1 player, got %d", len(result.Players))
	}

	if result.Players[0].Name != "NewPlayer" || result.Players[0].UserId != 9 {
		t.Fatalf("unexpected player from latest block: %#v", result.Players[0])
	}
}
