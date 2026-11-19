package metadata

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/romm"
)

// RomM enriches library games from a linked RomM instance when a ROM's md5
// or sha1 matches one RomM holds: its summary, genres, companies, release
// year, regions and cover. It reuses RomM's cached listing, so a sweep costs
// no RomM calls beyond the daily listing and one fetch per new cover.
type RomM struct {
	store    *catalog.Store
	client   *romm.Client
	mediaDir string
	now      func() time.Time
}

func NewRomM(store *catalog.Store, client *romm.Client, mediaDir string) *RomM {
	return &RomM{store: store, client: client, mediaDir: mediaDir, now: time.Now}
}

// Enrich runs before IGDB and takes the same candidates (no description, or
// stale), so IGDB only queries what RomM could not fill.
func (m *RomM) Enrich(ctx context.Context, staleBefore time.Time) (int, error) {
	roms, err := m.client.ROMs(ctx, false)
	if err != nil {
		return 0, err
	}
	byHash := map[string]romm.ROM{}
	for _, r := range roms {
		for _, h := range r.Hashes() {
			byHash[h] = r
		}
	}
	games, err := m.store.IGDBCandidates(ctx, staleBefore)
	if err != nil {
		return 0, err
	}
	updated := 0
	var failures []error
	for _, g := range games {
		r, ok := matchRomM(g, byHash)
		if !ok || g.ROMPatch {
			continue
		}
		if !applyRomM(&g, r) {
			continue
		}
		if g.ArtworkURL == "" && m.cacheCover(ctx, g.ID, r) {
			g.ArtworkURL = "/media/" + g.ID + "/cover"
		}
		if err := m.store.UpdateGameMetadata(ctx, g, m.now()); err != nil {
			failures = append(failures, err)
			continue
		}
		updated++
	}
	return updated, errors.Join(failures...)
}

func matchRomM(g catalog.Game, byHash map[string]romm.ROM) (romm.ROM, bool) {
	for _, a := range g.Artifacts {
		for _, h := range []string{a.MD5, a.SHA1} {
			if r, ok := byHash[strings.ToLower(h)]; ok && h != "" {
				return r, true
			}
		}
	}
	return romm.ROM{}, false
}

// applyRomM copies RomM's fields over the game's and reports whether RomM
// had anything to give. A hash match is exact, so RomM's curated name also
// replaces a header-derived title like "GAME".
func applyRomM(g *catalog.Game, r romm.ROM) bool {
	changed := false
	set := func(dst *string, v string) {
		if v != "" && *dst != v {
			*dst, changed = v, true
		}
	}
	set(&g.Title, r.Name)
	set(&g.Description, r.Summary)
	set(&g.Genre, strings.Join(r.Metadatum.Genres, ", "))
	if len(r.Metadatum.Companies) > 0 {
		set(&g.Developer, r.Metadatum.Companies[0])
	}
	if g.ReleaseDate == "" {
		set(&g.ReleaseDate, r.Year())
	}
	if g.Region == "" {
		set(&g.Region, strings.Join(r.Regions, ", "))
	}
	if changed && !strings.Contains(g.MetadataSource, "romm") {
		if g.MetadataSource == "" {
			g.MetadataSource = "romm"
		} else {
			g.MetadataSource += "+romm"
		}
	}
	return changed || (g.ArtworkURL == "" && r.CoverPath(true) != "")
}

// cacheCover saves RomM's cover where /media/{id}/cover serves it from.
func (m *RomM) cacheCover(ctx context.Context, gameID string, r romm.ROM) bool {
	p := r.CoverPath(true)
	if p == "" || m.mediaDir == "" {
		return false
	}
	resp, err := m.client.Get(ctx, p, false)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}[strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]]
	if ext == "" {
		return false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(b) == 0 {
		return false
	}
	if os.MkdirAll(m.mediaDir, 0o755) != nil {
		return false
	}
	return os.WriteFile(filepath.Join(m.mediaDir, gameID+ext), b, 0o644) == nil
}
