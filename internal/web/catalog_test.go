package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"romrelay/internal/catalog"
	"romrelay/internal/decryptor"
	patcher "romrelay/internal/patch"
	remotelib "romrelay/internal/remote"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSearchFoldIgnoresAccents(t *testing.T) {
	if !strings.Contains(searchFold("Pokémon Mystery Dungeon"), searchFold("Pokemon")) {
		t.Fatal("Pokemon should match Pokémon")
	}
}

func TestPublicPatchWorkspaceRequiresOwnROMAndForcesExpiry(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	output := filepath.Join(d, "patched")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(output)
	m := patcher.NewManager(s, patcher.ManagerConfig{LibraryDirectory: root, OutputDirectory: output, MaxROMBytes: 1 << 20, MaxPatchBytes: 1 << 20, Retention: time.Hour, Timeout: time.Second})
	defer m.Shutdown()
	ui := NewCatalog(s, "http://example", "", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), time.Hour)
	ui.SetPatchManager(m)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	page, err := http.Get(ts.URL + "/patches")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(html), "Clean base ROM") || !strings.Contains(string(html), "From your library") || !strings.Contains(string(html), "Temporary workshop") || !strings.Contains(string(html), "Cover / icon artwork") || !strings.Contains(string(html), "Description") {
		t.Fatalf("public patch help missing: %s", html)
	}
	var csrfCookie *http.Cookie
	for _, c := range page.Cookies() {
		if c.Name == "romrelay_patch_csrf" {
			csrfCookie = c
		}
	}
	if csrfCookie == nil {
		t.Fatal("public CSRF cookie missing")
	}
	csrf := csrfCookie.Value
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	romPart, _ := mw.CreateFormFile("rom", "mine.gba")
	_, _ = romPart.Write(make([]byte, 0x200))
	patchPart, _ := mw.CreateFormFile("patch", "mine.ips")
	_, _ = patchPart.Write([]byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 1, 'X', 'E', 'O', 'F'})
	_ = mw.WriteField("output_filename", "web-public.gba")
	_ = mw.WriteField("retain", "true")
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/patch-jobs", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(csrfCookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d %s", res.StatusCode, b)
	}
	var job patcher.Job
	if err = json.NewDecoder(res.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for job.Status == "queued" || job.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatal("job timeout")
		}
		time.Sleep(10 * time.Millisecond)
		r, er := http.Get(ts.URL + "/api/patch-jobs/" + job.ID)
		if er != nil {
			t.Fatal(er)
		}
		er = json.NewDecoder(r.Body).Decode(&job)
		r.Body.Close()
		if er != nil {
			t.Fatal(er)
		}
	}
	if job.Status != "completed" || job.Retain || job.ExpiresAt == nil {
		t.Fatalf("public job=%+v", job)
	}
}

// TestPublicPatchJobAcceptsCatalogArtifactID pins the Flow Audit fix: the
// public ROM Patcher must be able to patch a copy of a ROM already in the
// library by artifact_id, not only a freshly uploaded one, and the library
// file itself must survive the job untouched.
func TestPublicPatchJobAcceptsCatalogArtifactID(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	output := filepath.Join(d, "patched")
	os.MkdirAll(filepath.Join(root, "gba"), 0755)
	rom := make([]byte, 0x400)
	copy(rom[0xa0:], "LIBRARY GAME")
	romPath := filepath.Join(root, "gba", "library.gba")
	if err := os.WriteFile(romPath, rom, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.AddArtifactRoot(output)
	games, _ := s.Games(context.Background())
	artifactID := games[0].Artifacts[0].ID

	m := patcher.NewManager(s, patcher.ManagerConfig{LibraryDirectory: root, OutputDirectory: output, MaxROMBytes: 1 << 20, MaxPatchBytes: 1 << 20, Retention: time.Hour, Timeout: time.Second})
	defer m.Shutdown()
	ui := NewCatalog(s, "http://example", "", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), time.Hour)
	ui.SetPatchManager(m)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	page, err := http.Get(ts.URL + "/patches")
	if err != nil {
		t.Fatal(err)
	}
	page.Body.Close()
	var csrfCookie *http.Cookie
	for _, c := range page.Cookies() {
		if c.Name == "romrelay_patch_csrf" {
			csrfCookie = c
		}
	}
	if csrfCookie == nil {
		t.Fatal("public CSRF cookie missing")
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("artifact_id", artifactID)
	patchPart, _ := mw.CreateFormFile("patch", "mine.ips")
	_, _ = patchPart.Write([]byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 1, 'X', 'E', 'O', 'F'})
	_ = mw.WriteField("output_filename", "web-public-from-library.gba")
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/patch-jobs", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", csrfCookie.Value)
	req.AddCookie(csrfCookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d %s", res.StatusCode, b)
	}
	var job patcher.Job
	if err = json.NewDecoder(res.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for job.Status == "queued" || job.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatal("job timeout")
		}
		time.Sleep(10 * time.Millisecond)
		r, er := http.Get(ts.URL + "/api/patch-jobs/" + job.ID)
		if er != nil {
			t.Fatal(er)
		}
		er = json.NewDecoder(r.Body).Decode(&job)
		r.Body.Close()
		if er != nil {
			t.Fatal(er)
		}
	}
	if job.Status != "completed" {
		t.Fatalf("job=%+v", job)
	}
	got, err := os.ReadFile(romPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, rom) {
		t.Fatal("the library artifact must be copied, never modified, by a patch job that picked it")
	}
}

func TestMissingLocalSearchOffersAdminRemoteAction(t *testing.T) {
	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ui := NewCatalog(s, "http://example", "", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), 24*time.Hour)
	ui.SetRemoteManager(remotelib.New(s, remotelib.Options{}))
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/?q=missing+game")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "Search Remotely") || !strings.Contains(string(body), "remote_q=missing&#43;game") {
		t.Fatalf("missing remote action: %s", body)
	}
}

func TestCatalogShowsFallbackArtAndBackgroundHealth(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := filepath.Join(root, "gb", "fallback.gb")
	if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, make([]byte, 0x200), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ui := NewCatalog(s, "http://example", "", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), time.Hour)
	ui.SetBackground("metadata", BackgroundJob{Active: true, Message: "Libretro artwork", Done: 2, Total: 4})
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	// A game with no artwork still needs something in its card, and the page must
	// load the script that reports background work through the top-nav activity
	// icon (base.html, every page). Asserted by role and asset rather than by
	// class name, so restyling the card does not fail this test.
	if !strings.Contains(string(body), `id="activity-widget"`) {
		t.Fatalf("catalog is missing the activity-widget region: %s", body)
	}
	if !strings.Contains(string(body), `src="/assets/app.js"`) {
		t.Fatalf("catalog does not load the script that fills it: %s", body)
	}
	if !strings.Contains(string(body), "<svg") {
		t.Fatalf("a game without cover art must still show placeholder art: %s", body)
	}
	res, err = http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"background_jobs"`) || !strings.Contains(string(body), `"Libretro artwork"`) {
		t.Fatalf("health missing work state: %s", body)
	}
}

func TestAdminShutdownRequiresAuthAndCSRF(t *testing.T) {
	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin := "admin-token-with-24-characters!"
	ui := NewCatalog(s, "http://example", "", "default", admin, filepath.Join(d, "patches"), 24*time.Hour)
	called := make(chan struct{}, 1)
	ui.SetShutdown(func() { called <- struct{}{} })
	req := httptest.NewRequest(http.MethodPost, "/api/admin/shutdown", nil)
	res := httptest.NewRecorder()
	ui.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", res.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/admin/shutdown", nil)
	req.AddCookie(&http.Cookie{Name: "romrelay_admin", Value: admin})
	req.Header.Set("X-CSRF-Token", ui.csrf)
	res = httptest.NewRecorder()
	ui.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("shutdown status=%d body=%s", res.Code, res.Body.String())
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback not called")
	}
}

func TestCatalogRoutesAndSeparateAdminToken(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := filepath.Join(root, "gba", "test.gba")
	os.MkdirAll(filepath.Dir(p), 0755)
	os.WriteFile(p, make([]byte, 0x200), 0600)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	viewer := "viewer-token-with-24-characters"
	admin := "admin-token-with-24-characters!"
	ui := NewCatalog(s, "http://example", viewer, "default", admin, filepath.Join(d, "patches"), 24*time.Hour)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, _ := http.Get(ts.URL + "/")
	if res.StatusCode != 401 {
		t.Fatalf("public status %d", res.StatusCode)
	}
	res.Body.Close()
	// The viewer token authenticates downloads, not people. A browser carrying it
	// is still an anonymous visitor and must sign in: the token travels in QR
	// codes, link hrefs, and browser history, so treating it as a login let anyone
	// holding a copied URL walk past the sign-in page.
	res, _ = http.Get(ts.URL + "/?token=" + viewer)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the viewer token must not open a human page: got %d", res.StatusCode)
	}
	req0, _ := http.NewRequest(http.MethodGet, ts.URL+"/?token="+viewer, nil)
	req0.Header.Set("Accept", "text/html")
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err = browser.Do(req0)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Fatalf("a browser with only a viewer token belongs at the login page: got %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	// The handheld routes are exactly what the token is for: a 3DS scans a QR and
	// fetches the CIA, and it can hold no session to do it with.
	id := games[0].Artifacts[0].ID
	for _, path := range []string{"/d/" + id + "?token=" + viewer, "/healthz?token=" + viewer} {
		res, _ = http.Get(ts.URL + path)
		if res.StatusCode != 200 {
			t.Errorf("%s: the viewer token must still work for handheld downloads: %d", path, res.StatusCode)
		}
		res.Body.Close()
	}
	res, _ = http.Get(ts.URL + "/admin/patches?admin_token=" + viewer)
	if res.StatusCode != 401 {
		t.Fatalf("viewer entered admin: %d", res.StatusCode)
	}
	res.Body.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/patch-jobs", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: "romrelay_admin", Value: admin})
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", res.StatusCode)
	}
	res.Body.Close()
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/admin/patch-jobs", strings.NewReader(""))
	req.Header.Set("X-CSRF-Token", ui.csrf)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("CSRF bypassed admin auth: %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestEncryptedDownloadPromptsAndKeepsMutationAdminOnly(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := filepath.Join(root, "3ds", "cia", "encrypted", "mobile.cia")
	if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("catalog fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	a := games[0].Artifacts[0]
	admin := "admin-token-with-24-characters!"
	ui := NewCatalog(s, "http://example", "", "default", admin, filepath.Join(d, "patches"), 24*time.Hour)
	ui.SetDecryptor(decryptor.New(s, filepath.Join(d, "missing-driver"), root, filepath.Join(d, "work"), time.Second))
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/games/" + games[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "/download/"+a.ID) {
		t.Fatalf("encrypted artifact bypassed choice page: %s", body)
	}
	res, err = http.Get(ts.URL + "/download/" + a.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	text := string(body)
	for _, want := range []string{"Download original encrypted CIA", "mobile-based 3DS emulators", "Decryptor Redux.bat is missing"} {
		if !strings.Contains(text, want) {
			t.Errorf("choice page missing %q: %s", want, text)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/decrypt/"+a.ID, strings.NewReader("csrf_token="+ui.csrf))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("public viewer started decrypt job: %d", res.StatusCode)
	}
	res.Body.Close()
}

// The sign-in page is reachable by definition, so the CSS and JS it asks for must
// be too. With a viewer token configured they were not: the browser was refused
// app.css with a 401 and rendered the sign-in and change-password screens with no
// styling at all, which reads as a broken, undesigned page, not as an auth rule.
func TestTheSignInPageCanLoadItsOwnStylesheetAndScript(t *testing.T) {
	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// A viewer token configured is the case that broke: without one the LAN is
	// treated as trusted and everything is open anyway.
	ui := NewCatalog(s, "http://example", "viewer-token-with-24-characters", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), time.Hour)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	page, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()

	for _, asset := range []string{"/assets/app.css", "/assets/app.js"} {
		if !strings.Contains(string(body), `"`+asset+`"`) {
			t.Fatalf("the sign-in page no longer asks for %s", asset)
		}
		res, err := http.Get(ts.URL + asset)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("an anonymous visitor must be able to load %s, or the sign-in page renders unstyled: got %d", asset, res.StatusCode)
		}
	}
}

func TestSplitFBIBatches(t *testing.T) {
	b := splitFBI([]string{"12345", "67890", "abcde"}, 12)
	if len(b) != 2 || len(b[0]) != 2 || len(b[1]) != 1 {
		t.Fatalf("%v", b)
	}
}

// FBI installs every URL in a QR (it splits the payload on newlines), so a batch
// is meant to be a batch. The budget used to be 180 bytes, under three of these
// URLs, which made "batch QR" one code per ROM.
func fbiTestURLs(n int) []string {
	urls := make([]string, 0, n)
	for i := 0; i < n; i++ {
		urls = append(urls, fmt.Sprintf("http://192.168.1.43:8080/d/%024d?token=VVdAiPCeYTrzve0tZ9LomBYRvCRvV9QK", i))
	}
	return urls
}

func TestOneFBIQRCarriesAWholeBatchOfROMs(t *testing.T) {
	batches := splitFBI(fbiTestURLs(5), fbiQRPayloadMax)
	if len(batches) != 1 {
		t.Fatalf("five ROMs must fit one scannable QR, not %d: %#v", len(batches), batches)
	}
	if len(batches[0]) != 5 {
		t.Fatalf("all five URLs belong in the one batch: %#v", batches[0])
	}
}

// FBI prefixes http:// to any line without a scheme, so sending it is seven
// wasted characters per URL, most of another ROM per code.
func TestFBIPayloadDropsTheSchemeFBIAddsBack(t *testing.T) {
	batches := splitFBI(fbiTestURLs(1), fbiQRPayloadMax)
	got := batches[0][0]
	if strings.HasPrefix(got, "http://") {
		t.Errorf("the scheme FBI re-adds must not be spent on QR capacity: %q", got)
	}
	if !strings.HasPrefix(got, "192.168.1.43:8080/d/") {
		t.Errorf("the rest of the URL must survive intact: %q", got)
	}
	// An https relay keeps its scheme: FBI would otherwise downgrade the request.
	secure := splitFBI([]string{"https://relay.example/d/abc"}, fbiQRPayloadMax)
	if secure[0][0] != "https://relay.example/d/abc" {
		t.Errorf("https must be preserved, or FBI silently fetches over http: %q", secure[0][0])
	}
}

func TestFBIBatchesStayScannableAndWithinFBIsLimits(t *testing.T) {
	batches := splitFBI(fbiTestURLs(400), fbiQRPayloadMax)
	if len(batches) < 2 {
		t.Fatal("400 ROMs cannot be one QR; the payload must split")
	}
	for i, batch := range batches {
		if size := len(strings.Join(batch, "\n")) + 1; size > fbiQRPayloadMax && len(batch) > 1 {
			t.Fatalf("batch %d payload %d exceeds the camera-safe limit", i, size)
		}
		// FBI stores at most INSTALL_URLS_MAX = 128 URLs and drops the rest, so a
		// batch that exceeded it would silently not install everything it showed.
		if len(batch) > fbiURLsPerQR {
			t.Fatalf("batch %d holds %d URLs; FBI installs only %d", i, len(batch), fbiURLsPerQR)
		}
	}
	// Every URL must survive the split: a dropped one is a ROM that never installs.
	total := 0
	for _, batch := range batches {
		total += len(batch)
	}
	if total != 400 {
		t.Fatalf("batching lost URLs: got %d want 400", total)
	}
}

func Test3DSEncryptionUIFilter(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, v := range []string{"encrypted", "unencrypted"} {
		p := filepath.Join(root, "3ds", "cia", v, v+".cia")
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(v), 0600)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), 24*time.Hour)
	fbiURLs, err := ui.ciaURLs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fbiURLs) != 1 {
		t.Fatalf("FBI queue included unencrypted CIA: %v", fbiURLs)
	}
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/?platform=3ds&encryption=unencrypted")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	text := string(body)
	// The fixture names its games "encrypted" and "unencrypted", and one contains
	// the other, so the card title is matched with its tag to stay unambiguous.
	// The 3DS format facet is no longer offered as a sidebar control (Platform is
	// the only filter shown); the ?encryption= query itself still works for links
	// like the FBI batch flow, so filtering behavior is what this checks now.
	if !strings.Contains(text, ">unencrypted</strong>") {
		t.Fatalf("the unencrypted game is missing from its own filter: %s", text)
	}
	if strings.Contains(text, ">encrypted</strong>") {
		t.Fatal("encrypted-only game leaked into filter")
	}
	if !strings.Contains(text, "<svg") || !strings.Contains(text, `aria-hidden="true"`) {
		t.Fatal("encryption state is missing its accessible visual icon")
	}
	// The FBI batch modal is part of the 3DS view: markup that app.js fills once
	// it knows which CIAs are installable. The console-versus-emulator guidance
	// lives with the files themselves, on the game view.
	if !strings.Contains(text, "3DS Batch Installer") || !strings.Contains(text, "data-fbi") {
		t.Fatal("the 3DS view is missing the FBI batch modal")
	}
	gamesForGuide, _ := s.Games(context.Background())
	var mixedID string
	for _, game := range gamesForGuide {
		for _, artifact := range game.Artifacts {
			if artifact.Encryption == "encrypted" {
				mixedID = game.ID
			}
		}
	}
	gameRes, err := http.Get(ts.URL + "/games/" + mixedID)
	if err != nil {
		t.Fatal(err)
	}
	gameBody, _ := io.ReadAll(gameRes.Body)
	gameRes.Body.Close()
	if !strings.Contains(string(gameBody), "Console · FBI install") {
		t.Fatal("the game view does not explain which CIA format installs on a console")
	}
	script := httptest.NewRecorder()
	ui.Handler().ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if !strings.Contains(script.Body.String(), "artifact_ids") {
		t.Fatal("the bundled script no longer queues a selection to FBI")
	}
	games, _ := s.Games(context.Background())
	var ids []string
	for _, game := range games {
		for _, artifact := range game.Artifacts {
			ids = append(ids, artifact.ID)
		}
	}
	batchRes, err := http.Get(ts.URL + "/api/fbi/batches?ids=" + strings.Join(ids, ","))
	if err != nil {
		t.Fatal(err)
	}
	defer batchRes.Body.Close()
	var batch struct {
		Count int      `json:"count"`
		Files int      `json:"files"`
		QRs   []string `json:"qr_codes"`
	}
	if err = json.NewDecoder(batchRes.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	if batch.Files != 1 || batch.Count != 1 || len(batch.QRs) != 1 || !strings.Contains(batch.QRs[0], "ids=") {
		t.Fatalf("selected FBI batch included wrong artifacts: %#v", batch)
	}
}

func TestGameSynopsisExplainsEnabledIGDBMiss(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(root, "gba", "Exact Miss.gba")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, make([]byte, 0x200), 0600)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), 24*time.Hour)
	ui.SetIGDBEnabled(true)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/games/" + games[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	text := string(body)
	if !strings.Contains(text, "No exact synopsis match was returned by IGDB") || strings.Contains(text, "Enable the optional IGDB") {
		t.Fatalf("misleading synopsis state: %s", text)
	}
}

func TestAdminCanEditAndLockGameMetadata(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rom := filepath.Join(root, "gba", "Editable.gba")
	os.MkdirAll(filepath.Dir(rom), 0755)
	os.WriteFile(rom, make([]byte, 0x200), 0600)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	admin := "admin-token-with-24-characters!"
	ui := NewCatalog(s, "http://example", "", "default", admin, filepath.Join(d, "patches"), time.Hour)
	ui.SetMediaDirectory(filepath.Join(d, "artwork"))
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	for name, value := range map[string]string{"title": "Better Title", "developer": "Local Editor", "description": "A manually corrected synopsis.", "genre": "Adventure", "region": "USA"} {
		_ = mw.WriteField(name, value)
	}
	mw.Close()
	endpoint := ts.URL + "/api/admin/games/" + games[0].ID + "/metadata?admin_token=" + admin
	req, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(form.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", ui.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("metadata update=%d: %s", res.StatusCode, body)
	}
	got, _ := s.Game(context.Background(), games[0].ID)
	if got.Title != "Better Title" || got.Description != "A manually corrected synopsis." || got.MetadataSource != "manual" {
		t.Fatalf("metadata not saved: %#v", got)
	}
	// The editor itself is markup on the admin page now, wired by the bundle, so
	// what matters is that the page still offers the form the API above serves.
	// The token in the query is exchanged for a cookie by a redirect, so this needs
	// a jar to arrive authenticated.
	jar, _ := cookiejar.New(nil)
	page, err := (&http.Client{Jar: jar}).Get(ts.URL + "/admin/patches?admin_token=" + url.QueryEscape(admin))
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	for _, required := range []string{`id="metadata-form"`, `name="game_id"`, `name="remove_cover"`, "Save manual metadata"} {
		if !strings.Contains(string(pageBody), required) {
			t.Errorf("admin metadata editor missing %q", required)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 1024: "1.0 KiB", 1024 * 1024: "1.0 MiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d)=%q want %q", n, got, want)
		}
	}
}

func TestWebThemeCopyAndAdminTheme(t *testing.T) {
	for _, tc := range []struct{ theme, want, not string }{{"default", "No matching ROMs. Catalog reconciliation may still be running.", "No matching ears in the crib"}, {"corn", "ROM Relay · Corn Mode", "No matching ROMs. Catalog reconciliation may still be running."}} {
		d := t.TempDir()
		s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
		if err != nil {
			t.Fatal(err)
		}
		ui := NewCatalog(s, "http://example", "", tc.theme, "admin-token-with-24-characters!", filepath.Join(d, "patches"), 24*time.Hour)
		ts := httptest.NewServer(ui.Handler())
		res, err := http.Get(ts.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(body), tc.want) || strings.Contains(string(body), tc.not) {
			t.Errorf("theme %s homepage mismatch: %s", tc.theme, body)
		}
		client := *http.DefaultClient
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		res, err = client.Get(ts.URL + "/admin/patches?admin_token=admin-token-with-24-characters!")
		if err != nil {
			t.Fatal(err)
		}
		cookies := res.Cookies()
		res.Body.Close()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/patches", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(res.Body)
		res.Body.Close()
		if tc.theme == "corn" && !strings.Contains(string(body), "Patch silo") {
			t.Errorf("corn admin not themed: %s", body)
		}
		if tc.theme == "default" && !strings.Contains(string(body), "Patch workspace") {
			t.Errorf("default admin not themed: %s", body)
		}
		for _, required := range []string{"Create a patched ROM", "Edit game metadata and artwork", "Cover / icon artwork", "Catalog game / file"} {
			if !strings.Contains(string(body), required) {
				t.Errorf("admin management UI missing %q", required)
			}
		}
		// Server control moved to the /admin hub, it must not still be on the
		// Patch Workspace page, and it must be on the hub.
		if strings.Contains(string(body), "Server control") {
			t.Error("Patch Workspace still has Server control; it belongs on the /admin hub")
		}
		req, _ = http.NewRequest(http.MethodGet, ts.URL+"/admin", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		hubBody, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(hubBody), "Server control") || !strings.Contains(string(hubBody), `action="/api/admin/shutdown"`) {
			t.Errorf("admin hub missing Server control shutdown form: %s", hubBody)
		}
		ts.Close()
		s.Close()
	}
}

func TestCompletedDownloadExtendsPatchedRetention(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	patched := filepath.Join(d, "patched")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(patched)
	now := time.Unix(2_000_000_000, 0)
	s.SetClock(func() time.Time { return now })
	p := filepath.Join(patched, "gba", "temp.gba")
	os.MkdirAll(filepath.Dir(p), 0755)
	os.WriteFile(p, make([]byte, 0x200), 0600)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	a := games[0].Artifacts[0]
	if err = s.MarkArtifactRetention(context.Background(), p, false, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), 24*time.Hour)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/d/" + a.ID)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	got, err := s.Artifact(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(24 * time.Hour)
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(want) {
		t.Fatalf("expiry=%v want %v", got.ExpiresAt, want)
	}
}

func TestCatalogDownloadLengthAndEvents(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	os.MkdirAll(filepath.Join(root, "gba"), 0755)
	rom := make([]byte, 0x400)
	copy(rom[0xa0:], "TEST DOWNLOAD")
	os.WriteFile(filepath.Join(root, "gba", "download.gba"), rom, 0600)
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	a := games[0].Artifacts[0]
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), 24*time.Hour)
	var events []DownloadEvent
	ui.SetDownloadObserver(func(event DownloadEvent) { events = append(events, event) })
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, err := http.Head(ts.URL + "/d/" + a.ID)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("Content-Length"); got != strconv.FormatInt(a.Size, 10) {
		t.Fatalf("HEAD length=%q", got)
	}
	if len(events) != 0 {
		t.Fatalf("HEAD emitted download events: %#v", events)
	}
	res, err = http.Get(ts.URL + "/d/" + a.ID)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if got := res.Header.Get("Content-Length"); got != strconv.FormatInt(a.Size, 10) {
		t.Fatalf("GET length=%q", got)
	}
	if len(events) != 2 || events[0].Status != "started" || events[1].Status != "completed" || events[1].Written != a.Size {
		t.Fatalf("events=%#v", events)
	}
}

func TestHTTPMetricsObserveRequestsAndBytes(t *testing.T) {
	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), 24*time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	ui.Handler().ServeHTTP(res, req)
	metrics := ui.HTTPMetrics()
	if metrics.Requests != 1 || metrics.ResponseBytes <= 0 || metrics.Active != 0 {
		t.Fatalf("unexpected HTTP metrics: %#v", metrics)
	}
}

func TestFreshBootSetupPageWaitsForReadiness(t *testing.T) {
	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), time.Hour)
	ui.SetReady(false, "Scanning library")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	ui.Handler().ServeHTTP(res, req)
	if body := res.Body.String(); !strings.Contains(body, "Setup in progress") || !strings.Contains(body, "Scanning library") {
		t.Fatalf("setup page missing readiness state: %s", body)
	}
	health := httptest.NewRecorder()
	ui.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(health.Body.String(), `"ready":false`) || !strings.Contains(health.Body.String(), `"status":"setup"`) {
		t.Fatalf("health did not report setup state: %s", health.Body.String())
	}
	ui.SetReady(true, "ready")
	ready := httptest.NewRecorder()
	ui.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(ready.Body.String(), "Setup in progress") || !strings.Contains(ready.Body.String(), "ROM Relay") {
		t.Fatalf("catalog did not replace setup page: %s", ready.Body.String())
	}
}

func TestCatalogHidesPatchedGamesOnlyOnMainLander(t *testing.T) {
	d := t.TempDir()
	root, patched := filepath.Join(d, "platforms"), filepath.Join(d, "patched")
	for path, title := range map[string]string{filepath.Join(root, "gba", "base.gba"): "BASE GAME", filepath.Join(patched, "gba", "hack.gba"): "PATCHED GAME"} {
		rom := make([]byte, 0x200)
		copy(rom[0xa0:], title)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, rom, 0600); err != nil {
			t.Fatal(err)
		}
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
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), time.Hour)
	for path, expected := range map[string][2]string{
		// A temporary patched build (never retained) stays out of the library
		// until its Status chip, a legacy ?platform=patched link, or its exact
		// name asks for it by name; a bare platform filter is not enough.
		"/":                  {"BASE GAME", "PATCHED GAME"},
		"/?platform=gba":     {"BASE GAME", "PATCHED GAME"},
		"/?platform=patched": {"PATCHED GAME", "BASE GAME"},
		"/?q=PATCHED+GAME":   {"PATCHED GAME", ""},
	} {
		want, unwanted := expected[0], expected[1]
		res := httptest.NewRecorder()
		ui.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		body := res.Body.String()
		if !strings.Contains(body, want) || (unwanted != "" && strings.Contains(body, unwanted)) {
			t.Errorf("%s filtering mismatch: %s", path, body)
		}
	}
	// The Patched control is a server-rendered facet now, not something a script
	// injects: it must appear in the HTML of a view where patched builds exist.
	facet := httptest.NewRecorder()
	ui.Handler().ServeHTTP(facet, httptest.NewRequest(http.MethodGet, "/?platform=gba", nil))
	if !strings.Contains(facet.Body.String(), `value="patched"`) || !strings.Contains(facet.Body.String(), "Patched") {
		t.Fatal("the catalog does not offer the Patched status filter")
	}
}

func TestAdminCanQueue3DSToCIAConversion(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	rom := filepath.Join(root, "3ds", "3ds", "game.3ds")
	if err := os.MkdirAll(filepath.Dir(rom), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rom, make([]byte, 0x200), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	id := games[0].Artifacts[0].ID
	const admin = "admin-token-with-24-characters!"
	ui := NewCatalog(s, "http://example", "", "default", admin, filepath.Join(d, "patches"), time.Hour)
	called := make(chan string, 1)
	ui.SetConverter(func(_ context.Context, artifactID string, progress func(string, int64, int64)) error {
		progress("writing", 1, 2)
		called <- artifactID
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/convert/"+id+"?admin_token="+url.QueryEscape(admin), nil)
	req.Header.Set("X-CSRF-Token", ui.csrf)
	res := httptest.NewRecorder()
	ui.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("conversion status=%d body=%s", res.Code, res.Body.String())
	}
	select {
	case got := <-called:
		if got != id {
			t.Fatalf("converted %q want %q", got, id)
		}
	case <-time.After(time.Second):
		t.Fatal("conversion callback was not invoked")
	}
}

// The catalog's stylesheet is an external file, so the policy must allow
// same-origin styles. It did not when the pages were converted: every style used
// to be inline, style-src was 'unsafe-inline' alone, and the browser silently
// refused app.css, the page rendered completely unstyled while every text
// assertion still passed.
func TestTheContentSecurityPolicyAllowsThePagesOwnAssets(t *testing.T) {
	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ui := NewCatalog(s, "http://example", "", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), time.Hour)
	res := httptest.NewRecorder()
	ui.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))

	body := res.Body.String()
	policy := res.Header().Get("Content-Security-Policy")
	for _, directive := range []struct{ name, needle string }{
		{"style-src", `<link rel="stylesheet" href="/assets/app.css">`},
		{"script-src", `<script defer src="/assets/app.js"></script>`},
	} {
		if !strings.Contains(body, directive.needle) {
			t.Fatalf("the page no longer loads its own asset: %s", directive.needle)
		}
		section, _, _ := strings.Cut(policyDirective(policy, directive.name), ";")
		if !strings.Contains(section, "'self'") {
			t.Errorf("%s must allow 'self' or the browser refuses the asset the page just requested: %q", directive.name, policy)
		}
		// Now that no page carries inline <style> or <script>, the exemption that
		// made XSS exploitable must not creep back in with a "temporary" inline bit.
		if strings.Contains(section, "'unsafe-inline'") {
			t.Errorf("%s must not allow 'unsafe-inline': every page loads its assets same-origin now: %q", directive.name, policy)
		}
	}
}

// policyDirective returns one directive's value out of a CSP header.
func policyDirective(policy, name string) string {
	for _, part := range strings.Split(policy, ";") {
		part = strings.TrimSpace(part)
		if value, found := strings.CutPrefix(part, name+" "); found {
			return value
		}
	}
	return ""
}
