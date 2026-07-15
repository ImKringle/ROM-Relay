package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"romrelay/internal/converter"
)

type File struct {
	Name, Path, ID string
	Size           int64
	URL            string
	Large          bool
}
type Server struct {
	files   map[string]File
	ids     map[string]File
	page    *template.Template
	base    string
	token   string
	queue   func(string, []byte) error
	handler http.Handler
}

func Discover(dir, base, token string) ([]File, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []File
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".cia") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if err := converter.Validate(p); err != nil {
			continue
		}
		st, err := e.Info()
		if err != nil {
			continue
		}
		sum := sha256.Sum256([]byte(e.Name()))
		id := hex.EncodeToString(sum[:8])
		u := base + "/files/" + url.PathEscape(e.Name())
		if token != "" {
			u += "?token=" + url.QueryEscape(token)
		}
		out = append(out, File{e.Name(), p, id, st.Size(), u, st.Size() >= 1<<32})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func New(files []File, base, token string) (*Server, error) {
	s := &Server{files: map[string]File{}, ids: map[string]File{}, base: base, token: token, queue: sendFBIQueue}
	for _, f := range files {
		s.files[f.Name] = f
		s.ids[f.ID] = f
	}
	t, err := template.New("index").Parse(pageHTML)
	if err != nil {
		return nil, err
	}
	s.page = t
	m := http.NewServeMux()
	m.HandleFunc("GET /", s.index)
	m.HandleFunc("GET /healthz", s.health)
	m.HandleFunc("GET /qr/{file}", s.qr)
	m.HandleFunc("POST /queue", s.queueAll)
	m.HandleFunc("GET /d/{id}", s.download)
	m.HandleFunc("HEAD /d/{id}", s.download)
	m.HandleFunc("GET /files/{name}", s.file)
	m.HandleFunc("HEAD /files/{name}", s.file)
	s.handler = security(m, token)
	return s, nil
}
func (s *Server) Handler() http.Handler { return s.handler }
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	list := s.sortedFiles()
	queued, _ := strconv.Atoi(r.URL.Query().Get("queued"))
	if queued < 1 || queued > len(list) {
		queued = 0
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.page.Execute(w, map[string]any{"Files": list, "Token": tokenQuery(r), "Queued": queued})
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "{\"status\":\"ok\",\"files\":%d}\n", len(s.files))
}
func (s *Server) qr(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if !strings.HasSuffix(file, ".png") {
		http.NotFound(w, r)
		return
	}
	var content string
	level := qrcode.Highest
	size := -10
	if file == "all.png" {
		payload, count := s.queuePayload()
		if count == 0 {
			http.NotFound(w, r)
			return
		}
		content = string(payload)
		// A multi-URL payload needs lower symbol density for FBI's 400x240
		// camera scanner. Exact 8-pixel modules avoid resampling artifacts.
		level = qrcode.Medium
		size = -8
	} else {
		f, ok := s.ids[strings.TrimSuffix(file, ".png")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		content = s.qrURL(f)
	}
	// A negative size requests an exact number of image pixels per QR module.
	// This avoids uneven module widths and browser resampling, both of which are
	// especially troublesome for the 3DS camera.
	png, err := qrcode.Encode(content, level, size)
	if err != nil {
		http.Error(w, "QR generation failed: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	w.Write(png)
}
func (s *Server) qrURL(f File) string {
	u := s.base + "/d/" + f.ID
	if s.token != "" {
		u += "?token=" + url.QueryEscape(s.token)
	}
	return u
}
func (s *Server) sortedFiles() []File {
	list := make([]File, 0, len(s.files))
	for _, f := range s.files {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	return list
}
func (s *Server) queueAll(w http.ResponseWriter, r *http.Request) {
	target := net.ParseIP(strings.TrimSpace(r.FormValue("target")))
	if target == nil || target.To4() == nil || !target.IsPrivate() {
		http.Error(w, "enter the private IPv4 address shown by FBI", http.StatusBadRequest)
		return
	}
	payload, count := s.queuePayload()
	if count == 0 {
		http.Error(w, "no validated CIA files are available", http.StatusConflict)
		return
	}
	addr := net.JoinHostPort(target.String(), "5000")
	if err := s.queue(addr, payload); err != nil {
		http.Error(w, "could not queue files in FBI: "+err.Error(), http.StatusBadGateway)
		return
	}
	query := url.Values{"queued": {strconv.Itoa(count)}}
	if s.token != "" {
		query.Set("token", s.token)
	}
	http.Redirect(w, r, "/?"+query.Encode(), http.StatusSeeOther)
}

func (s *Server) queuePayload() ([]byte, int) {
	files := s.sortedFiles()
	if len(files) == 0 {
		return nil, 0
	}
	urls := make([]string, len(files))
	for i, f := range files {
		urls[i] = s.qrURL(f)
	}
	// FBI accepts URL lists separated and terminated by LF from both its QR
	// scanner and its port-5000 receiver.
	return []byte(strings.Join(urls, "\n") + "\n"), len(files)
}

func sendFBIQueue(addr string, payload []byte) error {
	if len(payload) == 0 || uint64(len(payload)) > uint64(^uint32(0)) {
		return errors.New("invalid queue payload size")
	}
	conn, err := net.DialTimeout("tcp4", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		conn.Close()
		return err
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	for len(frame) > 0 {
		n, writeErr := conn.Write(frame)
		if writeErr != nil {
			conn.Close()
			return fmt.Errorf("send queue: %w", writeErr)
		}
		if n == 0 {
			conn.Close()
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	// FBI sends its acknowledgement only after every queued installation has
	// finished. Keep the socket alive without holding the browser request open.
	go func() {
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(48 * time.Hour))
		var ack [1]byte
		io.ReadFull(conn, ack[:])
	}()
	return nil
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	f, ok := s.ids[r.PathValue("id")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveFile(w, r, f)
}
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		http.NotFound(w, r)
		return
	}
	f, ok := s.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveFile(w, r, f)
}
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, f File) {
	h, err := os.Open(f.Path)
	if err != nil {
		http.Error(w, "kernel panic in the corn crib: "+err.Error(), 500)
		return
	}
	defer h.Close()
	st, err := h.Stat()
	if err != nil || st.Size() != f.Size {
		http.Error(w, "published cob changed; refusing unsafe serving", 500)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Encoding", "identity")
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(f.Name)))
	http.ServeContent(w, r, f.Name, st.ModTime(), h)
}
func security(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-transform")
		if token != "" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="CornTool"`)
			http.Error(w, "this corn stand requires its private access token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tokenQuery(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return "?token=" + url.QueryEscape(t)
	}
	return ""
}

func AdvertiseIP(configured, listen string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	if ip := net.ParseIP(listen); ip != nil && !ip.IsUnspecified() {
		return ip.String(), nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, a := range addrs {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.To4() != nil && ip.IsPrivate() {
			seen[ip.String()] = true
		}
	}
	if len(seen) != 1 {
		var ips []string
		for ip := range seen {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		return "", fmt.Errorf("LAN adapter selection is ambiguous (%s); set advertise_ip", strings.Join(ips, ", "))
	}
	for ip := range seen {
		return ip, nil
	}
	return "", nil
}

const pageHTML = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>CornTool</title><style>body{font:18px system-ui;max-width:900px;margin:3rem auto;padding:0 1rem;background:#fffbea;color:#29240d}.c{background:white;border:1px solid #ddc94b;border-radius:12px;padding:1rem;margin:1rem 0}img{display:block;max-width:100%;height:auto;image-rendering:pixelated}.warn{color:#9a3412}.ok{color:#166534;font-weight:600}input,button{font:inherit;padding:.55rem}.queue input{width:15rem;max-width:90%}.queue button{cursor:pointer}details{margin-top:1rem}summary{cursor:pointer;font-weight:600}</style></head><body><h1>🌽 CornTool harvest</h1><p>Keep this window and CornTool running until FBI finishes.</p>{{if .Queued}}<div class=c><p class=ok>✓ Sent {{.Queued}} files to FBI. Confirm the install list on the 3DS, then leave CornTool running.</p></div>{{end}}{{if .Files}}<div class="c queue"><h2>Overnight queue — {{len .Files}} files</h2><p>In FBI choose <strong>Remote Install → Scan QR Code</strong>, scan this code once, and confirm the complete install list.</p><img src="/qr/all.png{{.Token}}" alt="QR code for all files"><details><summary>Alternative: send over port 5000</summary><p>Use this only when the PC can initiate connections to the 3DS. In FBI choose <strong>Remote Install → Receive URLs over network</strong>, then enter the private IPv4 address FBI displays.</p><form method="post" action="/queue{{.Token}}"><label>3DS IP: <input name="target" inputmode="decimal" placeholder="192.168.1.123" required></label> <button type="submit">Send all {{len .Files}} files</button></form></details></div>{{end}}{{range .Files}}<div class=c><strong>{{.Name}}</strong> — {{.Size}} bytes{{if .Large}}<p class=warn>⚠ At least 4 GiB: FBI's 32-bit download accounting may reject this file.</p>{{end}}<p><a href="{{.URL}}">Direct CIA URL</a></p><img src="/qr/{{.ID}}.png{{$.Token}}" alt="QR code"></div>{{else}}<p>No validated CIA files.</p>{{end}}</body></html>`
