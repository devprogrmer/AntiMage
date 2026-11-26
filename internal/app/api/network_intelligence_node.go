package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
)

func (s *Server) handleNetworkIntelligenceNode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := r.Context().Value(adminContextKey).(adminPrincipal)
	if !ok || p.Role != string(adminapp.RoleFullAccess) {
		writeError(w, http.StatusForbidden, "You're not allowed")
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("node_id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "valid node_id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	snapshot, err := s.nodeController.NetworkIntelligence(ctx, id)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Network telemetry unavailable")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
