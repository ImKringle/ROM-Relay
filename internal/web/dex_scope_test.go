package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"romrelay/internal/catalog"
	"romrelay/internal/pokemon"
)

// dexSidecar fakes /species with enough of the National list to exercise the
// scope logic (Chikorita #152, Cyndaquil #155, Azumarill #184).
func dexSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/species" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"count":3,"species":[{"dex":152,"name":"Chikorita"},{"dex":155,"name":"Cyndaquil"},{"dex":184,"name":"Azumarill"}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func fetchDex(t *testing.T, client *http.Client, url string) map[string]any {
	t.Helper()
	res, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dex fetch %s: %d %s", url, res.StatusCode, readBody(t, res))
	}
	var view map[string]any
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

// TestDexScopesRegionalNationalAndGlobal drives the three scopes: a HeartGold
// tab defaults to its regional (Johto) dex when the enrichment holds one, can be
// asked for its generation's National dex, and the global view spans everything.
func TestDexScopesRegionalNationalAndGlobal(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(dexSidecar(t).URL))
	ctx := context.Background()

	// Johto's regional dex, as the PokeAPI ingest stores it.
	for _, r := range []catalog.RegionalNumber{
		{Dex: 152, Pokedex: "updated-johto", Entry: 1},
		{Dex: 155, Pokedex: "updated-johto", Entry: 4},
	} {
		if err := f.store.UpsertRegionalNumber(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	client := f.signIn(t, "gold")
	owner := f.ownerID(t, "gold")
	if err := f.store.UpsertGameDex(ctx, owner, "HG", "", []int{152, 184}, []int{152, 184}); err != nil {
		t.Fatal(err)
	}

	// Regional is the default for a game whose dex is ingested: 2 entries, the
	// game's own numbering, Azumarill (not in the seeded Johto dex) excluded.
	regional := fetchDex(t, client, f.server.URL+"/api/dex?game=HG")
	if regional["scope_kind"] != "regional" || regional["region"] != "Johto" {
		t.Fatalf("a game with ingested regional data must default to it: %v / %v", regional["scope_kind"], regional["region"])
	}
	if total := regional["total"].(float64); total != 2 {
		t.Fatalf("the regional view is scored against the regional dex: total=%v", total)
	}
	entries := regional["entries"].([]any)
	first := entries[0].(map[string]any)
	if first["dex"].(float64) != 152 || first["regional"].(float64) != 1 {
		t.Fatalf("regional entries carry the game's own numbering: %v", first)
	}

	// National widens to the generation's dex (HG save range unrecorded here, so
	// the gen-4 cap applies) and includes Azumarill again.
	national := fetchDex(t, client, f.server.URL+"/api/dex?game=HG&scope=national")
	if national["scope_kind"] != "national" {
		t.Fatalf("scope=national must be honoured: %v", national["scope_kind"])
	}
	if total := national["total"].(float64); total != 3 {
		t.Fatalf("the national view spans the generation's dex: total=%v", total)
	}

	// Global is the Living Dex across everything.
	global := fetchDex(t, client, f.server.URL+"/api/dex")
	if global["scope_kind"] != "global" {
		t.Fatalf("no game means the global Living Dex: %v", global["scope_kind"])
	}
}

// TestDexRegionalScopeFallsBackWithoutIngestedData pins the degraded mode: no
// pokedex_regional rows means the regional toggle is not offered and the view
// stays the generation National dex rather than an empty grid.
func TestDexRegionalScopeFallsBackWithoutIngestedData(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(dexSidecar(t).URL))
	client := f.signIn(t, "silver")

	view := fetchDex(t, client, f.server.URL+"/api/dex?game=HG&scope=regional")
	if view["scope_kind"] != "national" {
		t.Fatalf("regional without data must fall back to national: %v", view["scope_kind"])
	}
	scopes := view["scopes"].([]any)
	if len(scopes) != 1 || scopes[0] != "national" {
		t.Fatalf("only national must be offered without ingested data: %v", scopes)
	}
}

// TestDexReturnsBothCaughtCountAndObtainedCountIndependently pins Phase 4 item
// 7: caught_count (PC copies, pokemon_instances) and count (the save's own
// obtained flag, game_dex.caught) are two independent figures, not one
// derived from the other. Chikorita has an obtained flag but no PC copy,
// Cyndaquil has a PC copy but no obtained flag, proving neither field
// silently mirrors the other.
func TestDexReturnsBothCaughtCountAndObtainedCountIndependently(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(dexSidecar(t).URL))
	ctx := context.Background()

	client := f.signIn(t, "gold")
	owner := f.ownerID(t, "gold")
	if err := f.store.UpsertGameDex(ctx, owner, "HG", "", []int{152}, []int{152}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.store.SavePokemonInstances(ctx, owner, []catalog.PokemonInstance{
		{Species: 155, SpeciesName: "Cyndaquil", OriginGame: "HG", Binary: []byte("cyndaquil-1")},
	}); err != nil {
		t.Fatal(err)
	}

	view := fetchDex(t, client, f.server.URL+"/api/dex?game=HG")
	entries := view["entries"].([]any)
	byDex := map[float64]map[string]any{}
	for _, raw := range entries {
		e := raw.(map[string]any)
		byDex[e["dex"].(float64)] = e
	}

	chikorita := byDex[152]
	if owned, _ := chikorita["owned"].(bool); !owned {
		t.Errorf("Chikorita has an obtained flag: owned must be true: %v", chikorita)
	}
	if cc, _ := chikorita["caught_count"].(float64); cc != 0 {
		t.Errorf("Chikorita has no PC copy: caught_count must be 0, got %v", chikorita["caught_count"])
	}

	cyndaquil := byDex[155]
	if owned, _ := cyndaquil["owned"].(bool); owned {
		t.Errorf("Cyndaquil has no obtained flag: owned must stay false: %v", cyndaquil)
	}
	if cc, _ := cyndaquil["caught_count"].(float64); cc != 1 {
		t.Errorf("Cyndaquil has one PC copy: caught_count must be 1, got %v", cyndaquil["caught_count"])
	}
}

// TestSpeciesDetailRegionParamNarrowsEncounters pins Phase 4 item 6: the
// species panel's encounters narrow to one region when ?region= names it
// (a trainer viewing their own playthrough's dex), and show every region
// when neither ?region= nor ?game= is given (the global dex).
func TestSpeciesDetailRegionParamNarrowsEncounters(t *testing.T) {
	f := newSaveVaultFixture(t)
	ash := f.signIn(t, "ash")
	ctx := context.Background()

	if err := f.store.UpsertSpecies(ctx, catalog.Species{Dex: 1, Name: "bulbasaur", GenderRate: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertEncounter(ctx, catalog.Encounter{Dex: 1, Version: "red", Location: "route-1", Method: "walk", MinLevel: 3, MaxLevel: 5}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertEncounter(ctx, catalog.Encounter{Dex: 1, Version: "heartgold", Location: "route-30", Method: "walk", MinLevel: 4, MaxLevel: 6}); err != nil {
		t.Fatal(err)
	}

	fetchRegions := func(url string) (regions []string, encounters int) {
		t.Helper()
		res, err := ash.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got struct {
			EncounterRegions []string         `json:"encounter_regions"`
			Encounters       []map[string]any `json:"encounters"`
		}
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got.EncounterRegions, len(got.Encounters)
	}

	regions, count := fetchRegions(f.server.URL + "/api/dex/species/1")
	if count != 2 || len(regions) != 2 {
		t.Fatalf("no region/game param must show every region: regions=%v count=%d", regions, count)
	}

	regions, count = fetchRegions(f.server.URL + "/api/dex/species/1?region=Kanto")
	if count != 1 || len(regions) != 1 || regions[0] != "Kanto" {
		t.Fatalf("?region=Kanto must narrow to Kanto only: regions=%v count=%d", regions, count)
	}

	regions, count = fetchRegions(f.server.URL + "/api/dex/species/1?region=Johto")
	if count != 1 || len(regions) != 1 || regions[0] != "Johto" {
		t.Fatalf("?region=Johto must narrow to Johto only: regions=%v count=%d", regions, count)
	}
}
