package api

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) handleNodeWireGuardRuntime(w http.ResponseWriter, r *http.Request, nodeID int64) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	runtime, err := s.nodeController.WGRuntime(ctx, nodeID)
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runtime)
}
