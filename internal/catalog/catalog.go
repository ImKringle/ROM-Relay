// Package catalog maintains ROM Relay's SQLite-backed filesystem catalog.
package catalog

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Game struct {
	ID             string     `json:"id"`
	Platform       string     `json:"platform"`
	Title          string     `json:"title"`
	Developer      string     `json:"developer,omitempty"`
	Publisher      string     `json:"publisher,omitempty"`
	Description    string     `json:"description,omitempty"`
	Region         string     `json:"region,omitempty"`
	ReleaseDate    string     `json:"release_date,omitempty"`
	Genre          string     `json:"genre,omitempty"`
	Players        string     `json:"players,omitempty"`
	Rating         string     `json:"rating,omitempty"`
	ArtworkURL     string     `json:"artwork_url,omitempty"`
	ScreenshotURL  string     `json:"screenshot_url,omitempty"`
	MetadataSource string     `json:"metadata_source,omitempty"`
	PatchFileID    string     `json:"patch_file_id,omitempty"`
	ROMPatch       bool       `json:"rom_patch"`
	Encryption     string     `json:"encryption,omitempty"`
	Artifacts      []Artifact `json:"artifacts,omitempty"`
}
type Artifact struct {
	ID         string         `json:"id"`
	GameID     string         `json:"game_id"`
	Platform   string         `json:"platform"`
	Name       string         `json:"name"`
	Path       string         `json:"-"`
	Extension  string         `json:"extension"`
	Size       int64          `json:"size"`
	SHA256     string         `json:"sha256"`
	SHA1       string         `json:"sha1"`
	MD5        string         `json:"md5"`
	CRC32      string         `json:"crc32"`
	Header     map[string]any `json:"header,omitempty"`
	ModTime    int64          `json:"-"`
	Patched    bool           `json:"patched"`
	External   bool           `json:"external"`
	Encryption string         `json:"encryption,omitempty"`
	Retain     bool           `json:"retain"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
}
type PatchJobRecord struct {
	ID, Status, ArtifactID, OutputName, Format, Error  string
	ResultArtifactID, ChecksumExpected, ChecksumActual string
	PatchFileID                                        string
	Retain, UnverifiedSource                           bool
	CreatedAt, UpdatedAt                               time.Time
	ExpiresAt                                          *time.Time
}
type PatchRecipe struct {
	ID              string `json:"id"`
	Platform        string `json:"platform"`
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	Path            string `json:"-"`
	SHA256          string `json:"sha256"`
	Extension       string `json:"extension"`
	OutputFilename  string `json:"output_filename"`
	BaseSHA256      string `json:"base_sha256,omitempty"`
	BaseCRC32       string `json:"base_crc32,omitempty"`
	ValidationType  string `json:"validation_type,omitempty"`
	ValidationValue string `json:"validation_value,omitempty"`
	Size            int64  `json:"size"`
	ModTime         int64  `json:"-"`
}
type MetadataRecord struct {
	Platform, Name, Description, Region, Developer, Publisher string
	Genre, ReleaseDate, Players, Rating, CRC32, MD5, SHA1     string
}
type Store struct {
	db           *sql.DB
	dialect      dialect
	dbPath       string
	root         string
	artworkDir   string
	extraRoots   []artifactRoot
	now          func() time.Time
	scanProgress func(files int, hashedBytes int64)
	activeMu     sync.Mutex
	active       map[string]int
}
type artifactRoot struct{ path, kind string }

// Open connects the catalog to its backing database. A SQLite file path uses the
// embedded single-writer SQLite engine; the Postgres backend is selected by a
// postgres:// DSN (see openDatabase).
func Open(path, root string) (*Store, error) {
	db, d, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, dialect: d, dbPath: path, root: root, now: time.Now, active: map[string]int{}}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// openDatabase opens the driver for the given DSN and applies backend-specific
// connection setup. SQLite uses a single connection with WAL and foreign keys;
// this is where the Postgres branch attaches.
func openDatabase(dsn string) (*sql.DB, dialect, error) {
	if isPostgresDSN(dsn) {
		return openPostgres(dsn)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, dialect{}, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, dialect{}, err
		}
	}
	return db, dialect{postgres: false}, nil
}

type PlatformRuntimeStats struct {
	Platform                     string
	Games, Base, Patched, Files  int
	EncryptedCIA, UnencryptedCIA int
}

type RuntimeStats struct {
	Platforms       []PlatformRuntimeStats
	Games, Files    int
	ActiveDownloads int
	DB              sql.DBStats
	WAL             bool
}

func (s *Store) RuntimeStats(ctx context.Context) (RuntimeStats, error) {
	rows, err := s.queryCtx(ctx, `SELECT games.id,games.platform,games.rom_patch,artifacts.path,artifacts.extension FROM games LEFT JOIN artifacts ON artifacts.game_id=games.id ORDER BY games.id`)
	if err != nil {
		return RuntimeStats{}, err
	}
	defer rows.Close()
	byPlatform := make(map[string]*PlatformRuntimeStats)
	for _, platform := range Platforms {
		row := &PlatformRuntimeStats{Platform: platform.ID}
		byPlatform[platform.ID] = row
	}
	var out RuntimeStats
	seenGames := map[string]bool{}
	for rows.Next() {
		var gameID, platform string
		var patched bool
		var path, extension sql.NullString
		if err = rows.Scan(&gameID, &platform, &patched, &path, &extension); err != nil {
			return RuntimeStats{}, err
		}
		row := byPlatform[platform]
		if row == nil {
			row = &PlatformRuntimeStats{Platform: platform}
			byPlatform[platform] = row
		}
		if !seenGames[gameID] {
			seenGames[gameID] = true
			row.Games++
			out.Games++
			if patched {
				row.Patched++
			} else {
				row.Base++
			}
		}
		if path.Valid {
			row.Files++
			out.Files++
			encryption := encryptionFor(Artifact{Platform: platform, Path: path.String, Extension: extension.String})
			if encryption == "encrypted" {
				row.EncryptedCIA++
			} else if encryption == "unencrypted" {
				row.UnencryptedCIA++
			}
		}
	}
	if err = rows.Err(); err != nil {
		return RuntimeStats{}, err
	}
	for _, platform := range Platforms {
		out.Platforms = append(out.Platforms, *byPlatform[platform.ID])
	}
	s.activeMu.Lock()
	for _, count := range s.active {
		out.ActiveDownloads += count
	}
	s.activeMu.Unlock()
	out.DB = s.db.Stats()
	_, err = os.Stat(s.dbPath + "-wal")
	out.WAL = err == nil
	return out, nil
}
func (s *Store) Close() error {
	// A clean SQLite owner checkpoints WAL before releasing the file. This keeps
	// normal shutdowns restart-friendly and distinguishes them from forced
	// termination. Postgres has no such per-connection sidecar to flush.
	var checkpointErr error
	if !s.dialect.postgres {
		_, checkpointErr = s.exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	closeErr := s.db.Close()
	return errors.Join(checkpointErr, closeErr)
}
func (s *Store) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}
func (s *Store) SetArtworkDirectory(path string) { s.artworkDir = path }

// Root returns the main library root every platform directory (3ds, gba, ...)
// lives under, so callers writing a new file into the library know where to
// put it without duplicating that layout decision.
func (s *Store) Root() string { return s.root }

// SetScanProgress registers a callback invoked during Reconcile with the number
// of library files indexed so far and the cumulative bytes hashed. It lets a
// large first-time scan (multi-gigabyte ROMs over a slow mount) report progress
// instead of appearing hung. The callback must be cheap and non-blocking.
func (s *Store) SetScanProgress(fn func(files int, hashedBytes int64)) { s.scanProgress = fn }
func (s *Store) RemoveGameArtwork(gameID string) {
	if s.artworkDir == "" {
		return
	}
	for _, ext := range []string{".png", ".jpg", ".webp"} {
		_ = os.Remove(filepath.Join(s.artworkDir, gameID+ext))
	}
}
func (s *Store) RemoveGameArtworkIfOrphan(ctx context.Context, gameID string) {
	var count int
	if err := s.queryRowCtx(ctx, `SELECT COUNT(*) FROM artifacts WHERE game_id=?`, gameID).Scan(&count); err == nil && count == 0 {
		s.RemoveGameArtwork(gameID)
	}
}

// AddArtifactRoot adds another platform-shaped tree, such as patched/<platform>.
func (s *Store) AddArtifactRoot(root string) {
	s.extraRoots = append(s.extraRoots, artifactRoot{root, "patched"})
}
func (s *Store) AddExternalArtifactRoot(root string) {
	s.extraRoots = append(s.extraRoots, artifactRoot{root, "external"})
}
func (s *Store) migrate() error {
	if s.dialect.postgres {
		return s.migratePostgres()
	}
	_, err := s.exec(`CREATE TABLE IF NOT EXISTS games(
id TEXT PRIMARY KEY, platform TEXT NOT NULL, title TEXT NOT NULL, developer TEXT NOT NULL DEFAULT '', publisher TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '', rom_patch BOOLEAN NOT NULL DEFAULT FALSE, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS artifacts(id TEXT PRIMARY KEY, game_id TEXT NOT NULL REFERENCES games(id) ON DELETE CASCADE, platform TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL UNIQUE, extension TEXT NOT NULL, size INTEGER NOT NULL, mod_time INTEGER NOT NULL, sha256 TEXT NOT NULL, sha1 TEXT NOT NULL, md5 TEXT NOT NULL, crc32 TEXT NOT NULL, header_json TEXT NOT NULL DEFAULT '{}', patched BOOLEAN NOT NULL DEFAULT FALSE, expires_at INTEGER, retain BOOLEAN NOT NULL DEFAULT FALSE);
CREATE INDEX IF NOT EXISTS artifacts_game ON artifacts(game_id);
CREATE TABLE IF NOT EXISTS patch_files(id TEXT PRIMARY KEY,path TEXT NOT NULL UNIQUE,size INTEGER NOT NULL,sha256 TEXT NOT NULL,extension TEXT NOT NULL,mod_time INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS patch_jobs(id TEXT PRIMARY KEY,status TEXT NOT NULL,artifact_id TEXT NOT NULL,output_name TEXT NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,error TEXT NOT NULL DEFAULT '',retain BOOLEAN NOT NULL DEFAULT FALSE,expires_at INTEGER);
CREATE TABLE IF NOT EXISTS scheduler_state(name TEXT PRIMARY KEY,last_run INTEGER NOT NULL,status TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL DEFAULT '',role TEXT NOT NULL DEFAULT 'user',created_at INTEGER NOT NULL,disabled BOOLEAN NOT NULL DEFAULT FALSE,must_change_password BOOLEAN NOT NULL DEFAULT FALSE);
CREATE TABLE IF NOT EXISTS pokemon_instances(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,fingerprint TEXT NOT NULL,species INTEGER NOT NULL,species_name TEXT NOT NULL DEFAULT '',nickname TEXT NOT NULL DEFAULT '',level INTEGER NOT NULL DEFAULT 0,nature TEXT NOT NULL DEFAULT '',ability TEXT NOT NULL DEFAULT '',gender TEXT NOT NULL DEFAULT '',shiny BOOLEAN NOT NULL DEFAULT FALSE,is_egg BOOLEAN NOT NULL DEFAULT FALSE,form INTEGER NOT NULL DEFAULT 0,generation INTEGER NOT NULL DEFAULT 0,ivs_json TEXT NOT NULL DEFAULT '{}',evs_json TEXT NOT NULL DEFAULT '{}',moves_json TEXT NOT NULL DEFAULT '[]',held_item TEXT NOT NULL DEFAULT '',ball INTEGER NOT NULL DEFAULT 0,pid INTEGER NOT NULL DEFAULT 0,origin_game TEXT NOT NULL DEFAULT '',origin_ot TEXT NOT NULL DEFAULT '',source_save TEXT NOT NULL DEFAULT '',origin_save_id TEXT NOT NULL DEFAULT '',pokemon_binary BLOB,created_at INTEGER NOT NULL,home_deposited_at INTEGER,home_last_withdrawn_at INTEGER);
CREATE UNIQUE INDEX IF NOT EXISTS pokemon_owner_fingerprint ON pokemon_instances(owner_id,fingerprint);
CREATE INDEX IF NOT EXISTS pokemon_owner_species ON pokemon_instances(owner_id,species);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,created_at INTEGER NOT NULL,expires_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS game_dex(owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,game TEXT NOT NULL,trainer TEXT NOT NULL DEFAULT '',species INTEGER NOT NULL,seen BOOLEAN NOT NULL DEFAULT FALSE,caught BOOLEAN NOT NULL DEFAULT FALSE,PRIMARY KEY(owner_id,game,trainer,species));
CREATE TABLE IF NOT EXISTS game_dex_meta(owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,game TEXT NOT NULL,max_species INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(owner_id,game));
CREATE TABLE IF NOT EXISTS saves(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,game_id TEXT NOT NULL DEFAULT '',game_label TEXT NOT NULL DEFAULT '',file_name TEXT NOT NULL DEFAULT '',size_bytes INTEGER NOT NULL DEFAULT 0,sha256 TEXT NOT NULL,source TEXT NOT NULL DEFAULT 'upload',uploaded_at INTEGER NOT NULL,parsed BOOLEAN NOT NULL DEFAULT FALSE,parsed_game TEXT NOT NULL DEFAULT '',parsed_trainer TEXT NOT NULL DEFAULT '',parsed_playtime TEXT NOT NULL DEFAULT '',pokemon_count INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS saves_owner_game ON saves(owner_id,game_id,uploaded_at DESC);
	CREATE TABLE IF NOT EXISTS pokedex_species(dex INTEGER PRIMARY KEY,name TEXT NOT NULL DEFAULT '',genus TEXT NOT NULL DEFAULT '',generation TEXT NOT NULL DEFAULT '',types TEXT NOT NULL DEFAULT '',height_dm INTEGER NOT NULL DEFAULT 0,weight_hg INTEGER NOT NULL DEFAULT 0,flavor_text TEXT NOT NULL DEFAULT '',is_legendary BOOLEAN NOT NULL DEFAULT FALSE,is_mythical BOOLEAN NOT NULL DEFAULT FALSE,updated_at INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS pokedex_evolution(from_dex INTEGER NOT NULL,to_dex INTEGER NOT NULL,trigger TEXT NOT NULL DEFAULT '',min_level INTEGER NOT NULL DEFAULT 0,item TEXT NOT NULL DEFAULT '',condition TEXT NOT NULL DEFAULT '',PRIMARY KEY(from_dex,to_dex));
	CREATE TABLE IF NOT EXISTS pokedex_regional(dex INTEGER NOT NULL,pokedex TEXT NOT NULL,entry_number INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(dex,pokedex));
	CREATE TABLE IF NOT EXISTS pokedex_encounters(dex INTEGER NOT NULL,version TEXT NOT NULL,location TEXT NOT NULL,method TEXT NOT NULL DEFAULT '',conditions TEXT NOT NULL DEFAULT '',min_level INTEGER NOT NULL DEFAULT 0,max_level INTEGER NOT NULL DEFAULT 0,chance INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(dex,version,location,method,conditions));
	CREATE TABLE IF NOT EXISTS pokedex_encounters_done(dex INTEGER PRIMARY KEY,fetched_at INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS pokedex_moves(name TEXT PRIMARY KEY,type TEXT NOT NULL DEFAULT '',category TEXT NOT NULL DEFAULT '',power INTEGER NOT NULL DEFAULT 0,accuracy INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS pokedex_learnset(dex INTEGER NOT NULL,move TEXT NOT NULL,method TEXT NOT NULL,level INTEGER NOT NULL DEFAULT 0,generations TEXT NOT NULL DEFAULT '',PRIMARY KEY(dex,move,method));
	CREATE TABLE IF NOT EXISTS pokedex_learnset_done(dex INTEGER PRIMARY KEY,fetched_at INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS trainer_profiles(owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,game TEXT NOT NULL,tid INTEGER NOT NULL,sid INTEGER NOT NULL,ot TEXT NOT NULL DEFAULT '',gender INTEGER NOT NULL DEFAULT 0,language INTEGER NOT NULL DEFAULT 0,played_minutes INTEGER NOT NULL DEFAULT 0,money INTEGER NOT NULL DEFAULT 0,money_max INTEGER NOT NULL DEFAULT 0,progress_kind TEXT NOT NULL DEFAULT '',progress_count INTEGER NOT NULL DEFAULT 0,progress_max INTEGER NOT NULL DEFAULT 0,hof_clears INTEGER,notes TEXT NOT NULL DEFAULT '{}',trusted BOOLEAN NOT NULL DEFAULT TRUE,source_save_id TEXT NOT NULL DEFAULT '',updated_at INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(owner_id,game,tid,sid));
	CREATE TABLE IF NOT EXISTS trainer_hof(owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,game TEXT NOT NULL,tid INTEGER NOT NULL,sid INTEGER NOT NULL,entry_index INTEGER NOT NULL,slot INTEGER NOT NULL,species INTEGER NOT NULL DEFAULT 0,level INTEGER NOT NULL DEFAULT 0,nickname TEXT NOT NULL DEFAULT '',PRIMARY KEY(owner_id,game,tid,sid,entry_index,slot));
	CREATE TABLE IF NOT EXISTS api_keys(id TEXT PRIMARY KEY,name TEXT NOT NULL,token TEXT NOT NULL UNIQUE,created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,created_at INTEGER NOT NULL,last_used_at INTEGER,revoked_at INTEGER);`)
	if err != nil {
		return err
	}
	// Additive migrations keep databases created by early ROM Relay builds usable.
	for name, definition := range map[string]string{
		"format":             "TEXT NOT NULL DEFAULT ''",
		"result_artifact_id": "TEXT NOT NULL DEFAULT ''",
		"unverified_source":  "BOOLEAN NOT NULL DEFAULT FALSE",
		"checksum_expected":  "TEXT NOT NULL DEFAULT ''",
		"checksum_actual":    "TEXT NOT NULL DEFAULT ''",
		"patch_file_id":      "TEXT NOT NULL DEFAULT ''",
	} {
		if err := s.ensureColumn("patch_jobs", name, definition); err != nil {
			return err
		}
	}
	for name, definition := range map[string]string{
		"platform":         "TEXT NOT NULL DEFAULT ''",
		"slug":             "TEXT NOT NULL DEFAULT ''",
		"title":            "TEXT NOT NULL DEFAULT ''",
		"output_filename":  "TEXT NOT NULL DEFAULT ''",
		"base_sha256":      "TEXT NOT NULL DEFAULT ''",
		"base_crc32":       "TEXT NOT NULL DEFAULT ''",
		"validation_type":  "TEXT NOT NULL DEFAULT ''",
		"validation_value": "TEXT NOT NULL DEFAULT ''",
	} {
		if err := s.ensureColumn("patch_files", name, definition); err != nil {
			return err
		}
	}
	if err := s.ensureColumn("pokemon_instances", "origin_save_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// origin_slot records where this Pokémon sat in its origin save at ingest
	// time ("party:2", "box:3:12"), the Editor's Party/Box grouping needs it
	// to show where each Pokémon actually lives, not just a flat list.
	if err := s.ensureColumn("pokemon_instances", "origin_slot", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	for _, column := range []string{"home_deposited_at", "home_last_withdrawn_at"} {
		if err := s.ensureColumn("pokemon_instances", column, "INTEGER"); err != nil {
			return err
		}
	}
	for _, column := range []string{"disabled", "must_change_password"} {
		if err := s.ensureColumn("users", column, "BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
			return err
		}
	}
	// Storage boxes were withdrawn in favour of the user Pokédex; drop their
	// columns from databases created while the feature existed. The placements
	// were derived from insertion order, so nothing of value is lost.
	for _, column := range []string{"box", "box_slot"} {
		if err := s.dropColumnIfPresent("pokemon_instances", column); err != nil {
			return err
		}
	}
	if err := s.ensureColumn("games", "patch_file_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("artifacts", "external", "BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
		return err
	}
	for name, definition := range map[string]string{
		"release_date":          "TEXT NOT NULL DEFAULT ''",
		"genre":                 "TEXT NOT NULL DEFAULT ''",
		"players":               "TEXT NOT NULL DEFAULT ''",
		"rating":                "TEXT NOT NULL DEFAULT ''",
		"artwork_url":           "TEXT NOT NULL DEFAULT ''",
		"screenshot_url":        "TEXT NOT NULL DEFAULT ''",
		"metadata_refreshed_at": "INTEGER NOT NULL DEFAULT 0",
		"metadata_source":       "TEXT NOT NULL DEFAULT 'local'",
	} {
		if err := s.ensureColumn("games", name, definition); err != nil {
			return err
		}
	}
	if _, err = s.exec(`CREATE TABLE IF NOT EXISTS libretro_entries(
platform TEXT NOT NULL,name TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',region TEXT NOT NULL DEFAULT '',developer TEXT NOT NULL DEFAULT '',publisher TEXT NOT NULL DEFAULT '',genre TEXT NOT NULL DEFAULT '',release_date TEXT NOT NULL DEFAULT '',players TEXT NOT NULL DEFAULT '',rating TEXT NOT NULL DEFAULT '',crc32 TEXT NOT NULL DEFAULT '',md5 TEXT NOT NULL DEFAULT '',sha1 TEXT NOT NULL DEFAULT '',PRIMARY KEY(platform,name,sha1,crc32));
CREATE INDEX IF NOT EXISTS libretro_sha1 ON libretro_entries(platform,sha1);
CREATE INDEX IF NOT EXISTS libretro_md5 ON libretro_entries(platform,md5);
CREATE INDEX IF NOT EXISTS libretro_crc32 ON libretro_entries(platform,crc32);`); err != nil {
		return err
	}
	if err := s.migrateGameDexTrainer(); err != nil {
		return err
	}
	for name, definition := range map[string]string{
		"egg_groups":    "TEXT NOT NULL DEFAULT ''",
		"gender_rate":   "INTEGER NOT NULL DEFAULT -1",
		"hatch_counter": "INTEGER NOT NULL DEFAULT 0",
	} {
		if err := s.ensureColumn("pokedex_species", name, definition); err != nil {
			return err
		}
	}
	for name, definition := range map[string]string{
		"stat_hp":  "INTEGER NOT NULL DEFAULT 0",
		"stat_atk": "INTEGER NOT NULL DEFAULT 0",
		"stat_def": "INTEGER NOT NULL DEFAULT 0",
		"stat_spa": "INTEGER NOT NULL DEFAULT 0",
		"stat_spd": "INTEGER NOT NULL DEFAULT 0",
		"stat_spe": "INTEGER NOT NULL DEFAULT 0",
	} {
		if err := s.ensureColumn("pokedex_species", name, definition); err != nil {
			return err
		}
	}
	for name, definition := range map[string]string{
		"abilities":      "TEXT NOT NULL DEFAULT ''",
		"hidden_ability": "TEXT NOT NULL DEFAULT ''",
	} {
		if err := s.ensureColumn("pokedex_species", name, definition); err != nil {
			return err
		}
	}
	return nil
}

// migrateGameDexTrainer adds the trainer column and widens game_dex's primary
// key to include it.
//
// This cannot be an ensureColumn: the primary key changes, SQLite cannot ALTER
// one, and the CREATE TABLE IF NOT EXISTS above is a no-op on a database that
// already has the table. Without the rebuild an upgraded database keeps a
// 3-column key while UpsertGameDex issues ON CONFLICT(owner_id,game,trainer,
// species), which needs a matching unique index and errors without one, so a
// fresh database would work and an upgraded one would fail.
func (s *Store) migrateGameDexTrainer() error {
	found, err := s.hasColumn("game_dex", "trainer")
	if err != nil || found {
		return err
	}
	// Foreign keys are dropped for the swap: the rename would otherwise be seen
	// as retargeting game_dex's own reference to users.
	if _, err := s.exec(`PRAGMA foreign_keys=off`); err != nil {
		return err
	}
	defer s.exec(`PRAGMA foreign_keys=on`)
	_, err = s.exec(`CREATE TABLE game_dex_new(owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,game TEXT NOT NULL,trainer TEXT NOT NULL DEFAULT '',species INTEGER NOT NULL,seen BOOLEAN NOT NULL DEFAULT FALSE,caught BOOLEAN NOT NULL DEFAULT FALSE,PRIMARY KEY(owner_id,game,trainer,species));
INSERT INTO game_dex_new(owner_id,game,trainer,species,seen,caught) SELECT owner_id,game,'',species,seen,caught FROM game_dex;
DROP TABLE game_dex;
ALTER TABLE game_dex_new RENAME TO game_dex;`)
	return err
}

func (s *Store) ensureColumn(table, name, definition string) error {
	found, err := s.hasColumn(table, name)
	if err != nil || found {
		return err
	}
	_, err = s.exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition)
	return err
}

// dropColumnIfPresent removes a column from an existing SQLite database, for
// features that have been withdrawn. It is a no-op on databases created after
// the column was removed from the schema.
func (s *Store) dropColumnIfPresent(table, name string) error {
	found, err := s.hasColumn(table, name)
	if err != nil || !found {
		return err
	}
	_, err = s.exec(`ALTER TABLE ` + table + ` DROP COLUMN ` + name)
	return err
}

func (s *Store) hasColumn(table, name string) (bool, error) {
	rows, err := s.query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	found := false
	for rows.Next() {
		var cid int
		var column, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err = rows.Scan(&cid, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return false, err
		}
		found = found || column == name
	}
	if err = rows.Close(); err != nil {
		return false, err
	}
	return found, nil
}

func (s *Store) EnsureDirectories() error {
	for _, p := range Platforms {
		if err := os.MkdirAll(filepath.Join(s.root, p.Directory), 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(s.root, p.Directory, "patches"), 0755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(s.root, "3ds", "3ds"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.root, "3ds", "cia", "encrypted"), 0755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(s.root, "3ds", "cia", "unencrypted"), 0755)
}

// Reconcile recursively indexes supported files from their headers alone, so a
// large library becomes browsable quickly. Content hashes are computed
// separately by HashPending. Unchanged size/modtime pairs reuse the stored
// record, so each file is inspected only once.
func (s *Store) Reconcile(ctx context.Context) error {
	if err := s.EnsureDirectories(); err != nil {
		return err
	}
	// Loaded once so each file on disk is checked against the catalog in
	// memory, rather than a "SELECT count(*)" round-trip per file.
	type fileStat struct{ size, modTime int64 }
	indexed := map[string]fileStat{}
	rows0, err := s.queryCtx(ctx, `SELECT path, size, mod_time FROM artifacts`)
	if err != nil {
		return err
	}
	for rows0.Next() {
		var p string
		var fs fileStat
		if err := rows0.Scan(&p, &fs.size, &fs.modTime); err != nil {
			rows0.Close()
			return err
		}
		indexed[p] = fs
	}
	if err := rows0.Close(); err != nil {
		return err
	}
	seen := map[string]bool{}
	var scanned int
	var lastReport time.Time
	report := func(force bool) {
		if s.scanProgress == nil {
			return
		}
		if force || time.Since(lastReport) >= 250*time.Millisecond {
			lastReport = time.Now()
			s.scanProgress(scanned, 0)
		}
	}
	roots := append([]artifactRoot{{s.root, "library"}}, s.extraRoots...)
	for _, rootInfo := range roots {
		scanRoot := rootInfo.path
		if err := os.MkdirAll(scanRoot, 0755); err != nil {
			return err
		}
		err := filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				if rootInfo.kind == "library" {
					rel, relErr := filepath.Rel(scanRoot, path)
					if relErr != nil {
						return relErr
					}
					parts := strings.FieldsFunc(filepath.ToSlash(rel), func(r rune) bool { return r == '/' })
					if len(parts) == 2 {
						reserved := strings.ToLower(parts[1])
						if reserved == "patches" || reserved == "patched" {
							return filepath.SkipDir
						}
					}
				}
				return nil
			}
			p, ok := DetectPlatform(scanRoot, path)
			if !ok {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			seen[abs] = true
			st, err := d.Info()
			if err != nil {
				return err
			}
			if fs, ok := indexed[abs]; ok && fs.size == st.Size() && fs.modTime == st.ModTime().UnixNano() {
				// Lightweight metadata rules may improve between releases. Apply
				// them to cached files without re-reading multi-gigabyte ROMs.
				if region := regionFromFilename(filepath.Base(abs)); region != "" {
					if _, err = s.exec(`UPDATE games SET region=?,updated_at=? WHERE id=(SELECT game_id FROM artifacts WHERE path=?) AND region='' AND metadata_source NOT LIKE 'manual%'`, region, time.Now().Unix(), abs); err != nil {
						return err
					}
				}
				scanned++
				report(false)
				return nil
			}
			a, err := indexArtifact(abs, p, st)
			if err != nil {
				return err
			}
			a.Patched = rootInfo.kind == "patched"
			a.External = rootInfo.kind == "external"
			if a.Patched {
				normalized := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(a.Name, filepath.Ext(a.Name))))
				sum := sha256.Sum256([]byte(a.Platform + "\x00patched\x00" + normalized))
				a.GameID = hex.EncodeToString(sum[:12])
			}
			if err := s.upsert(a); err != nil {
				return err
			}
			scanned++
			report(false)
			return nil
		})
		if err != nil {
			return err
		}
	}
	report(true)
	rows, err := s.query(`SELECT path FROM artifacts`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if !seen[p] {
			stale = append(stale, p)
		}
	}
	rows.Close()
	for _, p := range stale {
		if _, err = s.exec(`DELETE FROM artifacts WHERE path=?`, p); err != nil {
			return err
		}
	}
	_, err = s.exec(`DELETE FROM games WHERE NOT EXISTS(SELECT 1 FROM artifacts WHERE artifacts.game_id=games.id)`)
	return err
}

// EnsureArtifactHashed returns the artifact, computing and persisting its content
// hashes first if the background pass has not reached it yet. Callers that need
// hashes immediately, patch base-ROM validation, for example, use this so they
// do not depend on background hashing having finished.
func (s *Store) EnsureArtifactHashed(ctx context.Context, id string) (Artifact, error) {
	a, err := s.Artifact(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	if a.SHA256 != "" {
		return a, nil
	}
	sha, sha1Hex, md5Hex, crc32Hex, err := hashFile(ctx, a.Path, nil)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := s.execCtx(ctx, `UPDATE artifacts SET sha256=?,sha1=?,md5=?,crc32=? WHERE id=? AND sha256=''`, sha, sha1Hex, md5Hex, crc32Hex, id); err != nil {
		return Artifact{}, err
	}
	a.SHA256, a.SHA1, a.MD5, a.CRC32 = sha, sha1Hex, md5Hex, crc32Hex
	return a, nil
}

// HashPending computes the content hashes for artifacts that Reconcile indexed
// from their headers but has not hashed yet (sha256 == ""). It processes the
// smallest files first, so metadata for the bulk of a library fills in quickly
// while a few multi-gigabyte images finish last. Files are hashed across a
// bounded pool of workers (disk I/O and hashing both parallelize well across
// the many independent ROMs in a typical library), while database writes stay
// serialized behind the store's single SQLite connection. It is resumable: an
// interrupted run leaves the remaining artifacts unhashed for the next call.
// progress, when non-nil, receives the number of files hashed, the total
// pending, and the cumulative bytes hashed.
func (s *Store) HashPending(ctx context.Context, progress func(done, total int, hashedBytes int64)) error {
	rows, err := s.queryCtx(ctx, `SELECT id, path FROM artifacts WHERE sha256='' ORDER BY size ASC`)
	if err != nil {
		return err
	}
	type pending struct{ id, path string }
	var list []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.path); err != nil {
			rows.Close()
			return err
		}
		list = append(list, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	total := len(list)
	if total == 0 {
		return nil
	}

	var mu sync.Mutex
	var done int
	var hashedBytes int64
	var lastReport time.Time
	var firstErr error
	report := func(force bool) {
		if progress == nil {
			return
		}
		if force || time.Since(lastReport) >= 250*time.Millisecond {
			lastReport = time.Now()
			progress(done, total, hashedBytes)
		}
	}

	// GOMAXPROCS, not NumCPU: under a container CPU quota (docker --cpus),
	// NumCPU still reports the host's full logical core count, while
	// GOMAXPROCS is quota-aware on Linux (Go 1.25+) and tracks how many
	// goroutines can actually run at once, the number this pool should size
	// to, so a constrained container doesn't oversubscribe workers that just
	// thrash for CPU time instead of running.
	workers := runtime.GOMAXPROCS(0)
	if workers > total {
		workers = total
	}
	items := make(chan pending)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range items {
				sha, sha1Hex, md5Hex, crc32Hex, hashErr := hashFile(ctx, item.path, func(n int64) {
					mu.Lock()
					hashedBytes += n
					report(false)
					mu.Unlock()
				})
				if hashErr != nil {
					// A file removed between indexing and hashing is pruned by
					// the next reconcile; skip it rather than aborting the pass.
					if !errors.Is(hashErr, os.ErrNotExist) {
						mu.Lock()
						if firstErr == nil {
							firstErr = hashErr
						}
						mu.Unlock()
					}
					continue
				}
				if _, err := s.execCtx(ctx, `UPDATE artifacts SET sha256=?,sha1=?,md5=?,crc32=? WHERE id=? AND sha256=''`, sha, sha1Hex, md5Hex, crc32Hex, item.id); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				mu.Lock()
				done++
				report(true)
				mu.Unlock()
			}
		}()
	}
feed:
	for _, item := range list {
		select {
		case items <- item:
		case <-ctx.Done():
			break feed
		}
	}
	close(items)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// indexArtifact builds a catalog record from a file's header alone, without
// reading the whole file. This keeps the initial library scan fast (a few
// kilobytes per ROM) so the catalog becomes browsable immediately; the content
// hashes are filled in afterwards by the background pass (HashPending). The
// artifact ID is derived from the path so it is stable and available before any
// hashing.
func indexArtifact(path string, p Platform, st os.FileInfo) (Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	header := make([]byte, 0x1000)
	n, _ := io.ReadFull(f, header)
	header = header[:n]
	idSum := sha256.Sum256([]byte("artifact\x00" + strings.ToLower(path)))
	id := hex.EncodeToString(idSum[:12])
	title, meta := parseHeader(p.ID, header, filepath.Base(path))
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))))
	gs := sha256.Sum256([]byte(p.ID + "\x00" + normalized))
	gid := hex.EncodeToString(gs[:12])
	meta["title"] = title
	return Artifact{ID: id, GameID: gid, Platform: p.ID, Name: filepath.Base(path), Path: path, Extension: strings.ToLower(filepath.Ext(path)), Size: st.Size(), Header: meta, ModTime: st.ModTime().UnixNano()}, nil
}

// hashFile computes the four content hashes for a file in a single pass. onBytes,
// when non-nil, is called with the size of each chunk read, so a caller can
// report progress through a multi-gigabyte file.
func hashFile(ctx context.Context, path string, onBytes func(int64)) (sha256Hex, sha1Hex, md5Hex, crc32Hex string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", "", err
	}
	defer f.Close()
	h256, h1, hm, hc := sha256.New(), sha1.New(), md5.New(), crc32.NewIEEE()
	w := io.MultiWriter(h256, h1, hm, hc)
	buf := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", "", "", "", err
		}
		n, e := f.Read(buf)
		if n > 0 {
			if _, err = w.Write(buf[:n]); err != nil {
				return "", "", "", "", err
			}
			if onBytes != nil {
				onBytes(int64(n))
			}
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return "", "", "", "", e
		}
	}
	return hex.EncodeToString(h256.Sum(nil)), hex.EncodeToString(h1.Sum(nil)), hex.EncodeToString(hm.Sum(nil)), hex.EncodeToString(hc.Sum(nil)), nil
}
func cleanASCII(b []byte) string {
	b = bytesTrim(b)
	var x strings.Builder
	for _, c := range b {
		if c >= 32 && c < 127 {
			x.WriteByte(c)
		}
	}
	return strings.TrimSpace(x.String())
}
func bytesTrim(b []byte) []byte { return []byte(strings.Trim(string(b), "\x00 \xff")) }
func parseHeader(platform string, b []byte, name string) (string, map[string]any) {
	title := strings.TrimSuffix(name, filepath.Ext(name))
	m := map[string]any{}
	switch platform {
	case "gb", "gbc":
		if len(b) >= 0x150 {
			if v := cleanASCII(b[0x134:0x144]); v != "" {
				title = v
			}
			m["game_code"] = cleanASCII(b[0x13f:0x143])
			m["color_flag"] = b[0x143]
			m["sgb_flag"] = b[0x146]
			m["cartridge_type"] = b[0x147]
			m["rom_size_code"] = b[0x148]
			m["ram_size_code"] = b[0x149]
			m["region_code"] = b[0x14a]
			m["version"] = b[0x14c]
		}
	case "gba":
		if len(b) >= 0xc0 {
			if v := cleanASCII(b[0xa0:0xac]); v != "" {
				title = v
			}
			m["game_code"] = cleanASCII(b[0xac:0xb0])
			m["maker_code"] = cleanASCII(b[0xb0:0xb2])
			m["version"] = b[0xbc]
		}
	case "nds":
		if len(b) >= 0x20 {
			if v := cleanASCII(b[:12]); v != "" {
				title = v
			}
			m["game_code"] = cleanASCII(b[12:16])
			m["maker_code"] = cleanASCII(b[16:18])
			m["unit_code"] = b[18]
		}
	case "3ds":
		if len(b) >= 0x110 {
			m["media_id"] = hex.EncodeToString(b[0x108:0x110])
			m["header_size"] = binary.LittleEndian.Uint32(b[:4])
		}
	}
	return title, m
}
func (s *Store) upsert(a Artifact) error {
	b, _ := json.Marshal(a.Header)
	title, _ := a.Header["title"].(string)
	region := regionFromFilename(a.Name)
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = s.texec(tx, `INSERT INTO games(id,platform,title,region,rom_patch,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=CASE WHEN games.metadata_refreshed_at=0 THEN excluded.title ELSE games.title END,region=CASE WHEN games.region='' THEN excluded.region ELSE games.region END,rom_patch=excluded.rom_patch,updated_at=excluded.updated_at`, a.GameID, a.Platform, title, region, a.Patched, now, now)
	if err != nil {
		return err
	}
	_, err = s.texec(tx, `INSERT INTO artifacts(id,game_id,platform,name,path,extension,size,mod_time,sha256,sha1,md5,crc32,header_json,patched,external) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET id=excluded.id,game_id=excluded.game_id,name=excluded.name,extension=excluded.extension,size=excluded.size,mod_time=excluded.mod_time,sha256=excluded.sha256,sha1=excluded.sha1,md5=excluded.md5,crc32=excluded.crc32,header_json=excluded.header_json,patched=excluded.patched,external=excluded.external`, a.ID, a.GameID, a.Platform, a.Name, a.Path, a.Extension, a.Size, a.ModTime, a.SHA256, a.SHA1, a.MD5, a.CRC32, string(b), a.Patched, a.External)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func regionFromFilename(name string) string {
	name = strings.ToLower(name)
	for _, candidate := range []struct{ marker, region string }{
		{"(world)", "World"},
		{"(usa, europe)", "USA, Europe"},
		{"(europe, usa)", "USA, Europe"},
		{"(ue)", "USA, Europe"},
		{"(usa)", "USA"},
		{"(u)", "USA"},
		{"(europe)", "Europe"},
		{"(e)", "Europe"},
		{"(japan)", "Japan"},
		{"(j)", "Japan"},
	} {
		if strings.Contains(name, candidate.marker) {
			return candidate.region
		}
	}
	return ""
}
func (s *Store) Games(ctx context.Context) ([]Game, error) {
	rows, err := s.queryCtx(ctx, `SELECT id,platform,title,developer,publisher,description,region,release_date,genre,players,rating,artwork_url,screenshot_url,metadata_source,patch_file_id,rom_patch FROM games ORDER BY lower(title)`)
	if err != nil {
		return nil, err
	}
	var out []Game
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.Platform, &g.Title, &g.Developer, &g.Publisher, &g.Description, &g.Region, &g.ReleaseDate, &g.Genre, &g.Players, &g.Rating, &g.ArtworkURL, &g.ScreenshotURL, &g.MetadataSource, &g.PatchFileID, &g.ROMPatch); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		a, err := s.artifacts(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Artifacts = a
		out[i].Encryption = gameEncryption(a)
	}
	return out, nil
}
func (s *Store) Game(ctx context.Context, id string) (Game, error) {
	var g Game
	err := s.queryRowCtx(ctx, `SELECT id,platform,title,developer,publisher,description,region,release_date,genre,players,rating,artwork_url,screenshot_url,metadata_source,patch_file_id,rom_patch FROM games WHERE id=?`, id).Scan(&g.ID, &g.Platform, &g.Title, &g.Developer, &g.Publisher, &g.Description, &g.Region, &g.ReleaseDate, &g.Genre, &g.Players, &g.Rating, &g.ArtworkURL, &g.ScreenshotURL, &g.MetadataSource, &g.PatchFileID, &g.ROMPatch)
	if err != nil {
		return g, err
	}
	g.Artifacts, err = s.artifacts(ctx, id)
	g.Encryption = gameEncryption(g.Artifacts)
	return g, err
}
func (s *Store) artifacts(ctx context.Context, gid string) ([]Artifact, error) {
	rows, err := s.queryCtx(ctx, `SELECT id,game_id,platform,name,path,extension,size,sha256,sha1,md5,crc32,header_json,mod_time,patched,external,expires_at,retain FROM artifacts WHERE game_id=? ORDER BY lower(name)`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var h string
		var expires sql.NullInt64
		if err := rows.Scan(&a.ID, &a.GameID, &a.Platform, &a.Name, &a.Path, &a.Extension, &a.Size, &a.SHA256, &a.SHA1, &a.MD5, &a.CRC32, &h, &a.ModTime, &a.Patched, &a.External, &expires, &a.Retain); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(h), &a.Header)
		a.Encryption = encryptionFor(a)
		if expires.Valid {
			v := time.Unix(expires.Int64, 0)
			a.ExpiresAt = &v
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) Artifact(ctx context.Context, id string) (Artifact, error) {
	var a Artifact
	var h string
	var expires sql.NullInt64
	err := s.queryRowCtx(ctx, `SELECT id,game_id,platform,name,path,extension,size,sha256,sha1,md5,crc32,header_json,mod_time,patched,external,expires_at,retain FROM artifacts WHERE id=?`, id).Scan(&a.ID, &a.GameID, &a.Platform, &a.Name, &a.Path, &a.Extension, &a.Size, &a.SHA256, &a.SHA1, &a.MD5, &a.CRC32, &h, &a.ModTime, &a.Patched, &a.External, &expires, &a.Retain)
	json.Unmarshal([]byte(h), &a.Header)
	a.Encryption = encryptionFor(a)
	if expires.Valid {
		v := time.Unix(expires.Int64, 0)
		a.ExpiresAt = &v
	}
	return a, err
}
func (s *Store) ArtifactByPath(ctx context.Context, path string) (Artifact, error) {
	var id string
	if err := s.queryRowCtx(ctx, `SELECT id FROM artifacts WHERE path=?`, path).Scan(&id); err != nil {
		return Artifact{}, err
	}
	return s.Artifact(ctx, id)
}

func (s *Store) MarkArtifactRetention(ctx context.Context, path string, retain bool, expires time.Time) error {
	var value any
	if !retain {
		value = expires.Unix()
	}
	_, err := s.execCtx(ctx, `UPDATE artifacts SET retain=?,expires_at=? WHERE path=? AND (patched=TRUE OR external=TRUE)`, retain, value, path)
	return err
}
func (s *Store) BeginDownload(id string) func() {
	s.activeMu.Lock()
	s.active[id]++
	s.activeMu.Unlock()
	return func() {
		s.activeMu.Lock()
		s.active[id]--
		if s.active[id] <= 0 {
			delete(s.active, id)
		}
		s.activeMu.Unlock()
	}
}
func (s *Store) TouchCompletedDownload(ctx context.Context, id string, retention time.Duration) error {
	expires := s.now().Add(retention).Unix()
	_, err := s.execCtx(ctx, `UPDATE artifacts SET expires_at=? WHERE id=? AND (patched=TRUE OR external=TRUE) AND retain=FALSE`, expires, id)
	return err
}
func (s *Store) CleanupExpired(ctx context.Context) (deleted, deferred int, failures []error) {
	rows, err := s.queryCtx(ctx, `SELECT id,path,game_id FROM artifacts WHERE (patched=TRUE OR external=TRUE) AND retain=FALSE AND expires_at IS NOT NULL AND expires_at<=?`, s.now().Unix())
	if err != nil {
		return 0, 0, []error{err}
	}
	type candidate struct{ id, path, gameID string }
	var items []candidate
	for rows.Next() {
		var c candidate
		if e := rows.Scan(&c.id, &c.path, &c.gameID); e != nil {
			failures = append(failures, e)
		} else {
			items = append(items, c)
		}
	}
	rows.Close()
	for _, c := range items {
		s.activeMu.Lock()
		active := s.active[c.id] > 0
		s.activeMu.Unlock()
		if active {
			deferred++
			continue
		}
		if e := os.Remove(c.path); e != nil && !errors.Is(e, os.ErrNotExist) {
			failures = append(failures, e)
			continue
		}
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if _, e = s.texecCtx(ctx, tx, `DELETE FROM artifacts WHERE id=?`, c.id); e == nil {
			_, e = s.texecCtx(ctx, tx, `DELETE FROM patch_jobs WHERE result_artifact_id=?`, c.id)
		}
		if e == nil {
			_, e = s.texecCtx(ctx, tx, `DELETE FROM games WHERE NOT EXISTS(SELECT 1 FROM artifacts WHERE artifacts.game_id=games.id)`)
		}
		if e != nil {
			tx.Rollback()
			failures = append(failures, e)
			continue
		}
		if e = tx.Commit(); e != nil {
			failures = append(failures, e)
			continue
		}
		// External and patched job folders are private one-file directories.
		// Remove the now-empty leaf without ever recursively touching a library.
		_ = os.Remove(filepath.Dir(c.path))
		s.RemoveGameArtworkIfOrphan(ctx, c.gameID)
		deleted++
	}
	return
}

func (s *Store) SavePatchJob(ctx context.Context, j PatchJobRecord) error {
	var expires any
	if j.ExpiresAt != nil {
		expires = j.ExpiresAt.Unix()
	}
	_, err := s.execCtx(ctx, `INSERT INTO patch_jobs(id,status,artifact_id,output_name,created_at,updated_at,error,retain,expires_at,format,result_artifact_id,unverified_source,checksum_expected,checksum_actual,patch_file_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,updated_at=excluded.updated_at,error=excluded.error,retain=excluded.retain,expires_at=excluded.expires_at,format=excluded.format,result_artifact_id=excluded.result_artifact_id,unverified_source=excluded.unverified_source,checksum_expected=excluded.checksum_expected,checksum_actual=excluded.checksum_actual,patch_file_id=excluded.patch_file_id`, j.ID, j.Status, j.ArtifactID, j.OutputName, j.CreatedAt.Unix(), j.UpdatedAt.Unix(), j.Error, j.Retain, expires, j.Format, j.ResultArtifactID, j.UnverifiedSource, j.ChecksumExpected, j.ChecksumActual, j.PatchFileID)
	return err
}
func (s *Store) PatchJob(ctx context.Context, id string) (PatchJobRecord, error) {
	var j PatchJobRecord
	var created, updated int64
	var expires sql.NullInt64
	err := s.queryRowCtx(ctx, `SELECT id,status,artifact_id,output_name,created_at,updated_at,error,retain,expires_at,format,result_artifact_id,unverified_source,checksum_expected,checksum_actual,patch_file_id FROM patch_jobs WHERE id=?`, id).Scan(&j.ID, &j.Status, &j.ArtifactID, &j.OutputName, &created, &updated, &j.Error, &j.Retain, &expires, &j.Format, &j.ResultArtifactID, &j.UnverifiedSource, &j.ChecksumExpected, &j.ChecksumActual, &j.PatchFileID)
	j.CreatedAt = time.Unix(created, 0)
	j.UpdatedAt = time.Unix(updated, 0)
	if expires.Valid {
		v := time.Unix(expires.Int64, 0)
		j.ExpiresAt = &v
	}
	return j, err
}

func (s *Store) ReplacePatchRecipes(ctx context.Context, recipes []PatchRecipe) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = s.texecCtx(ctx, tx, `DELETE FROM patch_files`); err != nil {
		return err
	}
	stmt, err := s.tprepareCtx(ctx, tx, `INSERT INTO patch_files(id,path,size,sha256,extension,mod_time,platform,slug,title,output_filename,base_sha256,base_crc32,validation_type,validation_value) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range recipes {
		if _, err = stmt.ExecContext(ctx, r.ID, r.Path, r.Size, r.SHA256, r.Extension, r.ModTime, r.Platform, r.Slug, r.Title, r.OutputFilename, r.BaseSHA256, r.BaseCRC32, r.ValidationType, r.ValidationValue); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) PatchRecipes(ctx context.Context) ([]PatchRecipe, error) {
	rows, err := s.queryCtx(ctx, `SELECT id,platform,slug,title,path,sha256,extension,output_filename,base_sha256,base_crc32,validation_type,validation_value,size,mod_time FROM patch_files ORDER BY platform,lower(title),id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PatchRecipe
	for rows.Next() {
		var r PatchRecipe
		if err = rows.Scan(&r.ID, &r.Platform, &r.Slug, &r.Title, &r.Path, &r.SHA256, &r.Extension, &r.OutputFilename, &r.BaseSHA256, &r.BaseCRC32, &r.ValidationType, &r.ValidationValue, &r.Size, &r.ModTime); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) PatchRecipe(ctx context.Context, id string) (PatchRecipe, error) {
	var r PatchRecipe
	err := s.queryRowCtx(ctx, `SELECT id,platform,slug,title,path,sha256,extension,output_filename,base_sha256,base_crc32,validation_type,validation_value,size,mod_time FROM patch_files WHERE id=?`, id).Scan(&r.ID, &r.Platform, &r.Slug, &r.Title, &r.Path, &r.SHA256, &r.Extension, &r.OutputFilename, &r.BaseSHA256, &r.BaseCRC32, &r.ValidationType, &r.ValidationValue, &r.Size, &r.ModTime)
	return r, err
}
func (s *Store) LinkPatchedGameRecipe(ctx context.Context, gameID, patchID string) error {
	_, err := s.execCtx(ctx, `UPDATE games SET patch_file_id=? WHERE id=? AND rom_patch=TRUE`, patchID, gameID)
	return err
}
func (s *Store) DeletePatchJob(ctx context.Context, id string) error {
	_, err := s.execCtx(ctx, `DELETE FROM patch_jobs WHERE id=?`, id)
	return err
}

func encryptionFor(a Artifact) string {
	if a.Platform != "3ds" || !strings.EqualFold(a.Extension, ".cia") {
		return ""
	}
	parts := strings.FieldsFunc(strings.ToLower(filepath.ToSlash(filepath.Dir(a.Path))), func(r rune) bool { return r == '/' })
	for _, part := range parts {
		if part == "unencrypted" {
			return "unencrypted"
		}
	}
	// Converted CIAs and legacy files directly under 3ds/cia default encrypted.
	return "encrypted"
}

func gameEncryption(artifacts []Artifact) string {
	encrypted, unencrypted := false, false
	for _, a := range artifacts {
		if a.Encryption == "encrypted" {
			encrypted = true
		}
		if a.Encryption == "unencrypted" {
			unencrypted = true
		}
	}
	if encrypted && unencrypted {
		return "mixed"
	}
	if unencrypted {
		return "unencrypted"
	}
	if encrypted {
		return "encrypted"
	}
	return ""
}

// MetadataCandidates is the provider boundary: patched games are excluded in
// SQL so no manual refresh or stale-row caller can accidentally enqueue them.
func (s *Store) MetadataCandidates(ctx context.Context, staleBefore time.Time) ([]Game, error) {
	return s.metadataCandidates(ctx, `rom_patch=FALSE AND metadata_source NOT LIKE 'manual%' AND metadata_refreshed_at<?`, staleBefore.Unix())
}
func (s *Store) IGDBCandidates(ctx context.Context, staleBefore time.Time) ([]Game, error) {
	return s.metadataCandidates(ctx, `rom_patch=FALSE AND metadata_source NOT LIKE 'manual%' AND (description='' OR metadata_refreshed_at<?)`, staleBefore.Unix())
}
func (s *Store) metadataCandidates(ctx context.Context, where string, args ...any) ([]Game, error) {
	rows, err := s.queryCtx(ctx, `SELECT id,platform,title,developer,publisher,description,region,release_date,genre,players,rating,artwork_url,screenshot_url,metadata_source,patch_file_id,rom_patch FROM games WHERE `+where+` ORDER BY metadata_refreshed_at`, args...)
	if err != nil {
		return nil, err
	}
	var out []Game
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.Platform, &g.Title, &g.Developer, &g.Publisher, &g.Description, &g.Region, &g.ReleaseDate, &g.Genre, &g.Players, &g.Rating, &g.ArtworkURL, &g.ScreenshotURL, &g.MetadataSource, &g.PatchFileID, &g.ROMPatch); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Artifacts, err = s.artifacts(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return out, err
}

// UpdateGameMetadata is guarded again in SQL: even a compromised or stale
// provider queue cannot write remote metadata onto a patched game.
func (s *Store) UpdateGameMetadata(ctx context.Context, g Game, refreshed time.Time) error {
	_, err := s.execCtx(ctx, `UPDATE games SET title=CASE WHEN ?='' THEN title ELSE ? END,developer=?,publisher=?,description=?,region=?,release_date=?,genre=?,players=?,rating=?,artwork_url=?,screenshot_url=?,metadata_source=?,metadata_refreshed_at=? WHERE id=? AND rom_patch=FALSE AND metadata_source NOT LIKE 'manual%'`, g.Title, g.Title, g.Developer, g.Publisher, g.Description, g.Region, g.ReleaseDate, g.Genre, g.Players, g.Rating, g.ArtworkURL, g.ScreenshotURL, g.MetadataSource, refreshed.Unix(), g.ID)
	return err
}

// UpdateManualGameMetadata applies an operator-owned override to any catalog
// game. Provider candidate queries and writes explicitly exclude these rows.
func (s *Store) UpdateManualGameMetadata(ctx context.Context, g Game, refreshed time.Time) error {
	result, err := s.execCtx(ctx, `UPDATE games SET title=?,developer=?,publisher=?,description=?,region=?,release_date=?,genre=?,players=?,rating=?,artwork_url=?,metadata_source='manual',metadata_refreshed_at=?,updated_at=? WHERE id=?`, g.Title, g.Developer, g.Publisher, g.Description, g.Region, g.ReleaseDate, g.Genre, g.Players, g.Rating, g.ArtworkURL, refreshed.Unix(), refreshed.Unix(), g.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// UpdatePatchedGameMetadata is intentionally separate from provider writes.
// Its SQL guard prevents manual data from being attached to an unpatched game.
func (s *Store) UpdatePatchedGameMetadata(ctx context.Context, g Game, refreshed time.Time) error {
	_, err := s.execCtx(ctx, `UPDATE games SET title=CASE WHEN ?='' THEN title ELSE ? END,developer=?,publisher=?,description=?,region=?,release_date=?,genre=?,artwork_url=?,metadata_source='manual',metadata_refreshed_at=? WHERE id=? AND rom_patch=TRUE`, g.Title, g.Title, g.Developer, g.Publisher, g.Description, g.Region, g.ReleaseDate, g.Genre, g.ArtworkURL, refreshed.Unix(), g.ID)
	return err
}

func (s *Store) MarkMetadataChecked(ctx context.Context, id string, refreshed time.Time) error {
	_, err := s.execCtx(ctx, `UPDATE games SET metadata_refreshed_at=? WHERE id=? AND rom_patch=FALSE`, refreshed.Unix(), id)
	return err
}

// ReplaceLibretroPlatform swaps one platform index in a transaction, so an
// interrupted update leaves the previous offline database intact.
func (s *Store) ReplaceLibretroPlatform(ctx context.Context, platform string, records []MetadataRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = s.texecCtx(ctx, tx, `DELETE FROM libretro_entries WHERE platform=?`, platform); err != nil {
		return err
	}
	stmt, err := s.tprepareCtx(ctx, tx, `INSERT INTO libretro_entries(platform,name,description,region,developer,publisher,genre,release_date,players,rating,crc32,md5,sha1) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range records {
		if _, err = stmt.ExecContext(ctx, platform, r.Name, r.Description, r.Region, r.Developer, r.Publisher, r.Genre, r.ReleaseDate, r.Players, r.Rating, strings.ToLower(r.CRC32), strings.ToLower(r.MD5), strings.ToLower(r.SHA1)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MatchLibretro(ctx context.Context, a Artifact) (MetadataRecord, error) {
	var r MetadataRecord
	r.Platform = a.Platform
	columns := `name,description,region,developer,publisher,genre,release_date,players,rating,crc32,md5,sha1`
	queries := []struct{ field, value string }{{"sha1", a.SHA1}, {"md5", a.MD5}, {"crc32", a.CRC32}}
	for _, q := range queries {
		if q.value == "" {
			continue
		}
		err := s.queryRowCtx(ctx, `SELECT `+columns+` FROM libretro_entries WHERE platform=? AND `+q.field+`=? LIMIT 1`, a.Platform, strings.ToLower(q.value)).Scan(&r.Name, &r.Description, &r.Region, &r.Developer, &r.Publisher, &r.Genre, &r.ReleaseDate, &r.Players, &r.Rating, &r.CRC32, &r.MD5, &r.SHA1)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
	}
	return r, sql.ErrNoRows
}

// MatchLibretroName is a conservative container fallback for canonical release
// filenames. It is used only after every available content hash misses and
// requires an exact case-insensitive name within the already-known platform.
// This recovers metadata for converted 3DS containers and ROMs whose display
// title came from a short cartridge header without introducing fuzzy matches.
func (s *Store) MatchLibretroName(ctx context.Context, platform string, names ...string) (MetadataRecord, error) {
	var r MetadataRecord
	r.Platform = platform
	columns := `name,description,region,developer,publisher,genre,release_date,players,rating,crc32,md5,sha1`
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		err := s.queryRowCtx(ctx, `SELECT `+columns+` FROM libretro_entries WHERE platform=? AND lower(name)=lower(?) LIMIT 1`, platform, name).Scan(&r.Name, &r.Description, &r.Region, &r.Developer, &r.Publisher, &r.Genre, &r.ReleaseDate, &r.Players, &r.Rating, &r.CRC32, &r.MD5, &r.SHA1)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
	}
	return r, sql.ErrNoRows
}

func (s *Store) SchedulerState(ctx context.Context, name string) (time.Time, string, error) {
	var unix int64
	var status string
	err := s.queryRowCtx(ctx, `SELECT last_run,status FROM scheduler_state WHERE name=?`, name).Scan(&unix, &status)
	return time.Unix(unix, 0), status, err
}
func (s *Store) SetSchedulerState(ctx context.Context, name string, when time.Time, status string) error {
	_, err := s.execCtx(ctx, `INSERT INTO scheduler_state(name,last_run,status) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET last_run=excluded.last_run,status=excluded.status`, name, when.Unix(), status)
	return err
}
