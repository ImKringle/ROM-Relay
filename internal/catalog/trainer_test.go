package catalog

import (
	"context"
	"testing"
)

// A restarted game is a second playthrough, not an overwrite: the old run's
// money, badges and Hall of Fame have to survive it.
func TestASecondPlaythroughOfTheSameGameIsASecondProfile(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testOwner(t, store)

	first := TrainerProfile{OwnerID: owner, Game: "HG", TID: 1, SID: 2, OT: "ASH", Money: 5000, PlayedMinutes: 600}
	second := TrainerProfile{OwnerID: owner, Game: "HG", TID: 9, SID: 9, OT: "NEW", Money: 100, PlayedMinutes: 10}
	if err := store.UpsertTrainerProfile(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTrainerProfile(ctx, second); err != nil {
		t.Fatal(err)
	}

	list, err := store.TrainerProfiles(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("a restart must not overwrite the first run: %#v", list)
	}
}

// Play time only rises within a playthrough, so it, not upload order, decides
// which save's numbers a profile keeps. Restoring an old backup and re-uploading
// it must not roll the profile backwards.
func TestALowerPlayTimeSaveDoesNotOverwriteAProfile(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testOwner(t, store)

	played := TrainerProfile{OwnerID: owner, Game: "HG", TID: 1, SID: 2, OT: "ASH", Money: 5000, PlayedMinutes: 600}
	if err := store.UpsertTrainerProfile(ctx, played); err != nil {
		t.Fatal(err)
	}
	// An older backup of the same playthrough, uploaded later.
	backup := TrainerProfile{OwnerID: owner, Game: "HG", TID: 1, SID: 2, OT: "ASH", Money: 1, PlayedMinutes: 5}
	if err := store.UpsertTrainerProfile(ctx, backup); err != nil {
		t.Fatal(err)
	}

	list, err := store.TrainerProfiles(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("same playthrough must stay one profile: %#v", list)
	}
	if list[0].Money != 5000 || list[0].PlayedMinutes != 600 {
		t.Fatalf("the higher play time must win, not the later upload: %#v", list[0])
	}

	// And a genuinely newer save does advance it.
	newer := TrainerProfile{OwnerID: owner, Game: "HG", TID: 1, SID: 2, OT: "ASH", Money: 9000, PlayedMinutes: 900}
	if err := store.UpsertTrainerProfile(ctx, newer); err != nil {
		t.Fatal(err)
	}
	list, err = store.TrainerProfiles(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Money != 9000 {
		t.Fatalf("a higher play time must advance the profile: %#v", list[0])
	}
}

// The Hall of Fame is a snapshot: replacing it swaps the whole set, and the
// profile handle round-trips through ID() / TrainerProfileByID.
func TestTrainerHOFIsASnapshotAndTheIDRoundTrips(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testOwner(t, store)

	p := TrainerProfile{OwnerID: owner, Game: "HG", TID: 11031, SID: 55664, OT: "Dylan", PlayedMinutes: 100, HOFClears: intPtr(3)}
	if err := store.UpsertTrainerProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceTrainerHOF(ctx, p, []HOFEntry{{EntryIndex: 0, Slot: 0, Species: 160, Level: 48}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceTrainerHOF(ctx, p, []HOFEntry{{EntryIndex: 0, Slot: 0, Species: 6, Level: 60}}); err != nil {
		t.Fatal(err)
	}

	got, found, err := store.TrainerProfileByID(ctx, owner, p.ID())
	if err != nil || !found {
		t.Fatalf("the derived ID must resolve its own profile: %v %v", found, err)
	}
	if got.HOFClears == nil || *got.HOFClears != 3 {
		t.Fatalf("clears must survive storage: %#v", got.HOFClears)
	}
	hof, err := store.TrainerHOF(ctx, owner, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(hof) != 1 || hof[0].Species != 6 {
		t.Fatalf("a replaced Hall of Fame must not blend with the old one: %#v", hof)
	}

	// Another owner's profile is not found rather than forbidden.
	if _, found, err := store.TrainerProfileByID(ctx, "someone-else", p.ID()); err != nil || found {
		t.Fatalf("another owner must not see this profile: %v %v", found, err)
	}
}

// TestClearTrainerDexOnlyTouchesSelectedPlaythroughs pins the Flow Audit fix
// for QA item 3: the admin Player Data page's "Clear Associated Dex Data"
// action must scope to the trainer ids it was given, leaving every other
// playthrough, and the account's saves, alone.
func TestClearTrainerDexOnlyTouchesSelectedPlaythroughs(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testOwner(t, store)

	doomed := TrainerProfile{OwnerID: owner, Game: "HG", TID: 1, SID: 2, OT: "ASH", PlayedMinutes: 600}
	spared := TrainerProfile{OwnerID: owner, Game: "HG", TID: 9, SID: 9, OT: "NEW", PlayedMinutes: 10}
	if err := store.UpsertTrainerProfile(ctx, doomed); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTrainerProfile(ctx, spared); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceTrainerHOF(ctx, doomed, []HOFEntry{{EntryIndex: 0, Slot: 0, Species: 6, Level: 60}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertGameDex(ctx, owner, "HG", doomed.TrainerKey(), []int{1, 4}, []int{1}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertGameDex(ctx, owner, "HG", spared.TrainerKey(), []int{7}, []int{7}); err != nil {
		t.Fatal(err)
	}
	save, _, err := store.CreateSave(ctx, Save{OwnerID: owner, GameID: "game-hg", GameLabel: "HG", FileName: "hg.sav", SHA256: "aaa"})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ClearTrainerDex(ctx, owner, []string{doomed.ID()}); err != nil {
		t.Fatal(err)
	}

	if _, found, err := store.TrainerProfileByID(ctx, owner, doomed.ID()); err != nil || found {
		t.Fatalf("the selected profile must be gone: found=%v err=%v", found, err)
	}
	if hof, err := store.TrainerHOF(ctx, owner, doomed.ID()); err != nil || len(hof) != 0 {
		t.Fatalf("its Hall of Fame must go with it: %#v (err %v)", hof, err)
	}
	if rows, err := store.GameDexCaughtFor(ctx, owner, "HG", doomed.TrainerKey()); err != nil || len(rows) != 0 {
		t.Fatalf("its game_dex rows must go with it: %#v (err %v)", rows, err)
	}

	if got, found, err := store.TrainerProfileByID(ctx, owner, spared.ID()); err != nil || !found || got.PlayedMinutes != 10 {
		t.Fatalf("an unselected profile must survive untouched: found=%v got=%#v err=%v", found, got, err)
	}
	if rows, err := store.GameDexCaughtFor(ctx, owner, "HG", spared.TrainerKey()); err != nil || len(rows) != 1 {
		t.Fatalf("an unselected profile's game_dex rows must survive: %#v (err %v)", rows, err)
	}
	if _, err := store.SaveByID(ctx, owner, save.ID); err != nil {
		t.Fatalf("saves must never be touched by this action: %v", err)
	}
}

func intPtr(n int) *int { return &n }
