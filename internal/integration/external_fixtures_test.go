package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/patch"
	webui "romrelay/internal/web"
)

// This test intentionally contains no ROM or third-party patch bytes. Operators
// can point it at their own legal fixture tree with ROMRELAY_FIXTURE_ROOT.
func TestOperatorSuppliedROMAndPatchFixtures(t *testing.T) {
	sourceRoot := os.Getenv("ROMRELAY_FIXTURE_ROOT")
	if sourceRoot == "" {
		t.Skip("set ROMRELAY_FIXTURE_ROOT to an operator-supplied fixture tree")
	}
	d := t.TempDir()
	library := filepath.Join(d, "platforms")
	output := filepath.Join(d, "patched")
	files := map[string]string{
		"platforms/gb/Pokemon - Red Version (USA, Europe) (SGB Enhanced).gb": "gb/red.gb",
		"platforms/gbc/Pokemon - Crystal Version (UE) (V1.1) [C][!].gbc":     "gbc/crystal.gbc",
		"platforms/gba/Pkmn Emerald.gba":                                     "gba/emerald.gba",
		"platforms/gba/Pokemon - FireRed Version (USA, Europe).gba":          "gba/firered.gba",
		"platforms/nds/Pokemon - Diamond Version (USA) (Rev 5).nds":          "nds/diamond.nds",
		"patches/Mariomon Official v1.4.2.bps":                               "gba/patches/mariomon/mariomon.bps",
		"patches/Pokemon Radical Red Official v4.1.ups":                      "gba/patches/radical-red/radical-red.ups",
		"patches/Pokemon Unbound Official.ups":                               "gba/patches/unbound/unbound.ups",
	}
	for srcRel, dstRel := range files {
		src := filepath.Join(sourceRoot, filepath.FromSlash(srcRel))
		dst := filepath.Join(library, filepath.FromSlash(dstRel))
		if err := copyFixture(src, dst); err != nil {
			t.Fatalf("copy %s: %v", srcRel, err)
		}
	}
	store, err := catalog.Open(filepath.Join(d, "catalog.db"), library)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.AddArtifactRoot(output)
	if err = store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := patch.NewManager(store, patch.ManagerConfig{LibraryDirectory: library, OutputDirectory: output, ArtworkDirectory: filepath.Join(d, "art"), MaxROMBytes: 128 << 20, MaxPatchBytes: 64 << 20, Timeout: 2 * time.Minute})
	if err = m.ScanRecipes(context.Background()); err != nil {
		t.Fatal(err)
	}
	recipes, err := m.Recipes(context.Background())
	if err != nil || len(recipes) != 3 {
		t.Fatalf("recipes=%+v err=%v", recipes, err)
	}
	games, err := store.Games(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var bases []catalog.Artifact
	platforms := map[string]bool{}
	for _, g := range games {
		platforms[g.Platform] = true
		for _, a := range g.Artifacts {
			if a.Platform == "gba" {
				bases = append(bases, a)
			}
		}
	}
	for _, p := range []string{"gb", "gbc", "gba", "nds"} {
		if !platforms[p] {
			t.Fatalf("fixture platform %s not cataloged", p)
		}
	}
	for _, recipe := range recipes {
		if recipe.ValidationType != "CRC32" || recipe.ValidationValue == "" {
			t.Fatalf("recipe lacks embedded base checksum: %+v", recipe)
		}
		var base catalog.Artifact
		for _, a := range bases {
			if strings.EqualFold(a.CRC32, recipe.ValidationValue) {
				base = a
				break
			}
		}
		if base.ID == "" {
			t.Fatalf("no supplied base matches %s checksum %s", recipe.Title, recipe.ValidationValue)
		}
		patchBytes, err := os.ReadFile(recipe.Path)
		if err != nil {
			t.Fatal(err)
		}
		romBytes, err := os.ReadFile(base.Path)
		if err != nil {
			t.Fatal(err)
		}
		result, err := patch.ApplyEmbedded(context.Background(), romBytes, patchBytes, base.Name, filepath.Base(recipe.Path))
		if err != nil {
			t.Fatalf("apply %s: %v", recipe.Title, err)
		}
		if !result.HasSourceChecksum || !result.SourceChecksumMatch || len(result.Output) < 0xc0 || result.Output[0xb2] != 0x96 {
			t.Fatalf("invalid patched output for %s", recipe.Title)
		}
	}
	ui := webui.NewCatalog(store, "http://fixture.invalid", "", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), 24*time.Hour)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/games")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("games status %d", res.StatusCode)
	}
	res.Body.Close()
	var first catalog.Artifact
	for _, g := range games {
		if len(g.Artifacts) > 0 {
			first = g.Artifacts[0]
			break
		}
	}
	req, _ := http.NewRequest("GET", ts.URL+"/d/"+first.ID, nil)
	req.Header.Set("Range", "bytes=0-15")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status %d", res.StatusCode)
	}
	res.Body.Close()
	res, err = http.Get(ts.URL + "/qr/" + first.ID + ".png")
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 8)
	_, _ = io.ReadFull(res.Body, header)
	res.Body.Close()
	if string(header) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("QR route did not return PNG")
	}
}

func copyFixture(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
