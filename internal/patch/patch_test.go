package patch

import (
	"bytes"
	"context"
	"testing"
)

func TestIPS(t *testing.T) {
	src := []byte("abcdef")
	p := []byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 2, 'X', 'Y', 0, 0, 5, 0, 0, 0, 3, '!', 'E', 'O', 'F'}
	got, err := IPS(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("aXYde!!!")) {
		t.Fatalf("%q", got)
	}
	if bytes.Equal(src, got) {
		t.Fatal("source mutated")
	}
}
func TestEmbeddedEngineIPSIsolation(t *testing.T) {
	src := []byte("abcdef")
	p := []byte{'P', 'A', 'T', 'C', 'H', 0, 0, 1, 0, 2, 'X', 'Y', 'E', 'O', 'F'}
	got, err := ApplyEmbedded(context.Background(), src, p, "base.gba", "change.ips")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Output, []byte("aXYdef")) {
		t.Fatalf("%q", got.Output)
	}
	if got.HasSourceChecksum {
		t.Fatal("IPS unexpectedly verified source")
	}
	if !bytes.Equal(src, []byte("abcdef")) {
		t.Fatal("source mutated")
	}
}
func TestValidateOutputName(t *testing.T) {
	if _, err := ValidateOutputName("hack.gba", ".gba"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"../x.gba", "CON.gba", "wrong.nds", "trail. "} {
		if _, err := ValidateOutputName(n, ".gba"); err == nil {
			t.Errorf("accepted %q", n)
		}
	}
}
