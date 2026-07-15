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
	db         *sql.DB
	dbPath     string
	root       string
	artworkDir string
	extraRoots []artifactRoot
	now        func() time.Time
	activeMu   sync.Mutex
	active     map[string]int
}
type artifactRoot struct{ path, kind string }

func Open(path, root string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	s := &Store{db: db, dbPath: path, root: root, now: time.Now, active: map[string]int{}}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
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
	rows, err := s.db.QueryContext(ctx, `SELECT games.id,games.platform,games.rom_patch,artifacts.path,artifacts.extension FROM games LEFT JOIN artifacts ON artifacts.game_id=games.id ORDER BY games.id`)
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
	// A clean owner checkpoints WAL before releasing SQLite. This keeps normal
	// shutdowns restart-friendly and distinguishes them from forced termination.
	_, checkpointErr := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	closeErr := s.db.Close()
	return errors.Join(checkpointErr, closeErr)
}
func (s *Store) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}
func (s *Store) SetArtworkDirectory(path string) { s.artworkDir = path }
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
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE game_id=?`, gameID).Scan(&count); err == nil && count == 0 {
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
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS games(
id TEXT PRIMARY KEY, platform TEXT NOT NULL, title TEXT NOT NULL, developer TEXT NOT NULL DEFAULT '', publisher TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '', rom_patch BOOLEAN NOT NULL DEFAULT FALSE, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS artifacts(id TEXT PRIMARY KEY, game_id TEXT NOT NULL REFERENCES games(id) ON DELETE CASCADE, platform TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL UNIQUE, extension TEXT NOT NULL, size INTEGER NOT NULL, mod_time INTEGER NOT NULL, sha256 TEXT NOT NULL, sha1 TEXT NOT NULL, md5 TEXT NOT NULL, crc32 TEXT NOT NULL, header_json TEXT NOT NULL DEFAULT '{}', patched BOOLEAN NOT NULL DEFAULT FALSE, expires_at INTEGER, retain BOOLEAN NOT NULL DEFAULT FALSE);
CREATE INDEX IF NOT EXISTS artifacts_game ON artifacts(game_id);
CREATE TABLE IF NOT EXISTS patch_files(id TEXT PRIMARY KEY,path TEXT NOT NULL UNIQUE,size INTEGER NOT NULL,sha256 TEXT NOT NULL,extension TEXT NOT NULL,mod_time INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS patch_jobs(id TEXT PRIMARY KEY,status TEXT NOT NULL,artifact_id TEXT NOT NULL,output_name TEXT NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,error TEXT NOT NULL DEFAULT '',retain BOOLEAN NOT NULL DEFAULT FALSE,expires_at INTEGER);
CREATE TABLE IF NOT EXISTS scheduler_state(name TEXT PRIMARY KEY,last_run INTEGER NOT NULL,status TEXT NOT NULL DEFAULT '');`)
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
	if _, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS libretro_entries(
platform TEXT NOT NULL,name TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',region TEXT NOT NULL DEFAULT '',developer TEXT NOT NULL DEFAULT '',publisher TEXT NOT NULL DEFAULT '',genre TEXT NOT NULL DEFAULT '',release_date TEXT NOT NULL DEFAULT '',players TEXT NOT NULL DEFAULT '',rating TEXT NOT NULL DEFAULT '',crc32 TEXT NOT NULL DEFAULT '',md5 TEXT NOT NULL DEFAULT '',sha1 TEXT NOT NULL DEFAULT '',PRIMARY KEY(platform,name,sha1,crc32));
CREATE INDEX IF NOT EXISTS libretro_sha1 ON libretro_entries(platform,sha1);
CREATE INDEX IF NOT EXISTS libretro_md5 ON libretro_entries(platform,md5);
CREATE INDEX IF NOT EXISTS libretro_crc32 ON libretro_entries(platform,crc32);`); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureColumn(table, name, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var column, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err = rows.Scan(&cid, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		found = found || column == name
	}
	if err = rows.Close(); err != nil || found {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition)
	return err
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

// Reconcile recursively indexes supported files. Unchanged size/modtime pairs
// reuse stored fingerprints, so every changed byte stream is read only once.
func (s *Store) Reconcile(ctx context.Context) error {
	if err := s.EnsureDirectories(); err != nil {
		return err
	}
	seen := map[string]bool{}
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
			var n int
			if err = s.db.QueryRow(`SELECT count(*) FROM artifacts WHERE path=? AND size=? AND mod_time=?`, abs, st.Size(), st.ModTime().UnixNano()).Scan(&n); err != nil {
				return err
			}
			if n == 1 {
				// Lightweight metadata rules may improve between releases. Apply
				// them to cached files without re-reading multi-gigabyte ROMs.
				if region := regionFromFilename(filepath.Base(abs)); region != "" {
					if _, err = s.db.Exec(`UPDATE games SET region=?,updated_at=? WHERE id=(SELECT game_id FROM artifacts WHERE path=?) AND region='' AND metadata_source NOT LIKE 'manual%'`, region, time.Now().Unix(), abs); err != nil {
						return err
					}
				}
				return nil
			}
			a, err := fingerprint(ctx, abs, p, st)
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
			return s.upsert(a)
		})
		if err != nil {
			return err
		}
	}
	rows, err := s.db.Query(`SELECT path FROM artifacts`)
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
		if _, err = s.db.Exec(`DELETE FROM artifacts WHERE path=?`, p); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`DELETE FROM games WHERE NOT EXISTS(SELECT 1 FROM artifacts WHERE artifacts.game_id=games.id)`)
	return err
}

func fingerprint(ctx context.Context, path string, p Platform, st os.FileInfo) (Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	h256, h1, hm, hc := sha256.New(), sha1.New(), md5.New(), crc32.NewIEEE()
	header := make([]byte, 0x1000)
	n, _ := io.ReadFull(f, header)
	header = header[:n]
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return Artifact{}, err
	}
	w := io.MultiWriter(h256, h1, hm, hc)
	buf := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return Artifact{}, err
		}
		n, e := f.Read(buf)
		if n > 0 {
			if _, err = w.Write(buf[:n]); err != nil {
				return Artifact{}, err
			}
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return Artifact{}, e
		}
	}
	sha := hex.EncodeToString(h256.Sum(nil))
	idSum := sha256.Sum256([]byte(sha + "\x00" + strings.ToLower(path)))
	id := hex.EncodeToString(idSum[:12])
	title, meta := parseHeader(p.ID, header, filepath.Base(path))
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))))
	gs := sha256.Sum256([]byte(p.ID + "\x00" + normalized))
	gid := hex.EncodeToString(gs[:12])
	meta["title"] = title
	return Artifact{ID: id, GameID: gid, Platform: p.ID, Name: filepath.Base(path), Path: path, Extension: strings.ToLower(filepath.Ext(path)), Size: st.Size(), SHA256: sha, SHA1: hex.EncodeToString(h1.Sum(nil)), MD5: hex.EncodeToString(hm.Sum(nil)), CRC32: hex.EncodeToString(hc.Sum(nil)), Header: meta, ModTime: st.ModTime().UnixNano()}, nil
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
	_, err = tx.Exec(`INSERT INTO games(id,platform,title,region,rom_patch,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=CASE WHEN games.metadata_refreshed_at=0 THEN excluded.title ELSE games.title END,region=CASE WHEN games.region='' THEN excluded.region ELSE games.region END,rom_patch=excluded.rom_patch,updated_at=excluded.updated_at`, a.GameID, a.Platform, title, region, a.Patched, now, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO artifacts(id,game_id,platform,name,path,extension,size,mod_time,sha256,sha1,md5,crc32,header_json,patched,external) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET id=excluded.id,game_id=excluded.game_id,name=excluded.name,extension=excluded.extension,size=excluded.size,mod_time=excluded.mod_time,sha256=excluded.sha256,sha1=excluded.sha1,md5=excluded.md5,crc32=excluded.crc32,header_json=excluded.header_json,patched=excluded.patched,external=excluded.external`, a.ID, a.GameID, a.Platform, a.Name, a.Path, a.Extension, a.Size, a.ModTime, a.SHA256, a.SHA1, a.MD5, a.CRC32, string(b), a.Patched, a.External)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id,platform,title,developer,publisher,description,region,release_date,genre,players,rating,artwork_url,screenshot_url,metadata_source,patch_file_id,rom_patch FROM games ORDER BY lower(title)`)
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
	err := s.db.QueryRowContext(ctx, `SELECT id,platform,title,developer,publisher,description,region,release_date,genre,players,rating,artwork_url,screenshot_url,metadata_source,patch_file_id,rom_patch FROM games WHERE id=?`, id).Scan(&g.ID, &g.Platform, &g.Title, &g.Developer, &g.Publisher, &g.Description, &g.Region, &g.ReleaseDate, &g.Genre, &g.Players, &g.Rating, &g.ArtworkURL, &g.ScreenshotURL, &g.MetadataSource, &g.PatchFileID, &g.ROMPatch)
	if err != nil {
		return g, err
	}
	g.Artifacts, err = s.artifacts(ctx, id)
	g.Encryption = gameEncryption(g.Artifacts)
	return g, err
}
func (s *Store) artifacts(ctx context.Context, gid string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,game_id,platform,name,path,extension,size,sha256,sha1,md5,crc32,header_json,mod_time,patched,external,expires_at,retain FROM artifacts WHERE game_id=? ORDER BY lower(name)`, gid)
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
	err := s.db.QueryRowContext(ctx, `SELECT id,game_id,platform,name,path,extension,size,sha256,sha1,md5,crc32,header_json,mod_time,patched,external,expires_at,retain FROM artifacts WHERE id=?`, id).Scan(&a.ID, &a.GameID, &a.Platform, &a.Name, &a.Path, &a.Extension, &a.Size, &a.SHA256, &a.SHA1, &a.MD5, &a.CRC32, &h, &a.ModTime, &a.Patched, &a.External, &expires, &a.Retain)
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
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE path=?`, path).Scan(&id); err != nil {
		return Artifact{}, err
	}
	return s.Artifact(ctx, id)
}

func (s *Store) MarkArtifactRetention(ctx context.Context, path string, retain bool, expires time.Time) error {
	var value any
	if !retain {
		value = expires.Unix()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artifacts SET retain=?,expires_at=? WHERE path=? AND (patched=TRUE OR external=TRUE)`, retain, value, path)
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
	_, err := s.db.ExecContext(ctx, `UPDATE artifacts SET expires_at=? WHERE id=? AND (patched=TRUE OR external=TRUE) AND retain=FALSE`, expires, id)
	return err
}
func (s *Store) CleanupExpired(ctx context.Context) (deleted, deferred int, failures []error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,path,game_id FROM artifacts WHERE (patched=TRUE OR external=TRUE) AND retain=FALSE AND expires_at IS NOT NULL AND expires_at<=?`, s.now().Unix())
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
		if _, e = tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id=?`, c.id); e == nil {
			_, e = tx.ExecContext(ctx, `DELETE FROM patch_jobs WHERE result_artifact_id=?`, c.id)
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, `DELETE FROM games WHERE NOT EXISTS(SELECT 1 FROM artifacts WHERE artifacts.game_id=games.id)`)
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO patch_jobs(id,status,artifact_id,output_name,created_at,updated_at,error,retain,expires_at,format,result_artifact_id,unverified_source,checksum_expected,checksum_actual,patch_file_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,updated_at=excluded.updated_at,error=excluded.error,retain=excluded.retain,expires_at=excluded.expires_at,format=excluded.format,result_artifact_id=excluded.result_artifact_id,unverified_source=excluded.unverified_source,checksum_expected=excluded.checksum_expected,checksum_actual=excluded.checksum_actual,patch_file_id=excluded.patch_file_id`, j.ID, j.Status, j.ArtifactID, j.OutputName, j.CreatedAt.Unix(), j.UpdatedAt.Unix(), j.Error, j.Retain, expires, j.Format, j.ResultArtifactID, j.UnverifiedSource, j.ChecksumExpected, j.ChecksumActual, j.PatchFileID)
	return err
}
func (s *Store) PatchJob(ctx context.Context, id string) (PatchJobRecord, error) {
	var j PatchJobRecord
	var created, updated int64
	var expires sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,status,artifact_id,output_name,created_at,updated_at,error,retain,expires_at,format,result_artifact_id,unverified_source,checksum_expected,checksum_actual,patch_file_id FROM patch_jobs WHERE id=?`, id).Scan(&j.ID, &j.Status, &j.ArtifactID, &j.OutputName, &created, &updated, &j.Error, &j.Retain, &expires, &j.Format, &j.ResultArtifactID, &j.UnverifiedSource, &j.ChecksumExpected, &j.ChecksumActual, &j.PatchFileID)
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
	if _, err = tx.ExecContext(ctx, `DELETE FROM patch_files`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO patch_files(id,path,size,sha256,extension,mod_time,platform,slug,title,output_filename,base_sha256,base_crc32,validation_type,validation_value) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id,platform,slug,title,path,sha256,extension,output_filename,base_sha256,base_crc32,validation_type,validation_value,size,mod_time FROM patch_files ORDER BY platform,lower(title),id`)
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
	err := s.db.QueryRowContext(ctx, `SELECT id,platform,slug,title,path,sha256,extension,output_filename,base_sha256,base_crc32,validation_type,validation_value,size,mod_time FROM patch_files WHERE id=?`, id).Scan(&r.ID, &r.Platform, &r.Slug, &r.Title, &r.Path, &r.SHA256, &r.Extension, &r.OutputFilename, &r.BaseSHA256, &r.BaseCRC32, &r.ValidationType, &r.ValidationValue, &r.Size, &r.ModTime)
	return r, err
}
func (s *Store) LinkPatchedGameRecipe(ctx context.Context, gameID, patchID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE games SET patch_file_id=? WHERE id=? AND rom_patch=TRUE`, patchID, gameID)
	return err
}
func (s *Store) DeletePatchJob(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM patch_jobs WHERE id=?`, id)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id,platform,title,developer,publisher,description,region,release_date,genre,players,rating,artwork_url,screenshot_url,metadata_source,patch_file_id,rom_patch FROM games WHERE `+where+` ORDER BY metadata_refreshed_at`, args...)
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
	_, err := s.db.ExecContext(ctx, `UPDATE games SET title=CASE WHEN ?='' THEN title ELSE ? END,developer=?,publisher=?,description=?,region=?,release_date=?,genre=?,players=?,rating=?,artwork_url=?,screenshot_url=?,metadata_source=?,metadata_refreshed_at=? WHERE id=? AND rom_patch=FALSE AND metadata_source NOT LIKE 'manual%'`, g.Title, g.Title, g.Developer, g.Publisher, g.Description, g.Region, g.ReleaseDate, g.Genre, g.Players, g.Rating, g.ArtworkURL, g.ScreenshotURL, g.MetadataSource, refreshed.Unix(), g.ID)
	return err
}

// UpdateManualGameMetadata applies an operator-owned override to any catalog
// game. Provider candidate queries and writes explicitly exclude these rows.
func (s *Store) UpdateManualGameMetadata(ctx context.Context, g Game, refreshed time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE games SET title=?,developer=?,publisher=?,description=?,region=?,release_date=?,genre=?,players=?,rating=?,artwork_url=?,metadata_source='manual',metadata_refreshed_at=?,updated_at=? WHERE id=?`, g.Title, g.Developer, g.Publisher, g.Description, g.Region, g.ReleaseDate, g.Genre, g.Players, g.Rating, g.ArtworkURL, refreshed.Unix(), refreshed.Unix(), g.ID)
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
	_, err := s.db.ExecContext(ctx, `UPDATE games SET title=CASE WHEN ?='' THEN title ELSE ? END,developer=?,publisher=?,description=?,region=?,release_date=?,genre=?,artwork_url=?,metadata_source='manual',metadata_refreshed_at=? WHERE id=? AND rom_patch=TRUE`, g.Title, g.Title, g.Developer, g.Publisher, g.Description, g.Region, g.ReleaseDate, g.Genre, g.ArtworkURL, refreshed.Unix(), g.ID)
	return err
}

func (s *Store) MarkMetadataChecked(ctx context.Context, id string, refreshed time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE games SET metadata_refreshed_at=? WHERE id=? AND rom_patch=FALSE`, refreshed.Unix(), id)
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
	if _, err = tx.ExecContext(ctx, `DELETE FROM libretro_entries WHERE platform=?`, platform); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO libretro_entries(platform,name,description,region,developer,publisher,genre,release_date,players,rating,crc32,md5,sha1) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
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
		err := s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM libretro_entries WHERE platform=? AND `+q.field+`=? LIMIT 1`, a.Platform, strings.ToLower(q.value)).Scan(&r.Name, &r.Description, &r.Region, &r.Developer, &r.Publisher, &r.Genre, &r.ReleaseDate, &r.Players, &r.Rating, &r.CRC32, &r.MD5, &r.SHA1)
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
		err := s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM libretro_entries WHERE platform=? AND lower(name)=lower(?) LIMIT 1`, platform, name).Scan(&r.Name, &r.Description, &r.Region, &r.Developer, &r.Publisher, &r.Genre, &r.ReleaseDate, &r.Players, &r.Rating, &r.CRC32, &r.MD5, &r.SHA1)
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
	err := s.db.QueryRowContext(ctx, `SELECT last_run,status FROM scheduler_state WHERE name=?`, name).Scan(&unix, &status)
	return time.Unix(unix, 0), status, err
}
func (s *Store) SetSchedulerState(ctx context.Context, name string, when time.Time, status string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO scheduler_state(name,last_run,status) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET last_run=excluded.last_run,status=excluded.status`, name, when.Unix(), status)
	return err
}
