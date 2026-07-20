package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"romrelay/internal/catalog"
	"romrelay/internal/pokemon"
)

// TestLiveDexAgainstPKHeX is the live cross-check: real save files through the
// real PKHeX sidecar and the real upload endpoint, then every dex view compared
// against the sidecar's own answer. It skips unless the sidecar is reachable
// and the save corpus exists, so CI never sees it.
//
//	ROMRELAY_LIVE_SIDECAR (default http://127.0.0.1:5162)
//	ROMRELAY_LIVE_SAVES   (default D:\ROM Relay\save-data)
func TestLiveDexAgainstPKHeX(t *testing.T) {
	sidecarURL := os.Getenv("ROMRELAY_LIVE_SIDECAR")
	if sidecarURL == "" {
		sidecarURL = "http://127.0.0.1:5162"
	}
	saveDir := os.Getenv("ROMRELAY_LIVE_SAVES")
	if saveDir == "" {
		saveDir = `D:\ROM Relay\save-data`
	}
	if _, err := http.Get(sidecarURL + "/health"); err != nil {
		t.Skipf("sidecar not reachable at %s: %v", sidecarURL, err)
	}
	files, err := os.ReadDir(saveDir)
	if err != nil || len(files) == 0 {
		t.Skipf("no save corpus at %s: %v", saveDir, err)
	}

	f := newSaveVaultFixture(t)
	pc := pokemon.NewClient(sidecarURL)
	f.ui.SetPokemonClient(pc)
	ctx := context.Background()

	// Seed the regional Pokédex tables the way the PokeAPI ingest would, from
	// the vendored api-data checkout.
	seedRegional(t, f.store, "hoenn", "4")
	seedRegional(t, f.store, "updated-johto", "7")
	seedRegional(t, f.store, "updated-unova", "9")

	client := f.signIn(t, "dexlive")
	owner := f.ownerID(t, "dexlive")

	// PKHeX truth: parse each save directly, then upload it through the app.
	type truth struct {
		file         string
		game         string
		max          int
		tid, sid     int
		seen, caught []int
		mons         []catalog.PokemonInstance
	}
	var truths []truth
	for _, fi := range files {
		data, err := os.ReadFile(filepath.Join(saveDir, fi.Name()))
		if err != nil {
			t.Fatal(err)
		}
		mons, sum, err := pc.Ingest(ctx, data, fi.Name())
		if err != nil {
			t.Fatalf("sidecar rejected %s: %v", fi.Name(), err)
		}
		truths = append(truths, truth{
			file: fi.Name(), game: sum.Game, max: sum.MaxSpecies,
			tid: sum.Trainer.TID, sid: sum.Trainer.SID,
			seen: sum.Seen, caught: sum.Caught, mons: mons,
		})
		res := f.upload(t, client, f.gameID, fi.Name(), data)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("upload %s: %d %s", fi.Name(), res.StatusCode, readBody(t, res))
		}
		res.Body.Close()
	}

	regionalSets := map[string]map[int]bool{}
	for _, g := range []struct{ game, dex string }{{"E", "hoenn"}, {"HG", "updated-johto"}, {"B2", "updated-unova"}, {"W2", "updated-unova"}} {
		entries, err := f.store.RegionalDexEntries(ctx, []string{g.dex})
		if err != nil {
			t.Fatal(err)
		}
		set := map[int]bool{}
		for _, e := range entries {
			set[e.Dex] = true
		}
		regionalSets[g.game] = set
	}

	unionCaught := map[int]bool{}
	for _, tr := range truths {
		caughtSet := map[int]bool{}
		for _, sp := range tr.caught {
			caughtSet[sp] = true
			unionCaught[sp] = true
		}

		// 1. DB flags == PKHeX flags.
		rows, err := f.store.GameDexCaught(ctx, owner, tr.game)
		if err != nil {
			t.Fatal(err)
		}
		dbSet := map[int]bool{}
		for _, r := range rows {
			dbSet[r.Species] = true
		}
		if diff := setDiff(caughtSet, dbSet); diff != "" {
			t.Errorf("%s [%s]: game_dex caught != PKHeX caught: %s", tr.file, tr.game, diff)
		} else {
			t.Logf("%s [%s]: game_dex caught matches PKHeX exactly (%d species)", tr.file, tr.game, len(caughtSet))
		}

		// 2. Per-game National view: owned == caught, total == the save's own dex size.
		national := fetchDex(t, client, f.server.URL+"/api/dex?game="+tr.game+"&scope=national")
		if got, want := int(national["owned"].(float64)), len(caughtSet); got != want {
			t.Errorf("%s [%s] national owned=%d want %d", tr.file, tr.game, got, want)
		}
		if got, want := int(national["total"].(float64)), tr.max; got != want {
			t.Errorf("%s [%s] national total=%d want %d (save MaxSpecies)", tr.file, tr.game, got, want)
		}
		t.Logf("%s [%s] national view: %v/%v", tr.file, tr.game, national["owned"], national["total"])

		// 3. Per-game regional view: owned == caught ∩ regional dex, total == dex size.
		set := regionalSets[tr.game]
		if len(set) > 0 {
			wantOwned := 0
			for sp := range caughtSet {
				if set[sp] {
					wantOwned++
				}
			}
			regional := fetchDex(t, client, f.server.URL+"/api/dex?game="+tr.game)
			if regional["scope_kind"] != "regional" {
				t.Errorf("%s [%s]: expected regional default, got %v", tr.file, tr.game, regional["scope_kind"])
			}
			if got := int(regional["owned"].(float64)); got != wantOwned {
				t.Errorf("%s [%s] regional owned=%d want %d (caught∩dex)", tr.file, tr.game, got, wantOwned)
			}
			if got, want := int(regional["total"].(float64)), len(set); got != want {
				t.Errorf("%s [%s] regional total=%d want %d", tr.file, tr.game, got, want)
			}
			outside := len(caughtSet) - wantOwned
			t.Logf("%s [%s] regional view: %v/%v (%d caught species sit outside the regional dex)",
				tr.file, tr.game, regional["owned"], regional["total"], outside)
		}
	}

	// 4. Per-playthrough cards: dex_caught == that save's PKHeX caught count.
	res, err := client.Get(f.server.URL + "/api/userdex")
	if err != nil {
		t.Fatal(err)
	}
	var userdex struct {
		Profiles []struct {
			Game      string `json:"game"`
			TID       int    `json:"tid"`
			SID       int    `json:"sid"`
			DexCaught int    `json:"dex_caught"`
		} `json:"profiles"`
	}
	if err := json.NewDecoder(res.Body).Decode(&userdex); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for _, tr := range truths {
		found := false
		for _, p := range userdex.Profiles {
			if p.Game == tr.game && p.TID == tr.tid && p.SID == tr.sid {
				found = true
				if p.DexCaught != len(tr.caught) {
					t.Errorf("%s [%s] playthrough card dex_caught=%d want %d", tr.file, tr.game, p.DexCaught, len(tr.caught))
				} else {
					t.Logf("%s [%s] playthrough card dex_caught=%d matches", tr.file, tr.game, p.DexCaught)
				}
			}
		}
		if !found {
			t.Errorf("%s [%s]: no trainer profile for %d:%d", tr.file, tr.game, tr.tid, tr.sid)
		}
	}

	// 5. Global caught tracker: owned == the union of caught flags across every
	// save, never what is held in storage.
	global := fetchDex(t, client, f.server.URL+"/api/dex")
	if got, want := int(global["owned"].(float64)), len(unionCaught); got != want {
		t.Errorf("global owned=%d want %d (union of caught flags)", got, want)
	}
	t.Logf("global dex: %v/%v, union of PKHeX caught flags across all saves", global["owned"], global["total"])

	// 6. Unova region aggregate: caught(B2) ∪ caught(W2), intersected with the
	// updated-unova dex. Held Pokémon contribute nothing.
	unova := map[int]bool{}
	for _, tr := range truths {
		if tr.game != "B2" && tr.game != "W2" {
			continue
		}
		for _, sp := range tr.caught {
			unova[sp] = true
		}
	}
	wantUnova := 0
	for sp := range unova {
		if regionalSets["B2"][sp] {
			wantUnova++
		}
	}
	region := fetchDex(t, client, f.server.URL+"/api/dex?region=Unova")
	if got := int(region["owned"].(float64)); got != wantUnova {
		t.Errorf("Unova region owned=%d want %d", got, wantUnova)
	}
	t.Logf("Unova region view: %v/%v (expected %d)", region["owned"], region["total"], wantUnova)
}

// seedRegional loads one pokedex file from the api-data checkout into the store.
func seedRegional(t *testing.T, store *catalog.Store, dexName, dirID string) {
	t.Helper()
	path := filepath.Join("..", "..", "third_party", "api-data", "data", "api", "v2", "pokedex", dirID, "index.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("api-data checkout missing: %v", err)
	}
	var doc struct {
		Name           string `json:"name"`
		PokemonEntries []struct {
			EntryNumber    int `json:"entry_number"`
			PokemonSpecies struct {
				URL string `json:"url"`
			} `json:"pokemon_species"`
		} `json:"pokemon_entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != dexName {
		t.Fatalf("pokedex %s is %q, expected %q", dirID, doc.Name, dexName)
	}
	ctx := context.Background()
	for _, e := range doc.PokemonEntries {
		parts := strings.Split(strings.Trim(e.PokemonSpecies.URL, "/"), "/")
		dex, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertRegionalNumber(ctx, catalog.RegionalNumber{Dex: dex, Pokedex: dexName, Entry: e.EntryNumber}); err != nil {
			t.Fatal(err)
		}
	}
}

// setDiff describes the symmetric difference of two species sets, "" when equal.
func setDiff(want, got map[int]bool) string {
	var missing, extra []int
	for sp := range want {
		if !got[sp] {
			missing = append(missing, sp)
		}
	}
	for sp := range got {
		if !want[sp] {
			extra = append(extra, sp)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	sort.Ints(missing)
	sort.Ints(extra)
	return "missing=" + intsToString(missing) + " extra=" + intsToString(extra)
}

func intsToString(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
