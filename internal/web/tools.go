package web

import (
	"strings"

	"romrelay/internal/catalog"
)

// Access is the least privilege a tool requires. Tools are filtered by it on the
// server, so a tool a user may not use never reaches their browser.
type Access int

const (
	// AccessPublic is reachable by anyone who can reach the site.
	AccessPublic Access = iota
	// AccessUser needs a signed-in account.
	AccessUser
	// AccessAdmin needs the admin role.
	AccessAdmin
)

// Tool is one entry in the app launcher (the waffle). Icon is an inline SVG path
// drawn on a 24x24 grid, which keeps the launcher self-contained: no icon font,
// no image requests, nothing extra for the CSP to allow.
//
// A tool with Children is a group: it has no Route of its own, and exists only
// to gather related tools under one nav entry (e.g. "Pokémon Tools", "Admin").
// The nav renders a group with one visible child as a plain link to that
// child, and a group with several as a dropdown, so a group started for
// future siblings costs nothing until it actually has more than one.
type Tool struct {
	ID          string
	Name        string
	Description string
	Icon        string
	Route       string
	Requires    Access
	Children    []Tool
}

// ActiveFor reports whether the current request path is inside this tool,
// including inside any of its children, a group is "current" when the
// visitor is on one of its children's pages, even though the group itself has
// no route of its own.
func (t Tool) ActiveFor(path string) bool {
	if t.Route != "" && routeActive(path, t.Route) {
		return true
	}
	for _, child := range t.Children {
		if child.ActiveFor(path) {
			return true
		}
	}
	return false
}

func routeActive(path, route string) bool {
	if route == "/" {
		return path == "/"
	}
	return path == route || strings.HasPrefix(path, route+"/")
}

// tools is the registry behind the launcher. A new feature adds one entry here
// (or one child of an existing group) and appears in the waffle; there is no
// navigation markup to hand-edit.
var tools = []Tool{
	{
		ID: "library", Name: "Library", Route: "/", Requires: AccessPublic,
		Description: "Browse and download your ROM collection.",
		Icon:        "M7 8h10a4 4 0 0 1 3.8 5.2l-1 3A2.6 2.6 0 0 1 15.5 17l-1.2-1.3H9.7L8.5 17a2.6 2.6 0 0 1-4.3-.8l-1-3A4 4 0 0 1 7 8Z M7 12h4 M9 10v4",
	},
	{
		ID: "pokemon-tools", Name: "Pokémon Tools", Requires: AccessUser,
		Description: "Everything Pokémon-specific: your Pokédex, trainers, and more to come.",
		Icon:        "M12 3a9 9 0 0 1 9 9H3a9 9 0 0 1 9-9Z M12 21a9 9 0 0 1-9-9h18a9 9 0 0 1-9 9Z M12 9.5a2.5 2.5 0 1 1 0 5 2.5 2.5 0 0 1 0-5Z",
		Children: []Tool{
			{
				ID: "pokedex", Name: "Pokédex", Route: "/pokedex", Requires: AccessUser,
				Description: "Every species you hold or have caught, built from the saves you upload.",
				Icon:        "M12 3a9 9 0 0 1 9 9H3a9 9 0 0 1 9-9Z M12 21a9 9 0 0 1-9-9h18a9 9 0 0 1-9 9Z M12 9.5a2.5 2.5 0 1 1 0 5 2.5 2.5 0 0 1 0-5Z",
			},
			{
				ID: "trainers", Name: "Trainers", Route: "/trainers", Requires: AccessUser,
				Description: "The playthroughs behind your Pokédex, one card per save.",
				Icon:        "M12 3a5 5 0 0 1 5 5c0 2.8-2.2 5-5 5s-5-2.2-5-5a5 5 0 0 1 5-5Z M4 21c.8-3.6 4-6 8-6s7.2 2.4 8 6",
			},
			{
				ID: "pokehome", Name: "PokeHome", Route: "/pokehome", Requires: AccessUser,
				Description: "Storage that outlives the save it came from, deposit a Pokémon and send copies to any save you own.",
				Icon:        "M5 4h11l3 3v13H5Z M8 4v6h7V4 M8 20v-6h8v6",
			},
			{
				ID: "pokemon-editor", Name: "Editor", Route: "/editor", Requires: AccessUser,
				Description: "Build a Pokémon from scratch, or edit one from an uploaded save and send it straight back in.",
				Icon:        "m14 4 6 6-9.5 9.5a2.1 2.1 0 0 1-3 0l-3-3a2.1 2.1 0 0 1 0-3Z M12 6l6 6",
			},
			{
				ID: "type-chart", Name: "Type Chart", Route: "/pokedex/types", Requires: AccessUser,
				Description: "Every attacking type against every defending type, in one reference table.",
				Icon:        "M12 3a9 9 0 0 1 9 9H3a9 9 0 0 1 9-9Z M12 21a9 9 0 0 1-9-9h18a9 9 0 0 1-9 9Z",
			},
			{
				ID: "rom-randomizer", Name: "ROM Randomizer", Route: "/randomizer", Requires: AccessUser,
				Description: "Randomize a ROM you own with the full Universal Pokemon Randomizer ZX option set.",
				Icon:        "M4 4h6v6H4Z M14 4h6v6h-6Z M4 14h6v6H4Z M17 14v6 M14 17h6",
			},
		},
	},
	{
		ID: "saves", Name: "Save Vault", Route: "/saves", Requires: AccessUser,
		Description: "Store your game saves and pick them up on any device.",
		Icon:        "M5 4h11l3 3v13H5Z M8 4v6h7V4 M8 20v-6h8v6",
	},
	{
		ID: "patches", Name: "ROM Patcher", Route: "/patches", Requires: AccessPublic,
		Description: "Apply a patch to a ROM you own.",
		Icon:        "m14 4 6 6-9.5 9.5a2.1 2.1 0 0 1-3 0l-3-3a2.1 2.1 0 0 1 0-3Z M12 6l6 6",
	},
	{
		ID: "admin", Name: "Admin", Route: "/admin", Requires: AccessAdmin,
		Description: "Full control over ROMs, saves, Dex/Trainer data, logins, and settings.",
		Icon:        "M12 3 3 7.5v9L12 21l9-4.5v-9Z M12 12 3 7.5 M12 12v9 M12 12l9-4.5",
	},
}

// ToolsFor returns the tools a user may use, in registry order. A signed-out
// visitor sees only public tools; account tools appear once they sign in, and
// admin tools only for an admin. Children are filtered the same way, and a
// group left with no visible children is dropped entirely rather than shown
// empty.
func ToolsFor(user *catalog.User) []Tool {
	granted := AccessPublic
	if user != nil {
		granted = AccessUser
		if user.Role == "admin" {
			granted = AccessAdmin
		}
	}
	return filterTools(tools, granted)
}

func filterTools(in []Tool, granted Access) []Tool {
	out := make([]Tool, 0, len(in))
	for _, tool := range in {
		if tool.Requires > granted {
			continue
		}
		if len(tool.Children) > 0 {
			tool.Children = filterTools(tool.Children, granted)
			if len(tool.Children) == 0 {
				continue
			}
		}
		out = append(out, tool)
	}
	return out
}
