package web

import (
	"net/http"
	"strconv"
	"strings"

	"romrelay/internal/config"
)

// saveSettings applies the allowlisted fields from the form to a clone of the
// current configuration, writes it, and re-validates by loading it back
// through the same config.Load every startup uses, so this never duplicates
// loadV2's rules, it just reuses them. A save that fails validation restores
// the previous config.ini and changes nothing on disk.
func (s *CatalogServer) saveSettings(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	cfg, dir, ok := s.config()
	if !ok {
		http.Error(w, "settings are unavailable: the server was started without a configuration directory", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	candidate := cfg
	var badField string
	intField := func(name string, dst *int) {
		if badField != "" {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
		if err != nil {
			badField = name
			return
		}
		*dst = n
	}
	strField := func(name string, dst *string) { *dst = strings.TrimSpace(r.FormValue(name)) }
	boolField := func(name string, dst *bool) { *dst = r.FormValue(name) == "true" }

	strField("ui_theme", &candidate.UITheme)
	intField("scan_interval_hours", &candidate.LibraryScanIntervalHours)
	intField("metadata_refresh_days", &candidate.MetadataRefreshDays)

	strField("listen_address", &candidate.Server.ListenAddress)
	intField("port", &candidate.Server.Port)
	strField("advertise_ip", &candidate.Server.AdvertiseIP)
	intField("advertise_port", &candidate.Server.AdvertisePort)
	strField("public_base_url", &candidate.Server.PublicBaseURL)

	boolField("libretro_enabled", &candidate.Metadata.Libretro.Enabled)
	boolField("libretro_auto_update", &candidate.Metadata.Libretro.AutoUpdate)
	boolField("libretro_download_thumbnails", &candidate.Metadata.Libretro.DownloadThumbnails)
	boolField("igdb_enabled", &candidate.Metadata.IGDB.Enabled)
	boolField("pokeapi_enabled", &candidate.Metadata.PokeAPI.Enabled)

	boolField("remote_search_enabled", &candidate.RemoteSearch.Enabled)
	strField("remote_search_host", &candidate.RemoteSearch.Host)
	intField("remote_search_port", &candidate.RemoteSearch.Port)
	strField("remote_search_username", &candidate.RemoteSearch.Username)
	strField("remote_search_root_directory", &candidate.RemoteSearch.RootDirectory)
	strField("remote_search_host_key_sha256", &candidate.RemoteSearch.HostKeySHA256)
	intField("remote_search_max_results", &candidate.RemoteSearch.MaxResults)
	intField("remote_search_max_file_size_mb", &candidate.RemoteSearch.MaxFileSizeMB)

	boolField("patching_enabled", &candidate.Patching.Enabled)
	intField("retention_hours", &candidate.Patching.RetentionHours)
	intField("max_rom_size_mb", &candidate.Patching.MaxROMSizeMB)
	intField("max_patch_size_mb", &candidate.Patching.MaxPatchSizeMB)
	intField("timeout_seconds", &candidate.Patching.TimeoutSeconds)

	if badField != "" {
		http.Error(w, badField+" must be a whole number", http.StatusBadRequest)
		return
	}

	if err := config.BackupINI(dir); err != nil {
		http.Error(w, "could not back up the current configuration: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := config.WriteINI(dir, candidate); err != nil {
		http.Error(w, "could not write configuration: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reloaded, err := config.Load(dir)
	if err != nil {
		_ = config.RestoreINIBackup(dir)
		http.Error(w, "rejected: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.cfgMu.Lock()
	s.cfg = reloaded
	s.cfgMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"saved":  true,
		"detail": "Saved. Restart ROM Relay for these changes to take effect.",
	})
}

// apiSettings returns exactly the fields saveSettings accepts, by name. It is
// an explicit allowlist rather than the Config struct: Config's JSON tags
// would expose secrets (IGDB client secret) to the browser.
func (s *CatalogServer) apiSettings(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	cfg, _, ok := s.config()
	if !ok {
		http.Error(w, "settings are unavailable: the server was started without a configuration directory", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scan_interval_hours":            cfg.LibraryScanIntervalHours,
		"metadata_refresh_days":          cfg.MetadataRefreshDays,
		"listen_address":                 cfg.Server.ListenAddress,
		"port":                           cfg.Server.Port,
		"advertise_ip":                   cfg.Server.AdvertiseIP,
		"advertise_port":                 cfg.Server.AdvertisePort,
		"public_base_url":                cfg.Server.PublicBaseURL,
		"libretro_enabled":               cfg.Metadata.Libretro.Enabled,
		"libretro_auto_update":           cfg.Metadata.Libretro.AutoUpdate,
		"libretro_download_thumbnails":   cfg.Metadata.Libretro.DownloadThumbnails,
		"igdb_enabled":                   cfg.Metadata.IGDB.Enabled,
		"pokeapi_enabled":                cfg.Metadata.PokeAPI.Enabled,
		"remote_search_enabled":          cfg.RemoteSearch.Enabled,
		"remote_search_host":             cfg.RemoteSearch.Host,
		"remote_search_port":             cfg.RemoteSearch.Port,
		"remote_search_username":         cfg.RemoteSearch.Username,
		"remote_search_root_directory":   cfg.RemoteSearch.RootDirectory,
		"remote_search_host_key_sha256":  cfg.RemoteSearch.HostKeySHA256,
		"remote_search_max_results":      cfg.RemoteSearch.MaxResults,
		"remote_search_max_file_size_mb": cfg.RemoteSearch.MaxFileSizeMB,
		"patching_enabled":               cfg.Patching.Enabled,
		"retention_hours":                cfg.Patching.RetentionHours,
		"max_rom_size_mb":                cfg.Patching.MaxROMSizeMB,
		"max_patch_size_mb":              cfg.Patching.MaxPatchSizeMB,
		"timeout_seconds":                cfg.Patching.TimeoutSeconds,
	})
}
