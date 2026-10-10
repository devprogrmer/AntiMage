package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/antimage/antimage/internal/app/nodeclient"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

var (
	nodeStableVersionPattern = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+){1,3}(?:[-+._A-Za-z0-9]*)?$`)
	nodeDevVersionPattern    = regexp.MustCompile(`^dev-[0-9a-fA-F]{7,40}$`)
)

func (c Controller) UpdateRuntime(ctx context.Context, req Request) (result RuntimeResult, err error) {
	err = c.runDurableCommand(ctx, "update_runtime", req, func(queued Request) error {
		result, err = c.updateRuntimeNow(ctx, queued)
		return err
	})
	return
}

func (c Controller) UpdateGeo(ctx context.Context, req Request) (result RuntimeResult, err error) {
	err = c.runDurableCommand(ctx, "update_geo", req, func(queued Request) error {
		result, err = c.updateGeoNow(ctx, queued)
		return err
	})
	return
}

func (c Controller) RestartService(ctx context.Context, req Request) (result RuntimeResult, err error) {
	err = c.runDurableCommand(ctx, "restart_service", req, func(queued Request) error {
		result, err = c.restartServiceNow(ctx, queued)
		return err
	})
	return
}

func (c Controller) UpdateService(ctx context.Context, req Request) (result RuntimeResult, err error) {
	if req.Policy == "latest" {
		if req.Version != "" {
			return RuntimeResult{}, fmt.Errorf("latest policy cannot include a pinned version")
		}
		if req.Channel == "" {
			req.Channel = "stable"
		}
		if req.Channel != "stable" && req.Channel != "dev" {
			return RuntimeResult{}, fmt.Errorf("invalid update channel")
		}
	} else {
		channel, version, targetErr := validateNodeServiceUpdateTarget(req.Channel, req.Version)
		if targetErr != nil {
			return RuntimeResult{}, targetErr
		}
		req.Channel = channel
		req.Version = version
	}
	node, nodeErr := c.repo.Node(ctx, req.NodeID)
	if nodeErr != nil {
		return RuntimeResult{}, nodeErr
	}
	if !strings.EqualFold(strings.TrimSpace(node.Status), "connected") {
		return RuntimeResult{}, fmt.Errorf("%w: node is offline", ErrNodeOperationConflict)
	}
	err = c.runDurableCommand(ctx, "update_service", req, func(queued Request) error {
		result, err = c.updateServiceNow(ctx, queued)
		return err
	})
	return
}

func validateNodeServiceUpdateTarget(channel, version string) (string, string, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" || channel == "latest" {
		channel = "stable"
	}
	version = strings.TrimSpace(version)
	if channel != "stable" && channel != "dev" {
		return "", "", fmt.Errorf("node update channel must be stable or dev")
	}
	if version == "" || version == "latest" || version == "dev" {
		return "", "", fmt.Errorf("node update requires an exact version from the build catalog")
	}
	if channel == "dev" && !nodeDevVersionPattern.MatchString(version) {
		return "", "", fmt.Errorf("dev channel requires an exact dev-SHA version")
	}
	if channel == "stable" && !nodeStableVersionPattern.MatchString(version) {
		return "", "", fmt.Errorf("stable channel requires an exact release version")
	}
	return channel, version, nil
}

func (c Controller) NodeUpdateHistory(ctx context.Context, nodeID int64, limit int) ([]NodeUpdateOperation, error) {
	if nodeID <= 0 {
		return nil, fmt.Errorf("node_id is required")
	}
	return c.repo.NodeUpdateHistory(ctx, nodeID, limit)
}

func (c Controller) RebootHost(ctx context.Context, req Request) (result RuntimeResult, err error) {
	err = c.runDurableCommand(ctx, "reboot_host", req, func(queued Request) error {
		result, err = c.rebootHostNow(ctx, queued)
		return err
	})
	return
}

func (c Controller) ApplyTorProxy(ctx context.Context, req Request) (result RuntimeResult, err error) {
	err = c.runDurableCommand(ctx, "apply_tor_proxy", req, func(queued Request) error {
		result, err = c.applyTorProxyNow(ctx, queued)
		return err
	})
	return
}

func (c Controller) QueueTorProxy(ctx context.Context, req Request) error {
	if req.NodeID <= 0 {
		return fmt.Errorf("node_id is required")
	}
	_, err := c.repo.QueueCommand(ctx, "apply_tor_proxy", req.NodeID, req)
	return err
}

func (c Controller) ConfigureWindscribe(ctx context.Context, req Request) (result WindscribeResult, err error) {
	err = c.runDurableCommand(ctx, "configure_windscribe", req, func(queued Request) error {
		result, err = c.configureWindscribeNow(ctx, queued)
		return err
	})
	return
}

func (c Controller) ConfigurePsiphon(ctx context.Context, req Request) (result PsiphonResult, err error) {
	err = c.runDurableCommand(ctx, "configure_psiphon", req, func(queued Request) error {
		result, err = c.configurePsiphonNow(ctx, queued)
		return err
	})
	return
}

func (c Controller) runDurableCommand(ctx context.Context, operationType string, req Request, apply func(Request) error) error {
	if operationType != "update_service" {
		active, err := c.repo.HasActiveNodeUpdate(ctx, req.NodeID)
		if err != nil && !isMissingTableError(err) {
			return err
		}
		if active {
			return fmt.Errorf("%w: node update is in progress", ErrNodeOperationConflict)
		}
	}
	operation, err := c.repo.QueueCommand(ctx, operationType, req.NodeID, req)
	if err != nil {
		return err
	}
	if operationType != "update_service" {
		active, checkErr := c.repo.HasActiveNodeUpdate(ctx, req.NodeID)
		if checkErr != nil && !isMissingTableError(checkErr) {
			_ = c.repo.MarkOperationFailed(context.WithoutCancel(ctx), operation.ID, checkErr.Error())
			return checkErr
		}
		if active {
			conflict := fmt.Errorf("%w: node update is in progress", ErrNodeOperationConflict)
			_ = c.repo.MarkOperationFailed(context.WithoutCancel(ctx), operation.ID, conflict.Error())
			return conflict
		}
	}
	claimed, err := c.repo.MarkOperationRunning(ctx, operation.ID)
	if err != nil {
		return err
	}
	if !claimed {
		err := fmt.Errorf("node operation could not be claimed")
		_ = c.repo.MarkOperationFailed(context.WithoutCancel(ctx), operation.ID, err.Error())
		return err
	}
	req.OperationID = fmt.Sprintf("%s-%d", operationType, operation.ID)
	if err := apply(req); err != nil {
		if errors.Is(err, ErrCommandOutcomeUnknown) || errors.Is(err, operationapp.ErrLeaseLost) {
			statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			return errors.Join(err, c.repo.MarkOperationOutcomeUnknown(statusCtx, operation.ID))
		}
		statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if isPermanentOperationError(err) {
			_ = c.repo.MarkOperationFailed(statusCtx, operation.ID, err.Error())
		} else {
			_ = c.repo.MarkOperationRetrying(statusCtx, operation.ID, err.Error())
		}
		return err
	}
	statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return c.repo.MarkOperationDone(statusCtx, operation.ID)
}

func (c Controller) updateRuntimeNow(ctx context.Context, req Request) (RuntimeResult, error) {
	client, node, err := c.dial(ctx, req.NodeID)
	if err != nil {
		_ = c.repo.SetError(ctx, req.NodeID, err.Error())
		return RuntimeResult{}, friendlyNodeError("update runtime", req.NodeID, err)
	}
	var result RuntimeResult
	err = c.executeLegacyNodeCommand(legacyVersionEvidence(ctx, strings.TrimPrefix(strings.TrimSpace(req.Version), "v")), req.NodeID, req.OperationID, "core_update", func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		return client.Runtime().UpdateRuntime(worker, &nodev1.RuntimeUpdateRequest{OperationId: req.OperationID, Version: strings.TrimSpace(req.Version), Fence: fence})
	}, func(worker context.Context, res *nodev1.RuntimeActionResponse) error {
		var finishErr error
		result, finishErr = c.finishRuntime(worker, node, res.GetRuntime(), res.GetMessage())
		return finishErr
	})
	return result, err
}

func (c Controller) updateGeoNow(ctx context.Context, req Request) (RuntimeResult, error) {
	client, node, err := c.dial(ctx, req.NodeID)
	if err != nil {
		_ = c.repo.SetError(ctx, req.NodeID, err.Error())
		return RuntimeResult{}, friendlyNodeError("update geo", req.NodeID, err)
	}
	files := make([]*nodev1.GeoFile, 0, len(req.Files))
	for _, file := range req.Files {
		files = append(files, &nodev1.GeoFile{Name: file.Name, Url: file.URL})
	}
	var result RuntimeResult
	err = c.executeLegacyNodeCommand(ctx, req.NodeID, req.OperationID, "geo_update", func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		return client.Runtime().UpdateGeo(worker, &nodev1.GeoUpdateRequest{OperationId: req.OperationID, Files: files, Fence: fence})
	}, func(worker context.Context, res *nodev1.RuntimeActionResponse) error {
		var finishErr error
		result, finishErr = c.finishRuntime(worker, node, res.GetRuntime(), res.GetMessage())
		return finishErr
	})
	return result, err
}

func (c Controller) restartServiceNow(ctx context.Context, req Request) (RuntimeResult, error) {
	client, node, err := c.dial(ctx, req.NodeID)
	if err != nil {
		_ = c.repo.SetError(ctx, req.NodeID, err.Error())
		return RuntimeResult{}, friendlyNodeError("restart service", req.NodeID, err)
	}
	var result RuntimeResult
	err = c.executeLegacyNodeCommand(ctx, req.NodeID, req.OperationID, "node_restart", func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		return client.Runtime().RestartService(worker, &nodev1.ServiceRestartRequest{OperationId: req.OperationID, Fence: fence})
	}, func(_ context.Context, res *nodev1.RuntimeActionResponse) error {
		result = runtimeResult(node, res.GetRuntime(), nil)
		return nil
	})
	return result, err
}

func (c Controller) updateServiceNow(ctx context.Context, req Request) (RuntimeResult, error) {
	client, node, err := c.dial(ctx, req.NodeID)
	if err != nil {
		_ = c.repo.SetError(ctx, req.NodeID, err.Error())
		return RuntimeResult{}, friendlyNodeError("update service", req.NodeID, err)
	}
	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	if channel == "" || channel == "latest" {
		channel = "stable"
	}
	requestedVersion := strings.TrimSpace(req.Version)
	policy := "latest"
	if requestedVersion != "" {
		policy = "pinned"
	}
	previousVersion := strings.TrimSpace(client.NodeVersion())
	healthCtx, healthCancel := context.WithTimeout(ctx, 10*time.Second)
	health, err := client.Control().Health(healthCtx, &nodev1.HealthRequest{})
	healthCancel()
	if err != nil {
		return RuntimeResult{}, fmt.Errorf("update preflight health: %w", err)
	}
	if health == nil {
		return RuntimeResult{}, fmt.Errorf("update preflight runtime evidence is unavailable")
	}
	if err := requireNodeUpdateEvidence(health.GetRuntime()); err != nil {
		return RuntimeResult{}, err
	}
	var target systemapp.ResolvedInstall
	existing, lookupErr := c.repo.NodeUpdateOperation(ctx, req.OperationID)
	if lookupErr == nil {
		if existing.ResolvedTarget == nil {
			return RuntimeResult{}, fmt.Errorf("persisted update has no immutable artifact identity")
		}
		target = *existing.ResolvedTarget
		// A resumed request retains the original rollback version, even when
		// preflight now observes the new binary running after an interruption.
		previousVersion = existing.PreviousVersion
		if target.RequestedChannel != channel || target.RequestedVersion != requestedVersion {
			return RuntimeResult{}, fmt.Errorf("operation target cannot change during retry")
		}
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return RuntimeResult{}, lookupErr
	} else {
		checker := systemapp.NewGitHubUpdateChecker()
		checker.OS = health.GetRuntime().GetOperatingSystem()
		checker.Arch = health.GetRuntime().GetArchitecture()
		if checker.OS != "linux" || checker.Arch == "" {
			return RuntimeResult{}, fmt.Errorf("node update requires verified platform evidence")
		}
		catalog, err := checker.Versions(ctx, "devprogrmer/AntiMage", "node", true)
		if err != nil {
			return RuntimeResult{}, err
		}
		target, err = systemapp.ResolveInstall(catalog, channel, req.Policy, requestedVersion, checker.OS, checker.Arch)
		if err != nil {
			return RuntimeResult{}, err
		}
	}
	policy = target.RequestedPolicy
	operationStartedAt := time.Now()
	operation := NodeUpdateOperation{
		ID: req.OperationID, NodeID: node.ID, RequestedChannel: channel, StartedAt: operationStartedAt,
		UpdatePolicy: policy, RequestedVersion: requestedVersion,
		DesiredVersion: target.Version, PreviousVersion: previousVersion, Phase: "queued", ResolvedVersion: target.Version, ResolvedTarget: &target,
	}
	if err := c.repo.StartNodeUpdate(ctx, operation); err != nil {
		return RuntimeResult{}, fmt.Errorf("start node update: %w", err)
	}
	ctx, releaseExecution, err := c.beginNodeExecution(ctx, operation.ID)
	if err != nil {
		return RuntimeResult{}, err
	}
	defer releaseExecution()
	operation, err = c.repo.NodeUpdateOperation(ctx, operation.ID)
	if err != nil {
		return RuntimeResult{}, err
	}
	if operation.ResolvedTarget == nil {
		return RuntimeResult{}, fmt.Errorf("persisted immutable target is unavailable")
	}
	target = *operation.ResolvedTarget
	previousVersion = operation.PreviousVersion
	if err := c.repo.AdvanceNodeUpdate(ctx, operation.ID, "preflight", 5, "", "", "", "", "", false); err != nil {
		return RuntimeResult{}, err
	}
	payload, err := json.Marshal(target)
	if err != nil {
		return RuntimeResult{}, err
	}
	if err := c.repo.RecordNodeRestart(ctx, operation.ID, operationStartedAt); err != nil {
		return RuntimeResult{}, err
	}
	if err := c.requireNodeExecutor(ctx, operation.ID); err != nil {
		return RuntimeResult{}, err
	}
	res, err := c.sendFencedServiceUpdate(ctx, client, operation.ID, &nodev1.ServiceUpdateRequest{
		OperationId:       req.OperationID,
		Channel:           channel,
		Version:           target.Version,
		ResolvedBuildJson: string(payload),
	})
	if err != nil {
		if errors.Is(err, operationapp.ErrLeaseLost) {
			return RuntimeResult{}, err
		}
		if errors.Is(err, ErrCommandOutcomeUnknown) {
			persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer persistCancel()
			if persistErr := c.repo.AdvanceNodeUpdate(persistCtx, operation.ID, "outcome_unknown", 50, "", "", "", "dispatched update requires external reconciliation", "", false); persistErr != nil {
				return RuntimeResult{}, errors.Join(err, persistErr)
			}
			return RuntimeResult{}, err
		}
		_ = c.repo.AdvanceNodeUpdate(context.WithoutCancel(ctx), operation.ID, "failed", 100, "", "", "", err.Error(), "", true)
		_ = c.repo.SetError(ctx, req.NodeID, err.Error())
		return RuntimeResult{}, friendlyNodeError("update service", req.NodeID, err)
	}
	if err := requireAcceptedMaintenanceResponse(res, "update service"); err != nil {
		_ = c.repo.AdvanceNodeUpdate(context.WithoutCancel(ctx), operation.ID, "failed", 100, "", "", "", err.Error(), "", true)
		return RuntimeResult{}, err
	}
	if err := c.repo.AdvanceNodeUpdate(ctx, operation.ID, "waiting_for_reconnect", 75, "", "", "", "", "", false); err != nil {
		return RuntimeResult{}, err
	}
	c.discardCachedNodeClient(node.ID, client)
	verified, _, verifyErr := c.waitForPersistedNodeUpdate(ctx, operation.ID, target.Version)
	resolved := target.Version
	if verifyErr != nil {
		return c.rollbackNodeUpdate(ctx, node, req, operation, previousVersion, resolved, verifyErr)
	}
	if err := c.repo.AdvanceNodeUpdate(ctx, operation.ID, "health_check", 95, resolved, verified.InstalledNodeVersion, verified.NodeServiceVersion, "", "", false); err != nil {
		return RuntimeResult{}, err
	}
	if !verified.Connected || !verified.Started {
		return c.rollbackNodeUpdate(ctx, node, req, operation, previousVersion, resolved, fmt.Errorf("node runtime health check failed after update"))
	}
	verificationOperation, err := c.repo.NodeUpdateOperation(ctx, operation.ID)
	if err != nil {
		return RuntimeResult{}, err
	}
	confirmationCtx, confirmationCancel := context.WithDeadline(ctx, verificationOperation.HealthDeadline)
	defer confirmationCancel()
	confirmedClient, _, confirmErr := c.dial(confirmationCtx, node.ID)
	if confirmErr == nil {
		if err := c.requireNodeExecutor(confirmationCtx, operation.ID); err != nil {
			return RuntimeResult{}, err
		}
		response, err := c.finalizeNodeUpdate(confirmationCtx, confirmedClient, verificationOperation, string(payload))
		confirmErr = err
		if confirmErr == nil {
			confirmErr = requireAcceptedMaintenanceResponse(response, "verified update commit")
		}
	}
	if confirmErr != nil {
		if errors.Is(confirmErr, ErrCommandOutcomeUnknown) {
			_ = c.repo.AdvanceNodeUpdate(context.WithoutCancel(ctx), operation.ID, "outcome_unknown", 95, resolved, verified.InstalledNodeVersion, verified.NodeServiceVersion, "Update finalization acknowledgement unavailable; inspect receipt before recovery", "", false)
			return RuntimeResult{}, confirmErr
		}
		return c.rollbackNodeUpdate(ctx, node, req, operation, previousVersion, resolved, confirmErr)
	}
	if err := c.repo.AdvanceNodeUpdate(ctx, operation.ID, "completed", 100, resolved, verified.InstalledNodeVersion, verified.NodeServiceVersion, "", "", true); err != nil {
		return RuntimeResult{}, err
	}
	verified.UpdateOperation = &NodeUpdateOperation{ID: operation.ID, NodeID: node.ID, RequestedChannel: channel, UpdatePolicy: policy, RequestedVersion: requestedVersion, ResolvedVersion: resolved, PreviousVersion: previousVersion, DesiredVersion: firstNonEmpty(requestedVersion, resolved), InstalledVersion: verified.InstalledNodeVersion, RunningVersion: verified.NodeServiceVersion, Phase: "completed", Progress: 100}
	verified.DesiredNodeVersion = firstNonEmpty(requestedVersion, resolved)
	verified.RunningNodeVersion = verified.NodeServiceVersion
	verified.NodeUpdatePolicy = policy
	return verified, nil
}

func (c Controller) discardCachedNodeClient(nodeID int64, client *nodeclient.Client) {
	if c.nodeClients != nil {
		if cached, ok := c.nodeClients.LoadAndDelete(nodeID); ok {
			cachedClient := cached.(cachedNodeClient).client
			if cachedClient != nil {
				_ = cachedClient.Close()
			}
		}
	}
	if client != nil {
		_ = client.Close()
	}
}

func (c Controller) waitForNodeUpdate(ctx context.Context, nodeID int64, requested string, operationStartedAt time.Time) (RuntimeResult, string, error) {
	return c.waitForNodeUpdateUntil(ctx, nodeID, requested, operationStartedAt, operationStartedAt.Add(4*time.Minute))
}

func (c Controller) waitForPersistedNodeUpdate(ctx context.Context, id, requested string) (RuntimeResult, string, error) {
	op, err := c.repo.NodeUpdateOperation(ctx, id)
	if err != nil {
		return RuntimeResult{}, "", err
	}
	if op.RestartRequestedAt.IsZero() || op.ReconnectDeadline.IsZero() {
		return RuntimeResult{}, "", fmt.Errorf("persisted restart boundary or reconnect deadline is unavailable")
	}
	return c.waitForNodeUpdateUntil(ctx, op.NodeID, requested, op.RestartRequestedAt, op.ReconnectDeadline)
}

func (c Controller) waitForNodeUpdateUntil(ctx context.Context, nodeID int64, requested string, operationStartedAt, deadline time.Time) (RuntimeResult, string, error) {
	deadlineCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastVersion string
	for {
		if deadlineCtx.Err() != nil {
			return RuntimeResult{}, lastVersion, fmt.Errorf("persisted reconnect deadline expired for node %d", nodeID)
		}
		attemptCtx, attemptCancel := context.WithTimeout(deadlineCtx, 5*time.Second)
		client, node, err := c.dial(attemptCtx, nodeID)
		if err == nil {
			health, healthErr := client.Control().Health(attemptCtx, &nodev1.HealthRequest{})
			if healthErr != nil || health == nil || health.GetRuntime() == nil {
				c.discardCachedNodeClient(nodeID, client)
			} else {
				state := health.GetRuntime()
				lastVersion = strings.TrimSpace(state.GetNodeVersion())
				if nodeUpdateRuntimeMatches(state, requested, operationStartedAt) {
					result, finishErr := c.finishRuntime(attemptCtx, node, state, state.GetMessage())
					attemptCancel()
					if finishErr != nil {
						return RuntimeResult{}, lastVersion, finishErr
					}
					return result, lastVersion, nil
				}
			}
		}
		attemptCancel()
		select {
		case <-deadlineCtx.Done():
			if lastVersion != "" && requested != "" && lastVersion != requested {
				return RuntimeResult{}, lastVersion, fmt.Errorf("node reconnected with version %q; requested %q", lastVersion, requested)
			}
			return RuntimeResult{}, lastVersion, fmt.Errorf("timed out waiting for node %d to reconnect with a healthy updated runtime", nodeID)
		case <-ticker.C:
		}
	}
}

func nodeUpdateRuntimeMatches(state *nodev1.RuntimeState, requested string, operationStartedAt time.Time) bool {
	if state == nil || !state.GetConnected() || !state.GetStarted() {
		return false
	}
	running := strings.TrimSpace(state.GetNodeVersion())
	if running == "" || (requested != "" && !nodeVersionMatchesRequested(running, requested)) {
		return false
	}
	started := state.GetProcessStartedAtUnixNano()
	sampled := state.GetSampledAtUnixNano()
	return started > operationStartedAt.UnixNano() && sampled >= started && sampled > operationStartedAt.UnixNano()
}

func requireNodeUpdateEvidence(state *nodev1.RuntimeState) error {
	if state == nil || state.GetProcessStartedAtUnixNano() <= 0 || state.GetSampledAtUnixNano() < state.GetProcessStartedAtUnixNano() {
		return fmt.Errorf("node upgrade required: process and runtime evidence are unavailable")
	}
	evidence, verified := false, false
	for _, capability := range state.GetCapabilities() {
		if capability == "runtime_evidence_v1" {
			evidence = true
		}
		if capability == "verified_updates_v1" {
			verified = true
		}
	}
	profile := ClassifyDestructiveCapabilities(state.GetCapabilities())
	if evidence && verified && profile.SupportsFencing && profile.SupportsCommandIdempotency {
		return nil
	}
	return fmt.Errorf("node upgrade required: verified update agent and installer capabilities are unavailable")
}

func nodeVersionMatchesRequested(running, requested string) bool {
	running = strings.TrimSpace(running)
	requested = strings.TrimSpace(requested)
	if running == requested {
		return true
	}
	if !strings.HasPrefix(requested, "dev-") || !strings.HasPrefix(running, requested) {
		return false
	}
	requestedSHA := strings.TrimPrefix(requested, "dev-")
	runningSHA := strings.TrimPrefix(running, "dev-")
	if len(requestedSHA) < 7 || len(runningSHA) < len(requestedSHA) || len(runningSHA) > 40 {
		return false
	}
	for _, value := range runningSHA {
		if !((value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')) {
			return false
		}
	}
	return true
}

func (c Controller) rollbackNodeUpdate(ctx context.Context, node NodeRow, req Request, operation NodeUpdateOperation, previous, resolved string, updateErr error) (RuntimeResult, error) {
	if previous == "" {
		_ = c.recoveryFailure(context.WithoutCancel(ctx), operation, "previous running version is unknown; rollback requires inspection")
		return RuntimeResult{}, updateErr
	}
	_ = c.repo.AdvanceNodeUpdate(context.WithoutCancel(ctx), operation.ID, "rolling_back", 96, resolved, "", "", updateErr.Error(), "", false)
	client, _, err := c.dial(ctx, node.ID)
	if err != nil {
		_ = c.recoveryFailure(context.WithoutCancel(ctx), operation, "rollback could not reach node; update outcome requires inspection")
		return RuntimeResult{}, fmt.Errorf("update verification failed: %v; rollback failed: %w", updateErr, err)
	}
	rollbackStartedAt := time.Now()
	if err := c.repo.RecordNodeRestart(ctx, operation.ID, rollbackStartedAt); err != nil {
		return RuntimeResult{}, err
	}
	if err := c.requireNodeExecutor(ctx, operation.ID); err != nil {
		return RuntimeResult{}, err
	}
	rollback, err := c.sendFencedServiceUpdate(ctx, client, operation.ID, &nodev1.ServiceUpdateRequest{OperationId: operation.ID + "-rollback", Action: "rollback", BackupIdentity: operation.ID, Version: previous})
	if err == nil {
		err = requireAcceptedMaintenanceResponse(rollback, "rollback update")
	}
	if err != nil {
		_ = c.recordRollbackFailure(ctx, operation, true, err)
		return RuntimeResult{}, fmt.Errorf("update verification failed: %v; rollback failed: %w", updateErr, err)
	}
	c.discardCachedNodeClient(node.ID, client)
	rollbackResult, _, rollbackErr := c.waitForPersistedNodeUpdate(ctx, operation.ID, previous)
	if rollbackErr != nil {
		_ = c.recordRollbackFailure(ctx, operation, true, rollbackErr)
		return RuntimeResult{}, fmt.Errorf("update verification failed: %v; rollback verification failed: %w", updateErr, rollbackErr)
	}
	_ = c.repo.AdvanceNodeUpdate(context.WithoutCancel(ctx), operation.ID, "rolled_back", 100, resolved, previous, rollbackResult.NodeServiceVersion, updateErr.Error(), "", true)
	return rollbackResult, fmt.Errorf("update failed and was rolled back to %s: %w", previous, updateErr)
}

func requireAcceptedMaintenanceResponse(res *nodev1.RuntimeActionResponse, action string) error {
	if res == nil || !res.GetAccepted() {
		return fmt.Errorf("node %s was not accepted", action)
	}
	return nil
}

func (c Controller) rebootHostNow(ctx context.Context, req Request) (RuntimeResult, error) {
	client, node, err := c.dial(ctx, req.NodeID)
	if err != nil {
		_ = c.repo.SetError(ctx, req.NodeID, err.Error())
		return RuntimeResult{}, friendlyNodeError("reboot host", req.NodeID, err)
	}
	var result RuntimeResult
	err = c.executeLegacyNodeCommand(ctx, req.NodeID, req.OperationID, "host_reboot", func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		return client.Runtime().RebootHost(worker, &nodev1.HostRebootRequest{OperationId: req.OperationID, Fence: fence})
	}, func(_ context.Context, res *nodev1.RuntimeActionResponse) error {
		result = runtimeResult(node, res.GetRuntime(), nil)
		return nil
	})
	return result, err
}

func (c Controller) applyTorProxyNow(ctx context.Context, req Request) (RuntimeResult, error) {
	client, node, err := c.dial(ctx, req.NodeID)
	if err != nil {
		_ = c.repo.SetError(ctx, req.NodeID, err.Error())
		return RuntimeResult{}, friendlyNodeError("apply tor proxy", req.NodeID, err)
	}
	res, err := client.Runtime().ApplyTorProxy(ctx, &nodev1.TorProxyRequest{
		OperationId: req.OperationID,
		SocksPort:   req.TorSocksPort,
		ExitCountry: strings.TrimSpace(req.TorExitCountry),
		StrictExit:  req.TorStrictExit,
	})
	if err != nil {
		_ = c.repo.SetError(ctx, req.NodeID, err.Error())
		return RuntimeResult{}, friendlyNodeError("apply tor proxy", req.NodeID, err)
	}
	return runtimeResult(node, res.GetRuntime(), nil), nil
}

func (c Controller) configureWindscribeNow(ctx context.Context, req Request) (WindscribeResult, error) {
	client, node, err := c.dial(ctx, req.NodeID)
	if err != nil {
		return WindscribeResult{}, friendlyNodeError("configure Windscribe", req.NodeID, err)
	}
	res, err := client.Runtime().ConfigureWindscribe(ctx, &nodev1.WindscribeProxyRequest{
		OperationId:   req.OperationID,
		Action:        strings.TrimSpace(req.WindscribeAction),
		Username:      strings.TrimSpace(req.WindscribeUsername),
		Password:      req.WindscribePassword,
		Location:      strings.TrimSpace(req.WindscribeLocation),
		SocksPort:     req.WindscribeSocksPort,
		ProxyUsername: req.WindscribeProxyUsername,
		ProxyPassword: req.WindscribeProxyPassword,
	})
	if err != nil {
		return WindscribeResult{}, friendlyNodeError("configure Windscribe", req.NodeID, err)
	}
	locations := make([]WindscribeLocation, 0, len(res.GetLocations()))
	for _, location := range res.GetLocations() {
		locations = append(locations, WindscribeLocation{
			Name:      location.GetName(),
			Available: location.GetAvailable(),
		})
	}
	return WindscribeResult{
		Runtime:   runtimeResult(node, res.GetRuntime(), nil),
		Locations: locations,
	}, nil
}

func (c Controller) configurePsiphonNow(ctx context.Context, req Request) (PsiphonResult, error) {
	client, node, err := c.dial(ctx, req.NodeID)
	if err != nil {
		return PsiphonResult{}, friendlyNodeError("configure Psiphon", req.NodeID, err)
	}
	res, err := client.Runtime().ConfigurePsiphon(ctx, &nodev1.PsiphonProxyRequest{
		OperationId: req.OperationID,
		ConfigJson:  req.PsiphonConfigJSON,
		Action:      req.PsiphonAction,
		Locations:   req.PsiphonLocations,
		SocksPort:   req.PsiphonSocksPort,
	})
	if err != nil {
		return PsiphonResult{}, friendlyNodeError("configure Psiphon", req.NodeID, err)
	}
	instances := make([]PsiphonInstance, 0, len(res.GetInstances()))
	for _, instance := range res.GetInstances() {
		instances = append(instances, PsiphonInstance{
			Location:  instance.GetLocation(),
			SocksPort: instance.GetSocksPort(),
		})
	}
	return PsiphonResult{
		Runtime:   runtimeResult(node, res.GetRuntime(), nil),
		Instances: instances,
		Locations: res.GetLocations(),
	}, nil
}
