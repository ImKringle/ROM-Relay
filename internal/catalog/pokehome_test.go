package catalog

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// TestDepositExemptsFromPruneAndFromSaveDeletion pins PokeHome's core
// guarantee: a deposited Pokémon survives its source save's fate, whether
// that save vanishes via the admin PruneDexOrphans sweep or via an explicit
// DeleteSave. A non-deposited sibling from the same save is removed by both,
// exactly as before this feature existed.
func TestDepositExemptsFromPruneAndFromSaveDeletion(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	saveA, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", SHA256: "aaa"})
	if err != nil {
		t.Fatal(err)
	}
	saveB, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-ss", SHA256: "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	ids, _, _, err := s.SavePokemonInstances(ctx, owner, []PokemonInstance{
		{Species: 25, SpeciesName: "Pikachu", OriginSaveID: saveA.ID, Binary: []byte("kept-by-deposit")},
		{Species: 1, SpeciesName: "Bulbasaur", OriginSaveID: saveA.ID, Binary: []byte("plain")},
		{Species: 7, SpeciesName: "Squirtle", OriginSaveID: saveB.ID, Binary: []byte("other-save")},
	})
	if err != nil {
		t.Fatal(err)
	}
	deposited := ids[0]

	if _, err := s.DepositPokemonInstance(ctx, owner, deposited); err != nil {
		t.Fatal(err)
	}

	// PruneDexOrphans first: remove saveB's row directly (not through
	// DeleteSave, which already cascades pokemon_instances itself, this
	// simulates the case PruneDexOrphans actually exists for: a
	// pokemon_instances row whose origin save is gone by some other means).
	// Pikachu (deposited) and Bulbasaur (not deposited, saveA still exists)
	// must both survive this sweep.
	if _, err := s.execCtx(ctx, `DELETE FROM saves WHERE id=?`, saveB.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := s.PruneDexOrphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].ID != ids[2] {
		t.Fatalf("prune should remove exactly the orphaned Squirtle: %#v", removed)
	}

	// Now delete saveA itself, Pikachu's origin save. The deposit must keep
	// it alive even though DeleteSave normally takes a save's Pokémon with it
	// (TestDeleteSaveTakesItsPokemonWithIt pins that for the non-deposited case).
	if _, err := s.DeleteSave(ctx, owner, saveA.ID); err != nil {
		t.Fatal(err)
	}
	mons, err := s.PokemonInstances(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 1 || mons[0].ID != deposited {
		t.Fatalf("deleting the deposit's origin save must leave only the deposited Pokémon: %#v", mons)
	}
	if mons[0].HomeDepositedAt == nil {
		t.Fatal("the surviving row must still read as deposited")
	}
}

// TestWithdrawIsCopyNotMove pins the rev.2 duplication decision: withdrawing
// a deposited Pokémon is bookkeeping only (home_last_withdrawn_at), never a
// gate. The same deposit must remain listed and withdrawable again, no
// one-way consumption.
func TestWithdrawIsCopyNotMove(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	ids, _, _, err := s.SavePokemonInstances(ctx, owner, []PokemonInstance{
		{Species: 150, SpeciesName: "Mewtwo", Binary: []byte("mewtwo")},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := ids[0]
	if _, err := s.DepositPokemonInstance(ctx, owner, id); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := s.MarkPokemonWithdrawn(ctx, owner, id); err != nil {
			t.Fatalf("withdrawal %d: %v", i, err)
		}
		list, err := s.DepositedPokemonInstances(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ID != id {
			t.Fatalf("withdrawal %d must not remove the deposit: %#v", i, list)
		}
		if list[0].HomeLastWithdrawnAt == nil {
			t.Fatalf("withdrawal %d must record home_last_withdrawn_at", i)
		}
	}
}

// TestOriginSlotRoundTrips pins the QA fix for "the Editor doesn't show
// where in the save a Pokémon actually lives": OriginSlot must survive the
// save→read round trip unchanged, so the Editor's Party/Box grouping can
// trust it.
func TestOriginSlotRoundTrips(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	ids, _, _, err := s.SavePokemonInstances(ctx, owner, []PokemonInstance{
		{Species: 1, SpeciesName: "Bulbasaur", OriginSlot: "party:2", Binary: []byte("a")},
		{Species: 2, SpeciesName: "Ivysaur", OriginSlot: "box:3:12", Binary: []byte("b")},
		{Species: 3, SpeciesName: "Venusaur", Binary: []byte("c")}, // from-scratch: no slot
	})
	if err != nil {
		t.Fatal(err)
	}

	byID := map[string]string{}
	list, err := s.PokemonInstances(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		byID[p.ID] = p.OriginSlot
	}
	if byID[ids[0]] != "party:2" {
		t.Fatalf("expected party:2, got %q", byID[ids[0]])
	}
	if byID[ids[1]] != "box:3:12" {
		t.Fatalf("expected box:3:12, got %q", byID[ids[1]])
	}
	if byID[ids[2]] != "" {
		t.Fatalf("a from-scratch Pokémon must have no origin slot, got %q", byID[ids[2]])
	}
}

// TestDeletePokemonInstanceIsOwnerScoped pins the admin/PokeHome delete
// button's safety property: deleting removes the row outright regardless of
// deposit state, but only for its actual owner, another account's id, even
// if guessed correctly, is reported as not found rather than deleted.
func TestDeletePokemonInstanceIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)
	other, err := s.CreateUser(ctx, "otheruser", "hash", "user", false)
	if err != nil {
		t.Fatal(err)
	}

	ids, _, _, err := s.SavePokemonInstances(ctx, owner, []PokemonInstance{
		{Species: 25, SpeciesName: "Pikachu", Binary: []byte("pikachu")},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := ids[0]
	if _, err := s.DepositPokemonInstance(ctx, owner, id); err != nil {
		t.Fatal(err)
	}

	if err := s.DeletePokemonInstance(ctx, other, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting someone else's Pokémon must fail with ErrNoRows, got %v", err)
	}
	if _, err := s.PokemonInstanceByID(ctx, owner, id); err != nil {
		t.Fatalf("the wrong-owner delete attempt must not have touched the row: %v", err)
	}

	if err := s.DeletePokemonInstance(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PokemonInstanceByID(ctx, owner, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted Pokémon must be gone, got %v", err)
	}
}

// TestDepositIsIdempotent pins that depositing an already-deposited instance
// is a no-op success, not an error or a reset deposit time.
func TestDepositIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	ids, _, _, err := s.SavePokemonInstances(ctx, owner, []PokemonInstance{
		{Species: 6, SpeciesName: "Charizard", Binary: []byte("charizard")},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := ids[0]
	first, err := s.DepositPokemonInstance(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if first.HomeDepositedAt == nil {
		t.Fatal("first deposit must set home_deposited_at")
	}
	second, err := s.DepositPokemonInstance(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if !second.HomeDepositedAt.Equal(*first.HomeDepositedAt) {
		t.Fatalf("re-depositing must not move the original deposit time: %v -> %v", first.HomeDepositedAt, second.HomeDepositedAt)
	}
}

// TestMarkPokemonWithdrawnRequiresDeposit pins that withdrawal bookkeeping
// only applies to a currently-deposited instance, an instance that was
// never deposited has nothing to record.
func TestMarkPokemonWithdrawnRequiresDeposit(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	ids, _, _, err := s.SavePokemonInstances(ctx, owner, []PokemonInstance{
		{Species: 4, SpeciesName: "Charmander", Binary: []byte("charmander")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPokemonWithdrawn(ctx, owner, ids[0]); err == nil {
		t.Fatal("withdrawing a never-deposited instance must fail")
	}
}
