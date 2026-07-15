package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloseCheckpointsAndReleasesSQLiteFiles(t *testing.T) {
	d := t.TempDir()
	db := filepath.Join(d, "catalog.db")
	s, err := Open(db, filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureDirectories(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(d, "catalog-renamed.db")
	if err = os.Rename(db, renamed); err != nil {
		t.Fatalf("database handle remains active after Close: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		p := db + suffix
		if err = os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("sidecar remains locked: %s: %v", p, err)
		}
	}
}

func TestExternalArtifactRetentionLifecycle(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	external := filepath.Join(root, "external", "antisocial-expert")
	s, err := Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddExternalArtifactRoot(external)
	now := time.Unix(2_100_000_000, 0)
	s.SetClock(func() time.Time { return now })
	p := filepath.Join(external, "gba", "job", "remote.gba")
	os.MkdirAll(filepath.Dir(p), 0755)
	os.WriteFile(p, make([]byte, 32), 0600)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, err := s.ArtifactByPath(context.Background(), p)
	if err != nil || !a.External || a.Patched {
		t.Fatalf("external classification: %#v %v", a, err)
	}
	if err = s.MarkArtifactRetention(context.Background(), p, false, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	deleted, _, failures := s.CleanupExpired(context.Background())
	if deleted != 1 || len(failures) > 0 {
		t.Fatalf("cleanup=%d %v", deleted, failures)
	}
	if _, err = os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired external file remains")
	}
}

func TestReconcileParsesHashesAndPrunes(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	db := filepath.Join(d, "catalog.db")
	s, err := Open(db, root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.EnsureDirectories(); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 0x200)
	copy(b[0x134:], []byte("LEGAL TEST"))
	b[0x143] = 0x80
	b[0x147] = 1
	p := filepath.Join(root, "gb", "nested", "test.gb")
	if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, err := s.Games(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].Title != "LEGAL TEST" || len(games[0].Artifacts) != 1 || len(games[0].Artifacts[0].SHA256) != 64 {
		t.Fatalf("unexpected catalog: %+v", games)
	}
	if err = os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ = s.Games(context.Background())
	if len(games) != 0 {
		t.Fatalf("stale game remains: %+v", games)
	}
}

func TestReconcileSkipsPlatformPatchAndPatchedTreesAndPrunesLegacyRows(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	paths := []string{
		filepath.Join(root, "gba", "patches", "emerald-extended-cut", "Pokemon Emerald Patch Reference.gba"),
		filepath.Join(root, "gba", "patched", "Pokemon Emerald Extended Cut.gba"),
	}
	// Seed both legacy bad states directly, then prove reconciliation prunes
	// them while leaving the recipe/reference files on disk.
	for _, patchROM := range paths {
		if err = os.MkdirAll(filepath.Dir(patchROM), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(patchROM, make([]byte, 0x200), 0600); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(patchROM)
		a, fingerprintErr := fingerprint(context.Background(), patchROM, Platforms[2], st)
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		if err = s.upsert(a); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, err := s.Games(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 0 {
		t.Fatalf("patch-tree ROM leaked into catalog: %#v", games)
	}
	for _, patchROM := range paths {
		if _, err = os.Stat(patchROM); err != nil {
			t.Fatalf("reserved-tree file was removed: %v", err)
		}
	}
}

func TestManualMetadataIsLockedAgainstProviders(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rom := filepath.Join(root, "gba", "Manual Game.gba")
	os.MkdirAll(filepath.Dir(rom), 0755)
	os.WriteFile(rom, make([]byte, 0x200), 0600)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	g := games[0]
	g.Title, g.Description, g.Developer = "My Display Name", "My synopsis", "Me"
	if err = s.UpdateManualGameMetadata(context.Background(), g, time.Now()); err != nil {
		t.Fatal(err)
	}
	if candidates, _ := s.MetadataCandidates(context.Background(), time.Now().Add(time.Hour)); len(candidates) != 0 {
		t.Fatalf("manual game entered Libretro queue: %#v", candidates)
	}
	if candidates, _ := s.IGDBCandidates(context.Background(), time.Now().Add(time.Hour)); len(candidates) != 0 {
		t.Fatalf("manual game entered IGDB queue: %#v", candidates)
	}
	g.Description, g.MetadataSource = "provider overwrite", "igdb"
	if err = s.UpdateGameMetadata(context.Background(), g, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Game(context.Background(), g.ID)
	if got.Description != "My synopsis" || got.MetadataSource != "manual" {
		t.Fatalf("provider overwrote manual metadata: %#v", got)
	}
}

func TestRegionFromFilenameIsConservative(t *testing.T) {
	for name, want := range map[string]string{
		"Pokemon X (World) (Legit CIA).cia": "World",
		"Pokemon Yellow (UE) [!].gbc":       "USA, Europe",
		"Game (USA).gba":                    "USA",
		"Game (Japan).nds":                  "Japan",
		"Unlabeled.gba":                     "",
	} {
		if got := regionFromFilename(name); got != want {
			t.Errorf("regionFromFilename(%q)=%q want %q", name, got, want)
		}
	}
}

func TestReconcileBackfillsRegionWithoutRehashing(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rom := filepath.Join(root, "gba", "Cached Game (World).gba")
	if err = os.MkdirAll(filepath.Dir(rom), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(rom, make([]byte, 0x200), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE games SET region=''`); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	if len(games) != 1 || games[0].Region != "World" {
		t.Fatalf("region was not backfilled from cached artifact: %#v", games)
	}
	games[0].Region = ""
	if err = s.UpdateManualGameMetadata(context.Background(), games[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ = s.Games(context.Background())
	if games[0].Region != "" {
		t.Fatalf("cached inference overwrote manual empty region: %#v", games[0])
	}
}

func TestRuntimeStatsSeparatePatchedAndCIAEncryption(t *testing.T) {
	d := t.TempDir()
	root, patched := filepath.Join(d, "platforms"), filepath.Join(d, "patched")
	s, err := Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(patched)
	files := []string{
		filepath.Join(root, "3ds", "cia", "encrypted", "one.cia"),
		filepath.Join(root, "3ds", "cia", "unencrypted", "two.cia"),
		filepath.Join(patched, "gba", "hack.gba"),
	}
	for _, path := range files {
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, make([]byte, 0x400), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.activeMu.Lock()
	s.active["download"] = 2
	s.activeMu.Unlock()
	stats, err := s.RuntimeStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Games != 3 || stats.Files != 3 || stats.ActiveDownloads != 2 || !stats.WAL {
		t.Fatalf("unexpected totals: %#v", stats)
	}
	byPlatform := map[string]PlatformRuntimeStats{}
	for _, row := range stats.Platforms {
		byPlatform[row.Platform] = row
	}
	if got := byPlatform["3ds"]; got.EncryptedCIA != 1 || got.UnencryptedCIA != 1 || got.Base != 2 {
		t.Fatalf("unexpected 3DS stats: %#v", got)
	}
	if got := byPlatform["gba"]; got.Patched != 1 || got.Base != 0 {
		t.Fatalf("unexpected GBA stats: %#v", got)
	}
}

func TestPatchedRootCreatesDistinctGuardedGame(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	patched := filepath.Join(d, "patched")
	s, err := Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(patched)
	for _, base := range []string{root, patched} {
		p := filepath.Join(base, "gba", "same.gba")
		if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, make([]byte, 0x200), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, err := s.Games(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("want distinct games: %+v", games)
	}
	patchedCount := 0
	for _, g := range games {
		if g.ROMPatch {
			patchedCount++
		}
	}
	if patchedCount != 1 {
		t.Fatalf("patched flags: %+v", games)
	}
	candidates, err := s.MetadataCandidates(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ROMPatch {
		t.Fatalf("provider guard failed: %+v", candidates)
	}
}

func TestPlatformRegistry(t *testing.T) {
	root := filepath.Join("x", "platforms")
	cases := map[string]string{"gb/a.gb": "gb", "gbc/a.gbc": "gbc", "gba/a.gba": "gba", "nds/a.ds": "nds", "3ds/cia/a.cia": "3ds", "nes/a.nes": "nes", "snes/a.sfc": "snes", "n64/a.z64": "n64", "genesis/a.md": "genesis"}
	for rel, want := range cases {
		got, ok := DetectPlatform(root, filepath.Join(root, filepath.FromSlash(rel)))
		if !ok || got.ID != want {
			t.Errorf("%s: %+v %v", rel, got, ok)
		}
	}
}

func Test3DSCIAEncryptionFoldersAndClassification(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.EnsureDirectories(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"encrypted", "unencrypted"} {
		if st, e := os.Stat(filepath.Join(root, "3ds", "cia", dir)); e != nil || !st.IsDir() {
			t.Fatalf("missing %s directory: %v", dir, e)
		}
	}
	paths := []string{filepath.Join(root, "3ds", "cia", "encrypted", "same.cia"), filepath.Join(root, "3ds", "cia", "unencrypted", "same.cia"), filepath.Join(root, "3ds", "cia", "legacy.cia")}
	for i, p := range paths {
		if err = os.WriteFile(p, []byte{byte(i), 1, 2, 3}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, err := s.Games(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]int{}
	mixed := false
	for _, g := range games {
		if g.Encryption == "mixed" {
			mixed = true
		}
		for _, a := range g.Artifacts {
			states[a.Encryption]++
		}
	}
	if states["encrypted"] != 2 || states["unencrypted"] != 1 || !mixed {
		t.Fatalf("classification states=%v games=%+v", states, games)
	}
}

func TestRetentionCleanupAndActiveDownloadDeferral(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	patched := filepath.Join(d, "patched")
	s, err := Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(patched)
	now := time.Unix(2_000_000_000, 0)
	s.SetClock(func() time.Time { return now })
	p := filepath.Join(patched, "gba", "temporary.gba")
	os.MkdirAll(filepath.Dir(p), 0755)
	os.WriteFile(p, make([]byte, 0x200), 0600)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	a := games[0].Artifacts[0]
	if err = s.MarkArtifactRetention(context.Background(), p, false, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	release := s.BeginDownload(a.ID)
	deleted, deferred, failures := s.CleanupExpired(context.Background())
	if deleted != 0 || deferred != 1 || len(failures) != 0 {
		t.Fatalf("active cleanup %d %d %v", deleted, deferred, failures)
	}
	release()
	deleted, deferred, failures = s.CleanupExpired(context.Background())
	if deleted != 1 || deferred != 0 || len(failures) != 0 {
		t.Fatalf("cleanup %d %d %v", deleted, deferred, failures)
	}
	if _, err = os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("file remains: %v", err)
	}
	games, _ = s.Games(context.Background())
	if len(games) != 0 {
		t.Fatalf("rows remain: %+v", games)
	}
}
