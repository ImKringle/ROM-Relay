package decryptor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAvailabilityRequiresCompleteReduxLayout(t *testing.T) {
	d := t.TempDir()
	m := New(nil, d, "", t.TempDir(), 0)
	if ok, _ := m.Available(); ok {
		t.Fatal("empty driver directory reported ready")
	}
	for _, rel := range []string{scriptName, "bin/ctrtool.exe", "bin/decrypt.exe", "bin/makerom.exe", "bin/seeddb.bin"} {
		p := filepath.Join(d, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if ok, reason := m.Available(); !ok {
		t.Fatalf("complete layout unavailable: %s", reason)
	}
}

func TestBundleCopyRejectsSymlinkAndFindsSingleOutput(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "driver.exe"), []byte("driver"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyBundle(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	original := filepath.Join(stage, "game.cia")
	os.WriteFile(original, []byte("source"), 0600)
	want := filepath.Join(stage, "game Game-decrypted.cia")
	os.WriteFile(want, []byte("result"), 0600)
	got, err := findOutput(stage, original)
	if err != nil || got != want {
		t.Fatalf("findOutput=%q, %v", got, err)
	}
}
