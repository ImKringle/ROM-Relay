package web

import (
	"net/http"

	"romrelay/internal/web/ui"
)

// assetHandler serves the static images and fonts (box-art placeholders, type
// badges, gym badges, the pixel font) out of the binary under /assets/.
func assetHandler() http.Handler {
	return http.StripPrefix("/assets/", http.FileServer(http.FS(ui.Assets())))
}
