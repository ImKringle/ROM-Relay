package web

import (
	"sort"
	"strings"

	"romrelay/internal/catalog"
)

// mergeGames collapses catalog entries that are the same release in different
// file forms, typically a 3DS title present as both an encrypted and an
// unencrypted CIA, which the scanner stores as two games because their
// filenames differ. The merged entry keeps the best-documented game as its
// face and carries every group member's artifacts, so the grid shows one card
// and the game view offers the file forms as options.
//
// Patched builds never merge with their base (the key includes ROMPatch), and
// nothing merges across platforms.
func mergeGames(games []catalog.Game) []catalog.Game {
	byKey := map[string]int{}
	out := make([]catalog.Game, 0, len(games))
	for _, game := range games {
		key := game.Platform + "\x00" + mergeTitleKey(game) + "\x00"
		if game.ROMPatch {
			key += "patched"
		}
		at, seen := byKey[key]
		if !seen {
			byKey[key] = len(out)
			out = append(out, game)
			continue
		}
		merged := out[at]
		if preferGameFace(game, merged) {
			artifacts := merged.Artifacts
			merged = game
			merged.Artifacts = artifacts
		}
		merged.Artifacts = append(merged.Artifacts, game.Artifacts...)
		merged.Encryption = mergedEncryption(merged.Artifacts)
		out[at] = merged
	}
	for i := range out {
		sortArtifacts(out[i].Artifacts)
	}
	return out
}

// mergeSiblings returns the merged view of the group containing id, or false
// when id is in none of them.
func mergeSiblings(games []catalog.Game, id string) (catalog.Game, bool) {
	for _, merged := range mergeGames(games) {
		for _, artifact := range merged.Artifacts {
			if artifact.GameID == id {
				return merged, true
			}
		}
	}
	return catalog.Game{}, false
}

// displayTitle is the title without release tags: "Pokemon - Diamond Version
// (USA) (Rev 5)" reads as "Pokemon - Diamond Version". The full filename is
// still shown with the file itself and still searchable.
func displayTitle(title string) string {
	var kept strings.Builder
	depth := 0
	for _, r := range title {
		switch {
		case r == '(' || r == '[':
			depth++
		case r == ')' || r == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			kept.WriteRune(r)
		}
	}
	cleaned := strings.Join(strings.Fields(kept.String()), " ")
	if cleaned == "" {
		return title
	}
	return cleaned
}

// mergeTitleKey is the identity a release keeps across its file forms: the
// title with release tags, "(USA)", "(World)", "(Rev 2)", "(Legit CIA)",
// stripped, case and punctuation folded. Filename-derived titles and
// metadata titles for the same release both fold to the same key.
func mergeTitleKey(game catalog.Game) string {
	title := game.Title
	var kept strings.Builder
	depth := 0
	for _, r := range title {
		switch {
		case r == '(' || r == '[':
			depth++
		case r == ')' || r == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			kept.WriteRune(r)
		}
	}
	folded := searchFold(kept.String())
	var key strings.Builder
	key.Grow(len(folded))
	for _, r := range folded {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			key.WriteRune(r)
		}
	}
	return key.String()
}

// preferGameFace reports whether a should represent the merged card instead of
// b: artwork beats none, then a real metadata source, then a description.
func preferGameFace(a, b catalog.Game) bool {
	if (a.ArtworkURL != "") != (b.ArtworkURL != "") {
		return a.ArtworkURL != ""
	}
	if (a.MetadataSource != "") != (b.MetadataSource != "") {
		return a.MetadataSource != ""
	}
	return a.Description != "" && b.Description == ""
}

func mergedEncryption(artifacts []catalog.Artifact) string {
	encrypted, unencrypted := false, false
	for _, a := range artifacts {
		switch a.Encryption {
		case "encrypted":
			encrypted = true
		case "unencrypted":
			unencrypted = true
		}
	}
	switch {
	case encrypted && unencrypted:
		return "mixed"
	case encrypted:
		return "encrypted"
	case unencrypted:
		return "unencrypted"
	}
	return ""
}

// sortArtifacts orders a merged variant list the way a visitor decides:
// console-installable encrypted CIAs first, then unencrypted, then the rest,
// alphabetically within each band.
func sortArtifacts(artifacts []catalog.Artifact) {
	rank := func(a catalog.Artifact) int {
		switch a.Encryption {
		case "encrypted":
			return 0
		case "unencrypted":
			return 1
		}
		return 2
	}
	sort.SliceStable(artifacts, func(i, j int) bool {
		if r1, r2 := rank(artifacts[i]), rank(artifacts[j]); r1 != r2 {
			return r1 < r2
		}
		return artifacts[i].Name < artifacts[j].Name
	})
}
