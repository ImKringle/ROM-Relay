package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/pokemon"
	"romrelay/internal/savevault"
)

// saveVaultFixture builds a catalog with one real game, a server with a vault
// wired in, and a signed-in HTTP client.
type saveVaultFixture struct {
	store     *catalog.Store
	ui        *CatalogServer
	server    *httptest.Server
	gameID    string
	vaultRoot string
}

func newSaveVaultFixture(t *testing.T) *saveVaultFixture {
	t.Helper()
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	store, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	rom := filepath.Join(root, "gb", "pocket-monsters.gb")
	if err := os.MkdirAll(filepath.Dir(rom), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rom, make([]byte, 0x200), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	games, err := store.Games(ctx)
	if err != nil || len(games) == 0 {
		t.Fatalf("fixture needs a catalog game: %v", err)
	}

	vaultRoot := filepath.Join(d, "storage", "saves")
	ui := NewCatalog(store, "http://example", "", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), time.Hour)
	ui.SetSaveVault(savevault.NewDirectoryVault(vaultRoot))
	server := httptest.NewServer(ui.Handler())
	t.Cleanup(server.Close)

	return &saveVaultFixture{store: store, ui: ui, server: server, gameID: games[0].ID, vaultRoot: vaultRoot}
}

// signIn creates a user, logs it in, and returns a client carrying its session.
func (f *saveVaultFixture) signIn(t *testing.T, username string) *http.Client {
	t.Helper()
	ctx := context.Background()
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.store.CreateUser(ctx, username, hash, "user", false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.store.CreateSession(ctx, id, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	jar := &sessionJar{cookie: &http.Cookie{Name: sessionCookie, Value: token}}
	return &http.Client{Jar: jar}
}

func (f *saveVaultFixture) upload(t *testing.T, client *http.Client, gameID, fileName string, data []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if gameID != "" {
		_ = form.WriteField("game_id", gameID)
	}
	part, err := form.CreateFormFile("save_file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	form.Close()
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/api/saves/upload", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestUploadStoresSaveWhenItIsNotARecognisedPokemonSave(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signIn(t, "ash")

	// No PKHeX sidecar is configured, so nothing can parse this save. Vaulting
	// it must still succeed: parsing is an enhancement, not a precondition.
	res := f.upload(t, client, f.gameID, "zelda.sav", []byte("arbitrary save bytes"))
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("an unparseable save must still be stored: got %d: %s", res.StatusCode, body)
	}
	var got struct {
		SaveID string `json:"save_id"`
		Parsed bool   `json:"parsed"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Parsed {
		t.Fatal("a save no sidecar parsed must not be reported as parsed")
	}
	if got.SaveID == "" {
		t.Fatal("a stored save must be identified in the response")
	}
}

// TestDeletedSaveDisappearsFromTheListEndpoint reproduces the QA report
// "Deleted saves aren't cleared from the Editor" at the HTTP level: the
// Editor page's save picker and the Save Vault page both render from
// GET /api/saves with no client-side cache, so if a delete had failed to
// actually remove the row, both would keep listing it. Confirms it does not:
// DeleteSave's own DELETE FROM saves is unconditional and RowsAffected-guarded
// (see internal/catalog/saves.go), and this exercises it through the real
// upload → list → delete → list request cycle rather than the store directly.
func TestDeletedSaveDisappearsFromTheListEndpoint(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signIn(t, "ash")

	ids := make([]string, 3)
	for i := range ids {
		res := f.upload(t, client, f.gameID, "save.sav", []byte{byte(i), 1, 2, 3})
		var got struct {
			SaveID string `json:"save_id"`
		}
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		ids[i] = got.SaveID
	}

	list := func() []string {
		res, err := client.Get(f.server.URL + "/api/saves")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got struct {
			Saves []struct {
				ID string `json:"id"`
			} `json:"saves"`
		}
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(got.Saves))
		for i, s := range got.Saves {
			out[i] = s.ID
		}
		return out
	}

	if got := list(); len(got) != 3 {
		t.Fatalf("expected all 3 uploaded versions listed, got %v", got)
	}

	for _, id := range ids[:2] {
		req, err := http.NewRequest(http.MethodDelete, f.server.URL+"/api/saves/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("delete of %s failed: %d", id, res.StatusCode)
		}
	}

	got := list()
	if len(got) != 1 || got[0] != ids[2] {
		t.Fatalf("deleted saves must not still be listed: got %v, want only %s", got, ids[2])
	}
}

// fakeFireRedSidecar reports a save as FireRed, one half of the Gen 3
// FireRed/LeafGreen pair PKHeX cannot actually distinguish from save bytes
// alone, which is exactly the ambiguity the game-override endpoint exists to
// let a person correct by hand.
func fakeFireRedSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/parse" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"game":"FR","generation":3,"trainer":{"OT":"Red"},"count":0,
"max_species":151,"dex":{"seen":[],"caught":[]},"pokemon":[]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestGameOverrideCorrectsAnAmbiguousGen3Detection pins the QA fix for
// "uploading a LeafGreen save marks it the same game as FireRed": PKHeX's own
// guess (both saves came back "FR" from the fake sidecar, mirroring the real
// ambiguity) can be corrected by hand, but only to another game in the same
// FR/LG/FRLG cluster, an unrelated code like HeartGold must be refused.
func TestGameOverrideCorrectsAnAmbiguousGen3Detection(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(fakeFireRedSidecar(t).URL))
	client := f.signIn(t, "ash")

	uploadRes := f.upload(t, client, f.gameID, "leafgreen.sav", []byte("a leafgreen save"))
	defer uploadRes.Body.Close()
	var uploaded struct {
		SaveID string `json:"save_id"`
	}
	if err := json.NewDecoder(uploadRes.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}

	override := func(game string) *http.Response {
		payload, _ := json.Marshal(map[string]string{"game": game})
		req, err := http.NewRequest(http.MethodPost, f.server.URL+"/api/saves/"+uploaded.SaveID+"/game-override", strings.NewReader(string(payload)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// An unrelated game must be refused: this corrects an honest ambiguity,
	// not a general relabel.
	rejected := override("HG")
	defer rejected.Body.Close()
	if rejected.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unrelated game, got %d", rejected.StatusCode)
	}

	// LeafGreen, FR's actual cluster-mate, must be accepted.
	accepted := override("LG")
	defer accepted.Body.Close()
	if accepted.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(accepted.Body)
		t.Fatalf("expected 200 for LG, got %d: %s", accepted.StatusCode, body)
	}

	listRes, err := client.Get(f.server.URL + "/api/saves")
	if err != nil {
		t.Fatal(err)
	}
	defer listRes.Body.Close()
	var list struct {
		Saves []struct {
			ParsedGame string `json:"parsed_game"`
			GameLabel  string `json:"game_label"`
		} `json:"saves"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Saves) != 1 || list.Saves[0].ParsedGame != "LG" || list.Saves[0].GameLabel != "LeafGreen" {
		t.Fatalf("override must relabel the save as LeafGreen: %#v", list.Saves)
	}
}

// fakeBagSidecar answers /parse (so an upload can be filed as a Pokémon
// save at all), /bag/read (one Items pouch holding a single Potion), and
// /bag/write (echoing back whatever pouches the request named, so the test
// can assert the write endpoint forwarded the caller's own edit rather than
// asserting anything about real PKHeX pouch semantics, that belongs to the
// sidecar's own BagTests.cs).
func fakeBagSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/parse":
			_, _ = w.Write([]byte(`{"game":"D","generation":4,"trainer":{"OT":"Lucas"},"count":0,
"max_species":493,"dex":{"seen":[],"caught":[]},"pokemon":[]}`))
		case "/bag/read":
			_, _ = w.Write([]byte(`{"pouches":[{"type":"Items","max_count":999,"capacity":50,
"items":[{"index":17,"name":"Potion","count":5}]}]}`))
		case "/bag/write":
			var body struct {
				Save    string          `json:"save"`
				Pouches json.RawMessage `json:"pouches"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// The fake doesn't actually mutate save bytes, it just proves the
			// write request reached here with the caller's pouches intact by
			// echoing a recognizable marker back as the "new" save.
			newSave := base64.StdEncoding.EncodeToString([]byte("bag-written:" + body.Save))
			_, _ = w.Write([]byte(`{"save":"` + newSave + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestSaveBagReadAndWriteRoundTrip pins the Editor's Bag tab endpoints: GET
// reports what the sidecar says a save holds, and POST forwards an edit to
// the sidecar and records the result as a new save version rather than
// mutating the upload in place, the same "edit produces a new version"
// convention withdrawPokehome already uses for an injected Pokémon.
func TestSaveBagReadAndWriteRoundTrip(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(fakeBagSidecar(t).URL))
	client := f.signIn(t, "lucas")

	uploadRes := f.upload(t, client, f.gameID, "diamond.sav", []byte("a diamond save"))
	defer uploadRes.Body.Close()
	var uploaded struct {
		SaveID string `json:"save_id"`
	}
	if err := json.NewDecoder(uploadRes.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}

	readRes, err := client.Get(f.server.URL + "/api/saves/" + uploaded.SaveID + "/bag")
	if err != nil {
		t.Fatal(err)
	}
	defer readRes.Body.Close()
	if readRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(readRes.Body)
		t.Fatalf("GET bag: %d: %s", readRes.StatusCode, body)
	}
	var read struct {
		Pouches []pokemon.BagPouch `json:"pouches"`
	}
	if err := json.NewDecoder(readRes.Body).Decode(&read); err != nil {
		t.Fatal(err)
	}
	if len(read.Pouches) != 1 || read.Pouches[0].Type != "Items" || len(read.Pouches[0].Items) != 1 ||
		read.Pouches[0].Items[0].Name != "Potion" || read.Pouches[0].Items[0].Count != 5 {
		t.Fatalf("bag read must forward the sidecar's pouches: %#v", read.Pouches)
	}

	// A save this account does not own must not be readable by guessing its id.
	other := f.signIn(t, "barry")
	anonRead, err := other.Get(f.server.URL + "/api/saves/" + uploaded.SaveID + "/bag")
	if err != nil {
		t.Fatal(err)
	}
	anonRead.Body.Close()
	if anonRead.StatusCode != http.StatusNotFound {
		t.Fatalf("another account's save bag must 404, got %d", anonRead.StatusCode)
	}

	writePayload, _ := json.Marshal(map[string]any{
		"pouches": []map[string]any{{"type": "Items", "items": []map[string]any{{"index": 4, "count": 1}}}},
	})
	writeReq, err := http.NewRequest(http.MethodPost, f.server.URL+"/api/saves/"+uploaded.SaveID+"/bag", strings.NewReader(string(writePayload)))
	if err != nil {
		t.Fatal(err)
	}
	writeReq.Header.Set("Content-Type", "application/json")
	writeRes, err := client.Do(writeReq)
	if err != nil {
		t.Fatal(err)
	}
	defer writeRes.Body.Close()
	if writeRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(writeRes.Body)
		t.Fatalf("POST bag: %d: %s", writeRes.StatusCode, body)
	}
	var written struct {
		SaveID  string             `json:"save_id"`
		Pouches []pokemon.BagPouch `json:"pouches"`
	}
	if err := json.NewDecoder(writeRes.Body).Decode(&written); err != nil {
		t.Fatal(err)
	}
	if written.SaveID == "" || written.SaveID == uploaded.SaveID {
		t.Fatalf("writing the bag must record a new save version, got %q (original %q)", written.SaveID, uploaded.SaveID)
	}

	// The new version must actually be listed, a new row, not just a
	// response the client happens to see once.
	listRes, err := client.Get(f.server.URL + "/api/saves")
	if err != nil {
		t.Fatal(err)
	}
	defer listRes.Body.Close()
	var list struct {
		Saves []struct {
			ID string `json:"id"`
		} `json:"saves"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list.Saves {
		if s.ID == written.SaveID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new save version must be listed: %#v", list.Saves)
	}
}

func TestUploadRequiresASignedInUser(t *testing.T) {
	f := newSaveVaultFixture(t)

	res := f.upload(t, &http.Client{}, f.gameID, "hg.sav", []byte("bytes"))
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous upload must be refused: got %d", res.StatusCode)
	}
}

func TestSpeciesDetailEndpointServesEnrichmentAndSaysWhenItIsMissing(t *testing.T) {
	f := newSaveVaultFixture(t)
	ash := f.signIn(t, "ash")
	ctx := context.Background()

	// Not ingested yet: the page must be told, not shown a blank species.
	res, err := ash.Get(f.server.URL + "/api/dex/species/182")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("an un-ingested species must 404: got %d", res.StatusCode)
	}

	if err := f.store.UpsertSpecies(ctx, catalog.Species{Dex: 182, Name: "bellossom", Genus: "Flower Pokémon", Types: "grass"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertEvolution(ctx, catalog.Evolution{FromDex: 44, ToDex: 182, Condition: "Use a Sun Stone"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertRegionalNumber(ctx, catalog.RegionalNumber{Dex: 182, Pokedex: "original-johto", Entry: 86}); err != nil {
		t.Fatal(err)
	}

	res, err = ash.Get(f.server.URL + "/api/dex/species/182")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("species detail: got %d", res.StatusCode)
	}
	var got struct {
		Species    catalog.Species          `json:"species"`
		Evolutions []catalog.Evolution      `json:"evolutions"`
		Regional   []catalog.RegionalNumber `json:"regional"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Species.Genus != "Flower Pokémon" || len(got.Evolutions) != 1 || got.Evolutions[0].Condition != "Use a Sun Stone" {
		t.Fatalf("detail payload wrong: %#v", got)
	}
	if len(got.Regional) != 1 || got.Regional[0].Entry != 86 {
		t.Fatalf("regional numbers missing: %#v", got.Regional)
	}

	// Reference data is still behind the login, like the rest of the app.
	anon, err := http.Get(f.server.URL + "/api/dex/species/182")
	if err != nil {
		t.Fatal(err)
	}
	anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Errorf("an anonymous request must be refused: got %d", anon.StatusCode)
	}
}

// TestSpeciesDetailServesMatchupsBreedingAndLearnset covers the fields the
// redesigned species panel added: computed type matchups, breeding facts
// straight off the species row, and the level-up/machine learnset filtered to
// the newest generation the species has data for (with the full available set
// alongside it, so the panel's generation chips are never offered for a
// generation with nothing to show).
func TestSpeciesDetailServesMatchupsBreedingAndLearnset(t *testing.T) {
	f := newSaveVaultFixture(t)
	ash := f.signIn(t, "ash")
	ctx := context.Background()

	if err := f.store.UpsertSpecies(ctx, catalog.Species{
		Dex: 6, Name: "charizard", Genus: "Flame Pokémon", Types: "fire/flying",
		EggGroups: "dragon,monster", GenderRate: 1, HatchCounter: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertMove(ctx, catalog.Move{Name: "ember", Type: "fire", Category: "special", Power: 40, Accuracy: 100}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertMove(ctx, catalog.Move{Name: "dragon-claw", Type: "dragon", Category: "physical", Power: 80, Accuracy: 100}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertLearnsetEntry(ctx, 6, catalog.LearnsetEntry{Move: "ember", Method: "level-up", Level: 9, Generations: "1,3,6,9"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertLearnsetEntry(ctx, 6, catalog.LearnsetEntry{Move: "dragon-claw", Method: "machine", Generations: "3"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertEncounter(ctx, catalog.Encounter{Dex: 6, Version: "heartgold", Location: "mt-silver", Method: "walk", MinLevel: 55, MaxLevel: 55, Chance: 3}); err != nil {
		t.Fatal(err)
	}

	res, err := ash.Get(f.server.URL + "/api/dex/species/6")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("species detail: got %d", res.StatusCode)
	}
	var got struct {
		Species             catalog.Species         `json:"species"`
		Matchups            map[string]float64      `json:"matchups"`
		Encounters          []map[string]any        `json:"encounters"`
		EncounterRegions    []string                `json:"encounter_regions"`
		Learnset            []catalog.LearnsetEntry `json:"learnset"`
		LearnsetGeneration  int                     `json:"learnset_generation"`
		LearnsetGenerations []int                   `json:"learnset_generations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if got.Species.EggGroups != "dragon,monster" || got.Species.GenderRate != 1 || got.Species.HatchCounter != 20 {
		t.Errorf("breeding fields missing: %#v", got.Species)
	}

	// Fire/Flying takes x4 from Rock (both types are individually weak to it),
	// x2 from Electric and Water, and is immune to Ground: the real Charizard
	// chart, which is what proves the multiplication actually combines types
	// rather than just echoing one of them.
	if got.Matchups["rock"] != 4 || got.Matchups["electric"] != 2 || got.Matchups["water"] != 2 || got.Matchups["ground"] != 0 {
		t.Errorf("type matchups wrong: rock=%v electric=%v water=%v ground=%v",
			got.Matchups["rock"], got.Matchups["electric"], got.Matchups["water"], got.Matchups["ground"])
	}

	if len(got.EncounterRegions) != 1 || got.EncounterRegions[0] != "Johto" {
		t.Errorf("encounter region must be derived from the game table: %#v", got.EncounterRegions)
	}
	if len(got.Encounters) != 1 || got.Encounters[0]["region"] != "Johto" {
		t.Errorf("each encounter must carry its own region: %#v", got.Encounters)
	}

	// Default is the newest generation this species has data for (9, from
	// ember's set), not generation 1 and not every generation at once.
	if got.LearnsetGeneration != 9 {
		t.Errorf("learnset must default to the latest generation: got %d", got.LearnsetGeneration)
	}
	if len(got.Learnset) != 1 || got.Learnset[0].Move != "ember" {
		t.Fatalf("gen-9 learnset must be ember only, not dragon-claw (gen 3 only): %#v", got.Learnset)
	}
	if got.Learnset[0].Type != "ember-should-not-match" && got.Learnset[0].Type != "fire" {
		t.Errorf("learnset entries must carry the move's own type: %#v", got.Learnset[0])
	}
	wantGens := map[int]bool{1: true, 3: true, 6: true, 9: true}
	if len(got.LearnsetGenerations) != len(wantGens) {
		t.Errorf("available generations must union every move's set: %#v", got.LearnsetGenerations)
	}

	// ?gen=3 narrows to what charizard actually knew in gen 3: both moves.
	res2, err := ash.Get(f.server.URL + "/api/dex/species/6?gen=3")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var got2 struct {
		Learnset           []catalog.LearnsetEntry `json:"learnset"`
		LearnsetGeneration int                     `json:"learnset_generation"`
	}
	if err := json.NewDecoder(res2.Body).Decode(&got2); err != nil {
		t.Fatal(err)
	}
	if got2.LearnsetGeneration != 3 || len(got2.Learnset) != 2 {
		t.Fatalf("?gen=3 must show both moves it knew that generation: %#v", got2)
	}
}

// TestSpeciesDetailEmitsEmptyArraysNotNull guards against a species with no
// evolutions or regional numbers marshaling those fields as JSON null. The
// panel iterates them directly, so a null there, not an empty array, throws
// in the browser for every single-stage species, which is most of the dex.
func TestSpeciesDetailEmitsEmptyArraysNotNull(t *testing.T) {
	f := newSaveVaultFixture(t)
	ash := f.signIn(t, "ash")
	ctx := context.Background()

	// A species that has been ingested but has no evolution edges and no
	// regional Pokédex entries recorded, the common case, not Task 6's fixture.
	if err := f.store.UpsertSpecies(ctx, catalog.Species{Dex: 25, Name: "pikachu", Genus: "Mouse Pokémon", Types: "electric"}); err != nil {
		t.Fatal(err)
	}

	res, err := ash.Get(f.server.URL + "/api/dex/species/25")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("species detail: got %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw["evolutions"]); got != "[]" {
		t.Errorf("evolutions must marshal as [] when empty, not %s (a JS .map() over null throws): body=%s", got, body)
	}
	if got := string(raw["regional"]); got != "[]" {
		t.Errorf("regional must marshal as [] when empty, not %s: body=%s", got, body)
	}
}

func TestUploadRejectsAnUnknownGame(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signIn(t, "ash")

	res := f.upload(t, client, "no-such-game", "hg.sav", []byte("bytes"))
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a save must belong to a game in the catalog: got %d", res.StatusCode)
	}
}

func TestDownloadReturnsTheOwnersOwnSaveBytes(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signIn(t, "ash")
	want := []byte("the real save bytes")

	res := f.upload(t, client, f.gameID, "hg.sav", want)
	var uploaded struct {
		SaveID string `json:"save_id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&uploaded)
	res.Body.Close()

	down, err := client.Get(f.server.URL + "/saves/" + uploaded.SaveID + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer down.Body.Close()
	got, _ := io.ReadAll(down.Body)
	if string(got) != string(want) {
		t.Fatalf("download must return the uploaded bytes: got %q want %q", got, want)
	}
}

func TestDownloadHidesAnotherUsersSave(t *testing.T) {
	f := newSaveVaultFixture(t)
	owner := f.signIn(t, "ash")
	res := f.upload(t, owner, f.gameID, "hg.sav", []byte("private"))
	var uploaded struct {
		SaveID string `json:"save_id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&uploaded)
	res.Body.Close()

	intruder := f.signIn(t, "gary")
	down, err := intruder.Get(f.server.URL + "/saves/" + uploaded.SaveID + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer down.Body.Close()
	if down.StatusCode != http.StatusNotFound {
		t.Fatalf("another user's save must be indistinguishable from a missing one: got %d", down.StatusCode)
	}
}

func TestListSavesShowsOnlyYourOwnSaves(t *testing.T) {
	f := newSaveVaultFixture(t)
	owner := f.signIn(t, "ash")
	f.upload(t, owner, f.gameID, "hg.sav", []byte("mine")).Body.Close()

	intruder := f.signIn(t, "gary")
	res, err := intruder.Get(f.server.URL + "/api/saves")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got struct {
		Saves []catalog.Save `json:"saves"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Saves) != 0 {
		t.Fatalf("a user must not see another user's saves: got %d", len(got.Saves))
	}
}

// fakeSidecar stands in for the PKHeX sidecar, returning one Pokémon.
func fakeSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/species":
			_, _ = w.Write([]byte(`{"count":3,"species":[{"dex":1,"name":"Bulbasaur"},{"dex":2,"name":"Ivysaur"},{"dex":3,"name":"Venusaur"}]}`))
		case "/parse":
			_, _ = w.Write([]byte(`{"game":"HG","generation":4,"trainer":{"OT":"Ash"},"count":1,
"max_species":2,
"dex":{"seen":[1],"caught":[1]},
"pokemon":[{"slot":"box","species_id":1,"species":"Bulbasaur","nickname":"Bulba","level":5,
"pid":1234,"ot":"Ash","blob":"AAECAw==","moves":[]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// Downloading is the whole point of the relay, and a signed-in person must be
// able to do it from a normal browser link. Those links carry no ?token=, and the
// download route only ever accepted the token, so with a viewer token configured
// every download and every health poll from a signed-in browser came back 401.
func TestASignedInUserCanDownloadWithoutTheViewerTokenInTheURL(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	store, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rom := filepath.Join(root, "gb", "game.gb")
	if err := os.MkdirAll(filepath.Dir(rom), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rom, make([]byte, 0x200), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := store.Games(context.Background())
	id := games[0].Artifacts[0].ID

	const viewer = "viewer-token-with-24-characters"
	ui := NewCatalog(store, "http://example", viewer, "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), time.Hour)
	server := httptest.NewServer(ui.Handler())
	defer server.Close()

	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	userID, err := store.CreateUser(context.Background(), "ash", hash, "user", false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(context.Background(), userID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: &sessionJar{cookie: &http.Cookie{Name: sessionCookie, Value: session}}}

	for _, path := range []string{"/d/" + id, "/healthz"} {
		res, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("a signed-in user must reach %s without a token in the URL: got %d", path, res.StatusCode)
		}
	}

	// The token still works on its own, for the handheld that has no session.
	res, err := http.Get(server.URL + "/d/" + id + "?token=" + viewer)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("the viewer token must still authenticate a download: got %d", res.StatusCode)
	}
	// And a stranger with neither is still refused.
	res, err = http.Get(server.URL + "/d/" + id)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("an anonymous download without the token must be refused: got %d", res.StatusCode)
	}
}

// The save says which game it is, so the uploader is not asked to pick a ROM.
// Picking one was asking them to repeat what the file already states, and to be
// wrong about it: here the upload names Emerald and the save is HeartGold.
func TestTheUploadTakesTheGameFromTheSaveNotThePicker(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(fakeSidecar(t).URL))
	ash := f.signIn(t, "ash")

	// f.gameID is the fixture's GB ROM; the fake sidecar reads the save as HG.
	res := f.upload(t, ash, f.gameID, "mystery.sav", []byte("a heartgold save"))
	defer res.Body.Close()
	var got struct {
		Game   string `json:"game"`
		Parsed bool   `json:"parsed"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Parsed {
		t.Fatal("the fixture save is readable")
	}
	// "HeartGold", not "HG" and not the ROM the upload came from.
	if got.Game != "HeartGold" {
		t.Errorf("a save must be filed under the game it says it is, named readably: got %q", got.Game)
	}

	saves, err := f.store.SavesForOwner(context.Background(), f.ownerID(t, "ash"))
	if err != nil || len(saves) != 1 {
		t.Fatalf("want one stored save: %v %d", err, len(saves))
	}
	if saves[0].GameLabel != "HeartGold" {
		t.Errorf("the stored save is labelled from its own contents: got %q", saves[0].GameLabel)
	}
}

// A save nothing can read is still a save worth keeping, and the game page is the
// one place that knows what it belongs to.
func TestAnUnreadableSaveIsFiledUnderTheGamePageItCameFrom(t *testing.T) {
	f := newSaveVaultFixture(t) // no sidecar wired: nothing can name this save
	ash := f.signIn(t, "ash")

	res := f.upload(t, ash, f.gameID, "zelda.sav", []byte("not a pokemon save"))
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("an unreadable save must still be stored: %d %s", res.StatusCode, body)
	}
	saves, err := f.store.SavesForOwner(context.Background(), f.ownerID(t, "ash"))
	if err != nil || len(saves) != 1 {
		t.Fatalf("want one stored save: %v %d", err, len(saves))
	}
	if saves[0].GameID != f.gameID {
		t.Errorf("with nothing able to read it, the game it was uploaded from is what files it: got %q", saves[0].GameID)
	}
}

// The Dex is one person's collection. It used to be read for a single built-in
// "default" account regardless of who asked, so an uploader was shown a Dex that
// was not theirs, and their own Pokémon were missing from it.
func TestTheDexShowsTheSignedInUsersOwnPokemon(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(fakeSidecar(t).URL))

	ash := f.signIn(t, "ash")
	misty := f.signIn(t, "misty")
	f.upload(t, ash, f.gameID, "hg.sav", []byte("ash's heartgold save")).Body.Close()

	dexFor := func(client *http.Client) (owned int) {
		t.Helper()
		res, err := client.Get(f.server.URL + "/api/dex")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("dex status=%d: %s", res.StatusCode, body)
		}
		var view struct {
			Owned int `json:"owned"`
		}
		if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
			t.Fatal(err)
		}
		return view.Owned
	}

	if got := dexFor(ash); got != 1 {
		t.Errorf("the uploader must see their own Pokémon in their Dex: owned=%d want 1", got)
	}
	if got := dexFor(misty); got != 0 {
		t.Errorf("another user's Pokémon must not appear in your Dex: owned=%d want 0", got)
	}

	// Signed out, there is no dex to show rather than somebody else's.
	res, err := http.Get(f.server.URL + "/api/dex")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("an anonymous request has no Dex: got %d", res.StatusCode)
	}
}

// A save first stored while no parser was reachable must pick up its Pokémon
// details when it is uploaded again with the sidecar available. Otherwise the
// dedup path leaves the save permanently labelled as unreadable even though its
// Pokémon reached the Dex.
func TestReuploadRecordsParseResultsForASaveStoredWithoutAParser(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signIn(t, "ash")
	save := []byte("a heartgold save")

	first := f.upload(t, client, f.gameID, "hg.sav", save)
	first.Body.Close()

	f.ui.SetPokemonClient(pokemon.NewClient(fakeSidecar(t).URL))
	res := f.upload(t, client, f.gameID, "hg.sav", save)
	defer res.Body.Close()
	var got struct {
		Parsed       bool `json:"parsed"`
		PokemonFound int  `json:"pokemon_found"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.PokemonFound != 1 {
		t.Fatalf("the sidecar read the save: got pokemon_found=%d want 1", got.PokemonFound)
	}
	if !got.Parsed {
		t.Fatal("a save whose Pokémon were just read must be reported as parsed")
	}

	list, err := f.store.SavesForOwner(context.Background(), f.ownerID(t, "ash"))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("identical bytes must stay one version: got %d", len(list))
	}
	if !list[0].Parsed || list[0].PokemonCount != 1 {
		t.Fatalf("the stored save must record what the parser found, or Save Data lists a Pokémon save as unreadable: parsed=%v count=%d",
			list[0].Parsed, list[0].PokemonCount)
	}
}

// ownerID resolves a username to its id.
func (f *saveVaultFixture) ownerID(t *testing.T, username string) string {
	t.Helper()
	u, err := f.store.UserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// sessionJar is a minimal cookie jar that presents one session cookie.
type sessionJar struct{ cookie *http.Cookie }

func (j *sessionJar) SetCookies(*url.URL, []*http.Cookie) {}
func (j *sessionJar) Cookies(*url.URL) []*http.Cookie     { return []*http.Cookie{j.cookie} }

// Two different saves must both be stored, never deduplicated: the dedup check
// is by content hash, and only truly identical bytes may answer "already
// stored". A QA pass reported every upload answering "already stored" even for
// a different game and trainer; this pins the correct behaviour.
func TestDifferentSavesAreNeverDeduplicated(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(fakeSidecar(t).URL))
	client := f.signIn(t, "ash")

	stored := func(res *http.Response) bool {
		t.Helper()
		defer res.Body.Close()
		var got struct {
			Stored bool `json:"stored"`
		}
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got.Stored
	}

	if !stored(f.upload(t, client, f.gameID, "one.sav", []byte("first save bytes"))) {
		t.Fatal("the first save must be stored")
	}
	if !stored(f.upload(t, client, f.gameID, "two.sav", []byte("second, different save bytes"))) {
		t.Fatal("a different save for the same game must be stored, not deduplicated")
	}
	if stored(f.upload(t, client, f.gameID, "one-again.sav", []byte("first save bytes"))) {
		t.Fatal("identical bytes are the one case that must deduplicate")
	}
}
