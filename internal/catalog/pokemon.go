package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// PokemonMove is a single learned move on a stored Pokémon.
type PokemonMove struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// PokemonInstance is one stored Pokémon, extracted from an uploaded save by the
// PKHeX sidecar. The raw entity bytes are preserved in Binary so the exact
// Pokémon can later be re-injected into a save (the transfer/PokeHOME workflow).
type PokemonInstance struct {
	ID          string         `json:"id"`
	OwnerID     string         `json:"owner_id"`
	Fingerprint string         `json:"-"`
	Species     int            `json:"species"`
	SpeciesName string         `json:"species_name"`
	Nickname    string         `json:"nickname"`
	Level       int            `json:"level"`
	Nature      string         `json:"nature,omitempty"`
	Ability     string         `json:"ability,omitempty"`
	Gender      string         `json:"gender,omitempty"`
	Shiny       bool           `json:"shiny"`
	IsEgg       bool           `json:"is_egg"`
	Form        int            `json:"form"`
	Generation  int            `json:"generation,omitempty"`
	IVs         map[string]int `json:"ivs,omitempty"`
	EVs         map[string]int `json:"evs,omitempty"`
	Moves       []PokemonMove  `json:"moves,omitempty"`
	HeldItem    string         `json:"held_item,omitempty"`
	Ball        int            `json:"ball,omitempty"`
	PID         int64          `json:"-"`
	OriginGame  string         `json:"origin_game,omitempty"`
	OriginOT    string         `json:"origin_ot,omitempty"`
	SourceSave  string         `json:"source_save,omitempty"`
	// OriginSaveID links a Pokémon to the stored save it was extracted from,
	// giving the PokeHOME workflow the ownership history it needs. Pokémon
	// ingested before the save vault existed have no link.
	OriginSaveID string `json:"origin_save_id,omitempty"`
	// OriginSlot is where this Pokémon sat in its origin save at ingest time
	// ("party:2", "box:3:12"), empty for one created from scratch, which
	// never lived in a save slot at all. The Editor's Party/Box grouping
	// reads this to show where each Pokémon actually lives, instead of one
	// flat undifferentiated list.
	OriginSlot string    `json:"origin_slot,omitempty"`
	Binary     []byte    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`

	// HomeDepositedAt is set while this instance is deposited in PokeHome,
	// exempt from PruneDexOrphans, listed by DepositedPokemonInstances,
	// eligible for withdrawal.
	HomeDepositedAt *time.Time `json:"home_deposited_at,omitempty"`
	// HomeLastWithdrawnAt records the most recent successful withdrawal.
	// Bookkeeping only: withdrawal copies rather than consumes a deposit, so
	// this never gates anything.
	HomeLastWithdrawnAt *time.Time `json:"home_last_withdrawn_at,omitempty"`
}

// PokemonInstanceID computes the deterministic id SavePokemonInstances
// assigns a freshly ingested Pokémon, from its owner and raw entity bytes.
// Exported so a caller that needs the id before, or without, re-querying the
// database, writing the matching .pkm file alongside the insert, computes
// exactly the same value rather than duplicating the hash formula.
func PokemonInstanceID(ownerID string, binary []byte) string {
	return hex.EncodeToString(sha256Sum(ownerID + "\x00" + pokemonFingerprint(binary))[:12])
}

func pokemonFingerprint(binary []byte) string {
	return hex.EncodeToString(sha256Sum(string(binary))[:])
}

// EnsureDefaultUser returns the id of the built-in admin account, creating it on
// first use. This is for seeding that account at startup only.
//
// It is not a way to find out who is asking: ROM Relay is multi-user now, and
// request handlers must read the signed-in user instead. Using this at request
// time is what once showed every user the admin's Pokédex.
func (s *Store) EnsureDefaultUser(ctx context.Context) (string, error) {
	const username = "admin"
	id := hex.EncodeToString(sha256Sum("user\x00" + username)[:12])
	if _, err := s.execCtx(ctx, `INSERT INTO users(id,username,role,created_at) VALUES(?,?,?,?) ON CONFLICT(username) DO NOTHING`,
		id, username, "admin", time.Now().Unix()); err != nil {
		return "", err
	}
	var got string
	if err := s.queryRowCtx(ctx, `SELECT id FROM users WHERE username=?`, username).Scan(&got); err != nil {
		return "", err
	}
	return got, nil
}

// SavePokemonInstances persists parsed Pokémon for an owner. Duplicates (same
// owner and identical entity bytes) are skipped, so re-uploading a save does not
// multiply its Pokémon. Returns each entry's id (in list order, whether newly
// inserted or a pre-existing duplicate, a caller materializing .pkm files
// needs the id either way), how many were newly inserted, and how many were
// skipped as duplicates.
func (s *Store) SavePokemonInstances(ctx context.Context, ownerID string, list []PokemonInstance) (ids []string, inserted, skipped int, err error) {
	ids = make([]string, len(list))
	for i, p := range list {
		fingerprint := pokemonFingerprint(p.Binary)
		id := hex.EncodeToString(sha256Sum(ownerID + "\x00" + fingerprint)[:12])
		ids[i] = id
		ivs, _ := json.Marshal(p.IVs)
		evs, _ := json.Marshal(p.EVs)
		moves, _ := json.Marshal(p.Moves)
		res, execErr := s.execCtx(ctx, `INSERT INTO pokemon_instances(
id,owner_id,fingerprint,species,species_name,nickname,level,nature,ability,gender,shiny,is_egg,form,generation,
ivs_json,evs_json,moves_json,held_item,ball,pid,origin_game,origin_ot,source_save,origin_save_id,origin_slot,pokemon_binary,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(owner_id,fingerprint) DO NOTHING`,
			id, ownerID, fingerprint, p.Species, p.SpeciesName, p.Nickname, p.Level, p.Nature, p.Ability, p.Gender,
			p.Shiny, p.IsEgg, p.Form, p.Generation, string(ivs), string(evs), string(moves), p.HeldItem, p.Ball, p.PID,
			p.OriginGame, p.OriginOT, p.SourceSave, p.OriginSaveID, p.OriginSlot, p.Binary, time.Now().Unix())
		if execErr != nil {
			return ids, inserted, skipped, execErr
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		} else {
			skipped++
		}
	}
	return ids, inserted, skipped, nil
}

// PokemonInstances lists an owner's stored Pokémon, oldest first. The raw entity
// blob is omitted from the listing for size.
func (s *Store) PokemonInstances(ctx context.Context, ownerID string) ([]PokemonInstance, error) {
	rows, err := s.queryCtx(ctx, `SELECT id,owner_id,species,species_name,nickname,level,nature,ability,gender,shiny,is_egg,form,generation,ivs_json,evs_json,moves_json,held_item,ball,origin_game,origin_ot,source_save,origin_save_id,origin_slot,created_at,home_deposited_at,home_last_withdrawn_at FROM pokemon_instances WHERE owner_id=? ORDER BY created_at ASC,id ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PokemonInstance
	for rows.Next() {
		var p PokemonInstance
		var ivs, evs, moves string
		var created int64
		var deposited, withdrawn sql.NullInt64
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.Species, &p.SpeciesName, &p.Nickname, &p.Level, &p.Nature, &p.Ability,
			&p.Gender, &p.Shiny, &p.IsEgg, &p.Form, &p.Generation, &ivs, &evs, &moves, &p.HeldItem, &p.Ball,
			&p.OriginGame, &p.OriginOT, &p.SourceSave, &p.OriginSaveID, &p.OriginSlot, &created, &deposited, &withdrawn); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ivs), &p.IVs)
		_ = json.Unmarshal([]byte(evs), &p.EVs)
		_ = json.Unmarshal([]byte(moves), &p.Moves)
		p.CreatedAt = time.Unix(created, 0)
		p.HomeDepositedAt = nullTime(deposited)
		p.HomeLastWithdrawnAt = nullTime(withdrawn)
		out = append(out, p)
	}
	return out, rows.Err()
}

// nullTime converts a nullable unix-timestamp column into the *time.Time the
// API contract uses: present only when the column was set.
func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

// pokemonInstanceColumns is every pokemon_instances column PokemonInstanceByID
// and DepositedPokemonInstances read, including the raw entity bytes,
// PokemonInstances (the plain listing) omits pokemon_binary for size, but the
// deposit/withdraw/edit workflows all need the actual bytes to act on.
const pokemonInstanceColumns = `SELECT id,owner_id,species,species_name,nickname,level,nature,ability,gender,shiny,is_egg,form,generation,ivs_json,evs_json,moves_json,held_item,ball,pid,origin_game,origin_ot,source_save,origin_save_id,origin_slot,pokemon_binary,created_at,home_deposited_at,home_last_withdrawn_at FROM pokemon_instances`

func scanPokemonInstance(row rowScanner) (PokemonInstance, error) {
	var p PokemonInstance
	var ivs, evs, moves string
	var created int64
	var deposited, withdrawn sql.NullInt64
	if err := row.Scan(&p.ID, &p.OwnerID, &p.Species, &p.SpeciesName, &p.Nickname, &p.Level, &p.Nature, &p.Ability,
		&p.Gender, &p.Shiny, &p.IsEgg, &p.Form, &p.Generation, &ivs, &evs, &moves, &p.HeldItem, &p.Ball, &p.PID,
		&p.OriginGame, &p.OriginOT, &p.SourceSave, &p.OriginSaveID, &p.OriginSlot, &p.Binary, &created, &deposited, &withdrawn); err != nil {
		return PokemonInstance{}, err
	}
	_ = json.Unmarshal([]byte(ivs), &p.IVs)
	_ = json.Unmarshal([]byte(evs), &p.EVs)
	_ = json.Unmarshal([]byte(moves), &p.Moves)
	p.CreatedAt = time.Unix(created, 0)
	p.HomeDepositedAt = nullTime(deposited)
	p.HomeLastWithdrawnAt = nullTime(withdrawn)
	return p, nil
}

// PokemonInstanceByID reads one of an owner's stored Pokémon, including its
// raw entity bytes. Ownership is part of the query: another owner's instance
// is reported as sql.ErrNoRows, identical to one that does not exist.
func (s *Store) PokemonInstanceByID(ctx context.Context, ownerID, id string) (PokemonInstance, error) {
	return scanPokemonInstance(s.queryRowCtx(ctx, pokemonInstanceColumns+` WHERE id=? AND owner_id=?`, id, ownerID))
}

// DepositedPokemonInstances lists an owner's currently-deposited PokeHome
// Pokémon, newest deposit first, with their raw entity bytes included,
// callers render the PC box grid and can withdraw straight from this list
// without a second lookup.
func (s *Store) DepositedPokemonInstances(ctx context.Context, ownerID string) ([]PokemonInstance, error) {
	rows, err := s.queryCtx(ctx, pokemonInstanceColumns+` WHERE owner_id=? AND home_deposited_at IS NOT NULL ORDER BY home_deposited_at DESC,id ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PokemonInstance
	for rows.Next() {
		p, err := scanPokemonInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DepositPokemonInstance marks one of an owner's stored Pokémon as deposited
// in PokeHome, exempting it from PruneDexOrphans. Idempotent: depositing an
// already-deposited instance leaves its original deposit time untouched and
// is not an error. Returns sql.ErrNoRows for an unknown id or one owned by
// someone else.
func (s *Store) DepositPokemonInstance(ctx context.Context, ownerID, id string) (PokemonInstance, error) {
	if _, err := s.execCtx(ctx, `UPDATE pokemon_instances SET home_deposited_at=? WHERE id=? AND owner_id=? AND home_deposited_at IS NULL`,
		time.Now().Unix(), id, ownerID); err != nil {
		return PokemonInstance{}, err
	}
	return s.PokemonInstanceByID(ctx, ownerID, id)
}

// MarkPokemonWithdrawn records that a deposited instance was just injected
// into a save. Bookkeeping only: it does not clear home_deposited_at, so the
// same deposit remains withdrawable again, PokeHome copies, it does not
// consume (see the design doc's "duplication is allowed" decision).
func (s *Store) MarkPokemonWithdrawn(ctx context.Context, ownerID, id string) error {
	res, err := s.execCtx(ctx, `UPDATE pokemon_instances SET home_last_withdrawn_at=? WHERE id=? AND owner_id=? AND home_deposited_at IS NOT NULL`,
		time.Now().Unix(), id, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeletePokemonInstance removes one stored Pokémon outright, a deposited
// PokeHome mon someone no longer wants, or any other owned instance. Unlike
// PruneDexOrphans/DeleteSave's cascades, this is a direct request for this one
// row: it does not care whether a deposit exists or what its origin save is.
// Returns sql.ErrNoRows for an unknown id or one owned by someone else, so the
// caller can 404 rather than report success for nothing.
func (s *Store) DeletePokemonInstance(ctx context.Context, ownerID, id string) error {
	res, err := s.execCtx(ctx, `DELETE FROM pokemon_instances WHERE id=? AND owner_id=?`, id, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdatePokemonInstanceEntity overwrites a stored Pokémon's fields and raw
// bytes after an edit (see the sidecar's POST /edit). The row's id is
// unchanged, foreign references (a PokeHome deposit, the .pkm file on disk)
// stay valid, but its fingerprint is recomputed from the new bytes, so a
// future ingest that happens to produce identical bytes still dedups
// correctly against the edited row.
func (s *Store) UpdatePokemonInstanceEntity(ctx context.Context, ownerID, id string, p PokemonInstance) error {
	fingerprint := pokemonFingerprint(p.Binary)
	ivs, _ := json.Marshal(p.IVs)
	evs, _ := json.Marshal(p.EVs)
	moves, _ := json.Marshal(p.Moves)
	res, err := s.execCtx(ctx, `UPDATE pokemon_instances SET
fingerprint=?,species=?,species_name=?,nickname=?,level=?,nature=?,ability=?,gender=?,shiny=?,form=?,
ivs_json=?,evs_json=?,moves_json=?,held_item=?,ball=?,pid=?,pokemon_binary=?
WHERE id=? AND owner_id=?`,
		fingerprint, p.Species, p.SpeciesName, p.Nickname, p.Level, p.Nature, p.Ability, p.Gender, p.Shiny, p.Form,
		string(ivs), string(evs), string(moves), p.HeldItem, p.Ball, p.PID, p.Binary, id, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DexRow aggregates an owner's Pokémon by species and origin game, for building
// Living Dex and per-game Pokédex views.
type DexRow struct {
	Species int
	Game    string
	Count   int
	Shiny   bool
}

// PokemonDexRows returns per-species, per-game ownership counts for an owner.
// Eggs are excluded: an unhatched egg is not a Pokémon the dex can claim.
func (s *Store) PokemonDexRows(ctx context.Context, ownerID string) ([]DexRow, error) {
	rows, err := s.queryCtx(ctx, `SELECT species,origin_game,COUNT(*),MAX(CASE WHEN shiny THEN 1 ELSE 0 END) FROM pokemon_instances WHERE owner_id=? AND NOT is_egg GROUP BY species,origin_game`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DexRow
	for rows.Next() {
		var r DexRow
		var shiny int
		if err := rows.Scan(&r.Species, &r.Game, &r.Count, &shiny); err != nil {
			return nil, err
		}
		r.Shiny = shiny != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetGameDexMax records how many species a game's own Pokédex covers, so its
// completion can be measured against that rather than the National total. The
// largest value wins: a later save of the same game reporting a smaller range
// would otherwise shrink a dex that is already recorded.
func (s *Store) SetGameDexMax(ctx context.Context, ownerID, game string, maxSpecies int) error {
	if game == "" || maxSpecies <= 0 {
		return nil
	}
	_, err := s.execCtx(ctx, `INSERT INTO game_dex_meta(owner_id,game,max_species) VALUES(?,?,?) ON CONFLICT(owner_id,game) DO UPDATE SET max_species=CASE WHEN game_dex_meta.max_species>excluded.max_species THEN game_dex_meta.max_species ELSE excluded.max_species END`,
		ownerID, game, maxSpecies)
	return err
}

// GameDexMax returns the species count a game's Pokédex covers, or 0 when it was
// never recorded (a save ingested before this was stored).
func (s *Store) GameDexMax(ctx context.Context, ownerID, game string) (int, error) {
	var max int
	err := s.queryRowCtx(ctx, `SELECT max_species FROM game_dex_meta WHERE owner_id=? AND game=?`, ownerID, game).Scan(&max)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return max, err
}

// UpsertGameDex records the seen/caught Pokédex flags read from a save for a
// game, accumulating with any previous record for that playthrough (a species
// stays seen/caught once recorded). caught implies seen. trainer identifies the
// playthrough ("<TID>:<SID>"); "" is the unknown trainer, which pre-trainer
// rows and untrusted saves use.
func (s *Store) UpsertGameDex(ctx context.Context, ownerID, game, trainer string, seen, caught []int) error {
	if game == "" || (len(seen) == 0 && len(caught) == 0) {
		return nil
	}
	flags := map[int][2]bool{} // species -> {seen, caught}
	for _, sp := range seen {
		flags[sp] = [2]bool{true, flags[sp][1]}
	}
	for _, sp := range caught {
		flags[sp] = [2]bool{true, true}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for species, f := range flags {
		if _, err := s.texecCtx(ctx, tx, `INSERT INTO game_dex(owner_id,game,trainer,species,seen,caught) VALUES(?,?,?,?,?,?) ON CONFLICT(owner_id,game,trainer,species) DO UPDATE SET seen=(game_dex.seen OR excluded.seen),caught=(game_dex.caught OR excluded.caught)`,
			ownerID, game, trainer, species, f[0], f[1]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GameDexCaught returns the caught species (National Pokédex numbers) recorded
// for a game from its save's Pokédex, the basis for the true regional dex view.
// It is the merged view: an OR across every trainer, DISTINCT because a species
// caught in two playthroughs is still one species.
func (s *Store) GameDexCaught(ctx context.Context, ownerID, game string) ([]DexRow, error) {
	rows, err := s.queryCtx(ctx, `SELECT DISTINCT species FROM game_dex WHERE owner_id=? AND game=? AND caught=TRUE`, ownerID, game)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DexRow
	for rows.Next() {
		var sp int
		if err := rows.Scan(&sp); err != nil {
			return nil, err
		}
		out = append(out, DexRow{Species: sp, Game: game, Count: 1})
	}
	return out, rows.Err()
}

// GameDexCaughtFor returns one playthrough's caught species. GameDexCaught is
// the merged view across every trainer for the game; this is a single run.
func (s *Store) GameDexCaughtFor(ctx context.Context, ownerID, game, trainer string) ([]DexRow, error) {
	rows, err := s.queryCtx(ctx, `SELECT species FROM game_dex WHERE owner_id=? AND game=? AND trainer=? AND caught=TRUE`, ownerID, game, trainer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DexRow
	for rows.Next() {
		var sp int
		if err := rows.Scan(&sp); err != nil {
			return nil, err
		}
		out = append(out, DexRow{Species: sp, Game: game, Count: 1})
	}
	return out, rows.Err()
}

// GameDexCaughtAll returns every species with a caught flag for an owner across
// all games, the global caught tracker. A species caught in two games is one
// row per game; callers deduplicate.
func (s *Store) GameDexCaughtAll(ctx context.Context, ownerID string) ([]DexRow, error) {
	rows, err := s.queryCtx(ctx, `SELECT DISTINCT species,game FROM game_dex WHERE owner_id=? AND caught=TRUE`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DexRow
	for rows.Next() {
		var r DexRow
		if err := rows.Scan(&r.Species, &r.Game); err != nil {
			return nil, err
		}
		r.Count = 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// GameDexGames lists the games that have recorded Pokédex flags for an owner.
func (s *Store) GameDexGames(ctx context.Context, ownerID string) ([]string, error) {
	rows, err := s.queryCtx(ctx, `SELECT DISTINCT game FROM game_dex WHERE owner_id=? ORDER BY game`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// ClearDexForUser wipes one account's Pokédex-derived data, stored Pokémon,
// per-game dex flags, and trainer profiles, while keeping its saves. The next
// save upload rebuilds everything from the file. Deposited PokeHome Pokémon
// are wiped too: this is an explicit whole-account reset, not
// PruneDexOrphans' save-still-exists judgment call. Returns the removed
// pokemon_instances ids so the caller can also delete their .pkm files.
func (s *Store) ClearDexForUser(ctx context.Context, ownerID string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids, err := s.pokemonIDsWhere(ctx, tx, `owner_id=?`, ownerID)
	if err != nil {
		return nil, err
	}
	for _, q := range []string{
		`DELETE FROM pokemon_instances WHERE owner_id=?`,
		`DELETE FROM game_dex WHERE owner_id=?`,
		`DELETE FROM game_dex_meta WHERE owner_id=?`,
		`DELETE FROM trainer_hof WHERE owner_id=?`,
		`DELETE FROM trainer_profiles WHERE owner_id=?`,
	} {
		if _, err := s.texecCtx(ctx, tx, q, ownerID); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit()
}

// PokemonRef identifies one stored Pokémon by owner and id. PruneDexOrphans
// returns these rather than bare ids since its sweep spans every user, and a
// caller cleaning up .pkm files needs the owner to find the right file.
type PokemonRef struct {
	OwnerID string
	ID      string
}

// PruneDexOrphans deletes stored Pokémon whose source save no longer exists
// and that were never deposited in PokeHome, for every user. Instances
// without a recorded source are kept: nothing proves them stale. A deposit
// exempts an instance regardless of its source save's fate, that is the
// entire point of depositing. Returns the removed rows' owner/id pairs so the
// caller can also delete their .pkm files.
func (s *Store) PruneDexOrphans(ctx context.Context) ([]PokemonRef, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	const where = `origin_save_id<>'' AND origin_save_id NOT IN (SELECT id FROM saves) AND home_deposited_at IS NULL`
	rows, err := s.tqueryCtx(ctx, tx, `SELECT owner_id,id FROM pokemon_instances WHERE `+where)
	if err != nil {
		return nil, err
	}
	var refs []PokemonRef
	for rows.Next() {
		var ref PokemonRef
		if err := rows.Scan(&ref.OwnerID, &ref.ID); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(refs) > 0 {
		if _, err := s.texecCtx(ctx, tx, `DELETE FROM pokemon_instances WHERE `+where); err != nil {
			return nil, err
		}
	}
	return refs, tx.Commit()
}

// pokemonIDsWhere collects the ids of every pokemon_instances row a WHERE
// clause matches, inside a caller-owned transaction, the read half of a
// read-then-delete pair, so the caller can act on the same set it deletes
// (writing/removing .pkm files) rather than guessing from a row count.
func (s *Store) pokemonIDsWhere(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]string, error) {
	rows, err := s.tqueryCtx(ctx, tx, `SELECT id FROM pokemon_instances WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
