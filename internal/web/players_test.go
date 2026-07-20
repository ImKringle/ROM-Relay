package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"romrelay/internal/catalog"
)

func TestAdminSaveListingIsScopedToTheChosenUser(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")
	ash := f.signIn(t, "ash")
	misty := f.signIn(t, "misty")
	f.upload(t, ash, f.gameID, "ash.sav", []byte("ash's save")).Body.Close()
	f.upload(t, misty, f.gameID, "misty.sav", []byte("misty's save")).Body.Close()

	ashID := f.ownerID(t, "ash")
	res, err := admin.Get(f.server.URL + "/api/admin/users/" + ashID + "/saves")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an admin must be able to list a user's saves: got %d", res.StatusCode)
	}
	var body struct {
		Saves []struct {
			FileName string `json:"file_name"`
		} `json:"saves"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Saves) != 1 || body.Saves[0].FileName != "ash.sav" {
		t.Fatalf("must see only ash's save, not misty's: got %+v", body.Saves)
	}
}

func TestPlayerDataEndpointsRequireAdmin(t *testing.T) {
	f := newSaveVaultFixture(t)
	plain := f.signIn(t, "ash")
	ashID := f.ownerID(t, "ash")

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/admin/users/" + ashID + "/saves"},
		{http.MethodGet, "/api/admin/users/" + ashID + "/dex"},
		{http.MethodGet, "/admin/users/" + ashID + "/saves/anything/download"},
		{http.MethodDelete, "/api/admin/users/" + ashID + "/saves/anything"},
		{http.MethodPost, "/api/admin/users/" + ashID + "/clear-trainers"},
		{http.MethodGet, "/api/admin/users/" + ashID + "/pokehome"},
		{http.MethodDelete, "/api/admin/users/" + ashID + "/pokehome/anything"},
	}
	for _, route := range routes {
		req, err := http.NewRequest(route.method, f.server.URL+route.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-CSRF-Token", f.ui.csrf)
		res, err := plain.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: a plain user must not reach player data: got %d", route.method, route.path, res.StatusCode)
		}
	}
}

// TestAdminCanListAndDeletePokehomeDeposits pins the QA fix for Additional
// Notes items 2/3/5: an admin previously had no way to see or clear the
// PokeHome deposits behind a "N Pokémon stored" count that a dex/save clear
// deliberately leaves untouched (see ClearTrainerDex's own comment). Now they
// can list them and delete one directly.
func TestAdminCanListAndDeletePokehomeDeposits(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")
	f.signIn(t, "misty")
	mistyID := f.ownerID(t, "misty")

	ctx := context.Background()
	ids, _, _, err := f.store.SavePokemonInstances(ctx, mistyID, []catalog.PokemonInstance{
		{Species: 54, SpeciesName: "Psyduck", Binary: []byte("psyduck")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DepositPokemonInstance(ctx, mistyID, ids[0]); err != nil {
		t.Fatal(err)
	}

	listRes, err := admin.Get(f.server.URL + "/api/admin/users/" + mistyID + "/pokehome")
	if err != nil {
		t.Fatal(err)
	}
	defer listRes.Body.Close()
	var list struct {
		Count   int `json:"count"`
		Pokemon []struct {
			SpeciesName string `json:"species_name"`
		} `json:"pokemon"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || list.Pokemon[0].SpeciesName != "Psyduck" {
		t.Fatalf("expected Misty's one deposit listed: %+v", list)
	}

	req, err := http.NewRequest(http.MethodDelete, f.server.URL+"/api/admin/users/"+mistyID+"/pokehome/"+ids[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CSRF-Token", f.ui.csrf)
	delRes, err := admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer delRes.Body.Close()
	if delRes.StatusCode != http.StatusOK {
		t.Fatalf("admin delete of a deposit must succeed: got %d", delRes.StatusCode)
	}

	if _, err := f.store.PokemonInstanceByID(ctx, mistyID, ids[0]); err == nil {
		t.Fatal("the deposit must actually be gone after the admin delete")
	}
}

// TestClearUserTrainersEndpointScopesToSelectedTrainers pins the Flow Audit
// fix for QA item 3 at the HTTP layer: posting selected trainer ids to
// clear-trainers must remove only those playthroughs, leaving an unselected
// one for the same user alone.
func TestClearUserTrainersEndpointScopesToSelectedTrainers(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")
	f.signIn(t, "ash")
	ashID := f.ownerID(t, "ash")

	ctx := context.Background()
	doomed := catalog.TrainerProfile{OwnerID: ashID, Game: "HG", TID: 1, SID: 2, OT: "ASH", PlayedMinutes: 600}
	spared := catalog.TrainerProfile{OwnerID: ashID, Game: "HG", TID: 9, SID: 9, OT: "NEW", PlayedMinutes: 10}
	if err := f.store.UpsertTrainerProfile(ctx, doomed); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertTrainerProfile(ctx, spared); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{"trainer_ids": []string{doomed.ID()}})
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/api/admin/users/"+ashID+"/clear-trainers", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", f.ui.csrf)
	res, err := admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clearing selected trainers must succeed for an admin: got %d", res.StatusCode)
	}

	list, err := f.store.TrainerProfiles(ctx, ashID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID() != spared.ID() {
		t.Fatalf("only the selected playthrough must be cleared: %#v", list)
	}
}
