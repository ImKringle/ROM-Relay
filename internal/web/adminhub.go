package web

import "net/http"

// adminHubStats is the hub's quick-glance row. Both counts are read from
// stores that already track them for other purposes (RuntimeStats feeds the
// catalog health endpoint; Users feeds the Users page), nothing new is
// computed here.
type adminHubStats struct {
	Games, Files, Users int
}

type adminHubPage struct {
	pageData
	Stats adminHubStats
}

// adminHub is the landing page the "Admin" nav link goes to. It replaces the
// dropdown ("the waffle") that used to sit under that link: one real page,
// reached by one real click, with cards to every admin capability.
func (s *CatalogServer) adminHub(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		http.Error(w, s.word("admin authentication required", "the corn crib needs its secret token"), http.StatusUnauthorized)
		return
	}
	if s.adminTokenLanding(w, r, "/admin") {
		return
	}
	ctx := r.Context()
	stats := adminHubStats{}
	if rt, err := s.store.RuntimeStats(ctx); err == nil {
		stats.Games, stats.Files = rt.Games, rt.Files
	}
	if users, err := s.store.Users(ctx); err == nil {
		stats.Users = len(users)
	}
	data := adminHubPage{pageData: s.newPageData(r), Stats: stats}
	s.render(w, "adminhub", "adminhub", data)
}
