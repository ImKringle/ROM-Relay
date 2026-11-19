package catalog

import "testing"

func TestPlatformByPathSharedExtensions(t *testing.T) {
	cases := map[string]string{
		"game.gba":                          "gba",
		"Sega Genesis/Sonic.bin":            "genesis",
		"PlayStation 2/Game (USA).chd":      "ps2",
		"PlayStation/Game (USA).chd":        "ps1",
		"Redump/Nintendo GameCube/Game.iso": "gc",
		"psp/Game.iso":                      "psp",
		"misc/Game.iso":                     "",
	}
	for in, want := range cases {
		p, _ := PlatformByPath(in)
		if p.ID != want {
			t.Errorf("%s: got %q want %q", in, p.ID, want)
		}
	}
	if _, ok := PlatformByFilename("x.iso"); ok {
		t.Error(".iso must be ambiguous by extension alone")
	}
}
