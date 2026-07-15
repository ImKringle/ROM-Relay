package integration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"romrelay/internal/catalog"
)

// Opt-in audit for an operator library. It reads the generated cache and
// reports exact-hash versus conservative exact-name provider coverage without
// copying or modifying ROM bytes.
func TestOperatorMetadataCoverage(t *testing.T) {
	root := os.Getenv("ROMRELAY_FIXTURE_ROOT")
	if root == "" {
		t.Skip("ROMRELAY_FIXTURE_ROOT is not set")
	}
	store, err := catalog.Open(filepath.Join(root, "romrelay.db"), filepath.Join(root, "platforms"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	games, err := store.Games(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type stats struct{ Total, Described, Art, Hash, Name, Missing int }
	byPlatform := map[string]*stats{}
	for _, g := range games {
		s := byPlatform[g.Platform]
		if s == nil {
			s = &stats{}
			byPlatform[g.Platform] = s
		}
		s.Total++
		if g.Description != "" {
			s.Described++
		}
		if g.ArtworkURL != "" {
			s.Art++
		}
		matched := false
		for _, a := range g.Artifacts {
			if _, e := store.MatchLibretro(context.Background(), a); e == nil {
				s.Hash++
				matched = true
				break
			} else if !errors.Is(e, sql.ErrNoRows) {
				t.Fatal(e)
			}
		}
		if matched {
			continue
		}
		names := []string{g.Title}
		for _, a := range g.Artifacts {
			names = append(names, a.Name)
		}
		if _, e := store.MatchLibretroName(context.Background(), g.Platform, names...); e == nil {
			s.Name++
		} else if errors.Is(e, sql.ErrNoRows) {
			s.Missing++
		} else {
			t.Fatal(e)
		}
	}
	for platform, s := range byPlatform {
		t.Logf("%s total=%d described=%d art=%d hash=%d exact_name=%d missing=%d", platform, s.Total, s.Described, s.Art, s.Hash, s.Name, s.Missing)
	}
}
