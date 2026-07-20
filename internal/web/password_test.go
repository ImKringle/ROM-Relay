package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
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
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
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

	form := url.Values{"password": {"a new password"}, "confirm": {"a new password"}}
	res, err := client.Post(f.server.URL+"/account/password", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("setting a password should send the user on their way: got %d", res.StatusCode)
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

	form := url.Values{"password": {"a new password"}, "confirm": {"a different password"}}
	res, err := client.Post(f.server.URL+"/account/password", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
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
	real, err := client.PostForm(f.server.URL+"/login", url.Values{"username": {"banned"}, "password": {"known password"}})
	if err != nil {
		t.Fatal(err)
	}
	defer real.Body.Close()
	if real.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a disabled account must not sign in even with the right password: got %d", real.StatusCode)
	}
	disabledBody := readBody(t, real)

	// The refusal must look exactly like a wrong password: a different message
	// tells a stranger the account exists.
	unknown, err := client.PostForm(f.server.URL+"/login", url.Values{"username": {"nobody"}, "password": {"whatever"}})
	if err != nil {
		t.Fatal(err)
	}
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
