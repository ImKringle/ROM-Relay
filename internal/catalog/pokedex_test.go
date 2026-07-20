package catalog

import (
	"context"
	"testing"
)

func TestSpeciesRoundTrips(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	sp := Species{
		Dex: 182, Name: "bellossom", Genus: "Flower Pokémon", Generation: "generation-ii",
		Types: "grass", FlavorText: "BELLOSSOM gather at times and appear to dance.",
		HeightDM: 4, WeightHG: 58,
	}
	if err := s.UpsertSpecies(ctx, sp); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.SpeciesDetail(ctx, 182)
	if err != nil || !found {
		t.Fatalf("species must be readable back: found=%v err=%v", found, err)
	}
	if got.Name != "bellossom" || got.Genus != "Flower Pokémon" || got.Generation != "generation-ii" {
		t.Fatalf("stored species mismatch: %#v", got)
	}

	// Ingest is resumable and re-runnable, so a second write must update rather
	// than fail or duplicate.
	sp.Genus = "Corrected"
	if err := s.UpsertSpecies(ctx, sp); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.SpeciesDetail(ctx, 182)
	if got.Genus != "Corrected" {
		t.Fatalf("re-ingest must update in place: %#v", got)
	}
	if n, _ := s.SpeciesCount(ctx); n != 1 {
		t.Fatalf("re-ingest must not duplicate: count=%d", n)
	}
}

func TestSpeciesDetailReportsMissingRatherThanEmpty(t *testing.T) {
	got, found, err := testStore(t).SpeciesDetail(context.Background(), 999)
	if err != nil {
		t.Fatal(err)
	}
	// A species that was never ingested must be distinguishable from one with
	// blank fields, or the UI cannot tell "not fetched yet" from "no data".
	if found {
		t.Fatalf("an un-ingested species must report not-found: %#v", got)
	}
}

func TestEvolutionsAndRegionalNumbers(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.UpsertEvolution(ctx, Evolution{FromDex: 44, ToDex: 182, Trigger: "use-item", Item: "sun-stone", Condition: "Use a Sun Stone"}); err != nil {
		t.Fatal(err)
	}
	evos, err := s.EvolutionsFor(ctx, 44)
	if err != nil || len(evos) != 1 {
		t.Fatalf("evolutions for 44: %v %v", evos, err)
	}
	if evos[0].Condition != "Use a Sun Stone" || evos[0].ToDex != 182 {
		t.Fatalf("evolution mismatch: %#v", evos[0])
	}

	if err := s.UpsertRegionalNumber(ctx, RegionalNumber{Dex: 182, Pokedex: "original-johto", Entry: 86}); err != nil {
		t.Fatal(err)
	}
	nums, err := s.RegionalNumbersFor(ctx, 182)
	if err != nil || len(nums) != 1 || nums[0].Entry != 86 {
		t.Fatalf("regional numbers: %#v %v", nums, err)
	}
}

// TestRegionalDexEntriesOrdersAndMergesPokedexes pins the regional scope's data:
// entries come back in entry order, a split dex (Kalos) concatenates in the
// order given, and a species in two areas appears once.
func TestRegionalDexEntriesOrdersAndMergesPokedexes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, r := range []RegionalNumber{
		{Dex: 152, Pokedex: "original-johto", Entry: 1},
		{Dex: 182, Pokedex: "original-johto", Entry: 86},
		{Dex: 161, Pokedex: "original-johto", Entry: 19},
		{Dex: 650, Pokedex: "kalos-central", Entry: 1},
		{Dex: 25, Pokedex: "kalos-central", Entry: 36},
		{Dex: 25, Pokedex: "kalos-coastal", Entry: 90},
		{Dex: 129, Pokedex: "kalos-coastal", Entry: 44},
	} {
		if err := s.UpsertRegionalNumber(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	johto, err := s.RegionalDexEntries(ctx, []string{"original-johto"})
	if err != nil {
		t.Fatal(err)
	}
	if len(johto) != 3 || johto[0].Dex != 152 || johto[1].Dex != 161 || johto[2].Dex != 182 {
		t.Fatalf("entries must come back in entry order: %#v", johto)
	}

	kalos, err := s.RegionalDexEntries(ctx, []string{"kalos-central", "kalos-coastal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(kalos) != 3 {
		t.Fatalf("a species in two areas must appear once: %#v", kalos)
	}
	if kalos[0].Dex != 650 || kalos[1].Dex != 25 || kalos[2].Dex != 129 {
		t.Fatalf("split dexes concatenate in the order given: %#v", kalos)
	}
}

// TestLookupSpeciesMatchesAMoveNameTypedWithoutItsHyphen catches a real QA
// report: move names are stored PokeAPI-style ("false-swipe"), but a typical
// user types "false swipe" and expects the same match.
func TestLookupSpeciesMatchesAMoveNameTypedWithoutItsHyphen(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	sp := Species{Dex: 83, Name: "farfetchd", Genus: "Wild Duck Pokémon", Generation: "generation-i", Types: "normal/flying"}
	if err := s.UpsertSpecies(ctx, sp); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertLearnsetEntry(ctx, 83, LearnsetEntry{Move: "false-swipe", Method: "machine", Generations: "generation-viii"}); err != nil {
		t.Fatal(err)
	}

	for _, typed := range []string{"false-swipe", "false swipe", "False Swipe", "falseswipe", "FALSESWIPE"} {
		got, err := s.LookupSpecies(ctx, LookupFilter{MoveName: typed})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Dex != 83 {
			t.Fatalf("typing %q must still find the species learning false-swipe: got %#v", typed, got)
		}
	}

	none, err := s.LookupSpecies(ctx, LookupFilter{MoveName: "not-a-real-move"})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("an unrelated move must not match: got %#v", none)
	}
}
