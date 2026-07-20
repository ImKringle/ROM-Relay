package catalog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strconv"
	"time"
)

// Save sources. Uploads come from a user; transfers are produced by writing a
// stored Pokémon into a copy of an existing save (the PokeHOME workflow), which
// always yields a new version rather than modifying one.
const (
	SaveSourceUpload   = "upload"
	SaveSourceTransfer = "transfer"
)

// Save is one stored version of a user's game save. Versions are immutable and
// accumulate: a re-upload for the same game adds a row rather than replacing
// one, so a bad upload can never destroy the only copy. The bytes themselves
// live outside the database, keyed by ID.
type Save struct {
	ID         string    `json:"id"`
	OwnerID    string    `json:"-"`
	GameID     string    `json:"game_id,omitempty"`
	GameLabel  string    `json:"game_label"`
	FileName   string    `json:"file_name"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	Source     string    `json:"source"`
	UploadedAt time.Time `json:"uploaded_at"`

	// Parsed reports whether the PKHeX sidecar recognised this save. The
	// remaining fields are populated only when it did.
	Parsed         bool   `json:"parsed"`
	ParsedGame     string `json:"parsed_game,omitempty"`
	ParsedTrainer  string `json:"parsed_trainer,omitempty"`
	ParsedPlaytime string `json:"parsed_playtime,omitempty"`
	PokemonCount   int    `json:"pokemon_count,omitempty"`
}

// CreateSave records a new version of a save for its owner. It reports whether a
// row was created: re-uploading bytes identical to the game's newest version
// returns that version untouched rather than storing a duplicate.
func (s *Store) CreateSave(ctx context.Context, save Save) (Save, bool, error) {
	if existing, found, err := s.newestSave(ctx, save.OwnerID, save.GameID); err != nil {
		return Save{}, false, err
	} else if found && existing.SHA256 == save.SHA256 {
		return existing, false, nil
	}
	save.ID = newSaveID()
	if save.Source == "" {
		save.Source = SaveSourceUpload
	}
	save.UploadedAt = time.Now()
	if _, err := s.execCtx(ctx, `INSERT INTO saves(
id,owner_id,game_id,game_label,file_name,size_bytes,sha256,source,uploaded_at,parsed,parsed_game,parsed_trainer,parsed_playtime,pokemon_count)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		save.ID, save.OwnerID, save.GameID, save.GameLabel, save.FileName, save.SizeBytes, save.SHA256,
		save.Source, save.UploadedAt.UnixNano(), save.Parsed, save.ParsedGame, save.ParsedTrainer,
		save.ParsedPlaytime, save.PokemonCount); err != nil {
		return Save{}, false, err
	}
	return save, true, nil
}

// SavesForOwner lists every version an owner has stored, newest first.
func (s *Store) SavesForOwner(ctx context.Context, ownerID string) ([]Save, error) {
	rows, err := s.queryCtx(ctx, saveColumns+` FROM saves WHERE owner_id=? ORDER BY uploaded_at DESC,id DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Save
	for rows.Next() {
		save, err := scanSave(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, save)
	}
	return out, rows.Err()
}

// RecordSaveParse attaches parse results to an existing version. A save stored
// while the sidecar was unreachable is otherwise stuck labelled unreadable, even
// after a later upload reads its Pokémon.
//
// It re-files the save too: a save stored before anything could read it was filed
// under whatever the uploader was looking at, and the parser now knows better.
func (s *Store) RecordSaveParse(ctx context.Context, save Save) error {
	_, err := s.execCtx(ctx, `UPDATE saves SET parsed=?,parsed_game=?,parsed_trainer=?,parsed_playtime=?,pokemon_count=?,game_id=?,game_label=? WHERE id=? AND owner_id=?`,
		true, save.ParsedGame, save.ParsedTrainer, save.ParsedPlaytime, save.PokemonCount,
		save.GameID, save.GameLabel, save.ID, save.OwnerID)
	return err
}

// SaveByID reads one of an owner's saves. Ownership is part of the query rather
// than a separate check, so a caller cannot read another user's save by id.
// A save belonging to someone else is reported as sql.ErrNoRows, identical to a
// save that does not exist, which avoids confirming the id to a stranger.
func (s *Store) SaveByID(ctx context.Context, ownerID, id string) (Save, error) {
	return scanSave(s.queryRowCtx(ctx, saveColumns+` FROM saves WHERE id=? AND owner_id=?`, id, ownerID))
}

// DeleteSave removes one of an owner's saves, returning sql.ErrNoRows if the
// save does not exist or belongs to someone else.
//
// The Pokémon the save introduced go with it: they are that save's Living Dex
// contribution, and leaving them behind kept counting progress from a save the
// user removed. Anything a remaining save also holds comes back on re-upload,
// SavePokemonInstances dedups on identical entity bytes.
//
// A trainer_profiles row is a snapshot of whichever save currently has the
// highest play time for that TID/SID (see UpsertTrainerProfile), recorded in
// source_save_id. Deleting the save that produced a profile removes the
// profile, its Hall of Fame snapshot, and its per-trainer game_dex rows too,
// otherwise the Trainers tab kept showing a playthrough whose only save was
// gone. A profile still backed by a different save (source_save_id points
// elsewhere) is untouched. GameDexCaught's merged view is computed live across
// every trainer's rows, so removing this trainer's rows also drops any species
// only that playthrough ever caught, correct, since nothing else caught it.
//
// A Pokémon deposited in PokeHome (home_deposited_at set) survives its source
// save's deletion, that is the entire point of a deposit, so it is excluded
// from the origin_save_id sweep below. Returns the ids of any pokemon_instances
// rows that were removed, so the caller can also delete their .pkm files.
func (s *Store) DeleteSave(ctx context.Context, ownerID, id string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	profileRows, err := s.tqueryCtx(ctx, tx, `SELECT game,tid,sid FROM trainer_profiles WHERE owner_id=? AND source_save_id=?`, ownerID, id)
	if err != nil {
		return nil, err
	}
	var profiles []TrainerProfile
	for profileRows.Next() {
		var p TrainerProfile
		if err := profileRows.Scan(&p.Game, &p.TID, &p.SID); err != nil {
			profileRows.Close()
			return nil, err
		}
		profiles = append(profiles, p)
	}
	if err := profileRows.Err(); err != nil {
		profileRows.Close()
		return nil, err
	}
	profileRows.Close()

	for _, p := range profiles {
		if err := s.clearTrainerCascade(ctx, tx, ownerID, p); err != nil {
			return nil, err
		}
	}
	pokemonIDs, err := s.pokemonIDsWhere(ctx, tx, `owner_id=? AND origin_save_id=? AND home_deposited_at IS NULL`, ownerID, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.texecCtx(ctx, tx, `DELETE FROM pokemon_instances WHERE owner_id=? AND origin_save_id=? AND home_deposited_at IS NULL`, ownerID, id); err != nil {
		return nil, err
	}
	res, err := s.texecCtx(ctx, tx, `DELETE FROM saves WHERE id=? AND owner_id=?`, id, ownerID)
	if err != nil {
		return nil, err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if affected == 0 {
		return nil, sql.ErrNoRows
	}
	return pokemonIDs, tx.Commit()
}

// SetSaveGameOverride records a user-corrected game for a save the sidecar
// could only guess at, Gen 3 FireRed/LeafGreen and Ruby/Sapphire saves carry
// no cartridge identity in their own bytes, so PKHeX's own guess (surfaced as
// ParsedGame) can land on the wrong half of the pair. Takes precedence over
// that guess everywhere a save's game is displayed or grouped: game_id and
// game_label are the fields every read path (SavesForOwner, the Dex, the
// Trainers page) actually shows, and parsed_game is what a version row's own
// game chip reads, so all three move together.
func (s *Store) SetSaveGameOverride(ctx context.Context, ownerID, id, gameID, gameLabel, parsedGame string) error {
	res, err := s.execCtx(ctx, `UPDATE saves SET game_id=?,game_label=?,parsed_game=? WHERE id=? AND owner_id=?`,
		gameID, gameLabel, parsedGame, id, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// newestSave returns the most recent version for a game, if the owner has one.
// SaveByHash finds an owner's stored save with exactly these bytes, whatever it
// is filed under. Identical bytes are the same save even when the two uploads
// disagree about which game it belongs to, which happens when the first upload
// landed while nothing could read the save and name its game.
func (s *Store) SaveByHash(ctx context.Context, ownerID, sha string) (Save, bool, error) {
	return s.saveWhere(ctx, `WHERE owner_id=? AND sha256=? ORDER BY uploaded_at DESC,id DESC LIMIT 1`, ownerID, sha)
}

func (s *Store) newestSave(ctx context.Context, ownerID, gameID string) (Save, bool, error) {
	return s.saveWhere(ctx, `WHERE owner_id=? AND game_id=? ORDER BY uploaded_at DESC,id DESC LIMIT 1`, ownerID, gameID)
}

func (s *Store) saveWhere(ctx context.Context, clause string, args ...any) (Save, bool, error) {
	rows, err := s.queryCtx(ctx, saveColumns+` FROM saves `+clause, args...)
	if err != nil {
		return Save{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Save{}, false, rows.Err()
	}
	save, err := scanSave(rows)
	if err != nil {
		return Save{}, false, err
	}
	return save, true, nil
}

const saveColumns = `SELECT id,owner_id,game_id,game_label,file_name,size_bytes,sha256,source,uploaded_at,parsed,parsed_game,parsed_trainer,parsed_playtime,pokemon_count`

func scanSave(row rowScanner) (Save, error) {
	var save Save
	var uploaded int64
	if err := row.Scan(&save.ID, &save.OwnerID, &save.GameID, &save.GameLabel, &save.FileName, &save.SizeBytes,
		&save.SHA256, &save.Source, &uploaded, &save.Parsed, &save.ParsedGame, &save.ParsedTrainer,
		&save.ParsedPlaytime, &save.PokemonCount); err != nil {
		return Save{}, err
	}
	save.UploadedAt = time.Unix(0, uploaded)
	return save, nil
}

// newSaveID generates the identifier that also names the save on disk. It is
// server-generated so no user-supplied text ever reaches a filesystem path.
func newSaveID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is not recoverable; fall back to a time-based id
		// rather than returning a predictable or empty one.
		return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 16)))
	}
	return hex.EncodeToString(buf)
}
