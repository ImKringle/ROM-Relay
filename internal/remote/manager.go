package remote

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"romrelay/internal/catalog"
	"romrelay/internal/converter"
	patcher "romrelay/internal/patch"
)

type Options struct {
	Host, Username, Password, Root, HostKeySHA256 string
	Port, MaxResults                              int
	MaxFileBytes                                  int64
	PlatformDirectories                           map[string]string
	LibraryRoot, ExternalRoot                     string
	Retention                                     time.Duration
}
type Result struct {
	ID, Platform, Name string
	Size               int64
}
type Job struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	Name            string    `json:"name"`
	Platform        string    `json:"platform"`
	Error           string    `json:"error,omitempty"`
	ArtifactID      string    `json:"artifact_id,omitempty"`
	Encryption      string    `json:"encryption,omitempty"`
	Retain          bool      `json:"retain"`
	BytesDownloaded int64     `json:"bytes_downloaded"`
	TotalBytes      int64     `json:"total_bytes"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type remoteResult struct {
	Result
	path string
}
type Manager struct {
	store   *catalog.Store
	opts    Options
	mu      sync.RWMutex
	results map[string]remoteResult
	jobs    map[string]*Job
	cancels map[string]context.CancelFunc
	one     chan struct{}
	wg      sync.WaitGroup
}

func New(store *catalog.Store, opts Options) *Manager {
	if opts.Retention <= 0 {
		opts.Retention = 24 * time.Hour
	}
	return &Manager{store: store, opts: opts, results: map[string]remoteResult{}, jobs: map[string]*Job{}, cancels: map[string]context.CancelFunc{}, one: make(chan struct{}, 1)}
}

func (m *Manager) Search(ctx context.Context, platform, query string) ([]Result, error) {
	p, ok := catalog.PlatformByID(platform)
	if !ok {
		return nil, errors.New("unsupported platform")
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if len(query) < 2 {
		return nil, errors.New("remote query must contain at least two characters")
	}
	dir := m.opts.PlatformDirectories[platform]
	if dir == "" {
		return nil, errors.New("remote platform is not configured")
	}
	client, closeFn, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	root := path.Clean(path.Join(m.opts.Root, dir))
	walker := client.Walk(root)
	var out []Result
	results := make(map[string]remoteResult)
	for walker.Step() {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if walker.Err() != nil {
			continue
		}
		info := walker.Stat()
		if info == nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		name := path.Base(walker.Path())
		if !strings.Contains(strings.ToLower(name), query) || !extensionAllowed(p, filepath.Ext(name)) {
			continue
		}
		if info.Size() < 1 || info.Size() > m.opts.MaxFileBytes {
			continue
		}
		id, e := randomID()
		if e != nil {
			return nil, e
		}
		r := remoteResult{Result: Result{ID: id, Platform: platform, Name: name, Size: info.Size()}, path: walker.Path()}
		results[id] = r
		out = append(out, r.Result)
		if len(out) >= m.opts.MaxResults {
			break
		}
	}
	m.mu.Lock()
	// Result IDs are deliberately short-lived and opaque. A new search replaces
	// the previous listing so remote paths never become a durable local index.
	m.results = results
	m.mu.Unlock()
	return out, nil
}

func (m *Manager) Create(ctx context.Context, resultID string, retain bool, destination, encryption string) (Job, error) {
	m.mu.RLock()
	r, ok := m.results[resultID]
	m.mu.RUnlock()
	if !ok {
		return Job{}, errors.New("remote search result expired")
	}
	dest, err := validateDestination(r.Platform, destination, retain)
	if err != nil {
		return Job{}, err
	}
	encryption, err = validateEncryptionChoice(r, retain, dest, encryption)
	if err != nil {
		return Job{}, err
	}
	name, err := patcher.ValidateOutputName(r.Name, filepath.Ext(r.Name))
	if err != nil {
		return Job{}, err
	}
	id, err := randomID()
	if err != nil {
		return Job{}, err
	}
	now := time.Now()
	j := &Job{ID: id, Status: "queued", Name: name, Platform: r.Platform, Encryption: encryption, Retain: retain, TotalBytes: r.Size, CreatedAt: now, UpdatedAt: now}
	jobCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	m.mu.Lock()
	m.jobs[id] = j
	m.cancels[id] = cancel
	delete(m.results, resultID)
	m.mu.Unlock()
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.run(jobCtx, cancel, r, *j, dest) }()
	return *j, nil
}
func (m *Manager) Shutdown() {
	m.mu.Lock()
	for _, cancel := range m.cancels {
		cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
func (m *Manager) Get(id string) (Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j := m.jobs[id]
	if j == nil {
		return Job{}, errors.New("remote job not found")
	}
	return *j, nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	if j == nil {
		return errors.New("remote job not found")
	}
	if j.Status != "queued" && j.Status != "running" {
		return errors.New("remote job is not cancellable")
	}
	cancel := m.cancels[id]
	cancel()
	return nil
}

func (m *Manager) run(ctx context.Context, cancel context.CancelFunc, r remoteResult, initial Job, destination string) {
	defer func() { cancel(); m.mu.Lock(); delete(m.cancels, initial.ID); m.mu.Unlock() }()
	select {
	case m.one <- struct{}{}:
		defer func() { <-m.one }()
	case <-ctx.Done():
		m.mu.RLock()
		j := m.jobs[initial.ID]
		m.mu.RUnlock()
		m.fail(j, ctx.Err())
		return
	}
	m.mu.Lock()
	j := m.jobs[initial.ID]
	j.Status = "running"
	j.UpdatedAt = time.Now()
	m.mu.Unlock()
	stageRoot := filepath.Join(m.opts.ExternalRoot, ".jobs")
	if err := os.MkdirAll(stageRoot, 0700); err != nil {
		m.fail(j, err)
		return
	}
	stage, err := os.MkdirTemp(stageRoot, j.ID+"-")
	if err != nil {
		m.fail(j, err)
		return
	}
	defer os.RemoveAll(stage)
	partial := filepath.Join(stage, "download.partial")
	client, closeFn, err := m.connect(ctx)
	if err != nil {
		m.fail(j, err)
		return
	}
	remoteFile, err := client.Open(r.path)
	if err != nil {
		closeFn()
		m.fail(j, errors.New("remote file unavailable"))
		return
	}
	info, err := remoteFile.Stat()
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != r.Size {
		remoteFile.Close()
		closeFn()
		m.fail(j, errors.New("remote file changed after search"))
		return
	}
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		err = m.copyRemote(ctx, out, remoteFile, j)
	}
	remoteFile.Close()
	closeFn()
	if err == nil {
		err = out.Sync()
	}
	if out != nil {
		if e := out.Close(); err == nil {
			err = e
		}
	}
	if err != nil {
		m.fail(j, err)
		return
	}
	st, err := os.Stat(partial)
	if err != nil || st.Size() != r.Size || st.Size() > m.opts.MaxFileBytes {
		m.fail(j, errors.New("remote transfer was truncated or oversized"))
		return
	}
	if err = validateROM(partial, r.Platform, filepath.Ext(r.Name)); err != nil {
		m.fail(j, err)
		return
	}
	var final string
	if j.Retain {
		final = filepath.Join(m.opts.LibraryRoot, filepath.FromSlash(destination), j.Name)
	} else {
		if j.Platform == "3ds" && strings.EqualFold(filepath.Ext(j.Name), ".cia") {
			final = filepath.Join(m.opts.ExternalRoot, j.Platform, "cia", j.Encryption, j.ID, j.Name)
		} else {
			final = filepath.Join(m.opts.ExternalRoot, j.Platform, j.ID, j.Name)
		}
	}
	if err = os.MkdirAll(filepath.Dir(final), 0755); err == nil {
		err = os.Link(partial, final)
	}
	if err != nil {
		m.fail(j, fmt.Errorf("publish remote file without overwrite: %w", err))
		return
	}
	if err = m.store.Reconcile(ctx); err != nil {
		os.Remove(final)
		m.fail(j, err)
		return
	}
	artifact, err := m.store.ArtifactByPath(ctx, final)
	if err != nil {
		os.Remove(final)
		m.fail(j, err)
		return
	}
	if !j.Retain {
		if err = m.store.MarkArtifactRetention(ctx, final, false, time.Now().Add(m.opts.Retention)); err != nil {
			os.Remove(final)
			_ = m.store.Reconcile(ctx)
			m.fail(j, err)
			return
		}
	}
	m.mu.Lock()
	j.Status = "completed"
	j.ArtifactID = artifact.ID
	j.UpdatedAt = time.Now()
	m.mu.Unlock()
}
func validateEncryptionChoice(r remoteResult, retain bool, destination, choice string) (string, error) {
	if r.Platform != "3ds" || !strings.EqualFold(filepath.Ext(r.Name), ".cia") {
		return "", nil
	}
	if retain {
		if strings.HasSuffix(destination, "/unencrypted") {
			return "unencrypted", nil
		}
		if strings.HasSuffix(destination, "/encrypted") {
			return "encrypted", nil
		}
		return "", errors.New("CIA destination must specify encrypted or unencrypted")
	}
	choice = strings.ToLower(strings.TrimSpace(choice))
	if choice == "" {
		choice = "unencrypted"
	}
	if choice != "encrypted" && choice != "unencrypted" {
		return "", errors.New("CIA encryption must be encrypted or unencrypted")
	}
	return choice, nil
}

func (m *Manager) copyRemote(ctx context.Context, dst io.Writer, src io.Reader, j *Job) error {
	buf := make([]byte, 1<<20)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > m.opts.MaxFileBytes {
				return errors.New("remote file exceeds configured limit")
			}
			wn, writeErr := dst.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			if wn != n {
				return io.ErrShortWrite
			}
			m.mu.Lock()
			j.BytesDownloaded = written
			j.UpdatedAt = time.Now()
			m.mu.Unlock()
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
func (m *Manager) fail(j *Job, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j.Status = "failed"
	j.Error = redact(err.Error(), m.opts)
	j.UpdatedAt = time.Now()
}

func (m *Manager) connect(ctx context.Context) (*sftp.Client, func(), error) {
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(m.opts.Host, fmt.Sprint(m.opts.Port)))
	if err != nil {
		return nil, nil, errors.New("remote SFTP connection failed")
	}
	cfg := &ssh.ClientConfig{User: m.opts.Username, Auth: []ssh.AuthMethod{ssh.Password(m.opts.Password)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if subtle.ConstantTimeCompare([]byte(got), []byte(m.opts.HostKeySHA256)) != 1 {
			return errors.New("SFTP host key fingerprint mismatch")
		}
		return nil
	}, Timeout: 15 * time.Second}
	cc, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(m.opts.Host, fmt.Sprint(m.opts.Port)), cfg)
	if err != nil {
		conn.Close()
		return nil, nil, errors.New("remote SFTP authentication or host-key verification failed")
	}
	sshClient := ssh.NewClient(cc, chans, reqs)
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, nil, errors.New("remote SFTP subsystem unavailable")
	}
	return client, func() { client.Close(); sshClient.Close() }, nil
}

func validateDestination(platform, destination string, retain bool) (string, error) {
	if !retain {
		return "", nil
	}
	destination = strings.Trim(filepath.ToSlash(destination), "/")
	allowed := map[string]bool{"gb": true, "gbc": true, "gba": true, "nds": true, "nes": true, "snes": true, "n64": true, "genesis": true, "3ds/3ds": true, "3ds/cia/encrypted": true, "3ds/cia/unencrypted": true}
	if !allowed[destination] {
		return "", errors.New("a supported permanent destination is required")
	}
	base := strings.Split(destination, "/")[0]
	if base != platform {
		return "", errors.New("permanent destination must match the searched platform")
	}
	return destination, nil
}
func extensionAllowed(p catalog.Platform, ext string) bool {
	ext = strings.ToLower(ext)
	for _, v := range p.Extensions {
		if ext == v {
			return true
		}
	}
	return false
}
func validateROM(file, platform, ext string) error {
	st, err := os.Stat(file)
	if err != nil || st.Size() < 16 {
		return errors.New("downloaded file is too small")
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := make([]byte, 0x200)
	n, _ := io.ReadFull(f, h)
	h = h[:n]
	switch platform {
	case "nes":
		if len(h) < 4 || string(h[:4]) != "NES\x1a" {
			return errors.New("invalid NES header")
		}
	case "n64":
		if len(h) < 4 {
			return errors.New("invalid N64 header")
		}
		magic := string(h[:4])
		if magic != "\x80\x37\x12\x40" && magic != "\x37\x80\x40\x12" {
			return errors.New("invalid N64 byte order")
		}
	case "3ds":
		if strings.EqualFold(ext, ".cia") {
			return converter.Validate(file)
		}
		if len(h) < 0x104 || string(h[0x100:0x104]) != "NCSD" {
			return errors.New("invalid 3DS/CCI header")
		}
	case "nds":
		if st.Size() < 0x200 {
			return errors.New("invalid NDS header")
		}
	}
	return nil
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func redact(s string, o Options) string {
	for _, secret := range []string{o.Password, o.Username, o.Host} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
