package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"romrelay/internal/config"
)

// settingsFixture builds a saveVaultFixture and wires SetConfig from a fresh
// temp directory, the way main.go does for a real server.
func newSettingsFixture(t *testing.T) (*saveVaultFixture, string) {
	t.Helper()
	f := newSaveVaultFixture(t)
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.ui.SetConfig(cfg, dir)
	return f, dir
}

func TestSavingSettingsWritesConfigIniAndUpdatesTheServer(t *testing.T) {
	f, dir := newSettingsFixture(t)
	admin := f.adminClient(t, "owner")

	res := f.post(t, admin, "/api/admin/settings", validSettingsForm())
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a valid settings save must succeed: got %d", res.StatusCode)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "port = 9191") {
		t.Fatalf("config.ini must contain the new port: %s", data)
	}

	reloaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Server.Port != 9191 {
		t.Fatalf("the reloaded config must reflect the save: got port %d", reloaded.Server.Port)
	}
}

func TestSavingSettingsWithAnInvalidValueRestoresTheBackup(t *testing.T) {
	f, dir := newSettingsFixture(t)
	admin := f.adminClient(t, "owner")

	// A first, valid save establishes a config.ini to roll back to.
	res := f.post(t, admin, "/api/admin/settings", validSettingsForm())
	res.Body.Close()
	before, err := os.ReadFile(filepath.Join(dir, "config.ini"))
	if err != nil {
		t.Fatal(err)
	}

	form := validSettingsForm()
	form.Set("port", "99999")
	res = f.post(t, admin, "/api/admin/settings", form)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an out-of-range port must be rejected: got %d", res.StatusCode)
	}

	after, err := os.ReadFile(filepath.Join(dir, "config.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a rejected save must leave config.ini exactly as it was")
	}
}

// TestSavingInvalidSettingsWithNoPriorConfigLeavesNoFile pins the edge case a
// naive restore would get wrong: if config.ini did not exist before the save
// (nothing to back up), a rejected save must not leave the invalid file it
// wrote behind, it must remove it, restoring the true "absent" state.
func TestSavingInvalidSettingsWithNoPriorConfigLeavesNoFile(t *testing.T) {
	f, dir := newSettingsFixture(t)
	admin := f.adminClient(t, "owner")

	if _, err := os.Stat(filepath.Join(dir, "config.ini")); !os.IsNotExist(err) {
		t.Fatalf("fixture must start with no config.ini: %v", err)
	}

	form := validSettingsForm()
	form.Set("port", "99999")
	res := f.post(t, admin, "/api/admin/settings", form)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an out-of-range port must be rejected: got %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.ini")); !os.IsNotExist(err) {
		t.Fatal("a rejected save with nothing to restore to must leave no config.ini behind")
	}
}

func TestSavingSettingsNeverWritesASecretFieldToDisk(t *testing.T) {
	f, dir := newSettingsFixture(t)
	admin := f.adminClient(t, "owner")

	form := validSettingsForm()
	// A field the form never legitimately sends, simulates a tampered POST
	// trying to smuggle a secret into config.ini via the allowlisted writer.
	form.Set("database_url", "postgres://attacker/db")
	res := f.post(t, admin, "/api/admin/settings", form)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the save must still succeed, ignoring the unknown field: got %d", res.StatusCode)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "attacker") || strings.Contains(string(data), "database_url") {
		t.Fatalf("config.ini must never contain a secret field: %s", data)
	}
	reloaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DatabaseURL != "" {
		t.Fatalf("DatabaseURL must stay environment-sourced, not settable through the form: got %q", reloaded.DatabaseURL)
	}
}

func TestSettingsEndpointsRequireAdmin(t *testing.T) {
	f, _ := newSettingsFixture(t)
	plain := f.signIn(t, "ash")

	res, err := plain.Get(f.server.URL + "/admin/settings")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a plain user must not reach settings: got %d", res.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/api/admin/settings", strings.NewReader(validSettingsForm().Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", f.ui.csrf)
	res, err = plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a plain user must not save settings: got %d", res.StatusCode)
	}
}

func validSettingsForm() url.Values {
	return url.Values{
		"ui_theme":                       {"default"},
		"scan_interval_hours":            {"1"},
		"metadata_refresh_days":          {"30"},
		"listen_address":                 {"0.0.0.0"},
		"port":                           {"9191"},
		"advertise_ip":                   {""},
		"advertise_port":                 {"9191"},
		"public_base_url":                {""},
		"libretro_enabled":               {"true"},
		"libretro_auto_update":           {"true"},
		"libretro_download_thumbnails":   {"true"},
		"igdb_enabled":                   {"false"},
		"pokeapi_enabled":                {"false"},
		"remote_search_enabled":          {"false"},
		"remote_search_host":             {""},
		"remote_search_port":             {"22"},
		"remote_search_username":         {""},
		"remote_search_root_directory":   {"/"},
		"remote_search_host_key_sha256":  {""},
		"remote_search_max_results":      {"200"},
		"remote_search_max_file_size_mb": {"4096"},
		"patching_enabled":               {"true"},
		"retention_hours":                {"24"},
		"max_rom_size_mb":                {"512"},
		"max_patch_size_mb":              {"64"},
		"timeout_seconds":                {"120"},
	}
}
