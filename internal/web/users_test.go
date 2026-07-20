package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// adminClient signs in an admin account and returns a client holding its session.
func (f *saveVaultFixture) adminClient(t *testing.T, username string) *http.Client {
	t.Helper()
	ctx := context.Background()
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.store.CreateUser(ctx, username, hash, "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.store.CreateSession(ctx, id, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: &sessionJar{cookie: &http.Cookie{Name: sessionCookie, Value: token}}}
}

// post sends a form to an admin route with the CSRF token attached.
func (f *saveVaultFixture) post(t *testing.T, client *http.Client, path string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", f.ui.csrf)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCreatingAUserReturnsATemporaryPasswordThatWorksOnce(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")

	res := f.post(t, admin, "/api/admin/users", url.Values{"username": {"misty"}, "role": {"user"}})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an admin must be able to create a user: got %d", res.StatusCode)
	}
	var created struct {
		Username     string `json:"username"`
		TempPassword string `json:"temporary_password"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.TempPassword == "" {
		t.Fatal("the admin needs the generated password to pass on; it is shown only here")
	}

	// The password must actually let the new user in.
	form := url.Values{"username": {"misty"}, "password": {created.TempPassword}}
	login, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).
		PostForm(f.server.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer login.Body.Close()
	if login.StatusCode != http.StatusSeeOther {
		t.Fatalf("the temporary password must work at the login page: got %d", login.StatusCode)
	}

	user, err := f.store.UserByUsername(context.Background(), "misty")
	if err != nil {
		t.Fatal(err)
	}
	if !user.MustChangePassword {
		t.Fatal("a generated password must force a change, or the admin's password stays the user's password")
	}
	if user.PasswordHash == created.TempPassword {
		t.Fatal("the password must be stored as a hash, never as the plaintext that was shown")
	}
}

func TestASignedInNonAdminCannotReachUserManagement(t *testing.T) {
	f := newSaveVaultFixture(t)
	plain := f.signIn(t, "ash")
	victim := f.adminClient(t, "owner")
	_ = victim

	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/admin/users"},
		{http.MethodPost, "/api/admin/users"},
		{http.MethodPost, "/api/admin/users/someone/role"},
		{http.MethodPost, "/api/admin/users/someone/disabled"},
		{http.MethodPost, "/api/admin/users/someone/reset-password"},
		{http.MethodDelete, "/api/admin/users/someone"},
	}
	for _, route := range routes {
		req, err := http.NewRequest(route.method, f.server.URL+route.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-CSRF-Token", f.ui.csrf)
		res, err := plain.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: a plain user must not manage accounts: got %d", route.method, route.path, res.StatusCode)
		}
	}
}

// The screen is admin-only like the API behind it. A plain user reaching it would
// see a page of controls that every request behind them refuses.
func TestTheUsersScreenIsAdminOnly(t *testing.T) {
	f := newSaveVaultFixture(t)
	plain := f.signIn(t, "ash")

	res, err := plain.Get(f.server.URL + "/admin/users")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a plain user must not reach the users screen: got %d", res.StatusCode)
	}

	admin := f.adminClient(t, "owner")
	res, err = admin.Get(f.server.URL + "/admin/users")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an admin must reach the users screen: got %d", res.StatusCode)
	}
	// The list itself is filled from the API by the bundle; what the page must
	// carry is the create form and the shell that wires it.
	for _, required := range []string{`id="create-user-form"`, `name="username"`, `name="role"`, `id="users-list"`, `src="/assets/app.js"`} {
		if !strings.Contains(string(body), required) {
			t.Errorf("the users screen is missing %q", required)
		}
	}
}

func TestRefusingToRemoveTheLastAdminIsReportedAsAConflict(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")
	id := f.ownerID(t, "owner")

	res := f.post(t, admin, "/api/admin/users/"+id+"/role", url.Values{"role": {"user"}})
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("demoting the last admin must be refused with a conflict: got %d", res.StatusCode)
	}
}

func TestDeletingAUserRemovesTheirSaveFilesFromDisk(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")
	victim := f.signIn(t, "ash")
	f.upload(t, victim, f.gameID, "hg.sav", []byte("their only copy")).Body.Close()

	victimID := f.ownerID(t, "ash")
	saves, err := f.store.SavesForOwner(context.Background(), victimID)
	if err != nil || len(saves) != 1 {
		t.Fatalf("fixture needs one stored save: %v", err)
	}
	path := filepath.Join(f.vaultRoot, victimID, saves[0].ID+".sav")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the save file should exist before deletion: %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, f.server.URL+"/api/admin/users/"+victimID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CSRF-Token", f.ui.csrf)
	res, err := admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an admin must be able to delete a user: got %d", res.StatusCode)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("deleting a user must remove their save files, not leave orphaned bytes on disk")
	}
	if _, err := f.store.UserByUsername(context.Background(), "ash"); err == nil {
		t.Fatal("the account must be gone")
	}
}
