// Package romm talks to a RomM instance with an API token. The token stays in
// this process: callers get ROM data and streams, never URLs carrying it.
package romm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrRequest is what users see when RomM fails. Details (which include the
// RomM URL) go to the log only.
var ErrRequest = errors.New("RomM request failed; see the relay log")

// TTL: the RomM library rarely changes, so the whole listing is fetched once a week
// and reused (across restarts, via cacheFile) until it expires or Refresh.
const TTL = 7 * 24 * time.Hour

type Client struct {
	base, token string
	cacheFile   string       // listing persisted here, so a restart does not refetch
	api         *http.Client // single ROMs and images
	download    *http.Client // listing and ROM transfers, no overall timeout

	mu   sync.Mutex
	roms []ROM
	byID map[int]int
	at   time.Time
}

// New returns a client. cacheFile (optional) persists the listing.
func New(baseURL, token, cacheFile string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"), token: token, cacheFile: cacheFile,
		api:      &http.Client{Timeout: 30 * time.Second},
		download: &http.Client{},
	}
}

// ROM is the subset of RomM's rom schema the relay reads (checked against
// RomM 5.4.1).
type ROM struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	FSName     string   `json:"fs_name"`
	Size       int64    `json:"fs_size_bytes"`
	Platform   string   `json:"platform_display_name"`
	PlatSlug   string   `json:"platform_slug"`
	Regions    []string `json:"regions"`
	Languages  []string `json:"languages"`
	Tags       []string `json:"tags"`
	Summary    string   `json:"summary"`
	CoverSmall string   `json:"path_cover_small"`
	CoverLarge string   `json:"path_cover_large"`
	Screens    []string `json:"merged_screenshots"`
	MD5        string   `json:"md5_hash"`
	SHA1       string   `json:"sha1_hash"`
	Multi      bool     `json:"multi"`              // RomM 3.x
	MultiFiles bool     `json:"has_multiple_files"` // RomM 4.x+
	Files      []struct {
		MD5  string `json:"md5_hash"`
		SHA1 string `json:"sha1_hash"`
	} `json:"files"`
	Metadatum struct {
		Genres       []string        `json:"genres"`
		Companies    []string        `json:"companies"`
		Developers   []string        `json:"developers"`
		Publishers   []string        `json:"publishers"`
		GameModes    []string        `json:"game_modes"`
		PlayerCount  string          `json:"player_count"`
		Rating       json.RawMessage `json:"average_rating"`
		FirstRelease json.RawMessage `json:"first_release_date"` // epoch ms, may be null
	} `json:"metadatum"`
}

func (r ROM) Archive() bool {
	return r.Multi || r.MultiFiles || strings.HasSuffix(strings.ToLower(r.FSName), ".zip") || strings.HasSuffix(strings.ToLower(r.FSName), ".7z")
}

// Year is the first release year, or "".
func (r ROM) Year() string {
	if ms, err := strconv.ParseFloat(string(r.Metadatum.FirstRelease), 64); err == nil && ms > 0 {
		return strconv.Itoa(time.UnixMilli(int64(ms)).UTC().Year())
	}
	return ""
}

// Rating is RomM's average rating as text, or "".
func (r ROM) Rating() string {
	if v, err := strconv.ParseFloat(strings.Trim(string(r.Metadatum.Rating), `"`), 64); err == nil && v > 0 {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return ""
}

// Developer and Publisher fall back to RomM's combined companies list.
func (r ROM) Developer() string {
	if len(r.Metadatum.Developers) > 0 {
		return r.Metadatum.Developers[0]
	}
	if len(r.Metadatum.Companies) > 0 {
		return r.Metadatum.Companies[0]
	}
	return ""
}

func (r ROM) Publisher() string {
	if len(r.Metadatum.Publishers) > 0 {
		return r.Metadatum.Publishers[0]
	}
	return ""
}

// Hashes lists every md5/sha1 RomM knows for this ROM, lower-cased.
func (r ROM) Hashes() []string {
	var out []string
	add := func(h string) {
		if h != "" {
			out = append(out, strings.ToLower(h))
		}
	}
	add(r.MD5)
	add(r.SHA1)
	for _, f := range r.Files {
		add(f.MD5)
		add(f.SHA1)
	}
	return out
}

// ResourcePath reports whether p is one of RomM's static resource paths
// (covers, screenshots): the only paths the relay proxies for browsers.
func ResourcePath(p string) bool {
	bare := strings.SplitN(p, "?", 2)[0]
	return strings.HasPrefix(bare, "/assets/romm/resources/") && !strings.Contains(bare, "..")
}

// CoverPath is RomM's cover resource path (large preferred when large is
// true), or "" when missing or not a RomM resource path.
func (r ROM) CoverPath(large bool) string {
	p := r.CoverSmall
	if large && r.CoverLarge != "" {
		p = r.CoverLarge
	}
	if !ResourcePath(p) {
		return ""
	}
	return p
}

// Get calls RomM. Go drops the Authorization header on a redirect to another
// host, so a misbehaving RomM cannot bounce the token elsewhere. long selects
// the client without an overall timeout.
func (c *Client) Get(ctx context.Context, p string, long bool) (*http.Response, error) {
	// RomM's resource paths carry unescaped query values ("?ts=2025-04-08 17:23:12").
	p = strings.ReplaceAll(p, " ", "%20")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+p, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	client := c.api
	if long {
		client = c.download
	}
	resp, err := client.Do(req)
	bare := strings.SplitN(p, "?", 2)[0]
	if err != nil {
		log.Printf("romm: GET %s: %v", bare, err)
		return nil, ErrRequest
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		log.Printf("romm: GET %s: %s", bare, resp.Status)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, errors.New("RomM rejected the relay's token (check ROMM_TOKEN and its scopes)")
		}
		return nil, ErrRequest
	}
	return resp, nil
}

func (c *Client) decode(ctx context.Context, p string, long bool, out any) error {
	resp, err := c.Get(ctx, p, long)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512<<20)).Decode(out); err != nil {
		log.Printf("romm: decode %s: %v", strings.SplitN(p, "?", 2)[0], err)
		return ErrRequest
	}
	return nil
}

// ROMs returns the cached listing, loading it from disk or RomM on first use,
// after TTL, or when force is set. A failed unforced reload keeps serving the
// old copy.
// ponytail: one lock, so callers wait out a reload; fine at RomM library sizes.
func (c *Client) ROMs(ctx context.Context, force bool) ([]ROM, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.roms == nil {
		c.loadDisk()
	}
	if !force && c.roms != nil && time.Since(c.at) < TTL {
		return c.roms, nil
	}
	// Detached from the caller: a closed tab must not abort a half-done load.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	roms := []ROM{}
	for offset := 0; ; {
		var page struct {
			Items []ROM `json:"items"`
			Total int   `json:"total"`
		}
		// One big page, without RomM's library-wide indexes (char/id/filter),
		// which it otherwise repeats on every page: one light request per day.
		q := url.Values{"limit": {"10000"}, "offset": {strconv.Itoa(offset)}, "order_by": {"name"}, "order_dir": {"asc"},
			"with_char_index": {"false"}, "with_filter_values": {"false"}, "with_rom_id_index": {"false"}}
		if err := c.decode(ctx, "/api/roms?"+q.Encode(), true, &page); err != nil {
			if c.roms != nil && !force {
				return c.roms, nil
			}
			return nil, err
		}
		roms = append(roms, page.Items...)
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			break
		}
	}
	c.set(roms, time.Now())
	c.saveDisk()
	return roms, nil
}

// Loaded reports whether a listing is in memory or on disk, without calling
// RomM, so pages can avoid triggering a first fetch.
func (c *Client) Loaded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.roms == nil {
		c.loadDisk()
	}
	return c.roms != nil
}

func (c *Client) set(roms []ROM, at time.Time) {
	c.byID = make(map[int]int, len(roms))
	for i, r := range roms {
		c.byID[r.ID] = i
	}
	c.roms, c.at = roms, at
}

// diskListing is the persisted cache: only the fields ROM decodes, a few MB
// rather than RomM's full response.
type diskListing struct {
	At   time.Time `json:"at"`
	ROMs []ROM     `json:"roms"`
}

func (c *Client) loadDisk() {
	if c.cacheFile == "" {
		return
	}
	b, err := os.ReadFile(c.cacheFile)
	if err != nil {
		return
	}
	var d diskListing
	if json.Unmarshal(b, &d) == nil && d.ROMs != nil {
		c.set(d.ROMs, d.At)
	}
}

func (c *Client) saveDisk() {
	if c.cacheFile == "" {
		return
	}
	b, err := json.Marshal(diskListing{At: c.at, ROMs: c.roms})
	if err != nil {
		return
	}
	tmp := c.cacheFile + ".tmp"
	if os.MkdirAll(filepath.Dir(c.cacheFile), 0o755) == nil && os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.cacheFile)
	}
}

// Cached returns a ROM from the listing without calling RomM.
func (c *Client) Cached(id int) (ROM, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, ok := c.byID[id]
	if !ok {
		return ROM{}, false
	}
	return c.roms[i], true
}

// ROM fetches one ROM live, for grabs, where stale data would fail.
func (c *Client) ROM(ctx context.Context, id int) (ROM, error) {
	var r ROM
	err := c.decode(ctx, "/api/roms/"+strconv.Itoa(id), false, &r)
	return r, err
}

// Content streams a ROM's file (a zip for multi-file ROMs).
func (c *Client) Content(ctx context.Context, id int, fsName string) (*http.Response, error) {
	return c.Get(ctx, "/api/roms/"+strconv.Itoa(id)+"/content/"+url.PathEscape(fsName), true)
}
