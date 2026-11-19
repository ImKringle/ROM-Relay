package web

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/web/spa"
)

// The React UI's server side: the SPA shell, the session API it signs in
// through, and the request-forgery guard every mutation passes.

// spaPage serves the React shell for a page route. "GET /" is also the mux's
// catch-all, so unknown API paths answer 404 rather than HTML, and an
// ?admin_token= on an admin page is still exchanged for the admin cookie.
func (s *CatalogServer) spaPage(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	// Admin pages keep their own gate (security() lets /admin/ through to the
	// handler): only an admin gets even the shell. adminOK must run before the
	// landing, which sets the real admin cookie for any ?admin_token= present.
	if r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
		if !s.adminOK(r) {
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				http.Redirect(w, r, "/login?next="+urlPathEscape(r.URL.Path), http.StatusSeeOther)
				return
			}
			http.Error(w, "admin authentication required", http.StatusUnauthorized)
			return
		}
		if s.adminTokenLanding(w, r, r.URL.Path) {
			return
		}
	}
	s.spaShell(w, r)
}

// spaShell serves index.html for a human page route. The auth gate in
// security() has already run.
func (s *CatalogServer) spaShell(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(spa.FS(), "index.html")
	if err != nil {
		http.Error(w, "UI not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// spaAssets serves the hashed bundles. Their names change with their content,
// so browsers may keep them for a year.
func spaAssets() http.Handler {
	files := http.StripPrefix("/app-assets/", http.FileServer(http.FS(spa.FS())))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || strings.HasSuffix(r.URL.Path, "index.html") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}

// sessionInfo is everything the UI needs to draw itself for this visitor.
type sessionInfo struct {
	Authenticated      bool            `json:"authenticated"`
	Username           string          `json:"username,omitempty"`
	Role               string          `json:"role,omitempty"`
	Admin              bool            `json:"admin"`
	MustChangePassword bool            `json:"must_change_password"`
	CSRF               string          `json:"csrf,omitempty"`
	Open               bool            `json:"open"` // no viewer token: browsing without signing in is allowed
	Ready              bool            `json:"ready"`
	Tools              []Tool          `json:"tools"`
	Features           map[string]bool `json:"features"`
}

func (s *CatalogServer) sessionInfo(r *http.Request) sessionInfo {
	u := currentUser(r)
	info := sessionInfo{Admin: s.adminOK(r), Open: s.token == "", Ready: s.ready.Load(), Tools: ToolsFor(u)}
	if u != nil {
		info.Authenticated, info.Username, info.Role, info.MustChangePassword = true, u.Username, u.Role, u.MustChangePassword
	}
	// The CSRF token only goes to someone who can act: a signed-in user, an
	// admin-token holder, or anyone on an open relay.
	if info.Authenticated || info.Admin || info.Open {
		info.CSRF = s.csrf
	}
	info.Features = map[string]bool{
		"romm":       s.romm != nil,
		"torrents":   s.torrents != nil,
		"patches":    s.patches != nil,
		"randomizer": s.randomizer != nil,
		"pokemon":    s.pokemon != nil,
		"webdav":     s.webdavEnabled,
		"remote":     s.remote != nil,
		"convert":    s.convertUpload != nil,
		"crypt":      s.cryptAvailable(),
		"igdb":       s.igdbEnabled,
	}
	return info
}

func (s *CatalogServer) getSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sessionInfo(r))
}

// loginThrottle slows password guessing: after 5 failures for one username
// (or one client address) further attempts wait out a growing lockout. bcrypt
// already costs ~100 ms per try; this caps a patient attacker too.
// ponytail: in-memory, per process; a restart forgives everyone. Fine for one relay.
type loginThrottle struct {
	mu    sync.Mutex
	fails map[string]*throttleEntry
}

type throttleEntry struct {
	count int
	until time.Time
}

func (t *loginThrottle) blocked(keys ...string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var wait time.Duration
	for _, k := range keys {
		if e := t.fails[k]; e != nil {
			wait = max(wait, time.Until(e.until))
		}
	}
	return wait
}

func (t *loginThrottle) fail(keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fails == nil {
		t.fails = map[string]*throttleEntry{}
	}
	for _, k := range keys {
		e := t.fails[k]
		if e == nil {
			e = &throttleEntry{}
			t.fails[k] = e
		}
		e.count++
		if e.count >= 5 {
			// 30 s, doubling per further failure, capped at 15 minutes.
			e.until = time.Now().Add(min(30*time.Second<<min(e.count-5, 5), 15*time.Minute))
		}
	}
}

func (t *loginThrottle) clear(keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range keys {
		delete(t.fails, k)
	}
}

// clientAddr is the direct peer. Proxy headers are deliberately ignored: they
// are client-controlled unless a known proxy sets them, and a forged one only
// dodges the per-address half of the throttle (the per-username half stays).
func clientAddr(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return "ip:" + host
}

func withUser(ctx context.Context, u *catalog.User) context.Context {
	return context.WithValue(ctx, userContextKey{}, u)
}

// createSession signs in with JSON {username, password}: the form login's
// checks and deliberately vague failure, plus the throttle.
func (s *CatalogServer) createSession(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(body.Username)
	keys := []string{"user:" + strings.ToLower(username), clientAddr(r)}
	if wait := s.logins.blocked(keys...); wait > 0 {
		secs := int(wait.Round(time.Second) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		http.Error(w, "Too many failed sign-ins. Try again in "+strconv.Itoa(secs)+" seconds.", http.StatusTooManyRequests)
		return
	}
	fail := func() {
		s.logins.fail(keys...)
		http.Error(w, "Incorrect username or password.", http.StatusUnauthorized)
	}
	if !validCredentials(username, body.Password) {
		fail()
		return
	}
	user, err := s.store.UserByUsername(r.Context(), username)
	// A disabled account gets the same message as a wrong password, so a
	// stranger cannot confirm the account exists.
	if err != nil || user.Disabled || !verifyPassword(user.PasswordHash, body.Password) {
		fail()
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, time.Now().Add(sessionTTL))
	if err != nil {
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}
	s.logins.clear(keys...)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: strings.HasPrefix(s.base, "https://"), SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(sessionTTL),
	})
	writeJSON(w, http.StatusOK, s.sessionInfo(r.WithContext(withUser(r.Context(), &user))))
}

func (s *CatalogServer) deleteSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// forgeryOK guards every state-changing request a browser could be tricked
// into sending. Bearer API keys and machine routes (devices holding the
// viewer token) carry no ambient browser credential and are exempt.
//
// Two independent checks: the request must come from this site (Origin, and
// Sec-Fetch-Site where browsers send it), and it must carry the CSRF token,
// which only this site's pages can read. Signing in has no session yet, so it
// gets the origin check alone.
func (s *CatalogServer) forgeryOK(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if usedAPIKey(r) || isMachinePath(r.URL.Path) {
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	if r.URL.Path == "/api/session" && r.Method == http.MethodPost {
		return true
	}
	// No ambient credential (session or admin cookie) means nothing for a
	// forged request to ride; it gets the normal 401 from its handler instead.
	if !hasCookie(r, sessionCookie) && !hasCookie(r, "romrelay_admin") {
		return true
	}
	provided := r.Header.Get("X-CSRF-Token")
	return provided != "" && provided == s.csrf
}

func hasCookie(r *http.Request, name string) bool {
	c, err := r.Cookie(name)
	return err == nil && c.Value != ""
}
