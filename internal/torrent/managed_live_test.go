package torrent

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLaunchManaged starts a real qBittorrent through Launch: it must come up
// on the generated port with the generated password, then stop cleanly.
// Set QBT_TEST_BINARY to a qbittorrent-nox path to run it.
func TestLaunchManaged(t *testing.T) {
	bin := os.Getenv("QBT_TEST_BINARY")
	if bin == "" {
		t.Skip("QBT_TEST_BINARY not set")
	}
	m, err := Launch(context.Background(), bin, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Client.Sweep(context.Background()); err != nil {
		t.Fatalf("managed instance must accept the generated credentials: %v", err)
	}
	if NewClient(m.URL, "romrelay", "wrong").login(context.Background()) == nil {
		t.Fatal("a wrong password must be refused")
	}
	start := time.Now()
	m.Stop()
	if time.Since(start) > 14*time.Second {
		t.Fatal("qBittorrent ignored the clean shutdown request and had to be killed")
	}
}
