package nodecontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type NodeRollbackRequest struct {
	SourceOperationID string `json:"source_operation_id"`
	Reason            string `json:"reason"`
	Confirm           bool   `json:"confirm"`
}

func (c Controller) NodeRollbackBackup(ctx context.Context, nodeID int64, sourceID string) (systemapp.BinaryBackup, error) {
	source, err := c.repo.NodeUpdateOperation(ctx, sourceID)
	if err != nil {
		return systemapp.BinaryBackup{}, err
	}
	if source.NodeID != nodeID || source.PreviousVersion == "" || source.Action == "rollback" {
		return systemapp.BinaryBackup{}, fmt.Errorf("operation has no recoverable backup for this node")
	}
	client, _, err := c.dial(ctx, nodeID)
	if err != nil {
		return systemapp.BinaryBackup{}, err
	}
	response, err := client.Runtime().UpdateService(ctx, &nodev1.ServiceUpdateRequest{Action: "validate_backup", BackupIdentity: sourceID, Version: source.PreviousVersion})
	if err != nil {
		return systemapp.BinaryBackup{}, err
	}
	if response == nil || !response.Accepted {
		return systemapp.BinaryBackup{}, fmt.Errorf("backup verification was not accepted")
	}
	var backup systemapp.BinaryBackup
	if err := json.Unmarshal([]byte(response.BackupJson), &backup); err != nil {
		return backup, fmt.Errorf("backup verification metadata is unavailable")
	}
	if !backup.Valid || backup.Identity != sourceID || backup.Version != source.PreviousVersion || backup.TargetType != "node" {
		return backup, fmt.Errorf("backup verification metadata does not match source operation")
	}
	return backup, nil
}

func (c Controller) RollbackService(ctx context.Context, nodeID int64, req NodeRollbackRequest) (NodeUpdateOperation, error) {
	if !req.Confirm {
		return NodeUpdateOperation{}, fmt.Errorf("explicit rollback confirmation is required")
	}
	backup, err := c.NodeRollbackBackup(ctx, nodeID, req.SourceOperationID)
	if err != nil {
		return NodeUpdateOperation{}, err
	}
	client, _, err := c.dial(ctx, nodeID)
	if err != nil {
		return NodeUpdateOperation{}, err
	}
	health, err := client.Control().Health(ctx, &nodev1.HealthRequest{})
	if err != nil {
		return NodeUpdateOperation{}, err
	}
	if health == nil {
		return NodeUpdateOperation{}, fmt.Errorf("node runtime evidence is unavailable")
	}
	if err := requireNodeUpdateEvidence(health.GetRuntime()); err != nil {
		return NodeUpdateOperation{}, err
	}
	operation := NodeUpdateOperation{ID: fmt.Sprintf("rollback-%d-%d", nodeID, time.Now().UnixNano()), NodeID: nodeID, Action: "rollback", BackupIdentity: backup.Identity, Reason: req.Reason, PreviousVersion: health.GetRuntime().GetNodeVersion(), RequestedVersion: backup.Version, DesiredVersion: backup.Version, Phase: "validating_backup", StartedAt: time.Now().UTC()}
	if err := c.repo.StartNodeUpdate(ctx, operation); err != nil {
		return NodeUpdateOperation{}, err
	}
	workerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Minute)
	go func() { defer cancel(); c.executeManualRollback(workerCtx, operation) }()
	return operation, nil
}

func (c Controller) executeManualRollback(ctx context.Context, op NodeUpdateOperation) {
	ctx, releaseExecution, leaseErr := c.beginNodeExecution(ctx, op.ID)
	if leaseErr != nil {
		return
	}
	defer releaseExecution()
	dispatched := false
	fail := func(err error) {
		_ = c.recordRollbackFailure(ctx, op, dispatched, err)
	}
	client, _, err := c.dial(ctx, op.NodeID)
	if err != nil {
		fail(err)
		return
	}
	if err := c.repo.AdvanceNodeUpdate(ctx, op.ID, "restoring", 20, "", "", "", "", "", false); err != nil {
		fail(err)
		return
	}
	started := time.Now()
	if err := c.repo.RecordNodeRestart(ctx, op.ID, started); err != nil {
		fail(err)
		return
	}
	if err := c.requireNodeExecutor(ctx, op.ID); err != nil {
		return
	}
	dispatched = true
	response, err := c.sendFencedServiceUpdate(ctx, client, op.ID, &nodev1.ServiceUpdateRequest{OperationId: op.ID, Action: "rollback", BackupIdentity: op.BackupIdentity, Version: op.DesiredVersion})
	if err == nil {
		err = requireAcceptedMaintenanceResponse(response, "rollback")
	}
	if err != nil {
		fail(err)
		return
	}
	c.discardCachedNodeClient(op.NodeID, client)
	if err := c.repo.AdvanceNodeUpdate(ctx, op.ID, "waiting_for_reconnect", 60, "", "", "", "", "", false); err != nil {
		fail(err)
		return
	}
	result, _, err := c.waitForPersistedNodeUpdate(ctx, op.ID, op.DesiredVersion)
	if err != nil {
		fail(err)
		return
	}
	_ = c.repo.AdvanceNodeUpdate(context.WithoutCancel(ctx), op.ID, "rolled_back", 100, "", op.DesiredVersion, result.NodeServiceVersion, "", "", true)
}

func (c Controller) recordRollbackFailure(ctx context.Context, op NodeUpdateOperation, dispatched bool, failure error) error {
	if errors.Is(failure, operationapp.ErrLeaseLost) {
		return failure
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if !dispatched {
		return c.repo.AdvanceNodeUpdate(finish, op.ID, "failed", 100, "", "", "", failure.Error(), failure.Error(), true)
	}
	// Restore or restart may have committed before the ACK/health check failed.
	// Preserve ownership and the original restart deadline until recovery proves
	// actual files and fresh runtime; failure is not permission for a rival.
	phase := "outcome_unknown"
	if op.Action != "rollback" {
		// Automatic rollback retains the update's action and immutable target.
		// Its recovery phase must identify restore rather than update completion.
		phase = "rollback_verifying"
	}
	if err := c.repo.AdvanceNodeUpdate(finish, op.ID, phase, 60, "", "", "", "Dispatched rollback requires external reconciliation", "", false); err != nil {
		return err
	}
	stored, err := operationapp.Get(finish, c.repo.db, op.ID)
	if err != nil {
		return err
	}
	update, err := decodeNodeUpdate(stored)
	if err != nil {
		return err
	}
	if c.recoveryLifetime != nil {
		if lifetime := c.recoveryLifetime.Load(); lifetime != nil && lifetime.Err() == nil {
			go func() { _ = c.recoverNodeMaintenance(lifetime.Context, stored, update) }()
		}
	}
	return nil
}
