package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
)

// bearer sends a GET with the given token as an Authorization: Bearer header,
// with no session cookie, the request stands entirely on the API key.
func bearer(t *testing.T, base, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestAnAPIKeyAuthenticatesAdminAndGeneralRoutesUntilRevoked drives the whole
// API-key lifecycle: an admin mints a key, the raw token works against both
// an admin-only route and a general logged-in-only route with no cookie at
// all, and revoking it immediately stops both.
func TestAnAPIKeyAuthenticatesAdminAndGeneralRoutesUntilRevoked(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")

	created := f.post(t, admin, "/api/admin/api-keys", url.Values{"name": {"ci-script"}})
	defer created.Body.Close()
	if created.StatusCode != http.StatusOK {
		t.Fatalf("an admin must be able to create an API key: got %d body=%q", created.StatusCode, readBody(t, created))
	}
	var key struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	body, err := io.ReadAll(created.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &key); err != nil {
		t.Fatalf("could not parse the create response: %v (body=%s)", err, body)
	}
	if key.Token == "" {
		t.Fatal("a created key must return its raw token")
	}

	// The admin-only surface: no cookie, just the bearer token.
	adminOnly := bearer(t, f.server.URL, "/api/admin/games", key.Token)
	defer adminOnly.Body.Close()
	if adminOnly.StatusCode != http.StatusOK {
		t.Fatalf("the key must open an admin API route: got %d body=%q", adminOnly.StatusCode, readBody(t, adminOnly))
	}

	// The general logged-in-only surface: /api/me requires a resolvable
	// user, not admin specifically, this is what proves the key covers all
	// internal API usage, not just /api/admin/*.
	me := bearer(t, f.server.URL, "/api/me", key.Token)
	defer me.Body.Close()
	var meBody struct {
		Authenticated bool `json:"authenticated"`
		Admin         bool `json:"admin"`
	}
	if err := json.NewDecoder(me.Body).Decode(&meBody); err != nil {
		t.Fatal(err)
	}
	if !meBody.Authenticated || !meBody.Admin {
		t.Fatalf("a valid key must resolve to an authenticated admin identity, got %+v", meBody)
	}

	// A bad token must not be accepted anywhere the good one worked.
	bad := bearer(t, f.server.URL, "/api/admin/games", "rr_not-a-real-token")
	defer bad.Body.Close()
	if bad.StatusCode == http.StatusOK {
		t.Fatal("an invalid bearer token must not authenticate")
	}

	// f.post always issues a POST; revocation is a DELETE, so the request is
	// built directly instead of reusing that helper.
	req, err := http.NewRequest(http.MethodDelete, f.server.URL+"/api/admin/api-keys/"+key.ID, nil)
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
		t.Fatalf("an admin must be able to revoke a key: got %d body=%q", res.StatusCode, readBody(t, res))
	}

	after := bearer(t, f.server.URL, "/api/admin/games", key.Token)
	defer after.Body.Close()
	if after.StatusCode == http.StatusOK {
		t.Fatal("a revoked key must stop authenticating immediately")
	}
}

// TestTheAPIKeysPageRendersForASignedInAdmin catches a template execution
// error the JSON-API test above cannot: apikeys.html is only ever exercised
// when this specific page is requested.
func TestTheAPIKeysPageRendersForASignedInAdmin(t *testing.T) {
	f := newSaveVaultFixture(t)
	admin := f.adminClient(t, "owner")
	res, err := admin.Get(f.server.URL + "/admin/api-keys")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the API keys page must render for a signed-in admin: got %d body=%q", res.StatusCode, readBody(t, res))
	}
}
