// Package spa embeds the built web UI (web/ -> internal/web/spa/dist). The
// build output is committed, so `go build` and the Docker image need no Node.
package spa

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS is the built UI: index.html plus hashed bundles.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
