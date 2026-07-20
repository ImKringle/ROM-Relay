package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"romrelay/internal/catalog"
	"romrelay/internal/dex"
	"romrelay/internal/pokemon"
)

// listPokemon returns the signed-in user's stored Pokémon.
func (s *CatalogServer) listPokemon(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to see your Pokémon", http.StatusUnauthorized)
		return
	}
	list, err := s.store.PokemonInstances(ctx, user.ID)
	if err != nil {
		http.Error(w, "could not list Pokémon: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "pokemon": list})
}

// statEditJSON is the wire shape for a partial IV/EV change: any of the six
// stats may be omitted, meaning "leave unchanged."
type statEditJSON struct {
	HP  *int `json:"hp"`
	ATK *int `json:"atk"`
	DEF *int `json:"def"`
	SPA *int `json:"spa"`
	SPD *int `json:"spd"`
	SPE *int `json:"spe"`
}

func (s *statEditJSON) toFields() *pokemon.StatEdit {
	if s == nil {
		return nil
	}
	return &pokemon.StatEdit{HP: s.HP, ATK: s.ATK, DEF: s.DEF, SPA: s.SPA, SPD: s.SPD, SPE: s.SPE}
}

// editPokemonRequest is what a caller may change on a stored Pokémon in one
// call. Every field is optional; an absent one is left as it was. This is
// PKHeX used as a general editor, not gated to PokeHome deposits, since
// nothing about editing an already-owned Pokémon depends on whether it also
// happens to be deposited, and it edits the stored instance directly, not
// the save it was extracted from: to push a change back into a game save,
// withdraw the edited instance into that save like any other PokeHome
// transfer, the same way the origin save itself is a legal withdrawal target.
type editPokemonRequest struct {
	Nickname            *string       `json:"nickname"`
	Level               *int          `json:"level"`
	Nature              *string       `json:"nature"`
	Ability             *string       `json:"ability"`
	Gender              *string       `json:"gender"`
	Shiny               *bool         `json:"shiny"`
	Form                *int          `json:"form"`
	HeldItem            *string       `json:"held_item"`
	Ball                *int          `json:"ball"`
	OriginalTrainerName *string       `json:"original_trainer_name"`
	IVs                 *statEditJSON `json:"ivs"`
	EVs                 *statEditJSON `json:"evs"`
	Moves               []int         `json:"moves"`
}

// editPokemon rewrites a stored Pokémon's fields via the sidecar (the first
// write PKHeX gets used for direct editing, not just transfer) and persists
// the result: the database row's denormalized columns, its raw bytes, and
// its .pkm file all move together so none of the three can drift from what
// PKHeX actually wrote, a shiny toggle rewrites the PID, which a partial
// column patch would miss.
func (s *CatalogServer) editPokemon(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to edit your Pokémon", http.StatusUnauthorized)
		return
	}
	if s.pokemon == nil || !s.pokemon.Available() {
		http.Error(w, "Pokémon features are disabled", http.StatusServiceUnavailable)
		return
	}
	inst, ok := s.ownedPokemonInstance(w, r, user.ID)
	if !ok {
		return
	}
	var body editPokemonRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	edited, err := s.pokemon.Edit(ctx, inst.Binary, inst.Generation, pokemon.EditFields{
		Nickname: body.Nickname, Level: body.Level, Nature: body.Nature, Ability: body.Ability,
		Gender: body.Gender, Shiny: body.Shiny, Form: body.Form, HeldItem: body.HeldItem,
		Ball: body.Ball, OriginalTrainerName: body.OriginalTrainerName,
		IVs: body.IVs.toFields(), EVs: body.EVs.toFields(), Moves: body.Moves,
	})
	if err != nil {
		writeSidecarError(w, err)
		return
	}
	if err := s.store.UpdatePokemonInstanceEntity(ctx, user.ID, inst.ID, edited); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such Pokémon", http.StatusNotFound)
			return
		}
		http.Error(w, "could not save the edit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if s.pokemonFiles != nil {
		if err := s.pokemonFiles.Put(user.ID, inst.ID, edited.Binary); err != nil {
			http.Error(w, "edit was saved but the Pokémon file could not be updated: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	updated, err := s.store.PokemonInstanceByID(ctx, user.ID, inst.ID)
	if err != nil {
		http.Error(w, "edit was saved but could not be read back: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// createPokemonRequest is what a caller supplies to build a Pokémon that
// never came from a save. Generation and Species are required; every other
// field is the same optional shape editPokemonRequest already uses, since a
// created Pokémon's fields are set through the sidecar's shared ApplyEdits
// code (see pokemon.Client.Create).
type createPokemonRequest struct {
	Generation          int           `json:"generation"`
	Species             int           `json:"species"`
	Level               int           `json:"level"`
	Nickname            *string       `json:"nickname"`
	Nature              *string       `json:"nature"`
	Ability             *string       `json:"ability"`
	Gender              *string       `json:"gender"`
	Shiny               *bool         `json:"shiny"`
	Form                *int          `json:"form"`
	HeldItem            *string       `json:"held_item"`
	Ball                *int          `json:"ball"`
	OriginalTrainerName *string       `json:"original_trainer_name"`
	IVs                 *statEditJSON `json:"ivs"`
	EVs                 *statEditJSON `json:"evs"`
	Moves               []int         `json:"moves"`
}

// createPokemon builds a Pokémon from scratch via the sidecar and stores it
// exactly like one extracted from a save, same table, same dedupe-by-bytes
// rule (two identical from-scratch creates collide the same way re-uploading
// an identical save does), same .pkm file convention.
func (s *CatalogServer) createPokemon(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to create a Pokémon", http.StatusUnauthorized)
		return
	}
	if s.pokemon == nil || !s.pokemon.Available() {
		http.Error(w, "Pokémon features are disabled", http.StatusServiceUnavailable)
		return
	}
	var body createPokemonRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Generation < 1 || body.Species < 1 {
		http.Error(w, "generation and species are required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	inst, err := s.pokemon.Create(ctx, pokemon.CreateFields{
		Generation: body.Generation, Species: body.Species, Level: body.Level,
		Nickname: body.Nickname, Nature: body.Nature, Ability: body.Ability, Gender: body.Gender,
		Shiny: body.Shiny, Form: body.Form, HeldItem: body.HeldItem, Ball: body.Ball,
		OriginalTrainerName: body.OriginalTrainerName, IVs: body.IVs.toFields(), EVs: body.EVs.toFields(),
		Moves: body.Moves,
	})
	if err != nil {
		writeSidecarError(w, err)
		return
	}
	ids, inserted, _, err := s.store.SavePokemonInstances(ctx, user.ID, []catalog.PokemonInstance{inst})
	if err != nil {
		http.Error(w, "could not save the new Pokémon: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if inserted == 0 {
		http.Error(w, "an identical Pokémon already exists in your storage", http.StatusConflict)
		return
	}
	if s.pokemonFiles != nil {
		if err := s.pokemonFiles.Put(user.ID, ids[0], inst.Binary); err != nil {
			http.Error(w, "created but the Pokémon file could not be written: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	stored, err := s.store.PokemonInstanceByID(ctx, user.ID, ids[0])
	if err != nil {
		http.Error(w, "created but could not be read back: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

// apiReference serves the static-ish lookup tables the Pokémon editor and
// create-from-scratch flow need for their dropdowns: species, abilities,
// held items, and moves come from the sidecar's own name tables (so a
// selection can never fail as "unknown ability"/"unknown held item" on
// save), balls and nature stat effects are fixed game data.
func (s *CatalogServer) apiReference(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) == nil {
		http.Error(w, "sign in to see reference data", http.StatusUnauthorized)
		return
	}
	if s.pokemon == nil || !s.pokemon.Available() {
		http.Error(w, "Pokémon features are disabled", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	species, err := s.pokemon.SpeciesList(ctx)
	if err != nil {
		http.Error(w, "could not load species: "+err.Error(), http.StatusBadGateway)
		return
	}
	abilities, err := s.pokemon.AbilityList(ctx)
	if err != nil {
		http.Error(w, "could not load abilities: "+err.Error(), http.StatusBadGateway)
		return
	}
	items, err := s.pokemon.ItemList(ctx)
	if err != nil {
		http.Error(w, "could not load items: "+err.Error(), http.StatusBadGateway)
		return
	}
	moves, err := s.pokemon.MoveList(ctx)
	if err != nil {
		http.Error(w, "could not load moves: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"species": species, "abilities": abilities, "items": items, "moves": moves,
		"balls": pokemon.Balls(), "natures": pokemon.NatureEffects(),
	})
}

// apiDex returns the signed-in user's National Living Dex, or their per-game
// Pokédex when ?game= is set.
//
// The dex is one person's collection, so it is read for the account making the
// request. It used to resolve a single "default" account instead, which meant
// every signed-in user was shown the same dex, whoever's saves happened to have
// been ingested under that account.
func (s *CatalogServer) apiDex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.pokemon == nil || !s.pokemon.Available() {
		http.Error(w, "Pokémon features are disabled", http.StatusServiceUnavailable)
		return
	}
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to see your Dex", http.StatusUnauthorized)
		return
	}
	ownerID := user.ID
	species, err := s.pokemon.SpeciesList(ctx)
	if err != nil {
		http.Error(w, "could not load species list: "+err.Error(), http.StatusBadGateway)
		return
	}
	rows, err := s.store.PokemonDexRows(ctx, ownerID)
	if err != nil {
		http.Error(w, "could not read dex: "+err.Error(), http.StatusInternalServerError)
		return
	}
	refs := make([]dex.SpeciesRef, 0, len(species))
	for _, sp := range species {
		refs = append(refs, dex.SpeciesRef{Dex: sp.Dex, Name: sp.Name})
	}
	game := r.URL.Query().Get("game")
	scope := r.URL.Query().Get("scope") // "", "regional", or "national"

	// A region view aggregates the region's Pokédex across every game of that
	// region the user has saves for: "Johto" is one filter, and a game within
	// it is an optional narrowing, not the only entry point.
	if game == "" && r.URL.Query().Get("region") != "" {
		s.apiDexRegion(w, r, ownerID, refs, rows)
		return
	}
	info := pokemon.GameInfo(game)

	// A regional view exists only once the PokeAPI ingest has filled
	// pokedex_regional for this game's dex; without it the view falls back to
	// the generation's National dex rather than rendering an empty grid.
	var regional []catalog.RegionalNumber
	if game != "" && len(info.RegionalDexes) > 0 {
		if regional, err = s.store.RegionalDexEntries(ctx, info.RegionalDexes); err != nil {
			http.Error(w, "could not read the regional Pokédex: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if scope != "regional" && scope != "national" {
		scope = "national"
		if len(regional) > 0 {
			scope = "regional" // a game's own dex is what the game shows you
		}
	}
	if scope == "regional" && len(regional) == 0 {
		scope = "national"
	}

	// A National per-game view is scored against that generation's dex: the
	// save's own recorded range when it exists, the generation cap otherwise,
	// never the full modern list.
	maxSpecies := 0
	if game != "" {
		if maxSpecies, err = s.store.GameDexMax(ctx, ownerID, game); err != nil {
			http.Error(w, "could not read the game's Pokédex size: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if maxSpecies == 0 {
			maxSpecies = info.GenMax
		}
	}

	// This is a caught tracker: every count comes from the saves' own Pokédex
	// caught flags, never from the Pokémon you happen to hold. Stored Pokémon
	// only decorate a caught species with its shiny badge, "owned/in-PC" is a
	// separate future feature, not this view.
	var build []dex.OwnedRow
	if game == "" {
		caughtAll, err := s.store.GameDexCaughtAll(ctx, ownerID)
		if err != nil {
			http.Error(w, "could not read dex flags: "+err.Error(), http.StatusInternalServerError)
			return
		}
		shiny := map[int]bool{}
		for _, ro := range rows {
			if ro.Shiny {
				shiny[ro.Species] = true
			}
		}
		caughtSet := map[int]bool{}
		for _, c := range caughtAll {
			caughtSet[c.Species] = true
		}
		for sp := range caughtSet {
			build = append(build, dex.OwnedRow{Species: sp, Count: 1, Shiny: shiny[sp]})
		}
	} else {
		caught, err := s.store.GameDexCaught(ctx, ownerID, game)
		if err != nil {
			http.Error(w, "could not read regional dex: "+err.Error(), http.StatusInternalServerError)
			return
		}
		shiny := map[int]bool{}
		for _, ro := range rows {
			if ro.Game == game && ro.Shiny {
				shiny[ro.Species] = true
			}
		}
		for _, c := range caught {
			build = append(build, dex.OwnedRow{Species: c.Species, Game: game, Count: 1, Shiny: shiny[c.Species]})
		}
	}

	// The regional scope replaces the National ordering with the game's own:
	// only its species, in its numbering.
	if game != "" && scope == "regional" {
		names := map[int]string{}
		for _, ref := range refs {
			names[ref.Dex] = ref.Name
		}
		refs = refs[:0]
		for _, entry := range regional {
			name := names[entry.Dex]
			if name == "" {
				continue // a regional row for a species the sidecar list lacks
			}
			refs = append(refs, dex.SpeciesRef{Dex: entry.Dex, Name: name})
		}
		maxSpecies = 0 // the list itself is the bound
	}

	view := dex.Build(refs, build, game, maxSpecies)
	applyCaughtCounts(&view, rows, func(g string) bool { return game == "" || g == game })
	if game != "" && scope == "regional" {
		numbers := map[int]int{}
		for _, entry := range regional {
			numbers[entry.Dex] = entry.Entry
		}
		for i := range view.Entries {
			view.Entries[i].Regional = numbers[view.Entries[i].Dex]
		}
	}
	switch {
	case game == "":
		view.ScopeKind = "global"
	default:
		view.ScopeKind = scope
		view.Region = info.Region
		view.Scopes = []string{"national"}
		if len(regional) > 0 {
			view.Scopes = []string{"regional", "national"}
		}
	}
	// Offer a tab for every game with recorded Pokédex flags.
	codes := s.userGameCodes(ctx, ownerID)
	// PKHeX's code is what the API filters by; the name is what the tab reads.
	view.Games = view.Games[:0]
	for _, code := range codes {
		view.Games = append(view.Games, dex.GameRef{Code: code, Name: pokemon.GameName(code), Region: pokemon.GameInfo(code).Region})
	}
	view.ScopeName = "Global Pokédex"
	if game != "" {
		view.ScopeName = pokemon.GameName(game)
	}
	writeJSON(w, http.StatusOK, view)
}

// apiDexRegion renders the region-aggregate dex: the region's own Pokédex,
// with a species marked when any of the user's games set in that region has
// caught it.
func (s *CatalogServer) apiDexRegion(w http.ResponseWriter, r *http.Request, ownerID string, refs []dex.SpeciesRef, rows []catalog.DexRow) {
	ctx := r.Context()
	region := r.URL.Query().Get("region")

	// The user's games in this region, newest generation first, so the region's
	// numbering comes from the most recent dex when games disagree.
	codes := s.userGameCodes(ctx, ownerID)
	var regionGames []pokemon.Game
	for _, code := range codes {
		if info := pokemon.GameInfo(code); info.Region == region {
			regionGames = append(regionGames, info)
		}
	}
	sortGamesByGeneration(regionGames)
	inRegion := map[string]bool{}
	genMax := 0
	var dexNames []string
	seenDex := map[string]bool{}
	for _, info := range regionGames {
		inRegion[info.Code] = true
		if info.GenMax > genMax {
			genMax = info.GenMax
		}
		for _, name := range info.RegionalDexes {
			if !seenDex[name] {
				seenDex[name] = true
				dexNames = append(dexNames, name)
			}
		}
	}

	regional, err := s.store.RegionalDexEntries(ctx, dexNames)
	if err != nil {
		http.Error(w, "could not read the regional Pokédex: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// The region's count is the union of caught flags across its games, never
	// what you hold. Stored Pokémon only supply the shiny badge.
	shiny := map[int]bool{}
	for _, ro := range rows {
		if inRegion[ro.Game] && ro.Shiny {
			shiny[ro.Species] = true
		}
	}
	caughtSet := map[int]bool{}
	for _, info := range regionGames {
		caught, caughtErr := s.store.GameDexCaught(ctx, ownerID, info.Code)
		if caughtErr != nil {
			http.Error(w, "could not read regional dex: "+caughtErr.Error(), http.StatusInternalServerError)
			return
		}
		for _, c := range caught {
			caughtSet[c.Species] = true
		}
	}
	var build []dex.OwnedRow
	for species := range caughtSet {
		build = append(build, dex.OwnedRow{Species: species, Count: 1, Shiny: shiny[species]})
	}

	// The region's dex orders the grid when ingested; otherwise fall back to
	// the National list capped at the region's newest generation.
	maxSpecies := genMax
	if len(regional) > 0 {
		names := map[int]string{}
		for _, ref := range refs {
			names[ref.Dex] = ref.Name
		}
		refs = refs[:0]
		for _, entry := range regional {
			if name := names[entry.Dex]; name != "" {
				refs = append(refs, dex.SpeciesRef{Dex: entry.Dex, Name: name})
			}
		}
		maxSpecies = 0
	}

	view := dex.Build(refs, build, "", maxSpecies)
	applyCaughtCounts(&view, rows, func(g string) bool { return inRegion[g] })
	if len(regional) > 0 {
		numbers := map[int]int{}
		for _, entry := range regional {
			numbers[entry.Dex] = entry.Entry
		}
		for i := range view.Entries {
			view.Entries[i].Regional = numbers[view.Entries[i].Dex]
		}
	}
	view.Scope = "region"
	view.ScopeKind = "region"
	view.Region = region
	view.ScopeName = region + " Pokédex"
	view.Games = view.Games[:0]
	for _, code := range codes {
		view.Games = append(view.Games, dex.GameRef{Code: code, Name: pokemon.GameName(code), Region: pokemon.GameInfo(code).Region})
	}
	writeJSON(w, http.StatusOK, view)
}

// applyCaughtCounts fills each entry's CaughtCount from PC-held copies
// (pokemon_instances, via PokemonDexRows), independent of Count/Owned, which
// come from the save's own obtained flag (game_dex.caught). include narrows
// which games' rows count toward this view (a single game, or every game in
// a region); a nil-returning include would mean nothing counts, so callers
// always pass one.
func applyCaughtCounts(view *dex.View, rows []catalog.DexRow, include func(game string) bool) {
	counts := map[int]int{}
	for _, ro := range rows {
		if include(ro.Game) {
			counts[ro.Species] += ro.Count
		}
	}
	for i := range view.Entries {
		view.Entries[i].CaughtCount = counts[view.Entries[i].Dex]
	}
}

// userGameCodes lists every game with recorded Pokédex flags, sorted. Games a
// user only holds transferred Pokémon from are deliberately absent: the dex
// tracks caught flags from saves, and a game with no save has none.
func (s *CatalogServer) userGameCodes(ctx context.Context, ownerID string) []string {
	codes, err := s.store.GameDexGames(ctx, ownerID)
	if err != nil {
		return nil
	}
	return codes
}

func sortGamesByGeneration(games []pokemon.Game) {
	for i := 1; i < len(games); i++ {
		for j := i; j > 0 && games[j-1].Generation < games[j].Generation; j-- {
			games[j-1], games[j] = games[j], games[j-1]
		}
	}
}

// apiSpeciesDetail serves one species' reference data: what it is, what it
// evolves from and into, and its number in each game's own Pokédex.
//
// A species that was never ingested is a 404 rather than an empty object, so the
// page can say "enrichment is not configured" instead of rendering blanks.
func (s *CatalogServer) apiSpeciesDetail(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) == nil {
		http.Error(w, "sign in to see Pokédex details", http.StatusUnauthorized)
		return
	}
	dex, err := strconv.Atoi(r.PathValue("dex"))
	if err != nil || dex < 1 {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	species, found, err := s.store.SpeciesDetail(ctx, dex)
	if err != nil {
		http.Error(w, "could not read the Pokédex: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no Pokédex data for this species yet", http.StatusNotFound)
		return
	}
	evolutions, err := s.store.EvolutionsFor(ctx, dex)
	if err != nil {
		http.Error(w, "could not read evolutions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	regional, err := s.store.RegionalNumbersFor(ctx, dex)
	if err != nil {
		http.Error(w, "could not read regional numbers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// ?game= narrows encounters to that game's versions, that is also what
	// makes version exclusivity visible: an empty list under one version of a
	// pair says "not obtainable in the wild here".
	var versions []string
	if game := r.URL.Query().Get("game"); game != "" {
		versions = pokemon.GameInfo(game).Versions
	}
	encounters, err := s.store.EncountersFor(ctx, dex, versions)
	if err != nil {
		http.Error(w, "could not read encounters: "+err.Error(), http.StatusInternalServerError)
		return
	}
	learnset, err := s.store.LearnsetFor(ctx, dex)
	if err != nil {
		http.Error(w, "could not read moves: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Each encounter carries its region alongside the raw version name, so the
	// panel can offer a region filter without its own copy of the game table.
	// ?region= narrows to just that region, the same way ?game= narrows to one
	// game's versions, a trainer viewing a specific playthrough's dex sees
	// only where their own region obtains a species; omitting both params
	// (the global dex) leaves every region in.
	region := r.URL.Query().Get("region")
	encounterViews := make([]encounterView, 0, len(encounters))
	regionSet := map[string]bool{}
	for _, e := range encounters {
		er := pokemon.RegionForVersion(e.Version)
		if region != "" && er != region {
			continue
		}
		encounterViews = append(encounterViews, encounterView{Encounter: e, Region: er})
		if er != "" {
			regionSet[er] = true
		}
	}

	// The generation chip defaults to the newest one this species actually has
	// data for, and only that generation's rows are sent, a species with a
	// small kit should not have to filter down from ungenerationed noise, and
	// the panel needs the full available set to draw real chips, not dead ones.
	genSet := map[int]bool{}
	for _, e := range learnset {
		for _, part := range strings.Split(e.Generations, ",") {
			if n, err := strconv.Atoi(part); err == nil {
				genSet[n] = true
			}
		}
	}
	gens := make([]int, 0, len(genSet))
	for g := range genSet {
		gens = append(gens, g)
	}
	sort.Ints(gens)
	effectiveGen := 0
	if len(gens) > 0 {
		effectiveGen = gens[len(gens)-1]
	}
	if requested, err := strconv.Atoi(r.URL.Query().Get("gen")); err == nil && genSet[requested] {
		effectiveGen = requested
	}
	filteredLearnset := make([]catalog.LearnsetEntry, 0, len(learnset))
	for _, e := range learnset {
		for _, part := range strings.Split(e.Generations, ",") {
			if part == strconv.Itoa(effectiveGen) {
				filteredLearnset = append(filteredLearnset, e)
				break
			}
		}
	}

	regions := make([]string, 0, len(regionSet))
	for region := range regionSet {
		regions = append(regions, region)
	}
	sort.Strings(regions)

	// A nil slice marshals to JSON null, not []. Most species, every
	// single-stage one, have no evolutions, so the panel's JS would have to
	// nil-guard every array field rather than just .map() over them. Emitting
	// [] here keeps that null out of the wire format entirely.
	if evolutions == nil {
		evolutions = []catalog.Evolution{}
	}
	if regional == nil {
		regional = []catalog.RegionalNumber{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"species": species, "evolutions": evolutions, "regional": regional,
		"encounters": encounterViews, "encounter_regions": regions,
		"matchups": catalog.TypeMatchups(species.Types),
		"learnset": filteredLearnset, "learnset_generation": effectiveGen, "learnset_generations": gens,
	})
}

// apiPokedexLookupGames lists every game ROM Relay knows, for the Pokemon
// Lookup widget's "obtainable in / not in" version pickers. Alphabetical here
// rather than GameNamesByLength's longest-first order, which exists only for
// matching titles, not for a person scanning a dropdown.
func (s *CatalogServer) apiPokedexLookupGames(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) == nil {
		http.Error(w, "sign in to use Pokemon Lookup", http.StatusUnauthorized)
		return
	}
	names := pokemon.GameNamesByLength()
	sort.Slice(names, func(i, j int) bool { return names[i].Name < names[j].Name })
	out := make([]map[string]string, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]string{"code": n.Code, "name": n.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": out})
}

// apiPokedexLookup is the Pokemon Lookup widget's search: a pure reference-data
// query (type, move-by-method, version availability) independent of any
// trainer's caught/seen state, per the feature's "view only" scope.
func (s *CatalogServer) apiPokedexLookup(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) == nil {
		http.Error(w, "sign in to use Pokemon Lookup", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	f := catalog.LookupFilter{
		Type:       strings.ToLower(strings.TrimSpace(q.Get("type"))),
		MoveName:   strings.ToLower(strings.TrimSpace(q.Get("move"))),
		MoveMethod: q.Get("method"),
	}
	if game := q.Get("in_game"); game != "" {
		if versions := pokemon.GameInfo(game).Versions; len(versions) > 0 {
			f.InVersion = versions[0]
		}
	}
	if game := q.Get("not_in_game"); game != "" {
		if versions := pokemon.GameInfo(game).Versions; len(versions) > 0 {
			f.NotInVersion = versions[0]
		}
	}
	species, err := s.store.LookupSpecies(r.Context(), f)
	if err != nil {
		http.Error(w, "could not search the Pokedex: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if species == nil {
		species = []catalog.Species{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"species": species, "count": len(species)})
}

// encounterView adds the encounter's region to the stored fields, computed
// from the game table rather than persisted: the game table is the one place
// that association is allowed to change.
type encounterView struct {
	catalog.Encounter
	Region string `json:"region,omitempty"`
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// typeChartCell is one attacker/defender pairing's damage multiplier,
// pre-rendered into a CSS class and a short label, html/template has no
// float comparison, so the classification happens once here rather than in
// the template for every one of the 324 cells.
type typeChartCell struct {
	Class string
	Label string
}

type typeChartRow struct {
	Type  string
	Cells []typeChartCell
}

func classifyMultiplier(m float64) (class, label string) {
	switch m {
	case 0:
		return "mx0", "0×"
	case 0.25:
		return "mx25", "¼×"
	case 0.5:
		return "mx50", "½×"
	case 2:
		return "mx200", "2×"
	case 4:
		return "mx400", "4×"
	default:
		return "mx100", ""
	}
}

// typeChartPageData is the full 18×18 attacker/defender reference table:
// rows are defending types, columns (in the same AllTypes order) are
// attacking types, the standard type-chart layout, computed once per
// request from the same catalog.TypeMatchups a single species' matchup
// panel already uses, one defending type at a time.
type typeChartPageData struct {
	pageData
	AttackTypes []string
	Rows        []typeChartRow
}

// typeChartPage serves the standalone type-effectiveness reference: the same
// data the Pokédex species panel's "Type Matchups" chapter shows for one
// species, but every type against every other, with no species required.
func (s *CatalogServer) typeChartPage(w http.ResponseWriter, r *http.Request) {
	rows := make([]typeChartRow, len(catalog.AllTypes))
	for i, defend := range catalog.AllTypes {
		matchups := catalog.TypeMatchups(defend)
		cells := make([]typeChartCell, len(catalog.AllTypes))
		for j, attack := range catalog.AllTypes {
			class, label := classifyMultiplier(matchups[attack])
			cells[j] = typeChartCell{Class: class, Label: label}
		}
		rows[i] = typeChartRow{Type: defend, Cells: cells}
	}
	s.render(w, "typechart", "typechart", typeChartPageData{
		pageData: s.newPageData(r), AttackTypes: catalog.AllTypes, Rows: rows,
	})
}

// pokedexPage serves the Pokédex grid. It's filled by the client from
// /api/dex, 1,025 sprites is a lot of markup to send on every visit for a
// view that changes only when a save is uploaded.
func (s *CatalogServer) pokedexPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "pokedex", "pokedex", s.newPageData(r))
}

// trainersPage serves the trainer roster: one card per playthrough, built
// from the saves you upload. Split out of the Pokédex page so a game in
// progress is one page, not a tab. Filled by the client from /api/userdex.
func (s *CatalogServer) trainersPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "trainers", "trainers", s.newPageData(r))
}

// redirectTo permanently forwards a retired route, keeping any viewer token.
func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token := tokenQuery(r); token != "" {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + strings.TrimPrefix(token, "?")
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
