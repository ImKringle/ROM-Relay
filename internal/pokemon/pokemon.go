// Package pokemon ingests Pokémon save files by delegating the parsing to the
// PKHeX .NET sidecar and mapping the result into catalog storage records.
package pokemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"romrelay/internal/catalog"
)

// Client talks to the PKHeX save-parsing sidecar over HTTP.
type Client struct {
	baseURL   string
	http      *http.Client
	speciesMu sync.Mutex
	species   []Species

	refMu sync.Mutex
	refs  map[string][]Ref // keyed by wire key: "abilities", "items", "moves"
}

// Species is one National Pokédex entry from the sidecar.
type Species struct {
	Dex  int    `json:"dex"`
	Name string `json:"name"`
}

// Ref is one entry in a name-indexed reference table, an ability, held
// item, or move, the same {id, name} pairing Species already is for the
// Pokédex, just not species-specific.
type Ref struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// NewClient returns a client for the sidecar at baseURL. A blank baseURL yields a
// client whose Available reports false, so callers can disable Pokémon features.
func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 2 * time.Minute}}
}

// Available reports whether a sidecar is configured.
func (c *Client) Available() bool { return c != nil && c.baseURL != "" }

type stats struct {
	HP, ATK, DEF, SPA, SPD, SPE int
}

func (s stats) toMap() map[string]int {
	return map[string]int{"hp": s.HP, "atk": s.ATK, "def": s.DEF, "spa": s.SPA, "spd": s.SPD, "spe": s.SPE}
}

type parsedMove struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type parsedMon struct {
	Slot      string       `json:"slot"`
	SpeciesID int          `json:"species_id"`
	Species   string       `json:"species"`
	Nickname  string       `json:"nickname"`
	Level     int          `json:"level"`
	Nature    string       `json:"nature"`
	Ability   string       `json:"ability"`
	Shiny     bool         `json:"shiny"`
	Form      int          `json:"form"`
	Gender    string       `json:"gender"`
	IsEgg     bool         `json:"is_egg"`
	HeldItem  string       `json:"held_item"`
	Ball      int          `json:"ball"`
	PID       int64        `json:"pid"`
	OT        string       `json:"ot"`
	IVs       stats        `json:"ivs"`
	EVs       stats        `json:"evs"`
	Moves     []parsedMove `json:"moves"`
	Blob      string       `json:"blob"`
}

// Progress is a playthrough's headline progress. It is one field with a kind
// rather than a badge count because Alola has no gyms: gens 1-6 report "badges",
// gen 7 reports "trials", and both occupy the same slot on the card.
type Progress struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Max   int    `json:"max"`
}

type HOFMember struct {
	Species  int    `json:"species"`
	Level    int    `json:"level"`
	Nickname string `json:"nickname"`
}

type HOFTeam struct {
	Members []HOFMember `json:"members"`
}

// HallOfFame is what a save records about beating the game. Clears is a pointer
// because nil ("this game does not count") and 0 ("never won") are different
// claims that must never render the same.
type HallOfFame struct {
	Clears *int      `json:"clears"`
	Teams  []HOFTeam `json:"teams"`
}

// Trainer is the trainer behind one playthrough. Notes maps a field name to the
// reason it is absent; the sidecar authors them, because it is the only
// component that knows the game.
type Trainer struct {
	OT            string            `json:"ot"`
	TID           int               `json:"tid"`
	SID           int               `json:"sid"`
	Gender        int               `json:"gender"`
	Language      int               `json:"language"`
	PlayedMinutes int               `json:"played_minutes"`
	Money         int64             `json:"money"`
	MoneyMax      int64             `json:"money_max"`
	Progress      *Progress         `json:"progress"`
	HallOfFame    *HallOfFame       `json:"hall_of_fame"`
	Trusted       bool              `json:"trusted"`
	Notes         map[string]string `json:"notes"`
}

type parseResult struct {
	Game       string  `json:"game"`
	Generation int     `json:"generation"`
	Trainer    Trainer `json:"trainer"`
	Count      int     `json:"count"`
	MaxSpecies int     `json:"max_species"`
	Dex        struct {
		Seen   []int `json:"seen"`
		Caught []int `json:"caught"`
	} `json:"dex"`
	Pokemon []parsedMon `json:"pokemon"`
	// BoxCount/BoxSlotCount bound a save's PC: /parse only ever reports
	// occupied slots (a save's own AddMon skips empty ones), so these are what
	// let a caller compute the empty slots as everything unmentioned.
	BoxCount     int `json:"box_count"`
	BoxSlotCount int `json:"box_slot_count"`
}

// ErrUnreadableSave reports a save the sidecar ran against and could not read,
// as opposed to a sidecar that never answered. Callers need the difference: one
// is a plain non-Pokémon save, the other is an outage to fix.
var ErrUnreadableSave = errors.New("not a save the Pokémon parser can read")

// Summary describes an ingested save, including the save's own Pokédex flags for
// the true regional dex.
type Summary struct {
	Game       string  `json:"game"`
	Generation int     `json:"generation"`
	Trainer    Trainer `json:"trainer"`
	Found      int     `json:"pokemon_found"`
	Seen       []int   `json:"-"`
	Caught     []int   `json:"-"`
	// MaxSpecies is the highest National Pokédex number this game knows about, so
	// its completion can be measured against its own dex rather than the modern
	// National one.
	MaxSpecies int `json:"-"`
}

// SpeciesList returns the National Pokédex (number and name) from the sidecar,
// cached after the first call since it is static for a given PKHeX version.
func (c *Client) SpeciesList(ctx context.Context) ([]Species, error) {
	if !c.Available() {
		return nil, fmt.Errorf("pokemon sidecar is not configured")
	}
	c.speciesMu.Lock()
	defer c.speciesMu.Unlock()
	if c.species != nil {
		return c.species, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/species", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact pokemon sidecar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("species list unavailable: %s", resp.Status)
	}
	var payload struct {
		Species []Species `json:"species"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode species list: %w", err)
	}
	c.species = payload.Species
	return c.species, nil
}

// Ingest sends a save file to the sidecar, maps the parsed Pokémon into catalog
// records (decoding the raw entity blob), and returns them with a summary.
func (c *Client) Ingest(ctx context.Context, saveBytes []byte, sourceName string) ([]catalog.PokemonInstance, Summary, error) {
	if !c.Available() {
		return nil, Summary{}, fmt.Errorf("pokemon sidecar is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/parse", bytes.NewReader(saveBytes))
	if err != nil {
		return nil, Summary{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, Summary{}, fmt.Errorf("contact pokemon sidecar: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		message := string(bytes.TrimSpace(body))
		if message == "" {
			message = resp.Status
		}
		// A 4xx is the sidecar's verdict on the save itself; anything else means
		// the sidecar could not do its job, which is not the save's fault.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, Summary{}, fmt.Errorf("%w: %s", ErrUnreadableSave, message)
		}
		return nil, Summary{}, fmt.Errorf("pokemon sidecar failed: %s", message)
	}
	var result parseResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, Summary{}, fmt.Errorf("decode sidecar response: %w", err)
	}
	instances := make([]catalog.PokemonInstance, 0, len(result.Pokemon))
	for _, m := range result.Pokemon {
		inst, decodeErr := pokemonFromParsed(m, result.Generation)
		if decodeErr != nil {
			return nil, Summary{}, decodeErr
		}
		inst.OriginGame, inst.SourceSave = result.Game, sourceName
		instances = append(instances, inst)
	}
	return instances, Summary{
		Game: result.Game, Generation: result.Generation, Trainer: result.Trainer,
		Found: len(instances), Seen: result.Dex.Seen, Caught: result.Dex.Caught,
		MaxSpecies: result.MaxSpecies,
	}, nil
}

// pokemonFromParsed maps one sidecar-reported Pokémon into the catalog record
// shape, decoding its raw entity blob. Shared by Ingest (a save's whole box
// walk) and Edit (a single edited entity) so the two paths cannot drift on
// what a "parsed Pokémon" becomes in storage.
func pokemonFromParsed(m parsedMon, generation int) (catalog.PokemonInstance, error) {
	blob, err := base64.StdEncoding.DecodeString(m.Blob)
	if err != nil {
		return catalog.PokemonInstance{}, fmt.Errorf("decode entity blob: %w", err)
	}
	moves := make([]catalog.PokemonMove, 0, len(m.Moves))
	for _, mv := range m.Moves {
		moves = append(moves, catalog.PokemonMove{ID: mv.ID, Name: mv.Name})
	}
	return catalog.PokemonInstance{
		Species: m.SpeciesID, SpeciesName: m.Species, Nickname: m.Nickname, Level: m.Level,
		Nature: m.Nature, Ability: m.Ability, Gender: m.Gender, Shiny: m.Shiny, IsEgg: m.IsEgg,
		Form: m.Form, Generation: generation, IVs: m.IVs.toMap(), EVs: m.EVs.toMap(),
		Moves: moves, HeldItem: m.HeldItem, Ball: m.Ball, PID: m.PID, OriginOT: m.OT, OriginSlot: m.Slot, Binary: blob,
	}, nil
}

// SidecarStatusError reports a non-2xx response from the sidecar, carrying
// its status code and message through so a caller can forward the sidecar's
// own verdict, a 409 conversion failure, a 400 unreadable save, a 409
// occupied slot, a 409 legality rejection, instead of collapsing every
// failure into "internal error." Report carries the sidecar's per-line
// legality violations (see LegalityViolations in the sidecar), when there are
// any; every other error leaves it nil.
type SidecarStatusError struct {
	StatusCode int
	Message    string
	Report     []string
}

func (e *SidecarStatusError) Error() string { return e.Message }

func sidecarError(status int, body []byte) error {
	var payload struct {
		Error  string   `json:"error"`
		Report []string `json:"report"`
	}
	_ = json.Unmarshal(body, &payload)
	msg := payload.Error
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &SidecarStatusError{StatusCode: status, Message: msg, Report: payload.Report}
}

func (c *Client) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact pokemon sidecar: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, sidecarError(resp.StatusCode, body)
	}
	return body, nil
}

type injectRequest struct {
	Entity     string `json:"entity"`
	Generation int    `json:"generation"`
	Save       string `json:"save"`
	Box        int    `json:"box"`
	Slot       int    `json:"slot"`
	Overwrite  bool   `json:"overwrite,omitempty"`
}

type injectResult struct {
	Save string `json:"save"`
	Slot string `json:"slot"`
}

// Inject copies a stored Pokémon's raw bytes into a destination save's chosen
// box slot, delegating any cross-generation structural conversion entirely
// to PKHeX (see the sidecar's POST /inject), there is no generation-match
// gate here. Returns the destination save's new bytes and the slot actually
// written, which always echoes what was requested: injection either lands
// exactly there or the whole call fails with nothing written.
// overwrite bypasses the sidecar's occupied-slot 409: the caller (see
// withdrawPokehome's evict_to_home option) is expected to have already read
// out and stored whatever was there via OccupantAt before setting this.
func (c *Client) Inject(ctx context.Context, entity []byte, generation int, save []byte, box, slot int, overwrite bool) (newSave []byte, wroteSlot string, err error) {
	if !c.Available() {
		return nil, "", fmt.Errorf("pokemon sidecar is not configured")
	}
	body, err := c.postJSON(ctx, "/inject", injectRequest{
		Entity: base64.StdEncoding.EncodeToString(entity), Generation: generation,
		Save: base64.StdEncoding.EncodeToString(save), Box: box, Slot: slot, Overwrite: overwrite,
	})
	if err != nil {
		return nil, "", err
	}
	var result injectResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, "", fmt.Errorf("decode sidecar response: %w", err)
	}
	newSave, err = base64.StdEncoding.DecodeString(result.Save)
	if err != nil {
		return nil, "", fmt.Errorf("decode injected save: %w", err)
	}
	return newSave, result.Slot, nil
}

// StatEdit changes a subset of a Pokémon's six IVs or EVs; a nil field means
// "leave unchanged."
type StatEdit struct{ HP, ATK, DEF, SPA, SPD, SPE *int }

type statEditWire struct {
	HP  *int `json:"hp,omitempty"`
	ATK *int `json:"atk,omitempty"`
	DEF *int `json:"def,omitempty"`
	SPA *int `json:"spa,omitempty"`
	SPD *int `json:"spd,omitempty"`
	SPE *int `json:"spe,omitempty"`
}

func (s *StatEdit) wire() *statEditWire {
	if s == nil {
		return nil
	}
	return &statEditWire{HP: s.HP, ATK: s.ATK, DEF: s.DEF, SPA: s.SPA, SPD: s.SPD, SPE: s.SPE}
}

// EditFields is what a caller may change on a stored Pokémon in one /edit
// call. Every field is a pointer (or nil slice for Moves); unset means
// "leave unchanged", see the sidecar's ApplyEdits for exact semantics.
// Nature, Ability, and HeldItem take the same display-name strings /parse
// already reports, matching what PokemonInstance itself stores, no separate
// numeric-id contract for a caller to keep in sync.
type EditFields struct {
	Nickname            *string
	Level               *int
	Nature              *string
	Ability             *string
	Gender              *string
	Shiny               *bool
	Form                *int
	HeldItem            *string
	Ball                *int
	OriginalTrainerName *string
	IVs                 *StatEdit
	EVs                 *StatEdit
	Moves               []int
}

type editRequest struct {
	Entity              string        `json:"entity"`
	Generation          int           `json:"generation"`
	Nickname            *string       `json:"nickname,omitempty"`
	Level               *int          `json:"level,omitempty"`
	Nature              *string       `json:"nature,omitempty"`
	Ability             *string       `json:"ability,omitempty"`
	Gender              *string       `json:"gender,omitempty"`
	Shiny               *bool         `json:"shiny,omitempty"`
	Form                *int          `json:"form,omitempty"`
	HeldItem            *string       `json:"held_item,omitempty"`
	Ball                *int          `json:"ball,omitempty"`
	OriginalTrainerName *string       `json:"original_trainer_name,omitempty"`
	IVs                 *statEditWire `json:"ivs,omitempty"`
	EVs                 *statEditWire `json:"evs,omitempty"`
	Moves               []int         `json:"moves,omitempty"`
}

// Edit applies field changes to a stored Pokémon via the sidecar and maps the
// result back into the same record shape Ingest produces, so a caller
// overwrites every denormalized column in one store call rather than
// patching fields piecemeal and risking drift between what was asked for and
// what PKHeX actually wrote (a shiny toggle rewrites the PID, which a partial
// patch would miss).
func (c *Client) Edit(ctx context.Context, entity []byte, generation int, fields EditFields) (catalog.PokemonInstance, error) {
	if !c.Available() {
		return catalog.PokemonInstance{}, fmt.Errorf("pokemon sidecar is not configured")
	}
	body, err := c.postJSON(ctx, "/edit", editRequest{
		Entity: base64.StdEncoding.EncodeToString(entity), Generation: generation,
		Nickname: fields.Nickname, Level: fields.Level, Nature: fields.Nature, Ability: fields.Ability,
		Gender: fields.Gender, Shiny: fields.Shiny, Form: fields.Form, HeldItem: fields.HeldItem,
		Ball: fields.Ball, OriginalTrainerName: fields.OriginalTrainerName,
		IVs: fields.IVs.wire(), EVs: fields.EVs.wire(), Moves: fields.Moves,
	})
	if err != nil {
		return catalog.PokemonInstance{}, err
	}
	var m parsedMon
	if err := json.Unmarshal(body, &m); err != nil {
		return catalog.PokemonInstance{}, fmt.Errorf("decode sidecar response: %w", err)
	}
	return pokemonFromParsed(m, generation)
}

// BoxOccupant is one occupied box slot in a save, as reported by the
// sidecar's /parse.
type BoxOccupant struct {
	Species     int    `json:"species"`
	SpeciesName string `json:"species_name"`
	Nickname    string `json:"nickname"`
	Level       int    `json:"level"`
	Shiny       bool   `json:"shiny"`
}

// BoxLayout describes a save's PC: how many boxes, how many slots per box,
// and which "box:N:M" slots are occupied, enough to render a literal PC
// grid and compute the empty slots as everything this doesn't mention,
// mirroring how /parse itself only ever reports occupied slots.
type BoxLayout struct {
	BoxCount     int
	BoxSlotCount int
	Occupied     map[string]BoxOccupant
}

// BoxLayout asks the sidecar to parse a save and returns just its box
// occupancy, the same /parse call Ingest makes, reused rather than adding a
// second sidecar endpoint that would have to stay in sync with box-walking
// logic /parse already owns.
func (c *Client) BoxLayout(ctx context.Context, save []byte) (BoxLayout, error) {
	if !c.Available() {
		return BoxLayout{}, fmt.Errorf("pokemon sidecar is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/parse", bytes.NewReader(save))
	if err != nil {
		return BoxLayout{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return BoxLayout{}, fmt.Errorf("contact pokemon sidecar: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return BoxLayout{}, sidecarError(resp.StatusCode, body)
	}
	var result parseResult
	if err := json.Unmarshal(body, &result); err != nil {
		return BoxLayout{}, fmt.Errorf("decode sidecar response: %w", err)
	}
	occupied := make(map[string]BoxOccupant, len(result.Pokemon))
	for _, m := range result.Pokemon {
		if !strings.HasPrefix(m.Slot, "box:") {
			continue // party slots don't compete for PokeHome's box grid
		}
		occupied[m.Slot] = BoxOccupant{Species: m.SpeciesID, SpeciesName: m.Species, Nickname: m.Nickname, Level: m.Level, Shiny: m.Shiny}
	}
	return BoxLayout{BoxCount: result.BoxCount, BoxSlotCount: result.BoxSlotCount, Occupied: occupied}, nil
}

// OccupantAt reads one specific "box:N:M" slot's full Pokémon out of a save,
// not just the summary BoxLayout reports, the same raw entity bytes Ingest
// would have stored had this slot been read through the normal upload path.
// Used by the withdraw picker's evict-to-PokeHome option: a slot that already
// holds something needs its occupant turned into a real owned instance before
// this Pokémon can be written over it. Returns found=false for an empty slot
// rather than an error, a caller asking about a slot BoxLayout never listed
// as occupied is just checking, not making a mistake. The save's own
// generation (from this same /parse response, like Ingest uses) decodes the
// occupant, not the generation of whatever is about to replace it.
func (c *Client) OccupantAt(ctx context.Context, save []byte, slotKey string) (catalog.PokemonInstance, bool, error) {
	if !c.Available() {
		return catalog.PokemonInstance{}, false, fmt.Errorf("pokemon sidecar is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/parse", bytes.NewReader(save))
	if err != nil {
		return catalog.PokemonInstance{}, false, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return catalog.PokemonInstance{}, false, fmt.Errorf("contact pokemon sidecar: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return catalog.PokemonInstance{}, false, sidecarError(resp.StatusCode, body)
	}
	var result parseResult
	if err := json.Unmarshal(body, &result); err != nil {
		return catalog.PokemonInstance{}, false, fmt.Errorf("decode sidecar response: %w", err)
	}
	for _, m := range result.Pokemon {
		if m.Slot != slotKey {
			continue
		}
		inst, err := pokemonFromParsed(m, result.Generation)
		if err != nil {
			return catalog.PokemonInstance{}, false, err
		}
		return inst, true, nil
	}
	return catalog.PokemonInstance{}, false, nil
}

// BagItem is one occupied item slot in a bag pouch: an item id, its display
// name (resolved by the sidecar so a caller never needs a second lookup),
// and how many are held.
type BagItem struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// BagPouch is one pouch of a save's item bag, Items, KeyItems, Balls,
// TMHMs, Medicine, and so on (PKHeX's own pouch type names, used verbatim
// as the wire identifier a write request names back). MaxCount is the
// largest quantity a single item stack may hold in this pouch; Capacity is
// how many item slots the pouch has room for. Items omits empty slots,
// see the sidecar's Bag.Read.
type BagPouch struct {
	Type     string    `json:"type"`
	MaxCount int       `json:"max_count"`
	Capacity int       `json:"capacity"`
	Items    []BagItem `json:"items"`
}

type bagReadRequest struct {
	Save string `json:"save"`
}

type bagReadResult struct {
	Pouches []BagPouch `json:"pouches"`
}

// ReadBag asks the sidecar for a save's item bag, pouch by pouch.
func (c *Client) ReadBag(ctx context.Context, save []byte) ([]BagPouch, error) {
	if !c.Available() {
		return nil, fmt.Errorf("pokemon sidecar is not configured")
	}
	body, err := c.postJSON(ctx, "/bag/read", bagReadRequest{Save: base64.StdEncoding.EncodeToString(save)})
	if err != nil {
		return nil, err
	}
	var result bagReadResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode sidecar response: %w", err)
	}
	return result.Pouches, nil
}

type bagWriteRequest struct {
	Save    string     `json:"save"`
	Pouches []BagPouch `json:"pouches"`
}

type bagWriteResult struct {
	Save string `json:"save"`
}

// WriteBag applies pouch contents onto a save and returns the save's new
// bytes. A pouch absent from pouches is left untouched; a pouch present is
// replaced wholesale, see the sidecar's Bag.Write for the empty-slot
// padding rule.
func (c *Client) WriteBag(ctx context.Context, save []byte, pouches []BagPouch) ([]byte, error) {
	if !c.Available() {
		return nil, fmt.Errorf("pokemon sidecar is not configured")
	}
	body, err := c.postJSON(ctx, "/bag/write", bagWriteRequest{
		Save: base64.StdEncoding.EncodeToString(save), Pouches: pouches,
	})
	if err != nil {
		return nil, err
	}
	var result bagWriteResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode sidecar response: %w", err)
	}
	newSave, err := base64.StdEncoding.DecodeString(result.Save)
	if err != nil {
		return nil, fmt.Errorf("decode written save: %w", err)
	}
	return newSave, nil
}

// refList fetches and caches one of the sidecar's name tables (/abilities,
// /items, /moves), same cache-once shape as SpeciesList, generalized
// because all three are the same {id, name} list under a different wire key
// and endpoint path.
func (c *Client) refList(ctx context.Context, path, wireKey string) ([]Ref, error) {
	if !c.Available() {
		return nil, fmt.Errorf("pokemon sidecar is not configured")
	}
	c.refMu.Lock()
	defer c.refMu.Unlock()
	if cached, ok := c.refs[wireKey]; ok {
		return cached, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact pokemon sidecar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s list unavailable: %s", wireKey, resp.Status)
	}
	// The sidecar's response also carries a "count" field alongside the named
	// list (see /abilities, /items, /moves), decoding straight into
	// map[string][]Ref would try to unmarshal that number as a []Ref too, so
	// the list itself is pulled out by key first.
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s list: %w", wireKey, err)
	}
	var list []Ref
	if raw, ok := payload[wireKey]; ok {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("decode %s list: %w", wireKey, err)
		}
	}
	if c.refs == nil {
		c.refs = map[string][]Ref{}
	}
	c.refs[wireKey] = list
	return list, nil
}

// AbilityList/ItemList/MoveList back the editor's search dropdowns, the
// exact display-name strings the sidecar's ApplyEdits will accept back, so a
// selection made from one of these lists can never fail as an "unknown
// ability"/"unknown held item" edit.
func (c *Client) AbilityList(ctx context.Context) ([]Ref, error) {
	return c.refList(ctx, "/abilities", "abilities")
}
func (c *Client) ItemList(ctx context.Context) ([]Ref, error) {
	return c.refList(ctx, "/items", "items")
}
func (c *Client) MoveList(ctx context.Context) ([]Ref, error) {
	return c.refList(ctx, "/moves", "moves")
}

// CreateFields is what a caller supplies to build a Pokémon that never came
// from a save. Generation and Species are required; Level defaults to 5 when
// zero. Every other field is optional and applied the same way Edit applies
// it, see EditFields.
type CreateFields struct {
	Generation          int
	Species             int
	Level               int
	Nickname            *string
	Nature              *string
	Ability             *string
	Gender              *string
	Shiny               *bool
	Form                *int
	HeldItem            *string
	Ball                *int
	OriginalTrainerName *string
	IVs                 *StatEdit
	EVs                 *StatEdit
	Moves               []int
}

type createRequest struct {
	Generation          int           `json:"generation"`
	Species             int           `json:"species"`
	Level               int           `json:"level"`
	Nickname            *string       `json:"nickname,omitempty"`
	Nature              *string       `json:"nature,omitempty"`
	Ability             *string       `json:"ability,omitempty"`
	Gender              *string       `json:"gender,omitempty"`
	Shiny               *bool         `json:"shiny,omitempty"`
	Form                *int          `json:"form,omitempty"`
	HeldItem            *string       `json:"held_item,omitempty"`
	Ball                *int          `json:"ball,omitempty"`
	OriginalTrainerName *string       `json:"original_trainer_name,omitempty"`
	IVs                 *statEditWire `json:"ivs,omitempty"`
	EVs                 *statEditWire `json:"evs,omitempty"`
	Moves               []int         `json:"moves,omitempty"`
}

// Create asks the sidecar for a blank Pokémon of the requested
// generation/species/level with every other field applied through the same
// ApplyEdits code /edit uses, and maps the result into the same catalog
// record shape Ingest and Edit produce, a created Pokémon is stored no
// differently than one that arrived any other way.
func (c *Client) Create(ctx context.Context, fields CreateFields) (catalog.PokemonInstance, error) {
	if !c.Available() {
		return catalog.PokemonInstance{}, fmt.Errorf("pokemon sidecar is not configured")
	}
	body, err := c.postJSON(ctx, "/create", createRequest{
		Generation: fields.Generation, Species: fields.Species, Level: fields.Level,
		Nickname: fields.Nickname, Nature: fields.Nature, Ability: fields.Ability,
		Gender: fields.Gender, Shiny: fields.Shiny, Form: fields.Form, HeldItem: fields.HeldItem,
		Ball: fields.Ball, OriginalTrainerName: fields.OriginalTrainerName,
		IVs: fields.IVs.wire(), EVs: fields.EVs.wire(), Moves: fields.Moves,
	})
	if err != nil {
		return catalog.PokemonInstance{}, err
	}
	var m parsedMon
	if err := json.Unmarshal(body, &m); err != nil {
		return catalog.PokemonInstance{}, fmt.Errorf("decode sidecar response: %w", err)
	}
	return pokemonFromParsed(m, fields.Generation)
}
