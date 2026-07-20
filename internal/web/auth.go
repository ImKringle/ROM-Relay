package web

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"romrelay/internal/catalog"
)

const sessionCookie = "romrelay_session"
const sessionTTL = 30 * 24 * time.Hour

type userContextKey struct{}
type apiKeyContextKey struct{}

// HashPassword returns a bcrypt hash of a plaintext password.
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(h), err
}

// verifyPassword reports whether plain matches a stored bcrypt hash in constant
// time (bcrypt.CompareHashAndPassword is constant-time for a given hash).
func verifyPassword(hash, plain string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// validCredentials sanitizes and bounds login input. Usernames are restricted to
// a small safe character set and passwords to a length range. Storage always uses
// bound SQL parameters and bcrypt, so this is defense-in-depth, not the primary
// injection guard.
func validCredentials(username, password string) bool {
	// bcrypt refuses passwords over 72 bytes (bcrypt.ErrPasswordTooLong), so a
	// longer one must be rejected here with a clear message rather than surfacing
	// as a 500 from HashPassword when the user tries to set it.
	if len(username) < 1 || len(username) > 64 || len(password) < 1 || len(password) > 72 {
		return false
	}
	for _, r := range username {
		if !(r == '_' || r == '-' || r == '.' || r == '@' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	for _, r := range password {
		if r < 0x20 || r == 0x7f { // reject control characters
			return false
		}
	}
	return true
}

// currentUser returns the authenticated user for a request, or nil.
func currentUser(r *http.Request) *catalog.User {
	if u, ok := r.Context().Value(userContextKey{}).(*catalog.User); ok {
		return u
	}
	return nil
}

// usedAPIKey reports whether the request was authenticated with an API key
// rather than a cookie session.
func usedAPIKey(r *http.Request) bool {
	ok, _ := r.Context().Value(apiKeyContextKey{}).(bool)
	return ok
}

// resolveUser reads the session cookie and returns the user it identifies, if
// still valid.
func (s *CatalogServer) resolveUser(r *http.Request) *catalog.User {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	u, err := s.store.SessionUser(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	return &u
}

// resolveAPIKeyUser reads an `Authorization: Bearer <token>` header and
// returns the admin account the token was issued to, if the key is still
// valid. This is the API-integration path: a script authenticates with a
// named, revocable key instead of a browser session, and gets exactly the
// same authority the issuing admin has, since the resolved user flows
// through every check currentUser and adminOK already perform.
func (s *CatalogServer) resolveAPIKeyUser(r *http.Request) *catalog.User {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || token == "" {
		return nil
	}
	u, err := s.store.APIKeyUser(r.Context(), token)
	if err != nil {
		return nil
	}
	return &u
}

// isMachinePath reports the handheld/download routes that authenticate with the
// viewer token in the URL. These are fetched by a 3DS or by FBI after scanning a
// QR code, neither of which can hold a login session.
//
// This list is deliberately the whole reach of the viewer token: it opens
// downloads, never pages. The token is printed into QR codes and link hrefs and
// survives in browser history, so anything it unlocks is effectively public to
// whoever has seen one of those URLs.
func isMachinePath(p string) bool {
	return strings.HasPrefix(p, "/d/") || strings.HasPrefix(p, "/files/") || p == "/queue" || p == "/healthz"
}

// isPublicPath reports routes reachable without any authentication.
//
// The built stylesheet and script are public because the sign-in page is: they
// are the same bundle for everyone, hold no user data, and gating them only meant
// the one page an anonymous visitor is *supposed* to reach rendered with no CSS
// and no JavaScript. That is what made the sign-in and change-password screens
// look unstyled on any relay with a viewer token configured.
func isPublicPath(p string) bool {
	return p == "/login" || p == "/logout" ||
		strings.HasPrefix(p, "/sprites/") || strings.HasPrefix(p, "/assets/")
}

// security applies headers and the access model: machine routes use the viewer
// token; human routes require a login session (with the viewer token accepted as
// a fallback); admin routes are gated later by adminOK. Unauthenticated browser
// navigation is redirected to the login page.
func (s *CatalogServer) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-transform")
		// Every page is on the shared shell now and loads the built app.css/app.js as
		// same-origin files, so 'self' alone is enough: no inline <style> or <script>
		// is left to exempt. Reflected data is auto-escaped by html/template and by
		// the scripts (textContent), and every source stays same-origin.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; img-src 'self' data:; style-src 'self'")

		if user := s.resolveUser(r); user != nil {
			r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, user))
		} else if user := s.resolveAPIKeyUser(r); user != nil {
			// A bearer key is never attached automatically by a browser, so
			// there is nothing here for a forged cross-site request to ride:
			// mark it so adminCSRFOK can skip the CSRF token it would
			// otherwise require of a cookie-backed session.
			ctx := context.WithValue(r.Context(), userContextKey{}, user)
			ctx = context.WithValue(ctx, apiKeyContextKey{}, true)
			r = r.WithContext(ctx)
		}
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if isMachinePath(r.URL.Path) {
			// A signed-in person is at least as entitled to a download as anyone
			// holding the token. Without this, the token is the *only* way in and a
			// signed-in browser cannot download its own library: its links carry no
			// ?token=, so every /d/ click and every /healthz poll came back 401.
			tokenOK := s.token == "" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.token)) == 1
			if currentUser(r) == nil && !tokenOK {
				w.Header().Set("WWW-Authenticate", `Bearer realm="ROM Relay"`)
				http.Error(w, copyFor(s.theme).AuthError, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// A user carrying an admin-issued temporary password is held at the change
		// page. The page itself and signing out stay reachable, or the user would
		// be trapped in a redirect loop with no way forward or out. Machine and
		// public routes were already handled above: a 3DS cannot fill in a form.
		if needsPasswordChange(r) && r.URL.Path != passwordChangePath && r.URL.Path != "/logout" {
			if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
				http.Redirect(w, r, passwordChangePath, http.StatusSeeOther)
				return
			}
			http.Error(w, "Set your own password at "+passwordChangePath+" before using ROM Relay.", http.StatusForbidden)
			return
		}
		adminRoute := strings.HasPrefix(r.URL.Path, "/admin/") || strings.HasPrefix(r.URL.Path, "/api/admin/")
		// Human routes: a login session is the only way in. The viewer token used to
		// grant these too, which meant a copied `/?token=…` URL walked straight past
		// the sign-in page, the token is a download credential, not an identity, and
		// a page it opened had no user behind it anyway.
		//
		// When no viewer token is configured at all the LAN is still treated as
		// trusted and browsing stays open, which is the long-standing model for a
		// relay with no credentials set.
		// Admin routes fall through to adminOK in the handler.
		switch {
		case adminRoute, currentUser(r) != nil, s.token == "":
			next.ServeHTTP(w, r)
		case r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html"):
			http.Redirect(w, r, "/login?next="+urlPathEscape(r.URL.Path), http.StatusSeeOther)
		default:
			w.Header().Set("WWW-Authenticate", `Bearer realm="ROM Relay"`)
			http.Error(w, copyFor(s.theme).AuthError, http.StatusUnauthorized)
		}
	})
}

func urlPathEscape(p string) string {
	// Only allow returning to an in-app absolute path to avoid open redirects.
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	return p
}

// loginPage renders the login form. A present, valid session is redirected home.
func (s *CatalogServer) loginPage(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, urlPathEscape(r.URL.Query().Get("next")), "", http.StatusOK)
}

// renderLogin draws the sign-in form, optionally reporting a failed attempt.
func (s *CatalogServer) renderLogin(w http.ResponseWriter, r *http.Request, next, message string, status int) {
	data := loginPageData{pageData: s.newPageData(r), Next: next, Error: message}
	data.Bare = true
	s.renderStatus(w, status, "login", "login", data)
}

// loginPageData is the sign-in view.
type loginPageData struct {
	pageData
	Next  string
	Error string
}

// login validates credentials and, on success, issues a session cookie.
func (s *CatalogServer) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	next := urlPathEscape(r.FormValue("next"))
	fail := func() {
		s.renderLogin(w, r, next, "Incorrect username or password.", http.StatusUnauthorized)
	}
	if !validCredentials(username, password) {
		fail()
		return
	}
	user, err := s.store.UserByUsername(r.Context(), username)
	// A disabled account is refused with the same message as a wrong password: a
	// distinct one would confirm to a stranger that the account exists.
	if err != nil || user.Disabled || !verifyPassword(user.PasswordHash, password) {
		fail()
		return
	}
	token, err := s.store.CreateSession(r.Context(), user.ID, time.Now().Add(sessionTTL))
	if err != nil {
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: strings.HasPrefix(s.base, "https://"), SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(sessionTTL),
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// logout revokes the session and clears the cookie.
func (s *CatalogServer) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// apiMe reports the current user (for the waffle launcher to filter admin items).
func (s *CatalogServer) apiMe(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u == nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false, "admin": s.adminOK(r)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": u.Username, "role": u.Role, "admin": u.Role == "admin" || s.adminOK(r)})
}
