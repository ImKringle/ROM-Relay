package web

import "net/http"

// adminHubStats is the hub's quick-glance row. Both counts are read from
// stores that already track them for other purposes (RuntimeStats feeds the
// catalog health endpoint; Users feeds the Users page), nothing new is
// computed here.
type adminHubStats struct {
	Games, Files, Users int
}

// apiAdminOverview is the admin hub's quick-glance numbers, for the React UI.
func (s *CatalogServer) apiAdminOverview(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, "admin authentication required", http.StatusUnauthorized)
		return
	}
	stats := adminHubStats{}
	if rt, err := s.store.RuntimeStats(r.Context()); err == nil {
		stats.Games, stats.Files = rt.Games, rt.Files
	}
	if users, err := s.store.Users(r.Context()); err == nil {
		stats.Users = len(users)
	}
	writeJSON(w, http.StatusOK, map[string]int{"games": stats.Games, "files": stats.Files, "users": stats.Users})
}
