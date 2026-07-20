package catalog

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	d := t.TempDir()
	s, err := Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testOwner(t *testing.T, s *Store) string {
	t.Helper()
	id, err := s.EnsureDefaultUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSavesForOwnerReturnsEveryVersionNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	first, _, err := s.CreateSave(ctx, Save{
		OwnerID: owner, GameID: "game-hg", GameLabel: "Pokemon HeartGold",
		FileName: "hg.sav", SizeBytes: 524288, SHA256: "aaa", Source: SaveSourceUpload,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateSave(ctx, Save{
		OwnerID: owner, GameID: "game-hg", GameLabel: "Pokemon HeartGold",
		FileName: "hg.sav", SizeBytes: 524288, SHA256: "bbb", Source: SaveSourceUpload,
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := s.SavesForOwner(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("a second upload must not replace the first: got %d versions, want 2", len(list))
	}
	if list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("versions must be newest first: got %s,%s want %s,%s", list[0].ID, list[1].ID, second.ID, first.ID)
	}
}

func TestCreateSaveDedupsIdenticalBytesAgainstNewestVersion(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)
	save := Save{OwnerID: owner, GameID: "game-hg", GameLabel: "Pokemon HeartGold", FileName: "hg.sav", SHA256: "aaa"}

	original, created, err := s.CreateSave(ctx, save)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("the first upload of a save must create a version")
	}

	same, created, err := s.CreateSave(ctx, save)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("re-uploading identical bytes must not create a second version")
	}
	if same.ID != original.ID {
		t.Fatalf("dedup must return the existing version: got %s want %s", same.ID, original.ID)
	}
}

func TestSaveByIDRefusesAnotherOwnersSave(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)
	intruderID, err := s.CreateUser(ctx, "intruder", "hash", "user", false)
	if err != nil {
		t.Fatal(err)
	}

	mine, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", SHA256: "aaa"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.SaveByID(ctx, intruderID, mine.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("another user's save id must be indistinguishable from a missing one: got %v", err)
	}
	if _, err := s.SaveByID(ctx, owner, mine.ID); err != nil {
		t.Fatalf("the owner must be able to read their own save: %v", err)
	}
}

func TestDeleteSaveRefusesAnotherOwnersSave(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)
	intruderID, err := s.CreateUser(ctx, "intruder", "hash", "user", false)
	if err != nil {
		t.Fatal(err)
	}
	mine, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", SHA256: "aaa"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteSave(ctx, intruderID, mine.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a non-owner must not delete a save: got %v", err)
	}
	if _, err := s.SaveByID(ctx, owner, mine.ID); err != nil {
		t.Fatalf("the save must survive a non-owner's delete: %v", err)
	}
	if _, err := s.DeleteSave(ctx, owner, mine.ID); err != nil {
		t.Fatalf("the owner must be able to delete their own save: %v", err)
	}
	if _, err := s.SaveByID(ctx, owner, mine.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the save must be gone after its owner deletes it: got %v", err)
	}
}

func TestCreateSaveKeepsIdenticalBytesForDifferentGames(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	if _, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", SHA256: "aaa"}); err != nil {
		t.Fatal(err)
	}
	_, created, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-ss", SHA256: "aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("dedup is scoped to one game: an identical save for another game must still be stored")
	}
}

// TestDeleteSaveTakesItsPokemonWithIt pins the Living Dex cleanup: a deleted
// save must not leave the Pokémon it introduced behind, or the Dex still counts
// progress from a save the user removed. Pokémon from other saves survive, and
// re-uploading a remaining save can restore anything shared (the insert dedups
// on identical bytes).
func TestDeleteSaveTakesItsPokemonWithIt(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	doomed, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", GameLabel: "HG", FileName: "hg.sav", SHA256: "aaa"})
	if err != nil {
		t.Fatal(err)
	}
	kept, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-em", GameLabel: "EM", FileName: "em.sav", SHA256: "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.SavePokemonInstances(ctx, owner, []PokemonInstance{
		{Species: 25, SpeciesName: "Pikachu", OriginSaveID: doomed.ID, Binary: []byte("pika")},
		{Species: 1, SpeciesName: "Bulbasaur", OriginSaveID: kept.ID, Binary: []byte("bulba")},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteSave(ctx, owner, doomed.ID); err != nil {
		t.Fatal(err)
	}

	mons, err := s.PokemonInstances(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 1 || mons[0].Species != 1 {
		t.Fatalf("deleting a save must remove exactly its own Pokémon: %#v", mons)
	}
}

// TestDeleteSaveTakesItsTrainerDataWithIt pins the fix for QA item 1: deleting
// the save behind a trainer_profiles row must also remove that profile, its
// Hall of Fame snapshot, and its per-trainer game_dex rows, otherwise the
// Trainers tab kept showing a playthrough whose only save was gone. The merged
// trainer='' game_dex rows (the sticky cross-playthrough aggregate) survive.
func TestDeleteSaveTakesItsTrainerDataWithIt(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	doomed, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", GameLabel: "HG", FileName: "hg.sav", SHA256: "aaa"})
	if err != nil {
		t.Fatal(err)
	}
	profile := TrainerProfile{OwnerID: owner, Game: "HG", TID: 1, SID: 2, OT: "ASH", PlayedMinutes: 600, SourceSaveID: doomed.ID}
	if err := s.UpsertTrainerProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTrainerHOF(ctx, profile, []HOFEntry{{EntryIndex: 0, Slot: 0, Species: 6, Level: 60}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGameDex(ctx, owner, "HG", profile.TrainerKey(), []int{1, 4}, []int{1}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteSave(ctx, owner, doomed.ID); err != nil {
		t.Fatal(err)
	}

	if _, found, err := s.TrainerProfileByID(ctx, owner, profile.ID()); err != nil || found {
		t.Fatalf("the profile must not outlive its only save: found=%v err=%v", found, err)
	}
	if hof, err := s.TrainerHOF(ctx, owner, profile.ID()); err != nil || len(hof) != 0 {
		t.Fatalf("the Hall of Fame must go with its profile: %#v (err %v)", hof, err)
	}
	if perTrainer, err := s.GameDexCaughtFor(ctx, owner, "HG", profile.TrainerKey()); err != nil || len(perTrainer) != 0 {
		t.Fatalf("the profile's game_dex rows must go with it: %#v (err %v)", perTrainer, err)
	}
	merged, err := s.GameDexCaught(ctx, owner, "HG")
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 0 {
		t.Fatalf("the merged dex is computed live across trainers, so it must drop with its only contributor: %#v", merged)
	}
}

// TestDeleteSaveLeavesASupersedingProfileAlone pins the other half of the fix:
// deleting an old save that a newer, higher-play-time save has already
// superseded must not touch the profile the newer save now owns.
func TestDeleteSaveLeavesASupersedingProfileAlone(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := testOwner(t, s)

	older, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", GameLabel: "HG", FileName: "hg1.sav", SHA256: "aaa"})
	if err != nil {
		t.Fatal(err)
	}
	newer, _, err := s.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", GameLabel: "HG", FileName: "hg2.sav", SHA256: "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTrainerProfile(ctx, TrainerProfile{OwnerID: owner, Game: "HG", TID: 1, SID: 2, OT: "ASH", PlayedMinutes: 5, SourceSaveID: older.ID}); err != nil {
		t.Fatal(err)
	}
	profile := TrainerProfile{OwnerID: owner, Game: "HG", TID: 1, SID: 2, OT: "ASH", PlayedMinutes: 600, SourceSaveID: newer.ID}
	if err := s.UpsertTrainerProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteSave(ctx, owner, older.ID); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.TrainerProfileByID(ctx, owner, profile.ID())
	if err != nil || !found {
		t.Fatalf("the profile a live save still owns must survive: found=%v err=%v", found, err)
	}
	if got.PlayedMinutes != 600 {
		t.Fatalf("the surviving profile must still be the newer save's data: %#v", got)
	}
}
