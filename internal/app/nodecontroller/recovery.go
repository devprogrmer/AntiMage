package nodecontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
)

// RecoveryPolicy is deliberately explicit. External-state phases may only
// proceed with fresh runtime evidence or a remotely verified backup.
func RecoveryPolicy(phase string) string {
	switch phase {
	case "queued", "preflight", "resolving_version":
		return "safe_to_retry"
	case "outcome_unknown", "downloading", "verifying", "backing_up", "installing", "restarting", "waiting_for_reconnect", "verifying_version", "health_check", "rolling_back", "rollback_restarting", "rollback_verifying", "restoring", "validating_backup":
		return "must_reconcile_external_state"
	default:
		return "terminal_failure"
	}
}

func (c Controller) recoveryFailure(ctx context.Context, op NodeUpdateOperation, message string) error {
	stored, err := operationapp.Get(ctx, c.repo.db, op.ID)
	if err != nil {
		return err
	}
	if operationapp.Terminal(stored.State) {
		return nil
	}
	update, err := decodeNodeUpdate(stored)
	if err != nil {
		return err
	}
	update.RecoveryError = message
	stored.Metadata["update"] = update
	if err := operationapp.Save(ctx, c.repo.db, c.repo.dialect, stored); err != nil {
		return err
	}
	// An unverifiable remote side effect can still be in flight. Preserve the
	// reservation and critical evidence; failure is not permission for a rival.
	return c.repo.AdvanceNodeUpdate(ctx, op.ID, "manual_recovery_required", update.Progress, "", "", "", "startup recovery requires inspection: "+message, message, false)
}

func (c Controller) recoverNodeMaintenance(ctx context.Context, stored operationapp.Operation, op NodeUpdateOperation) error {
	if op.Phase == "manual_recovery_required" {
		return nil
	}
	leaseCtx, releaseExecution, leaseErr := c.beginNodeExecution(ctx, op.ID)
	if errors.Is(leaseErr, operationapp.ErrLeaseHeld) {
		if _, loaded := c.recoveryWorkers.LoadOrStore(op.ID, true); loaded {
			return nil
		}
		// A live executor remains authoritative. If it crashed, retry after its
		// bounded lease window without releasing the valid resource lock.
		go func() {
			timer := time.NewTimer(nodeExecutorLeaseDuration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				c.recoveryWorkers.Delete(op.ID)
				return
			case <-timer.C:
			}
			c.recoveryWorkers.Delete(op.ID)
			latest, err := operationapp.Get(ctx, c.repo.db, op.ID)
			if err != nil || operationapp.Terminal(latest.State) {
				return
			}
			update, err := decodeNodeUpdate(latest)
			if err == nil {
				_ = c.recoverNodeMaintenance(ctx, latest, update)
			}
		}()
		return nil
	}
	if leaseErr != nil {
		return leaseErr
	}
	ctx = leaseCtx
	if op.RecoveryAttemptCount >= 3 {
		defer releaseExecution()
		return c.recoveryFailure(ctx, op, "automatic recovery attempt limit reached; manual recovery required")
	}
	if RecoveryPolicy(op.Phase) == "terminal_failure" {
		defer releaseExecution()
		return c.recoveryFailure(ctx, op, "unsupported interrupted phase "+op.Phase)
	}
	if op.Action == "rollback" {
		if op.BackupIdentity == "" || op.DesiredVersion == "" {
			defer releaseExecution()
			return c.recoveryFailure(ctx, op, "persisted rollback backup identity or version is unavailable; no destructive retry issued")
		}
	} else if op.ResolvedTarget == nil || op.ResolvedTarget.Version == "" || op.ResolvedTarget.SHA256 == "" {
		defer releaseExecution()
		return c.recoveryFailure(ctx, op, "persisted immutable artifact identity is unavailable; no destructive retry issued")
	}
	if _, loaded := c.recoveryWorkers.LoadOrStore(op.ID, true); loaded {
		releaseExecution()
		return nil
	}
	if err := operationapp.CreateExclusive(ctx, c.repo.db, c.repo.dialect, stored); err != nil {
		releaseExecution()
		c.recoveryWorkers.Delete(op.ID)
		return err
	}
	op.LastRecoveryAttemptAt = time.Now().UTC()
	op.RecoveryAttemptCount++
	stored.Metadata["update"] = op
	stored.UpdatedAt = op.LastRecoveryAttemptAt.Unix()
	if err := operationapp.Save(ctx, c.repo.db, c.repo.dialect, stored); err != nil {
		releaseExecution()
		c.recoveryWorkers.Delete(op.ID)
		return err
	}
	if err := operationapp.AppendAudit(operationapp.WithAuditOrigin(ctx, "recovery"), c.repo.db, op.ID, "recovery_started", stored.Metadata); err != nil {
		releaseExecution()
		c.recoveryWorkers.Delete(op.ID)
		return err
	}
	go func() {
		defer releaseExecution()
		defer c.recoveryWorkers.Delete(op.ID)
		workerCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		c.reconcileNodeMaintenance(workerCtx, op)
		if op.RolloutID != "" {
			c.launchRollout(op.RolloutID)
		}
	}()
	return nil
}

func (c Controller) reconcileNodeMaintenance(ctx context.Context, op NodeUpdateOperation) {
	finishCtx := context.WithoutCancel(ctx)
	rollback := op.Action == "rollback" || op.Phase == "rolling_back" || op.Phase == "rollback_restarting" || op.Phase == "rollback_verifying"
	// Only untouched preflight requests are eligible for a new dispatch. A
	// persisted restart request means the RPC might already have been accepted.
	if !rollback && RecoveryPolicy(op.Phase) == "safe_to_retry" && op.RestartRequestedAt.IsZero() {
		_, err := c.updateServiceNow(ctx, Request{NodeID: op.NodeID, OperationID: op.ID, Channel: op.RequestedChannel, Policy: op.UpdatePolicy, Version: op.RequestedVersion})
		if err != nil {
			_ = c.recoveryFailure(finishCtx, op, "preflight recovery failed")
		}
		return
	}
	requested := op.DesiredVersion
	if rollback && op.Action != "rollback" {
		requested = op.PreviousVersion
	}
	if op.RestartRequestedAt.IsZero() || op.ReconnectDeadline.IsZero() {
		// Legacy records cannot prove a fresh restart. Do not synthesize a new
		// boundary and call a pre-existing process successful.
		_ = c.recoveryFailure(finishCtx, op, "persisted restart boundary/deadline is unavailable; external state requires operator inspection")
		return
	}
	if !c.reconcileNodeTransaction(ctx, op, rollback) {
		return
	}
	result, _, err := c.waitForPersistedNodeUpdate(ctx, op.ID, requested)
	if err == nil && rollback {
		_ = c.repo.AdvanceNodeUpdate(finishCtx, op.ID, "rolled_back", 100, "", requested, result.NodeServiceVersion, op.Error, "", true)
		return
	}
	if err == nil {
		healthDeadline := op.HealthDeadline
		if healthDeadline.IsZero() {
			healthDeadline = op.ReconnectDeadline.Add(30 * time.Second)
		}
		commitCtx, cancel := context.WithDeadline(ctx, healthDeadline)
		defer cancel()
		client, _, dialErr := c.dial(commitCtx, op.NodeID)
		if dialErr == nil {
			payload, marshalErr := json.Marshal(op.ResolvedTarget)
			if marshalErr != nil {
				dialErr = marshalErr
			} else {
				if err := c.requireNodeExecutor(commitCtx, op.ID); err != nil {
					return
				}
				response, commitErr := c.finalizeNodeUpdate(commitCtx, client, op, string(payload))
				dialErr = commitErr
				if dialErr == nil {
					dialErr = requireAcceptedMaintenanceResponse(response, "recovered update commit")
				}
			}
		}
		if dialErr == nil {
			_ = c.repo.AdvanceNodeUpdate(finishCtx, op.ID, "completed", 100, requested, result.InstalledNodeVersion, result.NodeServiceVersion, "", "", true)
			return
		}
		err = dialErr
	}
	if errors.Is(err, ErrCommandOutcomeUnknown) {
		_ = c.recoveryFailure(finishCtx, op, "update finalization has no completed receipt; no repeated commit or competing restore dispatched")
		return
	}
	if rollback {
		// An interrupted rollback is never dispatched twice without node-side job
		// inspection. Preserve critical evidence rather than report completion.
		_ = c.recoveryFailure(finishCtx, op, "interrupted rollback could not be verified within its persisted deadline")
		return
	}
	// Validate the operation-owned backup before attempting an automatic restore.
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, backupErr := c.NodeRollbackBackup(verifyCtx, op.NodeID, op.ID)
	cancel()
	if backupErr != nil {
		_ = c.recoveryFailure(finishCtx, op, "interrupted update cannot be verified and its backup is unavailable or invalid")
		return
	}
	_, node, dialErr := c.dial(ctx, op.NodeID)
	if dialErr != nil {
		_ = c.recoveryFailure(finishCtx, op, "verified backup cannot be restored because the node is unreachable")
		return
	}
	_, _ = c.rollbackNodeUpdate(ctx, node, Request{NodeID: op.NodeID, OperationID: op.ID}, op, op.PreviousVersion, op.DesiredVersion, fmt.Errorf("startup recovery verification: %w", err))
}
