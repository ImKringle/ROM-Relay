package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"romrelay/internal/catalog"
)

func TestFTPRoutesRejectNonPrivateHost(t *testing.T) {
	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), time.Hour)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	// 8.8.8.8 is public and 127.0.0.1 is loopback, not RFC1918 private, both
	// must be refused so ROM Relay never becomes an open relay toward an
	// arbitrary host with attacker-supplied FTP credentials.
	for _, host := range []string{"8.8.8.8", "127.0.0.1"} {
		for _, path := range []string{"/api/ftp/browse", "/api/ftp/send"} {
			res, err := http.PostForm(ts.URL+path, url.Values{"host": {host}, "port": {"5000"}})
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s host=%s: must be rejected, got %d", path, host, res.StatusCode)
			}
		}
	}
}

// TestArtifactsSelectedResolvesLocalPaths is the check for the one piece of
// logic ftp.go adds beyond the existing FBI selection helpers: turning
// caller-supplied artifact IDs back into the real on-disk files ftpxfer.Send
// will open, for any platform/extension (FTP push isn't CIA-only like the
// FBI batch installer).
func TestArtifactsSelectedResolvesLocalPaths(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "platforms")
	os.MkdirAll(filepath.Join(root, "nds"), 0755)
	os.MkdirAll(filepath.Join(root, "gba"), 0755)
	rom := make([]byte, 0x200)
	os.WriteFile(filepath.Join(root, "nds", "game.nds"), rom, 0600)
	os.WriteFile(filepath.Join(root, "gba", "other.gba"), rom, 0600)
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(context.Background())
	var ndsID string
	for _, g := range games {
		for _, a := range g.Artifacts {
			if a.Name == "game.nds" {
				ndsID = a.ID
			}
		}
	}
	if ndsID == "" {
		t.Fatal("seeded NDS artifact not found in catalog")
	}
	ui := NewCatalog(s, "http://example", "", "default", "", filepath.Join(d, "patches"), time.Hour)

	all, err := ui.artifactsSelected(context.Background(), nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("expected both artifacts with no filter, got %d, err=%v", len(all), err)
	}

	only, err := ui.artifactsSelected(context.Background(), selectedArtifactIDs(ndsID))
	if err != nil || len(only) != 1 {
		t.Fatalf("expected exactly the selected artifact, got %d, err=%v", len(only), err)
	}
	if only[0].Name != "game.nds" || only[0].Path == "" {
		t.Fatalf("resolved artifact wrong or missing path: %+v", only[0])
	}
	if _, err := os.Stat(only[0].Path); err != nil {
		t.Fatalf("resolved path does not exist on disk: %v", err)
	}
}
