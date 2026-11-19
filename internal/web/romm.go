package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/romm"
	"romrelay/internal/torrent"
)

// RomM: signed-in users search a linked RomM library and grab single ROMs
// into this one, like the Torrents page. The browser only ever talks to the
// relay; internal/romm holds the token and the cached listing.

// SetRomm enables the RomM page. libraryDir is the library root grabs land in.
func (s *CatalogServer) SetRomm(client *romm.Client, libraryDir string) {
	s.romm, s.rommLibrary = client, libraryDir
}

// RommTempRoot is where unretained RomM grabs go; an external artifact root,
// so they expire like torrent grabs and patch output.
func RommTempRoot(library string) string { return filepath.Join(library, "external", "romm") }

// rommPlatformIDs maps RomM platform slugs that differ from ROM Relay's
// platform ids; the rest (gb, gba, nds, snes, ps2, psp, switch...) match.
var rommPlatformIDs = map[string]string{
	"ngc": "gc", "gamecube": "gc", "psx": "ps1", "ps": "ps1",
	"genesis-slash-megadrive": "genesis", "megadrive": "genesis", "sfam": "snes",
	"nintendo-dsi": "nds", "new-nintendo-3ds": "3ds", "famicom": "nes",
}

// rommPlatform is the ROM Relay platform a RomM ROM belongs to. RomM knows
// the platform outright, which is what makes shared extensions (.iso) safe.
func rommPlatform(r romm.ROM) (catalog.Platform, bool) {
	id := r.PlatSlug
	if mapped, ok := rommPlatformIDs[id]; ok {
		id = mapped
	}
	return catalog.PlatformByID(id)
}

// rommGrabbable: ROM Relay has the platform, and the file is either an
// archive (unpacked on arrival) or one of that platform's extensions.
func rommGrabbable(r romm.ROM) bool {
	p, ok := rommPlatform(r)
	return ok && (r.Archive() || p.Has(strings.ToLower(filepath.Ext(r.FSName))))
}

func (s *CatalogServer) rommUserOK(w http.ResponseWriter, r *http.Request) bool {
	if currentUser(r) == nil && !s.adminOK(r) {
		http.Error(w, "sign in to use RomM", http.StatusUnauthorized)
		return false
	}
	if s.romm == nil {
		http.Error(w, "RomM is not configured on this relay (set ROMM_URL and ROMM_TOKEN)", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// refreshRomm reloads the cached RomM listing now. Cached covers stay: they
// are keyed by RomM id and RomM rarely changes art, so a refresh must not
// turn into thousands of image refetches.
func (s *CatalogServer) refreshRomm(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.romm == nil {
		http.Error(w, "RomM is not configured", http.StatusServiceUnavailable)
		return
	}
	roms, err := s.romm.ROMs(r.Context(), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": len(roms)})
}


// rommCoverDir caches covers under the library in a folder the catalog scan
// ignores (its top-level name is not a platform).
func (s *CatalogServer) rommCoverDir() string { return filepath.Join(s.rommLibrary, ".romm", "covers") }

// rommCover serves a cover (?large=1 for the big one) through the disk
// cache. Only ids in the cached listing are served.
func (s *CatalogServer) rommCover(w http.ResponseWriter, r *http.Request) {
	if !s.rommUserOK(w, r) {
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(r.PathValue("id"), rommIDPrefix))
	rom, ok := s.romm.Cached(id)
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	large := r.URL.Query().Get("large") == "1"
	p := rom.CoverPath(large)
	if p == "" {
		http.NotFound(w, r)
		return
	}
	name := strconv.Itoa(id)
	if large {
		name += "-l"
	}
	s.serveRommImage(w, r, name, p)
}

// serveRommImage serves a RomM resource from the relay's disk cache,
// fetching it once on first use. resource must pass romm.ResourcePath.
func (s *CatalogServer) serveRommImage(w http.ResponseWriter, r *http.Request, name, resource string) {
	cached := filepath.Join(s.rommCoverDir(), name)
	w.Header().Set("Cache-Control", "private, max-age=2592000") // 30 days; the disk copy is permanent
	if f, err := os.Open(cached); err == nil {
		defer f.Close()
		http.ServeContent(w, r, "", time.Time{}, f)
		return
	}
	resp, err := s.romm.Get(r.Context(), resource, false)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || !strings.HasPrefix(ct, "image/") {
		http.NotFound(w, r)
		return
	}
	if os.MkdirAll(filepath.Dir(cached), 0o755) == nil {
		_ = os.WriteFile(cached, b, 0o644)
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(b)
}

// grabRomm downloads one RomM ROM into the library in the background.
// Single files go straight to their platform folder; archives (zips and
// RomM's multi-file bundles) are unzipped when they hold only ROMs.
func (s *CatalogServer) grabRomm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if !s.rommUserOK(w, r) {
		return
	}
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	rom, err := s.romm.ROM(r.Context(), id) // live: the cache may be a week old
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	name := path.Base(strings.ReplaceAll(rom.FSName, `\`, "/"))
	if name == "." || name == "/" || name == "" {
		http.Error(w, "RomM returned no file name", http.StatusBadGateway)
		return
	}
	retain := r.FormValue("retain") == "true" && s.adminOK(r)
	root := s.rommLibrary
	if !retain {
		root = RommTempRoot(s.rommLibrary)
	}
	plat, ok := rommPlatform(rom)
	if !ok || !rommGrabbable(rom) {
		http.Error(w, "ROM Relay does not support "+rom.Platform+" "+filepath.Ext(name)+" files", http.StatusUnprocessableEntity)
		return
	}
	var dest string // final ROM path; blank for archives
	if !rom.Archive() {
		dest = filepath.Join(root, plat.Directory, name)
		if _, err := os.Stat(dest); err == nil {
			http.Error(w, "already in the library", http.StatusConflict)
			return
		}
	}
	key := "RomM · " + strings.TrimSuffix(name, path.Ext(name))
	s.SetBackground(key, BackgroundJob{Active: true, Message: "Starting…", Total: max(rom.Size, 1)})
	go s.rommDownload(key, rom, plat, name, root, dest)
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": name, "retained": retain, "job": key})
}

func (s *CatalogServer) rommDownload(key string, rom romm.ROM, plat catalog.Platform, name, root, dest string) {
	fail := func(err error) {
		s.SetBackground(key, BackgroundJob{Recent: true, Message: "Download failed", Error: err.Error()})
	}
	ctx := context.Background()
	resp, err := s.romm.Content(ctx, rom.ID, name)
	if err != nil {
		fail(err)
		return
	}
	defer resp.Body.Close()
	total := rom.Size
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	dir := filepath.Join(s.rommLibrary, ".incoming") // ignored by the catalog scan
	if dest != "" {
		dir = filepath.Dir(dest)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
		return
	}
	// No ROM extension on the temp name, so a scan mid-download never
	// catalogues a partial file.
	// Archives keep their extension (the unpacker picks zip/7z by it); RomM
	// serves multi-file games as a zip.
	ext := ""
	if dest == "" {
		ext = ".zip"
		if torrent.IsArchive(name) {
			ext = strings.ToLower(filepath.Ext(name))
		}
	}
	tmp, err := os.CreateTemp(dir, ".romm-*"+ext)
	if err != nil {
		fail(err)
		return
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	pr := &progressReader{r: resp.Body, report: func(n int64) {
		s.SetBackground(key, BackgroundJob{Active: true, Message: humanBytes(n) + " of " + humanBytes(total), Done: n, Total: max(total, 1)})
	}}
	_, err = io.Copy(tmp, pr)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		log.Printf("romm: download %d: %v", rom.ID, err)
		fail(errors.New("download interrupted"))
		return
	}
	written := 1
	if dest != "" {
		err = os.Rename(tmp.Name(), dest)
	} else {
		parked := name
		if !torrent.IsArchive(name) {
			parked += ext
		}
		written, err = s.rommExtract(tmp.Name(), root, parked, root != s.rommLibrary, plat)
	}
	if err != nil {
		fail(err)
		return
	}
	s.SetBackground(key, BackgroundJob{Recent: true, Message: fmt.Sprintf("Downloaded %d ROM(s)", written), Done: 1, Total: 1})
	s.libraryWritten(ctx, RommTempRoot(s.rommLibrary))
}

// rommExtract unpacks an archive grab with the torrent rules: one holding
// only ROMs (plus readmes) extracts straight away; anything else is parked in
// .incoming/<keep|temp>/RomM/ for an admin to review on Admin > Torrent
// Sources, exactly like a torrent archive.
func (s *CatalogServer) rommExtract(archive, root, name string, temporary bool, plat catalog.Platform) (int, error) {
	_, clean, err := torrent.InspectFor(archive, &plat)
	if err != nil {
		return 0, errors.New("RomM sent something that is not a readable archive")
	}
	if !clean {
		mode := stagingMode(!temporary)
		dest := filepath.Join(s.rommLibrary, ".incoming", mode, "RomM", name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return 0, err
		}
		if err := os.Rename(archive, dest); err != nil {
			return 0, err
		}
		return 0, errors.New("archive holds non-ROM files; parked for admin review under Torrent Sources")
	}
	files, err := torrent.ExtractFor(archive, root, &plat)
	if err == nil && len(files) == 0 {
		err = errors.New("already in the library")
	}
	return len(files), err
}

// progressReader reports bytes read at most twice a second.
type progressReader struct {
	r      io.Reader
	n      int64
	last   time.Time
	report func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if time.Since(p.last) > 500*time.Millisecond {
		p.last = time.Now()
		p.report(p.n)
	}
	return n, err
}

// ---- RomM as a library view ----
//
// The home page can switch to the RomM library: the same grid, filters and
// game modal, fed from the cached RomM listing instead of the local catalog.
// Local-only tools (FBI, FTP, conversion, patches, uploads) stay on the
// ROM Relay view. Game ids are "romm-<id>", which /games/{id} and
// /media/{id}/cover route here.

const rommIDPrefix = "romm-"

// rommPageSize caps cards per render; "Show more" raises it. The RomM library
// is thousands of games, far past what one page should carry.
const rommPageSize = 240

// rommPlatformNames maps RomM platform slugs to display names for
// platformLabel, filled as listings are converted.
var rommPlatformNames sync.Map

// rommViewOK: the RomM view needs RomM configured and a signed-in user (it
// spends the relay's RomM token, so it is never public).
func (s *CatalogServer) rommViewOK(r *http.Request) bool {
	return s.romm != nil && (currentUser(r) != nil || s.adminOK(r))
}

func rommGame(r romm.ROM) catalog.Game {
	slug := r.PlatSlug
	if slug == "" {
		slug = strings.ToLower(r.Platform)
	}
	if r.Platform != "" {
		rommPlatformNames.Store(slug, r.Platform)
	}
	g := catalog.Game{
		ID: rommIDPrefix + strconv.Itoa(r.ID), Platform: slug, Title: r.Name,
		Description: r.Summary, Developer: r.Developer(), Publisher: r.Publisher(),
		Region: strings.Join(r.Regions, ", "), Genre: strings.Join(r.Metadatum.Genres, ", "),
		ReleaseDate: r.Year(), Players: r.Metadatum.PlayerCount, Rating: r.Rating(), MetadataSource: "RomM",
		Artifacts: []catalog.Artifact{{Name: r.FSName}},
	}
	if g.Title == "" {
		g.Title = r.FSName
	}
	if r.CoverPath(false) != "" {
		g.ArtworkURL = "romm"
	}
	return g
}

// rommGroupKey stacks one game's regional and revision copies: same
// platform, same title once "(USA)"/"[b]" style tags are stripped.
func rommGroupKey(r romm.ROM) string {
	name := r.Name
	if name == "" {
		name = strings.TrimSuffix(r.FSName, filepath.Ext(r.FSName))
	}
	return r.PlatSlug + "|" + searchFold(displayTitle(name))
}

// rommRegionRank orders versions for the card: the most widely useful first.
func rommRegionRank(r romm.ROM) int {
	for i, want := range []string{"USA", "World", "Europe"} {
		for _, have := range r.Regions {
			if strings.EqualFold(have, want) {
				return i
			}
		}
	}
	return 3
}

// rommGroups stacks the listing, versions ordered best first (has a cover,
// then region), groups in listing (name) order.
func rommGroups(roms []romm.ROM) [][]romm.ROM {
	index := map[string]int{}
	var groups [][]romm.ROM
	for _, r := range roms {
		k := rommGroupKey(r)
		i, ok := index[k]
		if !ok {
			i = len(groups)
			index[k] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], r)
	}
	for _, g := range groups {
		sort.SliceStable(g, func(i, j int) bool {
			ci, cj := g[i].CoverPath(false) != "", g[j].CoverPath(false) != ""
			if ci != cj {
				return ci
			}
			return rommRegionRank(g[i]) < rommRegionRank(g[j])
		})
	}
	return groups
}

// rommGroupGame is one card: the best version's metadata, every version's
// file as an artifact (so search matches any of them and the card says how
// many), and the regions across all of them.
func rommGroupGame(g []romm.ROM) catalog.Game {
	game := rommGame(g[0])
	game.Artifacts = game.Artifacts[:0]
	var regions []string
	seen := map[string]bool{}
	for _, r := range g {
		game.Artifacts = append(game.Artifacts, catalog.Artifact{Name: r.FSName})
		for _, reg := range r.Regions {
			if !seen[reg] {
				seen[reg] = true
				regions = append(regions, reg)
			}
		}
	}
	if len(regions) > 3 {
		regions = append(regions[:3], "…")
	}
	game.Region = strings.Join(regions, ", ")
	return game
}

// rommShot serves one screenshot through the relay's disk cache.
func (s *CatalogServer) rommShot(w http.ResponseWriter, r *http.Request) {
	if !s.rommUserOK(w, r) {
		return
	}
	id, err1 := strconv.Atoi(r.PathValue("id"))
	n, err2 := strconv.Atoi(r.PathValue("n"))
	rom, ok := s.romm.Cached(id)
	if err1 != nil || err2 != nil || !ok || n < 0 || n >= len(rom.Screens) || !romm.ResourcePath(rom.Screens[n]) {
		http.NotFound(w, r)
		return
	}
	s.serveRommImage(w, r, fmt.Sprintf("%d-s%d", id, n), rom.Screens[n])
}
