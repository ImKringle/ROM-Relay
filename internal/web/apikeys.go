package web

import (
	"net/http"
	"strings"
)

// apiKeysPage serves the API key management screen. Like the Users page, the
// list is filled from the JSON API rather than server-rendered: creating a
// key shows its token exactly once, and re-rendering the whole page would
// lose it.
func (s *CatalogServer) apiKeysPage(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	if s.adminTokenLanding(w, r, "/admin/api-keys") {
		return
	}
	s.render(w, "apikeys", "apikeys", s.newPageData(r))
}

// adminAPIKey is one key as the management API reports it. The token itself
// is never included once created.
type adminAPIKey struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	CreatedBy         string `json:"created_by"`
	CreatedByUsername string `json:"created_by_username"`
	CreatedAt         string `json:"created_at"`
	LastUsedAt        string `json:"last_used_at,omitempty"`
	Revoked           bool   `json:"revoked"`
}

// listAPIKeys returns every key, active and revoked, so an admin can see
// what a script is still using.
func (s *CatalogServer) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	keys, err := s.store.APIKeys(r.Context())
	if err != nil {
		http.Error(w, "could not list API keys: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminAPIKey, 0, len(keys))
	for _, k := range keys {
		entry := adminAPIKey{
			ID: k.ID, Name: k.Name, CreatedBy: k.CreatedBy, CreatedByUsername: k.CreatedByUsername,
			CreatedAt: k.CreatedAt.UTC().Format("2006-01-02"), Revoked: k.RevokedAt != nil,
		}
		if k.LastUsedAt != nil {
			entry.LastUsedAt = k.LastUsedAt.UTC().Format("2006-01-02 15:04")
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "keys": out})
}

// createAPIKey mints a key attributed to the signed-in admin and returns the
// raw token once. Callers use it as `Authorization: Bearer <token>` against
// any internal API route: it carries the same authority the issuing admin
// has, everywhere that authority is checked (currentUser and adminOK alike),
// since it resolves to a real user session-equivalent identity in security().
func (s *CatalogServer) createAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 128 {
		http.Error(w, "name must be 1-128 characters", http.StatusBadRequest)
		return
	}
	actor := currentUser(r)
	if actor == nil {
		http.Error(w, "API keys must be created from a signed-in admin session, not the admin token alone", http.StatusBadRequest)
		return
	}
	rec, token, err := s.store.CreateAPIKey(r.Context(), name, actor.ID)
	if err != nil {
		http.Error(w, "could not create the key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rec.ID, "name": rec.Name, "token": token,
		"detail": "Shown only now. Store it somewhere safe, ROM Relay does not keep a copy you can retrieve later.",
	})
}

// revokeAPIKey disables a key immediately. It is not deleted, so the Admin
// suite keeps an audit trail of who issued it and when it was last used.
func (s *CatalogServer) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	if err := s.store.RevokeAPIKey(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "could not revoke the key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "revoked": true})
}
