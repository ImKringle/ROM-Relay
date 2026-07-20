package web

import (
	"context"
	"net/http"
	"os"
	"testing"
)

func TestAdminDeletingAROMRemovesTheFileAndTheOrphanedGame(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")
	ctx := context.Background()

	games, err := f.store.Games(ctx)
	if err != nil || len(games) != 1 || len(games[0].Artifacts) != 1 {
		t.Fatalf("fixture needs exactly one game with one artifact: %v", err)
	}
	artifact := games[0].Artifacts[0]
	if _, err := os.Stat(artifact.Path); err != nil {
		t.Fatalf("the ROM file should exist before deletion: %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, f.server.URL+"/api/admin/roms/"+artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CSRF-Token", f.ui.csrf)
	res, err := admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an admin must be able to delete a ROM: got %d", res.StatusCode)
	}

	if _, err := os.Stat(artifact.Path); !os.IsNotExist(err) {
		t.Fatal("deleting a ROM must remove the file from disk")
	}
	remaining, err := f.store.Games(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("the game had only this artifact and should be gone too: got %v", remaining)
	}
}

func TestDeletingAROMRequiresAdmin(t *testing.T) {
	f := newSaveVaultFixture(t)
	plain := f.signIn(t, "ash")
	ctx := context.Background()

	games, err := f.store.Games(ctx)
	if err != nil || len(games) != 1 || len(games[0].Artifacts) != 1 {
		t.Fatalf("fixture needs exactly one game with one artifact: %v", err)
	}
	artifact := games[0].Artifacts[0]

	req, err := http.NewRequest(http.MethodDelete, f.server.URL+"/api/admin/roms/"+artifact.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CSRF-Token", f.ui.csrf)
	res, err := plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a plain user must not delete a ROM: got %d", res.StatusCode)
	}
	if _, err := os.Stat(artifact.Path); err != nil {
		t.Fatal("a refused delete must leave the file in place")
	}
}
