package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"romrelay/internal/catalog"
	patcher "romrelay/internal/patch"
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

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("artifact_id", artifactID)
	patchPart, _ := mw.CreateFormFile("patch", "mine.ips")
	_, _ = patchPart.Write([]byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 1, 'X', 'E', 'O', 'F'})
	_ = mw.WriteField("output_filename", "web-public-from-library.gba")
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/patch-jobs", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", ui.csrf)
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

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 1024: "1.0 KiB", 1024 * 1024: "1.0 MiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d)=%q want %q", n, got, want)
		}
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
