package torrent

import (
	"os"
	"path/filepath"
	"testing"
)

// testdata/sample.7z: "Sample Game/game.gba" (280 bytes) + a readme.
func TestInspectAndExtract7z(t *testing.T) {
	if !IsArchive("x.7Z") || !IsArchive("x.zip") || IsArchive("x.gba") {
		t.Fatal("IsArchive")
	}
	entries, clean, err := Inspect("testdata/sample.7z")
	if err != nil || !clean || len(entries) != 2 {
		t.Fatalf("Inspect: %v clean=%v %+v", err, clean, entries)
	}
	lib := t.TempDir()
	written, err := Extract("testdata/sample.7z", lib)
	if err != nil || len(written) != 1 {
		t.Fatalf("Extract: %v %v", err, written)
	}
	b, err := os.ReadFile(filepath.Join(lib, "gba", "game.gba"))
	if err != nil || len(b) != 280 {
		t.Fatalf("extracted ROM: %v, %d bytes", err, len(b))
	}
}
