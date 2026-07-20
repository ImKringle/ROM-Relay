package dex

import "testing"

// national is a stand-in species list spanning past a early game's dex range.
func national(n int) []SpeciesRef {
	out := make([]SpeciesRef, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, SpeciesRef{Dex: i, Name: "Species"})
	}
	return out
}

// A game's Pokédex is scored against that game's own range. Measuring HeartGold
// out of the modern National total made every regional view read as a fraction of
// 1,025, the completion figure could never reach 100% and barely moved.
func TestAPerGameViewIsScoredAgainstThatGamesOwnDex(t *testing.T) {
	species := national(1025)
	rows := []OwnedRow{
		{Species: 1, Game: "HG", Count: 1},
		{Species: 250, Game: "HG", Count: 1},
	}

	view := Build(species, rows, "HG", 493)

	if view.Total != 493 {
		t.Errorf("a Johto dex is out of 493, not the National list: total=%d", view.Total)
	}
	if len(view.Entries) != 493 {
		t.Errorf("a per-game view must not list species the game has no concept of: entries=%d", len(view.Entries))
	}
	if view.Owned != 2 {
		t.Errorf("owned=%d want 2", view.Owned)
	}
	// 2/493 rounds to 0%, where 2/1025 also rounded to 0, check the arithmetic
	// uses the game's total by way of a figure that differs.
	if view.Percent != 2*100/493 {
		t.Errorf("percent must be measured against the game's own dex: got %d", view.Percent)
	}
}

// The National Living Dex keeps counting every species: it is what you own across
// every game, so nothing bounds it.
func TestTheNationalViewCountsEverySpecies(t *testing.T) {
	species := national(1025)
	rows := []OwnedRow{{Species: 1, Game: "HG", Count: 1}, {Species: 700, Game: "B2", Count: 1}}

	view := Build(species, rows, "", 0)

	if view.Total != 1025 || len(view.Entries) != 1025 {
		t.Errorf("the National dex spans the whole species list: total=%d entries=%d", view.Total, len(view.Entries))
	}
	if view.Owned != 2 {
		t.Errorf("owned=%d want 2", view.Owned)
	}
	if view.Scope != "national" {
		t.Errorf("scope=%q", view.Scope)
	}
}

// A game whose range was never recorded (ingested before the range was stored)
// still renders, against the National list, rather than collapsing to an empty
// dex.
func TestAGameWithNoRecordedRangeFallsBackToTheFullList(t *testing.T) {
	view := Build(national(1025), []OwnedRow{{Species: 5, Game: "E", Count: 1}}, "E", 0)

	if view.Total != 1025 {
		t.Errorf("total=%d want the full list as a fallback", view.Total)
	}
	if view.Owned != 1 {
		t.Errorf("owned=%d want 1", view.Owned)
	}
}
