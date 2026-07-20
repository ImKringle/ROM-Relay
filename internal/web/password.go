package web

import (
	"net/http"
)

// passwordChangePath is where a user is held while their password is one an
// admin generated for them.
const passwordChangePath = "/account/password"

// needsPasswordChange reports whether this request belongs to a signed-in user
// who is still carrying an admin-issued temporary password.
func needsPasswordChange(r *http.Request) bool {
	u := currentUser(r)
	return u != nil && u.MustChangePassword
}

// passwordChangePage renders the form for setting your own password.
//
// This page is deliberately plain: the UI overhaul owns page layout, and this
// exists so an account created through the admin API can actually be used in the
// meantime. Only its markup is expected to change.
func (s *CatalogServer) passwordChangePage(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login?next="+passwordChangePath, http.StatusSeeOther)
		return
	}
	s.renderPasswordChange(w, r, "", http.StatusOK)
}

func (s *CatalogServer) renderPasswordChange(w http.ResponseWriter, r *http.Request, message string, status int) {
	user := currentUser(r)
	data := passwordPage{
		pageData: s.newPageData(r),
		Username: user.Username,
		Forced:   user.MustChangePassword,
		Error:    message,
	}
	data.Bare = true
	s.renderStatus(w, status, "password", "password", data)
}

// passwordPage is the change-password view.
type passwordPage struct {
	pageData
	Username string
	Forced   bool
	Error    string
}

// changePassword sets the signed-in user's own password.
//
// A user still on a temporary password is not asked for their current one: they
// just used it to get here, and demanding it again only invites them to write it
// down. Anyone changing an established password must confirm the current one, so
// a borrowed unlocked browser cannot lock the owner out of their own account.
func (s *CatalogServer) changePassword(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("confirm") {
		s.renderPasswordChange(w, r, "Those passwords do not match.", http.StatusBadRequest)
		return
	}
	if !validCredentials(user.Username, password) {
		s.renderPasswordChange(w, r, "Choose a password of 1-72 characters, without control characters.", http.StatusBadRequest)
		return
	}
	if !user.MustChangePassword && !verifyPassword(user.PasswordHash, r.FormValue("current")) {
		s.renderPasswordChange(w, r, "That is not your current password.", http.StatusBadRequest)
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		http.Error(w, "could not secure the password", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetUserPassword(r.Context(), user.ID, hash, false); err != nil {
		http.Error(w, "could not save the password: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
