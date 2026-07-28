// Package randomizer runs the Universal Pokemon Randomizer ZX (vendored jar,
// see sidecar/randomizer) against a ROM the user already owns or uploads. The
// randomization logic itself is generation-specific Java the sidecar owns;
// this package only stages the source ROM, calls the sidecar, and publishes
// the result as a normal catalog artifact, the same shape as internal/patch.
package randomizer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to the randomizer sidecar over HTTP.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a client for the sidecar at baseURL. A blank baseURL
// yields a client whose Available reports false, so callers can disable the
// randomizer tool entirely when no sidecar is configured.
func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{}}
}

// Available reports whether a sidecar is configured.
func (c *Client) Available() bool { return c != nil && c.baseURL != "" }

// Randomize sends rom (raw bytes) and settingsJSON (the flat settings object,
// see sidecar/randomizer/Shim.java for the field list) to the sidecar and
// returns the randomized ROM bytes. extension must include the leading dot
// (e.g. ".gba") and match one the sidecar's rom handlers accept. The caller's
// context governs the timeout: randomizing a Gen 5 ROM can take tens of
// seconds.
func (c *Client) Randomize(ctx context.Context, rom []byte, extension, settingsJSON string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/randomize", bytes.NewReader(rom))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Randomizer-Extension", extension)
	req.Header.Set("X-Randomizer-Settings", settingsJSON)
	req.ContentLength = int64(len(rom))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("randomizer sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, fmt.Errorf("randomizer sidecar returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Capabilities reports which misc tweaks the sidecar's real RomHandler
// detection accepts for this exact ROM (availability varies by generation and
// even by specific game/revision, see Shim.java's capabilities handler), so
// the UI can hide options that Settings.tweakForRom would otherwise silently
// drop at randomize time.
type Capabilities struct {
	Generation int      `json:"generation"`
	MiscTweaks []string `json:"misc_tweaks"`
}

func (c *Client) Capabilities(ctx context.Context, rom []byte, extension string) (Capabilities, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/capabilities", bytes.NewReader(rom))
	if err != nil {
		return Capabilities{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Randomizer-Extension", extension)
	req.ContentLength = int64(len(rom))
	resp, err := c.http.Do(req)
	if err != nil {
		return Capabilities{}, fmt.Errorf("randomizer sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return Capabilities{}, errors.New(e.Error)
		}
		return Capabilities{}, fmt.Errorf("randomizer sidecar returned %d", resp.StatusCode)
	}
	var caps Capabilities
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		return Capabilities{}, err
	}
	return caps, nil
}

// Health reports whether the sidecar is up, for startup logging only.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	client := c.http
	if client.Timeout == 0 {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("randomizer sidecar health check returned %d", resp.StatusCode)
	}
	return nil
}
