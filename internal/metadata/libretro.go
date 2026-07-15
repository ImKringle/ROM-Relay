package metadata

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"romrelay/internal/catalog"
)

type LibretroOptions struct {
	DatabaseDirectory, ThumbnailDirectory string
	AutoUpdate, DownloadThumbnails        bool
	Progress                              func(stage string, done, total int)
	ROMProgress                           func(ROMProgress)
}

type ROMProgress struct {
	Provider, GameID, Title, Status string
	Done, Total                     int
}

type Libretro struct {
	store  *catalog.Store
	opts   LibretroOptions
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
}

var errNoLocalDAT = errors.New("no local Libretro DAT")

type platformSource struct{ ID, Database, ThumbnailRepo string }
type enrichOutcome struct {
	game    catalog.Game
	matched bool
	err     error
}

var libretroPlatforms = []platformSource{
	{"gb", "Nintendo - Game Boy", "Nintendo_-_Game_Boy"},
	{"gbc", "Nintendo - Game Boy Color", "Nintendo_-_Game_Boy_Color"},
	{"gba", "Nintendo - Game Boy Advance", "Nintendo_-_Game_Boy_Advance"},
	{"nds", "Nintendo - Nintendo DS", "Nintendo_-_Nintendo_DS"},
	{"3ds", "Nintendo - Nintendo 3DS", "Nintendo_-_Nintendo_3DS"},
	{"nes", "Nintendo - Nintendo Entertainment System", "Nintendo_-_Nintendo_Entertainment_System"},
	{"snes", "Nintendo - Super Nintendo Entertainment System", "Nintendo_-_Super_Nintendo_Entertainment_System"},
	{"n64", "Nintendo - Nintendo 64", "Nintendo_-_Nintendo_64"},
	{"genesis", "Sega - Mega Drive - Genesis", "Sega_-_Mega_Drive_-_Genesis"},
}

func NewLibretro(store *catalog.Store, opts LibretroOptions) *Libretro {
	return &Libretro{store: store, opts: opts, client: &http.Client{Timeout: 45 * time.Second}, now: time.Now}
}

// Sync downloads (when enabled), parses, and transactionally imports only the
// supported platform DATs. A failed platform keeps its prior SQLite index.
func (l *Libretro) Sync(ctx context.Context) (platforms, records int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	type result struct {
		p    platformSource
		recs []catalog.MetadataRecord
		err  error
	}
	jobs := make(chan platformSource)
	results := make(chan result)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				recs, e := l.loadPlatform(ctx, p)
				select {
				case results <- result{p, recs, e}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(results)
		defer wg.Wait()
		for _, p := range libretroPlatforms {
			select {
			case jobs <- p:
			case <-ctx.Done():
				close(jobs)
				return
			}
		}
		close(jobs)
	}()
	var failures []error
	done := 0
	for result := range results {
		p, recs, e := result.p, result.recs, result.err
		done++
		if l.opts.Progress != nil {
			l.opts.Progress("index", done, len(libretroPlatforms))
		}
		if e != nil {
			if errors.Is(e, errNoLocalDAT) {
				continue
			}
			failures = append(failures, fmt.Errorf("%s: %w", p.ID, e))
			continue
		}
		if e = l.store.ReplaceLibretroPlatform(ctx, p.ID, recs); e != nil {
			failures = append(failures, fmt.Errorf("%s import: %w", p.ID, e))
			continue
		}
		platforms++
		records += len(recs)
	}
	status := fmt.Sprintf("%d platforms, %d records", platforms, records)
	if joined := errors.Join(failures...); joined != nil {
		status += "; " + joined.Error()
		err = joined
	}
	updatedAt := l.now()
	if err != nil {
		// A partial or cancelled refresh must not look fresh for the entire
		// metadata_refresh_days window. Keep its diagnostic status but make the
		// scheduler eligible to retry on the next hourly check.
		updatedAt = time.Unix(0, 0)
	}
	_ = l.store.SetSchedulerState(context.Background(), "libretro_update", updatedAt, status)
	return
}

func (l *Libretro) loadPlatform(ctx context.Context, p platformSource) ([]catalog.MetadataRecord, error) {
	categories := []string{"no-intro", "developer", "publisher", "genre", "releaseyear"}
	parsed := map[string][]datGame{}
	for _, category := range categories {
		path := filepath.Join(l.opts.DatabaseDirectory, p.ID, category+".dat")
		if l.opts.AutoUpdate {
			u := "https://raw.githubusercontent.com/libretro/libretro-database/master/metadat/" + category + "/" + url.PathEscape(p.Database) + ".dat"
			if e := l.downloadDAT(ctx, u, path, category == "no-intro"); e != nil && category == "no-intro" {
				if _, statErr := os.Stat(path); statErr != nil {
					return nil, e
				}
			}
		}
		f, e := os.Open(path)
		if e != nil {
			if category == "no-intro" {
				if !l.opts.AutoUpdate && errors.Is(e, os.ErrNotExist) {
					return nil, errNoLocalDAT
				}
				return nil, errors.New("base DAT unavailable; enable auto_update or install it manually")
			}
			continue
		}
		games, parseErr := parseDAT(io.LimitReader(f, 96<<20))
		f.Close()
		if parseErr != nil {
			if category == "no-intro" {
				return nil, parseErr
			}
			continue
		}
		parsed[category] = games
	}
	overlays := map[string]datGame{}
	for category, games := range parsed {
		if category == "no-intro" {
			continue
		}
		for _, g := range games {
			key := strings.ToLower(g.Name)
			v := overlays[key]
			mergeDat(&v, g)
			overlays[key] = v
		}
	}
	var out []catalog.MetadataRecord
	seen := make(map[string]struct{})
	for _, g := range parsed["no-intro"] {
		mergeDat(&g, overlays[strings.ToLower(g.Name)])
		if g.SHA1 == "" && g.MD5 == "" && g.CRC32 == "" {
			continue
		}
		key := strings.ToLower(g.Name) + "\x00" + strings.ToLower(g.SHA1) + "\x00" + strings.ToLower(g.CRC32)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, catalog.MetadataRecord{Platform: p.ID, Name: g.Name, Description: g.Description, Region: g.Region, Developer: g.Developer, Publisher: g.Publisher, Genre: g.Genre, ReleaseDate: g.ReleaseDate, CRC32: g.CRC32, MD5: g.MD5, SHA1: g.SHA1})
	}
	return out, nil
}

func (l *Libretro) downloadDAT(ctx context.Context, address, path string, required bool) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	req.Header.Set("User-Agent", "ROMRelay/2")
	res, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound && !required {
		return nil
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("metadata HTTP %d", res.StatusCode)
	}
	if res.ContentLength > 96<<20 {
		return errors.New("metadata DAT exceeds 96 MiB")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".libretro-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpPath)
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(res.Body, (96<<20)+1))
	if err != nil {
		return err
	}
	if n > 96<<20 {
		return errors.New("metadata DAT exceeds 96 MiB")
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(path)
		if err = os.Rename(tmpPath, path); err != nil {
			return err
		}
	}
	ok = true
	return nil
}

func (l *Libretro) Enrich(ctx context.Context, staleBefore time.Time) (matched int, err error) {
	games, err := l.store.MetadataCandidates(ctx, staleBefore)
	if err != nil {
		return 0, err
	}
	jobs := make(chan catalog.Game)
	results := make(chan enrichOutcome)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for g := range jobs {
				select {
				case results <- l.enrichGame(ctx, g):
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(results)
		defer wg.Wait()
		for _, g := range games {
			select {
			case jobs <- g:
			case <-ctx.Done():
				close(jobs)
				return
			}
		}
		close(jobs)
	}()
	var failures []error
	done := 0
	for result := range results {
		done++
		if l.opts.Progress != nil {
			l.opts.Progress("artwork", done, len(games))
		}
		if result.matched {
			matched++
		}
		if result.err != nil {
			failures = append(failures, result.err)
		}
		if l.opts.ROMProgress != nil {
			status := "no exact match"
			if result.matched {
				status = "matched"
			}
			if result.err != nil {
				status = "failed: " + result.err.Error()
			}
			l.opts.ROMProgress(ROMProgress{Provider: "Libretro", GameID: result.game.ID, Title: result.game.Title, Status: status, Done: done, Total: len(games)})
		}
	}
	return matched, errors.Join(failures...)
}

func (l *Libretro) enrichGame(ctx context.Context, g catalog.Game) enrichOutcome {
	result := enrichOutcome{game: g}
	if g.ROMPatch || len(g.Artifacts) == 0 {
		return result
	}
	var record catalog.MetadataRecord
	var err error
	for _, a := range g.Artifacts {
		record, err = l.store.MatchLibretro(ctx, a)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			result.err = err
			break
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		names := []string{g.Title}
		for _, a := range g.Artifacts {
			names = append(names, a.Name)
		}
		record, err = l.store.MatchLibretroName(ctx, g.Platform, names...)
	}
	if err != nil {
		_ = l.store.MarkMetadataChecked(ctx, g.ID, l.now())
		return result
	}
	g.Title, g.Description, g.Region = record.Name, record.Description, record.Region
	g.MetadataSource = "libretro"
	g.Developer, g.Publisher, g.Genre, g.ReleaseDate = record.Developer, record.Publisher, record.Genre, record.ReleaseDate
	if l.opts.DownloadThumbnails {
		if path, e := l.cacheThumbnail(ctx, g.ID, g.Platform, g.Title); e == nil && path != "" {
			g.ArtworkURL = "/media/" + g.ID + "/cover"
		}
	}
	if e := l.store.UpdateGameMetadata(ctx, g, l.now()); e != nil {
		result.err = e
		return result
	}
	result.matched = true
	return result
}

func (l *Libretro) cacheThumbnail(ctx context.Context, gameID, platform, title string) (string, error) {
	path := l.ArtworkPath(gameID)
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return path, nil
	}
	repo := ""
	for _, p := range libretroPlatforms {
		if p.ID == platform {
			repo = p.ThumbnailRepo
			break
		}
	}
	if repo == "" {
		return "", nil
	}
	file := thumbnailName(title) + ".png"
	u := "https://raw.githubusercontent.com/libretro-thumbnails/" + repo + "/master/Named_Boxarts/" + url.PathEscape(file)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "ROMRelay/2")
	res, err := l.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if res.StatusCode != http.StatusOK || res.ContentLength > 8<<20 {
		return "", errors.New("thumbnail unavailable or oversized")
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(b) > 8<<20 {
		return "", errors.New("invalid thumbnail response")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "png" || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
		return "", errors.New("invalid thumbnail image")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cover-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpPath)
		}
	}()
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Close()
	}
	if err == nil {
		err = os.Rename(tmpPath, path)
	}
	if err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func (l *Libretro) ArtworkPath(gameID string) string {
	return filepath.Join(l.opts.ThumbnailDirectory, gameID+".png")
}

func thumbnailName(s string) string {
	r := strings.NewReplacer("&", "_", "*", "_", "/", "_", ":", "_", "`", "_", "<", "_", ">", "_", "?", "_", "\\", "_", "|", "_")
	return r.Replace(s)
}

type datGame struct{ Name, Description, Region, Developer, Publisher, Genre, ReleaseDate, CRC32, MD5, SHA1 string }

func mergeDat(dst *datGame, src datGame) {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.Region != "" {
		dst.Region = src.Region
	}
	if src.Developer != "" {
		dst.Developer = src.Developer
	}
	if src.Publisher != "" {
		dst.Publisher = src.Publisher
	}
	if src.Genre != "" {
		dst.Genre = src.Genre
	}
	if src.ReleaseDate != "" {
		dst.ReleaseDate = src.ReleaseDate
	}
	if src.CRC32 != "" {
		dst.CRC32 = src.CRC32
	}
	if src.MD5 != "" {
		dst.MD5 = src.MD5
	}
	if src.SHA1 != "" {
		dst.SHA1 = src.SHA1
	}
}

type datLexer struct{ r *bufio.Reader }

func (l *datLexer) token() (string, error) {
	for {
		c, _, e := l.r.ReadRune()
		if e != nil {
			return "", e
		}
		if !strings.ContainsRune(" \t\r\n", c) {
			l.r.UnreadRune()
			break
		}
	}
	c, _, e := l.r.ReadRune()
	if e != nil {
		return "", e
	}
	if c == '(' || c == ')' {
		return string(c), nil
	}
	if c == '"' {
		var b strings.Builder
		for {
			c, _, e = l.r.ReadRune()
			if e != nil {
				return "", e
			}
			if c == '"' {
				break
			}
			if c == '\\' {
				n, _, x := l.r.ReadRune()
				if x != nil {
					return "", x
				}
				b.WriteRune(n)
			} else {
				b.WriteRune(c)
			}
		}
		return b.String(), nil
	}
	var b strings.Builder
	b.WriteRune(c)
	for {
		c, _, e = l.r.ReadRune()
		if e == io.EOF {
			return b.String(), nil
		}
		if e != nil {
			return "", e
		}
		if strings.ContainsRune(" \t\r\n()", c) {
			l.r.UnreadRune()
			return b.String(), nil
		}
		b.WriteRune(c)
	}
}
func parseDAT(r io.Reader) ([]datGame, error) {
	l := &datLexer{bufio.NewReaderSize(r, 64<<10)}
	var out []datGame
	for {
		tok, e := l.token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if tok != "game" {
			continue
		}
		open, e := l.token()
		if e != nil || open != "(" {
			continue
		}
		obj, e := parseDatObject(l)
		if e != nil {
			return nil, e
		}
		g := datGame{Name: first(obj, "name"), Description: first(obj, "description"), Region: first(obj, "region"), Developer: first(obj, "developer"), Publisher: first(obj, "publisher"), Genre: first(obj, "genre"), ReleaseDate: first(obj, "releaseyear")}
		if g.ReleaseDate == "" {
			g.ReleaseDate = first(obj, "release_year")
		}
		if roms := obj["rom"]; len(roms) > 0 {
			if m, ok := roms[0].(map[string][]any); ok {
				g.CRC32 = first(m, "crc")
				g.MD5 = first(m, "md5")
				g.SHA1 = first(m, "sha1")
			}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func parseDatObject(l *datLexer) (map[string][]any, error) {
	m := map[string][]any{}
	for {
		k, e := l.token()
		if e != nil {
			return nil, e
		}
		if k == ")" {
			return m, nil
		}
		v, e := l.token()
		if e != nil {
			return nil, e
		}
		if v == "(" {
			child, e := parseDatObject(l)
			if e != nil {
				return nil, e
			}
			m[k] = append(m[k], child)
		} else {
			m[k] = append(m[k], v)
		}
	}
}
func first(m map[string][]any, k string) string {
	if a := m[k]; len(a) > 0 {
		if s, ok := a[0].(string); ok {
			return s
		}
	}
	return ""
}
