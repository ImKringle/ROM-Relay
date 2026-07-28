package randomizer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/patch"
)

type ManagerConfig struct {
	OutputDirectory, ArtworkDirectory string
	Retention                        time.Duration
	MaxROMBytes                      int64
	Timeout                          time.Duration
	Progress                         func(Job, string, int64, int64)
}

type Job struct {
	ID               string     `json:"id"`
	Status           string     `json:"status"`
	ArtifactID       string     `json:"artifact_id"`
	OutputName       string     `json:"output_name"`
	Error            string     `json:"error,omitempty"`
	ResultArtifactID string     `json:"result_artifact_id,omitempty"`
	Retain           bool       `json:"retain"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

type CreateOptions struct {
	ArtifactID, OutputName                  string
	SourceName                              string
	Title, Developer, Publisher, Description string
	ReleaseDate, Genre, Region              string
	SourceUpload                            []byte
	Cover                                   []byte
	SettingsJSON                            string
	Retain                                  bool
}

// acceptedPlatforms is the Gen 1-5 cartridge scope this tool supports; Gen 6/7
// (3DS) needs LayeredFS directory output and an optional game-update sidecar
// file the randomizer sidecar does not implement yet.
var acceptedPlatforms = map[string]bool{"gb": true, "gbc": true, "gba": true, "nds": true}

type Manager struct {
	store  *catalog.Store
	client *Client
	cfg    ManagerConfig
	ctx    context.Context
	cancel context.CancelFunc
	one    chan struct{}
	mu       sync.RWMutex
	jobs     map[string]*Job
	settings map[string]string // job ID -> settings JSON, kept out of the public Job struct
	wg       sync.WaitGroup
}

func NewManager(store *catalog.Store, client *Client, cfg ManagerConfig) *Manager {
	if cfg.Retention <= 0 {
		cfg.Retention = 24 * time.Hour
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 300 * time.Second
	}
	if cfg.ArtworkDirectory != "" {
		store.SetArtworkDirectory(cfg.ArtworkDirectory)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{store: store, client: client, cfg: cfg, ctx: ctx, cancel: cancel, one: make(chan struct{}, 1), jobs: map[string]*Job{}, settings: map[string]string{}}
}

func (m *Manager) Shutdown()          { m.cancel(); m.wg.Wait() }
func (m *Manager) MaxROMBytes() int64 { return m.cfg.MaxROMBytes }
func (m *Manager) Available() bool    { return m.client.Available() }

func (m *Manager) Create(ctx context.Context, o CreateOptions) (Job, error) {
	if !m.client.Available() {
		return Job{}, errors.New("randomizer sidecar is not configured")
	}
	var a catalog.Artifact
	var err error
	if len(o.SourceUpload) > 0 {
		if int64(len(o.SourceUpload)) > m.cfg.MaxROMBytes {
			return Job{}, errors.New("base ROM exceeds configured randomizing limit")
		}
		p, validationErr := catalog.ValidateROMUpload(o.SourceName, o.SourceUpload)
		if validationErr != nil {
			return Job{}, validationErr
		}
		if !acceptedPlatforms[p.ID] {
			return Job{}, errors.New("randomizer only supports Game Boy, Game Boy Color, Game Boy Advance, and Nintendo DS ROMs")
		}
		sum := sha256.Sum256(o.SourceUpload)
		a = catalog.Artifact{ID: "upload-" + hex.EncodeToString(sum[:12]), Platform: p.ID, Name: filepath.Base(o.SourceName), Extension: strings.ToLower(filepath.Ext(o.SourceName)), Size: int64(len(o.SourceUpload)), SHA256: hex.EncodeToString(sum[:]), CRC32: fmt.Sprintf("%08x", crc32.ChecksumIEEE(o.SourceUpload))}
	} else {
		a, err = m.store.EnsureArtifactHashed(ctx, o.ArtifactID)
		if err != nil {
			return Job{}, err
		}
		if !acceptedPlatforms[a.Platform] {
			return Job{}, errors.New("randomizer only supports Game Boy, Game Boy Color, Game Boy Advance, and Nintendo DS ROMs")
		}
	}
	if a.Size > m.cfg.MaxROMBytes {
		return Job{}, errors.New("ROM exceeds configured randomizing limit")
	}
	if o.SettingsJSON == "" {
		return Job{}, errors.New("settings are required")
	}
	var probe map[string]any
	if err = json.Unmarshal([]byte(o.SettingsJSON), &probe); err != nil {
		return Job{}, fmt.Errorf("invalid settings JSON: %w", err)
	}
	name, err := patch.ValidateOutputName(o.OutputName, a.Extension)
	if err != nil {
		return Job{}, err
	}
	final := filepath.Join(m.cfg.OutputDirectory, a.Platform, name)
	if _, err = os.Stat(final); err == nil {
		return Job{}, fmt.Errorf("output already exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Job{}, err
	}
	if len(o.Cover) > 0 {
		if _, err = patch.ValidateCover(o.Cover); err != nil {
			return Job{}, err
		}
	}
	id, err := randomID()
	if err != nil {
		return Job{}, err
	}
	now := time.Now()
	j := &Job{ID: id, Status: "queued", ArtifactID: a.ID, OutputName: name, Retain: o.Retain, CreatedAt: now, UpdatedAt: now}
	if !o.Retain {
		e := now.Add(m.cfg.Retention)
		j.ExpiresAt = &e
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.settings[id] = o.SettingsJSON
	m.mu.Unlock()
	m.persist(ctx, j)
	m.report(j, "Randomizing ROM "+a.Name, 0, 1)
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.run(a, o, final, j) }()
	return *j, nil
}

// Capabilities resolves a source ROM the same way Create does (library pick
// or direct upload) and asks the sidecar which misc tweaks that exact ROM's
// detected generation/handler actually supports, for the options form to hide
// choices that would otherwise be silently dropped at randomize time.
func (m *Manager) Capabilities(ctx context.Context, artifactID string, upload []byte, uploadName string) (Capabilities, error) {
	if !m.client.Available() {
		return Capabilities{}, errors.New("randomizer sidecar is not configured")
	}
	var source []byte
	var extension string
	if len(upload) > 0 {
		if int64(len(upload)) > m.cfg.MaxROMBytes {
			return Capabilities{}, errors.New("base ROM exceeds configured randomizing limit")
		}
		p, err := catalog.ValidateROMUpload(uploadName, upload)
		if err != nil {
			return Capabilities{}, err
		}
		if !acceptedPlatforms[p.ID] {
			return Capabilities{}, errors.New("randomizer only supports Game Boy, Game Boy Color, Game Boy Advance, and Nintendo DS ROMs")
		}
		source = upload
		extension = strings.ToLower(filepath.Ext(uploadName))
	} else {
		a, err := m.store.EnsureArtifactHashed(ctx, artifactID)
		if err != nil {
			return Capabilities{}, err
		}
		if !acceptedPlatforms[a.Platform] {
			return Capabilities{}, errors.New("randomizer only supports Game Boy, Game Boy Color, Game Boy Advance, and Nintendo DS ROMs")
		}
		if a.Size > m.cfg.MaxROMBytes {
			return Capabilities{}, errors.New("ROM exceeds configured randomizing limit")
		}
		source, err = readBounded(a.Path, m.cfg.MaxROMBytes)
		if err != nil {
			return Capabilities{}, err
		}
		extension = a.Extension
	}
	return m.client.Capabilities(ctx, source, extension)
}

func (m *Manager) Get(ctx context.Context, id string) (Job, error) {
	m.mu.RLock()
	j := m.jobs[id]
	if j != nil {
		snapshot := *j
		m.mu.RUnlock()
		return snapshot, nil
	}
	m.mu.RUnlock()
	record, err := m.store.RandomizerJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return Job{ID: record.ID, Status: record.Status, ArtifactID: record.ArtifactID, OutputName: record.OutputName, Error: record.Error, ResultArtifactID: record.ResultArtifactID, Retain: record.Retain, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, ExpiresAt: record.ExpiresAt}, nil
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
	m.update(ctx, j, "running")
	m.report(j, "Randomizing ROM "+a.Name, 0, 1)

	stage := filepath.Join(m.cfg.OutputDirectory, ".jobs", j.ID)
	if err := os.MkdirAll(stage, 0700); err != nil {
		m.fail(j, err)
		return
	}
	defer os.RemoveAll(stage)

	var source []byte
	var err error
	if len(o.SourceUpload) > 0 {
		source = o.SourceUpload
	} else {
		source, err = readBounded(a.Path, m.cfg.MaxROMBytes)
		if err != nil {
			m.fail(j, err)
			return
		}
	}

	output, err := m.client.Randomize(ctx, source, a.Extension, o.SettingsJSON)
	if err != nil {
		m.fail(j, err)
		return
	}
	m.report(j, "Validating randomized ROM "+a.Name, 1, 1)
	if int64(len(output)) > m.cfg.MaxROMBytes {
		m.fail(j, errors.New("randomized output exceeds configured ROM limit"))
		return
	}
	if len(output) == 0 {
		m.fail(j, errors.New("randomized output is empty"))
		return
	}
	if err = ctx.Err(); err != nil {
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

	if err = m.stageManualData(stage, o); err != nil {
		m.fail(j, err)
		return
	}
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
		return Job{}, errors.New("only a completed randomizer job can be made permanent")
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
	delete(m.settings, id)
	m.mu.Unlock()
	return m.store.DeleteRandomizerJob(ctx, id)
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
	m.report(j, "Completed · "+j.OutputName, 1, 1)
	return nil
}

// stageManualData/publishManualData mirror internal/patch's identically-shaped
// helpers: a randomized ROM is a catalog Game/Artifact exactly like a patched
// one, metadata staged alongside the output and committed only once the
// artifact exists.
func (m *Manager) stageManualData(stage string, o CreateOptions) error {
	g := catalog.Game{Title: strings.TrimSpace(o.Title), Developer: strings.TrimSpace(o.Developer), Publisher: strings.TrimSpace(o.Publisher), Description: strings.TrimSpace(o.Description), ReleaseDate: strings.TrimSpace(o.ReleaseDate), Genre: strings.TrimSpace(o.Genre), Region: strings.TrimSpace(o.Region)}
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(stage, "metadata.json"), b, 0600); err != nil {
		return err
	}
	if len(o.Cover) == 0 {
		return nil
	}
	ext, err := patch.ValidateCover(o.Cover)
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

func (m *Manager) fail(j *Job, err error) {
	m.mu.Lock()
	j.Status = "failed"
	j.Error = err.Error()
	j.UpdatedAt = time.Now()
	m.mu.Unlock()
	m.persist(context.Background(), j)
	m.report(j, "Failed · "+err.Error(), 0, 1)
}

func (m *Manager) update(ctx context.Context, j *Job, status string) {
	m.mu.Lock()
	j.Status = status
	j.UpdatedAt = time.Now()
	m.mu.Unlock()
	m.persist(ctx, j)
}

// persist writes the job row, pulling the settings JSON blob (never part of
// the public Job struct, so status polls don't carry it around) from the
// manager's own in-memory record set alongside the job.
func (m *Manager) persist(ctx context.Context, j *Job) {
	m.mu.RLock()
	snapshot := *j
	settingsJSON := m.settings[snapshot.ID]
	m.mu.RUnlock()
	_ = m.store.SaveRandomizerJob(ctx, catalog.RandomizerJobRecord{ID: snapshot.ID, Status: snapshot.Status, ArtifactID: snapshot.ArtifactID, OutputName: snapshot.OutputName, Error: snapshot.Error, ResultArtifactID: snapshot.ResultArtifactID, SettingsJSON: settingsJSON, Retain: snapshot.Retain, CreatedAt: snapshot.CreatedAt, UpdatedAt: snapshot.UpdatedAt, ExpiresAt: snapshot.ExpiresAt})
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

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
