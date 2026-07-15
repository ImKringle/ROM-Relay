package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixedBoot9Discovery(t *testing.T) {
	d := t.TempDir()
	if got := fixedBoot9(d); got != "" {
		t.Fatalf("unexpected %q", got)
	}
	protected := filepath.Join(d, "boot9_prot.bin")
	os.WriteFile(protected, []byte("x"), 0600)
	if got := fixedBoot9(d); got != protected {
		t.Fatalf("got %q", got)
	}
	full := filepath.Join(d, "boot9.bin")
	os.WriteFile(full, []byte("y"), 0600)
	if got := fixedBoot9(d); got != full {
		t.Fatalf("full dump should take precedence: %q", got)
	}
}
