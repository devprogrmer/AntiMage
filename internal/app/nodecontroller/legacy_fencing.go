package nodecontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	"github.com/antimage/antimage/internal/platform/requestctx"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type legacyEvidenceKey struct{}

func legacyConfigEvidence(ctx context.Context, config string) context.Context {
	digest := sha256.Sum256([]byte(config))
	return context.WithValue(ctx, legacyEvidenceKey{}, map[string]any{"expected_config_sha256": hex.EncodeToString(digest[:])})
}

func legacyVersionEvidence(ctx context.Context, version string) context.Context {
	return context.WithValue(ctx, legacyEvidenceKey{}, map[string]any{"expected_core_version": version})
}

// Every runtime-mutating legacy RPC reserves the same target as update/rollback.
// consume runs while that reservation and executor lease are still owned.
func (c Controller) executeLegacyNodeCommand(ctx context.Context, nodeID int64, id, action string, call func(context.Context, *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error), consume func(context.Context, *nodev1.RuntimeActionResponse) error) error {
	if id == "" || nodeID <= 0 {
		return fmt.Errorf("destructive command identity and target are required")
	}
	if err := c.requireDestructiveCapability(ctx, nodeID); err != nil {
		return err
	}
	now := time.Now().UTC()
	op := operationapp.Operation{ID: id, Type: action, TargetType: "node", TargetID: fmt.Sprint(nodeID), RequestedBy: requestctx.Admin(ctx), RequestID: requestctx.ID(ctx), State: "queued", Phase: "queued", CreatedAt: now.Unix(), UpdatedAt: now.Unix(), Metadata: map[string]any{"phase_deadline_at_nanos": now.Add(3 * time.Minute).UnixNano()}}
	if origin := operationapp.AuditOrigin(ctx); origin != "" {
		op.Metadata["origin"] = origin
	}
	if evidence, ok := ctx.Value(legacyEvidenceKey{}).(map[string]any); ok {
		for key, value := range evidence {
			op.Metadata[key] = value
		}
	}
	if err := operationapp.CreateExclusive(ctx, c.repo.db, c.repo.dialect, op); err != nil {
		return err
	}
	worker, release, err := c.beginNodeExecution(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	op, err = operationapp.Get(worker, c.repo.db, id)
	if err != nil {
		return err
	}
	if evidence, ok := ctx.Value(legacyEvidenceKey{}).(map[string]any); ok {
		for key, value := range evidence {
			if fmt.Sprint(op.Metadata[key]) != fmt.Sprint(value) {
				return fmt.Errorf("persisted destructive target evidence is immutable")
			}
		}
	}
	if op.Phase != "queued" {
		return ErrCommandOutcomeUnknown
	}
	deadline, err := metadataDeadline(op.Metadata["phase_deadline_at_nanos"])
	if err != nil {
		return err
	}
	worker, cancel := context.WithDeadline(worker, deadline)
	defer cancel()
	if err := worker.Err(); err != nil {
		return err
	}
	lease, _ := operationapp.ExecutorLeaseFromContext(worker)
	digest := sha256.Sum256([]byte(id + "|" + action + "|" + op.TargetID))
	fence := &nodev1.DestructiveFence{OperationId: id, CommandId: "command-" + hex.EncodeToString(digest[:16]), ResourceId: op.TargetID, ResourceGeneration: lease.ResourceGeneration, LeaseGeneration: lease.Generation}
	op.State, op.Phase, op.UpdatedAt = "running", "dispatched", time.Now().UTC().Unix()
	op.Metadata["command_id"] = fence.CommandId
	op.Metadata["dispatched_resource_generation"] = fence.ResourceGeneration
	op.Metadata["restart_boundary_nanos"] = time.Now().UTC().UnixNano()
	if err := operationapp.Save(worker, c.repo.db, c.repo.dialect, op); err != nil {
		return err
	}
	response, rpcErr := call(worker, fence)
	req := &nodev1.ServiceUpdateRequest{OperationId: id, FenceOperationId: id, CommandId: fence.CommandId, ResourceId: fence.ResourceId, ResourceGeneration: fence.ResourceGeneration, LeaseGeneration: fence.LeaseGeneration}
	err = c.validateFencedServiceResult(worker, id, req, response, rpcErr)
	if err == nil {
		err = requireAcceptedMaintenanceResponse(response, action)
	}
	if err == nil && action == "stop_runtime" && !response.GetRuntime().GetRuntimeStopVerified() {
		err = fmt.Errorf("runtime stop lacks verified process evidence")
	}
	if err == nil && consume != nil {
		err = consume(worker, response)
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(worker), 5*time.Second)
	defer stop()
	if errors.Is(err, operationapp.ErrLeaseLost) {
		return err
	}
	if err != nil {
		op.State, op.Phase, op.Error = "waiting", "outcome_unknown", "dispatched command requires external reconciliation"
		op.UpdatedAt = time.Now().UTC().Unix()
		persistErr := operationapp.Save(finish, c.repo.db, c.repo.dialect, op)
		if persistErr == nil {
			c.launchLegacyRecovery(op)
		}
		return errors.Join(ErrCommandOutcomeUnknown, err, persistErr)
	}
	if action == "node_restart" || action == "host_reboot" {
		// Scheduling is only acceptance. Keep ownership until a fresh process
		// confirms execution; no successful lifecycle is inferred from enqueue.
		op.State, op.Phase, op.UpdatedAt = "waiting", "waiting_for_reconnect", time.Now().UTC().Unix()
		persistErr := operationapp.Save(finish, c.repo.db, c.repo.dialect, op)
		if persistErr == nil {
			c.launchLegacyRecovery(op)
		}
		return persistErr
	}
	op.State, op.Phase, op.UpdatedAt = "completed", "completed", time.Now().UTC().Unix()
	op.CompletedAt = operationapp.Epoch(time.Now().UTC())
	return operationapp.Save(finish, c.repo.db, c.repo.dialect, op)
}

func metadataDeadline(value any) (time.Time, error) {
	var nanos int64
	if _, err := fmt.Sscan(fmt.Sprint(value), &nanos); err != nil || nanos <= 0 {
		return time.Time{}, fmt.Errorf("persisted destructive phase deadline unavailable")
	}
	return time.Unix(0, nanos), nil
}
