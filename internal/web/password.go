package web

import (
	"encoding/json"
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

// apiChangePassword is changePassword for the React UI: JSON in, JSON out,
// the same rules (no current password needed on a temporary one).
func (s *CatalogServer) apiChangePassword(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct{ Current, Password, Confirm string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	switch {
	case body.Password != body.Confirm:
		http.Error(w, "Those passwords do not match.", http.StatusBadRequest)
		return
	case !validCredentials(user.Username, body.Password):
		http.Error(w, "Choose a password of 1-72 characters, without control characters.", http.StatusBadRequest)
		return
	case !user.MustChangePassword && !verifyPassword(user.PasswordHash, body.Current):
		http.Error(w, "That is not your current password.", http.StatusBadRequest)
		return
	}
	hash, err := HashPassword(body.Password)
	if err != nil {
		http.Error(w, "could not secure the password", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetUserPassword(r.Context(), user.ID, hash, false); err != nil {
		http.Error(w, "could not save the password", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
