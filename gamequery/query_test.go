package gamequery

import (
	"testing"
	"time"

	"github.com/warsmite/gjq"
)

func TestNormalizePlayerCount_UsesProtocolCount(t *testing.T) {
	info := &gjq.ServerInfo{
		Players: 3,
		PlayerList: []gjq.PlayerInfo{
			{Name: "A"},
			{Name: "B"},
		},
	}

	if got := normalizePlayerCount(info); got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
}

func TestNormalizePlayerCount_FallsBackToPlayerList(t *testing.T) {
	info := &gjq.ServerInfo{
		Players: 0,
		PlayerList: []gjq.PlayerInfo{
			{Name: `^9ev^1'^7TEAM ^9Nick`},
			{Name: `^9ev^1'^7TEAM ^9marcos`},
			{Name: ""},
		},
	}

	if got := normalizePlayerCount(info); got != 2 {
		t.Fatalf("expected 2 non-empty players, got %d", got)
	}
}

func TestNormalizePlayerCount_CountsAnonymousEntries(t *testing.T) {
	info := &gjq.ServerInfo{
		PlayerList: []gjq.PlayerInfo{
			{Name: " ", Duration: gjq.Duration{Duration: time.Second}},
		},
	}

	if got := normalizePlayerCount(info); got != 1 {
		t.Fatalf("expected fallback list length 1, got %d", got)
	}
}
