package gamequery

import "testing"

func TestIsDragonwildsGame(t *testing.T) {
	cases := map[string]bool{
		"dragonwilds":           true,
		"rsdragonwilds":         true,
		"runescape-dragonwilds": true,
		"rs-dragonwilds":        true,
		"DRAGONWILDS":           false, // normalize happens before call
		"tie":                   false,
		"valheim":               false,
	}
	for in, want := range cases {
		if got := isDragonwildsGame(in); got != want {
			t.Fatalf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestDragonwildsPasswordProtected(t *testing.T) {
	if dragonwildsPasswordProtected(nil) {
		t.Fatal("nil attrs should be unprotected")
	}
	if dragonwildsPasswordProtected(map[string]interface{}{"XP_l": float64(0)}) {
		t.Fatal("0 should be unprotected")
	}
	if dragonwildsPasswordProtected(map[string]interface{}{"XP_l": float64(-2)}) {
		t.Fatal("-2 should be unprotected")
	}
	if !dragonwildsPasswordProtected(map[string]interface{}{"XP_l": float64(-8684486148751930199)}) {
		t.Fatal("non-sentinel hash should be protected")
	}
}
