package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"testing"
	"time"
)

// browserGet makes a request the way a browser navigates, which is what the
// forced-change redirect keys off: a non-browser caller gets an explanation
// instead of a redirect it could not follow.
func browserGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// noRedirect is a client that reports redirects instead of following them, so a
// test can see where the app is sending the user.
func noRedirect(jar http.CookieJar) *http.Client {
	return &http.Client{
		Jar:           jar,
		Transport:     csrfTransport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// testCSRF is the CSRF token every fixture's relay issues (they all use the
// same admin token, and the token is derived from it, see NewCatalog).
var testCSRF = func() string {
	sum := sha256.Sum256([]byte("romrelay-csrf\x00" + "admin-token-with-24-characters!"))
	return hex.EncodeToString(sum[:])
}()

// csrfTransport sends the CSRF token on every mutation, as the React UI does,
// so signed-in test clients behave like the real pages.
type csrfTransport struct{}

func (csrfTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-CSRF-Token") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("X-CSRF-Token", testCSRF)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// signInWithTempPassword creates an account carrying an admin-issued temporary
// password and returns a client holding its session.
func (f *saveVaultFixture) signInWithTempPassword(t *testing.T, username string) *http.Client {
	t.Helper()
	ctx := context.Background()
	hash, err := HashPassword("temp-password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.store.CreateUser(ctx, username, hash, "user", true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.store.CreateSession(ctx, id, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return noRedirect(&sessionJar{cookie: &http.Cookie{Name: sessionCookie, Value: token}})
}

func TestATemporaryPasswordHoldsTheUserAtTheChangePage(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signInWithTempPassword(t, "misty")

	res := browserGet(t, client, f.server.URL+"/dex")
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("a user on a temporary password must be sent to change it: got %d", res.StatusCode)
	}
	if location := res.Header.Get("Location"); location != "/account/password" {
		t.Fatalf("the redirect must lead to the change-password page: got %q", location)
	}
}

func TestTheChangePasswordPageItselfIsReachableOnATemporaryPassword(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signInWithTempPassword(t, "misty")

	res := browserGet(t, client, f.server.URL+passwordChangePath)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the change page must not redirect to itself, or the user is trapped in a loop: got %d", res.StatusCode)
	}
}

func TestSettingAPasswordClearsTheForcedChangeAndRestoresBrowsing(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signInWithTempPassword(t, "misty")

	res := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/account/password", map[string]string{"password": "a new password", "confirm": "a new password"}, f.ui.csrf)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("setting a password should succeed: got %d", res.StatusCode)
	}

	user, err := f.store.UserByUsername(context.Background(), "misty")
	if err != nil {
		t.Fatal(err)
	}
	if user.MustChangePassword {
		t.Fatal("the forced-change flag must clear once the user sets their own password")
	}
	if !verifyPassword(user.PasswordHash, "a new password") {
		t.Fatal("the new password must be the one stored")
	}

	page := browserGet(t, client, f.server.URL+"/saves")
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("with the flag cleared, normal pages must load again: got %d", page.StatusCode)
	}
}

func TestMismatchedConfirmationDoesNotChangeThePassword(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signInWithTempPassword(t, "misty")

	res := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/account/password", map[string]string{"password": "a new password", "confirm": "a different password"}, f.ui.csrf)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a typo in the confirmation must be refused: got %d", res.StatusCode)
	}
	user, err := f.store.UserByUsername(context.Background(), "misty")
	if err != nil {
		t.Fatal(err)
	}
	if !user.MustChangePassword {
		t.Fatal("a refused change must leave the account still needing one")
	}
}

func TestADisabledAccountCannotSignInAndIsNotIdentifiedAsReal(t *testing.T) {
	f := newSaveVaultFixture(t)
	ctx := context.Background()
	admin := f.adminClient(t, "owner")
	_ = admin
	hash, err := HashPassword("known password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.store.CreateUser(ctx, "banned", hash, "user", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetUserDisabled(ctx, id, true, f.ownerID(t, "owner")); err != nil {
		t.Fatal(err)
	}

	client := noRedirect(nil)
	real := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/session", map[string]string{"username": "banned", "password": "known password"}, "")
	defer real.Body.Close()
	if real.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a disabled account must not sign in even with the right password: got %d", real.StatusCode)
	}
	disabledBody := readBody(t, real)

	// The refusal must look exactly like a wrong password: a different message
	// tells a stranger the account exists.
	unknown := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/session", map[string]string{"username": "nobody", "password": "whatever"}, "")
	defer unknown.Body.Close()
	if body := readBody(t, unknown); body != disabledBody {
		t.Fatal("a disabled account must be refused with the same message as an unknown one, or it confirms the account is real")
	}
}

// readBody returns a response body as a string.
func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
