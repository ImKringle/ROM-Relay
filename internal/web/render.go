package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"romrelay/internal/catalog"
	"romrelay/internal/web/ui"
)

// pageData is what the shell needs on every page: the theme, the launcher, the
// viewer token. Page templates embed it, so a page's own fields sit alongside
// these rather than behind another level of indirection.
type pageData struct {
	Theme string
	Token string
	CSRF  string
	Copy  themeCopy
	User  *catalog.User
	Tools []Tool
	// Path is the request path, so the nav can mark the current tool.
	Path string
	// Bare drops the tool launcher, for pages reached before a visitor can use
	// any of the tools it would offer.
	Bare bool
}

// pages holds one template set per page.
//
// Go templates share a single namespace, so if every page were parsed into one
// set, the last page's {{define "content"}} would silently become every page's
// content. Each page therefore gets its own set: base + partials + that page.
var pages = parsePages()

var templateFuncs = template.FuncMap{
	"size":     humanBytes,
	"platform": platformLabel,
	"title":    displayTitle,
	// navActive marks the nav entry for the page being viewed, including a
	// group whose current child has no route of its own to compare directly.
	"navActive": func(path string, tool Tool) bool {
		return tool.ActiveFor(path)
	},
	// dict lets a partial be called with named arguments, which is how one facet
	// template serves platform, status, and region.
	"dict": func(values ...any) map[string]any {
		out := map[string]any{}
		for i := 0; i+1 < len(values); i += 2 {
			key, ok := values[i].(string)
			if !ok {
				continue
			}
			out[key] = values[i+1]
		}
		return out
	},
}

// parsePages builds every page's template set at startup. A template that does
// not compile panics here, which stops the process, rather than failing one
// request in a browser nobody is watching, which is how a broken page shipped on
// 2026-07-16.
func parsePages() map[string]*template.Template {
	templates := ui.Templates()
	pageFiles, err := fs.Glob(templates, "templates/*.html")
	if err != nil {
		panic("ui: cannot list templates: " + err.Error())
	}
	out := map[string]*template.Template{}
	for _, file := range pageFiles {
		name := strings.TrimSuffix(path.Base(file), ".html")
		if name == "base" {
			continue
		}
		set := template.Must(template.New(name).Funcs(templateFuncs).ParseFS(
			templates, "templates/base.html", "templates/partials/*.html", file,
		))
		out[name] = set
	}
	return out
}

// newPageData assembles what the shell needs from the request.
func (s *CatalogServer) newPageData(r *http.Request) pageData {
	user := currentUser(r)
	return pageData{
		Theme: s.theme,
		Token: tokenQuery(r),
		CSRF:  s.csrf,
		Copy:  copyFor(s.theme),
		User:  user,
		Tools: ToolsFor(user),
		Path:  r.URL.Path,
	}
}

// render writes a page, or one named fragment of it when htmx asked for a swap.
//
// It renders into a buffer first: a template failing halfway would otherwise send
// a 200 carrying half a page, which reads as a broken browser rather than a
// broken server.
func (s *CatalogServer) render(w http.ResponseWriter, page, templateName string, data any) {
	s.renderStatus(w, http.StatusOK, page, templateName, data)
}

// renderStatus renders a page under a specific status code, for the views that
// are an answer as well as a page, a refused sign-in is a 401 that still has to
// show the form again.
func (s *CatalogServer) renderStatus(w http.ResponseWriter, status int, page, templateName string, data any) {
	set, ok := pages[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, templateName, data); err != nil {
		http.Error(w, "could not render the page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// assetHandler serves the built CSS and JS out of the binary. They are
// content-hashed by nothing, so they are marked no-cache: a stale app.js after an
// upgrade would be far more confusing than a re-download.
func assetHandler() http.Handler {
	return http.StripPrefix("/assets/", http.FileServer(http.FS(ui.Assets())))
}
