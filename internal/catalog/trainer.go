package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// TrainerProfile is one playthrough: a game plus the trainer ID that identifies
// the run. A restarted game has a new TID/SID, so it becomes a second profile
// beside the first rather than overwriting it.
type TrainerProfile struct {
	OwnerID       string `json:"-"`
	Game          string `json:"game"`
	TID           int    `json:"tid"`
	SID           int    `json:"sid"`
	OT            string `json:"ot"`
	Gender        int    `json:"gender"`
	Language      int    `json:"language"`
	PlayedMinutes int    `json:"played_minutes"`
	Money         int64  `json:"money"`
	MoneyMax      int64  `json:"money_max"`
	ProgressKind  string `json:"progress_kind"`
	ProgressCount int    `json:"progress_count"`
	ProgressMax   int    `json:"progress_max"`
	// HOFClears is nil when the game does not count clears at all (gen 2 records
	// nothing; gen 7 records the team but no count). nil and 0 are different
	// claims and must never render the same.
	HOFClears    *int              `json:"hof_clears"`
	Notes        map[string]string `json:"notes"`
	Trusted      bool              `json:"trusted"`
	SourceSaveID string            `json:"source_save_id"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// ID is the profile's stable handle in URLs. It is derived rather than stored:
// the primary key already identifies a playthrough.
func (p TrainerProfile) ID() string {
	return p.Game + ":" + strconv.Itoa(p.TID) + ":" + strconv.Itoa(p.SID)
}

// TrainerKey is the game_dex table's trainer column format: TID:SID, without
// the game, since game_dex rows are already scoped to one game by their own
// column. This is the one place that format is spelled, the upload path
// writes it, DeleteSave and the userdex API both read it back, and two copies
// of this rule would drift silently (a delete that never matches, or a dex
// that reads empty).
func (p TrainerProfile) TrainerKey() string {
	return strconv.Itoa(p.TID) + ":" + strconv.Itoa(p.SID)
}

// HOFEntry is one Pokémon in one recorded Hall of Fame visit.
type HOFEntry struct {
	EntryIndex int    `json:"entry_index"`
	Slot       int    `json:"slot"`
	Species    int    `json:"species"`
	Level      int    `json:"level"`
	Nickname   string `json:"nickname"`
}

// UpsertTrainerProfile writes a playthrough, keeping whichever save has the
// higher play time.
//
// Play time only increases within a playthrough, so it, and not upload order,
// decides which save's numbers win. Keying on the newest upload would let a
// restored backup roll a profile backwards, and would make uploading saves out
// of order lossy.
func (s *Store) UpsertTrainerProfile(ctx context.Context, p TrainerProfile) error {
	notes, err := json.Marshal(p.Notes)
	if err != nil {
		return err
	}
	if p.Notes == nil {
		notes = []byte("{}")
	}
	_, err = s.execCtx(ctx, `INSERT INTO trainer_profiles(owner_id,game,tid,sid,ot,gender,language,played_minutes,money,money_max,progress_kind,progress_count,progress_max,hof_clears,notes,trusted,source_save_id,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(owner_id,game,tid,sid) DO UPDATE SET
ot=excluded.ot,gender=excluded.gender,language=excluded.language,
played_minutes=excluded.played_minutes,money=excluded.money,money_max=excluded.money_max,
progress_kind=excluded.progress_kind,progress_count=excluded.progress_count,progress_max=excluded.progress_max,
hof_clears=excluded.hof_clears,notes=excluded.notes,trusted=excluded.trusted,
source_save_id=excluded.source_save_id,updated_at=excluded.updated_at
WHERE excluded.played_minutes >= trainer_profiles.played_minutes`,
		p.OwnerID, p.Game, p.TID, p.SID, p.OT, p.Gender, p.Language, p.PlayedMinutes,
		p.Money, p.MoneyMax, p.ProgressKind, p.ProgressCount, p.ProgressMax,
		p.HOFClears, string(notes), p.Trusted, p.SourceSaveID, time.Now().Unix())
	return err
}

// TrainerProfiles lists an owner's playthroughs, most recently updated first.
func (s *Store) TrainerProfiles(ctx context.Context, ownerID string) ([]TrainerProfile, error) {
	rows, err := s.queryCtx(ctx, trainerColumns+` FROM trainer_profiles WHERE owner_id=? ORDER BY updated_at DESC,game,tid,sid`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrainerProfile
	for rows.Next() {
		p, err := scanTrainerProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// clearTrainerCascade deletes one playthrough's Hall of Fame, its per-trainer
// game_dex rows, and the trainer_profiles row itself, inside a caller-owned
// transaction. Shared by DeleteSave (a profile's only save just disappeared)
// and ClearTrainerDex (an admin explicitly clearing selected playthroughs),
// one place spells "delete a trainer's dex data" so the two callers can't drift.
func (s *Store) clearTrainerCascade(ctx context.Context, tx *sql.Tx, ownerID string, p TrainerProfile) error {
	if _, err := s.texecCtx(ctx, tx, `DELETE FROM trainer_hof WHERE owner_id=? AND game=? AND tid=? AND sid=?`, ownerID, p.Game, p.TID, p.SID); err != nil {
		return err
	}
	if _, err := s.texecCtx(ctx, tx, `DELETE FROM game_dex WHERE owner_id=? AND game=? AND trainer=?`, ownerID, p.Game, p.TrainerKey()); err != nil {
		return err
	}
	_, err := s.texecCtx(ctx, tx, `DELETE FROM trainer_profiles WHERE owner_id=? AND game=? AND tid=? AND sid=?`, ownerID, p.Game, p.TID, p.SID)
	return err
}

// ClearTrainerDex removes the Hall of Fame, per-trainer dex flags, and profile
// row for each of an owner's selected playthroughs, the per-trainer
// counterpart to ClearDexForUser's whole-account wipe, for the admin Player
// Data page's "Clear Associated Dex Data" action. Saves and stored Pokémon
// are left alone: this clears dex progress, not the account. An id that does
// not parse or does not belong to this owner is skipped rather than erroring,
// so a stale selection can't fail the whole batch.
func (s *Store) ClearTrainerDex(ctx context.Context, ownerID string, trainerIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range trainerIDs {
		game, tid, sid, ok := splitProfileID(id)
		if !ok {
			continue
		}
		if err := s.clearTrainerCascade(ctx, tx, ownerID, TrainerProfile{Game: game, TID: tid, SID: sid}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TrainerProfileByID resolves a profile handle (game:tid:sid) for an owner.
// Another owner's profile is simply not found.
func (s *Store) TrainerProfileByID(ctx context.Context, ownerID, id string) (TrainerProfile, bool, error) {
	game, tid, sid, ok := splitProfileID(id)
	if !ok {
		return TrainerProfile{}, false, nil
	}
	row := s.queryRowCtx(ctx, trainerColumns+` FROM trainer_profiles WHERE owner_id=? AND game=? AND tid=? AND sid=?`, ownerID, game, tid, sid)
	p, err := scanTrainerProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TrainerProfile{}, false, nil
	}
	if err != nil {
		return TrainerProfile{}, false, err
	}
	return p, true, nil
}

// ReplaceTrainerHOF swaps a profile's Hall of Fame for the given snapshot in one
// transaction. A Hall of Fame is a snapshot, not an accumulation, merging would
// blend two runs.
func (s *Store) ReplaceTrainerHOF(ctx context.Context, p TrainerProfile, entries []HOFEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.texecCtx(ctx, tx, `DELETE FROM trainer_hof WHERE owner_id=? AND game=? AND tid=? AND sid=?`, p.OwnerID, p.Game, p.TID, p.SID); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := s.texecCtx(ctx, tx, `INSERT INTO trainer_hof(owner_id,game,tid,sid,entry_index,slot,species,level,nickname) VALUES(?,?,?,?,?,?,?,?,?)`,
			p.OwnerID, p.Game, p.TID, p.SID, e.EntryIndex, e.Slot, e.Species, e.Level, e.Nickname); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TrainerHOF returns a profile's recorded Hall of Fame entries in order.
func (s *Store) TrainerHOF(ctx context.Context, ownerID, id string) ([]HOFEntry, error) {
	game, tid, sid, ok := splitProfileID(id)
	if !ok {
		return nil, nil
	}
	rows, err := s.queryCtx(ctx, `SELECT entry_index,slot,species,level,nickname FROM trainer_hof WHERE owner_id=? AND game=? AND tid=? AND sid=? ORDER BY entry_index,slot`, ownerID, game, tid, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HOFEntry
	for rows.Next() {
		var e HOFEntry
		if err := rows.Scan(&e.EntryIndex, &e.Slot, &e.Species, &e.Level, &e.Nickname); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const trainerColumns = `SELECT owner_id,game,tid,sid,ot,gender,language,played_minutes,money,money_max,progress_kind,progress_count,progress_max,hof_clears,notes,trusted,source_save_id,updated_at`

func scanTrainerProfile(row rowScanner) (TrainerProfile, error) {
	var p TrainerProfile
	var clears sql.NullInt64
	var notes string
	var updated int64
	if err := row.Scan(&p.OwnerID, &p.Game, &p.TID, &p.SID, &p.OT, &p.Gender, &p.Language,
		&p.PlayedMinutes, &p.Money, &p.MoneyMax, &p.ProgressKind, &p.ProgressCount, &p.ProgressMax,
		&clears, &notes, &p.Trusted, &p.SourceSaveID, &updated); err != nil {
		return TrainerProfile{}, err
	}
	if clears.Valid {
		n := int(clears.Int64)
		p.HOFClears = &n
	}
	if notes != "" {
		_ = json.Unmarshal([]byte(notes), &p.Notes)
	}
	p.UpdatedAt = time.Unix(updated, 0)
	return p, nil
}

// splitProfileID parses "game:tid:sid". The game code itself never contains a
// colon, so splitting from the right is unambiguous.
func splitProfileID(id string) (game string, tid, sid int, ok bool) {
	last := -1
	second := -1
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] != ':' {
			continue
		}
		if last == -1 {
			last = i
		} else {
			second = i
			break
		}
	}
	if last == -1 || second == -1 || second == 0 {
		return "", 0, 0, false
	}
	tid, err1 := strconv.Atoi(id[second+1 : last])
	sid, err2 := strconv.Atoi(id[last+1:])
	if err1 != nil || err2 != nil {
		return "", 0, 0, false
	}
	return id[:second], tid, sid, true
}
