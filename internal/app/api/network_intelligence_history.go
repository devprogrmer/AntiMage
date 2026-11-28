package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

func (s *Server) handleNetworkIntelligenceHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal, ok := r.Context().Value(adminContextKey).(adminPrincipal)
	if !ok || principal.Role != string(adminapp.RoleFullAccess) {
		writeError(w, http.StatusForbidden, "You're not allowed")
		return
	}
	nodeID, nodeErr := strconv.ParseInt(r.URL.Query().Get("node_id"), 10, 64)
	userID, userErr := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	if nodeErr != nil || userErr != nil || nodeID <= 0 || userID <= 0 {
		writeError(w, http.StatusBadRequest, "valid node_id and user_id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if s.cfg.NetworkHistoryPersistence {
		repo := &ni.HistoryRepository{DB: s.db}
		points, err := repo.List(ctx, nodeID, userID, time.Now().Add(-7*24*time.Hour))
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Network history unavailable")
			return
		}
		result := networkHistoryResponse{Storage: "durable", Collection: "background", Available: true, Truncated: len(points) == 500, Points: []json.RawMessage{}}
		for _, p := range points {
			row, err := json.Marshal(map[string]any{
				"node_id": p.NodeID, "user_id": p.UserID, "at": p.ObservedAt,
				"method": "tcp_ack_upload_observation", "ack_rtt_ms": p.ACKRTTMilliseconds,
				"jitter_ms": p.JitterMilliseconds, "retransmission_delta": p.RetransmissionDelta,
				"findings": ni.Diagnose(ni.DiagnosticInput{
					ObservedAt: p.ObservedAt, ACKRTT: millisecondsDuration(p.ACKRTTMilliseconds),
					Jitter: millisecondsDuration(p.JitterMilliseconds),
				}, p.ObservedAt),
			})
			if err != nil {
				writeError(w, http.StatusBadGateway, "Invalid network history")
				return
			}
			result.Points = append(result.Points, row)
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	raw, err := s.nodeController.NetworkIntelligence(ctx, nodeID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Network history unavailable")
		return
	}
	history, err := filterNetworkIntelligenceHistory(raw, nodeID, userID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Invalid network history")
		return
	}
	writeJSON(w, http.StatusOK, history)
}

type networkHistoryResponse struct {
	Storage    string            `json:"storage"`
	Collection string            `json:"collection"`
	Available  bool              `json:"available"`
	Truncated  bool              `json:"truncated"`
	Points     []json.RawMessage `json:"points"`
}

// Filtering applies after the node's global response cap. Truncated remains
// true so callers can distinguish a partial user history from a full series.
func filterNetworkIntelligenceHistory(raw json.RawMessage, nodeID, userID int64) (networkHistoryResponse, error) {
	var snapshot struct {
		History *struct {
			Storage    string            `json:"storage"`
			Collection string            `json:"collection"`
			Truncated  bool              `json:"truncated"`
			Points     []json.RawMessage `json:"points"`
		} `json:"history"`
	}
	result := networkHistoryResponse{Points: []json.RawMessage{}}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return result, err
	}
	if snapshot.History == nil {
		return result, nil
	}
	result.Available = true
	result.Storage = snapshot.History.Storage
	result.Collection = snapshot.History.Collection
	result.Truncated = snapshot.History.Truncated
	for _, rawPoint := range snapshot.History.Points {
		var point struct {
			NodeID int64 `json:"node_id"`
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(rawPoint, &point); err != nil {
			return result, err
		}
		if point.NodeID == nodeID && point.UserID == userID {
			result.Points = append(result.Points, rawPoint)
		}
	}
	return result, nil
}
