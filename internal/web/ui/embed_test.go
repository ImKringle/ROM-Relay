package ui

import (
	"io/fs"
	"testing"
)

// The built assets are committed so that building ROM Relay never needs Node.
// This is what stops that convenience from silently shipping stale CSS or JS.
func TestBuiltAssetsAreUpToDateWithTheirSources(t *testing.T) {
	problems, err := ManifestMismatch()
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Errorf("stale build output: %s", problem)
	}
	if len(problems) > 0 {
		t.Fatal("internal/web/ui/dist is out of date, run: npm run build")
	}
}

func TestTheBinaryCarriesTheAssetsItServes(t *testing.T) {
	for _, name := range []string{"app.css", "app.js"} {
		data, err := fs.ReadFile(Assets(), name)
		if err != nil {
			t.Fatalf("%s must be embedded, or the pages load unstyled: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}
