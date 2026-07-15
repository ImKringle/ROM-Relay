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
	"html/template"
	"io"
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
	"golang.org/x/text/unicode/norm"
	"romrelay/internal/catalog"
	"romrelay/internal/decryptor"
	patcher "romrelay/internal/patch"
	remotelib "romrelay/internal/remote"
)

type CatalogServer struct {
	store                *catalog.Store
	base, token, theme   string
	adminToken, patchDir string
	mediaDir             string
	retention            time.Duration
	patches              *patcher.Manager
	decryptor            *decryptor.Manager
	remote               *remotelib.Manager
	igdbEnabled          bool
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
}
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

const setupHTML = `<!doctype html><html><head><meta charset=utf-8><meta name=viewport content="width=device-width"><meta name=theme-color content="#080b12"><title>Setup in progress · ROM Relay</title><style>:root{color-scheme:dark;--bg:#080b12;--surface:#111827;--text:#f7f9fc;--muted:#94a3b8;--line:#26334a;--accent:#67e8f9}*{box-sizing:border-box}body{margin:0;background:radial-gradient(900px 520px at 82% -10%,#243c672e,transparent 65%),var(--bg);color:var(--text);font:16px/1.55 system-ui,sans-serif}body.corn{--bg:#151004;--surface:#29200b;--line:#5b4918;--accent:#fde047}.setup{min-height:100vh;display:grid;place-items:center;padding:24px}.card{width:min(520px,100%);padding:clamp(28px,7vw,54px);text-align:center;border:1px solid var(--line);border-radius:20px;background:var(--surface);box-shadow:0 22px 70px #0008}.spinner{width:54px;height:54px;margin:0 auto 22px;border:5px solid var(--line);border-top-color:var(--accent);border-radius:50%;animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}h1{margin:.35rem 0;font-size:clamp(2rem,7vw,3.5rem)}.eyebrow{color:var(--muted);font-size:.73rem;font-weight:750;letter-spacing:.13em;text-transform:uppercase}.muted{color:var(--muted)}progress{width:100%;height:8px;accent-color:var(--accent)}</style></head><body class="{{.Theme}}"><main class=setup><section class=card><div class=spinner role=status aria-label="Setup in progress"></div><span class=eyebrow>Fresh boot preparation</span><h1>Setup in progress.</h1><p class=muted id=stage>{{.Stage}}</p><progress aria-label="Indeterminate setup progress"></progress><p class=muted>The catalog will appear automatically when ROM Relay is ready.</p></section></main><script>(()=>{const suffix='{{.Token}}';async function poll(){try{const r=await fetch('/healthz'+suffix),h=await r.json();if(h.ready){location.reload();return}document.getElementById('stage').textContent=h.setup_stage||'Preparing catalog'}catch{}setTimeout(poll,1000)}poll()})()</script></body></html>`

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
	s := &CatalogServer{store: store, base: strings.TrimRight(base, "/"), token: token, theme: theme, adminToken: adminToken, patchDir: patchDir, retention: retention, csrf: hex.EncodeToString(csrfSum[:]), background: map[string]BackgroundJob{}}
	s.ready.Store(true)
	s.setupStage.Store("ready")
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
	m.HandleFunc("GET /patches", s.publicPatches)
	m.HandleFunc("GET /api/patch-files", s.publicPatchFiles)
	m.HandleFunc("POST /api/patch-jobs", s.createPublicPatchJob)
	m.HandleFunc("GET /api/patch-jobs/{id}", s.getPublicPatchJob)
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
	m.HandleFunc("GET /assets/admin.js", s.adminJSV2)
	m.HandleFunc("GET /assets/catalog.js", s.catalogJSV2)
	m.HandleFunc("GET /assets/patch.js", s.publicPatchJS)
	s.handler = s.observeHTTP(catalogSecurity(m, token, theme))
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
func (s *CatalogServer) Handler() http.Handler                       { return s.handler }
func (s *CatalogServer) SetPatchManager(manager *patcher.Manager)    { s.patches = manager }
func (s *CatalogServer) SetDecryptor(manager *decryptor.Manager)     { s.decryptor = manager }
func (s *CatalogServer) SetMediaDirectory(path string)               { s.mediaDir = path }
func (s *CatalogServer) SetRemoteManager(manager *remotelib.Manager) { s.remote = manager }
func (s *CatalogServer) SetConverter(convert func(context.Context, string, func(string, int64, int64)) error) {
	s.convert = convert
}
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
func (s *CatalogServer) SetupStage() string {
	if value := s.setupStage.Load(); value != nil {
		if stage, ok := value.(string); ok {
			return stage
		}
	}
	return "preparing catalog"
}
func (s *CatalogServer) SetIGDBEnabled(enabled bool)                { s.igdbEnabled = enabled }
func (s *CatalogServer) SetShutdown(fn func())                      { s.shutdown = fn }
func (s *CatalogServer) SetDownloadObserver(fn func(DownloadEvent)) { s.downloadObserver = fn }
func (s *CatalogServer) SetBackground(name string, job BackgroundJob) {
	s.jobMu.Lock()
	s.background[name] = job
	s.jobMu.Unlock()
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
	data := struct {
		Theme, Token, CSRF string
		Copy               themeCopy
	}{s.theme, tokenQuery(r), csrf, copyFor(s.theme)}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = template.Must(template.New("public-patch").Parse(publicPatchHTMLV2)).Execute(w, data)
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
	base, baseHeader, err := r.FormFile("rom")
	if err != nil {
		http.Error(w, "base ROM upload is required", 400)
		return
	}
	defer base.Close()
	baseBytes, err := io.ReadAll(io.LimitReader(base, s.patches.MaxROMBytes()+1))
	if err != nil || int64(len(baseBytes)) > s.patches.MaxROMBytes() {
		http.Error(w, "base ROM exceeds configured limit", http.StatusRequestEntityTooLarge)
		return
	}
	o := patcher.CreateOptions{PatchID: r.FormValue("patch_id"), OutputName: r.FormValue("output_filename"), Title: r.FormValue("title"), Developer: r.FormValue("developer"), Publisher: r.FormValue("publisher"), Description: r.FormValue("description"), ReleaseDate: r.FormValue("release_date"), Genre: r.FormValue("genre"), Region: r.FormValue("region"), SourceName: baseHeader.Filename, SourceUpload: baseBytes, Public: true}
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
	if r.URL.Query().Get("admin_token") != "" {
		http.SetCookie(w, &http.Cookie{Name: "romrelay_admin", Value: s.adminToken, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.base, "https://"), SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/admin/patches", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c := copyFor(s.theme)
	data := struct {
		Theme, CSRF string
		Copy        themeCopy
		Remote      template.HTML
	}{s.theme, s.csrf, c, template.HTML(s.remoteAdminHTML(r.URL.Query().Get("remote_q")))}
	_ = template.Must(template.New("admin").Parse(adminHTML)).Execute(w, data)
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
func (s *CatalogServer) remoteAdminHTML(query string) string {
	if s.remote == nil {
		return ""
	}
	return fmt.Sprintf(`<section class="panel admin-section" id=remote-library><div class=section-title><span class=section-icon aria-hidden=true>⌕</span><div><span class=eyebrow>Private SFTP</span><h2>Search remote library</h2></div></div><p class=muted>Search only the private SFTP source configured on this relay. A result is not cataloged until it is downloaded and validated.</p><form id=remote-search-form class="form-grid two"><label><span>Platform</span><select name=platform><option>gb</option><option>gbc</option><option>gba</option><option>nds</option><option>3ds</option><option>nes</option><option>snes</option><option>n64</option><option>genesis</option></select></label><label><span>Title search</span><input name=q value="%s" required minlength=2 placeholder="Pokemon Emerald"></label><button class="button primary" type=submit>Search SFTP</button></form><form id=remote-fetch-form class="form-grid two"><label><span>Selected result ID</span><input name=result_id required></label><label><span>Remote 3DS CIA type</span><select name=encryption><option value=unencrypted selected>Unencrypted / mobile emulator</option><option value=encrypted>Encrypted / FBI and real 3DS</option></select></label><label class=check-row><input type=checkbox name=retain value=true><span>Keep permanently after validation</span></label><label><span>Permanent destination</span><input name=destination placeholder="gba or 3ds/cia/unencrypted"><small>Required only when keeping the file.</small></label><button class=button type=submit>Download and validate</button></form><pre class=result-box id=remote-result aria-live=polite></pre></section>`, template.HTMLEscapeString(query))
}

func (s *CatalogServer) adminJSV2(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	script := strings.Replace(adminJSScript, `badge=(text,kind='')=>'<span class="badge '+kind+'">'+esc(text)+'</span>'`, adminBadgeRenderer, 1)
	_, _ = io.WriteString(w, script)
	_, _ = io.WriteString(w, `;(()=>{const csrf=document.querySelector('meta[name=csrf-token]').content,host=document.querySelector('.admin-content');if(!host)return;const section=document.createElement('section');section.className='panel admin-section';section.id='convert-3ds';section.innerHTML='<div class=section-title><span class=section-icon aria-hidden=true>⇄</span><div><span class=eyebrow>Console format</span><h2>Convert 3DS / CCI to CIA</h2></div></div><p class=muted>Choose a cataloged .3ds or .cci image. ROM Relay converts a copy into an encrypted CIA under the configured 3DS CIA folder; the source is never changed.</p><form id=conversion-form class="form-grid two"><label><span>Source image</span><select name=artifact_id required><option value="">Choose a .3ds or .cci file…</option></select></label><button class="button primary" type=submit>Convert to CIA</button></form><pre class=result-box aria-live=polite>No conversion submitted yet.</pre>';host.prepend(section);const nav=document.querySelector('.admin-nav');if(nav){const link=document.createElement('a');link.href='#convert-3ds';link.textContent='⇄ 3DS conversion';nav.prepend(link)}const form=section.querySelector('form'),select=form.elements.artifact_id,out=section.querySelector('pre');fetch('/api/games').then(r=>r.json()).then(x=>{for(const g of x.games||[])for(const a of g.artifacts||[])if(['.3ds','.cci'].includes((a.extension||'').toLowerCase())){const o=document.createElement('option');o.value=a.id;o.textContent=g.title+' · '+a.name;select.appendChild(o)}});form.addEventListener('submit',async e=>{e.preventDefault();out.textContent='Starting conversion…';const id=select.value,r=await fetch('/api/admin/convert/'+encodeURIComponent(id),{method:'POST',headers:{'X-CSRF-Token':csrf}});if(!r.ok){out.textContent=await r.text();return}out.textContent='Conversion queued. Progress appears in Active Jobs and the catalog activity panel.'})})()`)
}

func (s *CatalogServer) adminJS(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", 401)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	fmt.Fprint(w, `const csrf=document.querySelector('meta[name=csrf-token]').content,f=document.getElementById('patch-form'),o=document.getElementById('result'),pr=document.getElementById('patch-recipe');fetch('/api/admin/patch-files').then(r=>r.json()).then(x=>(x.patch_recipes||[]).forEach(p=>{const z=document.createElement('option');z.value=p.ID||p.id;z.textContent=(p.Platform||p.platform)+' · '+(p.Title||p.title)+' · '+(p.ID||p.id);pr.appendChild(z)})).catch(()=>{});f.addEventListener('submit',async e=>{e.preventDefault();o.textContent='Working…';const r=await fetch('/api/admin/patch-jobs',{method:'POST',headers:{'X-CSRF-Token':csrf},body:new FormData(f)});o.textContent=await r.text()});const rs=document.getElementById('remote-search-form'),rf=document.getElementById('remote-fetch-form'),ro=document.getElementById('remote-result');if(rs){rs.addEventListener('submit',async e=>{e.preventDefault();ro.textContent='Searching…';const q=new URLSearchParams(new FormData(rs));const r=await fetch('/api/admin/remote-search?'+q);ro.textContent=await r.text()});rf.addEventListener('submit',async e=>{e.preventDefault();ro.textContent='Fetching…';const r=await fetch('/api/admin/remote-jobs',{method:'POST',headers:{'X-CSRF-Token':csrf},body:new FormData(rf)});ro.textContent=await r.text()})}`)
	fmt.Fprint(w, `const help={artifact_id:'Required. The exact catalog file to copy as the base ROM. Start typing a game or filename below.',patch_id:'Choose a permanent indexed recipe, or leave blank when uploading a one-job patch.',local_patch:'Advanced/admin only. Relative path inside the temporary root patch inbox; do not use with an upload or recipe.',patch:'One-job patch upload. Required when neither an indexed recipe nor inbox path is selected.',output_filename:'Required for uploads/inbox patches; optional when the recipe supplies one. The base ROM extension is enforced.',retain:'Admin only. Keeps the completed output permanently; unchecked outputs expire after 24 hours.',cover:'Optional PNG, JPEG, or WebP cover, at most 8 MiB.'};Object.entries(help).forEach(([n,t])=>{const el=f.elements[n];if(!el)return;const small=document.createElement('small');small.className='muted';small.textContent=t;el.closest('label')?.appendChild(small)});const aid=f.elements.artifact_id,list=document.createElement('datalist');list.id='catalog-artifacts';aid.setAttribute('list',list.id);document.body.appendChild(list);fetch('/api/games').then(r=>r.json()).then(x=>(x.games||[]).forEach(g=>(g.artifacts||[]).forEach(a=>{const z=document.createElement('option');z.value=a.id;z.label=g.title+' · '+a.name;list.appendChild(z)})));const note=document.createElement('p');note.className='muted';note.textContent='Required fields: catalog Artifact ID, one patch source (recipe, inbox path, or upload), and an output filename unless the recipe provides it. Public users have a separate temporary-only workspace.';f.prepend(note);const permanent=document.createElement('section');permanent.className='panel';permanent.innerHTML='<h2>Keep a temporary result</h2><p class=muted>Enter the completed Job ID shown by the public workspace. Only this authenticated admin action can remove its expiry.</p><form id=retain-job><label>Completed Job ID <input name=job_id required></label><button>Make permanent</button></form><pre id=retain-result></pre>';f.closest('section').after(permanent);permanent.querySelector('form').addEventListener('submit',async e=>{e.preventDefault();const id=e.target.job_id.value.trim(),r=await fetch('/api/admin/patch-jobs/'+encodeURIComponent(id)+'/retain',{method:'POST',headers:{'X-CSRF-Token':csrf}});permanent.querySelector('pre').textContent=await r.text()})`)
	fmt.Fprint(w, `const metadataPanel=document.createElement('section');metadataPanel.className='panel';metadataPanel.innerHTML='<h2>Edit game metadata</h2><p class=muted>Choose any catalog game, correct its display fields, and optionally replace or remove its cover. Saving marks the record as a manual override so Libretro and IGDB cannot overwrite it.</p><form id=metadata-form enctype=multipart/form-data><label>Game <select name=game_id required><option value="">Choose a game…</option></select></label><label>Display title <input name=title required maxlength=240></label><label>Developer <input name=developer maxlength=240></label><label>Publisher <input name=publisher maxlength=240></label><label>Description <textarea name=description rows=7 maxlength=16384></textarea></label><label>Release date <input name=release_date maxlength=80></label><label>Genre <input name=genre maxlength=240></label><label>Region <input name=region maxlength=120></label><label>Players <input name=players maxlength=80></label><label>Rating <input name=rating maxlength=80></label><label>Replacement cover <input type=file name=cover accept="image/png,image/jpeg,image/webp"></label><label><input type=checkbox name=remove_cover value=true> Remove current cover</label><button type=submit>Save manual metadata</button></form><pre id=metadata-result aria-live=polite></pre>';permanent.after(metadataPanel);const mf=metadataPanel.querySelector('form'),ms=mf.elements.game_id,mr=metadataPanel.querySelector('pre');let metadataGames=[];function fillMetadata(g){for(const n of ['title','developer','publisher','description','release_date','genre','region','players','rating'])mf.elements[n].value=g[n]||'';mf.elements.remove_cover.checked=false}fetch('/api/games').then(r=>r.json()).then(x=>{metadataGames=x.games||[];for(const g of metadataGames){const z=document.createElement('option');z.value=g.id;z.textContent=g.platform.toUpperCase()+' · '+g.title;ms.appendChild(z)}});ms.addEventListener('change',()=>{const g=metadataGames.find(x=>x.id===ms.value);if(g)fillMetadata(g)});mf.addEventListener('submit',async e=>{e.preventDefault();if(!ms.value)return;mr.textContent='Saving…';const data=new FormData(mf);data.delete('game_id');const r=await fetch('/api/admin/games/'+encodeURIComponent(ms.value)+'/metadata',{method:'POST',headers:{'X-CSRF-Token':csrf},body:data});mr.textContent=r.ok?'Saved. This game is now protected as a manual override.':await r.text();if(r.ok){const g=await r.json();const n=metadataGames.findIndex(x=>x.id===g.id);if(n>=0)metadataGames[n]=g}})`)
}
func (s *CatalogServer) catalogJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	fmt.Fprint(w, `(()=>{const box=document.getElementById('background-work');if(!box)return;const polish=document.createElement('style');polish.textContent='.filters{scrollbar-width:none}.filters::-webkit-scrollbar{display:none}@media(max-width:520px){.catalog-hero h1{font-size:2rem!important;letter-spacing:-.045em}}';document.head.append(polish);const token=new URLSearchParams(location.search).get('token'),suffix=token?'?token='+encodeURIComponent(token):'';if(!document.querySelector('a[href^="/patches"]')){const nav=document.createElement('a');nav.id='patch-workspace-link';nav.className='chip';nav.href='/patches'+suffix;nav.textContent='Patch Workspace';box.parentElement.insertBefore(nav,box.nextSibling)}async function poll(){try{const r=await fetch('/healthz'+suffix),h=await r.json(),jobs=h.background_jobs||{};const rows=Object.entries(jobs).filter(([,j])=>j.active||j.recent||j.error).map(([n,j])=>{const pct=j.total?Math.round(j.done*100/j.total)+'%':'',bar=j.total?'<progress max="'+j.total+'" value="'+j.done+'"></progress>':'';return '<div><strong>'+safe(n.replaceAll('_',' '))+'</strong> '+safe(j.message||'')+' '+pct+(j.error?' · '+safe(j.error):'')+bar+'</div>'});box.innerHTML=rows.join('');box.hidden=!rows.length}catch{}setTimeout(poll,1000)}function safe(s){const d=document.createElement('div');d.textContent=s;return d.innerHTML}poll()})()`)
}

func (s *CatalogServer) catalogJSV2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, catalogJSScript)
	_, _ = io.WriteString(w, `;(()=>{const nav=document.querySelector('nav.filters[aria-label="Platforms"]');if(!nav)return;const p=new URLSearchParams(location.search),a=document.createElement('a');a.className='chip'+(p.get('platform')==='patched'?' active':'');a.href='/?platform=patched'+(p.get('token')?'&token='+encodeURIComponent(p.get('token')):'');a.textContent='Patched';nav.appendChild(a)})()`)
	// Preserve exact QR modules in the browser. The compact preview used to
	// resample dense FBI batch symbols down to 210px.
	_, _ = io.WriteString(w, `;(()=>{const s=document.createElement('style');s.textContent='.fbi-output{align-items:flex-start}.fbi-output figure{max-width:100%}.fbi-output img{width:auto!important;height:auto;max-width:100%;image-rendering:pixelated;display:block}.fbi-output figcaption{margin-top:8px;font-weight:700}';document.head.appendChild(s)})()`)
}
func (s *CatalogServer) publicPatchJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	fmt.Fprint(w, `(()=>{const meta=document.querySelector('meta[name=csrf-token]'),form=document.getElementById('public-patch-form'),recipes=document.getElementById('patch-recipe'),out=document.getElementById('patch-result'),token=new URLSearchParams(location.search).get('token'),suffix=token?'?token='+encodeURIComponent(token):'';fetch('/api/patch-files'+suffix).then(r=>r.json()).then(data=>(data.patch_recipes||[]).forEach(p=>{const o=document.createElement('option');o.value=p.id;o.textContent=p.platform.toUpperCase()+' · '+p.title+' · '+p.extension.toUpperCase();o.dataset.output=p.output_filename||'';recipes.appendChild(o)}));recipes.addEventListener('change',()=>{const o=recipes.selectedOptions[0];if(o&&o.dataset.output&&!form.output_filename.value)form.output_filename.value=o.dataset.output});form.addEventListener('submit',async e=>{e.preventDefault();out.textContent='Uploading and validating…';const res=await fetch('/api/patch-jobs'+suffix,{method:'POST',headers:{'X-CSRF-Token':meta.content},body:new FormData(form)});if(!res.ok){out.textContent=await res.text();return}const job=await res.json();async function poll(){const r=await fetch('/api/patch-jobs/'+encodeURIComponent(job.id)+suffix),j=await r.json();out.textContent='Status: '+j.status+(j.error?'\n'+j.error:'');if(j.status==='completed'&&j.result_artifact_id){const a=document.createElement('a');a.href='/d/'+j.result_artifact_id+suffix;a.textContent='Download patched ROM';out.appendChild(document.createTextNode('\n'));out.appendChild(a)}else if(j.status==='queued'||j.status==='running'){setTimeout(poll,1000)}}poll()})})()`)
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = template.Must(template.New("setup").Parse(setupHTML)).Execute(w, map[string]string{"Theme": s.theme, "Token": tokenQuery(r), "Stage": s.SetupStage()})
		return
	}
	games, err := s.store.Games(r.Context())
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	q := searchFold(strings.TrimSpace(r.URL.Query().Get("q")))
	platform := r.URL.Query().Get("platform")
	encryption := r.URL.Query().Get("encryption")
	if platform != "3ds" || (encryption != "encrypted" && encryption != "unencrypted") {
		encryption = ""
	}
	filtered := games[:0]
	for _, g := range games {
		if platform == "" && g.ROMPatch {
			continue
		}
		if platform == "patched" && !g.ROMPatch {
			continue
		}
		if q != "" {
			haystack := g.Title + " " + g.Developer + " " + g.Publisher + " " + g.Region + " " + g.Genre
			for _, a := range g.Artifacts {
				haystack += " " + a.Name
			}
			if !strings.Contains(searchFold(haystack), q) {
				continue
			}
		}
		if platform != "" && platform != "patched" && g.Platform != platform {
			continue
		}
		if encryption != "" {
			matches := false
			for _, a := range g.Artifacts {
				if a.Encryption == encryption {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
		}
		filtered = append(filtered, g)
	}
	data := struct {
		Games                                                                               []catalog.Game
		Platforms                                                                           []catalog.Platform
		Theme, Token, TokenAmp, ViewerToken, Q, PlatformFilter, EncryptionFilter, RemoteURL string
		Copy                                                                                themeCopy
	}{filtered, catalog.Platforms, s.theme, tokenQuery(r), tokenAmp(r), r.URL.Query().Get("token"), r.URL.Query().Get("q"), platform, encryption, func() string {
		if s.remote != nil && q != "" && len(filtered) == 0 {
			return "/admin/patches?remote_q=" + url.QueryEscape(r.URL.Query().Get("q"))
		}
		return ""
	}(), copyFor(s.theme)}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	template.Must(template.New("catalog").Funcs(template.FuncMap{"media": func(id string) string { return "/media/" + id + "/cover" + tokenQuery(r) }}).Parse(catalogHTML)).Execute(w, data)
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
	data := struct {
		Game                             catalog.Game
		Theme, Token, Base, SynopsisNote string
		Copy                             themeCopy
	}{Game: g, Theme: s.theme, Token: tokenQuery(r), Base: s.base, Copy: copyFor(s.theme), SynopsisNote: func() string {
		if g.Description != "" {
			return ""
		}
		if s.igdbEnabled {
			return s.word("No exact synopsis match was returned by IGDB for this title and platform. Local metadata and artwork are still available.", "IGDB found no exact tale for this ear, but its local label and cover are still snug in the crib.")
		}
		return s.word("No synopsis is available from local ROM headers or Libretro. Enable the optional IGDB provider for prose descriptions.", "No story scroll grew from the local header or Libretro. Enable optional IGDB if this ear needs a proper tale.")
	}()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	template.Must(template.New("game").Funcs(template.FuncMap{"download": func(a catalog.Artifact) string {
		if a.Encryption == "encrypted" {
			return "/download/" + a.ID + tokenQuery(r)
		}
		return s.withToken("/d/" + a.ID)
	}, "direct": func(id string) string { return s.withToken("/d/" + id) }, "media": func(id string) string { return "/media/" + id + "/cover" + tokenQuery(r) }, "qr": func(id string) string { return "/qr/" + id + ".png" + tokenQuery(r) }, "size": humanBytes}).Parse(gameHTML)).Execute(w, data)
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
	data := struct {
		Artifact            catalog.Artifact
		Mobile              *catalog.Artifact
		Theme, Token, CSRF  string
		Admin, DecryptReady bool
		DecryptReason       string
		Copy                themeCopy
	}{a, mobile, s.theme, tokenQuery(r), s.csrf, s.adminOK(r), ready, reason, copyFor(s.theme)}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	template.Must(template.New("choice").Funcs(template.FuncMap{"direct": func(id string) string { return s.withToken("/d/" + id) }}).Parse(downloadChoiceHTML)).Execute(w, data)
}
func (s *CatalogServer) apiGames(w http.ResponseWriter, r *http.Request) {
	games, err := s.store.Games(r.Context())
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"games": games})
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
		s.SetBackground(jobName, BackgroundJob{Active: true, Recent: true, Message: message, Done: tracker.written, Total: expected})
		if s.downloadObserver != nil {
			s.downloadObserver(DownloadEvent{ArtifactID: a.ID, Name: a.Name, RemoteIP: remoteIP, Status: status, Written: tracker.written, Expected: expected})
		}
		time.AfterFunc(15*time.Second, func() { s.ClearBackground(jobName) })
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
	if len(urls) == 0 {
		http.Error(w, "none of the selected files are FBI-compatible encrypted CIAs", http.StatusBadRequest)
		return
	}
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

// FBI scans a 400x240 camera frame. Although QR itself can hold much more,
// dense symbols become unreliable on a phone/monitor long before the format's
// theoretical capacity. Keep each ordered URL batch comfortably scannable.
const fbiQRPayloadMax = 180

func splitFBI(urls []string, max int) [][]string {
	var batches [][]string
	for _, u := range urls {
		if len(batches) == 0 || len(strings.Join(append(batches[len(batches)-1], u), "\n"))+1 > max {
			batches = append(batches, []string{u})
		} else {
			batches[len(batches)-1] = append(batches[len(batches)-1], u)
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
	if len(urls) == 0 {
		http.Error(w, "no CIA files are available", 409)
		return
	}
	payload := []byte(strings.Join(urls, "\n") + "\n")
	if err = sendFBIQueue(net.JoinHostPort(target.String(), "5000"), payload); err != nil {
		http.Error(w, "could not queue files in FBI", 502)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
func tokenAmp(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return "&token=" + url.QueryEscape(t)
	}
	return ""
}
func catalogSecurity(next http.Handler, token, theme string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-transform")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'unsafe-inline'")
		adminRoute := strings.HasPrefix(r.URL.Path, "/admin/") || strings.HasPrefix(r.URL.Path, "/api/admin/")
		if !adminRoute && token != "" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ROM Relay"`)
			http.Error(w, copyFor(theme).AuthError, 401)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const adminBadgeRenderer = `icon=(kind,text)=>kind==='patched'?'<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="m14 4 6 6-9.5 9.5a2.1 2.1 0 0 1-3 0l-3-3a2.1 2.1 0 0 1 0-3Z"/><path d="m12 6 6 6"/></svg>':kind==='encryption'?(String(text).startsWith('un')?'<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10" width="14" height="10" rx="2"/><path d="M16 10V7a4 4 0 0 0-7.6-1.7"/></svg>':'<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10" width="14" height="10" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>'):'',badge=(text,kind='')=>'<span class="badge '+kind+'">'+icon(kind,text)+esc(text)+'</span>'`

const sharedCSS = `:root{color-scheme:dark;--bg:#080b12;--bg2:#0d1320;--surface:#111827;--surface2:#172033;--surface3:#202b40;--text:#f7f9fc;--muted:#94a3b8;--faint:#64748b;--accent:#67e8f9;--accent2:#818cf8;--success:#5ee2a0;--warning:#fbbf24;--line:#26334a;--shadow:0 22px 70px #0008;--radius:18px;--gutter:clamp(18px,4vw,56px)}*{box-sizing:border-box}html{scrollbar-gutter:stable}body{margin:0;min-height:100vh;background:radial-gradient(900px 520px at 82% -10%,#243c672e,transparent 65%),radial-gradient(700px 460px at -10% 30%,#0e749026,transparent 70%),linear-gradient(155deg,var(--bg),var(--bg2));color:var(--text);font:15px/1.55 Inter,ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;-webkit-font-smoothing:antialiased}body.corn{--bg:#151004;--bg2:#211a07;--surface:#29200b;--surface2:#382d0f;--surface3:#493a13;--accent:#fde047;--accent2:#f59e0b;--success:#bef264;--line:#5b4918;background:radial-gradient(900px 520px at 82% -10%,#a1620738,transparent 65%),radial-gradient(700px 460px at -10% 30%,#ca8a0424,transparent 70%),linear-gradient(155deg,var(--bg),var(--bg2))}a{color:inherit}.wrap{width:min(1480px,100%);margin:0 auto;padding:0 var(--gutter) 64px}.site-header{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:26px 0 20px}.brand{display:flex;align-items:center;gap:13px;text-decoration:none}.brand-mark{width:42px;height:42px;display:grid;place-items:center;border:1px solid color-mix(in srgb,var(--accent) 45%,var(--line));border-radius:14px;background:linear-gradient(145deg,color-mix(in srgb,var(--accent) 18%,var(--surface)),var(--surface));box-shadow:0 10px 32px color-mix(in srgb,var(--accent) 15%,transparent);color:var(--accent)}.brand strong{display:block;font-size:1.05rem;letter-spacing:-.02em}.eyebrow{color:var(--muted);font-size:.73rem;font-weight:750;letter-spacing:.13em;text-transform:uppercase}.nav-actions{display:flex;align-items:center;gap:9px}.surface,.panel{background:linear-gradient(145deg,color-mix(in srgb,var(--surface2) 88%,transparent),color-mix(in srgb,var(--surface) 96%,transparent));border:1px solid var(--line);border-radius:var(--radius);box-shadow:0 14px 50px #0003}.icon{width:16px;height:16px;display:inline-block;vertical-align:-3px;fill:none;stroke:currentColor;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round;flex:0 0 auto}.icon-lg{width:22px;height:22px}.button,.chip,.badge{display:inline-flex;align-items:center;justify-content:center;gap:7px;text-decoration:none;white-space:nowrap}.button{min-height:42px;padding:0 16px;border:1px solid var(--line);border-radius:12px;background:var(--surface2);color:var(--text);font-weight:700;cursor:pointer;transition:.18s ease}.button:hover,.button:focus-visible{border-color:var(--accent);background:var(--surface3);transform:translateY(-1px)}.button.primary{color:#071016;border-color:transparent;background:linear-gradient(135deg,var(--accent),color-mix(in srgb,var(--accent2) 65%,var(--accent)));box-shadow:0 10px 28px color-mix(in srgb,var(--accent) 18%,transparent)}body.corn .button.primary{color:#211600}.chip{padding:8px 12px;border:1px solid var(--line);border-radius:999px;background:#ffffff05;color:var(--muted);font-weight:680;font-size:.82rem;transition:.18s ease}.chip:hover,.chip:focus-visible,.chip.active{color:var(--text);border-color:color-mix(in srgb,var(--accent) 65%,var(--line));background:color-mix(in srgb,var(--accent) 10%,var(--surface))}.badge{padding:5px 9px;border:1px solid #ffffff12;border-radius:999px;background:#080b12b8;color:#dbe5f4;font-size:.7rem;font-weight:780;letter-spacing:.055em;text-transform:uppercase;backdrop-filter:blur(10px)}body.corn .badge{background:#1c1509d9}.badge.encryption{color:var(--accent);border-color:color-mix(in srgb,var(--accent) 38%,transparent)}.badge.patched{color:var(--success);border-color:color-mix(in srgb,var(--success) 38%,transparent)}h1,h2,h3,p{margin-top:0}h1{font-size:clamp(2rem,5vw,4.2rem);line-height:1.02;letter-spacing:-.055em}h2{letter-spacing:-.025em}.muted{color:var(--muted)}.dim{color:var(--faint)}input,select,textarea,button{font:inherit}input,select,textarea{width:100%;padding:12px 14px;border:1px solid var(--line);border-radius:12px;outline:none;background:#080b1288;color:var(--text);transition:border-color .18s,box-shadow .18s}input:focus,select:focus,textarea:focus{border-color:var(--accent);box-shadow:0 0 0 3px color-mix(in srgb,var(--accent) 13%,transparent)}.work{margin:0 0 18px;padding:12px 14px;border:1px solid color-mix(in srgb,var(--accent) 30%,var(--line));border-radius:14px;background:color-mix(in srgb,var(--accent) 7%,var(--surface));max-height:42vh;overflow:auto}.work>div{padding:5px 0}.work progress{display:block;width:100%;height:6px;margin-top:5px;accent-color:var(--accent)}code{display:block;max-width:100%;padding:10px 12px;border:1px solid var(--line);border-radius:10px;background:#05070c;overflow:auto;color:#cbd5e1;font:12px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace}img.qr{width:min(210px,100%);padding:8px;border-radius:14px;background:white;image-rendering:pixelated}.back-link{display:inline-flex;align-items:center;gap:7px;margin:4px 0 22px;color:var(--muted);text-decoration:none;font-weight:700}.back-link:hover{color:var(--accent)}.empty-state{grid-column:1/-1;padding:56px 24px;text-align:center}.panel{padding:clamp(20px,3vw,34px)}fieldset{border:1px solid var(--line);border-radius:14px;padding:18px}label{display:grid;gap:6px;margin-bottom:14px}@media(max-width:720px){.wrap{padding-left:16px;padding-right:16px}.site-header{align-items:flex-start}.brand .eyebrow{display:none}.nav-actions .button span{display:none}.panel{border-radius:14px}.button{min-height:40px}}`

const adminHTML = `<!doctype html><html><head><meta charset=utf-8><meta name=viewport content="width=device-width"><meta name=csrf-token content="{{.CSRF}}"><meta name=theme-color content="#080b12"><title>{{.Copy.AdminTitle}} · ROM Relay</title><style>` + sharedCSS + `.admin-hero{padding:clamp(26px,5vw,54px);margin-bottom:20px}.admin-hero h1{margin-bottom:12px}.admin-hero p{max-width:760px}.admin-layout{display:grid;grid-template-columns:220px minmax(0,1fr);gap:20px;align-items:start}.admin-nav{position:sticky;top:18px;padding:16px}.admin-nav a{display:flex;gap:9px;padding:10px 11px;border-radius:10px;color:var(--muted);text-decoration:none;font-weight:680}.admin-nav a:hover{color:var(--text);background:#ffffff08}.admin-content{display:grid;gap:20px}.admin-section{scroll-margin-top:20px}.section-title{display:flex;align-items:center;gap:13px;margin-bottom:16px}.section-title h2{margin:0}.section-icon{width:42px;height:42px;display:grid;place-items:center;border:1px solid color-mix(in srgb,var(--accent) 35%,var(--line));border-radius:12px;background:color-mix(in srgb,var(--accent) 9%,var(--surface));color:var(--accent);font-size:1.3rem}.step{padding:18px;border:1px solid var(--line);border-radius:15px;background:#ffffff03}.step-head{display:flex;align-items:center;gap:11px;margin-bottom:14px}.step-number{width:28px;height:28px;display:grid;place-items:center;border-radius:50%;background:var(--accent);color:#071016;font-weight:900}.form-grid{display:grid;gap:14px}.form-grid.two{grid-template-columns:repeat(2,minmax(0,1fr))}.form-grid label{margin:0}.form-grid small{color:var(--muted);line-height:1.4}.check-row{display:flex!important;grid-column:1/-1;align-items:center;gap:10px}.check-row input{width:auto}.advanced{margin-top:14px;border:1px solid var(--line);border-radius:14px;background:#ffffff02}.advanced summary{padding:14px 16px;cursor:pointer;color:var(--accent);font-weight:750}.advanced>div{padding:2px 16px 16px}.result-box{min-height:54px;margin:14px 0 0;padding:13px;border:1px solid var(--line);border-radius:12px;background:#05070c;white-space:pre-wrap;overflow:auto}.metadata-preview{display:grid;grid-template-columns:96px minmax(0,1fr);gap:15px;align-items:center;margin:14px 0;padding:13px;border:1px solid var(--line);border-radius:14px;background:#ffffff03}.metadata-preview img,.preview-placeholder{width:96px;aspect-ratio:3/4;object-fit:cover;border-radius:9px;background:var(--surface3)}.preview-placeholder{display:grid;place-items:center;color:var(--faint)}.preview-badges{display:flex;gap:7px;flex-wrap:wrap}.danger{border-color:#ef444455}.danger .section-icon{color:#fca5a5;border-color:#ef444455;background:#ef44440c}@media(max-width:900px){.admin-layout{grid-template-columns:1fr}.admin-nav{position:static;display:flex;overflow:auto}.admin-nav a{white-space:nowrap}}@media(max-width:620px){.form-grid.two{grid-template-columns:1fr}.metadata-preview{grid-template-columns:72px 1fr}.metadata-preview img,.preview-placeholder{width:72px}}</style><script defer src="/assets/admin.js"></script></head><body class="{{.Theme}}"><main class=wrap><header class=site-header><a class=brand href="/"><span class=brand-mark aria-hidden=true>⚙</span><span><span class=eyebrow>Private operator session</span><strong>ROM Relay Admin</strong></span></a><a class=button href="/">View catalog</a></header><section class="admin-hero surface"><span class=eyebrow>Library management</span><h1>{{.Copy.AdminTitle}}</h1><p class=muted>Build patched entries, correct metadata and artwork, manage remote imports, and control retention. Changes here affect what everyone using this relay sees.</p></section><div class=admin-layout><nav class="admin-nav surface" aria-label="Admin sections"><a href="#create-patch">◈ Create patch</a><a href="#edit-metadata">✎ Game metadata</a><a href="#keep-result">♜ Retention</a>{{if .Remote}}<a href="#remote-library">⌕ Remote library</a>{{end}}<a href="#server-control">⏻ Server</a></nav><div class=admin-content><section class="panel admin-section" id=create-patch><div class=section-title><span class=section-icon aria-hidden=true>◈</span><div><span class=eyebrow>New catalog entry</span><h2>Create a patched ROM</h2></div></div><p class=muted>Choose one catalog file as the clean base, supply exactly one patch source, then name the output. The base ROM is copied and never modified.</p><form id=patch-form class=form-grid enctype=multipart/form-data><div class=step><div class=step-head><span class=step-number>1</span><strong>Choose the clean base ROM</strong></div><label><span>Catalog game / file <b class=required>required</b></span><input id=artifact-picker name=artifact_label list=catalog-artifacts required placeholder="Start typing a game title…"><input type=hidden name=artifact_id><datalist id=catalog-artifacts></datalist><small id=artifact-help>Select a suggestion so ROM Relay can use its exact artifact ID.</small></label></div><div class=step><div class=step-head><span class=step-number>2</span><strong>Choose the patch</strong></div><div class="form-grid two"><label><span>Reusable server recipe</span><select id=patch-recipe name=patch_id><option value="">Upload a one-job patch instead</option></select><small>Recipes remember their platform, expected base checksum, and default output name.</small></label><label><span>One-job patch upload</span><input type=file name=patch accept=".ips,.ups,.bps,.ppf,.rup,.aps,.bdf,.pmsr,.vcdiff"><small>Use only when no recipe is selected.</small></label></div><details class=advanced><summary>Advanced: legacy patch inbox path</summary><div><label><span>Relative path under the root patch inbox</span><input name=local_patch placeholder="folder/file.ips"><small>For migration only. Normal reusable patches belong under platforms/&lt;platform&gt;/patches.</small></label></div></details></div><div class=step><div class=step-head><span class=step-number>3</span><strong>Name and describe the result</strong></div><div class="form-grid two"><label><span>Output filename <b class=required>required*</b></span><input name=output_filename placeholder="Pokemon Radical Red.gba"><small>*Optional only when the selected recipe provides it. The base extension is enforced.</small></label><label><span>Display title</span><input name=title placeholder="Pokemon Radical Red"><small>Optional, but strongly recommended.</small></label><label><span>Developer / patch author</span><input name=developer></label><label><span>Publisher</span><input name=publisher></label><label><span>Release date</span><input name=release_date placeholder="2026"></label><label><span>Genre</span><input name=genre></label><label><span>Region</span><input name=region placeholder="World"></label><label><span>Cover / icon artwork</span><input type=file name=cover accept="image/png,image/jpeg,image/webp"><small>Optional PNG, JPEG, or WebP up to 8 MiB. This becomes the patched card artwork.</small></label></div><label><span>Description</span><textarea name=description rows=5 placeholder="What changes in this patch?"></textarea><small>Optional. Patched games never receive a remote synopsis, so this is the best place to explain the entry.</small></label><label class=check-row><input type=checkbox name=retain value=true><span>Keep this result permanently; otherwise remove it 24 hours after creation or its latest completed download.</span></label></div><button class="button primary" type=submit>Create and validate patched ROM</button></form><pre class=result-box id=result aria-live=polite>No patch job submitted yet.</pre></section>{{.Remote}}<section class="panel admin-section" id=keep-result><div class=section-title><span class=section-icon aria-hidden=true>♜</span><div><span class=eyebrow>Retention</span><h2>Keep a temporary result</h2></div></div><p class=muted>Paste a completed Job ID from the public patch workspace. Only an administrator can remove its expiry.</p><form id=retain-job class="form-grid two"><label><span>Completed Job ID</span><input name=job_id required></label><button class=button type=submit>Make permanent</button></form><pre class=result-box id=retain-result aria-live=polite></pre></section><section class="panel admin-section" id=edit-metadata><div class=section-title><span class=section-icon aria-hidden=true>✎</span><div><span class=eyebrow>Catalog presentation</span><h2>Edit game metadata and artwork</h2></div></div><p class=muted>Choose any game. Saving creates a manual override, so Libretro and IGDB will not undo your corrections.</p><form id=metadata-form class=form-grid enctype=multipart/form-data><label><span>Game</span><select name=game_id required><option value="">Choose a game…</option></select></label><div id=metadata-preview class=metadata-preview hidden><div class=preview-placeholder aria-hidden=true>Art</div><div><strong id=metadata-preview-title></strong><div id=metadata-preview-badges class=preview-badges></div><p class=muted id=metadata-preview-source></p></div></div><div class="form-grid two"><label><span>Display title <b class=required>required</b></span><input name=title required maxlength=240></label><label><span>Developer</span><input name=developer maxlength=240></label><label><span>Publisher</span><input name=publisher maxlength=240></label><label><span>Release date</span><input name=release_date maxlength=80></label><label><span>Genre</span><input name=genre maxlength=240></label><label><span>Region</span><input name=region maxlength=120></label><label><span>Players</span><input name=players maxlength=80></label><label><span>Rating</span><input name=rating maxlength=80></label><label><span>Replacement cover / icon</span><input type=file name=cover accept="image/png,image/jpeg,image/webp"><small>Optional. PNG, JPEG, or WebP up to 8 MiB.</small></label><label class=check-row><input type=checkbox name=remove_cover value=true><span>Remove current cover</span></label></div><label><span>Description</span><textarea name=description rows=7 maxlength=16384></textarea></label><button class="button primary" type=submit>Save manual metadata</button></form><pre class=result-box id=metadata-result aria-live=polite></pre></section><section class="panel admin-section danger" id=server-control><div class=section-title><span class=section-icon aria-hidden=true>⏻</span><div><span class=eyebrow>Safe exit</span><h2>Server control</h2></div></div><form method=post action="/api/admin/shutdown"><input type=hidden name=csrf_token value="{{.CSRF}}"><button class=button type=submit>Shut down ROM Relay cleanly</button></form><p class=muted>Stops HTTP, cancels and waits for background work, checkpoints SQLite, logs completion, and then restores the terminal.</p></section></div></div></main></body></html>`

const adminJSScript = `(()=>{const csrf=document.querySelector('meta[name=csrf-token]').content,headers={'X-CSRF-Token':csrf},patchForm=document.getElementById('patch-form'),patchOut=document.getElementById('result'),recipe=document.getElementById('patch-recipe'),artifactLabel=document.getElementById('artifact-picker'),artifactID=patchForm.elements.artifact_id,artifactList=document.getElementById('catalog-artifacts');let games=[],artifacts=[];const esc=s=>{const d=document.createElement('div');d.textContent=s||'';return d.innerHTML},badge=(text,kind='')=>'<span class="badge '+kind+'">'+esc(text)+'</span>',status=(box,text,kind='')=>box.innerHTML=badge(kind||'status',kind==='failed'?'patched':'')+' '+esc(text);fetch('/api/admin/patch-files').then(r=>r.json()).then(x=>(x.patch_recipes||[]).forEach(p=>{const z=document.createElement('option'),id=p.ID||p.id,title=p.Title||p.title,platform=p.Platform||p.platform;z.value=id;z.textContent=platform.toUpperCase()+' · '+title+' · '+id;z.dataset.output=p.OutputFilename||p.output_filename||'';z.dataset.title=title;recipe.appendChild(z)})).catch(e=>status(patchOut,'Could not load recipes: '+e,'failed'));recipe.addEventListener('change',()=>{const z=recipe.selectedOptions[0];if(z?.dataset.output&&!patchForm.elements.output_filename.value)patchForm.elements.output_filename.value=z.dataset.output;if(z?.dataset.title&&!patchForm.elements.title.value)patchForm.elements.title.value=z.dataset.title});fetch('/api/admin/games').then(r=>r.json()).then(x=>{games=x.games||[];for(const g of games)for(const a of g.artifacts||[]){const label=g.platform.toUpperCase()+' · '+g.title+' · '+a.name,z=document.createElement('option');artifacts.push({label,id:a.id,game:g,artifact:a});z.value=label;artifactList.appendChild(z)}const ms=document.querySelector('#metadata-form [name=game_id]');for(const g of games){const z=document.createElement('option');z.value=g.id;z.textContent=g.platform.toUpperCase()+' · '+g.title+(g.rom_patch?' · PATCHED':'');ms.appendChild(z)}});artifactLabel.addEventListener('input',()=>{const hit=artifacts.find(a=>a.label===artifactLabel.value);artifactID.value=hit?.id||'';document.getElementById('artifact-help').textContent=hit?'Selected '+hit.artifact.name+' ('+hit.artifact.extension.toUpperCase()+')':'Choose an exact suggestion from the list.'});patchForm.addEventListener('submit',async e=>{e.preventDefault();if(!artifactID.value){status(patchOut,'Choose an exact base-ROM suggestion first.','failed');return}status(patchOut,'Uploading and validating…','queued');const r=await fetch('/api/admin/patch-jobs',{method:'POST',headers,body:new FormData(patchForm)});if(!r.ok){status(patchOut,await r.text(),'failed');return}const job=await r.json();async function poll(){const q=await fetch('/api/admin/patch-jobs/'+encodeURIComponent(job.id)),j=await q.json();status(patchOut,'Job '+j.id+' · '+j.status+(j.error?' · '+j.error:''),j.status);if(j.status==='queued'||j.status==='running')setTimeout(poll,800)}poll()});const retain=document.getElementById('retain-job'),retainOut=document.getElementById('retain-result');retain.addEventListener('submit',async e=>{e.preventDefault();const id=retain.elements.job_id.value.trim(),r=await fetch('/api/admin/patch-jobs/'+encodeURIComponent(id)+'/retain',{method:'POST',headers});retainOut.textContent=r.ok?'Permanent retention enabled for '+id:await r.text()});const mf=document.getElementById('metadata-form'),ms=mf.elements.game_id,mr=document.getElementById('metadata-result'),preview=document.getElementById('metadata-preview');function fill(g){for(const n of ['title','developer','publisher','description','release_date','genre','region','players','rating'])mf.elements[n].value=g[n]||'';mf.elements.remove_cover.checked=false;preview.hidden=false;document.getElementById('metadata-preview-title').textContent=g.title;document.getElementById('metadata-preview-source').textContent='Metadata source: '+(g.metadata_source||'local');document.getElementById('metadata-preview-badges').innerHTML=badge(g.platform)+(g.rom_patch?badge('Patched','patched'):'')+(g.encryption?badge(g.encryption+' CIA','encryption'):'');const old=preview.querySelector('img,.preview-placeholder'),next=g.artwork_url?Object.assign(document.createElement('img'),{src:'/media/'+encodeURIComponent(g.id)+'/cover',alt:'Current cover for '+g.title}):Object.assign(document.createElement('div'),{className:'preview-placeholder',textContent:'No art'});old.replaceWith(next)}ms.addEventListener('change',()=>{const g=games.find(x=>x.id===ms.value);if(g)fill(g)});mf.addEventListener('submit',async e=>{e.preventDefault();if(!ms.value)return;status(mr,'Saving manual override…','working');const data=new FormData(mf);data.delete('game_id');const r=await fetch('/api/admin/games/'+encodeURIComponent(ms.value)+'/metadata',{method:'POST',headers,body:data});if(!r.ok){status(mr,await r.text(),'failed');return}const g=await r.json(),n=games.findIndex(x=>x.id===g.id);if(n>=0)games[n]=g;fill(g);status(mr,'Saved. Providers can no longer overwrite this entry.','completed')});const rs=document.getElementById('remote-search-form'),rf=document.getElementById('remote-fetch-form'),ro=document.getElementById('remote-result');if(rs){rs.addEventListener('submit',async e=>{e.preventDefault();status(ro,'Searching configured SFTP…','working');const r=await fetch('/api/admin/remote-search?'+new URLSearchParams(new FormData(rs)));ro.textContent=await r.text()});rf.addEventListener('submit',async e=>{e.preventDefault();status(ro,'Downloading and validating…','working');const r=await fetch('/api/admin/remote-jobs',{method:'POST',headers,body:new FormData(rf)});ro.textContent=await r.text()})}})()`

const catalogJSScript = `(()=>{const token=new URLSearchParams(location.search).get('token'),suffix=token?'?token='+encodeURIComponent(token):'',amp=token?'&token='+encodeURIComponent(token):'',box=document.getElementById('background-work'),safe=s=>{const d=document.createElement('div');d.textContent=s||'';return d.innerHTML};const polish=document.createElement('style');polish.textContent='.filters{scrollbar-width:none}.filters::-webkit-scrollbar{display:none}.format-guide{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin-top:12px}.format-note{padding:12px;border:1px solid var(--line);border-radius:12px;background:#ffffff04}.format-note strong{display:block;color:var(--accent)}.fbi-builder{margin:0 0 24px;padding:clamp(18px,3vw,30px)}.fbi-builder summary{cursor:pointer;list-style:none}.fbi-builder summary::-webkit-details-marker{display:none}.fbi-list{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:8px;margin:16px 0;max-height:340px;overflow:auto}.fbi-choice{display:flex;align-items:flex-start;gap:9px;margin:0;padding:11px;border:1px solid var(--line);border-radius:11px;background:#ffffff03}.fbi-choice input{width:auto;margin-top:4px}.fbi-actions{display:flex;gap:9px;flex-wrap:wrap}.fbi-output{display:flex;gap:16px;flex-wrap:wrap;margin-top:16px}.fbi-output figure{margin:0}.fbi-output img{width:210px;padding:8px;border-radius:13px;background:#fff}@media(max-width:520px){.catalog-hero h1{font-size:2rem!important}.format-guide{grid-template-columns:1fr}.fbi-actions{display:grid}.fbi-actions .button{width:100%}}';document.head.append(polish);async function poll(){if(!box)return;try{const r=await fetch('/healthz'+suffix),h=await r.json(),jobs=h.background_jobs||{},rows=Object.entries(jobs).filter(([,j])=>j.active||j.error).map(([n,j])=>{const pct=j.total?Math.round(j.done*100/j.total)+'%':'',bar=j.total?'<progress max="'+j.total+'" value="'+j.done+'"></progress>':'';return '<div><strong>'+safe(n.replaceAll('_',' '))+'</strong> '+safe(j.message||'')+' '+pct+(j.error?' · '+safe(j.error):'')+bar+'</div>'});box.innerHTML=rows.join('');box.hidden=!rows.length}catch{}setTimeout(poll,1000)}poll();const params=new URLSearchParams(location.search),toolbar=document.querySelector('.toolbar');if(toolbar&&params.get('platform')==='3ds'){const guide=document.createElement('div');guide.className='format-guide';guide.innerHTML='<div class=format-note><strong>🔒 Encrypted CIA</strong><span class=muted>Use with FBI and a physical or homelab Nintendo 3DS. This is the normal console-install format.</span></div><div class=format-note><strong>🔓 Unencrypted / decrypted CIA</strong><span class=muted>Use with compatible mobile or desktop emulators that cannot load console-encrypted content. FBI batches intentionally exclude these.</span></div>';toolbar.appendChild(guide)}fetch('/api/games'+suffix).then(r=>r.json()).then(data=>{const cias=[];for(const g of data.games||[])for(const a of g.artifacts||[])if((a.extension||'').toLowerCase()==='.cia'&&a.encryption!=='unencrypted')cias.push({id:a.id,title:g.title,name:a.name});if(!cias.length||!toolbar)return;const details=document.createElement('details');details.className='surface fbi-builder';details.innerHTML='<summary><span class=badge>FBI</span> <strong>Batch install selected CIAs</strong><p class=muted>Select two or more encrypted CIAs, then show the ordered QR batch or send the same selection to FBI on port 5000.</p></summary><div class=fbi-list></div><div class=fbi-actions><button class=button type=button data-action=all>Select all</button><button class="button primary" type=button data-action=qr>Build batch QR</button></div><form class=fbi-actions data-action=send><input name=target placeholder="FBI device IP, e.g. 192.168.1.55" aria-label="FBI device IP"><button class=button>Send selected to FBI port 5000</button></form><div class=fbi-output aria-live=polite></div>';const list=details.querySelector('.fbi-list'),out=details.querySelector('.fbi-output');for(const c of cias){const label=document.createElement('label');label.className='fbi-choice';const check=document.createElement('input');check.type='checkbox';check.value=c.id;const text=document.createElement('span');text.textContent=c.title+' · '+c.name;label.append(check,text);list.appendChild(label)}const selected=()=>[...list.querySelectorAll('input:checked')].map(x=>x.value);details.querySelector('[data-action=all]').addEventListener('click',()=>{const boxes=[...list.querySelectorAll('input')],all=boxes.every(x=>x.checked);boxes.forEach(x=>x.checked=!all)});details.querySelector('[data-action=qr]').addEventListener('click',async()=>{const ids=selected();if(!ids.length){out.textContent='Select at least one encrypted CIA.';return}out.textContent='Building QR batch…';const r=await fetch('/api/fbi/batches?ids='+encodeURIComponent(ids.join(','))+amp);if(!r.ok){out.textContent=await r.text();return}const b=await r.json();out.innerHTML='';b.qr_codes.forEach((src,i)=>{const f=document.createElement('figure'),img=document.createElement('img'),cap=document.createElement('figcaption');img.src=src;img.alt='FBI batch QR '+(i+1)+' of '+b.count;cap.textContent='Scan '+(i+1)+' of '+b.count;f.append(img,cap);out.appendChild(f)})});details.querySelector('form[data-action=send]').addEventListener('submit',async e=>{e.preventDefault();const ids=selected();if(!ids.length){out.textContent='Select at least one encrypted CIA.';return}const body=new URLSearchParams({target:e.target.target.value,artifact_ids:ids.join(',')}),r=await fetch('/queue'+suffix,{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body});out.textContent=r.ok?'Selected CIAs queued in FBI.':await r.text()});toolbar.after(details)})})()`

const publicPatchHTML = `<!doctype html><html><head><meta charset=utf-8><meta name=viewport content="width=device-width"><meta name=csrf-token content="{{.CSRF}}"><title>Patch Workspace · {{.Copy.Brand}}</title><style>` + sharedCSS + `.form-grid{display:grid;gap:1rem}.form-grid label{display:grid;gap:.35rem}.required{color:var(--accent)}small{color:var(--muted);line-height:1.4}pre{white-space:pre-wrap}</style><script defer src="/assets/patch.js{{.Token}}"></script></head><body class="{{.Theme}}"><main class=wrap><p><a href="/{{.Token}}">{{.Copy.Back}}</a></p><section class=panel><span class=badge>Temporary workspace</span><h1>Patch your own ROM</h1><p>Upload your legally obtained base ROM, then either choose one of this relay's indexed patch recipes or upload your own patch. Your source upload stays in private job staging and is deleted after the job. The patched result expires after 24 hours.</p><p class=muted>Only an administrator can use catalog ROMs, local server paths, confirm checksum mismatches, or make an output permanent.</p><form id=public-patch-form class=form-grid enctype=multipart/form-data><label><span>Base ROM <b class=required>required</b></span><input type=file name=rom required><small>The original unpatched ROM expected by the patch. Maximum size is controlled by the server configuration.</small></label><label><span>Server patch recipe</span><select id=patch-recipe name=patch_id><option value="">Upload my own patch instead</option></select><small>Recipes verify the uploaded ROM against their recorded or embedded checksum. A mismatch is rejected.</small></label><label><span>Your patch file</span><input type=file name=patch accept=".ips,.ups,.bps,.ppf,.rup,.aps,.bdf,.pmsr,.vcdiff"><small>Required only when no server recipe is selected. Supported formats: IPS, UPS, BPS, PPF, RUP, APS, BSDiff, PMSR, and VCDiff.</small></label><label><span>Output filename</span><input name=output_filename placeholder="My Patched Game.gba"><small>Required for an uploaded patch; optional for a recipe with a predefined name. The base ROM extension is enforced.</small></label><label><span>Display title</span><input name=title placeholder="My Patched Game"><small>Optional catalog title. Temporary patched games never use remote metadata providers.</small></label><button type=submit>Create temporary patched ROM</button></form><pre id=patch-result aria-live=polite></pre></section></main></body></html>`
const publicPatchHTMLV2 = `<!doctype html><html><head><meta charset=utf-8><meta name=viewport content="width=device-width"><meta name=csrf-token content="{{.CSRF}}"><title>Patch Workspace · {{.Copy.Brand}}</title><style>` + sharedCSS + `.patch-hero{padding:clamp(26px,5vw,54px);margin-bottom:20px}.patch-layout{display:grid;grid-template-columns:minmax(0,1fr) minmax(240px,340px);gap:20px;align-items:start}.patch-form{display:grid;gap:16px}.patch-step{padding:18px;border:1px solid var(--line);border-radius:15px;background:#ffffff03}.patch-step h2{font-size:1.1rem}.form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.form-grid label{margin:0}.full{grid-column:1/-1}.required{color:var(--accent)}small{color:var(--muted)}.patch-aside{position:sticky;top:18px}.patch-aside li{margin-bottom:10px;color:var(--muted)}.result-box{min-height:70px;padding:14px;border:1px solid var(--line);border-radius:12px;background:#05070c;white-space:pre-wrap}.cover-prompt{padding:14px;border:1px dashed color-mix(in srgb,var(--accent) 45%,var(--line));border-radius:13px;background:color-mix(in srgb,var(--accent) 5%,transparent)}@media(max-width:850px){.patch-layout{grid-template-columns:1fr}.patch-aside{position:static}}@media(max-width:620px){.form-grid{grid-template-columns:1fr}.full{grid-column:auto}}</style><script defer src="/assets/patch.js{{.Token}}"></script></head><body class="{{.Theme}}"><main class=wrap><header class=site-header><a class=brand href="/{{.Token}}"><span class=brand-mark aria-hidden=true>◈</span><span><span class=eyebrow>Temporary workshop</span><strong>{{.Copy.Brand}}</strong></span></a><a class=button href="/{{.Token}}">Back to library</a></header><section class="patch-hero surface"><span class=badge>Bring your own ROM</span><h1>Patch a game safely.</h1><p class=muted>Upload your legal clean base ROM, choose a reusable recipe or upload your patch, and ROM Relay creates a separate temporary catalog entry. Your original upload is deleted after the job.</p></section><div class=patch-layout><section class=panel><form id=public-patch-form class=patch-form enctype=multipart/form-data><div class=patch-step><span class=eyebrow>Step 1</span><h2>Clean base ROM</h2><label><span>Base ROM <b class=required>required</b></span><input type=file name=rom required><small>Use the exact revision expected by the patch. ROM Relay validates format and embedded checksums.</small></label></div><div class=patch-step><span class=eyebrow>Step 2</span><h2>Patch source</h2><div class=form-grid><label><span>Server recipe</span><select id=patch-recipe name=patch_id><option value="">Upload my own patch</option></select><small>Choose this when the relay already has the patch.</small></label><label><span>Your patch file</span><input type=file name=patch accept=".ips,.ups,.bps,.ppf,.rup,.aps,.bdf,.pmsr,.vcdiff"><small>Required only when no recipe is selected.</small></label></div></div><div class=patch-step><span class=eyebrow>Step 3</span><h2>Name the patched entry</h2><div class=form-grid><label><span>Output filename <b class=required>required*</b></span><input name=output_filename placeholder="Pokemon Radical Red.gba"><small>*A recipe may supply this. The base extension is enforced.</small></label><label><span>Display title</span><input name=title placeholder="Pokemon Radical Red"><small>Optional but recommended.</small></label><label><span>Developer / patch author</span><input name=developer></label><label><span>Release date</span><input name=release_date></label><label><span>Genre</span><input name=genre></label><label><span>Region</span><input name=region placeholder="World"></label><label class=full><span>Description</span><textarea name=description rows=5 placeholder="Describe what this patch changes…"></textarea><small>Optional. Patched entries never query remote metadata, so add this if other users should know what they are downloading.</small></label><label class="full cover-prompt"><span>Cover / icon artwork</span><input type=file name=cover accept="image/png,image/jpeg,image/webp"><small>Optional PNG, JPEG, or WebP up to 8 MiB. It appears on the patched game card; otherwise ROM Relay shows a controller placeholder.</small></label></div></div><button class="button primary" type=submit>Create temporary patched ROM</button></form><pre class=result-box id=patch-result aria-live=polite>No patch job submitted yet.</pre></section><aside class="panel patch-aside"><span class=eyebrow>Before you start</span><h2>What happens?</h2><ol><li>Your base ROM is copied into private staging.</li><li>The patch runs against that copy, never your source.</li><li>The output is validated and gets a green Patched badge.</li><li>The result expires after 24 hours or its latest completed download.</li></ol><p class=muted>Only the private admin workspace can make a result permanent or approve a checksum mismatch.</p><p><strong>Supported:</strong> IPS, UPS, BPS, PPF, RUP, APS, BSDiff, PMSR, and VCDiff.</p></aside></div></main></body></html>`

const catalogHTML = `<!doctype html><html><head><meta charset=utf-8><meta name=viewport content="width=device-width"><meta name=theme-color content="#080b12"><title>{{.Copy.Brand}}</title><style>` + sharedCSS + `.catalog-hero{padding:clamp(28px,5vw,62px);margin-bottom:20px;overflow:hidden;position:relative}.catalog-hero:after{content:"";position:absolute;right:-100px;top:-160px;width:420px;height:420px;border-radius:50%;background:radial-gradient(circle,color-mix(in srgb,var(--accent) 20%,transparent),transparent 68%);pointer-events:none}.catalog-hero h1{max-width:760px;margin-bottom:14px}.catalog-hero p{max-width:620px;font-size:1.02rem}.toolbar{position:sticky;top:10px;z-index:5;margin-bottom:24px;padding:14px;backdrop-filter:blur(22px);background:color-mix(in srgb,var(--surface) 82%,transparent)}.search{position:relative;margin:0}.search .icon{position:absolute;left:14px;top:50%;transform:translateY(-50%);color:var(--muted);pointer-events:none}.search input{height:48px;padding-left:43px;background:#060910a6}.filters{display:flex;gap:8px;margin-top:12px;overflow:auto;padding-bottom:2px}.library-heading{display:flex;align-items:end;justify-content:space-between;gap:20px;margin:30px 0 14px}.library-heading h2{margin:0;font-size:1.25rem}.game-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(215px,1fr));gap:clamp(14px,2vw,24px)}.game-card{min-width:0;overflow:hidden;text-decoration:none;border:1px solid var(--line);border-radius:17px;background:linear-gradient(155deg,var(--surface2),var(--surface));box-shadow:0 14px 44px #0003;transition:transform .2s ease,border-color .2s ease,box-shadow .2s ease}.game-card:hover,.game-card:focus-visible{outline:none;transform:translateY(-5px);border-color:color-mix(in srgb,var(--accent) 58%,var(--line));box-shadow:0 22px 60px #0007}.media{position:relative;aspect-ratio:3/4;overflow:hidden;background:linear-gradient(145deg,var(--surface3),var(--bg))}.cover{width:100%;height:100%;object-fit:cover;transition:transform .35s ease}.game-card:hover .cover{transform:scale(1.025)}.cover-placeholder{width:100%;height:100%;display:grid;place-items:center;color:var(--faint)}.cover-placeholder .icon{width:46px;height:46px}.status-stack{position:absolute;left:10px;right:10px;bottom:10px;display:flex;gap:6px;flex-wrap:wrap}.card-body{padding:15px 16px 17px}.card-kicker{display:flex;align-items:center;justify-content:space-between;gap:10px;color:var(--accent);font-size:.72rem;font-weight:800;letter-spacing:.1em;text-transform:uppercase}.game-card h2{margin:7px 0 6px;font-size:1.02rem;line-height:1.28}.card-meta{white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.card-copy{display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;min-height:2.9em;margin:10px 0 0;color:var(--muted);font-size:.82rem}@media(max-width:520px){.catalog-hero{padding:26px 20px}.catalog-hero h1{font-size:2.25rem}.game-grid{grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.card-body{padding:12px}.card-copy{display:none}.game-card h2{font-size:.9rem}.status-stack{left:7px;right:7px;bottom:7px}.badge{font-size:.61rem;padding:4px 6px}}</style><script defer src="/assets/catalog.js{{.Token}}"></script></head><body class="{{.Theme}}"><main class=wrap><header class=site-header><a class=brand href="/{{.Token}}"><span class=brand-mark><svg class="icon icon-lg" viewBox="0 0 24 24" aria-hidden=true><path d="M7 8h10a4 4 0 0 1 3.8 5.2l-1 3A2.6 2.6 0 0 1 15.5 17l-1.2-1.3H9.7L8.5 17a2.6 2.6 0 0 1-4.3-.8l-1-3A4 4 0 0 1 7 8Z"/><path d="M7 12h4M9 10v4M16.5 11.5h.01M18.5 13.5h.01"/></svg></span><span><span class=eyebrow>Self-hosted library</span><strong>{{.Copy.Brand}}</strong></span></a><nav class=nav-actions><a class=button href="/patches{{.Token}}"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="m14 4 6 6-9.5 9.5a2.1 2.1 0 0 1-3 0l-3-3a2.1 2.1 0 0 1 0-3Z"/><path d="m12 6 6 6M7.5 13.5l3 3"/></svg><span>Patch workspace</span></a></nav></header><section class="catalog-hero surface"><span class=eyebrow>Your collection, anywhere</span><h1>Pick a game.<br>Relay the good stuff.</h1><p class=muted>{{.Copy.Tagline}} Browse local dumps, patched variants, CIA formats, and ready-to-scan downloads without leaving your network.</p></section><div id=background-work class=work hidden aria-live=polite></div><section class="toolbar surface" aria-label="Library controls"><form class=search><svg class=icon viewBox="0 0 24 24" aria-hidden=true><circle cx=11 cy=11 r=7/><path d="m20 20-4-4"/></svg>{{if .ViewerToken}}<input type=hidden name=token value="{{.ViewerToken}}">{{end}}{{if .PlatformFilter}}<input type=hidden name=platform value="{{.PlatformFilter}}">{{end}}{{if .EncryptionFilter}}<input type=hidden name=encryption value="{{.EncryptionFilter}}">{{end}}<input name=q value="{{.Q}}" placeholder="Search titles, editions, and regions…" aria-label="Search games"></form><nav class=filters aria-label="Platforms"><a class="chip {{if not .PlatformFilter}}active{{end}}" href="/{{.Token}}">All systems</a>{{range .Platforms}}<a class="chip {{if eq $.PlatformFilter .ID}}active{{end}}" href="/?platform={{.ID}}{{$.TokenAmp}}">{{.Name}}</a>{{end}}</nav>{{if eq .PlatformFilter "3ds"}}<nav class=filters aria-label="3DS CIA encryption"><a class="chip {{if not .EncryptionFilter}}active{{end}}" href="/?platform=3ds{{.TokenAmp}}">All 3DS</a><a class="chip {{if eq .EncryptionFilter "encrypted"}}active{{end}}" href="/?platform=3ds&encryption=encrypted{{$.TokenAmp}}"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><rect x=5 y=10 width=14 height=10 rx=2/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>Encrypted CIA</a><a class="chip {{if eq .EncryptionFilter "unencrypted"}}active{{end}}" href="/?platform=3ds&encryption=unencrypted{{$.TokenAmp}}"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><rect x=5 y=10 width=14 height=10 rx=2/><path d="M16 10V7a4 4 0 0 0-7.6-1.7"/></svg>Unencrypted CIA</a></nav>{{end}}</section><div class=library-heading><div><span class=eyebrow>Library</span><h2>{{len .Games}} titles ready to relay</h2></div>{{if .Q}}<span class=muted>Results for “{{.Q}}”</span>{{end}}</div><section class=game-grid>{{range .Games}}<a class=game-card href="/games/{{.ID}}{{$.Token}}"><div class=media>{{if .ArtworkURL}}<img class=cover src="{{media .ID}}" alt="Cover art for {{.Title}}" loading=lazy>{{else}}<div class=cover-placeholder aria-hidden=true><svg class=icon viewBox="0 0 24 24"><path d="M7 8h10a4 4 0 0 1 3.8 5.2l-1 3A2.6 2.6 0 0 1 15.5 17l-1.2-1.3H9.7L8.5 17a2.6 2.6 0 0 1-4.3-.8l-1-3A4 4 0 0 1 7 8Z"/><path d="M7 12h4M9 10v4M16.5 11.5h.01M18.5 13.5h.01"/></svg></div>{{end}}<div class=status-stack>{{if .Encryption}}<span class="badge encryption">{{if eq .Encryption "unencrypted"}}<svg class=icon viewBox="0 0 24 24" aria-hidden=true><rect x=5 y=10 width=14 height=10 rx=2/><path d="M16 10V7a4 4 0 0 0-7.6-1.7"/></svg>{{else}}<svg class=icon viewBox="0 0 24 24" aria-hidden=true><rect x=5 y=10 width=14 height=10 rx=2/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>{{end}}{{.Encryption}}</span>{{end}}{{if .ROMPatch}}<span class="badge patched"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="m14 4 6 6-9.5 9.5a2.1 2.1 0 0 1-3 0l-3-3a2.1 2.1 0 0 1 0-3Z"/><path d="m12 6 6 6"/></svg>Patched</span>{{end}}</div></div><div class=card-body><div class=card-kicker><span>{{.Platform}}</span><span class=dim>{{len .Artifacts}} file{{if ne (len .Artifacts) 1}}s{{end}}</span></div><h2>{{.Title}}</h2><div class="card-meta muted">{{if .Developer}}{{.Developer}}{{else if .Publisher}}{{.Publisher}}{{else}}Local library{{end}}{{if .ReleaseDate}} · {{.ReleaseDate}}{{end}}</div><p class=card-copy>{{if .Description}}{{.Description}}{{else}}Local metadata ready. Open for formats, hashes, QR, and download options.{{end}}</p></div></a>{{else}}<div class="surface empty-state"><svg class="icon icon-lg" viewBox="0 0 24 24" aria-hidden=true><circle cx=11 cy=11 r=7/><path d="m20 20-4-4"/></svg><h2>Nothing on this shelf yet</h2><p class=muted>{{.Copy.Empty}}</p>{{if .RemoteURL}}<a class="button primary" href="{{.RemoteURL}}">Search Remotely</a>{{end}}</div>{{end}}</section></main></body></html>`
const gameHTML = `<!doctype html><html><head><meta charset=utf-8><meta name=viewport content="width=device-width"><meta name=theme-color content="#080b12"><title>{{.Game.Title}} · {{.Copy.Brand}}</title><style>` + sharedCSS + `.detail-grid{display:grid;grid-template-columns:minmax(220px,330px) minmax(0,1fr);gap:clamp(24px,5vw,70px);align-items:start}.cover-shell{position:sticky;top:20px}.hero{display:block;width:100%;aspect-ratio:3/4;object-fit:cover;border:1px solid var(--line);border-radius:22px;box-shadow:var(--shadow)}.hero-placeholder{display:grid;place-items:center;width:100%;aspect-ratio:3/4;border:1px solid var(--line);border-radius:22px;background:linear-gradient(145deg,var(--surface3),var(--surface));color:var(--faint)}.hero-placeholder .icon{width:62px;height:62px}.cover-actions{display:grid;gap:9px;margin-top:12px}.detail-main{min-width:0;padding-top:12px}.title-badges{display:flex;gap:7px;flex-wrap:wrap;margin-bottom:18px}.detail-main h1{max-width:960px;margin-bottom:20px}.synopsis{max-width:850px;color:#d7e0ec;font-size:1.02rem;line-height:1.75}.synopsis-note{display:flex;align-items:flex-start;gap:10px;max-width:780px;padding:14px 16px;border:1px solid var(--line);border-radius:13px;background:#ffffff04;color:var(--muted)}.facts{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px;margin:26px 0 38px}.fact{min-width:0;padding:14px;border:1px solid var(--line);border-radius:13px;background:#ffffff04}.fact .eyebrow{display:block;margin-bottom:4px}.fact strong{display:block;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.section-heading{display:flex;align-items:end;justify-content:space-between;gap:16px;margin:0 0 13px}.section-heading h2{margin:0}.artifact-list{display:grid;gap:12px}.artifact-card{padding:18px;border:1px solid var(--line);border-radius:16px;background:linear-gradient(145deg,var(--surface2),var(--surface));box-shadow:0 12px 35px #0002}.artifact-top{display:flex;justify-content:space-between;gap:20px;align-items:flex-start}.artifact-name{min-width:0}.artifact-name h3{margin:0 0 6px;font-size:1rem;overflow-wrap:anywhere}.artifact-tags{display:flex;gap:6px;flex-wrap:wrap}.artifact-actions{display:flex;gap:8px;flex-wrap:wrap}.tech{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:12px;align-items:end;margin-top:16px}.hash-label{margin:0 0 5px}.qr-details summary{cursor:pointer;color:var(--accent);font-weight:700;list-style:none}.qr-details summary::-webkit-details-marker{display:none}.qr-details[open]{grid-column:1/-1;padding-top:12px}.compat{margin:12px 0 0;color:var(--muted);font-size:.83rem}@media(max-width:820px){.detail-grid{grid-template-columns:1fr}.cover-shell{position:static;display:grid;grid-template-columns:minmax(130px,220px) 1fr;gap:14px;align-items:end}.cover-actions{margin:0}.detail-main{padding-top:0}.artifact-top,.tech{grid-template-columns:1fr;display:grid}}@media(max-width:520px){.cover-shell{grid-template-columns:120px 1fr}.detail-main h1{font-size:2.2rem}.facts{grid-template-columns:repeat(2,minmax(0,1fr));margin-bottom:28px}.artifact-card{padding:14px}.artifact-actions{display:grid}.artifact-actions .button{width:100%}}</style><script defer src="/assets/catalog.js{{.Token}}"></script></head><body class="{{.Theme}}"><main class=wrap><header class=site-header><a class=brand href="/{{.Token}}"><span class=brand-mark><svg class="icon icon-lg" viewBox="0 0 24 24" aria-hidden=true><path d="M7 8h10a4 4 0 0 1 3.8 5.2l-1 3A2.6 2.6 0 0 1 15.5 17l-1.2-1.3H9.7L8.5 17a2.6 2.6 0 0 1-4.3-.8l-1-3A4 4 0 0 1 7 8Z"/><path d="M7 12h4M9 10v4M16.5 11.5h.01M18.5 13.5h.01"/></svg></span><span><span class=eyebrow>Self-hosted library</span><strong>{{.Copy.Brand}}</strong></span></a><nav class=nav-actions><a class=button href="/patches{{.Token}}"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="m14 4 6 6-9.5 9.5a2.1 2.1 0 0 1-3 0l-3-3a2.1 2.1 0 0 1 0-3Z"/><path d="m12 6 6 6"/></svg><span>Patch workspace</span></a></nav></header><a class=back-link href="/{{.Token}}"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="m15 18-6-6 6-6"/></svg>{{.Copy.Back}}</a><div id=background-work class=work hidden aria-live=polite></div><section class=detail-grid><aside class=cover-shell>{{if .Game.ArtworkURL}}<img class=hero src="{{media .Game.ID}}" alt="Cover art for {{.Game.Title}}">{{else}}<div class=hero-placeholder aria-hidden=true><svg class=icon viewBox="0 0 24 24"><path d="M7 8h10a4 4 0 0 1 3.8 5.2l-1 3A2.6 2.6 0 0 1 15.5 17l-1.2-1.3H9.7L8.5 17a2.6 2.6 0 0 1-4.3-.8l-1-3A4 4 0 0 1 7 8Z"/><path d="M7 12h4M9 10v4M16.5 11.5h.01M18.5 13.5h.01"/></svg></div>{{end}}<div class=cover-actions>{{with index .Game.Artifacts 0}}<a class="button primary" href="{{download .}}"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="M12 3v12m0 0 4-4m-4 4-4-4M5 20h14"/></svg>Download primary</a>{{end}}<span class="button dim"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="M7 8h10v12H7zM9 4h6l2 4H7z"/></svg>{{len .Game.Artifacts}} variant{{if ne (len .Game.Artifacts) 1}}s{{end}}</span></div></aside><article class=detail-main><div class=title-badges><span class=badge><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="M7 8h10a4 4 0 0 1 3.8 5.2l-1 3A2.6 2.6 0 0 1 15.5 17l-1.2-1.3H9.7L8.5 17a2.6 2.6 0 0 1-4.3-.8l-1-3A4 4 0 0 1 7 8Z"/></svg>{{.Game.Platform}}</span>{{if .Game.Encryption}}<span class="badge encryption">{{if eq .Game.Encryption "unencrypted"}}<svg class=icon viewBox="0 0 24 24" aria-hidden=true><rect x=5 y=10 width=14 height=10 rx=2/><path d="M16 10V7a4 4 0 0 0-7.6-1.7"/></svg>{{else}}<svg class=icon viewBox="0 0 24 24" aria-hidden=true><rect x=5 y=10 width=14 height=10 rx=2/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>{{end}}{{.Game.Encryption}} CIA</span>{{end}}{{if .Game.ROMPatch}}<span class="badge patched"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="m14 4 6 6-9.5 9.5a2.1 2.1 0 0 1-3 0l-3-3a2.1 2.1 0 0 1 0-3Z"/></svg>Patched · local metadata</span>{{end}}</div><h1>{{.Game.Title}}</h1>{{if .Game.Description}}<p class=synopsis>{{.Game.Description}}</p>{{else}}<p class=synopsis-note><svg class=icon viewBox="0 0 24 24" aria-hidden=true><circle cx=12 cy=12 r=9/><path d="M12 11v5m0-8h.01"/></svg><span>{{.SynopsisNote}}</span></p>{{end}}<div class=facts>{{if .Game.Developer}}<div class=fact><span class=eyebrow>Developer</span><strong title="{{.Game.Developer}}">{{.Game.Developer}}</strong></div>{{end}}{{if .Game.Publisher}}<div class=fact><span class=eyebrow>Publisher</span><strong title="{{.Game.Publisher}}">{{.Game.Publisher}}</strong></div>{{end}}{{if .Game.ReleaseDate}}<div class=fact><span class=eyebrow>Released</span><strong>{{.Game.ReleaseDate}}</strong></div>{{end}}{{if .Game.Genre}}<div class=fact><span class=eyebrow>Genre</span><strong>{{.Game.Genre}}</strong></div>{{end}}{{if .Game.Region}}<div class=fact><span class=eyebrow>Region</span><strong>{{.Game.Region}}</strong></div>{{end}}{{if .Game.Players}}<div class=fact><span class=eyebrow>Players</span><strong>{{.Game.Players}}</strong></div>{{end}}{{if .Game.Rating}}<div class=fact><span class=eyebrow>IGDB rating</span><strong>{{.Game.Rating}}</strong></div>{{end}}{{if .Game.MetadataSource}}<div class=fact><span class=eyebrow>Metadata</span><strong>{{.Game.MetadataSource}}</strong></div>{{end}}</div><div class=section-heading><div><span class=eyebrow>Available files</span><h2>Choose your variant</h2></div></div><div class=artifact-list>{{range .Game.Artifacts}}<section class=artifact-card><div class=artifact-top><div class=artifact-name><h3>{{.Name}}</h3><div class=artifact-tags><span class=badge>{{.Extension}}</span>{{if .Encryption}}<span class="badge encryption">{{if eq .Encryption "unencrypted"}}<svg class=icon viewBox="0 0 24 24" aria-hidden=true><rect x=5 y=10 width=14 height=10 rx=2/><path d="M16 10V7a4 4 0 0 0-7.6-1.7"/></svg>{{else}}<svg class=icon viewBox="0 0 24 24" aria-hidden=true><rect x=5 y=10 width=14 height=10 rx=2/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>{{end}}{{.Encryption}}</span>{{end}}<span class=badge>{{size .Size}}</span></div></div><div class=artifact-actions><a class="button primary" href="{{download .}}"><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="M12 3v12m0 0 4-4m-4 4-4-4M5 20h14"/></svg>Download</a></div></div><div class=tech><div><p class="hash-label eyebrow">Direct URL</p><code>{{direct .ID}}</code></div><details class=qr-details><summary><svg class=icon viewBox="0 0 24 24" aria-hidden=true><path d="M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM15 14h2v2h-2zM18 17h2v3h-5v-2"/></svg>Show QR</summary><p><img class=qr src="{{qr .ID}}" alt="Direct-download QR code"></p></details></div><p class=compat>Delta and Manic: download, then use iOS Share/Open In. FBI supports CIA direct-download QR codes.</p></section>{{end}}</div></article></section></main></body></html>`
const downloadChoiceHTML = `<!doctype html><html><head><meta charset=utf-8><meta name=viewport content="width=device-width"><title>Choose CIA format · {{.Copy.Brand}}</title><style>` + sharedCSS + `</style></head><body class="{{.Theme}}"><main class=wrap><p><a href="/games/{{.Artifact.GameID}}{{.Token}}">← Game details</a></p><section class=panel><span class="badge encryption">encrypted CIA</span><h1>Choose the compatible download</h1><p><strong>Encrypted</strong> is the right choice for FBI and a real or homelab Nintendo 3DS. <strong>Unencrypted</strong> is commonly required by mobile-based 3DS emulators.</p><p><a class=chip href="{{direct .Artifact.ID}}">Download original encrypted CIA</a></p>{{if .Mobile}}<p><a class=chip href="{{direct .Mobile.ID}}">Download prepared unencrypted CIA</a></p>{{else if and .Admin .DecryptReady}}<form method=post action="/api/admin/decrypt/{{.Artifact.ID}}{{.Token}}"><input type=hidden name=csrf_token value="{{.CSRF}}"><button type=submit>Prepare unencrypted CIA</button></form><p class=muted>This runs the private Redux-compatible driver against a copy. The encrypted source is not changed.</p>{{else if not .DecryptReady}}<p class=muted>Unencrypted preparation is unavailable: {{.DecryptReason}}. See internal/drivers/cia-3ds-decryptor/README.md.</p>{{else}}<p class=muted>An administrator must open the private admin session before preparing a new unencrypted copy.</p>{{end}}</section></main></body></html>`
