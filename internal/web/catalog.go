package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/net/webdav"
	"golang.org/x/text/unicode/norm"
	"romrelay/internal/catalog"
	"romrelay/internal/config"
	"romrelay/internal/decryptor"
	"romrelay/internal/dex"
	patcher "romrelay/internal/patch"
	"romrelay/internal/pokemon"
	"romrelay/internal/pokemonvault"
	"romrelay/internal/randomizer"
	remotelib "romrelay/internal/remote"
	"romrelay/internal/savevault"
	"romrelay/internal/threedscrypt"
)

type CatalogServer struct {
	store                *catalog.Store
	base, token, theme   string
	adminToken, patchDir string
	mediaDir             string
	retention            time.Duration
	patches              *patcher.Manager
	randomizer           *randomizer.Manager
	decryptor            *decryptor.Manager
	remote               *remotelib.Manager
	pokemon              *pokemon.Client
	saves                savevault.Vault
	pokemonFiles         pokemonvault.Vault
	igdbEnabled          bool
	webdavEnabled        bool
	webdavHandler        *webdav.Handler
	csrf                 string
	handler              http.Handler
	scanning             atomic.Bool
	lastError            atomic.Value
	httpRequests         atomic.Int64
	httpResponseBytes    atomic.Int64
	httpActive           atomic.Int64
	downloadSeq          atomic.Uint64
	shutdown             func()
	downloadObserver     func(DownloadEvent)
	jobMu                sync.RWMutex
	background           map[string]BackgroundJob
	ready                atomic.Bool
	setupStage           atomic.Value
	convert              func(context.Context, string, func(string, int64, int64)) error
	convertUpload        ConvertUploadFunc
	crypt                *threedscrypt.Client
	uploadResultsDir     string
	uploadMu             sync.Mutex
	uploadJobs           map[string]*uploadJob
	cfgMu                sync.RWMutex
	cfg                  config.Config
	configDir            string
}

// ConvertUploadFunc converts an uploaded .3ds/.cci file (name is used only to
// pick the input extension) to a CIA and returns the CIA bytes, plus whether
// the source content was NCCH-encrypted, so the caller can route the result
// into the right encrypted/unencrypted destination instead of assuming one.
// Persisting the result is publishUploadResult's job, shared with the crypt
// upload flow, not this callback's.
type ConvertUploadFunc func(ctx context.Context, name string, data []byte, progress func(part string, done, total int64)) (result []byte, encrypted bool, err error)
type BackgroundJob struct {
	Active  bool   `json:"active"`
	Recent  bool   `json:"recent,omitempty"`
	Message string `json:"message,omitempty"`
	Done    int64  `json:"done,omitempty"`
	Total   int64  `json:"total,omitempty"`
	Error   string `json:"error,omitempty"`
}
type DownloadEvent struct {
	ArtifactID string
	Name       string
	RemoteIP   string
	Status     string
	Written    int64
	Expected   int64
}
type themeCopy struct{ Brand, Tagline, Empty, Back, AdminTitle, AdminIntro, AuthError string }

func copyFor(theme string) themeCopy {
	if theme == "corn" {
		return themeCopy{"🌽 ROM Relay · Corn Mode", "Fresh ROMs from your own corn crib.", "No matching ears in the crib. The harvest may still be running.", "Back to the corn crib", "Patch silo", "This private silo is separate from the public corn stand. Reusable patches come from the configured patch field.", "This private corn stand needs its viewer token"}
	}
	return themeCopy{"ROM Relay", "Your ROMs. Your network. Your relay.", "No matching ROMs. Catalog reconciliation may still be running.", "Library", "Patch workspace", "This private session is separate from viewer access. Reusable patches are read from the configured patch directory.", "ROM Relay requires its private viewer token"}
}

func NewCatalog(store *catalog.Store, base, token, theme, adminToken, patchDir string, retention time.Duration) *CatalogServer {
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	csrfSum := sha256.Sum256([]byte("romrelay-csrf\x00" + adminToken))
	s := &CatalogServer{store: store, base: strings.TrimRight(base, "/"), token: token, theme: theme, adminToken: adminToken, patchDir: patchDir, retention: retention, csrf: hex.EncodeToString(csrfSum[:]), background: map[string]BackgroundJob{}, uploadJobs: map[string]*uploadJob{}}
	s.ready.Store(true)
	s.setupStage.Store("ready")
	s.webdavHandler = &webdav.Handler{
		Prefix:     "/webdav",
		FileSystem: catalogWebDAVFS{store: store},
		LockSystem: webdav.NewMemLS(),
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /", s.index)
	m.HandleFunc("GET /games/{id}", s.game)
	m.HandleFunc("GET /api/games", s.apiGames)
	m.HandleFunc("GET /healthz", s.catalogHealth)
	m.HandleFunc("GET /d/{id}", s.catalogDownload)
	m.HandleFunc("HEAD /d/{id}", s.catalogDownload)
	m.HandleFunc("GET /download/{id}", s.downloadChoice)
	m.HandleFunc("GET /qr/{file}", s.catalogQR)
	m.HandleFunc("GET /qr/fbi/{batch}", s.fbiBatchQR)
	m.HandleFunc("GET /api/fbi/batches", s.fbiBatches)
	m.HandleFunc("GET /media/{id}/cover", s.catalogMedia)
	m.HandleFunc("POST /queue", s.catalogQueue)
	m.HandleFunc("POST /api/ftp/browse", s.ftpBrowse)
	m.HandleFunc("POST /api/ftp/send", s.ftpSend)
	m.HandleFunc("GET /patches", s.publicPatches)
	m.HandleFunc("GET /api/patch-files", s.publicPatchFiles)
	m.HandleFunc("POST /api/patch-jobs", s.createPublicPatchJob)
	m.HandleFunc("GET /api/patch-jobs/{id}", s.getPublicPatchJob)
	m.HandleFunc("POST /api/upload/convert", s.createConvertUpload)
	m.HandleFunc("GET /api/upload/convert/{id}", s.getUploadJob)
	m.HandleFunc("POST /api/upload/crypt", s.createCryptUpload)
	m.HandleFunc("GET /api/upload/crypt/{id}", s.getUploadJob)
	m.HandleFunc("POST /api/library/convert/{id}", s.createConvertFromLibrary)
	m.HandleFunc("POST /api/library/crypt/{id}", s.createCryptFromLibrary)
	m.HandleFunc("POST /api/library/upload", s.createLibraryUpload)
	m.HandleFunc("GET /admin", s.adminHub)
	m.HandleFunc("GET /admin/roms", s.adminRoms)
	m.HandleFunc("DELETE /api/admin/roms/{id}", s.deleteArtifact)
	m.HandleFunc("GET /admin/saves", s.adminSaves)
	m.HandleFunc("GET /api/admin/users/{id}/saves", s.adminUserSaves)
	m.HandleFunc("GET /admin/users/{id}/saves/{sid}/download", s.adminDownloadUserSave)
	m.HandleFunc("DELETE /api/admin/users/{id}/saves/{sid}", s.adminDeleteUserSave)
	m.HandleFunc("GET /api/admin/users/{id}/dex", s.adminUserDex)
	m.HandleFunc("GET /api/admin/users/{id}/pokehome", s.adminUserPokehome)
	m.HandleFunc("DELETE /api/admin/users/{id}/pokehome/{pid}", s.adminDeletePokehomeMon)
	m.HandleFunc("GET /admin/settings", s.adminSettings)
	m.HandleFunc("POST /api/admin/settings", s.saveSettings)
	m.HandleFunc("GET /admin/api-keys", s.apiKeysPage)
	m.HandleFunc("GET /api/admin/api-keys", s.listAPIKeys)
	m.HandleFunc("POST /api/admin/api-keys", s.createAPIKey)
	m.HandleFunc("DELETE /api/admin/api-keys/{id}", s.revokeAPIKey)
	m.HandleFunc("GET /admin/patches", s.adminPatches)
	m.HandleFunc("GET /api/admin/patch-files", s.patchFiles)
	m.HandleFunc("GET /api/admin/games", s.adminGames)
	m.HandleFunc("POST /api/admin/patch-jobs", s.createPatchJob)
	m.HandleFunc("GET /api/admin/patch-jobs/{id}", s.getPatchJob)
	m.HandleFunc("DELETE /api/admin/patch-jobs/{id}", s.deletePatchJob)
	m.HandleFunc("POST /api/admin/patch-jobs/{id}/publish", s.publishPatchJob)
	m.HandleFunc("POST /api/admin/patch-jobs/{id}/retain", s.retainPatchJob)
	m.HandleFunc("POST /api/admin/games/{id}/metadata", s.updateGameMetadata)
	m.HandleFunc("POST /api/admin/decrypt/{id}", s.prepareUnencrypted)
	m.HandleFunc("POST /api/admin/convert/{id}", s.convert3DS)
	m.HandleFunc("GET /api/admin/remote-search", s.remoteSearch)
	m.HandleFunc("POST /api/admin/remote-jobs", s.createRemoteJob)
	m.HandleFunc("GET /api/admin/remote-jobs/{id}", s.getRemoteJob)
	m.HandleFunc("DELETE /api/admin/remote-jobs/{id}", s.cancelRemoteJob)
	m.HandleFunc("POST /api/admin/shutdown", s.adminShutdown)
	m.HandleFunc("POST /api/saves/upload", s.uploadSave)
	m.HandleFunc("GET /api/pokemon", s.listPokemon)
	m.HandleFunc("POST /api/pokemon", s.createPokemon)
	m.HandleFunc("POST /api/pokemon/{id}/edit", s.editPokemon)
	m.HandleFunc("DELETE /api/pokemon/{id}", s.deletePokemon)
	m.HandleFunc("GET /api/pokemon/{id}/download", s.downloadPokemon)
	m.HandleFunc("GET /api/reference", s.apiReference)
	m.HandleFunc("GET /api/pokehome", s.listPokehome)
	m.HandleFunc("POST /api/pokehome/{id}/deposit", s.depositPokehome)
	m.HandleFunc("GET /api/pokehome/boxes", s.pokehomeBoxes)
	m.HandleFunc("POST /api/pokehome/{id}/withdraw", s.withdrawPokehome)
	m.HandleFunc("GET /pokehome", s.pokehomePage)
	m.HandleFunc("GET /api/dex", s.apiDex)
	m.HandleFunc("GET /api/dex/species/{dex}", s.apiSpeciesDetail)
	m.HandleFunc("GET /api/pokedex-lookup", s.apiPokedexLookup)
	m.HandleFunc("GET /api/pokedex-lookup/games", s.apiPokedexLookupGames)
	m.HandleFunc("GET /pokedex", s.pokedexPage)
	m.HandleFunc("GET /trainers", s.trainersPage)
	m.HandleFunc("GET /pokedex/types", s.typeChartPage)
	m.HandleFunc("GET /editor", s.pokemonEditorPage)
	m.HandleFunc("GET /randomizer", s.randomizerPage)
	m.HandleFunc("POST /api/randomize-jobs", s.createRandomizeJob)
	m.HandleFunc("GET /api/randomize-jobs/{id}", s.getRandomizeJob)
	m.HandleFunc("POST /api/randomize-jobs/{id}/retain", s.retainRandomizeJob)
	// Old links and bookmarks still land somewhere sensible.
	m.HandleFunc("GET /dex", redirectTo("/pokedex"))
	m.HandleFunc("GET /userdex", redirectTo("/trainers"))
	m.HandleFunc("GET /api/userdex", s.apiUserdex)
	m.HandleFunc("GET /api/userdex/{id}/dex", s.apiUserdexDex)
	m.HandleFunc("GET /account/password", s.passwordChangePage)
	m.HandleFunc("POST /account/password", s.changePassword)
	m.HandleFunc("GET /admin/users", s.usersPage)
	m.HandleFunc("GET /api/admin/users", s.listUsers)
	m.HandleFunc("POST /api/admin/users", s.createUser)
	m.HandleFunc("POST /api/admin/users/{id}/role", s.setUserRole)
	m.HandleFunc("POST /api/admin/users/{id}/disabled", s.setUserDisabled)
	m.HandleFunc("POST /api/admin/users/{id}/reset-password", s.resetUserPassword)
	m.HandleFunc("POST /api/admin/users/{id}/clear-dex", s.clearUserDex)
	m.HandleFunc("POST /api/admin/users/{id}/clear-trainers", s.clearUserTrainers)
	m.HandleFunc("POST /api/admin/dex/prune", s.pruneDex)
	m.HandleFunc("DELETE /api/admin/users/{id}", s.deleteUser)
	m.HandleFunc("GET /api/saves", s.listSaves)
	m.HandleFunc("GET /saves/{id}/download", s.downloadSave)
	m.HandleFunc("DELETE /api/saves/{id}", s.deleteSave)
	m.HandleFunc("POST /api/saves/{id}/game-override", s.setSaveGameOverride)
	m.HandleFunc("GET /api/saves/{id}/bag", s.getSaveBag)
	m.HandleFunc("POST /api/saves/{id}/bag", s.setSaveBag)
	m.HandleFunc("GET /saves", s.savesPage)
	m.Handle("GET /sprites/", dex.SpriteHandler())
	m.HandleFunc("GET /login", s.loginPage)
	m.HandleFunc("POST /login", s.login)
	m.HandleFunc("POST /logout", s.logout)
	m.HandleFunc("GET /logout", s.logout)
	m.HandleFunc("GET /api/me", s.apiMe)
	// One subtree route for everything under dist/, not one line per folder,
	// the per-folder allowlist this replaced silently 404'd every asset
	// directory (logo/, gender/, shiny/, typebadges/) added after it was last
	// updated, since a new folder needs a new line nobody remembers to add.
	m.Handle("GET /assets/", assetHandler())
	// A bare "/webdav/" pattern (matching every method) conflicts with the
	// "GET /" root route in net/http's mux ambiguity check, so every verb
	// golang.org/x/net/webdav.Handler dispatches is registered explicitly.
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "PROPFIND", "PROPPATCH", "PUT", "DELETE", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK", "POST"} {
		m.HandleFunc(method+" /webdav/", s.serveWebDAV)
	}
	m.HandleFunc("GET /account/webdav", s.webdavKeysPage)
	m.HandleFunc("GET /api/account/webdav-keys", s.listWebDAVKeys)
	m.HandleFunc("POST /api/account/webdav-keys", s.createWebDAVKey)
	m.HandleFunc("DELETE /api/account/webdav-keys/{id}", s.revokeWebDAVKey)
	s.handler = s.observeHTTP(s.security(m))
	return s
}

type HTTPMetrics struct {
	Requests, ResponseBytes, Active int64
}

func (s *CatalogServer) HTTPMetrics() HTTPMetrics {
	return HTTPMetrics{Requests: s.httpRequests.Load(), ResponseBytes: s.httpResponseBytes.Load(), Active: s.httpActive.Load()}
}

type OperatorState struct {
	Scanning bool
	Ready    bool
	Setup    string
	Jobs     map[string]BackgroundJob
	Error    string
}

func (s *CatalogServer) OperatorState() OperatorState {
	state := OperatorState{Scanning: s.scanning.Load(), Ready: s.ready.Load(), Setup: s.SetupStage(), Jobs: s.backgroundSnapshot()}
	if value := s.lastError.Load(); value != nil {
		state.Error, _ = value.(string)
	}
	return state
}

func (s *CatalogServer) observeHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.httpRequests.Add(1)
		s.httpActive.Add(1)
		defer s.httpActive.Add(-1)
		mw := &metricResponseWriter{ResponseWriter: w, bytes: &s.httpResponseBytes}
		next.ServeHTTP(mw, r)
	})
}

type metricResponseWriter struct {
	http.ResponseWriter
	bytes *atomic.Int64
}

func (w *metricResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes.Add(int64(n))
	return n, err
}
func (w *metricResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	n, err := io.Copy(struct{ io.Writer }{w}, r)
	return n, err
}
func (w *metricResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *metricResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking unsupported")
	}
	return h.Hijack()
}
func (w *metricResponseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}
func (s *CatalogServer) Handler() http.Handler                            { return s.handler }
func (s *CatalogServer) SetPatchManager(manager *patcher.Manager)         { s.patches = manager }
func (s *CatalogServer) SetRandomizerManager(manager *randomizer.Manager) { s.randomizer = manager }
func (s *CatalogServer) SetDecryptor(manager *decryptor.Manager)          { s.decryptor = manager }
func (s *CatalogServer) SetMediaDirectory(path string)                    { s.mediaDir = path }
func (s *CatalogServer) SetRemoteManager(manager *remotelib.Manager)      { s.remote = manager }
func (s *CatalogServer) SetPokemonClient(client *pokemon.Client)          { s.pokemon = client }
func (s *CatalogServer) SetPokemonVault(v pokemonvault.Vault)             { s.pokemonFiles = v }
func (s *CatalogServer) SetConverter(convert func(context.Context, string, func(string, int64, int64)) error) {
	s.convert = convert
}
func (s *CatalogServer) SetConvertUpload(fn ConvertUploadFunc)      { s.convertUpload = fn }
func (s *CatalogServer) SetCryptClient(client *threedscrypt.Client) { s.crypt = client }

// SetUploadResultsDir points the convert/crypt upload flows at a directory
// already registered with the store via AddArtifactRoot: every result they
// produce is written there and reconciled into the catalog as a normal,
// temporary (expiring-unless-retained) artifact, so download links, QR
// codes, and the retain checkbox all reuse the catalog's existing machinery
// instead of a second one built just for uploads.
func (s *CatalogServer) SetUploadResultsDir(dir string) { s.uploadResultsDir = dir }
func (s *CatalogServer) SetReady(ready bool, stage string) {
	if strings.TrimSpace(stage) == "" {
		if ready {
			stage = "ready"
		} else {
			stage = "preparing catalog"
		}
	}
	s.setupStage.Store(stage)
	s.ready.Store(ready)
}
func (s *CatalogServer) Ready() bool { return s.ready.Load() }

// SetScanStage updates the setup-page progress text during the initial library
// scan. It is a no-op once the catalog is ready, so a later background rescan
// never flips a serving site back to the "setup in progress" screen.
func (s *CatalogServer) SetScanStage(stage string) {
	if !s.ready.Load() {
		s.setupStage.Store(stage)
	}
}
func (s *CatalogServer) SetupStage() string {
	if value := s.setupStage.Load(); value != nil {
		if stage, ok := value.(string); ok {
			return stage
		}
	}
	return "preparing catalog"
}
func (s *CatalogServer) SetIGDBEnabled(enabled bool)   { s.igdbEnabled = enabled }
func (s *CatalogServer) SetWebDAVEnabled(enabled bool) { s.webdavEnabled = enabled }

// SetConfig wires the loaded configuration and the directory it came from,
// for the Settings panel: it renders cfg's ini-backed fields and, on save,
// regenerates config.ini in configDir. Neither is known at construction time
// (NewCatalog takes already-extracted fields, not the whole Config), and a
// server built without calling this, every existing test, simply has no
// Settings panel to serve; adminSettings reports that rather than panicking.
func (s *CatalogServer) SetConfig(cfg config.Config, configDir string) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg = cfg
	s.configDir = configDir
}

func (s *CatalogServer) config() (config.Config, string, bool) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg, s.configDir, s.configDir != ""
}
func (s *CatalogServer) SetShutdown(fn func())                      { s.shutdown = fn }
func (s *CatalogServer) SetDownloadObserver(fn func(DownloadEvent)) { s.downloadObserver = fn }
func (s *CatalogServer) SetBackground(name string, job BackgroundJob) {
	s.jobMu.Lock()
	s.background[name] = job
	s.jobMu.Unlock()
	// A job marked Recent (finished, but kept visible briefly so a fast job
	// isn't missed by the ~1s tray poll) must expire itself here: callers set
	// Recent and move on, they don't hold a timer. Only clear it if nothing
	// replaced it in the meantime (a fresh Active run of the same name).
	if job.Recent && !job.Active {
		time.AfterFunc(15*time.Second, func() {
			s.jobMu.Lock()
			if current, ok := s.background[name]; ok && current == job {
				delete(s.background, name)
			}
			s.jobMu.Unlock()
		})
	}
}
func (s *CatalogServer) ClearBackgroundPrefix(prefix string) {
	s.jobMu.Lock()
	for name := range s.background {
		if strings.HasPrefix(name, prefix) {
			delete(s.background, name)
		}
	}
	s.jobMu.Unlock()
}
func (s *CatalogServer) ClearBackground(name string) {
	s.jobMu.Lock()
	delete(s.background, name)
	s.jobMu.Unlock()
}
func (s *CatalogServer) backgroundSnapshot() map[string]BackgroundJob {
	s.jobMu.RLock()
	defer s.jobMu.RUnlock()
	out := make(map[string]BackgroundJob, len(s.background))
	for k, v := range s.background {
		out[k] = v
	}
	return out
}
func (s *CatalogServer) SetScanState(active bool, err error) {
	s.scanning.Store(active)
	if err != nil {
		s.lastError.Store(err.Error())
	}
}
func (s *CatalogServer) adminOK(r *http.Request) bool {
	// A logged-in admin account is the primary path.
	if u := currentUser(r); u != nil && u.Role == "admin" {
		return true
	}
	// The admin token remains accepted for compatibility and headless access.
	provided := r.URL.Query().Get("admin_token")
	if provided == "" {
		if c, err := r.Cookie("romrelay_admin"); err == nil {
			provided = c.Value
		}
	}
	return s.adminToken != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(s.adminToken)) == 1
}
func (s *CatalogServer) publicPatches(w http.ResponseWriter, r *http.Request) {
	if s.patches == nil {
		http.Error(w, "patch engine unavailable", http.StatusServiceUnavailable)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "could not start patch session", 500)
		return
	}
	csrf := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: "romrelay_patch_csrf", Value: csrf, Path: "/", SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(s.base, "https://")})
	// This page mints its own per-visit token paired with that cookie: the
	// workspace is open to anyone who can reach the relay, so it must not borrow
	// the admin CSRF token the rest of pageData carries.
	data := s.newPageData(r)
	data.CSRF = csrf
	s.render(w, "patches", "patches", data)
}
func (s *CatalogServer) publicPatchFiles(w http.ResponseWriter, r *http.Request) {
	if s.patches == nil {
		http.Error(w, "patch engine unavailable", 503)
		return
	}
	recipes, err := s.patches.Recipes(r.Context())
	if err != nil {
		http.Error(w, "patch index unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"patch_recipes": recipes})
}
func (s *CatalogServer) publicPatchCSRFOK(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie("romrelay_patch_csrf")
	provided := r.Header.Get("X-CSRF-Token")
	if err != nil || len(c.Value) != 64 || subtle.ConstantTimeCompare([]byte(c.Value), []byte(provided)) != 1 {
		http.Error(w, "invalid patch session; reload the workspace", http.StatusForbidden)
		return false
	}
	return true
}
func (s *CatalogServer) createPublicPatchJob(w http.ResponseWriter, r *http.Request) {
	if s.patches == nil {
		http.Error(w, "patch engine unavailable", 503)
		return
	}
	if !s.publicPatchCSRFOK(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.patches.MaxROMBytes()+s.patches.MaxPatchBytes()+(8<<20)+(512<<10))
	if err := r.ParseMultipartForm(s.patches.MaxPatchBytes()); err != nil {
		http.Error(w, "invalid or oversized multipart request", 400)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	o := patcher.CreateOptions{ArtifactID: r.FormValue("artifact_id"), PatchID: r.FormValue("patch_id"), OutputName: r.FormValue("output_filename"), Title: r.FormValue("title"), Developer: r.FormValue("developer"), Publisher: r.FormValue("publisher"), Description: r.FormValue("description"), ReleaseDate: r.FormValue("release_date"), Genre: r.FormValue("genre"), Region: r.FormValue("region"), Public: true}
	var err error
	// A base ROM comes from an upload or from an existing catalog artifact,
	// this is a personal library, so patching a copy of a ROM you already have
	// beats asking you to re-upload it. The picked artifact is never touched:
	// Manager.Create copies it into the job's own working file.
	if o.ArtifactID == "" {
		var base multipart.File
		var baseHeader *multipart.FileHeader
		base, baseHeader, err = r.FormFile("rom")
		if err != nil {
			http.Error(w, "choose a ROM from the library or upload one", 400)
			return
		}
		defer base.Close()
		o.SourceUpload, err = io.ReadAll(io.LimitReader(base, s.patches.MaxROMBytes()+1))
		if err != nil || int64(len(o.SourceUpload)) > s.patches.MaxROMBytes() {
			http.Error(w, "base ROM exceeds configured limit", http.StatusRequestEntityTooLarge)
			return
		}
		o.SourceName = baseHeader.Filename
	}
	if file, header, fileErr := r.FormFile("patch"); fileErr == nil {
		defer file.Close()
		o.Upload, err = io.ReadAll(io.LimitReader(file, s.patches.MaxPatchBytes()+1))
		if err != nil || int64(len(o.Upload)) > s.patches.MaxPatchBytes() {
			http.Error(w, "patch exceeds configured limit", http.StatusRequestEntityTooLarge)
			return
		}
		o.UploadName = header.Filename
	}
	if o.PatchID == "" && len(o.Upload) == 0 {
		http.Error(w, "choose an indexed recipe or upload a patch file", 400)
		return
	}
	if file, _, fileErr := r.FormFile("cover"); fileErr == nil {
		defer file.Close()
		o.Cover, err = io.ReadAll(io.LimitReader(file, (8<<20)+1))
		if err != nil || len(o.Cover) > 8<<20 {
			http.Error(w, "cover exceeds 8 MiB limit", http.StatusRequestEntityTooLarge)
			return
		}
	} else if !errors.Is(fileErr, http.ErrMissingFile) {
		http.Error(w, "invalid cover upload", http.StatusBadRequest)
		return
	}
	job, err := s.patches.Create(r.Context(), o)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(job)
}
func (s *CatalogServer) getPublicPatchJob(w http.ResponseWriter, r *http.Request) {
	if s.patches == nil {
		http.Error(w, "patch engine unavailable", 503)
		return
	}
	job, err := s.patches.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Public polling never exposes server paths and cannot publish, retain, or delete.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}
func (s *CatalogServer) adminPatches(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, s.word("admin authentication required", "the patch silo needs its secret token"), http.StatusUnauthorized)
		return
	}
	if s.adminTokenLanding(w, r, "/admin/patches") {
		return
	}
	data := adminPage{
		pageData:     s.newPageData(r),
		RemoteQuery:  r.URL.Query().Get("remote_q"),
		HasRemote:    s.remote != nil,
		HasConverter: s.convert != nil,
	}
	s.render(w, "admin", "admin", data)
}

// adminTokenLanding exchanges an ?admin_token= in the URL for the admin cookie
// and reloads the page without it, reporting whether it handled the request.
//
// An admin page fills itself from /api/admin/… and those fetches carry no query
// string, so a token-only admin who did not get the cookie would land on a page
// where every request 401s.
func (s *CatalogServer) adminTokenLanding(w http.ResponseWriter, r *http.Request, path string) bool {
	if r.URL.Query().Get("admin_token") == "" {
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: "romrelay_admin", Value: s.adminToken, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.base, "https://"), SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, path, http.StatusSeeOther)
	return true
}

// adminPage is the operator view. The two flags gate the sections that depend on
// optional wiring: a relay with no SFTP source or no 3DS converter would
// otherwise show a form whose every submission is a 503.
type adminPage struct {
	pageData
	RemoteQuery  string
	HasRemote    bool
	HasConverter bool
}

func (s *CatalogServer) adminShutdown(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.shutdown == nil {
		http.Error(w, "shutdown is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("ROM Relay is shutting down cleanly.\n"))
	go s.shutdown()
}

func (s *CatalogServer) convert3DS(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.convert == nil {
		http.Error(w, "3DS to CIA conversion is unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	a, err := s.store.Artifact(r.Context(), id)
	if err != nil {
		http.Error(w, "catalog file not found", http.StatusNotFound)
		return
	}
	if ext := strings.ToLower(a.Extension); ext != ".3ds" && ext != ".cci" {
		http.Error(w, "only .3ds and .cci files can be converted to CIA", http.StatusUnprocessableEntity)
		return
	}
	key := "Conversion · " + id
	s.jobMu.Lock()
	if current, ok := s.background[key]; ok && current.Active {
		s.jobMu.Unlock()
		http.Error(w, "this file is already being converted", http.StatusConflict)
		return
	}
	s.background[key] = BackgroundJob{Active: true, Message: "Converting " + a.Name + " to CIA"}
	s.jobMu.Unlock()
	go func() {
		err := s.convert(context.Background(), id, func(part string, done, total int64) {
			s.SetBackground(key, BackgroundJob{Active: true, Message: "Converting " + a.Name + " · " + part, Done: done, Total: total})
		})
		if err != nil {
			s.SetBackground(key, BackgroundJob{Recent: true, Message: "Conversion failed · " + a.Name, Error: err.Error()})
			return
		}
		s.SetBackground(key, BackgroundJob{Recent: true, Message: "CIA ready · " + strings.TrimSuffix(a.Name, filepath.Ext(a.Name)) + ".cia", Done: 1, Total: 1})
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "queued", "artifact_id": id})
}
func (s *CatalogServer) remoteSearch(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", 401)
		return
	}
	if s.remote == nil {
		http.Error(w, "remote search is disabled", 503)
		return
	}
	results, err := s.remote.Search(r.Context(), r.URL.Query().Get("platform"), r.URL.Query().Get("q"))
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}
func (s *CatalogServer) createRemoteJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.remote == nil {
		http.Error(w, "remote search is disabled", 503)
		return
	}
	job, err := s.remote.Create(r.Context(), r.FormValue("result_id"), r.FormValue("retain") == "true", r.FormValue("destination"), r.FormValue("encryption"))
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(job)
}
func (s *CatalogServer) getRemoteJob(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", 401)
		return
	}
	if s.remote == nil {
		http.Error(w, "remote search is disabled", 503)
		return
	}
	job, err := s.remote.Get(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}
func (s *CatalogServer) cancelRemoteJob(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.remote == nil {
		http.Error(w, "remote search is disabled", 503)
		return
	}
	if err := s.remote.Cancel(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *CatalogServer) patchFiles(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, s.word("admin authentication required", "the patch silo needs its secret token"), http.StatusUnauthorized)
		return
	}
	if s.patches != nil {
		recipes, err := s.patches.Recipes(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"patch_recipes": recipes})
		return
	}
	supported := map[string]bool{".ips": true, ".ups": true, ".bps": true, ".ppf": true, ".rup": true, ".aps": true, ".bdf": true, ".pmsr": true, ".vcdiff": true}
	type item struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		Format string `json:"format"`
	}
	var files []item
	_ = filepath.WalkDir(s.patchDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !supported[ext] {
			return nil
		}
		st, e := d.Info()
		if e != nil {
			return nil
		}
		rel, e := filepath.Rel(s.patchDir, path)
		if e == nil {
			files = append(files, item{filepath.ToSlash(rel), st.Size(), strings.TrimPrefix(ext, ".")})
		}
		return nil
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"patch_files": files})
}

func (s *CatalogServer) adminGames(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	games, err := s.store.Games(r.Context())
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"games": games})
}
func (s *CatalogServer) adminMutationOK(w http.ResponseWriter, r *http.Request) bool {
	if !s.adminCSRFOK(w, r) {
		return false
	}
	if s.patches == nil {
		http.Error(w, "patch engine unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}
func (s *CatalogServer) adminCSRFOK(w http.ResponseWriter, r *http.Request) bool {
	if !s.adminOK(r) {
		http.Error(w, s.word("admin authentication required", "the patch silo needs its secret token"), 401)
		return false
	}
	// A bearer API key is never attached automatically by a browser, so there
	// is no ambient credential for a forged cross-site request to ride. CSRF
	// tokens only defend the cookie session below.
	if usedAPIKey(r) {
		return true
	}
	provided := r.Header.Get("X-CSRF-Token")
	if provided == "" {
		provided = r.FormValue("csrf_token")
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(s.csrf)) != 1 {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

func (s *CatalogServer) prepareUnencrypted(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !s.adminCSRFOK(w, r) {
		return
	}
	if s.decryptor == nil {
		http.Error(w, "3DS decryptor unavailable", http.StatusServiceUnavailable)
		return
	}
	_, err := s.decryptor.Prepare(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	http.Redirect(w, r, "/download/"+url.PathEscape(r.PathValue("id"))+tokenQuery(r), http.StatusSeeOther)
}
func (s *CatalogServer) createPatchJob(w http.ResponseWriter, r *http.Request) {
	if !s.adminMutationOK(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.patches.MaxPatchBytes()+(8<<20)+(256<<10))
	if err := r.ParseMultipartForm(s.patches.MaxPatchBytes()); err != nil {
		http.Error(w, "invalid or oversized multipart request", 400)
		return
	}
	o := patcher.CreateOptions{ArtifactID: r.FormValue("artifact_id"), PatchID: r.FormValue("patch_id"), LocalPatch: r.FormValue("local_patch"), OutputName: r.FormValue("output_filename"), Title: r.FormValue("title"), Developer: r.FormValue("developer"), Publisher: r.FormValue("publisher"), Description: r.FormValue("description"), ReleaseDate: r.FormValue("release_date"), Genre: r.FormValue("genre"), Region: r.FormValue("region"), Retain: r.FormValue("retain") == "true"}
	if file, header, err := r.FormFile("patch"); err == nil {
		defer file.Close()
		data, readErr := io.ReadAll(io.LimitReader(file, s.patches.MaxPatchBytes()+1))
		if readErr != nil || int64(len(data)) > s.patches.MaxPatchBytes() {
			http.Error(w, "uploaded patch exceeds configured limit", http.StatusRequestEntityTooLarge)
			return
		}
		o.Upload = data
		o.UploadName = header.Filename
	}
	if file, _, err := r.FormFile("cover"); err == nil {
		defer file.Close()
		data, readErr := io.ReadAll(io.LimitReader(file, (8<<20)+1))
		if readErr != nil || len(data) > 8<<20 {
			http.Error(w, "cover exceeds 8 MiB limit", http.StatusRequestEntityTooLarge)
			return
		}
		o.Cover = data
	}
	job, err := s.patches.Create(r.Context(), o)
	if err != nil {
		status := 400
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(job)
}

func (s *CatalogServer) updateGameMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (8<<20)+(64<<10))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "invalid or oversized metadata form", http.StatusBadRequest)
		return
	}
	g, err := s.store.Game(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	fields := map[string]*string{
		"title": &g.Title, "developer": &g.Developer, "publisher": &g.Publisher,
		"description": &g.Description, "region": &g.Region, "release_date": &g.ReleaseDate,
		"genre": &g.Genre, "players": &g.Players, "rating": &g.Rating,
	}
	limits := map[string]int{"title": 240, "developer": 240, "publisher": 240, "description": 16 << 10, "region": 120, "release_date": 80, "genre": 240, "players": 80, "rating": 80}
	for name, dst := range fields {
		value := strings.TrimSpace(r.FormValue(name))
		if len(value) > limits[name] {
			http.Error(w, name+" is too long", http.StatusBadRequest)
			return
		}
		*dst = value
	}
	if g.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if s.mediaDir == "" {
		http.Error(w, "artwork directory unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.FormValue("remove_cover") == "true" {
		for _, ext := range []string{".png", ".jpg", ".webp"} {
			_ = os.Remove(filepath.Join(s.mediaDir, g.ID+ext))
		}
		g.ArtworkURL = ""
	}
	if file, _, fileErr := r.FormFile("cover"); fileErr == nil {
		defer file.Close()
		cover, readErr := io.ReadAll(io.LimitReader(file, (8<<20)+1))
		if readErr != nil || len(cover) > 8<<20 {
			http.Error(w, "cover exceeds 8 MiB limit", http.StatusRequestEntityTooLarge)
			return
		}
		ext, validateErr := patcher.ValidateCover(cover)
		if validateErr != nil {
			http.Error(w, validateErr.Error(), http.StatusUnsupportedMediaType)
			return
		}
		if err = s.publishManualCover(g.ID, ext, cover); err != nil {
			http.Error(w, "could not publish cover", http.StatusInternalServerError)
			return
		}
		g.ArtworkURL = "/media/" + g.ID + "/cover"
	} else if fileErr != nil && !errors.Is(fileErr, http.ErrMissingFile) {
		http.Error(w, "invalid cover upload", http.StatusBadRequest)
		return
	}
	if err = s.store.UpdateManualGameMetadata(r.Context(), g, time.Now()); err != nil {
		http.Error(w, "could not save metadata", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(g)
}

func (s *CatalogServer) publishManualCover(id, ext string, cover []byte) error {
	if err := os.MkdirAll(s.mediaDir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.mediaDir, id+"-cover-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(cover)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	dst := filepath.Join(s.mediaDir, id+ext)
	if err = os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(dst)
		if err = os.Rename(tmpPath, dst); err != nil {
			return err
		}
	}
	ok = true
	for _, other := range []string{".png", ".jpg", ".webp"} {
		if other != ext {
			_ = os.Remove(filepath.Join(s.mediaDir, id+other))
		}
	}
	return nil
}
func (s *CatalogServer) getPatchJob(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", 401)
		return
	}
	if s.patches == nil {
		http.Error(w, "patch engine unavailable", 503)
		return
	}
	job, err := s.patches.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}
func (s *CatalogServer) deletePatchJob(w http.ResponseWriter, r *http.Request) {
	if !s.adminMutationOK(w, r) {
		return
	}
	if err := s.patches.Delete(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *CatalogServer) publishPatchJob(w http.ResponseWriter, r *http.Request) {
	if !s.adminMutationOK(w, r) {
		return
	}
	job, err := s.patches.Publish(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}
func (s *CatalogServer) retainPatchJob(w http.ResponseWriter, r *http.Request) {
	if !s.adminMutationOK(w, r) {
		return
	}
	job, err := s.patches.Retain(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}
func (s *CatalogServer) word(clean, corn string) string {
	if s.theme == "corn" {
		return corn
	}
	return clean
}
func (s *CatalogServer) withToken(path string) string {
	u := s.base + path
	if s.token != "" {
		u += "?token=" + url.QueryEscape(s.token)
	}
	return u
}
func (s *CatalogServer) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !s.ready.Load() {
		// The catalog does not exist yet, so there is nothing to link to and no
		// launcher worth showing: the page watches readiness and reloads itself.
		data := setupPage{pageData: s.newPageData(r), Stage: s.SetupStage()}
		data.Bare = true
		s.render(w, "setup", "setup", data)
		return
	}
	games, err := s.store.Games(r.Context())
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	// One release, one card: the same game in several file forms (encrypted and
	// unencrypted CIA) is merged here, and the game view offers the forms.
	games = mergeGames(games)
	filter := parseCatalogFilter(r.URL.Query())
	data := catalogPage{
		pageData:         s.newPageData(r),
		Games:            filterGames(games, filter),
		Filter:           filter,
		Facets:           catalogFacets(games, filter),
		ConvertAvailable: s.convertUpload != nil,
		CryptAvailable:   s.cryptAvailable(),
	}
	// The encrypted/unencrypted distinction only bites when 3DS content is in
	// view, so the explanation appears exactly then.
	data.Show3DSGuide = filter.Platforms["3ds"]
	if s.remote != nil && filter.Query != "" && len(data.Games) == 0 {
		data.RemoteURL = "/admin/patches?remote_q=" + url.QueryEscape(filter.Query)
	}
	// htmx asks for the same URL and selects #game-grid out of the response, so
	// one handler serves both the page and the swap. There is no fragment
	// endpoint that could drift from the page it mirrors.
	s.render(w, "catalog", "catalog", data)
}

// setupPage is the first-boot view, shown while the initial index runs.
type setupPage struct {
	pageData
	Stage string
}

// catalogPage is the library view. It embeds pageData so the shell's fields and
// the page's own sit in one namespace for the template.
type catalogPage struct {
	pageData
	Games            []catalog.Game
	Filter           catalogFilter
	Facets           catalogFacetSet
	RemoteURL        string
	Show3DSGuide     bool
	ConvertAvailable bool
	CryptAvailable   bool
}

func searchFold(value string) string {
	value = strings.ToLower(norm.NFD.String(value))
	var folded strings.Builder
	folded.Grow(len(value))
	for _, r := range value {
		if !unicode.Is(unicode.Mn, r) {
			folded.WriteRune(r)
		}
	}
	return folded.String()
}
func (s *CatalogServer) game(w http.ResponseWriter, r *http.Request) {
	g, err := s.store.Game(r.Context(), r.PathValue("id"))
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	// Show the whole merged release, so an encrypted CIA's page also offers its
	// unencrypted sibling as a variant (and vice versa).
	if all, listErr := s.store.Games(r.Context()); listErr == nil {
		if merged, ok := mergeSiblings(all, g.ID); ok {
			g = merged
		}
	}
	// The URLs depend on the request (its viewer token) and on the deployment (the
	// advertised base), so they are resolved here rather than in a template
	// function that would need to smuggle the request in with it.
	artifacts := make([]artifactView, 0, len(g.Artifacts))
	for _, artifact := range g.Artifacts {
		artifacts = append(artifacts, artifactView{
			Artifact:    artifact,
			DownloadURL: s.downloadURL(r, artifact),
			DirectURL:   s.withToken("/d/" + artifact.ID),
			QRURL:       "/qr/" + artifact.ID + ".png" + tokenQuery(r),
		})
	}
	s.render(w, "game", "game", gamePage{
		pageData:     s.newPageData(r),
		Game:         g,
		Artifacts:    artifacts,
		CoverURL:     "/media/" + g.ID + "/cover" + tokenQuery(r),
		SynopsisNote: s.synopsisNote(g),
	})
}

// gamePage is one game's detail view.
type gamePage struct {
	pageData
	Game         catalog.Game
	Artifacts    []artifactView
	CoverURL     string
	SynopsisNote string
}

// artifactView is one downloadable file with its links already resolved.
type artifactView struct {
	catalog.Artifact
	DownloadURL string
	DirectURL   string
	QRURL       string
}

// downloadURL sends encrypted CIAs through the format chooser, which explains the
// console-versus-emulator difference before handing over a file that may not work
// where the visitor intends to use it.
func (s *CatalogServer) downloadURL(r *http.Request, a catalog.Artifact) string {
	if a.Encryption == "encrypted" {
		return "/download/" + a.ID + tokenQuery(r)
	}
	return s.withToken("/d/" + a.ID)
}

// synopsisNote explains an empty description, which is common for ROMs whose
// metadata came from a header rather than a database.
func (s *CatalogServer) synopsisNote(g catalog.Game) string {
	if g.Description != "" {
		return ""
	}
	if s.igdbEnabled {
		return s.word("No exact synopsis match was returned by IGDB for this title and platform. Local metadata and artwork are still available.", "IGDB found no exact tale for this ear, but its local label and cover are still snug in the crib.")
	}
	return s.word("No synopsis is available from local ROM headers or Libretro. Enable the optional IGDB provider for prose descriptions.", "No story scroll grew from the local header or Libretro. Enable optional IGDB if this ear needs a proper tale.")
}

func (s *CatalogServer) catalogMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if len(id) != 24 || strings.Trim(id, "0123456789abcdef") != "" || s.mediaDir == "" {
		http.NotFound(w, r)
		return
	}
	g, err := s.store.Game(r.Context(), id)
	if err != nil || g.ArtworkURL == "" {
		http.NotFound(w, r)
		return
	}
	for _, ext := range []string{".png", ".jpg", ".webp"} {
		path := filepath.Join(s.mediaDir, id+ext)
		f, e := os.Open(path)
		if e != nil {
			continue
		}
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() {
			f.Close()
			continue
		}
		defer f.Close()
		if ext == ".png" {
			w.Header().Set("Content-Type", "image/png")
		} else if ext == ".jpg" {
			w.Header().Set("Content-Type", "image/jpeg")
		} else {
			w.Header().Set("Content-Type", "image/webp")
		}
		w.Header().Set("Content-Disposition", "inline")
		http.ServeContent(w, r, "cover"+ext, st.ModTime(), f)
		return
	}
	http.NotFound(w, r)
}

func humanBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), "KiB"
	for _, next := range []string{"MiB", "GiB", "TiB"} {
		value /= 1024
		if value < 1024 {
			break
		}
		suffix = next
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}

func (s *CatalogServer) downloadChoice(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.Artifact(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	if a.Encryption != "encrypted" {
		http.Redirect(w, r, "/d/"+a.ID+tokenQuery(r), http.StatusSeeOther)
		return
	}
	g, err := s.store.Game(r.Context(), a.GameID)
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	var mobile *catalog.Artifact
	for i := range g.Artifacts {
		if g.Artifacts[i].Encryption == "unencrypted" {
			copy := g.Artifacts[i]
			mobile = &copy
			break
		}
	}
	ready, reason := false, "driver is not installed"
	if s.decryptor != nil {
		ready, reason = s.decryptor.Available()
	}
	data := choicePage{
		pageData:      s.newPageData(r),
		Artifact:      a,
		EncryptedURL:  s.withToken("/d/" + a.ID),
		Admin:         s.adminOK(r),
		DecryptReady:  ready,
		DecryptReason: reason,
	}
	if mobile != nil {
		data.MobileURL = s.withToken("/d/" + mobile.ID)
	}
	s.render(w, "choice", "choice", data)
}

// choicePage explains the encrypted/unencrypted CIA difference before handing
// over a file that may not work where the visitor means to use it.
type choicePage struct {
	pageData
	Artifact      catalog.Artifact
	EncryptedURL  string
	MobileURL     string
	Admin         bool
	DecryptReady  bool
	DecryptReason string
}

func (s *CatalogServer) apiGames(w http.ResponseWriter, r *http.Request) {
	games, err := s.store.Games(r.Context())
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Merged like the library view, so consumers (the FBI batch picker) see one
	// entry per release with every file form under it.
	json.NewEncoder(w).Encode(map[string]any{"games": mergeGames(games)})
}
func (s *CatalogServer) catalogHealth(w http.ResponseWriter, r *http.Request) {
	games, err := s.store.Games(r.Context())
	status := "ok"
	last := ""
	if v := s.lastError.Load(); v != nil {
		last = v.(string)
		status = "degraded"
	}
	if err != nil {
		status = "error"
		last = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	metadataStatus := "not yet updated"
	metadataUpdated := ""
	if when, providerStatus, stateErr := s.store.SchedulerState(r.Context(), "libretro_update"); stateErr == nil {
		metadataStatus = providerStatus
		metadataUpdated = when.UTC().Format(time.RFC3339)
	}
	igdbStatus := "disabled or not yet updated"
	if _, providerStatus, stateErr := s.store.SchedulerState(r.Context(), "igdb_update"); stateErr == nil {
		igdbStatus = providerStatus
	}
	if !s.ready.Load() && status == "ok" {
		status = "setup"
	}
	json.NewEncoder(w).Encode(map[string]any{"status": status, "ready": s.ready.Load(), "setup_stage": s.SetupStage(), "games": len(games), "scan_active": s.scanning.Load(), "last_error": last, "metadata_status": metadataStatus, "metadata_updated_at": metadataUpdated, "igdb_status": igdbStatus, "background_jobs": s.backgroundSnapshot()})
}
func (s *CatalogServer) catalogDownload(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.Artifact(r.Context(), r.PathValue("id"))
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	f, err := os.Open(a.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() != a.Size {
		http.Error(w, "catalog artifact changed; reconciliation required", 409)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Encoding", "identity")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(a.Name)))
	// ServeContent deliberately omits Content-Length when Content-Encoding is
	// present. Identity encoding is required for byte-stable ROM downloads, so
	// provide the full-response length explicitly. ServeContent supplies the
	// selected length for range requests.
	if r.Header.Get("Range") == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
	}
	isDownload := r.Method == http.MethodGet
	jobName := ""
	remoteIP := clientIP(r.RemoteAddr)
	if isDownload {
		done := s.store.BeginDownload(a.ID)
		defer done()
		jobName = fmt.Sprintf("Download · %s · #%d", a.Name, s.downloadSeq.Add(1))
		s.SetBackground(jobName, BackgroundJob{Active: true, Message: "sending to " + remoteIP, Total: a.Size})
		if s.downloadObserver != nil {
			s.downloadObserver(DownloadEvent{ArtifactID: a.ID, Name: a.Name, RemoteIP: remoteIP, Status: "started", Expected: a.Size})
		}
	}
	var nextProgress int64 = 1 << 20
	tracker := &downloadWriter{ResponseWriter: w, status: 200, progress: func(written int64) {
		if isDownload && written >= nextProgress {
			nextProgress = written + (1 << 20)
			s.SetBackground(jobName, BackgroundJob{Active: true, Message: "sending to " + remoteIP, Done: written, Total: a.Size})
		}
	}}
	http.ServeContent(tracker, r, a.Name, st.ModTime(), f)
	if isDownload {
		expected, parseErr := a.Size, error(nil)
		if tracker.status == http.StatusPartialContent {
			expected, parseErr = strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64)
		}
		completed := parseErr == nil && (tracker.status == http.StatusOK || tracker.status == http.StatusPartialContent) && expected == tracker.written && !tracker.failed
		status, message := "aborted", "transfer stopped before completion"
		if completed {
			status, message = "completed", "completed to "+remoteIP
			touchCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.store.TouchCompletedDownload(touchCtx, a.ID, s.retention)
			cancel()
		}
		// Keep the finished row visible briefly so fast LAN transfers are not
		// missed by the browser/TUI polling interval.
		s.SetBackground(jobName, BackgroundJob{Recent: true, Message: message, Done: tracker.written, Total: expected})
		if s.downloadObserver != nil {
			s.downloadObserver(DownloadEvent{ArtifactID: a.ID, Name: a.Name, RemoteIP: remoteIP, Status: status, Written: tracker.written, Expected: expected})
		}
	}
}

func clientIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	if strings.TrimSpace(address) == "" {
		return "unknown client"
	}
	return address
}

type downloadWriter struct {
	http.ResponseWriter
	status   int
	written  int64
	failed   bool
	progress func(int64)
}

func (w *downloadWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *downloadWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	if w.progress != nil {
		w.progress(w.written)
	}
	if err != nil || n != len(p) {
		w.failed = true
	}
	return n, err
}
func (s *CatalogServer) catalogQR(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if !strings.HasSuffix(file, ".png") {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSuffix(file, ".png")
	if _, err := s.store.Artifact(r.Context(), id); err != nil {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(s.withToken("/d/"+id), qrcode.Highest, -10)
	if err != nil {
		http.Error(w, "QR generation failed", 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	w.Write(png)
}
func (s *CatalogServer) ciaURLs(ctx context.Context) ([]string, error) {
	return s.ciaURLsSelected(ctx, nil)
}

func (s *CatalogServer) ciaURLsSelected(ctx context.Context, selected map[string]bool) ([]string, error) {
	games, err := s.store.Games(ctx)
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, g := range games {
		for _, a := range g.Artifacts {
			// FBI batches intentionally exclude unencrypted/mobile CIAs. They keep
			// their normal direct URL and QR on the game page.
			if strings.EqualFold(a.Extension, ".cia") && a.Encryption != "unencrypted" && (len(selected) == 0 || selected[a.ID]) {
				urls = append(urls, s.withToken("/d/"+a.ID))
			}
		}
	}
	sort.Strings(urls)
	return urls, nil
}

func selectedArtifactIDs(raw string) map[string]bool {
	selected := map[string]bool{}
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id != "" && len(id) <= 128 {
			selected[id] = true
		}
	}
	return selected
}

func (s *CatalogServer) fbiBatches(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("ids")
	selected := selectedArtifactIDs(raw)
	if len(selected) == 0 {
		http.Error(w, "select at least one encrypted CIA", http.StatusBadRequest)
		return
	}
	urls, err := s.ciaURLsSelected(r.Context(), selected)
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	// Selecting no FBI-installable CIAs is worth a warning, not a hard block,
	// other selection actions (e.g. plain downloads) still work either way.
	// The client renders "files":0 as a note rather than treating it as a
	// failed request.
	batches := splitFBI(urls, fbiQRPayloadMax)
	query := url.Values{"ids": []string{raw}}
	if token := r.URL.Query().Get("token"); token != "" {
		query.Set("token", token)
	}
	qrs := make([]string, len(batches))
	for i := range batches {
		qrs[i] = fmt.Sprintf("/qr/fbi/%d.png?%s", i+1, query.Encode())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"count": len(batches), "files": len(urls), "qr_codes": qrs})
}

// FBI reads a QR as a newline-separated list and installs every URL in it
// (source/fbi/action/installurl.c), so one code can carry a whole batch. Two
// limits decide how many:
//
//   - FBI stores at most INSTALL_URLS_MAX = 128 URLs, and ignores the rest.
//   - The 3DS camera is 400x240, so the symbol has about 240px to live in. Denser
//     QR versions need more pixels per module than that leaves, and simply do not
//     scan, capacity the format allows on paper is unusable here.
//
// fbiQRPayloadMax keeps a batch at roughly QR version 16 (81 modules, ~3px per
// module in frame), which is the point where reliable scanning stops. It was 180
// bytes, which is under three of these URLs, the "batch" was one QR per ROM.
const (
	fbiQRPayloadMax = 450
	fbiURLsPerQR    = 128
)

// fbiPayloadURL shortens a URL for a QR by dropping the scheme FBI puts back on
// its own: it prefixes "http://" to any line that lacks one. That is seven
// characters off every URL, which is most of a sixth URL per code.
//
// An https relay keeps its scheme, since FBI would otherwise downgrade it.
func fbiPayloadURL(u string) string {
	return strings.TrimPrefix(u, "http://")
}

// splitFBI packs URLs into as few scannable batches as possible, in order.
func splitFBI(urls []string, max int) [][]string {
	var batches [][]string
	for _, u := range urls {
		u = fbiPayloadURL(u)
		current := len(batches) - 1
		switch {
		case current < 0:
			batches = append(batches, []string{u})
		case len(batches[current]) >= fbiURLsPerQR,
			len(strings.Join(append(batches[current], u), "\n"))+1 > max:
			batches = append(batches, []string{u})
		default:
			batches[current] = append(batches[current], u)
		}
	}
	return batches
}
func (s *CatalogServer) fbiBatchQR(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("batch")
	if !strings.HasSuffix(file, ".png") {
		http.NotFound(w, r)
		return
	}
	n, err := strconv.Atoi(strings.TrimSuffix(file, ".png"))
	if err != nil || n < 1 {
		http.NotFound(w, r)
		return
	}
	selected := selectedArtifactIDs(r.URL.Query().Get("ids"))
	urls, err := s.ciaURLsSelected(r.Context(), selected)
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	batches := splitFBI(urls, fbiQRPayloadMax)
	if n > len(batches) {
		http.NotFound(w, r)
		return
	}
	payload := strings.Join(batches[n-1], "\n") + "\n"
	png, err := qrcode.Encode(payload, qrcode.Medium, -8)
	if err != nil {
		http.Error(w, "QR generation failed", 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-ROMRelay-Batch", fmt.Sprintf("%d/%d", n, len(batches)))
	w.Write(png)
}
func (s *CatalogServer) catalogQueue(w http.ResponseWriter, r *http.Request) {
	target := net.ParseIP(strings.TrimSpace(r.FormValue("target")))
	if target == nil || target.To4() == nil || !target.IsPrivate() {
		http.Error(w, "enter the private IPv4 address shown by FBI", 400)
		return
	}
	selected := selectedArtifactIDs(r.FormValue("artifact_ids"))
	urls, err := s.ciaURLsSelected(r.Context(), selected)
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	// As with the QR batch: none of the selection being FBI-installable is a
	// warning for the client to show, not a reason to reject the request.
	if len(urls) > 0 {
		payload := []byte(strings.Join(urls, "\n") + "\n")
		if err = sendFBIQueue(net.JoinHostPort(target.String(), "5000"), payload); err != nil {
			http.Error(w, "could not queue files in FBI", 502)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"queued": len(urls)})
}
func tokenAmp(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return "&token=" + url.QueryEscape(t)
	}
	return ""
}
