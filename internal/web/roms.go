package web

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// adminRoms serves the ROM Library management screen. Like the Patch Silo's
// metadata editor, the list itself is filled client-side from the existing
// /api/admin/games endpoint, no new listing endpoint needed.
func (s *CatalogServer) adminRoms(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	if s.adminTokenLanding(w, r, "/admin/roms") {
		return
	}
	s.render(w, "adminroms", "adminroms", s.newPageData(r))
}

// deleteArtifact removes one cataloged file: the file on disk, then a
// Reconcile so the artifact's row (and its game, if this was the game's last
// artifact, and that game's artwork) go with it. This is the same sequence
// patcher.Manager.Delete already uses to remove a patched result, reused
// here for any artifact, not only patch-job output.
func (s *CatalogServer) deleteArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.adminCSRFOK(w, r) {
		return
	}
	ctx := r.Context()
	a, err := s.store.Artifact(ctx, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	if err := os.Remove(a.Path); err != nil && !os.IsNotExist(err) {
		http.Error(w, "could not remove the file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.Reconcile(ctx); err != nil {
		http.Error(w, "file removed, but the catalog could not be reconciled: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.store.RemoveGameArtworkIfOrphan(ctx, a.GameID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": a.ID, "name": a.Name})
}

// createLibraryUpload adds a new ROM straight into the Library from the
// browser, instead of needing sftp access to the library root. It writes the
// file under its platform directory (the same layout Reconcile requires -
// see platformDirFor), reconciles it into the catalog immediately, and hashes
// it synchronously so it shows up ready rather than pending. Cover art and
// other metadata are left to the existing scheduled sweep (Libretro/IGDB
// enrich only take a whole-table cutoff, not a single artifact) - it appears
// shortly after, not instantly.
func (s *CatalogServer) createLibraryUpload(w http.ResponseWriter, r *http.Request) {
	data, name, ok := s.readUploadedFile(w, r, "rom")
	if !ok {
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	platformDir, known := platformDirFor(ext)
	if !known {
		http.Error(w, "unrecognized file extension: "+ext, http.StatusUnprocessableEntity)
		return
	}
	dir := filepath.Join(s.store.Root(), platformDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, "could not prepare library storage: "+err.Error(), http.StatusInternalServerError)
		return
	}
	path := filepath.Join(dir, filepath.Base(name))
	if _, err := os.Stat(path); err == nil {
		http.Error(w, "a file named "+filepath.Base(name)+" already exists in the library", http.StatusConflict)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		http.Error(w, "could not write the file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	if err := s.store.Reconcile(ctx); err != nil {
		http.Error(w, "file saved, but the catalog could not be reconciled: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a, err := s.store.ArtifactByPath(ctx, path)
	if err != nil {
		http.Error(w, "file saved, but could not be found in the catalog: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := s.store.EnsureArtifactHashed(ctx, a.ID); err != nil {
		http.Error(w, "file saved, but hashing failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": a.ID, "name": a.Name, "game_id": a.GameID})
}
