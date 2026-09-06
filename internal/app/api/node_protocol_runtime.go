package api

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleNodeProtocolRuntime(w http.ResponseWriter, r *http.Request, nodeID int64, protocol string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	runtime, err := s.nodeController.ProtocolRuntime(ctx, nodeID, strings.TrimSpace(protocol))
	if err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runtime)
}
