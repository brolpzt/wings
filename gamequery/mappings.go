package gamequery

// gameTypeOverrides maps legacy GameQ / GameDig identifiers to gjq registry IDs.
// Note: "tie" / Evrima is handled separately (EOS) and must NOT map to the-isle (Source).
var gameTypeOverrides = map[string]string{
	"cs16":            "counter-strike",
	"counterstrike16": "counter-strike",
	"cstrike":         "counter-strike",
	"goldsource":      "counter-strike",
	"cscz":            "counter-strike",
	"cz":              "counter-strike",
	"minecraft":       "minecraft-java",
	"mc":              "minecraft-java",
	"mcje":            "minecraft-java",
	"bedrock":         "minecraft-bedrock",
	"mcbe":            "minecraft-bedrock",
	"gmod":            "garrys-mod",
	"tf2":             "team-fortress-2",
	"css":             "counter-strike-source",
	"csgo":            "counter-strike-go",
	"codmw3":          "cod-mw2",
	"mw3":             "cod-mw2",
	"iw4x":            "cod-mw2",
	"coduo":           "cod-waw",
	"waw":             "cod-waw",
	"t4":              "cod-waw",
	"7dtd":            "7-days-to-die",
	"7d2d":            "7-days-to-die",
}

// protocolFallbacks handles games that are not in the gjq registry but share a known protocol.
var protocolFallbacks = map[string]string{
	"cod":   "quake3",
	"cod2":  "quake3",
	"mohaa": "quake3",
}
