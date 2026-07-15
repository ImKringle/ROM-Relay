package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestinationValidationAndRedaction(t *testing.T) {
	if _, err := validateDestination("gba", "", true); err == nil {
		t.Fatal("kept remote file accepted without destination")
	}
	if _, err := validateDestination("gba", "nds", true); err == nil {
		t.Fatal("cross-platform destination accepted")
	}
	if got, err := validateDestination("3ds", "3ds/cia/unencrypted", true); err != nil || got != "3ds/cia/unencrypted" {
		t.Fatalf("3DS destination=%q %v", got, err)
	}
	o := Options{Host: "host.example", Username: "operator", Password: "very-secret"}
	got := redact("operator very-secret host.example", o)
	if strings.Contains(got, o.Password) || strings.Contains(got, o.Username) || strings.Contains(got, o.Host) {
		t.Fatal("remote diagnostic leaked credentials")
	}
}

func TestRemoteCIAEncryptionChoiceDefaultsUnencrypted(t *testing.T) {
	r := remoteResult{Result: Result{Platform: "3ds", Name: "game.cia"}}
	if got, err := validateEncryptionChoice(r, false, "", ""); err != nil || got != "unencrypted" {
		t.Fatalf("default=%q err=%v", got, err)
	}
	if got, err := validateEncryptionChoice(r, false, "", "encrypted"); err != nil || got != "encrypted" {
		t.Fatalf("explicit=%q err=%v", got, err)
	}
	if got, err := validateEncryptionChoice(r, true, "3ds/cia/unencrypted", "encrypted"); err != nil || got != "unencrypted" {
		t.Fatalf("permanent destination must win: %q %v", got, err)
	}
	nonCIA := remoteResult{Result: Result{Platform: "gba", Name: "game.gba"}}
	if got, err := validateEncryptionChoice(nonCIA, false, "", "encrypted"); err != nil || got != "" {
		t.Fatalf("non-CIA classification=%q %v", got, err)
	}
}

func TestStructuralValidation(t *testing.T) {
	d := t.TempDir()
	valid := filepath.Join(d, "valid.nes")
	invalid := filepath.Join(d, "invalid.nes")
	os.WriteFile(valid, append([]byte("NES\x1a"), make([]byte, 20)...), 0600)
	os.WriteFile(invalid, make([]byte, 24), 0600)
	if err := validateROM(valid, "nes", ".nes"); err != nil {
		t.Fatal(err)
	}
	if err := validateROM(invalid, "nes", ".nes"); err == nil {
		t.Fatal("invalid NES accepted")
	}
}
