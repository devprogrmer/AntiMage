package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/antimage/antimage/internal/app/nodecontroller"
	operationapp "github.com/antimage/antimage/internal/app/operations"
)

func (s *Server) handleRollouts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		all, err := operationapp.List(ctx, s.db, 500)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to read rollouts")
			return
		}
		items := []operationapp.Operation{}
		for _, op := range all {
			if op.Type == "node_rollout" {
				items = append(items, publicOperation(op))
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"rollouts": items})
	case http.MethodPost:
		var req nodecontroller.RolloutRequest
		if err := decodeOptionalJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid rollout request")
			return
		}
		view, err := s.nodeController.CreateRollout(ctx, req)
		if err != nil {
			writeUpdateControllerError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, publicRolloutView(view))
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleRolloutPath(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/nodes/rollouts/"), "/"), "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var view nodecontroller.RolloutView
	var err error
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		view, err = s.nodeController.Rollout(ctx, parts[0])
	case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
		view, err = s.nodeController.CancelRollout(ctx, parts[0])
	case len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost:
		view, err = s.nodeController.RetryRollout(ctx, parts[0])
	case len(parts) == 2 && parts[1] == "start" && r.Method == http.MethodPost:
		var req struct {
			Confirm bool `json:"confirm"`
		}
		if err := decodeOptionalJSON(r, &req); err != nil || !req.Confirm {
			writeError(w, http.StatusBadRequest, "explicit rollout confirmation is required")
			return
		}
		view, err = s.nodeController.StartRollout(ctx, parts[0])
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err != nil {
		writeUpdateControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicRolloutView(view))
}
