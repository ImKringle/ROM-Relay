package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"romrelay/internal/torrent"
)

// Torrents: an admin curates the .torrent sources; any signed-in user can
// browse what is inside them and grab single files. qBittorrent does the
// transfer, leech-only (see internal/torrent).

// archiveRetention is how long an extracted archive, and a grabbed ROM not
// marked "retain", is kept before pruning.
const archiveRetention = 7 * 24 * time.Hour

// torrentPageSize is how many tiles one search page returns.
const torrentPageSize = 60

// SetTorrent enables torrents. dir holds the admin's .torrent sources;
// libraryDir is the library root as ROM Relay sees it and savePath the same
// folder as qBittorrent sees it. It also starts the sweeper that removes
// finished grabs from qBittorrent so nothing seeds.
func (s *CatalogServer) SetTorrent(client *torrent.Client, dir, libraryDir, savePath string) {
	s.torrents, s.torrentDir, s.torrentLibrary, s.torrentSavePath = client, dir, libraryDir, strings.TrimRight(savePath, `/\`)
	go s.sweepTorrents()
}

// SetLibraryChanged registers what runs after torrent grabs add ROMs to the
// catalog: hashing and the libretro/IGDB metadata sweep.
func (s *CatalogServer) SetLibraryChanged(fn func()) { s.libraryChanged = fn }

// torrentUserOK: any signed-in account, or the admin token.
func (s *CatalogServer) torrentUserOK(w http.ResponseWriter, r *http.Request) bool {
	if currentUser(r) == nil && !s.adminOK(r) {
		http.Error(w, "sign in to use torrents", http.StatusUnauthorized)
		return false
	}
	if s.torrents == nil {
		http.Error(w, "torrents are not available on this relay", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// ---- source index ----

// torrentItem is one grabbable file inside a stored torrent.
type torrentItem struct {
	Torrent  string `json:"torrent"`
	Index    int    `json:"index"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Platform string `json:"platform,omitempty"`
	Archive  bool   `json:"archive,omitempty"`
	torrent.Release
	lowerTitle string
}

type torrentSource struct {
	File  string `json:"file"`
	Name  string `json:"name"`
	Files int    `json:"files"`
	Size  int64  `json:"size"`
}

type cachedTorrent struct {
	mod    time.Time
	size   int64
	source torrentSource
	items  []torrentItem
}

// torrentIndex caches parsed torrents, re-parsing a file only when its size
// or mtime changes, so a keystroke in the search box costs a directory read
// and a filter pass, not a parse of every multi-megabyte .torrent.
type torrentIndex struct {
	mu    sync.Mutex
	files map[string]cachedTorrent
}

func (s *CatalogServer) torrentSnapshot() ([]torrentSource, []torrentItem) {
	s.torrentIdx.mu.Lock()
	defer s.torrentIdx.mu.Unlock()
	if s.torrentIdx.files == nil {
		s.torrentIdx.files = map[string]cachedTorrent{}
	}
	entries, _ := os.ReadDir(s.torrentDir)
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".torrent") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		seen[e.Name()] = true
		if c, ok := s.torrentIdx.files[e.Name()]; ok && c.mod.Equal(info.ModTime()) && c.size == info.Size() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.torrentDir, e.Name()))
		if err != nil {
			continue
		}
		m, err := torrent.Parse(b)
		if err != nil {
			continue
		}
		c := cachedTorrent{mod: info.ModTime(), size: info.Size(), source: torrentSource{File: e.Name(), Name: m.Name}}
		for _, f := range m.Files {
			c.source.Size += f.Size
			item := torrentItem{Torrent: e.Name(), Index: f.Index, Path: f.Path, Size: f.Size, Archive: isZip(f.Path)}
			if p, ok := catalog.PlatformByPath(f.Path); ok {
				item.Platform = p.ID
			}
			if item.Platform == "" && !item.Archive {
				continue // readmes, covers, etc. are not grabbable
			}
			item.Release = torrent.ParseRelease(f.Path)
			item.lowerTitle = strings.ToLower(item.Title)
			c.items = append(c.items, item)
		}
		c.source.Files = len(c.items)
		s.torrentIdx.files[e.Name()] = c
	}
	var sources []torrentSource
	var items []torrentItem
	for name, c := range s.torrentIdx.files {
		if !seen[name] {
			delete(s.torrentIdx.files, name)
			continue
		}
		sources = append(sources, c.source)
		items = append(items, c.items...)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, items
}

// ---- user-facing browse ----

type facetCount struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Count int    `json:"count"`
}

// searchTorrents filters the indexed files. q matches the game title only;
// region / language / tag / source are separate exact filters. Each facet's
// counts honour every filter except its own, so picking "Japan" still shows
// what the other regions would give.
func (s *CatalogServer) searchTorrents(w http.ResponseWriter, r *http.Request) {
	if !s.torrentUserOK(w, r) {
		return
	}
	q := r.URL.Query()
	words := strings.Fields(strings.ToLower(q.Get("q")))
	filters := map[string]string{"region": q.Get("region"), "language": q.Get("language"), "tag": q.Get("tag"), "source": q.Get("source")}
	offset, _ := strconv.Atoi(q.Get("offset"))
	sources, items := s.torrentSnapshot()

	values := func(it *torrentItem, facet string) []string {
		switch facet {
		case "region":
			return it.Regions
		case "language":
			return it.Languages
		case "tag":
			return it.Tags
		default:
			return []string{it.Torrent}
		}
	}
	match := func(it *torrentItem, skip string) bool {
		if !containsAll(it.lowerTitle, words) {
			return false
		}
		for facet, want := range filters {
			if want != "" && facet != skip && !hasValue(values(it, facet), want) {
				return false
			}
		}
		return true
	}

	var results []torrentItem
	counts := map[string]map[string]int{"region": {}, "language": {}, "tag": {}, "source": {}}
	for i := range items {
		it := &items[i]
		if match(it, "") {
			results = append(results, *it)
		}
		for facet, c := range counts {
			if match(it, facet) {
				for _, v := range values(it, facet) {
					c[v]++
				}
			}
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].lowerTitle != results[j].lowerTitle {
			return results[i].lowerTitle < results[j].lowerTitle
		}
		return results[i].Path < results[j].Path
	})
	total := len(results)
	if offset < 0 || offset > total {
		offset = total
	}
	results = results[offset:min(offset+torrentPageSize, total)]

	labels := map[string]string{}
	for _, src := range sources {
		labels[src.File] = src.Name
	}
	facets := map[string][]facetCount{}
	for facet, c := range counts {
		list := []facetCount{}
		for v, n := range c {
			list = append(list, facetCount{Value: v, Label: labels[v], Count: n})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Count != list[j].Count {
				return list[i].Count > list[j].Count
			}
			return list[i].Value < list[j].Value
		})
		facets[facet] = list
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "total": total, "offset": offset, "facets": facets})
}

func hasValue(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func containsAll(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// grabTorrentFile hands one file of a stored torrent to qBittorrent. ROMs go
// straight to a platform folder; archives to the staging folder, where the
// sweeper inspects and unzips them. Only an admin can make a grab permanent;
// everyone else's expire like patch output. The session cookie is
// SameSite=Lax, which is what guards this mutation (as with save uploads).
func (s *CatalogServer) grabTorrentFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if !s.torrentUserOK(w, r) {
		return
	}
	source, err := s.torrentPath(r.FormValue("torrent"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	index, err := strconv.Atoi(r.FormValue("index"))
	if err != nil {
		http.Error(w, "invalid file index", http.StatusBadRequest)
		return
	}
	b, err := os.ReadFile(source)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := torrent.Parse(b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var file *torrent.File
	for i := range m.Files {
		if m.Files[i].Index == index {
			file = &m.Files[i]
		}
	}
	if file == nil {
		http.Error(w, "no such file in that torrent", http.StatusBadRequest)
		return
	}
	retain := r.FormValue("retain") == "true" && s.adminOK(r)
	root := s.torrentSavePath
	if !retain {
		root += "/external/torrent"
	}
	// ponytail: a torrent's save path is fixed when first added, so a second
	// grab from the same torrent lands beside the first even if its kind
	// differs. ROM sets are one kind per torrent in practice.
	var dest string
	if p, ok := catalog.PlatformByPath(file.Path); ok {
		dest = root + "/" + p.Directory
	} else if isZip(file.Path) {
		dest = s.torrentSavePath + "/.incoming/" + stagingMode(retain)
	} else {
		http.Error(w, "not a ROM or .zip: "+filepath.Ext(file.Path), http.StatusUnprocessableEntity)
		return
	}
	// The tray row is named after the game, not the torrent (a source holds
	// thousands); a second grab from a still-running torrent takes over the
	// row. The label is registered before Grab, which can take seconds, so a
	// sweep in the meantime never invents a second row named after the torrent.
	name := path.Base(file.Path)
	key := "Download · " + strings.TrimSuffix(name, path.Ext(name))
	old, hadOld := s.grabLabels.Swap(m.Hash, key)
	if hadOld && old != key {
		s.ClearBackground(old.(string))
	}
	s.SetBackground(key, BackgroundJob{Active: true, Message: "Starting…", Total: 1000})
	if err := s.torrents.Grab(r.Context(), b, m, index, dest); err != nil {
		s.ClearBackground(key)
		if hadOld {
			s.grabLabels.Store(m.Hash, old) // an earlier grab is still running
		} else {
			s.grabLabels.Delete(m.Hash)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": file.Path, "retained": retain})
}

// grabLabel is the activity-tray row for a torrent: the game last grabbed
// from it, or the torrent's own name for one added before a restart.
func (s *CatalogServer) grabLabel(t torrent.Status) string {
	if key, ok := s.grabLabels.Load(t.Hash); ok {
		return key.(string)
	}
	return "Download · " + t.Name
}

// ---- background sweep, unzip, prune ----

// sweepTorrents polls qBittorrent for as long as the relay runs. Running
// forever (rather than only while a grab is in flight) also cleans up grabs
// that finished while ROM Relay was down.
// ponytail: fixed 10s poll, one cheap local API call; event-driven if it ever matters.
func (s *CatalogServer) sweepTorrents() {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		done, active, err := s.torrents.Sweep(ctx)
		if err != nil {
			s.SetBackground("torrent", BackgroundJob{Recent: true, Message: "Torrent sweep failed", Error: err.Error()})
		}
		for _, t := range active {
			msg := fmt.Sprintf("%s/s · %d seeds, %d peers connected", humanBytes(t.DLSpeed), t.Seeds, t.Peers)
			s.SetBackground(s.grabLabel(t), BackgroundJob{Active: true, Message: msg, Done: int64(t.Progress * 1000), Total: 1000})
		}
		for _, t := range done {
			s.SetBackground(s.grabLabel(t), BackgroundJob{Recent: true, Message: "Downloaded", Done: 1, Total: 1})
			s.grabLabels.Delete(t.Hash)
		}
		extracted := false
		if err == nil {
			// Only with a fresh active list: an archive qBittorrent is still
			// writing must never be opened.
			busy := map[string]bool{}
			for _, t := range active {
				busy[t.Name] = true
			}
			extracted = s.processIncoming(busy)
		}
		s.pruneArchives()
		if len(done) > 0 || extracted {
			s.libraryWritten(ctx, torrent.TempRoot(s.torrentLibrary))
		}
		cancel()
		time.Sleep(10 * time.Second)
	}
}

// libraryWritten catalogues freshly written ROMs, starts the expiry clock on
// temporary ones under tempRoot, and kicks off hashing + metadata.
func (s *CatalogServer) libraryWritten(ctx context.Context, tempRoot string) {
	if err := s.store.Reconcile(ctx); err != nil {
		log.Printf("torrent: reconcile: %v", err)
		return
	}
	if err := s.store.ExpireNewUnder(ctx, tempRoot, time.Now().Add(archiveRetention)); err != nil {
		log.Printf("torrent: set expiry: %v", err)
	}
	if s.libraryChanged != nil {
		s.libraryChanged()
	}
}

// Archives download into <library>/.incoming/<keep|temp>, which the catalog
// scan ignores (its top-level folder is not a platform). Extracted archives
// move to .incoming/.done until pruned.
func (s *CatalogServer) incomingDir() string { return filepath.Join(s.torrentLibrary, ".incoming") }
func (s *CatalogServer) doneDir() string     { return filepath.Join(s.incomingDir(), ".done") }

func stagingMode(retain bool) string {
	if retain {
		return "keep"
	}
	return "temp"
}

// processIncoming auto-extracts every finished archive that inspects clean
// and reports whether any ROM was written. Archives with unexpected contents
// stay put for an admin to review.
func (s *CatalogServer) processIncoming(busy map[string]bool) bool {
	wrote := false
	for _, rel := range s.incomingArchives() {
		// Downloads land at .incoming/<keep|temp>/<torrent name>[/...], so the
		// second path segment names the torrent that owns the file.
		parts := strings.SplitN(rel, "/", 3)
		if len(parts) < 3 || busy[parts[1]] {
			continue
		}
		if _, clean, err := torrent.Inspect(s.pendingPath(rel)); err != nil || !clean {
			continue
		}
		n, err := s.extractArchive(rel)
		wrote = wrote || n > 0
		if err != nil {
			s.SetBackground("Unzip · "+rel, BackgroundJob{Recent: true, Message: "Unzip failed · " + rel, Error: err.Error()})
		}
	}
	return wrote
}

// extractArchive unpacks one pending archive into the library and moves the
// archive to .done, stamped now so the 7-day prune counts from extraction.
func (s *CatalogServer) extractArchive(rel string) (int, error) {
	src := s.pendingPath(rel)
	library := s.torrentLibrary
	if !strings.HasPrefix(rel, "keep/") {
		library = torrent.TempRoot(s.torrentLibrary)
	}
	written, err := torrent.Extract(src, library)
	if err != nil {
		return len(written), err
	}
	dest := filepath.Join(s.doneDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return len(written), err
	}
	if err := os.Rename(src, dest); err != nil {
		return len(written), err
	}
	now := time.Now()
	_ = os.Chtimes(dest, now, now)
	removeEmptyParents(filepath.Dir(src), s.incomingDir())
	s.SetBackground("Unzip · "+rel, BackgroundJob{Recent: true, Message: "Unzipped " + strconv.Itoa(len(written)) + " ROM(s) · " + path.Base(rel), Done: 1, Total: 1})
	return len(written), nil
}

// pruneArchives deletes extracted archives older than archiveRetention.
func (s *CatalogServer) pruneArchives() {
	cutoff := time.Now().Add(-archiveRetention)
	_ = filepath.WalkDir(s.doneDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			if os.Remove(p) == nil {
				removeEmptyParents(filepath.Dir(p), s.doneDir())
			}
		}
		return nil
	})
}

// removeEmptyParents removes now-empty folders from dir up to (not including)
// stop. os.Remove refuses a non-empty folder, which ends the climb.
func removeEmptyParents(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop) && os.Remove(dir) == nil {
		dir = filepath.Dir(dir)
	}
}

// incomingArchives lists pending .zip files as slash paths relative to the
// incoming folder.
func (s *CatalogServer) incomingArchives() []string {
	var out []string
	root := s.incomingDir()
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p == s.doneDir() {
			return filepath.SkipDir
		}
		if !d.IsDir() && isZip(p) {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func (s *CatalogServer) pendingPath(rel string) string {
	return filepath.Join(s.incomingDir(), filepath.FromSlash(rel))
}

// isZip reports a grabbable archive (.zip or .7z), named for its first format.
func isZip(name string) bool { return torrent.IsArchive(name) }

// ---- admin: sources, review, retention ----

// torrentPath maps a stored torrent's file name to its path, refusing
// anything that is not a bare *.torrent name in the torrent directory.
func (s *CatalogServer) torrentPath(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(strings.ToLower(name), ".torrent") {
		return "", errors.New("invalid torrent name")
	}
	return filepath.Join(s.torrentDir, name), nil
}

// adminTorrents is the admin page's data: sources, archives pending or
// recently unzipped, and temporary grabs.
func (s *CatalogServer) adminTorrents(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	if s.torrents == nil {
		http.Error(w, "torrents are disabled: install qbittorrent-nox or set [torrent] qbittorrent_url", http.StatusServiceUnavailable)
		return
	}
	sources, _ := s.torrentSnapshot()
	if sources == nil {
		sources = []torrentSource{}
	}
	pending, extracted := s.archiveListing()
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources, "pending": pending, "extracted": extracted, "temporary": s.temporaryROMs(r.Context())})
}

func (s *CatalogServer) uploadTorrents(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.torrents == nil {
		http.Error(w, "torrents are disabled", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(s.torrentDir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var saved []string
	for _, fh := range r.MultipartForm.File["file"] {
		path, err := s.torrentPath(filepath.Base(fh.Filename))
		if err != nil {
			http.Error(w, fh.Filename+": only .torrent files can be uploaded", http.StatusBadRequest)
			return
		}
		f, err := fh.Open()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := torrent.Parse(b); err != nil {
			http.Error(w, fh.Filename+": "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		saved = append(saved, filepath.Base(path))
	}
	if len(saved) == 0 {
		http.Error(w, "choose at least one .torrent file", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": saved})
}

func (s *CatalogServer) deleteTorrent(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.torrents == nil {
		http.Error(w, "torrents are disabled", http.StatusServiceUnavailable)
		return
	}
	path, err := s.torrentPath(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.Remove(path); err != nil {
		http.Error(w, "could not delete: "+err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pendingArchive validates a client-supplied pending path: a local .zip path
// inside incoming, not inside .done.
func (s *CatalogServer) pendingArchive(rel string) (string, error) {
	if !filepath.IsLocal(filepath.FromSlash(rel)) || !isZip(rel) || strings.HasPrefix(rel, ".done/") {
		return "", errors.New("invalid archive path")
	}
	if _, err := os.Stat(s.pendingPath(rel)); err != nil {
		return "", errors.New("that archive is no longer pending")
	}
	return rel, nil
}

func (s *CatalogServer) extractPending(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.torrents == nil {
		http.Error(w, "torrents are disabled", http.StatusServiceUnavailable)
		return
	}
	rel, err := s.pendingArchive(r.FormValue("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n, err := s.extractArchive(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if n > 0 {
		s.libraryWritten(r.Context(), torrent.TempRoot(s.torrentLibrary))
	}
	writeJSON(w, http.StatusOK, map[string]any{"extracted": n})
}

func (s *CatalogServer) deletePending(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.torrents == nil {
		http.Error(w, "torrents are disabled", http.StatusServiceUnavailable)
		return
	}
	rel, err := s.pendingArchive(r.FormValue("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := s.pendingPath(rel)
	if err := os.Remove(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	removeEmptyParents(filepath.Dir(p), s.incomingDir())
	w.WriteHeader(http.StatusNoContent)
}

// keepTorrentROM retains a temporary torrent grab, the same retain flag patch
// output uses: no expiry, never pruned.
func (s *CatalogServer) keepTorrentROM(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.torrents == nil {
		http.Error(w, "torrents are disabled", http.StatusServiceUnavailable)
		return
	}
	a, err := s.store.Artifact(r.Context(), r.PathValue("id"))
	if err != nil || !strings.HasPrefix(a.Path, torrent.TempRoot(s.torrentLibrary)+string(filepath.Separator)) {
		http.Error(w, "not a temporary torrent ROM", http.StatusNotFound)
		return
	}
	if err := s.store.MarkArtifactRetention(r.Context(), a.Path, true, time.Time{}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": a.ID, "retained": true})
}

type pendingArchive struct {
	Path    string          `json:"path"`
	Entries []torrent.Entry `json:"entries"`
	Clean   bool            `json:"clean"`
	Error   string          `json:"error,omitempty"`
}

type extractedArchive struct {
	Path     string `json:"path"`
	PrunedOn string `json:"pruned_on"`
}

func (s *CatalogServer) archiveListing() ([]pendingArchive, []extractedArchive) {
	pending := []pendingArchive{}
	for _, rel := range s.incomingArchives() {
		entries, clean, err := torrent.Inspect(s.pendingPath(rel))
		item := pendingArchive{Path: rel, Entries: entries, Clean: clean}
		if err != nil {
			item.Error = "still downloading, or not a readable zip"
		}
		pending = append(pending, item)
	}
	extracted := []extractedArchive{}
	_ = filepath.WalkDir(s.doneDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			rel, _ := filepath.Rel(s.doneDir(), p)
			extracted = append(extracted, extractedArchive{Path: filepath.ToSlash(rel), PrunedOn: info.ModTime().Add(archiveRetention).Format("2006-01-02")})
		}
		return nil
	})
	return pending, extracted
}

type temporaryROM struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	ExpiresOn string `json:"expires_on,omitempty"`
	Retained  bool   `json:"retained"`
}

func (s *CatalogServer) temporaryROMs(ctx context.Context) []temporaryROM {
	out := []temporaryROM{}
	list, err := s.store.ExternalArtifactsUnder(ctx, torrent.TempRoot(s.torrentLibrary))
	if err != nil {
		return out
	}
	for _, a := range list {
		t := temporaryROM{ID: a.ID, Name: a.Name, Platform: a.Platform, Retained: a.Retain}
		if a.ExpiresAt != nil {
			t.ExpiresOn = a.ExpiresAt.Format("2006-01-02")
		}
		out = append(out, t)
	}
	return out
}
