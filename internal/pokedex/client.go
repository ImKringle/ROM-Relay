// Package pokedex fetches static Pokédex reference data from PokeAPI and stores
// it locally. It is a metadata provider in the same shape as internal/metadata's
// Libretro and IGDB: config-gated, background, and never on the request path.
package pokedex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is PokeAPI v2. Tests pass their own.
const DefaultBaseURL = "https://pokeapi.co/api/v2/"

// Client is a small PokeAPI reader.
//
// It is hand-written rather than using mtslzr/pokeapi-go, which takes no context
// (so a long ingest could not be cancelled at shutdown) and never checks the HTTP
// status before unmarshalling, which turns a 404 into a blank species that looks
// real. Only three endpoints are needed.
//
// It reads from one of two sources: the PokeAPI/api-data checkout on disk (the
// third_party/api-data submodule, the same JSON, one file per endpoint), or
// the live API. Local wins when configured: it is faster, versioned, and works
// on a closed LAN.
type Client struct {
	baseURL   string
	http      *http.Client
	localRoot string
}

func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 30 * time.Second}}
}

// NewLocalClient reads from an api-data checkout instead of the network. root
// is the directory that contains the endpoint tree, e.g.
// third_party/api-data/data/api/v2.
func NewLocalClient(root string) *Client {
	return &Client{baseURL: DefaultBaseURL, localRoot: root}
}

// IsLocal reports whether this client reads an on-disk checkout rather than
// the network. The ingest uses it to decide whether "be polite to a free
// API" throttling still applies, it doesn't for files already on disk.
func (c *Client) IsLocal() bool { return c.localRoot != "" }

type named struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// SpeciesResponse is the subset of /pokemon-species we use.
type SpeciesResponse struct {
	Name           string `json:"name"`
	Generation     named  `json:"generation"`
	EvolutionChain struct {
		URL string `json:"url"`
	} `json:"evolution_chain"`
	IsLegendary bool `json:"is_legendary"`
	IsMythical  bool `json:"is_mythical"`
	Genera      []struct {
		Genus    string `json:"genus"`
		Language named  `json:"language"`
	} `json:"genera"`
	PokedexNumbers []struct {
		EntryNumber int   `json:"entry_number"`
		Pokedex     named `json:"pokedex"`
	} `json:"pokedex_numbers"`
	FlavorTextEntries []struct {
		FlavorText string `json:"flavor_text"`
		Language   named  `json:"language"`
	} `json:"flavor_text_entries"`
	EggGroups    []named `json:"egg_groups"`
	GenderRate   int     `json:"gender_rate"`
	HatchCounter int     `json:"hatch_counter"`
}

// EggGroupNames is "monster,dragon", comma-joined, the same convention
// PokemonResponse.TypeList uses for "/" between a species' two types.
func (s SpeciesResponse) EggGroupNames() string {
	names := make([]string, 0, len(s.EggGroups))
	for _, g := range s.EggGroups {
		names = append(names, g.Name)
	}
	return strings.Join(names, ",")
}

// EnglishGenus is the "Flower Pokémon" line. PokeAPI returns every language, and
// the English one is not necessarily first.
func (s SpeciesResponse) EnglishGenus() string {
	for _, g := range s.Genera {
		if g.Language.Name == "en" {
			return g.Genus
		}
	}
	return ""
}

// EnglishFlavorText is the first English entry, with the newlines the games use
// to fit a text box flattened into ordinary prose.
func (s SpeciesResponse) EnglishFlavorText() string {
	for _, f := range s.FlavorTextEntries {
		if f.Language.Name != "en" {
			continue
		}
		clean := strings.ReplaceAll(f.FlavorText, "\n", " ")
		clean = strings.ReplaceAll(clean, "\f", " ")
		return strings.Join(strings.Fields(clean), " ")
	}
	return ""
}

// RegionalEntry is this species' number in one named Pokédex, or 0.
func (s SpeciesResponse) RegionalEntry(pokedex string) int {
	for _, p := range s.PokedexNumbers {
		if p.Pokedex.Name == pokedex {
			return p.EntryNumber
		}
	}
	return 0
}

// PokemonResponse is the subset of /pokemon we use.
type PokemonResponse struct {
	Height int `json:"height"`
	Weight int `json:"weight"`
	Types  []struct {
		Slot int   `json:"slot"`
		Type named `json:"type"`
	} `json:"types"`
	Moves []struct {
		Move                named `json:"move"`
		VersionGroupDetails []struct {
			LevelLearnedAt  int   `json:"level_learned_at"`
			MoveLearnMethod named `json:"move_learn_method"`
			VersionGroup    named `json:"version_group"`
		} `json:"version_group_details"`
	} `json:"moves"`
	Stats []struct {
		BaseStat int   `json:"base_stat"`
		Stat     named `json:"stat"`
	} `json:"stats"`
	Abilities []struct {
		Ability  named `json:"ability"`
		IsHidden bool  `json:"is_hidden"`
	} `json:"abilities"`
}

// AbilityNames splits a species' abilities into its regular slots ("/"-joined,
// slot order) and its hidden ability (empty if it has none), the same
// distinction PKHeX itself draws, since a hidden ability is granted a
// different way in-game than picking between two regular ones.
func (p PokemonResponse) AbilityNames() (regular string, hidden string) {
	var names []string
	for _, a := range p.Abilities {
		if a.IsHidden {
			hidden = a.Ability.Name
			continue
		}
		names = append(names, a.Ability.Name)
	}
	return strings.Join(names, "/"), hidden
}

// BaseStat is a species' base value for one of PokeAPI's six stat names
// ("hp", "attack", "defense", "special-attack", "special-defense", "speed").
// Zero for an unrecognised name rather than an error: a stat this build does
// not know about should be silently ignored, not abort the whole ingest.
func (p PokemonResponse) BaseStat(name string) int {
	for _, s := range p.Stats {
		if s.Stat.Name == name {
			return s.BaseStat
		}
	}
	return 0
}

// MoveResponse is the subset of /move we use: one attack's own reference data,
// shared by every species that can learn it.
type MoveResponse struct {
	Name        string `json:"name"`
	Power       int    `json:"power"`
	Accuracy    int    `json:"accuracy"`
	Type        named  `json:"type"`
	DamageClass named  `json:"damage_class"`
}

// MoveByURL takes the absolute URL a pokemon response's moves entry carries
// (the same shape EvolutionChain takes one for): the api-data checkout keys
// each move by numeric id, not name, so the URL from the response is the only
// reliable path to it.
func (c *Client) MoveByURL(ctx context.Context, url string) (MoveResponse, error) {
	var out MoveResponse
	err := c.get(ctx, url, &out)
	return out, err
}

// TypeList is "grass" or "grass/poison", in slot order.
func (p PokemonResponse) TypeList() string {
	names := make([]string, 0, len(p.Types))
	for _, t := range p.Types {
		names = append(names, t.Type.Name)
	}
	return strings.Join(names, "/")
}

// ChainLink is one node of an evolution chain; it nests into its evolutions.
type ChainLink struct {
	Species          named       `json:"species"`
	EvolutionDetails []Detail    `json:"evolution_details"`
	EvolvesTo        []ChainLink `json:"evolves_to"`
}

// Detail is the requirement to reach a node. Every field is optional; PokeAPI
// sends the lot with nulls.
type Detail struct {
	Trigger               *named `json:"trigger"`
	MinLevel              int    `json:"min_level"`
	Item                  *named `json:"item"`
	HeldItem              *named `json:"held_item"`
	KnownMove             *named `json:"known_move"`
	Location              *named `json:"location"`
	MinHappiness          int    `json:"min_happiness"`
	MinAffection          int    `json:"min_affection"`
	MinBeauty             int    `json:"min_beauty"`
	NeedsOverworldRain    bool   `json:"needs_overworld_rain"`
	TimeOfDay             string `json:"time_of_day"`
	Gender                *int   `json:"gender"`
	RelativePhysicalStats *int   `json:"relative_physical_stats"`
	TradeSpecies          *named `json:"trade_species"`
	PartySpecies          *named `json:"party_species"`
	TurnUpsideDown        bool   `json:"turn_upside_down"`
}

type ChainResponse struct {
	Chain ChainLink `json:"chain"`
}

func (c *Client) Species(ctx context.Context, dex int) (SpeciesResponse, error) {
	var out SpeciesResponse
	err := c.get(ctx, c.baseURL+"pokemon-species/"+strconv.Itoa(dex), &out)
	return out, err
}

func (c *Client) Pokemon(ctx context.Context, dex int) (PokemonResponse, error) {
	var out PokemonResponse
	err := c.get(ctx, c.baseURL+"pokemon/"+strconv.Itoa(dex), &out)
	return out, err
}

// EvolutionChain takes the absolute URL the species response carries, since the
// chain id is not derivable from the species number.
func (c *Client) EvolutionChain(ctx context.Context, url string) (ChainResponse, error) {
	var out ChainResponse
	err := c.get(ctx, url, &out)
	return out, err
}

// EncounterArea is one element of /pokemon/{id}/encounters: a location area and
// the per-version encounter details inside it.
type EncounterArea struct {
	LocationArea   named `json:"location_area"`
	VersionDetails []struct {
		Version          named `json:"version"`
		MaxChance        int   `json:"max_chance"`
		EncounterDetails []struct {
			MinLevel        int     `json:"min_level"`
			MaxLevel        int     `json:"max_level"`
			Chance          int     `json:"chance"`
			Method          named   `json:"method"`
			ConditionValues []named `json:"condition_values"`
		} `json:"encounter_details"`
	} `json:"version_details"`
}

// Encounters fetches where a species appears in the wild, per version.
func (c *Client) Encounters(ctx context.Context, dex int) ([]EncounterArea, error) {
	var out []EncounterArea
	err := c.get(ctx, c.baseURL+"pokemon/"+strconv.Itoa(dex)+"/encounters", &out)
	return out, err
}

// get is the only request path. It carries the context (so the ingest can be
// cancelled) and refuses a non-200 (so a failure never becomes a blank record).
func (c *Client) get(ctx context.Context, url string, out any) error {
	if c.localRoot != "" {
		return c.readLocal(url, out)
	}
	return c.fetch(ctx, url, out)
}

// readLocal resolves an API URL inside the api-data tree: the dump mirrors the
// API's paths exactly, one index.json per endpoint, so
// ".../api/v2/pokemon-species/182/" is "<root>/pokemon-species/182/index.json".
// A missing file is the local equivalent of a 404.
func (c *Client) readLocal(url string, out any) error {
	rel := url
	if at := strings.Index(rel, "/api/v2/"); at >= 0 {
		rel = rel[at+len("/api/v2/"):]
	}
	rel = strings.Trim(rel, "/")
	if rel == "" || strings.Contains(rel, "..") {
		return fmt.Errorf("pokeapi data: refusing path %q", url)
	}
	path := filepath.Join(c.localRoot, filepath.FromSlash(rel), "index.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("pokeapi data %s: %w", rel, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) fetch(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("contact pokeapi: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pokeapi %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}
