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
}

func PlatformByID(id string) (Platform, bool) {
	for _, p := range Platforms {
		if p.ID == id {
			return p, true
		}
	}
	return Platform{}, false
}

func PlatformByFilename(name string) (Platform, bool) {
	ext := strings.ToLower(filepath.Ext(filepath.Base(name)))
	for _, p := range Platforms {
		for _, allowed := range p.Extensions {
			if ext == allowed {
				return p, true
			}
		}
	}
	return Platform{}, false
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
