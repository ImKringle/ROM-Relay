package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"romrelay/internal/pokemon"
)

// fakeIllegalSidecar always rejects /create and /edit the way the real
// sidecar's LegalityViolations does for an illegal Pokémon: 409 with an
// error summary and a per-line report.
func fakeIllegalSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/create", "/edit":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"this Pokémon is not legal","report":["Ability is not valid for this species and origin.","Encounter type is not valid for this Pokémon."]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestCreatePokemonForwardsLegalityReport pins the QA fix "Pokemon Creator has
// no validation": PKHeX's own LegalityAnalysis (see the sidecar) is a hard
// block per product decision, not a warning, and its per-line violations must
// reach the caller intact rather than collapsing into one generic 500.
func TestCreatePokemonForwardsLegalityReport(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetPokemonClient(pokemon.NewClient(fakeIllegalSidecar(t).URL))
	client := f.signIn(t, "ash")

	payload, _ := json.Marshal(map[string]any{"generation": 9, "species": 25, "level": 5})
	res, err := client.Post(f.server.URL+"/api/pokemon", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("an illegal Pokémon must be rejected with 409, got %d", res.StatusCode)
	}
	var body struct {
		Error  string   `json:"error"`
		Report []string `json:"report"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Report) != 2 {
		t.Fatalf("expected the sidecar's 2-line legality report forwarded intact, got %#v", body.Report)
	}
}
