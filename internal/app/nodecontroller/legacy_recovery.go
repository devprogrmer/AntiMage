package nodecontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type controllerRecoveryContext struct{ context.Context }

func (c Controller) launchLegacyRecovery(op operationapp.Operation) {
	if c.recoveryLifetime == nil {
		return
	}
	lifetime := c.recoveryLifetime.Load()
	if lifetime == nil || lifetime.Err() != nil {
		return
	}
	go func() { _ = c.recoverLegacyMaintenance(lifetime.Context, op) }()
}

func legacyMaintenanceType(kind string) bool {
	switch kind {
	case "core_update", "core_restart", "sync_config", "node_restart", "host_reboot", "geo_update", "stop_runtime":
		return true
	}
	return false
}

// Recovery only probes actual runtime state. It never repeats a destructive RPC.
func (c Controller) recoverLegacyMaintenance(ctx context.Context, op operationapp.Operation) error {
	if op.Phase == "queued" || op.Phase == "manual_recovery_required" {
		return nil
	}
	worker, release, err := c.beginNodeExecution(ctx, op.ID)
	if errors.Is(err, operationapp.ErrLeaseHeld) {
		if _, loaded := c.recoveryWorkers.LoadOrStore(op.ID, true); loaded {
			return nil
		}
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
			if err == nil && !operationapp.Terminal(latest.State) {
				_ = c.recoverLegacyMaintenance(ctx, latest)
			}
		}()
		return nil
	}
	if err != nil {
		return err
	}
	if _, loaded := c.recoveryWorkers.LoadOrStore(op.ID, true); loaded {
		release()
		return nil
	}
	go func() {
		defer release()
		defer c.recoveryWorkers.Delete(op.ID)
		_ = c.reconcileLegacyMaintenance(worker, op.ID, func(probeCtx context.Context, nodeID int64) (*nodev1.RuntimeState, error) {
			client, _, err := c.dial(probeCtx, nodeID)
			if err != nil {
				return nil, err
			}
			commandID, _ := op.Metadata["command_id"].(string)
			request := &nodev1.HealthRequest{}
			if op.Type == "stop_runtime" || op.Type == "geo_update" {
				request.OperationId, request.CommandId = op.ID, commandID
			}
			response, err := client.Control().Health(probeCtx, request)
			if err != nil {
				return nil, err
			}
			return response.GetRuntime(), nil
		})
	}()
	return nil
}

func (c Controller) reconcileLegacyMaintenance(ctx context.Context, id string, probe func(context.Context, int64) (*nodev1.RuntimeState, error)) error {
	ctx = operationapp.WithAuditOrigin(ctx, "recovery")
	op, err := operationapp.Get(ctx, c.repo.db, id)
	if err != nil {
		return err
	}
	deadline, err := metadataDeadline(op.Metadata["phase_deadline_at_nanos"])
	if err != nil {
		return err
	}
	var attempts int
	_, _ = fmt.Sscan(fmt.Sprint(op.Metadata["recovery_attempts"]), &attempts)
	var nodeID int64
	if _, err := fmt.Sscan(op.TargetID, &nodeID); err != nil {
		return err
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for attempts < 3 && bounded.Err() == nil {
		attempts++
		op.Phase = "reconciling"
		op.Metadata["recovery_attempts"] = attempts
		op.Metadata["recovery_started_at_nanos"] = time.Now().UTC().UnixNano()
		// Persist before probing so a controller crash cannot replenish attempts.
		if err := operationapp.Save(bounded, c.repo.db, c.repo.dialect, op); err != nil {
			return err
		}
		probeCtx, stop := context.WithTimeout(bounded, 5*time.Second)
		state, probeErr := probe(probeCtx, nodeID)
		stop()
		if probeErr == nil && op.Type == "geo_update" && geoCommittedNeedsReload(op, state) {
			// Keep local and persisted intent aligned before the next probe save.
			op.Metadata["geo_reload_dispatched"] = true
			_ = c.resumeLegacyGeoReload(bounded, op, nodeID)
		}
		if probeErr == nil && legacyRuntimeEvidenceMatches(op, state) {
			if err := c.requireNodeExecutor(ctx, id); err != nil {
				return err
			}
			op.State, op.Phase, op.Error = "completed", "outcome_reconciled", ""
			op.UpdatedAt = time.Now().UTC().Unix()
			op.CompletedAt = operationapp.Epoch(time.Now().UTC())
			op.Metadata["recovery_result"] = "verified_runtime_state"
			return operationapp.Save(ctx, c.repo.db, c.repo.dialect, op)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-bounded.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	// Unknown state is not proof that the remote job stopped. Retain the target
	// reservation for manual inspection instead of opening a competing operation.
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	op.State, op.Phase, op.Error = "waiting", "manual_recovery_required", "destructive outcome could not be verified within its original deadline; no command replayed"
	op.UpdatedAt = time.Now().UTC().Unix()
	return operationapp.Save(finish, c.repo.db, c.repo.dialect, op)
}

func legacyRuntimeEvidenceMatches(op operationapp.Operation, state *nodev1.RuntimeState) bool {
	if state == nil || !state.Connected {
		return false
	}
	boundary, err := metadataDeadline(op.Metadata["restart_boundary_nanos"])
	if err != nil || state.SampledAtUnixNano <= boundary.UnixNano() {
		return false
	}
	if op.Type == "stop_runtime" {
		commandID, _ := op.Metadata["command_id"].(string)
		var generation int64
		_, _ = fmt.Sscan(fmt.Sprint(op.Metadata["dispatched_resource_generation"]), &generation)
		return !state.Started && state.RuntimeStopVerified && commandID != "" && generation > 0 && state.EvidenceOperationId == op.ID && state.EvidenceCommandId == commandID && state.EvidenceResourceGeneration == generation && state.EvidenceCommandState == "completed" && state.EvidenceProcessStartedAtUnixNano == state.ProcessStartedAtUnixNano
	}
	if !state.Started {
		return false
	}
	switch op.Type {
	case "geo_update":
		return geoEvidenceIdentityMatches(op, state) && state.GeoTransactionPhase == "reload_completed" && state.GeoDatasetSha256 != "" && state.GeoReloadVerified
	case "node_restart", "host_reboot":
		return state.ProcessStartedAtUnixNano > boundary.UnixNano() && state.ProcessStartedAtUnixNano <= state.SampledAtUnixNano
	case "sync_config":
		expected, _ := op.Metadata["expected_config_sha256"].(string)
		return expected != "" && state.ActiveConfigSha256 == expected
	case "core_restart":
		expected, _ := op.Metadata["expected_config_sha256"].(string)
		return expected != "" && state.ActiveConfigSha256 == expected && state.CoreProcessStartedAtUnixNano > boundary.UnixNano() && state.CoreProcessStartedAtUnixNano <= state.SampledAtUnixNano
	case "core_update":
		expected, _ := op.Metadata["expected_core_version"].(string)
		return expected != "" && expected != "latest" && strings.TrimPrefix(state.RunningCoreVersion, "v") == expected && state.CoreProcessStartedAtUnixNano > boundary.UnixNano() && state.CoreProcessStartedAtUnixNano <= state.SampledAtUnixNano
	}
	return false
}

func geoEvidenceIdentityMatches(op operationapp.Operation, state *nodev1.RuntimeState) bool {
	if state == nil {
		return false
	}
	commandID, _ := op.Metadata["command_id"].(string)
	var generation int64
	_, _ = fmt.Sscan(fmt.Sprint(op.Metadata["dispatched_resource_generation"]), &generation)
	return commandID != "" && generation > 0 && state.EvidenceOperationId == op.ID && state.EvidenceCommandId == commandID && state.EvidenceResourceGeneration == generation && state.EvidenceCommandState != "superseded"
}

func geoCommittedNeedsReload(op operationapp.Operation, state *nodev1.RuntimeState) bool {
	return geoEvidenceIdentityMatches(op, state) && state.GeoTransactionPhase == "files_committed" && state.GeoDatasetSha256 != "" && !state.GeoReloadVerified && op.Metadata["geo_reload_dispatched"] != true
}

func (c Controller) resumeLegacyGeoReload(ctx context.Context, op operationapp.Operation, nodeID int64) error {
	lease, ok := operationapp.ExecutorLeaseFromContext(ctx)
	if !ok {
		return operationapp.ErrLeaseLost
	}
	commandID, _ := op.Metadata["command_id"].(string)
	// Persist intent before dispatch. Neither restart nor transport failure may
	// issue this missing step twice; Health remains the reconciliation source.
	op.Metadata["geo_reload_dispatched"] = true
	if err := operationapp.Save(ctx, c.repo.db, c.repo.dialect, op); err != nil {
		return err
	}
	client, _, err := c.dial(ctx, nodeID)
	if err != nil {
		return err
	}
	fence := &nodev1.DestructiveFence{OperationId: op.ID, CommandId: commandID + "-reload", ResourceId: op.TargetID, ResourceGeneration: lease.ResourceGeneration, LeaseGeneration: lease.Generation}
	response, rpcErr := client.Runtime().UpdateGeo(ctx, &nodev1.GeoUpdateRequest{OperationId: op.ID, ResumeReloadOnly: true, Fence: fence})
	req := &nodev1.ServiceUpdateRequest{OperationId: op.ID, FenceOperationId: op.ID, CommandId: fence.CommandId, ResourceId: fence.ResourceId, ResourceGeneration: fence.ResourceGeneration, LeaseGeneration: fence.LeaseGeneration}
	return c.validateFencedServiceResult(ctx, op.ID, req, response, rpcErr)
}
