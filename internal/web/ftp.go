package web

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"romrelay/internal/catalog"
	"romrelay/internal/ftpxfer"
)

// ftpTarget parses and validates the host/port pair a request supplied for an
// FTP push. As with the FBI queue (catalogQueue), the target must be a
// private IPv4 address: ROM Relay only ever talks to a device on the caller's
// own LAN, never acts as a relay toward an arbitrary host on the internet.
func ftpTarget(r *http.Request) (string, error) {
	target := net.ParseIP(strings.TrimSpace(r.FormValue("host")))
	if target == nil || target.To4() == nil || !target.IsPrivate() {
		return "", errors.New("enter the private IPv4 address shown by your FTP server")
	}
	portText := strings.TrimSpace(r.FormValue("port"))
	if portText == "" {
		portText = "5000" // ftpd's own default port
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("enter a valid port number")
	}
	return net.JoinHostPort(target.String(), strconv.Itoa(port)), nil
}

// artifactsSelected resolves artifact IDs to their catalog records. Unlike
// ciaURLsSelected (which is FBI/CIA-specific), FTP push moves any file
// already in the library to any homebrew FTP server, so every platform and
// extension is eligible.
func (s *CatalogServer) artifactsSelected(ctx context.Context, selected map[string]bool) ([]catalog.Artifact, error) {
	games, err := s.store.Games(ctx)
	if err != nil {
		return nil, err
	}
	var out []catalog.Artifact
	for _, g := range games {
		for _, a := range g.Artifacts {
			if len(selected) == 0 || selected[a.ID] {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

func (s *CatalogServer) ftpBrowse(w http.ResponseWriter, r *http.Request) {
	addr, err := ftpTarget(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path := strings.TrimSpace(r.FormValue("path"))
	entries, err := ftpxfer.List(r.Context(), addr, r.FormValue("user"), r.FormValue("pass"), path)
	if err != nil {
		http.Error(w, "could not browse that FTP server: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"path": path, "entries": entries})
}

func (s *CatalogServer) ftpSend(w http.ResponseWriter, r *http.Request) {
	addr, err := ftpTarget(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path := strings.TrimSpace(r.FormValue("path"))
	selected := selectedArtifactIDs(r.FormValue("artifact_ids"))
	if len(selected) == 0 {
		http.Error(w, "select at least one file to send", http.StatusBadRequest)
		return
	}
	artifacts, err := s.artifactsSelected(r.Context(), selected)
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	files := make([]ftpxfer.SendFile, 0, len(artifacts))
	for _, a := range artifacts {
		files = append(files, ftpxfer.SendFile{Name: a.Name, Path: a.Path})
	}
	results, err := ftpxfer.Send(r.Context(), addr, r.FormValue("user"), r.FormValue("pass"), path, files)
	if err != nil {
		http.Error(w, "could not send to that FTP server: "+err.Error(), http.StatusBadGateway)
		return
	}
	sent := 0
	for _, res := range results {
		if res.Error == "" {
			sent++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sent": sent, "results": results})
}
