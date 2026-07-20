package pokedex

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

	"romrelay/internal/catalog"
)

// writeLocalFixture builds a minimal api-data-shaped tree for dex, evolving to
// evolvesToDex (0 for none) through a shared chain, so a local-client run can
// exercise the concurrent ingest path (runParallel) end to end.
func writeLocalFixture(t *testing.T, root string, dex int, name string) {
	t.Helper()
	speciesDir := filepath.Join(root, "pokemon-species", fmt.Sprint(dex))
	if err := os.MkdirAll(speciesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	species := fmt.Sprintf(`{"name":%q,"generation":{"name":"generation-i"},
"genera":[{"genus":"Weed Pokémon","language":{"name":"en"}}],
"evolution_chain":{"url":"https://pokeapi.co/api/v2/evolution-chain/18/"},
"pokedex_numbers":[],"flavor_text_entries":[],
"egg_groups":[{"name":"grass"}],"gender_rate":1,"hatch_counter":20}`, name)
	if err := os.WriteFile(filepath.Join(speciesDir, "index.json"), []byte(species), 0o644); err != nil {
		t.Fatal(err)
	}
	pokemonDir := filepath.Join(root, "pokemon", fmt.Sprint(dex))
	if err := os.MkdirAll(pokemonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pokemon := `{"height":4,"weight":58,"types":[{"slot":1,"type":{"name":"grass"}}],
"stats":[{"base_stat":75,"stat":{"name":"hp"}}],
"abilities":[{"ability":{"name":"chlorophyll"},"is_hidden":false,"slot":1}],
"moves":[]}`
	if err := os.WriteFile(filepath.Join(pokemonDir, "index.json"), []byte(pokemon), 0o644); err != nil {
		t.Fatal(err)
	}
	encountersDir := filepath.Join(pokemonDir, "encounters")
	if err := os.MkdirAll(encountersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(encountersDir, "index.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A local api-data checkout ingests concurrently (runParallel), unlike the
// network client's polite sequential walk. This exercises that path across
// several independent species plus a shared evolution chain, so the
// chainSet's dedup and the worker pool's shared counters get run for real
// (run with -race to catch any data race between workers).
func TestIngestRunsConcurrentlyAgainstALocalCheckout(t *testing.T) {
	root := t.TempDir()
	names := map[int]string{44: "gloom", 61: "poliwhirl", 182: "bellossom"}
	for dex, name := range names {
		writeLocalFixture(t, root, dex, name)
	}
	chainDir := filepath.Join(root, "evolution-chain", "18")
	if err := os.MkdirAll(chainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chain := `{"chain":{"species":{"name":"gloom","url":"https://pokeapi.co/api/v2/pokemon-species/44/"},
"evolution_details":[],"evolves_to":[{"species":{"name":"bellossom","url":"https://pokeapi.co/api/v2/pokemon-species/182/"},
"evolution_details":[{"trigger":{"name":"use-item"},"item":{"name":"sun-stone"}}],"evolves_to":[]}]}}`
	if err := os.WriteFile(filepath.Join(chainDir, "index.json"), []byte(chain), 0o644); err != nil {
		t.Fatal(err)
	}

	store := ingestStore(t)
	job := New(store, Options{Client: NewLocalClient(root)})
	stored, err := job.Run(context.Background(), 182)
	if err != nil {
		t.Fatal(err)
	}
	if stored != len(names) {
		t.Fatalf("expected %d species stored, got %d", len(names), stored)
	}
	ctx := context.Background()
	for dex, name := range names {
		sp, found, err := store.SpeciesDetail(ctx, dex)
		if err != nil || !found || sp.Name != name {
			t.Fatalf("dex %d must be stored as %q: found=%v err=%v got=%+v", dex, name, found, err, sp)
		}
	}
	evos, err := store.EvolutionsFor(ctx, 44)
	if err != nil || len(evos) != 1 || evos[0].ToDex != 182 {
		t.Fatalf("shared evolution chain must be stored exactly once, not duplicated or dropped by concurrent claims: %#v %v", evos, err)
	}
}

// fakeAPI serves a two-species world: Gloom (44) evolves into Bellossom (182)
// with a Sun Stone.
func fakeAPI(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		switch {
		case strings.HasPrefix(r.URL.Path, "/pokemon-species/"):
			dex := strings.TrimPrefix(r.URL.Path, "/pokemon-species/")
			name := map[string]string{"44": "gloom", "182": "bellossom"}[dex]
			fmt.Fprintf(w, `{"name":%q,"generation":{"name":"generation-i"},
"genera":[{"genus":"Weed Pokémon","language":{"name":"en"}}],
"evolution_chain":{"url":%q},
"pokedex_numbers":[{"entry_number":86,"pokedex":{"name":"original-johto"}}],
"flavor_text_entries":[{"flavor_text":"Line one.\nLine two.","language":{"name":"en"}}],
"egg_groups":[{"name":"grass"},{"name":"plant"}],"gender_rate":1,"hatch_counter":20}`,
				name, "http://"+r.Host+"/evolution-chain/18")
		case strings.HasSuffix(r.URL.Path, "/encounters"):
			if strings.Contains(r.URL.Path, "/pokemon/44/") {
				fmt.Fprint(w, `[{"location_area":{"name":"viridian-forest-area"},"version_details":[
{"version":{"name":"heartgold"},"max_chance":20,"encounter_details":[
{"min_level":3,"max_level":5,"chance":15,"method":{"name":"walk"},"condition_values":[{"name":"time-morning"}]},
{"min_level":4,"max_level":6,"chance":20,"method":{"name":"walk"},"condition_values":[{"name":"time-morning"}]}]}]}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case strings.HasPrefix(r.URL.Path, "/pokemon/"):
			fmt.Fprintf(w, `{"height":4,"weight":58,"types":[{"slot":1,"type":{"name":"grass"}},{"slot":2,"type":{"name":"poison"}}],
"stats":[{"base_stat":75,"stat":{"name":"hp"}},{"base_stat":80,"stat":{"name":"attack"}},
{"base_stat":85,"stat":{"name":"defense"}},{"base_stat":90,"stat":{"name":"special-attack"}},
{"base_stat":100,"stat":{"name":"special-defense"}},{"base_stat":50,"stat":{"name":"speed"}}],
"abilities":[{"ability":{"name":"chlorophyll"},"is_hidden":false,"slot":1},
{"ability":{"name":"healer"},"is_hidden":true,"slot":3}],
"moves":[
{"move":{"name":"petal-dance","url":%q},"version_group_details":[
{"level_learned_at":21,"move_learn_method":{"name":"level-up"},"version_group":{"name":"red-blue"}}]},
{"move":{"name":"solar-beam","url":%q},"version_group_details":[
{"level_learned_at":0,"move_learn_method":{"name":"machine"},"version_group":{"name":"red-blue"}}]},
{"move":{"name":"amnesia","url":%q},"version_group_details":[
{"level_learned_at":0,"move_learn_method":{"name":"tutor"},"version_group":{"name":"emerald"}}]}]}`,
				"http://"+r.Host+"/move/petal-dance/", "http://"+r.Host+"/move/solar-beam/", "http://"+r.Host+"/move/amnesia/")
		case strings.HasPrefix(r.URL.Path, "/move/"):
			name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/move/"), "/")
			fmt.Fprintf(w, `{"name":%q,"power":90,"accuracy":100,"type":{"name":"grass"},"damage_class":{"name":"special"}}`, name)
		case r.URL.Path == "/evolution-chain/18":
			fmt.Fprint(w, `{"chain":{"species":{"name":"gloom","url":"http://x/pokemon-species/44/"},"evolution_details":[],
"evolves_to":[{"species":{"name":"bellossom","url":"http://x/pokemon-species/182/"},
"evolution_details":[{"trigger":{"name":"use-item"},"item":{"name":"sun-stone"}}],"evolves_to":[]}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func ingestStore(t *testing.T) *catalog.Store {
	t.Helper()
	d := t.TempDir()
	store, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestIngestStoresSpeciesEvolutionsAndRegionalNumbers(t *testing.T) {
	var hits int32
	server := fakeAPI(t, &hits)
	store := ingestStore(t)

	var lastDone, lastTotal int
	job := New(store, Options{
		Client:   NewClient(server.URL + "/"),
		Progress: func(done, total int) { lastDone, lastTotal = done, total },
	})

	stored, err := job.Run(context.Background(), 182)
	if err != nil {
		t.Fatal(err)
	}
	if stored == 0 {
		t.Fatal("the ingest stored nothing")
	}

	ctx := context.Background()
	sp, found, err := store.SpeciesDetail(ctx, 182)
	if err != nil || !found {
		t.Fatalf("bellossom must be stored: %v %v", found, err)
	}
	if sp.Types != "grass/poison" {
		t.Errorf("types must be joined in slot order: %q", sp.Types)
	}
	if sp.FlavorText != "Line one. Line two." {
		t.Errorf("flavour text must be flattened: %q", sp.FlavorText)
	}
	if sp.EggGroups != "grass,plant" || sp.GenderRate != 1 || sp.HatchCounter != 20 {
		t.Errorf("breeding data wrong: groups=%q gender_rate=%d hatch=%d", sp.EggGroups, sp.GenderRate, sp.HatchCounter)
	}
	if sp.StatHP != 75 || sp.StatAtk != 80 || sp.StatDef != 85 || sp.StatSpA != 90 || sp.StatSpD != 100 || sp.StatSpe != 50 {
		t.Errorf("base stats must be parsed from the /pokemon payload, keyed by stat name: %#v",
			[]int{sp.StatHP, sp.StatAtk, sp.StatDef, sp.StatSpA, sp.StatSpD, sp.StatSpe})
	}

	learnset, err := store.LearnsetFor(ctx, 182)
	if err != nil {
		t.Fatal(err)
	}
	if len(learnset) != 2 {
		t.Fatalf("egg/tutor moves must be excluded, only level-up and machine kept: got %#v", learnset)
	}
	for _, e := range learnset {
		if e.Move == "petal-dance" && (e.Method != "level-up" || e.Level != 21 || e.Type != "grass" || e.Category != "special") {
			t.Errorf("level-up move wrong: %#v", e)
		}
		if e.Move == "solar-beam" && (e.Method != "machine" || e.Power != 90) {
			t.Errorf("machine move wrong: %#v", e)
		}
	}

	evos, err := store.EvolutionsFor(ctx, 44)
	if err != nil || len(evos) != 1 {
		t.Fatalf("gloom must have one evolution: %#v %v", evos, err)
	}
	if evos[0].ToDex != 182 || evos[0].Condition != "Use a Sun Stone" {
		t.Errorf("evolution edge wrong: %#v", evos[0])
	}

	nums, _ := store.RegionalNumbersFor(ctx, 182)
	if len(nums) != 1 || nums[0].Pokedex != "original-johto" || nums[0].Entry != 86 {
		t.Errorf("regional numbers wrong: %#v", nums)
	}
	if lastTotal == 0 || lastDone != lastTotal {
		t.Errorf("progress must finish at 100%%: %d/%d", lastDone, lastTotal)
	}
}

// A nil Trigger (PokeAPI omits the field on some evolution details) must still
// upsert the edge, not drop it, and reading it must not panic.
func TestStoreChainUpsertsEvolutionWithNilTrigger(t *testing.T) {
	store := ingestStore(t)
	job := New(store, Options{Client: NewClient("http://unused/")})
	link := ChainLink{
		Species: named{Name: "gloom", URL: "http://x/pokemon-species/44/"},
		EvolvesTo: []ChainLink{{
			Species:          named{Name: "bellossom", URL: "http://x/pokemon-species/182/"},
			EvolutionDetails: []Detail{{Trigger: nil}},
		}},
	}
	if err := job.storeChain(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	evos, err := store.EvolutionsFor(context.Background(), 44)
	if err != nil || len(evos) != 1 {
		t.Fatalf("a nil trigger must still upsert the edge, not drop it: %#v %v", evos, err)
	}
}

// Shutdown cancels background work and waits for it, so the job must stop.
func TestIngestStopsOnContextCancellation(t *testing.T) {
	var hits int32
	server := fakeAPI(t, &hits)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	job := New(ingestStore(t), Options{Client: NewClient(server.URL + "/")})
	if _, err := job.Run(ctx, 182); err == nil {
		t.Fatal("a cancelled ingest must return an error, not finish quietly")
	}
}

// The job is resumable: a species already stored is not fetched again, so a
// restarted ingest does not re-download what it has.
func TestIngestSkipsSpeciesItAlreadyHas(t *testing.T) {
	var hits int32
	server := fakeAPI(t, &hits)
	store := ingestStore(t)
	job := New(store, Options{Client: NewClient(server.URL + "/")})

	if _, err := job.Run(context.Background(), 182); err != nil {
		t.Fatal(err)
	}
	first := atomic.LoadInt32(&hits)

	if _, err := job.Run(context.Background(), 182); err != nil {
		t.Fatal(err)
	}
	if second := atomic.LoadInt32(&hits); second != first {
		t.Errorf("a second run must fetch nothing: %d requests before, %d after", first, second)
	}
}

// A species whose /pokemon fetch fails must not be stored at all. Storing it
// half-filled would mark it done forever: resumability only checks that the row
// exists, so nothing would ever repair it.
func TestIngestDoesNotStoreASpeciesWhosePokemonFetchFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/pokemon-species/"):
			fmt.Fprint(w, `{"name":"gloom","generation":{"name":"generation-i"},
"genera":[{"genus":"Weed Pokémon","language":{"name":"en"}}],
"evolution_chain":{"url":""},"pokedex_numbers":[],"flavor_text_entries":[]}`)
		case strings.HasPrefix(r.URL.Path, "/pokemon/"):
			http.Error(w, "upstream is having a bad day", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := ingestStore(t)
	if _, err := New(store, Options{Client: NewClient(server.URL + "/")}).Run(context.Background(), 44); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.SpeciesDetail(context.Background(), 44); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("a species whose /pokemon fetch failed must not be stored: the row marks it done forever and nothing will repair it")
	}
}

// TestIngestStoresEncountersAggregatedPerVersion pins the encounter pass: raw
// slots sharing (version, location, method, conditions) merge into one row with
// the widest level range and best chance, and the pass is marked done so the
// hourly retry does not refetch a species with none.
func TestIngestStoresEncountersAggregatedPerVersion(t *testing.T) {
	var hits int32
	server := fakeAPI(t, &hits)
	store := ingestStore(t)
	if _, err := New(store, Options{Client: NewClient(server.URL + "/")}).Run(context.Background(), 44); err != nil {
		t.Fatal(err)
	}

	got, err := store.EncountersFor(context.Background(), 44, []string{"heartgold"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("two slots with the same key must merge into one row: %#v", got)
	}
	e := got[0]
	if e.Location != "viridian-forest-area" || e.Method != "walk" || e.Conditions != "time-morning" {
		t.Fatalf("row must carry where and how: %#v", e)
	}
	if e.MinLevel != 3 || e.MaxLevel != 6 || e.Chance != 20 {
		t.Fatalf("levels widen and chance takes the best slot: %#v", e)
	}

	// Version filtering is what makes exclusivity visible.
	none, err := store.EncountersFor(context.Background(), 44, []string{"soulsilver"})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("a version with no rows must come back empty: %#v", none)
	}
}
