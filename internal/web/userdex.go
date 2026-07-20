package web

import (
	"net/http"

	"romrelay/internal/catalog"
	"romrelay/internal/pokemon"
)

// apiUserdex serves the signed-in user's playthroughs and their aggregates.
//
// It reads the account making the request. The Dex used to resolve a single
// "default" account instead, which showed every signed-in user the same data.
func (s *CatalogServer) apiUserdex(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to see your playthroughs", http.StatusUnauthorized)
		return
	}
	profiles, err := s.store.TrainerProfiles(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "could not read your playthroughs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if profiles == nil {
		profiles = []catalog.TrainerProfile{}
	}
	// Aggregates are computed, not stored: they are a view of the profiles and
	// would only drift if kept as columns.
	var money int64
	var minutes, clears int
	type profileView struct {
		catalog.TrainerProfile
		ID       string             `json:"id"`
		GameName string             `json:"game_name"`
		Region   string             `json:"region,omitempty"`
		HOF      []catalog.HOFEntry `json:"hof"`
		// DexCaught is this run's caught count, for the card's dex line.
		DexCaught int `json:"dex_caught"`
	}
	views := make([]profileView, 0, len(profiles))
	for _, p := range profiles {
		money += p.Money
		minutes += p.PlayedMinutes
		if p.HOFClears != nil {
			clears += *p.HOFClears
		}
		// Region drives which badge set the card renders (Kanto's Boulder
		// Badge is not Johto's Zephyr Badge); the game code is already the
		// one source of truth for it.
		view := profileView{TrainerProfile: p, ID: p.ID(), GameName: pokemon.GameName(p.Game), Region: pokemon.GameInfo(p.Game).Region, HOF: []catalog.HOFEntry{}}
		if hof, err := s.store.TrainerHOF(r.Context(), user.ID, p.ID()); err == nil && hof != nil {
			view.HOF = hof
		}
		if rows, err := s.store.GameDexCaughtFor(r.Context(), user.ID, p.Game, p.TrainerKey()); err == nil {
			view.DexCaught = len(rows)
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles": views,
		"totals": map[string]any{
			"money": money, "played_minutes": minutes, "clears": clears,
		},
	})
}

// apiUserdexDex serves one playthrough's dex, what THIS run caught, as opposed
// to the merged per-game tab, which is an OR across every trainer.
func (s *CatalogServer) apiUserdexDex(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to see your Dex", http.StatusUnauthorized)
		return
	}
	profile, found, err := s.store.TrainerProfileByID(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, "could not read the playthrough: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Scoped to the caller: a profile another user owns is simply not found.
	if !found {
		http.Error(w, "no such playthrough", http.StatusNotFound)
		return
	}
	rows, err := s.store.GameDexCaughtFor(r.Context(), user.ID, profile.Game, profile.TrainerKey())
	if err != nil {
		http.Error(w, "could not read the dex: "+err.Error(), http.StatusInternalServerError)
		return
	}
	caught := make([]int, 0, len(rows))
	for _, ro := range rows {
		caught = append(caught, ro.Species)
	}
	writeJSON(w, http.StatusOK, map[string]any{"caught": caught})
}
