package patch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"romrelay/internal/catalog"
)

func TestInspectUPSAndBPSReadsNativeFooter(t *testing.T) {
	for _, tc := range []struct{ ext, magic string }{{".ups", "UPS1"}, {".bps", "BPS1"}} {
		t.Run(tc.ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "large"+tc.ext)
			b := make([]byte, 1<<20)
			copy(b, tc.magic)
			binary.LittleEndian.PutUint32(b[len(b)-12:], 0x1234abcd)
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := inspectRecipe(context.Background(), path, filepath.Base(path), int64(len(b)), int64(len(b)))
			if err != nil {
				t.Fatal(err)
			}
			if got.ValidationType != "CRC32" || got.ValidationValue != "1234abcd" {
				t.Fatalf("inspection=%+v", got)
			}
		})
	}
}

func TestRecipeIndexStableIDBaseMatchAndResultLink(t *testing.T) {
	d := t.TempDir()
	library, output := filepath.Join(d, "platforms"), filepath.Join(d, "patched")
	store, err := catalog.Open(filepath.Join(d, "catalog.db"), library)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.AddArtifactRoot(output)
	base := make([]byte, 0x200)
	copy(base[0xa0:], []byte("RECIPE BASE"))
	basePath := filepath.Join(library, "gba", "base.gba")
	if err = os.MkdirAll(filepath.Dir(basePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(basePath, base, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(base)
	recipeDir := filepath.Join(library, "gba", "patches", "tiny-hack")
	if err = os.MkdirAll(recipeDir, 0755); err != nil {
		t.Fatal(err)
	}
	ips := []byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 1, 'X', 'E', 'O', 'F'}
	if err = os.WriteFile(filepath.Join(recipeDir, "tiny.ips"), ips, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"title":"Tiny Hack","patch":"tiny.ips","output_filename":"tiny-hack.gba","base":{"sha256":"%s"}}`, hex.EncodeToString(sum[:]))
	if err = os.WriteFile(filepath.Join(recipeDir, "recipe.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := NewManager(store, ManagerConfig{LibraryDirectory: library, OutputDirectory: output, ArtworkDirectory: filepath.Join(d, "art"), MaxROMBytes: 1 << 20, MaxPatchBytes: 1 << 20, Timeout: time.Second})
	if err = m.ScanRecipes(context.Background()); err != nil {
		t.Fatal(err)
	}
	recipes, err := m.Recipes(context.Background())
	if err != nil || len(recipes) != 1 {
		t.Fatalf("recipes=%+v err=%v", recipes, err)
	}
	firstID := recipes[0].ID
	if err = m.ScanRecipes(context.Background()); err != nil {
		t.Fatal(err)
	}
	recipes, _ = m.Recipes(context.Background())
	if recipes[0].ID != firstID {
		t.Fatal("content-derived recipe ID changed")
	}
	games, _ := store.Games(context.Background())
	var baseArtifact catalog.Artifact
	for _, g := range games {
		for _, a := range g.Artifacts {
			if a.Path == basePath {
				baseArtifact = a
			}
		}
	}
	job, err := m.Create(context.Background(), CreateOptions{ArtifactID: baseArtifact.ID, PatchID: firstID, Retain: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for job.Status == "queued" || job.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatal("job timeout")
		}
		time.Sleep(10 * time.Millisecond)
		job, err = m.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if job.Status != "completed" || job.PatchFileID != firstID {
		t.Fatalf("job=%+v", job)
	}
	a, err := store.Artifact(context.Background(), job.ResultArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.Game(context.Background(), a.GameID)
	if err != nil || g.PatchFileID != firstID || g.Title != "Tiny Hack" {
		t.Fatalf("game=%+v err=%v", g, err)
	}
	wrong := append([]byte(nil), base...)
	wrong[0] = 1
	wrongPath := filepath.Join(library, "gba", "wrong.gba")
	if err = os.WriteFile(wrongPath, wrong, 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	wa, err := store.ArtifactByPath(context.Background(), wrongPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Create(context.Background(), CreateOptions{ArtifactID: wa.ID, PatchID: firstID}); err == nil {
		t.Fatal("recipe accepted wrong base ROM")
	}
}

func TestTemporaryPatchInboxPrunesButRecipesRemain(t *testing.T) {
	d := t.TempDir()
	inbox := filepath.Join(d, "patches")
	library := filepath.Join(d, "platforms")
	old := filepath.Join(inbox, "old.ips")
	recipe := filepath.Join(library, "gba", "patches", "kept", "kept.ips")
	for _, p := range []string{old, recipe} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("PATCH000EOF"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.Open(filepath.Join(d, "db.sqlite"), library)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := NewManager(store, ManagerConfig{PatchDirectory: inbox, LibraryDirectory: library, Retention: 24 * time.Hour, MaxPatchBytes: 1 << 20})
	deleted, failures := m.CleanupTemporaryPatches(now)
	if deleted != 1 || len(failures) != 0 {
		t.Fatalf("cleanup=%d %v", deleted, failures)
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old inbox patch remains")
	}
	if _, err = os.Stat(recipe); err != nil {
		t.Fatal("permanent recipe was removed")
	}
}
