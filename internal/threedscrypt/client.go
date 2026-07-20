// Package threedscrypt encrypts and decrypts raw Nintendo DS/3DS cart dumps
// (.nds/.srl/.dsi/.ids, .3ds/.cci) by delegating to the NDecrypt .NET sidecar.
// NDecrypt.Core is .NET-only, so it lives there rather than in this package,
// the same split internal/pokemon uses for PKHeX.Core.
//
// This has nothing to do with .cia files: NDecrypt.Core only handles raw cart
// dumps, never installed CIA packages. .cia decrypt stays on the separate
// internal/decryptor (Redux) path.
package threedscrypt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client talks to the NDecrypt sidecar over HTTP.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a client for the sidecar at baseURL. A blank baseURL
// yields a client whose Available reports false, so callers can disable the
// encrypt/decrypt upload flow.
func NewClient(baseURL string) *Client {
	// No client-side timeout: cart dumps run into multiple GB, so the caller's
	// context carries whatever deadline is appropriate for the file size
	// instead of a fixed one here.
	return &Client{baseURL: baseURL, http: &http.Client{}}
}

// Available reports whether a sidecar is configured.
func (c *Client) Available() bool { return c != nil && c.baseURL != "" }

// SupportsExtension reports whether ext is one this sidecar can process at
// all, the same DS/3DS extension set NDecrypt.Core itself recognizes.
// Callers use this to route .cia elsewhere before ever calling the sidecar.
func SupportsExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".nds", ".srl", ".dsi", ".ids", ".3ds", ".cci":
		return true
	default:
		return false
	}
}

// PartitionStatus is one line of Info's report: a DS cart has one ("Secure
// Area"), a 3DS cart has eight ("Partition 0".."Partition 7").
type PartitionStatus struct {
	Name   string
	Status string // "Encrypted", "Decrypted", or "Empty"
}

// Info reports the current encryption state of every partition in cart,
// whose extension is ext (as it would appear on a filename, the sidecar
// dispatches by extension, not content sniffing).
func (c *Client) Info(ctx context.Context, ext string, cart []byte) ([]PartitionStatus, error) {
	body, err := c.post(ctx, "/info", ext, false, cart)
	if err != nil {
		return nil, err
	}
	var out struct {
		Info string `json:"info"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode info response: %w", err)
	}
	return parseInfo(out.Info), nil
}

// Direction reports the single, unambiguous operation Info's result implies:
// every partition already fully "Decrypted" or "Empty" means the next step is
// Encrypt; every partition "Encrypted" means Decrypt. A cart with a genuine
// mix (some partitions each way, real for multi-partition 3DS titles that
// only encrypt some content) has no single right answer, so ok is false and
// the caller must ask rather than guess.
func Direction(statuses []PartitionStatus) (decrypt bool, ok bool) {
	sawEncrypted, sawDecrypted := false, false
	for _, s := range statuses {
		switch s.Status {
		case "Encrypted":
			sawEncrypted = true
		case "Decrypted":
			sawDecrypted = true
		}
	}
	switch {
	case sawEncrypted && !sawDecrypted:
		return true, true
	case sawDecrypted && !sawEncrypted:
		return false, true
	default:
		return false, false
	}
}

func parseInfo(text string) []PartitionStatus {
	var out []PartitionStatus
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		name, status, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		out = append(out, PartitionStatus{Name: strings.TrimSpace(name), Status: strings.TrimSpace(status)})
	}
	return out
}

// Decrypt and Encrypt run the named operation on cart (extension ext) and
// return the resulting bytes. force skips the sidecar's sanity checks, the
// same escape hatch NDecrypt's own -f/--force flag is.
//
// A true result does not by itself mean every partition changed state, the
// underlying library reports success even when some partitions could not be
// processed for lack of the right keys (observed directly: a cart run through
// Decrypt with no key material came back with a few partitions flipped and
// the rest still "Encrypted", not an error). Callers that need to know
// whether the operation actually completed must call Info again on the
// result and check it, the same way EnsureComplete does for this package's
// own callers.
func (c *Client) Decrypt(ctx context.Context, ext string, cart []byte, force bool) ([]byte, error) {
	return c.post(ctx, "/decrypt", ext, force, cart)
}

func (c *Client) Encrypt(ctx context.Context, ext string, cart []byte, force bool) ([]byte, error) {
	return c.post(ctx, "/encrypt", ext, force, cart)
}

// EnsureComplete calls Info on result and returns an error unless every
// partition ended up in the direction the caller asked for, the
// success-includes-partial-failure gap Decrypt/Encrypt's own doc comment
// describes, closed in one place so every caller gets the same guarantee
// internal/converter.Validate already gives the other conversion path.
func (c *Client) EnsureComplete(ctx context.Context, ext string, result []byte, decrypted bool) error {
	statuses, err := c.Info(ctx, ext, result)
	if err != nil {
		return err
	}
	want := "Decrypted"
	if !decrypted {
		want = "Encrypted"
	}
	for _, s := range statuses {
		if s.Status != want && s.Status != "Empty" {
			return fmt.Errorf("incomplete: %s partition is still %s (missing key material, or an unsupported title such as seed crypto)", s.Name, s.Status)
		}
	}
	return nil
}

func (c *Client) post(ctx context.Context, path, ext string, force bool, cart []byte) ([]byte, error) {
	if !c.Available() {
		return nil, errors.New("NDecrypt sidecar is not configured")
	}
	url := c.baseURL + path + "?ext=" + strings.TrimPrefix(ext, ".")
	if force {
		url += "&force=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(cart))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact NDecrypt sidecar: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != "" {
			return nil, errors.New(apiErr.Error)
		}
		return nil, fmt.Errorf("NDecrypt sidecar %s: %s", path, resp.Status)
	}
	return body, nil
}
