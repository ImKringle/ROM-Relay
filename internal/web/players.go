package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

// adminSaves serves the Player Data screen: pick a user, see their saves and
// Pokédex/Trainer data. The roster comes from the existing /api/admin/users
// endpoint the Users page already uses.
func (s *CatalogServer) adminSaves(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	if s.adminTokenLanding(w, r, "/admin/saves") {
		return
	}
	s.render(w, "adminsaves", "adminsaves", s.newPageData(r))
}

// adminUserSaves lists one user's saves for the admin's Player Data view. It
// is the same data listSaves returns for a signed-in user's own saves, minus
// the box-art enrichment: this table is for administration, not browsing.
func (s *CatalogServer) adminUserSaves(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	list, err := s.store.SavesForOwner(r.Context(), target.ID)
	if err != nil {
		http.Error(w, "could not list saves: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "saves": list})
}

// adminDownloadUserSave streams one of a user's saves to the admin, the same
// way downloadSave streams a user's own save to themselves.
func (s *CatalogServer) adminDownloadUserSave(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	if s.saves == nil {
		http.Error(w, "save storage is not configured", http.StatusServiceUnavailable)
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	save, err := s.store.SaveByID(r.Context(), target.ID, r.PathValue("sid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	reader, err := s.saves.Open(save.OwnerID, save.ID)
	if err != nil {
		http.Error(w, "the stored save file is missing", http.StatusNotFound)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(save.FileName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, save.FileName, save.UploadedAt, reader)
}

// adminDeleteUserSave removes one of a user's saves and its bytes, the admin
// equivalent of deleteSave.
func (s *CatalogServer) adminDeleteUserSave(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	save, err := s.store.SaveByID(ctx, target.ID, r.PathValue("sid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pokemonIDs, err := s.store.DeleteSave(ctx, target.ID, save.ID)
	if err != nil {
		http.Error(w, "could not delete the save", http.StatusInternalServerError)
		return
	}
	if s.saves != nil {
		if err := s.saves.Delete(target.ID, save.ID); err != nil {
			http.Error(w, "could not delete the save file", http.StatusInternalServerError)
			return
		}
	}
	s.deletePokemonFiles(target.ID, pokemonIDs)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": save.ID})
}

// adminUserDex reports one user's Trainer profiles and how many Pokémon they
// have stored, for the admin's read-only Dex/Trainer overview. The one
// mutation this screen offers, clearing a user's Pokédex, already exists as
// POST /api/admin/users/{id}/clear-dex (users.go); this only adds the view.
func (s *CatalogServer) adminUserDex(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	profiles, err := s.store.TrainerProfiles(ctx, target.ID)
	if err != nil {
		http.Error(w, "could not read trainer profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pokemonCount := 0
	if mons, err := s.store.PokemonInstances(ctx, target.ID); err == nil {
		pokemonCount = len(mons)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"trainers": profiles,
		"pokemon":  pokemonCount,
	})
}

// adminUserPokehome lists one user's deposited PokeHome Pokémon for the admin
// Player Data page. These survive both clearUserTrainers (dex-scoped) and a
// save being deleted, a deposit is deliberately permanent (see the PokeHome
// design doc), so an admin auditing "why does this account still show N
// Pokémon stored" needs to see them directly, not just a count.
func (s *CatalogServer) adminUserPokehome(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	list, err := s.store.DepositedPokemonInstances(r.Context(), target.ID)
	if err != nil {
		http.Error(w, "could not list PokeHome: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "pokemon": list})
}

// adminDeletePokehomeMon removes one deposited Pokémon from a user's
// PokeHome, the only way to actually reduce the "stored" count a dex clear
// deliberately leaves untouched.
func (s *CatalogServer) adminDeletePokehomeMon(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("pid")
	if err := s.store.DeletePokemonInstance(r.Context(), target.ID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such Pokémon", http.StatusNotFound)
			return
		}
		http.Error(w, "could not delete the Pokémon: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.deletePokemonFiles(target.ID, []string{id})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// clearUserTrainers removes the Hall of Fame, dex flags, and profile row for
// one or more of a user's playthroughs, picked individually on the Player
// Data page ("Clear Associated Dex Data"), the scoped counterpart to
// clearUserDex's whole-account wipe. Saves and stored Pokémon are untouched.
func (s *CatalogServer) clearUserTrainers(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	var body struct {
		TrainerIDs []string `json:"trainer_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(body.TrainerIDs) == 0 {
		http.Error(w, "select at least one trainer", http.StatusBadRequest)
		return
	}
	if err := s.store.ClearTrainerDex(r.Context(), target.ID, body.TrainerIDs); err != nil {
		http.Error(w, "could not clear the selected trainers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": target.ID, "username": target.Username, "cleared": body.TrainerIDs})
}
