package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi/openapi.json
var openAPIJSON []byte

func (s *Server) handleOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	if !s.apiDocsEnabled.Load() {
		s.handleDocsDisabled(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPIJSON)
}

func (s *Server) handleDocsDisabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeError(w, http.StatusNotFound, "API docs are disabled")
}

func handleLegacyDocsRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	http.Redirect(w, r, "/guide/api", http.StatusMovedPermanently)
}
