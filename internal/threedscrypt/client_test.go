package threedscrypt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseInfoAndDirection(t *testing.T) {
	threeDS := parseInfo("\tPartition 0: Encrypted\r\n\tPartition 1: Encrypted\r\n")
	if len(threeDS) != 2 || threeDS[0].Name != "Partition 0" || threeDS[0].Status != "Encrypted" {
		t.Fatalf("parsed wrong: %#v", threeDS)
	}
	if decrypt, ok := Direction(threeDS); !ok || !decrypt {
		t.Fatalf("all-encrypted cart must resolve to decrypt: got %v %v", decrypt, ok)
	}

	allDecrypted := parseInfo("\tPartition 0: Decrypted\r\n\tPartition 1: Empty\r\n")
	if decrypt, ok := Direction(allDecrypted); !ok || decrypt {
		t.Fatalf("all-decrypted/empty cart must resolve to encrypt: got %v %v", decrypt, ok)
	}

	// A real mix has no single right answer: the caller must ask, not guess.
	mixed := parseInfo("\tPartition 0: Encrypted\r\n\tPartition 1: Decrypted\r\n")
	if _, ok := Direction(mixed); ok {
		t.Fatal("a genuine mix of encrypted and decrypted partitions must not resolve automatically")
	}
}

func TestSupportsExtension(t *testing.T) {
	for _, ext := range []string{".nds", ".NDS", ".3ds", ".cci", ".srl", ".dsi", ".ids"} {
		if !SupportsExtension(ext) {
			t.Errorf("%s must be supported", ext)
		}
	}
	for _, ext := range []string{".cia", ".zip", ""} {
		if SupportsExtension(ext) {
			t.Errorf("%s must not be supported - NDecrypt.Core has no CIA processor", ext)
		}
	}
}

func TestEnsureCompleteRejectsPartialDecrypt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirrors what the real sidecar returned with no key material: some
		// partitions flip, the rest that needed real keys stay Encrypted.
		w.Write([]byte(`{"info":"\tPartition 0: Decrypted\r\n\tPartition 1: Encrypted\r\n"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL)
	err := c.EnsureComplete(context.Background(), ".3ds", []byte("fake"), true)
	if err == nil {
		t.Fatal("a cart with a still-encrypted partition must not be reported complete")
	}
}

func TestEnsureCompleteAcceptsFullDecrypt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"info":"\tPartition 0: Decrypted\r\n\tPartition 1: Decrypted\r\n"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL)
	if err := c.EnsureComplete(context.Background(), ".3ds", []byte("fake"), true); err != nil {
		t.Fatalf("a fully decrypted cart must be reported complete: %v", err)
	}
}

func TestPostSurfacesSidecarError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":"decrypt failed - wrong keys"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL)
	_, err := c.Decrypt(context.Background(), ".3ds", []byte("fake"), false)
	if err == nil || err.Error() != "decrypt failed - wrong keys" {
		t.Fatalf("expected the sidecar's own error message, got %v", err)
	}
}

func TestUnavailableClientErrors(t *testing.T) {
	c := NewClient("")
	if c.Available() {
		t.Fatal("a blank base URL must report unavailable")
	}
	if _, err := c.Decrypt(context.Background(), ".3ds", []byte("x"), false); err == nil {
		t.Fatal("an unavailable client must refuse rather than dial nothing")
	}
}
