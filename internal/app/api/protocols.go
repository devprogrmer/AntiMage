package api

import (
	"net/http"

	"github.com/antimage/antimage/internal/protocols"
)

// handleProtocols exposes the single AntiMage capability contract to the
// dashboard and other operator clients.
func (s *Server) handleProtocols(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"protocols": protocols.All(),
	})
}
