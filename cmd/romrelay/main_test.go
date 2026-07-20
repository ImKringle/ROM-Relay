package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"romrelay/internal/config"
	"romrelay/internal/presentation"
)

func TestFixedBoot9Discovery(t *testing.T) {
	d := t.TempDir()
	if got := fixedBoot9(d); got != "" {
		t.Fatalf("unexpected %q", got)
	}
	protected := filepath.Join(d, "boot9_prot.bin")
	os.WriteFile(protected, []byte("x"), 0600)
	if got := fixedBoot9(d); got != protected {
		t.Fatalf("got %q", got)
	}
	full := filepath.Join(d, "boot9.bin")
	os.WriteFile(full, []byte("y"), 0600)
	if got := fixedBoot9(d); got != full {
		t.Fatalf("full dump should take precedence: %q", got)
	}
}

// fixture3DS builds a minimal unencrypted, icon-bearing 3DS ROM, the same
// byte layout internal/converter's own tests use for their unencrypted
// cases, good enough for converter.Convert to accept it.
func fixture3DS(t *testing.T, dir, name string) string {
	t.Helper()
	const mediaUnit = 0x200
	gameOff, gameSize := 0x4000, 0x5000
	b := make([]byte, gameOff+gameSize)
	copy(b[0x100:], "NCSD")
	binary.LittleEndian.PutUint32(b[0x120:], uint32(gameOff/mediaUnit))
	binary.LittleEndian.PutUint32(b[0x124:], uint32(gameSize/mediaUnit))
	h := b[gameOff : gameOff+0x200]
	copy(h[0x100:], "NCCH")
	binary.LittleEndian.PutUint32(h[0x1a0:], 4)
	h[0x18f] = 4 // flags&4 != 0 => unencrypted
	ext := b[gameOff+0x200 : gameOff+0x600]
	for i := range ext {
		ext[i] = byte(i * 7)
	}
	sum := sha256.Sum256(ext)
	copy(h[0x160:], sum[:])
	copy(b[gameOff+0x800:], "icon")
	icon := b[gameOff+0xa00 : gameOff+0xa00+0x36c0]
	for i := range icon {
		icon[i] = byte(i * 11)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// convertV2's queue is a worker pool (matching catalog.HashPending and
// pokedex.Ingest.runParallel), not a sequential walk, so this pins the two
// things that pool must still get right: every queued ROM is converted
// exactly once, and the ready callback (which drives reconcile+hash) fires
// once per file even though several workers call it concurrently.
func TestConvertV2ConvertsQueueConcurrently(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "3ds", "3ds")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	const n = 6
	for i := range n {
		fixture3DS(t, sourceDir, fmt.Sprintf("game%d.3ds", i))
	}
	cfg := config.Config{LibraryDirectory: root}
	var out bytes.Buffer
	present := presentation.New("default", &out, &out)
	var readyCount atomic.Int32
	err := convertV2(context.Background(), cfg, "", present,
		func(name, part string, done, total int64) {},
		func(path string) { readyCount.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if got := readyCount.Load(); got != n {
		t.Fatalf("ready must fire exactly once per file: got %d, want %d", got, n)
	}
	entries, err := os.ReadDir(filepath.Join(root, "3ds", "cia", "encrypted"))
	if err != nil {
		t.Fatal(err)
	}
	cias := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".cia" {
			cias++
		}
	}
	if cias != n {
		t.Fatalf("expected %d converted CIAs, got %d among %v", n, cias, entries)
	}
}
