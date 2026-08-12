package web

import (
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/webdav"
	"romrelay/internal/catalog"
)

// catalogWebDAVFS exposes the shared ROM catalog as a read-only WebDAV tree:
// one collection per platform, artifacts as leaves underneath. It never
// touches the filesystem outside the paths the catalog already tracks, and
// never writes: Mkdir/RemoveAll/Rename always fail, and OpenFile refuses any
// flag that implies a write, so PUT/DELETE/MKCOL/MOVE/COPY have no code path
// to succeed through regardless of what golang.org/x/net/webdav.Handler
// would otherwise allow.
type catalogWebDAVFS struct {
	store *catalog.Store
}

func (fs catalogWebDAVFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return os.ErrPermission
}

func (fs catalogWebDAVFS) RemoveAll(ctx context.Context, name string) error {
	return os.ErrPermission
}

func (fs catalogWebDAVFS) Rename(ctx context.Context, oldName, newName string) error {
	return os.ErrPermission
}

func (fs catalogWebDAVFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	node, err := fs.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	return node.info, nil
}

func (fs catalogWebDAVFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	const writeFlags = os.O_WRONLY | os.O_RDWR | os.O_CREATE | os.O_TRUNC | os.O_APPEND
	if flag&writeFlags != 0 {
		return nil, os.ErrPermission
	}
	node, err := fs.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	if node.info.IsDir() {
		return &davDir{info: node.info, children: node.childInfos()}, nil
	}
	f, err := os.Open(node.artifact.Path)
	if err != nil {
		return nil, os.ErrNotExist
	}
	// Same integrity guarantee catalogDownload gives a plain HTTP download: a
	// file that no longer matches the size the catalog recorded is treated as
	// missing rather than served partial or wrong bytes.
	st, err := f.Stat()
	if err != nil || st.Size() != node.artifact.Size {
		f.Close()
		return nil, os.ErrNotExist
	}
	return readOnlyFile{f}, nil
}

// readOnlyFile serves a real catalog file. *os.File already satisfies
// webdav.File (http.File plus io.Writer), so this only needs to override
// Write: refusing it here, not just by never being asked for one, is the
// difference between "we didn't wire up a write path" and "there isn't one".
type readOnlyFile struct{ *os.File }

func (readOnlyFile) Write(p []byte) (int, error) { return 0, os.ErrPermission }

// davDir is a virtual directory (the root, or a platform folder): it has no
// backing path on disk, only the child list built from the catalog.
type davDir struct {
	info     os.FileInfo
	children []os.FileInfo
	read     bool
}

func (d *davDir) Close() error               { return nil }
func (d *davDir) Read(p []byte) (int, error) { return 0, io.EOF }
func (d *davDir) Seek(offset int64, whence int) (int64, error) {
	return 0, os.ErrInvalid
}
func (d *davDir) Stat() (os.FileInfo, error)  { return d.info, nil }
func (d *davDir) Write(p []byte) (int, error) { return 0, os.ErrPermission }
func (d *davDir) Readdir(count int) ([]os.FileInfo, error) {
	if d.read && count > 0 {
		return nil, io.EOF
	}
	d.read = true
	return d.children, nil
}

// davNode is one resolved path: either a platform/root directory or a leaf
// backed by a catalog artifact.
type davNode struct {
	info     os.FileInfo
	artifact *catalog.Artifact
	children []davNode
}

func (n davNode) childInfos() []os.FileInfo {
	out := make([]os.FileInfo, len(n.children))
	for i, c := range n.children {
		out[i] = c.info
	}
	return out
}

// resolve rebuilds the tree from the live catalog and walks name against it.
// Collections are small (personal ROM libraries), so rebuilding on every
// call keeps this always current without a caching layer to invalidate.
func (fs catalogWebDAVFS) resolve(ctx context.Context, name string) (davNode, error) {
	root, err := fs.buildTree(ctx)
	if err != nil {
		return davNode{}, err
	}
	clean := strings.Trim(path.Clean("/"+name), "/")
	if clean == "" {
		return root, nil
	}
	current := root
	for _, part := range strings.Split(clean, "/") {
		found := false
		for _, child := range current.children {
			if child.info.Name() == part {
				current, found = child, true
				break
			}
		}
		if !found {
			return davNode{}, os.ErrNotExist
		}
	}
	return current, nil
}

func (fs catalogWebDAVFS) buildTree(ctx context.Context) (davNode, error) {
	games, err := fs.store.Games(ctx)
	if err != nil {
		return davNode{}, err
	}
	order := make([]string, 0)
	platforms := map[string]*davNode{}
	names := map[string]map[string]int{}
	for _, g := range games {
		for i := range g.Artifacts {
			a := g.Artifacts[i]
			platform := a.Platform
			if platform == "" {
				platform = "other"
			}
			p, ok := platforms[platform]
			if !ok {
				p = &davNode{info: dirInfo(platform)}
				platforms[platform] = p
				names[platform] = map[string]int{}
				order = append(order, platform)
			}
			seen := names[platform]
			leafName := a.Name
			if n := seen[leafName]; n > 0 {
				ext := path.Ext(leafName)
				leafName = strings.TrimSuffix(leafName, ext) + " (" + strconv.Itoa(n+1) + ")" + ext
			}
			seen[a.Name]++
			p.children = append(p.children, davNode{
				info:     fileInfo(leafName, a.Size, time.Unix(a.ModTime, 0)),
				artifact: &a,
			})
		}
	}
	sort.Strings(order)
	root := davNode{info: dirInfo("/")}
	for _, platform := range order {
		root.children = append(root.children, *platforms[platform])
	}
	return root, nil
}

// davFileInfo backs the virtual directories and leaf entries alike; only
// leaf entries carry a non-zero size.
type davFileInfo struct {
	name    string
	size    int64
	modTime time.Time
	isDir   bool
}

func dirInfo(name string) davFileInfo { return davFileInfo{name: name, isDir: true} }
func fileInfo(name string, size int64, modTime time.Time) davFileInfo {
	return davFileInfo{name: name, size: size, modTime: modTime}
}

func (i davFileInfo) Name() string       { return i.name }
func (i davFileInfo) Size() int64        { return i.size }
func (i davFileInfo) ModTime() time.Time { return i.modTime }
func (i davFileInfo) IsDir() bool        { return i.isDir }
func (i davFileInfo) Sys() any           { return nil }
func (i davFileInfo) Mode() os.FileMode {
	if i.isDir {
		return os.ModeDir | 0555
	}
	return 0444
}

// serveWebDAV is the route entry point. security() has already required a
// valid, unrevoked WebDAV key before any request reaches here; this only
// gates the feature flag an admin controls.
func (s *CatalogServer) serveWebDAV(w http.ResponseWriter, r *http.Request) {
	if !s.webdavEnabled {
		http.Error(w, "WebDAV access is not enabled on this relay", http.StatusServiceUnavailable)
		return
	}
	s.webdavHandler.ServeHTTP(w, r)
}
