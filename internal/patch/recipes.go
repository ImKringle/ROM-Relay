package patch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"romrelay/internal/catalog"
)

type recipeManifest struct {
	Title          string `json:"title"`
	Patch          string `json:"patch"`
	OutputFilename string `json:"output_filename"`
	BaseSHA256     string `json:"base_sha256"`
	BaseCRC32      string `json:"base_crc32"`
	Base           struct {
		SHA256 string `json:"sha256"`
		CRC32  string `json:"crc32"`
	} `json:"base"`
}

func (m *Manager) ScanRecipes(ctx context.Context) error {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	var recipes []catalog.PatchRecipe
	for _, platform := range catalog.Platforms {
		root := filepath.Join(m.cfg.LibraryDirectory, platform.Directory, "patches")
		if err := os.MkdirAll(root, 0755); err != nil {
			return err
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if !supportedPatchExtensions[ext] {
				return nil
			}
			st, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
				return nil
			}
			if st.Size() < 1 || st.Size() > m.cfg.MaxPatchBytes {
				return nil
			}
			hash, err := hashPatch(path, m.cfg.MaxPatchBytes)
			if err != nil {
				return err
			}
			manifest, err := readRecipeManifest(filepath.Dir(path), d.Name())
			if err != nil {
				return err
			}
			inspection, err := inspectRecipe(ctx, path, d.Name(), st.Size(), m.cfg.MaxPatchBytes)
			if err != nil {
				return fmt.Errorf("inspect patch recipe %s: %w", path, err)
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			slug := filepath.ToSlash(rel)
			if slug == "." {
				slug = strings.TrimSuffix(d.Name(), ext)
			}
			title := strings.TrimSpace(manifest.Title)
			if title == "" {
				title = strings.TrimSuffix(d.Name(), ext)
			}
			output := strings.TrimSpace(manifest.OutputFilename)
			if output == "" {
				output = sanitizeRecipeOutput(title) + primaryExtension(platform.ID)
			}
			baseSHA := firstNonEmpty(manifest.BaseSHA256, manifest.Base.SHA256)
			baseCRC := firstNonEmpty(manifest.BaseCRC32, manifest.Base.CRC32)
			recipes = append(recipes, catalog.PatchRecipe{ID: "patch-" + hash[:24], Platform: platform.ID, Slug: slug, Title: title, Path: path, SHA256: hash, Extension: ext, OutputFilename: output, BaseSHA256: normalizeHex(baseSHA), BaseCRC32: normalizeHex(baseCRC), ValidationType: inspection.ValidationType, ValidationValue: normalizeValidation(inspection.ValidationType, inspection.ValidationValue), Size: st.Size(), ModTime: st.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return m.store.ReplacePatchRecipes(ctx, recipes)
}

// UPS and BPS put the source, target, and patch CRC32 values in their final
// twelve bytes. Reading that footer directly avoids constructing a JavaScript
// typed array the size of the patch merely to index a recipe.
func inspectRecipe(ctx context.Context, path, name string, size, max int64) (PatchInspection, error) {
	if err := ctx.Err(); err != nil {
		return PatchInspection{}, err
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".ups" || ext == ".bps" {
		if size < 16 || size > max {
			return PatchInspection{}, errors.New("invalid patch size")
		}
		f, err := os.Open(path)
		if err != nil {
			return PatchInspection{}, err
		}
		defer f.Close()
		var header [4]byte
		var footer [12]byte
		if _, err = io.ReadFull(f, header[:]); err != nil {
			return PatchInspection{}, err
		}
		want := "UPS1"
		if ext == ".bps" {
			want = "BPS1"
		}
		if string(header[:]) != want {
			return PatchInspection{}, fmt.Errorf("invalid %s header", strings.TrimPrefix(ext, "."))
		}
		if _, err = f.ReadAt(footer[:], size-int64(len(footer))); err != nil {
			return PatchInspection{}, err
		}
		return PatchInspection{Format: strings.TrimPrefix(ext, "."), ValidationType: "CRC32", ValidationValue: fmt.Sprintf("%08x", binary.LittleEndian.Uint32(footer[0:4]))}, nil
	}
	b, err := readBounded(path, max)
	if err != nil {
		return PatchInspection{}, err
	}
	return InspectEmbedded(ctx, b, name)
}

func (m *Manager) Recipes(ctx context.Context) ([]catalog.PatchRecipe, error) {
	return m.store.PatchRecipes(ctx)
}

// CleanupTemporaryPatches prunes only supported regular files from the root
// inbox. Permanent platform recipe directories are outside this tree.
func (m *Manager) CleanupTemporaryPatches(now time.Time) (int, []error) {
	if m.cfg.PatchDirectory == "" {
		return 0, nil
	}
	cutoff := now.Add(-m.cfg.Retention)
	deleted := 0
	var failures []error
	err := filepath.WalkDir(m.cfg.PatchDirectory, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !supportedPatchExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() || st.ModTime().After(cutoff) {
			return nil
		}
		if e = os.Remove(path); e != nil {
			failures = append(failures, e)
		} else {
			deleted++
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, err)
	}
	return deleted, failures
}

func readRecipeManifest(dir, patchName string) (recipeManifest, error) {
	var m recipeManifest
	b, err := os.ReadFile(filepath.Join(dir, "recipe.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if len(b) > 64<<10 {
		return m, errors.New("recipe.json exceeds 64 KiB")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid recipe.json: %w", err)
	}
	if m.Patch != "" && !strings.EqualFold(filepath.Base(m.Patch), patchName) {
		return recipeManifest{}, nil
	}
	return m, nil
}

func hashPatch(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, max+1))
	if err != nil {
		return "", err
	}
	if n > max {
		return "", errors.New("patch exceeds configured limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func normalizeValidation(kind, value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if strings.EqualFold(kind, "CRC32") {
		if n, err := strconv.ParseUint(value, 10, 32); err == nil {
			return fmt.Sprintf("%08x", n)
		}
	}
	return normalizeHex(value)
}
func normalizeHex(v string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "0x"))
}
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
func primaryExtension(platform string) string {
	for _, p := range catalog.Platforms {
		if p.ID == platform && len(p.Extensions) > 0 {
			return p.Extensions[0]
		}
	}
	return ".rom"
}
func sanitizeRecipeOutput(s string) string {
	s = strings.TrimSpace(s)
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	s = r.Replace(s)
	if s == "" {
		return "patched-rom"
	}
	return s
}
