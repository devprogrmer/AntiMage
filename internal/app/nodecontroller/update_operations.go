package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
	"github.com/antimage/antimage/internal/platform/requestctx"
)

var ErrNodeOperationConflict = errors.New("node has a conflicting maintenance operation")

type NodeUpdateOperation struct {
	PhaseStartedAt        time.Time                  `json:"phase_started_at,omitempty"`
	RestartRequestedAt    time.Time                  `json:"restart_requested_at,omitempty"`
	ReconnectDeadline     time.Time                  `json:"reconnect_deadline,omitempty"`
	HealthDeadline        time.Time                  `json:"health_deadline,omitempty"`
	LastRecoveryAttemptAt time.Time                  `json:"last_recovery_attempt_at,omitempty"`
	RecoveryAttemptCount  int                        `json:"recovery_attempt_count,omitempty"`
	RecoveryError         string                     `json:"recovery_error,omitempty"`
	RolloutID             string                     `json:"rollout_id,omitempty"`
	Action                string                     `json:"action,omitempty"`
	BackupIdentity        string                     `json:"backup_identity,omitempty"`
	Reason                string                     `json:"reason,omitempty"`
	ResolvedTarget        *systemapp.ResolvedInstall `json:"resolved_target,omitempty"`
	ID                    string                     `json:"operation_id"`
	NodeID                int64                      `json:"node_id"`
	RequestedChannel      string                     `json:"requested_channel"`
	UpdatePolicy          string                     `json:"update_policy"`
	RequestedVersion      string                     `json:"requested_version"`
	ResolvedVersion       string                     `json:"resolved_version,omitempty"`
	PreviousVersion       string                     `json:"previous_version,omitempty"`
	DesiredVersion        string                     `json:"desired_version,omitempty"`
	InstalledVersion      string                     `json:"installed_version,omitempty"`
	RunningVersion        string                     `json:"running_version,omitempty"`
	Phase                 string                     `json:"phase"`
	Progress              int                        `json:"progress"`
	StartedAt             time.Time                  `json:"started_at"`
	UpdatedAt             time.Time                  `json:"updated_at"`
	CompletedAt           *time.Time                 `json:"completed_at,omitempty"`
	Error                 string                     `json:"error,omitempty"`
	RollbackError         string                     `json:"rollback_error,omitempty"`
}

func (r Repository) StartNodeUpdate(ctx context.Context, update NodeUpdateOperation) error {
	if strings.TrimSpace(update.ID) == "" || update.NodeID <= 0 {
		return fmt.Errorf("operation_id and node_id are required")
	}
	if existing, err := operationapp.Get(ctx, r.db, update.ID); err == nil {
		if existing.TargetType != "node" || existing.TargetID != fmt.Sprint(update.NodeID) || (existing.Type != "node_update" && existing.Type != "node_rollback") {
			return fmt.Errorf("operation ID belongs to a different request")
		}
		return operationapp.CreateExclusive(ctx, r.db, r.dialect, existing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var nodeID int64
	if err := r.db.QueryRowContext(ctx, "SELECT id FROM nodes WHERE id=?", update.NodeID).Scan(&nodeID); err != nil {
		return err
	}
	var active int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM node_operations WHERE node_id=? AND status IN ('pending','running','retrying') AND operation_type IN ('restart_service','update_runtime','update_geo','reboot_host','restart_node','reboot_node')", update.NodeID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("%w: node %d has active maintenance", ErrNodeOperationConflict, update.NodeID)
	}
	now := time.Now().UTC()
	if update.StartedAt.IsZero() {
		update.StartedAt = now
	}
	update.UpdatedAt = now
	if update.Phase == "" {
		update.Phase = "queued"
	}
	if update.PhaseStartedAt.IsZero() {
		update.PhaseStartedAt = update.StartedAt
	}
	if update.UpdatePolicy == "" {
		update.UpdatePolicy = "pinned"
	}
	op := operationapp.Operation{ID: update.ID, Type: "node_update", TargetType: "node", TargetID: fmt.Sprint(update.NodeID), RequestedBy: requestctx.Admin(ctx), RequestID: requestctx.ID(ctx), State: operationapp.StateForPhase(update.Phase), Phase: update.Phase, CreatedAt: update.StartedAt.Unix(), UpdatedAt: now.Unix(), StartedAt: operationapp.Epoch(update.StartedAt), Metadata: map[string]any{"update": update}}
	if update.Action == "rollback" {
		op.Type = "node_rollback"
	}
	if err := operationapp.CreateExclusive(ctx, r.db, r.dialect, op); err != nil {
		var conflict operationapp.Conflict
		if errors.As(err, &conflict) {
			return fmt.Errorf("%w: %w", ErrNodeOperationConflict, conflict)
		}
		return err
	}
	return nil
}
func (r Repository) HasActiveNodeUpdate(ctx context.Context, nodeID int64) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM operations WHERE target_type='node' AND target_id=? AND operation_type IN ('node_update','node_rollback') AND state IN ('queued','running','waiting')", fmt.Sprint(nodeID)).Scan(&count)
	return count > 0, err
}
func decodeNodeUpdate(op operationapp.Operation) (NodeUpdateOperation, error) {
	var update NodeUpdateOperation
	payload, err := json.Marshal(op.Metadata["update"])
	if err != nil {
		return update, err
	}
	if err = json.Unmarshal(payload, &update); err != nil {
		return update, err
	}
	if update.ID == "" {
		return update, fmt.Errorf("operation %s has no update metadata", op.ID)
	}
	return update, nil
}
func (r Repository) AdvanceNodeUpdate(ctx context.Context, id, phase string, progress int, resolved, installed, running, detail, rollbackError string, completed bool) error {
	ctx = operationapp.WithNodeMutation(ctx, id)
	if err := operationapp.AuthorizeNodeMutation(ctx, r.db, id); err != nil {
		return err
	}
	op, err := operationapp.Get(ctx, r.db, id)
	if err != nil {
		return err
	}
	update, err := decodeNodeUpdate(op)
	if err != nil {
		return err
	}
	if operationapp.Terminal(op.State) {
		return fmt.Errorf("operation %s is already terminal", id)
	}
	if strings.TrimSpace(phase) == "" {
		return fmt.Errorf("update phase is required")
	}
	if update.Phase != phase {
		update.PhaseStartedAt = time.Now().UTC()
	}
	update.Phase = phase
	update.Progress = max(0, min(100, progress))
	update.UpdatedAt = time.Now().UTC()
	if resolved != "" {
		if update.ResolvedTarget != nil && resolved != update.ResolvedTarget.Version {
			return fmt.Errorf("resolved operation target is immutable")
		}
		update.ResolvedVersion = resolved
		update.DesiredVersion = resolved
	}
	if installed != "" {
		update.InstalledVersion = installed
	}
	if running != "" {
		update.RunningVersion = running
	}
	update.Error = detail
	update.RollbackError = rollbackError
	if completed {
		done := update.UpdatedAt
		update.CompletedAt = &done
		op.CompletedAt = operationapp.Epoch(done)
	}
	op.State = operationapp.StateForPhase(phase)
	op.Phase = phase
	op.Progress = &update.Progress
	op.UpdatedAt = update.UpdatedAt.Unix()
	op.Error = detail
	if op.Metadata == nil {
		op.Metadata = make(map[string]any)
	}
	op.Metadata["update"] = update
	return operationapp.Save(ctx, r.db, r.dialect, op)
}

// Persist the restart boundary before issuing the RPC. A crash after acceptance
// must never give the operation a new reconnect window or accept an old process.
func (r Repository) RecordNodeRestart(ctx context.Context, id string, boundary time.Time) error {
	ctx = operationapp.WithNodeMutation(ctx, id)
	if err := operationapp.AuthorizeNodeMutation(ctx, r.db, id); err != nil {
		return err
	}
	op, err := operationapp.Get(ctx, r.db, id)
	if err != nil {
		return err
	}
	if operationapp.Terminal(op.State) {
		return fmt.Errorf("operation %s is already terminal", id)
	}
	update, err := decodeNodeUpdate(op)
	if err != nil {
		return err
	}
	update.RestartRequestedAt = boundary.UTC()
	update.ReconnectDeadline = boundary.UTC().Add(4 * time.Minute)
	update.HealthDeadline = update.ReconnectDeadline.Add(30 * time.Second)
	update.UpdatedAt = time.Now().UTC()
	op.Metadata["update"] = update
	op.UpdatedAt = update.UpdatedAt.Unix()
	return operationapp.Save(ctx, r.db, r.dialect, op)
}
func (r Repository) NodeUpdateOperation(ctx context.Context, id string) (NodeUpdateOperation, error) {
	op, err := operationapp.Get(ctx, r.db, id)
	if err != nil {
		return NodeUpdateOperation{}, err
	}
	return decodeNodeUpdate(op)
}
func (r Repository) LatestNodeUpdate(ctx context.Context, nodeID int64) (NodeUpdateOperation, error) {
	history, err := r.NodeUpdateHistory(ctx, nodeID, 1)
	if err != nil {
		return NodeUpdateOperation{}, err
	}
	if len(history) == 0 {
		return NodeUpdateOperation{}, sql.ErrNoRows
	}
	return history[0], nil
}
func (r Repository) NodeUpdateHistory(ctx context.Context, nodeID int64, limit int) ([]NodeUpdateOperation, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	items, err := operationapp.ListTarget(ctx, r.db, "node", fmt.Sprint(nodeID), 500)
	if err != nil {
		return nil, err
	}
	history := make([]NodeUpdateOperation, 0)
	for _, op := range items {
		if op.Type != "node_update" && op.Type != "node_rollback" {
			continue
		}
		update, err := decodeNodeUpdate(op)
		if err != nil {
			return nil, err
		}
		history = append(history, update)
		if len(history) >= limit {
			break
		}
	}
	return history, nil
}
