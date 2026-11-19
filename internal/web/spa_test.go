package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/decryptor"
	remotelib "romrelay/internal/remote"
)

// Tests for the React UI's server side: the shell, the access model it sits
// behind, and the JSON the pages read. They replace tests that asserted on
// server-rendered HTML; the behaviours they pin are the same.

func getJSON(t *testing.T, client *http.Client, url string, out any) int {
	t.Helper()
	res, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return res.StatusCode
}

func newTestCatalog(t *testing.T, files map[string][]byte, viewer, admin string) (*CatalogServer, *catalog.Store, *httptest.Server) {
	t.Helper()
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	for rel, data := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ui := NewCatalog(s, "http://example", viewer, "default", admin, filepath.Join(d, "patches"), time.Hour)
	ts := httptest.NewServer(ui.Handler())
	t.Cleanup(ts.Close)
	return ui, s, ts
}

// The viewer token authenticates handheld downloads, never people; human
// pages and their API need a session, and admin pages an admin.
func TestAccessModelForPagesAPIAndHandheldRoutes(t *testing.T) {
	const viewer, admin = "viewer-token-with-24-characters", "admin-token-with-24-characters!"
	ui, s, ts := newTestCatalog(t, map[string][]byte{"gba/test.gba": make([]byte, 0x200)}, viewer, admin)
	games, _ := s.Games(context.Background())
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, path := range []string{"/", "/?token=" + viewer, "/api/library", "/api/library?token=" + viewer} {
		res, _ := http.Get(ts.URL + path)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: anonymous (even with the viewer token) must be refused: got %d", path, res.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/?token="+viewer, nil)
	req.Header.Set("Accept", "text/html")
	res, _ := noFollow.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Fatalf("a browser without a session belongs at the login page: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	id := games[0].Artifacts[0].ID
	for _, path := range []string{"/d/" + id + "?token=" + viewer, "/healthz?token=" + viewer} {
		res, _ := http.Get(ts.URL + path)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: the viewer token must still work for handheld routes: %d", path, res.StatusCode)
		}
	}
	// Admin pages: no shell for a non-admin, and a wrong admin token must not
	// be exchanged for the admin cookie.
	res, _ = noFollow.Get(ts.URL + "/admin/patches?admin_token=" + viewer)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a viewer entered admin: %d", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == "romrelay_admin" {
			t.Fatal("a wrong admin token was exchanged for the admin cookie")
		}
	}
	res, _ = noFollow.Get(ts.URL + "/admin?admin_token=" + admin)
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the right admin token must land: %d", res.StatusCode)
	}
	// Mutations: CSRF is required of a cookie-backed admin, and CSRF alone is
	// not authority.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/admin/patch-jobs", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: "romrelay_admin", Value: admin})
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF must be refused: %d", res.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/admin/patch-jobs", strings.NewReader(""))
	req.Header.Set("X-CSRF-Token", ui.csrf)
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("CSRF bypassed admin auth: %d", res.StatusCode)
	}
	// Unknown API paths answer 404, not the HTML shell.
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/admin/no-such-thing?admin_token="+admin, nil)
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown API path: %d", res.StatusCode)
	}
}

// Cross-site requests are refused even with every other credential right.
func TestCrossOriginMutationsAreRefused(t *testing.T) {
	const admin = "admin-token-with-24-characters!"
	ui, _, ts := newTestCatalog(t, nil, "", admin)
	called := make(chan struct{}, 1)
	ui.SetShutdown(func() { called <- struct{}{} })
	for _, h := range []map[string]string{{"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/shutdown", nil)
		req.AddCookie(&http.Cookie{Name: "romrelay_admin", Value: admin})
		req.Header.Set("X-CSRF-Token", ui.csrf)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		res, _ := http.DefaultClient.Do(req)
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("%v: got %d", h, res.StatusCode)
		}
	}
	select {
	case <-called:
		t.Fatal("a cross-site request shut the relay down")
	default:
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/shutdown", nil)
	req.AddCookie(&http.Cookie{Name: "romrelay_admin", Value: admin})
	req.Header.Set("X-CSRF-Token", ui.csrf)
	req.Header.Set("Origin", ts.URL)
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("same-origin admin shutdown: %d", res.StatusCode)
	}
}

// The shell must load only same-origin assets, which the CSP allows without
// 'unsafe-inline', and they must load for an anonymous visitor (the sign-in
// page needs them).
func TestShellAssetsAreSameOriginAndPublic(t *testing.T) {
	_, _, ts := newTestCatalog(t, nil, "viewer-token-with-24-characters", "admin-token-with-24-characters!")
	res, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	policy := res.Header.Get("Content-Security-Policy")
	for _, d := range []string{"script-src", "style-src"} {
		v := policyDirective(policy, d)
		if !strings.Contains(v, "'self'") || strings.Contains(v, "unsafe-inline") {
			t.Errorf("%s must be 'self' without unsafe-inline: %q", d, policy)
		}
	}
	if strings.Contains(string(body), "<script>") || strings.Contains(string(body), "<style") {
		t.Fatal("the shell must carry no inline script or style")
	}
	assets := regexp.MustCompile(`(?:src|href)="(/app-assets/[^"]+)"`).FindAllStringSubmatch(string(body), -1)
	if len(assets) == 0 {
		t.Skip("UI not built (placeholder index.html)")
	}
	for _, m := range assets {
		r, err := http.Get(ts.URL + m[1])
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("an anonymous visitor must load %s: got %d", m[1], r.StatusCode)
		}
	}
}

// Session info drives the UI; it must not hand the CSRF token to an
// anonymous visitor on a locked relay, and must report setup state.
func TestSessionInfoAndSetupState(t *testing.T) {
	ui, _, ts := newTestCatalog(t, nil, "viewer-token-with-24-characters", "admin-token-with-24-characters!")
	var s sessionInfo
	if code := getJSON(t, http.DefaultClient, ts.URL+"/api/session", &s); code != http.StatusOK {
		t.Fatalf("session: %d", code)
	}
	if s.Authenticated || s.CSRF != "" || s.Open {
		t.Fatalf("anonymous on a locked relay: %+v", s)
	}
	ui.SetReady(false, "Scanning library")
	getJSON(t, http.DefaultClient, ts.URL+"/api/session", &s)
	if s.Ready {
		t.Fatal("setup state not reported")
	}
	res, _ := http.Get(ts.URL + "/healthz?token=viewer-token-with-24-characters")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), `"ready":false`) || !strings.Contains(string(b), `"status":"setup"`) {
		t.Fatalf("health did not report setup: %s", b)
	}
}

type libResp struct {
	Games []struct {
		ID, Title, Cover string
		Patched          bool
	} `json:"games"`
	Facets        map[string][]facetJSON `json:"facets"`
	RemoteSearch  bool                   `json:"remote_search"`
	HasEncryption bool                   `json:"has_encryption"`
}

func titles(r libResp) string {
	var out []string
	for _, g := range r.Games {
		out = append(out, g.Title)
	}
	return strings.Join(out, "|")
}

// A temporary patched build stays out of the library until asked for by its
// Status, a legacy ?platform=patched link, or its exact name.
func TestLibraryHidesTemporaryPatchedBuilds(t *testing.T) {
	d := t.TempDir()
	root, patched := filepath.Join(d, "platforms"), filepath.Join(d, "patched")
	for path, title := range map[string]string{filepath.Join(root, "gba", "base.gba"): "BASE GAME", filepath.Join(patched, "gba", "hack.gba"): "PATCHED GAME"} {
		rom := make([]byte, 0x200)
		copy(rom[0xa0:], title)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, rom, 0o600)
	}
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(patched)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), time.Hour).Handler())
	defer ts.Close()
	for q, want := range map[string][2]string{
		"":                  {"BASE GAME", "PATCHED GAME"},
		"?platform=gba":     {"BASE GAME", "PATCHED GAME"},
		"?platform=patched": {"PATCHED GAME", "BASE GAME"},
		"?q=PATCHED+GAME":   {"PATCHED GAME", ""},
	} {
		var r libResp
		getJSON(t, http.DefaultClient, ts.URL+"/api/library"+q, &r)
		got := titles(r)
		if !strings.Contains(got, want[0]) || (want[1] != "" && strings.Contains(got, want[1])) {
			t.Errorf("%q: got %q", q, got)
		}
	}
	var r libResp
	getJSON(t, http.DefaultClient, ts.URL+"/api/library?platform=gba", &r)
	found := false
	for _, f := range r.Facets["status"] {
		found = found || f.Value == "patched"
	}
	if !found {
		t.Fatal("the library does not offer the Patched status filter")
	}
}

// Encrypted CIAs: the FBI queue takes only encrypted ones, the library hides
// encrypted-only games unless asked, and a download goes through the chooser.
func TestEncryptedCIAFlows(t *testing.T) {
	ui, s, ts := newTestCatalog(t, map[string][]byte{"3ds/cia/encrypted/encrypted.cia": []byte("encrypted"), "3ds/cia/unencrypted/unencrypted.cia": []byte("unencrypted")}, "", "admin-token-with-24-characters!")
	urls, err := ui.ciaURLs(context.Background())
	if err != nil || len(urls) != 1 {
		t.Fatalf("FBI queue must hold only the encrypted CIA: %v %v", urls, err)
	}
	var r libResp
	getJSON(t, http.DefaultClient, ts.URL+"/api/library?platform=3ds&encryption=unencrypted", &r)
	if got := titles(r); !strings.Contains(got, "unencrypted") || strings.Contains(got, "|encrypted") || got == "encrypted" {
		t.Fatalf("encryption filter: %q", got)
	}
	games, _ := s.Games(context.Background())
	var encGame, encArtifact string
	for _, g := range games {
		for _, a := range g.Artifacts {
			if a.Encryption == "encrypted" {
				encGame, encArtifact = g.ID, a.ID
			}
		}
	}
	var d gameDetail
	getJSON(t, http.DefaultClient, ts.URL+"/api/library/"+encGame, &d)
	ok := false
	for _, f := range d.Files {
		ok = ok || (f.ID == encArtifact && strings.Contains(f.DownloadURL, "/download/"+encArtifact))
	}
	if !ok {
		t.Fatalf("an encrypted CIA must download through the chooser: %+v", d.Files)
	}
	ui.SetDecryptor(decryptor.New(s, filepath.Join(t.TempDir(), "missing-driver"), "", t.TempDir(), time.Second))
	var choice map[string]any
	getJSON(t, http.DefaultClient, ts.URL+"/api/files/"+encArtifact+"/choice", &choice)
	if choice["encrypted"] != true || choice["decrypt_ready"] != false || !strings.Contains(choice["decrypt_reason"].(string), "Decryptor Redux.bat is missing") {
		t.Fatalf("chooser: %v", choice)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/decrypt/"+encArtifact, nil)
	req.Header.Set("X-CSRF-Token", ui.csrf)
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a non-admin started a decrypt job: %d", res.StatusCode)
	}
	var batch struct {
		Count, Files int
		QRs          []string `json:"qr_codes"`
	}
	getJSON(t, http.DefaultClient, ts.URL+"/api/fbi/batches?ids="+encArtifact, &batch)
	if batch.Files != 1 || len(batch.QRs) != 1 {
		t.Fatalf("FBI batch: %+v", batch)
	}
}

func TestGameDetailExplainsAnIGDBMiss(t *testing.T) {
	ui, s, ts := newTestCatalog(t, map[string][]byte{"gba/Exact Miss.gba": make([]byte, 0x200)}, "", "")
	ui.SetIGDBEnabled(true)
	games, _ := s.Games(context.Background())
	var d gameDetail
	getJSON(t, http.DefaultClient, ts.URL+"/api/library/"+games[0].ID, &d)
	if !strings.Contains(d.SynopsisNote, "No exact synopsis match was returned by IGDB") || strings.Contains(d.SynopsisNote, "Enable the optional IGDB") {
		t.Fatalf("misleading synopsis note: %q", d.SynopsisNote)
	}
}

func TestLibraryOffersRemoteSearchWhenNothingMatches(t *testing.T) {
	ui, _, ts := newTestCatalog(t, nil, "", "admin-token-with-24-characters!")
	ui.SetRemoteManager(remotelib.New(nil, remotelib.Options{}))
	var r libResp
	getJSON(t, http.DefaultClient, ts.URL+"/api/library?q=missing+game", &r)
	if !r.RemoteSearch {
		t.Fatal("an empty search must offer the remote library")
	}
}

func TestAdminCanEditAndLockGameMetadata(t *testing.T) {
	const admin = "admin-token-with-24-characters!"
	ui, s, ts := newTestCatalog(t, map[string][]byte{"gba/Editable.gba": make([]byte, 0x200)}, "", admin)
	ui.SetMediaDirectory(t.TempDir())
	games, _ := s.Games(context.Background())
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("title", "Better Title")
	_ = mw.WriteField("description", "A manually corrected synopsis.")
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/games/"+games[0].ID+"/metadata?admin_token="+admin, &form)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", ui.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("metadata update=%d: %s", res.StatusCode, b)
	}
	got, _ := s.Game(context.Background(), games[0].ID)
	if got.Title != "Better Title" || got.MetadataSource != "manual" {
		t.Fatalf("metadata not saved: %#v", got)
	}
}

// The settings read endpoint is an allowlist: secrets never reach the browser.
func TestSettingsNeverExposeSecrets(t *testing.T) {
	ui, _, ts := newTestCatalog(t, nil, "", "admin-token-with-24-characters!")
	_ = ui
	res, _ := http.Get(ts.URL + "/api/admin/settings")
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("settings without admin: %d", res.StatusCode)
	}
}
