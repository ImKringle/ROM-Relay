package web

import (
	"net/url"
	"strings"
	"testing"

	"romrelay/internal/catalog"
)

func fixtureGames() []catalog.Game {
	return []catalog.Game{
		{ID: "1", Title: "Pokemon Emerald", Platform: "gba", Region: "USA"},
		{ID: "2", Title: "Pokemon HeartGold", Platform: "nds", Region: "USA"},
		{ID: "3", Title: "Mother 3", Platform: "gba", Region: "Japan"},
		{ID: "4", Title: "Mariomon", Platform: "gba", Region: "USA", ROMPatch: true},
		{ID: "5", Title: "Zelda OoT 3D", Platform: "3ds", Region: "Europe",
			Artifacts: []catalog.Artifact{{Encryption: "encrypted"}}},
	}
}

func filteredTitles(t *testing.T, query string) []string {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	got := filterGames(fixtureGames(), parseCatalogFilter(values))
	titles := make([]string, 0, len(got))
	for _, g := range got {
		titles = append(titles, g.Title)
	}
	return titles
}

// The bare library lists real games; patched builds are held back until asked
// for, so the landing page is not every hack of every game.
func TestTheBareLibraryHoldsBackPatchedBuilds(t *testing.T) {
	got := filteredTitles(t, "")
	if len(got) != 4 {
		t.Fatalf("the unfiltered lander shows originals only: got %v", got)
	}
	for _, title := range got {
		if title == "Mariomon" {
			t.Fatalf("a patched build must not appear on the bare lander: got %v", got)
		}
	}
}

// Choosing a platform alone is not the same as asking for patched builds: a
// temporary one (not retained) stays hidden until its Status chip, a legacy
// ?platform=patched link, or its exact name reveals it.
func TestChoosingAPlatformAloneDoesNotRevealTemporaryPatchedBuilds(t *testing.T) {
	got := filteredTitles(t, "platform=gba")
	for _, title := range got {
		if title == "Mariomon" {
			t.Fatalf("a platform filter alone must not reveal a temporary patched build: got %v", got)
		}
	}
}

// Searching its exact title (or file name) does reveal it: someone who already
// knows what they are looking for should still be able to find it.
func TestExactNameRevealsATemporaryPatchedBuild(t *testing.T) {
	got := filteredTitles(t, "q=Mariomon")
	if len(got) != 1 || got[0] != "Mariomon" {
		t.Fatalf("an exact title search must reveal the temporary patched build: got %v", got)
	}
}

// A retained (permanent) patched build is not temporary, so it shows up like
// any other game, tagged, with no filter needed.
func TestARetainedPatchedBuildIsNotHeldBack(t *testing.T) {
	games := fixtureGames()
	for i := range games {
		if games[i].Title == "Mariomon" {
			games[i].Artifacts = []catalog.Artifact{{Patched: true, Retain: true}}
		}
	}
	got := filterGames(games, parseCatalogFilter(url.Values{}))
	found := false
	for _, g := range got {
		found = found || g.Title == "Mariomon"
	}
	if !found {
		t.Fatalf("a retained patched build must appear on the bare lander: got %v", got)
	}
}

// Links, bookmarks, and QR codes from before status became its own facet still
// carry platform=patched.
func TestTheOldPatchedPseudoPlatformStillWorks(t *testing.T) {
	got := filteredTitles(t, "platform=patched")
	if len(got) != 1 || got[0] != "Mariomon" {
		t.Fatalf("an old ?platform=patched link must still show patched builds only: got %v", got)
	}
}

func TestPlatformFilterAcceptsMoreThanOnePlatform(t *testing.T) {
	// 3 originals across gba+nds; Mariomon (gba, temporary patched) stays held
	// back since a platform filter alone is not an explicit ask for it.
	got := filteredTitles(t, "platform=gba&platform=nds")
	if len(got) != 3 {
		t.Fatalf("checking two platforms shows both, not one: got %v", got)
	}
	for _, title := range got {
		if title == "Zelda OoT 3D" {
			t.Fatalf("a platform that was not checked must not appear: got %v", got)
		}
	}
}

func TestStatusSeparatesPatchedFromOriginal(t *testing.T) {
	patched := filteredTitles(t, "status=patched")
	if len(patched) != 1 || patched[0] != "Mariomon" {
		t.Fatalf("status=patched must show only patched builds: got %v", patched)
	}
	original := filteredTitles(t, "status=original")
	for _, title := range original {
		if title == "Mariomon" {
			t.Fatalf("status=original must exclude patched builds: got %v", original)
		}
	}
	if len(original) != 4 {
		t.Fatalf("status=original must show the rest: got %v", original)
	}
}

func TestFiltersCombine(t *testing.T) {
	// gba alone matches three; adding the search and the status has to narrow it
	// to one rather than either term winning outright.
	got := filteredTitles(t, "platform=gba&status=original&q=pokemon")
	if len(got) != 1 || got[0] != "Pokemon Emerald" {
		t.Fatalf("filters must narrow together, not replace each other: got %v", got)
	}
}

func TestSearchMatchesRegardlessOfAccentsAndCase(t *testing.T) {
	got := filteredTitles(t, "q=pok%C3%A9mon+emerald")
	if len(got) != 1 || got[0] != "Pokemon Emerald" {
		t.Fatalf("search must fold accents and case as it did before: got %v", got)
	}
}

func TestAnUnknownFilterValueIsIgnoredRatherThanEmptyingTheCatalog(t *testing.T) {
	got := filteredTitles(t, "platform=dreamcast")
	if len(got) != 0 {
		t.Fatalf("an unknown platform legitimately matches nothing: got %v", got)
	}
	// But a junk *facet* must not silently hide the catalog.
	got = filteredTitles(t, "colour=blue")
	if len(got) != 4 {
		t.Fatalf("an unrecognised filter must be ignored, not applied: got %v", got)
	}
}

// The Region facet is gone: a self-hosted library is almost always one region, so
// it was a column of checkboxes that filtered nothing out. Region is still text
// you can search for, which is the part worth keeping.
func TestRegionIsSearchableButIsNotAFacet(t *testing.T) {
	facets := catalogFacets(fixtureGames())
	for _, platform := range facets.Platforms {
		if strings.EqualFold(platform.Label, "USA") || strings.EqualFold(platform.Label, "World") {
			t.Errorf("region must not come back as a facet: %q", platform.Label)
		}
	}
	if got := filteredTitles(t, "q=USA"); len(got) == 0 {
		t.Error("searching for a region must still match the games carrying it")
	}
	// A stale bookmark carrying the retired parameter must be ignored, not applied
	// as a filter nothing matches.
	if got := filteredTitles(t, "region=USA"); len(got) != 4 {
		t.Errorf("an old region link must show the whole catalog, not an empty one: got %v", got)
	}
}

// Show Encrypted defaults off: a game whose only file is encrypted (3DS CIA
// or NDS cart) stays out of the bare Library view until the box is checked.
// A mixed or unencrypted-only game is never held back by it.
func TestShowEncryptedHidesEncryptedOnlyGamesByDefault(t *testing.T) {
	games := []catalog.Game{
		{ID: "1", Title: "Encrypted Only", Platform: "3ds", Encryption: "encrypted"},
		{ID: "2", Title: "Unencrypted Only", Platform: "3ds", Encryption: "unencrypted"},
		{ID: "3", Title: "Mixed", Platform: "3ds", Encryption: "mixed"},
		{ID: "4", Title: "No Crypt Concept", Platform: "gba"},
	}
	bare := filteredTitlesFrom(t, games, "")
	for _, title := range bare {
		if title == "Encrypted Only" {
			t.Fatalf("an encrypted-only game must not appear by default: got %v", bare)
		}
	}
	if len(bare) != 3 {
		t.Fatalf("mixed, unencrypted-only, and no-crypt-concept games must all still show: got %v", bare)
	}
	shown := filteredTitlesFrom(t, games, "show_encrypted=1")
	found := false
	for _, title := range shown {
		found = found || title == "Encrypted Only"
	}
	if !found {
		t.Fatalf("show_encrypted=1 must reveal the encrypted-only game: got %v", shown)
	}
}

func filteredTitlesFrom(t *testing.T, games []catalog.Game, query string) []string {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	got := filterGames(games, parseCatalogFilter(values))
	titles := make([]string, 0, len(got))
	for _, g := range got {
		titles = append(titles, g.Title)
	}
	return titles
}

func TestFacetsAreBuiltFromTheCatalogThatExists(t *testing.T) {
	facets := catalogFacets(fixtureGames())
	if len(facets.Platforms) != 3 {
		t.Fatalf("platform facets come from the games present: got %v", facets.Platforms)
	}
	// Offering a filter that matches nothing is a dead control.
	for _, platform := range facets.Platforms {
		if platform.Count == 0 {
			t.Errorf("facet %q is offered but matches no game", platform.Value)
		}
	}
}
