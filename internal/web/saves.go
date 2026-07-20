package web

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"romrelay/internal/catalog"
	"romrelay/internal/pokemon"
	"romrelay/internal/savevault"
)

// maxSaveUploadBytes bounds an uploaded save. The largest console saves are a few
// megabytes; this ceiling leaves room while refusing anything absurd.
const maxSaveUploadBytes = 16 << 20

// SetSaveVault wires the byte storage behind the save vault. Without one, the
// save routes report that the feature is unconfigured rather than failing oddly.
func (s *CatalogServer) SetSaveVault(v savevault.Vault) { s.saves = v }

// uploadSave stores a save for the signed-in user and, when the save is a
// Pokémon save the sidecar recognises, ingests its Pokémon into the user's Dex.
//
// The bytes are stored before parsing: a save that PKHeX cannot read is still a
// save worth keeping, so parsing is an enhancement rather than a precondition.
// The session cookie is SameSite=Lax, which is what guards this mutation.
func (s *CatalogServer) uploadSave(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to upload a save", http.StatusUnauthorized)
		return
	}
	if s.saves == nil {
		http.Error(w, "save storage is not configured", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSaveUploadBytes)
	if err := r.ParseMultipartForm(maxSaveUploadBytes); err != nil {
		http.Error(w, "could not read upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("save_file")
	if err != nil {
		http.Error(w, "attach a save file in the save_file field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSaveUploadBytes))
	if err != nil {
		http.Error(w, "could not read save file", http.StatusBadRequest)
		return
	}
	if len(data) == 0 {
		http.Error(w, "the save file is empty", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	// The save says which game it is, so nobody has to tell us. Parsing happens
	// before the record is built precisely so it can name the game: asking the
	// uploader to pick a ROM from a list was asking them to repeat what is written
	// in the file they just chose, and to be wrong about it.
	outcome, summary, instances := s.parseSave(ctx, data, header.Filename)

	sum := sha256.Sum256(data)
	record := catalog.Save{
		OwnerID:   user.ID,
		FileName:  filepath.Base(header.Filename),
		SizeBytes: int64(len(data)),
		SHA256:    hex.EncodeToString(sum[:]),
		Source:    catalog.SaveSourceUpload,
	}
	if outcome == parseRead {
		record.Parsed = true
		record.ParsedGame = summary.Game
		record.ParsedTrainer = summary.Trainer.OT
		record.PokemonCount = summary.Found
	}
	record.GameID, record.GameLabel = s.saveGame(ctx, outcome, summary, r.FormValue("game_id"))
	if record.GameID == "" && record.GameLabel == "" {
		http.Error(w, parseDetail(outcome)+" ROM Relay could not tell which game it is from, so there is nothing to file it under.", http.StatusBadRequest)
		return
	}

	// A save the parser just named may already be stored under whatever the
	// uploader was looking at the first time, when nothing could read it. Those are
	// the same save, so it is found by its bytes rather than by what it was filed
	// under, and re-filed below instead of stored twice.
	saved, created, err := catalog.Save{}, false, error(nil)
	if outcome == parseRead {
		if existing, found, findErr := s.store.SaveByHash(ctx, user.ID, record.SHA256); findErr != nil {
			http.Error(w, "could not check for an existing save: "+findErr.Error(), http.StatusInternalServerError)
			return
		} else if found {
			saved = existing
		}
	}
	if saved.ID == "" {
		if saved, created, err = s.store.CreateSave(ctx, record); err != nil {
			http.Error(w, "could not record the save: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if created {
		if err := s.saves.Put(saved.OwnerID, saved.ID, data); err != nil {
			// Leave no row pointing at bytes that were never written.
			_, _ = s.store.DeleteSave(ctx, saved.OwnerID, saved.ID)
			http.Error(w, "could not store the save file: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else if outcome == parseRead && (!saved.Parsed || saved.GameID != record.GameID) {
		// Stored before a parser could read it: record what it found and file it
		// under the game the parser named, rather than leaving it labelled
		// unreadable under a game somebody guessed.
		record.ID, record.OwnerID = saved.ID, saved.OwnerID
		if err := s.store.RecordSaveParse(ctx, record); err != nil {
			http.Error(w, "could not record the save's Pokémon details: "+err.Error(), http.StatusInternalServerError)
			return
		}
		saved.Parsed = true
		saved.PokemonCount = summary.Found
		saved.GameID, saved.GameLabel = record.GameID, record.GameLabel
	}

	response := map[string]any{
		"save_id":   saved.ID,
		"game":      saved.GameLabel,
		"file_name": saved.FileName,
		"stored":    created,
		"parsed":    saved.Parsed,
	}
	if outcome != parseRead {
		response["detail"] = parseDetail(outcome)
		writeJSON(w, http.StatusOK, response)
		return
	}

	for i := range instances {
		instances[i].OriginSaveID = saved.ID
	}
	ids, inserted, skipped, err := s.store.SavePokemonInstances(ctx, user.ID, instances)
	if err != nil {
		http.Error(w, "could not store Pokémon: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Every ingested Pokémon also gets a standalone .pkm file, materialized
	// alongside the database row (see the PokeHome design doc), whether it
	// was newly inserted or a re-upload's already-known duplicate, so a
	// database created before this feature shipped self-heals on next upload
	// rather than staying without files for Pokémon it already has.
	if s.pokemonFiles != nil {
		for i, inst := range instances {
			if err := s.pokemonFiles.Put(user.ID, ids[i], inst.Binary); err != nil {
				http.Error(w, "could not store a Pokémon file: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
	// A playthrough is keyed by the trainer inside the save, so a restarted game
	// becomes a second profile rather than overwriting the first. An untrusted
	// save (the GameCube gate) falls through with trainerKey == "": its dex rows
	// still merge into the per-game tab under the unknown trainer, and no
	// profile is written, identity only.
	trainerKey := ""
	if summary.Trainer.Trusted && summary.Trainer.OT != "" {
		profile := catalog.TrainerProfile{
			OwnerID: user.ID, Game: summary.Game,
			TID: summary.Trainer.TID, SID: summary.Trainer.SID,
			OT: summary.Trainer.OT, Gender: summary.Trainer.Gender,
			Language: summary.Trainer.Language, PlayedMinutes: summary.Trainer.PlayedMinutes,
			Money: summary.Trainer.Money, MoneyMax: summary.Trainer.MoneyMax,
			Notes: summary.Trainer.Notes, Trusted: true, SourceSaveID: saved.ID,
		}
		trainerKey = profile.TrainerKey()
		if p := summary.Trainer.Progress; p != nil {
			profile.ProgressKind, profile.ProgressCount, profile.ProgressMax = p.Kind, p.Count, p.Max
		}
		if h := summary.Trainer.HallOfFame; h != nil {
			profile.HOFClears = h.Clears
		}
		if err := s.store.UpsertTrainerProfile(ctx, profile); err != nil {
			http.Error(w, "could not record the playthrough: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if h := summary.Trainer.HallOfFame; h != nil {
			entries := make([]catalog.HOFEntry, 0, len(h.Teams))
			for i, team := range h.Teams {
				for slot, m := range team.Members {
					entries = append(entries, catalog.HOFEntry{
						EntryIndex: i, Slot: slot, Species: m.Species,
						Level: m.Level, Nickname: m.Nickname,
					})
				}
			}
			if err := s.store.ReplaceTrainerHOF(ctx, profile, entries); err != nil {
				http.Error(w, "could not record the Hall of Fame: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
	// Record the save's own Pokédex (seen/caught) for the regional dex, whether or
	// not new Pokémon were stored.
	if err := s.store.UpsertGameDex(ctx, user.ID, summary.Game, trainerKey, summary.Seen, summary.Caught); err != nil {
		http.Error(w, "could not record Pokédex: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// How many species this game's dex covers, so its completion is measured
	// against its own Pokédex rather than the modern National one.
	if err := s.store.SetGameDexMax(ctx, user.ID, summary.Game, summary.MaxSpecies); err != nil {
		http.Error(w, "could not record the game's Pokédex size: "+err.Error(), http.StatusInternalServerError)
		return
	}
	response["parsed_game"] = summary.Game
	response["pokemon_found"] = summary.Found
	response["inserted"] = inserted
	response["skipped"] = skipped
	response["dex_updated"] = inserted > 0
	writeJSON(w, http.StatusOK, response)
}

// saveGame decides what a save is filed under: the id that versions it and the
// label a person reads.
//
// A save PKHeX recognises is filed under its own game, keyed by PKHeX's code
// rather than a catalog ROM. The key has to be stable per game (it is what makes
// a re-upload a new version of the same save instead of an unrelated one), and a
// parsed game is stable whether the upload came from the Dex, the Save Data page,
// or a game page, and whether or not the matching ROM is even in the library.
//
// Matching the parsed game back to a catalog ROM by title is deliberately not
// attempted: "Black" is a prefix of "Black Version 2", and filing someone's Black
// 2 save under Black would be worse than not linking it at all.
//
// Anything PKHeX cannot read falls back to the game the upload came from, which
// is how a non-Pokémon save (the vault takes any save) still gets filed.
func (s *CatalogServer) saveGame(ctx context.Context, outcome parseOutcome, summary pokemon.Summary, gameIDHint string) (id, label string) {
	if outcome == parseRead && summary.Game != "" {
		return "pkhex:" + summary.Game, pokemon.GameName(summary.Game)
	}
	if gameIDHint != "" {
		if game, err := s.store.Game(ctx, gameIDHint); err == nil {
			return game.ID, game.Title
		}
	}
	return "", ""
}

// parseOutcome distinguishes why a save has no Pokémon details. "Nothing could
// read it" and "the reader was not there" look identical to the code but mean
// very different things to the person who just uploaded a Pokémon save.
type parseOutcome int

const (
	parseRead        parseOutcome = iota // the sidecar read the save
	parseUnsupported                     // the sidecar ran and could not read it
	parseNoSidecar                       // Pokémon support is not configured
	parseUnavailable                     // the sidecar is configured but did not answer
)

// parseDetail explains a non-parsed upload in the terms the user needs: whether
// to shrug, or to fix the sidecar and upload again.
func parseDetail(outcome parseOutcome) string {
	switch outcome {
	case parseNoSidecar:
		return "Stored. Pokémon features are switched off, so your Dex is unchanged."
	case parseUnavailable:
		return "Stored, but the Pokémon parser could not be reached, so your Dex is unchanged. Upload this save again once it is back."
	default:
		return "Stored. This is not a Pokémon save ROM Relay can read, so your Dex is unchanged."
	}
}

// parseSave asks the PKHeX sidecar to read a save. Every failure still leaves the
// save stored, parsing is an enhancement, but the reason is reported so an
// unreachable sidecar is never mistaken for an unreadable save.
func (s *CatalogServer) parseSave(ctx context.Context, data []byte, fileName string) (parseOutcome, pokemon.Summary, []catalog.PokemonInstance) {
	if s.pokemon == nil || !s.pokemon.Available() {
		return parseNoSidecar, pokemon.Summary{}, nil
	}
	instances, summary, err := s.pokemon.Ingest(ctx, data, fileName)
	if err == nil {
		return parseRead, summary, instances
	}
	if errors.Is(err, pokemon.ErrUnreadableSave) {
		return parseUnsupported, pokemon.Summary{}, nil
	}
	return parseUnavailable, pokemon.Summary{}, nil
}

// saveGameInfo is what the Save Data page shows about the game a save belongs
// to: the version name, where it runs, where it is set, and box art when the
// library holds a matching ROM.
type saveGameInfo struct {
	Version    string       `json:"version,omitempty"`
	Platform   string       `json:"platform,omitempty"`
	Region     string       `json:"region,omitempty"`
	Generation int          `json:"generation,omitempty"`
	Artwork    string       `json:"artwork,omitempty"`
	Alternates []gameChoice `json:"alternates,omitempty"`
}

// gameChoice is one option in a save's "this is actually…" override control,
// see setSaveGameOverride.
type gameChoice struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// ambiguousGameClusters lists the only games PKHeX cannot pin to one
// cartridge from a save's bytes alone: Gen 3's FireRed/LeafGreen and
// Ruby/Sapphire share identical SRAM layouts with no stored version byte,
// unlike every later generation, which does encode a real, reliable version
// identifier. This is deliberately not "any two games sharing a generation
// and region", that would also, wrongly, offer HeartGold/SoulSilver or
// Sun/Moon as if a save upload could actually be confused between them.
var ambiguousGameClusters = [][]string{
	{"FR", "LG", "FRLG"},
	{"R", "S", "RS"},
}

// gameAlternates returns the other games in currentCode's ambiguity cluster,
// or nil if it isn't in one, the honest set of things a save this ambiguous
// could actually be, and the only override choices setSaveGameOverride will
// accept for it.
func gameAlternates(currentCode string) []gameChoice {
	for _, cluster := range ambiguousGameClusters {
		hit := false
		for _, code := range cluster {
			if code == currentCode {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		out := make([]gameChoice, 0, len(cluster)-1)
		for _, code := range cluster {
			if code != currentCode {
				out = append(out, gameChoice{Code: code, Name: pokemon.GameName(code)})
			}
		}
		return out
	}
	return nil
}

// listSaves returns the signed-in user's stored saves, newest first, each with
// what ROM Relay knows about its game.
func (s *CatalogServer) listSaves(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to see your saves", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	list, err := s.store.SavesForOwner(ctx, user.ID)
	if err != nil {
		http.Error(w, "could not list saves: "+err.Error(), http.StatusInternalServerError)
		return
	}
	art := s.artworkByGameCode(ctx)
	type saveWithGame struct {
		catalog.Save
		GameInfo *saveGameInfo `json:"game_info,omitempty"`
	}
	out := make([]saveWithGame, 0, len(list))
	for _, save := range list {
		entry := saveWithGame{Save: save}
		switch {
		case save.ParsedGame != "":
			info := pokemon.GameInfo(save.ParsedGame)
			entry.GameInfo = &saveGameInfo{
				Version: info.Name, Platform: info.Platform, Region: info.Region,
				Generation: info.Generation, Artwork: art[save.ParsedGame],
				Alternates: gameAlternates(save.ParsedGame),
			}
		case save.GameID != "" && !strings.HasPrefix(save.GameID, "pkhex:"):
			// A save filed under a library ROM (nothing could read it): the
			// catalog knows the platform and any art directly.
			if game, err := s.store.Game(ctx, save.GameID); err == nil {
				entry.GameInfo = &saveGameInfo{Platform: game.Platform, Region: game.Region, Artwork: mediaCoverURL(game)}
			}
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "saves": out})
}

// artworkByGameCode maps PKHeX game codes to library box art, by matching each
// catalog title against the known version names, longest name first, that rule
// is what stops a "Pokemon Black Version 2" ROM answering for "Black" ("Black"
// is a prefix of "Black Version 2", and FireRed contains Red). Titles without
// "pok" are never considered, so "Silver Surfer" cannot answer for Silver.
func (s *CatalogServer) artworkByGameCode(ctx context.Context) map[string]string {
	games, err := s.store.Games(ctx)
	if err != nil {
		return nil
	}
	names := pokemon.GameNamesByLength()
	art := map[string]string{}
	for _, game := range games {
		// The art must be the same-origin cover route the Library uses:
		// ArtworkURL itself is the provider's source URL, which img-src 'self'
		// refuses, the Save Vault showed no box art at all because of it.
		cover := mediaCoverURL(game)
		if cover == "" {
			continue
		}
		title := strings.ToLower(game.Title)
		if !strings.Contains(title, "pok") {
			continue
		}
		for _, n := range names {
			// "X" and "Y" are real version names but match almost any title as
			// substrings; a one-letter version needs " x" at the end instead.
			if len(n.Name) < 3 {
				if !strings.HasSuffix(title, " "+strings.ToLower(n.Name)) {
					continue
				}
			} else if !strings.Contains(title, strings.ToLower(n.Name)) {
				continue
			}
			if art[n.Code] == "" {
				art[n.Code] = cover
			}
			break
		}
	}
	return art
}

// mediaCoverURL is the same-origin cover route for a game, empty when the game
// has no artwork.
func mediaCoverURL(game catalog.Game) string {
	if game.ArtworkURL == "" {
		return ""
	}
	return "/media/" + game.ID + "/cover"
}

// downloadSave streams one of the signed-in user's saves back to them.
func (s *CatalogServer) downloadSave(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to download your saves", http.StatusUnauthorized)
		return
	}
	save, ok := s.ownedSave(w, r, user.ID)
	if !ok {
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

// deleteSave removes one of the signed-in user's saves and its bytes.
func (s *CatalogServer) deleteSave(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to delete your saves", http.StatusUnauthorized)
		return
	}
	save, ok := s.ownedSave(w, r, user.ID)
	if !ok {
		return
	}
	pokemonIDs, err := s.store.DeleteSave(r.Context(), save.OwnerID, save.ID)
	if err != nil {
		http.Error(w, "could not delete the save", http.StatusInternalServerError)
		return
	}
	// Bytes that are already gone are not an error: the row is what the user sees.
	if err := s.saves.Delete(save.OwnerID, save.ID); err != nil {
		http.Error(w, "could not delete the save file", http.StatusInternalServerError)
		return
	}
	s.deletePokemonFiles(save.OwnerID, pokemonIDs)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": save.ID})
}

// gameOverrideRequest names the pokemon.games code the caller believes this
// save actually is.
type gameOverrideRequest struct {
	Game string `json:"game"`
}

// setSaveGameOverride lets a person correct a save's detected game. It exists
// because PKHeX cannot always tell one game of a pair apart from the save
// bytes alone, a Gen 3 FireRed/LeafGreen (or Ruby/Sapphire) save carries no
// cartridge identity in its own SRAM, so PKHeX's own guess sometimes lands on
// the wrong half, and no amount of parsing logic on this side can fix a fact
// the save format itself doesn't record. Restricted to a code in the same
// generation and region as what was already detected, so this corrects an
// honest ambiguity rather than refiling a save as an unrelated game.
func (s *CatalogServer) setSaveGameOverride(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to edit your saves", http.StatusUnauthorized)
		return
	}
	save, ok := s.ownedSave(w, r, user.ID)
	if !ok {
		return
	}
	var body gameOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Game == "" {
		http.Error(w, "a game code is required", http.StatusBadRequest)
		return
	}
	if save.ParsedGame == "" {
		http.Error(w, "this save was never read as a Pokémon save, so there is nothing to correct", http.StatusBadRequest)
		return
	}
	valid := false
	for _, choice := range gameAlternates(save.ParsedGame) {
		if choice.Code == body.Game {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "that isn't one of the games this save could honestly be", http.StatusBadRequest)
		return
	}
	requested := pokemon.GameInfo(body.Game)
	if err := s.store.SetSaveGameOverride(r.Context(), user.ID, save.ID, "pkhex:"+body.Game, requested.Name, body.Game); err != nil {
		http.Error(w, "could not update the save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": save.ID, "game": requested.Name})
}

// storeNewSaveVersion records newBytes as a new version of an existing save,
// same game/file name, just newer content, and persists the bytes,
// rolling back the row if the byte write fails so nothing is left pointing
// at data that was never actually stored. Shared by withdrawPokehome (an
// injected Pokémon) and setSaveBag (an edited item bag): both replace a
// save's bytes in place and need the exact same "new version, same
// identity" bookkeeping.
func (s *CatalogServer) storeNewSaveVersion(ctx context.Context, base catalog.Save, newBytes []byte) (catalog.Save, error) {
	sum := sha256.Sum256(newBytes)
	record := catalog.Save{
		OwnerID: base.OwnerID, GameID: base.GameID, GameLabel: base.GameLabel,
		FileName: base.FileName, SizeBytes: int64(len(newBytes)),
		SHA256: hex.EncodeToString(sum[:]), Source: catalog.SaveSourceTransfer,
	}
	saved, created, err := s.store.CreateSave(ctx, record)
	if err != nil {
		return catalog.Save{}, err
	}
	if created {
		if err := s.saves.Put(saved.OwnerID, saved.ID, newBytes); err != nil {
			_, _ = s.store.DeleteSave(ctx, saved.OwnerID, saved.ID)
			return catalog.Save{}, err
		}
	}
	return saved, nil
}

// readSaveBytes loads one owned save's raw bytes, the common first step for
// any handler that needs to hand a save to the sidecar.
func (s *CatalogServer) readSaveBytes(save catalog.Save) ([]byte, error) {
	reader, err := s.saves.Open(save.OwnerID, save.ID)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

// getSaveBag reports a save's item bag, pouch by pouch, so the standalone
// Editor's Bag tab can list what's held without a person needing to guess
// at PKHeX's own pouch names.
func (s *CatalogServer) getSaveBag(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to see a save's bag", http.StatusUnauthorized)
		return
	}
	if s.pokemon == nil || !s.pokemon.Available() {
		http.Error(w, "Pokémon features are disabled", http.StatusServiceUnavailable)
		return
	}
	save, ok := s.ownedSave(w, r, user.ID)
	if !ok {
		return
	}
	data, err := s.readSaveBytes(save)
	if err != nil {
		http.Error(w, "the stored save file is missing", http.StatusNotFound)
		return
	}
	pouches, err := s.pokemon.ReadBag(r.Context(), data)
	if err != nil {
		writeSidecarError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pouches": pouches})
}

// bagWriteRequest is what a caller may replace a save's bag with: only the
// pouches named are touched, each replaced wholesale, see pokemon.WriteBag.
type bagWriteRequest struct {
	Pouches []pokemon.BagPouch `json:"pouches"`
}

// setSaveBag rewrites a save's item bag via the sidecar and records the
// result as a new save version, the same "edit produces a new version"
// convention withdrawPokehome already uses for an injected Pokémon.
func (s *CatalogServer) setSaveBag(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in to edit a save's bag", http.StatusUnauthorized)
		return
	}
	if s.pokemon == nil || !s.pokemon.Available() {
		http.Error(w, "Pokémon features are disabled", http.StatusServiceUnavailable)
		return
	}
	save, ok := s.ownedSave(w, r, user.ID)
	if !ok {
		return
	}
	var body bagWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	data, err := s.readSaveBytes(save)
	if err != nil {
		http.Error(w, "the stored save file is missing", http.StatusNotFound)
		return
	}
	newBytes, err := s.pokemon.WriteBag(ctx, data, body.Pouches)
	if err != nil {
		writeSidecarError(w, err)
		return
	}
	saved, err := s.storeNewSaveVersion(ctx, save, newBytes)
	if err != nil {
		http.Error(w, "could not record the updated save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pouches, err := s.pokemon.ReadBag(ctx, newBytes)
	if err != nil {
		writeSidecarError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"save_id": saved.ID, "pouches": pouches})
}

// ownedSave resolves the save named in the URL, scoped to its owner. A save that
// belongs to someone else is reported as missing, which avoids confirming that
// the id exists.
func (s *CatalogServer) ownedSave(w http.ResponseWriter, r *http.Request, ownerID string) (catalog.Save, bool) {
	if s.saves == nil {
		http.Error(w, "save storage is not configured", http.StatusServiceUnavailable)
		return catalog.Save{}, false
	}
	save, err := s.store.SaveByID(r.Context(), ownerID, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such save", http.StatusNotFound)
		return catalog.Save{}, false
	}
	if err != nil {
		http.Error(w, "could not read the save", http.StatusInternalServerError)
		return catalog.Save{}, false
	}
	return save, true
}

// contentDisposition offers the save under its original name. The name is quoted
// and stripped of quotes and control characters so it cannot forge headers; the
// filename never reaches the filesystem, only this header.
func contentDisposition(name string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if clean == "" {
		clean = "save.sav"
	}
	return fmt.Sprintf("attachment; filename=%q", clean)
}

// savesPage serves the Save Data view: every save you have stored, per game.
func (s *CatalogServer) savesPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "saves", "saves", s.newPageData(r))
}
