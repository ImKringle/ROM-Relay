package catalog

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
)

type Platform struct {
	ID, Name, Directory string
	Extensions          []string
}

var Platforms = []Platform{
	{"gb", "Game Boy", "gb", []string{".gb"}},
	{"gbc", "Game Boy Color", "gbc", []string{".gbc"}},
	{"gba", "Game Boy Advance", "gba", []string{".gba"}},
	{"nds", "Nintendo DS/DSi", "nds", []string{".nds", ".ds"}},
	{"3ds", "Nintendo 3DS", "3ds", []string{".3ds", ".cci", ".cia"}},
	{"nes", "Nintendo Entertainment System", "nes", []string{".nes"}},
	{"snes", "Super Nintendo", "snes", []string{".smc", ".sfc", ".fig"}},
	{"n64", "Nintendo 64", "n64", []string{".n64", ".z64"}},
	{"genesis", "Sega Genesis", "genesis", []string{".gen", ".bin", ".md", ".smd"}},
	{"gc", "Nintendo GameCube", "gc", []string{".iso", ".gcm", ".rvz", ".gcz", ".ciso"}},
	{"ps1", "PlayStation", "ps1", []string{".chd", ".cue", ".bin", ".pbp", ".m3u"}},
	{"ps2", "PlayStation 2", "ps2", []string{".iso", ".chd", ".cso"}},
	{"psp", "PlayStation Portable", "psp", []string{".iso", ".cso", ".pbp"}},
	{"switch", "Nintendo Switch", "switch", []string{".nsp", ".xci", ".nsz", ".xcz"}},
}

func PlatformByID(id string) (Platform, bool) {
	for _, p := range Platforms {
		if p.ID == id {
			return p, true
		}
	}
	return Platform{}, false
}

// PlatformByFilename resolves a platform from the extension alone. An
// extension several platforms share (.iso, .bin, .chd, .cso, .pbp) is
// ambiguous and returns false: guessing would file a PS2 disc as GameCube.
// Use PlatformByPath, or the source's own platform, for those.
func PlatformByFilename(name string) (Platform, bool) {
	ext := strings.ToLower(filepath.Ext(filepath.Base(name)))
	var found []Platform
	for _, p := range Platforms {
		if p.Has(ext) {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		return Platform{}, false
	}
	return found[0], true
}

// Has reports whether ext (".iso", lower-case) is one of p's extensions.
func (p Platform) Has(ext string) bool {
	for _, allowed := range p.Extensions {
		if ext == allowed {
			return true
		}
	}
	return false
}

// PlatformByPath is PlatformByFilename plus a tie-break for shared
// extensions: a folder in the path naming the platform ("ps2/",
// "PlayStation 2/", "Sega Genesis/") decides. Torrents and archives
// usually carry one.
func PlatformByPath(name string) (Platform, bool) {
	if p, ok := PlatformByFilename(name); ok {
		return p, true
	}
	clean := strings.ToLower(strings.ReplaceAll(name, `\`, "/"))
	ext := strings.ToLower(filepath.Ext(clean))
	dirs := strings.Split(clean, "/")
	dirs = dirs[:len(dirs)-1]
	// The longest matching name wins, so "PlayStation 2/" is PS2, not PS1.
	var best Platform
	bestLen := 0
	for _, p := range Platforms {
		if !p.Has(ext) {
			continue
		}
		for _, d := range dirs {
			n := 0
			if d == p.Directory || d == p.ID {
				n = len(d)
			} else if strings.Contains(d, strings.ToLower(p.Name)) {
				n = len(p.Name)
			}
			if n > bestLen {
				best, bestLen = p, n
			}
		}
	}
	return best, bestLen > 0
}

// ValidateROMUpload performs bounded structural checks before an untrusted
// browser upload can enter the in-memory patch engine.
func ValidateROMUpload(name string, b []byte) (Platform, error) {
	p, ok := PlatformByFilename(name)
	if !ok {
		return Platform{}, errors.New("unsupported base ROM extension")
	}
	minimum := map[string]int{"gb": 0x150, "gbc": 0x150, "gba": 0xc0, "nds": 0x200, "3ds": 0x200, "nes": 16, "snes": 0x8000, "n64": 4, "genesis": 0x200}[p.ID]
	if len(b) < minimum {
		return Platform{}, errors.New("base ROM is too small for its platform")
	}
	switch p.ID {
	case "nes":
		if string(b[:4]) != "NES\x1a" {
			return Platform{}, errors.New("invalid NES header")
		}
	case "n64":
		magic := binary.BigEndian.Uint32(b[:4])
		if magic != 0x80371240 && magic != 0x37804012 && magic != 0x40123780 {
			return Platform{}, errors.New("invalid N64 byte order/header")
		}
	case "3ds":
		ext := strings.ToLower(filepath.Ext(name))
		if (ext == ".3ds" || ext == ".cci") && string(b[0x100:0x104]) != "NCSD" {
			return Platform{}, errors.New("invalid 3DS NCSD header")
		}
		if ext == ".cia" && binary.LittleEndian.Uint32(b[:4]) != 0x2020 {
			return Platform{}, errors.New("invalid CIA header")
		}
	}
	return p, nil
}

func DetectPlatform(root, path string) (Platform, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return Platform{}, false
	}
	parts := strings.FieldsFunc(filepath.ToSlash(rel), func(r rune) bool { return r == '/' })
	if len(parts) < 2 {
		return Platform{}, false
	}
	for _, p := range Platforms {
		if !strings.EqualFold(parts[0], p.Directory) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(path))
		for _, allowed := range p.Extensions {
			if ext == allowed {
				return p, true
			}
		}
	}
	return Platform{}, false
}
