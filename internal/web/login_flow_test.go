package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// TestANonAdminCanSignInThroughTheLoginForm drives the real POST /login path,
// every other test injects a session cookie directly, which is how a login
// regression shipped unseen.
func TestANonAdminCanSignInThroughTheLoginForm(t *testing.T) {
	f := newSaveVaultFixture(t)
	ctx := context.Background()
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUser(ctx, "brock", hash, "user", false); err != nil {
		t.Fatal(err)
	}

	jar, _ := cookiejar.New(nil)
	client := noRedirect(jar)
	res, err := client.Post(f.server.URL+"/login", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{"username": {"brock"}, "password": {"correct horse battery"}, "next": {"/"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("a correct password must sign in: got %d body=%q", res.StatusCode, readBody(t, res))
	}

	page := browserGet(t, client, f.server.URL+"/saves")
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("the session from the login form must open normal pages: got %d", page.StatusCode)
	}
}

// TestAFreshAccountCanSignInAndSetItsOwnPassword drives the whole first-login
// journey: temp password in, forced to the change page, new password accepted.
func TestAFreshAccountCanSignInAndSetItsOwnPassword(t *testing.T) {
	f := newSaveVaultFixture(t)
	ctx := context.Background()
	hash, err := HashPassword("temp-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUser(ctx, "may", hash, "user", true); err != nil {
		t.Fatal(err)
	}

	jar, _ := cookiejar.New(nil)
	client := noRedirect(jar)
	res, err := client.Post(f.server.URL+"/login", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{"username": {"may"}, "password": {"temp-pass-123"}, "next": {"/"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the temp password must sign in: got %d", res.StatusCode)
	}

	change, err := client.Post(f.server.URL+passwordChangePath, "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{"password": {"my own password"}, "confirm": {"my own password"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer change.Body.Close()
	if change.StatusCode != http.StatusSeeOther {
		t.Fatalf("a valid new password must be accepted: got %d body=%q", change.StatusCode, readBody(t, change))
	}
}
