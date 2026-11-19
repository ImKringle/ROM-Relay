package torrent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Category tags every torrent ROM Relay adds, so Sweep only ever touches its
// own torrents and leaves anything else in the qBittorrent instance alone.
const Category = "romrelay"

// Client talks to qBittorrent's Web API (v2). It works against 4.x and 5.x.
type Client struct {
	base, user, pass string
	http             *http.Client
}

func NewClient(base, user, pass string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{base: strings.TrimRight(base, "/"), user: user, pass: pass, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

// Status is one ROM Relay torrent as qBittorrent reports it.
type Status struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	Progress float64 `json:"progress"`
	State    string  `json:"state"`
	DLSpeed  int64   `json:"dlspeed"`   // bytes/s
	Seeds    int     `json:"num_seeds"` // connected
	Peers    int     `json:"num_leechs"`
}

// Grab adds the torrent with only the chosen file selected, saving into
// savePath (as qBittorrent sees the filesystem). If the torrent is already in
// qBittorrent from an earlier grab, the file is just switched on.
func (c *Client) Grab(ctx context.Context, data []byte, m Meta, file int, savePath string) error {
	existing, err := c.info(ctx, url.Values{"hashes": {m.Hash}})
	if err != nil {
		return err
	}
	// added: this grab put the torrent into qBittorrent, so it owns the whole
	// file selection (and may remove the torrent if the grab fails).
	added := len(existing) == 0
	if added {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		part, _ := w.CreateFormFile("torrents", m.Name+".torrent")
		_, _ = part.Write(data)
		for k, v := range map[string]string{
			"savepath": savePath, "category": Category, "autoTMM": "false",
			// Added stopped so nothing downloads before the file selection
			// below; 4.x reads "paused", 5.x reads "stopped".
			"paused": "true", "stopped": "true",
			// No seeding after completion (Sweep also removes it the moment
			// the file is done). Upload while downloading is left uncapped:
			// downloaders in the swarm only keep sending to peers that send
			// back, so a capped client crawls on anything but pure seeds.
			"ratioLimit": "0", "seedingTimeLimit": "0",
		} {
			_ = w.WriteField(k, v)
		}
		_ = w.Close()
		res, err := c.call(ctx, "/api/v2/torrents/add", w.FormDataContentType(), body.Bytes())
		if err != nil {
			return err
		}
		if strings.TrimSpace(res) == "Fails." {
			return errors.New("qBittorrent refused the torrent")
		}
		// Adding is asynchronous; wait for qBittorrent to register it.
		for i := 0; ; i++ {
			if existing, err = c.info(ctx, url.Values{"hashes": {m.Hash}}); err == nil && len(existing) > 0 {
				break
			}
			if i == 40 {
				return c.abandon(ctx, m.Hash, true, errors.New("qBittorrent accepted the torrent but never listed it"))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	var skip []string
	visible := map[int]bool{}
	for _, f := range m.Files {
		visible[f.Index] = true
		if f.Index != file {
			skip = append(skip, strconv.Itoa(f.Index))
		}
	}
	// Select, then read the selection back, and only start once it has held
	// for two reads in a row. qBittorrent 5.x lists a torrent while it is
	// still loading and can silently drop priority changes made in that
	// window, which once left all 7,770 files of a set downloading. The
	// torrent was added stopped, so nothing transfers until this passes.
	for i, stable := 0, 0; stable < 2; i++ {
		if added && len(skip) > 0 {
			if err := c.form(ctx, "/api/v2/torrents/filePrio", url.Values{"hash": {m.Hash}, "id": {strings.Join(skip, "|")}, "priority": {"0"}}); err != nil {
				return c.abandon(ctx, m.Hash, added, err)
			}
		}
		if err := c.form(ctx, "/api/v2/torrents/filePrio", url.Values{"hash": {m.Hash}, "id": {strconv.Itoa(file)}, "priority": {"1"}}); err != nil {
			return c.abandon(ctx, m.Hash, added, err)
		}
		if ok, _ := c.selectionIs(ctx, m.Hash, file, added, visible); ok {
			stable++
		} else {
			stable = 0
		}
		if i == 40 {
			return c.abandon(ctx, m.Hash, added, errors.New("qBittorrent did not apply the file selection, grab cancelled"))
		}
		select {
		case <-ctx.Done():
			return c.abandon(ctx, m.Hash, added, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	// 5.x renamed resume to start; try the new name, fall back to the old.
	if err := c.form(ctx, "/api/v2/torrents/start", url.Values{"hashes": {m.Hash}}); err != nil {
		if err := c.form(ctx, "/api/v2/torrents/resume", url.Values{"hashes": {m.Hash}}); err != nil {
			return c.abandon(ctx, m.Hash, added, err)
		}
	}
	// Last line of defence: if starting somehow reset the selection, pull the
	// torrent (and whatever it wrote) rather than let it fetch the whole set.
	if ok, err := c.selectionIs(ctx, m.Hash, file, added, visible); !ok {
		if err == nil {
			err = errors.New("qBittorrent changed the file selection on start, grab cancelled")
		}
		return c.abandon(ctx, m.Hash, added, err)
	}
	return nil
}

// selectionIs reports whether the chosen file is on and, when exclusive,
// every other (non-padding) file is off. qBittorrent lists files in index
// order, so position is the file index.
func (c *Client) selectionIs(ctx context.Context, hash string, file int, exclusive bool, visible map[int]bool) (bool, error) {
	raw, err := c.request(ctx, http.MethodGet, "/api/v2/torrents/files?hash="+hash, "", nil)
	if err != nil {
		return false, err
	}
	var files []struct {
		Priority int `json:"priority"`
	}
	if err := json.Unmarshal([]byte(raw), &files); err != nil || file >= len(files) {
		return false, err
	}
	for i, f := range files {
		if i == file && f.Priority == 0 || i != file && exclusive && visible[i] && f.Priority != 0 {
			return false, nil
		}
	}
	return true, nil
}

// abandon removes a torrent this grab added (files too: anything written is
// partial), so a failed grab never leaves a torrent behind to download later.
func (c *Client) abandon(ctx context.Context, hash string, added bool, cause error) error {
	if added {
		_ = c.form(context.WithoutCancel(ctx), "/api/v2/torrents/delete", url.Values{"hashes": {hash}, "deleteFiles": {"true"}})
	}
	return cause
}

// Sweep removes every finished ROM Relay torrent from qBittorrent, keeping its
// files on disk, and returns what finished and what is still downloading.
func (c *Client) Sweep(ctx context.Context) (done, active []Status, err error) {
	all, err := c.info(ctx, url.Values{"category": {Category}})
	if err != nil {
		return nil, nil, err
	}
	for _, t := range all {
		if t.Progress < 1 {
			active = append(active, t)
			continue
		}
		if err := c.form(ctx, "/api/v2/torrents/delete", url.Values{"hashes": {t.Hash}, "deleteFiles": {"false"}}); err != nil {
			return done, active, err
		}
		done = append(done, t)
	}
	return done, active, nil
}

func (c *Client) info(ctx context.Context, q url.Values) ([]Status, error) {
	res, err := c.request(ctx, http.MethodGet, "/api/v2/torrents/info?"+q.Encode(), "", nil)
	if err != nil {
		return nil, err
	}
	var out []Status
	return out, json.Unmarshal([]byte(res), &out)
}

func (c *Client) form(ctx context.Context, path string, v url.Values) error {
	_, err := c.call(ctx, path, "application/x-www-form-urlencoded", []byte(v.Encode()))
	return err
}

func (c *Client) call(ctx context.Context, path, contentType string, body []byte) (string, error) {
	return c.request(ctx, http.MethodPost, path, contentType, body)
}

// request sends one API call, logging in and retrying once if the session is
// missing or expired (qBittorrent answers 403).
func (c *Client) request(ctx context.Context, method, path, contentType string, body []byte) (string, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Referer", c.base) // qBittorrent's CSRF check
		res, err := c.http.Do(req)
		if err != nil {
			return "", fmt.Errorf("qBittorrent unreachable: %w", err)
		}
		text, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusForbidden && attempt == 0 {
			if err := c.login(ctx); err != nil {
				return "", err
			}
			continue
		}
		if res.StatusCode/100 != 2 { // 5.x answers some calls 204 No Content
			return "", fmt.Errorf("qBittorrent %s: %d %s", path, res.StatusCode, strings.TrimSpace(string(text)))
		}
		return string(text), nil
	}
}

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.user}, "password": {c.pass}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v2/auth/login", strings.NewReader(form))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base)
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qBittorrent unreachable: %w", err)
	}
	text, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	res.Body.Close()
	// 4.x: 200 "Ok." (or 200 "Fails."); 5.x: 204 with an empty body.
	body := strings.TrimSpace(string(text))
	if res.StatusCode/100 != 2 || (body != "Ok." && body != "") {
		return errors.New("qBittorrent login failed: check [torrent] username and QBITTORRENT_PASSWORD")
	}
	return nil
}
