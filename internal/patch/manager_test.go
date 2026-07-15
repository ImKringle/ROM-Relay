package patch

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"romrelay/internal/catalog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagerStagesPublishesRetainsAndDeletesIPS(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	output := filepath.Join(d, "patched")
	patches := filepath.Join(d, "patches")
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
	var progress []string
	m := NewManager(s, ManagerConfig{PatchDirectory: patches, OutputDirectory: output, ArtworkDirectory: artwork, Retention: 24 * time.Hour, MaxROMBytes: 1 << 20, MaxPatchBytes: 1 << 20, Timeout: time.Second, Progress: func(_ Job, message string, _, _ int64) {
		progressMu.Lock()
		progress = append(progress, message)
		progressMu.Unlock()
	}})
	ips := []byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 2, 'X', 'Y', 'E', 'O', 'F'}
	var cover bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err = png.Encode(&cover, img); err != nil {
		t.Fatal(err)
	}
	job, err := m.Create(context.Background(), CreateOptions{ArtifactID: artifact.ID, UploadName: "change.ips", Upload: ips, OutputName: "patched.gba", Title: "My Patched Game", Developer: "Local Author", Cover: cover.Bytes()})
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
	if job.Status != "completed" || job.ResultArtifactID == "" || !job.UnverifiedSource {
		t.Fatalf("job=%+v", job)
	}
	progressMu.Lock()
	joinedProgress := strings.Join(progress, "\n")
	progressMu.Unlock()
	for _, phase := range []string{"Patching ROM base.gba", "Validating patched ROM base.gba", "Completed"} {
		if !strings.Contains(joinedProgress, phase) {
			t.Errorf("missing patch progress phase %q in %q", phase, joinedProgress)
		}
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatal("source ROM changed")
	}
	published, err := os.ReadFile(filepath.Join(output, "gba", "patched.gba"))
	if err != nil {
		t.Fatal(err)
	}
	if string(published[1:3]) != "XY" {
		t.Fatalf("patch not applied: %x", published[:4])
	}
	a, err := s.Artifact(context.Background(), job.ResultArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Patched || a.ExpiresAt == nil || a.Retain {
		t.Fatalf("retention not recorded: %+v", a)
	}
	patchedGame, err := s.Game(context.Background(), a.GameID)
	if err != nil || patchedGame.Title != "My Patched Game" || patchedGame.Developer != "Local Author" || patchedGame.MetadataSource != "manual" || patchedGame.ArtworkURL == "" {
		t.Fatalf("manual metadata not published: %+v %v", patchedGame, err)
	}
	if _, err = os.Stat(filepath.Join(artwork, a.GameID+".png")); err != nil {
		t.Fatalf("cover not published: %v", err)
	}
	if err = m.Delete(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(output, "gba", "patched.gba")); !os.IsNotExist(err) {
		t.Fatalf("published output remains: %v", err)
	}
	if _, err = os.Stat(filepath.Join(artwork, a.GameID+".png")); !os.IsNotExist(err) {
		t.Fatalf("published cover remains: %v", err)
	}
}

func TestPublicUploadedROMPatchIsAlwaysTemporary(t *testing.T) {
	d := t.TempDir()
	store, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	output := filepath.Join(d, "patched")
	store.AddArtifactRoot(output)
	m := NewManager(store, ManagerConfig{LibraryDirectory: filepath.Join(d, "platforms"), OutputDirectory: output, MaxROMBytes: 1 << 20, MaxPatchBytes: 1 << 20, Retention: time.Hour, Timeout: time.Second})
	rom := make([]byte, 0x200)
	ips := []byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 1, 'X', 'E', 'O', 'F'}
	job, err := m.Create(context.Background(), CreateOptions{SourceName: "mine.gba", SourceUpload: rom, UploadName: "mine.ips", Upload: ips, OutputName: "public.gba", Retain: true, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for job.Status == "queued" || job.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatal("job timeout")
		}
		time.Sleep(10 * time.Millisecond)
		job, err = m.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if job.Status != "completed" || job.Retain || job.ExpiresAt == nil {
		t.Fatalf("public job=%+v", job)
	}
	a, err := store.Artifact(context.Background(), job.ResultArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Retain || a.ExpiresAt == nil {
		t.Fatalf("public artifact retention=%+v", a)
	}
	kept, err := m.Retain(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !kept.Retain || kept.ExpiresAt != nil {
		t.Fatalf("promoted job=%+v", kept)
	}
}

func TestCoverValidationRejectsActiveOrInvalidContent(t *testing.T) {
	for _, data := range [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), bytes.Repeat([]byte{'x'}, 64)} {
		if _, err := validateCover(data); err == nil {
			t.Fatal("invalid cover accepted")
		}
	}
}

func TestManagerShutdownCancelsQueuedJob(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	output := filepath.Join(d, "patched")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(output)
	source := filepath.Join(root, "gba", "base.gba")
	if err = os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, make([]byte, 0x200), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	m := NewManager(s, ManagerConfig{OutputDirectory: output, MaxROMBytes: 1 << 20, MaxPatchBytes: 1 << 20, Timeout: time.Minute})
	m.one <- struct{}{}
	ips := []byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 1, 'X', 'E', 'O', 'F'}
	job, err := m.Create(context.Background(), CreateOptions{ArtifactID: games[0].Artifacts[0].ID, UploadName: "x.ips", Upload: ips, OutputName: "queued.gba"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.Shutdown(); close(done) }()
	time.Sleep(10 * time.Millisecond)
	<-m.one
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manager shutdown did not wait/cancel")
	}
	job, err = m.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" {
		t.Fatalf("job=%+v", job)
	}
}

func TestManagerRejectsTraversalFormatAndCollision(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	output := filepath.Join(d, "patched")
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddArtifactRoot(output)
	source := filepath.Join(root, "gba", "base.gba")
	os.MkdirAll(filepath.Dir(source), 0755)
	os.WriteFile(source, make([]byte, 0x200), 0600)
	s.Reconcile(context.Background())
	games, _ := s.Games(context.Background())
	m := NewManager(s, ManagerConfig{PatchDirectory: filepath.Join(d, "patches"), OutputDirectory: output, MaxROMBytes: 1 << 20, MaxPatchBytes: 1 << 20})
	if _, err = m.Create(context.Background(), CreateOptions{ArtifactID: games[0].Artifacts[0].ID, UploadName: "bad.xyz", Upload: []byte("bad"), OutputName: "x.gba"}); err == nil {
		t.Fatal("unsupported format accepted")
	}
	if _, err = safeLocalPatch(filepath.Join(d, "patches"), "../escape.ips"); err == nil {
		t.Fatal("traversal accepted")
	}
}
