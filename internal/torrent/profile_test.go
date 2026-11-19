package torrent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWriteProfileCoversBothLayouts: qBittorrent <= 5.2 reads Web UI settings
// from [Preferences] WebUI\Key, 5.3+ from [WebUI] Key. Both must be present.
func TestWriteProfileCoversBothLayouts(t *testing.T) {
	dir := t.TempDir()
	if err := writeProfile(dir, 4321, "romrelay", "pw"); err != nil {
		t.Fatal(err)
	}
	name := "qBittorrent.conf"
	if runtime.GOOS == "windows" {
		name = "qBittorrent.ini"
	}
	b, err := os.ReadFile(filepath.Join(dir, "qBittorrent", "config", name))
	if err != nil {
		t.Fatal(err)
	}
	conf := string(b)
	for _, want := range []string{
		"[LegalNotice]\nAccepted=true",
		"[Preferences]\nWebUI\\Enabled=true\nWebUI\\Address=127.0.0.1\nWebUI\\Port=4321\nWebUI\\Username=romrelay\nWebUI\\Password_PBKDF2=\"@ByteArray(",
		"[WebUI]\nEnabled=true\nAddress=127.0.0.1\nPort=4321\nUsername=romrelay\nPassword_PBKDF2=\"@ByteArray(",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("profile missing %q:\n%s", want, conf)
		}
	}
}
