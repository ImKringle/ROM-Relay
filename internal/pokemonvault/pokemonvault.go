// Package pokemonvault stores every ingested Pokémon as a standalone .pkm
// file on disk, keyed by owner and instance id. The database holds the same
// bytes too (pokemon_instances.pokemon_binary), for querying; this package
// exists so a single Pokémon can be grabbed directly, by an admin, or by the
// real PKHeX desktop app, without any binary conversion, and so PokeHome
// deposits survive independently of their source save.
package pokemonvault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidIdentifier reports an owner or instance id that is empty or not a
// plain name. Both are server-generated hex, so a rejected one means a bug or
// an attack, never ordinary user input.
var ErrInvalidIdentifier = errors.New("invalid pokemon identifier")

// Vault stores and retrieves one Pokémon's raw entity bytes as a .pkm file.
type Vault interface {
	Put(ownerID, instanceID string, data []byte) error
	Open(ownerID, instanceID string) (io.ReadSeekCloser, error)
	Delete(ownerID, instanceID string) error
}

// DirectoryVault keeps files under root as root/<owner-id>/<instance-id>.pkm.
type DirectoryVault struct{ root string }

func NewDirectoryVault(root string) *DirectoryVault { return &DirectoryVault{root: root} }

func (v *DirectoryVault) Put(ownerID, instanceID string, data []byte) error {
	path, err := v.path(ownerID, instanceID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (v *DirectoryVault) Open(ownerID, instanceID string) (io.ReadSeekCloser, error) {
	path, err := v.path(ownerID, instanceID)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

// Delete removes a Pokémon's file. Bytes that are already absent are not an
// error: the caller's goal is that the file no longer exists, and a missing
// file must not block removing the database row that points at it.
func (v *DirectoryVault) Delete(ownerID, instanceID string) error {
	path, err := v.path(ownerID, instanceID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// path builds the on-disk location for a Pokémon, rejecting anything that is
// not a single plain path element. Both identifiers are server-generated hex,
// so this is a structural guarantee rather than a filter: no user-supplied
// text can reach the filesystem, and no id can traverse out of the vault root.
func (v *DirectoryVault) path(ownerID, instanceID string) (string, error) {
	for _, id := range []string{ownerID, instanceID} {
		if id == "" || id != filepath.Base(id) || id == "." || id == ".." ||
			strings.ContainsAny(id, `/\`) {
			return "", fmt.Errorf("%w: %q", ErrInvalidIdentifier, id)
		}
	}
	return filepath.Join(v.root, ownerID, instanceID+".pkm"), nil
}
