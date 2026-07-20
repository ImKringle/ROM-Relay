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

// trainerSidecar fakes /parse with a full trainer object, trusted or not.
func trainerSidecar(t *testing.T, trusted bool) *httptest.Server {
	t.Helper()
	body := `{"game":"HG","generation":4,"count":1,"max_species":493,
"trainer":{"ot":"ASH","tid":1,"sid":2,"played_minutes":600,"money":5000,"money_max":999999,
"progress":{"kind":"badges","count":16,"max":16},
"hall_of_fame":{"clears":3,"teams":[{"members":[{"species":160,"level":48,"nickname":""}]}]},
"trusted":true,"notes":{}},
"dex":{"seen":[1,4],"caught":[1]},
"pokemon":[{"slot":"box","species_id":1,"species":"Bulbasaur","nickname":"Bulba","level":5,
"pid":1234,"ot":"ASH","blob":"AAECAw==","moves":[]}]}`
	if !trusted {
		body = `{"game":"CXD","generation":3,"count":0,"max_species":386,
"trainer":{"ot":"garbage","tid":1,"sid":2,"trusted":false,
"notes":{"trainer":"ROM Relay cannot read trainer data from GameCube saves."}},
"dex":{"seen":[],"caught":[]},"pokemon":[]}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/parse" {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestUploadingASaveRecordsThePlaythrough(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(trainerSidecar(t, true).URL))
	client := f.signIn(t, "ash")
	ctx := context.Background()

	res := f.upload(t, client, "", "hg.sav", []byte("savedata"))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d", res.StatusCode)
	}

	owner := f.ownerID(t, "ash")
	profiles, err := f.store.TrainerProfiles(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].OT != "ASH" || profiles[0].ProgressCount != 16 {
		t.Fatalf("the upload must record the playthrough: %#v", profiles)
	}
	if profiles[0].HOFClears == nil || *profiles[0].HOFClears != 3 {
		t.Fatalf("clears must be recorded: %#v", profiles[0].HOFClears)
	}

	// The dex rows are keyed by the trainer, so this run's dex is separable.
	caught, err := f.store.GameDexCaughtFor(ctx, owner, "HG", "1:2")
	if err != nil {
		t.Fatal(err)
	}
	if len(caught) != 1 || caught[0].Species != 1 {
		t.Fatalf("the run's dex must be keyed by its trainer: %#v", caught)
	}

	hof, err := f.store.TrainerHOF(ctx, owner, profiles[0].ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(hof) != 1 || hof[0].Species != 160 {
		t.Fatalf("the Hall of Fame team must be stored: %#v", hof)
	}
}

// An untrusted save contributes identity only, never money, progress, or a
// profile. Colosseum parses into garbage without throwing, so a profile built
// from it would be confident nonsense.
func TestAnUntrustedSaveRecordsNoProfile(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(trainerSidecar(t, false).URL))
	client := f.signIn(t, "wes")
	ctx := context.Background()

	res := f.upload(t, client, "", "colosseum.gci", []byte("gc-savedata"))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d", res.StatusCode)
	}

	profiles, err := f.store.TrainerProfiles(ctx, f.ownerID(t, "wes"))
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("an untrusted save must not become a profile: %#v", profiles)
	}
}

func TestUserdexIsPerUserAndRefusesAnonymous(t *testing.T) {
	f := newSaveVaultFixture(t)
	ash := f.signIn(t, "ash")
	misty := f.signIn(t, "misty")
	ctx := context.Background()

	if err := f.store.UpsertTrainerProfile(ctx, catalog.TrainerProfile{
		OwnerID: f.ownerID(t, "ash"), Game: "HG", TID: 1, SID: 2, OT: "ASH",
		Money: 5000, PlayedMinutes: 600, Trusted: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Ash sees his playthrough...
	res, err := ash.Get(f.server.URL + "/api/userdex")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("userdex: got %d", res.StatusCode)
	}
	var got struct {
		Profiles []catalog.TrainerProfile `json:"profiles"`
		Totals   struct {
			Money         int64 `json:"money"`
			PlayedMinutes int   `json:"played_minutes"`
		} `json:"totals"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 1 || got.Totals.Money != 5000 {
		t.Fatalf("ash's own profiles and aggregates: %#v", got)
	}

	// ...and Misty sees none of it. The Dex once read a "default" account for
	// everyone; this is that bug's regression test.
	res2, err := misty.Get(f.server.URL + "/api/userdex")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var mistys struct {
		Profiles []catalog.TrainerProfile `json:"profiles"`
	}
	if err := json.NewDecoder(res2.Body).Decode(&mistys); err != nil {
		t.Fatal(err)
	}
	if len(mistys.Profiles) != 0 {
		t.Fatalf("a user must never see another user's playthroughs: %#v", mistys.Profiles)
	}

	// The playthrough data stays behind the login.
	anon, err := http.Get(f.server.URL + "/api/userdex")
	if err != nil {
		t.Fatal(err)
	}
	anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Errorf("an anonymous request must be refused: got %d", anon.StatusCode)
	}

	// A profile another user owns is not found, not forbidden.
	other, err := misty.Get(f.server.URL + "/api/userdex/HG:1:2/dex")
	if err != nil {
		t.Fatal(err)
	}
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("another user's playthrough must be a 404: got %d", other.StatusCode)
	}

	// The owner's per-playthrough dex answers.
	own, err := ash.Get(f.server.URL + "/api/userdex/HG:1:2/dex")
	if err != nil {
		t.Fatal(err)
	}
	defer own.Body.Close()
	if own.StatusCode != http.StatusOK {
		t.Errorf("the owner's playthrough dex must answer: got %d", own.StatusCode)
	}
}
