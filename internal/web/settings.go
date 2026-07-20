package web

import (
	"net/http"
	"strconv"
	"strings"

	"romrelay/internal/config"
)

// settingsPage is what adminsettings.html renders: the current values of the
// fields the form allows editing. Directory paths (library, patch, save,
// metadata cache locations) are shown nowhere here, they are filesystem
// layout, not runtime knobs, and are left to a hand-edited config.ini as
// today. Every field on this struct is ini-backed; nothing secret is ever
// read into it.
type settingsPage struct {
	pageData
	Cfg config.Config
}

// adminSettings serves the Settings screen. It 503s rather than rendering an
// empty form when SetConfig was never called, that only happens in tests
// that don't wire it, never in the real server (main.go always calls it).
func (s *CatalogServer) adminSettings(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	if s.adminTokenLanding(w, r, "/admin/settings") {
		return
	}
	cfg, _, ok := s.config()
	if !ok {
		http.Error(w, "settings are unavailable: the server was started without a configuration directory", http.StatusServiceUnavailable)
		return
	}
	s.render(w, "adminsettings", "adminsettings", settingsPage{pageData: s.newPageData(r), Cfg: cfg})
}

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
