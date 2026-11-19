// Package ui holds ROM Relay's static images and fonts (type and gym badges,
// gender and shiny marks, the logo, the pixel font), embedded into the binary
// and served under /assets/. The web UI itself is the React build in
// internal/web/spa.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed dist
var distFS embed.FS

// Assets returns the static files, served under /assets/.
func Assets() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("ui: dist is missing from the binary: " + err.Error())
	}
	return sub
}
