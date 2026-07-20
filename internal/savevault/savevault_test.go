package savevault

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPutAndOpenRoundTripSaveBytes(t *testing.T) {
	vault := NewDirectoryVault(t.TempDir())
	want := []byte("save bytes")

	if err := vault.Put("owner-1", "save-1", want); err != nil {
		t.Fatal(err)
	}
	reader, err := vault.Open("owner-1", "save-1")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("stored bytes must round-trip: got %q want %q", got, want)
	}
}

func TestVaultRejectsIdentifiersThatEscapeTheRoot(t *testing.T) {
	root := t.TempDir()
	vault := NewDirectoryVault(root)
	outside := filepath.Join(root, "..", "escaped.sav")
	t.Cleanup(func() { os.Remove(outside) })

	for _, id := range []struct{ owner, save string }{
		{"../..", "escaped"},
		{"owner-1", "../../escaped"},
		{"owner-1", "..\\..\\escaped"},
		{"owner-1", "sub/dir"},
		{"", "save-1"},
		{"owner-1", ""},
	} {
		if err := vault.Put(id.owner, id.save, []byte("x")); !errors.Is(err, ErrInvalidIdentifier) {
			t.Fatalf("Put(%q,%q) must be rejected: got %v", id.owner, id.save, err)
		}
		if _, err := vault.Open(id.owner, id.save); !errors.Is(err, ErrInvalidIdentifier) {
			t.Fatalf("Open(%q,%q) must be rejected: got %v", id.owner, id.save, err)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("a rejected identifier still wrote outside the vault root")
	}
}

func TestDeleteRemovesTheSaveBytes(t *testing.T) {
	vault := NewDirectoryVault(t.TempDir())
	if err := vault.Put("owner-1", "save-1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Delete("owner-1", "save-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Open("owner-1", "save-1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bytes must be gone after Delete: got %v", err)
	}
}

func TestDeleteIsQuietWhenBytesAreAlreadyGone(t *testing.T) {
	vault := NewDirectoryVault(t.TempDir())
	// A missing file must not block removing the database row that points at it.
	if err := vault.Delete("owner-1", "never-stored"); err != nil {
		t.Fatalf("deleting absent bytes must succeed: %v", err)
	}
}
