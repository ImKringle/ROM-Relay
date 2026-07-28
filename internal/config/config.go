// Package config loads ROM Relay's configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Server struct {
	ListenAddress     string `json:"listen_address"`
	Port              int    `json:"port"`
	AdvertiseIP       string `json:"advertise_ip"`
	AdvertisePort     int    `json:"advertise_port"`
	PublicBaseURL     string `json:"public_base_url"`
	RemoteAccessToken string `json:"remote_access_token"`
}

type LibretroMetadata struct {
	Enabled            bool   `json:"enabled"`
	DatabaseDirectory  string `json:"database_directory"`
	ThumbnailDirectory string `json:"thumbnail_directory"`
	AutoUpdate         bool   `json:"auto_update"`
	DownloadThumbnails bool   `json:"download_thumbnails"`
}

type IGDBMetadata struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// PokeAPIMetadata gates the Pokédex reference fill. It is off by default, and
// the Dex works without it. When DataDirectory points at a PokeAPI/api-data
// checkout (the third_party/api-data submodule), the fill reads from disk and
// never touches the network; otherwise it falls back to the live API.
type PokeAPIMetadata struct {
	Enabled       bool   `json:"enabled"`
	DataDirectory string `json:"data_directory"`
}

type Metadata struct {
	Libretro LibretroMetadata `json:"libretro"`
	IGDB     IGDBMetadata     `json:"igdb"`
	PokeAPI  PokeAPIMetadata  `json:"pokeapi"`
}

type RemoteSearch struct {
	Enabled             bool              `json:"enabled"`
	Host                string            `json:"host"`
	Port                int               `json:"port"`
	Username            string            `json:"username"`
	Password            string            `json:"password"`
	RootDirectory       string            `json:"root_directory"`
	HostKeySHA256       string            `json:"host_key_sha256"`
	MaxResults          int               `json:"max_results"`
	MaxFileSizeMB       int               `json:"max_file_size_mb"`
	PlatformDirectories map[string]string `json:"platform_directories"`
}

type Patching struct {
	Enabled          bool   `json:"enabled"`
	PatchDirectory   string `json:"patch_directory"`
	OutputDirectory  string `json:"output_directory"`
	AdminAccessToken string `json:"admin_access_token"`
	RetentionHours   int    `json:"retention_hours"`
	MaxROMSizeMB     int    `json:"max_rom_size_mb"`
	MaxPatchSizeMB   int    `json:"max_patch_size_mb"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
}

// Randomizer has no Enabled flag: unlike Patching (self-contained), it's
// useless without RandomizerSidecarURL, so that URL's presence is the on/off
// switch, the same convention PokemonSidecarURL/NDecryptSidecarURL already use.
type Randomizer struct {
	OutputDirectory string `json:"output_directory"`
	RetentionHours  int    `json:"retention_hours"`
	MaxROMSizeMB    int    `json:"max_rom_size_mb"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}

type Config struct {
	LibraryDirectory         string       `json:"library_directory"`
	DatabasePath             string       `json:"database_path"`
	DatabaseURL              string       `json:"-"`
	LibraryScanIntervalHours int          `json:"library_scan_interval_hours"`
	MetadataRefreshDays      int          `json:"metadata_refresh_days"`
	ExistingOutput           string       `json:"existing_output"`
	UITheme                  string       `json:"ui_theme"`
	Server                   Server       `json:"server"`
	Metadata                 Metadata     `json:"metadata"`
	RemoteSearch             RemoteSearch `json:"remote_search"`
	Patching                 Patching     `json:"patching"`
	Randomizer               Randomizer   `json:"randomizer"`

	// PokemonSidecarURL is the base URL of the PKHeX save-parsing sidecar. When
	// empty, Pokémon save ingestion and the Living Dex are disabled.
	PokemonSidecarURL string `json:"-"`

	// NDecryptSidecarURL is the base URL of the NDecrypt sidecar. When empty,
	// the .3ds/.cci/.nds encrypt/decrypt upload flow is disabled.
	NDecryptSidecarURL string `json:"-"`

	// RandomizerSidecarURL is the base URL of the randomizer sidecar. When
	// empty, the ROM Randomizer tool is disabled.
	RandomizerSidecarURL string `json:"-"`

	// SaveDirectory holds users' uploaded game saves. Only metadata goes in the
	// database, so this directory is what a backup of user saves must cover.
	SaveDirectory string `json:"save_directory"`

	// AdminPassword, when set, is applied to the built-in admin account at
	// startup. Blank leaves an existing password in place, or generates one on
	// first run (printed once to the operator).
	AdminPassword string `json:"-"`

	// Warnings collects non-fatal configuration issues surfaced at startup, such
	// as an enabled provider that is missing its credentials and was disabled.
	Warnings []string `json:"-"`
}

// Load reads ROM Relay's configuration from `config.ini` (non-secret settings)
// and secrets from the environment (optionally seeded by a `.env` file), both
// resolved relative to dir. Both files are optional: an absent config.ini yields
// documented defaults, and absent secrets are treated as empty. Relative paths
// in config.ini are resolved against dir.
func Load(dir string) (Config, error) {
	ini := &iniFile{sections: map[string]map[string]string{"": {}}}
	if b, err := os.ReadFile(filepath.Join(dir, "config.ini")); err == nil {
		ini, err = parseINI(bytes.NewReader(b))
		if err != nil {
			return Config{}, err
		}
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("read config.ini: %w", err)
	}
	if ini.has("library", "boot9_path") || ini.has("", "boot9_path") {
		return Config{}, errors.New(`boot9_path is no longer configurable; place boot9.bin or boot9_prot.bin under internal/drivers/3ds in the application directory`)
	}
	sec, err := loadSecrets(dir)
	if err != nil {
		return Config{}, err
	}
	r := iniReader{ini: ini}
	var c Config
	c.LibraryDirectory = r.str("library", "directory")
	c.DatabasePath = r.str("library", "database_path")
	c.DatabaseURL = sec.get("DATABASE_URL")
	c.PokemonSidecarURL = strings.TrimRight(sec.get("POKEMON_SIDECAR_URL"), "/")
	if c.PokemonSidecarURL == "" {
		c.PokemonSidecarURL = strings.TrimRight(r.str("pokemon", "sidecar_url"), "/")
	}
	c.NDecryptSidecarURL = strings.TrimRight(sec.get("NDECRYPT_SIDECAR_URL"), "/")
	if c.NDecryptSidecarURL == "" {
		c.NDecryptSidecarURL = strings.TrimRight(r.str("ndecrypt", "sidecar_url"), "/")
	}
	c.RandomizerSidecarURL = strings.TrimRight(sec.get("RANDOMIZER_SIDECAR_URL"), "/")
	if c.RandomizerSidecarURL == "" {
		c.RandomizerSidecarURL = strings.TrimRight(r.str("randomizer", "sidecar_url"), "/")
	}
	c.AdminPassword = sec.get("ADMIN_PASSWORD")
	c.LibraryScanIntervalHours = r.integer("library", "scan_interval_hours")
	c.MetadataRefreshDays = r.integer("library", "metadata_refresh_days")
	c.ExistingOutput = r.str("library", "existing_output")
	c.UITheme = r.str("library", "ui_theme")

	c.Server.ListenAddress = r.str("server", "listen_address")
	c.Server.Port = r.integer("server", "port")
	c.Server.AdvertiseIP = r.str("server", "advertise_ip")
	c.Server.AdvertisePort = r.integer("server", "advertise_port")
	c.Server.PublicBaseURL = r.str("server", "public_base_url")
	// Environment overrides let a container advertise the host's reachable address
	// (config.ini is baked into the image, and inside a container IP auto-detection
	// only finds the unreachable compose-network address).
	if v := strings.TrimSpace(sec.get("ADVERTISE_IP")); v != "" {
		c.Server.AdvertiseIP = v
	}
	if v := strings.TrimSpace(sec.get("PUBLIC_BASE_URL")); v != "" {
		c.Server.PublicBaseURL = v
	}
	c.Server.RemoteAccessToken = sec.get("REMOTE_ACCESS_TOKEN")

	c.Metadata.Libretro.Enabled = r.boolean("metadata.libretro", "enabled")
	c.Metadata.Libretro.DatabaseDirectory = r.str("metadata.libretro", "database_directory")
	c.Metadata.Libretro.ThumbnailDirectory = r.str("metadata.libretro", "thumbnail_directory")
	c.Metadata.Libretro.AutoUpdate = r.boolean("metadata.libretro", "auto_update")
	c.Metadata.Libretro.DownloadThumbnails = r.boolean("metadata.libretro", "download_thumbnails")
	c.Metadata.IGDB.Enabled = r.boolean("metadata.igdb", "enabled")
	c.Metadata.IGDB.ClientID = sec.get("IGDB_CLIENT_ID")
	c.Metadata.IGDB.ClientSecret = sec.get("IGDB_CLIENT_SECRET")
	c.Metadata.PokeAPI.Enabled = r.boolean("metadata.pokeapi", "enabled")
	c.Metadata.PokeAPI.DataDirectory = r.str("metadata.pokeapi", "data_directory")

	c.RemoteSearch.Enabled = r.boolean("remote_search", "enabled")
	c.RemoteSearch.Host = r.str("remote_search", "host")
	c.RemoteSearch.Port = r.integer("remote_search", "port")
	c.RemoteSearch.Username = r.str("remote_search", "username")
	c.RemoteSearch.Password = sec.get("REMOTE_SEARCH_PASSWORD")
	c.RemoteSearch.RootDirectory = r.str("remote_search", "root_directory")
	c.RemoteSearch.HostKeySHA256 = r.str("remote_search", "host_key_sha256")
	c.RemoteSearch.MaxResults = r.integer("remote_search", "max_results")
	c.RemoteSearch.MaxFileSizeMB = r.integer("remote_search", "max_file_size_mb")
	if platforms := ini.section("remote_search.platforms"); len(platforms) > 0 {
		c.RemoteSearch.PlatformDirectories = map[string]string{}
		for platform, directory := range platforms {
			c.RemoteSearch.PlatformDirectories[platform] = directory
		}
	}

	c.SaveDirectory = r.str("storage", "save_directory")

	c.Patching.Enabled = r.boolean("patching", "enabled")
	c.Patching.PatchDirectory = r.str("patching", "patch_directory")
	c.Patching.OutputDirectory = r.str("patching", "output_directory")
	c.Patching.AdminAccessToken = sec.get("ADMIN_ACCESS_TOKEN")
	c.Patching.RetentionHours = r.integer("patching", "retention_hours")
	c.Patching.MaxROMSizeMB = r.integer("patching", "max_rom_size_mb")
	c.Patching.MaxPatchSizeMB = r.integer("patching", "max_patch_size_mb")
	c.Patching.TimeoutSeconds = r.integer("patching", "timeout_seconds")

	c.Randomizer.OutputDirectory = r.str("randomizer", "output_directory")
	c.Randomizer.RetentionHours = r.integer("randomizer", "retention_hours")
	c.Randomizer.MaxROMSizeMB = r.integer("randomizer", "max_rom_size_mb")
	c.Randomizer.TimeoutSeconds = r.integer("randomizer", "timeout_seconds")
	if r.err != nil {
		return Config{}, r.err
	}
	return loadV2(c, dir,
		ini.has("library", "scan_interval_hours"),
		ini.has("patching", "enabled"),
		ini.has("metadata.libretro", "enabled"),
		ini.has("metadata.libretro", "auto_update"),
		ini.has("metadata.libretro", "download_thumbnails"))
}

// iniReader reads typed values from an iniFile, remembering the first parse
// error so a batch of field reads can be validated once. Absent keys return the
// zero value; loadV2 then applies documented defaults, preserving behavior that
// distinguishes "unset" from "explicitly zero" via iniFile.has.
type iniReader struct {
	ini *iniFile
	err error
}

func (r *iniReader) str(section, key string) string {
	return r.ini.section(section)[key]
}

func (r *iniReader) integer(section, key string) int {
	if !r.ini.has(section, key) {
		return 0
	}
	raw := r.ini.section(section)[key]
	n, err := strconv.Atoi(raw)
	if err != nil && r.err == nil {
		r.err = fmt.Errorf("config.ini [%s] %s: expected an integer, got %q", section, key, raw)
	}
	return n
}

func (r *iniReader) boolean(section, key string) bool {
	if !r.ini.has(section, key) {
		return false
	}
	switch strings.ToLower(r.ini.section(section)[key]) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		if r.err == nil {
			r.err = fmt.Errorf("config.ini [%s] %s: expected true or false, got %q", section, key, r.ini.section(section)[key])
		}
		return false
	}
}

func loadV2(c Config, base string, scanConfigured, patchEnabledConfigured, libretroEnabledConfigured, libretroAutoUpdateConfigured, libretroThumbnailsConfigured bool) (Config, error) {
	if c.LibraryDirectory == "" {
		c.LibraryDirectory = "platforms"
	}
	if c.DatabasePath == "" {
		c.DatabasePath = "romrelay.db"
	}
	if !scanConfigured {
		c.LibraryScanIntervalHours = 1
	}
	if c.MetadataRefreshDays == 0 {
		c.MetadataRefreshDays = 30
	}
	if c.ExistingOutput == "" {
		c.ExistingOutput = "skip"
	}
	if c.UITheme == "" {
		c.UITheme = "default"
	}
	if c.Server.ListenAddress == "" {
		c.Server.ListenAddress = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.AdvertisePort == 0 {
		c.Server.AdvertisePort = c.Server.Port
	}
	if !patchEnabledConfigured {
		c.Patching.Enabled = true
	}
	if !libretroEnabledConfigured {
		c.Metadata.Libretro.Enabled = true
	}
	if !libretroAutoUpdateConfigured {
		c.Metadata.Libretro.AutoUpdate = true
	}
	if !libretroThumbnailsConfigured {
		c.Metadata.Libretro.DownloadThumbnails = true
	}
	if c.Metadata.Libretro.DatabaseDirectory == "" {
		c.Metadata.Libretro.DatabaseDirectory = filepath.Join("metadata", "libretro", "databases")
	}
	if c.Metadata.Libretro.ThumbnailDirectory == "" {
		c.Metadata.Libretro.ThumbnailDirectory = filepath.Join("metadata", "libretro", "thumbnails")
	}
	if c.SaveDirectory == "" {
		c.SaveDirectory = filepath.Join("storage", "saves")
	}
	if c.Metadata.PokeAPI.DataDirectory == "" {
		// The api-data submodule's endpoint tree; resolved like every other
		// relative path, against the config directory.
		c.Metadata.PokeAPI.DataDirectory = filepath.Join("third_party", "api-data", "data", "api", "v2")
	}
	if c.Patching.PatchDirectory == "" {
		c.Patching.PatchDirectory = "patches"
	}
	if c.Patching.OutputDirectory == "" {
		c.Patching.OutputDirectory = "patched"
	}
	if c.Patching.RetentionHours == 0 {
		c.Patching.RetentionHours = 24
	}
	if c.Patching.MaxROMSizeMB == 0 {
		c.Patching.MaxROMSizeMB = 512
	}
	if c.Patching.MaxPatchSizeMB == 0 {
		c.Patching.MaxPatchSizeMB = 64
	}
	if c.Patching.TimeoutSeconds == 0 {
		c.Patching.TimeoutSeconds = 120
	}
	if c.Randomizer.OutputDirectory == "" {
		c.Randomizer.OutputDirectory = "randomized"
	}
	if c.Randomizer.RetentionHours == 0 {
		c.Randomizer.RetentionHours = 24
	}
	if c.Randomizer.MaxROMSizeMB == 0 {
		c.Randomizer.MaxROMSizeMB = 512
	}
	if c.Randomizer.TimeoutSeconds == 0 {
		// Gen 5 randomization with heavy trainer/wild options can run tens of
		// seconds; higher default ceiling than patching's single IPS/BPS apply.
		c.Randomizer.TimeoutSeconds = 300
	}
	if c.RemoteSearch.Port == 0 {
		c.RemoteSearch.Port = 22
	}
	if c.RemoteSearch.RootDirectory == "" {
		c.RemoteSearch.RootDirectory = "/"
	}
	if c.RemoteSearch.MaxResults == 0 {
		c.RemoteSearch.MaxResults = 200
	}
	if c.RemoteSearch.MaxFileSizeMB == 0 {
		c.RemoteSearch.MaxFileSizeMB = 4096
	}
	if c.RemoteSearch.PlatformDirectories == nil {
		c.RemoteSearch.PlatformDirectories = map[string]string{"gb": "gb", "gbc": "gbc", "gba": "gba", "nds": "nds", "3ds": "3ds", "nes": "nes", "snes": "snes", "n64": "n64", "genesis": "genesis"}
	}
	if c.ExistingOutput != "skip" {
		return c, errors.New("existing_output must be \"skip\"")
	}
	if c.UITheme != "default" && c.UITheme != "corn" {
		return c, errors.New("ui_theme must be \"default\" or \"corn\"")
	}
	if c.LibraryScanIntervalHours < 0 || c.MetadataRefreshDays < 1 {
		return c, errors.New("scan interval must be non-negative and metadata_refresh_days positive")
	}
	if err := validateServer(c.Server); err != nil {
		return c, err
	}
	if c.Patching.AdminAccessToken != "" && len(c.Patching.AdminAccessToken) < 24 {
		return c, errors.New("admin_access_token must contain at least 24 characters")
	}
	if c.Patching.AdminAccessToken != "" && c.Patching.AdminAccessToken == c.Server.RemoteAccessToken {
		return c, errors.New("admin_access_token and remote_access_token must be distinct")
	}
	if c.Patching.RetentionHours < 1 || c.Patching.MaxROMSizeMB < 1 || c.Patching.MaxPatchSizeMB < 1 || c.Patching.TimeoutSeconds < 1 {
		return c, errors.New("patching limits must be positive")
	}
	if c.Randomizer.RetentionHours < 1 || c.Randomizer.MaxROMSizeMB < 1 || c.Randomizer.TimeoutSeconds < 1 {
		return c, errors.New("randomizer limits must be positive")
	}
	if c.Metadata.IGDB.Enabled && (c.Metadata.IGDB.ClientID == "" || c.Metadata.IGDB.ClientSecret == "") {
		// Degrade rather than refuse to boot: a container with restart policies
		// would otherwise crash-loop on a missing credential. Libretro metadata
		// still works; the operator sees a warning and can add IGDB_CLIENT_ID /
		// IGDB_CLIENT_SECRET later.
		c.Metadata.IGDB.Enabled = false
		c.Warnings = append(c.Warnings, "metadata.igdb is enabled but IGDB_CLIENT_ID/IGDB_CLIENT_SECRET are not set; IGDB enrichment is disabled")
	}
	if c.RemoteSearch.Enabled {
		if c.RemoteSearch.Host == "" || strings.Contains(c.RemoteSearch.Host, "://") || c.RemoteSearch.Username == "" || c.RemoteSearch.Password == "" {
			return c, errors.New("remote_search requires host (without a URL scheme), username, and password")
		}
		if c.RemoteSearch.Port < 1 || c.RemoteSearch.Port > 65535 || !strings.HasPrefix(c.RemoteSearch.HostKeySHA256, "SHA256:") {
			return c, errors.New("remote_search requires a valid port and SHA256: host-key fingerprint")
		}
		if !strings.HasPrefix(c.RemoteSearch.RootDirectory, "/") || c.RemoteSearch.MaxResults < 1 || c.RemoteSearch.MaxResults > 2000 || c.RemoteSearch.MaxFileSizeMB < 1 {
			return c, errors.New("remote_search root must be absolute and limits must be positive (max_results <= 2000)")
		}
		for platform, directory := range c.RemoteSearch.PlatformDirectories {
			if _, ok := map[string]bool{"gb": true, "gbc": true, "gba": true, "nds": true, "3ds": true, "nes": true, "snes": true, "n64": true, "genesis": true}[platform]; !ok || directory == "" || strings.Contains(directory, "..") || strings.ContainsAny(directory, "\\\x00") {
				return c, errors.New("remote_search platform_directories contains an invalid mapping")
			}
		}
	}
	c.LibraryDirectory = absolute(base, c.LibraryDirectory)
	c.DatabasePath = absolute(base, c.DatabasePath)
	c.SaveDirectory = absolute(base, c.SaveDirectory)
	c.Patching.PatchDirectory = absolute(base, c.Patching.PatchDirectory)
	c.Patching.OutputDirectory = absolute(base, c.Patching.OutputDirectory)
	c.Metadata.Libretro.DatabaseDirectory = absolute(base, c.Metadata.Libretro.DatabaseDirectory)
	c.Metadata.Libretro.ThumbnailDirectory = absolute(base, c.Metadata.Libretro.ThumbnailDirectory)
	c.Metadata.PokeAPI.DataDirectory = absolute(base, c.Metadata.PokeAPI.DataDirectory)
	c.Server.PublicBaseURL = strings.TrimRight(c.Server.PublicBaseURL, "/")
	for _, dir := range []string{c.LibraryDirectory, c.Patching.PatchDirectory, c.Patching.OutputDirectory, c.Metadata.Libretro.DatabaseDirectory, c.Metadata.Libretro.ThumbnailDirectory} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return c, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return c, nil
}

func validateServer(s Server) error {
	if s.Port < 1 || s.Port > 65535 || s.AdvertisePort < 1 || s.AdvertisePort > 65535 {
		return errors.New("server ports must be between 1 and 65535")
	}
	if ip := net.ParseIP(s.ListenAddress); ip == nil {
		return errors.New("listen_address must be an IP address")
	}
	if s.AdvertiseIP != "" && net.ParseIP(s.AdvertiseIP) == nil {
		return errors.New("advertise_ip must be an IP address")
	}
	if s.RemoteAccessToken != "" && len(s.RemoteAccessToken) < 24 {
		return errors.New("remote_access_token must contain at least 24 characters")
	}
	if s.PublicBaseURL != "" {
		u, err := url.Parse(s.PublicBaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("public_base_url must be an absolute HTTP(S) URL without credentials, query, or fragment")
		}
		s.PublicBaseURL = strings.TrimRight(s.PublicBaseURL, "/")
	}
	return nil
}

// DatabaseDSN returns the effective data source for the catalog: the Postgres
// DATABASE_URL when set, otherwise the SQLite database file path.
func (c Config) DatabaseDSN() string {
	if c.DatabaseURL != "" {
		return c.DatabaseURL
	}
	return c.DatabasePath
}

func absolute(base, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p, _ = filepath.Abs(p)
	return p
}
