package torrent

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, _ := os.Create(p)
	w := zip.NewWriter(f)
	for name, body := range files {
		fw, _ := w.Create(name)
		fw.Write([]byte(body))
	}
	w.Close()
	f.Close()
	return p
}

func TestInspectAndExtract(t *testing.T) {
	// Nested folders, ROMs and readmes only: clean.
	clean := writeZip(t, map[string]string{"Set/GBA/Emerald.gba": "rom", "Set/readme.txt": "hi", "Set/DS/sub/Black.nds": "nds"})
	if _, ok, err := Inspect(clean); err != nil || !ok {
		t.Fatalf("ROMs+readme in nested folders must be clean: ok=%v err=%v", ok, err)
	}
	// Anything else needs review, as does an archive with no ROM at all.
	for name, files := range map[string]map[string]string{
		"exe":    {"Emerald.gba": "rom", "crack.exe": "x"},
		"no rom": {"readme.txt": "hi"},
	} {
		if _, ok, _ := Inspect(writeZip(t, files)); ok {
			t.Fatalf("%s: must require review", name)
		}
	}

	lib := t.TempDir()
	os.MkdirAll(filepath.Join(lib, "nds"), 0o755)
	os.WriteFile(filepath.Join(lib, "nds", "Black.nds"), []byte("keep"), 0o644)
	hostile := writeZip(t, map[string]string{"Set/GBA/Emerald.gba": "rom", "../../evil.gba": "x", "Set/DS/Black.nds": "new", "Set/readme.txt": "hi"})
	written, err := Extract(hostile, lib)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 { // Emerald + evil (flattened, contained); Black skipped as existing
		t.Fatalf("written = %v", written)
	}
	if b, _ := os.ReadFile(filepath.Join(lib, "gba", "Emerald.gba")); string(b) != "rom" {
		t.Fatal("ROM must be flattened into its platform folder")
	}
	if _, err := os.Stat(filepath.Join(lib, "gba", "evil.gba")); err != nil {
		t.Fatal("../ entries must be contained in the platform folder")
	}
	if b, _ := os.ReadFile(filepath.Join(lib, "nds", "Black.nds")); string(b) != "keep" {
		t.Fatal("existing library files must not be overwritten")
	}
	if _, err := os.Stat(filepath.Join(lib, "readme.txt")); err == nil {
		t.Fatal("readmes are allowed but not extracted")
	}
}
