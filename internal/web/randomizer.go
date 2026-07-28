package web

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"

	"romrelay/internal/randomizer"
)

// randomizerPage serves the ROM Randomizer view, under Pokémon Tools. Filled by
// the client from /api/randomizer-jobs; the surrounding auth middleware already
// requires a signed-in session for any non-public route, so unlike the public
// ROM Patcher there is no separate CSRF-cookie dance here.
func (s *CatalogServer) randomizerPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "randomizer", "randomizer", s.newPageData(r))
}

func (s *CatalogServer) createRandomizeJob(w http.ResponseWriter, r *http.Request) {
	if s.randomizer == nil || !s.randomizer.Available() {
		http.Error(w, "randomizer is unavailable", http.StatusServiceUnavailable)
		return
	}
	// Settings JSON is small (a flat object over ~150 keys); the ROM is the only
	// large part, bounded the same way the patch tool bounds its ROM+patch total.
	r.Body = http.MaxBytesReader(w, r.Body, s.randomizer.MaxROMBytes()+(8<<20)+(512<<10))
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Error(w, "invalid or oversized multipart request", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	o := randomizer.CreateOptions{
		ArtifactID:   r.FormValue("artifact_id"),
		OutputName:   r.FormValue("output_filename"),
		Title:        r.FormValue("title"),
		Developer:    r.FormValue("developer"),
		Publisher:    r.FormValue("publisher"),
		Description:  r.FormValue("description"),
		ReleaseDate:  r.FormValue("release_date"),
		Genre:        r.FormValue("genre"),
		Region:       r.FormValue("region"),
		SettingsJSON: r.FormValue("settings"),
		Retain:       r.FormValue("retain") == "true",
	}
	var err error
	// A base ROM comes from an upload or from an existing library artifact, the
	// same duality the ROM Patcher offers; the picked artifact is never touched,
	// Manager.Create copies it into the job's own working file.
	if o.ArtifactID == "" {
		var base multipart.File
		var baseHeader *multipart.FileHeader
		base, baseHeader, err = r.FormFile("rom")
		if err != nil {
			http.Error(w, "choose a ROM from the library or upload one", http.StatusBadRequest)
			return
		}
		defer base.Close()
		o.SourceUpload, err = io.ReadAll(io.LimitReader(base, s.randomizer.MaxROMBytes()+1))
		if err != nil || int64(len(o.SourceUpload)) > s.randomizer.MaxROMBytes() {
			http.Error(w, "base ROM exceeds configured limit", http.StatusRequestEntityTooLarge)
			return
		}
		o.SourceName = baseHeader.Filename
	}
	if file, _, fileErr := r.FormFile("cover"); fileErr == nil {
		defer file.Close()
		o.Cover, err = io.ReadAll(io.LimitReader(file, (8<<20)+1))
		if err != nil || len(o.Cover) > 8<<20 {
			http.Error(w, "cover exceeds 8 MiB limit", http.StatusRequestEntityTooLarge)
			return
		}
	} else if !errors.Is(fileErr, http.ErrMissingFile) {
		http.Error(w, "invalid cover upload", http.StatusBadRequest)
		return
	}
	job, err := s.randomizer.Create(r.Context(), o)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// randomizeCapabilities reports which misc tweaks the sidecar's real ROM
// detection supports for a given library artifact or direct upload, so the
// options form can hide choices that don't apply to this ROM's generation.
func (s *CatalogServer) randomizeCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.randomizer == nil || !s.randomizer.Available() {
		http.Error(w, "randomizer is unavailable", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.randomizer.MaxROMBytes()+(1<<20))
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Error(w, "invalid or oversized multipart request", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	artifactID := r.FormValue("artifact_id")
	var upload []byte
	var uploadName string
	if artifactID == "" {
		file, header, err := r.FormFile("rom")
		if err != nil {
			http.Error(w, "choose a ROM from the library or upload one", http.StatusBadRequest)
			return
		}
		defer file.Close()
		upload, err = io.ReadAll(io.LimitReader(file, s.randomizer.MaxROMBytes()+1))
		if err != nil || int64(len(upload)) > s.randomizer.MaxROMBytes() {
			http.Error(w, "base ROM exceeds configured limit", http.StatusRequestEntityTooLarge)
			return
		}
		uploadName = header.Filename
	}
	caps, err := s.randomizer.Capabilities(r.Context(), artifactID, upload, uploadName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, caps)
}

func (s *CatalogServer) getRandomizeJob(w http.ResponseWriter, r *http.Request) {
	if s.randomizer == nil {
		http.Error(w, "randomizer is unavailable", http.StatusServiceUnavailable)
		return
	}
	job, err := s.randomizer.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *CatalogServer) retainRandomizeJob(w http.ResponseWriter, r *http.Request) {
	if s.randomizer == nil {
		http.Error(w, "randomizer is unavailable", http.StatusServiceUnavailable)
		return
	}
	job, err := s.randomizer.Retain(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
