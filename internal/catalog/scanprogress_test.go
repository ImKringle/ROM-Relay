package catalog

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestScanProgressFires(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "cat.db")
	store, err := Open(dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var calls atomic.Int64
	var lastFiles atomic.Int64
	store.SetScanProgress(func(files int, hashedBytes int64) {
		calls.Add(1)
		lastFiles.Store(int64(files))
	})

	gbDir := filepath.Join(root, "gb")
	if err := os.MkdirAll(gbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A couple of small ROMs are enough: the progress callback must fire at least
	// once per indexed file.
	for _, name := range []string{"a.gb", "b.gb"} {
		if err := os.WriteFile(filepath.Join(gbDir, name), []byte("romrelay-scan-progress-payload-"+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 {
		t.Fatal("scan progress callback never fired despite indexing files")
	}
	if lastFiles.Load() == 0 {
		t.Fatalf("scan progress reported 0 files; got %d calls", calls.Load())
	}
}
