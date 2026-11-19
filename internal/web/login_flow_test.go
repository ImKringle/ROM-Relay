package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

// sendJSON makes a same-origin JSON request the way the React UI does. csrf
// may be empty, to prove a request without it is refused.
func sendJSON(t *testing.T, client *http.Client, method, url string, body any, csrf string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(method, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestANonAdminCanSignInThroughTheSessionAPI drives the real sign-in path
// (POST /api/session); most tests inject a session cookie directly, which is
// how a login regression once shipped unseen.
func TestANonAdminCanSignInThroughTheSessionAPI(t *testing.T) {
	f := newSaveVaultFixture(t)
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUser(context.Background(), "brock", hash, "user", false); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := noRedirect(jar)
	res := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/session", map[string]string{"username": "brock", "password": "correct horse battery"}, "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a correct password must sign in: got %d body=%q", res.StatusCode, readBody(t, res))
	}
	var s sessionInfo
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil || !s.Authenticated || s.Username != "brock" || s.CSRF == "" {
		t.Fatalf("sign-in must return the new session with a CSRF token: %+v %v", s, err)
	}
	page := browserGet(t, client, f.server.URL+"/saves")
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("the new session must open normal pages: got %d", page.StatusCode)
	}
	api, err := client.Get(f.server.URL + "/api/saves")
	if err != nil {
		t.Fatal(err)
	}
	api.Body.Close()
	if api.StatusCode != http.StatusOK {
		t.Fatalf("the new session must reach the API: got %d", api.StatusCode)
	}
}

// TestSignInRefusesCrossSiteRequests: a login form on another site must not
// be able to sign a victim's browser into an attacker's account.
func TestSignInRefusesCrossSiteRequests(t *testing.T) {
	f := newSaveVaultFixture(t)
	req, _ := http.NewRequest(http.MethodPost, f.server.URL+"/api/session", strings.NewReader(`{"username":"a","password":"b"}`))
	req.Header.Set("Origin", "https://evil.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-origin sign-in must be refused: got %d", res.StatusCode)
	}
}

// TestRepeatedFailedSignInsAreThrottled: after 5 wrong passwords the account
// (and address) waits, even if the 6th guess is right.
func TestRepeatedFailedSignInsAreThrottled(t *testing.T) {
	f := newSaveVaultFixture(t)
	hash, _ := HashPassword("right password")
	if _, err := f.store.CreateUser(context.Background(), "gary", hash, "user", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		res := sendJSON(t, http.DefaultClient, http.MethodPost, f.server.URL+"/api/session", map[string]string{"username": "gary", "password": "wrong"}, "")
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d", i, res.StatusCode)
		}
	}
	res := sendJSON(t, http.DefaultClient, http.MethodPost, f.server.URL+"/api/session", map[string]string{"username": "gary", "password": "right password"}, "")
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("a throttled account must wait even with the right password: got %d", res.StatusCode)
	}
}

// TestAFreshAccountCanSignInAndSetItsOwnPassword drives the whole first-login
// journey: temporary password in, then a new password accepted.
func TestAFreshAccountCanSignInAndSetItsOwnPassword(t *testing.T) {
	f := newSaveVaultFixture(t)
	hash, err := HashPassword("temp-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateUser(context.Background(), "may", hash, "user", true); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := noRedirect(jar)
	res := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/session", map[string]string{"username": "may", "password": "temp-pass-123"}, "")
	var s sessionInfo
	_ = json.NewDecoder(res.Body).Decode(&s)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !s.MustChangePassword {
		t.Fatalf("the temp password must sign in and flag the forced change: %d %+v", res.StatusCode, s)
	}
	change := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/account/password", map[string]string{"password": "my own password", "confirm": "my own password"}, s.CSRF)
	defer change.Body.Close()
	if change.StatusCode != http.StatusNoContent {
		t.Fatalf("a valid new password must be accepted: got %d body=%q", change.StatusCode, readBody(t, change))
	}
}
