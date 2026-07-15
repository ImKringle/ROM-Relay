package presentation

import (
	"bytes"
	"strings"
	"testing"
)

func TestThemeCopiesAndDeterministicRedirectOutput(t *testing.T) {
	for _, tc := range []struct{ theme, want, not string }{{"default", "[OK] Catalog ready", "corn crib"}, {"corn", "[🌱] Corn crib stocked", "Catalog ready"}} {
		var out, err bytes.Buffer
		p := New(tc.theme, &out, &err)
		p.Success("Catalog ready", "Corn crib stocked")
		p.Summary("ROM Relay startup", "Corn stand startup", Row{"Theme", tc.theme})
		got := out.String()
		if !strings.Contains(got, tc.want) || strings.Contains(got, tc.not) {
			t.Fatalf("theme %s output %q", tc.theme, got)
		}
		if strings.Contains(got, "\x1b[") {
			t.Fatalf("redirected output contains ANSI: %q", got)
		}
	}
}

func TestMetadataProgressNamesEveryROMWhenRedirected(t *testing.T) {
	var out, err bytes.Buffer
	p := New("default", &out, &err)
	m := p.NewMetadata()
	m.Update("IGDB", "a", "Pokemon X", "querying", 0, 2)
	m.Update("IGDB", "a", "Pokemon X", "matched", 1, 2)
	m.Update("IGDB", "b", "Pokemon Y", "no exact match", 2, 2)
	m.Stop(false)
	got := out.String()
	for _, want := range []string{"Pokemon X · querying", "Pokemon X · matched", "Pokemon Y · no exact match", "Metadata 2/2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
