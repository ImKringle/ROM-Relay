package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	body := fmt.Sprintf(`{"input_directory":%q,"serve_directory":%q}`, d, d)
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 8080 || c.AdvertisePort != 8080 || c.ListenAddress != "0.0.0.0" || c.ExistingOutput != "skip" {
		t.Fatalf("defaults not applied: %+v", c)
	}
}
func TestLoadRejectsInvalid(t *testing.T) {
	d := t.TempDir()
	for _, body := range []string{fmt.Sprintf(`{"input_directory":%q,"serve_directory":%q,"port":70000}`, d, d), fmt.Sprintf(`{"input_directory":%q,"serve_directory":%q,"advertise_port":70000}`, d, d), fmt.Sprintf(`{"input_directory":%q,"serve_directory":%q,"remote_access_token":"short"}`, d, d), fmt.Sprintf(`{"input_directory":%q,"serve_directory":%q,"advertise_ip":"::1"}`, d, d), fmt.Sprintf(`{"input_directory":%q,"serve_directory":%q} {}`, d, d)} {
		p := filepath.Join(d, "c.json")
		os.WriteFile(p, []byte(body), 0600)
		if _, err := Load(p); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestLoadV2DefaultsAndExplicitDisabledScan(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	if err := os.WriteFile(p, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Legacy || c.LibraryScanIntervalHours != 1 || c.MetadataRefreshDays != 30 || c.Server.Port != 8080 || c.UITheme != "default" || !c.Patching.Enabled || !c.Metadata.Libretro.Enabled || !c.Metadata.Libretro.AutoUpdate || !c.Metadata.Libretro.DownloadThumbnails {
		t.Fatalf("bad v2 defaults: %+v", c)
	}
	if err := os.WriteFile(p, []byte(`{"library_scan_interval_hours":0,"patching":{"enabled":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.LibraryScanIntervalHours != 0 {
		t.Fatalf("explicit zero became %d", c.LibraryScanIntervalHours)
	}
	if c.Patching.Enabled {
		t.Fatal("explicit patching disable was ignored")
	}
}

func TestMetadataConfigAndIGDBCredentials(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	os.WriteFile(p, []byte(`{"metadata":{"libretro":{"enabled":false,"auto_update":false,"download_thumbnails":false},"igdb":{"enabled":true}}}`), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("enabled IGDB accepted without credentials")
	}
	os.WriteFile(p, []byte(`{"metadata":{"libretro":{"enabled":false,"auto_update":false,"download_thumbnails":false},"igdb":{"enabled":true,"client_id":"id","client_secret":"secret"}}}`), 0600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Metadata.Libretro.Enabled || c.Metadata.Libretro.AutoUpdate || c.Metadata.Libretro.DownloadThumbnails || !c.Metadata.IGDB.Enabled {
		t.Fatalf("explicit metadata settings changed: %+v", c.Metadata)
	}
}

func TestRemoteSearchRequiresPinnedHostKey(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	os.WriteFile(p, []byte(`{"remote_search":{"enabled":true,"host":"sftp.example","username":"user","password":"secret"}}`), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("remote search accepted without pinned host key")
	}
	os.WriteFile(p, []byte(`{"remote_search":{"enabled":true,"host":"sftp.example","username":"user","password":"secret","host_key_sha256":"SHA256:fixture"}}`), 0600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.RemoteSearch.Enabled || c.RemoteSearch.Port != 22 || c.RemoteSearch.MaxResults != 200 {
		t.Fatalf("bad remote defaults: %+v", c.RemoteSearch)
	}
}

func TestThemeIsStrictAndReadAtLoad(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	for _, theme := range []string{"default", "corn"} {
		if err := os.WriteFile(p, []byte(fmt.Sprintf(`{"ui_theme":%q}`, theme)), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if c.UITheme != theme {
			t.Fatalf("got %q", c.UITheme)
		}
	}
	os.WriteFile(p, []byte(`{"ui_theme":"maize"}`), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("accepted unknown theme")
	}
}

func TestV2RejectsFormerBoot9ConfigField(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	os.WriteFile(p, []byte(`{"boot9_path":"private.bin"}`), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("v2 accepted configurable boot9 path")
	}
}
