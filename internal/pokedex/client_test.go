package pokedex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const speciesJSON = `{
  "name":"bellossom",
  "generation":{"name":"generation-ii"},
  "genera":[{"genus":"Blume","language":{"name":"de"}},{"genus":"Flower Pokémon","language":{"name":"en"}}],
  "evolution_chain":{"url":"https://pokeapi.co/api/v2/evolution-chain/18/"},
  "is_legendary":false,"is_mythical":false,
  "pokedex_numbers":[{"entry_number":182,"pokedex":{"name":"national"}},{"entry_number":86,"pokedex":{"name":"original-johto"}}],
  "flavor_text_entries":[{"flavor_text":"Ein Text","language":{"name":"de"}},{"flavor_text":"BELLOSSOM gather\nat times.","language":{"name":"en"}}]
}`

func TestClientReadsTheEnglishGenusAndFlavourText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pokemon-species/182" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(speciesJSON))
	}))
	defer server.Close()

	got, err := NewClient(server.URL+"/").Species(context.Background(), 182)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "bellossom" || got.Generation.Name != "generation-ii" {
		t.Fatalf("species mismatch: %#v", got)
	}
	if g := got.EnglishGenus(); g != "Flower Pokémon" {
		t.Errorf("the English genus must be chosen, not the first one: %q", g)
	}
	// Flavour text arrives with hard line breaks baked in for the game's text box.
	if f := got.EnglishFlavorText(); f != "BELLOSSOM gather at times." {
		t.Errorf("flavour text must be the English one with newlines flattened: %q", f)
	}
	if n := got.RegionalEntry("original-johto"); n != 86 {
		t.Errorf("regional entry: got %d want 86", n)
	}
}

// The rejected library never checked status and unmarshalled the error body,
// silently producing a blank species. The body here is valid JSON that would
// unmarshal cleanly into SpeciesResponse, so this test fails unless the client
// checks the status code itself.
func TestClientTreatsAnErrorStatusAsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not found."}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL+"/").Species(context.Background(), 9999)
	if err == nil {
		t.Fatal("a 404 must be an error, never a zero-value species")
	}
}

// The ingest runs in the background and shutdown cancels and waits for it.
func TestClientHonoursContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(speciesJSON))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewClient(server.URL+"/").Species(ctx, 182); err == nil {
		t.Fatal("a cancelled context must abort the request")
	}
}

// The local source is the api-data checkout: the same JSON, one index.json per
// endpoint. The client must resolve API URLs into that tree and treat a missing
// file as the 404 it is.
func TestLocalClientReadsApiDataTree(t *testing.T) {
	root := t.TempDir()
	speciesDir := filepath.Join(root, "pokemon-species", "44")
	if err := os.MkdirAll(speciesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"gloom","generation":{"name":"generation-i"},
  "evolution_chain":{"url":"https://pokeapi.co/api/v2/evolution-chain/18/"},
  "genera":[{"genus":"Weed Pokémon","language":{"name":"en"}}]}`
	if err := os.WriteFile(filepath.Join(speciesDir, "index.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	client := NewLocalClient(root)
	species, err := client.Species(context.Background(), 44)
	if err != nil {
		t.Fatal(err)
	}
	if species.Name != "gloom" || species.EnglishGenus() != "Weed Pokémon" {
		t.Fatalf("local read returned %+v", species)
	}
	// The chain URL is absolute; it must resolve inside the same tree.
	if _, err := client.EvolutionChain(context.Background(), species.EvolutionChain.URL); err == nil {
		t.Fatal("a chain file that does not exist must be an error, not a blank chain")
	}
	if _, err := client.Species(context.Background(), 45); err == nil {
		t.Fatal("a species file that does not exist must be an error, not a blank species")
	}
}
