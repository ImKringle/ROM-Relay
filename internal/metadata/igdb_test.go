package metadata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"romrelay/internal/catalog"
)

func TestIGDBExactMatchAndPatchedGuard(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("client_secret") != "private" {
				t.Error("OAuth secret missing")
			}
			fmt.Fprint(w, `{"access_token":"test-token","expires_in":3600}`)
		case "/games":
			b := make([]byte, r.ContentLength)
			r.Body.Read(b)
			if r.Header.Get("Authorization") != "Bearer test-token" || !strings.Contains(strings.ToLower(string(b)), `slug = ("exact-game")`) {
				t.Error("bad IGDB request")
			}
			fmt.Fprint(w, `[{"name":"Exact Game","slug":"exact-game","summary":"A fixture description.","first_release_date":946684800,"rating":88.5,"genres":[{"name":"Adventure"}],"involved_companies":[{"developer":true,"company":{"name":"Fixture Studio"}}]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	d := t.TempDir()
	root, patched := filepath.Join(d, "platforms"), filepath.Join(d, "patched")
	store, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.AddArtifactRoot(patched)
	for _, p := range []string{filepath.Join(root, "gba", "Exact Game.gba"), filepath.Join(patched, "gba", "Exact Game.gba")} {
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte("fixture"), 0600)
	}
	if err = store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	i := NewIGDB(store, IGDBOptions{ClientID: "client", ClientSecret: "private", ThumbnailDirectory: filepath.Join(d, "thumbs")})
	i.tokenURL = server.URL + "/token"
	i.apiURL = server.URL + "/games"
	updated, err := i.Enrich(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if updated != 1 || calls.Load() != 2 {
		t.Fatalf("updated=%d calls=%d; patched guard failed", updated, calls.Load())
	}
	games, _ := store.Games(context.Background())
	for _, g := range games {
		if g.ROMPatch {
			if g.Description != "" {
				t.Fatal("patched game enriched")
			}
		} else if g.Description != "A fixture description." || g.Developer != "Fixture Studio" || g.Genre != "Adventure" {
			t.Fatalf("bad metadata: %#v", g)
		}
	}
}

func TestIGDBTitleNormalizationFoldsAccents(t *testing.T) {
	if normalizeTitle("Pokémon X") != normalizeTitle("Pokemon X (USA)") {
		t.Fatalf("accent normalization mismatch: %q %q", normalizeTitle("Pokémon X"), normalizeTitle("Pokemon X (USA)"))
	}
}

func TestIGDBLookupUsesFullArtifactNameOverShortHeader(t *testing.T) {
	g := catalog.Game{Title: "POKEMON EMER", Artifacts: []catalog.Artifact{{Name: "Pokemon - Emerald Version (USA, Europe).gba"}}}
	if got := igdbLookupTitle(g); got != "pokemon emerald version" {
		t.Fatalf("lookup title=%q", got)
	}
}

func TestIGDBLookupTrustsLibretroTitleOverTaggedArtifact(t *testing.T) {
	g := catalog.Game{Title: "Pokemon - Crystal Version (USA, Europe) (Rev 1)", MetadataSource: "libretro", Artifacts: []catalog.Artifact{{Name: "Pokemon - Crystal Version (UE) (V1.1) [C][!].gbc"}}}
	if got := igdbLookupTitle(g); got != "pokemon crystal version" {
		t.Fatalf("lookup title=%q", got)
	}
}

func TestIGDBTitleTokenSignatureAllowsSubtitleOrder(t *testing.T) {
	a := titleTokenSignature("Super Mario Advance 2 - Super Mario World")
	b := titleTokenSignature("Super Mario World: Super Mario Advance 2")
	if a != b {
		t.Fatalf("subtitle signatures differ: %q %q", a, b)
	}
}

func TestIGDBSlugCandidatesIncludeVersionFallback(t *testing.T) {
	got := strings.Join(igdbSlugCandidates("Pokémon - Blue Version (USA)"), ",")
	if got != "pokemon-blue-version,pokemon-blue" {
		t.Fatalf("slug candidates=%q", got)
	}
}

func TestIGDBPlatformBatchesNeverMixPlatforms(t *testing.T) {
	games := []catalog.Game{{Platform: "gba"}, {Platform: "nds"}, {Platform: "gba"}}
	batches := igdbPlatformBatches(games, 10)
	if len(batches) != 2 || len(batches[0]) != 2 || batches[0][0].Platform != "gba" || len(batches[1]) != 1 || batches[1][0].Platform != "nds" {
		t.Fatalf("unexpected batches: %#v", batches)
	}
}

func TestIGDB429RetriesThenStopsBatchSweep(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"test-token","expires_in":3600}`)
			return
		}
		if r.URL.Path == "/games" {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "limited", http.StatusTooManyRequests)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	store, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = os.MkdirAll(filepath.Join(root, "gba"), 0755); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 11; n++ {
		if err = os.WriteFile(filepath.Join(root, "gba", fmt.Sprintf("Game %02d.gba", n)), make([]byte, 0x200), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	i := NewIGDB(store, IGDBOptions{ClientID: "client", ClientSecret: "private", ThumbnailDirectory: filepath.Join(d, "thumbs")})
	i.tokenURL = server.URL + "/token"
	i.apiURL = server.URL + "/games"
	i.sleep = func(context.Context, time.Duration) error { return nil }
	updated, err := i.Enrich(context.Background(), time.Now().Add(time.Hour))
	if err == nil || updated != 0 || calls.Load() != 4 || strings.Count(err.Error(), "rate limited") != 1 {
		t.Fatalf("updated=%d calls=%d err=%v", updated, calls.Load(), err)
	}
}
