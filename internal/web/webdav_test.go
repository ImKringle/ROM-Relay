package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
)

// webdavRequest builds a request against the fixture's server, optionally
// with HTTP Basic Auth. method is often a WebDAV verb (PROPFIND, MKCOL) that
// http.NewRequest accepts as an arbitrary string.
func webdavRequest(t *testing.T, f *saveVaultFixture, method, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if method == "PROPFIND" {
		req.Header.Set("Depth", "1")
	}
	if token != "" {
		req.SetBasicAuth("ignored", token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// mintWebDAVKey signs in as username and creates a WebDAV key through the
// same self-service endpoint a real user would use, returning the raw token.
func mintWebDAVKey(t *testing.T, f *saveVaultFixture, username string) string {
	t.Helper()
	client := f.signIn(t, username)
	res := f.post(t, client, "/api/account/webdav-keys", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("could not create a WebDAV key: got %d body=%q", res.StatusCode, readBody(t, res))
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" {
		t.Fatal("a created WebDAV key must return its raw token")
	}
	return created.Token
}

// TestCreateWebDAVKeyStoresTheLabelSentAsMultipartForm pins the actual
// encoding the browser uses (FormData -> multipart/form-data), not the
// url-encoded form f.post sends: a handler that calls r.ParseForm() before
// r.FormValue() silently loses a multipart field, since ParseForm never reads
// a multipart body and FormValue then treats the form as already parsed.
func TestCreateWebDAVKeyStoresTheLabelSentAsMultipartForm(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signIn(t, "may")

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("label", "My phone"); err != nil {
		t.Fatal(err)
	}
	form.Close()
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/api/account/webdav-keys", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("could not create a labelled key: got %d body=%q", res.StatusCode, readBody(t, res))
	}
	var created struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Label != "My phone" {
		t.Fatalf("the create response must echo the submitted label: got %q", created.Label)
	}

	list, err := client.Get(f.server.URL + "/api/account/webdav-keys")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	if !strings.Contains(readBody(t, list), "My phone") {
		t.Fatal("the label must survive into the key list, not just the create response")
	}
}

func TestWebDAVRequiresEnablingAndAValidKey(t *testing.T) {
	f := newSaveVaultFixture(t)
	token := mintWebDAVKey(t, f, "ash")

	// Off by default: even a good key must not open the route.
	disabled := webdavRequest(t, f, "PROPFIND", "/webdav/", token)
	defer disabled.Body.Close()
	if disabled.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("WebDAV must be off by default: got %d", disabled.StatusCode)
	}

	f.ui.SetWebDAVEnabled(true)

	noAuth := webdavRequest(t, f, "PROPFIND", "/webdav/", "")
	defer noAuth.Body.Close()
	if noAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials must be refused: got %d", noAuth.StatusCode)
	}
	if got := noAuth.Header.Get("WWW-Authenticate"); got == "" {
		t.Fatal("a 401 must carry a WWW-Authenticate challenge for a Basic Auth client to react to")
	}

	badToken := webdavRequest(t, f, "PROPFIND", "/webdav/", "not-a-real-token")
	defer badToken.Body.Close()
	if badToken.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unknown token must be refused: got %d", badToken.StatusCode)
	}

	good := webdavRequest(t, f, "PROPFIND", "/webdav/", token)
	defer good.Body.Close()
	if good.StatusCode != http.StatusMultiStatus {
		t.Fatalf("a valid key must list the root collection: got %d body=%q", good.StatusCode, readBody(t, good))
	}
}

func TestWebDAVListsAndDownloadsTheCatalog(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetWebDAVEnabled(true)
	token := mintWebDAVKey(t, f, "misty")

	games, err := f.store.Games(context.Background())
	if err != nil || len(games) == 0 || len(games[0].Artifacts) == 0 {
		t.Fatalf("fixture must have a catalog artifact to serve: %v", err)
	}
	artifact := games[0].Artifacts[0]

	root := webdavRequest(t, f, "PROPFIND", "/webdav/", token)
	defer root.Body.Close()
	rootBody := readBody(t, root)
	if root.StatusCode != http.StatusMultiStatus || !strings.Contains(rootBody, artifact.Platform) {
		t.Fatalf("root listing must include the %q platform collection: got %d body=%q", artifact.Platform, root.StatusCode, rootBody)
	}

	platform := webdavRequest(t, f, "PROPFIND", "/webdav/"+artifact.Platform+"/", token)
	defer platform.Body.Close()
	platformBody := readBody(t, platform)
	if platform.StatusCode != http.StatusMultiStatus || !strings.Contains(platformBody, artifact.Name) {
		t.Fatalf("platform listing must include %q: got %d body=%q", artifact.Name, platform.StatusCode, platformBody)
	}

	want, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	get := webdavRequest(t, f, http.MethodGet, "/webdav/"+artifact.Platform+"/"+artifact.Name, token)
	defer get.Body.Close()
	got, err := io.ReadAll(get.Body)
	if err != nil {
		t.Fatal(err)
	}
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET on a listed artifact must succeed: got %d", get.StatusCode)
	}
	if string(got) != string(want) {
		t.Fatalf("downloaded bytes must match the file on disk: got %d bytes, want %d", len(got), len(want))
	}
}

func TestWebDAVIsReadOnly(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetWebDAVEnabled(true)
	token := mintWebDAVKey(t, f, "brock")

	games, err := f.store.Games(context.Background())
	if err != nil || len(games) == 0 || len(games[0].Artifacts) == 0 {
		t.Fatalf("fixture must have a catalog artifact: %v", err)
	}
	artifact := games[0].Artifacts[0]
	target := "/webdav/" + artifact.Platform + "/" + artifact.Name

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		res := webdavRequest(t, f, method, target, token)
		res.Body.Close()
		if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusNoContent || res.StatusCode == http.StatusCreated {
			t.Fatalf("%s must never succeed against the read-only catalog: got %d", method, res.StatusCode)
		}
	}
	mkcol := webdavRequest(t, f, "MKCOL", "/webdav/new-folder/", token)
	mkcol.Body.Close()
	if mkcol.StatusCode == http.StatusCreated {
		t.Fatalf("MKCOL must never succeed against the read-only catalog: got %d", mkcol.StatusCode)
	}

	// The file on disk must be untouched by the attempted writes above.
	if _, err := os.Stat(artifact.Path); err != nil {
		t.Fatalf("the backing file must still exist after rejected writes: %v", err)
	}
}

func TestARevokedWebDAVKeyStopsWorkingImmediately(t *testing.T) {
	f := newSaveVaultFixture(t)
	f.ui.SetWebDAVEnabled(true)
	client := f.signIn(t, "gary")

	created := f.post(t, client, "/api/account/webdav-keys", nil)
	var body struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(created.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	before := webdavRequest(t, f, "PROPFIND", "/webdav/", body.Token)
	before.Body.Close()
	if before.StatusCode != http.StatusMultiStatus {
		t.Fatalf("a fresh key must work: got %d", before.StatusCode)
	}

	req, err := http.NewRequest(http.MethodDelete, f.server.URL+"/api/account/webdav-keys/"+body.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	revoke, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke.Body.Close()
	if revoke.StatusCode != http.StatusOK {
		t.Fatalf("the owner must be able to revoke their own key: got %d body=%q", revoke.StatusCode, readBody(t, revoke))
	}

	after := webdavRequest(t, f, "PROPFIND", "/webdav/", body.Token)
	defer after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked key must stop authenticating immediately: got %d", after.StatusCode)
	}
}
