package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeINI writes config.ini into a fresh temp directory and returns the dir.
func writeINI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.ini"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadDefaultsFromMissingFile(t *testing.T) {
	// An empty directory (no config.ini, no .env) must yield documented defaults.
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.LibraryScanIntervalHours != 1 || c.MetadataRefreshDays != 30 || c.Server.Port != 8080 ||
		c.Server.ListenAddress != "0.0.0.0" || c.UITheme != "default" || c.ExistingOutput != "skip" ||
		!c.Patching.Enabled || !c.Metadata.Libretro.Enabled || !c.Metadata.Libretro.AutoUpdate ||
		!c.Metadata.Libretro.DownloadThumbnails {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	for _, body := range []string{
		"[server]\nport = 70000\n",
		"[server]\nadvertise_port = 70000\n",
		"[server]\nport = not-a-number\n",
		"[patching]\nenabled = maybe\n",
		"[server]\nlisten_address = not-an-ip\n",
		"[server\nport = 8080\n",   // malformed section header
		"listen_address 0.0.0.0\n", // missing '='
	} {
		if _, err := Load(writeINI(t, body)); err == nil {
			t.Fatalf("accepted invalid config:\n%s", body)
		}
	}
}

func TestRemoteAccessTokenTooShortRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOTE_ACCESS_TOKEN", "short")
	if _, err := Load(dir); err == nil {
		t.Fatal("accepted a remote_access_token shorter than 24 characters")
	}
}

func TestExplicitDisabledScanAndPatching(t *testing.T) {
	dir := writeINI(t, "[library]\nscan_interval_hours = 0\n\n[patching]\nenabled = false\n")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.LibraryScanIntervalHours != 0 {
		t.Fatalf("explicit zero scan interval became %d", c.LibraryScanIntervalHours)
	}
	if c.Patching.Enabled {
		t.Fatal("explicit patching disable was ignored")
	}
}

func TestMetadataConfigAndIGDBCredentials(t *testing.T) {
	body := "[metadata.libretro]\nenabled = false\nauto_update = false\ndownload_thumbnails = false\n\n[metadata.igdb]\nenabled = true\n"
	// IGDB enabled without credentials must degrade (disable + warn), not fail,
	// so a restart-looping container does not crash-loop on a missing secret.
	degraded, err := Load(writeINI(t, body))
	if err != nil {
		t.Fatalf("IGDB without credentials should degrade, not error: %v", err)
	}
	if degraded.Metadata.IGDB.Enabled {
		t.Fatal("IGDB should be disabled when credentials are missing")
	}
	if len(degraded.Warnings) == 0 {
		t.Fatal("expected a warning when IGDB is disabled for missing credentials")
	}
	dir := writeINI(t, body)
	t.Setenv("IGDB_CLIENT_ID", "id")
	t.Setenv("IGDB_CLIENT_SECRET", "secret")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Metadata.Libretro.Enabled || c.Metadata.Libretro.AutoUpdate || c.Metadata.Libretro.DownloadThumbnails ||
		!c.Metadata.IGDB.Enabled || c.Metadata.IGDB.ClientID != "id" || c.Metadata.IGDB.ClientSecret != "secret" {
		t.Fatalf("explicit metadata settings changed: %+v", c.Metadata)
	}
}

func TestRemoteSearchRequiresPinnedHostKey(t *testing.T) {
	t.Setenv("REMOTE_SEARCH_PASSWORD", "secret")
	body := "[remote_search]\nenabled = true\nhost = sftp.example\nusername = user\n"
	if _, err := Load(writeINI(t, body)); err == nil {
		t.Fatal("remote search accepted without pinned host key")
	}
	dir := writeINI(t, body+"host_key_sha256 = SHA256:fixture\n")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.RemoteSearch.Enabled || c.RemoteSearch.Port != 22 || c.RemoteSearch.MaxResults != 200 ||
		c.RemoteSearch.Password != "secret" {
		t.Fatalf("bad remote defaults: %+v", c.RemoteSearch)
	}
}

func TestRemoteSearchPlatformDirectories(t *testing.T) {
	t.Setenv("REMOTE_SEARCH_PASSWORD", "secret")
	body := "[remote_search]\nenabled = true\nhost = sftp.example\nusername = user\nhost_key_sha256 = SHA256:fixture\n\n[remote_search.platforms]\ngba = games/gba\nnds = games/nds\n"
	c, err := Load(writeINI(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if c.RemoteSearch.PlatformDirectories["gba"] != "games/gba" || c.RemoteSearch.PlatformDirectories["nds"] != "games/nds" {
		t.Fatalf("platform directories not read: %+v", c.RemoteSearch.PlatformDirectories)
	}
	if _, ok := c.RemoteSearch.PlatformDirectories["3ds"]; ok {
		t.Fatal("an explicit platform map must not inherit defaults")
	}
}

func TestThemeIsStrictAndReadAtLoad(t *testing.T) {
	for _, theme := range []string{"default", "corn"} {
		c, err := Load(writeINI(t, "[library]\nui_theme = "+theme+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if c.UITheme != theme {
			t.Fatalf("got %q", c.UITheme)
		}
	}
	if _, err := Load(writeINI(t, "[library]\nui_theme = maize\n")); err == nil {
		t.Fatal("accepted unknown theme")
	}
}

func TestRejectsFormerBoot9ConfigField(t *testing.T) {
	if _, err := Load(writeINI(t, "[library]\nboot9_path = private.bin\n")); err == nil {
		t.Fatal("accepted a configurable boot9 path")
	}
}

func TestShippedExampleLoads(t *testing.T) {
	// The example shipped to users must parse and validate as-is.
	body, err := os.ReadFile(filepath.Join("..", "..", "config.example.ini"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.ini"), body, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("shipped config.example.ini failed to load: %v", err)
	}
	if c.Server.Port != 8080 || !c.Patching.Enabled {
		t.Fatalf("unexpected values from example: %+v", c)
	}
}

func TestEnvironmentWinsOverEnvFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("IGDB_CLIENT_ID=from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.ini"), []byte("[metadata.igdb]\nenabled = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IGDB_CLIENT_ID", "from-env")
	t.Setenv("IGDB_CLIENT_SECRET", "secret")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Metadata.IGDB.ClientID != "from-env" {
		t.Fatalf("process environment must win over .env file, got %q", c.Metadata.IGDB.ClientID)
	}
}

func TestPokeAPIProviderIsOffUnlessEnabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.ini"), []byte(`
[library]
directory = platforms

[metadata.pokeapi]
enabled = true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Metadata.PokeAPI.Enabled {
		t.Fatal("[metadata.pokeapi] enabled=true must switch the provider on")
	}

	// The default must be off: enrichment reaches out to the internet, and a
	// relay on a closed LAN should not try unless asked.
	if err := os.WriteFile(filepath.Join(dir, "config.ini"), []byte("[library]\ndirectory = platforms\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metadata.PokeAPI.Enabled {
		t.Fatal("the PokeAPI provider must default to off")
	}
}
