package torrent

import (
	"reflect"
	"testing"
)

func TestParseRelease(t *testing.T) {
	cases := map[string]Release{
		"DS/Pokemon - Black Version (USA, Europe) (NDSi Enhanced).zip": {Title: "Pokemon - Black Version", Regions: []string{"USA", "Europe"}, Tags: []string{"NDSi Enhanced"}},
		"Mario Kart DS (Japan) (Rev 1) [b].nds":                        {Title: "Mario Kart DS", Regions: []string{"Japan"}, Tags: []string{"Rev 1", "b"}},
		"Tetris DS (Europe) (En,Fr,De,Es,It).zip":                      {Title: "Tetris DS", Regions: []string{"Europe"}, Languages: []string{"En", "Fr", "De", "Es", "It"}},
		"Japan Quest (USA).gba":                                        {Title: "Japan Quest", Regions: []string{"USA"}},
		"plain.gba":                                                    {Title: "plain"},
	}
	for in, want := range cases {
		if got := ParseRelease(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", in, got, want)
		}
	}
}
