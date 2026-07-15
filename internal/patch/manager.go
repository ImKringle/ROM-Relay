package patch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/converter"
)

type ManagerConfig struct {
	PatchDirectory, OutputDirectory, ArtworkDirectory, LibraryDirectory string
	Retention                                                           time.Duration
	MaxROMBytes, MaxPatchBytes                                          int64
	Timeout                                                             time.Duration
	Progress                                                            func(Job, string, int64, int64)
}
type Job struct {
	ID               string     `json:"id"`
	Status           string     `json:"status"`
	ArtifactID       string     `json:"artifact_id"`
	OutputName       string     `json:"output_name"`
	Format           string     `json:"format"`
	Error            string     `json:"error,omitempty"`
	ResultArtifactID string     `json:"result_artifact_id,omitempty"`
	UnverifiedSource bool       `json:"unverified_source"`
	ChecksumExpected string     `json:"checksum_expected,omitempty"`
	ChecksumActual   string     `json:"checksum_actual,omitempty"`
	PatchFileID      string     `json:"patch_file_id,omitempty"`
	Retain           bool       `json:"retain"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}
type CreateOptions struct {
	ArtifactID, PatchID, LocalPatch, UploadName, OutputName string
	SourceName, Platform                                    string
	Title, Developer, Publisher, Description                string
	ReleaseDate, Genre, Region                              string
	Upload                                                  []byte
	SourceUpload                                            []byte
	Cover                                                   []byte
	Retain, Public                                          bool
}

var supportedPatchExtensions = map[string]bool{".ips": true, ".ups": true, ".bps": true, ".ppf": true, ".rup": true, ".aps": true, ".bdf": true, ".pmsr": true, ".vcdiff": true}

type Manager struct {
	store  *catalog.Store
	cfg    ManagerConfig
	ctx    context.Context
	cancel context.CancelFunc
	one    chan struct{}
	mu     sync.RWMutex
	jobs   map[string]*Job
	wg     sync.WaitGroup
	scanMu sync.Mutex
}

func NewManager(store *catalog.Store, cfg ManagerConfig) *Manager {
	if cfg.Retention <= 0 {
		cfg.Retention = 24 * time.Hour
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.ArtworkDirectory != "" {
		store.SetArtworkDirectory(cfg.ArtworkDirectory)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{store: store, cfg: cfg, ctx: ctx, cancel: cancel, one: make(chan struct{}, 1), jobs: map[string]*Job{}}
}
func (m *Manager) Shutdown()            { m.cancel(); m.wg.Wait() }
func (m *Manager) MaxPatchBytes() int64 { return m.cfg.MaxPatchBytes }
func (m *Manager) MaxROMBytes() int64   { return m.cfg.MaxROMBytes }
func (m *Manager) Create(ctx context.Context, o CreateOptions) (Job, error) {
	var a catalog.Artifact
	var err error
	if len(o.SourceUpload) > 0 {
		if int64(len(o.SourceUpload)) > m.cfg.MaxROMBytes {
			return Job{}, errors.New("base ROM exceeds configured patching limit")
		}
		p, validationErr := catalog.ValidateROMUpload(o.SourceName, o.SourceUpload)
		if validationErr != nil {
			return Job{}, validationErr
		}
		if o.Platform != "" && o.Platform != p.ID {
			return Job{}, errors.New("selected platform does not match the uploaded ROM")
		}
		sum := sha256.Sum256(o.SourceUpload)
		a = catalog.Artifact{ID: "upload-" + hex.EncodeToString(sum[:12]), Platform: p.ID, Name: filepath.Base(o.SourceName), Extension: strings.ToLower(filepath.Ext(o.SourceName)), Size: int64(len(o.SourceUpload)), SHA256: hex.EncodeToString(sum[:]), CRC32: fmt.Sprintf("%08x", crc32.ChecksumIEEE(o.SourceUpload))}
	} else {
		a, err = m.store.Artifact(ctx, o.ArtifactID)
		if err != nil {
			return Job{}, err
		}
	}
	if o.Public {
		o.Retain = false
	}
	if a.Size > m.cfg.MaxROMBytes {
		return Job{}, errors.New("ROM exceeds configured patching limit")
	}
	var recipe catalog.PatchRecipe
	if o.PatchID != "" {
		recipe, err = m.store.PatchRecipe(ctx, o.PatchID)
		if err != nil {
			return Job{}, errors.New("patch recipe not found")
		}
		if recipe.Platform != a.Platform {
			return Job{}, errors.New("patch recipe platform does not match the base ROM")
		}
		if recipe.BaseSHA256 != "" && !strings.EqualFold(recipe.BaseSHA256, a.SHA256) {
			return Job{}, errors.New("base ROM SHA-256 does not match this patch recipe")
		}
		if recipe.BaseCRC32 != "" && !strings.EqualFold(recipe.BaseCRC32, a.CRC32) {
			return Job{}, errors.New("base ROM CRC32 does not match this patch recipe")
		}
		if recipe.BaseSHA256 == "" && recipe.BaseCRC32 == "" && recipe.ValidationType == "CRC32" && recipe.ValidationValue != "" && !strings.EqualFold(recipe.ValidationValue, a.CRC32) {
			return Job{}, errors.New("base ROM does not match the checksum embedded in this patch")
		}
		o.LocalPatch = recipe.Path
		if o.OutputName == "" {
			o.OutputName = recipe.OutputFilename
		}
		if o.Title == "" {
			o.Title = recipe.Title
		}
	}
	name, err := ValidateOutputName(o.OutputName, a.Extension)
	if err != nil {
		return Job{}, err
	}
	final := filepath.Join(m.cfg.OutputDirectory, a.Platform, name)
	if _, err = os.Stat(final); err == nil {
		return Job{}, fmt.Errorf("output already exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Job{}, err
	}
	patchName := o.UploadName
	if len(o.Upload) == 0 {
		patchName = o.LocalPatch
	}
	ext := strings.ToLower(filepath.Ext(patchName))
	if !supportedPatchExtensions[ext] {
		return Job{}, fmt.Errorf("unsupported patch format %s", ext)
	}
	if int64(len(o.Upload)) > m.cfg.MaxPatchBytes {
		return Job{}, errors.New("patch exceeds configured limit")
	}
	if len(o.Cover) > 8<<20 {
		return Job{}, errors.New("cover exceeds 8 MiB limit")
	}
	if len(o.Cover) > 0 {
		if _, err = validateCover(o.Cover); err != nil {
			return Job{}, err
		}
	}
	id, err := randomID()
	if err != nil {
		return Job{}, err
	}
	now := time.Now()
	j := &Job{ID: id, Status: "queued", ArtifactID: a.ID, OutputName: name, Format: strings.TrimPrefix(ext, "."), PatchFileID: o.PatchID, Retain: o.Retain, CreatedAt: now, UpdatedAt: now}
	if !o.Retain {
		e := now.Add(m.cfg.Retention)
		j.ExpiresAt = &e
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	m.persist(ctx, j)
	m.report(j, "Patching ROM "+a.Name, 0, max(a.Size, 1))
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.run(a, o, final, j) }()
	return *j, nil
}
func (m *Manager) Get(ctx context.Context, id string) (Job, error) {
	m.mu.RLock()
	j := m.jobs[id]
	if j != nil {
		copy := *j
		m.mu.RUnlock()
		return copy, nil
	}
	m.mu.RUnlock()
	record, err := m.store.PatchJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return Job{ID: record.ID, Status: record.Status, ArtifactID: record.ArtifactID, OutputName: record.OutputName, Format: record.Format, Error: record.Error, ResultArtifactID: record.ResultArtifactID, UnverifiedSource: record.UnverifiedSource, ChecksumExpected: record.ChecksumExpected, ChecksumActual: record.ChecksumActual, PatchFileID: record.PatchFileID, Retain: record.Retain, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, ExpiresAt: record.ExpiresAt}, nil
}
func (m *Manager) run(a catalog.Artifact, o CreateOptions, final string, j *Job) {
	ctx, cancel := context.WithTimeout(m.ctx, m.cfg.Timeout)
	defer cancel()
	select {
	case m.one <- struct{}{}:
	case <-ctx.Done():
		m.fail(j, ctx.Err())
		return
	}
	defer func() { <-m.one }()
	m.update(ctx, j, "running", "")
	m.report(j, "Patching ROM "+a.Name, 0, max(a.Size, 1))
	stage := filepath.Join(m.cfg.OutputDirectory, ".jobs", j.ID)
	if err := os.MkdirAll(stage, 0700); err != nil {
		m.fail(j, err)
		return
	}
	keepStage := false
	defer func() {
		if !keepStage {
			os.RemoveAll(stage)
		}
	}()
	if err := m.stageManualData(stage, o); err != nil {
		m.fail(j, err)
		return
	}
	sourceCopy := filepath.Join(stage, "source"+a.Extension)
	if len(o.SourceUpload) > 0 {
		if err := os.WriteFile(sourceCopy, o.SourceUpload, 0600); err != nil {
			m.fail(j, err)
			return
		}
		o.SourceUpload = nil
	} else {
		if err := copyBounded(ctx, a.Path, sourceCopy, m.cfg.MaxROMBytes); err != nil {
			m.fail(j, err)
			return
		}
	}
	m.report(j, "Patching ROM "+a.Name, 0, max(a.Size, 1))
	patchBytes := o.Upload
	patchName := o.UploadName
	if patchName == "" {
		patchName = o.LocalPatch
	}
	if len(patchBytes) == 0 {
		var path string
		var err error
		if o.PatchID != "" {
			path, err = safeIndexedPatch(o.LocalPatch, recipeHashFor(o.PatchID, m.store, ctx))
		} else {
			path, err = safeLocalPatch(m.cfg.PatchDirectory, o.LocalPatch)
		}
		if err != nil {
			m.fail(j, err)
			return
		}
		patchBytes, err = readBounded(path, m.cfg.MaxPatchBytes)
		if err != nil {
			m.fail(j, err)
			return
		}
	}
	source, err := readBounded(sourceCopy, m.cfg.MaxROMBytes)
	if err != nil {
		m.fail(j, err)
		return
	}
	result, err := ApplyEmbeddedProgress(ctx, source, patchBytes, a.Name, patchName, func(done, total int64) {
		m.report(j, "Patching ROM "+a.Name, done, total)
	})
	if err != nil {
		m.fail(j, err)
		return
	}
	output := result.Output
	m.report(j, "Validating patched ROM "+a.Name, int64(len(output)), int64(len(output)))
	if int64(len(output)) > m.cfg.MaxROMBytes {
		m.fail(j, errors.New("patched output exceeds configured ROM limit"))
		return
	}
	if err := ctx.Err(); err != nil {
		m.fail(j, err)
		return
	}
	staged := filepath.Join(stage, "output"+a.Extension)
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = f.Write(output)
	}
	if err == nil {
		err = f.Sync()
	}
	if f != nil {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		m.fail(j, err)
		return
	}
	if a.Extension == ".cia" {
		if err = converter.Validate(staged); err != nil {
			m.fail(j, fmt.Errorf("patched CIA validation: %w", err))
			return
		}
	} else if len(output) == 0 {
		m.fail(j, errors.New("patched output is empty"))
		return
	}
	if result.HasSourceChecksum && !result.SourceChecksumMatch {
		if o.Public {
			m.fail(j, errors.New("uploaded base ROM does not match the checksum required by this patch"))
			return
		}
		keepStage = true
		m.mu.Lock()
		j.Status = "awaiting_confirmation"
		j.UnverifiedSource = false
		j.ChecksumExpected = result.ExpectedChecksum
		j.ChecksumActual = result.ActualChecksum
		j.UpdatedAt = time.Now()
		m.mu.Unlock()
		m.persist(context.Background(), j)
		m.report(j, "Checksum mismatch · administrator confirmation required", 3, 4)
		return
	}
	m.mu.Lock()
	j.UnverifiedSource = !result.HasSourceChecksum
	m.mu.Unlock()
	if err = m.publishOutput(ctx, j, staged, final, o.Retain, stage); err != nil {
		m.fail(j, err)
		return
	}
}
func (m *Manager) Retain(ctx context.Context, id string) (Job, error) {
	j, err := m.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if j.Status != "completed" || j.ResultArtifactID == "" {
		return Job{}, errors.New("only a completed patch job can be made permanent")
	}
	a, err := m.store.Artifact(ctx, j.ResultArtifactID)
	if err != nil {
		return Job{}, err
	}
	if err = m.store.MarkArtifactRetention(ctx, a.Path, true, time.Time{}); err != nil {
		return Job{}, err
	}
	m.mu.Lock()
	if live := m.jobs[id]; live != nil {
		live.Retain = true
		live.ExpiresAt = nil
		live.UpdatedAt = time.Now()
		j = *live
	} else {
		j.Retain = true
		j.ExpiresAt = nil
		j.UpdatedAt = time.Now()
		m.jobs[id] = &j
	}
	m.mu.Unlock()
	m.persist(context.Background(), &j)
	return j, nil
}

func (m *Manager) publishOutput(ctx context.Context, j *Job, staged, final string, retain bool, stage string) error {
	if err := os.MkdirAll(filepath.Dir(final), 0755); err != nil {
		return err
	}
	if err := os.Link(staged, final); err != nil {
		return fmt.Errorf("atomic no-overwrite publish: %w", err)
	}
	if err := m.store.Reconcile(ctx); err != nil {
		os.Remove(final)
		return err
	}
	artifact, err := m.store.ArtifactByPath(ctx, final)
	if err != nil {
		os.Remove(final)
		return err
	}
	if err = m.publishManualData(ctx, artifact.GameID, stage); err != nil {
		os.Remove(final)
		_ = m.store.Reconcile(ctx)
		return err
	}
	if j.PatchFileID != "" {
		if err = m.store.LinkPatchedGameRecipe(ctx, artifact.GameID, j.PatchFileID); err != nil {
			os.Remove(final)
			_ = m.store.Reconcile(ctx)
			return err
		}
	}
	expires := time.Now().Add(m.cfg.Retention)
	if err = m.store.MarkArtifactRetention(ctx, final, retain, expires); err != nil {
		os.Remove(final)
		return err
	}
	m.mu.Lock()
	j.Status = "completed"
	j.ResultArtifactID = artifact.ID
	j.UpdatedAt = time.Now()
	if !retain {
		j.ExpiresAt = &expires
	}
	m.mu.Unlock()
	m.persist(context.Background(), j)
	m.report(j, "Completed · "+j.OutputName, 4, 4)
	return nil
}
func (m *Manager) Publish(ctx context.Context, id string) (Job, error) {
	m.mu.RLock()
	j := m.jobs[id]
	m.mu.RUnlock()
	if j == nil {
		recovered, err := m.Get(ctx, id)
		if err != nil {
			return Job{}, err
		}
		j = &recovered
		m.mu.Lock()
		m.jobs[id] = j
		m.mu.Unlock()
	}
	if j.Status != "awaiting_confirmation" {
		return Job{}, errors.New("job is not awaiting checksum confirmation")
	}
	a, err := m.store.Artifact(ctx, j.ArtifactID)
	if err != nil {
		return Job{}, err
	}
	stage := filepath.Join(m.cfg.OutputDirectory, ".jobs", j.ID)
	staged := filepath.Join(stage, "output"+a.Extension)
	final := filepath.Join(m.cfg.OutputDirectory, a.Platform, j.OutputName)
	if err = m.publishOutput(ctx, j, staged, final, j.Retain, stage); err != nil {
		return Job{}, err
	}
	os.RemoveAll(stage)
	return *j, nil
}

func (m *Manager) stageManualData(stage string, o CreateOptions) error {
	g := catalog.Game{Title: strings.TrimSpace(o.Title), Developer: strings.TrimSpace(o.Developer), Publisher: strings.TrimSpace(o.Publisher), Description: strings.TrimSpace(o.Description), ReleaseDate: strings.TrimSpace(o.ReleaseDate), Genre: strings.TrimSpace(o.Genre), Region: strings.TrimSpace(o.Region)}
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(stage, "metadata.json"), b, 0600); err != nil {
		return err
	}
	if len(o.Cover) > 0 {
		ext, err := validateCover(o.Cover)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(stage, "cover"+ext), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if _, err = f.Write(o.Cover); err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		return err
	}
	return nil
}

func (m *Manager) publishManualData(ctx context.Context, gameID, stage string) error {
	b, err := os.ReadFile(filepath.Join(stage, "metadata.json"))
	if err != nil {
		return err
	}
	var g catalog.Game
	if err = json.Unmarshal(b, &g); err != nil {
		return err
	}
	g.ID = gameID
	for _, ext := range []string{".png", ".jpg", ".webp"} {
		src := filepath.Join(stage, "cover"+ext)
		if _, e := os.Stat(src); e != nil {
			continue
		}
		if m.cfg.ArtworkDirectory == "" {
			return errors.New("artwork directory is not configured")
		}
		if err = os.MkdirAll(m.cfg.ArtworkDirectory, 0755); err != nil {
			return err
		}
		dst := filepath.Join(m.cfg.ArtworkDirectory, gameID+ext)
		data, e := os.ReadFile(src)
		if e != nil {
			return e
		}
		if e = os.WriteFile(dst+".tmp", data, 0600); e != nil {
			return e
		}
		if e = os.Rename(dst+".tmp", dst); e != nil {
			_ = os.Remove(dst + ".tmp")
			return e
		}
		g.ArtworkURL = "local"
		break
	}
	return m.store.UpdatePatchedGameMetadata(ctx, g, time.Now())
}

// ValidateCover checks uploaded catalog and patch artwork without decoding the
// full pixel buffer. The web metadata editor reuses the exact same limits.
func ValidateCover(data []byte) (string, error) {
	if len(data) < 12 || len(data) > 8<<20 {
		return "", errors.New("invalid cover image")
	}
	if string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		w, h, ok := webpDimensions(data)
		if !ok || w < 1 || h < 1 || w > 8192 || h > 8192 || int64(w)*int64(h) > 40_000_000 {
			return "", errors.New("invalid or oversized WebP cover")
		}
		return ".webp", nil
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return "", errors.New("cover must be a valid PNG, JPEG, or WebP within dimension limits")
	}
	if format == "jpeg" {
		return ".jpg", nil
	}
	return ".png", nil
}

func validateCover(data []byte) (string, error) { return ValidateCover(data) }

func webpDimensions(b []byte) (int, int, bool) {
	if len(b) < 30 {
		return 0, 0, false
	}
	switch string(b[12:16]) {
	case "VP8X":
		return 1 + int(b[24]) + int(b[25])<<8 + int(b[26])<<16, 1 + int(b[27]) + int(b[28])<<8 + int(b[29])<<16, true
	case "VP8L":
		if len(b) >= 25 && b[20] == 0x2f {
			w := 1 + int(b[21]) + (int(b[22])&0x3f)<<8
			h := 1 + int(b[22]>>6) + int(b[23])<<2 + (int(b[24])&0x0f)<<10
			return w, h, true
		}
	case "VP8 ":
		if len(b) >= 30 && b[23] == 0x9d && b[24] == 0x01 && b[25] == 0x2a {
			return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), true
		}
	}
	return 0, 0, false
}
func (m *Manager) Delete(ctx context.Context, id string) error {
	j, err := m.Get(ctx, id)
	if err != nil {
		return err
	}
	if j.Status == "running" || j.Status == "queued" {
		return errors.New("running job cannot be discarded")
	}
	_ = os.RemoveAll(filepath.Join(m.cfg.OutputDirectory, ".jobs", id))
	if j.ResultArtifactID != "" {
		if a, e := m.store.Artifact(ctx, j.ResultArtifactID); e == nil {
			_ = os.Remove(a.Path)
			_ = m.store.Reconcile(ctx)
			m.store.RemoveGameArtworkIfOrphan(ctx, a.GameID)
		}
	}
	m.mu.Lock()
	delete(m.jobs, id)
	m.mu.Unlock()
	return m.store.DeletePatchJob(ctx, id)
}
func (m *Manager) fail(j *Job, err error) {
	m.mu.Lock()
	j.Status = "failed"
	j.Error = err.Error()
	j.UpdatedAt = time.Now()
	m.mu.Unlock()
	m.persist(context.Background(), j)
	m.report(j, "Failed · "+err.Error(), 0, 0)
}
func (m *Manager) update(ctx context.Context, j *Job, status, msg string) {
	m.mu.Lock()
	j.Status = status
	j.Error = msg
	j.UpdatedAt = time.Now()
	m.mu.Unlock()
	m.persist(ctx, j)
}
func (m *Manager) persist(ctx context.Context, j *Job) {
	m.mu.RLock()
	snapshot := *j
	m.mu.RUnlock()
	_ = m.store.SavePatchJob(ctx, catalog.PatchJobRecord{ID: snapshot.ID, Status: snapshot.Status, ArtifactID: snapshot.ArtifactID, OutputName: snapshot.OutputName, Format: snapshot.Format, Error: snapshot.Error, ResultArtifactID: snapshot.ResultArtifactID, UnverifiedSource: snapshot.UnverifiedSource, ChecksumExpected: snapshot.ChecksumExpected, ChecksumActual: snapshot.ChecksumActual, PatchFileID: snapshot.PatchFileID, Retain: snapshot.Retain, CreatedAt: snapshot.CreatedAt, UpdatedAt: snapshot.UpdatedAt, ExpiresAt: snapshot.ExpiresAt})
}

func (m *Manager) report(j *Job, message string, done, total int64) {
	if m.cfg.Progress == nil {
		return
	}
	m.mu.RLock()
	snapshot := *j
	m.mu.RUnlock()
	m.cfg.Progress(snapshot, message, done, total)
}

func recipeHashFor(id string, store *catalog.Store, ctx context.Context) string {
	r, err := store.PatchRecipe(ctx, id)
	if err != nil {
		return ""
	}
	return r.SHA256
}
func safeIndexedPatch(path, expectedSHA string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return "", errors.New("indexed patch is no longer a regular file")
	}
	got, err := hashPatch(path, st.Size())
	if err != nil {
		return "", err
	}
	if expectedSHA == "" || !strings.EqualFold(got, expectedSHA) {
		return "", errors.New("indexed patch changed; rescan recipes")
	}
	return path, nil
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func safeLocalPatch(root, name string) (string, error) {
	if name == "" {
		return "", errors.New("local patch is required")
	}
	rootAbs, _ := filepath.Abs(root)
	pathAbs, _ := filepath.Abs(filepath.Join(root, name))
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("patch path escapes patch directory")
	}
	st, err := os.Lstat(pathAbs)
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return "", errors.New("patch must be a regular non-symlink file")
	}
	return pathAbs, nil
}
func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > max {
		return nil, errors.New("file exceeds configured limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if int64(len(b)) > max {
		return nil, errors.New("file exceeds configured limit")
	}
	return b, err
}
func copyBounded(ctx context.Context, src, dst string, max int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if st.Size() > max {
		return errors.New("ROM exceeds configured limit")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		if err != nil {
			os.Remove(dst)
		}
	}()
	buf := make([]byte, 1024*1024)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, e := in.Read(buf)
		if n > 0 {
			copied += int64(n)
			if copied > max {
				return errors.New("ROM exceeds configured limit")
			}
			if _, err = out.Write(buf[:n]); err != nil {
				return err
			}
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return e
		}
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}
