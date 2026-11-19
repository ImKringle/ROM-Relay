package web

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"romrelay/internal/catalog"
	"romrelay/internal/romm"
)

// JSON for the library and game pages of the React UI. The filtering,
// merging and facet logic is the same code the server-rendered pages used.

// libraryCard is one grid card.
type libraryCard struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Platform      string `json:"platform"`
	PlatformLabel string `json:"platform_label"`
	Region        string `json:"region,omitempty"`
	Genre         string `json:"genre,omitempty"`
	Year          string `json:"year,omitempty"`
	Patched       bool   `json:"patched,omitempty"`
	Files         int    `json:"files"`
	Cover         string `json:"cover,omitempty"`
}

type facetJSON struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Count   int    `json:"count"`
	Checked bool   `json:"checked"`
}

type libraryResponse struct {
	Source        string                 `json:"source"`
	Games         []libraryCard          `json:"games"`
	Total         int                    `json:"total"`
	Limit         int                    `json:"limit"`
	Facets        map[string][]facetJSON `json:"facets"`
	HasEncryption bool                   `json:"has_encryption"`
	RemoteSearch  bool                   `json:"remote_search,omitempty"`
}

// libraryPageSize caps cards per response; the UI asks for more as it scrolls.
const libraryPageSize = 120

func facetsJSON(values []facetValue) []facetJSON {
	out := make([]facetJSON, 0, len(values))
	for _, v := range values {
		out = append(out, facetJSON{Value: v.Value, Label: v.Label, Count: v.Count, Checked: v.Checked})
	}
	return out
}

func cardFor(g catalog.Game, cover string) libraryCard {
	return libraryCard{
		ID: g.ID, Title: displayTitle(g.Title), Platform: g.Platform, PlatformLabel: platformLabel(g.Platform),
		Region: g.Region, Genre: g.Genre, Year: g.ReleaseDate, Patched: g.ROMPatch, Files: len(g.Artifacts), Cover: cover,
	}
}

// apiLibrary: GET /api/library?source=romm&q=&platform=&status=&show_encrypted=1&offset=
func (s *CatalogServer) apiLibrary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var games []catalog.Game
	source := "local"
	if q.Get("source") == "romm" {
		if !s.rommViewOK(r) {
			http.Error(w, "RomM is not available", http.StatusForbidden)
			return
		}
		roms, err := s.romm.ROMs(r.Context(), false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for _, g := range rommGroups(roms) {
			games = append(games, rommGroupGame(g))
		}
		// Games with art first: a bare filename card is rarely what anyone wants.
		sort.SliceStable(games, func(i, j int) bool { return games[i].ArtworkURL != "" && games[j].ArtworkURL == "" })
		source = "romm"
	} else {
		all, err := s.store.Games(r.Context())
		if err != nil {
			http.Error(w, "catalog unavailable", http.StatusInternalServerError)
			return
		}
		games = mergeGames(all)
	}
	filter := parseCatalogFilter(q)
	filtered := filterGames(games, filter)
	facets := catalogFacets(games, filter)
	offset, _ := strconv.Atoi(q.Get("offset"))
	offset = min(max(offset, 0), len(filtered))
	page := filtered[offset:min(offset+libraryPageSize, len(filtered))]
	resp := libraryResponse{Source: source, Total: len(filtered), Limit: libraryPageSize, Games: make([]libraryCard, 0, len(page)),
		Facets: map[string][]facetJSON{"platform": facetsJSON(facets.Platforms)}, HasEncryption: facets.HasEncryption}
	if source == "local" {
		resp.Facets["status"] = facetsJSON(facets.Statuses)
		resp.RemoteSearch = s.remote != nil && filter.Query != "" && len(filtered) == 0
	}
	tq := tokenQuery(r)
	for _, g := range page {
		cover := ""
		if g.ArtworkURL != "" {
			cover = "/media/" + g.ID + "/cover" + tq
		}
		resp.Games = append(resp.Games, cardFor(g, cover))
	}
	writeJSON(w, http.StatusOK, resp)
}

// gameFile is one downloadable form of a local game.
type gameFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Extension   string `json:"extension"`
	Size        int64  `json:"size"`
	Encryption  string `json:"encryption,omitempty"`
	Patched     bool   `json:"patched,omitempty"`
	DownloadURL string `json:"download_url"`
	DirectURL   string `json:"direct_url"`
	QRURL       string `json:"qr_url"`
}

type gameDetail struct {
	Source       string     `json:"source"`
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Platform     string     `json:"platform"`
	PlatformName string     `json:"platform_label"`
	Description  string     `json:"description,omitempty"`
	SynopsisNote string     `json:"synopsis_note,omitempty"`
	Developer    string     `json:"developer,omitempty"`
	Publisher    string     `json:"publisher,omitempty"`
	Released     string     `json:"released,omitempty"`
	Genre        string     `json:"genre,omitempty"`
	Region       string     `json:"region,omitempty"`
	Players      string     `json:"players,omitempty"`
	Rating       string     `json:"rating,omitempty"`
	Metadata     string     `json:"metadata_source,omitempty"`
	Patched      bool       `json:"patched,omitempty"`
	Cover        string     `json:"cover,omitempty"`
	Screenshots  []string   `json:"screenshots,omitempty"`
	Files        []gameFile `json:"files,omitempty"`
	Versions     []rommFile `json:"versions,omitempty"`
}

// rommFile is one RomM version of a stacked game.
type rommFile struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Size      int64    `json:"size"`
	Regions   []string `json:"regions,omitempty"`
	Languages []string `json:"languages,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Grabbable bool     `json:"grabbable"`
}

// apiGame: GET /api/library/{id}. "romm-<n>" ids answer from the RomM cache.
func (s *CatalogServer) apiGame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.HasPrefix(id, rommIDPrefix) {
		s.apiRommGame(w, r, id)
		return
	}
	g, err := s.store.Game(r.Context(), id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	if all, err := s.store.Games(r.Context()); err == nil {
		if merged, ok := mergeSiblings(all, g.ID); ok {
			g = merged
		}
	}
	d := gameDetail{Source: "local", ID: g.ID, Title: displayTitle(g.Title), Platform: g.Platform, PlatformName: platformLabel(g.Platform),
		Description: g.Description, Developer: g.Developer, Publisher: g.Publisher, Released: g.ReleaseDate, Genre: g.Genre,
		Region: g.Region, Players: g.Players, Rating: g.Rating, Metadata: g.MetadataSource, Patched: g.ROMPatch}
	if g.Description == "" {
		d.SynopsisNote = s.synopsisNote(g)
	}
	tq := tokenQuery(r)
	if g.ArtworkURL != "" {
		d.Cover = "/media/" + g.ID + "/cover" + tq
	}
	for _, a := range g.Artifacts {
		d.Files = append(d.Files, gameFile{ID: a.ID, Name: a.Name, Extension: a.Extension, Size: a.Size, Encryption: a.Encryption, Patched: a.Patched,
			DownloadURL: s.downloadURL(r, a), DirectURL: s.withToken("/d/" + a.ID), QRURL: "/qr/" + a.ID + ".png" + tq})
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *CatalogServer) apiRommGame(w http.ResponseWriter, r *http.Request, id string) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, rommIDPrefix))
	if err != nil || !s.rommViewOK(r) {
		http.NotFound(w, r)
		return
	}
	roms, err := s.romm.ROMs(r.Context(), false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	rom, ok := s.romm.Cached(n)
	if !ok {
		http.NotFound(w, r)
		return
	}
	group := []romm.ROM{rom}
	key := rommGroupKey(rom)
	for _, g := range rommGroups(roms) {
		if rommGroupKey(g[0]) == key {
			group = g
			break
		}
	}
	game := rommGroupGame(group)
	lead := group[0]
	d := gameDetail{Source: "romm", ID: game.ID, Title: game.Title, Platform: game.Platform, PlatformName: lead.Platform,
		Description: game.Description, Developer: game.Developer, Publisher: game.Publisher, Released: game.ReleaseDate,
		Genre: game.Genre, Region: game.Region, Players: game.Players, Rating: game.Rating, Metadata: "RomM"}
	if lead.CoverPath(true) != "" {
		d.Cover = "/api/romm/cover/" + strconv.Itoa(lead.ID) + "?large=1"
	}
	for i, p := range lead.Screens {
		if romm.ResourcePath(p) {
			d.Screenshots = append(d.Screenshots, "/api/romm/shot/"+strconv.Itoa(lead.ID)+"/"+strconv.Itoa(i))
		}
	}
	for _, v := range group {
		d.Versions = append(d.Versions, rommFile{ID: v.ID, Name: v.FSName, Size: v.Size, Regions: v.Regions, Languages: v.Languages, Tags: v.Tags, Grabbable: rommGrabbable(v)})
	}
	writeJSON(w, http.StatusOK, d)
}

// apiDownloadChoice backs the encrypted-CIA chooser: the original encrypted
// file (FBI, real 3DS) and, if one exists, the prepared unencrypted copy
// (mobile emulators).
func (s *CatalogServer) apiDownloadChoice(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.Artifact(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resp := map[string]any{"id": a.ID, "game_id": a.GameID, "name": a.Name, "encrypted": a.Encryption == "encrypted",
		"encrypted_url": s.withToken("/d/" + a.ID), "admin": s.adminOK(r)}
	if g, err := s.store.Game(r.Context(), a.GameID); err == nil {
		for _, sib := range g.Artifacts {
			if sib.Encryption == "unencrypted" {
				resp["mobile_url"] = s.withToken("/d/" + sib.ID)
				break
			}
		}
	}
	ready, reason := false, "driver is not installed"
	if s.decryptor != nil {
		ready, reason = s.decryptor.Available()
	}
	resp["decrypt_ready"], resp["decrypt_reason"] = ready, reason
	writeJSON(w, http.StatusOK, resp)
}
