package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/threedscrypt"
)

// uploadJob tracks one ad-hoc convert or encrypt/decrypt request from upload
// through to a downloadable result. It lives in memory only, a restart loses
// in-flight jobs the same way the background-job activity widget does, and
// for the same reason: none of this is meant to survive one.
type uploadJob struct {
	mu         sync.Mutex
	Status     string `json:"status"` // "running", "completed", "failed"
	Error      string `json:"error,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	// Direction records what a crypt job actually did, so the poll response
	// can say "decrypted" rather than leaving the caller to infer it.
	Direction string `json:"direction,omitempty"`
}

func (j *uploadJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = "failed"
	j.Error = err.Error()
}
func (j *uploadJob) complete(artifactID, direction string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = "completed"
	j.ArtifactID = artifactID
	j.Direction = direction
}
func (j *uploadJob) snapshot() uploadJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return uploadJob{Status: j.Status, Error: j.Error, ArtifactID: j.ArtifactID, Direction: j.Direction}
}

// cryptAvailable reports whether either half of the crypt upload flow can
// currently do anything: the NDecrypt sidecar (.3ds/.cci/.nds) or the Redux
// driver bundle (.cia, decrypt only). Either alone is enough to show the tool.
func (s *CatalogServer) cryptAvailable() bool {
	if s.crypt != nil && s.crypt.Available() {
		return true
	}
	if s.decryptor != nil {
		if available, _ := s.decryptor.Available(); available {
			return true
		}
	}
	return false
}

func newJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// maxUploadBytes bounds a convert/crypt upload body. This is deliberately its
// own ceiling rather than borrowing the patch workspace's MaxROMBytes: that
// one is sized for the ROMs a patch actually targets (GBA/SNES-class, tens of
// MB), while a raw 3DS/NDS cart dump routinely runs 1-4+ GB. Reusing the
// smaller number here rejected every real 3DS ROM outright.
const maxUploadBytes = 4 << 30 // 4 GiB

func (s *CatalogServer) maxUploadBytes() int64 { return maxUploadBytes }

// platformDirFor returns the catalog platform directory name (e.g. "3ds") an
// extension belongs to. Reconcile's DetectPlatform requires a file to sit one
// level under a recognized platform directory before it will index it at
// all, a flat upload-results folder is silently invisible to it, not an
// error, which is exactly the bug this closes.
func platformDirFor(ext string) (string, bool) {
	ext = strings.ToLower(ext)
	for _, p := range catalog.Platforms {
		for _, allowed := range p.Extensions {
			if allowed == ext {
				return p.Directory, true
			}
		}
	}
	return "", false
}

// publishUploadResult writes data into the shared upload-results root
// (registered once at startup via Store.AddArtifactRoot) and reconciles it
// into the catalog as a normal artifact, temporary and expiring unless the
// caller retains it, exactly like a patch job's output. jobID prefixes the
// filename so two concurrent uploads producing the same basename never
// collide.
func (s *CatalogServer) publishUploadResult(ctx context.Context, jobID, name string, data []byte) (catalog.Artifact, error) {
	if s.uploadResultsDir == "" {
		return catalog.Artifact{}, errors.New("upload staging directory is not configured")
	}
	platformDir, ok := platformDirFor(filepath.Ext(name))
	if !ok {
		return catalog.Artifact{}, fmt.Errorf("%s has no recognized platform to catalog it under", filepath.Ext(name))
	}
	dir := filepath.Join(s.uploadResultsDir, platformDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return catalog.Artifact{}, err
	}
	path := filepath.Join(dir, jobID+"-"+filepath.Base(name))
	if err := os.WriteFile(path, data, 0600); err != nil {
		return catalog.Artifact{}, err
	}
	if err := s.store.Reconcile(ctx); err != nil {
		return catalog.Artifact{}, err
	}
	return s.store.ArtifactByPath(ctx, path)
}

// retainIfRequested applies the same "keep this permanently" checkbox
// convention patch job creation already uses (name="retain" value="true"),
// against whatever artifact an upload job just produced.
func (s *CatalogServer) retainIfRequested(ctx context.Context, path string, retain bool) {
	if !retain {
		return
	}
	_ = s.store.MarkArtifactRetention(ctx, path, true, time.Time{})
}

// readCatalogArtifact loads an existing Library ROM's bytes from disk by
// catalog ID, so Convert/Crypt can run against a file already on the Relay
// without the browser downloading and re-uploading it. The artifact itself is
// never touched, same "copy, then hand off to the same pipeline an upload
// would use" as readUploadedFile.
func (s *CatalogServer) readCatalogArtifact(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	a, err := s.store.Artifact(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "catalog file not found", http.StatusNotFound)
		return nil, "", false
	}
	info, err := os.Stat(a.Path)
	if err != nil {
		http.Error(w, "catalog file is missing on disk", http.StatusNotFound)
		return nil, "", false
	}
	if info.Size() > s.maxUploadBytes() {
		http.Error(w, "file exceeds this server's configured size limit", http.StatusRequestEntityTooLarge)
		return nil, "", false
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		http.Error(w, "could not read catalog file", http.StatusInternalServerError)
		return nil, "", false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return nil, "", false
	}
	return data, a.Name, true
}

func (s *CatalogServer) readUploadedFile(w http.ResponseWriter, r *http.Request, field string) ([]byte, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUploadBytes()+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "invalid or oversized multipart request", http.StatusBadRequest)
		return nil, "", false
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile(field)
	if err != nil {
		http.Error(w, "choose a file to upload", http.StatusBadRequest)
		return nil, "", false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, s.maxUploadBytes()+1))
	if err != nil || int64(len(data)) > s.maxUploadBytes() {
		http.Error(w, "upload exceeds this server's configured size limit", http.StatusRequestEntityTooLarge)
		return nil, "", false
	}
	return data, header.Filename, true
}

// createConvertUpload runs the same .3ds/.cci -> encrypted CIA conversion the
// admin single-artifact flow does (internal/converter, via ConvertUploadFunc),
// but starting from an ad-hoc upload instead of a catalog artifact ID.
func (s *CatalogServer) createConvertUpload(w http.ResponseWriter, r *http.Request) {
	if s.convertUpload == nil {
		http.Error(w, "3DS to CIA conversion is unavailable", http.StatusServiceUnavailable)
		return
	}
	data, name, ok := s.readUploadedFile(w, r, "rom")
	if !ok {
		return
	}
	s.runConvertJob(w, r, name, data)
}

// createConvertFromLibrary is createConvertUpload's sibling for a ROM that's
// already in the Library: same conversion, same job/download machinery, just
// sourced from disk instead of a fresh upload.
func (s *CatalogServer) createConvertFromLibrary(w http.ResponseWriter, r *http.Request) {
	if s.convertUpload == nil {
		http.Error(w, "3DS to CIA conversion is unavailable", http.StatusServiceUnavailable)
		return
	}
	data, name, ok := s.readCatalogArtifact(w, r)
	if !ok {
		return
	}
	s.runConvertJob(w, r, name, data)
}

func (s *CatalogServer) runConvertJob(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".3ds" && ext != ".cci" {
		http.Error(w, "only .3ds and .cci files can be converted to CIA", http.StatusUnprocessableEntity)
		return
	}
	retain := r.FormValue("retain") == "true"
	id, err := newJobID()
	if err != nil {
		http.Error(w, "could not start job", http.StatusInternalServerError)
		return
	}
	job := &uploadJob{Status: "running"}
	s.uploadMu.Lock()
	s.uploadJobs[id] = job
	s.uploadMu.Unlock()

	go func() {
		ctx := context.Background()
		result, err := s.convertUpload(ctx, name, data, nil)
		if err != nil {
			job.fail(err)
			return
		}
		outName := strings.TrimSuffix(filepath.Base(name), ext) + ".cia"
		a, err := s.publishUploadResult(ctx, id, outName, result)
		if err != nil {
			job.fail(err)
			return
		}
		s.retainIfRequested(ctx, a.Path, retain)
		job.complete(a.ID, "")
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "running"})
}

// createCryptUpload encrypts or decrypts an uploaded cart/CIA. For
// .3ds/.cci/.nds/.srl/.dsi/.ids it runs through the NDecrypt sidecar and can
// go either direction; for .cia it can only decrypt, through the existing
// Redux driver (internal/decryptor), which has no encrypt direction at all.
//
// Direction is auto-detected from the file's own current state (see
// threedscrypt.Direction) and only requested explicitly (the "direction" form
// field, sent on a client retry) when that state is a genuine mix with no
// single right answer, the file is never re-uploaded to ask, the browser
// still holds it from the first submission.
func (s *CatalogServer) createCryptUpload(w http.ResponseWriter, r *http.Request) {
	data, name, ok := s.readUploadedFile(w, r, "rom")
	if !ok {
		return
	}
	s.runCryptJob(w, r, name, data)
}

// createCryptFromLibrary is createCryptUpload's sibling for a ROM/CIA that's
// already in the Library: same encrypt/decrypt pipeline, sourced from disk
// instead of a fresh upload.
func (s *CatalogServer) createCryptFromLibrary(w http.ResponseWriter, r *http.Request) {
	data, name, ok := s.readCatalogArtifact(w, r)
	if !ok {
		return
	}
	s.runCryptJob(w, r, name, data)
}

func (s *CatalogServer) runCryptJob(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	ext := strings.ToLower(filepath.Ext(name))
	retain := r.FormValue("retain") == "true"
	force := r.FormValue("force") == "true"
	requestedDirection := r.FormValue("direction") // "encrypt", "decrypt", or "" (auto)

	var run func(ctx context.Context) (result []byte, direction string, err error)
	switch {
	case ext == ".cia":
		if s.decryptor == nil {
			http.Error(w, "CIA decryption is unavailable", http.StatusServiceUnavailable)
			return
		}
		if available, reason := s.decryptor.Available(); !available {
			http.Error(w, "CIA decryption is unavailable: "+reason, http.StatusServiceUnavailable)
			return
		}
		if requestedDirection == "encrypt" {
			http.Error(w, "CIA files can only be decrypted, not encrypted - there is no encrypt driver for this format", http.StatusUnprocessableEntity)
			return
		}
		run = func(ctx context.Context) ([]byte, string, error) {
			out, err := s.decryptor.PrepareBytes(ctx, name, data)
			return out, "decrypt", err
		}
	case threedscrypt.SupportsExtension(ext):
		if s.crypt == nil || !s.crypt.Available() {
			http.Error(w, "encrypt/decrypt is unavailable", http.StatusServiceUnavailable)
			return
		}
		decrypt, ok := true, false
		switch requestedDirection {
		case "encrypt":
			decrypt, ok = false, true
		case "decrypt":
			decrypt, ok = true, true
		default:
			statuses, err := s.crypt.Info(r.Context(), ext, data)
			if err != nil {
				http.Error(w, "could not read this file: "+err.Error(), http.StatusUnprocessableEntity)
				return
			}
			decrypt, ok = threedscrypt.Direction(statuses)
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"needs_direction": true,
					"partitions":      statuses,
					"message":         "this file has a mix of encrypted and decrypted partitions; choose a direction and resubmit",
				})
				return
			}
		}
		run = func(ctx context.Context) ([]byte, string, error) {
			var out []byte
			var err error
			if decrypt {
				out, err = s.crypt.Decrypt(ctx, ext, data, force)
			} else {
				out, err = s.crypt.Encrypt(ctx, ext, data, force)
			}
			if err != nil {
				return nil, "", err
			}
			if verifyErr := s.crypt.EnsureComplete(ctx, ext, out, decrypt); verifyErr != nil {
				return nil, "", verifyErr
			}
			direction := "encrypt"
			if decrypt {
				direction = "decrypt"
			}
			return out, direction, nil
		}
	default:
		http.Error(w, "unrecognized file type: expected .nds, .srl, .dsi, .ids, .3ds, .cci, or .cia", http.StatusUnprocessableEntity)
		return
	}

	id, err := newJobID()
	if err != nil {
		http.Error(w, "could not start job", http.StatusInternalServerError)
		return
	}
	job := &uploadJob{Status: "running"}
	s.uploadMu.Lock()
	s.uploadJobs[id] = job
	s.uploadMu.Unlock()

	go func() {
		ctx := context.Background()
		result, direction, err := run(ctx)
		if err != nil {
			job.fail(err)
			return
		}
		a, err := s.publishUploadResult(ctx, id, filepath.Base(name), result)
		if err != nil {
			job.fail(err)
			return
		}
		s.retainIfRequested(ctx, a.Path, retain)
		job.complete(a.ID, direction)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "running"})
}

// getUploadJob polls a convert or crypt job (both id spaces share one map;
// IDs are random 128-bit values, so cross-kind collision is not a practical
// concern). Once completed, it resolves the produced artifact into the same
// download/QR links the game page uses, so the client needs no separate
// download machinery for upload results.
func (s *CatalogServer) getUploadJob(w http.ResponseWriter, r *http.Request) {
	s.uploadMu.Lock()
	job, found := s.uploadJobs[r.PathValue("id")]
	s.uploadMu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	snap := job.snapshot()
	out := map[string]any{"status": snap.Status}
	if snap.Error != "" {
		out["error"] = snap.Error
	}
	if snap.Direction != "" {
		out["direction"] = snap.Direction
	}
	if snap.Status == "completed" {
		a, err := s.store.Artifact(r.Context(), snap.ArtifactID)
		if err != nil {
			out["status"] = "failed"
			out["error"] = "result artifact is no longer available"
		} else {
			out["download_url"] = s.downloadURL(r, a)
			out["qr_url"] = "/qr/" + a.ID + ".png" + tokenQuery(r)
			out["filename"] = a.Name
			out["artifact_id"] = a.ID
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
