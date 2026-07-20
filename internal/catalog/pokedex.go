package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Species is one National Pokédex entry's reference data, as PokeAPI describes
// it. It is static per species, so it is fetched once and read from here after.
//
// GenderRate is PokeAPI's own encoding: -1 means genderless, otherwise it is
// eighths female (1 = 12.5% female, as Charizard's real 7:1 ratio is stored).
// EggGroups is comma-joined, the same convention Types already uses for "/".
type Species struct {
	Dex          int    `json:"dex"`
	Name         string `json:"name"`
	Genus        string `json:"genus,omitempty"`
	Generation   string `json:"generation,omitempty"`
	Types        string `json:"types,omitempty"`
	HeightDM     int    `json:"height_dm,omitempty"`
	WeightHG     int    `json:"weight_hg,omitempty"`
	FlavorText   string `json:"flavor_text,omitempty"`
	IsLegendary  bool   `json:"is_legendary,omitempty"`
	IsMythical   bool   `json:"is_mythical,omitempty"`
	EggGroups    string `json:"egg_groups,omitempty"`
	GenderRate   int    `json:"gender_rate"`
	HatchCounter int    `json:"hatch_counter,omitempty"`
	StatHP       int    `json:"stat_hp,omitempty"`
	StatAtk      int    `json:"stat_atk,omitempty"`
	StatDef      int    `json:"stat_def,omitempty"`
	StatSpA      int    `json:"stat_spa,omitempty"`
	StatSpD      int    `json:"stat_spd,omitempty"`
	StatSpe      int    `json:"stat_spe,omitempty"`
	// Abilities is "/"-joined, the same convention Types uses; HiddenAbility is
	// separate rather than a third slot in that list because it is chosen
	// differently in-game (a fixed chance, not one of the regular two) and the
	// editor needs to tell them apart, not just list all three together.
	Abilities     string `json:"abilities,omitempty"`
	HiddenAbility string `json:"hidden_ability,omitempty"`
}

// Evolution is one edge of an evolution chain. Condition is a rendered sentence
// ("Use a Sun Stone"), built at ingest rather than re-derived in the UI from
// PokeAPI's dozen nullable detail fields.
type Evolution struct {
	FromDex   int    `json:"from_dex"`
	ToDex     int    `json:"to_dex"`
	Trigger   string `json:"trigger,omitempty"`
	MinLevel  int    `json:"min_level,omitempty"`
	Item      string `json:"item,omitempty"`
	Condition string `json:"condition"`
}

// RegionalNumber is a species' number in one game's own Pokédex, which is what
// lets a HeartGold view read "Johto #086" rather than National #182.
type RegionalNumber struct {
	Dex     int    `json:"dex"`
	Pokedex string `json:"pokedex"`
	Entry   int    `json:"entry_number"`
}

// UpsertSpecies stores or refreshes one species. The ingest is resumable and may
// re-run, so this updates in place rather than failing on a repeat.
func (s *Store) UpsertSpecies(ctx context.Context, sp Species) error {
	_, err := s.execCtx(ctx, `INSERT INTO pokedex_species(dex,name,genus,generation,types,height_dm,weight_hg,flavor_text,is_legendary,is_mythical,egg_groups,gender_rate,hatch_counter,stat_hp,stat_atk,stat_def,stat_spa,stat_spd,stat_spe,abilities,hidden_ability,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(dex) DO UPDATE SET name=excluded.name,genus=excluded.genus,generation=excluded.generation,types=excluded.types,
height_dm=excluded.height_dm,weight_hg=excluded.weight_hg,flavor_text=excluded.flavor_text,
is_legendary=excluded.is_legendary,is_mythical=excluded.is_mythical,egg_groups=excluded.egg_groups,
gender_rate=excluded.gender_rate,hatch_counter=excluded.hatch_counter,
stat_hp=excluded.stat_hp,stat_atk=excluded.stat_atk,stat_def=excluded.stat_def,
stat_spa=excluded.stat_spa,stat_spd=excluded.stat_spd,stat_spe=excluded.stat_spe,
abilities=excluded.abilities,hidden_ability=excluded.hidden_ability,updated_at=excluded.updated_at`,
		sp.Dex, sp.Name, sp.Genus, sp.Generation, sp.Types, sp.HeightDM, sp.WeightHG,
		sp.FlavorText, sp.IsLegendary, sp.IsMythical, sp.EggGroups, sp.GenderRate, sp.HatchCounter,
		sp.StatHP, sp.StatAtk, sp.StatDef, sp.StatSpA, sp.StatSpD, sp.StatSpe,
		sp.Abilities, sp.HiddenAbility, time.Now().Unix())
	return err
}

func (s *Store) UpsertEvolution(ctx context.Context, e Evolution) error {
	_, err := s.execCtx(ctx, `INSERT INTO pokedex_evolution(from_dex,to_dex,trigger,min_level,item,condition)
VALUES(?,?,?,?,?,?)
ON CONFLICT(from_dex,to_dex) DO UPDATE SET trigger=excluded.trigger,min_level=excluded.min_level,item=excluded.item,condition=excluded.condition`,
		e.FromDex, e.ToDex, e.Trigger, e.MinLevel, e.Item, e.Condition)
	return err
}

func (s *Store) UpsertRegionalNumber(ctx context.Context, r RegionalNumber) error {
	_, err := s.execCtx(ctx, `INSERT INTO pokedex_regional(dex,pokedex,entry_number) VALUES(?,?,?)
ON CONFLICT(dex,pokedex) DO UPDATE SET entry_number=excluded.entry_number`,
		r.Dex, r.Pokedex, r.Entry)
	return err
}

// SpeciesDetail reports whether the species has been ingested at all, so the UI
// can tell "not fetched yet" from "fetched and blank".
func (s *Store) SpeciesDetail(ctx context.Context, dex int) (Species, bool, error) {
	var sp Species
	err := s.queryRowCtx(ctx, `SELECT dex,name,genus,generation,types,height_dm,weight_hg,flavor_text,is_legendary,is_mythical,egg_groups,gender_rate,hatch_counter,stat_hp,stat_atk,stat_def,stat_spa,stat_spd,stat_spe,abilities,hidden_ability FROM pokedex_species WHERE dex=?`, dex).
		Scan(&sp.Dex, &sp.Name, &sp.Genus, &sp.Generation, &sp.Types, &sp.HeightDM, &sp.WeightHG, &sp.FlavorText, &sp.IsLegendary, &sp.IsMythical, &sp.EggGroups, &sp.GenderRate, &sp.HatchCounter, &sp.StatHP, &sp.StatAtk, &sp.StatDef, &sp.StatSpA, &sp.StatSpD, &sp.StatSpe, &sp.Abilities, &sp.HiddenAbility)
	if errors.Is(err, sql.ErrNoRows) {
		return Species{}, false, nil
	}
	if err != nil {
		return Species{}, false, err
	}
	return sp, true, nil
}

func (s *Store) EvolutionsFor(ctx context.Context, dex int) ([]Evolution, error) {
	rows, err := s.queryCtx(ctx, `SELECT from_dex,to_dex,trigger,min_level,item,condition FROM pokedex_evolution WHERE from_dex=? OR to_dex=? ORDER BY from_dex,to_dex`, dex, dex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evolution
	for rows.Next() {
		var e Evolution
		if err := rows.Scan(&e.FromDex, &e.ToDex, &e.Trigger, &e.MinLevel, &e.Item, &e.Condition); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) RegionalNumbersFor(ctx context.Context, dex int) ([]RegionalNumber, error) {
	rows, err := s.queryCtx(ctx, `SELECT dex,pokedex,entry_number FROM pokedex_regional WHERE dex=? ORDER BY pokedex`, dex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RegionalNumber
	for rows.Next() {
		var r RegionalNumber
		if err := rows.Scan(&r.Dex, &r.Pokedex, &r.Entry); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RegionalDexEntries lists every species in the named regional Pokédexes, in
// entry order. Several names cover the games whose dex is split across areas
// (Kalos' three, Galar plus its DLC): the slices are concatenated in the order
// given, which is the order the games present them. A species appearing in more
// than one keeps its first appearance so the grid never shows it twice.
func (s *Store) RegionalDexEntries(ctx context.Context, pokedexes []string) ([]RegionalNumber, error) {
	var out []RegionalNumber
	seen := map[int]bool{}
	for _, name := range pokedexes {
		rows, err := s.queryCtx(ctx, `SELECT dex,pokedex,entry_number FROM pokedex_regional WHERE pokedex=? ORDER BY entry_number`, name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r RegionalNumber
			if err := rows.Scan(&r.Dex, &r.Pokedex, &r.Entry); err != nil {
				rows.Close()
				return nil, err
			}
			if seen[r.Dex] {
				continue
			}
			seen[r.Dex] = true
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// Encounter is one way to find a species in the wild in one game version:
// where, how, at what levels, how likely, and under which conditions ("swarm",
// "time-morning"). Conditions is a comma-joined list, it is display text, and
// part of the key only so distinct condition sets coexist.
type Encounter struct {
	Dex        int    `json:"dex"`
	Version    string `json:"version"`
	Location   string `json:"location"`
	Method     string `json:"method,omitempty"`
	Conditions string `json:"conditions,omitempty"`
	MinLevel   int    `json:"min_level,omitempty"`
	MaxLevel   int    `json:"max_level,omitempty"`
	Chance     int    `json:"chance,omitempty"`
}

func (s *Store) UpsertEncounter(ctx context.Context, e Encounter) error {
	_, err := s.execCtx(ctx, `INSERT INTO pokedex_encounters(dex,version,location,method,conditions,min_level,max_level,chance)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(dex,version,location,method,conditions) DO UPDATE SET min_level=excluded.min_level,max_level=excluded.max_level,chance=excluded.chance`,
		e.Dex, e.Version, e.Location, e.Method, e.Conditions, e.MinLevel, e.MaxLevel, e.Chance)
	return err
}

// EncountersFor lists a species' wild encounters, optionally limited to the
// named versions. Nil versions means every version.
func (s *Store) EncountersFor(ctx context.Context, dex int, versions []string) ([]Encounter, error) {
	query := `SELECT dex,version,location,method,conditions,min_level,max_level,chance FROM pokedex_encounters WHERE dex=?`
	args := []any{dex}
	if len(versions) > 0 {
		query += ` AND version IN (?` + strings.Repeat(",?", len(versions)-1) + `)`
		for _, v := range versions {
			args = append(args, v)
		}
	}
	query += ` ORDER BY version,location,method`
	rows, err := s.queryCtx(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Encounter
	for rows.Next() {
		var e Encounter
		if err := rows.Scan(&e.Dex, &e.Version, &e.Location, &e.Method, &e.Conditions, &e.MinLevel, &e.MaxLevel, &e.Chance); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EncountersFetched reports whether a species' encounters were already pulled.
// Presence of rows cannot stand in for this: many species legitimately have no
// wild encounters, and re-fetching all of those on every run would repeat the
// whole pass forever.
func (s *Store) EncountersFetched(ctx context.Context, dex int) (bool, error) {
	var n int
	if err := s.queryRowCtx(ctx, `SELECT COUNT(*) FROM pokedex_encounters_done WHERE dex=?`, dex).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) MarkEncountersFetched(ctx context.Context, dex int) error {
	_, err := s.execCtx(ctx, `INSERT INTO pokedex_encounters_done(dex,fetched_at) VALUES(?,?) ON CONFLICT(dex) DO UPDATE SET fetched_at=excluded.fetched_at`,
		dex, time.Now().Unix())
	return err
}

// SpeciesCount is how the ingest job knows whether it has work to do and how the
// API reports that enrichment is unavailable.
func (s *Store) SpeciesCount(ctx context.Context) (int, error) {
	var n int
	err := s.queryRowCtx(ctx, `SELECT COUNT(*) FROM pokedex_species`).Scan(&n)
	return n, err
}

// Move is one attack's reference data, shared across every species that can
// learn it. It is a global table, not per-species: ~900 rows total against
// 1,025 species each knowing a subset, so it is fetched once per move the
// first time any species' learnset references it.
type Move struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Category string `json:"category,omitempty"`
	Power    int    `json:"power,omitempty"`
	Accuracy int    `json:"accuracy,omitempty"`
}

func (s *Store) UpsertMove(ctx context.Context, m Move) error {
	_, err := s.execCtx(ctx, `INSERT INTO pokedex_moves(name,type,category,power,accuracy) VALUES(?,?,?,?,?)
ON CONFLICT(name) DO UPDATE SET type=excluded.type,category=excluded.category,power=excluded.power,accuracy=excluded.accuracy`,
		m.Name, m.Type, m.Category, m.Power, m.Accuracy)
	return err
}

// HasMove reports whether a move's reference data is already stored, so the
// ingest fetches each one at most once regardless of how many species learn it.
func (s *Store) HasMove(ctx context.Context, name string) (bool, error) {
	var n int
	if err := s.queryRowCtx(ctx, `SELECT COUNT(*) FROM pokedex_moves WHERE name=?`, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// LearnsetEntry is one move a species can learn by level-up or machine (TM/HM),
// joined with that move's own reference data. Generations is the sorted,
// comma-joined list of generation numbers ("1,3,6,9") the move is learnable by
// that method in, a species can pick up a move by level-up in one generation
// and lose it to a redesigned learnset in a later one, so this is a set, not a
// single value. Egg and tutor moves are deliberately not tracked: the request
// this serves asked for level-up and TM only.
type LearnsetEntry struct {
	Move        string `json:"move"`
	Type        string `json:"type,omitempty"`
	Category    string `json:"category,omitempty"`
	Power       int    `json:"power,omitempty"`
	Accuracy    int    `json:"accuracy,omitempty"`
	Method      string `json:"method"`
	Level       int    `json:"level,omitempty"`
	Generations string `json:"generations"`
}

func (s *Store) UpsertLearnsetEntry(ctx context.Context, dex int, e LearnsetEntry) error {
	_, err := s.execCtx(ctx, `INSERT INTO pokedex_learnset(dex,move,method,level,generations) VALUES(?,?,?,?,?)
ON CONFLICT(dex,move,method) DO UPDATE SET level=excluded.level,generations=excluded.generations`,
		dex, e.Move, e.Method, e.Level, e.Generations)
	return err
}

// LearnsetFor lists a species' level-up and TM moves, each carrying the move's
// own type/category/power/accuracy alongside the learn method and generations.
func (s *Store) LearnsetFor(ctx context.Context, dex int) ([]LearnsetEntry, error) {
	rows, err := s.queryCtx(ctx, `SELECT l.move,m.type,m.category,m.power,m.accuracy,l.method,l.level,l.generations
FROM pokedex_learnset l JOIN pokedex_moves m ON m.name=l.move
WHERE l.dex=? ORDER BY l.method,l.level,l.move`, dex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LearnsetEntry
	for rows.Next() {
		var e LearnsetEntry
		if err := rows.Scan(&e.Move, &e.Type, &e.Category, &e.Power, &e.Accuracy, &e.Method, &e.Level, &e.Generations); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LearnsetFetched/MarkLearnsetFetched follow the same "no rows is a real
// answer" pattern as EncountersFetched: a species with a tiny or move-tutor-only
// kit legitimately has zero level-up/machine rows.
func (s *Store) LearnsetFetched(ctx context.Context, dex int) (bool, error) {
	var n int
	if err := s.queryRowCtx(ctx, `SELECT COUNT(*) FROM pokedex_learnset_done WHERE dex=?`, dex).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// DexStatus reports, in one round trip, the three "is this already done"
// checks the ingest makes for every dex on every run: whether encounters were
// pulled, whether the learnset was pulled, and whether the species row itself
// is complete (the same HatchCounter/StatHP/Abilities gate SpeciesDetail's
// caller in the ingest applies). Collapsing three queries into one matters
// because the ingest walks all ~1,025 species on every run, including the
// hourly retry over an already-complete Pokédex where every dex is a pure
// skip - that path is otherwise all round-trip, no work.
func (s *Store) DexStatus(ctx context.Context, dex int) (encountersDone, learnsetDone, speciesDone bool, err error) {
	err = s.queryRowCtx(ctx, `SELECT
		EXISTS(SELECT 1 FROM pokedex_encounters_done WHERE dex=?),
		EXISTS(SELECT 1 FROM pokedex_learnset_done WHERE dex=?),
		EXISTS(SELECT 1 FROM pokedex_species WHERE dex=? AND hatch_counter>0 AND stat_hp>0 AND abilities<>'')`,
		dex, dex, dex).Scan(&encountersDone, &learnsetDone, &speciesDone)
	return
}

func (s *Store) MarkLearnsetFetched(ctx context.Context, dex int) error {
	_, err := s.execCtx(ctx, `INSERT INTO pokedex_learnset_done(dex,fetched_at) VALUES(?,?) ON CONFLICT(dex) DO UPDATE SET fetched_at=excluded.fetched_at`,
		dex, time.Now().Unix())
	return err
}

// LookupFilter narrows a Pokemon Lookup query (a pure reference-data search,
// independent of any trainer's dex/ownership state) to species matching every
// given, non-empty condition. Empty fields are not applied.
type LookupFilter struct {
	Type         string // a type name, e.g. "fire"
	MoveName     string // a move name, e.g. "surf"
	MoveMethod   string // "level-up" or "machine", narrows MoveName further
	InVersion    string // a PokeAPI version name, e.g. "white-2"
	NotInVersion string // a PokeAPI version name the species is NOT encountered in
}

// normalizeMoveName strips the separators a move name might be typed or
// stored with, so "False Swipe", "false_swipe", and "false-swipe" all match
// the same learnset row.
func normalizeMoveName(name string) string {
	r := strings.NewReplacer("-", "", "_", "", " ", "")
	return r.Replace(strings.ToLower(name))
}

// LookupSpecies runs a Pokemon Lookup query. Types is "/"-delimited per
// species (see Species doc comment), so the type match pads both sides with
// slashes to match a whole type name rather than a substring.
func (s *Store) LookupSpecies(ctx context.Context, f LookupFilter) ([]Species, error) {
	query := `SELECT sp.dex,sp.name,sp.genus,sp.generation,sp.types,sp.height_dm,sp.weight_hg,sp.flavor_text,sp.is_legendary,sp.is_mythical,sp.egg_groups,sp.gender_rate,sp.hatch_counter,sp.stat_hp,sp.stat_atk,sp.stat_def,sp.stat_spa,sp.stat_spd,sp.stat_spe,sp.abilities,sp.hidden_ability
FROM pokedex_species sp WHERE 1=1`
	var args []any
	if f.Type != "" {
		query += ` AND ('/' || sp.types || '/') LIKE '%/' || ? || '/%'`
		args = append(args, strings.ToLower(f.Type))
	}
	if f.MoveName != "" {
		// Move names are stored PokeAPI-style ("false-swipe"), but a typical
		// user types "false swipe" or "falseswipe", so both sides are
		// stripped of separators before comparing rather than requiring an
		// exact hyphenated match.
		sub := ` AND EXISTS(SELECT 1 FROM pokedex_learnset l WHERE l.dex=sp.dex AND REPLACE(REPLACE(REPLACE(l.move,'-',''),'_',''),' ','')=?`
		args = append(args, normalizeMoveName(f.MoveName))
		if f.MoveMethod != "" {
			sub += ` AND l.method=?`
			args = append(args, f.MoveMethod)
		}
		query += sub + `)`
	}
	if f.InVersion != "" {
		query += ` AND EXISTS(SELECT 1 FROM pokedex_encounters e WHERE e.dex=sp.dex AND e.version=?)`
		args = append(args, f.InVersion)
	}
	if f.NotInVersion != "" {
		query += ` AND NOT EXISTS(SELECT 1 FROM pokedex_encounters e WHERE e.dex=sp.dex AND e.version=?)`
		args = append(args, f.NotInVersion)
	}
	query += ` ORDER BY sp.dex`
	rows, err := s.queryCtx(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Species
	for rows.Next() {
		var sp Species
		if err := rows.Scan(&sp.Dex, &sp.Name, &sp.Genus, &sp.Generation, &sp.Types, &sp.HeightDM, &sp.WeightHG, &sp.FlavorText, &sp.IsLegendary, &sp.IsMythical, &sp.EggGroups, &sp.GenderRate, &sp.HatchCounter, &sp.StatHP, &sp.StatAtk, &sp.StatDef, &sp.StatSpA, &sp.StatSpD, &sp.StatSpe, &sp.Abilities, &sp.HiddenAbility); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}
