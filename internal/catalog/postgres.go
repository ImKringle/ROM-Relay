package catalog

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// isPostgresDSN reports whether a DSN selects the Postgres backend. Anything else
// is treated as a SQLite file path.
func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// openPostgres connects to a Postgres server via the pgx database/sql driver.
// Unlike SQLite, Postgres handles concurrent access, so a real connection pool
// is used instead of a single serialized connection.
func openPostgres(dsn string) (*sql.DB, dialect, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, dialect{}, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, dialect{}, fmt.Errorf("connect postgres: %w", err)
	}
	return db, dialect{postgres: true}, nil
}

// postgresSchema is the full schema for a Postgres catalog. Unlike the SQLite
// path, which layers additive ALTERs for databases created by older builds, a
// Postgres catalog is always created complete here: SQLite's additive columns
// are inlined, and every column that stores a Unix timestamp, file size, or
// UnixNano modtime uses BIGINT (SQLite's INTEGER is 64-bit, but Postgres INTEGER
// is only 32-bit and would overflow). Executed one statement at a time because
// the pgx database/sql driver uses the extended protocol, which forbids multiple
// statements per Exec.
var postgresSchema = []string{
	`CREATE TABLE IF NOT EXISTS games(
id TEXT PRIMARY KEY, platform TEXT NOT NULL, title TEXT NOT NULL,
developer TEXT NOT NULL DEFAULT '', publisher TEXT NOT NULL DEFAULT '',
description TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '',
rom_patch BOOLEAN NOT NULL DEFAULT FALSE,
created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
patch_file_id TEXT NOT NULL DEFAULT '',
release_date TEXT NOT NULL DEFAULT '', genre TEXT NOT NULL DEFAULT '',
players TEXT NOT NULL DEFAULT '', rating TEXT NOT NULL DEFAULT '',
artwork_url TEXT NOT NULL DEFAULT '', screenshot_url TEXT NOT NULL DEFAULT '',
metadata_refreshed_at BIGINT NOT NULL DEFAULT 0,
metadata_source TEXT NOT NULL DEFAULT 'local')`,
	`CREATE TABLE IF NOT EXISTS artifacts(
id TEXT PRIMARY KEY,
game_id TEXT NOT NULL REFERENCES games(id) ON DELETE CASCADE,
platform TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL UNIQUE,
extension TEXT NOT NULL, size BIGINT NOT NULL, mod_time BIGINT NOT NULL,
sha256 TEXT NOT NULL, sha1 TEXT NOT NULL, md5 TEXT NOT NULL, crc32 TEXT NOT NULL,
header_json TEXT NOT NULL DEFAULT '{}', patched BOOLEAN NOT NULL DEFAULT FALSE,
expires_at BIGINT, retain BOOLEAN NOT NULL DEFAULT FALSE,
external BOOLEAN NOT NULL DEFAULT FALSE)`,
	`CREATE INDEX IF NOT EXISTS artifacts_game ON artifacts(game_id)`,
	`CREATE TABLE IF NOT EXISTS patch_files(
id TEXT PRIMARY KEY, path TEXT NOT NULL UNIQUE, size BIGINT NOT NULL,
sha256 TEXT NOT NULL, extension TEXT NOT NULL, mod_time BIGINT NOT NULL,
platform TEXT NOT NULL DEFAULT '', slug TEXT NOT NULL DEFAULT '',
title TEXT NOT NULL DEFAULT '', output_filename TEXT NOT NULL DEFAULT '',
base_sha256 TEXT NOT NULL DEFAULT '', base_crc32 TEXT NOT NULL DEFAULT '',
validation_type TEXT NOT NULL DEFAULT '', validation_value TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS patch_jobs(
id TEXT PRIMARY KEY, status TEXT NOT NULL, artifact_id TEXT NOT NULL,
output_name TEXT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
error TEXT NOT NULL DEFAULT '', retain BOOLEAN NOT NULL DEFAULT FALSE,
expires_at BIGINT, format TEXT NOT NULL DEFAULT '',
result_artifact_id TEXT NOT NULL DEFAULT '', unverified_source BOOLEAN NOT NULL DEFAULT FALSE,
checksum_expected TEXT NOT NULL DEFAULT '', checksum_actual TEXT NOT NULL DEFAULT '',
patch_file_id TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS randomizer_jobs(
id TEXT PRIMARY KEY, status TEXT NOT NULL, artifact_id TEXT NOT NULL,
output_name TEXT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
error TEXT NOT NULL DEFAULT '', retain BOOLEAN NOT NULL DEFAULT FALSE,
expires_at BIGINT, result_artifact_id TEXT NOT NULL DEFAULT '',
settings_json TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS scheduler_state(
name TEXT PRIMARY KEY, last_run BIGINT NOT NULL, status TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS libretro_entries(
platform TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
region TEXT NOT NULL DEFAULT '', developer TEXT NOT NULL DEFAULT '',
publisher TEXT NOT NULL DEFAULT '', genre TEXT NOT NULL DEFAULT '',
release_date TEXT NOT NULL DEFAULT '', players TEXT NOT NULL DEFAULT '',
rating TEXT NOT NULL DEFAULT '', crc32 TEXT NOT NULL DEFAULT '',
md5 TEXT NOT NULL DEFAULT '', sha1 TEXT NOT NULL DEFAULT '',
PRIMARY KEY(platform,name,sha1,crc32))`,
	`CREATE INDEX IF NOT EXISTS libretro_sha1 ON libretro_entries(platform,sha1)`,
	`CREATE INDEX IF NOT EXISTS libretro_md5 ON libretro_entries(platform,md5)`,
	`CREATE INDEX IF NOT EXISTS libretro_crc32 ON libretro_entries(platform,crc32)`,
	`CREATE TABLE IF NOT EXISTS users(
id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL DEFAULT '',
role TEXT NOT NULL DEFAULT 'user', created_at BIGINT NOT NULL,
disabled BOOLEAN NOT NULL DEFAULT FALSE, must_change_password BOOLEAN NOT NULL DEFAULT FALSE)`,
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT FALSE`,
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE`,
	`CREATE TABLE IF NOT EXISTS pokemon_instances(
id TEXT PRIMARY KEY,
owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
fingerprint TEXT NOT NULL, species INTEGER NOT NULL, species_name TEXT NOT NULL DEFAULT '',
nickname TEXT NOT NULL DEFAULT '', level INTEGER NOT NULL DEFAULT 0,
nature TEXT NOT NULL DEFAULT '', ability TEXT NOT NULL DEFAULT '', gender TEXT NOT NULL DEFAULT '',
shiny BOOLEAN NOT NULL DEFAULT FALSE, is_egg BOOLEAN NOT NULL DEFAULT FALSE,
form INTEGER NOT NULL DEFAULT 0, generation INTEGER NOT NULL DEFAULT 0,
ivs_json TEXT NOT NULL DEFAULT '{}', evs_json TEXT NOT NULL DEFAULT '{}', moves_json TEXT NOT NULL DEFAULT '[]',
held_item TEXT NOT NULL DEFAULT '', ball INTEGER NOT NULL DEFAULT 0, pid BIGINT NOT NULL DEFAULT 0,
origin_game TEXT NOT NULL DEFAULT '', origin_ot TEXT NOT NULL DEFAULT '', source_save TEXT NOT NULL DEFAULT '',
origin_save_id TEXT NOT NULL DEFAULT '', pokemon_binary BYTEA, created_at BIGINT NOT NULL)`,
	`ALTER TABLE pokemon_instances ADD COLUMN IF NOT EXISTS origin_save_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE pokemon_instances ADD COLUMN IF NOT EXISTS home_deposited_at BIGINT`,
	`ALTER TABLE pokemon_instances ADD COLUMN IF NOT EXISTS home_last_withdrawn_at BIGINT`,
	// origin_slot records where this Pokémon sat in its origin save at ingest
	// time ("party:2", "box:3:12"), the Editor's Party/Box grouping needs it.
	`ALTER TABLE pokemon_instances ADD COLUMN IF NOT EXISTS origin_slot TEXT NOT NULL DEFAULT ''`,
	// Storage boxes were withdrawn in favour of the user Pokédex; their columns
	// go with them. Placements were derived from insertion order, so no user
	// data is lost.
	`ALTER TABLE pokemon_instances DROP COLUMN IF EXISTS box`,
	`ALTER TABLE pokemon_instances DROP COLUMN IF EXISTS box_slot`,
	`CREATE UNIQUE INDEX IF NOT EXISTS pokemon_owner_fingerprint ON pokemon_instances(owner_id,fingerprint)`,
	`CREATE INDEX IF NOT EXISTS pokemon_owner_species ON pokemon_instances(owner_id,species)`,
	`CREATE TABLE IF NOT EXISTS sessions(
id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id)`,
	`CREATE TABLE IF NOT EXISTS game_dex(
owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, game TEXT NOT NULL,
trainer TEXT NOT NULL DEFAULT '', species INTEGER NOT NULL,
seen BOOLEAN NOT NULL DEFAULT FALSE, caught BOOLEAN NOT NULL DEFAULT FALSE,
PRIMARY KEY(owner_id,game,trainer,species))`,
	`ALTER TABLE game_dex ADD COLUMN IF NOT EXISTS trainer TEXT NOT NULL DEFAULT ''`,
	// Widen the primary key on databases created before trainers existed. A
	// fresh database already has the 4-column key from the CREATE TABLE above,
	// so the conkey length check makes this a no-op there and keeps the whole
	// schema slice idempotent (it re-runs on every start).
	`DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'game_dex_pkey' AND array_length(conkey, 1) = 3) THEN
    ALTER TABLE game_dex DROP CONSTRAINT game_dex_pkey;
    ALTER TABLE game_dex ADD CONSTRAINT game_dex_pkey PRIMARY KEY (owner_id, game, trainer, species);
  END IF;
END $$`,
	`CREATE TABLE IF NOT EXISTS game_dex_meta(
owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, game TEXT NOT NULL,
max_species INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(owner_id,game))`,
	`CREATE TABLE IF NOT EXISTS pokedex_species(
dex INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '', genus TEXT NOT NULL DEFAULT '',
generation TEXT NOT NULL DEFAULT '', types TEXT NOT NULL DEFAULT '',
height_dm INTEGER NOT NULL DEFAULT 0, weight_hg INTEGER NOT NULL DEFAULT 0,
flavor_text TEXT NOT NULL DEFAULT '', is_legendary BOOLEAN NOT NULL DEFAULT FALSE,
is_mythical BOOLEAN NOT NULL DEFAULT FALSE, updated_at BIGINT NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS pokedex_evolution(
from_dex INTEGER NOT NULL, to_dex INTEGER NOT NULL, trigger TEXT NOT NULL DEFAULT '',
min_level INTEGER NOT NULL DEFAULT 0, item TEXT NOT NULL DEFAULT '',
condition TEXT NOT NULL DEFAULT '', PRIMARY KEY(from_dex,to_dex))`,
	`CREATE TABLE IF NOT EXISTS pokedex_regional(
dex INTEGER NOT NULL, pokedex TEXT NOT NULL, entry_number INTEGER NOT NULL DEFAULT 0,
PRIMARY KEY(dex,pokedex))`,
	`CREATE TABLE IF NOT EXISTS pokedex_encounters(
dex INTEGER NOT NULL, version TEXT NOT NULL, location TEXT NOT NULL,
method TEXT NOT NULL DEFAULT '', conditions TEXT NOT NULL DEFAULT '',
min_level INTEGER NOT NULL DEFAULT 0, max_level INTEGER NOT NULL DEFAULT 0,
chance INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(dex,version,location,method,conditions))`,
	`CREATE TABLE IF NOT EXISTS pokedex_encounters_done(
dex INTEGER PRIMARY KEY, fetched_at BIGINT NOT NULL DEFAULT 0)`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS egg_groups TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS gender_rate INTEGER NOT NULL DEFAULT -1`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS hatch_counter INTEGER NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS pokedex_moves(
name TEXT PRIMARY KEY, type TEXT NOT NULL DEFAULT '', category TEXT NOT NULL DEFAULT '',
power INTEGER NOT NULL DEFAULT 0, accuracy INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS pokedex_learnset(
dex INTEGER NOT NULL, move TEXT NOT NULL, method TEXT NOT NULL,
level INTEGER NOT NULL DEFAULT 0, generations TEXT NOT NULL DEFAULT '',
PRIMARY KEY(dex,move,method))`,
	`CREATE TABLE IF NOT EXISTS pokedex_learnset_done(
dex INTEGER PRIMARY KEY, fetched_at BIGINT NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS trainer_profiles(
owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, game TEXT NOT NULL,
tid INTEGER NOT NULL, sid INTEGER NOT NULL, ot TEXT NOT NULL DEFAULT '',
gender INTEGER NOT NULL DEFAULT 0, language INTEGER NOT NULL DEFAULT 0,
played_minutes INTEGER NOT NULL DEFAULT 0, money BIGINT NOT NULL DEFAULT 0,
money_max BIGINT NOT NULL DEFAULT 0, progress_kind TEXT NOT NULL DEFAULT '',
progress_count INTEGER NOT NULL DEFAULT 0, progress_max INTEGER NOT NULL DEFAULT 0,
hof_clears INTEGER, notes TEXT NOT NULL DEFAULT '{}', trusted BOOLEAN NOT NULL DEFAULT TRUE,
source_save_id TEXT NOT NULL DEFAULT '', updated_at BIGINT NOT NULL DEFAULT 0,
PRIMARY KEY(owner_id,game,tid,sid))`,
	`CREATE TABLE IF NOT EXISTS trainer_hof(
owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, game TEXT NOT NULL,
tid INTEGER NOT NULL, sid INTEGER NOT NULL, entry_index INTEGER NOT NULL, slot INTEGER NOT NULL,
species INTEGER NOT NULL DEFAULT 0, level INTEGER NOT NULL DEFAULT 0, nickname TEXT NOT NULL DEFAULT '',
PRIMARY KEY(owner_id,game,tid,sid,entry_index,slot))`,
	`CREATE TABLE IF NOT EXISTS saves(
id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
game_id TEXT NOT NULL DEFAULT '', game_label TEXT NOT NULL DEFAULT '', file_name TEXT NOT NULL DEFAULT '',
size_bytes BIGINT NOT NULL DEFAULT 0, sha256 TEXT NOT NULL, source TEXT NOT NULL DEFAULT 'upload',
uploaded_at BIGINT NOT NULL, parsed BOOLEAN NOT NULL DEFAULT FALSE, parsed_game TEXT NOT NULL DEFAULT '',
parsed_trainer TEXT NOT NULL DEFAULT '', parsed_playtime TEXT NOT NULL DEFAULT '',
pokemon_count INTEGER NOT NULL DEFAULT 0)`,
	`CREATE INDEX IF NOT EXISTS saves_owner_game ON saves(owner_id,game_id,uploaded_at DESC)`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS stat_hp INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS stat_atk INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS stat_def INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS stat_spa INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS stat_spd INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS stat_spe INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS abilities TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE pokedex_species ADD COLUMN IF NOT EXISTS hidden_ability TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS api_keys(
id TEXT PRIMARY KEY, name TEXT NOT NULL, token TEXT NOT NULL UNIQUE,
created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
created_at BIGINT NOT NULL, last_used_at BIGINT, revoked_at BIGINT)`,
	`CREATE TABLE IF NOT EXISTS webdav_keys(
id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
label TEXT NOT NULL DEFAULT '', token TEXT NOT NULL UNIQUE,
created_at BIGINT NOT NULL, last_used_at BIGINT, revoked_at BIGINT)`,
	`CREATE INDEX IF NOT EXISTS webdav_keys_user ON webdav_keys(user_id)`,
}

// migratePostgres creates the complete Postgres schema. It is idempotent
// (every statement is IF NOT EXISTS) so it runs safely on every startup.
func (s *Store) migratePostgres() error {
	for _, statement := range postgresSchema {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("postgres migrate: %w", err)
		}
	}
	return nil
}
