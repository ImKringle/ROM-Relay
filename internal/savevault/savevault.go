// Package savevault stores the raw bytes of users' game saves on disk, keyed by
// owner and save id. The database holds only metadata; this package owns the
// files.
package savevault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidIdentifier reports an owner or save id that is empty or that is not
// a plain name. Identifiers are server-generated, so a rejected one means a bug
// or an attack, never ordinary user input.
var ErrInvalidIdentifier = errors.New("invalid save identifier")

// Vault stores and retrieves save bytes. The PokeHOME transfer workflow needs
// exactly these operations: read a destination save, write the injected result.
type Vault interface {
	Put(ownerID, saveID string, data []byte) error
	Open(ownerID, saveID string) (io.ReadSeekCloser, error)
	Delete(ownerID, saveID string) error
}

// DirectoryVault keeps saves under root as root/<owner-id>/<save-id>.sav.
type DirectoryVault struct{ root string }

func NewDirectoryVault(root string) *DirectoryVault { return &DirectoryVault{root: root} }

func (v *DirectoryVault) Put(ownerID, saveID string, data []byte) error {
	path, err := v.path(ownerID, saveID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (v *DirectoryVault) Open(ownerID, saveID string) (io.ReadSeekCloser, error) {
	path, err := v.path(ownerID, saveID)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

// Delete removes a save's bytes. Bytes that are already absent are not an error:
// the caller's goal is that the file no longer exists, and a missing file must
// not block removing the database row that points at it.
func (v *DirectoryVault) Delete(ownerID, saveID string) error {
	path, err := v.path(ownerID, saveID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// path builds the on-disk location for a save, rejecting anything that is not a
// single plain path element. Both identifiers are server-generated hex, so this
// is a structural guarantee rather than a filter: no user-supplied text can
// reach the filesystem, and no id can traverse out of the vault root.
func (v *DirectoryVault) path(ownerID, saveID string) (string, error) {
	for _, id := range []string{ownerID, saveID} {
		if id == "" || id != filepath.Base(id) || id == "." || id == ".." ||
			strings.ContainsAny(id, `/\`) {
			return "", fmt.Errorf("%w: %q", ErrInvalidIdentifier, id)
		}
	}
	return filepath.Join(v.root, ownerID, saveID+".sav"), nil
}
