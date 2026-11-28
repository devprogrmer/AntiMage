package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type networkOptimizationRequest struct {
	Action      string `json:"action"`
	NodeID      int64  `json:"node_id"`
	UserID      int64  `json:"user_id"`
	OperationID string `json:"operation_id"`
	TCPMSS      uint32 `json:"tcp_mss,omitempty"`
}

func (s *Server) networkIntelligenceOptimizationRoute() http.HandlerFunc {
	if !s.cfg.NetworkIntelligenceEnabled && !s.cfg.NetworkOptimizationEnabled {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, http.StatusNotFound, "Network optimization is disabled")
		}
	}
	return s.requireAdmin(s.handleNetworkIntelligenceOptimization)
}

func (s *Server) handleNetworkIntelligenceOptimization(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal, ok := r.Context().Value(adminContextKey).(adminPrincipal)
	if !ok || principal.ID <= 0 || principal.Role != string(adminapp.RoleFullAccess) {
		writeError(w, http.StatusForbidden, "You're not allowed")
		return
	}
	if !s.cfg.NetworkIntelligenceEnabled && !s.cfg.NetworkOptimizationEnabled {
		writeError(w, http.StatusNotFound, "Network optimization is disabled")
		return
	}
	if r.Method == http.MethodGet {
		s.handleNetworkIntelligenceOptimizationList(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var input networkOptimizationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.NodeID <= 0 || input.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "node_id and user_id are required")
		return
	}
	var request *nodev1.NetworkOptimizationRequest
	var actionType, summary string
	switch strings.TrimSpace(input.Action) {
	case "apply_tcp_mss":
		if !s.cfg.NetworkIntelligenceEnabled || !s.cfg.NetworkOptimizationEnabled {
			writeError(w, http.StatusNotFound, "Network optimization is disabled")
			return
		}
		if input.TCPMSS < 536 || input.TCPMSS > 1340 || input.OperationID != "" {
			writeError(w, http.StatusBadRequest, "tcp_mss must be 536..1340 and operation_id must be omitted for apply")
			return
		}
		operationID, err := newNetworkOptimizationOperationID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not allocate operation identity")
			return
		}
		input.OperationID = operationID
		request = &nodev1.NetworkOptimizationRequest{Action: nodev1.NetworkOptimizationRequest_APPLY_TCP_MSS, NodeId: input.NodeID, UserId: input.UserID, OperationId: input.OperationID, TcpMss: input.TCPMSS}
		actionType = "network.optimization.apply"
		summary = fmt.Sprintf("Requested per-user WireGuard TCP MSS %d on node %d for user %d", input.TCPMSS, input.NodeID, input.UserID)
	case "rollback":
		if len(input.OperationID) != 32 || input.TCPMSS != 0 {
			writeError(w, http.StatusBadRequest, "rollback requires a 32-character operation_id and no tcp_mss")
			return
		}
		request = &nodev1.NetworkOptimizationRequest{Action: nodev1.NetworkOptimizationRequest_ROLLBACK, NodeId: input.NodeID, UserId: input.UserID, OperationId: input.OperationID}
		actionType = "network.optimization.rollback"
		summary = fmt.Sprintf("Requested rollback of per-user network optimization on node %d for user %d", input.NodeID, input.UserID)
	default:
		writeError(w, http.StatusBadRequest, "unsupported optimization action")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := s.recordNetworkOptimizationAudit(ctx, actionType, input, summary); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Could not record the required audit entry")
		return
	}
	response, err := s.nodeController.ManageNetworkOptimization(ctx, request)
	if err != nil {
		_ = s.updateNetworkOptimizationAudit(context.WithoutCancel(r.Context()), actionType, input.OperationID, "Network optimization failed: "+safeNetworkOptimizationError(err))
		writeError(w, http.StatusServiceUnavailable, "Node could not safely apply or roll back the requested optimization")
		return
	}
	if actionType == "network.optimization.apply" {
		if err := s.updateNetworkOptimizationAudit(ctx, actionType, input.OperationID, fmt.Sprintf("Applied per-user WireGuard TCP MSS %d on node %d for user %d", input.TCPMSS, input.NodeID, input.UserID)); err != nil {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer rollbackCancel()
			_, rollbackErr := s.nodeController.ManageNetworkOptimization(rollbackCtx, &nodev1.NetworkOptimizationRequest{Action: nodev1.NetworkOptimizationRequest_ROLLBACK, NodeId: input.NodeID, UserId: input.UserID, OperationId: input.OperationID})
			if rollbackErr != nil {
				writeError(w, http.StatusInternalServerError, "Audit finalization failed and automatic compensation failed; inspect the node before further changes")
				return
			}
			writeError(w, http.StatusServiceUnavailable, "Audit finalization failed; the node change was rolled back")
			return
		}
	} else {
		if err := s.markNetworkOptimizationRolledBack(ctx, input, principal.ID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "Node rollback completed but audit status could not be updated; reconcile the audit history")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id": input.NodeID, "user_id": input.UserID, "operation_id": response.GetOperationId(),
		"state": response.GetState(), "applied": response.GetApplied(),
	})
}

func (s *Server) handleNetworkIntelligenceOptimizationList(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.NetworkIntelligenceEnabled && !s.cfg.NetworkOptimizationEnabled {
		writeError(w, http.StatusNotFound, "Network optimization is disabled")
		return
	}
	nodeID, err := strconv.ParseInt(r.URL.Query().Get("node_id"), 10, 64)
	if err != nil || nodeID <= 0 {
		writeError(w, http.StatusBadRequest, "valid node_id required")
		return
	}
	userID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "valid user_id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	response, err := s.nodeController.ManageNetworkOptimization(ctx, &nodev1.NetworkOptimizationRequest{
		Action: nodev1.NetworkOptimizationRequest_LIST_USER, NodeId: nodeID, UserId: userID,
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Node optimization state is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node_id": nodeID, "user_id": userID, "operations": response.GetOperations()})
}

func newNetworkOptimizationOperationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Server) recordNetworkOptimizationAudit(ctx context.Context, actionType string, input networkOptimizationRequest, summary string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	changes := []recentActionValueChange{
		{Field: "node_id", Before: "", After: fmt.Sprint(input.NodeID)},
		{Field: "user_id", Before: "", After: fmt.Sprint(input.UserID)},
	}
	if actionType == "network.optimization.apply" {
		changes = append(changes, recentActionValueChange{Field: "tcp_mss", Before: "default", After: fmt.Sprint(input.TCPMSS)})
	}
	if err := s.recordRecentActionEventDetailsTx(ctx, tx, actionType, "network_optimization", input.OperationID, summary, changes, []string{fmt.Sprintf("node:%d", input.NodeID), fmt.Sprintf("user:%d", input.UserID)}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) updateNetworkOptimizationAudit(ctx context.Context, actionType, operationID, summary string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE recent_actions SET summary = ? WHERE resource_type = 'network_optimization' AND action_type = ? AND resource_key = ?`, summary, actionType, operationID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("network optimization audit entry not found")
	}
	return nil
}

func (s *Server) markNetworkOptimizationRolledBack(ctx context.Context, input networkOptimizationRequest, adminID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE recent_actions SET summary = ?, rollback_status = 'undone', undone_at = ?, undone_by_admin_id = ? WHERE resource_type = 'network_optimization' AND action_type = 'network.optimization.apply' AND resource_key = ? AND rollback_status = 'unsupported'`, "Applied per-user WireGuard TCP MSS was rolled back", dbTimestamp(time.Now().UTC()), adminID, input.OperationID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT rollback_status FROM recent_actions WHERE resource_type = 'network_optimization' AND action_type = 'network.optimization.apply' AND resource_key = ?`, input.OperationID).Scan(&status); err != nil {
			return errors.New("original optimization audit entry is missing")
		}
		if status == "undone" {
			return tx.Commit()
		}
		return errors.New("original optimization audit entry could not be reconciled")
	}
	if err := s.recordRecentActionEventDetailsTx(ctx, tx, "network.optimization.rollback", "network_optimization", input.OperationID, fmt.Sprintf("Rolled back per-user network optimization on node %d for user %d", input.NodeID, input.UserID), []recentActionValueChange{{Field: "operation_id", Before: input.OperationID, After: "rolled_back"}}, []string{fmt.Sprintf("node:%d", input.NodeID), fmt.Sprintf("user:%d", input.UserID)}); err != nil {
		return err
	}
	return tx.Commit()
}

func safeNetworkOptimizationError(err error) string {
	if err == nil {
		return "unknown"
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
