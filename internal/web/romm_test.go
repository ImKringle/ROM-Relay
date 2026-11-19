package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/romm"
)

// TestRommSearchCachesAndHidesToken: the listing is fetched once and then
// searched locally, covers are proxied, and neither the token nor RomM's URL
// reaches the browser.
func TestRommSearchCachesAndHidesToken(t *testing.T) {
	const token = "rmm_secret_token_value_123456"
	var listings atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/roms":
			listings.Add(1)
			// Two pages, to exercise paging.
			if r.URL.Query().Get("offset") == "0" {
				io.WriteString(w, `{"total":2,"items":[{"id":1,"name":"Pokemon Emerald","fs_name":"emerald.gba","fs_size_bytes":16777216,"platform_display_name":"Game Boy Advance","platform_slug":"gba","regions":["USA"],"summary":"Hoenn.","path_cover_small":"/assets/romm/resources/roms/1/cover/small.png","metadatum":{"genres":["RPG"],"first_release_date":1114387200000}}]}`)
			} else {
				io.WriteString(w, `{"total":2,"items":[{"id":2,"name":"Disc Game","fs_name":"disc.iso","platform_display_name":"PlayStation","platform_slug":"psx"}]}`)
			}
		case strings.HasPrefix(r.URL.Path, "/assets/romm/resources/"):
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "png")
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()

	d := t.TempDir()
	s, err := catalog.Open(filepath.Join(d, "db.sqlite"), filepath.Join(d, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const admin = "admin-token-abcdefghijklmnopqrstuvwxyz"
	ui := NewCatalog(s, "http://example", "", "default", admin, filepath.Join(d, "patches"), time.Hour)
	ui.SetRomm(romm.New(fake.URL, token, ""), filepath.Join(d, "platforms"))
	ui.SetReady(true, "")
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()

	get := func(path string) (int, string) {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	// The library's RomM source: anonymous visitors on this (open, no viewer
	// token) relay are not signed in, so RomM stays closed to them.
	if code, _ := get("/api/library?source=romm"); code != http.StatusForbidden {
		t.Fatalf("anonymous RomM library: got %d, want 403", code)
	}
	code, body := get("/api/library?source=romm&admin_token=" + admin + "&q=emerald")
	if code != http.StatusOK {
		t.Fatalf("library: %d %s", code, body)
	}
	for _, want := range []string{`"title":"Pokemon Emerald"`, `"year":"2005"`, `"cover":"/media/romm-1/cover"`, `"total":1`} {
		if !strings.Contains(body, want) {
			t.Errorf("library response missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, token) || strings.Contains(body, fake.URL) {
		t.Fatal("library response leaks the RomM token or URL")
	}
	if code, body := get("/media/romm-1/cover?admin_token=" + admin); code != http.StatusOK || body != "png" {
		t.Fatalf("RomM cover via /media: %d %q", code, body)
	}
	if code, _ := get("/api/romm/cover/99?admin_token=" + admin); code != http.StatusNotFound {
		t.Fatalf("unknown cover id: got %d, want 404", code)
	}
	code, body = get("/api/library/romm-1?admin_token=" + admin)
	if code != http.StatusOK || !strings.Contains(body, `"grabbable":true`) || !strings.Contains(body, "Hoenn.") || !strings.Contains(body, `"genre":"RPG"`) {
		t.Fatalf("RomM game detail: %d %s", code, body)
	}
	_, body = get("/api/library/romm-2?admin_token=" + admin)
	if !strings.Contains(body, `"grabbable":false`) {
		t.Errorf("an unsupported file must not be grabbable: %s", body)
	}
	if strings.Contains(body, token) || strings.Contains(body, fake.URL) {
		t.Fatal("RomM detail leaks the token or URL")
	}
	if n := listings.Load(); n != 2 {
		t.Fatalf("the listing should load once (2 pages) and then be cached, got %d page fetches", n)
	}
}
