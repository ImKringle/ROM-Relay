package web

import (
	"net/http"
	"strings"
)

// webdavKeysPage serves the self-service WebDAV key management screen. Like
// the admin API-keys page, the list is filled from the JSON API rather than
// server-rendered: creating a key shows its token exactly once, and
// re-rendering the whole page would lose it.
func (s *CatalogServer) webdavKeysPage(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login?next=/account/webdav", http.StatusSeeOther)
		return
	}
	s.render(w, "webdavkeys", "webdavkeys", s.newPageData(r))
}

// webdavKey is one key as the management API reports it. The token itself
// is never included once created.
type webdavKey struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	Revoked    bool   `json:"revoked"`
}

// listWebDAVKeys returns the signed-in user's own keys, active and revoked,
// so they can see what a device is still using.
func (s *CatalogServer) listWebDAVKeys(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	keys, err := s.store.WebDAVKeysForUser(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "could not list WebDAV keys: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]webdavKey, 0, len(keys))
	for _, k := range keys {
		entry := webdavKey{ID: k.ID, Label: k.Label, CreatedAt: k.CreatedAt.UTC().Format("2006-01-02"), Revoked: k.RevokedAt != nil}
		if k.LastUsedAt != nil {
			entry.LastUsedAt = k.LastUsedAt.UTC().Format("2006-01-02 15:04")
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "keys": out, "url": s.base + "/webdav/"})
}

// createWebDAVKey mints a key for the signed-in user and returns the raw
// token once. It carries only read-only access to the shared catalog under
// /webdav/, authenticated with HTTP Basic Auth (any username, the token as
// the password) - never the account password, and never any admin or
// general API authority.
func (s *CatalogServer) createWebDAVKey(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	// r.FormValue parses the body itself, urlencoded or multipart alike (the
	// browser sends this one as multipart/form-data via FormData); calling
	// r.ParseForm first would short-circuit that, since ParseForm never reads
	// a multipart body and FormValue then sees the form as already parsed.
	label := strings.TrimSpace(r.FormValue("label"))
	if len(label) > 128 {
		http.Error(w, "label must be at most 128 characters", http.StatusBadRequest)
		return
	}
	rec, token, err := s.store.CreateWebDAVKey(r.Context(), user.ID, label)
	if err != nil {
		http.Error(w, "could not create the key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rec.ID, "label": rec.Label, "token": token, "url": s.base + "/webdav/",
		"detail": "Shown only now. Store it somewhere safe, ROM Relay does not keep a copy you can retrieve later.",
	})
}

// revokeWebDAVKey disables a key immediately, scoped to the signed-in user so
// nobody can revoke another user's key even by guessing its ID. It is not
// deleted, so the account keeps an audit trail of what it issued and when it
// was last used.
func (s *CatalogServer) revokeWebDAVKey(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	if err := s.store.RevokeWebDAVKey(r.Context(), user.ID, r.PathValue("id")); err != nil {
		http.Error(w, "could not revoke the key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "revoked": true})
}
