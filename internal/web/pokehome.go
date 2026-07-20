package web

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"romrelay/internal/catalog"
	"romrelay/internal/pokemon"
)

// deletePokemonFiles best-effort removes a batch of .pkm files. Called after
// their database rows are already gone, so an individual file error (already
// missing, permissions) is logged-and-skipped rather than failing a request
// whose real work, the database delete, already succeeded.
func (s *CatalogServer) deletePokemonFiles(ownerID string, ids []string) {
	if s.pokemonFiles == nil {
		return
	}
	for _, id := range ids {
		_ = s.pokemonFiles.Delete(ownerID, id)
	}
}

// ownedPokemonInstance resolves the Pokémon named in the URL, scoped to its
// owner. One belonging to someone else is reported as missing, the same
// convention ownedSave uses for saves.
func (s *CatalogServer) ownedPokemonInstance(w http.ResponseWriter, r *http.Request, ownerID string) (catalog.PokemonInstance, bool) {
	inst, err := s.store.PokemonInstanceByID(r.Context(), ownerID, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such Pokémon", http.StatusNotFound)
		return catalog.PokemonInstance{}, false
	}
	if err != nil {
		http.Error(w, "could not read the Pokémon: "+err.Error(), http.StatusInternalServerError)
		return catalog.PokemonInstance{}, false
	}
	return inst, true
}

// writeSidecarError forwards a sidecar's own verdict (a 409 conversion
// failure, a 400 unreadable save) when there is one, and falls back to 502
// for an unreachable sidecar, the same distinction parseSave draws for reads,
// extended to a write path where there is no "safe partial" outcome: a
// failure here must be loud, not a silently half-done vault write.
func writeSidecarError(w http.ResponseWriter, err error) {
	var se *pokemon.SidecarStatusError
	if errors.As(err, &se) {
		// A legality rejection carries a per-line report the Creator/Editor UI
		// shows as a list, not just one string, the caller needs it as JSON,
		// not http.Error's plain text body.
		if len(se.Report) > 0 {
			writeJSON(w, se.StatusCode, map[string]any{"error": se.Message, "report": se.Report})
			return
		}
		http.Error(w, se.Message, se.StatusCode)
		return
	}
	http.Error(w, "could not reach the pokemon sidecar: "+err.Error(), http.StatusBadGateway)
}

// listPokehome returns the signed-in user's currently deposited Pokémon,
// newest deposit first.
func (s *CatalogServer) listPokehome(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to see your PokeHome", http.StatusUnauthorized)
		return
	}
	list, err := s.store.DepositedPokemonInstances(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "could not list PokeHome: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "pokemon": list})
}

// depositPokehome marks one of the caller's Pokémon as deposited, exempt
// from PruneDexOrphans regardless of what happens to its source save. Idempotent:
// depositing an already-deposited instance is a no-op success.
func (s *CatalogServer) depositPokehome(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to use PokeHome", http.StatusUnauthorized)
		return
	}
	if _, ok := s.ownedPokemonInstance(w, r, user.ID); !ok {
		return
	}
	inst, err := s.store.DepositPokemonInstance(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, "could not deposit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

// deletePokemon removes one owned Pokémon outright, a PokeHome deposit
// nobody wants kept, or any other stored instance. There is no undo: the row
// and its .pkm file are both gone once this returns.
func (s *CatalogServer) deletePokemon(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to manage your Pokémon", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	if err := s.store.DeletePokemonInstance(r.Context(), user.ID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such Pokémon", http.StatusNotFound)
			return
		}
		http.Error(w, "could not delete the Pokémon: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.deletePokemonFiles(user.ID, []string{id})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// downloadPokemon streams one owned Pokémon's raw entity bytes as a .pkm-style
// file, the "Download the .pkm file" option on the post-create/post-edit
// choice panel, and usable for any other owned instance the same way.
// ponytail: filenames use "pk{generation}" for every generation; PKHeX itself
// special-cases a few real extensions (.pb7, .pb8, .pa8) that don't follow
// that pattern. Cosmetic only, the bytes are unaffected, so upgrade this
// mapping only if a real user complains about the file extension.
func (s *CatalogServer) downloadPokemon(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to download your Pokémon", http.StatusUnauthorized)
		return
	}
	inst, ok := s.ownedPokemonInstance(w, r, user.ID)
	if !ok {
		return
	}
	name := inst.Nickname
	if name == "" {
		name = inst.SpeciesName
	}
	ext := fmt.Sprintf("pk%d", inst.Generation)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(name+"."+ext))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name+"."+ext, inst.CreatedAt, bytes.NewReader(inst.Binary))
}

// pokehomeBoxes reports a save's PC box layout, how many boxes, how many
// slots per box, and which slots are occupied, so the withdrawal picker can
// render a literal PC grid and only ever offer an empty slot.
func (s *CatalogServer) pokehomeBoxes(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to use PokeHome", http.StatusUnauthorized)
		return
	}
	if s.pokemon == nil || !s.pokemon.Available() {
		http.Error(w, "Pokémon features are disabled", http.StatusServiceUnavailable)
		return
	}
	if s.saves == nil {
		http.Error(w, "save storage is not configured", http.StatusServiceUnavailable)
		return
	}
	saveID := r.URL.Query().Get("save_id")
	if saveID == "" {
		http.Error(w, "save_id is required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	save, err := s.store.SaveByID(ctx, user.ID, saveID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such save", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "could not read the save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reader, err := s.saves.Open(save.OwnerID, save.ID)
	if err != nil {
		http.Error(w, "the stored save file is missing", http.StatusNotFound)
		return
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		http.Error(w, "could not read the save file", http.StatusInternalServerError)
		return
	}
	layout, err := s.pokemon.BoxLayout(ctx, data)
	if err != nil {
		writeSidecarError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"save_id": save.ID, "box_count": layout.BoxCount, "box_slot_count": layout.BoxSlotCount,
		"occupied": layout.Occupied,
	})
}

type withdrawRequest struct {
	SaveID string `json:"save_id"`
	Box    int    `json:"box"`
	Slot   int    `json:"slot"`
	// EvictToHome lets the picker target an occupied slot: its current
	// occupant is read out and deposited into the caller's own PokeHome
	// before being overwritten, rather than refusing with "slot occupied."
	EvictToHome bool `json:"evict_to_home"`
}

// withdrawPokehome copies a deposited Pokémon's raw bytes into a destination
// save's chosen box slot, producing a new save version. This is a copy, not a
// move: the deposit is untouched and can be withdrawn again (see the design
// doc's "duplication is allowed" decision), only home_last_withdrawn_at, a
// bookkeeping timestamp, changes.
func (s *CatalogServer) withdrawPokehome(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to use PokeHome", http.StatusUnauthorized)
		return
	}
	if s.pokemon == nil || !s.pokemon.Available() {
		http.Error(w, "Pokémon features are disabled", http.StatusServiceUnavailable)
		return
	}
	if s.saves == nil {
		http.Error(w, "save storage is not configured", http.StatusServiceUnavailable)
		return
	}
	// Despite the route living under /api/pokehome, this is not gated on
	// having been deposited: writing an owned Pokémon into a save is the same
	// sidecar Inject call regardless, and the standalone Editor page sends
	// straight from an upload without ever depositing, see the general-editor
	// comment on editPokemonRequest.
	inst, ok := s.ownedPokemonInstance(w, r, user.ID)
	if !ok {
		return
	}
	var body withdrawRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.SaveID == "" || body.Box < 1 || body.Slot < 1 {
		http.Error(w, "save_id, box, and slot are required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	destSave, err := s.store.SaveByID(ctx, user.ID, body.SaveID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such save", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "could not read the destination save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reader, err := s.saves.Open(destSave.OwnerID, destSave.ID)
	if err != nil {
		http.Error(w, "the stored save file is missing", http.StatusNotFound)
		return
	}
	destBytes, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		http.Error(w, "could not read the destination save file", http.StatusInternalServerError)
		return
	}

	if body.EvictToHome {
		slotKey := fmt.Sprintf("box:%d:%d", body.Box, body.Slot)
		occupant, found, err := s.pokemon.OccupantAt(ctx, destBytes, slotKey)
		if err != nil {
			writeSidecarError(w, err)
			return
		}
		if found {
			occupant.OriginGame, occupant.OriginSaveID = destSave.GameLabel, destSave.ID
			ids, _, _, err := s.store.SavePokemonInstances(ctx, user.ID, []catalog.PokemonInstance{occupant})
			if err != nil {
				http.Error(w, "could not preserve the occupied slot's Pokémon: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if len(ids) > 0 {
				if _, err := s.store.DepositPokemonInstance(ctx, user.ID, ids[0]); err != nil {
					http.Error(w, "could not deposit the evicted Pokémon: "+err.Error(), http.StatusInternalServerError)
					return
				}
				if s.pokemonFiles != nil {
					_ = s.pokemonFiles.Put(user.ID, ids[0], occupant.Binary)
				}
			}
		}
	}

	newSaveBytes, wroteSlot, err := s.pokemon.Inject(ctx, inst.Binary, inst.Generation, destBytes, body.Box, body.Slot, body.EvictToHome)
	if err != nil {
		writeSidecarError(w, err)
		return
	}

	saved, err := s.storeNewSaveVersion(ctx, destSave, newSaveBytes)
	if err != nil {
		http.Error(w, "could not record the new save version: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.MarkPokemonWithdrawn(ctx, user.ID, inst.ID); err != nil {
		http.Error(w, "withdrawal wrote the save but could not record it: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"save_id": saved.ID, "slot": wroteSlot, "instance_id": inst.ID,
	})
}

// pokehomePage serves the PokeHome view: every Pokémon currently deposited,
// rendered as a literal PC box grid. Filled by the client from /api/pokehome.
func (s *CatalogServer) pokehomePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "pokehome", "pokehome", s.newPageData(r))
}

// pokemonEditorPage serves the standalone Editor: create a Pokémon from
// scratch, or edit any Pokémon read out of a save and send the change back
// into it, without going through a PokeHome deposit first. Filled by the
// client from /api/saves and /api/pokemon, the same editor/withdraw modals
// PokeHome itself uses.
func (s *CatalogServer) pokemonEditorPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "editor", "editor", s.newPageData(r))
}
