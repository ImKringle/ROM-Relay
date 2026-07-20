package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"romrelay/internal/catalog"
	"romrelay/internal/pokemon"
)

// TestDownloadPokemonIsOwnerScoped pins the "download the .pkm file"
// post-create option: the raw entity bytes are served back byte-for-byte, but
// only to the Pokémon's own owner, another signed-in account gets a 404, the
// same convention every other owned-resource route in this file uses.
func TestDownloadPokemonIsOwnerScoped(t *testing.T) {
	f := newSaveVaultFixture(t)
	ash := f.signIn(t, "ash")
	misty := f.signIn(t, "misty")

	ctx := context.Background()
	users, err := f.store.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ashID string
	for _, u := range users {
		if u.Username == "ash" {
			ashID = u.ID
		}
	}
	ids, _, _, err := f.store.SavePokemonInstances(ctx, ashID, []catalog.PokemonInstance{
		{Species: 25, SpeciesName: "Pikachu", Generation: 4, Binary: []byte("raw-pikachu-bytes")},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := misty.Get(f.server.URL + "/api/pokemon/" + ids[0] + "/download")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("another account's download attempt must 404, got %d", res.StatusCode)
	}

	res, err = ash.Get(f.server.URL + "/api/pokemon/" + ids[0] + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the owner's download must succeed, got %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "raw-pikachu-bytes" {
		t.Fatalf("expected the raw entity bytes back untouched, got %q", body)
	}
	if disp := res.Header.Get("Content-Disposition"); !strings.Contains(disp, "pk4") {
		t.Fatalf("expected a generation-appropriate filename, got %q", disp)
	}
}

// fakeEvictSidecar answers /parse with one occupied slot ("box:1:1", a
// Charmander) and /inject either 409 or 200 depending on the request's
// overwrite flag, enough to exercise withdrawPokehome's evict_to_home path
// without a real PKHeX.
func fakeEvictSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/parse":
			_, _ = w.Write([]byte(`{"game":"E","generation":3,"trainer":{"OT":"Brock"},"count":1,
"max_species":151,"dex":{"seen":[],"caught":[]},"box_count":1,"box_slot_count":5,
"pokemon":[{"slot":"box:1:1","species_id":4,"species":"Charmander","nickname":"CHARMANDER","level":5,
"pid":9999,"ot":"Brock","blob":"AAECAw==","moves":[]}]}`))
		case r.URL.Path == "/inject":
			var body struct {
				Box       int  `json:"box"`
				Slot      int  `json:"slot"`
				Overwrite bool `json:"overwrite"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Box == 1 && body.Slot == 1 && !body.Overwrite {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"slot occupied"}`))
				return
			}
			_, _ = w.Write([]byte(`{"save":"AAECAw==","slot":"box:1:1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestWithdrawEvictToHomeMovesTheOccupantInsteadOfRefusing pins the QA fix for
// "occupied box slots just block the withdrawal picker": without
// evict_to_home, an occupied destination slot still 409s exactly as before
// (no regression). With it set, the slot's current occupant is read out via
// the sidecar's /parse and deposited into the caller's own PokeHome before the
// overwrite, so nothing is silently discarded.
func TestWithdrawEvictToHomeMovesTheOccupantInsteadOfRefusing(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(fakeEvictSidecar(t).URL))
	client := f.signIn(t, "misty")

	// Upload a save, the fake sidecar's /parse response ingests it as the
	// destination save and also stores its one Pokémon (Charmander, the
	// occupant we expect to get evicted).
	uploadRes := f.upload(t, client, "", "emerald.sav", []byte("an emerald save"))
	defer uploadRes.Body.Close()
	var uploaded struct {
		SaveID string `json:"save_id"`
	}
	if err := json.NewDecoder(uploadRes.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.SaveID == "" {
		t.Fatal("upload did not report a save_id")
	}

	// A second, separately-deposited Pokémon to withdraw into the occupied
	// slot, inserted directly rather than through /api/pokemon, since
	// creating one for real would need the sidecar's /create endpoint too,
	// which this fake doesn't implement (not what this test is about).
	ctx := context.Background()
	users, err := f.store.Users(ctx)
	if err != nil || len(users) == 0 {
		t.Fatalf("expected the signed-in user to exist: %v", err)
	}
	var owner string
	for _, u := range users {
		if u.Username == "misty" {
			owner = u.ID
		}
	}
	if owner == "" {
		t.Fatal("could not find misty's user id")
	}
	ids, _, _, err := f.store.SavePokemonInstances(ctx, owner, []catalog.PokemonInstance{
		{Species: 7, SpeciesName: "Squirtle", Binary: []byte("squirtle-bytes")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DepositPokemonInstance(ctx, owner, ids[0]); err != nil {
		t.Fatal(err)
	}

	withdraw := func(evict bool) *http.Response {
		payload, _ := json.Marshal(map[string]any{
			"save_id": uploaded.SaveID, "box": 1, "slot": 1, "evict_to_home": evict,
		})
		req, err := http.NewRequest(http.MethodPost, f.server.URL+"/api/pokehome/"+ids[0]+"/withdraw", strings.NewReader(string(payload)))
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

	// Without evict_to_home, an occupied slot still refuses, no regression
	// on the existing behavior.
	blocked := withdraw(false)
	defer blocked.Body.Close()
	if blocked.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 without evict_to_home, got %d", blocked.StatusCode)
	}

	// With it set, the withdrawal succeeds and the slot's former occupant
	// (Charmander) is now itself deposited in misty's PokeHome.
	sent := withdraw(true)
	defer sent.Body.Close()
	if sent.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with evict_to_home, got %d", sent.StatusCode)
	}

	home, err := client.Get(f.server.URL + "/api/pokehome")
	if err != nil {
		t.Fatal(err)
	}
	defer home.Body.Close()
	var list struct {
		Pokemon []struct {
			SpeciesName string `json:"species_name"`
		} `json:"pokemon"`
	}
	if err := json.NewDecoder(home.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	var sawCharmander, sawSquirtle bool
	for _, mon := range list.Pokemon {
		if mon.SpeciesName == "Charmander" {
			sawCharmander = true
		}
		if mon.SpeciesName == "Squirtle" {
			sawSquirtle = true
		}
	}
	if !sawCharmander {
		t.Fatalf("the evicted occupant must now be deposited in PokeHome too: %#v", list.Pokemon)
	}
	if !sawSquirtle {
		t.Fatalf("withdrawing is a copy, not a move, the original deposit must remain: %#v", list.Pokemon)
	}
}
