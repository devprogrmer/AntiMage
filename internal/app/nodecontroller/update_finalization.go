package nodecontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/antimage/antimage/internal/app/nodeclient"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func nodeServiceCommandID(id, action, operationID string) string {
	digest := sha256.Sum256([]byte(id + "|" + action + "|" + operationID))
	return "command-" + hex.EncodeToString(digest[:16])
}

// Finalization cancels a recovery watchdog, so a missing ACK is also an unknown
// destructive outcome. Persist intent first and inspect its durable receipt on
// recovery instead of sending the command again or starting a competing restore.
func (c Controller) finalizeNodeUpdate(ctx context.Context, client *nodeclient.Client, op NodeUpdateOperation, payload string) (*nodev1.RuntimeActionResponse, error) {
	if err := c.requireNodeExecutor(ctx, op.ID); err != nil {
		return nil, err
	}
	stored, err := operationapp.Get(ctx, c.repo.db, op.ID)
	if err != nil {
		return nil, err
	}
	commandID := nodeServiceCommandID(op.ID, "commit_update", op.ID)
	if stored.Metadata["finalization_dispatched"] == true {
		response, err := client.Control().Health(ctx, &nodev1.HealthRequest{OperationId: op.ID, CommandId: commandID})
		if err != nil || !nodeFinalizationReceiptMatches(stored, op, response.GetRuntime(), commandID) {
			return nil, ErrCommandOutcomeUnknown
		}
		if err := c.requireNodeExecutor(ctx, op.ID); err != nil {
			return nil, err
		}
		return &nodev1.RuntimeActionResponse{Accepted: true, Runtime: response.Runtime, Message: "Finalization reconciled from completed receipt and fresh runtime"}, nil
	}
	lease, ok := operationapp.ExecutorLeaseFromContext(ctx)
	if !ok {
		return nil, operationapp.ErrLeaseLost
	}
	stored.Metadata["finalization_dispatched"] = true
	stored.Metadata["finalization_resource_generation"] = lease.ResourceGeneration
	stored.Metadata["finalization_command_id"] = commandID
	if err := operationapp.Save(ctx, c.repo.db, c.repo.dialect, stored); err != nil {
		return nil, err
	}
	return c.sendFencedServiceUpdate(ctx, client, op.ID, &nodev1.ServiceUpdateRequest{OperationId: op.ID, Action: "commit_update", Version: op.DesiredVersion, ResolvedBuildJson: payload})
}

func nodeFinalizationReceiptMatches(stored operationapp.Operation, op NodeUpdateOperation, state *nodev1.RuntimeState, commandID string) bool {
	var generation int64
	_, _ = fmt.Sscan(fmt.Sprint(stored.Metadata["finalization_resource_generation"]), &generation)
	if state == nil || generation <= 0 || op.ResolvedTarget == nil {
		return false
	}
	return state.EvidenceOperationId == op.ID && state.EvidenceCommandId == commandID && state.EvidenceCommandState == "completed" && state.EvidenceResourceGeneration == generation && state.CurrentResourceGeneration >= generation && nodeVersionMatchesRequested(state.NodeVersion, op.DesiredVersion) && state.CommitSha == op.ResolvedTarget.Commit && state.Connected && state.Started && state.ProcessStartedAtUnixNano > op.RestartRequestedAt.UnixNano() && state.SampledAtUnixNano >= time.Now().Add(-30*time.Second).UnixNano() && state.SampledAtUnixNano <= time.Now().Add(time.Second).UnixNano()
}
