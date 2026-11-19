package torrent

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"romrelay/internal/catalog"
)

// archiveExts are the archive formats grabs unpack.
var archiveExts = map[string]bool{".zip": true, ".7z": true}

// IsArchive reports whether name is an archive Inspect/Extract can open.
func IsArchive(name string) bool {
	return archiveExts[strings.ToLower(path.Ext(strings.ReplaceAll(name, `\`, "/")))]
}

// archiveFile is one entry of a zip or 7z, behind one shape.
type archiveFile struct {
	name string
	size int64
	dir  bool
	open func() (io.ReadCloser, error)
}

// openArchive lists a .zip or .7z (by extension). Both readers verify each
// entry's CRC at EOF, so a corrupt or incomplete download fails the copy.
func openArchive(archive string) ([]archiveFile, io.Closer, error) {
	var out []archiveFile
	if strings.EqualFold(filepath.Ext(archive), ".7z") {
		r, err := sevenzip.OpenReader(archive)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range r.File {
			out = append(out, archiveFile{f.Name, int64(f.UncompressedSize), f.FileInfo().IsDir(), f.Open})
		}
		return out, r, nil
	}
	r, err := zip.OpenReader(archive)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range r.File {
		out = append(out, archiveFile{f.Name, int64(f.UncompressedSize64), f.FileInfo().IsDir(), f.Open})
	}
	return out, r, nil
}

// Entry is one file inside a downloaded archive, as shown for inspection.
type Entry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Platform string `json:"platform,omitempty"`
	Expected bool   `json:"expected"`
}

// readmeExts are harmless extras that ROM archives commonly carry. They are
// allowed without review but not extracted.
var readmeExts = map[string]bool{".txt": true, ".nfo": true}

// Inspect lists an archive's files. clean is true when it holds at least one
// ROM and nothing besides ROMs and readmes (any folder nesting is fine), which
// is what lets it extract without a human looking first.
func Inspect(archive string) (entries []Entry, clean bool, err error) {
	return InspectFor(archive, nil)
}

// entryPlatform places one archive entry: by extension or its folders, else
// by hint (the platform the archive's source says it is) when the hint
// accepts that extension.
func entryPlatform(name string, hint *catalog.Platform) (catalog.Platform, bool) {
	if p, ok := catalog.PlatformByPath(name); ok {
		return p, true
	}
	if hint != nil && hint.Has(strings.ToLower(path.Ext(name))) {
		return *hint, true
	}
	return catalog.Platform{}, false
}

// InspectFor is Inspect with a platform hint for shared extensions (.iso).
func InspectFor(archive string, hint *catalog.Platform) (entries []Entry, clean bool, err error) {
	files, closer, err := openArchive(archive)
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()
	clean = true
	roms := 0
	for _, f := range files {
		if f.dir {
			continue
		}
		e := Entry{Name: f.name, Size: f.size}
		if p, ok := entryPlatform(f.name, hint); ok {
			e.Platform, e.Expected = p.ID, true
			roms++
		} else {
			e.Expected = readmeExts[strings.ToLower(path.Ext(f.name))]
		}
		clean = clean && e.Expected
		entries = append(entries, e)
	}
	return entries, clean && roms > 0, nil
}

// Extract writes every ROM in the archive to libraryDir/<platform>/<file>,
// flattening the archive's folders (the catalog places games by top-level
// platform folder and extension). Non-ROM files are skipped, as are ROMs that
// already exist in the library. Each file goes through a temp file so a CRC
// failure (a corrupt or still-incomplete download) never leaves a partial ROM.
func Extract(archive, libraryDir string) (written []string, err error) {
	return ExtractFor(archive, libraryDir, nil)
}

// ExtractFor is Extract with a platform hint for shared extensions (.iso).
func ExtractFor(archive, libraryDir string, hint *catalog.Platform) (written []string, err error) {
	files, closer, err := openArchive(archive)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	for _, f := range files {
		p, ok := entryPlatform(f.name, hint)
		if !ok || f.dir {
			continue
		}
		// path.Base on the archive's own "/" separators; this also discards
		// any "../" a hostile archive might carry.
		name := path.Base(strings.ReplaceAll(f.name, `\`, "/"))
		dest := filepath.Join(libraryDir, p.Directory, name)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		if err := extractOne(f, dest); err != nil {
			return written, errors.New(f.name + ": " + err.Error())
		}
		written = append(written, dest)
	}
	return written, nil
}

func extractOne(f archiveFile, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	src, err := f.open()
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".extract-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, src) // the reader checks the CRC at EOF
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// TempRoot is where grabs not marked "retain" are stored: an external
// artifact root under the library, so they expire like patch output.
func TempRoot(library string) string { return filepath.Join(library, "external", "torrent") }
