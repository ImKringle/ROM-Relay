package web

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"romrelay/internal/catalog"
)

// temporaryPassword generates the one-time password handed to an admin when they
// create or reset an account. It is returned to the admin exactly once and only
// ever stored as a bcrypt hash.
func temporaryPassword() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// usersPage serves the account management screen. The list is filled from
// /api/admin/users by the bundle rather than server-rendered: every action on the
// page changes it, and re-rendering the whole page after each one would lose the
// temporary password shown alongside it.
func (s *CatalogServer) usersPage(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	if s.adminTokenLanding(w, r, "/admin/users") {
		return
	}
	s.render(w, "users", "users", s.newPageData(r))
}

// adminUser is one account as the management API reports it. The password hash is
// never included.
type adminUser struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	Role               string `json:"role"`
	Disabled           bool   `json:"disabled"`
	MustChangePassword bool   `json:"must_change_password"`
	CreatedAt          string `json:"created_at"`
	Saves              int    `json:"saves"`
	Pokemon            int    `json:"pokemon"`
}

// listUsers returns every account with a summary of what it owns, so an admin can
// see what deleting one would destroy.
func (s *CatalogServer) listUsers(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	users, err := s.store.Users(ctx)
	if err != nil {
		http.Error(w, "could not list users: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminUser, 0, len(users))
	for _, u := range users {
		entry := adminUser{
			ID: u.ID, Username: u.Username, Role: u.Role, Disabled: u.Disabled,
			MustChangePassword: u.MustChangePassword, CreatedAt: u.CreatedAt.UTC().Format("2006-01-02"),
		}
		if saves, err := s.store.SavesForOwner(ctx, u.ID); err == nil {
			entry.Saves = len(saves)
		}
		if mons, err := s.store.PokemonInstances(ctx, u.ID); err == nil {
			entry.Pokemon = len(mons)
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "users": out})
}

// createUser adds an account with a generated temporary password, which is
// returned once for the admin to pass on. The new user must replace it at first
// login, so the admin never knows their standing password.
func (s *CatalogServer) createUser(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	role := r.FormValue("role")
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "admin" {
		http.Error(w, "role must be user or admin", http.StatusBadRequest)
		return
	}
	password, err := temporaryPassword()
	if err != nil {
		http.Error(w, "could not generate a password", http.StatusInternalServerError)
		return
	}
	// Reuse the login rules so an account can never be created that cannot sign in.
	if !validCredentials(username, password) {
		http.Error(w, "username must be 1-64 characters of letters, digits, or . _ - @", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if _, err := s.store.UserByUsername(ctx, username); err == nil {
		http.Error(w, "that username is taken", http.StatusConflict)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "could not check the username: "+err.Error(), http.StatusInternalServerError)
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		http.Error(w, "could not secure the password", http.StatusInternalServerError)
		return
	}
	id, err := s.store.CreateUser(ctx, username, hash, role, true)
	if err != nil {
		http.Error(w, "could not create the user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "username": username, "role": role,
		"temporary_password": password,
		"detail":             "Give this password to the user. It is shown only now, and they must change it when they first sign in.",
	})
}

// setUserRole promotes or demotes an account.
func (s *CatalogServer) setUserRole(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	role := r.FormValue("role")
	if role != "user" && role != "admin" {
		http.Error(w, "role must be user or admin", http.StatusBadRequest)
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if err := s.store.SetUserRole(r.Context(), target.ID, role); err != nil {
		writeUserRuleError(w, err, "could not change the role")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": target.ID, "role": role})
}

// setUserDisabled blocks or restores an account's access without touching its data.
func (s *CatalogServer) setUserDisabled(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	disabled := r.FormValue("disabled") == "true"
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if err := s.store.SetUserDisabled(r.Context(), target.ID, disabled, s.actorID(r)); err != nil {
		writeUserRuleError(w, err, "could not change access")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": target.ID, "disabled": disabled})
}

// resetUserPassword issues a fresh temporary password for an account.
func (s *CatalogServer) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	password, err := temporaryPassword()
	if err != nil {
		http.Error(w, "could not generate a password", http.StatusInternalServerError)
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		http.Error(w, "could not secure the password", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetUserPassword(r.Context(), target.ID, hash, true); err != nil {
		http.Error(w, "could not reset the password: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// A reset is usually a response to a lost or shared password: end the old
	// sessions so the new password is the only way back in.
	if err := s.store.DeleteSessionsForUser(r.Context(), target.ID); err != nil {
		http.Error(w, "could not end the user's sessions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": target.ID, "username": target.Username, "temporary_password": password,
		"detail": "Shown only now. The user must change it at their next sign-in.",
	})
}

// deleteUser removes an account, its rows, and its save files. The database
// cascades the rows; the save bytes are files, so they are removed here, the
// save ids are read before the cascade takes them away.
func (s *CatalogServer) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	saves, err := s.store.SavesForOwner(ctx, target.ID)
	if err != nil {
		http.Error(w, "could not read the user's saves: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.DeleteUser(ctx, target.ID, s.actorID(r)); err != nil {
		writeUserRuleError(w, err, "could not delete the user")
		return
	}
	if s.saves != nil {
		for _, save := range saves {
			if err := s.saves.Delete(target.ID, save.ID); err != nil {
				http.Error(w, "the account is gone but its save files could not all be removed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": target.Username, "saves_removed": len(saves)})
}

// targetUser resolves the account named in the URL.
func (s *CatalogServer) targetUser(w http.ResponseWriter, r *http.Request) (catalog.User, bool) {
	user, err := s.store.UserByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such user", http.StatusNotFound)
		return catalog.User{}, false
	}
	if err != nil {
		http.Error(w, "could not read the user", http.StatusInternalServerError)
		return catalog.User{}, false
	}
	return user, true
}

// actorID identifies the admin making a change, so the store can refuse
// self-destructive edits. The admin token has no account behind it, which is why
// this can be empty.
func (s *CatalogServer) actorID(r *http.Request) string {
	if u := currentUser(r); u != nil {
		return u.ID
	}
	return ""
}

// writeUserRuleError maps the store's guard rails onto status codes: a refused
// rule is a conflict with reality, not a server fault.
func writeUserRuleError(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, catalog.ErrLastAdmin):
		http.Error(w, "This is the last admin account. Promote another user to admin first, or you will lock yourself out.", http.StatusConflict)
	case errors.Is(err, catalog.ErrSelfTarget):
		http.Error(w, "You cannot disable or delete your own account.", http.StatusBadRequest)
	default:
		http.Error(w, context+": "+err.Error(), http.StatusInternalServerError)
	}
}

// clearUserDex wipes one account's Pokédex data (Pokémon, dex flags, trainer
// profiles) while keeping its saves and the account itself. The next save
// upload rebuilds everything from the file.
func (s *CatalogServer) clearUserDex(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	ids, err := s.store.ClearDexForUser(r.Context(), target.ID)
	if err != nil {
		http.Error(w, "could not clear the Pokédex: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.deletePokemonFiles(target.ID, ids)
	writeJSON(w, http.StatusOK, map[string]any{"id": target.ID, "username": target.Username, "cleared": true})
}

// pruneDex removes stored Pokémon whose source save is gone, across all users.
func (s *CatalogServer) pruneDex(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	removed, err := s.store.PruneDexOrphans(r.Context())
	if err != nil {
		http.Error(w, "could not prune the Pokédex: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if s.pokemonFiles != nil {
		for _, ref := range removed {
			_ = s.pokemonFiles.Delete(ref.OwnerID, ref.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": len(removed)})
}
