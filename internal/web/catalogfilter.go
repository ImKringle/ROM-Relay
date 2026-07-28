package web

import (
	"net/url"
	"sort"
	"strings"

	"romrelay/internal/catalog"
)

// catalogFilter is the catalog view expressed as data. It is built from the URL
// and nothing else, which is what lets a filtered view be linked, bookmarked, and
// reached with the browser's back button.
type catalogFilter struct {
	Query string
	// Each facet is a set of accepted values. An empty set accepts everything, so
	// "nothing ticked" and "everything ticked" mean the same thing.
	Platforms  map[string]bool
	Statuses   map[string]bool
	Encryption string
	// ShowEncrypted is the Library page's default-off safety net: unless set,
	// a game whose only artifacts are encrypted (no decrypted/plain variant)
	// stays out of the main view. It's a checkbox, not a facet, and only
	// applies here: FTPd and the Batch Installer never call filterGames, so
	// they always see everything.
	ShowEncrypted bool
}

// facetValue is one checkbox: what it filters, how it reads, how many games it
// matches, and whether it is applied.
type facetValue struct {
	Value   string
	Label   string
	Count   int
	Checked bool
}

type catalogFacetSet struct {
	Platforms []facetValue
	Statuses  []facetValue
	// HasEncryption gates the Show Encrypted checkbox: true when at least one
	// game in view carries an encryption state (3DS CIA or NDS cart). Unlike
	// Platforms/Statuses this isn't a multi-value facet — encryption has
	// exactly one control (the ShowEncrypted checkbox on catalogFilter), so
	// there's nothing per-value to list or count.
	HasEncryption bool
}

// Any returns whether there is anything to show in the panel at all.
func (f catalogFacetSet) Any() bool {
	return len(f.Platforms) > 0 || len(f.Statuses) > 0 || f.HasEncryption
}

// Active counts the applied filters, for the drawer button's badge.
func (f catalogFilter) Active() int {
	return len(f.Platforms) + len(f.Statuses)
}

// parseCatalogFilter reads the filter out of a query string. Unrecognised
// parameters are ignored: a stray parameter must never blank the catalog.
func parseCatalogFilter(values url.Values) catalogFilter {
	platforms := valueSet(values["platform"])
	statuses := valueSet(values["status"])
	// "patched" was a pseudo-platform before status became its own facet. Old
	// links, bookmarks, and QR codes still carry it, so it is translated rather
	// than broken.
	if platforms["patched"] {
		delete(platforms, "patched")
		if len(platforms) == 0 {
			platforms = nil
		}
		if statuses == nil {
			statuses = map[string]bool{}
		}
		statuses["patched"] = true
	}
	filter := catalogFilter{
		Query:     strings.TrimSpace(values.Get("q")),
		Platforms: platforms,
		Statuses:  statuses,
	}
	if encryption := values.Get("encryption"); encryption == "encrypted" || encryption == "unencrypted" {
		filter.Encryption = encryption
	}
	filter.ShowEncrypted = values.Get("show_encrypted") == "1"
	return filter
}

func valueSet(values []string) map[string]bool {
	set := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// isPermanentPatch reports whether a patched game has been marked to keep
// (retained past its normal expiry), as opposed to a temporary workspace
// result still on the clock to be pruned.
func isPermanentPatch(game catalog.Game) bool {
	for _, artifact := range game.Artifacts {
		if artifact.Patched && artifact.Retain {
			return true
		}
	}
	return false
}

// exactPatchMatch reports whether the query names a temporary patched game
// exactly, by title or by an artifact's file name (with or without its
// extension). It is intentionally exact, not substring: a temporary result is
// found by someone who already knows what they are looking for, not stumbled
// into by a broad search.
func exactPatchMatch(game catalog.Game, query string) bool {
	needle := searchFold(query)
	if needle == "" {
		return false
	}
	if searchFold(game.Title) == needle {
		return true
	}
	for _, artifact := range game.Artifacts {
		if searchFold(artifact.Name) == needle {
			return true
		}
		if searchFold(strings.TrimSuffix(artifact.Name, "."+artifact.Extension)) == needle {
			return true
		}
	}
	return false
}

// filterGames narrows the catalog. Ticking more boxes within one facet widens the
// result; adding another facet narrows it.
//
// A permanent patched build (retained past its normal expiry) is treated like
// any other cataloged game and shown by default with its Patched tag. A
// temporary one, still on the clock to expire, stays out of the library view
// entirely until its exact title or file name is searched for.
func filterGames(games []catalog.Game, filter catalogFilter) []catalog.Game {
	needle := searchFold(filter.Query)
	out := make([]catalog.Game, 0, len(games))
	for _, game := range games {
		if game.ROMPatch && !isPermanentPatch(game) {
			// Explicitly asking for patched builds (the Status chip, or an old
			// ?platform=patched link normalized into it) is as deliberate as
			// naming the file outright, so either one reveals it too.
			explicit := filter.Statuses != nil && filter.Statuses["patched"]
			if !explicit && !exactPatchMatch(game, filter.Query) {
				continue
			}
		}
		if len(filter.Platforms) > 0 && !filter.Platforms[game.Platform] {
			continue
		}
		if len(filter.Statuses) > 0 && !filter.Statuses[statusOf(game)] {
			continue
		}
		if filter.Encryption != "" && !hasEncryption(game, filter.Encryption) {
			continue
		}
		// Only applies when no explicit encryption filter is chosen: picking
		// "encrypted" is itself a request to see encrypted games, so the
		// safety net must not then filter those same games back out.
		if filter.Encryption == "" && !filter.ShowEncrypted && game.Encryption == "encrypted" {
			continue
		}
		if needle != "" && !strings.Contains(searchFold(haystackFor(game)), needle) {
			continue
		}
		out = append(out, game)
	}
	return out
}

func statusOf(game catalog.Game) string {
	if game.ROMPatch {
		return "patched"
	}
	return "original"
}

func hasEncryption(game catalog.Game, encryption string) bool {
	for _, artifact := range game.Artifacts {
		if artifact.Encryption == encryption {
			return true
		}
	}
	return false
}

// haystackFor is everything a search should match: the title people remember,
// plus the metadata and filenames they might search by instead.
func haystackFor(game catalog.Game) string {
	parts := []string{game.Title, game.Developer, game.Publisher, game.Region, game.Genre}
	for _, artifact := range game.Artifacts {
		parts = append(parts, artifact.Name)
	}
	return strings.Join(parts, " ")
}

// catalogFacets builds the panel from the catalog that actually exists, so it
// never offers a checkbox matching nothing.
func catalogFacets(games []catalog.Game, applied ...catalogFilter) catalogFacetSet {
	var filter catalogFilter
	if len(applied) > 0 {
		filter = applied[0]
	}
	platforms, statuses := map[string]int{}, map[string]int{}
	anyEncryption := false
	for _, game := range games {
		platforms[game.Platform]++
		statuses[statusOf(game)]++
		if game.Encryption != "" {
			anyEncryption = true
		}
	}
	return catalogFacetSet{
		Platforms: facetsFrom(platforms, filter.Platforms, platformLabel),
		Statuses:  facetsFrom(statuses, filter.Statuses, statusLabel),
		// Offered only when at least one game actually carries an encryption
		// state (3DS CIA or NDS cart content), so it's not a dead control for
		// a GBA-only library.
		HasEncryption: anyEncryption,
	}
}

func facetsFrom(counts map[string]int, checked map[string]bool, label func(string) string) []facetValue {
	out := make([]facetValue, 0, len(counts))
	for value, count := range counts {
		out = append(out, facetValue{Value: value, Label: label(value), Count: count, Checked: checked[value]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

// platformLabel prefers the catalog's display name for a platform id.
func platformLabel(id string) string {
	for _, platform := range catalog.Platforms {
		if platform.ID == id {
			return platform.Name
		}
	}
	return strings.ToUpper(id)
}

func statusLabel(status string) string {
	if status == "patched" {
		return "Patched"
	}
	return "Original"
}
