package web

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dop251/goja"
	"io"
	"mime/multipart"
	"net/http"
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

func TestEmbeddedFrontendScriptsCompile(t *testing.T) {
	adminRendered := strings.Replace(adminJSScript, `badge=(text,kind='')=>'<span class="badge '+kind+'">'+esc(text)+'</span>'`, adminBadgeRenderer, 1)
	for name, script := range map[string]string{"admin": adminRendered, "catalog": catalogJSScript} {
		if _, err := goja.Compile(name+".js", script, false); err != nil {
			t.Errorf("%s JavaScript does not compile: %v", name, err)
		}
	}
}

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
	if !strings.Contains(string(html), "Base ROM") || !strings.Contains(string(html), "Temporary workshop") || !strings.Contains(string(html), "Cover / icon artwork") || !strings.Contains(string(html), "Description") {
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
	if !strings.Contains(string(body), "cover-placeholder") || !strings.Contains(string(body), "/assets/catalog.js") {
		t.Fatalf("catalog missing fallback/status UI: %s", body)
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
	res, _ = http.Get(ts.URL + "/?token=" + viewer)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "ROM Relay") {
		t.Fatalf("catalog %d %s", res.StatusCode, body)
	}
	id := games[0].Artifacts[0].ID
	for _, path := range []string{"/d/" + id + "?token=" + viewer, "/qr/" + id + ".png?token=" + viewer, "/api/games?token=" + viewer} {
		res, _ = http.Get(ts.URL + path)
		if res.StatusCode != 200 {
			t.Errorf("%s: %d", path, res.StatusCode)
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
func TestSplitFBIBatches(t *testing.T) {
	b := splitFBI([]string{"12345", "67890", "abcde"}, 12)
	if len(b) != 2 || len(b[0]) != 2 || len(b[1]) != 1 {
		t.Fatalf("%v", b)
	}
}

func TestSplitFBICameraSafeBatches(t *testing.T) {
	urls := []string{
		"http://192.168.1.43:8080/d/111111111111111111111111",
		"http://192.168.1.43:8080/d/222222222222222222222222",
		"http://192.168.1.43:8080/d/333333333333333333333333",
		"http://192.168.1.43:8080/d/444444444444444444444444",
		"http://192.168.1.43:8080/d/555555555555555555555555",
	}
	batches := splitFBI(urls, fbiQRPayloadMax)
	if len(batches) < 2 {
		t.Fatalf("dense FBI payload was not split: %#v", batches)
	}
	for _, batch := range batches {
		if size := len(strings.Join(batch, "\n")) + 1; size > fbiQRPayloadMax && len(batch) > 1 {
			t.Fatalf("batch payload %d exceeds camera-safe limit", size)
		}
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
	if !strings.Contains(text, "Unencrypted CIA") || !strings.Contains(text, "<h2>unencrypted</h2>") {
		t.Fatalf("unexpected filtered page: %s", text)
	}
	if !strings.Contains(text, "<svg") || !strings.Contains(text, "aria-hidden=true") {
		t.Fatal("encryption state is missing its accessible visual icon")
	}
	if strings.Contains(text, "<h2>encrypted</h2>") {
		t.Fatal("encrypted-only game leaked into filter")
	}
	js, err := http.Get(ts.URL + "/assets/catalog.js")
	if err != nil {
		t.Fatal(err)
	}
	jsBody, _ := io.ReadAll(js.Body)
	js.Body.Close()
	if !strings.Contains(string(jsBody), "Batch install selected CIAs") || !strings.Contains(string(jsBody), "Unencrypted / decrypted CIA") || !strings.Contains(string(jsBody), "artifact_ids") {
		t.Fatal("catalog script is missing FBI selection or encryption guidance")
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
	js, err := http.Get(ts.URL + "/assets/admin.js?admin_token=" + admin)
	if err != nil {
		t.Fatal(err)
	}
	jsBody, _ := io.ReadAll(js.Body)
	js.Body.Close()
	if !strings.Contains(string(jsBody), "metadata-form") || !strings.Contains(string(jsBody), "manual override") || !strings.Contains(string(jsBody), "Patched") || !strings.Contains(string(jsBody), `<svg class="icon"`) {
		t.Fatal("admin metadata editor missing from UI script")
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
	for _, tc := range []struct{ theme, want, not string }{{"default", "Your ROMs. Your network. Your relay.", "Corn Mode"}, {"corn", "ROM Relay · Corn Mode", "Your ROMs. Your network. Your relay."}} {
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
		for _, required := range []string{"Create a patched ROM", "Edit game metadata and artwork", "Cover / icon artwork", "Catalog game / file", "Server control"} {
			if !strings.Contains(string(body), required) {
				t.Errorf("admin management UI missing %q", required)
			}
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
		"/":                  {"BASE GAME", "PATCHED GAME"},
		"/?platform=gba":     {"PATCHED GAME", ""},
		"/?platform=patched": {"PATCHED GAME", "BASE GAME"},
	} {
		want, unwanted := expected[0], expected[1]
		res := httptest.NewRecorder()
		ui.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		body := res.Body.String()
		if !strings.Contains(body, want) || (unwanted != "" && strings.Contains(body, unwanted)) {
			t.Errorf("%s filtering mismatch: %s", path, body)
		}
	}
	js := httptest.NewRecorder()
	ui.Handler().ServeHTTP(js, httptest.NewRequest(http.MethodGet, "/assets/catalog.js", nil))
	if !strings.Contains(js.Body.String(), "platform=patched") || !strings.Contains(js.Body.String(), "Patched") {
		t.Fatal("catalog script does not expose the Patched platform")
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
