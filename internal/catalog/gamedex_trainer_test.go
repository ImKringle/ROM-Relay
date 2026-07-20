package catalog

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestGameDexKeepsPlaythroughsApartAndStillMergesThem(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testOwner(t, store)

	// Two playthroughs of the same game, each with its own caught list.
	if err := store.UpsertGameDex(ctx, owner, "HG", "ASH", []int{1, 4}, []int{1}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertGameDex(ctx, owner, "HG", "MISTY", []int{7}, []int{7}); err != nil {
		t.Fatal(err)
	}

	// The per-playthrough dex is that trainer's rows only.
	ash, err := store.GameDexCaughtFor(ctx, owner, "HG", "ASH")
	if err != nil {
		t.Fatal(err)
	}
	if len(ash) != 1 || ash[0].Species != 1 {
		t.Fatalf("ASH's dex must hold only ASH's catches: %#v", ash)
	}

	// The merged per-game tab is the OR across trainers.
	merged, err := store.GameDexCaught(ctx, owner, "HG")
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 {
		t.Fatalf("the merged tab must OR the playthroughs: %#v", merged)
	}
}

// The merged tab must not double-count a species caught in two playthroughs.
func TestGameDexMergedTabCountsASpeciesOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testOwner(t, store)
	if err := store.UpsertGameDex(ctx, owner, "HG", "ASH", []int{25}, []int{25}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertGameDex(ctx, owner, "HG", "MISTY", []int{25}, []int{25}); err != nil {
		t.Fatal(err)
	}
	merged, err := store.GameDexCaught(ctx, owner, "HG")
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 {
		t.Fatalf("a species caught in two playthroughs is still one species: %#v", merged)
	}
}

// A database written before trainers existed must survive the upgrade: its rows
// migrate to the unknown trainer and still aggregate into the merged tab. This
// is the case CREATE TABLE IF NOT EXISTS cannot reach.
func TestGameDexUpgradesADatabaseThatPredatesTrainers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL DEFAULT '',role TEXT NOT NULL DEFAULT 'user',created_at INTEGER NOT NULL,disabled BOOLEAN NOT NULL DEFAULT FALSE,must_change_password BOOLEAN NOT NULL DEFAULT FALSE);
CREATE TABLE game_dex(owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,game TEXT NOT NULL,species INTEGER NOT NULL,seen BOOLEAN NOT NULL DEFAULT FALSE,caught BOOLEAN NOT NULL DEFAULT FALSE,PRIMARY KEY(owner_id,game,species));
INSERT INTO users(id,username,created_at) VALUES('u1','ash',0);
INSERT INTO game_dex(owner_id,game,species,seen,caught) VALUES('u1','HG',25,TRUE,TRUE);`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	store, err := Open(path, t.TempDir())
	if err != nil {
		t.Fatalf("opening a pre-trainer database must migrate it: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// The old row survived, under the unknown trainer.
	merged, err := store.GameDexCaught(ctx, "u1", "HG")
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || merged[0].Species != 25 {
		t.Fatalf("the pre-trainer row must survive: %#v", merged)
	}

	// And the new 4-column key works, this is what ON CONFLICT needs, and what
	// a no-op CREATE TABLE IF NOT EXISTS would have left broken.
	if err := store.UpsertGameDex(ctx, "u1", "HG", "ASH", []int{25}, []int{25}); err != nil {
		t.Fatalf("upsert against the migrated table: %v", err)
	}
}
