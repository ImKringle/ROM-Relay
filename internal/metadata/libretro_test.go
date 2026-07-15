package metadata

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"romrelay/internal/catalog"
)

func TestParseDAT(t *testing.T) {
	data := `clrmamepro ( name "Nintendo" ) game ( name "Example (USA)" region "USA" rom ( name "Example.gba" size 4 crc AABBCCDD md5 0011 sha1 2233 ) )`
	games, err := parseDAT(strings.NewReader(data))
	if err != nil || len(games) != 1 {
		t.Fatalf("parse=%v %#v", err, games)
	}
	if games[0].Name != "Example (USA)" || games[0].CRC32 != "AABBCCDD" || games[0].SHA1 != "2233" {
		t.Fatalf("unexpected game: %#v", games[0])
	}
}

func TestOfflineLibretroMatchAndPatchedGuard(t *testing.T) {
	d := t.TempDir()
	library, patched := filepath.Join(d, "platforms"), filepath.Join(d, "patched")
	store, err := catalog.Open(filepath.Join(d, "catalog.db"), library)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.AddArtifactRoot(patched)
	rom := []byte("copyright-free-libretro-fixture")
	for _, path := range []string{filepath.Join(library, "gba", "local.gba"), filepath.Join(patched, "gba", "patched.gba")} {
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, rom, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Different container bytes but an exact canonical release filename exercise
	// the post-hash fallback used for converted 3DS and header-shortened ROMs.
	if err = os.WriteFile(filepath.Join(library, "gba", "Fixture Adventure (USA).gba"), make([]byte, 0x200), 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	h1, hm := sha1.Sum(rom), md5.Sum(rom)
	dat := fmt.Sprintf(`clrmamepro ( name "Nintendo - Game Boy Advance" ) game ( name "Fixture Adventure (USA)" region "USA" rom ( name "Fixture.gba" size %d crc %08X md5 %s sha1 %s ) )`, len(rom), crc32.ChecksumIEEE(rom), strings.ToUpper(hex.EncodeToString(hm[:])), strings.ToUpper(hex.EncodeToString(h1[:])))
	// Real Libretro DATs occasionally contain byte-identical duplicate rows.
	dat += strings.TrimPrefix(dat, `clrmamepro ( name "Nintendo - Game Boy Advance" )`)
	dbDir := filepath.Join(d, "metadata")
	base := filepath.Join(dbDir, "gba", "no-intro.dat")
	if err = os.MkdirAll(filepath.Dir(base), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(base, []byte(dat), 0600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"developer.dat":   `game ( name "Fixture Adventure (USA)" developer "Fixture Studio" )`,
		"publisher.dat":   `game ( name "Fixture Adventure (USA)" publisher "Fixture Press" )`,
		"genre.dat":       `game ( name "Fixture Adventure (USA)" genre "Adventure" )`,
		"releaseyear.dat": `game ( name "Fixture Adventure (USA)" releaseyear "2026" )`,
	} {
		if err = os.WriteFile(filepath.Join(dbDir, "gba", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	l := NewLibretro(store, LibretroOptions{DatabaseDirectory: dbDir, ThumbnailDirectory: filepath.Join(d, "thumbs")})
	platforms, records, syncErr := l.Sync(context.Background())
	if syncErr != nil {
		t.Fatal(syncErr)
	}
	if platforms != 1 || records != 1 {
		t.Fatalf("sync=%d/%d %v", platforms, records, syncErr)
	}
	matched, err := l.Enrich(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if matched != 2 {
		t.Fatalf("matched=%d, patched provider guard failed", matched)
	}
	if err = store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := store.Games(context.Background())
	for _, g := range games {
		if g.ROMPatch {
			if g.Title == "Fixture Adventure (USA)" {
				t.Fatal("patched game received provider metadata")
			}
			continue
		}
		if g.Title != "Fixture Adventure (USA)" || g.Developer != "Fixture Studio" || g.Genre != "Adventure" || g.ReleaseDate != "2026" {
			t.Fatalf("metadata not applied: %#v", g)
		}
	}
}
