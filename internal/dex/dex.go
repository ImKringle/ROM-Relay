// Package dex builds Living Dex / per-game Pokédex views and serves the local
// Pokémon sprite set. It is a leaf package: callers pass in the species list and
// ownership rows so dex has no dependency on storage or the sidecar.
package dex

import (
	"embed"
	"io/fs"
	"net/http"
	"sort"
)

//go:embed assets/sprites
var spriteFS embed.FS

// SpriteHandler serves the vendored PokeAPI sprites under /sprites/, e.g.
// /sprites/pokemon/25.png and /sprites/pokemon/shiny/25.png.
func SpriteHandler() http.Handler {
	sub, err := fs.Sub(spriteFS, "assets/sprites")
	if err != nil {
		panic(err) // embedded path is fixed; a failure here is a build error
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix("/sprites/", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Sprites are immutable content; allow long caching.
			w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
			next.ServeHTTP(w, r)
		})
	}(fileServer))
}

// SpeciesRef is one National Pokédex entry (number and name).
type SpeciesRef struct {
	Dex  int
	Name string
}

// OwnedRow is an aggregate of a species the owner holds, optionally scoped to the
// game it originated in.
type OwnedRow struct {
	Species int
	Game    string
	Count   int
	Shiny   bool
}

// Entry is one Pokédex cell in a View. Regional is the species' number in the
// active regional Pokédex, present only on a regional-scope view.
//
// Owned/Count reflect the save's own Pokédex "obtained" flag (game_dex.caught),
// the tracker's primary, always-populated source. CaughtCount is a
// second, independent figure: how many PC copies of the species the account
// actually holds (pokemon_instances). The two can disagree in either
// direction, an obtained flag with no stored copy (released, traded away) or
// a stored copy whose obtained flag was never set (imported directly), so
// neither is derived from the other; the caller fills CaughtCount in
// separately from PokemonDexRows.
type Entry struct {
	Dex         int    `json:"dex"`
	Name        string `json:"name"`
	Owned       bool   `json:"owned"`
	Shiny       bool   `json:"shiny"`
	Count       int    `json:"count"`
	CaughtCount int    `json:"caught_count,omitempty"`
	Regional    int    `json:"regional,omitempty"`
}

// GameRef is one game the owner has a dex for: the code the API filters by, the
// name a person recognises, and the region the UI groups it under.
type GameRef struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Region string `json:"region,omitempty"`
}

// View is a rendered dex: the full ordered species list with ownership marked,
// plus completion totals and the set of games seen.
type View struct {
	Scope string `json:"scope"` // "national" or a game code
	// ScopeName is Scope as a person reads it: "HeartGold", not "HG".
	ScopeName string `json:"scope_name"`
	// ScopeKind says what the numbers mean: "global" (the Living Dex across all
	// games), "regional" (the game's own regional Pokédex), or "national" (the
	// game's generation's National dex). Filled by the caller.
	ScopeKind string `json:"scope_kind,omitempty"`
	// Scopes lists the kinds available for this view's game, so the UI offers
	// only toggles that exist ("regional" needs ingested PokeAPI data).
	Scopes []string `json:"scopes,omitempty"`
	// Region is the game's region ("Johto"), for the regional heading.
	Region  string    `json:"region,omitempty"`
	Total   int       `json:"total"`
	Owned   int       `json:"owned"`
	Shiny   int       `json:"shiny"`
	Percent int       `json:"percent"`
	Games   []GameRef `json:"games"`
	Entries []Entry   `json:"entries"`
}

// Build produces a dex view. When game is empty the view is the National Living
// Dex across every stored Pokémon; otherwise it is limited to Pokémon whose
// origin game matches. Games always lists every game present in rows so the UI
// can offer per-game views.
//
// maxSpecies bounds a per-game view to that game's own Pokédex: HeartGold ends at
// #493, so a Johto dex is scored out of 493 and never shows a species the game
// has no concept of. Zero means "no limit", which is what the National view uses
// and what a game whose range was never recorded falls back to.
func Build(species []SpeciesRef, rows []OwnedRow, game string, maxSpecies int) View {
	gameSet := map[string]struct{}{}
	owned := map[int]*Entry{}
	for _, r := range rows {
		if r.Game != "" {
			gameSet[r.Game] = struct{}{}
		}
		if game != "" && r.Game != game {
			continue
		}
		e := owned[r.Species]
		if e == nil {
			e = &Entry{Owned: true}
			owned[r.Species] = e
		}
		e.Count += r.Count
		if r.Shiny {
			e.Shiny = true
		}
	}

	view := View{Scope: "national"}
	if game != "" {
		view.Scope = game
	}
	view.Entries = make([]Entry, 0, len(species))
	for _, s := range species {
		// A game's dex stops where the game does. Scoring Johto out of 1,025 made
		// every regional view read as a fraction of the National dex instead of
		// progress in that game.
		if maxSpecies > 0 && s.Dex > maxSpecies {
			continue
		}
		entry := Entry{Dex: s.Dex, Name: s.Name}
		if o := owned[s.Dex]; o != nil {
			entry.Owned = true
			entry.Shiny = o.Shiny
			entry.Count = o.Count
			view.Owned++
			if o.Shiny {
				view.Shiny++
			}
		}
		view.Entries = append(view.Entries, entry)
	}
	view.Total = len(view.Entries)
	if view.Total > 0 {
		view.Percent = view.Owned * 100 / view.Total
	}
	// Names are filled in by the caller, which knows the code table; Build stays a
	// leaf that only orders what it was given.
	codes := make([]string, 0, len(gameSet))
	for g := range gameSet {
		codes = append(codes, g)
	}
	sort.Strings(codes)
	for _, code := range codes {
		view.Games = append(view.Games, GameRef{Code: code, Name: code})
	}
	return view
}
