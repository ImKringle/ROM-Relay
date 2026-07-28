package web

import (
	"testing"

	"romrelay/internal/catalog"
)

// toolIDs flattens the tree (groups and their children) into one list of IDs,
// since most callers only care whether a capability is reachable at all, not
// whether it sits at the top level or inside a group.
func toolIDs(list []Tool) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.ID)
		out = append(out, toolIDs(t.Children)...)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

func TestAnAdminSeesAdminTools(t *testing.T) {
	got := toolIDs(ToolsFor(&catalog.User{Role: "admin"}))
	for _, want := range []string{"library", "pokedex", "saves", "admin"} {
		if !contains(got, want) {
			t.Errorf("an admin must see the %q tool: got %v", want, got)
		}
	}
}

func TestAPlainUserIsNotOfferedAdminTools(t *testing.T) {
	got := toolIDs(ToolsFor(&catalog.User{Role: "user"}))
	if contains(got, "admin") {
		t.Fatalf("admin must not be offered to a non-admin: got %v", got)
	}
	if !contains(got, "pokedex") {
		t.Fatalf("a signed-in user must still see their own tools: got %v", got)
	}
}

func TestASignedOutVisitorSeesOnlyPublicTools(t *testing.T) {
	got := toolIDs(ToolsFor(nil))
	if contains(got, "admin") || contains(got, "saves") || contains(got, "pokedex") {
		t.Fatalf("account tools must not be offered to a signed-out visitor: got %v", got)
	}
	if !contains(got, "library") {
		t.Fatalf("the library is public when no token is set: got %v", got)
	}
}

func TestEveryToolIsFullyDescribed(t *testing.T) {
	// Every tool needs a name, icon, and description or it would render as an
	// unlabelled nav entry. A group has no Route of its own (its children do);
	// a leaf tool must have one or it renders a dead link.
	var check func([]Tool)
	check = func(list []Tool) {
		for _, tool := range list {
			if tool.ID == "" || tool.Name == "" || tool.Description == "" || tool.Icon == "" {
				t.Errorf("tool %+v is missing a field the waffle renders", tool)
			}
			if len(tool.Children) == 0 && tool.Route == "" {
				t.Errorf("leaf tool %+v has no route", tool)
			}
			if len(tool.Children) > 0 && tool.Route != "" {
				t.Errorf("group tool %+v should not have its own route: children carry the routes", tool)
			}
			check(tool.Children)
		}
	}
	check(ToolsFor(&catalog.User{Role: "admin"}))
}

// TestPokemonToolsGroupsPokedexAndTrainers pins the nav shell after the
// 2026-07-18 UI Audit Phase 5 split: Trainers left the Pokédex page for its
// own route, so "Pokémon Tools" now groups children (Pokédex, Trainers,
// PokeHome, Editor, Type Chart, and ROM Randomizer, the latter added
// alongside the ROM Randomizer tool) and the base template renders it as a
// dropdown rather than a plain link.
// "Admin" stays a plain link (Route "/admin") rather than a group: a
// dropdown/disclosure nav entry ("the waffle") was explicitly rejected in
// favor of one link to a real landing page, so it never has Children at all.
func TestPokemonToolsGroupsPokedexAndTrainers(t *testing.T) {
	list := ToolsFor(&catalog.User{Role: "admin"})
	var pokemonTools, admin *Tool
	for i := range list {
		switch list[i].ID {
		case "pokemon-tools":
			pokemonTools = &list[i]
		case "admin":
			admin = &list[i]
		}
	}
	if pokemonTools == nil || len(pokemonTools.Children) != 6 ||
		pokemonTools.Children[0].ID != "pokedex" || pokemonTools.Children[1].ID != "trainers" ||
		pokemonTools.Children[2].ID != "pokehome" || pokemonTools.Children[3].ID != "pokemon-editor" ||
		pokemonTools.Children[4].ID != "type-chart" || pokemonTools.Children[5].ID != "rom-randomizer" {
		t.Fatalf("Pokémon Tools must group Pokédex, Trainers, PokeHome, Editor, Type Chart, then ROM Randomizer: %#v", pokemonTools)
	}
	if admin == nil || len(admin.Children) != 0 || admin.Route != "/admin" {
		t.Fatalf("Admin must be a plain link to /admin, not a group: %#v", admin)
	}
}

// TestAGroupWithNoVisibleChildrenIsDropped pins the case a plain user must
// never see an Admin entry that leads nowhere.
func TestAGroupWithNoVisibleChildrenIsDropped(t *testing.T) {
	got := toolIDs(ToolsFor(&catalog.User{Role: "user"}))
	if contains(got, "admin") {
		t.Fatalf("a non-admin must not see the Admin tool: got %v", got)
	}
}
