package randomizer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"romrelay/internal/catalog"
)

// fakeSidecar stands in for sidecar/randomizer/Shim.java: it checks the wire
// contract (extension header, settings header, raw ROM body) and returns a
// byte-flipped "randomized" ROM, so the manager's staging/publish pipeline is
// exercised without a JVM.
func fakeSidecar(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/randomize" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Randomizer-Extension") == "" || r.Header.Get("X-Randomizer-Settings") == "" {
			http.Error(w, `{"error":"missing headers"}`, 400)
			return
		}
		rom, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		out := append([]byte(nil), rom...)
		for i := range out {
			out[i] ^= 0xFF
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(out)
	}))
}

func TestManagerRandomizesPublishesRetainsAndDeletes(t *testing.T) {
	sidecar := fakeSidecar(t)
	defer sidecar.Close()

	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	output := filepath.Join(d, "randomized")
	artwork := filepath.Join(d, "artwork")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(output)

	source := filepath.Join(root, "gba", "base.gba")
	os.MkdirAll(filepath.Dir(source), 0755)
	original := make([]byte, 0x200)
	copy(original[0xa0:], []byte("LEGAL BASE"))
	os.WriteFile(source, original, 0600)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	artifact := games[0].Artifacts[0]

	var progressMu sync.Mutex
	var phases []string
	m := NewManager(s, NewClient(sidecar.URL), ManagerConfig{OutputDirectory: output, ArtworkDirectory: artwork, Retention: 24 * time.Hour, MaxROMBytes: 1 << 20, Timeout: 5 * time.Second, Progress: func(_ Job, message string, _, _ int64) {
		progressMu.Lock()
		phases = append(phases, message)
		progressMu.Unlock()
	}})

	job, err := m.Create(context.Background(), CreateOptions{ArtifactID: artifact.ID, OutputName: "random.gba", SettingsJSON: `{"trainers_mod":"random"}`, Title: "My Randomized Game"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for job.Status == "queued" || job.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatal("job timed out")
		}
		time.Sleep(10 * time.Millisecond)
		job, err = m.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if job.Status != "completed" || job.ResultArtifactID == "" {
		t.Fatalf("job=%+v", job)
	}

	after, err := os.ReadFile(source)
	if err != nil || string(after[0xa0:0xaa]) != "LEGAL BASE" {
		t.Fatal("source ROM was touched")
	}
	published, err := os.ReadFile(filepath.Join(output, "gba", "random.gba"))
	if err != nil {
		t.Fatal(err)
	}
	if published[0]^0xFF != original[0] {
		t.Fatalf("randomized output does not match fake sidecar transform")
	}

	a, err := s.Artifact(context.Background(), job.ResultArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Patched || a.ExpiresAt == nil || a.Retain {
		t.Fatalf("retention not recorded: %+v", a)
	}
	game, err := s.Game(context.Background(), a.GameID)
	if err != nil || game.Title != "My Randomized Game" {
		t.Fatalf("manual metadata not published: %+v %v", game, err)
	}

	kept, err := m.Retain(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !kept.Retain || kept.ExpiresAt != nil {
		t.Fatalf("retain did not clear expiry: %+v", kept)
	}

	if err = m.Delete(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(output, "gba", "random.gba")); !os.IsNotExist(err) {
		t.Fatalf("published output remains: %v", err)
	}
}

func TestCreateRejectsUnsupportedPlatformAndMissingSettings(t *testing.T) {
	sidecar := fakeSidecar(t)
	defer sidecar.Close()
	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	output := filepath.Join(d, "randomized")
	s.AddArtifactRoot(output)
	m := NewManager(s, NewClient(sidecar.URL), ManagerConfig{OutputDirectory: output, Retention: time.Hour, MaxROMBytes: 1 << 20, Timeout: time.Second})

	rom := make([]byte, 0x300)
	if _, err = m.Create(context.Background(), CreateOptions{SourceName: "game.3ds", SourceUpload: rom, OutputName: "out.3ds", SettingsJSON: "{}"}); err == nil {
		t.Fatal("expected 3DS upload to be rejected")
	}
	if _, err = m.Create(context.Background(), CreateOptions{SourceName: "game.gba", SourceUpload: rom, OutputName: "out.gba"}); err == nil {
		t.Fatal("expected missing settings to be rejected")
	}
}
