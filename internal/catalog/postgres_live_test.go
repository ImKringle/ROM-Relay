package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPostgresLive exercises the Postgres backend end-to-end against a real
// server. It runs only when ROMRELAY_TEST_POSTGRES_DSN is set (e.g.
// postgres://postgres:romrelay@localhost:55432/romrelay?sslmode=disable), so the
// normal test suite is unaffected. Each run uses a unique root so repeated runs
// against the same database stay independent.
func TestPostgresLive(t *testing.T) {
	dsn := os.Getenv("ROMRELAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ROMRELAY_TEST_POSTGRES_DSN to run the live Postgres test")
	}
	root := t.TempDir()
	store, err := Open(dsn, root)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()
	if !store.dialect.postgres {
		t.Fatal("expected the postgres dialect to be selected")
	}
	ctx := context.Background()

	// Place one ROM so Reconcile indexes a game + artifact (exercises the games
	// and artifacts upserts, including the large UnixNano mod_time BIGINT).
	gbDir := filepath.Join(root, "gb")
	if err := os.MkdirAll(gbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	romPath := filepath.Join(gbDir, "unique-live-rom.gb")
	if err := os.WriteFile(romPath, []byte("ROMRELAY-POSTGRES-LIVE-TEST-PAYLOAD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	art, err := store.ArtifactByPath(ctx, mustAbs(t, romPath))
	if err != nil {
		t.Fatalf("artifact by path: %v", err)
	}
	if art.Platform != "gb" || art.ModTime <= 0 {
		t.Fatalf("unexpected artifact: platform=%q mod_time=%d", art.Platform, art.ModTime)
	}
	games, err := store.Games(ctx)
	if err != nil {
		t.Fatalf("games: %v", err)
	}
	found := false
	for _, g := range games {
		if g.Platform == "gb" {
			found = true
		}
	}
	if !found {
		t.Fatal("reconciled gb game not returned by Games()")
	}

	// Scheduler state round-trip (INSERT ... ON CONFLICT upsert).
	stateName := "live_test_state_" + filepath.Base(root)
	when := time.Unix(1_700_000_000, 0)
	if err := store.SetSchedulerState(ctx, stateName, when, "complete"); err != nil {
		t.Fatalf("set scheduler state: %v", err)
	}
	if err := store.SetSchedulerState(ctx, stateName, when.Add(time.Hour), "again"); err != nil {
		t.Fatalf("upsert scheduler state: %v", err)
	}
	gotWhen, gotStatus, err := store.SchedulerState(ctx, stateName)
	if err != nil || gotStatus != "again" || !gotWhen.Equal(when.Add(time.Hour)) {
		t.Fatalf("scheduler round-trip: when=%v status=%q err=%v", gotWhen, gotStatus, err)
	}

	// Patch job round-trip (many columns, booleans, nullable expires_at).
	job := PatchJobRecord{
		ID:               "live-job-" + filepath.Base(root),
		Status:           "completed",
		ArtifactID:       art.ID,
		OutputName:       "out.gb",
		Retain:           true,
		UnverifiedSource: false,
		Format:           "ips",
		CreatedAt:        time.Unix(1_700_000_100, 0),
		UpdatedAt:        time.Unix(1_700_000_200, 0),
	}
	if err := store.SavePatchJob(ctx, job); err != nil {
		t.Fatalf("save patch job: %v", err)
	}
	got, err := store.PatchJob(ctx, job.ID)
	if err != nil || got.Status != "completed" || !got.Retain || got.Format != "ips" {
		t.Fatalf("patch job round-trip: %+v err=%v", got, err)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
