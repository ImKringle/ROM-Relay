// Package config loads ROM Relay's v2 configuration and the former CornTool
// configuration. Legacy fields are retained on Config so older integrations can
// migrate without a flag day; new code should use the v2 fields.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
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

type Metadata struct {
	Libretro LibretroMetadata `json:"libretro"`
	IGDB     IGDBMetadata     `json:"igdb"`
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

type Config struct {
	LibraryDirectory         string       `json:"library_directory"`
	DatabasePath             string       `json:"database_path"`
	LibraryScanIntervalHours int          `json:"library_scan_interval_hours"`
	MetadataRefreshDays      int          `json:"metadata_refresh_days"`
	Boot9Path                string       `json:"boot9_path"`
	ExistingOutput           string       `json:"existing_output"`
	UITheme                  string       `json:"ui_theme"`
	Server                   Server       `json:"server"`
	Metadata                 Metadata     `json:"metadata"`
	RemoteSearch             RemoteSearch `json:"remote_search"`
	Patching                 Patching     `json:"patching"`

	// Former CornTool v1 fields.
	InputDirectory string   `json:"input_directory,omitempty"`
	ServeDirectory string   `json:"serve_directory,omitempty"`
	ListenAddress  string   `json:"listen_address,omitempty"`
	Port           int      `json:"port,omitempty"`
	AdvertiseIP    string   `json:"advertise_ip,omitempty"`
	AdvertisePort  int      `json:"advertise_port,omitempty"`
	RemoteToken    string   `json:"remote_access_token,omitempty"`
	Legacy         bool     `json:"-"`
	Warnings       []string `json:"-"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	legacy := probe["input_directory"] != nil || probe["serve_directory"] != nil
	if !legacy && probe["boot9_path"] != nil {
		return Config{}, errors.New(`boot9_path is no longer configurable; place boot9.bin or boot9_prot.bin under internal\drivers\boot9-3ds beside ROMRelay.exe`)
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return c, errors.New("parse config: expected one JSON document")
	}
	if legacy {
		return loadLegacy(c)
	}
	patchEnabledConfigured := false
	libretroEnabledConfigured := false
	libretroAutoUpdateConfigured := false
	libretroThumbnailsConfigured := false
	if raw := probe["patching"]; raw != nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			patchEnabledConfigured = fields["enabled"] != nil
		}
	}
	if raw := probe["metadata"]; raw != nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil && fields["libretro"] != nil {
			var libretro map[string]json.RawMessage
			if json.Unmarshal(fields["libretro"], &libretro) == nil {
				libretroEnabledConfigured = libretro["enabled"] != nil
				libretroAutoUpdateConfigured = libretro["auto_update"] != nil
				libretroThumbnailsConfigured = libretro["download_thumbnails"] != nil
			}
		}
	}
	return loadV2(c, filepath.Dir(path), probe["library_scan_interval_hours"] != nil, patchEnabledConfigured, libretroEnabledConfigured, libretroAutoUpdateConfigured, libretroThumbnailsConfigured)
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
	if c.Metadata.IGDB.Enabled && (c.Metadata.IGDB.ClientID == "" || c.Metadata.IGDB.ClientSecret == "") {
		return c, errors.New("metadata.igdb requires client_id and client_secret when enabled")
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
	c.Patching.PatchDirectory = absolute(base, c.Patching.PatchDirectory)
	c.Patching.OutputDirectory = absolute(base, c.Patching.OutputDirectory)
	c.Metadata.Libretro.DatabaseDirectory = absolute(base, c.Metadata.Libretro.DatabaseDirectory)
	c.Metadata.Libretro.ThumbnailDirectory = absolute(base, c.Metadata.Libretro.ThumbnailDirectory)
	c.Server.PublicBaseURL = strings.TrimRight(c.Server.PublicBaseURL, "/")
	for _, dir := range []string{c.LibraryDirectory, c.Patching.PatchDirectory, c.Patching.OutputDirectory, c.Metadata.Libretro.DatabaseDirectory, c.Metadata.Libretro.ThumbnailDirectory} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return c, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return c, nil
}

func loadLegacy(c Config) (Config, error) {
	c.Legacy = true
	c.Warnings = []string{"legacy CornTool configuration detected; migrate to the nested ROM Relay v2 configuration"}
	if c.ListenAddress == "" {
		c.ListenAddress = "0.0.0.0"
	}
	if c.Port == 0 {
		c.Port = 8080
	}
	if c.AdvertisePort == 0 {
		c.AdvertisePort = c.Port
	}
	if c.ExistingOutput == "" {
		c.ExistingOutput = "skip"
	}
	if c.InputDirectory == "" || c.ServeDirectory == "" {
		return c, errors.New("input_directory and serve_directory are required")
	}
	for label, p := range map[string]string{"input_directory": c.InputDirectory, "serve_directory": c.ServeDirectory} {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return c, fmt.Errorf("%s is not an accessible directory: %s", label, p)
		}
	}
	if c.RemoteToken != "" && len(c.RemoteToken) < 16 {
		return c, errors.New("remote_access_token must contain at least 16 characters")
	}
	if ip := net.ParseIP(c.AdvertiseIP); c.AdvertiseIP != "" && (ip == nil || ip.To4() == nil) {
		return c, errors.New("advertise_ip must be an IPv4 address")
	}
	if ip := net.ParseIP(c.ListenAddress); ip == nil || ip.To4() == nil {
		return c, errors.New("listen_address must be an IPv4 address")
	}
	if c.ExistingOutput != "skip" {
		return c, errors.New("existing_output must be \"skip\" in v1")
	}
	s := Server{c.ListenAddress, c.Port, c.AdvertiseIP, c.AdvertisePort, "", c.RemoteToken}
	if err := validateServer(s); err != nil {
		return c, err
	}
	c.InputDirectory, _ = filepath.Abs(c.InputDirectory)
	c.ServeDirectory, _ = filepath.Abs(c.ServeDirectory)
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

func absolute(base, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p, _ = filepath.Abs(p)
	return p
}
