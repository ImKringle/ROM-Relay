package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"romrelay/internal/catalog"
)

type IGDBOptions struct {
	ClientID, ClientSecret, ThumbnailDirectory string
	ROMProgress                                func(ROMProgress)
}
type IGDB struct {
	store                       *catalog.Store
	opts                        IGDBOptions
	client                      *http.Client
	tokenURL, apiURL, imageBase string
	mu                          sync.Mutex
	token                       string
	tokenExpiry                 time.Time
	now                         func() time.Time
	sleep                       func(context.Context, time.Duration) error
	lastRequest                 time.Time
}

func NewIGDB(store *catalog.Store, opts IGDBOptions) *IGDB {
	return &IGDB{store: store, opts: opts, client: &http.Client{Timeout: 30 * time.Second}, tokenURL: "https://id.twitch.tv/oauth2/token", apiURL: "https://api.igdb.com/v4/games", imageBase: "https://images.igdb.com/igdb/image/upload/t_cover_big/", now: time.Now, sleep: sleepContext}
}

type igdbRateLimitError struct{ retryAfter time.Duration }

func (e *igdbRateLimitError) Error() string {
	return fmt.Sprintf("IGDB rate limited; retry after %s", e.retryAfter.Round(time.Second))
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (i *IGDB) Enrich(ctx context.Context, staleBefore time.Time) (updated int, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	defer func() {
		status := fmt.Sprintf("%d games enriched", updated)
		if err != nil {
			status += "; " + err.Error()
		}
		_ = i.store.SetSchedulerState(context.Background(), "igdb_update", i.now(), status)
	}()
	games, err := i.store.IGDBCandidates(ctx, staleBefore)
	if err != nil {
		return 0, err
	}
	var failures []error
	done := 0
	for _, batch := range igdbPlatformBatches(games, 10) {
		for _, g := range batch {
			i.report(g, "querying", done, len(games))
		}
		found, batchErr := i.lookupBatch(ctx, batch)
		if batchErr != nil {
			var limited *igdbRateLimitError
			if errors.As(batchErr, &limited) {
				for _, g := range batch {
					done++
					i.report(g, "rate limited", done, len(games))
				}
				return updated, limited
			}
			for _, g := range batch {
				done++
				i.report(g, "failed: "+batchErr.Error(), done, len(games))
			}
			failures = append(failures, batchErr)
			continue
		}
		for n := range batch {
			g, remote := batch[n], found[n]
			if g.ROMPatch {
				continue
			}
			done++
			if remote == nil {
				_ = i.store.MarkMetadataChecked(ctx, g.ID, i.now())
				i.report(g, "no exact match", done, len(games))
				continue
			}
			applyIGDB(&g, *remote)
			if g.ArtworkURL == "" && remote.Cover.ImageID != "" {
				if path, imageErr := i.cacheImage(ctx, g.ID, remote.Cover.ImageID); imageErr == nil && path != "" {
					g.ArtworkURL = "/media/" + g.ID + "/cover"
				}
			}
			if updateErr := i.store.UpdateGameMetadata(ctx, g, i.now()); updateErr != nil {
				failures = append(failures, updateErr)
				i.report(g, "failed: "+updateErr.Error(), done, len(games))
				continue
			}
			updated++
			i.report(g, "matched", done, len(games))
		}
	}
	return updated, errors.Join(failures...)
}

func (i *IGDB) report(g catalog.Game, status string, done, total int) {
	if i.opts.ROMProgress != nil {
		i.opts.ROMProgress(ROMProgress{Provider: "IGDB", GameID: g.ID, Title: g.Title, Status: status, Done: done, Total: total})
	}
}

const igdbFields = "name,slug,summary,first_release_date,rating,genres.name,involved_companies.developer,involved_companies.publisher,involved_companies.company.name,cover.image_id,screenshots.image_id"

type igdbMultiResult struct {
	Name   string     `json:"name"`
	Result []igdbGame `json:"result"`
}

func igdbPlatformBatches(games []catalog.Game, size int) [][]catalog.Game {
	if size < 1 {
		size = 10
	}
	byPlatform := make(map[string][]catalog.Game)
	var order []string
	for _, g := range games {
		if _, ok := byPlatform[g.Platform]; !ok {
			order = append(order, g.Platform)
		}
		byPlatform[g.Platform] = append(byPlatform[g.Platform], g)
	}
	var batches [][]catalog.Game
	for _, platform := range order {
		group := byPlatform[platform]
		for start := 0; start < len(group); start += size {
			end := start + size
			if end > len(group) {
				end = len(group)
			}
			batches = append(batches, group[start:end])
		}
	}
	return batches
}

func (i *IGDB) lookupBatch(ctx context.Context, games []catalog.Game) ([]*igdbGame, error) {
	out := make([]*igdbGame, len(games))
	if len(games) == 0 {
		return out, nil
	}
	token, err := i.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	platformIDs := map[string]int{"n64": 4, "nes": 18, "snes": 19, "nds": 20, "gbc": 22, "gba": 24, "genesis": 29, "gb": 33, "3ds": 37}
	platformID := platformIDs[games[0].Platform]
	if platformID == 0 {
		return out, nil
	}
	slugOwners := make(map[string]int)
	var slugs []string
	for n, g := range games {
		if g.ROMPatch || g.Platform != games[0].Platform {
			continue
		}
		for _, slug := range igdbSlugCandidates(igdbLookupTitle(g)) {
			if _, exists := slugOwners[slug]; exists {
				continue
			}
			slugOwners[slug] = n
			slugs = append(slugs, slug)
		}
	}
	if len(slugs) == 0 {
		return out, nil
	}
	quoted := make([]string, len(slugs))
	for n, slug := range slugs {
		quoted[n] = strconv.Quote(slug)
	}
	body := fmt.Sprintf(`fields %s; where platforms = (%d) & slug = (%s); limit %d;`, igdbFields, platformID, strings.Join(quoted, ","), len(slugs))
	b, err := i.doAPI(ctx, i.apiURL, token, body)
	if err != nil {
		return nil, err
	}
	var found []igdbGame
	if err = json.Unmarshal(b, &found); err != nil {
		return nil, errors.New("invalid IGDB batch JSON")
	}
	for n := range found {
		owner, ok := slugOwners[found[n].Slug]
		if !ok {
			continue
		}
		match := found[n]
		out[owner] = &match
	}
	// Slugs are fast and batch well, but IGDB occasionally disambiguates a slug
	// with a numeric suffix. Search only the misses, still through doAPI's global
	// limiter, and retain the same exact normalized/platform-scoped acceptance.
	for n, g := range games {
		if out[n] != nil || g.ROMPatch {
			continue
		}
		match, lookupErr := i.lookup(ctx, igdbLookupTitle(g), g.Platform)
		if lookupErr != nil {
			return nil, lookupErr
		}
		out[n] = match
	}
	return out, nil
}

type igdbGame struct {
	Name         string  `json:"name"`
	Slug         string  `json:"slug"`
	Summary      string  `json:"summary"`
	FirstRelease int64   `json:"first_release_date"`
	Rating       float64 `json:"rating"`
	Genres       []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Companies []struct {
		Developer bool `json:"developer"`
		Publisher bool `json:"publisher"`
		Company   struct {
			Name string `json:"name"`
		} `json:"company"`
	} `json:"involved_companies"`
	Cover struct {
		ImageID string `json:"image_id"`
	} `json:"cover"`
	Screenshots []struct {
		ImageID string `json:"image_id"`
	} `json:"screenshots"`
}

func (i *IGDB) lookup(ctx context.Context, title, platform string) (*igdbGame, error) {
	platformID := map[string]int{"n64": 4, "nes": 18, "snes": 19, "nds": 20, "gbc": 22, "gba": 24, "genesis": 29, "gb": 33, "3ds": 37}[platform]
	if platformID == 0 {
		return nil, nil
	}
	token, err := i.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	clean := strings.ReplaceAll(title, "\"", "\\\"")
	body := fmt.Sprintf(`search "%s"; fields %s; where platforms = (%d); limit 10;`, clean, igdbFields, platformID)
	b, err := i.doAPI(ctx, i.apiURL, token, body)
	if err != nil {
		return nil, err
	}
	var found []igdbGame
	if json.Unmarshal(b, &found) != nil {
		return nil, errors.New("invalid IGDB JSON")
	}
	wants := make(map[string]bool)
	for _, slug := range igdbSlugCandidates(title) {
		wants[slug] = true
	}
	for n := range found {
		remoteSlug := found[n].Slug
		if remoteSlug == "" {
			remoteSlug = strings.ReplaceAll(normalizeTitle(found[n].Name), " ", "-")
		}
		if wants[remoteSlug] {
			return &found[n], nil
		}
		if titleTokenSignature(title) == titleTokenSignature(found[n].Name) {
			return &found[n], nil
		}
	}
	return nil, nil
}

func (i *IGDB) doAPI(ctx context.Context, endpoint, token, body string) ([]byte, error) {
	var b []byte
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if wait := 350*time.Millisecond - time.Since(i.lastRequest); wait > 0 && !i.lastRequest.IsZero() {
			if err = i.sleep(ctx, wait); err != nil {
				return nil, err
			}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
		req.Header.Set("Client-ID", i.opts.ClientID)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		i.lastRequest = time.Now()
		res, requestErr := i.client.Do(req)
		if requestErr != nil {
			return nil, requestErr
		}
		if res.StatusCode == http.StatusTooManyRequests {
			delay := retryAfter(res.Header.Get("Retry-After"), time.Now())
			res.Body.Close()
			if delay < 0 {
				delay = time.Duration(1<<attempt) * time.Second
			}
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			if attempt == 2 {
				return nil, &igdbRateLimitError{retryAfter: delay}
			}
			if err = i.sleep(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			return nil, fmt.Errorf("IGDB HTTP %d", res.StatusCode)
		}
		b, err = io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
		res.Body.Close()
		break
	}
	if err != nil || len(b) > 2<<20 {
		return nil, errors.New("IGDB response invalid or oversized")
	}
	return b, nil
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return -1
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		d := when.Sub(now)
		if d < 0 {
			return 0
		}
		return d
	}
	return -1
}

func (i *IGDB) accessToken(ctx context.Context) (string, error) {
	if i.token != "" && i.now().Add(time.Minute).Before(i.tokenExpiry) {
		return i.token, nil
	}
	u, _ := url.Parse(i.tokenURL)
	q := u.Query()
	q.Set("client_id", i.opts.ClientID)
	q.Set("client_secret", i.opts.ClientSecret)
	q.Set("grant_type", "client_credentials")
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	res, err := i.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("Twitch OAuth HTTP %d", res.StatusCode)
	}
	var reply struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&reply) != nil || reply.AccessToken == "" {
		return "", errors.New("invalid Twitch OAuth response")
	}
	i.token = reply.AccessToken
	i.tokenExpiry = i.now().Add(time.Duration(reply.ExpiresIn) * time.Second)
	return i.token, nil
}

func applyIGDB(g *catalog.Game, r igdbGame) {
	if g.MetadataSource == "libretro" {
		g.MetadataSource = "libretro+igdb"
	} else {
		g.MetadataSource = "igdb"
	}
	if r.Name != "" {
		g.Title = r.Name
	}
	if r.Summary != "" {
		g.Description = r.Summary
	}
	if r.FirstRelease > 0 {
		g.ReleaseDate = time.Unix(r.FirstRelease, 0).UTC().Format("2006-01-02")
	}
	if r.Rating > 0 {
		g.Rating = fmt.Sprintf("%.1f", r.Rating)
	}
	var genres []string
	for _, v := range r.Genres {
		if v.Name != "" {
			genres = append(genres, v.Name)
		}
	}
	if len(genres) > 0 {
		g.Genre = strings.Join(genres, ", ")
	}
	for _, v := range r.Companies {
		if v.Developer && g.Developer == "" {
			g.Developer = v.Company.Name
		}
		if v.Publisher && g.Publisher == "" {
			g.Publisher = v.Company.Name
		}
	}
}

func (i *IGDB) cacheImage(ctx context.Context, gameID, imageID string) (string, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(imageID) {
		return "", errors.New("invalid IGDB image ID")
	}
	path := filepath.Join(i.opts.ThumbnailDirectory, gameID+".jpg")
	if st, e := os.Stat(path); e == nil && st.Size() > 0 {
		return path, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, i.imageBase+imageID+".jpg", nil)
	res, e := i.client.Do(req)
	if e != nil {
		return "", e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.ContentLength > 8<<20 {
		return "", errors.New("IGDB image unavailable or oversized")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if e != nil || len(b) > 8<<20 {
		return "", errors.New("invalid IGDB image")
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || (format != "jpeg" && format != "png") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
		return "", errors.New("invalid IGDB image")
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return "", e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".igdb-*.tmp")
	if e != nil {
		return "", e
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpPath)
		}
	}()
	if _, e = tmp.Write(b); e == nil {
		e = tmp.Sync()
	}
	if e == nil {
		e = tmp.Close()
	}
	if e == nil {
		e = os.Rename(tmpPath, path)
	}
	if e != nil {
		return "", e
	}
	ok = true
	return path, nil
}

var suffixes = regexp.MustCompile(`\s*(?:\([^)]*\)|\[[^]]*\])\s*`)
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func igdbLookupTitle(g catalog.Game) string {
	best := normalizeTitle(g.Title)
	if strings.Contains(g.MetadataSource, "libretro") {
		return best
	}
	for _, a := range g.Artifacts {
		candidate := normalizeTitle(strings.TrimSuffix(a.Name, filepath.Ext(a.Name)))
		if len(candidate) > len(best) {
			best = candidate
		}
	}
	return best
}

func normalizeTitle(v string) string {
	v = norm.NFD.String(strings.ToLower(suffixes.ReplaceAllString(v, " ")))
	v = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, v)
	return strings.Trim(nonAlnum.ReplaceAllString(v, " "), " ")
}

func igdbSlugCandidates(title string) []string {
	title = normalizeTitle(title)
	variants := []string{title}
	for _, suffix := range []string{" version", " edition"} {
		if strings.HasSuffix(title, suffix) {
			variants = append(variants, strings.TrimSuffix(title, suffix))
		}
	}
	if strings.HasPrefix(title, "pokemon ") && !strings.HasSuffix(title, " version") {
		variants = append(variants, title+" version")
	}
	if title == "pokemon yellow" || title == "pokemon yellow version" {
		variants = append(variants, "pokemon yellow version special pikachu edition")
	}
	seen := make(map[string]bool)
	out := make([]string, 0, len(variants))
	for _, variant := range variants {
		slug := strings.ReplaceAll(strings.TrimSpace(variant), " ", "-")
		if slug != "" && !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	return out
}

func titleTokenSignature(title string) string {
	tokens := strings.Fields(normalizeTitle(title))
	sort.Strings(tokens)
	return strings.Join(tokens, " ")
}
